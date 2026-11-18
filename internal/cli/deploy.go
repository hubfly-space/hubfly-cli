package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"hubfly-cli/internal/version"
)

const (
	builderRepoOwner = "hubfly-space"
	builderRepoName  = "hubfly-builder"
)

type builderInstallState struct {
	Version   string `json:"version,omitempty"`
	Source    string `json:"source,omitempty"`
	Checksum  string `json:"checksum,omitempty"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

func deployFlow(forceAdvanced bool) error {
	return deployFlowWithOptions(deployOptions{Advanced: forceAdvanced})
}

func deployFlowWithOptions(opts deployOptions) error {
	ctx, stopSignals := newDeployContext()
	defer stopSignals()

	token, err := ensureAuth(true)
	if err != nil {
		return err
	}

	projectDir, cfgPath, err := resolveDeployWorkspace(opts.ConfigPath)
	if err != nil {
		return err
	}

	cfg, created, err := loadOrInitDeployConfigAt(projectDir, cfgPath)
	if err != nil {
		return err
	}
	if cfg.Version < 2 {
		return fmt.Errorf("hubfly.build.json schema version %d requires explicit migration; run `hubfly config migrate --config %s`", cfg.Version, cfgPath)
	}
	quietJSONPlan := opts.PlanOnly && opts.JSON
	if !quietJSONPlan {
		printDeployHeader(projectDir)
	}
	if opts.PullOnly {
		return deployPullCloudConfig(token, projectDir, cfgPath, cfg, opts)
	}
	normalizeDeployConfig(&cfg, projectDir)
	applyDeployOverrides(&cfg, opts)

	editConfig := opts.Advanced
	if !quietJSONPlan && !opts.Advanced && !opts.AutoApprove && isInteractiveShell() {
		editConfig, err = promptDeployReviewChoice(created)
		if err != nil {
			return err
		}
	}

	if editConfig {
		if err := openDeployConfigEditor(cfgPath); err != nil {
			return err
		}
		cfg, _, err = loadOrInitDeployConfigAt(projectDir, cfgPath)
		if err != nil {
			return err
		}
		normalizeDeployConfig(&cfg, projectDir)
		applyDeployOverrides(&cfg, opts)
	}

	prepared, err := prepareDeployBuild(projectDir, cfgPath, &cfg, opts.BuilderVersion)
	if err != nil {
		return err
	}
	contextWarnings := buildContextWarnings(scanBuildContext(filepath.Join(projectDir, cfg.Build.ContextDir)))
	if err := ensureDeployProjectBinding(token, projectDir, &cfg, opts); err != nil {
		if errors.Is(err, errPlanNeedsNewProject) {
			// A dry run must not create projects or rewrite the manifest.
			if !quietJSONPlan {
				printDeploySummary(projectDir, cfg, prepared)
				printDeployWarnings("Builder warnings", prepared.Warnings)
				printDeployWarnings("Build context", contextWarnings)
				printDeployDiffPlan(buildDeployDiffPlan(projectDir, cfg, prepared, nil))
			}
			return printPlanForNewProject(opts)
		}
		return err
	}
	cfg.Metadata.BuilderVersion = prepared.BuilderVersion
	if !opts.PlanOnly {
		if err := saveDeployConfig(cfgPath, cfg); err != nil {
			return err
		}
	}

	current, err := loadBoundContainerSnapshot(token, &cfg)
	if err != nil {
		return err
	}
	if opts.Mode == "smart" {
		applySmartCloudDefaults(&cfg, current)
	}
	if current != nil && !opts.PlanOnly {
		active, activeErr := fetchActiveDeploySession(token, current.Container.ID)
		if activeErr != nil {
			return fmt.Errorf("check active deployment: %w", activeErr)
		}
		if active.Session != nil {
			if !opts.ForceNew {
				fmt.Println(activeDeploymentMessage(active.Session.ID, active.Session.Status, active.Session.Phase))
				if opts.Detach {
					return &deployExitError{code: exitDeployConflict, err: fmt.Errorf("deployment %s is already active; your changes were not deployed", active.Session.ID)}
				}
				fmt.Println("Following it; your local changes will NOT be deployed by this run.")
				attached, waitErr := waitForDeploySessionContext(ctx, token, active.Session.ID)
				if waitErr != nil {
					return waitErr
				}
				return finishAttachedDeployment(attached, active.Session.ID)
			}
			if !opts.AutoApprove {
				if !isInteractiveShell() {
					return fmt.Errorf("--force-new requires --yes in non-interactive mode")
				}
				confirmed, promptErr := promptYesNo("Cancel the active deployment and start a new rollout", false)
				if promptErr != nil {
					return promptErr
				}
				if !confirmed {
					return fmt.Errorf("deployment cancelled")
				}
			}
			if cancelErr := cancelDeploySession(token, active.Session.ID); cancelErr != nil {
				return fmt.Errorf("cancel active deployment: %w", cancelErr)
			}
		}
	}

	if !quietJSONPlan {
		printDeploySummary(projectDir, cfg, prepared)
		printDeployWarnings("Builder warnings", prepared.Warnings)
		printDeployWarnings("Build context", contextWarnings)
	}
	diffPlan := applyDeployModeToDiff(buildDeployDiffPlan(projectDir, cfg, prepared, current), opts.Mode)
	if !quietJSONPlan {
		printDeployDiffPlan(diffPlan)
	}

	deploymentConfig := buildDeploymentConfig(cfg)
	cloudRevision := ""
	if current != nil {
		cloudRevision = current.Container.Revision
	}
	serverPlan, err := createDeployPlan(token, createDeployPlanRequest{
		ProjectID:         cfg.Project.ID,
		ContainerID:       strings.TrimSpace(cfg.Container.ID),
		ClientVersion:     version.Version,
		Mode:              opts.Mode,
		BaseCloudRevision: cloudRevision,
		DesiredState:      deploymentConfig,
	})
	if err != nil {
		return fmt.Errorf("create server deployment plan: %w", err)
	}
	if opts.PlanOnly && opts.JSON {
		return json.NewEncoder(os.Stdout).Encode(serverPlan)
	}
	printServerDeployPlan(serverPlan)
	if opts.PlanOnly {
		return nil
	}
	if len(serverPlan.DestructiveOperations) > 0 {
		diffPlan.HasDestructive = true
	}
	if err := confirmDeployPlan(opts, diffPlan); err != nil {
		return err
	}
	// One key per deploy invocation, reused by every retry of the POST below.
	session, err := createDeploySessionWithMissingBoundFallback(ctx, token, cfgPath, &cfg, createDeploySessionRequest{
		PlanID:               serverPlan.PlanID,
		PlanHash:             serverPlan.PlanHash,
		BaseCloudRevision:    serverPlan.CurrentRevision,
		ClientIdempotencyKey: newDeployIdempotencyKey("cli-session:" + serverPlan.PlanHash),
		BuilderVersion:       prepared.BuilderVersion,
		BoundContainerID:     strings.TrimSpace(cfg.Container.ID),
		ApplyMode:            opts.Mode,
		Config:               deploymentConfig,
	}, opts)
	if err != nil {
		// Another CLI process may have acquired the container lease after the
		// preflight active-session check above. Treat that race exactly like an
		// active session discovered during preflight: attach to the winner
		// instead of making the losing process fail with a raw 409.
		if current != nil && !opts.ForceNew && isActiveDeploymentConflict(err) {
			active, activeErr := fetchActiveDeploySession(token, current.Container.ID)
			if activeErr == nil && active.Session != nil {
				fmt.Println(activeDeploymentMessage(active.Session.ID, active.Session.Status, active.Session.Phase))
				if opts.Detach {
					return &deployExitError{code: exitDeployConflict, err: fmt.Errorf("deployment %s became active while this deploy was starting; your changes were not deployed", active.Session.ID)}
				}
				fmt.Println("Following it; your local changes will NOT be deployed by this run.")
				attached, waitErr := waitForDeploySessionContext(ctx, token, active.Session.ID)
				if waitErr != nil {
					return waitErr
				}
				return finishAttachedDeployment(attached, active.Session.ID)
			}
			return withDeployExitCode(exitDeployConflict, err)
		}
		if ctx.Err() != nil {
			var coded *deployExitError
			if errors.As(err, &coded) {
				return err
			}
			return interruptedBeforeSession()
		}
		return err
	}

	localTag := generateLocalImageTag(session.BuildID)
	fmt.Printf("Deploy build id: %s\n", session.BuildID)
	printDeployStep(
		"Local build",
		fmt.Sprintf("docker build using %s", displayDeployBuildSource(projectDir, prepared)),
	)
	if err := buildLocalImage(ctx, projectDir, prepared.DockerfilePath, localTag, cfg); err != nil {
		if ctx.Err() != nil {
			return interruptedBeforeRollout(token, session, "the local build")
		}
		_ = reportDeployFailure(
			token,
			session.BuildID,
			session.Upload.Token,
			"Local build failed: "+err.Error(),
		)
		return &deployExitError{code: exitDeployBuildFailed, err: fmt.Errorf("local docker build failed: %w (session %s marked failed; nothing was deployed)", err, session.BuildID)}
	}
	defer func() {
		_ = removeLocalImage(localTag)
	}()

	printDeployStep(
		"Image upload",
		fmt.Sprintf("Streaming image to %s (%s)", session.Region.Name, session.Region.PrimaryIP),
	)
	imageDigest, err := uploadLocalImageContext(ctx, localTag, session)
	if err != nil {
		if ctx.Err() != nil && errors.Is(ctx.Err(), context.Canceled) {
			return interruptedBeforeRollout(token, session, "the image upload")
		}
		_ = reportDeployFailure(
			token,
			session.BuildID,
			session.Upload.Token,
			"Image upload failed: "+err.Error(),
		)
		return &deployExitError{code: exitDeployUploadFailed, err: fmt.Errorf("%w (session %s marked failed; nothing was deployed)", err, session.BuildID)}
	}
	if err := completeDeployUploadWithRetry(ctx, token, session, imageDigest); err != nil {
		return withDeployExitCode(exitDeployUnknown, fmt.Errorf(
			"finalize uploaded image: %w. The image is uploaded; check `hubfly deploy status %s` before redeploying",
			err, session.BuildID,
		))
	}

	cfg.Metadata.BuilderVersion = prepared.BuilderVersion
	cfg.Metadata.LastBuildID = session.BuildID
	if err := saveDeployConfig(cfgPath, cfg); err != nil {
		return err
	}

	if opts.Detach {
		fmt.Printf("Upload finished. Deployment is continuing in Hubfly.\n")
		fmt.Printf("Project:   %s (%s)\n", session.ProjectName, session.ProjectID)
		fmt.Printf("Build ID:  %s\n", session.BuildID)
		fmt.Printf("Config:    %s\n", cfgPath)
		return nil
	}

	status, err := waitForDeploySessionContext(ctx, token, session.BuildID)
	if err != nil {
		return err
	}
	if outcomeErr := deployOutcomeError(status, "deployment "+session.BuildID); outcomeErr != nil {
		return outcomeErr
	}

	if strings.TrimSpace(status.Build.BoundContainerID) != "" {
		cfg.Container.ID = status.Build.BoundContainerID
	}
	cfg.Project.ID = status.Build.ProjectID
	cfg.Project.Name = status.Build.ProjectName
	cfg.Project.Region = status.Build.RegionID
	cfg.Metadata.BuilderVersion = prepared.BuilderVersion
	cfg.Metadata.LastBuildID = status.Build.ID
	cfg.Metadata.LastImageDisplay = status.Build.ImageDisplay
	cfg.Metadata.LastDeployedAt = time.Now().UTC().Format(time.RFC3339)
	if err := saveDeployConfig(cfgPath, cfg); err != nil {
		return err
	}

	fmt.Printf("Deployment succeeded.\n")
	fmt.Printf("Project:   %s (%s)\n", status.Build.ProjectName, status.Build.ProjectID)
	fmt.Printf("Container: %s (%s)\n", cfg.Container.Name, cfg.Container.ID)
	fmt.Printf("Image:     %s\n", displayDeployValue(status.Build.ImageDisplay, "Managed by Hubfly"))
	fmt.Printf("Config:    %s\n", cfgPath)
	return nil
}

func deployPullCloudConfig(token, projectDir, cfgPath string, cfg deployConfigFile, opts deployOptions) error {
	if strings.TrimSpace(cfg.Container.ID) == "" {
		return fmt.Errorf("deploy pull requires a bound container ID")
	}
	snapshot, err := fetchDeployContainerSnapshot(token, cfg.Container.ID)
	if err != nil {
		return err
	}
	updated := cfg
	updated.Container.Name = snapshot.Container.Name
	updated.Project.ID = snapshot.Container.ProjectID
	updated.Project.Name = snapshot.Container.ProjectName
	updated.Deploy.Resources = deployResources{CPU: snapshot.Container.Resources.CPU, RAM: snapshot.Container.Resources.RAM, Storage: snapshot.Container.Resources.Storage}
	updated.Deploy.Runtime = deployRuntime{AutoSleep: snapshot.Container.Runtime.AutoSleep, AutoScale: snapshot.Container.Runtime.AutoScale, Is24x7: snapshot.Container.Runtime.Is24x7, AutoScaleMode: snapshot.Container.Runtime.AutoScaleMode}
	updated.Deploy.Ports = make([]deployPort, 0, len(snapshot.Container.Ports))
	for _, port := range snapshot.Container.Ports {
		updated.Deploy.Ports = append(updated.Deploy.Ports, deployPort{ID: port.ID, Container: port.Container, Protocol: normalizePortProtocol(port.Protocol)})
	}
	updated.Deploy.Volumes = make([]deployVolume, 0, len(snapshot.Container.Volumes))
	for _, volume := range snapshot.Container.Volumes {
		updated.Deploy.Volumes = append(updated.Deploy.Volumes, deployVolume{Name: volume.ID, MountPath: volume.MountPoint})
	}
	updated.Deploy.Process = deployProcess{Command: cloneStrings(snapshot.Container.Process.Command), Entrypoint: cloneStrings(snapshot.Container.Process.Entrypoint), WorkingDir: snapshot.Container.Process.WorkingDir}
	if snapshot.Container.Healthcheck != nil {
		updated.Deploy.Healthcheck = &deployHealthcheck{Test: cloneStrings(snapshot.Container.Healthcheck.Test), Interval: snapshot.Container.Healthcheck.Interval, Timeout: snapshot.Container.Healthcheck.Timeout, StartPeriod: snapshot.Container.Healthcheck.StartPeriod, Retries: snapshot.Container.Healthcheck.Retries}
	} else {
		updated.Deploy.Healthcheck = nil
	}
	updated.Deploy.RestartPolicy = &deployRestartPolicy{Name: snapshot.Container.RestartPolicy}
	updated.Deploy.Labels = snapshot.Container.Labels
	localEnv := make(map[string]deployEnvVar, len(cfg.Env))
	for _, item := range cfg.Env {
		localEnv[item.Name] = item
	}
	updated.Env = make([]deployEnvVar, 0, len(snapshot.Container.Environment))
	for _, cloud := range snapshot.Container.Environment {
		item, exists := localEnv[cloud.Key]
		if !exists || cloud.IsSecret {
			item = deployEnvVar{Name: cloud.Key, Secret: cloud.IsSecret, Scope: "both", From: "cloud"}
		}
		if cloud.IsSecret {
			item.Value = ""
			item.From = "cloud"
		}
		updated.Env = append(updated.Env, item)
	}
	oldPayload, _ := json.MarshalIndent(cfg, "", "  ")
	newPayload, _ := json.MarshalIndent(updated, "", "  ")
	fmt.Printf("Cloud adoption diff for %s:\n", cfgPath)
	fmt.Printf("  ports: %d -> %d\n  volumes: %d -> %d\n  environment keys: %d -> %d\n", len(cfg.Deploy.Ports), len(updated.Deploy.Ports), len(cfg.Deploy.Volumes), len(updated.Deploy.Volumes), len(cfg.Env), len(updated.Env))
	if string(oldPayload) == string(newPayload) {
		fmt.Println("No configuration changes.")
		return nil
	}
	if configHasUncommittedChanges(projectDir, cfgPath) && !opts.AutoApprove {
		if !isInteractiveShell() {
			return fmt.Errorf("refusing to overwrite an uncommitted manifest without --yes")
		}
		confirmed, promptErr := promptYesNo("Manifest has uncommitted changes. Create a backup and adopt cloud configuration", false)
		if promptErr != nil {
			return promptErr
		}
		if !confirmed {
			return fmt.Errorf("cloud adoption declined")
		}
	}
	backup := fmt.Sprintf("%s.%s.bak", cfgPath, time.Now().UTC().Format("20060102T150405Z"))
	if err := os.WriteFile(backup, append(oldPayload, '\n'), 0o600); err != nil {
		return err
	}
	if err := saveDeployConfig(cfgPath, updated); err != nil {
		return err
	}
	fmt.Printf("Updated %s\nBackup: %s\n", cfgPath, backup)
	return nil
}

func configHasUncommittedChanges(projectDir, cfgPath string) bool {
	relative, err := filepath.Rel(projectDir, cfgPath)
	if err != nil {
		return false
	}
	command := exec.Command("git", "diff", "--quiet", "--", relative)
	command.Dir = projectDir
	if err := command.Run(); err != nil {
		return true
	}
	command = exec.Command("git", "diff", "--cached", "--quiet", "--", relative)
	command.Dir = projectDir
	return command.Run() != nil
}

func printServerDeployPlan(plan deployPlanResponse) {
	fmt.Printf("\nServer plan %s (cloud revision %s)\n", plan.PlanID, plan.CurrentRevision)
	for _, operation := range plan.Operations {
		if operation.Action == "preserve" && operation.Origin == "cloud" {
			continue
		}
		marker := " "
		if operation.Destructive {
			marker = "!"
		}
		fmt.Printf(" %s %-9s %-28s [%s]", marker, operation.Action, operation.Path, operation.Origin)
		if operation.Message != "" {
			fmt.Printf(" — %s", operation.Message)
		}
		fmt.Println()
	}
	printDeployWarnings("Server warnings", plan.Warnings)
}

func applySmartCloudDefaults(cfg *deployConfigFile, current *deployContainerSnapshotResponse) {
	if current == nil || len(cfg.Deploy.Ports) == 0 {
		return
	}
	remaining := append([]cliDeploymentPort(nil), current.Container.Ports...)
	for idx := range cfg.Deploy.Ports {
		local := &cfg.Deploy.Ports[idx]
		match := -1
		for cloudIdx, cloud := range remaining {
			if local.ID != "" && local.ID == cloud.ID {
				match = cloudIdx
				break
			}
			if local.Container == cloud.Container && normalizePortProtocol(local.Protocol) == normalizePortProtocol(cloud.Protocol) {
				match = cloudIdx
				break
			}
		}
		// Version-1 manifests could only express TCP/UDP and therefore encoded
		// dashboard HTTP ports as TCP. A unique cloud port on the same container
		// port is the authoritative protocol in smart mode.
		if match < 0 && cfg.Version < 2 {
			for cloudIdx, cloud := range remaining {
				if local.Container == cloud.Container {
					if match >= 0 {
						match = -1
						break
					}
					match = cloudIdx
				}
			}
		}
		if match < 0 {
			continue
		}
		cloud := remaining[match]
		local.ID = cloud.ID
		if cfg.Version < 2 {
			local.Protocol = normalizePortProtocol(cloud.Protocol)
		}
		remaining = append(remaining[:match], remaining[match+1:]...)
	}
}

func applyDeployOverrides(cfg *deployConfigFile, opts deployOptions) {
	if strings.TrimSpace(opts.DockerfilePath) != "" {
		cfg.Build.Mode = "dockerfile"
		cfg.Build.DockerfilePath = strings.TrimSpace(opts.DockerfilePath)
	}
}

func loadBoundContainerSnapshot(token string, cfg *deployConfigFile) (*deployContainerSnapshotResponse, error) {
	containerID := strings.TrimSpace(cfg.Container.ID)
	if containerID == "" {
		return nil, nil
	}

	snapshot, err := fetchDeployContainerSnapshot(token, containerID)
	if err != nil {
		if apiErr, ok := err.(*apiError); ok && apiErr.Status == http.StatusNotFound {
			fmt.Fprintf(
				os.Stderr,
				"Warning: bound container %s no longer exists. The next deploy will create a new container.\n",
				containerID,
			)
			cfg.Container.ID = ""
			return nil, nil
		}
		return nil, err
	}
	return &snapshot, nil
}

func createDeploySessionWithMissingBoundFallback(
	ctx context.Context,
	token, cfgPath string,
	cfg *deployConfigFile,
	req createDeploySessionRequest,
	opts deployOptions,
) (deploySessionResponse, error) {
	session, err := createDeploySessionResilient(ctx, token, req)
	if !isMissingBoundContainerError(err) || strings.TrimSpace(cfg.Container.ID) == "" {
		return session, err
	}

	missingContainerID := strings.TrimSpace(cfg.Container.ID)
	shouldCreateNew := opts.AutoApprove
	if !shouldCreateNew {
		if !isInteractiveShell() {
			return deploySessionResponse{}, fmt.Errorf(
				"bound container %s was not found; rerun with --yes to clear it and create a new container",
				missingContainerID,
			)
		}

		var promptErr error
		shouldCreateNew, promptErr = promptCreateNewContainerInstead(missingContainerID)
		if promptErr != nil {
			return deploySessionResponse{}, promptErr
		}
		if !shouldCreateNew {
			return deploySessionResponse{}, fmt.Errorf("deployment cancelled")
		}
	}

	fmt.Fprintf(
		os.Stderr,
		"Bound container %s was not found. Retrying this deploy as a new container.\n",
		missingContainerID,
	)
	cfg.Container.ID = ""
	if err := saveDeployConfig(cfgPath, *cfg); err != nil {
		return deploySessionResponse{}, err
	}

	req.BoundContainerID = ""
	return createDeploySessionResilient(ctx, token, req)
}

func isMissingBoundContainerError(err error) bool {
	var apiErr *apiError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
		return false
	}

	return strings.Contains(apiErr.Message, "Bound container not found in this project.")
}

func isActiveDeploymentConflict(err error) bool {
	var apiErr *apiError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict {
		return false
	}
	if apiErr.Code == "ACTIVE_DEPLOYMENT" {
		return true
	}
	message := strings.ToLower(apiErr.Message)
	return strings.Contains(message, "already active") || strings.Contains(message, "active deployment")
}

func prepareDeployBuild(projectDir, cfgPath string, cfg *deployConfigFile, requestedBuilderVersion string) (deployPreparedBuild, error) {
	if hasCustomDockerfileConfig(*cfg) {
		dockerfilePath, err := resolveConfiguredDockerfilePath(projectDir, *cfg)
		if err != nil {
			return deployPreparedBuild{}, err
		}
		cfg.Metadata.BuilderVersion = ""
		return deployPreparedBuild{
			DockerfilePath:  dockerfilePath,
			BuilderVersion:  "",
			BuildSource:     "dockerfile",
			BuildSourcePath: dockerfilePath,
		}, nil
	}

	if dockerfilePath, ok := findProjectDockerfile(projectDir, *cfg); ok {
		cfg.Metadata.BuilderVersion = ""
		return deployPreparedBuild{
			DockerfilePath:  dockerfilePath,
			BuilderVersion:  "",
			BuildSource:     "dockerfile",
			BuildSourcePath: dockerfilePath,
		}, nil
	}

	builderPath, builderVersion, err := ensureLocalBuilderBinary(builderInstallRequest{
		RequestedVersion: requestedBuilderVersion,
		CheckLatest:      true,
	})
	if err != nil {
		return deployPreparedBuild{}, err
	}

	inspect, err := runBuilderInspect(builderPath, projectDir, cfgPath)
	if err != nil {
		return deployPreparedBuild{}, wrapBuilderInspectError(projectDir, *cfg, err)
	}

	applyInspectOutput(cfg, inspect)
	dockerfilePath, err := writeGeneratedDockerfile(projectDir, inspect.Dockerfile)
	if err != nil {
		return deployPreparedBuild{}, err
	}

	cfg.Metadata.BuilderVersion = builderVersion
	return deployPreparedBuild{
		DockerfilePath:  dockerfilePath,
		BuilderVersion:  builderVersion,
		BuildSource:     "generated",
		BuildSourcePath: dockerfilePath,
		Warnings:        cloneStrings(inspect.BuildConfig.ValidationWarnings),
	}, nil
}

func hasCustomDockerfileConfig(cfg deployConfigFile) bool {
	return normalizeBuildMode(cfg.Build.Mode) == "dockerfile" || strings.TrimSpace(cfg.Build.DockerfilePath) != ""
}

func resolveConfiguredDockerfilePath(projectDir string, cfg deployConfigFile) (string, error) {
	path := strings.TrimSpace(cfg.Build.DockerfilePath)
	if path == "" {
		path = "Dockerfile"
	}
	return validateDockerfilePath(projectDir, path)
}

func findProjectDockerfile(projectDir string, cfg deployConfigFile) (string, bool) {
	candidates := make([]string, 0, 2)
	workingDir := strings.TrimSpace(cfg.Build.WorkingDir)
	if workingDir != "" && workingDir != "." {
		candidates = append(candidates, filepath.Join(workingDir, "Dockerfile"))
	}
	candidates = append(candidates, "Dockerfile")

	for _, candidate := range candidates {
		path, err := validateDockerfilePath(projectDir, candidate)
		if err == nil {
			return path, true
		}
	}
	return "", false
}

func validateDockerfilePath(projectDir, dockerfilePath string) (string, error) {
	if strings.TrimSpace(dockerfilePath) == "" {
		return "", fmt.Errorf("dockerfile path is required")
	}

	projectDir, err := filepath.Abs(projectDir)
	if err != nil {
		return "", err
	}

	resolved := dockerfilePath
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(projectDir, filepath.Clean(dockerfilePath))
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", err
	}
	relPath, err := filepath.Rel(projectDir, resolved)
	if err != nil {
		return "", err
	}
	if relPath == ".." || strings.HasPrefix(relPath, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("dockerfile path must stay inside the project directory: %s", dockerfilePath)
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("dockerfile not found: %s", dockerfilePath)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("dockerfile path is not a regular file: %s", dockerfilePath)
	}
	return resolved, nil
}

func wrapBuilderInspectError(projectDir string, cfg deployConfigFile, cause error) error {
	message := strings.TrimSpace(cause.Error())
	if hasCustomDockerfileConfig(cfg) {
		return fmt.Errorf("failed to prepare build with the configured Dockerfile: %s", message)
	}

	return fmt.Errorf(
		"%s\nAdd a project Dockerfile and run `hubfly deploy` again, or set `build.mode` to `dockerfile` in %s to use a custom Dockerfile directly.",
		message,
		deployConfigPath(projectDir),
	)
}

func resolveOrgID(token, orgFilter string) (string, error) {
	if orgFilter == "" {
		return "", nil
	}
	orgs, err := fetchOrganizations(token)
	if err != nil {
		return "", err
	}
	for _, o := range orgs {
		if o.ID == orgFilter || o.Slug == orgFilter {
			return o.ID, nil
		}
	}
	return "", fmt.Errorf("organization '%s' not found", orgFilter)
}

func ensureDeployProjectBinding(token, projectDir string, cfg *deployConfigFile, opts deployOptions) error {
	// Global projects (gpj_…) deploy to their Cell project in one region.
	if globalID, region, ok := globalDeployTarget(token, cfg, opts); ok {
		return bindGlobalDeployProject(token, cfg, opts, globalID, region)
	}

	orgID, err := resolveOrgID(token, opts.Org)
	if err != nil {
		return err
	}

	var projects []project
	if orgID != "" {
		projects, err = fetchProjectsWithOrg(token, orgID)
	} else {
		projects, err = fetchProjects(token)
	}
	if err != nil {
		return err
	}

	requestedProject := strings.TrimSpace(opts.Project)
	if requestedProject != "" {
		if strings.EqualFold(requestedProject, "new") {
			return createProjectBinding(token, projectDir, cfg, opts, "")
		}
		if matched, ok := resolveRequestedProject(projects, requestedProject); ok {
			if regionOverride := strings.TrimSpace(opts.Region); regionOverride != "" &&
				!regionMatchesQuery(matched.Region, regionOverride) {
				return fmt.Errorf(
					"project %s is in region %s, not %s",
					matched.Name,
					matched.Region.Name,
					regionOverride,
				)
			}
			cfg.Project.ID = matched.ID
			cfg.Project.Name = matched.Name
			cfg.Project.Region = matched.Region.ID
			return nil
		}
		if strings.HasPrefix(requestedProject, "prj_") {
			if handled, err := bindGlobalChildDeployProject(token, cfg, opts, requestedProject, ""); handled || err != nil {
				return err
			}
		}
		return createProjectBinding(token, projectDir, cfg, opts, requestedProject)
	}

	projectID := strings.TrimSpace(cfg.Project.ID)
	if projectID != "" {
		for _, p := range projects {
			if p.ID == projectID {
				cfg.Project.ID = p.ID
				cfg.Project.Name = p.Name
				cfg.Project.Region = p.Region.ID
				return nil
			}
		}
		// Global projects' Cell projects are not in the project list.
		if handled, err := bindGlobalChildDeployProject(token, cfg, opts, projectID, cfg.Project.Region); handled || err != nil {
			return err
		}
		cfg.Project.ID = ""
	}

	if len(projects) == 0 {
		return createProjectBinding(token, projectDir, cfg, opts, "")
	}

	if !isInteractiveShell() {
		return createProjectBinding(token, projectDir, cfg, opts, "")
	}

	selection, err := selectProjectIndex(projects)
	if err != nil {
		return err
	}

	if selection == 1 {
		return createProjectBinding(token, projectDir, cfg, opts, "")
	}

	selected := projects[selection-2]
	cfg.Project.ID = selected.ID
	cfg.Project.Name = selected.Name
	cfg.Project.Region = selected.Region.ID
	return nil
}

func createProjectBinding(token, projectDir string, cfg *deployConfigFile, opts deployOptions, requestedName string) error {
	if opts.PlanOnly {
		return errPlanNeedsNewProject
	}
	regions, err := fetchRegions(token)
	if err != nil {
		return err
	}

	availableRegions := make([]region, 0, len(regions))
	for _, entry := range regions {
		if entry.Available {
			availableRegions = append(availableRegions, entry)
		}
	}
	if len(availableRegions) == 0 {
		availableRegions = regions
	}
	if len(availableRegions) == 0 {
		return fmt.Errorf("no regions available")
	}

	projectName := strings.TrimSpace(requestedName)
	if projectName == "" {
		projectName = defaultProjectName(projectDir, cfg.Project.Name)
	}
	if isInteractiveShell() && strings.TrimSpace(requestedName) == "" && !opts.AutoApprove {
		projectName, err = promptStringWithDefault("Project name", projectName)
		if err != nil {
			return err
		}
	}

	selectedRegion, err := resolveDeployRegion(availableRegions, strings.TrimSpace(opts.Region), strings.TrimSpace(cfg.Project.Region))
	if err != nil {
		return err
	}

	orgID, err := resolveOrgID(token, opts.Org)
	if err != nil {
		return err
	}

	createdProject, err := createProjectForDeploy(token, projectName, selectedRegion.ID, orgID)
	if err != nil {
		return err
	}

	cfg.Project.ID = createdProject.ID
	cfg.Project.Name = createdProject.Name
	cfg.Project.Region = createdProject.Region.ID
	return nil
}

func resolveRequestedProject(projects []project, query string) (project, bool) {
	query = strings.TrimSpace(query)
	if query == "" {
		return project{}, false
	}
	lower := strings.ToLower(query)
	for _, candidate := range projects {
		if candidate.ID == query || strings.EqualFold(candidate.Name, query) {
			return candidate, true
		}
		if strings.ToLower(strings.TrimSpace(candidate.Name)) == lower {
			return candidate, true
		}
	}
	return project{}, false
}

func resolveDeployRegion(availableRegions []region, requested, fallback string) (region, error) {
	if regionQuery := strings.TrimSpace(requested); regionQuery != "" {
		for _, candidate := range availableRegions {
			if regionMatchesQuery(candidate, regionQuery) {
				return candidate, nil
			}
		}
		return region{}, fmt.Errorf("region %q is not available for deploy", regionQuery)
	}

	if regionQuery := strings.TrimSpace(fallback); regionQuery != "" {
		for _, candidate := range availableRegions {
			if regionMatchesQuery(candidate, regionQuery) {
				return candidate, nil
			}
		}
	}

	if !isInteractiveShell() {
		return region{}, fmt.Errorf("region is required in non-interactive mode. Pass --region <region-id>")
	}

	selection, err := selectRegionIndex(availableRegions)
	if err != nil {
		return region{}, err
	}
	return availableRegions[selection-1], nil
}

func regionMatchesQuery(candidate region, query string) bool {
	query = strings.TrimSpace(query)
	if query == "" {
		return false
	}
	return candidate.ID == query ||
		strings.EqualFold(candidate.Name, query) ||
		strings.EqualFold(candidate.Location, query)
}

func promptMenuSelection(label string, max, defaultValue int) (int, error) {
	for {
		value, err := promptNumberWithDefault(label, defaultValue)
		if err != nil {
			return 0, err
		}
		if value < 1 || value > max {
			fmt.Println("Selection out of range.")
			continue
		}
		return value, nil
	}
}

func defaultProjectName(projectDir, current string) string {
	current = strings.TrimSpace(current)
	if current != "" {
		return current
	}
	return filepath.Base(projectDir)
}

func ensureLocalBuilderBinary(request builderInstallRequest) (string, string, error) {
	targetPath := builderBinaryPath()
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return "", "", err
	}
	requestedVersion := normalizeVersion(request.RequestedVersion)

	downloadURL := strings.TrimSpace(os.Getenv("HUBFLY_BUILDER_URL"))
	if downloadURL != "" {
		sourceKey := builderInstallSourceForURL(downloadURL)
		if path, version, ok := reuseInstalledBuilder(targetPath, sourceKey, ""); ok {
			fmt.Printf("Using hubfly-builder %s\n", version)
			return path, version, nil
		}
		fmt.Printf("Downloading hubfly-builder from %s\n", downloadURL)
		version, err := installBuilderFromURL(targetPath, downloadURL)
		if err != nil {
			return cachedBuilderOrVersionError(targetPath, requestedVersion, err)
		}
		_ = saveBuilderInstallState(builderInstallState{
			Version:   version,
			Source:    sourceKey,
			UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		})
		fmt.Printf("Using hubfly-builder %s (downloaded now)\n", version)
		return targetPath, version, nil
	}

	release, _, err := fetchBuilderReleaseCached(request.RequestedVersion, request.CheckLatest)
	if err != nil {
		return cachedBuilderOrVersionError(
			targetPath,
			requestedVersion,
			fmt.Errorf("failed to fetch hubfly-builder release metadata: %w", err),
		)
	}

	assetName, err := expectedBuilderAssetName(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return cachedBuilderOrError(targetPath, err)
	}

	sourceKey := builderInstallSourceForRelease(release.TagName, assetName)
	if path, version, ok := reuseInstalledBuilder(targetPath, sourceKey, release.TagName); ok {
		fmt.Printf("Using hubfly-builder %s\n", version)
		return path, version, nil
	}

	fmt.Printf("Downloading hubfly-builder %s for %s/%s\n", release.TagName, runtime.GOOS, runtime.GOARCH)
	version, err := installBuilderFromRelease(targetPath, release, assetName)
	if err != nil {
		return cachedBuilderOrVersionError(
			targetPath,
			requestedVersion,
			fmt.Errorf("failed to install hubfly-builder %s for %s/%s: %w", release.TagName, runtime.GOOS, runtime.GOARCH, err),
		)
	}
	if strings.TrimSpace(version) == "" || strings.EqualFold(version, "unknown") {
		version = strings.TrimSpace(release.TagName)
	}
	_ = saveBuilderInstallState(builderInstallState{
		Version:   version,
		Source:    sourceKey,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	})
	fmt.Printf("Using hubfly-builder %s (downloaded now)\n", version)
	return targetPath, version, nil
}

func reuseInstalledBuilder(targetPath, sourceKey, expectedVersion string) (string, string, bool) {
	version, err := probeBuilderVersion(targetPath)
	if err != nil {
		return "", "", false
	}

	if expectedVersion != "" && builderVersionsMatch(version, expectedVersion) {
		return targetPath, preferredBuilderVersion(version, expectedVersion), true
	}

	state, err := loadBuilderInstallState()
	if err != nil {
		return "", "", false
	}
	if strings.TrimSpace(state.Source) != strings.TrimSpace(sourceKey) {
		return "", "", false
	}

	return targetPath, preferredBuilderVersion(version, state.Version, expectedVersion), true
}

func builderInstallSourceForURL(downloadURL string) string {
	return "url:" + strings.TrimSpace(downloadURL)
}

func builderInstallSourceForRelease(tagName, assetName string) string {
	return "release:" + strings.TrimSpace(tagName) + ":" + strings.TrimSpace(assetName)
}

func builderVersionsMatch(installedVersion, expectedVersion string) bool {
	installedVersion = normalizeVersion(installedVersion)
	expectedVersion = normalizeVersion(expectedVersion)
	return installedVersion != "" && installedVersion == expectedVersion
}

func preferredBuilderVersion(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || strings.EqualFold(value, "unknown") {
			continue
		}
		return value
	}
	return "unknown"
}

func loadBuilderInstallState() (builderInstallState, error) {
	var state builderInstallState
	data, err := os.ReadFile(builderInstallStatePath())
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return builderInstallState{}, err
	}
	return state, nil
}

func saveBuilderInstallState(state builderInstallState) error {
	if err := os.MkdirAll(filepath.Dir(builderInstallStatePath()), 0o755); err != nil {
		return err
	}
	payload, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	return os.WriteFile(builderInstallStatePath(), payload, 0o644)
}

func runBuilderInspect(builderPath, projectDir, cfgPath string) (builderInspectOutput, error) {
	cmd := exec.Command(
		builderPath,
		"offline",
		"inspect",
		"--path",
		projectDir,
		"--config",
		cfgPath,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return builderInspectOutput{}, fmt.Errorf("hubfly-builder inspect failed: %s", strings.TrimSpace(string(output)))
	}

	var inspect builderInspectOutput
	if err := json.Unmarshal(output, &inspect); err != nil {
		return builderInspectOutput{}, err
	}
	return inspect, nil
}

func writeGeneratedDockerfile(projectDir, content string) (string, error) {
	path := generatedDockerfilePath(projectDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func openDeployConfigEditor(path string) error {
	editor := strings.TrimSpace(os.Getenv("EDITOR"))
	if editor == "" {
		fmt.Printf("Edit %s, then press Enter to continue.", path)
		_, err := prompt("")
		return err
	}

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command(editor, path)
	} else {
		cmd = exec.Command("bash", "-lc", fmt.Sprintf("%s %q", editor, path))
	}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func buildDeploymentConfig(cfg deployConfigFile) cliDeploymentConfig {
	managedFields := make([]string, 0, len(cfg.Specified))
	for field, specified := range cfg.Specified {
		if specified {
			managedFields = append(managedFields, field)
		}
	}
	sort.Strings(managedFields)
	volumes := make([]cliDeploymentVolume, 0, len(cfg.Deploy.Volumes))
	for _, volume := range cfg.Deploy.Volumes {
		name := strings.TrimSpace(volume.Name)
		mountPath := strings.TrimSpace(volume.MountPath)
		if name == "" || mountPath == "" {
			continue
		}
		volumes = append(volumes, cliDeploymentVolume{
			DockerVolumeName: name,
			MountPoint:       mountPath,
		})
	}

	ports := make([]cliDeploymentPort, 0, len(cfg.Deploy.Ports))
	for _, port := range cfg.Deploy.Ports {
		if port.Container <= 0 {
			continue
		}
		ports = append(ports, cliDeploymentPort{
			ID:        strings.TrimSpace(port.ID),
			Container: port.Container,
			Protocol:  normalizePortProtocol(port.Protocol),
			Host:      port.Host,
			HostIP:    strings.TrimSpace(port.HostIP),
		})
	}

	envVars := make([]cliDeploymentEnvVar, 0)
	for _, entry := range cfg.Env {
		scope := normalizeEnvScope(entry.Scope)
		if scope != "runtime" && scope != "both" {
			continue
		}
		key := strings.TrimSpace(entry.Name)
		if key == "" {
			continue
		}
		envVars = append(envVars, cliDeploymentEnvVar{
			ID:       "env_" + sanitizeContainerName(key),
			Key:      key,
			Value:    entry.Value,
			IsSecret: entry.Secret,
			Scope:    scope,
		})
	}

	var process *cliDeploymentProcess
	if len(cfg.Deploy.Process.Command) > 0 ||
		len(cfg.Deploy.Process.Entrypoint) > 0 ||
		strings.TrimSpace(cfg.Deploy.Process.WorkingDir) != "" {
		process = &cliDeploymentProcess{
			Command:    cloneStrings(cfg.Deploy.Process.Command),
			Entrypoint: cloneStrings(cfg.Deploy.Process.Entrypoint),
			WorkingDir: strings.TrimSpace(cfg.Deploy.Process.WorkingDir),
		}
	}

	var healthcheck *cliDeploymentHealthcheck
	if cfg.Deploy.Healthcheck != nil && len(cfg.Deploy.Healthcheck.Test) > 0 {
		healthcheck = &cliDeploymentHealthcheck{
			Test:        cloneStrings(cfg.Deploy.Healthcheck.Test),
			Interval:    cfg.Deploy.Healthcheck.Interval,
			Timeout:     cfg.Deploy.Healthcheck.Timeout,
			StartPeriod: cfg.Deploy.Healthcheck.StartPeriod,
			Retries:     cfg.Deploy.Healthcheck.Retries,
		}
	}

	var restartPolicy *cliDeploymentRestartPolicy
	if cfg.Deploy.RestartPolicy != nil && strings.TrimSpace(cfg.Deploy.RestartPolicy.Name) != "" {
		restartPolicy = &cliDeploymentRestartPolicy{
			Name:              cfg.Deploy.RestartPolicy.Name,
			MaximumRetryCount: cfg.Deploy.RestartPolicy.MaximumRetryCount,
		}
	}

	return cliDeploymentConfig{
		ProjectName:         cfg.Project.Name,
		ContainerName:       cfg.Container.Name,
		NetworkPrimaryAlias: strings.TrimSpace(cfg.Deploy.NetworkPrimaryAlias),
		NetworkAliases:      cloneStrings(cfg.Deploy.NetworkAliases),
		Region:              cfg.Project.Region,
		ProjectID:           cfg.Project.ID,
		Tier:                cfg.Deploy.Tier,
		Resources: cliDeploymentResources{
			CPU:     cfg.Deploy.Resources.CPU,
			RAM:     cfg.Deploy.Resources.RAM,
			Storage: cfg.Deploy.Resources.Storage,
			MaxCPU:  cfg.Deploy.Resources.MaxCPU,
			MaxRAM:  cfg.Deploy.Resources.MaxRAM,
		},
		AttachedVolumes: volumes,
		Runtime: cliDeploymentRuntime{
			AutoSleep:     cfg.Deploy.Runtime.AutoSleep,
			AutoScale:     cfg.Deploy.Runtime.AutoScale,
			Is24x7:        cfg.Deploy.Runtime.Is24x7,
			AutoScaleMode: cfg.Deploy.Runtime.AutoScaleMode,
		},
		Process:              process,
		Healthcheck:          healthcheck,
		RestartPolicy:        restartPolicy,
		Labels:               cfg.Deploy.Labels,
		Networking:           cliDeploymentNetworking{Ports: ports},
		EnvironmentVariables: envVars,
		Source: cliDeploymentSource{
			Type:        "docker",
			DockerImage: "",
		},
		Remove:          cfg.Remove,
		ManagedFields:   managedFields,
		ManifestVersion: cfg.Version,
	}
}

func normalizePortProtocol(value string) string {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "HTTP":
		return "HTTP"
	case "UDP":
		return "UDP"
	default:
		return "TCP"
	}
}

func generateLocalImageTag(buildID string) string {
	tag := strings.TrimSpace(strings.ToLower(buildID))
	tag = strings.ReplaceAll(tag, "_", "-")
	tag = strings.ReplaceAll(tag, ":", "-")
	return "hubfly-local:" + tag
}

func buildLocalImage(ctx context.Context, projectDir, dockerfilePath, localTag string, cfg deployConfigFile) error {
	contextPath := filepath.Join(projectDir, cfg.Build.ContextDir)
	ignoreDockerfile := dockerfilePath
	if !filepath.IsAbs(ignoreDockerfile) {
		ignoreDockerfile = filepath.Join(projectDir, ignoreDockerfile)
	}
	removeIgnore, err := applyHubflyIgnore(contextPath, ignoreDockerfile)
	if err != nil {
		return err
	}
	defer removeIgnore()
	args := []string{"build", "-f", dockerfilePath, "-t", localTag}

	secretFiles, cleanup, err := writeBuildSecretFiles(cfg.Env)
	if err != nil {
		return err
	}
	defer cleanup()

	for _, entry := range cfg.Env {
		name := strings.TrimSpace(entry.Name)
		if name == "" {
			continue
		}
		scope := normalizeEnvScope(entry.Scope)
		if scope != "build" && scope != "both" {
			continue
		}
		if entry.Secret {
			if secretPath, ok := secretFiles[name]; ok {
				args = append(args, "--secret", fmt.Sprintf("id=%s,src=%s", name, secretPath))
			}
			continue
		}
		args = append(args, "--build-arg", fmt.Sprintf("%s=%s", name, entry.Value))
	}

	args = append(args, contextPath)

	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.WaitDelay = 10 * time.Second
	cmd.Dir = projectDir
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), "DOCKER_BUILDKIT=1")
	return cmd.Run()
}

func writeBuildSecretFiles(envVars []deployEnvVar) (map[string]string, func(), error) {
	files := make(map[string]string)
	tmpDir, err := os.MkdirTemp("", "hubfly-build-secrets-*")
	if err != nil {
		return nil, nil, err
	}

	cleanup := func() {
		_ = os.RemoveAll(tmpDir)
	}

	for idx, entry := range envVars {
		name := strings.TrimSpace(entry.Name)
		if name == "" || !entry.Secret {
			continue
		}
		scope := normalizeEnvScope(entry.Scope)
		if scope != "build" && scope != "both" {
			continue
		}
		path := filepath.Join(tmpDir, fmt.Sprintf("%03d_%s", idx, sanitizeContainerName(name)))
		if err := os.WriteFile(path, []byte(entry.Value), 0o600); err != nil {
			cleanup()
			return nil, nil, err
		}
		files[name] = path
	}

	return files, cleanup, nil
}

func estimateLocalImageSize(localTag string) int64 {
	output, err := commandOutput(exec.Command("docker", "image", "inspect", localTag, "--format", "{{.Size}}"))
	if err != nil {
		return 0
	}
	size, err := strconv.ParseInt(strings.TrimSpace(output), 10, 64)
	if err != nil || size <= 0 {
		return 0
	}
	return size
}

func waitForDeploySession(token, buildID string) (deploySessionStatusResponse, error) {
	return waitForDeploySessionContext(context.Background(), token, buildID)
}

// deployEventKey identifies an event so re-fetching the full list after a
// reconnect prints each line exactly once, in server order.
func deployEventKey(createdAt, phase, status, message string) string {
	return createdAt + "\x00" + phase + "\x00" + status + "\x00" + message
}

// printNewDeployEvents prints events not printed yet and returns how many it printed.
func printNewDeployEvents(token, buildID string, seen map[string]bool) int {
	events, err := fetchDeploySessionEvents(token, buildID)
	if err != nil {
		debugf("deploy events fetch failed: %v", err)
		return 0
	}
	printed := 0
	for _, event := range events.Events {
		key := deployEventKey(event.CreatedAt, event.Phase, event.Status, event.Message)
		if seen[key] {
			continue
		}
		seen[key] = true
		printed++
		stamp := event.CreatedAt
		if parsed, parseErr := time.Parse(time.RFC3339Nano, event.CreatedAt); parseErr == nil {
			stamp = parsed.Local().Format("15:04:05")
		}
		line := fmt.Sprintf("  %s  %-12s %s", stamp, event.Phase, strings.TrimSpace(event.Message))
		if code := strings.TrimSpace(event.ErrorCode); code != "" {
			line += " [" + code + "]"
		}
		fmt.Println(line)
	}
	return printed
}

// waitForDeploySessionContext follows a deployment until it finishes. It
// streams new deployment events, survives network drops (reconnecting with
// backoff without losing events), and stops cleanly when ctx is cancelled.
func waitForDeploySessionContext(ctx context.Context, token, buildID string) (deploySessionStatusResponse, error) {
	deadline := time.Now().Add(30 * time.Minute)
	const (
		pollInterval       = 3 * time.Second
		heartbeatInterval  = 30 * time.Second
		stalledDeployLimit = 10 * time.Minute
		maxOfflineDuration = 3 * time.Minute
	)
	lastStatus := ""
	lastServerUpdate := time.Time{}
	lastProgress := time.Now()
	lastHeartbeat := time.Now()
	seenEvents := map[string]bool{}
	failures := 0
	var offlineSince time.Time

	for {
		if ctx.Err() != nil {
			return deploySessionStatusResponse{}, interruptedDuringRollout(buildID)
		}
		status, err := fetchDeploySession(token, buildID)
		if err != nil {
			if ctx.Err() != nil {
				return deploySessionStatusResponse{}, interruptedDuringRollout(buildID)
			}
			if !isTransientDeployError(err) {
				return deploySessionStatusResponse{}, fmt.Errorf("check deployment %s: %w", buildID, err)
			}
			failures++
			if offlineSince.IsZero() {
				offlineSince = time.Now()
			}
			if time.Since(offlineSince) >= maxOfflineDuration {
				return deploySessionStatusResponse{}, &deployExitError{code: exitDeployUnknown, err: fmt.Errorf(
					"lost contact with Hubfly for %s (%v); deployment %s may still be running. Check it with `hubfly deploy status %s`",
					maxOfflineDuration, err, buildID, buildID,
				)}
			}
			delay := deployBackoff(failures, 20*time.Second)
			fmt.Printf("Connection to Hubfly lost (%v); reconnecting in %s...\n", err, delay.Round(time.Second))
			if !sleepContext(ctx, delay) {
				return deploySessionStatusResponse{}, interruptedDuringRollout(buildID)
			}
			continue
		}
		if failures > 0 {
			fmt.Printf("Reconnected to Hubfly after %s.\n", time.Since(offlineSince).Round(time.Second))
			failures = 0
			offlineSince = time.Time{}
		}

		if printNewDeployEvents(token, buildID, seenEvents) > 0 {
			lastProgress = time.Now()
			lastHeartbeat = time.Now()
		}
		if status.Build.Status != lastStatus {
			lastStatus = status.Build.Status
			fmt.Printf("Deploy status: %s\n", lastStatus)
			lastProgress = time.Now()
		}

		if parsed, parseErr := time.Parse(time.RFC3339Nano, status.Build.UpdatedAt); parseErr == nil {
			if parsed.After(lastServerUpdate) {
				lastServerUpdate = parsed
				lastProgress = time.Now()
			}
		}

		switch status.Build.Status {
		case "success", "failed", "cancelled":
			return status, nil
		}

		now := time.Now()
		if now.Sub(lastHeartbeat) >= heartbeatInterval {
			lastHeartbeat = now
			serverUpdate := "unknown"
			if !lastServerUpdate.IsZero() {
				serverUpdate = lastServerUpdate.Local().Format(time.RFC3339)
			}
			fmt.Printf(
				"Deploy still in progress: status=%s, no-progress=%s, last server update=%s\n",
				status.Build.Status,
				now.Sub(lastProgress).Round(time.Second),
				serverUpdate,
			)
		}

		if now.After(deadline) {
			return deploySessionStatusResponse{}, &deployExitError{code: exitDeployUnknown, err: fmt.Errorf(
				"timed out after 30m waiting for deployment %s (last status: %s); it may still finish. Check it with `hubfly deploy status %s`",
				buildID, status.Build.Status, buildID,
			)}
		}
		if now.Sub(lastProgress) >= stalledDeployLimit {
			return deploySessionStatusResponse{}, &deployExitError{code: exitDeployUnknown, err: fmt.Errorf(
				"deployment %s made no progress for %s (last status: %s). Inspect it with `hubfly deploy events %s` or stop it with `hubfly deploy cancel %s`",
				buildID, stalledDeployLimit, status.Build.Status, buildID, buildID,
			)}
		}
		if !sleepContext(ctx, pollInterval) {
			return deploySessionStatusResponse{}, interruptedDuringRollout(buildID)
		}
	}
}

func removeLocalImage(localTag string) error {
	cmd := exec.Command("docker", "image", "rm", "-f", localTag)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run()
}

func commandOutput(cmd *exec.Cmd) (string, error) {
	output, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(output)), err
}

func installBuilderFromURL(targetPath, downloadURL string) (string, error) {
	newBinary, err := downloadAndExtractNamedBinary(
		downloadURL,
		builderBinaryCandidates(),
	)
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(newBinary) }()

	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return "", err
	}

	if err := copyFile(targetPath, newBinary); err != nil {
		return "", err
	}
	if err := os.Chmod(targetPath, 0o755); err != nil && runtime.GOOS != "windows" {
		return "", err
	}

	version, err := commandOutput(exec.Command(targetPath, "version"))
	if err != nil {
		return "unknown", nil
	}
	return strings.TrimSpace(version), nil
}

func cachedBuilderOrError(targetPath string, cause error) (string, string, error) {
	return cachedBuilderOrVersionError(targetPath, "", cause)
}

func cachedBuilderOrVersionError(targetPath, requestedVersion string, cause error) (string, string, error) {
	version, cachedErr := probeBuilderVersion(targetPath)
	if cachedErr != nil {
		return "", "", cause
	}
	if requestedVersion != "" && !builderVersionsMatch(version, requestedVersion) {
		return "", "", cause
	}

	fmt.Fprintf(
		os.Stderr,
		"Warning: %v\nUsing cached hubfly-builder %s at %s\n",
		cause,
		version,
		targetPath,
	)
	return targetPath, version, nil
}

func localBuilderVersion(targetPath string) (string, error) {
	return probeBuilderVersion(targetPath)
}

func builderBinaryCandidates() []string {
	if runtime.GOOS == "windows" {
		return []string{"hubfly-builder.exe", "hubfly-builder"}
	}
	return []string{"hubfly-builder", "hubfly-builder.exe"}
}

func expectedBuilderAssetName(goos, goarch string) (string, error) {
	switch goos {
	case "linux", "darwin":
		return fmt.Sprintf("hubfly-builder_%s_%s.tar.gz", goos, goarch), nil
	case "windows":
		return fmt.Sprintf("hubfly-builder_%s_%s.zip", goos, goarch), nil
	default:
		return "", fmt.Errorf(
			"hubfly-builder releases do not publish binaries for %s/%s. Supported OS families are linux, darwin, and windows",
			goos,
			goarch,
		)
	}
}

func findBuilderAssetURL(rel githubRelease, assetName string) (string, error) {
	assetURL, err := findAssetURL(rel, assetName)
	if err == nil {
		return assetURL, nil
	}

	available := make([]string, 0, len(rel.Assets))
	for _, asset := range rel.Assets {
		name := strings.TrimSpace(asset.Name)
		if name == "" || strings.HasSuffix(name, ".sha256") {
			continue
		}
		available = append(available, name)
	}
	if len(available) == 0 {
		return "", fmt.Errorf(
			"latest hubfly-builder release %s has no downloadable binary assets",
			rel.TagName,
		)
	}

	return "", fmt.Errorf(
		"latest hubfly-builder release %s does not include %q for %s/%s. Available assets: %s",
		rel.TagName,
		assetName,
		runtime.GOOS,
		runtime.GOARCH,
		strings.Join(available, ", "),
	)
}

func copyFile(targetPath, sourcePath string) error {
	in, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	tmpPath := targetPath + ".tmp"
	out, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	_ = os.Remove(targetPath)
	if err := os.Rename(tmpPath, targetPath); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

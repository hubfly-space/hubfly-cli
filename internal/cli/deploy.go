package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
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
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}

	projectDir, cfgPath, err := resolveDeployWorkspace(opts.ConfigPath)
	if err != nil {
		return err
	}

	printDeployHeader(projectDir)

	cfg, created, err := loadOrInitDeployConfigAt(projectDir, cfgPath)
	if err != nil {
		return err
	}
	normalizeDeployConfig(&cfg, projectDir)
	applyDeployOverrides(&cfg, opts)

	editConfig := opts.Advanced
	if !opts.Advanced && !opts.AutoApprove && isInteractiveShell() {
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
	if err := ensureDeployProjectBinding(token, projectDir, &cfg, opts); err != nil {
		return err
	}
	cfg.Metadata.BuilderVersion = prepared.BuilderVersion
	if err := saveDeployConfig(cfgPath, cfg); err != nil {
		return err
	}

	current, err := loadBoundContainerSnapshot(token, &cfg)
	if err != nil {
		return err
	}

	printDeploySummary(projectDir, cfg, prepared)
	printDeployWarnings("Builder warnings", prepared.Warnings)
	diffPlan := buildDeployDiffPlan(projectDir, cfg, prepared, current)
	printDeployDiffPlan(diffPlan)
	if err := confirmDeployPlan(opts, diffPlan); err != nil {
		return err
	}

	deploymentConfig := buildDeploymentConfig(cfg)
	session, err := createDeploySessionWithMissingBoundFallback(token, cfgPath, &cfg, createDeploySessionRequest{
		BuilderVersion:   prepared.BuilderVersion,
		BoundContainerID: strings.TrimSpace(cfg.Container.ID),
		Config:           deploymentConfig,
	}, opts)
	if err != nil {
		return err
	}

	localTag := generateLocalImageTag(session.BuildID)
	fmt.Printf("Deploy build id: %s\n", session.BuildID)
	printDeployStep(
		"Local build",
		fmt.Sprintf("docker build using %s", displayDeployBuildSource(projectDir, prepared)),
	)
	if err := buildLocalImage(projectDir, prepared.DockerfilePath, localTag, cfg); err != nil {
		_ = reportDeployCallback(
			session.BuildID,
			"failed",
			session.Upload.Token,
			"Local build failed: "+err.Error(),
		)
		return err
	}
	defer func() {
		_ = removeLocalImage(localTag)
	}()

	printDeployStep(
		"Image upload",
		fmt.Sprintf("Streaming image to %s (%s)", session.Region.Name, session.Region.PrimaryIP),
	)
	if err := uploadLocalImage(localTag, session); err != nil {
		_ = reportDeployCallback(
			session.BuildID,
			"failed",
			session.Upload.Token,
			"Image upload failed: "+err.Error(),
		)
		return err
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

	status, err := waitForDeploySession(token, session.BuildID)
	if err != nil {
		return err
	}
	if status.Build.Status != "success" {
		if strings.TrimSpace(status.Build.Error) == "" {
			return fmt.Errorf("deployment failed")
		}
		return fmt.Errorf("deployment failed: %s", status.Build.Error)
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
	token, cfgPath string,
	cfg *deployConfigFile,
	req createDeploySessionRequest,
	opts deployOptions,
) (deploySessionResponse, error) {
	session, err := createDeploySession(token, req)
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
	return createDeploySession(token, req)
}

func isMissingBoundContainerError(err error) bool {
	apiErr, ok := err.(*apiError)
	if !ok || apiErr.Status != http.StatusNotFound {
		return false
	}

	return strings.Contains(apiErr.Message, "Bound container not found in this project.")
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

func ensureDeployProjectBinding(token, projectDir string, cfg *deployConfigFile, opts deployOptions) error {
	projects, err := fetchProjects(token)
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

	createdProject, err := createProjectForDeploy(token, projectName, selectedRegion.ID)
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

	release, _, err := fetchBuilderReleaseCached(request.RequestedVersion)
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
	}
}

func normalizePortProtocol(value string) string {
	switch strings.ToUpper(strings.TrimSpace(value)) {
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

func buildLocalImage(projectDir, dockerfilePath, localTag string, cfg deployConfigFile) error {
	contextPath := filepath.Join(projectDir, cfg.Build.ContextDir)
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

	cmd := exec.Command("docker", args...)
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

func uploadLocalImage(localTag string, session deploySessionResponse) error {
	printDeployStep(
		"Upload archive",
		"Compressing the local Docker image before transfer",
	)
	archivePath, archiveSize, cleanup, err := exportCompressedImageArchive(localTag)
	if err != nil {
		return err
	}
	defer cleanup()

	return uploadChunkedImageArchive(archivePath, archiveSize, session, localTag)
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
	deadline := time.Now().Add(20 * time.Minute)
	lastStatus := ""

	for {
		status, err := fetchDeploySession(token, buildID)
		if err != nil {
			return deploySessionStatusResponse{}, err
		}
		if status.Build.Status != lastStatus {
			lastStatus = status.Build.Status
			fmt.Printf("Deploy status: %s\n", lastStatus)
		}

		switch status.Build.Status {
		case "success", "failed":
			return status, nil
		}

		if time.Now().After(deadline) {
			return deploySessionStatusResponse{}, fmt.Errorf("timed out waiting for deployment")
		}
		time.Sleep(3 * time.Second)
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

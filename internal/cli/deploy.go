package cli

import (
	"bytes"
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
	UpdatedAt string `json:"updatedAt,omitempty"`
}

func deployFlow(forceAdvanced bool) error {
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}

	projectDir, err := os.Getwd()
	if err != nil {
		return err
	}

	printDeployHeader(projectDir)

	cfgPath := deployConfigPath(projectDir)
	cfg, created, err := loadOrInitDeployConfig(projectDir)
	if err != nil {
		return err
	}

	prepared, err := prepareDeployBuild(projectDir, cfgPath, &cfg)
	if err != nil {
		return err
	}
	if err := ensureDeployProjectBinding(token, projectDir, &cfg); err != nil {
		return err
	}
	cfg.Metadata.BuilderVersion = prepared.BuilderVersion
	if err := saveDeployConfig(cfgPath, cfg); err != nil {
		return err
	}

	printDeploySummary(projectDir, cfg, prepared)
	printDeployWarnings("Builder warnings", prepared.Warnings)

	editConfig := forceAdvanced
	if !forceAdvanced {
		editConfig, err = promptDeployReviewChoice(created)
		if err != nil {
			return err
		}
	}

	if editConfig {
		if err := openDeployConfigEditor(cfgPath); err != nil {
			return err
		}
		cfg, _, err = loadOrInitDeployConfig(projectDir)
		if err != nil {
			return err
		}
		prepared, err = prepareDeployBuild(projectDir, cfgPath, &cfg)
		if err != nil {
			return err
		}
		if err := ensureDeployProjectBinding(token, projectDir, &cfg); err != nil {
			return err
		}
		cfg.Metadata.BuilderVersion = prepared.BuilderVersion
		if err := saveDeployConfig(cfgPath, cfg); err != nil {
			return err
		}
		printDeploySummary(projectDir, cfg, prepared)
		printDeployWarnings("Builder warnings", prepared.Warnings)
	}

	deploymentConfig := buildDeploymentConfig(cfg)
	session, err := createDeploySession(token, createDeploySessionRequest{
		BuilderVersion:   prepared.BuilderVersion,
		BoundContainerID: strings.TrimSpace(cfg.Container.ID),
		Config:           deploymentConfig,
	})
	if err != nil {
		return err
	}

	localTag := generateLocalImageTag(session.BuildID)
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
	cfg.Metadata.LastImageTag = status.Build.ImageTag
	cfg.Metadata.LastDeployedAt = time.Now().UTC().Format(time.RFC3339)
	if err := saveDeployConfig(cfgPath, cfg); err != nil {
		return err
	}

	fmt.Printf("Deployment succeeded.\n")
	fmt.Printf("Project:   %s (%s)\n", status.Build.ProjectName, status.Build.ProjectID)
	fmt.Printf("Container: %s (%s)\n", cfg.Container.Name, cfg.Container.ID)
	fmt.Printf("Image:     %s\n", status.Build.ImageTag)
	fmt.Printf("Config:    %s\n", cfgPath)
	return nil
}

func prepareDeployBuild(projectDir, cfgPath string, cfg *deployConfigFile) (deployPreparedBuild, error) {
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

	builderPath, builderVersion, err := ensureLocalBuilderBinary()
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

func ensureDeployProjectBinding(token, projectDir string, cfg *deployConfigFile) error {
	projects, err := fetchProjects(token)
	if err != nil {
		return err
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
		return createProjectBinding(token, projectDir, cfg)
	}

	selection, err := selectProjectIndex(projects)
	if err != nil {
		return err
	}

	if selection == 1 {
		return createProjectBinding(token, projectDir, cfg)
	}

	selected := projects[selection-2]
	cfg.Project.ID = selected.ID
	cfg.Project.Name = selected.Name
	cfg.Project.Region = selected.Region.ID
	return nil
}

func createProjectBinding(token, projectDir string, cfg *deployConfigFile) error {
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

	projectName, err := promptStringWithDefault(
		"Project name",
		defaultProjectName(projectDir, cfg.Project.Name),
	)
	if err != nil {
		return err
	}

	selection, err := selectRegionIndex(availableRegions)
	if err != nil {
		return err
	}

	selectedRegion := availableRegions[selection-1]
	createdProject, err := createProjectForDeploy(token, projectName, selectedRegion.ID)
	if err != nil {
		return err
	}

	cfg.Project.ID = createdProject.ID
	cfg.Project.Name = createdProject.Name
	cfg.Project.Region = createdProject.Region.ID
	return nil
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

func ensureLocalBuilderBinary() (string, string, error) {
	targetPath := builderBinaryPath()
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return "", "", err
	}

	downloadURL := strings.TrimSpace(os.Getenv("HUBFLY_BUILDER_URL"))
	if downloadURL != "" {
		sourceKey := builderInstallSourceForURL(downloadURL)
		if path, version, ok := reuseInstalledBuilder(targetPath, sourceKey, ""); ok {
			return path, version, nil
		}
		version, err := installBuilderFromURL(targetPath, downloadURL)
		if err != nil {
			return cachedBuilderOrError(targetPath, err)
		}
		_ = saveBuilderInstallState(builderInstallState{
			Version:   version,
			Source:    sourceKey,
			UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		})
		return targetPath, version, nil
	}

	release, err := fetchLatestReleaseForRepo(builderRepoOwner, builderRepoName)
	if err != nil {
		return cachedBuilderOrError(
			targetPath,
			fmt.Errorf("failed to fetch latest hubfly-builder release: %w", err),
		)
	}

	assetName, err := expectedBuilderAssetName(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return cachedBuilderOrError(targetPath, err)
	}

	sourceKey := builderInstallSourceForRelease(release.TagName, assetName)
	if path, version, ok := reuseInstalledBuilder(targetPath, sourceKey, release.TagName); ok {
		return path, version, nil
	}

	assetURL, err := findBuilderAssetURL(release, assetName)
	if err != nil {
		return cachedBuilderOrError(targetPath, err)
	}

	version, err := installBuilderFromURL(targetPath, assetURL)
	if err != nil {
		return cachedBuilderOrError(
			targetPath,
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
	return targetPath, version, nil
}

func reuseInstalledBuilder(targetPath, sourceKey, expectedVersion string) (string, string, bool) {
	version, err := localBuilderVersion(targetPath)
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
	cmd := exec.Command("docker", "save", localTag)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	progress := newUploadProgress("Upload progress", estimateLocalImageSize(localTag))
	req, err := http.NewRequest(http.MethodPost, session.Upload.URL, progress.Wrap(stdout))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-tar")
	req.Header.Set("X-Hubfly-Build-Id", session.BuildID)
	req.Header.Set("X-Hubfly-Upload-Token", session.Upload.Token)
	req.Header.Set("X-Hubfly-Source-Image", localTag)

	client := &http.Client{}
	if err := cmd.Start(); err != nil {
		return err
	}
	progress.Start()
	defer progress.Finish()

	resp, requestErr := client.Do(req)
	waitErr := cmd.Wait()

	if requestErr != nil {
		if waitErr != nil {
			return fmt.Errorf("%w; docker save: %s", requestErr, strings.TrimSpace(stderr.String()))
		}
		return requestErr
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if waitErr != nil {
		return fmt.Errorf("docker save failed: %s", strings.TrimSpace(stderr.String()))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("upload failed with %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
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
	version, cachedErr := localBuilderVersion(targetPath)
	if cachedErr != nil {
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
	if _, err := os.Stat(targetPath); err != nil {
		return "", err
	}
	version, err := commandOutput(exec.Command(targetPath, "version"))
	if err != nil {
		return "unknown", nil
	}
	version = strings.TrimSpace(version)
	if version == "" {
		version = "unknown"
	}
	return version, nil
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

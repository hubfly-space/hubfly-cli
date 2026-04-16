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
	"strings"
	"time"
)

const defaultBuilderDownloadURL = "https://github.com/hubfly-space/hubfly-builder/releases/latest/download/hubfly-builder"

func deployFlow(forceAdvanced bool) error {
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}

	projectDir, err := os.Getwd()
	if err != nil {
		return err
	}

	cfgPath := deployConfigPath(projectDir)
	cfg, created, err := loadOrInitDeployConfig(projectDir)
	if err != nil {
		return err
	}

	builderPath, builderVersion, err := ensureLocalBuilderBinary()
	if err != nil {
		return err
	}
	cfg.Metadata.BuilderVersion = builderVersion

	inspect, err := runBuilderInspect(builderPath, projectDir, cfgPath)
	if err != nil {
		return err
	}
	applyInspectOutput(&cfg, inspect)
	if err := ensureDeployProjectBinding(token, projectDir, &cfg); err != nil {
		return err
	}
	if err := saveDeployConfig(cfgPath, cfg); err != nil {
		return err
	}

	editConfig := forceAdvanced
	if !forceAdvanced {
		editConfig, err = promptYesNo(
			"Configure more in hubfly.build.json before build",
			created,
		)
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
		cfg.Metadata.BuilderVersion = builderVersion
		if err := ensureDeployProjectBinding(token, projectDir, &cfg); err != nil {
			return err
		}
		if err := saveDeployConfig(cfgPath, cfg); err != nil {
			return err
		}

		inspect, err = runBuilderInspect(builderPath, projectDir, cfgPath)
		if err != nil {
			return err
		}
		applyInspectOutput(&cfg, inspect)
		if err := saveDeployConfig(cfgPath, cfg); err != nil {
			return err
		}
	}

	dockerfilePath, err := writeGeneratedDockerfile(projectDir, inspect.Dockerfile)
	if err != nil {
		return err
	}

	deploymentConfig := buildDeploymentConfig(cfg)
	session, err := createDeploySession(token, createDeploySessionRequest{
		BuilderVersion:   builderVersion,
		BoundContainerID: strings.TrimSpace(cfg.Container.ID),
		Config:           deploymentConfig,
	})
	if err != nil {
		return err
	}

	localTag := generateLocalImageTag(session.BuildID)
	fmt.Printf("Building image locally with hubfly-builder %s\n", builderVersion)
	if err := buildLocalImage(projectDir, dockerfilePath, localTag, cfg); err != nil {
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

	fmt.Printf(
		"Uploading image to %s for project %s\n",
		session.Region.Name,
		session.ProjectName,
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
	cfg.Metadata.BuilderVersion = builderVersion
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

	fmt.Println("Select a project:")
	fmt.Println("  1. Create new project")
	for idx, p := range projects {
		fmt.Printf("  %d. %s [%s]\n", idx+2, p.Name, p.Region.Name)
	}

	selection, err := promptMenuSelection("Project", len(projects)+1, 1)
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

	fmt.Println("Select a region:")
	for idx, entry := range availableRegions {
		fmt.Printf("  %d. %s [%s]\n", idx+1, entry.Name, entry.Location)
	}
	selection, err := promptMenuSelection("Region", len(availableRegions), 1)
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
	if runtime.GOOS != "linux" {
		return "", "", fmt.Errorf("hubfly deploy currently supports automatic builder download on linux only")
	}

	targetPath := builderBinaryPath()
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return "", "", err
	}

	downloadURL := strings.TrimSpace(os.Getenv("HUBFLY_BUILDER_URL"))
	if downloadURL == "" {
		downloadURL = defaultBuilderDownloadURL
	}

	tmpPath := targetPath + ".download"
	req, err := http.NewRequest(http.MethodGet, downloadURL, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", "hubfly-cli-deploy")

	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Do(req)
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			file, createErr := os.OpenFile(tmpPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
			if createErr != nil {
				return "", "", createErr
			}
			if _, copyErr := io.Copy(file, resp.Body); copyErr != nil {
				_ = file.Close()
				return "", "", copyErr
			}
			if closeErr := file.Close(); closeErr != nil {
				return "", "", closeErr
			}
			if chmodErr := os.Chmod(tmpPath, 0o755); chmodErr != nil {
				return "", "", chmodErr
			}
			if renameErr := os.Rename(tmpPath, targetPath); renameErr != nil {
				return "", "", renameErr
			}
		} else {
			err = fmt.Errorf("builder download failed with status %d", resp.StatusCode)
		}
	}

	if err != nil {
		if _, statErr := os.Stat(targetPath); statErr != nil {
			return "", "", err
		}
	}

	version, versionErr := commandOutput(exec.Command(targetPath, "version"))
	if versionErr != nil {
		return targetPath, "unknown", nil
	}
	return targetPath, strings.TrimSpace(version), nil
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

	cmd := exec.Command("bash", "-lc", fmt.Sprintf("%s %q", editor, path))
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
		Process:        process,
		Healthcheck:    healthcheck,
		RestartPolicy:  restartPolicy,
		Labels:         cfg.Deploy.Labels,
		Networking:     cliDeploymentNetworking{Ports: ports},
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

	req, err := http.NewRequest(http.MethodPost, session.Upload.URL, stdout)
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

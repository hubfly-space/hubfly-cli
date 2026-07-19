package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func stackFlow(args []string) error {
	if len(args) == 0 {
		return errors.New(stackUsage())
	}
	switch args[0] {
	case "plan":
		return stackPlanFlow(args[1:])
	case "up":
		return stackUpFlow(args[1:])
	case "status":
		return stackStatusFlow(args[1:])
	case "logs":
		return stackLogsFlow(args[1:])
	case "exec":
		return stackExecFlow(args[1:])
	case "ssh":
		return stackSSHFlow(args[1:])
	case "down":
		return stackDownFlow(args[1:])
	default:
		return fmt.Errorf("unknown stack subcommand: %s\n%s", args[0], stackUsage())
	}
}

func stackUsage() string {
	return strings.TrimSpace(`
usage: hubfly stack <plan|up|status|logs|exec|ssh|down> [options]

examples:
  hubfly stack plan --file docker-compose.yml
  hubfly stack up --project new --region eu-1 --yes
  hubfly stack logs api --follow
  hubfly stack exec api -- printenv
  hubfly stack down --volumes --yes
`)
}

func stackPlanFlow(args []string) error {
	fs := flag.NewFlagSet("stack plan", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	filePath := fs.String("file", "", "compose file path")
	useJSON := fs.Bool("json", false, "print JSON-ish summary")
	if err := fs.Parse(args); err != nil {
		return err
	}

	spec, err := loadStackSpec(*filePath)
	if err != nil {
		return err
	}

	if *useJSON {
		fmt.Printf("stack=%s file=%s services=%d volumes=%d\n", spec.Name, spec.FilePath, len(spec.Services), len(spec.Volumes))
		for _, service := range spec.Services {
			buildMode := "image"
			if service.Build != nil {
				buildMode = "build"
			}
			fmt.Printf("service=%s container=%s mode=%s depends=%s\n", service.Name, service.ContainerName, buildMode, strings.Join(service.DependsOn, ","))
		}
		return nil
	}

	fmt.Println("Hubfly Stack Plan")
	fmt.Printf("%s  %s\n\n", spec.Name, spec.ProjectDir)
	fmt.Printf("Compose file: %s\n", spec.FilePath)
	fmt.Printf("Services: %d\n", len(spec.Services))
	fmt.Printf("Volumes: %d\n", len(spec.Volumes))
	fmt.Println()
	for _, service := range spec.Services {
		mode := displayDeployValue(service.Image, "local build")
		if service.Build != nil {
			mode = "local build"
		}
		fmt.Printf("- %s -> %s\n", service.Name, service.ContainerName)
		fmt.Printf("  source: %s\n", mode)
		fmt.Printf("  resources: %.2f vCPU / %d MB RAM / %d GB disk\n", service.Resources.CPU, service.Resources.RAM, service.Resources.Storage)
		if len(service.Ports) > 0 {
			fmt.Printf("  ports: %s\n", formatDeployPorts(service.Ports))
		}
		if len(service.Mounts) > 0 {
			fmt.Printf("  volumes: %d mount(s)\n", len(service.Mounts))
		}
	}
	printDeployWarnings("Stack warnings", spec.Warnings)
	return nil
}

func stackUpFlow(args []string) error {
	fs := flag.NewFlagSet("stack up", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	filePath := fs.String("file", "", "compose file path")
	project := fs.String("project", "", "target project id, name, or 'new'")
	region := fs.String("region", "", "target region id or name")
	autoApprove := fs.Bool("yes", false, "skip interactive confirmation prompts")
	removeOrphans := fs.Bool("remove-orphans", false, "remove managed services no longer present in the compose file")
	noBuild := fs.Bool("no-build", false, "skip local rebuilds for build-backed services")
	if err := fs.Parse(args); err != nil {
		return err
	}

	spec, err := loadStackSpec(*filePath)
	if err != nil {
		return err
	}
	printDeployWarnings("Stack warnings", spec.Warnings)

	token, err := ensureAuth(true)
	if err != nil {
		return err
	}

	cfg := defaultDeployConfig(spec.ProjectDir)
	cfg.Project.Name = spec.Name
	if err := ensureDeployProjectBinding(token, spec.ProjectDir, &cfg, deployOptions{
		Project:     strings.TrimSpace(*project),
		Region:      strings.TrimSpace(*region),
		AutoApprove: *autoApprove,
	}); err != nil {
		return err
	}

	projectDetails, err := fetchProject(token, cfg.Project.ID)
	if err != nil {
		return err
	}

	state, _, err := loadStackStateIfExists(spec.ProjectDir)
	if err != nil {
		return err
	}
	state.ProjectID = cfg.Project.ID
	state.ProjectName = cfg.Project.Name
	state.RegionID = cfg.Project.Region
	state.StackName = spec.Name
	state.ComposeFile = filepath.Base(spec.FilePath)
	state.ComposeHash = stackConfigHash(spec)

	volumeBindings, err := ensureStackVolumes(token, cfg.Project.ID, spec, &state, projectDetails)
	if err != nil {
		return err
	}

	ordered, err := stackDeployOrder(spec.Services)
	if err != nil {
		return err
	}
	currentContainers := make(map[string]container, len(projectDetails.Containers))
	for _, item := range projectDetails.Containers {
		currentContainers[item.ID] = item
	}

	for _, service := range ordered {
		fmt.Printf("\nService %s\n", service.Name)
		binding := state.Services[service.Name]
		exists := binding.ContainerID != "" && currentContainers[binding.ContainerID].ID != ""
		if binding.ConfigHash == service.ConfigHash && exists {
			fmt.Println("No config change detected; reusing current container.")
			continue
		}

		if service.Build != nil {
			if *noBuild {
				if exists {
					fmt.Println("Build skipped with --no-build; keeping current container.")
					continue
				}
				return fmt.Errorf("service %s requires a local build; rerun without --no-build", service.Name)
			}
			serviceState, deployErr := deployBuiltStackService(token, cfg, service, binding, volumeBindings)
			if deployErr != nil {
				return deployErr
			}
			serviceState.ConfigHash = service.ConfigHash
			state.Services[service.Name] = serviceState
			continue
		}

		if exists {
			fmt.Printf("Recreating image-based service %s\n", service.Name)
			if err := removeProjectContainer(token, cfg.Project.ID, binding.ContainerID); err != nil {
				return err
			}
		}
		serviceState, createErr := createImageStackService(token, cfg.Project.ID, service, volumeBindings)
		if createErr != nil {
			return createErr
		}
		serviceState.ConfigHash = service.ConfigHash
		state.Services[service.Name] = serviceState
	}

	if *removeOrphans {
		for name, binding := range state.Services {
			if stackHasService(spec.Services, name) {
				continue
			}
			if strings.TrimSpace(binding.ContainerID) == "" {
				delete(state.Services, name)
				continue
			}
			fmt.Printf("Removing orphaned service %s\n", name)
			if err := removeProjectContainer(token, cfg.Project.ID, binding.ContainerID); err != nil {
				return err
			}
			delete(state.Services, name)
		}
	}

	if err := saveStackState(spec.ProjectDir, state); err != nil {
		return err
	}
	fmt.Printf("\nStack %s is deployed in project %s (%s)\n", spec.Name, state.ProjectName, state.ProjectID)
	return nil
}

func stackStatusFlow(args []string) error {
	fs := flag.NewFlagSet("stack status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	filePath := fs.String("file", "", "compose file path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	projectDir, _, err := resolveStackWorkspace(*filePath)
	if err != nil {
		return err
	}
	state, err := loadStackState(projectDir)
	if err != nil {
		return err
	}
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}
	projectDetails, err := fetchProject(token, state.ProjectID)
	if err != nil {
		return err
	}
	containers := make(map[string]container, len(projectDetails.Containers))
	for _, item := range projectDetails.Containers {
		containers[item.ID] = item
	}

	fmt.Printf("Stack %s\n", state.StackName)
	fmt.Printf("Project %s (%s)\n\n", state.ProjectName, state.ProjectID)
	names := make([]string, 0, len(state.Services))
	for name := range state.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		service := state.Services[name]
		status := "missing"
		if item, ok := containers[service.ContainerID]; ok {
			status = item.Status
		}
		fmt.Printf("- %s  %s  %s\n", name, service.ContainerName, status)
	}
	return nil
}

func stackLogsFlow(args []string) error {
	fs := flag.NewFlagSet("stack logs", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	filePath := fs.String("file", "", "compose file path")
	follow := fs.Bool("follow", false, "follow logs")
	fs.BoolVar(follow, "f", false, "follow logs")
	if err := fs.Parse(args); err != nil {
		return err
	}
	projectDir, _, err := resolveStackWorkspace(*filePath)
	if err != nil {
		return err
	}
	state, err := loadStackState(projectDir)
	if err != nil {
		return err
	}
	services, err := selectStackServices(state, fs.Args())
	if err != nil {
		return err
	}
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}

	lastStdout := map[string]string{}
	lastStderr := map[string]string{}
	for {
		for _, name := range services {
			serviceState := state.Services[name]
			logs, fetchErr := fetchContainerLogs(token, state.ProjectID, serviceState.ContainerID)
			if fetchErr != nil {
				return fetchErr
			}
			if len(logs.Stdout) > len(lastStdout[name]) {
				printStackLogChunk(name, logs.Stdout[len(lastStdout[name]):], false)
				lastStdout[name] = logs.Stdout
			}
			if len(logs.Stderr) > len(lastStderr[name]) {
				printStackLogChunk(name, logs.Stderr[len(lastStderr[name]):], true)
				lastStderr[name] = logs.Stderr
			}
		}
		if !*follow {
			return nil
		}
		time.Sleep(3 * time.Second)
	}
}

func stackExecFlow(args []string) error {
	if len(args) < 3 {
		return errors.New("usage: hubfly stack exec <service> -- <cmd> [args...]")
	}
	serviceName := args[0]
	dashIndex := -1
	for idx, arg := range args {
		if arg == "--" {
			dashIndex = idx
			break
		}
	}
	if dashIndex == -1 || dashIndex == len(args)-1 {
		return errors.New("usage: hubfly stack exec <service> -- <cmd> [args...]")
	}
	projectDir, _, err := resolveStackWorkspace("")
	if err != nil {
		return err
	}
	state, err := loadStackState(projectDir)
	if err != nil {
		return err
	}
	serviceState, ok := state.Services[serviceName]
	if !ok {
		return fmt.Errorf("service %s not found in stack state", serviceName)
	}
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}
	return execContainerCommand(token, state.ProjectID, serviceState.ContainerID, serviceName, args[dashIndex+1:], 55*time.Second)
}

func stackSSHFlow(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: hubfly stack ssh <service>")
	}
	projectDir, _, err := resolveStackWorkspace("")
	if err != nil {
		return err
	}
	state, err := loadStackState(projectDir)
	if err != nil {
		return err
	}
	serviceState, ok := state.Services[args[0]]
	if !ok {
		return fmt.Errorf("service %s not found in stack state", args[0])
	}
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}
	return sshContainerTerminal(token, state.ProjectID, serviceState.ContainerID, args[0])
}

func stackDownFlow(args []string) error {
	fs := flag.NewFlagSet("stack down", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	filePath := fs.String("file", "", "compose file path")
	removeVolumes := fs.Bool("volumes", false, "remove managed volumes")
	autoApprove := fs.Bool("yes", false, "skip confirmation")
	if err := fs.Parse(args); err != nil {
		return err
	}
	projectDir, _, err := resolveStackWorkspace(*filePath)
	if err != nil {
		return err
	}
	state, err := loadStackState(projectDir)
	if err != nil {
		return err
	}
	if !*autoApprove && isInteractiveShell() {
		ok, promptErr := promptYesNo(fmt.Sprintf("Delete stack %s", state.StackName), false)
		if promptErr != nil {
			return promptErr
		}
		if !ok {
			return errors.New("stack removal cancelled")
		}
	}
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}
	serviceNames := make([]string, 0, len(state.Services))
	for name := range state.Services {
		serviceNames = append(serviceNames, name)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(serviceNames)))
	for _, name := range serviceNames {
		service := state.Services[name]
		if strings.TrimSpace(service.ContainerID) == "" {
			continue
		}
		fmt.Printf("Removing service %s\n", name)
		if err := removeProjectContainer(token, state.ProjectID, service.ContainerID); err != nil {
			return err
		}
	}
	if *removeVolumes {
		volumeNames := make([]string, 0, len(state.Volumes))
		for name := range state.Volumes {
			volumeNames = append(volumeNames, name)
		}
		sort.Strings(volumeNames)
		for _, name := range volumeNames {
			volume := state.Volumes[name]
			if strings.TrimSpace(volume.VolumeID) == "" {
				continue
			}
			fmt.Printf("Removing volume %s\n", name)
			if err := removeProjectVolume(token, state.ProjectID, volume.VolumeID); err != nil {
				return err
			}
		}
	}
	return deleteStackState(projectDir)
}

func ensureStackVolumes(
	token, projectID string,
	spec stackSpec,
	state *stackState,
	projectDetails projectDetails,
) (map[string]string, error) {
	bindings := map[string]string{}
	projectVolumesByID := map[string]volume{}
	projectVolumesByName := map[string]volume{}
	for _, item := range projectDetails.Volumes {
		projectVolumesByID[item.ID] = item
		projectVolumesByName[item.Name] = item
	}
	for name, volumeSpec := range spec.Volumes {
		if existing, ok := state.Volumes[name]; ok {
			if _, ok := projectVolumesByID[existing.VolumeID]; ok {
				bindings[name] = existing.VolumeID
				continue
			}
		}
		if existing, ok := projectVolumesByName[volumeSpec.ManagedName]; ok {
			bindings[name] = existing.ID
			state.Volumes[name] = stackVolumeState{
				VolumeID: existing.ID,
				Name:     existing.Name,
				SizeGb:   volumeSpec.SizeGb,
			}
			continue
		}
		fmt.Printf("Creating volume %s\n", volumeSpec.ManagedName)
		created, err := createProjectVolume(token, projectID, map[string]any{
			"projectId":         projectID,
			"name":              volumeSpec.ManagedName,
			"sizeGb":            volumeSpec.SizeGb,
			"performanceMode":   volumeSpec.PerformanceMode,
			"readOnly":          volumeSpec.ReadOnly,
			"backupEnabled":     false,
			"encryptionEnabled": false,
			"labels": map[string]string{
				"com.hubfly.stack.managed": "true",
				"com.hubfly.stack.name":    spec.Name,
				"com.hubfly.stack.volume":  name,
			},
		})
		if err != nil {
			return nil, err
		}
		volumeID := stringValue(created["id"])
		if volumeID == "" {
			return nil, fmt.Errorf("volume %s was created without an ID in the API response", volumeSpec.ManagedName)
		}
		bindings[name] = volumeID
		state.Volumes[name] = stackVolumeState{
			VolumeID: volumeID,
			Name:     volumeSpec.ManagedName,
			SizeGb:   volumeSpec.SizeGb,
		}
	}
	return bindings, nil
}

func deployBuiltStackService(
	token string,
	projectCfg deployConfigFile,
	service stackServiceSpec,
	binding stackServiceState,
	volumeBindings map[string]string,
) (stackServiceState, error) {
	deploymentConfig := buildStackDeploymentConfig(projectCfg, service, volumeBindings)
	session, err := createDeploySession(token, createDeploySessionRequest{
		BoundContainerID: strings.TrimSpace(binding.ContainerID),
		Config:           deploymentConfig,
	})
	if err != nil {
		return stackServiceState{}, err
	}

	localTag := generateLocalImageTag(session.BuildID)
	fmt.Printf("Deploy build id: %s\n", session.BuildID)
	printDeployStep("Local build", fmt.Sprintf("docker build for service %s", service.Name))
	if err := buildStackServiceImage(service, localTag); err != nil {
		_ = reportDeployFailure(token, session.BuildID, session.Upload.Token, "Stack local build failed: "+err.Error())
		return stackServiceState{}, err
	}
	defer func() { _ = removeLocalImage(localTag) }()

	printDeployStep("Image upload", fmt.Sprintf("Streaming image to %s (%s)", session.Region.Name, session.Region.PrimaryIP))
	if err := uploadLocalImage(localTag, session); err != nil {
		_ = reportDeployFailure(token, session.BuildID, session.Upload.Token, "Stack image upload failed: "+err.Error())
		return stackServiceState{}, err
	}

	status, err := waitForDeploySession(token, session.BuildID)
	if err != nil {
		return stackServiceState{}, err
	}
	if status.Build.Status != "success" {
		if status.Build.Error == "" {
			return stackServiceState{}, fmt.Errorf("service %s deployment failed", service.Name)
		}
		return stackServiceState{}, fmt.Errorf("service %s deployment failed: %s", service.Name, status.Build.Error)
	}
	containerID := strings.TrimSpace(status.Build.BoundContainerID)
	if containerID == "" {
		containerID = binding.ContainerID
	}
	return stackServiceState{
		ContainerID:   containerID,
		ContainerName: service.ContainerName,
		LastImage:     displayDeployValue(status.Build.ImageDisplay, session.Upload.CanonicalRef),
		LastBuildID:   session.BuildID,
	}, nil
}

func createImageStackService(
	token, projectID string,
	service stackServiceSpec,
	volumeBindings map[string]string,
) (stackServiceState, error) {
	payload := map[string]any{
		"projectId": projectID,
		"name":      service.ContainerName,
		"tier":      service.Tier,
		"source": map[string]any{
			"type":        "docker",
			"dockerImage": service.Image,
		},
		"resources": map[string]any{
			"cpu":     service.Resources.CPU,
			"ram":     service.Resources.RAM,
			"storage": service.Resources.Storage,
		},
		"runtime": map[string]any{
			"autoSleep":     service.Runtime.AutoSleep,
			"autoScale":     service.Runtime.AutoScale,
			"is24x7":        service.Runtime.Is24x7,
			"autoScaleMode": service.Runtime.AutoScaleMode,
		},
		"process": map[string]any{
			"command":    service.Command,
			"entrypoint": service.Entrypoint,
			"workingDir": service.WorkingDir,
		},
		"labels":               service.Labels,
		"networkAliases":       uniqueStrings(append([]string{service.Name}, service.NetworkAlias...)),
		"environmentVariables": stackEnvironmentPayload(service.Environment),
		"ports":                stackCreatePortsPayload(service.Ports),
		"volumeMounts":         stackCreateVolumePayload(service.Mounts, volumeBindings),
	}
	if service.Healthcheck != nil {
		payload["healthcheck"] = service.Healthcheck
	}
	if service.RestartPolicy != nil {
		payload["restartPolicy"] = service.RestartPolicy
	}
	created, err := createProjectContainer(token, projectID, payload)
	if err != nil {
		return stackServiceState{}, err
	}
	containerID := stringValue(created["id"])
	if containerID == "" {
		return stackServiceState{}, fmt.Errorf("service %s was created without an ID in the API response", service.Name)
	}
	return stackServiceState{
		ContainerID:   containerID,
		ContainerName: service.ContainerName,
		LastImage:     service.Image,
	}, nil
}

func buildStackDeploymentConfig(
	projectCfg deployConfigFile,
	service stackServiceSpec,
	volumeBindings map[string]string,
) cliDeploymentConfig {
	volumes := make([]cliDeploymentVolume, 0, len(service.Mounts))
	for _, mount := range service.Mounts {
		volumeID := volumeBindings[mount.VolumeName]
		if volumeID == "" {
			continue
		}
		volumes = append(volumes, cliDeploymentVolume{
			DockerVolumeName: volumeID,
			MountPoint:       mount.MountPath,
		})
	}
	envVars := make([]cliDeploymentEnvVar, 0, len(service.Environment))
	for _, env := range service.Environment {
		envVars = append(envVars, cliDeploymentEnvVar{
			ID:       "env_" + sanitizeContainerName(env.Name),
			Key:      env.Name,
			Value:    env.Value,
			IsSecret: env.Secret,
			Scope:    normalizeEnvScope(env.Scope),
		})
	}
	return cliDeploymentConfig{
		ProjectName:          projectCfg.Project.Name,
		ContainerName:        service.ContainerName,
		NetworkAliases:       uniqueStrings(append([]string{service.Name}, service.NetworkAlias...)),
		Region:               projectCfg.Project.Region,
		ProjectID:            projectCfg.Project.ID,
		Tier:                 service.Tier,
		Resources:            cliDeploymentResources(service.Resources),
		AttachedVolumes:      volumes,
		Runtime:              cliDeploymentRuntime(service.Runtime),
		Process:              &cliDeploymentProcess{Command: service.Command, Entrypoint: service.Entrypoint, WorkingDir: service.WorkingDir},
		Healthcheck:          (*cliDeploymentHealthcheck)(service.Healthcheck),
		RestartPolicy:        (*cliDeploymentRestartPolicy)(service.RestartPolicy),
		Labels:               service.Labels,
		Networking:           cliDeploymentNetworking{Ports: stackCLIPorts(service.Ports)},
		EnvironmentVariables: envVars,
		Source:               cliDeploymentSource{Type: "docker"},
	}
}

func buildStackServiceImage(service stackServiceSpec, localTag string) error {
	if service.Build == nil {
		return fmt.Errorf("service %s does not define a local build", service.Name)
	}
	args := []string{"build", "-f", service.Build.DockerfilePath, "-t", localTag}
	if strings.TrimSpace(service.Build.Target) != "" {
		args = append(args, "--target", strings.TrimSpace(service.Build.Target))
	}
	keys := make([]string, 0, len(service.Build.Args))
	for key := range service.Build.Args {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		args = append(args, "--build-arg", fmt.Sprintf("%s=%s", key, service.Build.Args[key]))
	}
	args = append(args, service.Build.ContextDir)

	cmd := exec.Command("docker", args...)
	cmd.Dir = service.Build.ContextDir
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), "DOCKER_BUILDKIT=1")
	return cmd.Run()
}

func stackDeployOrder(services []stackServiceSpec) ([]stackServiceSpec, error) {
	serviceMap := make(map[string]stackServiceSpec, len(services))
	for _, service := range services {
		serviceMap[service.Name] = service
	}
	visited := map[string]bool{}
	visiting := map[string]bool{}
	ordered := make([]stackServiceSpec, 0, len(services))
	var visit func(name string) error
	visit = func(name string) error {
		if visited[name] {
			return nil
		}
		if visiting[name] {
			return fmt.Errorf("compose dependency cycle detected at %s", name)
		}
		service, ok := serviceMap[name]
		if !ok {
			return fmt.Errorf("service %s depends on unknown service %s", name, name)
		}
		visiting[name] = true
		for _, dep := range service.DependsOn {
			if _, ok := serviceMap[dep]; !ok {
				return fmt.Errorf("service %s depends on unknown service %s", service.Name, dep)
			}
			if err := visit(dep); err != nil {
				return err
			}
		}
		visiting[name] = false
		visited[name] = true
		ordered = append(ordered, service)
		return nil
	}
	for _, service := range services {
		if err := visit(service.Name); err != nil {
			return nil, err
		}
	}
	return ordered, nil
}

func selectStackServices(state stackState, requested []string) ([]string, error) {
	if len(requested) == 0 {
		out := make([]string, 0, len(state.Services))
		for name := range state.Services {
			out = append(out, name)
		}
		sort.Strings(out)
		return out, nil
	}
	out := make([]string, 0, len(requested))
	for _, name := range requested {
		if _, ok := state.Services[name]; !ok {
			return nil, fmt.Errorf("service %s not found in stack state", name)
		}
		out = append(out, name)
	}
	return out, nil
}

func stackHasService(services []stackServiceSpec, name string) bool {
	for _, service := range services {
		if service.Name == name {
			return true
		}
	}
	return false
}

func stackEnvironmentPayload(env []deployEnvVar) []map[string]any {
	out := make([]map[string]any, 0, len(env))
	for _, item := range env {
		out = append(out, map[string]any{
			"key":      item.Name,
			"value":    item.Value,
			"isSecret": item.Secret,
		})
	}
	return out
}

func stackCreatePortsPayload(ports []deployPort) []map[string]any {
	out := make([]map[string]any, 0, len(ports))
	for _, item := range ports {
		protocol := strings.ToLower(strings.TrimSpace(item.Protocol))
		if protocol == "" {
			protocol = "tcp"
		}
		out = append(out, map[string]any{
			"protocol":   protocol,
			"container":  item.Container,
			"publicPort": item.Host,
		})
	}
	return out
}

func stackCreateVolumePayload(mounts []stackMount, bindings map[string]string) []map[string]any {
	out := make([]map[string]any, 0, len(mounts))
	for _, mount := range mounts {
		volumeID := bindings[mount.VolumeName]
		if volumeID == "" {
			continue
		}
		out = append(out, map[string]any{
			"volumeId":   volumeID,
			"mountPoint": mount.MountPath,
			"readOnly":   mount.ReadOnly,
		})
	}
	return out
}

func stackCLIPorts(ports []deployPort) []cliDeploymentPort {
	out := make([]cliDeploymentPort, 0, len(ports))
	for _, item := range ports {
		out = append(out, cliDeploymentPort{
			Container: item.Container,
			Protocol:  normalizePortProtocol(item.Protocol),
			Host:      item.Host,
			HostIP:    item.HostIP,
		})
	}
	return out
}

func printStackLogChunk(service, chunk string, stderr bool) {
	if strings.TrimSpace(chunk) == "" {
		return
	}
	prefix := "[" + service + "] "
	lines := strings.Split(chunk, "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		if stderr {
			fmt.Fprintln(os.Stderr, prefix+line)
			continue
		}
		fmt.Println(prefix + line)
	}
}

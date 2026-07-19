package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type stackSpec struct {
	Name       string
	FilePath   string
	ProjectDir string
	Warnings   []string
	Services   []stackServiceSpec
	Volumes    map[string]stackVolumeSpec
}

type stackServiceSpec struct {
	Name          string
	ContainerName string
	Image         string
	Build         *stackBuildSpec
	Command       []string
	Entrypoint    []string
	WorkingDir    string
	Environment   []deployEnvVar
	Ports         []deployPort
	DependsOn     []string
	Healthcheck   *deployHealthcheck
	RestartPolicy *deployRestartPolicy
	Labels        map[string]string
	NetworkAlias  []string
	Mounts        []stackMount
	Tier          string
	Resources     deployResources
	Runtime       deployRuntime
	ConfigHash    string
}

type stackBuildSpec struct {
	ContextDir     string
	DockerfilePath string
	Args           map[string]string
	Target         string
}

type stackVolumeSpec struct {
	Name            string
	ManagedName     string
	SizeGb          int
	PerformanceMode string
	ReadOnly        bool
}

type stackMount struct {
	VolumeName string
	MountPath  string
	ReadOnly   bool
}

var stackVariablePattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(:-([^}]*))?\}`)

func resolveStackWorkspace(fileInput string) (string, string, error) {
	if strings.TrimSpace(fileInput) != "" {
		resolved := strings.TrimSpace(fileInput)
		if !filepath.IsAbs(resolved) {
			cwd, err := os.Getwd()
			if err != nil {
				return "", "", err
			}
			resolved = filepath.Join(cwd, resolved)
		}
		resolved, err := filepath.Abs(resolved)
		if err != nil {
			return "", "", err
		}
		return filepath.Dir(resolved), resolved, nil
	}

	cwd, err := os.Getwd()
	if err != nil {
		return "", "", err
	}
	candidates := []string{
		"docker-compose.yml",
		"docker-compose.yaml",
		"compose.yml",
		"compose.yaml",
	}
	for _, name := range candidates {
		path := filepath.Join(cwd, name)
		if _, statErr := os.Stat(path); statErr == nil {
			return cwd, path, nil
		}
	}
	return cwd, filepath.Join(cwd, "docker-compose.yml"), nil
}

func loadStackSpec(fileInput string) (stackSpec, error) {
	projectDir, filePath, err := resolveStackWorkspace(fileInput)
	if err != nil {
		return stackSpec{}, err
	}
	content, err := os.ReadFile(filePath)
	if err != nil {
		return stackSpec{}, err
	}

	envValues := map[string]string{}
	for _, entry := range os.Environ() {
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) == 2 {
			envValues[parts[0]] = parts[1]
		}
	}
	defaultEnvPath := filepath.Join(projectDir, ".env")
	if fileEnv, err := loadEnvFile(defaultEnvPath); err == nil {
		for key, value := range fileEnv {
			if _, ok := envValues[key]; !ok {
				envValues[key] = value
			}
		}
	}

	expanded := expandComposeVariables(string(content), envValues)
	var raw map[string]any
	if err := yaml.Unmarshal([]byte(expanded), &raw); err != nil {
		return stackSpec{}, err
	}

	name := sanitizeContainerName(stringValue(raw["name"]))
	if name == "" {
		name = sanitizeContainerName(filepath.Base(projectDir))
	}
	if name == "" {
		name = "stack"
	}

	spec := stackSpec{
		Name:       name,
		FilePath:   filePath,
		ProjectDir: projectDir,
		Volumes:    map[string]stackVolumeSpec{},
	}

	for volumeName, rawVolume := range mapValue(raw["volumes"]) {
		serviceVolume := stackVolumeSpec{
			Name:            volumeName,
			ManagedName:     sanitizeContainerName(name + "-" + volumeName),
			SizeGb:          10,
			PerformanceMode: "balanced",
		}
		if extension := mapValue(mapValue(rawVolume)["x-hubfly"]); len(extension) > 0 {
			if size := intValue(extension["sizeGb"]); size > 0 {
				serviceVolume.SizeGb = size
			}
			if mode := strings.TrimSpace(stringValue(extension["performanceMode"])); mode != "" {
				serviceVolume.PerformanceMode = mode
			}
			serviceVolume.ReadOnly = boolValue(extension["readOnly"])
		}
		spec.Volumes[volumeName] = serviceVolume
	}

	serviceNames := make([]string, 0, len(mapValue(raw["services"])))
	for name := range mapValue(raw["services"]) {
		serviceNames = append(serviceNames, name)
	}
	sort.Strings(serviceNames)

	for _, serviceName := range serviceNames {
		rawService := mapValue(mapValue(raw["services"])[serviceName])
		service, warnings := parseStackService(spec, envValues, serviceName, rawService)
		spec.Warnings = append(spec.Warnings, warnings...)
		spec.Services = append(spec.Services, service)
	}

	return spec, nil
}

func parseStackService(
	spec stackSpec,
	baseEnv map[string]string,
	serviceName string,
	rawService map[string]any,
) (stackServiceSpec, []string) {
	warnings := []string{}
	cfg := defaultDeployConfig(spec.ProjectDir)
	cfg.Project.Name = spec.Name
	cfg.Container.Name = sanitizeContainerName(spec.Name + "-" + serviceName)
	cfg.Deploy.Labels = map[string]string{
		"com.hubfly.stack.managed": "true",
		"com.hubfly.stack.name":    spec.Name,
		"com.hubfly.stack.service": serviceName,
	}

	if extension := mapValue(rawService["x-hubfly"]); len(extension) > 0 {
		if tier := strings.TrimSpace(stringValue(extension["tier"])); tier != "" {
			cfg.Deploy.Tier = tier
		}
		if resources := mapValue(extension["resources"]); len(resources) > 0 {
			if cpu := floatValue(resources["cpu"]); cpu > 0 {
				cfg.Deploy.Resources.CPU = cpu
			}
			if ram := intValue(resources["ram"]); ram > 0 {
				cfg.Deploy.Resources.RAM = ram
			}
			if storage := intValue(resources["storage"]); storage > 0 {
				cfg.Deploy.Resources.Storage = storage
			}
		}
		if runtime := mapValue(extension["runtime"]); len(runtime) > 0 {
			if value, ok := runtime["autoSleep"]; ok {
				cfg.Deploy.Runtime.AutoSleep = boolValue(value)
			}
			if value, ok := runtime["autoScale"]; ok {
				cfg.Deploy.Runtime.AutoScale = boolValue(value)
			}
			if value, ok := runtime["is24x7"]; ok {
				cfg.Deploy.Runtime.Is24x7 = boolValue(value)
			}
			if mode := strings.TrimSpace(stringValue(runtime["autoScaleMode"])); mode != "" {
				cfg.Deploy.Runtime.AutoScaleMode = mode
			}
		}
	}
	normalizeDeployConfig(&cfg, spec.ProjectDir)

	envValues := map[string]string{}
	for key, value := range baseEnv {
		envValues[key] = value
	}
	for _, filePath := range stringListValue(rawService["env_file"]) {
		resolved := filePath
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(spec.ProjectDir, resolved)
		}
		fileEnv, err := loadEnvFile(resolved)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("service %s: failed to read env_file %s: %v", serviceName, filePath, err))
			continue
		}
		for key, value := range fileEnv {
			envValues[key] = value
		}
	}

	environment := parseComposeEnvironment(rawService["environment"], envValues)
	ports, portWarnings := parseComposePorts(rawService["ports"], rawService["expose"])
	warnings = append(warnings, portWarnings...)
	labels := parseStringMap(rawService["labels"])
	for key, value := range labels {
		cfg.Deploy.Labels[key] = value
	}

	mounts, mountWarnings := parseComposeMounts(rawService["volumes"], spec.Volumes)
	warnings = append(warnings, mountWarnings...)

	var buildSpec *stackBuildSpec
	if rawBuild := rawService["build"]; rawBuild != nil {
		buildSpec, warnings = parseStackBuild(rawBuild, spec.ProjectDir, serviceName, warnings)
	}

	if rawCommand, ok := rawService["command"]; ok {
		cfg.Deploy.Process.Command = parseCommandValue(rawCommand)
	}
	if rawEntrypoint, ok := rawService["entrypoint"]; ok {
		cfg.Deploy.Process.Entrypoint = parseCommandValue(rawEntrypoint)
	}
	if workingDir := strings.TrimSpace(stringValue(rawService["working_dir"])); workingDir != "" {
		cfg.Deploy.Process.WorkingDir = workingDir
	}

	service := stackServiceSpec{
		Name:          serviceName,
		ContainerName: cfg.Container.Name,
		Image:         strings.TrimSpace(stringValue(rawService["image"])),
		Build:         buildSpec,
		Command:       cfg.Deploy.Process.Command,
		Entrypoint:    cfg.Deploy.Process.Entrypoint,
		WorkingDir:    cfg.Deploy.Process.WorkingDir,
		Environment:   environment,
		Ports:         ports,
		DependsOn:     parseComposeDependsOn(rawService["depends_on"]),
		Healthcheck:   parseComposeHealthcheck(rawService["healthcheck"]),
		RestartPolicy: parseComposeRestartPolicy(rawService["restart"]),
		Labels:        cfg.Deploy.Labels,
		NetworkAlias:  uniqueStrings(append([]string{serviceName}, stringListValue(rawService["networks"])...)),
		Mounts:        mounts,
		Tier:          cfg.Deploy.Tier,
		Resources:     cfg.Deploy.Resources,
		Runtime:       cfg.Deploy.Runtime,
	}
	service.ConfigHash = stackConfigHash(map[string]any{
		"name":          service.Name,
		"containerName": service.ContainerName,
		"image":         service.Image,
		"build":         service.Build,
		"command":       service.Command,
		"entrypoint":    service.Entrypoint,
		"workingDir":    service.WorkingDir,
		"environment":   service.Environment,
		"ports":         service.Ports,
		"healthcheck":   service.Healthcheck,
		"restart":       service.RestartPolicy,
		"labels":        service.Labels,
		"mounts":        service.Mounts,
		"tier":          service.Tier,
		"resources":     service.Resources,
		"runtime":       service.Runtime,
	})
	return service, warnings
}

func parseStackBuild(
	rawBuild any,
	projectDir, serviceName string,
	warnings []string,
) (*stackBuildSpec, []string) {
	build := &stackBuildSpec{
		ContextDir:     projectDir,
		DockerfilePath: filepath.Join(projectDir, "Dockerfile"),
		Args:           map[string]string{},
	}
	switch value := rawBuild.(type) {
	case string:
		build.ContextDir = filepath.Join(projectDir, filepath.Clean(value))
		build.DockerfilePath = filepath.Join(build.ContextDir, "Dockerfile")
	case map[string]any:
		contextDir := strings.TrimSpace(stringValue(value["context"]))
		if contextDir == "" {
			contextDir = "."
		}
		build.ContextDir = filepath.Join(projectDir, filepath.Clean(contextDir))
		dockerfilePath := strings.TrimSpace(stringValue(value["dockerfile"]))
		if dockerfilePath == "" {
			dockerfilePath = "Dockerfile"
		}
		if filepath.IsAbs(dockerfilePath) {
			build.DockerfilePath = dockerfilePath
		} else {
			build.DockerfilePath = filepath.Join(build.ContextDir, filepath.Clean(dockerfilePath))
		}
		build.Target = strings.TrimSpace(stringValue(value["target"]))
		build.Args = parseStringMap(value["args"])
	default:
		warnings = append(warnings, fmt.Sprintf("service %s: unsupported build definition; skipping local build", serviceName))
		return nil, warnings
	}

	if _, err := os.Stat(build.ContextDir); err != nil {
		warnings = append(warnings, fmt.Sprintf("service %s: build context %s is missing", serviceName, build.ContextDir))
	}
	if _, err := os.Stat(build.DockerfilePath); err != nil {
		warnings = append(warnings, fmt.Sprintf("service %s: dockerfile %s is missing", serviceName, build.DockerfilePath))
	}
	return build, warnings
}

func parseComposeEnvironment(raw any, envValues map[string]string) []deployEnvVar {
	parsed := []deployEnvVar{}
	switch value := raw.(type) {
	case []any:
		for _, entry := range value {
			text := strings.TrimSpace(stringValue(entry))
			if text == "" {
				continue
			}
			parts := strings.SplitN(text, "=", 2)
			name := strings.TrimSpace(parts[0])
			if name == "" {
				continue
			}
			resolved := envValues[name]
			if len(parts) == 2 {
				resolved = parts[1]
			}
			parsed = append(parsed, deployEnvVar{Name: name, Value: resolved, Scope: "runtime"})
		}
	case map[string]any:
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			resolved := stringValue(value[key])
			if resolved == "" {
				resolved = envValues[key]
			}
			parsed = append(parsed, deployEnvVar{Name: key, Value: resolved, Scope: "runtime"})
		}
	}
	return parsed
}

func parseComposePorts(rawPorts any, rawExpose any) ([]deployPort, []string) {
	ports := []deployPort{}
	warnings := []string{}
	for _, entry := range listValue(rawPorts) {
		switch value := entry.(type) {
		case int:
			ports = append(ports, deployPort{Container: value, Protocol: "TCP"})
		case string:
			parsed, warning := parseComposePortString(value)
			if parsed.Container > 0 {
				ports = append(ports, parsed)
			}
			if warning != "" {
				warnings = append(warnings, warning)
			}
		case map[string]any:
			containerPort := intValue(value["target"])
			if containerPort <= 0 {
				continue
			}
			protocol := strings.ToUpper(strings.TrimSpace(stringValue(value["protocol"])))
			if protocol == "" {
				protocol = "TCP"
			}
			ports = append(ports, deployPort{
				Container: containerPort,
				Host:      intValue(value["published"]),
				Protocol:  protocol,
				HostIP:    strings.TrimSpace(stringValue(value["host_ip"])),
			})
		}
	}
	for _, entry := range listValue(rawExpose) {
		port := intValue(entry)
		if port > 0 {
			ports = append(ports, deployPort{Container: port, Protocol: "TCP"})
		}
	}
	return uniqueDeployPorts(ports), warnings
}

func parseComposePortString(value string) (deployPort, string) {
	text := strings.TrimSpace(value)
	protocol := "TCP"
	if slash := strings.LastIndex(text, "/"); slash >= 0 {
		suffix := strings.TrimSpace(text[slash+1:])
		if strings.EqualFold(suffix, "udp") {
			protocol = "UDP"
		}
		text = text[:slash]
	}
	parts := strings.Split(text, ":")
	switch len(parts) {
	case 1:
		return deployPort{Container: intValue(parts[0]), Protocol: protocol}, ""
	case 2:
		return deployPort{Host: intValue(parts[0]), Container: intValue(parts[1]), Protocol: protocol}, ""
	case 3:
		return deployPort{
			HostIP:    strings.TrimSpace(parts[0]),
			Host:      intValue(parts[1]),
			Container: intValue(parts[2]),
			Protocol:  protocol,
		}, ""
	default:
		return deployPort{}, fmt.Sprintf("unsupported port mapping %q ignored", value)
	}
}

func parseComposeDependsOn(raw any) []string {
	out := []string{}
	switch value := raw.(type) {
	case []any:
		for _, entry := range value {
			name := strings.TrimSpace(stringValue(entry))
			if name != "" {
				out = append(out, name)
			}
		}
	case map[string]any:
		for name := range value {
			if strings.TrimSpace(name) != "" {
				out = append(out, name)
			}
		}
		sort.Strings(out)
	}
	return uniqueStrings(out)
}

func parseComposeHealthcheck(raw any) *deployHealthcheck {
	value := mapValue(raw)
	if len(value) == 0 {
		return nil
	}
	test := parseCommandValue(value["test"])
	if len(test) == 0 {
		return nil
	}
	return &deployHealthcheck{
		Test:        test,
		Interval:    strings.TrimSpace(stringValue(value["interval"])),
		Timeout:     strings.TrimSpace(stringValue(value["timeout"])),
		StartPeriod: strings.TrimSpace(stringValue(value["start_period"])),
		Retries:     intValue(value["retries"]),
	}
}

func parseComposeRestartPolicy(raw any) *deployRestartPolicy {
	name := strings.TrimSpace(stringValue(raw))
	if name == "" {
		return nil
	}
	out := &deployRestartPolicy{Name: name}
	if strings.HasPrefix(name, "on-failure") {
		out.Name = "on-failure"
		if idx := strings.Index(name, ":"); idx >= 0 {
			out.MaximumRetryCount = intValue(name[idx+1:])
		}
	}
	return out
}

func parseComposeMounts(raw any, definedVolumes map[string]stackVolumeSpec) ([]stackMount, []string) {
	mounts := []stackMount{}
	warnings := []string{}
	for _, entry := range listValue(raw) {
		switch value := entry.(type) {
		case string:
			parts := strings.Split(value, ":")
			if len(parts) < 2 {
				warnings = append(warnings, fmt.Sprintf("anonymous volume %q ignored", value))
				continue
			}
			source := strings.TrimSpace(parts[0])
			target := strings.TrimSpace(parts[1])
			if source == "" || target == "" {
				continue
			}
			if isBindLikeMount(source) {
				warnings = append(warnings, fmt.Sprintf("bind mount %q ignored; use named volumes for Hubfly stacks", value))
				continue
			}
			if _, ok := definedVolumes[source]; !ok {
				warnings = append(warnings, fmt.Sprintf("volume %s referenced but not defined; using default managed volume settings", source))
			}
			mounts = append(mounts, stackMount{
				VolumeName: source,
				MountPath:  target,
				ReadOnly:   len(parts) > 2 && strings.EqualFold(strings.TrimSpace(parts[2]), "ro"),
			})
		case map[string]any:
			mountType := strings.ToLower(strings.TrimSpace(stringValue(value["type"])))
			if mountType == "bind" {
				warnings = append(warnings, "bind mounts are ignored for Hubfly stack deploys; use named volumes instead")
				continue
			}
			source := strings.TrimSpace(stringValue(value["source"]))
			target := strings.TrimSpace(stringValue(value["target"]))
			if source == "" || target == "" {
				continue
			}
			mounts = append(mounts, stackMount{
				VolumeName: source,
				MountPath:  target,
				ReadOnly:   boolValue(value["read_only"]),
			})
		}
	}
	return mounts, warnings
}

func loadEnvFile(path string) (map[string]string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		out[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
	}
	return out, nil
}

func expandComposeVariables(content string, env map[string]string) string {
	content = stackVariablePattern.ReplaceAllStringFunc(content, func(match string) string {
		groups := stackVariablePattern.FindStringSubmatch(match)
		if len(groups) < 2 {
			return match
		}
		if value, ok := env[groups[1]]; ok {
			return value
		}
		if len(groups) >= 4 {
			return groups[3]
		}
		return ""
	})
	simplePattern := regexp.MustCompile(`\$([A-Za-z_][A-Za-z0-9_]*)`)
	return simplePattern.ReplaceAllStringFunc(content, func(match string) string {
		key := strings.TrimPrefix(match, "$")
		return env[key]
	})
}

func parseCommandValue(raw any) []string {
	switch value := raw.(type) {
	case string:
		return strings.Fields(value)
	case []any:
		out := make([]string, 0, len(value))
		for _, entry := range value {
			text := strings.TrimSpace(stringValue(entry))
			if text != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func parseStringMap(raw any) map[string]string {
	out := map[string]string{}
	switch value := raw.(type) {
	case map[string]any:
		for key, entry := range value {
			out[key] = stringValue(entry)
		}
	case []any:
		for _, entry := range value {
			text := strings.TrimSpace(stringValue(entry))
			if text == "" {
				continue
			}
			parts := strings.SplitN(text, "=", 2)
			out[parts[0]] = ""
			if len(parts) == 2 {
				out[parts[0]] = parts[1]
			}
		}
	}
	return out
}

func mapValue(raw any) map[string]any {
	if raw == nil {
		return map[string]any{}
	}
	switch value := raw.(type) {
	case map[string]any:
		return value
	case map[any]any:
		out := make(map[string]any, len(value))
		for key, item := range value {
			out[stringValue(key)] = item
		}
		return out
	default:
		return map[string]any{}
	}
}

func listValue(raw any) []any {
	switch value := raw.(type) {
	case []any:
		return value
	case []string:
		out := make([]any, 0, len(value))
		for _, item := range value {
			out = append(out, item)
		}
		return out
	default:
		return nil
	}
}

func stringListValue(raw any) []string {
	switch value := raw.(type) {
	case string:
		if strings.TrimSpace(value) == "" {
			return nil
		}
		return []string{strings.TrimSpace(value)}
	case []any:
		out := make([]string, 0, len(value))
		for _, entry := range value {
			text := strings.TrimSpace(stringValue(entry))
			if text != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func stringValue(raw any) string {
	switch value := raw.(type) {
	case string:
		return value
	case fmt.Stringer:
		return value.String()
	case int:
		return strconv.Itoa(value)
	case int64:
		return strconv.FormatInt(value, 10)
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(value)
	default:
		return ""
	}
}

func intValue(raw any) int {
	switch value := raw.(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case string:
		parsed, _ := strconv.Atoi(strings.TrimSpace(value))
		return parsed
	default:
		return 0
	}
}

func floatValue(raw any) float64 {
	switch value := raw.(type) {
	case float64:
		return value
	case int:
		return float64(value)
	case string:
		parsed, _ := strconv.ParseFloat(strings.TrimSpace(value), 64)
		return parsed
	default:
		return 0
	}
}

func boolValue(raw any) bool {
	switch value := raw.(type) {
	case bool:
		return value
	case string:
		parsed, _ := strconv.ParseBool(strings.TrimSpace(value))
		return parsed
	default:
		return false
	}
}

func uniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func uniqueDeployPorts(values []deployPort) []deployPort {
	if len(values) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]deployPort, 0, len(values))
	for _, value := range values {
		if value.Container <= 0 {
			continue
		}
		key := fmt.Sprintf("%s:%s:%d:%d", value.HostIP, value.Protocol, value.Host, value.Container)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	return out
}

func isBindLikeMount(source string) bool {
	return strings.HasPrefix(source, ".") ||
		strings.HasPrefix(source, "/") ||
		strings.HasPrefix(source, "~") ||
		strings.Contains(source, "\\")
}

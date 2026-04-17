package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"unicode"
)

const deployConfigFileName = "hubfly.build.json"

func deployConfigPath(projectDir string) string {
	return filepath.Join(projectDir, deployConfigFileName)
}

func resolveDeployWorkspace(configInput string) (string, string, error) {
	if strings.TrimSpace(configInput) == "" {
		projectDir, err := os.Getwd()
		if err != nil {
			return "", "", err
		}
		return projectDir, deployConfigPath(projectDir), nil
	}

	resolved := strings.TrimSpace(configInput)
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

	if info, statErr := os.Stat(resolved); statErr == nil && info.IsDir() {
		return resolved, deployConfigPath(resolved), nil
	}

	return filepath.Dir(resolved), resolved, nil
}

func generatedDockerfilePath(projectDir string) string {
	return filepath.Join(projectDir, ".hubfly", "Dockerfile.generated")
}

func builderBinaryPath() string {
	name := "hubfly-builder"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(hubflyDir(), "tools", name)
}

func builderInstallStatePath() string {
	return filepath.Join(hubflyDir(), "tools", "hubfly-builder.json")
}

func builderReleaseCachePath() string {
	return filepath.Join(hubflyDir(), "tools", "hubfly-builder-release-cache.json")
}

func loadOrInitDeployConfig(projectDir string) (deployConfigFile, bool, error) {
	return loadOrInitDeployConfigAt(projectDir, deployConfigPath(projectDir))
}

func loadOrInitDeployConfigAt(projectDir, path string) (deployConfigFile, bool, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return deployConfigFile{}, false, err
		}
		cfg := defaultDeployConfig(projectDir)
		if err := saveDeployConfig(path, cfg); err != nil {
			return deployConfigFile{}, false, err
		}
		return cfg, true, nil
	}

	var cfg deployConfigFile
	if err := json.Unmarshal(content, &cfg); err != nil {
		return deployConfigFile{}, false, err
	}
	normalizeDeployConfig(&cfg, projectDir)
	return cfg, false, nil
}

func saveDeployConfig(path string, cfg deployConfigFile) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	payload, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	return os.WriteFile(path, payload, 0o644)
}

func defaultDeployConfig(projectDir string) deployConfigFile {
	base := filepath.Base(projectDir)
	containerName := sanitizeContainerName(base)
	if containerName == "" {
		containerName = "app"
	}

	var cfg deployConfigFile
	cfg.Version = 1
	cfg.Project.Name = base
	cfg.Container.Name = containerName
	cfg.Build.Mode = "auto"
	cfg.Build.DockerfilePath = ""
	cfg.Build.WorkingDir = "."
	cfg.Build.ContextDir = "."
	cfg.Deploy.Tier = "dedicated"
	cfg.Deploy.Resources = deployResources{
		CPU:     1,
		RAM:     800,
		Storage: 1,
	}
	cfg.Deploy.Runtime = deployRuntime{
		AutoSleep: false,
		AutoScale: false,
		Is24x7:    true,
	}
	cfg.Deploy.Process = deployProcess{}
	cfg.Deploy.Labels = map[string]string{}
	return cfg
}

func normalizeDeployConfig(cfg *deployConfigFile, projectDir string) {
	defaults := defaultDeployConfig(projectDir)

	if cfg.Version == 0 {
		cfg.Version = defaults.Version
	}
	if strings.TrimSpace(cfg.Project.Name) == "" {
		cfg.Project.Name = defaults.Project.Name
	}
	if strings.TrimSpace(cfg.Container.Name) == "" {
		cfg.Container.Name = defaults.Container.Name
	}
	cfg.Container.Name = sanitizeContainerName(cfg.Container.Name)
	if cfg.Container.Name == "" {
		cfg.Container.Name = defaults.Container.Name
	}
	if strings.TrimSpace(cfg.Build.Mode) == "" {
		cfg.Build.Mode = defaults.Build.Mode
	}
	cfg.Build.Mode = normalizeBuildMode(cfg.Build.Mode)
	cfg.Build.DockerfilePath = strings.TrimSpace(cfg.Build.DockerfilePath)
	if cfg.Build.Mode == "dockerfile" && cfg.Build.DockerfilePath == "" {
		cfg.Build.DockerfilePath = "Dockerfile"
	}
	if strings.TrimSpace(cfg.Build.WorkingDir) == "" {
		cfg.Build.WorkingDir = defaults.Build.WorkingDir
	}
	if strings.TrimSpace(cfg.Build.ContextDir) == "" {
		cfg.Build.ContextDir = defaults.Build.ContextDir
	}
	if strings.TrimSpace(cfg.Deploy.Tier) == "" {
		cfg.Deploy.Tier = defaults.Deploy.Tier
	}
	cfg.Deploy.Tier = normalizeDeployTier(cfg.Deploy.Tier)
	if cfg.Deploy.Resources.CPU <= 0 {
		cfg.Deploy.Resources.CPU = defaults.Deploy.Resources.CPU
	}
	if cfg.Deploy.Resources.RAM <= 0 {
		cfg.Deploy.Resources.RAM = defaults.Deploy.Resources.RAM
	}
	if cfg.Deploy.Resources.Storage <= 0 {
		cfg.Deploy.Resources.Storage = defaults.Deploy.Resources.Storage
	}
	if cfg.Deploy.Process.Command == nil {
		cfg.Deploy.Process.Command = defaults.Deploy.Process.Command
	}
	if cfg.Deploy.Process.Entrypoint == nil {
		cfg.Deploy.Process.Entrypoint = defaults.Deploy.Process.Entrypoint
	}
	if cfg.Deploy.Labels == nil {
		cfg.Deploy.Labels = map[string]string{}
	}
	applyDeployTierConstraints(cfg)

	for idx := range cfg.Env {
		cfg.Env[idx].Name = strings.TrimSpace(cfg.Env[idx].Name)
		cfg.Env[idx].Scope = normalizeEnvScope(cfg.Env[idx].Scope)
	}
}

func normalizeDeployTier(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "shared":
		return "shared"
	default:
		return "dedicated"
	}
}

func applyDeployTierConstraints(cfg *deployConfigFile) {
	if cfg.Deploy.Tier == "shared" {
		cfg.Deploy.Resources.CPU = 0.3
		cfg.Deploy.Resources.RAM = 256
		cfg.Deploy.Resources.Storage = 1
		cfg.Deploy.Resources.MaxCPU = 0
		cfg.Deploy.Resources.MaxRAM = 0
		cfg.Deploy.Runtime.AutoSleep = true
		cfg.Deploy.Runtime.AutoScale = false
		cfg.Deploy.Runtime.Is24x7 = false
		cfg.Deploy.Runtime.AutoScaleMode = ""
		return
	}

	if cfg.Deploy.Runtime.AutoSleep {
		cfg.Deploy.Runtime.AutoScale = false
		cfg.Deploy.Runtime.Is24x7 = false
	}
	if cfg.Deploy.Runtime.AutoScale {
		cfg.Deploy.Runtime.AutoSleep = false
		cfg.Deploy.Runtime.Is24x7 = true
		if strings.TrimSpace(cfg.Deploy.Runtime.AutoScaleMode) == "" {
			cfg.Deploy.Runtime.AutoScaleMode = "vertical"
		}
		if cfg.Deploy.Resources.MaxCPU > 0 && cfg.Deploy.Resources.MaxCPU < cfg.Deploy.Resources.CPU {
			cfg.Deploy.Resources.MaxCPU = cfg.Deploy.Resources.CPU
		}
		if cfg.Deploy.Resources.MaxRAM > 0 && cfg.Deploy.Resources.MaxRAM < cfg.Deploy.Resources.RAM {
			cfg.Deploy.Resources.MaxRAM = cfg.Deploy.Resources.RAM
		}
	} else {
		cfg.Deploy.Runtime.AutoScaleMode = ""
		cfg.Deploy.Resources.MaxCPU = 0
		cfg.Deploy.Resources.MaxRAM = 0
	}
}

func sanitizeContainerName(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return ""
	}

	var builder strings.Builder
	lastDash := false
	for _, ch := range value {
		switch {
		case unicode.IsLower(ch), unicode.IsDigit(ch):
			builder.WriteRune(ch)
			lastDash = false
		default:
			if !lastDash {
				builder.WriteByte('-')
				lastDash = true
			}
		}
	}

	sanitized := strings.Trim(builder.String(), "-")
	sanitized = strings.TrimSpace(sanitized)
	if sanitized == "" {
		return ""
	}
	return sanitized
}

func applyInspectOutput(cfg *deployConfigFile, inspect builderInspectOutput) {
	cfg.Build.Runtime = inspect.BuildConfig.Runtime
	cfg.Build.Framework = inspect.BuildConfig.Framework
	cfg.Build.Version = inspect.BuildConfig.Version
	cfg.Build.InstallCommand = inspect.BuildConfig.InstallCommand
	cfg.Build.SetupCommands = cloneStrings(inspect.BuildConfig.SetupCommands)
	cfg.Build.BuildCommand = inspect.BuildConfig.BuildCommand
	cfg.Build.PostBuildCommands = cloneStrings(inspect.BuildConfig.PostBuildCommands)
	cfg.Build.RunCommand = inspect.BuildConfig.RunCommand
	cfg.Build.RuntimeInitCommand = inspect.BuildConfig.RuntimeInitCommand
	cfg.Build.ExposePort = inspect.BuildConfig.ExposePort
	if strings.TrimSpace(inspect.BuildConfig.AppDir) != "" {
		cfg.Build.WorkingDir = inspect.BuildConfig.AppDir
	}
	if strings.TrimSpace(inspect.BuildConfig.BuildContextDir) != "" {
		cfg.Build.ContextDir = inspect.BuildConfig.BuildContextDir
	}

	if len(cfg.Deploy.Ports) == 0 {
		port, err := strconv.Atoi(strings.TrimSpace(inspect.BuildConfig.ExposePort))
		if err == nil && port > 0 {
			cfg.Deploy.Ports = []deployPort{
				{
					Container: port,
					Protocol:  "TCP",
				},
			}
		}
	}
}

func cloneStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, len(values))
	copy(out, values)
	return out
}

func normalizeEnvScope(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "build", "both":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "runtime"
	}
}

func normalizeBuildMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "manual":
		return "manual"
	case "docker", "dockerfile":
		return "dockerfile"
	default:
		return "auto"
	}
}

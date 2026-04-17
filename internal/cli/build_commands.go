package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

type buildCommandOptions struct {
	ConfigPath     string
	DockerfilePath string
	BuilderVersion string
	Force          bool
	PrintOnly      bool
	JSON           bool
}

func runBuildCommand(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: hubfly build <init|validate|edit|explain>")
	}

	switch args[0] {
	case "init":
		opts, err := parseBuildCommandOptions("init", args[1:])
		if err != nil {
			return err
		}
		return buildInitFlow(opts)
	case "validate":
		opts, err := parseBuildCommandOptions("validate", args[1:])
		if err != nil {
			return err
		}
		return buildValidateFlow(opts)
	case "edit":
		opts, err := parseBuildCommandOptions("edit", args[1:])
		if err != nil {
			return err
		}
		return buildEditFlow(opts)
	case "explain":
		opts, err := parseBuildCommandOptions("explain", args[1:])
		if err != nil {
			return err
		}
		return buildExplainFlow(opts)
	default:
		return fmt.Errorf("unknown build command: %s", args[0])
	}
}

func parseBuildCommandOptions(command string, args []string) (buildCommandOptions, error) {
	var opts buildCommandOptions
	fs := flag.NewFlagSet("build "+command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.ConfigPath, "config", "", "path to hubfly.build.json or a project directory")
	fs.StringVar(&opts.DockerfilePath, "dockerfile", "", "set build.mode=dockerfile and the Dockerfile path")
	fs.StringVar(&opts.BuilderVersion, "builder-version", "", "pin a specific hubfly-builder release tag")
	fs.BoolVar(&opts.Force, "force", false, "overwrite the current config with defaults during init")
	fs.BoolVar(&opts.PrintOnly, "print", false, "print the resulting config instead of a short summary")
	fs.BoolVar(&opts.JSON, "json", false, "emit JSON output where supported")
	if err := fs.Parse(args); err != nil {
		return buildCommandOptions{}, err
	}
	if len(fs.Args()) > 0 {
		return buildCommandOptions{}, fmt.Errorf("unexpected build arguments: %s", strings.Join(fs.Args(), " "))
	}
	return opts, nil
}

func buildInitFlow(opts buildCommandOptions) error {
	projectDir, cfgPath, err := resolveDeployWorkspace(opts.ConfigPath)
	if err != nil {
		return err
	}

	cfg := defaultDeployConfig(projectDir)
	if !opts.Force {
		if existing, _, loadErr := loadOrInitDeployConfigAt(projectDir, cfgPath); loadErr == nil {
			cfg = existing
		}
	}
	normalizeDeployConfig(&cfg, projectDir)

	if strings.TrimSpace(opts.DockerfilePath) != "" {
		cfg.Build.Mode = "dockerfile"
		cfg.Build.DockerfilePath = strings.TrimSpace(opts.DockerfilePath)
	}

	if err := saveDeployConfig(cfgPath, cfg); err != nil {
		return err
	}

	if opts.PrintOnly {
		payload, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(payload))
		return nil
	}

	fmt.Printf("Initialized %s\n", cfgPath)
	fmt.Printf("Build mode: %s\n", cfg.Build.Mode)
	return nil
}

func buildValidateFlow(opts buildCommandOptions) error {
	projectDir, cfgPath, err := resolveDeployWorkspace(opts.ConfigPath)
	if err != nil {
		return err
	}

	cfg, _, err := loadOrInitDeployConfigAt(projectDir, cfgPath)
	if err != nil {
		return err
	}
	normalizeDeployConfig(&cfg, projectDir)
	if strings.TrimSpace(opts.DockerfilePath) != "" {
		cfg.Build.Mode = "dockerfile"
		cfg.Build.DockerfilePath = strings.TrimSpace(opts.DockerfilePath)
	}

	prepared, err := prepareDeployBuild(projectDir, cfgPath, &cfg, opts.BuilderVersion)
	if err != nil {
		return err
	}
	if err := saveDeployConfig(cfgPath, cfg); err != nil {
		return err
	}

	if opts.JSON {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{
			"config":          cfg,
			"builderVersion":  prepared.BuilderVersion,
			"dockerfilePath":  prepared.DockerfilePath,
			"buildSource":     prepared.BuildSource,
			"buildSourcePath": prepared.BuildSourcePath,
			"warnings":        prepared.Warnings,
		})
	}

	fmt.Printf("Config valid: %s\n", cfgPath)
	fmt.Printf("Build source: %s\n", displayDeployBuildSource(projectDir, prepared))
	if len(prepared.Warnings) > 0 {
		printDeployWarnings("Validation warnings", prepared.Warnings)
	}
	return nil
}

func buildEditFlow(opts buildCommandOptions) error {
	projectDir, cfgPath, err := resolveDeployWorkspace(opts.ConfigPath)
	if err != nil {
		return err
	}

	cfg, _, err := loadOrInitDeployConfigAt(projectDir, cfgPath)
	if err != nil {
		return err
	}
	normalizeDeployConfig(&cfg, projectDir)
	if err := saveDeployConfig(cfgPath, cfg); err != nil {
		return err
	}

	if err := openDeployConfigEditor(cfgPath); err != nil {
		return err
	}
	return nil
}

func buildExplainFlow(opts buildCommandOptions) error {
	projectDir, cfgPath, err := resolveDeployWorkspace(opts.ConfigPath)
	if err != nil {
		return err
	}

	cfg, _, err := loadOrInitDeployConfigAt(projectDir, cfgPath)
	if err != nil {
		return err
	}
	normalizeDeployConfig(&cfg, projectDir)
	if strings.TrimSpace(opts.DockerfilePath) != "" {
		cfg.Build.Mode = "dockerfile"
		cfg.Build.DockerfilePath = strings.TrimSpace(opts.DockerfilePath)
	}

	if hasCustomDockerfileConfig(cfg) {
		dockerfilePath, err := resolveConfiguredDockerfilePath(projectDir, cfg)
		if err != nil {
			return err
		}
		if opts.JSON {
			return json.NewEncoder(os.Stdout).Encode(map[string]any{
				"mode":       "dockerfile",
				"dockerfile": dockerfilePath,
				"config":     cfg,
			})
		}

		fmt.Printf("Build mode: dockerfile\nDockerfile: %s\n", dockerfilePath)
		return nil
	}

	builderPath, builderVersion, err := ensureLocalBuilderBinary(builderInstallRequest{
		RequestedVersion: opts.BuilderVersion,
	})
	if err != nil {
		return err
	}

	inspect, err := runBuilderInspect(builderPath, projectDir, cfgPath)
	if err != nil {
		return wrapBuilderInspectError(projectDir, cfg, err)
	}

	if opts.JSON {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{
			"builderVersion": builderVersion,
			"inspect":        inspect,
		})
	}

	fmt.Printf("Builder: %s\n", builderVersion)
	fmt.Printf("Runtime: %s\n", displayDeployValue(inspect.BuildConfig.Runtime, "unknown"))
	fmt.Printf("Framework: %s\n", displayDeployValue(inspect.BuildConfig.Framework, "none"))
	fmt.Printf("Version: %s\n", displayDeployValue(inspect.BuildConfig.Version, "auto"))
	fmt.Printf("Working dir: %s\n", displayDeployValue(inspect.BuildConfig.AppDir, "."))
	fmt.Printf("Context dir: %s\n", displayDeployValue(inspect.BuildConfig.BuildContextDir, "."))
	fmt.Printf("Expose port: %s\n", displayDeployValue(inspect.BuildConfig.ExposePort, "not detected"))
	if len(inspect.BuildArgKeys) > 0 {
		fmt.Printf("Build args: %s\n", strings.Join(inspect.BuildArgKeys, ", "))
	}
	if len(inspect.BuildSecretKeys) > 0 {
		fmt.Printf("Build secrets: %s\n", strings.Join(inspect.BuildSecretKeys, ", "))
	}
	if len(inspect.BuildConfig.ValidationWarnings) > 0 {
		printDeployWarnings("Builder warnings", inspect.BuildConfig.ValidationWarnings)
	}
	return nil
}

func editorCommandExists() bool {
	editor := strings.TrimSpace(os.Getenv("EDITOR"))
	if editor == "" {
		return false
	}
	parts := strings.Fields(editor)
	if len(parts) == 0 {
		return false
	}
	_, err := exec.LookPath(parts[0])
	return err == nil
}

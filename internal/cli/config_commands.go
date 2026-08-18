package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func configFlow(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: hubfly config <validate|migrate|explain>")
	}
	if args[0] == "validate" || args[0] == "explain" {
		return runBuildCommand(append([]string{args[0]}, args[1:]...))
	}
	if args[0] != "migrate" {
		return fmt.Errorf("unknown config command %q", args[0])
	}
	fs := flag.NewFlagSet("config migrate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configPath := fs.String("config", "", "path to hubfly.build.json")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	projectDir, path, err := resolveDeployWorkspace(*configPath)
	if err != nil {
		return err
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var cfg deployConfigFile
	if err := json.Unmarshal(payload, &cfg); err != nil {
		return err
	}
	if cfg.Version >= 2 {
		fmt.Printf("Config is already schema version %d: %s\n", cfg.Version, path)
		return nil
	}
	if cfg.Version != 1 {
		return fmt.Errorf("unsupported config schema version %d", cfg.Version)
	}
	backup := fmt.Sprintf("%s.v1.%s.bak", path, time.Now().UTC().Format("20060102T150405Z"))
	if err := os.WriteFile(backup, payload, 0o600); err != nil {
		return fmt.Errorf("create migration backup: %w", err)
	}
	cfg.Version = 2
	normalizeDeployConfig(&cfg, projectDir)
	if err := saveDeployConfig(path, cfg); err != nil {
		return err
	}
	relBackup, _ := filepath.Rel(projectDir, backup)
	fmt.Printf("Migrated %s from schema v1 to v2\n", path)
	fmt.Printf("Backup: %s\n", strings.TrimSpace(relBackup))
	return nil
}

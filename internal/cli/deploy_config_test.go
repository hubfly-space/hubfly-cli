package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeDeployConfigDefaultsDockerfileMode(t *testing.T) {
	projectDir := t.TempDir()
	cfg := deployConfigFile{}
	cfg.Build.Mode = "docker"

	normalizeDeployConfig(&cfg, projectDir)

	if cfg.Build.Mode != "dockerfile" {
		t.Fatalf("expected dockerfile mode, got %q", cfg.Build.Mode)
	}
	if cfg.Build.DockerfilePath != "Dockerfile" {
		t.Fatalf("expected default dockerfile path, got %q", cfg.Build.DockerfilePath)
	}
}

func TestFindProjectDockerfilePrefersWorkingDir(t *testing.T) {
	projectDir := t.TempDir()
	appDir := filepath.Join(projectDir, "apps", "web")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatalf("failed to create app dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatalf("failed to write root Dockerfile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatalf("failed to write app Dockerfile: %v", err)
	}

	cfg := defaultDeployConfig(projectDir)
	cfg.Build.WorkingDir = "apps/web"

	path, ok := findProjectDockerfile(projectDir, cfg)
	if !ok {
		t.Fatalf("expected Dockerfile to be detected")
	}
	expected := filepath.Join(appDir, "Dockerfile")
	if path != expected {
		t.Fatalf("expected %q, got %q", expected, path)
	}
}

func TestBuilderVersionsMatch(t *testing.T) {
	if !builderVersionsMatch("1.7.1", "v1.7.1") {
		t.Fatalf("expected semver normalization to match")
	}
	if builderVersionsMatch("v1.7.1", "v1.7.2") {
		t.Fatalf("expected version mismatch")
	}
}

package cli

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDeployIgnoreMatcher(t *testing.T) {
	matcher := newIgnoreMatcher([]string{"node_modules", "**/*.log", "/dist/", "secrets/*", "!secrets/keep.txt", "# not a rule"})
	tests := map[string]bool{
		"node_modules":                true,
		"node_modules/react/index.js": true,
		"app/debug.log":               true,
		"debug.log":                   true,
		"dist/app.js":                 true,
		"src/dist.go":                 false,
		"secrets/token":               true,
		"secrets/keep.txt":            false,
		"src/main.go":                 false,
	}
	for path, want := range tests {
		if got := matcher.Ignored(path); got != want {
			t.Errorf("Ignored(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestScanBuildContextHonoursHubflyIgnore(t *testing.T) {
	dir := t.TempDir()
	mustWrite := func(rel string, size int) {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("main.go", 10)
	mustWrite("node_modules/x/index.js", 10)
	mustWrite("data/big.bin", largeContextFileBytes)
	mustWrite("fixtures/huge.bin", largeContextFileBytes)
	mustWrite(".hubflyignore", 0)
	if err := os.WriteFile(filepath.Join(dir, ".hubflyignore"), []byte("fixtures\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	report := scanBuildContext(dir)
	if !report.HubflyIgnore {
		t.Fatal("expected .hubflyignore to be detected")
	}
	if len(report.LargeFiles) != 1 || report.LargeFiles[0].Path != "data/big.bin" {
		t.Fatalf("large files = %+v, want only data/big.bin", report.LargeFiles)
	}
	if len(report.HeavyDirs) != 1 || report.HeavyDirs[0] != "node_modules" {
		t.Fatalf("heavy dirs = %v", report.HeavyDirs)
	}
	warnings := strings.Join(buildContextWarnings(report), "\n")
	if !strings.Contains(warnings, "node_modules/") || !strings.Contains(warnings, "data/big.bin") {
		t.Fatalf("warnings missing entries:\n%s", warnings)
	}
}

func TestApplyHubflyIgnoreWritesAndRemovesDockerfileIgnore(t *testing.T) {
	dir := t.TempDir()
	dockerfile := filepath.Join(dir, "Dockerfile")
	_ = os.WriteFile(dockerfile, []byte("FROM scratch\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, ".dockerignore"), []byte(".git\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, ".hubflyignore"), []byte("fixtures\n"), 0o644)

	cleanup, err := applyHubflyIgnore(dir, dockerfile)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(dockerfile + ".dockerignore")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), ".git") || !strings.Contains(string(data), "fixtures") {
		t.Fatalf("merged ignore file = %q", data)
	}
	cleanup()
	if _, err := os.Stat(dockerfile + ".dockerignore"); !os.IsNotExist(err) {
		t.Fatalf("expected generated ignore file to be removed, stat err = %v", err)
	}
}

func TestDeployExitCodes(t *testing.T) {
	tests := []struct {
		err  error
		want int
	}{
		{&deployExitError{code: exitDeployBuildFailed, err: errors.New("build")}, 10},
		{fmt.Errorf("wrapped: %w", &deployExitError{code: exitDeployConflict, err: errors.New("busy")}), 14},
		{withDeployExitCode(exitDeployUnknown, withDeployExitCode(exitDeployFailed, errors.New("x"))), 12},
		{fmt.Errorf("unexpected deploy arguments: x\n%s", deployUsage()), 2},
	}
	for _, tt := range tests {
		if got := exitCodeForError(tt.err); got != tt.want {
			t.Errorf("exitCodeForError(%q) = %d, want %d", tt.err, got, tt.want)
		}
	}
}

func TestIsTransientDeployError(t *testing.T) {
	tests := []struct {
		err  error
		want bool
	}{
		{&apiError{Status: http.StatusBadGateway}, true},
		{&apiError{Status: http.StatusTooManyRequests}, true},
		{&apiError{Status: http.StatusConflict}, false},
		{&apiError{Status: http.StatusUnauthorized}, false},
		{errors.New("read tcp 1.2.3.4:443: connection reset by peer"), true},
		{errors.New("manifest invalid"), false},
	}
	for _, tt := range tests {
		if got := isTransientDeployError(tt.err); got != tt.want {
			t.Errorf("isTransientDeployError(%v) = %v, want %v", tt.err, got, tt.want)
		}
	}
}

func TestActiveDeploymentConflictDetails(t *testing.T) {
	err := &apiError{Status: http.StatusConflict, Code: "ACTIVE_DEPLOYMENT", Message: "busy", Details: map[string]any{"buildId": "build_1", "status": "pending", "samePlan": true}}
	if !isActiveDeploymentConflict(err) {
		t.Fatal("expected active deployment conflict")
	}
	buildID, samePlan := activeDeploymentConflictDetails(fmt.Errorf("wrapped: %w", err))
	if buildID != "build_1" || !samePlan {
		t.Fatalf("got %q %v", buildID, samePlan)
	}
}

func TestDeployOutcomeError(t *testing.T) {
	var status deploySessionStatusResponse
	status.Build.Status = "success"
	if err := deployOutcomeError(status, "deployment b"); err != nil {
		t.Fatal(err)
	}
	status.Build.Status = "cancelled"
	if code := exitCodeForError(deployOutcomeError(status, "deployment b")); code != exitDeployFailed {
		t.Fatalf("cancelled exit code = %d", code)
	}
}

func TestUploadProgressLineETA(t *testing.T) {
	line := renderUploadProgressLine("Upload", 50<<20, 100<<20, 10*time.Second)
	if !strings.Contains(line, "ETA 10s") {
		t.Fatalf("expected ETA 10s in %q", line)
	}
	if line := renderUploadProgressLine("Upload", 1, 100, time.Second); !strings.Contains(line, "ETA --") {
		t.Fatalf("expected placeholder ETA early on, got %q", line)
	}
	if line := renderUploadProgressLine("Upload", 100, 100, 90*time.Second); !strings.Contains(line, "done in 1m30s") {
		t.Fatalf("expected completion time, got %q", line)
	}
}

func TestApplyDeployModeToDiffKeepsPortsInSmartMode(t *testing.T) {
	plan := deployDiffPlan{Warnings: []string{deployPortRemovalWarning + "TCP/8080"}, HasDestructive: true}
	smart := applyDeployModeToDiff(plan, "smart")
	if smart.HasDestructive || !strings.Contains(smart.Warnings[0], "smart mode keeps it") {
		t.Fatalf("smart diff = %+v", smart)
	}
	if replace := applyDeployModeToDiff(plan, "replace"); !replace.HasDestructive {
		t.Fatal("replace mode must stay destructive")
	}
	withEnv := applyDeployModeToDiff(deployDiffPlan{Warnings: []string{deployPortRemovalWarning + "TCP/80", "The current healthcheck will be removed"}}, "smart")
	if !withEnv.HasDestructive {
		t.Fatal("non-port warnings must stay destructive")
	}
}

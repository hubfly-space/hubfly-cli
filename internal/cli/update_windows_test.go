//go:build windows

package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Exercise replacement while Windows holds an executable image open.
func TestReplaceRunningExecutable(t *testing.T) {
	dir := t.TempDir()
	current := filepath.Join(dir, "hubfly.exe")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := copyFile(current, executable); err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(dir, "replacement.exe")
	if err := os.WriteFile(replacement, []byte("updated binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(current, "-test.run=^TestUpdateRunningProcessHelper$")
	cmd.Env = append(os.Environ(), "HUBFLY_TEST_UPDATE_BINARY="+replacement)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("running update failed: %v\n%s", err, output)
	}
	data, err := os.ReadFile(current)
	if err != nil || string(data) != "updated binary" {
		t.Fatalf("update did not replace executable: %q, %v", data, err)
	}
}

func TestUpdateRunningProcessHelper(t *testing.T) {
	replacement := os.Getenv("HUBFLY_TEST_UPDATE_BINARY")
	if replacement == "" {
		return
	}
	current, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := replaceExecutable(current, replacement); err != nil {
		t.Fatal(err)
	}
}

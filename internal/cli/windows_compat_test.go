package cli

import (
	"archive/zip"
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

func TestWindowsReleaseAssets(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		if got := expectedAssetName("windows", arch); got != "hubfly_windows_"+arch+".zip" {
			t.Fatalf("unexpected CLI asset: %s", got)
		}
		got, err := expectedBuilderAssetName("windows", arch)
		if err != nil || got != "hubfly-builder_windows_"+arch+".zip" {
			t.Fatalf("unexpected builder asset: %s, %v", got, err)
		}
	}
}

func TestDownloadWindowsArchive(t *testing.T) {
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	entry, err := zw.Create("release/hubfly.exe")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte("windows binary")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(archive.Bytes())
	}))
	defer server.Close()
	binary, err := downloadAndExtractBinary(server.URL + "/hubfly_windows_amd64.zip")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(filepath.Dir(binary))
	if runtime.GOOS == "windows" && filepath.Ext(binary) != ".exe" {
		t.Fatalf("downloaded executable is missing .exe: %s", binary)
	}
	data, err := os.ReadFile(binary)
	if err != nil || string(data) != "windows binary" {
		t.Fatalf("unexpected binary: %q, %v", data, err)
	}
}

func TestResizePolling(t *testing.T) {
	var authenticated atomic.Bool
	done := make(chan struct{})
	ticks := make(chan time.Time, 5)
	for range 5 {
		ticks <- time.Time{}
	}
	close(ticks)
	var sizesRead int
	readSize := func() (int, int, error) {
		sizesRead++
		switch sizesRead {
		case 1:
			return 0, 0, errors.New("no console")
		case 2:
			return 0, 24, nil
		case 3, 4:
			return 80, 24, nil
		default:
			return 120, 40, nil
		}
	}
	var sent [][2]int
	send := func(cols, rows int) error {
		sent = append(sent, [2]int{cols, rows})
		return nil
	}
	// An unauthenticated connection must not receive terminal messages.
	unauthTicks := make(chan time.Time, 1)
	unauthTicks <- time.Time{}
	close(unauthTicks)
	pollTerminalResize(&authenticated, done, unauthTicks, readSize, send)
	if sizesRead != 0 || len(sent) != 0 {
		t.Fatal("read or sent a size before authentication")
	}
	authenticated.Store(true)
	pollTerminalResize(&authenticated, done, ticks, readSize, send)
	if len(sent) != 2 || sent[0] != [2]int{80, 24} || sent[1] != [2]int{120, 40} {
		t.Fatalf("unexpected resize messages: %v", sent)
	}
	close(done)
	pollTerminalResize(&authenticated, done, make(chan time.Time), readSize, send)
}

func TestBuilderInspectionIntegration(t *testing.T) {
	builder := os.Getenv("HUBFLY_TEST_BUILDER")
	if builder == "" {
		t.Skip("set HUBFLY_TEST_BUILDER to test CLI/Builder interoperability")
	}
	root := filepath.Join(t.TempDir(), "project with spaces")
	app := filepath.Join(root, "apps", "web")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app, "index.html"), []byte("<h1>Windows</h1>\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := defaultDeployConfig(root)
	cfg.Build.WorkingDir = filepath.Join("apps", "web")
	config := deployConfigPath(root)
	if err := saveDeployConfig(config, cfg); err != nil {
		t.Fatal(err)
	}
	output, err := runBuilderInspect(builder, root, config)
	if err != nil {
		t.Fatal(err)
	}
	if output.BuildConfig.Runtime != "static" || output.BuildConfig.AppDir != "apps/web" || output.Dockerfile == "" {
		t.Fatalf("unexpected builder response: %+v", output)
	}
}

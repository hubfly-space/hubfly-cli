package cli

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/mod/semver"

	"hubfly-cli/internal/version"
)

type githubRelease struct {
	TagName string `json:"tag_name"`
	Name    string `json:"name"`
	Assets  []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

func showVersion() {
	fmt.Printf("hubfly version %s\n", version.Version)
	fmt.Printf("commit: %s\n", version.Commit)
	fmt.Printf("date:   %s\n", version.Date)
	fmt.Printf("os/arch: %s/%s\n", runtime.GOOS, runtime.GOARCH)
}

func updateFlow(checkOnly bool) error {
	rel, err := fetchLatestRelease()
	if err != nil {
		return fmt.Errorf("failed to fetch latest release: %w", err)
	}

	latest := normalizeVersion(rel.TagName)
	current := normalizeVersion(version.Version)

	if current == "" || current == "v0.0.0" || !semver.IsValid(current) {
		if checkOnly {
			fmt.Printf("Current version is %q (non-semver); latest is %s\n", version.Version, latest)
			return nil
		}
	} else {
		cmp := semver.Compare(current, latest)
		if cmp >= 0 {
			fmt.Printf("Already up to date (%s)\n", version.Version)
			return nil
		}
		if checkOnly {
			fmt.Printf("Update available: %s -> %s\n", version.Version, latest)
			return nil
		}
	}

	assetName := expectedAssetName(runtime.GOOS, runtime.GOARCH)
	assetURL, err := findAssetURL(rel, assetName)
	if err != nil {
		return err
	}

	if runtime.GOOS == "windows" {
		return fmt.Errorf("auto-update on Windows is not supported yet; download manually: %s", assetURL)
	}

	fmt.Printf("Updating hubfly: %s -> %s\n", version.Version, latest)
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to resolve current executable: %w", err)
	}
	exePath, err = filepath.EvalSymlinks(exePath)
	if err != nil {
		exePath = filepath.Clean(exePath)
	}

	newBinary, err := downloadAndExtractBinary(assetURL)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(newBinary) }()

	if err := replaceExecutable(exePath, newBinary); err != nil {
		return err
	}

	fmt.Println("Update successful. Re-run `hubfly version` to confirm.")
	return nil
}

func fetchLatestRelease() (githubRelease, error) {
	var rel githubRelease
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", version.RepoOwner, version.RepoName)

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return rel, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "hubfly-cli/"+version.Version)

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return rel, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return rel, fmt.Errorf("github API returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return rel, err
	}
	if strings.TrimSpace(rel.TagName) == "" {
		return rel, errors.New("latest release has no tag_name")
	}
	return rel, nil
}

func normalizeVersion(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	if !semver.IsValid(v) {
		return ""
	}
	return v
}

func expectedAssetName(goos, goarch string) string {
	ext := ".tar.gz"
	if goos == "windows" {
		ext = ".zip"
	}
	return fmt.Sprintf("hubfly_%s_%s%s", goos, goarch, ext)
}

func findAssetURL(rel githubRelease, name string) (string, error) {
	for _, a := range rel.Assets {
		if a.Name == name {
			return a.URL, nil
		}
	}
	return "", fmt.Errorf("release %s does not contain asset %q for %s/%s", rel.TagName, name, runtime.GOOS, runtime.GOARCH)
}

func downloadAndExtractBinary(assetURL string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, assetURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("User-Agent", "hubfly-cli/"+version.Version)

	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("download failed with %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	tmpDir, err := os.MkdirTemp("", "hubfly-update-*")
	if err != nil {
		return "", err
	}

	archivePath := filepath.Join(tmpDir, "archive")
	f, err := os.Create(archivePath)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}

	binaryPath := filepath.Join(tmpDir, "hubfly-new")
	assetName := expectedAssetName(runtime.GOOS, runtime.GOARCH)
	if strings.HasSuffix(assetName, ".zip") {
		if err := extractBinaryFromZip(archivePath, binaryPath); err != nil {
			return "", err
		}
	} else {
		if err := extractBinaryFromTarGz(archivePath, binaryPath); err != nil {
			return "", err
		}
	}
	if err := os.Chmod(binaryPath, 0o755); err != nil {
		return "", err
	}
	return binaryPath, nil
}

func extractBinaryFromTarGz(archivePath, outputPath string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	gz, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		base := filepath.Base(h.Name)
		if base == "hubfly" || base == "hubfly-cli" {
			out, err := os.Create(outputPath)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				_ = out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
			return nil
		}
	}
	return errors.New("binary not found in tar.gz archive")
}

func extractBinaryFromZip(archivePath, outputPath string) error {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer func() { _ = zr.Close() }()

	for _, f := range zr.File {
		base := filepath.Base(f.Name)
		if base != "hubfly.exe" && base != "hubfly" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.Create(outputPath)
		if err != nil {
			_ = rc.Close()
			return err
		}
		if _, err := io.Copy(out, rc); err != nil {
			_ = rc.Close()
			_ = out.Close()
			return err
		}
		_ = rc.Close()
		if err := out.Close(); err != nil {
			return err
		}
		return nil
	}
	return errors.New("binary not found in zip archive")
}

func replaceExecutable(currentPath, newBinaryPath string) error {
	dir := filepath.Dir(currentPath)
	stagedPath := filepath.Join(dir, ".hubfly.new")
	backupPath := filepath.Join(dir, ".hubfly.old")

	in, err := os.Open(newBinaryPath)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(stagedPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return fmt.Errorf("cannot write update to %s: %w", stagedPath, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}

	_ = os.Remove(backupPath)
	if err := os.Rename(currentPath, backupPath); err != nil {
		_ = os.Remove(stagedPath)
		return fmt.Errorf("cannot replace %s (try with proper permissions): %w", currentPath, err)
	}
	if err := os.Rename(stagedPath, currentPath); err != nil {
		_ = os.Rename(backupPath, currentPath)
		_ = os.Remove(stagedPath)
		return fmt.Errorf("failed to finalize update: %w", err)
	}
	_ = os.Remove(backupPath)
	return nil
}

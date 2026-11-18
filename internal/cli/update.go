package cli

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
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

	fmt.Printf("Updating hubfly: %s -> %s\n", version.Version, latest)
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to resolve current executable: %w", err)
	}
	exePath, err = filepath.EvalSymlinks(exePath)
	if err != nil {
		exePath = filepath.Clean(exePath)
	}

	checksum, err := fetchReleaseChecksum(rel, assetName)
	if err != nil {
		return err
	}

	newBinary, err := downloadAndExtractVerifiedBinary(assetURL, checksum, []string{"hubfly", "hubfly-cli", "hubfly.exe"})
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(filepath.Dir(newBinary)) }()

	backupPath, err := swapExecutable(exePath, newBinary)
	if err != nil {
		return err
	}
	if verifyErr := verifyInstalledExecutable(exePath); verifyErr != nil {
		if rollbackErr := rollbackExecutable(exePath, backupPath); rollbackErr != nil {
			return fmt.Errorf("update failed (%v) and rollback failed: %w; the previous binary is at %s", verifyErr, rollbackErr, backupPath)
		}
		return fmt.Errorf("update failed and was rolled back to %s: %w", version.Version, verifyErr)
	}
	// On Windows the running executable (now the backup) stays locked until
	// exit; the next update removes it.
	_ = os.Remove(backupPath)

	fmt.Println("Update successful. Re-run `hubfly version` to confirm.")
	return nil
}

func fetchLatestRelease() (githubRelease, error) {
	return fetchLatestReleaseForRepo(version.RepoOwner, version.RepoName)
}

func fetchLatestReleaseForRepo(owner, repo string) (githubRelease, error) {
	var rel githubRelease
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", owner, repo)

	ctx, cancel := RequestContext(20 * time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return rel, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "hubfly-cli/"+version.Version)

	resp, err := DoHTTP(req)
	if err != nil {
		return rel, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-RateLimit-Remaining") == "0" {
			return rel, errors.New("GitHub API rate limit reached; try again later")
		}
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
	return downloadAndExtractNamedBinary(
		assetURL,
		[]string{"hubfly", "hubfly-cli", "hubfly.exe"},
	)
}

func downloadAndExtractNamedBinary(assetURL string, binaryCandidates []string) (string, error) {
	return downloadAndExtractVerifiedBinary(assetURL, "", binaryCandidates)
}

// fetchReleaseChecksum reads the release's "<asset>.sha256" file
// ("<hex>  <name>") and returns the lowercase hex digest.
func fetchReleaseChecksum(rel githubRelease, assetName string) (string, error) {
	checksumURL, err := findAssetURL(rel, assetName+".sha256")
	if err != nil {
		return "", fmt.Errorf("release %s publishes no checksum for %s; refusing to install an unverified binary", rel.TagName, assetName)
	}
	ctx, cancel := RequestContext(30 * time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, checksumURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "hubfly-cli/"+version.Version)
	resp, err := DoHTTP(req)
	if err != nil {
		return "", fmt.Errorf("download checksum: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return "", fmt.Errorf("download checksum: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download checksum failed with %d", resp.StatusCode)
	}
	return parseChecksumFile(string(body))
}

func parseChecksumFile(content string) (string, error) {
	fields := strings.Fields(content)
	if len(fields) == 0 {
		return "", errors.New("checksum file is empty")
	}
	sum := strings.ToLower(fields[0])
	if len(sum) != 64 {
		return "", errors.New("checksum file is malformed")
	}
	if _, err := hex.DecodeString(sum); err != nil {
		return "", errors.New("checksum file is malformed")
	}
	return sum, nil
}

// downloadAndExtractVerifiedBinary downloads assetURL (retrying transient
// failures), checks its SHA-256 against expectedSHA256 when given, and
// extracts the first matching binary into a fresh temp dir.
func downloadAndExtractVerifiedBinary(assetURL, expectedSHA256 string, binaryCandidates []string) (string, error) {
	ctx, cancel := context.WithTimeout(RootContext(), RequestTimeout(10*time.Minute))
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, assetURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("User-Agent", "hubfly-cli/"+version.Version)

	resp, err := DoHTTP(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("download failed with %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	tmpDir, err := os.MkdirTemp("", "hubfly-update-*")
	if err != nil {
		return "", err
	}

	keepTemp := false
	defer func() {
		if !keepTemp {
			_ = os.RemoveAll(tmpDir)
		}
	}()

	archivePath := filepath.Join(tmpDir, "archive")
	f, err := os.Create(archivePath)
	if err != nil {
		return "", err
	}
	hasher := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, hasher), resp.Body); err != nil {
		_ = f.Close()
		return "", fmt.Errorf("download interrupted: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if expected := strings.ToLower(strings.TrimSpace(expectedSHA256)); expected != "" {
		if actual := hex.EncodeToString(hasher.Sum(nil)); actual != expected {
			return "", fmt.Errorf("checksum mismatch for downloaded release (expected %s, got %s); nothing was installed", expected, actual)
		}
	}

	outputName := "binary"
	if len(binaryCandidates) > 0 {
		outputName = binaryCandidates[0]
	}
	if runtime.GOOS == "windows" && !strings.HasSuffix(strings.ToLower(outputName), ".exe") {
		outputName += ".exe"
	}
	binaryPath := filepath.Join(tmpDir, outputName)

	switch {
	case strings.HasSuffix(strings.ToLower(assetURL), ".zip"):
		if err := extractBinaryFromZipByNames(archivePath, binaryPath, binaryCandidates...); err != nil {
			return "", err
		}
	case strings.HasSuffix(strings.ToLower(assetURL), ".tar.gz"),
		strings.HasSuffix(strings.ToLower(assetURL), ".tgz"):
		if err := extractBinaryFromTarGzByNames(archivePath, binaryPath, binaryCandidates...); err != nil {
			return "", err
		}
	default:
		if err := os.Rename(archivePath, binaryPath); err != nil {
			return "", err
		}
	}
	if err := os.Chmod(binaryPath, 0o755); err != nil {
		return "", err
	}
	keepTemp = true
	return binaryPath, nil
}

func extractBinaryFromTarGz(archivePath, outputPath string) error {
	return extractBinaryFromTarGzByNames(archivePath, outputPath, "hubfly", "hubfly-cli", "hubfly.exe")
}

func extractBinaryFromTarGzByNames(archivePath, outputPath string, binaryCandidates ...string) error {
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
	nameSet := make(map[string]struct{}, len(binaryCandidates))
	for _, name := range binaryCandidates {
		name = filepath.Base(strings.TrimSpace(name))
		if name == "" {
			continue
		}
		nameSet[name] = struct{}{}
	}
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
		if _, ok := nameSet[base]; ok {
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
	return extractBinaryFromZipByNames(archivePath, outputPath, "hubfly.exe", "hubfly", "hubfly-cli")
}

func extractBinaryFromZipByNames(archivePath, outputPath string, binaryCandidates ...string) error {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer func() { _ = zr.Close() }()

	nameSet := make(map[string]struct{}, len(binaryCandidates))
	for _, name := range binaryCandidates {
		name = filepath.Base(strings.TrimSpace(name))
		if name == "" {
			continue
		}
		nameSet[name] = struct{}{}
	}

	for _, f := range zr.File {
		base := filepath.Base(f.Name)
		if _, ok := nameSet[base]; !ok {
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
	backupPath, err := swapExecutable(currentPath, newBinaryPath)
	if err != nil {
		return err
	}
	_ = os.Remove(backupPath)
	return nil
}

// swapExecutable stages newBinaryPath next to currentPath, then swaps it in
// with two renames, keeping the old binary at the returned backup path. A
// failed swap puts the old binary back. Windows permits renaming the running
// executable but keeps it locked until exit.
func swapExecutable(currentPath, newBinaryPath string) (string, error) {
	dir := filepath.Dir(currentPath)
	stagedPath := filepath.Join(dir, ".hubfly.new")
	backupPath := filepath.Join(dir, ".hubfly.old")
	if runtime.GOOS == "windows" {
		stagedPath += ".exe"
		backupPath += ".exe"
	}

	in, err := os.Open(newBinaryPath)
	if err != nil {
		return "", err
	}
	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(stagedPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return "", fmt.Errorf("cannot write update to %s: %w", stagedPath, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(stagedPath)
		return "", err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		_ = os.Remove(stagedPath)
		return "", err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(stagedPath)
		return "", err
	}

	// A previous update's backup must be removable before replacing it.
	if err := os.Remove(backupPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		_ = os.Remove(stagedPath)
		return "", fmt.Errorf("cannot remove previous update backup: %w", err)
	}
	if err := os.Rename(currentPath, backupPath); err != nil {
		_ = os.Remove(stagedPath)
		return "", fmt.Errorf("cannot replace %s (try with proper permissions): %w", currentPath, err)
	}
	if err := os.Rename(stagedPath, currentPath); err != nil {
		_ = os.Rename(backupPath, currentPath)
		_ = os.Remove(stagedPath)
		return "", fmt.Errorf("failed to finalize update: %w", err)
	}
	return backupPath, nil
}

// rollbackExecutable restores backupPath over currentPath.
func rollbackExecutable(currentPath, backupPath string) error {
	failedPath := filepath.Join(filepath.Dir(currentPath), ".hubfly.failed")
	if runtime.GOOS == "windows" {
		failedPath += ".exe"
	}
	_ = os.Remove(failedPath)
	if err := os.Rename(currentPath, failedPath); err != nil {
		return err
	}
	if err := os.Rename(backupPath, currentPath); err != nil {
		_ = os.Rename(failedPath, currentPath)
		return err
	}
	_ = os.Remove(failedPath)
	return nil
}

// verifyInstalledExecutable runs `<path> version` to prove the new binary
// starts on this machine before the old one is discarded.
func verifyInstalledExecutable(path string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, path, "version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("new binary failed to start: %v: %s", err, strings.TrimSpace(string(output)))
	}
	if !strings.Contains(string(output), "hubfly version") {
		return fmt.Errorf("new binary printed unexpected version output: %s", strings.TrimSpace(string(output)))
	}
	return nil
}

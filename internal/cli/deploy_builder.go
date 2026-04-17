package cli

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"hubfly-cli/internal/version"
)

const builderReleaseCacheTTL = 10 * time.Minute

type builderInstallRequest struct {
	RequestedVersion string
}

type cachedGitHubRelease struct {
	FetchedAt string        `json:"fetchedAt,omitempty"`
	Release   githubRelease `json:"release"`
}

type builderReleaseCache struct {
	Latest *cachedGitHubRelease           `json:"latest,omitempty"`
	Tags   map[string]cachedGitHubRelease `json:"tags,omitempty"`
}

func fetchBuilderReleaseCached(requestedVersion string) (githubRelease, bool, error) {
	tag := normalizeVersion(requestedVersion)
	cache, _ := loadBuilderReleaseCache()
	now := time.Now().UTC()

	if tag == "" {
		if cache.Latest != nil && isCachedReleaseFresh(*cache.Latest, now) {
			return cache.Latest.Release, true, nil
		}
		release, err := fetchGitHubReleaseForRepo("", builderRepoOwner, builderRepoName)
		if err != nil {
			return githubRelease{}, false, err
		}
		cache.Latest = &cachedGitHubRelease{
			FetchedAt: now.Format(time.RFC3339),
			Release:   release,
		}
		_ = saveBuilderReleaseCache(cache)
		return release, false, nil
	}

	if cached, ok := cache.Tags[tag]; ok && isCachedReleaseFresh(cached, now) {
		return cached.Release, true, nil
	}

	release, err := fetchGitHubReleaseForRepo(tag, builderRepoOwner, builderRepoName)
	if err != nil {
		return githubRelease{}, false, err
	}
	if cache.Tags == nil {
		cache.Tags = make(map[string]cachedGitHubRelease)
	}
	cache.Tags[tag] = cachedGitHubRelease{
		FetchedAt: now.Format(time.RFC3339),
		Release:   release,
	}
	_ = saveBuilderReleaseCache(cache)
	return release, false, nil
}

func isCachedReleaseFresh(entry cachedGitHubRelease, now time.Time) bool {
	fetchedAt, err := time.Parse(time.RFC3339, strings.TrimSpace(entry.FetchedAt))
	if err != nil {
		return false
	}
	return now.Sub(fetchedAt) <= builderReleaseCacheTTL
}

func loadBuilderReleaseCache() (builderReleaseCache, error) {
	var cache builderReleaseCache
	data, err := os.ReadFile(builderReleaseCachePath())
	if err != nil {
		return cache, err
	}
	if err := json.Unmarshal(data, &cache); err != nil {
		return builderReleaseCache{}, err
	}
	return cache, nil
}

func saveBuilderReleaseCache(cache builderReleaseCache) error {
	if err := os.MkdirAll(filepath.Dir(builderReleaseCachePath()), 0o755); err != nil {
		return err
	}
	payload, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	return os.WriteFile(builderReleaseCachePath(), payload, 0o644)
}

func fetchGitHubReleaseForRepo(tag, owner, repo string) (githubRelease, error) {
	if normalizeVersion(tag) == "" {
		return fetchLatestReleaseForRepo(owner, repo)
	}
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/tags/%s", owner, repo, normalizeVersion(tag))
	return fetchGitHubReleaseAtURL(url)
}

func fetchGitHubReleaseAtURL(url string) (githubRelease, error) {
	var rel githubRelease
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
		return rel, fmt.Errorf("release metadata from github is missing tag_name")
	}
	return rel, nil
}

func expectedBuilderChecksumAssetName(assetName string) string {
	return assetName + ".sha256"
}

func installBuilderFromRelease(targetPath string, release githubRelease, assetName string) (string, error) {
	assetURL, err := findBuilderAssetURL(release, assetName)
	if err != nil {
		return "", err
	}
	checksumURL, err := findAssetURL(release, expectedBuilderChecksumAssetName(assetName))
	if err != nil {
		return "", fmt.Errorf("release %s is missing checksum asset %q", release.TagName, expectedBuilderChecksumAssetName(assetName))
	}

	archivePath, cleanup, err := downloadAssetToTemp(assetURL, filepath.Ext(assetName))
	if err != nil {
		return "", err
	}
	defer cleanup()

	expectedChecksum, err := downloadReleaseChecksum(checksumURL)
	if err != nil {
		return "", err
	}
	if err := verifyFileSHA256(archivePath, expectedChecksum); err != nil {
		return "", err
	}

	outputName := "hubfly-builder"
	if runtime.GOOS == "windows" {
		outputName = "hubfly-builder.exe"
	}
	tmpBinary := filepath.Join(filepath.Dir(archivePath), outputName)
	switch {
	case strings.HasSuffix(strings.ToLower(assetName), ".zip"):
		err = extractBinaryFromZipByNames(archivePath, tmpBinary, builderBinaryCandidates()...)
	default:
		err = extractBinaryFromTarGzByNames(archivePath, tmpBinary, builderBinaryCandidates()...)
	}
	if err != nil {
		return "", err
	}

	if err := copyFile(targetPath, tmpBinary); err != nil {
		return "", err
	}
	if err := os.Chmod(targetPath, 0o755); err != nil && runtime.GOOS != "windows" {
		return "", err
	}

	version, err := probeBuilderVersion(targetPath)
	if err != nil {
		return "", err
	}
	return version, nil
}

func downloadAssetToTemp(assetURL, suffix string) (string, func(), error) {
	req, err := http.NewRequest(http.MethodGet, assetURL, nil)
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("User-Agent", "hubfly-cli/"+version.Version)

	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", nil, fmt.Errorf("download failed with %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	tmpDir, err := os.MkdirTemp("", "hubfly-builder-download-*")
	if err != nil {
		return "", nil, err
	}

	archivePath := filepath.Join(tmpDir, "archive"+suffix)
	out, err := os.Create(archivePath)
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		return "", nil, err
	}
	if _, err := io.Copy(out, resp.Body); err != nil {
		_ = out.Close()
		_ = os.RemoveAll(tmpDir)
		return "", nil, err
	}
	if err := out.Close(); err != nil {
		_ = os.RemoveAll(tmpDir)
		return "", nil, err
	}

	return archivePath, func() { _ = os.RemoveAll(tmpDir) }, nil
}

func downloadReleaseChecksum(assetURL string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, assetURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("User-Agent", "hubfly-cli/"+version.Version)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("checksum download failed with %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		return strings.ToLower(strings.TrimSpace(fields[0])), nil
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("checksum file was empty")
}

func verifyFileSHA256(path, expectedChecksum string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	expected := strings.ToLower(strings.TrimSpace(expectedChecksum))
	if actual != expected {
		return fmt.Errorf("checksum mismatch for hubfly-builder download: expected %s, got %s", expected, actual)
	}
	return nil
}

func probeBuilderVersion(targetPath string) (string, error) {
	if _, err := os.Stat(targetPath); err != nil {
		return "", err
	}
	versionOutput, err := commandOutput(exec.Command(targetPath, "version"))
	if err != nil {
		return "", err
	}
	versionOutput = strings.TrimSpace(versionOutput)
	if versionOutput == "" {
		return "", fmt.Errorf("hubfly-builder at %s did not return a version", targetPath)
	}
	return versionOutput, nil
}

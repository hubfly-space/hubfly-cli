package cli

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	defaultUploadChunkSize = 8 * 1024 * 1024
	maxUploadChunkRetries  = 3
)

type builderUploadSessionState struct {
	BuildID        string `json:"buildId"`
	ChunkSize      int64  `json:"chunkSize"`
	TotalSize      int64  `json:"totalSize"`
	UploadedChunks []int  `json:"uploadedChunks"`
}

func exportCompressedImageArchive(localTag string) (string, int64, func(), error) {
	tmpDir, err := os.MkdirTemp("", "hubfly-upload-archive-*")
	if err != nil {
		return "", 0, nil, err
	}

	archivePath := filepath.Join(tmpDir, "image.tar.gz")
	out, err := os.Create(archivePath)
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		return "", 0, nil, err
	}

	cmd := exec.Command("docker", "save", localTag)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = out.Close()
		_ = os.RemoveAll(tmpDir)
		return "", 0, nil, err
	}

	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		_ = out.Close()
		_ = os.RemoveAll(tmpDir)
		return "", 0, nil, err
	}

	gz := gzip.NewWriter(out)
	_, copyErr := io.Copy(gz, stdout)
	closeErr := gz.Close()
	fileErr := out.Close()
	waitErr := cmd.Wait()
	if copyErr != nil {
		_ = os.RemoveAll(tmpDir)
		return "", 0, nil, copyErr
	}
	if closeErr != nil {
		_ = os.RemoveAll(tmpDir)
		return "", 0, nil, closeErr
	}
	if fileErr != nil {
		_ = os.RemoveAll(tmpDir)
		return "", 0, nil, fileErr
	}
	if waitErr != nil {
		_ = os.RemoveAll(tmpDir)
		return "", 0, nil, fmt.Errorf("docker save failed: %s", strings.TrimSpace(stderr.String()))
	}

	info, err := os.Stat(archivePath)
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		return "", 0, nil, err
	}
	return archivePath, info.Size(), func() { _ = os.RemoveAll(tmpDir) }, nil
}

func uploadChunkedImageArchive(archivePath string, archiveSize int64, session deploySessionResponse, sourceImage string) error {
	sessionsURL := builderUploadSessionsURL(session.Upload.URL)
	client := &http.Client{Timeout: 5 * time.Minute}

	state, err := createOrResumeBuilderUploadSession(client, sessionsURL, session, sourceImage, archiveSize, defaultUploadChunkSize)
	if err != nil {
		return err
	}
	chunkSize := state.ChunkSize
	if chunkSize <= 0 {
		chunkSize = defaultUploadChunkSize
	}

	uploadedSet := make(map[int]struct{}, len(state.UploadedChunks))
	for _, index := range state.UploadedChunks {
		if index >= 0 {
			uploadedSet[index] = struct{}{}
		}
	}

	progress := newUploadProgress("Upload progress", archiveSize)
	progress.Start()
	defer progress.Finish()
	progress.SetCurrent(uploadedBytesForChunks(archiveSize, chunkSize, uploadedSet))

	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	totalChunks := totalUploadChunks(archiveSize, chunkSize)
	for index := 0; index < totalChunks; index++ {
		if _, ok := uploadedSet[index]; ok {
			continue
		}
		chunkOffset := int64(index) * chunkSize
		chunkLength := chunkLengthAt(archiveSize, chunkSize, index)
		if err := uploadBuilderChunkWithRetry(client, sessionsURL, session, sourceImage, file, index, chunkOffset, chunkLength); err != nil {
			return err
		}
		uploadedSet[index] = struct{}{}
		progress.SetCurrent(uploadedBytesForChunks(archiveSize, chunkSize, uploadedSet))
	}

	return completeBuilderUploadSession(client, sessionsURL, session, sourceImage)
}

func builderUploadSessionsURL(uploadURL string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(uploadURL), "/")
	const legacyPath = "/api/v1/image-upload"
	if strings.HasSuffix(trimmed, legacyPath) {
		return strings.TrimSuffix(trimmed, legacyPath) + legacyPath + "/sessions"
	}
	return trimmed + "/sessions"
}

func createOrResumeBuilderUploadSession(
	client *http.Client,
	sessionsURL string,
	session deploySessionResponse,
	sourceImage string,
	totalSize int64,
	chunkSize int64,
) (builderUploadSessionState, error) {
	body := map[string]any{
		"totalSize":       totalSize,
		"chunkSize":       chunkSize,
		"contentEncoding": "docker-save+gzip",
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return builderUploadSessionState{}, err
	}

	req, err := http.NewRequest(http.MethodPost, sessionsURL, strings.NewReader(string(payload)))
	if err != nil {
		return builderUploadSessionState{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	attachBuilderUploadHeaders(req, session, sourceImage)

	resp, err := client.Do(req)
	if err != nil {
		return builderUploadSessionState{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return builderUploadSessionState{}, fmt.Errorf("failed to start upload session: %s", strings.TrimSpace(string(body)))
	}

	var state builderUploadSessionState
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		return builderUploadSessionState{}, err
	}
	sort.Ints(state.UploadedChunks)
	return state, nil
}

func uploadBuilderChunkWithRetry(
	client *http.Client,
	sessionsURL string,
	session deploySessionResponse,
	sourceImage string,
	file *os.File,
	index int,
	offset int64,
	length int64,
) error {
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return err
	}

	for attempt := 1; attempt <= maxUploadChunkRetries; attempt++ {
		reader := io.NewSectionReader(file, offset, length)
		req, err := http.NewRequest(
			http.MethodPut,
			fmt.Sprintf("%s/%s/chunks/%d", sessionsURL, session.BuildID, index),
			io.NopCloser(reader),
		)
		if err != nil {
			return err
		}
		req.ContentLength = length
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("X-Hubfly-Chunk-Offset", fmt.Sprintf("%d", offset))
		req.Header.Set("X-Hubfly-Chunk-Length", fmt.Sprintf("%d", length))
		attachBuilderUploadHeaders(req, session, sourceImage)

		resp, err := client.Do(req)
		if err == nil && resp != nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
			_ = resp.Body.Close()
			return nil
		}

		var statusErr error
		if resp != nil {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			statusErr = fmt.Errorf("upload chunk %d failed with %d: %s", index, resp.StatusCode, strings.TrimSpace(string(body)))
		}
		if attempt == maxUploadChunkRetries {
			if err != nil {
				return err
			}
			return statusErr
		}
		time.Sleep(time.Duration(attempt) * time.Second)
	}

	return nil
}

func completeBuilderUploadSession(
	client *http.Client,
	sessionsURL string,
	session deploySessionResponse,
	sourceImage string,
) error {
	req, err := http.NewRequest(
		http.MethodPost,
		fmt.Sprintf("%s/%s/complete", sessionsURL, session.BuildID),
		nil,
	)
	if err != nil {
		return err
	}
	attachBuilderUploadHeaders(req, session, sourceImage)

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to finalize upload: %s", strings.TrimSpace(string(body)))
	}
	return nil
}

func attachBuilderUploadHeaders(req *http.Request, session deploySessionResponse, sourceImage string) {
	req.Header.Set("X-Hubfly-Build-Id", session.BuildID)
	req.Header.Set("X-Hubfly-Upload-Token", session.Upload.Token)
	req.Header.Set("X-Hubfly-Source-Image", sourceImage)
}

func totalUploadChunks(totalSize, chunkSize int64) int {
	if totalSize <= 0 || chunkSize <= 0 {
		return 0
	}
	chunks := totalSize / chunkSize
	if totalSize%chunkSize != 0 {
		chunks++
	}
	return int(chunks)
}

func chunkLengthAt(totalSize, chunkSize int64, index int) int64 {
	if totalSize <= 0 || chunkSize <= 0 {
		return 0
	}
	offset := int64(index) * chunkSize
	remaining := totalSize - offset
	if remaining <= 0 {
		return 0
	}
	if remaining < chunkSize {
		return remaining
	}
	return chunkSize
}

func uploadedBytesForChunks(totalSize, chunkSize int64, uploaded map[int]struct{}) int64 {
	var total int64
	for index := range uploaded {
		total += chunkLengthAt(totalSize, chunkSize, index)
	}
	if total > totalSize {
		return totalSize
	}
	return total
}

package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

// Object transfers for `hubfly storage put/get`. Bytes never pass through
// the Dashboard: it returns a short-lived signed URL on the region's stream
// host and the CLI streams the file to or from it.

// storageMaxPutBytes is the regional stream host's limit for one signed PUT
// (hubfly-storage internal/stream/handler.go). Larger files need an S3
// client with multipart upload.
const storageMaxPutBytes = int64(20_000_000_000)

// storageProgressMinBytes is the size from which put/get show a progress line.
const storageProgressMinBytes = int64(8 << 20)

// storageTransferClient has no overall timeout: large transfers may run for
// a long time. Connection setup and response headers are still bounded.
var storageTransferClient = &http.Client{Transport: &http.Transport{
	Proxy:                 http.ProxyFromEnvironment,
	TLSHandshakeTimeout:   30 * time.Second,
	ResponseHeaderTimeout: 10 * time.Minute,
	IdleConnTimeout:       90 * time.Second,
}}

// storageProgress renders a transfer progress line on stderr. It redraws in
// place on a terminal and prints only the final line otherwise.
type storageProgress struct {
	label   string
	total   int64
	current atomic.Int64
	started time.Time
	tty     bool
	done    chan struct{}
	stopped chan struct{}
}

// newStorageProgress returns nil (no output) for small or quiet transfers.
func newStorageProgress(label string, total int64, quiet bool) *storageProgress {
	if quiet || total < storageProgressMinBytes {
		return nil
	}
	p := &storageProgress{label: label, total: total, started: time.Now(), tty: rtStderrIsTerminal(), done: make(chan struct{}), stopped: make(chan struct{})}
	go func() {
		defer close(p.stopped)
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-p.done:
				return
			case <-ticker.C:
				if p.tty {
					_, _ = fmt.Fprint(rtErr, "\r"+padProgressLine(p.line()))
				}
			}
		}
	}()
	return p
}

func (p *storageProgress) line() string {
	return renderUploadProgressLine(p.label, p.current.Load(), p.total, time.Since(p.started))
}

func (p *storageProgress) add(n int) {
	if p != nil {
		p.current.Add(int64(n))
	}
}

// finish stops the redraw loop; ok prints the final line.
func (p *storageProgress) finish(ok bool) {
	if p == nil {
		return
	}
	close(p.done)
	<-p.stopped
	switch {
	case p.tty && ok:
		_, _ = fmt.Fprint(rtErr, "\r"+padProgressLine(p.line())+"\n")
	case p.tty:
		_, _ = fmt.Fprint(rtErr, "\r"+padProgressLine("")+"\r")
	case ok:
		_, _ = fmt.Fprintln(rtErr, p.line())
	}
}

type storageCountingReader struct {
	reader   io.Reader
	progress *storageProgress
}

func (r storageCountingReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.progress.add(n)
	return n, err
}

type storageCountingWriter struct {
	writer   io.Writer
	progress *storageProgress
}

func (w storageCountingWriter) Write(p []byte) (int, error) {
	n, err := w.writer.Write(p)
	w.progress.add(n)
	return n, err
}

// storageTooLargeError explains the single-PUT limit.
func storageTooLargeError(localPath string, size int64) error {
	return fmt.Errorf("%s is %s; uploads through the CLI are limited to %s per file. Use an S3 client with multipart upload and a key from: hubfly storage keys create <name>",
		localPath, formatBytes(size), formatBytes(storageMaxPutBytes))
}

// storageContentType picks the Content-Type of an upload: the --content-type
// override, the file extension, then content sniffing.
func storageContentType(localPath, override string) (string, error) {
	if override = strings.TrimSpace(override); override != "" {
		if _, _, err := mime.ParseMediaType(override); err != nil {
			return "", fmt.Errorf("invalid --content-type %q", override)
		}
		return override, nil
	}
	if byExt := mime.TypeByExtension(strings.ToLower(filepath.Ext(localPath))); byExt != "" {
		return byExt, nil
	}
	file, err := os.Open(localPath)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	head := make([]byte, 512)
	n, err := io.ReadFull(file, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", err
	}
	if n == 0 {
		return "application/octet-stream", nil
	}
	return http.DetectContentType(head[:n]), nil
}

// storageUploadItem is one local file and the object key it is stored under.
type storageUploadItem struct {
	LocalPath string
	Key       string
	Size      int64
}

// storageUploadKey derives the object key for a single-file put: an empty
// key or one ending in "/" gets the file's base name appended.
func storageUploadKey(localPath, key string) string {
	if key == "" || strings.HasSuffix(key, "/") {
		return key + filepath.Base(localPath)
	}
	return key
}

// storageWalkUploads lists the regular files under root (symlinks and special
// files are skipped) with keys of prefix + their slash-separated relative
// path, as `aws s3 cp --recursive` does.
func storageWalkUploads(root, prefix string) ([]storageUploadItem, []string, error) {
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	items := []storageUploadItem{}
	skipped := []string{}
	err := filepath.WalkDir(root, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			skipped = append(skipped, current)
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, current)
		if err != nil {
			return err
		}
		items = append(items, storageUploadItem{LocalPath: current, Key: prefix + filepath.ToSlash(rel), Size: info.Size()})
		return nil
	})
	sort.Slice(items, func(i, j int) bool { return items[i].Key < items[j].Key })
	return items, skipped, err
}

// storageUploadFile streams one file to a signed upload URL. contentType must
// be the one the upload session was created with.
func storageUploadFile(uploadURL, localPath, contentType string, size int64, quiet bool) error {
	file, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	progress := newStorageProgress("Uploading "+filepath.Base(localPath), size, quiet)
	request, err := http.NewRequest(http.MethodPut, uploadURL, storageCountingReader{reader: file, progress: progress})
	if err != nil {
		progress.finish(false)
		return err
	}
	request.ContentLength = size
	request.Header.Set("Content-Type", contentType)
	if size == 0 {
		request.Body = http.NoBody
	}
	debugf("HTTP request: PUT %s (signed upload URL, %d bytes)", storageRedactURL(uploadURL), size)
	response, err := storageTransferClient.Do(request)
	if err != nil {
		progress.finish(false)
		return fmt.Errorf("upload failed: %w", storageRedactError(err, uploadURL))
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		progress.finish(false)
		if response.StatusCode == http.StatusRequestEntityTooLarge {
			return storageTooLargeError(localPath, size)
		}
		return fmt.Errorf("upload failed: %s", storageTransferStatus(response))
	}
	progress.finish(true)
	return nil
}

// storageDownloadTo streams a signed download URL into out and returns the
// number of bytes written.
func storageDownloadTo(downloadURL string, out io.Writer, label string, quiet bool) (int64, error) {
	debugf("HTTP request: GET %s (signed download URL)", storageRedactURL(downloadURL))
	response, err := storageTransferClient.Get(downloadURL)
	if err != nil {
		return 0, fmt.Errorf("download failed: %w", storageRedactError(err, downloadURL))
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return 0, fmt.Errorf("download failed: %s", storageTransferStatus(response))
	}
	progress := newStorageProgress(label, response.ContentLength, quiet)
	written, err := io.Copy(storageCountingWriter{writer: out, progress: progress}, response.Body)
	progress.finish(err == nil)
	if err != nil {
		return written, fmt.Errorf("download failed: %w", err)
	}
	if response.ContentLength >= 0 && written != response.ContentLength {
		return written, fmt.Errorf("download failed: received %d of %d bytes", written, response.ContentLength)
	}
	return written, nil
}

// storageDownloadFile downloads into dest through a temporary file in the
// same directory, so an interrupted download never leaves a partial file
// under the final name.
func storageDownloadFile(downloadURL, dest string, force, quiet bool) (int64, error) {
	if !force {
		if _, err := os.Lstat(dest); err == nil {
			return 0, fmt.Errorf("%s already exists; pass --force to overwrite it", dest)
		}
	}
	temp, err := os.CreateTemp(filepath.Dir(dest), "."+filepath.Base(dest)+".*.part")
	if err != nil {
		return 0, err
	}
	written, err := storageDownloadTo(downloadURL, temp, "Downloading "+filepath.Base(dest), quiet)
	closeErr := temp.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(temp.Name(), dest)
	}
	if err != nil {
		_ = os.Remove(temp.Name())
		return written, err
	}
	return written, nil
}

// storageDownloadName is the default local file name for an object key.
func storageDownloadName(key string) string {
	name := path.Base(strings.TrimSuffix(key, "/"))
	if name == "." || name == "/" || name == "" {
		return "download"
	}
	return name
}

func storageTransferStatus(response *http.Response) string {
	body, _ := io.ReadAll(io.LimitReader(response.Body, 2048))
	text := strings.TrimSpace(string(body))
	if text == "" {
		return response.Status
	}
	return fmt.Sprintf("%s: %s", response.Status, text)
}

// storageRedactURL drops the signed token from a transfer URL for debug
// output.
func storageRedactURL(raw string) string {
	for _, marker := range []string{"/upload/", "/signed/"} {
		if index := strings.Index(raw, marker); index >= 0 {
			return raw[:index+len(marker)] + "<token>"
		}
	}
	if index := strings.Index(raw, "?"); index >= 0 {
		return raw[:index] + "?<redacted>"
	}
	return raw
}

// storageRedactError keeps signed URLs out of transport error messages.
func storageRedactError(err error, raw string) error {
	text := err.Error()
	if !strings.Contains(text, raw) {
		return err
	}
	return errors.New(strings.ReplaceAll(text, raw, storageRedactURL(raw)))
}

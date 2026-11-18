package cli

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
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

// storageDefaultJobs and storageMaxJobs bound concurrent uploads of
// `storage put --recursive`.
const (
	storageDefaultJobs = 4
	storageMaxJobs     = 16
)

// storageProgressMinBytes is the size from which put/get show a progress line.
const storageProgressMinBytes = int64(8 << 20)

// storageProgressPlainEvery is how often a non-terminal stderr gets a
// progress line.
var storageProgressPlainEvery = 15 * time.Second

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
		lastLine := time.Now()
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-p.done:
				return
			case <-ticker.C:
				if p.tty {
					_, _ = fmt.Fprint(rtErr, "\r"+padProgressLine(p.line()))
				} else if time.Since(lastLine) >= storageProgressPlainEvery {
					// Logs and CI get a plain line now and then instead of
					// nothing until the end.
					lastLine = time.Now()
					_, _ = fmt.Fprintln(rtErr, p.line())
				}
			}
		}
	}()
	return p
}

func (p *storageProgress) line() string {
	return renderUploadProgressLine(p.label, p.current.Load(), p.total, time.Since(p.started))
}

// set moves the counter (a retried transfer restarts from its offset).
func (p *storageProgress) set(n int64) {
	if p != nil {
		p.current.Store(n)
	}
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

// ---------------------------------------------------------------------------
// Resilient transfers (shared by storage put/get and db import/export)
// ---------------------------------------------------------------------------

// transferMaxAttempts bounds consecutive failed attempts of one transfer
// (network errors, 5xx, 429, expired links). An attempt that moved bytes
// resets the count, so a long download over a flaky link still finishes.
var transferMaxAttempts = 6

// transferRetryDelay is the wait before retry number attempt (1-based).
var transferRetryDelay = func(attempt int, header http.Header) time.Duration {
	delay, _ := RetryDelay(attempt, header)
	if delay > time.Minute {
		delay = time.Minute
	}
	return delay
}

// transferLink returns the signed URL of a transfer; refresh=true asks for a
// new one because the previous one expired or was refused.
type transferLink func(refresh bool) (string, error)

// transferHTTPError is a non-2xx answer from the storage host.
type transferHTTPError struct {
	Status int
	Text   string
	Header http.Header
}

func (e *transferHTTPError) Error() string { return e.Text }

// transferLocalError is a local file failure (disk full, permissions): never
// retried.
type transferLocalError struct{ err error }

func (e transferLocalError) Error() string { return e.err.Error() }
func (e transferLocalError) Unwrap() error { return e.err }

// transferVerifyError: the bytes on the other side do not match ours.
type transferVerifyError struct{ text string }

func (e transferVerifyError) Error() string { return e.text }

// errTransferRestarted marks an attempt that emptied the destination to
// start over (retried like a network error).
var errTransferRestarted = errors.New("starting over")

// errTransferInterrupted is returned when Ctrl-C stops a transfer.
var errTransferInterrupted = errors.New("interrupted")

// transferClassify says whether err is worth another attempt and whether
// that attempt needs a fresh signed link.
func transferClassify(err error) (retry, refresh bool) {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, errTransferInterrupted) {
		return false, false
	}
	var local transferLocalError
	if errors.As(err, &local) {
		return false, false
	}
	var httpErr *transferHTTPError
	if errors.As(err, &httpErr) {
		switch {
		case httpErr.Status == http.StatusUnauthorized || httpErr.Status == http.StatusForbidden:
			return true, true
		case httpErr.Status == http.StatusRequestTimeout || httpErr.Status == http.StatusTooManyRequests || httpErr.Status >= 500:
			return true, false
		}
		return false, false
	}
	// Network and read errors, and verification mismatches.
	return true, false
}

// transferNotice prints a retry note on stderr without garbling a progress
// line drawn in place.
func transferNotice(format string, args ...any) {
	message := fmt.Sprintf(format, args...)
	if rtStderrIsTerminal() {
		_, _ = fmt.Fprint(rtErr, "\r"+padProgressLine("")+"\r")
	}
	_, _ = fmt.Fprintln(rtErr, message)
}

// transferRetryWait sleeps before the next attempt; false means Ctrl-C.
func transferRetryWait(ctx context.Context, what string, err error, attempt int, header http.Header) bool {
	delay := transferRetryDelay(attempt, header)
	transferNotice("%s failed (%v); retrying in %s (attempt %d of %d)", what, err, delay.Round(100*time.Millisecond), attempt+1, transferMaxAttempts)
	return SleepContext(ctx, delay) == nil
}

func transferErrHeader(err error) http.Header {
	var httpErr *transferHTTPError
	if errors.As(err, &httpErr) {
		return httpErr.Header
	}
	return nil
}

func isHexMD5(value string) bool {
	if len(value) != 32 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// transferETag is a response ETag without quotes or weak prefix.
func transferETag(header http.Header) string {
	value := strings.TrimSpace(header.Get("ETag"))
	value = strings.TrimPrefix(value, "W/")
	return strings.ToLower(strings.Trim(value, `"`))
}

// transferUpload PUTs localPath to the link, retrying failed attempts (a new
// link when the old one is refused) and verifying what the server stored:
// the size it reports and, for S3 links, the ETag against the local MD5.
// Signed upload URLs take one PUT, so a failed attempt restarts the file.
func transferUpload(ctx context.Context, link transferLink, localPath, contentType string, size int64, label string, quiet bool) error {
	progress := newStorageProgress(label, size, quiet)
	refresh := false
	var lastErr error
	for attempt := 1; ; attempt++ {
		uploadURL, err := link(refresh)
		if err != nil {
			progress.finish(false)
			return err
		}
		progress.set(0)
		lastErr = transferUploadOnce(ctx, uploadURL, localPath, contentType, size, progress)
		if lastErr == nil {
			progress.finish(true)
			return nil
		}
		if ctx.Err() != nil {
			progress.finish(false)
			return errTransferInterrupted
		}
		retry, needNew := transferClassify(lastErr)
		if !retry || attempt >= transferMaxAttempts {
			break
		}
		refresh = needNew
		if !transferRetryWait(ctx, "upload of "+filepath.Base(localPath), lastErr, attempt, transferErrHeader(lastErr)) {
			progress.finish(false)
			return errTransferInterrupted
		}
	}
	progress.finish(false)
	return lastErr
}

func transferUploadOnce(ctx context.Context, uploadURL, localPath, contentType string, size int64, progress *storageProgress) error {
	file, err := os.Open(localPath)
	if err != nil {
		return transferLocalError{err}
	}
	defer func() { _ = file.Close() }()
	hasher := md5.New()
	var body io.Reader = storageCountingReader{reader: io.TeeReader(file, hasher), progress: progress}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL, body)
	if err != nil {
		return transferLocalError{err}
	}
	request.ContentLength = size
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	if size == 0 {
		request.Body = http.NoBody
	}
	debugf("HTTP request: PUT %s (signed upload URL, %d bytes)", storageRedactURL(uploadURL), size)
	response, err := storageTransferClient.Do(request)
	if err != nil {
		return storageRedactError(err, uploadURL)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &transferHTTPError{Status: response.StatusCode, Text: storageTransferStatus(response), Header: response.Header}
	}
	sum := hex.EncodeToString(hasher.Sum(nil))
	if etag := transferETag(response.Header); isHexMD5(etag) && size > 0 && etag != sum {
		return transferVerifyError{fmt.Sprintf("checksum mismatch: the server stored MD5 %s, the local file is %s", etag, sum)}
	}
	reply, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	var stored struct {
		Size *int64 `json:"size"`
	}
	if json.Unmarshal(reply, &stored) == nil && stored.Size != nil && *stored.Size != size {
		return transferVerifyError{fmt.Sprintf("size mismatch: the server stored %d of %d bytes", *stored.Size, size)}
	}
	return nil
}

// transferDownloadState carries a download across attempts (and, for files,
// across runs).
type transferDownloadState struct {
	Offset int64  // bytes already written
	Total  int64  // object size, -1 while unknown
	ETag   string // validator seen first ("" = none)
	// Restart empties the destination so the download can start over (nil
	// when it cannot, e.g. stdout already received bytes).
	Restart func() error
	// Saved is called when Total/ETag are learned, to persist them.
	Saved func()
	// hash covers bytes 0..Offset when non-nil (checked against an MD5 ETag).
	hash hash.Hash
}

type transferWriter struct {
	writer io.Writer
	state  *transferDownloadState
	prog   *storageProgress
}

func (w transferWriter) Write(p []byte) (int, error) {
	n, err := w.writer.Write(p)
	if n > 0 {
		if w.state.hash != nil {
			w.state.hash.Write(p[:n])
		}
		w.state.Offset += int64(n)
		w.prog.add(n)
	}
	if err != nil {
		return n, transferLocalError{err}
	}
	return n, nil
}

// transferParseContentRange reads "bytes a-b/N" (or "bytes */N").
func transferParseContentRange(value string) (start, total int64, ok bool) {
	value = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(value), "bytes"))
	span, size, found := strings.Cut(value, "/")
	if !found {
		return 0, 0, false
	}
	total = -1
	if size != "*" {
		parsed, err := strconv.ParseInt(size, 10, 64)
		if err != nil {
			return 0, 0, false
		}
		total = parsed
	}
	if span == "*" {
		return -1, total, true
	}
	first, _, found := strings.Cut(span, "-")
	if !found {
		return 0, 0, false
	}
	parsed, err := strconv.ParseInt(first, 10, 64)
	if err != nil {
		return 0, 0, false
	}
	return parsed, total, true
}

// transferDownload streams the link into w from state.Offset, resuming with
// Range requests after a failure. It refuses to splice two versions of an
// object: a changed size or ETag restarts the download (or fails when it
// cannot). With an MD5 ETag and state.hash set, the result is checksummed.
func transferDownload(ctx context.Context, link transferLink, w io.Writer, state *transferDownloadState, label string, quiet bool) error {
	var progress *storageProgress
	refresh := false
	failures := 0
	restart := func(reason string) error {
		if state.Restart == nil {
			return transferLocalError{fmt.Errorf("stopped after %s: %s, and output already written cannot be rewound; run the command again", formatBytes(state.Offset), reason)}
		}
		transferNotice("%s; starting the download over", reason)
		if err := state.Restart(); err != nil {
			return transferLocalError{err}
		}
		state.Offset, state.Total, state.ETag = 0, -1, ""
		if state.hash != nil {
			state.hash.Reset()
		}
		progress.set(0)
		return nil
	}
	defer func() { progress.finish(false) }()
	for {
		if ctx.Err() != nil {
			return errTransferInterrupted
		}
		before := state.Offset
		done, err := transferDownloadOnce(ctx, link, refresh, w, state, &progress, label, quiet, restart)
		if !done && err == nil {
			err = errTransferRestarted
		}
		if done && err == nil {
			if state.Total >= 0 && state.Offset != state.Total {
				err = fmt.Errorf("received %d of %d bytes", state.Offset, state.Total)
			} else {
				if state.hash != nil && isHexMD5(state.ETag) {
					if sum := hex.EncodeToString(state.hash.Sum(nil)); sum != state.ETag {
						return transferVerifyError{fmt.Sprintf("checksum mismatch: downloaded MD5 %s, the server has %s", sum, state.ETag)}
					}
				}
				progress.finish(true)
				progress = nil
				return nil
			}
		}
		if ctx.Err() != nil {
			return errTransferInterrupted
		}
		retry, needNew := transferClassify(err)
		if !retry {
			return err
		}
		if state.Offset > before {
			failures = 0
		}
		failures++
		if failures >= transferMaxAttempts {
			return err
		}
		refresh = needNew
		if !transferRetryWait(ctx, "download", err, failures, transferErrHeader(err)) {
			return errTransferInterrupted
		}
	}
}

func transferDownloadOnce(ctx context.Context, link transferLink, refresh bool, w io.Writer, state *transferDownloadState, progress **storageProgress, label string, quiet bool, restart func(string) error) (bool, error) {
	downloadURL, err := link(refresh)
	if err != nil {
		return false, transferLocalError{err}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return false, transferLocalError{err}
	}
	if state.Offset > 0 {
		request.Header.Set("Range", fmt.Sprintf("bytes=%d-", state.Offset))
	}
	debugf("HTTP request: GET %s (signed download URL, from byte %d)", storageRedactURL(downloadURL), state.Offset)
	response, err := storageTransferClient.Do(request)
	if err != nil {
		return false, storageRedactError(err, downloadURL)
	}
	defer func() { _ = response.Body.Close() }()
	etag := transferETag(response.Header)
	switch {
	case response.StatusCode == http.StatusRequestedRangeNotSatisfiable && state.Offset > 0:
		_, total, ok := transferParseContentRange(response.Header.Get("Content-Range"))
		if ok && total == state.Offset && (state.Total < 0 || state.Total == total) {
			state.Total = total
			return true, nil
		}
		return false, restart("the partial download no longer matches the object")
	case response.StatusCode == http.StatusOK && state.Offset > 0:
		if err := restart("the server did not resume from byte " + strconv.FormatInt(state.Offset, 10)); err != nil {
			return false, err
		}
		state.Total = response.ContentLength
	case response.StatusCode == http.StatusPartialContent:
		start, total, ok := transferParseContentRange(response.Header.Get("Content-Range"))
		if !ok || start != state.Offset {
			return false, restart("the server answered an unexpected range")
		}
		if state.Total >= 0 && total >= 0 && total != state.Total || state.ETag != "" && etag != "" && etag != state.ETag {
			return false, restart("the object changed since the download started")
		}
		if state.Total < 0 {
			state.Total = total
		}
	case response.StatusCode < 200 || response.StatusCode >= 300:
		return false, &transferHTTPError{Status: response.StatusCode, Text: storageTransferStatus(response), Header: response.Header}
	default:
		state.Total = response.ContentLength
	}
	if state.ETag == "" {
		state.ETag = etag
	}
	if state.Saved != nil {
		state.Saved()
	}
	if *progress == nil {
		*progress = newStorageProgress(label, state.Total, quiet)
	}
	(*progress).set(state.Offset)
	_, err = io.Copy(transferWriter{writer: w, state: state, prog: *progress}, response.Body)
	if err != nil {
		return false, err
	}
	return true, nil
}

// storageUploadFile uploads one file through fresh upload sessions from
// link (see transferUpload).
func storageUploadFile(ctx context.Context, link transferLink, localPath, contentType string, size int64, quiet bool) error {
	err := transferUpload(ctx, link, localPath, contentType, size, "Uploading "+filepath.Base(localPath), quiet)
	if err == nil {
		return nil
	}
	if errors.Is(err, errTransferInterrupted) {
		return fmt.Errorf("upload of %s interrupted; nothing partial is kept, run the command again to upload it", localPath)
	}
	var httpErr *transferHTTPError
	if errors.As(err, &httpErr) && httpErr.Status == http.StatusRequestEntityTooLarge {
		return storageTooLargeError(localPath, size)
	}
	var apiErr *apiError
	if errors.As(err, &apiErr) {
		return err
	}
	return fmt.Errorf("upload failed: %w", err)
}

// storageDownloadTo streams a download into out (stdout); retries resume
// from the bytes already written.
func storageDownloadTo(ctx context.Context, link transferLink, out io.Writer, label string, quiet bool) (int64, error) {
	state := &transferDownloadState{Total: -1, hash: md5.New()}
	err := transferDownload(ctx, link, out, state, label, quiet)
	if errors.Is(err, errTransferInterrupted) {
		return state.Offset, fmt.Errorf("download interrupted after %s", formatBytes(state.Offset))
	}
	if err != nil {
		return state.Offset, fmt.Errorf("download failed: %w", err)
	}
	return state.Offset, nil
}

// transferPartialMeta identifies the object a partial file belongs to.
type transferPartialMeta struct {
	Total int64  `json:"total"`
	ETag  string `json:"etag,omitempty"`
}

// transferPartialPath is the resumable partial file of dest (hidden, next
// to it, so the final rename stays on one file system).
func transferPartialPath(dest string) string {
	return filepath.Join(filepath.Dir(dest), "."+filepath.Base(dest)+".hubfly-part")
}

// transferDownloadFile downloads into dest through a partial file that
// survives failures and Ctrl-C: running the same command again resumes it.
// The partial is renamed over dest only once complete and verified.
func transferDownloadFile(ctx context.Context, link transferLink, dest, label string, quiet bool, mode os.FileMode) (int64, error) {
	partial := transferPartialPath(dest)
	metaPath := partial + ".json"
	state := &transferDownloadState{Total: -1, hash: md5.New()}
	file, err := os.OpenFile(partial, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return 0, err
	}
	closed := false
	defer func() {
		if !closed {
			_ = file.Close()
		}
	}()
	var meta transferPartialMeta
	if info, statErr := file.Stat(); statErr == nil && info.Size() > 0 {
		if raw, readErr := os.ReadFile(metaPath); readErr == nil && json.Unmarshal(raw, &meta) == nil && (meta.Total < 0 || info.Size() <= meta.Total) {
			// Re-hash what is there so the checksum covers the whole file.
			if _, err := io.Copy(state.hash, file); err != nil {
				return 0, err
			}
			state.Offset, state.Total, state.ETag = info.Size(), meta.Total, meta.ETag
			transferNotice("Resuming %s from %s", filepath.Base(dest), formatBytes(state.Offset))
		} else if err := file.Truncate(0); err != nil {
			return 0, err
		}
	}
	if _, err := file.Seek(state.Offset, io.SeekStart); err != nil {
		return 0, err
	}
	state.Restart = func() error {
		if err := file.Truncate(0); err != nil {
			return err
		}
		_, err := file.Seek(0, io.SeekStart)
		return err
	}
	saved := transferPartialMeta{Total: -2}
	state.Saved = func() {
		current := transferPartialMeta{Total: state.Total, ETag: state.ETag}
		if current != saved {
			saved = current
			if raw, err := json.Marshal(current); err == nil {
				_ = os.WriteFile(metaPath, raw, 0o600)
			}
		}
	}
	err = transferDownload(ctx, link, file, state, label, quiet)
	if errors.Is(err, errTransferInterrupted) {
		return state.Offset, fmt.Errorf("download interrupted after %s; the partial file %s is kept and running the same command again resumes it", formatBytes(state.Offset), partial)
	}
	var verifyErr transferVerifyError
	if errors.As(err, &verifyErr) {
		_ = file.Close()
		closed = true
		_ = os.Remove(partial)
		_ = os.Remove(metaPath)
		return state.Offset, fmt.Errorf("download failed: %w; the partial file was removed, run the command again", err)
	}
	if err != nil {
		return state.Offset, fmt.Errorf("download failed: %w\nThe partial file %s is kept; running the same command again resumes it", err, partial)
	}
	if err := file.Sync(); err != nil {
		return state.Offset, err
	}
	closed = true
	if err := file.Close(); err != nil {
		return state.Offset, err
	}
	if mode != 0 {
		_ = os.Chmod(partial, mode)
	}
	if err := os.Rename(partial, dest); err != nil {
		return state.Offset, err
	}
	_ = os.Remove(metaPath)
	return state.Offset, nil
}

// storageDownloadFile downloads an object into dest (resumable).
func storageDownloadFile(ctx context.Context, link transferLink, dest string, force, quiet bool) (int64, error) {
	if !force {
		if _, err := os.Lstat(dest); err == nil {
			return 0, fmt.Errorf("%s already exists; pass --force to overwrite it", dest)
		}
	}
	return transferDownloadFile(ctx, link, dest, "Downloading "+filepath.Base(dest), quiet, 0)
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

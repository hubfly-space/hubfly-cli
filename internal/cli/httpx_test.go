package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newCountingServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, call int32)) (*httptest.Server, *int32) {
	t.Helper()
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler(w, r, atomic.AddInt32(&calls, 1))
	}))
	t.Cleanup(server.Close)
	return server, &calls
}

func TestHTTPRetryGetOn503ThenSucceeds(t *testing.T) {
	server, calls := newCountingServer(t, func(w http.ResponseWriter, _ *http.Request, call int32) {
		if call < 3 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": map[string]string{"id": "p1"}})
	})
	var out struct {
		ID string `json:"id"`
	}
	if err := doJSONRequest(http.MethodGet, server.URL+"/x", "tok", nil, &out); err != nil {
		t.Fatalf("doJSONRequest: %v", err)
	}
	if out.ID != "p1" || atomic.LoadInt32(calls) != 3 {
		t.Fatalf("got id=%q after %d calls", out.ID, atomic.LoadInt32(calls))
	}
}

func TestHTTPRetryNeverRetriesPlainPostOr4xx(t *testing.T) {
	server, calls := newCountingServer(t, func(w http.ResponseWriter, r *http.Request, _ int32) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":{"code":"BUSY","message":"operation in progress"}}`))
	})
	if err := doJSONRequest(http.MethodPost, server.URL+"/deploy", "tok", map[string]any{"a": 1}, nil); err == nil {
		t.Fatal("expected error")
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("POST without Idempotency-Key retried: %d calls", got)
	}
	err := doJSONRequest(http.MethodGet, server.URL+"/x", "tok", nil, nil)
	var apiErr *apiError
	if !errors.As(err, &apiErr) || apiErr.Status != 409 || apiErr.Code != "BUSY" {
		t.Fatalf("unexpected error %v", err)
	}
	if got := atomic.LoadInt32(calls); got != 2 {
		t.Fatalf("409 was retried: %d calls", got)
	}
}

func TestHTTPRetryPostWithIdempotencyKeyReusesKey(t *testing.T) {
	var mu sync.Mutex
	keys := []string{}
	bodies := []string{}
	server, _ := newCountingServer(t, func(w http.ResponseWriter, r *http.Request, call int32) {
		buf := make([]byte, 64)
		n, _ := r.Body.Read(buf)
		mu.Lock()
		keys = append(keys, r.Header.Get(IdempotencyKeyHeader))
		bodies = append(bodies, string(buf[:n]))
		mu.Unlock()
		if call == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"data":{}}`))
	})
	if err := doJSONRequestIdempotent(http.MethodPost, server.URL+"/create", "tok", map[string]any{"name": "a"}, &map[string]any{}, "key-1"); err != nil {
		t.Fatalf("idempotent POST: %v", err)
	}
	if len(keys) != 2 || keys[0] != "key-1" || keys[1] != "key-1" || bodies[0] != bodies[1] || bodies[1] == "" {
		t.Fatalf("keys=%v bodies=%v", keys, bodies)
	}
}

func TestHTTPRetryConnectErrorRetriedForPost(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()
	start := time.Now()
	err = doJSONRequest(http.MethodPost, "http://"+addr+"/x", "", map[string]any{}, nil)
	var reqErr *requestError
	if !errors.As(err, &reqErr) || reqErr.Attempts < 2 {
		t.Fatalf("expected retried connect error, got %v", err)
	}
	if !strings.Contains(err.Error(), "could not reach") || ErrorHint(err) == "" {
		t.Fatalf("unhelpful message %q / hint %q", err.Error(), ErrorHint(err))
	}
	if time.Since(start) > 20*time.Second {
		t.Fatalf("retries took too long: %s", time.Since(start))
	}
}

func TestHTTPTimeoutAndCancellation(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(release); server.Close() })

	err := DoJSON(http.MethodGet, server.URL, "", nil, nil, RequestOptions{Timeout: 100 * time.Millisecond})
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout, got %v", err)
	}
	if code := exitCodeForError(err); code != 6 {
		t.Fatalf("timeout exit code = %d", code)
	}

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	err = DoJSON(http.MethodGet, server.URL, "", nil, nil, RequestOptions{Context: ctx})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

func TestHTTPRequestTimeoutOverride(t *testing.T) {
	loadHTTPSettingsFromEnv()
	httpSettingsMu.Lock()
	saved := timeoutOverride
	httpSettingsMu.Unlock()
	t.Cleanup(func() { setTimeoutOverride(saved) })

	setTimeoutOverride(0)
	if got := RequestTimeout(0); got != DefaultRequestTimeout {
		t.Fatalf("default = %s", got)
	}
	setTimeoutOverride(10 * time.Second)
	if got := RequestTimeout(0); got != 10*time.Second {
		t.Fatalf("override = %s", got)
	}
	if got := RequestTimeout(5 * time.Minute); got != 5*time.Minute {
		t.Fatalf("override shortened a long built-in limit: %s", got)
	}
	rest, err := configureGlobalFlags([]string{"--timeout", "90s", "db", "query", "--timeout", "10s"})
	if err != nil || RequestTimeout(0) != 90*time.Second || len(rest) != 4 || rest[0] != "db" {
		t.Fatalf("rest=%v err=%v timeout=%s", rest, err, RequestTimeout(0))
	}
	if _, err := configureGlobalFlags([]string{"--timeout=abc", "projects"}); err == nil {
		t.Fatal("invalid --timeout accepted")
	}
}

func TestHTTPIsRetryableAndHelpers(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{&apiError{Status: 429}, true},
		{&apiError{Status: 502}, true},
		{&apiError{Status: 503}, true},
		{&apiError{Status: 504}, true},
		{&apiError{Status: 500}, false},
		{&apiError{Status: 404}, false},
		{&apiError{Status: 401}, false},
		{context.Canceled, false},
		{context.DeadlineExceeded, false},
		{&net.OpError{Op: "read", Err: errors.New("connection reset by peer")}, true},
		{errors.New("boom"), false},
	}
	for _, c := range cases {
		if got := IsRetryable(c.err); got != c.want {
			t.Errorf("IsRetryable(%v) = %v, want %v", c.err, got, c.want)
		}
	}
	key := NewIdempotencyKey()
	if len(key) != 36 || key == NewIdempotencyKey() {
		t.Fatalf("bad idempotency key %q", key)
	}
	req, _ := http.NewRequest(http.MethodPost, "http://example.invalid", nil)
	if IsIdempotentRequest(req) {
		t.Fatal("plain POST reported idempotent")
	}
	if !IsIdempotentRequest(WithIdempotencyKey(req)) {
		t.Fatal("POST with Idempotency-Key not idempotent")
	}
	if d, ok := RetryDelay(1, http.Header{"Retry-After": {"7"}}); !ok || d != 7*time.Second {
		t.Fatalf("Retry-After seconds: %s %v", d, ok)
	}
	if _, ok := RetryDelay(1, http.Header{"Retry-After": {"3600"}}); ok {
		t.Fatal("excessive Retry-After accepted")
	}
	for attempt := 1; attempt <= 6; attempt++ {
		d, ok := RetryDelay(attempt, nil)
		if !ok || d <= 0 || d > retryMaxDelay {
			t.Fatalf("backoff attempt %d = %s", attempt, d)
		}
	}
}

func TestHTTPErrorMessagesAndJSONShape(t *testing.T) {
	t.Setenv("HUBFLY_TOKEN", "")
	hints := map[int]string{401: "hubfly login", 402: "balance", 403: "permission", 404: "Not found", 409: "conflicts", 503: "req_123"}
	for status, want := range hints {
		err := &apiError{Status: status, Message: "x", RequestID: "req_123"}
		if hint := ErrorHint(err); !strings.Contains(hint, want) {
			t.Errorf("status %d hint %q lacks %q", status, hint, want)
		}
	}
	payload := ErrorJSON(&apiError{Status: 402, Code: "INSUFFICIENT_BALANCE", Message: "Insufficient balance", RequestID: "req_9"}, 5)
	var decoded ErrorPayload
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.OK || decoded.Error.Kind != "payment" || decoded.Error.Status != 402 || decoded.Error.RequestID != "req_9" || decoded.Error.Code != "INSUFFICIENT_BALANCE" || decoded.Error.ExitCode != 5 {
		t.Fatalf("unexpected JSON error: %s", payload)
	}

	server, _ := newCountingServer(t, func(w http.ResponseWriter, _ *http.Request, _ int32) {
		w.Header().Set(RequestIDHeader, "req_hdr")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("<html><body>oops</body></html>"))
	})
	err := doJSONRequest(http.MethodGet, server.URL, "", nil, nil)
	var apiErr *apiError
	if !errors.As(err, &apiErr) || apiErr.RequestID != "req_hdr" || strings.Contains(apiErr.Message, "<html") {
		t.Fatalf("5xx error = %#v", err)
	}
}

func TestHTTPWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "config.json")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			payload, _ := json.Marshal(map[string]int{"writer": i})
			if err := WriteFileAtomic(path, payload, 0o600); err != nil {
				t.Errorf("WriteFileAtomic: %v", err)
			}
		}(i)
	}
	wg.Wait()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]int
	if err := json.Unmarshal(content, &decoded); err != nil {
		t.Fatalf("config corrupted by concurrent writes: %q", content)
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(path)
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %o", info.Mode().Perm())
		}
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("temp files left behind: %v", entries)
	}
}

func TestUpdateChecksumParsingAndVerification(t *testing.T) {
	sum, err := parseChecksumFile(strings.Repeat("AB", 32) + "  hubfly_linux_amd64.tar.gz\n")
	if err != nil || sum != strings.Repeat("ab", 32) {
		t.Fatalf("parse: %q %v", sum, err)
	}
	if _, err := parseChecksumFile("nothex  file"); err == nil {
		t.Fatal("malformed checksum accepted")
	}
	server, _ := newCountingServer(t, func(w http.ResponseWriter, _ *http.Request, _ int32) {
		_, _ = w.Write([]byte("binary-content"))
	})
	if _, err := downloadAndExtractVerifiedBinary(server.URL+"/hubfly", strings.Repeat("0", 64), []string{"hubfly"}); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected checksum mismatch, got %v", err)
	}
	path, err := downloadAndExtractVerifiedBinary(server.URL+"/hubfly", "", []string{"hubfly"})
	if err != nil {
		t.Fatal(err)
	}
	_ = os.RemoveAll(filepath.Dir(path))
}

func TestUpdateSwapAndRollback(t *testing.T) {
	dir := t.TempDir()
	current := filepath.Join(dir, "hubfly")
	replacement := filepath.Join(dir, "replacement")
	if err := os.WriteFile(current, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(replacement, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	backup, err := swapExecutable(current, replacement)
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(current); string(data) != "new" {
		t.Fatalf("swap left %q", data)
	}
	if err := rollbackExecutable(current, backup); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(current); string(data) != "old" {
		t.Fatalf("rollback left %q", data)
	}
	if err := verifyInstalledExecutable(current); err == nil {
		t.Fatal("a non-runnable binary passed verification")
	}
}

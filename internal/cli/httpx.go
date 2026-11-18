package cli

// Shared HTTP layer for every HubFly API call: per-request timeouts, retries
// with jittered exponential backoff for idempotent requests, Ctrl-C
// cancellation through one root context, proxy support and human error
// messages (with a --json error shape for scripts).

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	mrand "math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"hubfly-cli/internal/version"
)

const (
	// DefaultRequestTimeout bounds one API request attempt. Container
	// snapshots may run a regional Hubnet inspection, so it stays generous.
	DefaultRequestTimeout = 45 * time.Second
	// IdempotencyKeyHeader marks a POST/PATCH as safe to retry.
	IdempotencyKeyHeader = "Idempotency-Key"
	// RequestIDHeader carries one id per logical request (kept across retries);
	// the engine echoes it as meta.requestId.
	RequestIDHeader = "X-Request-Id"

	defaultMaxAttempts = 3
	retryBaseDelay     = 400 * time.Millisecond
	retryMaxDelay      = 8 * time.Second
	maxRetryAfter      = 30 * time.Second
	forceQuitGrace     = 5 * time.Second
)

var (
	httpSettingsMu   sync.RWMutex
	httpSettingsOnce sync.Once
	timeoutOverride  time.Duration
	maxAttempts      = defaultMaxAttempts
	jsonErrorOutput  bool
	rootCtx          = context.Background()
	interrupted      bool
)

var sharedTransport = &http.Transport{
	Proxy: http.ProxyFromEnvironment,
	DialContext: (&net.Dialer{
		Timeout:   15 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext,
	ForceAttemptHTTP2:     true,
	MaxIdleConns:          32,
	MaxIdleConnsPerHost:   8,
	IdleConnTimeout:       90 * time.Second,
	TLSHandshakeTimeout:   15 * time.Second,
	ExpectContinueTimeout: time.Second,
}

var sharedHTTPClient = &http.Client{Transport: sharedTransport}

// HTTPClient returns the shared client (keep-alive pool, HTTP(S)_PROXY /
// NO_PROXY support). It sets no client-wide timeout: bound each request with
// a context (see RequestContext) so long downloads and streams still work.
func HTTPClient() *http.Client { return sharedHTTPClient }

func loadHTTPSettingsFromEnv() {
	httpSettingsOnce.Do(func() {
		httpSettingsMu.Lock()
		defer httpSettingsMu.Unlock()
		if raw := strings.TrimSpace(os.Getenv("HUBFLY_TIMEOUT")); raw != "" {
			if d, err := parseTimeoutValue(raw); err == nil {
				timeoutOverride = d
			} else {
				fmt.Fprintf(os.Stderr, "Warning: ignoring HUBFLY_TIMEOUT=%q: %v\n", raw, err)
			}
		}
		if raw := strings.TrimSpace(os.Getenv("HUBFLY_MAX_RETRIES")); raw != "" {
			if n, err := strconv.Atoi(raw); err == nil && n >= 0 && n <= 10 {
				maxAttempts = n + 1
			}
		}
		if isTruthy(os.Getenv("HUBFLY_JSON_ERRORS")) {
			jsonErrorOutput = true
		}
	})
}

func isTruthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// parseTimeoutValue accepts a Go duration ("90s", "2m") or plain seconds.
func parseTimeoutValue(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if seconds, err := strconv.ParseFloat(raw, 64); err == nil {
		if seconds <= 0 {
			return 0, errors.New("timeout must be positive")
		}
		return time.Duration(seconds * float64(time.Second)), nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid timeout %q (use e.g. 30s or 2m)", raw)
	}
	if d <= 0 {
		return 0, errors.New("timeout must be positive")
	}
	return d, nil
}

// RequestTimeout returns the timeout for one request attempt. def is the
// call's built-in timeout (<= 0 means DefaultRequestTimeout). A --timeout /
// HUBFLY_TIMEOUT override replaces the default and raises longer built-in
// limits only when it is longer.
func RequestTimeout(def time.Duration) time.Duration {
	loadHTTPSettingsFromEnv()
	httpSettingsMu.RLock()
	override := timeoutOverride
	httpSettingsMu.RUnlock()
	if def <= 0 {
		def = DefaultRequestTimeout
	}
	if override <= 0 {
		return def
	}
	if def > DefaultRequestTimeout && def > override {
		return def
	}
	return override
}

func setTimeoutOverride(d time.Duration) {
	loadHTTPSettingsFromEnv()
	httpSettingsMu.Lock()
	timeoutOverride = d
	httpSettingsMu.Unlock()
}

func retryAttempts() int {
	loadHTTPSettingsFromEnv()
	httpSettingsMu.RLock()
	defer httpSettingsMu.RUnlock()
	if maxAttempts < 1 {
		return 1
	}
	return maxAttempts
}

// RootContext is cancelled on Ctrl-C/SIGTERM once Main installed the handler
// (context.Background() in tests). Derive every request and wait from it.
func RootContext() context.Context {
	httpSettingsMu.RLock()
	defer httpSettingsMu.RUnlock()
	return rootCtx
}

// SetRootContext replaces the root context (tests, embedding).
func SetRootContext(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	httpSettingsMu.Lock()
	rootCtx = ctx
	httpSettingsMu.Unlock()
}

// RequestContext derives a context bounded by RequestTimeout(def) from the
// root context, so Ctrl-C cancels it too.
func RequestContext(def time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(RootContext(), RequestTimeout(def))
}

// SleepContext waits for d or until ctx is done (returns ctx.Err()).
func SleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// installInterruptHandler cancels the root context on the first Ctrl-C (or
// SIGTERM) and force-quits with 130 on the second one or after a grace period.
func installInterruptHandler() func() {
	ctx, cancel := context.WithCancel(context.Background())
	SetRootContext(ctx)
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		select {
		case <-done:
			return
		case <-signals:
		}
		httpSettingsMu.Lock()
		interrupted = true
		httpSettingsMu.Unlock()
		cancel()
		grace := time.NewTimer(forceQuitGrace)
		defer grace.Stop()
		select {
		case <-done:
		case <-signals:
			fmt.Fprintln(os.Stderr, "\nForce quit.")
			os.Exit(130)
		case <-grace.C:
			fmt.Fprintln(os.Stderr, "\nInterrupted.")
			os.Exit(130)
		}
	}()
	return func() {
		signal.Stop(signals)
		close(done)
		cancel()
	}
}

func wasInterrupted() bool {
	httpSettingsMu.RLock()
	defer httpSettingsMu.RUnlock()
	return interrupted
}

// NewIdempotencyKey returns a random UUIDv4 for the Idempotency-Key header.
func NewIdempotencyKey() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("hf-%d-%d", time.Now().UnixNano(), mrand.Int64())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

func newRequestID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("cli_%d", time.Now().UnixNano())
	}
	return "cli_" + hex.EncodeToString(b[:])
}

// WithIdempotencyKey sets a fresh Idempotency-Key on req unless one is set,
// which makes a POST/PATCH retryable by DoHTTP. Only use it on endpoints that
// dedupe on the key (or are naturally idempotent).
func WithIdempotencyKey(req *http.Request) *http.Request {
	if req != nil && strings.TrimSpace(req.Header.Get(IdempotencyKeyHeader)) == "" {
		req.Header.Set(IdempotencyKeyHeader, NewIdempotencyKey())
	}
	return req
}

// IsIdempotentMethod reports methods that are safe to repeat.
func IsIdempotentMethod(method string) bool {
	switch strings.ToUpper(method) {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodPut, http.MethodDelete:
		return true
	}
	return false
}

// IsIdempotentRequest: GET/HEAD/OPTIONS/PUT/DELETE, or any request with an
// Idempotency-Key header.
func IsIdempotentRequest(req *http.Request) bool {
	if req == nil {
		return false
	}
	return IsIdempotentMethod(req.Method) || strings.TrimSpace(req.Header.Get(IdempotencyKeyHeader)) != ""
}

// IsRetryable reports errors worth retrying for an idempotent request:
// transient network failures (not timeouts or cancellation) and API errors
// with status 429, 502, 503 or 504. Other 4xx/5xx are never retried.
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var apiErr *apiError
	if errors.As(err, &apiErr) {
		return isRetryableStatus(apiErr.Status)
	}
	var statusErr *httpStatusError
	if errors.As(err, &statusErr) {
		return isRetryableStatus(statusErr.Status)
	}
	return isTransientNetworkError(err)
}

func isRetryableStatus(status int) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

func isTransientNetworkError(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return false
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNABORTED) || errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ENETUNREACH) || errors.Is(err, syscall.EHOSTUNREACH) {
		return true
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return dnsErr.IsTemporary || dnsErr.IsTimeout || !dnsErr.IsNotFound
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		msg := strings.ToLower(urlErr.Err.Error())
		return strings.Contains(msg, "connection reset") || strings.Contains(msg, "broken pipe") ||
			strings.Contains(msg, "server closed idle connection") || strings.Contains(msg, "eof")
	}
	return false
}

// isConnectError: the request never reached the server (dial/DNS/proxy
// connect failed), so it is safe to retry whatever the method.
func isConnectError(err error) bool {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return !dnsErr.IsNotFound || dnsErr.IsTemporary
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && (opErr.Op == "dial" || opErr.Op == "proxyconnect") {
		var netErr net.Error
		return !(errors.As(err, &netErr) && netErr.Timeout())
	}
	return false
}

func shouldRetry(idempotent bool, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || RootContext().Err() != nil {
		return false
	}
	if idempotent {
		return IsRetryable(err)
	}
	return isConnectError(err)
}

// RetryDelay is the wait before retry number attempt (1-based): full-jitter
// exponential backoff, or the server's Retry-After when given. ok=false means
// Retry-After asks for longer than the CLI is willing to wait.
func RetryDelay(attempt int, header http.Header) (time.Duration, bool) {
	if header != nil {
		if raw := strings.TrimSpace(header.Get("Retry-After")); raw != "" {
			if d, parsed := parseRetryAfter(raw); parsed {
				if d > maxRetryAfter {
					return d, false
				}
				return d, true
			}
		}
	}
	if attempt < 1 {
		attempt = 1
	}
	d := retryBaseDelay << (attempt - 1)
	if d > retryMaxDelay || d <= 0 {
		d = retryMaxDelay
	}
	half := d / 2
	return half + time.Duration(mrand.Int64N(int64(half)+1)), true
}

func parseRetryAfter(raw string) (time.Duration, bool) {
	if seconds, err := strconv.Atoi(raw); err == nil {
		if seconds < 0 {
			seconds = 0
		}
		return time.Duration(seconds) * time.Second, true
	}
	if at, err := http.ParseTime(raw); err == nil {
		d := time.Until(at)
		if d < 0 {
			d = 0
		}
		return d, true
	}
	return 0, false
}

// httpStatusError is returned by DoHTTP callers' helpers for raw non-2xx
// responses that are not API envelopes.
type httpStatusError struct {
	Status int
	Body   string
}

func (e *httpStatusError) Error() string {
	if e.Body != "" {
		return fmt.Sprintf("HTTP %d: %s", e.Status, e.Body)
	}
	return fmt.Sprintf("HTTP %d", e.Status)
}

// requestError wraps a transport failure (no HTTP response) with the
// request's context; Unwrap keeps errors.Is(context.DeadlineExceeded) and
// net.Error checks working.
type requestError struct {
	Method    string
	URL       string
	RequestID string
	Timeout   time.Duration
	Attempts  int
	Err       error
}

func (e *requestError) Error() string {
	host := e.URL
	if parsed, err := url.Parse(e.URL); err == nil && parsed.Host != "" {
		host = parsed.Host
	}
	var netErr net.Error
	switch {
	case errors.Is(e.Err, context.Canceled):
		return "request cancelled"
	case errors.Is(e.Err, context.DeadlineExceeded) || (errors.As(e.Err, &netErr) && netErr.Timeout()):
		return fmt.Sprintf("request to %s timed out after %s (raise it with --timeout or HUBFLY_TIMEOUT)", host, e.Timeout.Round(time.Second))
	}
	cause := e.Err.Error()
	var urlErr *url.Error
	if errors.As(e.Err, &urlErr) {
		cause = urlErr.Err.Error()
	}
	suffix := ""
	if e.Attempts > 1 {
		suffix = fmt.Sprintf(" after %d attempts", e.Attempts)
	}
	return fmt.Sprintf("could not reach %s%s: %s", host, suffix, cause)
}

func (e *requestError) Unwrap() error { return e.Err }

// DoHTTP sends req with the shared client and retries it on transient
// failures (network errors, 429 honoring Retry-After, 502/503/504) when the
// request is idempotent (see IsIdempotentRequest); a connect failure is
// retried for any method. The request's own context bounds the whole call;
// a request without a deadline gets none, so set one (RequestContext) for
// normal API calls. Bodies are replayed through req.GetBody (set by
// http.NewRequest for bytes/strings readers); without it no retry happens.
// Non-2xx responses are returned as-is; the caller closes resp.Body.
func DoHTTP(req *http.Request) (*http.Response, error) {
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", version.UserAgent())
	}
	idempotent := IsIdempotentRequest(req)
	attempts := retryAttempts()
	ctx := req.Context()
	for attempt := 1; ; attempt++ {
		current := req
		if attempt > 1 {
			current = req.Clone(ctx)
			if req.Body != nil && req.Body != http.NoBody {
				if req.GetBody == nil {
					return nil, errors.New("request body cannot be replayed for retry")
				}
				body, err := req.GetBody()
				if err != nil {
					return nil, err
				}
				current.Body = body
			}
		}
		resp, err := sharedHTTPClient.Do(current)
		var header http.Header
		retryErr := err
		if err == nil {
			if !isRetryableStatus(resp.StatusCode) {
				return resp, nil
			}
			header = resp.Header
			retryErr = &httpStatusError{Status: resp.StatusCode}
		}
		retry := attempt < attempts && shouldRetry(idempotent, retryErr) &&
			(req.Body == nil || req.Body == http.NoBody || req.GetBody != nil)
		delay, ok := RetryDelay(attempt, header)
		if !retry || !ok {
			return resp, err
		}
		if resp != nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
			_ = resp.Body.Close()
		}
		debugf("HTTP retry %d/%d for %s %s in %s: %v", attempt, attempts-1, req.Method, redactURLForDebug(req.URL), delay.Round(time.Millisecond), retryErr)
		if sleepErr := SleepContext(ctx, delay); sleepErr != nil {
			return nil, sleepErr
		}
	}
}

func redactURLForDebug(u *url.URL) string {
	if u == nil {
		return ""
	}
	copyURL := *u
	copyURL.RawQuery = ""
	copyURL.User = nil
	return copyURL.String()
}

// RequestOptions tunes DoJSON.
type RequestOptions struct {
	// Context is the parent context (default RootContext()).
	Context context.Context
	// Timeout is the per-attempt timeout (default DefaultRequestTimeout,
	// adjusted by --timeout/HUBFLY_TIMEOUT through RequestTimeout).
	Timeout time.Duration
	// IdempotencyKey, when set, is sent as Idempotency-Key and makes a
	// POST/PATCH retryable. Only for endpoints that dedupe on it.
	IdempotencyKey string
	// Headers are added to the request.
	Headers http.Header
	// NoRetry disables retries (a connect failure is still retried).
	NoRetry bool
}

// DoJSON performs one HubFly API JSON request with the shared timeout,
// retry, auth and error handling; out receives the decoded payload (the
// {ok,data} envelope is unwrapped). Non-2xx responses return *apiError.
func DoJSON(method, rawURL, token string, body, out any, opts RequestOptions) error {
	parent := opts.Context
	if parent == nil {
		parent = RootContext()
	}
	var requestBytes []byte
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return err
		}
		requestBytes = payload
	}
	timeout := RequestTimeout(opts.Timeout)
	requestID := newRequestID()
	idempotent := IsIdempotentMethod(method) || strings.TrimSpace(opts.IdempotencyKey) != ""
	attempts := retryAttempts()
	if opts.NoRetry {
		attempts = 1
	}

	debugf("HTTP request: %s %s", method, rawURL)
	if token != "" {
		debugf("Authorization: Bearer %s", maskToken(token))
	}
	if len(requestBytes) > 0 {
		debugf("Request body: %s", redactJSONForDebug(requestBytes))
	}

	for attempt := 1; ; attempt++ {
		status, header, respBytes, err := doJSONAttempt(parent, method, rawURL, token, requestBytes, requestID, opts, timeout)
		if err != nil {
			err = &requestError{Method: method, URL: rawURL, RequestID: requestID, Timeout: timeout, Attempts: attempt, Err: err}
			if parent.Err() != nil {
				err = &requestError{Method: method, URL: rawURL, RequestID: requestID, Timeout: timeout, Attempts: attempt, Err: parent.Err()}
			}
		} else if status < 200 || status >= 300 {
			apiErr := parseAPIError(status, respBytes)
			if apiErr.RequestID == "" {
				apiErr.RequestID = strings.TrimSpace(header.Get(RequestIDHeader))
			}
			if apiErr.RequestID == "" && status >= 500 {
				apiErr.RequestID = requestID
			}
			err = apiErr
		} else {
			return decodeAPIResponse(status, respBytes, out)
		}

		// NoRetry still allows one retry of a request that never left.
		extraConnectRetry := opts.NoRetry && attempt == 1 && isConnectError(err)
		if attempt >= attempts && !extraConnectRetry {
			return err
		}
		if !shouldRetry(idempotent, err) {
			return err
		}
		var retryHeader http.Header
		if errors.As(err, new(*apiError)) {
			retryHeader = header
		}
		delay, ok := RetryDelay(attempt, retryHeader)
		if !ok {
			return err
		}
		debugf("HTTP retry %d/%d for %s %s in %s: %v", attempt, attempts-1, method, rawURL, delay.Round(time.Millisecond), err)
		if sleepErr := SleepContext(parent, delay); sleepErr != nil {
			return &requestError{Method: method, URL: rawURL, RequestID: requestID, Timeout: timeout, Attempts: attempt, Err: sleepErr}
		}
	}
}

func doJSONAttempt(parent context.Context, method, rawURL, token string, requestBytes []byte, requestID string, opts RequestOptions, timeout time.Duration) (int, http.Header, []byte, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	var reqBody io.Reader
	if requestBytes != nil {
		reqBody = strings.NewReader(string(requestBytes))
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, reqBody)
	if err != nil {
		return 0, nil, nil, err
	}
	for key, values := range opts.Headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if subaccount := strings.TrimSpace(os.Getenv("HUBFLY_SUBACCOUNT")); subaccount != "" {
		req.Header.Set("X-HubFly-Subaccount", subaccount)
	}
	if requestBytes != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key := strings.TrimSpace(opts.IdempotencyKey); key != "" {
		req.Header.Set(IdempotencyKeyHeader, key)
	}
	req.Header.Set(RequestIDHeader, requestID)
	req.Header.Set("User-Agent", version.UserAgent())

	resp, err := sharedHTTPClient.Do(req)
	if err != nil {
		debugf("HTTP transport error: %v", err)
		return 0, nil, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		debugf("HTTP read error: %v", err)
		return 0, nil, nil, err
	}
	debugf("HTTP response status: %d", resp.StatusCode)
	if len(respBytes) > 0 {
		debugf("Response body: %s", redactJSONForDebug(respBytes))
	}
	return resp.StatusCode, resp.Header, respBytes, nil
}

func parseAPIError(status int, respBytes []byte) *apiError {
	msg := strings.TrimSpace(string(respBytes))
	code := ""
	requestID := ""
	errorID := ""
	var details any
	if len(respBytes) > 0 {
		var apiPayload struct {
			Error   any    `json:"error"`
			Message string `json:"message"`
			Meta    struct {
				RequestID string `json:"requestId"`
			} `json:"meta"`
		}
		if err := json.Unmarshal(respBytes, &apiPayload); err == nil {
			requestID = strings.TrimSpace(apiPayload.Meta.RequestID)
			if apiErrorMessage, ok := apiPayload.Error.(string); ok && strings.TrimSpace(apiErrorMessage) != "" {
				msg = strings.TrimSpace(apiErrorMessage)
			} else if errorObject, ok := apiPayload.Error.(map[string]any); ok {
				if value, ok := errorObject["message"].(string); ok && strings.TrimSpace(value) != "" {
					msg = strings.TrimSpace(value)
				}
				if value, ok := errorObject["code"].(string); ok {
					code = strings.TrimSpace(value)
				}
				if value, ok := errorObject["errorId"].(string); ok {
					errorID = strings.TrimSpace(value)
				}
				details = errorObject["details"]
			} else if strings.TrimSpace(apiPayload.Message) != "" {
				msg = strings.TrimSpace(apiPayload.Message)
			}
		} else if looksLikeHTML(msg) {
			// Proxies and load balancers answer 502/503 with HTML pages.
			msg = ""
		}
	}
	if len(msg) > 500 {
		msg = msg[:500] + "..."
	}
	if msg == "" {
		msg = http.StatusText(status)
		if msg == "" {
			msg = "request failed"
		} else {
			msg = strings.ToLower(msg)
		}
	}
	return &apiError{
		Status:    status,
		Code:      code,
		Message:   msg,
		RequestID: requestID,
		ErrorID:   errorID,
		Details:   details,
	}
}

func looksLikeHTML(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	return strings.HasPrefix(lower, "<!doctype") || strings.HasPrefix(lower, "<html")
}

func decodeAPIResponse(status int, respBytes []byte, out any) error {
	if out == nil || len(respBytes) == 0 {
		return nil
	}
	var env struct {
		OK    bool            `json:"ok"`
		Data  json.RawMessage `json:"data"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(respBytes, &env); err == nil && (env.OK || env.Error != nil) {
		if !env.OK {
			if env.Error != nil {
				return &apiError{Status: status, Code: env.Error.Code, Message: env.Error.Message}
			}
			return &apiError{Status: status, Message: "request failed"}
		}
		return json.Unmarshal(env.Data, out)
	}
	return json.Unmarshal(respBytes, out)
}

// ---------------------------------------------------------------------------
// Human and JSON error output

// ErrorHint returns a short next step for err ("" when none applies).
func ErrorHint(err error) string {
	if err == nil {
		return ""
	}
	var apiErr *apiError
	if errors.As(err, &apiErr) {
		code := strings.ToUpper(apiErr.Code)
		switch {
		case apiErr.Status == http.StatusUnauthorized || code == "UNAUTHORIZED" || code == "TOKEN_EXPIRED" || code == "SESSION_EXPIRED":
			if strings.TrimSpace(os.Getenv("HUBFLY_TOKEN")) != "" {
				return "The token in HUBFLY_TOKEN is expired or revoked. Create a new one at " + cliAuthURL + " and update the variable."
			}
			return "Your session has expired or was revoked. Run `hubfly login` to sign in again."
		case apiErr.Status == http.StatusPaymentRequired || strings.Contains(code, "BALANCE") || strings.Contains(code, "PAYMENT"):
			return "Your balance is too low for this. Add funds at https://dashboard.hubfly.space/billing, then retry."
		case apiErr.Status == http.StatusForbidden:
			return "You do not have permission for this. Check the organization (--org) and that your role allows the action."
		case apiErr.Status == http.StatusNotFound:
			return "Not found. Check the name or ID and the organization it belongs to (`hubfly projects`, --org)."
		case apiErr.Status == http.StatusConflict:
			return "This conflicts with the resource's current state, often another operation still running. Wait for it to finish, then retry."
		case apiErr.Status == http.StatusTooManyRequests:
			return "Too many requests. Wait a moment, then retry."
		case apiErr.Status >= 500:
			id := apiErr.ErrorID
			if id == "" {
				id = apiErr.RequestID
			}
			if id != "" {
				return "HubFly had a server-side problem. Retry in a moment; if it keeps failing, contact support and quote request id " + id + "."
			}
			return "HubFly had a server-side problem. Retry in a moment; if it keeps failing, contact support."
		}
		return ""
	}
	var reqErr *requestError
	if errors.As(err, &reqErr) {
		if errors.Is(reqErr.Err, context.Canceled) || errors.Is(reqErr.Err, context.DeadlineExceeded) {
			return ""
		}
		var netErr net.Error
		if errors.As(reqErr.Err, &netErr) && netErr.Timeout() {
			return ""
		}
		return "Check your internet connection and proxy settings (HTTPS_PROXY, NO_PROXY). Run `hubfly doctor` to diagnose."
	}
	return ""
}

// FormatError renders err for people: the message plus a hint line.
func FormatError(err error) string {
	if err == nil {
		return ""
	}
	message := "Error: " + err.Error()
	if hint := ErrorHint(err); hint != "" {
		message += "\n" + hint
	}
	return message
}

// ErrorPayload is the --json error shape printed to stderr:
// {"ok":false,"error":{"message","code","status","requestId","hint","kind","exitCode"}}.
type ErrorPayload struct {
	OK    bool          `json:"ok"`
	Error ErrorEnvelope `json:"error"`
}

// ErrorEnvelope is the "error" object of ErrorPayload.
type ErrorEnvelope struct {
	Message   string `json:"message"`
	Kind      string `json:"kind"`
	Code      string `json:"code,omitempty"`
	Status    int    `json:"status,omitempty"`
	RequestID string `json:"requestId,omitempty"`
	Hint      string `json:"hint,omitempty"`
	ExitCode  int    `json:"exitCode"`
	Details   any    `json:"details,omitempty"`
}

// ErrorJSON builds the --json error document for err.
func ErrorJSON(err error, exitCode int) []byte {
	envelope := ErrorEnvelope{Message: err.Error(), Kind: "error", Hint: ErrorHint(err), ExitCode: exitCode}
	var apiErr *apiError
	var reqErr *requestError
	switch {
	case errors.As(err, &apiErr):
		envelope.Message = apiErr.Message
		envelope.Kind = apiErrorKind(apiErr)
		envelope.Code = apiErr.Code
		envelope.Status = apiErr.Status
		envelope.RequestID = apiErr.ErrorID
		if envelope.RequestID == "" {
			envelope.RequestID = apiErr.RequestID
		}
		envelope.Details = apiErr.Details
	case errors.As(err, &reqErr):
		envelope.RequestID = reqErr.RequestID
		var netErr net.Error
		switch {
		case errors.Is(reqErr.Err, context.Canceled):
			envelope.Kind = "cancelled"
		case errors.Is(reqErr.Err, context.DeadlineExceeded) || (errors.As(reqErr.Err, &netErr) && netErr.Timeout()):
			envelope.Kind = "timeout"
		default:
			envelope.Kind = "network"
		}
	case errors.Is(err, context.Canceled):
		envelope.Kind = "cancelled"
	}
	payload, marshalErr := json.Marshal(ErrorPayload{OK: false, Error: envelope})
	if marshalErr != nil {
		envelope.Details = nil
		payload, _ = json.Marshal(ErrorPayload{OK: false, Error: envelope})
	}
	return payload
}

func apiErrorKind(apiErr *apiError) string {
	switch {
	case apiErr.Status == http.StatusUnauthorized:
		return "auth"
	case apiErr.Status == http.StatusPaymentRequired:
		return "payment"
	case apiErr.Status == http.StatusForbidden:
		return "permission"
	case apiErr.Status == http.StatusNotFound:
		return "not_found"
	case apiErr.Status == http.StatusConflict:
		return "conflict"
	case apiErr.Status == http.StatusTooManyRequests:
		return "rate_limited"
	case apiErr.Status >= 500:
		return "server"
	case apiErr.Status >= 400:
		return "invalid_request"
	}
	return "api"
}

// ---------------------------------------------------------------------------
// Entry point

// configureGlobalFlags consumes leading global flags (before the command
// name, so subcommand flags such as `db query --timeout` keep working):
// --timeout <d>, --timeout=<d>, --json-errors, --no-retry.
func configureGlobalFlags(args []string) ([]string, error) {
	loadHTTPSettingsFromEnv()
	index := 0
	for index < len(args) {
		arg := args[index]
		switch {
		case arg == "--timeout":
			if index+1 >= len(args) {
				return nil, errors.New("usage: hubfly --timeout <duration> <command> (e.g. --timeout 90s)")
			}
			d, err := parseTimeoutValue(args[index+1])
			if err != nil {
				return nil, fmt.Errorf("invalid --timeout: %w", err)
			}
			setTimeoutOverride(d)
			index += 2
		case strings.HasPrefix(arg, "--timeout="):
			d, err := parseTimeoutValue(strings.TrimPrefix(arg, "--timeout="))
			if err != nil {
				return nil, fmt.Errorf("invalid --timeout: %w", err)
			}
			setTimeoutOverride(d)
			index++
		case arg == "--json-errors":
			httpSettingsMu.Lock()
			jsonErrorOutput = true
			httpSettingsMu.Unlock()
			index++
		case arg == "--no-retry":
			httpSettingsMu.Lock()
			maxAttempts = 1
			httpSettingsMu.Unlock()
			index++
		default:
			return args[index:], nil
		}
	}
	return args[index:], nil
}

func wantsJSONErrors(args []string) bool {
	httpSettingsMu.RLock()
	enabled := jsonErrorOutput
	httpSettingsMu.RUnlock()
	if enabled {
		return true
	}
	for _, arg := range args {
		if arg == "--" {
			break
		}
		if arg == "--json" || arg == "--output=json" {
			return true
		}
	}
	return false
}

// Main is the CLI entry point used by main.go: global flags, Ctrl-C
// handling, the doctor command and human/JSON error output around run().
func Main(args []string) int {
	args = configureDebug(args)
	args, err := configureGlobalFlags(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, FormatError(err))
		return 2
	}
	stop := installInterruptHandler()
	defer stop()
	debugf("debug mode enabled")

	if len(args) > 0 && args[0] == "doctor" {
		err = doctorFlow(args[1:])
	} else {
		err = run(args)
	}
	if err == nil {
		return 0
	}
	code := exitCodeForError(err)
	if wasInterrupted() || errors.Is(err, context.Canceled) && RootContext().Err() != nil {
		code = 130
	}
	if wantsJSONErrors(args) {
		fmt.Fprintln(os.Stderr, string(ErrorJSON(err, code)))
	} else if code == 130 {
		fmt.Fprintln(os.Stderr, "Interrupted.")
	} else {
		fmt.Fprintln(os.Stderr, FormatError(err))
	}
	return code
}

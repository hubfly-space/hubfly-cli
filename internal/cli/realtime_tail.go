package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	gws "github.com/gorilla/websocket"
)

// `hubfly realtime tail`: the live inspector (WIRE.md "Inspector"). The CLI
// mints a short-lived inspector token from the Dashboard, dials
// wss://<host>/hubfly/v1/inspect with the API origin as Origin, and sends the
// token in the first frame (never in the URL).

const (
	realtimeInspectPath      = "/hubfly/v1/inspect"
	realtimeTailDialTimeout  = 10 * time.Second
	realtimeTailReadyTimeout = 10 * time.Second
	realtimeTailPingInterval = 25 * time.Second
	realtimeTailIdleTimeout  = 75 * time.Second
	realtimeTailMaxAuthFails = 3
)

// Inspector close codes (WIRE.md).
const (
	realtimeCloseExpired      = 4000
	realtimeCloseBadToken     = 4401
	realtimeCloseOrigin       = 4403
	realtimeCloseAuthTimeout  = 4408
	realtimeCloseReplaced     = 4409
	realtimeCloseTooManyTails = 4429
)

var realtimeTailFamilies = []string{"conn", "sub", "member", "msg", "api", "err", "webhook"}

// rtTailContext returns the context that ends tail (Ctrl-C). Tests replace it.
var rtTailContext = func() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt)
}

type realtimeTailFilter struct {
	Apps          []string `json:"apps,omitempty"`
	Families      []string `json:"families,omitempty"`
	ChannelPrefix string   `json:"channel_prefix,omitempty"`
}

func (f realtimeTailFilter) empty() bool {
	return len(f.Apps) == 0 && len(f.Families) == 0 && f.ChannelPrefix == ""
}

type realtimeTail struct {
	// mint returns a fresh inspector token for every (re)connect.
	mint func(ctx context.Context) (rtInspectorToken, error)
	// origin is used when the token response does not name one.
	origin  string
	filter  realtimeTailFilter
	json    bool
	out     io.Writer
	errOut  io.Writer
	backoff func(attempt int) time.Duration

	writeMu sync.Mutex
}

type realtimeTailFatal struct{ err error }

func (f realtimeTailFatal) Error() string { return f.err.Error() }
func (f realtimeTailFatal) Unwrap() error { return f.err }

func defaultRealtimeTailBackoff(attempt int) time.Duration {
	if attempt <= 0 {
		return 0
	}
	delay := time.Second << (attempt - 1)
	if delay > 30*time.Second || delay <= 0 {
		delay = 30 * time.Second
	}
	return delay
}

// apiOrigin is the scheme://host[:port] of the Dashboard API (the inspector
// token's `orn` claim and the WebSocket Origin header).
func apiOrigin() (string, error) {
	parsed, err := url.Parse(apiHost)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("invalid HUBFLY_API_URL %q", apiHost)
	}
	return parsed.Scheme + "://" + parsed.Host, nil
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// validateInspectorURL accepts wss://<host>/hubfly/v1/inspect (ws:// only for
// loopback hosts) and drops any query: the token never goes in the URL.
func validateInspectorURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return "", fmt.Errorf("the Dashboard returned an invalid inspector URL")
	}
	switch parsed.Scheme {
	case "wss":
	case "ws":
		if !isLoopbackHost(parsed.Hostname()) {
			return "", fmt.Errorf("refusing an unencrypted inspector URL (ws://%s)", parsed.Host)
		}
	default:
		return "", fmt.Errorf("the Dashboard returned an inspector URL with scheme %q", parsed.Scheme)
	}
	if parsed.Path != realtimeInspectPath {
		return "", fmt.Errorf("unexpected inspector path %q", parsed.Path)
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	parsed.User = nil
	return parsed.String(), nil
}

func realtimeCloseCode(err error) int {
	var closeErr *gws.CloseError
	if errors.As(err, &closeErr) {
		return closeErr.Code
	}
	return 0
}

func sleepContext(ctx context.Context, delay time.Duration) bool {
	if delay <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// notice writes "# ..." to stderr. Text from the server or the network
// (messages, errors, ids) is quoted when it holds control or non-printable
// runes, so it cannot drive the terminal.
func (t *realtimeTail) notice(format string, args ...any) {
	for index, arg := range args {
		switch typed := arg.(type) {
		case string:
			args[index] = terminalSafe(typed)
		case error:
			args[index] = terminalSafe(typed.Error())
		case fmt.Stringer:
			args[index] = terminalSafe(typed.String())
		}
	}
	_, _ = fmt.Fprintf(t.errOut, "# "+format+"\n", args...)
}

// terminalPrintable reports whether s is valid UTF-8 made only of printable
// runes and spaces (no C0/C1 controls, DEL, bidi or other format runes).
func terminalPrintable(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

// terminalSafe returns s, or s quoted (strconv.Quote escapes every
// non-printable rune) when it could write escape sequences such as OSC 52
// or CSI to the terminal.
func terminalSafe(s string) string {
	if terminalPrintable(s) {
		return s
	}
	return strconv.Quote(s)
}

// escapeJSONNonPrintable rewrites the non-printable runes of compact JSON
// (DEL, C1, bidi and other format runes, which JSON allows raw inside
// strings) as \uXXXX escapes. Compact JSON has no raw whitespace outside
// strings, so every such rune is inside a string and the value is unchanged.
func escapeJSONNonPrintable(compact []byte) []byte {
	if terminalPrintable(string(compact)) {
		return compact
	}
	var out bytes.Buffer
	for _, r := range string(compact) {
		if unicode.IsPrint(r) {
			out.WriteRune(r)
			continue
		}
		if r > 0xFFFF {
			high, low := utf16.EncodeRune(r)
			fmt.Fprintf(&out, "\\u%04x\\u%04x", high, low)
			continue
		}
		fmt.Fprintf(&out, "\\u%04x", r)
	}
	return out.Bytes()
}

// run keeps a session open until ctx ends or a fatal close code arrives.
func (t *realtimeTail) run(ctx context.Context) error {
	attempt := 0
	authFailures := 0
	for {
		if ctx.Err() != nil {
			return nil
		}
		token, err := t.mint(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			var apiErr *apiError
			if errors.As(err, &apiErr) && apiErr.Status >= 400 && apiErr.Status < 500 && apiErr.Status != 429 {
				return realtimeAPIError(err)
			}
			attempt++
			delay := t.backoff(attempt)
			t.notice("could not get an inspector token (%v); retrying in %s", err, delay)
			if !sleepContext(ctx, delay) {
				return nil
			}
			continue
		}
		ready, err := t.session(ctx, token)
		if ctx.Err() != nil {
			return nil
		}
		var fatal realtimeTailFatal
		if errors.As(err, &fatal) {
			return fatal.err
		}
		if ready {
			attempt = 0
		}
		code := realtimeCloseCode(err)
		switch code {
		case realtimeCloseOrigin:
			return fmt.Errorf("the inspector refused origin %s (close 4403); the Dashboard must list the API origin in the realtime inspector origins", t.originFor(token))
		case realtimeCloseTooManyTails:
			return errors.New("the realtime server already has 5 inspector sessions open (close 4429); close one (dashboard or CLI) and try again")
		case realtimeCloseReplaced:
			// Reconnecting would take the session back from the newer one, and the
			// two would keep replacing each other.
			return errors.New("a newer inspector session of the same user replaced this one (close 4409); run tail again to take it back")
		case realtimeCloseBadToken:
			authFailures++
			if authFailures >= realtimeTailMaxAuthFails {
				return errors.New("the inspector rejected the token (close 4401: missing, invalid, expired or replayed)")
			}
			t.notice("inspector token rejected; reconnecting with a new token")
			continue
		case realtimeCloseExpired:
			authFailures = 0
			t.notice("inspector session ended (expired or server draining); reconnecting with a new token")
			continue
		case realtimeCloseAuthTimeout:
			t.notice("inspector authentication timed out; reconnecting")
		default:
			if ready {
				authFailures = 0
			}
			if err != nil {
				debugf("inspector session ended: %v", err)
			}
		}
		attempt++
		delay := t.backoff(attempt)
		t.notice("inspector connection lost; reconnecting in %s", delay)
		if !sleepContext(ctx, delay) {
			return nil
		}
	}
}

func (t *realtimeTail) originFor(token rtInspectorToken) string {
	if strings.TrimSpace(token.Origin) != "" {
		return strings.TrimSpace(token.Origin)
	}
	return t.origin
}

func (t *realtimeTail) write(conn *gws.Conn, value any) error {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return conn.WriteJSON(value)
}

type realtimeTailFrame struct {
	Type      string   `json:"type"`
	SessionID string   `json:"session_id"`
	Apps      []string `json:"apps"`
	ExpiresAt string   `json:"expires_at"`
	Sampling  *float64 `json:"sampling"`
	Rate      *float64 `json:"rate"`
	Count     *int64   `json:"count"`
	Message   string   `json:"message"`
}

// session runs one connection. ready reports whether the server accepted the
// token. Errors are close errors, transport errors or realtimeTailFatal.
func (t *realtimeTail) session(ctx context.Context, token rtInspectorToken) (bool, error) {
	target, err := validateInspectorURL(token.URL)
	if err != nil {
		return false, realtimeTailFatal{err}
	}
	if strings.TrimSpace(token.Token) == "" {
		return false, realtimeTailFatal{errors.New("the Dashboard returned an empty inspector token")}
	}
	header := http.Header{}
	header.Set("Origin", t.originFor(token))
	dialer := gws.Dialer{HandshakeTimeout: realtimeTailDialTimeout, Proxy: http.ProxyFromEnvironment}
	dialCtx, cancelDial := context.WithTimeout(ctx, realtimeTailDialTimeout)
	conn, response, err := dialer.DialContext(dialCtx, target, header)
	cancelDial()
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		if response != nil && response.StatusCode == http.StatusForbidden {
			return false, &gws.CloseError{Code: realtimeCloseOrigin, Text: "forbidden"}
		}
		return false, err
	}
	defer func() { _ = conn.Close() }()

	stop := make(chan struct{})
	var stopOnce sync.Once
	closeSession := func() { stopOnce.Do(func() { close(stop) }) }
	defer closeSession()
	go func() {
		select {
		case <-ctx.Done():
			t.writeMu.Lock()
			_ = conn.WriteControl(gws.CloseMessage, gws.FormatCloseMessage(gws.CloseNormalClosure, ""), time.Now().Add(time.Second))
			t.writeMu.Unlock()
			_ = conn.Close()
		case <-stop:
		}
	}()

	// First frame: the token. Never in the URL or the subprotocol.
	if err := t.write(conn, map[string]string{"type": "auth", "token": token.Token}); err != nil {
		return false, err
	}
	_ = conn.SetReadDeadline(time.Now().Add(realtimeTailReadyTimeout))
	var ready realtimeTailFrame
	for {
		_, raw, readErr := conn.ReadMessage()
		if readErr != nil {
			return false, readErr
		}
		if json.Unmarshal(raw, &ready) != nil {
			continue
		}
		if ready.Type == "ready" {
			break
		}
		t.handleFrame(raw)
	}
	apps := "all apps"
	if len(ready.Apps) > 0 && !(len(ready.Apps) == 1 && ready.Apps[0] == "*") {
		apps = "apps " + strings.Join(ready.Apps, ", ")
	}
	t.notice("inspector connected (%s, session %s); press Ctrl-C to stop", apps, ready.SessionID)
	if ready.Sampling != nil && *ready.Sampling < 1 {
		t.notice("sampling: showing about %s of events", formatSamplingRate(*ready.Sampling))
	}
	if !t.filter.empty() {
		frame := map[string]any{"type": "filter"}
		if len(t.filter.Apps) > 0 {
			frame["apps"] = t.filter.Apps
		}
		if len(t.filter.Families) > 0 {
			frame["families"] = t.filter.Families
		}
		if t.filter.ChannelPrefix != "" {
			frame["channel_prefix"] = t.filter.ChannelPrefix
		}
		if err := t.write(conn, frame); err != nil {
			return true, err
		}
	}

	go func() {
		ticker := time.NewTicker(realtimeTailPingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if t.write(conn, map[string]string{"type": "ping"}) != nil {
					return
				}
			}
		}
	}()

	for {
		_ = conn.SetReadDeadline(time.Now().Add(realtimeTailIdleTimeout))
		_, raw, readErr := conn.ReadMessage()
		if readErr != nil {
			return true, readErr
		}
		t.handleFrame(raw)
	}
}

func formatSamplingRate(rate float64) string {
	return strconv.FormatFloat(rate*100, 'f', -1, 64) + "%"
}

func (t *realtimeTail) handleFrame(raw []byte) {
	var frame realtimeTailFrame
	if err := json.Unmarshal(raw, &frame); err != nil {
		debugf("inspector: undecodable frame: %v", err)
		return
	}
	switch frame.Type {
	case "event":
		t.printEvent(raw)
	case "sampling":
		if t.json {
			t.printJSONLine(raw)
		}
		if frame.Rate != nil {
			if *frame.Rate >= 1 {
				t.notice("sampling off: showing every event")
			} else {
				t.notice("sampling: showing about %s of events (the inspector sends at most 500 events/s)", formatSamplingRate(*frame.Rate))
			}
		}
	case "dropped":
		if t.json {
			t.printJSONLine(raw)
		}
		if frame.Count != nil {
			t.notice("%d events dropped", *frame.Count)
		}
	case "pong", "ready":
	case "error":
		t.notice("inspector error: %s", frame.Message)
	default:
		debugf("inspector: unhandled frame type %q", frame.Type)
	}
}

func (t *realtimeTail) printJSONLine(raw []byte) {
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return
	}
	line := escapeJSONNonPrintable(compact.Bytes())
	_, _ = t.out.Write(append(line, '\n'))
}

func (t *realtimeTail) printEvent(raw []byte) {
	if t.json {
		t.printJSONLine(raw)
		return
	}
	var event map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&event) != nil {
		return
	}
	_, _ = fmt.Fprintln(t.out, formatRealtimeTailEvent(event))
}

// formatRealtimeTailEvent renders "15:04:05.000 <app> <kind> key=value ...".
func formatRealtimeTailEvent(event map[string]any) string {
	stamp := "--:--:--.---"
	if number, ok := event["t"].(json.Number); ok {
		if ms, err := number.Int64(); err == nil {
			stamp = time.UnixMilli(ms).Format("15:04:05.000")
		}
	}
	appID := formatRealtimeTailValue(event["app_id"])
	kind := formatRealtimeTailValue(event["kind"])
	skip := map[string]bool{"type": true, "seq": true, "t": true, "app_id": true, "family": true, "kind": true, "preview": true}
	keys := make([]string, 0, len(event))
	for key := range event {
		if !skip[key] {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	parts := []string{stamp, appID, kind}
	for _, key := range keys {
		parts = append(parts, terminalSafe(key)+"="+formatRealtimeTailValue(event[key]))
	}
	if preview, ok := event["preview"]; ok {
		parts = append(parts, "preview="+formatRealtimeTailValue(preview))
	}
	return strings.Join(parts, " ")
}

func formatRealtimeTailValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return "-"
	case string:
		if typed == "" || strings.ContainsAny(typed, " \t\n\"=") ||
			!terminalPrintable(typed) {
			return strconv.Quote(typed)
		}
		return typed
	case json.Number:
		return typed.String()
	case []any:
		items := make([]string, 0, len(typed))
		for _, item := range typed {
			items = append(items, formatRealtimeTailValue(item))
		}
		return strings.Join(items, ",")
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return terminalSafe(fmt.Sprint(typed))
		}
		return string(escapeJSONNonPrintable(encoded))
	}
}

func realtimeTailFlow(args []string) error {
	const usage = "hubfly realtime tail <server> [--app <app>...] [--family conn,sub,member,msg,api,err,webhook] [--channel-prefix <prefix>] [--preview] [--project <id|name>] [--json]"
	fs := newRealtimeFlags("tail")
	projectRef := fs.String("project", "", "")
	appRefs := &rtList{split: true}
	fs.Var(appRefs, "app", "")
	families := &rtList{split: true}
	fs.Var(families, "family", "")
	fs.Var(families, "families", "")
	channelPrefix := fs.String("channel-prefix", "", "")
	preview := fs.Bool("preview", false, "")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return fmt.Errorf("usage: %s", usage)
	}
	for index, family := range families.values {
		family = strings.ToLower(family)
		families.values[index] = family
		known := false
		for _, candidate := range realtimeTailFamilies {
			if family == candidate {
				known = true
				break
			}
		}
		if !known {
			return fmt.Errorf("invalid --family %q (expected %s)", family, strings.Join(realtimeTailFamilies, ", "))
		}
	}
	origin, err := apiOrigin()
	if err != nil {
		return err
	}
	target, err := resolveRealtimeTarget(positionals[0], *projectRef)
	if err != nil {
		return err
	}
	appIDs := []string{}
	if len(appRefs.values) > 0 {
		apps, appsErr := fetchRealtimeApps(target.token, target.instance.ProjectID, target.instance.ID)
		if appsErr != nil {
			return realtimeAPIError(appsErr)
		}
		for _, ref := range appRefs.values {
			app, pickErr := pickRealtimeApp(apps, target.instance.Name, ref)
			if pickErr != nil {
				return pickErr
			}
			appIDs = append(appIDs, app.AppID)
		}
	}
	tail := &realtimeTail{
		origin: origin,
		filter: realtimeTailFilter{Apps: appIDs, Families: families.values, ChannelPrefix: strings.TrimSpace(*channelPrefix)},
		json:   *asJSON,
		out:    rtOut,
		errOut: rtErr,
		mint: func(context.Context) (rtInspectorToken, error) {
			return createRealtimeInspectorToken(target.token, target.instance.ProjectID, target.instance.ID, rtInspectorTokenInput{
				Origin:         origin,
				Apps:           appIDs,
				PayloadPreview: *preview,
			})
		},
		backoff: defaultRealtimeTailBackoff,
	}
	ctx, stop := rtTailContext()
	defer stop()
	return tail.run(ctx)
}

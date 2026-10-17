package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	gws "github.com/gorilla/websocket"
)

// fakeInspector is an in-process stand-in for the engine's inspector socket
// (WIRE.md "Inspector"): it records the Origin, the URL query, the first
// (auth) frame and filter frames, then runs a per-connection script.
type fakeInspector struct {
	mu      sync.Mutex
	conns   int
	origins []string
	queries []string
	auths   []map[string]any
	filters []map[string]any
	reject  int // HTTP status to refuse the upgrade with
	script  func(n int, conn *gws.Conn)
	server  *httptest.Server
}

func newFakeInspector(t *testing.T) *fakeInspector {
	t.Helper()
	f := &fakeInspector{}
	upgrader := gws.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != realtimeInspectPath {
			http.NotFound(w, r)
			return
		}
		f.mu.Lock()
		f.conns++
		n := f.conns
		f.origins = append(f.origins, r.Header.Get("Origin"))
		f.queries = append(f.queries, r.URL.RawQuery)
		reject := f.reject
		script := f.script
		f.mu.Unlock()
		if reject != 0 {
			w.WriteHeader(reject)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		var auth map[string]any
		if err := conn.ReadJSON(&auth); err != nil {
			return
		}
		f.mu.Lock()
		f.auths = append(f.auths, auth)
		f.mu.Unlock()
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		script(n, conn)
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeInspector) url() string {
	return "ws" + strings.TrimPrefix(f.server.URL, "http") + realtimeInspectPath
}

func (f *fakeInspector) readFilter(conn *gws.Conn) {
	var filter map[string]any
	if conn.ReadJSON(&filter) == nil {
		f.mu.Lock()
		f.filters = append(f.filters, filter)
		f.mu.Unlock()
	}
}

func (f *fakeInspector) snapshot() (origins, queries []string, auths, filters []map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.origins...), append([]string(nil), f.queries...),
		append([]map[string]any(nil), f.auths...), append([]map[string]any(nil), f.filters...)
}

func inspectorReady(conn *gws.Conn, n int) {
	_ = conn.WriteJSON(map[string]any{"type": "ready", "session_id": fmt.Sprintf("is_%d", n), "apps": []string{"1000001"}, "expires_at": "2026-10-17T10:00:00Z", "sampling": 1})
}

func inspectorClose(conn *gws.Conn, code int, text string) {
	_ = conn.WriteControl(gws.CloseMessage, gws.FormatCloseMessage(code, text), time.Now().Add(time.Second))
}

func drainUntilClosed(conn *gws.Conn) {
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
	}
}

func waitForCondition(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type tailHarness struct {
	tail   *realtimeTail
	out    *rtSyncBuffer
	errOut *rtSyncBuffer
	mints  func() int
}

func newTailHarness(insp *fakeInspector, filter realtimeTailFilter, asJSON bool) tailHarness {
	var mu sync.Mutex
	mints := 0
	out, errOut := &rtSyncBuffer{}, &rtSyncBuffer{}
	tail := &realtimeTail{
		origin:  "https://fallback.hubfly.test",
		filter:  filter,
		json:    asJSON,
		out:     out,
		errOut:  errOut,
		backoff: func(int) time.Duration { return time.Millisecond },
		mint: func(context.Context) (rtInspectorToken, error) {
			mu.Lock()
			defer mu.Unlock()
			mints++
			// A token in the URL must be dropped: tokens only travel in the first frame.
			return rtInspectorToken{Token: fmt.Sprintf("tok-%d", mints), URL: insp.url() + "?token=never-in-the-url", Origin: "https://api.hubfly.test"}, nil
		},
	}
	return tailHarness{tail: tail, out: out, errOut: errOut, mints: func() int { mu.Lock(); defer mu.Unlock(); return mints }}
}

func runTailInBackground(tail *realtimeTail) (context.CancelFunc, chan error) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- tail.run(ctx) }()
	return cancel, done
}

func waitTailResult(t *testing.T, done chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("tail did not return")
		return nil
	}
}

func TestRealtimeTailAuthOriginFiltersSamplingAndReconnect(t *testing.T) {
	insp := newFakeInspector(t)
	insp.script = func(n int, conn *gws.Conn) {
		inspectorReady(conn, n)
		insp.readFilter(conn)
		if n == 1 {
			_ = conn.WriteJSON(map[string]any{"type": "sampling", "rate": 0.25})
			_ = conn.WriteJSON(map[string]any{"type": "dropped", "count": 7})
			_ = conn.WriteJSON(map[string]any{"type": "event", "seq": 1, "t": 1760600000000, "app_id": "1000001", "family": "msg", "kind": "msg.publish",
				"channels": []string{"orders"}, "event": "order.updated", "size": 120, "recipients": 4, "source": "api", "encrypted": false})
			inspectorClose(conn, realtimeCloseExpired, "session expired")
			return
		}
		_ = conn.WriteJSON(map[string]any{"type": "event", "seq": 1, "t": 1760600000500, "app_id": "1000001", "family": "conn", "kind": "conn.open",
			"socket_id": "1234567890.1234567890", "protocol": "hubfly", "origin": "https://shop.example"})
		drainUntilClosed(conn)
	}
	filter := realtimeTailFilter{Apps: []string{"1000001"}, Families: []string{"msg", "conn"}, ChannelPrefix: "orders"}
	h := newTailHarness(insp, filter, false)
	cancel, done := runTailInBackground(h.tail)
	waitForCondition(t, "the event of the second session", func() bool { return strings.Contains(h.out.String(), "conn.open") })
	cancel()
	if err := waitTailResult(t, done); err != nil {
		t.Fatalf("tail returned %v after Ctrl-C, want nil", err)
	}

	origins, queries, auths, filters := insp.snapshot()
	if h.mints() != 2 {
		t.Fatalf("minted %d tokens, want a fresh token per connection (2)", h.mints())
	}
	wantAuths := []map[string]any{{"type": "auth", "token": "tok-1"}, {"type": "auth", "token": "tok-2"}}
	if !reflect.DeepEqual(auths, wantAuths) {
		t.Fatalf("first frames = %v, want %v", auths, wantAuths)
	}
	if !reflect.DeepEqual(origins, []string{"https://api.hubfly.test", "https://api.hubfly.test"}) {
		t.Fatalf("Origin headers = %q", origins)
	}
	if !reflect.DeepEqual(queries, []string{"", ""}) {
		t.Fatalf("inspector URL queries = %q, want none (no token in the URL)", queries)
	}
	wantFilter := map[string]any{"type": "filter", "apps": []any{"1000001"}, "families": []any{"msg", "conn"}, "channel_prefix": "orders"}
	if len(filters) != 2 || !reflect.DeepEqual(filters[0], wantFilter) || !reflect.DeepEqual(filters[1], wantFilter) {
		t.Fatalf("filter frames = %v, want %v twice", filters, wantFilter)
	}
	out := h.out.String()
	if !strings.Contains(out, " 1000001 msg.publish channels=orders encrypted=false event=order.updated recipients=4 size=120 source=api\n") {
		t.Fatalf("pretty output:\n%s", out)
	}
	if !strings.Contains(out, " 1000001 conn.open origin=https://shop.example protocol=hubfly socket_id=1234567890.1234567890\n") {
		t.Fatalf("pretty output:\n%s", out)
	}
	notices := h.errOut.String()
	for _, want := range []string{"inspector connected (apps 1000001, session is_1)", "sampling: showing about 25% of events", "7 events dropped", "reconnecting with a new token", "session is_2"} {
		if !strings.Contains(notices, want) {
			t.Fatalf("notices missing %q:\n%s", want, notices)
		}
	}
}

func TestRealtimeTailJSONLines(t *testing.T) {
	insp := newFakeInspector(t)
	insp.script = func(n int, conn *gws.Conn) {
		inspectorReady(conn, n)
		_ = conn.WriteJSON(map[string]any{"type": "sampling", "rate": 0.5})
		_ = conn.WriteJSON(map[string]any{"type": "event", "seq": 9, "t": 1760600000000, "app_id": "1000001", "family": "err", "kind": "err.auth", "reason": "bad signature"})
		_ = conn.WriteJSON(map[string]any{"type": "dropped", "count": 3})
		_ = conn.WriteJSON(map[string]any{"type": "pong"})
		drainUntilClosed(conn)
	}
	h := newTailHarness(insp, realtimeTailFilter{}, true)
	cancel, done := runTailInBackground(h.tail)
	waitForCondition(t, "three JSON lines", func() bool { return strings.Count(h.out.String(), "\n") >= 3 })
	cancel()
	if err := waitTailResult(t, done); err != nil {
		t.Fatalf("tail returned %v", err)
	}
	lines := strings.Split(strings.TrimSpace(h.out.String()), "\n")
	types := []string{}
	for _, line := range lines {
		var frame map[string]any
		if err := json.Unmarshal([]byte(line), &frame); err != nil {
			t.Fatalf("line %q is not JSON: %v", line, err)
		}
		types = append(types, fmt.Sprint(frame["type"]))
	}
	if !reflect.DeepEqual(types, []string{"sampling", "event", "dropped"}) {
		t.Fatalf("JSON line types = %q", types)
	}
	if _, _, _, filters := insp.snapshot(); len(filters) != 0 {
		t.Fatalf("an empty filter must not send a filter frame: %v", filters)
	}
}

func TestRealtimeTailFatalCloseCodes(t *testing.T) {
	for _, tc := range []struct {
		code int
		want string
	}{
		{realtimeCloseOrigin, "4403"},
		{realtimeCloseTooManyTails, "5 inspector sessions"},
		{realtimeCloseReplaced, "4409"},
	} {
		insp := newFakeInspector(t)
		insp.script = func(_ int, conn *gws.Conn) { inspectorClose(conn, tc.code, "no") }
		h := newTailHarness(insp, realtimeTailFilter{}, false)
		_, done := runTailInBackground(h.tail)
		err := waitTailResult(t, done)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("close %d: err = %v, want %q", tc.code, err, tc.want)
		}
		if h.mints() != 1 {
			t.Fatalf("close %d: reconnected (%d tokens)", tc.code, h.mints())
		}
	}

	insp := newFakeInspector(t)
	insp.reject = http.StatusForbidden
	h := newTailHarness(insp, realtimeTailFilter{}, false)
	_, done := runTailInBackground(h.tail)
	if err := waitTailResult(t, done); err == nil || !strings.Contains(err.Error(), "refused origin https://api.hubfly.test") {
		t.Fatalf("handshake 403: err = %v", err)
	}
}

func TestRealtimeTailRejectedTokensRetryThenFail(t *testing.T) {
	insp := newFakeInspector(t)
	insp.script = func(_ int, conn *gws.Conn) { inspectorClose(conn, realtimeCloseBadToken, "invalid token") }
	h := newTailHarness(insp, realtimeTailFilter{}, false)
	_, done := runTailInBackground(h.tail)
	err := waitTailResult(t, done)
	if err == nil || !strings.Contains(err.Error(), "4401") {
		t.Fatalf("err = %v", err)
	}
	if h.mints() != realtimeTailMaxAuthFails {
		t.Fatalf("minted %d tokens, want %d", h.mints(), realtimeTailMaxAuthFails)
	}
	_, _, auths, _ := insp.snapshot()
	if len(auths) != realtimeTailMaxAuthFails || auths[1]["token"] != "tok-2" {
		t.Fatalf("auth frames = %v", auths)
	}
}

func TestRealtimeTailReconnectsAfterDroppedConnection(t *testing.T) {
	insp := newFakeInspector(t)
	insp.script = func(n int, conn *gws.Conn) {
		inspectorReady(conn, n)
		if n == 1 {
			_ = conn.UnderlyingConn().Close() // abrupt loss, no close frame
			return
		}
		_ = conn.WriteJSON(map[string]any{"type": "event", "t": 1760600000000, "app_id": "1000001", "kind": "msg.client", "channel": "private-x"})
		drainUntilClosed(conn)
	}
	h := newTailHarness(insp, realtimeTailFilter{}, false)
	cancel, done := runTailInBackground(h.tail)
	waitForCondition(t, "the event after reconnecting", func() bool { return strings.Contains(h.out.String(), "msg.client") })
	cancel()
	if err := waitTailResult(t, done); err != nil {
		t.Fatalf("tail returned %v", err)
	}
	if h.mints() != 2 || !strings.Contains(h.errOut.String(), "connection lost; reconnecting") {
		t.Fatalf("mints=%d notices:\n%s", h.mints(), h.errOut.String())
	}
}

func TestRealtimeTailTokenErrors(t *testing.T) {
	out, errOut := &rtSyncBuffer{}, &rtSyncBuffer{}
	tail := &realtimeTail{
		out: out, errOut: errOut, backoff: func(int) time.Duration { return time.Millisecond },
		mint: func(context.Context) (rtInspectorToken, error) {
			return rtInspectorToken{}, &apiError{Status: 409, Code: "MANAGED_INSTANCE_NOT_RUNNING", Message: "Start the realtime server to use this feature."}
		},
	}
	err := tail.run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Start the realtime server first") || exitCodeForError(err) != 4 {
		t.Fatalf("not running: %v", err)
	}

	calls := 0
	tail.mint = func(context.Context) (rtInspectorToken, error) {
		calls++
		if calls < 3 {
			return rtInspectorToken{}, errors.New("connection refused")
		}
		return rtInspectorToken{Token: "t", URL: "ws://example.com/hubfly/v1/inspect"}, nil
	}
	err = tail.run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unencrypted inspector URL") || calls != 3 {
		t.Fatalf("transient token errors then insecure URL: calls=%d err=%v", calls, err)
	}
	if !strings.Contains(errOut.String(), "could not get an inspector token") {
		t.Fatalf("notices:\n%s", errOut.String())
	}
}

func TestValidateInspectorURL(t *testing.T) {
	cases := []struct {
		in, want, err string
	}{
		{in: "wss://chat.rt.hubfly.app/hubfly/v1/inspect", want: "wss://chat.rt.hubfly.app/hubfly/v1/inspect"},
		{in: "wss://chat.rt.hubfly.app/hubfly/v1/inspect?token=x#frag", want: "wss://chat.rt.hubfly.app/hubfly/v1/inspect"},
		{in: "ws://127.0.0.1:9999/hubfly/v1/inspect", want: "ws://127.0.0.1:9999/hubfly/v1/inspect"},
		{in: "ws://chat.rt.hubfly.app/hubfly/v1/inspect", err: "unencrypted"},
		{in: "https://chat.rt.hubfly.app/hubfly/v1/inspect", err: "scheme"},
		{in: "wss://chat.rt.hubfly.app/app/key", err: "path"},
		{in: "", err: "invalid"},
	}
	for _, tc := range cases {
		got, err := validateInspectorURL(tc.in)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Fatalf("validateInspectorURL(%q) err = %v, want %q", tc.in, err, tc.err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Fatalf("validateInspectorURL(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
}

func TestAPIOriginStripsPath(t *testing.T) {
	original := apiHost
	t.Cleanup(func() { apiHost = original })
	apiHost = "https://api.hubfly.space/"
	if got, err := apiOrigin(); err != nil || got != "https://api.hubfly.space" {
		t.Fatalf("apiOrigin = %q, %v", got, err)
	}
	apiHost = "http://127.0.0.1:3000/base"
	if got, err := apiOrigin(); err != nil || got != "http://127.0.0.1:3000" {
		t.Fatalf("apiOrigin = %q, %v", got, err)
	}
}

func TestRealtimeTailCommandUsesDashboardTokenAndAPIOrigin(t *testing.T) {
	env := setupRealtimeTest(t)
	insp := newFakeInspector(t)
	insp.script = func(n int, conn *gws.Conn) {
		inspectorReady(conn, n)
		insp.readFilter(conn)
		_ = conn.WriteJSON(map[string]any{"type": "event", "seq": 1, "t": 1760600000000, "app_id": "1000001", "family": "msg", "kind": "msg.publish", "channels": []string{"orders"}, "event": "e", "preview": "{\"x\":1}"})
		drainUntilClosed(conn)
	}
	tokenPath := rtTestBase + "/realtime/inspector/token"
	env.dash.on("POST", tokenPath, func(request rtRecorded) (int, any) {
		return 200, map[string]any{"token": "tok-cmd", "url": insp.url(), "expiresAt": "2026-10-17T10:00:00Z",
			"origin": request.Body["origin"], "apps": []string{"1000001"}, "payloadPreview": true}
	})
	ctx, cancel := context.WithCancel(context.Background())
	rtTailContext = func() (context.Context, context.CancelFunc) { return ctx, cancel }
	done := make(chan error, 1)
	go func() {
		done <- run([]string{"rt", "tail", "chat", "--app", "production", "--family", "MSG", "--channel-prefix", "ord", "--preview", "--json"})
	}()
	waitForCondition(t, "the event line", func() bool { return strings.Contains(env.out.String(), "msg.publish") })
	cancel()
	if err := waitTailResult(t, done); err != nil {
		t.Fatalf("tail command returned %v", err)
	}

	dashOrigin := env.dash.server.URL
	tokens := env.dash.find("POST", tokenPath)
	wantBody := map[string]any{"origin": dashOrigin, "apps": []any{"1000001"}, "payloadPreview": true}
	if len(tokens) != 1 || !reflect.DeepEqual(tokens[0].Body, wantBody) {
		t.Fatalf("token requests = %+v, want body %v", tokens, wantBody)
	}
	origins, queries, auths, filters := insp.snapshot()
	if !reflect.DeepEqual(origins, []string{dashOrigin}) || !reflect.DeepEqual(queries, []string{""}) {
		t.Fatalf("origins=%q queries=%q", origins, queries)
	}
	if len(auths) != 1 || auths[0]["token"] != "tok-cmd" || auths[0]["type"] != "auth" {
		t.Fatalf("auth frames = %v", auths)
	}
	wantFilter := map[string]any{"type": "filter", "apps": []any{"1000001"}, "families": []any{"msg"}, "channel_prefix": "ord"}
	if len(filters) != 1 || !reflect.DeepEqual(filters[0], wantFilter) {
		t.Fatalf("filters = %v", filters)
	}
	var line map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(env.out.String())), &line); err != nil || line["preview"] != "{\"x\":1}" {
		t.Fatalf("--json output %q (%v)", env.out.String(), err)
	}
}

func TestFormatRealtimeTailEventOrdersFields(t *testing.T) {
	var event map[string]any
	decoder := json.NewDecoder(strings.NewReader(`{"type":"event","seq":3,"t":1760600000123,"app_id":"7","family":"msg","kind":"msg.send","preview":"hi there","targets":["1.2"],"results":{"a":1},"size":2}`))
	decoder.UseNumber()
	if err := decoder.Decode(&event); err != nil {
		t.Fatal(err)
	}
	got := formatRealtimeTailEvent(event)
	stamp := time.UnixMilli(1760600000123).Format("15:04:05.000")
	want := stamp + ` 7 msg.send results={"a":1} size=2 targets=1.2 preview="hi there"`
	if got != want {
		t.Fatalf("formatRealtimeTailEvent = %q, want %q", got, want)
	}
}

// M4: an end user controls event names, channels and previews, and the
// server's error messages reach notices. None may write escape sequences
// (OSC 52 clipboard writes, CSI screen clears, C1 controls, bidi overrides)
// to the operator's terminal.
const (
	osc52 = "client-\x1b]52;c;aGVsbG8=\x07"
	csi   = "\x1b[2J\x1b[H"
	c1CSI = "\u009b2J"
	bidi  = "evil\u202etxt.exe"
)

func hasTerminalControl(s string) bool {
	for _, r := range s {
		if r == '\n' {
			continue
		}
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) || r == '\u202e' {
			return true
		}
	}
	return false
}

func TestFormatRealtimeTailEventQuotesControlRunes(t *testing.T) {
	event := map[string]any{
		"t":            json.Number("1760600000123"),
		"app_id":       "7" + csi,
		"kind":         "msg.client" + c1CSI,
		"event":        osc52,
		"channels":     []any{"private-" + csi, "ok"},
		"preview":      map[string]any{"x": c1CSI + bidi},
		"user" + osc52: "u_1",
	}
	got := formatRealtimeTailEvent(event)
	if hasTerminalControl(got) {
		t.Fatalf("raw control runes in %q", got)
	}
	for _, want := range []string{
		strconv.Quote("7" + csi),
		strconv.Quote("msg.client" + c1CSI),
		"event=" + strconv.Quote(osc52),
		"channels=" + strconv.Quote("private-"+csi) + ",ok",
		strconv.Quote("user"+osc52) + "=u_1",
		`\u009b2J`,
		`\u202e`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("formatRealtimeTailEvent = %q, missing %q", got, want)
		}
	}
	// Printable text, including non-ASCII, stays as it is.
	if got := formatRealtimeTailValue("café-東京"); got != "café-東京" {
		t.Fatalf("printable text changed: %q", got)
	}
	if got := formatRealtimeTailValue("bad\xffutf8"); got != strconv.Quote("bad\xffutf8") {
		t.Fatalf("invalid UTF-8 not quoted: %q", got)
	}
}

func TestRealtimeTailNeutralizesEscapesInTextJSONAndNotices(t *testing.T) {
	for _, asJSON := range []bool{false, true} {
		insp := newFakeInspector(t)
		insp.script = func(n int, conn *gws.Conn) {
			inspectorReady(conn, n)
			_ = conn.WriteJSON(map[string]any{"type": "event", "seq": 1, "t": 1760600000000, "app_id": "1000001", "family": "msg", "kind": "msg.client", "event": osc52, "channels": []string{"presence-" + c1CSI + bidi}})
			_ = conn.WriteJSON(map[string]any{"type": "error", "message": "bad filter " + csi + osc52})
			drainUntilClosed(conn)
		}
		h := newTailHarness(insp, realtimeTailFilter{}, asJSON)
		cancel, done := runTailInBackground(h.tail)
		waitForCondition(t, "event and error notice", func() bool {
			return strings.Contains(h.out.String(), "\n") && strings.Contains(h.errOut.String(), "inspector error")
		})
		cancel()
		if err := waitTailResult(t, done); err != nil {
			t.Fatalf("tail returned %v", err)
		}
		out, notices := h.out.String(), h.errOut.String()
		if hasTerminalControl(out) || hasTerminalControl(notices) {
			t.Fatalf("json=%v: raw control runes\nout: %q\nnotices: %q", asJSON, out, notices)
		}
		if !strings.Contains(notices, "inspector error: "+strconv.Quote("bad filter "+csi+osc52)) {
			t.Fatalf("notice not quoted: %q", notices)
		}
		if asJSON {
			// Still valid JSON carrying the same values.
			var frame map[string]any
			if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &frame); err != nil {
				t.Fatalf("not JSON: %q (%v)", out, err)
			}
			if frame["event"] != osc52 || !reflect.DeepEqual(frame["channels"], []any{"presence-" + c1CSI + bidi}) {
				t.Fatalf("JSON values changed: %v", frame)
			}
		} else if !strings.Contains(out, "event="+strconv.Quote(osc52)) {
			t.Fatalf("text event not quoted: %q", out)
		}
	}
}

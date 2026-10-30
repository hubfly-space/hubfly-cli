package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	gws "github.com/gorilla/websocket"
)

// fakeStudio stands in for a stream server's studio socket (STUDIO.md): it
// records the Origin header, the URL query, the auth frame and watch frames,
// then runs a per-connection script.
type fakeStudio struct {
	mu      sync.Mutex
	conns   int
	origins []string
	queries []string
	auths   []map[string]any
	watches []map[string]any
	script  func(n int, conn *gws.Conn)
	server  *httptest.Server
}

func newFakeStudio(t *testing.T) *fakeStudio {
	t.Helper()
	f := &fakeStudio{}
	upgrader := gws.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != streamStudioPath {
			http.NotFound(w, r)
			return
		}
		f.mu.Lock()
		f.conns++
		n := f.conns
		f.origins = append(f.origins, r.Header.Get("Origin"))
		f.queries = append(f.queries, r.URL.RawQuery)
		script := f.script
		f.mu.Unlock()
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		var auth map[string]any
		if conn.ReadJSON(&auth) != nil {
			return
		}
		f.mu.Lock()
		f.auths = append(f.auths, auth)
		f.mu.Unlock()
		script(n, conn)
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeStudio) url() string {
	return "ws" + strings.TrimPrefix(f.server.URL, "http") + streamStudioPath
}

func (f *fakeStudio) ready(conn *gws.Conn, n int) {
	_ = conn.WriteJSON(map[string]any{"type": "ready", "tenant": "msi_live", "scopes": []string{"read", "operate"}, "session": fmt.Sprintf("ss_%d", n)})
}

func (f *fakeStudio) readWatch(conn *gws.Conn) {
	var watch map[string]any
	if conn.ReadJSON(&watch) == nil {
		f.mu.Lock()
		f.watches = append(f.watches, watch)
		f.mu.Unlock()
	}
}

func (f *fakeStudio) snapshot() (origins, queries []string, auths, watches []map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.origins...), append([]string(nil), f.queries...),
		append([]map[string]any(nil), f.auths...), append([]map[string]any(nil), f.watches...)
}

func streamLiveFrame(inputState, channelState string) map[string]any {
	return map[string]any{"type": "live", "data": map[string]any{
		"server":   map[string]any{"runState": "running", "viewers": 3},
		"inputs":   []any{map[string]any{"id": "sin_cam1abcd", "name": "Camera", "state": inputState}},
		"channels": []any{map[string]any{"id": "sch_main0001", "state": channelState}},
	}}
}

type streamTailHarness struct {
	tail   *streamTail
	out    *rtSyncBuffer
	errOut *rtSyncBuffer
	mints  func() int
}

func newStreamTailHarness(studio *fakeStudio, watch []string, asJSON bool) streamTailHarness {
	var mu sync.Mutex
	mints := 0
	out, errOut := &rtSyncBuffer{}, &rtSyncBuffer{}
	tail := &streamTail{
		watch: watch, meters: len(watch) > 0, json: asJSON, out: out, errOut: errOut,
		backoff: func(int) time.Duration { return time.Millisecond },
		mint: func(context.Context) (streamStudioToken, error) {
			mu.Lock()
			defer mu.Unlock()
			mints++
			// A token in the URL must be dropped: tokens only travel in the first frame.
			return streamStudioToken{Token: fmt.Sprintf("stok-%d", mints), URL: studio.url() + "?token=never-in-the-url", Scopes: []string{"read"}}, nil
		},
	}
	return streamTailHarness{tail: tail, out: out, errOut: errOut, mints: func() int { mu.Lock(); defer mu.Unlock(); return mints }}
}

func runStreamTailInBackground(tail *streamTail) (context.CancelFunc, chan error) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- tail.run(ctx) }()
	return cancel, done
}

func TestStreamTailPrintsLiveChangesEventsMetersAndReconnects(t *testing.T) {
	studio := newFakeStudio(t)
	studio.script = func(n int, conn *gws.Conn) {
		studio.ready(conn, n)
		studio.readWatch(conn)
		if n == 1 {
			_ = conn.WriteJSON(streamLiveFrame("offline", "offline"))
			_ = conn.WriteJSON(streamLiveFrame("live", "offline"))
			_ = conn.WriteJSON(map[string]any{"type": "event", "data": map[string]any{"at": "2026-10-29T08:00:00Z", "type": "channel.live", "data": map[string]any{"channel": "sch_main0001"}}})
			for i := 0; i < 3; i++ {
				_ = conn.WriteJSON(map[string]any{"type": "meters", "data": map[string]any{"in:sin_cam1abcd": map[string]any{"rms": []float64{-18.2, -17.9}, "peak": []float64{-6.1, -5.8}}}})
			}
			_ = conn.WriteJSON(map[string]any{"type": "error", "code": "bad_frame", "message": "unknown \x1b[31mframe"})
			_ = conn.WriteJSON(map[string]any{"type": "ping"})
			// Drop the connection: tail must reconnect with a new token.
			return
		}
		drainUntilClosed(conn)
	}
	h := newStreamTailHarness(studio, []string{"in:sin_cam1abcd"}, false)
	cancel, done := runStreamTailInBackground(h.tail)
	waitForCondition(t, "the second session", func() bool { _, _, auths, _ := studio.snapshot(); return len(auths) == 2 })
	cancel()
	if err := waitTailResult(t, done); err != nil {
		t.Fatalf("tail returned %v", err)
	}
	origins, queries, auths, watches := studio.snapshot()
	if !reflect.DeepEqual(origins, []string{"", ""}) || !reflect.DeepEqual(queries, []string{"", ""}) {
		t.Fatalf("origins=%q queries=%q (no Origin header, no token in the URL)", origins, queries)
	}
	if auths[0]["type"] != "auth" || auths[0]["token"] != "stok-1" || auths[1]["token"] != "stok-2" {
		t.Fatalf("auth frames = %v", auths)
	}
	if len(watches) < 1 || !reflect.DeepEqual(watches[0], map[string]any{"type": "watch", "sources": []any{"in:sin_cam1abcd"}}) {
		t.Fatalf("watch frames = %v", watches)
	}
	out := h.out.String()
	for _, want := range []string{"input Camera (sin_cam1abcd) offline\n", "channel sch_main0001 offline\n", "server running\n",
		"input Camera (sin_cam1abcd) offline -> live\n", "2026-10-29 08:00:00Z channel.live channel=sch_main0001",
		"meters in:sin_cam1abcd rms -18.2/-17.9 peak -6.1/-5.8 dBFS"} {
		if !strings.Contains(out, want) {
			t.Fatalf("tail output missing %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "meters ") != 1 || strings.Contains(out, "channel sch_main0001 offline -> ") {
		t.Fatalf("meters are throttled and unchanged state is not repeated:\n%s", out)
	}
	errOut := h.errOut.String()
	if !strings.Contains(errOut, "studio connected (session ss_1, scopes read,operate)") || !strings.Contains(errOut, "reconnecting") ||
		strings.Contains(errOut, "\x1b") {
		t.Fatalf("tail notices:\n%q", errOut)
	}
}

func TestStreamTailJSONLinesMaskSecrets(t *testing.T) {
	studio := newFakeStudio(t)
	studio.script = func(n int, conn *gws.Conn) {
		studio.ready(conn, n)
		_ = conn.WriteJSON(map[string]any{"type": "event", "data": map[string]any{"type": "input.connected", "data": map[string]any{"input": "sin_cam1abcd", "reason": "key " + streamTestKey}}})
		drainUntilClosed(conn)
	}
	h := newStreamTailHarness(studio, nil, true)
	cancel, done := runStreamTailInBackground(h.tail)
	waitForCondition(t, "the event line", func() bool { return strings.Contains(h.out.String(), "input.connected") })
	cancel()
	_ = waitTailResult(t, done)
	var line map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(h.out.String())), &line); err != nil || line["type"] != "event" {
		t.Fatalf("--json line %q (%v)", h.out.String(), err)
	}
	assertNoStreamSecret(t, "tail --json", h.out.String())
}

func TestStreamTailFatalCloseCodes(t *testing.T) {
	cases := []struct {
		code int
		want string
	}{
		{streamCloseForbidden, "close 4403"},
		{streamCloseTooManyStudio, "10 studio sessions"},
	}
	for _, tt := range cases {
		studio := newFakeStudio(t)
		studio.script = func(n int, conn *gws.Conn) {
			inspectorClose(conn, tt.code, "no")
			drainUntilClosed(conn)
		}
		h := newStreamTailHarness(studio, nil, false)
		_, done := runStreamTailInBackground(h.tail)
		err := waitTailResult(t, done)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Fatalf("close %d: error = %v", tt.code, err)
		}
	}
	studio := newFakeStudio(t)
	studio.script = func(n int, conn *gws.Conn) {
		inspectorClose(conn, streamCloseBadToken, "bad token")
		drainUntilClosed(conn)
	}
	h := newStreamTailHarness(studio, nil, false)
	_, done := runStreamTailInBackground(h.tail)
	err := waitTailResult(t, done)
	if err == nil || !strings.Contains(err.Error(), "close 4401") || h.mints() != streamTailMaxAuthFails {
		t.Fatalf("bad token: error = %v after %d tokens", err, h.mints())
	}
}

func TestValidateStudioURL(t *testing.T) {
	good, err := validateStudioURL("wss://live-1a2b.live.hubfly.test/v1/studio?token=x")
	if err != nil || good != "wss://live-1a2b.live.hubfly.test/v1/studio" {
		t.Fatalf("wss URL = %q (%v)", good, err)
	}
	for _, bad := range []string{"ws://live.hubfly.test/v1/studio", "https://live.hubfly.test/v1/studio", "wss://live.hubfly.test/other", "::"} {
		if _, err := validateStudioURL(bad); err == nil {
			t.Fatalf("validateStudioURL(%q) accepted", bad)
		}
	}
	if _, err := validateStudioURL("ws://127.0.0.1:8080/v1/studio"); err != nil {
		t.Fatalf("loopback ws refused: %v", err)
	}
}

func TestStreamTailCommandMintsACLIStudioToken(t *testing.T) {
	env := setupStreamTest(t)
	studio := newFakeStudio(t)
	studio.script = func(n int, conn *gws.Conn) {
		studio.ready(conn, n)
		studio.readWatch(conn)
		_ = conn.WriteJSON(streamLiveFrame("live", "live"))
		drainUntilClosed(conn)
	}
	tokenPath := streamTestAPI + "/studio-token/create"
	env.dash.ok("POST", tokenPath, map[string]any{"token": "stok-cmd", "url": studio.url(), "expiresAt": "2026-10-29T10:00:00Z", "scopes": []string{"read"}})
	ctx, cancel := context.WithCancel(context.Background())
	rtTailContext = func() (context.Context, context.CancelFunc) { return ctx, cancel }
	done := make(chan error, 1)
	go func() { done <- run([]string{"live", "tail", "live-show", "--watch", "in:Camera,ch:Main"}) }()
	waitForCondition(t, "the live line", func() bool { return strings.Contains(env.out.String(), "channel sch_main0001 live") })
	cancel()
	if err := waitTailResult(t, done); err != nil {
		t.Fatalf("tail command returned %v", err)
	}
	tokens := env.dash.find("POST", tokenPath)
	if len(tokens) != 1 || !reflect.DeepEqual(tokens[0].Body, map[string]any{"origin": "hubfly-cli"}) {
		t.Fatalf("studio token requests = %+v", tokens)
	}
	_, _, auths, watches := studio.snapshot()
	if len(auths) != 1 || auths[0]["token"] != "stok-cmd" {
		t.Fatalf("auth frames = %v", auths)
	}
	if len(watches) != 1 || !reflect.DeepEqual(watches[0]["sources"], []any{"in:sin_cam1abcd", "ch:sch_main0001"}) {
		t.Fatalf("watch frames = %v", watches)
	}
}

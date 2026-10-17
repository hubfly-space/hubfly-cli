package cli

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	gws "github.com/gorilla/websocket"
)

// `hubfly rt tail` against the REAL inspector socket of hubfly-realtime's
// rt-devserver; only the Dashboard's token endpoint is faked. Tokens are
// minted the way the Dashboard does (engine/src/core/managed/realtime/
// inspector.ts): EdDSA over the same header and claim order, with the
// public key in the engine's config.sync payload. Skipped unless
// HUBFLY_REALTIME_DEVSERVER names an rt-devserver binary:
//
//	(cd hubfly-realtime && go build -o /tmp/rt-devserver ./cmd/rt-devserver)
//	HUBFLY_REALTIME_DEVSERVER=/tmp/rt-devserver go test ./internal/cli -run Live

const (
	rtLiveOrigin   = "https://api.hubfly.test"
	rtLiveOtherOrn = "https://app.hubfly.test"
	rtLiveKid      = "ik_cli"
	rtLiveInstance = "mi_chat" // the fake Dashboard's instance id (token aud)
)

type rtLiveEngine struct {
	t       *testing.T
	bin     string
	dir     string
	config  string
	private ed25519.PrivateKey
	mu      sync.Mutex
	ws      string // host:port of the client listener (kept across restarts)
	admin   string
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	exited  chan struct{}
}

func startRTLiveEngine(t *testing.T) *rtLiveEngine {
	t.Helper()
	bin := os.Getenv("HUBFLY_REALTIME_DEVSERVER")
	if bin == "" {
		t.Skip("HUBFLY_REALTIME_DEVSERVER is not set (path to hubfly-realtime's rt-devserver)")
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	secret := make([]byte, 20)
	_, _ = rand.Read(secret)
	config := map[string]any{
		"generation": 1,
		"instance":   map[string]any{"hostnames": []string{}},
		"inspector": map[string]any{
			"public_keys": []map[string]string{{"kid": rtLiveKid, "key": base64.RawURLEncoding.EncodeToString(public)}},
			"origins":     []string{rtLiveOrigin, rtLiveOtherOrn},
		},
		"apps": []map[string]any{{
			"app_id": "1000001", "name": "production", "enabled": true,
			"settings": map[string]any{"inspector_payload_preview": true},
			"keys":     []map[string]any{{"key": rtTestAppKey, "secret": hex.EncodeToString(secret), "status": "active", "expires_at": nil}},
			"webhooks": []any{},
		}},
	}
	dir := t.TempDir()
	raw, _ := json.Marshal(config)
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	e := &rtLiveEngine{t: t, bin: bin, dir: dir, config: path, private: private}
	e.start("127.0.0.1:0")
	t.Cleanup(e.stop)
	return e
}

// start runs rt-devserver; listen is 127.0.0.1:0 first, then the same port.
func (e *rtLiveEngine) start(listen string) {
	e.t.Helper()
	ready := filepath.Join(e.dir, "ready.json")
	_ = os.Remove(ready)
	cmd := exec.Command(e.bin, "-config", e.config, "-listen", listen, "-admin", "127.0.0.1:0",
		"-instance-id", rtLiveInstance, "-cpu", "2", "-memory-mib", "4096", "-drain-budget", "1s",
		"-ready-file", ready, "-exit-with-stdin", "-log-level", "warn")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		e.t.Fatal(err)
	}
	cmd.Stdout, cmd.Stderr = io.Discard, os.Stderr
	if err := cmd.Start(); err != nil {
		e.t.Fatal(err)
	}
	e.cmd, e.stdin, e.exited = cmd, stdin, make(chan struct{})
	go func(done chan struct{}) { _ = cmd.Wait(); close(done) }(e.exited)
	deadline := time.Now().Add(10 * time.Second)
	for {
		raw, err := os.ReadFile(ready)
		if err == nil && bytes.HasSuffix(raw, []byte("\n")) {
			var line struct {
				Listeners []string `json:"listeners"`
				Admin     string   `json:"admin"`
			}
			if json.Unmarshal(raw, &line) == nil && len(line.Listeners) == 1 {
				e.mu.Lock()
				e.ws, e.admin = line.Listeners[0], line.Admin
				e.mu.Unlock()
				return
			}
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("rt-devserver did not become ready")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// stop drains (SIGTERM: inspector sessions close with 4000) and waits.
func (e *rtLiveEngine) stop() {
	if e.cmd == nil || e.cmd.Process == nil {
		return
	}
	select {
	case <-e.exited:
		return
	default:
	}
	_ = e.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-e.exited:
	case <-time.After(10 * time.Second):
		_ = e.cmd.Process.Kill()
		<-e.exited
	}
	_ = e.stdin.Close()
}

func (e *rtLiveEngine) addrs() (ws, admin string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.ws, e.admin
}

func (e *rtLiveEngine) inspectURL() string {
	ws, _ := e.addrs()
	return "ws://" + ws + realtimeInspectPath
}

func (e *rtLiveEngine) adminURL() string {
	_, admin := e.addrs()
	return "http://" + admin + "/ctl"
}

type rtLiveClaims struct {
	Iss  string   `json:"iss"`
	Aud  string   `json:"aud"`
	Sub  string   `json:"sub"`
	Scp  []string `json:"scp"`
	Apps []string `json:"apps"`
	Pv   bool     `json:"pv"`
	Orn  string   `json:"orn"`
	Iat  int64    `json:"iat"`
	Exp  int64    `json:"exp"`
	Jti  string   `json:"jti"`
}

// mint signs a token like mintRealtimeInspectorToken (header and claim order).
func (e *rtLiveEngine) mint(edit func(*rtLiveClaims)) string {
	jti := make([]byte, 10)
	_, _ = rand.Read(jti)
	now := time.Now().Unix()
	claims := rtLiveClaims{Iss: "hubfly", Aud: rtLiveInstance, Sub: "usr_cli", Scp: []string{"inspect"},
		Apps: []string{"*"}, Orn: rtLiveOrigin, Iat: now, Exp: now + 60, Jti: "it_" + hex.EncodeToString(jti)}
	if edit != nil {
		edit(&claims)
	}
	header := []byte(`{"alg":"EdDSA","typ":"JWT","kid":"` + rtLiveKid + `"}`)
	body, _ := json.Marshal(claims)
	input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(body)
	return input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(e.private, []byte(input)))
}

func (e *rtLiveEngine) ctl(cmd string, args any) map[string]any {
	e.t.Helper()
	raw, _ := json.Marshal(map[string]any{"v": 1, "id": "cli_live", "cmd": cmd, "args": args})
	response, err := http.Post(e.adminURL(), "application/json", bytes.NewReader(raw))
	if err != nil {
		e.t.Fatalf("ctl %s: %v", cmd, err)
	}
	defer response.Body.Close()
	var reply map[string]any
	if err := json.NewDecoder(response.Body).Decode(&reply); err != nil || reply["ok"] != true {
		e.t.Fatalf("ctl %s: %v %v", cmd, reply, err)
	}
	return reply
}

func (e *rtLiveEngine) publish(channel, event, data string) {
	e.ctl("publish", map[string]any{"app_id": "1000001", "event": event, "data": data,
		"channels": []string{channel}, "socket_ids": nil, "user_id": nil})
}

// burst offers ~n inspector events within a second (above the 500/s cap).
func (e *rtLiveEngine) burst(channel string, n int) {
	var wg sync.WaitGroup
	client := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 8}}
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < n/8; i++ {
				raw, _ := json.Marshal(map[string]any{"v": 1, "id": "b", "cmd": "publish", "args": map[string]any{
					"app_id": "1000001", "event": "burst", "data": "x", "channels": []string{channel}, "socket_ids": nil, "user_id": nil}})
				response, err := client.Post(e.adminURL(), "application/json", bytes.NewReader(raw))
				if err == nil {
					_, _ = io.Copy(io.Discard, response.Body)
					response.Body.Close()
				}
			}
		}()
	}
	wg.Wait()
}

// rawSession opens an inspector socket by hand (engine-side checks).
func (e *rtLiveEngine) rawSession(origin, token string) (*gws.Conn, map[string]any, error) {
	header := http.Header{}
	header.Set("Origin", origin)
	conn, _, err := gws.DefaultDialer.Dial(e.inspectURL(), header)
	if err != nil {
		return nil, nil, err
	}
	if token != "" {
		_ = conn.WriteJSON(map[string]string{"type": "auth", "token": token})
	}
	_ = conn.SetReadDeadline(time.Now().Add(8 * time.Second))
	var frame map[string]any
	if err := conn.ReadJSON(&frame); err != nil {
		conn.Close()
		return nil, nil, err
	}
	return conn, frame, nil
}

// rtLiveTokenRoute is the fake Dashboard's POST inspector/token: it mints a
// real token for the engine; `edit` changes claims (and the returned origin).
type rtLiveTokenRoute struct {
	mu     sync.Mutex
	mints  int
	bodies []map[string]any
	edit   func(*rtLiveClaims)
}

func (r *rtLiveTokenRoute) install(env rtTestEnv, engine *rtLiveEngine) {
	env.dash.on("POST", rtTestBase+"/realtime/inspector/token", func(request rtRecorded) (int, any) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.mints++
		r.bodies = append(r.bodies, request.Body)
		preview := request.Body["payloadPreview"] == true
		var claims rtLiveClaims
		token := engine.mint(func(c *rtLiveClaims) {
			c.Pv = preview
			if apps, ok := request.Body["apps"].([]any); ok && len(apps) > 0 {
				c.Apps = nil
				for _, app := range apps {
					c.Apps = append(c.Apps, fmt.Sprint(app))
				}
			}
			if r.edit != nil {
				r.edit(c)
			}
			claims = *c
		})
		return 200, map[string]any{"token": token, "url": engine.inspectURL(), "expiresAt": time.Unix(claims.Exp, 0).UTC().Format(time.RFC3339),
			"origin": claims.Orn, "apps": claims.Apps, "payloadPreview": claims.Pv}
	})
}

func (r *rtLiveTokenRoute) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.mints
}

func waitLive(t *testing.T, what string, within time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func jsonLines(text string) []map[string]any {
	lines := []map[string]any{}
	scanner := bufio.NewScanner(strings.NewReader(text))
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		var line map[string]any
		if json.Unmarshal(scanner.Bytes(), &line) == nil {
			lines = append(lines, line)
		}
	}
	return lines
}

func runLiveTail(args ...string) (context.CancelFunc, chan error) {
	ctx, cancel := context.WithCancel(context.Background())
	rtTailContext = func() (context.Context, context.CancelFunc) { return ctx, cancel }
	done := make(chan error, 1)
	go func() { done <- run(append([]string{"rt", "tail", "chat"}, args...)) }()
	return cancel, done
}

func waitLiveResult(t *testing.T, done chan error, within time.Duration) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(within):
		t.Fatal("tail did not return")
		return nil
	}
}

func TestRealtimeTailLiveEventsFiltersSamplingAndReconnect(t *testing.T) {
	engine := startRTLiveEngine(t)
	env := setupRealtimeTest(t)
	route := &rtLiveTokenRoute{}
	route.install(env, engine)

	cancel, done := runLiveTail("--app", "production", "--family", "msg", "--channel-prefix", "orders", "--preview", "--json")
	defer cancel()
	waitLive(t, "the inspector session", 10*time.Second, func() bool {
		return strings.Contains(env.errOut.String(), "inspector connected (apps 1000001")
	})
	engine.publish("orders-1", "first", `{"n":1}`)
	engine.publish("misc", "filtered-out", `{"n":2}`)
	engine.publish("orders-2", "second", `{"n":3}`)
	waitLive(t, "the second event", 5*time.Second, func() bool { return strings.Contains(env.out.String(), `"second"`) })
	var events []map[string]any
	for _, line := range jsonLines(env.out.String()) {
		if line["type"] == "event" {
			events = append(events, line)
		}
	}
	if len(events) != 2 {
		t.Fatalf("events = %v (filter must drop misc)", events)
	}
	first := events[0]
	for key, want := range map[string]any{"family": "msg", "kind": "msg.publish", "app_id": "1000001", "event": "first",
		"source": "console", "encrypted": false, "preview": `{"n":1}`} {
		if first[key] != want {
			t.Fatalf("event %s = %v, want %v (%v)", key, first[key], want, first)
		}
	}
	if channels, _ := first["channels"].([]any); len(channels) != 1 || channels[0] != "orders-1" {
		t.Fatalf("channels = %v", first["channels"])
	}
	// The request asked for previews and the token granted them.
	if body := route.bodies[0]; body["payloadPreview"] != true || body["origin"] != env.dash.server.URL {
		t.Fatalf("token request = %v", body)
	}

	// Above 500 events/s the engine drops and then samples; both frames reach
	// the output (JSON lines) and the stderr notices.
	engine.burst("orders-burst", 2400)
	// The engine announces sampling on the first event of the next window
	// (if it comes within 2 s); tick until both frames have arrived.
	waitLive(t, "dropped and sampling frames", 8*time.Second, func() bool {
		out := env.out.String()
		seen := strings.Contains(out, `"type":"dropped"`) && strings.Contains(out, `"type":"sampling"`)
		if !seen {
			engine.publish("orders-tick", "tick", "{}")
			time.Sleep(140 * time.Millisecond)
		}
		return seen
	})
	for _, line := range jsonLines(env.out.String()) {
		switch line["type"] {
		case "dropped":
			if count, _ := line["count"].(float64); count < 1 {
				t.Fatalf("dropped frame %v", line)
			}
		case "sampling":
			if rate, _ := line["rate"].(float64); rate <= 0 || rate > 1 {
				t.Fatalf("sampling frame %v", line)
			}
		}
	}
	notices := env.errOut.String()
	if !strings.Contains(notices, "events dropped") || !strings.Contains(notices, "sampling: showing about") {
		t.Fatalf("notices = %q", notices)
	}

	// A drain closes the session with 4000; tail mints a new token and
	// reconnects once the server is back on the same address.
	mintsBefore := route.count()
	engine.stop()
	waitLive(t, "the 4000 notice", 5*time.Second, func() bool {
		return strings.Contains(env.errOut.String(), "inspector session ended (expired or server draining)")
	})
	ws, _ := engine.addrs()
	engine.start(ws)
	waitLive(t, "the reconnect", 15*time.Second, func() bool {
		return strings.Count(env.errOut.String(), "inspector connected") >= 2
	})
	if route.count() < mintsBefore+1 {
		t.Fatalf("mints %d -> %d: reconnect must use a fresh token", mintsBefore, route.count())
	}
	engine.publish("orders-4", "after-reconnect", "{}")
	waitLive(t, "an event after the reconnect", 5*time.Second, func() bool {
		return strings.Contains(env.out.String(), `"after-reconnect"`)
	})
	cancel()
	if err := waitLiveResult(t, done, 5*time.Second); err != nil {
		t.Fatalf("tail returned %v", err)
	}
}

func TestRealtimeTailLiveWithoutPreviewAndTextOutput(t *testing.T) {
	engine := startRTLiveEngine(t)
	env := setupRealtimeTest(t)
	route := &rtLiveTokenRoute{}
	route.install(env, engine)
	cancel, done := runLiveTail("--family", "msg")
	defer cancel()
	waitLive(t, "the inspector session", 10*time.Second, func() bool {
		return strings.Contains(env.errOut.String(), "inspector connected (all apps")
	})
	engine.publish("orders", "plain", `{"secret":"payload"}`)
	waitLive(t, "the event line", 5*time.Second, func() bool { return strings.Contains(env.out.String(), "msg.publish") })
	line := env.out.String()
	if !strings.Contains(line, " 1000001 msg.publish ") || !strings.Contains(line, "event=plain") ||
		!strings.Contains(line, "channels=orders") || strings.Contains(line, "payload") {
		t.Fatalf("text line %q (pv=false: no preview)", line)
	}
	cancel()
	if err := waitLiveResult(t, done, 5*time.Second); err != nil {
		t.Fatalf("tail returned %v", err)
	}
}

func TestRealtimeTailLiveFatalCloseCodes(t *testing.T) {
	engine := startRTLiveEngine(t)

	t.Run("4403 origin not allowed", func(t *testing.T) {
		env := setupRealtimeTest(t)
		route := &rtLiveTokenRoute{edit: func(c *rtLiveClaims) { c.Orn = "https://evil.hubfly.test" }}
		route.install(env, engine)
		_, done := runLiveTail()
		err := waitLiveResult(t, done, 10*time.Second)
		if err == nil || !strings.Contains(err.Error(), "close 4403") || route.count() != 1 {
			t.Fatalf("err = %v, mints = %d", err, route.count())
		}
	})

	t.Run("4401 expired token three times", func(t *testing.T) {
		env := setupRealtimeTest(t)
		route := &rtLiveTokenRoute{edit: func(c *rtLiveClaims) { c.Iat, c.Exp = c.Iat-300, c.Iat-240 }}
		route.install(env, engine)
		_, done := runLiveTail()
		err := waitLiveResult(t, done, 15*time.Second)
		if err == nil || !strings.Contains(err.Error(), "close 4401") || route.count() != realtimeTailMaxAuthFails {
			t.Fatalf("err = %v, mints = %d", err, route.count())
		}
	})

	t.Run("4429 when 5 sessions are open", func(t *testing.T) {
		env := setupRealtimeTest(t)
		route := &rtLiveTokenRoute{}
		route.install(env, engine)
		for i := 0; i < 5; i++ {
			conn, frame, err := engine.rawSession(rtLiveOrigin, engine.mint(nil))
			if err != nil || frame["type"] != "ready" {
				t.Fatalf("session %d: %v %v", i, frame, err)
			}
			defer conn.Close()
		}
		_, done := runLiveTail()
		err := waitLiveResult(t, done, 10*time.Second)
		if err == nil || !strings.Contains(err.Error(), "close 4429") || route.count() != 1 {
			t.Fatalf("err = %v, mints = %d", err, route.count())
		}
	})
}

// Engine-side token rules (PROTOCOL.md "Inspector tokens", WIRE.md close codes).
func TestRealtimeInspectorLiveTokenRules(t *testing.T) {
	engine := startRTLiveEngine(t)
	closeCode := func(conn *gws.Conn, frame map[string]any, err error) int {
		if conn != nil {
			conn.Close()
			return 0
		}
		return realtimeCloseCode(err)
	}

	token := engine.mint(nil)
	conn, frame, err := engine.rawSession(rtLiveOrigin, token)
	if err != nil || frame["type"] != "ready" || frame["session_id"] == nil {
		t.Fatalf("first use: %v %v", frame, err)
	}
	conn.Close()
	if code := closeCode(engine.rawSession(rtLiveOrigin, token)); code != 4401 {
		t.Fatalf("replayed jti: close %d, want 4401", code)
	}
	wrongOrn := engine.mint(func(c *rtLiveClaims) { c.Orn = rtLiveOtherOrn })
	if code := closeCode(engine.rawSession(rtLiveOrigin, wrongOrn)); code != 4401 {
		t.Fatalf("orn != Origin: close %d, want 4401", code)
	}
	expired := engine.mint(func(c *rtLiveClaims) { c.Iat, c.Exp = c.Iat-300, c.Iat-240 })
	if code := closeCode(engine.rawSession(rtLiveOrigin, expired)); code != 4401 {
		t.Fatalf("expired: close %d, want 4401", code)
	}
	tooLong := engine.mint(func(c *rtLiveClaims) { c.Exp = c.Iat + 121 })
	if code := closeCode(engine.rawSession(rtLiveOrigin, tooLong)); code != 4401 {
		t.Fatalf("lifetime > 120 s: close %d, want 4401", code)
	}
	otherAud := engine.mint(func(c *rtLiveClaims) { c.Aud = "msi_other" })
	if code := closeCode(engine.rawSession(rtLiveOrigin, otherAud)); code != 4401 {
		t.Fatalf("aud of another instance: close %d, want 4401", code)
	}
	if code := closeCode(engine.rawSession("https://evil.hubfly.test", engine.mint(nil))); code != 4403 {
		t.Fatalf("Origin not allowed: close %d, want 4403", code)
	}
	if code := closeCode(engine.rawSession(rtLiveOrigin, "")); code != 4408 {
		t.Fatalf("no auth frame: close %d, want 4408", code)
	}

	// pv decides previews (the app allows them).
	preview := func(pv bool) any {
		conn, frame, err := engine.rawSession(rtLiveOrigin, engine.mint(func(c *rtLiveClaims) { c.Pv = pv }))
		if err != nil || frame["type"] != "ready" {
			t.Fatalf("pv=%v: %v %v", pv, frame, err)
		}
		defer conn.Close()
		engine.publish("pv-check", fmt.Sprintf("pv-%v", pv), `{"p":1}`)
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		for {
			var event map[string]any
			if err := conn.ReadJSON(&event); err != nil {
				t.Fatalf("pv=%v: %v", pv, err)
			}
			if event["kind"] == "msg.publish" && event["event"] == fmt.Sprintf("pv-%v", pv) {
				return event["preview"]
			}
		}
	}
	if got := preview(false); got != nil {
		t.Fatalf("pv=false preview %v", got)
	}
	if got := preview(true); got != `{"p":1}` {
		t.Fatalf("pv=true preview %v", got)
	}
}

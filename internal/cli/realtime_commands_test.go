package cli

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Fake Dashboard
// ---------------------------------------------------------------------------

type rtRecorded struct {
	Method string
	Path   string
	Query  url.Values
	Body   map[string]any
	Raw    string
}

type rtFakeDashboard struct {
	mu       sync.Mutex
	requests []rtRecorded
	routes   map[string]func(rtRecorded) (int, any)
	server   *httptest.Server
}

func newRTFakeDashboard(t *testing.T) *rtFakeDashboard {
	t.Helper()
	f := &rtFakeDashboard{routes: map[string]func(rtRecorded) (int, any){}}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		record := rtRecorded{Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(), Raw: r.URL.String() + " " + string(raw)}
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &record.Body)
		}
		f.mu.Lock()
		f.requests = append(f.requests, record)
		handler := f.routes[r.Method+" "+r.URL.Path]
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if handler == nil {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": map[string]any{"code": "NOT_FOUND", "message": "no route " + r.Method + " " + r.URL.Path}})
			return
		}
		status, data := handler(record)
		w.WriteHeader(status)
		if status >= 400 {
			_ = json.NewEncoder(w).Encode(data)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": data})
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *rtFakeDashboard) on(method, path string, handler func(rtRecorded) (int, any)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes[method+" "+path] = handler
}

func (f *rtFakeDashboard) ok(method, path string, data any) {
	f.on(method, path, func(rtRecorded) (int, any) { return http.StatusOK, data })
}

func (f *rtFakeDashboard) find(method, path string) []rtRecorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	found := []rtRecorded{}
	for _, request := range f.requests {
		if request.Method == method && request.Path == path {
			found = append(found, request)
		}
	}
	return found
}

func (f *rtFakeDashboard) allRaw() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	parts := []string{}
	for _, request := range f.requests {
		parts = append(parts, request.Raw)
	}
	return strings.Join(parts, "\n")
}

type rtSyncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *rtSyncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *rtSyncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

const (
	rtTestBase   = "/api/v1/projects/p-man/managed/instances/mi_chat"
	rtTestAppKey = "abcdefghij0123456789"
)

func rtTestApp() map[string]any {
	return map[string]any{
		"appId": "1000001", "instanceId": "mi_chat", "name": "production", "enabled": true, "source": "user",
		"settings": map[string]any{
			"allowedOrigins": []string{}, "clientEvents": false, "encryption": "optional",
			"history": map[string]any{"mode": "memory", "ttlSeconds": 3600, "maxMessages": 100},
		},
		"settingsRevision": 3,
		"keys": []map[string]any{{
			"id": "rk_1", "appId": "1000001", "key": rtTestAppKey, "secretHint": "…wxyz",
			"secretFingerprint": "fp1", "status": "active", "createdAt": "2026-10-17T08:00:00Z",
		}},
		"createdAt": "2026-10-17T08:00:00Z", "updatedAt": "2026-10-17T08:00:00Z",
	}
}

func rtTestInstance(id, name, service string) map[string]any {
	return map[string]any{
		"id": id, "name": name, "service": service, "engine": "realtime", "version": "1", "versionLabel": "Realtime 1",
		"status": "running", "size": map[string]any{"cpu": 0.5, "memoryMib": 1024, "storageGib": 1},
		"price":     map[string]any{"currency": "USD", "hourly": map[string]any{"total": 0.0052}, "monthly": map[string]any{"total": 3.82}},
		"createdAt": "2026-10-17T08:00:00Z",
	}
}

func installRealtimeFixtures(f *rtFakeDashboard) {
	f.ok("GET", "/api/v1/auth/me", map[string]any{"id": "u1", "name": "Tester", "email": "tester@example.test"})
	f.ok("GET", "/api/v1/projects", map[string]any{
		"items": []map[string]any{
			{"id": "p-man", "name": "rt-project", "type": "managed"},
			{"id": "p-web", "name": "web", "type": "cell"},
		},
		"page": 1, "pageCount": 1,
	})
	f.ok("GET", "/api/v1/projects/p-man/managed/instances", map[string]any{"items": []map[string]any{
		rtTestInstance("mi_chat", "chat", "realtime"),
		rtTestInstance("mi_pg", "pg", "database"),
	}})
	detail := rtTestInstance("mi_chat", "chat", "realtime")
	detail["realtime"] = map[string]any{
		"host": "chat-1a2b.rt.hubfly.test", "wsUrl": "wss://chat-1a2b.rt.hubfly.test", "httpUrl": "https://chat-1a2b.rt.hubfly.test",
		"apps": 1, "profile": map[string]any{"maxConnections": 7500},
		"sync": map[string]any{"generation": 4, "appliedGeneration": 4, "inSync": true},
	}
	f.ok("GET", rtTestBase, detail)
	f.ok("GET", rtTestBase+"/realtime/apps", map[string]any{"items": []map[string]any{rtTestApp()}})
	f.ok("GET", rtTestBase+"/realtime/connection", map[string]any{
		"host": "chat-1a2b.rt.hubfly.test", "wsUrl": "wss://chat-1a2b.rt.hubfly.test", "httpUrl": "https://chat-1a2b.rt.hubfly.test",
		"port": 443, "ready": true,
		"apps": []map[string]any{{"appId": "1000001", "name": "production", "enabled": true, "key": rtTestAppKey, "encryption": "optional"}},
	})
}

type rtTestEnv struct {
	dash   *rtFakeDashboard
	out    *rtSyncBuffer
	errOut *rtSyncBuffer
}

func setupRealtimeTest(t *testing.T) rtTestEnv {
	t.Helper()
	t.Setenv("HUBFLY_TOKEN", "hf_test_token")
	t.Setenv("HUBFLY_SUBACCOUNT", "")
	dash := newRTFakeDashboard(t)
	installRealtimeFixtures(dash)
	env := rtTestEnv{dash: dash, out: &rtSyncBuffer{}, errOut: &rtSyncBuffer{}}

	origAPI, origOut, origErr := apiHost, rtOut, rtErr
	origInteractive, origStdin, origPoll := rtInteractive, stdin, rtPollInterval
	origRTStdin, origStdinTerm, origStderrTerm := rtStdin, rtStdinIsTerminal, rtStderrIsTerminal
	origTailContext, origDebug := rtTailContext, debugEnabled
	t.Cleanup(func() {
		apiHost, rtOut, rtErr = origAPI, origOut, origErr
		rtInteractive, stdin, rtPollInterval = origInteractive, origStdin, origPoll
		rtStdin, rtStdinIsTerminal, rtStderrIsTerminal = origRTStdin, origStdinTerm, origStderrTerm
		rtTailContext, debugEnabled = origTailContext, origDebug
	})
	apiHost = dash.server.URL
	rtOut, rtErr = env.out, env.errOut
	rtInteractive = func() bool { return false }
	rtPollInterval = time.Millisecond
	rtStdinIsTerminal = func() bool { return true }
	rtStderrIsTerminal = func() bool { return false }
	return env
}

func setTestStdin(text string) {
	stdin = bufio.NewReader(strings.NewReader(text))
}

func mustRun(t *testing.T, args ...string) {
	t.Helper()
	if err := run(args); err != nil {
		t.Fatalf("run(%q) error = %v", args, err)
	}
}

// ---------------------------------------------------------------------------
// Parsing
// ---------------------------------------------------------------------------

func TestParseRealtimeArgsAllowsFlagsBetweenPositionals(t *testing.T) {
	fs := newRealtimeFlags("test")
	app := fs.String("app", "", "")
	yes := fs.Bool("yes", false, "")
	positionals, err := parseRealtimeArgs(fs, []string{"chat", "--app=production", "key-1", "--yes", "--", "--literal"}, "usage")
	if err != nil {
		t.Fatalf("parseRealtimeArgs error = %v", err)
	}
	if want := []string{"chat", "key-1", "--literal"}; !reflect.DeepEqual(positionals, want) {
		t.Fatalf("positionals = %q, want %q", positionals, want)
	}
	if *app != "production" || !*yes {
		t.Fatalf("flags app=%q yes=%v", *app, *yes)
	}
	if _, err := parseRealtimeArgs(newRealtimeFlags("test"), []string{"--nope"}, "hubfly realtime x"); err == nil || !strings.Contains(err.Error(), "usage: hubfly realtime x") {
		t.Fatalf("unknown flag error = %v", err)
	}
}

func TestParseRealtimeDuration(t *testing.T) {
	cases := map[string]int{"90": 90, "90s": 90, "15m": 900, "24h": 86400, "7d": 604800, "1h30m": 5400}
	for input, want := range cases {
		got, err := parseRealtimeDuration(input)
		if err != nil || got != want {
			t.Fatalf("parseRealtimeDuration(%q) = %d, %v; want %d", input, got, err, want)
		}
	}
	for _, input := range []string{"", "soon", "-5", "-1h"} {
		if _, err := parseRealtimeDuration(input); err == nil {
			t.Fatalf("parseRealtimeDuration(%q) succeeded", input)
		}
	}
	if got := formatRealtimeSeconds(86400); got != "1d" {
		t.Fatalf("formatRealtimeSeconds(86400) = %q", got)
	}
}

func TestRealtimeCommandValidationHappensBeforeAnyRequest(t *testing.T) {
	env := setupRealtimeTest(t)
	tests := []struct {
		args    []string
		wantErr string
	}{
		{[]string{"rt", "bogus"}, "unknown realtime command"},
		{[]string{"realtime", "info"}, "usage: hubfly realtime info"},
		{[]string{"rt", "create"}, "usage: hubfly realtime create"},
		{[]string{"rt", "create", "chat", "--cpu", "1"}, "pass --cpu and --memory together"},
		{[]string{"rt", "create", "chat", "--size", "small", "--cpu", "1", "--memory", "512"}, "not both"},
		{[]string{"rt", "delete"}, "usage: hubfly realtime delete"},
		{[]string{"rt", "apps"}, "usage: hubfly realtime apps"},
		{[]string{"rt", "apps", "create", "chat"}, "usage: hubfly realtime apps create"},
		{[]string{"rt", "apps", "update", "chat", "production"}, "nothing to update"},
		{[]string{"rt", "apps", "update", "chat", "production", "--enable", "--disable"}, "not both"},
		{[]string{"rt", "apps", "create", "chat", "staging", "--encryption", "maybe"}, "invalid --encryption"},
		{[]string{"rt", "apps", "create", "chat", "staging", "--history", "forever"}, "invalid --history"},
		{[]string{"rt", "apps", "create", "chat", "staging", "--client-events=perhaps"}, "invalid arguments"},
		{[]string{"rt", "keys", "rotate", "chat", "--grace", "soon"}, "invalid --grace"},
		{[]string{"rt", "keys", "revoke", "chat"}, "usage: hubfly realtime keys revoke"},
		{[]string{"rt", "bind", "chat"}, "usage: hubfly realtime bind"},
		{[]string{"rt", "bind", "chat", "--to", "web", "--client-prefix", "NUXT_PUBLIC"}, "invalid --client-prefix"},
		{[]string{"rt", "bind", "chat", "--to", "web", "--preset", "socketio"}, "invalid --preset"},
		{[]string{"rt", "env", "chat", "--format", "yaml"}, "invalid --format"},
		{[]string{"rt", "publish", "chat"}, "usage: hubfly realtime publish"},
		{[]string{"rt", "publish", "chat", "orders"}, "usage: hubfly realtime publish"},
		{[]string{"rt", "send", "chat", "--event", "ping"}, "not both or neither"},
		{[]string{"rt", "send", "chat", "--event", "ping", "--user", "u1", "--connection", "1234567890.1234567890"}, "not both or neither"},
		{[]string{"rt", "send", "chat", "--event", "ping", "--connection", "not-a-socket~secretpart"}, "invalid connection 1"},
		{[]string{"rt", "tail", "chat", "--family", "bogus"}, "invalid --family"},
		{[]string{"rt", "insights", "chat", "--window", "2d"}, "invalid --window"},
		{[]string{"rt", "keygen", "--kid", "c"}, "invalid --kid"},
		{[]string{"rt", "keygen", "--kid", "has space"}, "invalid --kid"},
	}
	for _, tt := range tests {
		err := run(tt.args)
		if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
			t.Fatalf("run(%q) error = %v, want substring %q", tt.args, err, tt.wantErr)
		}
		if strings.Contains(err.Error(), "secretpart") {
			t.Fatalf("run(%q) echoed an E2E connection id: %v", tt.args, err)
		}
		if code := exitCodeForError(err); code != 2 && !strings.Contains(tt.wantErr, "unknown") {
			t.Fatalf("run(%q) exit code = %d, want 2 (%v)", tt.args, code, err)
		}
	}
	if raw := env.dash.allRaw(); raw != "" {
		t.Fatalf("validation errors made requests:\n%s", raw)
	}
}

func TestRealtimeHelpPrintsUsage(t *testing.T) {
	env := setupRealtimeTest(t)
	mustRun(t, "rt")
	for _, want := range []string{"hubfly realtime create", "hubfly realtime tail", "hubfly realtime keygen", `"rt" is an alias`} {
		if !strings.Contains(env.out.String(), want) {
			t.Fatalf("usage missing %q:\n%s", want, env.out.String())
		}
	}
}

// ---------------------------------------------------------------------------
// list / info / create / delete
// ---------------------------------------------------------------------------

func TestRealtimeListShowsOnlyRealtimeServers(t *testing.T) {
	env := setupRealtimeTest(t)
	mustRun(t, "rt", "list", "--json")
	var listed []rtInstance
	if err := json.Unmarshal([]byte(env.out.String()), &listed); err != nil {
		t.Fatalf("list --json output is not JSON: %v\n%s", err, env.out.String())
	}
	if len(listed) != 1 || listed[0].ID != "mi_chat" || listed[0].ProjectName != "rt-project" || listed[0].ProjectID != "p-man" {
		t.Fatalf("listed = %+v", listed)
	}
	if got := env.dash.find("GET", "/api/v1/projects/p-web/managed/instances"); len(got) != 0 {
		t.Fatalf("listed instances of a cell project: %d requests", len(got))
	}

	env.out = &rtSyncBuffer{}
	rtOut = env.out
	mustRun(t, "realtime", "list")
	text := env.out.String()
	if !strings.Contains(text, "chat") || !strings.Contains(text, "$3.82/month") || strings.Contains(text, "pg") {
		t.Fatalf("list table:\n%s", text)
	}
}

func TestRealtimeInfoPrintsEndpointAndApps(t *testing.T) {
	env := setupRealtimeTest(t)
	mustRun(t, "rt", "info", "CHAT")
	text := env.out.String()
	for _, want := range []string{"wss://chat-1a2b.rt.hubfly.test", "https://chat-1a2b.rt.hubfly.test", "7,500", "in sync", "1000001", rtTestAppKey} {
		if !strings.Contains(text, want) {
			t.Fatalf("info output missing %q:\n%s", want, text)
		}
	}
}

func TestResolveRealtimeInstanceAmbiguousName(t *testing.T) {
	env := setupRealtimeTest(t)
	env.dash.ok("GET", "/api/v1/projects", map[string]any{
		"items": []map[string]any{{"id": "p-man", "name": "rt-project", "type": "managed"}, {"id": "p-two", "name": "two", "type": "managed"}},
		"page":  1, "pageCount": 1,
	})
	env.dash.ok("GET", "/api/v1/projects/p-two/managed/instances", map[string]any{"items": []map[string]any{rtTestInstance("mi_other", "chat", "realtime")}})
	_, err := resolveRealtimeInstance("hf_test_token", "chat", "")
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("expected ambiguity error, got %v", err)
	}
	instance, err := resolveRealtimeInstance("hf_test_token", "chat", "two")
	if err != nil || instance.ID != "mi_other" {
		t.Fatalf("resolve with --project = %+v, %v", instance, err)
	}
	instance, err = resolveRealtimeInstance("hf_test_token", "mi_chat", "")
	if err != nil || instance.ID != "mi_chat" {
		t.Fatalf("resolve by id = %+v, %v", instance, err)
	}
}

func installRealtimeCreateFixtures(env rtTestEnv) {
	env.dash.ok("GET", "/api/v1/managed/catalog", map[string]any{"realtime": map[string]any{
		"available": true,
		"sizes": []map[string]any{
			{"id": "nano", "label": "Nano", "cpu": 0.25, "memoryMib": 512, "profile": map[string]any{"maxConnections": 2500}},
			{"id": "small", "label": "Small", "cpu": 0.5, "memoryMib": 1024, "profile": map[string]any{"maxConnections": 7500}},
		},
		"storageGib":      map[string]any{"min": 1, "max": 100, "default": 1},
		"keyGraceSeconds": 86400,
	}})
	env.dash.ok("GET", "/api/v1/pricing/managed/estimate", map[string]any{
		"currency": "USD", "hoursPerMonth": 730,
		"hourly":  map[string]any{"total": 0.0052},
		"monthly": map[string]any{"total": 3.82},
	})
	created := rtTestInstance("mi_new", "chat2", "realtime")
	created["status"] = "provisioning"
	env.dash.ok("POST", "/api/v1/projects/p-man/managed/instances/create", map[string]any{
		"instance": created, "operationId": "op_1",
		"price":      map[string]any{"monthly": map[string]any{"total": 3.82}, "hourly": map[string]any{"total": 0.0052}},
		"initialApp": map[string]any{"appId": "1000001", "name": "production", "keyId": "rk_1", "key": rtTestAppKey},
	})
	polls := 0
	var pollsMu sync.Mutex
	env.dash.on("GET", "/api/v1/projects/p-man/managed/instances/mi_new/operations/op_1", func(rtRecorded) (int, any) {
		pollsMu.Lock()
		defer pollsMu.Unlock()
		polls++
		status, step := "running", "running"
		if polls >= 2 {
			status, step = "succeeded", "succeeded"
		}
		return 200, map[string]any{"id": "op_1", "kind": "create", "status": status,
			"steps": []map[string]any{{"key": "prepare", "label": "Prepare", "status": "succeeded"}, {"key": "start", "label": "Start engine", "status": step}}}
	})
	env.dash.ok("GET", "/api/v1/projects/p-man/managed/instances/mi_new", map[string]any{
		"id": "mi_new", "name": "chat2", "service": "realtime", "status": "running",
		"realtime": map[string]any{"wsUrl": "wss://chat2.rt.hubfly.test", "httpUrl": "https://chat2.rt.hubfly.test"},
	})
}

func TestRealtimeCreateQuotesPriceSendsQuoteAndPolls(t *testing.T) {
	env := setupRealtimeTest(t)
	installRealtimeCreateFixtures(env)
	mustRun(t, "rt", "create", "chat2", "--size", "small", "--project", "rt-project", "--app", "main", "--yes")

	estimates := env.dash.find("GET", "/api/v1/pricing/managed/estimate")
	if len(estimates) != 1 {
		t.Fatalf("estimate requests = %d", len(estimates))
	}
	query := estimates[0].Query
	if query.Get("cpu") != "0.5" || query.Get("memoryMib") != "1024" || query.Get("storageGib") != "1" {
		t.Fatalf("estimate query = %v", query)
	}
	creates := env.dash.find("POST", "/api/v1/projects/p-man/managed/instances/create")
	if len(creates) != 1 {
		t.Fatalf("create requests = %d", len(creates))
	}
	body := creates[0].Body
	if body["name"] != "chat2" || body["service"] != "realtime" || body["quotedHourlyTotal"] != 0.0052 {
		t.Fatalf("create body = %v", body)
	}
	if size := body["size"].(map[string]any); size["cpu"] != 0.5 || size["memoryMib"] != float64(1024) || size["storageGib"] != float64(1) {
		t.Fatalf("create size = %v", size)
	}
	if app := body["initialApp"].(map[string]any); app["name"] != "main" {
		t.Fatalf("initialApp = %v", app)
	}
	if key, _ := body["idempotencyKey"].(string); !strings.HasPrefix(key, "cli-rt-") || len(key) < 20 {
		t.Fatalf("idempotencyKey = %q", key)
	}
	if polls := env.dash.find("GET", "/api/v1/projects/p-man/managed/instances/mi_new/operations/op_1"); len(polls) != 2 {
		t.Fatalf("operation polls = %d, want 2", len(polls))
	}
	text := env.out.String()
	for _, want := range []string{"$3.82/month", "$0.0052/hour", "up to 7,500 connections", "is ready", "wss://chat2.rt.hubfly.test", rtTestAppKey} {
		if !strings.Contains(text, want) {
			t.Fatalf("create output missing %q:\n%s", want, text)
		}
	}
	if !strings.Contains(env.errOut.String(), "Creating realtime server [2/2] Start engine") {
		t.Fatalf("progress output:\n%s", env.errOut.String())
	}
}

func TestRealtimeCreateConfirmation(t *testing.T) {
	env := setupRealtimeTest(t)
	installRealtimeCreateFixtures(env)
	createPath := "/api/v1/projects/p-man/managed/instances/create"

	err := run([]string{"rt", "create", "chat2", "--project", "rt-project"})
	if err == nil || !strings.Contains(err.Error(), "--yes") || !strings.Contains(err.Error(), "$3.82/month") {
		t.Fatalf("non-interactive create without --yes: %v", err)
	}
	if got := env.dash.find("POST", createPath); len(got) != 0 {
		t.Fatalf("created without confirmation")
	}

	rtInteractive = func() bool { return true }
	setTestStdin("n\n")
	mustRun(t, "rt", "create", "chat2", "--project", "rt-project")
	if got := env.dash.find("POST", createPath); len(got) != 0 {
		t.Fatalf("created after the user declined")
	}
	if !strings.Contains(env.out.String(), "Cancelled.") {
		t.Fatalf("decline output:\n%s", env.out.String())
	}

	setTestStdin("y\n")
	mustRun(t, "rt", "create", "chat2", "--project", "rt-project", "--no-wait")
	creates := env.dash.find("POST", createPath)
	if len(creates) != 1 {
		t.Fatalf("create requests after yes = %d", len(creates))
	}
	// Default size is the smallest catalog size.
	if size := creates[0].Body["size"].(map[string]any); size["memoryMib"] != float64(512) {
		t.Fatalf("default size = %v", size)
	}
	if polls := env.dash.find("GET", "/api/v1/projects/p-man/managed/instances/mi_new/operations/op_1"); len(polls) != 0 {
		t.Fatalf("--no-wait polled %d times", len(polls))
	}
}

func TestRealtimeCreateInNewManagedProject(t *testing.T) {
	env := setupRealtimeTest(t)
	installRealtimeCreateFixtures(env)
	env.dash.ok("POST", "/api/v1/projects/create", map[string]any{"id": "p-man", "name": "chat2", "type": "managed"})
	mustRun(t, "rt", "create", "chat2", "--project", "new", "--yes", "--json")
	projects := env.dash.find("POST", "/api/v1/projects/create")
	if len(projects) != 1 {
		t.Fatalf("project create requests = %d", len(projects))
	}
	want := map[string]any{"name": "chat2", "type": "managed", "managedService": "realtime"}
	if !reflect.DeepEqual(projects[0].Body, want) {
		t.Fatalf("project body = %v, want %v", projects[0].Body, want)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(env.out.String()), &out); err != nil {
		t.Fatalf("--json output: %v\n%s", err, env.out.String())
	}
	if out["operationId"] != "op_1" || out["instance"] == nil {
		t.Fatalf("--json output = %v", out)
	}
	if !strings.Contains(env.errOut.String(), "$3.82/month") {
		t.Fatalf("--json quote should go to stderr:\n%s", env.errOut.String())
	}
}

func TestRealtimeCreatePriceChangedHint(t *testing.T) {
	env := setupRealtimeTest(t)
	installRealtimeCreateFixtures(env)
	env.dash.on("POST", "/api/v1/projects/p-man/managed/instances/create", func(rtRecorded) (int, any) {
		return 409, map[string]any{"ok": false, "error": map[string]any{"code": "MANAGED_PRICE_CHANGED", "message": "The price changed"}}
	})
	err := run([]string{"rt", "create", "chat2", "--project", "rt-project", "--yes"})
	var apiErr *apiError
	if !errors.As(err, &apiErr) || apiErr.Status != 409 || !strings.Contains(err.Error(), "new price") {
		t.Fatalf("price change error = %v", err)
	}
}

func TestRealtimeDeleteTypedConfirmation(t *testing.T) {
	env := setupRealtimeTest(t)
	deletePath := rtTestBase + "/delete"
	env.dash.ok("DELETE", deletePath, map[string]any{"operationId": "op_del"})

	if err := run([]string{"rt", "delete", "chat"}); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("non-interactive delete: %v", err)
	}
	rtInteractive = func() bool { return true }
	setTestStdin("Chat\n")
	mustRun(t, "rt", "delete", "chat")
	if got := env.dash.find("DELETE", deletePath); len(got) != 0 {
		t.Fatalf("deleted after a wrong typed name")
	}
	setTestStdin("chat\n")
	mustRun(t, "rt", "delete", "chat")
	deletes := env.dash.find("DELETE", deletePath)
	if len(deletes) != 1 || deletes[0].Query.Get("confirmName") != "chat" {
		t.Fatalf("delete requests = %+v", deletes)
	}

	rtInteractive = func() bool { return false }
	env.dash.on("GET", rtTestBase+"/operations/op_del", func(rtRecorded) (int, any) {
		return 404, map[string]any{"ok": false, "error": map[string]any{"code": "NOT_FOUND", "message": "gone"}}
	})
	mustRun(t, "rt", "delete", "mi_chat", "--yes", "--wait")
	if !strings.Contains(env.out.String(), "Realtime server 'chat' deleted.") {
		t.Fatalf("delete --wait output:\n%s", env.out.String())
	}
}

// ---------------------------------------------------------------------------
// apps / keys
// ---------------------------------------------------------------------------

func TestRealtimeAppsCreateSendsSettingsAndHidesSecret(t *testing.T) {
	env := setupRealtimeTest(t)
	created := map[string]any{
		"app":    map[string]any{"appId": "1000002", "name": "staging"},
		"key":    map[string]any{"id": "rk_2", "key": "zzzzzzzzzz0123456789"},
		"secret": "app-secret-value",
		"sync":   map[string]any{"status": "applied", "generation": 5},
	}
	env.dash.ok("POST", rtTestBase+"/realtime/apps/create", created)
	mustRun(t, "rt", "apps", "create", "chat", "staging",
		"--origins", "https://a.example, https://b.example",
		"--client-events", "--require-auth=false", "--encryption", "required",
		"--history", "durable", "--history-ttl", "1h", "--history-max", "500",
		"--offline-queue=false", "--max-connections", "1000", "--payload-preview")
	requests := env.dash.find("POST", rtTestBase+"/realtime/apps/create")
	if len(requests) != 1 {
		t.Fatalf("app create requests = %d", len(requests))
	}
	want := map[string]any{
		"name": "staging",
		"settings": map[string]any{
			"allowedOrigins":            []any{"https://a.example", "https://b.example"},
			"clientEvents":              true,
			"requireUserAuthentication": false,
			"encryption":                "required",
			"history":                   map[string]any{"mode": "durable", "ttlSeconds": float64(3600), "maxMessages": float64(500)},
			"offlineQueue":              map[string]any{"enabled": false},
			"maxConnections":            float64(1000),
			"inspectorPayloadPreview":   true,
		},
	}
	if !reflect.DeepEqual(requests[0].Body, want) {
		t.Fatalf("app create body =\n%v\nwant\n%v", requests[0].Body, want)
	}
	if strings.Contains(env.out.String(), "app-secret-value") || !strings.Contains(env.out.String(), "hidden") {
		t.Fatalf("apps create printed the secret without --reveal:\n%s", env.out.String())
	}

	env.out = &rtSyncBuffer{}
	rtOut = env.out
	mustRun(t, "rt", "apps", "create", "chat", "--name", "staging", "--reveal", "--json")
	var out map[string]any
	if err := json.Unmarshal([]byte(env.out.String()), &out); err != nil || out["secret"] != "app-secret-value" {
		t.Fatalf("apps create --reveal --json = %v (%v)", out, err)
	}
}

func TestRealtimeAppsUpdateAndDelete(t *testing.T) {
	env := setupRealtimeTest(t)
	updatedApp := rtTestApp()
	updatedApp["name"] = "prod"
	updatedApp["settingsRevision"] = 4
	env.dash.ok("PUT", rtTestBase+"/realtime/apps/1000001/update", map[string]any{"app": updatedApp, "sync": map[string]any{"status": "applied"}})
	env.dash.ok("PUT", rtTestBase+"/realtime/apps/1000001/settings/update", map[string]any{"app": updatedApp, "sync": map[string]any{"status": "pending"}})
	mustRun(t, "rt", "apps", "update", "chat", "production", "--name", "prod", "--disable", "--history", "memory", "--encryption", "off")

	updates := env.dash.find("PUT", rtTestBase+"/realtime/apps/1000001/update")
	if len(updates) != 1 || !reflect.DeepEqual(updates[0].Body, map[string]any{"name": "prod", "enabled": false}) {
		t.Fatalf("app update requests = %+v", updates)
	}
	settings := env.dash.find("PUT", rtTestBase+"/realtime/apps/1000001/settings/update")
	wantSettings := map[string]any{"settings": map[string]any{"history": map[string]any{"mode": "memory"}, "encryption": "off"}, "revision": float64(4)}
	if len(settings) != 1 || !reflect.DeepEqual(settings[0].Body, wantSettings) {
		t.Fatalf("settings update requests = %+v", settings)
	}

	env.dash.ok("DELETE", rtTestBase+"/realtime/apps/1000001/delete", map[string]any{"deleted": true, "variablesRemoved": 4})
	if err := run([]string{"rt", "apps", "delete", "chat", "production"}); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("non-interactive app delete: %v", err)
	}
	rtInteractive = func() bool { return true }
	setTestStdin("nope\n")
	mustRun(t, "rt", "apps", "delete", "chat", "1000001")
	if got := env.dash.find("DELETE", rtTestBase+"/realtime/apps/1000001/delete"); len(got) != 0 {
		t.Fatalf("app deleted after a wrong typed name")
	}
	setTestStdin("production\n")
	mustRun(t, "rt", "apps", "delete", "chat", "1000001")
	deletes := env.dash.find("DELETE", rtTestBase+"/realtime/apps/1000001/delete")
	if len(deletes) != 1 || deletes[0].Query.Get("confirmName") != "production" {
		t.Fatalf("app delete requests = %+v", deletes)
	}
}

func TestRealtimeAppPickingNeedsAppWhenSeveral(t *testing.T) {
	apps := []rtApp{{AppID: "1", Name: "production"}, {AppID: "2", Name: "staging"}}
	if _, err := pickRealtimeApp(apps, "chat", ""); err == nil || !strings.Contains(err.Error(), "--app") {
		t.Fatalf("expected --app hint, got %v", err)
	}
	if app, err := pickRealtimeApp(apps, "chat", "STAGING"); err != nil || app.AppID != "2" {
		t.Fatalf("pick by name = %+v, %v", app, err)
	}
	if app, err := pickRealtimeApp(apps, "chat", "1"); err != nil || app.Name != "production" {
		t.Fatalf("pick by id = %+v, %v", app, err)
	}
}

func TestRealtimeKeysFlows(t *testing.T) {
	env := setupRealtimeTest(t)
	keysPath := rtTestBase + "/realtime/apps/1000001/keys"
	env.dash.ok("POST", keysPath+"/create", map[string]any{
		"key": map[string]any{"id": "rk_2", "key": "kkkkkkkkkk0123456789", "status": "active"}, "secret": "created-secret",
		"sync": map[string]any{"status": "applied"},
	})
	mustRun(t, "rt", "keys", "create", "chat")
	if !strings.Contains(env.out.String(), "created-secret") {
		t.Fatalf("keys create must print the secret:\n%s", env.out.String())
	}
	// L10: the one time the secret is shown comes with a warning on stderr,
	// never in stdout (scripts parse it) and never containing the secret.
	if errText := env.errOut.String(); !strings.Contains(errText, "Warning: the new key's secret is printed below") ||
		strings.Contains(errText, "created-secret") || strings.Contains(env.out.String(), "Warning:") {
		t.Fatalf("keys create warning:\nstderr: %s\nstdout: %s", errText, env.out.String())
	}
	env.out, env.errOut = &rtSyncBuffer{}, &rtSyncBuffer{}
	rtOut, rtErr = env.out, env.errOut
	mustRun(t, "rt", "keys", "create", "chat", "--json")
	var created map[string]any
	if err := json.Unmarshal([]byte(env.out.String()), &created); err != nil || created["secret"] != "created-secret" {
		t.Fatalf("keys create --json = %q (%v)", env.out.String(), err)
	}
	if env.errOut.String() != "" {
		t.Fatalf("keys create --json wrote to stderr: %q", env.errOut.String())
	}
	if !strings.Contains(realtimeUsageText, `with --json it is in "secret"`) {
		t.Fatal("the usage text must say keys create --json includes the secret")
	}

	expires := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	env.dash.ok("POST", keysPath+"/rk_1/rotate", map[string]any{
		"key": map[string]any{"id": "rk_3", "key": "nnnnnnnnnn0123456789", "status": "active"}, "secret": "rotated-secret",
		"retiring": map[string]any{"id": "rk_1", "key": rtTestAppKey, "status": "retiring", "expiresAt": expires},
		"sync":     map[string]any{"status": "applied"}, "appsUpdated": 2,
	})
	env.out = &rtSyncBuffer{}
	rtOut = env.out
	mustRun(t, "rt", "keys", "rotate", "chat", "--grace", "1h")
	rotations := env.dash.find("POST", keysPath+"/rk_1/rotate")
	if len(rotations) != 1 || !reflect.DeepEqual(rotations[0].Body, map[string]any{"graceSeconds": float64(3600)}) {
		t.Fatalf("rotate requests = %+v", rotations)
	}
	text := env.out.String()
	if strings.Contains(text, "rotated-secret") || !strings.Contains(text, "grace period 1h") || !strings.Contains(text, expires) || !strings.Contains(text, "Bindings updated") {
		t.Fatalf("rotate output:\n%s", text)
	}
	env.out = &rtSyncBuffer{}
	rtOut = env.out
	mustRun(t, "rt", "keys", "rotate", "chat", rtTestAppKey, "--reveal")
	if rotations = env.dash.find("POST", keysPath+"/rk_1/rotate"); len(rotations[1].Body) != 0 {
		t.Fatalf("rotate without --grace must use the server default: %v", rotations[1].Body)
	}
	if !strings.Contains(env.out.String(), "rotated-secret") || !strings.Contains(env.out.String(), "grace period 1h") {
		t.Fatalf("rotate --reveal output:\n%s", env.out.String())
	}
	if err := run([]string{"rt", "keys", "rotate", "chat", "--grace", "0"}); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("grace 0 without --yes: %v", err)
	}

	env.dash.ok("POST", keysPath+"/rk_1/revoke", map[string]any{"key": map[string]any{"id": "rk_1", "status": "revoked"}, "sync": map[string]any{"status": "applied"}})
	if err := run([]string{"rt", "keys", "revoke", "chat", "rk_1"}); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("non-interactive revoke: %v", err)
	}
	rtInteractive = func() bool { return true }
	setTestStdin("n\n")
	mustRun(t, "rt", "keys", "revoke", "chat", "rk_1")
	if got := env.dash.find("POST", keysPath+"/rk_1/revoke"); len(got) != 0 {
		t.Fatalf("revoked after the user declined")
	}
	mustRun(t, "rt", "keys", "revoke", "chat", "rk_1", "--yes")
	if got := env.dash.find("POST", keysPath+"/rk_1/revoke"); len(got) != 1 {
		t.Fatalf("revoke requests = %d", len(got))
	}

	env.dash.ok("POST", keysPath+"/rk_1/reveal", map[string]any{"keyId": "rk_1", "appId": "1000001", "key": rtTestAppKey, "secret": "revealed-secret"})
	env.out = &rtSyncBuffer{}
	rtOut = env.out
	mustRun(t, "rt", "keys", "reveal", "chat")
	if !strings.Contains(env.out.String(), "revealed-secret") {
		t.Fatalf("reveal output:\n%s", env.out.String())
	}
}

// ---------------------------------------------------------------------------
// bind / env
// ---------------------------------------------------------------------------

func TestRealtimeBindRequestBody(t *testing.T) {
	env := setupRealtimeTest(t)
	env.dash.ok("POST", rtTestBase+"/realtime/bindings/create", map[string]any{
		"id": "rb_1", "appId": "1000001", "appName": "production", "targetProjectId": "p-web", "targetProjectName": "web",
		"preset": "laravel", "clientPrefix": "VITE_", "envKeys": []string{"BROADCAST_CONNECTION", "PUSHER_APP_ID"},
	})
	mustRun(t, "rt", "bind", "chat", "--to", "web", "--preset", "pusher", "--client-prefix", "vite", "--key", "rk_1")
	binds := env.dash.find("POST", rtTestBase+"/realtime/bindings/create")
	want := map[string]any{"appId": "1000001", "targetProjectId": "p-web", "preset": "laravel", "clientPrefix": "VITE_", "keyId": "rk_1"}
	if len(binds) != 1 || !reflect.DeepEqual(binds[0].Body, want) {
		t.Fatalf("bind requests = %+v", binds)
	}
	if !strings.Contains(env.out.String(), "Laravel/Pusher preset") || !strings.Contains(env.out.String(), "PUSHER_APP_ID") {
		t.Fatalf("bind output:\n%s", env.out.String())
	}

	env.dash.ok("GET", rtTestBase+"/realtime/bindings", map[string]any{"items": []map[string]any{{"id": "rb_1", "appName": "production", "targetProjectName": "web", "envKeys": []string{"A", "B"}}}})
	env.dash.ok("DELETE", rtTestBase+"/realtime/bindings/rb_1/delete", map[string]any{"deleted": true, "variablesRemoved": 2})
	mustRun(t, "rt", "bind", "remove", "chat", "rb_1", "--keep-variables", "--yes")
	removes := env.dash.find("DELETE", rtTestBase+"/realtime/bindings/rb_1/delete")
	if len(removes) != 1 || removes[0].Query.Get("removeVariables") != "false" {
		t.Fatalf("bind remove requests = %+v", removes)
	}
}

func TestRealtimeEnvVariablesMirrorDashboardPresets(t *testing.T) {
	keys := func(vars []rtEnvVar) []string {
		names := []string{}
		for _, variable := range vars {
			names = append(names, variable.Key)
		}
		return names
	}
	hubfly := realtimeEnvVariables("hubfly", "NEXT_PUBLIC_", "h.example", "1", "k", "s")
	if want := []string{"HUBFLY_REALTIME_URL", "HUBFLY_REALTIME_APP_ID", "HUBFLY_REALTIME_KEY", "HUBFLY_REALTIME_SECRET", "NEXT_PUBLIC_HUBFLY_REALTIME_URL", "NEXT_PUBLIC_HUBFLY_REALTIME_KEY"}; !reflect.DeepEqual(keys(hubfly), want) {
		t.Fatalf("hubfly preset = %q", keys(hubfly))
	}
	if hubfly[0].Value != "https://h.example" || !hubfly[3].Secret {
		t.Fatalf("hubfly values = %+v", hubfly)
	}
	laravel := realtimeEnvVariables("laravel", "none", "h.example", "1", "k", "s")
	if want := []string{"BROADCAST_CONNECTION", "PUSHER_APP_ID", "PUSHER_APP_KEY", "PUSHER_APP_SECRET", "PUSHER_HOST", "PUSHER_PORT", "PUSHER_SCHEME", "PUSHER_APP_CLUSTER", "VITE_PUSHER_APP_KEY", "VITE_PUSHER_HOST", "VITE_PUSHER_PORT", "VITE_PUSHER_SCHEME", "VITE_PUSHER_APP_CLUSTER"}; !reflect.DeepEqual(keys(laravel), want) {
		t.Fatalf("laravel preset = %q", keys(laravel))
	}
}

func TestRealtimeEnvHidesSecretUnlessRevealed(t *testing.T) {
	env := setupRealtimeTest(t)
	revealPath := rtTestBase + "/realtime/apps/1000001/keys/rk_1/reveal"
	env.dash.ok("POST", revealPath, map[string]any{"keyId": "rk_1", "appId": "1000001", "key": rtTestAppKey, "secret": "env-secret+/="})

	mustRun(t, "rt", "env", "chat")
	text := env.out.String()
	for _, want := range []string{"HUBFLY_REALTIME_URL=https://chat-1a2b.rt.hubfly.test", "HUBFLY_REALTIME_APP_ID=1000001", "HUBFLY_REALTIME_KEY=" + rtTestAppKey, "# HUBFLY_REALTIME_SECRET is hidden", "HUBFLY_REALTIME_E2E_KEYS"} {
		if !strings.Contains(text, want) {
			t.Fatalf("env output missing %q:\n%s", want, text)
		}
	}
	if got := env.dash.find("POST", revealPath); len(got) != 0 {
		t.Fatalf("env without --reveal revealed the secret")
	}

	env.out = &rtSyncBuffer{}
	rtOut = env.out
	mustRun(t, "rt", "env", "chat", "--reveal", "--client-prefix", "NEXT_PUBLIC_")
	if !strings.Contains(env.out.String(), "HUBFLY_REALTIME_SECRET=env-secret+/=") || !strings.Contains(env.out.String(), "NEXT_PUBLIC_HUBFLY_REALTIME_KEY=") {
		t.Fatalf("env --reveal output:\n%s", env.out.String())
	}

	env.out = &rtSyncBuffer{}
	rtOut = env.out
	mustRun(t, "rt", "env", "chat", "--format", "json", "--preset", "laravel")
	var vars map[string]string
	if err := json.Unmarshal([]byte(env.out.String()), &vars); err != nil {
		t.Fatalf("env --format json: %v\n%s", err, env.out.String())
	}
	if _, ok := vars["PUSHER_APP_SECRET"]; ok || vars["PUSHER_HOST"] != "chat-1a2b.rt.hubfly.test" || vars["BROADCAST_CONNECTION"] != "pusher" {
		t.Fatalf("env json = %v", vars)
	}
	if !strings.Contains(env.errOut.String(), "PUSHER_APP_SECRET") {
		t.Fatalf("expected an omitted-secret note on stderr:\n%s", env.errOut.String())
	}
}

func TestFormatDotenvValueQuotesSpecialCharacters(t *testing.T) {
	if got := formatDotenvValue("https://h.example"); got != "https://h.example" {
		t.Fatalf("plain value = %q", got)
	}
	if got := formatDotenvValue(`a "b" $c`); got != `"a \"b\" \$c"` {
		t.Fatalf("quoted value = %q", got)
	}
}

// ---------------------------------------------------------------------------
// publish / send / insights
// ---------------------------------------------------------------------------

func TestRealtimePublishRequestBody(t *testing.T) {
	env := setupRealtimeTest(t)
	publishPath := rtTestBase + "/realtime/apps/1000001/events/publish"
	env.dash.ok("POST", publishPath, map[string]any{"messageId": "m_1", "bytes": 7, "channels": map[string]any{"orders": map[string]any{"epoch": "e1", "offset": 42}}})

	mustRun(t, "rt", "publish", "chat", "--channel", "orders", "--channel", "a,b", "--event", "order.updated", "--data", `{"x":1}`, "--exclude", "1234567890.1234567890~clientkeyAAA")
	requests := env.dash.find("POST", publishPath)
	want := map[string]any{"event": "order.updated", "data": `{"x":1}`, "channels": []any{"orders", "a,b"}, "excludeSocketId": "1234567890.1234567890"}
	if len(requests) != 1 || !reflect.DeepEqual(requests[0].Body, want) {
		t.Fatalf("publish requests = %+v", requests)
	}
	if strings.Contains(env.dash.allRaw(), "clientkeyAAA") {
		t.Fatalf("an E2E client key reached the Dashboard")
	}
	if !strings.Contains(env.out.String(), "orders: offset 42 (epoch e1)") {
		t.Fatalf("publish output:\n%s", env.out.String())
	}

	mustRun(t, "rt", "publish", "chat", "orders", "greet", "hello world")
	if got := env.dash.find("POST", publishPath)[1].Body; got["event"] != "greet" || got["data"] != "hello world" {
		t.Fatalf("positional publish body = %v", got)
	}

	rtStdinIsTerminal = func() bool { return false }
	rtStdin = strings.NewReader("from stdin\n")
	mustRun(t, "rt", "publish", "chat", "--channel", "orders", "--event", "e")
	if got := env.dash.find("POST", publishPath)[2].Body; got["data"] != "from stdin" {
		t.Fatalf("stdin publish body = %v", got)
	}

	file := t.TempDir() + "/data.json"
	if err := os.WriteFile(file, []byte("[1,2,3]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "rt", "publish", "chat", "--channel", "orders", "--event", "e", "--data-file", file)
	if got := env.dash.find("POST", publishPath)[3].Body; got["data"] != "[1,2,3]" {
		t.Fatalf("file publish body = %v", got)
	}
}

func TestRealtimePublishSurfacesE2ERequiredAndTooLarge(t *testing.T) {
	env := setupRealtimeTest(t)
	publishPath := rtTestBase + "/realtime/apps/1000001/events/publish"
	env.dash.on("POST", publishPath, func(rtRecorded) (int, any) {
		return 403, map[string]any{"ok": false, "error": map[string]any{"code": "MANAGED_REALTIME_E2E_REQUIRED", "message": "This app requires end-to-end encryption"}}
	})
	err := run([]string{"rt", "publish", "chat", "orders", "e", "plain"})
	if err == nil || !strings.Contains(err.Error(), "HubFly server SDK") || exitCodeForError(err) != 3 {
		t.Fatalf("E2E required error = %v", err)
	}
	env.dash.on("POST", publishPath, func(rtRecorded) (int, any) {
		return 413, map[string]any{"ok": false, "error": map[string]any{"code": "MANAGED_REALTIME_PAYLOAD_TOO_LARGE", "message": "Payload too large"}}
	})
	if err := run([]string{"rt", "publish", "chat", "orders", "e", "big"}); err == nil || !strings.Contains(err.Error(), "message size limit") {
		t.Fatalf("413 error = %v", err)
	}
	env.dash.on("POST", publishPath, func(rtRecorded) (int, any) {
		return 409, map[string]any{"ok": false, "error": map[string]any{"code": "MANAGED_REALTIME_ENGINE_UNSUPPORTED", "message": "Not available"}}
	})
	if err := run([]string{"rt", "publish", "chat", "orders", "e", "x"}); err == nil || !strings.Contains(err.Error(), "engine version") {
		t.Fatalf("engine unsupported error = %v", err)
	}
}

// captureStderr runs fn with os.Stderr redirected (debugf writes there).
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stderr
	os.Stderr = writer
	done := make(chan string)
	go func() {
		data, _ := io.ReadAll(reader)
		done <- string(data)
	}()
	defer func() { os.Stderr = original }()
	fn()
	_ = writer.Close()
	os.Stderr = original
	return <-done
}

func TestRealtimeSendNeverSendsE2EClientKeys(t *testing.T) {
	env := setupRealtimeTest(t)
	sendPath := rtTestBase + "/realtime/apps/1000001/events/send"
	env.dash.ok("POST", sendPath, map[string]any{
		"messageId": "m_2", "bytes": 4,
		"results": []map[string]any{{"socketId": "1234567890.1234567890", "status": "delivered", "connections": 1}},
		"counts":  map[string]any{"delivered": 1},
	})
	const clientKey = "Zm9vYmFyYmF6cXV4c2VjcmV0a2V5MDEyMzQ1Njc4OQ"
	e2eID := "1234567890.1234567890~" + clientKey

	err := run([]string{"rt", "send", "chat", "--connection", e2eID, "--event", "notice", "--data", "hi"})
	if err == nil || !strings.Contains(err.Error(), "--strip-key") || !strings.Contains(err.Error(), "your own server") {
		t.Fatalf("plaintext to an E2E id: %v", err)
	}
	if strings.Contains(err.Error(), clientKey) {
		t.Fatalf("the error echoed the client key")
	}
	if got := env.dash.find("POST", sendPath); len(got) != 0 {
		t.Fatalf("refused send still reached the Dashboard")
	}

	debugEnabled = true
	debugOutput := captureStderr(t, func() {
		mustRun(t, "rt", "send", "chat", "--connection", e2eID+",2234567890.1234567890", "--event", "notice", "--data", "hi", "--strip-key")
		mustRun(t, "rt", "send", "chat", "--connection", e2eID, "--event", realtimeE2EEvent, "--data", `{"v":1}`)
	})
	debugEnabled = false
	requests := env.dash.find("POST", sendPath)
	if len(requests) != 2 {
		t.Fatalf("send requests = %d", len(requests))
	}
	if got := requests[0].Body["socketIds"]; !reflect.DeepEqual(got, []any{"1234567890.1234567890", "2234567890.1234567890"}) {
		t.Fatalf("socketIds = %v", got)
	}
	if got := requests[1].Body; got["event"] != realtimeE2EEvent || !reflect.DeepEqual(got["socketIds"], []any{"1234567890.1234567890"}) {
		t.Fatalf("hubfly.e2e send body = %v", got)
	}
	for name, text := range map[string]string{"requests": env.dash.allRaw(), "debug": debugOutput, "stdout": env.out.String(), "stderr": env.errOut.String()} {
		if strings.Contains(text, clientKey) {
			t.Fatalf("the E2E client key leaked into %s", name)
		}
	}
	if !strings.Contains(debugOutput, "/events/send") {
		t.Fatalf("debug output did not cover the send request:\n%s", debugOutput)
	}
	if !strings.Contains(env.errOut.String(), "Warning: sending plaintext") || !strings.Contains(env.errOut.String(), "Removed the client key") {
		t.Fatalf("send warnings:\n%s", env.errOut.String())
	}

	mustRun(t, "rt", "send", "chat", "--user", "u_42", "--event", "notice", "--data", "hi", "--message-id", "msg-1")
	if got := env.dash.find("POST", sendPath)[2].Body; !reflect.DeepEqual(got, map[string]any{"event": "notice", "data": "hi", "userId": "u_42", "messageId": "msg-1"}) {
		t.Fatalf("user send body = %v", got)
	}
	if !strings.Contains(env.out.String(), "1 delivered") {
		t.Fatalf("send output:\n%s", env.out.String())
	}
}

func TestRealtimeInsightsQueryAndOutput(t *testing.T) {
	env := setupRealtimeTest(t)
	insightsPath := rtTestBase + "/realtime/insights"
	rate := 0.995
	env.dash.ok("GET", insightsPath, map[string]any{
		"windowSeconds":   7200,
		"topChannels":     []map[string]any{{"name": "orders", "messages": 120, "bytes": 4096, "subscribersPeak": 12}},
		"topEvents":       []map[string]any{{"name": "order.updated", "count": 120, "bytes": 4096}},
		"authFailures":    []map[string]any{{"reason": "bad_signature", "count": 3}},
		"rateLimited":     map[string]any{"publish": 2},
		"closes":          map[string]any{"4001": 1},
		"connectionsPeak": 42,
		"libraries":       []map[string]any{{"name": "pusher-js", "version": "8.4.0", "count": 40}},
		"webhooks":        map[string]any{"successRate": rate, "latencyMsP95": 120, "backlog": 0},
	})
	mustRun(t, "rt", "insights", "chat", "--window", "2h", "--app", "production")
	requests := env.dash.find("GET", insightsPath)
	if len(requests) != 1 || requests[0].Query.Get("windowSeconds") != "7200" || requests[0].Query.Get("appId") != "1000001" {
		t.Fatalf("insights requests = %+v", requests)
	}
	for _, want := range []string{"last 2h", "Peak connections: 42", "orders", "order.updated", "bad_signature", "pusher-js", "99.5%", "120 ms"} {
		if !strings.Contains(env.out.String(), want) {
			t.Fatalf("insights output missing %q:\n%s", want, env.out.String())
		}
	}
}

// The insights view as the Dashboard builds it from a real engine's reply
// (testdata/realtime-engine-views.json, captured by hubfly-dashboard's
// engine-integration.test.ts against rt-devserver).
func TestRealtimeInsightsParsesEngineView(t *testing.T) {
	raw, err := os.ReadFile("testdata/realtime-engine-views.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Insights struct {
			Data map[string]any `json:"data"`
		} `json:"insights"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	env := setupRealtimeTest(t)
	env.dash.ok("GET", rtTestBase+"/realtime/insights", fixture.Insights.Data)

	mustRun(t, "rt", "insights", "chat", "--json")
	var decoded, want any
	if err := json.Unmarshal([]byte(env.out.String()), &decoded); err != nil {
		t.Fatalf("--json output: %v\n%s", err, env.out.String())
	}
	wantRaw, _ := json.Marshal(fixture.Insights.Data)
	_ = json.Unmarshal(wantRaw, &want)
	if !reflect.DeepEqual(decoded, want) {
		t.Fatalf("--json dropped or changed fields:\n got %v\nwant %v", decoded, want)
	}

	before := len(env.out.String())
	mustRun(t, "rt", "insights", "chat")
	out := env.out.String()[before:]
	data := fixture.Insights.Data
	channel := data["topChannels"].([]any)[0].(map[string]any)
	library := data["libraries"].([]any)[0].(map[string]any)
	failure := data["authFailures"].([]any)[0].(map[string]any)
	for _, want := range []string{
		"last 1h",
		fmt.Sprintf("Peak connections: %v", data["connectionsPeak"]),
		fmt.Sprintf("%v", channel["name"]),
		fmt.Sprintf("%v", failure["reason"]),
		"Webhooks: success 100.0%",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("insights output missing %q:\n%s", want, out)
		}
	}
	if !regexp.MustCompile(regexp.QuoteMeta(fmt.Sprint(library["name"])) + `\s+` + regexp.QuoteMeta(fmt.Sprint(library["version"]))).MatchString(out) {
		t.Fatalf("library row missing:\n%s", out)
	}
	// Every counter is zero in the capture: no empty tables.
	if strings.Contains(out, "Connection closes") || strings.Contains(out, "Rate limited") {
		t.Fatalf("zero counters listed:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// keygen
// ---------------------------------------------------------------------------

type rtFailingTransport struct {
	mu    sync.Mutex
	calls int
}

func (f *rtFailingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return nil, errors.New("network access is forbidden in this test")
}

func TestRealtimeKeygenIsLocalOnly(t *testing.T) {
	env := setupRealtimeTest(t)
	transport := &rtFailingTransport{}
	originalTransport := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = originalTransport })
	t.Setenv("HUBFLY_TOKEN", "")

	mustRun(t, "realtime", "keygen", "--kid", "k2")
	mustRun(t, "rt", "keygen")
	lines := strings.Split(strings.TrimSpace(env.out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("keygen output:\n%s", env.out.String())
	}
	withKid := regexp.MustCompile(`^k2:([A-Za-z0-9+/]{43}=)$`).FindStringSubmatch(lines[0])
	if withKid == nil {
		t.Fatalf("keygen --kid output %q is not kid:base64", lines[0])
	}
	for _, encoded := range []string{withKid[1], lines[1]} {
		raw, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || len(raw) != 32 {
			t.Fatalf("key %q decodes to %d bytes (%v), want 32", encoded, len(raw), err)
		}
	}
	if withKid[1] == lines[1] {
		t.Fatalf("two keygen runs returned the same key")
	}
	if transport.calls != 0 || env.dash.allRaw() != "" {
		t.Fatalf("keygen contacted the network: %d transport calls, requests:\n%s", transport.calls, env.dash.allRaw())
	}
}

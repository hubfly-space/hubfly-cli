package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestManagedLogsTailAndStreams(t *testing.T) {
	env := setupDBDTest(t)
	env.dash.ok("GET", dbdTestPG+"/logs", map[string]any{
		"stdout": "ready to accept connections\n", "stderr": "WARNING: slow\n",
		"stdoutTruncated": true, "stderrTruncated": false, "tailBytes": 65536,
	})
	mustRun(t, "db", "logs", "pg", "--tail", "128k")
	if got := env.dash.find("GET", dbdTestPG+"/logs")[0].Query.Get("tailBytes"); got != "131072" {
		t.Fatalf("tailBytes = %q", got)
	}
	if env.out.String() != "ready to accept connections\n" {
		t.Fatalf("stdout = %q", env.out.String())
	}
	errText := env.errOut.String()
	if !strings.Contains(errText, "WARNING: slow") || !strings.Contains(errText, "use --tail for more") {
		t.Fatalf("stderr = %q", errText)
	}
	dbdRunErr(t, "invalid --tail", "db", "logs", "pg", "--tail", "lots")
	dbdRunErr(t, "at most 2 MiB", "db", "logs", "pg", "--tail", "3m")
	if got := len(env.dash.find("GET", dbdTestPG+"/logs")); got != 1 {
		t.Fatalf("invalid --tail reached the Dashboard (%d requests)", got)
	}

	// Realtime servers use the same endpoint.
	env.dash.ok("GET", rtTestBase+"/logs", map[string]any{"stdout": "rt up", "stderr": "", "stdoutTruncated": false, "stderrTruncated": false, "tailBytes": 65536})
	mustRun(t, "rt", "logs", "chat")
	if !strings.Contains(env.out.String(), "rt up\n") {
		t.Fatalf("realtime logs = %q", env.out.String())
	}
	if got := env.dash.find("GET", rtTestBase+"/logs")[0].Query.Get("tailBytes"); got != "65536" {
		t.Fatalf("default tailBytes = %q", got)
	}
}

func TestManagedLogsFollowPrintsOnlyNewLines(t *testing.T) {
	env := setupDBDTest(t)
	tails := []string{"a\nb\n", "a\nb\nc\n", "b\nc\nd\ne\n", "x\ny\n"}
	var calls atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	moFollowContext = func() (context.Context, context.CancelFunc) { return ctx, cancel }
	env.dash.on("GET", dbdTestPG+"/logs", func(rtRecorded) (int, any) {
		index := int(calls.Add(1)) - 1
		if index >= len(tails)-1 {
			cancel()
			index = len(tails) - 1
		}
		return http.StatusOK, map[string]any{"stdout": tails[index], "stderr": "", "stdoutTruncated": false, "stderrTruncated": false, "tailBytes": 4096}
	})
	done := make(chan error, 1)
	go func() { done <- run([]string{"db", "logs", "pg", "--follow"}) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("follow error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("follow did not stop on cancel")
	}
	// The last tail shares nothing with the previous one, so it is printed whole.
	if got := env.out.String(); !strings.HasPrefix(got, "a\nb\nc\nd\ne\n") {
		t.Fatalf("follow output = %q", got)
	}
}

func TestMoNewLogText(t *testing.T) {
	cases := []struct{ previous, current, want string }{
		{"", "a\n", "a\n"},
		{"a\n", "a\n", ""},
		{"a\nb\n", "b\nc\n", "c\n"},
		{"a\nb\n", "q\n", "q\n"},
		{"a\nb\n", "a\nb\nc", ""},
		{"a\nb\n", "a\nb\nc\n", "c\n"},
		{"a\nb\nc\n", "b\nc\nd\n", "d\n"},
		{"a\nb\nc\n", "rtial b\nc\nd\n", "d\n"},
		{"x\nx\nx\n", "x\nx\nx\nx\n", "x\n"},
		{"a\nb\n", "b\nb\n", "b\n"},
	}
	for _, c := range cases {
		if got := moNewLogText(c.previous, c.current); got != c.want {
			t.Fatalf("moNewLogText(%q, %q) = %q, want %q", c.previous, c.current, got, c.want)
		}
	}
}

func moTestSample(cpu float64) map[string]any {
	return map[string]any{
		"time": "2026-10-20T10:00:00Z", "cpuPercent": cpu, "memoryUsageBytes": 128 << 20, "memoryLimitBytes": 512 << 20, "memoryUsagePercent": 25.0,
		"storageUsedBytes": 1 << 30, "storageTotalBytes": 10 << 30, "storageUsagePercent": 10.0, "processes": 12,
		"networkRxBytes": 2048, "networkTxBytes": 4096,
	}
}

func TestManagedMetricsDatabaseAndRealtime(t *testing.T) {
	env := setupDBDTest(t)
	env.dash.ok("GET", dbdTestPG+"/metrics", map[string]any{
		"latest":  moTestSample(4.5),
		"history": map[string]any{"windowSeconds": 3600, "sampleIntervalSeconds": 60, "records": []any{moTestSample(2), moTestSample(8)}},
		"database": map[string]any{
			"collectedAt": "2026-10-20T10:00:00Z", "uptimeSeconds": 7200,
			"connections": map[string]any{"active": 2, "idle": 3, "total": 5, "max": 100},
			"commits":     1500, "rollbacks": 3, "queriesTotal": 9000, "cacheHitRatio": 0.991, "databaseSizeBytes": 9 << 20, "deadlocks": 0,
		},
		"databaseHistory": []any{map[string]any{"t": "x", "tps": 1.0, "qps": 4.0}, map[string]any{"t": "y", "tps": 3.0, "qps": 6.0}},
		"realtime":        nil, "realtimeHistory": []any{}, "volume": nil,
	})
	mustRun(t, "db", "metrics", "pg", "--window", "6h")
	if got := env.dash.find("GET", dbdTestPG+"/metrics")[0].Query.Get("windowSeconds"); got != "21600" {
		t.Fatalf("windowSeconds = %q", got)
	}
	out := env.out.String()
	for _, fragment := range []string{"CPU:", "4.5%", "128.0 MB of 512.0 MB (25.0%)", "CPU avg 5.0%, peak 8.0%", "2 active, 3 idle, 5 total (max 100)",
		"99.1%", "2.0 transactions/s, 5.0 queries/s", "Uptime:", "2h 0m"} {
		if !strings.Contains(out, fragment) {
			t.Fatalf("db metrics missing %q:\n%s", fragment, out)
		}
	}
	dbdRunErr(t, "invalid --window", "db", "metrics", "pg", "--window", "1m")

	env.out.buf.Reset()
	env.dash.ok("GET", rtTestBase+"/metrics", map[string]any{
		"latest": moTestSample(1), "history": nil, "database": nil, "databaseHistory": []any{},
		"realtime": map[string]any{
			"collectedAt": "2026-10-20T10:00:00Z", "uptimeSeconds": 60,
			"connections": map[string]any{"current": 120, "peak": 300, "detached": 0, "max": 7500, "byProtocol": map[string]any{}, "byLibrary": map[string]any{}},
			"channels":    40, "subscriptions": 200, "presenceMembers": 10,
			"messages": map[string]any{"in": 5000, "out": 25000, "bySource": map[string]any{}, "byKind": map[string]any{}},
			"bytes":    map[string]any{"in": 1 << 20, "out": 5 << 20},
			"closes":   map[string]any{}, "errors": map[string]any{},
			"webhooks": map[string]any{"sent": 0, "failed": 0, "backlog": 0, "dead": 0, "dropped": 0, "latencyMsP50": 0, "latencyMsP95": 0},
			"history":  map[string]any{"messages": 0, "bytes": 0, "replays": 0, "gaps": 0, "degraded": false, "storedMessages": 0, "storedBytes": 0, "writeErrors": 0},
			"apps":     map[string]any{},
		},
		"realtimeHistory": []any{}, "volume": map[string]any{"totalBytes": 1 << 30, "usedBytes": 1 << 20},
	})
	mustRun(t, "rt", "metrics", "chat")
	out = env.out.String()
	for _, fragment := range []string{"120 now, peak 300 (limit 7,500)", "40 (200 subscriptions, 10 presence members)", "5,000 in, 25,000 out"} {
		if !strings.Contains(out, fragment) {
			t.Fatalf("realtime metrics missing %q:\n%s", fragment, out)
		}
	}
	if strings.Contains(out, "Database") {
		t.Fatalf("realtime metrics show a database section:\n%s", out)
	}

	env.out.buf.Reset()
	mustRun(t, "rt", "metrics", "chat", "--json")
	var decoded map[string]any
	if err := json.Unmarshal([]byte(env.out.String()), &decoded); err != nil || decoded["realtime"] == nil {
		t.Fatalf("--json output = %q (%v)", env.out.String(), err)
	}
}

func TestManagedOperations(t *testing.T) {
	env := setupDBDTest(t)
	env.dash.ok("GET", dbdTestPG+"/operations", map[string]any{"items": []map[string]any{
		{"id": "op_2", "instanceId": "mi_pg", "kind": "config_apply", "status": "running", "actorUserId": "u1", "createdAt": "2026-10-20T10:00:00Z", "finishedAt": nil, "error": nil,
			"steps": []map[string]any{{"key": "a", "label": "Validate", "status": "succeeded"}, {"key": "b", "label": "Reload", "status": "running"}}},
		{"id": "op_1", "instanceId": "mi_pg", "kind": "create", "status": "failed", "actorUserId": "u1", "createdAt": "2026-10-19T10:00:00Z", "finishedAt": "2026-10-19T10:05:00Z",
			"error": map[string]any{"code": "X", "message": "no capacity"},
			"steps": []map[string]any{{"key": "a", "label": "Provision", "status": "succeeded"}, {"key": "b", "label": "Start", "status": "succeeded"}}},
	}})
	mustRun(t, "db", "operations", "pg", "--limit", "5")
	if got := env.dash.find("GET", dbdTestPG+"/operations")[0].Query.Get("limit"); got != "5" {
		t.Fatalf("limit = %q", got)
	}
	out := env.out.String()
	for _, fragment := range []string{"ID", "KIND", "config apply", "Reload", "2/2 done", "no capacity", "2026-10-19 10:05:00Z"} {
		if !strings.Contains(out, fragment) {
			t.Fatalf("operations missing %q:\n%s", fragment, out)
		}
	}
	dbdRunErr(t, "invalid --limit", "db", "ops", "pg", "--limit", "0")
	env.dash.ok("GET", rtTestBase+"/operations", map[string]any{"items": []any{}})
	env.out.buf.Reset()
	mustRun(t, "rt", "operations", "chat")
	if !strings.Contains(env.out.String(), "No operations on 'chat' yet.") {
		t.Fatalf("empty operations:\n%s", env.out.String())
	}
	dbdRunErr(t, "realtime server 'pg' not found", "rt", "operations", "pg")
}

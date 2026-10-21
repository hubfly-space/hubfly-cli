package cli

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func mlTestOperation(env rtTestEnv, base, id, kind string) {
	env.dash.ok("GET", base+"/operations/"+id, map[string]any{"id": id, "kind": kind, "status": "succeeded"})
}

func TestManagedPowerCommandsForBothServices(t *testing.T) {
	env := setupDBTest(t, "postgresql")
	env.dash.ok("POST", dbcTestBase+"/stop", map[string]any{"operationId": "op_s"})
	mlTestOperation(env, dbcTestBase, "op_s", "stop")
	env.dash.ok("POST", rtTestBase+"/restart", map[string]any{"operationId": "op_r"})
	mlTestOperation(env, rtTestBase, "op_r", "restart")

	mustRun(t, "db", "stop", "pg")
	if len(env.dash.find("POST", dbcTestBase+"/stop")) != 1 || !strings.Contains(env.out.String(), "Database 'pg' is stopped.") {
		t.Fatalf("stop output:\n%s", env.out.String())
	}
	if !strings.Contains(env.errOut.String(), "pays for storage only") {
		t.Fatalf("stop warning missing:\n%s", env.errOut.String())
	}
	mustRun(t, "db", "start", "pg")
	if len(env.dash.find("POST", dbcTestBase+"/start")) != 0 || !strings.Contains(env.out.String(), "already running") {
		t.Fatalf("start of a running database:\n%s", env.out.String())
	}

	env.out.buf.Reset()
	mustRun(t, "rt", "restart", "chat", "--json")
	var payload map[string]any
	if err := json.Unmarshal([]byte(env.out.String()), &payload); err != nil || payload["operationId"] != "op_r" {
		t.Fatalf("restart --json = %q (%v)", env.out.String(), err)
	}
	if !strings.Contains(env.errOut.String(), "hands them over in place (about 1-3 s)") {
		t.Fatalf("realtime restart note missing:\n%s", env.errOut.String())
	}

	env.dash.on("POST", rtTestBase+"/stop", func(rtRecorded) (int, any) {
		return http.StatusConflict, map[string]any{"ok": false, "error": map[string]any{"code": "MANAGED_INVALID_STATE", "message": "Instance is busy"}}
	})
	err := run([]string{"realtime", "stop", "chat", "--no-wait"})
	if err == nil || !strings.Contains(err.Error(), "hubfly realtime info chat") || !strings.Contains(err.Error(), "is running") {
		t.Fatalf("invalid state error = %v", err)
	}
}

func TestManagedResizeQuotesAndValidates(t *testing.T) {
	env := setupDBTest(t, "postgresql")
	env.dash.ok("GET", "/api/v1/managed/catalog", dbcTestCatalog())
	env.dash.ok("GET", "/api/v1/pricing/managed/estimate", map[string]any{"hourly": map[string]any{"total": 0.0028}, "monthly": map[string]any{"total": 2.05}})
	env.dash.ok("POST", dbcTestBase+"/resize", map[string]any{"operationId": "op_z", "price": map[string]any{"monthly": map[string]any{"total": 2.05}}, "restartRecommended": true})
	mlTestOperation(env, dbcTestBase, "op_z", "resize")
	pending := dbcTestInstance("mi_pg", "pg", "postgresql")
	pending["pendingRestart"] = true
	env.dash.ok("GET", dbcTestBase, pending)

	for want, args := range map[string][]string{
		"nothing to change":          {"db", "resize", "pg", "--size", "nano"},
		"confirmation required":      {"db", "resize", "pg", "--storage", "2"},
		"--restart is for databases": {"rt", "resize", "chat", "--storage", "2", "--restart"},
		"choose a new size":          {"db", "resize", "pg"},
	} {
		err := run(args)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("run(%q) error = %v, want %q", args, err, want)
		}
	}
	if len(env.dash.find("POST", dbcTestBase+"/resize")) != 0 {
		t.Fatal("invalid resizes reached the API")
	}
	if _, err := managedTargetSize(managedServiceDatabase, rtSize{CPU: 1, MemoryMib: 2048, StorageGib: 20}, mlCatalogSizes{}, "", 0, 0, 10); err == nil || !strings.Contains(err.Error(), "never shrink") {
		t.Fatalf("shrink error = %v", err)
	}

	mustRun(t, "db", "resize", "pg", "--size", "small", "--storage", "2", "--yes")
	body := env.dash.find("POST", dbcTestBase+"/resize")[0].Body
	if body["cpu"] != 0.5 || body["memoryMib"] != float64(1024) || body["storageGib"] != float64(2) || body["quotedHourlyTotal"] != 0.0028 {
		t.Fatalf("resize body = %v", body)
	}
	if _, ok := body["restart"]; ok {
		t.Fatalf("restart sent without --restart: %v", body)
	}
	text := env.out.String()
	for _, want := range []string{"$3.82/month", "$2.05/month", "hubfly db restart pg"} {
		if !strings.Contains(text, want) {
			t.Fatalf("resize output missing %q:\n%s", want, text)
		}
	}
	mustRun(t, "db", "resize", "pg", "--storage", "3", "--restart", "--yes", "--no-wait")
	if body := env.dash.find("POST", dbcTestBase+"/resize")[1].Body; body["restart"] != true || body["cpu"] != 0.25 {
		t.Fatalf("resize --restart body = %v", body)
	}
}

func TestManagedRealtimeResizeUsesRealtimePresets(t *testing.T) {
	env := setupRealtimeTest(t)
	installRealtimeCreateFixtures(env)
	env.dash.ok("POST", rtTestBase+"/resize", map[string]any{"operationId": "op_z", "restartRecommended": false})
	mustRun(t, "rt", "resize", "chat", "--size", "nano", "--yes", "--no-wait")
	if body := env.dash.find("POST", rtTestBase+"/resize")[0].Body; body["cpu"] != 0.25 || body["memoryMib"] != float64(512) || body["storageGib"] != float64(1) {
		t.Fatalf("realtime resize body = %v", body)
	}
}

func TestManagedUpgradeRenameProtect(t *testing.T) {
	env := setupDBTest(t, "postgresql")
	mustRun(t, "db", "upgrade", "pg", "--yes")
	if !strings.Contains(env.out.String(), "is up to date") || len(env.dash.find("POST", dbcTestBase+"/upgrade")) != 0 {
		t.Fatalf("upgrade without update:\n%s", env.out.String())
	}
	detail := dbcTestInstance("mi_pg", "pg", "postgresql")
	detail["updateAvailable"] = map[string]any{"versionLabel": "PostgreSQL 18.1"}
	env.dash.ok("GET", dbcTestBase, detail)
	env.dash.ok("POST", dbcTestBase+"/upgrade", map[string]any{"operationId": "op_u"})
	mlTestOperation(env, dbcTestBase, "op_u", "upgrade")
	if err := run([]string{"db", "upgrade", "pg"}); err == nil || !strings.Contains(err.Error(), "confirmation required") {
		t.Fatalf("upgrade without --yes error = %v", err)
	}
	mustRun(t, "db", "upgrade", "pg", "--yes")
	if !strings.Contains(env.out.String(), "now runs PostgreSQL 18.1") {
		t.Fatalf("upgrade output:\n%s", env.out.String())
	}

	renamed := dbcTestInstance("mi_pg", "orders", "postgresql")
	env.dash.ok("PUT", dbcTestBase+"/update", renamed)
	mustRun(t, "db", "rename", "pg", "orders")
	if body := env.dash.find("PUT", dbcTestBase+"/update")[0].Body; body["name"] != "orders" {
		t.Fatalf("rename body = %v", body)
	}

	env.dash.on("PUT", rtTestBase+"/deletion-protection/update", func(r rtRecorded) (int, any) {
		return 200, map[string]any{"deletionProtection": r.Body["enabled"]}
	})
	mustRun(t, "rt", "protect", "chat", "on")
	mustRun(t, "rt", "protect", "chat", "off")
	calls := env.dash.find("PUT", rtTestBase+"/deletion-protection/update")
	if len(calls) != 2 || calls[0].Body["enabled"] != true || calls[1].Body["enabled"] != false {
		t.Fatalf("protect calls = %v", calls)
	}
	if !strings.Contains(env.out.String(), "hubfly realtime protect chat off") {
		t.Fatalf("protect output:\n%s", env.out.String())
	}
	if err := run([]string{"db", "protect", "pg", "maybe"}); err == nil || !strings.Contains(err.Error(), "expected on or off") {
		t.Fatalf("protect parse error = %v", err)
	}
}

func TestManagedNetworkShowBothScopes(t *testing.T) {
	env := setupDBTest(t, "postgresql")
	mustRun(t, "db", "network", "pg")
	if !strings.Contains(env.out.String(), "open to all") || !strings.Contains(env.out.String(), "pg-1a2b.db.hubfly.test:31234") {
		t.Fatalf("db network:\n%s", env.out.String())
	}
	env.dash.ok("GET", rtTestBase+"/network", map[string]any{
		"publicAccess": true, "endpoint": map[string]any{"host": "chat-1a2b.rt.hubfly.test", "port": 443},
		"allowlist": []map[string]any{{"cidr": "198.51.100.0/24", "label": "office", "createdAt": "2026-10-20"}},
		"revision":  2, "tlsRequired": true, "allowlistSupported": true, "maxEntries": 64, "allowlistScope": "server_api", "publicAccessEditable": false,
	})
	env.out.buf.Reset()
	mustRun(t, "rt", "network", "chat")
	if !strings.Contains(env.out.String(), "WebSocket clients are never IP-restricted") || !strings.Contains(env.out.String(), "office") {
		t.Fatalf("rt network:\n%s", env.out.String())
	}
	if err := run([]string{"rt", "network", "public", "chat", "off", "--yes"}); err == nil || !strings.Contains(err.Error(), "always public") {
		t.Fatalf("rt public error = %v", err)
	}
}

func TestManagedNetworkAllowRetriesOnceOnConflict(t *testing.T) {
	env := setupDBTest(t, "postgresql")
	var mu sync.Mutex
	revision := 3
	env.dash.on("GET", dbcTestBase+"/network", func(rtRecorded) (int, any) {
		mu.Lock()
		defer mu.Unlock()
		return 200, map[string]any{
			"publicAccess": true, "allowlist": []map[string]any{{"cidr": "10.0.0.0/8", "label": "vpn", "createdAt": "x"}},
			"revision": revision, "allowlistSupported": true, "maxEntries": 64, "allowlistScope": "connections", "publicAccessEditable": true,
		}
	})
	puts := 0
	env.dash.on("PUT", dbcTestBase+"/network/allowlist/update", func(rtRecorded) (int, any) {
		mu.Lock()
		defer mu.Unlock()
		puts++
		if puts == 1 {
			revision = 4
			return http.StatusConflict, map[string]any{"ok": false, "error": map[string]any{"code": "MANAGED_ALLOWLIST_CONFLICT", "message": "changed"}}
		}
		return 200, map[string]any{"operationId": "op_n", "revision": 5, "entries": 2}
	})
	mlTestOperation(env, dbcTestBase, "op_n", "network_update")
	mustRun(t, "db", "network", "allow", "pg", "--my-ip", "--label", "laptop")
	calls := env.dash.find("PUT", dbcTestBase+"/network/allowlist/update")
	if len(calls) != 2 || calls[0].Body["revision"] != float64(3) || calls[1].Body["revision"] != float64(4) {
		t.Fatalf("allowlist calls = %v", calls)
	}
	entries, _ := json.Marshal(calls[1].Body["entries"])
	if string(entries) != `[{"cidr":"10.0.0.0/8","label":"vpn"},{"cidr":"203.0.113.7/32","label":"laptop"}]` {
		t.Fatalf("entries = %s", entries)
	}
	if !strings.Contains(env.out.String(), "Allowed 203.0.113.7/32") {
		t.Fatalf("allow output:\n%s", env.out.String())
	}

	mustRun(t, "db", "network", "allow", "pg", "10.1.1.1/8")
	if len(env.dash.find("PUT", dbcTestBase+"/network/allowlist/update")) != 2 || !strings.Contains(env.out.String(), "already in the allowlist") {
		t.Fatalf("duplicate allow:\n%s", env.out.String())
	}
	mustRun(t, "db", "network", "remove", "pg", "10.0.0.0/8", "--no-wait")
	last := env.dash.find("PUT", dbcTestBase+"/network/allowlist/update")
	if entries, _ := json.Marshal(last[len(last)-1].Body["entries"]); string(entries) != `[]` {
		t.Fatalf("remove entries = %s", entries)
	}
	if !strings.Contains(env.out.String(), "any address with valid credentials can connect again") {
		t.Fatalf("remove output:\n%s", env.out.String())
	}
	if err := run([]string{"db", "network", "remove", "pg", "192.0.2.0/24"}); err == nil || !strings.Contains(err.Error(), "not in the allowlist") {
		t.Fatalf("remove missing error = %v", err)
	}
	if err := run([]string{"db", "network", "allow", "pg"}); err == nil || !strings.Contains(err.Error(), "usage:") {
		t.Fatalf("allow without address error = %v", err)
	}
}

func TestManagedNetworkPublicOffConfirms(t *testing.T) {
	env := setupDBTest(t, "postgresql")
	env.dash.ok("PUT", dbcTestBase+"/network/public-access/update", map[string]any{"operationId": "op_p", "publicAccess": false})
	mlTestOperation(env, dbcTestBase, "op_p", "network_update")
	if err := run([]string{"db", "network", "public", "pg", "off"}); err == nil || !strings.Contains(err.Error(), "confirmation required") {
		t.Fatalf("public off without --yes error = %v", err)
	}
	mustRun(t, "db", "network", "public", "pg", "on")
	if len(env.dash.find("PUT", dbcTestBase+"/network/public-access/update")) != 0 || !strings.Contains(env.out.String(), "already on") {
		t.Fatalf("public on when on:\n%s", env.out.String())
	}
	mustRun(t, "db", "network", "public", "pg", "off", "--yes")
	calls := env.dash.find("PUT", dbcTestBase+"/network/public-access/update")
	if len(calls) != 1 || calls[0].Body["enabled"] != false || !strings.Contains(env.out.String(), "Public access is off") {
		t.Fatalf("public off calls = %v\n%s", calls, env.out.String())
	}
}

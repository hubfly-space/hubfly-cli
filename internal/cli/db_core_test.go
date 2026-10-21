package cli

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Fixtures (fake Dashboard from realtime_commands_test.go)
// ---------------------------------------------------------------------------

const (
	dbcTestBase     = "/api/v1/projects/p-man/managed/instances/mi_pg"
	dbcTestPassword = "s3cr3t/p@ss word"
)

func dbcTestInstance(id, name, engine string) map[string]any {
	instance := rtTestInstance(id, name, managedServiceDatabase)
	instance["engine"] = engine
	instance["versionLabel"] = map[string]string{"postgresql": "PostgreSQL 18", "mysql": "MySQL 8.4 LTS", "libsql": "libSQL server 0.24"}[engine]
	instance["size"] = map[string]any{"cpu": 0.25, "memoryMib": 512, "storageGib": 1}
	instance["publicAccess"] = true
	instance["allowlistCount"] = 0
	instance["deletionProtection"] = false
	return instance
}

func dbcTestConnection(engine string) map[string]any {
	connection := map[string]any{
		"engine": engine, "host": "pg-1a2b.db.hubfly.test", "port": 31234, "database": "app", "username": "admin",
		"publicAccess": true, "ready": true, "secretKind": "password", "credentialsRotatedAt": nil,
		"tls":       map[string]any{"required": true, "verification": "self_signed", "certificateAvailable": true},
		"libsqlUrl": nil, "httpUrl": nil,
	}
	if engine == "mysql" {
		connection["port"] = 31235
	}
	if engine == "libsql" {
		connection["host"], connection["port"], connection["database"], connection["username"] = "sql-1a2b.db.hubfly.test", 443, nil, nil
		connection["libsqlUrl"], connection["httpUrl"] = "libsql://sql-1a2b.db.hubfly.test", "https://sql-1a2b.db.hubfly.test"
		connection["secretKind"] = "token"
	}
	return connection
}

// setupDBTest serves one database "pg" (mi_pg) of the given engine next to the
// realtime server "chat" (mi_chat) in managed project "rt-project" (p-man).
func setupDBTest(t *testing.T, engine string) rtTestEnv {
	t.Helper()
	env := setupRealtimeTest(t)
	instance := dbcTestInstance("mi_pg", "pg", engine)
	env.dash.ok("GET", "/api/v1/projects/p-man/managed/instances", map[string]any{"items": []map[string]any{
		rtTestInstance("mi_chat", "chat", "realtime"), instance,
	}})
	detail := dbcTestInstance("mi_pg", "pg", engine)
	detail["defaultDatabase"], detail["adminUsername"] = "app", "admin"
	env.dash.ok("GET", dbcTestBase, detail)
	env.dash.ok("GET", dbcTestBase+"/connection", dbcTestConnection(engine))
	env.dash.ok("POST", dbcTestBase+"/credentials/reveal", map[string]any{"kind": "password", "username": "admin", "value": dbcTestPassword})
	env.dash.ok("GET", dbcTestBase+"/network", map[string]any{
		"publicAccess": true, "endpoint": map[string]any{"host": "pg-1a2b.db.hubfly.test", "port": 31234},
		"allowlist": []map[string]any{}, "openToAll": true, "revision": 3, "tlsRequired": true,
		"allowlistSupported": true, "maxEntries": 64, "allowlistScope": "connections", "publicAccessEditable": true,
	})
	env.dash.ok("GET", "/api/v1/managed/client-ip", map[string]any{"ip": "203.0.113.7", "family": 4})
	return env
}

func dbcTestCatalog() map[string]any {
	price := func(total float64) map[string]any {
		return map[string]any{"currency": "USD", "hoursPerMonth": 730, "hourly": map[string]any{"total": total / 730}, "monthly": map[string]any{"total": total}}
	}
	versions := func(items ...map[string]any) []map[string]any { return items }
	return map[string]any{
		"available": true,
		"services":  []map[string]any{{"id": "database", "label": "Database", "status": "available"}},
		"engines": []map[string]any{
			{"id": "postgresql", "label": "PostgreSQL", "minMemoryMib": 512, "transport": "tcp", "startingPrice": price(1.92),
				"versions": versions(map[string]any{"id": "18", "label": "PostgreSQL 18", "isDefault": true, "status": "available"},
					map[string]any{"id": "17", "label": "PostgreSQL 17", "status": "available"})},
			{"id": "mysql", "label": "MySQL", "minMemoryMib": 1024, "transport": "tcp", "startingPrice": price(3.82),
				"versions": versions(map[string]any{"id": "8.4", "label": "MySQL 8.4 LTS", "isDefault": true, "lts": true, "status": "available"})},
			{"id": "libsql", "label": "SQLite (libSQL)", "minMemoryMib": 256, "transport": "https", "startingPrice": price(1.92),
				"versions": versions(map[string]any{"id": "0.24", "label": "libSQL server 0.24", "isDefault": true, "status": "available"})},
		},
		"sizes": map[string]any{
			"presets": []map[string]any{
				{"id": "nano", "label": "Nano", "cpu": 0.25, "memoryMib": 512},
				{"id": "small", "label": "Small", "cpu": 0.5, "memoryMib": 1024},
			},
			"cpu":        map[string]any{"min": 0.25, "max": 8, "step": 0.25},
			"memoryMib":  map[string]any{"min": 256, "max": 32768, "step": 256},
			"storageGib": map[string]any{"min": 1, "max": 500, "default": 10},
		},
		"maxAllowlistEntries": 64, "maxInstancesPerAccount": 10,
	}
}

func installDBCreateFixtures(env rtTestEnv) {
	env.dash.ok("GET", "/api/v1/managed/catalog", dbcTestCatalog())
	env.dash.ok("GET", "/api/v1/pricing/managed/estimate", map[string]any{
		"currency": "USD", "hoursPerMonth": 730, "hourly": map[string]any{"total": 0.00263}, "monthly": map[string]any{"total": 1.92},
	})
	created := dbcTestInstance("mi_new", "orders", "postgresql")
	created["status"] = "provisioning"
	env.dash.ok("POST", "/api/v1/projects/p-man/managed/instances/create", map[string]any{
		"instance": created, "operationId": "op_c", "price": map[string]any{"monthly": map[string]any{"total": 1.92}},
	})
	env.dash.ok("GET", "/api/v1/projects/p-man/managed/instances/mi_new/operations/op_c", map[string]any{"id": "op_c", "kind": "create", "status": "succeeded"})
	env.dash.ok("GET", "/api/v1/projects/p-man/managed/instances/mi_new", dbcTestInstance("mi_new", "orders", "postgresql"))
	env.dash.ok("GET", "/api/v1/projects/p-man/managed/instances/mi_new/connection", dbcTestConnection("postgresql"))
}

func assertNoSecret(t *testing.T, env rtTestEnv) {
	t.Helper()
	for _, text := range []string{env.out.String(), env.errOut.String()} {
		if strings.Contains(text, dbcTestPassword) || strings.Contains(text, encodeURIComponent(dbcTestPassword)) {
			t.Fatalf("output leaks the password:\n%s", text)
		}
	}
}

// ---------------------------------------------------------------------------
// Pure helpers
// ---------------------------------------------------------------------------

func TestEncodeURIComponentMatchesJavaScript(t *testing.T) {
	// Expected values from JavaScript's encodeURIComponent.
	cases := map[string]string{
		"abcXYZ019-_.!~*'()": "abcXYZ019-_.!~*'()",
		"p@ss:w/rd?#&=+$, ":  "p%40ss%3Aw%2Frd%3F%23%26%3D%2B%24%2C%20",
		"é":                  "%C3%A9",
	}
	for input, want := range cases {
		if got := encodeURIComponent(input); got != want {
			t.Fatalf("encodeURIComponent(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestBuildDBConnectionURLPerEngine(t *testing.T) {
	port := 31234
	database := "app"
	pg := dbcConnection{Engine: "postgresql", Host: "pg.db.hubfly.test", Port: &port, Database: &database}
	got, err := buildDBConnectionURL(pg, "admin", "a/b@c", "")
	if err != nil || got != "postgresql://admin:a%2Fb%40c@pg.db.hubfly.test:31234/app?sslmode=require" {
		t.Fatalf("postgresql url = %q, %v", got, err)
	}
	mysql := pg
	mysql.Engine = "mysql"
	got, _ = buildDBConnectionURL(mysql, "admin", "x", "shop db")
	if got != "mysql://admin:x@pg.db.hubfly.test:31234/shop%20db?ssl-mode=REQUIRED" {
		t.Fatalf("mysql url = %q", got)
	}
	libsqlURL := "libsql://sql.db.hubfly.test"
	got, _ = buildDBConnectionURL(dbcConnection{Engine: "libsql", Host: "sql.db.hubfly.test", LibsqlURL: &libsqlURL}, "", "", "")
	if got != libsqlURL {
		t.Fatalf("libsql url = %q", got)
	}
	if _, err := buildDBConnectionURL(dbcConnection{Engine: "postgresql"}, "a", "b", ""); err == nil {
		t.Fatal("expected an error without a host")
	}
}

func TestNormalizeAllowlistCIDR(t *testing.T) {
	cases := map[string]string{
		"203.0.113.7":    "203.0.113.7/32",
		" 10.1.2.3/8 ":   "10.0.0.0/8",
		"2001:db8::1":    "2001:db8::1/128",
		"2001:db8::1/32": "2001:db8::/32",
		"::ffff:1.2.3.4": "1.2.3.4/32",
	}
	for input, want := range cases {
		got, err := normalizeAllowlistCIDR(input)
		if err != nil || got != want {
			t.Fatalf("normalizeAllowlistCIDR(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	for _, bad := range []string{"", "300.1.1.1", "10.0.0.0/33", "example.com"} {
		if _, err := normalizeAllowlistCIDR(bad); err == nil {
			t.Fatalf("normalizeAllowlistCIDR(%q) accepted", bad)
		}
	}
}

func TestIPAllowed(t *testing.T) {
	entries := []mlAllowlistEntry{{CIDR: "10.0.0.0/8"}, {CIDR: "2001:db8::/32"}}
	if !ipAllowed("1.2.3.4", nil) {
		t.Fatal("an empty allowlist must allow everyone")
	}
	if !ipAllowed("10.9.8.7", entries) || !ipAllowed("2001:db8::5", entries) {
		t.Fatal("addresses inside the ranges must be allowed")
	}
	if ipAllowed("192.0.2.1", entries) || ipAllowed("not-an-ip", entries) {
		t.Fatal("addresses outside the ranges must be refused")
	}
}

// ---------------------------------------------------------------------------
// list / engines / info
// ---------------------------------------------------------------------------

func TestDBListShowsOnlyDatabases(t *testing.T) {
	env := setupDBTest(t, "postgresql")
	mustRun(t, "db", "list")
	text := env.out.String()
	if !strings.Contains(text, "PostgreSQL 18") || !strings.Contains(text, "mi_pg") || strings.Contains(text, "mi_chat") {
		t.Fatalf("db list output:\n%s", text)
	}
	env.out.buf.Reset()
	mustRun(t, "db", "list", "--json")
	var items []map[string]any
	if err := json.Unmarshal([]byte(env.out.String()), &items); err != nil || len(items) != 1 || items[0]["publicAccess"] != true {
		t.Fatalf("db list --json = %v (%v)", env.out.String(), err)
	}
}

func TestDBEnginesQuotesEveryPreset(t *testing.T) {
	env := setupDBTest(t, "postgresql")
	installDBCreateFixtures(env)
	mustRun(t, "db", "engines")
	if estimates := env.dash.find("GET", "/api/v1/pricing/managed/estimate"); len(estimates) != 2 || estimates[0].Query.Get("storageGib") != "10" {
		t.Fatalf("estimates = %v", estimates)
	}
	text := env.out.String()
	for _, want := range []string{"PostgreSQL 18 (default)", "MySQL 8.4 LTS (default)", "nano", "$1.92/month", "1-500 GiB"} {
		if !strings.Contains(text, want) {
			t.Fatalf("engines output missing %q:\n%s", want, text)
		}
	}
}

func TestDBEnginesUnavailable(t *testing.T) {
	env := setupDBTest(t, "postgresql")
	env.dash.ok("GET", "/api/v1/managed/catalog", map[string]any{"available": false, "engines": []any{}})
	mustRun(t, "db", "engines")
	if !strings.Contains(env.out.String(), "not available in any region") {
		t.Fatalf("output:\n%s", env.out.String())
	}
}

func TestDBInfoShowsConnectionWithoutSecrets(t *testing.T) {
	env := setupDBTest(t, "postgresql")
	mustRun(t, "db", "info", "pg")
	text := env.out.String()
	for _, want := range []string{"pg-1a2b.db.hubfly.test:31234", "Default database:", "app", "hubfly db cert pg", "Deletion protection:", "coming soon"} {
		if !strings.Contains(text, want) {
			t.Fatalf("info output missing %q:\n%s", want, text)
		}
	}
	if len(env.dash.find("POST", dbcTestBase+"/credentials/reveal")) != 0 {
		t.Fatal("info must not reveal credentials")
	}
	env.out.buf.Reset()
	mustRun(t, "db", "info", "pg", "--json")
	var payload map[string]map[string]any
	if err := json.Unmarshal([]byte(env.out.String()), &payload); err != nil || payload["connection"]["host"] != "pg-1a2b.db.hubfly.test" || payload["instance"]["id"] != "mi_pg" {
		t.Fatalf("info --json = %s (%v)", env.out.String(), err)
	}
}

// ---------------------------------------------------------------------------
// create / delete
// ---------------------------------------------------------------------------

func TestDBCreateSendsQuotedRequest(t *testing.T) {
	env := setupDBTest(t, "postgresql")
	installDBCreateFixtures(env)
	mustRun(t, "db", "create", "orders", "--project", "rt-project", "--storage", "1", "--database", "shop",
		"--allow", "10.1.2.3/8,198.51.100.4", "--allow-my-ip", "--yes")

	creates := env.dash.find("POST", "/api/v1/projects/p-man/managed/instances/create")
	if len(creates) != 1 {
		t.Fatalf("create requests = %d", len(creates))
	}
	body := creates[0].Body
	if body["service"] != "database" || body["engine"] != "postgresql" || body["version"] != "18" || body["publicAccess"] != true ||
		body["defaultDatabaseName"] != "shop" || body["quotedHourlyTotal"] != 0.00263 {
		t.Fatalf("create body = %v", body)
	}
	if _, ok := body["adminUsername"]; ok {
		t.Fatalf("adminUsername must never be sent: %v", body)
	}
	if size := body["size"].(map[string]any); size["cpu"] != 0.25 || size["memoryMib"] != float64(512) || size["storageGib"] != float64(1) {
		t.Fatalf("size = %v", size)
	}
	allow, _ := json.Marshal(body["allowlist"])
	if string(allow) != `[{"cidr":"10.0.0.0/8"},{"cidr":"198.51.100.4/32"},{"cidr":"203.0.113.7/32"}]` {
		t.Fatalf("allowlist = %s", allow)
	}
	if key, _ := body["idempotencyKey"].(string); !strings.HasPrefix(key, "cli-db-") {
		t.Fatalf("idempotencyKey = %q", key)
	}
	text := env.out.String()
	for _, want := range []string{"$1.92/month", "is ready", "pg-1a2b.db.hubfly.test:31234", "hubfly db url orders --reveal", "hubfly db bind orders --to <project>"} {
		if !strings.Contains(text, want) {
			t.Fatalf("create output missing %q:\n%s", want, text)
		}
	}
	if len(env.dash.find("POST", "/api/v1/projects/p-man/managed/instances/mi_new/credentials/reveal")) != 0 {
		t.Fatal("create must not reveal the password")
	}
}

func TestDBCreateDefaultsSizeToEngineMinimum(t *testing.T) {
	env := setupDBTest(t, "postgresql")
	installDBCreateFixtures(env)
	mustRun(t, "db", "create", "shop", "--engine", "mysql", "--project", "rt-project", "--private", "--yes", "--no-wait", "--json")
	body := env.dash.find("POST", "/api/v1/projects/p-man/managed/instances/create")[0].Body
	if size := body["size"].(map[string]any); size["memoryMib"] != float64(1024) || size["storageGib"] != float64(10) {
		t.Fatalf("mysql default size = %v", size)
	}
	if body["engine"] != "mysql" || body["version"] != "8.4" || body["publicAccess"] != false {
		t.Fatalf("body = %v", body)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(env.out.String()), &payload); err != nil || payload["operationId"] != "op_c" {
		t.Fatalf("--json stdout is not pure JSON: %q (%v)", env.out.String(), err)
	}
	if !strings.Contains(env.errOut.String(), "$1.92/month") {
		t.Fatalf("the quote must go to stderr with --json:\n%s", env.errOut.String())
	}
}

func TestDBCreateValidationAndConfirmation(t *testing.T) {
	env := setupDBTest(t, "postgresql")
	installDBCreateFixtures(env)
	cases := map[string][]string{
		"confirmation required":         {"db", "create", "orders", "--project", "rt-project"},
		"invalid --engine":              {"db", "create", "orders", "--engine", "oracle"},
		"needs at least 1024":           {"db", "create", "orders", "--engine", "mysql", "--size", "nano", "--project", "rt-project", "--yes"},
		"invalid --cpu":                 {"db", "create", "orders", "--cpu", "0.3", "--memory", "512", "--project", "rt-project", "--yes"},
		"invalid --allow":               {"db", "create", "orders", "--allow", "10.0.0.300"},
		"does not apply to SQLite":      {"db", "create", "orders", "--engine", "sqlite", "--database", "x", "--project", "rt-project", "--yes"},
		"--admin-user is not supported": {"db", "create", "orders", "--admin-user", "owner", "--project", "rt-project", "--yes"},
		"invalid --storage":             {"db", "create", "orders", "--storage", "501", "--project", "rt-project", "--yes"},
	}
	for want, args := range cases {
		err := run(args)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("run(%q) error = %v, want %q", args, err, want)
		}
	}
	if creates := env.dash.find("POST", "/api/v1/projects/p-man/managed/instances/create"); len(creates) != 0 {
		t.Fatalf("invalid creates reached the API: %d", len(creates))
	}
}

func TestDBDeleteConfirmsAndRespectsProtection(t *testing.T) {
	env := setupDBTest(t, "postgresql")
	env.dash.ok("DELETE", dbcTestBase+"/delete", map[string]any{"operationId": "op_d"})
	if err := run([]string{"db", "delete", "pg"}); err == nil || !strings.Contains(err.Error(), "confirmation required") {
		t.Fatalf("delete without --yes error = %v", err)
	}
	rtInteractive = func() bool { return true }
	setTestStdin("nope\n")
	mustRun(t, "db", "delete", "pg")
	if len(env.dash.find("DELETE", dbcTestBase+"/delete")) != 0 {
		t.Fatal("a mismatched name must not delete")
	}
	setTestStdin("pg\n")
	mustRun(t, "db", "delete", "pg")
	deletes := env.dash.find("DELETE", dbcTestBase+"/delete")
	if len(deletes) != 1 || deletes[0].Query.Get("confirmName") != "pg" {
		t.Fatalf("deletes = %v", deletes)
	}

	protected := dbcTestInstance("mi_pg", "pg", "postgresql")
	protected["deletionProtection"] = true
	env.dash.ok("GET", dbcTestBase, protected)
	err := run([]string{"db", "delete", "pg", "--yes"})
	if err == nil || !strings.Contains(err.Error(), "hubfly db protect pg off") {
		t.Fatalf("protected delete error = %v", err)
	}
}

func TestDBDeleteWaitTreatsGoneAsDone(t *testing.T) {
	env := setupDBTest(t, "postgresql")
	env.dash.ok("DELETE", dbcTestBase+"/delete", map[string]any{"operationId": "op_d"})
	mustRun(t, "db", "delete", "pg", "--yes", "--wait")
	if !strings.Contains(env.out.String(), "Database 'pg' deleted.") {
		t.Fatalf("output:\n%s", env.out.String())
	}
}

// ---------------------------------------------------------------------------
// url / env / credentials
// ---------------------------------------------------------------------------

func TestDBURLMasksPasswordUnlessRevealed(t *testing.T) {
	env := setupDBTest(t, "postgresql")
	mustRun(t, "db", "url", "pg")
	if got := strings.TrimSpace(env.out.String()); got != "postgresql://admin:****@pg-1a2b.db.hubfly.test:31234/app?sslmode=require" {
		t.Fatalf("masked url = %q", got)
	}
	if len(env.dash.find("POST", dbcTestBase+"/credentials/reveal")) != 0 {
		t.Fatal("url without --reveal must not reveal")
	}
	assertNoSecret(t, env)

	env.out.buf.Reset()
	mustRun(t, "db", "url", "pg", "--reveal", "--database", "other", "--json")
	var payload map[string]any
	if err := json.Unmarshal([]byte(env.out.String()), &payload); err != nil {
		t.Fatalf("--json stdout is not pure JSON: %q", env.out.String())
	}
	if payload["url"] != "postgresql://admin:s3cr3t%2Fp%40ss%20word@pg-1a2b.db.hubfly.test:31234/other?sslmode=require" || payload["passwordRevealed"] != true {
		t.Fatalf("revealed url = %v", payload)
	}
	if !strings.Contains(env.errOut.String(), "audit log") {
		t.Fatalf("reveal must mention the audit log:\n%s", env.errOut.String())
	}
}

func TestDBEnvFormats(t *testing.T) {
	env := setupDBTest(t, "mysql")
	mustRun(t, "db", "env", "pg")
	if got := env.out.String(); got != "DATABASE_URL=\"mysql://admin:****@pg-1a2b.db.hubfly.test:31235/app?ssl-mode=REQUIRED\"\n" {
		t.Fatalf("dotenv = %q", got)
	}
	env.out.buf.Reset()
	mustRun(t, "db", "env", "pg", "--reveal", "--format", "json")
	var values map[string]string
	if err := json.Unmarshal([]byte(env.out.String()), &values); err != nil || !strings.Contains(values["DATABASE_URL"], encodeURIComponent(dbcTestPassword)) {
		t.Fatalf("json env = %q (%v)", env.out.String(), err)
	}
}

func TestDBEnvLibsqlNeverInventsAToken(t *testing.T) {
	env := setupDBTest(t, "libsql")
	mustRun(t, "db", "env", "pg")
	if got := env.out.String(); got != "LIBSQL_URL=libsql://sql-1a2b.db.hubfly.test\n" {
		t.Fatalf("libsql env = %q", got)
	}
	if !strings.Contains(env.errOut.String(), "hubfly db tokens create pg") {
		t.Fatalf("token hint missing:\n%s", env.errOut.String())
	}
	if len(env.dash.find("POST", dbcTestBase+"/credentials/reveal")) != 0 {
		t.Fatal("libsql env must not reveal")
	}
}

func TestDBURLNotReady(t *testing.T) {
	env := setupDBTest(t, "postgresql")
	connection := dbcTestConnection("postgresql")
	connection["ready"] = false
	env.dash.ok("GET", dbcTestBase+"/connection", connection)
	if err := run([]string{"db", "url", "pg"}); err == nil || !strings.Contains(err.Error(), "still being prepared") {
		t.Fatalf("error = %v", err)
	}
}

func TestDBCredentialsShowAndRotate(t *testing.T) {
	env := setupDBTest(t, "postgresql")
	mustRun(t, "db", "credentials", "pg")
	if !strings.Contains(env.out.String(), "****") {
		t.Fatalf("credentials output:\n%s", env.out.String())
	}
	assertNoSecret(t, env)

	env.dash.ok("POST", dbcTestBase+"/credentials/rotate", map[string]any{"kind": "password", "username": "admin", "value": "n3w-secret"})
	if err := run([]string{"db", "credentials", "rotate", "pg"}); err == nil || !strings.Contains(err.Error(), "confirmation required") {
		t.Fatalf("rotate without --yes error = %v", err)
	}
	if len(env.dash.find("POST", dbcTestBase+"/credentials/rotate")) != 0 {
		t.Fatal("rotate ran without confirmation")
	}
	mustRun(t, "db", "credentials", "rotate", "pg", "--yes")
	if strings.Count(env.out.String(), "n3w-secret") != 1 {
		t.Fatalf("the new password must be printed once:\n%s", env.out.String())
	}
}

func TestDBCredentialsRotateLibsqlSigningKey(t *testing.T) {
	env := setupDBTest(t, "libsql")
	env.dash.ok("POST", dbcTestBase+"/credentials/rotate", map[string]any{"kind": "signing_key", "revokedTokens": 3, "appsReconnected": 1})
	mustRun(t, "db", "credentials", "rotate", "pg", "--yes")
	if !strings.Contains(env.out.String(), "3 tokens revoked, 1 app bindings received a new token") {
		t.Fatalf("output:\n%s", env.out.String())
	}
}

func TestCredentialRequestsNeverReachTheDebugLog(t *testing.T) {
	env := setupDBTest(t, "postgresql")
	logFile := filepath.Join(t.TempDir(), "stderr")
	file, err := os.Create(logFile)
	if err != nil {
		t.Fatal(err)
	}
	origStderr := os.Stderr
	os.Stderr = file
	debugEnabled = true
	runErr := run([]string{"db", "url", "pg", "--reveal"})
	os.Stderr = origStderr
	_ = file.Close()
	if runErr != nil {
		t.Fatal(runErr)
	}
	if !debugEnabled {
		t.Fatal("debug logging must be restored after the reveal")
	}
	logged, _ := os.ReadFile(logFile)
	if !strings.Contains(string(logged), "[debug]") {
		t.Fatal("expected other requests in the debug log")
	}
	if strings.Contains(string(logged), dbcTestPassword) || strings.Contains(string(logged), "credentials/reveal") {
		t.Fatalf("debug log leaks the reveal:\n%s", logged)
	}
	_ = env
}

// ---------------------------------------------------------------------------
// connect / shell
// ---------------------------------------------------------------------------

// installStubClient puts a fake psql/mysql in a temp dir that records its
// argv, PGPASSWORD/PGSSLMODE and (for mysql) the option file and its mode.
func installStubClient(t *testing.T, name string) (string, func() string) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("the stub client uses GNU stat")
	}
	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	script := "#!/bin/sh\n{\n  echo \"ARGS:$*\"\n  echo \"PGPASSWORD:$PGPASSWORD\"\n  echo \"PGSSLMODE:$PGSSLMODE\"\n" +
		"  for a in \"$@\"; do case \"$a\" in --defaults-extra-file=*) f=\"${a#--defaults-extra-file=}\"; echo \"MODE:$(stat -c %a \"$f\")\"; echo \"FILE:$f\"; cat \"$f\";; esac; done\n" +
		"} > \"" + record + "\"\nexit 3\n"
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	origLook, origExit := dbcLookPath, dbcExit
	t.Cleanup(func() { dbcLookPath, dbcExit = origLook, origExit })
	dbcLookPath = func(file string) (string, error) {
		if file != name {
			t.Fatalf("looked up %q, want %q", file, name)
		}
		return path, nil
	}
	return path, func() string {
		data, _ := os.ReadFile(record)
		return string(data)
	}
}

func TestDBConnectPostgresPassesPasswordOnlyInEnvironment(t *testing.T) {
	env := setupDBTest(t, "postgresql")
	_, recorded := installStubClient(t, "psql")
	exitCode := -1
	dbcExit = func(code int) { exitCode = code }
	mustRun(t, "db", "connect", "pg", "--database", "other", "--", "-c", "select 1")
	record := recorded()
	if !strings.Contains(record, "ARGS:-h pg-1a2b.db.hubfly.test -p 31234 -U admin -d other -c select 1") {
		t.Fatalf("psql argv:\n%s", record)
	}
	if !strings.Contains(record, "PGPASSWORD:"+dbcTestPassword) || !strings.Contains(record, "PGSSLMODE:require") {
		t.Fatalf("psql env:\n%s", record)
	}
	argsLine := strings.SplitN(record, "\n", 2)[0]
	if strings.Contains(argsLine, "s3cr3t") {
		t.Fatalf("password on argv: %s", argsLine)
	}
	if exitCode != 3 {
		t.Fatalf("exit code = %d, want the client's 3", exitCode)
	}
	assertNoSecret(t, env)
}

func TestDBConnectMySQLUsesPrivateOptionFile(t *testing.T) {
	env := setupDBTest(t, "mysql")
	_, recorded := installStubClient(t, "mysql")
	dbcExit = func(int) {}
	mustRun(t, "db", "connect", "pg")
	record := recorded()
	if !strings.Contains(record, "MODE:600") || !strings.Contains(record, `password="`+dbcTestPassword+`"`) || !strings.Contains(record, "ssl-mode=REQUIRED") {
		t.Fatalf("mysql option file:\n%s", record)
	}
	argsLine := strings.SplitN(record, "\n", 2)[0]
	if strings.Contains(argsLine, "s3cr3t") || !strings.HasSuffix(argsLine, " app") {
		t.Fatalf("mysql argv: %s", argsLine)
	}
	for _, line := range strings.Split(record, "\n") {
		if strings.HasPrefix(line, "FILE:") {
			if _, err := os.Stat(strings.TrimPrefix(line, "FILE:")); !os.IsNotExist(err) {
				t.Fatalf("option file was not removed (%v)", err)
			}
		}
	}
	assertNoSecret(t, env)
}

func TestDBConnectChecksAllowlistAndClient(t *testing.T) {
	env := setupDBTest(t, "postgresql")
	env.dash.ok("GET", dbcTestBase+"/network", map[string]any{
		"publicAccess": true, "allowlist": []map[string]any{{"cidr": "10.0.0.0/8", "label": nil, "createdAt": "x"}},
		"revision": 1, "allowlistSupported": true, "maxEntries": 64, "allowlistScope": "connections",
	})
	err := run([]string{"db", "connect", "pg"})
	if err == nil || !strings.Contains(err.Error(), "hubfly db network allow pg --my-ip") {
		t.Fatalf("error = %v", err)
	}
	if len(env.dash.find("POST", dbcTestBase+"/credentials/reveal")) != 0 {
		t.Fatal("connect must not reveal before the checks pass")
	}

	env.dash.ok("GET", "/api/v1/managed/client-ip", map[string]any{"ip": "10.2.3.4", "family": 4})
	origLook := dbcLookPath
	t.Cleanup(func() { dbcLookPath = origLook })
	dbcLookPath = func(string) (string, error) { return "", os.ErrNotExist }
	err = run([]string{"db", "connect", "pg"})
	if err == nil || !strings.Contains(err.Error(), "postgresql-client") {
		t.Fatalf("missing client error = %v", err)
	}
}

func TestDBConnectLibsqlExplainsHTTPS(t *testing.T) {
	env := setupDBTest(t, "libsql")
	mustRun(t, "db", "connect", "pg")
	if !strings.Contains(env.out.String(), "HTTPS") || !strings.Contains(env.out.String(), "hubfly db query pg") {
		t.Fatalf("output:\n%s", env.out.String())
	}
}

func TestDBShellChecks(t *testing.T) {
	env := setupDBTest(t, "libsql")
	if err := run([]string{"db", "shell", "pg"}); err == nil || !strings.Contains(err.Error(), "hubfly db query pg") {
		t.Fatalf("libsql shell error = %v", err)
	}
	stopped := dbcTestInstance("mi_pg", "pg", "postgresql")
	stopped["status"] = "stopped"
	env.dash.ok("GET", "/api/v1/projects/p-man/managed/instances", map[string]any{"items": []map[string]any{stopped}})
	if err := run([]string{"db", "shell", "pg"}); err == nil || !strings.Contains(err.Error(), "hubfly db start pg") {
		t.Fatalf("stopped shell error = %v", err)
	}
	if len(env.dash.find("POST", dbcTestBase+"/shell/session")) != 0 {
		t.Fatal("no session should be requested")
	}
}

// ---------------------------------------------------------------------------
// bind
// ---------------------------------------------------------------------------

func TestDBBindCreateListRemove(t *testing.T) {
	env := setupDBTest(t, "postgresql")
	binding := map[string]any{
		"id": "mb_1", "targetProjectId": "p-web", "targetProjectName": "web", "envKey": "ORDERS_URL",
		"authTokenEnvKey": nil, "databaseName": "app", "username": "app_web", "dedicatedUser": true, "createdAt": "x",
	}
	env.dash.ok("POST", dbcTestBase+"/bindings/create", binding)
	env.dash.ok("GET", dbcTestBase+"/bindings", map[string]any{"items": []map[string]any{binding}})
	env.dash.ok("DELETE", dbcTestBase+"/bindings/mb_1/delete", map[string]any{"deleted": true})

	if err := run([]string{"db", "bind", "pg", "--to", "web", "--access", "read_only"}); err == nil || !strings.Contains(err.Error(), "--new-user") {
		t.Fatalf("--access without --new-user error = %v", err)
	}
	if err := run([]string{"db", "bind", "pg", "--to", "rt-project"}); err == nil || !strings.Contains(err.Error(), "Cell projects only") {
		t.Fatalf("managed target error = %v", err)
	}
	mustRun(t, "db", "bind", "pg", "--to", "web", "--env", "ORDERS_URL", "--new-user", "--access", "read_only", "--database", "app")
	creates := env.dash.find("POST", dbcTestBase+"/bindings/create")
	if len(creates) != 1 {
		t.Fatalf("binding creates = %d", len(creates))
	}
	body := creates[0].Body
	if body["targetProjectId"] != "p-web" || body["envKey"] != "ORDERS_URL" || body["createUser"] != true || body["access"] != "read_only" || body["databaseName"] != "app" {
		t.Fatalf("binding body = %v", body)
	}
	if !strings.Contains(env.out.String(), "next deploy") || !strings.Contains(env.out.String(), "app_web") {
		t.Fatalf("bind output:\n%s", env.out.String())
	}

	env.out.buf.Reset()
	mustRun(t, "db", "bind", "list", "pg")
	if !strings.Contains(env.out.String(), "app_web (dedicated)") {
		t.Fatalf("bind list:\n%s", env.out.String())
	}
	if err := run([]string{"db", "bind", "remove", "pg", "mb_1"}); err == nil || !strings.Contains(err.Error(), "confirmation required") {
		t.Fatalf("remove without --yes error = %v", err)
	}
	mustRun(t, "db", "bind", "remove", "pg", "mb_1", "--keep-variable", "--yes")
	mustRun(t, "db", "bind", "remove", "pg", "mb_1", "--yes")
	deletes := env.dash.find(http.MethodDelete, dbcTestBase+"/bindings/mb_1/delete")
	if len(deletes) != 2 || deletes[0].Query.Get("removeVariable") != "false" || deletes[1].Query.Get("removeVariable") != "true" {
		t.Fatalf("binding deletes = %v", deletes)
	}
}

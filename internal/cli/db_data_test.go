package cli

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// Fixtures: a PostgreSQL, a MySQL and a libSQL instance next to the realtime
// fixtures of setupRealtimeTest.
// ---------------------------------------------------------------------------

const (
	dbdTestPG     = "/api/v1/projects/p-man/managed/instances/mi_pg"
	dbdTestMySQL  = "/api/v1/projects/p-man/managed/instances/mi_my"
	dbdTestLibSQL = "/api/v1/projects/p-man/managed/instances/mi_sql"
)

func dbdTestInstance(id, name, engine string) map[string]any {
	instance := rtTestInstance(id, name, "database")
	instance["engine"] = engine
	return instance
}

func dbdTestDetail(id, name, engine string) map[string]any {
	detail := dbdTestInstance(id, name, engine)
	caps := map[string]any{
		"databases": true, "users": true, "tokens": false, "query": true, "config": true, "certificate": true,
		"extensions": engine == "postgresql", "insights": true, "sessions": true, "export": true, "import": true,
	}
	detail["defaultDatabase"] = "app"
	detail["adminUsername"] = "hubfly_admin"
	detail["endpoint"] = map[string]any{"host": name + ".db.hubfly.test", "port": 5432}
	switch engine {
	case "postgresql":
		detail["engineLabel"] = "PostgreSQL"
	case "mysql":
		detail["engineLabel"] = "MySQL"
	case "libsql":
		detail["engineLabel"] = "SQLite (libSQL)"
		detail["defaultDatabase"] = nil
		detail["adminUsername"] = nil
		detail["endpoint"] = map[string]any{"host": name + ".db.hubfly.test", "port": nil}
		for _, key := range []string{"databases", "users", "config", "certificate", "insights", "sessions"} {
			caps[key] = false
		}
		caps["tokens"] = true
	}
	detail["capabilities"] = caps
	return detail
}

func setupDBDTest(t *testing.T) rtTestEnv {
	t.Helper()
	env := setupRealtimeTest(t)
	origFollow := moFollowContext
	t.Cleanup(func() { moFollowContext = origFollow })
	env.dash.ok("GET", "/api/v1/projects/p-man/managed/instances", map[string]any{"items": []map[string]any{
		rtTestInstance("mi_chat", "chat", "realtime"),
		dbdTestInstance("mi_pg", "pg", "postgresql"),
		dbdTestInstance("mi_my", "my", "mysql"),
		dbdTestInstance("mi_sql", "lite", "libsql"),
	}})
	env.dash.ok("GET", dbdTestPG, dbdTestDetail("mi_pg", "pg", "postgresql"))
	env.dash.ok("GET", dbdTestMySQL, dbdTestDetail("mi_my", "my", "mysql"))
	env.dash.ok("GET", dbdTestLibSQL, dbdTestDetail("mi_sql", "lite", "libsql"))
	return env
}

func dbdSucceed(env rtTestEnv, base, operationID string) {
	env.dash.ok("GET", base+"/operations/"+operationID, map[string]any{"id": operationID, "kind": "export", "status": "succeeded"})
}

func dbdRunErr(t *testing.T, want string, args ...string) {
	t.Helper()
	err := run(args)
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("run(%q) error = %v, want it to contain %q", args, err, want)
	}
}

func dbdChdir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
	return dir
}

// ---------------------------------------------------------------------------
// query
// ---------------------------------------------------------------------------

func dbdQueryResponse() map[string]any {
	return map[string]any{
		"readOnly": true, "durationMs": 3.2,
		"statements": []map[string]any{{
			"index": 0, "command": "SELECT", "status": "ok",
			"columns":  []map[string]any{{"name": "id", "type": "int4", "kind": "number"}, {"name": "note", "type": "text", "kind": "text"}},
			"rows":     [][]any{{"1", "hello"}, {"22", nil}, {"3", "a\nb"}},
			"rowCount": 3, "truncated": false, "durationMs": 1.1, "error": nil,
		}},
		"notices": []map[string]any{{"severity": "notice", "code": "00000", "message": "just saying"}},
	}
}

func TestDBQueryRequestAndTable(t *testing.T) {
	env := setupDBDTest(t)
	env.dash.ok("POST", dbdTestPG+"/query", dbdQueryResponse())

	mustRun(t, "db", "query", "pg", "SELECT id, note FROM t", "--database", "other", "--max-rows", "50", "--timeout", "10s")
	requests := env.dash.find("POST", dbdTestPG+"/query")
	if len(requests) != 1 {
		t.Fatalf("query requests = %d", len(requests))
	}
	body := requests[0].Body
	want := map[string]any{"sql": "SELECT id, note FROM t", "readOnly": true, "database": "other", "maxRows": float64(50), "timeoutMs": float64(10000)}
	if !reflect.DeepEqual(body, want) {
		t.Fatalf("query body = %#v", body)
	}
	out := env.out.String()
	for _, fragment := range []string{"id  note", "--  -----", " 1  hello", "22  NULL", " 3  a\\nb", "(3 rows)"} {
		if !strings.Contains(out, fragment) {
			t.Fatalf("table output missing %q:\n%s", fragment, out)
		}
	}
	if !strings.Contains(env.errOut.String(), "NOTICE: just saying (code 00000)") {
		t.Fatalf("notices on stderr:\n%s", env.errOut.String())
	}
}

func TestDBQueryInputSources(t *testing.T) {
	env := setupDBDTest(t)
	env.dash.ok("POST", dbdTestPG+"/query", dbdQueryResponse())
	path := filepath.Join(t.TempDir(), "q.sql")
	if err := os.WriteFile(path, []byte("SELECT 2"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "db", "query", "pg", "--file", path)
	rtStdin = strings.NewReader("SELECT 3")
	mustRun(t, "db", "query", "pg", "--file", "-")
	rtStdinIsTerminal = func() bool { return false }
	rtStdin = strings.NewReader("SELECT 4")
	mustRun(t, "db", "query", "pg")
	requests := env.dash.find("POST", dbdTestPG+"/query")
	got := []any{}
	for _, request := range requests {
		got = append(got, request.Body["sql"])
	}
	if !reflect.DeepEqual(got, []any{"SELECT 2", "SELECT 3", "SELECT 4"}) {
		t.Fatalf("sql sent = %v", got)
	}

	before := len(env.dash.find("POST", dbdTestPG+"/query"))
	rtStdinIsTerminal = func() bool { return true }
	dbdRunErr(t, "pass the SQL", "db", "query", "pg")
	dbdRunErr(t, "not both", "db", "query", "pg", "SELECT 1", "--file", path)
	dbdRunErr(t, "invalid --format", "db", "query", "pg", "SELECT 1", "--format", "xml")
	dbdRunErr(t, "invalid --max-rows", "db", "query", "pg", "SELECT 1", "--max-rows", "9000")
	dbdRunErr(t, "invalid --timeout", "db", "query", "pg", "SELECT 1", "--timeout", "5m")
	if after := len(env.dash.find("POST", dbdTestPG+"/query")); after != before {
		t.Fatalf("invalid queries reached the Dashboard")
	}
}

func TestDBQueryJSONAndCSV(t *testing.T) {
	env := setupDBDTest(t)
	env.dash.ok("POST", dbdTestPG+"/query", dbdQueryResponse())
	mustRun(t, "db", "query", "pg", "SELECT 1", "--format", "json")
	var decoded map[string]any
	if err := json.Unmarshal([]byte(env.out.String()), &decoded); err != nil {
		t.Fatalf("--format json is not pure JSON: %v\n%s", err, env.out.String())
	}
	if _, ok := decoded["statements"]; !ok {
		t.Fatalf("json output lacks statements: %v", decoded)
	}

	env.out.buf.Reset()
	mustRun(t, "db", "query", "pg", "SELECT 1", "--format", "csv")
	if got, want := env.out.String(), "id,note\n1,hello\n22,\n3,\"a\nb\"\n"; got != want {
		t.Fatalf("csv = %q, want %q", got, want)
	}

	two := dbdQueryResponse()
	statements := two["statements"].([]map[string]any)
	two["statements"] = []map[string]any{statements[0], statements[0]}
	env.dash.ok("POST", dbdTestPG+"/query", two)
	dbdRunErr(t, "one result set", "db", "query", "pg", "SELECT 1; SELECT 2", "--format", "csv")
}

func TestDBQueryStatementErrorAndReadOnlyHint(t *testing.T) {
	env := setupDBDTest(t)
	env.dash.ok("POST", dbdTestPG+"/query", map[string]any{
		"readOnly": true, "durationMs": 1,
		"statements": []map[string]any{{
			"index": 0, "command": nil, "status": "error", "columns": []any{}, "rows": []any{}, "rowCount": nil, "truncated": false, "durationMs": 1,
			"error": map[string]any{"message": "cannot execute INSERT in a read-only transaction", "code": "25006", "position": 1, "detail": nil, "hint": "use a writable transaction"},
		}},
		"notices": []any{},
	})
	dbdRunErr(t, "re-run with --write", "db", "query", "pg", "INSERT INTO t VALUES (1)")
	errText := env.errOut.String()
	for _, fragment := range []string{"ERROR (statement 1): cannot execute INSERT", "code 25006, position 1", "Hint: use a writable transaction"} {
		if !strings.Contains(errText, fragment) {
			t.Fatalf("stderr missing %q:\n%s", fragment, errText)
		}
	}
	err := run([]string{"db", "query", "pg", "INSERT INTO t VALUES (1)", "--write"})
	if err == nil || strings.Contains(err.Error(), "--write") {
		t.Fatalf("--write run error = %v", err)
	}
	if body := env.dash.find("POST", dbdTestPG+"/query")[1].Body; body["readOnly"] != false {
		t.Fatalf("--write body = %v", body)
	}
}

func TestDBQueryTruncationNotice(t *testing.T) {
	env := setupDBDTest(t)
	response := dbdQueryResponse()
	statement := response["statements"].([]map[string]any)[0]
	statement["truncated"] = true
	statement["rowCount"] = 1200
	env.dash.ok("POST", dbdTestPG+"/query", response)
	mustRun(t, "db", "query", "pg", "SELECT * FROM big")
	if !strings.Contains(env.out.String(), "(showing the first 3 of 1,200 rows; raise --max-rows (up to 5000) or add a LIMIT)") {
		t.Fatalf("truncation notice:\n%s", env.out.String())
	}
}

// ---------------------------------------------------------------------------
// databases
// ---------------------------------------------------------------------------

func TestDBDatabasesListCreateDelete(t *testing.T) {
	env := setupDBDTest(t)
	env.dash.ok("GET", dbdTestPG+"/databases", map[string]any{"items": []map[string]any{
		{"name": "app", "owner": "hubfly_admin", "sizeBytes": 8 * 1024 * 1024, "encoding": "UTF8", "collation": "C", "isDefault": true, "isSystem": false},
		{"name": "postgres", "owner": "postgres", "sizeBytes": nil, "encoding": "UTF8", "collation": "C", "isDefault": false, "isSystem": true},
		{"name": "shop", "owner": "hubfly_admin", "sizeBytes": 1024, "encoding": "UTF8", "collation": "C", "isDefault": false, "isSystem": false},
	}})
	mustRun(t, "db", "databases", "pg")
	out := env.out.String()
	if !strings.Contains(out, "default") || !strings.Contains(out, "system") || !strings.Contains(out, "8.0 MB") {
		t.Fatalf("databases list:\n%s", out)
	}

	env.dash.ok("POST", dbdTestPG+"/databases/create", map[string]any{"name": "blog"})
	mustRun(t, "db", "databases", "create", "pg", "blog", "--owner", "writer")
	if body := env.dash.find("POST", dbdTestPG+"/databases/create")[0].Body; !reflect.DeepEqual(body, map[string]any{"name": "blog", "owner": "writer"}) {
		t.Fatalf("create body = %v", body)
	}

	deletePath := dbdTestPG + "/databases/shop/delete"
	env.dash.ok("DELETE", deletePath, map[string]any{"dropped": true})
	dbdRunErr(t, "--yes", "db", "databases", "delete", "pg", "shop")
	dbdRunErr(t, "default database", "db", "databases", "delete", "pg", "app", "--yes")
	dbdRunErr(t, "system database", "db", "databases", "delete", "pg", "postgres", "--yes")
	dbdRunErr(t, "not found", "db", "databases", "delete", "pg", "nope", "--yes")
	rtInteractive = func() bool { return true }
	setTestStdin("shopx\n")
	mustRun(t, "db", "databases", "delete", "pg", "shop")
	if len(env.dash.find("DELETE", deletePath)) != 0 {
		t.Fatal("dropped after a wrong typed name")
	}
	setTestStdin("shop\n")
	mustRun(t, "db", "databases", "delete", "pg", "shop", "--force")
	deletes := env.dash.find("DELETE", deletePath)
	if len(deletes) != 1 || deletes[0].Query.Get("confirmName") != "shop" || deletes[0].Query.Get("force") != "true" {
		t.Fatalf("delete requests = %+v", deletes)
	}
}

func TestDBDataCapabilityErrors(t *testing.T) {
	env := setupDBDTest(t)
	dbdRunErr(t, "uses access tokens instead of database users", "db", "users", "lite")
	dbdRunErr(t, "single database", "db", "databases", "lite")
	dbdRunErr(t, "access tokens are only used by SQLite", "db", "tokens", "pg")
	dbdRunErr(t, "extensions are available for PostgreSQL only", "db", "extensions", "my")
	dbdRunErr(t, "publicly trusted certificate", "db", "cert", "lite")
	dbdRunErr(t, "HubFly manages its settings", "db", "config", "lite")
	dbdRunErr(t, "available for PostgreSQL and MySQL", "db", "sessions", "lite")
	dbdRunErr(t, "database 'chat' not found", "db", "users", "chat")
	for _, request := range env.dash.find("GET", dbdTestLibSQL+"/users") {
		t.Fatalf("users endpoint called for libSQL: %v", request)
	}
}

// ---------------------------------------------------------------------------
// users
// ---------------------------------------------------------------------------

func dbdTestUsers() map[string]any {
	return map[string]any{"items": []map[string]any{
		{"name": "hubfly_admin", "preset": "admin", "databases": []string{}, "allDatabases": true, "isPrimary": true, "canLogin": true},
		{"name": "reader", "preset": "read_only", "databases": []string{"app"}, "allDatabases": false, "isPrimary": false, "canLogin": true},
	}}
}

func TestDBUsersCreatePrintsPasswordOnce(t *testing.T) {
	env := setupDBDTest(t)
	env.dash.ok("POST", dbdTestPG+"/users/create", map[string]any{
		"user":     map[string]any{"name": "writer", "preset": "read_write", "databases": []string{"app", "shop"}, "allDatabases": false, "isPrimary": false, "canLogin": true},
		"password": "pw-secret-123",
	})
	mustRun(t, "db", "users", "create", "pg", "writer", "--databases", "app,shop")
	body := env.dash.find("POST", dbdTestPG+"/users/create")[0].Body
	want := map[string]any{"name": "writer", "preset": "read_write", "databases": []any{"app", "shop"}, "allDatabases": false}
	if !reflect.DeepEqual(body, want) {
		t.Fatalf("create body = %#v", body)
	}
	if strings.Count(env.out.String(), "pw-secret-123") != 1 || !strings.Contains(env.errOut.String(), "printed once") {
		t.Fatalf("password output:\nstdout %s\nstderr %s", env.out.String(), env.errOut.String())
	}

	env.out.buf.Reset()
	mustRun(t, "db", "users", "create", "pg", "boss", "--preset", "admin", "--json")
	var decoded map[string]any
	if err := json.Unmarshal([]byte(env.out.String()), &decoded); err != nil || decoded["password"] != "pw-secret-123" {
		t.Fatalf("--json output = %q (%v)", env.out.String(), err)
	}
	if body := env.dash.find("POST", dbdTestPG+"/users/create")[1].Body; body["allDatabases"] != true || body["preset"] != "admin" {
		t.Fatalf("admin body = %v", body)
	}
	dbdRunErr(t, "invalid --preset", "db", "users", "create", "pg", "x", "--preset", "superuser")
	dbdRunErr(t, "not both", "db", "users", "create", "pg", "x", "--databases", "app", "--all-databases")
}

func TestDBUsersUpdateDeleteRotate(t *testing.T) {
	env := setupDBDTest(t)
	env.dash.ok("GET", dbdTestPG+"/users", dbdTestUsers())
	env.dash.ok("GET", dbdTestPG+"/bindings", map[string]any{"items": []map[string]any{
		{"id": "b1", "targetProjectName": "web", "envKey": "DATABASE_URL", "username": "reader", "dedicatedUser": true},
	}})
	updatePath := dbdTestPG + "/users/reader/update"
	env.dash.ok("PUT", updatePath, map[string]any{"updated": true})
	mustRun(t, "db", "users", "update", "pg", "reader", "--preset", "read_write")
	if body := env.dash.find("PUT", updatePath)[0].Body; !reflect.DeepEqual(body, map[string]any{"preset": "read_write", "allDatabases": false, "databases": []any{"app"}}) {
		t.Fatalf("update keeps databases: %#v", body)
	}
	mustRun(t, "db", "users", "update", "pg", "reader", "--all-databases")
	if body := env.dash.find("PUT", updatePath)[1].Body; !reflect.DeepEqual(body, map[string]any{"preset": "read_only", "allDatabases": true}) {
		t.Fatalf("update keeps preset: %#v", body)
	}
	dbdRunErr(t, "nothing to update", "db", "users", "update", "pg", "reader")
	dbdRunErr(t, "primary admin", "db", "users", "update", "pg", "hubfly_admin", "--preset", "read_only")

	deletePath := dbdTestPG + "/users/reader/delete"
	env.dash.ok("DELETE", deletePath, map[string]any{"dropped": true})
	dbdRunErr(t, "cannot be deleted", "db", "users", "delete", "pg", "hubfly_admin", "--yes")
	dbdRunErr(t, "--yes", "db", "users", "delete", "pg", "reader")
	rtInteractive = func() bool { return true }
	setTestStdin("y\n")
	mustRun(t, "db", "users", "delete", "pg", "reader", "--reassign-to", "hubfly_admin")
	if deletes := env.dash.find("DELETE", deletePath); len(deletes) != 1 || deletes[0].Query.Get("reassignOwnedTo") != "hubfly_admin" {
		t.Fatalf("delete requests = %+v", deletes)
	}

	rtInteractive = func() bool { return false }
	rotatePath := dbdTestPG + "/users/reader/password/rotate"
	env.dash.ok("POST", rotatePath, map[string]any{"name": "reader", "password": "new-pw-456"})
	dbdRunErr(t, "credentials rotate", "db", "users", "rotate", "pg", "hubfly_admin", "--yes")
	env.out.buf.Reset()
	mustRun(t, "db", "users", "rotate", "pg", "reader", "--yes", "--json")
	var decoded map[string]any
	if err := json.Unmarshal([]byte(env.out.String()), &decoded); err != nil || decoded["password"] != "new-pw-456" {
		t.Fatalf("rotate --json = %q (%v)", env.out.String(), err)
	}
	if !strings.Contains(env.errOut.String(), "web (DATABASE_URL)") {
		t.Fatalf("rotate warns about bound apps:\n%s", env.errOut.String())
	}
}

// ---------------------------------------------------------------------------
// tokens / extensions
// ---------------------------------------------------------------------------

func TestDBTokensLibSQL(t *testing.T) {
	env := setupDBDTest(t)
	env.dash.ok("POST", dbdTestLibSQL+"/tokens/create", map[string]any{
		"token": map[string]any{"id": "tok_1", "name": "ci", "access": "read_only", "createdAt": "2026-10-20T10:00:00Z", "expiresAt": "2026-11-19T10:00:00Z", "active": true},
		"value": "eyJ.token.value",
	})
	mustRun(t, "db", "tokens", "create", "lite", "ci", "--access", "read_only", "--expires", "30")
	if body := env.dash.find("POST", dbdTestLibSQL+"/tokens/create")[0].Body; !reflect.DeepEqual(body, map[string]any{"name": "ci", "access": "read_only", "expiresInDays": float64(30)}) {
		t.Fatalf("token body = %v", body)
	}
	if strings.Count(env.out.String(), "eyJ.token.value") != 1 || !strings.Contains(env.out.String(), "libsql://lite.db.hubfly.test") {
		t.Fatalf("token output:\n%s", env.out.String())
	}
	mustRun(t, "db", "tokens", "create", "lite", "app")
	if body := env.dash.find("POST", dbdTestLibSQL+"/tokens/create")[1].Body; body["access"] != "full_access" {
		t.Fatalf("default access = %v", body)
	}
	dbdRunErr(t, "invalid --expires", "db", "tokens", "create", "lite", "x", "--expires", "0")

	env.dash.ok("GET", dbdTestLibSQL+"/tokens", map[string]any{"items": []map[string]any{
		{"id": "tok_1", "name": "ci", "access": "read_only", "createdAt": "2026-10-20T10:00:00Z", "expiresAt": nil, "active": true},
	}})
	env.dash.ok("DELETE", dbdTestLibSQL+"/tokens/tok_1/delete", map[string]any{"revoked": 2, "allTokensRevoked": true, "appsReconnected": 1})
	dbdRunErr(t, "--yes", "db", "tokens", "revoke", "lite", "ci")
	mustRun(t, "db", "tokens", "revoke", "lite", "ci", "--yes")
	if !strings.Contains(env.out.String(), "Revoked every token of 'lite', not only 'ci' (2 tokens;") || !strings.Contains(env.out.String(), "1 bound app received a new token") {
		t.Fatalf("revoke output:\n%s", env.out.String())
	}
}

func TestDBExtensions(t *testing.T) {
	env := setupDBDTest(t)
	env.dash.ok("GET", dbdTestPG+"/extensions", map[string]any{"items": []map[string]any{
		{"name": "pgcrypto", "installedVersion": nil, "defaultVersion": "1.3", "allowed": true, "description": "crypto"},
		{"name": "vector", "installedVersion": "0.8.0", "defaultVersion": "0.8.0", "allowed": true, "description": "vectors"},
		{"name": "plpython3u", "installedVersion": nil, "defaultVersion": "1.0", "allowed": false, "description": nil},
	}})
	env.dash.ok("POST", dbdTestPG+"/extensions/install", map[string]any{"installed": true})
	mustRun(t, "db", "extensions", "install", "pg", "pgcrypto", "--database", "shop")
	if body := env.dash.find("POST", dbdTestPG+"/extensions/install")[0].Body; !reflect.DeepEqual(body, map[string]any{"name": "pgcrypto", "database": "shop"}) {
		t.Fatalf("install body = %v", body)
	}
	if got := env.dash.find("GET", dbdTestPG+"/extensions")[0].Query.Get("database"); got != "shop" {
		t.Fatalf("list database = %q", got)
	}
	dbdRunErr(t, "allowlist", "db", "extensions", "install", "pg", "plpython3u")
	mustRun(t, "db", "extensions", "install", "pg", "vector")
	if !strings.Contains(env.out.String(), "already installed") || len(env.dash.find("POST", dbdTestPG+"/extensions/install")) != 1 {
		t.Fatalf("install of installed extension:\n%s", env.out.String())
	}

	removePath := dbdTestPG + "/extensions/vector/delete"
	env.dash.ok("DELETE", removePath, map[string]any{"dropped": true})
	mustRun(t, "db", "extensions", "remove", "pg", "vector", "--cascade", "--yes")
	if removes := env.dash.find("DELETE", removePath); len(removes) != 1 || removes[0].Query.Get("cascade") != "true" {
		t.Fatalf("remove requests = %+v", removes)
	}
}

// ---------------------------------------------------------------------------
// export / import
// ---------------------------------------------------------------------------

type dbdFakeStorage struct {
	mu      sync.Mutex
	server  *httptest.Server
	uploads map[string][]byte
	types   map[string]string
}

func newDBDFakeStorage(t *testing.T, downloads map[string]string) *dbdFakeStorage {
	t.Helper()
	storage := &dbdFakeStorage{uploads: map[string][]byte{}, types: map[string]string{}}
	storage.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Errorf("presigned request carried an Authorization header")
		}
		switch r.Method {
		case http.MethodPut:
			payload, _ := io.ReadAll(r.Body)
			storage.mu.Lock()
			storage.uploads[r.URL.Path] = payload
			storage.types[r.URL.Path] = r.Header.Get("Content-Type")
			storage.mu.Unlock()
		case http.MethodGet:
			content, ok := downloads[r.URL.Path]
			if !ok {
				http.Error(w, "<Error><Code>NoSuchKey</Code></Error>", http.StatusNotFound)
				return
			}
			_, _ = io.WriteString(w, content)
		}
	}))
	t.Cleanup(storage.server.Close)
	return storage
}

func TestDBExportDownloadsFile(t *testing.T) {
	env := setupDBDTest(t)
	dir := dbdChdir(t)
	storage := newDBDFakeStorage(t, map[string]string{"/exports/app.dump": "PGDMP-bytes"})
	env.dash.ok("POST", dbdTestPG+"/exports/create", map[string]any{"export": map[string]any{"id": "mex_1", "kind": "export", "status": "pending", "format": "native", "createdAt": "2026-10-20T10:00:00Z"}, "operationId": "op_exp"})
	dbdSucceed(env, dbdTestPG, "op_exp")
	env.dash.ok("POST", dbdTestPG+"/exports/mex_1/download", map[string]any{"url": storage.server.URL + "/exports/app.dump", "expiresAt": "2026-10-20T11:00:00Z", "fileName": "../app-20261020.dump"})

	mustRun(t, "db", "export", "pg")
	if body := env.dash.find("POST", dbdTestPG+"/exports/create")[0].Body; !reflect.DeepEqual(body, map[string]any{"databaseName": "app", "format": "native"}) {
		t.Fatalf("export body = %v", body)
	}
	content, err := os.ReadFile(filepath.Join(dir, "app-20261020.dump"))
	if err != nil || string(content) != "PGDMP-bytes" {
		t.Fatalf("downloaded file = %q (%v)", content, err)
	}
	if !strings.Contains(env.out.String(), "Exported database 'app' of 'pg' to app-20261020.dump") {
		t.Fatalf("export output:\n%s", env.out.String())
	}
	// The same file again is refused instead of overwritten.
	dbdRunErr(t, "already exists", "db", "export", "pg")
	dbdRunErr(t, "already exists", "db", "export", "pg", "--output", "app-20261020.dump")

	env.dash.ok("POST", dbdTestMySQL+"/exports/create", map[string]any{"export": map[string]any{"id": "mex_9", "kind": "export", "status": "pending", "format": "sql", "createdAt": "2026-10-20T10:00:00Z"}, "operationId": "op_my"})
	mustRun(t, "db", "export", "my", "--database", "shop", "--no-wait")
	if !strings.Contains(env.out.String(), "hubfly db exports download my mex_9") {
		t.Fatalf("--no-wait output:\n%s", env.out.String())
	}
	if body := env.dash.find("POST", dbdTestMySQL+"/exports/create")[0].Body; !reflect.DeepEqual(body, map[string]any{"databaseName": "shop", "format": "sql"}) {
		t.Fatalf("mysql export body = %v", body)
	}
	if len(env.dash.find("GET", dbdTestMySQL+"/operations/op_my")) != 0 {
		t.Fatal("--no-wait polled the operation")
	}
	dbdRunErr(t, "PostgreSQL only", "db", "export", "my", "--format", "native")
}

func TestDBExportsListAndDownload(t *testing.T) {
	env := setupDBDTest(t)
	dir := dbdChdir(t)
	storage := newDBDFakeStorage(t, map[string]string{"/x": "data"})
	env.dash.ok("GET", dbdTestPG+"/exports", map[string]any{"available": false, "items": []map[string]any{
		{"id": "mex_1", "kind": "export", "status": "failed", "databaseName": "app", "format": "sql", "fileName": nil, "sizeBytes": nil, "sha256": nil,
			"error": map[string]any{"code": "X", "message": "disk full"}, "createdAt": "2026-10-20T10:00:00Z", "finishedAt": nil, "expiresAt": nil},
	}})
	mustRun(t, "db", "exports", "pg")
	out := env.out.String()
	if !strings.Contains(out, "not configured for the region") || !strings.Contains(out, "mex_1 failed: disk full") {
		t.Fatalf("exports list:\n%s", out)
	}
	env.dash.ok("POST", dbdTestPG+"/exports/mex_2/download", map[string]any{"url": storage.server.URL + "/x", "expiresAt": "2026-10-20T11:00:00Z", "fileName": "a.sql"})
	mustRun(t, "db", "exports", "download", "pg", "mex_2", "--output", "copy.sql")
	if content, _ := os.ReadFile(filepath.Join(dir, "copy.sql")); string(content) != "data" {
		t.Fatalf("downloaded = %q", content)
	}
	env.dash.on("POST", dbdTestPG+"/exports/mex_3/download", func(rtRecorded) (int, any) {
		return 410, map[string]any{"ok": false, "error": map[string]any{"code": "MANAGED_EXPORT_EXPIRED", "message": "expired"}}
	})
	dbdRunErr(t, "kept for 7 days", "db", "exports", "download", "pg", "mex_3")
}

func TestDBImportUploadsAndStarts(t *testing.T) {
	env := setupDBDTest(t)
	storage := newDBDFakeStorage(t, nil)
	file := filepath.Join(t.TempDir(), "seed.sql.gz")
	if err := os.WriteFile(file, []byte("gzip-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	env.dash.ok("POST", dbdTestPG+"/imports/upload", map[string]any{"importId": "mim_1", "uploadUrl": storage.server.URL + "/imports/mim_1", "expiresAt": "2026-10-20T11:00:00Z", "maxBytes": 5368709120})
	env.dash.ok("POST", dbdTestPG+"/imports/mim_1/start", map[string]any{"operationId": "op_imp"})
	dbdSucceed(env, dbdTestPG, "op_imp")

	dbdRunErr(t, "--yes", "db", "import", "pg", file)
	if len(env.dash.find("POST", dbdTestPG+"/imports/upload")) != 0 {
		t.Fatal("import started without confirmation")
	}
	mustRun(t, "db", "import", "pg", file, "--database", "shop", "--yes")
	body := env.dash.find("POST", dbdTestPG+"/imports/upload")[0].Body
	if !reflect.DeepEqual(body, map[string]any{"fileName": "seed.sql.gz", "sizeBytes": float64(10), "format": "sql", "databaseName": "shop"}) {
		t.Fatalf("upload body = %v", body)
	}
	if string(storage.uploads["/imports/mim_1"]) != "gzip-bytes" || storage.types["/imports/mim_1"] != "application/octet-stream" {
		t.Fatalf("uploaded = %q (%s)", storage.uploads["/imports/mim_1"], storage.types["/imports/mim_1"])
	}
	if len(env.dash.find("POST", dbdTestPG+"/imports/mim_1/start")) != 1 || !strings.Contains(env.out.String(), "Imported seed.sql.gz into database 'shop' of 'pg'.") {
		t.Fatalf("import output:\n%s", env.out.String())
	}

	odd := filepath.Join(t.TempDir(), "backup.bin")
	_ = os.WriteFile(odd, []byte("x"), 0o600)
	dbdRunErr(t, "pass --format", "db", "import", "pg", odd, "--yes")
	dump := filepath.Join(t.TempDir(), "x.dump")
	_ = os.WriteFile(dump, []byte("x"), 0o600)
	dbdRunErr(t, "native dump", "db", "import", "my", dump, "--yes")
}

func TestDBDetectImportFormat(t *testing.T) {
	pg := dbdTarget{instance: rtInstance{Name: "pg"}, detail: dbdInstance{rtInstance: rtInstance{Engine: "postgresql"}}}
	lite := dbdTarget{instance: rtInstance{Name: "lite"}, detail: dbdInstance{rtInstance: rtInstance{Engine: "libsql"}}}
	cases := []struct {
		target dbdTarget
		file   string
		want   string
	}{
		{pg, "a.dump", "native"}, {pg, "a.BACKUP", "native"}, {pg, "a.sql", "sql"}, {pg, "a.sql.gz", "sql"},
		{lite, "a.sql.gz", "sql"}, {lite, "a.txt", "sql"},
	}
	for _, c := range cases {
		got, err := dbdDetectImportFormat(c.target, c.file)
		if err != nil || got != c.want {
			t.Fatalf("%s on %s = %q, %v; want %q", c.file, c.target.engine(), got, err, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// cert / sessions / insights
// ---------------------------------------------------------------------------

func TestDBCert(t *testing.T) {
	env := setupDBDTest(t)
	pem := "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----"
	env.dash.ok("GET", dbdTestPG+"/certificate", map[string]any{"pem": pem, "fingerprintSha256": "AA:BB", "expiresAt": nil, "fileName": "pg-ca.pem"})
	mustRun(t, "db", "cert", "pg")
	if env.out.String() != pem+"\n" {
		t.Fatalf("stdout must be the PEM only: %q", env.out.String())
	}
	if !strings.Contains(env.errOut.String(), "sslmode=verify-full") {
		t.Fatalf("verify hint:\n%s", env.errOut.String())
	}
	env.dash.ok("GET", dbdTestMySQL+"/certificate", map[string]any{"pem": pem, "fingerprintSha256": nil, "expiresAt": nil, "fileName": "my-ca.pem"})
	path := filepath.Join(t.TempDir(), "ca.pem")
	mustRun(t, "db", "cert", "my", "--output", path)
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("cert file: %v %v", info, err)
	}
	if !strings.Contains(env.errOut.String(), "--ssl-mode=VERIFY_IDENTITY") {
		t.Fatalf("mysql hint:\n%s", env.errOut.String())
	}
}

func TestDBSessions(t *testing.T) {
	env := setupDBDTest(t)
	env.dash.ok("GET", dbdTestPG+"/sessions", map[string]any{"items": []map[string]any{
		{"id": "4242", "user": "app", "database": "app", "clientAddress": "10.0.0.5", "viaProxy": true, "state": "active",
			"query": "SELECT *\n  FROM orders", "startedAt": nil, "durationMs": 65000, "waitEvent": nil, "platform": false},
		{"id": "7", "user": "hubfly_monitor", "database": "postgres", "clientAddress": nil, "viaProxy": false, "state": "idle",
			"query": nil, "startedAt": nil, "durationMs": nil, "waitEvent": nil, "platform": true},
	}})
	mustRun(t, "db", "sessions", "pg")
	out := env.out.String()
	for _, fragment := range []string{"10.0.0.5 (via HubFly proxy)", "1m 5s", "SELECT * FROM orders", "hubfly_monitor (HubFly)"} {
		if !strings.Contains(out, fragment) {
			t.Fatalf("sessions missing %q:\n%s", fragment, out)
		}
	}
	env.dash.ok("POST", dbdTestPG+"/sessions/4242/cancel", map[string]any{"ok": true})
	env.dash.ok("POST", dbdTestPG+"/sessions/4242/terminate", map[string]any{"ok": true})
	mustRun(t, "db", "sessions", "cancel", "pg", "4242")
	dbdRunErr(t, "--yes", "db", "sessions", "terminate", "pg", "4242")
	dbdRunErr(t, "belongs to HubFly", "db", "sessions", "terminate", "pg", "7", "--yes")
	mustRun(t, "db", "sessions", "terminate", "pg", "4242", "--yes")
	if len(env.dash.find("POST", dbdTestPG+"/sessions/4242/cancel")) != 1 || len(env.dash.find("POST", dbdTestPG+"/sessions/4242/terminate")) != 1 {
		t.Fatalf("signal requests:\n%s", env.dash.allRaw())
	}
}

func TestDBInsights(t *testing.T) {
	env := setupDBDTest(t)
	env.dash.ok("GET", dbdTestPG+"/insights", map[string]any{"available": true, "items": []map[string]any{
		{"queryId": "q1", "query": "SELECT * FROM t WHERE id = $1", "database": "app", "calls": 12000, "totalMs": 4200, "meanMs": 0.35, "rows": 12000},
	}})
	mustRun(t, "db", "insights", "pg", "--order-by", "calls", "--limit", "5")
	query := env.dash.find("GET", dbdTestPG+"/insights")[0].Query
	if query.Get("orderBy") != "calls" || query.Get("limit") != "5" {
		t.Fatalf("insights query = %v", query)
	}
	if !strings.Contains(env.out.String(), "12,000") || !strings.Contains(env.out.String(), "0.35 ms") {
		t.Fatalf("insights output:\n%s", env.out.String())
	}
	dbdRunErr(t, "invalid --order-by", "db", "insights", "pg", "--order-by", "rows")
	env.dash.ok("POST", dbdTestPG+"/insights/reset", map[string]any{"reset": true})
	dbdRunErr(t, "--yes", "db", "insights", "reset", "pg")
	mustRun(t, "db", "insights", "reset", "pg", "--yes")
	if len(env.dash.find("POST", dbdTestPG+"/insights/reset")) != 1 {
		t.Fatal("reset not sent")
	}
}

// ---------------------------------------------------------------------------
// config
// ---------------------------------------------------------------------------

func dbdTestConfig() map[string]any {
	return map[string]any{
		"generation": 4, "lastGoodGeneration": 4, "pendingRestart": []string{"shared_buffers"}, "rawFormat": "postgresql_conf", "raw": "", "revisionNumber": 2,
		"settings": []map[string]any{
			{"key": "work_mem", "value": "4MB", "defaultValue": "4MB", "type": "bytes", "unit": "kB", "min": nil, "max": nil, "enumValues": nil,
				"category": "Memory", "description": nil, "restartRequired": false, "locked": false, "editable": true, "modified": false},
			{"key": "shared_buffers", "value": "256MB", "defaultValue": "128MB", "type": "bytes", "unit": "8kB", "min": nil, "max": nil, "enumValues": nil,
				"category": "Memory", "description": nil, "restartRequired": true, "locked": false, "editable": true, "modified": true},
			{"key": "data_directory", "value": "/var/lib", "defaultValue": nil, "type": "string", "unit": nil, "min": nil, "max": nil, "enumValues": nil,
				"category": "Files", "description": nil, "restartRequired": true, "locked": true, "editable": false, "modified": false},
		},
	}
}

func TestDBConfigShow(t *testing.T) {
	env := setupDBDTest(t)
	env.dash.ok("GET", dbdTestPG+"/config", dbdTestConfig())
	mustRun(t, "db", "config", "pg")
	out := env.out.String()
	if !strings.Contains(out, "revision #2, generation 4") || !strings.Contains(out, "Waiting for a restart: shared_buffers") ||
		!strings.Contains(out, "modified, restart") || strings.Contains(out, "data_directory") {
		t.Fatalf("config show:\n%s", out)
	}
	env.out.buf.Reset()
	mustRun(t, "db", "config", "pg", "--all")
	if !strings.Contains(env.out.String(), "data_directory") {
		t.Fatalf("--all output:\n%s", env.out.String())
	}
}

func TestDBConfigSetValidatesConfirmsAndApplies(t *testing.T) {
	env := setupDBDTest(t)
	env.dash.ok("GET", dbdTestPG+"/config", dbdTestConfig())
	env.dash.ok("POST", dbdTestPG+"/config/validate", map[string]any{
		"valid": true, "errors": []any{}, "warnings": []map[string]any{{"key": "work_mem", "line": nil, "message": "large value"}},
		"restartRequired": []string{"shared_buffers"}, "raw": "",
		"diff": []map[string]any{
			{"key": "work_mem", "from": "4MB", "to": "64MB", "restartRequired": false},
			{"key": "shared_buffers", "from": "256MB", "to": nil, "restartRequired": true},
		},
	})
	env.dash.ok("POST", dbdTestPG+"/config/apply", map[string]any{"revisionId": "mcr_3", "revisionNumber": 3, "operationId": "op_cfg", "restartRequired": []string{"shared_buffers"}})
	dbdSucceed(env, dbdTestPG, "op_cfg")

	dbdRunErr(t, "--yes", "db", "config", "set", "pg", "work_mem=64MB", "shared_buffers=")
	if len(env.dash.find("POST", dbdTestPG+"/config/apply")) != 0 {
		t.Fatal("applied without confirmation")
	}
	mustRun(t, "db", "config", "set", "pg", "work_mem=64MB", "shared_buffers=", "--note", "tuning", "--yes")
	validate := env.dash.find("POST", dbdTestPG+"/config/validate")[0].Body
	if !reflect.DeepEqual(validate, map[string]any{"changes": map[string]any{"work_mem": "64MB", "shared_buffers": nil}}) {
		t.Fatalf("validate body = %#v", validate)
	}
	apply := env.dash.find("POST", dbdTestPG+"/config/apply")[0].Body
	want := map[string]any{"generation": float64(4), "changes": map[string]any{"work_mem": "64MB", "shared_buffers": nil}, "restart": "later", "note": "tuning"}
	if !reflect.DeepEqual(apply, want) {
		t.Fatalf("apply body = %#v", apply)
	}
	out := env.out.String()
	if !strings.Contains(out, "shared_buffers  256MB  ->  (default)  (needs a restart)") || !strings.Contains(out, "Applied revision #3") ||
		!strings.Contains(out, "Waiting for a restart: shared_buffers (run: hubfly db restart pg)") {
		t.Fatalf("config set output:\n%s", out)
	}
	if !strings.Contains(env.errOut.String(), "Warning: work_mem: large value") {
		t.Fatalf("warnings:\n%s", env.errOut.String())
	}
	dbdRunErr(t, "invalid setting", "db", "config", "set", "pg", "work_mem")
	dbdRunErr(t, "invalid --restart", "db", "config", "set", "pg", "a=b", "--restart", "soon")

	env.dash.ok("POST", dbdTestPG+"/config/validate", map[string]any{
		"valid": false, "errors": []map[string]any{{"key": "fsync", "line": nil, "message": "locked"}}, "warnings": []any{},
		"restartRequired": []any{}, "raw": "", "diff": []any{},
	})
	dbdRunErr(t, "not valid", "db", "config", "set", "pg", "fsync=off", "--yes")
	if !strings.Contains(env.errOut.String(), "Error: fsync: locked") {
		t.Fatalf("validation errors:\n%s", env.errOut.String())
	}
}

func TestDBConfigHistoryAndRevert(t *testing.T) {
	env := setupDBDTest(t)
	env.dash.ok("GET", dbdTestPG+"/config", dbdTestConfig())
	env.dash.ok("GET", dbdTestPG+"/config/history", map[string]any{"items": []map[string]any{
		{"id": "mcr_2", "number": 2, "state": "applied", "note": "first", "diff": []map[string]any{{"key": "work_mem", "from": nil, "to": "8MB", "restartRequired": false}},
			"actor": map[string]any{"id": "u1", "name": "Tester"}, "createdAt": "2026-10-20T10:00:00Z"},
	}})
	mustRun(t, "db", "config", "history", "pg", "--limit", "5")
	if got := env.dash.find("GET", dbdTestPG+"/config/history")[0].Query.Get("limit"); got != "5" {
		t.Fatalf("history limit = %q", got)
	}
	if !strings.Contains(env.out.String(), "work_mem=8MB") || !strings.Contains(env.out.String(), "Tester") {
		t.Fatalf("history output:\n%s", env.out.String())
	}
	env.dash.ok("POST", dbdTestPG+"/config/revert", map[string]any{"revisionId": "mcr_5", "revisionNumber": 5, "operationId": "op_rev", "restartRequired": []string{}})
	dbdSucceed(env, dbdTestPG, "op_rev")
	mustRun(t, "db", "config", "revert", "pg", "#2", "--restart", "now", "--yes")
	body := env.dash.find("POST", dbdTestPG+"/config/revert")[0].Body
	if !reflect.DeepEqual(body, map[string]any{"revisionId": "mcr_2", "generation": float64(4), "restart": "now"}) {
		t.Fatalf("revert body = %v", body)
	}
	dbdRunErr(t, "revision #9 not found", "db", "config", "revert", "pg", "9", "--yes")
}

func TestDBDataAPIErrorHints(t *testing.T) {
	env := setupDBDTest(t)
	env.dash.on("GET", dbdTestPG+"/databases", func(rtRecorded) (int, any) {
		return 409, map[string]any{"ok": false, "error": map[string]any{"code": "MANAGED_INSTANCE_NOT_RUNNING", "message": "The instance is not running"}}
	})
	err := run([]string{"db", "databases", "pg"})
	var apiErr *apiError
	if err == nil || !errors.As(err, &apiErr) || !strings.Contains(err.Error(), "hubfly db start pg") {
		t.Fatalf("error = %v", err)
	}
}

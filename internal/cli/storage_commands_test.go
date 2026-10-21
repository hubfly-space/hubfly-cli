package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

const stTestBase = "/api/v1/projects/st-1/storage"

// setupStorageTest is setupRealtimeTest with one Storage project ("media",
// st-1) next to a Cell project ("web", p-web).
func setupStorageTest(t *testing.T) rtTestEnv {
	t.Helper()
	env := setupRealtimeTest(t)
	t.Setenv(storageProjectEnv, "")
	env.dash.ok("GET", "/api/v1/projects", map[string]any{
		"items": []map[string]any{
			{"id": "st-1", "name": "media", "type": "storage", "regionName": "Kigali"},
			{"id": "p-web", "name": "web", "type": "cell"},
		},
		"page": 1, "pageCount": 1,
	})
	env.dash.ok("GET", stTestBase, map[string]any{
		"state": "active", "quotaBytes": 10 << 30, "bucketCount": 1, "s3Endpoint": "https://s3.kgl.hubfly.test",
		"buckets": []map[string]any{{"id": "b1", "name": "photos", "versioning": "disabled", "image_retention_seconds": 0, "created_at": "2026-10-20T08:00:00Z"}},
	})
	env.dash.ok("GET", stTestBase+"/buckets", []map[string]any{{"id": "b1", "name": "photos", "versioning": "disabled", "image_retention_seconds": 0, "created_at": "2026-10-20T08:00:00Z"}})
	env.dash.ok("GET", stTestBase+"/usage", map[string]any{
		"tenant_id": "t1", "storage_used_bytes": 1 << 30, "quota_bytes": 10 << 30, "active_jobs": 0,
		"egress_bytes": []map[string]any{{"day": "2026-10-19", "quantity": 2048}}, "encode_minutes": []map[string]any{},
	})
	return env
}

// stObjectsRoute imitates the region's flat, sorted listing: keys greater
// than the continuation token, perPage at a time.
func stObjectsRoute(keys []string, perPage int) func(rtRecorded) (int, any) {
	sorted := append([]string(nil), keys...)
	sort.Strings(sorted)
	return func(request rtRecorded) (int, any) {
		prefix, token := request.Query.Get("prefix"), request.Query.Get("continuationToken")
		objects := []map[string]any{}
		truncated := false
		for _, key := range sorted {
			if !strings.HasPrefix(key, prefix) || key <= token {
				continue
			}
			if len(objects) == perPage {
				truncated = true
				break
			}
			objects = append(objects, map[string]any{"bucket": "photos", "key": key, "size": 1536, "content_type": "image/jpeg", "created_at": "2026-10-20T08:00:00Z"})
		}
		next := ""
		if truncated {
			next = objects[len(objects)-1]["key"].(string)
		}
		return http.StatusOK, map[string]any{"objects": objects, "prefixes": []string{}, "next_token": next, "is_truncated": truncated}
	}
}

// stTransferServer stands in for the region's stream host.
type stTransferServer struct {
	server   *httptest.Server
	mu       sync.Mutex
	uploads  map[string][]byte
	types    map[string]string
	download []byte
}

func newSTTransferServer(t *testing.T) *stTransferServer {
	t.Helper()
	s := &stTransferServer{uploads: map[string][]byte{}, types: map[string]string{}}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/upload/"):
			if r.ContentLength < 0 {
				w.WriteHeader(http.StatusLengthRequired)
				return
			}
			body, _ := io.ReadAll(r.Body)
			s.mu.Lock()
			key := strings.TrimPrefix(r.URL.Path, "/upload/")
			s.uploads[key], s.types[key] = body, r.Header.Get("Content-Type")
			s.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/signed/"):
			_, _ = w.Write(s.download)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.server.Close)
	return s
}

// installUploadSessions answers upload-session requests with a URL on the
// transfer server named after the object key.
func (s *stTransferServer) installUploadSessions(dash *rtFakeDashboard) {
	dash.on("POST", stTestBase+"/objects/upload-session", func(request rtRecorded) (int, any) {
		key, _ := request.Body["objectKey"].(string)
		return http.StatusOK, map[string]any{
			"upload_url": s.server.URL + "/upload/" + key, "object_key": key,
			"content_type": request.Body["contentType"], "expires_at": "2099-01-01T00:00:00Z",
		}
	})
}

func runErr(args ...string) error { return run(args) }

func TestStorageHelpPrintsUsage(t *testing.T) {
	env := setupStorageTest(t)
	mustRun(t, "storage", "help")
	if strings.TrimSpace(env.out.String()) != storageUsageText {
		t.Fatalf("help output differs from storageUsageText:\n%s", env.out.String())
	}
}

func TestStorageProjectResolution(t *testing.T) {
	t.Run("single project is used", func(t *testing.T) {
		env := setupStorageTest(t)
		mustRun(t, "storage", "info")
		out := env.out.String()
		for _, want := range []string{"media", "https://s3.kgl.hubfly.test", "1.0 GB of 10.0 GB", "photos"} {
			if !strings.Contains(out, want) {
				t.Fatalf("info output missing %q:\n%s", want, out)
			}
		}
	})
	t.Run("ambiguous without a reference", func(t *testing.T) {
		env := setupStorageTest(t)
		env.dash.ok("GET", "/api/v1/projects", map[string]any{"items": []map[string]any{
			{"id": "st-1", "name": "media", "type": "storage"}, {"id": "st-2", "name": "backups", "type": "storage"},
		}, "page": 1, "pageCount": 1})
		err := runErr("storage", "buckets")
		if err == nil || !strings.Contains(err.Error(), "media (st-1)") || !strings.Contains(err.Error(), "backups (st-2)") || !strings.Contains(err.Error(), storageProjectEnv) {
			t.Fatalf("error = %v", err)
		}
		t.Setenv(storageProjectEnv, "MEDIA")
		mustRun(t, "storage", "buckets")
		if !strings.Contains(env.out.String(), "photos") {
			t.Fatalf("buckets output = %q", env.out.String())
		}
	})
	t.Run("env var names a missing project", func(t *testing.T) {
		setupStorageTest(t)
		t.Setenv(storageProjectEnv, "nope")
		err := runErr("storage", "buckets")
		if err == nil || !strings.Contains(err.Error(), "from "+storageProjectEnv) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("cell project is rejected", func(t *testing.T) {
		setupStorageTest(t)
		err := runErr("storage", "buckets", "--project", "web")
		if err == nil || !strings.Contains(err.Error(), "is a cell project") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("no storage projects", func(t *testing.T) {
		env := setupStorageTest(t)
		env.dash.ok("GET", "/api/v1/projects", map[string]any{"items": []map[string]any{{"id": "p-web", "name": "web", "type": "cell"}}, "page": 1, "pageCount": 1})
		err := runErr("storage", "ls")
		if err == nil || !strings.Contains(err.Error(), "hubfly storage create") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestStorageListToleratesFailures(t *testing.T) {
	env := setupStorageTest(t)
	env.dash.ok("GET", "/api/v1/projects", map[string]any{"items": []map[string]any{
		{"id": "st-1", "name": "media", "type": "storage"}, {"id": "st-2", "name": "broken", "type": "storage"}, {"id": "p-web", "name": "web", "type": "cell"},
	}, "page": 1, "pageCount": 1})
	mustRun(t, "storage", "list", "--json")
	var rows []storageProjectSummary
	if err := json.Unmarshal([]byte(env.out.String()), &rows); err != nil {
		t.Fatalf("stdout is not pure JSON: %v\n%s", err, env.out.String())
	}
	if len(rows) != 2 || rows[0].State != "active" || rows[0].UsedBytes == nil || rows[1].State != "unavailable" || rows[1].Error == "" {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestStorageCreateFiltersRegionsAndWaits(t *testing.T) {
	env := setupStorageTest(t)
	env.dash.ok("GET", "/api/v1/regions", []map[string]any{
		{"id": "r-cell", "name": "Lagos", "location": "NG", "products": map[string]any{"cell": true, "storage": false}},
		{"id": "r-st", "name": "Kigali", "location": "RW", "products": map[string]any{"cell": true, "storage": true}},
	})
	env.dash.ok("POST", "/api/v1/projects/create", map[string]any{"id": "st-9", "name": "assets", "type": "storage"})
	polls := 0
	env.dash.on("GET", "/api/v1/projects/st-9/storage", func(rtRecorded) (int, any) {
		polls++
		state := "provisioning"
		if polls > 1 {
			state = "active"
		}
		return http.StatusOK, map[string]any{"state": state, "quotaBytes": 10 << 30, "s3Endpoint": "https://s3.kgl.hubfly.test", "buckets": []any{}}
	})

	if err := runErr("storage", "create", "assets", "--region", "Lagos", "--yes"); err == nil || !strings.Contains(err.Error(), "does not offer Object Storage") {
		t.Fatalf("non-storage region error = %v", err)
	}
	if err := runErr("storage", "create", "assets"); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("confirmation error = %v", err)
	}
	if len(env.dash.find("POST", "/api/v1/projects/create")) != 0 {
		t.Fatal("project created without confirmation")
	}
	env.out.buf.Reset()
	mustRun(t, "storage", "create", "assets", "--yes", "--json")
	created := env.dash.find("POST", "/api/v1/projects/create")
	if len(created) != 1 || created[0].Body["type"] != "storage" || created[0].Body["regionId"] != "r-st" || created[0].Body["name"] != "assets" {
		t.Fatalf("create requests = %+v", created)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(env.out.String()), &out); err != nil {
		t.Fatalf("stdout is not pure JSON: %v\n%s", err, env.out.String())
	}
	if polls < 2 || out["storage"].(map[string]any)["state"] != "active" {
		t.Fatalf("polls = %d, out = %v", polls, out)
	}
}

func TestStorageLsGroupsPrefixesAndPaginates(t *testing.T) {
	env := setupStorageTest(t)
	keys := []string{"a.txt", "photos/1.jpg", "photos/2.jpg", "photos/x/3.jpg", "photos/x/4.jpg", "z.txt"}
	env.dash.on("GET", stTestBase+"/buckets/objects", stObjectsRoute(keys, 2))

	mustRun(t, "storage", "ls", "photos")
	out := env.out.String()
	if !strings.Contains(out, "PRE") || !strings.Contains(out, "photos/\n") || !strings.Contains(out, "a.txt") || !strings.Contains(out, "z.txt") || strings.Contains(out, "1.jpg") {
		t.Fatalf("ls output:\n%s", out)
	}
	for _, request := range env.dash.find("GET", stTestBase+"/buckets/objects") {
		if request.Query.Get("name") != "photos" || request.Query.Get("pageSize") != "200" {
			t.Fatalf("query = %v", request.Query)
		}
	}

	env.out.buf.Reset()
	mustRun(t, "storage", "ls", "photos/photos/")
	out = env.out.String()
	if !strings.Contains(out, "x/\n") || !strings.Contains(out, " 1.jpg") || strings.Contains(out, "3.jpg") || strings.Contains(out, "a.txt") {
		t.Fatalf("prefix ls output:\n%s", out)
	}

	env.out.buf.Reset()
	mustRun(t, "storage", "ls", "photos", "--recursive", "--json")
	var listing storageListing
	if err := json.Unmarshal([]byte(env.out.String()), &listing); err != nil {
		t.Fatalf("stdout is not pure JSON: %v", err)
	}
	if len(listing.Objects) != len(keys) || len(listing.Prefixes) != 0 || listing.Truncated {
		t.Fatalf("recursive listing = %+v", listing)
	}

	env.out.buf.Reset()
	mustRun(t, "storage", "ls", "photos", "-r", "--limit", "3", "--json")
	listing = storageListing{}
	_ = json.Unmarshal([]byte(env.out.String()), &listing)
	if len(listing.Objects) != 3 || !listing.Truncated {
		t.Fatalf("limited listing = %+v", listing)
	}
}

func TestStorageUploadKeyAndContentType(t *testing.T) {
	cases := map[[2]string]string{
		{"dir/cat.png", ""}:          "cat.png",
		{"dir/cat.png", "img/"}:      "img/cat.png",
		{"dir/cat.png", "img/a.png"}: "img/a.png",
	}
	for input, want := range cases {
		if got := storageUploadKey(input[0], input[1]); got != want {
			t.Fatalf("storageUploadKey(%q, %q) = %q, want %q", input[0], input[1], got, want)
		}
	}
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	png := write("image", []byte("\x89PNG\r\n\x1a\n0000000000"))
	jsonFile := write("data.json", []byte("{}"))
	binary := write("blob", []byte{0, 1, 2, 3})
	for _, check := range []struct{ path, override, want string }{
		{png, "", "image/png"},
		{jsonFile, "", "application/json"},
		{binary, "", "application/octet-stream"},
		{binary, "video/mp4", "video/mp4"},
	} {
		got, err := storageContentType(check.path, check.override)
		if err != nil || got != check.want {
			t.Fatalf("storageContentType(%s, %q) = %q, %v; want %q", filepath.Base(check.path), check.override, got, err, check.want)
		}
	}
	if _, err := storageContentType(binary, "not a type/"); err == nil {
		t.Fatal("invalid --content-type accepted")
	}
}

func TestStoragePutSingleAndRecursive(t *testing.T) {
	env := setupStorageTest(t)
	transfer := newSTTransferServer(t)
	transfer.installUploadSessions(env.dash)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "site", "css"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "site", "index.html"), []byte("<html></html>"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "site", "css", "app.css"), []byte("body{}"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "site", "empty.txt"), nil, 0o600)
	if err := os.Symlink(filepath.Join(dir, "site", "index.html"), filepath.Join(dir, "site", "link.html")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	mustRun(t, "storage", "put", filepath.Join(dir, "site", "index.html"), "photos/pages/")
	if got := string(transfer.uploads["pages/index.html"]); got != "<html></html>" {
		t.Fatalf("uploaded body = %q (uploads %v)", got, transfer.uploads)
	}
	if transfer.types["pages/index.html"] != "text/html; charset=utf-8" {
		t.Fatalf("content type = %q", transfer.types["pages/index.html"])
	}
	sessions := env.dash.find("POST", stTestBase+"/objects/upload-session")
	if len(sessions) != 1 || sessions[0].Body["bucket"] != "photos" || sessions[0].Body["contentType"] != "text/html; charset=utf-8" {
		t.Fatalf("sessions = %+v", sessions)
	}

	if err := runErr("storage", "put", filepath.Join(dir, "site"), "photos/www"); err == nil || !strings.Contains(err.Error(), "--recursive") {
		t.Fatalf("directory without --recursive error = %v", err)
	}
	mustRun(t, "storage", "put", filepath.Join(dir, "site"), "photos/www", "--recursive")
	for _, key := range []string{"www/index.html", "www/css/app.css", "www/empty.txt"} {
		if _, ok := transfer.uploads[key]; !ok {
			t.Fatalf("missing upload %s (uploads %v)", key, transfer.uploads)
		}
	}
	if _, ok := transfer.uploads["www/link.html"]; ok {
		t.Fatal("symlink was uploaded")
	}
	if !strings.Contains(env.errOut.String(), "link.html (not a regular file)") || !strings.Contains(env.out.String(), "Uploaded 3 files") {
		t.Fatalf("stdout:\n%s\nstderr:\n%s", env.out.String(), env.errOut.String())
	}
}

func TestStorageGetToFileAndStdout(t *testing.T) {
	env := setupStorageTest(t)
	transfer := newSTTransferServer(t)
	transfer.download = []byte("hello object")
	env.dash.ok("GET", stTestBase+"/objects/download", map[string]any{"download_url": transfer.server.URL + "/signed/tok/download", "expires_at": "2099-01-01T00:00:00Z"})
	dir := t.TempDir()
	dest := filepath.Join(dir, "out.txt")

	mustRun(t, "storage", "get", "photos/docs/readme.txt", dest)
	if data, _ := os.ReadFile(dest); string(data) != "hello object" {
		t.Fatalf("file content = %q", data)
	}
	request := env.dash.find("GET", stTestBase+"/objects/download")[0]
	if request.Query.Get("bucket") != "photos" || request.Query.Get("key") != "docs/readme.txt" {
		t.Fatalf("download query = %v", request.Query)
	}
	if err := runErr("storage", "get", "photos/docs/readme.txt", dest); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("overwrite error = %v", err)
	}
	transfer.download = []byte("v2")
	mustRun(t, "storage", "get", "photos/docs/readme.txt", dest, "--force")
	if data, _ := os.ReadFile(dest); string(data) != "v2" {
		t.Fatalf("forced content = %q", data)
	}
	// A directory destination gets the key's base name.
	mustRun(t, "storage", "get", "photos/docs/readme.txt", dir)
	if _, err := os.Stat(filepath.Join(dir, "readme.txt")); err != nil {
		t.Fatalf("download into directory: %v", err)
	}

	env.out.buf.Reset()
	mustRun(t, "storage", "get", "photos/docs/readme.txt", "-")
	if env.out.String() != "v2" {
		t.Fatalf("stdout = %q", env.out.String())
	}
}

func TestStorageRmConfirmsAndRecurses(t *testing.T) {
	env := setupStorageTest(t)
	env.dash.on("GET", stTestBase+"/buckets/objects", stObjectsRoute([]string{"old/a", "old/b", "old/c/d", "keep"}, 2))
	env.dash.ok("DELETE", stTestBase+"/objects/delete", map[string]any{"version_id": "v", "delete_marker": false})

	if err := runErr("storage", "rm", "photos/old/a"); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("confirmation error = %v", err)
	}
	if err := runErr("storage", "rm", "photos/old/", "--yes"); err == nil || !strings.Contains(err.Error(), "--recursive") {
		t.Fatalf("prefix without --recursive error = %v", err)
	}
	if n := len(env.dash.find("DELETE", stTestBase+"/objects/delete")); n != 0 {
		t.Fatalf("%d deletes without confirmation", n)
	}
	mustRun(t, "storage", "rm", "photos/old/", "--recursive", "--yes")
	deleted := []string{}
	for _, request := range env.dash.find("DELETE", stTestBase+"/objects/delete") {
		deleted = append(deleted, request.Query.Get("key"))
	}
	sort.Strings(deleted)
	if strings.Join(deleted, ",") != "old/a,old/b,old/c/d" {
		t.Fatalf("deleted = %v", deleted)
	}
	if !strings.Contains(env.out.String(), "Deleted 3 objects.") {
		t.Fatalf("stdout = %s", env.out.String())
	}
}

func TestStorageBucketDeleteRefusesNonEmpty(t *testing.T) {
	env := setupStorageTest(t)
	env.dash.on("GET", stTestBase+"/buckets/objects", stObjectsRoute([]string{"a"}, 1))
	err := runErr("storage", "buckets", "delete", "photos", "--yes")
	if err == nil || !strings.Contains(err.Error(), "not empty") || !strings.Contains(err.Error(), "rm photos/ --recursive") {
		t.Fatalf("error = %v", err)
	}
	if len(env.dash.find("DELETE", stTestBase+"/buckets/delete")) != 0 {
		t.Fatal("non-empty bucket delete was sent")
	}
}

func TestStorageBucketsUpdateVersioningRules(t *testing.T) {
	env := setupStorageTest(t)
	env.dash.on("PUT", stTestBase+"/buckets/update", func(request rtRecorded) (int, any) {
		return http.StatusOK, map[string]any{"name": "photos", "versioning": request.Body["versioning"], "image_retention_seconds": request.Body["imageRetentionSeconds"]}
	})
	if err := runErr("storage", "buckets", "update", "photos", "--image-retention", "1d"); err == nil || !strings.Contains(err.Error(), "--versioning suspended") {
		t.Fatalf("retention on a disabled bucket error = %v", err)
	}
	if err := runErr("storage", "buckets", "update", "photos", "--image-retention", "30m", "--versioning", "suspended"); err == nil || !strings.Contains(err.Error(), "1h to 365d") {
		t.Fatalf("short retention error = %v", err)
	}
	mustRun(t, "storage", "buckets", "update", "photos", "--versioning", "suspended", "--image-retention", "1d")
	updates := env.dash.find("PUT", stTestBase+"/buckets/update")
	if len(updates) != 1 || updates[0].Body["versioning"] != "suspended" || updates[0].Body["imageRetentionSeconds"] != float64(86400) {
		t.Fatalf("updates = %+v", updates)
	}
}

func stInstallKeyCreate(env rtTestEnv) {
	env.dash.on("POST", stTestBase+"/access-keys/create", func(request rtRecorded) (int, any) {
		return http.StatusOK, map[string]any{
			"access_key": "HFAKEXAMPLE", "secret_key": "s3cr3t-value", "tenant_id": "t1", "name": request.Body["name"],
			"permission": request.Body["permission"], "bucket_scope": request.Body["bucketScope"],
		}
	})
}

func TestStorageKeysCreateFormats(t *testing.T) {
	env := setupStorageTest(t)
	stInstallKeyCreate(env)

	mustRun(t, "storage", "keys", "create", "app", "--read-only", "--bucket", "photos", "--format", "dotenv")
	request := env.dash.find("POST", stTestBase+"/access-keys/create")[0]
	if request.Body["permission"] != "read" || request.Body["bucketScope"] != "photos" {
		t.Fatalf("create body = %v", request.Body)
	}
	out := env.out.String()
	for _, want := range []string{
		"HUBFLY_STORAGE_ENDPOINT=https://s3.kgl.hubfly.test", "HUBFLY_STORAGE_REGION=us-east-1", "HUBFLY_STORAGE_ACCESS_KEY_ID=HFAKEXAMPLE",
		"HUBFLY_STORAGE_SECRET_ACCESS_KEY=s3cr3t-value", "HUBFLY_STORAGE_BUCKET=photos", "AWS_ENDPOINT_URL_S3=https://s3.kgl.hubfly.test",
		"AWS_REGION=us-east-1", "AWS_ACCESS_KEY_ID=HFAKEXAMPLE", "AWS_SECRET_ACCESS_KEY=s3cr3t-value",
	} {
		if !strings.Contains(out, want+"\n") {
			t.Fatalf("dotenv output missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(env.errOut.String(), "shown only once") || strings.Contains(env.errOut.String(), "s3cr3t") {
		t.Fatalf("stderr = %q", env.errOut.String())
	}

	env.out.buf.Reset()
	mustRun(t, "storage", "keys", "create", "app", "--format", "json")
	var vars map[string]string
	if err := json.Unmarshal([]byte(env.out.String()), &vars); err != nil {
		t.Fatalf("stdout is not pure JSON: %v\n%s", err, env.out.String())
	}
	if vars["AWS_SECRET_ACCESS_KEY"] != "s3cr3t-value" || vars["HUBFLY_STORAGE_BUCKET"] != "" || len(vars) != 8 {
		t.Fatalf("json vars = %v", vars)
	}
	if body := env.dash.find("POST", stTestBase+"/access-keys/create")[1].Body; body["permission"] != "readwrite" || body["bucketScope"] != nil {
		t.Fatalf("default create body = %v", body)
	}

	env.out.buf.Reset()
	mustRun(t, "storage", "keys", "create", "app")
	if !strings.Contains(env.out.String(), "Secret access key:") || !strings.Contains(env.out.String(), "s3cr3t-value") {
		t.Fatalf("text output:\n%s", env.out.String())
	}
}

func TestStorageKeysCreateToWritesProjectEnv(t *testing.T) {
	env := setupStorageTest(t)
	stInstallKeyCreate(env)
	env.dash.ok("GET", "/api/v1/projects/p-web/env", []map[string]any{
		{"id": "e1", "key": "PORT", "value": "3000", "isSecret": false},
		{"id": "e2", "key": "AWS_REGION", "value": "eu-west-1", "isSecret": false},
	})
	env.dash.ok("POST", "/api/v1/projects/p-web/env/update", map[string]any{"success": true, "count": 10, "redeployed": 2})

	if err := runErr("storage", "keys", "create", "app", "--to", "media"); err == nil || !strings.Contains(err.Error(), "Cell project") {
		t.Fatalf("--to a storage project error = %v", err)
	}
	if len(env.dash.find("POST", stTestBase+"/access-keys/create")) != 0 {
		t.Fatal("key created although --to was invalid")
	}

	debugEnabled = true
	stderr := captureStderr(t, func() { mustRun(t, "storage", "keys", "create", "app", "--to", "web") })
	debugEnabled = false
	if !strings.Contains(stderr, "[debug]") || strings.Contains(stderr, "s3cr3t") {
		t.Fatalf("debug output leaks the secret or is missing:\n%s", stderr)
	}
	if strings.Contains(env.out.String(), "s3cr3t") || strings.Contains(env.errOut.String(), "s3cr3t") {
		t.Fatalf("secret printed with --to:\n%s\n%s", env.out.String(), env.errOut.String())
	}
	if !strings.Contains(env.out.String(), "AWS_SECRET_ACCESS_KEY") || !strings.Contains(env.out.String(), "Replaced existing values of: AWS_REGION") {
		t.Fatalf("stdout:\n%s", env.out.String())
	}
	update := env.dash.find("POST", "/api/v1/projects/p-web/env/update")[0]
	items := map[string]map[string]any{}
	for _, raw := range update.Body["envVars"].([]any) {
		item := raw.(map[string]any)
		items[item["key"].(string)] = item
	}
	if items["PORT"]["value"] != "3000" || items["AWS_REGION"]["value"] != "us-east-1" || len(items) != 9 {
		t.Fatalf("env update items = %v", items)
	}
	for _, name := range []string{"HUBFLY_STORAGE_SECRET_ACCESS_KEY", "AWS_SECRET_ACCESS_KEY"} {
		if items[name]["isSecret"] != true || items[name]["value"] != "s3cr3t-value" {
			t.Fatalf("%s = %v", name, items[name])
		}
	}
	if items["AWS_ACCESS_KEY_ID"]["isSecret"] != false {
		t.Fatalf("AWS_ACCESS_KEY_ID = %v", items["AWS_ACCESS_KEY_ID"])
	}
}

func TestStorageKeysCreateToRevokesOnEnvFailure(t *testing.T) {
	env := setupStorageTest(t)
	stInstallKeyCreate(env)
	env.dash.ok("GET", "/api/v1/projects/p-web/env", []map[string]any{})
	env.dash.on("POST", "/api/v1/projects/p-web/env/update", func(rtRecorded) (int, any) {
		return http.StatusBadRequest, map[string]any{"ok": false, "error": map[string]any{"code": "BAD", "message": "nope"}}
	})
	env.dash.ok("DELETE", stTestBase+"/access-keys/revoke", nil)
	err := runErr("storage", "keys", "create", "app", "--to", "web")
	if err == nil || !strings.Contains(err.Error(), "was revoked") {
		t.Fatalf("error = %v", err)
	}
	revokes := env.dash.find("DELETE", stTestBase+"/access-keys/revoke")
	if len(revokes) != 1 || revokes[0].Query.Get("accessKey") != "HFAKEXAMPLE" {
		t.Fatalf("revokes = %+v", revokes)
	}
}

func TestStoragePublishPublicAndUnpublish(t *testing.T) {
	env := setupStorageTest(t)
	asset := map[string]any{"id": "as_1", "url_id": "u1", "bucket": "photos", "object_key": "cat.jpg", "file_type": "image", "access_mode": "protected",
		"asset_url": "https://media.kgl.hubfly.test/photos/u1", "player_url": "https://media.kgl.hubfly.test/photos/u1/player"}
	assets := []map[string]any{}
	env.dash.on("GET", stTestBase+"/assets", func(rtRecorded) (int, any) { return http.StatusOK, assets })
	env.dash.ok("POST", stTestBase+"/assets/create", map[string]any{"asset": asset, "asset_url": asset["asset_url"], "player_url": asset["player_url"], "access_key": "asset-key-123"})
	env.dash.on("PUT", stTestBase+"/assets/update", func(request rtRecorded) (int, any) {
		updated := map[string]any{}
		for key, value := range asset {
			updated[key] = value
		}
		updated["access_mode"] = request.Body["accessMode"]
		return http.StatusOK, updated
	})
	env.dash.ok("DELETE", stTestBase+"/assets/delete", nil)

	mustRun(t, "storage", "publish", "photos/cat.jpg", "--title", "Cat")
	if body := env.dash.find("POST", stTestBase+"/assets/create")[0].Body; body["bucket"] != "photos" || body["objectKey"] != "cat.jpg" || body["title"] != "Cat" {
		t.Fatalf("create body = %v", body)
	}
	if body := env.dash.find("PUT", stTestBase+"/assets/update")[0].Body; body["assetId"] != "as_1" || body["accessMode"] != "public" {
		t.Fatalf("update body = %v", body)
	}
	out := env.out.String()
	if !strings.Contains(out, "public asset") || !strings.Contains(out, "https://media.kgl.hubfly.test/photos/u1/media") || strings.Contains(out, "asset-key-123") {
		t.Fatalf("publish output:\n%s", out)
	}

	assets = []map[string]any{asset}
	if err := runErr("storage", "publish", "photos/cat.jpg"); err == nil || !strings.Contains(err.Error(), "already published") {
		t.Fatalf("republish error = %v", err)
	}
	if err := runErr("storage", "unpublish", "photos/cat.jpg"); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("unpublish confirmation error = %v", err)
	}
	mustRun(t, "storage", "unpublish", "photos/cat.jpg", "--yes")
	if deletes := env.dash.find("DELETE", stTestBase+"/assets/delete"); len(deletes) != 1 || deletes[0].Query.Get("assetId") != "as_1" {
		t.Fatalf("deletes = %+v", deletes)
	}
}

func TestStorageEncodeWaitsForJob(t *testing.T) {
	env := setupStorageTest(t)
	env.dash.ok("POST", stTestBase+"/media/encode", map[string]any{"id": "job_1", "state": "queued"})
	polls := 0
	env.dash.on("GET", stTestBase+"/media", func(rtRecorded) (int, any) {
		polls++
		state, progress := "encoding", 40
		if polls > 1 {
			state, progress = "ready", 100
		}
		return http.StatusOK, []map[string]any{{"id": "job_1", "bucket": "photos", "source_key": "clip.mp4", "max_height": 480, "state": state, "progress": progress}}
	})
	env.dash.ok("POST", stTestBase+"/media/playback", map[string]any{"player_url": "https://media.kgl.hubfly.test/player/tok", "manifest_url": "https://media.kgl.hubfly.test/signed/tok/master.m3u8", "expires_at": "2099-01-01T00:00:00Z"})

	if err := runErr("storage", "encode", "photos/clip.mp4", "--max-height", "900"); err == nil {
		t.Fatal("invalid --max-height accepted")
	}
	mustRun(t, "storage", "encode", "photos/clip.mp4", "--max-height", "480p", "--wait", "--json")
	if body := env.dash.find("POST", stTestBase+"/media/encode")[0].Body; body["sourceKey"] != "clip.mp4" || body["maxHeight"] != float64(480) {
		t.Fatalf("encode body = %v", body)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(env.out.String()), &out); err != nil {
		t.Fatalf("stdout is not pure JSON: %v\n%s", err, env.out.String())
	}
	if out["playback"].(map[string]any)["player_url"] != "https://media.kgl.hubfly.test/player/tok" {
		t.Fatalf("out = %v", out)
	}
}

func TestStorageRedactURL(t *testing.T) {
	for input, want := range map[string]string{
		"https://media.test/upload/abc.def":           "https://media.test/upload/<token>",
		"https://media.test/signed/abc.def/download":  "https://media.test/signed/<token>",
		"https://other.test/x?X-Amz-Signature=secret": "https://other.test/x?<redacted>",
	} {
		if got := storageRedactURL(input); got != want {
			t.Fatalf("storageRedactURL(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestStorageDebugHidesSignedResponseURLs(t *testing.T) {
	env := setupStorageTest(t)
	env.dash.ok("POST", stTestBase+"/objects/upload-session", map[string]any{"upload_url": "https://media.test/upload/upload-secret"})
	env.dash.ok("GET", stTestBase+"/objects/download", map[string]any{"download_url": "https://media.test/signed/download-secret/download"})
	env.dash.ok("POST", stTestBase+"/assets/share-token", map[string]any{"token": "share-secret", "player_url": "https://media.test/player/share-secret"})
	env.dash.ok("POST", stTestBase+"/media/playback", map[string]any{"player_url": "https://media.test/player/playback-secret"})
	previous := debugEnabled
	debugEnabled = true
	defer func() { debugEnabled = previous }()
	stderr := captureStderr(t, func() {
		upload, err := createStorageUploadSession("hf_test_token", "st-1", "photos", "cat.jpg", "image/jpeg")
		if err != nil || !strings.Contains(upload.UploadURL, "upload-secret") {
			t.Fatalf("upload response lost: %v", err)
		}
		download, err := createStorageDownload("hf_test_token", "st-1", "photos", "cat.jpg")
		if err != nil || !strings.Contains(download.DownloadURL, "download-secret") {
			t.Fatalf("download response lost: %v", err)
		}
		share, err := createStorageShare("hf_test_token", "st-1", "a1", 3600)
		if err != nil || share.Token != "share-secret" {
			t.Fatalf("share response lost: %v", err)
		}
		playback, err := createStorageMediaPlayback("hf_test_token", "st-1", "j1")
		if err != nil || !strings.Contains(playback.PlayerURL, "playback-secret") {
			t.Fatalf("playback response lost: %v", err)
		}
	})
	if !strings.Contains(stderr, "[debug]") {
		t.Fatal("expected request diagnostics")
	}
	for _, secret := range []string{"upload-secret", "download-secret", "share-secret", "playback-secret"} {
		if strings.Contains(stderr, secret) {
			t.Fatalf("debug log leaked %q", secret)
		}
	}
	if !debugEnabled {
		t.Fatal("debug state was not restored")
	}
}

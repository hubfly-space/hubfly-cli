package cli

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	waTestWeb      = "/api/v1/projects/p-web/analytics"
	waTestStandard = "/api/v1/projects/p-an/analytics"
)

func waTestSite(id, publicID, projectID, name, domain, source string) map[string]any {
	return map[string]any{
		"id": id, "publicId": publicID, "projectId": projectID, "regionId": "r-kgl", "name": name,
		"primaryDomain": domain, "domains": []string{domain}, "includeSubdomains": false,
		"timezone": "Africa/Kigali", "currency": "USD", "retentionDays": 30, "identityMode": "cookieless",
		"source": source, "route": nil, "injectEnabled": source == "hubfly_route", "injectExcludePaths": []string{},
		"proxy": map[string]any{"status": "none", "error": nil, "syncedAt": nil}, "state": "active",
		"sync": map[string]any{"status": "synced", "error": nil, "syncedAt": "2026-10-29T08:00:00Z"},
		"install": map[string]any{
			"scriptUrl":    "https://hfa.kgl.hubfly.test/s/" + publicID + ".js",
			"snippet":      `<script defer src="https://hfa.kgl.hubfly.test/s/` + publicID + `.js"></script>`,
			"eventsApiUrl": "https://hfa.kgl.hubfly.test/api/v1/events", "autoInjected": source == "hubfly_route",
		},
		"lastEventAt": nil, "dbBytes": 2048, "historyBytesOver30d": 0, "movedAt": nil, "previousRegionId": nil,
		"createdAt": "2026-10-29T08:00:00Z", "updatedAt": "2026-10-29T08:00:00Z",
	}
}

func waTestOverview(projectID, projectType string, sites ...map[string]any) map[string]any {
	if sites == nil {
		sites = []map[string]any{}
	}
	return map[string]any{
		"projectId": projectID, "projectType": projectType, "regionId": "r-kgl",
		"availability": map[string]any{"open": true, "reason": nil}, "monthlyEventCap": nil,
		"sites": sites, "canEdit": true, "canManageRoutes": true,
	}
}

// setupAnalyticsTest is setupRealtimeTest with a Cell project ("web", p-web)
// that has one HubFly site, a standalone Analytics project ("stats", p-an)
// with one external site, and a Storage project that must be ignored.
func setupAnalyticsTest(t *testing.T) rtTestEnv {
	t.Helper()
	env := setupRealtimeTest(t)
	env.dash.ok("GET", "/api/v1/projects", map[string]any{
		"items": []map[string]any{
			{"id": "p-web", "name": "web", "type": "cell"},
			{"id": "p-an", "name": "stats", "type": "analytics"},
			{"id": "st-1", "name": "media", "type": "storage"},
		},
		"page": 1, "pageCount": 1,
	})
	env.dash.ok("GET", waTestWeb, waTestOverview("p-web", "cell", waTestSite("as_app", "hfa_app0000000000000", "p-web", "App", "app.example.com", "hubfly_route")))
	env.dash.ok("GET", waTestStandard, waTestOverview("p-an", "analytics", waTestSite("as_docs", "hfa_docs000000000000", "p-an", "Docs", "docs.example.com", "external")))
	return env
}

func TestAnalyticsHelpAndDispatch(t *testing.T) {
	env := setupAnalyticsTest(t)
	mustRun(t, "analytics", "help")
	if !strings.Contains(env.out.String(), "hubfly analytics sites create <domain>") {
		t.Fatalf("help = %s", env.out.String())
	}
	if err := runErr("analytics", "nope"); err == nil || !strings.Contains(err.Error(), "unknown analytics command") {
		t.Fatalf("unknown command error = %v", err)
	}
}

func TestAnalyticsSitesListSkipsOtherProjectTypes(t *testing.T) {
	env := setupAnalyticsTest(t)
	mustRun(t, "analytics", "sites", "--json")
	var sites []analyticsSite
	if err := json.Unmarshal([]byte(env.out.String()), &sites); err != nil {
		t.Fatalf("stdout is not pure JSON: %v\n%s", err, env.out.String())
	}
	if len(sites) != 2 || sites[0].ProjectName != "stats" || sites[1].ProjectName != "web" {
		t.Fatalf("sites = %+v", sites)
	}
	if len(env.dash.find("GET", "/api/v1/projects/st-1/analytics")) != 0 {
		t.Fatal("asked a Storage project for sites")
	}

	// One unreadable project is skipped unless it was named.
	env.dash.on("GET", waTestWeb, func(rtRecorded) (int, any) {
		return http.StatusForbidden, map[string]any{"ok": false, "error": map[string]any{"code": "FORBIDDEN", "message": "no access"}}
	})
	env.out.buf.Reset()
	mustRun(t, "analytics", "sites", "list")
	if out := env.out.String(); !strings.Contains(out, "docs.example.com") || strings.Contains(out, "app.example.com") {
		t.Fatalf("list = %s", out)
	}
	if err := runErr("analytics", "sites", "--project", "web"); err == nil || exitCodeForError(err) != 3 {
		t.Fatalf("named project error = %v", err)
	}
	if err := runErr("analytics", "sites", "--project", "media"); err == nil || !strings.Contains(err.Error(), "storage project") {
		t.Fatalf("storage project error = %v", err)
	}
}

func TestAnalyticsResolveSite(t *testing.T) {
	env := setupAnalyticsTest(t)
	for _, ref := range []string{"as_docs", "hfa_docs000000000000", "docs", "DOCS.example.com", "https://docs.example.com/"} {
		site, err := resolveAnalyticsSite("hf_test_token", ref, "")
		if ref == "https://docs.example.com/" {
			// Site references are names, ids or bare domains.
			if err == nil {
				t.Fatalf("resolve(%q) = %s, want not found", ref, site.ID)
			}
			continue
		}
		if err != nil || site.ID != "as_docs" {
			t.Fatalf("resolve(%q) = %+v, %v", ref, site.ID, err)
		}
	}
	if _, err := resolveAnalyticsSite("hf_test_token", "", ""); err == nil || !strings.Contains(err.Error(), "you have 2 sites") {
		t.Fatalf("no ref error = %v", err)
	}
	if _, err := resolveAnalyticsSite("hf_test_token", "missing", ""); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing error = %v", err)
	}
	site, err := resolveAnalyticsSite("hf_test_token", "", "web")
	if err != nil || site.ID != "as_app" {
		t.Fatalf("only site of web = %+v, %v", site.ID, err)
	}
	_ = env
}

func TestAnalyticsSitesCreateExternal(t *testing.T) {
	env := setupAnalyticsTest(t)
	created := waTestSite("as_blog", "hfa_blog000000000000", "p-an", "Blog", "blog.example.com", "external")
	env.dash.ok("POST", waTestStandard+"/sites/create", created)

	// The only standalone Analytics project is the default.
	mustRun(t, "analytics", "sites", "create", "https://Blog.Example.com/posts", "--name", "Blog", "--domain", "www.blog.example.com", "--retention", "90")
	requests := env.dash.find("POST", waTestStandard+"/sites/create")
	if len(requests) != 1 {
		t.Fatalf("create requests = %d", len(requests))
	}
	body := requests[0].Body
	domains, _ := body["domains"].([]any)
	if body["source"] != "external" || body["name"] != "Blog" || len(domains) != 2 || domains[0] != "blog.example.com" || domains[1] != "www.blog.example.com" || body["retentionDays"] != 90.0 {
		t.Fatalf("create body = %v", body)
	}
	if out := env.out.String(); !strings.Contains(out, `<script defer src="https://hfa.kgl.hubfly.test/s/hfa_blog000000000000.js"></script>`) {
		t.Fatalf("create output = %s", out)
	}

	if err := runErr("analytics", "sites", "create", "localhost"); err == nil || exitCodeForError(err) != 2 {
		t.Fatalf("bad domain error = %v", err)
	}
	if err := runErr("analytics", "sites", "create", "blog.example.com", "--retention", "7"); err == nil || !strings.Contains(err.Error(), "30 to 365") {
		t.Fatalf("retention error = %v", err)
	}

	env.dash.on("POST", waTestStandard+"/sites/create", func(rtRecorded) (int, any) {
		return http.StatusConflict, map[string]any{"ok": false, "error": map[string]any{"code": "ANALYTICS_DOMAIN_UNVERIFIED", "message": "blog.example.com is not a verified domain."}}
	})
	err := runErr("analytics", "sites", "create", "blog.example.com")
	if err == nil || !strings.Contains(err.Error(), "hubfly analytics verify") || exitCodeForError(err) != 4 {
		t.Fatalf("unverified error = %v", err)
	}
}

func TestAnalyticsSitesCreateNewProject(t *testing.T) {
	env := setupAnalyticsTest(t)
	env.dash.ok("GET", "/api/v1/analytics/regions", []map[string]any{
		{"id": "r-kgl", "name": "Kigali", "location": "RW"}, {"id": "r-fra", "name": "Frankfurt", "location": "DE"},
	})
	env.dash.ok("POST", "/api/v1/projects/create", map[string]any{"id": "p-new", "name": "shop.example.com", "type": "analytics"})
	env.dash.ok("POST", "/api/v1/projects/p-new/analytics/sites/create", waTestSite("as_shop", "hfa_shop000000000000", "p-new", "shop.example.com", "shop.example.com", "external"))

	if err := runErr("analytics", "sites", "create", "shop.example.com", "--project", "new"); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("confirmation error = %v", err)
	}
	if err := runErr("analytics", "sites", "create", "shop.example.com", "--project", "new", "--yes"); err == nil || !strings.Contains(err.Error(), "--region") {
		t.Fatalf("region error = %v", err)
	}
	if len(env.dash.find("POST", "/api/v1/projects/create")) != 0 {
		t.Fatal("project created without confirmation or region")
	}
	env.out.buf.Reset()
	mustRun(t, "analytics", "sites", "create", "shop.example.com", "--project", "new", "--region", "frankfurt", "--yes", "--json")
	projects := env.dash.find("POST", "/api/v1/projects/create")
	if len(projects) != 1 || projects[0].Body["type"] != "analytics" || projects[0].Body["regionId"] != "r-fra" || projects[0].Body["name"] != "shop.example.com" {
		t.Fatalf("project create = %+v", projects)
	}
	var site analyticsSite
	if err := json.Unmarshal([]byte(env.out.String()), &site); err != nil || site.ID != "as_shop" || site.ProjectName != "shop.example.com" {
		t.Fatalf("stdout = %s (%v)", env.out.String(), err)
	}
}

func TestAnalyticsSitesEnableOnHubflyRoute(t *testing.T) {
	env := setupAnalyticsTest(t)
	env.dash.ok("GET", waTestWeb+"/routes", []map[string]any{
		{"routeType": "container_endpoint", "routeId": "ep_1", "hostname": "app.example.com", "label": "app", "source": "hubfly", "siteId": "as_app"},
		{"routeType": "custom_domain", "routeId": "cd_7", "hostname": "shop.example.com", "label": "shop", "source": "custom", "siteId": nil},
	})
	created := waTestSite("as_shop", "hfa_shop000000000000", "p-web", "shop.example.com", "shop.example.com", "hubfly_route")
	env.dash.ok("POST", waTestWeb+"/sites/create", created)

	mustRun(t, "analytics", "sites", "enable", "--domain", "shop.example.com")
	requests := env.dash.find("POST", waTestWeb+"/sites/create")
	if len(requests) != 1 {
		t.Fatalf("create requests = %d", len(requests))
	}
	body := requests[0].Body
	if body["source"] != "hubfly_route" || body["routeType"] != "custom_domain" || body["routeId"] != "cd_7" || body["injectEnabled"] != true || body["domain"] != "shop.example.com" {
		t.Fatalf("enable body = %v", body)
	}
	if !strings.Contains(env.out.String(), "nothing to install") {
		t.Fatalf("enable output = %s", env.out.String())
	}
	if len(env.dash.find("GET", waTestStandard+"/routes")) != 0 {
		t.Fatal("asked a standalone Analytics project for routes")
	}
	if err := runErr("analytics", "sites", "enable", "app.example.com"); err == nil || !strings.Contains(err.Error(), "already has a site") {
		t.Fatalf("existing site error = %v", err)
	}
	if err := runErr("analytics", "sites", "enable", "--domain", "elsewhere.example.org"); err == nil || !strings.Contains(err.Error(), "sites create elsewhere.example.org") {
		t.Fatalf("unknown domain error = %v", err)
	}
}

func TestAnalyticsVerifyPrintsTXTRecord(t *testing.T) {
	env := setupAnalyticsTest(t)
	env.dash.ok("POST", waTestStandard+"/domains/verify", map[string]any{
		"domain": "blog.example.com", "status": "pending", "txtHost": "_hubfly.blog.example.com", "txtValue": "hubfly-verify=abc123", "message": "DNS changes can take a few minutes.",
	})
	mustRun(t, "analytics", "verify", "blog.example.com", "--project", "stats")
	out := env.out.String()
	if !strings.Contains(out, "_hubfly.blog.example.com") || !strings.Contains(out, "hubfly-verify=abc123") {
		t.Fatalf("verify output = %s", out)
	}
	if got := env.dash.find("POST", waTestStandard+"/domains/verify"); len(got) != 1 || got[0].Body["domain"] != "blog.example.com" {
		t.Fatalf("verify requests = %+v", got)
	}
}

func TestAnalyticsStatsQueriesAndFormats(t *testing.T) {
	env := setupAnalyticsTest(t)
	env.dash.on("POST", waTestStandard+"/sites/as_docs/query", func(request rtRecorded) (int, any) {
		meta := map[string]any{"from": "2026-09-30", "to": "2026-10-29", "timezone": "Africa/Kigali", "partial": false, "totalValues": 12}
		if request.Body["type"] == "aggregate" {
			return http.StatusOK, map[string]any{"type": "aggregate", "meta": meta, "results": map[string]any{
				"visitors":        map[string]any{"value": 1234, "comparisonValue": 1100, "change": 12.2},
				"visits":          map[string]any{"value": 1500, "comparisonValue": 1400, "change": 7.1},
				"pageviews":       map[string]any{"value": 4000, "comparisonValue": 3900, "change": 2.6},
				"views_per_visit": map[string]any{"value": 2.67, "comparisonValue": 2.79, "change": -4.3},
				"bounce_rate":     map[string]any{"value": 41.5, "comparisonValue": 44, "change": -2.5},
				"visit_duration":  map[string]any{"value": 65, "comparisonValue": 60, "change": 8.3},
			}}
		}
		return http.StatusOK, map[string]any{"type": "breakdown", "meta": meta, "results": []map[string]any{
			{"value": "/pricing", "values": map[string]any{"visitors": 120, "pageviews": 200}},
			{"value": "", "values": map[string]any{"visitors": 30, "pageviews": 31}},
		}}
	})

	mustRun(t, "analytics", "stats", "docs", "--compare", "--filter", "country=DE", "--filter", "page!=/admin")
	queries := env.dash.find("POST", waTestStandard+"/sites/as_docs/query")
	if len(queries) != 3 {
		t.Fatalf("queries = %d, want aggregate + 2 breakdowns", len(queries))
	}
	agg := queries[0].Body
	rng, _ := agg["range"].(map[string]any)
	filters, _ := agg["filters"].([]any)
	if agg["type"] != "aggregate" || agg["compare"] != "previous_period" || rng["period"] != "30d" || len(filters) != 2 {
		t.Fatalf("aggregate body = %v", agg)
	}
	if second := filters[1].(map[string]any); second["op"] != "is_not" || second["dim"] != "page" {
		t.Fatalf("filters = %v", filters)
	}
	if queries[1].Body["dim"] != "page" || queries[2].Body["dim"] != "source" || queries[1].Body["limit"] != 5.0 {
		t.Fatalf("breakdowns = %v / %v", queries[1].Body, queries[2].Body)
	}
	out := env.out.String()
	for _, want := range []string{"Docs · last 30 days (2026-09-30 to 2026-10-29), Africa/Kigali", "1,234", "+12.2%", "41.5%", "-2.5 pts", "1m 05s", "/pricing", "Direct / none", "… 10 more"} {
		if !strings.Contains(out, want) {
			t.Fatalf("stats output lacks %q:\n%s", want, out)
		}
	}

	env.out.buf.Reset()
	mustRun(t, "analytics", "stats", "docs", "--from", "2026-10-01", "--to", "2026-10-07", "--by", "country", "--limit", "3", "--json")
	var result analyticsStatsResult
	if err := json.Unmarshal([]byte(env.out.String()), &result); err != nil {
		t.Fatalf("stdout is not pure JSON: %v\n%s", err, env.out.String())
	}
	if len(result.Breakdowns) != 1 || len(result.Breakdowns["country"].Results) != 2 || result.Aggregate.Results["visitors"].Value != 1234.0 {
		t.Fatalf("json = %+v", result)
	}
	last := env.dash.find("POST", waTestStandard+"/sites/as_docs/query")
	if body := last[len(last)-1].Body; body["dim"] != "country" || body["limit"] != 3.0 || body["range"].(map[string]any)["from"] != "2026-10-01" {
		t.Fatalf("custom breakdown body = %v", body)
	}

	for _, args := range [][]string{
		{"analytics", "stats", "docs", "--period", "forever"},
		{"analytics", "stats", "docs", "--from", "2026-10-07"},
		{"analytics", "stats", "docs", "--from", "2026-10-07", "--to", "2026-10-01"},
		{"analytics", "stats", "docs", "--filter", "country"},
	} {
		if err := runErr(args...); err == nil || exitCodeForError(err) != 2 {
			t.Fatalf("run(%q) error = %v, want a usage error", args, err)
		}
	}
}

func TestAnalyticsRealtime(t *testing.T) {
	env := setupAnalyticsTest(t)
	env.dash.ok("POST", waTestStandard+"/sites/as_docs/query", map[string]any{
		"type": "realtime", "visitors": 12, "pageviews": 140, "perMinute": []any{},
		"topPages":   []map[string]any{{"value": "/", "visitors": 5}},
		"topSources": []map[string]any{{"value": "", "visitors": 3}}, "topCountries": []any{},
		"recent": []map[string]any{{"ts": "2026-10-29T10:01:02Z", "type": "pageview", "name": nil, "page": "/docs", "country": "DE", "city": "Berlin", "device": "desktop", "browser": "Chrome", "source": "Google"}},
	})
	mustRun(t, "analytics", "realtime", "docs")
	out := env.out.String()
	for _, want := range []string{"12 visitors in the last 5 minutes", "140 pageviews", "Direct / none", "/docs", "Google"} {
		if !strings.Contains(out, want) {
			t.Fatalf("realtime output lacks %q:\n%s", want, out)
		}
	}
	if body := env.dash.find("POST", waTestStandard+"/sites/as_docs/query")[0].Body; body["type"] != "realtime" || body["range"] != nil {
		t.Fatalf("realtime body = %v", body)
	}
	if err := runErr("analytics", "realtime", "docs", "--watch", "--json"); err == nil {
		t.Fatal("--watch --json accepted")
	}
}

func TestAnalyticsExportWritesFile(t *testing.T) {
	env := setupAnalyticsTest(t)
	env.dash.ok("GET", waTestStandard+"/sites/as_docs/export", map[string]any{
		"filename": "docs-events.csv", "contentType": "text/csv", "content": "timestamp,type\n2026-10-01T10:00:00Z,pageview\n", "truncated": true, "bytes": 46,
	})
	dir := t.TempDir()
	target := filepath.Join(dir, "events.csv")
	mustRun(t, "analytics", "export", "docs", "--from", "2026-10-01", "--to", "2026-10-07", "--kind", "sessions", "-o", target)
	data, err := os.ReadFile(target)
	if err != nil || !strings.HasPrefix(string(data), "timestamp,type\n") {
		t.Fatalf("file = %q, %v", data, err)
	}
	request := env.dash.find("GET", waTestStandard+"/sites/as_docs/export")[0]
	if request.Query.Get("from") != "2026-10-01" || request.Query.Get("to") != "2026-10-07" || request.Query.Get("kind") != "sessions" || request.Query.Get("format") != "csv" {
		t.Fatalf("export query = %v", request.Query)
	}
	if !strings.Contains(env.errOut.String(), "8 MB") {
		t.Fatalf("stderr = %s", env.errOut.String())
	}
	if err := runErr("analytics", "export", "docs", "--from", "2026-10-01", "--to", "2026-10-07", "-o", target); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("overwrite error = %v", err)
	}
	env.out.buf.Reset()
	mustRun(t, "analytics", "export", "docs", "--from", "2026-10-01", "--to", "2026-10-07", "-o", "-")
	if env.out.String() != "timestamp,type\n2026-10-01T10:00:00Z,pageview\n" {
		t.Fatalf("stdout = %q", env.out.String())
	}
	if err := runErr("analytics", "export", "docs", "--to", "2026-10-07"); err == nil || exitCodeForError(err) != 2 {
		t.Fatalf("missing --from error = %v", err)
	}
}

func TestAnalyticsKeysCreateShowsKeyOnce(t *testing.T) {
	env := setupAnalyticsTest(t)
	const secret = "hfak_abcdefghijklmnopqrstuvwxyz012345"
	env.dash.ok("POST", waTestStandard+"/sites/as_docs/keys/create", map[string]any{
		"key": secret, "synced": true,
		"ingestKey": map[string]any{"id": "ak_1", "siteId": "as_docs", "name": "backend", "prefix": "hfak_abcd", "createdAt": "2026-10-29T08:00:00Z", "lastUsedAt": nil, "revokedAt": nil},
	})
	env.dash.ok("GET", waTestStandard+"/sites/as_docs/keys", []map[string]any{
		{"id": "ak_1", "siteId": "as_docs", "name": "backend", "prefix": "hfak_abcd", "createdAt": "2026-10-29T08:00:00Z", "lastUsedAt": nil, "revokedAt": nil},
	})
	env.dash.ok("POST", waTestStandard+"/sites/as_docs/keys/ak_1/revoke", map[string]any{"id": "ak_1", "siteId": "as_docs", "name": "backend", "prefix": "hfak_abcd", "createdAt": "2026-10-29T08:00:00Z", "lastUsedAt": nil, "revokedAt": "2026-10-29T09:00:00Z"})

	mustRun(t, "analytics", "keys", "create", "docs", "backend")
	if out := env.out.String(); strings.Count(out, secret) != 1 || !strings.Contains(out, "shown once") {
		t.Fatalf("create output = %s", out)
	}
	env.out.buf.Reset()
	mustRun(t, "analytics", "keys", "docs")
	if out := env.out.String(); strings.Contains(out, secret) || !strings.Contains(out, "hfak_abcd…") {
		t.Fatalf("list output = %s", out)
	}
	if err := runErr("analytics", "keys", "revoke", "docs", "backend"); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("revoke confirmation error = %v", err)
	}
	mustRun(t, "analytics", "keys", "revoke", "docs", "backend", "--yes")
	if len(env.dash.find("POST", waTestStandard+"/sites/as_docs/keys/ak_1/revoke")) != 1 {
		t.Fatal("key not revoked")
	}
}

func TestAnalyticsSiteLifecycle(t *testing.T) {
	env := setupAnalyticsTest(t)
	paused := waTestSite("as_docs", "hfa_docs000000000000", "p-an", "Docs", "docs.example.com", "external")
	paused["state"] = "paused"
	env.dash.ok("PUT", waTestStandard+"/sites/as_docs/update", paused)
	env.dash.ok("DELETE", waTestStandard+"/sites/as_docs/delete", map[string]any{"deleted": true, "pending": false})
	env.dash.ok("GET", waTestStandard+"/sites/as_docs/status", map[string]any{
		"siteId": "as_docs", "reachable": true, "installed": false, "lastEventAt": nil, "eventsToday": 0, "rawEvents": 0, "dbBytes": 0, "summaryDays": 0,
	})

	mustRun(t, "analytics", "sites", "pause", "docs")
	if got := env.dash.find("PUT", waTestStandard+"/sites/as_docs/update"); len(got) != 1 || got[0].Body["state"] != "paused" {
		t.Fatalf("pause requests = %+v", got)
	}
	mustRun(t, "analytics", "sites", "status", "docs")
	if !strings.Contains(env.out.String(), "has not received any event yet") {
		t.Fatalf("status output = %s", env.out.String())
	}
	if err := runErr("analytics", "sites", "delete", "docs"); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("delete confirmation error = %v", err)
	}
	mustRun(t, "analytics", "sites", "delete", "docs", "--yes")
	if len(env.dash.find("DELETE", waTestStandard+"/sites/as_docs/delete")) != 1 {
		t.Fatal("site not deleted")
	}
}

func TestAnalyticsUsage(t *testing.T) {
	env := setupAnalyticsTest(t)
	env.dash.ok("GET", waTestStandard+"/usage", map[string]any{
		"month":           "2026-10",
		"events":          map[string]any{"billable": 1250000, "filtered": 300, "notCollected": 0, "last24h": 42000, "projectedMonthEnd": 1400000},
		"sites":           []map[string]any{{"siteId": "as_docs", "publicId": "hfa_docs000000000000", "name": "Docs", "billable": 1250000, "historyBytesOver30d": 0}},
		"allowance":       map[string]any{"freeEventsPerMonth": 100000, "ownerFreeUsed": 100000, "ownerFreeRemaining": 0},
		"monthlyEventCap": 5000000,
		"collection":      []map[string]any{{"regionId": "r-kgl", "paidAllowed": false, "reason": "balance", "paused": true, "eventCapReached": false}},
		"cost":            map[string]any{"priceMicroUsdPerMillion": 2000000, "paidEvents": 1150000, "billedMicroUsd": 2300000, "projectedMonthEndMicroUsd": 2600000, "historyBytesOver30d": 0, "historyMonthlyMicroUsd": 0},
		"updatedThrough":  "2026-10-29T10:00:00Z",
	})
	mustRun(t, "analytics", "usage", "stats")
	out := env.out.String()
	for _, want := range []string{"1,250,000 billable", "0 of 100,000 left", "5,000,000 per month", "$2.30", "$2.60", "a low balance"} {
		if !strings.Contains(out, want) {
			t.Fatalf("usage output lacks %q:\n%s", want, out)
		}
	}
	if err := runErr("analytics", "usage"); err == nil || !strings.Contains(err.Error(), "several projects") {
		t.Fatalf("usage without project error = %v", err)
	}
}

func TestNormalizeAnalyticsDomain(t *testing.T) {
	cases := map[string]string{
		"Example.com":                       "example.com",
		"https://www.example.com/pricing?x": "www.example.com",
		"shop.example.com:8443":             "shop.example.com",
		"docs.example.com.":                 "docs.example.com",
	}
	for input, want := range cases {
		if got, err := normalizeAnalyticsDomain(input); err != nil || got != want {
			t.Fatalf("normalizeAnalyticsDomain(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	for _, input := range []string{"", "localhost", "*.example.com", "user@example.com"} {
		if _, err := normalizeAnalyticsDomain(input); err == nil {
			t.Fatalf("normalizeAnalyticsDomain(%q) succeeded", input)
		}
	}
}

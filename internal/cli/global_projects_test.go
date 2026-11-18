package cli

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

const gpTestID = "gpj_11111111-2222-3333-4444-555555555555"

func gpSummary() map[string]any {
	return map[string]any{
		"id": gpTestID, "kind": "global", "name": "atlas", "status": "active",
		"ownership": "personal", "organizationId": nil, "organizationName": nil, "groupId": nil,
		"budget": "50.00",
		"regions": []map[string]any{
			{"id": "eu1", "name": "Europe", "location": "Germany"},
			{"id": "us1", "name": "US East", "location": "Virginia"},
		},
		"hasManagedServices": true,
		"counts": map[string]any{
			"cells": 3, "runningCells": 2, "volumes": 1, "boxes": 0,
			"databases": 1, "realtime": 0, "streams": 0, "storage": 1,
		},
		"billing": map[string]any{
			"hourlyCost": "0.0410", "monthlyCost": "29.93", "spentAmount": "4.12", "totalConsumed": "9.80",
		},
		"permissions": map[string]any{"canEdit": true, "canCreate": true, "canDelete": true, "canManageBilling": true},
		"createdAt":   "2026-11-01T10:00:00.000Z", "updatedAt": "2026-11-10T10:00:00.000Z",
	}
}

// setupGlobalProjectsTest is setupRealtimeTest with one classic Cell project
// (p-web, Europe), one global project (atlas) and the regions list.
func setupGlobalProjectsTest(t *testing.T) rtTestEnv {
	t.Helper()
	env := setupRealtimeTest(t)
	env.dash.ok("GET", "/api/v1/projects", map[string]any{
		"items": []map[string]any{{
			"id": "p-web", "name": "web", "type": "cell", "status": "active", "role": "owner",
			"spentAmount": "1.50", "regionId": "eu1", "regionName": "Europe", "regionLocation": "Germany",
		}},
		"page": 1, "pageCount": 1,
	})
	env.dash.ok("GET", "/api/v1/global-projects", map[string]any{"enabled": true, "items": []any{gpSummary()}})
	env.dash.ok("GET", "/api/v1/global-projects/"+gpTestID, gpSummary())
	env.dash.ok("GET", "/api/v1/regions", []map[string]any{
		{"id": "eu1", "name": "Europe", "location": "Germany", "products": map[string]any{"cell": true}},
		{"id": "us1", "name": "US East", "location": "Virginia", "products": map[string]any{"cell": true}},
	})
	env.dash.on("POST", "/api/v1/global-projects/"+gpTestID+"/children/ensure", func(r rtRecorded) (int, any) {
		region, _ := r.Body["regionId"].(string)
		return http.StatusOK, map[string]any{"projectId": "prj_cell_" + region, "type": "cell", "regionId": region, "created": region == "us1"}
	})
	return env
}

func TestIsGlobalProjectID(t *testing.T) {
	if !isGlobalProjectID(gpTestID) || !isGlobalProjectID(" "+gpTestID) {
		t.Fatal("gpj_ ids should be global")
	}
	for _, id := range []string{"", "prj_123", "web", "gpj"} {
		if isGlobalProjectID(id) {
			t.Fatalf("%q should not be global", id)
		}
	}
}

func TestProjectListJSONIncludesGlobalProjects(t *testing.T) {
	env := setupGlobalProjectsTest(t)
	mustRun(t, "project", "list", "--json")

	var items []map[string]any
	if err := json.Unmarshal([]byte(env.out.String()), &items); err != nil {
		t.Fatalf("output is not a JSON array: %v\n%s", err, env.out.String())
	}
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2", len(items))
	}
	classic, global := items[0], items[1]
	if classic["kind"] != "classic" || classic["id"] != "p-web" || classic["spentAmount"] != "1.50" || classic["regionId"] != "eu1" {
		t.Fatalf("classic entry = %#v", classic)
	}
	if regions, _ := classic["regions"].([]any); len(regions) != 1 {
		t.Fatalf("classic regions = %#v", classic["regions"])
	}
	if global["kind"] != "global" || global["id"] != gpTestID {
		t.Fatalf("global entry = %#v", global)
	}
	// The Dashboard summary passes through unchanged (permissions included).
	if _, ok := global["permissions"]; !ok {
		t.Fatalf("global entry lost server fields: %#v", global)
	}
	if regions, _ := global["regions"].([]any); len(regions) != 2 {
		t.Fatalf("global regions = %#v", global["regions"])
	}
}

func TestProjectListTableShowsGlobalProjects(t *testing.T) {
	env := setupGlobalProjectsTest(t)
	mustRun(t, "project", "list")
	out := env.out.String()
	for _, want := range []string{"KIND", "classic", "web", "global", "atlas", "Europe, US East, managed", gpTestID} {
		if !strings.Contains(out, want) {
			t.Fatalf("table missing %q:\n%s", want, out)
		}
	}
}

func TestProjectListToleratesServersWithoutGlobalProjects(t *testing.T) {
	cases := map[string]func(rtRecorded) (int, any){
		"gate off":     pmAPIError(http.StatusNotFound, "GLOBAL_PROJECTS_UNAVAILABLE", "Global projects are not available."),
		"old server":   pmAPIError(http.StatusNotFound, "NOT_FOUND", "Not found"),
		"scoped token": pmAPIError(http.StatusForbidden, "GLOBAL_PROJECT_SCOPED_TOKEN", "Scoped tokens can't use global projects."),
	}
	for name, handler := range cases {
		t.Run(name, func(t *testing.T) {
			env := setupGlobalProjectsTest(t)
			env.dash.on("GET", "/api/v1/global-projects", handler)
			mustRun(t, "project", "list", "--json")
			var items []map[string]any
			if err := json.Unmarshal([]byte(env.out.String()), &items); err != nil {
				t.Fatal(err)
			}
			if len(items) != 1 || items[0]["kind"] != "classic" {
				t.Fatalf("items = %#v", items)
			}
			if env.errOut.String() != "" {
				t.Fatalf("unexpected stderr: %s", env.errOut.String())
			}
		})
	}
}

func TestProjectListWarnsWhenGlobalProjectsFail(t *testing.T) {
	env := setupGlobalProjectsTest(t)
	env.dash.on("GET", "/api/v1/global-projects", pmAPIError(http.StatusBadRequest, "BAD_REQUEST", "Bad request"))
	mustRun(t, "project", "list")
	if !strings.Contains(env.out.String(), "web") || strings.Contains(env.out.String(), "atlas") {
		t.Fatalf("output = %s", env.out.String())
	}
	if !strings.Contains(env.errOut.String(), "could not list global projects") {
		t.Fatalf("stderr = %q", env.errOut.String())
	}
}

func TestProjectGetShowsGlobalSummary(t *testing.T) {
	env := setupGlobalProjectsTest(t)
	mustRun(t, "project", "get", gpTestID)
	out := env.out.String()
	for _, want := range []string{"atlas", "Kind:", "global", "Europe, US East, managed", "3 cells (2 running)", "$4.12 this month", "Monthly Cap:", "$50.00", "--project " + gpTestID + " --region"} {
		if !strings.Contains(out, want) {
			t.Fatalf("summary missing %q:\n%s", want, out)
		}
	}
}

func TestProjectGetJSONAndByName(t *testing.T) {
	env := setupGlobalProjectsTest(t)
	mustRun(t, "project", "get", "ATLAS", "--json")
	var got map[string]any
	if err := json.Unmarshal([]byte(env.out.String()), &got); err != nil {
		t.Fatal(err)
	}
	if got["id"] != gpTestID || got["kind"] != "global" {
		t.Fatalf("get by name = %#v", got)
	}
}

func TestProjectGetClassicProject(t *testing.T) {
	env := setupGlobalProjectsTest(t)
	mustRun(t, "project", "get", "web")
	if out := env.out.String(); !strings.Contains(out, "classic") || !strings.Contains(out, "p-web") {
		t.Fatalf("classic summary = %s", out)
	}
	if len(env.dash.find("GET", "/api/v1/global-projects")) != 0 {
		t.Fatal("a classic match should not list global projects")
	}
}

func TestProjectGetUnlimitedCap(t *testing.T) {
	if got := globalProjectCapText("-1.00"); got != "unlimited" {
		t.Fatalf("cap = %q", got)
	}
	if got := globalProjectCapText("25.00"); got != "$25.00" {
		t.Fatalf("cap = %q", got)
	}
}

func TestDeployGlobalProjectRequiresRegion(t *testing.T) {
	env := setupGlobalProjectsTest(t)
	cfg := deployConfigFile{}
	err := ensureDeployProjectBinding("hf_test_token", t.TempDir(), &cfg, deployOptions{Project: gpTestID})
	if err == nil || !strings.Contains(err.Error(), "--region") {
		t.Fatalf("err = %v", err)
	}
	if len(env.dash.find("POST", "/api/v1/global-projects/"+gpTestID+"/children/ensure")) != 0 {
		t.Fatal("no child should be ensured without a region")
	}
}

func TestDeployGlobalProjectEnsuresCellChildInRegion(t *testing.T) {
	env := setupGlobalProjectsTest(t)
	cfg := deployConfigFile{}
	cfg.Container.ID = "ctr_old"
	cfg.Project.ID = "p-web"
	if err := ensureDeployProjectBinding("hf_test_token", t.TempDir(), &cfg, deployOptions{Project: gpTestID, Region: "US East"}); err != nil {
		t.Fatal(err)
	}
	ensured := env.dash.find("POST", "/api/v1/global-projects/"+gpTestID+"/children/ensure")
	if len(ensured) != 1 || ensured[0].Body["type"] != "cell" || ensured[0].Body["regionId"] != "us1" {
		t.Fatalf("ensure requests = %#v", ensured)
	}
	if cfg.Project.ID != "prj_cell_us1" || cfg.Project.Region != "us1" || cfg.Project.Name != "atlas" {
		t.Fatalf("project binding = %#v", cfg.Project)
	}
	if cfg.Container.ID != "" {
		t.Fatalf("container from another project should be unbound, got %q", cfg.Container.ID)
	}
}

func TestDeployGlobalProjectFromConfigUsesConfigRegion(t *testing.T) {
	env := setupGlobalProjectsTest(t)
	cfg := deployConfigFile{}
	cfg.Project.ID = gpTestID
	cfg.Project.Region = "eu1"
	if err := ensureDeployProjectBinding("hf_test_token", t.TempDir(), &cfg, deployOptions{}); err != nil {
		t.Fatal(err)
	}
	if cfg.Project.ID != "prj_cell_eu1" || cfg.Project.Region != "eu1" {
		t.Fatalf("project binding = %#v", cfg.Project)
	}
	if len(env.dash.find("GET", "/api/v1/projects")) != 0 {
		t.Fatal("a global target should not list classic projects")
	}
}

func TestDeployBoundGlobalChildKeepsGlobalProject(t *testing.T) {
	env := setupGlobalProjectsTest(t)
	env.dash.ok("GET", "/api/v1/projects/prj_cell_eu1", map[string]any{
		"id": "prj_cell_eu1", "name": "atlas", "type": "cell", "globalProjectId": gpTestID,
		"region": map[string]any{"id": "eu1", "name": "Europe"},
	})

	// Next deploy: the config holds the child id the last deploy saved.
	cfg := deployConfigFile{}
	cfg.Project.ID = "prj_cell_eu1"
	cfg.Project.Region = "eu1"
	cfg.Container.ID = "ctr_eu"
	if err := ensureDeployProjectBinding("hf_test_token", t.TempDir(), &cfg, deployOptions{}); err != nil {
		t.Fatal(err)
	}
	if cfg.Project.ID != "prj_cell_eu1" || cfg.Container.ID != "ctr_eu" {
		t.Fatalf("binding changed: %#v %#v", cfg.Project, cfg.Container)
	}

	// --region moves the deploy to that region's Cell project.
	if err := ensureDeployProjectBinding("hf_test_token", t.TempDir(), &cfg, deployOptions{Region: "us1"}); err != nil {
		t.Fatal(err)
	}
	if cfg.Project.ID != "prj_cell_us1" || cfg.Project.Region != "us1" || cfg.Container.ID != "" {
		t.Fatalf("region switch binding = %#v %#v", cfg.Project, cfg.Container)
	}

	// --project gpj_… with the child bound reuses the config's region.
	cfg.Project.ID = "prj_cell_eu1"
	cfg.Project.Region = "eu1"
	if err := ensureDeployProjectBinding("hf_test_token", t.TempDir(), &cfg, deployOptions{Project: gpTestID}); err != nil {
		t.Fatal(err)
	}
	if cfg.Project.ID != "prj_cell_eu1" {
		t.Fatalf("binding = %#v", cfg.Project)
	}
}

func TestBindGlobalChildIgnoresClassicProjects(t *testing.T) {
	env := setupGlobalProjectsTest(t)
	env.dash.ok("GET", "/api/v1/projects/prj_classic", map[string]any{"id": "prj_classic", "globalProjectId": nil})
	cfg := deployConfigFile{}
	handled, err := bindGlobalChildDeployProject("hf_test_token", &cfg, deployOptions{}, "prj_classic", "")
	if handled || err != nil {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	// Unknown ids (404) are not global children either.
	handled, err = bindGlobalChildDeployProject("hf_test_token", &cfg, deployOptions{}, "prj_missing", "")
	if handled || err != nil {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
}

func TestDeployPlanDoesNotCreateGlobalChild(t *testing.T) {
	env := setupGlobalProjectsTest(t)
	env.dash.ok("GET", "/api/v1/global-projects/"+gpTestID+"/graph", map[string]any{
		"globalProjectId": gpTestID,
		"children":        []map[string]any{{"projectId": "prj_cell_eu1", "type": "cell", "regionId": "eu1"}},
	})
	ensurePath := "/api/v1/global-projects/" + gpTestID + "/children/ensure"

	cfg := deployConfigFile{}
	if err := ensureDeployProjectBinding("hf_test_token", t.TempDir(), &cfg, deployOptions{Project: gpTestID, Region: "eu1", PlanOnly: true}); err != nil {
		t.Fatal(err)
	}
	if cfg.Project.ID != "prj_cell_eu1" {
		t.Fatalf("plan binding = %#v", cfg.Project)
	}

	cfg = deployConfigFile{}
	err := ensureDeployProjectBinding("hf_test_token", t.TempDir(), &cfg, deployOptions{Project: gpTestID, Region: "us1", PlanOnly: true})
	if !errors.Is(err, errPlanNeedsNewProject) {
		t.Fatalf("err = %v, want errPlanNeedsNewProject", err)
	}
	if len(env.dash.find("POST", ensurePath)) != 0 {
		t.Fatal("a dry run must not create a regional project")
	}
}

func TestDeployGlobalProjectScopedTokenHint(t *testing.T) {
	env := setupGlobalProjectsTest(t)
	env.dash.on("GET", "/api/v1/global-projects/"+gpTestID, pmAPIError(http.StatusForbidden, "GLOBAL_PROJECT_SCOPED_TOKEN", "Scoped tokens can't use global projects."))
	cfg := deployConfigFile{}
	err := ensureDeployProjectBinding("hf_test_token", t.TempDir(), &cfg, deployOptions{Project: gpTestID, Region: "eu1"})
	if err == nil || !strings.Contains(err.Error(), "hubfly login") {
		t.Fatalf("err = %v", err)
	}
}

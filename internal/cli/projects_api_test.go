package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestNormalizeProjectSupportsFlatRegionFields(t *testing.T) {
	got := normalizeProject(project{
		RegionID:       "region-1",
		RegionName:     "Europe",
		RegionLocation: "eu-1",
	})

	if got.Region.ID != "region-1" || got.Region.Name != "Europe" || got.Region.Location != "eu-1" {
		t.Fatalf("normalized region = %#v", got.Region)
	}
}

func TestNormalizeProjectPreservesLegacyNestedRegion(t *testing.T) {
	got := normalizeProject(project{
		Region:         region{ID: "legacy", Name: "Legacy", Location: "old-1"},
		RegionID:       "flat",
		RegionName:     "Flat",
		RegionLocation: "new-1",
	})

	if got.Region.ID != "legacy" || got.Region.Name != "Legacy" || got.Region.Location != "old-1" {
		t.Fatalf("legacy region was overwritten: %#v", got.Region)
	}
}

func TestRegionProductsRepresentCellDeployAvailability(t *testing.T) {
	var item region
	item.Products = &struct {
		Cell bool `json:"cell"`
	}{Cell: true}
	item = normalizeRegion(item)

	if !item.Available {
		t.Fatal("cell-capable region should be deployable")
	}
}

func TestCreateProjectForDeployUsesCurrentContract(t *testing.T) {
	originalAPIHost := apiHost
	t.Cleanup(func() { apiHost = originalAPIHost })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		var input map[string]string
		if err := json.Unmarshal(body, &input); err != nil {
			t.Fatal(err)
		}
		if input["type"] != "cell" || input["regionId"] != "region-1" {
			t.Fatalf("create input = %#v", input)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"data":{"id":"project-1","name":"demo","type":"cell","regionId":"region-1"}}`))
	}))
	defer server.Close()
	apiHost = server.URL

	created, err := createProjectForDeploy("token", "demo", "region-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if created.Region.ID != "region-1" {
		t.Fatalf("created project region = %#v", created.Region)
	}
}

func TestFetchProjectsRequestsLargePagesAndKeepsOrder(t *testing.T) {
	env := setupRealtimeTest(t)
	const pageCount = 5
	env.dash.on("GET", "/api/v1/projects", func(r rtRecorded) (int, any) {
		page, _ := strconv.Atoi(r.Query.Get("page"))
		return http.StatusOK, map[string]any{
			"items": []map[string]any{
				{"id": fmt.Sprintf("p-%d", page), "name": "cell", "type": "cell"},
				{"id": fmt.Sprintf("b-%d", page), "name": "box", "type": "box"},
			},
			"page": page, "pageCount": pageCount,
		}
	})

	projects, err := fetchProjectsWithOrg("token", "")
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(projects))
	for _, p := range projects {
		got = append(got, p.ID)
	}
	if want := "p-1 p-2 p-3 p-4 p-5"; strings.Join(got, " ") != want {
		t.Fatalf("projects = %v, want %s", got, want)
	}
	requests := env.dash.find("GET", "/api/v1/projects")
	if len(requests) != pageCount {
		t.Fatalf("requests = %d, want %d", len(requests), pageCount)
	}
	for _, r := range requests {
		if r.Query.Get("pageSize") != strconv.Itoa(projectsPageSize) {
			t.Fatalf("pageSize = %q, want %d", r.Query.Get("pageSize"), projectsPageSize)
		}
	}
}

func TestFetchProjectsFailsWhenAnyPageFails(t *testing.T) {
	env := setupRealtimeTest(t)
	env.dash.on("GET", "/api/v1/projects", func(r rtRecorded) (int, any) {
		if r.Query.Get("page") == "3" {
			return http.StatusBadRequest, map[string]any{"error": map[string]any{"code": "BAD", "message": "boom"}}
		}
		return http.StatusOK, map[string]any{"items": []any{}, "page": 1, "pageCount": 4}
	})
	if _, err := fetchProjectsWithOrg("token", ""); err == nil {
		t.Fatal("expected an error when a page fails")
	}
}

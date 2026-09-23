package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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

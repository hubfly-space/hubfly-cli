package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseVolumeListOptions(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantProject string
		wantErr     bool
	}{
		{
			name:        "empty args",
			args:        nil,
			wantProject: "",
		},
		{
			name:        "flags with project",
			args:        []string{"--project", "my-project"},
			wantProject: "my-project",
		},
		{
			name:        "short flag with project",
			args:        []string{"-p", "proj-abc"},
			wantProject: "proj-abc",
		},
		{
			name:        "positional project",
			args:        []string{"pos-project"},
			wantProject: "pos-project",
		},
		{
			name:    "missing project argument",
			args:    []string{"--project"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts, err := parseVolumeListOptions(tt.args)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseVolumeListOptions(%v) error = %v, wantErr %v", tt.args, err, tt.wantErr)
			}
			if !tt.wantErr && opts.projectFilter != tt.wantProject {
				t.Errorf("projectFilter = %q, want %q", opts.projectFilter, tt.wantProject)
			}
		})
	}
}

func TestVolumeCommandRoutingValidation(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "attach missing args", args: []string{"volume", "attach"}, wantErr: "usage: hubfly volume attach"},
		{name: "detach missing arg", args: []string{"volume", "detach"}, wantErr: "usage: hubfly volume detach"},
		{name: "resize missing arg", args: []string{"volume", "resize"}, wantErr: "usage: hubfly volume resize"},
		{name: "delete missing arg", args: []string{"volume", "delete"}, wantErr: "usage: hubfly volume delete"},
		{name: "unknown subcommand", args: []string{"volume", "unknown-cmd"}, wantErr: "unknown volume command"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := run(tt.args)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("run(%q) error = %v, want substring %q", tt.args, err, tt.wantErr)
			}
		})
	}
}

func TestVolumeAPIOperations(t *testing.T) {
	originalAPIHost := apiHost
	t.Cleanup(func() { apiHost = originalAPIHost })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/box-volumes"):
			json.NewEncoder(w).Encode([]BoxVolume{
				{
					ID:      "bv-1",
					Name:    "db-data",
					SizeGiB: 25,
					Status:  "available",
				},
			})
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/box-volumes/create"):
			var input struct {
				Name    string `json:"name"`
				SizeGiB int    `json:"sizeGib"`
			}
			_ = json.NewDecoder(r.Body).Decode(&input)
			json.NewEncoder(w).Encode(BoxVolume{
				ID:      "bv-2",
				Name:    input.Name,
				SizeGiB: input.SizeGiB,
				Status:  "creating",
			})
		case r.Method == "POST" && strings.Contains(r.URL.Path, "/box-volumes/bv-1/mutate"):
			var input struct {
				Action string `json:"action"`
				BoxID  string `json:"boxId"`
			}
			_ = json.NewDecoder(r.Body).Decode(&input)
			status := "available"
			if input.Action == "attach" {
				status = "attached"
			}
			json.NewEncoder(w).Encode(BoxVolume{
				ID:            "bv-1",
				Name:          "db-data",
				SizeGiB:       25,
				Status:        status,
				AttachedBoxID: input.BoxID,
			})
		default:
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
		}
	}))
	defer server.Close()

	apiHost = server.URL

	// 1. Fetch box volumes
	volumes, err := fetchProjectBoxVolumes("test-token", "proj-1")
	if err != nil {
		t.Fatalf("fetchProjectBoxVolumes failed: %v", err)
	}
	if len(volumes) != 1 || volumes[0].ID != "bv-1" || volumes[0].Name != "db-data" {
		t.Fatalf("unexpected volumes: %+v", volumes)
	}

	// 2. Create box volume
	created, err := createProjectBoxVolume("test-token", "proj-1", "redis-data", 10)
	if err != nil {
		t.Fatalf("createProjectBoxVolume failed: %v", err)
	}
	if created.ID != "bv-2" || created.Name != "redis-data" || created.SizeGiB != 10 {
		t.Fatalf("unexpected created volume: %+v", created)
	}

	// 3. Mutate box volume (attach)
	err = mutateProjectBoxVolume("test-token", "proj-1", "bv-1", "attach", map[string]any{
		"boxId": "box-101",
	})
	if err != nil {
		t.Fatalf("mutateProjectBoxVolume failed: %v", err)
	}
}

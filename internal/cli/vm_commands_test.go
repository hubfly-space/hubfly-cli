package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseVMListOptions(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantProject string
		wantOrg     string
		wantErr     bool
	}{
		{
			name:        "empty args",
			args:        nil,
			wantProject: "",
			wantOrg:     "",
		},
		{
			name:        "flags with project and org",
			args:        []string{"--project", "proj-box", "--org", "my-org"},
			wantProject: "proj-box",
			wantOrg:     "my-org",
		},
		{
			name:        "positional project",
			args:        []string{"proj-box"},
			wantProject: "proj-box",
		},
		{
			name:    "missing project argument",
			args:    []string{"--project"},
			wantErr: true,
		},
		{
			name:    "missing org argument",
			args:    []string{"--org"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts, err := parseVMListOptions(tt.args)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseVMListOptions(%v) error = %v, wantErr %v", tt.args, err, tt.wantErr)
			}
			if !tt.wantErr {
				if opts.projectFilter != tt.wantProject {
					t.Errorf("projectFilter = %q, want %q", opts.projectFilter, tt.wantProject)
				}
				if opts.orgFilter != tt.wantOrg {
					t.Errorf("orgFilter = %q, want %q", opts.orgFilter, tt.wantOrg)
				}
			}
		})
	}
}

func TestVMCommandRoutingValidation(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "inspect missing arg", args: []string{"vm", "inspect"}, wantErr: "usage: hubfly vm inspect"},
		{name: "start missing arg", args: []string{"vm", "start"}, wantErr: "usage: hubfly vm start"},
		{name: "stop missing arg", args: []string{"vm", "stop"}, wantErr: "usage: hubfly vm stop"},
		{name: "restart missing arg", args: []string{"vm", "restart"}, wantErr: "usage: hubfly vm restart"},
		{name: "force-stop missing arg", args: []string{"vm", "force-stop"}, wantErr: "usage: hubfly vm force-stop"},
		{name: "resize missing arg", args: []string{"vm", "resize"}, wantErr: "usage: hubfly vm resize"},
		{name: "delete missing arg", args: []string{"vm", "delete"}, wantErr: "usage: hubfly vm delete"},
		{name: "create missing name", args: []string{"vm", "create"}, wantErr: "usage: hubfly vm create"},
		{name: "unknown subcommand", args: []string{"vm", "unknown-cmd"}, wantErr: "unknown vm command"},
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

func TestVMAPIOperations(t *testing.T) {
	originalAPIHost := apiHost
	t.Cleanup(func() { apiHost = originalAPIHost })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/boxes/images"):
			_, _ = w.Write([]byte(`{
				"images": [
					{"id": "ubuntu-22.04", "name": "Ubuntu 22.04 LTS", "family": "ubuntu", "version": "22.04", "defaultUser": "ubuntu"}
				]
			}`))

		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/boxes"):
			_, _ = w.Write([]byte(`[
				{
					"id": "box-123",
					"name": "dev-box",
					"status": "running",
					"vcpus": 2,
					"memoryMib": 2048,
					"rootDiskGib": 20,
					"imageId": "ubuntu-22.04"
				}
			]`))

		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/boxes/box-123/runtime"):
			_, _ = w.Write([]byte(`{
				"id": "box-123",
				"name": "dev-box",
				"status": "running",
				"vcpus": 2,
				"memoryMib": 2048,
				"rootDiskGib": 20
			}`))

		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/boxes/box-123/control"):
			body, _ := io.ReadAll(r.Body)
			var payload map[string]string
			_ = json.Unmarshal(body, &payload)
			if payload["action"] != "shutdown" {
				http.Error(w, "invalid action", http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"success": true}`))

		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/boxes/box-123/resize"):
			body, _ := io.ReadAll(r.Body)
			var input BoxResizeInput
			_ = json.Unmarshal(body, &input)
			if input.VCPUs != 4 {
				http.Error(w, "invalid vcpus", http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"success": true}`))

		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/boxes/box-123/delete"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"success": true}`))

		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/boxes/create"):
			body, _ := io.ReadAll(r.Body)
			var input BoxCreateInput
			_ = json.Unmarshal(body, &input)
			if input.Name != "new-vm" || input.ImageID != "ubuntu-22.04" {
				http.Error(w, "invalid input", http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"id": "box-999",
				"operationId": "boxop-123"
			}`))

		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	apiHost = server.URL

	// Test fetchProjectBoxes
	boxes, err := fetchProjectBoxes("test-token", "proj-1")
	if err != nil {
		t.Fatalf("fetchProjectBoxes failed: %v", err)
	}
	if len(boxes) != 1 || boxes[0].ID != "box-123" || boxes[0].Name != "dev-box" {
		t.Fatalf("unexpected boxes: %+v", boxes)
	}

	// Test fetchBoxRuntime
	runtime, err := fetchBoxRuntime("test-token", "proj-1", "box-123")
	if err != nil {
		t.Fatalf("fetchBoxRuntime failed: %v", err)
	}
	if runtime.Status != "running" || runtime.VCPUs != 2 {
		t.Fatalf("unexpected runtime: %+v", runtime)
	}

	// Test controlProjectBox
	err = controlProjectBox("test-token", "proj-1", "box-123", "shutdown")
	if err != nil {
		t.Fatalf("controlProjectBox failed: %v", err)
	}

	// Test resizeProjectBox
	err = resizeProjectBox("test-token", "proj-1", "box-123", BoxResizeInput{VCPUs: 4})
	if err != nil {
		t.Fatalf("resizeProjectBox failed: %v", err)
	}

	// Test removeProjectBox
	err = removeProjectBox("test-token", "proj-1", "box-123")
	if err != nil {
		t.Fatalf("removeProjectBox failed: %v", err)
	}

	// Test fetchProjectBoxImages
	images, err := fetchProjectBoxImages("test-token", "proj-1")
	if err != nil {
		t.Fatalf("fetchProjectBoxImages failed: %v", err)
	}
	if len(images) != 1 || images[0].ID != "ubuntu-22.04" {
		t.Fatalf("unexpected images: %+v", images)
	}

	// Test createProjectBox
	created, err := createProjectBox("test-token", "proj-1", BoxCreateInput{
		Name:    "new-vm",
		ImageID: "ubuntu-22.04",
	})
	if err != nil {
		t.Fatalf("createProjectBox failed: %v", err)
	}
	if created.ID != "box-999" || created.Name != "new-vm" {
		t.Fatalf("unexpected created box: %+v", created)
	}
}

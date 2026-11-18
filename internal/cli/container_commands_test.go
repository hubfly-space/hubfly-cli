package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseContainerListOptions(t *testing.T) {
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
			args:        []string{"--project", "my-project", "--org", "my-org"},
			wantProject: "my-project",
			wantOrg:     "my-org",
		},
		{
			name:        "positional project",
			args:        []string{"my-project"},
			wantProject: "my-project",
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
			opts, err := parseContainerListOptions(tt.args)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseContainerListOptions(%v) error = %v, wantErr %v", tt.args, err, tt.wantErr)
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

func TestContainerCommandRoutingValidation(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "inspect missing arg", args: []string{"container", "inspect"}, wantErr: "usage: hubfly container inspect"},
		{name: "start missing arg", args: []string{"container", "start"}, wantErr: "usage: hubfly container start"},
		{name: "stop missing arg", args: []string{"container", "stop"}, wantErr: "usage: hubfly container stop"},
		{name: "restart missing arg", args: []string{"container", "restart"}, wantErr: "usage: hubfly container restart"},
		{name: "delete missing arg", args: []string{"container", "delete"}, wantErr: "usage: hubfly container delete"},
		{name: "logs missing arg", args: []string{"container", "logs"}, wantErr: "usage: hubfly container logs"},
		{name: "unknown subcommand", args: []string{"container", "unknown-cmd"}, wantErr: "unknown container command"},
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

func TestContainerAPIOperations(t *testing.T) {
	originalAPIHost := apiHost
	t.Cleanup(func() { apiHost = originalAPIHost })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/control"):
			body, _ := io.ReadAll(r.Body)
			var payload map[string]string
			_ = json.Unmarshal(body, &payload)
			if payload["action"] != "restart" {
				http.Error(w, "invalid action", http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"success": true}`))

		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/containers/cont-123"):
			_, _ = w.Write([]byte(`{
				"id": "cont-123",
				"name": "web-frontend",
				"status": "running",
				"tier": "starter",
				"kind": "service",
				"resources": {"cpu": 1.0, "ram": 512, "storage": 10},
				"source": {"type": "docker", "dockerImage": "nginx:alpine"},
				"networking": {"ports": [{"container": 80, "protocol": "tcp", "tunnelUrl": "https://web.hubfly.space"}]}
			}`))

		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/containers"):
			_, _ = w.Write([]byte(`{
				"containers": [
					{"id": "cont-123", "name": "web-frontend", "status": "running"}
				]
			}`))

		case (r.Method == "POST" || r.Method == "DELETE") && strings.HasSuffix(r.URL.Path, "/containers/cont-123/remove"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"success": true}`))

		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	apiHost = server.URL

	// Test controlProjectContainer
	err := controlProjectContainer("test-token", "proj-1", "cont-123", "restart")
	if err != nil {
		t.Fatalf("controlProjectContainer failed: %v", err)
	}

	// Test fetchContainerDetails
	details, err := fetchContainerDetails("test-token", "proj-1", "cont-123")
	if err != nil {
		t.Fatalf("fetchContainerDetails failed: %v", err)
	}
	if details.ID != "cont-123" || details.Name != "web-frontend" || details.Status != "running" {
		t.Fatalf("unexpected container details: %+v", details)
	}
	if len(details.Networking.Ports) != 1 || details.Networking.Ports[0].Container != 80 {
		t.Fatalf("unexpected ports in container details: %+v", details.Networking.Ports)
	}

	// Test fetchProjectContainers
	containers, err := fetchProjectContainers("test-token", "proj-1")
	if err != nil {
		t.Fatalf("fetchProjectContainers failed: %v", err)
	}
	if len(containers) != 1 || containers[0].ID != "cont-123" {
		t.Fatalf("unexpected containers list: %+v", containers)
	}

	// Test removeProjectContainer
	err = removeProjectContainer("test-token", "proj-1", "cont-123")
	if err != nil {
		t.Fatalf("removeProjectContainer failed: %v", err)
	}
}

func TestContainerRuntimeErrorMapping(t *testing.T) {
	tests := []struct {
		name      string
		err       *apiError
		wantExit  int
		wantText  string
		wantRetry bool
		wantWait  time.Duration
	}{
		{
			name:     "stopped",
			err:      &apiError{Status: 409, Code: "CONTAINER_NOT_RUNNING", Details: map[string]any{"reason": "not_running"}},
			wantExit: exitContainerNotRunning,
			wantText: "container web is stopped",
		},
		{
			name:     "runtime missing",
			err:      &apiError{Status: 409, Code: "CONTAINER_NOT_RUNNING", Details: map[string]any{"reason": "runtime_missing"}},
			wantExit: exitContainerNotRunning,
			wantText: "has no runtime in its region",
		},
		{
			name:     "sleeping",
			err:      &apiError{Status: 409, Code: "CONTAINER_SLEEPING"},
			wantExit: exitContainerNotRunning,
			wantText: "wakes on the next request",
		},
		{
			name:      "region unavailable honors retryAfterSeconds",
			err:       &apiError{Status: 503, Code: "REGION_UNAVAILABLE", Details: map[string]any{"retryAfterSeconds": float64(7)}},
			wantExit:  exitContainerRegionDown,
			wantText:  "try again in 7s",
			wantRetry: true,
			wantWait:  7 * time.Second,
		},
		{
			name:      "region unavailable default wait",
			err:       &apiError{Status: 503, Code: "REGION_UNAVAILABLE"},
			wantExit:  exitContainerRegionDown,
			wantText:  "temporarily unavailable",
			wantRetry: true,
			wantWait:  defaultRegionRetryAfter,
		},
		{
			name:      "upstream timeout",
			err:       &apiError{Status: 504, Code: "UPSTREAM_TIMEOUT"},
			wantExit:  6,
			wantText:  "did not answer the logs request in time",
			wantRetry: true,
			wantWait:  5 * time.Second,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := friendlyContainerRuntimeError(fmt.Errorf("wrapped: %w", tt.err), "web", "logs")
			if !strings.Contains(err.Error(), tt.wantText) {
				t.Fatalf("message %q does not contain %q", err.Error(), tt.wantText)
			}
			if got := exitCodeForError(err); got != tt.wantExit {
				t.Fatalf("exit code = %d, want %d", got, tt.wantExit)
			}
			var apiErr *apiError
			if !errors.As(err, &apiErr) {
				t.Fatal("friendly error must unwrap to *apiError")
			}
			wait, retry := containerRuntimeRetryWait(err)
			if retry != tt.wantRetry || wait != tt.wantWait {
				t.Fatalf("retry = %v/%s, want %v/%s", retry, wait, tt.wantRetry, tt.wantWait)
			}
		})
	}

	other := &apiError{Status: 500, Code: "INTERNAL_ERROR", Message: "boom"}
	if got := friendlyContainerRuntimeError(other, "web", "logs"); got != error(other) {
		t.Fatalf("unrelated errors must pass through, got %v", got)
	}
	if _, retry := containerRuntimeRetryWait(other); retry {
		t.Fatal("unrelated errors must not be retried")
	}
}

func TestContainerRuntimeStatusLabel(t *testing.T) {
	region := friendlyContainerRuntimeError(&apiError{Status: 503, Code: "REGION_UNAVAILABLE", Details: map[string]any{"retryAfterSeconds": float64(20)}}, "web", "stats").(*containerRuntimeError)
	if got := containerRuntimeStatusLabel(region, "running"); got != "region unavailable (retry 20s)" {
		t.Fatalf("label = %q", got)
	}
	missing := friendlyContainerRuntimeError(&apiError{Status: 409, Code: "CONTAINER_NOT_RUNNING", Details: map[string]any{"reason": "runtime_missing"}}, "web", "stats").(*containerRuntimeError)
	if got := containerRuntimeStatusLabel(missing, "running"); got != "no runtime" {
		t.Fatalf("label = %q", got)
	}
}

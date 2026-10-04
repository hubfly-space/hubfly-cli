package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEnvKeyValidation(t *testing.T) {
	tests := []struct {
		key   string
		valid bool
	}{
		{"PORT", true},
		{"NODE_ENV", true},
		{"DATABASE_URL_2", true},
		{"_HIDDEN", true},
		{"foo_bar", true},
		{"", false},
		{"123BAD", false},
		{"INVALID-DASH", false},
		{"SPACE VAR", false},
		{"FOO=BAR", false},
		{"KEY.NAME", false},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			got := envKeyRegex.MatchString(tt.key)
			if got != tt.valid {
				t.Errorf("envKeyRegex.MatchString(%q) = %v, want %v", tt.key, got, tt.valid)
			}
		})
	}
}

func TestEnvCommandRoutingValidation(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "unknown subcommand", args: []string{"env", "invalidcmd"}, wantErr: "unknown env command"},
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

func TestEnvAPIOperations(t *testing.T) {
	originalAPIHost := apiHost
	t.Cleanup(func() { apiHost = originalAPIHost })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/api/v1/projects/proj-1/env"):
			json.NewEncoder(w).Encode([]ProjectEnvVar{
				{ID: "e-1", Key: "PORT", Value: "8080", IsSecret: false},
				{ID: "e-2", Key: "DB_PASS", Value: "supersecret", IsSecret: true},
			})
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/api/v1/projects/proj-1/env/update"):
			body, _ := io.ReadAll(r.Body)
			var input struct {
				EnvVars []ProjectEnvVar `json:"envVars"`
			}
			if err := json.Unmarshal(body, &input); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			json.NewEncoder(w).Encode(ProjectEnvUpdateResult{
				Success:    true,
				Count:      len(input.EnvVars),
				Redeployed: 1,
			})
		default:
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
		}
	}))
	defer server.Close()

	apiHost = server.URL

	// 1. Fetch env vars
	envVars, err := fetchProjectEnv("test-token", "proj-1")
	if err != nil {
		t.Fatalf("fetchProjectEnv failed: %v", err)
	}
	if len(envVars) != 2 {
		t.Fatalf("expected 2 env vars, got %d", len(envVars))
	}
	if envVars[0].Key != "PORT" || envVars[1].Key != "DB_PASS" || !envVars[1].IsSecret {
		t.Fatalf("unexpected env vars: %+v", envVars)
	}

	// 2. Update env vars
	toUpdate := []ProjectEnvVar{
		{Key: "PORT", Value: "9000", IsSecret: false},
		{Key: "NEW_KEY", Value: "val", IsSecret: true},
	}
	result, err := updateProjectEnv("test-token", "proj-1", toUpdate)
	if err != nil {
		t.Fatalf("updateProjectEnv failed: %v", err)
	}
	if !result.Success || result.Count != 2 || result.Redeployed != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
}

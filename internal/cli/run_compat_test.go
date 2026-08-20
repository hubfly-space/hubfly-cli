package cli

import (
	"strings"
	"testing"
)

func TestHubflyTokenEnvironmentVariableTakesPrecedence(t *testing.T) {
	t.Setenv("HUBFLY_TOKEN", "hf_test_from_environment")
	token, err := getToken()
	if err != nil {
		t.Fatal(err)
	}
	if token != "hf_test_from_environment" {
		t.Fatalf("getToken() = %q, want HUBFLY_TOKEN", token)
	}
}

func TestDocumentedCommandEntrypointsRemainRegistered(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "build", args: []string{"build"}, wantErr: "hubfly build"},
		{name: "stack", args: []string{"stack"}, wantErr: "hubfly stack"},
		{name: "exec", args: []string{"exec", "api"}, wantErr: "hubfly exec"},
		{name: "projects", args: []string{"projects", "--invalid"}, wantErr: "hubfly projects"},
		{name: "deploy", args: []string{"deploy", "--mode", "invalid"}, wantErr: "invalid deploy mode"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := run(tt.args)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("run(%q) error = %v, want substring %q", tt.args, err, tt.wantErr)
			}
			if strings.Contains(err.Error(), "unknown command") {
				t.Fatalf("run(%q) was rejected as an unknown command: %v", tt.args, err)
			}
		})
	}
}

func TestDocumentedProjectOrganizationFilter(t *testing.T) {
	for _, tt := range []struct {
		args []string
		want string
	}{
		{args: nil, want: ""},
		{args: []string{"--org", "team-slug"}, want: "team-slug"},
	} {
		got, err := parseOrgFilter(tt.args)
		if err != nil {
			t.Fatalf("parseOrgFilter(%q): %v", tt.args, err)
		}
		if got != tt.want {
			t.Fatalf("parseOrgFilter(%q) = %q, want %q", tt.args, got, tt.want)
		}
	}
}

func TestDeployDefaultsToDocumentedSmartApply(t *testing.T) {
	opts, err := parseDeployOptions([]string{"--yes"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Mode != "smart" || !opts.ModeExplicit {
		t.Fatalf("deploy defaults = mode %q explicit=%v, want smart/true", opts.Mode, opts.ModeExplicit)
	}
}

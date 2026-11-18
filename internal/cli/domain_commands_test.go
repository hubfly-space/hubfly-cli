package cli

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func domainTestList() []map[string]any {
	return []map[string]any{
		{
			"id": "dom_1", "domainName": "app.example.com", "status": "pending",
			"verificationFqdn": "_hubfly-verification.app.example.com", "verificationToken": "hubfly-verify=abc",
			"createdAt": "2026-11-11T08:00:00Z", "verifiedAt": nil, "lastError": nil,
			"containerId": nil, "assignedContainerName": nil, "assignedLoadBalancerId": nil, "assignedLoadBalancerName": nil,
		},
		{
			"id": "dom_2", "domainName": "www.example.org", "status": "verified",
			"verificationFqdn": nil, "verificationToken": nil,
			"createdAt": "2026-11-10T08:00:00Z", "verifiedAt": "2026-11-10T09:00:00Z", "lastError": nil,
			"containerId": "c1", "assignedContainerName": "web", "assignedLoadBalancerId": nil, "assignedLoadBalancerName": nil,
		},
	}
}

func setupDomainTest(t *testing.T) rtTestEnv {
	t.Helper()
	env := setupRealtimeTest(t)
	env.dash.ok("GET", "/api/v1/domains", domainTestList())
	return env
}

func TestDomainsListTableAndJSON(t *testing.T) {
	env := setupDomainTest(t)
	mustRun(t, "domains", "list")
	for _, part := range []string{"app.example.com", "pending", "container web"} {
		if !strings.Contains(env.out.String(), part) {
			t.Fatalf("list output missing %q:\n%s", part, env.out.String())
		}
	}
	env.out = &rtSyncBuffer{}
	rtOut = env.out
	mustRun(t, "domains", "list", "--json")
	var decoded []map[string]any
	if err := json.Unmarshal([]byte(env.out.String()), &decoded); err != nil || len(decoded) != 2 {
		t.Fatalf("--json = %v\n%s", err, env.out.String())
	}
}

func TestDomainsAddPrintsTXTRecordAndWaits(t *testing.T) {
	env := setupDomainTest(t)
	env.dash.ok("POST", "/api/v1/domains/create", map[string]any{
		"domainId": "dom_3", "domainName": "shop.example.com",
		"verificationHost": "_hubfly-verification.shop.example.com", "verificationToken": "hubfly-verify=xyz",
	})
	calls := 0
	env.dash.on("POST", "/api/v1/domains/dom_3/verify", func(rtRecorded) (int, any) {
		calls++
		if calls < 3 {
			return http.StatusOK, map[string]any{"success": false, "message": "Verification failed."}
		}
		return http.StatusOK, map[string]any{"success": true, "message": "Domain verified"}
	})
	mustRun(t, "domains", "add", "Shop.Example.com.", "--wait")
	if got := env.dash.find("POST", "/api/v1/domains/create"); len(got) != 1 || got[0].Body["domainName"] != "shop.example.com" {
		t.Fatalf("create body = %+v", got)
	}
	for _, part := range []string{"TXT", "_hubfly-verification.shop.example.com", "hubfly-verify=xyz"} {
		if !strings.Contains(env.out.String(), part) {
			t.Fatalf("add output missing %q:\n%s", part, env.out.String())
		}
	}
	if calls != 3 {
		t.Fatalf("verify calls = %d, want 3", calls)
	}
}

func TestDomainsAddRejectsURL(t *testing.T) {
	setupDomainTest(t)
	err := runErr("domains", "add", "https://app.example.com/x")
	if err == nil || !strings.Contains(err.Error(), "app.example.com") || !strings.Contains(err.Error(), "invalid domain") {
		t.Fatalf("err = %v", err)
	}
}

func TestDomainsVerifyFailureShowsRecordAndHint(t *testing.T) {
	env := setupDomainTest(t)
	env.dash.ok("POST", "/api/v1/domains/dom_1/verify", map[string]any{"success": false, "message": "Verification failed."})
	err := runErr("domains", "verify", "app.example.com")
	if err == nil || !strings.Contains(err.Error(), "--wait") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(env.errOut.String(), "hubfly-verify=abc") {
		t.Fatalf("stderr missing record:\n%s", env.errOut.String())
	}
}

func TestDomainsRemoveNeedsYesAndSuggestsTypos(t *testing.T) {
	env := setupDomainTest(t)
	env.dash.ok("POST", "/api/v1/domains/dom_2/remove", map[string]any{"success": true})
	if err := runErr("domains", "remove", "www.example.org"); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("noninteractive remove without --yes: %v", err)
	}
	if err := runErr("domains", "remove", "www.exampel.org", "--yes"); err == nil || !strings.Contains(err.Error(), "Did you mean 'www.example.org'") {
		t.Fatalf("typo err = %v", err)
	}
	mustRun(t, "domains", "remove", "www.example.org", "--yes")
	if len(env.dash.find("POST", "/api/v1/domains/dom_2/remove")) != 1 {
		t.Fatal("remove was not called")
	}
}

func TestDomainUnknownCommandSuggestions(t *testing.T) {
	setupDomainTest(t)
	if err := runErr("domains", "lsit"); err == nil || !strings.Contains(err.Error(), "hubfly domains list") {
		t.Fatalf("err = %v", err)
	}
	if err := runErr("deplyo"); err == nil || !strings.Contains(err.Error(), "Did you mean: hubfly deploy?") {
		t.Fatalf("err = %v", err)
	}
	if exitCodeForError(errors.New("unknown command: hubfly x")) != 2 {
		t.Fatal("unknown command must exit 2")
	}
}

func TestRemoteExitStatusPropagates(t *testing.T) {
	if remoteExitStatus(0) != nil {
		t.Fatal("exit 0 must be nil")
	}
	err := remoteExitStatus(42)
	var remote *remoteExitError
	if !errors.As(err, &remote) || remote.code != 42 || exitCodeForError(err) != 42 {
		t.Fatalf("remoteExitStatus(42) = %v", err)
	}
	if remoteExitStatus(-1).(*remoteExitError).code != 1 {
		t.Fatal("out-of-range codes map to 1")
	}
}

// ---------------------------------------------------------------------------
// Region migration watch / retry
// ---------------------------------------------------------------------------

func migTestView(status string, progress float64) map[string]any {
	view := map[string]any{
		"id": "pmig_1", "projectName": "web", "status": status, "mode": "warm", "kind": "migration",
		"source": map[string]any{"id": "r-eu", "name": "Europe"}, "target": map[string]any{"id": "r-us", "name": "US East"},
		"bytesTotal": 1 << 30, "bytesDone": int64(float64(1<<30) * progress),
		"decisionPending": false, "canCancel": true, "job": nil, "failureMessage": nil,
		"overallProgress": progress, "overallEtaSeconds": 120,
		"currentStep": map[string]any{"key": "copy", "label": "Copying volumes", "phase": "forward", "detail": "pass 2", "retries": 1, "remainingSeconds": 60, "stalled": false},
	}
	if status == "failed" {
		view["failureMessage"] = "The target region ran out of capacity."
		view["failureCode"] = "MIGRATION_TARGET_CAPACITY"
		view["failedStep"] = map[string]any{"key": "provision", "label": "Provision target"}
		view["failureFix"] = "Try again in a few minutes."
		view["retryable"] = true
		view["outcome"] = "stayed_in_source"
		view["currentStep"] = nil
	}
	return view
}

func TestProjectMigrationWatchSurvivesDropsAndShowsProgress(t *testing.T) {
	env := setupProjectMovesTest(t)
	calls := 0
	env.dash.on("GET", pmTestProject+"/migration", func(rtRecorded) (int, any) {
		calls++
		switch calls {
		case 1, 2:
			return http.StatusOK, migTestView("copying", 0.4)
		case 3:
			return http.StatusBadGateway, map[string]any{"ok": false, "error": map[string]any{"code": "UPSTREAM", "message": "bad gateway"}}
		case 4:
			return http.StatusOK, migTestView("copying", 0.7)
		}
		return http.StatusOK, migTestView("completed", 1)
	})
	mustRun(t, "project", "migration", "watch", "web")
	errOut := env.errOut.String()
	for _, part := range []string{"40%", "Copying volumes (pass 2) [retry 1]", "ETA 2m0s", "70%"} {
		if !strings.Contains(errOut, part) {
			t.Fatalf("watch stderr missing %q:\n%s", part, errOut)
		}
	}
	if !strings.Contains(env.out.String(), "completed") || !strings.Contains(env.out.String(), "Europe -> US East") {
		t.Fatalf("final status:\n%s", env.out.String())
	}
}

func TestProjectMigrationFailureExplainsAndRetries(t *testing.T) {
	env := setupProjectMovesTest(t)
	env.dash.ok("GET", pmTestProject+"/migration", migTestView("failed", 0.2))
	err := runErr("project", "migration", "status", "web", "--wait")
	if err == nil || !strings.Contains(err.Error(), "Try again in a few minutes") || !strings.Contains(err.Error(), "migration retry web") {
		t.Fatalf("wait err = %v", err)
	}
	if out := env.out.String(); !strings.Contains(out, "Failed at: Provision target") || !strings.Contains(out, "still runs in Europe") {
		t.Fatalf("status output:\n%s", out)
	}

	preview := map[string]any{"canMigrate": true, "feeMicroUsd": 0, "feeWaived": true, "blockers": []any{}, "warnings": []any{}, "estimates": map[string]any{}, "checklist": []any{}}
	env.dash.ok("GET", pmTestProject+"/migration/preview", preview)
	env.dash.ok("POST", pmTestProject+"/migration/create", migTestView("scheduled", 0))
	mustRun(t, "project", "migration", "retry", "web", "--yes", "--confirm-name", "web")
	created := env.dash.find("POST", pmTestProject+"/migration/create")
	if len(created) != 1 || created[0].Body["targetRegionId"] != "r-us" || created[0].Body["mode"] != "warm" {
		t.Fatalf("retry create = %+v", created)
	}
}

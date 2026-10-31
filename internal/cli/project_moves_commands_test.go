package cli

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

const (
	pmTestToken    = "hub_project_transfer_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef.v1"
	pmTestProject  = "/api/v1/projects/p-web"
	pmTestOfferAPI = "/api/v1/project-transfers/" + pmTestToken
)

func pmTransferView(status string) map[string]any {
	return map[string]any{
		"id": "ptr_1", "projectId": "p-web", "projectName": "web", "projectType": "cell", "status": status,
		"recipientEmail": "bob@example.test", "from": map[string]any{"type": "user", "id": "u1", "name": "Tester"},
		"to": nil, "expiresAt": "2099-01-01T00:00:00Z", "createdAt": "2026-10-26T08:00:00Z",
		"acceptedAt": nil, "completedAt": nil, "handoverAt": nil, "job": nil, "failureCode": nil, "failureMessage": nil,
	}
}

func pmPreview(canTransfer bool) map[string]any {
	blockers := []map[string]any{}
	if !canTransfer {
		blockers = append(blockers, map[string]any{"code": "TRANSFER_WORK_IN_PROGRESS", "severity": "blocker", "message": "A build is running."})
	}
	return map[string]any{
		"projectId": "p-web", "projectName": "web", "projectType": "cell", "canTransfer": canTransfer,
		"blockers":                   blockers,
		"warnings":                   []map[string]any{{"code": "TRANSFER_ENV_VISIBLE", "severity": "warning", "message": "The new owner can read environment values."}},
		"moves":                      []map[string]any{{"kind": "containers", "label": "Containers", "count": 2}, {"kind": "volumes", "label": "Volumes", "count": 0}},
		"revokedIfSenderLosesAccess": []map[string]any{{"kind": "personal_access_tokens", "label": "Project tokens", "count": 1}},
		"envValueCount":              3, "requiresOtp": true,
	}
}

func pmOffer() map[string]any {
	return map[string]any{
		"batchId": "pb_1", "senderName": "Alice", "expiresAt": "2099-01-01T00:00:00Z", "status": "pending",
		"projects": []map[string]any{
			{
				"transferId": "ptr_1", "projectName": "web", "projectType": "cell",
				"hosting": map[string]any{"hostnames": []string{"web.hubfly.app"}, "customDomains": []string{"acme.test"}, "mailDomains": []string{}, "buckets": 0, "containers": 2, "volumes": 1, "boxes": 0},
				"members": []map[string]any{
					{"userId": "u-bob", "name": "Bob", "email": "bob@example.test", "permissions": []string{"project:read"}, "credentials": []string{}},
					{"userId": "u-eve", "name": "Eve", "email": "eve@example.test", "permissions": []string{"project:edit"}, "credentials": []string{"Personal access token"}},
				},
			},
			{
				"transferId": "ptr_2", "projectName": "api", "projectType": "cell",
				"hosting": map[string]any{"hostnames": []string{}, "customDomains": []string{}, "mailDomains": []string{}, "buckets": 0, "containers": 1, "volumes": 0, "boxes": 0},
				"members": []map[string]any{},
			},
		},
		"destinations": []map[string]any{
			{"type": "user", "id": "u1", "name": "Tester", "eligible": true, "reason": nil},
			{"type": "organization", "id": "org_1", "name": "Acme", "eligible": true, "reason": nil},
			{"type": "organization", "id": "org_2", "name": "Viewers Inc", "eligible": false, "reason": "You are a viewer in this organization."},
		},
	}
}

func pmAcceptancePreview(eligible bool) map[string]any {
	blockers := []map[string]any{}
	if !eligible {
		blockers = append(blockers, map[string]any{"code": "TRANSFER_DESTINATION_FUNDS", "severity": "blocker", "message": "Add funds first."})
	}
	return map[string]any{
		"eligible": eligible, "blockers": blockers, "warnings": []map[string]any{},
		"balanceMicroUsd": 25_000_000, "requiredBalanceMicroUsd": 3_600_000, "hourlyBurnMicroUsd": 50_000,
		"billedPlans":      []map[string]any{{"kind": "mail", "label": "Mail domain", "amountMicroUsd": 2_000_000, "period": "month"}},
		"credentialPolicy": "keep", "revoked": []map[string]any{{"kind": "tokens", "label": "Tokens of removed members", "count": 1}},
	}
}

// setupProjectMovesTest is setupRealtimeTest with the Cell project "web" (p-web).
func setupProjectMovesTest(t *testing.T) rtTestEnv {
	t.Helper()
	env := setupRealtimeTest(t)
	env.dash.ok("GET", "/api/v1/projects", map[string]any{
		"items": []map[string]any{{"id": "p-web", "name": "web", "type": "cell"}, {"id": "p-man", "name": "rt-project", "type": "managed"}},
		"page":  1, "pageCount": 1,
	})
	env.dash.ok("GET", pmTestProject+"/transfer/preview", pmPreview(true))
	env.dash.ok("POST", pmTestProject+"/transfer/otp/create", map[string]any{"sentTo": "te***@example.test", "expiresAt": "2099-01-01T00:10:00Z"})
	env.dash.ok("POST", pmTestProject+"/transfer/create", pmTransferView("pending"))
	env.dash.ok("GET", pmTestOfferAPI, pmOffer())
	env.dash.ok("GET", pmTestOfferAPI+"/preview", pmAcceptancePreview(true))
	accepted := pmTransferView("accepted")
	accepted["handoverAt"] = "2026-10-26T10:00:00Z"
	env.dash.ok("POST", pmTestOfferAPI+"/accept", []map[string]any{accepted})
	env.dash.ok("POST", pmTestOfferAPI+"/decline", map[string]any{"declined": true})
	return env
}

func pmAPIError(status int, code, message string) func(rtRecorded) (int, any) {
	return func(rtRecorded) (int, any) {
		return status, map[string]any{"ok": false, "error": map[string]any{"code": code, "message": message}}
	}
}

func TestProjectHelpPrintsUsage(t *testing.T) {
	env := setupProjectMovesTest(t)
	mustRun(t, "project", "help")
	if strings.TrimSpace(env.out.String()) != projectUsageText {
		t.Fatalf("help output differs from projectUsageText:\n%s", env.out.String())
	}
	if err := runErr("project", "nope"); err == nil || exitCodeForError(err) != 2 {
		t.Fatalf("unknown project command error = %v", err)
	}
}

func TestNormalizeProjectTransferToken(t *testing.T) {
	for _, input := range []string{pmTestToken, " " + pmTestToken + "\n", "https://dashboard.hubfly.space/projects/transfers/" + pmTestToken, "https://dashboard.hubfly.space/projects/transfers/" + pmTestToken + "/"} {
		if got, ok := normalizeProjectTransferToken(input); !ok || got != pmTestToken {
			t.Fatalf("normalizeProjectTransferToken(%q) = %q, %v", input, got, ok)
		}
	}
	for _, input := range []string{"", "hub_project_transfer_abc.v1", strings.Replace(pmTestToken, ".v1", ".v2", 1), strings.Replace(pmTestToken, "0123", "ABCD", 1), "hub_project_invite_" + strings.Repeat("a", 64) + ".v1"} {
		if _, ok := normalizeProjectTransferToken(input); ok {
			t.Fatalf("normalizeProjectTransferToken(%q) accepted an invalid token", input)
		}
	}
}

func TestProjectTransferSendsOtpThenCreates(t *testing.T) {
	env := setupProjectMovesTest(t)
	setTestStdin("123456\n")
	mustRun(t, "project", "transfer", "web", "--to", "bob@example.test", "--yes", "--confirm-name", "web", "--rotate-credentials")

	otp := env.dash.find("POST", pmTestProject+"/transfer/otp/create")
	if len(otp) != 1 || otp[0].Body["recipientEmail"] != "bob@example.test" {
		t.Fatalf("otp requests = %+v", otp)
	}
	created := env.dash.find("POST", pmTestProject+"/transfer/create")
	if len(created) != 1 {
		t.Fatalf("create requests = %+v", created)
	}
	want := map[string]any{
		"recipientEmail": "bob@example.test", "confirmName": "web", "otp": "123456",
		"options": map[string]any{"credentialPolicy": "rotate"},
	}
	if !reflect.DeepEqual(created[0].Body, want) {
		t.Fatalf("create body = %#v, want %#v", created[0].Body, want)
	}
	out := env.out.String()
	for _, part := range []string{"Transfer web (cell, p-web) to bob@example.test", "Containers", "Project tokens", "3 environment value", "TRANSFER_ENV_VISIBLE", "Offer sent to bob@example.test"} {
		if !strings.Contains(out, part) {
			t.Fatalf("output missing %q:\n%s", part, out)
		}
	}
	if strings.Contains(out, "Volumes") {
		t.Fatalf("zero counts should be hidden:\n%s", out)
	}
	if !strings.Contains(env.errOut.String(), "te***@example.test") {
		t.Fatalf("stderr does not say where the code went:\n%s", env.errOut.String())
	}
}

func TestProjectTransferAutoPolicyOmitsOptions(t *testing.T) {
	env := setupProjectMovesTest(t)
	setTestStdin("123456\n")
	mustRun(t, "project", "transfer", "p-web", "--to", "bob@example.test", "--yes", "--confirm-name", "web")
	created := env.dash.find("POST", pmTestProject+"/transfer/create")
	if len(created) != 1 {
		t.Fatalf("create requests = %+v", created)
	}
	if _, ok := created[0].Body["options"]; ok {
		t.Fatalf("auto credential policy should omit options: %#v", created[0].Body)
	}
}

func TestProjectTransferJSONKeepsStdoutClean(t *testing.T) {
	env := setupProjectMovesTest(t)
	setTestStdin("123456\n")
	mustRun(t, "project", "transfer", "web", "--to", "bob@example.test", "--yes", "--confirm-name", "web", "--json")
	var view map[string]any
	if err := json.Unmarshal([]byte(env.out.String()), &view); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, env.out.String())
	}
	if view["id"] != "ptr_1" || view["status"] != "pending" {
		t.Fatalf("json = %#v", view)
	}
	if !strings.Contains(env.errOut.String(), "Confirmation code:") || !strings.Contains(env.errOut.String(), "Transfer web") {
		t.Fatalf("prompts and preview should go to stderr:\n%s", env.errOut.String())
	}
}

func TestProjectTransferInteractiveRetriesWrongCode(t *testing.T) {
	env := setupProjectMovesTest(t)
	rtInteractive = func() bool { return true }
	attempts := 0
	env.dash.on("POST", pmTestProject+"/transfer/create", func(request rtRecorded) (int, any) {
		attempts++
		if request.Body["otp"] != "654321" {
			return http.StatusBadRequest, map[string]any{"ok": false, "error": map[string]any{"code": "TRANSFER_OTP_INVALID", "message": "The code is not valid."}}
		}
		return http.StatusOK, pmTransferView("pending")
	})
	setTestStdin("web\n111111\n654321\n")
	mustRun(t, "project", "transfer", "web", "--to", "bob@example.test")
	if attempts != 2 {
		t.Fatalf("create attempts = %d, want 2", attempts)
	}
	if !strings.Contains(env.errOut.String(), "That code is not right") {
		t.Fatalf("stderr = %s", env.errOut.String())
	}
}

func TestProjectTransferTypedNameMismatchSendsNoCode(t *testing.T) {
	env := setupProjectMovesTest(t)
	rtInteractive = func() bool { return true }
	setTestStdin("wrong\n")
	err := runErr("project", "transfer", "web", "--to", "bob@example.test")
	if err == nil || exitCodeForError(err) != 7 {
		t.Fatalf("mismatch error = %v", err)
	}
	if len(env.dash.find("POST", pmTestProject+"/transfer/otp/create")) != 0 {
		t.Fatal("an OTP was requested after a wrong name")
	}
}

func TestProjectTransferValidation(t *testing.T) {
	cases := map[string][]string{
		"missing --to":           {"project", "transfer", "web"},
		"bad email":              {"project", "transfer", "web", "--to", "not-an-email", "--yes", "--confirm-name", "web"},
		"yes without name":       {"project", "transfer", "web", "--to", "bob@example.test", "--yes"},
		"no terminal, no --yes":  {"project", "transfer", "web", "--to", "bob@example.test"},
		"two projects":           {"project", "transfer", "web", "api", "--to", "bob@example.test", "--yes", "--confirm-name", "web"},
		"accept bad token":       {"project", "transfers", "accept", "nope", "--yes", "--personal"},
		"accept both dests":      {"project", "transfers", "accept", pmTestToken, "--yes", "--personal", "--org", "org_1"},
		"accept without --yes":   {"project", "transfers", "accept", pmTestToken, "--personal"},
		"decline without --yes":  {"project", "transfers", "decline", pmTestToken},
		"cancel without project": {"project", "transfers", "cancel", "--yes"},
		"history extra argument": {"project", "history", "web", "api"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			env := setupProjectMovesTest(t)
			err := runErr(args...)
			if err == nil || exitCodeForError(err) != 2 {
				t.Fatalf("run(%q) error = %v (exit %d), want a usage error", args, err, exitCodeForError(err))
			}
			if raw := env.dash.allRaw(); raw != "" {
				t.Fatalf("validation should happen before any request, got:\n%s", raw)
			}
		})
	}
}

func TestProjectTransferConfirmNameMismatch(t *testing.T) {
	env := setupProjectMovesTest(t)
	err := runErr("project", "transfer", "web", "--to", "bob@example.test", "--yes", "--confirm-name", "api")
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("error = %v", err)
	}
	if len(env.dash.find("POST", pmTestProject+"/transfer/otp/create")) != 0 {
		t.Fatal("an OTP was requested after a wrong --confirm-name")
	}
}

func TestProjectTransferBlockedStopsBeforeOtp(t *testing.T) {
	env := setupProjectMovesTest(t)
	env.dash.ok("GET", pmTestProject+"/transfer/preview", pmPreview(false))
	err := runErr("project", "transfer", "web", "--to", "bob@example.test", "--yes", "--confirm-name", "web")
	if err == nil || !strings.Contains(err.Error(), "A build is running.") || !strings.Contains(err.Error(), "TRANSFER_WORK_IN_PROGRESS") {
		t.Fatalf("error = %v", err)
	}
	if len(env.dash.find("POST", pmTestProject+"/transfer/otp/create")) != 0 {
		t.Fatal("an OTP was requested for a blocked project")
	}
}

func TestProjectTransferSessionRequiredPointsToDashboard(t *testing.T) {
	env := setupProjectMovesTest(t)
	env.dash.on("POST", pmTestProject+"/transfer/otp/create", pmAPIError(http.StatusForbidden, "MOVE_SESSION_REQUIRED", "Sign in to the dashboard to transfer a project."))
	err := runErr("project", "transfer", "web", "--to", "bob@example.test", "--yes", "--confirm-name", "web")
	if err == nil || exitCodeForError(err) != 3 {
		t.Fatalf("error = %v (exit %d)", err, exitCodeForError(err))
	}
	if !strings.Contains(err.Error(), "https://dashboard.hubfly.space/projects/p-web") {
		t.Fatalf("error lacks the dashboard link: %v", err)
	}
	if len(env.dash.find("POST", pmTestProject+"/transfer/create")) != 0 {
		t.Fatal("create was called after the OTP was refused")
	}
}

func TestProjectTransfersList(t *testing.T) {
	env := setupProjectMovesTest(t)
	outgoing := pmTransferView("accepted")
	outgoing["job"] = map[string]any{"id": "pj_1", "kind": "project_transfer", "status": "waiting", "phase": "forward", "step": "settle", "stepLabel": "Settling billing", "stepIndex": 1, "stepCount": 8}
	data := map[string]any{
		"incoming": []map[string]any{{"token": nil, "transferId": "ptr_9", "batchId": nil, "projectName": "shop", "projectType": "cell", "senderName": "Alice", "expiresAt": "2099-01-01T00:00:00Z", "status": "pending"}},
		"outgoing": []map[string]any{outgoing},
	}
	env.dash.ok("GET", "/api/v1/project-transfers", data)
	mustRun(t, "project", "transfers")
	out := env.out.String()
	for _, part := range []string{"Offered to you:", "shop", "Alice", "ptr_9", "Started by you:", "bob@example.test", "Settling billing (2/8)"} {
		if !strings.Contains(out, part) {
			t.Fatalf("list output missing %q:\n%s", part, out)
		}
	}

	env.out = &rtSyncBuffer{}
	rtOut = env.out
	mustRun(t, "project", "transfers", "list", "--json")
	var decoded map[string]any
	if err := json.Unmarshal([]byte(env.out.String()), &decoded); err != nil {
		t.Fatalf("--json output: %v\n%s", err, env.out.String())
	}
	if incoming := decoded["incoming"].([]any); len(incoming) != 1 {
		t.Fatalf("incoming = %#v", incoming)
	}
}

func TestProjectTransferStatus(t *testing.T) {
	env := setupProjectMovesTest(t)
	env.dash.ok("GET", pmTestProject+"/transfer", nil)
	mustRun(t, "project", "transfers", "status", "web")
	if !strings.Contains(env.out.String(), "has no transfers") {
		t.Fatalf("output = %s", env.out.String())
	}

	failed := pmTransferView("failed")
	failed["failureCode"] = "TRANSFER_SOURCE_DEFICIT"
	failed["failureMessage"] = "The sender's balance ran out."
	env.dash.ok("GET", pmTestProject+"/transfer", failed)
	env.out = &rtSyncBuffer{}
	rtOut = env.out
	mustRun(t, "project", "transfers", "status", "web")
	for _, part := range []string{"ptr_1", "failed", "The sender's balance ran out. (TRANSFER_SOURCE_DEFICIT)"} {
		if !strings.Contains(env.out.String(), part) {
			t.Fatalf("status output missing %q:\n%s", part, env.out.String())
		}
	}
}

func TestProjectTransferShow(t *testing.T) {
	env := setupProjectMovesTest(t)
	mustRun(t, "project", "transfers", "show", "https://dashboard.hubfly.space/projects/transfers/"+pmTestToken)
	out := env.out.String()
	for _, part := range []string{"Alice offers you 2 project(s)", "web.hubfly.app", "acme.test", "Eve <eve@example.test> u-eve - credentials: Personal access token", "Acme (organization, --org org_1)", "not eligible: You are a viewer"} {
		if !strings.Contains(out, part) {
			t.Fatalf("show output missing %q:\n%s", part, out)
		}
	}
}

func TestProjectTransferAcceptIntoOrganization(t *testing.T) {
	env := setupProjectMovesTest(t)
	mustRun(t, "project", "transfers", "accept", pmTestToken, "--org", "Acme", "--only", "web", "--keep-member", "eve@example.test", "--keep-member", "u-bob", "--yes")

	previews := env.dash.find("GET", pmTestOfferAPI+"/preview")
	if len(previews) != 1 {
		t.Fatalf("preview requests = %+v", previews)
	}
	query := previews[0].Query
	if query.Get("destinationType") != "organization" || query.Get("destinationId") != "org_1" || !reflect.DeepEqual(query["transferIds"], []string{"ptr_1"}) {
		t.Fatalf("preview query = %v", query)
	}
	accepts := env.dash.find("POST", pmTestOfferAPI+"/accept")
	if len(accepts) != 1 {
		t.Fatalf("accept requests = %+v", accepts)
	}
	want := map[string]any{
		"destinationType": "organization", "destinationId": "org_1", "transferIds": []any{"ptr_1"},
		"keepMembers": []any{map[string]any{"transferId": "ptr_1", "userIds": []any{"u-bob", "u-eve"}}},
	}
	if !reflect.DeepEqual(accepts[0].Body, want) {
		t.Fatalf("accept body = %#v, want %#v", accepts[0].Body, want)
	}
	out := env.out.String()
	for _, part := range []string{"$25.00 (needs $3.60)", "$0.05/hour", "Mail domain $2.00/month", "Accepted 1 project(s).", "web: the owner changes"} {
		if !strings.Contains(out, part) {
			t.Fatalf("accept output missing %q:\n%s", part, out)
		}
	}
}

func TestProjectTransferAcceptPersonalWholeOffer(t *testing.T) {
	env := setupProjectMovesTest(t)
	mustRun(t, "project", "transfers", "accept", pmTestToken, "--personal", "--yes", "--json")
	accepts := env.dash.find("POST", pmTestOfferAPI+"/accept")
	if len(accepts) != 1 {
		t.Fatalf("accept requests = %+v", accepts)
	}
	if want := map[string]any{"destinationType": "user", "destinationId": "u1"}; !reflect.DeepEqual(accepts[0].Body, want) {
		t.Fatalf("accept body = %#v, want %#v", accepts[0].Body, want)
	}
	if previews := env.dash.find("GET", pmTestOfferAPI+"/preview"); len(previews) != 1 || previews[0].Query.Has("transferIds") {
		t.Fatalf("whole-offer preview should not name transfers: %+v", previews)
	}
	var views []map[string]any
	if err := json.Unmarshal([]byte(env.out.String()), &views); err != nil || len(views) != 1 {
		t.Fatalf("--json output = %v\n%s", err, env.out.String())
	}
}

func TestProjectTransferAcceptRefusals(t *testing.T) {
	t.Run("ineligible organization", func(t *testing.T) {
		env := setupProjectMovesTest(t)
		err := runErr("project", "transfers", "accept", pmTestToken, "--org", "org_2", "--yes")
		if err == nil || !strings.Contains(err.Error(), "You are a viewer") {
			t.Fatalf("error = %v", err)
		}
		if len(env.dash.find("GET", pmTestOfferAPI+"/preview")) != 0 {
			t.Fatal("preview called for an ineligible destination")
		}
	})
	t.Run("unknown member", func(t *testing.T) {
		env := setupProjectMovesTest(t)
		err := runErr("project", "transfers", "accept", pmTestToken, "--personal", "--keep-member", "zed@example.test", "--yes")
		if err == nil || exitCodeForError(err) != 2 {
			t.Fatalf("error = %v", err)
		}
		if len(env.dash.find("POST", pmTestOfferAPI+"/accept")) != 0 {
			t.Fatal("accept called with an unknown member")
		}
	})
	t.Run("blocked preview", func(t *testing.T) {
		env := setupProjectMovesTest(t)
		env.dash.ok("GET", pmTestOfferAPI+"/preview", pmAcceptancePreview(false))
		err := runErr("project", "transfers", "accept", pmTestToken, "--personal", "--yes")
		if err == nil || !strings.Contains(err.Error(), "Add funds first.") {
			t.Fatalf("error = %v", err)
		}
		if len(env.dash.find("POST", pmTestOfferAPI+"/accept")) != 0 {
			t.Fatal("accept called despite blockers")
		}
	})
	t.Run("no destination without a terminal", func(t *testing.T) {
		setupProjectMovesTest(t)
		err := runErr("project", "transfers", "accept", pmTestToken, "--yes")
		if err == nil || !strings.Contains(err.Error(), "--personal or --org") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("offer not found", func(t *testing.T) {
		env := setupProjectMovesTest(t)
		env.dash.on("GET", pmTestOfferAPI, pmAPIError(http.StatusNotFound, "TRANSFER_NOT_FOUND", "Transfer not found"))
		err := runErr("project", "transfers", "accept", pmTestToken, "--personal", "--yes")
		if err == nil || exitCodeForError(err) != 5 || !strings.Contains(err.Error(), "hubfly whoami") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestProjectTransferAcceptInteractivePick(t *testing.T) {
	env := setupProjectMovesTest(t)
	rtInteractive = func() bool { return true }
	setTestStdin("9\n2\ny\n")
	mustRun(t, "project", "transfers", "accept", pmTestToken)
	accepts := env.dash.find("POST", pmTestOfferAPI+"/accept")
	if len(accepts) != 1 || accepts[0].Body["destinationId"] != "org_1" {
		t.Fatalf("accept requests = %+v", accepts)
	}
	if !strings.Contains(env.out.String(), "Please enter one of the numbers above.") {
		t.Fatalf("out = %s", env.out.String())
	}
}

func TestProjectTransferDeclineAndCancel(t *testing.T) {
	env := setupProjectMovesTest(t)
	mustRun(t, "project", "transfers", "decline", pmTestToken, "--yes")
	if len(env.dash.find("POST", pmTestOfferAPI+"/decline")) != 1 {
		t.Fatal("decline not sent")
	}

	rollingBack := pmTransferView("accepted")
	rollingBack["job"] = map[string]any{"id": "pj_1", "kind": "project_transfer", "status": "rolling_back", "phase": "compensation", "step": "unlock", "stepLabel": "Resuming changes", "stepIndex": 0, "stepCount": 1}
	env.dash.ok("POST", pmTestProject+"/transfer/cancel", rollingBack)
	mustRun(t, "project", "transfers", "cancel", "web", "--yes")
	if len(env.dash.find("POST", pmTestProject+"/transfer/cancel")) != 1 {
		t.Fatal("cancel not sent")
	}
	if out := env.out.String(); !strings.Contains(out, "Offer declined") || !strings.Contains(out, "rolling back: Resuming changes (1/1)") {
		t.Fatalf("output = %s", out)
	}
}

func TestProjectHistory(t *testing.T) {
	env := setupProjectMovesTest(t)
	env.dash.ok("GET", pmTestProject+"/history", map[string]any{"entries": []map[string]any{
		{"id": "ptr_1", "kind": "transfer", "status": "completed", "title": "Transferred to Acme", "detail": "From Tester", "actor": "Tester", "at": "2026-10-20T10:00:00Z", "href": nil},
	}})
	mustRun(t, "project", "history", "web")
	if out := env.out.String(); !strings.Contains(out, "Transferred to Acme: From Tester") || !strings.Contains(out, "completed") {
		t.Fatalf("history output = %s", out)
	}

	env.out = &rtSyncBuffer{}
	rtOut = env.out
	mustRun(t, "project", "history", "p-web", "--json")
	var decoded struct {
		Entries []map[string]any `json:"entries"`
	}
	if err := json.Unmarshal([]byte(env.out.String()), &decoded); err != nil || len(decoded.Entries) != 1 {
		t.Fatalf("--json output = %v\n%s", err, env.out.String())
	}
}

func TestResolveMoveProjectAmbiguousName(t *testing.T) {
	env := setupProjectMovesTest(t)
	env.dash.ok("GET", "/api/v1/projects", map[string]any{
		"items": []map[string]any{{"id": "p-1", "name": "web"}, {"id": "p-2", "name": "Web"}},
		"page":  1, "pageCount": 1,
	})
	err := runErr("project", "history", "web")
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("error = %v", err)
	}
	if err := runErr("project", "history", "missing"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing project error = %v", err)
	}
}

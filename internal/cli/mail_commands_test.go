package cli

import (
	"net/http"
	"strings"
	"testing"
)

func installMailFixtures(f *rtFakeDashboard) {
	f.ok("GET", "/api/v1/projects", map[string]any{
		"items": []map[string]any{
			{"id": "p-mail", "name": "mail-main", "type": "mail"},
			{"id": "p-web", "name": "web", "type": "cell"},
		},
		"page": 1, "pageCount": 1,
	})
	f.ok("GET", "/api/v1/projects/p-mail/mail/domains", []map[string]any{{
		"id": "md_1", "domain": "example.com", "state": "verified", "mailState": "active",
		"verifiedAt": "2026-11-01T10:00:00Z", "chargeMicroUsd": "0",
		"dnsRecords": map[string]any{"mx": "mx.hubfly.test", "spf": "v=spf1 include:hubfly.test ~all", "checks": map[string]any{"mx": true, "spf": false}},
	}})
	f.ok("GET", "/api/v1/projects/p-mail/mail/domains/md_1/sending-plan", map[string]any{
		"plans": []map[string]any{
			{"plan": "free", "allowance": 3000, "priceMicroUsd": "0", "perMinute": 10, "perHour": 100, "perDay": 300},
			{"plan": "growth", "allowance": 50000, "priceMicroUsd": "15000000", "perMinute": 100, "perHour": 2000, "perDay": 10000},
		},
		"enabled": true, "canManageBilling": true, "plan": "free", "nextPlan": nil, "autoRenew": false,
		"status": "active", "periodStart": "2026-11-01T00:00:00Z", "periodEnd": "2026-12-01T00:00:00Z",
		"used": 12, "remaining": 2988, "allowance": 3000, "usageAvailable": true, "renewalPriceMicroUsd": "0",
		"balanceMicroUsd": "25500000", "billingOwnerType": "user",
	})
}

func TestMailDomainsListAndStatus(t *testing.T) {
	env := setupRealtimeTest(t)
	installMailFixtures(env.dash)
	if err := runMailGroup([]string{"domains"}); err != nil {
		t.Fatal(err)
	}
	if out := env.out.String(); !strings.Contains(out, "example.com") || !strings.Contains(out, "1/2 ok") {
		t.Fatalf("list output: %s", out)
	}
	env.dash.ok("POST", "/api/v1/projects/p-mail/mail/domains/md_1/dns/verify", map[string]any{"checks": map[string]any{"mx": true, "spf": true}})
	if err := runMailGroup([]string{"domains", "status", "EXAMPLE.com", "--check"}); err != nil {
		t.Fatal(err)
	}
	out := env.out.String()
	if !strings.Contains(out, "Sending plan:  Free (active)") || strings.Contains(out, "MISSING") {
		t.Fatalf("status output: %s", out)
	}
	if err := runMailGroup([]string{"domains", "status", "nope.com"}); err == nil || !strings.Contains(err.Error(), "example.com") {
		t.Fatalf("unknown domain error = %v", err)
	}
}

func TestMailPlanShowAndChange(t *testing.T) {
	env := setupRealtimeTest(t)
	installMailFixtures(env.dash)
	if err := runMailGroup([]string{"plan", "example.com"}); err != nil {
		t.Fatal(err)
	}
	if out := env.out.String(); !strings.Contains(out, "12 of 3000") || !strings.Contains(out, "$15.00") || !strings.Contains(out, "$25.50") {
		t.Fatalf("plan output: %s", out)
	}
	// Non-interactive without --yes refuses.
	if err := runMailGroup([]string{"plan", "change", "example.com", "--plan", "growth"}); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("expected confirmation error, got %v", err)
	}
	if err := runMailGroup([]string{"plan", "change", "example.com", "--plan", "grwth", "--yes"}); err == nil || !strings.Contains(err.Error(), "invalid --plan") {
		t.Fatalf("expected invalid plan error, got %v", err)
	}
	env.dash.on("POST", "/api/v1/projects/p-mail/mail/domains/md_1/sending-plan/change", func(r rtRecorded) (int, any) {
		return http.StatusOK, map[string]any{"id": "chg_1", "status": "pending"}
	})
	if err := runMailGroup([]string{"plan", "change", "example.com", "--plan", "growth", "--yes"}); err != nil {
		t.Fatal(err)
	}
	calls := env.dash.find("POST", "/api/v1/projects/p-mail/mail/domains/md_1/sending-plan/change")
	if len(calls) != 1 {
		t.Fatalf("change calls = %d", len(calls))
	}
	body := calls[0].Body
	if body["plan"] != "growth" || body["action"] != "purchase" || !strings.HasPrefix(body["idempotencyKey"].(string), "mail-plan:") {
		t.Fatalf("change body = %v", body)
	}
	if !strings.Contains(env.out.String(), "Purchase recorded") {
		t.Fatalf("change output: %s", env.out.String())
	}
}

func TestMailPlanAction(t *testing.T) {
	cases := []struct {
		current, wanted string
		atRenewal       bool
		resume          bool
		want            string
	}{
		{"free", "growth", false, false, "purchase"},
		{"growth", "free", false, false, "schedule"},
		{"scale", "starter", true, false, "schedule"},
		{"growth", "growth", false, true, "resume"},
	}
	for _, c := range cases {
		got, err := mailPlanAction(c.current, c.wanted, c.atRenewal, c.resume)
		if err != nil || got != c.want {
			t.Fatalf("mailPlanAction(%v) = %q, %v", c, got, err)
		}
	}
	if _, err := mailPlanAction("growth", "growth", false, false); err == nil {
		t.Fatal("same plan without --resume should fail")
	}
}

func TestMailSendTest(t *testing.T) {
	env := setupRealtimeTest(t)
	installMailFixtures(env.dash)
	env.dash.ok("POST", "/api/v1/projects/p-mail/mail/send", map[string]any{"id": "msg_1", "status": "queued"})
	if err := runMailGroup([]string{"send-test", "--from", "bad", "--to", "a@b.test", "--yes"}); err == nil {
		t.Fatal("expected invalid address error")
	}
	if err := runMailGroup([]string{"send-test", "--from", "hello@example.com", "--to", "a@b.test,c@d.test", "--yes", "--json"}); err != nil {
		t.Fatal(err)
	}
	calls := env.dash.find("POST", "/api/v1/projects/p-mail/mail/send")
	if len(calls) != 1 {
		t.Fatalf("send calls = %d", len(calls))
	}
	if recipients, _ := calls[0].Body["recipients"].([]any); len(recipients) != 2 {
		t.Fatalf("send body = %v", calls[0].Body)
	}
	if !strings.Contains(env.out.String(), `"id": "msg_1"`) {
		t.Fatalf("json output: %s", env.out.String())
	}
}

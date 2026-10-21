package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

const rtTestAppBase = rtTestBase + "/realtime/apps/1000001"

func rtTestWebhook() map[string]any {
	keyID, key := "rk_1", rtTestAppKey
	return map[string]any{
		"id": "wh_1", "appId": "1000001", "url": "https://hooks.example.test/realtime?token=s3cr3t-query",
		"events": []string{"channel_occupied", "member_added"}, "enabled": true,
		"keyId": keyID, "key": key, "createdAt": "2026-10-17T08:00:00Z", "updatedAt": "2026-10-17T08:00:00Z",
	}
}

func installRealtimeWebhookFixtures(env rtTestEnv) {
	env.dash.ok("GET", "/api/v1/managed/catalog", map[string]any{"realtime": map[string]any{
		"webhookEvents": []string{"channel_occupied", "channel_vacated", "member_added", "member_removed", "client_event"},
	}})
	env.dash.ok("GET", rtTestAppBase+"/webhooks", map[string]any{"items": []map[string]any{rtTestWebhook()}})
}

// mustJSON fails unless stdout is exactly one JSON document.
func mustJSON(t *testing.T, env rtTestEnv, into any) {
	t.Helper()
	if err := json.Unmarshal([]byte(env.out.String()), into); err != nil {
		t.Fatalf("--json output is not pure JSON: %v\n%s", err, env.out.String())
	}
}

func TestRealtimeWebhooksListHidesQueryString(t *testing.T) {
	env := setupRealtimeTest(t)
	installRealtimeWebhookFixtures(env)
	mustRun(t, "rt", "webhooks", "chat")
	out := env.out.String()
	for _, want := range []string{"wh_1", "hooks.example.test/realtime?…", "channel_occupied,member_added", "true", rtTestAppKey} {
		if !strings.Contains(out, want) {
			t.Fatalf("webhooks list missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "s3cr3t-query") {
		t.Fatalf("the table printed the URL query:\n%s", out)
	}

	env.out = &rtSyncBuffer{}
	rtOut = env.out
	mustRun(t, "rt", "hooks", "list", "--json", "chat")
	var hooks []rtWebhook
	mustJSON(t, env, &hooks)
	if len(hooks) != 1 || hooks[0].ID != "wh_1" {
		t.Fatalf("webhooks json = %+v", hooks)
	}
}

func TestRealtimeWebhooksCreateValidatesAndSendsBody(t *testing.T) {
	env := setupRealtimeTest(t)
	installRealtimeWebhookFixtures(env)
	createPath := rtTestAppBase + "/webhooks/create"
	env.dash.ok("POST", createPath, map[string]any{"webhook": rtTestWebhook(), "sync": map[string]any{"status": "applied"}})

	for _, args := range [][]string{
		{"rt", "webhooks", "create", "chat", "--url", "http://hooks.example.test/x", "--events", "member_added"},
		{"rt", "webhooks", "create", "chat", "--url", "https://user:pw@hooks.example.test/x", "--events", "member_added"},
		{"rt", "webhooks", "create", "chat", "--url", "https://hooks.example.test/x"},
	} {
		if err := run(args); err == nil {
			t.Fatalf("run(%q) succeeded", args)
		}
	}
	if raw := env.dash.allRaw(); raw != "" {
		t.Fatalf("invalid arguments reached the Dashboard:\n%s", raw)
	}

	err := run([]string{"rt", "webhooks", "create", "chat", "--url", "https://hooks.example.test/x", "--events", "member_added,nope"})
	if err == nil || !strings.Contains(err.Error(), `unknown event "nope"`) || !strings.Contains(err.Error(), "channel_vacated") {
		t.Fatalf("unknown event: %v", err)
	}
	if got := env.dash.find("POST", createPath); len(got) != 0 {
		t.Fatalf("webhook created with an unknown event")
	}

	// Default key: no keyId, the Dashboard signs with the newest active key.
	mustRun(t, "rt", "webhooks", "create", "chat", "--url", "https://hooks.example.test/x", "--events", "Member_Added,member_added,channel_occupied")
	// --key by public key, created disabled.
	mustRun(t, "rt", "webhooks", "create", "chat", "--url", "https://hooks.example.test/y", "--events", "client_event", "--key", rtTestAppKey, "--disabled", "--app", "production")
	requests := env.dash.find("POST", createPath)
	if len(requests) != 2 {
		t.Fatalf("create requests = %d", len(requests))
	}
	if want := map[string]any{"url": "https://hooks.example.test/x", "events": []any{"member_added", "channel_occupied"}}; !reflect.DeepEqual(requests[0].Body, want) {
		t.Fatalf("create body = %v", requests[0].Body)
	}
	if want := map[string]any{"url": "https://hooks.example.test/y", "events": []any{"client_event"}, "keyId": "rk_1", "enabled": false}; !reflect.DeepEqual(requests[1].Body, want) {
		t.Fatalf("create body with key = %v", requests[1].Body)
	}
	if out := env.out.String(); !strings.Contains(out, "Created webhook wh_1") || strings.Contains(out, "s3cr3t-query") {
		t.Fatalf("create output:\n%s", out)
	}

	err = run([]string{"rt", "webhooks", "create", "chat", "--url", "https://hooks.example.test/x", "--events", "client_event", "--key", "rk_missing"})
	if err == nil || len(env.dash.find("POST", createPath)) != 2 {
		t.Fatalf("unknown --key: %v", err)
	}
}

func TestRealtimeWebhooksUpdateSendsOnlyGivenFields(t *testing.T) {
	env := setupRealtimeTest(t)
	installRealtimeWebhookFixtures(env)
	updatePath := rtTestAppBase + "/webhooks/wh_1/update"
	env.dash.ok("PUT", updatePath, map[string]any{"webhook": rtTestWebhook(), "sync": map[string]any{"status": "pending"}})

	if err := run([]string{"rt", "webhooks", "update", "chat", "wh_1"}); err == nil || !strings.Contains(err.Error(), "nothing to update") {
		t.Fatalf("empty update: %v", err)
	}
	if err := run([]string{"rt", "webhooks", "update", "chat", "wh_1", "--enable", "--disable"}); err == nil {
		t.Fatalf("--enable with --disable succeeded")
	}
	mustRun(t, "rt", "webhooks", "update", "chat", "wh_1", "--disable")
	mustRun(t, "rt", "webhooks", "update", "chat", "wh_1", "--events", "channel_vacated", "--key", "rk_1", "--url", "https://hooks.example.test/z")
	requests := env.dash.find("PUT", updatePath)
	if len(requests) != 2 {
		t.Fatalf("update requests = %d", len(requests))
	}
	if want := map[string]any{"enabled": false}; !reflect.DeepEqual(requests[0].Body, want) {
		t.Fatalf("update body = %v", requests[0].Body)
	}
	want := map[string]any{"url": "https://hooks.example.test/z", "events": []any{"channel_vacated"}, "keyId": "rk_1"}
	if !reflect.DeepEqual(requests[1].Body, want) {
		t.Fatalf("update body = %v", requests[1].Body)
	}
}

func TestRealtimeWebhooksDeleteNeedsConfirmation(t *testing.T) {
	env := setupRealtimeTest(t)
	installRealtimeWebhookFixtures(env)
	deletePath := rtTestAppBase + "/webhooks/wh_1/delete"
	env.dash.ok("DELETE", deletePath, map[string]any{"deleted": true, "sync": map[string]any{"status": "applied"}})

	if err := run([]string{"rt", "webhooks", "delete", "chat", "wh_1"}); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("non-interactive delete: %v", err)
	}
	rtInteractive = func() bool { return true }
	setTestStdin("n\n")
	mustRun(t, "rt", "webhooks", "delete", "chat", "wh_1")
	if len(env.dash.find("DELETE", deletePath)) != 0 {
		t.Fatalf("deleted after a no")
	}
	if err := run([]string{"rt", "webhooks", "delete", "chat", "wh_404", "--yes"}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("unknown webhook: %v", err)
	}
	mustRun(t, "rt", "webhooks", "delete", "chat", "wh_1", "-y")
	if len(env.dash.find("DELETE", deletePath)) != 1 || !strings.Contains(env.out.String(), "Deleted webhook wh_1") {
		t.Fatalf("delete output:\n%s", env.out.String())
	}
}

func TestRealtimeWebhooksTestReportsReceiverFailure(t *testing.T) {
	env := setupRealtimeTest(t)
	installRealtimeWebhookFixtures(env)
	testPath := rtTestAppBase + "/webhooks/wh_1/test"
	env.dash.ok("POST", testPath, map[string]any{"ok": false, "statusCode": nil, "durationMs": 12, "error": "host is not public"})
	err := run([]string{"rt", "webhooks", "test", "chat", "wh_1"})
	if err == nil || !strings.Contains(err.Error(), "did not accept") {
		t.Fatalf("failed test: %v", err)
	}
	if out := env.out.String(); !strings.Contains(out, "failed: no response in 12 ms") || !strings.Contains(out, "host is not public") {
		t.Fatalf("test output:\n%s", out)
	}

	env.dash.ok("POST", testPath, map[string]any{"ok": true, "statusCode": 204, "durationMs": 40, "error": nil})
	env.out = &rtSyncBuffer{}
	rtOut = env.out
	mustRun(t, "rt", "webhooks", "test", "chat", "wh_1", "--json")
	var result rtWebhookTestResult
	mustJSON(t, env, &result)
	if !result.OK || result.StatusCode == nil || *result.StatusCode != 204 {
		t.Fatalf("test json = %+v", result)
	}
}

func TestRealtimeWebhookDeliveriesPagesAndFilters(t *testing.T) {
	env := setupRealtimeTest(t)
	deliveriesPath := rtTestAppBase + "/webhooks/deliveries"
	delivery := func(id string) map[string]any {
		return map[string]any{
			"id": id, "webhookId": "wh_1", "status": "failed", "attempts": 3, "events": 2, "firstEvent": "member_added",
			"createdAt": "2026-10-20T09:00:00Z", "lastAttemptAt": "2026-10-20T09:01:00Z", "lastStatusCode": 502, "lastError": "bad gateway",
		}
	}
	env.dash.on("GET", deliveriesPath, func(request rtRecorded) (int, any) {
		size, _ := strconv.Atoi(request.Query.Get("limit"))
		items := []map[string]any{}
		for index := 0; index < size; index++ {
			items = append(items, delivery(fmt.Sprintf("d%s_%d", request.Query.Get("cursor"), index)))
		}
		next := "c2"
		if request.Query.Get("cursor") == "c2" {
			next = "c3"
		}
		return http.StatusOK, map[string]any{"items": items, "nextCursor": next}
	})

	if err := run([]string{"rt", "webhooks", "deliveries", "chat", "--status", "lost"}); err == nil {
		t.Fatalf("unknown --status succeeded")
	}
	mustRun(t, "rt", "webhooks", "deliveries", "chat", "--limit", "150", "--status", "FAILED", "--webhook", "wh_1")
	requests := env.dash.find("GET", deliveriesPath)
	if len(requests) != 2 {
		t.Fatalf("delivery requests = %d", len(requests))
	}
	first, second := requests[0].Query, requests[1].Query
	if first.Get("limit") != "100" || first.Get("status") != "failed" || first.Get("webhookId") != "wh_1" || first.Get("cursor") != "" {
		t.Fatalf("first page query = %v", first)
	}
	if second.Get("limit") != "50" || second.Get("cursor") != "c2" {
		t.Fatalf("second page query = %v", second)
	}
	out := env.out.String()
	for _, want := range []string{"WEBHOOK", "member_added +1", "HTTP 502: bad gateway", "2026-10-20T09:01:00Z", "More deliveries exist"} {
		if !strings.Contains(out, want) {
			t.Fatalf("deliveries output missing %q:\n%s", want, out)
		}
	}

	redeliverPath := rtTestAppBase + "/webhooks/deliveries/d_9/redeliver"
	env.dash.ok("POST", redeliverPath, map[string]any{"queued": true, "deliveryId": "d_9"})
	mustRun(t, "rt", "webhooks", "redeliver", "chat", "d_9")
	if len(env.dash.find("POST", redeliverPath)) != 1 || !strings.Contains(env.out.String(), "Queued delivery d_9 again") {
		t.Fatalf("redeliver output:\n%s", env.out.String())
	}
}

func TestRealtimeChannelsListAndInspect(t *testing.T) {
	env := setupRealtimeTest(t)
	env.dash.ok("GET", rtTestAppBase+"/channels", map[string]any{
		"items": []map[string]any{
			{"name": "presence-room", "type": "presence", "subscriptionCount": 3, "userCount": 2, "occupiedAt": "2026-10-20T09:00:00Z"},
			{"name": "orders", "type": "public", "subscriptionCount": 1, "userCount": nil},
		},
		"nextCursor": nil, "total": 5,
	})
	mustRun(t, "rt", "channels", "chat", "--prefix", "pres", "--limit", "2")
	request := env.dash.find("GET", rtTestAppBase+"/channels")[0]
	if request.Query.Get("prefix") != "pres" || request.Query.Get("limit") != "2" {
		t.Fatalf("channels query = %v", request.Query)
	}
	for _, want := range []string{"presence-room", "presence", "orders", "Showing 2 of 5"} {
		if !strings.Contains(env.out.String(), want) {
			t.Fatalf("channels output missing %q:\n%s", want, env.out.String())
		}
	}
	if err := run([]string{"rt", "channels", "chat", "--prefix", "bad prefix"}); err == nil {
		t.Fatalf("invalid --prefix succeeded")
	}

	env.dash.ok("GET", rtTestAppBase+"/channels/inspect", map[string]any{
		"name": "presence-room", "type": "presence", "occupied": true, "subscriptionCount": 3, "userCount": 2,
		"members": []map[string]any{
			{"userId": "u_1", "userInfo": `{"name":"Ada"}`, "connections": 2},
			{"userId": "u_2", "userInfo": nil, "connections": 1},
		},
		"membersTruncated": true,
		"history":          map[string]any{"mode": "durable", "epoch": "e7k2", "head": 45, "oldest": 3, "retainedMessages": 43, "retainedBytes": 2048},
		"cache":            map[string]any{"present": false},
	})
	mustRun(t, "rt", "channel", "inspect", "chat", "presence-room", "--members", "2")
	request = env.dash.find("GET", rtTestAppBase+"/channels/inspect")[0]
	if request.Query.Get("channel") != "presence-room" || request.Query.Get("membersLimit") != "2" {
		t.Fatalf("inspect query = %v", request.Query)
	}
	for _, want := range []string{"yes, 3 subscriptions", "durable, epoch e7k2, offsets 3-45 (43 messages", "u_1", `{"name":"Ada"}`, "More members are present"} {
		if !strings.Contains(env.out.String(), want) {
			t.Fatalf("inspect output missing %q:\n%s", want, env.out.String())
		}
	}
	if err := run([]string{"rt", "channels", "inspect", "chat", "bad channel"}); err == nil {
		t.Fatalf("invalid channel succeeded")
	}
}

func TestRealtimeHistoryKeepsEncryptedDataOpaque(t *testing.T) {
	env := setupRealtimeTest(t)
	historyPath := rtTestAppBase + "/history"
	env.dash.ok("GET", historyPath, map[string]any{
		"channel": "orders", "mode": "durable", "epoch": "e1", "head": 2, "oldest": 1, "truncated": true, "gap": false,
		"messages": []map[string]any{
			{"offset": 1, "event": "order.created", "data": `{"id":7}`, "publishedAt": "2026-10-20T09:00:00Z", "size": 8},
			{"offset": 2, "event": realtimeE2EEvent, "data": `{"v":1,"ct":"c2VhbGVk"}`, "publishedAt": "2026-10-20T09:00:01Z", "size": 24},
		},
	})
	if err := run([]string{"rt", "history", "chat"}); err == nil || !strings.Contains(err.Error(), "--channel") {
		t.Fatalf("history without --channel: %v", err)
	}
	mustRun(t, "rt", "history", "chat", "--channel", "orders", "--limit", "10")
	request := env.dash.find("GET", historyPath)[0]
	if request.Query.Get("channel") != "orders" || request.Query.Get("limit") != "10" {
		t.Fatalf("history query = %v", request.Query)
	}
	out := env.out.String()
	for _, want := range []string{"durable, epoch e1, offsets 1-2", "order.created", `{"id":7}`, "(end-to-end encrypted)", "1 message is end-to-end encrypted", "Older messages are retained"} {
		if !strings.Contains(out, want) {
			t.Fatalf("history output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "c2VhbGVk") {
		t.Fatalf("the table printed an encrypted envelope:\n%s", out)
	}
}

func TestRealtimeHistoryPurge(t *testing.T) {
	env := setupRealtimeTest(t)
	purgePath := rtTestAppBase + "/history/purge"
	env.dash.ok("POST", purgePath, map[string]any{"purgedMessages": 12})
	if err := run([]string{"rt", "history", "purge", "chat", "--channel", "orders"}); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("non-interactive purge: %v", err)
	}
	mustRun(t, "rt", "history", "purge", "chat", "--channel", "orders", "--yes")
	mustRun(t, "rt", "history", "purge", "chat", "--yes")
	requests := env.dash.find("POST", purgePath)
	if len(requests) != 2 || !reflect.DeepEqual(requests[0].Body, map[string]any{"channel": "orders"}) || len(requests[1].Body) != 0 {
		t.Fatalf("purge requests = %+v", requests)
	}
	if out := env.out.String(); !strings.Contains(out, "Purged 12 messages from channel 'orders'") || !strings.Contains(out, "from every channel of app 'production'") {
		t.Fatalf("purge output:\n%s", out)
	}
}

func TestRealtimeConnectionsStripsE2EKeys(t *testing.T) {
	env := setupRealtimeTest(t)
	const clientKey = "Zm9vYmFyYmF6cXV4c2VjcmV0a2V5MDEyMzQ1Njc4OQ"
	connectionsPath := rtTestBase + "/realtime/connections"
	env.dash.ok("GET", connectionsPath, map[string]any{"items": []map[string]any{{
		"socketId": "1234567890.1234567890~" + clientKey, "appId": "1000001", "userId": "u_1", "protocol": "7",
		"client": map[string]any{"name": "pusher-js", "version": "8.4.0"}, "state": "open",
		"connectedAt": "2026-10-20T09:00:00Z", "subscriptionCount": 2, "subscriptions": []string{"a", "b"},
	}}, "nextCursor": nil})

	mustRun(t, "rt", "connections", "chat")
	if got := env.dash.find("GET", connectionsPath)[0].Query; got.Get("appId") != "" || got.Get("limit") != "100" {
		t.Fatalf("connections query without filters = %v", got)
	}
	mustRun(t, "rt", "connections", "chat", "--user", "u_1", "--channel", "orders", "--json")
	got := env.dash.find("GET", connectionsPath)[1].Query
	if got.Get("appId") != "1000001" || got.Get("userId") != "u_1" || got.Get("channel") != "orders" {
		t.Fatalf("filtered connections query = %v", got)
	}
	out := env.out.String()
	for _, want := range []string{"SOCKET ID", "1234567890.1234567890", "production", "pusher-js 8.4.0"} {
		if !strings.Contains(out, want) {
			t.Fatalf("connections output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, clientKey) {
		t.Fatalf("the E2E client key was printed:\n%s", out)
	}

	// --user needs an app when the server has several.
	second := rtTestApp()
	second["appId"], second["name"] = "1000002", "staging"
	env.dash.ok("GET", rtTestBase+"/realtime/apps", map[string]any{"items": []map[string]any{rtTestApp(), second}})
	if err := run([]string{"rt", "connections", "chat", "--user", "u_1"}); err == nil || !strings.Contains(err.Error(), "--app") {
		t.Fatalf("--user without --app: %v", err)
	}
}

func TestRealtimeDisconnectConnectionsAndUser(t *testing.T) {
	env := setupRealtimeTest(t)
	const clientKey = "Zm9vYmFyYmF6cXV4c2VjcmV0a2V5MDEyMzQ1Njc4OQ"
	terminatePath := rtTestAppBase + "/connections/terminate"
	env.dash.on("POST", terminatePath, func(request rtRecorded) (int, any) {
		if request.Body["socketId"] == "9999999999.9999999999" {
			return http.StatusNotFound, map[string]any{"ok": false, "error": map[string]any{"code": "MANAGED_REALTIME_CONNECTION_NOT_FOUND", "message": "not open"}}
		}
		mode, _ := request.Body["mode"].(string)
		if mode == "" {
			mode = "terminate"
		}
		return http.StatusOK, map[string]any{"terminated": true, "mode": mode}
	})

	for _, args := range [][]string{
		{"rt", "disconnect", "chat"},
		{"rt", "disconnect", "chat", "--connection", "1234567890.1234567890", "--user", "u_1"},
		{"rt", "disconnect", "chat", "--connection", "1234567890.1234567890", "--mode", "kick"},
		{"rt", "disconnect", "chat", "--user", "u_1", "--mode", "disconnect"},
	} {
		if err := run(args); err == nil {
			t.Fatalf("run(%q) succeeded", args)
		}
	}
	if err := run([]string{"rt", "disconnect", "chat", "--connection", "1234567890.1234567890"}); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("non-interactive disconnect: %v", err)
	}
	if len(env.dash.find("POST", terminatePath)) != 0 {
		t.Fatalf("an invalid or unconfirmed disconnect reached the Dashboard")
	}

	mustRun(t, "rt", "disconnect", "chat", "--connection", "1234567890.1234567890~"+clientKey, "--yes")
	mustRun(t, "rt", "disconnect", "chat", "--connection", "1234567890.1234567890", "--mode", "disconnect", "--yes")
	requests := env.dash.find("POST", terminatePath)
	if len(requests) != 2 || !reflect.DeepEqual(requests[0].Body, map[string]any{"socketId": "1234567890.1234567890"}) ||
		!reflect.DeepEqual(requests[1].Body, map[string]any{"socketId": "1234567890.1234567890", "mode": "disconnect"}) {
		t.Fatalf("terminate requests = %+v", requests)
	}
	for name, text := range map[string]string{"requests": env.dash.allRaw(), "stdout": env.out.String(), "stderr": env.errOut.String()} {
		if strings.Contains(text, clientKey) {
			t.Fatalf("the E2E client key leaked into %s", name)
		}
	}
	if !strings.Contains(env.errOut.String(), "Removed the client key") || !strings.Contains(env.out.String(), "Disconnected 1234567890.1234567890 (the client reconnects)") {
		t.Fatalf("disconnect output:\n%s\n%s", env.out.String(), env.errOut.String())
	}

	err := run([]string{"rt", "disconnect", "chat", "--connection", "1234567890.1234567890,9999999999.9999999999", "--yes"})
	if err == nil || !strings.Contains(err.Error(), "1 of 2") || !strings.Contains(env.out.String(), "Connection 9999999999.9999999999 is not open") {
		t.Fatalf("partly closed: %v\n%s", err, env.out.String())
	}

	usersPath := rtTestAppBase + "/users/terminate"
	env.dash.ok("POST", usersPath, map[string]any{"terminated": 3})
	mustRun(t, "rt", "disconnect", "chat", "--user", "u_1", "--yes")
	if got := env.dash.find("POST", usersPath); len(got) != 1 || !reflect.DeepEqual(got[0].Body, map[string]any{"userId": "u_1"}) {
		t.Fatalf("user terminate requests = %+v", got)
	}
	if !strings.Contains(env.out.String(), "Terminated 3 connections of user 'u_1'") {
		t.Fatalf("user terminate output:\n%s", env.out.String())
	}
}

func TestRealtimeUsageSince(t *testing.T) {
	now := time.Date(2026, 10, 20, 15, 0, 0, 0, time.UTC)
	for value, want := range map[string]string{"7d": "2026-10-13", "48h": "2026-10-18", "2026-10-01": "2026-10-01", "0s": "2026-10-20"} {
		if got, err := realtimeUsageSince(value, now); err != nil || got != want {
			t.Fatalf("realtimeUsageSince(%q) = %q, %v; want %q", value, got, err, want)
		}
	}
	for _, value := range []string{"7", "soon", "2026-10-21", "500d"} {
		if _, err := realtimeUsageSince(value, now); err == nil {
			t.Fatalf("realtimeUsageSince(%q) succeeded", value)
		}
	}
}

func TestRealtimeUsageQueryAndTotals(t *testing.T) {
	env := setupRealtimeTest(t)
	realtimeUsageNow = func() time.Time { return time.Date(2026, 10, 20, 15, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { realtimeUsageNow = time.Now })
	usagePath := rtTestBase + "/realtime/usage"
	day := func(date string, peak int) map[string]any {
		return map[string]any{
			"day": date, "appId": "1000001", "peakConnections": peak, "connectionMinutes": 600, "messagesIn": 1000,
			"messagesOut": 25000, "bytesIn": 2048, "bytesOut": 1048576, "webhookDeliveries": 4,
		}
	}
	env.dash.ok("GET", usagePath, map[string]any{"days": []map[string]any{day("2026-10-19", 12), day("2026-10-20", 30)}})

	mustRun(t, "rt", "usage", "chat", "--since", "7d", "--app", "production")
	request := env.dash.find("GET", usagePath)[0]
	if request.Query.Get("since") != "2026-10-13" || request.Query.Get("appId") != "1000001" {
		t.Fatalf("usage query = %v", request.Query)
	}
	out := env.out.String()
	for _, want := range []string{"since 2026-10-13", "production", "TOTAL", "50,000", "1,200"} {
		if !strings.Contains(out, want) {
			t.Fatalf("usage output missing %q:\n%s", want, out)
		}
	}

	env.out = &rtSyncBuffer{}
	rtOut = env.out
	mustRun(t, "rt", "usage", "chat", "--json")
	if got := env.dash.find("GET", usagePath)[1].Query; len(got) != 0 {
		t.Fatalf("usage query without flags = %v", got)
	}
	var report rtUsage
	mustJSON(t, env, &report)
	if len(report.Days) != 2 || report.Days[1].PeakConnections != 30 {
		t.Fatalf("usage json = %+v", report)
	}
}

func TestRealtimeWebhookDisplayURL(t *testing.T) {
	for raw, want := range map[string]string{
		"https://hooks.example.test/a/b?token=x": "hooks.example.test/a/b?…",
		"https://hooks.example.test:8443/a":      "hooks.example.test:8443/a",
		"https://hooks.example.test":             "hooks.example.test",
	} {
		if got := realtimeWebhookDisplayURL(raw); got != want {
			t.Fatalf("realtimeWebhookDisplayURL(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestRealtimeDeliveryResponseSkipsRepeatedStatus(t *testing.T) {
	code, duration := 405, int64(140)
	repeated, other := "HTTP 405", "timeout"
	if got := realtimeDeliveryResponse(rtWebhookDelivery{LastStatusCode: &code, LastError: &repeated, DurationMs: &duration}); got != "HTTP 405 (140 ms)" {
		t.Fatalf("repeated status = %q", got)
	}
	if got := realtimeDeliveryResponse(rtWebhookDelivery{LastError: &other}); got != "timeout" {
		t.Fatalf("error only = %q", got)
	}
}

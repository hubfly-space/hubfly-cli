package cli

import (
	"strings"
	"testing"
)

func TestRedactJSONForDebugHidesTokensAndSecretValues(t *testing.T) {
	payload := []byte(`{"uploadToken":"upload-secret","environmentVariables":[{"key":"PASSWORD","value":"super-secret","isSecret":true}],"token":"api-secret","plain":"visible"}`)
	redacted := redactJSONForDebug(payload)
	for _, secret := range []string{"upload-secret", "super-secret", "api-secret"} {
		if strings.Contains(redacted, secret) {
			t.Fatalf("debug output leaked %q: %s", secret, redacted)
		}
	}
	if !strings.Contains(redacted, "visible") {
		t.Fatalf("expected non-secret diagnostic value to remain: %s", redacted)
	}
}

func TestRedactJSONForDebugHidesManagedCredentialValues(t *testing.T) {
	payload := []byte(`{"credentials":[{"kind":"password","username":"admin","value":"admin-secret"},{"kind":"token","value":"libsql-secret"}],"setting":{"kind":"text","value":"visible-setting"}}`)
	redacted := redactJSONForDebug(payload)
	for _, secret := range []string{"admin-secret", "libsql-secret"} {
		if strings.Contains(redacted, secret) {
			t.Fatalf("debug output leaked %q: %s", secret, redacted)
		}
	}
	for _, visible := range []string{"admin", "visible-setting"} {
		if !strings.Contains(redacted, visible) {
			t.Fatalf("debug output lost non-secret %q: %s", visible, redacted)
		}
	}
}

func TestRedactJSONForDebugHidesRealtimeSecrets(t *testing.T) {
	payload := []byte(`{"ok":true,"data":{
		"app":{"appId":"1000001","keys":[{"id":"rk_1","key":"publickey0123456789a","secretHint":"…wxyz","secretFingerprint":"fp-visible"}]},
		"key":{"id":"rk_2","key":"publickey0123456789b"},
		"secret":"rt-app-secret",
		"inspector":{"token":"inspector-jwt","url":"wss://chat.rt.hubfly.app/hubfly/v1/inspect"},
		"terminal":{"connectToken":"terminal-connect-token"},
		"webhook":{"signingSecret":"webhook-signing-secret"},
		"variables":[
			{"key":"HUBFLY_REALTIME_SECRET","value":"env-secret-1"},
			{"key":"PUSHER_APP_SECRET","value":"env-secret-2","secret":true},
			{"key":"HUBFLY_REALTIME_E2E_KEYS","value":"k1:e2e-master-key"},
			{"key":"HUBFLY_REALTIME_URL","value":"https://chat.rt.hubfly.app","secret":false}
		]}}`)
	redacted := redactJSONForDebug(payload)
	for _, secret := range []string{"rt-app-secret", "inspector-jwt", "terminal-connect-token", "webhook-signing-secret", "env-secret-1", "env-secret-2", "e2e-master-key"} {
		if strings.Contains(redacted, secret) {
			t.Fatalf("debug output leaked %q: %s", secret, redacted)
		}
	}
	for _, visible := range []string{"publickey0123456789a", "fp-visible", "…wxyz", "https://chat.rt.hubfly.app", `"secret":true`, `"secret":false`, "wss://chat.rt.hubfly.app/hubfly/v1/inspect"} {
		if !strings.Contains(redacted, visible) {
			t.Fatalf("debug output lost non-secret %q: %s", visible, redacted)
		}
	}
}

func TestDebugOutputNeverShowsRealtimeKeySecrets(t *testing.T) {
	env := setupRealtimeTest(t)
	keysPath := rtTestBase + "/realtime/apps/1000001/keys"
	env.dash.ok("POST", keysPath+"/create", map[string]any{"key": map[string]any{"id": "rk_2", "key": "kkkkkkkkkk0123456789"}, "secret": "debug-must-not-show-1", "sync": map[string]any{"status": "applied"}})
	env.dash.ok("POST", keysPath+"/rk_1/reveal", map[string]any{"keyId": "rk_1", "key": rtTestAppKey, "secret": "debug-must-not-show-2"})
	env.dash.ok("POST", rtTestBase+"/realtime/apps/create", map[string]any{"app": map[string]any{"appId": "1000002", "name": "s"}, "key": map[string]any{"id": "rk_9"}, "secret": "debug-must-not-show-3"})
	debugEnabled = true
	stderr := captureStderr(t, func() {
		mustRun(t, "rt", "keys", "create", "chat")
		mustRun(t, "rt", "env", "chat", "--reveal")
		mustRun(t, "rt", "apps", "create", "chat", "s")
	})
	debugEnabled = false
	if !strings.Contains(stderr, "Response body:") {
		t.Fatalf("debug output was not captured:\n%s", stderr)
	}
	for _, secret := range []string{"debug-must-not-show-1", "debug-must-not-show-2", "debug-must-not-show-3", "hf_test_token"} {
		if strings.Contains(stderr, secret) {
			t.Fatalf("--debug output leaked %q", secret)
		}
	}
}

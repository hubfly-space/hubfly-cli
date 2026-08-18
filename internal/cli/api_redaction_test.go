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

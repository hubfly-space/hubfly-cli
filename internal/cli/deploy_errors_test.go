package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDeploySessionFailureMessage(t *testing.T) {
	tests := []struct{ name, payload, want string }{
		{"quota response", `{"build":{"id":"build_test","status":"failed","errorCode":"FREE_TIER_EXHAUSTED","error":"Monthly free compute allowance exhausted. Choose a paid dedicated tier or wait for the monthly allowance to reset."}}`, "FREE_TIER_EXHAUSTED: Monthly free compute allowance exhausted. Choose a paid dedicated tier or wait for the monthly allowance to reset."},
		{"older server without code", `{"build":{"error":"Deployment failed after the build completed."}}`, "Deployment failed after the build completed."},
		{"code without message", `{"build":{"errorCode":"FREE_TIER_EXHAUSTED"}}`, "FREE_TIER_EXHAUSTED"},
		{"missing detail", `{"build":{"id":"build_test","error":"  "}}`, "no failure detail was returned; inspect deployment events for build build_test"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var status deploySessionStatusResponse
			if err := json.Unmarshal([]byte(tt.payload), &status); err != nil {
				t.Fatal(err)
			}
			if got := deploySessionFailureMessage(status); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
	var status deploySessionStatusResponse
	status.Build.Status = "success"
	if got := deploySessionErrorMessage(status); strings.TrimSpace(got) != "" {
		t.Fatalf("successful status has error: %q", got)
	}
}

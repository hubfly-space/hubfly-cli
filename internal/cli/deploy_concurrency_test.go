package cli

import (
	"net/http"
	"testing"
)

func TestIsActiveDeploymentConflict(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "active lease message", err: &apiError{Status: http.StatusConflict, Message: "Another deployment is already active for this container"}, want: true},
		{name: "active deployment wording", err: &apiError{Status: http.StatusConflict, Message: "active deployment already exists"}, want: true},
		{name: "spec conflict", err: &apiError{Status: http.StatusConflict, Code: "SPEC_CHANGED", Message: "Cloud specification changed"}, want: false},
		{name: "server error", err: &apiError{Status: http.StatusInternalServerError, Message: "active deployment"}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isActiveDeploymentConflict(tt.err); got != tt.want {
				t.Fatalf("isActiveDeploymentConflict() = %v, want %v", got, tt.want)
			}
		})
	}
}

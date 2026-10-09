package cli

import "strings"

// Keep the server's safe, actionable message and its machine-readable cause.
func deploySessionErrorMessage(status deploySessionStatusResponse) string {
	message := strings.TrimSpace(status.Build.Error)
	code := strings.TrimSpace(status.Build.ErrorCode)
	if code == "" {
		return message
	}
	if message == "" {
		return code
	}
	return code + ": " + message
}

func deploySessionFailureMessage(status deploySessionStatusResponse) string {
	if message := deploySessionErrorMessage(status); message != "" {
		return message
	}
	return "no failure detail was returned; inspect deployment events for build " + status.Build.ID
}

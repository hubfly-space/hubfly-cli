package cli

import (
	"fmt"
	"os"
	"strings"
)

var debugEnabled bool

func configureDebug(args []string) []string {
	for _, envName := range []string{"HUBFLY_DEBUG", "DEBUG"} {
		value := strings.TrimSpace(strings.ToLower(os.Getenv(envName)))
		if value == "1" || value == "true" || value == "yes" || value == "on" {
			debugEnabled = true
			break
		}
	}

	filtered := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == "--debug" {
			debugEnabled = true
			continue
		}
		filtered = append(filtered, arg)
	}
	return filtered
}

func debugf(format string, a ...any) {
	if !debugEnabled {
		return
	}
	fmt.Fprintf(os.Stderr, "[debug] "+format+"\n", a...)
}

func maskToken(token string) string {
	if token == "" {
		return ""
	}
	if len(token) <= 8 {
		return "***"
	}
	return token[:4] + "..." + token[len(token)-4:]
}

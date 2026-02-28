package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var (
	debugEnabled bool
	debugQuiet   bool
	debugMu      sync.Mutex
)

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

func setTUIDebugMode(active bool) {
	debugMu.Lock()
	defer debugMu.Unlock()
	debugQuiet = active
}

func debugf(format string, a ...any) {
	if !debugEnabled {
		return
	}
	line := fmt.Sprintf("[debug] "+format+"\n", a...)

	debugMu.Lock()
	quiet := debugQuiet
	debugMu.Unlock()

	if !quiet {
		_, _ = fmt.Fprint(os.Stderr, line)
		return
	}

	logDir := filepath.Join(hubflyDir(), "logs")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return
	}
	logPath := filepath.Join(logDir, "debug.log")
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = f.WriteString(line)
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

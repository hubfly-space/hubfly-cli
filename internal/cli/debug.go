package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/zalando/go-keyring"
	"golang.org/x/mod/semver"

	"hubfly-cli/internal/version"
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

// ---------------------------------------------------------------------------
// hubfly doctor: connectivity, auth, version and clock-skew checks.

type doctorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"` // ok, warn, fail
	Detail string `json:"detail"`
	Hint   string `json:"hint,omitempty"`
}

const maxClockSkew = 2 * time.Minute

func doctorFlow(args []string) error {
	jsonOut := false
	for _, arg := range args {
		switch arg {
		case "--json":
			jsonOut = true
		case "-h", "--help", "help":
			fmt.Println("usage: hubfly doctor [--json]")
			fmt.Println("Checks API connectivity, proxy, clock skew, authentication, CLI version and Docker.")
			return nil
		default:
			return fmt.Errorf("usage: hubfly doctor [--json] (unknown argument %q)", arg)
		}
	}

	checks := make([]doctorCheck, 0, 8)
	add := func(name, status, detail, hint string) {
		checks = append(checks, doctorCheck{Name: name, Status: status, Detail: detail, Hint: hint})
	}

	add("cli", "ok", fmt.Sprintf("hubfly %s (%s, %s/%s)", version.Version, version.Commit, runtime.GOOS, runtime.GOARCH), "")
	host := strings.TrimRight(apiHost, "/")
	add("api", "ok", host, "")

	if probe, err := http.NewRequest(http.MethodGet, host+"/health", nil); err == nil {
		proxyURL, proxyErr := http.ProxyFromEnvironment(probe)
		switch {
		case proxyErr != nil:
			add("proxy", "fail", proxyErr.Error(), "Fix HTTPS_PROXY / HTTP_PROXY (must be a valid URL).")
		case proxyURL != nil:
			redacted := *proxyURL
			redacted.User = nil
			add("proxy", "ok", "via "+redacted.String(), "")
		default:
			add("proxy", "ok", "direct (no HTTPS_PROXY for this host)", "")
		}
	}

	serverTime, latency, healthErr := doctorProbeHealth(host)
	if healthErr != nil {
		add("connectivity", "fail", healthErr.Error(), "Check your network, firewall and proxy settings (HTTPS_PROXY, NO_PROXY).")
	} else {
		add("connectivity", "ok", fmt.Sprintf("reachable in %s", latency.Round(time.Millisecond)), "")
		if serverTime.IsZero() {
			add("clock", "warn", "server sent no Date header; skew unknown", "")
		} else {
			// The Date header has 1s resolution and arrives about half the
			// round trip after it was stamped.
			skew := time.Until(serverTime.Add(latency / 2))
			if skew < 0 {
				skew = -skew
			}
			if skew > maxClockSkew {
				add("clock", "warn", fmt.Sprintf("local clock is off by %s", skew.Round(time.Second)), "Sync your system clock (NTP); signed URLs and tokens can fail with a skewed clock.")
			} else {
				add("clock", "ok", fmt.Sprintf("skew %s", skew.Round(time.Second)), "")
			}
		}
	}

	source := doctorTokenSource()
	token, tokenErr := getToken()
	switch {
	case tokenErr != nil:
		add("auth", "fail", tokenErr.Error(), "Run `hubfly login`.")
	case strings.TrimSpace(token) == "":
		add("auth", "warn", "not logged in", "Run `hubfly login` (or set HUBFLY_TOKEN).")
	case healthErr != nil:
		add("auth", "warn", "token found ("+source+"); not verified because the API is unreachable", "")
	default:
		u, err := fetchWhoAmI(token)
		if err != nil {
			hint := ErrorHint(err)
			var apiErr *apiError
			status := "warn"
			if errors.As(err, &apiErr) && (apiErr.Status == http.StatusUnauthorized || apiErr.Status == http.StatusForbidden) {
				status = "fail"
				if hint == "" {
					hint = "Run `hubfly login`."
				}
			}
			add("auth", status, fmt.Sprintf("token from %s rejected: %v", source, err), hint)
		} else {
			add("auth", "ok", fmt.Sprintf("logged in as %s (%s), token from %s", u.Name, u.Email, source), "")
		}
	}

	if info, err := os.Stat(configPath()); err == nil && runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		add("credentials file", "warn", fmt.Sprintf("%s is mode %o", configPath(), info.Mode().Perm()), "Run: chmod 600 "+configPath())
	}

	current := normalizeVersion(version.Version)
	if rel, err := fetchLatestRelease(); err != nil {
		add("version", "warn", "could not check for updates: "+err.Error(), "")
	} else if latest := normalizeVersion(rel.TagName); current != "" && latest != "" && semver.Compare(current, latest) < 0 {
		add("version", "warn", fmt.Sprintf("%s available (you have %s)", latest, version.Version), "Run `hubfly update`.")
	} else if current == "" {
		add("version", "ok", fmt.Sprintf("development build; latest release is %s", rel.TagName), "")
	} else {
		add("version", "ok", "up to date ("+version.Version+")", "")
	}

	if path, err := exec.LookPath("docker"); err != nil {
		add("docker", "warn", "docker not found in PATH", "Install Docker to build images locally with `hubfly deploy`; remote commands work without it.")
	} else {
		add("docker", "ok", path, "")
	}

	failures := 0
	for _, check := range checks {
		if check.Status == "fail" {
			failures++
		}
	}
	if jsonOut {
		payload, _ := json.MarshalIndent(map[string]any{"ok": failures == 0, "checks": checks}, "", "  ")
		fmt.Println(string(payload))
	} else {
		for _, check := range checks {
			label := map[string]string{"ok": " ok ", "warn": "warn", "fail": "FAIL"}[check.Status]
			fmt.Printf("[%s] %-16s %s\n", label, check.Name, check.Detail)
			if check.Hint != "" {
				fmt.Printf("       %-16s %s\n", "", check.Hint)
			}
		}
	}
	if failures > 0 {
		return fmt.Errorf("doctor found %d problem(s)", failures)
	}
	return nil
}

func doctorProbeHealth(host string) (time.Time, time.Duration, error) {
	ctx, cancel := RequestContext(15 * time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, host+"/health", nil)
	if err != nil {
		return time.Time{}, 0, err
	}
	started := time.Now()
	resp, err := DoHTTP(req)
	if err != nil {
		return time.Time{}, 0, (&requestError{Method: http.MethodGet, URL: host + "/health", Timeout: RequestTimeout(15 * time.Second), Err: err})
	}
	latency := time.Since(started)
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode >= 500 {
		return time.Time{}, latency, fmt.Errorf("health check returned HTTP %d", resp.StatusCode)
	}
	var serverTime time.Time
	if date := resp.Header.Get("Date"); date != "" {
		if parsed, parseErr := http.ParseTime(date); parseErr == nil {
			serverTime = parsed
		}
	}
	return serverTime, latency, nil
}

func doctorTokenSource() string {
	if tokenFromEnv() {
		return "HUBFLY_TOKEN"
	}
	if _, err := keyring.Get(credentialService, credentialAccount); err == nil {
		return "OS credential store"
	}
	if _, err := os.Stat(configPath()); err == nil {
		return configPath()
	}
	return "none"
}

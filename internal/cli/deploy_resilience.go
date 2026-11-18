package cli

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Deploy exit codes. 2-7 are shared with every command (see exitCodeForError);
// the 10+ codes are specific to `hubfly deploy` and `hubfly stack up` so CI
// can tell a broken Dockerfile from a network failure or a failed rollout.
const (
	exitDeployBuildFailed  = 10  // local docker build failed; nothing was uploaded
	exitDeployUploadFailed = 11  // image upload failed after retries; nothing was deployed
	exitDeployFailed       = 12  // Hubfly reported the rollout failed or was cancelled
	exitDeployUnknown      = 13  // timed out / lost contact; the rollout may still be running
	exitDeployConflict     = 14  // another deployment is active for the container
	exitDeployInterrupted  = 130 // Ctrl-C / SIGTERM
)

func deployExitCodesHelp() string {
	return strings.TrimSpace(`
exit codes:
  0    deployed (or plan printed / --detach upload finished)
  2    invalid usage or configuration
  3    not authenticated or not allowed
  5    other API error
  6    network timeout talking to Hubfly
  7    answered no at a confirmation prompt
  10   local docker build failed (nothing uploaded)
  11   image upload failed after retries (nothing deployed)
  12   deployment failed or was stopped on Hubfly
  13   outcome unknown: timed out or lost contact; check 'hubfly deploy status <buildId>'
  14   another deployment is already active for this container
  130  interrupted (Ctrl-C)
`)
}

type deployExitError struct {
	code int
	err  error
}

func (e *deployExitError) Error() string { return e.err.Error() }
func (e *deployExitError) Unwrap() error { return e.err }
func (e *deployExitError) ExitCode() int { return e.code }

func withDeployExitCode(code int, err error) error {
	if err == nil {
		return nil
	}
	var existing *deployExitError
	if errors.As(err, &existing) {
		return err
	}
	return &deployExitError{code: code, err: err}
}

// newDeployContext is cancelled by Ctrl-C/SIGTERM through the shared root
// context that Main's interrupt handler owns (see httpx.go).
func newDeployContext() (context.Context, context.CancelFunc) {
	return context.WithCancel(RootContext())
}

// reportDeployFailureDetached marks a session failed during interrupt
// cleanup. The root context is already cancelled by then and Main force-quits
// after a short grace period, so use a fresh, short-lived context.
func reportDeployFailureDetached(token string, session deploySessionResponse, message string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	return DoJSON(
		http.MethodPost,
		apiHost+"/api/v1/cli/deploy/sessions/"+url.PathEscape(session.BuildID)+"/fail",
		token,
		map[string]string{"uploadToken": session.Upload.Token, "error": message},
		nil,
		RequestOptions{Context: ctx, Timeout: 4 * time.Second, NoRetry: true},
	)
}

// interruptedBeforeRollout reports a Ctrl-C during the local build or upload.
// The server session is marked failed so it does not hold the container lease.
// The state is printed here because Main prints only "Interrupted." for 130.
func interruptedBeforeRollout(token string, session deploySessionResponse, stage string) error {
	state := "Hubfly session " + session.BuildID + " was marked failed; nothing was deployed and the running container is unchanged."
	if err := reportDeployFailureDetached(token, session, "Cancelled by user during "+stage); err != nil {
		state = fmt.Sprintf(
			"Could not mark Hubfly session %s as failed (%v); it expires on its own. Nothing was deployed and the running container is unchanged.",
			session.BuildID, err,
		)
	}
	err := fmt.Errorf("deploy interrupted during %s. %s", stage, state)
	fmt.Fprintln(os.Stderr, "\n"+err.Error())
	return &deployExitError{code: exitDeployInterrupted, err: err}
}

// interruptedDuringRollout reports a Ctrl-C after the image was handed over.
// The rollout belongs to Hubfly now, so it keeps going; tell the user how to
// follow or stop it instead of silently cancelling a half-applied rollout.
func interruptedDuringRollout(buildID string) error {
	err := fmt.Errorf(
		"stopped watching; deployment %s continues on Hubfly.\n  follow:  hubfly deploy status %s\n  events:  hubfly deploy events %s\n  cancel:  hubfly deploy cancel %s",
		buildID, buildID, buildID, buildID,
	)
	fmt.Fprintln(os.Stderr, "\n"+err.Error())
	return &deployExitError{code: exitDeployInterrupted, err: err}
}

// interruptedBeforeSession reports a Ctrl-C before any session existed.
func interruptedBeforeSession() error {
	err := errors.New("deploy interrupted before a deployment session was created; nothing changed on Hubfly")
	fmt.Fprintln(os.Stderr, "\n"+err.Error())
	return &deployExitError{code: exitDeployInterrupted, err: err}
}

// isTransientDeployError reports errors worth retrying: transport failures,
// timeouts, and 408/425/429/5xx answers. Auth, validation and conflict
// errors are final.
func isTransientDeployError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	var apiErr *apiError
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.Status == http.StatusRequestTimeout,
			apiErr.Status == http.StatusTooEarly,
			apiErr.Status == http.StatusTooManyRequests,
			apiErr.Status >= 500:
			return true
		}
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	message := strings.ToLower(err.Error())
	for _, needle := range []string{
		"connection reset", "connection refused", "broken pipe", "no such host",
		"tls handshake timeout", "i/o timeout", "unexpected eof", "server closed",
		"http2: ", "status code 5", "502 bad gateway", "503 service", "504 gateway",
		"429 too many", "timeout",
	} {
		if strings.Contains(message, needle) {
			return true
		}
	}
	return false
}

// deployBackoff returns the wait before retry attempt n (1-based): 1s, 2s,
// 4s, ... capped at max, with up to 25% jitter so parallel CLIs spread out.
func deployBackoff(attempt int, max time.Duration) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := time.Second << min(attempt-1, 10)
	if delay > max {
		delay = max
	}
	return delay + time.Duration(rand.Int63n(int64(delay)/4+1))
}

// --- build context scan -----------------------------------------------------

const (
	largeContextFileBytes  = 50 << 20  // warn about single files above 50 MB
	largeContextTotalBytes = 500 << 20 // warn when the whole context exceeds 500 MB
	contextScanFileLimit   = 200000
	contextScanTimeLimit   = 3 * time.Second
)

type contextFile struct {
	Path string
	Size int64
}

type buildContextReport struct {
	TotalBytes   int64
	Files        int
	LargeFiles   []contextFile
	HeavyDirs    []string
	Truncated    bool
	HubflyIgnore bool
}

type ignoreRule struct {
	pattern *regexp.Regexp
	negate  bool
}

type ignoreMatcher struct {
	rules       []ignoreRule
	hasNegation bool
}

func readIgnoreLines(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var lines []string
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

func newIgnoreMatcher(lines []string) ignoreMatcher {
	matcher := ignoreMatcher{}
	for _, line := range lines {
		negate := strings.HasPrefix(line, "!")
		if negate {
			line = strings.TrimSpace(line[1:])
			matcher.hasNegation = true
		}
		line = strings.Trim(filepath.ToSlash(filepath.Clean(line)), "/")
		if line == "" || line == "." {
			continue
		}
		compiled, err := regexp.Compile("^" + ignoreGlobToRegexp(line) + "$")
		if err != nil {
			continue
		}
		matcher.rules = append(matcher.rules, ignoreRule{pattern: compiled, negate: negate})
	}
	return matcher
}

// ignoreGlobToRegexp converts a .dockerignore glob (with ** support) to a regexp.
func ignoreGlobToRegexp(pattern string) string {
	var out strings.Builder
	for i := 0; i < len(pattern); i++ {
		ch := pattern[i]
		switch ch {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				i++
				if i+1 < len(pattern) && pattern[i+1] == '/' {
					i++
					out.WriteString("(?:.*/)?")
				} else {
					out.WriteString(".*")
				}
				continue
			}
			out.WriteString("[^/]*")
		case '?':
			out.WriteString("[^/]")
		case '[':
			end := strings.IndexByte(pattern[i:], ']')
			if end < 0 {
				out.WriteString(`\[`)
				continue
			}
			class := pattern[i+1 : i+end]
			if strings.HasPrefix(class, "!") {
				class = "^" + class[1:]
			}
			out.WriteString("[" + class + "]")
			i += end
		case '\\':
			if i+1 < len(pattern) {
				i++
				out.WriteString(regexp.QuoteMeta(string(pattern[i])))
			}
		default:
			out.WriteString(regexp.QuoteMeta(string(ch)))
		}
	}
	return out.String()
}

// Ignored reports whether a slash-separated context-relative path is excluded.
// A pattern that matches a parent directory excludes everything below it,
// and the last matching rule wins (so `!keep.txt` re-includes a file).
func (m ignoreMatcher) Ignored(rel string) bool {
	ignored := false
	for _, rule := range m.rules {
		if ignoreRuleMatches(rule.pattern, rel) {
			ignored = !rule.negate
		}
	}
	return ignored
}

func ignoreRuleMatches(pattern *regexp.Regexp, rel string) bool {
	if pattern.MatchString(rel) {
		return true
	}
	for idx := strings.IndexByte(rel, '/'); idx >= 0; {
		if pattern.MatchString(rel[:idx]) {
			return true
		}
		next := strings.IndexByte(rel[idx+1:], '/')
		if next < 0 {
			break
		}
		idx += next + 1
	}
	return false
}

func buildContextIgnoreLines(contextDir string) ([]string, bool) {
	lines := readIgnoreLines(filepath.Join(contextDir, ".dockerignore"))
	hubfly := readIgnoreLines(filepath.Join(contextDir, ".hubflyignore"))
	_, statErr := os.Stat(filepath.Join(contextDir, ".hubflyignore"))
	return append(lines, hubfly...), statErr == nil
}

// scanBuildContext walks the docker build context the way docker would send it
// (honouring .dockerignore and .hubflyignore) and records what makes uploads
// slow. It is bounded in files and time so huge trees never stall a deploy.
func scanBuildContext(contextDir string) buildContextReport {
	lines, hasHubflyIgnore := buildContextIgnoreLines(contextDir)
	matcher := newIgnoreMatcher(lines)
	report := buildContextReport{HubflyIgnore: hasHubflyIgnore}
	started := time.Now()
	heavyNames := map[string]bool{"node_modules": true, ".git": true, ".venv": true, "venv": true, "__pycache__": true, ".next": true, "target": true}

	_ = filepath.WalkDir(contextDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, relErr := filepath.Rel(contextDir, path)
		if relErr != nil || rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if report.Files >= contextScanFileLimit || time.Since(started) > contextScanTimeLimit {
			report.Truncated = true
			return fs.SkipAll
		}
		ignored := matcher.Ignored(rel)
		if entry.IsDir() {
			if ignored && !matcher.hasNegation {
				return fs.SkipDir
			}
			if !ignored && heavyNames[entry.Name()] {
				report.HeavyDirs = append(report.HeavyDirs, rel)
			}
			return nil
		}
		if ignored || !entry.Type().IsRegular() {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return nil
		}
		report.Files++
		report.TotalBytes += info.Size()
		if info.Size() >= largeContextFileBytes {
			report.LargeFiles = append(report.LargeFiles, contextFile{Path: rel, Size: info.Size()})
		}
		return nil
	})
	sort.Slice(report.LargeFiles, func(i, j int) bool { return report.LargeFiles[i].Size > report.LargeFiles[j].Size })
	return report
}

func buildContextWarnings(report buildContextReport) []string {
	var warnings []string
	for _, dir := range report.HeavyDirs {
		warnings = append(warnings, fmt.Sprintf("%s/ is part of the build context; add it to .dockerignore or .hubflyignore unless the image really needs it", dir))
	}
	for idx, file := range report.LargeFiles {
		if idx == 5 {
			warnings = append(warnings, fmt.Sprintf("...and %d more files over %s", len(report.LargeFiles)-5, formatBytes(largeContextFileBytes)))
			break
		}
		warnings = append(warnings, fmt.Sprintf("large file in build context: %s (%s)", file.Path, formatBytes(file.Size)))
	}
	if report.TotalBytes >= largeContextTotalBytes {
		warnings = append(warnings, fmt.Sprintf("build context is %s across %d files; builds and uploads will be slow", formatBytes(report.TotalBytes), report.Files))
	}
	if report.Truncated {
		warnings = append(warnings, fmt.Sprintf("build context has more than %d files or took over %s to scan; check your ignore files", contextScanFileLimit, contextScanTimeLimit))
	}
	return warnings
}

// applyHubflyIgnore makes docker honour .hubflyignore. BuildKit reads
// `<Dockerfile>.dockerignore` before `.dockerignore`, so when .hubflyignore
// exists and that file does not, write the merged rules there for the build
// and remove it afterwards. The returned cleanup is always safe to call.
func applyHubflyIgnore(contextDir, dockerfilePath string) (func(), error) {
	noop := func() {}
	hubflyIgnore := filepath.Join(contextDir, ".hubflyignore")
	if _, err := os.Stat(hubflyIgnore); err != nil {
		return noop, nil
	}
	target := dockerfilePath + ".dockerignore"
	if _, err := os.Stat(target); err == nil {
		return noop, nil
	}
	lines, _ := buildContextIgnoreLines(contextDir)
	content := "# Generated by hubfly deploy from .dockerignore + .hubflyignore; removed after the build.\n" + strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		return noop, fmt.Errorf("apply .hubflyignore: %w", err)
	}
	return func() { _ = os.Remove(target) }, nil
}

// --- deploy session creation ---------------------------------------------

func newDeployIdempotencyKey(prefix string) string {
	buf := make([]byte, 12)
	if _, err := cryptorand.Read(buf); err != nil {
		return fmt.Sprintf("%s:%d", prefix, time.Now().UnixNano())
	}
	return prefix + ":" + hex.EncodeToString(buf)
}

// activeDeploymentConflictDetails reads the 409 ACTIVE_DEPLOYMENT details the
// engine returns: the active build id and whether it applies the same plan.
func activeDeploymentConflictDetails(err error) (string, bool) {
	var apiErr *apiError
	if !errors.As(err, &apiErr) {
		return "", false
	}
	details, ok := apiErr.Details.(map[string]any)
	if !ok {
		return "", false
	}
	buildID, _ := details["buildId"].(string)
	samePlan, _ := details["samePlan"].(bool)
	return strings.TrimSpace(buildID), samePlan
}

// createDeploySessionResilient starts a deploy session, retrying transient
// failures with one idempotency key per deploy. The engine does not replay
// sessions by key yet, so when a retry hits 409 ACTIVE_DEPLOYMENT for the
// same plan right after a dropped request, that active session is our own
// orphan (its upload token was lost with the response): cancel it and retry.
func createDeploySessionResilient(ctx context.Context, token string, req createDeploySessionRequest) (deploySessionResponse, error) {
	if strings.TrimSpace(req.ClientIdempotencyKey) == "" {
		req.ClientIdempotencyKey = newDeployIdempotencyKey("cli-session")
	}
	const maxAttempts = 4
	ambiguous := false
	recovered := false
	for attempt := 1; ; attempt++ {
		session, err := createDeploySession(token, req)
		if err == nil {
			return session, nil
		}
		if ambiguous && !recovered && isActiveDeploymentConflict(err) {
			buildID, samePlan := activeDeploymentConflictDetails(err)
			if samePlan && buildID != "" && strings.TrimSpace(req.PlanHash) != "" {
				fmt.Fprintf(os.Stderr, "An earlier attempt of this deploy created session %s before the connection dropped; cancelling it and starting again.\n", buildID)
				if cancelErr := cancelDeploySession(token, buildID); cancelErr == nil {
					recovered = true
					continue
				}
			}
			return deploySessionResponse{}, err
		}
		var apiErr *apiError
		if errors.As(err, &apiErr) && strings.HasPrefix(apiErr.Code, "CLI_UPLOAD_") {
			// Per-user upload concurrency/rate limits: retrying in seconds
			// cannot help, so surface the server's guidance directly.
			return deploySessionResponse{}, err
		}
		if ctx.Err() != nil {
			return deploySessionResponse{}, interruptedBeforeSession()
		}
		if !isTransientDeployError(err) || attempt >= maxAttempts {
			return deploySessionResponse{}, err
		}
		ambiguous = true
		delay := deployBackoff(attempt, 15*time.Second)
		fmt.Fprintf(os.Stderr, "Starting the deployment failed (%v); retrying in %s (attempt %d/%d)\n", err, delay.Round(time.Second), attempt+1, maxAttempts)
		if !sleepContext(ctx, delay) {
			return deploySessionResponse{}, interruptedBeforeSession()
		}
	}
}

// activeDeploymentMessage explains a concurrent deployment and what to do.
func activeDeploymentMessage(buildID, status, phase string) string {
	return fmt.Sprintf(
		"Deployment %s is already running for this container (%s/%s), started by another `hubfly deploy`, the dashboard, or CI.\n"+
			"Only one deployment can run per container at a time.\n"+
			"  watch it:      hubfly deploy status %s\n"+
			"  supersede it:  hubfly deploy --force-new\n"+
			"  cancel it:     hubfly deploy cancel %s",
		buildID, displayDeployValue(status, "active"), displayDeployValue(phase, "-"), buildID, buildID,
	)
}

// finishAttachedDeployment reports the outcome of a deployment this process
// attached to instead of starting its own. Local changes were not deployed,
// so it exits 14 whatever the other deployment's outcome was.
func finishAttachedDeployment(status deploySessionStatusResponse, buildID string) error {
	outcome := displayDeployValue(status.Build.Status, "unknown")
	if status.Build.Status == "failed" {
		outcome += ": " + deploySessionFailureMessage(status)
	}
	return &deployExitError{code: exitDeployConflict, err: fmt.Errorf(
		"the other deployment %s finished (%s); your local changes were NOT deployed. Run `hubfly deploy` again to deploy them",
		buildID, outcome,
	)}
}

// deployOutcomeError maps a finished session to the deploy exit codes.
func deployOutcomeError(status deploySessionStatusResponse, subject string) error {
	switch status.Build.Status {
	case "success":
		return nil
	case "cancelled":
		return &deployExitError{code: exitDeployFailed, err: fmt.Errorf("%s was cancelled on Hubfly (build %s)", subject, status.Build.ID)}
	default:
		return &deployExitError{code: exitDeployFailed, err: fmt.Errorf("%s failed: %s", subject, deploySessionFailureMessage(status))}
	}
}

// errPlanNeedsNewProject stops `hubfly deploy plan` before it would create a
// project: a dry run never mutates Hubfly or the local manifest.
var errPlanNeedsNewProject = errors.New("deploy plan: this deploy would create a new project")

func printPlanForNewProject(opts deployOptions) error {
	if opts.JSON {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{
			"newProject": true,
			"operations": []any{},
			"message":    "This deploy would create a new project and container. Nothing was created; run `hubfly deploy` to apply.",
		})
	}
	fmt.Println("Plan: this deploy would create a new project and a new container.")
	fmt.Println("Nothing was created and hubfly.build.json was not changed. Run `hubfly deploy` (or pass --project <id|name>) to apply.")
	return nil
}

// completeDeployUploadWithRetry hands the uploaded digest to Hubfly. The
// endpoint is idempotent on its key, so transient failures are retried.
func completeDeployUploadWithRetry(ctx context.Context, token string, session deploySessionResponse, digest string) error {
	const maxAttempts = 5
	for attempt := 1; ; attempt++ {
		err := completeDeployUpload(token, session, digest)
		if err == nil || !isTransientDeployError(err) || attempt >= maxAttempts {
			return err
		}
		delay := deployBackoff(attempt, 15*time.Second)
		fmt.Fprintf(os.Stderr, "Finalizing the upload failed (%v); retrying in %s\n", err, delay.Round(time.Second))
		if !sleepContext(ctx, delay) {
			return err
		}
	}
}

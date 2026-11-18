package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type projectMigrationView struct {
	ID              string          `json:"id"`
	ProjectName     string          `json:"projectName"`
	Status          string          `json:"status"`
	Mode            string          `json:"mode"`
	Kind            string          `json:"kind"`
	Target          regionRefView   `json:"target"`
	Source          regionRefView   `json:"source"`
	BytesTotal      int64           `json:"bytesTotal"`
	BytesDone       int64           `json:"bytesDone"`
	DecisionPending bool            `json:"decisionPending"`
	CanCancel       bool            `json:"canCancel"`
	CanMoveBack     bool            `json:"canMoveBack"`
	Job             *projectJobView `json:"job"`
	FailureCode     *string         `json:"failureCode"`
	FailureMessage  *string         `json:"failureMessage"`
	// Additive progress/failure fields (Dashboard 2026-11-11); absent on
	// older engines, so every use tolerates nil.
	OverallProgress    *float64                `json:"overallProgress"`
	OverallEtaSeconds  *int64                  `json:"overallEtaSeconds"`
	CurrentStep        *migrationStepProgress  `json:"currentStep"`
	FailedStep         *struct{ Label string } `json:"failedStep"`
	FailureFix         *string                 `json:"failureFix"`
	Retryable          *bool                   `json:"retryable"`
	DowntimeSeconds    *int64                  `json:"downtimeSeconds"`
	DecisionDeadlineAt *string                 `json:"decisionDeadlineAt"`
	Outcome            *string                 `json:"outcome"`
}

type regionRefView struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type migrationStepProgress struct {
	Key              string  `json:"key"`
	Label            string  `json:"label"`
	Phase            string  `json:"phase"`
	Detail           *string `json:"detail"`
	Retries          int     `json:"retries"`
	RemainingSeconds *int64  `json:"remainingSeconds"`
	Stalled          bool    `json:"stalled"`
}
type projectMigrationPreview struct {
	CanMigrate  bool        `json:"canMigrate"`
	FeeMicroUSD int64       `json:"feeMicroUsd"`
	FeeWaived   bool        `json:"feeWaived"`
	Blockers    []moveIssue `json:"blockers"`
	Warnings    []moveIssue `json:"warnings"`
	Estimates   struct {
		Cold freezeEstimate `json:"cold"`
		Warm freezeEstimate `json:"warm"`
	} `json:"estimates"`
	Checklist []struct {
		Title  string  `json:"title"`
		Detail string  `json:"detail"`
		Copy   *string `json:"copy"`
	} `json:"checklist"`
	DNSPlan json.RawMessage `json:"dnsPlan"`
}
type freezeEstimate struct {
	Seconds     int64  `json:"seconds"`
	LowSeconds  int64  `json:"lowSeconds"`
	HighSeconds int64  `json:"highSeconds"`
	Basis       string `json:"basis"`
}

type migrationOptions struct {
	Project     string
	Region      string
	Mode        string
	At          string
	MaxFreeze   time.Duration
	Wait        bool
	Yes         bool
	ConfirmName string
	JSON        bool
	Timeout     time.Duration
}

func parseMigrationOptions(args []string, rehome bool) (migrationOptions, error) {
	fs := newProjectFlags("migrate")
	var o migrationOptions
	fs.StringVar(&o.Region, "region", "", "")
	fs.StringVar(&o.Mode, "mode", "warm", "")
	fs.StringVar(&o.At, "at", "", "")
	fs.DurationVar(&o.MaxFreeze, "max-freeze", 0, "")
	fs.BoolVar(&o.Wait, "wait", false, "")
	fs.BoolVar(&o.Yes, "yes", false, "")
	fs.BoolVar(&o.Yes, "y", false, "")
	fs.StringVar(&o.ConfirmName, "confirm-name", "", "")
	fs.BoolVar(&o.JSON, "json", false, "")
	fs.DurationVar(&o.Timeout, "timeout", 30*time.Minute, "")
	pos, err := parseRealtimeArgs(fs, args, "hubfly project migrate <project> --region <id> [--mode cold|warm] [--at RFC3339] [--max-freeze 10m] [--wait] [--yes --confirm-name <name>] [--json]")
	if err != nil {
		return o, err
	}
	if len(pos) != 1 || o.Region == "" {
		return o, errors.New("invalid arguments: supply one project and --region <id>")
	}
	o.Project = pos[0]
	if o.Mode != "cold" && o.Mode != "warm" {
		return o, errors.New("invalid arguments: --mode must be cold or warm")
	}
	if o.At != "" {
		if _, err := time.Parse(time.RFC3339, o.At); err != nil {
			return o, errors.New("invalid arguments: --at must be an RFC3339 timestamp with a timezone")
		}
	}
	if o.MaxFreeze != 0 && (o.MaxFreeze < time.Minute || o.MaxFreeze > 24*time.Hour || o.MaxFreeze%time.Second != 0) {
		return o, errors.New("invalid arguments: --max-freeze must be 1m–24h in whole seconds")
	}
	if o.Timeout <= 0 {
		return o, errors.New("invalid arguments: --timeout must be positive")
	}
	if rehome && (o.At != "" || o.MaxFreeze != 0) {
		return o, errors.New("invalid arguments: rehome has no freeze window")
	}
	if !o.Yes && !rtInteractive() {
		return o, errors.New("invalid arguments: use --yes --confirm-name <name> in noninteractive mode")
	}
	return o, nil
}

func projectMigrateFlow(args []string, rehome bool) error {
	o, err := parseMigrationOptions(args, rehome)
	if err != nil {
		return err
	}
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}
	target, err := resolveMoveProject(token, o.Project)
	if err != nil {
		return err
	}
	out := projectHumanOut(o.JSON)
	if !rehome {
		var preview projectMigrationPreview
		_, err = projectMovesCall(http.MethodGet, projectMovesProjectPath(target.ID, "/migration/preview?"+url.Values{"targetRegionId": {o.Region}, "mode": {o.Mode}}.Encode()), token, nil, &preview)
		if err != nil {
			return projectMovesAPIError(err, "/projects/"+target.ID)
		}
		estimate := preview.Estimates.Warm
		if o.Mode == "cold" {
			estimate = preview.Estimates.Cold
		}
		_, _ = fmt.Fprintf(out, "Move %s to %s (%s). Freeze estimate: %s–%s. Fee: $%.2f.\n", target.Name, o.Region, o.Mode, time.Duration(estimate.LowSeconds)*time.Second, time.Duration(estimate.HighSeconds)*time.Second, float64(preview.FeeMicroUSD)/1e6)
		printMoveIssues(out, "Warnings", preview.Warnings)
		for _, item := range preview.Checklist {
			_, _ = fmt.Fprintf(out, "- %s: %s\n", item.Title, item.Detail)
			if item.Copy != nil {
				_, _ = fmt.Fprintln(out, *item.Copy)
			}
		}
		if len(preview.DNSPlan) > 0 {
			_, _ = fmt.Fprintf(out, "DNS and connection plan: %s\n", preview.DNSPlan)
		}
		if !preview.CanMigrate {
			return moveIssuesError("This project cannot move yet", preview.Blockers)
		}
	}
	name := o.ConfirmName
	if !o.Yes {
		name, err = projectPromptLine(out, fmt.Sprintf("Type '%s' to confirm: ", target.Name))
		if err != nil {
			return err
		}
	}
	if name != target.Name {
		return errors.New("invalid arguments: the confirmation name does not match the project")
	}
	body := map[string]any{"targetRegionId": o.Region, "confirmName": name}
	suffix := "/rehome/create"
	if !rehome {
		suffix = "/migration/create"
		body["mode"] = o.Mode
		if o.At != "" {
			body["scheduledAt"] = o.At
		}
		if o.MaxFreeze != 0 {
			body["maxFreezeSeconds"] = int64(o.MaxFreeze / time.Second)
		}
	}
	var move projectMigrationView
	raw, err := projectMovesCall(http.MethodPost, projectMovesProjectPath(target.ID, suffix), token, body, &move)
	if err != nil {
		return projectMovesAPIError(err, "/projects/"+target.ID)
	}
	if o.Wait {
		move, raw, err = waitForProjectMigration(token, target.ID, move.ID, o.Timeout, o.JSON)
	}
	if o.JSON && len(raw) > 0 {
		if printErr := printProjectMovesRaw(raw); printErr != nil {
			return printErr
		}
	} else {
		printProjectMigration(move)
	}
	return err
}

func printProjectMigration(move projectMigrationView) {
	_, _ = fmt.Fprintf(rtOut, "%s: %s (%s)\n", move.ProjectName, strings.ReplaceAll(move.Status, "_", " "), move.ID)
	if move.Target.ID != "" {
		_, _ = fmt.Fprintf(rtOut, "Route: %s -> %s\n", regionLabel(move.Source), regionLabel(move.Target))
	}
	if line := migrationProgressLine(move); line != "" {
		_, _ = fmt.Fprintln(rtOut, line)
	} else if move.Job != nil {
		_, _ = fmt.Fprintln(rtOut, projectJobProgress(move.Job))
	}
	if move.BytesTotal > 0 {
		_, _ = fmt.Fprintf(rtOut, "Copied %s of %s.\n", formatMigrationBytes(move.BytesDone), formatMigrationBytes(move.BytesTotal))
	}
	if move.DowntimeSeconds != nil {
		_, _ = fmt.Fprintf(rtOut, "Downtime: %s\n", time.Duration(*move.DowntimeSeconds)*time.Second)
	}
	if text := migrationFailureText(move); text != "" {
		_, _ = fmt.Fprintln(rtOut, text)
	}
}

func regionLabel(r regionRefView) string {
	if r.Name != "" {
		return r.Name
	}
	return r.ID
}

func formatMigrationBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// migrationProgressLine is one line of live progress: overall percent, the
// current step with its detail and retries, and the ETA. Empty on engines
// without the progress fields.
func migrationProgressLine(move projectMigrationView) string {
	parts := []string{}
	if move.OverallProgress != nil {
		parts = append(parts, fmt.Sprintf("%3.0f%%", *move.OverallProgress*100))
	}
	if step := move.CurrentStep; step != nil && step.Label != "" {
		label := step.Label
		if step.Phase == "compensation" {
			label = "rolling back: " + label
		}
		if move.Job != nil && move.Job.StepCount > 0 {
			label = fmt.Sprintf("step %d/%d %s", move.Job.StepIndex+1, move.Job.StepCount, label)
		}
		if step.Detail != nil && *step.Detail != "" {
			label += " (" + *step.Detail + ")"
		}
		if step.Retries > 0 {
			label += fmt.Sprintf(" [retry %d]", step.Retries)
		}
		if step.Stalled {
			label += " [stalled]"
		}
		parts = append(parts, label)
	}
	if move.OverallEtaSeconds != nil && *move.OverallEtaSeconds > 0 {
		parts = append(parts, "ETA "+(time.Duration(*move.OverallEtaSeconds)*time.Second).String())
	}
	if move.DecisionPending {
		parts = append(parts, "waiting for your decision")
	}
	return strings.Join(parts, " · ")
}

// migrationFailureText explains a failed move: which step, why, what to do.
func migrationFailureText(move projectMigrationView) string {
	if move.FailureMessage == nil && move.FailureFix == nil && move.FailedStep == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("Failed")
	if move.FailedStep != nil && move.FailedStep.Label != "" {
		b.WriteString(" at: " + move.FailedStep.Label)
	}
	if move.FailureMessage != nil {
		b.WriteString("\nReason: " + *move.FailureMessage)
	}
	if move.FailureCode != nil && *move.FailureCode != "" {
		b.WriteString(" (" + *move.FailureCode + ")")
	}
	if move.Outcome != nil {
		switch *move.Outcome {
		case "stayed_in_source":
			b.WriteString("\nYour project still runs in " + regionLabel(move.Source) + ".")
		case "needs_operator":
			b.WriteString("\nHubfly support has been alerted; no action is needed from you.")
		}
	}
	if move.FailureFix != nil && *move.FailureFix != "" {
		b.WriteString("\nFix: " + *move.FailureFix)
	}
	if move.Retryable != nil && *move.Retryable {
		b.WriteString("\nRetry with: hubfly project migration retry " + move.ProjectName)
	}
	return b.String()
}

func migrationTerminalError(move projectMigrationView) (bool, error) {
	switch move.Status {
	case "completed":
		return true, nil
	case "failed", "rolled_back", "canceled", "needs_attention":
		message := strings.ReplaceAll(move.Status, "_", " ")
		if move.FailureMessage != nil {
			message = *move.FailureMessage
		}
		if move.FailureFix != nil && *move.FailureFix != "" {
			message += ". " + *move.FailureFix
		}
		if move.Retryable != nil && *move.Retryable {
			message += "\nRetry with: hubfly project migration retry " + move.ProjectName
		}
		return true, fmt.Errorf("migration %s %s: %s", move.ID, strings.ReplaceAll(move.Status, "_", " "), message)
	case "awaiting_decision":
		return true, errors.New("the move is waiting for your decision: use hubfly project migration continue freeze-now|keep-syncing|cancel " + move.ProjectName)
	}
	return false, nil
}

// waitForProjectMigration polls the move until it ends, printing a progress
// line to stderr whenever it changes (a carriage-return line on a terminal).
// Transient API or network errors are retried with backoff, so a dropped
// connection or laptop sleep does not end the watch; the move itself keeps
// running server-side either way.
func waitForProjectMigration(token, projectID, migrationID string, timeout time.Duration, quiet bool) (projectMigrationView, json.RawMessage, error) {
	deadline := time.Now().Add(timeout)
	interval := rtPollInterval
	if interval <= 0 || interval > 5*time.Second {
		interval = 5 * time.Second
	}
	live := !quiet && rtStderrIsTerminal()
	lastLine := ""
	failures := 0
	var last projectMigrationView
	var lastRaw json.RawMessage
	defer func() {
		if live && lastLine != "" {
			_, _ = fmt.Fprintln(rtErr)
		}
	}()
	for {
		var move projectMigrationView
		raw, err := projectMovesCall(http.MethodGet, projectMovesProjectPath(projectID, "/migration"), token, nil, &move)
		if err != nil {
			var apiErr *apiError
			permanent := errors.As(err, &apiErr) && apiErr.Status >= 400 && apiErr.Status < 500 && apiErr.Status != 408 && apiErr.Status != 429
			failures++
			if permanent || failures > 30 || time.Now().After(deadline) {
				return last, lastRaw, fmt.Errorf("%w\nThe migration keeps running. Resume watching with: hubfly project migration watch <project>", projectMovesAPIError(err, "/projects/"+projectID))
			}
			backoff := min(time.Duration(failures)*interval, time.Minute)
			if !quiet {
				if live && lastLine != "" {
					_, _ = fmt.Fprintln(rtErr)
					lastLine = ""
				}
				_, _ = fmt.Fprintf(rtErr, "Connection problem (%v); retrying in %s...\n", err, backoff)
			}
			time.Sleep(backoff)
			continue
		}
		failures = 0
		last, lastRaw = move, raw
		if migrationID == "" {
			migrationID = move.ID
		}
		if move.ID != migrationID {
			return move, raw, errors.New("the current migration changed; inspect project history")
		}
		if !quiet {
			line := migrationProgressLine(move)
			if line == "" {
				line = fmt.Sprintf("%s · %s", strings.ReplaceAll(move.Status, "_", " "), projectJobProgress(move.Job))
				if move.BytesTotal > 0 {
					line += fmt.Sprintf(" · %s/%s", formatMigrationBytes(move.BytesDone), formatMigrationBytes(move.BytesTotal))
				}
			}
			if line != lastLine {
				if live {
					_, _ = fmt.Fprintf(rtErr, "\r\033[2K%s", line)
				} else {
					_, _ = fmt.Fprintln(rtErr, line)
				}
				lastLine = line
			}
		}
		if done, terminalErr := migrationTerminalError(move); done {
			return move, raw, terminalErr
		}
		if time.Now().After(deadline) {
			return move, raw, errors.New("timed out waiting; the migration keeps running. Resume with: hubfly project migration watch " + move.ProjectName)
		}
		time.Sleep(interval)
	}
}

const projectMigrationGroupUsage = "usage: hubfly project migration status|watch|retry|cancel|continue|move-back|dns|attestation <project> [--wait] [--json]"

var projectMigrationActions = []string{"status", "watch", "retry", "cancel", "continue", "move-back", "dns", "attestation"}

func runProjectMigrationGroup(args []string) error {
	if len(args) == 0 {
		return errors.New(projectMigrationGroupUsage)
	}
	action := args[0]
	rest := args[1:]
	if action == "retry" {
		return projectMigrationRetry(rest)
	}
	if action == "watch" {
		// watch = status --wait: re-attach to a running move after a
		// disconnect, closed terminal or timeout.
		action, rest = "status", append([]string{"--wait"}, rest...)
	}
	decision := ""
	if action == "continue" {
		if len(rest) == 0 {
			return errors.New("supply freeze-now, keep-syncing or cancel and a project")
		}
		decision = strings.ReplaceAll(rest[0], "-", "_")
		rest = rest[1:]
		if decision != "freeze_now" && decision != "keep_syncing" && decision != "cancel" {
			return errors.New("invalid decision: use freeze-now, keep-syncing or cancel")
		}
	}
	switch action {
	case "status", "cancel", "continue", "move-back", "dns", "attestation":
	default:
		return unknownCommandError("project migration", action, projectMigrationActions)
	}
	fs := newProjectFlags("migration " + action)
	asJSON := fs.Bool("json", false, "")
	wait := fs.Bool("wait", false, "")
	yes := projectYesFlag(fs)
	name := fs.String("confirm-name", "", "")
	migrationID := fs.String("migration-id", "", "")
	timeout := fs.Duration("timeout", 30*time.Minute, "")
	pos, err := parseRealtimeArgs(fs, rest, "hubfly project migration "+action+" <project>")
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("supply one project")
	}
	if *timeout <= 0 {
		return errors.New("invalid arguments: --timeout must be positive")
	}
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}
	target, err := resolveMoveProject(token, pos[0])
	if err != nil {
		return err
	}
	write := action == "cancel" || action == "continue" || action == "move-back"
	body := map[string]any{}
	method := http.MethodGet
	suffix := "/migration"
	if write {
		if !*yes && !rtInteractive() {
			return errors.New("invalid arguments: pass --yes to confirm this action")
		}
		if !*yes {
			ok, confirmErr := projectConfirmYes(projectHumanOut(*asJSON), action+" the region move?")
			if confirmErr != nil {
				return confirmErr
			}
			if !ok {
				return errors.New("canceled")
			}
		}
		method = http.MethodPost
		suffix += "/" + action
	}
	if action == "continue" {
		body["decision"] = decision
	}
	if action == "move-back" {
		typed := *name
		if !*yes {
			typed, err = projectPromptLine(projectHumanOut(*asJSON), "Type the project name to move back: ")
			if err != nil {
				return err
			}
		}
		if typed != target.Name {
			return errors.New("invalid arguments: --confirm-name must match the project name")
		}
		body["confirmName"] = typed
	}
	if action == "dns" {
		suffix += "/dns"
	}
	if action == "attestation" {
		if *migrationID == "" {
			var move projectMigrationView
			_, err = projectMovesCall(http.MethodGet, projectMovesProjectPath(target.ID, "/migration"), token, nil, &move)
			if err != nil {
				return err
			}
			if move.ID == "" {
				return fmt.Errorf("project '%s' has no region move", target.Name)
			}
			*migrationID = move.ID
		}
		suffix += "/attestation?" + url.Values{"migrationId": {*migrationID}}.Encode()
	}
	var result projectMigrationView
	raw, err := projectMovesCall(method, projectMovesProjectPath(target.ID, suffix), token, body, &result)
	if err != nil {
		return projectMovesAPIError(err, "/projects/"+target.ID)
	}
	if *wait && action != "dns" && action != "attestation" {
		if result.ID == "" {
			return fmt.Errorf("project '%s' has no region move to watch. Start one with: hubfly project migrate %s --region <id>", target.Name, target.Name)
		}
		if action == "status" && !*asJSON {
			_, _ = fmt.Fprintf(rtErr, "Watching %s (Ctrl+C stops watching; the move continues).\n", result.ID)
		}
		result, raw, err = waitForProjectMigration(token, target.ID, result.ID, *timeout, *asJSON)
	}
	if *asJSON || action == "dns" || action == "attestation" {
		if printErr := printProjectMovesRaw(raw); printErr != nil {
			return printErr
		}
	} else if result.ID == "" {
		_, _ = fmt.Fprintf(rtOut, "Project '%s' has no region move.\n", target.Name)
	} else {
		printProjectMigration(result)
	}
	return err
}

// projectMigrationRetry starts a new move to the region and mode of the
// project's last move, after that move failed, rolled back or was canceled.
// Extra flags (--yes, --confirm-name, --wait, --json, --timeout, --at,
// --max-freeze, --mode) pass through to migrate.
func projectMigrationRetry(args []string) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return errors.New("usage: hubfly project migration retry <project> [--wait] [--yes --confirm-name <name>] [--json]")
	}
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}
	target, err := resolveMoveProject(token, args[0])
	if err != nil {
		return err
	}
	var last projectMigrationView
	if _, err := projectMovesCall(http.MethodGet, projectMovesProjectPath(target.ID, "/migration"), token, nil, &last); err != nil {
		return projectMovesAPIError(err, "/projects/"+target.ID)
	}
	if last.ID == "" || last.Target.ID == "" {
		return fmt.Errorf("project '%s' has no earlier region move to retry. Start one with: hubfly project migrate %s --region <id>", target.Name, target.Name)
	}
	switch last.Status {
	case "failed", "rolled_back", "canceled":
	case "completed":
		return fmt.Errorf("the last move of '%s' completed; nothing to retry", target.Name)
	default:
		return fmt.Errorf("the move %s is still %s; watch it with: hubfly project migration watch %s", last.ID, strings.ReplaceAll(last.Status, "_", " "), target.Name)
	}
	if last.Retryable != nil && !*last.Retryable {
		msg := "the last move failed for a reason a retry will not fix"
		if last.FailureFix != nil && *last.FailureFix != "" {
			msg += ": " + *last.FailureFix
		}
		return errors.New(msg)
	}
	mode := last.Mode
	if mode != "cold" && mode != "warm" {
		mode = "warm"
	}
	forwarded := []string{target.Name, "--region", last.Target.ID, "--mode", mode}
	forwarded = append(forwarded, args[1:]...)
	_, _ = fmt.Fprintf(rtErr, "Retrying the move of %s to %s (%s).\n", target.Name, regionLabel(last.Target), mode)
	return projectMigrateFlow(forwarded, last.Kind == "rehome")
}

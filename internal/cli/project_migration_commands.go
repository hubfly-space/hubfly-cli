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
	BytesTotal      int64           `json:"bytesTotal"`
	BytesDone       int64           `json:"bytesDone"`
	DecisionPending bool            `json:"decisionPending"`
	CanCancel       bool            `json:"canCancel"`
	Job             *projectJobView `json:"job"`
	FailureMessage  *string         `json:"failureMessage"`
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
	if move.BytesTotal > 0 {
		_, _ = fmt.Fprintf(rtOut, "Copied %d of %d bytes.\n", move.BytesDone, move.BytesTotal)
	}
	if move.Job != nil {
		_, _ = fmt.Fprintln(rtOut, projectJobProgress(move.Job))
	}
	if move.FailureMessage != nil {
		_, _ = fmt.Fprintln(rtOut, *move.FailureMessage)
	}
}

func migrationTerminalError(move projectMigrationView) (bool, error) {
	switch move.Status {
	case "completed":
		return true, nil
	case "failed", "rolled_back", "canceled", "needs_attention":
		message := move.Status
		if move.FailureMessage != nil {
			message = *move.FailureMessage
		}
		return true, fmt.Errorf("migration %s: %s", move.ID, message)
	case "awaiting_decision":
		return true, errors.New("the move is waiting for your decision: use project migration continue freeze-now|keep-syncing|cancel <project>")
	}
	return false, nil
}

func waitForProjectMigration(token, projectID, migrationID string, timeout time.Duration, quiet bool) (projectMigrationView, json.RawMessage, error) {
	deadline := time.Now().Add(timeout)
	for {
		var move projectMigrationView
		raw, err := projectMovesCall(http.MethodGet, projectMovesProjectPath(projectID, "/migration"), token, nil, &move)
		if err != nil {
			return move, raw, projectMovesAPIError(err, "/projects/"+projectID)
		}
		if move.ID != migrationID {
			return move, raw, errors.New("the current migration changed; inspect project history")
		}
		if done, terminalErr := migrationTerminalError(move); done {
			return move, raw, terminalErr
		}
		if !quiet {
			_, _ = fmt.Fprintf(rtErr, "%s: %s · %d/%d bytes\n", move.ID, move.Status, move.BytesDone, move.BytesTotal)
		}
		if time.Now().After(deadline) {
			return move, raw, errors.New("timed out waiting; the migration keeps running. Use project migration status <project>")
		}
		time.Sleep(5 * time.Second)
	}
}

func runProjectMigrationGroup(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: hubfly project migration status|cancel|continue|move-back|dns|attestation <project>")
	}
	action := args[0]
	rest := args[1:]
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
		return fmt.Errorf("unknown project migration command %q", action)
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

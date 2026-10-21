package cli

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
)

// `hubfly db cert|sessions|insights|config`: the instance CA certificate,
// live sessions, top queries and engine configuration.

// ---------------------------------------------------------------------------
// cert
// ---------------------------------------------------------------------------

func dbCertFlow(args []string) error {
	const usage = "hubfly db cert <db> [--output <file>] [--project <id|name>]"
	fs := newRealtimeFlags("db cert")
	projectRef := fs.String("project", "", "")
	output := fs.String("output", "", "")
	fs.StringVar(output, "o", "", "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return dbdUsageError(usage)
	}
	target, err := resolveDBDTarget(positionals[0], *projectRef)
	if err != nil {
		return err
	}
	if err := target.require("certificate"); err != nil {
		return err
	}
	cert, err := fetchDBDCertificate(target.token, target.instance.ProjectID, target.instance.ID)
	if err != nil {
		return target.apiError(err)
	}
	pem := cert.PEM
	if !strings.HasSuffix(pem, "\n") {
		pem += "\n"
	}
	path := strings.TrimSpace(*output)
	if path == "" || path == "-" {
		_, _ = fmt.Fprint(rtOut, pem)
		dbdCertHint(target, cert, "<file>")
		return nil
	}
	if err := os.WriteFile(path, []byte(pem), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	_, _ = fmt.Fprintf(rtOut, "Saved the CA certificate of '%s' to %s.\n", target.name(), path)
	dbdCertHint(target, cert, path)
	return nil
}

// dbdCertHint says how to verify the server with the certificate (stderr,
// so the PEM on stdout stays clean).
func dbdCertHint(target dbdTarget, cert dbdCertificate, path string) {
	if cert.FingerprintSHA256 != nil && *cert.FingerprintSHA256 != "" {
		_, _ = fmt.Fprintf(rtErr, "SHA-256 fingerprint: %s\n", *cert.FingerprintSHA256)
	}
	if cert.ExpiresAt != nil && *cert.ExpiresAt != "" {
		_, _ = fmt.Fprintf(rtErr, "Expires: %s\n", dbdTime(*cert.ExpiresAt))
	}
	switch target.engine() {
	case "postgresql":
		_, _ = fmt.Fprintf(rtErr, "Verify the server: add sslmode=verify-full&sslrootcert=%s to the connection URL.\n", path)
	case "mysql":
		_, _ = fmt.Fprintf(rtErr, "Verify the server: mysql --ssl-mode=VERIFY_IDENTITY --ssl-ca=%s ...\n", path)
	}
}

// ---------------------------------------------------------------------------
// sessions
// ---------------------------------------------------------------------------

func dbdSessionsListFlow(args []string) error {
	const usage = "hubfly db sessions [list] <db> [--project <id|name>] [--json]"
	fs := newRealtimeFlags("db sessions list")
	projectRef := fs.String("project", "", "")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return dbdUsageError(usage)
	}
	target, err := resolveDBDTarget(positionals[0], *projectRef)
	if err != nil {
		return err
	}
	if err := target.require("sessions"); err != nil {
		return err
	}
	sessions, err := fetchDBDSessions(target.token, target.instance.ProjectID, target.instance.ID)
	if err != nil {
		return target.apiError(err)
	}
	if *asJSON {
		return rtPrintJSON(sessions)
	}
	if len(sessions) == 0 {
		_, _ = fmt.Fprintf(rtOut, "No sessions on '%s'.\n", target.name())
		return nil
	}
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tUSER\tDATABASE\tCLIENT\tSTATE\tDURATION\tQUERY")
	platform := 0
	for _, session := range sessions {
		client := dbdText(session.ClientAddress, "-")
		if session.ViaProxy {
			client += " (via HubFly proxy)"
		}
		user := dbdText(session.User, "-")
		if session.Platform {
			user += " (HubFly)"
			platform++
		}
		duration := "-"
		if session.DurationMs != nil {
			duration = dbdDuration(*session.DurationMs)
		}
		state := dbdText(session.State, "-")
		if session.WaitEvent != nil && *session.WaitEvent != "" {
			state += " (" + *session.WaitEvent + ")"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", session.ID, user, dbdText(session.Database, "-"), client, state,
			duration, dbdOneLine(dbdText(session.Query, "-"), 60))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if platform > 0 {
		_, _ = fmt.Fprintln(rtOut, "Sessions marked (HubFly) belong to the platform (console, monitoring) and cannot be cancelled or terminated.")
	}
	return nil
}

func dbdSessionsSignalFlow(signal string, args []string) error {
	usage := fmt.Sprintf("hubfly db sessions %s <db> <sessionId> [--project <id|name>] [--yes]", signal)
	fs := newRealtimeFlags("db sessions " + signal)
	projectRef := fs.String("project", "", "")
	yes := fs.Bool("yes", false, "")
	fs.BoolVar(yes, "y", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 2 || strings.TrimSpace(positionals[1]) == "" {
		return dbdUsageError(usage)
	}
	sessionID := strings.TrimSpace(positionals[1])
	target, err := resolveDBDTarget(positionals[0], *projectRef)
	if err != nil {
		return err
	}
	if err := target.require("sessions"); err != nil {
		return err
	}
	sessions, err := fetchDBDSessions(target.token, target.instance.ProjectID, target.instance.ID)
	if err != nil {
		return target.apiError(err)
	}
	var session *dbdSession
	for index := range sessions {
		if sessions[index].ID == sessionID {
			session = &sessions[index]
		}
	}
	if session == nil {
		return fmt.Errorf("session %s not found on '%s'; it may have ended (list them with: hubfly db sessions %s)", sessionID, target.name(), target.name())
	}
	if session.Platform {
		return fmt.Errorf("session %s belongs to HubFly (console or monitoring) and cannot be %s", sessionID, map[string]string{"cancel": "cancelled", "terminate": "terminated"}[signal])
	}
	describe := fmt.Sprintf("session %s (%s on %s)", sessionID, dbdText(session.User, "unknown user"), dbdText(session.Database, "unknown database"))
	if signal == "terminate" {
		question := fmt.Sprintf("Terminate %s on '%s'? Its connection is closed and an open transaction is rolled back.", describe, target.name())
		confirmed, err := dbdConfirm(*yes, question, "terminate the session")
		if err != nil {
			return err
		}
		if !confirmed {
			_, _ = fmt.Fprintln(rtOut, "Cancelled.")
			return nil
		}
	}
	ok, err := signalDBDSession(target.token, target.instance.ProjectID, target.instance.ID, sessionID, signal)
	if err != nil {
		return target.apiError(err)
	}
	switch {
	case !ok:
		_, _ = fmt.Fprintf(rtOut, "%s had already ended or had nothing to %s.\n", describe, signal)
	case signal == "cancel":
		_, _ = fmt.Fprintf(rtOut, "Cancelled the running query of %s; the connection stays open.\n", describe)
	default:
		_, _ = fmt.Fprintf(rtOut, "Terminated %s.\n", describe)
	}
	return nil
}

// ---------------------------------------------------------------------------
// insights
// ---------------------------------------------------------------------------

func dbdInsightsFlow(args []string) error {
	const usage = "hubfly db insights <db> [--order-by total_time|mean_time|calls] [--limit <n>] [--project <id|name>] [--json]"
	fs := newRealtimeFlags("db insights")
	projectRef := fs.String("project", "", "")
	orderBy := fs.String("order-by", "total_time", "")
	limit := fs.Int("limit", 20, "")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return dbdUsageError(usage)
	}
	order, err := dbdInsightsOrder(*orderBy)
	if err != nil {
		return err
	}
	if *limit < 1 || *limit > 200 {
		return fmt.Errorf("invalid --limit %d (use 1-200)", *limit)
	}
	target, err := resolveDBDTarget(positionals[0], *projectRef)
	if err != nil {
		return err
	}
	if err := target.require("insights"); err != nil {
		return err
	}
	raw, err := fetchDBDInsightsRaw(target.token, target.instance.ProjectID, target.instance.ID, order, *limit)
	if err != nil {
		return target.apiError(err)
	}
	if *asJSON {
		return rtPrintJSON(raw)
	}
	var insights dbdInsights
	if err := dbdDecode(raw, &insights); err != nil {
		return fmt.Errorf("read the insights: %w", err)
	}
	if !insights.Available {
		source := "pg_stat_statements"
		if target.engine() == "mysql" {
			source = "the performance schema"
		}
		_, _ = fmt.Fprintf(rtOut, "Query insights are not available on '%s' right now (%s is not collecting statistics).\n", target.name(), source)
		return nil
	}
	if len(insights.Items) == 0 {
		_, _ = fmt.Fprintf(rtOut, "No query statistics on '%s' yet.\n", target.name())
		return nil
	}
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "CALLS\tTOTAL\tMEAN\tROWS\tDATABASE\tQUERY")
	for _, item := range insights.Items {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", formatThousands64(item.Calls), dbdMillis(item.TotalMs), dbdMillis(item.MeanMs),
			formatThousands64(item.Rows), dbdText(item.Database, "-"), dbdOneLine(item.Query, 70))
	}
	return tw.Flush()
}

func dbdInsightsOrder(value string) (string, error) {
	switch strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), "-", "_")) {
	case "total_time", "total", "":
		return "total_time", nil
	case "mean_time", "mean":
		return "mean_time", nil
	case "calls":
		return "calls", nil
	}
	return "", fmt.Errorf("invalid --order-by %q (use total_time, mean_time or calls)", value)
}

// dbdMillis renders a mean query time with sub-millisecond precision.
func dbdMillis(ms float64) string {
	if ms < 10 {
		return fmt.Sprintf("%.2f ms", ms)
	}
	return dbdDuration(ms)
}

func dbdInsightsResetFlow(args []string) error {
	const usage = "hubfly db insights reset <db> [--project <id|name>] [--yes]"
	fs := newRealtimeFlags("db insights reset")
	projectRef := fs.String("project", "", "")
	yes := fs.Bool("yes", false, "")
	fs.BoolVar(yes, "y", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return dbdUsageError(usage)
	}
	target, err := resolveDBDTarget(positionals[0], *projectRef)
	if err != nil {
		return err
	}
	if err := target.require("insights"); err != nil {
		return err
	}
	confirmed, err := dbdConfirm(*yes, fmt.Sprintf("Reset the query statistics of '%s'? Collection starts again from zero.", target.name()), "reset the statistics")
	if err != nil {
		return err
	}
	if !confirmed {
		_, _ = fmt.Fprintln(rtOut, "Cancelled.")
		return nil
	}
	if err := resetDBDInsights(target.token, target.instance.ProjectID, target.instance.ID); err != nil {
		return target.apiError(err)
	}
	_, _ = fmt.Fprintf(rtOut, "Reset the query statistics of '%s'.\n", target.name())
	return nil
}

// ---------------------------------------------------------------------------
// config
// ---------------------------------------------------------------------------

const dbdConfigTimeout = 15 * time.Minute

func dbdConfigShowFlow(args []string) error {
	const usage = "hubfly db config <db> [--all] [--project <id|name>] [--json]"
	fs := newRealtimeFlags("db config")
	projectRef := fs.String("project", "", "")
	all := fs.Bool("all", false, "")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return dbdUsageError(usage)
	}
	target, err := resolveDBDTarget(positionals[0], *projectRef)
	if err != nil {
		return err
	}
	if err := target.require("config"); err != nil {
		return err
	}
	raw, err := fetchDBDConfigRaw(target.token, target.instance.ProjectID, target.instance.ID)
	if err != nil {
		return target.apiError(err)
	}
	if *asJSON {
		return rtPrintJSON(raw)
	}
	var config dbdConfig
	if err := dbdDecode(raw, &config); err != nil {
		return fmt.Errorf("read the configuration: %w", err)
	}
	revision := "none (defaults)"
	if config.RevisionNumber != nil {
		revision = fmt.Sprintf("#%d", *config.RevisionNumber)
	}
	_, _ = fmt.Fprintf(rtOut, "Configuration of '%s' (%s), revision %s, generation %d\n", target.name(), target.engineLabel(), revision, config.Generation)
	if len(config.PendingRestart) > 0 {
		_, _ = fmt.Fprintf(rtOut, "Waiting for a restart: %s (run: hubfly db restart %s)\n", strings.Join(config.PendingRestart, ", "), target.name())
	}
	settings := []dbdConfigSetting{}
	for _, setting := range config.Settings {
		if *all || setting.Editable || setting.Modified {
			settings = append(settings, setting)
		}
	}
	sort.SliceStable(settings, func(a, b int) bool {
		if settings[a].Category != settings[b].Category {
			return settings[a].Category < settings[b].Category
		}
		return settings[a].Key < settings[b].Key
	})
	_, _ = fmt.Fprintln(rtOut)
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "CATEGORY\tKEY\tVALUE\tDEFAULT\tNOTES")
	for _, setting := range settings {
		notes := []string{}
		if setting.Modified {
			notes = append(notes, "modified")
		}
		if setting.RestartRequired {
			notes = append(notes, "restart")
		}
		if setting.Locked || !setting.Editable {
			notes = append(notes, "locked")
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", setting.Category, setting.Key, dbdConfigValue(setting.Value, setting.Unit),
			dbdConfigValue(setting.DefaultValue, setting.Unit), strings.Join(notes, ", "))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if !*all {
		_, _ = fmt.Fprintln(rtOut, "\nShowing editable and modified settings; add --all for every setting. Change them with: hubfly db config set", target.name(), "<key>=<value>")
	}
	return nil
}

func dbdConfigValue(value, unit *string) string {
	if value == nil {
		return "-"
	}
	text := dbdOneLine(*value, 40)
	if text == "" {
		text = `""`
	}
	if unit != nil && *unit != "" && !strings.ContainsAny(text, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ") {
		text += " " + *unit
	}
	return text
}

func dbdParseRestart(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "later":
		return "later", nil
	case "now":
		return "now", nil
	}
	return "", fmt.Errorf("invalid --restart %q (use now or later)", value)
}

// dbdParseConfigChanges reads key=value pairs. An empty value (key=) removes
// the override so the setting returns to its default.
func dbdParseConfigChanges(pairs []string) (map[string]*string, error) {
	changes := map[string]*string{}
	for _, pair := range pairs {
		key, value, found := strings.Cut(pair, "=")
		key = strings.TrimSpace(key)
		if !found || key == "" {
			return nil, fmt.Errorf("invalid setting %q (use <key>=<value>; <key>= resets it to the default)", pair)
		}
		if _, duplicate := changes[key]; duplicate {
			return nil, fmt.Errorf("invalid arguments: %s is set twice", key)
		}
		value = strings.TrimSpace(value)
		if value == "" {
			changes[key] = nil
			continue
		}
		changes[key] = &value
	}
	return changes, nil
}

func dbdDiffValue(value *string) string {
	if value == nil {
		return "(default)"
	}
	return dbdOneLine(*value, 40)
}

func writeDBDConfigDiff(diff []dbdConfigDiff) {
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	for _, entry := range diff {
		restart := ""
		if entry.RestartRequired {
			restart = "(needs a restart)"
		}
		_, _ = fmt.Fprintf(tw, "  %s\t%s\t->\t%s\t%s\n", entry.Key, dbdDiffValue(entry.From), dbdDiffValue(entry.To), restart)
	}
	_ = tw.Flush()
}

func writeDBDConfigProblems(label string, problems []dbdConfigProblem) {
	for _, problem := range problems {
		where := ""
		if problem.Key != nil && *problem.Key != "" {
			where = *problem.Key + ": "
		}
		_, _ = fmt.Fprintf(rtErr, "%s: %s%s\n", label, where, problem.Message)
	}
}

func dbdConfigSetFlow(args []string) error {
	const usage = "hubfly db config set <db> <key>=<value>... [--restart now|later] [--note <text>] [--project <id|name>] [--yes]"
	fs := newRealtimeFlags("db config set")
	projectRef := fs.String("project", "", "")
	restartText := fs.String("restart", "", "")
	note := fs.String("note", "", "")
	yes := fs.Bool("yes", false, "")
	fs.BoolVar(yes, "y", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) < 2 {
		return dbdUsageError(usage)
	}
	restart, err := dbdParseRestart(*restartText)
	if err != nil {
		return err
	}
	changes, err := dbdParseConfigChanges(positionals[1:])
	if err != nil {
		return err
	}
	target, err := resolveDBDTarget(positionals[0], *projectRef)
	if err != nil {
		return err
	}
	if err := target.require("config"); err != nil {
		return err
	}
	raw, err := fetchDBDConfigRaw(target.token, target.instance.ProjectID, target.instance.ID)
	if err != nil {
		return target.apiError(err)
	}
	var current dbdConfig
	if err := dbdDecode(raw, &current); err != nil {
		return fmt.Errorf("read the configuration: %w", err)
	}
	validation, err := validateDBDConfig(target.token, target.instance.ProjectID, target.instance.ID, changes)
	if err != nil {
		return target.apiError(err)
	}
	writeDBDConfigProblems("Warning", validation.Warnings)
	if !validation.Valid {
		writeDBDConfigProblems("Error", validation.Errors)
		return errors.New("the configuration is not valid; nothing was changed")
	}
	if len(validation.Diff) == 0 {
		_, _ = fmt.Fprintln(rtOut, "Nothing to change: the settings already have these values.")
		return nil
	}
	_, _ = fmt.Fprintf(rtOut, "Changes to '%s':\n", target.name())
	writeDBDConfigDiff(validation.Diff)
	question := fmt.Sprintf("Apply %s to '%s'?", dbdPlural(int64(len(validation.Diff)), "change", "changes"), target.name())
	if len(validation.RestartRequired) > 0 {
		if restart == "now" {
			question += fmt.Sprintf(" The database restarts now for %s (connections drop briefly).", strings.Join(validation.RestartRequired, ", "))
		} else {
			question += fmt.Sprintf(" Changes to %s take effect at the next restart (or use --restart now).", strings.Join(validation.RestartRequired, ", "))
		}
	}
	confirmed, err := dbdConfirm(*yes, question, "apply the configuration")
	if err != nil {
		return err
	}
	if !confirmed {
		_, _ = fmt.Fprintln(rtOut, "Cancelled.")
		return nil
	}
	body := map[string]any{"generation": current.Generation, "changes": changes, "restart": restart}
	if strings.TrimSpace(*note) != "" {
		body["note"] = strings.TrimSpace(*note)
	}
	applied, err := applyDBDConfig(target.token, target.instance.ProjectID, target.instance.ID, body)
	if err != nil {
		return target.apiError(err)
	}
	return dbdFinishConfigChange(target, applied, restart, fmt.Sprintf("Applied revision #%d to '%s'.", applied.RevisionNumber, target.name()))
}

// dbdFinishConfigChange follows the config_apply operation and reports what
// still waits for a restart.
func dbdFinishConfigChange(target dbdTarget, applied dbdConfigApplyResult, restart, done string) error {
	if _, err := waitForManagedOperation(target.token, target.instance, applied.OperationID, "Applying the configuration", dbdConfigTimeout, false, false); err != nil {
		return target.apiError(err)
	}
	_, _ = fmt.Fprintln(rtOut, done)
	if restart == "later" && len(applied.RestartRequired) > 0 {
		_, _ = fmt.Fprintf(rtOut, "Waiting for a restart: %s (run: hubfly db restart %s)\n", strings.Join(applied.RestartRequired, ", "), target.name())
	}
	return nil
}

func dbdConfigHistoryFlow(args []string) error {
	const usage = "hubfly db config history <db> [--limit <n>] [--project <id|name>] [--json]"
	fs := newRealtimeFlags("db config history")
	projectRef := fs.String("project", "", "")
	limit := fs.Int("limit", 20, "")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return dbdUsageError(usage)
	}
	if *limit < 1 || *limit > 200 {
		return fmt.Errorf("invalid --limit %d (use 1-200)", *limit)
	}
	target, err := resolveDBDTarget(positionals[0], *projectRef)
	if err != nil {
		return err
	}
	if err := target.require("config"); err != nil {
		return err
	}
	raw, err := fetchDBDConfigHistoryRaw(target.token, target.instance.ProjectID, target.instance.ID, *limit)
	if err != nil {
		return target.apiError(err)
	}
	if *asJSON {
		return rtPrintJSON(raw)
	}
	var history struct {
		Items []dbdConfigRevision `json:"items"`
	}
	if err := dbdDecode(raw, &history); err != nil {
		return fmt.Errorf("read the configuration history: %w", err)
	}
	if len(history.Items) == 0 {
		_, _ = fmt.Fprintf(rtOut, "No configuration changes on '%s' yet.\n", target.name())
		return nil
	}
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "#\tID\tSTATE\tCREATED\tBY\tCHANGES\tNOTE")
	for _, revision := range history.Items {
		changes := make([]string, 0, len(revision.Diff))
		for _, entry := range revision.Diff {
			changes = append(changes, fmt.Sprintf("%s=%s", entry.Key, dbdDiffValue(entry.To)))
		}
		actor := "-"
		if revision.Actor != nil {
			actor = dbdText(revision.Actor.Name, revision.Actor.ID)
		}
		_, _ = fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n", revision.Number, revision.ID, strings.ReplaceAll(revision.State, "_", " "),
			dbdTime(revision.CreatedAt), actor, dbdOneLine(strings.Join(changes, ", "), 60), dbdOneLine(dbdText(revision.Note, ""), 40))
	}
	return tw.Flush()
}

func dbdConfigRevertFlow(args []string) error {
	const usage = "hubfly db config revert <db> <revisionId> [--restart now|later] [--project <id|name>] [--yes]"
	fs := newRealtimeFlags("db config revert")
	projectRef := fs.String("project", "", "")
	restartText := fs.String("restart", "", "")
	yes := fs.Bool("yes", false, "")
	fs.BoolVar(yes, "y", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 2 || strings.TrimSpace(positionals[1]) == "" {
		return dbdUsageError(usage)
	}
	restart, err := dbdParseRestart(*restartText)
	if err != nil {
		return err
	}
	target, err := resolveDBDTarget(positionals[0], *projectRef)
	if err != nil {
		return err
	}
	if err := target.require("config"); err != nil {
		return err
	}
	raw, err := fetchDBDConfigRaw(target.token, target.instance.ProjectID, target.instance.ID)
	if err != nil {
		return target.apiError(err)
	}
	var current dbdConfig
	if err := dbdDecode(raw, &current); err != nil {
		return fmt.Errorf("read the configuration: %w", err)
	}
	revisionID := strings.TrimSpace(positionals[1])
	// Accept "#3" or "3" for the revision number as shown by config history.
	if number := strings.TrimPrefix(revisionID, "#"); number != "" && strings.Trim(number, "0123456789") == "" {
		resolved, err := dbdFindRevisionByNumber(target, number)
		if err != nil {
			return err
		}
		revisionID = resolved
	}
	question := fmt.Sprintf("Restore the configuration of '%s' to revision %s? This is recorded as a new revision.", target.name(), positionals[1])
	if restart == "now" {
		question += " The database restarts now if a restored setting needs it."
	}
	confirmed, err := dbdConfirm(*yes, question, "revert the configuration")
	if err != nil {
		return err
	}
	if !confirmed {
		_, _ = fmt.Fprintln(rtOut, "Cancelled.")
		return nil
	}
	applied, err := revertDBDConfig(target.token, target.instance.ProjectID, target.instance.ID, map[string]any{
		"revisionId": revisionID, "generation": current.Generation, "restart": restart,
	})
	if err != nil {
		return target.apiError(err)
	}
	return dbdFinishConfigChange(target, applied, restart, fmt.Sprintf("Restored revision %s of '%s' as revision #%d.", positionals[1], target.name(), applied.RevisionNumber))
}

func dbdFindRevisionByNumber(target dbdTarget, number string) (string, error) {
	raw, err := fetchDBDConfigHistoryRaw(target.token, target.instance.ProjectID, target.instance.ID, 200)
	if err != nil {
		return "", target.apiError(err)
	}
	var history struct {
		Items []dbdConfigRevision `json:"items"`
	}
	if err := dbdDecode(raw, &history); err != nil {
		return "", fmt.Errorf("read the configuration history: %w", err)
	}
	for _, revision := range history.Items {
		if fmt.Sprint(revision.Number) == strings.TrimLeft(number, "0") {
			return revision.ID, nil
		}
	}
	return "", fmt.Errorf("revision #%s not found on '%s' (list them with: hubfly db config history %s)", number, target.name(), target.name())
}

package cli

import (
	"errors"
	"flag"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

// ---------------------------------------------------------------------------
// App settings flags (apps create/update)
// ---------------------------------------------------------------------------

type realtimeSettingsFlags struct {
	origins           rtOptString
	clientEvents      rtOptBool
	subscriptionCount rtOptBool
	requireAuth       rtOptBool
	offlineQueue      rtOptBool
	payloadPreview    rtOptBool
	encryption        rtOptString
	history           rtOptString
	historyTTL        rtOptString
	historyMax        rtOptString
	maxConnections    rtOptString
	maxMessageBytes   rtOptString
}

func registerRealtimeSettingsFlags(fs *flag.FlagSet) *realtimeSettingsFlags {
	f := &realtimeSettingsFlags{}
	fs.Var(&f.origins, "origins", "")
	fs.Var(&f.clientEvents, "client-events", "")
	fs.Var(&f.subscriptionCount, "subscription-count", "")
	fs.Var(&f.requireAuth, "require-auth", "")
	fs.Var(&f.offlineQueue, "offline-queue", "")
	fs.Var(&f.payloadPreview, "payload-preview", "")
	fs.Var(&f.encryption, "encryption", "")
	fs.Var(&f.history, "history", "")
	fs.Var(&f.historyTTL, "history-ttl", "")
	fs.Var(&f.historyMax, "history-max", "")
	fs.Var(&f.maxConnections, "max-connections", "")
	fs.Var(&f.maxMessageBytes, "max-message-bytes", "")
	return f
}

func parseRealtimePositiveInt(name, value string) (int, error) {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed < 0 {
		return 0, fmt.Errorf("invalid --%s %q: expected a whole number", name, value)
	}
	return parsed, nil
}

// build returns the settings the user changed, in the Dashboard's input shape
// (the Dashboard merges them into the stored settings).
func (f *realtimeSettingsFlags) build() (map[string]any, error) {
	settings := map[string]any{}
	if f.origins.set {
		origins := []string{}
		for _, origin := range strings.Split(f.origins.value, ",") {
			if origin = strings.TrimSpace(origin); origin != "" {
				origins = append(origins, origin)
			}
		}
		settings["allowedOrigins"] = origins
	}
	if f.clientEvents.set {
		settings["clientEvents"] = f.clientEvents.value
	}
	if f.subscriptionCount.set {
		settings["subscriptionCountEvents"] = f.subscriptionCount.value
	}
	if f.requireAuth.set {
		settings["requireUserAuthentication"] = f.requireAuth.value
	}
	if f.payloadPreview.set {
		settings["inspectorPayloadPreview"] = f.payloadPreview.value
	}
	if f.offlineQueue.set {
		settings["offlineQueue"] = map[string]any{"enabled": f.offlineQueue.value}
	}
	if f.encryption.set {
		value := strings.ToLower(strings.TrimSpace(f.encryption.value))
		if value != "off" && value != "optional" && value != "required" {
			return nil, fmt.Errorf("invalid --encryption %q: expected off, optional or required", f.encryption.value)
		}
		settings["encryption"] = value
	}
	history := map[string]any{}
	if f.history.set {
		value := strings.ToLower(strings.TrimSpace(f.history.value))
		if value != "off" && value != "memory" && value != "durable" {
			return nil, fmt.Errorf("invalid --history %q: expected off, memory or durable", f.history.value)
		}
		history["mode"] = value
	}
	if f.historyTTL.set {
		seconds, err := parseRealtimeDuration(f.historyTTL.value)
		if err != nil {
			return nil, fmt.Errorf("invalid --history-ttl: %v", err)
		}
		history["ttlSeconds"] = seconds
	}
	if f.historyMax.set {
		value, err := parseRealtimePositiveInt("history-max", f.historyMax.value)
		if err != nil {
			return nil, err
		}
		history["maxMessages"] = value
	}
	if len(history) > 0 {
		settings["history"] = history
	}
	if f.maxConnections.set {
		value, err := parseRealtimePositiveInt("max-connections", f.maxConnections.value)
		if err != nil {
			return nil, err
		}
		settings["maxConnections"] = value
	}
	if f.maxMessageBytes.set {
		value, err := parseRealtimePositiveInt("max-message-bytes", f.maxMessageBytes.value)
		if err != nil {
			return nil, err
		}
		settings["maxMessageBytes"] = value
	}
	return settings, nil
}

func describeRealtimeSync(sync rtSync) string {
	if sync.Status == "applied" {
		return "applied to the server"
	}
	text := "pending (applies when the server is running)"
	if sync.Error != nil && *sync.Error != "" {
		text += ": " + *sync.Error
	}
	return text
}

// realtimeTarget is a resolved server plus the auth token.
type realtimeTarget struct {
	token    string
	instance rtInstance
}

func resolveRealtimeTarget(serverRef, projectRef string) (realtimeTarget, error) {
	token, err := ensureAuth(true)
	if err != nil {
		return realtimeTarget{}, err
	}
	instance, err := resolveRealtimeInstance(token, serverRef, projectRef)
	if err != nil {
		return realtimeTarget{}, err
	}
	return realtimeTarget{token: token, instance: instance}, nil
}

// ---------------------------------------------------------------------------
// apps
// ---------------------------------------------------------------------------

func runRealtimeAppsGroup(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: hubfly realtime apps <list|create|update|delete> <server> ...")
	}
	switch args[0] {
	case "list", "ls":
		return realtimeAppsListFlow(args[1:])
	case "create":
		return realtimeAppsCreateFlow(args[1:])
	case "update", "set":
		return realtimeAppsUpdateFlow(args[1:])
	case "delete", "rm":
		return realtimeAppsDeleteFlow(args[1:])
	default:
		return realtimeAppsListFlow(args)
	}
}

func realtimeAppsListFlow(args []string) error {
	const usage = "hubfly realtime apps list <server> [--project <id|name>] [--json]"
	fs := newRealtimeFlags("apps list")
	projectRef := fs.String("project", "", "")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return fmt.Errorf("usage: %s", usage)
	}
	target, err := resolveRealtimeTarget(positionals[0], *projectRef)
	if err != nil {
		return err
	}
	apps, err := fetchRealtimeApps(target.token, target.instance.ProjectID, target.instance.ID)
	if err != nil {
		return realtimeAPIError(err)
	}
	if *asJSON {
		return rtPrintJSON(apps)
	}
	if len(apps) == 0 {
		_, _ = fmt.Fprintf(rtOut, "No apps on realtime server '%s'.\n", target.instance.Name)
		return nil
	}
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "APP ID\tNAME\tENABLED\tENCRYPTION\tHISTORY\tCLIENT EVENTS\tACTIVE KEYS\tORIGINS")
	for _, app := range apps {
		active := 0
		for _, key := range app.Keys {
			if key.Status == "active" {
				active++
			}
		}
		origins := "any"
		if len(app.Settings.AllowedOrigins) > 0 {
			origins = strings.Join(app.Settings.AllowedOrigins, ",")
		}
		history := app.Settings.History.Mode
		if history != "" && history != "off" && app.Settings.History.TTLSeconds > 0 {
			history += " " + formatRealtimeSeconds(app.Settings.History.TTLSeconds)
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%t\t%s\t%s\t%t\t%d\t%s\n",
			app.AppID, app.Name, app.Enabled, app.Settings.Encryption, history,
			app.Settings.ClientEvents, active, origins)
	}
	return tw.Flush()
}

func realtimeAppsCreateFlow(args []string) error {
	const usage = "hubfly realtime apps create <server> <name> [settings] [--project <id|name>] [--reveal] [--json]"
	fs := newRealtimeFlags("apps create")
	projectRef := fs.String("project", "", "")
	name := fs.String("name", "", "")
	reveal := fs.Bool("reveal", false, "")
	asJSON := fs.Bool("json", false, "")
	settingsFlags := registerRealtimeSettingsFlags(fs)
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) == 2 && *name == "" {
		*name = positionals[1]
		positionals = positionals[:1]
	}
	if len(positionals) != 1 || strings.TrimSpace(*name) == "" {
		return fmt.Errorf("usage: %s", usage)
	}
	settings, err := settingsFlags.build()
	if err != nil {
		return err
	}
	target, err := resolveRealtimeTarget(positionals[0], *projectRef)
	if err != nil {
		return err
	}
	created, err := createRealtimeApp(target.token, target.instance.ProjectID, target.instance.ID, strings.TrimSpace(*name), settings)
	if err != nil {
		return realtimeAPIError(err)
	}
	if *asJSON {
		out := map[string]any{"app": created.App, "key": created.Key, "sync": created.Sync}
		if *reveal {
			out["secret"] = created.Secret
		}
		return rtPrintJSON(out)
	}
	_, _ = fmt.Fprintf(rtOut, "Created app '%s' (id %s) on realtime server '%s'.\n", created.App.Name, created.App.AppID, target.instance.Name)
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "  Key:\t%s (%s)\n", created.Key.Key, created.Key.ID)
	if *reveal {
		_, _ = fmt.Fprintf(tw, "  Secret:\t%s\n", created.Secret)
	} else {
		_, _ = fmt.Fprintf(tw, "  Secret:\thidden (reveal it with: hubfly realtime keys reveal %s %s --app %s)\n", target.instance.Name, created.Key.ID, created.App.AppID)
	}
	_, _ = fmt.Fprintf(tw, "  Config:\t%s\n", describeRealtimeSync(created.Sync))
	return tw.Flush()
}

func realtimeAppsUpdateFlow(args []string) error {
	const usage = "hubfly realtime apps update <server> <app> [--name <name>] [--enable|--disable] [settings] [--project <id|name>] [--json]"
	fs := newRealtimeFlags("apps update")
	projectRef := fs.String("project", "", "")
	newName := fs.String("name", "", "")
	enable := fs.Bool("enable", false, "")
	disable := fs.Bool("disable", false, "")
	asJSON := fs.Bool("json", false, "")
	settingsFlags := registerRealtimeSettingsFlags(fs)
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 2 {
		return fmt.Errorf("usage: %s", usage)
	}
	if *enable && *disable {
		return errors.New("invalid arguments: use --enable or --disable, not both")
	}
	settings, err := settingsFlags.build()
	if err != nil {
		return err
	}
	appBody := map[string]any{}
	if strings.TrimSpace(*newName) != "" {
		appBody["name"] = strings.TrimSpace(*newName)
	}
	if *enable || *disable {
		appBody["enabled"] = *enable
	}
	if len(appBody) == 0 && len(settings) == 0 {
		return fmt.Errorf("invalid arguments: nothing to update\nusage: %s", usage)
	}
	target, err := resolveRealtimeTarget(positionals[0], *projectRef)
	if err != nil {
		return err
	}
	app, err := resolveRealtimeApp(target.token, target.instance, positionals[1])
	if err != nil {
		return err
	}
	result := rtAppUpdateResult{App: app}
	if len(appBody) > 0 {
		result, err = updateRealtimeApp(target.token, target.instance.ProjectID, target.instance.ID, app.AppID, appBody)
		if err != nil {
			return realtimeAPIError(err)
		}
	}
	if len(settings) > 0 {
		result, err = updateRealtimeAppSettings(target.token, target.instance.ProjectID, target.instance.ID, app.AppID, settings, result.App.SettingsRevision)
		if err != nil {
			return realtimeAPIError(err)
		}
	}
	if *asJSON {
		return rtPrintJSON(result)
	}
	_, _ = fmt.Fprintf(rtOut, "Updated app '%s' (id %s). Config: %s.\n", result.App.Name, result.App.AppID, describeRealtimeSync(result.Sync))
	return nil
}

func realtimeAppsDeleteFlow(args []string) error {
	const usage = "hubfly realtime apps delete <server> <app> [--project <id|name>] [--yes]"
	fs := newRealtimeFlags("apps delete")
	projectRef := fs.String("project", "", "")
	yes := fs.Bool("yes", false, "")
	fs.BoolVar(yes, "y", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 2 {
		return fmt.Errorf("usage: %s", usage)
	}
	target, err := resolveRealtimeTarget(positionals[0], *projectRef)
	if err != nil {
		return err
	}
	app, err := resolveRealtimeApp(target.token, target.instance, positionals[1])
	if err != nil {
		return err
	}
	if !*yes {
		if !rtInteractive() {
			return errors.New("invalid arguments: confirmation required; re-run with --yes to delete")
		}
		confirmed, promptErr := confirmTyped(fmt.Sprintf(
			"This permanently deletes app '%s' (id %s) from realtime server '%s': its keys stop working, its clients are disconnected and the variables written by its bindings are removed.",
			app.Name, app.AppID, target.instance.Name), app.Name)
		if promptErr != nil {
			return promptErr
		}
		if !confirmed {
			_, _ = fmt.Fprintln(rtOut, "Cancelled: the name did not match.")
			return nil
		}
	}
	result, err := deleteRealtimeApp(target.token, target.instance.ProjectID, target.instance.ID, app.AppID, app.Name)
	if err != nil {
		return realtimeAPIError(err)
	}
	_, _ = fmt.Fprintf(rtOut, "Deleted app '%s' (%d bound variables removed).\n", app.Name, result.VariablesRemoved)
	return nil
}

// ---------------------------------------------------------------------------
// keys
// ---------------------------------------------------------------------------

func runRealtimeKeysGroup(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: hubfly realtime keys <list|create|rotate|revoke|reveal> <server> ...")
	}
	switch args[0] {
	case "list", "ls":
		return realtimeKeysListFlow(args[1:])
	case "create":
		return realtimeKeysCreateFlow(args[1:])
	case "rotate":
		return realtimeKeysRotateFlow(args[1:])
	case "revoke":
		return realtimeKeysRevokeFlow(args[1:])
	case "reveal":
		return realtimeKeysRevealFlow(args[1:])
	default:
		return realtimeKeysListFlow(args)
	}
}

type realtimeKeyCommand struct {
	serverRef  string
	projectRef string
	appRef     string
	rest       []string
}

type realtimeKeyArgs struct {
	target realtimeTarget
	app    rtApp
	rest   []string
}

// parseRealtimeKeyCommand parses "<server> [extra...]" plus --app/--project
// without contacting the API.
func parseRealtimeKeyCommand(fs *flag.FlagSet, args []string, usage string, minArgs, maxArgs int) (realtimeKeyCommand, error) {
	projectRef := fs.String("project", "", "")
	appRef := fs.String("app", "", "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return realtimeKeyCommand{}, err
	}
	if len(positionals) < minArgs || len(positionals) > maxArgs {
		return realtimeKeyCommand{}, fmt.Errorf("usage: %s", usage)
	}
	return realtimeKeyCommand{serverRef: positionals[0], projectRef: *projectRef, appRef: *appRef, rest: positionals[1:]}, nil
}

// resolve finds the server and the app.
func (c realtimeKeyCommand) resolve() (realtimeKeyArgs, error) {
	target, err := resolveRealtimeTarget(c.serverRef, c.projectRef)
	if err != nil {
		return realtimeKeyArgs{}, err
	}
	app, err := resolveRealtimeApp(target.token, target.instance, c.appRef)
	if err != nil {
		return realtimeKeyArgs{}, err
	}
	return realtimeKeyArgs{target: target, app: app, rest: c.rest}, nil
}

func realtimeKeysListFlow(args []string) error {
	const usage = "hubfly realtime keys list <server> [--app <app>] [--project <id|name>] [--json]"
	fs := newRealtimeFlags("keys list")
	asJSON := fs.Bool("json", false, "")
	command, err := parseRealtimeKeyCommand(fs, args, usage, 1, 1)
	if err != nil {
		return err
	}
	parsed, err := command.resolve()
	if err != nil {
		return err
	}
	keys, err := fetchRealtimeKeys(parsed.target.token, parsed.target.instance.ProjectID, parsed.target.instance.ID, parsed.app.AppID)
	if err != nil {
		return realtimeAPIError(err)
	}
	if *asJSON {
		return rtPrintJSON(keys)
	}
	if len(keys) == 0 {
		_, _ = fmt.Fprintf(rtOut, "App '%s' has no keys.\n", parsed.app.Name)
		return nil
	}
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tKEY\tSTATUS\tSECRET\tEXPIRES\tCREATED")
	for _, key := range keys {
		expires := "-"
		if key.ExpiresAt != nil && *key.ExpiresAt != "" {
			expires = *key.ExpiresAt
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", key.ID, key.Key, key.Status, key.SecretHint, expires, key.CreatedAt)
	}
	return tw.Flush()
}

func realtimeKeysCreateFlow(args []string) error {
	const usage = "hubfly realtime keys create <server> [--app <app>] [--project <id|name>] [--json]"
	fs := newRealtimeFlags("keys create")
	asJSON := fs.Bool("json", false, "")
	command, err := parseRealtimeKeyCommand(fs, args, usage, 1, 1)
	if err != nil {
		return err
	}
	parsed, err := command.resolve()
	if err != nil {
		return err
	}
	created, err := createRealtimeKey(parsed.target.token, parsed.target.instance.ProjectID, parsed.target.instance.ID, parsed.app.AppID)
	if err != nil {
		return realtimeAPIError(err)
	}
	// The new secret is shown this one time (like the web dialog); `keys
	// reveal` shows it again later, audited. --json includes it too (see the
	// usage text).
	if *asJSON {
		return rtPrintJSON(created)
	}
	_, _ = fmt.Fprintln(rtErr, "Warning: the new key's secret is printed below. Store it now and keep it out of logs and shared terminals; `keys reveal` shows it again (audited).")
	_, _ = fmt.Fprintf(rtOut, "Created key %s for app '%s'.\n", created.Key.ID, parsed.app.Name)
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "  Key:\t%s\n", created.Key.Key)
	_, _ = fmt.Fprintf(tw, "  Secret:\t%s\n", created.Secret)
	_, _ = fmt.Fprintf(tw, "  Config:\t%s\n", describeRealtimeSync(created.Sync))
	return tw.Flush()
}

func realtimeKeysRotateFlow(args []string) error {
	const usage = "hubfly realtime keys rotate <server> [<key>] [--app <app>] [--grace <duration>] [--project <id|name>] [--reveal] [--yes] [--json]"
	fs := newRealtimeFlags("keys rotate")
	graceText := fs.String("grace", "", "")
	reveal := fs.Bool("reveal", false, "")
	yes := fs.Bool("yes", false, "")
	fs.BoolVar(yes, "y", false, "")
	asJSON := fs.Bool("json", false, "")
	command, err := parseRealtimeKeyCommand(fs, args, usage, 1, 2)
	if err != nil {
		return err
	}
	var grace *int
	if strings.TrimSpace(*graceText) != "" {
		seconds, graceErr := parseRealtimeDuration(*graceText)
		if graceErr != nil {
			return fmt.Errorf("invalid --grace: %v", graceErr)
		}
		grace = &seconds
	}
	parsed, err := command.resolve()
	if err != nil {
		return err
	}
	keyRef := ""
	if len(parsed.rest) == 1 {
		keyRef = parsed.rest[0]
	}
	key, err := pickRealtimeKey(parsed.app, keyRef)
	if err != nil {
		return err
	}
	if grace != nil && *grace == 0 && !*yes {
		if !rtInteractive() {
			return errors.New("invalid arguments: --grace 0 revokes the old key immediately; re-run with --yes to confirm")
		}
		confirmed, promptErr := promptYesNo(fmt.Sprintf("--grace 0 revokes key %s immediately; clients and servers still using it fail. Continue?", key.Key), false)
		if promptErr != nil {
			return promptErr
		}
		if !confirmed {
			_, _ = fmt.Fprintln(rtOut, "Cancelled.")
			return nil
		}
	}
	rotated, err := rotateRealtimeKey(parsed.target.token, parsed.target.instance.ProjectID, parsed.target.instance.ID, parsed.app.AppID, key.ID, grace)
	if err != nil {
		return realtimeAPIError(err)
	}
	graceSeconds := -1
	if grace != nil {
		graceSeconds = *grace
	} else if rotated.Retiring.ExpiresAt != nil {
		if expires, parseErr := time.Parse(time.RFC3339, *rotated.Retiring.ExpiresAt); parseErr == nil {
			graceSeconds = int(time.Until(expires).Round(time.Minute) / time.Second)
		}
	}
	if *asJSON {
		out := map[string]any{
			"key":         rotated.Key,
			"retiring":    rotated.Retiring,
			"sync":        rotated.Sync,
			"appsUpdated": rotated.AppsUpdated,
		}
		if graceSeconds >= 0 {
			out["graceSeconds"] = graceSeconds
		}
		if *reveal {
			out["secret"] = rotated.Secret
		}
		return rtPrintJSON(out)
	}
	_, _ = fmt.Fprintf(rtOut, "Rotated key %s of app '%s'.\n", key.ID, parsed.app.Name)
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "  New key:\t%s (%s)\n", rotated.Key.Key, rotated.Key.ID)
	if *reveal {
		_, _ = fmt.Fprintf(tw, "  New secret:\t%s\n", rotated.Secret)
	} else {
		_, _ = fmt.Fprintf(tw, "  New secret:\thidden (pass --reveal, or: hubfly realtime keys reveal %s %s --app %s)\n", parsed.target.instance.Name, rotated.Key.ID, parsed.app.AppID)
	}
	oldUntil := "is revoked now"
	if rotated.Retiring.ExpiresAt != nil && *rotated.Retiring.ExpiresAt != "" {
		oldUntil = "keeps working until " + *rotated.Retiring.ExpiresAt
		if graceSeconds > 0 {
			oldUntil += " (grace period " + formatRealtimeSeconds(graceSeconds) + ")"
		}
	}
	_, _ = fmt.Fprintf(tw, "  Old key:\t%s %s\n", rotated.Retiring.Key, oldUntil)
	if rotated.AppsUpdated > 0 {
		_, _ = fmt.Fprintf(tw, "  Bindings updated:\t%d\n", rotated.AppsUpdated)
	}
	_, _ = fmt.Fprintf(tw, "  Config:\t%s\n", describeRealtimeSync(rotated.Sync))
	return tw.Flush()
}

func realtimeKeysRevokeFlow(args []string) error {
	const usage = "hubfly realtime keys revoke <server> <key> [--app <app>] [--project <id|name>] [--yes] [--json]"
	fs := newRealtimeFlags("keys revoke")
	yes := fs.Bool("yes", false, "")
	fs.BoolVar(yes, "y", false, "")
	asJSON := fs.Bool("json", false, "")
	command, err := parseRealtimeKeyCommand(fs, args, usage, 2, 2)
	if err != nil {
		return err
	}
	parsed, err := command.resolve()
	if err != nil {
		return err
	}
	key, err := pickRealtimeKey(parsed.app, parsed.rest[0])
	if err != nil {
		return err
	}
	if !*yes {
		if !rtInteractive() {
			return errors.New("invalid arguments: confirmation required; re-run with --yes to revoke")
		}
		confirmed, promptErr := promptYesNo(fmt.Sprintf("Revoke key %s (%s) of app '%s' now? Clients and servers using it fail immediately.", key.ID, key.Key, parsed.app.Name), false)
		if promptErr != nil {
			return promptErr
		}
		if !confirmed {
			_, _ = fmt.Fprintln(rtOut, "Cancelled.")
			return nil
		}
	}
	revoked, err := revokeRealtimeKey(parsed.target.token, parsed.target.instance.ProjectID, parsed.target.instance.ID, parsed.app.AppID, key.ID)
	if err != nil {
		return realtimeAPIError(err)
	}
	if *asJSON {
		return rtPrintJSON(revoked)
	}
	_, _ = fmt.Fprintf(rtOut, "Revoked key %s of app '%s'. Config: %s.\n", key.ID, parsed.app.Name, describeRealtimeSync(revoked.Sync))
	if revoked.AppsUpdated > 0 {
		_, _ = fmt.Fprintf(rtOut, "%d bindings updated.\n", revoked.AppsUpdated)
	}
	return nil
}

func realtimeKeysRevealFlow(args []string) error {
	const usage = "hubfly realtime keys reveal <server> [<key>] [--app <app>] [--project <id|name>] [--json]"
	fs := newRealtimeFlags("keys reveal")
	asJSON := fs.Bool("json", false, "")
	command, err := parseRealtimeKeyCommand(fs, args, usage, 1, 2)
	if err != nil {
		return err
	}
	parsed, err := command.resolve()
	if err != nil {
		return err
	}
	keyRef := ""
	if len(parsed.rest) == 1 {
		keyRef = parsed.rest[0]
	}
	key, err := pickRealtimeKey(parsed.app, keyRef)
	if err != nil {
		return err
	}
	revealed, err := revealRealtimeKey(parsed.target.token, parsed.target.instance.ProjectID, parsed.target.instance.ID, parsed.app.AppID, key.ID)
	if err != nil {
		return realtimeAPIError(err)
	}
	if *asJSON {
		return rtPrintJSON(revealed)
	}
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "App ID:\t%s\n", revealed.AppID)
	_, _ = fmt.Fprintf(tw, "Key:\t%s\n", revealed.Key)
	_, _ = fmt.Fprintf(tw, "Secret:\t%s\n", revealed.Secret)
	return tw.Flush()
}

// ---------------------------------------------------------------------------
// bind
// ---------------------------------------------------------------------------

var realtimeClientPrefixes = []string{"none", "NEXT_PUBLIC_", "VITE_", "PUBLIC_"}

// normalizeRealtimeClientPrefix accepts "", "none", "next_public", "VITE_"...
func normalizeRealtimeClientPrefix(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.EqualFold(value, "none") {
		return "none", nil
	}
	upper := strings.ToUpper(value)
	if !strings.HasSuffix(upper, "_") {
		upper += "_"
	}
	for _, prefix := range realtimeClientPrefixes {
		if prefix == upper {
			return prefix, nil
		}
	}
	return "", fmt.Errorf("invalid --client-prefix %q (expected none, NEXT_PUBLIC_, VITE_ or PUBLIC_)", value)
}

func normalizeRealtimePreset(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "hubfly":
		return "hubfly", nil
	case "laravel", "pusher":
		return "laravel", nil
	default:
		return "", fmt.Errorf("invalid --preset %q (expected hubfly or laravel)", value)
	}
}

func runRealtimeBindGroup(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "list", "ls":
			return realtimeBindListFlow(args[1:])
		case "remove", "rm", "delete":
			return realtimeBindRemoveFlow(args[1:])
		case "create":
			return realtimeBindCreateFlow(args[1:])
		}
	}
	return realtimeBindCreateFlow(args)
}

func realtimeBindCreateFlow(args []string) error {
	const usage = "hubfly realtime bind <server> --to <project> [--app <app>] [--preset hubfly|laravel] [--client-prefix none|NEXT_PUBLIC_|VITE_|PUBLIC_] [--key <key>] [--project <id|name>] [--json]"
	fs := newRealtimeFlags("bind")
	projectRef := fs.String("project", "", "")
	targetRef := fs.String("to", "", "")
	appRef := fs.String("app", "", "")
	presetText := fs.String("preset", "hubfly", "")
	prefixText := fs.String("client-prefix", "none", "")
	keyRef := fs.String("key", "", "")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 1 || strings.TrimSpace(*targetRef) == "" {
		return fmt.Errorf("usage: %s", usage)
	}
	preset, err := normalizeRealtimePreset(*presetText)
	if err != nil {
		return err
	}
	prefix, err := normalizeRealtimeClientPrefix(*prefixText)
	if err != nil {
		return err
	}
	target, err := resolveRealtimeTarget(positionals[0], *projectRef)
	if err != nil {
		return err
	}
	app, err := resolveRealtimeApp(target.token, target.instance, *appRef)
	if err != nil {
		return err
	}
	input := rtBindingCreateInput{AppID: app.AppID, Preset: preset, ClientPrefix: prefix}
	if strings.TrimSpace(*keyRef) != "" {
		key, keyErr := pickRealtimeKey(app, *keyRef)
		if keyErr != nil {
			return keyErr
		}
		input.KeyID = key.ID
	}
	projects, err := fetchAllProjectsAnyType(target.token, "")
	if err != nil {
		return err
	}
	for _, item := range projects {
		if item.ID == *targetRef || strings.EqualFold(item.Name, *targetRef) {
			input.TargetProjectID = item.ID
			break
		}
	}
	if input.TargetProjectID == "" {
		return fmt.Errorf("project '%s' not found", *targetRef)
	}
	binding, err := createRealtimeBinding(target.token, target.instance.ProjectID, target.instance.ID, input)
	if err != nil {
		return realtimeAPIError(err)
	}
	if *asJSON {
		return rtPrintJSON(binding)
	}
	presetLabel := "HubFly"
	if binding.Preset == "laravel" {
		presetLabel = "Laravel/Pusher"
	}
	_, _ = fmt.Fprintf(rtOut, "Bound app '%s' of '%s' to project '%s' (%s preset, binding %s).\n",
		app.Name, target.instance.Name, binding.TargetProjectName, presetLabel, binding.ID)
	_, _ = fmt.Fprintf(rtOut, "Variables: %s\n", strings.Join(binding.EnvKeys, ", "))
	_, _ = fmt.Fprintln(rtOut, "Redeploy or restart the project's containers to load them.")
	return nil
}

func realtimeBindListFlow(args []string) error {
	const usage = "hubfly realtime bind list <server> [--project <id|name>] [--json]"
	fs := newRealtimeFlags("bind list")
	projectRef := fs.String("project", "", "")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return fmt.Errorf("usage: %s", usage)
	}
	target, err := resolveRealtimeTarget(positionals[0], *projectRef)
	if err != nil {
		return err
	}
	bindings, err := fetchRealtimeBindings(target.token, target.instance.ProjectID, target.instance.ID)
	if err != nil {
		return realtimeAPIError(err)
	}
	if *asJSON {
		return rtPrintJSON(bindings)
	}
	if len(bindings) == 0 {
		_, _ = fmt.Fprintf(rtOut, "Realtime server '%s' has no bindings.\n", target.instance.Name)
		return nil
	}
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tAPP\tPROJECT\tPRESET\tCLIENT PREFIX\tKEY\tVARIABLES")
	for _, binding := range bindings {
		key := "-"
		if binding.Key != nil {
			key = *binding.Key
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%d\n", binding.ID, binding.AppName, binding.TargetProjectName,
			binding.Preset, binding.ClientPrefix, key, len(binding.EnvKeys))
	}
	return tw.Flush()
}

func realtimeBindRemoveFlow(args []string) error {
	const usage = "hubfly realtime bind remove <server> <bindingId> [--keep-variables] [--project <id|name>] [--yes]"
	fs := newRealtimeFlags("bind remove")
	projectRef := fs.String("project", "", "")
	keep := fs.Bool("keep-variables", false, "")
	yes := fs.Bool("yes", false, "")
	fs.BoolVar(yes, "y", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 2 {
		return fmt.Errorf("usage: %s", usage)
	}
	target, err := resolveRealtimeTarget(positionals[0], *projectRef)
	if err != nil {
		return err
	}
	bindings, err := fetchRealtimeBindings(target.token, target.instance.ProjectID, target.instance.ID)
	if err != nil {
		return realtimeAPIError(err)
	}
	var binding *rtBinding
	for index := range bindings {
		if bindings[index].ID == positionals[1] {
			binding = &bindings[index]
			break
		}
	}
	if binding == nil {
		return fmt.Errorf("binding '%s' not found on realtime server '%s'", positionals[1], target.instance.Name)
	}
	if !*yes {
		if !rtInteractive() {
			return errors.New("invalid arguments: confirmation required; re-run with --yes to remove")
		}
		what := fmt.Sprintf("Remove binding %s of app '%s' and its %d variables from project '%s'?", binding.ID, binding.AppName, len(binding.EnvKeys), binding.TargetProjectName)
		if *keep {
			what = fmt.Sprintf("Remove binding %s of app '%s' (variables stay in project '%s')?", binding.ID, binding.AppName, binding.TargetProjectName)
		}
		confirmed, promptErr := promptYesNo(what, false)
		if promptErr != nil {
			return promptErr
		}
		if !confirmed {
			_, _ = fmt.Fprintln(rtOut, "Cancelled.")
			return nil
		}
	}
	removed, err := deleteRealtimeBinding(target.token, target.instance.ProjectID, target.instance.ID, binding.ID, !*keep)
	if err != nil {
		return realtimeAPIError(err)
	}
	_, _ = fmt.Fprintf(rtOut, "Removed binding %s (%d variables removed).\n", binding.ID, removed)
	return nil
}

// ---------------------------------------------------------------------------
// env
// ---------------------------------------------------------------------------

type rtEnvVar struct {
	Key    string
	Value  string
	Secret bool
}

const realtimeE2EKeysEnv = "HUBFLY_REALTIME_E2E_KEYS"

// realtimeEnvVariables mirrors the Dashboard's realtimeBindingVariables
// (shared/managed-services/realtime-bindings.ts): what "Bind to app" writes.
func realtimeEnvVariables(preset, clientPrefix, host, appID, key, secret string) []rtEnvVar {
	if preset == "laravel" {
		return []rtEnvVar{
			{Key: "BROADCAST_CONNECTION", Value: "pusher"},
			{Key: "PUSHER_APP_ID", Value: appID},
			{Key: "PUSHER_APP_KEY", Value: key},
			{Key: "PUSHER_APP_SECRET", Value: secret, Secret: true},
			{Key: "PUSHER_HOST", Value: host},
			{Key: "PUSHER_PORT", Value: "443"},
			{Key: "PUSHER_SCHEME", Value: "https"},
			{Key: "PUSHER_APP_CLUSTER", Value: "mt1"},
			{Key: "VITE_PUSHER_APP_KEY", Value: key},
			{Key: "VITE_PUSHER_HOST", Value: host},
			{Key: "VITE_PUSHER_PORT", Value: "443"},
			{Key: "VITE_PUSHER_SCHEME", Value: "https"},
			{Key: "VITE_PUSHER_APP_CLUSTER", Value: "mt1"},
		}
	}
	url := "https://" + host
	variables := []rtEnvVar{
		{Key: "HUBFLY_REALTIME_URL", Value: url},
		{Key: "HUBFLY_REALTIME_APP_ID", Value: appID},
		{Key: "HUBFLY_REALTIME_KEY", Value: key},
		{Key: "HUBFLY_REALTIME_SECRET", Value: secret, Secret: true},
	}
	if clientPrefix != "" && clientPrefix != "none" {
		variables = append(variables,
			rtEnvVar{Key: clientPrefix + "HUBFLY_REALTIME_URL", Value: url},
			rtEnvVar{Key: clientPrefix + "HUBFLY_REALTIME_KEY", Value: key},
		)
	}
	return variables
}

var dotenvPlainValue = regexp.MustCompile(`^[A-Za-z0-9_./:@+=,-]*$`)

func formatDotenvValue(value string) string {
	if dotenvPlainValue.MatchString(value) {
		return value
	}
	escaped := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "$", `\$`).Replace(value)
	return `"` + escaped + `"`
}

func realtimeEnvFlow(args []string) error {
	const usage = "hubfly realtime env <server> [--app <app>] [--preset hubfly|laravel] [--client-prefix <prefix>] [--key <key>] [--format dotenv|json] [--reveal] [--project <id|name>]"
	fs := newRealtimeFlags("env")
	projectRef := fs.String("project", "", "")
	appRef := fs.String("app", "", "")
	presetText := fs.String("preset", "hubfly", "")
	prefixText := fs.String("client-prefix", "none", "")
	keyRef := fs.String("key", "", "")
	format := fs.String("format", "dotenv", "")
	reveal := fs.Bool("reveal", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return fmt.Errorf("usage: %s", usage)
	}
	preset, err := normalizeRealtimePreset(*presetText)
	if err != nil {
		return err
	}
	prefix, err := normalizeRealtimeClientPrefix(*prefixText)
	if err != nil {
		return err
	}
	outputFormat := strings.ToLower(strings.TrimSpace(*format))
	if outputFormat == "env" {
		outputFormat = "dotenv"
	}
	if outputFormat != "dotenv" && outputFormat != "json" {
		return fmt.Errorf("invalid --format %q (expected dotenv or json)", *format)
	}
	target, err := resolveRealtimeTarget(positionals[0], *projectRef)
	if err != nil {
		return err
	}
	app, err := resolveRealtimeApp(target.token, target.instance, *appRef)
	if err != nil {
		return err
	}
	var key rtKey
	if strings.TrimSpace(*keyRef) != "" {
		if key, err = pickRealtimeKey(app, *keyRef); err != nil {
			return err
		}
	} else {
		var ok bool
		if key, ok = newestActiveRealtimeKey(app); !ok {
			return fmt.Errorf("app '%s' has no active key; create one with: hubfly realtime keys create %s --app %s", app.Name, target.instance.Name, app.AppID)
		}
	}
	connection, err := fetchRealtimeConnectionInfo(target.token, target.instance.ProjectID, target.instance.ID)
	if err != nil {
		return realtimeAPIError(err)
	}
	secret := ""
	if *reveal {
		revealed, revealErr := revealRealtimeKey(target.token, target.instance.ProjectID, target.instance.ID, app.AppID, key.ID)
		if revealErr != nil {
			return realtimeAPIError(revealErr)
		}
		secret = revealed.Secret
	}
	variables := realtimeEnvVariables(preset, prefix, connection.Host, app.AppID, key.Key, secret)
	hidden := []string{}
	for _, variable := range variables {
		if variable.Secret && !*reveal {
			hidden = append(hidden, variable.Key)
		}
	}
	e2e := app.Settings.Encryption != "" && app.Settings.Encryption != "off"

	if outputFormat == "json" {
		out := map[string]string{}
		for _, variable := range variables {
			if variable.Secret && !*reveal {
				continue
			}
			out[variable.Key] = variable.Value
		}
		if len(hidden) > 0 {
			_, _ = fmt.Fprintf(rtErr, "Omitted %s; pass --reveal to include it.\n", strings.Join(hidden, ", "))
		}
		if e2e {
			_, _ = fmt.Fprintf(rtErr, "%s (end-to-end master keys) is yours to set: generate one with 'hubfly realtime keygen'; HubFly never stores it.\n", realtimeE2EKeysEnv)
		}
		return rtPrintJSON(out)
	}
	_, _ = fmt.Fprintf(rtOut, "# HubFly Realtime: app %s (%s) on %s\n", app.Name, app.AppID, target.instance.Name)
	for _, variable := range variables {
		if variable.Secret && !*reveal {
			_, _ = fmt.Fprintf(rtOut, "# %s is hidden; pass --reveal to print it\n", variable.Key)
			continue
		}
		_, _ = fmt.Fprintf(rtOut, "%s=%s\n", variable.Key, formatDotenvValue(variable.Value))
	}
	if e2e {
		_, _ = fmt.Fprintf(rtOut, "# %s (end-to-end master keys) is yours: generate with 'hubfly realtime keygen'; HubFly never stores it\n", realtimeE2EKeysEnv)
	}
	return nil
}

// sortedKeys is used for stable table output of count maps.
func sortedKeys(values map[string]int64) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

package cli

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"golang.org/x/term"
)

// `hubfly realtime` (alias `rt`): Managed Realtime servers, apps, keys,
// bindings, events, the live inspector and E2E key generation.

var (
	rtOut io.Writer = os.Stdout
	rtErr io.Writer = os.Stderr
	// rtInteractive reports whether prompts can be answered (tests override it).
	rtInteractive = isInteractiveShell
	// rtStdinIsTerminal decides whether event data may come from stdin.
	rtStdinIsTerminal  = func() bool { return term.IsTerminal(int(os.Stdin.Fd())) }
	rtStderrIsTerminal = func() bool { return term.IsTerminal(int(os.Stderr.Fd())) }
	rtPollInterval     = 2 * time.Second
)

const realtimeUsageText = `Managed Realtime (Pusher-compatible WebSockets with end-to-end encryption):
  hubfly realtime list [--project <id|name>] [--json]
  hubfly realtime info <server> [--json]
  hubfly realtime create [<name>] [--size <preset>|--cpu <n> --memory <mib>] [--storage <gib>]
                         [--project <id|name|new>] [--app <name>] [--yes] [--no-wait] [--json]
  hubfly realtime delete <server> [--yes] [--wait]
  hubfly realtime apps [list] <server> [--json]
  hubfly realtime apps create <server> <name> [settings] [--reveal] [--json]
  hubfly realtime apps update <server> <app> [--name <name>] [--enable|--disable] [settings] [--json]
  hubfly realtime apps delete <server> <app> [--yes]
  hubfly realtime keys list <server> [--app <app>] [--json]
  hubfly realtime keys create <server> [--app <app>] [--json]
                              (prints the new secret once; with --json it is in "secret")
  hubfly realtime keys rotate <server> [<key>] [--app <app>] [--grace <duration>] [--reveal] [--yes] [--json]
  hubfly realtime keys revoke <server> <key> [--app <app>] [--yes] [--json]
  hubfly realtime keys reveal <server> [<key>] [--app <app>] [--json]
  hubfly realtime bind <server> --to <project> [--app <app>] [--preset hubfly|laravel]
                       [--client-prefix none|NEXT_PUBLIC_|VITE_|PUBLIC_] [--key <key>] [--json]
  hubfly realtime bind list <server> [--json]
  hubfly realtime bind remove <server> <bindingId> [--keep-variables] [--yes]
  hubfly realtime env <server> [--app <app>] [--preset hubfly|laravel] [--client-prefix <prefix>]
                      [--key <key>] [--format dotenv|json] [--reveal]
  hubfly realtime publish <server> --channel <name>... --event <name> [--data <text>|--data-file <path|->]
                          [--app <app>] [--exclude <socketId>] [--message-id <id>] [--json]
  hubfly realtime send <server> (--connection <id>...|--user <id>) --event <name>
                       [--data <text>|--data-file <path|->] [--app <app>] [--strip-key] [--json]
  hubfly realtime tail <server> [--app <app>...] [--family conn,sub,member,msg,api,err,webhook]
                       [--channel-prefix <prefix>] [--preview] [--json]
  hubfly realtime insights <server> [--app <app>] [--window <duration>] [--json]
  hubfly realtime keygen [--kid <kid>]

App settings (apps create/update):
  --origins <a,b> --client-events[=false] --subscription-count[=false] --require-auth[=false]
  --encryption off|optional|required --history off|memory|durable --history-ttl <duration>
  --history-max <n> --offline-queue[=false] --max-connections <n> --max-message-bytes <n>
  --payload-preview[=false]

<server> is a realtime server id or name (narrow with --project). <app> is an app id or
name; it may be omitted when the server has one app. Event data is sent as text.
"rt" is an alias of "realtime".`

func printRealtimeUsage() {
	_, _ = fmt.Fprintln(rtOut, realtimeUsageText)
}

func runRealtimeGroup(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		printRealtimeUsage()
		return nil
	}
	command, rest := args[0], args[1:]
	switch command {
	case "list", "ls":
		return realtimeListFlow(rest)
	case "info", "show", "inspect":
		return realtimeInfoFlow(rest)
	case "create":
		return realtimeCreateFlow(rest)
	case "delete", "rm":
		return realtimeDeleteFlow(rest)
	case "apps", "app":
		return runRealtimeAppsGroup(rest)
	case "keys", "key":
		return runRealtimeKeysGroup(rest)
	case "bind", "bindings":
		return runRealtimeBindGroup(rest)
	case "env":
		return realtimeEnvFlow(rest)
	case "publish", "pub":
		return realtimePublishFlow(rest)
	case "send":
		return realtimeSendFlow(rest)
	case "tail":
		return realtimeTailFlow(rest)
	case "insights":
		return realtimeInsightsFlow(rest)
	case "keygen":
		return realtimeKeygenFlow(rest)
	default:
		return fmt.Errorf("unknown realtime command %q (available: list, info, create, delete, apps, keys, bind, env, publish, send, tail, insights, keygen)", command)
	}
}

// ---------------------------------------------------------------------------
// Flags
// ---------------------------------------------------------------------------

func newRealtimeFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet("realtime "+name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

// parseRealtimeArgs parses flags placed anywhere between positional arguments
// (the flag package stops at the first positional) and returns the positionals.
func parseRealtimeArgs(fs *flag.FlagSet, args []string, usage string) ([]string, error) {
	positionals := []string{}
	rest := args
	for {
		before := rest
		if err := fs.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, fmt.Errorf("usage: %s", usage)
			}
			return nil, fmt.Errorf("invalid arguments: %v\nusage: %s", err, usage)
		}
		rest = fs.Args()
		if len(rest) == 0 {
			return positionals, nil
		}
		consumed := len(before) - len(rest)
		if consumed > 0 && before[consumed-1] == "--" {
			return append(positionals, rest...), nil
		}
		positionals = append(positionals, rest[0])
		rest = rest[1:]
	}
}

// rtList is a repeatable flag; with split it also accepts comma lists.
type rtList struct {
	values []string
	split  bool
}

func (l *rtList) String() string { return strings.Join(l.values, ",") }

func (l *rtList) Set(value string) error {
	if !l.split {
		l.values = append(l.values, value)
		return nil
	}
	for _, part := range strings.Split(value, ",") {
		if part = strings.TrimSpace(part); part != "" {
			l.values = append(l.values, part)
		}
	}
	return nil
}

// rtOptBool is a boolean flag that remembers whether it was given.
type rtOptBool struct {
	set   bool
	value bool
}

func (b *rtOptBool) String() string   { return strconv.FormatBool(b.value) }
func (b *rtOptBool) IsBoolFlag() bool { return true }
func (b *rtOptBool) Set(value string) error {
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fmt.Errorf("expected true or false")
	}
	b.set, b.value = true, parsed
	return nil
}

// rtOptString is a string flag that remembers whether it was given (so an
// empty value can mean "clear").
type rtOptString struct {
	set   bool
	value string
}

func (s *rtOptString) String() string { return s.value }
func (s *rtOptString) Set(value string) error {
	s.set, s.value = true, value
	return nil
}

// parseRealtimeDuration accepts Go durations ("90s", "24h") or plain seconds.
func parseRealtimeDuration(value string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, errors.New("empty duration")
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds < 0 {
			return 0, errors.New("duration must not be negative")
		}
		return seconds, nil
	}
	if strings.HasSuffix(value, "d") {
		days, err := strconv.Atoi(strings.TrimSuffix(value, "d"))
		if err == nil && days >= 0 {
			return days * 86400, nil
		}
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed < 0 {
		return 0, fmt.Errorf("invalid duration %q (use seconds or values like 90s, 15m, 24h, 7d)", value)
	}
	return int(parsed / time.Second), nil
}

func formatRealtimeSeconds(seconds int) string {
	switch {
	case seconds <= 0:
		return "0s"
	case seconds%86400 == 0:
		return fmt.Sprintf("%dd", seconds/86400)
	case seconds%3600 == 0:
		return fmt.Sprintf("%dh", seconds/3600)
	case seconds%60 == 0:
		return fmt.Sprintf("%dm", seconds/60)
	default:
		return fmt.Sprintf("%ds", seconds)
	}
}

// ---------------------------------------------------------------------------
// Resolution
// ---------------------------------------------------------------------------

func fetchManagedProjects(token, projectRef string) ([]project, error) {
	all, err := fetchAllProjectsAnyType(token, "")
	if err != nil {
		return nil, err
	}
	matches := []project{}
	for _, item := range all {
		if projectRef != "" {
			if item.ID == projectRef || strings.EqualFold(item.Name, projectRef) {
				matches = append(matches, item)
			}
			continue
		}
		if item.Type == "managed" {
			matches = append(matches, item)
		}
	}
	if projectRef != "" && len(matches) == 0 {
		return nil, fmt.Errorf("project '%s' not found", projectRef)
	}
	return matches, nil
}

// listRealtimeInstances returns the realtime servers of the managed projects
// (or of one project). Projects the caller cannot read are skipped unless
// one was named.
func listRealtimeInstances(token, projectRef string) ([]rtInstance, error) {
	projects, err := fetchManagedProjects(token, projectRef)
	if err != nil {
		return nil, err
	}
	result := []rtInstance{}
	for _, item := range projects {
		instances, listErr := fetchManagedInstances(token, item.ID)
		if listErr != nil {
			if projectRef != "" {
				return nil, listErr
			}
			debugf("skipping project %s: %v", item.ID, listErr)
			continue
		}
		for _, instance := range instances {
			if instance.Service != "realtime" {
				continue
			}
			instance.ProjectID = item.ID
			instance.ProjectName = item.Name
			result = append(result, instance)
		}
	}
	return result, nil
}

func resolveRealtimeInstance(token, ref, projectRef string) (rtInstance, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return rtInstance{}, errors.New("missing realtime server id or name")
	}
	instances, err := listRealtimeInstances(token, projectRef)
	if err != nil {
		return rtInstance{}, err
	}
	var byID, byName []rtInstance
	for _, instance := range instances {
		if instance.ID == ref {
			byID = append(byID, instance)
		} else if strings.EqualFold(instance.Name, ref) {
			byName = append(byName, instance)
		}
	}
	if len(byID) == 1 {
		return byID[0], nil
	}
	if len(byName) == 1 {
		return byName[0], nil
	}
	if len(byName) > 1 {
		names := make([]string, 0, len(byName))
		for _, instance := range byName {
			names = append(names, fmt.Sprintf("%s in project '%s'", instance.ID, instance.ProjectName))
		}
		return rtInstance{}, fmt.Errorf("realtime server name '%s' is ambiguous (%s); use the id or --project", ref, strings.Join(names, ", "))
	}
	return rtInstance{}, fmt.Errorf("realtime server '%s' not found (list servers with: hubfly realtime list)", ref)
}

func resolveRealtimeApp(token string, instance rtInstance, ref string) (rtApp, error) {
	apps, err := fetchRealtimeApps(token, instance.ProjectID, instance.ID)
	if err != nil {
		return rtApp{}, realtimeAPIError(err)
	}
	return pickRealtimeApp(apps, instance.Name, ref)
}

func pickRealtimeApp(apps []rtApp, serverName, ref string) (rtApp, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		switch len(apps) {
		case 0:
			return rtApp{}, fmt.Errorf("realtime server '%s' has no apps; create one with: hubfly realtime apps create %s <name>", serverName, serverName)
		case 1:
			return apps[0], nil
		default:
			names := make([]string, 0, len(apps))
			for _, app := range apps {
				names = append(names, fmt.Sprintf("%s (%s)", app.Name, app.AppID))
			}
			return rtApp{}, fmt.Errorf("realtime server '%s' has %d apps: %s; choose one with --app <id|name>", serverName, len(apps), strings.Join(names, ", "))
		}
	}
	for _, app := range apps {
		if app.AppID == ref {
			return app, nil
		}
	}
	for _, app := range apps {
		if strings.EqualFold(app.Name, ref) {
			return app, nil
		}
	}
	return rtApp{}, fmt.Errorf("app '%s' not found on realtime server '%s'", ref, serverName)
}

// pickRealtimeKey finds a key by id or public key; with an empty reference it
// takes the only active key.
func pickRealtimeKey(app rtApp, ref string) (rtKey, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		active := []rtKey{}
		for _, key := range app.Keys {
			if key.Status == "active" {
				active = append(active, key)
			}
		}
		if len(active) == 1 {
			return active[0], nil
		}
		if len(active) == 0 {
			return rtKey{}, fmt.Errorf("app '%s' has no active key; create one with: hubfly realtime keys create", app.Name)
		}
		ids := make([]string, 0, len(active))
		for _, key := range active {
			ids = append(ids, key.ID)
		}
		return rtKey{}, fmt.Errorf("app '%s' has %d active keys (%s); name one", app.Name, len(active), strings.Join(ids, ", "))
	}
	for _, key := range app.Keys {
		if key.ID == ref || key.Key == ref {
			return key, nil
		}
	}
	return rtKey{}, fmt.Errorf("key '%s' not found in app '%s'", ref, app.Name)
}

// newestActiveRealtimeKey is the key a binding or env block uses by default.
func newestActiveRealtimeKey(app rtApp) (rtKey, bool) {
	var newest rtKey
	found := false
	for _, key := range app.Keys {
		if key.Status != "active" {
			continue
		}
		if !found || key.CreatedAt > newest.CreatedAt {
			newest, found = key, true
		}
	}
	return newest, found
}

// realtimeAPIError adds a hint to the Dashboard errors a realtime user can act
// on. The wrapped *apiError keeps exit codes intact.
func realtimeAPIError(err error) error {
	var apiErr *apiError
	if !errors.As(err, &apiErr) {
		return err
	}
	hint := ""
	switch {
	case apiErr.Code == "MANAGED_REALTIME_ENGINE_UNSUPPORTED":
		hint = "This realtime server's engine version does not support this yet."
	case apiErr.Code == "MANAGED_REALTIME_E2E_REQUIRED":
		hint = "This app requires end-to-end encryption: HubFly rejects plaintext. Encrypt events on your own server with the HubFly server SDK."
	case apiErr.Code == "MANAGED_REALTIME_E2E_CONNECTION_ID":
		hint = "End-to-end connection ids carry a client key that must never reach HubFly. Send encrypted direct messages from your own server."
	case apiErr.Status == 413:
		hint = "The event data is larger than this app's message size limit."
	case apiErr.Code == "MANAGED_FEATURE_UNSUPPORTED":
		hint = "That managed instance is not a realtime server."
	case apiErr.Code == "MANAGED_INSTANCE_NOT_RUNNING":
		hint = "Start the realtime server first."
	case apiErr.Code == "MANAGED_PRICE_CHANGED":
		hint = "Run the command again to see the new price."
	}
	if hint == "" {
		return err
	}
	return fmt.Errorf("%w\n%s", err, hint)
}

func rtPrintJSON(value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(rtOut, string(encoded))
	return err
}

func rtMonthly(price rtPrice) string {
	return fmt.Sprintf("$%.2f/month", price.Monthly.Total)
}

func rtHourly(price rtPrice) string {
	return fmt.Sprintf("$%.4f/hour", price.Hourly.Total)
}

func formatRealtimeSize(size rtSize) string {
	return fmt.Sprintf("%s vCPU, %d MiB", strconv.FormatFloat(size.CPU, 'f', -1, 64), size.MemoryMib)
}

func formatThousands(value int) string {
	text := strconv.Itoa(value)
	if value < 0 {
		return text
	}
	var out strings.Builder
	for index, digit := range text {
		if index > 0 && (len(text)-index)%3 == 0 {
			out.WriteByte(',')
		}
		out.WriteRune(digit)
	}
	return out.String()
}

func randomRealtimeID(prefix string) string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return prefix + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return prefix + hex.EncodeToString(buf)
}

// confirmTyped asks the user to type an exact name.
func confirmTyped(message, expected string) (bool, error) {
	_, _ = fmt.Fprintln(rtOut, message)
	answer, err := prompt(fmt.Sprintf("Type '%s' to confirm: ", expected))
	if err != nil {
		return false, err
	}
	return answer == expected, nil
}

// ---------------------------------------------------------------------------
// Operation polling
// ---------------------------------------------------------------------------

type rtSpinner struct {
	enabled bool
	frames  []string
	index   int
	last    string
	width   int
}

func newRealtimeSpinner(quiet bool) *rtSpinner {
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	if runtime.GOOS == "windows" {
		frames = []string{"|", "/", "-", "\\"}
	}
	return &rtSpinner{enabled: !quiet && rtStderrIsTerminal(), frames: frames}
}

func (s *rtSpinner) update(text string, quiet bool) {
	if quiet {
		return
	}
	if !s.enabled {
		if text != s.last {
			_, _ = fmt.Fprintln(rtErr, text)
			s.last = text
		}
		return
	}
	frame := s.frames[s.index%len(s.frames)]
	s.index++
	line := frame + " " + text
	padding := ""
	if s.width > len(line) {
		padding = strings.Repeat(" ", s.width-len(line))
	}
	s.width = len(line)
	_, _ = fmt.Fprint(rtErr, "\r"+line+padding)
	s.last = text
}

func (s *rtSpinner) stop() {
	if s.enabled && s.width > 0 {
		_, _ = fmt.Fprint(rtErr, "\r"+strings.Repeat(" ", s.width)+"\r")
		s.width = 0
	}
}

func realtimeOperationProgress(label string, operation rtOperation) string {
	for index, step := range operation.Steps {
		if step.Status == "running" {
			return fmt.Sprintf("%s [%d/%d] %s", label, index+1, len(operation.Steps), step.Label)
		}
	}
	done := 0
	for _, step := range operation.Steps {
		if step.Status == "succeeded" || step.Status == "skipped" {
			done++
		}
	}
	if len(operation.Steps) > 0 {
		return fmt.Sprintf("%s [%d/%d] %s", label, done, len(operation.Steps), operation.Status)
	}
	return label + " " + operation.Status
}

// waitForRealtimeOperation polls an operation until it finishes. With
// goneIsDone a 404 (the instance is gone) counts as success.
func waitForRealtimeOperation(token string, instance rtInstance, operationID, label string, timeout time.Duration, quiet, goneIsDone bool) (rtOperation, error) {
	spinner := newRealtimeSpinner(quiet)
	defer spinner.stop()
	deadline := time.Now().Add(timeout)
	failures := 0
	for {
		operation, err := fetchManagedOperation(token, instance.ProjectID, instance.ID, operationID)
		if err != nil {
			var apiErr *apiError
			if goneIsDone && errors.As(err, &apiErr) && apiErr.Status == 404 {
				return rtOperation{ID: operationID, Status: "succeeded"}, nil
			}
			if errors.As(err, &apiErr) && apiErr.Status < 500 {
				return rtOperation{}, err
			}
			failures++
			if failures >= 5 {
				return rtOperation{}, fmt.Errorf("lost track of operation %s: %w", operationID, err)
			}
		} else {
			failures = 0
			switch operation.Status {
			case "succeeded":
				return operation, nil
			case "failed", "canceled":
				message := operation.Status
				if operation.Error != nil && strings.TrimSpace(operation.Error.Message) != "" {
					message = operation.Error.Message
				}
				return operation, fmt.Errorf("operation %s %s: %s", operationID, operation.Status, message)
			}
			spinner.update(realtimeOperationProgress(label, operation), quiet)
		}
		if time.Now().After(deadline) {
			return operation, fmt.Errorf("timed out after %s waiting for operation %s; it keeps running on HubFly (check with: hubfly realtime info %s)", timeout, operationID, instance.ID)
		}
		time.Sleep(rtPollInterval)
	}
}

// ---------------------------------------------------------------------------
// list / info
// ---------------------------------------------------------------------------

func realtimeListFlow(args []string) error {
	const usage = "hubfly realtime list [--project <id|name>] [--json]"
	fs := newRealtimeFlags("list")
	projectRef := fs.String("project", "", "")
	fs.StringVar(projectRef, "p", "", "")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) > 1 {
		return fmt.Errorf("unexpected argument: %s\nusage: %s", positionals[1], usage)
	}
	if len(positionals) == 1 && *projectRef == "" {
		*projectRef = positionals[0]
	}
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}
	instances, err := listRealtimeInstances(token, *projectRef)
	if err != nil {
		return err
	}
	if *asJSON {
		return rtPrintJSON(instances)
	}
	if len(instances) == 0 {
		_, _ = fmt.Fprintln(rtOut, "No realtime servers found. Create one with: hubfly realtime create <name>")
		return nil
	}
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAME\tSTATUS\tSIZE\tPRICE\tVERSION\tPROJECT\tID")
	for _, instance := range instances {
		status := instance.Status
		if instance.ActiveOperation != nil && instance.ActiveOperation.Status != "succeeded" {
			status += " (" + instance.ActiveOperation.Kind + ")"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			instance.Name, status, formatRealtimeSize(instance.Size), rtMonthly(instance.Price),
			instance.VersionLabel, instance.ProjectName, instance.ID)
	}
	return tw.Flush()
}

func realtimeInfoFlow(args []string) error {
	const usage = "hubfly realtime info <server> [--project <id|name>] [--json]"
	fs := newRealtimeFlags("info")
	projectRef := fs.String("project", "", "")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return fmt.Errorf("usage: %s", usage)
	}
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}
	instance, err := resolveRealtimeInstance(token, positionals[0], *projectRef)
	if err != nil {
		return err
	}
	detail, err := fetchManagedInstance(token, instance.ProjectID, instance.ID)
	if err != nil {
		return realtimeAPIError(err)
	}
	detail.ProjectID, detail.ProjectName = instance.ProjectID, instance.ProjectName
	connection, err := fetchRealtimeConnectionInfo(token, instance.ProjectID, instance.ID)
	if err != nil {
		return realtimeAPIError(err)
	}
	if *asJSON {
		return rtPrintJSON(map[string]any{"instance": detail, "connection": connection})
	}
	status := detail.Status
	if detail.StatusReason != nil && *detail.StatusReason != "" {
		status += " (" + *detail.StatusReason + ")"
	}
	rows := [][2]string{
		{"Realtime server", detail.Name},
		{"ID", detail.ID},
		{"Project", fmt.Sprintf("%s (%s)", detail.ProjectName, detail.ProjectID)},
		{"Status", status},
		{"Version", detail.VersionLabel},
		{"Size", fmt.Sprintf("%s, %d GiB", formatRealtimeSize(detail.Size), detail.Size.StorageGib)},
		{"Price", rtMonthly(detail.Price) + " (" + rtHourly(detail.Price) + ")"},
		{"WebSocket URL", connection.WsURL},
		{"HTTP API URL", connection.HTTPURL},
	}
	if detail.Realtime != nil {
		rows = append(rows, [2]string{"Max connections", formatThousands(detail.Realtime.Profile.MaxConnections)})
		sync := "in sync"
		if !detail.Realtime.Sync.InSync {
			sync = fmt.Sprintf("pending (generation %d, applied %d)", detail.Realtime.Sync.Generation, detail.Realtime.Sync.AppliedGeneration)
			if detail.Realtime.Sync.LastError != nil && *detail.Realtime.Sync.LastError != "" {
				sync += ": " + *detail.Realtime.Sync.LastError
			}
		}
		rows = append(rows, [2]string{"Config", sync})
	}
	if !connection.Ready {
		rows = append(rows, [2]string{"Endpoint", "not ready yet"})
	}
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	for _, row := range rows {
		_, _ = fmt.Fprintf(tw, "%s:\t%s\n", row[0], row[1])
	}
	_ = tw.Flush()
	if len(connection.Apps) > 0 {
		_, _ = fmt.Fprintln(rtOut)
		tw = tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "APP ID\tNAME\tENABLED\tKEY\tENCRYPTION")
		for _, app := range connection.Apps {
			key := "-"
			if app.Key != nil {
				key = *app.Key
			}
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%t\t%s\t%s\n", app.AppID, app.Name, app.Enabled, key, app.Encryption)
		}
		_ = tw.Flush()
	}
	return nil
}

// ---------------------------------------------------------------------------
// create / delete
// ---------------------------------------------------------------------------

type realtimeCreateOptions struct {
	Name        string
	SizeID      string
	CPU         float64
	MemoryMib   int
	StorageGib  int
	ProjectRef  string
	ProjectName string
	AppName     string
	Version     string
	Yes         bool
	NoWait      bool
	JSON        bool
	Timeout     time.Duration
}

const realtimeCreateUsage = "hubfly realtime create [<name>] [--size <preset>|--cpu <n> --memory <mib>] [--storage <gib>] [--project <id|name|new>] [--project-name <name>] [--app <name>] [--version <id>] [--yes] [--no-wait] [--timeout <duration>] [--json]"

func parseRealtimeCreateOptions(args []string) (realtimeCreateOptions, error) {
	var opts realtimeCreateOptions
	fs := newRealtimeFlags("create")
	fs.StringVar(&opts.Name, "name", "", "")
	fs.StringVar(&opts.SizeID, "size", "", "")
	fs.Float64Var(&opts.CPU, "cpu", 0, "")
	fs.IntVar(&opts.MemoryMib, "memory", 0, "")
	fs.IntVar(&opts.StorageGib, "storage", 0, "")
	fs.StringVar(&opts.ProjectRef, "project", "", "")
	fs.StringVar(&opts.ProjectRef, "p", "", "")
	fs.StringVar(&opts.ProjectName, "project-name", "", "")
	fs.StringVar(&opts.AppName, "app", "", "")
	fs.StringVar(&opts.Version, "version", "", "")
	fs.BoolVar(&opts.Yes, "yes", false, "")
	fs.BoolVar(&opts.Yes, "y", false, "")
	fs.BoolVar(&opts.NoWait, "no-wait", false, "")
	fs.BoolVar(&opts.JSON, "json", false, "")
	timeout := fs.String("timeout", "15m", "")
	positionals, err := parseRealtimeArgs(fs, args, realtimeCreateUsage)
	if err != nil {
		return opts, err
	}
	if len(positionals) > 1 {
		return opts, fmt.Errorf("unexpected argument: %s\nusage: %s", positionals[1], realtimeCreateUsage)
	}
	if len(positionals) == 1 {
		if opts.Name != "" && opts.Name != positionals[0] {
			return opts, fmt.Errorf("name given twice\nusage: %s", realtimeCreateUsage)
		}
		opts.Name = positionals[0]
	}
	opts.Name = strings.TrimSpace(opts.Name)
	if opts.Name == "" {
		return opts, fmt.Errorf("usage: %s", realtimeCreateUsage)
	}
	if (opts.CPU > 0) != (opts.MemoryMib > 0) {
		return opts, errors.New("invalid size: pass --cpu and --memory together (or --size <preset>)")
	}
	if opts.CPU < 0 || opts.MemoryMib < 0 || opts.StorageGib < 0 {
		return opts, errors.New("invalid size: values must be positive")
	}
	if opts.CPU > 0 && opts.SizeID != "" {
		return opts, errors.New("invalid size: use --size or --cpu/--memory, not both")
	}
	seconds, err := parseRealtimeDuration(*timeout)
	if err != nil || seconds <= 0 {
		return opts, fmt.Errorf("invalid --timeout %q", *timeout)
	}
	opts.Timeout = time.Duration(seconds) * time.Second
	return opts, nil
}

func pickRealtimeSize(catalog rtCatalog, opts realtimeCreateOptions) (rtSize, string, error) {
	storage := opts.StorageGib
	if storage == 0 {
		storage = catalog.StorageGib.Default
	}
	if storage == 0 {
		storage = 1
	}
	if opts.CPU > 0 {
		return rtSize{CPU: opts.CPU, MemoryMib: opts.MemoryMib, StorageGib: storage}, "Custom", nil
	}
	if len(catalog.Sizes) == 0 {
		return rtSize{}, "", errors.New("the Dashboard returned no realtime sizes")
	}
	if opts.SizeID == "" {
		first := catalog.Sizes[0]
		return rtSize{CPU: first.CPU, MemoryMib: first.MemoryMib, StorageGib: storage}, realtimeSizeLabel(first), nil
	}
	ids := []string{}
	for _, size := range catalog.Sizes {
		ids = append(ids, size.ID)
		if strings.EqualFold(size.ID, opts.SizeID) || strings.EqualFold(size.Label, opts.SizeID) {
			return rtSize{CPU: size.CPU, MemoryMib: size.MemoryMib, StorageGib: storage}, realtimeSizeLabel(size), nil
		}
	}
	return rtSize{}, "", fmt.Errorf("invalid --size %q (available: %s)", opts.SizeID, strings.Join(ids, ", "))
}

func realtimeSizeLabel(size rtCatalogSize) string {
	label := size.Label
	if size.Profile.MaxConnections > 0 {
		label += fmt.Sprintf(", up to %s connections", formatThousands(size.Profile.MaxConnections))
	}
	return label
}

// resolveRealtimeProject returns the managed project to create in; a zero
// project with create=true means "create a new managed project".
func resolveRealtimeProject(token string, opts realtimeCreateOptions) (project, bool, error) {
	ref := strings.TrimSpace(opts.ProjectRef)
	if strings.EqualFold(ref, "new") {
		return project{}, true, nil
	}
	if ref != "" {
		projects, err := fetchManagedProjects(token, ref)
		if err != nil {
			return project{}, false, err
		}
		chosen := projects[0]
		if chosen.Type != "" && chosen.Type != "managed" {
			return project{}, false, fmt.Errorf("project '%s' is a %s project; realtime servers live in managed projects (use --project new)", chosen.Name, chosen.Type)
		}
		return chosen, false, nil
	}
	managed, err := fetchManagedProjects(token, "")
	if err != nil {
		return project{}, false, err
	}
	if len(managed) == 0 {
		return project{}, true, nil
	}
	if !rtInteractive() {
		return project{}, false, errors.New("invalid arguments: choose a managed project with --project <id|name|new>")
	}
	_, _ = fmt.Fprintln(rtOut, "Managed projects:")
	for index, item := range managed {
		_, _ = fmt.Fprintf(rtOut, "  %d) %s (%s)\n", index+1, item.Name, item.ID)
	}
	_, _ = fmt.Fprintf(rtOut, "  %d) New managed project\n", len(managed)+1)
	choice, err := promptNumber(fmt.Sprintf("Project [1-%d]: ", len(managed)+1), len(managed)+1)
	if err != nil {
		return project{}, false, err
	}
	if choice == 0 {
		return project{}, false, errors.New("cancelled")
	}
	if choice == len(managed)+1 {
		return project{}, true, nil
	}
	return managed[choice-1], false, nil
}

func realtimeCreateFlow(args []string) error {
	opts, err := parseRealtimeCreateOptions(args)
	if err != nil {
		return err
	}
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}
	catalog, err := fetchManagedCatalogRealtime(token)
	if err != nil {
		return err
	}
	if !catalog.Available {
		return errors.New("Managed Realtime is not available in any region yet")
	}
	size, sizeLabel, err := pickRealtimeSize(catalog, opts)
	if err != nil {
		return err
	}
	target, createProject, err := resolveRealtimeProject(token, opts)
	if err != nil {
		return err
	}
	price, err := fetchManagedPriceEstimate(token, size)
	if err != nil {
		return realtimeAPIError(err)
	}

	projectName := strings.TrimSpace(opts.ProjectName)
	if projectName == "" {
		projectName = opts.Name
	}
	projectLabel := fmt.Sprintf("%s (%s)", target.Name, target.ID)
	if createProject {
		projectLabel = fmt.Sprintf("new managed project '%s' (HubFly chooses the region)", projectName)
	}
	quoteOut := rtOut
	if opts.JSON {
		quoteOut = rtErr // keep stdout clean for --json
	}
	tw := tabwriter.NewWriter(quoteOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "Realtime server:\t%s\n", opts.Name)
	_, _ = fmt.Fprintf(tw, "Size:\t%s (%s, %d GiB storage)\n", sizeLabel, formatRealtimeSize(size), size.StorageGib)
	_, _ = fmt.Fprintf(tw, "Project:\t%s\n", projectLabel)
	_, _ = fmt.Fprintf(tw, "Price:\t%s (%s, billed hourly)\n", rtMonthly(price), rtHourly(price))
	_ = tw.Flush()

	if !opts.Yes {
		if !rtInteractive() {
			return fmt.Errorf("invalid arguments: confirmation required; re-run with --yes to accept %s", rtMonthly(price))
		}
		confirmed, promptErr := promptYesNo("Create this realtime server?", false)
		if promptErr != nil {
			return promptErr
		}
		if !confirmed {
			_, _ = fmt.Fprintln(rtOut, "Cancelled.")
			return nil
		}
	}

	if createProject {
		created, createErr := createManagedProject(token, projectName)
		if createErr != nil {
			return fmt.Errorf("failed to create managed project: %w", createErr)
		}
		target = created
		_, _ = fmt.Fprintf(rtErr, "Created managed project '%s' (%s).\n", created.Name, created.ID)
	}

	body := map[string]any{
		"name":    opts.Name,
		"service": "realtime",
		"size": map[string]any{
			"cpu":        size.CPU,
			"memoryMib":  size.MemoryMib,
			"storageGib": size.StorageGib,
		},
		"quotedHourlyTotal": price.Hourly.Total,
		"idempotencyKey":    randomRealtimeID("cli-rt-"),
	}
	if opts.Version != "" {
		body["version"] = opts.Version
	}
	if opts.AppName != "" {
		body["initialApp"] = map[string]any{"name": opts.AppName}
	}
	created, err := createRealtimeInstance(token, target.ID, body)
	if err != nil {
		return realtimeAPIError(err)
	}
	instance := created.Instance
	instance.ProjectID, instance.ProjectName = target.ID, target.Name

	var operation *rtOperation
	if !opts.NoWait && created.OperationID != "" {
		finished, waitErr := waitForRealtimeOperation(token, instance, created.OperationID, "Creating realtime server", opts.Timeout, opts.JSON, false)
		if waitErr != nil {
			return waitErr
		}
		operation = &finished
	}
	detail, detailErr := fetchManagedInstance(token, target.ID, instance.ID)
	if detailErr != nil {
		detail = rtInstanceDetail{rtInstance: instance}
	}
	detail.ProjectID, detail.ProjectName = target.ID, target.Name

	if opts.JSON {
		return rtPrintJSON(map[string]any{
			"instance":    detail,
			"operationId": created.OperationID,
			"operation":   operation,
			"price":       created.Price,
			"initialApp":  created.InitialApp,
		})
	}
	if operation != nil {
		_, _ = fmt.Fprintf(rtOut, "Realtime server '%s' is ready.\n", instance.Name)
	} else {
		_, _ = fmt.Fprintf(rtOut, "Creating realtime server '%s' (operation %s).\n", instance.Name, created.OperationID)
	}
	tw = tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "  ID:\t%s\n", instance.ID)
	if detail.Realtime != nil && detail.Realtime.WsURL != "" {
		_, _ = fmt.Fprintf(tw, "  WebSocket URL:\t%s\n", detail.Realtime.WsURL)
		_, _ = fmt.Fprintf(tw, "  HTTP API URL:\t%s\n", detail.Realtime.HTTPURL)
	}
	if created.InitialApp != nil {
		_, _ = fmt.Fprintf(tw, "  App:\t%s (id %s)\n", created.InitialApp.Name, created.InitialApp.AppID)
		_, _ = fmt.Fprintf(tw, "  Key:\t%s\n", created.InitialApp.Key)
	}
	_ = tw.Flush()
	_, _ = fmt.Fprintln(rtOut, "\nNext steps:")
	_, _ = fmt.Fprintf(rtOut, "  hubfly realtime env %s --reveal         # print the app's environment variables\n", instance.Name)
	_, _ = fmt.Fprintf(rtOut, "  hubfly realtime bind %s --to <project>  # write them into a Cell project\n", instance.Name)
	return nil
}

func realtimeDeleteFlow(args []string) error {
	const usage = "hubfly realtime delete <server> [--project <id|name>] [--yes] [--wait]"
	fs := newRealtimeFlags("delete")
	projectRef := fs.String("project", "", "")
	yes := fs.Bool("yes", false, "")
	fs.BoolVar(yes, "y", false, "")
	wait := fs.Bool("wait", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return fmt.Errorf("usage: %s", usage)
	}
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}
	instance, err := resolveRealtimeInstance(token, positionals[0], *projectRef)
	if err != nil {
		return err
	}
	if !*yes {
		if !rtInteractive() {
			return errors.New("invalid arguments: confirmation required; re-run with --yes to delete")
		}
		confirmed, promptErr := confirmTyped(fmt.Sprintf(
			"This permanently deletes realtime server '%s' (%s) in project '%s', with all its apps, keys and history. Connected clients are disconnected.",
			instance.Name, instance.ID, instance.ProjectName), instance.Name)
		if promptErr != nil {
			return promptErr
		}
		if !confirmed {
			_, _ = fmt.Fprintln(rtOut, "Cancelled: the name did not match.")
			return nil
		}
	}
	operationID, err := deleteRealtimeInstance(token, instance.ProjectID, instance.ID, instance.Name)
	if err != nil {
		return realtimeAPIError(err)
	}
	if *wait && operationID != "" {
		if _, waitErr := waitForRealtimeOperation(token, instance, operationID, "Deleting realtime server", 15*time.Minute, false, true); waitErr != nil {
			return waitErr
		}
		_, _ = fmt.Fprintf(rtOut, "Realtime server '%s' deleted.\n", instance.Name)
		return nil
	}
	_, _ = fmt.Fprintf(rtOut, "Deleting realtime server '%s' (operation %s).\n", instance.Name, operationID)
	return nil
}

// ---------------------------------------------------------------------------
// keygen (local only: never contacts the network)
// ---------------------------------------------------------------------------

var realtimeKidPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

// generateRealtimeMasterKey returns an E2E master key in the E2E.md format:
// 32 random bytes, standard base64 with padding, optionally prefixed "kid:".
func generateRealtimeMasterKey(kid string) (string, error) {
	if kid != "" && (!realtimeKidPattern.MatchString(kid) || kid == "c") {
		return "", fmt.Errorf("invalid --kid %q: use 1-32 characters from A-Z, a-z, 0-9, _ and - (not \"c\")", kid)
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	encoded := base64.StdEncoding.EncodeToString(buf)
	if kid == "" {
		return encoded, nil
	}
	return kid + ":" + encoded, nil
}

func realtimeKeygenFlow(args []string) error {
	const usage = "hubfly realtime keygen [--kid <kid>]"
	fs := newRealtimeFlags("keygen")
	kid := fs.String("kid", "", "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) > 0 {
		return fmt.Errorf("unexpected argument: %s\nusage: %s", positionals[0], usage)
	}
	key, err := generateRealtimeMasterKey(strings.TrimSpace(*kid))
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintln(rtOut, key)
	if rtStderrIsTerminal() {
		_, _ = fmt.Fprintln(rtErr, "Generated locally; HubFly never sees it. Put it first in HUBFLY_REALTIME_E2E_KEYS on your servers (comma-separated kid:base64 list; the first entry encrypts).")
	}
	return nil
}

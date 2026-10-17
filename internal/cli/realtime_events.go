package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"text/tabwriter"
)

// rtStdin is where event data is read from with `--data-file -` or a pipe.
var rtStdin io.Reader = os.Stdin

// realtimeMaxDataBytes caps local reads; the server enforces the app's own
// (smaller) message limit and answers 413 above it.
const realtimeMaxDataBytes = 1 << 20

// realtimeE2EEvent is the outer event name of a HubFly v1 E2E envelope.
const realtimeE2EEvent = "hubfly.e2e"

var realtimeSocketIDPattern = regexp.MustCompile(`^[1-9][0-9]{9}\.[1-9][0-9]{9}$`)

// splitRealtimeConnectionID returns the socket id of a connection id. An
// end-to-end connection id is "<socket_id>~<k>" where k is the client's
// direct-message key (E2E.md): k is dropped here and never leaves this
// machine, not even in --debug output.
func splitRealtimeConnectionID(id string) (string, bool) {
	id = strings.TrimSpace(id)
	if index := strings.IndexByte(id, '~'); index >= 0 {
		return id[:index], true
	}
	return id, false
}

// realtimeSocketTargets validates connection ids and returns their unique
// socket ids plus how many carried a client key. Errors never echo an id,
// because an E2E connection id is a secret.
func realtimeSocketTargets(ids []string) ([]string, int, error) {
	seen := map[string]bool{}
	socketIDs := []string{}
	withKey := 0
	for index, id := range ids {
		socketID, hadKey := splitRealtimeConnectionID(id)
		if !realtimeSocketIDPattern.MatchString(socketID) {
			return nil, 0, fmt.Errorf("invalid connection %d: expected a socket id such as 1234567890.1234567890 or an end-to-end connection id", index+1)
		}
		if hadKey {
			withKey++
		}
		if !seen[socketID] {
			seen[socketID] = true
			socketIDs = append(socketIDs, socketID)
		}
	}
	return socketIDs, withKey, nil
}

func trimOneNewline(value string) string {
	if strings.HasSuffix(value, "\r\n") {
		return strings.TrimSuffix(value, "\r\n")
	}
	return strings.TrimSuffix(value, "\n")
}

func readLimitedRealtimeData(reader io.Reader, source string) (string, error) {
	payload, err := io.ReadAll(io.LimitReader(reader, realtimeMaxDataBytes+1))
	if err != nil {
		return "", fmt.Errorf("read event data from %s: %w", source, err)
	}
	if len(payload) > realtimeMaxDataBytes {
		return "", fmt.Errorf("event data from %s is larger than 1 MiB", source)
	}
	return trimOneNewline(string(payload)), nil
}

// readRealtimeEventData takes --data, --data-file (a path or "-") or piped
// stdin. One trailing newline of a file or stdin is removed.
func readRealtimeEventData(data rtOptString, dataFile string) (string, error) {
	dataFile = strings.TrimSpace(dataFile)
	if data.set && dataFile != "" {
		return "", errors.New("invalid arguments: use --data or --data-file, not both")
	}
	if data.set {
		if len(data.value) > realtimeMaxDataBytes {
			return "", errors.New("event data is larger than 1 MiB")
		}
		return data.value, nil
	}
	if dataFile != "" && dataFile != "-" {
		file, err := os.Open(dataFile)
		if err != nil {
			return "", fmt.Errorf("read event data: %w", err)
		}
		defer func() { _ = file.Close() }()
		return readLimitedRealtimeData(file, dataFile)
	}
	if dataFile == "" && rtStdinIsTerminal() {
		return "", errors.New("invalid arguments: pass the event data with --data <text>, --data-file <path|-> or on stdin")
	}
	value, err := readLimitedRealtimeData(rtStdin, "stdin")
	if err != nil {
		return "", err
	}
	if value == "" && dataFile == "" {
		return "", errors.New("invalid arguments: no event data on stdin (use --data \"\" to send empty data)")
	}
	return value, nil
}

// ---------------------------------------------------------------------------
// publish
// ---------------------------------------------------------------------------

func realtimePublishFlow(args []string) error {
	const usage = "hubfly realtime publish <server> --channel <name>... --event <name> [--data <text>|--data-file <path|->] [--app <app>] [--exclude <socketId>] [--message-id <id>] [--project <id|name>] [--json]\n       hubfly realtime publish <server> <channel> <event> [<data>]"
	fs := newRealtimeFlags("publish")
	projectRef := fs.String("project", "", "")
	appRef := fs.String("app", "", "")
	channels := &rtList{}
	fs.Var(channels, "channel", "")
	fs.Var(channels, "c", "")
	event := fs.String("event", "", "")
	fs.StringVar(event, "e", "", "")
	var data rtOptString
	fs.Var(&data, "data", "")
	fs.Var(&data, "d", "")
	dataFile := fs.String("data-file", "", "")
	exclude := fs.String("exclude", "", "")
	messageID := fs.String("message-id", "", "")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) < 1 || len(positionals) > 4 || len(positionals) == 2 {
		return fmt.Errorf("usage: %s", usage)
	}
	if len(positionals) >= 3 {
		if len(channels.values) > 0 || *event != "" {
			return fmt.Errorf("invalid arguments: give the channel and event as flags or as arguments, not both\nusage: %s", usage)
		}
		channels.values = []string{positionals[1]}
		*event = positionals[2]
		if len(positionals) == 4 {
			if data.set || *dataFile != "" {
				return fmt.Errorf("invalid arguments: event data given twice\nusage: %s", usage)
			}
			data = rtOptString{set: true, value: positionals[3]}
		}
	}
	if len(channels.values) == 0 || strings.TrimSpace(*event) == "" {
		return fmt.Errorf("usage: %s", usage)
	}
	payload, err := readRealtimeEventData(data, *dataFile)
	if err != nil {
		return err
	}
	input := rtPublishInput{Event: strings.TrimSpace(*event), Data: payload, Channels: channels.values, MessageID: strings.TrimSpace(*messageID)}
	if strings.TrimSpace(*exclude) != "" {
		socketID, _ := splitRealtimeConnectionID(*exclude)
		if !realtimeSocketIDPattern.MatchString(socketID) {
			return errors.New("invalid --exclude: expected a socket id such as 1234567890.1234567890")
		}
		input.ExcludeSocketID = socketID
	}
	target, err := resolveRealtimeTarget(positionals[0], *projectRef)
	if err != nil {
		return err
	}
	app, err := resolveRealtimeApp(target.token, target.instance, *appRef)
	if err != nil {
		return err
	}
	result, err := publishRealtimeEvent(target.token, target.instance.ProjectID, target.instance.ID, app.AppID, input)
	if err != nil {
		return realtimeAPIError(err)
	}
	if *asJSON {
		return rtPrintJSON(result)
	}
	plural := "s"
	if len(input.Channels) == 1 {
		plural = ""
	}
	_, _ = fmt.Fprintf(rtOut, "Published '%s' to %d channel%s (%d bytes, message %s).\n", input.Event, len(input.Channels), plural, result.Bytes, result.MessageID)
	names := make([]string, 0, len(result.Channels))
	for name := range result.Channels {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		position := result.Channels[name]
		if position.Offset == nil {
			continue
		}
		epoch := ""
		if position.Epoch != nil {
			epoch = " (epoch " + *position.Epoch + ")"
		}
		_, _ = fmt.Fprintf(rtOut, "  %s: offset %d%s\n", name, *position.Offset, epoch)
	}
	return nil
}

// ---------------------------------------------------------------------------
// send
// ---------------------------------------------------------------------------

const realtimeE2ERefusal = `End-to-end connection ids given: %d (<socket_id>~<key>). Their key never leaves this machine, and HubFly cannot deliver plaintext to them.
End-to-end direct messages must be encrypted by your own server: use the HubFly server SDK, which reads the key from the id.
To send this event unencrypted to the socket ids anyway, re-run with --strip-key.`

func realtimeSendFlow(args []string) error {
	const usage = "hubfly realtime send <server> (--connection <id>...|--user <id>) --event <name> [--data <text>|--data-file <path|->] [--app <app>] [--message-id <id>] [--strip-key] [--project <id|name>] [--json]"
	fs := newRealtimeFlags("send")
	projectRef := fs.String("project", "", "")
	appRef := fs.String("app", "", "")
	connections := &rtList{split: true}
	fs.Var(connections, "connection", "")
	user := fs.String("user", "", "")
	event := fs.String("event", "", "")
	fs.StringVar(event, "e", "", "")
	var data rtOptString
	fs.Var(&data, "data", "")
	fs.Var(&data, "d", "")
	dataFile := fs.String("data-file", "", "")
	messageID := fs.String("message-id", "", "")
	stripKey := fs.Bool("strip-key", false, "")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 1 || strings.TrimSpace(*event) == "" {
		return fmt.Errorf("usage: %s", usage)
	}
	hasUser := strings.TrimSpace(*user) != ""
	if (len(connections.values) > 0) == hasUser {
		return fmt.Errorf("invalid arguments: send to --connection ids or to one --user, not both or neither\nusage: %s", usage)
	}
	input := rtSendInput{Event: strings.TrimSpace(*event), MessageID: strings.TrimSpace(*messageID)}
	if hasUser {
		input.UserID = strings.TrimSpace(*user)
	} else {
		socketIDs, withKey, targetErr := realtimeSocketTargets(connections.values)
		if targetErr != nil {
			return targetErr
		}
		if withKey > 0 {
			switch {
			case input.Event == realtimeE2EEvent:
				_, _ = fmt.Fprintf(rtErr, "Removed the client key from %d end-to-end connection id(s); it never leaves this machine.\n", withKey)
			case *stripKey:
				_, _ = fmt.Fprintf(rtErr, "Warning: sending plaintext to the socket ids of %d end-to-end connection id(s); their client key was removed locally and was not sent.\n", withKey)
			default:
				return fmt.Errorf("invalid arguments: "+realtimeE2ERefusal, withKey)
			}
		}
		input.SocketIDs = socketIDs
	}
	payload, err := readRealtimeEventData(data, *dataFile)
	if err != nil {
		return err
	}
	input.Data = payload
	target, err := resolveRealtimeTarget(positionals[0], *projectRef)
	if err != nil {
		return err
	}
	app, err := resolveRealtimeApp(target.token, target.instance, *appRef)
	if err != nil {
		return err
	}
	result, err := sendRealtimeEvent(target.token, target.instance.ProjectID, target.instance.ID, app.AppID, input)
	if err != nil {
		return realtimeAPIError(err)
	}
	if *asJSON {
		return rtPrintJSON(result)
	}
	_, _ = fmt.Fprintf(rtOut, "Sent '%s' (%d bytes, message %s): %d delivered, %d buffered, %d queued, %d offline.\n",
		input.Event, result.Bytes, result.MessageID, result.Counts.Delivered, result.Counts.Buffered, result.Counts.Queued, result.Counts.Offline)
	if len(result.Results) > 0 {
		tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "TARGET\tSTATUS\tCONNECTIONS")
		for _, item := range result.Results {
			targetName := "-"
			if item.SocketID != nil {
				targetName = *item.SocketID
			} else if item.UserID != nil {
				targetName = "user " + *item.UserID
			}
			connectionsText := "-"
			if item.Connections != nil {
				connectionsText = fmt.Sprintf("%d", *item.Connections)
			}
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", targetName, item.Status, connectionsText)
		}
		_ = tw.Flush()
	}
	if result.Notice != nil && *result.Notice != "" {
		_, _ = fmt.Fprintln(rtErr, *result.Notice)
	}
	return nil
}

// ---------------------------------------------------------------------------
// insights
// ---------------------------------------------------------------------------

func realtimeInsightsFlow(args []string) error {
	const usage = "hubfly realtime insights <server> [--app <app>] [--window <duration>] [--project <id|name>] [--json]"
	fs := newRealtimeFlags("insights")
	projectRef := fs.String("project", "", "")
	appRef := fs.String("app", "", "")
	window := fs.String("window", "1h", "")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return fmt.Errorf("usage: %s", usage)
	}
	windowSeconds, err := parseRealtimeDuration(*window)
	if err != nil || windowSeconds < 60 || windowSeconds > 86400 {
		return fmt.Errorf("invalid --window %q: use 1m to 24h", *window)
	}
	target, err := resolveRealtimeTarget(positionals[0], *projectRef)
	if err != nil {
		return err
	}
	appID := ""
	if strings.TrimSpace(*appRef) != "" {
		app, appErr := resolveRealtimeApp(target.token, target.instance, *appRef)
		if appErr != nil {
			return appErr
		}
		appID = app.AppID
	}
	insights, err := fetchRealtimeInsights(target.token, target.instance.ProjectID, target.instance.ID, appID, windowSeconds)
	if err != nil {
		return realtimeAPIError(err)
	}
	if *asJSON {
		return rtPrintJSON(insights)
	}
	scope := "all apps"
	if appID != "" {
		scope = "app " + appID
	}
	_, _ = fmt.Fprintf(rtOut, "Insights for '%s' (%s), last %s\n", target.instance.Name, scope, formatRealtimeSeconds(insights.WindowSeconds))
	_, _ = fmt.Fprintf(rtOut, "Peak connections: %d\n", insights.ConnectionsPeak)

	section := func(title string) *tabwriter.Writer {
		_, _ = fmt.Fprintf(rtOut, "\n%s\n", title)
		return tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	}
	if len(insights.TopChannels) > 0 {
		tw := section("Top channels")
		_, _ = fmt.Fprintln(tw, "CHANNEL\tMESSAGES\tBYTES\tPEAK SUBSCRIBERS")
		for _, channel := range insights.TopChannels {
			_, _ = fmt.Fprintf(tw, "%s\t%d\t%d\t%d\n", channel.Name, channel.Messages, channel.Bytes, channel.SubscribersPeak)
		}
		_ = tw.Flush()
	}
	if len(insights.TopEvents) > 0 {
		tw := section("Top events")
		_, _ = fmt.Fprintln(tw, "EVENT\tCOUNT\tBYTES")
		for _, event := range insights.TopEvents {
			_, _ = fmt.Fprintf(tw, "%s\t%d\t%d\n", event.Name, event.Count, event.Bytes)
		}
		_ = tw.Flush()
	}
	if len(insights.AuthFailures) > 0 {
		tw := section("Auth failures")
		_, _ = fmt.Fprintln(tw, "REASON\tCOUNT")
		for _, failure := range insights.AuthFailures {
			_, _ = fmt.Fprintf(tw, "%s\t%d\n", failure.Reason, failure.Count)
		}
		_ = tw.Flush()
	}
	// The engine reports every counter (closes by reason name, rate limits
	// by kind), mostly zero: only non-zero ones are listed.
	for _, counts := range []struct {
		title  string
		header string
		values map[string]int64
	}{
		{"Rate limited", "LIMIT\tCOUNT", insights.RateLimited},
		{"Connection closes", "REASON\tCOUNT", insights.Closes},
	} {
		keys := []string{}
		for _, key := range sortedKeys(counts.values) {
			if counts.values[key] != 0 {
				keys = append(keys, key)
			}
		}
		if len(keys) == 0 {
			continue
		}
		tw := section(counts.title)
		_, _ = fmt.Fprintln(tw, counts.header)
		for _, key := range keys {
			_, _ = fmt.Fprintf(tw, "%s\t%d\n", key, counts.values[key])
		}
		_ = tw.Flush()
	}
	if len(insights.Libraries) > 0 {
		tw := section("Client libraries")
		_, _ = fmt.Fprintln(tw, "LIBRARY\tVERSION\tCONNECTIONS")
		for _, library := range insights.Libraries {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%d\n", library.Name, library.Version, library.Count)
		}
		_ = tw.Flush()
	}
	successRate, latency := "-", "-"
	if insights.Webhooks.SuccessRate != nil {
		successRate = fmt.Sprintf("%.1f%%", *insights.Webhooks.SuccessRate*100)
	}
	if insights.Webhooks.LatencyMsP95 != nil {
		latency = fmt.Sprintf("%.0f ms", *insights.Webhooks.LatencyMsP95)
	}
	_, _ = fmt.Fprintf(rtOut, "\nWebhooks: success %s, p95 latency %s, backlog %d\n", successRate, latency, insights.Webhooks.Backlog)
	return nil
}

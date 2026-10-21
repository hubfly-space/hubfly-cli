package cli

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

// `hubfly realtime channels|history|connections|disconnect|usage`: the live
// state of a realtime server (occupied channels and presence, retained
// history, connections), terminations, and daily usage. The Dashboard reads
// them from the engine; nothing here is cached.

// ---------------------------------------------------------------------------
// channels
// ---------------------------------------------------------------------------

func runRealtimeChannelsGroup(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "help", "--help", "-h":
			printRealtimeUsageFor("hubfly realtime channels")
			return nil
		case "list", "ls":
			return realtimeChannelsListFlow(args[1:])
		case "inspect", "info", "show":
			return realtimeChannelsInspectFlow(args[1:])
		}
	}
	return realtimeChannelsListFlow(args)
}

func realtimeChannelsListFlow(args []string) error {
	const usage = "hubfly realtime channels <server> [--app <app>] [--prefix <prefix>] [--limit <n>] [--project <id|name>] [--json]"
	const maxChannels = 10000
	fs := newRealtimeFlags("channels")
	prefixText := fs.String("prefix", "", "")
	limit := fs.Int("limit", 100, "")
	asJSON := fs.Bool("json", false, "")
	command, err := parseRealtimeKeyCommand(fs, args, usage, 1, 1)
	if err != nil {
		return err
	}
	prefix := strings.TrimSpace(*prefixText)
	if prefix != "" && !realtimeChannelPrefixPattern.MatchString(prefix) {
		return fmt.Errorf("invalid --prefix %q: use the beginning of a channel name (a-z, A-Z, 0-9 and -_=@,.;#)", *prefixText)
	}
	if err := checkRealtimeLimit("limit", *limit, maxChannels); err != nil {
		return err
	}
	parsed, err := command.resolve()
	if err != nil {
		return err
	}
	var total *int64
	channels, next, err := collectRealtimePages(*limit, 1000, func(size int, cursor string) ([]rtChannel, *string, error) {
		query := url.Values{"limit": {strconv.Itoa(size)}}
		if prefix != "" {
			query.Set("prefix", prefix)
		}
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		page, pageErr := fetchRealtimeChannels(parsed.target.token, parsed.target.instance.ProjectID, parsed.target.instance.ID, parsed.app.AppID, query)
		if page.Total != nil {
			total = page.Total
		}
		return page.Items, page.NextCursor, pageErr
	})
	if err != nil {
		return realtimeAPIError(err)
	}
	if *asJSON {
		return rtPrintSafeJSON(rtChannelPage{Items: channels, NextCursor: next, Total: total})
	}
	if len(channels) == 0 {
		scope := ""
		if prefix != "" {
			scope = fmt.Sprintf(" starting with '%s'", prefix)
		}
		_, _ = fmt.Fprintf(rtOut, "No occupied channels%s in app '%s'.\n", scope, parsed.app.Name)
		return nil
	}
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAME\tTYPE\tSUBSCRIBERS\tUSERS\tOCCUPIED SINCE")
	for _, channel := range channels {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", rtCell(channel.Name), rtCell(channel.Type),
			formatThousands64(channel.SubscriptionCount), rtCount(channel.UserCount), rtCellPtr(channel.OccupiedAt))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if next != nil || (total != nil && *total > int64(len(channels))) {
		shown := fmt.Sprintf("Showing %s", formatThousands(len(channels)))
		if total != nil {
			shown += " of " + formatThousands64(*total)
		}
		_, _ = fmt.Fprintf(rtOut, "%s occupied channels; raise --limit or narrow with --prefix.\n", shown)
	}
	return nil
}

// describeRealtimeHistoryPosition renders "durable, epoch e7k2m9, offsets 3-45".
func describeRealtimeHistoryPosition(mode string, epoch *string, oldest, head *int64) string {
	if mode == "" || mode == "off" {
		return "off"
	}
	parts := []string{mode}
	if epoch != nil && *epoch != "" {
		parts = append(parts, "epoch "+*epoch)
	}
	switch {
	case oldest != nil && head != nil && *oldest > 0:
		parts = append(parts, fmt.Sprintf("offsets %d-%d", *oldest, *head))
	case head != nil:
		parts = append(parts, fmt.Sprintf("head offset %d", *head))
	}
	return rtCell(strings.Join(parts, ", "))
}

func describeRealtimeChannelCache(cache rtChannelCacheState) string {
	if !cache.Present {
		return "no cached event"
	}
	text := "holds the last event"
	if cache.Event != nil && *cache.Event != "" {
		text += " '" + rtShorten(*cache.Event, 60) + "'"
	}
	if cache.CachedAt != nil && *cache.CachedAt != "" {
		text += " from " + *cache.CachedAt
	}
	return rtCell(text)
}

func realtimeChannelsInspectFlow(args []string) error {
	const usage = "hubfly realtime channels inspect <server> <channel> [--app <app>] [--members <n>] [--project <id|name>] [--json]"
	const maxMembers = 1000
	fs := newRealtimeFlags("channels inspect")
	members := fs.Int("members", 0, "")
	asJSON := fs.Bool("json", false, "")
	command, err := parseRealtimeKeyCommand(fs, args, usage, 2, 2)
	if err != nil {
		return err
	}
	channel, err := checkRealtimeChannel("channel", command.rest[0])
	if err != nil {
		return err
	}
	if *members != 0 {
		if err := checkRealtimeLimit("members", *members, maxMembers); err != nil {
			return err
		}
	}
	parsed, err := command.resolve()
	if err != nil {
		return err
	}
	detail, err := inspectRealtimeChannel(parsed.target.token, parsed.target.instance.ProjectID, parsed.target.instance.ID, parsed.app.AppID, channel, *members)
	if err != nil {
		return realtimeAPIError(err)
	}
	if *asJSON {
		return rtPrintSafeJSON(detail)
	}
	occupied := "no"
	if detail.Occupied {
		occupied = "yes, " + rtPlural(detail.SubscriptionCount, "subscription", "subscriptions")
	}
	rows := [][2]string{
		{"Channel", rtCell(detail.Name)},
		{"App", fmt.Sprintf("%s (%s)", parsed.app.Name, parsed.app.AppID)},
		{"Type", rtCell(detail.Type)},
		{"Occupied", occupied},
	}
	if detail.UserCount != nil {
		rows = append(rows, [2]string{"Users", formatThousands64(*detail.UserCount)})
	}
	if detail.History != nil {
		history := describeRealtimeHistoryPosition(detail.History.Mode, detail.History.Epoch, detail.History.Oldest, detail.History.Head)
		if history != "off" {
			history += fmt.Sprintf(" (%s, %s retained)", rtPlural(detail.History.RetainedMessages, "message", "messages"), formatBytes(detail.History.RetainedBytes))
		}
		rows = append(rows, [2]string{"History", history})
	}
	if detail.Cache != nil {
		rows = append(rows, [2]string{"Cache", describeRealtimeChannelCache(*detail.Cache)})
	}
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	for _, row := range rows {
		_, _ = fmt.Fprintf(tw, "%s:\t%s\n", row[0], row[1])
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if len(detail.Members) > 0 {
		_, _ = fmt.Fprintln(rtOut)
		tw = tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "USER ID\tCONNECTIONS\tUSER INFO")
		for _, member := range detail.Members {
			info := "-"
			if member.UserInfo != nil {
				info = rtCell(rtShorten(*member.UserInfo, 60))
			}
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", rtCell(member.UserID), formatThousands64(member.Connections), info)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}
	if detail.MembersTruncated {
		_, _ = fmt.Fprintf(rtOut, "More members are present; raise --members (up to %s).\n", formatThousands(maxMembers))
	}
	return nil
}

// ---------------------------------------------------------------------------
// history
// ---------------------------------------------------------------------------

func runRealtimeHistoryGroup(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "help", "--help", "-h":
			printRealtimeUsageFor("hubfly realtime history")
			return nil
		case "purge":
			return realtimeHistoryPurgeFlow(args[1:])
		}
	}
	return realtimeHistoryFlow(args)
}

// realtimeHistoryEncrypted reports a message HubFly cannot read: a HubFly v1
// envelope (event hubfly.e2e) or any message of an encrypted channel, which
// carries a Pusher envelope (E2E.md "Envelopes").
func realtimeHistoryEncrypted(channel string, message rtHistoryMessage) bool {
	return message.Event == realtimeE2EEvent || message.Event == "client-"+realtimeE2EEvent ||
		strings.HasPrefix(channel, "private-encrypted-")
}

func realtimeHistoryFlow(args []string) error {
	const usage = "hubfly realtime history <server> --channel <name> [--app <app>] [--limit <n>] [--project <id|name>] [--json]"
	const maxMessages = 1000
	fs := newRealtimeFlags("history")
	channelRef := fs.String("channel", "", "")
	fs.StringVar(channelRef, "c", "", "")
	limit := fs.Int("limit", 0, "")
	asJSON := fs.Bool("json", false, "")
	command, err := parseRealtimeKeyCommand(fs, args, usage, 1, 1)
	if err != nil {
		return err
	}
	if strings.TrimSpace(*channelRef) == "" {
		return fmt.Errorf("usage: %s", usage)
	}
	channel, err := checkRealtimeChannel("--channel", *channelRef)
	if err != nil {
		return err
	}
	if *limit != 0 {
		if err := checkRealtimeLimit("limit", *limit, maxMessages); err != nil {
			return err
		}
	}
	parsed, err := command.resolve()
	if err != nil {
		return err
	}
	history, err := fetchRealtimeHistory(parsed.target.token, parsed.target.instance.ProjectID, parsed.target.instance.ID, parsed.app.AppID, channel, *limit)
	if err != nil {
		return realtimeAPIError(err)
	}
	if *asJSON {
		return rtPrintSafeJSON(history)
	}
	if (history.Mode == "" || history.Mode == "off") && len(history.Messages) == 0 {
		_, _ = fmt.Fprintf(rtOut, "Channel '%s' keeps no history in app '%s'. Turn history on with: hubfly realtime apps update %s %s --history durable\n",
			channel, parsed.app.Name, parsed.target.instance.Name, parsed.app.AppID)
		return nil
	}
	_, _ = fmt.Fprintf(rtOut, "Channel '%s' in app '%s': history %s\n", channel, parsed.app.Name,
		describeRealtimeHistoryPosition(history.Mode, history.Epoch, history.Oldest, history.Head))
	if len(history.Messages) == 0 {
		_, _ = fmt.Fprintln(rtOut, "No retained messages.")
		return nil
	}
	encrypted := 0
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "OFFSET\tAT\tEVENT\tSIZE\tDATA")
	for _, message := range history.Messages {
		data := rtCell(rtShorten(message.Data, 60))
		if realtimeHistoryEncrypted(channel, message) {
			encrypted++
			data = "(end-to-end encrypted)"
		}
		_, _ = fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\n", message.Offset, rtCellPtr(message.PublishedAt),
			rtCell(rtShorten(message.Event, 40)), formatBytes(message.Size), data)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if encrypted > 0 {
		_, _ = fmt.Fprintf(rtOut, "%s end-to-end encrypted: HubFly cannot read them. Decrypt them with your own keys (for example history() in the HubFly server SDK).\n",
			rtPlural(int64(encrypted), "message is", "messages are"))
	}
	if history.Truncated {
		_, _ = fmt.Fprintf(rtOut, "Older messages are retained; raise --limit (up to %s).\n", formatThousands(maxMessages))
	}
	if history.Gap {
		_, _ = fmt.Fprintln(rtOut, "Some messages expired or the epoch changed (gap).")
	}
	return nil
}

func realtimeHistoryPurgeFlow(args []string) error {
	const usage = "hubfly realtime history purge <server> [--channel <name>] [--app <app>] [--project <id|name>] [--yes]"
	fs := newRealtimeFlags("history purge")
	channelRef := fs.String("channel", "", "")
	fs.StringVar(channelRef, "c", "", "")
	yes := fs.Bool("yes", false, "")
	fs.BoolVar(yes, "y", false, "")
	command, err := parseRealtimeKeyCommand(fs, args, usage, 1, 1)
	if err != nil {
		return err
	}
	channel := ""
	if strings.TrimSpace(*channelRef) != "" {
		if channel, err = checkRealtimeChannel("--channel", *channelRef); err != nil {
			return err
		}
	}
	parsed, err := command.resolve()
	if err != nil {
		return err
	}
	// Without --channel the Dashboard purges every channel of the app.
	question := fmt.Sprintf("Delete the retained history of channel '%s' in app '%s'? Clients can no longer recover those messages.", channel, parsed.app.Name)
	if channel == "" {
		question = fmt.Sprintf("Delete the retained history of every channel in app '%s'? Clients can no longer recover those messages.", parsed.app.Name)
	}
	confirmed, err := confirmRealtimeExtras(*yes, "purge", question)
	if err != nil || !confirmed {
		return err
	}
	purged, err := purgeRealtimeHistory(parsed.target.token, parsed.target.instance.ProjectID, parsed.target.instance.ID, parsed.app.AppID, channel)
	if err != nil {
		return realtimeAPIError(err)
	}
	if channel == "" {
		_, _ = fmt.Fprintf(rtOut, "Purged %s from every channel of app '%s'.\n", rtPlural(purged, "message", "messages"), parsed.app.Name)
		return nil
	}
	_, _ = fmt.Fprintf(rtOut, "Purged %s from channel '%s'.\n", rtPlural(purged, "message", "messages"), channel)
	return nil
}

// ---------------------------------------------------------------------------
// connections / disconnect
// ---------------------------------------------------------------------------

func realtimeConnectionLibrary(connection rtConnection) string {
	name, version := "", ""
	if connection.Client.Name != nil {
		name = rtShorten(*connection.Client.Name, 32)
	}
	if connection.Client.Version != nil {
		version = rtShorten(*connection.Client.Version, 16)
	}
	return rtCell(strings.TrimSpace(name + " " + version))
}

func realtimeConnectionsFlow(args []string) error {
	const usage = "hubfly realtime connections <server> [--app <app>] [--user <id>] [--channel <name>] [--limit <n>] [--project <id|name>] [--json]"
	const maxConnections = 10000
	fs := newRealtimeFlags("connections")
	userRef := fs.String("user", "", "")
	channelRef := fs.String("channel", "", "")
	limit := fs.Int("limit", 100, "")
	asJSON := fs.Bool("json", false, "")
	command, err := parseRealtimeKeyCommand(fs, args, usage, 1, 1)
	if err != nil {
		return err
	}
	userID, channel := "", ""
	if strings.TrimSpace(*userRef) != "" {
		if userID, err = checkRealtimeUserID("--user", *userRef); err != nil {
			return err
		}
	}
	if strings.TrimSpace(*channelRef) != "" {
		if channel, err = checkRealtimeChannel("--channel", *channelRef); err != nil {
			return err
		}
	}
	if err := checkRealtimeLimit("limit", *limit, maxConnections); err != nil {
		return err
	}
	target, err := resolveRealtimeTarget(command.serverRef, command.projectRef)
	if err != nil {
		return err
	}
	// User ids and channel names belong to an app: those filters need one.
	apps, app, err := resolveRealtimeExtrasApps(target, command.appRef, userID != "" || channel != "")
	if err != nil {
		return err
	}
	connections, next, err := collectRealtimePages(*limit, 1000, func(size int, cursor string) ([]rtConnection, *string, error) {
		query := url.Values{"limit": {strconv.Itoa(size)}}
		if app != nil {
			query.Set("appId", app.AppID)
		}
		if userID != "" {
			query.Set("userId", userID)
		}
		if channel != "" {
			query.Set("channel", channel)
		}
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		page, pageErr := fetchRealtimeConnections(target.token, target.instance.ProjectID, target.instance.ID, query)
		return page.Items, page.NextCursor, pageErr
	})
	if err != nil {
		return realtimeAPIError(err)
	}
	// The engine knows socket ids only; should an end-to-end connection id
	// (<socket_id>~<key>) ever appear, its client key is never printed.
	for index := range connections {
		connections[index].SocketID, _ = splitRealtimeConnectionID(connections[index].SocketID)
	}
	if *asJSON {
		return rtPrintSafeJSON(rtConnectionPage{Items: connections, NextCursor: next})
	}
	if len(connections) == 0 {
		_, _ = fmt.Fprintf(rtOut, "No open connections on realtime server '%s' match.\n", target.instance.Name)
		return nil
	}
	names := realtimeAppNames(apps)
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "SOCKET ID\tAPP\tUSER\tSTATE\tCHANNELS\tPROTOCOL\tLIBRARY\tCONNECTED")
	for _, connection := range connections {
		appName := names[connection.AppID]
		if appName == "" {
			appName = connection.AppID
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", rtCell(connection.SocketID), rtCell(appName),
			rtCellPtr(connection.UserID), rtCell(connection.State), formatThousands64(connection.SubscriptionCount),
			rtCell(connection.Protocol), realtimeConnectionLibrary(connection), rtCellPtr(connection.ConnectedAt))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if next != nil {
		_, _ = fmt.Fprintf(rtOut, "More connections exist; raise --limit (up to %s) or filter with --app, --user or --channel.\n", formatThousands(maxConnections))
	}
	return nil
}

func realtimeDisconnectFlow(args []string) error {
	const usage = "hubfly realtime disconnect <server> (--connection <socketId>...|--user <id>) [--mode terminate|disconnect] [--app <app>] [--project <id|name>] [--yes]"
	fs := newRealtimeFlags("disconnect")
	connections := &rtList{split: true}
	fs.Var(connections, "connection", "")
	userRef := fs.String("user", "", "")
	modeText := fs.String("mode", "", "")
	yes := fs.Bool("yes", false, "")
	fs.BoolVar(yes, "y", false, "")
	command, err := parseRealtimeKeyCommand(fs, args, usage, 1, 1)
	if err != nil {
		return err
	}
	hasUser := strings.TrimSpace(*userRef) != ""
	if (len(connections.values) > 0) == hasUser {
		return fmt.Errorf("invalid arguments: name --connection ids or one --user, not both or neither\nusage: %s", usage)
	}
	// terminate (the API default) closes with 4009: the client stops and its
	// session is discarded. disconnect closes with 4200: the client
	// reconnects at once and HubFly SDK clients resume their session.
	mode := strings.ToLower(strings.TrimSpace(*modeText))
	if mode != "" && mode != "terminate" && mode != "disconnect" {
		return fmt.Errorf("invalid --mode %q (expected terminate or disconnect)", *modeText)
	}
	if hasUser && mode == "disconnect" {
		return errors.New("invalid arguments: --mode disconnect applies to --connection; a user's connections are always terminated")
	}
	userID := ""
	var socketIDs []string
	withKey := 0
	if hasUser {
		if userID, err = checkRealtimeUserID("--user", *userRef); err != nil {
			return err
		}
	} else if socketIDs, withKey, err = realtimeSocketTargets(connections.values); err != nil {
		return err
	}
	parsed, err := command.resolve()
	if err != nil {
		return err
	}
	if withKey > 0 {
		_, _ = fmt.Fprintf(rtErr, "Removed the client key from %d end-to-end connection id(s); it never leaves this machine.\n", withKey)
	}
	if hasUser {
		return terminateRealtimeUserFlow(parsed, userID, *yes)
	}
	verb, question := "terminate", fmt.Sprintf("Terminate %s of app '%s'? The clients stop and do not reconnect on their own.",
		rtPlural(int64(len(socketIDs)), "connection", "connections"), parsed.app.Name)
	if mode == "disconnect" {
		verb, question = "disconnect", fmt.Sprintf("Disconnect %s of app '%s'? The clients reconnect at once.",
			rtPlural(int64(len(socketIDs)), "connection", "connections"), parsed.app.Name)
	}
	confirmed, err := confirmRealtimeExtras(*yes, verb, question)
	if err != nil || !confirmed {
		return err
	}
	closed := 0
	for _, socketID := range socketIDs {
		result, callErr := terminateRealtimeConnection(parsed.target.token, parsed.target.instance.ProjectID, parsed.target.instance.ID, parsed.app.AppID, socketID, mode)
		var apiErr *apiError
		if callErr != nil && !(errors.As(callErr, &apiErr) && apiErr.Code == "MANAGED_REALTIME_CONNECTION_NOT_FOUND") {
			return realtimeAPIError(callErr)
		}
		if callErr != nil || !result.Terminated {
			_, _ = fmt.Fprintf(rtOut, "Connection %s is not open.\n", socketID)
			continue
		}
		closed++
		if result.Mode == "disconnect" {
			_, _ = fmt.Fprintf(rtOut, "Disconnected %s (the client reconnects).\n", socketID)
		} else {
			_, _ = fmt.Fprintf(rtOut, "Terminated %s (the client stops).\n", socketID)
		}
	}
	if closed < len(socketIDs) {
		return fmt.Errorf("%d of %d connections were not open", len(socketIDs)-closed, len(socketIDs))
	}
	return nil
}

func terminateRealtimeUserFlow(parsed realtimeKeyArgs, userID string, yes bool) error {
	confirmed, err := confirmRealtimeExtras(yes, "terminate", fmt.Sprintf(
		"Terminate every connection and session of user '%s' in app '%s'? Their clients stop and do not reconnect on their own.",
		userID, parsed.app.Name))
	if err != nil || !confirmed {
		return err
	}
	terminated, err := terminateRealtimeUser(parsed.target.token, parsed.target.instance.ProjectID, parsed.target.instance.ID, parsed.app.AppID, userID)
	if err != nil {
		return realtimeAPIError(err)
	}
	if terminated == 0 {
		_, _ = fmt.Fprintf(rtOut, "User '%s' had no open connections or sessions in app '%s'.\n", userID, parsed.app.Name)
		return nil
	}
	_, _ = fmt.Fprintf(rtOut, "Terminated %s of user '%s' (the clients stop).\n", rtPlural(terminated, "connection", "connections"), userID)
	return nil
}

// ---------------------------------------------------------------------------
// usage
// ---------------------------------------------------------------------------

// realtimeUsageNow is the clock --since counts back from (tests set it).
var realtimeUsageNow = time.Now

// realtimeUsageMaxDays: the engine keeps daily usage for 400 days.
const realtimeUsageMaxDays = 400

func utcDay(t time.Time) time.Time {
	year, month, day := t.UTC().Date()
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

// realtimeUsageSince turns --since (a duration such as 7d or 720h, or a
// YYYY-MM-DD date) into the first UTC day to report.
func realtimeUsageSince(value string, now time.Time) (string, error) {
	value = strings.TrimSpace(value)
	var day time.Time
	if parsed, err := time.Parse("2006-01-02", value); err == nil {
		day = parsed
	} else {
		// A bare number has no obvious unit here (days? seconds?): ask for one.
		if _, numErr := strconv.Atoi(value); numErr == nil {
			return "", fmt.Errorf("invalid --since %q: add a unit (for example 7d or 720h) or give a date (YYYY-MM-DD)", value)
		}
		seconds, durErr := parseRealtimeDuration(value)
		if durErr != nil {
			return "", fmt.Errorf("invalid --since %q: use a duration such as 7d or 720h, or a date (YYYY-MM-DD)", value)
		}
		day = utcDay(now.Add(-time.Duration(seconds) * time.Second))
	}
	today := utcDay(now)
	if day.After(today) {
		return "", fmt.Errorf("invalid --since %q: the date is in the future", value)
	}
	if day.Before(today.AddDate(0, 0, -(realtimeUsageMaxDays - 1))) {
		return "", fmt.Errorf("invalid --since %q: daily usage is kept for %d days", value, realtimeUsageMaxDays)
	}
	return day.Format("2006-01-02"), nil
}

func realtimeUsageFlow(args []string) error {
	const usage = "hubfly realtime usage <server> [--app <app>] [--since <duration|YYYY-MM-DD>] [--project <id|name>] [--json]"
	fs := newRealtimeFlags("usage")
	sinceText := fs.String("since", "", "")
	asJSON := fs.Bool("json", false, "")
	command, err := parseRealtimeKeyCommand(fs, args, usage, 1, 1)
	if err != nil {
		return err
	}
	since := ""
	if strings.TrimSpace(*sinceText) != "" {
		if since, err = realtimeUsageSince(*sinceText, realtimeUsageNow()); err != nil {
			return err
		}
	}
	target, err := resolveRealtimeTarget(command.serverRef, command.projectRef)
	if err != nil {
		return err
	}
	apps, app, err := resolveRealtimeExtrasApps(target, command.appRef, false)
	if err != nil {
		return err
	}
	appID := ""
	if app != nil {
		appID = app.AppID
	}
	report, err := fetchRealtimeUsage(target.token, target.instance.ProjectID, target.instance.ID, appID, since)
	if err != nil {
		return realtimeAPIError(err)
	}
	if *asJSON {
		return rtPrintSafeJSON(report)
	}
	period := "the last 30 days"
	if since != "" {
		period = "since " + since
	}
	scope := "all apps"
	if app != nil {
		scope = fmt.Sprintf("app '%s'", app.Name)
	}
	if len(report.Days) == 0 {
		_, _ = fmt.Fprintf(rtOut, "No usage recorded for %s of '%s' %s.\n", scope, target.instance.Name, period)
		return nil
	}
	_, _ = fmt.Fprintf(rtOut, "Daily usage of '%s' (%s), %s (UTC days):\n", target.instance.Name, scope, period)
	names := realtimeAppNames(apps)
	var total rtUsageDay
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "DAY\tAPP\tPEAK CONNECTIONS\tCONNECTION MINUTES\tMESSAGES IN\tMESSAGES OUT\tBYTES IN\tBYTES OUT\tWEBHOOKS")
	row := func(day, app string, values rtUsageDay) {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", day, app,
			formatThousands64(values.PeakConnections), formatThousands64(values.ConnectionMinutes),
			formatThousands64(values.MessagesIn), formatThousands64(values.MessagesOut),
			formatBytes(values.BytesIn), formatBytes(values.BytesOut), formatThousands64(values.WebhookDeliveries))
	}
	for _, day := range report.Days {
		appName := names[day.AppID]
		if appName == "" {
			appName = day.AppID
		}
		row(rtCell(day.Day), rtCell(appName), day)
		if day.PeakConnections > total.PeakConnections {
			total.PeakConnections = day.PeakConnections
		}
		total.ConnectionMinutes += day.ConnectionMinutes
		total.MessagesIn += day.MessagesIn
		total.MessagesOut += day.MessagesOut
		total.BytesIn += day.BytesIn
		total.BytesOut += day.BytesOut
		total.WebhookDeliveries += day.WebhookDeliveries
	}
	if len(report.Days) > 1 {
		row("TOTAL", "", total)
	}
	return tw.Flush()
}

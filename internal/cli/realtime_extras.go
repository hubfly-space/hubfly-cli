package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// `hubfly realtime` webhooks, channels, history, connections, disconnect and
// usage. The commands live in realtime_extras_webhooks.go and
// realtime_extras_inspect.go, the API calls in realtime_extras_api.go.

func runRealtimeExtrasCommand(command string, args []string) error {
	switch command {
	case "webhooks", "webhook", "hooks":
		return runRealtimeWebhooksGroup(args)
	case "channels", "channel":
		return runRealtimeChannelsGroup(args)
	case "history":
		return runRealtimeHistoryGroup(args)
	case "connections":
		return realtimeConnectionsFlow(args)
	case "disconnect":
		return realtimeDisconnectFlow(args)
	case "usage":
		return realtimeUsageFlow(args)
	default:
		return fmt.Errorf("unknown realtime command %q (run: hubfly realtime help)", command)
	}
}

// printRealtimeUsageFor prints the usage lines of one command group, e.g.
// "hubfly realtime webhooks".
func printRealtimeUsageFor(prefix string) {
	for _, line := range strings.Split(realtimeUsageText, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == prefix || strings.HasPrefix(trimmed, prefix+" ") {
			_, _ = fmt.Fprintln(rtOut, trimmed)
		}
	}
}

// ---------------------------------------------------------------------------
// Names (WIRE.md "Names and limits"), checked before any request
// ---------------------------------------------------------------------------

const realtimeUserChannelPrefix = "#server-to-user-"

var (
	realtimeChannelPattern       = regexp.MustCompile(`^[-a-zA-Z0-9_=@,.;]{1,200}$`)
	realtimeChannelPrefixPattern = regexp.MustCompile(`^[-a-zA-Z0-9_=@,.;#]{1,200}$`)
	realtimeUserIDPattern        = regexp.MustCompile(`^[-a-zA-Z0-9_=@,.;]{1,128}$`)
	realtimeWebhookIDPattern     = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	realtimeDeliveryIDPattern    = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
)

func validRealtimeChannelName(name string) bool {
	if strings.HasPrefix(name, realtimeUserChannelPrefix) {
		return realtimeUserIDPattern.MatchString(strings.TrimPrefix(name, realtimeUserChannelPrefix))
	}
	return realtimeChannelPattern.MatchString(name)
}

// checkRealtimeChannel validates a channel name; label is how the user gave
// it ("--channel" or "channel").
func checkRealtimeChannel(label, value string) (string, error) {
	value = strings.TrimSpace(value)
	if !validRealtimeChannelName(value) {
		return "", fmt.Errorf("invalid %s %q: channel names are 1-200 characters from a-z, A-Z, 0-9 and -_=@,.; (# only in #server-to-user-<id>)", label, value)
	}
	return value, nil
}

func checkRealtimeUserID(label, value string) (string, error) {
	value = strings.TrimSpace(value)
	if !realtimeUserIDPattern.MatchString(value) {
		return "", fmt.Errorf("invalid %s %q: user ids are 1-128 characters from a-z, A-Z, 0-9 and -_=@,.;", label, value)
	}
	return value, nil
}

func checkRealtimeLimit(flagName string, value, upper int) error {
	if value < 1 || value > upper {
		return fmt.Errorf("invalid --%s %d: use 1 to %s", flagName, value, formatThousands(upper))
	}
	return nil
}

// ---------------------------------------------------------------------------
// Resolution, paging, confirmation
// ---------------------------------------------------------------------------

// resolveRealtimeExtrasApps returns the server's apps and, when ref is given
// or required is set, the chosen app (the only one when ref is empty).
func resolveRealtimeExtrasApps(target realtimeTarget, ref string, required bool) ([]rtApp, *rtApp, error) {
	apps, err := fetchRealtimeApps(target.token, target.instance.ProjectID, target.instance.ID)
	if err != nil {
		return nil, nil, realtimeAPIError(err)
	}
	if strings.TrimSpace(ref) == "" && !required {
		return apps, nil, nil
	}
	app, err := pickRealtimeApp(apps, target.instance.Name, ref)
	if err != nil {
		return nil, nil, err
	}
	return apps, &app, nil
}

func realtimeAppNames(apps []rtApp) map[string]string {
	names := make(map[string]string, len(apps))
	for _, app := range apps {
		names[app.AppID] = app.Name
	}
	return names
}

// collectRealtimePages fetches cursor pages of at most pageMax items until
// limit items are collected or the last page is read. The returned cursor is
// non-nil when more items exist.
func collectRealtimePages[T any](limit, pageMax int, fetch func(size int, cursor string) ([]T, *string, error)) ([]T, *string, error) {
	items := []T{}
	cursor := ""
	for {
		page, next, err := fetch(min(limit-len(items), pageMax), cursor)
		if err != nil {
			return nil, nil, err
		}
		items = append(items, page...)
		if next == nil || *next == "" {
			return items, nil, nil
		}
		if len(items) >= limit || len(page) == 0 {
			if len(items) > limit {
				items = items[:limit]
			}
			return items, next, nil
		}
		cursor = *next
	}
}

// confirmRealtimeExtras asks a yes/no question unless yes is set. Without a
// terminal it fails and asks for --yes (verb names the action in that hint).
func confirmRealtimeExtras(yes bool, verb, question string) (bool, error) {
	if yes {
		return true, nil
	}
	if !rtInteractive() {
		return false, fmt.Errorf("invalid arguments: confirmation required; re-run with --yes to %s", verb)
	}
	confirmed, err := promptYesNo(question, false)
	if err != nil {
		return false, err
	}
	if !confirmed {
		_, _ = fmt.Fprintln(rtOut, "Cancelled.")
	}
	return confirmed, nil
}

// ---------------------------------------------------------------------------
// Output
// ---------------------------------------------------------------------------

// rtPrintSafeJSON prints indented JSON whose strings carry no raw DEL, C1,
// bidi or other non-printable runes (they are \u-escaped, the values are
// unchanged): history payloads, presence info, event and client names come
// from end users and must not drive the terminal.
func rtPrintSafeJSON(value any) error {
	compact, err := json.Marshal(value)
	if err != nil {
		return err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, escapeJSONNonPrintable(compact), "", "  "); err != nil {
		return err
	}
	out.WriteByte('\n')
	_, err = rtOut.Write(out.Bytes())
	return err
}

// rtShorten cuts text to at most limit runes, ending in "…" when cut.
func rtShorten(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	runes := []rune(text)
	return string(runes[:limit-1]) + "…"
}

// rtCell renders a table cell: "-" when empty, quoted when the text holds
// tabs, newlines or other runes that could break the table or the terminal.
func rtCell(text string) string {
	if text == "" {
		return "-"
	}
	return terminalSafe(text)
}

func rtCellPtr(text *string) string {
	if text == nil {
		return "-"
	}
	return rtCell(*text)
}

func rtCount(value *int64) string {
	if value == nil {
		return "-"
	}
	return formatThousands64(*value)
}

func rtPlural(count int64, singular, plural string) string {
	if count == 1 {
		return "1 " + singular
	}
	return formatThousands64(count) + " " + plural
}

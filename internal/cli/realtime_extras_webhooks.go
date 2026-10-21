package cli

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
)

// `hubfly realtime webhooks`: the webhooks of an app (WIRE.md "Webhooks").
// The Dashboard stores them; the engine signs, queues, retries and logs the
// deliveries. Tables and messages show a webhook URL without its query
// string, where receivers often put tokens (--json prints what the API
// returns).

const realtimeWebhooksGroupUsage = "usage: hubfly realtime webhooks [list|create|update|delete|test|deliveries|redeliver] <server> ... (see: hubfly realtime webhooks help)"

var realtimeDeliveryStatuses = []string{"pending", "delivered", "failed", "dead"}

func runRealtimeWebhooksGroup(args []string) error {
	if len(args) == 0 {
		return errors.New(realtimeWebhooksGroupUsage)
	}
	switch args[0] {
	case "help", "--help", "-h":
		printRealtimeUsageFor("hubfly realtime webhooks")
		return nil
	case "list", "ls":
		return realtimeWebhooksListFlow(args[1:])
	case "create":
		return realtimeWebhooksCreateFlow(args[1:])
	case "update", "set":
		return realtimeWebhooksUpdateFlow(args[1:])
	case "delete", "rm":
		return realtimeWebhooksDeleteFlow(args[1:])
	case "test":
		return realtimeWebhooksTestFlow(args[1:])
	case "deliveries":
		return realtimeWebhookDeliveriesFlow(args[1:])
	case "redeliver":
		return realtimeWebhookRedeliverFlow(args[1:])
	default:
		return realtimeWebhooksListFlow(args)
	}
}

// realtimeWebhookDisplayURL is the host and path of a webhook URL, with "?…"
// in place of a query string.
func realtimeWebhookDisplayURL(raw string) string {
	text, query := raw, false
	if parsed, err := url.Parse(raw); err == nil && parsed.Host != "" {
		text = parsed.Host + parsed.EscapedPath()
		query = parsed.RawQuery != "" || parsed.ForceQuery
	} else if index := strings.IndexByte(raw, '?'); index >= 0 {
		text, query = raw[:index], true
	}
	if query {
		text += "?…"
	}
	return rtCell(text)
}

// checkRealtimeWebhookURL catches common mistakes before any request; the
// Dashboard applies the full rules (public hosts only, port 443 or 8443).
// Errors never echo the URL: its query string may hold a token.
func checkRealtimeWebhookURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("invalid --url: enter the https:// URL that receives the webhooks")
	}
	parsed, err := url.Parse(raw)
	switch {
	case err != nil || parsed.Hostname() == "":
		return "", errors.New("invalid --url: enter an absolute URL such as https://example.com/hooks/realtime")
	case !strings.EqualFold(parsed.Scheme, "https"):
		return "", errors.New("invalid --url: webhook URLs must use https://")
	case parsed.User != nil:
		return "", errors.New("invalid --url: remove the user name and password")
	}
	return raw, nil
}

// normalizeRealtimeWebhookEvents checks event names against the catalog's
// webhookEvents (case-insensitively, keeping the catalog spelling) and drops
// duplicates. An empty catalog list leaves the check to the Dashboard.
func normalizeRealtimeWebhookEvents(requested, known []string) ([]string, error) {
	seen := map[string]bool{}
	events := []string{}
	for _, name := range requested {
		canonical := name
		if len(known) > 0 {
			canonical = ""
			for _, candidate := range known {
				if strings.EqualFold(candidate, name) {
					canonical = candidate
					break
				}
			}
			if canonical == "" {
				return nil, fmt.Errorf("invalid --events: unknown event %q (available: %s)", name, strings.Join(known, ", "))
			}
		}
		if !seen[canonical] {
			seen[canonical] = true
			events = append(events, canonical)
		}
	}
	return events, nil
}

// realtimeWebhookEventsFor validates --events against the managed catalog.
func realtimeWebhookEventsFor(token string, requested []string) ([]string, error) {
	known, err := fetchRealtimeWebhookEvents(token)
	if err != nil {
		return nil, realtimeAPIError(err)
	}
	return normalizeRealtimeWebhookEvents(requested, known)
}

// realtimeWebhookSigningKey resolves --key (a key id or public key) to one of
// the app's active keys.
func realtimeWebhookSigningKey(app rtApp, ref string) (rtKey, error) {
	key, err := pickRealtimeKey(app, ref)
	if err != nil {
		return rtKey{}, err
	}
	if key.Status != "active" {
		return rtKey{}, fmt.Errorf("invalid --key: key %s is %s; webhooks are signed with an active key", key.ID, key.Status)
	}
	return key, nil
}

func checkRealtimeWebhookID(id string) (string, error) {
	id = strings.TrimSpace(id)
	if !realtimeWebhookIDPattern.MatchString(id) {
		return "", fmt.Errorf("invalid webhook id %q (list them with: hubfly realtime webhooks <server>)", id)
	}
	return id, nil
}

// realtimeWebhookKeyLabel names the key that signs a webhook's deliveries.
func realtimeWebhookKeyLabel(hook rtWebhook) string {
	if hook.Key == nil || *hook.Key == "" {
		return "-"
	}
	if hook.KeyID == nil {
		return *hook.Key + " (newest active key)"
	}
	return *hook.Key
}

func findRealtimeWebhook(parsed realtimeKeyArgs, webhookID string) (rtWebhook, error) {
	hooks, err := fetchRealtimeWebhooks(parsed.target.token, parsed.target.instance.ProjectID, parsed.target.instance.ID, parsed.app.AppID)
	if err != nil {
		return rtWebhook{}, realtimeAPIError(err)
	}
	for _, hook := range hooks {
		if hook.ID == webhookID {
			return hook, nil
		}
	}
	return rtWebhook{}, fmt.Errorf("webhook '%s' not found in app '%s' (list them with: hubfly realtime webhooks %s --app %s)",
		webhookID, parsed.app.Name, parsed.target.instance.Name, parsed.app.AppID)
}

func printRealtimeWebhookDetails(hook rtWebhook, sync rtSync) error {
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "  URL:\t%s\n", realtimeWebhookDisplayURL(hook.URL))
	_, _ = fmt.Fprintf(tw, "  Events:\t%s\n", strings.Join(hook.Events, ", "))
	_, _ = fmt.Fprintf(tw, "  Signing key:\t%s\n", realtimeWebhookKeyLabel(hook))
	_, _ = fmt.Fprintf(tw, "  Enabled:\t%t\n", hook.Enabled)
	_, _ = fmt.Fprintf(tw, "  Config:\t%s\n", describeRealtimeSync(sync))
	return tw.Flush()
}

// ---------------------------------------------------------------------------
// list / create / update / delete
// ---------------------------------------------------------------------------

func realtimeWebhooksListFlow(args []string) error {
	const usage = "hubfly realtime webhooks [list] <server> [--app <app>] [--project <id|name>] [--json]"
	fs := newRealtimeFlags("webhooks list")
	asJSON := fs.Bool("json", false, "")
	command, err := parseRealtimeKeyCommand(fs, args, usage, 1, 1)
	if err != nil {
		return err
	}
	parsed, err := command.resolve()
	if err != nil {
		return err
	}
	hooks, err := fetchRealtimeWebhooks(parsed.target.token, parsed.target.instance.ProjectID, parsed.target.instance.ID, parsed.app.AppID)
	if err != nil {
		return realtimeAPIError(err)
	}
	if *asJSON {
		return rtPrintSafeJSON(hooks)
	}
	if len(hooks) == 0 {
		_, _ = fmt.Fprintf(rtOut, "App '%s' has no webhooks. Add one with: hubfly realtime webhooks create %s --url <https-url> --events <a,b> --app %s\n",
			parsed.app.Name, parsed.target.instance.Name, parsed.app.AppID)
		return nil
	}
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tURL\tEVENTS\tENABLED\tKEY")
	for _, hook := range hooks {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%t\t%s\n", rtCell(hook.ID), realtimeWebhookDisplayURL(hook.URL),
			rtCell(strings.Join(hook.Events, ",")), hook.Enabled, rtCell(realtimeWebhookKeyLabel(hook)))
	}
	return tw.Flush()
}

func realtimeWebhooksCreateFlow(args []string) error {
	const usage = "hubfly realtime webhooks create <server> --url <https-url> --events <a,b> [--key <key>] [--disabled] [--app <app>] [--project <id|name>] [--json]"
	fs := newRealtimeFlags("webhooks create")
	rawURL := fs.String("url", "", "")
	events := &rtList{split: true}
	fs.Var(events, "events", "")
	keyRef := fs.String("key", "", "")
	disabled := fs.Bool("disabled", false, "")
	asJSON := fs.Bool("json", false, "")
	command, err := parseRealtimeKeyCommand(fs, args, usage, 1, 1)
	if err != nil {
		return err
	}
	if strings.TrimSpace(*rawURL) == "" || len(events.values) == 0 {
		return fmt.Errorf("usage: %s", usage)
	}
	webhookURL, err := checkRealtimeWebhookURL(*rawURL)
	if err != nil {
		return err
	}
	parsed, err := command.resolve()
	if err != nil {
		return err
	}
	chosen, err := realtimeWebhookEventsFor(parsed.target.token, events.values)
	if err != nil {
		return err
	}
	body := map[string]any{"url": webhookURL, "events": chosen}
	// Without --key the Dashboard signs with the app's newest active key.
	if strings.TrimSpace(*keyRef) != "" {
		key, keyErr := realtimeWebhookSigningKey(parsed.app, *keyRef)
		if keyErr != nil {
			return keyErr
		}
		body["keyId"] = key.ID
	}
	if *disabled {
		body["enabled"] = false
	}
	created, err := createRealtimeWebhook(parsed.target.token, parsed.target.instance.ProjectID, parsed.target.instance.ID, parsed.app.AppID, body)
	if err != nil {
		return realtimeAPIError(err)
	}
	if *asJSON {
		return rtPrintSafeJSON(created)
	}
	_, _ = fmt.Fprintf(rtOut, "Created webhook %s for app '%s' on realtime server '%s'.\n", created.Webhook.ID, parsed.app.Name, parsed.target.instance.Name)
	if err := printRealtimeWebhookDetails(created.Webhook, created.Sync); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(rtOut, "Send a signed test event with: hubfly realtime webhooks test %s %s --app %s\n", parsed.target.instance.Name, created.Webhook.ID, parsed.app.AppID)
	return nil
}

func realtimeWebhooksUpdateFlow(args []string) error {
	const usage = "hubfly realtime webhooks update <server> <webhook> [--url <url>] [--events <a,b>] [--key <key>] [--enable|--disable] [--app <app>] [--project <id|name>] [--json]"
	fs := newRealtimeFlags("webhooks update")
	var rawURL rtOptString
	fs.Var(&rawURL, "url", "")
	events := &rtList{split: true}
	fs.Var(events, "events", "")
	keyRef := fs.String("key", "", "")
	enable := fs.Bool("enable", false, "")
	disable := fs.Bool("disable", false, "")
	asJSON := fs.Bool("json", false, "")
	command, err := parseRealtimeKeyCommand(fs, args, usage, 2, 2)
	if err != nil {
		return err
	}
	if *enable && *disable {
		return errors.New("invalid arguments: use --enable or --disable, not both")
	}
	if !rawURL.set && len(events.values) == 0 && strings.TrimSpace(*keyRef) == "" && !*enable && !*disable {
		return fmt.Errorf("invalid arguments: nothing to update\nusage: %s", usage)
	}
	webhookID, err := checkRealtimeWebhookID(command.rest[0])
	if err != nil {
		return err
	}
	// Only the fields given are sent; the Dashboard keeps the others.
	body := map[string]any{}
	if rawURL.set {
		webhookURL, urlErr := checkRealtimeWebhookURL(rawURL.value)
		if urlErr != nil {
			return urlErr
		}
		body["url"] = webhookURL
	}
	if *enable || *disable {
		body["enabled"] = *enable
	}
	parsed, err := command.resolve()
	if err != nil {
		return err
	}
	if len(events.values) > 0 {
		chosen, eventsErr := realtimeWebhookEventsFor(parsed.target.token, events.values)
		if eventsErr != nil {
			return eventsErr
		}
		body["events"] = chosen
	}
	if strings.TrimSpace(*keyRef) != "" {
		key, keyErr := realtimeWebhookSigningKey(parsed.app, *keyRef)
		if keyErr != nil {
			return keyErr
		}
		body["keyId"] = key.ID
	}
	updated, err := updateRealtimeWebhook(parsed.target.token, parsed.target.instance.ProjectID, parsed.target.instance.ID, parsed.app.AppID, webhookID, body)
	if err != nil {
		return realtimeAPIError(err)
	}
	if *asJSON {
		return rtPrintSafeJSON(updated)
	}
	_, _ = fmt.Fprintf(rtOut, "Updated webhook %s of app '%s'.\n", updated.Webhook.ID, parsed.app.Name)
	return printRealtimeWebhookDetails(updated.Webhook, updated.Sync)
}

func realtimeWebhooksDeleteFlow(args []string) error {
	const usage = "hubfly realtime webhooks delete <server> <webhook> [--app <app>] [--project <id|name>] [--yes]"
	fs := newRealtimeFlags("webhooks delete")
	yes := fs.Bool("yes", false, "")
	fs.BoolVar(yes, "y", false, "")
	command, err := parseRealtimeKeyCommand(fs, args, usage, 2, 2)
	if err != nil {
		return err
	}
	webhookID, err := checkRealtimeWebhookID(command.rest[0])
	if err != nil {
		return err
	}
	parsed, err := command.resolve()
	if err != nil {
		return err
	}
	hook, err := findRealtimeWebhook(parsed, webhookID)
	if err != nil {
		return err
	}
	confirmed, err := confirmRealtimeExtras(*yes, "delete", fmt.Sprintf(
		"Delete webhook %s (%s) of app '%s'? Its queued deliveries are dropped.",
		hook.ID, realtimeWebhookDisplayURL(hook.URL), parsed.app.Name))
	if err != nil || !confirmed {
		return err
	}
	deleted, err := deleteRealtimeWebhook(parsed.target.token, parsed.target.instance.ProjectID, parsed.target.instance.ID, parsed.app.AppID, hook.ID)
	if err != nil {
		return realtimeAPIError(err)
	}
	_, _ = fmt.Fprintf(rtOut, "Deleted webhook %s (%s). Config: %s.\n", hook.ID, realtimeWebhookDisplayURL(hook.URL), describeRealtimeSync(deleted.Sync))
	return nil
}

// ---------------------------------------------------------------------------
// test / deliveries / redeliver
// ---------------------------------------------------------------------------

func realtimeWebhooksTestFlow(args []string) error {
	const usage = "hubfly realtime webhooks test <server> <webhook> [--app <app>] [--project <id|name>] [--json]"
	fs := newRealtimeFlags("webhooks test")
	asJSON := fs.Bool("json", false, "")
	command, err := parseRealtimeKeyCommand(fs, args, usage, 2, 2)
	if err != nil {
		return err
	}
	webhookID, err := checkRealtimeWebhookID(command.rest[0])
	if err != nil {
		return err
	}
	parsed, err := command.resolve()
	if err != nil {
		return err
	}
	hook, err := findRealtimeWebhook(parsed, webhookID)
	if err != nil {
		return err
	}
	result, err := testRealtimeWebhook(parsed.target.token, parsed.target.instance.ProjectID, parsed.target.instance.ID, parsed.app.AppID, hook.ID)
	if err != nil {
		return realtimeAPIError(err)
	}
	// A receiver that does not answer 2xx fails the command (exit code 5),
	// so scripts can check a webhook.
	failed := errors.New("the webhook receiver did not accept the test event")
	if *asJSON {
		if printErr := rtPrintSafeJSON(result); printErr != nil {
			return printErr
		}
		if !result.OK {
			return failed
		}
		return nil
	}
	response := "no response"
	if result.StatusCode != nil {
		response = fmt.Sprintf("HTTP %d", *result.StatusCode)
	}
	if result.DurationMs != nil {
		response += fmt.Sprintf(" in %d ms", *result.DurationMs)
	}
	if result.OK {
		_, _ = fmt.Fprintf(rtOut, "Test event delivered to %s: %s.\n", realtimeWebhookDisplayURL(hook.URL), response)
		return nil
	}
	_, _ = fmt.Fprintf(rtOut, "Test event to %s failed: %s.\n", realtimeWebhookDisplayURL(hook.URL), response)
	// The engine's error repeats "HTTP <code>" when the receiver answered.
	if result.Error != nil && strings.TrimSpace(*result.Error) != "" && !strings.HasPrefix(response, strings.TrimSpace(*result.Error)+" ") && strings.TrimSpace(*result.Error) != response {
		_, _ = fmt.Fprintf(rtOut, "  Error: %s\n", terminalSafe(strings.TrimSpace(*result.Error)))
	}
	return failed
}

func realtimeDeliveryEvents(delivery rtWebhookDelivery) string {
	if delivery.FirstEvent == nil || *delivery.FirstEvent == "" {
		return rtPlural(delivery.Events, "event", "events")
	}
	text := rtShorten(*delivery.FirstEvent, 40)
	if delivery.Events > 1 {
		text += fmt.Sprintf(" +%d", delivery.Events-1)
	}
	return rtCell(text)
}

func realtimeDeliveryResponse(delivery rtWebhookDelivery) string {
	text := ""
	if delivery.LastStatusCode != nil {
		text = fmt.Sprintf("HTTP %d", *delivery.LastStatusCode)
	}
	// The engine's error repeats "HTTP <code>" when the receiver answered.
	if delivery.LastError != nil && strings.TrimSpace(*delivery.LastError) != "" && strings.TrimSpace(*delivery.LastError) != text {
		if text != "" {
			text += ": "
		}
		text += rtShorten(strings.TrimSpace(*delivery.LastError), 48)
	}
	if delivery.DurationMs != nil {
		if text != "" {
			text += " "
		}
		text += fmt.Sprintf("(%d ms)", *delivery.DurationMs)
	}
	return rtCell(text)
}

// realtimeDeliveryTime is when a delivery last changed: delivered, last
// attempted, or created.
func realtimeDeliveryTime(delivery rtWebhookDelivery) string {
	for _, at := range []*string{delivery.DeliveredAt, delivery.LastAttemptAt, delivery.CreatedAt} {
		if at != nil && *at != "" {
			return rtCell(*at)
		}
	}
	return "-"
}

func realtimeWebhookDeliveriesFlow(args []string) error {
	const usage = "hubfly realtime webhooks deliveries <server> [--webhook <webhook>] [--status <status>] [--limit <n>] [--app <app>] [--project <id|name>] [--json]"
	const maxDeliveries = 1000
	fs := newRealtimeFlags("webhooks deliveries")
	webhookRef := fs.String("webhook", "", "")
	statusText := fs.String("status", "", "")
	limit := fs.Int("limit", 50, "")
	asJSON := fs.Bool("json", false, "")
	command, err := parseRealtimeKeyCommand(fs, args, usage, 1, 1)
	if err != nil {
		return err
	}
	webhookID := ""
	if strings.TrimSpace(*webhookRef) != "" {
		if webhookID, err = checkRealtimeWebhookID(*webhookRef); err != nil {
			return err
		}
	}
	status := strings.ToLower(strings.TrimSpace(*statusText))
	if status != "" && !slices.Contains(realtimeDeliveryStatuses, status) {
		return fmt.Errorf("invalid --status %q (expected %s)", *statusText, strings.Join(realtimeDeliveryStatuses, ", "))
	}
	if err := checkRealtimeLimit("limit", *limit, maxDeliveries); err != nil {
		return err
	}
	parsed, err := command.resolve()
	if err != nil {
		return err
	}
	deliveries, next, err := collectRealtimePages(*limit, 100, func(size int, cursor string) ([]rtWebhookDelivery, *string, error) {
		query := url.Values{"limit": {strconv.Itoa(size)}}
		if webhookID != "" {
			query.Set("webhookId", webhookID)
		}
		if status != "" {
			query.Set("status", status)
		}
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		page, pageErr := fetchRealtimeWebhookDeliveries(parsed.target.token, parsed.target.instance.ProjectID, parsed.target.instance.ID, parsed.app.AppID, query)
		return page.Items, page.NextCursor, pageErr
	})
	if err != nil {
		return realtimeAPIError(err)
	}
	if *asJSON {
		return rtPrintSafeJSON(rtWebhookDeliveryPage{Items: deliveries, NextCursor: next})
	}
	if len(deliveries) == 0 {
		_, _ = fmt.Fprintf(rtOut, "No webhook deliveries found for app '%s' (the log keeps 7 days).\n", parsed.app.Name)
		return nil
	}
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tWEBHOOK\tEVENTS\tSTATUS\tATTEMPTS\tRESPONSE\tAT")
	for _, delivery := range deliveries {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\t%s\n", rtCell(delivery.ID), rtCell(delivery.WebhookID),
			realtimeDeliveryEvents(delivery), rtCell(delivery.Status), delivery.Attempts,
			realtimeDeliveryResponse(delivery), realtimeDeliveryTime(delivery))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if next != nil {
		_, _ = fmt.Fprintf(rtOut, "More deliveries exist; raise --limit (up to %d) or filter with --webhook or --status.\n", maxDeliveries)
	}
	return nil
}

func realtimeWebhookRedeliverFlow(args []string) error {
	const usage = "hubfly realtime webhooks redeliver <server> <deliveryId> [--app <app>] [--project <id|name>] [--json]"
	fs := newRealtimeFlags("webhooks redeliver")
	asJSON := fs.Bool("json", false, "")
	command, err := parseRealtimeKeyCommand(fs, args, usage, 2, 2)
	if err != nil {
		return err
	}
	deliveryID := strings.TrimSpace(command.rest[0])
	if !realtimeDeliveryIDPattern.MatchString(deliveryID) {
		return fmt.Errorf("invalid delivery id %q (list them with: hubfly realtime webhooks deliveries <server>)", deliveryID)
	}
	parsed, err := command.resolve()
	if err != nil {
		return err
	}
	result, err := redeliverRealtimeWebhookDelivery(parsed.target.token, parsed.target.instance.ProjectID, parsed.target.instance.ID, parsed.app.AppID, deliveryID)
	if err != nil {
		return realtimeAPIError(err)
	}
	if *asJSON {
		if printErr := rtPrintSafeJSON(result); printErr != nil {
			return printErr
		}
	}
	if !result.Queued {
		return fmt.Errorf("the realtime server did not queue delivery %s again", deliveryID)
	}
	if !*asJSON {
		_, _ = fmt.Fprintf(rtOut, "Queued delivery %s again; it is signed with the webhook key's current secret.\n", result.DeliveryID)
	}
	return nil
}

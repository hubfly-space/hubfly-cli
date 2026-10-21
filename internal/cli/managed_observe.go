package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

// Observation commands shared by `hubfly db` and `hubfly realtime`: logs,
// metrics and operations. service is managedServiceDatabase or
// managedServiceRealtime.

func runManagedObserveCommand(service, command string, args []string) error {
	switch command {
	case "logs":
		return managedLogsFlow(service, args)
	case "metrics":
		return managedMetricsFlow(service, args)
	case "operations", "ops":
		return managedOperationsFlow(service, args)
	}
	return fmt.Errorf("unknown %s command %q", managedGroupFor(service).Command, command)
}

// moFollowContext ends `logs --follow` (Ctrl-C). Tests replace it.
var moFollowContext = func() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt)
}

// ---------------------------------------------------------------------------
// API
// ---------------------------------------------------------------------------

type moLogs struct {
	Stdout          string `json:"stdout"`
	Stderr          string `json:"stderr"`
	StdoutTruncated bool   `json:"stdoutTruncated"`
	StderrTruncated bool   `json:"stderrTruncated"`
	TailBytes       int64  `json:"tailBytes"`
}

type moSample struct {
	Time                string  `json:"time"`
	CPUPercent          float64 `json:"cpuPercent"`
	MemoryUsageBytes    int64   `json:"memoryUsageBytes"`
	MemoryLimitBytes    int64   `json:"memoryLimitBytes"`
	MemoryUsagePercent  float64 `json:"memoryUsagePercent"`
	StorageUsedBytes    int64   `json:"storageUsedBytes"`
	StorageTotalBytes   int64   `json:"storageTotalBytes"`
	StorageUsagePercent float64 `json:"storageUsagePercent"`
	Processes           int64   `json:"processes"`
	NetworkRxBytes      int64   `json:"networkRxBytes"`
	NetworkTxBytes      int64   `json:"networkTxBytes"`
}

type moDatabaseMetrics struct {
	CollectedAt   string   `json:"collectedAt"`
	UptimeSeconds *float64 `json:"uptimeSeconds"`
	Connections   *struct {
		Active float64 `json:"active"`
		Idle   float64 `json:"idle"`
		Total  float64 `json:"total"`
		Max    float64 `json:"max"`
	} `json:"connections"`
	Commits           *float64 `json:"commits"`
	Rollbacks         *float64 `json:"rollbacks"`
	QueriesTotal      *float64 `json:"queriesTotal"`
	CacheHitRatio     *float64 `json:"cacheHitRatio"`
	DatabaseSizeBytes *float64 `json:"databaseSizeBytes"`
	Deadlocks         *float64 `json:"deadlocks"`
}

type moDatabasePoint struct {
	T   string   `json:"t"`
	TPS *float64 `json:"tps"`
	QPS *float64 `json:"qps"`
}

type moRealtimeMetrics struct {
	CollectedAt   string   `json:"collectedAt"`
	UptimeSeconds *float64 `json:"uptimeSeconds"`
	Connections   struct {
		Current  float64 `json:"current"`
		Peak     float64 `json:"peak"`
		Detached float64 `json:"detached"`
		Max      float64 `json:"max"`
	} `json:"connections"`
	Channels        float64 `json:"channels"`
	Subscriptions   float64 `json:"subscriptions"`
	PresenceMembers float64 `json:"presenceMembers"`
	Messages        struct {
		In  float64 `json:"in"`
		Out float64 `json:"out"`
	} `json:"messages"`
	Bytes struct {
		In  float64 `json:"in"`
		Out float64 `json:"out"`
	} `json:"bytes"`
	Webhooks struct {
		Sent    float64 `json:"sent"`
		Failed  float64 `json:"failed"`
		Backlog float64 `json:"backlog"`
	} `json:"webhooks"`
	History struct {
		StoredMessages float64 `json:"storedMessages"`
		StoredBytes    float64 `json:"storedBytes"`
		Degraded       bool    `json:"degraded"`
	} `json:"history"`
}

type moRealtimePoint struct {
	T                    string   `json:"t"`
	Connections          *float64 `json:"connections"`
	MessagesInPerSecond  *float64 `json:"messagesInPerSecond"`
	MessagesOutPerSecond *float64 `json:"messagesOutPerSecond"`
}

type moMetrics struct {
	Latest  *moSample `json:"latest"`
	History *struct {
		WindowSeconds int        `json:"windowSeconds"`
		Records       []moSample `json:"records"`
	} `json:"history"`
	Database        *moDatabaseMetrics `json:"database"`
	DatabaseHistory []moDatabasePoint  `json:"databaseHistory"`
	Realtime        *moRealtimeMetrics `json:"realtime"`
	RealtimeHistory []moRealtimePoint  `json:"realtimeHistory"`
	Volume          *struct {
		TotalBytes int64 `json:"totalBytes"`
		UsedBytes  int64 `json:"usedBytes"`
	} `json:"volume"`
}

type moOperation struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Status string `json:"status"`
	Steps  []struct {
		Key    string `json:"key"`
		Label  string `json:"label"`
		Status string `json:"status"`
	} `json:"steps"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	CreatedAt  string  `json:"createdAt"`
	FinishedAt *string `json:"finishedAt"`
}

func fetchManagedLogs(token, projectID, instanceID string, tailBytes int64) (moLogs, error) {
	query := url.Values{}
	if tailBytes > 0 {
		query.Set("tailBytes", strconv.FormatInt(tailBytes, 10))
	}
	var payload moLogs
	err := doJSONRequest(http.MethodGet, dbdWithQuery(rtInstancePath(projectID, instanceID)+"/logs", query), token, nil, &payload)
	return payload, err
}

func fetchManagedMetricsRaw(token, projectID, instanceID string, windowSeconds int) (json.RawMessage, error) {
	query := url.Values{}
	if windowSeconds > 0 {
		query.Set("windowSeconds", strconv.Itoa(windowSeconds))
	}
	return dbdGetRaw(token, dbdWithQuery(rtInstancePath(projectID, instanceID)+"/metrics", query))
}

func fetchManagedOperationsRaw(token, projectID, instanceID string, limit int) (json.RawMessage, error) {
	query := url.Values{}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	return dbdGetRaw(token, dbdWithQuery(rtInstancePath(projectID, instanceID)+"/operations", query))
}

// moResolve authenticates and resolves the instance of a service.
func moResolve(service, ref, projectRef string) (string, rtInstance, error) {
	token, err := ensureAuth(true)
	if err != nil {
		return "", rtInstance{}, err
	}
	instance, err := resolveManagedInstanceOf(token, ref, projectRef, service)
	return token, instance, err
}

// ---------------------------------------------------------------------------
// logs
// ---------------------------------------------------------------------------

const (
	moDefaultTailBytes = 64 * 1024
	moMinTailBytes     = 4 * 1024
	moMaxTailBytes     = 2 * 1024 * 1024
)

// moParseBytes reads a byte count: 65536, 64k, 64KB, 1m, 2MiB.
func moParseBytes(value string) (int64, error) {
	text := strings.ToLower(strings.TrimSpace(value))
	multiplier := int64(1)
	for _, unit := range []struct {
		suffix string
		factor int64
	}{{"mib", 1 << 20}, {"mb", 1 << 20}, {"m", 1 << 20}, {"kib", 1 << 10}, {"kb", 1 << 10}, {"k", 1 << 10}, {"b", 1}} {
		if strings.HasSuffix(text, unit.suffix) {
			text, multiplier = strings.TrimSpace(strings.TrimSuffix(text, unit.suffix)), unit.factor
			break
		}
	}
	number, err := strconv.ParseInt(text, 10, 64)
	if err != nil || number < 1 {
		return 0, fmt.Errorf("invalid --tail %q (use a byte count such as 65536, 64k or 1m)", value)
	}
	return number * multiplier, nil
}

func managedLogsFlow(service string, args []string) error {
	group := managedGroupFor(service)
	usage := fmt.Sprintf("hubfly %s logs %s [--tail <bytes>] [--follow] [--project <id|name>]", group.Command, group.Arg)
	fs := newRealtimeFlags(group.Command + " logs")
	projectRef := fs.String("project", "", "")
	tailText := fs.String("tail", "", "")
	follow := fs.Bool("follow", false, "")
	fs.BoolVar(follow, "f", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return dbdUsageError(usage)
	}
	tailBytes := int64(moDefaultTailBytes)
	if strings.TrimSpace(*tailText) != "" {
		if tailBytes, err = moParseBytes(*tailText); err != nil {
			return err
		}
		if tailBytes < moMinTailBytes {
			tailBytes = moMinTailBytes
		}
		if tailBytes > moMaxTailBytes {
			return fmt.Errorf("invalid --tail %q (at most 2 MiB)", *tailText)
		}
	}
	token, instance, err := moResolve(service, positionals[0], *projectRef)
	if err != nil {
		return err
	}
	logs, err := fetchManagedLogs(token, instance.ProjectID, instance.ID, tailBytes)
	if err != nil {
		return moAPIError(service, instance, err)
	}
	if logs.Stdout == "" && logs.Stderr == "" && !*follow {
		_, _ = fmt.Fprintf(rtOut, "No logs for '%s' yet.\n", instance.Name)
		return nil
	}
	if *follow {
		_, _ = io.WriteString(rtOut, moCompleteLines(logs.Stdout))
		_, _ = io.WriteString(rtErr, moCompleteLines(logs.Stderr))
	} else {
		_, _ = io.WriteString(rtOut, moEnsureNewline(logs.Stdout))
		_, _ = io.WriteString(rtErr, moEnsureNewline(logs.Stderr))
	}
	if (logs.StdoutTruncated || logs.StderrTruncated) && !*follow {
		_, _ = fmt.Fprintf(rtErr, "(showing the last %s of each stream; use --tail for more, up to 2m)\n", formatBytes(logs.TailBytes))
	}
	if !*follow {
		return nil
	}
	ctx, cancel := moFollowContext()
	defer cancel()
	lastOut, lastErr := logs.Stdout, logs.Stderr
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(rtPollInterval):
		}
		next, err := fetchManagedLogs(token, instance.ProjectID, instance.ID, tailBytes)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			debugf("logs poll failed: %v", err)
			continue
		}
		_, _ = io.WriteString(rtOut, moNewLogText(lastOut, next.Stdout))
		_, _ = io.WriteString(rtErr, moNewLogText(lastErr, next.Stderr))
		lastOut, lastErr = next.Stdout, next.Stderr
	}
}

func moEnsureNewline(text string) string {
	if text == "" || strings.HasSuffix(text, "\n") {
		return text
	}
	return text + "\n"
}

// moCompleteLines drops a trailing partial line (it is printed once complete).
func moCompleteLines(text string) string {
	return text[:strings.LastIndexByte(text, '\n')+1]
}

// moNewLogText returns the complete lines of current that come after
// previous. Both are byte tails of the same stream: current is a later part
// of previous (its first line may be cut) followed by new output, so the
// longest suffix of previous, starting at a line, that current continues is
// the overlap. Without one (more output than one tail since the last poll)
// all of current is new.
func moNewLogText(previous, current string) string {
	previous, current = moCompleteLines(previous), moCompleteLines(current)
	if current == "" {
		return ""
	}
	if previous == "" {
		return current
	}
	offsets := []int{0}
	if cut := strings.IndexByte(current, '\n'); cut >= 0 && cut+1 < len(current) {
		offsets = append(offsets, cut+1)
	}
	for _, offset := range offsets {
		rest := current[offset:]
		firstLine := rest[:strings.IndexByte(rest, '\n')+1]
		for start := 0; start < len(previous); {
			if strings.HasPrefix(rest, previous[start:]) {
				return rest[len(previous)-start:]
			}
			next := strings.Index(previous[start:], "\n"+firstLine)
			if next < 0 {
				break
			}
			start += next + 1
		}
	}
	return current
}

// ---------------------------------------------------------------------------
// metrics
// ---------------------------------------------------------------------------

func managedMetricsFlow(service string, args []string) error {
	group := managedGroupFor(service)
	usage := fmt.Sprintf("hubfly %s metrics %s [--window <duration>] [--project <id|name>] [--json]", group.Command, group.Arg)
	fs := newRealtimeFlags(group.Command + " metrics")
	projectRef := fs.String("project", "", "")
	windowText := fs.String("window", "1h", "")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return dbdUsageError(usage)
	}
	window, err := parseRealtimeDuration(*windowText)
	if err != nil {
		return fmt.Errorf("invalid --window: %w", err)
	}
	if window < 300 || window > 7*86400 {
		return fmt.Errorf("invalid --window %q (use 5m to 7d)", *windowText)
	}
	token, instance, err := moResolve(service, positionals[0], *projectRef)
	if err != nil {
		return err
	}
	raw, err := fetchManagedMetricsRaw(token, instance.ProjectID, instance.ID, window)
	if err != nil {
		return moAPIError(service, instance, err)
	}
	if *asJSON {
		return rtPrintJSON(raw)
	}
	var metrics moMetrics
	if err := dbdDecode(raw, &metrics); err != nil {
		return fmt.Errorf("read the metrics: %w", err)
	}
	writeManagedMetrics(rtOut, group, instance, metrics, window)
	return nil
}

func moPercent(value float64) string {
	return fmt.Sprintf("%.1f%%", value)
}

func moCount(value float64) string {
	return formatThousands64(int64(value))
}

func moUsage(used, total int64, percent float64) string {
	if total <= 0 {
		return formatBytes(used)
	}
	return fmt.Sprintf("%s of %s (%s)", formatBytes(used), formatBytes(total), moPercent(percent))
}

func writeManagedMetrics(w io.Writer, group managedGroup, instance rtInstance, metrics moMetrics, window int) {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	row := func(label, value string) { _, _ = fmt.Fprintf(tw, "  %s\t%s\n", label, value) }
	if metrics.Latest == nil {
		_, _ = fmt.Fprintf(w, "No resource samples for '%s' yet (status: %s).\n", instance.Name, managedStatusLabel(instance))
	} else {
		latest := metrics.Latest
		_, _ = fmt.Fprintf(w, "Metrics of '%s' (%s) at %s\n", instance.Name, managedStatusLabel(instance), dbdTime(latest.Time))
		row("CPU:", moPercent(latest.CPUPercent))
		row("Memory:", moUsage(latest.MemoryUsageBytes, latest.MemoryLimitBytes, latest.MemoryUsagePercent))
		if latest.StorageTotalBytes > 0 {
			row("Storage:", moUsage(latest.StorageUsedBytes, latest.StorageTotalBytes, latest.StorageUsagePercent))
		} else if metrics.Volume != nil && metrics.Volume.TotalBytes > 0 {
			used := metrics.Volume.UsedBytes
			row("Storage:", moUsage(used, metrics.Volume.TotalBytes, float64(used)*100/float64(metrics.Volume.TotalBytes)))
		}
		row("Network:", fmt.Sprintf("%s received, %s sent", formatBytes(latest.NetworkRxBytes), formatBytes(latest.NetworkTxBytes)))
		if metrics.History != nil && len(metrics.History.Records) > 1 {
			var cpuSum, cpuPeak float64
			var memPeak int64
			for _, sample := range metrics.History.Records {
				cpuSum += sample.CPUPercent
				if sample.CPUPercent > cpuPeak {
					cpuPeak = sample.CPUPercent
				}
				if sample.MemoryUsageBytes > memPeak {
					memPeak = sample.MemoryUsageBytes
				}
			}
			label := "Last " + formatRealtimeSeconds(window) + ":"
			row(label, fmt.Sprintf("CPU avg %s, peak %s; memory peak %s", moPercent(cpuSum/float64(len(metrics.History.Records))), moPercent(cpuPeak), formatBytes(memPeak)))
		}
	}
	if database := metrics.Database; database != nil && group.Service == managedServiceDatabase {
		_, _ = fmt.Fprintln(tw, "Database\t")
		if database.Connections != nil {
			connections := database.Connections
			row("Connections:", fmt.Sprintf("%s active, %s idle, %s total (max %s)", moCount(connections.Active), moCount(connections.Idle), moCount(connections.Total), moCount(connections.Max)))
		}
		if database.QueriesTotal != nil {
			row("Queries:", moCount(*database.QueriesTotal)+" since start")
		}
		if database.Commits != nil {
			text := moCount(*database.Commits) + " commits"
			if database.Rollbacks != nil {
				text += ", " + moCount(*database.Rollbacks) + " rollbacks"
			}
			row("Transactions:", text)
		}
		if tps, qps, ok := moDatabaseRates(metrics.DatabaseHistory); ok {
			row("Last "+formatRealtimeSeconds(window)+":", fmt.Sprintf("%.1f transactions/s, %.1f queries/s on average", tps, qps))
		}
		if database.CacheHitRatio != nil {
			row("Cache hit ratio:", moPercent(*database.CacheHitRatio*100))
		}
		if database.DatabaseSizeBytes != nil {
			row("Data size:", formatBytes(int64(*database.DatabaseSizeBytes)))
		}
		if database.Deadlocks != nil && *database.Deadlocks > 0 {
			row("Deadlocks:", moCount(*database.Deadlocks))
		}
		if database.UptimeSeconds != nil {
			row("Uptime:", dbdDuration(*database.UptimeSeconds*1000))
		}
	}
	if realtime := metrics.Realtime; realtime != nil && group.Service == managedServiceRealtime {
		_, _ = fmt.Fprintln(tw, "Realtime\t")
		connections := fmt.Sprintf("%s now, peak %s", moCount(realtime.Connections.Current), moCount(realtime.Connections.Peak))
		if realtime.Connections.Max > 0 {
			connections += fmt.Sprintf(" (limit %s)", moCount(realtime.Connections.Max))
		}
		if realtime.Connections.Detached > 0 {
			connections += fmt.Sprintf(", %s resuming", moCount(realtime.Connections.Detached))
		}
		row("Connections:", connections)
		row("Channels:", fmt.Sprintf("%s (%s subscriptions, %s presence members)", moCount(realtime.Channels), moCount(realtime.Subscriptions), moCount(realtime.PresenceMembers)))
		row("Messages:", fmt.Sprintf("%s in, %s out since start (%s in, %s out)", moCount(realtime.Messages.In), moCount(realtime.Messages.Out),
			formatBytes(int64(realtime.Bytes.In)), formatBytes(int64(realtime.Bytes.Out))))
		if in, out, ok := moRealtimeRates(metrics.RealtimeHistory); ok {
			row("Last "+formatRealtimeSeconds(window)+":", fmt.Sprintf("%.1f messages/s in, %.1f out on average", in, out))
		}
		if realtime.Webhooks.Sent > 0 || realtime.Webhooks.Failed > 0 || realtime.Webhooks.Backlog > 0 {
			row("Webhooks:", fmt.Sprintf("%s sent, %s failed, %s queued", moCount(realtime.Webhooks.Sent), moCount(realtime.Webhooks.Failed), moCount(realtime.Webhooks.Backlog)))
		}
		if realtime.History.StoredMessages > 0 || realtime.History.Degraded {
			text := fmt.Sprintf("%s messages stored (%s)", moCount(realtime.History.StoredMessages), formatBytes(int64(realtime.History.StoredBytes)))
			if realtime.History.Degraded {
				text += ", degraded"
			}
			row("History:", text)
		}
		if realtime.UptimeSeconds != nil {
			row("Uptime:", dbdDuration(*realtime.UptimeSeconds*1000))
		}
	}
	_ = tw.Flush()
}

func moDatabaseRates(points []moDatabasePoint) (float64, float64, bool) {
	var tps, qps float64
	count := 0
	for _, point := range points {
		if point.TPS == nil || point.QPS == nil {
			continue
		}
		tps += *point.TPS
		qps += *point.QPS
		count++
	}
	if count == 0 {
		return 0, 0, false
	}
	return tps / float64(count), qps / float64(count), true
}

func moRealtimeRates(points []moRealtimePoint) (float64, float64, bool) {
	var in, out float64
	count := 0
	for _, point := range points {
		if point.MessagesInPerSecond == nil || point.MessagesOutPerSecond == nil {
			continue
		}
		in += *point.MessagesInPerSecond
		out += *point.MessagesOutPerSecond
		count++
	}
	if count == 0 {
		return 0, 0, false
	}
	return in / float64(count), out / float64(count), true
}

// ---------------------------------------------------------------------------
// operations
// ---------------------------------------------------------------------------

func managedOperationsFlow(service string, args []string) error {
	group := managedGroupFor(service)
	usage := fmt.Sprintf("hubfly %s operations %s [--limit <n>] [--project <id|name>] [--json]", group.Command, group.Arg)
	fs := newRealtimeFlags(group.Command + " operations")
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
	token, instance, err := moResolve(service, positionals[0], *projectRef)
	if err != nil {
		return err
	}
	raw, err := fetchManagedOperationsRaw(token, instance.ProjectID, instance.ID, *limit)
	if err != nil {
		return moAPIError(service, instance, err)
	}
	if *asJSON {
		return rtPrintJSON(raw)
	}
	var payload struct {
		Items []moOperation `json:"items"`
	}
	if err := dbdDecode(raw, &payload); err != nil {
		return fmt.Errorf("read the operations: %w", err)
	}
	if len(payload.Items) == 0 {
		_, _ = fmt.Fprintf(rtOut, "No operations on '%s' yet.\n", instance.Name)
		return nil
	}
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tKIND\tSTATUS\tSTEP\tSTARTED\tFINISHED\tERROR")
	for _, operation := range payload.Items {
		errorText := ""
		if operation.Error != nil {
			errorText = dbdOneLine(operation.Error.Message, 60)
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", operation.ID, strings.ReplaceAll(operation.Kind, "_", " "), operation.Status,
			moOperationStep(operation), dbdTime(operation.CreatedAt), dbdTimePtr(operation.FinishedAt), errorText)
	}
	return tw.Flush()
}

// moOperationStep is the running or failed step, or the progress count.
func moOperationStep(operation moOperation) string {
	done := 0
	for _, step := range operation.Steps {
		switch step.Status {
		case "running", "failed":
			return step.Label
		case "succeeded", "skipped":
			done++
		}
	}
	if len(operation.Steps) == 0 {
		return "-"
	}
	return fmt.Sprintf("%d/%d done", done, len(operation.Steps))
}

// moAPIError adds the next step to errors a user can act on.
func moAPIError(service string, instance rtInstance, err error) error {
	if service == managedServiceDatabase {
		return dbdTarget{instance: instance}.apiError(err)
	}
	return realtimeAPIError(err)
}

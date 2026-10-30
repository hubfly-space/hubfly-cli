package cli

import (
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"regexp"
	"strings"
	"text/tabwriter"
	"time"
)

// Reports of `hubfly analytics`: stats (aggregate + breakdowns), realtime and
// export. Queries go through the Dashboard's typed query endpoint
// (hubfly-analytics/docs/API.md describes the semantics).

var analyticsStatsMetrics = []string{"visitors", "visits", "pageviews", "views_per_visit", "bounce_rate", "visit_duration"}

var analyticsMetricLabels = map[string]string{
	"visitors": "Visitors", "visits": "Visits", "pageviews": "Pageviews", "views_per_visit": "Views per visit",
	"bounce_rate": "Bounce rate", "visit_duration": "Visit duration", "events": "Events",
}

var analyticsDimLabels = map[string]string{
	"page": "PAGE", "entry_page": "ENTRY PAGE", "exit_page": "EXIT PAGE", "source": "SOURCE", "referrer": "REFERRER",
	"channel": "CHANNEL", "country": "COUNTRY", "region": "REGION", "city": "CITY", "browser": "BROWSER",
	"os": "OS", "device": "DEVICE", "language": "LANGUAGE", "hostname": "HOSTNAME", "event": "EVENT",
	"utm_source": "UTM SOURCE", "utm_medium": "UTM MEDIUM", "utm_campaign": "UTM CAMPAIGN",
	"utm_content": "UTM CONTENT", "utm_term": "UTM TERM",
}

var analyticsPeriodLabels = map[string]string{
	"today": "today", "7d": "last 7 days", "30d": "last 30 days", "month": "this month", "6mo": "last 6 months", "12mo": "last 12 months",
}

var analyticsDatePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// analyticsWatchInterval is how often `realtime --watch` refreshes.
var analyticsWatchInterval = 10 * time.Second

// analyticsRangeFrom reads --period or --from/--to.
func analyticsRangeFrom(period, from, to string) (*analyticsRange, error) {
	from, to = strings.TrimSpace(from), strings.TrimSpace(to)
	if from != "" || to != "" {
		if from == "" || to == "" {
			return nil, errors.New("invalid arguments: --from and --to go together")
		}
		if !analyticsDatePattern.MatchString(from) || !analyticsDatePattern.MatchString(to) {
			return nil, errors.New("invalid arguments: dates are YYYY-MM-DD")
		}
		if from > to {
			return nil, errors.New("invalid arguments: --from is after --to")
		}
		return &analyticsRange{From: from, To: to}, nil
	}
	period = strings.TrimSpace(period)
	if _, ok := analyticsPeriodLabels[period]; !ok {
		return nil, fmt.Errorf("invalid arguments: --period is one of today, 7d, 30d, month, 6mo, 12mo (got %q)", period)
	}
	return &analyticsRange{Period: period}, nil
}

// analyticsFiltersFrom turns --filter dim=value (or dim!=value, dim~value)
// into query filters.
func analyticsFiltersFrom(values []string) ([]analyticsFilter, error) {
	filters := []analyticsFilter{}
	for _, raw := range values {
		op, separator := "is", "="
		switch {
		case strings.Contains(raw, "!="):
			op, separator = "is_not", "!="
		case strings.Contains(raw, "~"):
			op, separator = "contains", "~"
		}
		dim, value, ok := strings.Cut(raw, separator)
		dim, value = strings.TrimSpace(dim), strings.TrimSpace(value)
		if !ok || dim == "" {
			return nil, fmt.Errorf("invalid arguments: --filter %q: use <dimension>=<value>, <dimension>!=<value> or <dimension>~<text>", raw)
		}
		if op == "is" && strings.ContainsAny(value, "*") {
			op = "matches"
		}
		filters = append(filters, analyticsFilter{Dim: dim, Op: op, Values: []string{value}})
	}
	return filters, nil
}

// analyticsStatsResult is the --json output of `hubfly analytics stats`.
type analyticsStatsResult struct {
	Site       string                        `json:"site"`
	Aggregate  analyticsAggregate            `json:"aggregate"`
	Breakdowns map[string]analyticsBreakdown `json:"breakdowns"`
}

func analyticsStatsFlow(args []string) error {
	const usage = "hubfly analytics stats <site> [--period today|7d|30d|month|6mo|12mo | --from <date> --to <date>] [--by <dimension>] [--limit <n>] [--filter <dim>=<value>]... [--compare] [--project <p>] [--json]"
	var period, from, to, by *string
	var limit *int
	var compare, asJSON *bool
	var filterValues rtList
	token, site, _, rest, err := analyticsSiteTarget("stats", usage, args, func(fs *flag.FlagSet) {
		period = fs.String("period", "30d", "")
		from = fs.String("from", "", "")
		to = fs.String("to", "", "")
		by = fs.String("by", "", "")
		limit = fs.Int("limit", 10, "")
		compare = fs.Bool("compare", false, "")
		asJSON = fs.Bool("json", false, "")
		fs.Var(&filterValues, "filter", "")
	})
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return fmt.Errorf("unexpected argument: %s\nusage: %s", rest[0], usage)
	}
	if *limit < 1 || *limit > 1000 {
		return errors.New("invalid arguments: --limit must be 1 to 1000")
	}
	queryRange, err := analyticsRangeFrom(*period, *from, *to)
	if err != nil {
		return err
	}
	filters, err := analyticsFiltersFrom(filterValues.values)
	if err != nil {
		return err
	}
	aggregateQuery := analyticsQuery{Type: "aggregate", Range: queryRange, Filters: filters, Metrics: analyticsStatsMetrics}
	if *compare {
		aggregateQuery.Compare = "previous_period"
	}
	result := analyticsStatsResult{Site: site.ID, Breakdowns: map[string]analyticsBreakdown{}}
	if err := runAnalyticsQuery(token, site.ProjectID, site.ID, aggregateQuery, &result.Aggregate); err != nil {
		return analyticsAPIError(err)
	}
	dims := []string{"page", "source"}
	breakdownLimit := 5
	if strings.TrimSpace(*by) != "" {
		dims, breakdownLimit = []string{strings.TrimSpace(*by)}, *limit
	}
	for _, dim := range dims {
		var breakdown analyticsBreakdown
		query := analyticsQuery{Type: "breakdown", Range: queryRange, Filters: filters, Dim: dim, Limit: breakdownLimit, Metrics: []string{"visitors", "pageviews"}, Order: &analyticsQueryOrder{Metric: "visitors", Dir: "desc"}}
		if err := runAnalyticsQuery(token, site.ProjectID, site.ID, query, &breakdown); err != nil {
			return analyticsAPIError(err)
		}
		result.Breakdowns[dim] = breakdown
	}
	if *asJSON {
		return rtPrintJSON(result)
	}
	printAnalyticsStats(site, queryRange, result, dims, *compare)
	return nil
}

func printAnalyticsStats(site analyticsSite, queryRange *analyticsRange, result analyticsStatsResult, dims []string, compare bool) {
	meta := result.Aggregate.Meta
	span := analyticsPeriodLabels[queryRange.Period]
	if span == "" {
		span = queryRange.From + " to " + queryRange.To
	}
	if meta.From != nil && meta.To != nil && queryRange.Period != "" {
		span += fmt.Sprintf(" (%s to %s)", *meta.From, *meta.To)
	}
	if meta.Timezone != nil && *meta.Timezone != "" {
		span += ", " + *meta.Timezone
	}
	_, _ = fmt.Fprintf(rtOut, "%s · %s\n\n", site.Name, span)
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	for _, metric := range analyticsStatsMetrics {
		value, ok := result.Aggregate.Results[metric]
		if !ok {
			continue
		}
		line := fmt.Sprintf("  %s\t%s", analyticsMetricLabels[metric], formatAnalyticsMetric(metric, value.Value))
		if compare {
			line += "\t" + formatAnalyticsChange(metric, value.Change)
		}
		_, _ = fmt.Fprintln(tw, line)
	}
	_ = tw.Flush()
	for _, dim := range dims {
		breakdown := result.Breakdowns[dim]
		_, _ = fmt.Fprintln(rtOut)
		label := analyticsDimLabels[dim]
		if label == "" {
			label = strings.ToUpper(strings.ReplaceAll(dim, "_", " "))
		}
		if len(breakdown.Results) == 0 {
			_, _ = fmt.Fprintf(rtOut, "%s: no data\n", label)
			continue
		}
		tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
		_, _ = fmt.Fprintf(tw, "%s\tVISITORS\tPAGEVIEWS\n", label)
		for _, row := range breakdown.Results {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", analyticsDimValue(dim, row.Value), formatAnalyticsMetric("visitors", row.Values["visitors"]), formatAnalyticsMetric("pageviews", row.Values["pageviews"]))
		}
		_ = tw.Flush()
		if breakdown.Meta.TotalValues != nil && *breakdown.Meta.TotalValues > int64(len(breakdown.Results)) {
			_, _ = fmt.Fprintf(rtOut, "… %d more (use --by %s --limit <n>)\n", *breakdown.Meta.TotalValues-int64(len(breakdown.Results)), dim)
		}
	}
	if meta.Partial && meta.CoveredFrom != nil {
		_, _ = fmt.Fprintf(rtOut, "\nFiltered reports read raw events, kept for the site's retention: data starts %s.\n", *meta.CoveredFrom)
	}
}

// analyticsDimValue labels the empty value the service reports for "none".
func analyticsDimValue(dim, value string) string {
	if value != "" {
		return value
	}
	switch dim {
	case "source", "referrer", "channel":
		return "Direct / none"
	}
	return "(unknown)"
}

func analyticsFloat(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case int64:
		return float64(typed), true
	case int:
		return float64(typed), true
	}
	return 0, false
}

func formatAnalyticsMetric(metric string, value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	number, ok := analyticsFloat(value)
	if !ok {
		return "-"
	}
	switch metric {
	case "bounce_rate", "conversion_rate":
		return fmt.Sprintf("%.1f%%", number)
	case "visit_duration", "time_on_page":
		return formatAnalyticsDuration(number)
	case "views_per_visit":
		return fmt.Sprintf("%.2f", number)
	}
	if number == math.Trunc(number) && math.Abs(number) < 1e15 {
		return formatAnalyticsCount(int64(number))
	}
	return fmt.Sprintf("%.2f", number)
}

func formatAnalyticsDuration(seconds float64) string {
	total := int(math.Round(seconds))
	if total < 60 {
		return fmt.Sprintf("%ds", total)
	}
	if total < 3600 {
		return fmt.Sprintf("%dm %02ds", total/60, total%60)
	}
	return fmt.Sprintf("%dh %02dm", total/3600, (total%3600)/60)
}

// formatAnalyticsChange renders a change against the comparison period:
// percentage points for rates, a percentage otherwise.
func formatAnalyticsChange(metric string, change *float64) string {
	if change == nil {
		return "-"
	}
	if metric == "bounce_rate" || metric == "conversion_rate" {
		return fmt.Sprintf("%+.1f pts", *change)
	}
	return fmt.Sprintf("%+.1f%%", *change)
}

// ---------------------------------------------------------------------------
// realtime
// ---------------------------------------------------------------------------

func analyticsRealtimeFlow(args []string) error {
	const usage = "hubfly analytics realtime <site> [--watch] [--project <p>] [--json]"
	var watch, asJSON *bool
	token, site, _, rest, err := analyticsSiteTarget("realtime", usage, args, func(fs *flag.FlagSet) {
		watch = fs.Bool("watch", false, "")
		fs.BoolVar(watch, "w", false, "")
		asJSON = fs.Bool("json", false, "")
	})
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return fmt.Errorf("unexpected argument: %s\nusage: %s", rest[0], usage)
	}
	if *watch && *asJSON {
		return errors.New("invalid arguments: --watch and --json do not go together")
	}
	for {
		var live analyticsRealtime
		if err := runAnalyticsQuery(token, site.ProjectID, site.ID, analyticsQuery{Type: "realtime"}, &live); err != nil {
			return analyticsAPIError(err)
		}
		if *asJSON {
			return rtPrintJSON(live)
		}
		printAnalyticsRealtime(site, live)
		if !*watch {
			return nil
		}
		_, _ = fmt.Fprintf(rtOut, "\nRefreshing every %s (Ctrl+C to stop)…\n\n", analyticsWatchInterval)
		time.Sleep(analyticsWatchInterval)
	}
}

func printAnalyticsRealtime(site analyticsSite, live analyticsRealtime) {
	_, _ = fmt.Fprintf(rtOut, "%s · %s\n", site.Name, time.Now().Format("15:04:05"))
	_, _ = fmt.Fprintf(rtOut, "%s visitors in the last 5 minutes, %s pageviews in the last 30 minutes\n", formatAnalyticsCount(live.Visitors), formatAnalyticsCount(live.Pageviews))
	tops := []struct {
		label string
		dim   string
		rows  []analyticsRealtimeTop
	}{{"PAGE", "page", live.TopPages}, {"SOURCE", "source", live.TopSources}, {"COUNTRY", "country", live.TopCountries}}
	for _, top := range tops {
		if len(top.rows) == 0 {
			continue
		}
		_, _ = fmt.Fprintln(rtOut)
		tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
		_, _ = fmt.Fprintf(tw, "%s\tVISITORS\n", top.label)
		for _, row := range top.rows {
			_, _ = fmt.Fprintf(tw, "%s\t%d\n", analyticsDimValue(top.dim, row.Value), row.Visitors)
		}
		_ = tw.Flush()
	}
	if len(live.Recent) == 0 {
		return
	}
	_, _ = fmt.Fprintln(rtOut)
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "TIME\tEVENT\tPAGE\tCOUNTRY\tDEVICE\tSOURCE")
	for index, event := range live.Recent {
		if index == 10 {
			break
		}
		name := event.Type
		if event.Name != nil && *event.Name != "" {
			name = *event.Name
		}
		when := event.TS
		if parsed, parseErr := time.Parse(time.RFC3339Nano, event.TS); parseErr == nil {
			when = parsed.Local().Format("15:04:05")
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", when, name, event.Page, valueOrDash(derefString(event.Country)), valueOrDash(derefString(event.Device)), analyticsDimValue("source", derefString(event.Source)))
	}
	_ = tw.Flush()
}

// ---------------------------------------------------------------------------
// export
// ---------------------------------------------------------------------------

func analyticsExportFlow(args []string) error {
	const usage = "hubfly analytics export <site> --from <date> --to <date> [--kind events|sessions|summaries] [--format csv|ndjson] [--output <file>|-] [--force] [--project <p>]"
	var from, to, kind, format, output *string
	var force *bool
	token, site, _, rest, err := analyticsSiteTarget("export", usage, args, func(fs *flag.FlagSet) {
		from = fs.String("from", "", "")
		to = fs.String("to", "", "")
		kind = fs.String("kind", "events", "")
		format = fs.String("format", "csv", "")
		output = fs.String("output", "", "")
		fs.StringVar(output, "o", "", "")
		force = fs.Bool("force", false, "")
	})
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return fmt.Errorf("unexpected argument: %s\nusage: %s", rest[0], usage)
	}
	if *from == "" || *to == "" {
		return fmt.Errorf("invalid arguments: --from and --to are required\nusage: %s", usage)
	}
	if _, err := analyticsRangeFrom("", *from, *to); err != nil {
		return err
	}
	if *kind != "events" && *kind != "sessions" && *kind != "summaries" {
		return errors.New("invalid arguments: --kind is events, sessions or summaries")
	}
	if *format != "csv" && *format != "ndjson" {
		return errors.New("invalid arguments: --format is csv or ndjson")
	}
	target := *output
	if target != "-" {
		if target == "" {
			target = fmt.Sprintf("%s-%s-%s-to-%s.%s", site.PublicID, *kind, *from, *to, *format)
		}
		if _, statErr := os.Stat(target); statErr == nil && !*force {
			return fmt.Errorf("invalid arguments: %s exists; use --force to overwrite it or --output <file>", target)
		}
	}
	exported, err := exportAnalyticsData(token, site.ProjectID, site.ID, *from, *to, *kind, *format)
	if err != nil {
		return analyticsAPIError(err)
	}
	if target == "-" {
		_, err = fmt.Fprint(rtOut, exported.Content)
	} else {
		err = os.WriteFile(target, []byte(exported.Content), 0o600)
		if err == nil {
			_, _ = fmt.Fprintf(rtErr, "Wrote %s (%s).\n", target, formatBytes(int64(len(exported.Content))))
		}
	}
	if err != nil {
		return err
	}
	if exported.Truncated {
		_, _ = fmt.Fprintln(rtErr, "The export stopped at 8 MB: split the date range to get the rest.")
	}
	return nil
}

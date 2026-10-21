package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"
)

// `hubfly storage`: Object Storage projects, buckets, objects, S3 access
// keys, managed assets and media. Shares the flag parsing, JSON output,
// prompts and spinner of `hubfly realtime` (realtime_commands.go).

const storageUsageText = `Object Storage (S3-compatible buckets, objects, access keys, published assets and video):
  hubfly storage list [--org <id|slug>] [--json]
  hubfly storage create <name> [--region <id|name>] [--org <id|slug>] [--yes] [--json]
  hubfly storage info [<project>] [--json]
  hubfly storage usage [<project>] [--json]
  hubfly storage delete <project> [--yes]

Buckets and objects:
  hubfly storage buckets [list] [--project <p>] [--json]
  hubfly storage buckets create <bucket> [--versioning enabled|suspended|disabled] [--project <p>] [--json]
  hubfly storage buckets update <bucket> [--versioning <mode>] [--image-retention <duration>] [--project <p>]
  hubfly storage buckets delete <bucket> [--yes] [--project <p>]
  hubfly storage ls [<bucket>[/<prefix>]] [--recursive] [--search <text>] [--limit <n>] [--project <p>] [--json]
  hubfly storage put <file|dir> <bucket>[/<key>] [--recursive] [--content-type <type>] [--project <p>]
  hubfly storage get <bucket>/<key> [<file>|-] [--force] [--project <p>]
  hubfly storage rm <bucket>/<key>... [--recursive] [--yes] [--project <p>]
  hubfly storage url <bucket>/<key> [--project <p>] [--json]

S3 access keys:
  hubfly storage keys [list] [--project <p>] [--json]
  hubfly storage keys create <name> [--read-only] [--bucket <bucket>] [--format text|dotenv|json]
                                    [--to <cell-project>] [--project <p>]
  hubfly storage keys revoke <accessKey> [--yes] [--project <p>]

Assets and media:
  hubfly storage assets [list] [--bucket <bucket>] [--project <p>] [--json]
  hubfly storage publish <bucket>/<key> [--protected] [--title <text>] [--project <p>] [--json]
  hubfly storage share <bucket>/<key>|<assetId> [--expires <duration>] [--project <p>] [--json]
  hubfly storage unpublish <assetId|bucket/key> [--yes] [--project <p>]
  hubfly storage encode <bucket>/<key> [--max-height 360|480|720|1080] [--wait] [--project <p>] [--json]
  hubfly storage media [list] [--project <p>] [--json]

<project> is a Storage project id or name. It may be omitted when you have exactly one Storage
project, or set HUBFLY_STORAGE_PROJECT. Object paths are <bucket>/<key>. An S3 secret is printed
once, when the key is created.`

// storageProjectEnv names the default Storage project for commands without
// --project.
const storageProjectEnv = "HUBFLY_STORAGE_PROJECT"

// storageSigningRegion is the SigV4 region the CLI writes into credentials:
// HubFly Storage accepts any signing region and the Dashboard reports none.
const storageSigningRegion = "us-east-1"

func printStorageUsage() {
	_, _ = fmt.Fprintln(rtOut, storageUsageText)
}

func runStorageGroup(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		printStorageUsage()
		return nil
	}
	command, rest := args[0], args[1:]
	switch command {
	case "list":
		return storageListFlow(rest)
	case "create", "new":
		return storageCreateFlow(rest)
	case "info", "show", "inspect":
		return storageInfoFlow(rest)
	case "usage":
		return storageUsageFlow(rest)
	case "delete", "destroy":
		return storageDeleteFlow(rest)
	case "buckets", "bucket":
		return runStorageBucketsGroup(rest)
	case "ls":
		return storageLsFlow(rest)
	case "put", "upload":
		return storagePutFlow(rest)
	case "get", "download":
		return storageGetFlow(rest)
	case "rm", "remove":
		return storageRmFlow(rest)
	case "url":
		return storageURLFlow(rest)
	case "keys", "key":
		return runStorageKeysGroup(rest)
	case "assets", "asset":
		return runStorageAssetsGroup(rest)
	case "publish":
		return storagePublishFlow(rest)
	case "share":
		return storageShareFlow(rest)
	case "unpublish":
		return storageUnpublishFlow(rest)
	case "encode":
		return storageEncodeFlow(rest)
	case "media":
		return runStorageMediaGroup(rest)
	default:
		return fmt.Errorf("unknown storage command %q (run: hubfly storage help)", command)
	}
}

func newStorageFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet("storage "+name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

// storageProjectFlag registers --project/-p.
func storageProjectFlag(fs *flag.FlagSet) *string {
	ref := fs.String("project", "", "")
	fs.StringVar(ref, "p", "", "")
	return ref
}

func storageYesFlag(fs *flag.FlagSet) *bool {
	yes := fs.Bool("yes", false, "")
	fs.BoolVar(yes, "y", false, "")
	return yes
}

// ---------------------------------------------------------------------------
// Project resolution
// ---------------------------------------------------------------------------

// storageTarget is a resolved Storage project plus the token used to find it.
type storageTarget struct {
	token   string
	project project
}

func isStorageProject(item project) bool {
	return item.Type == "storage"
}

func fetchStorageProjects(token, organizationID string) ([]project, error) {
	all, err := fetchAllProjectsAnyType(token, organizationID)
	if err != nil {
		return nil, err
	}
	result := []project{}
	for _, item := range all {
		if isStorageProject(item) {
			result = append(result, item)
		}
	}
	return result, nil
}

// resolveStorageTarget authenticates and resolves the Storage project of a
// command. allowPrompt permits an interactive pick when several projects
// match nothing more specific; pass false when stdout must stay clean
// (--json, `get ... -`).
func resolveStorageTarget(ref string, allowPrompt bool) (storageTarget, error) {
	token, err := ensureAuth(true)
	if err != nil {
		return storageTarget{}, err
	}
	chosen, err := resolveStorageProject(token, ref, allowPrompt)
	if err != nil {
		return storageTarget{}, err
	}
	return storageTarget{token: token, project: chosen}, nil
}

// resolveStorageProject finds a Storage project by id or case-insensitive
// name; without a reference it uses HUBFLY_STORAGE_PROJECT, then the only
// Storage project, then an interactive pick.
func resolveStorageProject(token, ref string, allowPrompt bool) (project, error) {
	ref = strings.TrimSpace(ref)
	source := ""
	if ref == "" {
		if fromEnv := strings.TrimSpace(os.Getenv(storageProjectEnv)); fromEnv != "" {
			ref, source = fromEnv, " (from "+storageProjectEnv+")"
		}
	}
	all, err := fetchAllProjectsAnyType(token, "")
	if err != nil {
		return project{}, err
	}
	candidates := []project{}
	for _, item := range all {
		if isStorageProject(item) {
			candidates = append(candidates, item)
		}
	}
	if ref != "" {
		var byName []project
		for _, item := range candidates {
			if item.ID == ref {
				return item, nil
			}
			if strings.EqualFold(item.Name, ref) {
				byName = append(byName, item)
			}
		}
		if len(byName) == 1 {
			return byName[0], nil
		}
		if len(byName) > 1 {
			return project{}, fmt.Errorf("Storage project name '%s'%s is ambiguous (%s); use the project id", ref, source, storageProjectList(byName))
		}
		for _, item := range all {
			if item.ID == ref || strings.EqualFold(item.Name, ref) {
				kind := item.Type
				if kind == "" {
					kind = "cell"
				}
				return project{}, fmt.Errorf("project '%s'%s is a %s project, not a Storage project (list Storage projects with: hubfly storage list)", item.Name, source, kind)
			}
		}
		return project{}, fmt.Errorf("Storage project '%s'%s not found (list them with: hubfly storage list)", ref, source)
	}
	switch len(candidates) {
	case 0:
		return project{}, errors.New("no Storage projects found; create one with: hubfly storage create <name>")
	case 1:
		return candidates[0], nil
	}
	if allowPrompt && rtInteractive() {
		_, _ = fmt.Fprintln(rtOut, "Storage projects:")
		for index, item := range candidates {
			_, _ = fmt.Fprintf(rtOut, "  %d) %s (%s, %s)\n", index+1, item.Name, valueOrDash(item.Region.Name), item.ID)
		}
		choice, promptErr := promptNumber(fmt.Sprintf("Project [1-%d]: ", len(candidates)), len(candidates))
		if promptErr != nil {
			return project{}, promptErr
		}
		if choice == 0 {
			return project{}, errors.New("cancelled")
		}
		return candidates[choice-1], nil
	}
	return project{}, fmt.Errorf("invalid arguments: you have %d Storage projects (%s); choose one with --project <id|name> or set %s", len(candidates), storageProjectList(candidates), storageProjectEnv)
}

func storageProjectList(items []project) string {
	parts := make([]string, 0, len(items))
	for _, item := range items {
		parts = append(parts, fmt.Sprintf("%s (%s)", item.Name, item.ID))
	}
	return strings.Join(parts, ", ")
}

func resolveStorageOrganization(token, ref string) (organization, error) {
	orgs, err := fetchOrganizations(token)
	if err != nil {
		return organization{}, err
	}
	for _, org := range orgs {
		if org.ID == ref || strings.EqualFold(org.Slug, ref) {
			return org, nil
		}
	}
	return organization{}, fmt.Errorf("organization '%s' not found (list them with: hubfly orgs)", ref)
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

var storageBucketPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)

// validateStorageBucketName mirrors the regional service's rule.
func validateStorageBucketName(name string) error {
	if !storageBucketPattern.MatchString(name) || strings.Contains(name, "..") || storageIPv4Pattern.MatchString(name) {
		return fmt.Errorf("invalid bucket name %q: use 3-63 lowercase letters, digits, dots and hyphens, starting and ending with a letter or digit", name)
	}
	return nil
}

var storageIPv4Pattern = regexp.MustCompile(`^\d+\.\d+\.\d+\.\d+$`)

// splitStoragePath splits "<bucket>/<key>"; the key may be empty or contain
// slashes.
func splitStoragePath(value string) (string, string, error) {
	bucket, key, _ := strings.Cut(strings.TrimSpace(value), "/")
	if bucket == "" || strings.ContainsAny(bucket, " \t\\") {
		return "", "", fmt.Errorf("invalid object path %q: expected <bucket>/<key>", value)
	}
	return bucket, key, nil
}

// splitStorageObjectPath is splitStoragePath for commands that need one
// object.
func splitStorageObjectPath(value string) (string, string, error) {
	bucket, key, err := splitStoragePath(value)
	if err != nil {
		return "", "", err
	}
	if key == "" || strings.HasSuffix(key, "/") {
		return "", "", fmt.Errorf("invalid object path %q: expected <bucket>/<key> naming one object", value)
	}
	return bucket, key, nil
}

// storageAPIError adds a hint to the Dashboard errors a storage user can act
// on. notFound describes what a 404 from the storage region means for the
// command (for example "bucket 'media' not found"). The wrapped *apiError
// keeps exit codes intact.
func storageAPIError(err error, notFound string) error {
	var apiErr *apiError
	if !errors.As(err, &apiErr) {
		return err
	}
	hint := ""
	switch {
	case apiErr.Code == "STORAGE_GATEWAY_ERROR" && apiErr.Status == 404 && notFound != "":
		hint = notFound + "."
	case apiErr.Code == "STORAGE_GATEWAY_ERROR":
		hint = "The storage region rejected or could not complete the request."
	case apiErr.Code == "REGIONAL_SERVICE_UNAVAILABLE":
		hint = "The project's storage region cannot be reached right now; try again shortly."
	case apiErr.Code == "STORAGE_PROJECT_NOT_FOUND":
		hint = "That project is not a Storage project (list them with: hubfly storage list)."
	case apiErr.Code == "STORAGE_KEY_BUCKET_SCOPE":
		hint = "Your API token is restricted to another bucket."
	case apiErr.Status == 403:
		hint = "Your role on this project does not allow this action."
	}
	if hint == "" {
		return err
	}
	return fmt.Errorf("%w\n%s", err, hint)
}

// storageConfirm asks a yes/no question unless --yes was given. Without a
// terminal it returns an error naming --yes.
func storageConfirm(yes bool, question, action string) (bool, error) {
	if yes {
		return true, nil
	}
	if !rtInteractive() {
		return false, fmt.Errorf("invalid arguments: confirmation required; re-run with --yes to %s", action)
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

// storageTime renders an API timestamp in local time.
func storageTime(value string) string {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return valueOrDash(value)
	}
	return parsed.Local().Format("2006-01-02 15:04:05")
}

// storageExpiry renders an expiry time with the time left.
func storageExpiry(value string) string {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return valueOrDash(value)
	}
	left := time.Until(parsed).Round(time.Minute)
	if left <= 0 {
		return storageTime(value) + " (expired)"
	}
	return fmt.Sprintf("%s (in %s)", storageTime(value), strings.TrimSuffix(strings.TrimSuffix(left.String(), "0s"), "0m"))
}

func storageQuota(used, quota int64) string {
	if quota <= 0 {
		return formatBytes(used)
	}
	return fmt.Sprintf("%s of %s (%.1f%%)", formatBytes(used), formatBytes(quota), float64(used)*100/float64(quota))
}

func storageRetention(seconds int64) string {
	if seconds == 0 {
		return "keep"
	}
	return formatRealtimeSeconds(int(seconds))
}

// storageParallel runs fn for indexes [0, count) on a few workers and returns
// the errors by index.
func storageParallel(count, workers int, fn func(index int) error) []error {
	errs := make([]error, count)
	if workers < 1 {
		workers = 1
	}
	next := make(chan int)
	var wg sync.WaitGroup
	for worker := 0; worker < workers && worker < count; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range next {
				errs[index] = fn(index)
			}
		}()
	}
	for index := 0; index < count; index++ {
		next <- index
	}
	close(next)
	wg.Wait()
	return errs
}

// ---------------------------------------------------------------------------
// list / create / info / usage / delete
// ---------------------------------------------------------------------------

// storageProjectSummary is one row of `hubfly storage list`.
type storageProjectSummary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	RegionID    string `json:"regionId"`
	RegionName  string `json:"regionName"`
	State       string `json:"state"`
	BucketCount int    `json:"bucketCount"`
	UsedBytes   *int64 `json:"usedBytes"`
	QuotaBytes  int64  `json:"quotaBytes"`
	S3Endpoint  string `json:"s3Endpoint,omitempty"`
	Error       string `json:"error,omitempty"`
}

func storageListFlow(args []string) error {
	const usage = "hubfly storage list [--org <id|slug>] [--json]"
	fs := newStorageFlags("list")
	orgRef := fs.String("org", "", "")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) > 0 {
		return fmt.Errorf("unexpected argument: %s\nusage: %s", positionals[0], usage)
	}
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}
	organizationID := ""
	if strings.TrimSpace(*orgRef) != "" {
		org, orgErr := resolveStorageOrganization(token, strings.TrimSpace(*orgRef))
		if orgErr != nil {
			return orgErr
		}
		organizationID = org.ID
	}
	projects, err := fetchStorageProjects(token, organizationID)
	if err != nil {
		return err
	}
	summaries := make([]storageProjectSummary, len(projects))
	storageParallel(len(projects), 4, func(index int) error {
		item := projects[index]
		summary := storageProjectSummary{ID: item.ID, Name: item.Name, RegionID: item.Region.ID, RegionName: item.Region.Name}
		overview, overviewErr := fetchStorageOverview(token, item.ID)
		if overviewErr != nil {
			debugf("storage overview of %s: %v", item.ID, overviewErr)
			summary.State, summary.Error = "unavailable", overviewErr.Error()
		} else {
			summary.State, summary.BucketCount, summary.QuotaBytes, summary.S3Endpoint = overview.State, overview.BucketCount, overview.QuotaBytes, overview.S3Endpoint
			if usage, usageErr := fetchStorageUsage(token, item.ID); usageErr == nil {
				used := usage.StorageUsedBytes
				summary.UsedBytes = &used
			} else {
				debugf("storage usage of %s: %v", item.ID, usageErr)
			}
		}
		summaries[index] = summary
		return nil
	})
	if *asJSON {
		return rtPrintJSON(summaries)
	}
	if len(summaries) == 0 {
		_, _ = fmt.Fprintln(rtOut, "No Storage projects found. Create one with: hubfly storage create <name>")
		return nil
	}
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAME\tSTATUS\tREGION\tBUCKETS\tUSED/QUOTA\tID")
	for _, summary := range summaries {
		buckets, used := "-", "-"
		if summary.Error == "" {
			buckets = fmt.Sprintf("%d", summary.BucketCount)
			used = "- / " + formatBytes(summary.QuotaBytes)
			if summary.UsedBytes != nil {
				used = formatBytes(*summary.UsedBytes) + " / " + formatBytes(summary.QuotaBytes)
			}
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", summary.Name, valueOrDash(summary.State), valueOrDash(summary.RegionName), buckets, used, summary.ID)
	}
	return tw.Flush()
}

func pickStorageRegion(regions []storageRegion, ref string, allowPrompt bool) (storageRegion, error) {
	available := []storageRegion{}
	for _, item := range regions {
		if item.Products.Storage {
			available = append(available, item)
		}
	}
	if len(available) == 0 {
		return storageRegion{}, errors.New("Object Storage is not available in any region yet")
	}
	names := make([]string, 0, len(available))
	for _, item := range available {
		names = append(names, fmt.Sprintf("%s (%s)", item.Name, item.ID))
	}
	ref = strings.TrimSpace(ref)
	if ref != "" {
		for _, item := range available {
			if item.ID == ref || strings.EqualFold(item.Name, ref) || strings.EqualFold(item.Location, ref) {
				return item, nil
			}
		}
		for _, item := range regions {
			if item.ID == ref || strings.EqualFold(item.Name, ref) || strings.EqualFold(item.Location, ref) {
				return storageRegion{}, fmt.Errorf("invalid --region: region '%s' does not offer Object Storage (available: %s)", item.Name, strings.Join(names, ", "))
			}
		}
		return storageRegion{}, fmt.Errorf("invalid --region %q (available: %s)", ref, strings.Join(names, ", "))
	}
	if len(available) == 1 {
		return available[0], nil
	}
	if !allowPrompt || !rtInteractive() {
		return storageRegion{}, fmt.Errorf("invalid arguments: choose a region with --region <id|name> (available: %s)", strings.Join(names, ", "))
	}
	_, _ = fmt.Fprintln(rtOut, "Storage regions (your data stays in the region you pick):")
	for index, item := range available {
		_, _ = fmt.Fprintf(rtOut, "  %d) %s, %s (%s)\n", index+1, item.Name, valueOrDash(item.Location), item.ID)
	}
	choice, err := promptNumber(fmt.Sprintf("Region [1-%d]: ", len(available)), len(available))
	if err != nil {
		return storageRegion{}, err
	}
	if choice == 0 {
		return storageRegion{}, errors.New("cancelled")
	}
	return available[choice-1], nil
}

func storageCreateFlow(args []string) error {
	const usage = "hubfly storage create <name> [--region <id|name>] [--org <id|slug>] [--yes] [--json]"
	fs := newStorageFlags("create")
	regionRef := fs.String("region", "", "")
	orgRef := fs.String("org", "", "")
	yes := storageYesFlag(fs)
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return fmt.Errorf("usage: %s", usage)
	}
	name := strings.TrimSpace(positionals[0])
	if length := len([]rune(name)); length < 3 || length > 64 {
		return fmt.Errorf("invalid project name %q: use 3-64 characters", name)
	}
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}
	regions, err := fetchStorageRegions(token)
	if err != nil {
		return err
	}
	chosen, err := pickStorageRegion(regions, *regionRef, !*asJSON)
	if err != nil {
		return err
	}
	var org organization
	if strings.TrimSpace(*orgRef) != "" {
		if org, err = resolveStorageOrganization(token, strings.TrimSpace(*orgRef)); err != nil {
			return err
		}
	}
	quoteOut := rtOut
	if *asJSON {
		quoteOut = rtErr // keep stdout clean for --json
	}
	owner := "personal"
	if org.ID != "" {
		owner = fmt.Sprintf("organization %s (%s)", org.Name, org.Slug)
	}
	tw := tabwriter.NewWriter(quoteOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "Storage project:\t%s\n", name)
	_, _ = fmt.Fprintf(tw, "Region:\t%s, %s (%s)\n", chosen.Name, valueOrDash(chosen.Location), chosen.ID)
	_, _ = fmt.Fprintf(tw, "Owner:\t%s\n", owner)
	_ = tw.Flush()
	confirmed, err := storageConfirm(*yes, "Create this Storage project?", "create it")
	if err != nil || !confirmed {
		return err
	}
	created, err := createStorageProject(token, name, chosen.ID, org.ID)
	if err != nil {
		var apiErr *apiError
		if errors.As(err, &apiErr) && apiErr.Code == "REGION_PRODUCT_UNAVAILABLE" {
			return fmt.Errorf("%w\nPick another region (see: hubfly storage create --help).", err)
		}
		return fmt.Errorf("failed to create Storage project: %w", err)
	}
	if created.Name == "" {
		created.Name = name
	}
	overview, err := waitForStorageReady(token, created, 2*time.Minute, *asJSON)
	if err != nil {
		return err
	}
	if *asJSON {
		return rtPrintJSON(map[string]any{"project": created, "region": chosen, "storage": overview})
	}
	_, _ = fmt.Fprintf(rtOut, "Storage project '%s' is ready.\n", created.Name)
	tw = tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "  ID:\t%s\n", created.ID)
	_, _ = fmt.Fprintf(tw, "  Region:\t%s\n", chosen.Name)
	_, _ = fmt.Fprintf(tw, "  S3 endpoint:\t%s\n", storageEndpointLabel(overview.S3Endpoint))
	_, _ = fmt.Fprintf(tw, "  Quota:\t%s\n", formatBytes(overview.QuotaBytes))
	_ = tw.Flush()
	_, _ = fmt.Fprintln(rtOut, "\nNext steps:")
	_, _ = fmt.Fprintf(rtOut, "  hubfly storage buckets create <bucket> --project %s\n", created.ID)
	_, _ = fmt.Fprintf(rtOut, "  hubfly storage keys create <name> --project %s   # S3 credentials for your app\n", created.ID)
	return nil
}

// waitForStorageReady polls the project's storage overview until its tenant
// is active. Provisioning normally finishes during the create call.
func waitForStorageReady(token string, created project, timeout time.Duration, quiet bool) (storageOverview, error) {
	spinner := newRealtimeSpinner(quiet)
	defer spinner.stop()
	deadline := time.Now().Add(timeout)
	for {
		overview, err := fetchStorageOverview(token, created.ID)
		if err == nil && overview.State == "active" {
			return overview, nil
		}
		state := overview.State
		if err != nil {
			debugf("waiting for storage of %s: %v", created.ID, err)
			state = "provisioning"
		}
		if time.Now().After(deadline) {
			reason := "state " + valueOrDash(overview.State)
			if err != nil {
				reason = err.Error()
			}
			return overview, fmt.Errorf("Storage project '%s' (%s) was created but its storage is not ready yet (%s); check with: hubfly storage info %s", created.Name, created.ID, reason, created.ID)
		}
		spinner.update("Preparing storage ("+state+")", quiet)
		time.Sleep(rtPollInterval)
	}
}

func storageEndpointLabel(endpoint string) string {
	if strings.TrimSpace(endpoint) == "" {
		return "not configured for this region yet"
	}
	return endpoint
}

// storageUsageTotals sums the daily usage series.
func storageUsageTotals(usage storageUsageSummary) (egressBytes, encodeMinutes float64) {
	for _, day := range usage.EgressBytes {
		egressBytes += day.Quantity
	}
	for _, day := range usage.EncodeMinutes {
		encodeMinutes += day.Quantity
	}
	return egressBytes, encodeMinutes
}

func storageInfoFlow(args []string) error {
	const usage = "hubfly storage info [<project>] [--json]"
	fs := newStorageFlags("info")
	projectRef := storageProjectFlag(fs)
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) > 1 || (len(positionals) == 1 && *projectRef != "") {
		return fmt.Errorf("usage: %s", usage)
	}
	if len(positionals) == 1 {
		*projectRef = positionals[0]
	}
	target, err := resolveStorageTarget(*projectRef, !*asJSON)
	if err != nil {
		return err
	}
	overview, err := fetchStorageOverview(target.token, target.project.ID)
	if err != nil {
		return storageAPIError(err, "")
	}
	usageSummary, usageErr := fetchStorageUsage(target.token, target.project.ID)
	if usageErr != nil {
		debugf("storage usage of %s: %v", target.project.ID, usageErr)
	}
	if *asJSON {
		out := map[string]any{"project": target.project, "storage": overview, "usage": nil}
		if usageErr == nil {
			out["usage"] = usageSummary
		}
		return rtPrintJSON(out)
	}
	rows := [][2]string{
		{"Storage project", target.project.Name},
		{"ID", target.project.ID},
		{"Region", valueOrDash(target.project.Region.Name)},
		{"State", valueOrDash(overview.State)},
		{"S3 endpoint", storageEndpointLabel(overview.S3Endpoint)},
		{"Signing region", storageSigningRegion + " (any SigV4 region is accepted; use path-style addressing)"},
	}
	if usageErr == nil {
		egress, encode := storageUsageTotals(usageSummary)
		rows = append(rows,
			[2]string{"Used", storageQuota(usageSummary.StorageUsedBytes, overview.QuotaBytes)},
			[2]string{"Active media jobs", fmt.Sprintf("%d", usageSummary.ActiveJobs)},
			[2]string{"Egress (30 days)", formatBytes(int64(egress))},
			[2]string{"Encoding (30 days)", fmt.Sprintf("%.1f min", encode)},
		)
	} else {
		rows = append(rows, [2]string{"Quota", formatBytes(overview.QuotaBytes)}, [2]string{"Used", "unavailable"})
	}
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	for _, row := range rows {
		_, _ = fmt.Fprintf(tw, "%s:\t%s\n", row[0], row[1])
	}
	_ = tw.Flush()
	_, _ = fmt.Fprintln(rtOut)
	return printStorageBuckets(overview.Buckets, target.project)
}

func storageUsageFlow(args []string) error {
	const usage = "hubfly storage usage [<project>] [--json]"
	fs := newStorageFlags("usage")
	projectRef := storageProjectFlag(fs)
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) > 1 || (len(positionals) == 1 && *projectRef != "") {
		return fmt.Errorf("usage: %s", usage)
	}
	if len(positionals) == 1 {
		*projectRef = positionals[0]
	}
	target, err := resolveStorageTarget(*projectRef, !*asJSON)
	if err != nil {
		return err
	}
	usageSummary, err := fetchStorageUsage(target.token, target.project.ID)
	if err != nil {
		return storageAPIError(err, "")
	}
	if *asJSON {
		return rtPrintJSON(usageSummary)
	}
	egress, encode := storageUsageTotals(usageSummary)
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "Storage project:\t%s (%s)\n", target.project.Name, target.project.ID)
	_, _ = fmt.Fprintf(tw, "Used:\t%s\n", storageQuota(usageSummary.StorageUsedBytes, usageSummary.QuotaBytes))
	_, _ = fmt.Fprintf(tw, "Active media jobs:\t%d\n", usageSummary.ActiveJobs)
	_, _ = fmt.Fprintf(tw, "Egress (30 days):\t%s\n", formatBytes(int64(egress)))
	_, _ = fmt.Fprintf(tw, "Encoding (30 days):\t%.1f min\n", encode)
	_ = tw.Flush()

	days := map[string][2]float64{}
	for _, day := range usageSummary.EgressBytes {
		entry := days[day.Day]
		entry[0] += day.Quantity
		days[day.Day] = entry
	}
	for _, day := range usageSummary.EncodeMinutes {
		entry := days[day.Day]
		entry[1] += day.Quantity
		days[day.Day] = entry
	}
	if len(days) == 0 {
		_, _ = fmt.Fprintln(rtOut, "\nNo egress or encoding in the last 30 days.")
		return nil
	}
	ordered := make([]string, 0, len(days))
	for day := range days {
		ordered = append(ordered, day)
	}
	sort.Strings(ordered)
	_, _ = fmt.Fprintln(rtOut)
	tw = tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "DAY\tEGRESS\tENCODING")
	for _, day := range ordered {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%.1f min\n", day, formatBytes(int64(days[day][0])), days[day][1])
	}
	return tw.Flush()
}

func storageDeleteFlow(args []string) error {
	const usage = "hubfly storage delete <project> [--yes]"
	fs := newStorageFlags("delete")
	yes := storageYesFlag(fs)
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 1 || strings.TrimSpace(positionals[0]) == "" {
		return fmt.Errorf("usage: %s", usage)
	}
	target, err := resolveStorageTarget(positionals[0], false)
	if err != nil {
		return err
	}
	if !*yes {
		if !rtInteractive() {
			return errors.New("invalid arguments: confirmation required; re-run with --yes to delete")
		}
		contents := ""
		if overview, overviewErr := fetchStorageOverview(target.token, target.project.ID); overviewErr == nil {
			contents = fmt.Sprintf(" It has %d bucket(s).", overview.BucketCount)
		}
		confirmed, promptErr := confirmTyped(fmt.Sprintf(
			"This permanently deletes Storage project '%s' (%s) with all its buckets, objects, published assets and media.%s Its S3 access keys stop working immediately.",
			target.project.Name, target.project.ID, contents), target.project.Name)
		if promptErr != nil {
			return promptErr
		}
		if !confirmed {
			_, _ = fmt.Fprintln(rtOut, "Cancelled: the name did not match.")
			return nil
		}
	}
	if err := deleteStorageProject(target.token, target.project.ID); err != nil {
		return storageAPIError(err, "")
	}
	_, _ = fmt.Fprintf(rtOut, "Deleted Storage project '%s' (%s).\n", target.project.Name, target.project.ID)
	return nil
}

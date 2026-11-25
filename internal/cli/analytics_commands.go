package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
)

// `hubfly analytics`: Web Analytics sites, install snippets, reports,
// exports, usage and ingest keys. Shares the flag parsing, JSON output,
// prompts and helpers of `hubfly realtime` and `hubfly storage`.

const analyticsUsageText = `Web Analytics (cookieless website analytics):
  hubfly analytics sites [list] [--project <p>] [--json]
  hubfly analytics sites create <domain> [--name <name>] [--domain <domain>]... [--include-subdomains]
                                [--timezone <tz>] [--retention <days>] [--project <p>|new] [--region <id|name>]
                                [--yes] [--json]
  hubfly analytics sites enable --domain <hostname> [--name <name>] [--no-inject] [--project <p>] [--json]
  hubfly analytics sites info|status <site> [--json]
  hubfly analytics sites pause|resume <site> [--json]
  hubfly analytics sites delete <site> [--yes]
  hubfly analytics verify <domain> [--project <p>] [--json]
  hubfly analytics snippet <site> [--json]

Reports:
  hubfly analytics stats <site> [--period today|7d|30d|month|6mo|12mo | --from <date> --to <date>]
                         [--by <dimension>] [--limit <n>] [--filter <dim>=<value>]... [--compare] [--json]
  hubfly analytics realtime <site> [--watch] [--json]
  hubfly analytics export <site> --from <date> --to <date> [--kind events|sessions|summaries]
                          [--format csv|ndjson] [--output <file>|-] [--force]
  hubfly analytics usage [<project>] [--json]

Ingest keys (server-side events API):
  hubfly analytics keys [list] <site> [--json]
  hubfly analytics keys create <site> <name> [--json]
  hubfly analytics keys revoke <site> <keyId> [--yes]

<site> is a site id, public id (hfa_…), name or domain; it may be omitted when you have exactly one
site. <p> is a project id or name. "sites create" tracks a website hosted anywhere: its domain must be
a verified custom domain (see "hubfly analytics verify"); with --project new it creates a standalone
Analytics project. "sites enable" tracks a domain your Cell or Box project serves on HubFly and
injects the script for you. Dates are site-local (YYYY-MM-DD). An ingest key is printed once.`

func printAnalyticsUsage() {
	_, _ = fmt.Fprintln(rtOut, analyticsUsageText)
}

func runAnalyticsGroup(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		printAnalyticsUsage()
		return nil
	}
	command, rest := args[0], args[1:]
	switch command {
	case "sites", "site":
		return runAnalyticsSitesGroup(rest)
	case "list":
		return analyticsSitesListFlow(rest)
	case "verify":
		return analyticsVerifyFlow(rest)
	case "snippet", "install":
		return analyticsSnippetFlow(rest)
	case "stats", "report":
		return analyticsStatsFlow(rest)
	case "realtime", "live":
		return analyticsRealtimeFlow(rest)
	case "export":
		return analyticsExportFlow(rest)
	case "usage":
		return analyticsUsageFlow(rest)
	case "keys", "key":
		return runAnalyticsKeysGroup(rest)
	default:
		return fmt.Errorf("unknown analytics command %q (run: hubfly analytics help)", command)
	}
}

func runAnalyticsSitesGroup(args []string) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return analyticsSitesListFlow(args)
	}
	command, rest := args[0], args[1:]
	switch command {
	case "list":
		return analyticsSitesListFlow(rest)
	case "create", "new", "add":
		return analyticsSitesCreateFlow(rest)
	case "enable":
		return analyticsSitesEnableFlow(rest)
	case "info", "show", "inspect":
		return analyticsSiteInfoFlow(rest)
	case "status":
		return analyticsSiteStatusFlow(rest)
	case "pause":
		return analyticsSiteStateFlow("pause", rest)
	case "resume":
		return analyticsSiteStateFlow("resume", rest)
	case "delete", "remove", "rm":
		return analyticsSiteDeleteFlow(rest)
	default:
		return fmt.Errorf("unknown analytics sites command %q (run: hubfly analytics help)", command)
	}
}

func newAnalyticsFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet("analytics "+name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

// ---------------------------------------------------------------------------
// Project and site resolution
// ---------------------------------------------------------------------------

// isAnalyticsHostProject reports whether a project can have sites: Cell and
// Box projects, and standalone Analytics projects.
func isAnalyticsHostProject(item project) bool {
	switch item.Type {
	case "", "cell", "box", "analytics":
		return true
	}
	return false
}

// analyticsProjects returns the projects that can have sites, or the one
// named by ref (id or case-insensitive name).
func analyticsProjects(token, ref string) ([]project, error) {
	all, err := fetchAllProjectsAnyType(token, "")
	if err != nil {
		return nil, err
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		result := []project{}
		for _, item := range all {
			if isAnalyticsHostProject(item) {
				result = append(result, item)
			}
		}
		return result, nil
	}
	var byName []project
	for _, item := range all {
		if item.ID == ref {
			byName = []project{item}
			break
		}
		if strings.EqualFold(item.Name, ref) {
			byName = append(byName, item)
		}
	}
	switch len(byName) {
	case 0:
		return nil, fmt.Errorf("project '%s' not found (list them with: hubfly projects)", ref)
	case 1:
		if !isAnalyticsHostProject(byName[0]) {
			return nil, fmt.Errorf("project '%s' is a %s project; Web Analytics sites live in Cell, Box or Analytics projects", byName[0].Name, byName[0].Type)
		}
		return byName, nil
	}
	return nil, fmt.Errorf("project name '%s' is ambiguous (%s); use the project id", ref, storageProjectList(byName))
}

// listAnalyticsSites returns the sites of the caller's projects (or of the
// project named by ref). Projects that cannot be read are skipped unless one
// was named.
func listAnalyticsSites(token, ref string) ([]analyticsSite, error) {
	projects, err := analyticsProjects(token, ref)
	if err != nil {
		return nil, err
	}
	perProject := make([][]analyticsSite, len(projects))
	errs := fanOut(len(projects), func(index int) error {
		item := projects[index]
		overview, fetchErr := fetchAnalyticsOverview(token, item.ID)
		if fetchErr != nil {
			return fetchErr
		}
		for _, site := range overview.Sites {
			site.ProjectName = item.Name
			perProject[index] = append(perProject[index], site)
		}
		return nil
	})
	sites := []analyticsSite{}
	for index, fetchErr := range errs {
		if fetchErr != nil {
			if ref != "" {
				return nil, analyticsAPIError(fetchErr)
			}
			debugf("skipping project %s: %v", projects[index].ID, fetchErr)
			continue
		}
		sites = append(sites, perProject[index]...)
	}
	return sites, nil
}

// resolveAnalyticsSite finds one site by id, public id, then case-insensitive
// name or domain. Without a reference it uses the only site.
func resolveAnalyticsSite(token, ref, projectRef string) (analyticsSite, error) {
	sites, err := listAnalyticsSites(token, projectRef)
	if err != nil {
		return analyticsSite{}, err
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		switch len(sites) {
		case 0:
			return analyticsSite{}, errors.New("no Web Analytics sites found; add one with: hubfly analytics sites create <domain>")
		case 1:
			return sites[0], nil
		}
		return analyticsSite{}, fmt.Errorf("invalid arguments: you have %d sites (%s); name one", len(sites), analyticsSiteList(sites))
	}
	var byName []analyticsSite
	for _, site := range sites {
		if site.ID == ref || site.PublicID == ref {
			return site, nil
		}
		if strings.EqualFold(site.Name, ref) || analyticsSiteHasDomain(site, ref) {
			byName = append(byName, site)
		}
	}
	if len(byName) == 1 {
		return byName[0], nil
	}
	if len(byName) > 1 {
		return analyticsSite{}, fmt.Errorf("site '%s' is ambiguous (%s); use the site id or --project", ref, analyticsSiteList(byName))
	}
	return analyticsSite{}, fmt.Errorf("site '%s' not found (list them with: hubfly analytics sites)", ref)
}

func analyticsSiteHasDomain(site analyticsSite, domain string) bool {
	domain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
	if strings.EqualFold(site.PrimaryDomain, domain) {
		return true
	}
	for _, item := range site.Domains {
		if strings.EqualFold(item, domain) {
			return true
		}
	}
	return false
}

func analyticsSiteList(sites []analyticsSite) string {
	parts := make([]string, 0, len(sites))
	for _, site := range sites {
		parts = append(parts, fmt.Sprintf("%s in project '%s' (%s)", site.Name, site.ProjectName, site.ID))
	}
	return strings.Join(parts, ", ")
}

// analyticsSiteTarget parses the common "<site> [--project <p>]" form and
// resolves the site.
func analyticsSiteTarget(name, usage string, args []string, extra func(fs *flag.FlagSet)) (string, analyticsSite, *flag.FlagSet, []string, error) {
	fs := newAnalyticsFlags(name)
	projectRef := storageProjectFlag(fs)
	if extra != nil {
		extra(fs)
	}
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return "", analyticsSite{}, nil, nil, err
	}
	ref := ""
	if len(positionals) > 0 {
		ref, positionals = positionals[0], positionals[1:]
	}
	token, err := ensureAuth(true)
	if err != nil {
		return "", analyticsSite{}, nil, nil, err
	}
	site, err := resolveAnalyticsSite(token, ref, *projectRef)
	if err != nil {
		return "", analyticsSite{}, nil, nil, err
	}
	return token, site, fs, positionals, nil
}

// analyticsAPIError adds a hint to the Dashboard errors an analytics user can
// act on. The wrapped *apiError keeps exit codes intact.
func analyticsAPIError(err error) error {
	var apiErr *apiError
	if !errors.As(err, &apiErr) {
		return err
	}
	hint := ""
	switch apiErr.Code {
	case "ANALYTICS_DOMAIN_UNVERIFIED":
		hint = "Verify the domain first: hubfly analytics verify <domain> --project <p>."
	case "ANALYTICS_UNAVAILABLE":
		hint = "Existing sites keep collecting; new sites can be added once Web Analytics opens in this region."
	case "ANALYTICS_UNREACHABLE":
		hint = "The site's analytics region cannot be reached right now; try again shortly."
	case "ANALYTICS_QUERY_TIMEOUT":
		hint = "Narrow the date range or the filters and try again."
	case "ANALYTICS_RETENTION_REQUIRES_BALANCE":
		hint = "Top up your balance to keep data longer."
	case "ANALYTICS_PROJECT_TYPE_UNSUPPORTED":
		hint = "Sites live in Cell, Box or Analytics projects (create one with: hubfly analytics sites create <domain> --project new)."
	}
	if hint == "" && apiErr.Status == 403 {
		hint = "Your role on this project does not allow this action."
	}
	if hint == "" {
		return err
	}
	return fmt.Errorf("%w\n%s", err, hint)
}

// ---------------------------------------------------------------------------
// sites list / info / status / pause / resume / delete
// ---------------------------------------------------------------------------

func analyticsSitesListFlow(args []string) error {
	const usage = "hubfly analytics sites [list] [--project <p>] [--json]"
	fs := newAnalyticsFlags("sites list")
	projectRef := storageProjectFlag(fs)
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
	sites, err := listAnalyticsSites(token, *projectRef)
	if err != nil {
		return err
	}
	sort.SliceStable(sites, func(i, j int) bool {
		if sites[i].ProjectName != sites[j].ProjectName {
			return sites[i].ProjectName < sites[j].ProjectName
		}
		return sites[i].Name < sites[j].Name
	})
	if *asJSON {
		return rtPrintJSON(sites)
	}
	if len(sites) == 0 {
		_, _ = fmt.Fprintln(rtOut, "No Web Analytics sites found. Add one with: hubfly analytics sites create <domain>")
		return nil
	}
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAME\tDOMAIN\tSTATE\tSOURCE\tLAST EVENT\tPROJECT\tID")
	for _, site := range sites {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", site.Name, site.PrimaryDomain, analyticsStateLabel(site), analyticsSourceLabel(site.Source), analyticsLastEvent(site.LastEventAt), site.ProjectName, site.ID)
	}
	return tw.Flush()
}

func analyticsSourceLabel(source string) string {
	if source == "hubfly_route" {
		return "hubfly"
	}
	return valueOrDash(source)
}

// analyticsStateLabel adds a pending or failed sync to the site state.
func analyticsStateLabel(site analyticsSite) string {
	state := valueOrDash(site.State)
	switch {
	case site.Sync.Status == "error" || site.Proxy.Status == "error":
		state += " (sync error)"
	case site.Sync.Status == "pending" || site.Proxy.Status == "pending":
		state += " (syncing)"
	}
	return state
}

func analyticsLastEvent(value *string) string {
	if value == nil || *value == "" {
		return "never"
	}
	return storageTime(*value)
}

func analyticsSiteInfoFlow(args []string) error {
	const usage = "hubfly analytics sites info <site> [--project <p>] [--json]"
	var asJSON *bool
	_, site, _, rest, err := analyticsSiteTarget("sites info", usage, args, func(fs *flag.FlagSet) { asJSON = fs.Bool("json", false, "") })
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return fmt.Errorf("unexpected argument: %s\nusage: %s", rest[0], usage)
	}
	if *asJSON {
		return rtPrintJSON(site)
	}
	printAnalyticsSite(site)
	return nil
}

func printAnalyticsSite(site analyticsSite) {
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	row := func(label, value string) { _, _ = fmt.Fprintf(tw, "%s\t%s\n", label, value) }
	row("Name:", site.Name)
	row("ID:", site.ID)
	row("Public ID:", site.PublicID)
	row("Project:", fmt.Sprintf("%s (%s)", valueOrDash(site.ProjectName), site.ProjectID))
	row("State:", analyticsStateLabel(site))
	domains := strings.Join(site.Domains, ", ")
	if site.IncludeSubdomains {
		domains += " (and subdomains)"
	}
	row("Domains:", domains)
	if site.Source == "hubfly_route" {
		inject := "no (add the snippet yourself)"
		if site.InjectEnabled {
			inject = "yes"
		}
		row("Served by:", "HubFly")
		row("Script injected:", inject)
	} else {
		row("Served by:", "your own host (external)")
	}
	row("Time zone:", site.Timezone)
	row("Retention:", fmt.Sprintf("%d days", site.RetentionDays))
	row("Visitors:", site.IdentityMode)
	row("Last event:", analyticsLastEvent(site.LastEventAt))
	row("Storage:", formatBytes(site.DBBytes))
	if site.Sync.Error != nil && *site.Sync.Error != "" {
		row("Sync error:", *site.Sync.Error)
	}
	if site.Proxy.Error != nil && *site.Proxy.Error != "" {
		row("Proxy error:", *site.Proxy.Error)
	}
	row("Created:", storageTime(site.CreatedAt))
	_ = tw.Flush()
}

func analyticsSiteStatusFlow(args []string) error {
	const usage = "hubfly analytics sites status <site> [--project <p>] [--json]"
	var asJSON *bool
	token, site, _, rest, err := analyticsSiteTarget("sites status", usage, args, func(fs *flag.FlagSet) { asJSON = fs.Bool("json", false, "") })
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return fmt.Errorf("unexpected argument: %s\nusage: %s", rest[0], usage)
	}
	status, err := fetchAnalyticsSiteStatus(token, site.ProjectID, site.ID)
	if err != nil {
		return analyticsAPIError(err)
	}
	if *asJSON {
		return rtPrintJSON(status)
	}
	switch {
	case !status.Reachable:
		_, _ = fmt.Fprintf(rtOut, "%s: the analytics region cannot be reached right now.\n", site.Name)
	case status.Installed:
		_, _ = fmt.Fprintf(rtOut, "%s is installed: last event %s, %d events today, %s stored.\n", site.Name, analyticsLastEvent(status.LastEventAt), status.EventsToday, formatBytes(status.DBBytes))
	default:
		_, _ = fmt.Fprintf(rtOut, "%s has not received any event yet. Add the snippet (hubfly analytics snippet %s) and open the site.\n", site.Name, site.ID)
	}
	return nil
}

func analyticsSiteStateFlow(action string, args []string) error {
	usage := "hubfly analytics sites " + action + " <site> [--project <p>] [--json]"
	var asJSON *bool
	token, site, _, rest, err := analyticsSiteTarget("sites "+action, usage, args, func(fs *flag.FlagSet) { asJSON = fs.Bool("json", false, "") })
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return fmt.Errorf("unexpected argument: %s\nusage: %s", rest[0], usage)
	}
	state := "active"
	if action == "pause" {
		state = "paused"
	}
	updated, err := updateAnalyticsSite(token, site.ProjectID, site.ID, map[string]any{"state": state})
	if err != nil {
		return analyticsAPIError(err)
	}
	updated.ProjectName = site.ProjectName
	if *asJSON {
		return rtPrintJSON(updated)
	}
	if state == "paused" {
		_, _ = fmt.Fprintf(rtOut, "Paused %s: nothing is collected until you resume it; its data is kept.\n", site.Name)
	} else {
		_, _ = fmt.Fprintf(rtOut, "Resumed %s.\n", site.Name)
	}
	return nil
}

func analyticsSiteDeleteFlow(args []string) error {
	const usage = "hubfly analytics sites delete <site> [--yes] [--project <p>]"
	var yes *bool
	token, site, _, rest, err := analyticsSiteTarget("sites delete", usage, args, func(fs *flag.FlagSet) { yes = storageYesFlag(fs) })
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return fmt.Errorf("unexpected argument: %s\nusage: %s", rest[0], usage)
	}
	confirmed, err := storageConfirm(*yes, fmt.Sprintf("Delete site %s (%s) and all of its data?", site.Name, site.PrimaryDomain), "delete the site")
	if err != nil || !confirmed {
		return err
	}
	result, err := deleteAnalyticsSite(token, site.ProjectID, site.ID)
	if err != nil {
		return analyticsAPIError(err)
	}
	if result.Pending {
		_, _ = fmt.Fprintf(rtOut, "Deleted %s. HubFly finishes removing its data in the region shortly.\n", site.Name)
	} else {
		_, _ = fmt.Fprintf(rtOut, "Deleted %s.\n", site.Name)
	}
	return nil
}

// ---------------------------------------------------------------------------
// sites create / enable / verify / snippet
// ---------------------------------------------------------------------------

// analyticsHostProject resolves where a new site goes: --project <p>, "new"
// (a standalone Analytics project), the only Analytics project, or a pick.
func analyticsHostProject(token, ref, regionRef, newName string, allowPrompt bool) (project, error) {
	ref = strings.TrimSpace(ref)
	if strings.EqualFold(ref, "new") {
		return createAnalyticsProjectFlow(token, newName, regionRef, allowPrompt)
	}
	if ref != "" {
		projects, err := analyticsProjects(token, ref)
		if err != nil {
			return project{}, err
		}
		return projects[0], nil
	}
	all, err := fetchAllProjectsAnyType(token, "")
	if err != nil {
		return project{}, err
	}
	standalone := []project{}
	for _, item := range all {
		if item.Type == "analytics" {
			standalone = append(standalone, item)
		}
	}
	if len(standalone) == 1 {
		return standalone[0], nil
	}
	if allowPrompt && rtInteractive() {
		_, _ = fmt.Fprintln(rtOut, "Add the site to:")
		for index, item := range standalone {
			_, _ = fmt.Fprintf(rtOut, "  %d) %s (%s)\n", index+1, item.Name, item.ID)
		}
		_, _ = fmt.Fprintf(rtOut, "  %d) A new Analytics project\n", len(standalone)+1)
		choice, promptErr := promptNumber(fmt.Sprintf("Project [1-%d]: ", len(standalone)+1), len(standalone)+1)
		if promptErr != nil {
			return project{}, promptErr
		}
		if choice == 0 {
			return project{}, errors.New("cancelled")
		}
		if choice <= len(standalone) {
			return standalone[choice-1], nil
		}
		return createAnalyticsProjectFlow(token, newName, regionRef, allowPrompt)
	}
	if len(standalone) == 0 {
		return project{}, errors.New("invalid arguments: choose a project with --project <id|name>, or --project new to create an Analytics project")
	}
	return project{}, fmt.Errorf("invalid arguments: you have %d Analytics projects (%s); choose one with --project <id|name>", len(standalone), storageProjectList(standalone))
}

func createAnalyticsProjectFlow(token, name, regionRef string, allowPrompt bool) (project, error) {
	regions, err := fetchAnalyticsRegions(token)
	if err != nil {
		return project{}, err
	}
	if len(regions) == 0 {
		return project{}, errors.New("Web Analytics is not available in any region yet")
	}
	chosen, err := pickAnalyticsRegion(regions, regionRef, allowPrompt)
	if err != nil {
		return project{}, err
	}
	created, err := createAnalyticsProject(token, name, chosen.ID)
	if err != nil {
		return project{}, analyticsAPIError(err)
	}
	if created.Name == "" {
		created.Name = name
	}
	_, _ = fmt.Fprintf(rtErr, "Created Analytics project %s in %s.\n", created.Name, chosen.Name)
	return created, nil
}

func pickAnalyticsRegion(regions []analyticsRegion, ref string, allowPrompt bool) (analyticsRegion, error) {
	ref = strings.TrimSpace(ref)
	if ref != "" {
		for _, item := range regions {
			if item.ID == ref || strings.EqualFold(item.Name, ref) {
				return item, nil
			}
		}
		names := make([]string, 0, len(regions))
		for _, item := range regions {
			names = append(names, item.Name)
		}
		return analyticsRegion{}, fmt.Errorf("invalid arguments: region '%s' does not offer Web Analytics (available: %s)", ref, strings.Join(names, ", "))
	}
	if len(regions) == 1 {
		return regions[0], nil
	}
	if allowPrompt && rtInteractive() {
		_, _ = fmt.Fprintln(rtOut, "Regions:")
		for index, item := range regions {
			_, _ = fmt.Fprintf(rtOut, "  %d) %s (%s)\n", index+1, item.Name, valueOrDash(item.Location))
		}
		choice, err := promptNumber(fmt.Sprintf("Region [1-%d]: ", len(regions)), len(regions))
		if err != nil {
			return analyticsRegion{}, err
		}
		if choice == 0 {
			return analyticsRegion{}, errors.New("cancelled")
		}
		return regions[choice-1], nil
	}
	return analyticsRegion{}, errors.New("invalid arguments: choose a region with --region <id|name>")
}

// normalizeAnalyticsDomain accepts a hostname or URL and returns the
// lowercase hostname.
func normalizeAnalyticsDomain(value string) (string, error) {
	domain := strings.ToLower(strings.TrimSpace(value))
	if index := strings.Index(domain, "://"); index >= 0 {
		domain = domain[index+3:]
	}
	if index := strings.IndexAny(domain, "/?#"); index >= 0 {
		domain = domain[:index]
	}
	if index := strings.LastIndex(domain, ":"); index >= 0 {
		domain = domain[:index]
	}
	domain = strings.TrimSuffix(domain, ".")
	if domain == "" || !strings.Contains(domain, ".") || strings.ContainsAny(domain, " \t@*") {
		return "", fmt.Errorf("invalid domain %q: use a hostname such as example.com", value)
	}
	return domain, nil
}

func analyticsSitesCreateFlow(args []string) error {
	const usage = "hubfly analytics sites create <domain> [--name <name>] [--domain <domain>]... [--include-subdomains] [--timezone <tz>] [--retention <days>] [--project <p>|new] [--region <id|name>] [--yes] [--json]"
	fs := newAnalyticsFlags("sites create")
	projectRef := storageProjectFlag(fs)
	name := fs.String("name", "", "")
	var extra rtList
	extra.split = true
	fs.Var(&extra, "domain", "")
	includeSubdomains := fs.Bool("include-subdomains", false, "")
	timezone := fs.String("timezone", "", "")
	retention := fs.Int("retention", 0, "")
	regionRef := fs.String("region", "", "")
	yes := storageYesFlag(fs)
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) > 1 {
		return fmt.Errorf("unexpected argument: %s\nusage: %s", positionals[1], usage)
	}
	rawDomains := append(append([]string{}, positionals...), extra.values...)
	if len(rawDomains) == 0 {
		return fmt.Errorf("usage: %s", usage)
	}
	domains := []string{}
	for _, raw := range rawDomains {
		domain, domainErr := normalizeAnalyticsDomain(raw)
		if domainErr != nil {
			return domainErr
		}
		if !containsString(domains, domain) {
			domains = append(domains, domain)
		}
	}
	if *retention != 0 && (*retention < 30 || *retention > 365) {
		return errors.New("invalid arguments: --retention must be 30 to 365 days")
	}
	siteName := strings.TrimSpace(*name)
	if siteName == "" {
		siteName = domains[0]
	}
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}
	if strings.EqualFold(strings.TrimSpace(*projectRef), "new") {
		confirmed, confirmErr := storageConfirm(*yes, fmt.Sprintf("Create a new Analytics project %q for %s?", siteName, domains[0]), "create the project")
		if confirmErr != nil || !confirmed {
			return confirmErr
		}
	}
	host, err := analyticsHostProject(token, *projectRef, *regionRef, siteName, !*asJSON)
	if err != nil {
		return err
	}
	body := map[string]any{"name": siteName, "source": "external", "domains": domains}
	if *includeSubdomains {
		body["includeSubdomains"] = true
	}
	if strings.TrimSpace(*timezone) != "" {
		body["timezone"] = strings.TrimSpace(*timezone)
	}
	if *retention != 0 {
		body["retentionDays"] = *retention
	}
	site, err := createAnalyticsSite(token, host.ID, body)
	if err != nil {
		return analyticsAPIError(err)
	}
	site.ProjectName = host.Name
	if *asJSON {
		return rtPrintJSON(site)
	}
	_, _ = fmt.Fprintf(rtOut, "Created site %s (%s) in project %s.\n\n", site.Name, site.ID, host.Name)
	printAnalyticsSnippet(site)
	return nil
}

func containsString(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

func analyticsSitesEnableFlow(args []string) error {
	const usage = "hubfly analytics sites enable --domain <hostname> [--name <name>] [--no-inject] [--project <p>] [--json]"
	fs := newAnalyticsFlags("sites enable")
	projectRef := storageProjectFlag(fs)
	domainRef := fs.String("domain", "", "")
	name := fs.String("name", "", "")
	noInject := fs.Bool("no-inject", false, "")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if *domainRef == "" && len(positionals) == 1 {
		*domainRef, positionals = positionals[0], nil
	}
	if len(positionals) > 0 {
		return fmt.Errorf("unexpected argument: %s\nusage: %s", positionals[0], usage)
	}
	if strings.TrimSpace(*domainRef) == "" {
		return fmt.Errorf("usage: %s", usage)
	}
	domain, err := normalizeAnalyticsDomain(*domainRef)
	if err != nil {
		return err
	}
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}
	projects, err := analyticsProjects(token, *projectRef)
	if err != nil {
		return err
	}
	type match struct {
		project project
		route   analyticsServedRoute
	}
	found := make([][]match, len(projects))
	errs := fanOut(len(projects), func(index int) error {
		if projects[index].Type == "analytics" {
			return nil
		}
		routes, routesErr := fetchAnalyticsRoutes(token, projects[index].ID)
		if routesErr != nil {
			return routesErr
		}
		for _, route := range routes {
			if strings.EqualFold(route.Hostname, domain) {
				found[index] = append(found[index], match{project: projects[index], route: route})
			}
		}
		return nil
	})
	matches := []match{}
	for index, routesErr := range errs {
		if routesErr != nil {
			if *projectRef != "" {
				return analyticsAPIError(routesErr)
			}
			debugf("skipping project %s: %v", projects[index].ID, routesErr)
			continue
		}
		matches = append(matches, found[index]...)
	}
	if len(matches) == 0 {
		return fmt.Errorf("no Cell or Box project serves %s on HubFly; for a website hosted elsewhere use: hubfly analytics sites create %s", domain, domain)
	}
	if len(matches) > 1 {
		names := make([]string, 0, len(matches))
		for _, item := range matches {
			names = append(names, fmt.Sprintf("%s (%s)", item.project.Name, item.project.ID))
		}
		return fmt.Errorf("%s is served by several projects (%s); choose one with --project", domain, strings.Join(names, ", "))
	}
	chosen := matches[0]
	if chosen.route.SiteID != nil && *chosen.route.SiteID != "" {
		return fmt.Errorf("%s already has a site (%s); see: hubfly analytics sites info %s", domain, *chosen.route.SiteID, *chosen.route.SiteID)
	}
	siteName := strings.TrimSpace(*name)
	if siteName == "" {
		siteName = domain
	}
	body := map[string]any{
		"name": siteName, "source": "hubfly_route", "domain": domain,
		"routeType": chosen.route.RouteType, "routeId": chosen.route.RouteID, "injectEnabled": !*noInject,
	}
	site, err := createAnalyticsSite(token, chosen.project.ID, body)
	if err != nil {
		return analyticsAPIError(err)
	}
	site.ProjectName = chosen.project.Name
	if *asJSON {
		return rtPrintJSON(site)
	}
	_, _ = fmt.Fprintf(rtOut, "Enabled Web Analytics on %s (site %s, project %s).\n", domain, site.ID, chosen.project.Name)
	if site.InjectEnabled {
		_, _ = fmt.Fprintln(rtOut, "HubFly adds the script to your HTML pages; nothing to install. Check it with: hubfly analytics sites status "+site.ID)
		return nil
	}
	_, _ = fmt.Fprintln(rtOut)
	printAnalyticsSnippet(site)
	return nil
}

func analyticsVerifyFlow(args []string) error {
	const usage = "hubfly analytics verify <domain> [--project <p>] [--json]"
	fs := newAnalyticsFlags("verify")
	projectRef := storageProjectFlag(fs)
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return fmt.Errorf("usage: %s", usage)
	}
	if strings.EqualFold(strings.TrimSpace(*projectRef), "new") {
		return errors.New("invalid arguments: verify a domain in an existing project (create one with: hubfly analytics sites create <domain> --project new)")
	}
	domain, err := normalizeAnalyticsDomain(positionals[0])
	if err != nil {
		return err
	}
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}
	host, err := analyticsHostProject(token, *projectRef, "", domain, false)
	if err != nil {
		return err
	}
	result, err := verifyAnalyticsDomain(token, host.ID, domain)
	if err != nil {
		return analyticsAPIError(err)
	}
	if *asJSON {
		return rtPrintJSON(result)
	}
	if result.Status == "verified" {
		_, _ = fmt.Fprintf(rtOut, "%s is verified. Add it with: hubfly analytics sites create %s\n", result.Domain, result.Domain)
		return nil
	}
	_, _ = fmt.Fprintf(rtOut, "%s is not verified yet. Add this DNS record, then run this command again:\n\n", result.Domain)
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "  TYPE\tHOST\tVALUE")
	_, _ = fmt.Fprintf(tw, "  TXT\t%s\t%s\n", valueOrDash(derefString(result.TxtHost)), valueOrDash(derefString(result.TxtValue)))
	_ = tw.Flush()
	if strings.TrimSpace(result.Message) != "" {
		_, _ = fmt.Fprintf(rtOut, "\n%s\n", result.Message)
	}
	return nil
}

func analyticsSnippetFlow(args []string) error {
	const usage = "hubfly analytics snippet <site> [--project <p>] [--json]"
	var asJSON *bool
	_, site, _, rest, err := analyticsSiteTarget("snippet", usage, args, func(fs *flag.FlagSet) { asJSON = fs.Bool("json", false, "") })
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return fmt.Errorf("unexpected argument: %s\nusage: %s", rest[0], usage)
	}
	if *asJSON {
		return rtPrintJSON(site.Install)
	}
	printAnalyticsSnippet(site)
	return nil
}

func printAnalyticsSnippet(site analyticsSite) {
	if site.Install.AutoInjected {
		_, _ = fmt.Fprintf(rtOut, "HubFly adds the script to the HTML pages of %s; you only need the snippet for pages served elsewhere:\n\n", site.PrimaryDomain)
	} else {
		_, _ = fmt.Fprintf(rtOut, "Add this to the <head> of every page of %s:\n\n", site.PrimaryDomain)
	}
	_, _ = fmt.Fprintln(rtOut, strings.TrimSpace(site.Install.Snippet))
	if site.Install.EventsAPIURL != "" {
		_, _ = fmt.Fprintf(rtOut, "\nServer-side events: POST %s with an ingest key (hubfly analytics keys create %s <name>).\n", site.Install.EventsAPIURL, site.ID)
	}
}

// ---------------------------------------------------------------------------
// usage
// ---------------------------------------------------------------------------

func analyticsUsageFlow(args []string) error {
	const usage = "hubfly analytics usage [<project>] [--json]"
	fs := newAnalyticsFlags("usage")
	projectRef := storageProjectFlag(fs)
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) > 1 {
		return fmt.Errorf("unexpected argument: %s\nusage: %s", positionals[1], usage)
	}
	ref := *projectRef
	if len(positionals) == 1 {
		ref = positionals[0]
	}
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}
	var host project
	if strings.TrimSpace(ref) == "" {
		sites, listErr := listAnalyticsSites(token, "")
		if listErr != nil {
			return listErr
		}
		ids := map[string]string{}
		for _, site := range sites {
			ids[site.ProjectID] = site.ProjectName
		}
		if len(ids) != 1 {
			if len(ids) == 0 {
				return errors.New("no Web Analytics sites found; add one with: hubfly analytics sites create <domain>")
			}
			return errors.New("invalid arguments: your sites are in several projects; name one: hubfly analytics usage <project>")
		}
		for id, name := range ids {
			host = project{ID: id, Name: name}
		}
	} else {
		projects, resolveErr := analyticsProjects(token, ref)
		if resolveErr != nil {
			return resolveErr
		}
		host = projects[0]
	}
	report, err := fetchAnalyticsUsage(token, host.ID)
	if err != nil {
		return analyticsAPIError(err)
	}
	if *asJSON {
		return rtPrintJSON(report)
	}
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	row := func(label, value string) { _, _ = fmt.Fprintf(tw, "%s\t%s\n", label, value) }
	row("Project:", fmt.Sprintf("%s (%s)", valueOrDash(host.Name), host.ID))
	row("Month:", report.Month)
	row("Events:", fmt.Sprintf("%s billable, %s filtered, %s not collected", formatAnalyticsCount(report.Events.Billable), formatAnalyticsCount(report.Events.Filtered), formatAnalyticsCount(report.Events.NotCollected)))
	row("Last 24 hours:", formatAnalyticsCount(report.Events.Last24h))
	row("Projected:", formatAnalyticsCount(report.Events.ProjectedMonthEnd)+" by month end")
	row("Free allowance:", fmt.Sprintf("%s of %s left this month (shared by your projects)", formatAnalyticsCount(report.Allowance.OwnerFreeRemaining), formatAnalyticsCount(report.Allowance.FreeEventsPerMonth)))
	if report.MonthlyEventCap != nil {
		row("Event cap:", formatAnalyticsCount(int64(*report.MonthlyEventCap))+" per month")
	}
	if report.Cost != nil {
		row("Cost so far:", formatMicroUSD(report.Cost.BilledMicroUSD))
		row("Projected cost:", formatMicroUSD(report.Cost.ProjectedMonthEndMicroUSD+report.Cost.HistoryMonthlyMicroUSD))
	}
	for _, state := range report.Collection {
		switch {
		case state.EventCapReached:
			row("Collection:", "stopped: the monthly event cap is reached")
		case state.Paused:
			reason := "billing"
			if state.Reason != nil && *state.Reason == "spend_cap" {
				reason = "the spend cap"
			} else if state.Reason != nil && *state.Reason == "balance" {
				reason = "a low balance"
			}
			row("Collection:", "paid events paused by "+reason+" (free events still count)")
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if len(report.Sites) > 0 {
		_, _ = fmt.Fprintln(rtOut)
		sites := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
		_, _ = fmt.Fprintln(sites, "SITE\tBILLABLE EVENTS")
		for _, site := range report.Sites {
			_, _ = fmt.Fprintf(sites, "%s\t%s\n", site.Name, formatAnalyticsCount(site.Billable))
		}
		return sites.Flush()
	}
	return nil
}

func formatMicroUSD(value int64) string {
	return fmt.Sprintf("$%.2f", float64(value)/1e6)
}

// formatAnalyticsCount groups thousands: 1234567 -> 1,234,567.
func formatAnalyticsCount(value int64) string {
	sign := ""
	if value < 0 {
		sign, value = "-", -value
	}
	digits := fmt.Sprintf("%d", value)
	for index := len(digits) - 3; index > 0; index -= 3 {
		digits = digits[:index] + "," + digits[index:]
	}
	return sign + digits
}

// ---------------------------------------------------------------------------
// keys
// ---------------------------------------------------------------------------

func runAnalyticsKeysGroup(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "list":
			return analyticsKeysListFlow(args[1:])
		case "create", "new":
			return analyticsKeysCreateFlow(args[1:])
		case "revoke", "delete":
			return analyticsKeysRevokeFlow(args[1:])
		}
	}
	return analyticsKeysListFlow(args)
}

func analyticsKeysListFlow(args []string) error {
	const usage = "hubfly analytics keys [list] <site> [--project <p>] [--json]"
	var asJSON *bool
	token, site, _, rest, err := analyticsSiteTarget("keys list", usage, args, func(fs *flag.FlagSet) { asJSON = fs.Bool("json", false, "") })
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return fmt.Errorf("unexpected argument: %s\nusage: %s", rest[0], usage)
	}
	keys, err := fetchAnalyticsIngestKeys(token, site.ProjectID, site.ID)
	if err != nil {
		return analyticsAPIError(err)
	}
	if *asJSON {
		return rtPrintJSON(keys)
	}
	if len(keys) == 0 {
		_, _ = fmt.Fprintf(rtOut, "No ingest keys for %s. Create one with: hubfly analytics keys create %s <name>\n", site.Name, site.ID)
		return nil
	}
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAME\tPREFIX\tSTATUS\tLAST USED\tCREATED\tID")
	for _, key := range keys {
		status := "active"
		if key.RevokedAt != nil {
			status = "revoked"
		}
		lastUsed := "never"
		if key.LastUsedAt != nil {
			lastUsed = storageTime(*key.LastUsedAt)
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s…\t%s\t%s\t%s\t%s\n", key.Name, key.Prefix, status, lastUsed, storageTime(key.CreatedAt), key.ID)
	}
	return tw.Flush()
}

func analyticsKeysCreateFlow(args []string) error {
	const usage = "hubfly analytics keys create <site> <name> [--project <p>] [--json]"
	var asJSON *bool
	token, site, _, rest, err := analyticsSiteTarget("keys create", usage, args, func(fs *flag.FlagSet) { asJSON = fs.Bool("json", false, "") })
	if err != nil {
		return err
	}
	if len(rest) != 1 || strings.TrimSpace(rest[0]) == "" {
		return fmt.Errorf("usage: %s", usage)
	}
	created, err := createAnalyticsIngestKey(token, site.ProjectID, site.ID, strings.TrimSpace(rest[0]))
	if err != nil {
		return analyticsAPIError(err)
	}
	if *asJSON {
		return rtPrintJSON(created)
	}
	_, _ = fmt.Fprintf(rtOut, "Created ingest key %s for %s. It is shown once; store it now:\n\n%s\n\n", created.IngestKey.Name, site.Name, created.Key)
	_, _ = fmt.Fprintf(rtOut, "Send events with: POST %s and the header Authorization: Bearer <key>\n", valueOrDash(site.Install.EventsAPIURL))
	if !created.Synced {
		_, _ = fmt.Fprintln(rtErr, "The key reaches the analytics region within a few minutes.")
	}
	return nil
}

func analyticsKeysRevokeFlow(args []string) error {
	const usage = "hubfly analytics keys revoke <site> <keyId> [--yes] [--project <p>]"
	var yes *bool
	token, site, _, rest, err := analyticsSiteTarget("keys revoke", usage, args, func(fs *flag.FlagSet) { yes = storageYesFlag(fs) })
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return fmt.Errorf("usage: %s", usage)
	}
	keys, err := fetchAnalyticsIngestKeys(token, site.ProjectID, site.ID)
	if err != nil {
		return analyticsAPIError(err)
	}
	var key *analyticsIngestKey
	for index := range keys {
		if keys[index].ID == rest[0] || strings.EqualFold(keys[index].Name, rest[0]) || keys[index].Prefix == rest[0] {
			key = &keys[index]
			break
		}
	}
	if key == nil {
		return fmt.Errorf("ingest key '%s' not found (list them with: hubfly analytics keys %s)", rest[0], site.ID)
	}
	confirmed, err := storageConfirm(*yes, fmt.Sprintf("Revoke ingest key %s (%s…)? Servers using it stop sending events.", key.Name, key.Prefix), "revoke the key")
	if err != nil || !confirmed {
		return err
	}
	if _, err := revokeAnalyticsIngestKey(token, site.ProjectID, site.ID, key.ID); err != nil {
		return analyticsAPIError(err)
	}
	_, _ = fmt.Fprintf(rtOut, "Revoked ingest key %s.\n", key.Name)
	return nil
}

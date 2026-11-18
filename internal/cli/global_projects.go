package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
)

// Global projects (`gpj_<uuid>`): one project across every region. Hubfly
// space keeps a hidden regional "child" project per (region, type) and
// creates it on demand (POST .../children/ensure); resources use the child's
// id with the usual product endpoints. Children never appear in
// GET /api/v1/projects.

const globalProjectIDPrefix = "gpj_"

func isGlobalProjectID(value string) bool {
	return strings.HasPrefix(strings.TrimSpace(value), globalProjectIDPrefix)
}

type globalProjectRegion struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Location string `json:"location,omitempty"`
}

type globalProjectCounts struct {
	Cells        int `json:"cells"`
	RunningCells int `json:"runningCells"`
	Volumes      int `json:"volumes"`
	Boxes        int `json:"boxes"`
	Databases    int `json:"databases"`
	Realtime     int `json:"realtime"`
	Streams      int `json:"streams"`
	Storage      int `json:"storage"`
}

type globalProjectBilling struct {
	HourlyCost    string `json:"hourlyCost"`
	MonthlyCost   string `json:"monthlyCost"`
	SpentAmount   string `json:"spentAmount"`
	TotalConsumed string `json:"totalConsumed"`
}

type globalProjectSummary struct {
	ID                 string                `json:"id"`
	Kind               string                `json:"kind"`
	Name               string                `json:"name"`
	Status             string                `json:"status"`
	Ownership          string                `json:"ownership"`
	OrganizationID     string                `json:"organizationId"`
	OrganizationName   string                `json:"organizationName"`
	Budget             string                `json:"budget"`
	Regions            []globalProjectRegion `json:"regions"`
	HasManagedServices bool                  `json:"hasManagedServices"`
	Counts             globalProjectCounts   `json:"counts"`
	Billing            *globalProjectBilling `json:"billing"`
	CreatedAt          string                `json:"createdAt"`
	UpdatedAt          string                `json:"updatedAt"`
	raw                json.RawMessage
}

func (s *globalProjectSummary) UnmarshalJSON(data []byte) error {
	type plain globalProjectSummary
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*s = globalProjectSummary(decoded)
	s.raw = append(json.RawMessage(nil), data...)
	return nil
}

// MarshalJSON passes the server's summary through unchanged, so --json
// output gains new Dashboard fields without a CLI release.
func (s globalProjectSummary) MarshalJSON() ([]byte, error) {
	if len(s.raw) > 0 {
		return s.raw, nil
	}
	type plain globalProjectSummary
	return json.Marshal(plain(s))
}

type globalProjectList struct {
	Enabled bool                   `json:"enabled"`
	Items   []globalProjectSummary `json:"items"`
}

type globalProjectChild struct {
	ProjectID string `json:"projectId"`
	Type      string `json:"type"`
	RegionID  string `json:"regionId"`
	Created   bool   `json:"created"`
}

func globalProjectPath(globalProjectID string) string {
	return apiHost + "/api/v1/global-projects/" + url.PathEscape(strings.TrimSpace(globalProjectID))
}

// fetchGlobalProjects lists global projects. Servers without the feature (an
// older Dashboard, the gate off: 404 GLOBAL_PROJECTS_UNAVAILABLE) and
// scoped tokens (403 GLOBAL_PROJECT_SCOPED_TOKEN) yield an empty list.
func fetchGlobalProjects(token, orgID string) ([]globalProjectSummary, error) {
	requestURL := apiHost + "/api/v1/global-projects"
	if orgID != "" {
		requestURL += "?" + url.Values{"organizationId": {orgID}}.Encode()
	}
	var payload globalProjectList
	if err := doJSONRequest(http.MethodGet, requestURL, token, nil, &payload); err != nil {
		if globalProjectsUnavailable(err) {
			return nil, nil
		}
		return nil, err
	}
	return payload.Items, nil
}

func globalProjectsUnavailable(err error) bool {
	var apiErr *apiError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.Status {
	case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return true
	case http.StatusForbidden:
		return apiErr.Code == "GLOBAL_PROJECT_SCOPED_TOKEN"
	}
	return false
}

func fetchGlobalProject(token, globalProjectID string) (globalProjectSummary, error) {
	var payload globalProjectSummary
	err := doJSONRequest(http.MethodGet, globalProjectPath(globalProjectID), token, nil, &payload)
	return payload, globalProjectAPIError(err)
}

func ensureGlobalProjectChild(token, globalProjectID, childType, regionID string) (globalProjectChild, error) {
	body := map[string]any{"type": childType}
	if regionID != "" {
		body["regionId"] = regionID
	}
	var payload globalProjectChild
	err := doJSONRequest(http.MethodPost, globalProjectPath(globalProjectID)+"/children/ensure", token, body, &payload)
	return payload, globalProjectAPIError(err)
}

// findGlobalProjectChild looks up an existing child without creating one
// (for dry runs): the canvas graph lists every child.
func findGlobalProjectChild(token, globalProjectID, childType, regionID string) (globalProjectChild, bool, error) {
	var graph struct {
		Children []globalProjectChild `json:"children"`
	}
	if err := doJSONRequest(http.MethodGet, globalProjectPath(globalProjectID)+"/graph", token, nil, &graph); err != nil {
		return globalProjectChild{}, false, globalProjectAPIError(err)
	}
	for _, child := range graph.Children {
		if child.Type == childType && child.RegionID == regionID {
			return child, true, nil
		}
	}
	return globalProjectChild{}, false, nil
}

// globalProjectAPIError adds a hint to the global-project refusals a CLI user
// can act on.
func globalProjectAPIError(err error) error {
	var apiErr *apiError
	if !errors.As(err, &apiErr) {
		return err
	}
	switch apiErr.Code {
	case "GLOBAL_PROJECT_SCOPED_TOKEN":
		return fmt.Errorf("%w\nGlobal projects need a full account token: run `hubfly login` instead of using a scoped or machine token", err)
	case "GLOBAL_PROJECTS_UNAVAILABLE":
		return fmt.Errorf("%w\nGlobal projects are not available for this account yet", err)
	}
	return err
}

// projectChildOf returns the global project a project id belongs to and the
// project's region ("" for classic projects, or when the project can't be
// read).
func projectChildOf(token, projectID string) (string, string) {
	var payload struct {
		GlobalProjectID *string `json:"globalProjectId"`
		RegionID        string  `json:"regionId"`
		Region          struct {
			ID string `json:"id"`
		} `json:"region"`
	}
	if err := doJSONRequest(http.MethodGet, apiHost+"/api/v1/projects/"+url.PathEscape(strings.TrimSpace(projectID)), token, nil, &payload); err != nil {
		return "", ""
	}
	if payload.GlobalProjectID == nil {
		return "", ""
	}
	return strings.TrimSpace(*payload.GlobalProjectID), firstNonEmpty(payload.Region.ID, payload.RegionID)
}

// resolveGlobalCellTarget returns the Cell child of a global project in the
// region matching regionQuery (an id, name or location), creating it unless
// dryRun is set. A dry run with no child yet returns errPlanNeedsNewProject.
func resolveGlobalCellTarget(token, globalProjectID, regionQuery string, dryRun bool) (globalProjectChild, error) {
	regionQuery = strings.TrimSpace(regionQuery)
	if regionQuery == "" {
		return globalProjectChild{}, fmt.Errorf(
			"%s is a global project: choose the region to deploy to with --region <region> (or set project.region in %s)",
			globalProjectID, deployConfigFileName,
		)
	}
	regionID := regionQuery
	if regions, err := fetchRegions(token); err == nil {
		for _, candidate := range regions {
			if regionMatchesQuery(candidate, regionQuery) {
				regionID = candidate.ID
				break
			}
		}
	}
	if dryRun {
		child, found, err := findGlobalProjectChild(token, globalProjectID, "cell", regionID)
		if err != nil {
			return globalProjectChild{}, err
		}
		if !found {
			return globalProjectChild{}, errPlanNeedsNewProject
		}
		return child, nil
	}
	child, err := ensureGlobalProjectChild(token, globalProjectID, "cell", regionID)
	if err != nil {
		return globalProjectChild{}, err
	}
	if strings.TrimSpace(child.ProjectID) == "" {
		return globalProjectChild{}, fmt.Errorf("global project %s returned no Cell project for region %s", globalProjectID, regionID)
	}
	return child, nil
}

// globalDeployTarget reports whether a deploy targets a global project by
// its gpj_ id (`--project gpj_…`, or project.id = "gpj_…" in the deploy
// config) and the region to use: --region, else project.region when the
// config is already bound to that global project.
func globalDeployTarget(token string, cfg *deployConfigFile, opts deployOptions) (globalID, region string, ok bool) {
	requested := strings.TrimSpace(opts.Project)
	bound := strings.TrimSpace(cfg.Project.ID)
	region = strings.TrimSpace(opts.Region)
	switch {
	case isGlobalProjectID(requested):
		globalID = requested
		if region == "" && bound != "" {
			sameProject := bound == requested
			if !sameProject && !isGlobalProjectID(bound) {
				parent, _ := projectChildOf(token, bound)
				sameProject = parent == requested
			}
			if sameProject {
				region = strings.TrimSpace(cfg.Project.Region)
			}
		}
		return globalID, region, true
	case requested == "" && isGlobalProjectID(bound):
		if region == "" {
			region = strings.TrimSpace(cfg.Project.Region)
		}
		return bound, region, true
	}
	return "", "", false
}

// bindGlobalChildDeployProject handles a project id the project list doesn't
// show: when it is a global project's hidden Cell child (deploy saves the
// child id in the config), the deploy goes to that global project, in the
// --region region, else fallbackRegion, else the child's own region.
// It reports false for anything else.
func bindGlobalChildDeployProject(token string, cfg *deployConfigFile, opts deployOptions, projectID, fallbackRegion string) (bool, error) {
	globalID, childRegion := projectChildOf(token, projectID)
	if globalID == "" {
		return false, nil
	}
	region := firstNonEmpty(strings.TrimSpace(opts.Region), strings.TrimSpace(fallbackRegion), childRegion)
	return true, bindGlobalDeployProject(token, cfg, opts, globalID, region)
}

// bindGlobalDeployProject points the deploy config at the Cell child of a
// global project in one region, creating that child on first use (dry runs
// only look it up). The child id is what the deploy endpoints take.
func bindGlobalDeployProject(token string, cfg *deployConfigFile, opts deployOptions, globalID, region string) error {
	summary, err := fetchGlobalProject(token, globalID)
	if err != nil {
		return err
	}
	child, err := resolveGlobalCellTarget(token, globalID, region, opts.PlanOnly)
	if err != nil {
		return err
	}
	bound := strings.TrimSpace(cfg.Project.ID)
	if bound != "" && bound != globalID && bound != child.ProjectID && strings.TrimSpace(cfg.Container.ID) != "" {
		_, _ = fmt.Fprintf(os.Stderr, "Deploying %s to region %s: the bound container %s belongs to another project, so this deploy creates a new container.\n",
			valueOrDash(summary.Name), child.RegionID, cfg.Container.ID)
		cfg.Container.ID = ""
	}
	cfg.Project.ID = child.ProjectID
	if summary.Name != "" {
		cfg.Project.Name = summary.Name
	}
	cfg.Project.Region = child.RegionID
	if child.Created {
		_, _ = fmt.Fprintf(os.Stderr, "Added region %s to global project %s.\n", child.RegionID, valueOrDash(summary.Name))
	}
	return nil
}

// ---------------------------------------------------------------------------
// hubfly project list / get
// ---------------------------------------------------------------------------

const projectListUsage = "hubfly project list [--org <id|slug>] [--json]"

// projectListEntry is one `hubfly project list --json` item: the classic
// project fields plus kind ("classic" or "global") and regions. Global
// entries are the Dashboard's GlobalProjectSummary, which carries both.
type projectListEntry struct {
	project
	Kind    string                `json:"kind"`
	Regions []globalProjectRegion `json:"regions"`
}

func classicProjectListEntry(p project) projectListEntry {
	regions := []globalProjectRegion{}
	if p.Region.ID != "" {
		regions = append(regions, globalProjectRegion{ID: p.Region.ID, Name: p.Region.Name, Location: p.Region.Location})
	}
	return projectListEntry{project: p, Kind: "classic", Regions: regions}
}

func projectListFlow(args []string) error {
	fs := newProjectFlags("list")
	org := fs.String("org", "", "")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, projectListUsage)
	if err != nil {
		return err
	}
	if len(positionals) != 0 {
		return fmt.Errorf("usage: %s", projectListUsage)
	}
	if !*asJSON && rtInteractive() {
		return projectsFlow(strings.TrimSpace(*org))
	}
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}
	orgID, err := resolveOrgID(token, strings.TrimSpace(*org))
	if err != nil {
		return err
	}
	classic, err := fetchProjectsWithOrg(token, orgID)
	if err != nil {
		return err
	}
	globals, err := fetchGlobalProjects(token, orgID)
	if err != nil {
		_, _ = fmt.Fprintf(rtErr, "Warning: could not list global projects: %v\n", err)
		globals = nil
	}

	if *asJSON {
		items := make([]any, 0, len(classic)+len(globals))
		for _, p := range classic {
			items = append(items, classicProjectListEntry(p))
		}
		for _, g := range globals {
			items = append(items, g)
		}
		return rtPrintJSON(items)
	}

	if len(classic) == 0 && len(globals) == 0 {
		_, _ = fmt.Fprintln(rtOut, "No projects found.")
		return nil
	}
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "KIND\tNAME\tREGIONS\tSTATUS\tSPENT\tID")
	for _, p := range classic {
		_, _ = fmt.Fprintf(tw, "classic\t%s\t%s\t%s\t%s\t%s\n", p.Name, valueOrDash(p.Region.Name), valueOrDash(p.Status), valueOrDash(p.Spent), p.ID)
	}
	for _, g := range globals {
		spent := ""
		if g.Billing != nil {
			spent = g.Billing.SpentAmount
		}
		_, _ = fmt.Fprintf(tw, "global\t%s\t%s\t%s\t%s\t%s\n", g.Name, globalProjectRegionNames(g), valueOrDash(g.Status), valueOrDash(spent), g.ID)
	}
	return tw.Flush()
}

func globalProjectRegionNames(g globalProjectSummary) string {
	names := make([]string, 0, len(g.Regions)+1)
	for _, r := range g.Regions {
		names = append(names, valueOrDash(firstNonEmpty(r.Name, r.ID)))
	}
	if g.HasManagedServices {
		names = append(names, "managed")
	}
	if len(names) == 0 {
		return "-"
	}
	return strings.Join(names, ", ")
}

const projectGetUsage = "hubfly project get <project> [--json]"

func projectGetFlow(args []string) error {
	fs := newProjectFlags("get")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, projectGetUsage)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return fmt.Errorf("usage: %s", projectGetUsage)
	}
	ref := strings.TrimSpace(positionals[0])
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}

	if !isGlobalProjectID(ref) {
		classic, classicErr := resolveMoveProject(token, ref)
		if classicErr == nil {
			if *asJSON {
				return rtPrintJSON(classicProjectListEntry(classic))
			}
			return printClassicProjectSummary(classic)
		}
		// Not a classic project: try global projects by name.
		globals, err := fetchGlobalProjects(token, "")
		if err != nil {
			return classicErr
		}
		var matches []globalProjectSummary
		for _, g := range globals {
			if strings.EqualFold(g.Name, ref) {
				matches = append(matches, g)
			}
		}
		switch len(matches) {
		case 0:
			return classicErr
		case 1:
			ref = matches[0].ID
		default:
			return fmt.Errorf("%d global projects are named %q: use the project id (gpj_…)", len(matches), ref)
		}
	}

	summary, err := fetchGlobalProject(token, ref)
	if err != nil {
		return err
	}
	if *asJSON {
		return rtPrintJSON(summary)
	}
	return printGlobalProjectSummary(summary)
}

func printClassicProjectSummary(p project) error {
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "Name:\t%s\n", p.Name)
	_, _ = fmt.Fprintf(tw, "ID:\t%s\n", p.ID)
	_, _ = fmt.Fprintf(tw, "Kind:\tclassic (%s)\n", valueOrDash(p.Type))
	_, _ = fmt.Fprintf(tw, "Region:\t%s\n", valueOrDash(firstNonEmpty(p.Region.Name, p.Region.ID)))
	_, _ = fmt.Fprintf(tw, "Status:\t%s\n", valueOrDash(p.Status))
	_, _ = fmt.Fprintf(tw, "Role:\t%s\n", valueOrDash(p.Role))
	_, _ = fmt.Fprintf(tw, "Spent:\t%s\n", valueOrDash(p.Spent))
	_, _ = fmt.Fprintf(tw, "Monthly:\t%s\n", valueOrDash(p.Monthly))
	return tw.Flush()
}

func printGlobalProjectSummary(g globalProjectSummary) error {
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "Name:\t%s\n", g.Name)
	_, _ = fmt.Fprintf(tw, "ID:\t%s\n", g.ID)
	_, _ = fmt.Fprintln(tw, "Kind:\tglobal")
	_, _ = fmt.Fprintf(tw, "Status:\t%s\n", valueOrDash(g.Status))
	owner := valueOrDash(g.Ownership)
	if g.OrganizationName != "" {
		owner += " (" + g.OrganizationName + ")"
	}
	_, _ = fmt.Fprintf(tw, "Owner:\t%s\n", owner)
	_, _ = fmt.Fprintf(tw, "Regions:\t%s\n", globalProjectRegionNames(g))
	c := g.Counts
	_, _ = fmt.Fprintf(tw, "Resources:\t%d cells (%d running), %d volumes, %d boxes, %d storage, %d databases, %d realtime, %d streams\n",
		c.Cells, c.RunningCells, c.Volumes, c.Boxes, c.Storage, c.Databases, c.Realtime, c.Streams)
	if g.Billing != nil {
		_, _ = fmt.Fprintf(tw, "Cost:\t$%s/hour, ~$%s/month\n", g.Billing.HourlyCost, g.Billing.MonthlyCost)
		_, _ = fmt.Fprintf(tw, "Spent:\t$%s this month, $%s total\n", g.Billing.SpentAmount, g.Billing.TotalConsumed)
		_, _ = fmt.Fprintf(tw, "Monthly Cap:\t%s\n", globalProjectCapText(g.Budget))
	}
	_, _ = fmt.Fprintf(tw, "Created:\t%s\n", valueOrDash(g.CreatedAt))
	if err := tw.Flush(); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(rtOut, "\nDeploy a Cell: hubfly deploy --project %s --region <region>\n", g.ID)
	return nil
}

func globalProjectCapText(budget string) string {
	budget = strings.TrimSpace(budget)
	if budget == "" || strings.HasPrefix(budget, "-") || budget == "0" || budget == "0.00" {
		return "unlimited"
	}
	return "$" + budget
}

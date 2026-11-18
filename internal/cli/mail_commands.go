package cli

import (
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"
)

// `hubfly mail`: Mail domains, sending plans and test sends against the
// Dashboard API under /api/v1/projects/{projectId}/mail.

const mailUsageText = `Mail (domains, sending plans and test sends):
  hubfly mail domains [list] [--project <p>] [--json]
  hubfly mail domains status <domain> [--check] [--project <p>] [--json]
  hubfly mail plan [show] <domain> [--project <p>] [--json]
  hubfly mail plan change <domain> --plan free|starter|growth|scale [--at-renewal|--resume] [--yes] [--project <p>] [--json]
  hubfly mail send-test --from <address> --to <address>[,<address>] [--subject <text>] [--text <body>] [--yes] [--project <p>] [--json]

<project> is a Mail project id or name. It may be omitted when you have exactly one Mail project,
or set HUBFLY_MAIL_PROJECT. <domain> is a domain name or id.

plan change buys a higher plan now (charged from the account balance), or with --at-renewal
switches at the end of the current period (use it to downgrade); --resume turns renewal back on.`

const mailProjectEnv = "HUBFLY_MAIL_PROJECT"

var mailPlanNames = []string{"free", "starter", "growth", "scale"}

type mailDomain struct {
	ID         string          `json:"id"`
	Domain     string          `json:"domain"`
	State      string          `json:"state"`
	MailState  *string         `json:"mailState"`
	DNSRecords *mailDNSRecords `json:"dnsRecords"`
	VerifiedAt *string         `json:"verifiedAt"`
	ChargedAt  *string         `json:"chargedAt"`
}

type mailDNSRecords struct {
	MX     string          `json:"mx,omitempty"`
	SPF    string          `json:"spf,omitempty"`
	DKIM   string          `json:"dkim,omitempty"`
	DMARC  string          `json:"dmarc,omitempty"`
	Checks map[string]bool `json:"checks,omitempty"`
}

type mailPlanOption struct {
	Plan          string `json:"plan"`
	Allowance     int64  `json:"allowance"`
	PriceMicroUSD string `json:"priceMicroUsd"`
	PerMinute     int64  `json:"perMinute"`
	PerHour       int64  `json:"perHour"`
	PerDay        int64  `json:"perDay"`
}

type mailPlanView struct {
	Plans             []mailPlanOption `json:"plans"`
	Enabled           bool             `json:"enabled"`
	CanManageBilling  bool             `json:"canManageBilling"`
	Plan              string           `json:"plan"`
	NextPlan          *string          `json:"nextPlan"`
	AutoRenew         bool             `json:"autoRenew"`
	Status            string           `json:"status"`
	LastError         *string          `json:"lastError"`
	PeriodStart       string           `json:"periodStart"`
	PeriodEnd         string           `json:"periodEnd"`
	Used              int64            `json:"used"`
	Remaining         int64            `json:"remaining"`
	Allowance         int64            `json:"allowance"`
	UsageAvailable    bool             `json:"usageAvailable"`
	Transition        bool             `json:"transition"`
	RenewalPriceMicro string           `json:"renewalPriceMicroUsd"`
	BalanceMicroUSD   *string          `json:"balanceMicroUsd"`
	RateUsage         *struct {
		Minute int64 `json:"minute"`
		Hour   int64 `json:"hour"`
		Day    int64 `json:"day"`
	} `json:"rateUsage,omitempty"`
	Readiness *struct {
		Ready  bool `json:"ready"`
		Checks []struct {
			ID     string `json:"id"`
			OK     bool   `json:"ok"`
			Detail string `json:"detail"`
		} `json:"checks"`
	} `json:"readiness,omitempty"`
	BillingOwnerType string `json:"billingOwnerType,omitempty"`
	Activation       *struct {
		Attempts      int     `json:"attempts"`
		LastError     *string `json:"lastError"`
		NextAttemptAt *string `json:"nextAttemptAt"`
	} `json:"activation,omitempty"`
}

type mailSubmission struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type mailTarget struct {
	token   string
	project project
}

func mailAPIPath(projectID string) string {
	return apiHost + "/api/v1/projects/" + url.PathEscape(projectID) + "/mail"
}

func fetchMailDomains(token, projectID string) ([]mailDomain, error) {
	var payload []mailDomain
	err := doJSONRequest(http.MethodGet, mailAPIPath(projectID)+"/domains", token, nil, &payload)
	return payload, err
}

func checkMailDomainDNS(token, projectID, domainID string) (map[string]bool, error) {
	var payload struct {
		Checks map[string]bool `json:"checks"`
	}
	err := doJSONRequest(http.MethodPost, mailAPIPath(projectID)+"/domains/"+url.PathEscape(domainID)+"/dns/verify", token, map[string]any{"projectId": projectID, "domainId": domainID}, &payload)
	return payload.Checks, err
}

func fetchMailPlan(token, projectID, domainID string) (mailPlanView, error) {
	var payload mailPlanView
	err := doJSONRequest(http.MethodGet, mailAPIPath(projectID)+"/domains/"+url.PathEscape(domainID)+"/sending-plan", token, nil, &payload)
	return payload, err
}

func changeMailPlan(token, projectID, domainID, plan, action, key string) (mailSubmission, error) {
	var payload mailSubmission
	body := map[string]any{"projectId": projectID, "domainId": domainID, "plan": plan, "action": action, "idempotencyKey": key}
	err := doJSONRequestIdempotent(http.MethodPost, mailAPIPath(projectID)+"/domains/"+url.PathEscape(domainID)+"/sending-plan/change", token, body, &payload, key)
	return payload, err
}

func submitMailTest(token, projectID string, body map[string]any, key string) (mailSubmission, error) {
	var payload mailSubmission
	err := doJSONRequestIdempotent(http.MethodPost, mailAPIPath(projectID)+"/send", token, body, &payload, key)
	return payload, err
}

// ---------------------------------------------------------------------------
// dispatch and target
// ---------------------------------------------------------------------------

func runMailGroup(args []string) error {
	if len(args) == 0 || isHelpArg(args[0]) {
		_, _ = fmt.Fprintln(rtOut, mailUsageText)
		return nil
	}
	switch args[0] {
	case "domains", "domain":
		rest := args[1:]
		if len(rest) > 0 {
			switch rest[0] {
			case "list", "ls":
				return mailDomainsListFlow(rest[1:])
			case "status", "show", "info":
				return mailDomainStatusFlow(rest[1:])
			}
		}
		return mailDomainsListFlow(rest)
	case "plan", "plans", "sending-plan":
		rest := args[1:]
		if len(rest) > 0 {
			switch rest[0] {
			case "show":
				return mailPlanShowFlow(rest[1:])
			case "change", "set", "buy":
				return mailPlanChangeFlow(rest[1:])
			}
		}
		return mailPlanShowFlow(rest)
	case "send-test", "test":
		return mailSendTestFlow(args[1:])
	}
	known := []string{"domains", "plan", "send-test"}
	return fmt.Errorf("unknown mail command %q%s\n\n%s", args[0], didYouMean(args[0], known), mailUsageText)
}

func isHelpArg(arg string) bool {
	return arg == "help" || arg == "-h" || arg == "--help"
}

// resolveMailTarget finds the Mail project: --project, HUBFLY_MAIL_PROJECT,
// or the only Mail project of the account.
func resolveMailTarget(ref string) (mailTarget, error) {
	token, err := ensureAuth(true)
	if err != nil {
		return mailTarget{}, err
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		ref = strings.TrimSpace(os.Getenv(mailProjectEnv))
	}
	projects, err := fetchAllProjectsAnyType(token, "")
	if err != nil {
		return mailTarget{}, err
	}
	return pickMailProject(token, projects, ref)
}

func pickMailProject(token string, projects []project, ref string) (mailTarget, error) {
	mail := []project{}
	for _, candidate := range projects {
		if ref != "" && (candidate.ID == ref || strings.EqualFold(candidate.Name, ref)) {
			return mailTarget{token: token, project: candidate}, nil
		}
		if strings.EqualFold(candidate.Type, "mail") {
			mail = append(mail, candidate)
		}
	}
	if ref != "" {
		return mailTarget{}, fmt.Errorf("project '%s' not found; list your projects with: hubfly projects", ref)
	}
	switch len(mail) {
	case 0:
		return mailTarget{}, errors.New("no Mail project found; create one in the Dashboard (Mail), then run this again")
	case 1:
		return mailTarget{token: token, project: mail[0]}, nil
	}
	names := make([]string, 0, len(mail))
	for _, candidate := range mail {
		names = append(names, candidate.Name)
	}
	return mailTarget{}, fmt.Errorf("you have %d Mail projects (%s); choose one with --project <name> or %s", len(mail), strings.Join(names, ", "), mailProjectEnv)
}

// findMailDomain matches a domain by name (case-insensitive) or id.
func findMailDomain(domains []mailDomain, ref string) (mailDomain, error) {
	ref = strings.TrimSuffix(strings.TrimSpace(ref), ".")
	for _, domain := range domains {
		if domain.ID == ref || strings.EqualFold(domain.Domain, ref) {
			return domain, nil
		}
	}
	names := make([]string, 0, len(domains))
	for _, domain := range domains {
		names = append(names, domain.Domain)
	}
	if len(names) == 0 {
		return mailDomain{}, fmt.Errorf("mail domain '%s' not found: this project has no Mail domains yet; add one in the Dashboard", ref)
	}
	return mailDomain{}, fmt.Errorf("mail domain '%s' not found%s (domains: %s)", ref, didYouMean(ref, names), strings.Join(names, ", "))
}

func mailResolveDomain(target mailTarget, ref string) (mailDomain, error) {
	domains, err := fetchMailDomains(target.token, target.project.ID)
	if err != nil {
		return mailDomain{}, mailAPIError(err)
	}
	return findMailDomain(domains, ref)
}

// mailAPIError adds a next step to common API failures.
func mailAPIError(err error) error {
	var apiErr *apiError
	if !errors.As(err, &apiErr) {
		return err
	}
	switch {
	case apiErr.Code == "MAIL_PLANS_DISABLED":
		return fmt.Errorf("%w\nSending plan purchases are not open yet in this environment.", err)
	case apiErr.Status == http.StatusPaymentRequired || strings.Contains(strings.ToUpper(apiErr.Code), "BALANCE"):
		return fmt.Errorf("%w\nAdd funds in the Dashboard (Billing), then run this again.", err)
	case apiErr.Status == http.StatusForbidden:
		return fmt.Errorf("%w\nYou need edit access to the project (and billing access for plan changes).", err)
	case apiErr.Status == http.StatusServiceUnavailable || apiErr.Status == http.StatusBadGateway:
		return fmt.Errorf("%w\nThe Mail service is unreachable right now; try again shortly.", err)
	}
	return err
}

// ---------------------------------------------------------------------------
// domains
// ---------------------------------------------------------------------------

func mailDomainState(domain mailDomain) string {
	state := domain.State
	if domain.MailState != nil && *domain.MailState != "" && *domain.MailState != domain.State {
		state += " (mail: " + *domain.MailState + ")"
	}
	return state
}

func mailDomainsListFlow(args []string) error {
	const usage = "hubfly mail domains [list] [--project <p>] [--json]"
	fs := newRealtimeFlags("mail domains")
	projectRef := fs.String("project", "", "")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 0 {
		return fmt.Errorf("usage: %s", usage)
	}
	target, err := resolveMailTarget(*projectRef)
	if err != nil {
		return err
	}
	domains, err := fetchMailDomains(target.token, target.project.ID)
	if err != nil {
		return mailAPIError(err)
	}
	if *asJSON {
		return rtPrintJSON(domains)
	}
	if len(domains) == 0 {
		_, _ = fmt.Fprintf(rtOut, "No Mail domains in %s yet; add one in the Dashboard (Mail > Domains).\n", target.project.Name)
		return nil
	}
	tw := tabwriter.NewWriter(rtOut, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "DOMAIN\tSTATE\tDNS\tVERIFIED\tID")
	for _, domain := range domains {
		verified := "-"
		if domain.VerifiedAt != nil && *domain.VerifiedAt != "" {
			verified = mailShortTime(*domain.VerifiedAt)
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", domain.Domain, mailDomainState(domain), mailDNSSummary(domain.DNSRecords), verified, domain.ID)
	}
	return tw.Flush()
}

// mailDNSSummary is "4/4 ok" from the last DNS check, "-" before one.
func mailDNSSummary(records *mailDNSRecords) string {
	if records == nil || len(records.Checks) == 0 {
		return "-"
	}
	ok := 0
	for _, passed := range records.Checks {
		if passed {
			ok++
		}
	}
	return fmt.Sprintf("%d/%d ok", ok, len(records.Checks))
}

func mailDomainStatusFlow(args []string) error {
	const usage = "hubfly mail domains status <domain> [--check] [--project <p>] [--json]"
	fs := newRealtimeFlags("mail domains status")
	projectRef := fs.String("project", "", "")
	check := fs.Bool("check", false, "")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return fmt.Errorf("usage: %s", usage)
	}
	target, err := resolveMailTarget(*projectRef)
	if err != nil {
		return err
	}
	domain, err := mailResolveDomain(target, positionals[0])
	if err != nil {
		return err
	}
	if *check {
		checks, checkErr := checkMailDomainDNS(target.token, target.project.ID, domain.ID)
		if checkErr != nil {
			return mailAPIError(checkErr)
		}
		if domain.DNSRecords == nil {
			domain.DNSRecords = &mailDNSRecords{}
		}
		domain.DNSRecords.Checks = checks
	}
	plan, planErr := fetchMailPlan(target.token, target.project.ID, domain.ID)
	if *asJSON {
		out := map[string]any{"domain": domain}
		if planErr == nil {
			out["sendingPlan"] = plan
		}
		return rtPrintJSON(out)
	}
	tw := tabwriter.NewWriter(rtOut, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "Domain:\t%s\n", domain.Domain)
	_, _ = fmt.Fprintf(tw, "ID:\t%s\n", domain.ID)
	_, _ = fmt.Fprintf(tw, "State:\t%s\n", mailDomainState(domain))
	if domain.VerifiedAt != nil && *domain.VerifiedAt != "" {
		_, _ = fmt.Fprintf(tw, "Verified:\t%s\n", mailShortTime(*domain.VerifiedAt))
	}
	if planErr == nil {
		_, _ = fmt.Fprintf(tw, "Sending plan:\t%s (%s)\n", mailPlanTitle(plan.Plan), plan.Status)
	}
	_ = tw.Flush()
	if records := domain.DNSRecords; records != nil {
		_, _ = fmt.Fprintln(rtOut, "\nDNS records:")
		dns := tabwriter.NewWriter(rtOut, 0, 4, 2, ' ', 0)
		for _, row := range [][2]string{{"mx", records.MX}, {"spf", records.SPF}, {"dkim", records.DKIM}, {"dmarc", records.DMARC}} {
			if row[1] == "" {
				continue
			}
			status := "not checked"
			if passed, ok := records.Checks[row[0]]; ok {
				status = map[bool]string{true: "ok", false: "MISSING"}[passed]
			}
			_, _ = fmt.Fprintf(dns, "  %s\t%s\t%s\n", strings.ToUpper(row[0]), status, row[1])
		}
		_ = dns.Flush()
		if !*check {
			_, _ = fmt.Fprintf(rtOut, "\nRe-check DNS now with: hubfly mail domains status %s --check\n", domain.Domain)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// sending plans
// ---------------------------------------------------------------------------

func mailPlanTitle(name string) string {
	if name == "" {
		return "-"
	}
	return strings.ToUpper(name[:1]) + name[1:]
}

// mailMicroUSD formats a micro-USD decimal string as dollars.
func mailMicroUSD(value string) string {
	amount, ok := new(big.Int).SetString(strings.TrimSpace(value), 10)
	if !ok {
		return value
	}
	dollars := new(big.Rat).SetFrac(amount, big.NewInt(1_000_000))
	return "$" + dollars.FloatString(2)
}

func mailShortTime(value string) string {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999-07", "2006-01-02 15:04:05.999999999-07:00"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC().Format("2006-01-02 15:04 UTC")
		}
	}
	return value
}

func mailFindPlan(view mailPlanView, name string) (mailPlanOption, bool) {
	for _, option := range view.Plans {
		if option.Plan == name {
			return option, true
		}
	}
	return mailPlanOption{}, false
}

func mailPlanShowFlow(args []string) error {
	const usage = "hubfly mail plan [show] <domain> [--project <p>] [--json]"
	fs := newRealtimeFlags("mail plan")
	projectRef := fs.String("project", "", "")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return fmt.Errorf("usage: %s", usage)
	}
	target, err := resolveMailTarget(*projectRef)
	if err != nil {
		return err
	}
	domain, err := mailResolveDomain(target, positionals[0])
	if err != nil {
		return err
	}
	view, err := fetchMailPlan(target.token, target.project.ID, domain.ID)
	if err != nil {
		return mailAPIError(err)
	}
	if *asJSON {
		return rtPrintJSON(view)
	}
	writeMailPlan(domain, view)
	return nil
}

func writeMailPlan(domain mailDomain, view mailPlanView) {
	tw := tabwriter.NewWriter(rtOut, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "Domain:\t%s\n", domain.Domain)
	_, _ = fmt.Fprintf(tw, "Plan:\t%s (%s)\n", mailPlanTitle(view.Plan), view.Status)
	if view.PeriodEnd != "" {
		_, _ = fmt.Fprintf(tw, "Period:\t%s to %s\n", mailShortTime(view.PeriodStart), mailShortTime(view.PeriodEnd))
	}
	var limits *mailPlanOption
	for i := range view.Plans {
		if view.Plans[i].Plan == view.Plan {
			limits = &view.Plans[i]
		}
	}
	switch {
	case view.RateUsage != nil && limits != nil:
		_, _ = fmt.Fprintf(tw, "Sent (recipients):\t%d/%d this minute · %d/%d this hour · %d/%d today\n",
			view.RateUsage.Minute, limits.PerMinute, view.RateUsage.Hour, limits.PerHour, view.RateUsage.Day, limits.PerDay)
	case limits != nil && view.Allowance == 0:
		_, _ = fmt.Fprintf(tw, "Sending limits:\t%d/minute · %d/hour · %d/day\n", limits.PerMinute, limits.PerHour, limits.PerDay)
	case view.UsageAvailable && view.Allowance > 0:
		_, _ = fmt.Fprintf(tw, "Sent this period:\t%d of %d (%d left)\n", view.Used, view.Allowance, view.Remaining)
	case view.Allowance > 0:
		_, _ = fmt.Fprintf(tw, "Allowance:\t%d messages per period\n", view.Allowance)
	}
	if view.NextPlan != nil && *view.NextPlan != "" {
		_, _ = fmt.Fprintf(tw, "At renewal:\tswitches to %s\n", mailPlanTitle(*view.NextPlan))
	} else if view.Plan != "free" {
		renew := "off"
		if view.AutoRenew {
			renew = "on, " + mailMicroUSD(view.RenewalPriceMicro)
		}
		_, _ = fmt.Fprintf(tw, "Renewal:\t%s\n", renew)
	}
	if view.BalanceMicroUSD != nil {
		_, _ = fmt.Fprintf(tw, "Account balance:\t%s\n", mailMicroUSD(*view.BalanceMicroUSD))
	}
	if view.LastError != nil && *view.LastError != "" {
		_, _ = fmt.Fprintf(tw, "Last error:\t%s\n", *view.LastError)
	}
	if view.Activation != nil {
		next := "-"
		if view.Activation.NextAttemptAt != nil {
			next = mailShortTime(*view.Activation.NextAttemptAt)
		}
		_, _ = fmt.Fprintf(tw, "Activation:\tretrying (attempt %d, next %s)\n", view.Activation.Attempts, next)
	}
	_ = tw.Flush()
	if view.Readiness != nil && !view.Readiness.Ready {
		_, _ = fmt.Fprintln(rtOut, "\nNot ready for paid plans yet:")
		for _, check := range view.Readiness.Checks {
			if !check.OK {
				_, _ = fmt.Fprintf(rtOut, "  - %s\n", check.Detail)
			}
		}
	}
	if len(view.Plans) > 0 {
		_, _ = fmt.Fprintln(rtOut, "\nPlans:")
		plans := tabwriter.NewWriter(rtOut, 0, 4, 2, ' ', 0)
		// Rate-only catalogs (migration 0059) have no monthly quota column.
		withQuota := false
		for _, option := range view.Plans {
			if option.Allowance > 0 {
				withQuota = true
			}
		}
		if withQuota {
			_, _ = fmt.Fprintln(plans, "  PLAN\tPRICE\tMESSAGES\tPER MIN/HOUR/DAY")
		} else {
			_, _ = fmt.Fprintln(plans, "  PLAN\tPRICE/MONTH\tRECIPIENTS PER MIN/HOUR/DAY")
		}
		for _, option := range view.Plans {
			marker := ""
			if option.Plan == view.Plan {
				marker = " (current)"
			}
			if withQuota {
				_, _ = fmt.Fprintf(plans, "  %s%s\t%s\t%d\t%d/%d/%d\n", mailPlanTitle(option.Plan), marker, mailMicroUSD(option.PriceMicroUSD), option.Allowance, option.PerMinute, option.PerHour, option.PerDay)
			} else {
				_, _ = fmt.Fprintf(plans, "  %s%s\t%s\t%d/%d/%d\n", mailPlanTitle(option.Plan), marker, mailMicroUSD(option.PriceMicroUSD), option.PerMinute, option.PerHour, option.PerDay)
			}
		}
		_ = plans.Flush()
	}
	if !view.Enabled {
		_, _ = fmt.Fprintln(rtOut, "\nPlan changes are not available for this domain right now.")
	} else if !view.CanManageBilling {
		_, _ = fmt.Fprintln(rtOut, "\nOnly the billing owner can change this plan.")
	} else {
		_, _ = fmt.Fprintf(rtOut, "\nChange it with: hubfly mail plan change %s --plan <name>\n", domain.Domain)
	}
}

// mailPlanAction picks the API action of a plan change.
func mailPlanAction(current, wanted string, atRenewal, resume bool) (string, error) {
	switch {
	case atRenewal && resume:
		return "", errors.New("choose one of --at-renewal and --resume")
	case resume:
		return "resume", nil
	case atRenewal || wanted == "free":
		return "schedule", nil
	case wanted == current:
		return "", fmt.Errorf("the domain is already on the %s plan; pass --resume to turn renewal back on", mailPlanTitle(wanted))
	}
	return "purchase", nil
}

func mailPlanChangeFlow(args []string) error {
	const usage = "hubfly mail plan change <domain> --plan free|starter|growth|scale [--at-renewal|--resume] [--yes] [--project <p>] [--json]"
	fs := newRealtimeFlags("mail plan change")
	projectRef := fs.String("project", "", "")
	planName := fs.String("plan", "", "")
	atRenewal := fs.Bool("at-renewal", false, "")
	resume := fs.Bool("resume", false, "")
	yes := fs.Bool("yes", false, "")
	fs.BoolVar(yes, "y", false, "")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return fmt.Errorf("usage: %s", usage)
	}
	wanted := strings.ToLower(strings.TrimSpace(*planName))
	if wanted == "" {
		return fmt.Errorf("--plan is required (one of %s)\nusage: %s", strings.Join(mailPlanNames, ", "), usage)
	}
	known := false
	for _, name := range mailPlanNames {
		known = known || name == wanted
	}
	if !known {
		return fmt.Errorf("invalid --plan %q (one of %s)%s", wanted, strings.Join(mailPlanNames, ", "), didYouMean(wanted, mailPlanNames))
	}
	target, err := resolveMailTarget(*projectRef)
	if err != nil {
		return err
	}
	domain, err := mailResolveDomain(target, positionals[0])
	if err != nil {
		return err
	}
	view, err := fetchMailPlan(target.token, target.project.ID, domain.ID)
	if err != nil {
		return mailAPIError(err)
	}
	if !view.Enabled {
		return fmt.Errorf("plan changes are not available for %s right now (status %s)", domain.Domain, view.Status)
	}
	if !view.CanManageBilling {
		return fmt.Errorf("only the billing owner (%s) can change the plan of %s", view.BillingOwnerType, domain.Domain)
	}
	action, err := mailPlanAction(view.Plan, wanted, *atRenewal, *resume)
	if err != nil {
		return err
	}
	option, _ := mailFindPlan(view, wanted)
	var question string
	switch action {
	case "purchase":
		question = fmt.Sprintf("Buy the %s plan for %s now for %s (charged from the account balance; %d messages per period)?", mailPlanTitle(wanted), domain.Domain, mailMicroUSD(option.PriceMicroUSD), option.Allowance)
	case "schedule":
		question = fmt.Sprintf("Switch %s to the %s plan at renewal (%s)?", domain.Domain, mailPlanTitle(wanted), mailShortTime(view.PeriodEnd))
	default:
		question = fmt.Sprintf("Turn renewal of the %s plan for %s back on?", mailPlanTitle(wanted), domain.Domain)
	}
	confirmed, err := dbdConfirm(*yes, question, "change the sending plan")
	if err != nil {
		return err
	}
	if !confirmed {
		_, _ = fmt.Fprintln(rtOut, "Cancelled.")
		return nil
	}
	// One key per change: a retried request (network failure) cannot charge
	// twice.
	key := "mail-plan:" + NewIdempotencyKey()
	result, err := changeMailPlan(target.token, target.project.ID, domain.ID, wanted, action, key)
	if err != nil {
		return mailAPIError(err)
	}
	if *asJSON {
		return rtPrintJSON(result)
	}
	switch {
	case result.Status == "pending":
		_, _ = fmt.Fprintf(rtOut, "Purchase recorded. Sending capacity for %s updates when activation completes; follow it with: hubfly mail plan %s\n", domain.Domain, domain.Domain)
	case action == "schedule":
		_, _ = fmt.Fprintf(rtOut, "%s switches to the %s plan at renewal.\n", domain.Domain, mailPlanTitle(wanted))
	case action == "resume":
		_, _ = fmt.Fprintf(rtOut, "Renewal for %s is on again.\n", domain.Domain)
	default:
		_, _ = fmt.Fprintf(rtOut, "%s is now on the %s plan (%s).\n", domain.Domain, mailPlanTitle(wanted), result.Status)
	}
	return nil
}

// ---------------------------------------------------------------------------
// send-test
// ---------------------------------------------------------------------------

func mailSplitAddresses(values []string) []string {
	out := []string{}
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

func mailValidAddress(value string) bool {
	at := strings.LastIndexByte(value, '@')
	return at > 0 && at < len(value)-1 && !strings.ContainsAny(value, " <>\t\r\n") && strings.Contains(value[at+1:], ".")
}

func mailSendTestFlow(args []string) error {
	const usage = "hubfly mail send-test --from <address> --to <address>[,<address>] [--subject <text>] [--text <body>] [--yes] [--project <p>] [--json]"
	fs := newRealtimeFlags("mail send-test")
	projectRef := fs.String("project", "", "")
	from := fs.String("from", "", "")
	to := &rtList{split: true}
	fs.Var(to, "to", "")
	subject := fs.String("subject", "Test message from Hubfly space", "")
	text := fs.String("text", "", "")
	yes := fs.Bool("yes", false, "")
	fs.BoolVar(yes, "y", false, "")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 0 {
		return fmt.Errorf("usage: %s", usage)
	}
	sender := strings.TrimSpace(*from)
	recipients := mailSplitAddresses(to.values)
	if sender == "" || len(recipients) == 0 {
		return fmt.Errorf("--from and --to are required\nusage: %s", usage)
	}
	for _, address := range append([]string{sender}, recipients...) {
		if !mailValidAddress(address) {
			return fmt.Errorf("invalid email address %q", address)
		}
	}
	if len(recipients) > 10 {
		return errors.New("a test goes to at most 10 recipients")
	}
	body := strings.TrimSpace(*text)
	if body == "" {
		body = "This is a test message sent with the Hubfly space CLI (hubfly mail send-test)."
	}
	target, err := resolveMailTarget(*projectRef)
	if err != nil {
		return err
	}
	confirmed, err := dbdConfirm(*yes, fmt.Sprintf("Send a test from %s to %s? It counts toward the sending allowance.", sender, strings.Join(recipients, ", ")), "send the test")
	if err != nil {
		return err
	}
	if !confirmed {
		_, _ = fmt.Fprintln(rtOut, "Cancelled.")
		return nil
	}
	key := "mail-test:" + NewIdempotencyKey()
	payload := map[string]any{
		"projectId":      target.project.ID,
		"from":           sender,
		"recipients":     recipients,
		"subject":        strings.TrimSpace(*subject),
		"text":           body,
		"idempotencyKey": key,
		"submittedAt":    time.Now().UTC().Format(time.RFC3339),
	}
	result, err := submitMailTest(target.token, target.project.ID, payload, key)
	if err != nil {
		return mailAPIError(err)
	}
	if *asJSON {
		return rtPrintJSON(result)
	}
	_, _ = fmt.Fprintf(rtOut, "Test accepted (message %s, %s). Follow delivery in the Dashboard (Mail > Activity).\n", result.ID, result.Status)
	return nil
}

package cli

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Custom domains on the Dashboard API (hubfly-dashboard/engine/src/api/v1/domains):
//   GET  /api/v1/domains                       list the caller's domains
//   POST /api/v1/domains/create                {domainName}
//   POST /api/v1/domains/{domainId}/verify     checks the TXT record
//   POST /api/v1/domains/{domainId}/remove
// Domains belong to a user; attaching one to a container or load balancer
// stays in the web app.

const domainsUsage = `usage: hubfly domains list [--json]
       hubfly domains add <domain> [--wait] [--timeout 30m] [--json]
       hubfly domains verify <domain|id> [--wait] [--timeout 30m] [--json]
       hubfly domains remove <domain|id> [--yes] [--json]`

type customDomain struct {
	ID                     string  `json:"id"`
	DomainName             string  `json:"domainName"`
	Status                 string  `json:"status"`
	VerificationFqdn       *string `json:"verificationFqdn"`
	VerificationToken      *string `json:"verificationToken"`
	CreatedAt              string  `json:"createdAt"`
	VerifiedAt             *string `json:"verifiedAt"`
	LastError              *string `json:"lastError"`
	AssignedContainerName  *string `json:"assignedContainerName"`
	AssignedLoadBalancerID *string `json:"assignedLoadBalancerId"`
	AssignedLoadBalancer   *string `json:"assignedLoadBalancerName"`
}

type customDomainCreated struct {
	DomainID          string  `json:"domainId"`
	DomainName        string  `json:"domainName"`
	VerificationHost  *string `json:"verificationHost"`
	VerificationToken *string `json:"verificationToken"`
}

type domainVerifyResult struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

func domainsAPI(path string) string {
	return apiHost + "/api/v1/domains" + path
}

func runDomainsGroup(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		_, _ = fmt.Fprintln(rtOut, domainsUsage)
		return nil
	}
	action, rest := args[0], args[1:]
	fs := newProjectFlags("domains " + action)
	asJSON := fs.Bool("json", false, "")
	wait := fs.Bool("wait", false, "")
	timeout := fs.Duration("timeout", 30*time.Minute, "")
	yes := projectYesFlag(fs)
	switch action {
	case "list", "ls", "add", "create", "verify", "remove", "rm", "delete":
	default:
		return unknownCommandError("domains", action, []string{"list", "add", "verify", "remove"})
	}
	pos, err := parseRealtimeArgs(fs, rest, strings.TrimPrefix(domainsUsage, "usage: "))
	if err != nil {
		return err
	}
	if *timeout <= 0 {
		return errors.New("invalid arguments: --timeout must be positive")
	}
	needsArg := action != "list" && action != "ls"
	if needsArg && len(pos) != 1 || !needsArg && len(pos) != 0 {
		return errors.New(domainsUsage)
	}
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}
	switch action {
	case "list", "ls":
		return domainsList(token, *asJSON)
	case "add", "create":
		return domainsAdd(token, pos[0], *wait, *timeout, *asJSON)
	case "verify":
		return domainsVerify(token, pos[0], *wait, *timeout, *asJSON)
	default:
		return domainsRemove(token, pos[0], *yes, *asJSON)
	}
}

func fetchCustomDomains(token string) ([]customDomain, error) {
	var out []customDomain
	err := doJSONRequest(http.MethodGet, domainsAPI(""), token, nil, &out)
	return out, err
}

// resolveCustomDomain finds a domain by id or name (case-insensitive) and
// suggests the closest name on a typo.
func resolveCustomDomain(token, ref string) (customDomain, error) {
	domains, err := fetchCustomDomains(token)
	if err != nil {
		return customDomain{}, err
	}
	want := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(ref)), ".")
	names := make([]string, 0, len(domains))
	for _, d := range domains {
		if d.ID == ref || strings.ToLower(d.DomainName) == want {
			return d, nil
		}
		names = append(names, d.DomainName)
	}
	msg := fmt.Sprintf("domain '%s' not found", ref)
	if s := closestMatch(want, names); s != "" {
		msg += fmt.Sprintf(". Did you mean '%s'?", s)
	} else if len(names) == 0 {
		msg += ". Add one with: hubfly domains add <domain>"
	}
	return customDomain{}, errors.New(msg)
}

func domainsList(token string, asJSON bool) error {
	domains, err := fetchCustomDomains(token)
	if err != nil {
		return err
	}
	if asJSON {
		if domains == nil {
			domains = []customDomain{}
		}
		return rtPrintJSON(domains)
	}
	if len(domains) == 0 {
		_, _ = fmt.Fprintln(rtOut, "No custom domains. Add one with: hubfly domains add <domain>")
		return nil
	}
	_, _ = fmt.Fprintf(rtOut, "%-36s  %-12s  %s\n", "DOMAIN", "STATUS", "ATTACHED TO")
	for _, d := range domains {
		attached := "-"
		if d.AssignedContainerName != nil {
			attached = "container " + *d.AssignedContainerName
		} else if d.AssignedLoadBalancer != nil {
			attached = "load balancer " + *d.AssignedLoadBalancer
		}
		_, _ = fmt.Fprintf(rtOut, "%-36s  %-12s  %s\n", d.DomainName, d.Status, attached)
	}
	return nil
}

func printDomainDNS(w io.Writer, name string, host, value *string) {
	if host == nil || value == nil || *host == "" || *value == "" {
		_, _ = fmt.Fprintf(w, "%s is verified.\n", name)
		return
	}
	_, _ = fmt.Fprintf(w, "Add this DNS record at your DNS provider to verify %s:\n\n", name)
	_, _ = fmt.Fprintf(w, "  Type   TXT\n  Name   %s\n  Value  %s\n\n", *host, *value)
	_, _ = fmt.Fprintf(w, "Then run: hubfly domains verify %s --wait\n", name)
}

func domainsAdd(token, name string, wait bool, timeout time.Duration, asJSON bool) error {
	name = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
	if u, err := url.Parse(name); err == nil && u.Host != "" {
		return fmt.Errorf("invalid domain %q: pass the bare host name, e.g. %s", name, u.Hostname())
	}
	if !strings.Contains(name, ".") || strings.ContainsAny(name, " /:") {
		return fmt.Errorf("invalid domain %q: expected a host name like app.example.com", name)
	}
	var created customDomainCreated
	if err := doJSONRequest(http.MethodPost, domainsAPI("/create"), token, map[string]any{"domainName": name}, &created); err != nil {
		return err
	}
	human := projectHumanOut(asJSON)
	_, _ = fmt.Fprintf(human, "Added %s (%s).\n", created.DomainName, created.DomainID)
	printDomainDNS(human, created.DomainName, created.VerificationHost, created.VerificationToken)
	if wait && created.VerificationToken != nil {
		result, err := waitForDomainVerification(token, created.DomainID, timeout, human)
		if asJSON {
			_ = rtPrintJSON(map[string]any{"domain": created, "verification": result})
		}
		return err
	}
	if asJSON {
		return rtPrintJSON(created)
	}
	return nil
}

func domainsVerify(token, ref string, wait bool, timeout time.Duration, asJSON bool) error {
	d, err := resolveCustomDomain(token, ref)
	if err != nil {
		return err
	}
	human := projectHumanOut(asJSON)
	var result domainVerifyResult
	if wait {
		result, err = waitForDomainVerification(token, d.ID, timeout, human)
	} else {
		err = doJSONRequest(http.MethodPost, domainsAPI("/"+url.PathEscape(d.ID)+"/verify"), token, map[string]any{}, &result)
		if err == nil && !result.Success {
			err = domainNotVerifiedError(d, result.Message)
		}
	}
	if asJSON {
		_ = rtPrintJSON(map[string]any{"domainId": d.ID, "domainName": d.DomainName, "success": result.Success, "message": result.Message})
	} else if err == nil {
		_, _ = fmt.Fprintf(rtOut, "%s is verified. %s\n", d.DomainName, result.Message)
	}
	if err != nil && !asJSON && !wait {
		printDomainDNS(rtErr, d.DomainName, d.VerificationFqdn, d.VerificationToken)
	}
	return err
}

func domainNotVerifiedError(d customDomain, message string) error {
	if message == "" {
		message = "the TXT record was not found yet"
	}
	return fmt.Errorf("%s is not verified: %s. DNS changes can take a few minutes; retry with: hubfly domains verify %s --wait", d.DomainName, strings.TrimSuffix(message, "."), d.DomainName)
}

// waitForDomainVerification retries verify until it succeeds or the timeout
// passes, backing off from the poll interval up to one minute.
func waitForDomainVerification(token, domainID string, timeout time.Duration, human io.Writer) (domainVerifyResult, error) {
	deadline := time.Now().Add(timeout)
	delay := rtPollInterval
	if delay <= 0 {
		delay = 5 * time.Second
	}
	_, _ = fmt.Fprintf(human, "Waiting for the TXT record (up to %s, Ctrl+C to stop; verification continues on the next check)...\n", timeout)
	for attempt := 1; ; attempt++ {
		var result domainVerifyResult
		err := doJSONRequest(http.MethodPost, domainsAPI("/"+url.PathEscape(domainID)+"/verify"), token, map[string]any{}, &result)
		if err == nil && result.Success {
			return result, nil
		}
		var apiErr *apiError
		if err != nil && errors.As(err, &apiErr) && apiErr.Status >= 400 && apiErr.Status < 500 && apiErr.Status != 429 {
			return result, err
		}
		if time.Now().Add(delay).After(deadline) {
			if err != nil {
				return result, fmt.Errorf("timed out waiting for verification: %w", err)
			}
			return result, fmt.Errorf("timed out waiting for verification: %s", result.Message)
		}
		_, _ = fmt.Fprintf(human, "  check %d: not yet (%s); next check in %s\n", attempt, domainCheckMessage(result.Message, err), delay)
		time.Sleep(delay)
		if delay < time.Minute {
			delay *= 2
			if delay > time.Minute {
				delay = time.Minute
			}
		}
	}
}

func domainCheckMessage(message string, err error) string {
	if err != nil {
		return err.Error()
	}
	if strings.TrimSpace(message) == "" {
		return "TXT record not found yet"
	}
	return message
}

func domainsRemove(token, ref string, yes, asJSON bool) error {
	d, err := resolveCustomDomain(token, ref)
	if err != nil {
		return err
	}
	if !yes {
		if !rtInteractive() {
			return errors.New("invalid arguments: pass --yes to remove a domain in noninteractive mode")
		}
		ok, confirmErr := projectConfirmYes(projectHumanOut(asJSON), fmt.Sprintf("Remove %s?", d.DomainName))
		if confirmErr != nil {
			return confirmErr
		}
		if !ok {
			return errors.New("cancelled")
		}
	}
	var result struct {
		Success bool `json:"success"`
	}
	if err := doJSONRequest(http.MethodPost, domainsAPI("/"+url.PathEscape(d.ID)+"/remove"), token, map[string]any{}, &result); err != nil {
		return err
	}
	if asJSON {
		return rtPrintJSON(map[string]any{"domainId": d.ID, "domainName": d.DomainName, "removed": true})
	}
	_, _ = fmt.Fprintf(rtOut, "Removed %s.\n", d.DomainName)
	return nil
}

// ---------------------------------------------------------------------------
// Typo suggestions
// ---------------------------------------------------------------------------

// closestMatch returns the candidate nearest to input by edit distance, or ""
// when none is close enough to be a likely typo.
func closestMatch(input string, candidates []string) string {
	best, bestDist := "", -1
	for _, c := range candidates {
		d := levenshtein(strings.ToLower(input), strings.ToLower(c))
		if bestDist < 0 || d < bestDist {
			best, bestDist = c, d
		}
	}
	limit := len(input) / 3
	if limit < 2 {
		limit = 2
	}
	if bestDist < 0 || bestDist > limit {
		return ""
	}
	return best
}

func levenshtein(a, b string) int {
	ar, br := []rune(a), []rune(b)
	prev := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		cur := make([]int, len(br)+1)
		cur[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(br)]
}

// unknownCommandError reports an unknown subcommand with a suggestion.
func unknownCommandError(group, action string, known []string) error {
	prefix := "hubfly "
	if group != "" {
		prefix += group + " "
	}
	msg := fmt.Sprintf("unknown command: %s%s", prefix, action)
	if s := closestMatch(action, known); s != "" {
		msg += fmt.Sprintf("\nDid you mean: %s%s?", prefix, s)
	}
	return errors.New(msg + "\nRun 'hubfly help' for usage.")
}

// didYouMean is ", did you mean X?" for a likely typo, else "".
func didYouMean(input string, known []string) string {
	if s := closestMatch(input, known); s != "" {
		return fmt.Sprintf("; did you mean %q?", s)
	}
	return ""
}

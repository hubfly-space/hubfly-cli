package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/mail"
	"strconv"
	"strings"
	"text/tabwriter"
)

// `hubfly project`: the project list, project history and moving a project
// to another owner (transfers). Region migration commands (migrate,
// migration, rehome) join the same group: add their usage lines to
// projectUsageText and their cases to runProjectGroup.

const projectUsageText = `Region moves:
  hubfly project migrate <project> --region <id> [--mode cold|warm] [--at RFC3339] [--max-freeze 10m] [--wait] [--json]
  hubfly project migration status|watch|retry|cancel|continue|move-back|dns|attestation <project> [--wait] [--json]
  hubfly project rehome <project> --region <id> [--yes --confirm-name <name>] [--json]

Projects:
  hubfly project list [--org <id|slug>] [--json]
  hubfly project get <project> [--json]
  hubfly project history <project> [--json]

Global projects (gpj_…) span every region: list and get show them; deploy a Cell to one region
with hubfly deploy --project gpj_… --region <region>.

Transfers (give a project to another HubFly account or organization):
  hubfly project transfer <project> --to <email> [--rotate-credentials] [--yes --confirm-name <name>] [--json]
  hubfly project transfers [list] [--json]
  hubfly project transfers status <project> [--json]
  hubfly project transfers show <token> [--json]
  hubfly project transfers accept <token> [--personal | --org <id|slug>] [--only <transferId>...]
                                          [--keep-member <userId|email>...] [--yes] [--json]
  hubfly project transfers decline <token> [--yes]
  hubfly project transfers cancel <project> [--yes] [--json]

<project> is a project id or name. <token> is the offer token from the transfer email, or the
whole link. The owner changes at the next full hour after the offer is accepted.
Starting, accepting, declining and cancelling a transfer may require a signed-in dashboard
session: when HubFly refuses the CLI token, the command prints the dashboard page to use.`

func printProjectUsage() {
	_, _ = fmt.Fprintln(rtOut, projectUsageText)
}

func runProjectGroup(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		printProjectUsage()
		return nil
	}
	command, rest := args[0], args[1:]
	switch command {
	case "list", "ls":
		return projectListFlow(rest)
	case "get", "show", "info":
		return projectGetFlow(rest)
	case "history":
		return projectHistoryFlow(rest)
	case "transfer":
		return projectTransferFlow(rest)
	case "transfers":
		return runProjectTransfersGroup(rest)
	case "migrate":
		return projectMigrateFlow(rest, false)
	case "migration":
		return runProjectMigrationGroup(rest)
	case "rehome":
		return projectMigrateFlow(rest, true)
	default:
		return fmt.Errorf("unknown project command %q%s (run: hubfly project help)", command, didYouMean(command, []string{"list", "get", "history", "transfer", "transfers", "migrate", "migration", "rehome", "help"}))
	}
}

func runProjectTransfersGroup(args []string) error {
	if len(args) == 0 {
		return projectTransfersListFlow(args)
	}
	command, rest := args[0], args[1:]
	switch command {
	case "list", "ls":
		return projectTransfersListFlow(rest)
	case "status":
		return projectTransferStatusFlow(rest)
	case "show", "offer":
		return projectTransferShowFlow(rest)
	case "accept":
		return projectTransferAcceptFlow(rest)
	case "decline":
		return projectTransferDeclineFlow(rest)
	case "cancel":
		return projectTransferCancelFlow(rest)
	case "help", "--help", "-h":
		printProjectUsage()
		return nil
	default:
		if strings.HasPrefix(command, "-") {
			return projectTransfersListFlow(args)
		}
		return fmt.Errorf("unknown project transfers command %q%s (run: hubfly project help)", command, didYouMean(command, []string{"list", "status", "show", "accept", "decline", "cancel"}))
	}
}

func newProjectFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet("project "+name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func projectYesFlag(fs *flag.FlagSet) *bool {
	yes := fs.Bool("yes", false, "")
	fs.BoolVar(yes, "y", false, "")
	return yes
}

// ---------------------------------------------------------------------------
// Shared helpers (also used by the region migration commands)
// ---------------------------------------------------------------------------

// resolveMoveProject finds a project of any type by id or case-insensitive
// name.
func resolveMoveProject(token, ref string) (project, error) {
	ref = strings.TrimSpace(ref)
	all, err := fetchAllProjectsAnyType(token, "")
	if err != nil {
		return project{}, err
	}
	var byName []project
	for _, item := range all {
		if item.ID == ref {
			return item, nil
		}
		if strings.EqualFold(item.Name, ref) {
			byName = append(byName, item)
		}
	}
	switch len(byName) {
	case 0:
		return project{}, fmt.Errorf("project '%s' not found (list them with: hubfly projects)", ref)
	case 1:
		return byName[0], nil
	}
	return project{}, fmt.Errorf("project name '%s' is ambiguous (%s); use the project id", ref, storageProjectList(byName))
}

// projectDashboardURL is the Dashboard web app (the CLI login page's host).
func projectDashboardURL(path string) string {
	return strings.TrimSuffix(cliAuthURL, "/cli/auth") + path
}

// projectMovesAPIError adds a hint to the Dashboard errors of a transfer or
// migration. dashboardPath is the web page where the same action can be
// finished (used when HubFly refuses the CLI's token). The wrapped
// *apiError keeps exit codes intact.
func projectMovesAPIError(err error, dashboardPath string) error {
	var apiErr *apiError
	if !errors.As(err, &apiErr) {
		return err
	}
	hint := ""
	switch apiErr.Code {
	case "MOVE_SESSION_REQUIRED":
		hint = "HubFly accepts this only from a signed-in dashboard session, not from an API or CLI token."
		if dashboardPath != "" {
			hint += "\nContinue in the dashboard: " + projectDashboardURL(dashboardPath)
		}
	case "PROJECT_MOVES_DISABLED":
		hint = "Project transfers and region moves are paused on HubFly right now; try again later."
	case "PROJECT_LOCKED", "PROJECT_JOB_IN_PROGRESS":
		hint = "Another transfer or move of this project is in progress; check it with: hubfly project history <project>"
	case "TRANSFER_NOT_FOUND":
		hint = "The offer does not exist, is no longer open, or was sent to another email address than the account you are logged in with (check with: hubfly whoami)."
	case "TRANSFER_EMAIL_NOT_VERIFIED":
		hint = "Verify your account's email address in the dashboard first."
	case "TRANSFER_FORBIDDEN":
		hint = "Only the personal owner, or an owner of the organization, can transfer a project. Accepting into an organization needs its owner role, or a role that can create projects and manage billing."
	case "TRANSFER_OTP_THROTTLED", "TRANSFER_OFFER_THROTTLED":
		hint = "Too many attempts; wait a few minutes before trying again."
	}
	if hint == "" {
		return err
	}
	return fmt.Errorf("%w\n%s", err, hint)
}

// projectHumanOut is where previews, notes and prompts go: stderr with
// --json, so stdout carries only the JSON document.
func projectHumanOut(asJSON bool) io.Writer {
	if asJSON {
		return rtErr
	}
	return rtOut
}

// projectPromptLine prints label to w and reads one line from stdin.
func projectPromptLine(w io.Writer, label string) (string, error) {
	_, _ = fmt.Fprint(w, label)
	line, err := stdin.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// projectConfirmYes asks a yes/no question; the default is no.
func projectConfirmYes(w io.Writer, question string) (bool, error) {
	for {
		answer, err := projectPromptLine(w, question+" (y/N): ")
		if err != nil {
			return false, err
		}
		switch strings.ToLower(answer) {
		case "y", "yes":
			return true, nil
		case "", "n", "no":
			return false, nil
		}
		_, _ = fmt.Fprintln(w, "Please answer yes or no.")
	}
}

// printProjectMovesRaw prints an API data object for --json.
func printProjectMovesRaw(raw json.RawMessage) error {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	return rtPrintJSON(value)
}

func printMoveIssues(w io.Writer, title string, issues []moveIssue) {
	if len(issues) == 0 {
		return
	}
	_, _ = fmt.Fprintf(w, "%s:\n", title)
	for _, issue := range issues {
		_, _ = fmt.Fprintf(w, "  - %s (%s)\n", issue.Message, issue.Code)
	}
}

func printMoveCounts(w io.Writer, title string, counts []moveCount) {
	shown := []moveCount{}
	for _, item := range counts {
		if item.Count > 0 {
			shown = append(shown, item)
		}
	}
	if len(shown) == 0 {
		return
	}
	_, _ = fmt.Fprintf(w, "%s:\n", title)
	for _, item := range shown {
		_, _ = fmt.Fprintf(w, "  %4d  %s\n", item.Count, item.Label)
	}
}

// moveIssuesError lists blockers as one error.
func moveIssuesError(prefix string, blockers []moveIssue) error {
	parts := make([]string, 0, len(blockers))
	for _, issue := range blockers {
		parts = append(parts, "  - "+issue.Message+" ("+issue.Code+")")
	}
	if len(parts) == 0 {
		return errors.New(prefix)
	}
	return fmt.Errorf("%s:\n%s", prefix, strings.Join(parts, "\n"))
}

func optionalText(value *string) string {
	if value == nil {
		return "-"
	}
	return valueOrDash(*value)
}

// projectJobProgress describes a running job ("Settling billing (2/8)").
func projectJobProgress(job *projectJobView) string {
	if job == nil {
		return "-"
	}
	label := job.Status
	if job.StepLabel != nil && *job.StepLabel != "" {
		label = *job.StepLabel
		if job.StepCount > 0 {
			label = fmt.Sprintf("%s (%d/%d)", label, job.StepIndex+1, job.StepCount)
		}
		if job.Phase == "compensation" {
			label = "rolling back: " + label
		}
	}
	if job.ErrorMsg != nil && *job.ErrorMsg != "" {
		label += " - " + *job.ErrorMsg
	}
	return label
}

// ---------------------------------------------------------------------------
// project transfer
// ---------------------------------------------------------------------------

const projectTransferUsage = "hubfly project transfer <project> --to <email> [--rotate-credentials] [--yes --confirm-name <name>] [--json]"

type projectTransferOptions struct {
	projectRef  string
	to          string
	rotate      bool
	yes         bool
	confirmName string
	json        bool
}

func parseProjectTransferOptions(args []string) (projectTransferOptions, error) {
	fs := newProjectFlags("transfer")
	to := fs.String("to", "", "")
	rotate := fs.Bool("rotate-credentials", false, "")
	yes := projectYesFlag(fs)
	confirmName := fs.String("confirm-name", "", "")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, projectTransferUsage)
	if err != nil {
		return projectTransferOptions{}, err
	}
	if len(positionals) != 1 || strings.TrimSpace(*to) == "" {
		return projectTransferOptions{}, fmt.Errorf("usage: %s", projectTransferUsage)
	}
	recipient := strings.TrimSpace(*to)
	if parsed, parseErr := mail.ParseAddress(recipient); parseErr != nil || parsed.Address != recipient {
		return projectTransferOptions{}, fmt.Errorf("invalid email address %q for --to", recipient)
	}
	opts := projectTransferOptions{
		projectRef:  positionals[0],
		to:          recipient,
		rotate:      *rotate,
		yes:         *yes,
		confirmName: *confirmName,
		json:        *asJSON,
	}
	if opts.yes && opts.confirmName == "" {
		return projectTransferOptions{}, errors.New("invalid arguments: --yes needs --confirm-name <project name>")
	}
	if !opts.yes && !rtInteractive() {
		return projectTransferOptions{}, errors.New("invalid arguments: confirmation required; re-run with --yes --confirm-name <project name>")
	}
	return opts, nil
}

func projectTransferFlow(args []string) error {
	opts, err := parseProjectTransferOptions(args)
	if err != nil {
		return err
	}
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}
	target, err := resolveMoveProject(token, opts.projectRef)
	if err != nil {
		return err
	}
	dashboardPath := "/projects/" + target.ID
	preview, err := previewProjectTransfer(token, target.ID)
	if err != nil {
		return projectMovesAPIError(err, dashboardPath)
	}
	out := projectHumanOut(opts.json)
	printProjectTransferPreview(out, preview, opts.to)
	if !preview.CanTransfer {
		return moveIssuesError(fmt.Sprintf("project '%s' cannot be transferred yet", preview.ProjectName), preview.Blockers)
	}

	if opts.yes {
		if strings.TrimSpace(opts.confirmName) != strings.TrimSpace(preview.ProjectName) {
			return fmt.Errorf("invalid arguments: --confirm-name %q does not match the project name %q", opts.confirmName, preview.ProjectName)
		}
	} else {
		_, _ = fmt.Fprintf(out, "This offers '%s' to %s. Once they accept, they own it and pay for it.\n", preview.ProjectName, opts.to)
		typed, confirmErr := projectPromptLine(out, fmt.Sprintf("Type '%s' to confirm: ", preview.ProjectName))
		if confirmErr != nil {
			return confirmErr
		}
		if typed != strings.TrimSpace(preview.ProjectName) {
			return errors.New("cancelled: the name did not match")
		}
	}

	sent, err := createProjectTransferOtp(token, target.ID, opts.to)
	if err != nil {
		return projectMovesAPIError(err, dashboardPath)
	}
	_, _ = fmt.Fprintf(rtErr, "We emailed a confirmation code to %s (valid until %s).\n", sent.SentTo, storageTime(sent.ExpiresAt))

	var view projectTransferView
	var raw json.RawMessage
	for attempt := 1; ; attempt++ {
		code, promptErr := projectPromptLine(out, "Confirmation code: ")
		if promptErr != nil {
			return promptErr
		}
		code = strings.TrimSpace(code)
		if code == "" {
			return errors.New("cancelled: no confirmation code entered")
		}
		view, raw, err = createProjectTransfer(token, target.ID, opts.to, preview.ProjectName, code, opts.rotate)
		var apiErr *apiError
		if err != nil && errors.As(err, &apiErr) && apiErr.Code == "TRANSFER_OTP_INVALID" && attempt < 3 && rtInteractive() {
			_, _ = fmt.Fprintln(rtErr, "That code is not right; try again.")
			continue
		}
		break
	}
	if err != nil {
		return projectMovesAPIError(err, dashboardPath)
	}
	if opts.json {
		return printProjectMovesRaw(raw)
	}
	_, _ = fmt.Fprintf(rtOut, "Offer sent to %s. It expires %s.\n", view.RecipientEmail, storageExpiry(view.ExpiresAt))
	_, _ = fmt.Fprintln(rtOut, "The recipient accepts from the link in their email, or with: hubfly project transfers accept <token>")
	_, _ = fmt.Fprintf(rtOut, "Check it with: hubfly project transfers status %s\n", target.ID)
	return nil
}

func printProjectTransferPreview(w io.Writer, preview projectTransferPreview, recipient string) {
	_, _ = fmt.Fprintf(w, "Transfer %s (%s, %s) to %s\n", preview.ProjectName, valueOrDash(preview.ProjectType), preview.ProjectID, recipient)
	printMoveCounts(w, "Moves with the project", preview.Moves)
	printMoveCounts(w, "Revoked if you lose access", preview.RevokedIfSenderLosesAccess)
	if preview.EnvValueCount > 0 {
		_, _ = fmt.Fprintf(w, "The new owner can read the project's %d environment value(s).\n", preview.EnvValueCount)
	}
	printMoveIssues(w, "Warnings", preview.Warnings)
	printMoveIssues(w, "Blockers", preview.Blockers)
	_, _ = fmt.Fprintln(w)
}

// ---------------------------------------------------------------------------
// project transfers list | status | show
// ---------------------------------------------------------------------------

func projectTransfersListFlow(args []string) error {
	const usage = "hubfly project transfers [list] [--json]"
	fs := newProjectFlags("transfers list")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 0 {
		return fmt.Errorf("usage: %s", usage)
	}
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}
	list, raw, err := listProjectTransfers(token)
	if err != nil {
		return projectMovesAPIError(err, "")
	}
	if *asJSON {
		return printProjectMovesRaw(raw)
	}
	if len(list.Incoming) == 0 && len(list.Outgoing) == 0 {
		_, _ = fmt.Fprintln(rtOut, "No project transfers.")
		return nil
	}
	if len(list.Incoming) > 0 {
		_, _ = fmt.Fprintln(rtOut, "Offered to you:")
		tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "PROJECT\tTYPE\tFROM\tSTATUS\tEXPIRES\tTRANSFER")
		for _, item := range list.Incoming {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", item.ProjectName, valueOrDash(item.ProjectType), item.SenderName, item.Status, storageExpiry(item.ExpiresAt), item.TransferID)
		}
		_ = tw.Flush()
		_, _ = fmt.Fprintln(rtOut, "Accept with the link in the offer email, or: hubfly project transfers accept <token>")
	}
	if len(list.Outgoing) > 0 {
		if len(list.Incoming) > 0 {
			_, _ = fmt.Fprintln(rtOut)
		}
		_, _ = fmt.Fprintln(rtOut, "Started by you:")
		printProjectTransferTable(list.Outgoing)
	}
	return nil
}

func printProjectTransferTable(views []projectTransferView) {
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "PROJECT\tTO\tSTATUS\tPROGRESS\tEXPIRES\tTRANSFER")
	for _, view := range views {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", view.ProjectName, projectTransferRecipient(view), view.Status, projectJobProgress(view.Job), storageExpiry(view.ExpiresAt), view.ID)
	}
	_ = tw.Flush()
}

func projectTransferRecipient(view projectTransferView) string {
	if view.To != nil && view.To.Name != "" {
		return fmt.Sprintf("%s (%s)", view.To.Name, view.RecipientEmail)
	}
	return view.RecipientEmail
}

func projectTransferStatusFlow(args []string) error {
	const usage = "hubfly project transfers status <project> [--json]"
	fs := newProjectFlags("transfers status")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return fmt.Errorf("usage: %s", usage)
	}
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}
	target, err := resolveMoveProject(token, positionals[0])
	if err != nil {
		return err
	}
	view, raw, err := fetchProjectTransfer(token, target.ID)
	if err != nil {
		return projectMovesAPIError(err, "")
	}
	if *asJSON {
		return printProjectMovesRaw(raw)
	}
	if view == nil {
		_, _ = fmt.Fprintf(rtOut, "Project '%s' has no transfers.\n", target.Name)
		return nil
	}
	printProjectTransferView(*view)
	return nil
}

func printProjectTransferView(view projectTransferView) {
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "Project:\t%s (%s)\n", view.ProjectName, valueOrDash(view.ProjectType))
	_, _ = fmt.Fprintf(tw, "Transfer:\t%s\n", view.ID)
	_, _ = fmt.Fprintf(tw, "Status:\t%s\n", view.Status)
	_, _ = fmt.Fprintf(tw, "From:\t%s\n", valueOrDash(view.From.Name))
	_, _ = fmt.Fprintf(tw, "To:\t%s\n", projectTransferRecipient(view))
	_, _ = fmt.Fprintf(tw, "Created:\t%s\n", storageTime(view.CreatedAt))
	if view.Status == "pending" {
		_, _ = fmt.Fprintf(tw, "Expires:\t%s\n", storageExpiry(view.ExpiresAt))
	}
	if view.AcceptedAt != nil {
		_, _ = fmt.Fprintf(tw, "Accepted:\t%s\n", storageTime(*view.AcceptedAt))
	}
	if view.HandoverAt != nil {
		_, _ = fmt.Fprintf(tw, "Owner changes:\t%s\n", storageTime(*view.HandoverAt))
	}
	if view.CompletedAt != nil {
		_, _ = fmt.Fprintf(tw, "Completed:\t%s\n", storageTime(*view.CompletedAt))
	}
	if view.Job != nil {
		_, _ = fmt.Fprintf(tw, "Progress:\t%s\n", projectJobProgress(view.Job))
	}
	if view.FailureMessage != nil && *view.FailureMessage != "" {
		_, _ = fmt.Fprintf(tw, "Failure:\t%s (%s)\n", *view.FailureMessage, optionalText(view.FailureCode))
	}
	_ = tw.Flush()
}

func projectTransferShowFlow(args []string) error {
	const usage = "hubfly project transfers show <token> [--json]"
	fs := newProjectFlags("transfers show")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return fmt.Errorf("usage: %s", usage)
	}
	offerToken, ok := normalizeProjectTransferToken(positionals[0])
	if !ok {
		return errors.New("invalid transfer token: copy the link or the token from the offer email")
	}
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}
	offer, raw, err := fetchProjectTransferOffer(token, offerToken)
	if err != nil {
		return projectMovesAPIError(err, "")
	}
	if *asJSON {
		return printProjectMovesRaw(raw)
	}
	printProjectTransferOffer(rtOut, offer)
	return nil
}

func printProjectTransferOffer(w io.Writer, offer projectTransferOffer) {
	_, _ = fmt.Fprintf(w, "%s offers you %d project(s). The offer expires %s.\n", offer.SenderName, len(offer.Projects), storageExpiry(offer.ExpiresAt))
	for _, item := range offer.Projects {
		hosting := item.Hosting
		_, _ = fmt.Fprintf(w, "\n%s (%s, transfer %s)\n", item.ProjectName, valueOrDash(item.ProjectType), item.TransferID)
		_, _ = fmt.Fprintf(w, "  %d container(s), %d volume(s), %d box(es), %d bucket(s)\n", hosting.Containers, hosting.Volumes, hosting.Boxes, hosting.Buckets)
		if len(hosting.Hostnames) > 0 {
			_, _ = fmt.Fprintf(w, "  Hostnames: %s\n", strings.Join(hosting.Hostnames, ", "))
		}
		if len(hosting.CustomDomains) > 0 {
			_, _ = fmt.Fprintf(w, "  Custom domains: %s\n", strings.Join(hosting.CustomDomains, ", "))
		}
		if len(hosting.MailDomains) > 0 {
			_, _ = fmt.Fprintf(w, "  Mail domains: %s\n", strings.Join(hosting.MailDomains, ", "))
		}
		if len(item.Members) > 0 {
			_, _ = fmt.Fprintln(w, "  Members (removed unless you keep them with --keep-member):")
			for _, member := range item.Members {
				line := fmt.Sprintf("    %s <%s> %s", member.Name, member.Email, member.UserID)
				if len(member.Credentials) > 0 {
					line += " - credentials: " + strings.Join(member.Credentials, ", ")
				}
				_, _ = fmt.Fprintln(w, line)
			}
		}
	}
	if len(offer.Destinations) > 0 {
		_, _ = fmt.Fprintln(w, "\nYou can accept into:")
		for _, destination := range offer.Destinations {
			_, _ = fmt.Fprintf(w, "  %s\n", projectTransferDestinationLabel(destination))
		}
	}
}

func projectTransferDestinationLabel(destination projectTransferDestination) string {
	kind := "personal account"
	flag := "--personal"
	if destination.Type == "organization" {
		kind = "organization"
		flag = "--org " + destination.ID
	}
	label := fmt.Sprintf("%s (%s, %s)", destination.Name, kind, flag)
	if !destination.Eligible {
		label += " - not eligible: " + optionalText(destination.Reason)
	}
	return label
}

// ---------------------------------------------------------------------------
// project transfers accept | decline | cancel
// ---------------------------------------------------------------------------

const projectTransferAcceptUsage = "hubfly project transfers accept <token> [--personal | --org <id|slug>] [--only <transferId>...] [--keep-member <userId|email>...] [--yes] [--json]"

func projectTransferAcceptFlow(args []string) error {
	fs := newProjectFlags("transfers accept")
	personal := fs.Bool("personal", false, "")
	orgRef := fs.String("org", "", "")
	only := &rtList{split: true}
	fs.Var(only, "only", "")
	keep := &rtList{split: true}
	fs.Var(keep, "keep-member", "")
	yes := projectYesFlag(fs)
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, projectTransferAcceptUsage)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return fmt.Errorf("usage: %s", projectTransferAcceptUsage)
	}
	if *personal && strings.TrimSpace(*orgRef) != "" {
		return errors.New("invalid arguments: use either --personal or --org, not both")
	}
	offerToken, ok := normalizeProjectTransferToken(positionals[0])
	if !ok {
		return errors.New("invalid transfer token: copy the link or the token from the offer email")
	}
	if !*yes && !rtInteractive() {
		return errors.New("invalid arguments: confirmation required; re-run with --yes")
	}
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}
	dashboardPath := "/projects/transfers/" + offerToken
	offer, _, err := fetchProjectTransferOffer(token, offerToken)
	if err != nil {
		return projectMovesAPIError(err, "")
	}
	out := projectHumanOut(*asJSON)
	printProjectTransferOffer(out, offer)
	_, _ = fmt.Fprintln(out)

	selected, err := selectOfferedTransfers(offer, only.values)
	if err != nil {
		return err
	}
	keepMembers, err := resolveKeptMembers(selected, keep.values)
	if err != nil {
		return err
	}
	destination, err := pickTransferDestination(out, token, offer, *personal, strings.TrimSpace(*orgRef))
	if err != nil {
		return err
	}
	transferIDs := []string{}
	if len(only.values) > 0 {
		for _, item := range selected {
			transferIDs = append(transferIDs, item.TransferID)
		}
	}

	preview, _, err := previewProjectTransferAcceptance(token, offerToken, destination, transferIDs)
	if err != nil {
		return projectMovesAPIError(err, dashboardPath)
	}
	printProjectTransferAcceptancePreview(out, preview)
	if !preview.Eligible {
		return moveIssuesError("you cannot accept this offer into that destination yet", preview.Blockers)
	}
	if !*yes {
		confirmed, confirmErr := projectConfirmYes(out, fmt.Sprintf("Accept %d project(s)? You pay for them from the moment the owner changes", len(selected)))
		if confirmErr != nil {
			return confirmErr
		}
		if !confirmed {
			return errors.New("cancelled")
		}
	}
	views, raw, err := acceptProjectTransfer(token, offerToken, destination, transferIDs, keepMembers)
	if err != nil {
		return projectMovesAPIError(err, dashboardPath)
	}
	if *asJSON {
		return printProjectMovesRaw(raw)
	}
	_, _ = fmt.Fprintf(rtOut, "Accepted %d project(s).\n", len(views))
	for _, view := range views {
		handover := "at the next full hour"
		if view.HandoverAt != nil {
			handover = storageTime(*view.HandoverAt)
		}
		_, _ = fmt.Fprintf(rtOut, "  %s: the owner changes %s\n", view.ProjectName, handover)
	}
	return nil
}

// selectOfferedTransfers returns the projects of the offer to accept: all of
// them, or those named by --only (transfer id or project name).
func selectOfferedTransfers(offer projectTransferOffer, only []string) ([]projectTransferOfferProject, error) {
	if len(only) == 0 {
		return offer.Projects, nil
	}
	selected := []projectTransferOfferProject{}
	seen := map[string]bool{}
	for _, ref := range only {
		found := false
		for _, item := range offer.Projects {
			if item.TransferID == ref || strings.EqualFold(item.ProjectName, ref) {
				if !seen[item.TransferID] {
					selected = append(selected, item)
					seen[item.TransferID] = true
				}
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("invalid --only %q: the offer has no such project", ref)
		}
	}
	return selected, nil
}

// resolveKeptMembers maps --keep-member (user id or email) to the projects
// where that person is a direct member.
func resolveKeptMembers(selected []projectTransferOfferProject, refs []string) ([]projectTransferKeepMembers, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	matched := map[string]bool{}
	result := []projectTransferKeepMembers{}
	for _, item := range selected {
		ids := []string{}
		for _, member := range item.Members {
			for _, ref := range refs {
				if member.UserID == ref || strings.EqualFold(member.Email, ref) {
					ids = append(ids, member.UserID)
					matched[ref] = true
					break
				}
			}
		}
		if len(ids) > 0 {
			result = append(result, projectTransferKeepMembers{TransferID: item.TransferID, UserIDs: ids})
		}
	}
	for _, ref := range refs {
		if !matched[ref] {
			return nil, fmt.Errorf("invalid --keep-member %q: not a member of the offered project(s)", ref)
		}
	}
	return result, nil
}

// pickTransferDestination resolves --personal / --org, or asks in a
// terminal.
func pickTransferDestination(w io.Writer, token string, offer projectTransferOffer, personal bool, orgRef string) (projectTransferDestinationInput, error) {
	choose := func(destination projectTransferDestination) (projectTransferDestinationInput, error) {
		if !destination.Eligible {
			return projectTransferDestinationInput{}, fmt.Errorf("%s cannot receive this offer: %s", destination.Name, optionalText(destination.Reason))
		}
		return projectTransferDestinationInput{Type: destination.Type, ID: destination.ID}, nil
	}
	switch {
	case personal:
		for _, destination := range offer.Destinations {
			if destination.Type == "user" {
				return choose(destination)
			}
		}
		return projectTransferDestinationInput{Type: "user"}, nil
	case orgRef != "":
		for _, destination := range offer.Destinations {
			if destination.Type == "organization" && (destination.ID == orgRef || strings.EqualFold(destination.Name, orgRef)) {
				return choose(destination)
			}
		}
		org, err := resolveStorageOrganization(token, orgRef)
		if err != nil {
			return projectTransferDestinationInput{}, err
		}
		for _, destination := range offer.Destinations {
			if destination.Type == "organization" && destination.ID == org.ID {
				return choose(destination)
			}
		}
		return projectTransferDestinationInput{}, fmt.Errorf("organization '%s' cannot receive this offer: you need its owner role, or a role that can create projects and manage billing", org.Name)
	}
	eligible := []projectTransferDestination{}
	for _, destination := range offer.Destinations {
		if destination.Eligible {
			eligible = append(eligible, destination)
		}
	}
	if len(eligible) == 0 {
		return projectTransferDestinationInput{}, errors.New("none of your accounts can receive this offer (see the reasons above)")
	}
	if !rtInteractive() {
		return projectTransferDestinationInput{}, errors.New("invalid arguments: choose where the project goes with --personal or --org <id|slug>")
	}
	_, _ = fmt.Fprintln(w, "Where should the project go?")
	for index, destination := range eligible {
		_, _ = fmt.Fprintf(w, "  %d) %s\n", index+1, projectTransferDestinationLabel(destination))
	}
	for {
		answer, err := projectPromptLine(w, fmt.Sprintf("Destination [1-%d, 0 to cancel]: ", len(eligible)))
		if err != nil {
			return projectTransferDestinationInput{}, err
		}
		choice, convErr := strconv.Atoi(answer)
		if convErr != nil || choice < 0 || choice > len(eligible) {
			_, _ = fmt.Fprintln(w, "Please enter one of the numbers above.")
			continue
		}
		if choice == 0 {
			return projectTransferDestinationInput{}, errors.New("cancelled")
		}
		return choose(eligible[choice-1])
	}
}

func printProjectTransferAcceptancePreview(w io.Writer, preview projectTransferAcceptancePreview) {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "Balance:\t%s (needs %s)\n", formatMicroUSD(preview.BalanceMicroUSD), formatMicroUSD(preview.RequiredBalanceMicroUSD))
	_, _ = fmt.Fprintf(tw, "Running cost:\t%s/hour\n", formatMicroUSD(preview.HourlyBurnMicroUSD))
	for _, plan := range preview.BilledPlans {
		_, _ = fmt.Fprintf(tw, "Billed to you:\t%s %s/%s\n", plan.Label, formatMicroUSD(plan.AmountMicroUSD), plan.Period)
	}
	policy := "credentials of people who keep access stay valid"
	if preview.CredentialPolicy == "rotate" {
		policy = "every project credential is revoked"
	}
	_, _ = fmt.Fprintf(tw, "Credentials:\t%s\n", policy)
	_ = tw.Flush()
	printMoveCounts(w, "Revoked at the handover", preview.Revoked)
	printMoveIssues(w, "Warnings", preview.Warnings)
	printMoveIssues(w, "Blockers", preview.Blockers)
	_, _ = fmt.Fprintln(w)
}

func projectTransferDeclineFlow(args []string) error {
	const usage = "hubfly project transfers decline <token> [--yes]"
	fs := newProjectFlags("transfers decline")
	yes := projectYesFlag(fs)
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return fmt.Errorf("usage: %s", usage)
	}
	offerToken, ok := normalizeProjectTransferToken(positionals[0])
	if !ok {
		return errors.New("invalid transfer token: copy the link or the token from the offer email")
	}
	confirmed, err := storageConfirm(*yes, "Decline every project of this offer?", "decline the offer")
	if err != nil {
		return err
	}
	if !confirmed {
		return errors.New("cancelled")
	}
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}
	if err := declineProjectTransfer(token, offerToken); err != nil {
		return projectMovesAPIError(err, "/projects/transfers/"+offerToken)
	}
	_, _ = fmt.Fprintln(rtOut, "Offer declined. The sender keeps the project(s).")
	return nil
}

func projectTransferCancelFlow(args []string) error {
	const usage = "hubfly project transfers cancel <project> [--yes] [--json]"
	fs := newProjectFlags("transfers cancel")
	yes := projectYesFlag(fs)
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return fmt.Errorf("usage: %s", usage)
	}
	if !*yes && !rtInteractive() {
		return errors.New("invalid arguments: confirmation required; re-run with --yes to cancel the transfer")
	}
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}
	target, err := resolveMoveProject(token, positionals[0])
	if err != nil {
		return err
	}
	confirmed, err := storageConfirm(*yes, fmt.Sprintf("Cancel the transfer of '%s'?", target.Name), "cancel the transfer")
	if err != nil {
		return err
	}
	if !confirmed {
		return errors.New("cancelled")
	}
	view, raw, err := cancelProjectTransfer(token, target.ID)
	if err != nil {
		return projectMovesAPIError(err, "/projects/"+target.ID)
	}
	if *asJSON {
		return printProjectMovesRaw(raw)
	}
	if view.Status == "canceled" {
		_, _ = fmt.Fprintf(rtOut, "Transfer of '%s' canceled.\n", view.ProjectName)
		return nil
	}
	_, _ = fmt.Fprintf(rtOut, "Canceling the transfer of '%s': the owner change is being rolled back (%s).\n", view.ProjectName, projectJobProgress(view.Job))
	_, _ = fmt.Fprintf(rtOut, "Check it with: hubfly project transfers status %s\n", target.ID)
	return nil
}

// ---------------------------------------------------------------------------
// project history
// ---------------------------------------------------------------------------

func projectHistoryFlow(args []string) error {
	const usage = "hubfly project history <project> [--json]"
	fs := newProjectFlags("history")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return fmt.Errorf("usage: %s", usage)
	}
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}
	target, err := resolveMoveProject(token, positionals[0])
	if err != nil {
		return err
	}
	history, raw, err := fetchProjectHistory(token, target.ID)
	if err != nil {
		return projectMovesAPIError(err, "")
	}
	if *asJSON {
		return printProjectMovesRaw(raw)
	}
	if len(history.Entries) == 0 {
		_, _ = fmt.Fprintf(rtOut, "Project '%s' has no transfers or region moves.\n", target.Name)
		return nil
	}
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "WHEN\tKIND\tSTATUS\tWHAT\tBY")
	for _, entry := range history.Entries {
		what := entry.Title
		if entry.Detail != nil && *entry.Detail != "" {
			what += ": " + *entry.Detail
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", storageTime(entry.At), entry.Kind, entry.Status, what, optionalText(entry.Actor))
	}
	return tw.Flush()
}

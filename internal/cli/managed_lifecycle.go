package cli

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

// Lifecycle commands shared by `hubfly db` and `hubfly realtime`: start, stop,
// restart, resize, upgrade, rename, protect and network. service is
// managedServiceDatabase or managedServiceRealtime.

func runManagedLifecycleCommand(service, command string, args []string) error {
	switch command {
	case "start", "stop", "restart":
		return managedPowerFlow(service, command, args)
	case "resize":
		return managedResizeFlow(service, args)
	case "upgrade":
		return managedUpgradeFlow(service, args)
	case "rename":
		return managedRenameFlow(service, args)
	case "protect":
		return managedProtectFlow(service, args)
	case "network":
		return runManagedNetworkGroup(service, args)
	}
	return fmt.Errorf("unknown %s command %q (run: hubfly %s help)", managedGroupFor(service).Command, command, managedGroupFor(service).Command)
}

// Default waits per operation kind: the engine's own deadline plus a margin.
var managedWaitTimeouts = map[string]time.Duration{
	"start":   20 * time.Minute,
	"stop":    15 * time.Minute,
	"restart": 20 * time.Minute,
	"resize":  25 * time.Minute,
	"upgrade": 35 * time.Minute,
	"network": 15 * time.Minute,
}

func parseManagedTimeout(value, kind string) (time.Duration, error) {
	if strings.TrimSpace(value) == "" {
		return managedWaitTimeouts[kind], nil
	}
	seconds, err := parseRealtimeDuration(value)
	if err != nil || seconds <= 0 {
		return 0, fmt.Errorf("invalid --timeout %q", value)
	}
	return time.Duration(seconds) * time.Second, nil
}

func titleNoun(group managedGroup) string {
	return strings.ToUpper(group.Noun[:1]) + group.Noun[1:]
}

// managedAPIError adds a hint to the Dashboard errors a user can act on for
// either managed service. The wrapped *apiError keeps exit codes intact.
func managedAPIError(group managedGroup, name string, err error) error {
	var apiErr *apiError
	if !errors.As(err, &apiErr) {
		return err
	}
	if name == "" {
		name = group.Arg
	}
	hint := ""
	switch apiErr.Code {
	case "MANAGED_INVALID_STATE":
		hint = fmt.Sprintf("Check its status with: hubfly %s info %s (start works from stopped or failed; stop, restart, resize and upgrade need it running).", group.Command, name)
	case "MANAGED_DELETION_PROTECTED":
		hint = fmt.Sprintf("Turn deletion protection off first: hubfly %s protect %s off", group.Command, name)
	case "MANAGED_PRICE_CHANGED":
		hint = "Run the command again to see the new price."
	case "MANAGED_STORAGE_SHRINK_UNSUPPORTED":
		hint = "Storage can be increased but never reduced."
	case "MANAGED_INVALID_SIZE":
		hint = "See the allowed sizes with: hubfly db engines"
		if group.Service == managedServiceRealtime {
			hint = "Realtime servers need at least 512 MiB of memory."
		}
	case "MANAGED_UP_TO_DATE":
		hint = "Nothing to upgrade: it already runs the newest build."
	case "MANAGED_OPERATION_IN_PROGRESS", "MANAGED_JOB_RUNNING":
		hint = fmt.Sprintf("Wait for the running operation to finish (see: hubfly %s info %s).", group.Command, name)
	case "MANAGED_ALLOWLIST_CONFLICT":
		hint = "The allowlist changed in the meantime. Run the command again."
	case "MANAGED_ALLOWLIST_UNAVAILABLE":
		hint = "IP allowlists are not available for this instance yet."
	case "MANAGED_INSTANCE_NOT_RUNNING":
		hint = fmt.Sprintf("Start it first: hubfly %s start %s", group.Command, name)
	case "MANAGED_ENDPOINT_NOT_READY":
		hint = fmt.Sprintf("The endpoint is not ready. Check that public access is on: hubfly %s network %s", group.Command, name)
	case "MANAGED_SERVICES_UNAVAILABLE":
		hint = "Managed services are not available in any region for this account yet."
	case "MANAGED_NAME_TAKEN":
		hint = "Choose another name."
	case "MANAGED_NETWORK_UNCHANGED":
		hint = "Nothing changed."
	}
	if hint == "" {
		if group.Service == managedServiceRealtime {
			return realtimeAPIError(err)
		}
		return err
	}
	return fmt.Errorf("%w\n%s", err, hint)
}

// managedRestartNote describes what clients see during a restart or upgrade.
// Realtime restarts hand connections over in place; realtime upgrades drain
// clients.
func managedRestartNote(group managedGroup, command string) string {
	if group.Service == managedServiceRealtime {
		if command == "upgrade" {
			return "Upgrading drains connected clients: expect about 10-20 s without service. Clients reconnect automatically."
		}
		return "Restarting keeps connections: HubFly hands them over in place (about 1-3 s)."
	}
	return "Apps lose their connection briefly while the database restarts. Most clients reconnect on their own."
}

// ---------------------------------------------------------------------------
// start / stop / restart
// ---------------------------------------------------------------------------

func managedPowerFlow(service, command string, args []string) error {
	group := managedGroupFor(service)
	usage := fmt.Sprintf("hubfly %s %s %s [--project <id|name>] [--no-wait] [--timeout <duration>] [--json]", group.Command, command, group.Arg)
	fs := newRealtimeFlags(command)
	projectRef := fs.String("project", "", "")
	noWait := fs.Bool("no-wait", false, "")
	timeoutText := fs.String("timeout", "", "")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return fmt.Errorf("usage: %s", usage)
	}
	timeout, err := parseManagedTimeout(*timeoutText, command)
	if err != nil {
		return err
	}
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}
	instance, err := resolveManagedInstanceOf(token, positionals[0], *projectRef, service)
	if err != nil {
		return err
	}
	already := (command == "start" && instance.Status == "running") || (command == "stop" && instance.Status == "stopped")
	if already {
		if *asJSON {
			return rtPrintJSON(map[string]any{"instance": instance, "operationId": nil})
		}
		_, _ = fmt.Fprintf(rtOut, "%s '%s' is already %s.\n", titleNoun(group), instance.Name, instance.Status)
		return nil
	}
	switch command {
	case "stop":
		if service == managedServiceRealtime {
			_, _ = fmt.Fprintf(rtErr, "Stopping '%s' drains every client; the server is unavailable until you start it again. A stopped server pays for storage only.\n", instance.Name)
		} else {
			_, _ = fmt.Fprintf(rtErr, "Stopping '%s' disconnects every client; apps that use it fail until you start it again. A stopped database pays for storage only.\n", instance.Name)
		}
	case "restart":
		_, _ = fmt.Fprintln(rtErr, managedRestartNote(group, "restart"))
	}
	operationID, err := postManagedInstanceAction(token, instance, command)
	if err != nil {
		apiErr := managedAPIError(group, instance.Name, err)
		var typed *apiError
		if errors.As(err, &typed) && typed.Code == "MANAGED_INVALID_STATE" {
			return fmt.Errorf("%w\n%s '%s' is %s.", apiErr, titleNoun(group), instance.Name, managedStatusLabel(instance))
		}
		return apiErr
	}
	verbs := map[string][2]string{
		"start":   {"Starting", "running"},
		"stop":    {"Stopping", "stopped"},
		"restart": {"Restarting", "running again"},
	}
	verb := verbs[command]
	if *noWait {
		if *asJSON {
			return rtPrintJSON(map[string]any{"operationId": operationID})
		}
		_, _ = fmt.Fprintf(rtOut, "%s %s '%s' (operation %s).\n", verb[0], group.Noun, instance.Name, operationID)
		return nil
	}
	operation, err := waitForManagedOperation(token, instance, operationID, verb[0]+" "+group.Noun, timeout, *asJSON, false)
	if err != nil {
		return err
	}
	if *asJSON {
		detail, detailErr := fetchManagedInstanceDetailed(token, instance)
		if detailErr != nil {
			return managedAPIError(group, instance.Name, detailErr)
		}
		return rtPrintJSON(map[string]any{"instance": detail, "operationId": operationID, "operation": operation})
	}
	_, _ = fmt.Fprintf(rtOut, "%s '%s' is %s.\n", titleNoun(group), instance.Name, verb[1])
	return nil
}

// ---------------------------------------------------------------------------
// resize
// ---------------------------------------------------------------------------

// managedTargetSize computes the requested size from the current one and the
// flags. sizeID may name a catalog preset; cpu/memory give a custom size.
func managedTargetSize(service string, current rtSize, catalog mlCatalogSizes, sizeID string, cpu float64, memoryMib, storageGib int) (rtSize, error) {
	next := current
	if sizeID != "" {
		ids := []string{}
		found := false
		if service == managedServiceRealtime {
			for _, preset := range catalog.Realtime.Sizes {
				ids = append(ids, preset.ID)
				if strings.EqualFold(preset.ID, sizeID) || strings.EqualFold(preset.Label, sizeID) {
					next.CPU, next.MemoryMib, found = preset.CPU, preset.MemoryMib, true
				}
			}
		} else {
			for _, preset := range catalog.Sizes.Presets {
				ids = append(ids, preset.ID)
				if strings.EqualFold(preset.ID, sizeID) || strings.EqualFold(preset.Label, sizeID) {
					next.CPU, next.MemoryMib, found = preset.CPU, preset.MemoryMib, true
				}
			}
		}
		if !found {
			return rtSize{}, fmt.Errorf("invalid --size %q (available: %s)", sizeID, strings.Join(ids, ", "))
		}
	}
	if cpu > 0 {
		next.CPU, next.MemoryMib = cpu, memoryMib
	}
	if storageGib > 0 {
		if storageGib < current.StorageGib {
			return rtSize{}, fmt.Errorf("invalid --storage %d: storage can grow but never shrink (currently %d GiB)", storageGib, current.StorageGib)
		}
		next.StorageGib = storageGib
	}
	if next == current {
		return rtSize{}, fmt.Errorf("nothing to change: it already has %s and %d GiB storage", formatRealtimeSize(current), current.StorageGib)
	}
	return next, nil
}

func managedResizeFlow(service string, args []string) error {
	group := managedGroupFor(service)
	usage := fmt.Sprintf("hubfly %s resize %s [--size <preset>|--cpu <n> --memory <mib>] [--storage <gib>] [--yes] [--no-wait] [--json]", group.Command, group.Arg)
	if service == managedServiceDatabase {
		usage = "hubfly db resize <db> [--size <preset>|--cpu <n> --memory <mib>] [--storage <gib>] [--restart] [--yes] [--no-wait] [--json]"
	}
	fs := newRealtimeFlags("resize")
	projectRef := fs.String("project", "", "")
	sizeID := fs.String("size", "", "")
	cpu := fs.Float64("cpu", 0, "")
	memory := fs.Int("memory", 0, "")
	storage := fs.Int("storage", 0, "")
	restart := fs.Bool("restart", false, "")
	yes := fs.Bool("yes", false, "")
	fs.BoolVar(yes, "y", false, "")
	noWait := fs.Bool("no-wait", false, "")
	timeoutText := fs.String("timeout", "", "")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return fmt.Errorf("usage: %s", usage)
	}
	if (*cpu > 0) != (*memory > 0) {
		return errors.New("invalid size: pass --cpu and --memory together (or --size <preset>)")
	}
	if *cpu < 0 || *memory < 0 || *storage < 0 {
		return errors.New("invalid size: values must be positive")
	}
	if *cpu > 0 && *sizeID != "" {
		return errors.New("invalid size: use --size or --cpu/--memory, not both")
	}
	if *sizeID == "" && *cpu == 0 && *storage == 0 {
		return fmt.Errorf("invalid arguments: choose a new size with --size, --cpu/--memory or --storage\nusage: %s", usage)
	}
	if *restart && service == managedServiceRealtime {
		return errors.New("invalid arguments: --restart is for databases; realtime servers apply a new size live")
	}
	timeout, err := parseManagedTimeout(*timeoutText, "resize")
	if err != nil {
		return err
	}
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}
	instance, err := resolveManagedInstanceOf(token, positionals[0], *projectRef, service)
	if err != nil {
		return err
	}
	if instance.Status != "running" {
		return fmt.Errorf("%s '%s' is %s; start it before resizing (hubfly %s start %s)", group.Noun, instance.Name, managedStatusLabel(instance), group.Command, instance.Name)
	}
	var catalog mlCatalogSizes
	if *sizeID != "" {
		if catalog, err = fetchManagedCatalogSizes(token); err != nil {
			return err
		}
	}
	next, err := managedTargetSize(service, instance.Size, catalog, *sizeID, *cpu, *memory, *storage)
	if err != nil {
		return err
	}
	price, err := fetchManagedPriceEstimate(token, next)
	if err != nil {
		return managedAPIError(group, instance.Name, err)
	}

	quoteOut := rtOut
	if *asJSON {
		quoteOut = rtErr
	}
	tw := tabwriter.NewWriter(quoteOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "%s:\t%s\n", titleNoun(group), instance.Name)
	_, _ = fmt.Fprintf(tw, "Current:\t%s, %d GiB storage, %s\n", formatRealtimeSize(instance.Size), instance.Size.StorageGib, rtMonthly(instance.Price))
	_, _ = fmt.Fprintf(tw, "New:\t%s, %d GiB storage, %s (%s, billed hourly)\n", formatRealtimeSize(next), next.StorageGib, rtMonthly(price), rtHourly(price))
	_ = tw.Flush()
	if next.StorageGib > instance.Size.StorageGib {
		_, _ = fmt.Fprintln(quoteOut, "Storage can grow but never shrink.")
	}
	if !*yes {
		if !rtInteractive() {
			return fmt.Errorf("invalid arguments: confirmation required; re-run with --yes to accept %s", rtMonthly(price))
		}
		confirmed, promptErr := promptYesNo(fmt.Sprintf("Resize this %s?", group.Noun), false)
		if promptErr != nil {
			return promptErr
		}
		if !confirmed {
			_, _ = fmt.Fprintln(rtOut, "Cancelled.")
			return nil
		}
	}
	body := map[string]any{
		"cpu":               next.CPU,
		"memoryMib":         next.MemoryMib,
		"storageGib":        next.StorageGib,
		"quotedHourlyTotal": price.Hourly.Total,
	}
	if *restart {
		body["restart"] = true
	}
	result, err := resizeManagedInstance(token, instance, body)
	if err != nil {
		return managedAPIError(group, instance.Name, err)
	}
	var operation *rtOperation
	if !*noWait && result.OperationID != "" {
		finished, waitErr := waitForManagedOperation(token, instance, result.OperationID, "Resizing "+group.Noun, timeout, *asJSON, false)
		if waitErr != nil {
			return waitErr
		}
		operation = &finished
	}
	pendingRestart := false
	var detail *mlInstance
	if operation != nil {
		if fetched, detailErr := fetchManagedInstanceDetailed(token, instance); detailErr == nil {
			detail = &fetched
			pendingRestart = fetched.PendingRestart
		}
	}
	if *asJSON {
		return rtPrintJSON(map[string]any{
			"operationId":        result.OperationID,
			"operation":          operation,
			"price":              result.Price,
			"restartRecommended": result.RestartRecommended,
			"instance":           detail,
		})
	}
	if operation == nil {
		_, _ = fmt.Fprintf(rtOut, "Resizing %s '%s' (operation %s).\n", group.Noun, instance.Name, result.OperationID)
	} else {
		_, _ = fmt.Fprintf(rtOut, "Resized %s '%s' to %s, %d GiB storage (%s).\n", group.Noun, instance.Name, formatRealtimeSize(next), next.StorageGib, rtMonthly(result.Price))
	}
	if service == managedServiceRealtime {
		_, _ = fmt.Fprintln(rtOut, "Realtime servers apply a new size live.")
	}
	if service == managedServiceDatabase && result.RestartRecommended && !*restart {
		if operation == nil || pendingRestart {
			_, _ = fmt.Fprintf(rtOut, "The database may need a restart to use the new memory settings (shown as a pending restart in: hubfly db info %s).\n", instance.Name)
			_, _ = fmt.Fprintf(rtOut, "Restart when convenient: hubfly db restart %s\n", instance.Name)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// upgrade / rename / protect
// ---------------------------------------------------------------------------

func managedUpgradeFlow(service string, args []string) error {
	group := managedGroupFor(service)
	usage := fmt.Sprintf("hubfly %s upgrade %s [--yes] [--no-wait] [--json]", group.Command, group.Arg)
	fs := newRealtimeFlags("upgrade")
	projectRef := fs.String("project", "", "")
	yes := fs.Bool("yes", false, "")
	fs.BoolVar(yes, "y", false, "")
	noWait := fs.Bool("no-wait", false, "")
	timeoutText := fs.String("timeout", "", "")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return fmt.Errorf("usage: %s", usage)
	}
	timeout, err := parseManagedTimeout(*timeoutText, "upgrade")
	if err != nil {
		return err
	}
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}
	instance, err := resolveManagedInstanceOf(token, positionals[0], *projectRef, service)
	if err != nil {
		return err
	}
	detail, err := fetchManagedInstanceDetailed(token, instance)
	if err != nil {
		return managedAPIError(group, instance.Name, err)
	}
	if detail.UpdateAvailable == nil {
		if *asJSON {
			return rtPrintJSON(map[string]any{"operationId": nil, "upToDate": true})
		}
		_, _ = fmt.Fprintf(rtOut, "%s '%s' is up to date (%s).\n", titleNoun(group), instance.Name, detail.VersionLabel)
		return nil
	}
	if instance.Status != "running" {
		return fmt.Errorf("%s '%s' is %s; start it before upgrading (hubfly %s start %s)", group.Noun, instance.Name, managedStatusLabel(instance), group.Command, instance.Name)
	}
	if !*yes {
		if !rtInteractive() {
			return errors.New("invalid arguments: confirmation required; re-run with --yes to upgrade")
		}
		_, _ = fmt.Fprintf(rtOut, "Upgrading '%s' from %s to %s. %s\n", instance.Name, detail.VersionLabel, detail.UpdateAvailable.VersionLabel, managedRestartNote(group, "upgrade"))
		confirmed, promptErr := promptYesNo(fmt.Sprintf("Upgrade this %s?", group.Noun), false)
		if promptErr != nil {
			return promptErr
		}
		if !confirmed {
			_, _ = fmt.Fprintln(rtOut, "Cancelled.")
			return nil
		}
	}
	operationID, err := postManagedInstanceAction(token, instance, "upgrade")
	if err != nil {
		return managedAPIError(group, instance.Name, err)
	}
	if *noWait {
		if *asJSON {
			return rtPrintJSON(map[string]any{"operationId": operationID})
		}
		_, _ = fmt.Fprintf(rtOut, "Upgrading %s '%s' (operation %s).\n", group.Noun, instance.Name, operationID)
		return nil
	}
	operation, err := waitForManagedOperation(token, instance, operationID, "Upgrading "+group.Noun, timeout, *asJSON, false)
	if err != nil {
		return err
	}
	if *asJSON {
		return rtPrintJSON(map[string]any{"operationId": operationID, "operation": operation})
	}
	_, _ = fmt.Fprintf(rtOut, "%s '%s' now runs %s.\n", titleNoun(group), instance.Name, detail.UpdateAvailable.VersionLabel)
	return nil
}

func managedRenameFlow(service string, args []string) error {
	group := managedGroupFor(service)
	usage := fmt.Sprintf("hubfly %s rename %s <new-name> [--project <id|name>] [--json]", group.Command, group.Arg)
	fs := newRealtimeFlags("rename")
	projectRef := fs.String("project", "", "")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 2 || strings.TrimSpace(positionals[1]) == "" {
		return fmt.Errorf("usage: %s", usage)
	}
	newName := strings.TrimSpace(positionals[1])
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}
	instance, err := resolveManagedInstanceOf(token, positionals[0], *projectRef, service)
	if err != nil {
		return err
	}
	if instance.Name == newName {
		_, _ = fmt.Fprintf(rtOut, "%s '%s' already has that name.\n", titleNoun(group), instance.Name)
		return nil
	}
	updated, err := renameManagedInstance(token, instance, newName)
	if err != nil {
		return managedAPIError(group, instance.Name, err)
	}
	if *asJSON {
		updated.ProjectID, updated.ProjectName = instance.ProjectID, instance.ProjectName
		return rtPrintJSON(updated)
	}
	_, _ = fmt.Fprintf(rtOut, "Renamed %s '%s' to '%s'. Its id and connection details do not change.\n", group.Noun, instance.Name, updated.Name)
	return nil
}

func parseOnOff(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "on", "true", "yes", "enable", "enabled":
		return true, nil
	case "off", "false", "no", "disable", "disabled":
		return false, nil
	}
	return false, fmt.Errorf("expected on or off, got %q", value)
}

func managedProtectFlow(service string, args []string) error {
	group := managedGroupFor(service)
	usage := fmt.Sprintf("hubfly %s protect %s on|off [--project <id|name>] [--json]", group.Command, group.Arg)
	fs := newRealtimeFlags("protect")
	projectRef := fs.String("project", "", "")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 2 {
		return fmt.Errorf("usage: %s", usage)
	}
	enabled, err := parseOnOff(positionals[1])
	if err != nil {
		return fmt.Errorf("invalid arguments: %v\nusage: %s", err, usage)
	}
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}
	instance, err := resolveManagedInstanceOf(token, positionals[0], *projectRef, service)
	if err != nil {
		return err
	}
	protected, err := setManagedDeletionProtection(token, instance, enabled)
	if err != nil {
		return managedAPIError(group, instance.Name, err)
	}
	if *asJSON {
		return rtPrintJSON(map[string]any{"deletionProtection": protected})
	}
	if protected {
		_, _ = fmt.Fprintf(rtOut, "Deletion protection is on for %s '%s'. It cannot be deleted until you run: hubfly %s protect %s off\n", group.Noun, instance.Name, group.Command, instance.Name)
	} else {
		_, _ = fmt.Fprintf(rtOut, "Deletion protection is off for %s '%s'.\n", group.Noun, instance.Name)
	}
	return nil
}

// ---------------------------------------------------------------------------
// network
// ---------------------------------------------------------------------------

func runManagedNetworkGroup(service string, args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "show", "list", "ls":
			return managedNetworkShowFlow(service, args[1:])
		case "public":
			return managedNetworkPublicFlow(service, args[1:])
		case "allow", "add":
			return managedNetworkAllowFlow(service, args[1:])
		case "remove", "rm", "delete":
			return managedNetworkRemoveFlow(service, args[1:])
		}
	}
	return managedNetworkShowFlow(service, args)
}

func managedNetworkShowFlow(service string, args []string) error {
	group := managedGroupFor(service)
	usage := fmt.Sprintf("hubfly %s network %s [--project <id|name>] [--json]", group.Command, group.Arg)
	fs := newRealtimeFlags("network")
	projectRef := fs.String("project", "", "")
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
	instance, err := resolveManagedInstanceOf(token, positionals[0], *projectRef, service)
	if err != nil {
		return err
	}
	network, err := fetchManagedNetwork(token, instance)
	if err != nil {
		return managedAPIError(group, instance.Name, err)
	}
	if *asJSON {
		return rtPrintJSON(network)
	}
	public := "off (no public endpoint)"
	if network.PublicAccess {
		public = "on"
		if endpoint := managedEndpointText(network.Endpoint.Host, network.Endpoint.Port); endpoint != "" {
			public += " (" + endpoint + ")"
		}
	}
	allowlist := "not available for this instance yet"
	if network.AllowlistSupported {
		if len(network.Allowlist) == 0 {
			allowlist = "empty: open to all addresses"
		} else {
			allowlist = fmt.Sprintf("%d of %d entries", len(network.Allowlist), network.MaxEntries)
		}
	}
	tls := "optional"
	if network.TLSRequired {
		tls = "required"
	}
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "%s:\t%s\n", titleNoun(group), instance.Name)
	_, _ = fmt.Fprintf(tw, "Public access:\t%s\n", public)
	_, _ = fmt.Fprintf(tw, "IP allowlist:\t%s\n", allowlist)
	_, _ = fmt.Fprintf(tw, "TLS:\t%s\n", tls)
	_ = tw.Flush()
	if network.AllowlistScope == "server_api" {
		_, _ = fmt.Fprintln(rtOut, "The allowlist guards the server HTTP API; WebSocket clients are never IP-restricted.")
	}
	if len(network.Allowlist) > 0 {
		_, _ = fmt.Fprintln(rtOut)
		tw = tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "CIDR\tLABEL\tADDED")
		for _, entry := range network.Allowlist {
			label := "-"
			if entry.Label != nil && *entry.Label != "" {
				label = *entry.Label
			}
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", entry.CIDR, label, entry.CreatedAt)
		}
		_ = tw.Flush()
	}
	return nil
}

func managedEndpointText(host string, port *int) string {
	if host == "" {
		return ""
	}
	if port != nil && *port > 0 {
		return net.JoinHostPort(host, strconv.Itoa(*port))
	}
	return host
}

func managedNetworkPublicFlow(service string, args []string) error {
	group := managedGroupFor(service)
	usage := fmt.Sprintf("hubfly %s network public %s on|off [--yes] [--no-wait] [--project <id|name>]", group.Command, group.Arg)
	fs := newRealtimeFlags("network public")
	projectRef := fs.String("project", "", "")
	yes := fs.Bool("yes", false, "")
	fs.BoolVar(yes, "y", false, "")
	noWait := fs.Bool("no-wait", false, "")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 2 {
		return fmt.Errorf("usage: %s", usage)
	}
	enabled, err := parseOnOff(positionals[1])
	if err != nil {
		return fmt.Errorf("invalid arguments: %v\nusage: %s", err, usage)
	}
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}
	instance, err := resolveManagedInstanceOf(token, positionals[0], *projectRef, service)
	if err != nil {
		return err
	}
	network, err := fetchManagedNetwork(token, instance)
	if err != nil {
		return managedAPIError(group, instance.Name, err)
	}
	if !network.PublicAccessEditable {
		if service == managedServiceRealtime {
			return errors.New("realtime servers are always public (WebSocket clients connect over the internet); use the allowlist to restrict the server HTTP API")
		}
		return fmt.Errorf("public access of %s '%s' cannot be changed", group.Noun, instance.Name)
	}
	if network.PublicAccess == enabled {
		state := map[bool]string{true: "on", false: "off"}[enabled]
		if *asJSON {
			return rtPrintJSON(map[string]any{"publicAccess": enabled, "operationId": nil})
		}
		_, _ = fmt.Fprintf(rtOut, "Public access is already %s for %s '%s'.\n", state, group.Noun, instance.Name)
		return nil
	}
	if !enabled && !*yes {
		if !rtInteractive() {
			return errors.New("invalid arguments: confirmation required; re-run with --yes to turn public access off")
		}
		_, _ = fmt.Fprintf(rtOut, "Turning public access off removes the public endpoint of '%s'. External clients, and apps whose connection string uses the public host (including HubFly app bindings), lose access. The allowlist is kept for when you turn it back on.\n", instance.Name)
		confirmed, promptErr := promptYesNo("Turn public access off?", false)
		if promptErr != nil {
			return promptErr
		}
		if !confirmed {
			_, _ = fmt.Fprintln(rtOut, "Cancelled.")
			return nil
		}
	}
	result, err := updateManagedPublicAccess(token, instance, enabled)
	if err != nil {
		return managedAPIError(group, instance.Name, err)
	}
	var operation *rtOperation
	if !*noWait && result.OperationID != "" {
		finished, waitErr := waitForManagedOperation(token, instance, result.OperationID, "Updating network", managedWaitTimeouts["network"], *asJSON, false)
		if waitErr != nil {
			return waitErr
		}
		operation = &finished
	}
	if *asJSON {
		return rtPrintJSON(map[string]any{"publicAccess": result.PublicAccess, "operationId": result.OperationID, "operation": operation})
	}
	state := map[bool]string{true: "on", false: "off"}[result.PublicAccess]
	if operation == nil {
		_, _ = fmt.Fprintf(rtOut, "Turning public access %s for %s '%s' (operation %s).\n", state, group.Noun, instance.Name, result.OperationID)
		return nil
	}
	_, _ = fmt.Fprintf(rtOut, "Public access is %s for %s '%s'.\n", state, group.Noun, instance.Name)
	return nil
}

// normalizeAllowlistCIDR accepts an IP address or CIDR range and returns the
// canonical network form the Dashboard stores (host bits cleared; a bare IPv4
// address becomes /32, a bare IPv6 address /128).
func normalizeAllowlistCIDR(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("empty IP address or CIDR range")
	}
	if !strings.Contains(value, "/") {
		ip := net.ParseIP(value)
		if ip == nil {
			return "", fmt.Errorf("invalid IP address or CIDR range %q", value)
		}
		if ip.To4() != nil {
			return ip.To4().String() + "/32", nil
		}
		return ip.String() + "/128", nil
	}
	_, network, err := net.ParseCIDR(value)
	if err != nil {
		return "", fmt.Errorf("invalid IP address or CIDR range %q", value)
	}
	return network.String(), nil
}

// clientAllowlistCIDR asks the Dashboard which address it sees for this
// machine and returns it as a single-address range.
func clientAllowlistCIDR(token string) (string, error) {
	clientIP, err := fetchManagedClientIP(token)
	if err != nil {
		return "", err
	}
	if clientIP.IP == nil || strings.TrimSpace(*clientIP.IP) == "" {
		return "", errors.New("HubFly could not detect your public IP address; pass it explicitly (for example 203.0.113.7)")
	}
	return normalizeAllowlistCIDR(*clientIP.IP)
}

func allowlistInputs(entries []mlAllowlistEntry) []mlAllowlistInput {
	inputs := make([]mlAllowlistInput, 0, len(entries))
	for _, entry := range entries {
		input := mlAllowlistInput{CIDR: entry.CIDR}
		if entry.Label != nil {
			input.Label = *entry.Label
		}
		inputs = append(inputs, input)
	}
	return inputs
}

// applyAllowlistChange reads the allowlist, applies change and saves it with
// the revision it read. A concurrent edit (revision conflict) is retried once.
// change returns the new entries, or nil entries with a message when there is
// nothing to save.
func applyAllowlistChange(token string, group managedGroup, instance rtInstance, change func(network mlNetwork) ([]mlAllowlistInput, string, error)) (mlNetwork, *mlAllowlistResult, string, error) {
	for attempt := 0; ; attempt++ {
		network, err := fetchManagedNetwork(token, instance)
		if err != nil {
			return network, nil, "", managedAPIError(group, instance.Name, err)
		}
		if !network.AllowlistSupported {
			return network, nil, "", fmt.Errorf("IP allowlists are not available for %s '%s' yet", group.Noun, instance.Name)
		}
		entries, message, err := change(network)
		if err != nil || entries == nil {
			return network, nil, message, err
		}
		result, err := updateManagedAllowlist(token, instance, entries, network.Revision)
		if err != nil {
			var apiErr *apiError
			if attempt == 0 && errors.As(err, &apiErr) && apiErr.Code == "MANAGED_ALLOWLIST_CONFLICT" {
				debugf("allowlist changed concurrently; retrying once")
				continue
			}
			return network, nil, "", managedAPIError(group, instance.Name, err)
		}
		return network, &result, message, nil
	}
}

func managedNetworkAllowFlow(service string, args []string) error {
	group := managedGroupFor(service)
	usage := fmt.Sprintf("hubfly %s network allow %s (<cidr>|--my-ip) [--label <text>] [--no-wait] [--project <id|name>]", group.Command, group.Arg)
	fs := newRealtimeFlags("network allow")
	projectRef := fs.String("project", "", "")
	myIP := fs.Bool("my-ip", false, "")
	label := fs.String("label", "", "")
	noWait := fs.Bool("no-wait", false, "")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) < 1 || len(positionals) > 2 || (len(positionals) == 2) == *myIP {
		return fmt.Errorf("usage: %s", usage)
	}
	*label = strings.TrimSpace(*label)
	if len(*label) > 64 {
		return errors.New("invalid --label: at most 64 characters")
	}
	cidr := ""
	if !*myIP {
		if cidr, err = normalizeAllowlistCIDR(positionals[1]); err != nil {
			return fmt.Errorf("invalid arguments: %v", err)
		}
		if strings.Contains(positionals[1], "/") && cidr != strings.TrimSpace(positionals[1]) {
			_, _ = fmt.Fprintf(rtErr, "Note: %s is stored as %s (host bits cleared).\n", strings.TrimSpace(positionals[1]), cidr)
		}
	}
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}
	instance, err := resolveManagedInstanceOf(token, positionals[0], *projectRef, service)
	if err != nil {
		return err
	}
	if *myIP {
		if cidr, err = clientAllowlistCIDR(token); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(rtErr, "Your IP address as HubFly sees it: %s\n", cidr)
	}
	wasEmpty := false
	network, result, message, err := applyAllowlistChange(token, group, instance, func(network mlNetwork) ([]mlAllowlistInput, string, error) {
		entries := allowlistInputs(network.Allowlist)
		wasEmpty = len(entries) == 0
		for index, entry := range entries {
			if entry.CIDR != cidr {
				continue
			}
			if *label == "" || entry.Label == *label {
				return nil, fmt.Sprintf("%s is already in the allowlist of %s '%s'.", cidr, group.Noun, instance.Name), nil
			}
			entries[index].Label = *label
			return entries, fmt.Sprintf("Updated the label of %s on %s '%s'.", cidr, group.Noun, instance.Name), nil
		}
		if network.MaxEntries > 0 && len(entries) >= network.MaxEntries {
			return nil, "", fmt.Errorf("the allowlist is full (%d entries); remove one first with: hubfly %s network remove %s <cidr>", network.MaxEntries, group.Command, instance.Name)
		}
		entries = append(entries, mlAllowlistInput{CIDR: cidr, Label: *label})
		return entries, fmt.Sprintf("Allowed %s on %s '%s' (%d entries).", cidr, group.Noun, instance.Name, len(entries)), nil
	})
	if err != nil {
		return err
	}
	return finishAllowlistChange(token, group, instance, network, result, message, wasEmpty, *noWait, *asJSON)
}

func managedNetworkRemoveFlow(service string, args []string) error {
	group := managedGroupFor(service)
	usage := fmt.Sprintf("hubfly %s network remove %s <cidr> [--no-wait] [--project <id|name>]", group.Command, group.Arg)
	fs := newRealtimeFlags("network remove")
	projectRef := fs.String("project", "", "")
	noWait := fs.Bool("no-wait", false, "")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 2 {
		return fmt.Errorf("usage: %s", usage)
	}
	cidr, err := normalizeAllowlistCIDR(positionals[1])
	if err != nil {
		return fmt.Errorf("invalid arguments: %v", err)
	}
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}
	instance, err := resolveManagedInstanceOf(token, positionals[0], *projectRef, service)
	if err != nil {
		return err
	}
	network, result, message, err := applyAllowlistChange(token, group, instance, func(network mlNetwork) ([]mlAllowlistInput, string, error) {
		entries := allowlistInputs(network.Allowlist)
		kept := make([]mlAllowlistInput, 0, len(entries))
		for _, entry := range entries {
			if entry.CIDR != cidr {
				kept = append(kept, entry)
			}
		}
		if len(kept) == len(entries) {
			return nil, "", fmt.Errorf("%s is not in the allowlist of %s '%s' (see: hubfly %s network %s)", cidr, group.Noun, instance.Name, group.Command, instance.Name)
		}
		message := fmt.Sprintf("Removed %s from %s '%s' (%d entries left).", cidr, group.Noun, instance.Name, len(kept))
		if len(kept) == 0 {
			message += " The allowlist is empty: any address with valid credentials can connect again."
			if group.Service == managedServiceRealtime {
				message = fmt.Sprintf("Removed %s from %s '%s'. The allowlist is empty: the server HTTP API accepts requests from any address again.", cidr, group.Noun, instance.Name)
			}
		}
		return kept, message, nil
	})
	if err != nil {
		return err
	}
	return finishAllowlistChange(token, group, instance, network, result, message, false, *noWait, *asJSON)
}

func finishAllowlistChange(token string, group managedGroup, instance rtInstance, network mlNetwork, result *mlAllowlistResult, message string, wasEmpty, noWait, asJSON bool) error {
	if result == nil {
		if asJSON {
			return rtPrintJSON(map[string]any{"changed": false, "revision": network.Revision, "entries": len(network.Allowlist)})
		}
		_, _ = fmt.Fprintln(rtOut, message)
		return nil
	}
	var operation *rtOperation
	if !noWait && result.OperationID != "" {
		finished, err := waitForManagedOperation(token, instance, result.OperationID, "Applying the allowlist", managedWaitTimeouts["network"], asJSON, false)
		if err != nil {
			return err
		}
		operation = &finished
	}
	if asJSON {
		return rtPrintJSON(map[string]any{"changed": true, "operationId": result.OperationID, "revision": result.Revision, "entries": result.Entries, "operation": operation})
	}
	_, _ = fmt.Fprintln(rtOut, message)
	if operation == nil {
		_, _ = fmt.Fprintf(rtOut, "Applying the change (operation %s).\n", result.OperationID)
	}
	if wasEmpty {
		if group.Service == managedServiceRealtime {
			_, _ = fmt.Fprintln(rtOut, "The allowlist was empty: from now on only listed addresses can call the server HTTP API. WebSocket clients are never IP-restricted.")
		} else {
			_, _ = fmt.Fprintln(rtOut, "The allowlist was empty (open to all): from now on only listed addresses can connect through the public endpoint.")
		}
	}
	if !network.PublicAccess && group.Service == managedServiceDatabase {
		_, _ = fmt.Fprintf(rtOut, "Public access is off, so the allowlist applies once you turn it on: hubfly db network public %s on\n", instance.Name)
	}
	return nil
}

package cli

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

// Core `hubfly db` commands: list, engines, info, create, delete and bind.
// url, env, credentials, connect and shell live in db_core_connect.go.

var dbEngineIDs = []string{"postgresql", "mysql", "libsql"}

// normalizeDBEngine maps the engine names people type to catalog ids.
func normalizeDBEngine(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "postgresql", "postgres", "pg", "psql":
		return "postgresql", nil
	case "mysql":
		return "mysql", nil
	case "libsql", "sqlite", "sqlite3":
		return "libsql", nil
	}
	return "", fmt.Errorf("invalid --engine %q (use postgresql, mysql or libsql)", value)
}

// dbAPIError adds database hints to Dashboard errors.
func dbAPIError(name string, err error) error {
	var apiErr *apiError
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case "MANAGED_FEATURE_UNSUPPORTED":
			return fmt.Errorf("%w\nThis database engine does not support that.", err)
		case "MANAGED_ENGINE_NOT_READY":
			return fmt.Errorf("%w\nThe database is still starting. Try again in a moment.", err)
		case "MANAGED_BINDING_TARGET_UNSUPPORTED":
			return fmt.Errorf("%w\nDatabases can be bound to Cell projects only.", err)
		case "MANAGED_INVALID_DATABASE_NAME", "MANAGED_INVALID_ENV_KEY", "MANAGED_INVALID_NAME":
			return err
		}
	}
	return managedAPIError(managedGroupFor(managedServiceDatabase), name, err)
}

// listDBInstances returns the databases across managed projects (or of one
// project) with the database fields of the list endpoint.
func listDBInstances(token, projectRef string) ([]mlInstance, error) {
	projects, err := fetchManagedProjects(token, projectRef)
	if err != nil {
		return nil, err
	}
	result := []mlInstance{}
	for _, item := range projects {
		instances, listErr := fetchManagedInstancesDetailed(token, item.ID)
		if listErr != nil {
			if projectRef != "" {
				return nil, listErr
			}
			debugf("skipping project %s: %v", item.ID, listErr)
			continue
		}
		for _, instance := range instances {
			if instance.Service != managedServiceDatabase {
				continue
			}
			instance.ProjectID, instance.ProjectName = item.ID, item.Name
			result = append(result, instance)
		}
	}
	return result, nil
}

func formatDBSize(size rtSize) string {
	return fmt.Sprintf("%s, %d GiB", formatRealtimeSize(size), size.StorageGib)
}

func onOff(value bool) string {
	if value {
		return "on"
	}
	return "off"
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// ---------------------------------------------------------------------------
// list / engines / info
// ---------------------------------------------------------------------------

func dbListFlow(args []string) error {
	const usage = "hubfly db list [--project <id|name>] [--json]"
	fs := newRealtimeFlags("db list")
	projectRef := fs.String("project", "", "")
	fs.StringVar(projectRef, "p", "", "")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) > 1 {
		return fmt.Errorf("unexpected argument: %s\nusage: %s", positionals[1], usage)
	}
	if len(positionals) == 1 && *projectRef == "" {
		*projectRef = positionals[0]
	}
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}
	instances, err := listDBInstances(token, *projectRef)
	if err != nil {
		return err
	}
	if *asJSON {
		return rtPrintJSON(instances)
	}
	if len(instances) == 0 {
		_, _ = fmt.Fprintln(rtOut, "No databases found. Create one with: hubfly db create <name>")
		return nil
	}
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAME\tENGINE\tSTATUS\tSIZE\tPRICE\tPROJECT\tID")
	for _, instance := range instances {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			instance.Name, instance.VersionLabel, managedStatusLabel(instance.rtInstance), formatDBSize(instance.Size),
			rtMonthly(instance.Price), instance.ProjectName, instance.ID)
	}
	return tw.Flush()
}

func dbEnginesFlow(args []string) error {
	const usage = "hubfly db engines [--json]"
	fs := newRealtimeFlags("db engines")
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
	catalog, err := fetchDBCatalog(token)
	if err != nil {
		return err
	}
	storage := int(catalog.Sizes.StorageGib.Default)
	if storage <= 0 {
		storage = 10
	}
	prices := map[string]rtPrice{}
	for _, preset := range catalog.Sizes.Presets {
		price, priceErr := fetchManagedPriceEstimate(token, rtSize{CPU: preset.CPU, MemoryMib: preset.MemoryMib, StorageGib: storage})
		if priceErr != nil {
			debugf("price estimate for %s failed: %v", preset.ID, priceErr)
			continue
		}
		prices[preset.ID] = price
	}
	if *asJSON {
		return rtPrintJSON(map[string]any{"catalog": catalog, "presetPrices": prices, "priceStorageGib": storage})
	}
	if !catalog.databasesAvailable() {
		_, _ = fmt.Fprintln(rtOut, "Managed Databases are not available in any region for this account yet.")
		return nil
	}
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ENGINE\tVERSION\tLABEL\tSTATUS\tMIN MEMORY\tFROM")
	for _, engine := range catalog.Engines {
		for _, version := range engine.Versions {
			label := version.Label
			var tags []string
			if version.IsDefault {
				tags = append(tags, "default")
			}
			if version.LTS && !strings.Contains(label, "LTS") {
				tags = append(tags, "LTS")
			}
			if len(tags) > 0 {
				label += " (" + strings.Join(tags, ", ") + ")"
			}
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d MiB\t%s\n", engine.ID, version.ID, label, version.Status, engine.MinMemoryMib, rtMonthly(engine.StartingPrice))
		}
	}
	_ = tw.Flush()

	_, _ = fmt.Fprintln(rtOut)
	tw = tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "SIZE\tVCPU\tMEMORY\tPRICE (%d GiB storage)\tFOR\n", storage)
	for _, preset := range catalog.Sizes.Presets {
		price := "-"
		if quote, ok := prices[preset.ID]; ok {
			price = rtMonthly(quote)
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%d MiB\t%s\t%s\n", preset.ID, strconv.FormatFloat(preset.CPU, 'f', -1, 64), preset.MemoryMib, price, preset.Description)
	}
	_ = tw.Flush()
	_, _ = fmt.Fprintln(rtOut)
	_, _ = fmt.Fprintf(rtOut, "Custom sizes: %s-%s vCPU in steps of %s, %s-%s MiB memory in steps of %s (MySQL needs at least 1024 MiB, PostgreSQL 512 MiB).\n",
		strconv.FormatFloat(catalog.Sizes.CPU.Min, 'f', -1, 64), strconv.FormatFloat(catalog.Sizes.CPU.Max, 'f', -1, 64), strconv.FormatFloat(catalog.Sizes.CPU.Step, 'f', -1, 64),
		strconv.FormatFloat(catalog.Sizes.MemoryMib.Min, 'f', -1, 64), strconv.FormatFloat(catalog.Sizes.MemoryMib.Max, 'f', -1, 64), strconv.FormatFloat(catalog.Sizes.MemoryMib.Step, 'f', -1, 64))
	_, _ = fmt.Fprintf(rtOut, "Storage: %d-%d GiB (default %d), can grow but never shrink.\n", int(catalog.Sizes.StorageGib.Min), int(catalog.Sizes.StorageGib.Max), storage)
	if catalog.MaxInstancesPerAccount > 0 {
		_, _ = fmt.Fprintf(rtOut, "Up to %d managed instances per account. Billed hourly; the quote shown by hubfly db create is authoritative.\n", catalog.MaxInstancesPerAccount)
	}
	return nil
}

func dbInfoFlow(args []string) error {
	const usage = "hubfly db info <db> [--project <id|name>] [--json]"
	fs := newRealtimeFlags("db info")
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
	instance, err := resolveDatabaseInstance(token, positionals[0], *projectRef)
	if err != nil {
		return err
	}
	detail, err := fetchManagedInstanceDetailed(token, instance)
	if err != nil {
		return dbAPIError(instance.Name, err)
	}
	connection, err := fetchDBConnection(token, instance)
	if err != nil {
		return dbAPIError(instance.Name, err)
	}
	if *asJSON {
		return rtPrintJSON(map[string]any{"instance": detail, "connection": connection})
	}
	name := detail.Name
	status := managedStatusLabel(detail.rtInstance)
	if detail.StatusReason != nil && *detail.StatusReason != "" {
		status += " (" + *detail.StatusReason + ")"
	}
	engine := detail.VersionLabel
	if detail.EngineVersion != nil && *detail.EngineVersion != "" {
		engine += " (" + *detail.EngineVersion + ")"
	}
	if detail.UpdateAvailable != nil {
		engine += fmt.Sprintf("; %s is available (hubfly db upgrade %s)", detail.UpdateAvailable.VersionLabel, name)
	}
	storage := fmt.Sprintf("%d GiB", detail.Size.StorageGib)
	if detail.StorageUsedBytes != nil {
		storage = formatBytes(*detail.StorageUsedBytes) + " used of " + storage
	}
	rows := [][2]string{
		{"Database", name},
		{"ID", detail.ID},
		{"Project", fmt.Sprintf("%s (%s)", detail.ProjectName, detail.ProjectID)},
		{"Engine", engine},
		{"Status", status},
		{"Size", formatRealtimeSize(detail.Size)},
		{"Storage", storage},
		{"Price", rtMonthly(detail.Price) + " (" + rtHourly(detail.Price) + ")"},
	}
	endpoint := managedEndpointText(connection.Host, connection.Port)
	switch {
	case !connection.PublicAccess:
		endpoint = "none: public access is off (hubfly db network public " + name + " on)"
	case !connection.Ready || endpoint == "":
		endpoint = "being prepared"
	}
	if connection.Engine == "libsql" {
		rows = append(rows, [2]string{"URL", firstNonEmpty(derefString(connection.LibsqlURL), endpoint)})
		if url := derefString(connection.HTTPURL); url != "" {
			rows = append(rows, [2]string{"HTTP URL", url})
		}
		rows = append(rows, [2]string{"Auth", fmt.Sprintf("tokens (hubfly db tokens create %s <name>)", name)})
	} else {
		rows = append(rows, [2]string{"Host", endpoint})
		rows = append(rows, [2]string{"Default database", firstNonEmpty(derefString(connection.Database), "-")})
		rows = append(rows, [2]string{"Admin user", firstNonEmpty(derefString(connection.Username), "-")})
	}
	tls := "required"
	switch connection.TLS.Verification {
	case "self_signed":
		tls += fmt.Sprintf(" (per-instance certificate; for verify-full: hubfly db cert %s)", name)
	case "public_ca":
		tls += " (publicly trusted certificate)"
	}
	rows = append(rows, [2]string{"TLS", tls})
	public := onOff(detail.PublicAccess)
	if detail.AllowlistCount > 0 {
		public += fmt.Sprintf(", %d allowlist entries", detail.AllowlistCount)
	} else if detail.PublicAccess {
		public += ", open to all addresses"
	}
	rows = append(rows, [2]string{"Public access", public})
	rows = append(rows, [2]string{"Deletion protection", onOff(detail.DeletionProtection)})
	if detail.PendingRestart {
		rows = append(rows, [2]string{"Pending restart", "yes: settings apply after hubfly db restart " + name})
	}
	rows = append(rows, [2]string{"Backups", "automatic backups are coming soon; export with: hubfly db export " + name})
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	for _, row := range rows {
		_, _ = fmt.Fprintf(tw, "%s:\t%s\n", row[0], row[1])
	}
	return tw.Flush()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// create
// ---------------------------------------------------------------------------

type dbCreateOptions struct {
	Name         string
	Engine       string
	Version      string
	SizeID       string
	CPU          float64
	MemoryMib    int
	StorageGib   int
	ProjectRef   string
	ProjectName  string
	DatabaseName string
	AdminUser    string
	Private      bool
	Allow        rtList
	AllowMyIP    bool
	Yes          bool
	NoWait       bool
	JSON         bool
	Timeout      time.Duration
}

const dbCreateUsage = "hubfly db create [<name>] [--engine postgresql|mysql|libsql] [--version <v>] [--size <preset>|--cpu <n> --memory <mib>] [--storage <gib>] [--project <id|name|new>] [--project-name <name>] [--database <name>] [--private] [--allow <cidr>]... [--allow-my-ip] [--yes] [--no-wait] [--timeout <duration>] [--json]"

func parseDBCreateOptions(args []string) (dbCreateOptions, error) {
	opts := dbCreateOptions{Allow: rtList{split: true}}
	fs := newRealtimeFlags("db create")
	fs.StringVar(&opts.Name, "name", "", "")
	fs.StringVar(&opts.Engine, "engine", "", "")
	fs.StringVar(&opts.Version, "version", "", "")
	fs.StringVar(&opts.SizeID, "size", "", "")
	fs.Float64Var(&opts.CPU, "cpu", 0, "")
	fs.IntVar(&opts.MemoryMib, "memory", 0, "")
	fs.IntVar(&opts.StorageGib, "storage", 0, "")
	fs.StringVar(&opts.ProjectRef, "project", "", "")
	fs.StringVar(&opts.ProjectRef, "p", "", "")
	fs.StringVar(&opts.ProjectName, "project-name", "", "")
	fs.StringVar(&opts.DatabaseName, "database", "", "")
	fs.StringVar(&opts.AdminUser, "admin-user", "", "")
	fs.BoolVar(&opts.Private, "private", false, "")
	fs.Var(&opts.Allow, "allow", "")
	fs.BoolVar(&opts.AllowMyIP, "allow-my-ip", false, "")
	fs.BoolVar(&opts.Yes, "yes", false, "")
	fs.BoolVar(&opts.Yes, "y", false, "")
	fs.BoolVar(&opts.NoWait, "no-wait", false, "")
	fs.BoolVar(&opts.JSON, "json", false, "")
	timeout := fs.String("timeout", "35m", "")
	positionals, err := parseRealtimeArgs(fs, args, dbCreateUsage)
	if err != nil {
		return opts, err
	}
	if len(positionals) > 1 {
		return opts, fmt.Errorf("unexpected argument: %s\nusage: %s", positionals[1], dbCreateUsage)
	}
	if len(positionals) == 1 {
		if opts.Name != "" && opts.Name != positionals[0] {
			return opts, fmt.Errorf("name given twice\nusage: %s", dbCreateUsage)
		}
		opts.Name = positionals[0]
	}
	opts.Name = strings.TrimSpace(opts.Name)
	if opts.AdminUser != "" {
		// The Dashboard ignores adminUsername at create time and always uses
		// the engine's default admin user, so the flag is refused rather
		// than silently dropped.
		return opts, errors.New("invalid arguments: --admin-user is not supported yet; HubFly creates the admin user 'admin' (add more users later with: hubfly db users create)")
	}
	if opts.Engine != "" {
		if opts.Engine, err = normalizeDBEngine(opts.Engine); err != nil {
			return opts, err
		}
	}
	if (opts.CPU > 0) != (opts.MemoryMib > 0) {
		return opts, errors.New("invalid size: pass --cpu and --memory together (or --size <preset>)")
	}
	if opts.CPU < 0 || opts.MemoryMib < 0 || opts.StorageGib < 0 {
		return opts, errors.New("invalid size: values must be positive")
	}
	if opts.CPU > 0 && opts.SizeID != "" {
		return opts, errors.New("invalid size: use --size or --cpu/--memory, not both")
	}
	for index, value := range opts.Allow.values {
		cidr, cidrErr := normalizeAllowlistCIDR(value)
		if cidrErr != nil {
			return opts, fmt.Errorf("invalid --allow: %v", cidrErr)
		}
		opts.Allow.values[index] = cidr
	}
	seconds, err := parseRealtimeDuration(*timeout)
	if err != nil || seconds <= 0 {
		return opts, fmt.Errorf("invalid --timeout %q", *timeout)
	}
	opts.Timeout = time.Duration(seconds) * time.Second
	return opts, nil
}

// pickDBEngine returns the engine from the flag, an interactive choice, or
// PostgreSQL.
func pickDBEngine(catalog dbcCatalog, flagValue string) (dbcEngine, error) {
	if flagValue != "" {
		engine, ok := catalog.engine(flagValue)
		if !ok {
			return dbcEngine{}, fmt.Errorf("engine %q is not offered right now (see: hubfly db engines)", flagValue)
		}
		return engine, nil
	}
	if rtInteractive() && len(catalog.Engines) > 1 {
		_, _ = fmt.Fprintln(rtOut, "Engines:")
		for index, engine := range catalog.Engines {
			_, _ = fmt.Fprintf(rtOut, "  %d) %s, from %s\n", index+1, engine.Label, rtMonthly(engine.StartingPrice))
		}
		choice, err := promptNumber(fmt.Sprintf("Engine [1-%d]: ", len(catalog.Engines)), len(catalog.Engines))
		if err != nil {
			return dbcEngine{}, err
		}
		if choice == 0 {
			return dbcEngine{}, errors.New("cancelled")
		}
		return catalog.Engines[choice-1], nil
	}
	engine, ok := catalog.engine("postgresql")
	if !ok {
		if len(catalog.Engines) == 0 {
			return dbcEngine{}, errors.New("the Dashboard returned no database engines")
		}
		engine = catalog.Engines[0]
	}
	return engine, nil
}

func pickDBVersion(engine dbcEngine, flagValue string) (dbcEngineVersion, error) {
	ids := []string{}
	for _, version := range engine.Versions {
		ids = append(ids, version.ID)
		if flagValue != "" && (strings.EqualFold(version.ID, flagValue) || strings.EqualFold(version.Label, flagValue)) {
			return version, nil
		}
	}
	if flagValue != "" {
		return dbcEngineVersion{}, fmt.Errorf("invalid --version %q for %s (available: %s)", flagValue, engine.Label, strings.Join(ids, ", "))
	}
	for _, version := range engine.Versions {
		if version.IsDefault {
			return version, nil
		}
	}
	if len(engine.Versions) == 0 {
		return dbcEngineVersion{}, fmt.Errorf("no versions of %s are offered right now", engine.Label)
	}
	return engine.Versions[0], nil
}

// pickDBSize returns the size from the flags, defaulting to the smallest
// preset that meets the engine's minimum memory.
func pickDBSize(catalog dbcCatalog, engine dbcEngine, opts dbCreateOptions) (rtSize, string, error) {
	storage := opts.StorageGib
	if storage == 0 {
		storage = int(catalog.Sizes.StorageGib.Default)
	}
	if storage == 0 {
		storage = 10
	}
	limits := catalog.Sizes
	if limits.StorageGib.Max > 0 && (float64(storage) < limits.StorageGib.Min || float64(storage) > limits.StorageGib.Max) {
		return rtSize{}, "", fmt.Errorf("invalid --storage %d: choose %d-%d GiB", storage, int(limits.StorageGib.Min), int(limits.StorageGib.Max))
	}
	if opts.CPU > 0 {
		if limits.CPU.Max > 0 && (opts.CPU < limits.CPU.Min || opts.CPU > limits.CPU.Max || !isStepMultiple(opts.CPU, limits.CPU.Step)) {
			return rtSize{}, "", fmt.Errorf("invalid --cpu %s: choose %s-%s in steps of %s",
				strconv.FormatFloat(opts.CPU, 'f', -1, 64), strconv.FormatFloat(limits.CPU.Min, 'f', -1, 64),
				strconv.FormatFloat(limits.CPU.Max, 'f', -1, 64), strconv.FormatFloat(limits.CPU.Step, 'f', -1, 64))
		}
		if limits.MemoryMib.Max > 0 && (float64(opts.MemoryMib) < limits.MemoryMib.Min || float64(opts.MemoryMib) > limits.MemoryMib.Max || !isStepMultiple(float64(opts.MemoryMib), limits.MemoryMib.Step)) {
			return rtSize{}, "", fmt.Errorf("invalid --memory %d: choose %d-%d MiB in steps of %d",
				opts.MemoryMib, int(limits.MemoryMib.Min), int(limits.MemoryMib.Max), int(limits.MemoryMib.Step))
		}
		if opts.MemoryMib < engine.MinMemoryMib {
			return rtSize{}, "", fmt.Errorf("invalid --memory %d: %s needs at least %d MiB", opts.MemoryMib, engine.Label, engine.MinMemoryMib)
		}
		return rtSize{CPU: opts.CPU, MemoryMib: opts.MemoryMib, StorageGib: storage}, "Custom", nil
	}
	if len(limits.Presets) == 0 {
		return rtSize{}, "", errors.New("the Dashboard returned no database sizes")
	}
	ids := []string{}
	for _, preset := range limits.Presets {
		if opts.SizeID == "" {
			if preset.MemoryMib >= engine.MinMemoryMib {
				return rtSize{CPU: preset.CPU, MemoryMib: preset.MemoryMib, StorageGib: storage}, preset.Label, nil
			}
			continue
		}
		ids = append(ids, preset.ID)
		if strings.EqualFold(preset.ID, opts.SizeID) || strings.EqualFold(preset.Label, opts.SizeID) {
			if preset.MemoryMib < engine.MinMemoryMib {
				return rtSize{}, "", fmt.Errorf("invalid --size %s: %s needs at least %d MiB of memory", preset.ID, engine.Label, engine.MinMemoryMib)
			}
			return rtSize{CPU: preset.CPU, MemoryMib: preset.MemoryMib, StorageGib: storage}, preset.Label, nil
		}
	}
	if opts.SizeID == "" {
		return rtSize{}, "", fmt.Errorf("no size preset meets the %d MiB minimum of %s; use --cpu and --memory", engine.MinMemoryMib, engine.Label)
	}
	return rtSize{}, "", fmt.Errorf("invalid --size %q (available: %s)", opts.SizeID, strings.Join(ids, ", "))
}

func isStepMultiple(value, step float64) bool {
	if step <= 0 {
		return true
	}
	ratio := value / step
	return math.Abs(ratio-math.Round(ratio)) < 1e-9
}

// resolveDBProject returns the managed project to create in; create=true
// means "create a new managed project".
func resolveDBProject(token string, opts dbCreateOptions) (project, bool, error) {
	ref := strings.TrimSpace(opts.ProjectRef)
	if strings.EqualFold(ref, "new") {
		return project{}, true, nil
	}
	if ref != "" {
		projects, err := fetchManagedProjects(token, ref)
		if err != nil {
			return project{}, false, err
		}
		chosen := projects[0]
		if chosen.Type != "" && chosen.Type != "managed" {
			return project{}, false, fmt.Errorf("project '%s' is a %s project; databases live in managed projects (use --project new)", chosen.Name, chosen.Type)
		}
		return chosen, false, nil
	}
	managed, err := fetchManagedProjects(token, "")
	if err != nil {
		return project{}, false, err
	}
	if len(managed) == 0 {
		return project{}, true, nil
	}
	if !rtInteractive() {
		return project{}, false, errors.New("invalid arguments: choose a managed project with --project <id|name|new>")
	}
	_, _ = fmt.Fprintln(rtOut, "Managed projects:")
	for index, item := range managed {
		_, _ = fmt.Fprintf(rtOut, "  %d) %s (%s)\n", index+1, item.Name, item.ID)
	}
	_, _ = fmt.Fprintf(rtOut, "  %d) New managed project\n", len(managed)+1)
	choice, err := promptNumber(fmt.Sprintf("Project [1-%d]: ", len(managed)+1), len(managed)+1)
	if err != nil {
		return project{}, false, err
	}
	if choice == 0 {
		return project{}, false, errors.New("cancelled")
	}
	if choice == len(managed)+1 {
		return project{}, true, nil
	}
	return managed[choice-1], false, nil
}

func dbCreateFlow(args []string) error {
	opts, err := parseDBCreateOptions(args)
	if err != nil {
		return err
	}
	if opts.Name == "" {
		if !rtInteractive() {
			return fmt.Errorf("usage: %s", dbCreateUsage)
		}
		if opts.Name, err = prompt("Database name: "); err != nil {
			return err
		}
		if opts.Name = strings.TrimSpace(opts.Name); opts.Name == "" {
			return errors.New("cancelled: a name is required")
		}
	}
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}
	catalog, err := fetchDBCatalog(token)
	if err != nil {
		return err
	}
	if !catalog.databasesAvailable() {
		return errors.New("Managed Databases are not available in any region for this account yet")
	}
	engine, err := pickDBEngine(catalog, opts.Engine)
	if err != nil {
		return err
	}
	version, err := pickDBVersion(engine, opts.Version)
	if err != nil {
		return err
	}
	if engine.ID == "libsql" && opts.DatabaseName != "" {
		return errors.New("invalid arguments: --database does not apply to SQLite (libSQL); it authenticates with tokens")
	}
	size, sizeLabel, err := pickDBSize(catalog, engine, opts)
	if err != nil {
		return err
	}
	allowlist := append([]string{}, opts.Allow.values...)
	if opts.AllowMyIP {
		cidr, ipErr := clientAllowlistCIDR(token)
		if ipErr != nil {
			return ipErr
		}
		allowlist = append(allowlist, cidr)
	}
	allowlist = uniqueStrings(allowlist)
	if catalog.MaxAllowlistEntries > 0 && len(allowlist) > catalog.MaxAllowlistEntries {
		return fmt.Errorf("invalid --allow: at most %d entries", catalog.MaxAllowlistEntries)
	}
	target, createProject, err := resolveDBProject(token, opts)
	if err != nil {
		return err
	}
	price, err := fetchManagedPriceEstimate(token, size)
	if err != nil {
		return dbAPIError(opts.Name, err)
	}

	projectName := strings.TrimSpace(opts.ProjectName)
	if projectName == "" {
		projectName = opts.Name
	}
	projectLabel := fmt.Sprintf("%s (%s)", target.Name, target.ID)
	if createProject {
		projectLabel = fmt.Sprintf("new managed project '%s' (HubFly chooses the region)", projectName)
	}
	network := "public, open to all addresses (TLS required)"
	switch {
	case opts.Private:
		network = "private (no public endpoint)"
	case len(allowlist) > 0:
		network = fmt.Sprintf("public, allowlist %s (TLS required)", strings.Join(allowlist, ", "))
	}
	quoteOut := rtOut
	if opts.JSON {
		quoteOut = rtErr // keep stdout clean for --json
	}
	tw := tabwriter.NewWriter(quoteOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "Database:\t%s\n", opts.Name)
	_, _ = fmt.Fprintf(tw, "Engine:\t%s\n", version.Label)
	_, _ = fmt.Fprintf(tw, "Size:\t%s (%s, %d GiB storage)\n", sizeLabel, formatRealtimeSize(size), size.StorageGib)
	_, _ = fmt.Fprintf(tw, "Network:\t%s\n", network)
	_, _ = fmt.Fprintf(tw, "Project:\t%s\n", projectLabel)
	_, _ = fmt.Fprintf(tw, "Price:\t%s (%s, billed hourly; a stopped database pays for storage only)\n", rtMonthly(price), rtHourly(price))
	_ = tw.Flush()

	if !opts.Yes {
		if !rtInteractive() {
			return fmt.Errorf("invalid arguments: confirmation required; re-run with --yes to accept %s", rtMonthly(price))
		}
		confirmed, promptErr := promptYesNo("Create this database?", false)
		if promptErr != nil {
			return promptErr
		}
		if !confirmed {
			_, _ = fmt.Fprintln(rtOut, "Cancelled.")
			return nil
		}
	}

	if createProject {
		created, createErr := createManagedProjectFor(token, projectName, managedServiceDatabase)
		if createErr != nil {
			return fmt.Errorf("failed to create managed project: %w", createErr)
		}
		target = created
		_, _ = fmt.Fprintf(rtErr, "Created managed project '%s' (%s).\n", created.Name, created.ID)
	}

	entries := make([]map[string]any, 0, len(allowlist))
	for _, cidr := range allowlist {
		entries = append(entries, map[string]any{"cidr": cidr})
	}
	body := map[string]any{
		"name":    opts.Name,
		"service": managedServiceDatabase,
		"engine":  engine.ID,
		"version": version.ID,
		"size": map[string]any{
			"cpu":        size.CPU,
			"memoryMib":  size.MemoryMib,
			"storageGib": size.StorageGib,
		},
		"publicAccess":      !opts.Private,
		"allowlist":         entries,
		"quotedHourlyTotal": price.Hourly.Total,
		"idempotencyKey":    randomRealtimeID("cli-db-"),
	}
	if opts.DatabaseName != "" {
		body["defaultDatabaseName"] = opts.DatabaseName
	}
	created, err := createRealtimeInstance(token, target.ID, body)
	if err != nil {
		return dbAPIError(opts.Name, err)
	}
	instance := created.Instance
	instance.ProjectID, instance.ProjectName = target.ID, target.Name

	var operation *rtOperation
	if !opts.NoWait && created.OperationID != "" {
		finished, waitErr := waitForManagedOperation(token, instance, created.OperationID, "Creating database", opts.Timeout, opts.JSON, false)
		if waitErr != nil {
			return waitErr
		}
		operation = &finished
	}
	var connection *dbcConnection
	if operation != nil {
		if fetched, connErr := fetchDBConnection(token, instance); connErr == nil {
			connection = &fetched
		}
	}
	if opts.JSON {
		detail, detailErr := fetchManagedInstanceDetailed(token, instance)
		if detailErr != nil {
			detail = mlInstance{rtInstanceDetail: rtInstanceDetail{rtInstance: instance}}
		}
		return rtPrintJSON(map[string]any{
			"instance":    detail,
			"operationId": created.OperationID,
			"operation":   operation,
			"price":       created.Price,
			"connection":  connection,
		})
	}
	if operation != nil {
		_, _ = fmt.Fprintf(rtOut, "Database '%s' is ready.\n", instance.Name)
	} else {
		_, _ = fmt.Fprintf(rtOut, "Creating database '%s' (operation %s). Follow it with: hubfly db info %s\n", instance.Name, created.OperationID, instance.Name)
	}
	tw = tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "  ID:\t%s\n", instance.ID)
	if connection != nil {
		if connection.Engine == "libsql" {
			_, _ = fmt.Fprintf(tw, "  URL:\t%s\n", firstNonEmpty(derefString(connection.LibsqlURL), connection.Host))
		} else {
			if endpoint := managedEndpointText(connection.Host, connection.Port); endpoint != "" && connection.PublicAccess {
				_, _ = fmt.Fprintf(tw, "  Host:\t%s\n", endpoint)
			}
			_, _ = fmt.Fprintf(tw, "  Database:\t%s\n", derefString(connection.Database))
			_, _ = fmt.Fprintf(tw, "  User:\t%s\n", derefString(connection.Username))
		}
	}
	_ = tw.Flush()
	_, _ = fmt.Fprintln(rtOut, "\nNext steps:")
	if engine.ID == "libsql" {
		_, _ = fmt.Fprintf(rtOut, "  hubfly db tokens create %s <name>    # create an auth token (shown once)\n", instance.Name)
		_, _ = fmt.Fprintf(rtOut, "  hubfly db bind %s --to <project>     # write LIBSQL_URL and LIBSQL_AUTH_TOKEN into a Cell project\n", instance.Name)
		_, _ = fmt.Fprintf(rtOut, "  hubfly db query %s \"select 1\"       # run SQL\n", instance.Name)
		return nil
	}
	_, _ = fmt.Fprintf(rtOut, "  hubfly db url %s --reveal            # print the connection URL with the password\n", instance.Name)
	_, _ = fmt.Fprintf(rtOut, "  hubfly db bind %s --to <project>     # write DATABASE_URL into a Cell project\n", instance.Name)
	_, _ = fmt.Fprintf(rtOut, "  hubfly db connect %s                 # open %s\n", instance.Name, dbClientName(engine.ID))
	return nil
}

// ---------------------------------------------------------------------------
// delete
// ---------------------------------------------------------------------------

func dbDeleteFlow(args []string) error {
	const usage = "hubfly db delete <db> [--project <id|name>] [--yes] [--wait]"
	fs := newRealtimeFlags("db delete")
	projectRef := fs.String("project", "", "")
	yes := fs.Bool("yes", false, "")
	fs.BoolVar(yes, "y", false, "")
	wait := fs.Bool("wait", false, "")
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
	instance, err := resolveDatabaseInstance(token, positionals[0], *projectRef)
	if err != nil {
		return err
	}
	detail, err := fetchManagedInstanceDetailed(token, instance)
	if err != nil {
		return dbAPIError(instance.Name, err)
	}
	if detail.DeletionProtection {
		return fmt.Errorf("database '%s' has deletion protection on; turn it off first: hubfly db protect %s off", instance.Name, instance.Name)
	}
	if !*yes {
		if !rtInteractive() {
			return errors.New("invalid arguments: confirmation required; re-run with --yes to delete")
		}
		confirmed, promptErr := confirmTyped(fmt.Sprintf(
			"This permanently deletes database '%s' (%s, %s) in project '%s' with all its data. Automatic backups are not available yet; export first if you need the data (hubfly db export %s).",
			instance.Name, instance.ID, instance.VersionLabel, instance.ProjectName, instance.Name), instance.Name)
		if promptErr != nil {
			return promptErr
		}
		if !confirmed {
			_, _ = fmt.Fprintln(rtOut, "Cancelled: the name did not match.")
			return nil
		}
	}
	operationID, err := deleteRealtimeInstance(token, instance.ProjectID, instance.ID, instance.Name)
	if err != nil {
		return dbAPIError(instance.Name, err)
	}
	if *wait && operationID != "" {
		if _, waitErr := waitForManagedOperation(token, instance, operationID, "Deleting database", 35*time.Minute, false, true); waitErr != nil {
			return waitErr
		}
		_, _ = fmt.Fprintf(rtOut, "Database '%s' deleted.\n", instance.Name)
		return nil
	}
	_, _ = fmt.Fprintf(rtOut, "Deleting database '%s' (operation %s).\n", instance.Name, operationID)
	return nil
}

// ---------------------------------------------------------------------------
// bind
// ---------------------------------------------------------------------------

func runDBBindGroup(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "list", "ls":
			return dbBindListFlow(args[1:])
		case "remove", "rm", "delete":
			return dbBindRemoveFlow(args[1:])
		case "create":
			return dbBindCreateFlow(args[1:])
		}
	}
	return dbBindCreateFlow(args)
}

func bindingVariables(binding dbcBinding) []string {
	keys := []string{binding.EnvKey}
	if key := derefString(binding.AuthTokenEnvKey); key != "" {
		keys = append(keys, key)
	}
	return keys
}

func dbBindCreateFlow(args []string) error {
	const usage = "hubfly db bind <db> --to <project> [--env <NAME>] [--database <name>] [--new-user [--access read_only|read_write]] [--project <id|name>] [--json]"
	fs := newRealtimeFlags("db bind")
	projectRef := fs.String("project", "", "")
	targetRef := fs.String("to", "", "")
	envKey := fs.String("env", "", "")
	databaseName := fs.String("database", "", "")
	newUser := fs.Bool("new-user", false, "")
	access := fs.String("access", "", "")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 1 || strings.TrimSpace(*targetRef) == "" {
		return fmt.Errorf("usage: %s", usage)
	}
	input := dbcBindingInput{
		EnvKey:       strings.TrimSpace(*envKey),
		DatabaseName: strings.TrimSpace(*databaseName),
		CreateUser:   *newUser,
	}
	if *access != "" {
		if !*newUser {
			return errors.New("invalid arguments: --access applies to a dedicated user; add --new-user")
		}
		switch *access {
		case "read_only", "read_write":
			input.Access = *access
		default:
			return fmt.Errorf("invalid --access %q (use read_only or read_write)", *access)
		}
	}
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}
	instance, err := resolveDatabaseInstance(token, positionals[0], *projectRef)
	if err != nil {
		return err
	}
	projects, err := fetchAllProjectsAnyType(token, "")
	if err != nil {
		return err
	}
	for _, item := range projects {
		if item.ID == *targetRef || strings.EqualFold(item.Name, *targetRef) {
			if item.Type != "" && item.Type != "cell" {
				return fmt.Errorf("project '%s' is a %s project; databases can be bound to Cell projects only", item.Name, item.Type)
			}
			input.TargetProjectID = item.ID
			break
		}
	}
	if input.TargetProjectID == "" {
		return fmt.Errorf("project '%s' not found", *targetRef)
	}
	binding, err := createDBBinding(token, instance, input)
	if err != nil {
		return dbAPIError(instance.Name, err)
	}
	if *asJSON {
		return rtPrintJSON(binding)
	}
	_, _ = fmt.Fprintf(rtOut, "Bound database '%s' to project '%s' (binding %s).\n", instance.Name, binding.TargetProjectName, binding.ID)
	_, _ = fmt.Fprintf(rtOut, "Variables: %s\n", strings.Join(bindingVariables(binding), ", "))
	if binding.DedicatedUser {
		_, _ = fmt.Fprintf(rtOut, "User: %s (dedicated to this binding; dropped when the binding is removed)\n", derefString(binding.Username))
	}
	_, _ = fmt.Fprintln(rtOut, "Apps in the project pick up the new values on their next deploy.")
	return nil
}

func dbBindListFlow(args []string) error {
	const usage = "hubfly db bind list <db> [--project <id|name>] [--json]"
	fs := newRealtimeFlags("db bind list")
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
	instance, err := resolveDatabaseInstance(token, positionals[0], *projectRef)
	if err != nil {
		return err
	}
	bindings, err := fetchDBBindings(token, instance)
	if err != nil {
		return dbAPIError(instance.Name, err)
	}
	if *asJSON {
		return rtPrintJSON(bindings)
	}
	if len(bindings) == 0 {
		_, _ = fmt.Fprintf(rtOut, "Database '%s' has no bindings. Add one with: hubfly db bind %s --to <project>\n", instance.Name, instance.Name)
		return nil
	}
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tPROJECT\tVARIABLES\tDATABASE\tUSER")
	for _, binding := range bindings {
		user := firstNonEmpty(derefString(binding.Username), "-")
		if binding.DedicatedUser {
			user += " (dedicated)"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", binding.ID, binding.TargetProjectName,
			strings.Join(bindingVariables(binding), ", "), firstNonEmpty(derefString(binding.DatabaseName), "-"), user)
	}
	return tw.Flush()
}

func dbBindRemoveFlow(args []string) error {
	const usage = "hubfly db bind remove <db> <bindingId> [--keep-variable] [--project <id|name>] [--yes]"
	fs := newRealtimeFlags("db bind remove")
	projectRef := fs.String("project", "", "")
	keep := fs.Bool("keep-variable", false, "")
	yes := fs.Bool("yes", false, "")
	fs.BoolVar(yes, "y", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 2 {
		return fmt.Errorf("usage: %s", usage)
	}
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}
	instance, err := resolveDatabaseInstance(token, positionals[0], *projectRef)
	if err != nil {
		return err
	}
	bindings, err := fetchDBBindings(token, instance)
	if err != nil {
		return dbAPIError(instance.Name, err)
	}
	var binding *dbcBinding
	for index := range bindings {
		if bindings[index].ID == positionals[1] {
			binding = &bindings[index]
			break
		}
	}
	if binding == nil {
		return fmt.Errorf("binding '%s' not found on database '%s' (list them with: hubfly db bind list %s)", positionals[1], instance.Name, instance.Name)
	}
	variables := strings.Join(bindingVariables(*binding), ", ")
	if !*yes {
		if !rtInteractive() {
			return errors.New("invalid arguments: confirmation required; re-run with --yes to remove")
		}
		what := fmt.Sprintf("Remove binding %s and delete %s from project '%s'?", binding.ID, variables, binding.TargetProjectName)
		if *keep {
			what = fmt.Sprintf("Remove binding %s (%s stays in project '%s')?", binding.ID, variables, binding.TargetProjectName)
		}
		if binding.DedicatedUser {
			what += fmt.Sprintf(" Its dedicated user %s is dropped.", derefString(binding.Username))
		}
		confirmed, promptErr := promptYesNo(what, false)
		if promptErr != nil {
			return promptErr
		}
		if !confirmed {
			_, _ = fmt.Fprintln(rtOut, "Cancelled.")
			return nil
		}
	}
	if err := deleteDBBinding(token, instance, binding.ID, !*keep); err != nil {
		return dbAPIError(instance.Name, err)
	}
	if *keep {
		_, _ = fmt.Fprintf(rtOut, "Removed binding %s (%s kept in project '%s').\n", binding.ID, variables, binding.TargetProjectName)
	} else {
		_, _ = fmt.Fprintf(rtOut, "Removed binding %s and %s from project '%s'.\n", binding.ID, variables, binding.TargetProjectName)
	}
	return nil
}

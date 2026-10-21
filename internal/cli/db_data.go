package cli

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"
)

// Data and administration `hubfly db` commands: query, databases, users,
// tokens, extensions, export/import, cert, sessions, insights and config.
// Every command resolves the instance, reads its detail and checks the
// engine's capabilities before it calls an engine-specific endpoint.
// Implementations: db_data_query.go, db_data_admin.go, db_data_transfer.go
// and db_data_operate.go; API calls in db_data_api.go.

// dbdTarget is a resolved database instance with its detail and the token.
type dbdTarget struct {
	token    string
	instance rtInstance
	detail   dbdInstance
}

func resolveDBDTarget(ref, projectRef string) (dbdTarget, error) {
	token, err := ensureAuth(true)
	if err != nil {
		return dbdTarget{}, err
	}
	instance, err := resolveDatabaseInstance(token, ref, projectRef)
	if err != nil {
		return dbdTarget{}, err
	}
	detail, err := fetchDBDInstance(token, instance.ProjectID, instance.ID)
	if err != nil {
		return dbdTarget{}, err
	}
	detail.ProjectID, detail.ProjectName = instance.ProjectID, instance.ProjectName
	if detail.Engine == "" {
		detail.Engine = instance.Engine
	}
	return dbdTarget{token: token, instance: instance, detail: detail}, nil
}

func (t dbdTarget) name() string { return t.instance.Name }

func (t dbdTarget) engine() string { return t.detail.Engine }

// engineLabel is "PostgreSQL", "MySQL" or "SQLite (libSQL)".
func (t dbdTarget) engineLabel() string {
	switch {
	case t.detail.EngineLabel != "":
		return t.detail.EngineLabel
	case t.detail.Engine == "postgresql":
		return "PostgreSQL"
	case t.detail.Engine == "mysql":
		return "MySQL"
	case t.detail.Engine == "libsql":
		return "SQLite (libSQL)"
	}
	return t.detail.Engine
}

// defaultDatabase is the database commands use when --database is omitted.
func (t dbdTarget) defaultDatabase() string {
	if t.detail.DefaultDatabase != nil {
		return *t.detail.DefaultDatabase
	}
	return ""
}

func (t dbdTarget) adminUsername() string {
	if t.detail.AdminUsername != nil && *t.detail.AdminUsername != "" {
		return *t.detail.AdminUsername
	}
	return "admin"
}

// require checks one engine capability and, when it is missing, says what the
// engine offers instead.
func (t dbdTarget) require(feature string) error {
	caps := t.detail.Capabilities
	supported := map[string]bool{
		"query":       caps.Query,
		"databases":   caps.Databases,
		"users":       caps.Users,
		"tokens":      caps.Tokens,
		"extensions":  caps.Extensions,
		"export":      caps.Export,
		"import":      caps.Import,
		"certificate": caps.Certificate,
		"sessions":    caps.Sessions,
		"insights":    caps.Insights,
		"config":      caps.Config,
	}
	if supported[feature] {
		return nil
	}
	name, label := t.name(), t.engineLabel()
	libsql := t.engine() == "libsql"
	switch {
	case feature == "databases" && libsql:
		return fmt.Errorf("'%s' is a %s instance with a single database (main); logical databases are a PostgreSQL and MySQL feature", name, label)
	case feature == "users" && libsql:
		return fmt.Errorf("'%s' is a %s instance: it uses access tokens instead of database users (see: hubfly db tokens %s)", name, label, name)
	case feature == "tokens":
		return fmt.Errorf("access tokens are only used by SQLite (libSQL) instances; '%s' is a %s instance and uses database users (see: hubfly db users %s)", name, label, name)
	case feature == "extensions":
		return fmt.Errorf("extensions are available for PostgreSQL only; '%s' is a %s instance", name, label)
	case feature == "certificate" && libsql:
		return fmt.Errorf("'%s' is a %s instance: it is served over HTTPS with a publicly trusted certificate, so there is no instance certificate to download", name, label)
	case feature == "config" && libsql:
		return fmt.Errorf("'%s' is a %s instance: HubFly manages its settings (configuration is available for PostgreSQL and MySQL)", name, label)
	case (feature == "sessions" || feature == "insights") && libsql:
		return fmt.Errorf("%s are available for PostgreSQL and MySQL; '%s' is a %s instance", dbdFeatureNoun(feature), name, label)
	}
	return fmt.Errorf("%s are not available for '%s' (%s)", dbdFeatureNoun(feature), name, label)
}

func dbdFeatureNoun(feature string) string {
	switch feature {
	case "query":
		return "SQL queries"
	case "databases":
		return "logical databases"
	case "users":
		return "database users"
	case "tokens":
		return "access tokens"
	case "certificate":
		return "instance certificates"
	case "sessions":
		return "session lists"
	case "insights":
		return "query insights"
	case "config":
		return "configuration changes"
	case "export":
		return "exports"
	case "import":
		return "imports"
	}
	return feature
}

// apiError adds a hint to the Dashboard errors a database user can act on.
// The wrapped *apiError keeps exit codes intact.
func (t dbdTarget) apiError(err error) error {
	var apiErr *apiError
	if !errors.As(err, &apiErr) {
		return err
	}
	name := t.name()
	hint := ""
	switch apiErr.Code {
	case "MANAGED_INSTANCE_NOT_RUNNING":
		hint = fmt.Sprintf("Start it with: hubfly db start %s", name)
	case "MANAGED_EXPORTS_UNAVAILABLE":
		hint = "Exports and imports are not configured for this database's region yet."
	case "MANAGED_RATE_LIMITED", "RATE_LIMITED":
		hint = "Wait a moment and retry."
	case "MANAGED_INSUFFICIENT_SPACE":
		hint = fmt.Sprintf("Grow the storage with: hubfly db resize %s --storage <gib>", name)
	case "MANAGED_JOB_RUNNING":
		hint = fmt.Sprintf("Follow it with: hubfly db operations %s", name)
	case "MANAGED_CONFIG_CONFLICT":
		hint = "Run the command again to use the current configuration."
	case "MANAGED_EXPORT_EXPIRED":
		hint = fmt.Sprintf("Exports are kept for 7 days; create a new one with: hubfly db export %s", name)
	case "MANAGED_EXPORT_NOT_READY":
		hint = fmt.Sprintf("Check its status with: hubfly db exports %s", name)
	}
	if hint == "" {
		return err
	}
	return fmt.Errorf("%w\n%s", err, hint)
}

// dbdConfirm asks a yes/no question unless --yes was given. Non-interactive
// runs must pass --yes; action completes "re-run with --yes to ...".
func dbdConfirm(yes bool, question, action string) (bool, error) {
	if yes {
		return true, nil
	}
	if !rtInteractive() {
		return false, fmt.Errorf("invalid arguments: confirmation required; re-run with --yes to %s", action)
	}
	return promptYesNo(question, false)
}

// dbdConfirmTyped asks for the exact name unless --yes was given.
func dbdConfirmTyped(yes bool, message, expected, action string) (bool, error) {
	if yes {
		return true, nil
	}
	if !rtInteractive() {
		return false, fmt.Errorf("invalid arguments: confirmation required; re-run with --yes to %s", action)
	}
	return confirmTyped(message, expected)
}

func dbdUsageError(usage string) error {
	return fmt.Errorf("usage: %s", usage)
}

func dbdUnexpected(argument, usage string) error {
	return fmt.Errorf("unexpected argument: %s\nusage: %s", argument, usage)
}

// dbdSplitList splits a comma list and drops empty entries.
func dbdSplitList(value string) []string {
	items := []string{}
	for _, part := range strings.Split(value, ",") {
		if part = strings.TrimSpace(part); part != "" {
			items = append(items, part)
		}
	}
	return items
}

func dbdText(value *string, empty string) string {
	if value == nil || *value == "" {
		return empty
	}
	return *value
}

// dbdTime renders an API timestamp as "2006-01-02 15:04:05Z" (UTC).
func dbdTime(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return value
	}
	return parsed.UTC().Format("2006-01-02 15:04:05Z")
}

func dbdTimePtr(value *string) string {
	if value == nil {
		return "-"
	}
	return dbdTime(*value)
}

// dbdDuration renders milliseconds compactly: 850 ms, 4.2s, 3m 10s, 2h 5m, 3d 4h.
func dbdDuration(ms float64) string {
	if math.IsNaN(ms) || ms < 0 {
		return "-"
	}
	if ms < 1000 {
		return fmt.Sprintf("%.0f ms", ms)
	}
	seconds := ms / 1000
	if seconds < 60 {
		return fmt.Sprintf("%.1fs", seconds)
	}
	total := int64(seconds)
	days, hours, minutes, secs := total/86400, total%86400/3600, total%3600/60, total%60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, minutes)
	default:
		return fmt.Sprintf("%dm %ds", minutes, secs)
	}
}

// dbdOneLine collapses whitespace (queries span lines) and shortens the text
// to max runes.
func dbdOneLine(value string, max int) string {
	value = strings.Join(strings.Fields(value), " ")
	if max > 0 && utf8.RuneCountInString(value) > max {
		runes := []rune(value)
		return string(runes[:max-1]) + "…"
	}
	return value
}

func dbdPlural(count int64, singular, plural string) string {
	if count == 1 {
		return fmt.Sprintf("%d %s", count, singular)
	}
	return fmt.Sprintf("%s %s", formatThousands64(count), plural)
}

func formatThousands64(value int64) string {
	if value >= math.MinInt && value <= math.MaxInt {
		return formatThousands(int(value))
	}
	return fmt.Sprintf("%d", value)
}

// ---------------------------------------------------------------------------
// Group dispatchers ("[list]" is the default subcommand)
// ---------------------------------------------------------------------------

func runDBDatabasesGroup(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: hubfly db databases [list|create|delete] <db> ...")
	}
	switch args[0] {
	case "list", "ls":
		return dbdDatabasesListFlow(args[1:])
	case "create", "new":
		return dbdDatabasesCreateFlow(args[1:])
	case "delete", "rm", "drop":
		return dbdDatabasesDeleteFlow(args[1:])
	default:
		return dbdDatabasesListFlow(args)
	}
}

func runDBUsersGroup(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: hubfly db users [list|create|update|delete|rotate] <db> ...")
	}
	switch args[0] {
	case "list", "ls":
		return dbdUsersListFlow(args[1:])
	case "create", "new":
		return dbdUsersCreateFlow(args[1:])
	case "update", "set":
		return dbdUsersUpdateFlow(args[1:])
	case "delete", "rm", "drop":
		return dbdUsersDeleteFlow(args[1:])
	case "rotate", "rotate-password":
		return dbdUsersRotateFlow(args[1:])
	default:
		return dbdUsersListFlow(args)
	}
}

func runDBTokensGroup(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: hubfly db tokens [list|create|revoke] <db> ...")
	}
	switch args[0] {
	case "list", "ls":
		return dbdTokensListFlow(args[1:])
	case "create", "new":
		return dbdTokensCreateFlow(args[1:])
	case "revoke", "delete", "rm":
		return dbdTokensRevokeFlow(args[1:])
	default:
		return dbdTokensListFlow(args)
	}
}

func runDBExtensionsGroup(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: hubfly db extensions [list|install|remove] <db> ...")
	}
	switch args[0] {
	case "list", "ls":
		return dbdExtensionsListFlow(args[1:])
	case "install", "add", "create":
		return dbdExtensionsInstallFlow(args[1:])
	case "remove", "rm", "delete", "drop":
		return dbdExtensionsRemoveFlow(args[1:])
	default:
		return dbdExtensionsListFlow(args)
	}
}

func runDBExportsGroup(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: hubfly db exports [list|download] <db> ...")
	}
	switch args[0] {
	case "list", "ls":
		return dbdExportsListFlow(args[1:])
	case "download", "get":
		return dbdExportsDownloadFlow(args[1:])
	default:
		return dbdExportsListFlow(args)
	}
}

func runDBSessionsGroup(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: hubfly db sessions [list|cancel|terminate] <db> ...")
	}
	switch args[0] {
	case "list", "ls":
		return dbdSessionsListFlow(args[1:])
	case "cancel":
		return dbdSessionsSignalFlow("cancel", args[1:])
	case "terminate", "kill":
		return dbdSessionsSignalFlow("terminate", args[1:])
	default:
		return dbdSessionsListFlow(args)
	}
}

func runDBInsightsGroup(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: hubfly db insights <db> [--order-by total_time|mean_time|calls] [--limit <n>] [--json]\n       hubfly db insights reset <db> [--yes]")
	}
	switch args[0] {
	case "reset":
		return dbdInsightsResetFlow(args[1:])
	case "list", "ls", "show", "top":
		return dbdInsightsFlow(args[1:])
	default:
		return dbdInsightsFlow(args)
	}
}

func runDBConfigGroup(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: hubfly db config [show|set|history|revert] <db> ...")
	}
	switch args[0] {
	case "show", "get", "list", "ls":
		return dbdConfigShowFlow(args[1:])
	case "set", "apply":
		return dbdConfigSetFlow(args[1:])
	case "history", "revisions":
		return dbdConfigHistoryFlow(args[1:])
	case "revert", "rollback":
		return dbdConfigRevertFlow(args[1:])
	default:
		return dbdConfigShowFlow(args)
	}
}

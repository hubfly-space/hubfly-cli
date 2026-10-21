package cli

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"
)

// `hubfly db databases|users|tokens|extensions`: logical databases and
// users (PostgreSQL, MySQL), access tokens (SQLite/libSQL) and extensions
// (PostgreSQL).

// ---------------------------------------------------------------------------
// databases
// ---------------------------------------------------------------------------

func dbdDatabasesListFlow(args []string) error {
	const usage = "hubfly db databases [list] <db> [--project <id|name>] [--json]"
	fs := newRealtimeFlags("db databases list")
	projectRef := fs.String("project", "", "")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return dbdUsageError(usage)
	}
	target, err := resolveDBDTarget(positionals[0], *projectRef)
	if err != nil {
		return err
	}
	if err := target.require("databases"); err != nil {
		return err
	}
	databases, err := fetchDBDDatabases(target.token, target.instance.ProjectID, target.instance.ID)
	if err != nil {
		return target.apiError(err)
	}
	if *asJSON {
		return rtPrintJSON(databases)
	}
	if len(databases) == 0 {
		_, _ = fmt.Fprintf(rtOut, "No databases on '%s'. Create one with: hubfly db databases create %s <name>\n", target.name(), target.name())
		return nil
	}
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAME\tOWNER\tSIZE\tENCODING\tCOLLATION\tNOTES")
	for _, database := range databases {
		size := "-"
		if database.SizeBytes != nil {
			size = formatBytes(*database.SizeBytes)
		}
		notes := []string{}
		if database.IsDefault {
			notes = append(notes, "default")
		}
		if database.IsSystem {
			notes = append(notes, "system")
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", database.Name, dbdText(database.Owner, "-"), size,
			dbdText(database.Encoding, "-"), dbdText(database.Collation, "-"), strings.Join(notes, ", "))
	}
	return tw.Flush()
}

func dbdDatabasesCreateFlow(args []string) error {
	const usage = "hubfly db databases create <db> <name> [--owner <user>] [--project <id|name>] [--json]"
	fs := newRealtimeFlags("db databases create")
	projectRef := fs.String("project", "", "")
	owner := fs.String("owner", "", "")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 2 || strings.TrimSpace(positionals[1]) == "" {
		return dbdUsageError(usage)
	}
	target, err := resolveDBDTarget(positionals[0], *projectRef)
	if err != nil {
		return err
	}
	if err := target.require("databases"); err != nil {
		return err
	}
	name, err := createDBDDatabase(target.token, target.instance.ProjectID, target.instance.ID, strings.TrimSpace(positionals[1]), strings.TrimSpace(*owner))
	if err != nil {
		return target.apiError(err)
	}
	if *asJSON {
		return rtPrintJSON(map[string]any{"name": name})
	}
	ownerText := strings.TrimSpace(*owner)
	if ownerText == "" {
		ownerText = target.adminUsername()
	}
	if target.engine() == "mysql" {
		_, _ = fmt.Fprintf(rtOut, "Created database '%s' on '%s'.\n", name, target.name())
	} else {
		_, _ = fmt.Fprintf(rtOut, "Created database '%s' on '%s' (owner %s).\n", name, target.name(), ownerText)
	}
	_, _ = fmt.Fprintf(rtOut, "Connect to it with: hubfly db url %s --database %s\n", target.name(), name)
	return nil
}

func dbdDatabasesDeleteFlow(args []string) error {
	const usage = "hubfly db databases delete <db> <name> [--force] [--project <id|name>] [--yes]"
	fs := newRealtimeFlags("db databases delete")
	projectRef := fs.String("project", "", "")
	force := fs.Bool("force", false, "")
	yes := fs.Bool("yes", false, "")
	fs.BoolVar(yes, "y", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 2 || strings.TrimSpace(positionals[1]) == "" {
		return dbdUsageError(usage)
	}
	name := strings.TrimSpace(positionals[1])
	target, err := resolveDBDTarget(positionals[0], *projectRef)
	if err != nil {
		return err
	}
	if err := target.require("databases"); err != nil {
		return err
	}
	databases, err := fetchDBDDatabases(target.token, target.instance.ProjectID, target.instance.ID)
	if err != nil {
		return target.apiError(err)
	}
	var found *dbdDatabase
	for index := range databases {
		if databases[index].Name == name {
			found = &databases[index]
		}
	}
	switch {
	case found == nil:
		return fmt.Errorf("database '%s' not found on '%s' (list them with: hubfly db databases %s)", name, target.name(), target.name())
	case found.IsDefault || name == target.defaultDatabase():
		return fmt.Errorf("'%s' is the default database of '%s' and cannot be dropped", name, target.name())
	case found.IsSystem:
		return fmt.Errorf("'%s' is a system database and cannot be dropped", name)
	}
	sessions := "The drop fails while sessions use the database; --force disconnects them first."
	if *force {
		sessions = "Sessions connected to it are disconnected first (--force)."
	}
	message := fmt.Sprintf("This permanently drops database '%s' on '%s' and every table in it. There are no automatic backups (export it first with: hubfly db export %s --database %s). %s",
		name, target.name(), target.name(), name, sessions)
	confirmed, err := dbdConfirmTyped(*yes, message, name, "drop the database")
	if err != nil {
		return err
	}
	if !confirmed {
		_, _ = fmt.Fprintln(rtOut, "Cancelled: the name did not match.")
		return nil
	}
	if _, err := deleteDBDDatabase(target.token, target.instance.ProjectID, target.instance.ID, name, *force); err != nil {
		var apiErr *apiError
		if !*force && errors.As(err, &apiErr) && apiErr.Code == "MANAGED_ENGINE_ERROR" {
			return fmt.Errorf("%w\nIf sessions are using the database, re-run with --force to disconnect them first.", err)
		}
		return target.apiError(err)
	}
	_, _ = fmt.Fprintf(rtOut, "Dropped database '%s' on '%s'.\n", name, target.name())
	return nil
}

// ---------------------------------------------------------------------------
// users
// ---------------------------------------------------------------------------

// dbdPresetLabels describe the access presets as the Dashboard does. No
// preset is a superuser.
var dbdPresetLabels = map[string]string{
	"read_only":  "read only",
	"read_write": "read & write",
	"admin":      "admin",
	"custom":     "custom (changed outside HubFly)",
}

func dbdPresetLabel(preset string) string {
	if label, ok := dbdPresetLabels[preset]; ok {
		return label
	}
	return preset
}

func dbdNormalizePreset(value string) (string, error) {
	switch strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), "-", "_")) {
	case "read_only", "readonly", "ro":
		return "read_only", nil
	case "read_write", "readwrite", "rw":
		return "read_write", nil
	case "admin":
		return "admin", nil
	}
	return "", fmt.Errorf("invalid --preset %q (use read_only, read_write or admin)", value)
}

func dbdUserScope(user dbdUser) string {
	if user.Preset == "admin" || user.AllDatabases {
		return "all databases"
	}
	if len(user.Databases) == 0 {
		return "no databases"
	}
	return strings.Join(user.Databases, ", ")
}

func dbdFindUser(users []dbdUser, name string) *dbdUser {
	for index := range users {
		if users[index].Name == name {
			return &users[index]
		}
	}
	return nil
}

// dbdUserAppBindings lists the app bindings that connect as a user. Failures
// only cost the warning.
func dbdUserAppBindings(target dbdTarget, name string) []dbdUserBinding {
	bindings, err := fetchDBDUserBindings(target.token, target.instance.ProjectID, target.instance.ID)
	if err != nil {
		debugf("could not read bindings of %s: %v", target.instance.ID, err)
		return nil
	}
	matches := []dbdUserBinding{}
	for _, binding := range bindings {
		if binding.Username != nil && *binding.Username == name {
			matches = append(matches, binding)
		}
	}
	return matches
}

func dbdBindingProjects(bindings []dbdUserBinding) string {
	names := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		names = append(names, fmt.Sprintf("%s (%s)", binding.TargetProjectName, binding.EnvKey))
	}
	return strings.Join(names, ", ")
}

func dbdUsersListFlow(args []string) error {
	const usage = "hubfly db users [list] <db> [--project <id|name>] [--json]"
	fs := newRealtimeFlags("db users list")
	projectRef := fs.String("project", "", "")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return dbdUsageError(usage)
	}
	target, err := resolveDBDTarget(positionals[0], *projectRef)
	if err != nil {
		return err
	}
	if err := target.require("users"); err != nil {
		return err
	}
	users, err := fetchDBDUsers(target.token, target.instance.ProjectID, target.instance.ID)
	if err != nil {
		return target.apiError(err)
	}
	if *asJSON {
		return rtPrintJSON(users)
	}
	if len(users) == 0 {
		_, _ = fmt.Fprintf(rtOut, "No users on '%s' yet. Create one with: hubfly db users create %s <name>\n", target.name(), target.name())
		return nil
	}
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAME\tACCESS\tDATABASES\tLOGIN\tNOTES")
	for _, user := range users {
		login := "yes"
		if !user.CanLogin {
			login = "no"
		}
		notes := ""
		if user.IsPrimary {
			notes = "primary admin"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", user.Name, dbdPresetLabel(user.Preset), dbdUserScope(user), login, notes)
	}
	return tw.Flush()
}

// dbdUserScopeFlags are --databases a,b and --all-databases.
type dbdUserScopeFlags struct {
	databases    rtOptString
	allDatabases bool
}

func (f *dbdUserScopeFlags) validate() ([]string, error) {
	list := dbdSplitList(f.databases.value)
	if f.databases.set && f.allDatabases {
		return nil, errors.New("invalid arguments: use --databases or --all-databases, not both")
	}
	if f.databases.set && len(list) == 0 {
		return nil, errors.New("invalid --databases: name at least one database (or use --all-databases)")
	}
	return list, nil
}

func dbdPrintPasswordOnce(user, password string, rotated bool) {
	verb := "created"
	if rotated {
		verb = "rotated"
	}
	_, _ = fmt.Fprintf(rtErr, "Warning: the password of '%s' is printed once, now that it was %s; HubFly cannot show it again. Store it securely.\n", user, verb)
}

func dbdUsersCreateFlow(args []string) error {
	const usage = "hubfly db users create <db> <name> [--preset read_only|read_write|admin] [--databases <a,b>|--all-databases] [--project <id|name>] [--json]"
	fs := newRealtimeFlags("db users create")
	projectRef := fs.String("project", "", "")
	presetText := fs.String("preset", "read_write", "")
	scope := &dbdUserScopeFlags{}
	fs.Var(&scope.databases, "databases", "")
	fs.BoolVar(&scope.allDatabases, "all-databases", false, "")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 2 || strings.TrimSpace(positionals[1]) == "" {
		return dbdUsageError(usage)
	}
	preset, err := dbdNormalizePreset(*presetText)
	if err != nil {
		return err
	}
	databases, err := scope.validate()
	if err != nil {
		return err
	}
	if preset == "admin" && len(databases) > 0 {
		return errors.New("invalid arguments: admin users have access to every database; drop --databases")
	}
	body := map[string]any{"name": strings.TrimSpace(positionals[1]), "preset": preset}
	if len(databases) > 0 {
		body["databases"] = databases
		body["allDatabases"] = false
	} else {
		body["allDatabases"] = true
	}
	target, err := resolveDBDTarget(positionals[0], *projectRef)
	if err != nil {
		return err
	}
	if err := target.require("users"); err != nil {
		return err
	}
	created, err := createDBDUser(target.token, target.instance.ProjectID, target.instance.ID, body)
	if err != nil {
		return target.apiError(err)
	}
	dbdPrintPasswordOnce(created.User.Name, created.Password, false)
	if *asJSON {
		return rtPrintJSON(created)
	}
	_, _ = fmt.Fprintf(rtOut, "Created user '%s' on '%s' (%s, %s).\n", created.User.Name, target.name(), dbdPresetLabel(created.User.Preset), dbdUserScope(created.User))
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "  User:\t%s\n", created.User.Name)
	_, _ = fmt.Fprintf(tw, "  Password:\t%s\n", created.Password)
	return tw.Flush()
}

func dbdUsersUpdateFlow(args []string) error {
	const usage = "hubfly db users update <db> <name> [--preset <preset>] [--databases <a,b>|--all-databases] [--project <id|name>] [--json]"
	fs := newRealtimeFlags("db users update")
	projectRef := fs.String("project", "", "")
	var presetText rtOptString
	fs.Var(&presetText, "preset", "")
	scope := &dbdUserScopeFlags{}
	fs.Var(&scope.databases, "databases", "")
	fs.BoolVar(&scope.allDatabases, "all-databases", false, "")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 2 || strings.TrimSpace(positionals[1]) == "" {
		return dbdUsageError(usage)
	}
	if !presetText.set && !scope.databases.set && !scope.allDatabases {
		return fmt.Errorf("invalid arguments: nothing to update; pass --preset, --databases or --all-databases\nusage: %s", usage)
	}
	preset := ""
	if presetText.set {
		if preset, err = dbdNormalizePreset(presetText.value); err != nil {
			return err
		}
	}
	databases, err := scope.validate()
	if err != nil {
		return err
	}
	if preset == "admin" && len(databases) > 0 {
		return errors.New("invalid arguments: admin users have access to every database; drop --databases")
	}
	name := strings.TrimSpace(positionals[1])
	target, err := resolveDBDTarget(positionals[0], *projectRef)
	if err != nil {
		return err
	}
	if err := target.require("users"); err != nil {
		return err
	}
	users, err := fetchDBDUsers(target.token, target.instance.ProjectID, target.instance.ID)
	if err != nil {
		return target.apiError(err)
	}
	current := dbdFindUser(users, name)
	if current == nil {
		return fmt.Errorf("user '%s' not found on '%s' (list them with: hubfly db users %s)", name, target.name(), target.name())
	}
	if current.IsPrimary {
		return fmt.Errorf("'%s' is the primary admin user of '%s'; its access cannot be changed", name, target.name())
	}
	// The API replaces the user's grants: keep what the flags do not change.
	if preset == "" {
		if current.Preset == "custom" || dbdPresetLabels[current.Preset] == "" {
			return fmt.Errorf("invalid arguments: '%s' has custom privileges (changed outside HubFly); choose one with --preset read_only|read_write|admin", name)
		}
		preset = current.Preset
	}
	updated := dbdUser{Name: name, Preset: preset, IsPrimary: false, CanLogin: current.CanLogin}
	switch {
	case preset == "admin" || scope.allDatabases:
		updated.AllDatabases = true
	case len(databases) > 0:
		updated.Databases = databases
	case current.AllDatabases || len(current.Databases) == 0:
		updated.AllDatabases = true
	default:
		updated.Databases = current.Databases
	}
	body := map[string]any{"preset": preset, "allDatabases": updated.AllDatabases}
	if !updated.AllDatabases {
		body["databases"] = updated.Databases
	} else {
		updated.Databases = []string{}
	}
	if err := updateDBDUser(target.token, target.instance.ProjectID, target.instance.ID, name, body); err != nil {
		return target.apiError(err)
	}
	if *asJSON {
		return rtPrintJSON(updated)
	}
	_, _ = fmt.Fprintf(rtOut, "Updated user '%s' on '%s': %s, %s.\n", name, target.name(), dbdPresetLabel(preset), dbdUserScope(updated))
	return nil
}

func dbdUsersDeleteFlow(args []string) error {
	const usage = "hubfly db users delete <db> <name> [--reassign-to <user>] [--project <id|name>] [--yes]"
	fs := newRealtimeFlags("db users delete")
	projectRef := fs.String("project", "", "")
	reassignTo := fs.String("reassign-to", "", "")
	yes := fs.Bool("yes", false, "")
	fs.BoolVar(yes, "y", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 2 || strings.TrimSpace(positionals[1]) == "" {
		return dbdUsageError(usage)
	}
	name := strings.TrimSpace(positionals[1])
	target, err := resolveDBDTarget(positionals[0], *projectRef)
	if err != nil {
		return err
	}
	if err := target.require("users"); err != nil {
		return err
	}
	users, err := fetchDBDUsers(target.token, target.instance.ProjectID, target.instance.ID)
	if err != nil {
		return target.apiError(err)
	}
	user := dbdFindUser(users, name)
	if user == nil {
		return fmt.Errorf("user '%s' not found on '%s' (list them with: hubfly db users %s)", name, target.name(), target.name())
	}
	if user.IsPrimary {
		return fmt.Errorf("'%s' is the primary admin user of '%s' and cannot be deleted (rotate its password with: hubfly db credentials rotate %s)", name, target.name(), target.name())
	}
	heir := strings.TrimSpace(*reassignTo)
	if heir == name {
		return errors.New("invalid --reassign-to: choose a user other than the one being deleted")
	}
	if heir != "" && dbdFindUser(users, heir) == nil {
		return fmt.Errorf("invalid --reassign-to: user '%s' not found on '%s'", heir, target.name())
	}
	heirText := heir
	if heirText == "" {
		heirText = "the primary admin (" + target.adminUsername() + ")"
	}
	question := fmt.Sprintf("Delete user '%s' on '%s'? Its connections are closed and anything it owns is reassigned to %s; tables and data stay.", name, target.name(), heirText)
	if bindings := dbdUserAppBindings(target, name); len(bindings) > 0 {
		question += fmt.Sprintf(" Apps bound as this user lose access: %s.", dbdBindingProjects(bindings))
	}
	confirmed, err := dbdConfirm(*yes, question, "delete the user")
	if err != nil {
		return err
	}
	if !confirmed {
		_, _ = fmt.Fprintln(rtOut, "Cancelled.")
		return nil
	}
	if _, err := deleteDBDUser(target.token, target.instance.ProjectID, target.instance.ID, name, heir); err != nil {
		return target.apiError(err)
	}
	_, _ = fmt.Fprintf(rtOut, "Deleted user '%s' on '%s'.\n", name, target.name())
	return nil
}

func dbdUsersRotateFlow(args []string) error {
	const usage = "hubfly db users rotate <db> <name> [--project <id|name>] [--yes] [--json]"
	fs := newRealtimeFlags("db users rotate")
	projectRef := fs.String("project", "", "")
	yes := fs.Bool("yes", false, "")
	fs.BoolVar(yes, "y", false, "")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 2 || strings.TrimSpace(positionals[1]) == "" {
		return dbdUsageError(usage)
	}
	name := strings.TrimSpace(positionals[1])
	target, err := resolveDBDTarget(positionals[0], *projectRef)
	if err != nil {
		return err
	}
	if err := target.require("users"); err != nil {
		return err
	}
	users, err := fetchDBDUsers(target.token, target.instance.ProjectID, target.instance.ID)
	if err != nil {
		return target.apiError(err)
	}
	user := dbdFindUser(users, name)
	if user == nil {
		return fmt.Errorf("user '%s' not found on '%s' (list them with: hubfly db users %s)", name, target.name(), target.name())
	}
	if user.IsPrimary {
		return fmt.Errorf("'%s' is the primary admin user of '%s'; rotate its password with: hubfly db credentials rotate %s", name, target.name(), target.name())
	}
	question := fmt.Sprintf("Rotate the password of user '%s' on '%s'? Open connections stay open; new connections need the new password.", name, target.name())
	bindings := dbdUserAppBindings(target, name)
	if len(bindings) > 0 {
		question += fmt.Sprintf(" Apps bound as this user keep the old password in their variables: %s.", dbdBindingProjects(bindings))
	}
	confirmed, err := dbdConfirm(*yes, question, "rotate the password")
	if err != nil {
		return err
	}
	if !confirmed {
		_, _ = fmt.Fprintln(rtOut, "Cancelled.")
		return nil
	}
	rotated, err := rotateDBDUserPassword(target.token, target.instance.ProjectID, target.instance.ID, name)
	if err != nil {
		return target.apiError(err)
	}
	dbdPrintPasswordOnce(name, rotated.Password, true)
	if len(bindings) > 0 {
		_, _ = fmt.Fprintf(rtErr, "Update the variables of the apps bound as '%s' (%s), or re-create their bindings.\n", name, dbdBindingProjects(bindings))
	}
	if *asJSON {
		return rtPrintJSON(rotated)
	}
	_, _ = fmt.Fprintf(rtOut, "Rotated the password of user '%s' on '%s'.\n", name, target.name())
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "  User:\t%s\n", rotated.Name)
	_, _ = fmt.Fprintf(tw, "  Password:\t%s\n", rotated.Password)
	return tw.Flush()
}

// ---------------------------------------------------------------------------
// tokens (SQLite/libSQL)
// ---------------------------------------------------------------------------

func dbdTokenAccessLabel(access string) string {
	if access == "read_only" {
		return "read only"
	}
	return "full access"
}

func dbdTokensListFlow(args []string) error {
	const usage = "hubfly db tokens [list] <db> [--project <id|name>] [--json]"
	fs := newRealtimeFlags("db tokens list")
	projectRef := fs.String("project", "", "")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return dbdUsageError(usage)
	}
	target, err := resolveDBDTarget(positionals[0], *projectRef)
	if err != nil {
		return err
	}
	if err := target.require("tokens"); err != nil {
		return err
	}
	tokens, err := fetchDBDTokens(target.token, target.instance.ProjectID, target.instance.ID)
	if err != nil {
		return target.apiError(err)
	}
	if *asJSON {
		return rtPrintJSON(tokens)
	}
	if len(tokens) == 0 {
		_, _ = fmt.Fprintf(rtOut, "No tokens on '%s'. Create one with: hubfly db tokens create %s <name>\n", target.name(), target.name())
		return nil
	}
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tNAME\tACCESS\tSTATUS\tCREATED\tEXPIRES")
	for _, token := range tokens {
		status := "active"
		if !token.Active {
			status = "revoked or expired"
		}
		expires := "never"
		if token.ExpiresAt != nil {
			expires = dbdTime(*token.ExpiresAt)
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", token.ID, token.Name, dbdTokenAccessLabel(token.Access), status, dbdTime(token.CreatedAt), expires)
	}
	return tw.Flush()
}

func dbdTokensCreateFlow(args []string) error {
	const usage = "hubfly db tokens create <db> <name> [--access read_only|full_access] [--expires <days>] [--project <id|name>] [--json]"
	fs := newRealtimeFlags("db tokens create")
	projectRef := fs.String("project", "", "")
	accessText := fs.String("access", "full_access", "")
	expiresText := fs.String("expires", "", "")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 2 || strings.TrimSpace(positionals[1]) == "" {
		return dbdUsageError(usage)
	}
	access := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(*accessText), "-", "_"))
	switch access {
	case "read_only", "readonly", "ro":
		access = "read_only"
	case "full_access", "full", "rw", "read_write":
		access = "full_access"
	default:
		return fmt.Errorf("invalid --access %q (use read_only or full_access)", *accessText)
	}
	days := 0
	if text := strings.TrimSuffix(strings.TrimSpace(*expiresText), "d"); text != "" {
		days, err = strconv.Atoi(text)
		if err != nil || days < 1 || days > 3650 {
			return fmt.Errorf("invalid --expires %q (use a number of days, 1-3650)", *expiresText)
		}
	}
	target, err := resolveDBDTarget(positionals[0], *projectRef)
	if err != nil {
		return err
	}
	if err := target.require("tokens"); err != nil {
		return err
	}
	created, err := createDBDToken(target.token, target.instance.ProjectID, target.instance.ID, strings.TrimSpace(positionals[1]), access, days)
	if err != nil {
		return target.apiError(err)
	}
	_, _ = fmt.Fprintf(rtErr, "Warning: token '%s' is printed once, now; HubFly cannot show it again. Store it securely.\n", created.Token.Name)
	if *asJSON {
		return rtPrintJSON(created)
	}
	expires := "never expires"
	if created.Token.ExpiresAt != nil {
		expires = "expires " + dbdTime(*created.Token.ExpiresAt)
	}
	_, _ = fmt.Fprintf(rtOut, "Created token '%s' on '%s' (%s, %s).\n", created.Token.Name, target.name(), dbdTokenAccessLabel(created.Token.Access), expires)
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "  ID:\t%s\n", created.Token.ID)
	_, _ = fmt.Fprintf(tw, "  Token:\t%s\n", created.Value)
	if target.detail.Endpoint != nil && target.detail.Endpoint.Host != "" {
		_, _ = fmt.Fprintf(tw, "  URL:\tlibsql://%s\n", target.detail.Endpoint.Host)
	}
	return tw.Flush()
}

func dbdTokensRevokeFlow(args []string) error {
	const usage = "hubfly db tokens revoke <db> <tokenId> [--project <id|name>] [--yes]"
	fs := newRealtimeFlags("db tokens revoke")
	projectRef := fs.String("project", "", "")
	yes := fs.Bool("yes", false, "")
	fs.BoolVar(yes, "y", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 2 || strings.TrimSpace(positionals[1]) == "" {
		return dbdUsageError(usage)
	}
	ref := strings.TrimSpace(positionals[1])
	target, err := resolveDBDTarget(positionals[0], *projectRef)
	if err != nil {
		return err
	}
	if err := target.require("tokens"); err != nil {
		return err
	}
	tokens, err := fetchDBDTokens(target.token, target.instance.ProjectID, target.instance.ID)
	if err != nil {
		return target.apiError(err)
	}
	token, err := dbdPickToken(tokens, ref, target.name())
	if err != nil {
		return err
	}
	question := fmt.Sprintf("Revoke token '%s' (%s) on '%s'? SQLite (libSQL) cannot revoke a single token: this rotates the signing key and invalidates EVERY token of the instance. Apps connected with hubfly db bind get a new token (redeploy them); other apps lose access until you create new tokens for them.",
		token.Name, token.ID, target.name())
	confirmed, err := dbdConfirm(*yes, question, "revoke every token")
	if err != nil {
		return err
	}
	if !confirmed {
		_, _ = fmt.Fprintln(rtOut, "Cancelled.")
		return nil
	}
	result, err := revokeDBDToken(target.token, target.instance.ProjectID, target.instance.ID, token.ID)
	if err != nil {
		return target.apiError(err)
	}
	_, _ = fmt.Fprintf(rtOut, "Revoked every token of '%s', not only '%s' (%s; the signing key was rotated).\n", target.name(), token.Name, dbdPlural(int64(result.Revoked), "token", "tokens"))
	if result.AppsReconnected > 0 {
		_, _ = fmt.Fprintf(rtOut, "%s received a new token; redeploy them to use it.\n", dbdPlural(int64(result.AppsReconnected), "bound app", "bound apps"))
	}
	return nil
}

// dbdPickToken finds a token by id, then by name.
func dbdPickToken(tokens []dbdToken, ref, instanceName string) (dbdToken, error) {
	for _, token := range tokens {
		if token.ID == ref {
			return token, nil
		}
	}
	var byName []dbdToken
	for _, token := range tokens {
		if token.Name == ref {
			byName = append(byName, token)
		}
	}
	if len(byName) == 1 {
		return byName[0], nil
	}
	if len(byName) > 1 {
		ids := make([]string, 0, len(byName))
		for _, token := range byName {
			ids = append(ids, token.ID)
		}
		return dbdToken{}, fmt.Errorf("token name '%s' is ambiguous (%s); use the id", ref, strings.Join(ids, ", "))
	}
	return dbdToken{}, fmt.Errorf("token '%s' not found on '%s' (list them with: hubfly db tokens %s)", ref, instanceName, instanceName)
}

// ---------------------------------------------------------------------------
// extensions (PostgreSQL)
// ---------------------------------------------------------------------------

// dbdExtensionsDatabase is the database the extension commands use.
func dbdExtensionsDatabase(target dbdTarget, database string) string {
	if database != "" {
		return database
	}
	if name := target.defaultDatabase(); name != "" {
		return name
	}
	return "app"
}

func dbdExtensionsListFlow(args []string) error {
	const usage = "hubfly db extensions [list] <db> [--database <name>] [--project <id|name>] [--json]"
	fs := newRealtimeFlags("db extensions list")
	projectRef := fs.String("project", "", "")
	database := fs.String("database", "", "")
	fs.StringVar(database, "d", "", "")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return dbdUsageError(usage)
	}
	target, err := resolveDBDTarget(positionals[0], *projectRef)
	if err != nil {
		return err
	}
	if err := target.require("extensions"); err != nil {
		return err
	}
	extensions, err := fetchDBDExtensions(target.token, target.instance.ProjectID, target.instance.ID, strings.TrimSpace(*database))
	if err != nil {
		return target.apiError(err)
	}
	if *asJSON {
		return rtPrintJSON(extensions)
	}
	databaseName := dbdExtensionsDatabase(target, strings.TrimSpace(*database))
	if len(extensions) == 0 {
		_, _ = fmt.Fprintf(rtOut, "No extensions are available in database '%s'.\n", databaseName)
		return nil
	}
	_, _ = fmt.Fprintf(rtOut, "Extensions in database '%s' of '%s':\n", databaseName, target.name())
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAME\tINSTALLED\tAVAILABLE\tALLOWED\tDESCRIPTION")
	for _, extension := range extensions {
		allowed := "yes"
		if !extension.Allowed {
			allowed = "no"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", extension.Name, dbdText(extension.InstalledVersion, "-"),
			dbdText(extension.DefaultVersion, "-"), allowed, dbdOneLine(dbdText(extension.Description, ""), 60))
	}
	return tw.Flush()
}

func dbdFindExtension(extensions []dbdExtension, name string) *dbdExtension {
	for index := range extensions {
		if strings.EqualFold(extensions[index].Name, name) {
			return &extensions[index]
		}
	}
	return nil
}

func dbdExtensionsInstallFlow(args []string) error {
	const usage = "hubfly db extensions install <db> <name> [--database <name>] [--project <id|name>]"
	fs := newRealtimeFlags("db extensions install")
	projectRef := fs.String("project", "", "")
	database := fs.String("database", "", "")
	fs.StringVar(database, "d", "", "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 2 || strings.TrimSpace(positionals[1]) == "" {
		return dbdUsageError(usage)
	}
	name := strings.TrimSpace(positionals[1])
	target, err := resolveDBDTarget(positionals[0], *projectRef)
	if err != nil {
		return err
	}
	if err := target.require("extensions"); err != nil {
		return err
	}
	databaseName := dbdExtensionsDatabase(target, strings.TrimSpace(*database))
	extensions, err := fetchDBDExtensions(target.token, target.instance.ProjectID, target.instance.ID, strings.TrimSpace(*database))
	if err != nil {
		return target.apiError(err)
	}
	extension := dbdFindExtension(extensions, name)
	switch {
	case extension == nil:
		return fmt.Errorf("extension '%s' is not available on '%s' (list them with: hubfly db extensions %s)", name, target.name(), target.name())
	case !extension.Allowed:
		return fmt.Errorf("extension '%s' is not on HubFly's allowlist, so it cannot be installed", extension.Name)
	case extension.InstalledVersion != nil && *extension.InstalledVersion != "":
		_, _ = fmt.Fprintf(rtOut, "Extension '%s' %s is already installed in database '%s'.\n", extension.Name, *extension.InstalledVersion, databaseName)
		return nil
	}
	if err := installDBDExtension(target.token, target.instance.ProjectID, target.instance.ID, strings.TrimSpace(*database), extension.Name); err != nil {
		return target.apiError(err)
	}
	_, _ = fmt.Fprintf(rtOut, "Installed extension '%s' in database '%s' of '%s'.\n", extension.Name, databaseName, target.name())
	return nil
}

func dbdExtensionsRemoveFlow(args []string) error {
	const usage = "hubfly db extensions remove <db> <name> [--database <name>] [--cascade] [--project <id|name>] [--yes]"
	fs := newRealtimeFlags("db extensions remove")
	projectRef := fs.String("project", "", "")
	database := fs.String("database", "", "")
	fs.StringVar(database, "d", "", "")
	cascade := fs.Bool("cascade", false, "")
	yes := fs.Bool("yes", false, "")
	fs.BoolVar(yes, "y", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 2 || strings.TrimSpace(positionals[1]) == "" {
		return dbdUsageError(usage)
	}
	name := strings.TrimSpace(positionals[1])
	target, err := resolveDBDTarget(positionals[0], *projectRef)
	if err != nil {
		return err
	}
	if err := target.require("extensions"); err != nil {
		return err
	}
	databaseName := dbdExtensionsDatabase(target, strings.TrimSpace(*database))
	dependents := "The removal fails if objects depend on it; --cascade drops them too."
	if *cascade {
		dependents = "Objects that depend on it (columns, indexes, functions, ...) are dropped too (--cascade)."
	}
	question := fmt.Sprintf("Remove extension '%s' from database '%s' of '%s'? %s", name, databaseName, target.name(), dependents)
	confirmed, err := dbdConfirm(*yes, question, "remove the extension")
	if err != nil {
		return err
	}
	if !confirmed {
		_, _ = fmt.Fprintln(rtOut, "Cancelled.")
		return nil
	}
	if _, err := removeDBDExtension(target.token, target.instance.ProjectID, target.instance.ID, strings.TrimSpace(*database), name, *cascade); err != nil {
		var apiErr *apiError
		if !*cascade && errors.As(err, &apiErr) && apiErr.Code == "MANAGED_ENGINE_ERROR" {
			return fmt.Errorf("%w\nIf other objects depend on the extension, re-run with --cascade to drop them too.", err)
		}
		return target.apiError(err)
	}
	_, _ = fmt.Fprintf(rtOut, "Removed extension '%s' from database '%s' of '%s'.\n", name, databaseName, target.name())
	return nil
}

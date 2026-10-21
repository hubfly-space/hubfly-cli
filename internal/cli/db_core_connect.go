package cli

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
)

// `hubfly db url | env | credentials | connect | shell`: connection details,
// secrets (shown only with --reveal; reveals are audited by HubFly), the local
// psql/mysql client and the in-browser-style terminal shell.

var (
	// dbcExit ends the process with the database client's exit status (tests
	// override it).
	dbcExit = os.Exit
	// dbcLookPath finds the database client binary (tests override it).
	dbcLookPath = exec.LookPath
)

const dbMaskedSecret = "****"

// encodeURIComponent matches JavaScript's encodeURIComponent, which the
// Dashboard uses when it writes DATABASE_URL for bindings.
func encodeURIComponent(value string) string {
	var out strings.Builder
	for _, b := range []byte(value) {
		switch {
		case b >= 'A' && b <= 'Z', b >= 'a' && b <= 'z', b >= '0' && b <= '9',
			strings.IndexByte("-_.!~*'()", b) >= 0:
			out.WriteByte(b)
		default:
			fmt.Fprintf(&out, "%%%02X", b)
		}
	}
	return out.String()
}

func dbClientName(engine string) string {
	if engine == "mysql" {
		return "mysql"
	}
	return "psql"
}

// buildDBConnectionURL builds the URL in the format the Dashboard writes for
// bindings: <scheme>://user:password@host:port/database?<tls>.
func buildDBConnectionURL(connection dbcConnection, username, password, database string) (string, error) {
	if connection.Engine == "libsql" {
		if url := derefString(connection.LibsqlURL); url != "" {
			return url, nil
		}
		if connection.Host == "" {
			return "", errors.New("the database has no public endpoint")
		}
		return "libsql://" + connection.Host, nil
	}
	if connection.Host == "" {
		return "", errors.New("the database has no public endpoint")
	}
	if database == "" {
		database = derefString(connection.Database)
	}
	scheme, tls := "postgresql", "sslmode=require"
	if connection.Engine == "mysql" {
		scheme, tls = "mysql", "ssl-mode=REQUIRED"
	}
	port := ""
	if connection.Port != nil {
		port = strconv.Itoa(*connection.Port)
	}
	return fmt.Sprintf("%s://%s:%s@%s:%s/%s?%s", scheme, encodeURIComponent(username), encodeURIComponent(password),
		connection.Host, port, encodeURIComponent(database), tls), nil
}

// dbConnectionReady explains why a database cannot be reached yet.
func dbConnectionReady(name string, connection dbcConnection) error {
	if !connection.PublicAccess {
		return fmt.Errorf("database '%s' has no public endpoint: public access is off (turn it on with: hubfly db network public %s on)", name, name)
	}
	if !connection.Ready || connection.Host == "" {
		return fmt.Errorf("the endpoint of database '%s' is still being prepared; try again shortly (status: hubfly db info %s)", name, name)
	}
	return nil
}

// revealDBPassword reveals the admin password and notes the audit entry on
// stderr.
func revealDBPassword(token string, instance rtInstance) (dbcSecret, error) {
	secret, err := revealDBCredentials(token, instance)
	if err != nil {
		return secret, dbAPIError(instance.Name, err)
	}
	_, _ = fmt.Fprintln(rtErr, "Revealed the admin password (HubFly records reveals in the audit log).")
	return secret, nil
}

type dbConnectTarget struct {
	token      string
	instance   rtInstance
	connection dbcConnection
}

func resolveDBConnection(ref, projectRef string) (dbConnectTarget, error) {
	token, err := ensureAuth(true)
	if err != nil {
		return dbConnectTarget{}, err
	}
	instance, err := resolveDatabaseInstance(token, ref, projectRef)
	if err != nil {
		return dbConnectTarget{}, err
	}
	connection, err := fetchDBConnection(token, instance)
	if err != nil {
		return dbConnectTarget{}, dbAPIError(instance.Name, err)
	}
	return dbConnectTarget{token: token, instance: instance, connection: connection}, nil
}

func libsqlTokenHint(name string) string {
	return fmt.Sprintf("Authenticate with a token: hubfly db tokens create %s <name> (a token is shown once, when it is created).", name)
}

// dbURLVariables returns the variables `db url`/`db env` print. The password
// is masked unless reveal is set.
func dbURLVariables(target dbConnectTarget, database string, reveal bool) ([]rtEnvVar, bool, error) {
	connection := target.connection
	if err := dbConnectionReady(target.instance.Name, connection); err != nil {
		return nil, false, err
	}
	if connection.Engine == "libsql" {
		url, err := buildDBConnectionURL(connection, "", "", "")
		if err != nil {
			return nil, false, err
		}
		return []rtEnvVar{{Key: "LIBSQL_URL", Value: url}}, false, nil
	}
	username, password := derefString(connection.Username), dbMaskedSecret
	if reveal {
		secret, err := revealDBPassword(target.token, target.instance)
		if err != nil {
			return nil, false, err
		}
		username, password = firstNonEmpty(secret.Username, username), secret.Value
	}
	url, err := buildDBConnectionURL(connection, username, password, database)
	if err != nil {
		return nil, false, err
	}
	return []rtEnvVar{{Key: "DATABASE_URL", Value: url, Secret: reveal}}, reveal, nil
}

// ---------------------------------------------------------------------------
// url / env
// ---------------------------------------------------------------------------

func dbURLFlow(args []string) error {
	const usage = "hubfly db url <db> [--database <name>] [--reveal] [--project <id|name>] [--json]"
	fs := newRealtimeFlags("db url")
	projectRef := fs.String("project", "", "")
	database := fs.String("database", "", "")
	reveal := fs.Bool("reveal", false, "")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return fmt.Errorf("usage: %s", usage)
	}
	target, err := resolveDBConnection(positionals[0], *projectRef)
	if err != nil {
		return err
	}
	if target.connection.Engine == "libsql" && *database != "" {
		return errors.New("invalid arguments: --database does not apply to SQLite (libSQL)")
	}
	variables, revealed, err := dbURLVariables(target, strings.TrimSpace(*database), *reveal)
	if err != nil {
		return err
	}
	url := variables[0].Value
	if *asJSON {
		result := map[string]any{"engine": target.connection.Engine, "url": url, "passwordRevealed": revealed}
		return rtPrintJSON(result)
	}
	_, _ = fmt.Fprintln(rtOut, url)
	switch {
	case target.connection.Engine == "libsql":
		_, _ = fmt.Fprintln(rtErr, libsqlTokenHint(target.instance.Name))
	case !revealed:
		_, _ = fmt.Fprintln(rtErr, "The password is hidden; add --reveal to include it.")
	}
	return nil
}

func dbEnvFlow(args []string) error {
	const usage = "hubfly db env <db> [--database <name>] [--reveal] [--format dotenv|json] [--project <id|name>]"
	fs := newRealtimeFlags("db env")
	projectRef := fs.String("project", "", "")
	database := fs.String("database", "", "")
	reveal := fs.Bool("reveal", false, "")
	format := fs.String("format", "dotenv", "")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return fmt.Errorf("usage: %s", usage)
	}
	if *asJSON {
		*format = "json"
	}
	if *format != "dotenv" && *format != "json" {
		return fmt.Errorf("invalid --format %q (use dotenv or json)", *format)
	}
	target, err := resolveDBConnection(positionals[0], *projectRef)
	if err != nil {
		return err
	}
	if target.connection.Engine == "libsql" && *database != "" {
		return errors.New("invalid arguments: --database does not apply to SQLite (libSQL)")
	}
	variables, revealed, err := dbURLVariables(target, strings.TrimSpace(*database), *reveal)
	if err != nil {
		return err
	}
	if *format == "json" {
		values := map[string]string{}
		for _, variable := range variables {
			values[variable.Key] = variable.Value
		}
		if err := rtPrintJSON(values); err != nil {
			return err
		}
	} else {
		for _, variable := range variables {
			_, _ = fmt.Fprintf(rtOut, "%s=%s\n", variable.Key, formatDotenvValue(variable.Value))
		}
	}
	switch {
	case target.connection.Engine == "libsql":
		_, _ = fmt.Fprintln(rtErr, "LIBSQL_AUTH_TOKEN is not printed: "+libsqlTokenHint(target.instance.Name))
	case !revealed:
		_, _ = fmt.Fprintln(rtErr, "The password is hidden; add --reveal to include it.")
	}
	return nil
}

// ---------------------------------------------------------------------------
// credentials
// ---------------------------------------------------------------------------

func runDBCredentialsGroup(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "rotate", "reset":
			return dbCredentialsRotateFlow(args[1:])
		case "show", "reveal":
			return dbCredentialsShowFlow(args[1:], args[0] == "reveal")
		}
	}
	return dbCredentialsShowFlow(args, false)
}

func dbCredentialsShowFlow(args []string, revealDefault bool) error {
	const usage = "hubfly db credentials <db> [--reveal] [--project <id|name>] [--json]"
	fs := newRealtimeFlags("db credentials")
	projectRef := fs.String("project", "", "")
	reveal := fs.Bool("reveal", revealDefault, "")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return fmt.Errorf("usage: %s", usage)
	}
	target, err := resolveDBConnection(positionals[0], *projectRef)
	if err != nil {
		return err
	}
	connection := target.connection
	if connection.Engine == "libsql" || connection.SecretKind == "token" {
		if *asJSON {
			return rtPrintJSON(map[string]any{"kind": "token", "credentialsRotatedAt": connection.CredentialsRotatedAt})
		}
		_, _ = fmt.Fprintf(rtOut, "Database '%s' (SQLite/libSQL) authenticates with tokens, not a password.\n", target.instance.Name)
		_, _ = fmt.Fprintf(rtOut, "List them with: hubfly db tokens list %s\n", target.instance.Name)
		_, _ = fmt.Fprintln(rtOut, libsqlTokenHint(target.instance.Name))
		return nil
	}
	username, password := derefString(connection.Username), dbMaskedSecret
	if *reveal {
		secret, revealErr := revealDBPassword(target.token, target.instance)
		if revealErr != nil {
			return revealErr
		}
		username, password = firstNonEmpty(secret.Username, username), secret.Value
	}
	if *asJSON {
		result := map[string]any{"kind": "password", "username": username, "password": nil, "credentialsRotatedAt": connection.CredentialsRotatedAt}
		if *reveal {
			result["password"] = password
		}
		return rtPrintJSON(result)
	}
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "Username:\t%s\n", username)
	_, _ = fmt.Fprintf(tw, "Password:\t%s\n", password)
	if connection.CredentialsRotatedAt != nil {
		_, _ = fmt.Fprintf(tw, "Rotated:\t%s\n", *connection.CredentialsRotatedAt)
	}
	_ = tw.Flush()
	if !*reveal {
		_, _ = fmt.Fprintln(rtErr, "The password is hidden; add --reveal to show it.")
	}
	return nil
}

func dbCredentialsRotateFlow(args []string) error {
	const usage = "hubfly db credentials rotate <db> [--yes] [--project <id|name>] [--json]"
	fs := newRealtimeFlags("db credentials rotate")
	projectRef := fs.String("project", "", "")
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
	target, err := resolveDBConnection(positionals[0], *projectRef)
	if err != nil {
		return err
	}
	name := target.instance.Name
	libsql := target.connection.Engine == "libsql"
	if !*yes {
		if !rtInteractive() {
			return errors.New("invalid arguments: confirmation required; re-run with --yes to rotate")
		}
		if libsql {
			_, _ = fmt.Fprintf(rtOut, "Rotating the signing key of '%s' revokes every token. Each app binding gets a fresh token in its LIBSQL_AUTH_TOKEN variable (apps pick it up on their next deploy); other clients need a new token.\n", name)
		} else {
			_, _ = fmt.Fprintf(rtOut, "Rotating sets a new admin password for '%s'; the old one stops working. Bindings that use the admin user get the new DATABASE_URL (apps pick it up on their next deploy); bindings with a dedicated user keep theirs. Update every other client that uses the old password.\n", name)
		}
		confirmed, promptErr := promptYesNo("Rotate the credentials?", false)
		if promptErr != nil {
			return promptErr
		}
		if !confirmed {
			_, _ = fmt.Fprintln(rtOut, "Cancelled.")
			return nil
		}
	}
	secret, err := rotateDBCredentials(target.token, target.instance)
	if err != nil {
		return dbAPIError(name, err)
	}
	if *asJSON {
		if secret.Kind == "signing_key" {
			return rtPrintJSON(map[string]any{"kind": secret.Kind, "revokedTokens": secret.RevokedTokens, "appsReconnected": secret.AppsReconnected})
		}
		return rtPrintJSON(map[string]any{"kind": secret.Kind, "username": secret.Username, "password": secret.Value})
	}
	if secret.Kind == "signing_key" {
		_, _ = fmt.Fprintf(rtOut, "Rotated the signing key of '%s': %d tokens revoked, %d app bindings received a new token.\n", name, secret.RevokedTokens, secret.AppsReconnected)
		_, _ = fmt.Fprintf(rtOut, "Create tokens for other clients with: hubfly db tokens create %s <name>\n", name)
		return nil
	}
	_, _ = fmt.Fprintf(rtOut, "Rotated the admin password of '%s'. It is shown once:\n", name)
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "  Username:\t%s\n", secret.Username)
	_, _ = fmt.Fprintf(tw, "  Password:\t%s\n", secret.Value)
	_ = tw.Flush()
	_, _ = fmt.Fprintf(rtOut, "Bindings with the admin user were updated; redeploy their apps to use it. Print the URL any time with: hubfly db url %s --reveal\n", name)
	return nil
}

// ---------------------------------------------------------------------------
// connect / shell
// ---------------------------------------------------------------------------

// ipAllowed reports whether ip falls in one of the allowlist ranges. An empty
// allowlist allows every address.
func ipAllowed(ip string, entries []mlAllowlistEntry) bool {
	if len(entries) == 0 {
		return true
	}
	parsed := net.ParseIP(strings.TrimSpace(ip))
	if parsed == nil {
		return false
	}
	for _, entry := range entries {
		if _, network, err := net.ParseCIDR(entry.CIDR); err == nil && network.Contains(parsed) {
			return true
		}
	}
	return false
}

func dbClientInstallHint(engine string) string {
	if engine == "mysql" {
		return "mysql was not found in PATH. Install the MySQL client (for example: apt install mysql-client, brew install mysql-client), or use: hubfly db shell / hubfly db query"
	}
	return "psql was not found in PATH. Install the PostgreSQL client (for example: apt install postgresql-client, brew install libpq), or use: hubfly db shell / hubfly db query"
}

// mysqlOptionValue quotes a value for a MySQL option file.
func mysqlOptionValue(value string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value) + `"`
}

func dbConnectFlow(args []string) error {
	const usage = "hubfly db connect <db> [--database <name>] [--project <id|name>] [-- <client args>...]"
	fs := newRealtimeFlags("db connect")
	projectRef := fs.String("project", "", "")
	database := fs.String("database", "", "")
	clientArgs := []string{}
	for index, arg := range args {
		if arg == "--" {
			clientArgs = append(clientArgs, args[index+1:]...)
			args = args[:index]
			break
		}
	}
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return fmt.Errorf("usage: %s", usage)
	}
	target, err := resolveDBConnection(positionals[0], *projectRef)
	if err != nil {
		return err
	}
	name, connection := target.instance.Name, target.connection
	if connection.Engine == "libsql" {
		_, _ = fmt.Fprintf(rtOut, "Database '%s' is SQLite (libSQL), which is reached over HTTPS, not with a terminal client.\n", name)
		if url := derefString(connection.LibsqlURL); url != "" {
			_, _ = fmt.Fprintf(rtOut, "URL: %s\n", url)
		}
		_, _ = fmt.Fprintln(rtOut, libsqlTokenHint(name))
		_, _ = fmt.Fprintf(rtOut, "Run SQL from here with: hubfly db query %s \"select 1\"\n", name)
		return nil
	}
	if err := dbConnectionReady(name, connection); err != nil {
		return err
	}
	network, err := fetchManagedNetwork(target.token, target.instance)
	if err != nil {
		return dbAPIError(name, err)
	}
	if len(network.Allowlist) > 0 {
		clientIP, ipErr := fetchManagedClientIP(target.token)
		switch {
		case ipErr != nil || clientIP.IP == nil || *clientIP.IP == "":
			_, _ = fmt.Fprintf(rtErr, "Could not detect your IP address. If the connection is refused, allow it with: hubfly db network allow %s <your-ip>\n", name)
		case !ipAllowed(*clientIP.IP, network.Allowlist):
			return fmt.Errorf("your IP address %s is not in the allowlist of database '%s'; allow it with: hubfly db network allow %s --my-ip", *clientIP.IP, name, name)
		}
	}
	client := dbClientName(connection.Engine)
	clientPath, err := dbcLookPath(client)
	if err != nil {
		return errors.New(dbClientInstallHint(connection.Engine))
	}
	secret, err := revealDBPassword(target.token, target.instance)
	if err != nil {
		return err
	}
	username := firstNonEmpty(secret.Username, derefString(connection.Username))
	dbName := firstNonEmpty(strings.TrimSpace(*database), derefString(connection.Database))
	port := ""
	if connection.Port != nil {
		port = strconv.Itoa(*connection.Port)
	}

	env := os.Environ()
	var argv []string
	cleanup := func() {}
	if connection.Engine == "mysql" {
		// The password goes into a private option file, never onto argv.
		dir, dirErr := os.MkdirTemp("", "hubfly-mysql-")
		if dirErr != nil {
			return dirErr
		}
		cleanup = func() { _ = os.RemoveAll(dir) }
		optionFile := filepath.Join(dir, "client.cnf")
		content := fmt.Sprintf("[client]\nhost=%s\nport=%s\nuser=%s\npassword=%s\nssl-mode=REQUIRED\n",
			mysqlOptionValue(connection.Host), port, mysqlOptionValue(username), mysqlOptionValue(secret.Value))
		if writeErr := os.WriteFile(optionFile, []byte(content), 0o600); writeErr != nil {
			cleanup()
			return writeErr
		}
		argv = append([]string{"--defaults-extra-file=" + optionFile}, clientArgs...)
		if dbName != "" {
			argv = append(argv, dbName)
		}
	} else {
		// psql reads the password from PGPASSWORD, never from argv.
		env = append(env, "PGPASSWORD="+secret.Value, "PGSSLMODE=require")
		argv = append([]string{"-h", connection.Host, "-p", port, "-U", username, "-d", dbName}, clientArgs...)
	}
	_, _ = fmt.Fprintf(rtErr, "Connecting to '%s' with %s (TLS required)...\n", name, client)
	cmd := exec.Command(clientPath, argv...)
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	runErr := cmd.Run()
	cleanup()
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		dbcExit(exitErr.ExitCode())
		return nil
	}
	if runErr != nil {
		return fmt.Errorf("failed to run %s: %w", client, runErr)
	}
	return nil
}

func dbShellFlow(args []string) error {
	const usage = "hubfly db shell <db> [--project <id|name>]"
	fs := newRealtimeFlags("db shell")
	projectRef := fs.String("project", "", "")
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
	if instance.Engine == "libsql" {
		return fmt.Errorf("database '%s' is SQLite (libSQL), which has no terminal shell; run SQL with: hubfly db query %s \"select 1\"", instance.Name, instance.Name)
	}
	if instance.Status != "running" {
		return fmt.Errorf("database '%s' is %s; start it first: hubfly db start %s", instance.Name, managedStatusLabel(instance), instance.Name)
	}
	session, err := createDBShellSession(token, instance)
	if err != nil {
		var apiErr *apiError
		if errors.As(err, &apiErr) && apiErr.Code == "MANAGED_FEATURE_UNSUPPORTED" {
			return fmt.Errorf("%w\nThis engine has no terminal shell; run SQL with: hubfly db query %s", err, instance.Name)
		}
		return dbAPIError(instance.Name, err)
	}
	_, _ = fmt.Fprintf(rtErr, "Opening %s on database '%s' (exit with Ctrl+D)...\n", dbClientName(instance.Engine), instance.Name)
	return runTerminalSession(session, "database "+instance.Name)
}

package cli

import (
	"fmt"
)

// `hubfly db` (aliases `database`, `databases`): Managed Databases —
// PostgreSQL, MySQL and SQLite (libSQL) instances that HubFly operates.
// Lifecycle commands shared with `hubfly realtime` live in
// managed_lifecycle.go, logs/metrics/operations in managed_observe.go.

const dbUsageText = `Managed Databases (PostgreSQL, MySQL and SQLite/libSQL operated by HubFly):
  hubfly db list [--project <id|name>] [--json]
  hubfly db engines [--json]
  hubfly db info <db> [--json]
  hubfly db create [<name>] [--engine postgresql|mysql|libsql] [--version <v>]
                   [--size <preset>|--cpu <n> --memory <mib>] [--storage <gib>]
                   [--project <id|name|new>] [--project-name <name>]
                   [--database <name>] [--admin-user <name>]
                   [--private] [--allow <cidr>]... [--allow-my-ip]
                   [--yes] [--no-wait] [--timeout <duration>] [--json]
  hubfly db delete <db> [--yes] [--wait]
  hubfly db start|stop|restart <db> [--no-wait] [--json]
  hubfly db resize <db> [--size <preset>|--cpu <n> --memory <mib>] [--storage <gib>] [--restart] [--yes] [--no-wait] [--json]
  hubfly db upgrade <db> [--yes] [--no-wait]
  hubfly db rename <db> <new-name>
  hubfly db protect <db> on|off

Connect:
  hubfly db url <db> [--database <name>] [--reveal] [--json]
  hubfly db env <db> [--database <name>] [--reveal] [--format dotenv|json]
  hubfly db credentials <db> [--reveal] [--json]
  hubfly db credentials rotate <db> [--yes] [--json]
  hubfly db connect <db> [--database <name>] [-- <client args>...]
  hubfly db shell <db>
  hubfly db cert <db> [--output <file>]

Data:
  hubfly db query <db> [<sql>|--file <path|->] [--database <name>] [--write] [--max-rows <n>]
                  [--timeout <duration>] [--format table|json|csv]
  hubfly db databases [list] <db> [--json]
  hubfly db databases create <db> <name> [--owner <user>] [--json]
  hubfly db databases delete <db> <name> [--force] [--yes]
  hubfly db users [list] <db> [--json]
  hubfly db users create <db> <name> [--preset read_only|read_write|admin] [--databases <a,b>|--all-databases] [--json]
  hubfly db users update <db> <name> [--preset <preset>] [--databases <a,b>|--all-databases] [--json]
  hubfly db users delete <db> <name> [--reassign-to <user>] [--yes]
  hubfly db users rotate <db> <name> [--yes] [--json]
  hubfly db tokens [list] <db> [--json]                                  (SQLite/libSQL)
  hubfly db tokens create <db> <name> [--access read_only|full_access] [--expires <days>] [--json]
  hubfly db tokens revoke <db> <tokenId> [--yes]
  hubfly db extensions [list] <db> [--database <name>] [--json]          (PostgreSQL)
  hubfly db extensions install <db> <name> [--database <name>]
  hubfly db extensions remove <db> <name> [--database <name>] [--cascade] [--yes]
  hubfly db export <db> [--database <name>] [--format native|sql] [--output <file>] [--no-wait]
  hubfly db exports [list] <db> [--json]
  hubfly db exports download <db> <exportId> [--output <file>]
  hubfly db import <db> <file> [--database <name>] [--format native|sql] [--yes] [--no-wait]

Network and apps:
  hubfly db network <db> [--json]
  hubfly db network public <db> on|off [--yes]
  hubfly db network allow <db> (<cidr>|--my-ip) [--label <text>]
  hubfly db network remove <db> <cidr>
  hubfly db bind <db> --to <project> [--env <NAME>] [--database <name>] [--new-user [--access read_only|read_write]] [--json]
  hubfly db bind list <db> [--json]
  hubfly db bind remove <db> <bindingId> [--keep-variable] [--yes]

Operate:
  hubfly db logs <db> [--tail <bytes>] [--follow]
  hubfly db metrics <db> [--window <duration>] [--json]
  hubfly db operations <db> [--limit <n>] [--json]
  hubfly db sessions [list] <db> [--json]
  hubfly db sessions cancel|terminate <db> <sessionId> [--yes]
  hubfly db insights <db> [--order-by total_time|mean_time|calls] [--limit <n>] [--json]
  hubfly db insights reset <db> [--yes]
  hubfly db config <db> [--json]
  hubfly db config set <db> <key>=<value>... [--restart now|later] [--note <text>] [--yes]
  hubfly db config history <db> [--limit <n>] [--json]
  hubfly db config revert <db> <revisionId> [--restart now|later] [--yes]

<db> is a database instance id or name (narrow with --project). Sizes: nano, small, medium,
large, xlarge (see: hubfly db engines). HubFly chooses the region. Secrets are printed only
with --reveal or once when they are created. "database" and "databases" are aliases of "db".`

func printDBUsage() {
	_, _ = fmt.Fprintln(rtOut, dbUsageText)
}

func runDBGroup(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		printDBUsage()
		return nil
	}
	command, rest := args[0], args[1:]
	switch command {
	// Core: db_core.go
	case "list", "ls":
		return dbListFlow(rest)
	case "engines", "catalog":
		return dbEnginesFlow(rest)
	case "info", "show", "inspect":
		return dbInfoFlow(rest)
	case "create", "new":
		return dbCreateFlow(rest)
	case "delete", "rm", "destroy":
		return dbDeleteFlow(rest)
	case "url":
		return dbURLFlow(rest)
	case "env":
		return dbEnvFlow(rest)
	case "credentials", "creds", "password":
		return runDBCredentialsGroup(rest)
	case "connect":
		return dbConnectFlow(rest)
	case "shell":
		return dbShellFlow(rest)
	case "bind", "bindings":
		return runDBBindGroup(rest)
	// Shared with `hubfly realtime`: managed_lifecycle.go / managed_observe.go
	case "start", "stop", "restart", "resize", "upgrade", "rename", "protect", "network":
		return runManagedLifecycleCommand(managedServiceDatabase, command, rest)
	case "logs", "metrics", "operations", "ops":
		return runManagedObserveCommand(managedServiceDatabase, command, rest)
	// Data: db_data.go
	case "query", "sql":
		return dbQueryFlow(rest)
	case "databases", "dbs":
		return runDBDatabasesGroup(rest)
	case "users", "user":
		return runDBUsersGroup(rest)
	case "tokens", "token":
		return runDBTokensGroup(rest)
	case "extensions", "extension", "ext":
		return runDBExtensionsGroup(rest)
	case "export":
		return dbExportFlow(rest)
	case "exports":
		return runDBExportsGroup(rest)
	case "import":
		return dbImportFlow(rest)
	case "cert", "certificate", "ca":
		return dbCertFlow(rest)
	case "sessions", "session":
		return runDBSessionsGroup(rest)
	case "insights":
		return runDBInsightsGroup(rest)
	case "config":
		return runDBConfigGroup(rest)
	default:
		return fmt.Errorf("unknown db command %q (run: hubfly db help)", command)
	}
}

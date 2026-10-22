# hubfly-cli

Open-source Hubfly CLI written in Go.

Repo: https://github.com/hubfly-space/hubfly-cli

## Quick install

### Linux / macOS

```bash
curl -fsSL https://raw.githubusercontent.com/hubfly-space/hubfly-cli/main/install.sh | bash
```

Verify the installation:

```bash
hubfly version
```

### Windows (PowerShell)

Open Windows PowerShell or PowerShell in Windows Terminal and run:

```powershell
irm https://raw.githubusercontent.com/hubfly-space/hubfly-cli/main/install.ps1 | iex
hubfly version
hubfly login
```

The installer automatically selects the Windows AMD64 (x64) or ARM64 release,
verifies its SHA-256 checksum, and installs `hubfly.exe` to
`%LOCALAPPDATA%\HubFly\bin`. It adds that directory to your user PATH and the
current PowerShell session. The default installation does not require an
administrator terminal. Reopen any other terminals to pick up the PATH change.

For a custom installation directory, download and run the installer:

```powershell
$installer = Join-Path $env:TEMP 'hubfly-install.ps1'
Invoke-WebRequest -UseBasicParsing `
  -Uri 'https://raw.githubusercontent.com/hubfly-space/hubfly-cli/main/install.ps1' `
  -OutFile $installer
& $installer -InstallDir "$env:USERPROFILE\Tools\HubFly"
```

To install a specific published release, also pass `-Version <release-tag>`
(replace `<release-tag>` with the desired tag from the
[releases page](https://github.com/hubfly-space/hubfly-cli/releases)).

If PowerShell blocks the downloaded script, run it with an execution-policy
override for that process, then open a new terminal:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File $installer
```

If `hubfly` is not recognized after installation, check the binary directly:

```powershell
& "$env:LOCALAPPDATA\HubFly\bin\hubfly.exe" version
```

For a custom installation, use that directory instead. Confirm it is in your
user PATH and reopen the terminal.

### Manual Windows installation

Download `hubfly_windows_amd64.zip` for x64 or `hubfly_windows_arm64.zip` for
ARM64, plus its matching `.sha256` file, from the
[releases page](https://github.com/hubfly-space/hubfly-cli/releases).
In the download directory, verify and extract the archive (change `$asset` for
ARM64):

```powershell
$asset = 'hubfly_windows_amd64.zip'
$expected = ((Get-Content -Raw "$asset.sha256").Trim() -split '\s+')[0]
$actual = (Get-FileHash -Algorithm SHA256 $asset).Hash
if ($expected -notmatch '^[a-fA-F0-9]{64}$' -or $actual -ne $expected) {
  throw 'Release archive checksum verification failed.'
}
$installDir = Join-Path $env:LOCALAPPDATA 'HubFly\bin'
Expand-Archive -LiteralPath $asset -DestinationPath $installDir -Force
& "$installDir\hubfly.exe" version
```

Add `$installDir` to your user PATH and open a new terminal to run `hubfly`
from any directory. Docker with Linux containers enabled is required for local
image builds; installation and remote management commands can run without it.

## Features

- Token-based auth (`login`, `logout`, `whoami`)
- Persistent Bubble Tea TUI for project/container/tunnel workflow (`projects`)
- Single and multiple tunnel connect flows
- SSH, one-shot command exec, and log streaming for containers
- Local-first stack orchestration for Compose-style apps (`stack`)
- Local tunnel status screens while sessions are active
- Local `hubfly deploy` flow with reusable `hubfly-builder`, `hubfly.build.json`, deploy diffs, and resumable image uploads
- Build config helpers: `hubfly build init|validate|edit|explain`
- Debug mode for API requests/responses (`--debug` or `HUBFLY_DEBUG=1`)
- Managed Realtime servers, apps, keys, bindings, webhooks, events, live inspector and E2E key generation (`realtime`, alias `rt`)
- Managed Databases (PostgreSQL, MySQL, SQLite/libSQL): create, connect, query, users, network, app bindings, export/import (`db`)
- Object Storage: storage projects, buckets, object upload/download, S3 access keys, published assets and video encoding (`storage`)
- Built-in version and self-update commands
- Tunnel service mode (`service`)
- GitHub Pages documentation page in `index.html`

## Commands

```bash
hubfly login [--token <TOKEN>]
hubfly logout
hubfly whoami
hubfly projects
hubfly deploy [advanced|--advanced] [--project <id|name|new>] [--region <region>] [--yes]
              [--config <path>] [--detach] [--dockerfile <path>] [--builder-version <tag>]
hubfly stack plan [--file <compose-file>]
hubfly stack up [--file <compose-file>] [--project <id|name|new>] [--region <region>] [--yes] [--remove-orphans] [--no-build]
hubfly stack status [--file <compose-file>]
hubfly stack logs [service...] [--follow|-f]
hubfly stack exec <service> -- <cmd> [args...]
hubfly stack ssh <service>
hubfly stack down [--file <compose-file>] [--volumes] [--yes]
hubfly build init [--config <path>] [--dockerfile <path>] [--force] [--print]
hubfly build validate [--config <path>] [--dockerfile <path>] [--builder-version <tag>] [--json]
hubfly build edit [--config <path>]
hubfly build explain [--config <path>] [--dockerfile <path>] [--builder-version <tag>] [--json]
hubfly tunnel <containerIdOrName> <localPort> <targetPort>
hubfly ssh <containerIdOrName> [-- <cmd> [args...]]
hubfly exec <containerIdOrName> -- <cmd> [args...]
hubfly logs <containerIdOrName> [--follow|-f]
hubfly orgs
hubfly version
hubfly update --check
hubfly update
hubfly service [--port <port>]
hubfly realtime <list|info|create|delete|start|stop|restart|resize|apps|keys|bind|env|publish|send|tail|webhooks|channels|history|insights|keygen|...> [options]
hubfly db <list|engines|info|create|delete|start|stop|restart|resize|url|env|connect|shell|query|users|databases|network|bind|export|import|...> [options]
hubfly storage <list|create|info|usage|buckets|ls|put|get|rm|url|keys|publish|share|encode|...> [options]
```

## Managed Realtime

`hubfly realtime` (alias `rt`) manages HubFly Managed Realtime: Pusher-compatible
WebSocket servers with HubFly extensions and built-in end-to-end encryption.
`<server>` is a realtime server id or name (add `--project <id|name>` when two
projects use the same name). `<app>` is an app id or name; `--app` may be omitted
when the server has one app. Every command that prints data accepts `--json`.

```bash
hubfly rt list [--project <id|name>]
hubfly rt info chat
hubfly rt create chat --size small --project new                  # quotes the monthly price and asks to confirm
hubfly rt create chat --size small --project my-managed --yes      # scripts: --yes accepts the quoted price
hubfly rt delete chat                                              # type the name to confirm, or --yes
```

`create` reads the sizes from the managed catalog, quotes the price from the
pricing endpoint (`$x.xx/month`, billed hourly), asks for confirmation unless
`--yes` is given, creates the server (in a new managed project with
`--project new`; HubFly chooses the region of managed projects) and waits for the operation with a progress spinner. Pass
`--no-wait` to return right away, `--cpu`/`--memory` for a custom size and
`--app <name>` to name the first app (default `production`).

Apps, keys and bindings:

```bash
hubfly rt apps chat
hubfly rt apps create chat staging --origins https://app.example.com --encryption required
hubfly rt apps update chat staging --client-events --history durable --history-ttl 1h
hubfly rt apps delete chat staging
hubfly rt keys list chat --app staging
hubfly rt keys create chat --app staging          # prints the new secret once
hubfly rt keys rotate chat --app staging --grace 24h
hubfly rt keys revoke chat <keyId> --app staging
hubfly rt keys reveal chat --app staging           # audited secret reveal
hubfly rt bind chat --to web --preset laravel --client-prefix VITE_
hubfly rt bind list chat
hubfly rt bind remove chat <bindingId>             # removes the variables it wrote; --keep-variables keeps them
hubfly rt env chat --app staging --format dotenv   # add --reveal to include the secret
```

App settings flags for `apps create`/`apps update`: `--origins <a,b>`,
`--client-events[=false]`, `--subscription-count[=false]`, `--require-auth[=false]`,
`--encryption off|optional|required`, `--history off|memory|durable`,
`--history-ttl <duration>`, `--history-max <n>`, `--offline-queue[=false]`,
`--max-connections <n>`, `--max-message-bytes <n>` and `--payload-preview[=false]`.

Secrets are printed only by `keys create`, `keys reveal`, and by `--reveal` on
`apps create`, `keys rotate` and `env`. `keys create` always prints the new
secret, the one time it is shown (as in the web dialog): in text mode with a
warning on stderr, with `--json` in the `secret` field. `keys rotate` shows how long the old key
keeps working (the grace period). `--debug` never prints secrets or tokens.

Events and debugging:

```bash
hubfly rt publish chat --channel orders --event order.updated --data '{"id":42}'
echo '{"id":42}' | hubfly rt publish chat orders order.updated
hubfly rt send chat --connection 1234567890.1234567890 --event notice --data hello
hubfly rt send chat --user u_42 --event notice --data-file ./notice.json
hubfly rt tail chat --family msg,conn --channel-prefix orders
hubfly rt tail chat --json | jq .
hubfly rt insights chat --window 6h
```

Event data is sent as text from `--data`, `--data-file <path|->` or stdin.
End-to-end connection ids (`<socket_id>~<key>`) carry a client key that must
never reach HubFly: the CLI removes the key before any request. Plaintext sends
to such ids are refused; encrypt end-to-end direct messages on your own server
with the HubFly server SDK (or pass `--strip-key` to send unencrypted plaintext
to the socket id).

`tail` opens the live inspector. It gets a short-lived inspector token from the
Dashboard, connects to `wss://<host>/hubfly/v1/inspect` with the token's origin
(the API origin when the token names none) as `Origin`, sends the token in the first frame, and reconnects with a new token
when the session ends. It prints one line per event (`--json` for JSON lines)
and notes sampling and dropped events on stderr. Press Ctrl-C to stop.

End-to-end master keys are generated locally and never sent anywhere:

```bash
hubfly rt keygen --kid k2      # k2:<base64 of 32 random bytes>
```

Put the key first in `HUBFLY_REALTIME_E2E_KEYS` (comma-separated `kid:base64`
entries; the first one encrypts) on your own servers.

Manage the server lifecycle and API allowlist:

```bash
hubfly rt stop chat
hubfly rt start chat
hubfly rt restart chat
hubfly rt resize chat --size small --yes
hubfly rt upgrade chat --yes
hubfly rt rename chat team-chat
hubfly rt protect team-chat on
hubfly rt network allow team-chat --my-ip --label office
hubfly rt network remove team-chat 203.0.113.10/32
```

Commands wait for the operation unless you pass `--no-wait`. Restarts use an
in-place handoff that preserves connections; resize applies live. Upgrades and
stops drain clients. The network allowlist guards the server HTTP API;
WebSocket clients are never IP-restricted. Realtime servers always have public
access. Use `--json` for structured lifecycle output and `--timeout` to limit
how long to wait.

Logs, metrics and operations (the same commands work for `hubfly db`):

```bash
hubfly rt logs chat                    # last 64 KB of the server's stdout and stderr
hubfly rt logs chat --tail 512k --follow   # polls every 2 s and prints new lines; Ctrl-C stops
hubfly rt metrics chat --window 6h     # CPU, memory, storage, network, connections, channels, messages
hubfly rt operations chat --limit 10   # create, restart, resize, upgrade, ... with status and errors
```

`--tail` takes bytes (`65536`, `64k`, `1m`; 4 KB to 2 MB). `--window` takes 5m to 7d.
`metrics` and `operations` accept `--json`.

Webhooks:

```bash
hubfly rt webhooks chat                            # ID, URL (host and path), EVENTS, ENABLED, KEY
hubfly rt webhooks create chat --url https://example.com/hooks/realtime --events channel_occupied,member_added
hubfly rt webhooks create chat --url https://example.com/hooks/realtime --events client_event --key <keyId> --disabled
hubfly rt webhooks update chat <webhookId> --events channel_occupied,channel_vacated --enable
hubfly rt webhooks test chat <webhookId>           # sends a signed hubfly.test event and shows the response
hubfly rt webhooks deliveries chat --status failed --limit 20
hubfly rt webhooks redeliver chat <deliveryId>
hubfly rt webhooks delete chat <webhookId>         # asks to confirm, or --yes
```

Webhook URLs must use `https://` on a public host (port 443 or 8443).
`--events` is checked against the event names in the managed catalog
(`channel_occupied`, `channel_vacated`, `member_added`, `member_removed`,
`client_event`, `cache_miss`, `subscription_count` and the `hubfly.*`
connection, presence and delivery events). `update` sends only the flags you
give. Tables show a webhook URL without its query string; `--json` prints the
URL as stored.

Deliveries are batched POSTs signed with the secret of the webhook's key
(`--key`, an app key id or public key; by default the app's newest active key):
`X-Pusher-Key` and `X-Pusher-Signature` (HMAC-SHA256 hex of the body, so the
`webhook()` validators of Pusher server libraries work), plus
`X-HubFly-Delivery-Id` (dedupe on it: delivery is at least once) and
`X-HubFly-Signature: t=<unix seconds>,v1=<HMAC-SHA256 hex of "<t>.<body>">`
(reject old `t` values to stop replays). Failed deliveries are retried with
backoff for up to 24 hours, then marked `dead`; the delivery log keeps 7 days
and any delivery can be queued again with `redeliver`. `test` is not written to
the delivery log; it exits non-zero when the receiver does not answer 2xx.

Live state, history and usage:

```bash
hubfly rt channels chat --prefix presence-          # occupied channels: NAME, TYPE, SUBSCRIBERS, USERS
hubfly rt channels inspect chat presence-room --members 50
hubfly rt history chat --channel orders --limit 20  # OFFSET, AT, EVENT, SIZE, DATA
hubfly rt history purge chat --channel orders       # without --channel: every channel of the app
hubfly rt connections chat --user u_42              # SOCKET ID, APP, USER, CHANNELS, PROTOCOL, LIBRARY, CONNECTED
hubfly rt disconnect chat --connection 1234567890.1234567890
hubfly rt disconnect chat --connection 1234567890.1234567890 --mode disconnect
hubfly rt disconnect chat --user u_42               # every connection and session of the user
hubfly rt usage chat --since 7d                     # or --since 2026-10-01; default: the last 30 days
```

`history` reads the messages the app retains (`--history memory|durable` on
the app). Messages of `private-encrypted-` channels and `hubfly.e2e` envelopes
stay opaque: HubFly cannot read them, so the table shows them as end-to-end
encrypted (decrypt them on your own server with the HubFly server SDK).
`connections` lists every app of the server; its `--user` and `--channel`
filters need an app when the server has several (`--app`).

`disconnect` uses `--mode terminate` by default: the connection closes with
code 4009, the client does not reconnect and its session is discarded.
`--mode disconnect` closes with code 4200: the client reconnects at once and
HubFly SDK clients resume their session. `--user` always terminates. Like
`send`, `disconnect` strips the client key from end-to-end connection ids
(`<socket_id>~<key>`) before any request, and `connections` never prints one.
`history purge`, `disconnect` and `webhooks delete` ask for confirmation; pass
`--yes` in scripts.

`usage` prints daily totals per app (UTC days, kept 400 days): peak
connections, connection minutes, messages in and out, bytes in and out and
webhook deliveries.

## Managed Databases

`hubfly db` (aliases `database` and `databases`) manages PostgreSQL, MySQL and
SQLite/libSQL instances. Use an instance id or name, with `--project` when names
are ambiguous. HubFly chooses the region. Run `hubfly db engines` for the
available engine versions, size presets and current prices.

```bash
hubfly db engines
hubfly db create app-db --engine postgresql --size nano --storage 10 --project new
hubfly db list
hubfly db info app-db --json
hubfly db url app-db                       # password hidden
hubfly db env app-db --reveal               # treat the output as a secret
hubfly db credentials app-db --reveal
hubfly db credentials rotate app-db --yes
hubfly db connect app-db -- --version       # local psql or mysql client
hubfly db shell app-db                     # remote console, no local client needed
hubfly db bind app-db --to web --new-user --access read_write
hubfly db bind list app-db
hubfly db bind remove app-db <bindingId> --yes
```

Creation shows a price quote and asks for confirmation; scripts must pass
`--yes`. Pick an existing managed project with `--project <id|name>`, or create
one with `--project new [--project-name <name>]`. MySQL defaults to the smallest
preset with at least 1 GiB of RAM. The default database is `app` (override with
`--database` for PostgreSQL/MySQL); the admin username is fixed to `admin`.
`--no-wait` returns when creation is accepted; `--timeout` limits the wait.

Credentials stay hidden unless you use `--reveal` or create/rotate them.
`connect` passes passwords through the client's environment, and any client
arguments follow `--`. libSQL uses its HTTPS-compatible URL and an auth token;
use a libSQL SDK or `hubfly db query` instead of a PostgreSQL/MySQL client.
Bindings write `DATABASE_URL`, or `LIBSQL_URL` and `LIBSQL_AUTH_TOKEN`, into a
Cell project's environment. Deploy the app to pick up the new values.
`--new-user` creates a dedicated PostgreSQL/MySQL user for the binding.

```bash
hubfly db stop app-db
hubfly db start app-db
hubfly db restart app-db
hubfly db resize app-db --size small --storage 20 --restart --yes
hubfly db upgrade app-db --yes
hubfly db rename app-db orders-db
hubfly db protect orders-db on
hubfly db network orders-db
hubfly db network allow orders-db --my-ip --label office
hubfly db network remove orders-db 203.0.113.10/32
hubfly db network public orders-db off --yes
hubfly db protect orders-db off
hubfly db delete orders-db --yes --wait
```

Storage can only grow. Resize shows the old and new prices; some changes need a
restart (`--restart` applies it now). Stopped instances pay for storage only.
Deletion requires the instance name or `--yes`, and protection must be off.
Public access is on by default; `--allow` or `--allow-my-ip` at creation sets an
IP allowlist. `--private`, or disabling public access later, removes the public
endpoint, including access by bound apps that use that hostname. TLS is always
required when connecting to a public endpoint.

Run SQL:

```bash
hubfly db query mydb "SELECT id, email FROM users LIMIT 5"
hubfly db query mydb --file ./report.sql --format csv > report.csv
cat migrate.sql | hubfly db query mydb --write
hubfly db query mydb "SELECT * FROM orders" --database shop --max-rows 2000 --format json
```

Queries run read-only unless you pass `--write`; a read-only run that tries to
change data fails and says so. The SQL comes from the argument, `--file <path|->`
or stdin (up to 100 KB; use `hubfly db import` for larger scripts). Each statement
prints a table (`NULL` for null values) or its command tag; results are capped at
`--max-rows` (default 500, at most 5000) and `--timeout` (default 30s, at most 60s).
`--format csv` prints a single result set. Notices and errors (code, position, hint)
go to stderr, and the command exits non-zero when a statement fails.

Logical databases, users and access (PostgreSQL and MySQL):

```bash
hubfly db databases mydb                           # default and system databases are marked
hubfly db databases create mydb shop --owner app_user
hubfly db databases delete mydb shop --force       # asks you to type the name; --force disconnects sessions first
hubfly db users mydb
hubfly db users create mydb reporter --preset read_only --databases shop
hubfly db users update mydb reporter --preset read_write --all-databases
hubfly db users rotate mydb reporter
hubfly db users delete mydb reporter --reassign-to admin
```

Presets: `read_only`, `read_write` (the default; can also run migrations) and
`admin` (all databases; never a superuser). `users create` and `users rotate`
print the password once (with `--json`, in `password`). The primary admin user
cannot be deleted; rotate its password with `hubfly db credentials rotate`.

Access tokens (SQLite/libSQL) and extensions (PostgreSQL):

```bash
hubfly db tokens create mylite ci --access read_only --expires 30   # prints the token once
hubfly db tokens mylite
hubfly db tokens revoke mylite <tokenId>
hubfly db extensions mydb --database shop
hubfly db extensions install mydb pgcrypto --database shop
hubfly db extensions remove mydb pgcrypto --cascade
```

New tokens have full access unless you pass `--access read_only`. libSQL cannot
revoke a single token: `tokens revoke` rotates the signing key, which invalidates
every token of the instance (apps connected with `hubfly db bind` get a new token).

Export and import:

```bash
hubfly db export mydb --database shop               # waits, then downloads to the current directory
hubfly db export mydb --format sql --output shop.sql
hubfly db export mydb --no-wait                     # prints the export id
hubfly db exports mydb
hubfly db exports download mydb <exportId> --output shop.dump
hubfly db import mydb ./shop.dump --database shop
```

PostgreSQL exports default to the `native` format (`pg_dump -Fc`); `--format sql`
gives plain SQL. MySQL and libSQL export gzip-compressed SQL. Exports are kept for
7 days, and the CLI never overwrites an existing file. `import` asks for
confirmation, uploads the file (up to 5 GB) and runs it against the existing data;
it stops at the first error. The format comes from the file name: `.dump` and
`.backup` are PostgreSQL native dumps, `.sql` and `.sql.gz` are SQL; pass
`--format` for other names. There are no automatic backups yet, so export before
risky changes.

Certificates, sessions, query insights and configuration:

```bash
hubfly db cert mydb --output mydb-ca.pem   # CA for sslmode=verify-full / --ssl-mode=VERIFY_IDENTITY
hubfly db sessions mydb
hubfly db sessions cancel mydb <sessionId>       # cancels the running query
hubfly db sessions terminate mydb <sessionId>    # closes the connection
hubfly db insights mydb --order-by mean_time --limit 10
hubfly db insights reset mydb
hubfly db config mydb                      # --all for every setting
hubfly db config set mydb work_mem=16MB max_connections=200 --restart later --note "more memory"
hubfly db config set mydb work_mem=        # back to the default
hubfly db config history mydb
hubfly db config revert mydb 3
hubfly db logs mydb --follow
hubfly db metrics mydb --window 24h
hubfly db operations mydb
```

`config set` validates the change, shows the diff and which settings need a
restart, and asks before applying. With `--restart later` (the default) those
settings wait for the next `hubfly db restart`; `--restart now` restarts the
database right away. Sessions opened by HubFly (console, monitoring) are marked
and cannot be cancelled. `logs` needs the database operate permission. Engine
features are checked first: tokens are libSQL-only, extensions PostgreSQL-only,
and libSQL has no users, sessions, insights or configuration.

## Object Storage

`hubfly storage` manages HubFly Object Storage: S3-compatible buckets, objects,
scoped access keys, published assets and video encoding. Storage projects are
regional: you pick a region that offers Object Storage when you create one.
`<project>` is a Storage project id or name; `--project` may be omitted when you
have exactly one Storage project, or set `HUBFLY_STORAGE_PROJECT`. Object paths
are `<bucket>/<key>`. Commands that print data accept `--json`.

```bash
hubfly storage list
hubfly storage create media --region <id|name>     # asks to confirm unless --yes, then waits until ready
hubfly storage info media                            # S3 endpoint, state, usage, buckets
hubfly storage usage media                           # used/quota, egress and encoding per day (30 days)
hubfly storage delete media                          # type the name to confirm, or --yes
```

Deleting a Storage project revokes its access keys at once; its buckets and
objects are removed after the region's recovery window.

Buckets and objects:

```bash
hubfly storage buckets create photos --versioning enabled
hubfly storage buckets update photos --versioning suspended --image-retention 30d
hubfly storage buckets delete photos                 # only empty buckets
hubfly storage ls                                    # buckets
hubfly storage ls photos/2026/                       # "directories" (PRE) and objects
hubfly storage ls photos --recursive --limit 0       # every key
hubfly storage put ./cat.jpg photos/                 # key photos/cat.jpg
hubfly storage put ./site photos/www --recursive     # site/a/b.html -> www/a/b.html
hubfly storage get photos/cat.jpg ./cat.jpg          # --force overwrites; "-" writes to stdout
hubfly storage rm photos/old/ --recursive            # lists, counts and asks to confirm (or --yes)
hubfly storage url photos/cat.jpg                    # temporary download link (15 minutes)
```

`put` streams each file to a short-lived signed upload URL. The content type
comes from `--content-type`, then the file extension, then the file's first
bytes. One upload is limited to 20 GB; for larger files use an S3 client with
multipart upload and a key from `keys create`. Versioning can be enabled or
suspended but not turned off again; image retention (`keep` or 1h to 365d)
controls how long generated image variants are cached.

S3 access keys:

```bash
hubfly storage keys
hubfly storage keys create backend                         # read/write on every bucket
hubfly storage keys create cdn --read-only --bucket photos --format dotenv
hubfly storage keys create backend --to web                # writes the variables into Cell project "web"
hubfly storage keys revoke <accessKey>
```

The secret is printed once, when the key is created (with a warning on
stderr). `--format dotenv|json` and `--to` use `HUBFLY_STORAGE_ENDPOINT`,
`HUBFLY_STORAGE_REGION`, `HUBFLY_STORAGE_ACCESS_KEY_ID`,
`HUBFLY_STORAGE_SECRET_ACCESS_KEY`, `HUBFLY_STORAGE_BUCKET` (bucket-scoped keys
only) and the AWS SDK names `AWS_ENDPOINT_URL_S3`, `AWS_REGION`,
`AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`. `--to` stores the secrets as
secret variables, prints only the variable names, reloads the project's
containers, and revokes the new key again if the variables cannot be written.
HubFly Storage accepts any SigV4 signing region (the CLI uses `us-east-1`); use
path-style addressing. `--debug` never prints secrets.

Assets and video:

```bash
hubfly storage publish photos/cat.jpg                # public asset; prints page and file URLs
hubfly storage publish photos/report.pdf --protected # prints the asset access key
hubfly storage share photos/clip.mp4 --expires 2h    # temporary link (5m to 24h)
hubfly storage assets --bucket photos
hubfly storage unpublish photos/cat.jpg              # deletes the asset AND the object
hubfly storage encode photos/clip.mp4 --max-height 1080 --wait
hubfly storage media                                 # encoding jobs
```

A published asset's page URL shows a viewer; add `/media` for the file itself.
Protected assets need `?key=<access key>` (or the `X-Asset-Key` header) or a
`share` link. `unpublish` removes the asset together with its source object,
renditions and image variants, so download the object first if you want to keep
it. `encode` produces HLS renditions up to the chosen height (default 720);
`--wait` follows the job and prints a signed player URL.

## Windows build and deploy

Use PowerShell or Windows Terminal. Auth uses Windows Credential Manager when
available; local configuration and the downloaded inspector live under
`%USERPROFILE%\.hubfly`.

`hubfly build validate`, `hubfly build explain`, and automatic build detection
use the Windows `hubfly-builder.exe` offline inspector. It detects runtimes and
generates Linux Dockerfiles. `hubfly deploy` and stack image builds require
Docker with Linux containers enabled (for example, Docker Desktop). The
regional Hubcell build server remains on Linux. Box SSH connections require
`ssh.exe` on PATH.

`hubfly update` replaces the CLI executable on Windows. Reopen the command to
use the new version. Container terminal sessions support live window resizing.

```powershell
$env:HUBFLY_API_URL = 'http://127.0.0.1:3000'
hubfly whoami
```

## API compatibility

By default the CLI talks to:

```bash
https://api.hubfly.space
```

Override it for local or staging environments:

```bash
HUBFLY_API_URL=http://127.0.0.1:3000 hubfly whoami
```

For CI, set `HUBFLY_TOKEN` instead of storing a local session:

```bash
HUBFLY_TOKEN=hf_... hubfly deploy --project my-api --region eu-1 --yes
```

API errors include the backend trace ID when the dashboard returns one, for example:

```text
Authentication required (status 401, trace err_...)
```

Use that trace ID to find the matching backend log.

## TUI Controls (`hubfly projects`)

- `↑/↓` or `j/k`: move
- `enter`: select/confirm
- `esc`: back
- `q`: quit from top-level
- Type text in filterable lists to search

Multi-tunnel selection:
- `space`: toggle tunnel
- `a`: toggle all
- `enter`: continue

## Versioning and updates

`hubfly version` shows:
- version tag
- commit SHA
- build date
- OS/arch

`hubfly update --check` checks latest release.

`hubfly update` downloads the latest release for your OS/arch and replaces the local binary (Linux/macOS).

## Deploying Apps

`hubfly deploy` works directly from your project directory. The CLI:

- creates or reuses `hubfly.build.json`
- checks for the latest `hubfly-builder` release before deploy unless `--builder-version` pins a specific version
- runs builder inspection for auto-detected stacks
- builds the Docker image locally on your machine
- uploads a compressed image archive to the regional builder with retryable chunk uploads
- reports local build/upload failures back to the authenticated CLI deploy session endpoint
- waits for the deploy by default, or returns early with `--detach`

Common examples:

```bash
hubfly deploy
hubfly deploy advanced
hubfly deploy --project new --region rw-kigali-1 --yes
hubfly deploy --project my-api --dockerfile ./deploy/Dockerfile --yes
hubfly deploy --config ./ops/hubfly.build.json --builder-version v1.7.1 --yes
```

Important flags:

- `--project`: existing project id/name, or `new`
- `--region`: required for non-interactive new-project deploys
- `--yes`: required in non-interactive/scripted deploys after reviewing the diff
- `--config`: custom `hubfly.build.json` path or project directory
- `--detach`: stop after upload and let Hubfly finish the deploy asynchronously
- `--dockerfile`: force Dockerfile mode for this run
- `--builder-version`: pin a specific `hubfly-builder` release

Before deploy, the CLI shows a diff for:

- image source
- resources
- ports
- volumes
- runtime environment variables
- healthcheck
- bound-container replacement behavior

Non-interactive deploys should provide enough information up front:

```bash
hubfly deploy --project my-api --region eu-1 --config ./hubfly.build.json --yes
```

Use `--detach` when a script only needs to upload the image and let Hubfly finish deployment in the background.

## Build Config Tooling

`hubfly deploy` does not require any manual setup, but the build helpers are useful when you want to inspect or adjust the config explicitly.

```bash
hubfly build init
hubfly build validate
hubfly build edit
hubfly build explain
```

What they do:

- `hubfly build init`: create or refresh `hubfly.build.json`
- `hubfly build validate`: resolve the effective build plan and verify Dockerfile/builder inputs
- `hubfly build edit`: open `hubfly.build.json` in `$EDITOR`
- `hubfly build explain`: show what `hubfly-builder` detected and why

Useful JSON output:

```bash
hubfly build validate --json
hubfly build explain --json
```

Those commands are meant for CI and for comparing the local build plan against what the dashboard will deploy.

## Debug and logs

Enable debug:

```bash
hubfly --debug projects
# or
HUBFLY_DEBUG=1 hubfly projects
```

When debug mode is enabled:
- Non-TUI commands print debug lines to stderr.
- During TUI mode (`projects`), debug lines are written to:
  - `~/.hubfly/logs/debug.log`

Debug output includes:
- HTTP method/URL
- masked Authorization token
- request/response payloads (secrets, tokens and secret environment values are masked)
- tunnel route selection details
- backend error/request trace IDs when the API returns them

Runtime logs:

```bash
hubfly logs <containerIdOrName>
hubfly logs <containerIdOrName> --follow
hubfly stack logs api --follow
```

Remote command execution:

```bash
hubfly ssh <containerIdOrName>
hubfly ssh <containerIdOrName> -- ls -la /app
hubfly exec <containerIdOrName> -- printenv
hubfly exec <containerIdOrName> -- sh -lc "ls -la /app"
hubfly stack exec api -- printenv
hubfly stack ssh api
```

In an interactive `hubfly ssh`, `stack ssh`, or database shell session, press `Ctrl+]` to detach and return to your local shell without sending `exit` to the remote shell.

## Stack Deploys

`hubfly stack` reads a local Compose-style file, builds any `build:` services on the user machine, pushes them through the authenticated regional registry upload flow, and creates or updates each service as a normal Hubfly container.

Supported today:

- `image` and `build` services
- named volumes
- `environment` and `env_file`
- `ports`, `depends_on`, `healthcheck`, and `restart`
- `x-hubfly` overrides for tier, resources, runtime, and volume size

Current behavior:

- build-backed services redeploy in place through the existing CLI deploy session path
- image-only services are recreated when their config changes
- bind mounts are ignored with warnings; use named volumes instead

Examples:

```bash
hubfly stack plan --file docker-compose.yml
hubfly stack up --project new --region eu-1 --yes
hubfly stack status
hubfly stack logs web --follow
hubfly stack down --volumes --yes
```

## SSH tunnel behavior

For tunnel connections, the CLI now uses a Hubfly-managed known_hosts file to avoid interactive host-key failures:

- `UserKnownHostsFile=~/.hubfly/known_hosts`
- `HostKeyAlias=hubfly-<tunnelId>`
- `StrictHostKeyChecking=accept-new`
- `BatchMode=yes`

This prevents global `~/.ssh/known_hosts` conflicts and avoids prompt-based failures in TUI sessions.

## Tunnel service mode

```bash
hubfly service
hubfly service --port 5600
```

Endpoints:
- `GET /health`
- `POST /start`
- `POST /stop`
- `GET /status`

This is useful when a desktop app, editor extension, or local automation needs to manage Hubfly tunnels without controlling the interactive TUI.

## GitHub Pages docs

This repository includes a standalone docs page:

```text
index.html
```

It uses Tailwind through the CDN and can be shipped directly with GitHub Pages from the repository root.

## Build from source

Linux/macOS:

```bash
git clone https://github.com/hubfly-space/hubfly-cli.git
cd hubfly-cli
go build -o hubfly .
./hubfly version
```

Windows (PowerShell, with Go installed):

```powershell
git clone https://github.com/hubfly-space/hubfly-cli.git
cd hubfly-cli
go build -o hubfly.exe .
.\hubfly.exe version
```

## Release automation

GitHub Actions workflow builds and publishes release assets on tag push (`v*`):
- `hubfly_linux_amd64.tar.gz`
- `hubfly_linux_arm64.tar.gz`
- `hubfly_darwin_amd64.tar.gz`
- `hubfly_darwin_arm64.tar.gz`
- `hubfly_windows_amd64.zip`
- `hubfly_windows_arm64.zip`

Each release asset also has a `.sha256` checksum file.

## Storage paths

- Token: `~/.hubfly/config.json`
- Keys: `~/.hubfly/keys`
- Known hosts (Hubfly-managed): `~/.hubfly/known_hosts`
- Debug logs: `~/.hubfly/logs/debug.log`

## Development

```bash
go build ./...
go test ./...
```

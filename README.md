# hubfly-cli

Open-source Hubfly CLI written in Go.

Repo: https://github.com/hubfly-space/hubfly-cli

## Quick install

Linux/macOS:

```bash
curl -fsSL https://raw.githubusercontent.com/hubfly-space/hubfly-cli/main/install.sh | bash
```

Then verify:

```bash
hubfly version
```

## Features

- Token-based auth (`login`, `logout`, `whoami`)
- Persistent Bubble Tea TUI for project/container/tunnel workflow (`projects`)
- Single and multiple tunnel connect flows
- SSH, exec, and log streaming for containers
- Local tunnel status screens while sessions are active
- Local `hubfly deploy` flow with reusable `hubfly-builder`, `hubfly.build.json`, deploy diffs, and resumable image uploads
- Build config helpers: `hubfly build init|validate|edit|explain`
- Debug mode for API requests/responses (`--debug` or `HUBFLY_DEBUG=1`)
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
hubfly build init [--config <path>] [--dockerfile <path>] [--force] [--print]
hubfly build validate [--config <path>] [--dockerfile <path>] [--builder-version <tag>] [--json]
hubfly build edit [--config <path>]
hubfly build explain [--config <path>] [--dockerfile <path>] [--builder-version <tag>] [--json]
hubfly tunnel <containerIdOrName> <localPort> <targetPort>
hubfly ssh <containerIdOrName>
hubfly exec <containerIdOrName> -- <cmd> [args...]
hubfly logs <containerIdOrName> [--follow|-f]
hubfly orgs
hubfly version
hubfly update --check
hubfly update
hubfly service [--port <port>]
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
- request/response payloads
- tunnel route selection details
- backend error/request trace IDs when the API returns them

Runtime logs:

```bash
hubfly logs <containerIdOrName>
hubfly logs <containerIdOrName> --follow
```

Remote command execution:

```bash
hubfly exec <containerIdOrName> -- printenv
hubfly exec <containerIdOrName> -- sh -lc "ls -la /app"
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

```bash
git clone https://github.com/hubfly-space/hubfly-cli.git
cd hubfly-cli
go build -o hubfly .
./hubfly version
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

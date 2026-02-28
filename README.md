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
- Local tunnel status screens while sessions are active
- Debug mode for API requests/responses (`--debug` or `HUBFLY_DEBUG=1`)
- Built-in version and self-update commands
- Tunnel service mode (`service`)

## Commands

```bash
hubfly login [--token <TOKEN>]
hubfly logout
hubfly whoami
hubfly projects
hubfly tunnel <containerIdOrName> <localPort> <targetPort>
hubfly version
hubfly update --check
hubfly update
hubfly service [--port <port>]
```

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

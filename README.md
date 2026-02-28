# hubfly-cli

Open-source Hubfly CLI written in Go.

This repository contains one Go codebase that includes:
- End-user Hubfly CLI
- Local tunnel-service HTTP server

Node/Bun code was removed; no JavaScript runtime is required.

## Features

- Token-based authentication (`login`, `logout`, `whoami`)
- Bubble Tea powered persistent TUI state machine for project/container/tunnel navigation (`projects`)
- Fast tunnel command (`tunnel <containerIdOrName> <localPort> <targetPort>`)
- Single-tunnel and multi-tunnel interactive connection from container view
- SSH key management in `~/.hubfly/keys`
- Local token storage in `~/.hubfly/config.json`
- Tunnel service API via `service` command (`/health`, `/start`, `/stop`, `/status`)
- Debug mode for API troubleshooting (`--debug` or `HUBFLY_DEBUG=1`)

## Requirements

- Go `1.24+`
- `ssh`
- `ssh-keygen`

## Install / Build

### Build from source

```bash
git clone https://github.com/hubfly-space/hubfly-cli.git
cd hubfly-cli
go build -o hubfly .
```

### Run without building

```bash
go run . help
```

## CLI Usage

```bash
./hubfly help
```

### Commands

- `./hubfly login`
- `./hubfly login --token <TOKEN>`
- `./hubfly logout`
- `./hubfly whoami`
- `./hubfly projects`
- `./hubfly tunnel <containerIdOrName> <localPort> <targetPort>`

## TUI Workflow (`projects`)

Run:

```bash
./hubfly projects
```

### Keyboard controls

Single-select screens (projects/containers/tunnels/actions):
- `↑/↓` or `j/k`: move
- `enter`: select
- `q` or `esc`: cancel/back
- Type text: filter list

Multi-select tunnel screen:
- `↑/↓` or `j/k`: move
- `space`: toggle item
- `a`: toggle all
- `enter`: confirm selection
- `q` or `esc`: cancel

### Flow

1. Pick a project from a searchable list.
2. Inspect container resources and status.
3. Choose action:
   - Create New Tunnel
   - Connect One Tunnel
   - Connect Multiple Tunnels
4. For multi-tunnel connect, choose tunnels in TUI, then pick target-port mode or custom local ports.
5. Launch all selected tunnels concurrently in the same session.
6. Stop all active multi-tunnel sessions with `s`, `Enter`, `Esc`, or `Ctrl+C`.

Examples:

```bash
./hubfly login --token hf_xxxxxxxxx
./hubfly projects
./hubfly tunnel my-api 8080 80
```

## Debug Mode

Use debug mode to print API request/response details for troubleshooting.

### Enable per command

```bash
./hubfly --debug whoami
./hubfly --debug projects
./hubfly --debug tunnel my-api 8080 80
```

### Enable via environment variable

```bash
HUBFLY_DEBUG=1 ./hubfly projects
```

### What debug mode logs

- HTTP method + URL
- Authorization header (token is masked)
- Request JSON body
- Response status code
- Response body
- Network/transport errors

## Tunnel Service Mode

Run the service:

```bash
./hubfly service
./hubfly service --port 5600
```

### Endpoints

- `GET /health`
- `POST /start`
- `POST /stop`
- `GET /status`

### Start tunnel request example

```bash
curl -X POST http://localhost:5600/start \
  -H "Content-Type: application/json" \
  -d '{
    "id": "my-web-tunnel",
    "ssh_host": "1.2.3.4",
    "ssh_port": 22,
    "ssh_user": "root",
    "private_key": "-----BEGIN RSA PRIVATE KEY-----\\n...\\n-----END RSA PRIVATE KEY-----",
    "local_port": 8080,
    "remote_host": "127.0.0.1",
    "remote_port": 80
  }'
```

### Stop tunnel request example

```bash
curl -X POST http://localhost:5600/stop \
  -H "Content-Type: application/json" \
  -d '{"id":"my-web-tunnel"}'
```

### Status example

```bash
curl http://localhost:5600/status
```

## Storage Paths

- Token: `~/.hubfly/config.json`
- SSH private/public keys: `~/.hubfly/keys`

## Development

```bash
go build ./...
go test ./...
```

## Security Notes

- Debug mode prints full request/response payloads. Use with care in shared logs.
- SSH private keys are generated locally and stored on your machine.

## License

Add your preferred open-source license in `LICENSE` (for example MIT or Apache-2.0).

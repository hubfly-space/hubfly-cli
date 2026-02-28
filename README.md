# hubfly-cli (Go)

Unified Go project containing:
- Hubfly CLI (`login`, `logout`, `whoami`, `projects`, `tunnel`)
- Tunnel service API (`service` command)

## Requirements

- Go 1.23+
- OpenSSH tools available in PATH: `ssh`, `ssh-keygen`

## Build

```bash
go build -o hubfly .
```

## CLI Usage

```bash
./hubfly login
./hubfly logout
./hubfly whoami
./hubfly projects
./hubfly tunnel <containerIdOrName> <localPort> <targetPort>
```

Generated keys are stored in `~/.hubfly/keys`.
Token is stored in `~/.hubfly/config.json`.

## Tunnel Service Usage

```bash
./hubfly service
# or custom port
./hubfly service --port 5600
```

Service endpoints:
- `GET /health`
- `POST /start`
- `POST /stop`
- `GET /status`

## Notes

- No Node/Bun runtime is required anymore.
- Existing tunnel-service behavior is preserved under the Go `service` subcommand.

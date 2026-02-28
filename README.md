# hubfly-cli

Open-source Hubfly CLI written in Go.

This repository now contains one Go codebase that includes:
- The end-user Hubfly CLI
- The local tunnel-service HTTP server

Node/Bun code was removed; no JavaScript runtime is required.

## Features

- Token-based authentication (`login`, `logout`, `whoami`)
- Interactive project/container flow (`projects`)
- Fast tunnel command (`tunnel <containerIdOrName> <localPort> <targetPort>`)
- SSH key management in `~/.hubfly/keys`
- Local token storage in `~/.hubfly/config.json`
- Tunnel service API via `service` command (`/health`, `/start`, `/stop`, `/status`)
- Debug mode for API troubleshooting (`--debug` or `HUBFLY_DEBUG=1`)

## Requirements

- Go `1.23+`
- `ssh`
- `ssh-keygen`

## Install / Build

### Build from source

```bash
git clone https://github.com/<your-org>/hubfly-cli.git
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

Examples:

```bash
./hubfly login --token hf_xxxxxxxxx
./hubfly whoami
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

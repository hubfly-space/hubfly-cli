# Hubfly Tunnel Service

The `tunnel-service` is a standalone Go application that manages multiple SSH tunnels. It provides a REST API to start, stop, and monitor tunnels dynamically.

## How it works

The service runs a HTTP server on port `5600`. When a tunnel is requested, it:
1.  Establishes an SSH connection to the specified `ssh_host` using the provided `private_key`.
2.  Opens a local TCP listener on `local_port`.
3.  For every connection received on `local_port`, it forwards the traffic through the SSH tunnel to the `remote_host:remote_port`.
4.  Manages multiple tunnels concurrently using Go routines.

## API Endpoints

### 1. Health Check
Checks if the service is up and running.

**Request:**
```bash
curl http://localhost:5600/health
```

**Response:**
```text
OK
```

---


### 2. Start Tunnel
Initiates a new SSH tunnel.

**Request:**
```bash
curl -X POST http://localhost:5600/start \
-H "Content-Type: application/json" \
-d '{
  "id": "my-web-tunnel",
  "ssh_host": "1.2.3.4",
  "ssh_port": 22,
  "ssh_user": "root",
  "private_key": "-----BEGIN RSA PRIVATE KEY-----\n...\n-----END RSA PRIVATE KEY-----",
  "local_port": 8080,
  "remote_host": "127.0.0.1",
  "remote_port": 80
}'
```

| Field | Description | Required |
| :--- | :--- | :--- |
| `id` | Unique identifier for the tunnel. | No (auto-generated if missing) |
| `ssh_host` | The SSH gateway server address. | Yes |
| `ssh_port` | The SSH gateway server port. | Yes |
| `ssh_user` | The SSH username. | No (defaults to `root`) |
| `private_key` | The private key string (PEM format). | Yes |
| `local_port` | The port to open on the local machine. | Yes |
| `remote_host` | The target host accessible from the SSH gateway. | Yes |
| `remote_port` | The target port on the remote host. | Yes |

**Response (Success):**
```json
{
  "status": "initiated",
  "id": "my-web-tunnel"
}
```

---


### 3. Stop Tunnel
Closes and removes an active tunnel.

**Request:**
```bash
curl -X POST http://localhost:5600/stop \
-H "Content-Type: application/json" \
-d '{
  "id": "my-web-tunnel"
}'
```

**Response (Success):**
```text
Tunnel stopped
```

---


### 4. Tunnel Status
Lists all currently active tunnels.

**Request:**
```bash
curl http://localhost:5600/status
```

**Response:**
```json
[
  {
    "id": "my-web-tunnel",
    "local_port": 8080,
    "target": "127.0.0.1:80",
    "status": "active",
    "ssh_server": "1.2.3.4:22"
  }
]
```

## Running the service

Ensure you have Go installed, then:

```bash
cd tunnel-service
go build -o tunnel-manager main.go
./tunnel-manager
```

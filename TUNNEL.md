# Ephemeral SSH Tunnels for Project Docker Networks

## Goal
Allow users to create temporary SSH-based tunnels into any **project network** (a Docker network created per project) so they can access containers inside that private network without permanently exposing container ports. The system will:

- Provide a **CLI** that authenticates the user, generates an SSH key pair locally, sends the **public key** to the backend.
- Backend will create an ephemeral **SSH tunnel container** attached to the chosen project network, add the public key to its `authorized_keys`, publish an ephemeral host port for SSH, and return connection details to the CLI.
- The CLI will open SSH port forwards (`-L`) that forward a local port to `container-in-network:target_port` using the ephemeral SSH container as a jump host.
- Support multiple tunnels per project and automatic cleanup (TTL / manual stop).

---

## High-level components

1. **CLI (client)**
   - Generates SSH key pair locally (private key stays locally; public key uploaded).
   - Authenticates with backend API and requests tunnel creation for a project.
   - Shows connection details and runs the SSH command (or prints it for the user).
   - Can list active tunnels and close them (requests backend to stop the container).

2. **Backend API**
   - Auth + API for tunnel lifecycle: create, list, inspect, stop.
   - Communicates with Docker Engine API to create ephemeral containers.
   - Stores metadata about tunnels (owner, project, containerId, hostPort, createdAt, ttl, publicKey, labels).

3. **Tunnel container image**
   - Minimal image running `sshd` (and `socat` if you want local proxying). Entry script writes provided `authorized_keys` and starts `sshd`.
   - Must accept the public environment var when the container is created.

4. **Orchestrator / Cleanup job**
   - Periodic job that stops and removes tunnel containers that have expired or been revoked.

5. **Security controls**
   - Limit capabilities, resource constraints, AppArmor/SELinux profiles.
   - Ensure the container cannot access the Docker socket.
   - Host network exposure limited to assigned ephemeral host port(s) only.
   - Logging and audit trail for who created which tunnel and when.

---

## Docker / Tunnel container details

### Recommended tunnel image
A tiny image based on `alpine` or `debian:slim` with `openssh-server` and a simple entrypoint that:

1. Writes the public key to `/home/tunnel/.ssh/authorized_keys` (create user `tunnel`).
2. Ensures permissions are correct (700 for `.ssh`, 600 for `authorized_keys`).
3. Starts `sshd` in the foreground.

Example Dockerfile (conceptual):

```Dockerfile
FROM alpine:3.18
RUN apk add --no-cache openssh-server bash
RUN adduser -D -h /home/tunnel tunnel && mkdir -p /home/tunnel/.ssh && chown -R tunnel:tunnel /home/tunnel
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
EXPOSE 22
CMD ["/entrypoint.sh"]
```

`entrypoint.sh` should accept the public key via environment variable `PUBLIC_KEY` or a mounted file at `/run/authorized_keys` and write it into the tunnel user authorized_keys.

**Important runtime settings:**
- Run as root to start `sshd`, but drop capabilities using Docker `cap_drop` and `read_only` filesystem where possible.
- `HostConfig.PortBindings` should map container port `22/tcp` to a host port `0` (ephemeral) so Docker assigns a random available port.
- Set `RestartPolicy` to `no`.
- Add a label such as `com.example.tunnel=true` and `com.example.owner=<user-id>` for management.


### How the SSH forwarding works
1. The ephemeral SSH container is attached to the **same Docker network** as the target container (so the SSH container can resolve the target container by name or IP inside that network).
2. The CLI performs SSH local port forwarding using the ephemeral container as the SSH server:

```bash
ssh -i path/to/private_key -p <HOST_PORT> tunnel@<HOST_IP> -L <LOCAL_PORT>:<TARGET_CONTAINER_NAME>:<TARGET_PORT> -N
```

- `HOST_IP` is the Docker host address reachable by the CLI (e.g., the project's server public IP).
- `<TARGET_CONTAINER_NAME>` resolves inside the same docker network since the SSH container is connected to that network.
- The tunnel gives the local machine access to `TARGET_CONTAINER_NAME:TARGET_PORT` by forwarding through the ephemeral container.

Multiple `-L` flags may be specified to create several tunnels in the same SSH session.

---

## Backend API design

### Tunnel lifecycle endpoints

1. **Create tunnel**
   - `POST /api/v1/tunnels`
   - Body:
     ```json
     {
       "projectId": "proj-123",
       "publicKey": "ssh-rsa AAAA... user@example.com",
       "targetContainer": "db-1",
       "targetPort": 3306,
       "ttlSeconds": 3600,
       "labels": {"purpose":"debug"}
     }
     ```
   - Response (201):
     ```json
     {
       "tunnelId": "t-abc",
       "sshHost": "203.0.113.10",
       "sshPort": 49221,
       "sshUser": "tunnel",
       "createdAt": "2025-12-04T...Z",
       "expiresAt": "2025-12-04T...Z",
       "instructions": "ssh -i ~/.ssh/tunnel_t-abc -p 49221 tunnel@203.0.113.10 -L 3306:db-1:3306 -N"
     }
     ```
   - Backend responsibilities:
     - Validate the `projectId` belongs to the caller.
     - Validate `targetContainer` exists in that project's network.
     - Create an ephemeral container attached to the project's Docker network and inject `publicKey`.
     - Return connection info and a `tunnelId`.

2. **List tunnels**
   - `GET /api/v1/projects/{projectId}/tunnels`
   - Returns active tunnels for the project.

3. **Get tunnel**
   - `GET /api/v1/tunnels/{tunnelId}` → details and host port.

4. **Stop tunnel**
   - `POST /api/v1/tunnels/{tunnelId}/stop` → stops and removes the container and marks it terminated.

5. **Revoke public key** (optional)
   - `POST /api/v1/tunnels/{tunnelId}/revoke` → remove `authorized_keys` and stop container.

### Docker Engine API interactions (overview)

- Communicate with Docker engine via the Docker HTTP API.
- Example flow on `POST /api/v1/tunnels`:
  1. Validate request and check project network name (e.g., `project_<id>_net`).
  2. Create container JSON with `HostConfig` and `NetworkConfig`:
     - Attach to `project` network by name using `NetworkingConfig` or later attach via `docker network connect`.
     - Set `PortBindings` for `22/tcp` to `[{ "HostPort": "0" }]` (let Docker assign an ephemeral host port).
     - Mount a tmpfs or temporary file with the `authorized_keys` file OR pass public key via environment variable and the image entrypoint writes it.
     - Example JSON (conceptual) for `POST /containers/create`:

```json
{
  "Image": "myorg/ssh-tunnel:latest",
  "Env": ["PUBLIC_KEY=ssh-rsa AAAA... user@host"],
  "ExposedPorts": {"22/tcp": {}},
  "HostConfig": {
    "PortBindings": {"22/tcp": [{"HostPort": "0"}]},
    "NetworkMode": "default",
    "RestartPolicy": {"Name": ""},
    "AutoRemove": false
  },
  "Labels": {
    "com.example.tunnel": "true",
    "com.example.owner": "user-123",
    "com.example.project": "proj-123"
  }
}
```

  3. Start the container (`POST /containers/{id}/start`).
  4. Inspect container: `GET /containers/{id}/json` to read `NetworkSettings.Ports["22/tcp"][0].HostPort` and confirm the assigned host port. Save that in the database along with `containerId`.
  5. Return the host's reachable IP and hostPort to the CLI.

**Note**: If your Docker host is behind NAT or not directly reachable, the host port must be published on a host that the CLI can reach (or you must have an intermediate gateway agent with a stable public address). Consider using a single gateway host per region.


---

## CLI design and flow (detailed)

### Responsibilities
- Authenticate user (obtain token)
- Generate SSH key pair if not already present for this user-session
- Upload public key to API when requesting tunnel
- Request tunnel creation and poll for readiness
- Start SSH command locally to establish port forwarding
- Provide commands to list and stop tunnels

### CLI commands (example)
```
# login
mycli login --username alice

# create tunnel interactively
mycli tunnel create --project proj-123

# create tunnel with args
mycli tunnel create --project proj-123 --container db-1 --port 3306 --local 3306 --ttl 3600

# list tunnels
mycli tunnel list --project proj-123

# stop tunnel
mycli tunnel stop t-abc
```

### Key generation (example in pseudocode)
1. Check `~/.mycli/keys/tunnel-<user>-<timestamp>` exists. If not, create RSA key pair (4096 bits or ed25519).
2. Keep private key locally with `0600` permissions. Send only the public key to API.

Example Node.js snippet to generate key pair (conceptual):
```js
const { generateKeyPairSync } = require('crypto');
const { publicKey, privateKey } = generateKeyPairSync('rsa', { modulusLength: 4096 });
// write privateKey to file and publicKey to file
```

### Tunnel creation sequence (CLI)
1. User runs `mycli tunnel create`.
2. CLI ensures user is authenticated and has a key pair.
3. CLI sends `POST /api/v1/tunnels` with `publicKey`, `projectId`, `targetContainer`, `targetPort`, and `ttlSeconds`.
4. Backend responds with `tunnelId` and `status: creating`.
5. CLI polls `GET /api/v1/tunnels/{tunnelId}` (or waits for a websocket notification) until `status: ready` and receives `sshPort` and `sshHost`.
6. CLI prints the `ssh` command and optionally runs it for the user:

```bash
ssh -i ~/.mycli/keys/tunnel-<id> -p <SSH_PORT> tunnel@<SSH_HOST> -L <LOCAL_PORT>:<TARGET_CONTAINER>:<TARGET_PORT> -N
```

If you want the CLI to automatically spawn the ssh process: run `ssh` as a subprocess and keep it running, or manage multiple tunnels in a single SSH session (use `-f -N` to background the SSH process).

---

## Security considerations

1. **Private key never leaves the client** — only the public key is uploaded.
2. **Limited lifetime** — require a TTL for tunnel containers so keys are removed and containers destroyed.
3. **Audit logs** — record who created which tunnel and when, and log container IDs and host ports.
4. **Least privilege** — run the sshd process as a non-root `tunnel` user inside the container. Drop Linux capabilities and use seccomp/AppArmor profiles.
5. **Network isolation** — do not bind the container to the host network except the published SSH host port.
6. **Rate limits & abuse prevention** — limit how many tunnels a user can create concurrently and total duration to avoid resource exhaustion.
7. **Public host reachability** — only publish the ephemeral host port on IPs/nics that are safe to expose. Consider binding to a private gateway that sits in a DMZ and enforces additional checks.
8. **Rotate & revoke** — provide a revoke API that removes the public key and stops the container immediately.

---

## Operational / edge cases & notes

- **DNS resolution**: The `TARGET_CONTAINER_NAME` must be resolvable from the tunnel container; attach the container to the same user-defined Docker network (not just the default bridge). If you attach after creation, use `POST /networks/{network}/connect` using Docker API.
- **Multiple tunnels per container**: You can open multiple `-L` forwards in one SSH session. The CLI can either create one ephemeral SSH container and open multiple forwards through it, or create multiple ephemeral containers (simpler for isolation but more resources).
- **HostPort allocation**: When you set `HostPort` to `0`, Docker will allocate a port. Use `docker inspect` to read the assigned port. If you prefer deterministic ranges, allocate from a managed pool in your backend and set `HostPort` explicitly.
- **Non-public hosts**: If the server is not publicly reachable, you can implement a centrally-routable gateway (e.g., a static gateway server per region) the CLI connects to and then tunnels further internally. Alternatively, use a reverse SSH or websocket-based reverse tunnel (more complex).
- **Connection stability**: If SSH containers are ephemeral, long-running user sessions will break when containers are restarted/removed; make sure the lifecycle is clear to users.

---

## Example Docker Engine API calls (curl / unix socket)

Create container:

```bash
curl -s -XPOST --unix-socket /var/run/docker.sock http://localhost/containers/create -H 'Content-Type: application/json' -d '
{
  "Image": "myorg/ssh-tunnel:latest",
  "Env": ["PUBLIC_KEY=ssh-rsa AAAA..."] ,
  "ExposedPorts": {"22/tcp": {}},
  "HostConfig": {
    "PortBindings": {"22/tcp": [{"HostPort":"0"}]},
    "RestartPolicy": {"Name": ""}
  },
  "Labels": {"com.example.tunnel":"true","com.example.owner":"user-123","com.example.project":"proj-123"}
}
'
```

Start container:
```bash
curl -s -XPOST --unix-socket /var/run/docker.sock "http://localhost/containers/<containerId>/start"
```

Inspect to find host port:
```bash
curl -s --unix-socket /var/run/docker.sock "http://localhost/containers/<containerId>/json" | jq '.NetworkSettings.Ports["22/tcp"][0].HostPort'
```

---

## Example minimal server-side pseudo implementation notes

- Use a Docker SDK (Go `docker/client`, Python `docker-py`, or Node `dockerode`) rather than raw HTTP where possible.
- Steps inside `createTunnel` handler:
  1. Authenticate and authorize user on project.
  2. Validate container name exists and network name is known.
  3. Prepare container create config; include the `PUBLIC_KEY` env var or temporary mount.
  4. Create container with `NetworkingConfig` or create then `docker network connect` to the project network.
  5. Start container.
  6. Inspect and persist `HostPort` and `containerId` in DB.
  7. Return connection details.

---

## Example SSH command for the user (one-liner)

```bash
ssh -i ~/.mycli/keys/tunnel-t-abc -p 49221 tunnel@203.0.113.10 -L 3306:db-1:3306 -N
```

Replace `203.0.113.10` and `49221` with the host IP and assigned host port returned by the API. Replace `db-1` and `3306` with the chosen container name and target port.

---

## Summary & next steps

1. Build or pick a hardened `ssh-tunnel` image that accepts public key input on startup.
2. Implement `POST /api/v1/tunnels` in your backend using a Docker SDK to create/start/inspect the tunnel container.
3. Implement CLI key generation + upload + polling logic and a small UX to spawn the SSH process.
4. Add cleanup/background job and security hardening.

If you'd like, I can:
- Provide a full `entrypoint.sh` and `Dockerfile` for the tunnel image.
- Draft a concrete Node.js or Go backend handler for `POST /api/v1/tunnels` showing SDK calls.
- Create a sample CLI implementation (Node.js or Go) that covers key generation, calling the API, and launching SSH.

Tell me which of those you'd like next and which language you prefer for the CLI/backend examples.

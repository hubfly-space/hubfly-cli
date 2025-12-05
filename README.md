# hubfly-cli

A CLI tool for interacting with the Hubfly API.

## Features

- **Authentication**: Securely log in with your API token.
- **Token Management**: Automatically stores your token for future sessions and handles re-authentication if the token expires.
- **User Info**: Quickly view the currently authenticated user profile.
- **Project Management**: View project details and resource usage.
- **Container Tunnels**: Securely access private containers via ephemeral SSH tunnels using local port forwarding.

## Installation

1.  Clone the repository.
2.  Install dependencies:

```bash
bun install
```

### Requirements
- **OpenSSH Client**: The CLI uses the system's `ssh` and `ssh-keygen` commands. Ensure these are available in your PATH (standard on Linux and macOS).

## Usage

To start the CLI and trigger the default authentication flow:

```bash
bun start
```

### Commands

- **Login**: Force a login or switch accounts.
  ```bash
  bun start login
  ```

- **Logout**: Remove the stored authentication token.
  ```bash
  bun start logout
  ```

- **Who Am I**: Check the currently logged-in user details.
  ```bash
  bun start whoami
  ```

- **Projects & Tunnels**: List all your projects, view details, and manage container tunnels.
  ```bash
  bun start projects
  ```
  *Follow the interactive prompts to:*
  1. Select a Project.
  2. Choose "Manage Container (Tunnels)".
  3. **Create Tunnel**: Generates a secure key pair and opens an ephemeral entry point to your container.
  4. **Connect**: Selects a local port (e.g., localhost:8080) to forward to the remote container port (e.g., 80).

## Configuration

The API host is configured in `src/constants.ts`. By default, it points to `http://localhost:3000`.

```typescript
export const API_HOST = "http://localhost:3000";
```

## Key Storage

Generated SSH keys for tunnels are stored securely in `~/.hubfly/keys`. 
- **Public keys** are uploaded to the ephemeral tunnel container.
- **Private keys** remain on your local machine with restricted permissions (`600`).

## Development

This project uses [Bun](https://bun.com) as the runtime and package manager.

- **Run locally**: `bun run index.ts` or `bun start`
- **Typecheck**: `bun run typecheck`

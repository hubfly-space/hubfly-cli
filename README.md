# hubfly-cli

A CLI tool for interacting with the Hubfly API.

## Features

- **Authentication**: Securely log in with your API token.
- **Token Management**: Automatically stores your token for future sessions and handles re-authentication if the token expires.
- **User Info**: Quickly view the currently authenticated user profile.

## Installation

1.  Clone the repository.
2.  Install dependencies:

```bash
bun install
```

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

- **Projects**: List all your projects and interactively select one to view its details, including associated containers.
  ```bash
  bun start projects
  ```

## Configuration

The API host is configured in `src/constants.ts`. By default, it points to `http://localhost:3000`.

```typescript
export const API_HOST = "http://localhost:3000";
```

## Development

This project uses [Bun](https://bun.com) as the runtime and package manager.

- **Run locally**: `bun run index.ts` or `bun start`
- **Typecheck**: `bun run typecheck`
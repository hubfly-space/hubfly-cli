#!/usr/bin/env bun
import { Command } from "commander";
import { password, select, input, number } from "@inquirer/prompts";
import { getToken, setToken, deleteToken } from "./src/store.ts";
import {
  fetchWhoAmI,
  fetchProjects,
  fetchProject,
  fetchTunnels,
  createTunnel,
  ApiError,
  type Container,
  type Tunnel,
} from "./src/api.ts";
import {
  generateKeyPairAndSave,
  runTunnelConnection,
  renameKeyFiles,
} from "./src/ssh.ts";
import { join } from "node:path";
import { homedir } from "node:os";

const program = new Command();

program.name("hubfly").description("Hubfly CLI tool").version("0.0.1");

async function loginFlow(isRetry = false): Promise<void> {
  if (isRetry) {
    console.log(
      "\nAuthentication failed. Please check your token and try again.",
    );
  } else {
    console.log("\nPlease authenticate to continue.");
  }

  const token = await password({
    message: isRetry ? "Enter your token again:" : "Enter your API token:",
  });

  // Check for empty token
  if (!token.trim()) {
    console.log("Token cannot be empty.");
    return loginFlow(true);
  }

  try {
    const user = await fetchWhoAmI(token);
    setToken(token);
    console.log(`\nSuccessfully logged in as ${user.name} (${user.email})`);
  } catch (error) {
    console.error(
      "\nError:",
      error instanceof Error ? error.message : String(error),
    );
    // Recursively ask for token until success or user exit (ctrl+c)
    await loginFlow(true);
  }
}

async function ensureAuth(silent = false): Promise<string | null> {
  const token = getToken();

  if (!token) {
    if (!silent) console.log("No valid session found.");
    await loginFlow();
    return getToken() || null;
  }

  try {
    const user = await fetchWhoAmI(token);
    if (!silent) {
      console.log(`Logged in as ${user.name} (${user.email})`);
    }
    return token;
  } catch (error) {
    if (
      error instanceof ApiError &&
      (error.status === 401 || error.status === 403)
    ) {
      if (!silent) console.log("Session expired or invalid.");
      deleteToken();
      await loginFlow(true);
      return getToken() || null;
    } else {
      if (!silent)
        console.error(
          "Failed to verify session:",
          error instanceof Error ? error.message : String(error),
        );
      // Do not delete token on network errors, but exit as we can't proceed
      process.exit(1);
    }
  }
}

program
  .command("login")
  .description("Log in with your API token")
  .action(async () => {
    // Force login flow even if token exists? Or just check?
    // Usually 'login' implies "I want to switch account or re-login"
    // But let's just check auth, if valid say so, else login.
    const token = getToken();
    if (token) {
      try {
        const user = await fetchWhoAmI(token);
        console.log(`Already logged in as ${user.name} (${user.email})`);
        // Optional: Ask if they want to re-login?
        // For now, just return.
        return;
      } catch {
        deleteToken();
      }
    }
    await loginFlow();
  });

program
  .command("logout")
  .description("Log out and remove stored token")
  .action(() => {
    deleteToken();
    console.log("Logged out successfully.");
  });

program
  .command("whoami")
  .description("Show current logged in user")
  .action(async () => {
    await ensureAuth();
  });

program
  .command("projects")
  .description("List all projects and select one to view details")
  .action(async () => {
    const token = await ensureAuth(true);
    if (!token) return;

    try {
      const projects = await fetchProjects(token);

      if (projects.length === 0) {
        console.log("No projects found.");
        return;
      }

      const selectedProjectId = await select({
        message: "Select a project to view details:",
        choices: projects.map((p) => ({
          name: `${p.name} (${p.region.name}) - ${p.status}`,
          value: p.id,
          description: `Role: ${p.role} | Created: ${p.createdAt}`,
        })),
      });

      await manageProject(token, selectedProjectId);
    } catch (error) {
      console.error(
        "Error fetching projects:",
        error instanceof Error ? error.message : String(error),
      );
    }
  });

async function manageProject(token: string, projectId: string) {
  while (true) {
    console.log(`\nFetching details for project ID: ${projectId}...`);
    const details = await fetchProject(token, projectId);

    if (details.containers.length === 0) {
      console.log("No containers found in this project.");
    } else {
      console.log(`\nContainers in project:\n`);
      const tableData = details.containers.map((c) => ({
        Name: c.name,
        Status: c.status,
        Type: c.source.type,
        "CPU (Cores)": c.resources.cpu,
        "RAM (MB)": c.resources.ram,
        Tier: c.tier,
      }));
      console.table(tableData);
    }

    const action = await select({
      message: "Project Actions:",
      choices: [
        {
          name: "Manage Container (Tunnels)",
          value: "manage_container",
          disabled: details.containers.length === 0,
        },
        { name: "Refresh", value: "refresh" },
        { name: "Back to Projects", value: "back" },
      ],
    });

    if (action === "back") return;
    if (action === "refresh") continue;

    if (action === "manage_container") {
      const containerId = await select({
        message: "Select a container to manage:",
        choices: details.containers.map((c) => ({ name: c.name, value: c.id })),
      });
      const container = details.containers.find((c) => c.id === containerId)!;
      await manageContainer(token, projectId, container);
    }
  }
}

async function manageContainer(
  token: string,
  projectId: string,
  container: Container,
) {
  while (true) {
    console.log(`\n--- Container: ${container.name} ---`);

    let myTunnels: Tunnel[] = [];
    try {
      const tunnels = await fetchTunnels(token, projectId);

      myTunnels = tunnels.filter((t) => t.targetContainerId === container.id);
    } catch (e) {
      console.log(
        "Could not fetch tunnels (Project might not support them yet or network error).",
      );
    }

    if (myTunnels.length > 0) {
      console.log("Active Tunnels:");
      console.table(
        myTunnels.map((t) => ({
          ID: t.tunnelId,
          "SSH Host": `${t.sshHost}:${t.sshPort}`,
          Target: `${t.targetNetwork.ipAddress}:${t.targetPort}`,
          Expires: t.expiresAt,
        })),
      );
    } else {
      console.log("No active tunnels found for this container.");
    }

    const action = await select({
      message: "Tunnel Actions:",
      choices: [
        { name: "Create New Tunnel", value: "create" },
        {
          name: "Connect to Tunnel",
          value: "connect",
          disabled: myTunnels.length === 0,
        },
        { name: "Back", value: "back" },
      ],
    });

    if (action === "back") return;

    if (action === "create") {
      const port = await number({
        message: "Enter internal container port (e.g. 80):",
        default: 80,
      });
      if (!port) continue;

      console.log("Generating SSH keys...");
      const tempId = `temp-${Date.now()}`;
      try {
        const { publicKey } = await generateKeyPairAndSave(tempId);

        console.log("Creating tunnel on server...");
        const tunnel = await createTunnel(token, {
          projectId,
          targetContainer: container.name,
          containerId: container.id,
          targetPort: port,
          publicKey,
        });

        console.log("Tunnel created successfully! Renaming keys...");
        await renameKeyFiles(tempId, `tunnel-${tunnel.tunnelId}`);
        console.log("Keys saved.");
      } catch (e) {
        console.error(
          "Failed to create tunnel:",
          e instanceof Error ? e.message : String(e),
        );
      }
    }

    if (action === "connect") {
      const tunnelId = await select({
        message: "Select tunnel to connect:",
        choices: myTunnels.map((t) => ({
          name: `${t.tunnelId} (Target: ${t.targetPort})`,
          value: t.tunnelId,
        })),
      });
      const tunnel = myTunnels.find((t) => t.tunnelId === tunnelId)!;

      // Find key
      const keyPath = join(
        homedir(),
        ".hubfly",
        "keys",
        `tunnel-${tunnel.tunnelId}`,
      );

      // Ask for local port
      const localPort = await number({
        message: "Enter local port to forward to (default: same as target):",
        default: tunnel.targetPort,
      });

      if (localPort) {
        await runTunnelConnection(tunnel, keyPath, localPort);
      }
    }
  }
}

// Default command handler
program.action(async () => {
  // If no command is specified, we verify auth as requested
  await ensureAuth();
});
program.parse(process.argv);

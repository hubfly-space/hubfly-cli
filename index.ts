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
        await runTunnelConnection(
          tunnel,
          keyPath,
          localPort,
          tunnel.targetPort,
        );
      }
    }
  }
}

program
  .command("tunnel")
  .description("Quickly tunnel to a container (creates new if needed)")
  .argument("<containerIdOrName>", "Container ID or Name")
  .argument("<localPort>", "Local port to forward to")
  .argument("<targetPort>", "Local port to forward to")
  .action(async (containerIdOrName, localPortStr, targetPortStr) => {
    const localPort = parseInt(localPortStr, 10);
    const targetPort = parseInt(targetPortStr, 10);

    if (isNaN(localPort) || isNaN(targetPort)) {
      console.error("Invalid port number.");
      process.exit(1);
    }

    const token = await ensureAuth(true);
    if (!token) return;

    try {
      // 1. Find the container
      console.log(`Searching for container '${containerIdOrName}'...`);
      const projects = await fetchProjects(token);

      let targetContainer: Container | null = null;
      let targetProjectId: string | null = null;

      // Parallel search could be faster but let's go sequential or blocked for simplicity/errors
      // We need project ID to create tunnel later
      for (const p of projects) {
        try {
          const details = await fetchProject(token, p.id);
          const found = details.containers.find(
            (c) => c.id === containerIdOrName || c.name === containerIdOrName,
          );
          if (found) {
            targetContainer = found;
            targetProjectId = p.id;
            break;
          }
        } catch (e) {
          // ignore error for specific project fetch
        }
      }

      if (!targetContainer || !targetProjectId) {
        console.error(
          `Container '${containerIdOrName}' not found in any project.`,
        );
        return;
      }

      console.log(
        `Found container: ${targetContainer.name} (${targetContainer.id})`,
      );

      // 2. Check for existing active tunnels & keys
      console.log("Checking for existing tunnels...");
      const tunnels = await fetchTunnels(token, targetProjectId);
      // Filter for this container and ensure keys exist
      const activeTunnels = tunnels.filter(
        (t) => t.targetContainerId === targetContainer!.id,
      );

      let tunnelToUse: Tunnel | null = null;
      let keyPathToUse: string | null = null;

      const { existsSync } = await import("node:fs");

      for (const t of activeTunnels) {
        // Check if key exists
        const keyPath = join(
          homedir(),
          ".hubfly",
          "keys",
          `tunnel-${t.tunnelId}`,
        );
        if (existsSync(keyPath)) {
          // Check expiry?
          if (new Date(t.expiresAt) > new Date()) {
            tunnelToUse = t;
            keyPathToUse = keyPath;
            break;
          }
        }
      }

      if (tunnelToUse && keyPathToUse) {
        console.log(`Found existing active tunnel: ${tunnelToUse.tunnelId}`);
        // If local port requested is different from tunnel target port, warn?
        // Actually runTunnelConnection takes localPort argument, so we can map anything to the tunnel's target port.
        // But the tunnel itself is fixed to a specific target port on the container.
        // Wait, the tunnel object has `targetPort`.
        // If the user wants to tunnel to port 80, but the existing tunnel is for port 3000, we can't reuse it?
        // Good catch. The existing tunnel is bound to a specific target port.

        // We should check if existing tunnel target port matches what user might want?
        // But the user only provided ONE port "localPort".
        // Assumption: User wants localPort -> targetPort (same).
        // Or we should assume the user wants to connect to the tunnel's defined target port?
        // If I say "tunnel my-web 8080", I expect local 8080 -> container 8080.
        // If existing tunnel is container 3000, I can't use it.

        if (tunnelToUse.targetPort !== localPort) {
          console.log(
            `Existing tunnel found but targets port ${tunnelToUse.targetPort}, not ${localPort}. Creating new one...`,
          );
          tunnelToUse = null; // force create
        }
      }

      // 3. Create if needed
      if (!tunnelToUse) {
        console.log(
          "No suitable existing tunnel found. Creating new tunnel...",
        );
        console.log("Generating SSH keys...");
        const tempId = `temp-${Date.now()}`;
        const { publicKey } = await generateKeyPairAndSave(tempId);

        console.log(`Creating tunnel for port ${localPort}...`);
        const newTunnel = await createTunnel(token, {
          projectId: targetProjectId,
          targetContainer: targetContainer.name,
          containerId: targetContainer.id,
          targetPort: localPort, // Assuming target port = local port requested
          publicKey,
        });

        console.log("Tunnel created successfully! Renaming keys...");
        const newKeyPath = await renameKeyFiles(
          tempId,
          `tunnel-${newTunnel.tunnelId}`,
        );
        tunnelToUse = newTunnel;
        keyPathToUse = newKeyPath;
      }

      // 4. Connect
      if (tunnelToUse && keyPathToUse) {
        await runTunnelConnection(
          tunnelToUse,
          keyPathToUse,
          localPort,
          targetPort,
        );
      }
    } catch (error) {
      console.error(
        "Error in tunnel command:",
        error instanceof Error ? error.message : String(error),
      );
      process.exit(1);
    }
  });

// Default command handler
program.action(async () => {
  // If no command is specified, we verify auth as requested
  await ensureAuth();
});
program.parse(process.argv);

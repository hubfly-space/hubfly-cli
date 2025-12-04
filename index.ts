#!/usr/bin/env bun
import { Command } from 'commander';
import { password, select } from '@inquirer/prompts';
import { getToken, setToken, deleteToken } from './src/store.ts';
import { fetchWhoAmI, fetchProjects, fetchProject, ApiError } from './src/api.ts';

const program = new Command();

program
  .name('hubfly')
  .description('Hubfly CLI tool')
  .version('0.0.1');

async function loginFlow(isRetry = false): Promise<void> {
  if (isRetry) {
    console.log('\nAuthentication failed. Please check your token and try again.');
  } else {
    console.log('\nPlease authenticate to continue.');
  }
  
  const token = await password({ 
    message: isRetry ? 'Enter your token again:' : 'Enter your API token:',
  });

  // Check for empty token
  if (!token.trim()) {
    console.log('Token cannot be empty.');
    return loginFlow(true);
  }
  
  try {
    const user = await fetchWhoAmI(token);
    setToken(token);
    console.log(`\nSuccessfully logged in as ${user.name} (${user.email})`);
  } catch (error) {
    console.error('\nError:', error instanceof Error ? error.message : String(error));
    // Recursively ask for token until success or user exit (ctrl+c)
    await loginFlow(true);
  }
}

async function ensureAuth(silent = false): Promise<string | null> {
  const token = getToken();
  
  if (!token) {
    if (!silent) console.log('No valid session found.');
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
    if (error instanceof ApiError && (error.status === 401 || error.status === 403)) {
      if (!silent) console.log('Session expired or invalid.');
      deleteToken();
      await loginFlow(true);
      return getToken() || null;
    } else {
      if (!silent) console.error('Failed to verify session:', error instanceof Error ? error.message : String(error));
      // Do not delete token on network errors, but exit as we can't proceed
      process.exit(1);
    }
  }
}

program
  .command('login')
  .description('Log in with your API token')
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
  .command('logout')
  .description('Log out and remove stored token')
  .action(() => {
    deleteToken();
    console.log('Logged out successfully.');
  });

program
  .command('whoami')
  .description('Show current logged in user')
  .action(async () => {
    await ensureAuth();
  });

program
  .command('projects')
  .description('List all projects and select one to view details')
  .action(async () => {
    const token = await ensureAuth(true);
    if (!token) return;

    try {
      const projects = await fetchProjects(token);

      if (projects.length === 0) {
        console.log('No projects found.');
        return;
      }

      const selectedProjectId = await select({
        message: 'Select a project to view details:',
        choices: projects.map((p) => ({
          name: `${p.name} (${p.region.name}) - ${p.status}`,
          value: p.id,
          description: `Role: ${p.role} | Created: ${p.createdAt}`,
        })),
      });

      console.log(`\nFetching details for project ID: ${selectedProjectId}...`);
      const details = await fetchProject(token, selectedProjectId);

      if (details.containers.length === 0) {
        console.log('No containers found in this project.');
      } else {
        console.log(`\nContainers in project:\n`);
        const tableData = details.containers.map(c => ({
          Name: c.name,
          Status: c.status,
          Type: c.source.type,
          'CPU (Cores)': c.resources.cpu,
          'RAM (MB)': c.resources.ram,
          Tier: c.tier
        }));
        console.table(tableData);
      }

    } catch (error) {
      console.error('Error fetching projects:', error instanceof Error ? error.message : String(error));
    }
  });

// Default command handler
program.action(async () => {
    // If no command is specified, we verify auth as requested
    await ensureAuth();
});
program.parse(process.argv);

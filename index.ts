#!/usr/bin/env bun
import { Command } from 'commander';
import { password } from '@inquirer/prompts';
import { getToken, setToken, deleteToken } from './src/store.ts';
import { fetchWhoAmI, ApiError } from './src/api.ts';

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

async function ensureAuth(silent = false) {
  const token = getToken();
  
  if (!token) {
    if (!silent) console.log('No valid session found.');
    await loginFlow();
    return;
  }

  try {
    const user = await fetchWhoAmI(token);
    if (!silent) {
       console.log(`Logged in as ${user.name} (${user.email})`);
    }
  } catch (error) {
    if (error instanceof ApiError && (error.status === 401 || error.status === 403)) {
      if (!silent) console.log('Session expired or invalid.');
      deleteToken();
      await loginFlow(true);
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

// Default command handler
program.action(async () => {
    // If no command is specified, we verify auth as requested
    await ensureAuth();
});

program.parse(process.argv);

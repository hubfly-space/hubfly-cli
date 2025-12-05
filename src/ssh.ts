import { generateKeyPair } from "node:crypto";
import { writeFile, mkdir, chmod } from "node:fs/promises";
import { homedir } from "node:os";
import { join } from "node:path";
import { spawn } from "node:child_process";
import { type Tunnel } from "./api.js";

const KEYS_DIR = join(homedir(), ".hubfly", "keys");

export async function ensureKeysDir() {
  await mkdir(KEYS_DIR, { recursive: true });
}

export async function generateSSHKeys(
  name: string,
): Promise<{ publicKey: string; privateKeyPath: string }> {
  await ensureKeysDir();

  return new Promise((resolve, reject) => {
    generateKeyPair(
      "rsa",
      {
        modulusLength: 4096,
        publicKeyEncoding: {
          type: "pkcs1",
          format: "pem",
        },
        privateKeyEncoding: {
          type: "pkcs1",
          format: "pem",
        },
      },
      async (err, publicKey, privateKey) => {
        if (err) return reject(err);

        // format public key for authorized_keys (openssh format)
        // The generated PEM is not in OpenSSH format (ssh-rsa ...).
        // We need to convert or generate directly in OpenSSH format.
        // Node's generateKeyPair can output openssh format for public key.
      },
    );
  });
}

// Re-implementing with promisified generateKeyPair and correct formats
export async function generateKeyPairAndSave(
  identifier: string,
): Promise<{ publicKey: string; privateKeyPath: string }> {
  await ensureKeysDir();
  const privateKeyPath = join(KEYS_DIR, `${identifier}`);
  const publicKeyPath = join(KEYS_DIR, `${identifier}.pub`);

  return new Promise((resolve, reject) => {
    generateKeyPair(
      "rsa",
      {
        modulusLength: 4096,
        publicKeyEncoding: {
          type: "spki",
          format: "pem",
        },
        privateKeyEncoding: {
          type: "pkcs1",
          format: "pem",
        },
      },
      async (err, publicKey, privateKey) => {
        if (err) return reject(err);

        // Convert SPKI PEM to OpenSSH format
        // Actually, simpler to use ssh-keygen or just use node-forge or ssh-pkcs12
        // But we can just use 'ssh-keygen' if available, or try to format it manually.
        // Converting PEM to ssh-rsa is non-trivial without a library like ssh2-streams or similar.

        // Let's try to use 'bun' specific or shell command 'ssh-keygen' if possible as it's more reliable for OpenSSH format.
        // But we want to avoid dependencies.

        // Alternative: Node 10+ supports 'openssh' type for public key? No, only for private?
        // Node 12+ crypto.createPublicKey(key).export({ type: 'openssh', format: 'pem' }) ??

        try {
          // Save private key
          await writeFile(privateKeyPath, privateKey, { mode: 0o600 });

          // Generate public key in OpenSSH format from private key
          // Using ssh-keygen -y -f private_key > public_key
          // This requires ssh-keygen installed, which is standard on Linux/Mac.
          const proc = spawn("ssh-keygen", ["-y", "-f", privateKeyPath]);

          let pubOutput = "";
          proc.stdout.on("data", (data) => (pubOutput += data));

          proc.on("close", async (code) => {
            if (code === 0) {
              const sshPublicKey = pubOutput.trim() + ` hubfly-generated`;
              await writeFile(publicKeyPath, sshPublicKey);
              resolve({ publicKey: sshPublicKey, privateKeyPath });
            } else {
              reject(
                new Error("Failed to generate public key with ssh-keygen"),
              );
            }
          });
        } catch (e) {
          reject(e);
        }
      },
    );
  });
}

export async function runTunnelConnection(
  tunnel: Tunnel,
  privateKeyPath: string,
  localPort: number,
  targetPort: number,
): Promise<void> {
  console.log(`\nEstablishing tunnel...`);

  console.log(
    `Local: localhost:${localPort} -> Remote: ${tunnel.targetNetwork.ipAddress}:${targetPort}`,
  );
  console.log(`Run command manually if this fails:`);
  console.log(
    `ssh -i ${privateKeyPath} -p ${tunnel.sshPort} ${tunnel.sshUser}@${tunnel.sshHost} -L ${localPort}:${tunnel.targetNetwork.ipAddress}:${targetPort} -N`,
  );

  const maxRetries = 3;
  const retryDelay = 2000;

  for (let attempt = 1; attempt <= maxRetries + 1; attempt++) {
    if (attempt > 1) {
      console.log(
        `\nConnection failed. Retrying in ${retryDelay / 1000}s... (Attempt ${attempt}/${maxRetries + 1})`,
      );
      await new Promise((resolve) => setTimeout(resolve, retryDelay));
    }

    const exitCode = await new Promise<number>((resolve) => {
      const ssh = spawn(
        "ssh",
        [
          "-i",
          privateKeyPath,
          "-p",
          tunnel.sshPort.toString(),
          "-o",
          "StrictHostKeyChecking=no",
          "-o",
          "UserKnownHostsFile=/dev/null",
          `${tunnel.sshUser}@${tunnel.sshHost}`,
          "-L",
          `${localPort}:${tunnel.targetNetwork.ipAddress}:${targetPort}`,
          "-N",
        ],
        { stdio: "inherit" },
      );

      ssh.on("close", (code) => {
        resolve(code ?? 1);
      });
    });

    if (exitCode === 0 || exitCode === 130) {
      console.log(`Tunnel connection closed (code ${exitCode})`);
      break;
    }

    if (attempt === maxRetries + 1) {
      console.log(`Tunnel connection closed (code ${exitCode})`);
    }
  }
}

export async function renameKeyFiles(
  oldIdentifier: string,
  newIdentifier: string,
) {
  const oldPriv = join(KEYS_DIR, oldIdentifier);
  const oldPub = join(KEYS_DIR, `${oldIdentifier}.pub`);
  const newPriv = join(KEYS_DIR, newIdentifier);
  const newPub = join(KEYS_DIR, `${newIdentifier}.pub`);

  const { rename } = await import("node:fs/promises");
  await rename(oldPriv, newPriv);
  await rename(oldPub, newPub);
  return newPriv; // return new path to private key
}

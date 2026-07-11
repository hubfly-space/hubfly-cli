package cli

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func generateKeyPairAndSave(identifier string) (jwk, string, error) {
	if err := os.MkdirAll(keysDir(), 0o700); err != nil {
		return jwk{}, "", err
	}
	privateKeyPath := filepath.Join(keysDir(), identifier)

	// Generate 4096-bit RSA key
	privateKey, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return jwk{}, "", err
	}

	// Encode Private Key to PEM (PKCS#1 RSA private key format)
	privBytes := x509.MarshalPKCS1PrivateKey(privateKey)
	privBlock := &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: privBytes,
	}
	privPEM := pem.EncodeToMemory(privBlock)

	// Save private key file
	if err := os.WriteFile(privateKeyPath, privPEM, 0600); err != nil {
		return jwk{}, "", err
	}

	// Create JWK for Public Key
	nBytes := privateKey.PublicKey.N.Bytes()
	nBase64 := base64.RawURLEncoding.EncodeToString(nBytes)

	eBytes := big.NewInt(int64(privateKey.PublicKey.E)).Bytes()
	eBase64 := base64.RawURLEncoding.EncodeToString(eBytes)

	pubJWK := jwk{
		Kty: "RSA",
		N:   nBase64,
		E:   eBase64,
	}

	return pubJWK, string(privPEM), nil
}

func removeKeyPair(identifier string) error {
	base := filepath.Join(keysDir(), identifier)
	_ = os.Remove(base)
	_ = os.Remove(base + ".pub")
	return nil
}

func renameKeyFiles(oldIdentifier, newIdentifier string) (string, error) {
	oldPriv := filepath.Join(keysDir(), oldIdentifier)
	oldPub := oldPriv + ".pub"
	newPriv := filepath.Join(keysDir(), newIdentifier)
	newPub := newPriv + ".pub"
	if err := os.Rename(oldPriv, newPriv); err != nil {
		return "", err
	}
	if err := os.Rename(oldPub, newPub); err != nil {
		return "", err
	}
	return newPriv, nil
}

func runTunnelConnection(t tunnel, privateKeyPath string, localPort, targetPort int) error {
	forwardHost := resolveTunnelForwardHost(t)
	fmt.Println("Establishing tunnel...")
	fmt.Printf("Local: localhost:%d -> Remote: %s:%d\n", localPort, forwardHost, targetPort)
	fmt.Println("Run manually if needed:")
	fmt.Printf("ssh -i %s -p %d %s@%s -L %d:%s:%d -N\n", privateKeyPath, t.SSHPort, strings.TrimSpace(t.SSHUser), strings.TrimSpace(t.SSHHost), localPort, forwardHost, targetPort)

	maxRetries := 3
	retryDelay := 2 * time.Second

	for attempt := 1; attempt <= maxRetries+1; attempt++ {
		if attempt > 1 {
			fmt.Printf("Connection failed. Retrying in %.0fs... (Attempt %d/%d)\n", retryDelay.Seconds(), attempt, maxRetries+1)
			time.Sleep(retryDelay)
		}

		cmd := tunnelCommand(t, privateKeyPath, localPort, targetPort)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Stdin = os.Stdin

		err := cmd.Run()
		exitCode := 1
		if err == nil {
			exitCode = 0
		} else if ee := (&exec.ExitError{}); errors.As(err, &ee) {
			exitCode = ee.ExitCode()
		}

		if exitCode == 0 || exitCode == 130 {
			fmt.Printf("Tunnel connection closed (code %d)\n", exitCode)
			return nil
		}
		if attempt == maxRetries+1 {
			return fmt.Errorf("tunnel connection closed with code %d", exitCode)
		}
	}
	return nil
}

func startTunnelConnectionBackground(t tunnel, privateKeyPath string, localPort, targetPort int) (*exec.Cmd, error) {
	cmd := tunnelCommand(t, privateKeyPath, localPort, targetPort)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}

func tunnelCommand(t tunnel, privateKeyPath string, localPort, targetPort int) *exec.Cmd {
	forwardHost := resolveTunnelForwardHost(t)
	knownHosts := filepath.Join(hubflyDir(), "known_hosts")
	_ = os.MkdirAll(hubflyDir(), 0o700)
	if f, err := os.OpenFile(knownHosts, os.O_CREATE, 0o600); err == nil {
		_ = f.Close()
	}
	hostAlias := fmt.Sprintf("hubfly-%s", strings.TrimSpace(t.TunnelID))

	debugf("tunnel route: tunnel=%s forward_host=%s local_port=%d target_port=%d host_alias=%s", t.TunnelID, forwardHost, localPort, targetPort, hostAlias)
	return exec.Command("ssh",
		"-i", privateKeyPath,
		"-o", "ExitOnForwardFailure=yes",
		"-o", "ConnectTimeout=10",
		"-o", "ServerAliveInterval=30",
		"-o", "ServerAliveCountMax=3",
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "UserKnownHostsFile="+knownHosts,
		"-o", "HostKeyAlias="+hostAlias,
		"-p", strconv.Itoa(t.SSHPort),
		fmt.Sprintf("%s@%s", strings.TrimSpace(t.SSHUser), strings.TrimSpace(t.SSHHost)),
		"-L", fmt.Sprintf("%d:%s:%d", localPort, forwardHost, targetPort),
		"-N",
	)
}

func resolveTunnelForwardHost(t tunnel) string {
	if strings.TrimSpace(t.DockerName) != "" {
		return strings.TrimSpace(t.DockerName)
	}
	if len(t.TargetNetwork.Aliases) > 0 {
		for _, alias := range t.TargetNetwork.Aliases {
			if strings.TrimSpace(alias) != "" {
				return strings.TrimSpace(alias)
			}
		}
	}
	if strings.TrimSpace(t.TargetNetwork.IPAddress) != "" {
		return strings.TrimSpace(t.TargetNetwork.IPAddress)
	}
	return "127.0.0.1"
}

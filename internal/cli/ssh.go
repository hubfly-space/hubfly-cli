package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func generateKeyPairAndSave(identifier string) (string, error) {
	if err := os.MkdirAll(keysDir(), 0o700); err != nil {
		return "", err
	}
	privateKeyPath := filepath.Join(keysDir(), identifier)
	publicKeyPath := privateKeyPath + ".pub"

	cmd := exec.Command("ssh-keygen", "-q", "-t", "rsa", "-b", "4096", "-N", "", "-f", privateKeyPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("ssh-keygen failed: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	pub, err := os.ReadFile(publicKeyPath)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(pub)), nil
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
	fmt.Println("Establishing tunnel...")
	fmt.Printf("Local: localhost:%d -> Remote: %s:%d\n", localPort, t.TargetNetwork.IPAddress, targetPort)
	fmt.Println("Run manually if needed:")
	fmt.Printf("ssh -i %s -p %d %s@%s -L %d:%s:%d -N\n", privateKeyPath, t.SSHPort, strings.TrimSpace(t.SSHUser), strings.TrimSpace(t.SSHHost), localPort, t.TargetNetwork.IPAddress, targetPort)

	maxRetries := 3
	retryDelay := 2 * time.Second

	for attempt := 1; attempt <= maxRetries+1; attempt++ {
		if attempt > 1 {
			fmt.Printf("Connection failed. Retrying in %.0fs... (Attempt %d/%d)\n", retryDelay.Seconds(), attempt, maxRetries+1)
			time.Sleep(retryDelay)
		}

		cmd := exec.Command("ssh",
			"-i", privateKeyPath,
			"-p", strconv.Itoa(t.SSHPort),
			fmt.Sprintf("%s@%s", strings.TrimSpace(t.SSHUser), strings.TrimSpace(t.SSHHost)),
			"-L", fmt.Sprintf("%d:%s:%d", localPort, t.TargetNetwork.IPAddress, targetPort),
			"-N",
		)
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

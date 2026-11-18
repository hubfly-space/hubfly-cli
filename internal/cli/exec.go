package cli

import (
	"fmt"
	"os"
	"time"
)

func execFlow(containerIDOrName string, command []string, timeout time.Duration) error {
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "Searching for container '%s'...\n", containerIDOrName)
	targetContainer, targetProjectID, err := findContainer(token, containerIDOrName)
	if err != nil {
		return err
	}

	return execContainerCommand(
		token,
		targetProjectID,
		targetContainer.ID,
		containerIDOrName,
		command,
		timeout,
	)
}

func execContainerCommand(
	token, projectID, containerID, displayName string,
	command []string,
	timeout time.Duration,
) error {
	result, err := execInContainer(token, projectID, containerID, command, timeout)
	if err != nil {
		return friendlyContainerRuntimeError(err, displayName, "commands")
	}

	if result.Stdout != "" {
		fmt.Print(result.Stdout)
	}
	if result.Stderr != "" {
		fmt.Fprint(os.Stderr, result.Stderr)
	}
	return remoteExitStatus(result.ExitCode)
}

// remoteExitError carries the exit code of a remote command or shell so Run
// returns it as the process exit code after deferred cleanup (terminal
// restore, connection close) has run, instead of os.Exit skipping it.
type remoteExitError struct{ code int }

func (e *remoteExitError) Error() string {
	return fmt.Sprintf("remote command exited with status %d", e.code)
}
func (e *remoteExitError) ExitCode() int { return e.code }

// remoteExitStatus is nil for a zero exit code.
func remoteExitStatus(code int) error {
	if code == 0 {
		return nil
	}
	if code < 0 || code > 255 {
		code = 1
	}
	return &remoteExitError{code: code}
}

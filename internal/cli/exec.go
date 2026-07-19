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

	fmt.Printf("Searching for container '%s'...\n", containerIDOrName)
	targetContainer, targetProjectID, err := findContainer(token, containerIDOrName)
	if err != nil {
		return err
	}

	result, err := execInContainer(token, targetProjectID, targetContainer.ID, command, timeout)
	if err != nil {
		return err
	}

	if result.Stdout != "" {
		fmt.Print(result.Stdout)
	}
	if result.Stderr != "" {
		fmt.Fprint(os.Stderr, result.Stderr)
	}
	os.Exit(result.ExitCode)
	return nil
}

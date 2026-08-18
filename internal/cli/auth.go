package cli

import (
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const cliAuthURL = "https://dashboard.hubfly.space/cli/auth"

func authRequiredError() error {
	return fmt.Errorf("authentication required; open %s to create a token, then run hubfly login --token <token>", cliAuthURL)
}

func login(providedToken string) error {
	token := strings.TrimSpace(providedToken)
	if token != "" {
		u, err := fetchWhoAmI(token)
		if err != nil {
			return err
		}
		if err := setToken(token); err != nil {
			return err
		}
		fmt.Printf("Successfully logged in as %s (%s)\n", u.Name, u.Email)
		return nil
	}

	if !isInteractiveShell() {
		return authRequiredError()
	}

	started, err := startDeviceLogin()
	if err != nil {
		return err
	}
	fmt.Printf("Open %s\n", started.VerificationURL)
	fmt.Printf("One-time code: %s\n", started.UserCode)
	_ = openBrowser(started.VerificationURL)
	deadline := time.Now().Add(time.Duration(started.ExpiresIn) * time.Second)
	interval := time.Duration(started.Interval) * time.Second
	if interval < time.Second {
		interval = 3 * time.Second
	}
	for time.Now().Before(deadline) {
		time.Sleep(interval)
		result, pollErr := pollDeviceLogin(started.DeviceCode)
		if pollErr != nil {
			return pollErr
		}
		if result.Status != "approved" || result.Token == "" {
			continue
		}
		u, authErr := fetchWhoAmI(result.Token)
		if authErr != nil {
			return authErr
		}
		if err := setToken(result.Token); err != nil {
			return err
		}
		fmt.Printf("Successfully logged in as %s (%s)\n", u.Name, u.Email)
		return nil
	}
	return fmt.Errorf("device login timed out")
}

func openBrowser(target string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", target)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		command = exec.Command("xdg-open", target)
	}
	return command.Start()
}

func ensureAuth(silent bool) (string, error) {
	token, err := getToken()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(token) == "" {
		if !silent {
			fmt.Println("No valid session found.")
		}
		if !isInteractiveShell() {
			return "", authRequiredError()
		}
		if err := login(""); err != nil {
			return "", err
		}
		return getToken()
	}

	u, err := fetchWhoAmI(token)
	if err == nil {
		if !silent {
			fmt.Printf("Logged in as %s (%s)\n", u.Name, u.Email)
		}
		return token, nil
	}

	var apiErr *apiError
	if errors.As(err, &apiErr) && (apiErr.Status == 401 || apiErr.Status == 403) {
		if !silent {
			fmt.Println("Session expired or invalid.")
		}
		_ = deleteToken()
		if !isInteractiveShell() {
			return "", authRequiredError()
		}
		if loginErr := login(""); loginErr != nil {
			return "", loginErr
		}
		return getToken()
	}
	return "", err
}

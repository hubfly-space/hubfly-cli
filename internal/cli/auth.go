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
	ctx := RootContext()
	transientFailures := 0
	for time.Now().Before(deadline) {
		if err := SleepContext(ctx, interval); err != nil {
			return errors.New("login cancelled")
		}
		result, pollErr := pollDeviceLogin(started.DeviceCode)
		if pollErr != nil {
			var apiErr *apiError
			if errors.As(pollErr, &apiErr) && apiErr.Status == 410 {
				if apiErr.Code == "DEVICE_CODE_USED" {
					return errors.New("this login code was already used (the response may have been lost); run `hubfly login` again")
				}
				return errors.New("the login code expired; run `hubfly login` again")
			}
			// Keep polling through short network blips; the shared HTTP
			// layer already retried connect failures.
			if ctx.Err() == nil && (IsRetryable(pollErr) || isTransientNetworkError(pollErr)) && transientFailures < 5 {
				transientFailures++
				debugf("device login poll failed (%d/5): %v", transientFailures, pollErr)
				continue
			}
			return pollErr
		}
		transientFailures = 0
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
	return errors.New("device login timed out; run `hubfly login` again")
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
		if tokenFromEnv() {
			// Deleting the stored session would not help: the environment
			// variable wins on every run.
			return "", fmt.Errorf("the token in HUBFLY_TOKEN was rejected (%w); create a new one at %s", err, cliAuthURL)
		}
		if !silent {
			fmt.Println("Session expired or invalid.")
		}
		_ = deleteToken()
		if !isInteractiveShell() {
			return "", fmt.Errorf("your session has expired or was revoked; run `hubfly login` (or set HUBFLY_TOKEN; create a token at %s): %w", cliAuthURL, err)
		}
		if loginErr := login(""); loginErr != nil {
			return "", loginErr
		}
		return getToken()
	}
	return "", err
}

package cli

import (
	"errors"
	"fmt"
	"strings"
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

	emptyAttempts := 0
	for {
		fmt.Printf("Please authenticate to continue. Go to %s to get the token\n", cliAuthURL)
		input, err := prompt("Enter your API token: ")
		if err != nil {
			return err
		}
		input = strings.TrimSpace(input)
		if input == "" {
			emptyAttempts++
			if emptyAttempts >= 3 {
				return authRequiredError()
			}
			fmt.Println("Token cannot be empty.")
			continue
		}
		emptyAttempts = 0

		u, authErr := fetchWhoAmI(input)
		if authErr != nil {
			fmt.Printf("Authentication failed: %v\n", authErr)
			continue
		}

		if err := setToken(input); err != nil {
			return err
		}
		fmt.Printf("Successfully logged in as %s (%s)\n", u.Name, u.Email)
		return nil
	}
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

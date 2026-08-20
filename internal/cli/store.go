package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zalando/go-keyring"
)

const credentialService = "hubfly-cli"
const credentialAccount = "default"

func hubflyDir() string {
	return filepath.Join(userHomeDir(), ".hubfly")
}

func keysDir() string {
	return filepath.Join(hubflyDir(), "keys")
}

func tunnelsDir() string {
	return filepath.Join(hubflyDir(), "tunnels")
}

func configPath() string {
	return filepath.Join(hubflyDir(), "config.json")
}

func userHomeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return h
}

func getToken() (string, error) {
	if token := strings.TrimSpace(os.Getenv("HUBFLY_TOKEN")); token != "" {
		return token, nil
	}
	if token, err := keyring.Get(credentialService, credentialAccount); err == nil {
		return token, nil
	}
	content, err := os.ReadFile(configPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	var cfg storeConfig
	if err := json.Unmarshal(content, &cfg); err != nil {
		return "", err
	}
	return cfg.Token, nil
}

func setToken(token string) error {
	if err := keyring.Set(credentialService, credentialAccount, token); err == nil {
		_ = os.Remove(configPath())
		return nil
	}
	fmt.Fprintln(os.Stderr, "Warning: OS credential store is unavailable; saving the token in a mode-0600 fallback file.")
	if err := os.MkdirAll(hubflyDir(), 0o700); err != nil {
		return err
	}
	cfg := storeConfig{Token: token}
	payload, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(configPath(), payload, 0o600)
}

func deleteToken() error {
	keyringErr := keyring.Delete(credentialService, credentialAccount)
	if keyringErr != nil && !errors.Is(keyringErr, keyring.ErrNotFound) {
		fmt.Fprintf(os.Stderr, "Warning: could not clear OS credential store: %v\n", keyringErr)
	}
	err := os.Remove(configPath())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

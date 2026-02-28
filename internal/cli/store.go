package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

func hubflyDir() string {
	return filepath.Join(userHomeDir(), ".hubfly")
}

func keysDir() string {
	return filepath.Join(hubflyDir(), "keys")
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
	err := os.Remove(configPath())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

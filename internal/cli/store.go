package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/zalando/go-keyring"
)

// credentialMu serializes token reads/writes inside one process; the atomic
// rename in WriteFileAtomic keeps concurrent CLI processes from ever seeing
// a half-written config.
var credentialMu sync.Mutex

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
	credentialMu.Lock()
	defer credentialMu.Unlock()
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
		return "", fmt.Errorf("%s is unreadable (%v); run `hubfly login` to replace it", configPath(), err)
	}
	return cfg.Token, nil
}

// tokenFromEnv reports whether the active token comes from HUBFLY_TOKEN.
func tokenFromEnv() bool {
	return strings.TrimSpace(os.Getenv("HUBFLY_TOKEN")) != ""
}

func setToken(token string) error {
	credentialMu.Lock()
	defer credentialMu.Unlock()
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
	return WriteFileAtomic(configPath(), payload, 0o600)
}

func deleteToken() error {
	credentialMu.Lock()
	defer credentialMu.Unlock()
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

// WriteFileAtomic writes data to path so readers only ever see the old or the
// new content: it writes a temp file in the same directory, fsyncs it, sets
// perm, then renames it over path (MoveFileEx replace on Windows). The parent
// directory is created with 0700 when missing.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(perm); err != nil && runtime.GOOS != "windows" {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	committed = true
	if runtime.GOOS != "windows" {
		if d, err := os.Open(dir); err == nil {
			_ = d.Sync()
			_ = d.Close()
		}
	}
	return nil
}

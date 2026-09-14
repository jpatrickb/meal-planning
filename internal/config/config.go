// Package config resolves where mealcli's data lives and reads its secrets.
//
// Resolution mirrors the sibling jobtracker tool's convention on this machine:
// env var override, then a config file, then a hardcoded default. Nothing here
// touches disk at import time — `meal --help` and `meal init` must work with no
// data directory yet in existence.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

const (
	dataRootEnvVar = "MEALCLI_DATA_ROOT"
	usdaKeyEnvVar  = "MEALCLI_USDA_API_KEY"
	configDirName  = "mealcli"
	configFileName = "config.toml"
	secretsFile    = "secrets.env"

	usdaKeyPlaceholder = "<YOUR_USDA_FDC_API_KEY>"
)

// Config is the resolved runtime configuration for a mealcli invocation.
type Config struct {
	DataRoot string
}

type fileConfig struct {
	DataRoot string `toml:"data_root"`
}

// ConfigDir returns ~/.config/mealcli (or $XDG_CONFIG_HOME/mealcli if set),
// without requiring it to exist.
func ConfigDir() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, configDirName), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	return filepath.Join(home, ".config", configDirName), nil
}

// Resolve determines the data root: env var > config file > default ~/MealPlanning.
// It does not create the directory or any files.
func Resolve() (*Config, error) {
	if root := os.Getenv(dataRootEnvVar); root != "" {
		return &Config{DataRoot: root}, nil
	}

	cfgDir, err := ConfigDir()
	if err != nil {
		return nil, err
	}
	cfgPath := filepath.Join(cfgDir, configFileName)
	if data, err := os.ReadFile(cfgPath); err == nil {
		var fc fileConfig
		if err := toml.Unmarshal(data, &fc); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", cfgPath, err)
		}
		if fc.DataRoot != "" {
			return &Config{DataRoot: fc.DataRoot}, nil
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("reading %s: %w", cfgPath, err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolving home directory: %w", err)
	}
	return &Config{DataRoot: filepath.Join(home, "MealPlanning")}, nil
}

// WriteDataRoot persists the data root to the config file, used by `meal init`.
// It does not overwrite an existing config file's data_root silently — callers
// should check ConfigFileExists first if that matters.
func WriteDataRoot(dataRoot string) error {
	cfgDir, err := ConfigDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}
	cfgPath := filepath.Join(cfgDir, configFileName)
	f, err := os.Create(cfgPath)
	if err != nil {
		return fmt.Errorf("writing %s: %w", cfgPath, err)
	}
	defer f.Close()
	return toml.NewEncoder(f).Encode(fileConfig{DataRoot: dataRoot})
}

// ConfigFileExists reports whether a config.toml has already been written.
func ConfigFileExists() (bool, error) {
	cfgDir, err := ConfigDir()
	if err != nil {
		return false, err
	}
	_, err = os.Stat(filepath.Join(cfgDir, configFileName))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// USDAAPIKey resolves the USDA FoodData Central API key: env var first, then
// a local secrets file that is never meant to be git-tracked. It returns ""
// (never an error) when no real key is configured yet - including when the
// secrets file still holds its unedited placeholder value - so callers can't
// mistake the placeholder for a working key.
func USDAAPIKey() (string, error) {
	if key := os.Getenv(usdaKeyEnvVar); key != "" && key != usdaKeyPlaceholder {
		return key, nil
	}
	cfgDir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(cfgDir, secretsFile))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("reading secrets file: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if strings.TrimSpace(key) == usdaKeyEnvVar {
			if v := strings.TrimSpace(val); v != usdaKeyPlaceholder {
				return v, nil
			}
			return "", nil
		}
	}
	return "", nil
}

// EnsureSecretsFile creates a placeholder secrets.env (chmod 600) if one
// doesn't already exist, so `meal doctor` has something concrete to point at.
// It never overwrites an existing file.
func EnsureSecretsFile() (path string, created bool, err error) {
	cfgDir, err := ConfigDir()
	if err != nil {
		return "", false, err
	}
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		return "", false, fmt.Errorf("creating config dir: %w", err)
	}
	secretsPath := filepath.Join(cfgDir, secretsFile)
	if _, err := os.Stat(secretsPath); err == nil {
		return secretsPath, false, nil
	} else if !os.IsNotExist(err) {
		return "", false, err
	}

	contents := "# mealcli secrets - do not commit this file to git.\n" +
		usdaKeyEnvVar + "=" + usdaKeyPlaceholder + "\n"
	if err := os.WriteFile(secretsPath, []byte(contents), 0o600); err != nil {
		return "", false, fmt.Errorf("writing secrets file: %w", err)
	}
	return secretsPath, true, nil
}

// Package config loads the on-disk Pyrycode configuration file.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// Config is the on-disk schema for ~/.pyry/config.json. Fields are added
// additively over time; consumers reading an older file see missing-field
// defaults via DefaultConfig + overlay-decode in Load.
type Config struct {
	RelayURL string `json:"relay_url"`
}

// DefaultConfig returns the built-in defaults. Used directly when no config
// file exists, and as the overlay base when a partial file is present so
// absent fields keep their default values.
func DefaultConfig() Config {
	return Config{
		RelayURL: "wss://relay.pyrycode.dev",
	}
}

// Load reads the config file at path and returns a Config with defaults
// filled in for absent fields. A missing file returns DefaultConfig() with
// no error. A malformed file returns a wrapped error (no silent fallback —
// operator must fix or remove the file). On any error the returned Config
// is the zero value; callers must check err.
func Load(path string) (Config, error) {
	cfg := DefaultConfig()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return cfg, nil
		}
		return Config{}, fmt.Errorf("config: read %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("config: parse %s: %w", path, err)
	}
	return cfg, nil
}

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

	// DebugCapture, when true, records the daemon's interactive session to a
	// .cast file (see #802). Default OFF: the JSON zero value for an absent
	// field is false, so a config that omits "debug_capture" behaves as OFF
	// with no DefaultConfig entry. SECURITY: a recording holds every PTY byte
	// — prompt, output, tool output — so this is strictly opt-in.
	DebugCapture bool `json:"debug_capture"`

	// InteractiveRunner selects which interactive runner the daemon builds:
	// "" or "pty" → the terminal-driven PTY supervisor (byte-identical to
	// today's daemon startup, the #1077 rollback guarantee); "stream-json" →
	// the streamsup-backed runner (#1081). Absent/empty is the default via the
	// JSON zero value — NO DefaultConfig entry, so a config that omits the field
	// keeps the PTY path. Any other value aborts startup (validated at the
	// composition root, not here — the accepted set maps to factories the leaf
	// config package cannot import). Rollback: set back to "pty" (or remove the
	// field) and restart the daemon.
	InteractiveRunner string `json:"interactive_runner"`

	// StdioPermissionPrompt routes non-bypass interactive permission asks over
	// claude's stdio control protocol. The zero value keeps the proven MCP prompt
	// tool path, so an absent field is the rollback posture. Read once at daemon
	// startup; changing the file takes effect after a restart.
	StdioPermissionPrompt bool `json:"stdio_permission_prompt"`
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

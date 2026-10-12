// Package config loads the on-disk Pyrycode configuration file.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
)

// Config is the on-disk schema for ~/.pyry/config.json. Fields are added
// additively over time; consumers reading an older file see missing-field
// defaults via DefaultConfig + overlay-decode in Load.
type Config struct {
	RelayURL              string                 `json:"relay_url"`
	MemorySearchProviders []MemorySearchProvider `json:"memory_search_providers"`

	// Memory is nil when managed memory is unconfigured. Search declarations
	// remain independent; consumers validate these settings before using them.
	Memory *MemorySettings `json:"memory,omitempty"`

	// RelayAdmissionTokens binds private admission credentials to exact identities.
	// Nil means absent; a present map requires private effective-user storage.
	RelayAdmissionTokens map[string]string `json:"relay_admission_tokens,omitempty"`

	// DebugCapture once recorded the daemon's interactive session to a .cast
	// file (#802). The recorder was removed with the terminal runner in #1348,
	// and since #1514 the daemon refuses to start when this is true, so an
	// operator's opt-in cannot be silently ignored. It is still decoded here
	// because Load stays parse-only; the rejection lives at the composition
	// root. An absent field decodes to false and starts normally.
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

// MemorySearchProvider declares search access for one agent and canonical workspace.
// Enabled is a pointer so a missing value cannot silently assert a disabled install.
type MemorySearchProvider struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Agent       string `json:"agent"`
	Workspace   string `json:"workspace"`
	Enabled     *bool  `json:"enabled"`
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
// no error. Malformed JSON and admission configuration return value-free errors
// without parser causes. On any error the returned Config
// is the zero value; callers must check err.
func Load(path string) (Config, error) {
	cfg := DefaultConfig()
	data, file, parent, err := readConfig(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return cfg, nil
		}
		return Config{}, fmt.Errorf("config: read %s: %w", path, err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return Config{}, errors.New("config: parse: invalid JSON configuration")
	}
	if err := validateAdmission(root, file, parent); err != nil {
		return Config{}, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, errors.New("config: parse: invalid JSON configuration")
	}
	return cfg, nil
}

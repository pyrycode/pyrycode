package agentrun

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// SettingsFilename is the fixed basename of the per-spawn settings file
// emitted by WriteSettings inside the caller's workdir. The dispatcher
// gitignores this filename in its own repo; pyry does not modify any
// .gitignore here.
const SettingsFilename = ".pyry-agent-run-settings.json"

// settingsFile is the on-disk shape; field order is load-bearing so that
// Go's struct-field-order serialization produces the canonical
// `{"permissions":{"allow":[...],"defaultMode":"deny"}}` byte sequence.
type settingsFile struct {
	Permissions permissions `json:"permissions"`
}

type permissions struct {
	Allow       []string `json:"allow"`
	DefaultMode string   `json:"defaultMode"`
}

// WriteSettings emits the per-spawn claude settings JSON inside workdir
// and returns the resolved path.
//
// The shape is exactly {"permissions": {"allow": <allowed>, "defaultMode": "deny"}}.
// allowed is round-tripped verbatim — caller is responsible for non-emptiness
// (the pyry agent-run flag parser enforces this at the CLI boundary); a nil or
// empty slice is accepted and produces "allow": [] cleanly (NOT "allow": null).
//
// The file lives at <workdir>/.pyry-agent-run-settings.json. The dispatcher
// gitignores this filename in its repo; pyry does not modify any .gitignore.
//
// Written atomically via the project's standard recipe (os.CreateTemp in the
// same directory → encode → Sync → Close → Rename), at mode 0o600. Overwrite
// of a prior file is safe; the rename is the commit point.
//
// Stdout-marker contract (sibling #332): runAgentRun prints the returned path
// behind the literal prefix `settings-file: ` on a line of its own. The
// dispatcher scrapes that line with `^settings-file: (.+)$`.
func WriteSettings(workdir string, allowed []string) (string, error) {
	if allowed == nil {
		allowed = []string{}
	}
	path := filepath.Join(workdir, SettingsFilename)

	tmp, err := os.CreateTemp(workdir, ".pyry-agent-run-settings-*.tmp")
	if err != nil {
		return "", fmt.Errorf("agentrun: write settings: create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if err := os.Chmod(tmpName, 0o600); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("agentrun: write settings: chmod temp: %w", err)
	}

	enc := json.NewEncoder(tmp)
	if err := enc.Encode(&settingsFile{
		Permissions: permissions{Allow: allowed, DefaultMode: "deny"},
	}); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("agentrun: write settings: encode: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("agentrun: write settings: fsync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("agentrun: write settings: close temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return "", fmt.Errorf("agentrun: write settings: rename: %w", err)
	}
	return path, nil
}

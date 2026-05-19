// Package settings writes per-spawn deny-default permission JSON files for
// interactive claude spawned via PTY drive. The file replicates the tool
// whitelist that `claude -p --allowedTools` enforced before the 2026-05-19
// pivot back to PTY drive (--allowedTools is additive in interactive mode;
// only --settings with defaultMode:"deny" replicates -p semantics — see
// Phase A spike, 2026-05-14).
//
// MUST NOT log the allowedTools slice, the JSON payload, or the returned
// path. Tool names are operational config rather than secret material, but
// the parent agentrun package family's no-content-logging discipline
// applies to every subpackage uniformly.
package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// settingsFile and permissions are field-order-load-bearing: Go's struct
// serialisation produces the canonical
// {"permissions":{"allow":[...],"defaultMode":"deny"}} byte sequence.
type settingsFile struct {
	Permissions permissions `json:"permissions"`
}

type permissions struct {
	Allow       []string `json:"allow"`
	DefaultMode string   `json:"defaultMode"`
}

// WriteSettings generates a per-spawn deny-default permissions JSON file
// in os.TempDir() and returns the absolute path of the written file. The
// caller is responsible for cleanup via `defer os.Remove(path)`; the
// helper does not register cleanup itself on the success path.
//
// JSON shape (compact, no whitespace, trailing \n from json.Encoder.Encode):
//
//	{"permissions":{"allow":[<allowedTools>],"defaultMode":"deny"}}
//
// allowedTools is round-tripped verbatim — element order and any duplicates
// are preserved. The helper performs no deduplication, no sorting, and no
// canonicalisation.
//
// Validation: returns a non-nil error before any file is created when
// len(allowedTools) == 0. The caller has already validated non-emptiness
// at the CLI parse boundary (#470); this check is defence-in-depth.
//
// Tempfile naming: written via os.CreateTemp(os.TempDir(),
// "pyry-agent-run-settings-*.json"); the random infix is owned by the OS.
//
// Error path: if any operation after os.CreateTemp returns successfully
// fails (Encode, Close), the helper best-effort removes the tempfile
// before returning the error. Callers never see a leaked path on the
// error path — they only get a path on success.
func WriteSettings(allowedTools []string) (string, error) {
	if len(allowedTools) == 0 {
		return "", errors.New("agentrun/settings: allowedTools required")
	}

	f, err := os.CreateTemp("", "pyry-agent-run-settings-*.json")
	if err != nil {
		return "", fmt.Errorf("agentrun/settings: create temp: %w", err)
	}
	tmpName := f.Name()

	enc := json.NewEncoder(f)
	if err := enc.Encode(&settingsFile{
		Permissions: permissions{
			Allow:       allowedTools,
			DefaultMode: "deny",
		},
	}); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("agentrun/settings: encode: %w", err)
	}

	if err := f.Close(); err != nil {
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("agentrun/settings: close: %w", err)
	}

	return tmpName, nil
}

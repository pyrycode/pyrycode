// Package trust pre-marks a workdir as trusted in ~/.claude.json so
// interactive claude (spawned via PTY drive) skips the workspace-trust modal.
//
// Best-effort: no file lock. A concurrent writer may produce a lost update;
// tui-driver's HasTrustModal(snap) provides the runtime safety net that
// dismisses the modal if pre-marking lost the race. The helper is still
// atomic on the single-writer axis (tempfile + rename) so a crashed pyry
// mid-write does not leave ~/.claude.json in a broken state for the user's
// own interactive claude sessions.
//
// MUST NOT log file contents at any layer. ~/.claude.json may contain
// tokens or claude-internal state pyry does not own; the helper takes a
// pass-through view (preserve fields verbatim) and emits nothing to logs.
package trust

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/pyrycode/pyrycode/internal/agentrun"
)

// MarkWorkdirTrusted ensures
//
//	~/.claude.json :: projects[<realpath(workdir)>].hasTrustDialogAccepted = true
//
// Idempotent. Atomic — writes to a tempfile in the same directory then
// renames over the target. Returns the resolved realpath on success.
//
// On absent ~/.claude.json the helper creates it with mode 0o600 and a
// minimal skeleton. On existing ~/.claude.json the helper preserves all
// other top-level fields, the projects map's sibling entries, and any
// extra keys on the target entry verbatim — including numeric precision
// (no float64 round-trip of int64-sized values).
func MarkWorkdirTrusted(workdir string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("agentrun/trust: home dir: %w", err)
	}
	return markWorkdirTrustedIn(home, workdir)
}

// markWorkdirTrustedIn is the test seam — tests pass t.TempDir() as homeDir
// directly so they can run in parallel (t.Setenv("HOME", ...) forbids it).
func markWorkdirTrustedIn(homeDir, workdir string) (string, error) {
	realpath, err := agentrun.ResolveWorkdir(workdir)
	if err != nil {
		return "", fmt.Errorf("agentrun/trust: %w", err)
	}
	dataPath := filepath.Join(homeDir, ".claude.json")

	mode := fs.FileMode(0o600)
	if info, err := os.Stat(dataPath); err == nil {
		mode = info.Mode().Perm()
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("agentrun/trust: stat %s: %w", dataPath, err)
	}

	var data []byte
	if b, err := os.ReadFile(dataPath); err == nil {
		data = b
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("agentrun/trust: read %s: %w", dataPath, err)
	}

	root := map[string]any{}
	if len(data) > 0 {
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.UseNumber()
		if err := dec.Decode(&root); err != nil {
			return "", fmt.Errorf("agentrun/trust: parse %s: %w", dataPath, err)
		}
	}

	var projects map[string]any
	if raw, ok := root["projects"]; ok {
		projects, ok = raw.(map[string]any)
		if !ok {
			return "", fmt.Errorf("agentrun/trust: projects in %s is not a JSON object", dataPath)
		}
	} else {
		projects = map[string]any{}
	}
	root["projects"] = projects

	var entry map[string]any
	if raw, ok := projects[realpath]; ok {
		entry, ok = raw.(map[string]any)
		if !ok {
			return "", fmt.Errorf("agentrun/trust: projects[%q] in %s is not a JSON object", realpath, dataPath)
		}
	} else {
		entry = map[string]any{}
	}
	entry["hasTrustDialogAccepted"] = true
	projects[realpath] = entry

	tmp, err := os.CreateTemp(homeDir, ".claude.json.tmp-*")
	if err != nil {
		return "", fmt.Errorf("agentrun/trust: create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := os.Chmod(tmpName, mode); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("agentrun/trust: chmod temp: %w", err)
	}
	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	if err := enc.Encode(root); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("agentrun/trust: encode: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("agentrun/trust: fsync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("agentrun/trust: close temp: %w", err)
	}
	if err := os.Rename(tmpName, dataPath); err != nil {
		return "", fmt.Errorf("agentrun/trust: rename: %w", err)
	}
	return realpath, nil
}

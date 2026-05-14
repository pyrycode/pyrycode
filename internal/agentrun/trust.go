// Package agentrun provides helpers used by the `pyry agent-run` verb (wired
// in #338B) and the JSONL watcher (#333) to interoperate with claude's
// on-disk state.
//
// MUST NOT log file contents at any layer. ~/.claude.json may contain
// tokens or claude-internal state pyry does not own; the helper takes a
// pass-through view (preserve fields verbatim) and emits a key+verdict on
// success, not the underlying bytes.
package agentrun

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// ResolveWorkdir returns the resolved absolute path of workdir, mirroring how
// claude resolves a workdir before reading ~/.claude.json's projects map.
// Resolves symlinks (macOS /var → /private/var). Wraps fs.ErrNotExist when the
// path does not exist.
func ResolveWorkdir(workdir string) (string, error) {
	abs, err := filepath.Abs(workdir)
	if err != nil {
		return "", fmt.Errorf("agentrun: resolve workdir %q: %w", workdir, err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("agentrun: resolve workdir %q: %w", workdir, err)
	}
	return resolved, nil
}

// MarkWorkdirTrusted sets projects[<ResolveWorkdir(workdir)>].
// hasTrustDialogAccepted = true in <homeDir>/.claude.json, under a file lock
// spanning the entire read-modify-write window. Idempotent. Atomic on-disk.
//
// All existing fields in ~/.claude.json are preserved verbatim. Numeric values
// are preserved using json.Number to avoid float64 precision loss.
func MarkWorkdirTrusted(homeDir, workdir string) error {
	key, err := ResolveWorkdir(workdir)
	if err != nil {
		return err
	}

	dataPath := filepath.Join(homeDir, ".claude.json")
	lockPath := dataPath + ".lock"

	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("agentrun: lock open %s: %w", lockPath, err)
	}
	defer func() {
		_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
		_ = lockFile.Close()
	}()
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("agentrun: lock acquire %s: %w", lockPath, err)
	}

	mode := os.FileMode(0o600)
	if info, err := os.Stat(dataPath); err == nil {
		mode = info.Mode().Perm()
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("agentrun: stat %s: %w", dataPath, err)
	}

	root := map[string]any{}
	data, err := os.ReadFile(dataPath)
	switch {
	case err == nil:
		if len(data) > 0 {
			dec := json.NewDecoder(bytes.NewReader(data))
			dec.UseNumber()
			if err := dec.Decode(&root); err != nil {
				return fmt.Errorf("agentrun: parse %s: %w", dataPath, err)
			}
		}
	case errors.Is(err, fs.ErrNotExist):
		// Empty root; will create skeleton.
	default:
		return fmt.Errorf("agentrun: read %s: %w", dataPath, err)
	}

	projectsRaw, ok := root["projects"]
	var projects map[string]any
	if !ok || projectsRaw == nil {
		projects = map[string]any{}
	} else {
		projects, ok = projectsRaw.(map[string]any)
		if !ok {
			return fmt.Errorf("agentrun: parse %s: %q field is not an object", dataPath, "projects")
		}
	}

	entryRaw, ok := projects[key]
	var entry map[string]any
	if !ok || entryRaw == nil {
		entry = map[string]any{}
	} else {
		entry, ok = entryRaw.(map[string]any)
		if !ok {
			return fmt.Errorf("agentrun: parse %s: projects[%q] is not an object", dataPath, key)
		}
	}

	entry["hasTrustDialogAccepted"] = true
	projects[key] = entry
	root["projects"] = projects

	tmp, err := os.CreateTemp(homeDir, ".claude.json.tmp-*")
	if err != nil {
		return fmt.Errorf("agentrun: create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := os.Chmod(tmpName, mode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("agentrun: chmod temp: %w", err)
	}
	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	if err := enc.Encode(root); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("agentrun: encode: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("agentrun: fsync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("agentrun: close temp: %w", err)
	}
	if err := os.Rename(tmpName, dataPath); err != nil {
		return fmt.Errorf("agentrun: rename: %w", err)
	}
	return nil
}


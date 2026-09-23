// Package trust pre-answers, in ~/.claude.json, the startup gates a headless
// claude child cannot answer for itself: the workspace-trust gate, which
// interactive claude renders as a modal, and the approval that lets a
// CLAUDE.md `@` import resolving outside the session's working directory
// expand.
//
// Best-effort: no file lock. A concurrent writer may produce a lost update,
// so the entry can be missing when the child spawns. Nothing catches that at
// runtime: every surviving spawn is headless stream-json, and no pyry code
// watches for or answers a startup dialog. What a lost race costs, per gate:
//
//   - Workspace trust. A 2026-05-14 probe (recorded in the streamrunner
//     package overview) saw stream-json claude run without a trust dialog, but
//     only with --dangerously-skip-permissions, the daemon's flag shape. There
//     is no captured evidence for the --permission-mode dontAsk shape the
//     self-check spawns; `pyry agent-run` spawns that shape without
//     pre-marking at all.
//   - External includes. This gate does apply to stream-json children (#2451,
//     proven by the live claude_md_external_includes test): a lost race means
//     the child starts with its outside-the-cwd `@` imports unexpanded, and
//     claude logs nothing about it.
//
// The helper is still atomic on the single-writer axis (tempfile + rename) so
// a crashed pyry mid-write does not leave ~/.claude.json in a broken state for
// the user's own interactive claude sessions.
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

// MarkWorkdirTrusted ensures, on ~/.claude.json's
// projects[<realpath(workdir)>] entry:
//
//	hasTrustDialogAccepted                  = true
//	hasClaudeMdExternalIncludesApproved     = true
//	hasClaudeMdExternalIncludesWarningShown = true
//
// All three are written unconditionally — an existing false is overwritten.
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
	// Claude expands an `@` import resolving outside the session's working
	// directory only when hasClaudeMdExternalIncludesApproved is true on the
	// entry of the folder that OWNS the CLAUDE.md — marking the folder a child
	// was spawned in does nothing. A headless child cannot answer the approval
	// dialog and nothing is logged when imports are skipped, so an entry left
	// at false silently strips every external import from the injected
	// instructions block (#2451). Assigned unconditionally, like the trust
	// key: an existing false is the state this fixes.
	entry["hasClaudeMdExternalIncludesApproved"] = true
	entry["hasClaudeMdExternalIncludesWarningShown"] = true
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

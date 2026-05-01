package sessions

import (
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// jsonlExt is the suffix claude writes for session transcripts.
const jsonlExt = ".jsonl"

// uuidStemPattern matches the canonical 36-char lowercase UUIDv4 stem claude
// uses for its <uuid>.jsonl filenames. Identical shape to NewID's output.
var uuidStemPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// encodeWorkdir maps a working directory to the path component claude uses
// under ~/.claude/projects/. Verified empirically: claude replaces both '/'
// and '.' with '-', so "/Users/.../.pyrycode-worktrees/x" becomes
// "-Users-...--pyrycode-worktrees-x" (note the doubled dash).
//
//	"/foo/bar"  -> "-foo-bar"
//	"/foo/.bar" -> "-foo--bar"
//	""          -> ""
func encodeWorkdir(workdir string) string {
	if workdir == "" {
		return ""
	}
	r := strings.NewReplacer("/", "-", ".", "-")
	return r.Replace(workdir)
}

// DefaultClaudeSessionsDir returns the directory where claude writes
// <uuid>.jsonl files for the given workdir. Returns "" if workdir is empty
// or $HOME is unresolvable; callers treat "" as "reconciliation disabled".
func DefaultClaudeSessionsDir(workdir string) string {
	if workdir == "" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".claude", "projects", encodeWorkdir(workdir))
}

// mostRecentJSONL scans dir for files matching <uuid>.jsonl (canonical
// 36-char UUID stem) and returns the SessionID of the one with the latest
// ModTime. Non-matching filenames, subdirectories, and entries that fail to
// stat are silently skipped. Returns ("", nil) when no matching entry exists.
//
// On a tie in mtime, the lexicographically-larger UUID wins — deterministic
// for tests; in practice claude doesn't produce ties at second resolution.
func mostRecentJSONL(dir string) (SessionID, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var (
		bestID   SessionID
		bestTime = int64(-1)
	)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, jsonlExt) {
			continue
		}
		stem := name[:len(name)-len(jsonlExt)]
		if !uuidStemPattern.MatchString(stem) {
			continue
		}
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		mt := info.ModTime().UnixNano()
		if mt > bestTime || (mt == bestTime && SessionID(stem) > bestID) {
			bestTime = mt
			bestID = SessionID(stem)
		}
	}
	return bestID, nil
}

// reconcileBootstrapOnNew inspects claude's session dir for the workdir and,
// if the most-recent on-disk JSONL belongs to a different UUID than the
// pool's bootstrap entry, rotates the bootstrap entry to that UUID via
// p.RotateID. A missing/unreadable dir or empty dir is logged and ignored —
// startup proceeds with the existing bootstrap.
//
// This is the seam the live-detection ticket reuses: same RotateID call,
// driven by an fsnotify event instead of a startup scan.
func reconcileBootstrapOnNew(p *Pool, claudeSessionsDir string, log *slog.Logger) error {
	if claudeSessionsDir == "" {
		return nil
	}
	mostRecent, err := mostRecentJSONL(claudeSessionsDir)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Warn("reconcile: read claude sessions dir failed",
				"dir", claudeSessionsDir, "err", err)
		}
		return nil
	}
	if mostRecent == "" {
		return nil
	}
	current := p.Default().ID()
	if mostRecent == current {
		return nil
	}
	log.Info("reconcile: rotating bootstrap session id from on-disk JSONL",
		"from", current, "to", mostRecent, "dir", claudeSessionsDir)
	return p.RotateID(current, mostRecent)
}

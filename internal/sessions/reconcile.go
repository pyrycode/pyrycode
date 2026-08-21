package sessions

import (
	"os"
	"path/filepath"
	"regexp"
)

// workdirNonAlnum matches every character claude replaces when it encodes a
// working directory into its ~/.claude/projects/ path component.
var workdirNonAlnum = regexp.MustCompile(`[^A-Za-z0-9]`)

// encodeWorkdir maps a working directory to the path component claude uses
// under ~/.claude/projects/. Verified empirically: claude replaces EVERY
// non-alphanumeric character with '-', not only '/' and '.'. A space counts,
// so "/Users/.../Second Brain" becomes "-Users-...-Second-Brain" — the earlier
// '/'-and-'.'-only encoder left the space intact and pointed at a folder that
// never exists, so the transcript reader could not find claude's reply.
//
//	"/foo/bar"        -> "-foo-bar"
//	"/foo/.bar"       -> "-foo--bar"
//	"/foo/Second Brain" -> "-foo-Second-Brain"
//	""                -> ""
func encodeWorkdir(workdir string) string {
	if workdir == "" {
		return ""
	}
	return workdirNonAlnum.ReplaceAllString(workdir, "-")
}

// DefaultClaudeSessionsDir returns the directory where claude writes
// <uuid>.jsonl files for the given workdir. Returns "" if workdir is empty
// or $HOME is unresolvable; callers treat "" as "reconciliation disabled".
//
// The workdir is symlink-resolved before encoding (#989): claude encodes its
// RESOLVED cwd into the projects folder name, so on macOS a workdir under
// /var/folders/... (a symlink to /private/var/...) writes transcripts under
// -private-var-folders-.... Encoding the literal form pointed every by-id
// resolver at a folder claude never writes; the old fd-probe path masked this
// because its confidentiality guard compared symlink-resolved paths on both
// sides. Production paths under /Users carry no symlink, which is why only
// tmpdir-based test environments ever saw the mismatch. Resolution failure
// (workdir vanished, permission) falls back to the literal form — same
// best-effort shape as the resolvers' own EvalSymlinks fallbacks.
func DefaultClaudeSessionsDir(workdir string) string {
	if workdir == "" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	resolved, err := filepath.EvalSymlinks(workdir)
	if err != nil {
		resolved = workdir
	}
	return filepath.Join(home, ".claude", "projects", encodeWorkdir(resolved))
}

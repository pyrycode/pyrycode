// Package agentrun owns ResolveWorkdir, the filesystem-path canonicaliser
// used by internal/agentrun/trust to key into ~/.claude.json's projects map.
// JSONL-path encoding (the dashed ~/.claude/projects/<encoded>/ name) lives
// in tuidriver, not here.
//
// MUST NOT log file contents. Callers that consume the resolved paths must
// uphold the same constraint at their layer.
package agentrun

import (
	"fmt"
	"path/filepath"
)

// ResolveWorkdir returns the resolved absolute path of workdir, mirroring how
// claude resolves a workdir before reading ~/.claude.json's projects map.
// Resolves symlinks (macOS /var → /private/var). Wraps fs.ErrNotExist when the
// path does not exist.
//
// Sole remaining caller after #508: internal/agentrun/trust. A follow-up
// issue tracks eventual removal alongside a tuidriver-exposes-canonicalise
// change or a local filepath.EvalSymlinks inside trust.
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

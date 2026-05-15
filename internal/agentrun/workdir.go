// Package agentrun provides workdir-encoding helpers used by the JSONL
// session-file watcher (#349) to locate claude's per-session output under
// ~/.claude/projects/<encoded-cwd>/.
//
// MUST NOT log file contents. Callers that consume the resolved paths must
// uphold the same constraint at their layer.
package agentrun

import (
	"fmt"
	"path/filepath"
	"strings"
)

var projectDirReplacer = strings.NewReplacer("/", "-", ".", "-")

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

// EncodeProjectDir returns the dashed directory-name segment claude uses
// under ~/.claude/projects/ for the given workdir. Chains ResolveWorkdir
// then maps '/' and '.' to '-' in the resolved absolute path. The result
// does NOT include the ~/.claude/projects/ prefix or any .jsonl suffix.
func EncodeProjectDir(workdir string) (string, error) {
	resolved, err := ResolveWorkdir(workdir)
	if err != nil {
		return "", err
	}
	return projectDirReplacer.Replace(resolved), nil
}

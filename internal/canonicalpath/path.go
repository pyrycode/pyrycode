// Package canonicalpath resolves filesystem paths to absolute, symlink-free
// paths with on-disk component casing. It does not enforce confinement or write
// trust state.
package canonicalpath

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Resolve returns the canonical filesystem path of path, whether it names a
// file or a directory. Relative paths resolve against the process's current
// directory; empty input means the current directory. Symlinks are followed,
// then each component is rewritten to its on-disk spelling on a best-effort
// basis. Exact-case matches win; only a unique strings.EqualFold match changes
// spelling. Unreadable directories and absent or ambiguous matches retain the
// input component.
//
// Resolution failures return an empty path and wrap the underlying error,
// preserving errors.Is identity, including fs.ErrNotExist and fs.ErrPermission.
func Resolve(path string) (string, error) {
	// Retain ResolveWorkdir's error context during the consumer migration.
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("agentrun: resolve workdir %q: %w", path, err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("agentrun: resolve workdir %q: %w", path, err)
	}
	return canonicalCase(resolved), nil
}

// canonicalCase improves the casing of an absolute, symlink-free path without
// adding a failure mode. EvalSymlinks can preserve input case on case-insensitive
// filesystems, while consumers such as Claude's trust lookup use on-disk case.
// Linux and macOS roots are bare separators; Windows is out of scope.
func canonicalCase(path string) string {
	canonical := string(os.PathSeparator)
	for _, component := range strings.Split(strings.TrimPrefix(path, canonical), string(os.PathSeparator)) {
		if component == "" {
			continue
		}
		entries, err := os.ReadDir(canonical)
		if err != nil {
			// Best-effort: resolution already succeeded. An execute-only
			// ancestor, for example, can prevent a case probe without
			// preventing traversal. Keep the input component and continue.
			canonical = filepath.Join(canonical, component)
			continue
		}
		canonical = filepath.Join(canonical, matchEntry(entries, component))
	}
	return canonical
}

// matchEntry prefers an exact match, otherwise a unique case-folded match.
// Missing or ambiguous matches preserve target rather than picking a sibling.
func matchEntry(entries []os.DirEntry, target string) string {
	fold := ""
	foldCount := 0
	for _, e := range entries {
		name := e.Name()
		if name == target {
			return name
		}
		if strings.EqualFold(name, target) {
			fold = name
			foldCount++
		}
	}
	if foldCount == 1 {
		return fold
	}
	return target
}

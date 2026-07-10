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
	"os"
	"path/filepath"
	"strings"
)

// ResolveWorkdir returns the resolved absolute path of workdir, canonicalised
// the way claude canonicalises its own cwd before reading ~/.claude.json's
// projects map. It:
//   - resolves relative paths against the current directory (filepath.Abs),
//   - resolves symlinks (filepath.EvalSymlinks; on macOS /var → /private/var), and
//   - rewrites each path component to its on-disk spelling, so a workdir on a
//     case-insensitive filesystem whose configured case differs from disk (e.g.
//     .../WorkSpace configured, .../Workspace on disk) resolves to the spelling
//     claude derives — the key claude reads from the projects map and the key
//     internal/agentrun/trust pre-marks as trusted.
//
// Wraps fs.ErrNotExist when the path does not exist.
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
	return canonicalCase(resolved), nil
}

// canonicalCase rewrites each component of an absolute, symlink-free path to its
// actual on-disk spelling. filepath.EvalSymlinks preserves the input case of a
// component that is not itself a symlink, so on a case-insensitive filesystem a
// wrong-cased workdir survives EvalSymlinks unchanged; claude, by contrast,
// canonicalises to the on-disk case. This step closes that gap.
//
// Best-effort and infallible by contract: path is the output of a successful
// filepath.EvalSymlinks, so it is already absolute, symlink-free, and exists.
// canonicalCase only improves the casing; any obstacle degrades to keeping the
// input component verbatim, which is never worse than returning path unchanged.
// (Windows is out of scope per CLAUDE.md, so the root is a bare separator with
// no volume name to preserve.)
func canonicalCase(path string) string {
	canonical := string(os.PathSeparator)
	for _, component := range strings.Split(strings.TrimPrefix(path, canonical), string(os.PathSeparator)) {
		if component == "" {
			continue
		}
		entries, err := os.ReadDir(canonical)
		if err != nil {
			// Best-effort: the path is already validated by EvalSymlinks; we
			// merely could not learn this component's on-disk casing (e.g. an
			// execute-only, non-readable ancestor). Keep the input component and
			// continue — no new failure mode versus the pre-#910 behaviour.
			canonical = filepath.Join(canonical, component)
			continue
		}
		canonical = filepath.Join(canonical, matchEntry(entries, component))
	}
	return canonical
}

// matchEntry returns the on-disk spelling of target among entries:
//   - an exact-case match wins (the no-regression fast path and, on a
//     case-sensitive filesystem, the sibling-safety guarantee),
//   - otherwise a unique case-insensitive match is the on-disk spelling (the
//     case-insensitive-filesystem wrong-case fix; such a filesystem cannot hold
//     two names differing only by case, so the match is unique), and
//   - zero matches, or two-or-more case-insensitive matches with no exact one,
//     keep target unchanged (best-effort; never pick an arbitrary sibling).
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

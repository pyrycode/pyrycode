// Package transcript owns probe-preferred resolution of claude's session
// transcripts (<uuid>.jsonl under ~/.claude/projects/<encoded-cwd>/) and the
// canonical UUID-stem / .jsonl constants. It is the single authoritative home
// two near-twin resolver families migrate onto:
//
//   - Family A (internal/sessions) — the inbound delivery-confirm growth
//     baseline: (path, size), not-found = nil error.
//   - Family B (cmd/pyry) — the outbound turn-stream tail: (path, offset),
//     not-found = error, with a cold/warm offset rule.
//
// The families share one core: dir canonicalisation, the confidentiality guard
// (canonicalise the probe-reported path, require it to live directly in the
// trusted sessions dir with a <uuid>.jsonl base), by-id / pinned stat, and
// newest-by-mtime selection. This package extracts that core once, as a set of
// composable primitives rather than a single resolver. Their divergent
// concerns — the inverted not-found conventions, the cold/warm offset
// semantics, and the two different pinned-vs-probe dispatch orders — stay in the
// call-site adapters, never here: the core returns a neutral Result so each
// adapter composes it in its own order with its own convention.
//
// The package is a leaf: it imports stdlib only, and in particular not
// internal/sessions, so every consumer can import it with no cycle. That leaf
// rule is why the probe is accepted via a locally-defined one-method interface
// rather than an imported one — originally so it could not depend on
// internal/sessions/rotation, which #2137 has since deleted.
package transcript

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Ext is the suffix claude writes for session transcripts — the single source
// of truth for every family that resolves one.
const Ext = ".jsonl"

// uuidStemPattern matches the canonical 36-char lowercase UUIDv4 stem claude
// uses for its <uuid>.jsonl filenames. Byte-identical to the local regexps it
// replaced in internal/sessions and cmd/pyry (and, until #2137 deleted that
// package, internal/sessions/rotation).
var uuidStemPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// ValidStem reports whether stem is a canonical lowercase UUIDv4 — the one
// matcher callers use instead of a local regexp. Same shape as sessions.NewID's
// output.
func ValidStem(stem string) bool {
	return uuidStemPattern.MatchString(stem)
}

// sessionFileStem returns the UUID stem of a <uuid>.jsonl filename and whether
// it is a valid session transcript name (strips Ext, then ValidStem). A name
// without the Ext suffix, or with a non-UUID stem, yields ("", false).
func sessionFileStem(name string) (string, bool) {
	if !strings.HasSuffix(name, Ext) {
		return "", false
	}
	stem := name[:len(name)-len(Ext)]
	if !ValidStem(stem) {
		return "", false
	}
	return stem, true
}

// Probe reports which JSONL a pid currently holds open ("" = none). Declared
// locally rather than imported, to keep this package a leaf.
//
// It has no production implementation since #2137 deleted internal/sessions/
// rotation, whose per-pid probes were the only ones: real claude opens its
// transcript, appends and closes within milliseconds, so asking the OS what a pid
// holds open practically never answered. This interface and its Probed /
// GuardProbedPath helpers are kept deliberately — they were already callerless
// before that ticket, and removing them is a separate call.
type Probe interface {
	OpenJSONL(pid int) (string, error)
}

// Result is the neutral carrier the core returns. It encodes neither a
// not-found convention nor an offset rule: a caller derives a growth baseline
// as Size directly, or a tail offset as Size (warm) / 0 (cold) using its own
// cold/warm state. An unresolved lookup is the zero Result (Found() == false).
type Result struct {
	Path string
	Size int64
}

// Found reports whether the lookup resolved to a transcript.
func (r Result) Found() bool { return r.Path != "" }

// CanonicalDir returns dir with symlinks resolved (filepath.Clean on error) —
// the canonicalisation the resolver families already do. A caller precomputes it
// once (per construction/subscription) and passes it to GuardProbedPath / Probed.
func CanonicalDir(dir string) string {
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return filepath.Clean(dir)
	}
	return resolved
}

// GuardProbedPath is the confidentiality boundary: it canonicalises the
// probe-reported path and accepts it only if it lives directly in the trusted
// sessions dir with a <uuid>.jsonl base. This closes the one untrusted→trusted
// crossing — under PID reuse the probe's fd table could name any file on disk.
//
// canonicalDir MUST be produced by CanonicalDir over the trusted sessions dir.
// The probed side is symlink-resolved too, so both sides compare over real
// paths (an in-dir symlink pointing out is rejected). Misuse — passing a raw,
// un-canonicalised dir — fails closed: a mismatched compare rejects rather than
// accepting a different real dir.
//
// On accept returns (base, true) where base is the resolved base name the caller
// rebuilds under the ORIGINAL dir. On reject returns ("", false). Stat-free: no
// filesystem mutation, no metadata read.
func GuardProbedPath(probed, canonicalDir string) (base string, ok bool) {
	resolved, err := filepath.EvalSymlinks(probed)
	if err != nil {
		resolved = filepath.Clean(probed)
	}
	if filepath.Dir(resolved) != canonicalDir {
		return "", false
	}
	b := filepath.Base(resolved)
	if _, ok := sessionFileStem(b); !ok {
		return "", false
	}
	return b, true
}

// StatByID resolves a pinned / by-id transcript: stat <dir>/<id>.jsonl. It
// validates id via ValidStem BEFORE any filepath.Join (path-traversal safety,
// defense-in-depth even for server-minted ids — a clean UUID stem has no '/' or
// '..'), so an invalid stem returns (Result{}, err) with no filesystem access.
// A hit returns (Result{path, size}, nil); an absent/unreadable file returns
// (Result{}, err) with the raw os.Stat error. The adapter maps the error to its
// convention (Family A swallows to nil-empty; Family B wraps for retry).
func StatByID(dir, id string) (Result, error) {
	if !ValidStem(id) {
		return Result{}, fmt.Errorf("invalid session id %q", id)
	}
	path := filepath.Join(dir, id+Ext)
	info, err := os.Stat(path)
	if err != nil {
		return Result{}, err
	}
	return Result{Path: path, Size: info.Size()}, nil
}

// Newest selects the <uuid>.jsonl in dir with the latest ModTime, tie-breaking
// on the lexicographically-larger stem (deterministic for tests, stable across
// map-free iteration — matches mostRecentJSONL / resolveLatestSessionJSONL). A
// readdir error returns (Result{}, err); non-matching filenames, subdirectories,
// and entries that vanish between ReadDir and Stat are silently skipped. No
// match returns (Result{}, nil); a hit returns (Result{path, size}, nil).
func Newest(dir string) (Result, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return Result{}, err
	}
	var (
		bestStem string
		bestSize int64
		bestTime = int64(-1)
		found    bool
	)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		stem, ok := sessionFileStem(e.Name())
		if !ok {
			continue
		}
		info, err := os.Stat(filepath.Join(dir, e.Name()))
		if err != nil {
			continue // vanished/raced between ReadDir and Stat — skip, not fatal
		}
		mt := info.ModTime().UnixNano()
		if mt > bestTime || (mt == bestTime && stem > bestStem) {
			bestTime = mt
			bestStem = stem
			bestSize = info.Size()
			found = true
		}
	}
	if !found {
		return Result{}, nil
	}
	return Result{Path: filepath.Join(dir, bestStem+Ext), Size: bestSize}, nil
}

// Probed is probe-preferred resolution: it returns the transcript the pid
// currently holds open, guarded by the confidentiality boundary. Every benign
// no-result collapses to (Result{}, nil) — the core carries no not-found
// vocabulary. The one surfaced error is the probe call itself failing, so a
// retry-on-error adapter can wrap/log it.
//
//   - pid <= 0 (restart backoff / pre-spawn) → (Result{}, nil); the probe is
//     NOT called.
//   - probe error → (Result{}, err); empty open path (claude under --continue
//     holds no .jsonl fd yet) → (Result{}, nil).
//   - guard reject (path outside canonicalDir or non-<uuid>.jsonl base) →
//     (Result{}, nil).
//   - the candidate rebuilt under the ORIGINAL dir raced away before stat →
//     (Result{}, nil); a hit → (Result{candidate, size}, nil).
//
// canonicalDir MUST be CanonicalDir(dir), precomputed once by the caller.
func Probed(dir, canonicalDir string, probe Probe, pid int) (Result, error) {
	if pid <= 0 {
		return Result{}, nil
	}
	open, err := probe.OpenJSONL(pid)
	if err != nil {
		return Result{}, err
	}
	if open == "" {
		return Result{}, nil
	}
	base, ok := GuardProbedPath(open, canonicalDir)
	if !ok {
		return Result{}, nil
	}
	candidate := filepath.Join(dir, base)
	info, err := os.Stat(candidate)
	if err != nil {
		return Result{}, nil
	}
	return Result{Path: candidate, Size: info.Size()}, nil
}

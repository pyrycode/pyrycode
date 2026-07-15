package sessions

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/pyrycode/pyrycode/internal/sessions/rotation"
)

// jsonlExt is the suffix claude writes for session transcripts.
const jsonlExt = ".jsonl"

// uuidStemPattern matches the canonical 36-char lowercase UUIDv4 stem claude
// uses for its <uuid>.jsonl filenames. Identical shape to NewID's output.
var uuidStemPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

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

// newTranscriptResolver returns a supervisor.Config.ResolveTranscript closure
// over dir: the newest <uuid>.jsonl plus its current byte size, ("", 0, nil)
// when none exists yet. Reuses mostRecentJSONL so it selects the same file
// reconcile and the rotation watcher do — which is what lets the turn-bridge
// stream the very turn a growth-confirm observed. A file that vanishes between
// the scan and the os.Stat surfaces as the stat error (the caller treats it as
// "no baseline" / "no growth", never a panic). The ctx param is unused today; it
// satisfies the field signature and lets a future resolver honour cancellation.
func newTranscriptResolver(dir string) func(ctx context.Context) (string, int64, error) {
	return func(context.Context) (string, int64, error) {
		id, err := mostRecentJSONL(dir)
		if err != nil {
			return "", 0, err
		}
		if id == "" {
			return "", 0, nil
		}
		path := filepath.Join(dir, string(id)+jsonlExt)
		info, err := os.Stat(path)
		if err != nil {
			return "", 0, err
		}
		return path, info.Size(), nil
	}
}

// availabilityReporter is the optional interface a rotation.Probe implements to
// declare it cannot answer OpenJSONL — the no-lsof noopProbe returns false. A
// probe that omits the method (the real darwinProbe / linuxProbe) is treated as
// usable. Mirrors cmd/pyry's local copy (interactive_turn_stream_v2.go); the
// shared rotation.Probe interface stays single-method so the import direction
// (sessions -> rotation) is unchanged.
type availabilityReporter interface {
	Available() bool
}

// probeUsable reports whether probe can answer OpenJSONL. The no-lsof noopProbe
// declares itself unusable via availabilityReporter; any probe without that
// method is usable.
func probeUsable(probe rotation.Probe) bool {
	if r, ok := probe.(availabilityReporter); ok {
		return r.Available()
	}
	return true
}

// newProbePreferredTranscriptResolver returns a supervisor.Config.ResolveTranscript
// closure that tracks the transcript the daemon's OWN bootstrap claude child holds
// open — the <uuid>.jsonl reported by probe.OpenJSONL(pidFn()) — instead of the
// newest file by mtime (newTranscriptResolver). This fixes the shared-dir
// collision the delivery-confirm baseline suffers (#838, follow-up to #827's
// turn-stream fix): when a second claude runs in the same cwd it writes its own
// <uuid>.jsonl into the same ~/.claude/projects/<encoded-cwd>/ folder, and the
// newest-by-mtime baseline then latches onto the wrong file, so the growth-confirm
// can false-confirm or hang. The probe answers "which jsonl does exactly THIS pid
// have open", so the baseline follows the daemon's own child regardless of a newer
// sibling.
//
// If the probe cannot answer (the no-lsof noopProbe), delegate to
// newTranscriptResolver(dir) so the no-probe path stays byte-identical to today's
// newest-by-mtime baseline (AC5, mirroring #827's noopProbe fallback). That
// delegated closure is the ONLY returned form that may emit a non-nil error, and
// only because it IS today's behaviour.
//
// The probe path's no-result convention is INVERTED from the sibling
// resolveOwnBootstrapJSONL (cmd/pyry): every no-baseline condition returns
// ("", 0, nil) — a nil error, NEVER a non-nil error and NEVER an mtime fallback.
// This is load-bearing (#838 AC3): confirmViaTranscriptGrowth diverts to the
// stochastic Committed-chip fallback on a non-nil baseline error (supervisor.go),
// the very #668 heuristic the growth-confirm exists to replace, so signalling
// no-baseline as an error would silently reintroduce false-ack risk. ("", 0, nil)
// instead keeps the caller on the growth path: it delivers, then polls until the
// daemon's own child's file appears/grows, or fails loud with ErrTurnNotCommitted.
// The sibling returns errors for the same conditions only because ITS subscriber
// retries on error — copy the guard logic, invert the not-found signal.
//
// Unlike the sibling, this resolver keeps no resolvedOnce/sawEmpty cold/warm
// offset state: the consumer uses the return purely as a (path, size) baseline for
// grew(), so it needs the true current byte size on every call, never a rewound
// tail offset.
//
// Per resolve:
//
//   - pid := pidFn(). pid <= 0 (restart backoff / not yet spawned) -> ("", 0, nil);
//     the probe is NOT called.
//   - probe.OpenJSONL(pid). A probe error, or an empty path (claude under
//     --continue has not created its JSONL fd yet), -> ("", 0, nil) — never mtime.
//   - Confidentiality guard (AC4): canonicalise the probed path and require its
//     directory to equal resolvedDir (precomputed once) AND its base to be a
//     <uuid>.jsonl; reject -> ("", 0, nil). Closes the untrusted probe->path
//     crossing (PID reuse could hand back an unrelated process's fd).
//   - os.Stat the candidate rebuilt under the ORIGINAL dir (the form the rest of
//     the daemon uses; the symlink-resolved form is guard-only). Raced away ->
//     ("", 0, nil). Success -> (candidate, size, nil).
//
// Pinned-id preference (#989): pinnedID, when non-nil and returning a valid
// UUID stem, short-circuits the probe entirely — the resolver stats
// <dir>/<id>.jsonl and returns (path, size, nil), or the ("", 0, nil)
// no-baseline sentinel while the file does not exist yet. The probe path below
// is DEFEATED by real claude: it opens its transcript, appends one event, and
// closes it within milliseconds, so probe.OpenJSONL practically never observes
// an open fd (fakeclaude holds its file open continuously, which is why every
// fake-tier test passes). With the session id pinned at spawn (#839 bootstrap,
// per-conversation pool ids), the path is deterministic and can only ever be
// our own child's file — we minted the uuid — so by-id resolution is strictly
// safer than the probe. pinnedID is consulted per resolve so a /clear rotation
// picked up by reconcile is honoured on the next call. An empty return falls
// through to the probe path (legacy unpinned spawns).
//
// Concurrency: the closure holds no mutable state and takes no locks; pidFn reads
// the live child PID (mutex-guarded via Supervisor.State), so a respawn across
// backoff is observed as a consistent 0-or-live snapshot.
func newProbePreferredTranscriptResolver(dir string, probe rotation.Probe, pidFn func() int, pinnedID func() string) func(ctx context.Context) (string, int64, error) {
	if !probeUsable(probe) {
		// No lsof (or an otherwise-unusable probe): keep today's newest-by-mtime
		// baseline so the no-probe path is byte-identical to pre-fix behaviour.
		// Deliberately unchanged even when pinnedID is set — this branch exists
		// as the untouched legacy fallback.
		return newTranscriptResolver(dir)
	}
	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		resolvedDir = filepath.Clean(dir)
	}
	return func(context.Context) (string, int64, error) {
		if pinnedID != nil {
			if id := pinnedID(); id != "" && uuidStemPattern.MatchString(id) {
				path := filepath.Join(dir, id+jsonlExt)
				info, err := os.Stat(path)
				if err != nil {
					// Not created yet (fresh session pre-first-turn) or raced
					// away — no baseline, same sentinel as the probe path.
					return "", 0, nil
				}
				return path, info.Size(), nil
			}
		}
		pid := pidFn()
		if pid <= 0 {
			// Child in restart backoff or pre-spawn — no process to probe.
			return "", 0, nil
		}
		open, err := probe.OpenJSONL(pid)
		if err != nil || open == "" {
			// Transient probe failure, or claude under --continue holds no .jsonl
			// fd yet. No baseline; never fall back to the colliding mtime file.
			return "", 0, nil
		}
		// Confidentiality guard (AC4): canonicalise the probed path and require it
		// to live directly in the trusted dir with a UUID stem. A file outside dir
		// (PID reuse handing back an unrelated process's fd) is rejected, not
		// stat'd. Both sides are symlink-resolved before the dir compare so an
		// in-dir symlink pointing out cannot slip through.
		openResolved, resolveErr := filepath.EvalSymlinks(open)
		if resolveErr != nil {
			openResolved = filepath.Clean(open)
		}
		base := filepath.Base(openResolved)
		if filepath.Dir(openResolved) != resolvedDir ||
			!strings.HasSuffix(base, jsonlExt) ||
			!uuidStemPattern.MatchString(base[:len(base)-len(jsonlExt)]) {
			return "", 0, nil
		}
		// Rebuild under the original dir — the form the rest of the daemon uses —
		// rather than the guard-only symlink-resolved form.
		candidate := filepath.Join(dir, base)
		info, err := os.Stat(candidate)
		if err != nil {
			// Raced away between probe and stat — no baseline this tick.
			return "", 0, nil
		}
		return candidate, info.Size(), nil
	}
}

package sessions

import (
	"context"
	"os"
	"path/filepath"
	"regexp"

	"github.com/pyrycode/pyrycode/internal/sessions/rotation"
	"github.com/pyrycode/pyrycode/internal/transcript"
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

// newTranscriptResolver returns a supervisor.Config.ResolveTranscript closure
// over dir: the newest <uuid>.jsonl plus its current byte size, ("", 0, nil)
// when none exists yet. Delegates to transcript.Newest so it selects the same
// file the rotation watcher and Family B (cmd/pyry) do — which is what lets the
// turn-bridge stream the very turn a growth-confirm observed. transcript.Newest
// stats each candidate once and returns Path+Size together, so a file that
// vanishes between the readdir and the stat is skipped (not surfaced as an
// error); a missing dir still propagates the os.ReadDir error, which the caller
// treats as "no baseline" / "no growth", never a panic. The ctx param is unused
// today; it satisfies the field signature and lets a future resolver honour
// cancellation.
func newTranscriptResolver(dir string) func(ctx context.Context) (string, int64, error) {
	return func(context.Context) (string, int64, error) {
		res, err := transcript.Newest(dir)
		if err != nil {
			return "", 0, err
		}
		// The zero Result (no match) naturally yields ("", 0, nil).
		return res.Path, res.Size, nil
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
// The resolution core (dir canonicalisation, the confidentiality guard, and
// by-id / probe selection) lives in internal/transcript (#1148); this resolver is
// the Family A adapter over it, composing those primitives in Family A's dispatch
// order and mapping every no-result to Family A's ("", 0, nil) convention.
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
// transcript.Probed already collapses pid<=0 / empty-open / guard-reject /
// vanished-before-stat to (Result{}, nil); the one error it surfaces — the
// probe.OpenJSONL call itself failing — is SWALLOWED here (the "res, _ :=") so it
// too becomes ("", 0, nil). The sibling WRAPS that same error only because ITS
// subscriber retries on error: the neutral core surfaces, each adapter maps to
// its own convention.
//
// Unlike the sibling, this resolver keeps no resolvedOnce/sawEmpty cold/warm
// offset state: the consumer uses the return purely as a (path, size) baseline for
// grew(), so it needs the true current byte size on every call, never a rewound
// tail offset.
//
// Per resolve:
//
//   - Pinned-id preference (#989): pinnedID, when non-nil and returning a valid
//     UUID stem, short-circuits the probe entirely — transcript.StatByID stats
//     <dir>/<id>.jsonl and returns (path, size, nil), or the ("", 0, nil)
//     no-baseline sentinel while the file does not exist yet, and does NOT fall
//     through to the probe. The transcript.ValidStem pre-check is the branch
//     selector, not redundant with StatByID's own check: a VALID pinned id routes
//     to StatByID (a miss is no-baseline, no probe), whereas an EMPTY or INVALID
//     id falls through to the probe path — StatByID alone cannot distinguish
//     "invalid stem" from "valid-but-missing" for that routing decision.
//   - Probe path (pinnedID nil / empty / non-UUID stem): transcript.Probed reports
//     the jsonl pidFn()'s process holds open, guarded (AC4) to live directly in
//     dir with a <uuid>.jsonl base — closing the untrusted probe->path crossing
//     that PID reuse could exploit. Every no-result, and the swallowed probe
//     error, yield ("", 0, nil); never an mtime fallback.
//
// The probe path is DEFEATED by real claude: it opens its transcript, appends one
// event, and closes it within milliseconds, so probe.OpenJSONL practically never
// observes an open fd (fakeclaude holds its file open continuously, which is why
// every fake-tier test passes). With the session id pinned at spawn (#839
// bootstrap, per-conversation pool ids) the path is deterministic and can only
// ever be our own child's file — we minted the uuid — so by-id resolution is
// strictly safer than the probe. pinnedID is consulted per resolve so a /clear
// rotation picked up by reconcile is honoured on the next call.
//
// Concurrency: the closure holds no mutable state and takes no locks; canonicalDir
// is computed once at construction and read-only thereafter; pidFn reads the live
// child PID (mutex-guarded via Supervisor.State), so a respawn across backoff is
// observed as a consistent 0-or-live snapshot.
func newProbePreferredTranscriptResolver(dir string, probe rotation.Probe, pidFn func() int, pinnedID func() string) func(ctx context.Context) (string, int64, error) {
	if !probeUsable(probe) {
		// No lsof (or an otherwise-unusable probe): keep today's newest-by-mtime
		// baseline so the no-probe path is byte-identical to pre-fix behaviour.
		// Deliberately unchanged even when pinnedID is set — this branch exists
		// as the untouched legacy fallback, and is the ONLY returned closure that
		// may emit a non-nil error (because it IS today's behaviour).
		return newTranscriptResolver(dir)
	}
	canonicalDir := transcript.CanonicalDir(dir)
	return func(context.Context) (string, int64, error) {
		if pinnedID != nil {
			if id := pinnedID(); id != "" && transcript.ValidStem(id) {
				res, err := transcript.StatByID(dir, id)
				if err != nil {
					// Not created yet (fresh session pre-first-turn) or raced
					// away — no baseline; do NOT fall through to the probe.
					return "", 0, nil
				}
				return res.Path, res.Size, nil
			}
		}
		// Swallow transcript.Probed's one surfaced error (a failing probe.OpenJSONL)
		// into the same ("", 0, nil) no-baseline sentinel — the convention inversion
		// vs the cmd/pyry sibling, and never an mtime fallback.
		res, _ := transcript.Probed(dir, canonicalDir, probe, pidFn())
		return res.Path, res.Size, nil
	}
}

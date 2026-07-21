package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/sessions/rotation"
	"github.com/pyrycode/pyrycode/internal/supervisor"
	"github.com/pyrycode/pyrycode/internal/transcript"
	"github.com/pyrycode/pyrycode/internal/turnbridge"
	"github.com/pyrycode/pyrycode/internal/turnevent"
	"github.com/pyrycode/tui-driver/pkg/tuidriver"
)

// newBootstrapProbe builds the rotation.Probe the bootstrap resolver uses to ask
// "which <uuid>.jsonl does the daemon's OWN claude child have open?". Indirected
// via a package var (mirrors sessions.newProbe) so tests inject a fake without
// touching the platform-specific build files.
var newBootstrapProbe = rotation.DefaultProbe

// availabilityReporter is the optional interface a rotation.Probe implements to
// declare it cannot answer OpenJSONL — the no-lsof noop probe returns false. A
// probe that does not implement it (the real darwinProbe / linuxProbe) is treated
// as usable. Kept local so the shared rotation.Probe interface stays unchanged.
type availabilityReporter interface {
	Available() bool
}

// bootstrapProbeUsable reports whether probe can answer OpenJSONL. The no-lsof
// noop probe declares itself unusable via availabilityReporter; any probe without
// that method is usable.
func bootstrapProbeUsable(probe rotation.Probe) bool {
	if r, ok := probe.(availabilityReporter); ok {
		return r.Available()
	}
	return true
}

// startInteractiveTurnStreamV2 wires the #615 structured-event producer to the
// #632 capability-gated emitter so the active conversation's turn events flow to
// interactive phones. It constructs the emitter over the v2 manager, builds the
// producer with NewTargetSubscriber driven by the follow-active resolveTarget
// (#679) + an OnEvent callback bridging each event to emitter.Handle, starts the
// producer goroutine, and returns a cleanup that blocks until that goroutine
// exits.
//
// The OnEvent closure captures ctx (the relay lifecycle ctx) — the ctx-less
// OnEvent -> ctx-ful Handle seam #632 named. It runs ONLY on the producer's
// single Run goroutine (drain invokes OnEvent serially), so the emitter's
// unguarded counters never race — #632's named single-Run-goroutine assumption
// holds by construction.
//
// The cursor for both the live emitter and the #647 replay source is the
// active-conversation signal (#687), NOT the bootstrap supervisor's
// CurrentConversation(). Since #678 routes turns to bound-session supervisors,
// the bootstrap cursor stays empty and the structured stream would drop every
// event; active is stamped by sessionRouter.Route, so the stream emits and
// stamps the routed conversation's id.
//
// The producer now FOLLOWS the active conversation (#679): resolveTarget maps
// the active conversation to its bound session's supervisor + a by-id JSONL
// resolver (mtime-independent), so the reply tails that conversation's own
// transcript and never another conversation's more-recently-written file. boundHost
// is the conv→session→supervisor lookup; sup stays a param as the
// before-any-route bootstrap fallback host (AC4).
func startInteractiveTurnStreamV2(
	ctx context.Context,
	sup *supervisor.Supervisor,
	active *activeConversation,
	boundHost boundHostFunc,
	mgr *relay.V2SessionManager,
	claudeSessionsDir string,
	probe rotation.Probe,
	pidFn func() int,
	bootstrapIDFn func() string,
	logger *slog.Logger,
) func() {
	emitter := newInteractiveTurnEmitterV2(active, mgr, logger)
	// Publish the emitter's event ring + the conversation cursor to the manager
	// so a phone reconnecting with hello.last_event_id can be replayed the
	// missed tail (#647). Late-bound here (not a V2SessionConfig field) to break
	// the emitter↔manager construction cycle: the ring is created inside the
	// emitter constructor above, after NewV2SessionManager already ran. This is
	// the only call site; when the stream is disabled the manager keeps a nil
	// replay source and a reconnecting phone simply gets the live stream.
	//
	// The replay cursor must follow the SAME active-conversation signal as the
	// live emitter (#687): leaving it on the empty bootstrap CurrentConversation
	// would re-introduce the empty-cursor drop on the reconnect-replay path, so
	// replayed envelopes would drop or carry the wrong attribution.
	mgr.SetReplaySource(emitter.ring, active.CurrentConversation)
	// Session.Events requires a Tracker; zero opts -> package defaults. The
	// tracker's stall_detected marker now maps through to a stall envelope
	// (the mapper no longer discards it).
	tr := tuidriver.NewTracker(tuidriver.TrackerOpts{})
	resolve := resolveTarget(active, boundHost, sup, claudeSessionsDir, probe, pidFn, bootstrapIDFn)
	sub := turnbridge.NewTargetSubscriber(resolve, tr, logger)

	prod, err := turnbridge.New(turnbridge.Config{
		Subscribe: sub,
		OnEvent:   func(ev turnevent.Event) { emitter.Handle(ctx, ev) },
		// Delta coalescing (#609): the emitter owns the ~250ms timer; the producer
		// selects its channel and routes the fire back into flushDelta on the same
		// single Run goroutine as OnEvent — symmetric partner of the OnEvent
		// closure, both capturing the relay lifecycle ctx.
		FlushSignal: emitter.flushC(),
		OnFlush:     func() { emitter.flushDelta(ctx) },
		Logger:      logger,
	})
	if err != nil {
		// Unreachable: New errors only on a nil Subscribe. Fail soft for an
		// optional surface rather than taking down the relay leg.
		logger.Warn("relay: interactive turn stream disabled; producer build failed",
			"event", "interactive_turn_stream.build_err", "err", err)
		return func() {}
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := prod.Run(ctx); err != nil {
			// Run returns only ctx.Err() per its contract; debug-log and exit.
			logger.Debug("relay: interactive turn stream run returned", "err", err)
		}
	}()
	return func() { <-done }
}

// resolveLatestSessionJSONL is the recency resolver. Each call scans dir for
// <uuid>.jsonl files and returns the most-recently-modified one plus a
// startOffset. Since #679 this is the convID == "" (no-route-yet / bootstrap)
// branch of resolveTarget — the AC4 unchanged-bootstrap path; once a turn is
// routed the by-id resolveBoundSessionJSONL takes over. Because the subscriber
// calls resolve fresh on every (re)subscription, returning the newest file each
// time still lets a pre-route bootstrap rotation pick up the new JSONL.
//
// startOffset = TailFromEnd (own-fd EOF) means the tail seeks to the end of its
// OWN fd — no caller os.Stat of a size a rotation could stale between resolve and
// tail-open — so a (re)subscription streams only NEW events and never replays the
// historical transcript to the phone. This is the right default for a warm resume
// (a --continue transcript already on disk) and for a /clear rotation.
//
// Cold start (#671) is the one exception. On a fresh relay session there is no
// transcript on disk when the producer first subscribes (claude under --continue
// defers JSONL creation until the first input lands), so an early resolve reports
// not-found and the subscriber retries. The phone's prompt then lands and claude
// writes the user turn + the assistant reply in one go, so the next resolve finds
// a brand-new file whose whole content IS the current turn. Returning size there
// would start the tail at EOF — past the in-flight reply — and the reply would
// never stream to the phone (the live mobile#421 drop). For that case alone the
// resolver returns startOffset = 0 so the tail begins at the file's start.
//
// The discrimination is stateful across calls: the FIRST file returned after one
// or more not-found results (and before any file has been returned) is a
// cold-start file -> offset 0. A file present at the first look (warm resume) or
// any file after one has already been returned (a rotation) -> TailFromEnd.
// Offset 0 is confined to a brand-new session file — there is no prior transcript
// to leak, because a resumed transcript would already exist on disk and take the
// warm path (see TestResolveLatestSessionJSONL_WarmStartTailsFromEnd).
//
// Concurrency: resolvedOnce / sawEmpty are read and written only inside the
// returned closure, which NewTargetSubscriber invokes from the single
// Producer.Run goroutine (Run -> subscribe -> resolve). They therefore need no
// mutex — the same single-Run-goroutine invariant the OnEvent / flushDelta
// closures rely on. Do NOT call this resolver from multiple goroutines.
//
// The resolver is otherwise pure (dir -> newest path): no $HOME / cwd / symlink
// dependency, so it unit-tests against a t.TempDir(). It reuses the daemon's
// already-computed claudeSessionsDir (the exact dir reconcile + the rotation
// watcher use) rather than recomputing via a second cwd-encoder — single source
// of truth, coherent with the shipped machinery.
func resolveLatestSessionJSONL(dir string) func(ctx context.Context) (path string, startOffset int64, err error) {
	var (
		resolvedOnce bool // a session file has been returned at least once
		sawEmpty     bool // an earlier call found no file (empty/absent dir)
	)
	return func(ctx context.Context) (string, int64, error) {
		res, err := transcript.Newest(dir)
		if err != nil {
			// The os.ReadDir error: a not-yet-created project dir is a cold-start
			// signal too (claude creates it lazily on first input), so count it as
			// "no file yet". Wrap with the path (a path/errno, never file bytes).
			if !resolvedOnce {
				sawEmpty = true
			}
			return "", 0, fmt.Errorf("read claude sessions dir %s: %w", dir, err)
		}
		if !res.Found() {
			// No matching <uuid>.jsonl in the dir. Family B returns an error so the
			// subscriber retries (error-on-not-found convention).
			if !resolvedOnce {
				sawEmpty = true
			}
			return "", 0, fmt.Errorf("no session jsonl found in %s", dir)
		}
		off := int64(tuidriver.TailFromEnd)
		if !resolvedOnce && sawEmpty {
			// Cold start: this fresh file appeared only after an earlier
			// not-found, so the whole file is the current turn — tail from 0.
			off = 0
		}
		resolvedOnce = true
		return res.Path, off, nil
	}
}

// resolveBootstrapJSONL picks the bootstrap transcript resolver (#989). When the
// bootstrap spawn's session id is pinned (#839: --session-id <bootstrap pool
// id>, the production path), the transcript path is deterministic and can only
// ever be our own child's file — the daemon minted the uuid — so it resolves
// by-id via resolveBoundSessionJSONL, with that resolver's cold/warm offset
// rule. When no pinned id is available (legacy/unpinned spawn), it falls back to
// the #854 PID-probe resolver below.
//
// Why the probe cannot stay preferred: real claude opens its transcript,
// appends one event, and closes it within milliseconds, so probe.OpenJSONL
// practically never observes an open fd — the subscription retries forever and
// no reply ever streams (the residual fresh-daemon deadlock behind
// pyrycode-mobile#528). fakeclaude holds its transcript open continuously,
// which is why every fake-tier e2e passes over the probe.
//
// pinnedID is consulted once per subscription (this function runs per
// resolveTarget invocation), so a /clear id rotation picked up by reconcile is
// honoured on the next resubscribe.
func resolveBootstrapJSONL(dir string, pinnedID func() string, probe rotation.Probe, pidFn func() int) func(ctx context.Context) (path string, startOffset int64, err error) {
	if pinnedID != nil {
		if id := pinnedID(); id != "" && transcript.ValidStem(id) {
			return resolveBoundSessionJSONL(dir, id)
		}
	}
	return resolveOwnBootstrapJSONL(dir, probe, pidFn)
}

// resolveOwnBootstrapJSONL is the probe-preferred bootstrap resolver. Instead of
// picking the newest <uuid>.jsonl in dir by mtime (resolveLatestSessionJSONL), it
// tails the transcript the daemon's OWN claude child actually has open — the file
// the pid returned by pidFn holds, reported by probe.OpenJSONL. This fixes the
// shared-directory collision: when a second claude (an interactive Claudian) runs
// in the same cwd, it writes its own <uuid>.jsonl into the same
// ~/.claude/projects/<encoded-cwd>/ folder, and the newest-by-mtime heuristic
// then tails the wrong claude's transcript so the phone gets no reply or the wrong
// one. The probe answers "which jsonl does exactly THIS pid have open", so the
// tail follows the daemon's own child regardless of a newer sibling.
//
// Construction time: if the probe cannot answer (the no-lsof noop), delegate to
// resolveLatestSessionJSONL(dir) so the no-lsof path stays byte-identical to the
// pre-fix behaviour. Otherwise precompute canonicalDir once via
// transcript.CanonicalDir (symlink-resolved, Clean on error) for the
// confidentiality guard below, mirroring the rotation watcher's dir canonicalisation.
//
// Per call (the returned closure keeps resolvedOnce / sawEmpty, the same
// per-subscription cold/warm offset rule as resolveBoundSessionJSONL):
//
//  1. pid := pidFn(). pid <= 0 means the child is in restart backoff or has not
//     spawned yet, so there is no fd to probe: mark sawEmpty (if not yet resolved)
//     and return a not-found error so the subscriber retries in 500ms. The probe
//     is NOT called.
//  2. probe.OpenJSONL(pid). A probe error → not-found (retry). An empty path is a
//     cold start (claude under --continue defers JSONL creation until the first
//     input lands, so the child holds no .jsonl fd yet) → mark sawEmpty, return
//     not-found. It NEVER falls back to the newest-by-mtime file — that is exactly
//     the colliding heuristic this resolver exists to avoid.
//  3. Validate the reported path via transcript.GuardProbedPath(open, canonicalDir):
//     it canonicalises the probed path and accepts it only if it lives directly in
//     canonicalDir with a <uuid>.jsonl base (the confidentiality guard — never tail
//     a file outside the trusted sessions dir, e.g. if PID reuse hands back an
//     unrelated process's fd). A reject returns a not-found error, no sawEmpty.
//  4. os.Stat (the path may vanish between probe and stat → retry — the stat is
//     the vanish-race existence gate; its size is no longer read). Offset =
//     TailFromEnd (own-fd EOF), or 0 when the file first appears after an earlier
//     empty look (cold start, so the whole file is the current turn). Set
//     resolvedOnce.
//
// The returned path is rebuilt under the original dir (filepath.Join(dir, base)),
// the same form the sibling resolvers and the rest of the daemon use over
// claudeSessionsDir; the symlink-resolved form is used only for the guard.
//
// Concurrency: resolvedOnce / sawEmpty are read and written only inside the
// returned closure, which the single Producer.Run goroutine invokes. Same
// single-Run-goroutine invariant the sibling resolvers rely on; no mutex. Do NOT
// call this resolver from multiple goroutines.
func resolveOwnBootstrapJSONL(dir string, probe rotation.Probe, pidFn func() int) func(ctx context.Context) (path string, startOffset int64, err error) {
	if !bootstrapProbeUsable(probe) {
		// No lsof (or an otherwise-unusable probe): keep the exact pre-fix
		// newest-by-mtime behaviour so the no-probe path is unchanged.
		return resolveLatestSessionJSONL(dir)
	}
	// Precompute the canonical (symlink-resolved) dir once for the confidentiality
	// guard below, mirroring the rotation watcher's dir canonicalisation.
	canonicalDir := transcript.CanonicalDir(dir)
	var (
		resolvedOnce bool // a session file has been returned at least once
		sawEmpty     bool // an earlier call found no file (pid down / no fd yet)
	)
	return func(ctx context.Context) (string, int64, error) {
		pid := pidFn()
		if pid <= 0 {
			// Child in restart backoff or pre-spawn — no process to probe.
			if !resolvedOnce {
				sawEmpty = true
			}
			return "", 0, fmt.Errorf("bootstrap child not running (pid %d)", pid)
		}
		open, err := probe.OpenJSONL(pid)
		if err != nil {
			// Transient probe failure (e.g. an lsof hiccup) — retry, never mtime.
			return "", 0, fmt.Errorf("probe open jsonl for pid %d: %w", pid, err)
		}
		if open == "" {
			// Cold start: claude under --continue has not created its JSONL yet,
			// so the child holds no .jsonl fd. Retry; never fall back to mtime.
			if !resolvedOnce {
				sawEmpty = true
			}
			return "", 0, fmt.Errorf("bootstrap child pid %d has no jsonl open yet", pid)
		}
		// Confidentiality guard: GuardProbedPath canonicalises the probed path and
		// accepts it only if it lives directly in the trusted dir (canonicalDir)
		// with a <uuid>.jsonl base. A file outside dir (PID reuse handing back some
		// unrelated process's fd) is rejected, not tailed. No sawEmpty here — a
		// guard reject is a security decision, not a cold-start "no file yet".
		// (The two pre-migration reject strings collapse to one; the raw probed
		// path was never named in either message and is not named here.)
		base, ok := transcript.GuardProbedPath(open, canonicalDir)
		if !ok {
			return "", 0, fmt.Errorf("probed jsonl outside sessions dir %s or not a session jsonl", canonicalDir)
		}
		// Return the path under the original dir — the same form the sibling
		// resolvers use — rather than the guard-only symlink-resolved form.
		candidate := filepath.Join(dir, base)
		// The stat is the vanish-race existence gate (the path may disappear between
		// the probe and here → retry); its size is no longer read — the tail owns its
		// fd end via TailFromEnd below.
		if _, err := os.Stat(candidate); err != nil {
			// Raced away between probe and stat — retry.
			if !resolvedOnce {
				sawEmpty = true
			}
			return "", 0, fmt.Errorf("stat probed jsonl %s: %w", candidate, err)
		}
		off := int64(tuidriver.TailFromEnd)
		if !resolvedOnce && sawEmpty {
			// Cold start: this fresh file appeared only after an earlier empty
			// look, so the whole file is the current turn — tail from 0.
			off = 0
		}
		resolvedOnce = true
		return candidate, off, nil
	}
}

// boundHostFunc resolves a conversation id to the supervisor hosting its bound
// claude session, that session's id, AND the directory claude writes that
// session's transcript into (its per-Cwd JSONL dir since #686 — see
// perConversationSessionsDir). (nil, "", "", false) when the conversation is
// unknown, has no bound session, its session id misses in the pool, or its
// per-Cwd directory can't be derived — the follow-active resolver turns any of
// those into a retry, never a bootstrap fallback (cross-conversation
// confidentiality, #679/#686). Built in runSupervisor where the pool +
// conversations registry are concrete; *supervisor.Supervisor satisfies
// turnbridge.SessionHost, so the bound supervisor is both subscription host and
// PTY-state source.
type boundHostFunc func(convID string) (host turnbridge.SessionHost, sessionID, dir string, ok bool)

// perConversationSessionsDir returns the directory claude writes a bound
// session's <id>.jsonl into, given that session's spawn workdir (#686). claude
// keys its transcript directory off the cwd it was launched with, so the
// authoritative input is supervisor.Config.WorkDir — the realpath captured at
// CreateIn time (#685), not the raw recorded conv.Cwd.
//
//   - sessionWorkDir == bootstrapWorkDir → sharedDir. A default (null-Cwd)
//     conversation spawns in the bootstrap workdir, so it keeps resolving from
//     the startup-computed shared claudeSessionsDir, byte-for-byte unchanged
//     (AC3) — including the latent abs-vs-realpath skew that dir already carries.
//   - sessionWorkDir == "" → sharedDir (defensive default; a supervisor built
//     with no WorkDir inherits the process cwd, same shared-dir treatment).
//   - otherwise → sessions.DefaultClaudeSessionsDir(sessionWorkDir), the single
//     source of truth for the ~/.claude/projects/<encoded-cwd>/ encoding
//     (AC1/AC2). Returns "" only when DefaultClaudeSessionsDir can't encode (no
//     $HOME); the caller treats "" as unresolvable (retry, never fall back).
//
// Pure string logic over its three inputs (the one os.UserHomeDir read inside
// DefaultClaudeSessionsDir aside) — no re-canonicalisation, no symlink syscalls.
func perConversationSessionsDir(sessionWorkDir, bootstrapWorkDir, sharedDir string) string {
	if sessionWorkDir == "" || sessionWorkDir == bootstrapWorkDir {
		return sharedDir
	}
	return sessions.DefaultClaudeSessionsDir(sessionWorkDir)
}

// resolveTarget is the follow-active TargetResolver (#679). On each
// (re)subscription it snapshots the active conversation — its id AND the channel
// that fires when the id next changes, atomically via active.watch() so host +
// path + teardown all key off one consistent view — and maps it to a Target:
//
//   - convID == "" (no route yet): the bootstrap host + the probe-preferred
//     bootstrap resolver (resolveOwnBootstrapJSONL), which tails the transcript
//     the daemon's OWN claude child has open rather than the newest file by mtime,
//     so a second claude writing into the same shared dir cannot redirect the
//     tail. Switch is still set so the first route re-subscribes onto the routed
//     bound session.
//   - convID != "" and resolvable to the BOOTSTRAP session: the bootstrap
//     supervisor + the probe-preferred bootstrap resolver (resolveOwnBootstrapJSONL),
//     NOT the by-id resolver. The bootstrap claude spawns without --session-id
//     (#854), so it mints its own on-disk transcript uuid that never equals its
//     pool id; a by-id resolver would tail <poolID>.jsonl, which never appears,
//     and loop forever (the fresh-daemon deadlock this ticket fixes). Binding by
//     PID probe converges the "no jsonl yet" wait once the first turn lands.
//   - convID != "" and resolvable to a NON-bootstrap session: that session's
//     supervisor + a by-id resolver that tails <bound-session-id>.jsonl in that
//     conversation's OWN per-Cwd JSONL directory (returned by boundHost since
//     #686), mtime-independent and never another Cwd's directory (AC1/AC2).
//   - convID != "" but unresolvable (deleted/unbound mid-flight): an error so the
//     subscriber backs off and retries. It NEVER falls back to the bootstrap under
//     a non-empty cursor — the emitter stamps convID, so tailing any other
//     transcript would cross-stream another conversation's output (the
//     confidentiality property this ticket protects).
//
// A fresh JSONL resolver closure is built per call so its cold/warm offset state
// is per-subscription: a brand-new bound session cold-starts at offset 0; a
// switch-back to a live session warm-tails from EOF.
func resolveTarget(active *activeConversation, boundHost boundHostFunc, bootstrap turnbridge.SessionHost, dir string, probe rotation.Probe, pidFn func() int, bootstrapIDFn func() string) turnbridge.TargetResolver {
	return func(ctx context.Context) (turnbridge.Target, error) {
		convID, switchCh := active.watch()
		if convID == "" {
			return turnbridge.Target{
				Host:    bootstrap,
				Resolve: resolveBootstrapJSONL(dir, bootstrapIDFn, probe, pidFn),
				Switch:  switchCh,
			}, nil
		}
		host, sessionID, convDir, ok := boundHost(convID)
		if !ok {
			return turnbridge.Target{}, fmt.Errorf("no bound session for active conversation %q", convID)
		}
		if host == bootstrap {
			// The conversation is bound to the bootstrap session — the daemon's
			// own interactive claude. Since #839 that spawn pins
			// --session-id <bootstrap pool id>, so the transcript path is
			// deterministic; resolveBootstrapJSONL prefers it and falls back to
			// the PID probe only for an unpinned legacy spawn (#989 — the probe
			// is defeated by real claude's open-append-close write pattern, so
			// preferring it deadlocked every real bootstrap turn). dir is the
			// shared claudeSessionsDir; boundHost also returns it as convDir for
			// the bootstrap (perConversationSessionsDir maps the bootstrap
			// workdir back to it), so the two agree — use dir to mirror the
			// convID == "" branch.
			return turnbridge.Target{
				Host:    host,
				Resolve: resolveBootstrapJSONL(dir, bootstrapIDFn, probe, pidFn),
				Switch:  switchCh,
			}, nil
		}
		// convDir is the bound session's OWN per-Cwd JSONL directory (#686), not
		// the bootstrap-branch dir param (which stays the shared claudeSessionsDir
		// for the convID == "" path above). A per-Cwd conversation's transcript
		// lives in its own ~/.claude/projects/<encoded-cwd>/ folder, so the by-id
		// resolver must tail <sessionID>.jsonl under convDir.
		return turnbridge.Target{
			Host:    host,
			Resolve: resolveBoundSessionJSONL(convDir, sessionID),
			Switch:  switchCh,
		}, nil
	}
}

// resolveBoundSessionJSONL returns a resolve closure that tails a FIXED bound
// session transcript — <sessionID>.jsonl under dir — instead of scanning for the
// newest file. Because the path is keyed off the bound session id, another
// session writing more recently can never redirect the tail (AC2, the
// cross-conversation confidentiality property). Sibling of
// resolveLatestSessionJSONL, mirroring that resolver's per-subscription cold/warm
// offset rule over one fixed file.
//
// Offset (mtime-independent): the file absent at the first look then appearing is
// a cold start (a brand-new bound session whose whole file is the current turn)
// → offset 0 so the in-flight reply streams (#671, per bound session). Present at
// the first look is a warm resume / switch-back → TailFromEnd (own-fd EOF, the
// tail owns its fd — never replay the conversation's history to the
// internet-exposed phone).
//
// Concurrency: resolvedOnce / sawEmpty are read and written only inside the
// returned closure, which NewTargetSubscriber invokes from the single
// Producer.Run goroutine. They therefore need no mutex — the same
// single-Run-goroutine invariant resolveLatestSessionJSONL relies on. Do NOT
// call this resolver from multiple goroutines.
func resolveBoundSessionJSONL(dir, sessionID string) func(ctx context.Context) (path string, startOffset int64, err error) {
	var (
		resolvedOnce bool
		sawEmpty     bool
	)
	return func(ctx context.Context) (string, int64, error) {
		// Path-safety branch-selector (defense-in-depth): sessionID is already a
		// server-minted UUID from the trusted registry/pool, but validate the stem
		// HERE so a malformed id never reaches the sawEmpty-setting stat-error
		// branch below. A clean UUID stem contains no '/' or '.', so the join in
		// StatByID cannot traverse out. This pre-check is NOT redundant with
		// StatByID's own internal ValidStem: it is the branch selector that keeps
		// an invalid stem out of the cold/warm state (no sawEmpty on an invalid
		// id), matching the pre-migration behaviour AC3 pins.
		if !transcript.ValidStem(sessionID) {
			return "", 0, fmt.Errorf("invalid bound session id %q", sessionID)
		}
		res, err := transcript.StatByID(dir, sessionID)
		if err != nil {
			// File absent / raced away. Family B returns an error so the subscriber
			// retries. Wrap with the path (a path/errno, never file bytes).
			if !resolvedOnce {
				sawEmpty = true
			}
			return "", 0, fmt.Errorf("stat bound session jsonl %s: %w", filepath.Join(dir, sessionID+transcript.Ext), err)
		}
		off := int64(tuidriver.TailFromEnd)
		if !resolvedOnce && sawEmpty {
			// Cold start: this fresh file appeared only after an earlier absent
			// look, so the whole file is the current turn — tail from 0.
			off = 0
		}
		resolvedOnce = true
		return res.Path, off, nil
	}
}

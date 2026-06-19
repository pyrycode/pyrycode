package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/supervisor"
	"github.com/pyrycode/pyrycode/internal/turnbridge"
	"github.com/pyrycode/pyrycode/internal/turnevent"
	"github.com/pyrycode/tui-driver/pkg/tuidriver"
)

// jsonlStreamExt is the suffix claude writes for session transcripts.
const jsonlStreamExt = ".jsonl"

// jsonlStemPattern matches the canonical 36-char lowercase UUIDv4 stem claude
// uses for its <uuid>.jsonl filenames. Duplicated (not reused) from
// internal/sessions.uuidStemPattern: the resolver lives in package main and the
// sessions helper is private. Filtering to the same stem shape means the
// resolver selects the SAME file mostRecentJSONL (reconcile + the fsnotify
// rotation watcher) would for the same dir — coherent-by-construction with the
// rest of the daemon over claudeSessionsDir.
var jsonlStemPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

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
	resolve := resolveTarget(active, boundHost, sup, claudeSessionsDir)
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
// startOffset = size means the tail starts at EOF, so a (re)subscription streams
// only NEW events and never replays the historical transcript to the phone.
// This is the right default for a warm resume (a --continue transcript already
// on disk) and for a /clear rotation.
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
// any file after one has already been returned (a rotation) -> size. Offset 0 is
// confined to a brand-new session file — there is no prior transcript to leak,
// because a resumed transcript would already exist on disk and take the warm
// path (see TestResolveLatestSessionJSONL_WarmStartTailsFromSize).
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
		entries, err := os.ReadDir(dir)
		if err != nil {
			// A not-yet-created project dir is a cold-start signal too (claude
			// creates it lazily on first input), so count it as "no file yet".
			if !resolvedOnce {
				sawEmpty = true
			}
			// Wrap with the path (an os. error — a path/errno, never file bytes).
			return "", 0, fmt.Errorf("read claude sessions dir %s: %w", dir, err)
		}
		var (
			bestName string
			bestSize int64
			bestTime = int64(-1)
		)
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if !strings.HasSuffix(name, jsonlStreamExt) {
				continue
			}
			if !jsonlStemPattern.MatchString(name[:len(name)-len(jsonlStreamExt)]) {
				continue
			}
			info, err := os.Stat(filepath.Join(dir, name))
			if err != nil {
				continue // vanished/raced between ReadDir and Stat — skip, not fatal
			}
			// Tie-break on the lexicographically-larger name (deterministic for
			// tests; matches mostRecentJSONL). Stable across map-free iteration.
			mt := info.ModTime().UnixNano()
			if mt > bestTime || (mt == bestTime && name > bestName) {
				bestTime = mt
				bestName = name
				bestSize = info.Size()
			}
		}
		if bestName == "" {
			if !resolvedOnce {
				sawEmpty = true
			}
			return "", 0, fmt.Errorf("no session jsonl found in %s", dir)
		}
		off := bestSize
		if !resolvedOnce && sawEmpty {
			// Cold start: this fresh file appeared only after an earlier
			// not-found, so the whole file is the current turn — tail from 0.
			off = 0
		}
		resolvedOnce = true
		return filepath.Join(dir, bestName), off, nil
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
//   - convID == "" (no route yet): the bootstrap host + the recency resolver, the
//     unchanged pre-#679 path (AC4). Switch is still set so the first route
//     re-subscribes onto the routed bound session.
//   - convID != "" and resolvable: the bound session's supervisor + a by-id
//     resolver that tails <bound-session-id>.jsonl in that conversation's OWN
//     per-Cwd JSONL directory (returned by boundHost since #686), mtime-
//     independent and never another Cwd's directory (AC1/AC2).
//   - convID != "" but unresolvable (deleted/unbound mid-flight): an error so the
//     subscriber backs off and retries. It NEVER falls back to the bootstrap under
//     a non-empty cursor — the emitter stamps convID, so tailing any other
//     transcript would cross-stream another conversation's output (the
//     confidentiality property this ticket protects).
//
// A fresh JSONL resolver closure is built per call so its cold/warm offset state
// is per-subscription: a brand-new bound session cold-starts at offset 0; a
// switch-back to a live session warm-tails from EOF.
func resolveTarget(active *activeConversation, boundHost boundHostFunc, bootstrap turnbridge.SessionHost, dir string) turnbridge.TargetResolver {
	return func(ctx context.Context) (turnbridge.Target, error) {
		convID, switchCh := active.watch()
		if convID == "" {
			return turnbridge.Target{
				Host:    bootstrap,
				Resolve: resolveLatestSessionJSONL(dir),
				Switch:  switchCh,
			}, nil
		}
		host, sessionID, convDir, ok := boundHost(convID)
		if !ok {
			return turnbridge.Target{}, fmt.Errorf("no bound session for active conversation %q", convID)
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
// the first look is a warm resume / switch-back → offset = size (tail from EOF,
// never replay the conversation's history to the internet-exposed phone).
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
		// Path-safety guard (defense-in-depth): sessionID is already a
		// server-minted UUID from the trusted registry/pool, but validate the
		// stem before the Join so a malformed id can never escape dir. A clean
		// UUID stem contains no '/' or '.', so filepath.Join(dir, stem+ext)
		// cannot traverse out.
		if !jsonlStemPattern.MatchString(sessionID) {
			return "", 0, fmt.Errorf("invalid bound session id %q", sessionID)
		}
		path := filepath.Join(dir, sessionID+jsonlStreamExt)
		info, err := os.Stat(path)
		if err != nil {
			if !resolvedOnce {
				sawEmpty = true
			}
			// Wrap with the path (a path/errno, never file bytes).
			return "", 0, fmt.Errorf("stat bound session jsonl %s: %w", path, err)
		}
		off := info.Size()
		if !resolvedOnce && sawEmpty {
			// Cold start: this fresh file appeared only after an earlier absent
			// look, so the whole file is the current turn — tail from 0.
			off = 0
		}
		resolvedOnce = true
		return path, off, nil
	}
}

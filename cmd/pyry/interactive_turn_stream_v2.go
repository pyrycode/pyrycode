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
// #632 capability-gated emitter so the supervised session's turn events flow to
// interactive phones, and keep flowing across a /clear-rotated JSONL on the next
// (re)subscription. It constructs the emitter over the v2 manager, builds the
// producer with NewSessionSubscriber (over Supervisor.Session()) + a
// rotation-following JSONL resolver + an OnEvent callback bridging each event to
// emitter.Handle, starts the producer goroutine, and returns a cleanup that
// blocks until that goroutine exits.
//
// The OnEvent closure captures ctx (the relay lifecycle ctx) — the ctx-less
// OnEvent -> ctx-ful Handle seam #632 named. It runs ONLY on the producer's
// single Run goroutine (drain invokes OnEvent serially), so the emitter's
// unguarded counters never race — #632's named single-Run-goroutine assumption
// holds by construction.
func startInteractiveTurnStreamV2(
	ctx context.Context,
	sup *supervisor.Supervisor,
	mgr *relay.V2SessionManager,
	claudeSessionsDir string,
	logger *slog.Logger,
) func() {
	emitter := newInteractiveTurnEmitterV2(sup, mgr, logger)
	// Publish the emitter's event ring + the conversation cursor to the manager
	// so a phone reconnecting with hello.last_event_id can be replayed the
	// missed tail (#647). Late-bound here (not a V2SessionConfig field) to break
	// the emitter↔manager construction cycle: the ring is created inside the
	// emitter constructor above, after NewV2SessionManager already ran. This is
	// the only call site; when the stream is disabled the manager keeps a nil
	// replay source and a reconnecting phone simply gets the live stream.
	mgr.SetReplaySource(emitter.ring, sup.CurrentConversation)
	// Session.Events requires a Tracker; zero opts -> package defaults. The
	// tracker's stall_detected marker now maps through to a stall envelope
	// (the mapper no longer discards it).
	tr := tuidriver.NewTracker(tuidriver.TrackerOpts{})
	resolve := resolveLatestSessionJSONL(claudeSessionsDir)
	sub := turnbridge.NewSessionSubscriber(sup, resolve, tr, logger)

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

// resolveLatestSessionJSONL returns the resolve closure for
// turnbridge.NewSessionSubscriber. Each call scans dir for <uuid>.jsonl files
// and returns the most-recently-modified one plus a startOffset. Because
// NewSessionSubscriber calls resolve fresh on every (re)subscription, returning
// the newest file each time is what makes a live /clear rotation pick up the new
// JSONL — this rotation-following is load-bearing for the /clear AC, not
// incidental.
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
// returned closure, which NewSessionSubscriber invokes from the single
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

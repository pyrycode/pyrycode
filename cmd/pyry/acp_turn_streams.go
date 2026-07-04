package main

import (
	"context"
	"log/slog"
	"sync"

	"github.com/pyrycode/pyrycode/internal/acp"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/turnbridge"
	"github.com/pyrycode/tui-driver/pkg/tuidriver"
)

// acpTurnStreams owns one turnbridge.Producer goroutine per addressable ACP
// session, each driving the acpTurnStream sink (#750) for exactly ONE fixed
// session id. One instance per `pyry acp` process. It is the composition-root
// wiring that gives acpTurnStream its first non-test caller: without it, no
// turnbridge.Producer tails a session's transcript, so no session/update ever
// reaches the host.
//
// Far thinner than the mobile leg's startInteractiveTurnStreamV2: ACP has one
// host, one fixed session per stream, no capability fan-out, no replay ring, and
// no follow-active cursor. Each stream serves a single session id via a
// resolveBoundSessionJSONL resolver with a nil Switch (never re-keyed).
//
// dir == "" disables streaming (no $HOME / unresolvable claude sessions dir);
// start becomes a no-op so the process serves without live streaming rather than
// failing.
//
// SECURITY (preservation, not a new surface): the wiring never logs application
// content. Every log site here carries only the content-free event kind, the
// session id, and an error sentinel — never JSONL bytes or model output. The sink
// (#750) enforces the same posture on the notification path.
type acpTurnStreams struct {
	ctx  context.Context // runCtx; parent of every producer goroutine
	pool *sessions.Pool  // Lookup(id) -> Session -> Supervisor (the SessionHost)
	dir  string          // claude <id>.jsonl directory; "" => streaming disabled
	// onTurnEnd (#751) resolves the held session/prompt call for a session when its
	// turn ends: start binds it to the fixed session id and passes the bound
	// closure to the sink's own onTurnEnd seam. nil-tolerant — a nil manager seam
	// leaves the sink's TurnEnd branch a debug no-op (#796's pure-emit tests, and
	// the dir == "" path, pass nil through). Wired to promptHolds.end at the
	// composition root.
	onTurnEnd func(sessionID, reason string)
	logger    *slog.Logger
	transport *acp.Transport      // set by attach before Serve accepts frames
	mu        sync.Mutex          // guards started
	started   map[string]struct{} // idempotency: at most one producer per session id
	wg        sync.WaitGroup      // joins every producer goroutine
}

// newACPTurnStreams builds the per-process manager. ctx is the composition
// root's runCtx: cancelling it stops every producer, and wait joins them.
// onTurnEnd resolves a session's held session/prompt call on turn end (#751);
// nil disables resolution (the sink logs the TurnEnd as a debug no-op).
func newACPTurnStreams(ctx context.Context, pool *sessions.Pool, dir string, onTurnEnd func(sessionID, reason string), logger *slog.Logger) *acpTurnStreams {
	return &acpTurnStreams{
		ctx:       ctx,
		pool:      pool,
		dir:       dir,
		onTurnEnd: onTurnEnd,
		logger:    logger,
		started:   make(map[string]struct{}),
	}
}

// attach stores the transport the producers notify on. Called as the first line
// of the register closure, which the acp "register before Serve" invariant runs
// before any handler dispatch — so transport is set-once and happens-before every
// start, needing no lock.
func (m *acpTurnStreams) attach(t *acp.Transport) {
	m.transport = t
}

// start spawns exactly one producer goroutine tailing id's transcript and
// feeding the acpTurnStream sink, unless dir is empty (streaming disabled) or a
// producer for id is already running (idempotent — session/load may be called
// repeatedly for the same id, and session/new+session/load of one id must not
// double-start). It runs on the read-loop goroutine (session/new, session/load),
// which dispatches serially inside Serve; the mu guard is belt-and-suspenders
// against a future off-loop caller.
func (m *acpTurnStreams) start(id sessions.SessionID) {
	if m.dir == "" {
		return
	}
	m.mu.Lock()
	if _, ok := m.started[string(id)]; ok {
		m.mu.Unlock()
		return
	}
	m.started[string(id)] = struct{}{}
	m.mu.Unlock()

	sess, err := m.pool.Lookup(id)
	if err != nil {
		// Unreachable on the create/activate-success path (the caller looked the
		// id up moments ago); defensive only. Content-free: id + error sentinel.
		m.logger.Debug("acp: turn stream lookup failed",
			"event", "acp_turn.lookup_err",
			"session_id", string(id),
			"err", err.Error())
		m.unmark(id)
		return
	}
	host := sess.Supervisor()

	// The fixed-target resolver: one session, no re-key. resolveBoundSessionJSONL
	// is reused verbatim from the mobile leg and built FRESH inside the closure so
	// its per-subscription cold/warm offset state resets on each re-subscription
	// (the #671 rule, per bound session). Switch is nil: ACP never re-keys a stream
	// onto a different session, so session-end and parent-ctx cancel are the only
	// teardown triggers.
	dir, sessionID := m.dir, string(id)
	resolve := func(ctx context.Context) (turnbridge.Target, error) {
		return turnbridge.Target{
			Host:    host,
			Resolve: resolveBoundSessionJSONL(dir, sessionID),
			Switch:  nil,
		}, nil
	}
	// A fresh Tracker per session (not shared) mirrors the mobile path's
	// one-tracker-per-stream and avoids cross-session parse-state races.
	sub := turnbridge.NewTargetSubscriber(resolve, tuidriver.NewTracker(tuidriver.TrackerOpts{}), m.logger)
	// Bind the manager's onTurnEnd seam to this fixed session id so the sink
	// resolves the held session/prompt call with the mapped stopReason on TurnEnd
	// (#751). Preserve nil-tolerance: a nil manager seam passes nil through, keeping
	// the sink's debug-no-op TurnEnd branch for #796's pure-emit tests and the
	// dir == "" path.
	var onEnd func(string)
	if m.onTurnEnd != nil {
		onEnd = func(reason string) { m.onTurnEnd(sessionID, reason) }
	}
	sink := newACPTurnStream(m.transport, sessionID, onEnd, m.logger)
	prod, err := turnbridge.New(turnbridge.Config{
		Subscribe: sub,
		OnEvent:   sink.Handle,
		Logger:    m.logger,
	})
	if err != nil {
		// Unreachable: New errors only on a nil Subscribe. Fail soft — skip this
		// session's stream rather than aborting serveACPWithPool.
		m.logger.Warn("acp: turn stream disabled; producer build failed",
			"event", "acp_turn.build_err",
			"session_id", sessionID,
			"err", err.Error())
		m.unmark(id)
		return
	}

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		// Run returns only ctx.Err() per its contract → debug-log and exit.
		if err := prod.Run(m.ctx); err != nil {
			m.logger.Debug("acp: turn stream run returned",
				"event", "acp_turn.run_returned",
				"session_id", sessionID,
				"err", err.Error())
		}
	}()
}

// unmark drops id from started so a later start may retry after a soft failure
// (Lookup miss / build error). Both callers are on the unreachable defensive
// paths; keeping start retryable is the honest invariant.
func (m *acpTurnStreams) unmark(id sessions.SessionID) {
	m.mu.Lock()
	delete(m.started, string(id))
	m.mu.Unlock()
}

// wait joins every producer goroutine. Call after m.ctx is cancelled, so each
// producer has already observed cancellation and is returning.
func (m *acpTurnStreams) wait() {
	m.wg.Wait()
}

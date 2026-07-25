package main

import (
	"context"
	"log/slog"
	"sync"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// turnBusyTracker holds the set of conversations that currently have an open turn
// on the stream-json path, so the inbound-delivery path can ask "is a turn running
// for conversation X?" from any goroutine. It is self-synchronised — all methods
// are safe for concurrent use — and it is fed from the drain's fan-in
// (startStreamTurnDrainV2), BEFORE that drain's active-session gate: the emitter's
// own lifecycle state cannot answer this question, being unguarded, scalar rather
// than per-conversation, and populated only for the conversation the cursor points
// at (interactive_turn_v2.go:81-86, :141).
//
// It stores membership only: a conversation key and the fact that it is mid-turn.
// Never the event, its content, a turn id, a timestamp, or a count. Absent key ≡
// idle ≡ unknown ≡ unbound ≡ never seen, all through one map lookup, which is what
// keeps Busy from becoming a "does conversation X exist" oracle (#1101, the posture
// screenSnapshotterOrNil records at relay.go:395-410). The map is bounded by the
// conversations currently mid-turn, not by every conversation ever seen.
//
// KNOWN GAP — the clear is event-driven only. A turn is closed here solely by its
// TurnEnd arriving on the fan-in. Two paths reach a permanently-busy conversation
// without any TurnEnd ever arriving, and each is its own slice:
//
//   - a session torn down under the conversation (/clear rotation, idle/cap
//     eviction) — #1202. That slice also owns the narrower rotation edge: because
//     conversationForSession matches SessionHistory, a retired session's late
//     TurnEnd can clear a turn its successor opened (fails open, reports idle when
//     busy — the same direction as a daemon restart, which starts all-idle).
//   - a child that dies mid-turn and is respawned, which fires no pool transition
//     and emits no result line for the abandoned turn — #1203.
//
// Both must land before any consumer reads this signal. While the tracker is
// unwired a wedge is harmless; once consulted it becomes a conversation that can
// never be delivered to again.
//
// SECURITY: content-free. The only fields ever logged are the event discriminant
// (eventKind) and the producing session id, matching the drain's existing drop
// diagnostics (stream_turn_drain.go:74-79, :129-133). The key is never taken from
// the wire or the stream bytes — it is resolved daemon-side from the registry
// against the runner's construction-time session tag, so a hostile or confused
// child can only ever mark its OWN conversation busy.
type turnBusyTracker struct {
	// resolve maps a producing session id to the conversation that owns it.
	// Injected (conversationForSession(w.convReg, sid) in production) so this file
	// never imports internal/conversations — the same purity discipline
	// session_transition_v2.go keeps.
	resolve func(sessionID string) (conversationID string, ok bool)
	logger  *slog.Logger

	mu   sync.Mutex
	busy map[string]struct{}
	// changed is a generation channel: never sent on, closed and replaced under mu
	// whenever set membership actually changes. One channel serves every waiter —
	// each re-checks its own key after a wakeup, so a spurious wakeup costs a map
	// lookup and nothing else.
	changed chan struct{}
}

// newTurnBusyTracker constructs the tracker. It panics if resolve is nil — a
// programmer error the type cannot function without, matching eventring.New's
// panic-on-misconfig (ring.go:78-85). A nil logger falls back to slog.Default,
// mirroring newStreamTurnSink.
func newTurnBusyTracker(resolve func(sessionID string) (conversationID string, ok bool), logger *slog.Logger) *turnBusyTracker {
	if resolve == nil {
		panic("turnBusyTracker: resolve must not be nil")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &turnBusyTracker{
		resolve: resolve,
		logger:  logger,
		busy:    make(map[string]struct{}),
		changed: make(chan struct{}),
	}
}

// observe feeds one fan-in envelope into the tracker. It is called only from the
// drain goroutine, so it inherits that goroutine's single-writer invariant
// (stream_turn_drain.go:103-106) — but the type is self-synchronised regardless,
// since its readers run anywhere.
//
// The opener set is a WHITELIST, not "anything that is not a TurnEnd". The
// evidence is the producer's growth path rather than a stray event: streamsup's
// parser tolerates-and-drops rate_limit_event today (parser.go:158-163), and that
// is exactly the line that becomes an ApiRetry the day someone wires it — a
// blacklist would wedge a conversation on it. TurnEnd on a conversation that is
// not busy is a plain no-op delete.
//
// A nil receiver is a no-op, so a caller with no tracker (the drain's own tests)
// needs no construction. The drain's parameter is deliberately the concrete
// *turnBusyTracker and not an interface: a typed-nil pointer in an interface is
// non-nil at the interface level and would route straight past this guard into a
// nil-map read — the same hazard screenSnapshotterOrNil exists to dodge.
func (t *turnBusyTracker) observe(sessionID string, ev turnevent.Event) {
	if t == nil {
		return
	}

	var opens bool
	switch ev.(type) {
	case turnevent.ThoughtChunk, turnevent.TextChunk, turnevent.ToolStart, turnevent.ToolUpdate:
		opens = true
	case turnevent.TurnEnd:
		// Both stop reasons close the turn; resultTurnEndReason (parser.go:173)
		// only picks the reason field, so there is one code path upstream too.
		opens = false
	default:
		// Stall / ApiRetry / Compacting — tui-driver status peers with no turn
		// lifecycle meaning (the emitter treats them the same way,
		// interactive_turn_v2.go:218-239) — and any future variant.
		return
	}

	// Resolved OUTSIDE the lock: resolve scans the conversations registry and takes
	// that registry's mutex, so calling it under t.mu would establish a
	// tracker.mu -> convReg.mu order for no benefit. The scan is O(conversations)
	// per event, which is comfortably within budget at the post-#609 arrival rate
	// (~one per JSONL message / ~250ms, stream_turn_drain.go:10-16) over a
	// human-scale conversation set. A future slice that raises that rate by an
	// order of magnitude wants a by-session-id read method on the registry, not a
	// cache here.
	convID, ok := t.resolve(sessionID)
	if !ok || convID == "" {
		// An unbound producing session — the bootstrap session before any binding,
		// or one whose conversation was removed. Tracking it under an empty key
		// would both wedge that key and collide with the unknown-conversation
		// answer, so it is simply not tracked.
		//
		// SECURITY: content-free — discriminant + session id only.
		t.logger.Debug("relay: stream-turn busy skip; session resolves to no conversation",
			"event", "stream_turn.busy_unresolved",
			"kind", eventKind(ev),
			"session_id", sessionID)
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	_, was := t.busy[convID]
	if opens == was {
		return // membership unchanged: no mutation, and no broadcast
	}
	if opens {
		t.busy[convID] = struct{}{}
	} else {
		delete(t.busy, convID)
	}

	// Close-and-replace under the same lock acquisition as the mutation: that is
	// what makes WaitIdle's check-and-subscribe atomic. close never blocks, so
	// holding mu across it is safe.
	close(t.changed)
	t.changed = make(chan struct{})
}

// Busy reports whether conversationID currently has an open turn. Unknown,
// unbound, never-seen and empty conversation ids all report false through the
// identical map lookup — the signature is the existence-oracle enforcement, not a
// runtime branch, so a later widening to (bool, error) would reintroduce the
// oracle silently.
func (t *turnBusyTracker) Busy(conversationID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, busy := t.busy[conversationID]
	return busy
}

// WaitIdle blocks until conversationID has no open turn and returns nil, or
// returns ctx.Err() if ctx is cancelled first. It returns nil immediately when the
// conversation is already idle — including when it is unknown or unbound, for the
// same reason Busy reports false there.
//
// The membership check and the generation-channel capture happen under ONE lock
// acquisition. Splitting them reintroduces a lost wakeup: a transition landing in
// the gap would close a channel the waiter has not captured yet, leaving it asleep
// on the replacement.
func (t *turnBusyTracker) WaitIdle(ctx context.Context, conversationID string) error {
	for {
		t.mu.Lock()
		_, busy := t.busy[conversationID]
		changed := t.changed
		t.mu.Unlock()

		if !busy {
			return nil
		}

		select {
		case <-changed:
			// Membership moved somewhere; re-check this conversation's own key.
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

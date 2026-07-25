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
// KNOWN GAP — ONE path remains open. A turn is closed here by two feeds: its
// TurnEnd arriving on the fan-in (observe), and a pool teardown transition — a
// /clear rotation or an idle/cap eviction — reaching clearForSession (#1202).
// What is still missing is a child that dies mid-turn and is respawned: it fires
// no pool transition and emits no result line for the abandoned turn — #1203.
//
// #1203 must land before any consumer reads this signal. While the tracker is
// unwired a wedge is harmless; once consulted it becomes a conversation that can
// never be delivered to again.
//
// The narrower rotation edge #1202 was expected to own is UNREACHABLE, and is
// recorded here rather than defended with a guard. It would need two distinct
// producer tags resolving to one conversation at the same time: a /clear re-keys
// ONE pool entry in place, and the Parser's sink tag is fixed at runner
// construction (streamsup_runner.go:105) while RestartFresh rotates only the
// runner's internal spawn id — so every id reachable through
// conversationForSession's SessionHistory match belongs to the SAME runner that
// continues under the successor id, tagging its events identically either way.
// Eviction cannot supply a second producer either: being binding-neutral, an
// evicted id never enters SessionHistory (conversations/registry.go:241 is its
// only production writer, reached solely from sessions/transition.go:59).
//
// SECURITY: content-free. The only fields ever logged are the event discriminant
// (eventKind) and the producing session id, matching the drain's existing drop
// diagnostics (stream_turn_drain.go:74-79, :143-149). The key is never taken from
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

	t.setBusy(convID, opens)
}

// clearForSession closes any open turn on the conversation that owns sessionID.
// Those are exactly the turns whose TurnEnd never arrives, so observe alone would
// leave the conversation busy forever. It has TWO callers:
//
//   - the teardown feed (#1202), driven from the pool's TransitionObserver on a
//     /clear rotation or an idle/cap eviction (session_transition_v2.go:274-281);
//   - the drain's exit arm (#1209), reached when a child-exit signal rides the
//     fan-in ahead of the tracker feed (stream_turn_drain.go). That lane exists
//     but nothing in production fires it until #1210 supplies a producer.
//
// A nil receiver is a no-op, mirroring observe. This is not defensive padding:
// the transition observer is wired UNCONDITIONALLY (relay.go) while the tracker
// is constructed only on the stream path, so in the daemon's default PTY mode
// this method IS called on a nil tracker at the first /clear or eviction, on the
// pool's own lifecycle goroutine. Busy and WaitIdle carry no such guard — they
// take t.mu immediately — which is why the wiring hands the nil to THIS method
// and to no other. The caller's parameter is likewise the concrete
// *turnBusyTracker and never an interface, for the reason observe documents.
//
// It runs SYNCHRONOUSLY on its caller's goroutine — the one that fired the
// transition, or the drain goroutine for the exit arm — and must not block on
// either. #659's observer contract requires it of the first; for the second the
// requirement is the drain's own, since a stalled clear would wedge the whole
// fan-in, the coalescing flush timer included. That holds: the work is one resolve
// (a slice-header copy under the conversations registry's mutex — Save releases
// that mutex BEFORE any file I/O, conversations/registry.go:79-90), one map delete
// and one close, all bounded with no channel receive, no I/O and no callback out.
// The transition caller already pays a full atomic write including fsync one line
// earlier on the /clear path (sessions/transition.go:57-64 → rebindConversation →
// Save), so a leaf-mutex membership delete is orders of magnitude cheaper than
// what it has already spent before the observer is even called.
//
// On the drain goroutine this feed is additionally serialised against observe by
// CONSTRUCTION — same single reader, one envelope at a time — rather than by t.mu.
// The two callers still run concurrently with each other, which is what setBusy's
// single lock acquisition covers; both clears are idempotent and same-direction,
// so no interleaving of them can produce a spurious open.
//
// The key is a SESSION id, never a conversation id. The only production producer
// of the value is internal/sessions' own record of a lifecycle event it
// performed, and the conversation is then resolved daemon-side by the same
// closure observe uses — which is what keeps the SECURITY note above ("the key is
// never taken from the wire") true for this feed as well. A clearConversation
// variant would be a shorter call chain and would quietly retire that invariant.
//
// Idempotent: an already-idle conversation is neither mutated nor re-broadcast.
func (t *turnBusyTracker) clearForSession(sessionID string) {
	if t == nil {
		return
	}

	// Resolved OUTSIDE t.mu — the identical lock-order reason observe documents.
	convID, ok := t.resolve(sessionID)
	if !ok || convID == "" {
		// Expected rather than exceptional: a bootstrap session evicted before any
		// binding, or an evicted id whose conversation has since re-bound elsewhere.
		// Skipping is the fail-closed answer; a wildcard or empty-key clear would
		// report a LIVE turn on some other conversation as idle.
		//
		// SECURITY: content-free, and session_id ONLY. The resolved conversation_id
		// is deliberately withheld — it is a routing key treated as sensitive
		// alongside session ids and workspace_cwd (session_transition_v2.go:38-47):
		// resolved daemon-side, stamped on the wire, never logged.
		t.logger.Debug("relay: stream-turn clear skip; session resolves to no conversation",
			"event", "stream_turn.clear_unresolved",
			"session_id", sessionID)
		return
	}

	t.setBusy(convID, false)
}

// setBusy applies one membership change for conversationID under a SINGLE t.mu
// acquisition, broadcasting on t.changed only when the set actually moved.
//
// Shared by both feeds rather than hand-duplicated in each: the close-and-replace
// protocol below is the invariant WaitIdle's check-and-subscribe atomicity
// depends on, and a second, independently-written copy of it is precisely the
// lost-wakeup bug this extraction forecloses.
func (t *turnBusyTracker) setBusy(conversationID string, open bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	_, was := t.busy[conversationID]
	if open == was {
		return // membership unchanged: no mutation, and no broadcast
	}
	if open {
		t.busy[conversationID] = struct{}{}
	} else {
		delete(t.busy, conversationID)
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

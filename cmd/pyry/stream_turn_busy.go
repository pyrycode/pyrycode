package main

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// turnBusyTracker holds the set of conversations that currently have an open turn
// on the stream-json path, so the inbound-delivery path can ask "is a turn running
// for conversation X?" from any goroutine. It is self-synchronised — all methods
// are safe for concurrent use — and it is fed from the drain's fan-in
// (startStreamTurnDrainV2), BEFORE that drain's active-session gate: the emitter's
// own lifecycle state cannot answer this question, being unguarded, scalar rather
// than per-conversation, and populated only for the conversation the cursor
// points at (interactiveTurnEmitterV2's lifecycle fields, filled in Handle).
//
// That inbound-delivery consumer now EXISTS: newInboundDeliver (main.go) waits on
// waitIdleForDelivery and then marks with openForDelivery, both between Activate
// and the write, so a message sent mid-turn parks in the msgqueue backlog for the
// duration of the running turn instead of racing it into the child's stdin pipe
// (#1199). That is what makes the queued-backlog UI and the drop-before-drain
// control work on the stream path, and it is why the signal is a guarantee rather
// than a hint — see waitIdleForDelivery for the ordering argument.
//
// It stores membership only: a conversation key and the fact that it is mid-turn.
// Never the event, its content, a turn id, a timestamp, or a count. Absent key ≡
// idle ≡ unknown ≡ unbound ≡ never seen, all through one map lookup, which is what
// keeps Busy from becoming a "does conversation X exist" oracle (#1101, the
// posture screenSnapshotterOrNil records). The map is bounded by the
// conversations currently mid-turn, not by every conversation ever seen.
//
// A SECOND membership set sits beside it, and it is membership too: inflight
// holds the tool calls that have not finished, keyed by conversation and then by
// claude's tool_use_id — never the tool's name, its input, or a timestamp
// (#1917). ToolCallInFlight collapses unknown, never-seen and empty values of
// EITHER key to one false through the identical pair of lookups, for the reason
// the paragraph above gives, doubled because the pair could otherwise oracle
// either id. Its own bound is tighter: an outer key exists only while its inner
// set is non-empty, and the whole entry dies with the turn at every close feed
// below.
//
// THREE FEEDS close a turn here, and between them no reachable sequence leaves a
// conversation reported busy forever: its TurnEnd arriving on the fan-in
// (observe); a pool teardown transition — a /clear rotation or an idle/cap
// eviction — reaching clearForSession (#1202); and a child that dies mid-turn,
// which fires no pool transition and emits no result line for the abandoned turn,
// reaching clearForSession through the drain's exit arm (the #1209 lane, fired in
// production by the producer newStreamRunnerFactory installs, #1210).
//
// ONE FEED BESIDES observe OPENS one: openForDelivery, called by the delivery seam
// (#1199) inside the same statement sequence that immediately performs the write.
// It does not weaken the analysis above, feed by feed. An open is placed only
// immediately before a write; a failed write is undone in that same sequence; and a
// successful write starts a turn that closes through one of the three feeds already
// listed — its TurnEnd, a pool teardown, or the child dying. The one residual is a
// child that is alive, has consumed the envelope, and whose turn simply never ends.
// That is a LEGITIMATELY RUNNING turn, not the wedge class below: it is bounded by
// streamTurnHoldTimeout on each delivery attempt and by msgqueue's give-up across
// them, surfacing as a typed session_error rather than being defended with a guard
// here (#1199 AC3).
//
// That third feed was this file's KNOWN GAP, and closing it was the stated
// precondition for any consumer reading this signal: while the tracker was unwired
// a wedge was harmless, but once consulted it becomes a conversation that can never
// be delivered to again. The precondition is now SATISFIED — recorded rather than
// deleted, because that hazard is what shaped the design.
//
// The narrower rotation edge #1202 was expected to own is UNREACHABLE, and is
// recorded here rather than defended with a guard. It would need two distinct
// producer tags resolving to one conversation at the same time: a /clear re-keys
// ONE pool entry in place, and the Parser's sink tag is fixed at runner
// construction (`newStreamRunnerFactory`) while RestartFresh rotates only the
// runner's internal spawn id — so every id reachable through
// conversationForSession's SessionHistory match belongs to the SAME runner that
// continues under the successor id, tagging its events identically either way.
// Eviction cannot supply a second producer either: being binding-neutral, an
// evicted id never enters SessionHistory (`RebindSession` is its
// only production writer, reached solely from sessions/`notifyTransition`).
//
// SECURITY: content-free. The only fields ever logged are the event discriminant
// (eventKind) and the producing session id, matching the drain's existing drop
// diagnostics in `sinkFor` and `exitFor`.
//
// For the two SESSION-keyed feeds — observe and clearForSession — the key is never
// taken from the wire or the stream bytes: it is resolved daemon-side from the
// registry against the runner's construction-time session tag, so a hostile or
// confused child can only ever mark its OWN conversation busy. openForDelivery is
// the one feed whose key does arrive in a send_message payload, and the honest
// statement for it is narrower rather than the same: that conversation id has
// already passed two independent daemon-side gates before it can reach the mark —
// router.Route at enqueue (registry hit → non-empty CurrentSessionID → pool.Lookup
// hit, rejected synchronously otherwise; the routing target is read from the
// server-stored registry row, never phone-writable, send_message.go) and
// sessionRouter.resolve again as the first statement of the delivery seam
// (main.go). An unknown, unbound or forged id returns before the mark, so the mark
// only ever names the conversation the daemon is about to write to — one the caller
// was already authorized to write to, which is why inserting that key reveals
// nothing: no read is added, and Busy still collapses unknown / unbound / idle to
// one answer through one map lookup.
type turnBusyTracker struct {
	// resolve maps a producing session id to the conversation that owns it.
	// Injected (conversationForSession(w.convReg, sid) in production) so this file
	// never imports internal/conversations — the same purity discipline
	// session_transition_v2.go keeps.
	resolve func(sessionID string) (conversationID string, ok bool)
	logger  *slog.Logger

	mu   sync.Mutex
	busy map[string]struct{}
	// inflight holds the tool calls currently in flight, keyed by conversation and
	// then by claude's tool_use_id. NESTED rather than a flat
	// map[toolCallID]conversationID, and that is a SECURITY property rather than a
	// shape preference: tool-call ids are minted by each child, so a hostile or
	// confused child on conversation C can emit a tool_use block reusing an id
	// genuinely in flight on A. Flat, that capture would OVERWRITE A's entry and
	// flip A's answer to false — C changing another conversation's answer. Nested,
	// C's fabrication lands under C's own key and the worst it achieves is a false
	// positive about itself, which is exactly the bound the SECURITY paragraph
	// above already claims. Sweeping is O(1) either way here — one delete of the
	// outer key rather than a scan.
	inflight map[string]map[string]struct{}
	// changed is a generation channel: never sent on, closed and replaced under mu
	// whenever set membership actually changes. One channel serves every waiter —
	// each re-checks its own key after a wakeup, so a spurious wakeup costs a map
	// lookup and nothing else.
	changed chan struct{}
}

// newTurnBusyTracker constructs the tracker. It panics if resolve is nil — a
// programmer error the type cannot function without, matching eventring.New's
// panic-on-misconfig (`New` in ring.go). A nil logger falls back to slog.Default,
// mirroring newStreamTurnSink.
func newTurnBusyTracker(resolve func(sessionID string) (conversationID string, ok bool), logger *slog.Logger) *turnBusyTracker {
	if resolve == nil {
		panic("turnBusyTracker: resolve must not be nil")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &turnBusyTracker{
		resolve:  resolve,
		logger:   logger,
		busy:     make(map[string]struct{}),
		inflight: make(map[string]map[string]struct{}),
		changed:  make(chan struct{}),
	}
}

// turnMark is one fan-in event's effect on this tracker's per-conversation mark.
// It is the SOLE definition of that split, read by two callers with opposite
// needs: observe, which applies the mark, and `sinkFor`, which refuses to drop a
// closing-class envelope on a saturated fan-in (#1496).
//
// One classifier rather than two agreeing type switches, because the agreement is
// load-bearing. A variant added to observe's closer arm but not to the sink's
// never-drop set reintroduces #1496's wedge silently — a turn-closing event that
// the fan-in is free to discard, leaving the mark open with nothing left that
// could ever clear it. Sharing the switch makes that agreement structural instead
// of a convention two files have to keep.
type turnMark uint8

const (
	// turnMarkNone is an event with no turn-lifecycle meaning.
	turnMarkNone turnMark = iota
	// turnMarkOpen opens the conversation's mark.
	turnMarkOpen
	// turnMarkClose closes it, and is never dropped at the fan-in.
	turnMarkClose
)

// turnMarkFor classifies one event. Pure: it switches on the Go variant type
// only, never on a field value, so no content claude produced can steer the
// answer.
//
// The opener set is a WHITELIST, not "anything that is not a TurnEnd". The
// evidence was the producer's growth path rather than a stray event: streamsup's
// parser tolerated-and-dropped rate_limit_event, and that was exactly the line
// that would grow an event of its own the day someone wired it — a blacklist would
// have wedged a conversation on it. TurnEnd on a conversation that is not busy is
// a plain no-op delete.
//
// DISCHARGED 2026-08-09 (#1404): that day came, and the whitelist held with no
// code change here. The line now maps to turnevent.RateLimited — not the ApiRetry
// this comment used to predict, which is the second reason a blacklist would have
// been wrong — and the variant lands in the default arm below, a no-op for this
// tracker. That is the CORRECT answer rather than an omission: a usage limit is
// orthogonal to turn lifecycle, so opening a turn on one would wedge the
// conversation exactly as opening one on an Unrecognized would. streamsup's
// ignoredLineTypes — which this comment once cited by line, at a number that had
// drifted onto an unrelated declaration — is down to `system` alone.
//
// The whitelist direction is what makes the classifier safe for its SECOND caller
// too. An unknown variant falls to turnMarkNone, so the sink treats it as
// droppable: a future variant wrongly left out of the reserve costs capacity,
// while the wedge only ever comes from a CLOSER misclassified as droppable — and
// closers are the enumerated arm, not the fall-through.
func turnMarkFor(ev turnevent.Event) turnMark {
	switch ev.(type) {
	case turnevent.ThoughtChunk, turnevent.TextChunk, turnevent.ToolStart, turnevent.ToolUpdate:
		return turnMarkOpen
	case turnevent.TurnEnd:
		// Both stop reasons close the turn; resultTurnEndReason (`maxTaskRosterDescription`)
		// only picks the reason field, so there is one code path upstream too.
		return turnMarkClose
	default:
		// Stall / ApiRetry / Compacting — tui-driver status peers with no turn
		// lifecycle meaning (the emitter's `Handle` treats them the same way) —
		// plus Unrecognized, and any future variant.
		//
		// The opener set above is a whitelist, so Unrecognized needs no code
		// change to land here, and landing here is the CORRECT answer rather than
		// an omission: we do not know what the message is, so it must neither open
		// nor close a turn. Opening one would wedge the conversation, since no turn
		// end follows a message we could not understand.
		return turnMarkNone
	}
}

// toolCallDelta is one event's effect on the in-flight set. The zero value
// carries no delta, which is what every non-tool variant produces.
type toolCallDelta struct {
	id      string // empty ⇒ no delta
	started bool   // true: the call went in flight; false: it finished
}

// toolCallDeltaFor classifies one event. Pure, and switches on the Go variant
// type only — the sole field it reads is the id itself, never a discriminant.
// That is turnMarkFor's discipline, kept for the same reason: no content claude
// produced may steer the answer.
//
// A ToolUpdate always DROPS, and its Status is deliberately not read. The parser
// emits ToolUpdate from exactly one site, emitUser, out of a tool_result block,
// and toolStatus maps is_error onto completed/failed only — never pending, never
// in progress — so a ToolUpdate in production is always terminal.
// turnevent.ToolStatusInProgress has no production producer at all. Reading
// Status would therefore buy nothing and would fail OPEN: a fabricated
// in-progress tool_result would pin a finished call in flight instead of
// dropping the child's own call early.
//
// NOT folded into turnMarkFor, which has a second caller — sinkFor, whose
// never-drop reserve reads the same value. Widening its return would couple the
// fan-in's drop policy to tool-call retention for no reason. Two small pure
// classifiers, one concern each.
func toolCallDeltaFor(ev turnevent.Event) toolCallDelta {
	switch e := ev.(type) {
	case turnevent.ToolStart:
		return toolCallDelta{id: e.ToolCallID, started: true}
	case turnevent.ToolUpdate:
		return toolCallDelta{id: e.ToolCallID}
	default:
		// Every other variant, PermissionRequest included — it carries a
		// ToolCallID but is a question about a call, not a change to whether one
		// is running, and it never reaches this fan-in anyway.
		return toolCallDelta{}
	}
}

// observe feeds one fan-in envelope into the tracker. It is called only from the
// drain goroutine, so it inherits that goroutine's single-writer invariant
// (`exitFor`) — but the type is self-synchronised regardless,
// since its readers run anywhere.
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
	switch turnMarkFor(ev) {
	case turnMarkOpen:
		opens = true
	case turnMarkClose:
		opens = false
	default:
		return
	}

	// Resolved OUTSIDE the lock: resolve scans the conversations registry and takes
	// that registry's mutex, so calling it under t.mu would establish a
	// tracker.mu -> convReg.mu order for no benefit. The scan is O(conversations)
	// per event, which is comfortably within budget at the post-#609 arrival rate
	// (~one per JSONL message / ~250ms, `streamTurnSinkBuf`) over a
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

	// The tool-call delta rides the resolve and the non-empty check above rather
	// than repeating them: an unresolvable session captures nothing, so no call is
	// ever retained under an empty conversation key, and no second resolve call is
	// needed — which is what keeps resolve outside t.mu, as its own note requires.
	//
	// A turnMarkNone event returns before this, so a variant that is neither
	// opener nor closer contributes no delta even if a future one carried an id.
	// That is the correct answer: a call cannot be in flight on a conversation
	// with no turn open.
	t.setBusy(convID, opens, toolCallDeltaFor(ev))
}

// clearForSession closes any open turn on the conversation that owns sessionID.
// Those are exactly the turns whose TurnEnd never arrives, so observe alone would
// leave the conversation busy forever. It has TWO callers:
//
//   - the teardown feed (#1202), driven from the pool's TransitionObserver on a
//     /clear rotation or an idle/cap eviction (`startSessionTransitionStreamV2`);
//   - the drain's exit arm (#1209), reached when a child-exit signal rides the
//     fan-in ahead of the tracker feed (stream_turn_drain.go). That lane is fired
//     in production by the per-runner producer newStreamRunnerFactory installs
//     (streamsup_runner.go, #1210), so a child that dies mid-turn clears here.
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
// that mutex BEFORE any file I/O), one map delete
// and one close, all bounded with no channel receive, no I/O and no callback out.
// The transition caller already pays a full atomic write including fsync one line
// earlier on the /clear path (`notifyTransition` → rebindConversation →
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
		// alongside session ids and workspace_cwd (`sessionTransitionEmitterV2`):
		// resolved daemon-side, stamped on the wire, never logged.
		t.logger.Debug("relay: stream-turn clear skip; session resolves to no conversation",
			"event", "stream_turn.clear_unresolved",
			"session_id", sessionID)
		return
	}

	t.setBusy(convID, false, toolCallDelta{})
}

// setBusy applies one membership change for conversationID — AND that event's
// tool-call delta — under a SINGLE t.mu acquisition, broadcasting on t.changed
// only when the busy set actually moved, and reports whether it moved.
//
// Carrying the delta here rather than in a capture method of its own is what
// makes the two mutations atomic against each other, and the split version is a
// reachable lost update rather than a style question: observe runs on the drain
// goroutine while the teardown feed runs clearForSession on the pool's lifecycle
// or rotation-watcher goroutine, so a /clear landing between the mark and a
// separate capture would sweep an empty inflight entry and let the capture
// re-insert afterwards — a call reported in flight on a conversation whose
// session is gone, and invisible to -race because both paths hold t.mu.
//
// Shared by every feed rather than hand-duplicated in each: the close-and-replace
// protocol below is the invariant WaitIdle's check-and-subscribe atomicity
// depends on, and a second, independently-written copy of it is precisely the
// lost-wakeup bug this extraction forecloses.
//
// The changed return exists for openForDelivery's undo, and it is reported out of
// the ONE lock acquisition the mutation already takes. Deriving the same answer
// from a separate Busy read would be a TOCTOU: a competing feed's opener landing
// between the read and the mark would make the undo clear a turn this delivery
// never opened, reporting a live turn idle and letting the next message through
// unheld. The two event-driven callers discard it, which Go permits with no edit
// at their call sites.
func (t *turnBusyTracker) setBusy(conversationID string, open bool, tool toolCallDelta) (changed bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	// The tool delta is applied BEFORE the membership comparison, and that
	// ordering is a contract rather than a detail. A ToolStart mid-turn arrives on
	// an ALREADY-BUSY conversation, so open == was and the early return below
	// fires; a delta applied after it would capture nothing but the first tool
	// call of a turn.
	//
	// An empty id is no delta at all, silently — the whole reason ToolCallInFlight
	// needs no guard for an empty tool-call id. Whether the parser can even
	// produce one is left unresolved on purpose: the refusal makes the answer
	// irrelevant to correctness, and the empty answer has to be negative however
	// it arises.
	if tool.id != "" {
		if tool.started {
			calls := t.inflight[conversationID]
			if calls == nil {
				calls = make(map[string]struct{})
				t.inflight[conversationID] = calls
			}
			calls[tool.id] = struct{}{}
		} else if calls := t.inflight[conversationID]; calls != nil {
			delete(calls, tool.id)
			if len(calls) == 0 {
				// The outer key exists only while its inner set is non-empty, which
				// is what bounds the map by the conversations with a call running
				// rather than by every conversation ever seen.
				delete(t.inflight, conversationID)
			}
		}
	}

	_, was := t.busy[conversationID]
	if open == was {
		return false // membership unchanged: no mutation, and no broadcast
	}
	if open {
		t.busy[conversationID] = struct{}{}
	} else {
		delete(t.busy, conversationID)
		// The whole sweep, one statement: a closing turn takes its retained calls
		// with it, which is what every close feed rides — the turn's own TurnEnd,
		// a pool teardown through clearForSession, and the drain's exit arm. Placed
		// AFTER the delta above so a close wins over a same-call add; that pair is
		// unreachable today, since every tool-bearing variant is an opener, and the
		// ordering makes it fail closed if it ever becomes reachable.
		//
		// Reaching here at all implies the conversation was busy, and a non-empty
		// inflight entry implies busy — capture and mark happen in this one call
		// for every tool-bearing variant — so the early return above can never skip
		// a sweep that had anything to do.
		delete(t.inflight, conversationID)
	}

	// Close-and-replace under the same lock acquisition as the mutation: that is
	// what makes WaitIdle's check-and-subscribe atomic. close never blocks, so
	// holding mu across it is safe.
	close(t.changed)
	t.changed = make(chan struct{})
	return true
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

// ToolCallInFlight reports whether toolCallID is a tool call currently in flight
// on conversationID (#1917). MEMBERSHIP, not lookup: it answers "does this call
// belong to this conversation?" and never "which conversation owns this call?",
// mirroring Busy.
//
// Unknown, never-seen and empty values of EITHER parameter reach false through
// the identical two lookups — an empty conversation key is never inserted
// (observe refuses an unresolvable session) and an empty tool-call id is never
// inserted (setBusy refuses it) — so neither needs a guard, and adding one would
// be the runtime branch this posture forbids. As with Busy, THE SIGNATURE IS THE
// EXISTENCE-ORACLE ENFORCEMENT: a later widening to (bool, error), or any
// variant handing back the conversation id, would reintroduce the oracle
// silently, and here it would do so for either id.
//
// No nil-receiver guard, matching Busy and WaitIdle. The wiring hands the nil
// only to the methods PTY mode actually reaches, and this read is not one of
// them; a guard would be the first step toward a consumer silently reading false
// in PTY mode instead of failing loudly.
//
// It deliberately does NOT also gate on busy[conversationID]: that is redundant
// under the invariant that a non-empty inflight entry implies a busy one, and
// would add a lookup and a branch for no change in behaviour.
func (t *turnBusyTracker) ToolCallInFlight(conversationID, toolCallID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, inFlight := t.inflight[conversationID][toolCallID]
	return inFlight
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

// --- #1199: the delivery feed -------------------------------------------------

// waitIdleForDelivery blocks until conversationID has no open turn, bounded by
// timeout. It is the delivery seam's half of the mid-turn hold (#1199), which is
// what makes msgqueue's DeliverFunc contract — "MUST block while claude is busy …
// that blocking IS the drain's turn-end pacing" (msgqueue/queue.go) — true on the
// stream path, where the write itself returns as soon as the envelope is in the
// child's stdin pipe.
//
// A nil receiver (PTY mode, where the tracker is never constructed) and an empty
// conversation id both return nil at once, leaving the seam semantically unchanged.
// Busy and WaitIdle keep their own non-nil-safe contracts, so the wiring hands the
// nil to this method, to openForDelivery and to clearForSession, and to no other.
// Refusing the empty key preserves the absent ≡ idle ≡ unknown ≡ unbound collapse
// the type is built on.
//
// It returns nil once the conversation is idle — the ordinary case, and the point
// at which the seam writes; context.DeadlineExceeded when timeout elapses with the
// turn still open, having written NOTHING, so the retry is a clean re-attempt and
// never a duplicate turn; or context.Canceled when ctx is cancelled, which is both
// daemon shutdown AND the queued head being dropped — msgqueue.Remove cancels the
// in-flight delivery precisely so this wait unblocks at once and the drain advances
// without writing.
//
// WHY THE HOLD IS DETERMINISTIC and not merely likelier. The tracker's ordinary
// opener feed is asynchronous (child → parser → sink → drain → observe), so a
// waiter relying on it alone would let a back-to-back second message through before
// any event for the first had been parsed. This does not rely on it. The serial
// per-conversation drain calls openForDelivery on its OWN goroutine inside the same
// deliver call that then writes, so for messages A then B on one conversation the
// mark for A happens-before deliver(A) returns, which happens-before deliver(B)
// starts, which happens-before B reads membership here. That is program order on
// one goroutine: B parks however fast or slow the child is, including a child that
// has not yet started reading its stdin.
//
// timeout bounds ONE delivery attempt, not the message; see streamTurnHoldTimeout
// (main.go) for the arithmetic against msgqueue's give-up bound.
func (t *turnBusyTracker) waitIdleForDelivery(ctx context.Context, conversationID string, timeout time.Duration) error {
	if t == nil || conversationID == "" {
		return nil
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return t.WaitIdle(waitCtx, conversationID)
}

// openForDelivery marks conversationID mid-turn for a delivery that is about to
// write, and returns the undo that closes that turn again. A nil receiver or an
// empty conversation id marks nothing and returns an inert undo, so the seam needs
// no branch of its own and PTY mode runs the identical statement sequence.
//
// THE MARK PRECEDES THE WRITE, and that ordering is load-bearing rather than
// stylistic. A mark placed after a successful write races the asynchronous opener
// feed: on a fast child the turn's own TurnEnd can land and clear before the
// marking statement runs, leaving a stale mark that no later event will ever clear.
// Marking first makes the ordering unconditional — no byte has reached the child,
// so no event for this turn can precede the mark. The cost is one undo on the
// write-error path.
//
// The undo is LIVE ONLY IF THIS CALL ACTUALLY OPENED THE TURN, which setBusy
// reports out of the single lock acquisition it already takes. When the
// conversation was already busy — another feed's opener landing between the wait
// returning nil and this mark, a --resume respawn replaying events being the
// plausible route — the undo is a no-op, so a failed write can never report
// somebody else's live turn idle and release the next message into it.
//
// Handing the clear back as a closure rather than exposing a second
// clear-by-conversation method is deliberate: the only way to obtain a clear is to
// have placed the matching open, so no later caller can reach "mark this
// conversation idle" by name. clearForSession stays the session-keyed door, for the
// reason its own doc gives.
func (t *turnBusyTracker) openForDelivery(conversationID string) (undo func()) {
	if t == nil || conversationID == "" {
		return func() {}
	}
	// Zero delta on both marks: this feed carries no event and no tool call. The
	// undo's sweep is a no-op whenever the conversation was still idle, which is
	// the ordinary case — the undo only fires when this call actually opened the
	// turn, so nothing was retained at the moment it was placed. Not
	// unconditionally, though: the delta is applied ahead of setBusy's membership
	// early return, so a ToolStart landing between the open and a failed write
	// does populate the entry — reachable once a /clear has cleared the mark while
	// the old child is still streaming. Discarding it there is correct rather than
	// incidental: inflight moves with busy under one lock acquisition, so a
	// conversation this tracker reports idle never retains a call.
	if !t.setBusy(conversationID, true, toolCallDelta{}) {
		return func() {}
	}
	return func() { t.setBusy(conversationID, false, toolCallDelta{}) }
}

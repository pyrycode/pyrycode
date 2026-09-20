package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/eventring"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/turnbridge"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// coalesceWindow bounds the delivery latency of a single long-streaming
// assistant message: a non-empty delta buffer flushes after at most this long
// even when no message-boundary or non-text event arrives first (ADR 025
// § Phase 2, "per JSONL message or ~250 ms"). Tunable here; not exposed as
// config in this slice (no AC asks for it).
const coalesceWindow = 250 * time.Millisecond

// maxDeltaTextBytes bounds the RAW assistant-text bytes carried by a single
// assistant_delta, so the marshalled protocol.Envelope stays under the
// 65519-byte v2 application-envelope cap (docs/protocol-mobile.md
// § Application-envelope size cap: Noise's 65535-byte transport message minus
// the 16-byte AEAD tag). It bounds the INPUT text, not the envelope — do not
// "correct" it towards 65519.
//
// The arithmetic. encoding/json has SetEscapeHTML on by default, so '<', '>',
// '&' and every control byte without a short escape each cost six bytes on the
// wire; the cut here is a BYTE cut, which makes 6-bytes-out-per-input-byte the
// true ceiling. Multi-byte runes are NOT the worst case — Go emits them raw, so
// a 4-byte emoji stays 4 bytes. 10000 x 6 = 60000 B of escaped text, plus ~512 B
// budgeted for everything outside `text`: the payload's other three keys and its
// punctuation, a 36-byte conversation_id and a 36-byte turn_id, seq's digits,
// and the envelope's own id/type/ts/payload/event_id frame. 60512 B, ~7.6% under
// the cap; the measured worst case (the cap test's '<' fill) is 60220 B, 91.9%,
// so the 512-byte budget is headroom over an observed ~220 B.
//
// The invariant is ENFORCED by
// TestInteractiveTurnEmitterV2_OversizedDeltaFitsEnvelopeCap; if that ever
// fails, LOWER this constant — never raise it. The conservative constant is the
// belt; the deterministic per-frame cap test is the suspenders (different
// fabric). Same shape as relay's bundleChunkBytes.
const maxDeltaTextBytes = 10000

// interactiveBroadcaster is the capability-aware fan-out surface the structured
// emitter needs: the interactive-conn snapshot (#626) and the per-conn sealed
// push (#571). *relay.V2SessionManager satisfies it. Declared at the consumer
// (CODING-STYLE) so the emitter unit-tests drive it without a real manager.
type interactiveBroadcaster interface {
	ActiveConns(ctx context.Context) []relay.ActiveConn
	Push(ctx context.Context, connID string, env protocol.Envelope) error
}

// cursorReader is the minimal conversation-cursor surface the interactive turn
// emitter needs: CurrentConversation() — the cursor stamped by send_message via
// Supervisor.WriteUserTurn (#312/#322). Declared as an interface so the emitter
// unit tests can drive it without a real *supervisor.Supervisor;
// activeConversation.CurrentConversation() (main.go) is the production implementer.
type cursorReader interface {
	CurrentConversation() string
}

type assistantDeltaLane struct {
	turnID string
	seq    int
}

// interactiveTurnEmitterV2 is the stateful structured turn-event emitter at the
// heart of Phase 2 (ADR 025 § Phase 2). It consumes one neutral
// turnevent.Event at a time via Handle, derives the turn_state lifecycle
// statefully (responding / thinking / idle), maps each content event to a v2
// wire envelope via the pure #627 adapter, and fans each envelope only to v2
// conns that were granted the interactive capability (#626).
//
// It is a passive state machine — it spawns no goroutine and owns no queue
// (contrast assistantTurnEmitterV2, whose PTY-drain source could wedge claude).
// All lifecycle fields are plain (no atomic, no mutex): Handle is designed to
// run only on the producer's single Run goroutine, which invokes OnEvent
// serially (turnbridge/producer.go). #633 wires that goroutine; here unit
// tests call Handle directly with a scripted sequence.
//
// Delta coalescing (#609): consecutive TextChunks sharing a MessageID are
// buffered and emitted as ONE assistant_delta — flushed at the next message
// boundary, before any interleaved non-text envelope, at the turn boundary, or
// on the ~250ms coalesceWindow timer, whichever comes first. The timer is owned
// here but its channel is selected by the producer's drain loop and routed back
// into flushDelta on that SAME single Run goroutine (Config.FlushSignal /
// OnFlush, wired by startInteractiveTurnStreamV2). So the emitter still spawns
// no goroutine and the coalescing fields stay unguarded-but-race-free. Go 1.26
// timer semantics (no stale fire after Stop/Reset) make the single-goroutine
// arm/reset correct without a manual channel drain. A flush larger than
// maxDeltaTextBytes splits across several assistant_deltas (#1495) so no
// envelope exceeds the v2 application-envelope cap; the buffer is what
// coalesces, the flush is what bounds the frame.
//
// SECURITY: application output — assistant text, thought text (never even
// mapped), tool title/input/result summaries — is NEVER logged at any level.
// Logs carry only lengths-free discriminants: event, kind (the event-type
// name, not content), conversation_id, turn_id, env_id, conn_id, and Push's
// transport-sentinel err. Thought text is dropped by MapEvent and never
// forwarded — thinking surfaces only as a turn_state transition.
type interactiveTurnEmitterV2 struct {
	sup    cursorReader
	bcast  interactiveBroadcaster
	logger *slog.Logger

	// Lifecycle state — read/written only on the single Handle goroutine.
	inTurn       bool                 // whether a turn is currently open
	turnID       string               // current turn's id, minted at turn start
	turnConvID   string               // conversation that owns the open turn; set at turn open, compared each Handle (#1062)
	seq          int                  // per-turn assistant-delta counter; 0 at each turn boundary
	currentState turnbridge.TurnState // last-emitted turn_state, for transition de-dup
	childLanes   map[string]*assistantDeltaLane

	// nextID is the session-monotonic envelope-ID counter. It is NEVER reset
	// across turns (mirrors #589's policy; the basis for #611 mid-turn-reconnect
	// resync). nextID++ runs per conn per envelope, so the same logical envelope
	// gets a distinct env.ID on each conn — each conn still sees a strictly
	// increasing subsequence.
	nextID uint64

	// ring stores every fanned-out event under a durable, connection-independent
	// id that is unique daemon-wide (#2022) for the #647 reconnect-replay path,
	// while retention stays per conversation. It is owned here
	// (created in the constructor, daemon-resident since the emitter is built
	// once in startInteractiveTurnStreamV2 — codebase/633.md), distinct from the
	// per-conn envelope nextID above (NOT overloaded). The ring is self-
	// synchronised: it is the one field a second goroutine (the future query
	// path) may touch, so the emitter's other fields stay unguarded and
	// single-goroutine. Appended on emit, before the per-conn fan-out.
	ring *eventring.Ring

	// hist is the durable conversation log (#2114): the same envelopes the ring
	// holds for reconnect catch-up, kept past the ring's per-conversation bound
	// and past a daemon restart, which is what the ring cannot do. Appended on
	// emit beside the ring, before the per-conn fan-out.
	//
	// nil means no durable log, and emitting is then exactly what it was before
	// this field existed — which is what lets every emitter test here keep
	// constructing emitters with no store. Assigned after construction rather
	// than passed to newInteractiveTurnEmitterV2: that constructor has 86 call
	// sites across 8 test files, and a positional parameter is not separable
	// from its call sites in Go. Concrete pointer, never an interface; see
	// appendConversationHistory for why that matters and where the nil guard
	// lives.
	hist *history.Store

	// usageRec is the per-conversation context-reading memory (#2460): the daemon's
	// answer to "what did claude last report for this conversation" after a daemon
	// restart, an app restart, or from a second device. Written from the
	// turnevent.ContextUsage arm below; the on-demand request_context_usage flight
	// writes the SAME recorder, because one memory with two producers is the point
	// — a value written from only one arm goes stale the moment a client uses the
	// other.
	//
	// nil means no memory, and emitting is then exactly what it was before this
	// field existed — which is what lets every emitter test here keep constructing
	// without one. Assigned after construction for hist's reason, one field up:
	// newInteractiveTurnEmitterV2 has 86 call sites across 8 test files and a
	// positional parameter is not separable from them in Go. The nil guard lives in
	// contextUsageRecorder.record, beside the rest of that seam's inertness.
	usageRec *contextUsageRecorder

	// Delta-coalescing state (#609) — read/written only on the single Handle/
	// flush goroutine, same contract as the lifecycle fields above. The invariant
	// the timer relies on: flushTimer is armed iff deltaBuf is non-empty.
	deltaBuf    strings.Builder // accumulated assistant text for the open (un-flushed) delta
	deltaMsgID  string          // MessageID of the buffered text; meaningful only while deltaBuf.Len() > 0
	deltaParent string          // ParentToolCallID of the buffered text; empty identifies the main lane
	deltaConvID string          // conversation cursor captured when buffering began; the flush emits against it
	flushTimer  *time.Timer     // ~250ms latency timer; owned here, its channel selected by the producer (flushC)
}

// newInteractiveTurnEmitterV2 constructs an emitter wired to sup and bcast. The
// coalescing timer is created disarmed (NewTimer then Stop — Go 1.26 needs no
// channel drain) and armed only when the first chunk of a delta is buffered.
func newInteractiveTurnEmitterV2(sup cursorReader, bcast interactiveBroadcaster, logger *slog.Logger) *interactiveTurnEmitterV2 {
	t := time.NewTimer(coalesceWindow)
	t.Stop()
	return &interactiveTurnEmitterV2{
		sup:        sup,
		bcast:      bcast,
		logger:     logger,
		flushTimer: t,
		ring:       eventring.New(eventring.MaxEventsPerConversation),
	}
}

// flushC exposes the coalescing timer's channel for the producer's drain select
// (#609). The producer selects it and routes each fire back into flushDelta on
// the single Run goroutine; the emitter never reads this channel itself, so the
// timer fire is not a second goroutine touching the emitter's state.
func (e *interactiveTurnEmitterV2) flushC() <-chan time.Time { return e.flushTimer.C }

// Handle drives the emitter one event at a time. It reads the conversation
// cursor once; on an empty cursor it drops the event (mirrors #589). Otherwise
// it type-switches the event into the lifecycle actions and the ordered
// envelopes they emit (see the per-kind table in the spec). Not safe for
// concurrent use — designed for the producer's single Run goroutine.
func (e *interactiveTurnEmitterV2) Handle(ctx context.Context, ev turnevent.Event) {
	convID := e.sup.CurrentConversation()
	if convID == "" {
		e.logger.Debug("relay: interactive-turn drop; no cursor",
			"event", "interactive_turn.no_cursor",
			"kind", eventKind(ev))
		return
	}

	if e.inTurn && convID != e.turnConvID {
		// Follow-active switch (#1062): the prior conversation's subscription was
		// torn down mid-turn, so its TurnEnd never arrived and inTurn/currentState
		// are stale. Flush its buffered delta against its OWN conversation
		// (deltaConvID is captured, not the live cursor), then mark the turn closed
		// so startTurnIfNeeded re-mints a fresh turn — a new turnID, seq 0, and an
		// opening turn_state — for the new conversation. flushDelta before endTurn:
		// the abandoned text still carries the prior turn's turnID/seq, in place
		// until the re-mint. No-op when nothing is buffered.
		e.flushDelta(ctx)
		e.endTurn()
	}

	switch v := ev.(type) {
	case turnevent.ThoughtChunk:
		if !e.startTurnIfNeeded(convID) {
			return
		}
		e.flushDelta(ctx) // any pending delta precedes the thinking transition
		// Thought text is never forwarded; thinking surfaces only as a state.
		e.transitionTo(ctx, convID, turnbridge.StateThinking)
	case turnevent.TextChunk:
		if !e.startTurnIfNeeded(convID) {
			return
		}
		// Lane/message-boundary flush: changing either key ends the prior delta
		// before this chunk opens a fresh one. One active buffer preserves global
		// arrival order when main and child streams interleave.
		if e.deltaBuf.Len() > 0 && (v.ParentToolCallID != e.deltaParent || v.MessageID != e.deltaMsgID) {
			e.flushDelta(ctx)
		}
		if !e.ensureDeltaLane(convID, v.ParentToolCallID) {
			return
		}
		// Safe before buffering: during a text run the state is already
		// responding, so this is a no-op and never emits a turn_state ahead of
		// buffered text; it only emits on the first content of a turn, when the
		// buffer is necessarily empty.
		e.transitionTo(ctx, convID, turnbridge.StateResponding)
		wasEmpty := e.deltaBuf.Len() == 0
		e.deltaConvID = convID
		e.deltaMsgID = v.MessageID
		e.deltaParent = v.ParentToolCallID
		e.deltaBuf.WriteString(v.Text)
		if wasEmpty {
			// Arm the latency window from the OLDEST unflushed chunk; never re-arm
			// on a same-message append, so a long-streaming message is bounded to
			// one window, not one-per-chunk.
			e.flushTimer.Reset(coalesceWindow)
		}
	case turnevent.ToolStart:
		if !e.startTurnIfNeeded(convID) {
			return
		}
		e.flushDelta(ctx) // buffered text precedes the tool_use it logically preceded
		e.transitionTo(ctx, convID, turnbridge.StateResponding)
		e.emitMapped(ctx, convID, ev)
	case turnevent.ToolUpdate:
		if !e.startTurnIfNeeded(convID) {
			return
		}
		e.flushDelta(ctx) // buffered text precedes the tool_result
		e.transitionTo(ctx, convID, turnbridge.StateResponding)
		e.emitMapped(ctx, convID, ev)
	case turnevent.ToolProgress:
		// A progress reading belongs to the tool row that ToolStart already opened.
		// It is turn-scoped but lifecycle-neutral: preserve wire order by flushing
		// prior text, then publish without emitting another turn_state or retaining
		// counter state in the daemon.
		if !e.startTurnIfNeeded(convID) {
			return
		}
		e.flushDelta(ctx)
		e.emitMapped(ctx, convID, ev)
	case turnevent.ToolCallDenied:
		// claude refused a tool call it had already announced (#2233). TURN-SCOPED,
		// taking ToolStart/ToolUpdate's shape above rather than the status peers' below,
		// and the reason is the join: this frame names ONE call the client has already
		// seen a tool_use for, carries that call's tool_use_id, and must land in the
		// same turn as it. The captured line order puts it strictly between the two
		// (assistant/tool_use → system/permission_denied → user/tool_result), so
		// startTurnIfNeeded ordinarily finds the turn the tool_use opened and mints
		// nothing; it is called anyway so a denial arriving first still addresses a
		// turn rather than an empty string.
		//
		// It deliberately does NOT call transitionTo, which is the one place it departs
		// from the two arms it otherwise copies. A denial reports no lifecycle change:
		// the ToolStart before it already set responding, and emitting a second
		// turn_state would claim a transition this event does not report. turnMarkFor
		// answers turnMarkNone for the variant and #2232 pinned that with a row in the
		// turn-mark totality guard, so nothing downstream reads a busy edge off it.
		//
		// Flush any pending delta first so buffered text keeps its wire position ahead
		// of the denial. Like turn_state this flows through emit() and is NOT a
		// droppable delta (the droppable set is assistant_delta only, #610), so it holds
		// a queue slot; that is bounded by the producer rather than here — one frame per
		// permission_denied line, no accumulator and no dedup.
		//
		// No capability gate in the arm, and no second bound on claude's five strings.
		// The interactive grant is filtered once, in emit(), for every frame type; all
		// five were bounded at construction by streamsup's maxTaskFieldID /
		// maxDenialProse. Writing either here is how a single gate stops being single.
		if !e.startTurnIfNeeded(convID) {
			return
		}
		e.flushDelta(ctx)
		e.emitMapped(ctx, convID, ev)
	case turnevent.TurnEnd:
		if !e.inTurn {
			e.logger.Debug("relay: interactive-turn drop; turn_end outside turn",
				"event", "interactive_turn.turn_end_no_turn")
			return
		}
		e.flushDelta(ctx) // turn-boundary flush: delta precedes turn_end, before idle
		e.emitMapped(ctx, convID, ev)
		e.transitionTo(ctx, convID, turnbridge.StateIdle)
		e.endTurn()
	case turnevent.Stall:
		// Onset-only control/state signal — a peer of turn_state. Emit with NO
		// turn-lifecycle mutation: a stall is orthogonal to thinking/responding/
		// idle and not turn-scoped (no startTurnIfNeeded / transitionTo / endTurn;
		// inTurn, turnID, currentState all untouched). It only flushes any pending
		// delta first so buffered text keeps its wire position ahead of the stall.
		// The phone self-clears on the next turn activity. Like turn_state it flows
		// through emit() and is NOT a droppable delta — the droppable set is
		// assistant_delta only (#610), so a stall is never silently discarded.
		e.flushDelta(ctx)
		e.emitMapped(ctx, convID, ev)
	case turnevent.ApiRetry, turnevent.Compacting:
		// PTY-derived status peers of turn_state (claude's API-retry and
		// auto-compaction sub-states). Like Stall, they carry NO turn-lifecycle
		// mutation: a status peer is orthogonal to thinking/responding/idle and
		// not turn-scoped (no startTurnIfNeeded / transitionTo / endTurn; inTurn,
		// turnID, currentState untouched). Flush any pending delta first so
		// buffered text keeps its wire position ahead of the status frame, then
		// emit. The counter/active fields are bounded (see the SECURITY note); no
		// banner or screen text is ever held or forwarded.
		e.flushDelta(ctx)
		e.emitMapped(ctx, convID, ev)
	case turnevent.CompactionBoundary:
		// claude's compaction boundary (#2237), taking the same shape as the status
		// peers above: NO turn-lifecycle mutation (no startTurnIfNeeded / transitionTo /
		// endTurn; inTurn, turnID, currentState untouched).
		//
		// Kept a separate case from the ApiRetry/Compacting pair despite the identical
		// body, following this switch's own rule: it merges arms that share a REASON, and
		// this one's reason is its own. Those are sub-states of a live turn with two
		// edges; this is a MARK IN THE CONVERSATION'S HISTORY with none — it says why
		// claude no longer remembers something from before that point. The producer emits
		// it for a boundary line that followed no compacting edge at all (deliberately, so
		// an auto-compaction announcing itself differently is still published), so unlike
		// its neighbours this frame may legitimately arrive with no turn open whatever.
		// Opening one here would wedge the conversation exactly as opening one on an
		// unrecognized message would: no turn end follows a mark orthogonal to the turn.
		// turnMarkFor's default already answers turnMarkNone, and
		// TestTurnMarkFor_TotalOverEveryVariant pins it.
		//
		// Flush any pending delta first so buffered text keeps its wire position ahead of
		// the mark. Like turn_state this flows through emit() and is NOT a droppable delta
		// (the droppable set is assistant_delta only, #610), so it holds a queue slot;
		// that is bounded by the producer rather than here — one frame per compaction
		// boundary line, no accumulator and no dedup, and the frame is four small fields.
		//
		// No capability gate in the arm, and no second bound on the trigger. The
		// interactive grant is filtered once, in emit(), for every frame type; the trigger
		// was bounded at construction by streamsup's maxCompactTrigger. Writing either
		// here is how a single gate stops being single.
		e.flushDelta(ctx)
		e.emitMapped(ctx, convID, ev)
	case turnevent.Banner:
		// claude's operator-facing text (#2256), taking the same shape as the arms above:
		// NO turn-lifecycle mutation (no startTurnIfNeeded / transitionTo / endTurn;
		// inTurn, turnID, currentState untouched).
		//
		// Kept a separate case from the CompactionBoundary and ApiRetry/Compacting arms
		// despite the identical body, following this switch's own rule: it merges arms
		// that share a REASON, and this one's reason is its own. Those report MACHINE
		// STATES and history marks the daemon observed for itself; this carries TEXT
		// CLAUDE WROTE FOR A PERSON, which nothing in the daemon corroborates.
		//
		// Opening a turn here would wedge the conversation exactly as opening one on an
		// unrecognized message would, and the argument is stronger than
		// CompactionBoundary's rather than borrowed from it: a notification belongs to
		// claude's own queue and rides no turn, and a prompt a hook refuses is never
		// answered — so no turn end follows either producer, ever. turnMarkFor's
		// whitelist default already answers turnMarkNone, and
		// TestTurnMarkFor_TotalOverEveryVariant pins it.
		//
		// Flush any pending delta first so buffered text keeps its wire position ahead of
		// the banner. Like turn_state this flows through emit() and is NOT a droppable
		// delta (the droppable set is assistant_delta only, #610), so it holds a queue
		// slot; that is bounded by the producer rather than here — one frame per line,
		// no accumulator and no dedup.
		//
		// No capability gate in the arm and no bound on the text. The interactive grant
		// is filtered once, in emit(), for every frame type, and the text was bounded at
		// construction by the producer (#2257). Writing either here is how a single gate
		// stops being single. And nothing in this arm reads StopsTurn: the frame is a
		// report, so a claude-authored bool must not become a lever on the daemon's own
		// turn lifecycle.
		e.flushDelta(ctx)
		e.emitMapped(ctx, convID, ev)
	case turnevent.Unrecognized:
		// A parser-gap diagnostic, not a claude sub-state, but it takes the same
		// shape as the status peers above: NO turn-lifecycle mutation (no
		// startTurnIfNeeded / transitionTo / endTurn; inTurn, turnID, currentState
		// untouched). That is the deliberate choice, not an oversight — we do not
		// know what the message is, so it must neither open nor close a turn.
		// Opening one would wedge the conversation, because no turn end follows a
		// message we could not understand.
		//
		// Flush any pending delta first so buffered text keeps its wire position
		// ahead of the diagnostic. Like turn_state it flows through emit() and is
		// NOT a droppable delta — the droppable set is assistant_delta only
		// (#610). A burst therefore holds queue slots, which is accepted: a burst
		// means something is genuinely wrong and you want to see it.
		e.flushDelta(ctx)
		e.emitMapped(ctx, convID, ev)
	case turnevent.BackgroundTaskStarted, turnevent.BackgroundTaskUpdated, turnevent.BackgroundTaskRoster,
		turnevent.BackgroundTaskProgress:
		// claude's background-task lifecycle (#1394), taking the same shape as the
		// status peers above: NO turn-lifecycle mutation (no startTurnIfNeeded /
		// transitionTo / endTurn; inTurn, turnID, currentState untouched). A
		// background task OUTLIVES the turn that spawned it — that is the whole
		// #1240 point — so it is not turn-scoped, and opening a turn on one would
		// wedge the conversation exactly as opening one on an unrecognized message
		// would: no turn end follows work that is orthogonal to the turn.
		//
		// Flush any pending delta first so buffered text keeps its wire position
		// ahead of the frame. An empty roster is FORWARDED, not filtered — it says
		// nothing is alive, which is the reassurance #1240's symptom needs. Like
		// turn_state these flow through emit() and are NOT droppable deltas (the
		// droppable set is assistant_delta only, #610), so a burst holds queue
		// slots; the producer already caps roster entries, and these only fire on
		// claude's own lifecycle lines.
		//
		// BackgroundTaskProgress (#2246) joins the case list rather than earning an arm
		// of its own, because every sentence above already describes it: same subject,
		// same non-turn-scoped posture, same flush-then-emit. What is NOT already true
		// of it is the last clause — this variant does not fire only on claude's
		// lifecycle lines, it fires repeatedly while a task runs. That is exactly why
		// its producer carries a rate bound (streamsup.minTaskToolCallsPerEvent) where
		// its three neighbours carry none, and, as with thinking progress, no second cap
		// is imposed here: a filter in this file would silently diverge from the one
		// that decided the event existed.
		e.flushDelta(ctx)
		e.emitMapped(ctx, convID, ev)
	case turnevent.ThinkingProgress:
		// Claude's live thinking reading is enough to open an interruptible turn,
		// even before a partial ThoughtChunk, assistant text, or tool call arrives.
		// The result line closes every subtype through the shared TurnEnd arm, while
		// pool teardown and child exit cover a child that emits no result, so this
		// opener has the same total closure as content-driven openers.
		//
		// Start before publishing so the state and progress share one daemon-minted
		// turn identity with later content. Flush before the transition so buffered
		// text from an earlier inference request keeps its wire position ahead of
		// the new thinking phase. transitionTo de-duplicates repeated readings.
		// Like turn_state these flow through emit() and are
		// NOT droppable deltas (the droppable set is assistant_delta only, #610),
		// so they hold queue slots; the producer's rate bound
		// (streamsup.minThinkingTokensPerEvent, one per 64 tokens of accumulated
		// delta) is what keeps the count to roughly 1–2 per typical turn, and no
		// second cap is imposed here.
		if !e.startTurnIfNeeded(convID) {
			return
		}
		e.flushDelta(ctx)
		e.transitionTo(ctx, convID, turnbridge.StateThinking)
		e.emitMapped(ctx, convID, ev)
	case turnevent.RateLimited:
		// claude's usage-limit report (#1410), taking the same shape as the status
		// peers above: NO turn-lifecycle mutation (no startTurnIfNeeded /
		// transitionTo / endTurn; inTurn, turnID, currentState untouched).
		//
		// The reason is specific to this variant. A usage limit is a condition of
		// the ACCOUNT, not of the turn: claude emits its rate_limit_event line once
		// per run whatever the turn state, so the frame may legitimately arrive with
		// no turn open at all. Opening one here would wedge the conversation exactly
		// as opening one on an unrecognized message would — no turn end follows a
		// fact orthogonal to the turn.
		//
		// Flush any pending delta first so buffered text keeps its wire position
		// ahead of the report. Like turn_state this flows through emit() and is NOT
		// a droppable delta (the droppable set is assistant_delta only, #610), so it
		// holds a queue slot; that is bounded by the producer rather than here —
		// emitRateLimit's gate emits at most once per rate_limit_event line, and
		// claude sends one per run. Since #2250 that gate publishes a benign reading
		// as well, when one follows a non-benign one, so a run can carry the clear as
		// well as the warning; the per-line ceiling is what bounds the queue and it
		// did not move.
		e.flushDelta(ctx)
		e.emitMapped(ctx, convID, ev)
	case turnevent.ModelAnnounced:
		// claude's announced model for the turn (#1638), taking the same shape as
		// the status peers above: NO turn-lifecycle mutation (no startTurnIfNeeded /
		// transitionTo / endTurn; inTurn, turnID, currentState untouched).
		//
		// Kept a separate case from RateLimited above despite the identical body,
		// following this switch's own rule: it merges arms that share a REASON
		// (ApiRetry/Compacting, the three background-task variants) and keeps
		// ThinkingProgress and RateLimited apart because each has its own. So does
		// this one. A usage limit is a condition of the ACCOUNT; an announced model
		// is a property of the TURN'S CONFIGURATION — claude emits its init line once
		// per turn, so the frame rides along with every turn rather than delimiting
		// one, and arrives in every conversation rather than in an unlucky one. That
		// last part is why opening a turn here would be worse than it is for the
		// peers above: it would wedge ALL of them. TestTurnMarkFor_TotalOverEveryVariant
		// already pins the lifecycle answer as turnMarkNone.
		//
		// Flush any pending delta first so buffered text keeps its wire position
		// ahead of the announcement. Like turn_state this flows through emit() and is
		// NOT a droppable delta (the droppable set is assistant_delta only, #610), so
		// it holds a queue slot. Nothing bounds the rate on either side and that is
		// deliberate: the producer applies no dedup (one event per init line), the
		// frames are small, and the cadence is claude's own rather than anything
		// network-reachable — a second, differently-shaped filter here is the hazard
		// the ThinkingProgress and RateLimited arms both name.
		//
		// No capability gate in the arm. The interactive grant is filtered once, in
		// emit(), for every frame type; writing a second one here is how that single
		// gate stops being single.
		e.flushDelta(ctx)
		e.emitMapped(ctx, convID, ev)
	case turnevent.ModelRefusalFallback:
		// A refusal fallback explains a model change but carries no request or
		// message identity and is not a lifecycle edge. Preserve wire order by
		// flushing pending text, then publish without opening, transitioning, or
		// ending a turn. The capability decision remains the single gate in emit.
		e.flushDelta(ctx)
		e.emitMapped(ctx, convID, ev)
	case turnevent.ModelRefusalNoFallback:
		// A refusal without fallback explains why output stopped but carries no
		// identity that makes it a lifecycle edge. Flush pending text to preserve
		// wire order, then publish through emit's single capability gate without
		// opening, transitioning, or ending a turn.
		e.flushDelta(ctx)
		e.emitMapped(ctx, convID, ev)
	case turnevent.SessionFacts:
		// claude's own build and the posture it says the child is running under
		// (#2252), taking the same shape as the status peers above: NO turn-lifecycle
		// mutation (no startTurnIfNeeded / transitionTo / endTurn; inTurn, turnID,
		// currentState untouched).
		//
		// Kept a separate case from ModelAnnounced above despite the identical body,
		// following this switch's own rule that arms merge when they share a REASON.
		// These two are the closest pair on the switch — they map two halves of ONE
		// system/init line, and neither is a turn boundary — and the rule is still not
		// met, because what the arms argue about is HOW FAR OUT the fact sits. An
		// announced model is a property of the TURN'S CONFIGURATION and genuinely varies
		// turn to turn, which is the hazard that arm spends its length on. A build and a
		// posture are properties of the CHILD: the build cannot change under a running
		// child at all, and the posture changes only when someone changes it. So the
		// per-turn staleness ModelAnnounced warns a consumer about has no analogue here,
		// and merging the arms would file that difference under a shared comment.
		// TestTurnMarkFor_TotalOverEveryVariant pins the lifecycle answer as turnMarkNone.
		//
		// THE FRAME IS MAPPED AND PUSHED. turnbridge.MapEvent's turnevent.SessionFacts
		// arm (#2254) constructs protocol.SessionFactsPayload, so emitMapped reaches emit
		// with a payload and this lane carries the report to every interactive client.
		// Between #2252 and #2254 it took the unmapped branch instead, logging one
		// content-free Debug per turn naming the kind only — which is why this arm was
		// written before anything consumed it, so the variant was never logged as an
		// UNKNOWN event by the default arm below. eventKind's arm is the other half of
		// that and still earns its place: without it every eventKind call site would read
		// kind=unknown for a variant the daemon does recognize.
		//
		// Flush any pending delta first so buffered text keeps its wire position ahead of
		// the report. Like turn_state this flows through emit() and is NOT a droppable
		// delta (the droppable set is assistant_delta only, #610), so it holds a queue
		// slot. Nothing bounds the rate on either side and that is deliberate, for the
		// arm above's reason exactly: the producer applies no dedup (at most one event
		// per init line), both values are already bounded at construction by streamsup's
		// maxClaudeVersionField and maxPermissionModeField, and the cadence is claude's
		// own rather than anything network-reachable.
		//
		// No capability gate in the arm. The interactive grant is filtered once, in
		// emit(), for every frame type; writing a second one here is how that single
		// gate stops being single.
		e.flushDelta(ctx)
		e.emitMapped(ctx, convID, ev)
	case turnevent.MCPStatus:
		// One admitted snapshot describes the child rather than a turn, so it opens,
		// transitions, and closes nothing. The producer already owns strict-config
		// eligibility and once-per-child cardinality; this lane only preserves order
		// by flushing earlier text before publishing through emit's single capability
		// gate and one-logical-event ring append.
		e.flushDelta(ctx)
		e.emitMapped(ctx, convID, ev)
	case turnevent.ModelList:
		// claude's inventory of selectable models (#1849), taking the same shape as
		// the status peers above: NO turn-lifecycle mutation (no startTurnIfNeeded /
		// transitionTo / endTurn; inTurn, turnID, currentState untouched).
		//
		// Kept a separate case from ModelAnnounced above despite the identical body,
		// following the rule that arm states: this switch merges arms that share a
		// REASON and keeps apart the ones that do not. An announced model is a
		// property of the TURN'S CONFIGURATION, emitted once per turn; the menu is a
		// property of the CHILD, reported once per initialize exchange, so it is not
		// per-turn at all. That is one step further out than its neighbour, and it is
		// what makes opening a turn here worse than merely wrong: an announcement at
		// least rides a turn that some TurnEnd will close, while a turn opened on a
		// list has no turn end anywhere in its future to clear the mark.
		// TestTurnMarkFor_TotalOverEveryVariant already pins the lifecycle answer as
		// turnMarkNone and this arm agrees with it.
		//
		// Flush any pending delta first so buffered text keeps its wire position ahead
		// of the menu. The event is passed through UNTOUCHED and no field of it is
		// read here: turnbridge.MapEvent's arm copies slice HEADERS, cmd/pyry's
		// sessionModelHold retains the same value without copying, and
		// sessionModelHold.ModelList() reads it on a relay-leg goroutine — so a sort,
		// an in-place dedupe or an append into a slice the event owns would be a data
		// race on the session's retained menu, not merely a wrong menu.
		//
		// TWO QUEUES, TWO ANSWERS — there is no blanket "never dropped" here.
		// DOWNSTREAM at pushQueue this is not a droppable delta (the droppable set
		// there is assistant_delta only, #610), same as every peer above. UPSTREAM at
		// the fan-in it is: turnMarkFor answers turnMarkNone, so streamTurnSink's
		// sinkFor classes the event droppable and can refuse it at droppableCap under
		// load. That loss point is known and deliberately not fixed here — the live
		// lane is best-effort by construction, which is acceptable precisely because
		// sessionModelHold's retention sits ABOVE that send and #1846 is the reliable
		// path for a client that needs the menu it missed.
		//
		// Nothing bounds the rate on either side and that is deliberate: one
		// initialize exchange per child is the cadence, claude's own rather than
		// anything network-reachable, and all three of the list's dimensions are
		// already bounded at construction by streamsup's caps — a second,
		// differently-shaped filter here is the hazard the ThinkingProgress,
		// RateLimited and ModelAnnounced arms each name.
		//
		// No capability gate in the arm, for the arm above's reason.
		e.flushDelta(ctx)
		e.emitMapped(ctx, convID, ev)
	case turnevent.SlashCommandList:
		// The workspace's inventory of invocable slash commands (#2003), taking the
		// same shape as the status peers above: NO turn-lifecycle mutation (no
		// startTurnIfNeeded / transitionTo / endTurn; inTurn, turnID, currentState
		// untouched).
		//
		// Kept a separate case from ModelList above despite the identical body, and
		// this is the closest any pair in this switch has come to meeting the merge
		// rule that arm states: both are properties of the CHILD reported once per
		// initialize exchange, one step further out than per-turn, and
		// turnbridge.MapEvent's own arm calls this "the OTHER inventory the one
		// initialize exchange carries". The rule is still not met, because what the
		// two arms actually argue about is where they differ. What sits ABOVE the
		// fan-in drop: sessionModelHold retains the menu, and there is no analogue for
		// commands until #2004. What the reliable fallback is: #1846 for the menu,
		// #2005 for these. And the trust origin of the carried strings: claude's own
		// there, the WORKSPACE's here — a command defined in a repository was written
		// by whoever wrote that repository — which is what makes the logging posture
		// stricter here rather than merely equal. A merged arm would have to state
		// both sets side by side and would lose the per-variant argument, which is the
		// same basis on which ThinkingProgress and RateLimited stay apart.
		// TestTurnMarkFor_TotalOverEveryVariant already pins the lifecycle answer as
		// turnMarkNone and this arm agrees with it.
		//
		// Flush any pending delta first so buffered text keeps its wire position ahead
		// of the inventory. The event is passed through UNTOUCHED and no field of it
		// is read here — the ModelList arm's carry-never-mutate rule, with its
		// evidence one ticket away rather than in the tree: MapEvent's arm copies
		// slice HEADERS, and #2005 reads the mapped payload on a relay-leg goroutine,
		// so a sort, an in-place dedupe or an append into a slice the event owns would
		// be a data race across two goroutines rather than merely a wrong menu.
		//
		// TWO QUEUES, TWO ANSWERS, the arm above's unchanged. DOWNSTREAM at pushQueue
		// this is not a droppable delta (the droppable set there is assistant_delta
		// only, #610), same as every peer above. UPSTREAM at the fan-in it is:
		// turnMarkFor answers turnMarkNone, so streamTurnSink's sinkFor classes the
		// event droppable and can refuse it at droppableCap under load. DELIVERY ON
		// THIS LANE IS BEST-EFFORT BY CONSTRUCTION and this arm does not pretend
		// otherwise: beside that fan-in refusal, the bootstrap child's report is lost
		// unconditionally — it reports before any conversation is routed, so the
		// no-cursor drop above takes it — and a session rotation delivers no fresh
		// inventory. All three are #2005's connect-time snapshot to repair. The one
		// difference from the menu's own answer is that no retention sits above the
		// send here yet (#2004), which changes WHO repairs the loss and not whether
		// this arm should emit.
		//
		// Nothing bounds the rate on either side and that is deliberate: one
		// initialize exchange per child is the cadence, claude's own rather than
		// anything network-reachable; every CONTENT dimension is already bounded at
		// construction by streamsup's caps, and the frame's BYTE cost by #2002's
		// maxSlashCommandListBytes inside the mapper, which is the one place both wire
		// consumers pass through. A second, differently-shaped filter here is the
		// hazard the ThinkingProgress, RateLimited, ModelAnnounced and ModelList arms
		// each name.
		//
		// No capability gate in the arm, for the arms above's reason.
		e.flushDelta(ctx)
		e.emitMapped(ctx, convID, ev)
	case turnevent.ContextUsage:
		// One solicited reading of claude's context-window arithmetic (#2371),
		// taking the same shape as the status peers above: NO turn-lifecycle
		// mutation (no startTurnIfNeeded / transitionTo / endTurn; inTurn, turnID,
		// currentState untouched).
		//
		// Kept a separate case from the three inventories above despite the identical
		// body, following the rule the ModelAnnounced arm states: this switch merges
		// arms that share a REASON and keeps apart the ones that do not. Every arm
		// above argues about HOW FAR OUT its fact sits — per-turn configuration, or a
		// property of the child reported once per initialize exchange. THIS ONE IS
		// THE FIRST ON THE SWITCH WHOSE ARGUMENT IS ABOUT WHEN IT ARRIVES, and that
		// difference is the whole reason the arm is written this way.
		//
		// IT ARRIVES AFTER THE TURN IT DESCRIBES HAS CLOSED. turnEndContextUsageRequester
		// asks for the reading from its own Sink on TurnEnd (#2289), so by the time
		// the answer is parsed and reaches this lane the turn is over and
		// startTurnIfNeeded would open a FRESH one. That is worse than merely wrong,
		// and worse here than for the ModelList arm above: a menu at least arrives
		// with no turn expected, where this event arrives immediately after a turn
		// end, so a re-minted turn would look plausible in a client and would sit
		// there forever — nothing is coming to close it. The event's own cadence
		// guarantees no later TurnEnd clears the mark. cmd/pyry's turnMarkFor answers
		// turnMarkNone by construction (whitelist opener set, turnMarkNone default)
		// and TestTurnMarkFor_TotalOverEveryVariant pins that independently, so this
		// arm needs no change there and must not acquire one.
		//
		// Flush any pending delta first so buffered text keeps its wire position
		// ahead of the reading. In the production order there is rarely anything
		// buffered — the turn's own end already flushed — but the flush is not
		// conditional on that, because a follow-active switch can reorder this lane
		// and the mid-turn ordering is pinned by its own test.
		//
		// The event is passed through UNTOUCHED on the WIRE leg and no field of it is
		// read to build one. turnbridge.MapEvent's arm is still the only consumer of
		// the reading's strings on that path, and its own comment owns the
		// no-normalisation rule for the memory paths.
		//
		// SINCE #2460 THE ARM HAS A SECOND, NON-WIRE CONSUMER: the registry memory
		// below, which reads exactly five fields — Model, TotalTokens, MaxTokens,
		// Percentage — and stamps its own clock. The three inventories (Categories,
		// MCPTools, MemoryFiles) and their dropped counts are NOT among them and must
		// not become so; contextUsageRecorder.record's block owns that argument and
		// drops them by never naming them.
		//
		// TWO QUEUES, TWO ANSWERS, the arms above's unchanged. DOWNSTREAM at pushQueue
		// this is not a droppable delta (the droppable set there is assistant_delta
		// only, #610). UPSTREAM at the fan-in it is: turnMarkFor answers turnMarkNone,
		// so streamTurnSink's sinkFor classes the event droppable and can refuse it at
		// droppableCap under load. DELIVERY ON THIS LANE IS BEST-EFFORT BY
		// CONSTRUCTION and this arm does not pretend otherwise — but the loss here is
		// SELF-REPAIRING in a way an inventory's is not, and that is the one place
		// this variant is easier than its neighbours rather than harder: a fresh
		// reading is solicited after the very next turn, so no connect-time resolver
		// is owed for it. A client that missed one is at most one turn stale.
		//
		// Nothing bounds the rate on either side and that is deliberate: one reading
		// per completed turn is the cadence, driven by the daemon's own post-turn ask
		// rather than by anything network-reachable, and every dimension of the
		// reading is already bounded at construction by streamsup's
		// maxContextUsageEntries and maxContextUsageStringBytes. A second,
		// differently-shaped filter here is the hazard the ThinkingProgress,
		// RateLimited, ModelAnnounced, ModelList and SlashCommandList arms each name.
		//
		// No capability gate in the arm, for the arms above's reason.
		e.flushDelta(ctx)
		e.emitMapped(ctx, convID, ev)
		// #2460, and it runs AFTER the publish so the wire frame is never delayed
		// behind an fsync. It is the one side effect on this switch that is not a
		// send, and it is deliberately not gated on the send succeeding: the two
		// have different failure modes and a conn-side drop is not a reason to
		// forget a reading. Lifecycle stays untouched, per this arm's whole point.
		//
		// convID is the follow-active cursor this arm already stamps the frame
		// with, so the registry write inherits the frame's attribution rather than
		// making a second one — there is no state in which the row and the frame
		// disagree about which conversation reported. An id the registry does not
		// hold writes nothing and saves nothing; see record.
		e.usageRec.record(conversations.ConversationID(convID), v)
	default:
		e.logger.Debug("relay: interactive-turn drop; unknown event",
			"event", "interactive_turn.unknown",
			"kind", eventKind(ev))
	}
}

// startTurnIfNeeded opens a turn if one is not already open: mint a fresh turn
// id, reset seq, and clear currentState so the first state transition emits. On
// a turn-id mint failure (crypto/rand — defensive) it WARN-logs and leaves the
// turn closed so the next event retries. Returns whether a turn is open.
func (e *interactiveTurnEmitterV2) startTurnIfNeeded(convID string) bool {
	if e.inTurn {
		return true
	}
	id, err := conversations.NewID()
	if err != nil {
		// crypto/rand failure — never echo err detail beyond the sentinel.
		e.logger.Warn("relay: interactive-turn drop; turn-id mint failed",
			"event", "interactive_turn.rand_err",
			"conversation_id", convID)
		return false
	}
	e.turnID = string(id)
	e.seq = 0
	e.currentState = ""
	e.inTurn = true
	e.turnConvID = convID
	return true
}

// ensureDeltaLane lazily mints the identity for a child assistant lane. The
// empty parent id is the main lane already opened by startTurnIfNeeded. A mint
// failure leaves no partial lane state and logs no application-controlled id.
func (e *interactiveTurnEmitterV2) ensureDeltaLane(convID, parentID string) bool {
	if parentID == "" {
		return true
	}
	if _, ok := e.childLanes[parentID]; ok {
		return true
	}
	id, err := conversations.NewID()
	if err != nil {
		e.logger.Warn("relay: interactive-turn drop; child turn-id mint failed",
			"event", "interactive_turn.child_rand_err",
			"conversation_id", convID)
		return false
	}
	if e.childLanes == nil {
		e.childLanes = make(map[string]*assistantDeltaLane)
	}
	e.childLanes[parentID] = &assistantDeltaLane{turnID: string(id)}
	return true
}

func (e *interactiveTurnEmitterV2) deltaAddress(parentID string) (string, int) {
	if parentID == "" {
		return e.turnID, e.seq
	}
	lane := e.childLanes[parentID]
	return lane.turnID, lane.seq
}

func (e *interactiveTurnEmitterV2) advanceDeltaSeq(parentID string) {
	if parentID == "" {
		e.seq++
		return
	}
	e.childLanes[parentID].seq++
}

// transitionTo emits a turn_state envelope for state, de-duped against the
// last-emitted state. State-change-based emission is a superset of "first
// content -> responding" and naturally handles interleaving (thinking -> text
// -> thinking re-emits each transition).
func (e *interactiveTurnEmitterV2) transitionTo(ctx context.Context, convID string, state turnbridge.TurnState) {
	if e.currentState == state {
		return
	}
	e.currentState = state
	typ, payload := turnbridge.BuildTurnState(convID, state)
	e.emit(ctx, convID, typ, payload)
}

// endTurn closes the current turn and discards every main/child lane identity
// and counter. It need not touch the delta buffer — callers flush before every
// turn end or conversation switch.
func (e *interactiveTurnEmitterV2) endTurn() {
	e.inTurn = false
	e.turnID = ""
	e.turnConvID = ""
	e.seq = 0
	e.currentState = ""
	e.childLanes = nil
}

// closeForConversation returns an externally-abandoned active turn to idle
// without inventing a turn_end result the child never emitted. The caller is the
// stream drain, so lifecycle and delta fields retain their single-writer rule.
func (e *interactiveTurnEmitterV2) closeForConversation(ctx context.Context, conversationID string) {
	if !e.inTurn || e.turnConvID != conversationID {
		return
	}
	e.flushDelta(ctx)
	e.transitionTo(ctx, conversationID, turnbridge.StateIdle)
	e.endTurn()
}

// splitDeltaText splits s into consecutive chunks of at most max bytes each,
// never cutting through a multi-byte rune. Every chunk is a substring of s — no
// copy, no re-encoding — so concatenating the result in order reproduces s
// byte-for-byte, which is what makes the phone's reassembly structural rather
// than hoped-for. Returns nil for empty s, and never an empty chunk otherwise.
//
// Cut points back up from the stride boundary to the nearest rune start, which
// for valid UTF-8 terminates within three steps. The degenerate guard covers
// invalid UTF-8, where max consecutive continuation bytes would otherwise walk
// the cut back to start, emit an empty chunk and loop forever on
// claude-controlled input: cut at the unadjusted stride instead, which changes
// WHERE the split lands but never WHAT is emitted. That input cannot arise today
// — Text reaches here from encoding/json's string decoding, which substitutes
// U+FFFD for invalid bytes — so the guard is defence in depth.
func splitDeltaText(s string, max int) []string {
	if s == "" {
		return nil
	}
	var out []string
	for start := 0; start < len(s); {
		end := start + max
		if end >= len(s) {
			out = append(out, s[start:])
			break
		}
		for end > start && !utf8.RuneStart(s[end]) {
			end--
		}
		if end == start {
			end = start + max
		}
		out = append(out, s[start:end])
		start = end
	}
	return out
}

// flushDelta emits the buffered assistant text as ONE OR MORE coalesced
// assistant_deltas (current turnID + seq), each carrying at most
// maxDeltaTextBytes so no envelope exceeds the v2 application-envelope cap. It
// advances seq once per EMITTED envelope, clears the buffer, and stops the
// timer. It is a NO-OP on an empty buffer, which is what makes every flush
// trigger — message boundary, interleaved non-text envelope, turn end, or the
// timer firing on no pending text — harmless. Reached from Handle (inside the
// producer's OnEvent) and from the producer's OnFlush, both on the single Run
// goroutine; never concurrently (see the struct doc). seq NEVER advances once
// per buffered TextChunk: a buffer that fits emits exactly one delta with one
// seq, unchanged from #609.
//
// The loop is deliberately unconditional — no ctx.Err() check between chunks.
// Abandoning the remainder on teardown would turn a shutdown race into silently
// truncated assistant text; emit already returns early inside its Push-error
// branch, which bounds the wasted work to one conn snapshot per remaining chunk.
func (e *interactiveTurnEmitterV2) flushDelta(ctx context.Context) {
	if e.deltaBuf.Len() == 0 {
		return
	}
	// Capture before emitting: the reset below clears these fields, so the loop
	// must read the values the flush started with, not live state.
	text, convID, msgID, parentID := e.deltaBuf.String(), e.deltaConvID, e.deltaMsgID, e.deltaParent
	turnID, seq := e.deltaAddress(parentID)
	// Reconstruct a synthetic TextChunk per chunk and reuse the emitMapped path —
	// no new payload path. MapEvent carries ParentToolCallID to the wire;
	// MessageID remains only a coalescing key.
	for _, chunk := range splitDeltaText(text, maxDeltaTextBytes) {
		e.emitMappedAt(ctx, convID, turnevent.TextChunk{
			MessageID:        msgID,
			ParentToolCallID: parentID,
			Text:             chunk,
		}, turnID, seq)
		// Advance after mapping so the first frame in every lane starts at zero.
		seq++
		e.advanceDeltaSeq(parentID)
	}
	e.deltaBuf.Reset()
	e.deltaMsgID = ""
	e.deltaParent = ""
	e.deltaConvID = ""
	e.flushTimer.Stop()
}

// emitMapped maps a content event to its wire envelope via the pure #627
// adapter and emits it. ok==false is defensive — unreachable for every variant
// Handle routes here, since only ThoughtChunk and a nil event drop in MapEvent
// and neither reaches this function.
func (e *interactiveTurnEmitterV2) emitMapped(ctx context.Context, convID string, ev turnevent.Event) {
	e.emitMappedAt(ctx, convID, ev, e.turnID, e.seq)
}

func (e *interactiveTurnEmitterV2) emitMappedAt(ctx context.Context, convID string, ev turnevent.Event, turnID string, seq int) {
	typ, payload, ok := turnbridge.MapEvent(ev, turnbridge.TurnContext{
		ConversationID: convID,
		TurnID:         turnID,
		Seq:            seq,
	})
	if !ok {
		e.logger.Debug("relay: interactive-turn drop; no wire mapping",
			"event", "interactive_turn.unmapped",
			"kind", eventKind(ev))
		return
	}
	e.emit(ctx, convID, typ, payload)
}

// emit is the one place envelopes reach the wire (the ~25-LOC #589 echo,
// capability-gated). It marshals the payload once, snapshots the open conns
// fresh, filters to interactive grants, and Pushes one sealed envelope per conn
// with a per-conn monotonic env.ID.
func (e *interactiveTurnEmitterV2) emit(ctx context.Context, convID, typ string, payload any) {
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		// Defensive: the #607 payloads are closed string/int/bool structs and
		// cannot fail to marshal in practice. Never echo the payload or
		// err.Error() (encoding/json quotes invalid input bytes into its error).
		e.logger.Debug("relay: interactive-turn drop; payload marshal",
			"event", "interactive_turn.marshal_err",
			"conversation_id", convID,
			"turn_id", e.turnID)
		return
	}

	// Assign the durable, daemon-wide-unique event id and record it for replay
	// ONCE per logical event, before the per-conn fan-out — so the same event
	// carries the same id regardless of conn count, and is retained even when no
	// phone is interactive right now (the ring is the replay source for absent
	// phones that reconnect later). One timestamp per logical event, shared by
	// every conn (hoisted out of the loop). The returned id is stamped on each
	// conn's envelope below — #649 surfaces it on the wire so a reconnecting
	// phone can advertise it as last_event_id (consumed by #647). The ring's own
	// mutex handles the future cross-goroutine read; the emitter takes no lock.
	ts := time.Now().UTC()
	eventID := e.ring.Append(convID, typ, payloadJSON, ts)
	// The durable half of the same record (#2114), beside the ring and under the
	// same four values: one append per LOGICAL event, before the per-conn
	// fan-out, so a conversation with no interactive conn open still accumulates
	// history. A failure never suppresses the ring append or the wire emit
	// below — appendConversationHistory returns nothing, so there is no branch
	// to take.
	appendConversationHistory(e.hist, e.logger, "interactive_turn.history_append_err",
		convID, typ, payloadJSON, ts)

	// Fresh snapshot per envelope: a conn that joined mid-turn is included next
	// emit; a dropped conn is absent here, or surfaces as a Push error below.
	for _, c := range e.bcast.ActiveConns(ctx) {
		if !c.Interactive {
			continue // the capability gate — non-interactive conns never see the structured stream
		}
		e.nextID++
		// &eventID is shared by reference across every per-conn envelope: it is
		// a loop-invariant local, captured once and never reassigned, and Push
		// only ever reads the envelope (marshal/seal). So all conns observe the
		// identical durable id (AC-2) with no race and no per-conn allocation.
		env := protocol.Envelope{
			ID:      e.nextID,
			Type:    typ,
			TS:      ts,
			Payload: payloadJSON,
			EventID: &eventID,
		}
		if err := e.bcast.Push(ctx, c.ConnID, env); err != nil {
			if ctx.Err() != nil {
				return // teardown
			}
			e.logger.Debug("relay: interactive-turn push dropped",
				"event", "interactive_turn.push_err",
				"conn_id", c.ConnID,
				"env_id", e.nextID,
				"conversation_id", convID,
				"turn_id", e.turnID,
				"err", err)
		}
	}
}

// eventKind returns a content-free type discriminant for an Event, for log
// fields only. It never returns event content — only the variant name.
func eventKind(ev turnevent.Event) string {
	switch ev.(type) {
	case turnevent.TextChunk:
		return "text_chunk"
	case turnevent.ThoughtChunk:
		return "thought_chunk"
	case turnevent.ToolStart:
		return "tool_start"
	case turnevent.ToolUpdate:
		return "tool_update"
	case turnevent.ToolProgress:
		// The variant name only. ToolCallID is a claude-authored join key and must
		// not enter a log; ElapsedSeconds is omitted with it so every eventKind arm
		// remains content-free.
		return "tool_progress"
	case turnevent.TurnEnd:
		return "turn_end"
	case turnevent.Stall:
		return "stall"
	case turnevent.ApiRetry:
		return "api_retry"
	case turnevent.Compacting:
		return "compacting"
	case turnevent.ToolCallDenied:
		// The variant NAME only, and here the temptation is the widest on this switch:
		// ToolName and Message are exactly what a log line explaining a denial would
		// reach for, and Message is claude-authored prose that may quote THE COMMAND
		// LINE THAT WAS REFUSED. Neither is returned, and neither is the id or either
		// reason field. The variant is claimed by a Handle case on this lane, so this
		// file's `interactive_turn.unknown` Debug is not a live call site for it; the
		// reachable one is the no-cursor drop, which returns before the type switch,
		// plus the OTHER eventKind sites (acp_turn_stream.go, stream_turn_busy.go,
		// stream_turn_drain.go), all of which would otherwise read kind=unknown for a
		// variant the daemon does recognize.
		return "tool_denied"
	case turnevent.CompactionBoundary:
		// The variant NAME only, for the arms below's reason, and the temptation here is
		// Trigger — claude's own word, and precisely the field a log line explaining a
		// compaction would reach for. It is not returned, and neither count is. The
		// variant is claimed by a Handle case on this lane, so this file's
		// `interactive_turn.unknown` Debug is not a live call site for it; the reachable
		// one is the no-cursor drop, which returns before the type switch, plus the OTHER
		// eventKind sites (acp_turn_stream.go, stream_turn_busy.go, stream_turn_drain.go),
		// all of which would otherwise read kind=unknown for a variant the daemon does
		// recognize.
		return "compaction_boundary"
	case turnevent.Banner:
		// The variant NAME only, and the temptation here is the widest on this switch —
		// wider than the tool_denied arm's, which already called itself that. Text is
		// arbitrary claude-authored prose whose whole purpose is to explain something to
		// an operator, so a log line explaining a banner would reach for it first, and
		// Level reads like a log level by construction. NEITHER IS RETURNED, and neither
		// is the truncation flag or the stops-turn flag. The variant is claimed by a
		// Handle case on this lane, so this file's `interactive_turn.unknown` Debug is
		// not a live call site for it; the reachable one is the no-cursor drop, which
		// returns before the type switch, plus the OTHER eventKind sites
		// (acp_turn_stream.go, stream_turn_busy.go, stream_turn_drain.go), all of which
		// would otherwise read kind=unknown for a variant the daemon does recognize.
		return "banner"
	case turnevent.Unrecognized:
		// The variant NAME only. The event's Kind field holds claude's offending
		// type string, which is not returned here: this feeds log fields, and the
		// package rule is that nothing derived from claude's output reaches a log.
		return "unrecognized"
	case turnevent.BackgroundTaskStarted:
		// The variant NAME only, for the arm above's reason. These three carry the
		// most tempting fields in the package to log — TaskID, ToolCallID,
		// Description (the literal command line for claude's local_bash task
		// type), TaskType, Patch, and each roster row's three — and none of them
		// is returned here.
		return "background_task_started"
	case turnevent.BackgroundTaskUpdated:
		return "background_task_updated"
	case turnevent.BackgroundTaskRoster:
		return "background_task_roster"
	case turnevent.BackgroundTaskProgress:
		// The variant NAME only, for the arms above's reason — and here the temptation
		// is sharper than on any neighbour, because this variant's Description is a
		// readable account of what a subagent is doing and would make a genuinely
		// useful-looking log field. It is claude's text and it names a file, so it stays
		// out. The arm exists for the OTHER call sites, not for this file's default:
		// the handler case above claims the variant on this lane, but the ACP surface
		// drops it via acpbridge's own default and logs the kind, which would otherwise
		// read "unknown" for a variant the daemon does recognize.
		return "background_task_progress"
	case turnevent.ThinkingProgress:
		// The variant NAME only, for the arms above's reason — though here there
		// is nothing claude-derived to be tempted by in the first place: both
		// fields are ints, and neither is returned. The arm exists for the OTHER
		// call sites (acp_turn_stream.go, stream_turn_busy.go,
		// stream_turn_drain.go), not for this file's default: the handler case
		// above claims the variant on this lane, but the ACP surface drops it via
		// acpbridge's own default and logs the kind, which would otherwise read
		// "unknown" for a variant the daemon does recognize.
		return "thinking_progress"
	case turnevent.RateLimited:
		// The variant NAME only, for the arms above's reason — and here the
		// temptation is real again: Status and LimitType are claude-authored strings,
		// and Status is precisely the field a log line wants to explain itself with.
		// Neither is returned. The arm exists for the OTHER call sites
		// (acp_turn_stream.go, stream_turn_busy.go, stream_turn_drain.go), which
		// would otherwise log "unknown" for a variant the daemon does recognize —
		// exactly the ThinkingProgress arm's reason.
		return "rate_limited"
	case turnevent.ModelAnnounced:
		// The variant NAME only, for the arms above's reason — and Model is precisely
		// the field #833's posture, restated across internal/relay's
		// v2session_settings.go and internal/sessions' pool.go as "model / effort /
		// YOLO values are NEVER logged at any level", exists to keep out of a log. It
		// is not returned here.
		//
		// Like the two arms above, this variant is now claimed by a Handle case on
		// this lane (#1638), so this file's `interactive_turn.unknown` Debug is no
		// longer a live call site for it. Since #2003 that GENERALISES to the
		// production producer rather than being a claim about ModelAnnounced alone:
		// the last variant internal/streamsup emitted without a Handle case was
		// SlashCommandList, and the arm below records the case that claims it, so
		// that Debug is now a live call site for nothing the production producer
		// emits. Handle has an arm for 17 of turnevent.Event's 18 implementations,
		// and the one without is PermissionRequest, which streamsup.Parser never
		// produces (it is PTY/modalbridge-only, as
		// TestTurnMarkFor_TotalOverEveryVariant states independently).
		//
		// CORRECTED 2026-09-10 (#2252): the COUNT in the correction below has moved
		// again, and is restated here rather than edited in place so both corrections
		// stay legible. SessionFacts is a twentieth variant and it lands WITH a Handle
		// case, so the reading is now 18 of 20 with the same TWO arms missing —
		// PermissionRequest, which streamsup.Parser never produces, and
		// ConversationReset, whose absence is #2134's deliberate one. What did not move:
		// this arm's claim about ITSELF, and the fact that a count in a comment is
		// exactly the kind of claim nothing reddens when it goes stale. The reusable
		// check is a grep for the digits.
		//
		// CORRECTED 2026-09-06 (#2134): both sentences above are FALSE now, and the
		// paragraph is left standing rather than rewritten so what changed stays
		// legible. ConversationReset is a variant internal/streamsup DOES produce and
		// that deliberately has NO Handle case, so the `interactive_turn.unknown` Debug
		// is a live AND reachable call site for a production-producer variant again —
		// the first since #2003. The count is now 17 of 19 with TWO arms missing, and
		// only one of them is PermissionRequest. What is unchanged is this arm's own
		// claim about ITSELF: ModelAnnounced is still claimed by a Handle case, so the
		// Debug is still not live for THIS variant, and the no-cursor drop is still the
		// reachable eventKind site for it. Only the generalisation to "nothing the
		// production producer emits" has expired.
		//
		// The reachable eventKind call site left on this lane for THIS variant is the
		// no-cursor drop, which returns before the type switch. Without the arm every
		// eventKind site — here, acp_turn_stream.go, stream_turn_busy.go,
		// stream_turn_drain.go — would read kind=unknown for a variant the daemon does
		// recognize.
		return "model_announced"
	case turnevent.ModelRefusalFallback:
		// Variant name only. Models, scope, category, banner, and report tokens are
		// claude-authored content and must not enter logs.
		return "model_refusal_fallback"
	case turnevent.ModelRefusalNoFallback:
		// Variant name only. Model, category, banner, and report tokens are
		// claude-authored content and must not enter logs.
		return "model_refusal_no_fallback"
	case turnevent.SessionFacts:
		// The variant NAME only, for the arms above's reason, and here the temptation is
		// the PermissionMode: it is the field a log line explaining an unexpected posture
		// would reach for, and it would look genuinely useful. It is not returned, and
		// neither is the version. Both are claude-authored strings arriving on the path
		// maxModelField already bounds, so the #833 posture the arm above names —
		// restated across internal/relay's v2session_settings.go and internal/sessions'
		// pool.go as "model / effort / YOLO values are NEVER logged at any level" —
		// covers them unchanged. TruncatedFields is not returned either, though its
		// contents are DAEMON-authored: a cut is a fact about claude's value, and the
		// count of them would be one bit of it.
		//
		// THIS ARM MATCHES ITS NEIGHBOURS AGAIN since #2254, and the correction is worth
		// stating because the arm's justification MOVED rather than expired. In the
		// window between #2252 and #2254 it was live for this file's own Debug: the
		// variant had a Handle case, so the unknown-event drop was not its site, but
		// turnbridge.MapEvent had no arm, so emitMapped's unmapped-drop Debug fired for
		// it once per turn and read this name. That mapping arm landed, so the unmapped
		// drop is no longer a site for this variant either. What remains is the reachable
		// site the arms above name — the no-cursor drop, which returns before the type
		// switch — plus acp_turn_stream.go, stream_turn_busy.go and stream_turn_drain.go,
		// every one of which would read kind=unknown without this arm for a variant the
		// daemon does recognize.
		return "session_facts"
	case turnevent.MCPStatus:
		// Variant name only. Every server string is untrusted display content and none
		// is returned to this or any other diagnostic call site.
		return "mcp_status"
	case turnevent.ModelList:
		// The variant NAME only, for the arms above's reason — and here the
		// temptation is multiplied rather than merely present: every entry carries a
		// Value, a ResolvedModel and a DisplayName, which are exactly the fields the
		// arm above names #833's posture as existing to keep out of a log. None of
		// them is returned, and neither is the entry count.
		//
		// Like the three arms above, this variant is now claimed by a Handle case on
		// this lane (#1849), so this file's `interactive_turn.unknown` Debug is no
		// longer a live call site for it. The arm exists for the OTHER eventKind call
		// sites (acp_turn_stream.go, stream_turn_busy.go, stream_turn_drain.go) and
		// for the no-cursor drop on this lane, which returns before the type switch
		// and is therefore the reachable one here — all of which would otherwise read
		// kind=unknown for a variant the daemon does recognize.
		return "model_list"
	case turnevent.SlashCommandList:
		// The variant NAME only, and here the discipline matters MORE than in the
		// arms above rather than less. Every string this variant carries is
		// WORKSPACE-authored — a command defined in a repository was written by
		// whoever wrote that repository, a lower-trust origin than claude's own
		// strings — so the #833 posture the arm above names, restated across
		// internal/relay's v2session_settings.go and internal/sessions' pool.go as
		// "model / effort / YOLO values are NEVER logged at any level", covers these
		// a fortiori. No entry's Name is returned, no entry's TruncatedFields, and
		// neither is the entry count.
		//
		// Like the four arms above, this variant is now claimed by a Handle case on
		// this lane (#2003), so this file's `interactive_turn.unknown` Debug is no
		// longer a live call site for it. The chain that got here: #1854 declared the
		// variant, internal/streamsup's emitSlashCommandList produces it — reached
		// from emitModelList's initialize handling (#1877) and from the commands-only
		// rung #1891 added — turnbridge.MapEvent maps it (#2001), #2002 bounded the
		// mapped frame to the v2 application-envelope cap, and Handle's case routes
		// the event to that mapping. This was the last variant a production producer
		// emitted without a case, which is what lets the ModelAnnounced arm above
		// generalise its own claim.
		//
		// CORRECTED 2026-09-06 (#2134): "the last" has expired, and with it the support
		// this sentence lent the ModelAnnounced arm's generalisation — see that arm's
		// own correction. ConversationReset is now a production-producer variant with
		// no Handle case, deliberately, so SlashCommandList was the last such variant
		// only until #2134. This arm's claim about ITSELF still holds: #2003 did claim
		// the variant on this lane, and everything below about which sites stay
		// reachable for it is unaffected.
		//
		// STILL LIVE FOR THIS VARIANT, and reachable rather than merely live, because
		// a production producer emits it: the no-cursor drop, which returns before
		// the type switch and takes the bootstrap child's report unconditionally,
		// and stream_turn_drain.go's sink-full droppable drop and not-active-session
		// drop. Any initialize reply carrying a NON-EMPTY commands array puts one of
		// these on this lane, whether or not a models array rides with it, and each
		// of those three is dropped and logged by kind.
		//
		// Not reachable for this variant: observe's unbound-session drop and sinkFor's
		// close drop, both gated on a non-turnMarkNone mark, and emitMapped's unmapped
		// drop — WHICH STAYS UNREACHABLE FOR A NEW REASON. Something routes to it now,
		// where before nothing did; what keeps it untaken is that MapEvent never
		// refuses this variant, mapping even a zero-value SlashCommandList. That is
		// strictly the stronger statement, so do not read the shortened enumeration
		// above as a weakening.
		return "slash_command_list"
	case turnevent.ContextUsage:
		// The variant NAME only, and here the temptation is the LARGEST on the
		// switch rather than merely present. This event carries a Model, a Name per
		// category, a Name and a ServerName per MCP tool, and a PATH per memory file
		// — and the paths are the operator's and the workspace's own filesystem text,
		// the lowest-trust origin any arm here handles. Every one of them is exactly
		// what a diagnostic line explaining an unexpected reading would reach for.
		// NONE is returned, and neither is any list length nor any of the three
		// dropped counts: a count of what was cut is still a fact about the reading's
		// content, the SessionFacts arm's rule for TruncatedFields unchanged.
		//
		// The #833 posture the arms above name — restated across internal/relay's
		// v2session_settings.go and internal/sessions' pool.go as "model / effort /
		// YOLO values are NEVER logged at any level" — covers the Model directly and
		// the workspace-authored strings a fortiori.
		//
		// Like the five arms above, this variant is now claimed by a Handle case on
		// this lane (#2371), so this file's `interactive_turn.unknown` Debug is no
		// longer a live call site for it, and neither is emitMapped's unmapped drop:
		// turnbridge.MapEvent never refuses the variant, mapping even a zero-value
		// ContextUsage. STILL LIVE AND REACHABLE: the no-cursor drop, which returns
		// before the type switch, plus acp_turn_stream.go, stream_turn_busy.go and
		// stream_turn_drain.go — every one of which would read kind=unknown without
		// this arm for a variant the daemon does recognize. stream_turn_drain.go's
		// droppable drop is reachable for this variant specifically, because
		// turnMarkFor classes it turnMarkNone and therefore droppable at the fan-in.
		return "context_usage"
	case turnevent.ConversationReset:
		// The variant NAME only, for the arms above's reason. NewConversationID is
		// claude-authored and names a transcript on the operator's own disk; it is not
		// returned here.
		//
		// THIS ARM IS THE INVERSE OF THE FIVE ABOVE, and the difference is the whole
		// reason it exists. Each of those records that its variant is now claimed by a
		// Handle case, so this file's `interactive_turn.unknown` Debug is no longer
		// live for it. This variant has NO Handle case, deliberately (#2134): the
		// boundary a client draws comes from the session_transition frame, so the
		// event owes no wire shape, and turnbridge.MapEvent's default drops it. So the
		// Debug in Handle's own default IS the reachable call site here — not the
		// no-cursor drop, which the arms above have to fall back on — and without this
		// arm it would read kind=unknown for a variant the daemon does recognize.
		//
		// Do not "fix" that by adding a Handle case. The Debug being reachable for
		// this variant is the behaviour #2134's AC4 tests.
		return "conversation_reset"
	default:
		return "unknown"
	}
}

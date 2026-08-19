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
// push (#571). Distinct from #589's v2Broadcaster, which uses the
// capability-agnostic ActiveConnIDs. *relay.V2SessionManager satisfies it.
// Declared at the consumer (CODING-STYLE) so the emitter unit-tests drive it
// without a real manager.
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

	// nextID is the session-monotonic envelope-ID counter. It is NEVER reset
	// across turns (mirrors #589's policy; the basis for #611 mid-turn-reconnect
	// resync). nextID++ runs per conn per envelope, so the same logical envelope
	// gets a distinct env.ID on each conn — each conn still sees a strictly
	// increasing subsequence.
	nextID uint64

	// ring stores every fanned-out event under a durable, connection-independent
	// per-conversation id for the #647 reconnect-replay path. It is owned here
	// (created in the constructor, daemon-resident since the emitter is built
	// once in startInteractiveTurnStreamV2 — codebase/633.md), distinct from the
	// per-conn envelope nextID above (NOT overloaded). The ring is self-
	// synchronised: it is the one field a second goroutine (the future query
	// path) may touch, so the emitter's other fields stay unguarded and
	// single-goroutine. Appended on emit, before the per-conn fan-out.
	ring *eventring.Ring

	// Delta-coalescing state (#609) — read/written only on the single Handle/
	// flush goroutine, same contract as the lifecycle fields above. The invariant
	// the timer relies on: flushTimer is armed iff deltaBuf is non-empty.
	deltaBuf    strings.Builder // accumulated assistant text for the open (un-flushed) delta
	deltaMsgID  string          // MessageID of the buffered text; meaningful only while deltaBuf.Len() > 0
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
		// Message-boundary flush: a new JSONL message id ends the prior delta
		// before this chunk opens a fresh one (per-JSONL-message batching).
		if e.deltaBuf.Len() > 0 && v.MessageID != e.deltaMsgID {
			e.flushDelta(ctx)
		}
		// Safe before buffering: during a text run the state is already
		// responding, so this is a no-op and never emits a turn_state ahead of
		// buffered text; it only emits on the first content of a turn, when the
		// buffer is necessarily empty.
		e.transitionTo(ctx, convID, turnbridge.StateResponding)
		wasEmpty := e.deltaBuf.Len() == 0
		e.deltaConvID = convID
		e.deltaMsgID = v.MessageID
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
	case turnevent.BackgroundTaskStarted, turnevent.BackgroundTaskUpdated, turnevent.BackgroundTaskRoster:
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
		e.flushDelta(ctx)
		e.emitMapped(ctx, convID, ev)
	case turnevent.ThinkingProgress:
		// claude's mid-turn proof of life (#1386), taking the same shape as the
		// status peers above: NO turn-lifecycle mutation (no startTurnIfNeeded /
		// transitionTo / endTurn; inTurn, turnID, currentState untouched).
		//
		// The reason is specific to this variant. Thinking progress is a READING,
		// not a state transition — the turn's thinking state is already reported
		// by turn_state: thinking, driven by ThoughtChunk above. Opening a turn
		// here would be worse than redundant: the parser emits these during an
		// inference request that may not have produced any assistant content yet,
		// so a turn opened on one has no guaranteed end and would wedge the
		// conversation exactly as opening one on an unrecognized message would.
		//
		// Flush any pending delta first so buffered text keeps its wire position
		// ahead of the reading. Like turn_state these flow through emit() and are
		// NOT droppable deltas (the droppable set is assistant_delta only, #610),
		// so they hold queue slots; the producer's rate bound
		// (streamsup.minThinkingTokensPerEvent, one per 64 tokens of accumulated
		// delta) is what keeps the count to roughly 1–2 per typical turn, and no
		// second cap is imposed here.
		e.flushDelta(ctx)
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
		// emitRateLimit's gate fires at most once per run.
		e.flushDelta(ctx)
		e.emitMapped(ctx, convID, ev)
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

// endTurn closes the current turn. The next content/thought event re-mints a
// fresh turn id and resets seq/currentState via startTurnIfNeeded. It need not
// touch the delta buffer — the TurnEnd arm already flushed it, and the buffer
// is only ever non-empty inside an open turn.
func (e *interactiveTurnEmitterV2) endTurn() {
	e.inTurn = false
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
	text, convID, msgID := e.deltaBuf.String(), e.deltaConvID, e.deltaMsgID
	// Reconstruct a synthetic TextChunk per chunk and reuse emitMapped — no new
	// payload path. MapEvent reads only Text for the assistant_delta; MessageID
	// is the coalescing key, not a wire field.
	for _, chunk := range splitDeltaText(text, maxDeltaTextBytes) {
		e.emitMapped(ctx, convID, turnevent.TextChunk{
			MessageID: msgID,
			Text:      chunk,
		})
		// Inside the loop, after the emit: emitMapped reads e.seq off the
		// struct, so hoisting this either way gives every chunk the same seq or
		// shifts the whole sequence by one.
		e.seq++
	}
	e.deltaBuf.Reset()
	e.deltaMsgID = ""
	e.deltaConvID = ""
	e.flushTimer.Stop()
}

// emitMapped maps a content event to its wire envelope via the pure #627
// adapter and emits it. ok==false is defensive — unreachable for every variant
// Handle routes here, since only ThoughtChunk and a nil event drop in MapEvent
// and neither reaches this function.
func (e *interactiveTurnEmitterV2) emitMapped(ctx context.Context, convID string, ev turnevent.Event) {
	typ, payload, ok := turnbridge.MapEvent(ev, turnbridge.TurnContext{
		ConversationID: convID,
		TurnID:         e.turnID,
		Seq:            e.seq,
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

	// Assign the durable per-conversation event id and record it for replay
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
	case turnevent.TurnEnd:
		return "turn_end"
	case turnevent.Stall:
		return "stall"
	case turnevent.ApiRetry:
		return "api_retry"
	case turnevent.Compacting:
		return "compacting"
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
		// Unlike the two arms above, this variant has no Handle case at all — no wire
		// mapping exists yet (turnbridge.MapEvent's default drops it), so it lands in
		// Handle's own default and this file's `interactive_turn.unknown` Debug is a
		// live call site rather than only the ACP surface's. Without the arm every
		// eventKind site — here, acp_turn_stream.go, stream_turn_busy.go,
		// stream_turn_drain.go — would read kind=unknown for a variant the daemon does
		// recognize.
		return "model_announced"
	default:
		return "unknown"
	}
}

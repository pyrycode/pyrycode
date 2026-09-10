// Package turnbridge adapts the neutral internal turn-event model
// (internal/turnevent, #606) OUT to the v2 interactive wire payloads (#607):
// MapEvent shapes one turnevent.Event into a typed payload, and BuildTurnState
// shapes the turn_state payload the lifecycle machine drives. It is a pure
// value-to-value adapter — no I/O, no state, no envelope-ID minting, no clock
// read, no sealing. Every one of those belongs to the consumer (the
// turn-lifecycle integration slice); keeping them out is what makes this
// table-testable and isolates it from the lifecycle state machine. See
// cmd/pyry's interactiveTurnEmitterV2.emit for the consumer shape that wraps a
// payload into an Envelope.
package turnbridge

import (
	"bytes"
	"encoding/json"
	"sort"
	"unicode/utf8"

	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// TurnContext is the per-event turn addressing the consumer supplies to
// MapEvent. The adapter never derives these — which conversation / turn / seq
// applies to a given event is a turn-lifecycle decision owned by the consumer.
type TurnContext struct {
	ConversationID string
	TurnID         string
	// Seq is the per-turn assistant-delta ordering counter. It is consumed
	// ONLY by the TextChunk -> assistant_delta mapping and ignored for every
	// other event kind; the consumer advances it.
	Seq int
}

// TurnState is the coarse turn-lifecycle state BuildTurnState shapes into a
// turn_state payload. String-backed so the call site is enum-safe; the wire
// field itself stays a plain string (#607).
type TurnState string

const (
	StateThinking   TurnState = "thinking"
	StateResponding TurnState = "responding"
	StateIdle       TurnState = "idle"
)

// maxSummaryLen bounds the INPUT précis to a single line of at most this many
// runes. A phone-display bound, not a wire constraint (the envelope cap is far
// larger); tunable if the mobile view wants a different cap. It stopped bounding
// the RESULT précis in #1680 — that side has its own, much larger cap in
// maxResultSummaryRunes below, which IS a wire constraint.
const maxSummaryLen = 200

// The bounds on the per-field tool input inputFields extracts (#1678). These
// are WIRE constraints and deliberately invert maxSummaryLen's framing above:
// the whole point of sending fields rather than one précis is that the values
// are large, so together they are what keeps a tool_use inside the v2
// application-envelope cap of 65519 bytes (docs/protocol-mobile.md
// § Application-envelope size cap). Separate from maxSummaryLen because
// input_summary keeps its meaning, its value and its own cap unchanged.
//
// The arithmetic, in maxDeltaTextBytes's form (cmd/pyry). encoding/json has
// SetEscapeHTML on by default, so '<', '>', '&' and every control byte without
// a short escape each cost six bytes on the wire. The cut here is a RUNE cut
// and six bytes per rune is still the ceiling: a 1-byte rune escapes to at most
// 6, a multi-byte rune is emitted RAW at 4 bytes or fewer, U+2028/U+2029 escape
// to 6 from 3 input bytes, and an invalid byte becomes U+FFFD (6 bytes on the
// wire) while utf8.RuneCountInString counts it as one rune. So
// maxInputTotalRunes x 6 = 51000 B is the map's content ceiling. The bound is
// on PRE-ellipsis content: each admitted value may add one "…" beyond its cap,
// exactly as inputSummary's does, so at most maxInputFields further runes ride
// on top.
//
// Measured worst case, filling every value with '<' and the fields the producer
// does NOT bound as hostilely as they could plausibly arrive: 56618 B, 86.4% of
// the cap, with roughly 8.9 KB of headroom. The invariant is ENFORCED by
// protocol's TestToolUsePayload_FitV2EnvelopeCap; if that ever fails, LOWER
// these constants — never raise them. The conservative constants are the belt;
// the deterministic per-frame cap test is the suspenders (different fabric).
//
// tool_use and tool_result are separate envelopes, so #1680's result cap does
// not ride this frame and the two budgets do not sum.
const (
	// maxInputValueRunes bounds ONE input value. 4000 is measured, not guessed:
	// across 11336 claude tool calls carrying a structured input, 97% arrive
	// completely intact at this cap and the average call carries 547 characters
	// against the hard 200 input_summary carries. 8000 buys one further
	// percentage point and doubles the worst case (#1678).
	maxInputValueRunes = 4000

	// maxInputKeyRunes bounds one input KEY. Real keys are file_path, command,
	// pattern — this is generous and exists only so one pathological key cannot
	// eat the whole budget. An over-long key drops its entry rather than being
	// truncated: a cut key is a false claim about the input's field NAME, where a
	// cut value is honestly marked with "…".
	maxInputKeyRunes = 128

	// maxInputFields bounds the ENTRY COUNT. The observed maximum is 6 (mean
	// 2.3). Not redundant with the rune budget below: per-entry JSON structure
	// (the quotes, colon and comma of `"":"",`) is bytes a CONTENT budget cannot
	// see, so without this a map of many tiny entries could out-cost its own
	// content.
	maxInputFields = 16

	// maxInputTotalRunes bounds keys and values SUMMED, because per-value caps do
	// not compose into an envelope guarantee on their own — three full 4000-rune
	// values on one Edit would reach 72000 B escaped, over the cap before the
	// payload's other fields are counted. 8500 does not bind on measured traffic:
	// the worst call in the whole corpus is 8147 characters, which the per-value
	// cap reduces to roughly 8090 before this bound is consulted. That margin is
	// why it is not lower.
	//
	// It is a MEMORY knob as well as a wire knob, which is the second reason for
	// the never-raise rule above: internal/eventring retains up to
	// MaxEventsPerConversation events per conversation and preferentially keeps
	// control-class events, of which tool_use is one, so this constant multiplies
	// the ring's worst-case per-conversation footprint (#1678 § Security review).
	maxInputTotalRunes = 8500
)

// maxResultSummaryRunes bounds the tool-result précis resultSummary derives
// (#1680). Like the input-field caps above and unlike maxSummaryLen, this is a
// WIRE constraint: the live producer (internal/streamsup's toolResultContent)
// returns claude's result text VERBATIM with no cap of its own, so this constant
// is the only thing standing between an arbitrarily large tool result and the v2
// application-envelope cap of 65519 bytes (docs/protocol-mobile.md
// § Application-envelope size cap). Separate from maxSummaryLen because
// input_summary keeps its meaning, its value and its own cap unchanged, and
// because results are an order of magnitude bigger than inputs: measured across
// 11379 local tool results, mean 2959 characters and median 721, of which only
// 22% survive a 200-rune cap whole.
//
// 10000 is maxDeltaTextBytes's number (cmd/pyry) on purpose. That is the one
// other free-text field on a v2 envelope and it solved this same problem, so a
// different number for the same envelope would invite the two to drift. It
// carries at least 90% of measured results whole, against 22% today. 16000 was
// MEASURED at 96309 B — 47% over the cap — and must not be re-proposed: exceeding
// the cap does not truncate a frame, it LOSES it (nothing on relay's push path
// bounds the plaintext, so an oversized frame fails at the AEAD or is rejected by
// the phone's own decode cap), and tool_result is never-droppable control class,
// so the operator would see an empty row rather than a shortened one.
//
// The arithmetic, in maxDeltaTextBytes's form. encoding/json has SetEscapeHTML on
// by default, so '<', '>', '&' and every control byte without a short escape each
// cost six bytes on the wire. The cut here is a RUNE cut and six bytes per rune is
// still the ceiling: a 1-byte rune escapes to at most 6, a multi-byte rune is
// emitted RAW at 4 bytes or fewer, U+2028/U+2029 escape to 6 from 3 input bytes,
// and an invalid byte becomes U+FFFD (6 bytes on the wire) while
// utf8.RuneCountInString counts it as one rune. So 10000 x 6 = 60000 B is the
// content ceiling. The bound is on PRE-ellipsis content, exactly as the input caps
// above are: one "…" rides on top, costing 3 raw bytes rather than 6 because
// U+2026 is emitted unescaped.
//
// Measured worst case: 61363 B, 93.7% of the cap, with roughly 4.2 KB of headroom
// (the cap test's number, one byte above the spec's 61362 because "is_error":false
// costs one more than true and the field is never omitted).
// That headroom rests on an ASSUMPTION rather than on an enforced cap, which is
// worth saying out loud — nothing bounds the three identity fields.
// conversation_id and turn_id are daemon-supplied; tool_use_id is claude's
// block.ToolUseID passed through verbatim by streamsup's emitUser, as name and
// block.ID are on the tool_use side. The measurement therefore fills all three at
// 64 runes, roughly 11x the longest observed. Capping them upstream is a separate
// ticket (#1678 § Open questions, inherited by #1680).
//
// The invariant is ENFORCED by TestToolResultPayload_FitV2EnvelopeCap, which
// drives this helper rather than a hand-built payload so the constant is what the
// measurement stands on; if it ever fails, LOWER this constant — never raise it.
// The conservative constant is the belt; the deterministic per-frame cap test is
// the suspenders (different fabric). The second reason for the never-raise rule is
// MEMORY: internal/eventring retains up to MaxEventsPerConversation (1024) events
// per conversation and preferentially keeps control-class events, of which
// tool_result is one, holding the marshalled payload bytes — so this constant
// multiplies the ring's worst-case per-conversation footprint, from ~1.4 MiB at
// the old bound to ~59 MiB at this one (#1680 § Security review).
//
// tool_use and tool_result are separate envelopes, so the input budget above does
// not ride this frame and the two do not sum.
const maxResultSummaryRunes = 10000

// maxSlashCommandListBytes bounds the SERIALISED `commands` array of a
// slash_command_list payload — the bytes that array actually costs on the wire,
// its brackets and separators included — so the marshalled protocol.Envelope
// stays under the 65519-byte v2 application-envelope cap (docs/protocol-mobile.md
// § Application-envelope size cap). Like maxDeltaTextBytes (cmd/pyry) it bounds
// the BOUNDED FIELD and not the envelope: do not "correct" it towards 65519, and
// do not confuse it with outbound_test.go's maxV2AppEnvelope, which IS that cap.
//
// WHAT IT DEFENDS AGAINST, and this is the frame where the producer's caps compose
// the wrong way. streamsup bounds every CONTENT dimension —
// maxSlashCommandListEntries at 128 entries over a per-entry term of
// maxSlashCommandName + maxSlashCommandArgumentHint + maxSlashCommandDescription +
// maxSlashCommandAliasCount * maxSlashCommandAlias = 256 + 256 + 256 + 8 * 64 =
// 1280 bytes — and 128 * 1280 = 163,840 is 2.5x the cap before anything else is
// counted. THAT FIGURE IS RAW-BYTE ARITHMETIC, NOT A WIRE MEASUREMENT: encoding/json
// has SetEscapeHTML on by default, so '<', '>', '&' and U+2028/U+2029 each cost six
// bytes, and the true worst case on the wire is up to ~6x higher again. The
// conclusion is the same either way — NO COUNT CAP CLOSES THE GAP, and
// maxSlashCommandListEntries' own doc carries that derivation: the only count whose
// worst case fits is 51, which is the committed capture's own size, so a
// worst-case-derived count cap fires on claude's ordinary output. The cut is
// therefore MEASURED against marshalled bytes rather than counted.
//
// THE RESERVE is 65519 - 64000 = 1519 B, for everything outside `commands`: the
// payload's own conversation_id and dropped_commands keys with their punctuation,
// and the envelope's id, type, ts, payload and event_id frame.
// TestSlashCommandListPayload_FitV2EnvelopeCap measures that reserve at 567 B with
// a HOSTILE 64-rune conversation_id — hostile because nothing in this package
// bounds that field, maxResultSummaryRunes' stated assumption unchanged — so 1519
// is roughly 2.7x an already-pessimistic worst case, and the measured worst-case
// envelope is 63224 B, 96.5% of the cap. That is tighter than the sibling
// constants' margins on purpose: what those reserve headroom against is an
// UNBOUNDED input field, where here the only unbounded contributor left outside
// the measurement is conversation_id, and 952 B of spare would take another 158
// escaped runes of it to spend.
//
// ITS BRANCH IS UNREACHABLE ON CLAUDE'S ORDINARY OUTPUT, which is what makes it a
// bound rather than dead code, and it is the property to preserve if the number
// ever moves. The committed capture's 51-entry menu serialises to about 11,403
// bytes DAEMON-SIDE — the figure taken after the producer's field caps and with
// Go's escaping, not the raw-claude 14,277 for the same array — roughly 18% of this
// budget. What the bound answers is the workspace that capture is not: a
// command-heavy repository whose descriptions are markup-dense, where a menu that
// would today be lost whole arrives truncated and counted instead.
//
// The invariant is ENFORCED by TestSlashCommandListPayload_FitV2EnvelopeCap; if
// that ever fails, LOWER this constant — never raise it. The conservative constant
// is the belt; the deterministic per-frame cap test is the suspenders (different
// fabric), maxDeltaTextBytes' pairing unchanged.
const maxSlashCommandListBytes = 64000

// MapEvent maps one neutral turnevent.Event plus explicit turn context to the
// matching v2 interactive wire payload and its envelope type discriminant.
//
// ok is false for events with no wire representation: ThoughtChunk (ADR 025 —
// #607 defines no thought-text envelope; thinking surfaces as a turn_state
// transition the consumer drives via BuildTurnState, and the thought text is
// NOT forwarded) and any nil/unknown Event. The consumer drops + debug-logs
// those. Pure; safe on a zero-value Event.
//
// payload is one of the protocol.*Payload value structs, or nil when ok is
// false. It is any because the payloads share no marker interface; the
// consumer json.Marshals it directly (same path as protocol.MessagePayload).
// The consumer owns the envelope ID, TS, marshal, and seal — none happen here.
func MapEvent(ev turnevent.Event, tc TurnContext) (typ string, payload any, ok bool) {
	switch e := ev.(type) {
	case turnevent.TextChunk:
		return protocol.TypeAssistantDelta, protocol.AssistantDeltaPayload{
			ConversationID:  tc.ConversationID,
			TurnID:          tc.TurnID,
			Seq:             tc.Seq,
			ParentToolUseID: e.ParentToolCallID,
			Text:            e.Text,
		}, true
	case turnevent.ToolStart:
		// RawInput is read TWICE and the two readings are independent by design.
		// InputSummary keeps its exact meaning, value and cap (#1678 AC 1) — it is
		// what mobile parses today and the whole-input fallback when the field map
		// drops something — while Input is the same blob's own top-level fields,
		// each bounded on its own so an Edit's file_path survives beside the
		// replaced text instead of being buried inside it. Neither derives from
		// the other: a summary computed FROM the capped fields would silently
		// change input_summary's value, which is the one thing this ticket must
		// not do.
		//
		// A nil Input is FORWARDED, not pre-allocated: absent, empty and
		// not-an-object inputs all yield nil here and
		// ToolUsePayload.MarshalJSON (#1678) normalises it to "input":{} on the
		// wire. Allocating an empty map here would produce the same bytes while
		// hiding which layer owns the decision — the BackgroundTaskRoster arm
		// below states the same rule for the same reason.
		// ParentToolUseID crosses VERBATIM and is not re-capped, this file's standing
		// terms: streamsup's parentToolUseID bounds it at CONSTRUCTION, where every cap
		// in that package is applied, so a second bound here would be a number to keep
		// in step with one that already holds. The ONE transformation is the name —
		// turnevent spells it ParentToolCallID after its own ToolCallID, and this frame
		// carries it as parent_tool_use_id, matching the tool_use_id beside it. That is
		// the ToolCallDenied arm below's rule: a token names a key the frame publishes.
		return protocol.TypeToolUse, protocol.ToolUsePayload{
			ConversationID:  tc.ConversationID,
			TurnID:          tc.TurnID,
			ToolUseID:       e.ToolCallID,
			ParentToolUseID: e.ParentToolCallID,
			Name:            e.Title,
			InputSummary:    inputSummary(e.RawInput),
			Input:           inputFields(e.RawInput),
		}, true
	case turnevent.ToolUpdate:
		// ResultDetail maps straight through, deliberately UNCAPPED here, and
		// that asymmetry with ResultSummary beside it is the point. resultSummary
		// needs maxResultSummaryRunes because its producer (streamsup's
		// toolResultContent) returns claude's text verbatim with no bound of its
		// own. ResultDetail's producer (streamsup's toolResultDetail) formats
		// decoded int64s and its own literals across five shapes, so it is
		// bounded at CONSTRUCTION — where every cap in that package is applied —
		// at 48 bytes, and carries no claude-supplied byte to cap. Since #2025
		// two of those literals are multi-byte (U+2212, U+00B7); encoding/json
		// escapes neither, and both are already counted in the 48. A second cap
		// here would be a number to keep correct against a string that cannot
		// grow — and, now that the alphabet is no longer pure ASCII, a rune cap
		// that could split one of those two glyphs for no reason.
		// ParentToolUseID: the ToolStart arm above states the pass-through and the
		// rename, and both hold unchanged here.
		return protocol.TypeToolResult, protocol.ToolResultPayload{
			ConversationID:  tc.ConversationID,
			TurnID:          tc.TurnID,
			ToolUseID:       e.ToolCallID,
			ParentToolUseID: e.ParentToolCallID,
			IsError:         e.Status == turnevent.ToolStatusFailed,
			ResultSummary:   resultSummary(e.Content),
			ResultDetail:    e.ResultDetail,
		}, true
	case turnevent.ToolProgress:
		return protocol.TypeToolProgress, protocol.ToolProgressPayload{
			ConversationID: tc.ConversationID,
			TurnID:         tc.TurnID,
			ToolUseID:      e.ToolCallID,
			ElapsedSeconds: e.ElapsedSeconds,
		}, true
	case turnevent.ToolCallDenied:
		// TURN-SCOPED, unlike the status peers below: tc.TurnID is carried, and only
		// tc.Seq is ignored. A denial is not a mark in the conversation's history — it
		// names ONE tool call the client has already seen a tool_use for, and the
		// captured line order puts it strictly inside the turn that made the call
		// (assistant/tool_use → system/permission_denied → user/tool_result). A client
		// joins the three frames on ToolUseID, so they have to agree on the turn too.
		//
		// Every string crosses VERBATIM and nothing is re-capped: the producer bounded
		// all five at CONSTRUCTION (streamsup's maxTaskFieldID / maxDenialProse), so a
		// second bound here would be a number to keep in step with one that already
		// holds — the standing terms of every arm in this file.
		//
		// The ONE transformation is the id report token. turnevent.ToolCallDenied names
		// that field tool_call_id, its own field name; this frame carries it as
		// tool_use_id, the name the tool_use and tool_result frames already use. A token
		// naming a key the frame does not carry is one a client cannot look up, so the
		// vocabulary is translated here rather than published broken.
		return protocol.TypeToolDenied, protocol.ToolDeniedPayload{
			ConversationID:     tc.ConversationID,
			TurnID:             tc.TurnID,
			ToolUseID:          e.ToolCallID,
			ToolName:           e.ToolName,
			DecisionReasonType: e.DecisionReasonType,
			DecisionReason:     e.DecisionReason,
			Message:            e.Message,
			TruncatedFields:    deniedReportKeys(e.TruncatedFields),
			DroppedFields:      deniedReportKeys(e.DroppedFields),
		}, true
	case turnevent.TurnEnd:
		// The three stop-shape fields map straight through, deliberately UNCAPPED
		// here — ToolUpdate's ResultDetail arm above argues the shape of this
		// choice. Both strings are bounded at CONSTRUCTION by their producer
		// (streamsup's maxTurnEndStopField), where every cap in that package is
		// applied, so a second bound here would be a number to keep in step with
		// one that already holds. The window fields on the same variant are NOT
		// mapped, which stays true: this arm builds the payload field by field, so
		// what reaches the wire is what is named here and nothing else.
		return protocol.TypeTurnEnd, protocol.TurnEndPayload{
			ConversationID: tc.ConversationID,
			TurnID:         tc.TurnID,
			// Unchanged and unchanged-by-construction: still the daemon's own
			// two-value classification, never claude's `stop_reason` key.
			StopReason:     string(e.Reason),
			Outcome:        e.Outcome,
			IsError:        e.IsError,
			TerminalReason: e.TerminalReason,
			// #2224's category maps through on the same terms, and its producer bounded
			// it at the same construction site with the same constant. That it was read
			// off an `assistant` line rather than the `result` line changes nothing
			// here: this arm maps an already-built event and knows nothing about which
			// line any field came from.
			ErrorCategory: e.ErrorCategory,
			// #2260's four numbers, mapped straight through and UNDIFFERENCED. Two of
			// them are running totals, and converting one into a per-turn delta is the
			// mistake this arm is structurally unable to make: it maps a single event
			// and holds no previous turn's value. Uncapped here on the same terms as
			// the strings above, and for a stronger reason — those are bounded at
			// construction by a constant this file would have to keep in step, whereas
			// these are bounded by their Go types for every input claude can send.
			DurationMS:          e.DurationMS,
			DurationAPIMS:       e.DurationAPIMS,
			NumTurns:            e.NumTurns,
			CostUSDTotal:        e.CostUSDTotal,
			InputTokens:         e.InputTokens,
			OutputTokens:        e.OutputTokens,
			CacheReadTokens:     e.CacheReadTokens,
			CacheCreationTokens: e.CacheCreationTokens,
		}, true
	case turnevent.Stall:
		// A stall carries conversation identity only — it is not turn-scoped and
		// not a delta, so tc.TurnID and tc.Seq are ignored (as BuildTurnState
		// ignores them).
		return protocol.TypeStall, protocol.StallPayload{
			ConversationID: tc.ConversationID,
		}, true
	case turnevent.ApiRetry:
		// Like Stall, a status peer carries conversation identity only —
		// tc.TurnID and tc.Seq are ignored (not turn-scoped, not a delta). The
		// counter rides across only as the two bounded ints.
		return protocol.TypeApiRetry, protocol.ApiRetryPayload{
			ConversationID: tc.ConversationID,
			Active:         e.Active,
			Current:        e.Current,
			Total:          e.Total,
		}, true
	case turnevent.Compacting:
		// Like Stall and ApiRetry, a status peer carries conversation identity only —
		// tc.TurnID and tc.Seq are ignored (not turn-scoped, not a delta).
		//
		// The two claude-authored strings (#2236) map straight through, deliberately
		// UNCAPPED here on the TurnEnd arm's stated terms: streamsup bounds both at
		// construction with maxCompactField, so a second bound in this adapter would be
		// a number to keep in step with one that already holds. Both are empty on every
		// rising edge and on the turn-boundary reset, and this arm neither fills nor
		// clears them — it maps what the event holds, which is what makes the producer
		// the single place either value is decided.
		return protocol.TypeCompacting, protocol.CompactingPayload{
			ConversationID: tc.ConversationID,
			Active:         e.Active,
			Result:         e.Result,
			ErrorText:      e.ErrorText,
		}, true
	case turnevent.CompactionBoundary:
		// Conversation identity only, like the status peers above: tc.TurnID and tc.Seq
		// are ignored and the payload has no field for either. The reason is specific to
		// this variant rather than borrowed. A compaction boundary is a mark in the
		// CONVERSATION's history — it says why claude no longer remembers something from
		// before that point — and the producer emits it for a boundary line that followed
		// no compacting edge at all, so it can legitimately arrive with no turn open.
		//
		// Every field crosses VERBATIM and nothing is invented, filtered or defaulted.
		// Trigger is bounded at CONSTRUCTION by the producer (streamsup's
		// maxCompactTrigger, which DROPS rather than cuts), so a second bound here would
		// be a number to keep in step with one that already holds — the Compacting arm's
		// stated terms. Neither count is clamped, range-checked or ordered: they are
		// claude's numbers, not the daemon's, exactly as the RateLimited arm below says of
		// ResetsAt, and a value the producer could not decode produced no event to map.
		//
		// THE TWO POINTERS CROSS AS POINTERS, which is what carries claude's presence
		// rather than collapsing it — a nil PostTokens means claude stated no such count
		// and must not become a zero. They are not deep-copied: the producer allocates a
		// fresh int per line, retains neither, and nothing downstream mutates a payload,
		// so the aliasing is observable to nobody and a defensive copy would only obscure
		// that.
		return protocol.TypeCompactionBoundary, protocol.CompactionBoundaryPayload{
			ConversationID: tc.ConversationID,
			Trigger:        e.Trigger,
			PreTokens:      e.PreTokens,
			PostTokens:     e.PostTokens,
		}, true
	case turnevent.Banner:
		// Conversation identity only, like the status peers above: tc.TurnID and tc.Seq
		// are ignored and the payload has no field for either. The reason belongs to the
		// PRODUCER SET rather than being borrowed from a neighbour. A prompt a hook
		// refuses is never answered, so no turn exists to attribute the refusal to; a
		// notification belongs to claude's own queue and rides no turn at all. There is
		// no turn this adapter could honestly name.
		//
		// ALL FOUR claude-SIDE VALUES CROSS VERBATIM. Nothing here bounds, drops, cuts,
		// defaults or recomputes: Text is bounded at CONSTRUCTION by the producer (#2257,
		// at 4 KiB) and Level dropped there when over-long, so a second bound in this
		// adapter would be a number to keep in step with one that already holds — the
		// Compacting and CompactionBoundary arms' stated terms. An unrecognised Level is
		// not an error and is not normalised: TurnEnd.Outcome's open-set rule.
		//
		// IN PARTICULAR Truncated IS NOT RECOMPUTED from len(Text), which is the one
		// mistake this arm could plausibly make and the one the bridge test's long-text
		// row exists to kill. The producer decided whether it cut; a length comparison
		// here would be a SECOND authority on that fact, free to disagree with the first
		// — and it would be wrong by construction the moment the producer's cap changes,
		// since this package does not hold the constant.
		//
		// Every field is a value type, so unlike the CompactionBoundary arm's pointers
		// and the ToolCallDenied arm's slices there is nothing to alias and no copy to
		// justify.
		return protocol.TypeBanner, protocol.BannerPayload{
			ConversationID: tc.ConversationID,
			Level:          e.Level,
			Text:           e.Text,
			Truncated:      e.Truncated,
			StopsTurn:      e.StopsTurn,
		}, true
	case turnevent.Unrecognized:
		// Conversation identity only, like Stall and Compacting above: tc.TurnID
		// and tc.Seq are ignored. This one is not merely "not turn-scoped" — an
		// unrecognized message has no turn we can HONESTLY attribute it to, since
		// we could not parse it well enough to know what it belongs to. Raw is
		// already truncated by the producer; this adapter is pure and re-caps
		// nothing.
		return protocol.TypeUnrecognizedMessage, protocol.UnrecognizedMessagePayload{
			ConversationID: tc.ConversationID,
			Site:           string(e.Site),
			MessageType:    e.Kind,
			Raw:            e.Raw,
			Truncated:      e.Truncated,
		}, true
	case turnevent.BackgroundTaskStarted:
		// Conversation identity only, like the status peers above: tc.TurnID and
		// tc.Seq are ignored, and the payload has no field for either. A
		// background task OUTLIVES the turn that spawned it — that is the whole
		// #1240 point — so attributing it to a turn would be a claim we cannot
		// honestly make. Every string crosses verbatim: the producer bounded them
		// at construction (streamsup's maxTaskFieldID / maxTaskDescription) and
		// this adapter is pure and re-caps nothing. TruncatedFields rides along
		// because a payload that dropped it would present claude's cut text to a
		// phone as complete.
		return protocol.TypeBackgroundTaskStarted, protocol.BackgroundTaskStartedPayload{
			ConversationID:  tc.ConversationID,
			TaskID:          e.TaskID,
			ToolCallID:      e.ToolCallID,
			Description:     e.Description,
			TaskType:        e.TaskType,
			TruncatedFields: e.TruncatedFields,
		}, true
	case turnevent.BackgroundTaskUpdated:
		// The peer of BackgroundTaskStarted, same posture: not turn-scoped, so
		// tc.TurnID and tc.Seq are ignored. Patch crosses WHOLE and unparsed —
		// nothing here reads, validates, or re-serialises it, because a mapping
		// that enumerated known patch keys would silently discard every key
		// claude ships next.
		//
		// Two of claude's subtypes produce this event and they fill DISJOINT
		// fields (#2245) — Patch from task_updated, Status and Summary from
		// task_notification. This adapter does not care which: it copies all four
		// fields whatever their state, so an empty one crosses as empty and a
		// consumer reads a non-empty Status as "a terminal state was reported".
		// Branching here on which subtype produced the event would put a second
		// copy of that rule in a place with no access to the answer.
		//
		// Every string crosses verbatim: the producer bounded them at construction
		// (streamsup's maxTaskFieldID / maxTaskPatch / maxTaskSummary) and this
		// adapter is pure and re-caps nothing.
		return protocol.TypeBackgroundTaskUpdated, protocol.BackgroundTaskUpdatedPayload{
			ConversationID:  tc.ConversationID,
			TaskID:          e.TaskID,
			Patch:           e.Patch,
			Status:          e.Status,
			Summary:         e.Summary,
			TruncatedFields: e.TruncatedFields,
		}, true
	case turnevent.BackgroundTaskRoster:
		// The aggregate peer, same posture again: not turn-scoped, tc.TurnID and
		// tc.Seq ignored.
		//
		// A nil Tasks is FORWARDED, not filtered: an empty roster says nothing is
		// alive, which is precisely the reassurance #1240's symptom needs, so
		// suppressing it would delete the payoff of the whole feature. The nil is
		// passed straight through and BackgroundTaskRosterPayload.MarshalJSON
		// (#1393) normalises it to "tasks":[] on the wire — deliberately not
		// pre-allocated here, which would produce the same bytes while hiding the
		// normalisation that type owns.
		//
		// DroppedTasks is the roster's ONLY truncation report — it is not called
		// truncated_fields and the payload has no top-level field of that name —
		// so losing it would tell a phone that a capped roster is the whole
		// roster.
		var tasks []protocol.BackgroundTask
		for _, t := range e.Tasks {
			tasks = append(tasks, protocol.BackgroundTask{
				TaskID:          t.TaskID,
				TaskType:        t.TaskType,
				Description:     t.Description,
				TruncatedFields: t.TruncatedFields,
			})
		}
		return protocol.TypeBackgroundTaskRoster, protocol.BackgroundTaskRosterPayload{
			ConversationID: tc.ConversationID,
			Tasks:          tasks,
			DroppedTasks:   e.DroppedTasks,
		}, true
	case turnevent.BackgroundTaskProgress:
		// The fourth background-task frame, same posture as the three above: not
		// turn-scoped, so tc.TurnID and tc.Seq are ignored and the payload has no field
		// for either. A background task's work outlives the turn that spawned it.
		//
		// Every field crosses VERBATIM and nothing is invented: the producer bounded
		// the four strings at construction (streamsup's maxTaskFieldID /
		// maxTaskDescription) and this adapter is pure and re-caps nothing. The three
		// integers cross including a zero — a task reporting no tokens or no elapsed
		// time is a legitimate reading, exactly as ThinkingProgress's {0,0} is.
		//
		// NO SUPPRESSION BRANCH, on the ThinkingProgress arm's rule and for its reason:
		// the producer's rate bound (streamsup.minTaskToolCallsPerEvent) already decides
		// which of claude's lines earn an event, and a second, differently-shaped filter
		// here would silently diverge from it. In particular nothing here compares this
		// event's counters against a previous one — this adapter keeps no state, and the
		// per-task memory that decides the rate lives in the producer where the bound is.
		//
		// TruncatedFields rides along because a payload that dropped it would present
		// claude's cut text to a phone as complete.
		return protocol.TypeBackgroundTaskProgress, protocol.BackgroundTaskProgressPayload{
			ConversationID:  tc.ConversationID,
			TaskID:          e.TaskID,
			Description:     e.Description,
			SubagentType:    e.SubagentType,
			LastToolName:    e.LastToolName,
			TotalTokens:     e.TotalTokens,
			ToolUses:        e.ToolUses,
			DurationMS:      e.DurationMS,
			TruncatedFields: e.TruncatedFields,
		}, true
	case turnevent.ThinkingProgress:
		// Conversation identity only, like the status peers above: tc.TurnID and
		// tc.Seq are ignored and the payload has no field for either. It is a
		// periodic READING of an inference request in flight, not a turn-scoped
		// fact — claude restarts the cumulative count at every inference-request
		// boundary, so attributing a reading to a turn would misdescribe it.
		//
		// Both ints cross verbatim, including a zero-value {0,0}: that is a
		// legitimate reading exactly as ApiRetry's {0,0} counter is a legitimate
		// "count unknown". No suppression branch here — the producer's rate bound
		// (streamsup.minThinkingTokensPerEvent) already decides which of claude's
		// lines earn an event, and a second, differently-shaped filter in this
		// adapter would silently diverge from it.
		//
		// There is nothing to cap or truncate: the event carries no
		// claude-authored text at all, which is why it has no TruncatedFields to
		// carry across (see turnevent.ThinkingProgress).
		return protocol.TypeThinkingProgress, protocol.ThinkingProgressPayload{
			ConversationID:       tc.ConversationID,
			EstimatedTokens:      e.EstimatedTokens,
			EstimatedTokensDelta: e.EstimatedTokensDelta,
		}, true
	case turnevent.RateLimited:
		// Conversation identity only, like the status peers above: tc.TurnID and
		// tc.Seq are ignored and the payload has no field for either. A usage-limit
		// window is a condition of the ACCOUNT, not of the turn — claude reports it
		// once per run whatever the turn state — so attributing it to whichever turn
		// happened to observe it would misdescribe it.
		//
		// Every field crosses VERBATIM and nothing is invented: this is translation,
		// not policy. The decision "is this worth telling a person" was already made
		// upstream by emitRateLimit's four-rung gate, so a second, differently-shaped
		// filter here would silently diverge from the producer's — the hazard the
		// ThinkingProgress arm above names. That gate is a STATE MACHINE rather than a
		// per-line test since #2250: a benign status is silent on its own but IS
		// published when it follows a non-benign reading, which is the falling edge a
		// client clears a quota banner on. So the arm that looks most droppable here is
		// the one that must not be dropped, and a filter added here on the benign value
		// would delete the clear while leaving the warning — the exact defect #2250
		// fixed, restored one layer down. Hence no filtering, no defaulting, and
		// specifically:
		//
		//   - ResetsAt is NOT clamped or range-checked in either direction. It is
		//     claude's number, not the daemon's clock: not necessarily in the future,
		//     not necessarily in a sane range, and 0 means claude did not report it
		//     rather than "now" (turnevent.RateLimited.ResetsAt, and
		//     RateLimitedPayload's SECURITY paragraph).
		//   - Utilization is NOT clamped or range-checked either, and specifically not to
		//     0..1: it is claude's number, not a bounded fraction, so a negative or an
		//     above-one reading crosses exactly as a far-future ResetsAt does
		//     (turnevent.RateLimited.Utilization, and RateLimitedPayload's SECURITY
		//     paragraph, where both numbers are named together for the reason a reader
		//     would otherwise infer that the second one was checked).
		//   - THE POINTER CROSSES AS A POINTER, which is what keeps claude's presence
		//     rather than collapsing it — nil means claude stated no reading and must not
		//     become a 0, which on the wire is a FRESH window. That is the
		//     CompactionBoundary arm's rule above, and it is not deep-copied for that
		//     arm's three reasons: the producer allocates a fresh float64 per line,
		//     retains neither, and nothing downstream mutates a payload.
		//   - Neither string is re-capped. The producer bounded both at construction
		//     (streamsup's maxRateLimitField), following Unrecognized's precedent, so
		//     a second cap here would be a second place the limit is decided and the
		//     two could disagree silently. Utilization has no cap at all and wants none:
		//     a float64 cannot grow, which is why ResetsAt has none either.
		//   - A nil TruncatedFields is passed straight through, and HERE that nil is
		//     what puts "truncated_fields":null on the wire. The roster arm above
		//     looks identical and means the OPPOSITE: BackgroundTaskRosterPayload
		//     owns a nil→[] MarshalJSON, and RateLimitedPayload deliberately has none
		//     (protocol/interactive.go), because nothing-was-cut is an ABSENCE. A
		//     mapper that helpfully allocated an empty slice would put [] on the wire
		//     and tell a phone that claude's cut text is complete.
		//
		// Status is the one field that says WHY the report fired, and the producer's
		// gate is deliberately loud in that direction — any non-benign status emits,
		// so an unrecognised one surfaces and a human looks. Dropping it here would
		// silence that one layer later, and since #2250 it would also leave the
		// falling edge indistinguishable from the warning: the benign value IS the
		// client's discriminator for the clear, no daemon-computed flag marking it.
		return protocol.TypeRateLimited, protocol.RateLimitedPayload{
			ConversationID:  tc.ConversationID,
			Status:          e.Status,
			LimitType:       e.LimitType,
			ResetsAt:        e.ResetsAt,
			Utilization:     e.Utilization,
			TruncatedFields: e.TruncatedFields,
		}, true
	case turnevent.ModelAnnounced:
		// Conversation identity only, like the status peers above: tc.TurnID and
		// tc.Seq are ignored and the payload has no field for either. An announced
		// model is a property of the turn's CONFIGURATION, not a turn boundary —
		// claude emits its init line once per TURN, so the announcement rides along
		// with every turn rather than delimiting one, and cmd/pyry's
		// TestTurnMarkFor_TotalOverEveryVariant already pins the lifecycle answer as
		// turnMarkNone.
		//
		// Model crosses BYTE-FOR-BYTE: no lowercasing, no alias expansion, no
		// date-stamping, no family mapping, and no lookup against any published model
		// list. What claude actually announces is MEASURED, and why that argues for
		// carrying the value untouched rather than repairing it — including why a
		// lookup miss is ORDINARY rather than an error — is turnevent.ModelAnnounced's
		// Model doc, its single source of truth, not restated here. Specifically:
		//
		//   - It is NOT re-capped. The producer bounded it at construction (streamsup's
		//     maxModelField), following Unrecognized's precedent, so a second cap here
		//     would be a second place the limit is decided and the two could disagree
		//     silently. maxSummaryLen lives in THIS file and is not the applicable
		//     bound — it is the tool-précis cap, and reaching for it here is the
		//     specific mistake to avoid.
		//   - There is no charset check. internal/relay's validModel bounds a
		//     PHONE-supplied override and is deliberately a different rule; applying it
		//     here would reject identifiers claude legitimately announces.
		//   - Truncated crosses too, and is load-bearing: a payload that dropped it
		//     would present claude's cut text to a phone as complete.
		//
		// No suppression branch, not even on an empty Model. The gate that decides
		// whether an event exists at all is the producer's (it does not emit on an
		// empty model), so a second, differently-shaped filter here would silently
		// diverge from it — the hazard the ThinkingProgress and RateLimited arms above
		// both name.
		return protocol.TypeModelAnnounced, protocol.ModelAnnouncedPayload{
			ConversationID: tc.ConversationID,
			Model:          e.Model,
			Truncated:      e.Truncated,
		}, true
	case turnevent.ModelRefusalFallback:
		// Conversation identity only. The event carries no request or claude message
		// identity that can join it to the assistant-delta stream, so tc.TurnID and
		// tc.Seq have no honest destination. All published values cross verbatim and
		// are not re-capped; emitModelRefusalFallback bounded them at construction.
		// RefusalExplanation is deliberately excluded because Banner is the one
		// display string the wire publishes.
		return protocol.TypeModelRefusalFallback, protocol.ModelRefusalFallbackPayload{
			ConversationID:  tc.ConversationID,
			OriginalModel:   e.OriginalModel,
			FallbackModel:   e.FallbackModel,
			Scope:           e.Scope,
			RefusalCategory: e.RefusalCategory,
			Banner:          e.Banner,
			TruncatedFields: modelRefusalFallbackReportKeys(e.TruncatedFields),
			DroppedFields:   modelRefusalFallbackReportKeys(e.DroppedFields),
		}, true
	case turnevent.SessionFacts:
		// The OTHER half of the same system/init line the arm above maps, and it sits
		// here rather than anywhere else in this switch for that reason. Conversation
		// identity only, like the status peers: tc.TurnID and tc.Seq are ignored and the
		// payload has no field either could land in. A build and a posture are properties
		// of the CHILD RUN — the build cannot change under a running child at all, and
		// the posture changes only when someone changes it — so they are one step further
		// out than the neighbour's per-turn configuration, and further still from a turn
		// boundary. cmd/pyry's TestTurnMarkFor_TotalOverEveryVariant pins the lifecycle
		// answer as turnMarkNone.
		//
		// Both strings cross BYTE-FOR-BYTE. Why that is the right answer rather than a
		// lazy one — no version parsing, no posture allow-list, and why a value matching
		// no known release or mode is ORDINARY rather than an error — is
		// turnevent.SessionFacts' field docs, their single source of truth, not restated
		// here. Specifically, and each point is the arm above's, unchanged:
		//
		//   - NEITHER IS RE-CAPPED. The producer bounded both at construction (streamsup's
		//     maxClaudeVersionField and maxPermissionModeField), so a second cap here
		//     would be a second place the limit is decided and the two could disagree
		//     silently. maxSummaryLen lives in THIS file and is not the applicable bound —
		//     it is the tool-précis cap, and reaching for it here is the specific mistake
		//     to avoid.
		//   - There is no charset check on either. internal/relay's validModel bounds a
		//     PHONE-supplied override and is deliberately a different rule; the posture is
		//     the field where a membership check is most tempting and most wrong, since an
		//     allow-list would drop the first report of a posture nobody has heard of —
		//     the case an operator most needs to see.
		//   - A nil TruncatedFields is passed straight through, and HERE that nil is what
		//     puts "truncated_fields":null on the wire. This is the RateLimited arm's
		//     polarity and NOT the roster arm's: SessionFactsPayload owns no MarshalJSON,
		//     so nothing normalises the nil afterwards, and a mapper that helpfully
		//     allocated an empty slice would emit [] and tell a phone that claude's cut
		//     text is complete. The slice header is shared rather than deep-copied, for
		//     the CompactionBoundary arm's three reasons: streamsup's emitSessionFacts
		//     declares its accumulator fresh per call, retains no reference to it after
		//     emit, and nothing downstream mutates a payload.
		//
		// No suppression branch, not even on two empty strings. The producer emits when
		// EITHER fact is present, so a line naming only a posture arrives here with an
		// empty version — that is a report, not a defect, and a second, differently-shaped
		// filter here would silently diverge from the gate that already decided the event
		// exists. That is the hazard the ThinkingProgress, RateLimited and ModelAnnounced
		// arms above all name.
		//
		// Nothing here re-decides what the frame may carry, and it could not: the four
		// init-line keys naming the operator's filesystem and claude's own session
		// identity are absent from the producer's DECODE TARGET (streamsup's
		// systemInitLine), which is why this arm cannot leak one even by accident.
		return protocol.TypeSessionFacts, protocol.SessionFactsPayload{
			ConversationID:    tc.ConversationID,
			ClaudeCodeVersion: e.ClaudeCodeVersion,
			PermissionMode:    e.PermissionMode,
			TruncatedFields:   e.TruncatedFields,
		}, true
	case turnevent.ModelList:
		// Conversation identity only, like the status peers above: tc.TurnID and
		// tc.Seq are ignored and the payload has no field for either. It is not even
		// per-turn, let alone turn-scoped — one initialize exchange per child
		// produces one of these — and it opens and closes no turn: cmd/pyry's
		// turnMarkFor answers turnMarkNone by construction (whitelist opener set,
		// turnMarkNone default) and TestTurnMarkFor_TotalOverEveryVariant pins that
		// independently.
		//
		// Every field crosses 1:1 and VERBATIM. This is translation, not policy, and
		// nothing here is derived, defaulted or synthesised — ConversationID is the
		// only value the mapping supplies. Specifically:
		//
		//   - A nil Models is FORWARDED, not pre-allocated. ModelListPayload's
		//     MarshalJSON owns nil→[], and allocating here would produce the same
		//     bytes while hiding which layer owns the normalisation — the rule the
		//     ToolStart and BackgroundTaskRoster arms above both state.
		//   - EffortLevels crosses as the SLICE IT IS, nil left nil, and
		//     ModelOption's MarshalJSON normalises it to []. Same reason as Models,
		//     and note the hazard here is ONLY the layer-ownership one: an allocation
		//     would produce identical bytes.
		//   - TruncatedFields crosses as the slice it is too, and HERE the nil is
		//     load-bearing. ModelOption's MarshalJSON deliberately EXEMPTS this
		//     field, so nothing normalises it afterwards: nothing-was-cut is an
		//     ABSENCE and must reach the wire as null. A mapper that helpfully
		//     allocated an empty slice — or appended into a fresh one — would emit []
		//     and tell a phone that claude's cut text is complete. That is the
		//     RateLimited arm's rule above, unchanged. THE ASYMMETRY WITH
		//     EffortLevels ONE FIELD UP IS THE POINT, in both directions: two list
		//     fields of one struct, one normalised and one not, and a reader who
		//     takes that for an accident will "fix" whichever of them they meet
		//     second.
		//   - DroppedModels is CARRIED, never recomputed from len(models) and never a
		//     constant. It is the count the decode recorded when streamsup's
		//     maxModelListEntries fired: recomputing it from the payload's own rows
		//     yields the wrong number by construction, and shipping 0 tells a phone
		//     that a capped menu is the whole menu. BackgroundTaskRoster's
		//     DroppedTasks states the reason, unchanged.
		//   - SupportsAutoMode crosses verbatim, including a false. Absent, JSON null
		//     and an explicit false were already collapsed to false upstream by
		//     encoding/json and by turnevent.ModelOption's SupportsAutoMode, and it
		//     is a REPORT to a client's menu that nothing in the daemon may read as
		//     authorization.
		//
		// NOTHING IS RE-CAPPED, RE-ORDERED, CANONICALISED OR CHARSET-CHECKED. The
		// producer bounded all three of the list's dimensions at construction —
		// turnevent.ModelList's Models names them together — so a second cap here
		// would be a second place the limit is decided and the two could disagree
		// silently. maxSummaryLen and maxResultSummaryRunes live in THIS file and are
		// NOT applicable bounds; reaching for either is the specific mistake to
		// avoid. Entry order is claude's and so is effort-level order, the latter
		// measured non-alphabetical, so a sort is observable rather than harmless.
		// internal/relay's validModel and validEffort bound a PHONE-supplied inbound
		// value and are deliberately not applied here in either direction:
		// validEffort's enum is closed, so running claude's outbound list through it
		// would drop a level claude legitimately publishes.
		//
		// CARRY, NEVER MUTATE THROUGH. The loop copies slice HEADERS, so the payload
		// shares backing arrays with the event it was handed, and two facts make that
		// sharing safe only under this rule: cmd/pyry's sessionModelHold.Sink retains
		// the ModelList WITHOUT copying and forwards the same value, and
		// sessionModelHold.ModelList() is read on a relay-leg goroutine while the
		// parser's forwarder writes the hold. A sort, an in-place dedupe, a filter,
		// or an append into a slice the event owns would therefore corrupt the
		// session's retained menu across two goroutines — a data race, not merely a
		// correctness bug. ModelListPayload's MarshalJSON refuses to reach through
		// into p.Models[i] for exactly this reason and says so; this arm inherits the
		// rule one layer up, and a read-only loop building a fresh OUTER slice holds
		// it by construction.
		//
		// No suppression branch, not even on an empty Models. The gate that decides
		// whether the event exists at all is the producer's, and turnevent.ModelList's
		// Models documents it ("Never empty"), so a second, differently-shaped filter
		// here would silently diverge from it — the hazard the ThinkingProgress,
		// RateLimited and ModelAnnounced arms above each name. A zero-value ModelList
		// therefore maps, exactly as a zero-value ModelAnnounced does.
		var models []protocol.ModelOption
		for _, m := range e.Models {
			models = append(models, protocol.ModelOption{
				ResolvedModel:    m.ResolvedModel,
				Value:            m.Value,
				DisplayName:      m.DisplayName,
				EffortLevels:     m.EffortLevels,
				SupportsAutoMode: m.SupportsAutoMode,
				TruncatedFields:  m.TruncatedFields,
			})
		}
		return protocol.TypeModelList, protocol.ModelListPayload{
			ConversationID: tc.ConversationID,
			Models:         models,
			DroppedModels:  e.DroppedModels,
		}, true
	case turnevent.SlashCommandList:
		// The OTHER inventory the one initialize exchange carries, beside the
		// ModelList arm above: that one answers what can be SELECTED, this one what
		// can be INVOKED. Conversation identity only, for that arm's reason —
		// tc.TurnID and tc.Seq are ignored, the payload has no field for either, and
		// one initialize exchange per child produces one of these. It opens and
		// closes no turn: cmd/pyry's turnMarkFor answers turnMarkNone by
		// construction, and TestTurnMarkFor_TotalOverEveryVariant pins that
		// independently.
		//
		// Every field crosses 1:1 and VERBATIM. This is translation, not policy, and
		// nothing here is derived, defaulted or synthesised — ConversationID is the
		// only value the mapping supplies. Specifically:
		//
		//   - A nil Commands is FORWARDED, not pre-allocated.
		//     SlashCommandListPayload's MarshalJSON owns nil→[], and allocating here
		//     would produce the same bytes while hiding which layer owns the
		//     normalisation — the rule the ToolStart, BackgroundTaskRoster and
		//     ModelList arms above all state.
		//   - Aliases crosses as the SLICE IT IS, nil left nil, and
		//     protocol.SlashCommand's MarshalJSON normalises it to []. Same shape as
		//     Commands but NOT the same reason: that method's own doc argues the
		//     collapse from a measurement (claude never sends an empty alias array,
		//     so absent and empty are one reading), where the payload's argues that
		//     [] is a positive statement. Note the hazard here is ONLY the
		//     layer-ownership one: an allocation would produce identical bytes.
		//   - TruncatedFields crosses as the slice it is too, and HERE the nil is
		//     load-bearing. SlashCommand's MarshalJSON deliberately EXEMPTS this
		//     field, so nothing normalises it afterwards: nothing-was-cut is an
		//     ABSENCE and must reach the wire as null. A mapper that helpfully
		//     allocated an empty slice — or appended into a fresh one — would emit []
		//     and tell a client that claude's cut text is complete. That is the
		//     RateLimited and ModelList arms' rule above, unchanged. THE ASYMMETRY
		//     WITH Aliases ONE FIELD UP IS THE POINT, in both directions: two list
		//     fields of ONE struct, one normalised and one not, and a reader who takes
		//     that for an accident will "fix" whichever of them they meet second.
		//   - DroppedCommands is CARRIED and ADDED TO, never recomputed from
		//     len(commands) and never a constant. The base is the count the decode
		//     recorded when streamsup's maxSlashCommandListEntries fired, and the
		//     frame cut below adds whatever IT drops on top, so len(commands) +
		//     dropped_commands is the list's true size after BOTH cuts. Recomputing
		//     it from the payload's own rows yields the wrong number by construction,
		//     and shipping 0 tells a client that a capped menu is the whole menu.
		//     BackgroundTaskRoster's DroppedTasks states the reason, unchanged — with
		//     the one difference that this arm is itself a second cutter, where that
		//     one only carries its producer's number.
		//
		// NOTHING IS RE-CAPPED, RE-ORDERED, CANONICALISED OR CHARSET-CHECKED ALONG ANY
		// DIMENSION THE PRODUCER BOUNDS. The producer bounded every CONTENT dimension
		// at construction — turnevent.SlashCommandList's own doc names them together,
		// four field caps plus an alias-count cap plus an entry cap — so a second cap
		// on any of those here would be a second place that limit is decided and the
		// two could disagree silently. maxSummaryLen and maxResultSummaryRunes live in
		// THIS file and are NOT applicable bounds; reaching for either is the specific
		// mistake to avoid. Entry order is claude's and so is alias order, and NO
		// CHARSET ASSUMPTION BELONGS HERE: Name is not an identifier — one name in the
		// committed capture is __remote-workflow — and an alias is not a second entry,
		// so expanding one into a synthetic row would invent a command claude never
		// published.
		//
		// THE FRAME AXIS IS A DIFFERENT DIMENSION AND IT IS DECIDED HERE (#2002),
		// which is why the paragraph above is scoped to the producer's dimensions
		// rather than claiming this arm does nothing. No cap the producer applies
		// bounds how many BYTES the array costs — 128 entries at its 1280-byte
		// per-entry term is 163,840 raw, and Go's escaping puts the wire worst case
		// higher still — and this is the one place both wire consumers pass through
		// (#2003's live-lane emission and #2005's connect-time resolver), so one bound
		// here covers both where a bound at either would be that second place.
		// maxSlashCommandListBytes carries the arithmetic and the reserve.
		//
		// The cut is MEASURED rather than counted, for that constant's stated reason,
		// and what it measures is the marshalled protocol.SlashCommand — not the
		// turnevent one, since SlashCommand.MarshalJSON's nil-Aliases normalisation
		// and the JSON key names are both inside the bytes that actually cross. It
		// takes a PREFIX: entries go from the TAIL in claude's order, never reordered
		// and never hole-punched, so a client sees a shortened menu rather than one
		// with gaps, and a row that does not fit ENDS the walk rather than being
		// skipped over in favour of a smaller one behind it. A row that cannot be
		// marshalled at all ends it the same way and is counted as dropped — that is
		// unreachable, since encoding/json coerces invalid UTF-8 rather than rejecting
		// it, and it is fail-closed rather than a path invented for a reachable state:
		// an entry that cannot be measured cannot be admitted to a measured budget.
		// That branch builds no error value and writes no log line, because wrapping a
		// row's Name into one would put a workspace-authored string on the exact path
		// the paragraph below exists to keep it off.
		//
		// CARRY, NEVER MUTATE THROUGH, the ModelList arm's rule with its evidence one
		// ticket away rather than in the tree. The loop copies slice HEADERS, so the
		// payload shares Aliases and TruncatedFields backing arrays with the event it
		// was handed. There is no sessionModelHold analogue for this list today —
		// that retention is #2004's — so the reason the rule binds NOW is #2005,
		// which reads the mapped payload on a relay-leg goroutine. A sort, an
		// in-place dedupe, a filter, or an append into a slice the event owns would
		// therefore corrupt a retained menu across two goroutines. The frame cut holds
		// the rule for the same reason and by the same means — it builds a fresh outer
		// slice and STOPS EARLY, never reslicing or truncating e.Commands itself. A
		// read-only loop building a fresh OUTER slice holds the rule by construction,
		// and
		// SlashCommandListPayload's MarshalJSON refuses to reach through into
		// p.Commands[i] for the same reason and says so.
		//
		// EVERY STRING HERE IS WORKSPACE-AUTHORED, a lower-trust origin than claude's
		// own strings, and NONE OF THEM MAY REACH A LOG RECORD — so this arm writes
		// no log line at all, which is what cmd/pyry's eventKind arm for this variant
		// enforces on its own side by returning the variant NAME only. This slice
		// adds exactly ONE sink to the enumeration streamsup's maxSlashCommandName
		// owns: a field of a protocol.SlashCommandListPayload. No exec.Command
		// argument, no filepath.Join, no filepath.Match, no regexp, no log attribute.
		// The bytes are bounded but NOT sanitized, and the render boundary owing that
		// is the CLIENT's, as both types' SECURITY paragraphs assign.
		//
		// No suppression branch, not even on an empty Commands. The gate that decides
		// whether the event exists at all is the producer's — streamsup's
		// emitSlashCommandList suppresses the empty list — so a second,
		// differently-shaped filter here would silently diverge from it, the hazard
		// the ThinkingProgress, RateLimited, ModelAnnounced and ModelList arms above
		// each name. A zero-value SlashCommandList therefore maps.
		var commands []protocol.SlashCommand
		// size starts at 2, the array's own "[" and "]", so the running total is
		// what json.Marshal(commands) will measure rather than a content-only sum.
		size := 2
		for _, c := range e.Commands {
			row := protocol.SlashCommand{
				Name:            c.Name,
				ArgumentHint:    c.ArgumentHint,
				Description:     c.Description,
				Aliases:         c.Aliases,
				TruncatedFields: c.TruncatedFields,
			}
			b, err := json.Marshal(row)
			if err != nil {
				break
			}
			cost := len(b)
			if len(commands) > 0 {
				cost++ // the comma separating this row from the last kept one
			}
			if size+cost > maxSlashCommandListBytes {
				break
			}
			size += cost
			commands = append(commands, row)
		}
		return protocol.TypeSlashCommandList, protocol.SlashCommandListPayload{
			ConversationID:  tc.ConversationID,
			Commands:        commands,
			DroppedCommands: e.DroppedCommands + (len(e.Commands) - len(commands)),
		}, true
	default:
		// ThoughtChunk and nil/unknown drop (see doc comment).
		return "", nil, false
	}
}

// deniedReportKeys translates one of turnevent.ToolCallDenied's report slices from the
// DAEMON's field vocabulary into protocol.ToolDeniedPayload's wire keys. Exactly one
// token differs: that event names the id tool_call_id, its own field name, where the
// frame carries it as tool_use_id — the name the tool_use and tool_result frames already
// use for the same identifier. A token naming a key the frame does not carry is one a
// client cannot look up, which would make the report useless precisely on the join key.
//
// nil in, nil out, and that polarity is load-bearing rather than tidy: a nil report says
// nothing was cut or dropped, and protocol.ToolDeniedPayload has no MarshalJSON, so nil
// is what reaches the wire as a literal null. An empty non-nil slice would ship [] and
// tell a phone that claude's cut text is complete.
//
// It COPIES unconditionally rather than rewriting in place, and returns a fresh backing
// array even when no token changed. This is the only arm in this file that rewrites
// slice CONTENTS, and the same event is also observed by cmd/pyry's history-append path
// and by eventKind's other call sites; sharing the producer's array would let this
// function's output become visible to them as a corrupted report. Copying is what makes
// MapEvent's documented purity true for this variant rather than nearly true.
func deniedReportKeys(report []string) []string {
	if report == nil {
		return nil
	}
	out := make([]string, len(report))
	for i, name := range report {
		if name == "tool_call_id" {
			out[i] = "tool_use_id"
			continue
		}
		out[i] = name
	}
	return out
}

// modelRefusalFallbackReportKeys copies a daemon report into the closed wire
// vocabulary. RefusalExplanation is intentionally absent from the payload, so
// its token is filtered with any unknown token rather than naming a key a client
// cannot inspect. Starting with nil preserves nil for no reports and for an
// excluded-only report, which is what marshals as null instead of [].
func modelRefusalFallbackReportKeys(report []string) []string {
	var out []string
	for _, name := range report {
		switch name {
		case "scope", "original_model", "fallback_model", "refusal_category", "banner":
			out = append(out, name)
		}
	}
	return out
}

// BuildTurnState shapes a turn_state payload for the given conversation and
// target state. The consumer's lifecycle machine decides WHICH state applies
// (e.g. observing a ThoughtChunk means "thinking") and calls this; the adapter
// only shapes the payload. The return type is concrete (not any) because it is
// monomorphic — the consumer needs no type assertion.
func BuildTurnState(conversationID string, state TurnState) (typ string, payload protocol.TurnStatePayload) {
	return protocol.TypeTurnState, protocol.TurnStatePayload{
		ConversationID: conversationID,
		State:          string(state),
	}
}

// inputSummary derives a human-readable précis of a tool's opaque RawInput: the
// JSON compacted to a single line (insignificant whitespace stripped), then
// truncated to maxSummaryLen runes. Empty/nil input, or input that does not
// compact as JSON, yields "" — RawInput is best-effort/opaque (#606), so a
// malformed blob is a précis-less tool_use, not an error.
func inputSummary(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return ""
	}
	return truncate(buf.String(), maxSummaryLen)
}

// inputFields extracts a tool's opaque RawInput as a bounded name-to-string map
// of its own top-level fields (#1678), and returns nil when there is nothing to
// send. Every value is the input's value VERBATIM — a JSON string decoded (so a
// path is a path and an embedded newline is a newline, not a re-quoted JSON
// literal), any other JSON type in its compact JSON form — with no path
// rewritten to a workspace-relative form and no other normalisation.
//
// nil is the answer for an absent input, an input that is not a JSON object,
// and an empty object alike: RawInput is best-effort and opaque (#606), so a
// malformed blob is a field-less tool_use, not an error — inputSummary's
// posture above, for its reasons. That is also why there is no error return and
// no logging on any of these paths: internal/turnbridge has no logger and its
// package doc commits to "no I/O", tool inputs are USER CONTENT, and an error
// value would tempt a caller to log the blob.
//
// Entries are admitted SHORTEST VALUE FIRST (ties broken by key, so the result
// is deterministic), each spending runeLen(key) + runeLen(capped value) from
// maxInputTotalRunes. That order is the policy the bound needs and is what
// actually fixes the reported bug: for a Write{content, file_path} or an
// Edit{file_path, new_string, old_string}, the short identifying field is
// admitted before the bulk text can spend the budget, where sorted-key order
// would put content ahead of file_path and reproduce the original complaint.
//
// An entry that does not fit WHOLE is dropped and the walk stops; it is never
// shortened to fit. That buys the invariant that every value on the wire is
// either the input's value verbatim or that value cut at exactly
// maxInputValueRunes, which is one reject branch fewer and a cleaner client
// contract. It costs nothing measurable — the peak observed Edit reduces to
// roughly file_path plus two capped values, inside the budget — so do not
// "improve" this into a shorten-to-fit path.
//
// A dropped field is simply ABSENT: this payload deliberately carries no
// truncated_fields list (#1678), the "…" marker is this wire's value-level
// convention, and input_summary remains the whole-input fallback.
func inputFields(raw json.RawMessage) map[string]string {
	if len(raw) == 0 {
		return nil
	}
	// The decode is what makes "not a JSON object" free: an array, number,
	// string or bool all fail to unmarshal into a map, and so does a malformed
	// blob. A literal null succeeds and leaves obj nil, which the length check
	// then catches.
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil
	}

	type field struct {
		key   string
		value string
		// runes is the value's length charged against the budget: PRE-ellipsis
		// content, which is what the constants' arithmetic bounds, so it is the
		// capped length rather than len(value) whenever value carries a "…".
		runes int
	}
	fields := make([]field, 0, len(obj))
	for k, v := range obj {
		if utf8.RuneCountInString(k) > maxInputKeyRunes {
			continue
		}
		s := inputValue(v)
		n := utf8.RuneCountInString(s)
		if n > maxInputValueRunes {
			n = maxInputValueRunes
		}
		fields = append(fields, field{key: k, value: truncate(s, maxInputValueRunes), runes: n})
	}
	sort.Slice(fields, func(i, j int) bool {
		if fields[i].runes != fields[j].runes {
			return fields[i].runes < fields[j].runes
		}
		return fields[i].key < fields[j].key
	})

	var out map[string]string
	budget := maxInputTotalRunes
	for _, f := range fields {
		cost := utf8.RuneCountInString(f.key) + f.runes
		if len(out) >= maxInputFields || cost > budget {
			break
		}
		budget -= cost
		if out == nil {
			out = make(map[string]string, len(fields))
		}
		out[f.key] = f.value
	}
	return out
}

// inputValue renders one decoded input value as the string the wire carries: a
// JSON string arrives DECODED, every other JSON type as its compact JSON form
// (interior whitespace stripped, as inputSummary does for the whole blob).
//
// The string case is selected on the leading quote rather than on whether an
// unmarshal into a string SUCCEEDS, and the difference is not stylistic: a JSON
// null unmarshals into a string without error and leaves it "", so the
// success-based form would render null as an empty value instead of as the
// literal "null" its non-string siblings get.
//
// The compaction cannot fail for a value that came out of a successful decode,
// so the guard returns the bytes unchanged rather than inventing a rejection —
// handing back what was handed in is the honest answer for an opaque blob.
func inputValue(raw json.RawMessage) string {
	if v := bytes.TrimLeft(raw, " \t\r\n"); len(v) > 0 && v[0] == '"' {
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			return s
		}
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return string(raw)
	}
	return buf.String()
}

// resultSummary derives a human-readable précis of a tool result's content,
// exhaustive over the sealed ToolContent sum type so a future producer variant
// cannot silently vanish. nil (the legal status-only ToolUpdate) yields "".
//
// Every arm is bounded by the one maxResultSummaryRunes, not by a per-arm
// exception. The cap is inert for two of them — a DiffContent carries a path and
// a TerminalContent carries "terminal " + id, neither of which approaches it —
// so the behavioural change is scoped to the TextContent arm, the only one the
// live producer emits; one rule is cheaper to read and to test than three.
//
// is_error is deliberately NOT visible here. The flag is derived at the MapEvent
// call site, and an error result is truncated at exactly this bound: uncapped is
// unavailable (a failing build dumps as much as it likes, and an unbounded field
// on a frame with a hard 65519-byte cap is a lost control frame waiting to
// happen), and giving errors the envelope's own ~10693-rune headroom instead
// would be a distinction with no behavioural difference on any content that
// exists (#1680). Keeping the flag out is what keeps this signature and arm
// count unchanged.
//
// The live inbound producer (internal/streamsup's toolResultContent) only ever
// emits TextContent or nil; the Diff/Terminal renderings are unreachable today
// but handled (the type is sealed) and kept deliberately minimal until a
// producer (e.g. the ACP adapter #600) emits them, at which point the descriptor
// shape can be refined against a real consumer.
func resultSummary(c turnevent.ToolContent) string {
	switch v := c.(type) {
	case turnevent.TextContent:
		return truncate(v.Text, maxResultSummaryRunes)
	case turnevent.DiffContent:
		return truncate(v.Path, maxResultSummaryRunes)
	case turnevent.TerminalContent:
		return truncate("terminal "+v.TerminalID, maxResultSummaryRunes)
	default:
		return ""
	}
}

// truncate returns s unchanged when it is at most max runes; otherwise it cuts
// at max runes and appends an ellipsis. Rune-aware (not byte-slicing) so
// multibyte text never splits mid-rune.
func truncate(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max]) + "…"
}

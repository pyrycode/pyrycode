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

// maxSummaryLen bounds the input/result précis to a single line of at most this
// many runes. A phone-display bound, not a wire constraint (the envelope cap is
// far larger); tunable if the mobile view wants a different cap.
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
			ConversationID: tc.ConversationID,
			TurnID:         tc.TurnID,
			Seq:            tc.Seq,
			Text:           e.Text,
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
		return protocol.TypeToolUse, protocol.ToolUsePayload{
			ConversationID: tc.ConversationID,
			TurnID:         tc.TurnID,
			ToolUseID:      e.ToolCallID,
			Name:           e.Title,
			InputSummary:   inputSummary(e.RawInput),
			Input:          inputFields(e.RawInput),
		}, true
	case turnevent.ToolUpdate:
		return protocol.TypeToolResult, protocol.ToolResultPayload{
			ConversationID: tc.ConversationID,
			TurnID:         tc.TurnID,
			ToolUseID:      e.ToolCallID,
			IsError:        e.Status == turnevent.ToolStatusFailed,
			ResultSummary:  resultSummary(e.Content),
		}, true
	case turnevent.TurnEnd:
		return protocol.TypeTurnEnd, protocol.TurnEndPayload{
			ConversationID: tc.ConversationID,
			TurnID:         tc.TurnID,
			StopReason:     string(e.Reason),
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
		return protocol.TypeCompacting, protocol.CompactingPayload{
			ConversationID: tc.ConversationID,
			Active:         e.Active,
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
		return protocol.TypeBackgroundTaskUpdated, protocol.BackgroundTaskUpdatedPayload{
			ConversationID:  tc.ConversationID,
			TaskID:          e.TaskID,
			Patch:           e.Patch,
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
		// upstream by emitRateLimit's three-rung gate (a benign status produces no
		// event at all), so a second, differently-shaped filter here would silently
		// diverge from the producer's — the hazard the ThinkingProgress arm above
		// names. Hence no filtering, no defaulting, and specifically:
		//
		//   - ResetsAt is NOT clamped or range-checked in either direction. It is
		//     claude's number, not the daemon's clock: not necessarily in the future,
		//     not necessarily in a sane range, and 0 means claude did not report it
		//     rather than "now" (turnevent.RateLimited.ResetsAt, and
		//     RateLimitedPayload's SECURITY paragraph).
		//   - Neither string is re-capped. The producer bounded both at construction
		//     (streamsup's maxRateLimitField), following Unrecognized's precedent, so
		//     a second cap here would be a second place the limit is decided and the
		//     two could disagree silently.
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
		// silence that one layer later.
		return protocol.TypeRateLimited, protocol.RateLimitedPayload{
			ConversationID:  tc.ConversationID,
			Status:          e.Status,
			LimitType:       e.LimitType,
			ResetsAt:        e.ResetsAt,
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
	default:
		// ThoughtChunk and nil/unknown drop (see doc comment).
		return "", nil, false
	}
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
// The live inbound producer (internal/streamsup's toolResultContent) only ever
// emits TextContent or nil; the Diff/Terminal renderings are unreachable today
// but handled (the type is sealed) and kept deliberately minimal until a
// producer (e.g. the ACP adapter #600) emits them, at which point the descriptor
// shape can be refined against a real consumer.
func resultSummary(c turnevent.ToolContent) string {
	switch v := c.(type) {
	case turnevent.TextContent:
		return truncate(v.Text, maxSummaryLen)
	case turnevent.DiffContent:
		return truncate(v.Path, maxSummaryLen)
	case turnevent.TerminalContent:
		return truncate("terminal "+v.TerminalID, maxSummaryLen)
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

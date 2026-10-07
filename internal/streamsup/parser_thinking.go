package streamsup

import (
	"encoding/json"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// minThinkingTokensPerEvent is the accumulated estimated_tokens_delta that earns
// one turnevent.ThinkingProgress. It is the package's first constant bounding
// FREQUENCY rather than SIZE, and a `min` rather than a `max` because it is the
// smallest quantum that earns an event — the max* naming above would read
// backwards.
//
// There is deliberately NO envelope arithmetic here, and its absence is the
// point rather than an omission: every constant above bounds claude-derived TEXT,
// whose length claude chooses, against the v2 application-envelope cap. This
// event carries two ints, which cannot grow, so the size argument has no term to
// compute. What needs bounding instead is how OFTEN the event fires —
// system/thinking_tokens is the highest-rate subtype claude puts on this surface
// (~10 lines/turn on the 2026-07-27 measurement, 33 in the committed capture's
// one turn), and a 1:1 mapping would put every one of them on the event stream.
//
// The CEILING is measured, not chosen. In the committed capture
// (internal/e2e/realclaude/testdata/dropped_lines_v2.1.220.json) the turn's 33
// lines fall into four bursts — claude's counter restarts at each inference
// request — totalling 184, 167, 126 and 197 tokens of delta. The smallest is 126,
// so any bound above it lets a whole burst go silent: at 127 burst 3 emits
// nothing while the other three still do, and a visibly-thinking turn falls quiet
// for a stretch. 126 is therefore a hard ceiling from the data.
//
// 64 is roughly half of it, and the multiple is the safety margin exactly as
// maxTaskFieldID's 9x and maxTaskDescription's 32x are: a future burst HALF the
// size of the smallest one ever observed still emits, from a zero residual. It is
// also a real reduction — 33 lines become 8 events on the capture, ~4x — and at
// the 2026-07-27 measurement's ~10 lines/turn and ~20 tokens/line it implies
// roughly 1-2 events on a typical turn. Power of two, matching the package's
// other constants.
const minThinkingTokensPerEvent = 64

// systemThinkingTokensLine is the decoded payload of one system/thinking_tokens
// line. Kept separate from streamLine for systemTaskStartedLine's reason, and
// separate from the three background-task targets because it shares no key with
// any of them.
//
// The field set is exactly what the committed capture shows and nothing invented:
// all 33 captured records carry the identical key set, and these are the two numeric
// values the mapping reads. Parent attribution is also read when present on a
// forwarded subagent line. The other two, uuid and session_id, are deliberately
// absent — see turnevent.ThinkingProgress's doc. Absent from the DECODE TARGET is
// a stronger guarantee than the test's reflection sweep, because a field that is
// never declared cannot leak.
//
// Plain int, not *int: absence and an explicit zero are treated IDENTICALLY
// because the rule's response to both is the same — accumulate nothing, emit
// nothing — so a pointer would buy a distinction nothing acts on. A non-numeric
// value, or one too large for int, fails the whole decode and takes the
// undecodable path, exactly as systemTaskUpdatedLine.TaskID does.
type systemThinkingTokensLine struct {
	EstimatedTokens      int             `json:"estimated_tokens"`
	EstimatedTokensDelta int             `json:"estimated_tokens_delta"`
	ParentToolUseID      json.RawMessage `json:"parent_tool_use_id"`
}

// emitThinkingProgress decodes a system/thinking_tokens line and emits at most
// one turnevent.ThinkingProgress, reporting that it consumed the line either way.
// Field mapping and the bound come from the committed capture
// (internal/e2e/realclaude/testdata/dropped_lines_v2.1.220.json), never from a
// hand-built payload.
//
// Unlike its three siblings this mapping is NOT a pure function of one line: it
// is rate-bounded, so most lines accumulate and emit nothing. The rule is
//
//	d <= 0                -> consume, no event               (guard)
//	d >= bound - acc      -> emit the line's two values; acc = 0
//	otherwise             -> acc += d
//
// with acc reset at the `result` arm in consumeLine. >= rather than > because a
// line landing exactly on the bound has delivered the full quantum.
//
// WRITE THE CROSSING TEST SUBTRACTED, NEVER ADDITIVELY. `acc += d; if acc >=
// bound` is arithmetically identical for every value that fits and OVERFLOWS for
// one that does not: estimated_tokens_delta 9223372036854775807 decodes into int
// cleanly, and on a nonzero residual acc+d wraps to roughly -2^63. The comparison
// then reads false, the accumulator lands where no realistic delta climbs out of,
// and the turn's liveness signal is silently dead until the next `result` — an
// unrecoverable state in the one feature whose purpose is making a wedged turn
// visible, from a single malformed or version-drifted line. Subtracted, both
// operands of `bound - acc` are in [1, bound] by the field's invariant, so no
// expression here can overflow at all: the failure is unrepresentable rather than
// guarded against. This is the obvious refactor to get wrong, which is why
// TestParser_ThinkingAccumulatorSurvivesAnExtremeDelta pins it — and why its
// discriminating assertion is the SECOND event after the extreme line, not the
// first.
//
// The d <= 0 guard is not a defence against an unobserved claude bug. A negative
// delta would drive the accumulator down and could leave the bound uncrossable
// for the rest of the turn — SILENCE, which is the single outcome the no-silent-
// burst property forbids. One comparison keeps that property true for inputs the
// capture does not constrain.
//
// NO SILENT BURST, by construction rather than by tuning. claude's cumulative
// counter restarts at every inference request, so a rule keyed on its high-water
// mark emits nothing for a burst that never exceeds an earlier peak (the
// capture's burst 2 tops out below burst 1's). Keying on the DELTA is immune: the
// accumulator only ever grows within a turn, so any burst whose own delta total
// reaches the bound emits at least once regardless of the residual it inherited —
// a residual can only bring the emit FORWARD. Every observed burst clears the
// bound with roughly 2x to spare.
//
// The decode's input is `line` — the TOP-LEVEL bytes — never a nested field, and
// the reason is sharper here than on any sibling. streamLine's doc states the
// property it preserves: control shapes are read from the top level only and
// nested content is never re-scanned, which is what stops a tool result whose
// text is literally `{"type":"result"}` from forging a turn boundary. This event
// is a LIVENESS CLAIM, so decoding it from anywhere else would let claude's own
// tool output assert that the daemon is alive while it is wedged.
//
// A payload that will not decode into the shape (a string estimated_tokens, say,
// or a number too large for int) is dropped with a content-free Debug and no
// event — NOT surfaced as an Unrecognized, because keeping system whole on
// ignoredLineTypes is what makes "no system line reaches the unrecognized lane"
// structural. NOTHING NUMERIC IS LOGGED on either path: the package rule is that
// nothing derived from claude's output reaches a log, and a token count is
// exactly the "it's just a number" exception that rule gets bent for first —
// emitBackgroundTaskRoster's doc refuses the identical bend for the roster's
// entry count.
func (p *Parser) emitThinkingProgress(line []byte) bool {
	var tl systemThinkingTokensLine
	if err := json.Unmarshal(line, &tl); err != nil {
		// The subtype is a message-name keyword, not payload — the same class as
		// sl.Type in the drop log above, so this adds no new category of logged
		// content. Neither token number is logged, and the count is the thing this
		// handler is most tempted to explain itself with.
		p.log.Debug("streamsup: dropping undecodable system line", "subtype", "thinking_tokens")
		return true
	}

	// Absent, zero and negative all land here and are treated identically: no
	// progress to report, and nothing accumulated.
	if tl.EstimatedTokensDelta <= 0 {
		return true
	}
	// The COMPLEMENT of the doc's `d >= bound - acc -> emit`, written this way so
	// the accumulate branch returns early and the emit is the function's tail.
	// Subtracted, never additive — see the doc above. `bound - acc` is in
	// [1, bound] by the invariant at thinkingSinceEmit, so it cannot overflow
	// whatever claude sends; and the addition below is reached only when
	// d < bound - acc, which is exactly the condition making acc + d < bound, so
	// it cannot overflow either and the invariant is restored on the way out.
	if tl.EstimatedTokensDelta < minThinkingTokensPerEvent-p.thinkingSinceEmit {
		p.thinkingSinceEmit += tl.EstimatedTokensDelta
		return true
	}

	p.thinkingSinceEmit = 0
	// The line's OWN values, not the accumulated total: the event stays a pure
	// function of the line that produced it, and the accumulator's residue is a
	// documented consumer hazard on turnevent.ThinkingProgress rather than a
	// number invented here. The two ints need no caps; parent attribution uses
	// the existing bounded validator.
	p.emit(turnevent.ThinkingProgress{
		ParentToolCallID:     parentToolUseID(tl.ParentToolUseID),
		EstimatedTokens:      tl.EstimatedTokens,
		EstimatedTokensDelta: tl.EstimatedTokensDelta,
	})
	return true
}

package streamsup

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// thinkingCapturedLineCount, thinkingCapturedEventCount and
// thinkingCapturedNeighbourEventCount are #1385's count pins, written as
// LITERALS and deliberately not derived from minThinkingTokensPerEvent. Same
// rule as taskStartedCapCheat, applied to a count instead of a byte length: an
// expectation the test recomputes from the constant puts the constant on BOTH
// sides of the assertion, so mutating it moves both and the pin stays green
// while proving nothing.
//
// THE EVIDENCE, measured against the shipped parser by
// TestParser_ThinkingProgressCountFromCapture and its neighbour sub-test:
//
//	bound  32 -> 14 events
//	bound  63 ->  8 events
//	bound  64 ->  8 events   <- shipped
//	bound  65 ->  7 events   <- the nearest DIFFERING neighbour
//	bound 128 ->  4 events
//
// Discrimination here is SINGLE-SIDED, and that is the strongest property
// available rather than a shortcut: the count is a step function of the bound
// with plateaus wider than one, and no threshold in [41, 125] changes the count
// at both T-1 and T+1 (verified by sweeping the committed capture). 63 and 64
// sit on the same plateau, so the pin is anchored at 65.
const (
	thinkingCapturedLineCount           = 33
	thinkingCapturedEventCount          = 8
	thinkingNeighbourBound              = 65
	thinkingCapturedNeighbourEventCount = 7
)

// thinkingTokensLineFixture builds a system/thinking_tokens line from the two
// mapped keys. It invents NO field structure — the keys are exactly the
// capture's — and exists only to vary the VALUES, which is what the rate-bound
// proof needs and what the capture cannot supply: no captured line carries a
// zero, a negative, or an extreme delta.
func thinkingTokensLineFixture(tokens, delta int) string {
	return fmt.Sprintf(`{"type":"system","subtype":"thinking_tokens","estimated_tokens":%d,"estimated_tokens_delta":%d}`, tokens, delta)
}

// thinkingProgressEvents drives lines through ONE parser, in order, and returns
// the ThinkingProgress events they emitted plus the index of the input line that
// produced each. One parser, not one per line: the accumulator's carry-over
// between lines is the thing under test, and a fresh parser per line would reset
// it and silently turn every rate assertion into a per-line assertion.
//
// It fails on any non-ThinkingProgress event so a stray emission cannot inflate
// a count that another assertion then reads as correct.
func thinkingProgressEvents(t *testing.T, lines []string) ([]turnevent.ThinkingProgress, []int) {
	t.Helper()
	var events []turnevent.ThinkingProgress
	var at []int
	var current int
	p := NewParser(func(ev turnevent.Event) {
		tp, ok := ev.(turnevent.ThinkingProgress)
		if !ok {
			t.Fatalf("line %d emitted %T, want turnevent.ThinkingProgress", current, ev)
		}
		events = append(events, tp)
		at = append(at, current)
	}, discardLogger())
	for i, line := range lines {
		current = i
		if _, err := p.Write([]byte(line + "\n")); err != nil {
			t.Fatalf("Write(line %d) err = %v, want nil", i, err)
		}
	}
	return events, at
}

// capturedThinkingLines returns the capture's thinking_tokens payloads as
// strings, in stream order, asserting the count first so every pin below cannot
// go vacuous on a capture that stopped matching.
func capturedThinkingLines(t *testing.T) []string {
	t.Helper()
	raw := capturedSystemLines(t, "thinking_tokens")
	if len(raw) != thinkingCapturedLineCount {
		t.Fatalf("the capture holds %d thinking_tokens records, want %d — the counts pinned below were measured against %d",
			len(raw), thinkingCapturedLineCount, thinkingCapturedLineCount)
	}
	lines := make([]string, len(raw))
	for i, b := range raw {
		lines[i] = string(b)
	}
	return lines
}

// TestParser_ThinkingProgressCountFromCapture is #1385's AC2 and AC3: the 33
// captured lines produce strictly fewer than 33 events, and the number they DO
// produce is pinned as an explicit literal that a change to the bound moves.
//
// Driving the 33 lines alone is equivalent to driving the whole captured stream:
// the capture's terminated_on is `result` and its outcome_detail records the
// per-subtype census as valid for THIS turn, so no `result` line falls between
// the first and last of them and the accumulator is never reset mid-run. That is
// the assumption the count rests on.
//
// See thinkingCapturedEventCount for the measured bound→count table and why the
// discrimination is single-sided.
func TestParser_ThinkingProgressCountFromCapture(t *testing.T) {
	t.Parallel()
	lines := capturedThinkingLines(t)

	events, _ := thinkingProgressEvents(t, lines)
	if len(events) != thinkingCapturedEventCount {
		t.Errorf("the %d captured lines produced %d events, want %d", len(lines), len(events), thinkingCapturedEventCount)
	}

	// AC2's "strictly fewer", asserted rather than left to the reader.
	if len(events) >= len(lines) {
		t.Errorf("the mapping emitted %d events for %d lines; the rate bound must emit strictly fewer", len(events), len(lines))
	}

	// AC3's discriminating half. The rule is reproduced here against a DIFFERENT
	// bound rather than by mutating the shipped constant, which a test cannot do:
	// this is what shows the pinned 8 is a property of the bound and not of the
	// capture. If the shipped rule ever stops agreeing with this reproduction, the
	// count above is the one that governs and this arm is the one to re-derive.
	t.Run("neighbouring_bound_gives_a_different_count", func(t *testing.T) {
		got := replayThinkingRule(t, lines, thinkingNeighbourBound)
		if got != thinkingCapturedNeighbourEventCount {
			t.Errorf("at bound %d the capture produces %d events, want %d", thinkingNeighbourBound, got, thinkingCapturedNeighbourEventCount)
		}
		if got == thinkingCapturedEventCount {
			t.Errorf("bound %d produces the same count as the shipped bound (%d); the pin above discriminates nothing",
				thinkingNeighbourBound, got)
		}
	})
}

// replayThinkingRule reproduces the shipped rate rule against an arbitrary bound
// and reports how many events the lines would produce. It exists for the two
// assertions the shipped parser cannot make about itself — a different bound's
// count, and the silent-burst control — and is deliberately a REPRODUCTION, not a
// call into production code parameterized for tests: production takes the bound
// from the constant, and adding a test seam to vary it is what would let the
// constant stop being load-bearing.
func replayThinkingRule(t *testing.T, lines []string, bound int) int {
	t.Helper()
	return len(replayThinkingRuleAt(t, lines, bound))
}

// replayThinkingRuleAt is replayThinkingRule's positional form: the indices of
// the lines that would emit.
func replayThinkingRuleAt(t *testing.T, lines []string, bound int) []int {
	t.Helper()
	var acc int
	var at []int
	for i, line := range lines {
		var tl struct {
			Delta int `json:"estimated_tokens_delta"`
		}
		if err := json.Unmarshal([]byte(line), &tl); err != nil {
			t.Fatalf("decoding line %d: %v", i, err)
		}
		if tl.Delta <= 0 {
			continue
		}
		if tl.Delta >= bound-acc {
			at = append(at, i)
			acc = 0
			continue
		}
		acc += tl.Delta
	}
	return at
}

// TestParser_ThinkingProgressCoversEveryBurst is #1385's AC1 and the criterion a
// naive rule fails: claude's estimated_tokens counter is cumulative within one
// INFERENCE REQUEST, not within a turn, so it restarts near zero several times
// per turn. A rule keyed on that counter's high-water mark emits nothing at all
// for a burst that never exceeds an earlier burst's peak — the captured turn's
// burst 2 tops out at 167 against burst 1's 184 — and a visibly-thinking turn
// falls silent for a whole stretch.
//
// The bursts are DERIVED from the capture rather than pinned: a new burst starts
// wherever a line's estimated_tokens fails to exceed its predecessor's. The
// derivation's own result is asserted first so this cannot go vacuous on a
// capture whose shape changed.
//
// The per-burst assertion is a MINIMUM of one, not an exact distribution: the
// exact distribution is what TestParser_ThinkingProgressCountFromCapture already
// pins, and pinning it twice would make both brittle for no extra coverage.
func TestParser_ThinkingProgressCoversEveryBurst(t *testing.T) {
	t.Parallel()
	lines := capturedThinkingLines(t)

	// bursts[i] is the half-open line range [start, end) of one inference request.
	type burst struct{ start, end int }
	var bursts []burst
	start := 0
	prev := -1
	for i, line := range lines {
		var tl struct {
			Tokens int `json:"estimated_tokens"`
		}
		if err := json.Unmarshal([]byte(line), &tl); err != nil {
			t.Fatalf("decoding line %d: %v", i, err)
		}
		if i > 0 && tl.Tokens <= prev {
			bursts = append(bursts, burst{start, i})
			start = i
		}
		prev = tl.Tokens
	}
	bursts = append(bursts, burst{start, len(lines)})

	const wantBursts = 4
	if len(bursts) != wantBursts {
		t.Fatalf("the capture's counter resets derive %d bursts, want %d — the per-burst assertion below "+
			"is only meaningful against the measured burst structure", len(bursts), wantBursts)
	}

	_, at := thinkingProgressEvents(t, lines)
	perBurst := make([]int, len(bursts))
	for _, i := range at {
		for b, br := range bursts {
			if i >= br.start && i < br.end {
				perBurst[b]++
			}
		}
	}
	for b, n := range perBurst {
		if n == 0 {
			t.Errorf("burst %d (lines [%d,%d)) produced no events: a client watching this turn would see the "+
				"thinking signal stop for the whole stretch, which is exactly the failure AC1 forbids",
				b+1, bursts[b].start, bursts[b].end)
		}
	}

	// The control that makes the assertion above falsifiable. 127 is one past the
	// smallest burst's 126-token total, so at that bound burst 3 goes silent while
	// the other three still emit — the silent-stretch failure, reproduced. Without
	// this a reader cannot tell whether "at least one per burst" is a real property
	// or a tautology of the rule's shape.
	t.Run("control_a_bound_above_the_smallest_burst_goes_silent", func(t *testing.T) {
		const silentBound = 127
		at := replayThinkingRuleAt(t, lines, silentBound)
		perBurst := make([]int, len(bursts))
		for _, i := range at {
			for b, br := range bursts {
				if i >= br.start && i < br.end {
					perBurst[b]++
				}
			}
		}
		if perBurst[2] != 0 {
			t.Errorf("at bound %d burst 3 produced %d events, want 0 — this control is what proves the "+
				"per-burst assertion above can fail at all", silentBound, perBurst[2])
		}
		for _, b := range []int{0, 1, 3} {
			if perBurst[b] == 0 {
				t.Errorf("at bound %d burst %d also went silent; the control is meant to isolate the SMALLEST "+
					"burst, not to silence the turn", silentBound, b+1)
			}
		}
	})
}

// TestParser_ThinkingProgressMapsFromCapture is #1385's AC5: the captured line
// becomes one turnevent.ThinkingProgress carrying the two measured numeric
// fields, and neither of claude's two dropped keys.
//
// The field assertions are DERIVED from the emitting line's own payload rather
// than pinned, which is this family's rule (the capture is redacted, so a pinned
// expectation would pin a placeholder).
func TestParser_ThinkingProgressMapsFromCapture(t *testing.T) {
	t.Parallel()
	lines := capturedThinkingLines(t)

	events, at := thinkingProgressEvents(t, lines)
	if len(events) == 0 {
		t.Fatalf("the captured lines produced no events; there is nothing to map-check")
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(lines[at[0]]), &payload); err != nil {
		t.Fatalf("decoding the first emitting line: %v", err)
	}
	for _, r := range []struct {
		field     string
		claudeKey string
		got       int
	}{
		{"EstimatedTokens", "estimated_tokens", events[0].EstimatedTokens},
		{"EstimatedTokensDelta", "estimated_tokens_delta", events[0].EstimatedTokensDelta},
	} {
		want, ok := payload[r.claudeKey].(float64)
		if !ok || want == 0 {
			t.Fatalf("the capture's emitting line carries no non-zero number %q, so the routing of %s cannot be proven against it",
				r.claudeKey, r.field)
		}
		if r.got != int(want) {
			t.Errorf("%s: got %d, want the line's %s = %d", r.field, r.got, r.claudeKey, int(want))
		}
	}

	// The two deliberate drops (see ThinkingProgress's doc): claude's session_id is
	// NOT the daemon's conversation identity, and uuid has no reader in the daemon.
	// Swept by REFLECTION so a field added later is covered without anyone
	// remembering to extend a list.
	//
	// ParentToolCallID is a string used for classification, so the string sweep
	// must still exclude unrelated session and line identities. The integer floor
	// separately pins the two measured numbers.
	rv := reflect.ValueOf(events[0])
	var ints, strs int
	for i := 0; i < rv.NumField(); i++ {
		switch rv.Field(i).Kind() {
		case reflect.Int:
			ints++
		case reflect.String:
			strs++
		}
	}
	if ints != 2 {
		t.Errorf("ThinkingProgress carries %d int fields, want 2 (estimated_tokens, estimated_tokens_delta)", ints)
	}
	for _, key := range []string{"session_id", "uuid"} {
		v, _ := payload[key].(string)
		if v == "" {
			t.Fatalf("the capture carries no string %q, so the drop assertion would be vacuous", key)
		}
		for i := 0; i < rv.NumField(); i++ {
			if rv.Field(i).Kind() == reflect.String && rv.Field(i).String() == v {
				t.Errorf("field %s carries claude's %s (%q); it is deliberately NOT on this event",
					rv.Type().Field(i).Name, key, v)
			}
		}
	}
	if strs != 0 {
		t.Logf("ThinkingProgress now has %d string field(s); the reflection sweep above is no longer trivial", strs)
	}
}

// TestParser_ThinkingProgressSilentWithoutPayload is #1385's AC6: a line of this
// subtype carrying no thinking payload produces no event. That is what keeps the
// ptyrunner surface — measured at ZERO thinking_tokens lines by #1218 — free of
// both new traffic and any liveness claim the daemon cannot support.
//
// It holds twice over, and both halves are worth seeing: the d <= 0 guard is the
// direct reason (an absent estimated_tokens_delta decodes to 0 and returns before
// touching the accumulator), and independently the accumulator's invariant
// acc ∈ [0, bound-1] means a zero-delta line could not trigger an emit even if
// the guard were removed. The sub_turn_control row is the second half.
func TestParser_ThinkingProgressSilentWithoutPayload(t *testing.T) {
	t.Parallel()
	rows := []struct {
		name  string
		lines []string
		want  int
	}{
		// The realclaude mirror's old bare fixture, which is why that fixture had to
		// gain a payload when the subtype became mapped (#1385).
		{"no payload keys at all", []string{`{"type":"system","subtype":"thinking_tokens"}`}, 0},
		// Both rows moved verbatim from TestParser_LineMapping and
		// TestParser_IgnoredLineTypesStaySilent, where they asserted this subtype was
		// dropped as an ignored line. The subtype is MAPPED now, so the outcome is
		// unchanged but the reason is not: `tokens` is not a key the mapping reads,
		// so these lines carry no delta and the rate rule emits nothing.
		{"wrong key (moved from the mapping table)", []string{`{"type":"system","subtype":"thinking_tokens","tokens":42}`}, 0},
		{"wrong key (moved from the silence table)", []string{`{"type":"system","subtype":"thinking_tokens","tokens":128}`}, 0},
		{"cumulative present, delta absent", []string{`{"type":"system","subtype":"thinking_tokens","estimated_tokens":900}`}, 0},
		{"delta exactly zero", []string{thinkingTokensLineFixture(900, 0)}, 0},
		{"delta negative", []string{thinkingTokensLineFixture(900, -5000)}, 0},
		// Undecodable: a string where an int belongs fails the whole decode and takes
		// the content-free drop, emitting nothing.
		{"undecodable payload", []string{`{"type":"system","subtype":"thinking_tokens","estimated_tokens":"lots","estimated_tokens_delta":1024}`}, 0},
		// The control: without it every zero above could pass on a mis-wired sink.
		{"control_a_crossing_line_emits", []string{thinkingTokensLineFixture(1024, 1024)}, 1},
		// The sub-turn control, and the sharper half of AC6: a payload-free line
		// arriving on a parser that has already accumulated bound-1 tokens still
		// emits nothing. This is the acc ∈ [0, bound-1] invariant — the property that
		// makes a payload-free line unable to trigger an emit even indirectly.
		{"sub_turn_control_payload_free_after_a_near_crossing", []string{
			thinkingTokensLineFixture(minThinkingTokensPerEvent-1, minThinkingTokensPerEvent-1),
			`{"type":"system","subtype":"thinking_tokens"}`,
		}, 0},
	}
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			events, _ := thinkingProgressEvents(t, tc.lines)
			if len(events) != tc.want {
				t.Errorf("got %d events, want %d (%#v)", len(events), tc.want, events)
			}
		})
	}
}

// TestParser_ThinkingAccumulatorSurvivesAnExtremeDelta pins the one property no
// capture motivates and the obvious implementation gets wrong.
//
// Written additively — acc += d; if acc >= bound — the rule overflows:
// estimated_tokens_delta 9223372036854775807 decodes into int cleanly, and on any
// nonzero residual that addition wraps to roughly -2^63. The comparison then
// reads false, the accumulator lands in a region no realistic delta climbs out
// of, and the turn's liveness signal is SILENTLY DEAD until the next `result` —
// an unrecoverable state in the one feature whose purpose is making a wedged turn
// visible, reachable from a single malformed or version-drifted line.
//
// The shipped rule is written subtracted (d >= bound-acc), where both operands
// are in [1, bound] by the accumulator's invariant, so no expression in it can
// overflow at all. The failure is unrepresentable rather than guarded against.
//
// The DISCRIMINATING assertion is the SECOND event, not the first: the additive
// form also emits on the extreme line itself. What it cannot do is emit again
// afterwards.
//
// The setup values DERIVE from minThinkingTokensPerEvent, deliberately unlike
// TestParser_ThinkingProgressCountFromCapture's literals. The rule there is about
// pinning the bound's VALUE, and a derived expectation would put the constant on
// both sides of that assertion. Here the property under test — that the crossing
// arithmetic cannot overflow — holds at every bound, and the constant appears
// only in the SETUP, never in the expectation. Deriving keeps a legitimate bound
// change re-deriving exactly one number (the count pin), which is the number that
// should be re-derived, instead of quietly breaking tests that assert something
// else.
func TestParser_ThinkingAccumulatorSurvivesAnExtremeDelta(t *testing.T) {
	t.Parallel()
	const extreme = `{"type":"system","subtype":"thinking_tokens","estimated_tokens":1,"estimated_tokens_delta":9223372036854775807}`

	t.Run("from_a_nonzero_residual", func(t *testing.T) {
		events, _ := thinkingProgressEvents(t, []string{
			// accumulate bound-1, no event
			thinkingTokensLineFixture(minThinkingTokensPerEvent-1, minThinkingTokensPerEvent-1),
			// crosses: one event, accumulator reset
			extreme,
			// a full quantum: must emit again
			thinkingTokensLineFixture(minThinkingTokensPerEvent, minThinkingTokensPerEvent),
		})
		if len(events) != 2 {
			t.Fatalf("got %d events, want 2 — if the second is missing the accumulator wrapped negative on the "+
				"extreme delta and the turn's liveness signal is dead until the next result (%#v)", len(events), events)
		}
	})

	t.Run("from_a_zero_accumulator", func(t *testing.T) {
		events, _ := thinkingProgressEvents(t, []string{extreme})
		if len(events) != 1 {
			t.Errorf("got %d events, want 1", len(events))
		}
	})
}

// TestParser_ThinkingAccumulatorResetsAtTurnEnd pins the accumulator's one
// boundary. `result` is the only turn boundary the parser recognizes, and the
// reset there is what keeps zero cross-turn bleed structural now that the parser
// holds state at all.
func TestParser_ThinkingAccumulatorResetsAtTurnEnd(t *testing.T) {
	t.Parallel()
	for _, subtype := range []string{"success", "error_during_execution"} {
		t.Run(subtype, func(t *testing.T) {
			var events []turnevent.Event
			p := NewParser(func(ev turnevent.Event) { events = append(events, ev) }, discardLogger())
			lines := []string{
				// accumulate bound-1; then a `result`; then the single token that
				// would cross if the residual had survived the boundary.
				thinkingTokensLineFixture(minThinkingTokensPerEvent-1, minThinkingTokensPerEvent-1),
				`{"type":"result","subtype":"` + subtype + `"}`,
				thinkingTokensLineFixture(1, 1),
			}
			for i, line := range lines {
				if _, err := p.Write([]byte(line + "\n")); err != nil {
					t.Fatalf("Write(line %d) err = %v, want nil", i, err)
				}
			}
			var turnEnds, progress int
			for _, ev := range events {
				switch ev.(type) {
				case turnevent.TurnEnd:
					turnEnds++
				case turnevent.ThinkingProgress:
					progress++
				}
			}
			if progress != 0 {
				t.Errorf("got %d ThinkingProgress events after the turn boundary, want 0 — the accumulator "+
					"carried a residual across a `result` line", progress)
			}
			// The reset must not disturb the arm it lives in.
			if turnEnds != 1 {
				t.Errorf("got %d TurnEnd events, want exactly 1", turnEnds)
			}
		})
	}
}

// TestParser_ThinkingProgressDropIsLoggedContentFree is the package's standing
// rule applied to the new path: nothing derived from claude's output reaches a
// log. Both paths are driven — a line that emits and a line the decode rejects —
// because the malformed one's handler is the one most tempted to explain itself.
//
// The NEW leak surface this closes is numeric. Every sibling's sweep looks for
// claude's TEXT; here the payload is two integers, and "it's just a number" is
// exactly the exception a content-free rule gets bent for first
// (emitBackgroundTaskRoster's doc already refuses the identical bend for the
// roster's entry count). So the sweep covers the decimal rendering of every token
// number the run used, and those numbers are DISTINCTIVE — 70141, not 5 or 128 —
// so a substring match cannot false-positive on a byte count, a timestamp, or the
// bound itself appearing in unrelated record text, and a real leak cannot hide
// behind a digit that was going to be there anyway.
func TestParser_ThinkingProgressDropIsLoggedContentFree(t *testing.T) {
	t.Parallel()

	captured := capturedSystemLines(t, "thinking_tokens")
	var payload map[string]any
	if err := json.Unmarshal(captured[0], &payload); err != nil {
		t.Fatalf("decoding the captured payload: %v", err)
	}
	capturedSession, _ := payload["session_id"].(string)
	capturedUUID, _ := payload["uuid"].(string)
	if capturedSession == "" || capturedUUID == "" {
		t.Fatalf("the capture carries no session_id/uuid strings, so the leak sweep would be vacuous")
	}

	// Distinctive numbers, per the doc above. The emitting pair crosses the bound;
	// the malformed line's delta is a string, so the decode fails and the
	// content-free drop path runs.
	const (
		emitTokens      = 70141
		emitDelta       = 70143
		malformedTokens = 70147
	)
	malformed := fmt.Sprintf(`{"type":"system","subtype":"thinking_tokens","estimated_tokens":%d,"estimated_tokens_delta":"%d"}`,
		malformedTokens, malformedTokens)

	rec := &logRecorder{}
	var events []turnevent.Event
	p := NewParser(func(ev turnevent.Event) { events = append(events, ev) }, slog.New(rec))
	for _, line := range []string{thinkingTokensLineFixture(emitTokens, emitDelta), malformed} {
		if _, err := p.Write([]byte(line + "\n")); err != nil {
			t.Fatalf("Write err = %v, want nil", err)
		}
	}

	// The malformed line is dropped, and NOT as an Unrecognized: keeping `system`
	// whole on ignoredLineTypes is what makes "no system line reaches the
	// unrecognized lane" structural.
	if len(events) != 1 {
		t.Fatalf("event count: got %d, want 1 (the crossing line only) — %#v", len(events), events)
	}
	if _, ok := events[0].(turnevent.ThinkingProgress); !ok {
		t.Fatalf("event type: got %T, want turnevent.ThinkingProgress", events[0])
	}

	all := rec.withMessage("streamsup: dropping undecodable system line")
	if len(all) != 1 {
		t.Fatalf("undecodable-drop records: got %d, want 1", len(all))
	}
	if !reflect.DeepEqual(all[0].attrs, map[string]string{"subtype": "thinking_tokens"}) {
		t.Errorf("the undecodable-drop record carries %#v, want the subtype keyword only", all[0].attrs)
	}

	leaks := []string{
		capturedSession, capturedUUID,
		strconv.Itoa(emitTokens), strconv.Itoa(emitDelta), strconv.Itoa(malformedTokens),
	}
	for _, r := range rec.all() {
		for _, leak := range leaks {
			if strings.Contains(r.msg, leak) {
				t.Errorf("record message carries claude-derived content (%q): %q", leak, r.msg)
			}
			for k, v := range r.attrs {
				if strings.Contains(v, leak) {
					t.Errorf("record %q attr %q carries claude-derived content (%q); this path logs the subtype only",
						r.msg, k, leak)
				}
			}
		}
	}
}

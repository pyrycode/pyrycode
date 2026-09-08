package streamsup

import (
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// The compaction line shapes, as literals. Transcribed from the shape #2229's
// live capture observed against claude 2.1.259 and recorded in
// docs/knowledge/features/streamsup-package-system-maps-per-subtype-since-2026-08-07.md:
// `system/status` carries status:"compacting" while compaction runs and
// status:null plus compact_result/compact_error when it ends, and a separate
// `system/compact_boundary` carries compact_metadata.
//
// HAND-AUTHORED, and the reason is worth stating rather than leaving as an
// apparent shortcut. #2229's capture fired and reported those shapes, but the run
// was the dispatcher's gate-only real-claude lap, which verifies from a detached
// worktree and never runs `git add` — so the fixture the AC names was written
// in-repo and thrown away with the worktree. compactionFixtureReplay below is the
// assertion over those bytes and arms the moment an operator commits them; these
// literals are what proves the mapping in the meantime, in the style every other
// mapping arm in this package is tested in.
const (
	compactingStartLine = `{"type":"system","subtype":"status","status":"compacting"}`
	compactingEndLine   = `{"type":"system","subtype":"status","status":null,"compact_result":"success"}`
	compactingFailLine  = `{"type":"system","subtype":"status","status":null,` +
		`"compact_result":"failed","compact_error":"context window still over budget"}`
	// TRANSCRIBED FROM THE COMMITTED CAPTURE (#2237), not hand-authored like its
	// neighbours above and not retyped from the ticket body. Frame 16 of
	// internal/e2e/realclaude/testdata/compaction_v2.1.259.json, whole and in claude's
	// own key order, with the redaction placeholder the capture carries left in place.
	//
	// It replaces a literal that carried INVENTED counts (120000/18000) written when
	// no capture existed. That the numbers were plausible is exactly what made them
	// worth replacing: a row asserting on them was asserting on this file's author.
	//
	// It carries every key the observed line carries, INCLUDING the four #2237 does
	// not publish and the three uuids it must not leak. That is deliberate and is
	// what makes the allowlist rows below non-vacuous — a decode target that grew a
	// field would show up here rather than in review.
	compactBoundaryLine = `{"type":"system","subtype":"compact_boundary","session_id":"$SESSION_ID",` +
		`"uuid":"7d31f0bb-482d-45c0-bf8a-2ef2cd2ccfc0","compact_metadata":{"trigger":"manual",` +
		`"pre_tokens":23600,"post_tokens":2612,"cumulative_dropped_tokens":20988,"duration_ms":15596,` +
		`"preserved_segment":{"head_uuid":"ae93ee09-99cd-472e-a34b-10278c8aa85a",` +
		`"anchor_uuid":"e9d8ca32-d112-4cfd-876b-656936a4ee31",` +
		`"tail_uuid":"134c965c-edc1-45bf-8cc7-e2aa0198c829"},` +
		`"preserved_messages":{"anchor_uuid":"e9d8ca32-d112-4cfd-876b-656936a4ee31",` +
		`"uuids":["ae93ee09-99cd-472e-a34b-10278c8aa85a","134c965c-edc1-45bf-8cc7-e2aa0198c829"],` +
		`"all_uuids":["ae93ee09-99cd-472e-a34b-10278c8aa85a","134c965c-edc1-45bf-8cc7-e2aa0198c829"]}},` +
		`"logical_parent_uuid":"134c965c-edc1-45bf-8cc7-e2aa0198c829"}`
	compactingResultLine = `{"type":"result","subtype":"success"}`
)

// The observed line's three published values, as constants, so a row cannot assert
// on a number this file typed twice and get them to agree with each other rather
// than with the capture.
const (
	compactBoundaryTrigger   = "manual"
	compactBoundaryPreTokens = 23600
	compactBoundaryPostToken = 2612
)

// compactingTrace renders one event as the single token this file's rows assert
// on. The projection is deliberate: the rows are about WHICH edges fire and in
// what order relative to the turn boundary, and TurnEnd carries a dozen fields
// none of which this ticket moves. Anything not named here renders as its Go type,
// so a row that starts emitting something unexpected still fails rather than
// matching by omission.
func compactingTrace(ev turnevent.Event) string {
	switch e := ev.(type) {
	case turnevent.Compacting:
		if e.Active {
			return "compacting:true"
		}
		return "compacting:false"
	case turnevent.CompactionBoundary:
		// #2237. Named rather than collapsed into "other", because the rows below are
		// about ORDER — where this frame falls relative to the edge pair and the turn
		// boundary — and a token reading "other" would let it move without a row noticing.
		// The VALUES are not projected here: those are
		// TestParser_CompactBoundaryPublishesTriggerAndCounts's, over the events themselves.
		return "compact_boundary"
	case turnevent.TurnEnd:
		return "turn_end"
	case turnevent.Unrecognized:
		return "unrecognized"
	default:
		return "other"
	}
}

// compactingRun feeds lines to one Parser in order and returns the trace of every
// event emitted. One parser across the whole sequence, never one per line: the
// edge pair is cross-line state, so a per-line helper would make every row vacuous.
func compactingRun(t *testing.T, rec slog.Handler, lines ...string) []string {
	t.Helper()
	var got []string
	logger := discardLogger()
	if rec != nil {
		logger = slog.New(rec)
	}
	p := NewParser(func(ev turnevent.Event) { got = append(got, compactingTrace(ev)) }, logger)
	for _, line := range lines {
		if _, err := p.Write([]byte(line + "\n")); err != nil {
			t.Fatalf("Write(%.40s) err = %v, want nil", line, err)
		}
	}
	return got
}

// TestParser_CompactingEdges is the mapping table for #2227: a sequence of lines
// in, the exact event trace out. Every acceptance criterion this package can
// answer offline is one row.
func TestParser_CompactingEdges(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		lines []string
		want  []string
		why   string
	}{
		{
			name:  "the edge pair",
			lines: []string{compactingStartLine, compactingEndLine},
			want:  []string{"compacting:true", "compacting:false"},
			why:   "the whole feature: one rising edge, one falling edge, nothing else",
		},
		{
			name:  "a second compacting line while the edge is open is silent",
			lines: []string{compactingStartLine, compactingStartLine, compactingEndLine},
			want:  []string{"compacting:true", "compacting:false"},
			why: "AC 4. claude repeating itself must not put a second banner-on frame on the " +
				"wire; the state machine's open arm is what makes the mapping idempotent",
		},
		{
			name:  "a closing line with no edge open is silent",
			lines: []string{compactingEndLine},
			want:  nil,
			why: "AC 4. A falling edge with nothing to fall from would tell a client to clear a " +
				"banner it never raised, which is a claim about a compaction that never ran",
		},
		{
			name:  "a failed compaction still closes the edge",
			lines: []string{compactingStartLine, compactingFailLine},
			want:  []string{"compacting:true", "compacting:false"},
			why: "AC 5. compact_result is not a discriminator: the banner cannot stick, so the " +
				"falling edge is a function of `status` leaving \"compacting\" and of nothing else",
		},
		{
			name:  "a result line closes an open edge BEFORE the turn ends",
			lines: []string{compactingStartLine, compactingResultLine},
			want:  []string{"compacting:true", "compacting:false", "turn_end"},
			why: "AC 5's second half, and the ORDER is the assertion. A turn_end arriving first " +
				"would leave the client holding a lit banner across a boundary the turn already " +
				"closed — the stuck banner this criterion exists to forbid",
		},
		{
			name:  "a result line with no edge open emits no falling edge",
			lines: []string{compactingResultLine},
			want:  []string{"turn_end"},
			why: "the reset is CONDITIONAL on the wire even though it is unconditional in state; " +
				"an unconditional emit would put a compacting:false on every turn that ever ends",
		},
		{
			name:  "compact_boundary is a frame of its own and not an edge",
			lines: []string{compactingStartLine, compactBoundaryLine, compactingEndLine},
			want:  []string{"compacting:true", "compact_boundary", "compacting:false"},
			why: "#2237's AC 3, and the row is unchanged in what it PROTECTS. The boundary line now " +
				"produces a frame, but it is not an edge: it neither opens nor closes the pair, so " +
				"the falling edge that follows still fires. That is what proves emitCompactionBoundary " +
				"left the compacting flag alone — a mapping that set or cleared it would swallow the " +
				"third token here",
		},
		{
			name: "an undecodable status line leaves the edge intact",
			lines: []string{
				compactingStartLine,
				`{"type":"system","subtype":"status","status":{"phase":"compacting"}}`,
				compactingEndLine,
			},
			want: []string{"compacting:true", "compacting:false"},
			why: "a `status` claude changed the shape of consumes the line and touches nothing, " +
				"per emitThinkingProgress's precedent. Closing the edge on a line we could not " +
				"read would be inventing an observation; the `result` reset bounds it either way",
		},
		{
			name: "no residual crosses a completed turn boundary",
			lines: []string{
				compactingStartLine, compactingEndLine, compactingResultLine,
				compactingEndLine, compactingResultLine,
			},
			want: []string{"compacting:true", "compacting:false", "turn_end", "turn_end"},
			why: "the second turn's closing line finds no open edge and says nothing. Parser's " +
				"doc rests its whole cross-turn argument on the one reset point, and a third " +
				"field that leaked past it would falsify that argument for the two already there",
		},
		{
			name: "an edge left open by a resultless turn closes at the next result",
			lines: []string{
				compactingStartLine, compactingStartLine, compactingResultLine,
			},
			want: []string{"compacting:true", "compacting:false", "turn_end"},
			why: "the residual's bound, pinned. A child that dies mid-compaction leaves the field " +
				"set on a long-lived parser (cmd/pyry builds one per session); what answers it is " +
				"this reset, so the banner outlives its compaction by at most one boundary",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := compactingRun(t, nil, tc.lines...)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("events = %v, want %v\nwhy: %s", got, tc.want, tc.why)
			}
		})
	}
}

// TestParser_CompactingSurfacesNoUnrecognized is AC 2's hermetic half. It is a
// separate test rather than a column on the table above because it asserts over
// the WHOLE corpus at once: the criterion is about a turn, not about a line, and
// a per-row column would let a future row be added without it.
func TestParser_CompactingSurfacesNoUnrecognized(t *testing.T) {
	t.Parallel()

	got := compactingRun(t, nil,
		compactingStartLine, compactBoundaryLine, compactingFailLine, compactingEndLine,
		`{"type":"system","subtype":"status","status":{"phase":"compacting"}}`,
		compactingResultLine,
	)
	for i, tr := range got {
		if tr == "unrecognized" {
			t.Fatalf("event %d is an unrecognized_message; the whole compaction family arrives as "+
				"`system` subtypes, so every one of them is claimed by emitSystemSubtype or dropped "+
				"by ignoredLineTypes and NONE of them may reach a client as a noise row\ntrace: %v",
				i, got)
		}
	}
}

// TestParser_CompactingLogsClaudesFailureTextBounded pins the one place this
// ticket lets claude's own bytes into a log, and the bound on it.
//
// The exception is deliberate and argued at emitCompactingStatus: emitUnrecognized
// states a package-wide "never the content itself" rule, and #2224 is what makes
// this defensible rather than a drift — decodeAssistantError already bounds
// claude-authored error text at 256 bytes and publishes it ON THE WIRE, so a
// bounded copy in a Debug the production daemon does not print is strictly less
// exposure than the package already ships. This test is what keeps the bound real.
func TestParser_CompactingLogsClaudesFailureTextBounded(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("e", maxCompactField+64)
	rec := &logRecorder{}
	compactingRun(t, rec, compactingStartLine,
		`{"type":"system","subtype":"status","status":null,"compact_result":"failed",`+
			`"compact_error":"`+long+`"}`)

	records := rec.withMessage(compactingEndedMsg)
	if len(records) != 1 {
		t.Fatalf("records with message %q = %d, want 1 (the falling edge logs once, and only "+
			"the falling edge logs at all)", compactingEndedMsg, len(records))
	}
	attrs := records[0].attrs
	if got := attrs["compact_result"]; got != "failed" {
		t.Errorf("compact_result = %q, want %q", got, "failed")
	}
	if got := len(attrs["compact_error"]); got != maxCompactField {
		t.Errorf("len(compact_error) = %d, want %d (truncateField's cap, applied before the "+
			"value reaches the logger)", got, maxCompactField)
	}
	// Nothing else from the line. The value a drop site is most tempted to explain
	// itself with here is the metadata, and it is not on this line at all — but
	// asserting the attribute set is closed is what stops a later field being added
	// without the cap decision being made again.
	for k := range attrs {
		switch k {
		case "compact_result", "compact_error", "truncated":
		default:
			t.Errorf("unexpected log attribute %q: this record carries the two capped fields and "+
				"nothing else claude authored", k)
		}
	}
}

// TestParser_CompactingRisingEdgeLogsNothingFromTheLine keeps the exception above
// as narrow as it is written. The rising edge has no claude-authored field worth
// logging, so it logs nothing at all, and a future arm that starts explaining
// itself with the line's contents fails here rather than in review.
func TestParser_CompactingRisingEdgeLogsNothingFromTheLine(t *testing.T) {
	t.Parallel()

	rec := &logRecorder{}
	compactingRun(t, rec, compactingStartLine)
	if got := rec.all(); len(got) != 0 {
		t.Fatalf("the rising edge emitted %d log record(s), want 0: %v", len(got), got)
	}
}

// compactingStartLineWithOutcome is a RISING line carrying the two keys the
// closing line carries. Constructed rather than observed, and that is the point:
// a rising-edge row fed compactingStartLine could not fail, because that line has
// no compact_result or compact_error to leak. #2236's AC 2 is a claim about what
// the rising arm does with fields it CAN see, so the line has to have them.
const compactingStartLineWithOutcome = `{"type":"system","subtype":"status","status":"compacting",` +
	`"compact_result":"success","compact_error":"boom"}`

// compactingEdges runs a sequence and returns only the Compacting events, in
// order. compactingRun's trace projection collapses the pair to two tokens, which
// is right for the rows about WHICH edges fire and useless for the rows about what
// they carry; compactingEvents returns every event, so a caller would re-filter.
func compactingEdges(t *testing.T, lines ...string) []turnevent.Compacting {
	t.Helper()
	var got []turnevent.Compacting
	for _, ev := range compactingEvents(t, lines...) {
		if c, ok := ev.(turnevent.Compacting); ok {
			got = append(got, c)
		}
	}
	return got
}

// TestParser_CompactingFallingEdgeCarriesClaudesOutcome is #2236's AC 1 and AC 3.
//
// The two values are the ones emitCompactingStatus has decoded and capped since
// #2227; what this ticket changes is which sink they reach, so the rows here assert
// the VALUES rather than re-deriving the bound. That the bound itself is unchanged
// is proved by TestParser_CompactingLogsClaudesFailureTextBounded still passing
// untouched — same constant, same truncateField call, same two locals, now used
// twice instead of once.
func TestParser_CompactingFallingEdgeCarriesClaudesOutcome(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("e", maxCompactField+64)
	tests := []struct {
		name       string
		lines      []string
		wantResult string
		wantError  string
		why        string
	}{
		{
			name:       "a failed compaction names itself",
			lines:      []string{compactingStartLine, compactingFailLine},
			wantResult: "failed",
			wantError:  "context window still over budget",
			why: "AC 1, and the whole ticket. Before this the frame carried a bare boolean, so a " +
				"failed compaction and a successful one were indistinguishable everywhere a client " +
				"can see and the only diagnostic was a Debug the production daemon does not print",
		},
		{
			name:       "a successful compaction carries its result and no error",
			lines:      []string{compactingStartLine, compactingEndLine},
			wantResult: "success",
			wantError:  "",
			why: "AC 3's absent case. On the success path claude sends no compact_error key at " +
				"all, and an absent string decodes to the zero value — the edge still fires",
		},
		{
			name: "both present and both empty",
			lines: []string{compactingStartLine,
				`{"type":"system","subtype":"status","status":null,"compact_result":"","compact_error":""}`},
			wantResult: "",
			wantError:  "",
			why: "AC 3's empty case. Absent, null and empty are one reading for both fields, which " +
				"is why systemStatusLine declares them as plain strings rather than pointers",
		},
		{
			name: "an oversized error is cut at the cap and the edge still fires",
			lines: []string{compactingStartLine,
				`{"type":"system","subtype":"status","status":null,"compact_result":"failed",` +
					`"compact_error":"` + long + `"}`},
			wantResult: "failed",
			wantError:  strings.Repeat("e", maxCompactField),
			why: "AC 3's oversized case. truncateField CUTS rather than dropping, unlike #2224's " +
				"maxTurnEndStopField: this is prose, where a cut sentence still reads as what it " +
				"is, not a token set where a cut token would match nothing while looking like one",
		},
		{
			name:       "the daemon's own reset carries neither",
			lines:      []string{compactingStartLine, compactingResultLine},
			wantResult: "",
			wantError:  "",
			why: "consumeLine's turn-boundary reset is the SECOND producer of a falling edge and it " +
				"has no claude line to read an outcome off. Empty here is a true statement: the " +
				"daemon closed the edge itself and claude reported nothing",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			edges := compactingEdges(t, tc.lines...)
			if len(edges) != 2 || edges[1].Active {
				t.Fatalf("edges = %+v, want a rising edge then a falling one", edges)
			}
			falling := edges[1]
			if falling.Result != tc.wantResult {
				t.Errorf("Result = %q, want %q\nwhy: %s", falling.Result, tc.wantResult, tc.why)
			}
			if falling.ErrorText != tc.wantError {
				t.Errorf("len(ErrorText) = %d, want %d (first difference matters more than the "+
					"bytes; got %.60q)\nwhy: %s",
					len(falling.ErrorText), len(tc.wantError), falling.ErrorText, tc.why)
			}
		})
	}
}

// TestParser_CompactingRisingEdgeCarriesNeitherField is AC 2.
//
// It is the field-level sibling of TestParser_CompactingRisingEdgeLogsNothingFromThe
// Line, and it is non-vacuous for the same reason that one is not: the line it feeds
// CARRIES both keys, so an arm that started copying them fails here. A rising edge
// reporting an outcome would be a claim about a compaction that has not finished.
func TestParser_CompactingRisingEdgeCarriesNeitherField(t *testing.T) {
	t.Parallel()

	edges := compactingEdges(t, compactingStartLineWithOutcome)
	if len(edges) != 1 || !edges[0].Active {
		t.Fatalf("edges = %+v, want exactly one rising edge", edges)
	}
	if got := edges[0]; got.Result != "" || got.ErrorText != "" {
		t.Errorf("rising edge = %+v, want Result and ErrorText empty: the rising arm reads Status "+
			"and nothing else, and the line it was fed carries both keys precisely so that a copy "+
			"would show up here", got)
	}
}

// The two `user` lines a real compact turn puts on the wire AFTER the falling
// edge, transcribed from the capture record #2229's probe wrote during the
// 2026-09-08 real-claude lap against claude 2.1.259 (recovered from that run's
// artifact directory, because the fixture the probe wrote in-repo went out with
// the detached worktree; #2236 landed those recovered bytes and filled
// compactionPinnedShapes from them, so the capture is committed now and these
// literals are no longer the only record of the shape). Both carry
// `message.content` as a JSON STRING rather than the block
// array streamMessage declares, which is the property that matters here — it is
// what makes streamLine's decode fail and so what kept isSynthetic, the flag the
// parser has suppressed harness prose on since #2087, from ever being consulted.
//
// The envelope keys and the flag values are byte-exact from the capture. The
// summary PROSE is elided: the captured line ran 3182 bytes, and the discriminator
// this file asserts on is the envelope, never the text, so carrying the whole
// summary would buy nothing and put a page of claude's conversation transcript in
// a test file. Eliding it cannot make the row vacuous — a shortened body is still
// a string body, so it still fails the same decode and still reaches the same arm.
const (
	// isSynthetic:true, isReplay:false — claude's compaction summary, re-seeding
	// the context as a message the person never wrote.
	compactSummaryUserLine = `{"type":"user","message":{"role":"user","content":` +
		`"This session is being continued from a previous conversation that ran out of context. ` +
		`The summary below covers the earlier portion of the conversation.\n\nSummary:\n1. Primary ` +
		`Request and Intent:\n   [elided]\n"},"session_id":"s","parent_tool_use_id":null,` +
		`"uuid":"e9d8ca32-d112-4cfd-876b-656936a4ee31","timestamp":"2026-09-08T03:44:00.501Z",` +
		`"isReplay":false,"isSynthetic":true}`
	// isReplay:true, and NO isSynthetic key at all — the harness echoing the slash
	// command's own stdout. The two lines carry different flags, which is why the
	// arm reads both and neither trigger subsumes the other.
	compactStdoutUserLine = `{"type":"user","message":{"role":"user","content":` +
		`"<local-command-stdout>Compacted </local-command-stdout>"},"session_id":"s",` +
		`"parent_tool_use_id":null,"uuid":"24a6df51-e9e5-4657-bee9-1baf429b91dc",` +
		`"timestamp":"2026-09-08T03:44:00.614Z","isReplay":true}`
	// The same shape carrying NEITHER flag. Not observed — constructed to pin the
	// arm's narrowness, because a suppression that swallowed every string-content
	// user line would be the failure emitUnrecognized exists to prevent.
	compactUnflaggedUserLine = `{"type":"user","message":{"role":"user","content":"plain prose"},` +
		`"session_id":"s","uuid":"f0f0f0f0-0000-0000-0000-00000000f0f0"}`
)

// compactBoundaries runs a sequence and returns only the CompactionBoundary
// events, in order — compactingEdges' sibling for #2237's frame.
func compactBoundaries(t *testing.T, lines ...string) []turnevent.CompactionBoundary {
	t.Helper()
	var got []turnevent.CompactionBoundary
	for _, ev := range compactingEvents(t, lines...) {
		if b, ok := ev.(turnevent.CompactionBoundary); ok {
			got = append(got, b)
		}
	}
	return got
}

// tokenCount renders one count pointer for a failure message, keeping nil and zero
// visibly apart. Every assertion below turns on that distinction, so a message that
// printed both as "0" would describe the failure it is reporting incorrectly.
func tokenCount(p *int) string {
	if p == nil {
		return "absent"
	}
	return strconv.Itoa(*p)
}

// TestParser_CompactBoundaryPublishesTriggerAndCounts is #2237's AC 1, AC 3 and
// AC 4: which boundary lines produce a frame, and what each frame carries.
//
// THE ABSENT-VERSUS-ZERO PAIR IS THE TICKET. Rows 2 and 3 feed the same line shape
// with post_tokens omitted and with post_tokens explicitly 0, and they are two rows
// rather than one precisely so neither can pass while the other fails: a plain int
// field satisfies row 3 and quietly fails row 2, which is the "24k → 0 tokens"
// rendering AC 1 exists to forbid.
//
// The rows assert on constants transcribed from the committed capture rather than on
// numbers retyped here, so a literal edited to match a mapping would have to edit the
// capture's own values to stay green.
func TestParser_CompactBoundaryPublishesTriggerAndCounts(t *testing.T) {
	t.Parallel()

	pre, post := compactBoundaryPreTokens, compactBoundaryPostToken
	zero := 0
	longTrigger := strings.Repeat("t", maxCompactTrigger+1)

	tests := []struct {
		name        string
		lines       []string
		wantFrames  int
		wantTrigger string
		wantPre     *int
		wantPost    *int
		why         string
	}{
		{
			name:       "the observed line publishes the trigger and both counts",
			lines:      []string{compactingStartLine, compactBoundaryLine, compactingEndLine},
			wantFrames: 1, wantTrigger: compactBoundaryTrigger, wantPre: &pre, wantPost: &post,
			why: "AC 1, over claude's own bytes. Before this the trigger and the counts reached " +
				"nothing at all: the line was the one measured-and-dropped subtype left standing",
		},
		{
			name: "an absent post_tokens crosses as absent, not as zero",
			lines: []string{`{"type":"system","subtype":"compact_boundary",` +
				`"compact_metadata":{"trigger":"auto","pre_tokens":23600}}`},
			wantFrames: 1, wantTrigger: "auto", wantPre: &pre, wantPost: nil,
			why: "AC 1's whole point. post_tokens is optional in claude's own shape, and the " +
				"committed capture happens to carry it — so absence is a hermetic row's job. A " +
				"client rendering \"24k → 0 tokens\" here is the failure the criterion names",
		},
		{
			name: "an explicit zero post_tokens crosses as zero, not as absent",
			lines: []string{`{"type":"system","subtype":"compact_boundary",` +
				`"compact_metadata":{"trigger":"auto","pre_tokens":23600,"post_tokens":0}}`},
			wantFrames: 1, wantTrigger: "auto", wantPre: &pre, wantPost: &zero,
			why: "the other half of the pair, and the row a plain int field passes. Both rows " +
				"green is the only state in which present-versus-absent has actually survived",
		},
		{
			name:       "a boundary line following no compacting edge is still published",
			lines:      []string{compactBoundaryLine},
			wantFrames: 1, wantTrigger: compactBoundaryTrigger, wantPre: &pre, wantPost: &post,
			why: "AC 4. Whether an AUTO compaction announces itself with the same status lines is " +
				"unmeasured — the capture drove a manual /compact — so the arm reads no parser " +
				"state at all and a boundary with nothing before it maps identically",
		},
		{
			name:       "a line carrying no compact_metadata produces no frame",
			lines:      []string{`{"type":"system","subtype":"compact_boundary","session_id":"s"}`},
			wantFrames: 0,
			why: "AC 3. A frame carrying no trigger and no count is a claim with no content; the " +
				"metadata pointer is the presence discriminator, and the line is still CONSUMED",
		},
		{
			name:       "an undecodable boundary line produces no frame",
			lines:      []string{`{"type":"system","subtype":"compact_boundary","compact_metadata":[1,2]}`},
			wantFrames: 0,
			why: "AC 3, per emitCompactingStatus's undecodable arm: a line this parser cannot read " +
				"is not evidence of anything, and inventing the observation would be worse than " +
				"missing it",
		},
		{
			name: "a count claude sent out of int range costs the whole frame",
			lines: []string{`{"type":"system","subtype":"compact_boundary",` +
				`"compact_metadata":{"trigger":"manual","pre_tokens":1e400,"post_tokens":3}}`},
			wantFrames: 0,
			why: "the fail-closed direction, pinned. One absurd field fails the line's whole decode " +
				"rather than half of it, so the frame is lost instead of arriving half-true. Losing " +
				"a frame is recoverable; a frame asserting numbers claude did not state is not",
		},
		{
			name: "an oversized trigger is dropped and the counts still cross",
			lines: []string{`{"type":"system","subtype":"compact_boundary","compact_metadata":` +
				`{"trigger":"` + longTrigger + `","pre_tokens":23600,"post_tokens":2612}}`},
			wantFrames: 1, wantTrigger: "", wantPre: &pre, wantPost: &post,
			why: "the bound DROPS rather than cuts, unlike Compacting.ErrorText beside it: this is " +
				"a token a client matches, where a cut token would match no known value while " +
				"looking like one. The counts are unaffected — the bound is per field, not per frame",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := compactBoundaries(t, tc.lines...)
			if len(got) != tc.wantFrames {
				t.Fatalf("frames = %d, want %d\nwhy: %s", len(got), tc.wantFrames, tc.why)
			}
			if tc.wantFrames == 0 {
				return
			}
			b := got[0]
			if b.Trigger != tc.wantTrigger {
				t.Errorf("Trigger = %q, want %q\nwhy: %s", b.Trigger, tc.wantTrigger, tc.why)
			}
			if !sameCount(b.PreTokens, tc.wantPre) {
				t.Errorf("PreTokens = %s, want %s\nwhy: %s",
					tokenCount(b.PreTokens), tokenCount(tc.wantPre), tc.why)
			}
			if !sameCount(b.PostTokens, tc.wantPost) {
				t.Errorf("PostTokens = %s, want %s\nwhy: %s",
					tokenCount(b.PostTokens), tokenCount(tc.wantPost), tc.why)
			}
		})
	}
}

// sameCount compares two count pointers by PRESENCE first and value second, which
// is the comparison the whole ticket turns on. Written out rather than reached with
// reflect.DeepEqual so the nil-versus-zero case is visibly the first branch.
func sameCount(got, want *int) bool {
	if got == nil || want == nil {
		return got == nil && want == nil
	}
	return *got == *want
}

// TestParser_CompactBoundaryCarriesNothingElseFromTheMetadata is #2237's AC 2 at
// the producer: the allowlist, asserted over claude's own bytes.
//
// It is a tripwire rather than a proof, and saying so is the honest framing. The
// real guarantee is STRUCTURAL — turnevent.CompactionBoundary has three fields and
// systemCompactBoundaryLine declares three, so encoding/json discards every other
// key including ones claude has not shipped yet. What this test catches is a FUTURE
// field being added to either without the exclusion decision being made again, and
// the identifiers it would leak are the ones that matter most: three uuids naming
// entries in the operator's own transcript, unredacted in the committed capture.
func TestParser_CompactBoundaryCarriesNothingElseFromTheMetadata(t *testing.T) {
	t.Parallel()

	got := compactBoundaries(t, compactBoundaryLine)
	if len(got) != 1 {
		t.Fatalf("frames = %d, want 1", len(got))
	}
	rendered, err := json.Marshal(got[0])
	if err != nil {
		t.Fatalf("marshalling the event: %v", err)
	}
	// Every identifier the observed line carries and this frame must not. Taken from
	// the same literal the parser was fed, so a capture re-taken at a new claude
	// release moves both ends together.
	for _, id := range []string{
		"$SESSION_ID",
		"7d31f0bb-482d-45c0-bf8a-2ef2cd2ccfc0", // the line's own uuid
		"ae93ee09-99cd-472e-a34b-10278c8aa85a", // preserved_segment.head_uuid
		"e9d8ca32-d112-4cfd-876b-656936a4ee31", // preserved_segment.anchor_uuid
		"134c965c-edc1-45bf-8cc7-e2aa0198c829", // tail_uuid and logical_parent_uuid
		"20988",                                // cumulative_dropped_tokens
		"15596",                                // duration_ms
	} {
		if strings.Contains(string(rendered), id) {
			t.Errorf("the frame carries %q, which is on the line but not on the allowlist. The "+
				"uuids name entries in the operator's own transcript; the two counts are simply "+
				"unasked-for, and an unused field is a claim nobody checks", id)
		}
	}
}

// compactingEvents is compactingRun's sibling for the rows that assert on an
// event's FIELDS rather than on its kind. The trace projection collapses every
// Unrecognized to one token, which is exactly the information a reader needs here
// — #2227's live gate failed with "2 unrecognized_message frame(s)" and no way to
// tell which lines they were, and that cost a whole gate lap.
func compactingEvents(t *testing.T, lines ...string) []turnevent.Event {
	t.Helper()
	var got []turnevent.Event
	p := NewParser(func(ev turnevent.Event) { got = append(got, ev) }, discardLogger())
	for _, line := range lines {
		if _, err := p.Write([]byte(line + "\n")); err != nil {
			t.Fatalf("Write(%.40s) err = %v, want nil", line, err)
		}
	}
	return got
}

// TestParser_CompactTurnUserLinesStaySilent is AC 2 over the half of the compact
// turn the plan got wrong.
//
// The plan argued AC 2 was STRUCTURAL: every compaction line arrives as a `system`
// subtype, and an unmapped `system` subtype is dropped by ignoredLineTypes rather
// than surfaced, so zero unrecognized frames followed from the seam. #2227's live
// gate falsified that — the compact turn emitted two frames, and the census of the
// same run names them: alongside the two `system/status` lines and the
// `system/compact_boundary`, the turn carries `user: 2`. Neither is a compaction
// line by subtype; both are consequences of one.
//
// They reach emitUnrecognized as UNDECODABLE, not as an unknown user block, and
// that distinction is the fix. streamMessage.Content is []json.RawMessage; both
// lines carry content as a string, so json.Unmarshal fails on the whole line and
// consumeLine's undecodable branch fires BEFORE emitUser is ever called. The
// isSynthetic guard that would have dropped the summary was already there and
// already correct — it was simply unreachable behind a decode that never got that
// far. The frame that reached the wire carried the line's own bytes as Raw, so a
// compact turn put claude's entire conversation summary into a client noise row.
func TestParser_CompactTurnUserLinesStaySilent(t *testing.T) {
	t.Parallel()

	got := compactingEvents(t,
		compactingStartLine, compactingEndLine, compactBoundaryLine,
		compactSummaryUserLine, compactStdoutUserLine, compactingResultLine,
	)
	for i, ev := range got {
		u, ok := ev.(turnevent.Unrecognized)
		if !ok {
			continue
		}
		t.Fatalf("event %d is an unrecognized_message (site=%q kind=%q raw=%.60q); the compact "+
			"turn's own user lines are harness-authored — the summary re-seeding the context and "+
			"the slash command's stdout echo — and neither is the person's speech or the model's",
			i, u.Site, u.Kind, u.Raw)
	}
	// Non-vacuity: the arm must still have suppressed something, or a row that
	// stopped producing these lines at all would read as a pass.
	if len(got) == 0 {
		t.Fatal("no events at all; the sequence should still produce both compacting edges")
	}
}

// TestParser_CompactTurnUnflaggedUserLineStillSurfaces is the other half, and the
// one that keeps the suppression from becoming a blanket. A string-content user
// line carrying NEITHER isSynthetic nor isReplay is not something the harness has
// claimed authorship of, so it stays visible — the same fail-open direction
// emitUser takes for an unknown block type, and the reason the arm reads two flags
// by value instead of matching the shape alone.
func TestParser_CompactTurnUnflaggedUserLineStillSurfaces(t *testing.T) {
	t.Parallel()

	got := compactingEvents(t, compactUnflaggedUserLine)
	if len(got) != 1 {
		t.Fatalf("got %d event(s), want exactly 1 unrecognized_message: %v", len(got), got)
	}
	u, ok := got[0].(turnevent.Unrecognized)
	if !ok {
		t.Fatalf("got %T, want turnevent.Unrecognized", got[0])
	}
	if u.Site != turnevent.UnrecognizedUndecodable {
		t.Errorf("site = %q, want %q: the line genuinely does not fit the block model, and "+
			"reporting where it failed is the honest answer", u.Site, turnevent.UnrecognizedUndecodable)
	}
}

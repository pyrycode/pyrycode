package streamsup

import (
	"log/slog"
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
	compactBoundaryLine = `{"type":"system","subtype":"compact_boundary",` +
		`"compact_metadata":{"trigger":"manual","pre_tokens":120000,"post_tokens":18000}}`
	compactingResultLine = `{"type":"result","subtype":"success"}`
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
			name:  "compact_boundary is not an edge",
			lines: []string{compactingStartLine, compactBoundaryLine, compactingEndLine},
			want:  []string{"compacting:true", "compacting:false"},
			why: "the metadata line is #2228's, and it carries nothing this ticket publishes. It " +
				"stays dropped — silently, because `system` is on ignoredLineTypes, which is why " +
				"leaving it unmapped costs no unrecognized_message",
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

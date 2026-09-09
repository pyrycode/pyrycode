package streamsup

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// #2249 — the committed real-claude reading of claude's usage-limit WARNING BAND,
// replayed through the parser from this package.
//
// It rides capturedInitialize rather than minting a reader of its own, which is the
// one way this file departs from permission_denial_capture_test.go's shape. The
// reason is in initCaptureRecord's doc at the field it reads: every reader in this
// package decodes a DIFFERENT record shape, which is what makes reaching for the
// wrong one yield zero values rather than a failure, and a second reader over the
// initialize record would be one more parallel struct of a type behind the
// e2e_realclaude tag. The convention's actual ban — on growing a reader a PATH
// PARAMETER, since the provenance assertions are what stop a hand-built file being
// swapped in behind them — is not touched: capturedInitialize keeps its closed arm
// selector, mints its own path, and every check below the lines is inherited rather
// than copied weaker. That reader's own file doc says it exists so the provenance
// discipline is written once while several decode slices ride it; this is one more.
//
// WHAT THE FOUR ARMS MEASURE, and it is not one thing. Each carries exactly one
// rate_limit_event line. The base arm's reads status allowed_warning against
// limit_type seven_day with utilization 0.94 — the only non-benign status on record
// across four claude versions, and the only record carrying the key at all. The
// other three read the benign allowed, carry NO utilization key, and are therefore
// both the absence-is-the-measured-case evidence and the non-vacuity control: a
// mapping that emitted nothing would satisfy the base arm's count only if its pin
// were also zero, and nothing would say it should not be. That is
// denialCapturePinnedCounts' control_bypass row, reused.

// rateLimitCapturePinnedEvents IS THE MEASUREMENT THIS TICKET COMMITS: how many
// turnevent.RateLimited each arm's record yields once emitRateLimit's gate has run.
//
// THE THREE ZEROES ARE LOAD-BEARING, for the reason above. They are not "no data" —
// each of those arms does carry a rate_limit_event, and the zero is the gate
// correctly silencing a benign one, which the re-derivation below distinguishes from
// a record that simply held no such line.
var rateLimitCapturePinnedEvents = map[string]int{
	initCaptureArmBase:               1,
	initCaptureArmBeforeFirstTurn:    0,
	initCaptureArmAfterCompletedTurn: 0,
	initCaptureArmNoRequest:          0,
}

// rateLimitCaptureArmName renders an arm for a subtest name and a failure message.
// The base arm's identifier is the empty string — what distinguishes #1688's unarmed
// record — and an unnamed subtest would report a failure against nothing a reader
// could look up.
func rateLimitCaptureArmName(arm string) string {
	if arm == "" {
		return "base"
	}
	return arm
}

// capturedRateLimitInfo re-derives one captured line's rate_limit_info from the
// LINE'S OWN BYTES. Derived here rather than read from the record's summary fields,
// and declared separately from the production rateLimitInfo it mirrors: a test that
// decoded through the production target could not catch that target dropping a key,
// which is the whole claim this file makes.
//
// Utilization is a pointer for the same reason it is one in production — an absent
// key must be distinguishable from a reported zero — and here that pointer is also
// what lets the benign arms assert an ABSENCE rather than a value.
type capturedRateLimitInfo struct {
	Status      string   `json:"status"`
	LimitType   string   `json:"rateLimitType"`
	ResetsAt    int64    `json:"resetsAt"`
	Utilization *float64 `json:"utilization"`
}

// capturedInitializeStdoutLines returns one arm's recorded stdout lines as claude's
// own bytes, with the RECORDER's whitespace removed and nothing else.
//
// The compaction is #2234's, and its argument carries over unchanged: the record is
// written by an indenting encoder, so each stored line comes back spread over
// several physical lines, and the parser is line-oriented — it would read each
// fragment as a line of its own, turning one event into a dozen undecodable rows.
// json.Compact removes insignificant whitespace only, so key ORDER and every value
// byte survive, which is what keeps this a replay of claude's line rather than a
// re-encoding of somebody's decode of it.
//
// Every failure is t.Fatalf, never a skip: the captures are committed, so a record
// with no stdout lines is a broken premise rather than an unavailable resource, and
// a replay over none is vacuous — every assertion built on it would pass without
// reading a byte claude sent.
func capturedInitializeStdoutLines(t *testing.T, arm string) []json.RawMessage {
	t.Helper()

	rec, _ := capturedInitialize(t, arm)
	if len(rec.StdoutEvents) == 0 {
		t.Fatalf("%s: the record holds ZERO stdout lines", initCapturePath(arm))
	}

	lines := make([]json.RawMessage, 0, len(rec.StdoutEvents))
	for i, event := range rec.StdoutEvents {
		var compact bytes.Buffer
		if err := json.Compact(&compact, event); err != nil {
			t.Fatalf("%s: stdout line %d is not valid JSON: %v", initCapturePath(arm), i, err)
		}
		lines = append(lines, json.RawMessage(compact.Bytes()))
	}
	return lines
}

// capturedRateLimitInfos re-derives which of an arm's lines are rate_limit_event
// lines, and what each one said, from each line's own envelope. The counts and
// values below are therefore a measurement of claude's bytes rather than an
// agreement with the pin they are compared against.
func capturedRateLimitInfos(t *testing.T, arm string) []capturedRateLimitInfo {
	t.Helper()

	var out []capturedRateLimitInfo
	for _, line := range capturedInitializeStdoutLines(t, arm) {
		var envelope struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(line, &envelope); err != nil {
			t.Fatalf("%s: a captured line does not decode as JSON: %v", initCapturePath(arm), err)
		}
		if envelope.Type != "rate_limit_event" {
			continue
		}
		var wrapper struct {
			Info capturedRateLimitInfo `json:"rate_limit_info"`
		}
		if err := json.Unmarshal(line, &wrapper); err != nil {
			t.Fatalf("%s: a captured rate_limit_event does not decode: %v", initCapturePath(arm), err)
		}
		out = append(out, wrapper.Info)
	}
	return out
}

// capturedLineTypeIndexes re-derives WHERE in one arm's recorded stdout the lines of
// a given top-level type sit, from each line's own envelope.
//
// Positions rather than a count, because the two callers below need an ORDER between
// two different types — a reading and the turn boundary that follows it — and a pair
// of counts cannot express that. Same re-derivation discipline capturedRateLimitInfos
// applies to the readings themselves: what is asserted about the record is measured
// from the record.
func capturedLineTypeIndexes(t *testing.T, arm, typ string) []int {
	t.Helper()

	var out []int
	for i, line := range capturedInitializeStdoutLines(t, arm) {
		var envelope struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(line, &envelope); err != nil {
			t.Fatalf("%s: a captured line does not decode as JSON: %v", initCapturePath(arm), err)
		}
		if envelope.Type == typ {
			out = append(out, i)
		}
	}
	return out
}

// replayInitializeCapture feeds every line of one arm through ONE parser, in the
// order claude wrote them, and returns the usage-limit events it emitted beside the
// count of turnevent.Unrecognized the same replay produced.
//
// One parser rather than one per line, #2234's correction: the parser holds
// cross-line state, production builds one per session, and a per-line parser
// replays a shape the bytes were never recorded from. These records carry whole
// turns — assistant text, tool calls, result lines — so that matters here for more
// than the one line under test.
func replayInitializeCapture(t *testing.T, arm string) (rateLimited []turnevent.RateLimited, unrecognized int) {
	t.Helper()
	return replayInitializeCaptures(t, arm)
}

// replayInitializeCaptures is the SAME replay over several arms in sequence, and it
// is the whole of #2250's fixture: one parser, several records' lines fed through it
// in the order given.
//
// It exists because no capture of a FALLING EDGE can be taken. That would need an
// account crossing a warning band and back inside one run, and claude reports the
// window once per run — so the sequence is COMPOSED from committed single-reading
// records instead. That is composition of captured bytes, not a hand-built payload:
// every line still comes from capturedInitializeStdoutLines, and the arm names still
// come from initCaptureArms' closed set. Deliberately NOT grown a path parameter,
// which is the one change that would put a hand-built file behind the provenance
// assertions capturedInitialize exists to enforce.
//
// The single-arm form above delegates here so there is ONE replay implementation and
// a change to it cannot make the two disagree.
func replayInitializeCaptures(t *testing.T, arms ...string) (rateLimited []turnevent.RateLimited, unrecognized int) {
	t.Helper()

	sink := func(ev turnevent.Event) {
		switch e := ev.(type) {
		case turnevent.RateLimited:
			rateLimited = append(rateLimited, e)
		case turnevent.Unrecognized:
			unrecognized++
		}
	}
	p := NewParser(sink, discardLogger())
	for _, arm := range arms {
		for _, line := range capturedInitializeStdoutLines(t, arm) {
			if _, err := p.Write(append(bytes.Clone(line), '\n')); err != nil {
				t.Fatalf("%s: Write err = %v, want nil", initCapturePath(arm), err)
			}
		}
	}
	return rateLimited, unrecognized
}

// TestParser_RateLimitWarningCaptureCarriesUtilization is #2249's AC 1: replaying
// the committed allowed_warning capture yields a turnevent.RateLimited carrying
// claude's utilization — 0.94 for that capture.
//
// Every wanted value is re-derived from the captured line's OWN bytes rather than
// written as a literal, so this measures claude's record instead of agreeing with a
// constant somebody could edit to match a replay nobody re-read. The guard that the
// captured reading is non-nil is what stops the comparison passing vacuously if a
// future re-capture drops the key: without it, nil == nil would read as success.
func TestParser_RateLimitWarningCaptureCarriesUtilization(t *testing.T) {
	t.Parallel()

	arm := initCaptureArmBase
	path := initCapturePath(arm)

	captured := capturedRateLimitInfos(t, arm)
	if len(captured) != 1 {
		t.Fatalf("%s: the record holds %d rate_limit_event lines, want exactly 1 — this test pins "+
			"the single warning-band reading on record and cannot say which of several it meant",
			path, len(captured))
	}
	want := captured[0]
	if want.Utilization == nil {
		t.Fatalf("%s: the captured rate_limit_info carries no utilization key, so every comparison "+
			"below would compare nil against nil and pass on a mapping that drops the field. This "+
			"capture is the ticket's whole premise; if a re-capture genuinely lost the key, that is a "+
			"documentation change and this test moves with it", path)
	}
	if want.Status == benignRateLimitStatus {
		t.Fatalf("%s: the captured status is the measured-benign %q, which the gate silences — the "+
			"replay below would yield nothing and this test would be asserting against an empty slice",
			path, benignRateLimitStatus)
	}

	got, _ := replayInitializeCapture(t, arm)
	if len(got) != rateLimitCapturePinnedEvents[arm] {
		t.Fatalf("%s: replaying the record produced %d RateLimited events, want %d — one for the "+
			"captured non-benign reading", path, len(got), rateLimitCapturePinnedEvents[arm])
	}

	ev := got[0]
	if ev.Utilization == nil {
		t.Fatalf("%s: the event carries no utilization. claude sent %v on this line, so the reading "+
			"is being dropped somewhere between rateLimitInfo and the event — which is the whole of "+
			"what a client needs to say how much of the window is spent", path, *want.Utilization)
	}
	if *ev.Utilization != *want.Utilization {
		t.Errorf("%s: utilization is %v, want claude's own %v", path, *ev.Utilization, *want.Utilization)
	}
	// The two strings ride along because a utilization attached to the wrong reading
	// is worth nothing: this pins that the number arrived on the event describing the
	// line that carried it, not on some other.
	if ev.Status != want.Status {
		t.Errorf("%s: status is %q, want claude's own %q", path, ev.Status, want.Status)
	}
	if ev.LimitType != want.LimitType {
		t.Errorf("%s: limit_type is %q, want claude's own rateLimitType %q", path, ev.LimitType, want.LimitType)
	}
	if ev.ResetsAt != want.ResetsAt {
		t.Errorf("%s: resets_at is %d, want claude's own %d", path, ev.ResetsAt, want.ResetsAt)
	}
	// Nothing in the capture is near maxRateLimitField, so a cut here would mean a cap
	// moved under the observation.
	if ev.TruncatedFields != nil {
		t.Errorf("%s: the event reports %v truncated, want nil — no captured value is near its cap",
			path, ev.TruncatedFields)
	}
}

// TestParser_RateLimitBenignCaptureArmsReportNoUtilization is the other half of the
// measurement, and the half the wire shape's pointer exists for: ABSENCE IS THE
// MEASURED CASE.
//
// The three sibling arms each carry a rate_limit_event whose status is the benign
// value and which carries no utilization key at all. Two things are asserted, and
// they are independent. The records' own bytes must show the key genuinely absent —
// which is the evidence that a reading claude omits is ordinary rather than
// exceptional, and therefore that collapsing absence into 0 would misreport the
// common case. And the replay must yield no event, because the gate silences the
// benign status; that zero is what stops the sibling test above from passing on a
// mapping that emits nothing.
//
// Deliberately NOT a claim that the gate is what suppressed them: a record holding
// no rate_limit_event at all would also yield zero, so the line count is re-derived
// first and the zero only means what it says once that is non-zero.
func TestParser_RateLimitBenignCaptureArmsReportNoUtilization(t *testing.T) {
	t.Parallel()

	// Counted in the PARENT, synchronously, rather than inside the parallel subtests:
	// three goroutines incrementing one int is a data race, and -race would report it
	// as a finding of this file rather than of the code under test.
	benign := 0
	for _, arm := range initCaptureArms {
		if rateLimitCapturePinnedEvents[arm] != 0 {
			continue
		}
		benign++
		t.Run(rateLimitCaptureArmName(arm), func(t *testing.T) {
			t.Parallel()
			path := initCapturePath(arm)

			captured := capturedRateLimitInfos(t, arm)
			if len(captured) == 0 {
				t.Fatalf("%s: the record holds NO rate_limit_event line, so the zero below would "+
					"be the absence of input rather than the gate's verdict", path)
			}
			for i, info := range captured {
				if info.Status != benignRateLimitStatus {
					t.Fatalf("%s: rate_limit_event %d reads status %q, not the benign %q — this arm "+
						"is pinned at zero events, so a non-benign reading here means the pin and "+
						"the fixture disagree and one of them must move", path, i, info.Status,
						benignRateLimitStatus)
				}
				if info.Utilization != nil {
					t.Errorf("%s: rate_limit_event %d carries utilization %v. Every benign record on "+
						"file omits the key, and that absence is what the pointer on the event and "+
						"on the wire exists to carry; a benign reading that now reports one is a "+
						"claude shape change worth documenting", path, i, *info.Utilization)
				}
			}

			got, _ := replayInitializeCapture(t, arm)
			if len(got) != 0 {
				t.Errorf("%s: replaying the record produced %d RateLimited events, want 0 — the "+
					"captured status is the measured-benign value, so a healthy run must stay silent\n%+v",
					path, len(got), got)
			}
		})
	}
	if benign != 3 {
		t.Errorf("swept %d benign arms, want 3 — a control over fewer proves less than it claims "+
			"about the warning arm's count", benign)
	}
}

// TestParser_RateLimitFallingEdgeAcrossComposedCaptures is #2250's first three
// criteria in one pass over nothing but committed bytes: a non-benign reading, a
// turn boundary, then two benign readings, all through ONE parser.
//
// THE SEQUENCE IS COMPOSED, NOT CAPTURED, and the distinction is the fixture's whole
// design — see replayInitializeCaptures. The three arms are chosen for what their
// records already contain rather than for variety: the base arm carries the single
// allowed_warning reading on record and a result line LATER IN THE SAME RECORD, and
// each benign arm carries exactly one allowed reading. So base -> benign -> benign
// replays warning, boundary, clear, silence.
//
// EVERY PREMISE IS RE-DERIVED FROM THE RECORDS' OWN BYTES, and three of them exist
// to stop the conclusion passing vacuously. The boundary must sit AFTER the reading
// in the base arm, or the turn-boundary criterion is asserted over a record where no
// boundary intervened. The two arms' limit types must DIFFER, or the assertion that
// the clearing event carries the clearing reading's fields — rather than the
// remembered warning's — compares two equal values and cannot fail. And the warning
// reading must carry a utilization while the clearing one does not, for the same
// reason one field further on.
func TestParser_RateLimitFallingEdgeAcrossComposedCaptures(t *testing.T) {
	t.Parallel()

	const (
		warnArm  = initCaptureArmBase
		clearArm = initCaptureArmBeforeFirstTurn
		quietArm = initCaptureArmAfterCompletedTurn
	)

	warned := capturedRateLimitInfos(t, warnArm)
	if len(warned) != 1 || warned[0].Status == benignRateLimitStatus {
		t.Fatalf("%s: the record holds %d rate_limit_event lines and the first reads status %q, want "+
			"exactly 1 carrying a NON-benign status — this arm is the falling edge's opening reading",
			initCapturePath(warnArm), len(warned), warned[0].Status)
	}
	readings := capturedLineTypeIndexes(t, warnArm, "rate_limit_event")
	boundaries := capturedLineTypeIndexes(t, warnArm, "result")
	if len(boundaries) == 0 || boundaries[len(boundaries)-1] < readings[0] {
		t.Fatalf("%s: the record's result lines sit at %v and its reading at %v — no turn boundary "+
			"follows the reading, so a replay of this arm cannot say a boundary failed to clear the latch",
			initCapturePath(warnArm), boundaries, readings[0])
	}

	cleared := capturedRateLimitInfos(t, clearArm)
	if len(cleared) != 1 || cleared[0].Status != benignRateLimitStatus {
		t.Fatalf("%s: the record holds %d rate_limit_event lines and the first reads status %q, want "+
			"exactly 1 carrying the benign %q — this arm is the falling edge itself",
			initCapturePath(clearArm), len(cleared), cleared[0].Status, benignRateLimitStatus)
	}
	if quiet := capturedRateLimitInfos(t, quietArm); len(quiet) != 1 || quiet[0].Status != benignRateLimitStatus {
		t.Fatalf("%s: want exactly 1 rate_limit_event carrying the benign %q — this arm is the SECOND "+
			"benign reading, and the fire-once claim is empty if it carries none",
			initCapturePath(quietArm), benignRateLimitStatus)
	}
	if cleared[0].LimitType == warned[0].LimitType {
		t.Fatalf("both arms name limit_type %q, so asserting the clearing event carries the clearing "+
			"reading's value cannot distinguish it from the remembered warning's. On record these differ "+
			"(seven_day against five_hour), which is itself a client-visible fact", cleared[0].LimitType)
	}
	if warned[0].Utilization == nil || cleared[0].Utilization != nil {
		t.Fatalf("the warning reading's utilization is %v and the clearing one's is %v, want a value "+
			"then an absence — every benign record on file omits the key, and the pair is what makes "+
			"the nil assertion below a measurement rather than a coincidence",
			warned[0].Utilization, cleared[0].Utilization)
	}

	got, unrecognized := replayInitializeCaptures(t, warnArm, clearArm, quietArm)
	if unrecognized != 0 {
		t.Errorf("the composed replay produced %d turnevent.Unrecognized events, want 0", unrecognized)
	}
	if len(got) != 2 {
		t.Fatalf("the composed replay produced %d RateLimited events, want exactly 2 — the warning, "+
			"then the falling edge on the FIRST benign reading and nothing on the second\n%+v", len(got), got)
	}

	if got[0].Status != warned[0].Status {
		t.Errorf("the first event's status is %q, want the warning arm's own %q", got[0].Status, warned[0].Status)
	}
	edge := got[1]
	if edge.Status != cleared[0].Status {
		t.Errorf("the falling edge's status is %q, want claude's own %q verbatim — the benign value IS "+
			"the client's discriminator, so a daemon-chosen label here would be unreadable",
			edge.Status, cleared[0].Status)
	}
	if edge.LimitType != cleared[0].LimitType {
		t.Errorf("the falling edge's limit_type is %q, want the CLEARING reading's %q. %q is the "+
			"remembered warning's, and republishing it would describe a window claude did not report",
			edge.LimitType, cleared[0].LimitType, warned[0].LimitType)
	}
	if edge.ResetsAt != cleared[0].ResetsAt {
		t.Errorf("the falling edge's resets_at is %d, want the clearing reading's %d (the warning's was %d)",
			edge.ResetsAt, cleared[0].ResetsAt, warned[0].ResetsAt)
	}
	if edge.Utilization != nil {
		t.Errorf("the falling edge carries utilization %v, want nil — the clearing reading omits the "+
			"key, and carrying the warning's %v forward would tell a client the window is still 94%% spent",
			*edge.Utilization, *warned[0].Utilization)
	}
	if edge.TruncatedFields != nil {
		t.Errorf("the falling edge reports %v truncated, want nil — no captured value is near its cap",
			edge.TruncatedFields)
	}
}

// TestParser_RateLimitCaptureCostsNoUnrecognizedRow is deliberately whole-record
// rather than rate-limit-only, and deliberately across all four arms.
//
// The claim is that widening the decode target costs nothing on the surfaced lane:
// rate_limit_event is claimed by consumeLine's own case arm, so it can never reach
// emitUnrecognized, and the new optional field must not change that for any OTHER
// line of four real turns either. A row here would put a noise event on the
// operator's timeline and, one tier up, in front of the live zero-unrecognized gate.
func TestParser_RateLimitCaptureCostsNoUnrecognizedRow(t *testing.T) {
	t.Parallel()

	for _, arm := range initCaptureArms {
		t.Run(rateLimitCaptureArmName(arm), func(t *testing.T) {
			t.Parallel()
			got, unrecognized := replayInitializeCapture(t, arm)
			if unrecognized != 0 {
				t.Errorf("%s: replaying the record produced %d turnevent.Unrecognized events, want 0",
					initCapturePath(arm), unrecognized)
			}
			if want := rateLimitCapturePinnedEvents[arm]; len(got) != want {
				t.Errorf("%s: %d RateLimited events, want %d — a zero-unrecognized claim over a "+
					"replay that mapped the wrong number of lines says nothing",
					initCapturePath(arm), len(got), want)
			}
		})
	}
}

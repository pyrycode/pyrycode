//go:build e2e_realclaude

package realclaude

// What a result trailer's terminal_reason MEANS given the runner path the run
// was observed to take: a pure predicate over three already-reduced readings,
// answering with one value from its own closed set of six.
//
// This file reaches no verdict about pyry and takes no measurement. Everything
// here runs offline: no live claude, no credentials, no daemon, no exec, no
// clock, no goroutine, no env gate, no t.Skip.
//
//	go test -race -tags e2e_realclaude -run '^TestTrail' -v ./internal/e2e/realclaude/
//
// # Why the PAIR of readings, and neither alone
//
// The fixed decode cannot separate an ABSENT terminal_reason from one emitted as
// "" — the argument is trailer_key_names_test.go:14-24 and is not re-derived
// here. #1357's key-name reading (result_trailer_observation_test.go:120-135)
// makes that separation, and it is still not enough on its own, because ABSENCE
// MEANS OPPOSITE THINGS ON THE TWO RUNNER PATHS:
//
//   - ptyrunner constructs the emitter itself (ptyrunner/runner.go:468), and
//     streamjson/emitter.go:383-391 is a chokepoint substituting the recorded
//     detail or "unclassified" before marshalling. On that path the field is
//     present and non-empty BY CONSTRUCTION, so absence is a departure.
//   - streamrunner.Run tees claude's stdout for the watchdog and passes the
//     bytes through UNCHANGED (streamrunner/runner.go:177-179), synthesising a
//     trailer of its own only when the idle-stall watchdog fired and claude
//     emitted no result. So on every healthy run of that path the trailer is
//     claude's OWN result line and carries no terminal_reason at all, and
//     absence is that path's healthy shape.
//
// The decoded scalar alone cannot see presence, and the key names alone cannot
// say what presence is worth. The pair can, which is why the predicate takes
// both plus the path.
//
// # The path reading, and the one that must NOT be used
//
// tdnRunnerFromArgv is the reading:
// streamrunner-positive on --input-format, ptyrunner-positive on --session-id,
// and three distinct indeterminate answers for both-or-neither-or-unread.
// reachRunnerPathFromArgv is deliberately
// NOT used — it keys on --append-system-prompt-file, which BOTH argv builders
// pass (cmd/pyry/`buildStreamRunnerClaudeArgs`, ptyrunner/runner.go:621), so it labels a
// correctly-wired stream run "ptyrunner" and has no streamrunner answer at all.
//
// # Scope
//
// Nothing here touches trailGate or trailClassifyRun; both consume this
// predicate from their own side. #1420 wired it into trailGate's absence branch,
// where its three ABSENCE answers decide which Detail the arm publishes, and
// #1417 took the owes-none answer further: on that reading the gate now answers
// trailGateAbsentOwesNone rather than trailGateOutOfContract, and
// trailClassifyRun's step-1 switch gained the matching arm in the same commit.
// The hazard the shipped paragraph here held open — a new gate value registered
// in trailIsGateValue but unhandled in that switch, which no closure test
// catches — is therefore discharged rather than deferred, and it is recorded at
// the switch itself (trail_run_outcome_test.go:830-1026) rather than here.
//
// This file's own six values are unchanged by either: they say what a
// terminal_reason MEANS against a path, and what a consumer does with that
// meaning is the consumer's decision.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// --- the value space ----------------------------------------------------------

// What a terminal_reason means against the observed runner path, as a closed set
// of six. Every value carries a `reason-` prefix, and the prefix is load-bearing
// for the same reason `gate-` is (trailer_admissibility_test.go:95-101): this
// space's words mean nearly what the gate's, the admit's and the run outcomes'
// words mean, so a copy-paste between spaces must be a visible mistake rather
// than a plausible line.
//
// Absent / Blank / Named are three different words rather than three inflections
// of "present" on purpose: this space's whole point is refusing to collapse
// absence into emptiness, and two identifiers one tab-completion apart would
// make that collapse a plausible typo.
const (
	// trailReasonAbsentOwesNone: terminal_reason is off a line from the
	// streamrunner path, which owes none. That path's documented healthy shape.
	trailReasonAbsentOwesNone = "reason-absent-on-owes-none-path"
	// trailReasonPresentOwesNone: terminal_reason is ON a line from the
	// streamrunner path, which owes none.
	//
	// THE CLAIM LIMIT IS THE POINT OF THIS VALUE. It says the line is not that
	// path's documented healthy shape, and it never says pyry wrote it. Because
	// streamrunner.Run passes claude's bytes through unchanged
	// (internal/agentrun/streamrunner/runner.go:177-179), a terminal_reason on a
	// line from that path could equally be one CLAUDE emitted, and no reading of
	// the line can tell the two apart. Pyry's own synthesis there is
	// unconditional when it happens (streamrunner/watchdog.go:253, :280), but
	// that is a statement about what pyry writes and never about what claude
	// cannot. A value claiming more would let claude's own output name pyry as
	// its author.
	//
	// EMPTINESS IS NOT DISTINGUISHED HERE, deliberately: a present-and-empty
	// terminal_reason from streamrunner is as much "not that path's documented
	// healthy shape" as a non-empty one, so this value absorbs both. Splitting it
	// in two would be inventing a distinction the owes-none path does not carry.
	trailReasonPresentOwesNone = "reason-present-on-owes-none-path"
	// trailReasonAbsentOwesOne: terminal_reason is off a line from the ptyrunner
	// path, which owes one by construction.
	trailReasonAbsentOwesOne = "reason-absent-on-owes-one-path"
	// trailReasonBlankOwesOne: terminal_reason is on a line from the ptyrunner
	// path and decodes empty — emitter.go:383-391's chokepoint substitutes the
	// recorded detail or "unclassified" rather than marshal an empty one, so its
	// guarantee is violated.
	trailReasonBlankOwesOne = "reason-blank-on-owes-one-path"
	// trailReasonNamedOwesOne: terminal_reason is on a line from the ptyrunner
	// path and names a reason. That path's documented healthy shape.
	trailReasonNamedOwesOne = "reason-named-on-owes-one-path"
	// trailReasonPathUnnamed: the path reading names no runner.
	//
	// This is a default arm and NOT the fall-through catch-all trailGate,
	// trailAdmitAttribution and trailClassifyRun each refuse (they put their
	// out-of-contract value in a guard at the top, precisely so no unrecognised
	// input reads as an answer about pyry). There is no out-of-contract value in
	// this space, and that is sound because this value's meaning IS "the path
	// reading names no runner" — a positive, TRUE statement about the reading,
	// equally true of tdnRunnerFromArgv's three indeterminate answers and of any
	// label outside the two runners. It makes no claim about pyry, so it cannot
	// launder one.
	trailReasonPathUnnamed = "reason-path-names-no-runner"
)

// trailReasonKeyName is resultTrailer.TerminalReason's json tag name
// (`resultTrailer`), i.e. the top-level key trailKeyNames reports.
//
// No reflect pin against the struct tag guards this, and that is a decision
// rather than an omission: TestTrailReasonPresenceComesFromTheKeyNames drives
// the absent/present-empty pair under one fixed path reading and asserts they
// reach DIFFERENT values, so a misspelling here makes presence false for both,
// collapses them onto one value, and goes red there. A second mechanism would
// guard the same drift.
const trailReasonKeyName = "terminal_reason"

// --- the record ---------------------------------------------------------------

// trailReasonResult is what the predicate produces, in trailAdmitResult's shape
// (trailer_admissibility_test.go:349-352): two fields, no pointer into any input,
// and no quote of any captured string.
//
// It deliberately carries no certified reason, unlike trailGateResult: copying
// the decoded scalar out would put a value FROM THE TRAILER into this record's
// output. A consumer that needs the reason takes it from trailGateResult.Reason,
// which already publishes it.
type trailReasonResult struct {
	Value  string `json:"value"`
	Detail string `json:"detail"`
}

// --- the predicate --------------------------------------------------------------

// trailReasonAgainstPath says what a trailer's terminal_reason means given the
// runner path the run was observed to take.
//
// Pure over its three inputs: no exec, no clock, no filesystem, no *testing.T,
// and it never fails a test — the same contract as trailScan, trailGate,
// trailAdmitAttribution, trailClassifyRun, tdnClassifyReapLog, pinReadState and
// fifoLiveRead. It returns no error and cannot fail: an unrecognised label is a
// fully-answered case (trailReasonPathUnnamed), not a breakage, and a nil
// keyNames is absence rather than an error.
//
// # Three scalars, never the record
//
// It takes neither trailScanResult nor trailObservation, for two reasons. Taking
// the record would drag Line — verbatim model output, marked
// OPERATOR-REVIEW-BEFORE-PASTE (result_trailer_observation_test.go:100-107) —
// into reach, and it would inherit the nil-Trailer pointer trap that trailGate
// had to answer with a contract check. Three inputs the caller has already
// reduced is what makes this function have NO unsafe input at all. It makes no
// claim that the key names and the decoded scalar came from the same line; that
// is the caller's obligation and it lands on #1367/#1368.
//
// # Parameter order is not accidental
//
// runnerReading and decodedReason are both string, so keyNames sits BETWEEN them
// and the two are never adjacent. The compiler still cannot catch a
// transposition (unlike finRecordInputs' named fields,
// `finRecordInputs`), but nothing here sits on opposite sides
// of a leak boundary: a transposition produces a wrong value, not a
// publication. A struct for three parameters would be machinery for no check.
//
// # The two steps that decide, in order
//
//   - The reading is REDUCED with finRecordRunnerLabel and never re-parsed or
//     prefix-matched. This is the case finRecordRunnerAgreement's doc explicitly
//     allows (`finRecordRunnerAgreement`): a reduced label compared
//     against a KNOWN-EXPECTED literal, not two unknowns prefix-matched. The two
//     runner labels are bare string literals, following the five shipped
//     comparison sites (`TestFinLiveStageEnvDeltaNamesTheRunner`, :498, :576, :597 and
//     `TestFinRecordRunnerAgreement`); this ticket introduces no constants for
//     them.
//   - Presence comes from the KEY NAMES ALONE. Taking it from decodedReason != ""
//     would merge "absent" and "present-and-empty" on the owes-one path, because
//     their decoded values are identically "" — the exact collapse #1357's
//     reading was landed to prevent.
//
// # No input byte interpolates into the output, at all
//
// Every arm's Detail is FIXED PROSE interpolating only this file's own
// constants, finRecordRunnerIndeterminate, the two bare runner labels and file
// cites — never runnerReading, never its reduced label, never a key name, never
// decodedReason. That is stronger than the ticket requires (the reading is
// admissible, being one of tdnRunnerFromArgv's constant answers, by the rule
// finRecordRun already states for ClaudeCommand at
// finding_run_record_test.go:235-238), and it is chosen because it makes the
// guarantee STRUCTURAL rather than a discipline — the same doctrine that made
// trailKeyNames return []string. A hand-built reading is not one of those
// constant answers, and a Detail echoing the label would publish whatever a
// caller passed. Nothing is lost: the consumer's record publishes the reading
// separately (finRecordRun.RunnerFromArgv).
func trailReasonAgainstPath(runnerReading string, keyNames []string, decodedReason string) trailReasonResult {
	present := slices.Contains(keyNames, trailReasonKeyName)

	switch finRecordRunnerLabel(runnerReading) {
	case "streamrunner":
		if !present {
			return trailReasonResult{
				Value: trailReasonAbsentOwesNone,
				Detail: trailDetail("%s: the argv reduced to streamrunner and terminal_reason is "+
					"off the line. That path passes claude's bytes through unchanged "+
					"(streamrunner/runner.go:177-179), so a healthy run's trailer is claude's own "+
					"result line and owes no terminal_reason", trailReasonAbsentOwesNone),
			}
		}
		return trailReasonResult{
			Value: trailReasonPresentOwesNone,
			Detail: trailDetail("%s: the argv reduced to streamrunner, which owes no "+
				"terminal_reason, and the line carries one anyway. The claim is only that the "+
				"line is NOT that path's documented healthy shape, and never that pyry wrote it "+
				"— the path passes claude's bytes through unchanged "+
				"(streamrunner/runner.go:177-179), so claude can produce the same reading. Empty "+
				"or named, both land here", trailReasonPresentOwesNone),
		}
	case "ptyrunner":
		if !present {
			return trailReasonResult{
				Value: trailReasonAbsentOwesOne,
				Detail: trailDetail("%s: the argv reduced to ptyrunner, which owes a "+
					"terminal_reason — every trailer there is pyry's own and emitter.go:383-391 "+
					"substitutes the recorded detail or \"unclassified\" before marshalling — and "+
					"the field is off the line entirely", trailReasonAbsentOwesOne),
			}
		}
		if decodedReason == "" {
			return trailReasonResult{
				Value: trailReasonBlankOwesOne,
				Detail: trailDetail("%s: the argv reduced to ptyrunner and terminal_reason is on "+
					"the line but decodes empty. emitter.go:383-391 substitutes \"unclassified\" "+
					"rather than marshal an empty one, so that chokepoint's guarantee is violated",
					trailReasonBlankOwesOne),
			}
		}
		return trailReasonResult{
			Value: trailReasonNamedOwesOne,
			Detail: trailDetail("%s: the argv reduced to ptyrunner and terminal_reason is on the "+
				"line and names a reason — that path's documented healthy shape",
				trailReasonNamedOwesOne),
		}
	}

	return trailReasonResult{
		Value: trailReasonPathUnnamed,
		Detail: trailDetail("%s: the runner reading reduced to a label naming neither ptyrunner "+
			"nor streamrunner, which is where tdnRunnerFromArgv's three %q answers land. The "+
			"trailer's shape is not consulted, because what a terminal_reason means depends on "+
			"which path wrote it", trailReasonPathUnnamed, finRecordRunnerIndeterminate),
	}
}

// --- membership helper ----------------------------------------------------------

// trailIsReasonValue reports whether v is one of the recorded terminal-reason
// values, in trailIsGateValue's shape (`trailIsGateValue`) and
// for the family's stated reason: a value a reader of the published record
// cannot look up is a verdict they cannot interpret.
//
// #1367/#1368 must CALL this rather than re-switch, which is the family's
// convention for trailIsGateValue, trailIsAdmitValue, trailIsBoundFrom and
// pinIsVerdict.
func trailIsReasonValue(v string) bool {
	switch v {
	case trailReasonAbsentOwesNone, trailReasonPresentOwesNone, trailReasonAbsentOwesOne,
		trailReasonBlankOwesOne, trailReasonNamedOwesOne, trailReasonPathUnnamed:
		return true
	}
	return false
}

// --- fixtures ---------------------------------------------------------------------

// trailReasonNeedles is one DISTINCT needle per INPUT POSITION of the predicate.
// A shared needle could not say WHICH position leaked, which is
// trailKeyNamesNeedles' rule (trailer_key_names_test.go:125-137).
func trailReasonNeedles() map[string]string {
	return map[string]string{
		"reading":  "TRAIL-REASON-READING-NEEDLE-MUST-NOT-REACH-A-PUBLIC-ISSUE",
		"key_name": "TRAIL-REASON-KEYNAME-NEEDLE-MUST-NOT-REACH-A-PUBLIC-ISSUE",
		"decoded":  "TRAIL-REASON-DECODED-NEEDLE-MUST-NOT-REACH-A-PUBLIC-ISSUE",
	}
}

// trailReasonPtyArgv, trailReasonStreamArgv and trailReasonNeitherArgv are argv
// strings carrying the marker tdnRunnerFromArgv keys on, and NOTHING is
// hand-typed about the readings themselves: every path reading below is that
// helper's own output over one of these. Hand-typing bare tokens instead would
// make the compared-whole mis-implementation green, because the rows would then
// never carry the parenthesised reason a whole-string comparison chokes on.
const (
	trailReasonPtyArgv     = "claude --session-id 11111111-2222-3333-4444-555555555555 --model opus"
	trailReasonStreamArgv  = "claude --input-format stream-json --output-format stream-json"
	trailReasonNeitherArgv = "claude --model opus"
	trailReasonBothArgv    = "claude --session-id 1111 --input-format stream-json"
)

// --- tests --------------------------------------------------------------------------

// TestTrailReasonAgainstPath drives all nine combinations of {absent,
// present-empty, present-non-empty} x {streamrunner, ptyrunner, indeterminate}.
//
// The cross product is EXHAUSTIVE BY CONSTRUCTION — it is generated from three
// shapes and three paths rather than listed — so a row cannot be pruned without
// deleting a shape or a path, and the count assertion catches that. The three
// shapes come from real scans of shipped fixtures and the three readings from
// real tdnRunnerFromArgv calls; nothing in the table is hand-built.
//
// # Two properties of the mutant matrix, stated so they are not rediscovered
//
//   - The two "indeterminate treated as a runner" mis-implementations share a red
//     row-set (R7-R9): no row separates them. Both are still caught, because they
//     yield DIFFERENT wrong values on each of those rows — which is why the
//     assertion is `got.Value != want` and never "is / is not
//     trailReasonPathUnnamed". A coarser assertion loses the distinction in the
//     failure output.
//   - R1, R4 and R6 are red only under "the reading compared whole instead of by
//     label". They are that mutant's ONLY evidence and are the rows most likely to
//     look prunable. They are not.
func TestTrailReasonAgainstPath(t *testing.T) {
	shapes := []struct {
		name        string
		line        string
		wantPresent bool
		wantDecoded string
	}{
		{"absent", trailKeyNamesNoTerminalReason(), false, ""},
		{"present-and-empty", trailKeyNamesEmptyTerminalReason(), true, ""},
		{"present-and-non-empty", trailFixtureTrailer, true, "completed"},
	}
	scans := make([]trailScanResult, len(shapes))
	for i, s := range shapes {
		got := trailScan([]byte(s.line + "\n"))
		// THE NON-VACUITY PRECONDITIONS. A shape that scanned to nothing, or one
		// whose presence and decode are not what the row claims, would make every
		// row below assert against an input other than the one named.
		if got.State != trailSeen {
			t.Fatalf("%s: scan state got %q (%s), want %q", s.name, got.State, got.Detail, trailSeen)
		}
		if got.Trailer == nil {
			t.Fatalf("%s: scan trailer got nil, want a decode of the full line", s.name)
		}
		if has := slices.Contains(got.KeyNames, trailReasonKeyName); has != s.wantPresent {
			t.Fatalf("%s: terminal_reason present in key names %q is %t, want %t",
				s.name, got.KeyNames, has, s.wantPresent)
		}
		if got.Trailer.TerminalReason != s.wantDecoded {
			t.Fatalf("%s: decoded terminal_reason got %q, want %q",
				s.name, got.Trailer.TerminalReason, s.wantDecoded)
		}
		scans[i] = got
	}

	paths := []struct {
		name string
		argv string
	}{
		{"streamrunner", trailReasonStreamArgv},
		{"ptyrunner", trailReasonPtyArgv},
		{"indeterminate", trailReasonNeitherArgv},
	}
	readings := make([]string, len(paths))
	for i, p := range paths {
		reading := tdnRunnerFromArgv(p.argv)
		// The fixture's own precondition: the argv must produce the path this row
		// is named for, or the expected matrix below is indexed by a lie.
		if label := finRecordRunnerLabel(reading); label != p.name {
			t.Fatalf("%s: tdnRunnerFromArgv(%q) reduced to %q, want %q",
				p.name, p.argv, label, p.name)
		}
		readings[i] = reading
	}

	// Indexed [path][shape], in the ticket's R1-R9 order. Not to be pruned for
	// redundancy: the standard is that every enumerated mis-implementation is
	// caught, not that every row is uniquely necessary.
	want := [3][3]string{
		{trailReasonAbsentOwesNone, trailReasonPresentOwesNone, trailReasonPresentOwesNone},
		{trailReasonAbsentOwesOne, trailReasonBlankOwesOne, trailReasonNamedOwesOne},
		{trailReasonPathUnnamed, trailReasonPathUnnamed, trailReasonPathUnnamed},
	}

	rows := 0
	reached := make(map[string]bool, len(want))
	for pi, p := range paths {
		for si, s := range shapes {
			rows++
			row := pi*len(shapes) + si + 1
			t.Run(fmt.Sprintf("R%d %s on %s", row, s.name, p.name), func(t *testing.T) {
				got := trailReasonAgainstPath(readings[pi], scans[si].KeyNames,
					scans[si].Trailer.TerminalReason)

				if got.Value != want[pi][si] {
					t.Errorf("value: got %q (%s), want %q", got.Value, got.Detail, want[pi][si])
				}
				if !trailIsReasonValue(got.Value) {
					t.Errorf("value: %q is outside the recorded terminal-reason space", got.Value)
				}
				if got.Detail == "" {
					t.Error("empty detail: a result that cannot say which arm fired and why is " +
						"indistinguishable from a reading")
				}
				// The Details' whole content is their tail — the claim limit and
				// the cite both sit at the end — so a Detail the cap truncated has
				// lost exactly the part worth publishing.
				if strings.HasSuffix(got.Detail, reachTruncationMarker) {
					t.Errorf("detail is %d bytes and lost its tail to the %d-byte cap: %s",
						len(got.Detail), reachMaxCommandBytes, got.Detail)
				}
			})
			reached[want[pi][si]] = true
		}
	}
	if rows != 9 {
		t.Errorf("the table drove %d row(s), want 9 — the cross product of three trailer shapes "+
			"and three path readings, and rows are not to be pruned", rows)
	}

	// The closure check the union map in TestTrailAdmissibilityConstantsAreClosed
	// explicitly cannot make: that map catches a COLLIDING value and never an
	// UNHANDLED one. A seventh constant no row reaches, or a sixth that lost its
	// arm, goes red here.
	for _, v := range []string{trailReasonAbsentOwesNone, trailReasonPresentOwesNone,
		trailReasonAbsentOwesOne, trailReasonBlankOwesOne, trailReasonNamedOwesOne,
		trailReasonPathUnnamed} {
		if !reached[v] {
			t.Errorf("%q is a recorded value no row reaches: either it has no arm or the table "+
				"stopped covering the space", v)
		}
		delete(reached, v)
	}
	for v := range reached {
		t.Errorf("the rows reach %q, which is outside the recorded six", v)
	}

	// The control: the membership helper is not one that returns true for
	// everything, so the green above is a property of the predicate's space.
	if trailIsReasonValue(trailGateUsable) {
		t.Errorf("trailIsReasonValue accepts %q, a value from the gate's space — a membership "+
			"helper that accepts anything certifies nothing", trailGateUsable)
	}

	// AC3's second half: a reading that names no runner reaches the unnamed value
	// EVEN WHEN the trailer's shape would otherwise qualify. The shape driven here
	// is the one that reaches trailReasonNamedOwesOne on ptyrunner, and all three
	// indeterminate variants tdnRunnerFromArgv can produce are driven — which is
	// the direct evidence that the reading was reduced rather than compared
	// against one variant.
	t.Run("every indeterminate variant reaches the unnamed value", func(t *testing.T) {
		qualifying := scans[2]
		variants := map[string]string{
			"no claude row was pinned":    "",
			"argv carries BOTH markers":   trailReasonBothArgv,
			"argv carries NEITHER marker": trailReasonNeitherArgv,
		}
		seen := make(map[string]string, len(variants))
		for name, argv := range variants {
			reading := tdnRunnerFromArgv(argv)
			if other, dup := seen[reading]; dup {
				t.Fatalf("%s and %s produce the same reading %q — the three variants must be "+
					"distinct or this sub-test drives one of them three times", name, other, reading)
			}
			seen[reading] = name

			got := trailReasonAgainstPath(reading, qualifying.KeyNames,
				qualifying.Trailer.TerminalReason)
			if got.Value != trailReasonPathUnnamed {
				t.Errorf("%s: got %q (%s), want %q — the trailer's shape qualifies for %q on "+
					"ptyrunner, and a reading naming no runner must outrank it", name, got.Value,
					got.Detail, trailReasonPathUnnamed, trailReasonNamedOwesOne)
			}
		}
		if len(seen) != 3 {
			t.Errorf("the sub-test drove %d distinct indeterminate reading(s), want 3", len(seen))
		}
	})
}

// TestTrailReasonPresenceComesFromTheKeyNames is the division of labour made
// executable: presence is read from the key names and NEVER from the decoded
// scalar.
//
// Two inputs whose decoded terminal reason is identically "" and whose key names
// differ must reach different values under one fixed path reading. The second
// half — that both lines decode to the SAME TerminalReason — is what establishes
// the decode could not have answered this; without it the first half proves only
// that two inputs differ.
//
// The streamrunner side of the same distinction is R1 vs R2 above, where the two
// deliberately DO share a value: emptiness is not distinguished on the path that
// owes no terminal_reason. This test therefore fixes the reading to ptyrunner,
// which is where the absence/emptiness split lives.
func TestTrailReasonPresenceComesFromTheKeyNames(t *testing.T) {
	absent := trailScan([]byte(trailKeyNamesNoTerminalReason() + "\n"))
	empty := trailScan([]byte(trailKeyNamesEmptyTerminalReason() + "\n"))

	for _, got := range []struct {
		name string
		res  trailScanResult
	}{{"absent", absent}, {"present-and-empty", empty}} {
		if got.res.State != trailSeen {
			t.Fatalf("%s: state got %q (%s), want %q — both fixtures carry \"type\":\"result\", "+
				"and a miss here would leave this test comparing two empty reads",
				got.name, got.res.State, got.res.Detail, trailSeen)
		}
		if got.res.Trailer == nil {
			t.Fatalf("%s: trailer got nil, want a decode of the full line", got.name)
		}
	}

	reading := tdnRunnerFromArgv(trailReasonPtyArgv)
	if label := finRecordRunnerLabel(reading); label != "ptyrunner" {
		t.Fatalf("the fixed path reading reduced to %q, want ptyrunner — the two values below "+
			"are the owes-one path's absence/emptiness split", label)
	}

	gotAbsent := trailReasonAgainstPath(reading, absent.KeyNames, absent.Trailer.TerminalReason)
	gotEmpty := trailReasonAgainstPath(reading, empty.KeyNames, empty.Trailer.TerminalReason)

	if gotAbsent.Value == gotEmpty.Value {
		t.Errorf("both inputs reach %q: their decoded terminal reasons are identically %q, so a "+
			"predicate taking presence from the scalar merges them — and the distinction #1357's "+
			"key-name reading was landed to make is spent", gotAbsent.Value, "")
	}
	if gotAbsent.Value != trailReasonAbsentOwesOne {
		t.Errorf("the line with no terminal_reason: got %q (%s), want %q",
			gotAbsent.Value, gotAbsent.Detail, trailReasonAbsentOwesOne)
	}
	if gotEmpty.Value != trailReasonBlankOwesOne {
		t.Errorf("the line carrying \"terminal_reason\":\"\": got %q (%s), want %q",
			gotEmpty.Value, gotEmpty.Detail, trailReasonBlankOwesOne)
	}

	// THE SECOND HALF: the decode collapses what the key names separate.
	if absent.Trailer.TerminalReason != empty.Trailer.TerminalReason {
		t.Fatalf("terminal_reason after the decode: got %q and %q — this test's premise is that "+
			"the fixed decode COLLAPSES the two, and if it did not the key-name reading would be "+
			"unnecessary", absent.Trailer.TerminalReason, empty.Trailer.TerminalReason)
	}
	if absent.Trailer.TerminalReason != "" {
		t.Errorf("terminal_reason after the decode: got %q, want %q from both lines",
			absent.Trailer.TerminalReason, "")
	}
}

// TestTrailReasonResultCarriesNoCapturedBytes makes the
// operator-review-before-paste obligation checkable rather than advisory, in
// TestTrailAdmissibilityRecordsCarryNoCapturedBytes's shape.
//
// One distinct needle per INPUT POSITION, because the record must carry nothing
// from ANY of the three and a shared needle could not say which one leaked. Both
// sub-tests fatal on the reached value BEFORE the sweep: a predicate that
// answered trailReasonPathUnnamed here would leak nothing and pass everything.
//
// Sub-test B is the one that earns its place. It needles exactly the
// parenthesised tail finRecordRunnerLabel strips, so a reduction that retained
// the whole string — or a Detail that echoed the reading it was handed — goes
// red there and nowhere else.
func TestTrailReasonResultCarriesNoCapturedBytes(t *testing.T) {
	needles := trailReasonNeedles()
	if len(needles) != 3 {
		t.Fatalf("the plant list holds %d needle(s), want 3 — one per input position", len(needles))
	}
	seen := make(map[string]string, len(needles))
	for position, needle := range needles {
		if other, dup := seen[needle]; dup {
			t.Fatalf("%s and %s carry the same needle %q: a shared needle cannot say WHICH "+
				"position leaked", position, other, needle)
		}
		seen[needle] = position
	}

	scan := trailScan([]byte(trailFixtureTrailer + "\n"))
	if scan.State != trailSeen {
		t.Fatalf("scan state: got %q (%s), want %q", scan.State, scan.Detail, trailSeen)
	}
	// The key-name position's plant: the fixture's real names plus one extra name
	// carrying a needle. terminal_reason is still among them, so the row reaches a
	// real arm rather than the absence one.
	needledNames := append(slices.Clone(scan.KeyNames), needles["key_name"])
	if !slices.Contains(needledNames, trailReasonKeyName) {
		t.Fatalf("the needled key names %q lost terminal_reason — the sub-tests below need a "+
			"present shape", needledNames)
	}

	sweep := func(t *testing.T, got trailReasonResult, planted ...string) {
		t.Helper()
		if got.Value != trailReasonNamedOwesOne {
			t.Fatalf("value: got %q (%s), want %q — the premise is a well-formed ptyrunner input "+
				"whose three positions carry needles, and a predicate answering anything else "+
				"here would leak nothing and pass everything below",
				got.Value, got.Detail, trailReasonNamedOwesOne)
		}
		encoded, err := json.Marshal(got)
		if err != nil {
			t.Fatalf("marshalling the terminal-reason result: %v", err)
		}
		for _, needle := range planted {
			if bytes.Contains(encoded, []byte(needle)) {
				t.Errorf("the marshalled result carries %s's planted bytes: %s",
					seen[needle], encoded)
			}
		}
	}

	t.Run("nothing from the key names or the decoded scalar crosses", func(t *testing.T) {
		sweep(t, trailReasonAgainstPath(tdnRunnerFromArgv(trailReasonPtyArgv), needledNames,
			needles["decoded"]), needles["key_name"], needles["decoded"])
	})

	t.Run("nothing from the reading's parenthesised tail crosses", func(t *testing.T) {
		// A LEGITIMATE label with a needled reason, so the label reduces to
		// ptyrunner and the row reaches a real arm rather than the default one.
		reading := "ptyrunner (" + needles["reading"] + ")"
		if label := finRecordRunnerLabel(reading); label != "ptyrunner" {
			t.Fatalf("the needled reading reduced to %q, want ptyrunner — the needle must sit in "+
				"the part the reduction STRIPS, or this sub-test proves nothing", label)
		}
		sweep(t, trailReasonAgainstPath(reading, needledNames, needles["decoded"]),
			needles["reading"], needles["key_name"], needles["decoded"])
	})
}

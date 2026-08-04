//go:build e2e_realclaude

package realclaude

// The run-outcome gather PARAMETERISED on pyry's reap-log stderr and on the
// pinned process-group set, with an offline proof that the finding AND a genuine
// negative both come out of its own composition.
//
// Everything here runs offline: synthetic stdout, synthetic stderr, no subject
// process, no staged process group, no live claude, no credentials, no daemon,
// no turn, no t.Skip.
//
//	go test -race -tags e2e_realclaude -run '^TestFinGather' -v ./internal/e2e/realclaude/
//
// # What this closes that #1268's rig structurally cannot
//
// trailRigGather (trail_run_rig_test.go:150) is the correct composition wired to
// the wrong two inputs. It passes a nil LITERAL as the reap-log stderr and keys
// the attribution on the TEST PROCESS's own group. tdnClassifyReapLog(nil, ...)
// can only reach tdnReapNoLine (LineCount == 0), which trailAdmitAttribution
// answers with trailAdmitVoidNoLine — so trailAdmitProof is unreachable, and
// with it trailOutcomeRunningAtTrailer (trail_run_outcome_test.go:118), the only
// outcome that is a finding. Fed a live pyry's stdout that gather still composes,
// still classifies and still returns a plausible outcome: it reports a CLEAN
// NEGATIVE ON EVERY RUN, FOREVER, WITH NO SYMPTOM. Both hardcodings are correct
// where they are — #1268's nil is what makes "no reap line was synthesised" a
// property of that file — which is why this is a new gather rather than an edit.
//
// # The pin and the scan are two inputs, not one
//
// ptyrunner.Run's pinned teardown order (runner.go:479-485, the reap defer at
// :398) runs the reap immediately AFTER the trailer is written, completing in the
// time of one ps exec (reap.go:75-76). A scan taken after the trailer is observed
// therefore matches NOTHING on a healthy run — the predicted reading, not a
// failure. So the pgid the attribution joins on cannot come from a post-trailer
// scan; it must be pinned DURING the turn while the hold guarantees the command
// is alive. Two scans, two roles, two parameters:
//
//   - pinned []int — the during-turn pin, the join key into pyry's reap log;
//   - needles []string — the post-trailer argv scan, the content join and the
//     per-pid corroboration.
//
// pinned is []int and NEVER []reachProc or []pinMatch. finAttributeFanOut's own
// doc (finding_attribution_fanout_test.go:195-202) states why: reachProc.Command
// is verbatim argv read off the ambient process table, and a []reachProc
// signature that recorded only .PGID would pass every test in this file while
// reopening the credential channel. The signature is the enforcement.
//
// # The proof outranks every match-count arm
//
// trailClassifyRun's Step 2 (:523) returns trailOutcomeRunningAtTrailer on
// Admit.Value == trailAdmitProof BEFORE Step 4 (ArgvScanErrored), Step 5
// (RowsScanned == 0), Step 7 (MatchCount > 0) or Step 8 are consulted. That is
// what makes this file's rows cheap: with no subject staged EVERY row runs at
// MatchCount == 0, and the finding and the negative still separate. Stretching a
// hold past the reap to force a post-trailer match would destroy the very
// ordering the proof rests on.
//
// # An empty match set under a certifying gate IS the documented healthy negative
//
// trailRunWellFormed() (:611) — the classifier's own canonical well-formed input
// — is trailGateUsable + "completed" + MatchCount 0 + RowsScanned 12 +
// trailAdmitVoidGroupUnnamed, and it reaches trailOutcomeNoRowMatched, whose
// Detail says that value is "A statement about THE SCAN and NOT A STATEMENT THAT
// THE COMMAND HAD EXITED". Reaching for a run-void-* to make the negative arm
// "assert a void" would vary two dimensions at once.
//
// # The certified terminal reason crosses into the readings VERBATIM, by design
//
// This is the one boundary the no-captured-bytes test must STATE rather than
// leave to its plant site, because the obvious reading of #1271's "plant the
// needle in every string-bearing input" makes it red against shipped code:
//
//   - trailGate fills Reason from res.Trailer.TerminalReason and splices it %q
//     into Detail on BOTH certifying arms (trailer_admissibility_test.go:306,
//     :317); readings.Gate takes that result whole;
//   - the gather hands the same Reason to the fan-out as certified, and
//     trailAdmitAttribution splices it %q into Detail on the budget arm (:428)
//     and the proof arm (:470); readings.Admit takes that result whole too.
//
// So a needle in terminal_reason lands in readings.Gate.Detail and, on the
// finding row, in readings.Admit.Detail as well, and the only fix would be to
// stop quoting the reason in two shipped predicates — deleting the field the
// operator reads to interpret the gate. #1280 met this one layer down and
// resolved it by planting only in the stderr
// (finding_attribution_fanout_test.go:730-731). THE NEEDLE'S HOME HERE IS THE
// TRAILER'S `result` FIELD AND THE REAP-LOG STDERR; it may not enter
// terminal_reason. That plant is the STRONGER test, not a weakened one:
// resultTrailer has no `result` member so trailScanResult.Trailer structurally
// cannot carry it, trailScanResult.Line carries it verbatim but capped, and
// trailRunReadings carries neither field.
//
// # The two staged values that are still staged, and who owns them
//
// PyryExited = true and ClaudeState = "" are #1268's two staged values, kept here
// with their original justification (see finGatherReadings). #1282 must not
// inherit the first silently: the argument that condemns the nil stderr applies
// one field over.
//
// # Failure messages
//
// A t.Fatalf here MAY name counts, pgids, verdicts, outcome values, gate and
// admit values, BoundFrom, and finAttribute* condition names. It MAY NEVER name
// pinScan.Matches (verbatim argv off the ambient process table), the
// function-local observation's Line, or a trailScanResult's trailer. Printing a
// whole readings or record IS safe, and only because
// TestFinGatherReturnsNoCapturedBytes proves it — that test is what licenses the
// rest of this file's messages rather than being one AC among five.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// --- constants ------------------------------------------------------------------

const (
	// finGatherTrailerWait is the trailer poll's timeout, matching
	// trailRigTrailerWait. NO ROW EVER WAITS IT OUT: the certifying rows pre-seed
	// the buffer so the first poll hits, and the non-certifying row seeds an
	// over-long line, which trailWaitForTrailer returns from IMMEDIATELY
	// (result_trailer_observation_test.go:264-266) because abortion is monotone.
	finGatherTrailerWait = 10 * time.Second
	// finGatherNamedPGID is the group the synthetic reap line names. It is
	// finding_attribution_fanout_test.go's own fixture value, so the two chains
	// read as one.
	finGatherNamedPGID = 7788
	// finGatherUnnamedPGID is a pinned group the reap line does NOT name — the
	// negative row's ONE varied dimension. Everything else on that row is byte for
	// byte the finding row's input, because a negative fixture differing in more
	// than the intended dimension asserts its own construction.
	finGatherUnnamedPGID = 4242
	// finGatherNeedleName is the argv needle's basename inside t.TempDir(). The
	// full PATH is the needle: unique per run, and no process's command line
	// carries it — including the test binary's own, whose argv is `-test.run=...`
	// and nothing more. NOTHING IS EVER CREATED AT THAT PATH: it is a match
	// pattern handed to a Go-side matcher, never a filesystem operand.
	finGatherNeedleName = "fin-gather-subject"
)

// finGatherNeedleTrailer is an ORDINARY trailer in emitter.go:456-468's pinned
// wire order whose `result` field carries trailNeedle.
//
// Two properties are load-bearing:
//
//   - THE NEEDLE SITS INSIDE reachMaxCommandBytes, so trailScanResult.Line
//     genuinely carries it and the channel the gather held really was dirty.
//     trailPaddedTrailer places it PAST the cap deliberately
//     (result_trailer_observation_test.go:297-300), which would make the stdout
//     half of TestFinGatherReturnsNoCapturedBytes vacuous: the only leak it could
//     then catch is a record storing the line IN FULL, and trailRunReadings has no
//     field that could. That test asserts the premise so a later edit cannot push
//     the plant past the cap in silence.
//   - terminal_reason STAYS CLEAN ("completed"), which makes the header's
//     exclusion structural in the fixture rather than a discipline at the plant
//     site. trailPaddedTrailer is the wrong fixture here for a second reason as
//     well: it renders "max_turns", and on that path trailAdmitAttribution returns
//     its structural void BEFORE reading the verdict at all (:425-435), so the
//     needle-bearing line's classification would be discarded and the test would
//     go green even against a leak on the proof arm.
const finGatherNeedleTrailer = `{"type":"result","subtype":"success","is_error":false,` +
	`"duration_ms":4210,"num_turns":3,"result":"` + trailNeedle + `",` +
	`"stop_reason":"end_turn","session_id":"11111111-2222-3333-4444-555555555555",` +
	`"total_cost_usd":0.0123,"usage":{"input_tokens":120,"output_tokens":45,` +
	`"cache_creation_input_tokens":0,"cache_read_input_tokens":0},` +
	`"terminal_reason":"completed"}`

// --- the gather -------------------------------------------------------------------

// finGatherReadings assembles a complete trailRunReadings from the four inputs a
// live probe holds, using ONLY shipped producers, and returns the attribution
// record alongside it.
//
// Nothing it returns is built by struct literal: no trailGateResult,
// trailAdmitResult or tdnReapOutcome reaches the readings except as the output of
// trailGate, finAttributeFanOut or pinScanArgv/pinReadState. Funnelling the whole
// composition through one function is what makes that checkable in one place
// rather than argued across call sites.
//
// # What it returns, and what it deliberately does not
//
// The trailer observation is a FUNCTION-LOCAL INTERMEDIATE and is never returned.
// trailObservation embeds trailScanResult (result_trailer_observation_test.go:125-126),
// so handing it back would promote .Trailer and .Line straight into the caller's
// reach. This is the one place the composition diverges from trailRigGather,
// which returns it because #1268's AC4 needed Staleness; nothing here does —
// BoundFrom is on the readings, and Staleness is NOT a classifier input and must
// not be used as one.
//
// The second return is #1280's own finAttributeRecord, returned WHOLE. It is
// proven to carry no captured bytes, and it is where Conditions lives — the field
// a consumer branches on. Returning it means this file defines no new record type.
//
// # The caller's obligations
//
//   - pinned is []int. A CALLER HOLDING pinScan.Matches CONVERTS AT ITS OWN CALL
//     SITE, taking .PGID and carrying nothing else: reachProc.Command is verbatim
//     argv, and a ps column is how an operator's CLAUDE_CODE_OAUTH_TOKEN or
//     ANTHROPIC_API_KEY reaches an artifact destined for a public issue. That
//     conversion site is #1282's, and it is where the channel would reopen.
//   - PyryExited IS STAGED true HERE AND #1282 MUST NOT INHERIT THAT SILENTLY.
//     The same argument that condemns #1268's nil stderr applies one field over:
//     fed a live pyry, a gather that hardcodes PyryExited would never let Step 3's
//     trailOutcomeVoidPyryDidNotExit fire, and a run where pyry hung would report a
//     scan-side answer — or the finding — instead of the staging void. That is a
//     FALSE-POSITIVE direction, worse than the false negative this function
//     closes. #1282 owns promoting PyryExited (and ClaudeState) to parameters, or
//     filling them from real producers, BEFORE it feeds this gather a live pyry.
//     Not done here because no test in this file could exercise either value.
//
// # Which contract check shapes which leg
//
//   - C2 (trail_run_outcome_test.go:377) requires a trailGateUsable value to carry
//     a non-empty Reason. The gate is fed a real scanned trailer, so the reason
//     arrives filled; a hand-built trailGateResult{Value: trailGateUsable} is
//     exactly the fixture C2 exists to reject.
//   - C3/C4 (:390, :402) make the Admit guard MANDATORY rather than defensive.
//     Calling the predicate unconditionally fails C4 on every non-certifying gate;
//     skipping it unconditionally fails C3 on every certifying one.
//   - C8/C9 (:451, :463) require the counts and the errored flag to be one
//     pinScanArgv call's own, which is why nothing here types one in. pinScanArgv
//     returns the ZERO pinScan on error, so the three are consistent by
//     construction.
func finGatherReadings(stdout *probeSyncBuffer, needles []string, stderr []byte,
	pinned []int) (trailRunReadings, finAttributeRecord) {
	var readings trailRunReadings
	var record finAttributeRecord

	// The trailer leg. The observation's embedded scan result is REUSED rather
	// than re-scanned, because that is the composition a live probe performs.
	obs := trailWaitForTrailer(stdout, finGatherTrailerWait)
	readings.BoundFrom = obs.BoundFrom
	readings.Gate = trailGate(obs.trailScanResult)

	// The attribution leg, guarded on the gate's certified Reason: the exact
	// condition C3 and C4 split on, and the one trailAdmitAttribution's own
	// contract block (trailer_admissibility_test.go:413) rejects the negation of.
	// Outside the guard the fan-out is not called at all and record stays zero.
	//
	// Selected is copied in ONLY when the fan-out attributed a group. Its doc
	// comment (finding_attribution_fanout_test.go:106-114) states the obligation
	// this function is the caller of: branch on Conditions, and never copy a zero
	// Selected into Admit — under a certifying gate that reaches
	// trailOutcomeOutOfContract via C3, a caller bug dressed as a reading. The
	// condition is passed OUT to the caller instead; the outcome tier that names
	// such runs is #1278's.
	if readings.Gate.Reason != "" {
		record = finAttributeFanOut(stderr, pinned, readings.Gate.Reason)
		if !finAttributeHasCondition(record, finAttributeNoGroups) {
			readings.Admit = record.Selected
		}
	}

	// The argv leg. No exclusions: this file excludes nothing, and nil says so
	// more clearly than an empty map. One call, three fields — C8/C9's subject.
	scan, err := pinScanArgv(needles, nil)
	readings.ArgvScanErrored = err != nil
	readings.MatchCount = scan.MatchCount
	readings.RowsScanned = scan.RowsScanned

	// One per-pid read per MATCHED pid, taken here rather than at a call site, so
	// "Liveness holds a read for each" is true by construction of the gather.
	for _, match := range scan.Matches {
		readings.Liveness = append(readings.Liveness, pinReadState(match.PID))
	}

	// The only two staged values. See the doc comment above for why the first is
	// not cosmetic and why #1282 owns it.
	readings.PyryExited = true // no pyry runs here, so there is none to fail to exit
	readings.ClaudeState = ""  // no claude runs here; C7 admits "" as "not read"

	return readings, record
}

// --- fixtures ---------------------------------------------------------------------

// finGatherCase is one gather input and what its composition must produce.
type finGatherCase struct {
	name string
	// seed is the stdout line the trailer leg reads, written to a fresh buffer
	// before the gather runs.
	seed   string
	stderr []byte
	pinned []int
	// wantReason is the terminal reason the gate must certify: non-empty exactly
	// on the certifying rows, which is what splits C3 from C4 in the assert helper.
	wantGate    string
	wantReason  string
	wantAdmit   string // "" on the row whose gate certifies nothing
	wantOutcome string
}

// finGatherCases returns every composition under test.
//
// A FUNCTION rather than a package-level var, for trailRunWellFormed's stated
// reason (trail_run_outcome_test.go:609-610): the rows carry slices, and a shared
// backing array is reachable from every test in this package.
//
// The first two rows differ in EXACTLY ONE DIMENSION — the pinned pgid. Same
// stdout, same stderr, same needles, both at MatchCount == 0. That is the
// ordering proof: Step 2 outranks Steps 4, 5, 7 and 8, demonstrated by this
// gather's own composition rather than described.
func finGatherCases() []finGatherCase {
	// One anchored line naming finGatherNamedPGID and no other group.
	named := []byte(trailReapLine(1, fmt.Sprintf("[%d]", finGatherNamedPGID)) + "\n")

	return []finGatherCase{
		{
			name:        "the reap log names a pinned group, so the composition reaches the finding",
			seed:        trailFixtureTrailer,
			stderr:      named,
			pinned:      []int{finGatherNamedPGID},
			wantGate:    trailGateUsable,
			wantReason:  "completed",
			wantAdmit:   trailAdmitProof,
			wantOutcome: trailOutcomeRunningAtTrailer,
		},
		{
			name:        "the reap log names a different group, so the composition reaches a genuine negative",
			seed:        trailFixtureTrailer,
			stderr:      named,
			pinned:      []int{finGatherUnnamedPGID},
			wantGate:    trailGateUsable,
			wantReason:  "completed",
			wantAdmit:   trailAdmitVoidGroupUnnamed,
			wantOutcome: trailOutcomeNoRowMatched,
		},
		{
			// C4's row. trailWaitForTrailer returns from an aborted scan
			// IMMEDIATELY, so this row costs no wall clock despite the 10 s wait.
			name:        "a non-certifying gate leaves the attribution unclassified",
			seed:        trailPaddedTrailer(trailOverlongPad),
			stderr:      named,
			pinned:      []int{finGatherNamedPGID},
			wantGate:    trailGateScanAborted,
			wantReason:  "",
			wantAdmit:   "",
			wantOutcome: trailOutcomeVoidTrailerScanAborted,
		},
	}
}

// finGatherNeedles returns this run's argv needle set: one path unique to the
// running test, under which nothing is ever staged.
func finGatherNeedles(t *testing.T) []string {
	t.Helper()
	return []string{filepath.Join(t.TempDir(), finGatherNeedleName)}
}

// finGatherSeed writes one fixture line into a fresh buffer and returns the exact
// bytes written, so an assertion can re-run the shipped producers over the SAME
// bytes the gather read.
func finGatherSeed(t *testing.T, stdout *probeSyncBuffer, line string) []byte {
	t.Helper()
	seed := []byte(line + "\n")
	if _, err := stdout.Write(seed); err != nil {
		t.Fatalf("seed the gather's stdout buffer: %v", err)
	}
	return seed
}

// --- tests ------------------------------------------------------------------------

// TestFinGatherComposesTheFindingAndTheNegative is this file's actual claim: the
// finding AND a genuine negative both come out of the gather's own composition,
// driven offline from a synthetic stdout and a synthetic stderr.
//
// #1268 structurally could not make this claim — its nil stderr literal puts
// trailAdmitProof out of reach — which is why synthesising a reap line is
// legitimate HERE and forbidden THERE. Every row classifies the gather's own
// readings; no row hand-builds a trailRunReadings.
func TestFinGatherComposesTheFindingAndTheNegative(t *testing.T) {
	for _, tc := range finGatherCases() {
		t.Run(tc.name, func(t *testing.T) {
			var stdout probeSyncBuffer
			seed := finGatherSeed(t, &stdout, tc.seed)

			readings, record := finGatherReadings(&stdout, finGatherNeedles(t), tc.stderr, tc.pinned)
			finGatherAssertContract(t, tc, seed, readings, record)

			outcome := trailClassifyRun(readings)
			if outcome.Value != tc.wantOutcome {
				t.Fatalf("the gather's own readings classify as %q (%s); want %q. The two certifying "+
					"rows differ in the pinned pgid ALONE, so a row landing on its neighbour's value "+
					"means the reap-log join is not what separated them",
					outcome.Value, outcome.Detail, tc.wantOutcome)
			}
		})
	}
}

// finGatherAssertContract discharges #1268's four contract obligations on EVERY
// row rather than once, so the parameterisation is checked against each
// composition the file produces. Each check names the contract check it
// discharges.
func finGatherAssertContract(t *testing.T, tc finGatherCase, seed []byte,
	readings trailRunReadings, record finAttributeRecord) {
	t.Helper()

	// C2 (trail_run_outcome_test.go:377): the gate is fed a REAL SCANNED TRAILER.
	// Byte for byte against the shipped producers over the same bytes the gather
	// read — trailGateResult is three strings, so == suffices. A hand-built
	// trailGateResult{Value: trailGateUsable} is exactly the fixture C2 exists to
	// reject, and this equality is what rules it out.
	if want := trailGate(trailScan(seed)); readings.Gate != want {
		t.Fatalf("C2: the gate reads %+v; want %+v — the gate must be trailGate's own output over "+
			"the scanned trailer, never a value the gather typed in", readings.Gate, want)
	}
	// Pinned against the row as well, so a fixture edit that moved both sides in
	// step cannot pass: at a non-usable value step 1 short-circuits and every row
	// would agree.
	if readings.Gate.Value != tc.wantGate || readings.Gate.Reason != tc.wantReason {
		t.Fatalf("C2: the gate reads %q certifying reason %q; want %q certifying %q",
			readings.Gate.Value, readings.Gate.Reason, tc.wantGate, tc.wantReason)
	}

	if tc.wantReason != "" {
		// C3 (:390): the attribution predicate is called EXACTLY WHEN the gate
		// certified a reason, and what it produced is what reached Admit. The
		// fan-out is pure over its three arguments, so this recomputation is
		// deterministic: a gather that passed a different certified string, or that
		// re-derived the selection rule instead of calling finAttributeFanOut, goes
		// red here.
		if !trailIsAdmitValue(readings.Admit.Value) {
			t.Fatalf("C3: the gate certified %q and the attribution reads %q, which is not one of "+
				"trailIsAdmitValue's seven — a certifying gate arriving unclassified reaches %s",
				readings.Gate.Reason, readings.Admit.Value, trailOutcomeOutOfContract)
		}
		if want := finAttributeFanOut(tc.stderr, tc.pinned, readings.Gate.Reason).Selected; readings.Admit != want {
			t.Fatalf("C3: the attribution reads %+v; want the fan-out's own Selected %+v — the "+
				"reduction from many pinned groups to one value is finAttributeFanOut's, and "+
				"re-deriving it here would be a second opinion about finAttributeOrder",
				readings.Admit, want)
		}
		if readings.Admit.Value != tc.wantAdmit {
			t.Fatalf("C3: the attribution reads %q; want %q — the reap log names %d and this row "+
				"pinned %v", readings.Admit.Value, tc.wantAdmit, finGatherNamedPGID, tc.pinned)
		}
	} else {
		// C4 (:402): Admit is left ZERO when the gate certified nothing. This is
		// the zero-value comparison the package uses for "not classified"
		// (trail_run_outcome_test.go:664, finding_attribution_fanout_test.go:667) —
		// a comparison against an existing consumer's idiom, not a reading the
		// gather produced by literal.
		if readings.Admit != (trailAdmitResult{}) {
			t.Fatalf("C4: the gate certifies no reason yet the attribution reads %+v — passing an "+
				"uncertified run to the predicate judges it as if the gate had approved it",
				readings.Admit)
		}
		// The other half, which the readings alone cannot show: the fan-out was not
		// called AT ALL, rather than called and its result discarded.
		if !reflect.DeepEqual(record, finAttributeRecord{}) {
			t.Fatalf("C4: the gate certifies no reason yet the attribution record is %+v; want the "+
				"zero record — the fan-out must not run on a gate that certified nothing", record)
		}
	}

	// C8 (:451): both counts are ONE SCAN's own. {MatchCount: 1} with RowsScanned
	// unfilled is the hand-typed fixture C8 exists to catch.
	if readings.MatchCount < 0 || readings.RowsScanned < 0 ||
		readings.MatchCount > readings.RowsScanned {
		t.Fatalf("C8: the argv scan reports %d match(es) across %d row(s), a triple pinPartition "+
			"cannot emit", readings.MatchCount, readings.RowsScanned)
	}

	// C9 (:463): the errored flag comes from the SAME call as the counts.
	// pinScanArgv returns the ZERO pinScan on error, so the three are consistent
	// by construction — what is pinned here is that invariant. The errored arm has
	// NO LIVE REPRO in an offline rig, which is this package's own idiom for an
	// unproducible contract arm (trailer_admissibility_test.go:293-299).
	if readings.ArgvScanErrored && (readings.MatchCount != 0 || readings.RowsScanned != 0) {
		t.Fatalf("C9: the argv scan is recorded as errored yet reports %d match(es) across %d "+
			"row(s), a pair pinScanArgv cannot emit", readings.MatchCount, readings.RowsScanned)
	}
	if !readings.ArgvScanErrored && readings.RowsScanned <= 0 {
		t.Fatalf("C9: the argv scan ran without error and parsed %d well-formed row(s); want more "+
			"than 0 — at zero the reading is %s, a nothing-was-measured masquerading as this "+
			"file's negative", readings.RowsScanned, trailOutcomeVoidNoRowsParsed)
	}

	// AC2's ordering claim, asserted rather than described. No subject is staged
	// anywhere in this file, so every row runs at zero matches and the finding row
	// STILL reports the finding: Step 2 (:523) is consulted before Step 7 and Step
	// 8 are. On a live run the same thing happens for a different reason — the
	// reaper has already killed the group by the time the post-trailer scan runs —
	// which is why nothing may be done to the hold or the scan timing to "make the
	// match survive".
	if readings.MatchCount != 0 {
		t.Fatalf("the argv scan matched %d of %d row(s) although no subject is staged; want 0 — "+
			"these rows exist to show the outcome separates with an EMPTY match set, and a row that "+
			"matched something proves that at Step 7 instead", readings.MatchCount,
			readings.RowsScanned)
	}
	// The negative must be a genuine answer and never a void: each neighbouring
	// void would be a nothing-was-measured dressed as the healthy reading.
	if tc.wantOutcome == trailOutcomeNoRowMatched && !readings.PyryExited {
		t.Fatalf("the negative row reports pyry as not exited, so it lands on %s — a staging fault, "+
			"not the clean negative %s names", trailOutcomeVoidPyryDidNotExit,
			trailOutcomeNoRowMatched)
	}

	// Free corroboration on every row: a composition that assembled a record its
	// own producers cannot emit shows up here first.
	outcome := trailClassifyRun(readings)
	if !trailIsRunOutcome(outcome.Value) || outcome.Value == trailOutcomeOutOfContract {
		t.Fatalf("the gather's readings classify as %q (%s); want one of the recorded outcomes and "+
			"not %q", outcome.Value, outcome.Detail, trailOutcomeOutOfContract)
	}
}

// TestFinGatherEmptyPinnedSetIsNotAReading pins that an empty pinned set is
// SURFACED AS A CONDITION and never composed into readings.
//
// Both subtests carry a certifying stdout and a real reap line and differ only in
// the pinned set, so the condition is attributable to the set alone. The second
// is not a duplicate: it separates a gather that READS Conditions from one that
// special-cases len(pinned) == 0.
func TestFinGatherEmptyPinnedSetIsNotAReading(t *testing.T) {
	stderr := []byte(trailReapLine(1, fmt.Sprintf("[%d]", finGatherNamedPGID)) + "\n")

	cases := []struct {
		name   string
		pinned []int
		// wantUnreportable is the groups surfaced rather than classified, or nil
		// when the set was empty to begin with.
		wantUnreportable []int
	}{
		{
			name:   "a caller that pinned nothing, or pinned before the subject execed",
			pinned: nil,
		},
		{
			// reap.go:52 skips pgid <= 1 before it kills anything, so no reap line
			// can carry one and tdnClassifyReapLog's :147 guard would blame the
			// instrument for a consumer that simply failed to capture a pgid.
			name:             "a set in which every group is one the reaper can never report",
			pinned:           []int{0, 1},
			wantUnreportable: []int{0, 1},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout probeSyncBuffer
			finGatherSeed(t, &stdout, trailFixtureTrailer)

			readings, record := finGatherReadings(&stdout, finGatherNeedles(t), stderr, tc.pinned)

			// The premise: the gate certified, so the attribution leg genuinely ran
			// and the empty set is what stopped it — not a short-circuiting void.
			if readings.Gate.Value != trailGateUsable || readings.Gate.Reason == "" {
				t.Fatalf("the gate reads %q certifying %q; want %q certifying a reason — without a "+
					"certifying gate the fan-out is never called and this subtest asserts nothing "+
					"about the empty set", readings.Gate.Value, readings.Gate.Reason, trailGateUsable)
			}

			// Read through the SHIPPED helper, never by re-walking Conditions here.
			if !finAttributeHasCondition(record, finAttributeNoGroups) {
				t.Fatalf("the record carries conditions %v; want %s among them — Conditions is the "+
					"field a consumer branches on, and a gather that does not surface it hands its "+
					"caller no way to tell an unattributed run from an attributed one",
					record.Conditions, finAttributeNoGroups)
			}
			// The mutual exclusion Selected's doc comment states.
			if record.Selected != (trailAdmitResult{}) || len(record.Entries) != 0 {
				t.Fatalf("the record selected %+v across %d entr(ies); want the zero selection and "+
					"no entries — the two are the SAME condition as %s",
					record.Selected, len(record.Entries), finAttributeNoGroups)
			}
			// The obligation this gather discharges: it did NOT copy the zero
			// Selected into Admit.
			if readings.Admit != (trailAdmitResult{}) {
				t.Fatalf("the readings carry attribution %+v although no group was attributed; want "+
					"the zero value — copying a zero Selected in is the caller bug %s's doc comment "+
					"names", readings.Admit, finAttributeNoGroups)
			}
			if !reflect.DeepEqual(record.Unreportable, tc.wantUnreportable) {
				t.Errorf("the record surfaces unreportable groups %v; want %v",
					record.Unreportable, tc.wantUnreportable)
			}
			if tc.wantUnreportable != nil &&
				!finAttributeHasCondition(record, finAttributeGroupUnreportable) {
				t.Errorf("the record carries conditions %v; want %s among them — the groups were "+
					"surfaced, not classified", record.Conditions, finAttributeGroupUnreportable)
			}

			// THE PIN: no trailRunOutcome is produced from these readings. Made
			// checkable by pricing the alternative exactly as
			// TestFinAttributeEmptySetAlternativesArePublishedFalsehoods does one
			// layer down — classifying them anyway reaches out-of-contract, so the
			// caller's obligation is to BRANCH, not to publish.
			got := trailClassifyRun(readings)
			if got.Value != trailOutcomeOutOfContract {
				t.Fatalf("classifying the unattributed readings anyway reaches %q (%s); want %q via "+
					"C3 — if these readings classified cleanly there would be nothing forcing a "+
					"caller to branch on the condition", got.Value, got.Detail,
					trailOutcomeOutOfContract)
			}
			if !strings.Contains(got.Detail, "no run condition under which a certifying gate arrives") {
				t.Errorf("detail: got %q, want C3's own sentence — the arm reached matters as much "+
					"as the value, because three other contract checks also answer %s",
					got.Detail, trailOutcomeOutOfContract)
			}
		})
	}
}

// TestFinGatherReturnsNoCapturedBytes makes the operator-review-before-paste
// obligation checkable rather than advisory, over BOTH of the gather's returns.
//
// # Both plants, and the one that is excluded
//
// The needle goes into the two inputs that could carry captured bytes into the
// returns: the reap-log stderr, and the stdout buffer at the trailer's `result`
// field, which is what trailNeedle already stands in for
// (result_trailer_observation_test.go:297-300). IT MAY NOT GO INTO
// terminal_reason: trailGate and trailAdmitAttribution both quote the certified
// reason %q into their Details BY DESIGN (trailer_admissibility_test.go:306,
// :317, :428, :470), and readings.Gate and readings.Admit take those results
// whole — so such a plant would be red against shipped code whose only fix
// deletes the field the operator reads to interpret the gate. The exclusion is
// stated here and made structural by finGatherNeedleTrailer, which renders
// terminal_reason "completed".
//
// The stderr plant sits ON THE ANCHORED LINE, after the pgids= list, which is
// #1280's position and the only non-vacuous one: tdnClassifyReapLog skips every
// line not carrying tdnReapMessage BEFORE it fills any field
// (teardown_liveness_test.go:161-163), so a needle on a non-anchored line enters
// nothing and the test would go green over a record that captured everything.
//
// # The structural half fixes #1280's flat scan
//
// The forbidden-key walk RECURSES. trailRunReadings carries no json tags, so its
// marshalled keys are Go field names and Gate, Admit, Liveness and the record's
// Entries[] are all nested — a flat top-level scan examines none of their keys,
// which is exactly the gap #1280's transplant left open.
func TestFinGatherReturnsNoCapturedBytes(t *testing.T) {
	var stdout probeSyncBuffer
	seed := finGatherSeed(t, &stdout, finGatherNeedleTrailer)
	stderr := []byte(trailReapLine(1, fmt.Sprintf("[%d] %s", finGatherNamedPGID, trailNeedle)) + "\n")

	readings, record := finGatherReadings(&stdout, finGatherNeedles(t), stderr,
		[]int{finGatherNamedPGID})

	// --- the premises, each of which turns a vacuous plant into a Fatalf ---

	// The trailer leg read the needle-bearing line as an ORDINARY trailer, and the
	// needle is not in terminal_reason. This is the exclusion above, asserted in
	// code rather than left in prose.
	if readings.Gate.Value != trailGateUsable || readings.Gate.Reason != "completed" {
		t.Fatalf("the gate reads %q certifying %q; want %q certifying \"completed\" — the needle "+
			"belongs in the trailer's `result` field and NEVER in terminal_reason, which two "+
			"shipped predicates quote verbatim by design", readings.Gate.Value,
			readings.Gate.Reason, trailGateUsable)
	}
	// The channel the gather held genuinely carried the needle: it survived the
	// 512-byte cap in the retained copy. Without this, a later fixture edit could
	// push the plant past the cap and leave the stdout half asserting nothing.
	if line := trailScan(seed).Line; !strings.Contains(line, trailNeedle) {
		t.Fatalf("the scanned trailer line does not carry the needle within the %d-byte cap, so "+
			"the stdout plant is vacuous: the only leak it could still catch is a record storing "+
			"the line IN FULL, and trailRunReadings has no field that could (%d bytes retained)",
			reachMaxCommandBytes, len(line))
	}
	// The needle-bearing anchored line was recognised, parsed and found to name
	// the pinned group, and certified is not the budget reason — so the proof arm,
	// which is the arm that quotes the most, genuinely ran.
	if readings.Admit.Value != trailAdmitProof {
		t.Fatalf("the attribution reads %q; want %s — the premise is that the needle rides an "+
			"ANCHORED line the classifier read in full and attributed to group %d",
			readings.Admit.Value, trailAdmitProof, finGatherNamedPGID)
	}

	// --- the checks, over BOTH returns ---

	for _, subject := range []struct {
		what  string
		value any
	}{
		{"readings", readings},
		{"attribution record", record},
	} {
		encoded, err := json.Marshal(subject.value)
		if err != nil {
			t.Fatalf("marshalling the %s: %v", subject.what, err)
		}
		if bytes.Contains(encoded, []byte(trailNeedle)) {
			t.Errorf("the marshalled %s carries captured bytes: %s", subject.what, encoded)
		}

		var decoded any
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatalf("decoding the marshalled %s: %v", subject.what, err)
		}
		for _, path := range finGatherForbiddenKeyPaths(subject.what, decoded) {
			t.Errorf("the %s carries key %s: this value's whole worth is that it can be published "+
				"unreviewed, and such a field would inherit the operator-review-before-paste "+
				"obligation onto it", subject.what, path)
		}
	}
}

// --- the forbidden-key walk ---------------------------------------------------------

// finGatherForbiddenKeys is what a key must not look like. line and stderr join
// #1271's four because the channels these values are exposed to are
// tdnReapOutcome.Line and trailScanResult.Line, not only a ps column.
func finGatherForbiddenKeys() []string {
	return []string{"command", "args", "comm", "argv", "line", "stderr"}
}

// finGatherExemptKeys names the marshalled keys that match a forbidden substring
// and are legitimate anyway. EXACT KEYS AND NEVER A PREFIX RULE: a future
// ArgvMatches or StderrTail must still trip.
func finGatherExemptKeys() map[string]string {
	return map[string]string{
		// ArgvScanErrored is a bool discriminator recording THAT the scan failed
		// and never what it said (trail_run_outcome_test.go:186-193).
		"argvscanerrored": "the errored discriminator, which records that the scan failed",
		// pinStateOutcome.ToolStderr. #1271 admits pinStateOutcome into the
		// readings WHOLE (trail_run_outcome_test.go:200-203) because it carries no
		// command column by construction — pinStateColumns is `pid=,ppid=,stat=`
		// with an enforcing test. Named here rather than left to fire later: this
		// file's rows match nothing, so Liveness is empty today and the first row
		// that filled it would go red on a shipped field.
		"tool_stderr": "pinStateOutcome's own ps-stderr field, admitted whole by #1271",
	}
}

// finGatherForbiddenKeyPaths walks a decoded JSON value and returns the dotted
// path of every non-exempt key matching a forbidden substring.
//
// It DESCENDS into every object and every array element rather than scanning the
// top level: Gate, Admit, Liveness and the attribution record's Entries[] are all
// nested, and #1280's transplanted flat scan examined none of their keys.
//
// Matching is against the LOWERCASED key, which was not required one layer down
// and is required here: trailRunReadings carries no json tags, so its marshalled
// keys are Go field names (Gate, Admit, Liveness), and a case-sensitive scan over
// lowercase needles would miss a future `Command string` field entirely.
//
// It returns paths rather than calling t.Errorf so the walk is itself testable —
// see TestFinGatherForbiddenKeyWalkDescends.
func finGatherForbiddenKeyPaths(where string, v any) []string {
	var found []string
	switch node := v.(type) {
	case map[string]any:
		for key, child := range node {
			path := where + "." + key
			lowered := strings.ToLower(key)
			if _, exempt := finGatherExemptKeys()[lowered]; !exempt {
				for _, forbidden := range finGatherForbiddenKeys() {
					if strings.Contains(lowered, forbidden) {
						found = append(found, fmt.Sprintf("%s (%q-shaped)", path, forbidden))
						break
					}
				}
			}
			found = append(found, finGatherForbiddenKeyPaths(path, child)...)
		}
	case []any:
		for i, child := range node {
			found = append(found, finGatherForbiddenKeyPaths(fmt.Sprintf("%s[%d]", where, i), child)...)
		}
	}
	return found
}

// TestFinGatherForbiddenKeyWalkDescends is the mutation test for the walk itself.
//
// Without it the walk's depth-handling is only ever exercised against values that
// PASS, and a walk that silently examined the top level alone would report a
// clean sweep over exactly the nesting #1280's flat scan missed. Each row perturbs
// one thing.
func TestFinGatherForbiddenKeyWalkDescends(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{
			name: "a forbidden key one level down, which a flat scan never examines",
			in:   `{"Gate":{"value":"usable","command":"leaked"}}`,
			want: true,
		},
		{
			name: "a forbidden key inside an array element, which is Entries[]'s own shape",
			in:   `{"entries":[{"pgid":7788,"admit":{"reap_line":"leaked"}}]}`,
			want: true,
		},
		{
			name: "the exempt errored discriminator, matched case-insensitively",
			in:   `{"ArgvScanErrored":false}`,
			want: false,
		},
		{
			name: "a field the exemption must NOT cover, because it is an exact key and not a prefix",
			in:   `{"ArgvMatches":["cat /tmp/x"]}`,
			want: true,
		},
		{
			name: "the readings' own key set, which must sweep clean",
			in: `{"Gate":{"value":"usable","terminal_reason":"completed","detail":"d"},` +
				`"Admit":{"value":"proof","detail":"d"},"ArgvScanErrored":false,"MatchCount":0,` +
				`"RowsScanned":12,"Liveness":null,"PyryExited":true,"BoundFrom":"from-start",` +
				`"ClaudeState":""}`,
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var decoded any
			if err := json.Unmarshal([]byte(tc.in), &decoded); err != nil {
				t.Fatalf("decoding the fixture: %v", err)
			}
			got := finGatherForbiddenKeyPaths("fixture", decoded)
			if (len(got) > 0) != tc.want {
				t.Fatalf("the walk reported %v; want a hit: %t — a walk that cannot go red at depth "+
					"reports a clean sweep over the nesting it never entered", got, tc.want)
			}
		})
	}
}

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
// # Two caller-supplied strings cross into the readings VERBATIM, by design
//
// These are the boundaries the no-captured-bytes test must STATE rather than
// leave to its plant site, because the obvious reading of #1271's "plant the
// needle in every string-bearing input" makes it red against shipped code:
//
//   - trailGate fills Reason from res.Trailer.TerminalReason and splices it %q
//     into Detail on BOTH certifying arms (trailer_admissibility_test.go:306,
//     :317); readings.Gate takes that result whole;
//   - the gather hands the same Reason to the fan-out as certified, and
//     trailAdmitAttribution splices it %q into Detail on the budget arm (:428)
//     and the proof arm (:470); readings.Admit takes that result whole too;
//   - finGatherInputs.ClaudeState is copied into readings.ClaudeState whole,
//     trailClassifyRun republishes it as claude_state
//     (trail_run_outcome_test.go:249, :356), and C7 quotes an out-of-contract
//     value %q into the published Detail (:444-449).
//
// So a needle in terminal_reason lands in readings.Gate.Detail and, on the
// finding row, in readings.Admit.Detail as well; a needle in the claude verdict
// lands in readings.ClaudeState itself. In either case the only fix would be to
// stop publishing a field the operator reads to interpret the run — the
// certified reason, or the corroboration verdict. #1280 met the first one layer
// down and resolved it by planting only in the stderr
// (finding_attribution_fanout_test.go:730-731). THE NEEDLE'S HOME HERE IS THE
// TRAILER'S `result` FIELD AND THE REAP-LOG STDERR; it may enter neither
// terminal_reason nor ClaudeState. That plant is the STRONGER test, not a
// weakened one: resultTrailer has no `result` member so trailScanResult.Trailer
// structurally cannot carry it, trailScanResult.Line carries it verbatim but
// capped, and trailRunReadings carries neither field.
//
// Both exclusions are made STRUCTURAL IN THE FIXTURES rather than left as a
// discipline at the plant site: finGatherNeedleTrailer renders terminal_reason
// "completed", and TestFinGatherReturnsNoCapturedBytes hands the gather
// pinStateRunning — a constant from pinReadState's closed set, which cannot
// carry a needle and which proves the field introduces no forbidden key, as ""
// would not.
//
// # Failure messages
//
// A t.Fatalf here MAY name counts, pgids, verdicts, outcome values, gate and
// admit values, BoundFrom, finAttribute* condition names, and the sighting's own
// scalars: its scan state, its staleness, its carries-a-decoded-trailer
// discriminator and the four it copies out of the decode — subtype, is_error,
// terminal_reason and stop_reason. That last one is the model's, forwarded
// unvalidated and crossing uncapped (finSighting, § What the four decoded scalars
// are worth); it is named here so the exposure is inherited KNOWINGLY rather than
// by omission. A message MAY NEVER name pinScan.Matches (verbatim argv off the
// ambient process table), the function-local observation's Line, or a
// trailScanResult's trailer.
//
// Printing a whole readings, record or SIGHTING is safe, and only because
// TestFinGatherReturnsNoCapturedBytes proves it of all three — the licence
// extends exactly as far as that sweep does, which is why #1309 extended the two
// together. That test is what licenses the rest of this file's messages rather
// than being one AC among five.

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
	// finGatherUndocumentedState is a claude liveness verdict nobody defined:
	// neither one of pinReadState's four nor "" for not-read. Deliberately NOT
	// the classifier layer's own "some-verdict-nobody-defined"
	// (trail_run_outcome_test.go:749), so a failure names which layer produced
	// the value that reached C7.
	finGatherUndocumentedState = "fin-gather-verdict-nobody-defined"
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

// finGatherInputs is what a live probe holds when it composes one run's
// readings: #1281's four values, plus the two #1282 left staged in the gather's
// own body.
//
// NAMED FIELDS RATHER THAN POSITIONAL PARAMETERS, for a different reason than
// finRecordInputs' (finding_run_record_test.go:205-212). That type's reason —
// two adjacent same-typed strings on opposite sides of the argv prohibition —
// applies only weakly here, because a bool and a string cannot transpose without
// a compile error. The reason that does apply is
// TestFinGatherPyryExitIsObservableAtTheOutcome's: its two arms must be
// identical in stdout seed, needles, stderr and pinned set BYTE FOR BYTE and
// differ in one field. Across six positional arguments that identity is a
// discipline retyped at each call site and checkable only by reading; over a
// struct it is `copy the value, set one field` — the package's own
// vary-one-dimension idiom (trailRunWellFormed, trail_run_outcome_test.go:611) —
// and it holds BY CONSTRUCTION. Same doctrine either way: prefer the shape that
// cannot be got wrong over the discipline that must not be. Secondarily, a
// reader of finGatherReadings(&stdout, needles, stderr, pinned, true, "") has no
// way to know what that true asserts.
//
// # Both new fields keep the readings' own names, types and ZERO-POLARITY
//
//   - PyryExited's zero is false, which reads as "did not exit" and reaches Step
//     3's void. That is the SAFE direction, and it is the identical argument
//     trailRunReadings.PyryExited makes for itself
//     (trail_run_outcome_test.go:204-207): a caller who omits the field gets a
//     void, never a finding.
//   - ClaudeState's zero is "", which is C7's shipped "not read" (:215-218,
//     :444) — an honest report rather than an unfilled field.
//
// Because the two zeros land where the readings' own zeros land, an
// incompletely-filled finGatherInputs degrades to a named nothing-was-measured
// rather than to a claim.
type finGatherInputs struct {
	// Stdout is the buffer the trailer leg polls. It is read through Bytes(),
	// which returns a COPY (background_trigger_probe_test.go:736-742), so
	// handing one buffer to two gathers is non-destructive.
	Stdout *probeSyncBuffer
	// Needles is the post-trailer argv scan's match set: the content join and
	// the per-pid corroboration.
	Needles []string
	// Stderr is pyry's reap-log stderr, the attribution leg's own input.
	Stderr []byte
	// Pinned is the during-turn pin, the join key into that reap log. []int AND
	// NEVER []reachProc — see the caller's obligations on finGatherReadings.
	Pinned []int
	// PyryExited is whether pyry exited within its deadline, as its caller
	// measured it.
	PyryExited bool
	// ClaudeState is the claude child's liveness verdict, as its caller read it.
	//
	// ITS ADMISSIBLE PRODUCER IS pinReadState's Verdict
	// (process_pin_liveness_test.go:275) over the pid probeWaitForDirectChild
	// returns (background_trigger_probe_test.go:975) — a CLOSED four-value set
	// whose pinStateColumns is `pid=,ppid=,stat=` and carries no command column
	// by construction. NEVER a raw ps column. This value crosses the gather
	// unvalidated, is republished as claude_state and is quoted into a published
	// Detail on C7's arm, so a column read here would put verbatim argv — and
	// with it an operator's CLAUDE_CODE_OAUTH_TOKEN or ANTHROPIC_API_KEY — into
	// an artifact destined for a public issue, which is the channel Pinned is
	// []int to keep shut. "" is admissible and is the honest report for "no
	// claude was read"; the gather validates nothing else, and that is a
	// statement about the gather rather than a licence for its caller.
	ClaudeState string
}

// finSighting is what the trailer poll MEASURED, handed back without handing
// back the sighting that measured it.
//
// finGatherReadings classifies against a trailObservation and then drops it (see
// there). Step 3 of the live composition — finTrailerBuild
// (finding_trailer_evidence_test.go:203) — needs what that sighting saw, and
// recovering it with a SECOND trailWaitForTrailer is not equivalent: by then the
// trailer is already in the buffer, the second call matches on its first poll and
// reports trailBoundFromStart, a discriminator whose own doc says it BOUNDS
// NOTHING (result_trailer_observation_test.go:80-84). The difference is a
// mis-report and not a cost. So the gather copies the measurements onto this
// value, which carries neither .Line nor the decoded pointer. #1308 moves
// finTrailerBuild onto it; nothing consumes it yet.
//
// # What it deliberately does not carry
//
//   - NO Bounded. lateness_bounded is BoundFrom == trailBoundFromMiss AND NOTHING
//     ELSE (finding_trailer_evidence_test.go:209-215), and that stays its one
//     source; this value supplies the discriminator that derivation reads. A
//     second source would let a record publish a non-bound wearing a bound's
//     label.
//   - NO ObservedAt. finTrailerBuild does not read it, and the constraint is
//     one-way: the carrier holds what its consumer needs and nothing more. A clock
//     value with no reader is a field whose zero has to be argued about later.
//   - NO Detail. The published Detail is finTrailerBuild's and stays there. The
//     omission is what discharges #1284's headroom rule STRUCTURALLY rather than
//     by an assertion: with no formatted string here there is no 512-byte budget
//     for a leak to hide behind, so the sweep below owes no per-row headroom
//     check. Adding a Detail later would owe it in full
//     (finding_trailer_evidence_test.go:648-662).
//
// # Staleness travels as PUBLISHED EVIDENCE and is not a classifier input
//
// trailRunReadings deliberately has no field for it
// (trail_run_outcome_test.go:211-214): trailBoundFromStart carries a real duration
// that bounds nothing, so a classifier able to read a staleness could be tempted
// to discriminate on it. Carrying it HERE does not admit it THERE, and the two
// types stay separate for exactly that reason.
//
// # CarriesTrailer records the outcome of a PAIR, not a State
//
// finTrailerBuild gates on obs.State == trailSeen && obs.Trailer != nil (:218).
// Once this value is forbidden the pointer, that pair is unrecomputable
// downstream, and State alone cannot serve — a trailSeen scan with a nil Trailer
// is exactly the case the pair exists to separate. The field is what lets a reader
// tell "there was no trailer" from "the trailer's fields were empty". No reachable
// sighting separates the two today, because trailWaitForTrailer fills Trailer on
// every trailSeen result; the pair is the shape that stays correct if a later scan
// learns to return one without the other.
//
// Its zero is false, which routes to the no-decoded-trailer arm where the four
// scalars are zeroes — an honest nothing-was-measured, the same argument
// finGatherInputs.PyryExited makes for itself (:209-213). An incompletely-filled
// sighting degrades to a named nothing rather than to a claim.
//
// # What the four decoded scalars are worth
//
// wireFields (emitter.go:428-437) derives Subtype, IsError and TerminalReason from
// a SINGLE ExitReason, so their agreement is one value rendered three ways and not
// three corroborating reads; TerminalReason is pyry's own synthesis and claude
// never emitted it. Only StopReason is independently sourced, forwarded from the
// model's last message unvalidated (emitter.go:210): it is the one
// model-influenced field crossing this value UNCAPPED, exactly as it crosses
// finTrailerRecord (finding_trailer_evidence_test.go:114-124). Capping it is out
// of scope there and here; naming it is what keeps a later sweep author from
// planting a needle in a field this value must carry verbatim.
//
// # The json keys, and the absence of omitempty
//
// Tagged because encoding/json renders a Duration as a bare nanosecond count and
// the unit belongs in the key — trailObservation:133-135 and finTrailerRecord:147
// both give that reason — and MIRRORING finTrailerRecord's keys, so #1308's move
// onto this value is a rename-free projection. NO FIELD CARRIES omitempty, for
// that record's own reason (:102-110): the discriminator must always be present
// beside the four, and dropping a false IsError or an honest zero Staleness
// collapses distinctions this type exists to keep.
type finSighting struct {
	State          string        `json:"trailer_state"`
	BoundFrom      string        `json:"lateness_bound_from"`
	Staleness      time.Duration `json:"staleness_ns"`
	CarriesTrailer bool          `json:"carries_trailer"`

	Subtype        string `json:"subtype"`
	IsError        bool   `json:"is_error"`
	TerminalReason string `json:"terminal_reason"`
	StopReason     string `json:"stop_reason"`
}

// finGatherReadings assembles a complete trailRunReadings from the
// finGatherInputs a live probe holds, using ONLY shipped producers, and returns
// the attribution record alongside it.
//
// Nothing it returns is built by struct literal: no trailGateResult,
// trailAdmitResult or tdnReapOutcome reaches the readings except as the output of
// trailGate, finAttributeFanOut or pinScanArgv/pinReadState. Funnelling the whole
// composition through one function is what makes that checkable in one place
// rather than argued across call sites.
//
// # What it returns, and what it deliberately does not
//
// The trailer observation is STILL a FUNCTION-LOCAL INTERMEDIATE and is still
// never returned. trailObservation embeds trailScanResult
// (result_trailer_observation_test.go:125-126), so handing it back would promote
// .Line and .Trailer straight into the caller's reach — and both withholdings
// stand. .Line is verbatim model output marked OPERATOR-REVIEW-BEFORE-PASTE,
// roughly 415 of its retained 512 bytes being the trailer's `result` field, which
// is the assistant's last message. .Trailer is a *resultTrailer, which carries
// PermissionDenials *[]json.RawMessage (tool_loop_test.go:199) — raw bytes no cap
// applies to — behind a pointer whose unchecked deref panics by design. COPYING
// SCALARS OUT is what keeps both out of reach, and it is the whole point of the
// third return.
//
// What DOES leave the function is what the sighting MEASURED, on that third
// return: a finSighting, which carries neither of the two. #1309 added it because
// Step 3 of the live composition (finTrailerBuild) needs the bound, its
// discriminator, the staleness, the scan state and the four decoded scalars,
// and because recovering them with a SECOND trailWaitForTrailer would mis-report
// rather than merely cost — see finSighting for that argument.
//
// So the divergence from trailRigGather narrows rather than dies: that one
// returns the observation because #1268's AC4 needed Staleness; this one withholds
// the observation and copies out what a reader needs. Staleness travels from
// #1309 onward, ON THE CARRIER AND AS PUBLISHED EVIDENCE. It is still NOT a
// classifier input and must not be used as one, which is why trailRunReadings
// still has no field for it (trail_run_outcome_test.go:211-214) and why the two
// types stay separate.
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
func finGatherReadings(in finGatherInputs) (trailRunReadings, finAttributeRecord, finSighting) {
	var readings trailRunReadings
	var record finAttributeRecord
	var sighting finSighting

	// The trailer leg. The observation's embedded scan result is REUSED rather
	// than re-scanned, because that is the composition a live probe performs.
	obs := trailWaitForTrailer(in.Stdout, finGatherTrailerWait)
	readings.BoundFrom = obs.BoundFrom
	readings.Gate = trailGate(obs.trailScanResult)

	// What that sighting MEASURED, copied out of the same obs that just filled
	// BoundFrom and fed the gate: one poll, no second scan, no second wait and no
	// clock reading of this function's own — trailWaitForTrailer stamped both
	// durations itself (result_trailer_observation_test.go:256-262).
	//
	// Inline rather than behind a finSightingFrom constructor, for the reason
	// stated above: funnelling the whole composition through one function is what
	// makes it checkable in one place, and a constructor would add a symbol whose
	// tests either duplicate this one's or do not exist.
	sighting = finSighting{State: obs.State, BoundFrom: obs.BoundFrom, Staleness: obs.Staleness}
	// The State operand goes FIRST and Go's && short-circuits left to right, so
	// the ordering is a property of the source rather than of this comment. It is
	// deliberately identical to finTrailerBuild:218, because #1308 moves that
	// builder onto this value and the two computations must agree.
	sighting.CarriesTrailer = obs.State == trailSeen && obs.Trailer != nil
	if sighting.CarriesTrailer {
		sighting.Subtype = obs.Trailer.Subtype
		sighting.IsError = obs.Trailer.IsError
		sighting.TerminalReason = obs.Trailer.TerminalReason
		sighting.StopReason = obs.Trailer.StopReason
	}

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
		record = finAttributeFanOut(in.Stderr, in.Pinned, readings.Gate.Reason)
		if !finAttributeHasCondition(record, finAttributeNoGroups) {
			readings.Admit = record.Selected
		}
	}

	// The argv leg. No exclusions: this file excludes nothing, and nil says so
	// more clearly than an empty map. One call, three fields — C8/C9's subject.
	scan, err := pinScanArgv(in.Needles, nil)
	readings.ArgvScanErrored = err != nil
	readings.MatchCount = scan.MatchCount
	readings.RowsScanned = scan.RowsScanned

	// One per-pid read per MATCHED pid, taken here rather than at a call site, so
	// "Liveness holds a read for each" is true by construction of the gather.
	for _, match := range scan.Matches {
		readings.Liveness = append(readings.Liveness, pinReadState(match.PID))
	}

	// The caller's own two readings, carried WHOLE. No default, no zero-value
	// rewrite, no fill-in-if-unset, no normalisation and no pinIsVerdict call: C7
	// is the classifier's check over the claude verdict and it is already
	// shipped, so a second opinion here would repair exactly the record C7 exists
	// to reject and put trailOutcomeOutOfContract out of reach through this
	// composition.
	//
	// What honest readings buy is ONE class of run: with a usable gate and an
	// attribution that is not proof, PyryExited false now reaches Step 3's
	// staging void where the constant reported a scan-side answer or a Step 4-6
	// void. A proof-carrying run is unchanged — Step 2 sits above Step 3 by
	// design (trail_run_outcome_test.go:323-334) and proofPyryLive pins it
	// (:695-696, :820-821) — and so is a run whose gate is not usable, which Step
	// 1 answers first.
	readings.PyryExited = in.PyryExited
	readings.ClaudeState = in.ClaudeState

	return readings, record, sighting
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

			readings, record, _ := finGatherReadings(finGatherInputs{
				Stdout:  &stdout,
				Needles: finGatherNeedles(t),
				Stderr:  tc.stderr,
				Pinned:  tc.pinned,
				// Passed on every row rather than carried as a finGatherCase
				// field: all three rows want this value, so a per-row field would
				// be a zero to point somewhere plus three copies to keep in step.
				// No pyry runs here, so there is none to fail to exit.
				PyryExited: true,
				// ClaudeState omitted: no claude runs here, and "" is C7's
				// shipped "not read".
			})
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

			readings, record, _ := finGatherReadings(finGatherInputs{
				Stdout:  &stdout,
				Needles: finGatherNeedles(t),
				Stderr:  stderr,
				Pinned:  tc.pinned,
				// No pyry runs here, so there is none to fail to exit;
				// ClaudeState is omitted because no claude runs here either.
				PyryExited: true,
			})

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

// finGatherNegativeInputs returns the inputs of finGatherCases()' second row —
// an ordinary trailer, an anchored reap line naming a group the caller did NOT
// pin, and a needle nothing is staged at — with both caller readings at the
// values the gather's two constants used to hold.
//
// It seeds the buffer it is handed and is the base the two tests below copy and
// vary ONE field of. The negative composition is the one those tests need,
// because it is the only class of run the promotion moves: its gate is usable,
// so Step 1 does not answer it, and its attribution is not proof, so Step 2 does
// not either.
func finGatherNegativeInputs(t *testing.T, stdout *probeSyncBuffer) finGatherInputs {
	t.Helper()
	finGatherSeed(t, stdout, trailFixtureTrailer)
	return finGatherInputs{
		Stdout:     stdout,
		Needles:    finGatherNeedles(t),
		Stderr:     []byte(trailReapLine(1, fmt.Sprintf("[%d]", finGatherNamedPGID)) + "\n"),
		Pinned:     []int{finGatherUnnamedPGID},
		PyryExited: true,
	}
}

// TestFinGatherPyryExitIsObservableAtTheOutcome pins that the pyry-exit reading
// the CALLER hands in is what the outcome is decided on, by varying that field
// alone across two arms and separating their outcomes.
//
// # Why the pair is built on the negative arm and never on the proof arm
//
// Promoting the reading moves the outcome of exactly one class of run: usable
// gate, attribution NOT proof. A run whose gate is not usable is answered by
// Step 1 before the reading is consulted, and a PROOF-CARRYING run is answered
// by Step 2 — which sits above Step 3 BY DESIGN
// (trail_run_outcome_test.go:323-334, "Voiding it for a staging failure would
// SUPPRESS A FINDING THE RUN GENUINELY ESTABLISHED") and is pinned by
// proofPyryLive, a proof carrying PyryExited false that wants
// trailOutcomeRunningAtTrailer (:695-696, :820-821). Building this pair on the
// proof arm would require reordering the classifier, which is a defect and not
// a fix.
//
// # The two arms are identical BY CONSTRUCTION, not by discipline
//
// The live arm is a COPY of the exited arm's finGatherInputs with one field set,
// so the stdout buffer, the needle slice, the stderr and the pinned set are the
// same values rather than equal-looking rebuilds. Sharing one buffer is sound:
// trailWaitForTrailer only ever calls stdout.Bytes()
// (result_trailer_observation_test.go:251), which returns a COPY of an
// append-only buffer (background_trigger_probe_test.go:736-742), so the read is
// non-destructive, both arms observe the same bytes on their first poll and both
// take BoundFrom trailBoundFromStart. Rebuilding the needles instead of sharing
// them would vary a second dimension, because finGatherNeedles calls t.TempDir()
// and that returns a NEW directory on every call.
func TestFinGatherPyryExitIsObservableAtTheOutcome(t *testing.T) {
	var stdout probeSyncBuffer
	exited := finGatherNegativeInputs(t, &stdout)

	exitedReadings, _, _ := finGatherReadings(exited)

	// The premises. The first two put this pair in the class of run the reading
	// can move at all; without the last two the exited arm could be sitting on
	// Step 5's run-void-no-rows-parsed and the pair would separate two voids
	// rather than an answer from a void.
	if exitedReadings.Gate.Value != trailGateUsable || exitedReadings.Gate.Reason != "completed" {
		t.Fatalf("the gate reads %q certifying %q; want %q certifying \"completed\" — at any other "+
			"gate value Step 1 answers both arms before the pyry-exit reading is reached and the "+
			"pair asserts nothing", exitedReadings.Gate.Value, exitedReadings.Gate.Reason,
			trailGateUsable)
	}
	if exitedReadings.Admit.Value != trailAdmitVoidGroupUnnamed {
		t.Fatalf("the attribution reads %q; want %q — on %s Step 2 answers both arms and the pair "+
			"would be asserting against the classifier's own ordering",
			exitedReadings.Admit.Value, trailAdmitVoidGroupUnnamed, trailAdmitProof)
	}
	if exitedReadings.MatchCount != 0 || exitedReadings.RowsScanned <= 0 {
		t.Fatalf("the argv scan matched %d of %d row(s); want 0 of more than 0 — at zero rows the "+
			"exited arm lands on %s and the pair separates two voids",
			exitedReadings.MatchCount, exitedReadings.RowsScanned, trailOutcomeVoidNoRowsParsed)
	}

	live := exited
	live.PyryExited = false
	liveReadings, _, _ := finGatherReadings(live)

	// Each arm's reading is the one its caller handed in, which is the crossing
	// the outcomes below are downstream of.
	if exitedReadings.PyryExited != exited.PyryExited ||
		liveReadings.PyryExited != live.PyryExited {
		t.Fatalf("the arms were handed %t and %t and their readings carry %t and %t — the value "+
			"is being rewritten inside the gather rather than carried", exited.PyryExited,
			live.PyryExited, exitedReadings.PyryExited, liveReadings.PyryExited)
	}

	exitedOutcome := trailClassifyRun(exitedReadings)
	liveOutcome := trailClassifyRun(liveReadings)

	// Stated first, because this is the failure a gather that ignores the value
	// produces and the message that names the diagnosis.
	if exitedOutcome.Value == liveOutcome.Value {
		t.Fatalf("both arms classify as %q although they differ in the pyry-exit reading ALONE "+
			"(%t against %t): the value is not reaching the classifier, so a run in which pyry "+
			"never exited would be reported as whatever the scan happened to say",
			exitedOutcome.Value, exited.PyryExited, live.PyryExited)
	}
	if exitedOutcome.Value != trailOutcomeNoRowMatched {
		t.Fatalf("the arm reporting pyry as exited classifies as %q (%s); want %q — this arm is "+
			"the composition's own answer and must be unchanged by the promotion",
			exitedOutcome.Value, exitedOutcome.Detail, trailOutcomeNoRowMatched)
	}
	if liveOutcome.Value != trailOutcomeVoidPyryDidNotExit {
		t.Fatalf("the arm reporting pyry as still live classifies as %q (%s); want %q — every "+
			"reading on that arm is staged after a teardown that did not complete, and reporting "+
			"the scan-side answer instead states the negative %s names about a run that never "+
			"reached it", liveOutcome.Value, liveOutcome.Detail, trailOutcomeVoidPyryDidNotExit,
			trailOutcomeNoRowMatched)
	}
}

// TestFinGatherCarriesTheClaudeVerdictAsHandedIn pins that the claude liveness
// verdict crosses the gather EXACTLY as the caller handed it in: not defaulted,
// not re-derived from anything else the gather read, and not validated on the
// way.
//
// # Why this is not a finGatherCases row
//
// finGatherAssertContract runs on every row of that table and fails any row
// classifying trailOutcomeOutOfContract (:511-514), which is precisely the
// answer the middle row requires. The claim needs a test of its own.
//
// # Why the gather validates nothing
//
// C7 (trail_run_outcome_test.go:444-449) is the classifier's check over this
// field and it is already shipped. A pinIsVerdict call in the gather would
// repair exactly the record C7 exists to reject, putting the out-of-contract
// answer out of reach through this composition — and it would break the LAST row
// first, because "" is C7's shipped "not read" and is not a pinIsVerdict value.
// A caller with no claude to read must be able to say so.
//
// The first and last rows want the SAME outcome, and that is the point: the
// verdict is corroboration, and corroboration never moves the answer
// (trail_run_outcome_test.go:336-343).
func TestFinGatherCarriesTheClaudeVerdictAsHandedIn(t *testing.T) {
	var stdout probeSyncBuffer
	base := finGatherNegativeInputs(t, &stdout)

	cases := []struct {
		name        string
		claudeState string
		wantOutcome string
		// wantDetail is a fragment of the arm's OWN sentence. Asserted rather
		// than the value alone because eight other contract checks answer with
		// trailOutcomeOutOfContract, so a gather bug that broke the gate or the
		// bound would satisfy a value-only assertion.
		wantDetail string
	}{
		{
			name:        "a documented verdict crosses unchanged and does not move the outcome",
			claudeState: pinStateRunning,
			wantOutcome: trailOutcomeNoRowMatched,
		},
		{
			name:        "a verdict outside the documented set reaches the out-of-contract answer unrepaired",
			claudeState: finGatherUndocumentedState,
			wantOutcome: trailOutcomeOutOfContract,
			wantDetail:  "the claude-still-alive reading carries verdict",
		},
		{
			name:        "the empty value stays reachable as a deliberate not-read",
			claudeState: "",
			wantOutcome: trailOutcomeNoRowMatched,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := base
			in.ClaudeState = tc.claudeState

			readings, _, _ := finGatherReadings(in)

			// IDENTITY with what was handed in, never membership: a gather that
			// normalised, blanked or re-derived the field would still satisfy a
			// pinIsVerdict check on the first row.
			if readings.ClaudeState != tc.claudeState {
				t.Fatalf("the gather was handed the claude verdict %q and its readings carry %q — "+
					"the value is defaulted, repaired or re-derived rather than carried",
					tc.claudeState, readings.ClaudeState)
			}
			// The premise for both halves of "not re-derived": this file's scan
			// matches no row, so there is no per-pid read the gather could have
			// derived a verdict from — and C7's per-pid loop cannot be the arm
			// that fires on the middle row.
			if len(readings.Liveness) != 0 {
				t.Fatalf("the readings carry %d per-pid read(s); want none — with a non-empty "+
					"liveness set a gather deriving the claude verdict from it would pass the "+
					"identity check by coincidence", len(readings.Liveness))
			}

			outcome := trailClassifyRun(readings)

			// The second half of "carried exactly as handed in": the classifier
			// republishes the field verbatim (trail_run_outcome_test.go:356).
			if outcome.ClaudeState != tc.claudeState {
				t.Fatalf("the published claude_state reads %q; want the %q that was handed in",
					outcome.ClaudeState, tc.claudeState)
			}
			if outcome.Value != tc.wantOutcome {
				t.Fatalf("the readings classify as %q (%s); want %q", outcome.Value, outcome.Detail,
					tc.wantOutcome)
			}
			if tc.wantDetail != "" && !strings.Contains(outcome.Detail, tc.wantDetail) {
				t.Errorf("detail: got %q, want C7's own claude-verdict sentence — the arm reached "+
					"matters as much as the value, because eight other contract checks also answer "+
					"%s", outcome.Detail, trailOutcomeOutOfContract)
			}
		})
	}
}

// TestFinGatherReturnsNoCapturedBytes makes the operator-review-before-paste
// obligation checkable rather than advisory, over ALL THREE of the gather's
// returns.
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

	readings, record, sighting := finGatherReadings(finGatherInputs{
		Stdout:     &stdout,
		Needles:    finGatherNeedles(t),
		Stderr:     stderr,
		Pinned:     []int{finGatherNamedPGID},
		PyryExited: true,
		// A CONSTANT FROM pinReadState's CLOSED SET rather than "": it makes the
		// header's ClaudeState exclusion structural in the fixture instead of a
		// discipline at the plant site, the way finGatherNeedleTrailer does for
		// terminal_reason, and it proves the field introduces no forbidden key —
		// which "" would not. It does not move the outcome, because a verdict is
		// corroboration (trail_run_outcome_test.go:336-343).
		ClaudeState: pinStateRunning,
	})

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

	// --- the checks, over ALL THREE returns ---

	for _, subject := range []struct {
		what  string
		value any
	}{
		{"readings", readings},
		{"attribution record", record},
		// #1309's carrier. The moment the gather returned a third value this
		// slice had to grow, or the file would ship an unswept return. It is
		// expected to pass STRUCTURALLY — resultTrailer has no `result` member,
		// so the plant above cannot reach the four decoded scalars through the
		// decode, and the carrier holds no Line — which is the property being
		// claimed rather than a vacuity: the row is a live guard against a
		// FUTURE field, caught by bytes here, by name in the key walk and by
		// type in TestFinSightingReachesNoScanType.
		{"sighting", sighting},
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

// TestFinGatherSightingComesFromTheClassifiedPoll pins that the third return is
// filled from the SAME trailWaitForTrailer sighting that fills the readings'
// BoundFrom and feeds trailGate — one call inside the gather, no second scan and
// no second wait.
//
// # What one gather call can show here, and what it cannot
//
// Agreement on the bound discriminator is the claim available from a single call.
// A second trailWaitForTrailer over this pre-seeded buffer would ALSO report
// trailBoundFromStart, so this row does not go red against a re-scan; the row that
// would needs a buffer that misses a poll first, and it is #1310's along with its
// wall clock. What this row does close is a carrier filled from anywhere other
// than the poll whose scan result was classified.
//
// Staleness has no counterpart on the readings to agree with — trailRunReadings
// deliberately carries no staleness field (trail_run_outcome_test.go:211-214) — so
// it is pinned as FILLED FROM THIS SIGHTING RATHER THAN LEFT BEHIND: non-zero and
// under the poll's own timeout. A left-behind field is exactly zero, which is why
// the weakened >= 0 form would be vacuous rather than merely lenient.
func TestFinGatherSightingComesFromTheClassifiedPoll(t *testing.T) {
	var stdout probeSyncBuffer
	// The negative composition, seeded with an ordinary trailer so the first poll
	// hits and no row waits out finGatherTrailerWait.
	readings, _, sighting := finGatherReadings(finGatherNegativeInputs(t, &stdout))

	// The premise: the poll MATCHED. At any other state the bound is
	// trailBoundNone and the staleness is legitimately zero, and the two checks
	// below would be asserting against a sighting that measured nothing.
	if sighting.State != trailSeen {
		t.Fatalf("the sighting reports scan state %q; want %q — the buffer is pre-seeded with an "+
			"ordinary trailer, so a first poll that did not match means this row is asserting "+
			"about a sighting with no bound to carry", sighting.State, trailSeen)
	}

	if sighting.BoundFrom != readings.BoundFrom {
		t.Fatalf("the sighting reports bound origin %q and the readings %q — ONE gather call makes "+
			"ONE trailWaitForTrailer call, so a disagreement means the carrier was filled from a "+
			"second scan, whose first poll matches by construction and reports %q, a discriminator "+
			"that bounds nothing", sighting.BoundFrom, readings.BoundFrom, trailBoundFromStart)
	}
	if sighting.Staleness <= 0 || sighting.Staleness >= finGatherTrailerWait {
		t.Fatalf("the sighting reports staleness %v; want a duration in (0, %v) — a field the copy "+
			"left behind is EXACTLY zero, and one at or above the poll's own timeout could not "+
			"have been measured by it", sighting.Staleness, finGatherTrailerWait)
	}
}

// TestFinGatherSightingCarriesTheDecodedScalars pins the carrier's own
// discriminator and the four decoded scalars across the two arms the shipped
// fixtures already reach.
//
// # What these rows are, and what they are not
//
// Over the in-cap fixture the decoded arm is a FILL CHECK AND NOTHING MORE: the
// capped copy and the full line are the same bytes there, so no row here can claim
// which of the two the gather read. That discrimination needs a fixture whose
// terminal_reason falls past the cap, and it is #1310's.
//
// Equally, no reachable sighting produces trailSeen with a nil Trailer —
// trailWaitForTrailer fills the pointer on every seen result — so the inconsistent
// pair is not exercised. It arrives only from a hand-built fixture, which is all
// finTrailerBuild ever sees and nothing this gather can emit.
//
// # Neither arm costs wall clock
//
// Both seeds pre-empt the poll loop: the first matches on the first iteration, and
// trailWaitForTrailer returns from an ABORTED scan immediately because abortion is
// monotone (:129-135). A genuinely absent trailer would carry no decode either and
// would burn finGatherTrailerWait in full, which is why it is not the arm used.
func TestFinGatherSightingCarriesTheDecodedScalars(t *testing.T) {
	cases := []struct {
		name string
		seed string
		// wantState and wantCarries are asserted on the SHIPPED SCAN first and on
		// the carrier second, so a fixture edit that moved the arm reports itself
		// rather than silently retargeting the row.
		wantState   string
		wantCarries bool
	}{
		{
			name:        "a decoded trailer, whose four fields cross onto the carrier",
			seed:        trailFixtureTrailer,
			wantState:   trailSeen,
			wantCarries: true,
		},
		{
			// finGatherCases()' C4 row (:413-423), for its stated reason: it is the
			// no-decode arm this file already ships and it returns immediately.
			name:        "an aborted scan, which carries no decoded trailer at all",
			seed:        trailPaddedTrailer(trailOverlongPad),
			wantState:   trailAborted,
			wantCarries: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout probeSyncBuffer
			seed := finGatherSeed(t, &stdout, tc.seed)

			// Recomputed through the SHIPPED scanner over the SAME bytes the gather
			// read, the file's own C2 idiom (:502): the expectation is the decode's
			// and never four typed-in literals. On the no-decode arm want stays the
			// zero resultTrailer, which makes "their zero values" exact.
			scan := trailScan(seed)
			var want resultTrailer
			if scan.Trailer != nil {
				want = *scan.Trailer
			}

			if scan.State != tc.wantState {
				t.Fatalf("the shipped scan reads state %q (%s); want %q — the row's expectations "+
					"below are tied to the arm this fixture reaches, so a fixture that moved arms "+
					"must fail here rather than retarget them", scan.State, scan.Detail, tc.wantState)
			}
			if (scan.Trailer != nil) != tc.wantCarries {
				t.Fatalf("the shipped scan carries a decoded trailer: %t; want %t — the carrier's "+
					"discriminator is pinned against the scan rather than against a literal",
					scan.Trailer != nil, tc.wantCarries)
			}

			_, _, sighting := finGatherReadings(finGatherInputs{
				Stdout:  &stdout,
				Needles: finGatherNeedles(t),
				// No reap log and no pin: this row asserts about the trailer leg,
				// and the attribution leg's own rows are this file's other tests.
				PyryExited: true,
			})

			if sighting.State != tc.wantState {
				t.Errorf("the sighting reports scan state %q; want %q", sighting.State, tc.wantState)
			}
			if sighting.CarriesTrailer != tc.wantCarries {
				t.Fatalf("the sighting reports carries-a-decoded-trailer %t; want %t — the field "+
					"records the outcome of the State/nil PAIR finTrailerBuild gates on, and a "+
					"reader with only a State cannot tell \"there was no trailer\" from \"the "+
					"trailer's fields were empty\"", sighting.CarriesTrailer, tc.wantCarries)
			}
			if sighting.Subtype != want.Subtype || sighting.IsError != want.IsError ||
				sighting.TerminalReason != want.TerminalReason ||
				sighting.StopReason != want.StopReason {
				t.Errorf("the sighting carries subtype=%q is_error=%t terminal_reason=%q "+
					"stop_reason=%q; want %q, %t, %q, %q — the four are copied out of the decode "+
					"this scan produced, and on an arm carrying none they hold its zero values",
					sighting.Subtype, sighting.IsError, sighting.TerminalReason, sighting.StopReason,
					want.Subtype, want.IsError, want.TerminalReason, want.StopReason)
			}
		})
	}
}

// TestFinSightingReachesNoScanType is the carrier's structural claim, and a
// comment could not serve it: the point is that a LATER EDIT adding a field that
// carries any of the three ONE LEVEL DOWN fails too, which asserting over a single
// instance would never catch.
//
// finRecordInputReaches (finding_run_record_test.go:731) is the family's walker —
// same package, same build tag — and it follows struct fields, slice and array
// elements, pointers and map keys and values, so naming resultTrailer catches a
// *resultTrailer as well. The increment here is the third forbidden type, not a
// second traversal.
//
// Scope is the carrier alone. trailRunReadings argues its own non-reachability at
// trail_run_outcome_test.go:208-214 and finRecordRun's is pinned by
// TestFinRecordEmbedsTrailerRecordWhole; restating either here would be scope
// creep.
func TestFinSightingReachesNoScanType(t *testing.T) {
	carrier := reflect.TypeOf(finSighting{})
	for _, forbidden := range []reflect.Type{
		reflect.TypeOf(trailObservation{}),
		reflect.TypeOf(trailScanResult{}),
		reflect.TypeOf(resultTrailer{}),
	} {
		if finRecordInputReaches(carrier, forbidden, map[reflect.Type]bool{}) {
			t.Errorf("%s is reachable from finSighting: taking it would promote "+
				"trailScanResult.Line — verbatim model output, OPERATOR-REVIEW-BEFORE-PASTE — or "+
				"the *resultTrailer, and with it PermissionDenials *[]json.RawMessage, raw bytes "+
				"no cap applies to, back into the caller's reach, along with the "+
				"panic-on-unchecked-deref obligation the discriminated optional imposes. Copying "+
				"the four scalars out is what keeps all three out of reach", forbidden)
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

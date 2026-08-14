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
// trailRigGather is the correct composition wired to
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
// doc (`finAttributeFanOut`) states why: reachProc.Command
// is verbatim argv read off the ambient process table, and a []reachProc
// signature that recorded only .PGID would pass every test in this file while
// reopening the credential channel. The signature is the enforcement.
//
// # The proof outranks every match-count arm
//
// trailClassifyRun's Step 2 returns
// trailOutcomeRunningAtTrailer on Admit.Value == trailAdmitProof BEFORE Step 4
// (ArgvScanErrored), Step 5 (RowsScanned == 0), Step 7 (MatchCount > 0) or
// Step 8 are consulted. That is what makes this file's rows cheap: with no
// subject staged EVERY row runs at MatchCount == 0, and the finding and the
// negative still separate. Stretching a hold past the reap to force a
// post-trailer match would destroy the very ordering the proof rests on.
//
// # An empty match set under a certifying gate IS the documented healthy negative
//
// trailRunWellFormed() (`trailRunWellFormed`) — the classifier's own
// canonical well-formed input — is trailGateUsable + "completed" + MatchCount 0 +
// RowsScanned 12 + trailAdmitVoidGroupUnnamed, and it reaches
// trailOutcomeNoRowMatched, whose Detail says that value is "A statement about THE
// SCAN and NOT A STATEMENT THAT THE COMMAND HAD EXITED". Reaching for a run-void-*
// to make the negative arm "assert a void" would vary two dimensions at once.
//
// # Three caller-supplied strings cross into the readings VERBATIM, by design
//
// These are the boundaries the no-captured-bytes tests must STATE rather than
// leave to their plant sites, because the obvious reading of #1271's "plant the
// needle in every string-bearing input" makes them red against shipped code:
//
//   - trailGate fills Reason from res.Trailer.TerminalReason and splices it %q
//     into Detail on BOTH certifying arms (`trailDetail`,
//     :399); readings.Gate takes that result whole;
//   - the gather hands the same Reason to the fan-out as certified, and
//     trailAdmitAttribution splices it %q into Detail on the budget arm (:534)
//     and the proof arm (:598); readings.Admit takes that result whole too;
//   - finGatherInputs.ClaudeState is copied into readings.ClaudeState whole,
//     trailClassifyRun republishes it as claude_state
//     (`trailRunOutcome`, :650), and C7 quotes an out-of-contract
//     value %q into the published Detail (:740-745).
//
// So a needle in terminal_reason lands in readings.Gate.Detail and, on the
// finding row, in readings.Admit.Detail as well; a needle in the claude verdict
// lands in readings.ClaudeState itself. In either case the only fix would be to
// stop publishing a field the operator reads to interpret the run — the
// certified reason, or the corroboration verdict. #1280 met the first one layer
// down and resolved it by planting only in the stderr
// (finding_attribution_fanout_test.go:731-732). THE NEEDLE'S HOME IN THAT SWEEP
// IS THE TRAILER'S `result` FIELD AND THE REAP-LOG STDERR; it may enter neither
// terminal_reason nor ClaudeState. That plant is the STRONGER test, not a
// weakened one: resultTrailer has no `result` member so trailScanResult.Trailer
// structurally cannot carry it, trailScanResult.Line carries it verbatim but
// capped, and trailRunReadings carries neither field.
//
// #1452 ADDED THE THIRD, and it is the one exception to the paragraph above:
// finGatherInputs.RunnerPath crosses through finGatherRunnerPath onto
// trailGateResult.RunnerPath and is republished as runner_path, but NO Detail
// anywhere interpolates it — the gate copies it to a field and nowhere into
// prose, and every arm of trailReasonAgainstPath is fixed prose over its own
// file's constants. So a needle planted THERE has a real route to the record and
// no legitimate publication to defend, which is why it gets a sweep of its own
// (TestFinGatherRunnerPathCarriesNoCapturedBytes) rather than an exclusion.
// What keeps it clean is the field's contract: only tdnRunnerFromArgv's five
// source-authored constants may arrive, and they echo no argv.
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
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// --- constants ------------------------------------------------------------------

const (
	// finGatherTrailerWait is the trailer poll's timeout, matching
	// trailRigTrailerWait. NO ROW EVER WAITS IT OUT — a claim that no longer rests
	// on pre-seeding alone, because two rows deliberately do not pre-seed:
	//
	//   - the pre-seeded rows still hit on the FIRST poll, so their loop returns
	//     before it ever sleeps;
	//   - TestFinGatherSightingReportsTheMissBound and
	//     TestFinGatherRecordPublishesTheMeasuredMissBound each leave the buffer
	//     empty on purpose and append the trailer past two poll ticks, so each
	//     one's trailer arrives well inside the wait and each loop costs roughly
	//     600ms — THESE TWO ARE THIS FILE'S ONLY WALL CLOCK, and what the miss
	//     bound costs;
	//   - the non-certifying row seeds an over-long line, which trailWaitForTrailer
	//     returns from IMMEDIATELY (result_trailer_observation_test.go:289-291)
	//     because abortion is monotone.
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
	// (`trailRunCases`), so a failure names which layer produced
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
//     (`trailNeedle`), which would make the stdout
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
// readings: #1281's four values, the two #1282 left staged in the gather's own
// body, the runner-path reading #1452 routed in from the caller, and the
// pinned-pid read #1458 routed in from the same place.
//
// NAMED FIELDS RATHER THAN POSITIONAL PARAMETERS, for a reason that has GROWN
// INTO finRecordInputs' (finding_run_record_test.go:205-212). That type's reason
// — two adjacent same-typed strings — did not apply while the staged pair was a
// bool and a string, which cannot transpose without a compile error. Since #1452
// ClaudeState and RunnerPath are both string and adjacent, so it does. Neither
// is verbatim argv, so a transposition here produces a wrong reading rather than
// a publication of captured bytes; named fields make it a compile error anyway.
//
// The reason that applied from the start is
// TestFinGatherPyryExitIsObservableAtTheOutcome's: its two arms must be
// identical in stdout seed, needles, stderr and pinned set BYTE FOR BYTE and
// differ in one field. Across eight positional arguments that identity is a
// discipline retyped at each call site and checkable only by reading; over a
// struct it is `copy the value, set one field` — the package's own
// vary-one-dimension idiom (trailRunWellFormed) —
// and it holds BY CONSTRUCTION. Same doctrine either way: prefer the shape that
// cannot be got wrong over the discipline that must not be. Secondarily, a
// reader of finGatherReadings(&stdout, needles, stderr, pinned, true, "", "", pin)
// has no way to know what that true asserts.
//
// # All four staged fields keep the readings' own names, types and ZERO-POLARITY
//
//   - PyryExited's zero is false, which reads as "did not exit" and reaches Step
//     3's void. That is the SAFE direction, and it is the identical argument
//     trailRunReadings.PyryExited makes for itself
//     (trail_run_outcome_test.go:429-432): a caller who omits the field gets a
//     void, never a finding.
//   - ClaudeState's zero is "", which is C7's shipped "not read" (:440-443,
//     :748) — an honest report rather than an unfilled field.
//   - RunnerPath's zero is "", which finGatherRunnerPath maps to the shipped
//     not-read answer before the gate sees it. The two spellings reach the SAME
//     gate decision — finRecordRunnerLabel("") matches neither runner label, so
//     both route to trailReasonPathUnnamed — so the mapping repairs no
//     classification. What it changes is publication: trailGateResult.RunnerPath
//     is `json:"runner_path,omitempty"`, so an unfilled reading would DROP the
//     field, and a reader of the artifact cannot tell a dropped field from one
//     that was never there. A caller that supplies nothing therefore publishes
//     today's record rather than a quieter one.
//   - PinnedPid's zero is the zero pinStateOutcome, carrying Verdict "" — which
//     trailSightingReasonPidReadFailed's own doc names among the shapes the
//     sighting route answers for. That argued home INSIDE the route is what
//     carries the closing sentence below over a fourth field, and it is why the
//     empty case takes no read at all rather than a pinReadState(0), which would
//     answer pinStateInstrumentFailed and so claim an instrument ran.
//
// Because all four zeros land on a named nothing-was-measured rather than on a
// claim, an incompletely-filled finGatherInputs degrades honestly.
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
	// (`pinReadState`) over the pid probeWaitForDirectChild
	// returns (`probeWaitForDirectChild`) — a CLOSED four-value set
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
	// RunnerPath is the runner-path reading, ALREADY REDUCED BY THE CALLER.
	//
	// ITS ONE ADMISSIBLE PRODUCER IS tdnRunnerFromArgv, whose five answers are
	// source-authored constants that echo no argv. NEVER finLivePinReading's
	// ClaudeCommand itself, which is verbatim argv and is marked INPUT ONLY —
	// NEVER PUBLISHED at that reading's own tier. This value crosses the gather
	// unvalidated and is republished as runner_path on trailGateResult, so raw
	// argv here would put an operator's CLAUDE_CODE_OAUTH_TOKEN or
	// ANTHROPIC_API_KEY into an artifact destined for a public issue — the same
	// channel Pinned is []int to keep shut, one field along.
	//
	// THE REDUCTION HAPPENS AT THE CALL SITE AND NEVER INSIDE THE GATHER, which
	// is Pinned's own doctrine applied to a string: a field typed to hold only
	// the reduced answer gives verbatim argv no place to land, so the channel
	// stays shut STRUCTURALLY rather than by a check this type cannot make — a
	// plain string cannot tell a label from an argv. A `ClaudeCommand string`
	// field reduced inside the gather was rejected on exactly those grounds, not
	// on style: it would put verbatim argv into this struct. The residual is
	// covered by three different fabrics — this doc, and the two rungs of
	// TestFinGatherRunnerPathCarriesNoCapturedBytes, which catch a reduction that
	// echoed its input's tail and one that echoed its head.
	//
	// "" is admissible and means THE CALLER STAGED NO READING —
	// finGatherRunnerPath maps it to the shipped not-read answer. Like
	// ClaudeState above, the gather validates nothing else, and that is a
	// statement about the gather rather than a licence for its caller. Also like
	// tdnClaudeCommand's own "", it is ambiguity about which row was claude's and
	// never a claim that the run took the other path.
	RunnerPath string
	// PinnedPid is the pinned-pid read of #1440's sighting route, TAKEN BY THE
	// CALLER AFTER PYRY EXITED.
	//
	// ITS ONE ADMISSIBLE PRODUCER IS pinReadState, over a pid from the same set
	// Pinned above carries. NEVER trailSightingPin, a FIXTURE CONSTRUCTOR whose own
	// doc says it sets no StateColumn and no ToolStderr — precisely the shape a
	// live failing read DOES produce — and never the gather's own per-matched-pid
	// pinReadState loop, whose operand is the argv scan's live matches at gather
	// time. Both differ from this route's read in the pid AND in the instant, so
	// reusing either answers a different question.
	//
	// THE SELECTION RULE LIVES HERE AND NOT ONLY AT THE CALLER, because the pinned
	// set is a SLICE and this field is ONE outcome. The caller reads the FIRST
	// entry when the set is non-empty and takes NO READ AT ALL when it is empty.
	// The first is not an arbitrary pick among differing groups: on a healthy run
	// the set carries TWO entries naming ONE detached process group, undeduped by
	// design — finLivePinReduce projects one entry per FIFO-matched row, and
	// TestFinLivePinReduce's raw-projection subtest asserts len ==
	// finLivePinWantRows with both entries equal. On a FAILED SCAN the set is nil,
	// where an unguarded index panics inside the only live caller.
	//
	// THE ZERO IS ADMISSIBLE AND IS THE HONEST REPORT for that empty case. It
	// carries Verdict "", which trailSightingReasonPidReadFailed's own doc names
	// among the shapes the route answers for, so nothing has to be invented and no
	// contract check is owed — see the no-C10 note inside trailClassifyRun. It is
	// deliberately NOT pinReadState(0), which returns pinStateInstrumentFailed: a
	// member of the closed verdict space, saying the instrument RAN AND FAILED
	// where the zero says none was there.
	//
	// IT TRAVELS WHOLE. Narrowing it to its Verdict string would leave a planted
	// needle no route to travel and make the gather-tier sweep #1459 owes
	// unbuildable — the reason trailRunReadings.PinnedPid's own doc already gives
	// for taking it whole: its Detail, StateColumn and ToolStderr ARE
	// string-bearing, and pinClassifyState's instrument-failed branch puts RAW ps
	// STDERR into ToolStderr and folds it into Detail. This value crosses the
	// gather unvalidated and NOTHING DOWNSTREAM CHECKS IT EITHER, which is why its
	// shape is the caller's obligation; the gather validates nothing here, and that
	// is a statement about the gather rather than a licence for its caller.
	PinnedPid pinStateOutcome
}

// finSighting is what the trailer poll MEASURED, handed back without handing
// back the sighting that measured it.
//
// finGatherReadings classifies against a trailObservation and then drops it (see
// there). Step 3 of the live composition — finTrailerBuild
// (finding_trailer_evidence_test.go) — needs what that sighting saw, and
// recovering it with a SECOND trailWaitForTrailer is not equivalent: by then the
// trailer is already in the buffer, the second call matches on its first poll and
// reports trailBoundFromStart, a discriminator whose own doc says it BOUNDS
// NOTHING (result_trailer_observation_test.go:80-84). The difference is a
// mis-report and not a cost. So the gather copies the measurements onto this
// value, which carries neither .Line nor the decoded pointer. #1320 moved
// finTrailerBuild onto it, and that builder is now its consumer.
//
// # What it deliberately does not carry
//
//   - NO Bounded. lateness_bounded is BoundFrom == trailBoundFromMiss AND NOTHING
//     ELSE (finding_trailer_evidence_test.go:309-315), and that stays its one
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
//     (finding_trailer_evidence_test.go:723-737).
//
// # Staleness travels as PUBLISHED EVIDENCE and is not a classifier input
//
// trailRunReadings deliberately has no field for it
// (trail_run_outcome_test.go:436-439): trailBoundFromStart carries a real duration
// that bounds nothing, so a classifier able to read a staleness could be tempted
// to discriminate on it. Carrying it HERE does not admit it THERE, and the two
// types stay separate for exactly that reason.
//
// # CarriesTrailer records the outcome of a PAIR, not a State
//
// The gather computes obs.State == trailSeen && obs.Trailer != nil; finTrailerBuild
// reads the bool it records. Once this value is forbidden the pointer, that pair is
// unrecomputable downstream, and State alone cannot serve — a trailSeen scan with a
// nil Trailer is exactly the case the pair exists to separate. The field is what
// lets a reader tell "there was no trailer" from "the trailer's fields were empty".
// No reachable sighting separates the two today, because trailWaitForTrailer fills
// Trailer on every trailSeen result; the pair is the shape that stays correct if a
// later scan learns to return one without the other.
//
// Its zero is false, which routes to the no-decoded-trailer arm where the four
// scalars are zeroes — an honest nothing-was-measured, the same argument
// finGatherInputs.PyryExited makes for itself (:415-419). An incompletely-filled
// sighting degrades to a named nothing rather than to a claim.
//
// # What the four decoded scalars are worth
//
// wireFields (emitter.go:428-437) derives Subtype, IsError and TerminalReason from
// a SINGLE ExitReason, so their agreement is one value rendered three ways and not
// three corroborating reads; TerminalReason is pyry's own synthesis and claude
// never emitted it. Only StopReason is independently sourced, forwarded from the
// model's last message unvalidated (`Emit`): it is the one
// model-influenced field crossing this value UNCAPPED, exactly as it crosses
// finTrailerRecord (finding_trailer_evidence_test.go:114-124). Capping it is out
// of scope there and here; naming it is what keeps a later sweep author from
// planting a needle in a field this value must carry verbatim.
//
// # The json keys, and the absence of omitempty
//
// Tagged because encoding/json renders a Duration as a bare nanosecond count and
// the unit belongs in the key — trailObservation:133-135 and finTrailerRecord:206
// both give that reason — and MIRRORING finTrailerRecord's keys, so #1320's move
// onto this value was a rename-free projection. NO FIELD CARRIES omitempty, for
// that record's own reason (:102-110): the discriminator must always be present
// beside the four, and dropping a false IsError or an honest zero Staleness
// collapses distinctions this type exists to keep.
//
// # The key names, and why they are not a fifth scalar
//
// KeyNames comes from a DIFFERENT READER than the four above — trailKeyNames over
// the full line (`trailKeyNames`), not resultTrailer's fixed decode —
// so it is the fifth trailer FIELD and deliberately not a fifth decoded scalar.
// Keeping it in its own group is what leaves every "the four decoded scalars"
// sentence in this family true. It carries the names ONLY, and structurally rather
// than by discipline: trailKeyNames returns []string and discards its
// map[string]json.RawMessage internally, so no VALUE can cross.
//
// It is BOUNDED HERE, by finBoundKeyNames below, because this carrier is the
// first tier either fill site reaches and the names arrive from claude's output
// rather than from pyry.
//
// No omitempty, for the blanket reason above — and the collapse that rule defends
// against is unreachable on this field anyway. trailScan's match return is past
// tr.Type == "result" and so reachable only from a line that already decoded as a
// JSON object, which means the map decode always succeeds and always carries at
// least `type` (result_trailer_observation_test.go:199-205). A SEEN trailer
// therefore cannot produce an empty name set: "no names" is reachable only from
// the not-seen arm, so the byte-identical rendering omitempty would cause cannot
// occur. trailScanResult's own tier uses omitempty (:135) and is not a precedent
// here.
type finSighting struct {
	State          string        `json:"trailer_state"`
	BoundFrom      string        `json:"lateness_bound_from"`
	Staleness      time.Duration `json:"staleness_ns"`
	CarriesTrailer bool          `json:"carries_trailer"`

	Subtype        string `json:"subtype"`
	IsError        bool   `json:"is_error"`
	TerminalReason string `json:"terminal_reason"`
	StopReason     string `json:"stop_reason"`

	KeyNames []string `json:"trailer_keys"`
}

// The two publication bounds for the trailer's key names. NAMED CONSTANTS rather
// than inline literals, because the hostile-fixture proofs and the artifact-wide
// sweeps that follow (#1364, #1362) must assert their own fixtures sit under
// them, and a fixture pinned against a literal drifts the moment the literal
// moves.
//
// # Why the count bound is 32, and why a much larger one could not be proved
//
// The ceiling is not a name count but the SCANNER'S LINE, and quoting the byte
// figure is what makes the argument shape-independent. trailScan's bufio.Scanner
// buffer is deliberately NOT raised past the 64 KiB default
// (`trailScan`), and a line at or past the limit
// ABORTS the scan rather than truncating it: KeyNames comes back nil and
// CarriesTrailer is false. Measured on this tree, 65535 bytes are accepted and
// 65536 rejected. A fixture proving a count bound bites must carry bound+1 names
// AND STILL SCAN, which at ordinary `"k000000":"v"` key shapes puts the
// unprovable floor near 4680 names — 8710 at maximally compact ones, which is a
// property of the generator rather than of the scanner and is why the byte figure
// leads. 32 sits two orders of magnitude below that floor, so #1364's 33-name
// fixture is some 400 bytes and stays readable rather than golfed against the
// scanner.
//
// 32 is also roughly three times the real shape: the trailer carries ELEVEN
// top-level keys (trailExpectedKeyNames), and they are the claude CLI's envelope
// rather than the model's text, so no producer emits more.
//
// # Why the two values are deliberately different
//
// 64 is roughly four times the longest envelope name (terminal_reason, 15 bytes),
// and it is deliberately not another 32: a fill or a proof that reaches for the
// wrong constant is then detectable, where two equal values would make a
// transposition invisible.
const (
	finTrailerMaxKeyNames     = 32
	finTrailerMaxKeyNameBytes = 64
)

// finBoundKeyNames returns names bounded for publication. Five clauses, each
// load-bearing:
//
//   - NIL for a nil or empty input, and never []string{}. A not-seen record must
//     render null rather than [], which is the distinction
//     TestFinRecordPublishesTheTrailerKeyNamesTheReaderRead asserts on the
//     marshalled bytes.
//   - AT MOST finTrailerMaxKeyNames entries, in the input's order. trailKeyNames
//     already sorted them, so the kept set is the alphabetic prefix.
//   - EACH ENTRY BOUNDED INDIVIDUALLY, NEVER AS A JOINED STRING. A joined cap
//     would let a leak in a late name be truncated away and turn a containment
//     sweep green over a record that leaked, which is the defect #1284 shipped
//     and then had to fix.
//   - AN OVER-LONG ENTRY IS TRUNCATED AND MARKED, never dropped, mirroring
//     reachCapCommand. Dropping removes
//     evidence silently; truncating announces itself. Two names sharing a
//     finTrailerMaxKeyNameBytes-byte prefix therefore collapse to one string —
//     both carrying the marker, so the duplication is visibly an artefact.
//   - IT ALLOCATES ITS OWN BACKING ARRAY ON EVERY PATH, INCLUDING WHEN THE INPUT
//     IS ALREADY UNDER BOTH BOUNDS. An `if there is nothing to do, return names`
//     fast path looks free and is not: finTrailerBuild copies this field by plain
//     slice assignment, so a pass-through would make the published
//     finTrailerRecord.KeyNames alias trailScanResult.KeyNames itself, and the
//     shipped `dropped := seen` struct copy
//     (`TestFinTrailerRecordFillsTheFourScalarsOnlyBehindCarriesTrailer`) would then put two carriers on one
//     backing array while `go test -race` runs this package's tests in parallel
//     (`trailRunWellFormed`). The builder's plain assignment is safe
//     ONLY because this clause holds, which is why the clause is stated at the
//     producer rather than at the consumer.
//
// Pure over its input and with no error return, matching this family's builder
// contract: it never fails a test.
func finBoundKeyNames(names []string) []string {
	if len(names) == 0 {
		return nil
	}
	kept := len(names)
	if kept > finTrailerMaxKeyNames {
		kept = finTrailerMaxKeyNames
	}
	bounded := make([]string, 0, kept)
	for _, name := range names[:kept] {
		if len(name) > finTrailerMaxKeyNameBytes {
			name = name[:finTrailerMaxKeyNameBytes] + reachTruncationMarker
		}
		bounded = append(bounded, name)
	}
	return bounded
}

// finGatherRunnerPath normalises an unstaged runner-path reading to the shipped
// "not read" answer, so an unfilled field publishes what it means rather than
// nothing.
//
// Total, and INFORMATION-PRESERVING: trailRunnerUnread() IS tdnRunnerFromArgv("")
// — the shipped reader's own answer to an unread argv — so the two spellings of
// "nothing was read" collapse onto the one the record can publish. It re-types
// neither that answer nor its prose.
//
// # It is a PUBLICATION fix and not a classification repair
//
// This is deliberately NOT the "no default, no zero-value rewrite, no
// fill-in-if-unset" rule finGatherReadings states over the caller's other two
// readings. That rule exists because C7 is a SHIPPED CLASSIFIER CHECK over the
// claude verdict, so a second opinion in the gather would repair the exact record
// C7 exists to reject. There is no analogous checker here:
// finRecordRunnerLabel("") returns "", which matches neither runner label, so ""
// and trailRunnerUnread() reach the same trailReasonPathUnnamed answer and the
// same gate value. The normalisation therefore changes NO DECISION. What it
// changes is whether runner_path survives into the marshalled record, because
// trailGateResult.RunnerPath carries omitempty — see that field's own doc.
//
// # Why the gather owns it and not the gate
//
// trailGateResult.RunnerPath is "the reading the gate was HANDED, copied onto the
// result", and TestTrailGateReadsTheRunnerPathOnlyWhereARowDeclaresIt is keyed on
// the gate copying its input through unchanged. Normalising there would make the
// gate lie about what it was handed. Telling "unfilled" from "read and
// indeterminate" is the composer's call, so it is made here.
//
// ONE SYMBOL, TWO CALLERS. finGatherReadings and finGatherAssertContract's C2
// recomputation both route through this function and neither carries its own copy
// of the rule — finBoundKeyNames' reason, one tier along: two hand-written copies
// of a fill required to agree is how two fills stop agreeing.
func finGatherRunnerPath(reading string) string {
	if reading == "" {
		return trailRunnerUnread()
	}
	return reading
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
// (result_trailer_observation_test.go:141-142), so handing it back would promote
// .Line and .Trailer straight into the caller's reach — and both withholdings
// stand. .Line is verbatim model output marked OPERATOR-REVIEW-BEFORE-PASTE,
// roughly 415 of its retained 512 bytes being the trailer's `result` field, which
// is the assistant's last message. .Trailer is a *resultTrailer, which carries
// PermissionDenials *[]json.RawMessage (`resultTrailer`) — raw bytes no cap
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
// still has no field for it (trail_run_outcome_test.go:436-439) and why the two
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
//   - RunnerPath is a REDUCED reading. A caller holding claude's argv reduces it
//     with tdnRunnerFromArgv at its own call site, for the identical reason and
//     with the identical failure mode — see that field's doc. Never
//     reachRunnerPathFromArgv, which keys on --append-system-prompt-file and so
//     labels a correctly-wired stream run ptyrunner.
//   - PinnedPid is READ AFTER PYRY EXITED, an instant this function does not
//     observe and cannot wait for, so the timing is the caller's obligation and
//     not a check. Its READ IS TAKEN AT THE CALL SITE for that reason and never
//     here: the per-matched-pid loop below reads the argv scan's own live matches
//     at gather time, and neither its operand nor its instant is this route's. A
//     second pinReadState call inside this function over in.Pinned would answer at
//     the instant this one observes rather than at the one the route is about.
//
// # Which contract check shapes which leg
//
//   - C2 (`trailClassifyRun`) requires a trailGateUsable value to carry
//     a non-empty Reason. The gate is fed a real scanned trailer, so the reason
//     arrives filled; a hand-built trailGateResult{Value: trailGateUsable} is
//     exactly the fixture C2 exists to reject.
//   - C3/C4 (:694, :706) make the Admit guard MANDATORY rather than defensive.
//     Calling the predicate unconditionally fails C4 on every non-certifying gate;
//     skipping it unconditionally fails C3 on every certifying one.
//   - C8/C9 (:755, :767) require the counts and the errored flag to be one
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
	// The runner path is THE CALLER'S READING, normalised for publication only.
	// It does not come from this gather's scan and could not: the needles are the
	// FIFO path alone, and the needle prohibition in finExitRunProbe stands —
	// this gather has no finLivePinReduce, so claude's own row would land in the
	// classifier's match-count arms and in the published liveness list. The
	// reading does not have to come from here, because the caller already holds
	// one: finLivePinReduce fills Pin.ClaudeCommand from the driver's own
	// two-needle scan during the turn, and the probe reduces it at its own call
	// site. So the gate's absence arm is no longer pinned to its path-unnamed
	// case, and trailGateAbsentOwesNone becomes reachable from a live stream run.
	readings.Gate = trailGate(trailGateInput{Scan: obs.trailScanResult,
		RunnerPath: finGatherRunnerPath(in.RunnerPath)})

	// What that sighting MEASURED, copied out of the same obs that just filled
	// BoundFrom and fed the gate: one poll, no second scan, no second wait and no
	// clock reading of this function's own — trailWaitForTrailer stamped both
	// durations itself (result_trailer_observation_test.go:281-287).
	//
	// Inline rather than behind a finSightingFrom constructor, for the reason
	// stated above: funnelling the whole composition through one function is what
	// makes it checkable in one place, and a constructor would add a symbol whose
	// tests either duplicate this one's or do not exist.
	sighting = finSighting{State: obs.State, BoundFrom: obs.BoundFrom, Staleness: obs.Staleness}
	// The State operand goes FIRST and Go's && short-circuits left to right, so
	// the ordering is a property of the source rather than of this comment. It is
	// deliberately identical to finTrailerSighting's fill on the fixture side:
	// finTrailerBuild computes nothing now, and the two computations must agree.
	//
	// THE KEY-NAME FILL IS UNDER THAT SAME OBLIGATION, and its asymmetry runs the
	// dangerous way: every offline test drives the FIXTURE side, so bounding there
	// and forgetting here would ship an unbounded field on every LIVE probe run
	// with nothing red. No in-file pin is available for it — this function reaches
	// ps through pinScanArgv and pinReadState, and the evidence file forbids exec
	// by its own header — so the obligation is held here and by review, exactly as
	// the pair's is. Both sides route through finBoundKeyNames and neither carries
	// its own copy of the arithmetic, because two hand-written copies of a cap is
	// how two fills required to agree stop agreeing.
	sighting.CarriesTrailer = obs.State == trailSeen && obs.Trailer != nil
	if sighting.CarriesTrailer {
		sighting.Subtype = obs.Trailer.Subtype
		sighting.IsError = obs.Trailer.IsError
		sighting.TerminalReason = obs.Trailer.TerminalReason
		sighting.StopReason = obs.Trailer.StopReason
		// trailObservation EMBEDS trailScanResult
		// (result_trailer_observation_test.go:141-142), so the names the scan
		// already read are in reach here with no new plumbing.
		sighting.KeyNames = finBoundKeyNames(obs.KeyNames)
	}

	// The attribution leg, guarded on the gate's certified Reason: the exact
	// condition C3 and C4 split on, and the one trailAdmitAttribution's own
	// contract block (`trailGate`) rejects the negation of.
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

	// The caller's own three readings, carried WHOLE. No default, no zero-value
	// rewrite, no fill-in-if-unset, no normalisation and no pinIsVerdict call: C7
	// is the classifier's check over the claude verdict and it is already
	// shipped, so a second opinion here would repair exactly the record C7 exists
	// to reject and put trailOutcomeOutOfContract out of reach through this
	// composition.
	//
	// THE PIN'S CASE IS THE SAME RULE FROM THE OPPOSITE DIRECTION and is the
	// stronger of the two. There is NO contract check over PinnedPid anywhere —
	// the no-C10 note inside trailClassifyRun is why — so a repair here would be
	// caught by no downstream check at all, and its zero already has an argued home
	// inside the route rather than needing one built. It lands on PinnedPid and
	// NEVER on Liveness, which is the argv scan's own per-pid set: appending it
	// there is type-correct, since pinReadState returns exactly this type, and
	// silently wrong, since Liveness feeds tdnVerdictSummary into the published
	// record and Step 6's instrument-failure void.
	// TestFinGatherPinnedPidDoesNotReachTheLiveness is the deterministic net.
	//
	// What honest readings buy is ONE class of run: with a usable gate and an
	// attribution that is not proof, PyryExited false now reaches Step 3's
	// staging void where the constant reported a scan-side answer or a Step 4-6
	// void. A proof-carrying run is unchanged — Step 2 sits above Step 3 by
	// design (`trailClassifyRun`) and proofPyryLive pins it
	// (:1336-1337, :1564-1565) — and so is a run whose gate is not usable, which Step
	// 1 answers first.
	readings.PyryExited = in.PyryExited
	readings.ClaudeState = in.ClaudeState
	readings.PinnedPid = in.PinnedPid

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
// reason (`trailRunWellFormed`): the rows carry slices, and a shared
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

			// Held in a variable rather than built inline at the call, so C2's
			// recomputation reads the RunnerPath THIS CALL was handed instead of
			// a second copy the two sides could drift apart on.
			in := finGatherInputs{
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
				// shipped "not read". RunnerPath omitted for the same reason on
				// every row of this table, which is what makes these three the
				// rows that pin today's published record: the gate still reads
				// the not-read answer, and the gate answers a runner-path table
				// of its own below.
			}
			readings, record, _ := finGatherReadings(in)
			finGatherAssertContract(t, tc, in, seed, readings, record)

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
func finGatherAssertContract(t *testing.T, tc finGatherCase, in finGatherInputs, seed []byte,
	readings trailRunReadings, record finAttributeRecord) {
	t.Helper()

	// C2 (`trailClassifyRun`): the gate is fed a REAL SCANNED TRAILER.
	// Byte for byte against the shipped producers over the same bytes the gather
	// read — trailGateResult is four strings, so == suffices. A hand-built
	// trailGateResult{Value: trailGateUsable} is exactly the fixture C2 exists to
	// reject, and this equality is what rules it out.
	//
	// The fourth string is the carried runner path, and since #1452 both sides go
	// through finGatherRunnerPath OVER THE READING THIS CALL WAS HANDED rather
	// than through a reading typed in here.
	//
	// WHAT THAT DOES AND DOES NOT BUY, measured under `go test -overlay` rather
	// than asserted. It DOES make this equality the red for a gather that dropped
	// the normalisation and passed in.RunnerPath through raw: all three rows then
	// publish "" against a recomputation that publishes trailRunnerUnread(). It
	// does NOT discriminate a gather that ignores in.RunnerPath entirely, because
	// every row of this table stages no reading, so the two spellings agree — that
	// mis-implementation is TestFinGatherGateReadsTheRunnerPathTheRunTook's to
	// redden, and it does. Reading the row's own input rather than a constant is
	// what keeps this check honest the day a row here does stage one.
	if want := trailGate(trailGateInput{Scan: trailScan(seed),
		RunnerPath: finGatherRunnerPath(in.RunnerPath)}); readings.Gate != want {
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
		// C3 (:694): the attribution predicate is called EXACTLY WHEN the gate
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
		// C4 (:706): Admit is left ZERO when the gate certified nothing. This is
		// the zero-value comparison the package uses for "not classified"
		// (`trailRunCases`, `TestFinAttributeEmptySetAlternativesArePublishedFalsehoods`) —
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
	// unproducible contract arm (trailer_admissibility_test.go:375-381).
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
	// anywhere in this file, so every row runs at zero matches and the finding
	// row STILL reports the finding: Step 2 (`trailClassifyRun`) is
	// consulted before Step 7 and Step 8 are. On a live run the same thing happens
	// for a different reason — the reaper has already killed the group by the time
	// the post-trailer scan runs — which is why nothing may be done to the hold or
	// the scan timing to "make the match survive".
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
// (`trailClassifyRun`, "Voiding it for a staging failure would
// SUPPRESS A FINDING THE RUN GENUINELY ESTABLISHED") and is pinned by
// proofPyryLive, a proof carrying PyryExited false that wants
// trailOutcomeRunningAtTrailer (:1371-1372, :1602-1603). Building this pair on the
// proof arm would require reordering the classifier, which is a defect and not
// a fix.
//
// # The two arms are identical BY CONSTRUCTION, not by discipline
//
// The live arm is a COPY of the exited arm's finGatherInputs with one field set,
// so the stdout buffer, the needle slice, the stderr and the pinned set are the
// same values rather than equal-looking rebuilds. Sharing one buffer is sound:
// trailWaitForTrailer only ever calls stdout.Bytes()
// (`trailWaitForTrailer`), which returns a COPY of an
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
// finGatherAssertContract runs on every row of that table and its free
// corroboration fails any row classifying trailOutcomeOutOfContract, which is
// precisely the answer the middle row requires. The claim needs a test of its own.
//
// # Why the gather validates nothing
//
// C7 (trail_run_outcome_test.go:748-753) is the classifier's check over this
// field and it is already shipped. A pinIsVerdict call in the gather would
// repair exactly the record C7 exists to reject, putting the out-of-contract
// answer out of reach through this composition — and it would break the LAST row
// first, because "" is C7's shipped "not read" and is not a pinIsVerdict value.
// A caller with no claude to read must be able to say so.
//
// The first and last rows want the SAME outcome, and that is the point: the
// verdict is corroboration, and corroboration never moves the answer
// (`trailClassifyRun`).
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
			// republishes the field verbatim (`trailClassifyRun`).
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
// The needle goes into two of the three inputs that could carry captured bytes
// into the returns: the reap-log stderr, and the stdout buffer at the trailer's
// `result` field, which is what trailNeedle already stands in for. IT MAY NOT GO INTO
// terminal_reason: trailGate and trailAdmitAttribution both quote the certified
// reason %q into their Details BY DESIGN (`trailDetail`,
// :399, :534, :598), and readings.Gate and readings.Admit take those results
// whole — so such a plant would be red against shipped code whose only fix
// deletes the field the operator reads to interpret the gate. The exclusion is
// stated here and made structural by finGatherNeedleTrailer, which renders
// terminal_reason "completed".
//
// THE THIRD INPUT IS #1458's PinnedPid, AND NO SWEEP COVERS IT YET. Its one
// admissible producer is pinReadState, whose instrument-failed branch puts raw ps
// stderr into ToolStderr and folds it into Detail, and the value crosses this
// gather WHOLE onto a return of this very function — so the channel opens at
// exactly the tier this test is about. #1459 owes that sweep and re-points this
// note at it when it lands. NO THIRD PLANT IS ADDED HERE: #1452's precedent is
// that a new route gets a sibling test of its own
// (TestFinGatherRunnerPathCarriesNoCapturedBytes) rather than a third plant on
// this one. Said in the note rather than left for the next reader to notice,
// because the sentence above would otherwise read as coverage of everything that
// could carry captured bytes into the returns.
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
		// corroboration (`trailClassifyRun`).
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

// --- the runner-path reading --------------------------------------------------------

// finGatherRunnerPathCase is one runner-path reading, the trailer shape it is
// read against, and the gate answer that pairing earns.
type finGatherRunnerPathCase struct {
	name string
	// reading is what the CALLER supplies. Every non-empty one is
	// tdnRunnerFromArgv's OWN OUTPUT over a shipped argv fixture and never a
	// re-typed copy of its prose, so a reword of that reader reddens these rows
	// rather than leaving a stale literal passing.
	reading string
	// seed is the stdout line the trailer leg reads.
	seed string
	// wantValue is the gate value the pairing must reach, and wantReason the
	// terminal reason it must certify — non-empty on the usable row alone.
	wantValue  string
	wantReason string
	// marker is the absence case the Detail must name, or "" for a row whose
	// trailer carries a terminal_reason and so decides no absence case at all.
	// It is THE DISCRIMINATOR on the two rows sharing trailGateOutOfContract,
	// where the value alone separates nothing.
	marker string
	// why is what a failure prints, so a red row says what it was worth.
	why string
}

// finGatherRunnerPathCases returns the four reading-dependent answers the gate
// has, each paired with the reading that earns it.
//
// A FUNCTION rather than a package-level var, for trailRunWellFormed's stated
// reason (`trailRunWellFormed`).
//
// A NEW TABLE rather than three more rows on finGatherCases(), whose cardinality
// and ordinal claims are load-bearing at several sites, and rather than on
// trailGateCases(), whose nine rows and "row nine" ordinal are load-bearing at
// more. This table's cardinality is fresh and has no claims to sweep.
func finGatherRunnerPathCases() []finGatherRunnerPathCase {
	return []finGatherRunnerPathCase{
		{
			name:      "a stream run's own trailer, read under the reading that run took",
			reading:   tdnRunnerFromArgv(tdnFixtureStreamArgv),
			seed:      trailKeyNamesNoTerminalReason(),
			wantValue: trailGateAbsentOwesNone,
			marker:    trailReasonAbsentOwesNone,
			why: "this is the value the ticket exists to make reachable: on the headless " +
				"PYRY_USE_STREAMJSON=1 path a healthy trailer is claude's own result line and " +
				"carries no terminal_reason by construction, and it is the sole entry to the " +
				"evidence route built for a path that writes no reap log on a clean exit",
		},
		{
			name:      "the same trailer under a ptyrunner reading, which owes one",
			reading:   tdnRunnerFromArgv(tdnFixturePtyArgv),
			seed:      trailKeyNamesNoTerminalReason(),
			wantValue: trailGateOutOfContract,
			marker:    trailReasonAbsentOwesOne,
			why: "absence where a reason is OWED is a departure from what that path " +
				"constructs. It shares its value with the row below, so the case marker is " +
				"what says which of the two sites answered",
		},
		{
			name:      "the same trailer under no reading at all, which is today's answer for every run",
			reading:   "",
			seed:      trailKeyNamesNoTerminalReason(),
			wantValue: trailGateOutOfContract,
			marker:    trailReasonPathUnnamed,
			why: "a caller that supplies nothing still publishes today's record: " +
				"finGatherRunnerPath maps \"\" to the shipped not-read answer, so the gate " +
				"reaches the path-unnamed site and runner_path survives omitempty",
		},
		{
			name:      "a named terminal_reason under a ptyrunner reading stays usable",
			reading:   tdnRunnerFromArgv(tdnFixturePtyArgv),
			seed:      trailFixtureTrailer,
			wantValue: trailGateUsable,
			// trailFixtureTrailer's own terminal_reason, the same one
			// finGatherCases()' two certifying rows want.
			wantReason: "completed",
			why: "this row is what keeps #1337's recorded ptyrunner finding and the shipped " +
				"ptyrunner probe standing: where a terminal_reason is present and NAMED, a " +
				"ptyrunner reading certifies exactly what trailRunnerUnread() certified before",
		},
	}
}

// TestFinGatherGateReadsTheRunnerPathTheRunTook is this ticket's actual claim:
// each of the gate's four reading-dependent answers is reached THROUGH THE
// GATHER by the reading that earns it, driven offline from fixtures.
//
// Every row compares the WHOLE trailGateResult against the shipped gate's own
// output over that row's own reading, and never against a typed-in value — the
// file's C2 idiom, one composition up.
//
// # What each row is the sole red for, and why the value alone is not enough
//
//   - R2 AND R3 SHARE trailGateOutOfContract, so a check on the value alone
//     discriminates nothing between them. The absence case marker is the
//     discriminator, and trailGateAbsenceCaseMarkers()' three are pairwise
//     non-containing (asserted at TestTrailGateNamesWhichAbsenceCaseFired), so a
//     swap of the two arms is red rather than invisible.
//   - R3 IS THIS TABLE'S ONLY RED FOR A MISSING NORMALISATION. A gather passing
//     in.RunnerPath through raw produces RunnerPath "" while the recomputation
//     produces trailRunnerUnread(); trailGateResult is four strings compared with
//     ==, so R3 goes red and R1/R2/R4 stay green. The published-field check below
//     is the same claim read off the marshalled bytes, where omitempty is what
//     would silently drop it.
//   - R1, R2 AND R4 ARE THIS TABLE'S REDS FOR A GATHER THAT IGNORED in.RunnerPath
//     and went on typing trailRunnerUnread() in — and R3 is not, since under that
//     mutant it is handed the value it already wanted. R1 falls to
//     trailGateOutOfContract and R2 to the path-unnamed site; R4 keeps its value
//     AND its certified reason and is caught by the whole-struct equality alone,
//     which is why that equality is stated first and not as a corroboration.
//
// Each of the four claims above was verified under `go test -overlay` against a
// mutated tree rather than argued.
//
// # The Detail budget is checked on the OUTPUT rather than argued
//
// trailDetail caps at reachMaxCommandBytes and reachCapCommand TRUNCATES AND
// MARKS rather than failing, so an overlong Detail ships a sentence severed past
// its own marker while still satisfying the marker assertion above. #1417
// measured the three absence Details at 461 / 464 / 468 B and the presence Detail
// at 427 B, all under trailRunnerUnread() — and those measurements stay valid
// only because NO Detail interpolates the reading. R1/R2/R3 drive the three
// absence Details under three DIFFERENT readings, so the check costs one line per
// row and is the deterministic net for that claim: an edit that interpolated the
// reading would make the budget a function of the reading and these rows would go
// red rather than shipping a cut sentence.
func TestFinGatherGateReadsTheRunnerPathTheRunTook(t *testing.T) {
	for _, tc := range finGatherRunnerPathCases() {
		t.Run(tc.name, func(t *testing.T) {
			var stdout probeSyncBuffer
			seed := finGatherSeed(t, &stdout, tc.seed)

			// No reap log and no pin: three of the four rows certify nothing, so
			// the attribution leg does not run on them at all, and the fourth's is
			// the shipped table's business rather than this row's.
			readings, _, _ := finGatherReadings(finGatherInputs{
				Stdout:     &stdout,
				Needles:    finGatherNeedles(t),
				PyryExited: true,
				RunnerPath: tc.reading,
			})

			// The whole result against the SHIPPED GATE'S own output over the same
			// bytes and THIS ROW'S OWN READING. trailGateResult is four strings, so
			// == suffices, and the fourth is the reading itself.
			want := trailGate(trailGateInput{Scan: trailScan(seed),
				RunnerPath: finGatherRunnerPath(tc.reading)})
			if readings.Gate != want {
				t.Fatalf("the gate reads %+v; want %+v — the gather must hand trailGate the reading "+
					"its caller staged, normalised only for publication, and never a reading it "+
					"typed in itself", readings.Gate, want)
			}

			if readings.Gate.Value != tc.wantValue || readings.Gate.Reason != tc.wantReason {
				t.Fatalf("the gate reads %q certifying %q; want %q certifying %q. %s. Detail: %s",
					readings.Gate.Value, readings.Gate.Reason, tc.wantValue, tc.wantReason,
					tc.why, readings.Gate.Detail)
			}

			// The published reading is a NAMED answer on every row, including the
			// one whose caller staged nothing. runner_path carries omitempty, so an
			// unfilled reading would route to the same value while dropping the
			// field, and a reader of the artifact cannot tell a dropped field from
			// one that was never there.
			if readings.Gate.RunnerPath == "" {
				t.Fatalf("the gate publishes an EMPTY runner_path although the row staged %q — no "+
					"shipped producer emits \"\": tdnRunnerFromArgv answers non-empty on every "+
					"branch, including for an empty argv", tc.reading)
			}
			encoded, err := json.Marshal(readings.Gate)
			if err != nil {
				t.Fatalf("marshalling the gate result: %v", err)
			}
			if !bytes.Contains(encoded, []byte(`"runner_path"`)) {
				t.Errorf("the marshalled gate result drops runner_path: %s — omitempty removes the "+
					"field for an unfilled reading, which is the distinction finGatherRunnerPath "+
					"exists to keep", encoded)
			}

			var named []string
			for _, m := range trailGateAbsenceCaseMarkers() {
				if strings.Contains(readings.Gate.Detail, m) {
					named = append(named, m)
				}
			}
			switch {
			case tc.marker == "" && len(named) != 0:
				t.Errorf("the Detail names absence case(s) %q, want none of the three: this row's "+
					"trailer carries a terminal_reason, so no absence case applies to it. Detail: %s",
					named, readings.Gate.Detail)
			case tc.marker != "" && (len(named) != 1 || named[0] != tc.marker):
				t.Errorf("the Detail names absence case(s) %q, want exactly [%s] under reading %q. "+
					"%s. Detail: %s", named, tc.marker, tc.reading, tc.why, readings.Gate.Detail)
			}

			if len(readings.Gate.Detail) >= reachMaxCommandBytes ||
				strings.Contains(readings.Gate.Detail, reachTruncationMarker) {
				t.Errorf("the gate's Detail is %d bytes against the %d-byte cap and truncated=%t — "+
					"reachCapCommand TRUNCATES AND MARKS rather than failing, so a Detail at the cap "+
					"ships a sentence severed PAST its own case marker while the marker assertion "+
					"above still passes", len(readings.Gate.Detail), reachMaxCommandBytes,
					strings.Contains(readings.Gate.Detail, reachTruncationMarker))
			}
		})
	}
}

// TestFinGatherRunnerPathDoesNotReachTheScan pins that the reading reaches the
// GATE and nothing else: the argv leg's population is invariant across readings.
//
// The rows are byte-identical in stdout seed, needles, stderr and pinned set —
// the same values rather than equal-looking rebuilds, since finGatherNeedles
// calls t.TempDir() and would vary a second dimension — and differ in RunnerPath
// alone.
//
// # The needle is a t.TempDir() path, and the fourth row is the instrument
//
// finGatherNeedles returns a path under which nothing is ever staged, so
// MatchCount is 0 and Liveness empty DETERMINISTICALLY. Three rows carry the
// ordinary readings; the FOURTH carries a deliberately abusive one, os.Args[0],
// which IS a matching needle. A gather routing the reading into its needle set
// would take that row's MatchCount from 0 to at least 1 and put this very process
// into the published liveness list. Under the correct gather it stays 0.
//
// # The control is what stops the negative being vacuous
//
// MatchCount == 0 on the abusive row proves nothing unless os.Args[0] is
// demonstrably a needle that matches. So the test runs pinScanArgv over it ITSELF
// and requires this process among the matches — the shipped self-matching-needle
// idiom of TestPinScanArgv_ExcludesTheInstrumentsOwnProcess, membership and never
// a count, for that test's own measured reason.
//
// # Why NOT the obvious shape, which is flakier AND weaker
//
// Making os.Args[0] the GATHER'S needle and comparing MatchCount across rows is
// unsound here: this package makes many t.Parallel() calls and re-execs itself in
// several places, those children carry os.Args[0] as their own argv[0], and
// reachMatchArgvRows matches a needle as a substring of the FULL command line. A
// sibling's child appearing or exiting between two of the four gather calls moves
// the count, so a cross-row equality would be testing the harness. os.Args[0] is
// read here as a STRING USED AS A CONTENT NEEDLE and never as a process to start:
// this file adds no os/exec import and calls no exec.Command, exec.CommandContext,
// exec.LookPath, os.StartProcess or syscall.Exec, which the gather's own body
// states as an obligation.
//
// # What this test does NOT kill
//
// It goes red against any design that lets the reading reach in.Needles. It does
// NOT kill "the gather appends tdnClaudeNeedle", because offline no claude process
// exists for that needle to match. That clause is discharged STRUCTURALLY — the
// field is a reduced string, never a needle, and the gather still calls
// pinScanArgv(in.Needles, nil) with in.Needles verbatim — and it is said here so a
// later reader does not over-trust the green.
func TestFinGatherRunnerPathDoesNotReachTheScan(t *testing.T) {
	self := os.Args[0]
	if self == "" {
		t.Fatalf("os.Args[0] is empty; this test needs the test binary's own path as the abusive " +
			"reading, and without it the fourth row asserts nothing")
	}

	control, err := pinScanArgv([]string{self}, nil)
	if err != nil {
		t.Fatalf("the control scan over os.Args[0]: %v", err)
	}
	if control.RowsScanned == 0 {
		t.Fatalf("the control scan reported no error and parsed 0 row(s); the live `ps` read is "+
			"not wired up, so the abusive row's zero would mean nothing matches anything here "+
			"rather than that the gather kept the reading out of its scan (matched %d)",
			control.MatchCount)
	}
	if !pinScanHasPID(control.Matches, os.Getpid()) {
		t.Fatalf("own pid %d is not among the %d row(s) matching os.Args[0]; that reading is not a "+
			"needle that would have matched, so the abusive row below is vacuous",
			os.Getpid(), control.MatchCount)
	}

	// ONE buffer, ONE needle slice, ONE stderr and ONE pinned set across the four
	// rows. Sharing the buffer is sound: trailWaitForTrailer only ever calls
	// Bytes(), which returns a COPY of an append-only buffer, so every row observes
	// the same bytes on its first poll.
	var stdout probeSyncBuffer
	finGatherSeed(t, &stdout, trailFixtureTrailer)
	base := finGatherInputs{
		Stdout:     &stdout,
		Needles:    finGatherNeedles(t),
		Stderr:     []byte(trailReapLine(1, fmt.Sprintf("[%d]", finGatherNamedPGID)) + "\n"),
		Pinned:     []int{finGatherNamedPGID},
		PyryExited: true,
	}

	for _, tc := range []struct {
		name    string
		reading string
	}{
		{name: "a reading reducing to streamrunner", reading: tdnRunnerFromArgv(tdnFixtureStreamArgv)},
		{name: "a reading reducing to ptyrunner", reading: tdnRunnerFromArgv(tdnFixturePtyArgv)},
		{name: "no reading at all", reading: ""},
		{name: "a reading that IS a matching needle", reading: self},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := base
			in.RunnerPath = tc.reading
			readings, _, _ := finGatherReadings(in)

			if readings.ArgvScanErrored {
				t.Fatalf("the argv scan is recorded as errored; want a clean scan — with a broken " +
					"scan the counts below are the zero pinScan's and this row asserts nothing")
			}
			if readings.RowsScanned <= 0 {
				t.Fatalf("the argv scan parsed %d well-formed row(s); want more than 0 — at zero "+
					"rows a zero match count is a nothing-was-measured rather than a statement "+
					"about the needle set", readings.RowsScanned)
			}
			if readings.MatchCount != 0 {
				t.Errorf("the argv scan matched %d of %d row(s) although the needle set is one "+
					"t.TempDir() path nothing is staged at; want 0 — a non-zero count here is the "+
					"reading having reached in.Needles, which would put this process into the "+
					"classifier's match-count arms", readings.MatchCount, readings.RowsScanned)
			}
			if len(readings.Liveness) != 0 {
				t.Errorf("the readings carry %d per-pid read(s); want none — Liveness holds one read "+
					"per MATCHED pid, so a non-empty list is the same defect read off the published "+
					"liveness list instead of off the count", len(readings.Liveness))
			}
		})
	}
}

// finGatherArgvHead is the leading substring BOTH shipped argv fixtures carry
// (tdnFixturePtyArgv and tdnFixtureStreamArgv) and which appears in none of
// tdnRunnerFromArgv's five constant answers.
//
// It is the SECOND FABRIC of the sweep below: the needle rung catches a reduction
// that echoed the TAIL it was handed, this one catches a reduction that echoed the
// HEAD, and it fires even against a future caller who passes raw argv carrying no
// planted needle at all. The sweep asserts its presence in the argv it plants
// rather than trusting this sentence.
const finGatherArgvHead = "/opt/node/bin/node"

// TestFinGatherRunnerPathCarriesNoCapturedBytes sweeps the route this ticket
// opens: runner_path IS marshalled, and every shipped sweep over it has run under
// trailRunnerUnread() — the EMPTY-argv answer — so none has yet run it under a
// reading derived from a real argv.
//
// Five rungs, in order, of which the middle three are what stop the negative
// being vacuous:
//
//  1. THE PLANT. The reading is tdnRunnerFromArgv's own output over an argv
//     carrying trailNeedle, handed to the gather as RunnerPath.
//  2. THE ROUTE IS REAL. The marshalled readings carry the literal runner_path
//     and readings.Gate.RunnerPath is the reading handed in. A sweep over a field
//     omitempty dropped measures nothing.
//  3. THE NEEDLE WOULD HAVE TRAVELLED. The planted argv genuinely carries it, so
//     had the reduction echoed its input the needle would be in RunnerPath, and by
//     rung 2 RunnerPath is in the bytes.
//  4. THE NEGATIVE, over all three returns and over both published Details.
//  5. THE SECOND FABRIC, against argv generally rather than against the plant.
//
// The stdout seed is trailFixtureTrailer, which carries NO needle, and the reap
// line carries none either — so the reading is the only route by which trailNeedle
// could reach any of the three returns, and a hit names its own cause.
//
// # The plant rides the PTYRUNNER argv, and that is what widens the sweep
//
// trailFixtureTrailer carries a NAMED terminal_reason, so under a streamrunner
// reading the gate answers trailGatePresentOwesNone and certifies nothing — the
// attribution leg then never runs and readings.Admit stays zero, which would make
// the Admit half of rung 4 a sweep over an empty string. Under a ptyrunner reading
// the same fixture reaches trailGateUsable certifying "completed", the fan-out
// runs, and the sweep covers the PROOF arm, which is the arm that quotes the most.
// Both argv fixtures carry the same head, so rung 5 is unaffected by the choice.
func TestFinGatherRunnerPathCarriesNoCapturedBytes(t *testing.T) {
	argvWithNeedle := tdnFixturePtyArgv + " " + trailNeedle
	reading := tdnRunnerFromArgv(argvWithNeedle)

	var stdout probeSyncBuffer
	finGatherSeed(t, &stdout, trailFixtureTrailer)
	readings, record, sighting := finGatherReadings(finGatherInputs{
		Stdout:     &stdout,
		Needles:    finGatherNeedles(t),
		Stderr:     []byte(trailReapLine(1, fmt.Sprintf("[%d]", finGatherNamedPGID)) + "\n"),
		Pinned:     []int{finGatherNamedPGID},
		PyryExited: true,
		RunnerPath: reading,
	})

	// --- rung 3, stated first because it is a property of the plant -------------

	if !strings.Contains(argvWithNeedle, trailNeedle) {
		t.Fatalf("the planted argv does not carry the needle, so a reduction that echoed its " +
			"input would leak nothing and every negative below is vacuous")
	}
	if !strings.Contains(argvWithNeedle, finGatherArgvHead) {
		t.Fatalf("the planted argv does not carry %q, so the head rung below asserts nothing — "+
			"both shipped argv fixtures start with it and that is what makes it a second fabric",
			finGatherArgvHead)
	}

	// --- the premises: the two arms that quote the most genuinely ran -----------

	if readings.Gate.Value != trailGateUsable || readings.Gate.Reason != "completed" {
		t.Fatalf("the gate reads %q certifying %q; want %q certifying \"completed\" — the seed is "+
			"an ORDINARY trailer, and at any other value the attribution leg does not run and the "+
			"Admit sweep below is over a zero value", readings.Gate.Value, readings.Gate.Reason,
			trailGateUsable)
	}
	if readings.Admit.Value != trailAdmitProof {
		t.Fatalf("the attribution reads %q; want %s — the proof arm is the arm that quotes the "+
			"most, so a sweep over its Detail on any other arm is weaker than it claims",
			readings.Admit.Value, trailAdmitProof)
	}

	encoded, err := json.Marshal(readings)
	if err != nil {
		t.Fatalf("marshalling the readings: %v", err)
	}

	// --- rung 2: the route is real ---------------------------------------------

	if !bytes.Contains(encoded, []byte(`"runner_path"`)) {
		t.Fatalf("the marshalled readings carry no runner_path key, so the sweep below runs over a "+
			"field omitempty dropped and measures nothing: %s", encoded)
	}
	if readings.Gate.RunnerPath != reading {
		t.Fatalf("the published reading is %q; want the %q that was handed in — the sweep is about "+
			"what THIS reading put into the record, and a different one there makes it about "+
			"something else", readings.Gate.RunnerPath, reading)
	}

	// --- rung 4: the negative, over all three returns --------------------------

	for _, subject := range []struct {
		what  string
		value any
	}{
		{"readings", readings},
		{"attribution record", record},
		{"sighting", sighting},
	} {
		blob, err := json.Marshal(subject.value)
		if err != nil {
			t.Fatalf("marshalling the %s: %v", subject.what, err)
		}
		if bytes.Contains(blob, []byte(trailNeedle)) {
			t.Errorf("the marshalled %s carries captured bytes from the argv the reading was "+
				"derived from: %s", subject.what, blob)
		}
		var decoded any
		if err := json.Unmarshal(blob, &decoded); err != nil {
			t.Fatalf("decoding the marshalled %s: %v", subject.what, err)
		}
		for _, path := range finGatherForbiddenKeyPaths(subject.what, decoded) {
			t.Errorf("the %s carries key %s: the new route publishes runner_path, and a key "+
				"shaped like an argv column would inherit the operator-review-before-paste "+
				"obligation onto a value whose whole worth is that it can be published unreviewed",
				subject.what, path)
		}
	}
	// The two published Details, which the marshalled sweep above already covers
	// for readings but not for the record — and which are named here because they
	// are the strings an operator reads, and the ones a future arm would be
	// tempted to interpolate the reading into.
	if strings.Contains(readings.Gate.Detail, trailNeedle) {
		t.Errorf("the gate's Detail carries captured bytes: %s", readings.Gate.Detail)
	}
	if strings.Contains(readings.Admit.Detail, trailNeedle) {
		t.Errorf("the attribution's Detail carries captured bytes: %s", readings.Admit.Detail)
	}

	// --- rung 5: the second fabric ---------------------------------------------

	if bytes.Contains(encoded, []byte(finGatherArgvHead)) {
		t.Errorf("the marshalled readings carry %q, the head BOTH argv fixtures share and none of "+
			"tdnRunnerFromArgv's five answers does: a reduction echoing the head of what it was "+
			"handed leaks argv even when no needle was planted in it. Readings: %s",
			finGatherArgvHead, encoded)
	}
}

// --- the pinned-pid reading ---------------------------------------------------------

// finGatherPinnedReadPID is the pid the pinned-pid fixtures below are ABOUT. It is
// neither finGatherNamedPGID nor finGatherUnnamedPGID, so a failure message naming
// it cannot be misread as the attribution leg's join key: this reading is a
// DIFFERENT INSTRUMENT over a DIFFERENT operand and shares nothing with that leg.
const finGatherPinnedReadPID = 9111

// finGatherPinnedReading is one shape the caller's pinned-pid read can take.
type finGatherPinnedReading struct {
	name    string
	reading pinStateOutcome
}

// finGatherPinnedReadings returns the three shapes the rows below vary across: the
// zero, a running verdict with the StateColumn that arm fills, and an instrument
// failure carrying BOTH string-bearing members.
//
// SHAPES AND NOT MERELY VERDICTS, because the claim is that the WHOLE value
// crosses. A pass-through narrowed to in.PinnedPid.Verdict satisfies every
// verdict-only assertion, and the two members it would drop — Detail and
// ToolStderr, where pinClassifyState's instrument-failed branch puts raw ps stderr
// — are exactly the route the gather-tier sweep #1459 owes has to travel.
//
// FIXTURES AND NOT LIVE pinReadState CALLS. A live read is non-deterministic and
// the subject here is ROUTING rather than the producer; the
// produced-by-pinReadState constraint binds the LIVE CALLER, where it is held by
// finGatherInputs.PinnedPid's doc and by review. Said here so a later reader does
// not over-trust the green.
//
// A FUNCTION rather than a package-level var, for trailRunWellFormed's stated
// reason — although every field here is a scalar, the rows are copied onto a
// shared base and the file keeps one rule.
func finGatherPinnedReadings() []finGatherPinnedReading {
	return []finGatherPinnedReading{
		{
			name:    "no read was taken at all",
			reading: pinStateOutcome{},
		},
		{
			name: "a running verdict carrying the state column that arm fills",
			reading: pinStateOutcome{
				Verdict:     pinStateRunning,
				Detail:      "a per-pid read that found the pinned group in the table",
				PID:         finGatherPinnedReadPID,
				PPID:        1,
				StateColumn: "S+",
			},
		},
		{
			name: "an instrument failure carrying BOTH string-bearing members",
			reading: pinStateOutcome{
				Verdict:    pinStateInstrumentFailed,
				Detail:     "ps exited 1 and wrote to stderr, so it never reported on the pid",
				PID:        finGatherPinnedReadPID,
				ExitStatus: 1,
				ToolStderr: "ps: illegal option",
			},
		},
	}
}

// TestFinGatherPinnedPidDoesNotReachTheLiveness pins that the caller's pinned-pid
// read crosses onto PinnedPid WHOLE and reaches Liveness NOT AT ALL, across rows
// byte-identical in stdout seed, needles, stderr and pinned set that differ in that
// reading alone — TestFinGatherRunnerPathDoesNotReachTheScan's shape, one field
// along.
//
// # The prohibition needs a deterministic net because the wrong edit COMPILES
//
// Liveness is []pinStateOutcome, the gather already appends a pinReadState result
// to it once per matched pid, and pinReadState returns exactly the type this field
// holds — so appending the new reading there instead is type-correct, one line from
// the right answer, and silent. It is also wrong on the merits: Liveness is the
// ARGV SCAN's per-pid set, it feeds tdnVerdictSummary into the published record and
// Step 6's instrument-failure void, and it goes blind once claude exits and the
// group re-parents to init. A pinned pid landing there would move answers on runs
// that have nothing to do with this route.
//
// # What makes the negative non-vacuous
//
// finGatherNeedles returns a t.TempDir() path nothing is ever staged at, so the
// argv leg matches nothing and Liveness is empty DETERMINISTICALLY on every row —
// which is precisely what a build appending the reading there would break, since
// the appended read is the only entry there would be. The premise asserts run
// FIRST: without them a broken ps leaves every check below asserting about the zero
// pinScan rather than about the routing.
//
// # RowsScanned is asserted per row and never compared ACROSS rows
//
// Its value moves between two gather calls for reasons that have nothing to do with
// this field: this package makes many t.Parallel() calls and re-execs itself in
// several places, so a sibling's child appearing or exiting between two calls moves
// the count. The sibling above states that measurement for its own rows; a cross-row
// equality here would be testing the harness. The per-row floor is what the vacuity
// guard actually needs.
//
// NO NEEDLE PLANT AND NO CAPTURED-BYTES SWEEP HERE. The gather tier's sweep over
// this route is #1459's, and #1452's precedent is that a new route gets a sibling
// test of its own rather than a third plant on an existing one.
func TestFinGatherPinnedPidDoesNotReachTheLiveness(t *testing.T) {
	// ONE buffer, ONE needle slice, ONE stderr and ONE pinned set across the rows,
	// for the sibling's stated reason: finGatherNeedles calls t.TempDir() and would
	// vary a second dimension, and sharing the buffer is sound because
	// trailWaitForTrailer only ever calls Bytes(), which returns a copy.
	var stdout probeSyncBuffer
	base := finGatherNegativeInputs(t, &stdout)

	for _, tc := range finGatherPinnedReadings() {
		t.Run(tc.name, func(t *testing.T) {
			in := base
			in.PinnedPid = tc.reading
			readings, _, _ := finGatherReadings(in)

			if readings.ArgvScanErrored {
				t.Fatalf("the argv scan is recorded as errored; want a clean scan — with a broken " +
					"scan Liveness is empty because nothing was read rather than because the " +
					"reading stayed out of it, and this row asserts nothing")
			}
			if readings.RowsScanned <= 0 {
				t.Fatalf("the argv scan parsed %d well-formed row(s); want more than 0 — at zero "+
					"rows an empty Liveness is a nothing-was-measured rather than a statement "+
					"about where the caller's reading landed", readings.RowsScanned)
			}

			// pinStateOutcome is four strings and three ints, so == compares the WHOLE
			// value: a pass-through carrying only the verdict, or rebuilding the
			// outcome from parts, is red here rather than on a member a later reader
			// would have to think to check.
			if readings.PinnedPid != tc.reading {
				t.Errorf("the readings carry pinned-pid read %+v; want %+v — the caller's reading "+
					"crosses WHOLE, and narrowing it to its verdict would leave the Detail and "+
					"ToolStderr members no route to travel", readings.PinnedPid, tc.reading)
			}
			if len(readings.Liveness) != 0 {
				t.Errorf("the readings carry %d per-pid read(s) %+v; want none — Liveness holds one "+
					"read per MATCHED pid and the needle set is one t.TempDir() path nothing is "+
					"staged at, so an entry here is the pinned-pid reading having been appended to "+
					"the argv scan's own set, which feeds the published verdict summary",
					len(readings.Liveness), readings.Liveness)
			}
			if readings.MatchCount != 0 {
				t.Errorf("the argv scan matched %d of %d row(s) although the needle set is one "+
					"t.TempDir() path nothing is staged at; want 0 — the same defect read off the "+
					"count instead of off the liveness list",
					readings.MatchCount, readings.RowsScanned)
			}
		})
	}
}

// TestFinGatherHalfStagedRouteMovesNoOutcome is this ticket's no-outcome-moves
// claim, driven offline through the SHIPPED classifier: with the pid read staged
// and the ordering still unfilled, the gate-absent-owes-none arm answers
// trailOutcomeVoidSightingRouteNotStaged exactly as it does with neither staged.
//
// # The half-staged pair is now the SHIPPED state, not a hypothesis
//
// It is what every live run through finGatherReadings produces, because this ticket
// stages the pin and #1457 has not yet staged the ordering. The arm's guard is
// single-sided ON PURPOSE — the "Testing both fields with &&" note inside
// trailClassifyRun argues that an && guard falls through on exactly this pair and
// publishes a premise as MEASURED and failed — so a pass-through that let the guard
// be defeated is a live defect and not a latent one. This test is where it goes red.
//
// # The recipe is the only one that reaches the arm
//
// Seed trailKeyNamesNoTerminalReason() — a trailer with no terminal_reason key at
// all — under a reading tdnRunnerFromArgv reduces to streamrunner. That is
// finGatherRunnerPathCases' first row, and without both halves the gate lands
// elsewhere and every assertion below is about a different arm, which is why the
// gate value is a Fatalf premise rather than a corroboration.
//
// Ordering is unfilled AUTOMATICALLY: finGatherInputs has no field for it yet. When
// #1457 adds one, this test needs revisiting — the pair it drives stops being what
// a live run produces, and the guard it exercises stops being single-sidedly
// load-bearing.
//
// # Why the whole-value equality is taken over ONE gather's readings
//
// The stronger comparison is between two outcomes differing in the new field and in
// NOTHING else, and two separate gather calls cannot supply that: trailRunOutcome
// publishes MatchCount and RowsScanned, and two live ps scans disagree on the
// latter for reasons this ticket has nothing to do with (see the sibling above). So
// the pair compared field-for-field is one gather's readings against the same
// readings with the new field cleared — exactly the one-dimension variation the
// claim is about — while the second gather, staging nothing, pins that a shipped
// gather with no reading at all still reaches the same VALUE.
//
// Whole-value equality and not a check on Value alone: a pass-through that defeated
// the guard would move Route, RouteReason or Detail while leaving the value intact,
// and every field of trailRunOutcome is a string, an int or a bool, so == compares
// all of them.
func TestFinGatherHalfStagedRouteMovesNoOutcome(t *testing.T) {
	var stdout probeSyncBuffer
	finGatherSeed(t, &stdout, trailKeyNamesNoTerminalReason())
	base := finGatherInputs{
		Stdout:     &stdout,
		Needles:    finGatherNeedles(t),
		PyryExited: true,
		RunnerPath: tdnRunnerFromArgv(tdnFixtureStreamArgv),
	}

	// The reading that WOULD establish had the ordering been certified: a running
	// verdict is what trailEstablishSighting answers trailSightingEstablished on, so
	// a build consulting the route on a half-staged pair reports a FINDING here
	// rather than merely a different void.
	staged := base
	staged.PinnedPid = pinStateOutcome{
		Verdict:     pinStateRunning,
		Detail:      "a per-pid read that found the pinned group in the table",
		PID:         finGatherPinnedReadPID,
		PPID:        1,
		StateColumn: "S+",
	}

	stagedReadings, _, _ := finGatherReadings(staged)
	unstagedReadings, _, _ := finGatherReadings(base)

	// --- the premises, each of which turns a vacuous comparison into a Fatalf ---

	for _, p := range []struct {
		what     string
		readings trailRunReadings
	}{
		{"the staged row", stagedReadings},
		{"the unstaged row", unstagedReadings},
	} {
		if p.readings.Gate.Value != trailGateAbsentOwesNone {
			t.Fatalf("%s reads gate %q (%s); want %q — that arm is the only one that consults the "+
				"sighting route, and at any other gate value step 1 answers first and this "+
				"comparison is about a different arm", p.what, p.readings.Gate.Value,
				p.readings.Gate.Detail, trailGateAbsentOwesNone)
		}
		if p.readings.Ordering.Value != "" {
			t.Fatalf("%s carries ordering %q; want the unfilled \"\" — the half-staged pair is the "+
				"whole subject here, and a filled ordering makes this a test of the route "+
				"instead of of the guard above it", p.what, p.readings.Ordering.Value)
		}
	}
	if stagedReadings.PinnedPid != staged.PinnedPid {
		t.Fatalf("the staged row carries pinned-pid read %+v; want %+v — without the reading "+
			"having crossed, the pair below differs in nothing and the invariance is vacuous",
			stagedReadings.PinnedPid, staged.PinnedPid)
	}
	if unstagedReadings.PinnedPid != (pinStateOutcome{}) {
		t.Fatalf("the unstaged row carries pinned-pid read %+v; want the zero — a gather filling "+
			"this field from anywhere but its caller would make both rows staged",
			unstagedReadings.PinnedPid)
	}

	// --- the claim ---

	withPin := trailClassifyRun(stagedReadings)
	cleared := stagedReadings
	cleared.PinnedPid = pinStateOutcome{}
	withoutPin := trailClassifyRun(cleared)

	if withPin.Value != trailOutcomeVoidSightingRouteNotStaged {
		t.Fatalf("the staged row classifies as %q (%s); want %q — the arm's guard tests the "+
			"ORDERING alone and answers before the route is consulted, so staging the pid moves "+
			"no outcome. Route %q reason %q", withPin.Value, withPin.Detail,
			trailOutcomeVoidSightingRouteNotStaged, withPin.Route, withPin.RouteReason)
	}
	if withPin != withoutPin {
		t.Errorf("the two outcomes differ across the new field alone:\n with pin: %+v\n"+
			" without: %+v\nthe guard is single-sided BY DESIGN, and a pass-through that let it "+
			"be defeated publishes a premise as MEASURED and failed on a run where nothing was "+
			"staged", withPin, withoutPin)
	}

	// The second gather, which stages nothing at all: a shipped gather with no
	// reading reaches the same value, so the field's mere existence moved nothing.
	if got := trailClassifyRun(unstagedReadings); got.Value != withPin.Value {
		t.Errorf("a gather staging nothing classifies as %q (%s) while one staging a pinned-pid "+
			"read classifies as %q — the two must agree, because the ordering guard answers "+
			"before either reading is consulted", got.Value, got.Detail, withPin.Value)
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
// trailBoundFromStart, so this row does not go red against a re-scan. The row that
// does is TestFinGatherSightingReportsTheMissBound below: it pays a real sleep for
// a buffer that misses a poll first, and so proves the carrier reports the bound
// that bounds something. What THIS row closes is a carrier filled from anywhere
// other than the poll whose scan result was classified.
//
// Staleness has no counterpart on the readings to agree with — trailRunReadings
// deliberately carries no staleness field (trail_run_outcome_test.go:436-439) — so
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

// TestFinGatherSightingReportsTheMissBound proves the carrier reports the one
// bound value that bounds anything, MEASURED against a poll that really missed
// before the trailer arrived rather than trusted because a comment says the
// carrier copies what the sighting measured.
//
// # Why this row costs wall clock, and why only one other does
//
// Every other row here pre-seeds the buffer — every one but
// TestFinGatherRecordPublishesTheMeasuredMissBound below, which leaves it
// unseeded for this row's own reason and costs the same wall clock. A seeded
// buffer makes the first poll hit, so lastMiss stays zero and the bound is
// trailBoundFromStart — the discriminator whose own doc says it BOUNDS NOTHING
// (result_trailer_observation_test.go:80-85).
// trailBoundFromMiss is produced only where a poll missed first (:281-287 there),
// and reaching it costs a real sleep: polls at ~0, ~200 and ~400ms miss, the
// append lands at ~500ms, and the poll at ~600ms hits with the bound measured
// from the ~400ms miss. There is no cheaper route to the interesting value.
//
// The structure is TestTrailWaitForTrailer's first subtest (:554-594 there),
// mirrored rather than reinvented — including the goroutine split, which is not
// style: probeSyncBuffer.Write returns an error that must be reported with
// t.Fatalf, and calling t.* from a spawned goroutine after the test function has
// returned panics. finGatherReadings takes no *testing.T, so the inputs are built
// on the test goroutine and only the call itself crosses. The increment
// over that subtest is that the claim is made over the CARRIER the gather returns,
// one level up, and never over the observation.
//
// # The second observation is the contrast, and only its discriminator is bound
//
// A second trailWaitForTrailer over the SAME buffer after the gather has returned
// reports trailBoundFromStart: the trailer is in the buffer by then, so its first
// poll matches and lastMiss is still zero. That contrast is what makes the miss
// bound above discriminating rather than merely asserted — it measures, in this
// test and over these bytes, what a carrier filled from a second scan would have
// reported instead. Its timeout is immaterial for that same reason; passing the
// file's own constant is what keeps a new literal out. It is a direct call and NOT
// a second finGatherReadings, which would re-run the argv and attribution legs
// this row asserts nothing about.
//
// THE DISCRIMINATOR IS READ OFF THE CALL AND THE OBSERVATION IS NEVER BOUND. That
// value is a trailObservation, so it carries .Line and the decoded pointer — the
// two things the carrier exists to keep out of a caller's reach and the two the
// header forbids a message from naming (:114-116). With no observation in scope a
// later edit CANNOT %v one into a failure; with one in scope only attention would
// stop it, and the failure would be SILENT, because trailFixtureTrailer carries no
// needle and a stray print would trip no sweep. Prefer the shape that cannot be
// got wrong over the discipline that must not be, in finGatherInputs' own words.
//
// # What this row does not assert
//
// The gate and the attribution run here as on every other row — trailFixtureTrailer
// certifies "completed" — and this row asserts on neither; the file's other tests
// own them. That the carrier is filled independently of the verdict is already a
// property of the source rather than of this fixture: the fill sits at the trailer
// leg and the attribution guard on Gate.Reason sits below it. Staleness is
// TestFinGatherSightingComesFromTheClassifiedPoll's, the claim that a staleness
// covers the true lateness is TestTrailWaitForTrailer's (:584-587 there), and
// lateness_bounded is derived from this discriminator at one place only
// (finding_trailer_evidence_test.go:309-315). This row adds a second source of none
// of the three, and plants no needle.
func TestFinGatherSightingReportsTheMissBound(t *testing.T) {
	var stdout probeSyncBuffer
	// Built inline rather than through finGatherNegativeInputs, which SEEDS the
	// buffer it is handed — the one thing this row exists not to do, since a seeded
	// buffer makes the first poll hit and the bound the opposite of this row's
	// claim. Every other field is that helper's, so this row differs from the file's
	// base fixture in exactly one dimension: the buffer starts empty. Built on the
	// TEST goroutine, because finGatherNeedles calls t.TempDir().
	in := finGatherInputs{
		Stdout:     &stdout,
		Needles:    finGatherNeedles(t),
		Stderr:     []byte(trailReapLine(1, fmt.Sprintf("[%d]", finGatherNamedPGID)) + "\n"),
		Pinned:     []int{finGatherUnnamedPGID},
		PyryExited: true,
	}

	var readings trailRunReadings
	var sighting finSighting
	done := make(chan struct{})
	go func() {
		defer close(done)
		readings, _, sighting = finGatherReadings(in)
	}()

	// Past two poll ticks, so at least one non-matching poll is certainly observed
	// before the append.
	time.Sleep(500 * time.Millisecond)

	// ONE Write call, deliberately: it holds the mutex for its whole body, so a
	// concurrent poll sees either none of the line or all of it. Two writes would
	// let a poll observe a torn JSON line — ordinary input to trailScan, which fails
	// to unmarshal it and simply does not match — but enough to make the tick on
	// which the trailer becomes visible non-deterministic.
	if _, err := stdout.Write([]byte(trailFixtureTrailer + "\n")); err != nil {
		t.Fatalf("appending the trailer: %v", err)
	}
	<-done

	secondBound := trailWaitForTrailer(&stdout, finGatherTrailerWait).BoundFrom

	// The premise: the poll MATCHED. At any other state the bound is trailBoundNone
	// and the checks below would be asserting about a sighting that measured
	// nothing, arriving as a bare bound mismatch rather than naming itself.
	if sighting.State != trailSeen {
		t.Fatalf("the sighting reports scan state %q; want %q — the trailer was appended past two "+
			"poll ticks and well inside the %v wait, so any other state means the poll never "+
			"observed it and there is no bound to assert about", sighting.State, trailSeen,
			finGatherTrailerWait)
	}

	if sighting.BoundFrom != trailBoundFromMiss {
		t.Fatalf("the carrier reports bound origin %q; want %q — the buffer was EMPTY when the "+
			"gather started and the append landed past two poll ticks, so a non-matching poll was "+
			"certainly observed before it and the bound is a real one", sighting.BoundFrom,
			trailBoundFromMiss)
	}
	if readings.BoundFrom != sighting.BoundFrom {
		t.Fatalf("the readings report bound origin %q and the carrier %q — ONE gather call makes "+
			"ONE trailWaitForTrailer call, so a disagreement on the value that actually bounds "+
			"something means the carrier was filled from somewhere other than the poll whose scan "+
			"result was classified", readings.BoundFrom, sighting.BoundFrom)
	}
	if secondBound != trailBoundFromStart {
		t.Errorf("a second observation of the same buffer reports bound origin %q; want %q — the "+
			"contrast is what makes the check above discriminating rather than merely asserted: it "+
			"measures, over these very bytes, what a carrier filled from a second scan would have "+
			"reported instead", secondBound, trailBoundFromStart)
	}
}

// TestFinGatherRecordPublishesTheMeasuredMissBound proves the PUBLISHED lateness
// bound is the one the CLASSIFYING sighting measured, by driving a record out of
// a poll that really missed before the trailer arrived and putting it beside the
// record the discarded alternative would have published over the same bytes.
//
// # The two tiers this joins
//
// TestFinTrailerRecordCarriesTheBoundAndItsDiscriminator
// (`TestFinTrailerRecordCarriesTheBoundAndItsDiscriminator`) pins lateness_bounded on
// trailBoundFromMiss alone across all three discriminators — but with the
// discriminator HANDED IN, as every finTrailerBuild call site before this row
// does: each reaches the builder through finTrailerSighting over a shipped scan,
// with the bound arriving as a literal or a table field. Nothing at that tier
// ever met a poll. The row above measures the discriminator against a poll that
// really missed and stops at the carrier, deliberately. So the boolean was
// pinned where it was not measured and measured where it was not published, and
// this row is the join. Since #1320 the builder takes the gather's carrier, so
// one poll drives both the classified outcome and the published record — but
// that is an argument from the INPUT TYPE, and until this row it was the only
// thing standing behind the claim.
//
// # The contrast, and why it is measured rather than fixtured
//
// By the time the composition has classified, the trailer is in the buffer, so a
// second trailWaitForTrailer matches on its FIRST poll and reports
// trailBoundFromStart — the discriminator whose own doc says it BOUNDS NOTHING
// (result_trailer_observation_test.go:80-85). A record built from it publishes
// lateness_bounded false for a run that was genuinely bounded. Both records come
// out of one builder over one set of bytes under the SAME outcome argument, so
// the published difference is attributable to the bound and not to the verdict,
// and a consumer that regressed to the second-scan shape goes red here.
//
// THE DISCRIMINATOR IS READ OFF THE CALL AND THE OBSERVATION IS NEVER BOUND, in
// the row above's own words and for its reason: a trailObservation carries .Line
// and the decoded pointer, the two things the carrier exists to keep out of a
// caller's reach and the two the header forbids a message from naming
// (:114-116). With no observation in scope a later edit CANNOT %v one into a
// failure. That is also why the second carrier is derived through
// finTrailerSighting over
// trailScan(stdout.Bytes()): the shipped derivation, pure over bytes, taking a
// scan and not an observation, and how every existing call site reaches the
// builder. NO finSightingFrom CONSTRUCTOR — finGatherReadings' carrier fill
// rejects one by name, and this row adds no symbol.
//
// # The second carrier's Staleness is handed zero, and that is a decision
//
// The row asserts nothing about it, so a measured value would buy nothing.
// Reading a SECOND field off the second poll needs either a bound
// trailObservation, forbidden above, or a THIRD trailWaitForTrailer call — and a
// carrier whose duration and discriminator came from two different observations
// is the exact defect shape this family exists to prevent. Handing the FIRST
// sighting's Staleness is rejected more sharply for the same reason: it would
// build, inside the test that proves a record's lateness fields come from one
// poll, a record whose staleness_ns and lateness_bound_from came from different
// polls. Zero is the no-measurement value, so the second record is SYNTHETIC IN
// THAT ONE DIMENSION — a real start-bound poll stamps a positive now.Sub(start)
// (`trailWaitForTrailer`).
//
// The consequence, stated so it is not read as a hole: carrying a zero Staleness
// beside a false Bounded, this row does NOT discriminate a builder deriving
// Bounded from Staleness != 0. That mutant is killed at the record tier by
// TestFinTrailerRecordCarriesTheBoundAndItsDiscriminator's start-bound and
// aborted rows, each pairing a 250ms staleness with a discriminator that bounds
// nothing. Restating it here is not this row's job; this row's job is that the
// PUBLISHED bound is the one the CLASSIFYING sighting measured.
//
// # Wall clock, messages, and what this row does not assert
//
// It leaves the buffer empty and appends past two poll ticks exactly as the row
// above does and for the same reason — a pre-seeded buffer cannot reach the miss
// bound at all (result_trailer_observation_test.go:281-287), so the delayed
// append is load-bearing rather than decorative — and it costs the same roughly
// 600ms. finGatherTrailerWait's own accounting names both rows, and NO ROW EVER
// WAITS THAT CONSTANT OUT.
//
// Its messages name a finTrailerRecord's fields, which the header's licence does
// NOT cover: that licence is enumerated over the composition's THREE RETURNS and
// this record is not one of them. The licence is
// TestFinTrailerRecordCarriesNoCapturedBytes' (`TestFinTrailerRecordCarriesNoCapturedBytes`),
// which sweeps this record in the other file — named rather than assumed, and
// stop_reason's uncapped model-authored exposure is inherited knowingly here as
// it is there.
//
// The gate and the attribution run as on every row here — trailFixtureTrailer
// certifies "completed" — and neither record is asserted on for them. The
// carrier-tier claims are the row above's and are not restated: these claims are
// the RECORD's, and the record-tier assertion subsumes the premise it would
// otherwise need. Neither record's Staleness is asserted and the two are never
// asserted equal — the first is measured from the last non-matching poll and the
// second is a handed zero, so an equality there would be red by construction.
// This row plants no needle.
func TestFinGatherRecordPublishesTheMeasuredMissBound(t *testing.T) {
	var stdout probeSyncBuffer
	// The row above's fixture field for field, and for its reason:
	// finGatherNegativeInputs SEEDS the buffer it is handed, and a seeded buffer
	// makes the first poll hit and the bound the opposite of this row's claim.
	// Built on the TEST goroutine, because finGatherNeedles calls t.TempDir().
	in := finGatherInputs{
		Stdout:     &stdout,
		Needles:    finGatherNeedles(t),
		Stderr:     []byte(trailReapLine(1, fmt.Sprintf("[%d]", finGatherNamedPGID)) + "\n"),
		Pinned:     []int{finGatherUnnamedPGID},
		PyryExited: true,
	}

	var sighting finSighting
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, sighting = finGatherReadings(in)
	}()

	// Past two poll ticks, so at least one non-matching poll is certainly observed
	// before the append.
	time.Sleep(500 * time.Millisecond)

	// ONE Write call, deliberately: it holds the mutex for its whole body, so a
	// concurrent poll sees either none of the line or all of it. Two writes would
	// let a poll observe a torn JSON line — ordinary input to trailScan, which
	// simply does not match — but enough to make the tick on which the trailer
	// becomes visible non-deterministic.
	if _, err := stdout.Write([]byte(trailFixtureTrailer + "\n")); err != nil {
		t.Fatalf("appending the trailer: %v", err)
	}
	<-done

	// The measured record, built from the carrier the poll that CLASSIFIED this
	// run filled. The outcome is held IDENTICAL across both builds so the
	// published difference is attributable to the bound and not to the verdict; it
	// is what the header documents for these inputs (:59-66), but this row makes
	// no claim that the composition reached it and does not call trailClassifyRun
	// to find out — that would add a leg it asserts nothing about.
	first := finTrailerBuild(trailOutcomeNoRowMatched, sighting)

	// The contrast: the discriminator MEASURED off a real second poll over these
	// exact bytes, the rest of the carrier derived from those same bytes. Both
	// reads run on the test goroutine after <-done with no writer left, so the
	// bytes are frozen and the two see identical content.
	secondBound := trailWaitForTrailer(&stdout, finGatherTrailerWait).BoundFrom
	second := finTrailerBuild(trailOutcomeNoRowMatched,
		finTrailerSighting(trailScan(stdout.Bytes()), 0, secondBound))

	// The premise: the poll MATCHED. At any other state the bound is
	// trailBoundNone and everything below would be asserting about a record whose
	// sighting measured nothing, arriving as a bare bound mismatch rather than
	// naming itself.
	if first.State != trailSeen {
		t.Fatalf("the published record reports scan state %q; want %q — the trailer was appended "+
			"past two poll ticks and well inside the %v wait, so any other state means the poll "+
			"never observed it, the bound is %q and there is nothing below to assert about",
			first.State, trailSeen, finGatherTrailerWait, trailBoundNone)
	}

	if first.BoundFrom != trailBoundFromMiss {
		t.Fatalf("the published record reports bound origin %q; want %q — the buffer was EMPTY "+
			"when the gather started and the append landed past two poll ticks, so a non-matching "+
			"poll was certainly observed before the one that classified this run",
			first.BoundFrom, trailBoundFromMiss)
	}
	if !first.Bounded {
		t.Errorf("the published record reports lateness_bounded %t over bound origin %q; want "+
			"true — lateness_bounded is BoundFrom == %s and nothing else, and this is the first "+
			"row where that BoundFrom reached the builder from a poll that really missed rather "+
			"than from a table field", first.Bounded, first.BoundFrom, trailBoundFromMiss)
	}

	if second.BoundFrom != trailBoundFromStart {
		t.Errorf("a record built from a SECOND observation of the same bytes reports bound origin "+
			"%q; want %q — the trailer is in the buffer by then, so that poll matches first and "+
			"measures from its own start; this is what a record filled from a second scan would "+
			"have published over these very bytes instead", second.BoundFrom, trailBoundFromStart)
	}
	if second.Bounded {
		t.Errorf("the second record reports lateness_bounded %t over bound origin %q; want false "+
			"— %s bounds nothing, so a record publishing true there would wear a bound's label "+
			"for a reading that measured none, on a run that WAS genuinely bounded",
			second.Bounded, second.BoundFrom, trailBoundFromStart)
	}

	// Separated by MEASUREMENT and not by argument: one builder, one outcome
	// argument, one set of bytes. Without this the two records could differ for
	// any reason at all, and it is the load-bearing half of the contrast.
	for _, f := range []struct {
		field     string
		got, want any
	}{
		{"outcome", second.Outcome, first.Outcome},
		{"trailer_state", second.State, first.State},
		{"subtype", second.Subtype, first.Subtype},
		{"is_error", second.IsError, first.IsError},
		{"terminal_reason", second.TerminalReason, first.TerminalReason},
		{"stop_reason", second.StopReason, first.StopReason},
	} {
		if f.got != f.want {
			t.Errorf("the two records disagree on %s: the second publishes %v and the measured one "+
				"%v — they come out of one builder from one outcome argument over one set of "+
				"bytes, so the ONLY published difference may be the bound, its boolean and the "+
				"Detail that interpolates both", f.field, f.got, f.want)
		}
	}

	// The published Detail tracks the MEASURED bound rather than merely being
	// non-empty, which is all the record-tier row asserts. The two constants are
	// not substrings of one another, so the check is clean in both directions.
	firstNamesIt := strings.Contains(first.Detail, trailBoundFromMiss)
	secondNamesIt := strings.Contains(second.Detail, trailBoundFromStart)
	if !firstNamesIt || !secondNamesIt {
		t.Errorf("the published Details do not each name the bound their record was built from: "+
			"the measured record's names %q=%t and the second's names %q=%t, and both must hold "+
			"— a reader of either record's prose must be able to tell which bound it is reading "+
			"without consulting the field beside it", trailBoundFromMiss, firstNamesIt,
			trailBoundFromStart, secondNamesIt)
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
// terminal_reason falls past the cap, and it belongs to
// TestFinGatherSightingScalarsComeFromTheFullLineDecode, below in this file. Named
// by SYMBOL and with no line number deliberately: that test and this sentence land
// in one diff, so any number written here would be measured before the lines it
// points at existed.
//
// Equally, no reachable sighting produces trailSeen with a nil Trailer —
// trailWaitForTrailer fills the pointer on every seen result — so the inconsistent
// pair is not exercised. finTrailerBuild sees no pair at all now, only this bool, and
// its synthetic analogue is flipped off a scan-filled carrier rather than typed in.
//
// # Neither arm costs wall clock
//
// Both seeds pre-empt the poll loop: the first matches on the first iteration, and
// trailWaitForTrailer returns from an ABORTED scan immediately because abortion is
// monotone, which is finGatherTrailerWait's last accounting bullet. A genuinely
// absent trailer would carry no decode either and
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
			// finGatherCases()' C4 row, for its stated reason: it is the no-decode arm
			// this file already ships and it returns immediately.
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
			// read, finGatherAssertContract's own C2 idiom: the expectation is the decode's
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
					"records the outcome of the State/nil PAIR and is the one bool finTrailerBuild "+
					"gates on, and a reader with only a State cannot tell \"there was no trailer\" "+
					"from \"the trailer's fields were empty\"", sighting.CarriesTrailer, tc.wantCarries)
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

// --- the over-cap fixture ---------------------------------------------------------

// finGatherOverCapPad is the padding that makes the two candidate reads of a
// scanned trailer give DIFFERENT ANSWERS, which is the whole of
// TestFinGatherSightingScalarsComeFromTheFullLineDecode's premise.
//
// trailPaddedTrailer(200) renders a 585-byte line, past reachCapCommand's
// 512-byte cap. Measured at 8ce6a7f, on that line:
//
//	"subtype"          byte 17           inside the cap
//	"is_error"         byte 45           inside the cap
//	"stop_reason"      byte 348          inside the cap
//	"terminal_reason"  byte 555          PAST it — key and value both cut
//	trailNeedle        bytes [304, 346)  inside the cap
//
// Those offsets are a property of the PAD and not an invariant of the fixture, so
// the test asserts the one it rests on rather than trusting this comment —
// finWriteTrailerPad makes the same disclaimer for the same reason
// (`finWriteTrailerPad`).
//
// # The usable window is pad 141 through 366
//
// Below 141 the "terminal_reason" KEY still fits: at pad 140 it occupies bytes
// [495, 512) and survives the cap exactly, while its value does not. The two reads
// would then agree about the key and the precondition would go vacuous. Above 366
// trailNeedle falls past the cap, which costs this pad its reusability rather than
// this test's claim. 200 sits comfortably inside both ends.
//
// # Pin the KEY, never the value
//
// trailPaddedTrailer renders subtype "error_max_turns" alongside terminal_reason
// "max_turns", so "max_turns" first occurs at byte 34 and survives every cap:
// strings.Contains(Line, "max_turns") is true even on a line whose terminal_reason
// was cut clean off. A precondition pinning the VALUE is silently vacuous. This is
// the one edit a later "simplification" would reach for.
//
// # The needle lands INSIDE the cap at this pad
//
// That is the opposite of trailNeedle's own stated intent, "placed PAST the cap so
// a record that leaked it could only have done so by recording the line in full"
// (`trailNeedle`). It is safe for the test below,
// which plants no needle of its own, marshals nothing and makes no leak claim. It
// means this pad MUST NOT be reused by a sweep whose argument is "a needle sighting
// proves the line was recorded in full" — under this pad a needle in Line is
// expected. finWriteTrailerPad deliberately takes the opposite position for its own
// sweep, where in-cap is exactly what makes the plant non-vacuous; both conventions
// are correct, and which one a pad holds has to be written down where the pad is.
const finGatherOverCapPad = 200

// TestFinGatherSightingScalarsComeFromTheFullLineDecode proves the carrier's four
// decoded scalars are what the FULL trailer line decoded to and never a re-read of
// the capped copy, MEASURED on a fixture where the two reads give different answers
// rather than trusted because trailScanResult.Trailer's doc says which one it is
// (result_trailer_observation_test.go:108-118).
//
// That separation is what licenses the cap being applied to Line alone: truncation
// degrades human-readable evidence and never a field the consumer branches on.
//
// # Why the shipped fill check cannot make this claim
//
// TestFinGatherSightingCarriesTheDecodedScalars runs its decoded arm over
// trailFixtureTrailer, 342 bytes and so UNDER the cap. There the capped copy IS the
// full line, both reads agree, and the row passes whichever one the gather took —
// which is why its own doc calls itself a fill check and nothing more. This test
// pays for a line past the cap, where the two disagree.
//
// # All four scalars discriminate, not only the one whose bytes the cap reached
//
// At finGatherOverCapPad only terminal_reason is cut; subtype, is_error and
// stop_reason all sit inside the cap, and the first two are early enough on the
// wire order that no pad could ever cut them. A reader checking key positions alone
// would conclude three of the four are decorative. They are not, because the
// alternative read is not a per-key one. The capped copy is line[:512] plus a
// truncation marker — JSON cut mid-token — and json.Unmarshal validates its whole
// input before it fills anything, so it returns an error and writes NOTHING:
//
//	                  full-line decode   re-read of the capped copy
//	subtype           error_max_turns    ""
//	is_error          true               false
//	stop_reason       end_turn           ""
//	terminal_reason   max_turns          ""
//
// That is a property of encoding/json rather than of this fixture, which is why the
// contrast below is measured here rather than argued. It is unmarshalled into a
// FRESHLY DECLARED destination, and that is load-bearing: on a syntax error
// Unmarshal leaves the destination untouched, so a reused variable would carry its
// own contents into the comparison instead of the zero resultTrailer.
//
// # Failure messages
//
// The header's rule (:114-116) binds harder here than anywhere else in this file.
// At this pad the retained Line genuinely carries padded stand-in payload AND
// trailNeedle within the cap, and no sweep covers this row —
// TestFinGatherReturnsNoCapturedBytes runs over its own fixture, not this one, so a
// stray print here would reach a published artifact with no test to catch it. A
// message in this test MAY name scan.State, scan.Detail, len(scan.Line),
// reachMaxCommandBytes, finGatherOverCapPad, the key being sought, and the four
// scalars on either side of a comparison. It MAY NEVER render scan.Line, scan,
// scan.Trailer, or a whole resultTrailer. TestFinGatherReturnsNoCapturedBytes'
// in-cap premise is the shipped model for this: it reasons about Line and prints
// only its length.
//
// # What this row does not assert
//
// The gate and the attribution. terminal_reason "max_turns" makes trailGate answer
// trailGateBudgetFired carrying a NON-EMPTY Reason
// (trailer_admissibility_test.go:384-394), so the gather's attribution guard is
// satisfied and that leg RUNS here, returning a structural void for the budget path.
// Incidental: this row is about the trailer leg, and asserting on either would
// restate rows the file already ships. That the carrier fill is independent of the
// gate's verdict holds by DATA DEPENDENCE and not by ordering — the fill reads only
// obs and never reads readings.Gate, which finGatherReadings computes ABOVE the
// fill. Staleness and BoundFrom belong to the two tests above; this row adds no
// second source of either.
//
// # It costs no wall clock
//
// The buffer is pre-seeded and 585 bytes is far under bufio.Scanner's 64 KiB
// default, so the first poll hits and the loop returns before it ever sleeps.
func TestFinGatherSightingScalarsComeFromTheFullLineDecode(t *testing.T) {
	var stdout probeSyncBuffer
	seed := finGatherSeed(t, &stdout, trailPaddedTrailer(finGatherOverCapPad))

	// Recomputed through the SHIPPED scanner over the SAME bytes the gather is
	// about to read, finGatherAssertContract's own C2 idiom: the expectation is the
	// decode's and never four typed-in literals.
	scan := trailScan(seed)
	if scan.State != trailSeen || scan.Trailer == nil {
		t.Fatalf("the shipped scan reads state %q carrying a decoded trailer %t (%s); want %q "+
			"carrying one — every check below is about WHICH of the two reads the carrier took, "+
			"and with no decode there is nothing to compare either of them against",
			scan.State, scan.Trailer != nil, scan.Detail, trailSeen)
	}
	want := *scan.Trailer

	// --- the precondition: the two candidate reads genuinely disagree ---

	// The decode has it. Literal-free, and exact: the zero resultTrailer holds "",
	// so a non-empty value here is one the FULL-line decode filled.
	if want.TerminalReason == "" {
		t.Fatalf("the full-line decode reports an EMPTY terminal_reason — the fixture renders it " +
			"last on the wire order, and with it empty the decode and the capped copy agree on the " +
			"zero value and every check below asserts nothing")
	}
	// The capped copy lacks it. The KEY and never the value: trailPaddedTrailer
	// renders subtype "error_max_turns", so "max_turns" first occurs at byte 34
	// and survives every cap — a check for the VALUE reads true even on a line
	// whose terminal_reason was cut clean off, and would be silently vacuous.
	if strings.Contains(scan.Line, `"terminal_reason"`) {
		t.Fatalf("the retained copy still carries the \"terminal_reason\" KEY inside the %d-byte "+
			"cap (%d bytes retained), so the two candidate reads no longer disagree and this row "+
			"is vacuous — pad %d must keep that key past the cap, and the usable window is 141 "+
			"through 366", reachMaxCommandBytes, len(scan.Line), finGatherOverCapPad)
	}

	// --- the contrast: what a re-read of the capped copy would have reported ---

	// FRESHLY declared, and that is the point: on a syntax error Unmarshal leaves
	// its destination untouched, so a reused value would carry its own contents
	// into the comparison rather than the zero resultTrailer.
	var reread resultTrailer
	if err := json.Unmarshal([]byte(scan.Line), &reread); err == nil {
		t.Fatalf("the retained copy decoded CLEANLY — it is the first %d bytes plus a truncation "+
			"marker and is expected to be JSON cut mid-token. If the cap learned to close what it "+
			"truncates, the checks below stop measuring a contrast and start recording an "+
			"agreement", reachMaxCommandBytes)
	}
	if reread.Subtype == want.Subtype || reread.IsError == want.IsError ||
		reread.TerminalReason == want.TerminalReason || reread.StopReason == want.StopReason {
		t.Fatalf("a re-read of the capped copy AGREES with the full-line decode on at least one of "+
			"the four, so that one cannot discriminate between the two reads: subtype %q vs %q, "+
			"is_error %t vs %t, terminal_reason %q vs %q, stop_reason %q vs %q",
			reread.Subtype, want.Subtype, reread.IsError, want.IsError,
			reread.TerminalReason, want.TerminalReason, reread.StopReason, want.StopReason)
	}

	// --- the claim ---

	_, _, sighting := finGatherReadings(finGatherInputs{
		Stdout:  &stdout,
		Needles: finGatherNeedles(t),
		// No reap log and no pin, exactly as the shipped table's rows pass them:
		// this row asserts about the trailer leg.
		PyryExited: true,
	})

	if sighting.State != trailSeen || !sighting.CarriesTrailer {
		t.Fatalf("the sighting reports scan state %q carrying a decoded trailer %t; want %q "+
			"carrying one — the four scalars are filled only behind that pair, so at any other "+
			"reading they hold zero values and the comparison below would be about nothing",
			sighting.State, sighting.CarriesTrailer, trailSeen)
	}
	if sighting.Subtype != want.Subtype || sighting.IsError != want.IsError ||
		sighting.TerminalReason != want.TerminalReason || sighting.StopReason != want.StopReason {
		t.Errorf("the sighting carries subtype=%q is_error=%t terminal_reason=%q stop_reason=%q; "+
			"want %q, %t, %q, %q — the capped copy decodes to NONE of the four, so a carrier filled "+
			"from a re-read of the retained line misses every one of them and not merely the one "+
			"whose bytes the cap reached",
			sighting.Subtype, sighting.IsError, sighting.TerminalReason, sighting.StopReason,
			want.Subtype, want.IsError, want.TerminalReason, want.StopReason)
	}
}

// --- the carrier's structural claim -----------------------------------------------

// TestFinSightingReachesNoScanType is the carrier's structural claim, and a
// comment could not serve it: the point is that a LATER EDIT adding a field that
// carries any of the three ONE LEVEL DOWN fails too, which asserting over a single
// instance would never catch.
//
// finRecordInputReaches is the family's walker —
// same package, same build tag — and it follows struct fields, slice and array
// elements, pointers and map keys and values, so naming resultTrailer catches a
// *resultTrailer as well. The increment here is the third forbidden type, not a
// second traversal.
//
// Scope is the carrier alone. trailRunReadings argues its own non-reachability at
// trail_run_outcome_test.go:433-439 and finRecordRun's is pinned by
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
		// and never what it said (trail_run_outcome_test.go:411-418).
		"argvscanerrored": "the errored discriminator, which records that the scan failed",
		// pinStateOutcome.ToolStderr. #1271 admits pinStateOutcome into the
		// readings WHOLE (trail_run_outcome_test.go:425-428) because it carries no
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
			in:   `{"entries":[{"pgid":7930,"admit":{"reap_line":"leaked"}}]}`,
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

//go:build e2e_realclaude

package realclaude

import (
	"strings"
	"testing"
)

// TestTrailComposesUnderAPtyrunnerReading pins the chain the live exit-path
// probe's headline reading rests on — trailGate -> trailAdmitAttribution ->
// trailClassifyRun — driven offline from fixtures under a runner-path reading
// that reduces to ptyrunner, with the published Details pinned beside the value.
//
// # What this pin is about, and what it is NOT
//
// It reproduces the composition GIVEN a ptyrunner reading. It does NOT reproduce
// the path a live run reports, and nothing here may be read as claiming it does:
// both shipped gathers fill the gate's runner-path field with trailRunnerUnread()
// by construction (finding_run_gather_test.go:552, :789;
// trail_run_rig_test.go:162), because tdnClaudeCommand skips any row whose
// matched-needle list lacks tdnClaudeNeedle (teardown_liveness_probe_test.go:561-575)
// and finding_exit_path_probe_test.go:264-272 forbids adding that needle to that
// gather's scan — it has no finLivePinReduce, so claude's row would land in the
// classifier's match-count arms and in the published liveness list. Over a live
// run today the reading always names no runner.
//
// # Why it is green the day it lands
//
// The chain here is driven over trailFixtureTrailer, which reaches
// trailGateUsable — an arm that ignores the runner path — so THIS composition
// cannot vary by it, which is the property being pinned rather than a gap. The
// general claim that no arm reads the path is gone: #1420's absence arm reads it,
// and reaches three different cases by it. A green pin is worth exactly what it
// discriminates, so every assertion below is the sole red for one enumerated
// mis-implementation, each one demonstrated under `go test -overlay` rather than
// asserted.
//
// The two shipped tests that come closest state neither claim.
// TestTrailRunComposesWithGateCases (trail_run_outcome_test.go:1148) drives the
// whole chain, but every trailGateCases() row carries trailRunnerUnread(), the
// indeterminate answer. TestTrailGateReadsTheRunnerPathOnlyWhereARowDeclaresIt
// (trailer_admissibility_test.go:1552) does drive all five readings, but only over
// trailGate, and it compares each row AGAINST ITSELF: an edit that moved a Detail
// under all five readings alike passes it untouched.
//
// # The needle is the whole constant answer, and the sweep is over Detail alone
//
// A Detail may legitimately name a runner in fixed prose, so a bare `ptyrunner`
// token is not the defect and is never the needle; an interpolated READING is.
// The needle is the whole string the shipped reader returned.
//
// The sweep is over the Detail STRING and never over the marshalled record.
// trailGateResult carries the reading in its own RunnerPath field by design
// (trailer_admissibility_test.go:364), and clause B of
// TestTrailGateReadsTheRunnerPathOnlyWhereARowDeclaresIt (:1585-1590) requires it
// to arrive intact — so a whole-record sweep for this needle is RED AGAINST A
// CORRECT BUILD. The Detail-only sweep stays the correct rung after #1420: the
// absence arm embeds trailReasonAgainstPath's answer, whose prose interpolates no
// reading, so no arm puts the reading into a Detail.
//
// # Two Details carry the no-echo claim, not three
//
// trailGate is handed the reading directly through trailGateInput.RunnerPath, and
// trailClassifyRun is handed it inside readings.Gate.RunnerPath, because
// trailRunReadings.Gate is the whole trailGateResult (trail_run_outcome_test.go:199).
// trailAdmitAttribution(reap tdnReapOutcome, certified string) is handed NO
// runner path at all, so a byte assertion on its Detail would be green by
// construction whatever that arm did — a rung no mutation can redden. It is
// dropped deliberately, and this paragraph is the reason, stated where a reader
// looking for the missing third assertion will find it.
//
// # Each Detail claim is scoped to one Detail and keyed on a phrase unique to it
//
// The admit and run proof arms close in near-identical prose — both say the group
// was "alive strictly AFTER the trailer was written", and all three arms cite
// runner.go:479-485. A bare shared phrase, or an assertion over a joined or
// concatenated Detail string, would let one deleted sentence redden two rungs and
// leave neither a sole red. Only the em dash in the admit phrase and the comma in
// the run phrase separate the two neighbours.
//
// # The run-side cap-headroom branch
//
// Every Detail here goes through trailDetail, which caps at reachMaxCommandBytes.
// The run-level Detail spends most of that budget, so a later edit that grew its
// prose could push an echo past the cap and leave the no-echo claim passing while
// an echo was being silently truncated away — #1284's cap vacuity, on a margin
// thin enough to be a live risk. It is an `else if` and not a second `if`
// deliberately: any mutation that echoes the reading also eats the headroom, so
// two independent checks would make that mutation redden two assertions and lose
// its sole red. The gate's Detail gets no such branch — its measured slack is
// wide enough that the failure mode is speculative, and an unobserved failure
// mode earns no defence.
func TestTrailComposesUnderAPtyrunnerReading(t *testing.T) {
	const heldPGID = 7788

	// --- the premises, each over this test's own inputs ------------------------

	// Premise. The reading is obtained by DRIVING the shipped reader over an
	// argv, never by re-typing its prose as a literal — a re-typed copy would go
	// on passing after the reader reworded itself. Reddened by a reword of
	// tdnRunnerFromArgv, which sits outside this chain and is out of scope here.
	reading := tdnRunnerFromArgv(tdnFixturePtyArgv)
	if label := finRecordRunnerLabel(reading); label != "ptyrunner" {
		t.Fatalf("premise: the reading %q reduces to label %q, want %q — without that this "+
			"composition is not being driven under a ptyrunner reading at all", reading, label,
			"ptyrunner")
	}

	// Premise. The reading carries no VALUE from the argv it was driven over.
	// This is the first test to drive a real runner argv into
	// trailGateInput.RunnerPath, a field that IS marshalled into the published
	// gate record (trailer_admissibility_test.go:254-289), and the boundary
	// keeping a command string out of it is that tdnRunnerFromArgv returns
	// constant literals and interpolates nothing from its argument. The reader
	// may name the flag --session-id in its answer; it may never echo what
	// follows one. Both literals below are substrings of this repo's own fixture
	// argv, never a capture of anything a run produced.
	for _, value := range []string{"11111111-", "/tmp/s.json"} {
		if strings.Contains(reading, value) {
			t.Fatalf("premise: the reading carries %q, a value out of the argv it was driven "+
				"over. The needle below would then BE an argv-bearing string, so every no-echo "+
				"claim here would go on passing while the published gate record carried a "+
				"command string: %q", value, reading)
		}
	}

	// Premise. The reap record is CLASSIFIED by the real producer rather than
	// typed, which pins the proof arm to a record tdnClassifyReapLog actually
	// emits — TestTrailAdmitAttribution's recipe (trailer_admissibility_test.go:1715)
	// and its reason.
	classified := tdnClassifyReapLog([]byte(trailReapLine(1, "[7788]")+"\n"), heldPGID)
	if classified.Verdict != tdnReapHeldPGIDKilled || classified.LineCount != 1 {
		t.Fatalf("premise: tdnClassifyReapLog gave %s / %d line(s) (%s), want %s / 1 — the "+
			"admissible half of this composition is that the real producer emits this record",
			classified.Verdict, classified.LineCount, classified.Detail, tdnReapHeldPGIDKilled)
	}

	gate := trailGate(trailGateInput{
		Scan:       trailScan([]byte(trailFixtureTrailer + "\n")),
		RunnerPath: reading,
	})

	// Premise. The correct-consumer obligation made explicit, and what keeps ""
	// out of the predicate's contract arm: trailGate fills Reason on exactly
	// trailGateUsable and trailGateBudgetFired, so an uncertified gate is one
	// whose reason the predicate is owed no call with.
	if gate.Reason == "" {
		t.Fatalf("premise: the gate certified no reason (%s: %s), so the attribution predicate "+
			"is owed no call and nothing below would be a statement about the proof "+
			"composition", gate.Value, gate.Detail)
	}

	admit := trailAdmitAttribution(classified, gate.Reason)

	// trailRunProofReadings supplies the REST of the record — the counts,
	// Liveness, PyryExited, BoundFrom and ClaudeState that satisfy contract
	// checks C6-C9. Its Gate and Admit are hand-built ("usable" / "the proof"),
	// and those two fields are exactly what this test requires the real functions
	// to produce, so the copy's pair is overwritten. It is a function rather than
	// a package-level value precisely so a caller may take a copy and vary it;
	// only whole-value assignments happen here, and nothing is written through
	// the Liveness slice or the trailer pointer either copy aliases.
	in := trailRunProofReadings()
	in.Gate = gate
	in.Admit = admit

	out := trailClassifyRun(in)

	// --- the claims ------------------------------------------------------------
	//
	// A gate-VALUE premise is deliberately not among the premises above. Any
	// mutation to trailGate's usable arm's value cascades: a different in-space
	// value trips C5 (trail_run_outcome_test.go:437-443), an out-of-space one
	// trips C1, and either way the run-level outcome moves too — so it could never
	// be the sole red for anything, and it would be an assertion this test owes a
	// mutation for and cannot have. "Reached through trailGateUsable and
	// trailAdmitProof" is asserted instead on the PUBLISHED provenance fields,
	// which is both the published-bytes framing and mutation-discriminable.

	if out.Value != trailOutcomeRunningAtTrailer {
		t.Errorf("run outcome: got %q (%s), want %q — a trailer certifying a reason that is not "+
			"%q, composed with an admissible reap record, is the one path to the finding",
			out.Value, out.Detail, trailOutcomeRunningAtTrailer, trailBudgetTerminalReason)
	}
	if out.Gate != trailGateUsable {
		t.Errorf("gate provenance: got %q, want %q — the published record must name the gate "+
			"value this outcome was reached through, or a reader cannot tell which composition "+
			"produced it", out.Gate, trailGateUsable)
	}
	if out.Admit != trailAdmitProof {
		t.Errorf("admit provenance: got %q, want %q — the other half of the same obligation: an "+
			"outcome that cannot name the attribution it rests on is a verdict without its "+
			"argument", out.Admit, trailAdmitProof)
	}

	// The no-echo pair. The gate is handed the reading directly.
	if strings.Contains(gate.Detail, reading) {
		t.Errorf("the gate's Detail carries the runner reading %q verbatim: %s\nthe reading is "+
			"carried in trailGateResult's own RunnerPath field, which clause B requires to "+
			"arrive intact; a Detail that also interpolates it republishes it", reading, gate.Detail)
	}

	// The classifier is handed the same reading inside readings.Gate.RunnerPath.
	if strings.Contains(out.Detail, reading) {
		t.Errorf("the run-level Detail carries the runner reading %q verbatim: %s\nthe "+
			"classifier is handed the reading inside readings.Gate.RunnerPath, and no arm may "+
			"republish it as prose", reading, out.Detail)
	} else if room := reachMaxCommandBytes - len(out.Detail); room < len(reading) {
		t.Errorf("the run-level Detail is %d of %d bytes, leaving %d for a %d-byte reading: the "+
			"no-echo claim above is UNTESTABLE at this length, because an echo would be "+
			"truncated away by trailDetail's cap rather than found. Shorten that arm's prose "+
			"rather than trusting a claim the cap has made vacuous: %s", len(out.Detail),
			reachMaxCommandBytes, room, len(reading), out.Detail)
	}

	// The three sentences each Detail's argument rests on, one assertion per
	// Detail, each keyed on a phrase that separates that arm from its neighbours.
	const (
		gateSentence  = "a reap-log attribution can be proof"
		admitSentence = "alive strictly AFTER the trailer was written — and therefore alive"
		runSentence   = "The group was alive strictly AFTER the trailer was written, and " +
			"therefore alive when it was written."
	)
	if !strings.Contains(gate.Detail, gateSentence) {
		t.Errorf("the gate's Detail no longer says %q: %s\nthe usable arm certifies a reason on "+
			"the strength of emitter.Close() writing the trailer before the reap defer, and a "+
			"value published without that sentence is a verdict a reader cannot check",
			gateSentence, gate.Detail)
	}
	if !strings.Contains(admit.Detail, admitSentence) {
		t.Errorf("the attribution's Detail no longer says %q: %s\nthe proof arm's whole argument "+
			"is the ORDERING, and an admissibility value published without it asserts what it "+
			"no longer shows", admitSentence, admit.Detail)
	}
	if !strings.Contains(out.Detail, runSentence) {
		t.Errorf("the run-level Detail no longer says %q: %s\nthis is the finding's own statement "+
			"of the ordering, and it is a separate rung from the attribution's because both arms "+
			"close on near-identical prose", runSentence, out.Detail)
	}
}

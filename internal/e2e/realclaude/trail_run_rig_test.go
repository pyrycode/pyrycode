//go:build e2e_realclaude

package realclaude

// The trailer-outcome classifier proven to FLIP inside its own rig, against real
// processes. #1266's observation, #1270's two admissibility results and #1271's
// classifier are each proven against fixtures and synthetic buffers; this file
// closes what fixtures structurally cannot. Everything here runs offline: a real
// FIFO, a real `cat` and a synthetic stdout buffer — no live claude, no
// credentials, no network, no t.Skip.
//
//	go test -race -tags e2e_realclaude -run '^TestTrailRig' -v ./internal/e2e/realclaude/
//
// # What this proves that a fixture cannot
//
// trailClassifyRun is PURE, so it will happily classify inputs gathered from
// somewhere other than where the probe claims to gather them: a rig wired to the
// wrong path emits the same positive as one wired to the right path. What
// separates them is a PRE-SUBJECT NEGATIVE followed by IN-RIG TRANSITIONS —
// gather the classifier's own inputs before the subject exists, then while it
// runs, then after it dies, all through the code path the live probes will use.
// The flip is driven by the ARGV MATCH COUNT (step 8 -> step 7) and never by
// liveness: per-pid verdicts are corroboration and only pinStateInstrumentFailed
// is tested on any arm at all.
//
// # trailOutcomeRunningAtTrailer is deliberately unreachable here
//
// It is returned from step 2 alone, gated on trailAdmitProof, which requires
// pyry's reaper to have named the held group on exactly one anchored line. This
// rig stages no reap line and MUST NOT GROW ONE — synthesising one would fake the
// finding the live probes exist to earn. A live `cat` therefore lands on
// trailOutcomeMatchedUnattributed, the correct and honest answer for a run with a
// match and a silent attribution. tdnClassifyReapLog is called over a literal nil
// INSIDE trailRigGather rather than over a parameter, so "no reap line was
// synthesised" is a property of this file rather than a promise about its call
// sites.
//
// # The two staged values, and why they are the only two
//
// This rig runs no pyry and no claude, so PyryExited and ClaudeState have no
// producer to be gathered from. PyryExited = true because step 3 voids EVERY
// reading below it: left at its zero value both readings would land on
// trailOutcomeVoidPyryDidNotExit, they would AGREE, and the test would look like
// it had disproven the very flip it was built to prove. ClaudeState = "" is C7's
// explicit "not read". Every other field of trailRunReadings comes from a real
// producer, and no tdnReapOutcome, trailGateResult or trailAdmitResult is built
// by struct literal anywhere in this file.
//
// # Failure messages
//
// The rig's t.Fatalf strings may name counts, pids, verdicts, outcome values and
// BoundFrom. They may NEVER name pinScan.Matches (verbatim argv read off the
// ambient process table), a trailObservation's Line, or a trailScanResult's
// trailer. Today that line only ever holds trailFixtureTrailer, a synthetic
// constant; the rule is stated anyway, because this file's value is that a LIVE
// probe can be built on it, and at that point the line holds verbatim model
// output marked operator-review-before-paste.

import (
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// The rig's constants. Every duration is a deadline on a real process or a real
// poll, so each is named rather than typed at its call site.
const (
	// trailRigTrailerWait is the trailer poll's timeout. It exceeds
	// trailRigInducedLateness by ~6x so AC4's deadline arm cannot fire and the
	// observation it records is always a genuine sighting.
	trailRigTrailerWait = 10 * time.Second
	// trailRigInducedLateness is the gap AC4 opens between starting the poll and
	// appending the trailer. Chosen against probePollInterval — the interval the
	// live probes poll at — rather than as a wall-clock round number: the
	// assertion's margin is measured in poll intervals, so the induced gap must
	// be too.
	trailRigInducedLateness = 8 * probePollInterval
	// trailRigStalenessMargin is how far below the induced lateness a
	// miss-derived bound must land. A START-derived staleness is now-start, where
	// now is the successful poll's stamp (at or after the append, by up to one
	// poll interval) and start is stamped inside the wait microseconds after the
	// goroutine was spawned — so the buggy value sits within roughly one poll
	// interval of the induced gap on EITHER side, and a bare `< induced` would
	// pass under the bug whenever scheduling exceeded the post-append poll
	// granularity. A correct miss-derived staleness is at most one poll interval
	// plus a scan, so a threshold two intervals below the induced gap leaves the
	// correct reading ~5 intervals of headroom and the buggy one ~2 intervals the
	// wrong side of the line.
	trailRigStalenessMargin = 2 * probePollInterval
	// trailRigRendezvousWait bounds the wait for the subject to open the FIFO's
	// read end. Matches fifoLiveRendezvousWait.
	trailRigRendezvousWait = 10 * time.Second
	// trailRigDeathWait bounds the poll from "killed" to "reads as a zombie".
	// Kill is asynchronous, so the pre-signal state is expected and polled past;
	// nothing else is.
	trailRigDeathWait = 10 * time.Second
	// trailRigFIFOName is the FIFO's basename inside t.TempDir(). The full PATH is
	// the needle: it is unique per run and no process's command line carries it
	// before the subject execs — including the test binary's own, whose argv is
	// `-test.run=...` and nothing more.
	trailRigFIFOName = "trail-rig-subject"
)

// trailRigHeldPGID is the process group every reading in this rig is classified
// against.
//
// This rig runs no reaper, so no group is "held" in the sense tdnClassifyReapLog
// means. The value only has to be one that function can CLASSIFY: its
// heldPGID <= 1 guard returns tdnReapInstrumentFailed, which would route the
// attribution to trailAdmitVoidInstrument instead of the honest
// trailAdmitVoidNoLine. The test process's own group is always above 1.
//
// Using the SAME group for the pre-subject and during-subject readings is the
// point rather than a convenience: a negative reading must differ from the
// positive one in exactly the dimension under test, and here that dimension is
// the argv match count and nothing else.
func trailRigHeldPGID() int { return syscall.Getpgrp() }

// trailRigGather runs every producer this rig can run and returns the readings
// they emit, plus the trailer observation they were derived from.
//
// The whole rig funnels through this one function, so "no field of
// trailRunReadings is hand-assigned" is checkable in one place rather than
// argued across four call sites. Two parameters, because two things genuinely
// vary across those sites; everything else is a constant of the rig and is
// documented here rather than repeated at each call.
//
// The observation is a RIG-LOCAL intermediate. It is never assigned into
// trailRunReadings — which has no field that could hold it, deliberately, since
// trailObservation embeds trailScanResult and would promote the trailer pointer
// back into reach — and it is never published. AC4 reads its Staleness and
// BoundFrom and nothing else.
//
// The contract block at the top of trailClassifyRun rejects a lazily-built
// input, so three of its nine checks shape this function directly:
//
//   - C2 requires a trailGateUsable value to carry a non-empty Reason. The gate
//     is fed a real trailer line through trailScan, so the reason arrives filled;
//     a hand-built trailGateResult{Value: trailGateUsable} is exactly the fixture
//     C2 exists to reject.
//   - C3/C4 make the Admit guard MANDATORY rather than defensive. C4 rejects a
//     non-empty Admit under a gate that certifies nothing and C3 rejects a zero
//     Admit under one that does, so calling the predicate unconditionally fails
//     C4 on every non-usable gate and skipping it unconditionally fails C3 on
//     every usable one. "No reap log" does not mean "no attribution value".
//   - C8/C9 require the counts to be pinScanArgv's own, which is why nothing here
//     types one in and why the errored flag is derived from the same call.
func trailRigGather(stdout *probeSyncBuffer, needles []string) (trailRunReadings, trailObservation) {
	var readings trailRunReadings

	// The trailer leg. The observation's embedded scan result is REUSED rather
	// than re-scanned, because that is the composition a live probe performs.
	obs := trailWaitForTrailer(stdout, trailRigTrailerWait)
	readings.BoundFrom = obs.BoundFrom
	// The runner path is CONSTANT by construction and FORBIDDEN to close: no
	// caller's needles carry tdnClaudeNeedle, and this rig runs no claude at all
	// (:40) — staging one to be read would change what this rig is.
	// Full reason: trail_ptyrunner_composition_test.go:19-26.
	readings.Gate = trailGate(trailGateInput{Scan: obs.trailScanResult,
		RunnerPath: trailRunnerUnread()})

	// The attribution leg. The nil is a LITERAL here and never a parameter: there
	// is no pyry in this rig, so there is no stderr to pass, and making it
	// un-passable is what makes the no-reap-line prohibition structural.
	// tdnClassifyReapLog reaches tdnReapNoLine honestly over it (LineCount == 0),
	// which trailAdmitAttribution answers with trailAdmitVoidNoLine.
	//
	// The guard is on the gate's certified Reason because that is the exact
	// condition C3 and C4 split on, and trailAdmitAttribution's own contract
	// block rejects an empty certified reason out of hand.
	if readings.Gate.Reason != "" {
		readings.Admit = trailAdmitAttribution(tdnClassifyReapLog(nil, trailRigHeldPGID()),
			readings.Gate.Reason)
	}

	// The argv leg. No exclusions: this rig excludes nothing, and nil says so
	// more clearly than an empty map. pinScanArgv returns the ZERO pinScan on
	// error, so the counts and the flag are consistent by construction — which is
	// what C9 checks.
	scan, err := pinScanArgv(needles, nil)
	readings.ArgvScanErrored = err != nil
	readings.MatchCount = scan.MatchCount
	readings.RowsScanned = scan.RowsScanned

	// One per-pid read per MATCHED pid, taken here rather than at one call site,
	// so AC5's "Liveness holds a read for each" is true by construction of the
	// gather rather than by a loop a later call site could forget.
	for _, match := range scan.Matches {
		readings.Liveness = append(readings.Liveness, pinReadState(match.PID))
	}

	// The only two staged values in this file. See the header for why the first
	// is not cosmetic.
	readings.PyryExited = true // no pyry runs here, so there is none to fail to exit
	readings.ClaudeState = ""  // no claude runs here; C7 admits "" as "not read"

	return readings, obs
}

// TestTrailRigFlipsAcrossOneSubjectLifetime is the rig's actual claim: the
// classifier's inputs, gathered through the live path, produce DIFFERENT outcomes
// before and during one real subject's life.
//
// A rig wired to the wrong path would produce the same answer at both readings,
// which is why the pre-subject reading is taken at all and why it must be a
// genuine negative rather than a void. Both dead readings are then taken, because
// the per-pid read's zombie/reaped pair exists precisely because collapsing it
// makes every zombie read as running — and here both states are deterministically
// producible, the subject being a direct child of the test process.
func TestTrailRigFlipsAcrossOneSubjectLifetime(t *testing.T) {
	fifoPath := filepath.Join(t.TempDir(), trailRigFIFOName)
	needles := []string{fifoPath}

	// Step 1 of trailClassifyRun answers before any argv step, so a rig whose
	// gate is not usable short-circuits to a void on BOTH readings and the flip
	// never happens. trailFixtureTrailer is the right seed precisely because it
	// is ORDINARY: a budget-fired terminal reason would divert step 1 to
	// trailOutcomeVoidBudgetFired instead.
	var stdout probeSyncBuffer
	if _, err := stdout.Write([]byte(trailFixtureTrailer + "\n")); err != nil {
		t.Fatalf("seed the rig's stdout buffer with the trailer fixture: %v", err)
	}

	// Creates the FIFO and holds its WRITE end for the whole test, so the
	// subject's block is attributable to the subject and the FIFO's later EOF is
	// attributable to the cleanup.
	rendezvous := holdProbeFIFO(t, fifoPath)

	// --- the pre-subject reading ---

	before, _ := trailRigGather(&stdout, needles)

	// Asserted FIRST: an errored scan lands on trailOutcomeVoidArgvScanErrored,
	// and treating that void as this rig's negative would be the rig proving
	// nothing while looking green.
	if before.ArgvScanErrored {
		t.Fatalf("the pre-subject argv scan errored as an instrument, so the reading this rig "+
			"calls a genuine negative is %s instead — a void the rig is not entitled to read as "+
			"an answer about the match set", trailOutcomeVoidArgvScanErrored)
	}
	// What separates the negative from trailOutcomeVoidNoRowsParsed: rows were
	// scanned and none of them matched, rather than nothing having been read.
	if before.RowsScanned <= 0 {
		t.Fatalf("the pre-subject argv scan parsed %d well-formed row(s); want more than 0 — with "+
			"none the reading is %s, a nothing-was-measured, and not the genuine negative this "+
			"rig's flip starts from", before.RowsScanned, trailOutcomeVoidNoRowsParsed)
	}
	if before.MatchCount != 0 {
		t.Fatalf("the pre-subject argv scan matched %d of %d row(s) although no subject has been "+
			"launched; want 0 — a needle that matches before its subject exists cannot show the "+
			"rig is reading the subject", before.MatchCount, before.RowsScanned)
	}
	// The gate and attribution legs genuinely ran through their producers: the
	// flip below must not be an accident of a short-circuiting void.
	// "completed" is trailFixtureTrailer's own terminal_reason, spelled as this
	// package spells it everywhere else. Pinning the value rather than merely
	// non-emptiness is what keeps a seed edit that fired the budget arm from
	// passing here and then diverting step 1 to trailOutcomeVoidBudgetFired.
	if before.Gate.Value != trailGateUsable || before.Gate.Reason != "completed" {
		t.Fatalf("the pre-subject gate reads %q certifying reason %q; want %q certifying "+
			"\"completed\" — without a usable trailer there is no certified instant for either "+
			"reading to be about and step 1 answers both of them identically",
			before.Gate.Value, before.Gate.Reason, trailGateUsable)
	}
	if before.Admit.Value != trailAdmitVoidNoLine {
		t.Fatalf("the pre-subject attribution reads %q; want %q — this rig stages no reap line, so "+
			"the honest attribution is the no-line void, and any other value means the "+
			"attribution leg did not run over the reap-line-free buffer it claims to",
			before.Admit.Value, trailAdmitVoidNoLine)
	}

	beforeOutcome := trailClassifyRun(before)
	if beforeOutcome.Value != trailOutcomeNoRowMatched {
		t.Fatalf("the pre-subject readings classify as %q (%s); want %q", beforeOutcome.Value,
			beforeOutcome.Detail, trailOutcomeNoRowMatched)
	}

	// --- the subject ---

	cmd := exec.Command(fifoLiveReaderCommand, fifoPath)
	// The subject inherits nothing. Under `make e2e-realclaude` this process may
	// carry CLAUDE_CODE_OAUTH_TOKEN / ANTHROPIC_API_KEY, and exec.Command passes
	// the parent environment through by default. Nothing in this rig reads any
	// process's environment — both ps reads use explicit column lists — so this
	// is defence in depth, and it costs nothing: `cat` needs no environment, and
	// exec.Command has already resolved the binary from the PARENT's PATH.
	cmd.Env = []string{}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s on the rig's FIFO: %v", fifoLiveReaderCommand, err)
	}
	subjectPID := cmd.Process.Pid

	select {
	case <-rendezvous:
		// Fires the instant the subject opens the read end, which is also what
		// guarantees it has execed and carries the needle in its argv.
	case <-time.After(trailRigRendezvousWait):
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("the rendezvous never fired within %s although the subject was started; the "+
			"subject is not known to be blocked on the FIFO, so no reading taken here would be "+
			"about a live subject", trailRigRendezvousWait)
	}

	// The rendezvous proves a reader ARRIVED; this proves one is STILL holding
	// the read end at scan time. Safe only because holdProbeFIFO's persistent
	// write end means this read's transient second write end is not the last one
	// closing.
	if live := fifoLiveRead(fifoPath); live.Verdict != fifoLiveReaderPresent {
		t.Fatalf("the FIFO reads %q at scan time; want %q — the subject is not holding the read "+
			"end, so the during-subject reading below would not be about a live subject",
			live.Verdict, fifoLiveReaderPresent)
	}

	// --- the during-subject reading ---

	during, _ := trailRigGather(&stdout, needles)

	if during.ArgvScanErrored || during.RowsScanned <= 0 {
		t.Fatalf("the during-subject argv scan reports errored=%t across %d row(s); want a clean "+
			"scan over more than 0 rows — otherwise the reading is a void and says nothing about "+
			"the subject", during.ArgvScanErrored, during.RowsScanned)
	}
	if during.MatchCount <= 0 {
		t.Fatalf("the during-subject argv scan matched %d of %d row(s) while the subject is "+
			"blocked on the FIFO; want more than 0 — a rig that cannot see the subject it is "+
			"looking straight at is not reading the subject at all",
			during.MatchCount, during.RowsScanned)
	}
	if len(during.Liveness) != during.MatchCount {
		t.Fatalf("the during-subject readings carry %d per-pid read(s) for %d matched row(s); "+
			"want one read per matched pid", len(during.Liveness), during.MatchCount)
	}

	duringOutcome := trailClassifyRun(during)
	if duringOutcome.Value != trailOutcomeMatchedUnattributed {
		t.Fatalf("the during-subject readings classify as %q (%s); want %q — a match with a "+
			"silent attribution is the honest answer here, because this rig stages no reap line "+
			"and %q is therefore unreachable", duringOutcome.Value, duringOutcome.Detail,
			trailOutcomeMatchedUnattributed, trailOutcomeRunningAtTrailer)
	}

	// The rig's actual claim, stated as its own assertion rather than left as an
	// inference from the two above.
	if beforeOutcome.Value == duringOutcome.Value {
		t.Fatalf("the pre-subject and during-subject readings both classify as %q; the outcome "+
			"does not flip across the subject's arrival, so the rig is not demonstrably reading "+
			"the subject rather than a constant", beforeOutcome.Value)
	}

	// --- the two dead readings ---

	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill the subject: %v", err)
	}

	// The argv match set is deliberately NOT asserted at either dead reading: ps
	// renders a zombie's command= column without the original argv (`<defunct>`,
	// `[cat] <defunct>`), and which of those a platform prints is not this
	// ticket's claim.
	var zombie pinStateOutcome
	deadline := time.Now().Add(trailRigDeathWait)
poll:
	for {
		zombie = pinReadState(subjectPID)
		switch zombie.Verdict {
		case pinStateExitedNotReaped:
			break poll
		case pinStateInstrumentFailed:
			// Retrying past an instrument failure is exactly the spin-until-green
			// shape that turns a broken instrument into a pass.
			t.Fatalf("the per-pid read for the killed subject (pid %d) failed as an instrument: "+
				"%s. Not retried: a poll that spins past an instrument failure turns the "+
				"instrument's own breakage into a pass", subjectPID, zombie.Detail)
		case pinStateNoSuchProcess:
			// The test is the parent and has not waited, so this is unproducible;
			// reaching it means the rig has lost the parent relationship its whole
			// zombie claim rests on.
			t.Fatalf("the per-pid read for the killed subject (pid %d) reads %q before this test "+
				"waited on it, which its parent relationship makes unproducible — something "+
				"else reaped the subject and the zombie reading below would be about nothing",
				subjectPID, pinStateNoSuchProcess)
		}
		// pinStateRunning: Kill is asynchronous, so the pre-signal state is the
		// one state worth polling past.
		if !time.Now().Before(deadline) {
			t.Fatalf("the per-pid read for the killed subject (pid %d) still reads %q after %s; "+
				"want %q — the killed-but-unreaped state is what keeps a zombie from reading as "+
				"running", subjectPID, zombie.Verdict, trailRigDeathWait, pinStateExitedNotReaped)
		}
		time.Sleep(probePollInterval)
	}

	// A killed process yields *exec.ExitError; that is the expected result here,
	// so nothing is asserted about it. Wait is synchronous with reaping, so the
	// read below is taken ONCE — a retry loop could only hide a defect.
	_ = cmd.Wait()

	reaped := pinReadState(subjectPID)
	if reaped.Verdict != pinStateNoSuchProcess {
		t.Fatalf("the per-pid read for the reaped subject (pid %d) reads %q; want %q — the read "+
			"does not separate a reaped process from an unreaped one, and ps lists a zombie as a "+
			"row, so without the separation every zombie reads as running",
			subjectPID, reaped.Verdict, pinStateNoSuchProcess)
	}
	if zombie.Verdict == reaped.Verdict {
		t.Fatalf("both dead readings for pid %d report %q; the two are collapsed, which is the "+
			"exact distinction this rig takes both readings to exercise",
			subjectPID, zombie.Verdict)
	}
	for _, dead := range []pinStateOutcome{zombie, reaped} {
		if dead.Verdict == pinStateRunning || dead.Verdict == pinStateInstrumentFailed {
			t.Fatalf("a dead reading for pid %d reports %q; neither dead reading may be %q "+
				"(the failure direction that makes a killed subject read as live) nor %q (an "+
				"absence dressed as a reading)", subjectPID, dead.Verdict, pinStateRunning,
				pinStateInstrumentFailed)
		}
	}
}

// trailRigGathered is one gather's two return values, carried over a channel so
// AC4 can run the gather concurrently with the append that ends its wait.
type trailRigGathered struct {
	readings trailRunReadings
	obs      trailObservation
}

// TestTrailRigBoundIsMeasuredFromTheLastMiss proves the lateness bound is
// measured from the last NON-MATCHING poll inside the composed rig, not merely
// inside the helper.
//
// This deliberately restates an assertion #1266 already makes against a synthetic
// buffer in isolation. That is the point rather than a duplication to trim: a
// bound that holds in the helper says nothing about a rig that CALLS the helper
// wrongly, and the rig is what the live probes will inherit.
//
// No FIFO and no subject — the needle matches nothing, which keeps this test's
// claim about the bound and about nothing else.
func TestTrailRigBoundIsMeasuredFromTheLastMiss(t *testing.T) {
	// A path unique to this run with no process staged under it, so the gather's
	// argv leg runs for real and matches nothing.
	needles := []string{filepath.Join(t.TempDir(), trailRigFIFOName)}

	var stdout probeSyncBuffer

	// Buffered at 1 so the goroutine cannot leak on a send if this test has
	// already failed and returned.
	results := make(chan trailRigGathered, 1)
	spawnedAt := time.Now()
	go func() {
		readings, obs := trailRigGather(&stdout, needles)
		results <- trailRigGathered{readings: readings, obs: obs}
	}()

	// The poll is now running over an EMPTY buffer, so every iteration until the
	// append below is a genuine miss and the bound it records is derived from
	// one.
	time.Sleep(trailRigInducedLateness)
	appendedAt := time.Now()
	if _, err := stdout.Write([]byte(trailFixtureTrailer + "\n")); err != nil {
		t.Fatalf("append the trailer fixture mid-poll: %v", err)
	}

	got := <-results
	induced := appendedAt.Sub(spawnedAt)

	if got.obs.State != trailSeen {
		t.Fatalf("the trailer poll ended in state %q; want %q — the wait timed out rather than "+
			"seeing the appended trailer, so no bound was measured at all", got.obs.State,
			trailSeen)
	}
	// The deterministic half: a start-derived reading reports trailBoundFromStart
	// and fails here outright, before any duration is compared.
	if got.obs.BoundFrom != trailBoundFromMiss {
		t.Fatalf("the observation reports its bound measured from %q; want %q — a bound measured "+
			"from the loop's start bounds NOTHING, because the trailer may have become visible "+
			"before the loop began", got.obs.BoundFrom, trailBoundFromMiss)
	}
	if want := induced - trailRigStalenessMargin; got.obs.Staleness >= want {
		t.Fatalf("the recorded staleness is %s against an induced lateness of %s; want strictly "+
			"less than %s — a bound at or above the induced gap is the start-derived reading "+
			"this assertion exists to catch, and the margin is what keeps scheduling noise from "+
			"letting that reading pass", got.obs.Staleness, induced, want)
	}

	// Free corroboration: the composed gather emits a contract-legal record on
	// the miss path too, not only on the pre-seeded one.
	outcome := trailClassifyRun(got.readings)
	if !trailIsRunOutcome(outcome.Value) || outcome.Value == trailOutcomeOutOfContract {
		t.Fatalf("the miss-path readings classify as %q (%s); want one of the recorded outcomes "+
			"and not %q — the gather assembled a record its own producers cannot have emitted",
			outcome.Value, outcome.Detail, trailOutcomeOutOfContract)
	}
}

// TestTrailRigCarriesMoreThanOneMatchedRow proves a match count above one is
// carried through without resolving "the" pid.
//
// The subject is launched through a shell so that TWO rows carry the needle: the
// wrapper, whose argv holds the path as a positional parameter, and the `cat` it
// forks. Matching is against the WHOLE command line (reachMatchArgvRows), which
// is what makes both rows hits.
//
// A separate staging from the flip test rather than one rig serving both: here
// the subject is a GRANDCHILD, so cmd.Wait on the shell does not reap it and the
// zombie/reaped pair would not be deterministically producible. Splitting keeps
// each claim exact instead of making one rig serve two incompatible parent
// relationships.
func TestTrailRigCarriesMoreThanOneMatchedRow(t *testing.T) {
	fifoPath := filepath.Join(t.TempDir(), trailRigFIFOName)
	needles := []string{fifoPath}

	var stdout probeSyncBuffer
	if _, err := stdout.Write([]byte(trailFixtureTrailer + "\n")); err != nil {
		t.Fatalf("seed the rig's stdout buffer with the trailer fixture: %v", err)
	}

	rendezvous := holdProbeFIFO(t, fifoPath)

	// Both operands are resolved in the PARENT and passed as positional
	// parameters, so the command string below is a compile-time constant with no
	// interpolation site at all: no shell metacharacter in either path is ever
	// interpreted. The obvious alternative — fmt.Sprintf("cat %s; exit 0", path)
	// — is a shell-injection shape even though today's only input is t.TempDir().
	// ps still prints both as argv of the shell, so the needle still matches its
	// row.
	catPath, err := exec.LookPath(fifoLiveReaderCommand)
	if err != nil {
		t.Fatalf("resolve %s in the parent's PATH: %v", fifoLiveReaderCommand, err)
	}
	// `exit 0` after the command is what defeats the exec-optimisation: sh, dash,
	// bash and zsh all replace themselves with a SINGLE simple command, leaving
	// one process and one matching row. With a builtin after it the command is no
	// longer last and must be forked.
	cmd := exec.Command("sh", "-c", `"$1" "$2"; exit 0`, "sh", catPath, fifoPath)
	cmd.Env = []string{} // see the flip test's subject for why
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the shell wrapper on the rig's FIFO: %v", err)
	}
	// A defer rather than a t.Cleanup, and the ordering is load-bearing:
	// t.Cleanup runs LIFO AFTER the test body, so a cleanup registered here would
	// run BEFORE holdProbeFIFO's release and block forever waiting for an EOF
	// only that later cleanup can deliver. Killing the GROUP is what reaches the
	// grandchild — cmd.Process.Kill() on the shell does not.
	pgid := cmd.Process.Pid
	defer func() {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		_ = cmd.Wait()
	}()

	select {
	case <-rendezvous:
		// Fires when the `cat` opens the read end, which is also what guarantees
		// it has execed and carries the needle in its argv.
	case <-time.After(trailRigRendezvousWait):
		t.Fatalf("the rendezvous never fired within %s although the shell wrapper was started; "+
			"the second needle-carrying row is not known to exist", trailRigRendezvousWait)
	}

	readings, _ := trailRigGather(&stdout, needles)

	if readings.ArgvScanErrored {
		t.Fatalf("the argv scan errored as an instrument, so no match set exists to count")
	}
	if readings.MatchCount <= 1 {
		t.Fatalf("the argv scan matched %d of %d row(s) with a shell wrapper and its forked "+
			"child both carrying the needle; want more than 1 — the scan deliberately refuses to "+
			"resolve \"the\" pid, and a rig that never produces a plural match set never "+
			"exercises that refusal", readings.MatchCount, readings.RowsScanned)
	}
	// The classifier deliberately has no alignment check between len(Liveness)
	// and MatchCount — it never indexes one against the other — so the rig is
	// where a read per matched pid is pinned.
	if len(readings.Liveness) != readings.MatchCount {
		t.Fatalf("the readings carry %d per-pid read(s) for %d matched row(s); want one read per "+
			"matched pid", len(readings.Liveness), readings.MatchCount)
	}
	for _, read := range readings.Liveness {
		if !pinIsVerdict(read.Verdict) {
			t.Fatalf("the per-pid read for pid %d carries verdict %q, which is not one of the "+
				"four pinReadState documents", read.PID, read.Verdict)
		}
		if read.Verdict == pinStateInstrumentFailed {
			t.Fatalf("the per-pid read for pid %d failed as an instrument: %s. Every matched pid "+
				"is a live process this test staged, so an instrument failure here means the "+
				"read did not run rather than that the process was gone", read.PID, read.Detail)
		}
	}

	outcome := trailClassifyRun(readings)
	if outcome.Value != trailOutcomeMatchedUnattributed {
		t.Fatalf("the plural-match readings classify as %q (%s); want %q — a match count above "+
			"one must not change the outcome, because the scan resolves no single pid for it to "+
			"change to", outcome.Value, outcome.Detail, trailOutcomeMatchedUnattributed)
	}
}

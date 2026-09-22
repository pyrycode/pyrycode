//go:build e2e_realclaude

package realclaude

// The live entry point for the HEADLESS STREAM PATH: does `pyry agent-run` under
// PYRY_USE_STREAMJSON=1 reach its normal exit path — trailer written, process
// returned — while a command that run launched is still running?
//
// THAT PATH AND ONLY THAT PATH. The ptyrunner default is #1337's, recorded live
// against claude 2.1.220 on 2026-08-06, and a finding that does not name its path
// names nothing: the two do not write the same trailer and do not present the same
// process tree at trailer time. Here the trailer is claude's OWN `result` line —
// streamrunner's Run tees claude's stdout for the watchdog and
// passes the bytes through unchanged — claude exits before the observation window,
// and pyry reaps nothing on a clean exit.
//
// # It composes and publishes; it derives nothing
//
// #1340's finLiveRunStage stages the turn under #1349's
// finLiveStageStreamEnvDelta. finGatherReadings →
// finStreamCertifyOrdering → finExitClassify → finTrailerBuild →
// finRecordBuild → finWriteArtifacts turn its handle into
// a publishable record. Every one of those is shipped and offline-proven, and every
// outcome they return is consumed AS RETURNED: not re-derived, not renamed, not
// cross-checked into a new verdict. finExitClassify is REUSED from #1337's probe
// rather than copied, and the per-group attribution fan-out is called INSIDE the
// gather and is not called again here.
//
// # Why the sighting route is the evidence, and why the reap log is absent
//
// THE SIBLING FILE'S CENTRAL ARGUMENT IS NOT MERELY DIFFERENT HERE, IT IS
// INVERTED, and copying it across would ship a false claim. #1337 rests its
// verdict on pyry's own reap log, and treats point-in-time reads as systematically
// late. On this path there is no reap log to rest on: streamrunner's Run reaps only
// inside its cmd.Cancel, whose own comment records that it never fires on a clean
// exit. So a healthy run writes NO reap line at all and the attribution leg is
// EMPTY BY CONSTRUCTION rather than merely late — a DIFFERENT evidence class, not a
// weaker version of the same one.
//
// What stands in its place is #1439's ordering argument, consumed through #1440's
// pinned-pid route. THE ORDERING IS: the trailer's appearance on pyry's stdout
// precedes pyry's exit, which precedes the pinned-pid re-read. The rig's own POLL
// is later than all three, and that is not a defect in the argument — the premise
// is that the trailer WAS SIGHTED, and bytes reach h.Stdout only while pyry is
// alive, so a trailer the gather finds in the buffer after pyry exited was
// necessarily written before pyry exited.
//
// # What this path's evidence cannot say
//
// NO TERMINAL REASON IS CERTIFIED HERE. Claude writes terminal_reason only on an
// error stop such as `--max-turns` (#1388), and pyry writes it only on the
// streamrunner's idle-stall trailer; claude's `result` line on a healthy run
// carries no such key, so the gate reads trailGateAbsentOwesNone rather than
// certifying anything.
// With nothing certified there is no declared-finished instant, so
// trailOutcomeRunningAtTrailer is NOT REACHABLE from this composition and MUST NOT
// BE APPROXIMATED IN PROSE. The strongest claim available here is about the
// trailer's SIGHTING, which is what trailOutcomeAliveAtSightingByOrdering names.
// That gap between the two paths' strongest claims is a finding to report, not a
// shortfall to close by wording.
//
// # Captured-bytes discipline
//
// ARTIFACTS GO INTO A PUBLIC ISSUE. NO `ps -E` AND NO `-Eww`, anywhere: those dump
// the operator's CLAUDE_CODE_OAUTH_TOKEN and ANTHROPIC_API_KEY. This rig needs no
// environment read of any process at all and runs no ps of its own — pinReadState's
// pid-pinned lookup is `pid=,ppid=,stat=` by construction (`pinStateColumns`) and
// is the only one it reaches. The pinned-pid re-check is a lookup for ONE known
// pid and is not a reason to persist a full-table command column.
//
// It HOLDS verbatim argv (h.Pin.Rows[i].Command, h.Pin.ClaudeCommand) and forwards
// both to finRecordBuild UNEXAMINED AND UNFORMATTED, which reduces the rows to three
// integers each and the claude argv to one of tdnRunnerFromArgv's constants.
// Neither is read, logged or interpolated here.
//
// The raw trailer line is not published and is not REACHABLE: finSighting carries
// neither trailScanResult.Line nor the *resultTrailer, so the prohibition holds by
// the shape of the gather's third return rather than by a discipline. Every value
// the log channel carries is enumerated with its own safety argument at step 7.
//
// # It asserts almost nothing
//
// t.Fatalf fires only on structural failure, and this file adds exactly ONE of its
// own: os.MkdirTemp failing. Three abort paths are INHERITED from finLiveRunStage
// and are not re-guarded here — pyry never spawning a claude child, no system/init
// session id, and ReadJSONL's fatal on a transcript it cannot open or
// parse, reached through the assembly. Every other failure mode is RECORDED,
// because a probe that turns an unexpected reading into a red test loses the
// reading.
//
// # One skip gate, not two
//
// finStreamExitEnableEnv follows reachEnableEnv's shape
// (`TestRealClaude_BackgroundReachability`), so `make e2e-realclaude` skips this
// probe by default and a skip is the NORMAL outcome, saying nothing about pyry's
// behaviour.
//
// The reach probe's second gate — an ambient PYRY_USE_STREAMJSON check — is
// deliberately NOT copied, for the reason finLiveRunStage's SITE A already records:
// this run's delta NAMES PYRY_USE_STREAMJSON EXPLICITLY and os/exec resolves a
// duplicated variable in favour of the later entry, so the ambient loses either
// way. The observed-path reading at step 4 is the real guard and is strictly
// stronger than an env check.
//
// # This file ships ONE offline test, and the entry point is not it
//
// The entry point needs a live claude and a credentialed worktree; its exercise
// under `make e2e-realclaude` is compilation and `go test`'s vet subset. Note that
// `go vet` and `staticcheck` in `make check` run WITHOUT -tags e2e_realclaude, so
// neither analyses this file; `make e2e-realclaude` is the gate.
//
// The one thing worth trapping is finStreamCertifyOrdering: it is the only decision
// this ticket introduces that is pure and offline-drivable, and its third argument
// is the premise a wrong answer would publish a false claim from.

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

// --- the constants ------------------------------------------------------------------

// finStreamExitEnableEnv gates the live probe, following reachEnableEnv's shape.
//
// DELIBERATELY NOT PYRY_PROBE_EXIT_PATH_STREAM. A name carrying another gate's name
// as a prefix makes a grep for either report both, and these are TWO GATES rather
// than one: an operator must be able to spend one live turn on one path without
// spending two. finExitEnableEnv is not a substring of this
// name, and this name is not a substring of it.
const finStreamExitEnableEnv = "PYRY_PROBE_STREAM_EXIT_PATH"

// finStreamExitPyryExitDeadline bounds the wait for pyry to exit ON ITS OWN with the
// FIFO write end STILL HELD.
//
// IT IS THIS FILE'S OWN CONSTANT AND IT IS NOT finExitPyryExitDeadline, whose own
// doc calls itself that file's own constant for the same reason. Same value and the
// same asymmetry argument — too short manufactures a trailOutcomeVoidPyryDidNotExit
// on a HEALTHY run and costs an operator a live round-trip, where too long costs
// only wall-clock on a run that has already failed — and SEPARATE so that re-tuning
// one path cannot silently move the other. Same doctrine
// finLiveStageStreamEnvDelta follows against finLiveStageEnvDelta.
//
// ONE LEG IS GENUINELY PATH-SPECIFIC, and it is why a shared constant would be
// wrong even at equal values. On ptyrunner claude's stdin/stdout are the tty, so
// pyry's own cmd.Wait on claude waits on process exit alone. Here streamrunner sets
// cmd.Stdout to a non-*os.File parser, so os/exec creates a pipe and that Wait
// blocks until every dup of claude's stdout write end is closed — including any
// held by the detached Bash group this probe keeps un-finishable. It is BOUNDED by
// streamrunner's WaitDelay rather than unbounded; see § Error handling on
// finStreamExitRunProbe for what a non-zero exit code means if it fires.
const finStreamExitPyryExitDeadline = 120 * time.Second

// --- the one decision this file introduces -------------------------------------------

// finStreamCertifyOrdering certifies #1439's three ordering premises for a run
// staged by this rig, from the trailer scan's own state and pyry's exit.
//
// THE PARAMETER IS THE SCAN STATE AND NEVER A finSighting. That is the same
// narrow-the-parameter enforcement trailCertifyOrdering makes for its own three
// bools: a finSighting carries TerminalReason, StopReason, Subtype and KeyNames,
// none of which this decision needs and none of which a forbidden-key denylist
// would catch. With the input pinned to a closed-space string and a bool, this
// function can see no captured byte at all.
//
// # The third argument is STRUCTURAL and is not a guess
//
// holdProbeFIFO keeps the write end, hands its caller a receive-only channel with
// no release path of its own, and closes its release channel only in the t.Cleanup
// IT REGISTERS ITSELF, which by construction runs after the subtest body returns.
// finStreamExitRunProbe registers no cleanup at all and takes its wait on pyry's
// exit IN THE BODY, so the hold is held for the whole of that wait BY
// CONSTRUCTION. trailCertifyOrdering's own doc sanctions exactly this: a caller
// passing holdHeld=true is asserting something it can know.
//
// GETTING IT WRONG WOULD PUBLISH A FALSE CLAIM, which is the threat
// finGatherInputs.Ordering's doc leaves open and names: a probe that GUESSED the
// premise would publish trailOutcomeAliveAtSightingByOrdering on a run where the
// hold had been released, and a released hold means the pinned pid could have been
// retired and reissued between the sighting and the later read — so the artifact
// would name a process it cannot show is the same one. That is the case
// trailOrderVoidUnheld exists to refuse, and it is why that void outranks the other
// two premises.
//
// Pure over its input: no exec, no clock, no filesystem, no *testing.T, and it
// never fails a test — the contract every predicate in this family keeps.
func finStreamCertifyOrdering(sightingState string, pyryExited bool) trailOrderResult {
	// == trailSeen and never != "": trailAbsent and trailAborted are both non-empty
	// members of the scan's own state space, and a run whose scan ABORTED sighted no
	// trailer either.
	return trailCertifyOrdering(sightingState == trailSeen, pyryExited, true)
}

// --- the entry point ------------------------------------------------------------------

// TestRealClaude_StreamExitPathWhileCommandRuns stages one live turn on the
// headless PYRY_USE_STREAMJSON=1 path, waits for pyry to exit WITH THE HELD COMMAND
// STILL UNFINISHABLE, and records whether that command was still running when the
// trailer was sighted on pyry's stdout.
//
// NO t.Parallel: finLiveRunStage reaches WithWorktreeAuthenticated, which calls
// t.Setenv, and Go's runtime refuses that pairing. That helper also SKIPS when
// neither ANTHROPIC_API_KEY nor CLAUDE_CODE_OAUTH_TOKEN is set — the credential
// skip, separate from the opt-in gate below.
func TestRealClaude_StreamExitPathWhileCommandRuns(t *testing.T) {
	if os.Getenv(finStreamExitEnableEnv) != "1" {
		t.Skipf("#1353 stream exit-path probe: skipped because %s != 1.\n"+
			"This is an EVIDENCE PROBE, not a regression gate — a skip here is the "+
			"normal `make e2e-realclaude` outcome and says nothing about pyry's "+
			"behaviour. It costs one live claude turn. The probe sets "+
			"BASH_DEFAULT_TIMEOUT_MS=5000 and PYRY_USE_STREAMJSON=1 on the pyry "+
			"process itself (#1349's env delta), so no extra environment is needed. "+
			"The -timeout accommodates this file's %s alongside the driver's own "+
			"deadlines and the gather's %s trailer wait:\n"+
			"  %s=1 go test -tags e2e_realclaude -timeout 15m -v \\\n"+
			"    -run 'TestRealClaude_StreamExitPathWhileCommandRuns' ./internal/e2e/realclaude/",
			finStreamExitEnableEnv, finStreamExitPyryExitDeadline, finGatherTrailerWait,
			finStreamExitEnableEnv)
	}

	// Deliberately NOT t.TempDir(): that is removed when the test ends, and the
	// operator needs these files afterwards to compose the issue comment.
	artifactDir, err := os.MkdirTemp("", "pyry-1353-stream-probe-*")
	if err != nil {
		t.Fatalf("create artifact dir: %v", err)
	}
	t.Logf("#1353 probe artifacts: %s", artifactDir)
	finStreamExitRunProbe(t, artifactDir)
}

// finStreamExitRunProbe takes the handle, does the reading, the certification, the
// classification and the publication. THE ORDER IS LOAD-BEARING AT FOUR POINTS,
// each called out below. Straight-line: no early return and no disposition of its
// own.
//
// # Error handling — two path-specific hazards, RECORDED and never repaired
//
// h.ExitStatus MAY BE NON-ZERO ON A CORRECT, COMPLETE RUN. streamrunner sets
// cmd.Stdout to a non-*os.File writer, so pyry's own cmd.Wait on claude blocks
// until every dup of claude's stdout write end is closed, including any held by the
// detached Bash group this rig keeps alive. It is bounded by streamrunner's
// WaitDelay; if that fires, Run returns the wait error and runAgentRun maps it to a
// non-zero exit. PyryExited still closes, and well inside the deadline above.
// WHETHER IT FIRES IS NOT DECIDABLE OFFLINE AND THIS RUN IS THE FIRST MEASUREMENT —
// finLiveRunStage's SITE B says so and names this ticket. A non-zero exit_code on
// an otherwise healthy run IS THIS READING, not a defect, and it changes no
// verdict: the discriminator is the trailer and the ordering, never the exit
// status.
//
// A terminal_reason PRESENT on this path means pyry's idle-stall watchdog fired and
// synthesised the trailer. The gate answers trailGatePresentOwesNone and the
// classifier trailOutcomeVoidReasonNotOwedByPath. Not a finding about the exit
// path; a run to re-take.
func finStreamExitRunProbe(t *testing.T, artifactDir string) {
	// 1. Stage, under the STREAM delta. Everything after this runs with the FIFO
	// write end STILL HELD: holdProbeFIFO releases only in the t.Cleanup it registers
	// itself, which by construction runs after this body returns. This rig registers
	// no cleanup at all, so it cannot disturb the driver's LIFO ordering — the driver
	// registers its defence-in-depth SIGKILL BEFORE the hold, so LIFO releases the
	// FIFO first and pyry gets a real chance to exit on its own.
	//
	// THIS IS AN INVERSION OF THE NEIGHBOURING PRECEDENT AND NOT A COPY OF IT:
	// runReachProbe releases the FIFO first, deliberately, so pyry can finish. Here
	// the hold must outlast the wait, because a wait taken after the release would
	// measure the RIG's release rather than pyry's exit.
	h := finLiveRunStage(t, finLiveStageStreamEnvDelta())

	// 2. Wait for pyry's own exit — IN THE BODY, NEVER IN A CLEANUP.
	//
	// A channel receive and never a pinReadState on h.PyryPID: pyry is a direct child
	// of the test binary, so between exit and Wait it is a zombie, and a zombie
	// answers Signal(0) with nil — a liveness probe would report "still running" for
	// an already-returned pyry.
	pyryExited := false
	exitCode := pinExitStatusUnknown
	select {
	case <-h.PyryExited:
		pyryExited = true
		// THE READ SITS INSIDE THE ARM, AND THAT IS NOT A STYLE CHOICE. The close
		// of that channel is the ONLY happens-before edge between the driver's
		// cmd.Wait goroutine writing ExitStatus and this read. Any shape that
		// evaluates h.ExitStatus outside the receive — hoisting it above the
		// select, or passing it to a helper alongside the bool — reads the field
		// concurrently with that goroutine and is a data race `go test -race` will
		// flag. There is deliberately no such helper.
		exitCode = h.ExitStatus
	case <-time.After(finStreamExitPyryExitDeadline):
		// The unexited path simply leaves pinExitStatusUnknown in place, which is
		// what keeps a non-exiting run from publishing exit_code: 0 — a value
		// finRecordRun.ExitCode documents as A REAL SUCCESSFUL EXIT.
	}

	// 3. The claude-still-alive corroboration. pinReadState's Verdict and NOTHING
	// ELSE — never a raw ps column, because this value crosses the gather unvalidated,
	// is republished as claude_state and is quoted into a published Detail. ClaudePID
	// is guaranteed positive: the driver fatals on 0.
	//
	// ITS MEANING IS RE-DERIVED FOR THIS PATH AND IS NOT THE SIBLING'S. On ptyrunner
	// the expected no-such-process comes from sess.Close() having SIGTERMed claude.
	// Here claude exits ON ITS OWN after writing its `result` line, before pyry
	// returns — so the same expected value arrives for a different reason, and what
	// it decides is WHETHER ANY PARENTAGE-BASED VIEW OF THE COMMAND COULD HAVE BEEN
	// INFORMATIVE AT ALL. It is a race, which is exactly why it is recorded per run
	// rather than assumed. Corroboration, and never the finding.
	claudeState := pinReadState(h.ClaudePID).Verdict

	// 3b. The pinned-pid read #1440's sighting route consumes. A SECOND pinReadState
	// call and never a reuse of the one above: that outcome is over h.ClaudePID, an
	// int naming CLAUDE ITSELF, while this route's pid comes from the during-turn
	// pinned set. Same instant, different pid — a reuse reads correct and answers
	// about the wrong process.
	//
	// THE SELECTION RULE AND THE TIMING OBLIGATION ARE finGatherInputs.PinnedPid's
	// OWN, and they are obeyed here rather than re-argued: the FIRST entry when the
	// set is non-empty, NO READ AT ALL when it is empty — never pinReadState(0),
	// which claims an instrument ran — and the read taken AFTER the exit wait,
	// because the route's claim is about an instant later than pyry's exit. Hoisting
	// it above the select would take an EARLY reading rather than a racy one:
	// h.Pin.PGIDs is written before the handle is returned and the driver's cmd.Wait
	// goroutine never touches it.
	//
	// THIS DISCHARGES THE THREE SUB-CLAUSES THE AC PUTS ON THE ALIVENESS READING,
	// STRUCTURALLY. The pid was pinned DURING the turn while the command was still
	// reachable and matched on the FIFO path in its FULL ARGV by the driver's own
	// scan; the re-check is a lookup FOR THAT PID ALONE and requires no descendant
	// relationship (`pinReadState`); and pinReadState's closed set separates
	// pinStateRunning from pinStateExitedNotReaped, because `ps` lists a zombie as a
	// row. probeHasCommand IS NOT USED and would answer a different question — it
	// matches on filepath.Base(argv[0]), so the held command records as `cat`, and it
	// is scoped to DESCENDANTS, the one scoping guaranteed to fail here once claude
	// exits and the group re-parents to init.
	//
	// No dedupe and no cardinality assertion: whether the set has the expected size
	// is finOutcomeStaging's count arm's business.
	var pinnedPid pinStateOutcome
	if len(h.Pin.PGIDs) > 0 {
		pinnedPid = pinReadState(h.Pin.PGIDs[0])
	}

	// THE OBSERVED RUNNER-PATH READING, reduced ONCE here and used twice below: the
	// gather's gate takes it at step 4, and step 7 logs its label beside the gate
	// value the run reached, so the record and the gate CANNOT DISAGREE about which
	// path ran. tdnRunnerFromArgv is streamrunner-positive on --input-format, which
	// buildStreamRunnerClaudeArgs passes and ptyrunner's buildArgs intentionally
	// omits — so a run that silently took the ptyrunner default reads as NOT THIS
	// PATH. An EMPTY h.Pin.ClaudeCommand is admissible and lands on the shipped
	// indeterminate answer, which is AMBIGUITY and never a wrong-path claim.
	//
	// NEVER reachRunnerPathFromArgv: it keys on --append-system-prompt-file, which
	// BOTH argv builders emit, so it would label a correctly-wired stream run
	// ptyrunner.
	runnerPath := tdnRunnerFromArgv(h.Pin.ClaudeCommand)

	// 4. Gather, on the pass-through of everything above. One call.
	readings, attribution, sighting := finGatherReadings(finGatherInputs{
		// The LIVE buffer; the gather polls it. Bytes() returns a copy under a
		// mutex, so this is non-destructive.
		Stdout: h.Stdout,
		// THE FIFO PATH ALONE. Not the driver's two-needle list: that scan carries
		// tdnClaudeNeedle because finLivePinReduce separates the populations
		// afterwards, and THIS GATHER HAS NO SUCH REDUCTION — adding the claude
		// needle would put claude's own row into the classifier's match-count arms
		// and into the published liveness list. The FIFO path alone is safe without
		// exclusions: neither this binary's argv nor pyry's carries it.
		Needles: []string{h.FIFOPath},
		// READ AFTER THE EXIT WAIT, which is why the handle carries live buffers
		// rather than a snapshot. ON THIS PATH IT IS EXPECTED TO CARRY NO REAP LINE
		// AT ALL, and that is the point rather than a defect: streamrunner reaps only
		// inside cmd.Cancel, which never fires on a clean exit.
		Stderr: h.Stderr.Bytes(),
		// Already the []int the gather takes; nothing is converted here. Reaching
		// for Pin.Rows and projecting .PGID off it would open the channel that
		// conversion site is famous for — reachProc.Command is verbatim argv.
		Pinned:      h.Pin.PGIDs,
		PyryExited:  pyryExited,
		ClaudeState: claudeState,
		// REDUCED AT THE CALL SITE AND NEVER INSIDE THE GATHER, because this value
		// is republished as runner_path: handing the argv over would put an
		// operator's CLAUDE_CODE_OAUTH_TOKEN or ANTHROPIC_API_KEY one field away
		// from an artifact destined for a public issue. It is #1452's reading, and
		// it is what makes trailGateAbsentOwesNone reachable at all from here — an
		// unfilled one would route a healthy stream run's absent terminal_reason to
		// the gate's path-unnamed case instead.
		RunnerPath: runnerPath,
		// THE PINNED-PID READ, TAKEN AT STEP 3b AND NEVER INSIDE THE GATHER: the
		// gather's own per-matched-pid loop reads the argv scan's live matches at
		// gather time, so it answers at an instant this route is not about. It
		// crosses WHOLE — narrowing it to a verdict string would leave
		// TestFinGatherPinnedPidCarriesNoCapturedBytes no route to build.
		PinnedPid: pinnedPid,
		// Ordering is LEFT UNFILLED HERE ON PURPOSE, and it is filled on the
		// READINGS immediately below. See that assignment for the argument.
	})

	// 4a. THE CERTIFIED ORDERING, FILLED AFTER THE GATHER AND STRICTLY BEFORE THE
	// CLASSIFIER. Four things about this line are load-bearing.
	//
	// WHICH FIELD THIS IS. It fills trailRunReadings.Ordering.
	// finGatherInputs.Ordering is left at its zero and MUST STAY THAT WAY: that
	// field's own doc says no live caller fills it and none may be added, and this
	// rig adds none. The circularity that doc names is real; this routes AROUND it
	// rather than resolving it, and a reader following this ticket must not conclude
	// the input field became fillable.
	//
	// WHY AFTER AND NOT BEFORE. The trailerSighted premise is only knowable from the
	// gather's own third return. Recovering it with an earlier trailWaitForTrailer
	// would degrade the gather's OWN observation to trailBoundFromStart — the
	// discriminator whose own doc says it BOUNDS NOTHING — and that degraded
	// observation is what fills the published BoundFrom, the Staleness and the gate.
	// A mis-report, not a cost.
	//
	// WHY THIS IS A FILL AND NOT A RE-DERIVATION. The gather left the field zero
	// because it could not be given the input, and says so in its own body: holdHeld
	// is a fact about a FIFO the CALLER holds. Nothing is overwritten, nothing the
	// gather decided is re-decided, and the value comes from trailCertifyOrdering's
	// own output — the field's one admissible producer — and never from a
	// trailOrderResult literal.
	//
	// WHY THE ORDERING PREMISE HOLDS EVEN THOUGH THE POLL IS LAST. Bytes reach
	// h.Stdout only while pyry is alive, so a trailer the gather finds after pyry
	// exited was WRITTEN before pyry exited: trailer → pyry's exit → the pinned-pid
	// read, in that order. Step 3b happening earlier in PROGRAM order than the
	// gather's poll changes none of it.
	readings.Ordering = finStreamCertifyOrdering(sighting.State, pyryExited)

	// 5. Staging first; the classifier only on the pass-through. REUSED from #1337's
	// probe and not copied: an unstaged run's post-trailer scan still parses a
	// healthy process table and matches nothing, which would reach an ANSWER about a
	// run in which no command ever existed.
	outcome, classified, consulted := finExitClassify(h.Staging, readings)

	// 6. Build and write.
	rec := finRecordBuild(finRecordInputs{
		ExitCode: exitCode,
		// THE DURING-TURN PIN, never a second scan. Pin.Rows is already
		// FIFO-needle-only: finLivePinReduce filtered claude's own row out.
		Rows:        h.Pin.Rows,
		Liveness:    readings.Liveness,
		Attribution: attribution,
		// Built from the gather's THIRD RETURN and never from a second
		// trailWaitForTrailer, which by now would match on its first poll and report
		// trailBoundFromStart — a MIS-REPORT, not a cost.
		Trailer: finTrailerBuild(outcome, sighting),
		// The REAL delta. reachRunnerPathFromEnv reads ambient os.Getenv first and
		// only then lets the delta override, so an empty or partial delta would make
		// this a reading of the operator's shell rather than of this run.
		RunnerFromEnv: reachRunnerPathFromEnv(h.EnvDelta),
		// WHOLE AND UNEXAMINED. It reaches tdnRunnerFromArgv INSIDE the builder, so
		// THE SAME ARGV IS REDUCED TWICE — once at the gather's call site above and
		// once here — and the two are deliberately NOT hoisted into one:
		// tdnRunnerFromArgv is pure over this string, so they agree by construction,
		// and sharing would mean changing one of two shipped signatures, since
		// finRecordInputs takes the ARGV and the gather takes the LABEL.
		ClaudeCommand: h.Pin.ClaudeCommand,
		ClaudeVersion: h.ClaudeVersion,
	})
	finWriteArtifacts(t, artifactDir, rec)

	// 7. Publish what the artifact writer does not: the staging result, the
	// classifier's outcome, the certified ordering, the gate value and the counts
	// reach no field of finRecordRun. So this channel is t.Logf, which keeps the two
	// proven artifact files exactly as the shipped writer produces them rather than
	// growing a second file-writing surface no sweep covers.
	//
	// EVERY RENDERING BELOW IS FIELD-BY-FIELD OR json.Marshal. NEVER a %v verb
	// applied to a struct or a slice: that is the concrete mechanism by which a %v on
	// h.Pin.Rows would print every matched row's full argv from a line that reads as
	// ordinary debug formatting.
	//
	// THE INVENTORY, each with its own safety argument. h.Staging — proven by
	// TestFinOutcomeResultCarriesNoCapturedBytes. The classifier's outcome — proven
	// by TestTrailRunOutcomeCarriesNoCapturedBytes. The ordering — trap-free by
	// construction and proven by TestTrailOrderResultCarriesNoCapturedBytes. The
	// exit code, the deadline, the counts and the pinned pid's PID — ints. The
	// runner label, the gate value, the route, the route reason, the liveness
	// verdicts and terminal_reason — closed-set constants. NEVER stop_reason, which
	// is model-influenced and carried uncapped by design, and NEVER any sub-record's
	// Detail: pinStateOutcome's Detail, StateColumn and ToolStderr are all
	// string-bearing and its instrument-failed branch folds RAW ps stderr into
	// Detail, which is why the pinned-pid read is rendered as two fields and never
	// whole.
	if blob, err := json.Marshal(h.Staging); err != nil {
		t.Logf("#1353 staging result: marshal failed: %v", err)
	} else {
		t.Logf("#1353 staging result: %s", blob)
	}

	switch {
	case !consulted:
		t.Logf("#1353 classifier outcome: NOT CONSULTED. The staging tier returned %s, which is "+
			"not %s, so trailClassifyRun was never called and the published outcome is the "+
			"staging value itself. An unstaged run's post-trailer scan still parses a healthy "+
			"process table and matches nothing, which would have reached an ANSWER about a run "+
			"in which no command ever existed.",
			h.Staging.Value, finOutcomeReadyToClassify)
	default:
		if blob, err := json.Marshal(classified); err != nil {
			t.Logf("#1353 classifier outcome: marshal failed: %v", err)
		} else {
			t.Logf("#1353 classifier outcome: %s", blob)
		}
	}

	if blob, err := json.Marshal(readings.Ordering); err != nil {
		t.Logf("#1353 certified ordering: marshal failed: %v", err)
	} else {
		t.Logf("#1353 certified ordering: %s. Its Detail's fixed premise clause is how "+
			"\"the hold was still held for the whole of that wait\" reaches the record as "+
			"EVIDENCE rather than as prose.", blob)
	}

	exitNote := "pyry exited on its own within the deadline"
	switch {
	case !pyryExited:
		exitNote = "pyry did NOT exit within the deadline, so the recorded code is " +
			"pinExitStatusUnknown for that reason and not for a signal"
	case exitCode == pinExitStatusUnknown:
		exitNote = "pyry exited but its status carries no clean exit code — a signalled " +
			"process reports the same value, so this is not-a-clean-self-exit and not a timeout"
	case exitCode != 0:
		exitNote = "pyry exited with a NON-ZERO code, which on this path is consistent with " +
			"streamrunner's WaitDelay firing on claude's stdout pipe while the detached Bash " +
			"group still holds a dup of its write end — a reading, not a defect, and it moves " +
			"no verdict: the discriminator is the trailer and the ordering, never the status"
	}
	t.Logf("#1353 pyry exit: exited_within_%s=%t, recorded exit_code=%d (%s). The FIFO write end "+
		"was HELD FOR THE WHOLE OF THAT WAIT: this rig registers no cleanup, and holdProbeFIFO "+
		"releases only in the t.Cleanup it registers itself, which runs after this body returns. "+
		"So the exit was pyry's own and not the rig's.",
		finStreamExitPyryExitDeadline, pyryExited, exitCode, exitNote)

	reasonNote := "the gate value is neither owes-none case, so no terminal_reason statement " +
		"is made from it"
	switch readings.Gate.Value {
	case trailGateAbsentOwesNone:
		reasonNote = "the gate read " + trailGateAbsentOwesNone + ": terminal_reason was ABSENT " +
			"on a path that owes none, which is the EXPECTED healthy shape of a stream run. " +
			"Read from the KEY NAMES rather than assumed — after the fixed decode an absent key " +
			"and one emitted as \"\" are the same value — and run.json's trailer_keys is the " +
			"auditable evidence: the names claude emitted, names only, never their values"
	case trailGatePresentOwesNone:
		reasonNote = "the gate read " + trailGatePresentOwesNone + ": a terminal_reason was " +
			"PRESENT on a path that owes none, which means pyry's idle-stall watchdog fired and " +
			"synthesised the trailer. THIS RUN IS NOT ONE TO DRAW A VERDICT FROM"
	}
	t.Logf("#1353 observed path: claude's own argv reads %s and the trailer gate reached %s. "+
		"Both come from ONE reading — the same tdnRunnerFromArgv answer fills the gate's input "+
		"and the record's runner_from_argv — so the record and the gate cannot disagree about "+
		"which path ran; an EMPTY claude argv reads as indeterminate, which is ambiguity and "+
		"never a wrong-path claim. %s.",
		finRecordRunnerLabel(runnerPath), readings.Gate.Value, reasonNote)

	routeNote := "the classifier was not consulted, so no evidence route was named"
	if consulted {
		routeNote = "evidence route " + classified.Route + " / " + classified.RouteReason +
			" — the route fields, not the outcome value, are what keep this answer apart " +
			"from #1337's reap-log one"
	}
	t.Logf("#1353 corroboration: the claude child read %q after pyry's exit, the per-pid "+
		"post-trailer reads number %d, and the pinned-pid re-read of pid %d answered %q. "+
		"NONE OF THESE IS THE FINDING. The claude reading decides only whether any "+
		"parentage-based view of the command could have been informative at all, and it is a "+
		"race. THE PATH-SPECIFIC ASYMMETRY AGAINST #1337: there the post-trailer scan found 0 "+
		"matches across 893 rows because the reap had already killed the group, and here "+
		"NOTHING REAPS ON A CLEAN EXIT — so a match (%d of %d row(s) scanned) is the PREDICTED "+
		"reading rather than an anomaly. %s.",
		claudeState, len(readings.Liveness), pinnedPid.PID, pinnedPid.Verdict,
		readings.MatchCount, readings.RowsScanned, routeNote)

	// The finding, in one sentence, keyed on the classifier's value. Counts,
	// closed-set values and terminal_reason only — never stop_reason and never any
	// sub-record's Detail.
	switch {
	case !consulted:
		t.Logf("#1353 FINDING: NO CLAIM IS MADE. The run did not stage (%s), so no reading here "+
			"is about a held command and nothing is asserted about pyry's exit path.",
			h.Staging.Value)
	case outcome == trailOutcomeAliveAtSightingByOrdering:
		t.Logf("#1353 FINDING: THE COMMAND WAS STILL RUNNING WHEN THE TRAILER WAS SIGHTED ON "+
			"PYRY'S STDOUT. THE EXIT CODE ALONE DOES NOT SEPARATE A COMPLETED RUN FROM A "+
			"TERMINATED ONE — both exit 0, and since #1388 a complete result trailer "+
			"additionally makes streamrunner discard claude's non-zero status — so what carried "+
			"this is THE CERTIFIED ORDERING OF THE THREE INSTANTS (%s) AND THE PINNED-PID "+
			"RE-READ (%s), NOT A TRAILER FIELD; terminal_reason read %q. NO DECLARED-FINISHED "+
			"INSTANT EXISTS ON THIS PATH, so #1337's stronger aliveness-at-declared-finish "+
			"claim is UNAVAILABLE here — recorded as absent, never restated in weaker words.",
			readings.Ordering.Value, pinnedPid.Verdict, rec.Trailer.TerminalReason)
	default:
		t.Logf("#1353 FINDING: this run establishes no such claim — it reached %s, not %s, and a "+
			"run landing on %s is an INSTRUMENT FAILURE rather than a reading about pyry. THE "+
			"EXIT CODE ALONE DOES NOT SEPARATE A COMPLETED RUN FROM A TERMINATED ONE — both "+
			"exit 0, and since #1388 a complete result trailer additionally makes streamrunner "+
			"discard claude's non-zero status — so the evidence here would have been THE "+
			"CERTIFIED ORDERING (%s) AND THE PINNED-PID RE-READ (%s), NOT A TRAILER FIELD; "+
			"terminal_reason read %q over %d post-trailer match(es). NO DECLARED-FINISHED "+
			"INSTANT EXISTS ON THIS PATH, so the stronger claim is unavailable here either.",
			outcome, trailOutcomeAliveAtSightingByOrdering, trailOutcomeOutOfContract,
			readings.Ordering.Value, pinnedPid.Verdict, rec.Trailer.TerminalReason,
			readings.MatchCount)
	}
}

// --- the offline trap ----------------------------------------------------------------

// finStreamOrderingCase is one scan state, one exit outcome, and the ordering value
// the composition must return.
type finStreamOrderingCase struct {
	name          string
	sightingState string
	pyryExited    bool
	want          string
}

// TestFinStreamCertifyOrderingMapsThePremises drives every shipped scan state
// against both exit outcomes.
//
// Six rows rather than a representative pair: trailAbsent and trailAborted are
// DIFFERENT scan states that must map to the same premise, and a table carrying
// only one of them would leave the other to judgement — an implementation testing
// `sightingState != ""` in place of `== trailSeen` passes on both `trailSeen` rows
// and reddens only on the four it omits.
//
// THE CLAUSE THAT PINS THE THIRD ARGUMENT is the assertion that NO ROW ANSWERS
// trailOrderVoidUnheld. That single check is what makes this trap discriminating
// rather than decorative: finStreamCertifyOrdering passing `false` for holdHeld
// would redden every row here, because trailOrderVoidUnheld OUTRANKS both other
// premises — which is exactly why the hold is the premise whose failure removes the
// SUBJECT of the later claim rather than one endpoint of the ordering.
func TestFinStreamCertifyOrderingMapsThePremises(t *testing.T) {
	cases := []finStreamOrderingCase{
		{
			name:          "a sighted trailer and a completed exit order the three instants",
			sightingState: trailSeen,
			pyryExited:    true,
			want:          trailOrderCertified,
		},
		{
			name:          "a sighted trailer with no exit leaves the later read later than nothing",
			sightingState: trailSeen,
			pyryExited:    false,
			want:          trailOrderVoidNoExit,
		},
		{
			name:          "an absent trailer leaves no earlier instant",
			sightingState: trailAbsent,
			pyryExited:    true,
			want:          trailOrderVoidUnsighted,
		},
		{
			name:          "an absent trailer outranks a missing exit",
			sightingState: trailAbsent,
			pyryExited:    false,
			want:          trailOrderVoidUnsighted,
		},
		{
			name:          "an aborted scan sighted no trailer either",
			sightingState: trailAborted,
			pyryExited:    true,
			want:          trailOrderVoidUnsighted,
		},
		{
			name:          "an aborted scan outranks a missing exit",
			sightingState: trailAborted,
			pyryExited:    false,
			want:          trailOrderVoidUnsighted,
		},
	}

	// The table's own half of the clause: a row that WANTED the unheld void would
	// make the per-row check below assert the very answer this rig cannot produce.
	for _, tc := range cases {
		if tc.want == trailOrderVoidUnheld {
			t.Fatalf("row %q wants %s, which this rig cannot produce: holdProbeFIFO releases only "+
				"in the t.Cleanup it registers itself, so the hold is held for the whole of the "+
				"wait by construction", tc.name, trailOrderVoidUnheld)
		}
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := finStreamCertifyOrdering(tc.sightingState, tc.pyryExited)
			if got.Value == trailOrderVoidUnheld {
				t.Fatalf("value: got %s (%s) — the third premise is passed as a STRUCTURAL true, "+
					"so no input to this function may answer it. A run published off a guessed "+
					"hold would name a pid that could have been retired and reissued between the "+
					"sighting and the later read", got.Value, got.Detail)
			}
			if got.Value != tc.want {
				t.Errorf("value: got %q (%s), want %q for scan state %q and pyry-exited %t",
					got.Value, got.Detail, tc.want, tc.sightingState, tc.pyryExited)
			}
		})
	}
}

//go:build e2e_realclaude

package realclaude

// The live entry point: does `pyry agent-run` reach its normal exit path — trailer
// written, process returned — while a command that run launched is still running?
//
// The ptyrunner default and ONLY that path. The headless stream path is #1237's,
// and a finding that does not name its path names nothing: the two do not write
// the same trailer and do not present the same process tree at trailer time.
//
// # It composes and publishes; it derives nothing
//
// #1340's finLiveRunStage stages the turn. finGatherReadings → trailClassifyRun →
// finTrailerBuild → finRecordBuild → finWriteArtifacts turn its handle into a
// publishable record. Every one of those is shipped and offline-proven, and every
// outcome they return is consumed AS RETURNED: not re-derived, not renamed, not
// cross-checked into a new verdict. The per-group attribution fan-out is called
// INSIDE the gather and is not called again here.
//
// # Why the reap log is the evidence and the point-in-time reads are not
//
// ptyrunner.Run pins its teardown order in its own comment — emitter.Close() writes
// the trailer, then cancel(), then the reap defer, then sess.Close()'s SIGTERM
// (runner.go:479-485, the defer at :398). So at trailer time claude is alive and
// unsignalled and any auto-backgrounded command is still a descendant of pyry.
//
// The instant of interest is that write, and it is NOT directly observable: a rig
// sees the trailer only when it polls pyry's stdout, up to one probePollInterval
// (200ms) after the write, and the reap starts effectively immediately after it and
// finishes in the time of one ps exec (reap.go:76). A liveness read taken at
// OBSERVATION time therefore finds the held command already reaped on a healthy run,
// and reporting that as "the command had exited" is a systematic FALSE NEGATIVE on
// the question this file asks.
//
// What answers it is pyry's own reap log. ReapDescendantGroups logs the groups it
// actually killed and skips those already gone (reap.go:56-57, :65), so a group
// named there was alive when the reaper ran — strictly after the trailer was
// written, and therefore alive when it was written. That is an ordering argument
// internal to the run's own logs and depends on no point-in-time read. The natural
// alternative witness — "was claude still alive when I read?" — is BLIND to this:
// the reap runs between the trailer and claude's SIGTERM, so in that window the
// command is dead-by-reap while claude still reads alive, and such a witness would
// certify the read as timely over exactly the case this probe exists to catch.
//
// # Captured-bytes discipline
//
// ARTIFACTS GO INTO A PUBLIC ISSUE. NO `ps -E` AND NO `-Eww`, anywhere: those dump
// the operator's CLAUDE_CODE_OAUTH_TOKEN and ANTHROPIC_API_KEY. This rig needs no
// environment read of any process at all and runs no ps of its own — pinReadState's
// pid-pinned lookup is `pid=,ppid=,stat=` by construction
// (`pinStateColumns`) and is the only one it reaches.
//
// It HOLDS verbatim argv (h.Pin.Rows[i].Command, h.Pin.ClaudeCommand) and forwards
// both to finRecordBuild UNEXAMINED AND UNFORMATTED, which reduces the rows to three
// integers each and the claude argv to one of tdnRunnerFromArgv's three constants.
// Neither is read, logged or interpolated here. The one log channel this file adds
// carries two values with passing byte sweeps and nothing else — see § the fifth
// criterion's channel, at finExitRunProbe.
//
// The raw trailer line is not published and is not reachable: finSighting carries
// neither trailScanResult.Line nor the *resultTrailer, so the prohibition holds by
// the shape of the input rather than by a discipline.
//
// # It asserts almost nothing
//
// t.Fatalf fires only on structural failure, and this file adds exactly ONE of its
// own: os.MkdirTemp failing. Three abort paths are INHERITED from finLiveRunStage
// and are not re-guarded here — pyry never spawning a claude child, no system/init
// session id (finding_live_run_test.go:336-346), and ReadJSONL's fatal on a
// transcript it cannot open or parse (`ReadJSONL`, :165) reached through the
// assembly. Every other failure mode is RECORDED, because a probe that turns an
// unexpected reading into a red test loses the reading.
//
// # One skip gate, not two
//
// finExitEnableEnv follows reachEnableEnv's shape (`TestRealClaude_BackgroundReachability`),
// so `make e2e-realclaude` skips this probe by default and a skip is the NORMAL
// outcome, saying nothing about pyry's behaviour.
//
// The reach probe's second gate (PYRY_USE_STREAMJSON=1, :300-310) is deliberately
// NOT copied, because neither of its two reasons transfers and copying it would skip
// a run that would have been correct. (1) Its delta does not name the variable;
// finLiveStageEnvDelta names PYRY_USE_STREAMJSON=0 EXPLICITLY
// (`finLiveStageEnvDelta`, :194-196) and spawnProbePyry appends the
// delta to os.Environ(), which os/exec resolves in favour of the later value — so
// the delta wins over the operator's shell. (2) Its content-first root pinning keys
// on --session-id, which only ptyrunner emits; this rig pins nothing content-first,
// resolving claude through probeWaitForDirectChild's descendant walk. #1340 states
// the same conclusion for the same reason (finding_live_run_test.go:247-251). The
// observed-path reading below is the real guard and is strictly stronger than an env
// check.
//
// # This file ships ONE offline test, and the entry point is not it
//
// The entry point needs a live claude and a credentialed worktree; its exercise
// under `make e2e-realclaude` is compilation and `go test`'s vet subset. Note that
// `go vet` and `staticcheck` in `make check` run WITHOUT -tags e2e_realclaude, so
// neither analyses this file.
//
// The one thing worth trapping is finExitClassify: it is the only decision this
// ticket introduces that is pure and offline-drivable, and the staging-before-the-
// classifier rule is entirely about it.

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

// --- the constants ------------------------------------------------------------------

// finExitEnableEnv gates the live probe, following reachEnableEnv's shape.
const finExitEnableEnv = "PYRY_PROBE_EXIT_PATH"

// finExitPyryExitDeadline bounds the wait for pyry to exit ON ITS OWN with the FIFO
// write end STILL HELD.
//
// IT IS THIS FILE'S OWN CONSTANT AND IT IS NOT probePyryExitGrace, which the two are
// easy to conflate. That one (20s, background_trigger_probe_test.go:134) measures the
// driver's defence-in-depth cleanup waiting AFTER THE FIFO RELEASE before SIGKILLing
// (finding_live_run_test.go:460-492) — a mechanical unblock. This one measures a turn
// COMPLETING with the hold still on: claude receiving the tool_result, producing a
// final assistant message, emitter.Close() writing the trailer, teardown, exit. That
// is a model round-trip plus teardown. Reusing the other constant would name one
// duration for two unrelated waits.
//
// 120s is deliberately generous, because the two errors are not symmetric: too short
// produces a false trailOutcomeVoidPyryDidNotExit on a HEALTHY run, and on a
// needs-real-claude ticket that costs an operator round-trip; too long costs only
// wall-clock on a run that has already failed. A void whose logged evidence shows the
// trailer plainly present means the deadline was short, not that pyry hung — raise
// this and re-run rather than treating it as a finding.
const finExitPyryExitDeadline = 120 * time.Second

// --- the one decision this file introduces -------------------------------------------

// finExitClassify returns the outcome string finTrailerBuild publishes, the
// classifier's full outcome, and whether the classifier was consulted at all.
//
// STAGING IS DECIDED FIRST AND A RUN THAT FAILED TO STAGE IS NEVER PASSED TO THE
// CLASSIFIER AT ALL. That is not a tidiness rule: on an unstaged run the post-trailer
// argv scan still runs over a healthy process table, parses rows and matches nothing,
// which reaches trailClassifyRun's final fall-through arm — trailOutcomeNoRowMatched,
// one of its four ANSWERS — published about a run in which no command ever existed.
//
// finTrailerBuild's first parameter is a bare string fed from EITHER closed set (the
// classifier's sixteen, `trailClassifyRun`, and the staging tier's seven,
// finding_staging_gate_test.go:113-134) precisely so the staging value can be carried
// straight through, so no adapter is needed and none is added.
//
// It is a named pure function rather than an inline if for one reason: it is the only
// decision here that is drivable offline, and the rule above is what its trap is
// entirely about.
//
// On a non-pass-through it returns the ZERO trailRunOutcome, whose Value is "" —
// which trailIsRunOutcome rejects, so a reader who meets it can look it up and find
// it is not a member. A caller must branch on the third return, not on the second.
func finExitClassify(staging finOutcomeResult, readings trailRunReadings) (string, trailRunOutcome, bool) {
	// An identity comparison against a package constant, and the only boundary this
	// file decides. finOutcomeReadyToClassify is deliberately NOT the zero value
	// (finding_staging_gate_test.go:100-108), so an unfilled finOutcomeResult lands
	// on the safe side of this arm rather than reading as "go classify it".
	if staging.Value != finOutcomeReadyToClassify {
		return staging.Value, trailRunOutcome{}, false
	}
	out := trailClassifyRun(readings)
	return out.Value, out, true
}

// --- the entry point ------------------------------------------------------------------

// TestRealClaude_ExitPathWhileCommandRuns stages one live turn on the ptyrunner
// default, waits for pyry to exit WITH THE HELD COMMAND STILL UNFINISHABLE, and
// records whether pyry declared the turn finished while that command was running.
//
// NO t.Parallel: finLiveRunStage reaches WithWorktreeAuthenticated, which calls
// t.Setenv (fixtures.go:96-107), and Go's runtime refuses that pairing. That helper
// also SKIPS when neither ANTHROPIC_API_KEY nor CLAUDE_CODE_OAUTH_TOKEN is set — the
// credential skip, separate from the opt-in gate below.
func TestRealClaude_ExitPathWhileCommandRuns(t *testing.T) {
	if os.Getenv(finExitEnableEnv) != "1" {
		t.Skipf("#1337 exit-path probe: skipped because %s != 1.\n"+
			"This is an EVIDENCE PROBE, not a regression gate — a skip here is the "+
			"normal `make e2e-realclaude` outcome and says nothing about pyry's "+
			"behaviour. It costs one live claude turn. The probe sets "+
			"BASH_DEFAULT_TIMEOUT_MS=5000 and PYRY_USE_STREAMJSON=0 on the pyry "+
			"process itself (#1342's env delta), so no extra environment is needed. "+
			"The -timeout accommodates this file's %s alongside the driver's own "+
			"deadlines and the gather's %s trailer wait:\n"+
			"  %s=1 go test -tags e2e_realclaude -timeout 15m -v \\\n"+
			"    -run 'TestRealClaude_ExitPathWhileCommandRuns' ./internal/e2e/realclaude/",
			finExitEnableEnv, finExitPyryExitDeadline, finGatherTrailerWait, finExitEnableEnv)
	}

	// Deliberately NOT t.TempDir(): that is removed when the test ends, and the
	// operator needs these files afterwards to compose the issue comment.
	artifactDir, err := os.MkdirTemp("", "pyry-1337-probe-*")
	if err != nil {
		t.Fatalf("create artifact dir: %v", err)
	}
	t.Logf("#1337 probe artifacts: %s", artifactDir)
	finExitRunProbe(t, artifactDir)
}

// finExitRunProbe takes the handle, does the reading, the classification and the
// publication. THE ORDER IS LOAD-BEARING AT THREE POINTS, each called out below.
func finExitRunProbe(t *testing.T, artifactDir string) {
	// 1. Stage. Everything after this runs with the FIFO write end STILL HELD:
	// holdProbeFIFO releases only in the t.Cleanup it registers itself, which by
	// construction runs after this body returns. The driver also registers its
	// defence-in-depth SIGKILL cleanup BEFORE that hold, so LIFO releases the FIFO
	// first and pyry gets a real chance to exit on its own. This rig registers no
	// cleanup at all, so it cannot disturb that order.
	h := finLiveRunStage(t, finLiveStageEnvDelta())

	// 2. Wait for pyry's own exit — IN THE BODY, NEVER IN A CLEANUP. If this wait
	// were taken after the hold's release, the RIG's release would have produced the
	// exit and the reading would measure the rig rather than pyry.
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
		// select, passing it to a finExitObservedCode(exited, status) helper
		// alongside the bool — reads the field concurrently with that goroutine and
		// is a data race `go test -race` will flag. There is deliberately no such
		// helper: the only race-free shape is the one that needs none.
		exitCode = h.ExitStatus
	case <-time.After(finExitPyryExitDeadline):
		// The unexited path simply leaves pinExitStatusUnknown in place, which is
		// what keeps a non-exiting run from publishing exit_code: 0 — a value
		// finRecordRun.ExitCode documents as A REAL SUCCESSFUL EXIT
		// (finding_run_record_test.go:126-135).
	}

	// 3. The claude-still-alive corroboration. pinReadState's Verdict and NOTHING
	// ELSE — never a raw ps column, because this value crosses the gather unvalidated,
	// is republished as claude_state and is quoted into a published Detail. ClaudePID
	// is guaranteed positive: the driver fatals on 0.
	//
	// Taken HERE, after the exit wait, and its expected value on a healthy run is
	// no-such-process — pyry has exited, so sess.Close() has already SIGTERMed claude.
	// That is this reading's KNOWN BLINDNESS and exactly why it is corroboration and
	// never the finding: the reap runs between the trailer and that SIGTERM, so in
	// that window the command is dead-by-reap while claude still reads alive.
	claudeState := pinReadState(h.ClaudePID).Verdict

	// 3b. The pinned-pid read #1440's sighting route consumes. A SECOND pinReadState
	// call and never a reuse of the one above: that outcome is over h.ClaudePID, an
	// int naming CLAUDE ITSELF, while this route's pid comes from the during-turn
	// pinned set. Same instant, different pid — a reuse reads correct and answers
	// about the wrong process.
	//
	// THE FIRST ENTRY OF THAT SET, which is not an arbitrary pick among differing
	// groups: finLivePinReduce projects one entry per FIFO-matched row with
	// duplicates deliberately intact, so on a healthy run the set is two entries
	// naming ONE detached process group — TestFinLivePinReduce's raw-projection
	// subtest is the measurement. AN EMPTY SET TAKES NO READ AT ALL. The set is nil
	// whenever the pin scan failed, so an unguarded index panics here; and
	// pinReadState(0) is the wrong stand-in for the empty case, because it answers
	// pinStateInstrumentFailed and so claims an instrument ran. The zero
	// pinStateOutcome carries Verdict "", which trailSightingReasonPidReadFailed's
	// own doc names among the shapes the route answers for.
	//
	// TAKEN HERE, AFTER THE EXIT WAIT, because the route's claim is about a pid
	// re-read at an instant later than pyry's exit — the obligation
	// finGatherInputs.PinnedPid states and the gather structurally cannot check.
	// Hoisting it above the select would take an early reading rather than a racy
	// one: unlike h.ExitStatus, h.Pin.PGIDs is written before the handle is returned
	// and the driver's cmd.Wait goroutine never touches it.
	//
	// No dedupe and no cardinality assertion. Whether the set has the expected size
	// is finOutcomeStaging's count arm's business, checked against
	// finLivePinWantRows; a second opinion here would duplicate a shipped gate.
	var pinnedPid pinStateOutcome
	if len(h.Pin.PGIDs) > 0 {
		pinnedPid = pinReadState(h.Pin.PGIDs[0])
	}

	// 4. Gather, on the pass-through of everything above. One call.
	readings, attribution, sighting := finGatherReadings(finGatherInputs{
		// The LIVE buffer; the gather polls it. Bytes() returns a copy under a
		// mutex, so this is non-destructive.
		Stdout: h.Stdout,
		// THE FIFO PATH ALONE. Not the driver's two-needle list
		// (`finLiveRunStage`): that scan carries tdnClaudeNeedle
		// because finLivePinReduce separates the populations afterwards, and the
		// gather has NO SUCH REDUCTION — its argv leg calls
		// pinScanArgv(in.Needles, nil) and fills MatchCount, RowsScanned and one
		// pinReadState per matched pid from that one call. Adding the claude needle
		// would put claude's own row into the classifier's match-count arms and
		// into the published liveness list. The FIFO path alone is safe without
		// exclusions: neither this binary's argv nor pyry's carries it.
		//
		// #1452 did NOT reopen this: the runner-path reading below reaches the
		// gather as a REDUCED STRING and never as a needle, so the needle set is
		// what it always was.
		Needles: []string{h.FIFOPath},
		// READ AFTER THE EXIT WAIT, which is why the handle carries live buffers
		// rather than a snapshot: pyry's reap log — the primary evidence — lands on
		// stderr at teardown.
		Stderr: h.Stderr.Bytes(),
		// Already the []int the gather takes; nothing is converted here. Reaching
		// for Pin.Rows and projecting .PGID off it would open the channel that
		// conversion site is famous for — reachProc.Command is verbatim argv.
		Pinned:      h.Pin.PGIDs,
		PyryExited:  pyryExited,
		ClaudeState: claudeState,
		// THE RUNNER-PATH READING, REDUCED HERE AND NEVER INSIDE THE GATHER. The
		// gather takes tdnRunnerFromArgv's answer — one of five source-authored
		// constants — and never h.Pin.ClaudeCommand itself, which is verbatim argv
		// marked INPUT ONLY — NEVER PUBLISHED on finLivePinReading. This value is
		// republished as runner_path, so handing the argv over and reducing it
		// inside the gather would put an operator's CLAUDE_CODE_OAUTH_TOKEN or
		// ANTHROPIC_API_KEY one field away from an artifact destined for a public
		// issue — the same conversion-site rule Pinned above obeys.
		//
		// Never reachRunnerPathFromArgv: it keys on --append-system-prompt-file,
		// which BOTH argv builders emit, so it would label a correctly-wired stream
		// run ptyrunner.
		//
		// An EMPTY ClaudeCommand is admissible and lands on the shipped
		// indeterminate answer, exactly as it does at ClaudeCommand below. This is
		// what makes trailGateAbsentOwesNone reachable from a live headless stream
		// run, where a healthy trailer is claude's own result line and carries no
		// terminal_reason at all.
		RunnerPath: tdnRunnerFromArgv(h.Pin.ClaudeCommand),
		// THE PINNED-PID READ, TAKEN AT STEP 3b AND NEVER INSIDE THE GATHER.
		// #1452's RunnerPath doctrine one field along, and the reason here is
		// TIMING rather than credentials: the gather's own per-matched-pid loop
		// reads the argv scan's live matches at gather time, so it answers at an
		// instant this route is not about. It crosses WHOLE — narrowing it to a
		// verdict string would leave TestFinGatherPinnedPidCarriesNoCapturedBytes
		// no route to build.
		PinnedPid: pinnedPid,
	})

	// 5. Staging first; the classifier only on the pass-through.
	outcome, classified, consulted := finExitClassify(h.Staging, readings)

	// 6. Build and write.
	rec := finRecordBuild(finRecordInputs{
		ExitCode: exitCode,
		// THE DURING-TURN PIN, never a second scan. The gather runs its own scan
		// internally and returns only counts and per-pid liveness — trailRunReadings
		// carries MatchCount, RowsScanned and Liveness and no Matches — so a second
		// post-trailer scan here would be a re-derivation and on a healthy run would
		// publish an empty matched_rows. Pin.Rows is already FIFO-needle-only:
		// finLivePinReduce filtered claude's own row out, which is why RowCount is
		// finLivePinWantRows and not scan.MatchCount.
		Rows:        h.Pin.Rows,
		Liveness:    readings.Liveness,
		Attribution: attribution,
		// Built from the gather's THIRD RETURN and never from a second
		// trailWaitForTrailer: by now the trailer is already in the buffer, so a
		// second call matches on its first poll and reports trailBoundFromStart, a
		// discriminator whose own doc says it bounds nothing. That is a MIS-REPORT,
		// not a cost.
		Trailer: finTrailerBuild(outcome, sighting),
		// The REAL delta. reachRunnerPathFromEnv reads ambient os.Getenv first and
		// only then lets the delta override (background_reach_probe_test.go:1102-1108),
		// so an empty or partial delta would make this a reading of the operator's
		// shell rather than of this run.
		RunnerFromEnv: reachRunnerPathFromEnv(h.EnvDelta),
		// WHOLE AND UNEXAMINED. An EMPTY value is ADMISSIBLE and is not a staging
		// failure: tdnClaudeCommand returns "" when zero OR SEVERAL rows carry the
		// claude needle (teardown_liveness_probe_test.go:571-573), so emptiness is
		// ambiguity about which row was claude's, never a claim that the run took
		// the other path. It reaches tdnRunnerFromArgv inside the builder and lands
		// on the shipped indeterminate verdict — the THIRD ANSWER, never a
		// disagreement. Not gated on, not defaulted, not repaired with a second
		// argv read. The builder computes both the argv label and the agreement;
		// this rig computes neither.
		//
		// SO THIS FUNCTION REDUCES THE SAME ARGV TWICE since #1452 — once at its
		// own call site for the gather's gate, and once inside finRecordBuild for
		// the record's RunnerFromArgv — and the two are deliberately NOT hoisted
		// into one. tdnRunnerFromArgv is pure over this string, so they agree by
		// construction; sharing a value would mean changing one of the two
		// signatures, since finRecordInputs takes the ARGV and the gather takes the
		// LABEL, and that is scope neither ticket has.
		ClaudeCommand: h.Pin.ClaudeCommand,
		ClaudeVersion: h.ClaudeVersion,
	})
	finWriteArtifacts(t, artifactDir, rec)

	// 7. Publish what the artifact writer does not.
	//
	// The record carries only the trailer sub-record's outcome STRING: the
	// classifier's Detail, ClaudeState, LivenessSummary, gate value and match counts
	// reach no field of finRecordRun, and the staging result reaches no artifact at
	// all. So this channel is t.Logf, which is PROVABLY SAFE for both values —
	// TestTrailRunOutcomeCarriesNoCapturedBytes and
	// TestFinOutcomeResultCarriesNoCapturedBytes —
	// and keeps the two proven artifact files exactly as the shipped writer produces
	// them rather than growing a second file-writing surface no sweep covers.
	//
	// EVERY RENDERING BELOW IS FIELD-BY-FIELD OR json.Marshal. NEVER a %v verb applied
	// to a struct or a slice: that is the content rule the writer's own note line
	// states (finding_artifact_write_test.go:171-180), and it is the concrete
	// mechanism by which a %v on h.Pin.Rows would print every matched row's full argv
	// from a line that reads as ordinary debug formatting.
	if blob, err := json.Marshal(h.Staging); err != nil {
		t.Logf("#1337 staging result: marshal failed: %v", err)
	} else {
		t.Logf("#1337 staging result: %s", blob)
	}

	switch {
	case !consulted:
		t.Logf("#1337 classifier outcome: NOT CONSULTED. The staging tier returned %s, which is "+
			"not %s, so trailClassifyRun was never called and the published outcome is the "+
			"staging value itself. An unstaged run's post-trailer scan still parses a healthy "+
			"process table and matches nothing, which would have reached the classifier's "+
			"fall-through ANSWER about a run in which no command ever existed.",
			h.Staging.Value, finOutcomeReadyToClassify)
	default:
		if blob, err := json.Marshal(classified); err != nil {
			t.Logf("#1337 classifier outcome: marshal failed: %v", err)
		} else {
			t.Logf("#1337 classifier outcome: %s", blob)
		}
	}

	exitNote := "pyry exited on its own within the deadline"
	switch {
	case !pyryExited:
		exitNote = "pyry did NOT exit within the deadline, so the recorded code is " +
			"pinExitStatusUnknown for that reason and not for a signal"
	case exitCode == pinExitStatusUnknown:
		exitNote = "pyry exited but its status carries no clean exit code — a signalled " +
			"process reports the same value, so this is not-a-clean-self-exit and not a timeout"
	}
	t.Logf("#1337 pyry exit: exited_within_%s=%t, recorded exit_code=%d (%s). The FIFO write end "+
		"was HELD FOR THE WHOLE OF THAT WAIT: this rig registers no cleanup, and holdProbeFIFO "+
		"releases only in the t.Cleanup it registers itself, which runs after this body returns. "+
		"So the exit was pyry's own and not the rig's.",
		finExitPyryExitDeadline, pyryExited, exitCode, exitNote)

	t.Logf("#1337 corroboration: the claude child read %q after pyry's exit, and the per-pid "+
		"post-trailer reads number %d. BOTH ARE CORROBORATION AND NEITHER MOVES AN ATTRIBUTION. "+
		"The claude reading is BLIND to the case this probe exists to catch — the reap runs "+
		"between the trailer and claude's SIGTERM, so in that window the command is dead-by-reap "+
		"while claude still reads alive — and the per-pid reads are KNOWN LATE, taken after the "+
		"reap rather than at the trailer's write.",
		claudeState, len(readings.Liveness))

	t.Logf("#1337 the lateness gap, stated so it does not read as an inconsistency: the "+
		"artifact's matched_rows holds %d row(s) from the DURING-TURN pin, while the "+
		"post-trailer scan reported %d match(es) across %d row(s) scanned. That gap IS the "+
		"systematic-lateness story — a rig sees the trailer up to one poll interval after the "+
		"write and the reap finishes in the time of one ps exec — and it can only be told here, "+
		"because finRecordRun has no match-count field and the artifact alone cannot show the "+
		"two numbers side by side. The artifact's liveness is absent for the same reason: it is "+
		"omitempty and the post-trailer scan matched nothing.",
		h.Pin.RowCount, readings.MatchCount, readings.RowsScanned)

	// The finding, in one sentence, keyed on the classifier's value. It names counts,
	// closed-set values and terminal_reason only — never stop_reason, which is
	// model-influenced and carried uncapped by design, and never any sub-record's
	// Detail.
	switch {
	case !consulted:
		t.Logf("#1337 FINDING: NO CLAIM IS MADE. The run did not stage (%s), so no reading here "+
			"is about a held command and nothing is asserted about pyry's exit path.",
			h.Staging.Value)
	case outcome == trailOutcomeRunningAtTrailer:
		t.Logf("#1337 FINDING: PYRY DECLARED THE TURN FINISHED WHILE THE COMMAND WAS STILL "+
			"RUNNING — its own reap log names a pinned group, and emitter.Close() wrote the "+
			"trailer before the reap defer reached that group, so it was alive when the trailer "+
			"was written. THE EXIT CODE ALONE CANNOT SEPARATE A COMPLETED RUN FROM A "+
			"BUDGET-TERMINATED ONE — both exit 0 (cmd/pyry/agent_run.go:271-277) — so the "+
			"discriminator used is the trailer's terminal_reason, read as %q.",
			rec.Trailer.TerminalReason)
	default:
		t.Logf("#1337 FINDING: this run establishes no such claim — it reached %s, not %s. THE "+
			"EXIT CODE ALONE CANNOT SEPARATE A COMPLETED RUN FROM A BUDGET-TERMINATED ONE — both "+
			"exit 0 (cmd/pyry/agent_run.go:271-277) — so the discriminator used is the trailer's "+
			"terminal_reason, read as %q, over %d post-trailer match(es).",
			outcome, trailOutcomeRunningAtTrailer, rec.Trailer.TerminalReason, readings.MatchCount)
	}
}

// --- the offline trap ----------------------------------------------------------------

// finExitClassifyCase is one staging disposition, the readings beside it, and what
// the composition must return.
type finExitClassifyCase struct {
	name     string
	staging  finOutcomeResult
	readings trailRunReadings
	// want is the outcome string, wantConsulted whether trailClassifyRun was reached.
	want          string
	wantConsulted bool
}

// TestFinExitClassifyConsultsTheClassifierOnlyOnThePassThrough drives every staging
// value through the composition.
//
// The six failure rows carry the PROOF readings deliberately: a wrongly-consulted
// classifier answers trailOutcomeRunningAtTrailer over them, which is loudly
// distinguishable from the staging value the row expects. Rows carrying readings that
// classify to a void would make several mis-implementations harder to separate.
//
// Both fixtures come from the SHIPPED constructors and never from a hand-built
// trailRunReadings: a hand-built one is exactly the fixture the classifier's contract
// block C1-C9 exists to reject.
func TestFinExitClassifyConsultsTheClassifierOnlyOnThePassThrough(t *testing.T) {
	// The well-formed row asserts against the value trailClassifyRun ITSELF produces,
	// so it pins the pass-through rather than re-asserting the classifier's own table
	// — which trail_run_outcome_test.go already owns.
	wellFormed := trailRunWellFormed()
	wantWellFormed := trailClassifyRun(wellFormed).Value

	var cases []finExitClassifyCase
	// RANGED over the shipped closed set rather than hand-listed, so a seventh
	// staging value added later arrives here without an edit.
	for _, value := range finOutcomeValues() {
		if value == finOutcomeReadyToClassify {
			continue
		}
		cases = append(cases, finExitClassifyCase{
			name:    value,
			staging: finOutcomeResult{Value: value, Detail: "a staging failure"},
			// Not the value under test, and that is the point: the readings are
			// the ones a wrongly-consulted classifier would answer loudest over.
			readings:      trailRunProofReadings(),
			want:          value,
			wantConsulted: false,
		})
	}
	cases = append(cases,
		finExitClassifyCase{
			name:          finOutcomeReadyToClassify + " over the proof readings",
			staging:       finOutcomeResult{Value: finOutcomeReadyToClassify, Detail: "staged"},
			readings:      trailRunProofReadings(),
			want:          trailOutcomeRunningAtTrailer,
			wantConsulted: true,
		},
		finExitClassifyCase{
			name:          finOutcomeReadyToClassify + " over the well-formed readings",
			staging:       finOutcomeResult{Value: finOutcomeReadyToClassify, Detail: "staged"},
			readings:      wellFormed,
			want:          wantWellFormed,
			wantConsulted: true,
		},
	)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, outcome, consulted := finExitClassify(tc.staging, tc.readings)
			if got != tc.want {
				t.Errorf("outcome = %q, want %q", got, tc.want)
			}
			if consulted != tc.wantConsulted {
				t.Errorf("consulted = %t, want %t", consulted, tc.wantConsulted)
			}
			if !tc.wantConsulted {
				// The ZERO trailRunOutcome, and never the classifier's answer
				// discarded down to its string: an implementation that consults
				// unconditionally and merely drops the outcome string still
				// returns a filled record here.
				if outcome.Value != "" {
					t.Errorf("trailRunOutcome.Value = %q on a non-pass-through staging value, "+
						"want \"\": the classifier must not be consulted at all", outcome.Value)
				}
				return
			}
			// The second return is the classifier's OWN outcome, so the string and
			// the record cannot come from two different calls.
			if outcome.Value != tc.want {
				t.Errorf("trailRunOutcome.Value = %q, want %q", outcome.Value, tc.want)
			}
		})
	}
}

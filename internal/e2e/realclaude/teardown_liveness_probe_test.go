//go:build e2e_realclaude

package realclaude

// #1251's live rig: is a Bash command claude has moved to the background dead
// after pyry has exited, and did it die by the REAPER's hand rather than by
// accident?
//
// The answer already exists. An operator measured it by hand on 2026-07-30, 3
// reps, identical: the command does not survive, and the reaper is provably what
// kills it. This file is not the discovery — it is the rig that keeps the answer
// true. agentrun.ReapDescendantGroups is a four-ticket defence (#565, #864,
// #923, #915/#924), the backgrounding behaviour is days old and
// claude-version-dependent, and the whole reading rests on a cmd.Cancel ordering
// that any teardown refactor could silently invert with nothing going red.
//
// # The expensive outcome is a MANUFACTURED PASS, not a missed leak
//
// Two facts make fabrication the live risk. The expected reading is published in
// a public comment — pid absent, count=1, held pgid in the list — so any
// criterion of the form "the record states whether the process was alive or
// dead" is satisfiable by transcribing that table. And the pipeline cannot run
// the live half: the dispatch environment has no claude login, the real-claude
// suite SKIPs, and a skip exits 0. Together those mean the compliant-LOOKING
// path is a hand-typed record.
//
// So: the record is a RIG ARTIFACT (writeTdnArtifacts, one file, never composed
// by hand), and the instrument is shown to FLIP inside the very run whose
// verdict it reports. tdnBeforeFault is that gate — the expected after-reading
// is `dead`, so an instrument hard-wired to `dead` passes a live run while
// proving nothing, and a before-snapshot that already reads dead (or finds no
// row) VOIDS the run rather than producing a clean result.
//
// # The three readings this rig has to separate
//
// Three different realities produce "the process was dead after pyry exited",
// and only one means the reaper's reach is intact:
//
//  1. the reaper SIGKILLed its group;
//  2. the command finished on its own;
//  3. it died alongside claude rather than by the reaper's hand.
//
// Both discriminators are STRUCTURAL, never prose. (2) is ruled out by the still
// held FIFO: `cat` cannot reach EOF while this rig holds the write end, and the
// hold outlives the teardown by construction (§ The cleanup-ordering trap). (3)
// is ruled out by a pgid appearing in the reap line, because reap.go:56-62 skips
// ESRCH BEFORE the append — membership in that list proves kill(2) succeeded.
//
// RUNNER-INDEPENDENCE IS AN ARGUMENT FROM THE CODE, NOT A MEASUREMENT.
// ptyrunner (runner.go:314,398,499), streamrunner (runner.go:201) and streamsup
// (runner.go:567) each route teardown through the identical
// agentrun.ReapDescendantGroups behind a reapDescendantGroupsFn seam. Strong
// argument; still an argument. This rig measures ONE runner per run and the
// record names which — from the process table (RunnerFromArgv), not from the
// environment it set.
//
// # The cleanup-ordering trap, which inverts this rig's own result
//
// runReachProbe registers its cleanups so the FIFO is released FIRST,
// deliberately, "so pyry gets a real chance to finish the turn and exit on its
// own" (background_reach_probe_test.go:355-356). Copying that structure here
// silently destroys the measurement: `cat` reaches EOF and exits by itself, and
// the after-snapshot still reads "dead" — producing exactly the clean-but-
// unearned reading (2) describes.
//
// holdProbeFIFO is correct as-is and needs no edit: its release is a t.Cleanup,
// which by construction runs after the test body. So the SIGTERM, the wait and
// the after-snapshot all happen IN THE BODY, which holds the FIFO across the
// teardown for free. What must not be copied is the release-first registration
// order — the defence-in-depth group kill here is registered AFTER
// holdProbeFIFO, so LIFO runs kill-then-release and every reading is already
// taken by then.
//
// # Reused, not rebuilt
//
// Ten of this rig's eleven instruments already exist on main and are CALLED
// here. #1223's staging (spawnProbePyry, holdProbeFIFO, probeSyncBuffer, the
// JSONL waiters), #1230's argv discipline (reachScanArgv's -ww / no-`-E` read,
// reachCapCommand, reachBackgroundHandle, reachRunnerPathFromEnv), #1235's
// content-first scan and four-valued per-pid read (pinScanArgv, pinReadState),
// #1239's reader-presence read (fifoLiveRead) and #1250's reap-log classifier
// and record writer (tdnClassifyReapLog, writeTdnArtifacts). None is edited.
//
// The trigger is settled and is not re-derived: BASH_DEFAULT_TIMEOUT_MS set low
// on the `pyry agent-run` process, 7/7 identical fires on claude 2.1.220
// (#1223). Both runners hand claude pyry's own environment verbatim, so setting
// it on the parent is the whole plumbing story.
//
// #1230's PYRY_USE_STREAMJSON skip gate is deliberately NOT copied. It exists
// because that probe pins claude's pid content-first on `--session-id`, which
// only ptyrunner emits. This rig never needs claude's pid: it anchors on the
// FIFO path in the held command's argv and on pyry's own pid, which it spawned.
// Inheriting the gate would exclude the stream path — the one the operator
// actually measured.
//
// # Redaction — inherited, not re-derived
//
// Never `ps -E`, never `-e` with an environment column, never BSD `eww`: all
// print every process's environment, which on an operator machine means
// CLAUDE_CODE_OAUTH_TOKEN / ANTHROPIC_API_KEY, into an artifact destined for a
// public issue. This rig performs NO environment read and must not gain the
// capability. Both ps call sites are inherited unchanged (reachScanArgv via
// pinScanArgv; pinStateArgs via pinReadState), and pinStateColumns is not
// widened — its doc comment forbids command/args/comm and
// TestPinStateColumns_ReadsNoEnvironment enforces it. `command` reaches the
// record only from the argv scan's already-matched rows. writeTdnArtifacts emits
// exactly ONE file, which is itself #1250's redaction assertion; do not follow
// writeReachArtifacts, whose second file holds a verbatim ps snapshot.
//
// # Running it
//
// The live half is opt-in — it costs one live claude turn, and `make
// e2e-realclaude` runs the whole package glob:
//
//	PYRY_PROBE_TEARDOWN_LIVENESS=1 go test -tags e2e_realclaude -timeout 10m -v \
//	  -run 'TestRealClaude_TeardownLiveness' ./internal/e2e/realclaude/
//
// Run it on BOTH runners (add PYRY_USE_STREAMJSON=1 for the second) and paste
// both artifacts; the record's runner_from_argv is what names which ran. The
// t.Logf summary is the SKIP≠PASS proof — a `go test` exit code cannot tell a
// real run from a skip.
//
// The two self-checks in this file (TestTdnRunnerFromArgv, TestTdnDecideAfter)
// are pure, credential-free and never skip, so they belong to #1250's offline
// suite and keep its zero-SKIP property:
//
//	go test -tags e2e_realclaude -run '^TestTdn' -v ./internal/e2e/realclaude/

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// tdnProbeEnableEnv gates the live half.
const tdnProbeEnableEnv = "PYRY_PROBE_TEARDOWN_LIVENESS"

// tdnProbeTicket is the rig's own provenance. tdnTicket (= "1250") stays the
// offline instrument's and is deliberately not repurposed.
const tdnProbeTicket = "1251"

const (
	// tdnFIFOName is distinct from #1223's probeFIFOName and #1230's
	// reachFIFOName so a concurrently-running probe's `cat` can never satisfy
	// this run's content match.
	tdnFIFOName = "teardown-hold"
	// tdnClaudeNeedle pins claude's own row for the runner label. BOTH runners
	// emit it (ptyrunner/runner.go:620, cmd/pyry/`buildStreamRunnerClaudeArgs`), which is
	// exactly why it identifies claude and never the runner — see
	// tdnRunnerFromArgv.
	//
	// It is disjoint from the FIFO needle by construction: neither runner puts
	// the prompt on claude's argv (both deliver it via PromptBytes), so the FIFO
	// path reaches only the `zsh -c` wrapper and its `cat`; and pyry's own argv
	// carries `--system-prompt-file=`, which does not contain this string.
	tdnClaudeNeedle = "--append-system-prompt-file"
)

// tdnEnvDelta is #1223's settled trigger, set on the `pyry agent-run` process.
// No production change and no forwarding list: both runners hand claude pyry's
// own environment verbatim.
var tdnEnvDelta = []string{"BASH_DEFAULT_TIMEOUT_MS=5000"}

// tdnTeardownPath names the teardown this rig exercises, in the record, because
// the reading is only about the path that produced it. `pyry agent-run` installs
// signal.NotifyContext(SIGTERM, SIGINT) at cmd/pyry/agent_run.go:258, so a
// SIGTERM to its PID cancels the run context and runs the real teardown, reap
// included. A SIGTERM to its GROUP or a SIGKILL would measure a leak pyry's real
// teardown never produces.
const tdnTeardownPath = "operator SIGTERM to the `pyry agent-run` pid " +
	"(signal.NotifyContext, cmd/pyry/agent_run.go:258) — not the budget-hit teardown " +
	"and not the watchdog teardown"

// TestRealClaude_TeardownLiveness stages one live turn, waits for claude to
// background the held command, tears pyry down through its real SIGTERM path,
// and records whether the command survived and by whose hand it died.
//
// It asserts almost nothing. t.Fatalf fires only on structural failure (mkfifo,
// the prompt-file writes, pyry won't build, pyry won't start) — everything else
// is RECORDED, because a probe that turns an unexpected reading into a red test
// loses the reading. Exactly one condition is red: tdnDispositionLeaked.
//
// The name is NOT TestTdn… on purpose. #1250's file advertises its offline suite
// as `-run '^TestTdn'` and its knowledge doc records "22 subtests, zero SKIP" as
// a property of that command; a credential-gated test joining that suite would
// add a SKIP to it and silently falsify the claim.
func TestRealClaude_TeardownLiveness(t *testing.T) {
	if os.Getenv(tdnProbeEnableEnv) != "1" {
		t.Skipf("#1251 teardown-liveness rig: skipped because %s != 1.\n"+
			"It costs one live claude turn, and `make e2e-realclaude` runs the whole "+
			"package glob — so a skip here is the normal outcome and says nothing about "+
			"pyry's behaviour. The rig sets BASH_DEFAULT_TIMEOUT_MS=5000 on the pyry "+
			"process itself (#1223's settled trigger), so no extra environment is needed:\n"+
			"  %s=1 go test -tags e2e_realclaude -timeout 10m -v \\\n"+
			"    -run 'TestRealClaude_TeardownLiveness' ./internal/e2e/realclaude/\n"+
			"Add PYRY_USE_STREAMJSON=1 for the stream path; the record names which ran.",
			tdnProbeEnableEnv, tdnProbeEnableEnv)
	}

	// Deliberately NOT t.TempDir(): that is removed when the test ends, and the
	// operator needs the artifact afterwards to compose the issue comment.
	// MkdirTemp creates it 0700, and writeTdnArtifacts writes 0600 inside it.
	artifactDir, err := os.MkdirTemp("", "pyry-1251-probe-*")
	if err != nil {
		t.Fatalf("create artifact dir: %v", err)
	}
	t.Logf("#1251 rig artifacts: %s", artifactDir)
	runTdnProbe(t, artifactDir)
}

// runTdnProbe is the whole staging, in the ORDER that keeps the measurement
// earnable. Every step's failure routes through tdnFinish, which writes the
// artifact before it skips.
func runTdnProbe(t *testing.T, artifactDir string) {
	// Skips (without credentials) before anything is created.
	workdir := WithWorktreeAuthenticated(t)
	claudeBin := resolveClaudeBin(t)

	fifoPath := filepath.Join(workdir, tdnFIFOName)
	rec := &tdnRecord{
		Ticket:        tdnProbeTicket,
		ClaudeVersion: probeClaudeVersion(claudeBin),
		TeardownPath:  tdnTeardownPath,
		RunnerFromEnv: reachRunnerPathFromEnv(tdnEnvDelta),
		// Overwritten from the before-snapshot's claude row. Seeded with the
		// indeterminate answer so a record written by a structural t.Fatalf
		// never reads as a runner claim nobody made.
		RunnerFromArgv: tdnRunnerFromArgv(""),
		FIFOPath:       fifoPath,
	}
	rec.decide(tdnDispositionSkipped, "the rig did not reach its classification point; no "+
		"claim is made about pyry's teardown")
	tdnSeedNotes(rec)

	// Registered FIRST so LIFO runs it LAST: the record is complete by then, and
	// a structural t.Fatalf below still leaves the evidence on disk.
	t.Cleanup(func() { writeTdnArtifacts(t, artifactDir, rec) })

	// holdProbeFIFO registers its own release cleanup, which by construction
	// runs after the test body — so the write end is held across the teardown
	// and the after-snapshot for free, and `cat` cannot reach EOF and exit by
	// itself. That is the structural discriminator that rules out reading (2).
	rendezvous := holdProbeFIFO(t, fifoPath)

	// Registered AFTER holdProbeFIFO — the deliberate inversion of
	// runReachProbe:355-356. LIFO therefore runs kill-then-release, which is
	// harmless because the body has already taken every reading, and it never
	// releases the FIFO before the measurement.
	pyryExited := make(chan struct{})
	t.Cleanup(func() {
		// LOAD-BEARING. Without this guard a failure before cmd.Start reaches
		// syscall.Kill(-0, SIGKILL), and kill(0, sig) is defined as "send to
		// every process in the CALLER's own process group" — the test binary
		// would SIGKILL itself and its siblings. One line, whole-run blast
		// radius.
		if rec.PyryPID <= 0 {
			return
		}
		select {
		case <-pyryExited:
			return
		default:
		}
		// No grace wait here, unlike runReachProbe: the FIFO write end is STILL
		// HELD at this point by design, so pyry cannot finish the turn on its
		// own and waiting would only burn the deadline.
		rec.note("pyry (pid %d) had not exited when the rig finished, so its group was "+
			"SIGKILLed as defence in depth. The FIFO write end was still held, so it "+
			"could not have completed the turn on its own", rec.PyryPID)
		_ = syscall.Kill(-rec.PyryPID, syscall.SIGKILL)
	})

	promptPath := filepath.Join(workdir, "prompt.txt")
	if err := os.WriteFile(promptPath, []byte(probePrompt(fifoPath)), 0o600); err != nil {
		t.Fatalf("write %s: %v", promptPath, err)
	}
	systemPath := filepath.Join(workdir, "system.txt")
	if err := os.WriteFile(systemPath, []byte(probeSystemPrompt), 0o600); err != nil {
		t.Fatalf("write %s: %v", systemPath, err)
	}

	bin := ensurePyryBuilt(t)
	var stdout, stderr probeSyncBuffer
	cmd := spawnProbePyry(t, bin, workdir, promptPath, systemPath, tdnEnvDelta, &stdout, &stderr)
	rec.PyryPID = cmd.Process.Pid

	// pyry's exit is established by WAITING ON THE PROCESS, never by a Signal(0)
	// probe: pyry is a direct child of this process, so it becomes a zombie
	// between exit and Wait, and a zombie answers Signal(0) with nil. A liveness
	// probe would report "still running" for an already-returned pyry (#1223's
	// lesson). Read the channel.
	go func() {
		_ = cmd.Wait()
		close(pyryExited)
	}()

	sessionID := probeWaitForSessionID(&stdout, probeSessionIDDeadline)
	if sessionID == "" {
		// Recorded and skipped, NOT t.Fatalf as runReachProbe does here: a
		// missing session id is a staging miss, and failing structurally would
		// discard the artifact-bearing path for a condition that is not a
		// finding about pyry.
		// pyry's stderr is LOGGED, never recorded: the artifact is pasted into a
		// public issue and this rig's record carries only classified readings —
		// the one place stderr text reaches it is tdnReapOutcome.Line, which is
		// anchored, capped and about one known message.
		t.Logf("#1251 pyry stderr at the session-id deadline:\n%s", truncate(stderr.Bytes()))
		rec.decide(tdnDispositionSkipped, "no system/init session_id reached pyry's stdout "+
			"within %s, so the JSONL this rig reads its trigger evidence from was never "+
			"located", probeSessionIDDeadline)
		tdnFinish(t, rec, artifactDir)
		return
	}
	rec.note("session id %s; session JSONL %s", sessionID, jsonlPathFor(workdir, sessionID))

	select {
	case <-rendezvous:
	case <-time.After(probeRendezvousDeadline):
		rec.decide(tdnDispositionSkipped, "no reader opened %s within %s — the model never "+
			"issued the Bash call, so nothing was backgrounded to measure",
			fifoPath, probeRendezvousDeadline)
		tdnFinish(t, rec, artifactDir)
		return
	}

	toolUseID, toolUseRaw := probeWaitForBashToolUse(t, workdir, sessionID, probeToolUseDeadline)
	if toolUseID == "" {
		rec.decide(tdnDispositionSkipped, "the rendezvous fired (the command ran) but no Bash "+
			"tool_use reached the session JSONL within %s", probeToolUseDeadline)
		tdnFinish(t, rec, artifactDir)
		return
	}

	// probeWaitForBashToolUse returns the FIRST Bash tool_use regardless of
	// input.command — a deliberately-shipped #1223 gap. If the model issued any
	// other Bash call first, keying off that envelope would time the whole
	// measurement against the wrong tool call. Guarded content-first here rather
	// than by editing the shared rig, and only the boolean is kept: the model's
	// verbatim params are model-controlled bytes this rig has no use for.
	rawInput, _, _ := probeToolUseInput(toolUseRaw, toolUseID)
	if !strings.Contains(reachToolUseCommand(rawInput), fifoPath) {
		rec.decide(tdnDispositionSkipped, "the tracked Bash tool_use (%s) does not run this "+
			"run's FIFO, so it is not the call this rig measures", toolUseID)
		tdnFinish(t, rec, artifactDir)
		return
	}

	resultRaw := probeWaitForToolResult(t, workdir, sessionID, toolUseID, probeToolResultDeadline)
	if resultRaw == nil {
		rec.decide(tdnDispositionSkipped, "no tool_result matched the tracked tool_use within "+
			"%s — the call was still open and the command still blocked, so nothing was "+
			"backgrounded this run", probeToolResultDeadline)
		tdnFinish(t, rec, artifactDir)
		return
	}
	taskID, timedOutAfterMs, handle := reachBackgroundHandle(resultRaw)
	if !handle {
		rec.decide(tdnDispositionSkipped, "the tool_result carries no "+
			"toolUseResult.backgroundTaskId, so the trigger did not fire this run (#1223's "+
			"7-of-7 firing rate is evidence, not a guarantee). A trigger miss is not a pyry "+
			"regression")
		tdnFinish(t, rec, artifactDir)
		return
	}
	rec.note("background handle %s came back (timed_out_after_ms=%s) for Bash tool_use %s",
		taskID, tdnOrNone(timedOutAfterMs), toolUseID)

	// --- the before-snapshot: pin identity content-first, then read liveness ---
	//
	// Neither exclusion should ever fire — pinPartition records one only when it
	// actually matched, so an entry in the artifact is itself the signal that a
	// needle leaked into a process it should not have reached.
	needles := []string{fifoPath, tdnClaudeNeedle}
	exclude := map[int]string{
		os.Getpid(): "the rig's own test binary",
		rec.PyryPID: "the `pyry agent-run` process the rig spawned",
	}

	before := tdnScan(tdnAtBefore, needles, exclude)
	rec.Before = &before
	rec.HeldPIDs, rec.HeldPGID = tdnPinHeld(before.ArgvScan, fifoPath)
	before.complete(fifoPath, rec.HeldPIDs)
	rec.RunnerFromArgv = tdnRunnerFromArgv(tdnClaudeCommand(before.ArgvScan))

	// ONE gate for every way the before-snapshot can fail to support a verdict —
	// a failed scan, no matched row, several process groups, a broken index, or
	// any liveness verdict that is not `running`. Each written out longhand
	// would be its own record field, decide call and reasoning step, which is
	// what pushes a rig like this over its budget.
	if fault := tdnBeforeFault(rec); fault != "" {
		rec.decide(tdnDispositionSkipped, "the run is VOID and is recorded as a staging or "+
			"instrument fault rather than as a clean result: %s", fault)
		tdnFinish(t, rec, artifactDir)
		return
	}
	rec.note("the instrument flipped inside this run: every pinned pid read %q at the "+
		"before-snapshot, and the after-verdict below is read by the same classifier over "+
		"the same pids", pinStateRunning)

	// --- teardown: pyry's real one ---
	//
	// pyry finishing on its own would leave the record naming a teardown that
	// never ran — and it would still produce a clean-looking `dead-by-reaper`,
	// because normal completion reaps too. That is a false provenance claim in a
	// published artifact, and TeardownPath is the one claim AC1 turns on. Both
	// shapes are caught: an already-Waited process refuses the signal, and a
	// zombie whose Wait has not yet returned would ACCEPT it, so the channel is
	// read first.
	select {
	case <-pyryExited:
		rec.decide(tdnDispositionSkipped, "pyry had already exited before the rig sent its "+
			"SIGTERM, so the teardown named in this record is not the one that ran. Recorded "+
			"and skipped rather than reported: normal completion reaps too, so this run would "+
			"otherwise publish a clean reading attributed to the wrong teardown path")
		tdnFinish(t, rec, artifactDir)
		return
	default:
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		rec.decide(tdnDispositionSkipped, "SIGTERM to pyry's pid %d did not land (%v), so the "+
			"teardown named in this record was never invoked by the rig and no reading about "+
			"it is available", rec.PyryPID, err)
		tdnFinish(t, rec, artifactDir)
		return
	}
	select {
	case <-pyryExited:
	case <-time.After(probePyryExitGrace):
		rec.decide(tdnDispositionSkipped, "pyry did not exit within %s of the SIGTERM, so the "+
			"teardown never completed and any after-reading would be about a live pyry",
			probePyryExitGrace)
		tdnFinish(t, rec, artifactDir)
		return
	}

	// --- the after-snapshot, taken with the FIFO write end STILL HELD ---
	after := tdnScan(tdnAtAfter, needles, exclude)
	rec.After = &after
	// The pids are the ones pinned BEFORE: the after-read is a pure liveness
	// read on the pinned pid, and the after scan is the content RE-MATCH that
	// catches a recycled pid — not a re-pin.
	after.complete(fifoPath, rec.HeldPIDs)

	rec.Reap = tdnClassifyReapLog(stderr.Bytes(), rec.HeldPGID)
	tdnDecideAfter(rec)
	tdnFinish(t, rec, artifactDir)
}

// tdnSeedNotes records what this rig does NOT measure and how its two
// discriminators work, BEFORE any measurement — so the statements survive even a
// run that skips at its first step.
func tdnSeedNotes(rec *tdnRecord) {
	rec.note("the two structural discriminators, which is what makes a `dead` reading " +
		"earned rather than merely clean: the FIFO's write end is held ACROSS the " +
		"teardown and the after-snapshot, so the command could not have finished on its " +
		"own; and a pgid appearing in the reaper's line proves kill(2) SUCCEEDED, because " +
		"reap.go:56-62 skips ESRCH before the append — which rules out the command having " +
		"died alongside claude rather than by the reaper's hand")
	rec.note("NOT measured by this run: the terminal/PTY path FOR TEARDOWN; " +
		"internal/streamsup's daemon lifecycle; and runner-independence, which is an " +
		"ARGUMENT from the shared reapDescendantGroupsFn seam (ptyrunner runner.go:314," +
		"398,499; streamrunner runner.go:201; streamsup runner.go:567 all call " +
		"agentrun.ReapDescendantGroups) and NOT a measurement — this record's " +
		"runner_from_argv names the one runner that actually ran")
	rec.note("runner_from_env is DOCUMENTATION, not corroboration: it reads the effective " +
		"environment, which the operator's shell may already have set (it had, on " +
		"2026-07-25, silently invalidating a #1223 gate). runner_from_argv is the evidence " +
		"— it names the runner from the process table this run produced")
}

// --- the two snapshots -------------------------------------------------------

// tdnScan takes the content-match half of one snapshot.
//
// The scan's error and the liveness verdicts are SEPARATE VALUES WITH NO
// CROSS-ASSIGNMENT (#1235's gate-placement obligation): a failed scan is
// recorded as such and never relabels a per-pid reading, and vice versa.
func tdnScan(at string, needles []string, exclude map[int]string) tdnSnapshot {
	snap := tdnSnapshot{At: at}
	scan, err := pinScanArgv(needles, exclude)
	snap.ArgvScan = scan
	if err != nil {
		snap.ScanErr = err.Error()
	}
	return snap
}

// complete fills in the per-pid liveness reads and the FIFO's own reader state,
// in that order. Liveness is index-aligned with pids, which is the invariant the
// record's before/after pairing rests on.
//
// fifoLiveRead is recorded at both points as CORROBORATION, not as a gate: the
// verdict is decided by the liveness read and the content match only, so the
// FIFO state cannot invert it in either direction. Under-gating here costs
// nothing; over-gating costs a reject branch.
func (s *tdnSnapshot) complete(fifoPath string, pids []int) {
	for _, pid := range pids {
		s.Liveness = append(s.Liveness, pinReadState(pid))
	}
	s.FIFO = fifoLiveRead(fifoPath)
}

// tdnPinHeld resolves the held rows out of one scan: every pid whose full
// command line carries the run's FIFO path, and the single process group they
// share.
//
// pids is a SLICE because #1230's live run matched TWO rows on the FIFO needle —
// the `zsh -c` wrapper claude runs Bash through, whose argv carries the whole
// command string, and the `cat` itself. Resolving "the" held pid is the
// first-match defect pinScan.Matches exists to refuse.
//
// pgid is 0 when the held rows do not resolve to exactly one group: the reap
// classification would then have no single subject and the run cannot interpret
// its own staging. The expected shape is one — claude isolates the whole Bash
// command into one detached group, and the hand run measured count=1 on all
// three reps. tdnBeforeFault turns a 0 into the run's void.
func tdnPinHeld(scan pinScan, fifoPath string) (pids []int, pgid int) {
	seenPID := make(map[int]bool)
	seenPGID := make(map[int]bool)
	var pgids []int
	for _, m := range scan.Matches {
		if !reachMatchedNeedle(m, fifoPath) {
			continue
		}
		if !seenPID[m.PID] {
			seenPID[m.PID] = true
			pids = append(pids, m.PID)
		}
		if !seenPGID[m.PGID] {
			seenPGID[m.PGID] = true
			pgids = append(pgids, m.PGID)
		}
	}
	if len(pgids) != 1 {
		return pids, 0
	}
	return pids, pgids[0]
}

// tdnHeldRow returns the matched row for pid whose argv still carries the run's
// FIFO path. It is the content RE-MATCH: the join that separates a genuine leak
// from a recycled pid, kept at the rig level rather than inside the record,
// which is the contract #1250 wrote down for both live consumers.
func tdnHeldRow(scan pinScan, fifoPath string, pid int) (reachProc, bool) {
	for _, m := range scan.Matches {
		if m.PID == pid && reachMatchedNeedle(m, fifoPath) {
			return m, true
		}
	}
	return reachProc{}, false
}

// tdnClaudeCommand returns claude's own command line from one scan, or "" when
// zero or several rows carry the claude needle. The runner label is PROVENANCE,
// not a precondition, so an ambiguous read is recorded as indeterminate rather
// than skipping the run.
func tdnClaudeCommand(scan pinScan) string {
	var found string
	n := 0
	for _, m := range scan.Matches {
		if !reachMatchedNeedle(m, tdnClaudeNeedle) {
			continue
		}
		n++
		found = m.Command
	}
	if n != 1 {
		return ""
	}
	return found
}

// --- the disposition ---------------------------------------------------------

// tdnBeforeFault names why the before-snapshot cannot support a verdict, or
// returns "" when it can.
//
// This is AC2's FLIP GATE and it is the reason the known answer is earnable
// rather than assumable. The expected after-reading is `dead`, so an instrument
// hard-wired to `dead` passes a live run while proving nothing; requiring the
// same classifier to have read `alive` against the same pids, in the same run,
// costs one extra call and closes that hole. A before-snapshot that already
// reads dead, or finds no row, VOIDS the run — it is recorded as a staging or
// instrument fault and NEVER as a clean result.
func tdnBeforeFault(rec *tdnRecord) string {
	switch {
	case rec.Before == nil:
		return "no before-snapshot was taken"
	case rec.Before.ScanErr != "":
		return fmt.Sprintf("the before-snapshot's argv scan failed (%s), so no pid was pinned "+
			"content-first", rec.Before.ScanErr)
	case len(rec.HeldPIDs) == 0:
		return "no process-table row carried the run's FIFO path at the before-snapshot, so " +
			"there was nothing to watch across the teardown"
	case rec.HeldPGID <= 1:
		return fmt.Sprintf("the %d held row(s) do not resolve to a single process group "+
			"(held_pgid recorded as %d), so the reap classification would have no single "+
			"subject", len(rec.HeldPIDs), rec.HeldPGID)
	case len(rec.Before.Liveness) != len(rec.HeldPIDs):
		return fmt.Sprintf("the before-snapshot holds %d liveness reading(s) for %d pinned "+
			"pid(s); the record's index alignment is broken",
			len(rec.Before.Liveness), len(rec.HeldPIDs))
	}
	for i, out := range rec.Before.Liveness {
		if out.PID != rec.HeldPIDs[i] {
			return fmt.Sprintf("before-liveness entry %d is about pid %d but the pinned pid at "+
				"that index is %d; the record's index alignment is broken",
				i, out.PID, rec.HeldPIDs[i])
		}
		if out.Verdict != pinStateRunning {
			return fmt.Sprintf("the before-snapshot read pid %d as %q rather than %q (%s), so "+
				"the instrument was never seen to flip and its after-teardown reading would "+
				"be assumed rather than earned", out.PID, out.Verdict, pinStateRunning, out.Detail)
		}
	}
	return ""
}

// tdnDecideAfter sets the record's disposition from the two snapshots and the
// reap classification. It is PURE over rec — no exec, no clock, no *testing.T —
// which is what lets TestTdnDecideAfter drive every arm offline.
//
// A POSITIVE ALLOWLIST. Once the preconditions hold, ONLY an after-verdict of
// pinStateRunning whose content re-match still identifies this run's command
// fails the test; everything ambiguous skips. The two rules the ordering
// encodes:
//
//   - INSTRUMENT FAILURES ARE CHECKED BEFORE THE LEAK ARM. A broken instrument
//     is not a pyry regression and must never be the thing that opens a leak
//     ticket. The skip's Detail enumerates every after-verdict, so a
//     leak-shaped reading alongside a broken one is still visible to an
//     operator reading the artifact — it just does not get to be the verdict.
//   - THE CONTENT RE-MATCH IS DISPOSITIVE ONLY FOR `running`. A zombie also
//     fails the content match, because the kernel replaces a defunct process's
//     argv (`<defunct>` on Linux, `(cat)` on macOS), so its row no longer
//     carries the FIFO needle. A naive "alive-ish and the needle is gone ⇒ pid
//     reuse" rule would misfile every zombie. For pinStateExitedNotReaped and
//     pinStateNoSuchProcess the needle's absence is EXPECTED and carries no
//     information: the needle was consumed at the before-snapshot and never has
//     to survive into the after-read.
func tdnDecideAfter(rec *tdnRecord) {
	if fault := tdnBeforeFault(rec); fault != "" {
		rec.decide(tdnDispositionSkipped, "the run is VOID and is recorded as a staging or "+
			"instrument fault rather than as a clean result: %s", fault)
		return
	}
	if rec.After == nil {
		rec.decide(tdnDispositionSkipped, "no after-snapshot was taken, so nothing is known "+
			"about the command's state after pyry exited")
		return
	}
	if rec.After.ScanErr != "" {
		rec.decide(tdnDispositionSkipped, "instrument fault: the after-snapshot's argv scan "+
			"failed (%s), so the content re-match that separates a genuine leak from a "+
			"recycled pid was never taken", rec.After.ScanErr)
		return
	}
	if len(rec.After.Liveness) != len(rec.Before.Liveness) {
		rec.decide(tdnDispositionSkipped, "instrument fault: the after-snapshot holds %d "+
			"liveness reading(s) against the before-snapshot's %d, so the two are not about "+
			"the same processes", len(rec.After.Liveness), len(rec.Before.Liveness))
		return
	}

	for i, out := range rec.After.Liveness {
		if out.PID != rec.HeldPIDs[i] {
			rec.decide(tdnDispositionSkipped, "instrument fault: after-liveness entry %d is "+
				"about pid %d but the pinned pid at that index is %d; the record's index "+
				"alignment is broken", i, out.PID, rec.HeldPIDs[i])
			return
		}
		if out.Verdict == pinStateInstrumentFailed {
			rec.decide(tdnDispositionSkipped, "instrument fault: the after-liveness read for "+
				"pid %d could not be taken (%s). A broken instrument is not a pyry regression "+
				"and must never be the thing that opens a leak ticket. Every after-verdict, "+
				"for the record: %s", out.PID, out.Detail, tdnVerdictSummary(rec.After.Liveness))
			return
		}
	}
	if rec.Reap.Verdict == tdnReapInstrumentFailed {
		// Deliberately terse: the classifier's own Detail already says which arm
		// fired and why it is not an answer, and every byte spent restating it
		// here is a byte reachCapCommand would cut off that Detail.
		rec.decide(tdnDispositionSkipped, "instrument fault: the reaper's log line could not "+
			"be classified, so no statement about the reaper is available (%s)", rec.Reap.Detail)
		return
	}

	// The one red arm, scanned across EVERY pid before the reuse arm is
	// considered: a confirmed leak anywhere outranks a recycled pid elsewhere.
	for _, out := range rec.After.Liveness {
		if out.Verdict != pinStateRunning {
			continue
		}
		if row, matched := tdnHeldRow(rec.After.ArgvScan, rec.FIFOPath, out.PID); matched {
			rec.decide(tdnDispositionLeaked, "pid %d is STILL RUNNING after pyry exited "+
				"(stat=%q) and its argv still carries the run's FIFO path, so it is the same "+
				"process this run pinned and not a recycled pid. pgid %d survived pyry's "+
				"teardown. Matched row: %s",
				out.PID, out.StateColumn, rec.HeldPGID, row.Command)
			return
		}
	}
	for _, out := range rec.After.Liveness {
		if out.Verdict != pinStateRunning {
			continue
		}
		rec.decide(tdnDispositionSkipped, "pid %d reads %q after pyry exited, but its argv no "+
			"longer carries the run's FIFO path — the pid was RECYCLED between the two "+
			"snapshots and that row is about a different process. Recorded and skipped: pid "+
			"reuse is not a leak, and it is caught by re-running the content match rather "+
			"than by widening the per-pid read", out.PID, out.Verdict)
		return
	}

	for _, out := range rec.After.Liveness {
		if out.Verdict != pinStateNoSuchProcess && out.Verdict != pinStateExitedNotReaped {
			rec.decide(tdnDispositionSkipped, "unrecognised after-verdict %q for pid %d; this "+
				"rig's allowlist has no arm for it, so no claim is made", out.Verdict, out.PID)
			return
		}
	}

	// Every pinned pid is dead, and the held FIFO rules out its having finished
	// on its own. What remains is by whose hand.
	switch rec.Reap.Verdict {
	case tdnReapHeldPGIDKilled:
		rec.decide(tdnDispositionReaperKilled, "every pinned pid is dead after pyry exited "+
			"(%s), the FIFO's write end was held across the teardown so the command could "+
			"not have finished on its own, and the reaper reported killing pgid %d — which "+
			"proves kill(2) succeeded, because reap.go:56-62 skips ESRCH before the append. "+
			"The reaper's reach covers backgrounded commands",
			tdnVerdictSummary(rec.After.Liveness), rec.HeldPGID)
	case tdnReapNoLine:
		rec.decide(tdnDispositionDeadUnattributed, "every pinned pid is dead after pyry "+
			"exited (%s) and the held FIFO rules out its having finished on its own, but no "+
			"reap line appears in pyry's stderr. That silence is AMBIGUOUS BY CONSTRUCTION — "+
			"reap.go:64 guards the emit on len(reaped) > 0, so it means the reaper ran and "+
			"reaped nothing OR that it never fired — and is recorded as such, never as \"the "+
			"reaper never ran\". The reaper's hand is NOT established by this run",
			tdnVerdictSummary(rec.After.Liveness))
	default:
		rec.decide(tdnDispositionDeadUnattributed, "every pinned pid is dead after pyry "+
			"exited (%s) and the held FIFO rules out its having finished on its own, but the "+
			"reaper emitted its line without pgid %d among the groups it reported killing "+
			"(%v). The command is dead; the reaper's hand is NOT established by this run",
			tdnVerdictSummary(rec.After.Liveness), rec.HeldPGID, rec.Reap.PGIDs)
	}
}

// --- the runner label --------------------------------------------------------

// tdnRunnerFromArgv names the runner from claude's own command line.
//
// reachRunnerPathFromArgv is deliberately
// NOT reused: it keys on --append-system-prompt-file and its comment calls that
// "the ptyrunner-shape marker", but buildStreamRunnerClaudeArgs
// (cmd/pyry/`buildStreamRunnerClaudeArgs`) emits the identical flag, so it answers
// "ptyrunner" on the streamrunner path too. That is harmless in #1230, which
// skips outright under PYRY_USE_STREAMJSON=1; this rig deliberately does not
// copy that gate, so reusing the helper would mislabel the record on the stream
// path with nothing going red.
//
// The two discriminating markers, each emitted by exactly one argv builder:
// --session-id by ptyrunner.buildArgs (runner.go:618) and --input-format by
// buildStreamRunnerClaudeArgs. Both or neither is
// indeterminate rather than a guess.
func tdnRunnerFromArgv(claudeCommand string) string {
	if strings.TrimSpace(claudeCommand) == "" {
		return "indeterminate (no single claude row was pinned, so the runner was not read " +
			"from the process table)"
	}
	session := strings.Contains(claudeCommand, "--session-id")
	input := strings.Contains(claudeCommand, "--input-format")
	switch {
	case session && input:
		return "indeterminate (claude argv carries BOTH --session-id and --input-format, " +
			"which no single runner emits)"
	case session:
		return "ptyrunner (claude argv carries --session-id)"
	case input:
		return "streamrunner (claude argv carries --input-format)"
	default:
		return "indeterminate (claude argv carries NEITHER --session-id nor --input-format)"
	}
}

// --- finishing ---------------------------------------------------------------

// tdnFinish is the ONE exit for every path: it writes the artifact, logs the
// summary, and turns the disposition into a test outcome.
//
// The artifact is written BEFORE any t.Errorf or t.Skip, so a red run still
// carries its reading. The cleanup registered at the top of the body writes
// again — same path, idempotent overwrite — which is the net under a structural
// t.Fatalf.
//
// The per-phase t.Logf output is the SKIP≠PASS proof: a transcript must
// distinguish a real run from a skipped one at a glance, which a `go test` exit
// code cannot.
func tdnFinish(t *testing.T, rec *tdnRecord, artifactDir string) {
	t.Helper()
	writeTdnArtifacts(t, artifactDir, rec)
	artifact := filepath.Join(artifactDir, tdnArtifactName)

	t.Logf("#1251 disposition=%s\n  %s", rec.Disposition, rec.DispositionDetail)
	t.Logf("#1251 runner: env=%s | argv=%s | claude %s\n  teardown: %s",
		rec.RunnerFromEnv, rec.RunnerFromArgv, rec.ClaudeVersion, rec.TeardownPath)
	t.Logf("#1251 pyry pid=%d | held pids=%v pgid=%d | fifo=%s",
		rec.PyryPID, rec.HeldPIDs, rec.HeldPGID, rec.FIFOPath)
	tdnLogSnapshot(t, rec.Before)
	tdnLogSnapshot(t, rec.After)
	t.Logf("#1251 reap: %s pgids=%v count=%d lines=%d\n  %s",
		rec.Reap.Verdict, rec.Reap.PGIDs, rec.Reap.Count, rec.Reap.LineCount, rec.Reap.Detail)
	t.Logf("#1251 artifact: %s", artifact)

	switch rec.Disposition {
	case tdnDispositionLeaked:
		// The rig goes red and prints; filing the follow-up is an operator
		// action off the back of the artifact, deliberately not automated here.
		t.Errorf("#1251 LEAK — a backgrounded Bash command SURVIVED pyry's teardown.\n"+
			"  %s\n"+
			"  leaked pgid:   %d (held pids %v)\n"+
			"  teardown path: %s\n"+
			"  runner:        %s\n"+
			"A follow-up ticket is owed naming those three values; this rig does not file "+
			"it. Artifact: %s",
			rec.DispositionDetail, rec.HeldPGID, rec.HeldPIDs, rec.TeardownPath,
			rec.RunnerFromArgv, artifact)
	case tdnDispositionSkipped:
		t.Skipf("#1251 recorded and skipped: %s\nArtifact: %s", rec.DispositionDetail, artifact)
	}
}

func tdnLogSnapshot(t *testing.T, snap *tdnSnapshot) {
	t.Helper()
	if snap == nil {
		return
	}
	t.Logf("#1251 %s: liveness=%s | fifo=%s | argv scan matched %d of %d rows%s",
		snap.At, tdnVerdictSummary(snap.Liveness), snap.FIFO.Verdict,
		snap.ArgvScan.MatchCount, snap.ArgvScan.RowsScanned, tdnScanErrSuffix(snap.ScanErr))
	for _, out := range snap.Liveness {
		t.Logf("#1251   pid=%d verdict=%s stat=%q ppid=%d", out.PID, out.Verdict,
			out.StateColumn, out.PPID)
	}
	for _, m := range snap.ArgvScan.Matches {
		t.Logf("#1251   row pid=%d ppid=%d pgid=%d command=%s", m.PID, m.PPID, m.PGID, m.Command)
	}
}

// tdnVerdictSummary renders one snapshot's liveness readings compactly, so a
// skip's Detail can carry every verdict rather than only the one that fired.
func tdnVerdictSummary(outcomes []pinStateOutcome) string {
	if len(outcomes) == 0 {
		return "<no pid was read>"
	}
	parts := make([]string, 0, len(outcomes))
	for _, out := range outcomes {
		parts = append(parts, fmt.Sprintf("pid=%d %s", out.PID, out.Verdict))
	}
	return strings.Join(parts, ", ")
}

func tdnScanErrSuffix(scanErr string) string {
	if scanErr == "" {
		return ""
	}
	return " | scan error: " + scanErr
}

// tdnOrNone keeps an empty field from reading as a missing one in a note, where
// "" and "absent" look identical.
func tdnOrNone(s string) string {
	if s == "" {
		return "<none recorded>"
	}
	return s
}

// --- credential-free self-checks ---------------------------------------------
//
// Both are pure and neither skips, so they belong to #1250's offline suite:
//
//	go test -tags e2e_realclaude -run '^TestTdn' -v ./internal/e2e/realclaude/

// tdnFixturePtyArgv and tdnFixtureStreamArgv are the two runners' real claude
// command lines, built from their actual argv builders — ptyrunner.buildArgs
// (runner.go:616-625) and buildStreamRunnerClaudeArgs (agent_run.go:364-378).
// Both carry --append-system-prompt-file, which is precisely why that flag
// cannot name a runner.
const (
	tdnFixturePtyArgv = `/opt/node/bin/node /opt/claude/cli.js --session-id ` +
		`11111111-2222-3333-4444-555555555555 --settings /tmp/s.json --permission-mode ` +
		`dontAsk --append-system-prompt-file /tmp/wd/system.txt --model claude-haiku-4-5 ` +
		`--effort low`

	tdnFixtureStreamArgv = `/opt/node/bin/node /opt/claude/cli.js --input-format ` +
		`stream-json --output-format stream-json --verbose --dangerously-skip-permissions ` +
		`--append-system-prompt-file /tmp/wd/system.txt --model claude-haiku-4-5 --effort ` +
		`low --max-turns 6 --allowed-tools Bash`
)

func TestTdnRunnerFromArgv(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    string
	}{
		{
			name:    "a real ptyrunner argv names ptyrunner",
			command: tdnFixturePtyArgv,
			want:    "ptyrunner",
		},
		{
			name:    "a real streamrunner argv names streamrunner",
			command: tdnFixtureStreamArgv,
			want:    "streamrunner",
		},
		{
			// THE regression guard against re-adopting reachRunnerPathFromArgv,
			// which keys on this flag alone and calls it "the ptyrunner-shape
			// marker". Both runners emit it, so on its own it names nothing.
			name:    "--append-system-prompt-file alone names no runner",
			command: `/opt/node/bin/node /opt/claude/cli.js --append-system-prompt-file /tmp/wd/system.txt`,
			want:    "indeterminate",
		},
		{
			name:    "both markers is indeterminate, not a guess",
			command: tdnFixturePtyArgv + " --input-format stream-json",
			want:    "indeterminate",
		},
		{
			name:    "no claude row pinned",
			command: "",
			want:    "indeterminate",
		},
		{
			name:    "whitespace is not a command line",
			command: "   \t ",
			want:    "indeterminate",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tdnRunnerFromArgv(tc.command)
			if !strings.HasPrefix(got, tc.want) {
				t.Fatalf("tdnRunnerFromArgv(%q) = %q; want it to start with %q",
					tc.command, got, tc.want)
			}
			if !strings.Contains(got, "(") {
				t.Errorf("tdnRunnerFromArgv returned %q with no parenthesised reason; a "+
					"runner label in a published artifact has to say what it was read off", got)
			}
		})
	}
}

// The decision table's fixtures. tdnDecideHeldTable is the shape a still-held
// command produces — the `zsh -c` wrapper and its `cat`, one shared pgid, both
// carrying the run's FIFO path. tdnDecideRecycledTable is the same two pids
// carrying DIFFERENT commands, which is what a recycled pid looks like.
const (
	tdnDecideFIFO = "/tmp/run-t/teardown-hold"

	tdnDecideHeldTable = `
  300   200   300 /bin/zsh -c cat /tmp/run-t/teardown-hold
  400   300   300 cat /tmp/run-t/teardown-hold
`

	tdnDecideRecycledTable = `
  300   200   300 /usr/sbin/cupsd -l
  400   300   300 /usr/bin/something-else --after-the-pid-was-recycled
`

	// An anchored reap line whose pgids= attribute is missing entirely.
	tdnDecideBrokenReapLine = `2026/07/30 23:18:29 INFO ` +
		`agentrun: reaped claude descendant process groups count=1`
)

func TestTdnDecideAfter(t *testing.T) {
	running := func(pid, ppid int) pinStateOutcome {
		return pinClassifyState(pid, []byte(fmt.Sprintf("%d %d S\n", pid, ppid)), nil)
	}
	zombie := func(pid, ppid int) pinStateOutcome {
		return pinClassifyState(pid, []byte(fmt.Sprintf("%d %d Z\n", pid, ppid)), nil)
	}
	gone := func(t *testing.T, pid int) pinStateOutcome {
		t.Helper()
		return pinClassifyState(pid, nil, pinExit1(t, ""))
	}
	broken := func(t *testing.T, pid int) pinStateOutcome {
		t.Helper()
		return pinClassifyState(pid, nil, pinExit1(t, "ps: nosuchcol: keyword not found"))
	}
	bothRunning := func(t *testing.T) []pinStateOutcome {
		t.Helper()
		return []pinStateOutcome{running(300, 200), running(400, 300)}
	}

	tests := []struct {
		name         string
		heldPGID     int
		before       func(t *testing.T) []pinStateOutcome
		after        func(t *testing.T) []pinStateOutcome
		afterTable   string
		reapStderr   string
		want         string
		wantDetailIn []string
	}{
		{
			name:       "both pids gone and the reaper named the held pgid",
			heldPGID:   tdnFixtureHeldPGID,
			before:     bothRunning,
			after:      func(t *testing.T) []pinStateOutcome { return []pinStateOutcome{gone(t, 300), gone(t, 400)} },
			afterTable: tdnDecideRecycledTable,
			reapStderr: tdnFixtureDefaultOne,
			want:       tdnDispositionReaperKilled,
			// The two structural discriminators must be stated in the verdict
			// that rests on them, not left to a file comment.
			wantDetailIn: []string{"could not have finished on its own", "ESRCH"},
		},
		{
			name:       "both pids gone but no reap line: dead, hand not established",
			heldPGID:   tdnFixtureHeldPGID,
			before:     bothRunning,
			after:      func(t *testing.T) []pinStateOutcome { return []pinStateOutcome{gone(t, 300), gone(t, 400)} },
			afterTable: tdnDecideRecycledTable,
			reapStderr: tdnFixtureOtherLines,
			want:       tdnDispositionDeadUnattributed,
			// no-reap-line is AMBIGUOUS BY CONSTRUCTION and must never be
			// recorded as "the reaper never ran".
			wantDetailIn: []string{"AMBIGUOUS", "reaped nothing", "never fired"},
		},
		{
			name:     "both pids gone and the reap line does not carry the held pgid",
			heldPGID: 4242,
			before:   bothRunning,
			after:    func(t *testing.T) []pinStateOutcome { return []pinStateOutcome{gone(t, 300), gone(t, 400)} },
			// tdnFixtureDefaultOne reaps 89355 only, so 4242 is absent.
			afterTable:   tdnDecideRecycledTable,
			reapStderr:   tdnFixtureDefaultOne,
			want:         tdnDispositionDeadUnattributed,
			wantDetailIn: []string{"NOT established"},
		},
		{
			name:     "a zombie counts as dead, and its missing needle is not pid reuse",
			heldPGID: tdnFixtureHeldPGID,
			before:   bothRunning,
			after: func(t *testing.T) []pinStateOutcome {
				return []pinStateOutcome{zombie(300, 200), gone(t, 400)}
			},
			// The needle is GONE from the after table, which is exactly what a
			// defunct process's replaced argv produces. The § join rule's guard:
			// this must NOT be read as pid reuse.
			afterTable:   tdnDecideRecycledTable,
			reapStderr:   tdnFixtureDefaultOne,
			want:         tdnDispositionReaperKilled,
			wantDetailIn: []string{pinStateExitedNotReaped},
		},
		{
			name:         "a running pid whose argv still carries the FIFO path is THE leak",
			heldPGID:     tdnFixtureHeldPGID,
			before:       bothRunning,
			after:        func(t *testing.T) []pinStateOutcome { return []pinStateOutcome{running(300, 200), gone(t, 400)} },
			afterTable:   tdnDecideHeldTable,
			reapStderr:   tdnFixtureDefaultOne,
			want:         tdnDispositionLeaked,
			wantDetailIn: []string{"STILL RUNNING", "not a recycled pid"},
		},
		{
			name:     "a running pid whose needle is gone is a recycled pid, never a leak",
			heldPGID: tdnFixtureHeldPGID,
			before:   bothRunning,
			after:    func(t *testing.T) []pinStateOutcome { return []pinStateOutcome{running(300, 200), gone(t, 400)} },
			// Same pid, different command: the row is about another process.
			afterTable:   tdnDecideRecycledTable,
			reapStderr:   tdnFixtureDefaultOne,
			want:         tdnDispositionSkipped,
			wantDetailIn: []string{"RECYCLED"},
		},
		{
			name:         "a broken after-liveness read never opens a leak ticket",
			heldPGID:     tdnFixtureHeldPGID,
			before:       bothRunning,
			after:        func(t *testing.T) []pinStateOutcome { return []pinStateOutcome{broken(t, 300), gone(t, 400)} },
			afterTable:   tdnDecideHeldTable,
			reapStderr:   tdnFixtureDefaultOne,
			want:         tdnDispositionSkipped,
			wantDetailIn: []string{"instrument fault", "must never be the thing that opens a leak ticket"},
		},
		{
			name:     "a broken before-liveness read voids the run",
			heldPGID: tdnFixtureHeldPGID,
			before: func(t *testing.T) []pinStateOutcome {
				return []pinStateOutcome{broken(t, 300), running(400, 300)}
			},
			after:        func(t *testing.T) []pinStateOutcome { return []pinStateOutcome{gone(t, 300), gone(t, 400)} },
			afterTable:   tdnDecideRecycledTable,
			reapStderr:   tdnFixtureDefaultOne,
			want:         tdnDispositionSkipped,
			wantDetailIn: []string{"VOID", "never seen to flip"},
		},
		{
			name:     "a before-snapshot that already reads dead voids the run",
			heldPGID: tdnFixtureHeldPGID,
			before: func(t *testing.T) []pinStateOutcome {
				return []pinStateOutcome{gone(t, 300), running(400, 300)}
			},
			after:        func(t *testing.T) []pinStateOutcome { return []pinStateOutcome{gone(t, 300), gone(t, 400)} },
			afterTable:   tdnDecideRecycledTable,
			reapStderr:   tdnFixtureDefaultOne,
			want:         tdnDispositionSkipped,
			wantDetailIn: []string{"VOID", "assumed rather than earned"},
		},
		{
			name:         "an unclassifiable reap line records and skips",
			heldPGID:     tdnFixtureHeldPGID,
			before:       bothRunning,
			after:        func(t *testing.T) []pinStateOutcome { return []pinStateOutcome{gone(t, 300), gone(t, 400)} },
			afterTable:   tdnDecideRecycledTable,
			reapStderr:   tdnDecideBrokenReapLine,
			want:         tdnDispositionSkipped,
			wantDetailIn: []string{"instrument fault", tdnReapHeldPGIDAbsent},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := &tdnRecord{
				Ticket:   tdnProbeTicket,
				FIFOPath: tdnDecideFIFO,
				HeldPGID: tc.heldPGID,
				HeldPIDs: []int{300, 400},
				Before: &tdnSnapshot{
					At:       tdnAtBefore,
					ArgvScan: pinMatchArgvExcluding([]byte(tdnDecideHeldTable), []string{tdnDecideFIFO}, nil),
					Liveness: tc.before(t),
				},
				After: &tdnSnapshot{
					At:       tdnAtAfter,
					ArgvScan: pinMatchArgvExcluding([]byte(tc.afterTable), []string{tdnDecideFIFO}, nil),
					Liveness: tc.after(t),
				},
				Reap: tdnClassifyReapLog([]byte(tc.reapStderr), tc.heldPGID),
			}

			tdnDecideAfter(rec)

			if rec.Disposition != tc.want {
				t.Fatalf("disposition: got %q (%s), want %q",
					rec.Disposition, rec.DispositionDetail, tc.want)
			}
			if !tdnIsDisposition(rec.Disposition) {
				t.Errorf("disposition %q is not one of the recorded values", rec.Disposition)
			}
			if rec.DispositionDetail == "" {
				t.Error("empty disposition detail: a verdict that cannot say what earned it " +
					"is indistinguishable from a transcribed one")
			}
			for _, want := range tc.wantDetailIn {
				if !strings.Contains(rec.DispositionDetail, want) {
					t.Errorf("detail: got %q, want it to contain %q", rec.DispositionDetail, want)
				}
			}
		})
	}

	t.Run("a missing after-snapshot is a skip, never a dead reading", func(t *testing.T) {
		rec := &tdnRecord{
			Ticket:   tdnProbeTicket,
			FIFOPath: tdnDecideFIFO,
			HeldPGID: tdnFixtureHeldPGID,
			HeldPIDs: []int{300},
			Before: &tdnSnapshot{
				At:       tdnAtBefore,
				ArgvScan: pinMatchArgvExcluding([]byte(tdnDecideHeldTable), []string{tdnDecideFIFO}, nil),
				Liveness: []pinStateOutcome{running(300, 200)},
			},
			Reap: tdnClassifyReapLog([]byte(tdnFixtureDefaultOne), tdnFixtureHeldPGID),
		}
		tdnDecideAfter(rec)
		if rec.Disposition != tdnDispositionSkipped {
			t.Fatalf("disposition: got %q (%s), want %q",
				rec.Disposition, rec.DispositionDetail, tdnDispositionSkipped)
		}
	})
}

func TestTdnPinHeld(t *testing.T) {
	t.Run("both held rows are returned, sharing one pgid", func(t *testing.T) {
		// The anti-first-match property, asserted rather than commented: #1230's
		// live run matched TWO rows on the FIFO needle.
		scan := pinMatchArgvExcluding([]byte(tdnDecideHeldTable), []string{tdnDecideFIFO}, nil)
		pids, pgid := tdnPinHeld(scan, tdnDecideFIFO)
		if len(pids) != 2 || pids[0] != 300 || pids[1] != 400 {
			t.Fatalf("held pids: got %v, want [300 400] — resolving \"the\" held pid is the "+
				"first-match defect pinScan.Matches exists to refuse", pids)
		}
		if pgid != 300 {
			t.Errorf("held pgid: got %d, want 300", pgid)
		}
	})

	t.Run("held rows spanning two process groups resolve to no pgid", func(t *testing.T) {
		const split = `
  300   200   300 /bin/zsh -c cat /tmp/run-t/teardown-hold
  400   300   400 cat /tmp/run-t/teardown-hold
`
		scan := pinMatchArgvExcluding([]byte(split), []string{tdnDecideFIFO}, nil)
		pids, pgid := tdnPinHeld(scan, tdnDecideFIFO)
		if pgid != 0 {
			t.Errorf("held pgid: got %d, want 0 — the reap classification would have no "+
				"single subject, so the run cannot interpret its own staging", pgid)
		}
		// The pids are still returned: they are evidence in the record even
		// though no verdict is reachable from them.
		if len(pids) != 2 {
			t.Errorf("held pids: got %v, want both rows recorded", pids)
		}
	})

	t.Run("no matching row resolves to nothing", func(t *testing.T) {
		scan := pinMatchArgvExcluding([]byte(tdnDecideRecycledTable), []string{tdnDecideFIFO}, nil)
		pids, pgid := tdnPinHeld(scan, tdnDecideFIFO)
		if len(pids) != 0 || pgid != 0 {
			t.Errorf("got pids=%v pgid=%d, want neither", pids, pgid)
		}
	})

	t.Run("the claude row is pinned by its own needle, not the FIFO's", func(t *testing.T) {
		const table = `
  200   100   200 /opt/node/bin/node /opt/claude/cli.js --session-id 1111 --append-system-prompt-file /tmp/wd/system.txt
  300   200   300 /bin/zsh -c cat /tmp/run-t/teardown-hold
`
		scan := pinMatchArgvExcluding([]byte(table),
			[]string{tdnDecideFIFO, tdnClaudeNeedle}, nil)
		pids, _ := tdnPinHeld(scan, tdnDecideFIFO)
		if len(pids) != 1 || pids[0] != 300 {
			t.Errorf("held pids: got %v, want [300] — the two needles are disjoint by "+
				"construction, so claude's row must not enter the held set", pids)
		}
		if got := tdnRunnerFromArgv(tdnClaudeCommand(scan)); !strings.HasPrefix(got, "ptyrunner") {
			t.Errorf("runner from the pinned claude row: got %q, want ptyrunner", got)
		}
	})
}

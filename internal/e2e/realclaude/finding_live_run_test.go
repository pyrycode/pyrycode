//go:build e2e_realclaude

package realclaude

// The live staging driver: one `pyry agent-run` turn on the ptyrunner default,
// staged to the instant a process pin is meaningful, pinned there, and handed
// back as a handle carrying the run's facts and the staging tier's verdict.
//
// This file reaches no verdict about pyry and takes no measurement of its own.
// It spawns, holds, waits, pins, assembles and hands back; every reading it
// forwards was produced by a shipped reduction, and every disposition it
// forwards was produced by the shipped gate.
//
// # It ships no test, and that is the whole testing strategy
//
// The driver is NOT offline-drivable — that is the entire reason its three
// blockers exist, and the reachable behaviour is already covered by their traps:
// the pin reduction and its count (#1338), the staged command literal, prompt,
// FIFO name and env delta (#1342), and the eight-field record assembly (#1343).
// What is left here is the part that genuinely needs a live process, and it is
// exercised end-to-end only by the live entry point in #1337.
//
// So finLiveRunStage has NO CALLER anywhere in the tree until #1337 lands. That
// is correct and expected. Do NOT invent a caller to silence a lint that does
// not run, and do NOT add an offline test that spawns pyry or a real claude.
// This file's exercise is compilation and `go test`'s vet subset under
// `make e2e-realclaude`, on a machine with no Claude login — which is why
// `needs-real-claude` sits on #1337 and not here. Note that `go vet` and
// `staticcheck` in `make check` run WITHOUT -tags e2e_realclaude, so neither
// analyses this file; `make e2e-realclaude` is the gate.
//
// The FIFO-name distinctness rule is NOT re-declared here. It shipped in #1342
// as TestFinLiveStageFIFONameIsDisjointFromEveryShippedName
// (finding_live_staging_test.go:363), which pins finLiveStageFIFOName
// substring-disjoint in both directions against every other shipped name. This
// file CONSUMES the name; it does not declare it, so it owes no trap for it.
//
// # Nothing the blockers ship is re-derived
//
// The staged command, prompt, FIFO name, env delta and system prompt come from
// #1342; the eight-field assembly and its five caller-side facts from #1343; the
// row count, process-group projection, matched rows and claude argv from #1338's
// pure reduction. The driver forwards. It does not filter the scan, count it,
// dedupe it, rebuild the record, or re-derive the verdict.
//
// # Captured-bytes discipline
//
// NO `ps -E` AND NO `-Eww`, anywhere. Those dump CLAUDE_CODE_OAUTH_TOKEN and
// ANTHROPIC_API_KEY, and this driver needs no environment read of any process at
// all. Its one content scan is pinScanArgv → reachScanArgv's
// `ps -axww -o pid=,ppid=,pgid=,command=` (background_reach_probe_test.go:876) —
// `-ww` widens output without touching the environment.
//
// Matched rows and claude's argv cross the handle as the CAPPED reachProc.Command
// the shipped matcher already produces (reachCapCommand, :945). Do NOT re-read an
// uncapped argv to "repair" a truncation: the cap is the discipline, not a defect,
// and finLivePinReduce's membership test reads the recorded needle list precisely
// so a truncated row still counts (finding_live_pin_test.go:151-158).
//
// This file writes no artifact, logs no captured string and formats no Detail at
// all. The only Detail on the handle is the gate's, which quotes neither operand
// of its identity arm by construction (finding_staging_gate_test.go:304). The two
// t.Fatalf messages below carry a deadline and pyry's own stderr and nothing from
// the process table; the one t.Logf names a duration.
//
// The content pin is a full-table `ps -axww` BY CONSTRUCTION — that is how the
// `zsh -c` wrapper and its `cat` are found by argv content — and only its MATCHED
// rows are recorded. No tree snapshot is recorded at all: probeWaitForDirectChild's
// internal walk is the narrow descendant walk rooted at pyry's pid
// (background_trigger_probe_test.go:870) and only the int pid it returns is kept.

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// --- the handle -------------------------------------------------------------------

// finLiveRunHandle is one staged live turn: pyry's and claude's pids, the exit
// signal and the exit status it publishes, the two live output buffers, the run's
// FIFO path, env delta and claude version, the during-turn pin reading, and the
// staging tier's verdict.
//
// INPUT ONLY — NEVER PUBLISHED, and NO JSON TAGS. The rule is inherited rather
// than invented: Pin.Rows[i].Command and Pin.ClaudeCommand are verbatim argv read
// off the AMBIENT process table, so finLivePinReading
// (finding_live_pin_test.go:66-74) and finOutcomeStaging
// (finding_staging_gate_test.go:142) both carry the same prohibition for the same
// reason. Adding tags "for symmetry" is the first step toward a published record
// quoting captured bytes into a public issue.
//
// It is returned as a POINTER, and that is not a style preference: the pyry-exit
// kill cleanup is registered BEFORE pyry's pid exists (§ the cleanups, below), so
// its closure must read a pid written later. That is the shape the reach
// precedent uses with rec.PyryPID (background_reach_probe_test.go:364), and it is
// why the handle is allocated as the driver's first composite literal rather than
// composed at the end.
//
// WHAT IT DELIBERATELY DOES NOT CARRY — #1343 left the question open and this is
// the answer (finding_live_assembly_test.go:259-264): #1340 does not need more.
//
//   - No finOutcomeStaging record. Its own doc says "INPUT ONLY — NEVER
//     PUBLISHED"; the verdict crosses, the record does not.
//   - No workdir and no session id. Both are consumed INSIDE the driver, by the
//     assembly. finGatherInputs' six fields need neither.
//   - No staged command field. It is not lost, it is DERIVED:
//     finLiveStageCommand(h.FIFOPath) is a pure function of a field the handle
//     already carries, from the one source the driver itself used. A second copy
//     would be a second declaration of a captured string with no consumer.
//   - No tool_use id, no raw tool_result bytes, no raw pinScan. Model-controlled
//     bytes and the pre-reduction scan, which carries every ambient match rather
//     than only this run's.
//   - No artifact and no artifact dir. #1337 owns the record and the artifact.
//
// If #1337 needs one of these, that is #1337's argument to make — the same
// posture #1343 took toward this ticket.
type finLiveRunHandle struct {
	// PyryPID is the spawned `pyry agent-run` process; ClaudePID is its direct
	// child, as probeWaitForDirectChild resolved it. ClaudePID is the pid
	// finGatherInputs.ClaudeState is read over
	// (finding_run_gather_test.go:256-269).
	PyryPID   int
	ClaudePID int
	// PyryExited is closed by the cmd.Wait goroutine. RECEIVE-ONLY, so a consumer
	// cannot close a channel this file's goroutine owns; its zero is a nil channel,
	// which blocks forever rather than reading as "exited" — the safe direction.
	//
	// The driver's own body NEVER reads it and never releases the hold early. It
	// crosses unread so the consumer can take that wait itself, with the FIFO still
	// held, which is what lets #1337 observe the trailer at all.
	//
	// ITS CLOSE IS ALSO THE ONLY HAPPENS-BEFORE EDGE FOR ExitStatus, which the
	// goroutine writes strictly BEFORE closing this channel. A consumer reading that
	// field anywhere but inside a receive arm on this channel races that goroutine.
	PyryExited <-chan struct{}
	// ExitStatus is pyry's process exit code, and it is VALID ONLY AFTER A RECEIVE
	// FROM PyryExited. #1337 argued for it under the invitation above: the goroutine
	// discarded the status, cmd is function-local, and a consumer holding only
	// PyryPID cannot recover it — cmd.Wait has already reaped pyry, and a second wait
	// on a reaped child returns ECHILD.
	//
	// Initialised to pinExitStatusUnknown (process_pin_liveness_test.go:236) rather
	// than left at the int zero, because finRecordRun.ExitCode documents 0 as A REAL
	// SUCCESSFUL EXIT (finding_run_record_test.go:126-135): an unwritten field would
	// publish a clean exit for a run that never exited. Same zero-polarity doctrine
	// trailRunReadings.PyryExited and finOutcomeReadyToClassify each argue for
	// themselves. That makes the VALUE point the safe way; it does NOT make an
	// unsynchronised ACCESS safe, which is what the edge above is for.
	//
	// -1 is not uniquely "did not exit": ProcessState.ExitCode() also returns it for
	// a signalled process, which the defence-in-depth SIGKILL below would produce.
	// Both mean "not a clean self-exit"; separating them is the consumer's job, from
	// whether its own wait completed.
	ExitStatus int
	// Stdout and Stderr are the LIVE buffers, never a []byte snapshot. This is
	// load-bearing rather than convenient: pyry's reap log lands on stderr at
	// teardown and the trailer lands on stdout at emitter.Close(), BOTH after this
	// driver returns. A []byte here would be a snapshot taken strictly too early,
	// and #1337's finGatherInputs.Stderr would be filled from a buffer that had not
	// yet seen the bytes it exists to read. The consumer calls .Bytes() — which
	// returns a copy (background_trigger_probe_test.go:736-742) — at its own
	// reading point.
	Stdout *probeSyncBuffer
	Stderr *probeSyncBuffer
	// FIFOPath is workdir/finLiveStageFIFOName, the join #1342 named at
	// finding_live_staging_test.go:204-205. It is finGatherInputs.Needles' content
	// join, and the same string the pin was taken on.
	FIFOPath string
	// EnvDelta is exactly what was handed to spawnProbePyry, and ClaudeVersion is
	// probeClaudeVersion's best-effort read — a version that could not be read is
	// recorded as such rather than failing the run.
	EnvDelta      []string
	ClaudeVersion string
	// Pin crosses WHOLE, as finLivePinReading, and is never flattened into three
	// fields. Flattening would re-declare four values that already have one
	// declaration, drop RowCount, and invite a consumer to recompute it as
	// len(Rows) — a second producer for a number pinPartition and finLivePinReduce
	// each assign exactly once. Handing the reduction's own type across is what
	// discharges "neither is re-derived here" structurally.
	//
	// Pin.PGIDs is the []int finGatherInputs.Pinned takes, and never a []reachProc
	// (finding_run_gather_test.go:249-251). An EMPTY Pin.ClaudeCommand is
	// AMBIGUITY, not a staging failure: tdnClaudeCommand returns "" when zero OR
	// SEVERAL rows carry the claude needle (teardown_liveness_probe_test.go:571-573).
	// It is provenance, it is not one of finOutcomeStaging's eight fields, and it
	// must not be gated on here or downstream.
	Pin finLivePinReading
	// Staging is finLiveAssembleStaging's return AS RETURNED — not re-derived, not
	// renamed, not cross-checked into a new verdict, not inspected to choose a
	// different code path. The assembly already guarantees that property in its own
	// body (finding_live_assembly_test.go:228-231); this driver's obligation is to
	// add nothing to it.
	Staging finOutcomeResult
}

// --- the driver -------------------------------------------------------------------

// finLiveRunStage stages one live `pyry agent-run` turn on the ptyrunner default
// and returns its handle: a command held un-finishable, the auto-background
// trigger fired, the held processes pinned DURING the turn, and the staging
// tier's verdict.
//
// NO PARAMETERS BEYOND t: the driver establishes its own credentialed worktree,
// and there is no configuration a caller could vary that would still be this run.
// It MAY NOT be called from a parallel test — WithWorktreeAuthenticated calls
// t.Setenv (fixtures.go:96-107) and Go's runtime refuses that pairing. That
// helper also SKIPS rather than fails when neither credential variable is set,
// which is why it is the first statement: a machine without credentials skips
// before anything is created, and HOME is repointed before any path is derived
// from it.
//
// THE BODY IS STRAIGHT-LINE: no early return, no disposition of its own, no
// failure arm. Every staging failure it could branch on already has a named home
// in finOutcomeStagingGate, and the gate is RANK-ORDERED so the driver need not
// decide which one to report (finding_staging_gate_test.go:290-366: identity
// outranks trigger outranks rendezvous outranks pin-scan-errored outranks count).
// An early return on a rendezvous miss would save at most one deadline of wall
// clock on a run that has already lost a live turn, at the cost of a path that
// can only ever be exercised live. No Bash call, a different command, no trigger,
// no rendezvous, a failed ps, a surprising row count — each is a FACT to fill and
// hand over, never a failure: an instrument reading is a datum, not a reason to
// abort a turn.
//
// # The two t.Fatalf's, and they are the only ones
//
// Both are rig faults with NO NAMED HOME in the gate, and reporting them through
// the gate would file a false story:
//
//   - probeWaitForDirectChild returning 0 — pyry never spawned claude. The gate
//     has no arm for "the supervisor never came up", and every reading downstream
//     would be about a process that does not exist.
//   - probeWaitForSessionID returning "" — without a session id the assembly reads
//     a transcript path that cannot exist, comes back BashIssued: false, and the
//     gate reports stage-no-bash-call: "the model issued no Bash call" for a run in
//     which the model was never asked. A false attribution, which is exactly the
//     class this family guards against.
//
// ONE INHERITED ABORT PATH, named so a live caller knows about it:
// finLiveAssembleStaging inherits ReadJSONL's t.Fatalf on a transcript it cannot
// open or parse (fixtures.go:152, :163). A MISSING file is not fatal —
// probeWaitForBashToolUse guards with os.Stat first
// (background_trigger_probe_test.go:767), so it times out to "no Bash call
// issued", the safe direction.
//
// # Turn headroom, and no budget-fired run
//
// spawnProbePyry already passes --max-turns=probeMaxTurns ("6"), which leaves
// room for the turn to COMPLETE — claude receives the tool_result and replies —
// not merely to reach the Bash call. No new spawn helper is added: spawnProbePyry
// registers no t.Cleanup of its own, starts pyry and returns the *exec.Cmd, which
// is exactly this driver's requirement.
//
// NO BUDGET-FIRED RUN IS STAGED, for two independent production reasons. (1) THE
// EXIT CODE CANNOT SEPARATE THE OUTCOMES: the budget's Terminate hook cancels the
// run context (ptyrunner/runner.go:502), Run returns nil on a cancelled run
// context (:600-601) exactly as on normal completion (:606), and runAgentRun maps
// only a non-nil, non-context.Canceled error to a non-zero exit
// (cmd/pyry/agent_run.go:271-277) — the discriminator is the trailer, not the exit
// status. (2) THE BUDGET PATH CANNOT ANSWER THE DOWNSTREAM QUESTION AT ALL: its
// hook reaps INSIDE the hook, before the trailer is written (:492-503, the reap at
// :499). So: none is staged.
//
// PYRY_USE_STREAMJSON=0 is in #1342's env delta by name
// (finding_live_staging_test.go:174-179) and spawnProbePyry appends the delta to
// os.Environ(), so the delta wins over an ambient setting. No skip guard is needed
// here — the reach probe's (background_reach_probe_test.go:296) exists because its
// delta does not name the variable.
func finLiveRunStage(t *testing.T) *finLiveRunHandle {
	t.Helper()

	// Skips (without credentials) before anything is created, and repoints HOME
	// before any path below is derived from it.
	workdir := WithWorktreeAuthenticated(t)
	claudeBin := resolveClaudeBin(t)

	fifoPath := filepath.Join(workdir, finLiveStageFIFOName)
	var stdout, stderr probeSyncBuffer
	pyryExited := make(chan struct{})
	h := &finLiveRunHandle{
		PyryExited: pyryExited,
		// Points the unwritten value the safe way — 0 is a real successful exit
		// downstream. See the field's own doc.
		ExitStatus:    pinExitStatusUnknown,
		Stdout:        &stdout,
		Stderr:        &stderr,
		FIFOPath:      fifoPath,
		EnvDelta:      finLiveStageEnvDelta(),
		ClaudeVersion: probeClaudeVersion(claudeBin),
	}

	// Registered BEFORE holdProbeFIFO so LIFO releases the FIFO first and pyry
	// gets a real chance to finish the turn and exit on its own; this is the
	// defense-in-depth net for the case where it does not. ESRCH is benign.
	//
	// The order is called out rather than left implicit because INVERTING IT HAS
	// NO SYMPTOM: registering the kill after the hold, so LIFO kills before
	// releasing, yields a clean-looking run — green, handle populated — in which
	// the rig, not pyry, produced the exit, and #1337's exit reading silently
	// measures the rig. Both nearest precedents do exactly this and say so
	// (background_reach_probe_test.go:355-374, background_trigger_probe_test.go:438-453).
	t.Cleanup(func() {
		// LOAD-BEARING (background_trigger_probe_test.go:443). Without this
		// guard a failure before cmd.Start reaches syscall.Kill(-0, SIGKILL),
		// and kill(0, sig) is defined as "send to every process in the CALLER's
		// own process group" — the test binary would SIGKILL itself and its
		// siblings. One line, whole-run blast radius.
		if h.PyryPID <= 0 {
			return
		}
		// KEPT, and not in tension with "the body never reads pyryExited": that
		// rule is about the body. This grace is what gives pyry its real chance
		// to exit on its own after the FIFO release, which is what makes the
		// kill defense-in-depth rather than the cause of the exit.
		select {
		case <-pyryExited:
		case <-time.After(probePyryExitGrace):
			// The honest stand-in for the precedents' rec.note, since this file
			// has no record. It names a duration and nothing from the process
			// table.
			t.Logf("finLiveRunStage: pyry did not exit within %s of the FIFO release; "+
				"SIGKILLing its group", probePyryExitGrace)
		}
		_ = syscall.Kill(-h.PyryPID, syscall.SIGKILL)
	})

	rendezvous := holdProbeFIFO(t, fifoPath)

	promptPath := filepath.Join(workdir, "prompt.txt")
	if err := os.WriteFile(promptPath, []byte(finLiveStagePrompt(fifoPath)), 0o600); err != nil {
		t.Fatalf("write %s: %v", promptPath, err)
	}
	systemPath := filepath.Join(workdir, "system.txt")
	if err := os.WriteFile(systemPath, []byte(finLiveStageSystemPrompt), 0o600); err != nil {
		t.Fatalf("write %s: %v", systemPath, err)
	}

	bin := ensurePyryBuilt(t)
	cmd := spawnProbePyry(t, bin, workdir, promptPath, systemPath, h.EnvDelta, &stdout, &stderr)
	h.PyryPID = cmd.Process.Pid

	// pyry is a direct child of this process, so it becomes a zombie between
	// exit and Wait — and a zombie answers Signal(0) with nil. A liveness probe
	// would therefore report "still running" for an already-returned pyry,
	// falsifying the during-turn claim (#1223's lesson). Read the channel.
	//
	// The goroutine exits when cmd.Wait returns, which happens when pyry exits
	// and its stdout/stderr copiers finish. Both cleanups guarantee that
	// terminates. Claude runs on a PTY, so the held `cat` does not inherit
	// pyry's stdout/stderr pipe write ends and cannot hold Wait open.
	//
	// The exit status is written BEFORE the close, because that close is the only
	// happens-before edge a consumer has: a write after it is a race that reads
	// correct on every run nobody is examining. The Wait error itself stays
	// discarded — it carries no information the status does not — and the
	// ProcessState guard follows the repo's own shape (internal/e2e/attach_stdio.go:234-237),
	// leaving pinExitStatusUnknown in place when there is no state to read.
	go func() {
		_ = cmd.Wait()
		if cmd.ProcessState != nil {
			h.ExitStatus = cmd.ProcessState.ExitCode()
		}
		close(pyryExited)
	}()

	h.ClaudePID = probeWaitForDirectChild(h.PyryPID, probeClaudeChildDeadline)
	if h.ClaudePID == 0 {
		t.Fatalf("pyry never spawned a claude child within %s\nstderr:\n%s",
			probeClaudeChildDeadline, truncate(stderr.Bytes()))
	}

	sessionID := probeWaitForSessionID(&stdout, probeSessionIDDeadline)
	if sessionID == "" {
		t.Fatalf("no system/init session_id on pyry stdout within %s\nstderr:\n%s",
			probeSessionIDDeadline, truncate(stderr.Bytes()))
	}

	// A FACT, never a failure: finOutcomeStagingGate has a named, rank-ordered
	// arm for an incomplete rendezvous, so this is filled and handed over.
	rendezvousDone := false
	select {
	case <-rendezvous:
		rendezvousDone = true
	case <-time.After(probeRendezvousDeadline):
	}

	// THE DRIVER TAKES THE tool_result WAIT ITSELF, BEFORE PINNING, and skipping
	// it is the failure this step exists to prevent. finLiveAssembleStaging also
	// waits for the tool_use and tool_result internally (via finTranscriptFill,
	// finding_staging_fill_test.go:252-263), but it takes the pin counts as
	// INPUTS, so its wait happens strictly after the pin. A driver reasoning "the
	// assembly does the waiting" pins before the `cat` exists, matches 0 or 1
	// rows, and fires the gate's count arm reporting stage-pin-count-unexpected on
	// a correctly staged run — with no other symptom, and one live claude turn
	// spent finding out.
	//
	// These waits DECIDE NOTHING; they are TIMING and never SELECTION.
	// finTranscriptFill selects its Bash call content-first against the staged
	// command (finTranscriptSelectBash, :255), so both raw envelopes are
	// discarded here: they are model-controlled bytes with no downstream use, and
	// every question they could answer is one the assembly answers content-first
	// from the same transcript.
	//
	// The empty-id guard is not decoration: probeWaitForToolResult matches on
	// ToolUseID EQUALITY (background_trigger_probe_test.go:806), so an empty id
	// would poll a full probeToolResultDeadline for a match it cannot make.
	//
	// NAMED LIMITATION, ACCEPTED. probeWaitForBashToolUse returns the FIRST Bash
	// tool_use regardless of input.command — a deliberately-shipped #1223 gap. If
	// the model issues some other Bash call first, this timing keys off that
	// envelope and the pin may land early. Both precedents guard content-first
	// because both DECIDE A DISPOSITION from it; this driver decides nothing, so
	// it adds no guard. The untimed case is bounded and safe: the assembly still
	// selects content-first, so the run reports either stage-command-not-staged or
	// stage-pin-count-unexpected — both non-verdict outcomes, never a false
	// ready-to-classify. Growing a second content-first waiter is the shared-rig
	// edit both precedents explicitly declined to make, and it is not made here.
	toolUseID, _ := probeWaitForBashToolUse(t, workdir, sessionID, probeToolUseDeadline)
	if toolUseID != "" {
		_ = probeWaitForToolResult(t, workdir, sessionID, toolUseID, probeToolResultDeadline)
	}

	// --- the pin: DURING the turn, never after the trailer is observed ---
	//
	// The tool_result has fired and the FIFO write end is STILL held —
	// holdProbeFIFO releases only in the t.Cleanup it registers itself, which by
	// construction runs after this body. So `cat` is blocked in read() until EOF
	// and the match is guaranteed.
	//
	// A scan taken AFTER the observed trailer matches nothing on a healthy run: a
	// rig sees the trailer only when it polls pyry's stdout, up to one
	// probePollInterval (200ms) after emitter.Close() writes it, and the
	// descendant reap at ptyrunner/runner.go:398 starts effectively immediately
	// after that write and finishes in the time of one ps exec (reap.go:75-76).
	//
	// ONE scan carrying BOTH needles — the shipped precedent at
	// teardown_liveness_probe_test.go:377. It saves a second ps:
	// reachMatchArgvRows matches a row carrying ANY needle
	// (background_reach_probe_test.go:911-916), so one scan yields both
	// populations and finLivePinReduce separates them.
	//
	// NEITHER EXCLUSION SHOULD EVER FIRE. pinPartition records one only when it
	// actually matched (process_pin_liveness_test.go:146-148), so an entry
	// appearing in a record is itself the signal that a needle leaked into a
	// process it should not have reached.
	needles := []string{fifoPath, tdnClaudeNeedle}
	exclude := map[int]string{
		os.Getpid(): "the rig's own test binary",
		h.PyryPID:   "the `pyry agent-run` process the rig spawned",
	}
	scan, scanErr := pinScanArgv(needles, exclude)

	// Handed to the reduction UNCHANGED: nothing here filters, counts or dedupes
	// it. On error the value is the zero pinScan (process_pin_liveness_test.go:194),
	// which reduces to the zero reading, and the error itself has its own named
	// arm ranked above the count arm — so it is carried as PinScanErrored below
	// rather than becoming a second error channel here.
	h.Pin = finLivePinReduce(scan, fifoPath)

	// The want is finLivePinWantRows and NEVER scan.MatchCount (3 on a healthy
	// run: claude's own row rides along on the second needle) and NEVER
	// len(h.Pin.PGIDs) (1 on a healthy run: claude isolates the whole Bash command
	// into one detached group). Both wrong fills are argued at
	// finding_live_pin_test.go:125-139 and both fire the gate's count arm against
	// a want of 2 on a correctly staged run.
	//
	// The same live deadlines are passed through to the assembly. ITS SECOND READ
	// OF THE TRANSCRIPT IS EXPECTED AND IS NOT A DEFECT — the file is already on
	// disk by then, so both of its waiters return on their first poll, which is
	// the case its deadline parameters exist for
	// (finding_live_assembly_test.go:240-244).
	h.Staging = finLiveAssembleStaging(t, workdir, sessionID, finLiveAssembleFacts{
		StagedCommand:  finLiveStageCommand(fifoPath),
		RendezvousDone: rendezvousDone,
		PinScanErrored: scanErr != nil,
		PinMatchCount:  h.Pin.RowCount,
		PinWantCount:   finLivePinWantRows,
	}, probeToolUseDeadline, probeToolResultDeadline)

	return h
}

//go:build e2e_realclaude

package realclaude

// Evidence probe for #1230 — is a Bash command claude has moved to the
// background inside `agentrun.ReapDescendantGroups`'s descendant-BFS reach,
// and would its process group survive the reaper's three exclusions?
//
// This file is NOT a regression gate. It records one observation; #1231 draws
// the confirmatory half. It is opt-in (PYRY_PROBE_BACKGROUND_REACH=1) because
// `make e2e-realclaude` runs the whole package glob and an ungated probe would
// burn a live claude turn on every `make preship`. A SKIP here is the normal
// outcome and carries no signal about pyry's behaviour.
//
// # What is being probed
//
// The reaper's entire reach is a BFS from claude's pid over a single
// `ps -axo pid=,ppid=,pgid=` snapshot (internal/agentrun/reap.go:75). If a
// backgrounded command is not a transitive child of claude's pid — a double
// fork orphans it to pid 1 — the walk never reaches it, the group is never
// killed, and the process outlives pyry. Nobody has checked whether the
// commands claude backgrounds on timeout expiry are inside that reach.
//
// THE PREDICTIVE HALF ONLY: would the walk find it, and would its pgid survive
// `reap.go:52`'s three exclusions. One during-turn snapshot, no teardown.
// Whether the group actually dies, and by whose hand, is #1231's to answer and
// must not be claimed here.
//
// The answer is runner-independent by construction: ptyrunner, streamrunner and
// streamsup all route their teardown reap through the identical
// agentrun.ReapDescendantGroups behind a reapDescendantGroupsFn seam.
//
// # Reuse, not rebuild
//
// The staging half — spawn pyry, hold a FIFO's write end so claude's `cat`
// cannot finish on its own, poll the session JSONL — is #1223's, on main as
// background_trigger_probe_test.go, and is called here verbatim. That file is
// deliberately NOT edited: #1231 wants the same helpers, and two children
// editing the shared rig is a merge conflict for zero benefit. Every symbol
// introduced here is `reach`-prefixed for the same reason.
//
// The trigger is settled and is not re-derived: BASH_DEFAULT_TIMEOUT_MS set low
// on the `pyry agent-run` process, 7/7 identical fires on claude 2.1.220
// (#1223). One row, one rep, no lever matrix, no negative control.
//
// # What #1223 structurally cannot answer, and this file adds
//
// #1223's probeAnnotateCommands reduces each process to `filepath.Base(argv[0])`
// — dropping the argv tail that holds the FIFO path — and only annotates
// processes it already found by walking DOWN from pyry. Two independent reasons
// it can answer "is there a process named `cat` under pyry" and cannot answer
// "where is the process holding THIS FIFO". The second is the only question that
// distinguishes a re-parented survivor (which reads merely `absent` from a
// subtree-first search) from a command claude killed. So identification here is
// content-first across the WHOLE process table, by full command line.
//
// # Redaction — the one design decision worth auditing
//
// That full-table argv read is every process's command line on the operator's
// machine, and these artifacts get pasted into a public issue. The defence is
// structural, not a reviewer's judgment call:
//
//  1. argv only, NEVER envp. This file must never invoke `ps -E`, `ps -e` with
//     an environment column, or the BSD-syntax `ps eww`. Those print argv PLUS
//     the full environment — and WithWorktreeAuthenticated requires
//     CLAUDE_CODE_OAUTH_TOKEN or ANTHROPIC_API_KEY in the outer environment,
//     which pyry passes to claude verbatim. One flag is the difference between
//     this probe and dumping the operator's live credential into a public
//     comment. #1223 needed an environment read and paid for it with the
//     probeLeverVars three-name allowlist; this ticket needs none, so the safe
//     design is to not have the capability.
//  2. reachScanArgv execs ps, hands the bytes straight to reachMatchArgvRows,
//     and returns only matched rows. The raw table is confined to that one stack
//     frame: never a record field, never a file, never a log line.
//  3. The needles are two run-scoped, run-unique strings this test generated —
//     the FIFO path and the session UUID. An unrelated process's argv cannot
//     enter the record by accident.
//  4. Needles are matched in Go, in-process — never passed to ps, grep, or a
//     shell. There is no user-controlled value in any exec argument.
//  5. Each retained command is capped at reachMaxCommandBytes with an explicit
//     marker, and the total rows scanned is recorded alongside the match count
//     so an implausible match count is visible rather than silent.
//  6. The ONLY ps output persisted verbatim is the integer-column snapshot
//     (pid/ppid/pgid) — three integers per line, no command column.
//  7. Artifacts are mode 0600 in a MkdirTemp directory outside the repo, and
//     unlike #1223 no pyry stdout/stderr file is written at all.
//
// Residual, stated rather than hidden: the retained rows' argv ARE published.
// Those are `cat <fifo>` / `zsh -c cat <fifo>` and pyry's own
// `claude --session-id … --settings …`, all constructed by pyry from this
// probe's own flags. They disclose temp paths and the run's session UUID —
// both fields #1223 already published. Skim the record before pasting.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// reachEnableEnv gates the live probe.
const reachEnableEnv = "PYRY_PROBE_BACKGROUND_REACH"

const (
	// reachFIFOName is distinct from #1223's probeFIFOName so a concurrent
	// trigger probe's `cat` can never satisfy this run's content match.
	reachFIFOName = "reach-hold"
	// reachPSTimeout bounds each ps exec, mirroring probeProcessSnapshot.
	reachPSTimeout = 5 * time.Second
	// reachMaxCommandBytes caps every retained command line. A local process
	// can read the process table, so it could spawn a process carrying this
	// run's needle in its argv to place attacker-chosen text into an artifact
	// the operator pastes into a public issue. That needs local code execution
	// plus a race inside the held window — past this probe's threat model — but
	// the cap costs three lines and bounds the blast radius to a skim.
	reachMaxCommandBytes  = 512
	reachTruncationMarker = "...(truncated by #1230 probe)"
)

// reachEnvDelta is the settled #1223 trigger, set on the `pyry agent-run`
// process. Both runners hand claude pyry's own environment verbatim, so this is
// the whole plumbing story — no production change, no forwarding list.
var reachEnvDelta = []string{"BASH_DEFAULT_TIMEOUT_MS=5000"}

// AC1's three-valued match outcome, never collapsed. The fourth value is not a
// collapse of the three: it is the instrument reporting that it did not run.
const (
	// reachMatched: the background handle came back AND at least one argv row
	// carries the FIFO needle. Only this value licenses a reachability verdict.
	reachMatched = "matched"
	// reachTriggerNeverFired: no background handle came back this run. The
	// probe is inconclusive and NO reachability claim is made. #1223's 7-of-7
	// firing rate is evidence, not a guarantee for this run.
	reachTriggerNeverFired = "trigger-never-fired"
	// reachFiredNoRowMatched: the handle came back and zero argv rows matched.
	// A finding in its own right — backgrounded and gone from the table.
	reachFiredNoRowMatched = "fired-no-row-matched"
	// reachMatchUndetermined: the argv scan itself failed, so the match
	// question was never asked.
	reachMatchUndetermined = "undetermined-argv-scan-failed"
)

// AC5's dispositions.
const (
	reachNotReachable      = "not reachable from claude's pid"
	reachReachableTargeted = "reachable and survives all three exclusions"
	reachReachableExcluded = "reachable but excluded by the reaper's own arithmetic"
	reachLeftTable         = "backgrounded process left the table"
	reachInconclusive      = "inconclusive, re-run"
)

// reachProc is one process-table row. Command and Needles are populated ONLY
// for content-matched rows; rows parsed out of the integer snapshot carry the
// three integers and nothing else.
type reachProc struct {
	PID     int      `json:"pid"`
	PPID    int      `json:"ppid"`
	PGID    int      `json:"pgid"`
	Command string   `json:"command,omitempty"`
	Needles []string `json:"matched_needles,omitempty"`
}

// reachHeld is one matched row's reachability reading, both ways.
//
// Row's three INTEGERS are the integer snapshot's — the same frame as every
// other operand in the exclusion arithmetic, and the frame AC4 publishes
// verbatim — while its Command and Needles are the argv scan's. matched_rows
// keeps the argv scan's own integers unaltered, and any divergence between the
// two frames is recorded in notes.
type reachHeld struct {
	Row       reachProc   `json:"row"`
	Hops      []reachProc `json:"hops_up_to_claude"`
	ReachedUp bool        `json:"reached_claude_walking_up"`
	InDownBFS bool        `json:"in_down_bfs_from_claude"`
	HopsInBFS bool        `json:"every_hop_in_down_bfs"`
	Agree     bool        `json:"up_and_down_agree"`
}

// reachExclusion is reap.go:52's arithmetic for one pgid, carrying every
// integer each comparison is made against so a reader can redo it by eye.
type reachExclusion struct {
	HeldPGID       int      `json:"held_pgid"`
	ReaperSelfPGID int      `json:"reaper_self_pgid"`
	ClaudePID      int      `json:"claude_pid"`
	ClaudePGID     int      `json:"claude_pgid"`
	Survives       bool     `json:"survives_all_three_exclusions"`
	ExcludedBy     []string `json:"excluded_by,omitempty"`
}

// reachRecord is the whole deliverable. It carries no raw process table, no
// environment read, no model-authored JSON and no pyry stream capture.
type reachRecord struct {
	Ticket        string   `json:"ticket"`
	ClaudeBin     string   `json:"claude_bin"`
	ClaudeVersion string   `json:"claude_version"`
	Model         string   `json:"model"`
	EnvDelta      []string `json:"env_delta"`
	RunnerPathEnv string   `json:"runner_path_from_env"`
	// RunnerPathArgv evidences the runner from the process table rather than
	// from the env this test set — #1223 learned the hard way that a routing
	// assumption can change underneath a probe.
	RunnerPathArgv string `json:"runner_path_from_argv"`
	Workdir        string `json:"workdir"`
	FIFOPath       string `json:"fifo_path"`
	Prompt         string `json:"prompt"`
	SessionID      string `json:"session_id"`
	SessionJSONL   string `json:"session_jsonl_path"`

	PyryPID           int  `json:"pyry_pid"`
	PyryPGID          int  `json:"pyry_pgid"`
	PyryIsGroupLeader bool `json:"pyry_is_own_group_leader"`

	ClaudePIDPositional int  `json:"claude_pid_positional"`
	ClaudePIDContent    int  `json:"claude_pid_content"`
	ClaudePIDAgree      bool `json:"claude_pid_identifications_agree"`
	// ClaudeRow carries no omitempty: encoding/json does not honour it for
	// struct types, so an all-zero row would be emitted anyway. It is emitted
	// unconditionally and reads as zeroes when the root was never pinned.
	ClaudeRow           reachProc `json:"claude_row"`
	ClaudePGID          int       `json:"claude_pgid"`
	ClaudeIsGroupLeader bool      `json:"claude_pgid_equals_pid"`

	BackgroundHandlePresent   bool   `json:"background_handle_present"`
	BackgroundTaskID          string `json:"background_task_id,omitempty"`
	TimedOutAfterMs           string `json:"timed_out_after_ms,omitempty"`
	ToolUseCommandMatchedFIFO bool   `json:"tool_use_command_matched_fifo"`

	MatchOutcome    string      `json:"match_outcome"`
	MatchedRows     []reachProc `json:"matched_rows,omitempty"`
	ArgvRowsScanned int         `json:"argv_rows_scanned"`
	ArgvScanError   string      `json:"argv_scan_error,omitempty"`
	SkewMissingPIDs []int       `json:"skew_pids_absent_from_integer_table,omitempty"`

	Held         []reachHeld      `json:"held_processes,omitempty"`
	DownBFSCount int              `json:"down_bfs_descendant_count"`
	UpDownAgree  bool             `json:"up_and_down_reads_agree"`
	Exclusions   []reachExclusion `json:"exclusion_arithmetic,omitempty"`

	SnapshotDuringTurn bool     `json:"snapshot_taken_during_turn"`
	PSError            string   `json:"ps_error,omitempty"`
	Disposition        string   `json:"disposition"`
	DispositionDetail  string   `json:"disposition_detail"`
	Notes              []string `json:"notes,omitempty"`

	// rawPS is the verbatim integer-column snapshot. Unexported, so
	// encoding/json skips it; it is written as a sibling file instead.
	rawPS []byte
}

func (r *reachRecord) note(format string, args ...any) {
	r.Notes = append(r.Notes, fmt.Sprintf(format, args...))
}

func (r *reachRecord) decide(verdict, format string, args ...any) {
	r.Disposition = verdict
	r.DispositionDetail = fmt.Sprintf(format, args...)
}

// TestRealClaude_BackgroundReachability stages one live turn, waits for claude
// to background the held command, and records whether the reaper's BFS could
// reach it. It asserts almost nothing: t.Fatalf fires only on structural
// failure (pyry won't build, mkfifo fails, pyry never spawns a child, no
// system/init session id). Every other failure mode is RECORDED, because a
// probe that turns an unexpected reading into a red test loses the reading.
func TestRealClaude_BackgroundReachability(t *testing.T) {
	if os.Getenv(reachEnableEnv) != "1" {
		t.Skipf("#1230 background-reachability probe: skipped because %s != 1.\n"+
			"This is an EVIDENCE PROBE, not a regression gate — a skip here is the "+
			"normal `make e2e-realclaude` outcome and says nothing about pyry's "+
			"behaviour. It costs one live claude turn. The probe sets "+
			"BASH_DEFAULT_TIMEOUT_MS=5000 on the pyry process itself (#1223's "+
			"settled trigger), so no extra environment is needed:\n"+
			"  %s=1 go test -tags e2e_realclaude -timeout 10m -v \\\n"+
			"    -run 'TestRealClaude_BackgroundReachability' ./internal/e2e/realclaude/",
			reachEnableEnv, reachEnableEnv)
	}
	// Content-first root pinning keys on `--session-id <uuid>` in claude's
	// argv, which only the ptyrunner path emits (ptyrunner/runner.go:618);
	// under PYRY_USE_STREAMJSON=1 claude mints its own id and the needle would
	// never match, so the run would spend a live turn to reach a guaranteed
	// root-disagreement. The streamrunner path is out of scope for this ticket
	// (its claude also inherits pyry's group, which changes which exclusion
	// fires — worth a follow-up if the ptyrunner reading is surprising).
	//
	// This is NOT a hypothetical guard: PYRY_USE_STREAMJSON=1 was live in the
	// dispatcher environment when this file was written. #1223 hit the same
	// drift from the other side — its fallback row was gated on an assumption
	// about `agent-run`'s default that had silently stopped holding.
	if os.Getenv("PYRY_USE_STREAMJSON") == "1" {
		t.Skipf("#1230 probe: PYRY_USE_STREAMJSON=1 is set, which selects the "+
			"headless stream-json runner. Its claude argv carries no --session-id, "+
			"so this probe cannot pin claude's pid content-first and the run would "+
			"burn a live turn to reach a guaranteed root-disagreement. The ticket "+
			"is scoped to the `pyry agent-run` default (ptyrunner) path — clear the "+
			"variable for the run:\n"+
			"  env -u PYRY_USE_STREAMJSON %s=1 go test -tags e2e_realclaude \\\n"+
			"    -timeout 10m -v -run 'TestRealClaude_BackgroundReachability' \\\n"+
			"    ./internal/e2e/realclaude/", reachEnableEnv)
	}

	// Deliberately NOT t.TempDir(): that is removed when the test ends, and the
	// developer needs these files afterwards to compose the issue comment.
	artifactDir, err := os.MkdirTemp("", "pyry-1230-probe-*")
	if err != nil {
		t.Fatalf("create artifact dir: %v", err)
	}
	t.Logf("#1230 probe artifacts: %s", artifactDir)
	runReachProbe(t, artifactDir)
}

// runReachProbe stages the rendezvous, takes the during-turn measurement, and
// writes the record.
func runReachProbe(t *testing.T, artifactDir string) {
	// Skips (without credentials) before anything is created.
	workdir := WithWorktreeAuthenticated(t)
	claudeBin := resolveClaudeBin(t)

	fifoPath := filepath.Join(workdir, reachFIFOName)
	rec := &reachRecord{
		Ticket:        "1230",
		ClaudeBin:     claudeBin,
		ClaudeVersion: probeClaudeVersion(claudeBin),
		Model:         probeModel,
		EnvDelta:      reachEnvDelta,
		RunnerPathEnv: reachRunnerPathFromEnv(reachEnvDelta),
		Workdir:       workdir,
		FIFOPath:      fifoPath,
		Prompt:        probePrompt(fifoPath),
		MatchOutcome:  reachTriggerNeverFired,
		Disposition:   reachInconclusive,
		DispositionDetail: "the probe did not reach its classification point; no " +
			"reachability claim is made",
	}
	// Stated in the record, not just in a comment, so a reader of the pasted
	// artifact is not misled into counting two agreeing reads. The
	// PYRY_USE_STREAMJSON gate above returns before this record exists, so
	// runner_path_from_env can only ever read "ptyrunner" in any record that
	// gets written. Only runner_path_from_argv carries evidential signal.
	rec.note("runner_path_from_env is documentation, NOT independent corroboration " +
		"of runner_path_from_argv: the PYRY_USE_STREAMJSON gate returns before this " +
		"record exists, so it can only read ptyrunner. The argv read is the one that " +
		"evidences the runner from the process table")

	// Registered FIRST so LIFO runs it LAST: the record is complete by then,
	// and a structural t.Fatalf below still leaves partial evidence on disk.
	t.Cleanup(func() { writeReachArtifacts(t, artifactDir, rec) })

	// Registered BEFORE holdProbeFIFO so LIFO releases the FIFO first and pyry
	// gets a real chance to finish the turn and exit on its own.
	pyryExited := make(chan struct{})
	t.Cleanup(func() {
		// LOAD-BEARING (background_trigger_probe_test.go:443). Without this
		// guard a failure before cmd.Start reaches syscall.Kill(-0, SIGKILL),
		// and kill(0, sig) is defined as "send to every process in the CALLER's
		// own process group" — the test binary would SIGKILL itself and its
		// siblings. One line, whole-run blast radius.
		if rec.PyryPID <= 0 {
			return
		}
		select {
		case <-pyryExited:
		case <-time.After(probePyryExitGrace):
			rec.note("pyry did not exit within %s of the FIFO release; SIGKILLed its group",
				probePyryExitGrace)
		}
		_ = syscall.Kill(-rec.PyryPID, syscall.SIGKILL)
	})

	rendezvous := holdProbeFIFO(t, fifoPath)

	promptPath := filepath.Join(workdir, "prompt.txt")
	if err := os.WriteFile(promptPath, []byte(rec.Prompt), 0o600); err != nil {
		t.Fatalf("write %s: %v", promptPath, err)
	}
	systemPath := filepath.Join(workdir, "system.txt")
	if err := os.WriteFile(systemPath, []byte(probeSystemPrompt), 0o600); err != nil {
		t.Fatalf("write %s: %v", systemPath, err)
	}

	bin := ensurePyryBuilt(t)
	var stdout, stderr probeSyncBuffer
	cmd := spawnProbePyry(t, bin, workdir, promptPath, systemPath, reachEnvDelta, &stdout, &stderr)
	rec.PyryPID = cmd.Process.Pid

	// pyry is a direct child of this process, so it becomes a zombie between
	// exit and Wait — and a zombie answers Signal(0) with nil. A liveness probe
	// would therefore report "still running" for an already-returned pyry,
	// falsifying the during-turn claim (#1223's lesson). Read the channel.
	go func() {
		_ = cmd.Wait()
		close(pyryExited)
	}()

	rec.ClaudePIDPositional = probeWaitForDirectChild(rec.PyryPID, probeClaudeChildDeadline)
	if rec.ClaudePIDPositional == 0 {
		t.Fatalf("pyry never spawned a claude child within %s\nstderr:\n%s",
			probeClaudeChildDeadline, truncate(stderr.Bytes()))
	}

	sessionID := probeWaitForSessionID(&stdout, probeSessionIDDeadline)
	if sessionID == "" {
		t.Fatalf("no system/init session_id on pyry stdout within %s\nstderr:\n%s",
			probeSessionIDDeadline, truncate(stderr.Bytes()))
	}
	rec.SessionID = sessionID
	rec.SessionJSONL = jsonlPathFor(workdir, sessionID)

	select {
	case <-rendezvous:
	case <-time.After(probeRendezvousDeadline):
		rec.decide(reachInconclusive, "no reader opened %s within %s — the model "+
			"never issued the Bash call, so nothing was backgrounded to look for",
			fifoPath, probeRendezvousDeadline)
		reachFinish(t, rec)
		return
	}

	toolUseID, toolUseRaw := probeWaitForBashToolUse(t, workdir, sessionID, probeToolUseDeadline)
	if toolUseID == "" {
		rec.decide(reachInconclusive, "the rendezvous fired (the command ran) but no "+
			"Bash tool_use reached %s within %s", rec.SessionJSONL, probeToolUseDeadline)
		reachFinish(t, rec)
		return
	}

	// probeWaitForBashToolUse returns the FIRST Bash tool_use regardless of
	// input.command — a deliberately-shipped #1223 gap. If the model issued any
	// other Bash call first, keying off that envelope would time the whole
	// measurement against the wrong tool call. Guard content-first here rather
	// than editing the shared rig, and keep only the boolean: the model's
	// verbatim params are model-controlled bytes this ticket has no use for.
	rawInput, _, _ := probeToolUseInput(toolUseRaw, toolUseID)
	command := reachToolUseCommand(rawInput)
	rec.ToolUseCommandMatchedFIFO = strings.Contains(command, fifoPath)
	if !rec.ToolUseCommandMatchedFIFO {
		rec.decide(reachInconclusive, "the tracked Bash tool_use (%s) does not run the "+
			"run's FIFO, so it is not the call this probe measures — inconclusive, "+
			"re-run", toolUseID)
		reachFinish(t, rec)
		return
	}

	resultRaw := probeWaitForToolResult(t, workdir, sessionID, toolUseID, probeToolResultDeadline)
	reachMeasure(rec, resultRaw, pyryExited)
	reachFinish(t, rec)
}

// reachMeasure is the classification point: the matching tool_result has
// landed, the FIFO write end is STILL held (holdProbeFIFO's t.Cleanup runs
// after this returns), and pyry is still running. The four steps are ordered
// and the order is load-bearing.
func reachMeasure(rec *reachRecord, resultRaw []byte, pyryExited <-chan struct{}) {
	// (1) The integer snapshot. Every number in AC2 and AC3 comes out of these
	// bytes, and it is the one readout pasted into the comment. Only snap.raw
	// is used: snap.descendants is #1223's subtree-first, base-name-annotated
	// view, which is precisely the read this ticket exists to replace.
	//
	// Discarding it is not free: on success probeProcessSnapshot also runs a
	// second, narrow `ps -o pid=,command= -p <pids>` internally to annotate
	// those descendants (background_trigger_probe_test.go:930), so one extra
	// exec lands between the two load-bearing snapshots. "Immediately
	// following" in step (2) therefore means the next statement, not the next
	// syscall. Harmless — the held process cannot exit while the FIFO write end
	// is held, and step (4) nets any skew — but a reader should not have to
	// open the shared rig to discover it.
	snap := probeProcessSnapshot(rec.PyryPID)
	rec.rawPS = snap.raw
	rec.PSError = snap.err

	// (2) The argv snapshot, immediately following. Matched rows only.
	matches, total, scanErr := reachScanArgv([]string{rec.FIFOPath, rec.SessionID})
	rec.MatchedRows = matches
	rec.ArgvRowsScanned = total
	if scanErr != nil {
		rec.ArgvScanError = scanErr.Error()
	}

	// (3) During-turn evidence, from the cmd.Wait channel.
	select {
	case <-pyryExited:
		rec.SnapshotDuringTurn = false
	default:
		rec.SnapshotDuringTurn = true
	}

	// (4) Skew cross-check. Two snapshots means a check-then-use window; what
	// bounds it is that the held process cannot exit during it (the FIFO write
	// end is held) and that only pids matched by a run-unique needle are ever
	// used, so a pid recycled between the two ps calls cannot be mistaken for a
	// matched one. That is the argument; this is the net under it.
	index := reachIndexFromPS(snap.raw)
	for _, m := range matches {
		if _, ok := index[m.PID]; !ok {
			rec.SkewMissingPIDs = append(rec.SkewMissingPIDs, m.PID)
		}
	}

	var heldRows, rootRows []reachProc
	for _, m := range matches {
		if reachMatchedNeedle(m, rec.FIFOPath) {
			heldRows = append(heldRows, m)
		}
		if reachMatchedNeedle(m, rec.SessionID) {
			rootRows = append(rootRows, m)
		}
	}

	// AC1's three-valued outcome. ESTABLISH THE HANDLE FIRST, then look at the
	// table: a zero-row match must never be recorded as "not reachable from
	// claude's pid" — that reports the alarming branch from an instrument that
	// did not run.
	rec.BackgroundTaskID, rec.TimedOutAfterMs, rec.BackgroundHandlePresent =
		reachBackgroundHandle(resultRaw)
	switch {
	case resultRaw == nil:
		rec.MatchOutcome = reachTriggerNeverFired
		rec.decide(reachInconclusive, "no tool_result matched the tracked tool_use "+
			"within %s — the call was still open and the command still blocked, so "+
			"nothing was backgrounded this run", probeToolResultDeadline)
		return
	case !rec.BackgroundHandlePresent:
		rec.MatchOutcome = reachTriggerNeverFired
		rec.decide(reachInconclusive, "the tool_result carries no "+
			"toolUseResult.backgroundTaskId, so no background handle came back this "+
			"run (#1223's 7-of-7 firing rate is evidence, not a guarantee) — "+
			"inconclusive, re-run")
		return
	case rec.ArgvScanError != "":
		rec.MatchOutcome = reachMatchUndetermined
		rec.decide(reachInconclusive, "instrument fault: the argv scan failed (%s), so "+
			"the match question was never asked", rec.ArgvScanError)
		return
	case len(heldRows) == 0:
		rec.MatchOutcome = reachFiredNoRowMatched
		rec.decide(reachLeftTable, "the background handle came back (%s) but no row in "+
			"the %d-row process table carries the run's FIFO path — the command was "+
			"backgrounded and has left the table. Surprising given the write end is "+
			"still held; reported verbatim rather than interpreted",
			rec.BackgroundTaskID, rec.ArgvRowsScanned)
		return
	default:
		rec.MatchOutcome = reachMatched
	}

	// The integer snapshot's own error is a GATE, not a footnote — everything
	// from here down reads off snap.raw. probeProcessSnapshot returns
	// exec.Cmd.Output()'s partial stdout ALONGSIDE the error
	// (background_trigger_probe_test.go:871-873), so on a 5 s context timeout —
	// a loaded machine is this probe's expected condition, not the exotic one —
	// snap.raw is a CUT process table. A cut that drops an intermediate hop
	// while keeping the held row makes the up-walk and the down-BFS agree on
	// "not reachable" and sails past the skew check, publishing
	// reachNotReachable: a public claim that pyry leaks processes, filed off an
	// instrument that half-ran. That is exactly what AC1 and AC5 forbid.
	//
	// The gate sits AFTER the match-outcome switch rather than inside it. The
	// match question is answered by the argv ps alone, so a failed integer
	// snapshot must neither relabel a true `matched` nor suppress a true
	// `fired-no-row-matched` finding — both stand on an instrument that did
	// run. What a failed integer snapshot disqualifies is the reachability
	// half, which is all of what follows.
	if rec.PSError != "" {
		rec.decide(reachInconclusive, "instrument fault: the integer ps snapshot failed "+
			"(%s), so every hop, membership and exclusion integer below would be read "+
			"off a partial process table. NO reachability verdict is stated — a cut "+
			"table can make both reads agree on 'not reachable' and both be wrong",
			rec.PSError)
		return
	}
	if !rec.SnapshotDuringTurn {
		rec.decide(reachInconclusive, "pyry had already returned when the snapshot was "+
			"taken, so this is not a during-turn reading")
		return
	}
	if len(rec.SkewMissingPIDs) > 0 {
		rec.decide(reachInconclusive, "instrument fault (two-snapshot skew): pid(s) %v "+
			"matched in the argv table but are absent from the integer table — NOT a "+
			"reachability finding", rec.SkewMissingPIDs)
		return
	}
	pyryRow, ok := index[rec.PyryPID]
	if !ok {
		rec.decide(reachInconclusive, "instrument fault: pyry's own pid %d is absent "+
			"from the integer snapshot, so the reaper's `self` pgid cannot be read and "+
			"the exclusion arithmetic would compare against the wrong integer",
			rec.PyryPID)
		return
	}
	rec.PyryPGID = pyryRow.PGID
	rec.PyryIsGroupLeader = pyryRow.PGID == pyryRow.PID
	// spawnProbePyry sets Setpgid (background_trigger_probe_test.go:640), so
	// under this probe pyry is its own group leader. An operator-launched pyry
	// inherits its shell's job-control group instead. The verdict is unaffected
	// only if the held pgid differs from BOTH candidate values, which the
	// recorded integers show explicitly.
	rec.note("pyry pgid %d (own group leader: %t) — this probe spawns pyry with "+
		"Setpgid, so its group is its own pid; an operator-launched pyry would carry "+
		"its shell's job-control group instead", rec.PyryPGID, rec.PyryIsGroupLeader)

	claudePID, ok := reachPinRoot(rec, rootRows, index)
	if !ok {
		return
	}

	// AC2: two independent reads, same bytes, and they must agree.
	down := probeDescendantsFromPS(snap.raw, claudePID)
	rec.DownBFSCount = len(down)
	inSubtree := make(map[int]bool, len(down))
	for _, p := range down {
		inSubtree[p.PID] = true
	}

	rec.UpDownAgree = true
	allReachable := true
	for _, row := range heldRows {
		// AC3's PRIMARY operand must come from the same frame as every other
		// integer it is compared against. ReaperSelfPGID is read from the
		// integer snapshot, ClaudePGID likewise (reachPinRoot), and the integer
		// snapshot is the readout AC4 pastes into the comment — so a held pgid
		// taken from the argv ps would leave a reader redoing the arithmetic by
		// eye checking against a number the record did not use. Reading one
		// pid's pgid out of two snapshots can disagree, so the published row
		// carries the INTEGER snapshot's three integers and the argv scan's
		// Command/Needles, and any divergence is noted rather than silently
		// resolved. Presence in `index` is guaranteed here: the skew gate above
		// returned if any matched pid was absent from the integer table.
		intRow := index[row.PID]
		if intRow.PPID != row.PPID || intRow.PGID != row.PGID {
			rec.note("held pid %d differs between the two snapshots (integer ppid=%d "+
				"pgid=%d, argv ppid=%d pgid=%d); the integer snapshot is authoritative",
				row.PID, intRow.PPID, intRow.PGID, row.PPID, row.PGID)
		}
		row.PPID, row.PGID = intRow.PPID, intRow.PGID

		hops, reached := reachChainUp(index, row.PID, claudePID)
		held := reachHeld{
			Row:       row,
			Hops:      hops,
			ReachedUp: reached,
			InDownBFS: inSubtree[row.PID],
			HopsInBFS: true,
		}
		for _, hop := range hops {
			// claude roots the walk, so it is not in its own descendant set.
			if hop.PID != claudePID && !inSubtree[hop.PID] {
				held.HopsInBFS = false
			}
		}
		held.Agree = held.ReachedUp == held.InDownBFS && (!reached || held.HopsInBFS)
		if !held.Agree {
			rec.UpDownAgree = false
		}
		if !reached {
			allReachable = false
		}
		rec.Held = append(rec.Held, held)
	}
	if !rec.UpDownAgree {
		rec.decide(reachInconclusive, "instrument fault: the up-walk and the down-BFS "+
			"disagree over the same snapshot bytes. That is a bug in this probe, not a "+
			"finding about pyry — fix it and re-run")
		return
	}

	// AC3: the exclusion arithmetic, with the concrete integers.
	allSurvive := true
	seenPGID := make(map[int]bool)
	for _, held := range rec.Held {
		if seenPGID[held.Row.PGID] {
			continue
		}
		seenPGID[held.Row.PGID] = true
		survives, reasons := reachExclusionVerdict(held.Row.PGID, rec.PyryPGID, claudePID)
		rec.Exclusions = append(rec.Exclusions, reachExclusion{
			HeldPGID:       held.Row.PGID,
			ReaperSelfPGID: rec.PyryPGID,
			ClaudePID:      claudePID,
			ClaudePGID:     rec.ClaudePGID,
			Survives:       survives,
			ExcludedBy:     reasons,
		})
		if !survives {
			allSurvive = false
		}
	}
	if !rec.ClaudeIsGroupLeader {
		rec.note("FINDING: claude's pgid (%d) is NOT its pid (%d), so reap.go:52's "+
			"`pgid == rootPid` does not exclude what reap.go:45-47 claims it does — "+
			"claude's own group is not the group that comparison names",
			rec.ClaudePGID, claudePID)
	}

	switch {
	case !allReachable:
		rec.decide(reachNotReachable, "at least one process holding the run's FIFO is "+
			"NOT a transitive child of claude's pid %d, so the reaper's BFS cannot "+
			"find it and cannot kill it. File a follow-up naming the held pid(s) and "+
			"pgid(s) below and the reachability gap", claudePID)
	case allSurvive:
		rec.decide(reachReachableTargeted, "every process holding the run's FIFO is a "+
			"transitive child of claude's pid %d and its pgid survives all three of "+
			"reap.go:52's exclusions. THIS ESTABLISHES ONLY THAT THE REAPER WOULD "+
			"TARGET THE GROUP. Whether it actually dies, and by whose hand, is "+
			"#1231's to answer and is not claimed here", claudePID)
	default:
		rec.decide(reachReachableExcluded, "the held process is a transitive child of "+
			"claude's pid %d, but at least one pgid is excluded by reap.go:52's own "+
			"arithmetic, so the reaper would NOT kill it. See exclusion_arithmetic "+
			"for which comparison fires and against which integer", claudePID)
	}
}

// reachPinRoot pins claude's pid content-first (AC2). Both reachability reads
// are rooted at it, so a mis-identified root makes them AGREE AND BOTH BE
// WRONG — agreement does not validate the root. #1223's probeWaitForDirectChild
// (:975) returns pyry's first direct child by position with no content check,
// whereas the reaper's actual argument is the pid of the claude command pyry
// spawned. So the content evidence is the argv row carrying this run's session
// UUID (pyry passes `--session-id <uuid>`, ptyrunner/runner.go:618): unique on
// the machine, and it survives shebang rewriting, which matching on the
// resolved claude binary path does not — the CLI may execute as `node …/cli.js`.
//
// If the content evidence is unavailable or contradicts the positional pick,
// the record says so and STOPS rather than doing arithmetic that would be
// relative to the wrong process.
func reachPinRoot(rec *reachRecord, rootRows []reachProc, index map[int]reachProc) (int, bool) {
	switch len(rootRows) {
	case 0:
		rec.decide(reachInconclusive, "no argv row carries this run's session id, so "+
			"claude's pid cannot be pinned content-first. The positional pick (%d) is "+
			"recorded but no arithmetic is done relative to it — both reachability "+
			"reads are rooted at this pid and would agree with each other while both "+
			"being wrong", rec.ClaudePIDPositional)
		return 0, false
	case 1:
		rec.ClaudeRow = rootRows[0]
	default:
		var direct []reachProc
		for _, r := range rootRows {
			if r.PPID == rec.PyryPID {
				direct = append(direct, r)
			}
		}
		if len(direct) != 1 {
			rec.decide(reachInconclusive, "%d argv rows carry this run's session id and "+
				"%d of them are direct children of pyry — claude's pid is ambiguous, so "+
				"no arithmetic is done", len(rootRows), len(direct))
			return 0, false
		}
		rec.note("%d argv rows carried the session id; picked the single direct child "+
			"of pyry (pid %d)", len(rootRows), direct[0].PID)
		rec.ClaudeRow = direct[0]
	}

	rec.ClaudePIDContent = rec.ClaudeRow.PID
	rec.RunnerPathArgv = reachRunnerPathFromArgv(rec.ClaudeRow.Command)
	rec.ClaudePIDAgree = rec.ClaudePIDContent == rec.ClaudePIDPositional
	if !rec.ClaudePIDAgree {
		rec.decide(reachInconclusive, "the content-pinned claude pid (%d, the argv row "+
			"carrying this run's session id) disagrees with the positional pick (%d, "+
			"pyry's first direct child). No arithmetic is done relative to a root two "+
			"independent identifications do not agree on",
			rec.ClaudePIDContent, rec.ClaudePIDPositional)
		return 0, false
	}

	// Read claude's pgid from the INTEGER snapshot: that is the load-bearing
	// readout, and it is the one pasted into the comment.
	claudeRow, ok := index[rec.ClaudePIDContent]
	if !ok {
		rec.decide(reachInconclusive, "instrument fault: claude's pid %d is absent from "+
			"the integer snapshot although it matched in the argv table",
			rec.ClaudePIDContent)
		return 0, false
	}
	rec.ClaudePGID = claudeRow.PGID
	rec.ClaudeIsGroupLeader = claudeRow.PGID == claudeRow.PID
	if claudeRow.PGID != rec.ClaudeRow.PGID {
		rec.note("claude's pgid differs between the two snapshots (integer %d, argv "+
			"%d); the integer snapshot is authoritative", claudeRow.PGID, rec.ClaudeRow.PGID)
	}
	return rec.ClaudePIDContent, true
}

// reachFinish logs the summary. The per-phase t.Logf output is the SKIP≠PASS
// proof: a transcript must distinguish a real run from a skipped one at a
// glance, which a `go test` exit code cannot.
func reachFinish(t *testing.T, rec *reachRecord) {
	t.Helper()
	t.Logf("#1230 match_outcome=%s disposition=%s\n  %s",
		rec.MatchOutcome, rec.Disposition, rec.DispositionDetail)
	t.Logf("#1230 runner: env=%s argv=%s | claude %s | handle=%t task=%s timed_out_after_ms=%s",
		rec.RunnerPathEnv, rec.RunnerPathArgv, rec.ClaudeVersion,
		rec.BackgroundHandlePresent, rec.BackgroundTaskID, rec.TimedOutAfterMs)
	t.Logf("#1230 roots: pyry pid=%d pgid=%d | claude positional=%d content=%d agree=%t "+
		"pgid=%d pgid==pid=%t | argv rows matched=%d of %d scanned",
		rec.PyryPID, rec.PyryPGID, rec.ClaudePIDPositional, rec.ClaudePIDContent,
		rec.ClaudePIDAgree, rec.ClaudePGID, rec.ClaudeIsGroupLeader,
		len(rec.MatchedRows), rec.ArgvRowsScanned)
	for _, held := range rec.Held {
		t.Logf("#1230 held pid=%d pgid=%d up_reached=%t in_down_bfs=%t agree=%t\n  hops: %s",
			held.Row.PID, held.Row.PGID, held.ReachedUp, held.InDownBFS, held.Agree,
			reachFormatHops(held.Hops))
	}
	for _, ex := range rec.Exclusions {
		t.Logf("#1230 exclusions for pgid=%d: reaper_self_pgid=%d claude_pid=%d "+
			"claude_pgid=%d survives=%t excluded_by=%v",
			ex.HeldPGID, ex.ReaperSelfPGID, ex.ClaudePID, ex.ClaudePGID,
			ex.Survives, ex.ExcludedBy)
	}
}

// writeReachArtifacts persists the record and the verbatim integer snapshot to
// a directory that survives the test. The integer snapshot is the ONLY ps
// output written verbatim — three integers per line, no command column.
func writeReachArtifacts(t *testing.T, dir string, rec *reachRecord) {
	t.Helper()
	blob, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Errorf("marshal reach record: %v", err)
		return
	}
	// AC4's verbatim snapshot is a deliverable, so its ABSENCE has to be
	// explained in place. Writing nothing when the ps returned nothing leaves an
	// empty artifact directory that reads as "the probe never got that far",
	// which is a different claim from "the process table read failed".
	psContent := rec.rawPS
	if len(psContent) == 0 {
		psContent = []byte(fmt.Sprintf(
			"# no rows: the integer `ps -axo pid=,ppid=,pgid=` snapshot returned no "+
				"bytes.\n# ps_error: %s\n# disposition: %s\n",
			reachOrNone(rec.PSError), reachOrNone(rec.Disposition)))
	}
	files := map[string][]byte{
		filepath.Join(dir, "reach.json"):   append(blob, '\n'),
		filepath.Join(dir, "reach.ps.txt"): psContent,
	}
	for path, content := range files {
		if len(content) == 0 {
			continue
		}
		if err := os.WriteFile(path, content, 0o600); err != nil {
			// The evidence IS this ticket's deliverable, so a lost artifact is
			// loud — but it does not abort the remaining cleanups.
			t.Errorf("write reach artifact %s: %v", path, err)
		}
	}
}

// --- process-table helpers --------------------------------------------------

// reachScanArgv execs one full-table argv read, hands the bytes straight to
// reachMatchArgvRows, and returns ONLY matched rows plus the total scanned.
// The raw table never leaves this frame: it is never assigned to a record
// field, never written to a file, never logged.
//
// -ww is mandatory: without it macOS truncates the command column to the
// terminal width and the argv tail holding the FIFO path is silently lost —
// reproducing the exact defect this ticket exists to avoid, with no symptom.
//
// NEVER add -E, -e with an environment column, or the BSD-syntax `eww`. Those
// print each process's full ENVIRONMENT, which here means the operator's
// CLAUDE_CODE_OAUTH_TOKEN / ANTHROPIC_API_KEY, into an artifact destined for a
// public issue. This probe needs no environment read at all, so it does not
// have the capability (see the file header, redaction rule 1).
func reachScanArgv(needles []string) ([]reachProc, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), reachPSTimeout)
	defer cancel()
	table, err := exec.CommandContext(ctx, "ps", "-axww", "-o", "pid=,ppid=,pgid=,command=").Output()
	if err != nil {
		return nil, 0, fmt.Errorf("ps -axww -o pid=,ppid=,pgid=,command=: %w", err)
	}
	matches, total := reachMatchArgvRows(table, needles)
	return matches, total, nil
}

// reachMatchArgvRows returns the rows whose FULL command line contains any
// needle, plus the total number of well-formed rows scanned. Pure over its
// bytes so the credential-free self-check can drive it synthetically.
//
// Matching is against the whole command line, never a base name: #1223's
// probeAnnotateCommands reduces each row to filepath.Base(argv[0]), which drops
// the argv tail holding the FIFO path — the one thing that distinguishes a
// re-parented survivor from a command claude killed.
//
// Each retained command is capped AFTER matching, so a needle that fell inside
// the cap still matches a line longer than the cap.
func reachMatchArgvRows(table []byte, needles []string) ([]reachProc, int) {
	var matches []reachProc
	total := 0
	for _, line := range strings.Split(string(table), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		pid, perr := strconv.Atoi(fields[0])
		ppid, pperr := strconv.Atoi(fields[1])
		pgid, pgerr := strconv.Atoi(fields[2])
		if perr != nil || pperr != nil || pgerr != nil {
			continue
		}
		total++
		command := reachCommandColumn(line)
		var hit []string
		for _, needle := range needles {
			if needle != "" && strings.Contains(command, needle) {
				hit = append(hit, needle)
			}
		}
		if len(hit) == 0 {
			continue
		}
		matches = append(matches, reachProc{
			PID:     pid,
			PPID:    ppid,
			PGID:    pgid,
			Command: reachCapCommand(command),
			Needles: hit,
		})
	}
	return matches, total
}

// reachCommandColumn returns everything after the three integer columns,
// unsplit — a command line contains spaces and must not be tokenised.
func reachCommandColumn(line string) string {
	rest := strings.TrimLeft(line, " \t")
	for i := 0; i < 3; i++ {
		j := strings.IndexAny(rest, " \t")
		if j < 0 {
			return ""
		}
		rest = strings.TrimLeft(rest[j:], " \t")
	}
	return strings.TrimRight(rest, " \t")
}

func reachCapCommand(command string) string {
	if len(command) <= reachMaxCommandBytes {
		return command
	}
	return command[:reachMaxCommandBytes] + reachTruncationMarker
}

// reachMatchedNeedle reports whether a matched row hit a specific needle. It
// reads the recorded needle list rather than re-scanning Command, which the
// byte cap may have truncated.
func reachMatchedNeedle(p reachProc, needle string) bool {
	for _, n := range p.Needles {
		if n == needle {
			return true
		}
	}
	return false
}

// reachIndexFromPS parses a `ps -axo pid=,ppid=,pgid=` snapshot into a pid→row
// index, using the same Fields / len==3 / Atoi discipline as production's
// descendantPGIDs (internal/agentrun/reap.go:83-96) so this evidence is
// directly comparable with what the reap walk sees.
func reachIndexFromPS(snapshot []byte) map[int]reachProc {
	index := make(map[int]reachProc)
	for _, line := range strings.Split(string(snapshot), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			continue
		}
		pid, perr := strconv.Atoi(fields[0])
		ppid, pperr := strconv.Atoi(fields[1])
		pgid, pgerr := strconv.Atoi(fields[2])
		if perr != nil || pperr != nil || pgerr != nil {
			continue
		}
		index[pid] = reachProc{PID: pid, PPID: ppid, PGID: pgid}
	}
	return index
}

// reachChainUp walks ppid links from `from` toward `root`, returning every hop
// (from → … → root) and whether root was reached. It is AC2's UP read; the DOWN
// read is #1223's already-self-checked probeDescendantsFromPS, which is itself a
// byte-pure mirror of production's descendantPGIDs.
//
// Terminates on reaching root (reachable), on pid <= 1 (not reachable — the
// double-fork orphan this ticket exists to detect), on a pid absent from the
// index (not reachable), or on revisiting a pid (cycle guard, mirroring
// production's `seen`).
func reachChainUp(index map[int]reachProc, from, root int) ([]reachProc, bool) {
	var hops []reachProc
	seen := make(map[int]bool)
	for pid := from; pid > 1; {
		p, ok := index[pid]
		if !ok || seen[pid] {
			return hops, false
		}
		seen[pid] = true
		hops = append(hops, p)
		if pid == root {
			return hops, true
		}
		pid = p.PPID
	}
	return hops, false
}

// reachExclusionVerdict evaluates internal/agentrun/reap.go:52's three
// exclusions. reasons names each one that fires, carrying the integers the
// comparison was made against.
//
// pyryPGID must be the pgid of the process that CALLS ReapDescendantGroups —
// the `pyry agent-run` process, not the test binary that spawned it.
// syscall.Getpgrp() is read there (reap.go:49); reading the test's own group
// would silently compare against the wrong integer.
func reachExclusionVerdict(heldPGID, pyryPGID, claudePID int) (bool, []string) {
	var reasons []string
	if heldPGID <= 1 {
		reasons = append(reasons, fmt.Sprintf("pgid <= 1 (pgid=%d)", heldPGID))
	}
	if heldPGID == pyryPGID {
		reasons = append(reasons,
			fmt.Sprintf("pgid == self, the reaper's own group (pgid=%d, self=%d)", heldPGID, pyryPGID))
	}
	if heldPGID == claudePID {
		reasons = append(reasons,
			fmt.Sprintf("pgid == rootPid, i.e. claude's PID (pgid=%d, rootPid=%d)", heldPGID, claudePID))
	}
	return len(reasons) == 0, reasons
}

// --- JSONL projections ------------------------------------------------------

// reachBackgroundHandle projects the tool_result envelope's toolUseResult:
// backgroundTaskId (presence is "the handle came back") plus timedOutAfterMs as
// the expiry-path corroboration — 5000 on the expiry path, absent when the
// model asked for backgrounding (#1223 AC4).
//
// It deliberately does NOT key off input.timeout: #1223 observed that field
// absent 10-of-10, because the client default backgrounds with nothing in the
// request params.
//
// toolUseResult is a sibling of `message` on the user line and is not always an
// object, so it is decoded in two stages — a type mismatch must read as "no
// handle", never derail the whole projection.
func reachBackgroundHandle(raw []byte) (string, string, bool) {
	var outer struct {
		ToolUseResult json.RawMessage `json:"toolUseResult"`
	}
	if err := json.Unmarshal(raw, &outer); err != nil || len(outer.ToolUseResult) == 0 {
		return "", "", false
	}
	var result struct {
		BackgroundTaskID string       `json:"backgroundTaskId"`
		TimedOutAfterMs  *json.Number `json:"timedOutAfterMs"`
	}
	if err := json.Unmarshal(outer.ToolUseResult, &result); err != nil {
		return "", "", false
	}
	timedOut := ""
	if result.TimedOutAfterMs != nil {
		timedOut = result.TimedOutAfterMs.String()
	}
	return result.BackgroundTaskID, timedOut, result.BackgroundTaskID != ""
}

// reachToolUseCommand projects input.command out of the model's verbatim Bash
// params. Only the derived boolean reaches the record — the params themselves
// are model-controlled bytes this ticket has no use for, and #1223 already
// published them.
func reachToolUseCommand(input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var params struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return ""
	}
	return params.Command
}

// --- small formatters -------------------------------------------------------

// reachRunnerPathFromEnv names the runner from the EFFECTIVE environment, not
// just the delta this probe sets: PYRY_USE_STREAMJSON may already be exported
// in the operator's shell (it was, on 2026-07-25, and that silently invalidated
// a #1223 gate). Only the exact string "1" is truthy, matching
// cmd/pyry/agent_run.go:266.
//
// Its streamrunner branch is unreachable in any run that produces a record: the
// PYRY_USE_STREAMJSON gate in TestRealClaude_BackgroundReachability returns
// first. Kept because it encodes the truthiness rule and the effective-env read
// for the next reader, but it is documentation, not evidence — see the standing
// note the record carries. reachRunnerPathFromArgv is the corroborating read.
func reachRunnerPathFromEnv(delta []string) string {
	streamJSON := os.Getenv("PYRY_USE_STREAMJSON") == "1"
	for _, kv := range delta {
		if strings.HasPrefix(kv, "PYRY_USE_STREAMJSON=") {
			streamJSON = kv == "PYRY_USE_STREAMJSON=1"
		}
	}
	if streamJSON {
		return "streamrunner (headless stream-json)"
	}
	return "ptyrunner (interactive TUI, the agent-run default)"
}

// reachRunnerPathFromArgv corroborates the runner path from the process table
// rather than from the env this test set. --append-system-prompt-file is the
// ptyrunner-shape marker from ptyrunner's buildArgs (runner.go:616-625).
func reachRunnerPathFromArgv(command string) string {
	if strings.Contains(command, "--append-system-prompt-file") {
		return "ptyrunner (claude argv carries --append-system-prompt-file)"
	}
	return "indeterminate (claude argv carries no --append-system-prompt-file)"
}

// reachSameRow compares two rows by their four recorded columns. reachProc
// carries a []string, so it is not comparable with == and the self-checks
// cannot use struct equality.
func reachSameRow(a, b reachProc) bool {
	return a.PID == b.PID && a.PPID == b.PPID && a.PGID == b.PGID && a.Command == b.Command
}

// reachOrNone keeps an empty field from reading as a missing one in a
// plain-text artifact, where "" and "absent" look identical.
func reachOrNone(s string) string {
	if s == "" {
		return "<none recorded>"
	}
	return s
}

func reachFormatHops(hops []reachProc) string {
	if len(hops) == 0 {
		return "<none>"
	}
	parts := make([]string, 0, len(hops))
	for _, h := range hops {
		parts = append(parts, fmt.Sprintf("pid=%d ppid=%d pgid=%d", h.PID, h.PPID, h.PGID))
	}
	return strings.Join(parts, " -> ")
}

// --- credential-free self-checks -------------------------------------------
//
// These run without the opt-in gate and without credentials. A bug in either
// parser would turn a clean observation into a false one:
//
//	go test -tags e2e_realclaude -v -run 'TestReach' ./internal/e2e/realclaude/

// reachArgvFixture is a synthetic four-column `ps -axww -o
// pid=,ppid=,pgid=,command=` table shaped like the real one: pyry → claude →
// zsh → cat, plus an unrelated process whose BASE NAME collides with the FIFO's
// and three malformed lines.
const reachArgvFixture = `
    1     0     1 /sbin/launchd
  100     1   100 /usr/local/bin/pyry agent-run --prompt-file=/tmp/run-a/prompt.txt
  200   100   200 /opt/node/bin/node /opt/claude/cli.js --session-id 11111111-2222-3333-4444-555555555555 --settings /tmp/s.json --append-system-prompt-file /tmp/run-a/system.txt
  300   200   300 /bin/zsh -c cat /tmp/run-a/reach-hold
  400   300   300 cat /tmp/run-a/reach-hold
  500     1   500 /usr/bin/reach-hold --an-unrelated-daemon
garbage
  700   700
    x     1     1 /bin/false
`

func TestReachMatchArgvRows(t *testing.T) {
	const (
		fifo    = "/tmp/run-a/reach-hold"
		session = "11111111-2222-3333-4444-555555555555"
	)
	// Six well-formed rows: 1, 100, 200, 300, 400, 500. `garbage` has one
	// field, `700 700` has two, and the `x` row's pid is not an integer.
	const wantTotal = 6

	tests := []struct {
		name    string
		needles []string
		wantPID []int
	}{
		{
			// The defect this ticket exists to avoid: probeAnnotateCommands
			// reduces 300/400 to base names `zsh`/`cat` and would find neither,
			// while finding 500 — whose base name collides but whose argv does
			// NOT carry the path.
			name:    "needle in the argv tail matches; base-name collision does not",
			needles: []string{fifo},
			wantPID: []int{300, 400},
		},
		{
			name:    "session id pins exactly one row",
			needles: []string{session},
			wantPID: []int{200},
		},
		{
			name:    "both needles, union in table order",
			needles: []string{fifo, session},
			wantPID: []int{200, 300, 400},
		},
		{
			name:    "no match returns an empty slice and a non-zero total",
			needles: []string{"/tmp/run-b/nothing-here"},
			wantPID: nil,
		},
		{
			name:    "empty needles never match",
			needles: []string{""},
			wantPID: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, total := reachMatchArgvRows([]byte(reachArgvFixture), tc.needles)
			if total != wantTotal {
				t.Fatalf("rows scanned: got %d, want %d (malformed lines must be "+
					"skipped, not fatal)", total, wantTotal)
			}
			if len(got) != len(tc.wantPID) {
				t.Fatalf("matches: got %+v, want pids %v", got, tc.wantPID)
			}
			for i, want := range tc.wantPID {
				if got[i].PID != want {
					t.Fatalf("match[%d]: got pid %d, want %d", i, got[i].PID, want)
				}
			}
		})
	}

	t.Run("command column keeps its spaces", func(t *testing.T) {
		got, _ := reachMatchArgvRows([]byte(reachArgvFixture), []string{"--prompt-file="})
		if len(got) != 1 {
			t.Fatalf("matches: got %+v, want exactly pyry's row", got)
		}
		const want = "/usr/local/bin/pyry agent-run --prompt-file=/tmp/run-a/prompt.txt"
		if got[0].Command != want {
			t.Fatalf("command column: got %q, want %q (the column is the rest of the "+
				"line after three integers, not a tokenised field)", got[0].Command, want)
		}
	})

	t.Run("ppid and pgid are carried through", func(t *testing.T) {
		got, _ := reachMatchArgvRows([]byte(reachArgvFixture), []string{fifo})
		want := []reachProc{
			{PID: 300, PPID: 200, PGID: 300},
			{PID: 400, PPID: 300, PGID: 300},
		}
		for i := range want {
			if got[i].PID != want[i].PID || got[i].PPID != want[i].PPID || got[i].PGID != want[i].PGID {
				t.Fatalf("row[%d]: got pid=%d ppid=%d pgid=%d, want pid=%d ppid=%d pgid=%d",
					i, got[i].PID, got[i].PPID, got[i].PGID,
					want[i].PID, want[i].PPID, want[i].PGID)
			}
			if !reachMatchedNeedle(got[i], fifo) {
				t.Fatalf("row[%d]: needle %q not recorded in %v", i, fifo, got[i].Needles)
			}
		}
	})

	t.Run("over-long command is capped but still matched", func(t *testing.T) {
		needle := "NEEDLE-NEAR-THE-FRONT"
		line := fmt.Sprintf("  900   100   900 /bin/x %s %s\n",
			needle, strings.Repeat("y", reachMaxCommandBytes*2))
		got, total := reachMatchArgvRows([]byte(line), []string{needle})
		if total != 1 || len(got) != 1 {
			t.Fatalf("matches: got %+v (total %d), want exactly one", got, total)
		}
		if !strings.HasSuffix(got[0].Command, reachTruncationMarker) {
			t.Fatalf("command %q does not end with the truncation marker %q",
				got[0].Command, reachTruncationMarker)
		}
		if len(got[0].Command) != reachMaxCommandBytes+len(reachTruncationMarker) {
			t.Fatalf("capped command length: got %d, want %d",
				len(got[0].Command), reachMaxCommandBytes+len(reachTruncationMarker))
		}
	})

	t.Run("a needle past the cap still matches the row", func(t *testing.T) {
		needle := "NEEDLE-PAST-THE-CAP"
		line := fmt.Sprintf("  901   100   901 /bin/x %s %s\n",
			strings.Repeat("y", reachMaxCommandBytes*2), needle)
		got, _ := reachMatchArgvRows([]byte(line), []string{needle})
		if len(got) != 1 {
			t.Fatalf("matches: got %+v, want one — matching is against the FULL "+
				"command line, the cap applies only to what is retained", got)
		}
	})
}

func TestReachChainUp(t *testing.T) {
	// pyry(100) → claude(200) → zsh(300) → cat(400). 600 is the leak case: a
	// re-parented orphan whose ppid is 1. 500 is an unrelated sibling.
	const snapshot = `
    1     0     1
  100     1   100
  200   100   200
  300   200   300
  400   300   300
  600     1   600
  500     1   500
`
	index := reachIndexFromPS([]byte(snapshot))

	tests := []struct {
		name        string
		from, root  int
		wantHops    []reachProc
		wantReached bool
	}{
		{
			name: "three hops from the held command up to claude",
			from: 400, root: 200,
			wantHops: []reachProc{
				{PID: 400, PPID: 300, PGID: 300},
				{PID: 300, PPID: 200, PGID: 300},
				{PID: 200, PPID: 100, PGID: 200},
			},
			wantReached: true,
		},
		{
			// THE LEAK CASE this whole ticket exists to detect: a double fork
			// orphans the backgrounded command to pid 1, the BFS never reaches
			// it, and the reaper cannot kill it.
			name: "re-parented to pid 1 is not reachable",
			from: 600, root: 200,
			wantHops:    []reachProc{{PID: 600, PPID: 1, PGID: 600}},
			wantReached: false,
		},
		{
			name: "a pid absent from the index is not reachable and does not panic",
			from: 999, root: 200,
			wantHops:    nil,
			wantReached: false,
		},
		{
			name: "an unrelated sibling is not reachable",
			from: 500, root: 200,
			wantHops:    []reachProc{{PID: 500, PPID: 1, PGID: 500}},
			wantReached: false,
		},
		{
			name: "from == root is reached in one hop",
			from: 200, root: 200,
			wantHops:    []reachProc{{PID: 200, PPID: 100, PGID: 200}},
			wantReached: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hops, reached := reachChainUp(index, tc.from, tc.root)
			if reached != tc.wantReached {
				t.Fatalf("reached: got %t, want %t (hops %+v)", reached, tc.wantReached, hops)
			}
			if len(hops) != len(tc.wantHops) {
				t.Fatalf("hops: got %+v, want %+v", hops, tc.wantHops)
			}
			for i := range hops {
				if !reachSameRow(hops[i], tc.wantHops[i]) {
					t.Fatalf("hop[%d]: got %+v, want %+v", i, hops[i], tc.wantHops[i])
				}
			}
		})
	}

	t.Run("a ppid cycle terminates", func(t *testing.T) {
		cyclic := reachIndexFromPS([]byte("  800   801   800\n  801   800   801\n"))
		if _, reached := reachChainUp(cyclic, 800, 200); reached {
			t.Fatal("a cycle must report not-reachable, not loop forever")
		}
	})

	t.Run("malformed lines are skipped, mirroring descendantPGIDs", func(t *testing.T) {
		got := reachIndexFromPS([]byte("PID PPID PGID\n  100     1   100\ngarbage\n" +
			"  200   100   x\n  201   100   201\n  202\n"))
		want := map[int]reachProc{
			100: {PID: 100, PPID: 1, PGID: 100},
			201: {PID: 201, PPID: 100, PGID: 201},
		}
		if len(got) != len(want) {
			t.Fatalf("index: got %+v, want %+v", got, want)
		}
		for pid, w := range want {
			if !reachSameRow(got[pid], w) {
				t.Fatalf("index[%d]: got %+v, want %+v", pid, got[pid], w)
			}
		}
	})
}

// TestReachBackgroundHandle pins the projection that decides AC1's
// matched-vs-trigger-never-fired split. A silently-absent read would collapse
// every run to "inconclusive" and waste the live turn, so the envelope shape —
// toolUseResult as a SIBLING of message, and not always an object — is checked
// deterministically rather than discovered live.
func TestReachBackgroundHandle(t *testing.T) {
	tests := []struct {
		name         string
		line         string
		wantID       string
		wantTimedOut string
		wantPresent  bool
	}{
		{
			name: "expiry path carries both the handle and timedOutAfterMs",
			line: `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_a"}]},` +
				`"toolUseResult":{"backgroundTaskId":"bg_1","timedOutAfterMs":5000,"interrupted":false}}`,
			wantID:       "bg_1",
			wantTimedOut: "5000",
			wantPresent:  true,
		},
		{
			name: "model-asked path carries the handle without timedOutAfterMs",
			line: `{"type":"user","message":{"content":[]},` +
				`"toolUseResult":{"backgroundTaskId":"bg_2","interrupted":false}}`,
			wantID:      "bg_2",
			wantPresent: true,
		},
		{
			name: "no handle is the trigger-never-fired branch",
			line: `{"type":"user","message":{"content":[]},` +
				`"toolUseResult":{"stdout":"","stderr":"","interrupted":false}}`,
			wantPresent: false,
		},
		{
			name:        "toolUseResult absent",
			line:        `{"type":"user","message":{"content":[]}}`,
			wantPresent: false,
		},
		{
			name:        "toolUseResult as a string does not derail the projection",
			line:        `{"type":"user","toolUseResult":"Exit code 143"}`,
			wantPresent: false,
		},
		{
			name:        "malformed line yields no handle",
			line:        `{"type":"user","toolUseResult":`,
			wantPresent: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id, timedOut, present := reachBackgroundHandle([]byte(tc.line))
			if present != tc.wantPresent {
				t.Fatalf("present: got %t, want %t (id=%q)", present, tc.wantPresent, id)
			}
			if id != tc.wantID {
				t.Fatalf("backgroundTaskId: got %q, want %q", id, tc.wantID)
			}
			if timedOut != tc.wantTimedOut {
				t.Fatalf("timedOutAfterMs: got %q, want %q", timedOut, tc.wantTimedOut)
			}
		})
	}
}

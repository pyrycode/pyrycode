//go:build e2e_realclaude

package realclaude

// Evidence probe for #1223 — a deterministic trigger for claude returning a
// background handle under `pyry agent-run`.
//
// This file is NOT a regression gate. It collects readouts; #1224–#1227 draw
// the conclusions from them. It is opt-in (PYRY_PROBE_BACKGROUND_TRIGGER=1)
// because `make e2e-realclaude` runs the whole package glob (Makefile) and an
// ungated probe would burn ~9 live claude turns on every `make preship`. A SKIP
// here is the normal outcome and carries no signal about pyry's behaviour.
//
// # What is being probed
//
// claude's Bash tool moves a command to the background instead of stopping it
// when the command reaches its timeout without finishing. The backgrounding is
// documented client behaviour; what the model chooses is the timeout VALUE.
// Two observations against claude 2.1.220 on consecutive days: `tail -f
// /dev/null` got a model-chosen `"timeout":5000` and was backgrounded; `cat
// <fifo>` got no `timeout` field at all and ran unbounded. So the lever to find
// is not "make claude background a command" — it is "make a timeout expire
// regardless of what the model asks for". Only environment levers qualify;
// anything the model fills in is prompt-steering in disguise.
//
// # Mechanism: the test owns the in-flight window
//
//	test:   mkfifo <workdir>/probe-hold
//	test:   goroutine → open(fifo, O_WRONLY)   [blocks: no reader yet]
//	claude: Bash → cat <workdir>/probe-hold    [blocks: no writer yet]
//	        ↓ both opens complete at the same instant — a rendezvous
//	test:   holds the write end, never writes  → cat blocks in read() forever
//	test:   Close() (t.Cleanup ONLY)           → cat sees EOF and exits
//
// Three properties fall out, one per acceptance criterion:
//
//  1. The command cannot complete on its own. So a tool_result matching the
//     Bash tool_use, observed while the test still holds the write end, can
//     only mean claude ended the call itself. That is a STRUCTURAL
//     discriminator — it does not match claude's result prose, which is the
//     treadmill #563 and #1219 each paid for once.
//  2. The command stays alive as long as the test wants. So the `ps` snapshot
//     has no timing race: it is taken synchronously at the instant the
//     tool_result is observed, with pyry still running and `cat` still blocked.
//  3. The window's start is race-free. The blocking open(O_WRONLY) returns the
//     instant `cat` starts — no poll lag. (That lag is what lost #1219's race
//     against a 5 s model-chosen timeout.)
//
// The write end never leaves holdProbeFIFO; the only release is its own
// t.Cleanup. If a caller could close it early, `cat` would exit on its own and
// every did-not-fire rep would be misclassified as fired — a silent false
// positive on the ticket's central question.
//
// # Command choice, and what is not load-bearing
//
// `cat <fifo>` is used because it is not `sleep`-leading (Claude Code never
// auto-backgrounds a `sleep`-leading command — a different mechanism from
// #563's refusal of a standalone `sleep`), because it is the exact command
// whose 2026-07-28 observation gave the no-model-`timeout` branch this probe
// targets, and because it is a single, ps-visible leaf process.
//
// NOT load-bearing: the neutral command and FIFO names. They only reduce a cue
// that might prompt the model to attach a defensive timeout; properties 1–3
// hold regardless. Do not read them as "pick a command claude won't bound" —
// that is the treadmill this shape exists to end. For the same reason no prompt
// here carries a timeout nudge (contrast runningTurnPrompt in
// interactive_stream_running_turn_test.go, which is a live bet on steering this
// exact axis): steering the model is the lever the ticket ruled out.
//
// # Path and env plumbing
//
// `pyry agent-run` defaults to the ptyrunner (interactive TUI) path;
// PYRY_USE_STREAMJSON=1 selects the headless stream-json path. Both hand claude
// pyry's own environment verbatim — ptyrunner leaves cfg.Env nil so os/exec
// passes the parent environment through, and tuidriver.EnsureClaudeEnv only
// materialises it to override TERM. So setting a variable on the `pyry
// agent-run` process is the whole plumbing story: no production change, no
// forwarding list. Whether claude HONOURS it is what this probe measures, and
// probeReadLeverEnv separates "never arrived" from "arrived and was ignored"
// at the OS level, without going through the model.
//
// # Branch hygiene
//
// Every helper here is probe-local by design, including near-duplicates of
// helpers in sigterm_mid_tool_use_test.go (syncBuffer, the descendant walk, the
// session-id poll). That file is being rewritten by PR #1222; ~40 LOC of
// duplication buys independence from a rework of a file this probe has no
// stake in.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// probeEnableEnv gates the live probe. probeFallbackEnv additionally enables
// the conditional stream-json fallback row, which is only worth a live turn
// once the ptyrunner rows have come back did-not-fire.
const (
	probeEnableEnv   = "PYRY_PROBE_BACKGROUND_TRIGGER"
	probeFallbackEnv = "PYRY_PROBE_BACKGROUND_TRIGGER_FALLBACK"
)

// Timing constants. All are generous multiples of the 5 s lever the rows set,
// so a slow turn is never misread as a lever failure.
const (
	// probeClaudeChildDeadline bounds the wait for pyry to spawn claude.
	probeClaudeChildDeadline = 25 * time.Second
	// probeSessionIDDeadline bounds the wait for the system/init line.
	probeSessionIDDeadline = 20 * time.Second
	// probeRendezvousDeadline covers TUI spawn + idle + prompt delivery + the
	// model reaching the Bash call.
	probeRendezvousDeadline = 60 * time.Second
	// probeToolUseDeadline bounds the wait for the tool_use envelope to reach
	// disk; it lags the subprocess by a couple of seconds.
	probeToolUseDeadline = 30 * time.Second
	// probeToolResultDeadline is 9x the 5 s lever. Expiry means did-not-fire,
	// not "too slow".
	probeToolResultDeadline = 45 * time.Second
	// probePollInterval paces every JSONL / process-table poll.
	probePollInterval = 200 * time.Millisecond
	// probePyryExitGrace lets pyry finish the turn and exit on its own after
	// the FIFO release, before the defense-in-depth kill.
	probePyryExitGrace = 20 * time.Second
	// probeFIFOReleaseDeadline bounds the hold goroutine's shutdown.
	probeFIFOReleaseDeadline = 10 * time.Second
)

// Probe fixture shape. The model is pinned so a record is reproducible;
// --max-turns leaves headroom for the turn to COMPLETE (claude receives the
// tool_result and replies), not merely to reach the Bash call.
const (
	probeMaxTurns        = "6"
	probeEffort          = "low"
	probeModel           = "claude-haiku-4-5"
	probeFIFOName        = "probe-hold"
	probeHeldCommandName = "cat"
)

// probeLeverVars is the redaction allowlist for the claude-environment read.
// These artifacts get pasted into a public issue, and the claude child's
// environment contains CLAUDE_CODE_OAUTH_TOKEN and ANTHROPIC_API_KEY — so the
// reader extracts these three names and never dumps the environment.
var probeLeverVars = []string{
	"BASH_DEFAULT_TIMEOUT_MS",
	"BASH_MAX_TIMEOUT_MS",
	"CLAUDE_CODE_DISABLE_BACKGROUND_TASKS",
}

// probeSystemPrompt steers the model toward a single verbatim Bash invocation
// so exactly one tool_use is in flight. It deliberately says nothing about
// timeouts or backgrounding — that axis is the measurement, not the setup.
const probeSystemPrompt = "You are an e2e evidence probe. " +
	"When asked to run a shell command, use the Bash tool exactly once, " +
	"run the command verbatim, do NOT chain commands with && or ;, do NOT " +
	"comment, and do NOT do anything else."

// probePrompt is the timeout-expiry prompt: a single Bash call on a command
// that cannot return while the test holds the FIFO's write end.
func probePrompt(fifoPath string) string {
	return fmt.Sprintf("Use the Bash tool to run `cat %s`. Do nothing else.", fifoPath)
}

// probeRunInBackgroundPrompt is AC4's comparison target. run_in_background is
// a parameter the MODEL fills in, so reaching it means asking for it — which
// is why this row is a comparison and not a determinism candidate.
func probeRunInBackgroundPrompt(fifoPath string) string {
	return fmt.Sprintf("Use the Bash tool to run `cat %s` with run_in_background "+
		"set to true. Do nothing else.", fifoPath)
}

// probeRow is one lever configuration. Rows differ only in the environment
// handed to the pyry process and, for one row, the prompt.
type probeRow struct {
	name string
	// env is appended to os.Environ() on the pyry process. It is also the
	// verbatim "env delta" recorded in the evidence.
	env []string
	// prompt is nil for the default timeout-expiry prompt.
	prompt func(fifoPath string) string
	// reps is 3 only where AC3's "at least three times" applies.
	reps int
	// conditional rows skip unless probeFallbackEnv is set.
	conditional bool
	// why is copied into the record so a pasted artifact explains itself.
	why string
}

// probeRows is the lever matrix. Nine live turns across the five default rows.
// Run ROW BY ROW (-run 'TestRealClaude_BackgroundHandleTrigger/<row>'): a
// whole-table run can exceed both the go test default timeout and a single
// tool call's budget.
//
// There is no no-env baseline row: AC4 permits the ticket's quoted 2026-07-27
// transcript as the expiry-path baseline and does not require reproducing that
// coin flip afresh.
var probeRows = []probeRow{
	{
		name: "default-only",
		env:  []string{"BASH_DEFAULT_TIMEOUT_MS=5000"},
		reps: 3,
		why: "L1 in isolation — the branch where the model sets no timeout, " +
			"which is the branch that broke determinism on 2026-07-28.",
	},
	{
		name: "max-only",
		env:  []string{"BASH_MAX_TIMEOUT_MS=5000"},
		reps: 1,
		why: "L2 in isolation. Against a command the model does not bound, a " +
			"ceiling on what the model MAY set should not apply — a did-not-fire " +
			"here is AC2's per-lever attribution evidence, not a failure.",
	},
	{
		name: "both",
		env:  []string{"BASH_DEFAULT_TIMEOUT_MS=5000", "BASH_MAX_TIMEOUT_MS=5000"},
		reps: 3,
		why: "The ticket's primary candidate: the default covers the branch " +
			"where the model sets no timeout, the ceiling covers the branch " +
			"where it sets one. Neither alone covers both.",
	},
	{
		name: "disabled",
		env: []string{
			"BASH_DEFAULT_TIMEOUT_MS=5000",
			"BASH_MAX_TIMEOUT_MS=5000",
			"CLAUDE_CODE_DISABLE_BACKGROUND_TASKS=1",
		},
		reps: 1,
		why: "Free negative control. If a lever fires above and this suppresses " +
			"it, the timeout-expiry path is confirmed as what fired.",
	},
	{
		name:   "run-in-background",
		env:    nil,
		prompt: probeRunInBackgroundPrompt,
		reps:   1,
		why: "AC4's comparison target, not a determinism candidate — the model " +
			"fills this parameter in.",
	},
	{
		name: "both-streamjson",
		env: []string{
			"BASH_DEFAULT_TIMEOUT_MS=5000",
			"BASH_MAX_TIMEOUT_MS=5000",
			"PYRY_USE_STREAMJSON=1",
		},
		reps:        1,
		conditional: true,
		why: "Conditional fallback: run ONLY after default-only and both have " +
			"both come back did-not-fire on the ptyrunner path. A lever that " +
			"fires here but not under the TUI is a first-class AC2 finding.",
	},
}

// Probe outcomes. Three-valued for the lever question, plus a fourth for the
// case where the model never issued the Bash call at all — which is not
// evidence about the lever.
const (
	// probeFiredBackground: a matching tool_result appeared while the write
	// end was still held AND the held command is still a descendant of pyry.
	// The command was moved aside, not killed.
	probeFiredBackground = "fired-background"
	// probeFiredOther: matching tool_result present, but the held command is
	// absent from the snapshot. Recorded verbatim; no conclusion drawn.
	probeFiredOther = "fired-other"
	// probeDidNotFire: no matching tool_result within the deadline.
	probeDidNotFire = "did-not-fire"
	// probeCommandNeverRan: the rendezvous or the tool_use never happened.
	probeCommandNeverRan = "command-never-ran"
)

// probeProc is one row of a `ps -axo pid=,ppid=,pgid=` snapshot, optionally
// annotated with its leaf command name from a follow-up ps.
type probeProc struct {
	PID     int    `json:"pid"`
	PPID    int    `json:"ppid"`
	PGID    int    `json:"pgid"`
	Command string `json:"command,omitempty"`
}

// probeRecord is the per-rep evidence. It is written as JSON next to sibling
// files holding the byte-verbatim captures (AC1 says verbatim, so the JSONL
// lines and the ps snapshot are never reformatted).
type probeRecord struct {
	Ticket        string   `json:"ticket"`
	Row           string   `json:"row"`
	Rep           int      `json:"rep"`
	Why           string   `json:"why"`
	ClaudeBin     string   `json:"claude_bin"`
	ClaudeVersion string   `json:"claude_version"`
	RunnerPath    string   `json:"runner_path"`
	Model         string   `json:"model"`
	EnvDelta      []string `json:"env_delta"`
	Prompt        string   `json:"prompt"`
	SystemPrompt  string   `json:"system_prompt"`
	Workdir       string   `json:"workdir"`
	FIFOPath      string   `json:"fifo_path"`
	PyryPID       int      `json:"pyry_pid"`
	ClaudePID     int      `json:"claude_pid"`
	SessionID     string   `json:"session_id"`
	SessionJSONL  string   `json:"session_jsonl_path"`

	Outcome         string `json:"outcome"`
	OutcomeDetail   string `json:"outcome_detail,omitempty"`
	RendezvousFired bool   `json:"rendezvous_fired"`
	ToolUseFound    bool   `json:"tool_use_found"`
	ToolResultFound bool   `json:"tool_result_found"`

	// ToolUseInput is the model's verbatim Bash params. InputTimeout* are
	// DIAGNOSTIC, never dispositive: they say which branch a rep exercised
	// (model-set vs default-applied), and nothing about whether a lever fired.
	ToolUseInput        json.RawMessage `json:"tool_use_input,omitempty"`
	InputTimeoutPresent bool            `json:"input_timeout_present"`
	InputTimeoutValue   string          `json:"input_timeout_value,omitempty"`

	// SnapshotDuringTurn records whether pyry was still running when the ps
	// snapshot was taken. AC5 requires the snapshot to precede `pyry agent-run`
	// returning; this self-evidences it rather than asking the reader to trust
	// it. False means AC5 is unmet for this rep — a stated fact, not a gap.
	SnapshotDuringTurn bool        `json:"snapshot_taken_during_turn"`
	PSDescendants      []probeProc `json:"ps_descendants,omitempty"`
	PSError            string      `json:"ps_error,omitempty"`
	HeldCommandAlive   bool        `json:"held_command_alive_in_snapshot"`

	LeverEnvInClaude map[string]string `json:"lever_env_in_claude"`
	LeverEnvMethod   string            `json:"lever_env_read_method,omitempty"`
	LeverEnvError    string            `json:"lever_env_read_error,omitempty"`

	WorkdirListing []string `json:"workdir_listing,omitempty"`
	Notes          []string `json:"notes,omitempty"`

	// Byte-verbatim captures, written as sibling files rather than embedded.
	// Unexported, so encoding/json skips them.
	rawToolUse    []byte
	rawToolResult []byte
	rawPS         []byte
	rawStdout     []byte
	rawStderr     []byte
}

func (r *probeRecord) note(format string, args ...any) {
	r.Notes = append(r.Notes, fmt.Sprintf(format, args...))
}

// TestRealClaude_BackgroundHandleTrigger runs the lever matrix against a live
// claude and records what each row does. It asserts almost nothing: t.Fatalf
// fires only on structural failure (pyry won't build, mkfifo fails, pyry never
// spawns claude, no system/init session id). did-not-fire, command-never-ran
// and reps disagreeing are all t.Logf'd into the record — per AC3 a lever that
// fires intermittently is REPORTED as non-deterministic, not made a red test.
func TestRealClaude_BackgroundHandleTrigger(t *testing.T) {
	if os.Getenv(probeEnableEnv) != "1" {
		t.Skipf("#1223 background-handle trigger probe: skipped because %s != 1.\n"+
			"This is an EVIDENCE PROBE, not a regression gate — a skip here is the "+
			"normal `make e2e-realclaude` outcome and says nothing about pyry's "+
			"behaviour. It costs ~9 live claude turns.\n"+
			"Run one row at a time (a whole-table run can outlast the go test "+
			"timeout and a single tool call's budget):\n"+
			"  %s=1 go test -tags e2e_realclaude -timeout 20m -v \\\n"+
			"    -run 'TestRealClaude_BackgroundHandleTrigger/default-only' \\\n"+
			"    ./internal/e2e/realclaude/\n"+
			"Rows: default-only, max-only, both, disabled, run-in-background"+
			" (and both-streamjson, which additionally needs %s=1).",
			probeEnableEnv, probeEnableEnv, probeFallbackEnv)
	}

	// Deliberately NOT t.TempDir(): that is removed when the test ends, and the
	// developer needs these files afterwards to compose the issue comments.
	artifactDir, err := os.MkdirTemp("", "pyry-1223-probe-*")
	if err != nil {
		t.Fatalf("create artifact dir: %v", err)
	}
	t.Logf("#1223 probe artifacts: %s", artifactDir)

	for _, row := range probeRows {
		t.Run(row.name, func(t *testing.T) {
			if row.conditional && os.Getenv(probeFallbackEnv) != "1" {
				t.Skipf("row %q is the conditional fallback (%s); set %s=1 to run it. "+
					"Only worth a live turn once default-only and both have both "+
					"come back did-not-fire on the ptyrunner path.",
					row.name, row.why, probeFallbackEnv)
			}
			for rep := 1; rep <= row.reps; rep++ {
				t.Run(fmt.Sprintf("rep%d", rep), func(t *testing.T) {
					runProbeRep(t, artifactDir, row, rep)
				})
			}
		})
	}
}

// runProbeRep stages one rendezvous, observes what claude does with it, and
// writes the evidence. Each rep is its own subtest so it gets its own
// t.TempDir, t.Setenv and t.Cleanup — reps must never share a HOME or a FIFO.
func runProbeRep(t *testing.T, artifactDir string, row probeRow, rep int) {
	// Skips (without credentials) before anything is created.
	workdir := WithWorktreeAuthenticated(t)
	claudeBin := resolveClaudeBin(t)

	promptFn := row.prompt
	if promptFn == nil {
		promptFn = probePrompt
	}
	fifoPath := filepath.Join(workdir, probeFIFOName)

	rec := &probeRecord{
		Ticket:        "1223",
		Row:           row.name,
		Rep:           rep,
		Why:           row.why,
		ClaudeBin:     claudeBin,
		ClaudeVersion: probeClaudeVersion(claudeBin),
		RunnerPath:    probeRunnerPath(row.env),
		Model:         probeModel,
		EnvDelta:      row.env,
		Prompt:        promptFn(fifoPath),
		SystemPrompt:  probeSystemPrompt,
		Workdir:       workdir,
		FIFOPath:      fifoPath,
		Outcome:       probeCommandNeverRan,
		OutcomeDetail: "rep did not reach a classification point",
	}

	// Registered FIRST so LIFO runs it LAST: the record is complete by then,
	// and a structural t.Fatalf below still leaves partial evidence on disk.
	t.Cleanup(func() { writeProbeArtifacts(t, artifactDir, rec) })

	// Registered BEFORE holdProbeFIFO so LIFO releases the FIFO first and pyry
	// gets a real chance to finish the turn and exit on its own; this is the
	// defense-in-depth net for the case where it does not. ESRCH is benign.
	pyryExited := make(chan struct{})
	t.Cleanup(func() {
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
	if err := os.WriteFile(systemPath, []byte(rec.SystemPrompt), 0o600); err != nil {
		t.Fatalf("write %s: %v", systemPath, err)
	}

	bin := ensurePyryBuilt(t)
	var stdout, stderr probeSyncBuffer
	cmd := spawnProbePyry(t, bin, workdir, promptPath, systemPath, row.env, &stdout, &stderr)
	rec.PyryPID = cmd.Process.Pid

	// pyry is a direct child of this process, so it becomes a zombie between
	// exit and Wait — and a zombie answers Signal(0) with nil. A liveness probe
	// would therefore report "still running" for an already-returned pyry,
	// which is exactly the AC5 property being evidenced. Wait in a goroutine
	// and read the channel instead; the goroutine's shutdown path is pyry's
	// exit, which the cleanup above guarantees.
	go func() {
		_ = cmd.Wait()
		close(pyryExited)
	}()

	claudePID := probeWaitForDirectChild(rec.PyryPID, probeClaudeChildDeadline)
	if claudePID == 0 {
		t.Fatalf("pyry never spawned a claude child within %s\nstderr:\n%s",
			probeClaudeChildDeadline, truncate(stderr.Bytes()))
	}
	rec.ClaudePID = claudePID

	sessionID := probeWaitForSessionID(&stdout, probeSessionIDDeadline)
	if sessionID == "" {
		t.Fatalf("no system/init session_id on pyry stdout within %s\nstderr:\n%s",
			probeSessionIDDeadline, truncate(stderr.Bytes()))
	}
	rec.SessionID = sessionID
	rec.SessionJSONL = jsonlPathFor(workdir, sessionID)

	// The rendezvous is the moment `cat` opened the FIFO: the command is
	// genuinely executing, and from here it cannot complete on its own.
	select {
	case <-rendezvous:
		rec.RendezvousFired = true
	case <-time.After(probeRendezvousDeadline):
		rec.Outcome = probeCommandNeverRan
		rec.OutcomeDetail = fmt.Sprintf("no reader opened %s within %s — the model "+
			"never issued the Bash call. Not evidence about the lever.",
			fifoPath, probeRendezvousDeadline)
		probeFinish(t, rec, &stdout, &stderr)
		return
	}

	toolUseID, toolUseRaw := probeWaitForBashToolUse(t, workdir, sessionID, probeToolUseDeadline)
	if toolUseID == "" {
		rec.Outcome = probeCommandNeverRan
		rec.OutcomeDetail = fmt.Sprintf("the rendezvous DID fire (the command ran) but no "+
			"Bash tool_use reached %s within %s", rec.SessionJSONL, probeToolUseDeadline)
		probeFinish(t, rec, &stdout, &stderr)
		return
	}
	rec.ToolUseFound = true
	rec.rawToolUse = toolUseRaw
	rec.ToolUseInput, rec.InputTimeoutPresent, rec.InputTimeoutValue = probeToolUseInput(toolUseRaw, toolUseID)

	resultRaw := probeWaitForToolResult(t, workdir, sessionID, toolUseID, probeToolResultDeadline)

	// The classification point. The FIFO's write end is still held — the only
	// release is holdProbeFIFO's own t.Cleanup, which by construction runs
	// after this function returns. So a tool_result here means claude ended the
	// call itself; the command cannot have completed.
	snap := probeProcessSnapshot(rec.PyryPID)
	rec.rawPS = snap.raw
	rec.PSDescendants = snap.descendants
	rec.PSError = snap.err
	rec.HeldCommandAlive = probeHasCommand(snap.descendants, probeHeldCommandName)
	select {
	case <-pyryExited:
		rec.SnapshotDuringTurn = false
		rec.note("snapshot-window-missed: pyry had already returned when the ps " +
			"snapshot was taken, so AC5's during-the-turn requirement is unmet " +
			"for this rep")
	default:
		rec.SnapshotDuringTurn = true
	}
	rec.LeverEnvInClaude, rec.LeverEnvMethod, rec.LeverEnvError = probeReadLeverEnv(claudePID)
	rec.WorkdirListing = probeListWorkdir(workdir)

	switch {
	case resultRaw == nil:
		rec.Outcome = probeDidNotFire
		rec.OutcomeDetail = fmt.Sprintf("no tool_result matching tool_use_id=%s within %s "+
			"of the tool_use — the call was still open and `cat` still blocked",
			toolUseID, probeToolResultDeadline)
	case rec.HeldCommandAlive:
		rec.ToolResultFound = true
		rec.rawToolResult = resultRaw
		rec.Outcome = probeFiredBackground
		rec.OutcomeDetail = fmt.Sprintf("tool_result for tool_use_id=%s landed while the "+
			"FIFO write end was still held, and %q is still a descendant of pyry — "+
			"the command was moved aside, not killed", toolUseID, probeHeldCommandName)
	default:
		rec.ToolResultFound = true
		rec.rawToolResult = resultRaw
		rec.Outcome = probeFiredOther
		rec.OutcomeDetail = fmt.Sprintf("tool_result for tool_use_id=%s landed while the "+
			"FIFO write end was still held, but %q is absent from the ps snapshot — "+
			"claude bounded the call and the command did not survive. Recorded "+
			"verbatim; no conclusion drawn", toolUseID, probeHeldCommandName)
	}

	probeFinish(t, rec, &stdout, &stderr)
}

// probeFinish captures pyry's streams into the record and logs the rep's
// classification. It never fails the test: every non-structural outcome is
// evidence, including the null one the ticket explicitly sanctions.
func probeFinish(t *testing.T, rec *probeRecord, stdout, stderr *probeSyncBuffer) {
	t.Helper()
	rec.rawStdout = stdout.Bytes()
	rec.rawStderr = stderr.Bytes()
	t.Logf("#1223 %s/rep%d: outcome=%s runner=%s input.timeout=%s\n  %s",
		rec.Row, rec.Rep, rec.Outcome, rec.RunnerPath,
		probeTimeoutSummary(rec), rec.OutcomeDetail)
}

func probeTimeoutSummary(rec *probeRecord) string {
	if !rec.InputTimeoutPresent {
		return "absent"
	}
	return rec.InputTimeoutValue
}

// probeRunnerPath names which of pyry's two runners the row selects.
// PYRY_USE_STREAMJSON=1 (and only the exact string "1") picks the headless
// stream-json path; everything else falls through to the ptyrunner default.
func probeRunnerPath(env []string) string {
	for _, kv := range env {
		if kv == "PYRY_USE_STREAMJSON=1" {
			return "streamrunner (headless stream-json)"
		}
	}
	return "ptyrunner (interactive TUI, the agent-run default)"
}

// probeClaudeVersion records the claude build a rep ran against. Best-effort:
// a version we could not read is recorded as such rather than failing the rep,
// because the transcript is still evidence without it.
func probeClaudeVersion(claudeBin string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, claudeBin, "--version").CombinedOutput()
	if err != nil {
		return fmt.Sprintf("<unavailable: %v>", err)
	}
	return strings.TrimSpace(string(out))
}

// spawnProbePyry starts `pyry agent-run` asynchronously with the row's env
// appended to this process's environment. Setpgid makes pyry its own
// process-group leader, so the defense-in-depth cleanup can reap the group and
// so the process-tree walk has a stable root. File-local by design — see the
// header's branch-hygiene note.
func spawnProbePyry(t *testing.T, bin, workdir, promptPath, systemPath string, extraEnv []string, stdout, stderr *probeSyncBuffer) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(bin,
		"agent-run",
		"--prompt-file="+promptPath,
		"--system-prompt-file="+systemPath,
		"--allowed-tools=Bash",
		"--max-turns="+probeMaxTurns,
		"--effort="+probeEffort,
		"--model="+probeModel,
		"--workdir="+workdir,
		"--output-format=stream-json",
	)
	// The whole env-plumbing story: pyry passes its own environment to claude
	// verbatim on both runner paths, so setting a variable here lands it in
	// claude's environment with no production change.
	cmd.Env = append(os.Environ(), extraEnv...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawnProbePyry: start %s: %v", bin, err)
	}
	return cmd
}

// holdProbeFIFO creates a FIFO at path and starts a goroutine that blocks in
// open(path, O_WRONLY) until a reader opens the other end. Closing the returned
// channel is the rendezvous signal: it fires the instant claude's `cat` starts,
// with no poll lag.
//
// The write end NEVER leaves this helper, and the only release is the t.Cleanup
// registered here — which by construction runs after the subtest body. Handing
// the *os.File to the caller would let a rep close it early, `cat` would exit on
// its own, and a did-not-fire rep would be misclassified as fired.
//
// A closed channel rather than a buffered send: the rendezvous may be awaited
// by more than one receiver (the body and, in the self-check, a cleanup
// assertion), and a buffered send deadlocks the second waiter.
//
// Shutdown path: if no reader ever arrives the goroutine is still parked in
// open(), so the cleanup opens the read end non-blockingly to release it.
func holdProbeFIFO(t *testing.T, path string) <-chan struct{} {
	t.Helper()
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("holdProbeFIFO: mkfifo %s: %v", path, err)
	}

	var (
		rendezvous = make(chan struct{})
		release    = make(chan struct{})
		done       = make(chan struct{})
		openErr    error
	)
	go func() {
		defer close(done)
		// Blocks until a reader opens the FIFO — this IS the rendezvous, not
		// merely a block.
		f, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err != nil {
			openErr = err
			return
		}
		close(rendezvous)
		<-release
		_ = f.Close()
	}()

	t.Cleanup(func() {
		close(release)
		select {
		case <-rendezvous:
			// The write end is open; close(release) above lets the goroutine
			// close it, which is the EOF that finally lets `cat` exit.
		default:
			// No reader ever arrived, so the goroutine is parked in open().
			// Opening the read end non-blockingly unblocks it. Harmless if the
			// rendezvous fired in the meantime: an extra reader does not send
			// EOF to `cat` (only the last writer closing does).
			if rf, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0); err == nil {
				_ = rf.Close()
			}
		}
		select {
		case <-done:
			if openErr != nil {
				t.Errorf("holdProbeFIFO: open %s for write: %v", path, openErr)
			}
		case <-time.After(probeFIFOReleaseDeadline):
			t.Errorf("holdProbeFIFO: hold goroutine did not exit within %s of release; "+
				"the FIFO write end may still be open", probeFIFOReleaseDeadline)
		}
	})

	return rendezvous
}

// probeSyncBuffer is a goroutine-safe byte buffer. os/exec writes the child's
// streams from a copier goroutine while this test reads them mid-run (to learn
// the session id), so a plain bytes.Buffer would be a data race under -race.
// Probe-local on purpose — see the header's branch-hygiene note.
type probeSyncBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (b *probeSyncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	return len(p), nil
}

// Bytes returns a copy so callers never read the underlying array while the
// copier goroutine appends to it.
func (b *probeSyncBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]byte, len(b.buf))
	copy(out, b.buf)
	return out
}

// probeWaitForSessionID polls pyry's stdout for the system/init session_id.
// Returns "" on timeout.
func probeWaitForSessionID(stdout *probeSyncBuffer, timeout time.Duration) string {
	deadline := time.Now().Add(timeout)
	for {
		if id := parseInitSessionID(stdout.Bytes()); id != "" {
			return id
		}
		if !time.Now().Before(deadline) {
			return ""
		}
		time.Sleep(probePollInterval)
	}
}

// probeWaitForBashToolUse polls the session JSONL until a Bash tool_use
// envelope is flushed, returning its id and the verbatim line bytes. The
// envelope lags the subprocess by a couple of seconds.
func probeWaitForBashToolUse(t *testing.T, workdir, sessionID string, timeout time.Duration) (string, []byte) {
	t.Helper()
	path := jsonlPathFor(workdir, sessionID)
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			for _, e := range ReadJSONL(t, workdir, sessionID) {
				if e.Kind != "assistant" {
					continue
				}
				blocks, err := parseContentBlocks(e.Raw)
				if err != nil {
					continue
				}
				for _, b := range blocks {
					if b.Type == "tool_use" && b.Name == "Bash" && b.ID != "" {
						return b.ID, append([]byte(nil), e.Raw...)
					}
				}
			}
		}
		if !time.Now().Before(deadline) {
			return "", nil
		}
		time.Sleep(probePollInterval)
	}
}

// probeWaitForToolResult polls the session JSONL for a tool_result matching
// toolUseID, returning the verbatim line bytes. nil means the call was still
// open when the deadline expired — the did-not-fire signal.
func probeWaitForToolResult(t *testing.T, workdir, sessionID, toolUseID string, timeout time.Duration) []byte {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		for _, e := range ReadJSONL(t, workdir, sessionID) {
			if e.Kind != "user" {
				continue
			}
			blocks, err := parseContentBlocks(e.Raw)
			if err != nil {
				continue
			}
			for _, b := range blocks {
				if b.Type == "tool_result" && b.ToolUseID == toolUseID {
					return append([]byte(nil), e.Raw...)
				}
			}
		}
		if !time.Now().Before(deadline) {
			return nil
		}
		time.Sleep(probePollInterval)
	}
}

// probeToolUseInput projects the model's verbatim Bash params out of an
// assistant line, plus whether input.timeout was present and its value.
//
// contentBlock (tool_loop_test.go) deliberately does not carry `input`; this is
// an additional narrow projection of a field it omits, not a redefinition.
//
// input.timeout is DIAGNOSTIC, never dispositive. It says which branch a rep
// exercised — model-set timeout vs default-applied — and nothing about whether
// a lever fired. Classification is structural (§ Mechanism).
func probeToolUseInput(raw []byte, toolUseID string) (json.RawMessage, bool, string) {
	var envelope struct {
		Message struct {
			Content []struct {
				ID    string          `json:"id"`
				Input json.RawMessage `json:"input"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, false, ""
	}
	for _, b := range envelope.Message.Content {
		if b.ID != toolUseID || len(b.Input) == 0 {
			continue
		}
		var params struct {
			Timeout *json.Number `json:"timeout"`
		}
		if err := json.Unmarshal(b.Input, &params); err != nil || params.Timeout == nil {
			return b.Input, false, ""
		}
		return b.Input, true, params.Timeout.String()
	}
	return nil, false, ""
}

// probeSnapshot is AC5's raw process readout plus the descendant subtree parsed
// out of that same snapshot. One `ps -axo pid=,ppid=,pgid=` exec serves both
// the classifier and the evidence.
type probeSnapshot struct {
	raw         []byte
	descendants []probeProc
	err         string
}

// probeProcessSnapshot takes the AC5 snapshot and resolves the leaf command
// name of each descendant. The wide snapshot has no command column and AC5 pins
// its exact format, so the names come from one narrow follow-up ps rather than
// from widening (and so falsifying) the recorded readout.
func probeProcessSnapshot(root int) probeSnapshot {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	raw, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,ppid=,pgid=").Output()
	if err != nil {
		return probeSnapshot{raw: raw, err: fmt.Sprintf("ps -axo pid=,ppid=,pgid=: %v", err)}
	}
	descendants := probeDescendantsFromPS(raw, root)
	return probeSnapshot{
		raw:         raw,
		descendants: probeAnnotateCommands(descendants),
	}
}

// probeDescendantsFromPS returns every transitive child of root found in a
// `ps -axo pid=,ppid=,pgid=` snapshot, in breadth-first order.
//
// Mirrors the production parse in internal/agentrun/reap.go's descendantPGIDs
// (the trailing `=` on each column suppresses the header, so every line is
// three whitespace-separated integers) so this evidence is directly comparable
// with what pyry's reap walk sees — #1224–#1227 read it against that code.
// Pure over its bytes so the credential-free self-check can drive it with a
// synthetic tree. `seen` guards against the cycle a stale snapshot with pid
// reuse could introduce.
func probeDescendantsFromPS(snapshot []byte, root int) []probeProc {
	children := make(map[int][]int)
	byPID := make(map[int]probeProc)
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
		children[ppid] = append(children[ppid], pid)
		byPID[pid] = probeProc{PID: pid, PPID: ppid, PGID: pgid}
	}

	var out []probeProc
	seen := make(map[int]bool)
	queue := append([]int(nil), children[root]...)
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		if seen[pid] {
			continue
		}
		seen[pid] = true
		if p, ok := byPID[pid]; ok {
			out = append(out, p)
		}
		queue = append(queue, children[pid]...)
	}
	return out
}

// probeAnnotateCommands fills in each descendant's leaf command name (argv[0]
// with any directory stripped) from a single narrow ps. Best-effort: a process
// that exited between the two calls is simply left unannotated.
func probeAnnotateCommands(procs []probeProc) []probeProc {
	if len(procs) == 0 {
		return procs
	}
	pids := make([]string, 0, len(procs))
	for _, p := range procs {
		pids = append(pids, strconv.Itoa(p.PID))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-o", "pid=,command=", "-p", strings.Join(pids, ",")).Output()
	if err != nil {
		return procs
	}
	names := make(map[int]string)
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pid, convErr := strconv.Atoi(fields[0])
		if convErr != nil {
			continue
		}
		names[pid] = filepath.Base(fields[1])
	}
	for i := range procs {
		procs[i].Command = names[procs[i].PID]
	}
	return procs
}

// probeHasCommand reports whether any descendant's leaf command name matches.
func probeHasCommand(procs []probeProc, name string) bool {
	for _, p := range procs {
		if p.Command == name {
			return true
		}
	}
	return false
}

// probeWaitForDirectChild blocks until root has a direct child, returning its
// pid (0 on timeout). Reads the same ps snapshot shape as everything else here
// rather than adding a second process-enumeration mechanism.
func probeWaitForDirectChild(root int, timeout time.Duration) int {
	deadline := time.Now().Add(timeout)
	for {
		for _, p := range probeProcessSnapshot(root).descendants {
			if p.PPID == root {
				return p.PID
			}
		}
		if !time.Now().Before(deadline) {
			return 0
		}
		time.Sleep(probePollInterval)
	}
}

// probeReadLeverEnv reads the lever variables out of the claude child's
// environment at the OS level. This is the different-fabric control on the
// largest confound of a null outcome: "the variable never reached claude" looks
// identical to "claude ignored it" from the transcript alone, and this
// separates them without spending a live turn and without going through the
// model.
//
// REDACTION IS MANDATORY. claude's environment holds CLAUDE_CODE_OAUTH_TOKEN
// and ANTHROPIC_API_KEY, and these artifacts get pasted into a public issue —
// so only the three names in probeLeverVars are extracted, and the raw ps
// output is never retained. A variable absent from the environment is reported
// as "<absent>", which is itself a finding.
func probeReadLeverEnv(pid int) (map[string]string, string, string) {
	// macOS wants `ps -E -ww -p`; procps wants the BSD-syntax `ps eww -p`.
	variants := [][]string{
		{"-E", "-ww", "-p", strconv.Itoa(pid)},
		{"eww", "-p", strconv.Itoa(pid)},
	}
	var lastErr string
	for _, args := range variants {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		out, err := exec.CommandContext(ctx, "ps", args...).Output()
		cancel()
		if err != nil {
			lastErr = fmt.Sprintf("ps %s: %v", strings.Join(args, " "), err)
			continue
		}
		found := make(map[string]string, len(probeLeverVars))
		for _, name := range probeLeverVars {
			found[name] = "<absent>"
		}
		hit := false
		for _, field := range strings.Fields(string(out)) {
			for _, name := range probeLeverVars {
				if strings.HasPrefix(field, name+"=") {
					found[name] = strings.TrimPrefix(field, name+"=")
					hit = true
				}
			}
		}
		if !hit {
			// The command succeeded but showed no lever variable. That is
			// either a genuine absence or an environment ps declined to
			// print; try the other syntax before believing it.
			lastErr = fmt.Sprintf("ps %s: no lever variable in output", strings.Join(args, " "))
			continue
		}
		return found, "ps " + strings.Join(args, " "), ""
	}
	absent := make(map[string]string, len(probeLeverVars))
	for _, name := range probeLeverVars {
		absent[name] = "<unread>"
	}
	return absent, "", lastErr + " (fall back to a one-turn `printenv BASH_DEFAULT_TIMEOUT_MS` row; " +
		"that route is model-mediated, hence the fallback)"
}

// probeListWorkdir returns the workdir's entry NAMES ONLY. Under
// WithWorktreeAuthenticated the workdir IS the pinned HOME, so this listing
// shows .claude/ and .claude.json — list them, never their contents.
func probeListWorkdir(workdir string) []string {
	entries, err := os.ReadDir(workdir)
	if err != nil {
		return []string{fmt.Sprintf("<read %s: %v>", workdir, err)}
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// writeProbeArtifacts persists one rep's evidence to a directory that survives
// the test. The verbatim captures go to sibling files rather than into the JSON
// record: AC1 asks for verbatim envelopes, and a JSON-embedded string is
// escaped, not verbatim.
//
// pyry's stdout/stderr are truncated to 1024 bytes here, but SKIM THEM before
// pasting anything into a public issue.
func writeProbeArtifacts(t *testing.T, dir string, rec *probeRecord) {
	t.Helper()
	stem := filepath.Join(dir, fmt.Sprintf("%s-rep%d", rec.Row, rec.Rep))

	blob, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Errorf("marshal probe record %s: %v", stem, err)
		return
	}
	files := map[string][]byte{
		stem + ".json":              append(blob, '\n'),
		stem + ".tool_use.jsonl":    rec.rawToolUse,
		stem + ".tool_result.jsonl": rec.rawToolResult,
		stem + ".ps.txt":            rec.rawPS,
		stem + ".pyry-stdout.txt":   []byte(truncate(rec.rawStdout)),
		stem + ".pyry-stderr.txt":   []byte(truncate(rec.rawStderr)),
	}
	for path, content := range files {
		if len(content) == 0 {
			continue
		}
		if err := os.WriteFile(path, content, 0o600); err != nil {
			// The evidence IS this ticket's deliverable, so a lost artifact is
			// loud rather than silent — but it does not abort the remaining
			// cleanups, so t.Errorf and not t.Fatalf.
			t.Errorf("write probe artifact %s: %v", path, err)
		}
	}
}

// --- credential-free self-checks -------------------------------------------
//
// These are the only deterministic tests in this file, and the only ones that
// run without credentials or the opt-in gate. Without them a bug in the hold
// would turn every did-not-fire rep into a false fired:
//
//	go test -tags e2e_realclaude -v -run 'TestProbeFIFOHold|TestProbeDescendantsFromPS' \
//	  ./internal/e2e/realclaude/

// TestProbeFIFOHold_HoldsReaderUntilCleanupRelease is the "did the release
// work" seam. It stands a local `cat` in for claude and asserts all three
// properties the classification rests on: the rendezvous fires when the reader
// opens, the reader stays blocked while the write end is held, and it exits
// once — and only once — the cleanup release runs.
//
// The exit assertion is registered BEFORE holdProbeFIFO, so t.Cleanup's LIFO
// order puts it AFTER the release. Registering it later would run it before the
// release and it would fail spuriously; adding a kill-the-reader safety net
// after holdProbeFIFO would make it pass vacuously.
func TestProbeFIFOHold_HoldsReaderUntilCleanupRelease(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, probeFIFOName)

	var (
		cmd    *exec.Cmd
		exited = make(chan struct{})
	)

	// Registered FIRST → runs LAST, after holdProbeFIFO's release.
	t.Cleanup(func() {
		if cmd == nil {
			return
		}
		select {
		case <-exited:
		case <-time.After(probeFIFOReleaseDeadline):
			_ = cmd.Process.Kill()
			t.Errorf("reader did not exit within %s of the cleanup release — the "+
				"FIFO write end was not closed, so a did-not-fire rep could be "+
				"misclassified as fired", probeFIFOReleaseDeadline)
		}
	})

	rendezvous := holdProbeFIFO(t, path)

	cmd = exec.Command(probeHeldCommandName, path)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s %s: %v", probeHeldCommandName, path, err)
	}
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()

	select {
	case <-rendezvous:
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("rendezvous never fired although a reader opened %s — the hold "+
			"goroutine's blocking open(O_WRONLY) did not return", path)
	}

	// The hold is real: with the write end held and nothing written, the reader
	// must still be blocked in read().
	select {
	case <-exited:
		t.Fatalf("reader exited while the write end was still held — the hold is " +
			"not real, and every did-not-fire rep would be misread as fired")
	case <-time.After(500 * time.Millisecond):
	}
}

// TestProbeDescendantsFromPS_SyntheticTree drives the descendant walk with a
// synthetic `ps -axo pid=,ppid=,pgid=` snapshot, so the parse the classifier
// and AC5's readout both depend on is verified without a live process tree.
// Mirrors the shape of internal/agentrun/reap_test.go, whose production parse
// this one deliberately matches.
func TestProbeDescendantsFromPS_SyntheticTree(t *testing.T) {
	// pyry(100) → claude(200) → zsh(300) → cat(400); 500 is an unrelated
	// sibling and 600 is a second-level branch under claude.
	const snapshot = `
    1     0     1
  100     1   100
  200   100   200
  300   200   300
  400   300   300
  600   200   600
  500     1   500
`
	tests := []struct {
		name string
		root int
		want []probeProc
	}{
		{
			name: "full subtree in breadth-first order",
			root: 100,
			want: []probeProc{
				{PID: 200, PPID: 100, PGID: 200},
				{PID: 300, PPID: 200, PGID: 300},
				{PID: 600, PPID: 200, PGID: 600},
				{PID: 400, PPID: 300, PGID: 300},
			},
		},
		{
			name: "mid-tree root excludes ancestors and unrelated siblings",
			root: 300,
			want: []probeProc{{PID: 400, PPID: 300, PGID: 300}},
		},
		{
			name: "leaf has no descendants",
			root: 400,
			want: nil,
		},
		{
			name: "unknown root has no descendants",
			root: 999,
			want: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := probeDescendantsFromPS([]byte(snapshot), tc.root)
			if len(got) != len(tc.want) {
				t.Fatalf("descendants of %d: got %+v, want %+v", tc.root, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("descendants of %d [%d]: got %+v, want %+v",
						tc.root, i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestProbeDescendantsFromPS_MalformedLines pins that a snapshot with a header,
// short lines and non-numeric columns is tolerated rather than derailing the
// walk — `ps` output shape is the one input this parse does not control.
func TestProbeDescendantsFromPS_MalformedLines(t *testing.T) {
	const snapshot = `PID PPID PGID
  100     1   100
garbage
  200   100   x
  201   100   201
  202
`
	got := probeDescendantsFromPS([]byte(snapshot), 100)
	want := []probeProc{{PID: 201, PPID: 100, PGID: 201}}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("descendants of 100: got %+v, want %+v", got, want)
	}
}

// TestProbeToolUseInput_TimeoutProjection pins the diagnostic projection
// against both observed branches: the 2026-07-27 `tail -f` tool_use carried
// input.timeout, and the 2026-07-28 `cat <fifo>` tool_use on the same claude
// build carried no timeout field at all. Envelopes are shaped as claude writes
// them so the projection is not merely self-consistent.
func TestProbeToolUseInput_TimeoutProjection(t *testing.T) {
	tests := []struct {
		name        string
		line        string
		id          string
		wantPresent bool
		wantValue   string
	}{
		{
			name: "model-set timeout (the 2026-07-27 branch)",
			line: `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_a",` +
				`"name":"Bash","input":{"command":"tail -f /dev/null","timeout":5000}}]}}`,
			id:          "toolu_a",
			wantPresent: true,
			wantValue:   "5000",
		},
		{
			name: "no timeout field (the 2026-07-28 branch)",
			line: `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_b",` +
				`"name":"Bash","input":{"command":"cat /tmp/probe-hold"}}]}}`,
			id:          "toolu_b",
			wantPresent: false,
		},
		{
			name: "id must match the block",
			line: `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_c",` +
				`"name":"Bash","input":{"command":"cat /tmp/probe-hold","timeout":5000}}]}}`,
			id:          "toolu_other",
			wantPresent: false,
		},
		{
			name:        "malformed line yields no projection",
			line:        `{"type":"assistant","message":`,
			id:          "toolu_a",
			wantPresent: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			input, present, value := probeToolUseInput([]byte(tc.line), tc.id)
			if present != tc.wantPresent {
				t.Fatalf("input.timeout present: got %t, want %t (input=%s)",
					present, tc.wantPresent, input)
			}
			if value != tc.wantValue {
				t.Fatalf("input.timeout value: got %q, want %q", value, tc.wantValue)
			}
		})
	}
}

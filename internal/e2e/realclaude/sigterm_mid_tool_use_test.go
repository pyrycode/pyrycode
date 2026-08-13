//go:build e2e_realclaude

package realclaude

// Regression guard for the SIGTERM-mid-tool_use cleanup contract (#422):
// when pyry receives SIGTERM while real claude has a Bash subprocess
// in flight, these production invariants must hold:
//
//  1. Full-subtree cleanup. pyry reaps the claude process it spawned AND
//     claude's in-flight Bash subprocess group — no leftover claude and no
//     orphaned `cat` after pyry exits. claude runs every Bash command in a
//     detached descendant process group two levels below pyry; on SIGTERM
//     pyry walks claude's descendant groups and SIGKILLs them (#565), so the
//     whole subtree is gone, not just the direct child.
//  2. JSONL consistency. The on-disk session JSONL ends at a complete
//     envelope boundary — no half-written trailing line that a future
//     --continue would choke on.
//  3. Bounded exit window. pyry exits within 5s of SIGTERM. A hang IS the
//     regression being guarded against.
//  4. SIGTERM landed mid-tool_use. A Bash tool_use envelope is on disk, and no
//     matching tool_result shows claude's OWN bound ending the call — no
//     background handle, no timeout-expiry record, and no bound in the call
//     params that claude has not itself overruled by recording the shutdown as
//     what interrupted the call. A result claude wrote while being torn down is
//     expected and accepted; a result claude wrote because its own clock ran out
//     is the defeat. The signals are ranked, not merely counted — the precedence
//     rule lives on classifyBashToolResult; this enumeration is its summary.
//
// Fragility history — invariant 4 is the one that keeps breaking. Twice the
// fixture lost its ability to keep a command in flight, and both times the
// root cause was the same: the open window belonged to claude.
//
//   - `sleep 30`, defeated by claude 2.1.158. The Bash tool began refusing a
//     standalone sleep ("Blocked: standalone sleep 30 ..."), so no subprocess
//     ever spawned. Fixed by swapping the command to `tail -f /dev/null`
//     (#563).
//   - `tail -f /dev/null`, defeated by claude 2.1.220. claude now attaches its
//     own timeout to a Bash call and, on expiry, backgrounds the command and
//     returns a tool_result immediately ("Command did not complete within its
//     5s timeout and was moved to the background (ID: ...)"). In the
//     2026-07-27 probe the `"timeout":5000` sat inside the tool_use PARAMS,
//     with a matching description — so THAT bound was the MODEL's choice on a
//     command whose shape advertises "blocks forever". Read narrowly: #1223
//     later established that claude also carries a client-side default bound
//     which fires the identical backgrounding with nothing in the params, so
//     "the model chose this one" is not "there is no client policy". Both
//     branches are live, and the discriminator reads a surface for each — see
//     "Where defeat #5 would land". Note the irony this header used to record:
//     it rejected
//     run_in_background because "it returns immediately, so a tool_result
//     lands and invariant 4 cannot hold". That is now claude's automatic
//     behaviour once its own timeout fires (#1219).
//
// The third entry is NOT a fixture defeat, and reading it as one is the
// mistake to avoid:
//
//   - 2026-07-28, claude 2.1.220, with the window already inverted (below).
//     claude attached NO timeout to the `cat <fifo>` call — the inversion
//     held, and the command stayed in flight until the signal. But claude
//     writes a synthetic tool_result into the session JSONL during its own
//     teardown: `is_error: true` with rejection prose, carrying the
//     envelope-level flags `interruptedByShutdown: true` and
//     `toolDenialKind: "user-rejected"` (mislabelled — no user rejected
//     anything; it is a teardown artifact). That flag is claude RECORDING the
//     very fact invariant 4 exists to prove, but it lands on the surface the
//     old "no matching tool_result" assertion forbade. So the assertion
//     flipped sign — accept an interruption artifact, reject a completion —
//     rather than the fixture changing shape a third time (#1219).
//
// The fourth entry is not claude's doing at all. It is this test failing pyry
// for doing its job, and it is the reason the discriminator reads the way it
// now does:
//
//   - 2026-07-29, eight consecutive operator runs of the shape above: 7 PASS,
//     1 FAIL. The classification log said `absent` seven times and
//     `ended-by-claude` once. `interrupted-by-shutdown` — the shape the whole
//     discriminator had been built around the day before — did not occur ONCE.
//     The failing run had staged the scenario perfectly: the rendezvous fired,
//     the write end was still held, the pre-SIGTERM snapshot found nothing on
//     disk at the instant of the signal, and invariants 1, 2 and 3 all passed.
//     claude had simply recorded an ordinary `"Exit code 1"` for the command,
//     with no timeout, no background handle and no interruption marker. The
//     likely mechanism: pyry's reaper SIGKILLed the Bash group — invariant 1
//     doing exactly its job — claude saw the child die and wrote a plain
//     failed-command result before processing its own SIGTERM. Which of those
//     two teardown steps wins is a race, and that race was the 1-in-8. Keying
//     ACCEPTANCE on the absence of a claude-internal flag therefore accused
//     pyry of the one thing it did right, so the check flipped a second time:
//     it now rejects only on POSITIVE evidence that claude's own bound ended
//     the call, and every other shape passes (#1219).
//
// Why this shape is structurally different, not a third command guess. The
// blocking artifact is created and held by the TEST; claude only waits on it:
//
//	test:   mkfifo <workdir>/sigterm-hold
//	test:   goroutine → open(fifo, O_WRONLY)  [blocks: no reader yet]
//	claude: Bash → cat <workdir>/sigterm-hold [blocks: no writer yet]
//	        ↓ both opens complete at the same instant — a rendezvous
//	test:   holds the write end, never writes → cat blocks in read() forever
//	test:   Close() (t.Cleanup only)          → cat sees EOF and exits
//
// Two properties fall out. Both are pure test-side mechanics that no
// claude-side policy change can take away:
//
//  1. The window's END is the test's. `cat` cannot complete while the test
//     holds the write end open, and the only release is holdFIFO's t.Cleanup.
//  2. The window's START is race-free. The blocking open(O_WRONLY) returns at
//     the instant `cat` starts — no 100 ms poll lag. That lag is precisely
//     what lost the race against a 5 s model-chosen timeout.
//
// NOT load-bearing: the neutral command (`cat <path>` reads as an
// instantaneous file read, not an open-ended block) and the neutral FIFO name
// (`sigterm-hold`, no `.fifo` suffix). Those only reduce the cue that might
// prompt the model to attach a defensive timeout; if claude bounds every Bash
// call regardless, they buy nothing while (1) and (2) still hold. Do NOT read
// them as "the fix is picking a command claude won't bound" — that is the
// treadmill this shape exists to end. For the same reason the prompt carries
// no "do not set a timeout" nudge: steering the model is the treadmill by
// another name.
//
// On defeat #5. If invariant 4b trips, the fixture command cannot have
// completed on its own — the test still held the write end when the assertion
// ran, because holdFIFO releases only in t.Cleanup, which by construction runs
// after the test body. Three outcomes, each settled by an artifact the run
// already produced. Check in this order:
//
//  1. Was TestHoldFIFO_RendezvousAndRelease (this file) also RED in this run?
//     Yes → a holdFIFO lifetime bug: the window mechanism itself is broken.
//     Fix that first and treat this failure as downstream noise. No → the
//     write end was held; continue.
//  2. Which signature fired? A `backgroundTaskId` or `timedOutAfterMs` on the
//     dumped tool_result is claude's client timeout expiring → defeat #5, a
//     claude-side policy change, NOT a pyry regression. An `input.timeout` or
//     `run_in_background` on the dumped tool_use, with no result-side handle,
//     is claude bounding the call at request time — and if that bound is
//     larger than the in-flight duration the message reports, it cannot have
//     fired, so the failure is a false accusation and routes back the same way.
//  3. Only if the bound is one pyry outran: pyry's SIGTERM path let the call
//     outlive claude's own bound — check invariant 3 in the same run.
//
// On a claude-side defeat the escape hatch is `needs-rework:po` on #1219 with
// the probe transcript attached, NOT a fifth command.
//
// Event-driven SIGTERM timing. The test does not guess when to signal. It
// waits for three real events: (a) the FIFO rendezvous, which fires the
// instant claude's `cat` opens the FIFO for reading; (b) the `cat` subprocess
// appears as a descendant of pyry, which is where its process group is
// captured; (c) the Bash tool_use envelope is flushed to claude's on-disk
// session file (it lags the subprocess by a couple of seconds). Only then does
// it SIGTERM, so invariant 4's "tool_use present" precondition holds
// regardless of how fast or slow a given claude version is.
//
// Subprocess detection (claude 2.1.158). claude runs every Bash command in
// its own process group two levels below pyry, so `pgrep -g <pyry-pgid>`
// cannot see it. waitForBashSubprocess walks the process tree by parent
// instead, and returns the subprocess's process group — the group invariant 1
// now asserts pyry reaped (#565), no longer merely the group the test reaps in
// cleanup. Since the rendezvous already proves the command is executing, a
// timeout in that walk now means "could not read the pgid", not "claude never
// ran the command".
//
// Terminal-shape branch: branch A is now OBSERVED, not hypothetical. #422
// picked branch B (clean stream truncation at a complete envelope boundary,
// nothing written on signal) and invariant 4 pinned it as "no matching
// tool_result". The 2026-07-28 live runs falsified that — claude does write on
// teardown, see the third fragility entry above. The flip instruction this
// paragraph used to carry was directionally right (same surface, opposite
// sign) but named a field that does not exist on this surface: it said
// `subtype != success`, and Subtype belongs to resultTrailer
// (`resultTrailer`), the stream-json result trailer, not to
// contentBlock (tool_loop_test.go:160-168), which is the tool_result surface
// here. Do not chase it.
//
// Where defeat #5 would land. The discriminator is POSITIVE evidence that
// claude's own bound ended the call, read from two surfaces (see
// classifyBashToolResult):
//
//	toolUseResult.backgroundTaskId  the command was moved to the background
//	toolUseResult.timedOutAfterMs   claude's bound expired
//	input.timeout                   claude bounded the call at request time
//	input.run_in_background         claude detached it at request time
//
// Two surfaces because neither covers both branches. The 2026-07-27 defeat was
// a bound the MODEL chose, visible only as `input.timeout` in the call params.
// #1223 then established the other branch: a client-side bound claude applies
// itself produces the identical backgrounding with NO timeout field on the
// tool_use at all — 10 live reps, absent in every one. Read precisely, because
// the margin note below depends on the distinction: #1223 exercised that
// mechanism by SETTING BASH_DEFAULT_TIMEOUT_MS to 5000, so what is established
// is the mechanism, not the shipped 120000 default firing — no run has yet
// observed that. A check reading only the params would pass that branch
// vacuously; a check reading only the sidecar would rest on fields the
// 2026-07-27 transcript never captured. Each surface covers what the other
// misses, and TestClassifyBashToolResult_ProbeEnvelopes has a row per branch.
//
// The polarity is the reverse of this check's 2026-07-28 shape, and the cost is
// real: an unknown now ACCEPTS. What that gives up does not vanish, it moves to
// the pre-SIGTERM snapshot below. A bound that expires AFTER the signal ended
// nothing — the call was still in flight when the signal landed — so every
// bounding defeat writes its tool_result BEFORE the signal, where a pure timing
// check catches it with no claude field in the loop. Different fabric, on
// purpose, and it is why the on-disk check can afford to fail open. Neither
// check is sufficient alone — the snapshot misses a result flushed just after
// the signal, and that is precisely the case the post-exit handle catches. See
// classifyBashToolResult for how the two holes fail to overlap.
//
// The known blind spot, recorded rather than closed. With
// CLAUDE_CODE_DISABLE_BACKGROUND_TASKS=1 claude kills the command on expiry
// instead of backgrounding it and writes `"Exit code 143\nCommand timed out
// after 5s"` with NO backgroundTaskId and NO timedOutAfterMs (#1223's negative
// control). That is a bounding defeat this check cannot see: only claude's
// prose separates it from the ordinary teardown abort of 2026-07-29, and prose
// is excluded. It needs an env var this test does not set, and the pre-SIGTERM
// snapshot is what would catch it — the expiry precedes the signal — subject to
// the same flush caveat as above. If it ever lands here, look there first.
//
// Also worth knowing: the client default bound is 120s, and the number to hold
// against it is rendezvous→SIGTERM, not elapsed-since-pyry-started. claude's
// clock starts when the COMMAND starts, which is the instant the rendezvous
// fires. State the frame whenever this margin is quoted — the two readings point
// opposite ways:
//
//	rendezvous → SIGTERM   50s   waitForBashSubprocess 25s + waitForSessionID
//	                             10s + waitForBashToolUseOnDisk 15s
//	pyry start → SIGTERM  100s   the above plus waitForDirectChild 25s and the
//	                             rendezvous wait itself 25s
//
// 50s against a 120s default is a 70s margin; 100s against it is 20s. The first
// is the real one, because a bound that has not started running cannot expire.
// The code already measures it that way: heldBeforeSignal starts at
// rendezvousAt, and invariant 4b's failure message compares any bound claude
// attached against exactly that quantity.
//
// 70s is margin, not comfort — every one of those three budgets is a worst case
// a healthy run spends a fraction of, but a claude release that halves the
// default, or a gate slow enough to actually spend them, puts the fixture back
// on the treadmill. That failure will read as `backgroundTaskId` plus
// `timedOutAfterMs` on the tool_result.
//
// `is_error` was rejected despite being the more durable, public wire
// vocabulary already present on contentBlock, and #1223's capture has since
// made the rejection unarguable: the backgrounding envelope carries
// `is_error: false`, while BOTH accepting shapes carry `is_error: true` (the
// teardown abort's "Exit code 1" and the interruption artifact). Reading it
// would have inverted the guard, not merely weakened it. `TestRealClaude_BashTool_NonZeroExit`
// makes the same point from the other side — it asserts is_error == true on a
// Bash tool_result for a command that RAN TO COMPLETION and exited non-zero.
// (This package already reads is_error with three different meanings across
// three surfaces.) `toolDenialKind` stays rejected as semantically mislabelled,
// exactly the kind of field a later release corrects. `interruptedByShutdown`
// survives only as an ACCEPTING signal; its absence no longer rejects, which is
// precisely the dependency 2026-07-29 falsified. claude's prose is rejected
// outright: matching a string that goes stale every release rebuilds the
// treadmill inside the failure path.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/agentrun/jsonl"
)

// sigtermSystemPrompt steers haiku toward a single Bash invocation so a
// tool_use is guaranteed in flight when the test sends SIGTERM. Mirrors
// the anti-chain wording in longSessionSystemPrompt.
const sigtermSystemPrompt = "You are an e2e regression-guard test. " +
	"When asked to run a shell command, use the Bash tool exactly once, " +
	"run the command verbatim, do NOT chain commands with && or ;, do NOT " +
	"comment, and do NOT do anything else."

// sigtermProcessName is the leaf process the fixture command spawns. The
// process-tree walk matches a descendant of pyry by this base name.
//
// Accepted risk: a stray `cat` among pyry's descendants would match. The walk
// only ever sees pyry's own subtree and the prompt runs exactly one command,
// so this is not worth defending with a full-command-line match — and matching
// the command line would find the `zsh -c` wrapper first (same pgid, but it
// exists before `cat` does).
const sigtermProcessName = "cat"

// sigtermPromptFormat forces a single Bash call running `cat <fifo>`, where
// <fifo> is a FIFO the test created and holds open for writing (see holdFIFO).
// The command stays in flight — blocked in read() — until SIGTERM lands,
// however long detection takes, because only the test can close the window.
// claude then writes a tool_result during its OWN teardown, after the signal:
// usually an ordinary failed-command result, sometimes an interruption
// artifact. Invariant 4b accepts either — what it rejects is a result claude
// wrote because its own bound expired. See the file header for why the command
// name itself is not load-bearing, and why there is deliberately no "do not set
// a timeout" nudge.
const sigtermPromptFormat = "Use the Bash tool to run `cat %s`. Do nothing else."

// syncBuffer is a goroutine-safe bytes.Buffer. os/exec writes the child's
// stdout/stderr from a copier goroutine, and this test reads them while the
// child is still running (to learn the session id and surface stderr on
// mid-run fatals). A plain bytes.Buffer would be a data race under -race.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

// Bytes returns a copy so callers never read the underlying array while the
// copier goroutine mutates it.
func (s *syncBuffer) Bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.buf.Bytes()
	out := make([]byte, len(b))
	copy(out, b)
	return out
}

// TestRealClaude_SigtermMidToolUse is the regression sensor described in
// #422. It is the only realclaude test that sends SIGTERM mid-run.
func TestRealClaude_SigtermMidToolUse(t *testing.T) {
	workdir := WithWorktreeAuthenticated(t)

	// The in-flight command blocks on this FIFO, which the test creates and
	// holds open for writing until t.Cleanup. That is the ownership inversion
	// the file header describes: the window's end is the test's, not claude's.
	// Created before prompt.txt because the prompt has to name the path.
	fifoPath := filepath.Join(workdir, "sigterm-hold")
	fifoOpened := holdFIFO(t, fifoPath)

	promptPath := filepath.Join(workdir, "prompt.txt")
	if err := os.WriteFile(promptPath, []byte(fmt.Sprintf(sigtermPromptFormat, fifoPath)), 0o600); err != nil {
		t.Fatalf("write %s: %v", promptPath, err)
	}
	systemPath := filepath.Join(workdir, "system.txt")
	if err := os.WriteFile(systemPath, []byte(sigtermSystemPrompt), 0o600); err != nil {
		t.Fatalf("write %s: %v", systemPath, err)
	}

	bin := ensurePyryBuilt(t)

	var stdout, stderr syncBuffer
	cmd := spawnPyryAgentRun(t, bin, workdir, promptPath, systemPath, &stdout, &stderr)

	// Setpgid: true above makes pyry its own process-group leader (pgid ==
	// pid). pyryPid roots the process tree the detection walk descends.
	pyryPid := cmd.Process.Pid

	// Defense-in-depth cleanup: if any assertion below trips before the
	// salvage kills run, this still reaps pyry's own group at test end.
	// ESRCH on success is harmless.
	t.Cleanup(func() {
		_ = syscall.Kill(-pyryPid, syscall.SIGKILL)
	})

	// claude is pyry's single direct child; capture its pid so invariant 1
	// can confirm pyry reaps it after SIGTERM.
	claudePid := waitForDirectChild(t, pyryPid, 25*time.Second)
	if claudePid == 0 {
		_ = syscall.Kill(-pyryPid, syscall.SIGKILL)
		t.Fatalf("pyry never spawned a claude child within 25s\nstderr:\n%s",
			truncate(stderr.Bytes()))
	}

	// Event (a): the FIFO rendezvous. holdFIFO's blocking open(O_WRONLY)
	// returns at the instant claude's `cat` opens the FIFO for reading, so
	// this fires exactly when the command starts executing — no poll lag.
	// 25s matches the budget the process-tree walk below uses for "claude got
	// as far as the Bash call".
	select {
	case <-fifoOpened:
	case <-time.After(25 * time.Second):
		_ = syscall.Kill(-pyryPid, syscall.SIGKILL)
		t.Fatalf("claude never opened the fixture FIFO %s within 25s — it did not "+
			"reach the Bash call, or it ran a different command\nstderr:\n%s",
			fifoPath, truncate(stderr.Bytes()))
	}
	// The instant the command started, which is also the instant claude's own
	// bound (if it set one) started running down. Only used by invariant 4b's
	// failure message, where the elapsed time is what tells a future reader
	// whether a bound claude attached could actually have fired before SIGTERM.
	rendezvousAt := time.Now()

	// Event (b): find the `cat` subprocess in pyry's descendants and record
	// its process group so the #565 orphan can be reaped. claude runs the
	// command in its own descendant process group, so the test walks the
	// process tree (not pyry's group) to find it. The rendezvous above
	// already proved the command is executing, so a timeout here means "could
	// not read the pgid", not "claude never ran the command".
	bashPGID, found := waitForBashSubprocess(t, pyryPid, sigtermProcessName, 25*time.Second)
	if !found {
		_ = syscall.Kill(-pyryPid, syscall.SIGKILL)
		t.Fatalf("claude opened %s (so `%s` is running) but no `%s` descendant of "+
			"pyry could be resolved to a process group within 25s\nstderr:\n%s",
			fifoPath, sigtermProcessName, sigtermProcessName, truncate(stderr.Bytes()))
	}
	// Defense-in-depth: pyry reaps this Bash subprocess group on SIGTERM
	// (#565), and invariant 1 below asserts it is gone after pyry exits. This
	// cleanup is a belt-and-suspenders safety net for the case where the
	// production reap regresses — the positive assertion runs first, so a
	// regression is caught, not masked. ESRCH (already reaped) is harmless.
	t.Cleanup(func() {
		_ = syscall.Kill(-bashPGID, syscall.SIGKILL)
	})

	// Event (c): wait until claude has flushed the Bash tool_use to its
	// on-disk session file. It lands a couple of seconds after the subprocess
	// starts; SIGTERM before the flush truncates the session file without it,
	// and invariant 4's "tool_use present" check needs it. The test still holds
	// the FIFO's write end, so `cat` cannot return ON ITS OWN, however long this
	// wait takes.
	//
	// What does NOT follow is that no tool_result can appear during this wait.
	// Holding the write end constrains the COMMAND, not claude: if claude's own
	// bound expires it writes a result and backgrounds the still-running `cat`
	// (the 2026-07-27 defeat), and on teardown it writes one regardless. That is
	// exactly why the snapshot below reads the disk instead of assuming it is
	// clean.
	sessionID := waitForSessionID(&stdout, 10*time.Second)
	if sessionID == "" {
		_ = syscall.Kill(-pyryPid, syscall.SIGKILL)
		t.Fatalf("no system/init session_id on pyry stdout within 10s\nstderr:\n%s",
			truncate(stderr.Bytes()))
	}
	if !waitForBashToolUseOnDisk(t, workdir, sessionID, 15*time.Second) {
		_ = syscall.Kill(-pyryPid, syscall.SIGKILL)
		t.Fatalf("claude never flushed the Bash tool_use to the session file within 15s "+
			"(session_id=%s); cannot assert SIGTERM landed mid-tool_use\nstderr:\n%s",
			sessionID, truncate(stderr.Bytes()))
	}

	// Pre-signal snapshot: belt-and-suspenders for invariant 4, with different
	// fabric. The post-exit check below reads a claude-internal field; this one
	// reads a pure TIMING fact and depends on no claude field at all. ANY
	// matching tool_result already on disk here means claude ended the Bash
	// call before the test signalled, so the scenario was never staged — and
	// it cannot be a pyry regression, because pyry has not been signalled yet.
	//
	// Not sufficient alone (a tool_result written just before the signal but
	// flushed just after is missed), which is why the post-exit check stays
	// primary. Its second contribution is diagnostic: it fails at the moment of
	// truth instead of surfacing 40s later as an ambiguous 4b.
	//
	// A miss from findBashToolUse SKIPS rather than fails: waitForBashToolUseOnDisk
	// just returned true, so an empty read here is transient, and invariant 4a
	// below catches a genuine absence.
	preEvents := ReadJSONL(t, workdir, sessionID)
	if preID, preIdx := findBashToolUse(preEvents); preID != "" {
		if kind, entry := classifyBashToolResult(preEvents, preID, preIdx); kind != toolResultAbsent {
			_ = syscall.Kill(-pyryPid, syscall.SIGKILL)
			t.Fatalf("a matching tool_result (%s) for Bash tool_use_id=%s was already on "+
				"disk when SIGTERM was about to be sent — claude ended the Bash call "+
				"before the test signalled, so the mid-tool_use scenario was never "+
				"staged. This is a fixture defeat by definition: pyry has not been "+
				"signalled yet, so it cannot be a production regression. See this "+
				"file's fragility history.\n\ntool_use:\n%s\n\ntool_result:\n%s",
				kind, preID, truncate(preEvents[preIdx].Raw), truncate(entry.Raw))
		}
	}

	heldBeforeSignal := time.Since(rendezvousAt)
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM to pyry (pid=%d): %v", pyryPid, err)
	}

	// Bounded wait: the 5s budget is the production teardown contract.
	// Pyry's teardown is structurally bounded: SIGTERM → ctx cancel →
	// runner forwards SIGTERM to claude → cmd.Wait returns within the kill
	// grace (SIGKILL fallback via WaitDelay). Deadline expiry IS the
	// regression being guarded against — a hang at this point is not a flake.
	waitDone := make(chan error, 1)
	go func() {
		waitDone <- cmd.Wait()
	}()

	select {
	case <-waitDone:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		// Drain Wait so the buffer reads below are happens-after the
		// stdlib's internal forwarder writes; bounded so a wedged child
		// does not hang the suite.
		select {
		case <-waitDone:
		case <-time.After(2 * time.Second):
		}
		t.Fatalf("pyry did not exit within 5s of SIGTERM — hang regression "+
			"(teardown kill-grace contract)\nstderr:\n%s", truncate(stderr.Bytes()))
	}

	// Invariant 1: pyry reaps the full claude subtree — the direct child AND
	// claude's detached Bash subprocess group (#565). On SIGTERM pyry walks
	// claude's descendant process groups and SIGKILLs them, then SIGTERMs
	// claude, so neither the claude process nor the `cat <fifo>` group is
	// alive once pyry has exited.
	if !waitForProcessGone(claudePid, 2*time.Second) {
		t.Fatalf("claude (pid=%d, pyry's direct child) still alive after pyry exit — "+
			"pyry did not reap the process it spawned\nstderr:\n%s",
			claudePid, truncate(stderr.Bytes()))
	}
	if !waitForGroupGone(bashPGID, 2*time.Second) {
		t.Fatalf("claude's Bash subprocess group (pgid=%d) still alive after pyry exit — "+
			"pyry did not reap claude's detached descendant process group (#565)\nstderr:\n%s",
			bashPGID, truncate(stderr.Bytes()))
	}

	jsonlPath := jsonlPathFor(workdir, sessionID)
	jsonlBytes, err := os.ReadFile(jsonlPath)
	if err != nil {
		t.Fatalf("read %s: %v", jsonlPath, err)
	}

	// Invariant 2: no half-written line. ReadJSONL silently retains trailing
	// partial bytes (see internal/agentrun/jsonl/reader.go:188-262), so the
	// only way to surface a half-written tail is an explicit byte-tail check.
	if len(jsonlBytes) == 0 {
		t.Fatalf("jsonl %s is empty (claude wrote no events before SIGTERM)", jsonlPath)
	}
	if jsonlBytes[len(jsonlBytes)-1] != '\n' {
		lastNL := bytes.LastIndexByte(jsonlBytes, '\n')
		trailing := len(jsonlBytes) - lastNL - 1
		t.Fatalf("jsonl %s does not end with a newline — half-written "+
			"trailing line of %d bytes (last newline at index %d, file size %d)",
			jsonlPath, trailing, lastNL, len(jsonlBytes))
	}

	events := ReadJSONL(t, workdir, sessionID)

	// Invariant 4a: Bash tool_use present. We waited for it on disk above, so
	// a miss here means claude rewrote or truncated the session file during
	// teardown — not the expected branch-B shape.
	bashToolUseID, bashIdx := findBashToolUse(events)
	if bashToolUseID == "" {
		t.Fatalf("the Bash tool_use was flushed to %s before SIGTERM but is absent "+
			"after pyry exit — the session file was rewritten or truncated during "+
			"teardown", jsonlPath)
	}

	// Invariant 4b: no matching tool_result shows claude's OWN bound ending the
	// call. Only toolResultBounded fails. The other three accept: `absent`
	// (claude torn down before it wrote anything), `interrupted-by-shutdown`
	// (claude recording the signal landing mid-call) and `unbounded` (an
	// ordinary failed-command result, which is what pyry's reaper killing the
	// Bash group looks like from claude's side — the 2026-07-29 shape).
	//
	// The discrimination in the message below is STRUCTURAL, not textual. It
	// deliberately does not branch on claude's prose ("moved to the
	// background") — matching on a string that goes stale every release would
	// rebuild the treadmill inside the failure path. The structural fact is
	// permanent: the fixture command blocks on a FIFO this test created and
	// still holds open (holdFIFO closes the write end only in t.Cleanup, which
	// by construction runs after this assertion), so the command CANNOT have
	// completed on its own.
	kind, entry := classifyBashToolResult(events, bashToolUseID, bashIdx)
	// Recorded on every run, pass or fail: which accepting shape a given claude
	// version produced. This line is how the 2026-07-29 defect was caught —
	// eight operator runs logged `absent` seven times and never once logged
	// `interrupted-by-shutdown`, which is what exposed a discriminator resting
	// on a field that occurred in none of them. If the accept branch drifts
	// again, this line in the -v log is what dates the change.
	t.Logf("invariant 4b: matching tool_result for Bash tool_use_id=%s classified as %q",
		bashToolUseID, kind)
	if kind == toolResultBounded {
		t.Fatalf("matching tool_result on disk for Bash tool_use_id=%s in %s carries "+
			"positive evidence that claude's OWN bound ended the Bash call — a "+
			"backgroundTaskId or timedOutAfterMs on the toolUseResult sidecar, or a "+
			"timeout/run_in_background in the tool_use params. The call ended on "+
			"claude's clock rather than on the signal, so the mid-tool_use scenario "+
			"was never staged.\n\n"+
			"The fixture command blocks on the FIFO %s, which this test created "+
			"and still held open for writing when this assertion ran (holdFIFO "+
			"releases only in t.Cleanup, structurally after the test body), so it "+
			"cannot have completed on its own. It was in flight for %s before "+
			"SIGTERM. Check, in this order:\n"+
			"  1. Did TestHoldFIFO_RendezvousAndRelease (this file) also fail in "+
			"this run? If yes, this is a holdFIFO lifetime bug — the window "+
			"mechanism is broken; fix that first and treat this failure as "+
			"downstream noise.\n"+
			"  2. Which signature fired? A `backgroundTaskId` or `timedOutAfterMs` "+
			"on the tool_result envelope below is claude's client timeout expiring "+
			"(#1223: the default is BASH_DEFAULT_TIMEOUT_MS, 120000 unless set) — "+
			"fixture defeat #5, a claude-side policy change, NOT a pyry regression.\n"+
			"     An `input.timeout`/`run_in_background` on the tool_use envelope "+
			"with no result-side handle means claude bounded the call at request "+
			"time. If that bound is LARGER than the %s above it cannot have fired, "+
			"and this failure is a false accusation — route it to `needs-rework:po` "+
			"as well, with this message attached.\n"+
			"  3. Only if the bound is one pyry outran: pyry's SIGTERM path let the "+
			"call outlive claude's own bound — check invariant 3 (exit within 5s of "+
			"SIGTERM) in this same run.\n"+
			"See this file's header for the fragility history. On a claude-side "+
			"defeat the escape hatch is `needs-rework:po` on #1219 with the probe "+
			"transcript attached, not a fifth command guess.\n\n"+
			"tool_use:\n%s\n\ntool_result:\n%s",
			bashToolUseID, jsonlPath, fifoPath, heldBeforeSignal, heldBeforeSignal,
			truncate(events[bashIdx].Raw), truncate(entry.Raw))
	}
}

// toolResultKind classifies the matching Bash tool_result for invariant 4.
//
// Only toolResultBounded fails the invariant, and it fires only on POSITIVE
// evidence that claude's own bound ended the Bash call. Every other shape
// accepts. See classifyBashToolResult for why the polarity runs this way and
// what pays for the rejecting power it gives up.
type toolResultKind int

const (
	// toolResultAbsent — no matching tool_result on disk: claude was torn
	// down before it wrote one. ACCEPT.
	toolResultAbsent toolResultKind = iota
	// toolResultInterrupted — matching tool_result carrying claude's
	// envelope-level interruptedByShutdown flag: claude recording that the
	// signal landed mid-call. The 2026-07-28 shape. ACCEPT.
	toolResultInterrupted
	// toolResultUnbounded — matching tool_result with no bound in evidence and
	// no interruption marker: an ordinary command result for a command that
	// died under claude. The 2026-07-29 shape ("Exit code 1"), which is what
	// pyry's own reaper SIGKILLing the Bash group — invariant 1 doing exactly
	// its job — looks like from claude's side. ACCEPT.
	toolResultUnbounded
	// toolResultBounded — matching tool_result carrying positive evidence that
	// claude's own bound ended the call: a backgroundTaskId or timedOutAfterMs
	// on the toolUseResult sidecar, or a timeout / run_in_background in the
	// tool_use params. The call ended on claude's clock, so the signal did not
	// land mid-call and the scenario was never staged. REJECT.
	toolResultBounded
)

func (k toolResultKind) String() string {
	switch k {
	case toolResultAbsent:
		return "absent"
	case toolResultInterrupted:
		return "interrupted-by-shutdown"
	case toolResultUnbounded:
		return "unbounded"
	case toolResultBounded:
		return "bounded-by-claude"
	default:
		return "unknown"
	}
}

// classifyBashToolResult scans events after index from for a tool_result whose
// tool_use_id is toolUseID, and classifies it. from is the index of the Bash
// tool_use envelope, as returned by findBashToolUse; a negative from (no
// tool_use) yields toolResultAbsent.
//
// The second return is the matching entry — zero-valued when absent — so
// callers can quote it verbatim in a failure message.
//
// POLARITY, and why it is the opposite of this check's first shape. The
// 2026-07-28 version accepted only on a positive interruptedByShutdown marker
// and rejected every unknown. Eight live operator runs falsified that: the
// marker appeared in NONE of them, and the one run that recorded an ordinary
// "Exit code 1" was failed for it — after staging the scenario correctly. So
// acceptance no longer keys on the absence of a claude-internal flag; rejection
// keys on the presence of claude's own bound. See the 2026-07-29 fragility
// entry in the file header.
//
// The rejecting power that flip gives up does not vanish, it MOVES. A bound
// that expires after SIGTERM did not end anything — the call was still in
// flight when the signal landed — so every bounding defeat writes its
// tool_result BEFORE the signal, where the live test's pre-SIGTERM snapshot
// catches it on a pure timing fact with no claude field in the loop. Different
// fabric, on purpose.
//
// The snapshot does not carry that alone, and claiming it does would overstate
// the cover: a result written just before the signal but FLUSHED just after is
// on neither side of the read. What closes it is that the two mechanisms miss
// different things — a backgrounding defeat always carries a handle, so the
// post-exit check catches exactly the case whose late flush defeats the
// snapshot. Neither is sufficient; the pair is.
//
// Evidence precedence, asymmetric on purpose:
//
//  1. A background handle or timeout-expiry record on the RESULT outranks
//     everything: it is claude recording what actually ended the call.
//  2. An interruption marker outranks a bound on the TOOL_USE. The tool_use
//     records only claude's intent at request time and says nothing about
//     whether the bound ever fired, so a generous bound plus an explicit "the
//     shutdown interrupted this" must not be read as a defeat.
//
// First match wins among lines, deliberately: if claude ever wrote two results
// for one call, the first is the one that dates when the call ended.
func classifyBashToolResult(events []JSONLEntry, toolUseID string, from int) (toolResultKind, JSONLEntry) {
	if from < 0 || from+1 >= len(events) {
		return toolResultAbsent, JSONLEntry{}
	}
	// Read claude's call params once: findBashToolUse resolved toolUseID out
	// of events[from], so that is the envelope carrying them.
	boundedAtCall := boundedAtToolUse(events[from].Raw, toolUseID)
	for _, e := range events[from+1:] {
		if e.Kind != "user" {
			continue
		}
		blocks, err := parseContentBlocks(e.Raw)
		if err != nil {
			continue
		}
		for _, b := range blocks {
			if b.Type != "tool_result" || b.ToolUseID != toolUseID {
				continue
			}
			switch {
			case boundedAtToolResult(e.Raw):
				return toolResultBounded, e
			case interruptedByShutdown(e.Raw):
				return toolResultInterrupted, e
			case boundedAtCall:
				return toolResultBounded, e
			default:
				return toolResultUnbounded, e
			}
		}
	}
	return toolResultAbsent, JSONLEntry{}
}

// jsonFieldPresent reports whether a raw field was present in the source JSON
// with a value other than null. Used for the bounding signals, where PRESENCE
// is the evidence and the value is only diagnostic — and where reading a
// `null` as present would manufacture a failure.
func jsonFieldPresent(raw json.RawMessage) bool {
	return len(raw) > 0 && !bytes.Equal(raw, []byte("null"))
}

// boundedAtToolUse reports whether claude's own params on the Bash tool_use
// with id toolUseID show it bounding or detaching the call: an `input.timeout`
// bound, or `input.run_in_background`. Either says the call was never going to
// stay open until the signal on claude's side.
//
// This is the MODEL-set branch, and it is the only branch the 2026-07-27
// transcript captured — `"timeout":5000` in the params of a `tail -f /dev/null`
// call. #1223 established the other branch: claude's client default
// (BASH_DEFAULT_TIMEOUT_MS) backgrounds the command with no timeout field on
// the tool_use at all, in 10 live reps out of 10. Hence two surfaces.
//
// Matched by id rather than "the first tool_use block": an assistant line can
// carry several blocks, and reading another call's params would classify this
// call on someone else's evidence.
//
// contentBlock has no Input field and this decodes into a local struct rather
// than adding one: contentBlock is shared across the package.
func boundedAtToolUse(raw json.RawMessage, toolUseID string) bool {
	var envelope struct {
		Message struct {
			Content []struct {
				Type  string `json:"type"`
				ID    string `json:"id"`
				Input struct {
					Timeout         json.RawMessage `json:"timeout"`
					RunInBackground bool            `json:"run_in_background"`
				} `json:"input"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return false
	}
	for _, b := range envelope.Message.Content {
		if b.Type != "tool_use" || b.ID != toolUseID {
			continue
		}
		return jsonFieldPresent(b.Input.Timeout) || b.Input.RunInBackground
	}
	return false
}

// boundedAtToolResult reports whether the tool_result LINE carries claude's
// toolUseResult sidecar recording that its own clock ended the call: a
// `backgroundTaskId` (the command was moved to the background) or a
// `timedOutAfterMs` (the bound expired). Both are verbatim from #1223's live
// capture of this exact shape on claude 2.1.220.
//
// This outranks every other signal because it is claude recording what actually
// ended the call, not what it intended at request time.
//
// `toolUseResult` is a TOP-LEVEL field of the line, a sibling of `type` and
// `message`, and it is not always an object: on the 2026-07-29 teardown-abort
// shape it is the string "Error: Exit code 1". A non-object fails the second
// decode and reports no signature, which is the correct answer for that shape.
//
// Every capture on record writes one tool_result per user line, so the line's
// sidecar belongs to the block the caller matched.
func boundedAtToolResult(raw json.RawMessage) bool {
	var envelope struct {
		ToolUseResult json.RawMessage `json:"toolUseResult"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return false
	}
	var sidecar struct {
		BackgroundTaskID json.RawMessage `json:"backgroundTaskId"`
		TimedOutAfterMs  json.RawMessage `json:"timedOutAfterMs"`
	}
	if err := json.Unmarshal(envelope.ToolUseResult, &sidecar); err != nil {
		return false
	}
	return jsonFieldPresent(sidecar.BackgroundTaskID) || jsonFieldPresent(sidecar.TimedOutAfterMs)
}

// interruptedByShutdown reports whether a JSONL line carries claude's
// envelope-level `interruptedByShutdown` flag set to true.
//
// The flag is a TOP-LEVEL field of the line, a sibling of `type` and
// `message` — not a field of the content block. It is decoded into a local
// anonymous struct on purpose: contentBlock is shared across this package, and
// this flag does not belong on it.
//
// An absent field decodes to Go's zero value (false) and a malformed line
// returns false. Since 2026-07-29 that is no longer load-bearing in either
// direction: the flag is read ONLY as a corroborating accept signal, never as
// the thing whose absence rejects. Its absence was the whole 2026-07-29 defect
// — it appeared in zero of eight live runs, so rejecting on it failed a
// correctly staged scenario. A rename or removal now costs nothing but the
// classification label in the -v log.
func interruptedByShutdown(raw json.RawMessage) bool {
	var envelope struct {
		InterruptedByShutdown bool `json:"interruptedByShutdown"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return false
	}
	return envelope.InterruptedByShutdown
}

// Probe envelopes for TestClassifyBashToolResult_ProbeEnvelopes.
//
// PROVENANCE — read before editing. These are the shapes the discriminator has
// to separate, taken from the live transcripts on #1219 (2026-07-27, -28, -29)
// and on #1223, whose probe ran 10 reps against the same claude 2.1.220 and the
// same FIFO-held `cat`. Every reconstruction is called out per line; do not
// mistake a reconstruction for a capture, and do not "tidy" a verbatim string.
const (
	// (A) 2026-07-27, claude 2.1.220 backgrounding `tail -f /dev/null` on a
	// bound the MODEL chose.
	// VERBATIM: the command, and `"timeout":5000` sitting inside the tool_use
	// PARAMS (that placement is the evidence the bound was the MODEL's choice).
	// RECONSTRUCTED: the tool_use id, and the description, which the transcript
	// elided to "…with 5 second timeout".
	probeToolUseBackgrounded = `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_01ReconstructedProbeA","name":"Bash","input":{"command":"tail -f /dev/null","description":"Run tail -f /dev/null with 5 second timeout","timeout":5000}}]}}`

	// (A)'s result. VERBATIM: the prose. RECONSTRUCTED: the background ID and
	// log path (the transcript elided both), the tool_use_id, the `user` line
	// wrapper — and `"is_error":true`, which the transcript did NOT retain.
	// That value was AC2's worst-case rule rather than an observation.
	//
	// #1223 has since captured the real value on this shape: `is_error:false`
	// (see probeToolResultBackgroundedDefault). Both accepting shapes below
	// carry `is_error:true`, so reading that flag would have INVERTED the
	// guard, not merely weakened it. The two rows are kept because they still
	// pin the verdict as invariant to it — nothing reads is_error now.
	probeToolResultBackgroundedIsError = `{"type":"user","message":{"content":[{"type":"tool_result","content":"Command did not complete within its 5s timeout and was moved to the background (ID: bash_1). Output is being written to: /tmp/claude-bash-1.log","is_error":true,"tool_use_id":"toolu_01ReconstructedProbeA"}]}}`

	// (A)'s result with `is_error` absent — the other side of the worst-case
	// rule. Same line, that one key removed.
	probeToolResultBackgroundedNoIsError = `{"type":"user","message":{"content":[{"type":"tool_result","content":"Command did not complete within its 5s timeout and was moved to the background (ID: bash_1). Output is being written to: /tmp/claude-bash-1.log","tool_use_id":"toolu_01ReconstructedProbeA"}]}}`

	// (A') 2026-07-28, from #1223's probe: the SAME backgrounding, driven by
	// claude's own default bound instead of a model-chosen one. This is the
	// branch a params-only check cannot see — `input.timeout` is absent here
	// and was absent in all 10 of that probe's reps.
	//
	// VERBATIM apart from the id: the tool_use is #1223's captured Bash block
	// with its id replaced by the one the tool_result below references. The two
	// envelopes were published from different reps of the same row, so pairing
	// them requires that substitution; nothing else was touched, including the
	// long temp path.
	probeToolUseBackgroundedDefault = `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_01RBzRv2Q5Y4cwWEkpX9patp","name":"Bash","input":{"command":"cat /var/folders/k0/gc07w9ws319b07n0plnw6y8r0000gn/T/TestRealClaude_BackgroundHandleTriggerdefault-onlyrep12710027791/001/probe-hold"},"caller":{"type":"direct"}}]}}`

	// (A')'s result, VERBATIM AND COMPLETE — the whole JSONL line as #1223
	// published it, nothing elided. This is the capture the discriminator's
	// result-side surface is built on: `toolUseResult.backgroundTaskId` and
	// `toolUseResult.timedOutAfterMs`, alongside `is_error:false`.
	probeToolResultBackgroundedDefault = `{"parentUuid":"763cbff1-73f9-479e-acdf-d52c172c2882","isSidechain":false,"promptId":"36927ebc-f73f-455f-ac0b-a84efd55a2aa","type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_01RBzRv2Q5Y4cwWEkpX9patp","type":"tool_result","content":"Command did not complete within its 5s timeout and was moved to the background (ID: bjh1j6tle). Output is being written to: /private/tmp/claude-501/-private-var-folders-k0-gc07w9ws319b07n0plnw6y8r0000gn-T-TestRealClaude-BackgroundHandleTriggerdefault-onlyrep12436024847-001/495d590d-6660-4ad3-94d7-a8bfc6aa0731/tasks/bjh1j6tle.output. You will be notified when it completes. To check interim output, use Read on that file path.","is_error":false}]},"uuid":"7f875fcc-69f8-4721-96c5-6e8ba37d08c0","timestamp":"2026-07-28T20:46:49.520Z","toolUseResult":{"stdout":"","stderr":"","interrupted":false,"isImage":false,"noOutputExpected":false,"backgroundTaskId":"bjh1j6tle","timedOutAfterMs":5000},"sourceToolAssistantUUID":"763cbff1-73f9-479e-acdf-d52c172c2882","userType":"external","entrypoint":"developer","cwd":"/private/var/folders/k0/gc07w9ws319b07n0plnw6y8r0000gn/T/TestRealClaude_BackgroundHandleTriggerdefault-onlyrep12436024847/001","sessionId":"495d590d-6660-4ad3-94d7-a8bfc6aa0731","version":"2.1.220","gitBranch":"HEAD"}`

	// (A'') the model asking for the background directly, also from #1223.
	// ASSEMBLED, not a single captured line: #1223 published this path as a
	// field-by-field comparison rather than a raw envelope. VERBATIM from that
	// table: `run_in_background:true` in the params, the result prose,
	// `is_error:false`, `backgroundTaskId` PRESENT and `timedOutAfterMs`
	// ABSENT — the one field that separates this path from expiry.
	// RECONSTRUCTED: the ids, the paths, and the `user` line wrapper.
	//
	// It is here because it is the shape where the params surface and the
	// result surface each catch something the other would miss.
	probeToolUseRunInBackground = `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_01ReconstructedRunInBg","name":"Bash","input":{"command":"cat /tmp/pyry-probe/probe-hold","run_in_background":true}}]}}`

	probeToolResultRunInBackground = `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"Command running in background with ID: b83vgi4zc. Output is being written to: /private/tmp/claude-501/probe/tasks/b83vgi4zc.output","is_error":false,"tool_use_id":"toolu_01ReconstructedRunInBg"}]},"toolUseResult":{"stdout":"","stderr":"","interrupted":false,"isImage":false,"noOutputExpected":false,"backgroundTaskId":"b83vgi4zc"},"version":"2.1.220"}`

	// (B) 2026-07-28, the correctly-staged run. VERBATIM: the command shape
	// (`cat <fifo>`) and the ABSENCE of any timeout field — the observation
	// that falsified the escape hatch's trigger. RECONSTRUCTED: the workdir
	// path and the tool_use id, which is set to the one the transcript
	// retained for the matching tool_result below.
	probeToolUseInterrupted = `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_01WHRACfWECW8GRys7Wy1szz","name":"Bash","input":{"command":"cat /tmp/pyry-e2e/sigterm-hold"}}]}}`

	// (B)'s result. VERBATIM: the content prose, `is_error`, `tool_use_id`, and
	// the three envelope-level fields `toolDenialKind`, `interruptedByShutdown`
	// and `version`. RECONSTRUCTED: only the `user` line wrapper — the
	// transcript showed the content block and the envelope fields separately.
	//
	// Note `is_error:true` here AND on (A) above: that is why `is_error` cannot
	// be the discriminator. See the file header.
	probeToolResultInterrupted = `{"type":"user","message":{"content":[{"type":"tool_result","content":"The user doesn't want to proceed with this tool use. The tool use was rejected...","is_error":true,"tool_use_id":"toolu_01WHRACfWECW8GRys7Wy1szz"}]},"toolDenialKind":"user-rejected","interruptedByShutdown":true,"version":"2.1.220"}`

	// (B)'s envelope with the interruption flag written explicitly false.
	// Synthetic. Since 2026-07-29 this ACCEPTS, where it used to reject: with
	// no bound in evidence, an explicit `false` says no more than an absent
	// flag does. The row is kept precisely because it pins that reversal.
	probeToolResultInterruptedFalse = `{"type":"user","message":{"content":[{"type":"tool_result","content":"The user doesn't want to proceed with this tool use. The tool use was rejected...","is_error":true,"tool_use_id":"toolu_01WHRACfWECW8GRys7Wy1szz"}]},"toolDenialKind":"user-rejected","interruptedByShutdown":false,"version":"2.1.220"}`

	// An interruption marker for an UNRELATED tool call. Synthetic — stops a
	// stray marker elsewhere in the session from laundering a defeat.
	probeToolResultInterruptedOtherID = `{"type":"user","message":{"content":[{"type":"tool_result","content":"The user doesn't want to proceed with this tool use. The tool use was rejected...","is_error":true,"tool_use_id":"toolu_01SomeOtherToolCall"}]},"toolDenialKind":"user-rejected","interruptedByShutdown":true,"version":"2.1.220"}`

	// (C) 2026-07-29, the run that failed the previous discriminator while
	// having staged the scenario correctly. This is the regression this rework
	// exists to fix, and it must ACCEPT.
	//
	// The tool_use is VERBATIM from the #1219 transcript except for the
	// assistant line wrapper and the workdir path, which the issue elided to
	// "…/sigterm-hold". Note what is ABSENT: no `input.timeout`.
	probeToolUseTeardownAbort = `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_01Q3x5zbCFNRFb177vkr9iJi","name":"Bash","input":{"command":"cat /tmp/pyry-e2e/sigterm-hold"}}]}}`

	// (C)'s result, VERBATIM AND COMPLETE. claude recording an ordinary failed
	// command — no background handle, no timeout-expiry record, no interruption
	// marker. The likely mechanism is pyry's own reaper SIGKILLing the Bash
	// group, which claude saw as the child dying under it.
	//
	// Note `toolUseResult` here is a STRING, not the object it is on the
	// backgrounding shapes. boundedAtToolResult must survive that.
	probeToolResultTeardownAbort = `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"Exit code 1","is_error":true,"tool_use_id":"toolu_01Q3x5zbCFNRFb177vkr9iJi"}]},"toolUseResult":"Error: Exit code 1","timestamp":"2026-07-28T21:23:16.688Z","version":"2.1.220"}`

	// An interruption marker carried on a line that ALSO has a background
	// handle. Synthetic — pins the first half of the precedence rule: what
	// claude records as having ended the call outranks the marker.
	probeToolResultInterruptedWithHandle = `{"type":"user","message":{"content":[{"type":"tool_result","content":"The user doesn't want to proceed with this tool use. The tool use was rejected...","is_error":true,"tool_use_id":"toolu_01WHRACfWECW8GRys7Wy1szz"}]},"toolUseResult":{"backgroundTaskId":"bjh1j6tle","timedOutAfterMs":5000},"toolDenialKind":"user-rejected","interruptedByShutdown":true,"version":"2.1.220"}`

	// A bound too generous to have fired within this test's worst-case 50s from
	// rendezvous to SIGTERM — the frame that matters, since claude's clock starts
	// when the command starts; see the file header's margin note. (600000 is the
	// documented BASH_MAX_TIMEOUT_MS ceiling.) Synthetic
	// — pins the second half of the precedence rule: a params-side bound must
	// not override claude's own statement that the shutdown interrupted the
	// call. Reading it the other way round is how a correctly staged run gets
	// failed, which is the 2026-07-29 defect in a different costume.
	probeToolUseGenerousBound = `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_01WHRACfWECW8GRys7Wy1szz","name":"Bash","input":{"command":"cat /tmp/pyry-e2e/sigterm-hold","timeout":600000}}]}}`
)

// TestClassifyBashToolResult_ProbeEnvelopes pins invariant 4b's discriminator
// against every live-observed probe shape without claude and without
// credentials, so it runs on every `make e2e-realclaude` regardless of auth:
// the backgrounding envelopes must be REJECTED (toolResultBounded), and the
// shutdown-interruption and teardown-abort envelopes ACCEPTED.
//
// The teardown-abort row is the 2026-07-29 regression: the run that this
// check's previous shape failed while the scenario had staged correctly. It is
// pinned here credential-free precisely because eight live runs were not enough
// to surface it — one in eight is a bad detector.
//
// Each case is built as a JSONL string and pushed through the real parser
// rather than hand-built as entries. That is the point, not ceremony:
// jsonl.Event.Kind is DERIVED from the line's `type` field, so a hand-set Kind
// would let this fixture agree with a live path that skips the line. It also
// exercises the same two entry points the live test uses — findBashToolUse
// then classifyBashToolResult.
func TestClassifyBashToolResult_ProbeEnvelopes(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		want  toolResultKind
	}{
		{
			// (A) 2026-07-27: rejected on the params surface, the only
			// surface this transcript captured.
			name:  "background_on_model_bound_is_error_true",
			lines: []string{probeToolUseBackgrounded, probeToolResultBackgroundedIsError},
			want:  toolResultBounded,
		},
		{
			// Same verdict as the row above with the flag absent: the
			// rejection of (A) is INVARIANT to `is_error`, so no re-probe of
			// the 2026-07-27 transcript can change it. #1223 has since shown
			// the true value is `false` — see the const's comment.
			name:  "background_on_model_bound_is_error_absent",
			lines: []string{probeToolUseBackgrounded, probeToolResultBackgroundedNoIsError},
			want:  toolResultBounded,
		},
		{
			// (A') the branch a params-only check cannot see: no
			// `input.timeout` anywhere, rejected on the result sidecar alone.
			// This row is why the discriminator reads two surfaces.
			name:  "background_on_client_default_no_params_timeout",
			lines: []string{probeToolUseBackgroundedDefault, probeToolResultBackgroundedDefault},
			want:  toolResultBounded,
		},
		{
			// (A'') `timedOutAfterMs` is absent on this path, so the handle is
			// what rejects it — the mirror of the row above.
			name:  "background_on_model_request_no_timeout_expiry",
			lines: []string{probeToolUseRunInBackground, probeToolResultRunInBackground},
			want:  toolResultBounded,
		},
		{
			name:  "shutdown_interruption",
			lines: []string{probeToolUseInterrupted, probeToolResultInterrupted},
			want:  toolResultInterrupted,
		},
		{
			// (C) the 2026-07-29 regression row. An ordinary non-zero exit
			// with no bound in evidence must ACCEPT: it is what pyry's own
			// reaper looks like from claude's side. Also the row that proves
			// a STRING `toolUseResult` does not derail the sidecar decode.
			name:  "teardown_abort_nonzero_exit",
			lines: []string{probeToolUseTeardownAbort, probeToolResultTeardownAbort},
			want:  toolResultUnbounded,
		},
		{
			name:  "tool_use_only",
			lines: []string{probeToolUseInterrupted},
			want:  toolResultAbsent,
		},
		{
			name:  "interruption_marker_for_a_different_tool_use",
			lines: []string{probeToolUseInterrupted, probeToolResultInterruptedOtherID},
			want:  toolResultAbsent,
		},
		{
			// Accepts since 2026-07-29, where it used to reject. With no bound
			// in evidence an explicit `false` says no more than an absent flag.
			name:  "interruption_flag_explicitly_false",
			lines: []string{probeToolUseInterrupted, probeToolResultInterruptedFalse},
			want:  toolResultUnbounded,
		},
		{
			// Precedence, half one: what claude records as having ended the
			// call outranks an interruption marker on the same line.
			name:  "background_handle_outranks_interruption_marker",
			lines: []string{probeToolUseInterrupted, probeToolResultInterruptedWithHandle},
			want:  toolResultBounded,
		},
		{
			// Precedence, half two: a params-side bound too generous to have
			// fired must NOT override claude's own interruption marker.
			// Reading this row the other way is the 2026-07-29 defect again.
			name:  "interruption_marker_outranks_a_generous_bound",
			lines: []string{probeToolUseGenerousBound, probeToolResultInterrupted},
			want:  toolResultInterrupted,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			events := parseJSONLFixture(t, tc.lines)
			if len(events) != len(tc.lines) {
				t.Fatalf("parsed %d events from %d fixture lines — a fixture line is "+
					"malformed JSON and the reader skipped it", len(events), len(tc.lines))
			}

			toolUseID, idx := findBashToolUse(events)
			if toolUseID == "" {
				t.Fatalf("findBashToolUse found no Bash tool_use in the fixture — the "+
					"fixture is wrong, not the classifier\n%s", strings.Join(tc.lines, "\n"))
			}

			got, entry := classifyBashToolResult(events, toolUseID, idx)
			if got != tc.want {
				t.Fatalf("classifyBashToolResult(tool_use_id=%s) = %s, want %s\nmatched entry:\n%s",
					toolUseID, got, tc.want, truncate(entry.Raw))
			}
			if got == toolResultAbsent && len(entry.Raw) != 0 {
				t.Errorf("toolResultAbsent must come with a zero-valued entry, got:\n%s",
					truncate(entry.Raw))
			}
			if got != toolResultAbsent && len(entry.Raw) == 0 {
				t.Errorf("%s must come with the matching entry so the failure message can "+
					"quote it verbatim, got a zero value", got)
			}
		})
	}
}

// parseJSONLFixture pushes fixture lines through the same parser ReadJSONL
// uses, so Event.Kind is derived from each line's `type` field rather than
// supplied by the test. The trailing newline is required: the reader retains a
// line without one as pending partial bytes and never surfaces it.
func parseJSONLFixture(t *testing.T, lines []string) []JSONLEntry {
	t.Helper()
	src := strings.Join(lines, "\n") + "\n"
	r := jsonl.NewReader(strings.NewReader(src), jsonl.Config{})
	var events []JSONLEntry
	for {
		ev, err := r.Next()
		if errors.Is(err, io.EOF) {
			return events
		}
		if err != nil {
			t.Fatalf("parseJSONLFixture: %v", err)
		}
		events = append(events, ev)
	}
}

// holdFIFO creates a FIFO at path and blocks a goroutine on open(O_WRONLY).
// The returned channel is closed exactly once, at the instant a reader opens
// the FIFO — i.e. the instant claude's `cat` starts executing. Until the write
// end is closed, that reader stays blocked in read(): this is the open window
// the test owns, per the file header.
//
// HAZARD — why the caller never gets the *os.File. Closing the write end makes
// `cat` exit on its own, so a release before invariant 1 has been asserted
// would make "pyry reaped the Bash process group" pass VACUOUSLY. The only
// release lives in the t.Cleanup registered here, which by construction runs
// after the test body — the assertion order is safe by structure, not by
// discipline. Do not add a caller-facing release().
//
// Per #422's precedent (reaffirmed in #1219's Technical Notes), this helper is
// file-local; promote to fixtures.go only when a second test needs it.
func holdFIFO(t *testing.T, path string) <-chan struct{} {
	t.Helper()
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("holdFIFO: mkfifo %s: %v (a pre-existing file at that path is "+
			"the realistic cause)", path, err)
	}

	opened := make(chan struct{})
	done := make(chan struct{})
	// writeEnd and openErr are written by the goroutine and read by the
	// cleanup only after <-done, so the channel close carries the
	// happens-before. The timeout arm below deliberately reads neither.
	var (
		writeEnd *os.File
		openErr  error
	)
	go func() {
		defer close(done)
		f, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err != nil {
			openErr = err
			return
		}
		writeEnd = f
		close(opened)
	}()

	t.Cleanup(func() {
		// Release, in this order:
		//  1. Open the read end non-blocking. POSIX guarantees O_RDONLY|
		//     O_NONBLOCK on a FIFO never blocks, and it completes the writer
		//     goroutine's rendezvous on the path where claude never ran the
		//     command. It stays open across the wait below so a goroutine
		//     that has not yet reached open(2) still finds a reader.
		//  2. Wait (bounded) for the goroutine to leave open(2), which is
		//     also what makes the reads below race-free.
		//  3. Close the write end — the only release of the hold.
		r, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			t.Errorf("holdFIFO: cleanup read-open %s: %v", path, err)
		}
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			// Leave the read end open on purpose: closing it would re-park a
			// writer goroutine that has not yet reached open(2).
			t.Errorf("holdFIFO: writer goroutine still parked in open(%s) 5s "+
				"after the read end was opened", path)
			return
		}
		if r != nil {
			_ = r.Close()
		}
		if openErr != nil {
			t.Errorf("holdFIFO: open %s for writing: %v", path, openErr)
		}
		if writeEnd != nil {
			_ = writeEnd.Close()
		}
	})

	return opened
}

// TestHoldFIFO_RendezvousAndRelease proves the mechanism TestRealClaude_
// SigtermMidToolUse rests on, without claude and without credentials: the
// rendezvous fires when (and only when) a reader arrives, the reader stays
// blocked for as long as the test holds the write end, only the test's
// cleanup ends it, and a never-read FIFO leaks no goroutine. It carries the
// e2e_realclaude build tag so it lives beside the test it underpins, but it
// runs on every `make e2e-realclaude` regardless of auth.
func TestHoldFIFO_RendezvousAndRelease(t *testing.T) {
	t.Run("rendezvous_hold_release", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "sigterm-hold")

		// Registered BEFORE holdFIFO so it runs AFTER holdFIFO's release
		// (t.Cleanup is LIFO). That ordering is the only way to observe the
		// post-release state from inside the subtest — and it is the same
		// ordering property invariant 1 relies on. It doubles as the reaper
		// for the spawned `cat` so the package leaves no strays.
		// readerDone is CLOSED rather than sent on, so both the assertion in
		// the body and this cleanup can wait on it. A buffered send would let
		// whichever ran first drain the value and hang the other.
		var (
			reader     *exec.Cmd
			readerErr  error
			readerDone = make(chan struct{})
		)
		t.Cleanup(func() {
			if reader == nil {
				return
			}
			select {
			case <-readerDone:
			case <-time.After(5 * time.Second):
				t.Errorf("`cat %s` still running 5s after holdFIFO closed the write "+
					"end — the test does not own the window's end", path)
				_ = reader.Process.Kill()
				<-readerDone
			}
		})

		opened := holdFIFO(t, path)

		select {
		case <-opened:
			t.Fatalf("holdFIFO signalled a rendezvous on %s before any reader opened it", path)
		default:
		}

		cmd := exec.Command("cat", path)
		if err := cmd.Start(); err != nil {
			t.Fatalf("start `cat %s`: %v", path, err)
		}
		reader = cmd
		go func() {
			readerErr = cmd.Wait()
			close(readerDone)
		}()

		select {
		case <-opened:
		case <-time.After(10 * time.Second):
			t.Fatalf("holdFIFO did not signal a rendezvous within 10s of `cat %s` starting", path)
		}

		// The window stays open while the test holds the write end. readerErr
		// is race-free here: the close of readerDone carries the
		// happens-before from the cmd.Wait goroutine.
		select {
		case <-readerDone:
			t.Fatalf("`cat %s` exited (%v) while holdFIFO still held the write end — "+
				"the window closed without the test releasing it", path, readerErr)
		case <-time.After(500 * time.Millisecond):
		}
	})

	t.Run("no_reader_no_leak", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "sigterm-hold")
		opened := holdFIFO(t, path)
		select {
		case <-opened:
			t.Fatalf("holdFIFO signalled a rendezvous on %s with no reader", path)
		case <-time.After(200 * time.Millisecond):
		}
		// Returning here runs holdFIFO's cleanup, which must release the
		// parked writer goroutine rather than hang the suite. holdFIFO's own
		// bounded wait t.Errorf's if it does not.
	})
}

// spawnPyryAgentRun constructs and starts the same argv as RunPyryAgentRun
// but does NOT wait for completion — the caller needs PID access to send
// SIGTERM mid-run. Setpgid: true places pyry in its own process group so
// the defense-in-depth cleanup can reap it. Per the ticket's "Technical
// Notes" and resilience_test.go's precedent, this helper is file-local;
// promote to fixtures.go only when a second test needs the same shape.
func spawnPyryAgentRun(t *testing.T, bin, workdir, promptPath, systemPath string, stdoutBuf, stderrBuf *syncBuffer) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(bin,
		"agent-run",
		"--prompt-file="+promptPath,
		"--system-prompt-file="+systemPath,
		"--allowed-tools=Bash",
		// A generous ceiling, not a sync point. The test interrupts once
		// the three events in the file header have fired — the earliest
		// being the FIFO rendezvous — so this only
		// has to be high enough that claude reaches the single Bash call. A
		// tight cap of 2 was fine on older claude but newer versions spend a
		// turn or two before the tool call, so 2 hit the cap before Bash
		// ran. Keep it loose enough to bound a runaway, not so tight it
		// races claude's pacing.
		"--max-turns=6",
		"--effort=low",
		"--model=claude-haiku-4-5",
		"--workdir="+workdir,
		"--output-format=stream-json",
	)
	cmd.Env = os.Environ()
	cmd.Stdout = stdoutBuf
	cmd.Stderr = stderrBuf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawnPyryAgentRun: start %s: %v", bin, err)
	}
	return cmd
}

// waitForDirectChild blocks until pyry has spawned its claude child (pyry's
// single direct child) or until timeout, returning the child's pid (0 on
// timeout). pgrep -P lists direct children only.
func waitForDirectChild(t *testing.T, pyryPid int, timeout time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		out, err := exec.Command("pgrep", "-P", strconv.Itoa(pyryPid)).Output()
		if err == nil {
			for _, f := range strings.Fields(string(out)) {
				if pid, convErr := strconv.Atoi(f); convErr == nil {
					return pid
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return 0
}

// waitForBashSubprocess blocks until claude's in-flight shell command (the
// leaf process whose base name is `name`, e.g. "cat") appears as a
// DESCENDANT of pyry, or until timeout. It replaces a fixed pre-SIGTERM
// sleep: the caller signals the moment the command is genuinely running.
// claude 2.1.158 runs each Bash command in its own process group two levels
// below pyry, so a `pgrep -g <pyry-pgid>` cannot see it; this walks the
// process tree by parent instead. Returns the subprocess's process group id
// (so the caller can reap the #565 leak) and whether it was found.
func waitForBashSubprocess(t *testing.T, pyryPid int, name string, timeout time.Duration) (int, bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, pid := range descendantsOf(pyryPid) {
			if leafCommandName(pid) == name {
				if pgid := pgidOf(pid); pgid > 0 {
					return pgid, true
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return 0, false
}

// descendantsOf returns every transitive child pid of root via a breadth-
// first walk over `pgrep -P`. pgrep exit code 1 (no children) is the normal
// leaf case and is skipped silently. Bounded by the live process tree, which
// is shallow here (pyry → claude → shell → command).
func descendantsOf(root int) []int {
	var all []int
	queue := []int{root}
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		out, err := exec.Command("pgrep", "-P", strconv.Itoa(parent)).Output()
		if err != nil {
			continue
		}
		for _, f := range strings.Fields(string(out)) {
			if pid, convErr := strconv.Atoi(f); convErr == nil {
				all = append(all, pid)
				queue = append(queue, pid)
			}
		}
	}
	return all
}

// leafCommandName returns the base name of a process's executable (argv[0]
// with any directory stripped), e.g. "cat" for `cat /path/to/fifo` and
// "zsh" for `/bin/zsh -c ...`. Used to pick the leaf Bash command out of the
// process tree, not the shell wrapper that runs it. Empty on lookup failure.
func leafCommandName(pid int) string {
	out, err := exec.Command("ps", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return ""
	}
	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) == 0 {
		return ""
	}
	return filepath.Base(fields[0])
}

// pgidOf returns a process's process-group id, or 0 on lookup failure.
func pgidOf(pid int) int {
	out, err := exec.Command("ps", "-o", "pgid=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0
	}
	pgid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0
	}
	return pgid
}

// waitForProcessGone returns true once pid is no longer alive, polling up to
// timeout. syscall.Kill with signal 0 probes liveness without delivering a
// signal: a non-nil error (ESRCH) means the process is gone.
func waitForProcessGone(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if syscall.Kill(pid, 0) != nil {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// waitForGroupGone returns true once no process in group pgid is alive,
// polling up to timeout. syscall.Kill(-pgid, 0) probes the whole group without
// delivering a signal: a non-nil error (ESRCH) means every member is gone.
// The group's members (claude's `zsh` + `cat`) are reparented to init when
// pyry tears claude down, and init reaps them once pyry's reap SIGKILLs the
// group — the sibling of waitForProcessGone for a whole process group (#565).
func waitForGroupGone(pgid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if syscall.Kill(-pgid, 0) != nil {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// waitForSessionID polls pyry's stdout for the system/init session_id, up to
// timeout. Returns "" if none appears.
func waitForSessionID(stdout *syncBuffer, timeout time.Duration) string {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if id := parseInitSessionID(stdout.Bytes()); id != "" {
			return id
		}
		time.Sleep(100 * time.Millisecond)
	}
	return ""
}

// waitForBashToolUseOnDisk polls claude's on-disk session JSONL until the
// Bash tool_use envelope is flushed (it lags the subprocess by a couple of
// seconds), or until timeout. Uses the same parse as the final assertion so
// the wait and the assertion agree.
func waitForBashToolUseOnDisk(t *testing.T, workdir, sessionID string, timeout time.Duration) bool {
	t.Helper()
	path := jsonlPathFor(workdir, sessionID)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			if id, _ := findBashToolUse(ReadJSONL(t, workdir, sessionID)); id != "" {
				return true
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// findBashToolUse returns the id and event index of the first Bash tool_use
// in events, or ("", -1) if none is present.
func findBashToolUse(events []JSONLEntry) (string, int) {
	for i, e := range events {
		if e.Kind != "assistant" {
			continue
		}
		blocks, err := parseContentBlocks(e.Raw)
		if err != nil {
			continue
		}
		for _, b := range blocks {
			if b.Type == "tool_use" && b.Name == "Bash" && b.ID != "" {
				return b.ID, i
			}
		}
	}
	return "", -1
}

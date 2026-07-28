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
//  4. SIGTERM landed mid-tool_use. A Bash tool_use envelope is on disk, and
//     any matching tool_result is a shutdown-interruption artifact — never a
//     completion of the command, and never claude bounding the call itself.
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
//     with a matching description — so the bound was the MODEL's choice on a
//     command whose shape advertises "blocks forever", not a fixed client
//     policy. Note the irony this header used to record: it rejected
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
// On defeat #4. If invariant 4b trips, the fixture command cannot have
// completed on its own — the test still held the write end when the assertion
// ran, because holdFIFO releases only in t.Cleanup, which by construction runs
// after the test body. Three outcomes, each settled by an artifact the run
// already produced. Check in this order:
//
//  1. Was TestHoldFIFO_RendezvousAndRelease (this file) also RED in this run?
//     Yes → a holdFIFO lifetime bug: the window mechanism itself is broken.
//     Fix that first and treat this failure as downstream noise. No → the
//     write end was held; continue.
//  2. Does the dumped tool_use envelope carry an `input.timeout` field? Yes →
//     claude bounded the call itself. Defeat #4, a claude-side policy change,
//     NOT a pyry regression. No → claude ended the call without bounding it,
//     or the interruption marker changed shape: compare the dumped
//     tool_result envelope against the 2026-07-28 shape above.
//  3. Only if neither: pyry's SIGTERM path regressed — it let claude finish
//     the tool call instead of tearing it down.
//
// On a claude-side defeat the escape hatch is `needs-rework:po` on #1219 with
// the probe transcript attached, NOT a fourth command.
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
// (tool_loop_test.go:194-203), the stream-json result trailer, not to
// contentBlock (tool_loop_test.go:160-168), which is the tool_result surface
// here. Do not chase it.
//
// Where defeat #4 would land. The discriminator is the envelope-level
// `interruptedByShutdown` flag on the line carrying the matching tool_result
// (see classifyBashToolResult). Its cost, recorded so the next contributor
// knows where to look first: the flag is claude-internal, undocumented, and
// had ZERO occurrences repo-wide before #1219. It is chosen anyway for the
// direction in which it fails. The rule is "accept only on positive evidence
// of shutdown interruption; every unknown resolves to REJECT" — so if claude
// renames or drops the flag, the line decodes to Go's zero value (false), the
// test goes RED, and someone looks. Fail-closed costs no extra code.
//
// `is_error` was rejected despite being the more durable, public wire
// vocabulary already present on contentBlock: resilience_test.go:126 asserts
// is_error == true on a Bash tool_result for a command that RAN TO COMPLETION
// and exited non-zero, so accepting on it would accept the exact outcome
// invariant 4 exists to reject. (This package already reads is_error with
// three different meanings across three surfaces.) `toolDenialKind` was
// rejected as semantically mislabelled — exactly the kind of field a later
// release corrects. claude's prose is rejected outright: matching a string
// that goes stale every release rebuilds the treadmill inside the failure
// path.

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
// claude then writes a tool_result during its OWN teardown, after the signal;
// that is the interruption artifact invariant 4b accepts, not a completion of
// the command. See the file header for why the command name itself is not
// load-bearing, and why there is deliberately no "do not set a timeout" nudge.
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
	// and invariant 4's "tool_use present" check needs it. The test still
	// holds the FIFO's write end, so `cat` cannot return and no tool_result is
	// ever written, however long this wait takes.
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

	// Invariant 4b: any matching tool_result is a shutdown-interruption
	// artifact, never a completion. toolResultAbsent also passes — it is the
	// pre-2.1.220 shape, claude torn down before it wrote anything, which is
	// still valid evidence that the signal landed mid-call. Only
	// toolResultEnded fails: claude closed the Bash call itself.
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
	// Recorded on every run, pass or fail: which of the two passing shapes a
	// given claude version produced. `interrupted-by-shutdown` is the 2.1.220
	// shape; `absent` is the pre-2.1.220 one and is also valid. If the accept
	// branch ever drifts from one to the other, this line in the -v log is what
	// dates the change.
	t.Logf("invariant 4b: matching tool_result for Bash tool_use_id=%s classified as %q",
		bashToolUseID, kind)
	if kind == toolResultEnded {
		t.Fatalf("matching tool_result on disk for Bash tool_use_id=%s in %s carries no "+
			"shutdown-interruption marker — claude ended the Bash call itself.\n\n"+
			"The fixture command blocks on the FIFO %s, which this test created "+
			"and still held open for writing when this assertion ran (holdFIFO "+
			"releases only in t.Cleanup, structurally after the test body), so it "+
			"cannot have completed on its own. Check, in this order:\n"+
			"  1. Did TestHoldFIFO_RendezvousAndRelease (this file) also fail in "+
			"this run? If yes, this is a holdFIFO lifetime bug — the window "+
			"mechanism is broken; fix that first and treat this failure as "+
			"downstream noise.\n"+
			"  2. Does the tool_use envelope below carry an `input.timeout` field? "+
			"If yes, claude bounded the call itself — fixture defeat #4, a "+
			"claude-side policy change, NOT a pyry regression.\n"+
			"     If no, claude ended the call without bounding it, or the "+
			"interruption marker changed shape: compare the tool_result envelope "+
			"below against the 2026-07-28 shape in this file's header. The "+
			"discriminator is the envelope-level `interruptedByShutdown` flag; a "+
			"rename or removal is defeat #4 landing on the FIELD rather than on "+
			"the command.\n"+
			"  3. Only if neither: pyry's SIGTERM path regressed — it let claude "+
			"finish the tool call instead of tearing it down.\n"+
			"See this file's header for the fragility history. On a claude-side "+
			"defeat the escape hatch is `needs-rework:po` on #1219 with the probe "+
			"transcript attached, not a fourth command guess.\n\n"+
			"tool_use:\n%s\n\ntool_result:\n%s",
			bashToolUseID, jsonlPath, fifoPath,
			truncate(events[bashIdx].Raw), truncate(entry.Raw))
	}
}

// toolResultKind classifies the matching Bash tool_result for invariant 4.
//
// The whole design rests on one rule: accept only on POSITIVE evidence of
// shutdown interruption; every unknown resolves to reject. That is what makes
// a vacuous pass structurally impossible rather than merely unlikely — see the
// file header for why the discriminator is `interruptedByShutdown` and not
// `is_error`.
type toolResultKind int

const (
	// toolResultAbsent — no matching tool_result on disk. Pre-2.1.220 shape:
	// claude was torn down before it wrote anything.
	toolResultAbsent toolResultKind = iota
	// toolResultInterrupted — matching tool_result carrying claude's
	// envelope-level interruptedByShutdown flag. The 2026-07-28 shape.
	toolResultInterrupted
	// toolResultEnded — matching tool_result with NO interruption marker.
	// claude ended the call on its own; the scenario was never staged.
	toolResultEnded
)

func (k toolResultKind) String() string {
	switch k {
	case toolResultAbsent:
		return "absent"
	case toolResultInterrupted:
		return "interrupted-by-shutdown"
	case toolResultEnded:
		return "ended-by-claude"
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
// First match wins, deliberately. If claude ever wrote a completion result and
// then an interruption marker for the same call, the completion is what proves
// the call ended before the signal, and reporting it is the fail-closed answer.
func classifyBashToolResult(events []JSONLEntry, toolUseID string, from int) (toolResultKind, JSONLEntry) {
	if from < 0 || from+1 >= len(events) {
		return toolResultAbsent, JSONLEntry{}
	}
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
			if interruptedByShutdown(e.Raw) {
				return toolResultInterrupted, e
			}
			return toolResultEnded, e
		}
	}
	return toolResultAbsent, JSONLEntry{}
}

// interruptedByShutdown reports whether a JSONL line carries claude's
// envelope-level `interruptedByShutdown` flag set to true.
//
// The flag is a TOP-LEVEL field of the line, a sibling of `type` and
// `message` — not a field of the content block. It is decoded into a local
// anonymous struct on purpose: contentBlock is shared across this package, and
// this flag does not belong on it.
//
// Fail-closed by construction. An absent field decodes to Go's zero value
// (false) and a malformed line returns false, so "no evidence" is never
// mistaken for "interrupted". If claude renames or drops the flag the test
// goes RED rather than silently green.
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
// PROVENANCE — read before editing. These are the two shapes the discriminator
// has to separate, taken from the 2026-07-27 and 2026-07-28 probe transcripts
// recorded in #1219. Every reconstruction is called out per line; do not
// mistake a reconstruction for a capture, and do not "tidy" a verbatim string.
const (
	// (A) 2026-07-27, claude 2.1.220 backgrounding `tail -f /dev/null`.
	// VERBATIM: the command, and `"timeout":5000` sitting inside the tool_use
	// PARAMS (that placement is the evidence the bound was the MODEL's choice).
	// RECONSTRUCTED: the tool_use id, and the description, which the transcript
	// elided to "…with 5 second timeout".
	probeToolUseBackgrounded = `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_01ReconstructedProbeA","name":"Bash","input":{"command":"tail -f /dev/null","description":"Run tail -f /dev/null with 5 second timeout","timeout":5000}}]}}`

	// (A)'s result. VERBATIM: the prose. RECONSTRUCTED: the background ID and
	// log path (the transcript elided both), the tool_use_id, the `user` line
	// wrapper — and `"is_error":true`, which the transcript did NOT retain.
	// That value is AC2's worst-case rule, not an observation: the flag is
	// assumed to take whichever value makes the discriminator's job hardest.
	// The next case pins that the verdict does not depend on it.
	probeToolResultBackgroundedIsError = `{"type":"user","message":{"content":[{"type":"tool_result","content":"Command did not complete within its 5s timeout and was moved to the background (ID: bash_1). Output is being written to: /tmp/claude-bash-1.log","is_error":true,"tool_use_id":"toolu_01ReconstructedProbeA"}]}}`

	// (A)'s result with `is_error` absent — the other side of the worst-case
	// rule. Same line, that one key removed.
	probeToolResultBackgroundedNoIsError = `{"type":"user","message":{"content":[{"type":"tool_result","content":"Command did not complete within its 5s timeout and was moved to the background (ID: bash_1). Output is being written to: /tmp/claude-bash-1.log","tool_use_id":"toolu_01ReconstructedProbeA"}]}}`

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
	// Synthetic — pins fail-closed on the explicit-false shape, not only the
	// absent one.
	probeToolResultInterruptedFalse = `{"type":"user","message":{"content":[{"type":"tool_result","content":"The user doesn't want to proceed with this tool use. The tool use was rejected...","is_error":true,"tool_use_id":"toolu_01WHRACfWECW8GRys7Wy1szz"}]},"toolDenialKind":"user-rejected","interruptedByShutdown":false,"version":"2.1.220"}`

	// An interruption marker for an UNRELATED tool call. Synthetic — stops a
	// stray marker elsewhere in the session from laundering a defeat.
	probeToolResultInterruptedOtherID = `{"type":"user","message":{"content":[{"type":"tool_result","content":"The user doesn't want to proceed with this tool use. The tool use was rejected...","is_error":true,"tool_use_id":"toolu_01SomeOtherToolCall"}]},"toolDenialKind":"user-rejected","interruptedByShutdown":true,"version":"2.1.220"}`
)

// TestClassifyBashToolResult_ProbeEnvelopes pins invariant 4b's discriminator
// against both probe shapes without claude and without credentials, so it runs
// on every `make e2e-realclaude` regardless of auth: the 2026-07-27
// background-on-timeout envelope must be REJECTED (toolResultEnded) and the
// 2026-07-28 shutdown-interruption envelope ACCEPTED (toolResultInterrupted).
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
			name:  "background_on_timeout_is_error_true",
			lines: []string{probeToolUseBackgrounded, probeToolResultBackgroundedIsError},
			want:  toolResultEnded,
		},
		{
			// Same verdict as the row above with the flag absent: the
			// rejection of (A) is INVARIANT to `is_error`, so no re-probe of
			// the 2026-07-27 transcript can change it.
			name:  "background_on_timeout_is_error_absent",
			lines: []string{probeToolUseBackgrounded, probeToolResultBackgroundedNoIsError},
			want:  toolResultEnded,
		},
		{
			name:  "shutdown_interruption",
			lines: []string{probeToolUseInterrupted, probeToolResultInterrupted},
			want:  toolResultInterrupted,
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
			name:  "interruption_flag_explicitly_false",
			lines: []string{probeToolUseInterrupted, probeToolResultInterruptedFalse},
			want:  toolResultEnded,
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
		// A generous ceiling, not a sync point. The test interrupts as
		// soon as the `tail -f /dev/null` subprocess appears, so this only
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
// leaf process whose base name is `name`, e.g. "tail") appears as a
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
// with any directory stripped), e.g. "tail" for `tail -f /dev/null` and
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
// The group's members (claude's `zsh` + `tail`) are reparented to init when
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

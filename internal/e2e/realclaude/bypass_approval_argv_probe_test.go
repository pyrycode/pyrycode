//go:build e2e_realclaude

package realclaude

// #2061 — #1686 wants every claude child launched with
// --dangerously-skip-permissions and downgraded in-band immediately. Two things
// about that argv have never been measured, and either "no" closes #1686.
//
//	Q2  With production's FOUR approval flags also on the launch argv, does the
//	    child launch, and is it gated THROUGH THE APPROVAL BRIDGE after the
//	    downgrade?  A "no" means the design removes the daemon's approval gate
//	    from every session.
//	Q3  Can anything execute between launch and the downgrade landing?
//
// # The findings, measured 2026-09-03 against claude 2.1.239
//
// BOTH ARE YES. Five children, 52 s, model claude-haiku-4-5, every arm exit 0, no
// tripped deadline, no error_max_turns, empty stderr, 30-67 stdout lines each.
// #1686 is NOT closed by either question.
//
//	arm                 | init.permissionMode | approvals: t1, t2 | turn 1  | turn 2
//	--------------------+---------------------+-------------------+---------+--------
//	bypass_then_default | [bypassPermissions default] | 0, 1      | ungated | GATED
//	approval_only       | [default default]   | 1, 1              | GATED   | GATED
//	prewrite_1..3       | [default default]   | 1, 1              | GATED   | GATED
//
// "approvals" is how many approval requests reached the stub socket during that
// turn — the read both verdicts are taken on. "gated" is permission_denials 1 with
// tool_result.is_error true; "ungated" is 0 and false.
//
//   - Q2: APPROVAL GATE INTACT. The combined argv LAUNCHES — the child came up in
//     `bypassPermissions` with the four approval flags on it, and its system/init
//     line reported `mcp_servers` [{"name":"pyry_approve","status":"connected"}],
//     i.e. the approval server is registered and CONNECTED even while the session
//     is in bypass. So claude does not refuse --dangerously-skip-permissions beside
//     --permission-mode default, and permissionArgs' doc — "the yolo branch means
//     claude's permission path is disabled: no prompt tool, no mcp-config" —
//     describes what pyry SENDS, not a claude that would reject the pair.
//
//     Then the decisive read. Turn 1, in bypass, reached the stub socket ZERO
//     times and ran ungated. The downgrade was acked
//     `{"subtype":"success",…,"response":{"mode":"default"}}` and init reported
//     bypassPermissions → default. Turn 2 reached the stub socket ONCE and was
//     gated. THE BRIDGE COMES BACK. #1686's design does not remove the daemon's
//     approval gate.
//
//     Note what the arm's own turn 1 proves in passing: bypass genuinely
//     SUPPRESSES the bridge rather than merely allowing through it. A connected
//     mcp_servers entry and zero approvals on the same turn is what separates
//     "registered" from "consulted", and it is why the socket read and not the
//     mcp_servers echo is the verdict.
//
//   - Q3: RACE WINDOW CLOSED, on all three consecutive spawns, with no
//     disagreement. Every prewrite child was acked before turn 1 was written
//     (control_responses 1 at the pre_turn_control stage), and every one of them
//     was GATED on turn 1 — one approval each reached the stub socket. Neither
//     branch of AC 3's partition fired: the downgrade DID draw an ack before turn 1
//     (so the design's "downgrade immediately" step has a landing point), and no
//     spawn observed the turn ungated (so no window was open).
//
//     The strongest part of this read is the init line. All three prewrite children
//     reported `default` at init line 0 — their FIRST init line, emitted with turn
//     1 — against bypass_then_default's `bypassPermissions` at the same index on
//     the same argv. The downgrade landed before claude published its opening
//     posture at all, which is a stronger statement than "before the tool ran".
//
// WHAT THIS DOES NOT SETTLE, and #1686 must not read into it. Three limits, each
// with the reason it is a limit:
//
//   - THE STUB DENIED EVERY APPROVAL. A production bridge that ALLOWS would produce
//     an ungated tool_result on turn 2 that is behaviourally identical to a dead
//     bridge — which is exactly AC 2's argument and exactly why the verdict is
//     taken on whether the socket was CONSULTED, not on what the turn did. The
//     verdict survives the allow case; the behavioural rows below it do not.
//   - THE ACK WAS AWAITED. Q3 measured the case most favourable to #1686: the
//     downgrade's control_response was waited for on setModeControlBudget before
//     turn 1 was written. A production writer that fires the downgrade and does not
//     wait has not been measured, and CLOSED here does not license one.
//   - IT MEASURES CLAUDE, NOT PYRY. The daemon's structural fail-safe is untouched:
//     an in-band escalation is unreachable from pyry's own surface because
//     permissionModeAllowed refuses `bypassPermissions` by NON-MEMBERSHIP, and that
//     allow-list is production code this ticket does not modify. It is also a
//     claude-version fact rather than a contract — #1595's opposite-direction
//     caveat held for nineteen releases, which is evidence, not a guarantee.
//     SUPERSEDED as of #2066, which routed the escalation in band on purpose: the
//     allow-list admits it now, so the fail-safe this bullet names has moved to the
//     wire (internal/relay's validPermissionMode) and to permissionModeSpawnWritable
//     at the spawn. The bullet stands as the record of what held when this probe ran,
//     and its point — that the probe measures claude and not pyry — is unchanged.
//
// # Why neither #1595's nor #2060's capture answers these
//
// Both probes deliberately spawned a BARE argv — no --permission-prompt-tool, no
// --mcp-config — because the #383 spike concluded --permission-prompt-tool stdio
// short-circuits enforcement. Production's non-yolo interactive spawn carries four
// flags neither had, composed by permissionArgs and injected per spawn by
// withApprovalArgs:
//
//	--permission-prompt-tool mcp__pyry_approve__approve
//	--mcp-config <path>
//	--strict-mcp-config
//	--permission-mode default
//
// Every capture under testdata/ is EITHER bypass-with-no-approval-flags OR
// approval-flags-with-no-bypass, and the reason no capture is both is structural:
// withApprovalArgs returns early on --dangerously-skip-permissions, so production
// never composes the two. permissionArgs' own doc says the yolo branch means
// "claude's permission path is disabled: no prompt tool, no mcp-config" — which is
// precisely the hazard Q2 exists to measure, since #1686 would put the bypass flag
// onto that same argv for EVERY session.
//
// #2060 settled the mechanism this ticket builds on and settles nothing here:
// claude gates the escalation on the LAUNCH ARGV, not on the session's current
// mode. Its `reescalate` arm ran turn 1 ungated, but its downgrade was written
// AFTER that turn, so it measured a bypass child before a downgrade was asked for
// — not a turn racing a downgrade already written.
//
// # Five children, one drive sequence
//
//	arm                 | launch flags        | control request | position     | read
//	--------------------+---------------------+-----------------+--------------+-------
//	bypass_then_default | bypass + the four   | default         | after turn 1 | turn 2
//	approval_only       | the four            | (none)          | —            | turn 2
//	prewrite_1..3       | bypass + the four   | default         | BEFORE turn 1| turn 1
//
// Every arm drives TWO turns on setModePromptOne / setModePromptTwo verbatim, so
// #1595's index-symmetry rule holds: Q2 is classified at turn 2 on the arm and its
// control alike, and Q3 at turn 1 across all three references. The three prewrite
// arms differ from each other in NAME ALONE — that is the point, since Q3's subject
// is whether the outcome is stable across spawns rather than a one-run coincidence.
//
// # Q2's verdict is taken on the SOCKET, not on the behaviour
//
// This is the constraint the whole file is shaped around. A bypass-launched child
// whose bridge never came back executes its tool ungated; a correctly bridged child
// whose approval was ALLOWED also executes ungated. Those two read IDENTICALLY on
// every field setModeFieldMatches compares, so a verdict taken on that comparison
// would report the gate intact in exactly the case where it is gone.
//
// So the run records how many approval requests reached the stub socket during each
// turn, and bypassArgvGateVerdict takes Q2 on the post-downgrade turn's count and
// on nothing else — no probeOutcome reaches that function. The behavioural
// comparison is logged BESIDE the verdict as corroboration and never enters it.
//
// # The stub answers DENY, unconditionally
//
// askQuestionCaptureServe's shape, for its reason plus one more: with a deny stub a
// correctly-bridged post-downgrade turn is GATED and a dead-bridge one is UNGATED,
// so the behavioural read is informative here and the two reads can be checked
// against each other. That does NOT license taking the verdict behaviourally — the
// deny is a property of this stub, not of production, and AC 2's allow case is the
// one #1686 would actually ship. Deny is also what keeps a gated tool from
// executing on the approval_only arm; the bypass arms execute `ls -la /` regardless,
// which is the posture under test.
//
// The deny message is a fixed package constant and is NEVER derived from the
// request: a message built from claude-supplied bytes would be sent straight back
// out through a run log this pipeline salvages.
//
// # Q3's read, and why the ack is awaited
//
// The prewrite arms write the downgrade before any user turn and WAIT for its ack on
// setModeControlBudget before turn 1 is written. Waiting is the case most favourable
// to #1686, so an ungated turn 1 after a landed ack is a REAL race window rather than
// an artefact of not waiting. bypassArgvWindowOutcome partitions the two outcomes the
// design treats differently, per spawn: acked-and-still-ungated (a real window), and
// no-ack-before-turn-1 (the design cannot write the downgrade that early, so its
// "downgrade immediately" step has no landing point).
//
// # What is committed, and what is redacted
//
// writeSetModeFixture is called unchanged — a sibling writer would be a second route
// to packageDir that eleven finOfflineExecBans entries silently fail to cover. The
// mcp-config and socket paths are run-local temp paths and go through
// bypassArgvRedactor before they are recorded: /var/folders/ and
// /private/var/folders/ are two of dropcapFixedNeedles' five fixed deny classes, and
// a fresh random path per run would also make these captures undiffable — in a
// measurement whose whole subject is the argv. The approval log records the tool NAME
// (capped; it arrives from a subprocess) and NO tool input, which is model-composed
// bytes that writeSetModeFixture runs no deny-scan over.
//
// WHAT THE REDACTOR DOES NOT COVER, stated so nobody reads it as a guarantee it
// does not make: claude echoes its own working directory into the system/init
// line's `cwd`, and that directory is the pinned t.TempDir() HOME, so every capture
// here carries a /private/var/folders/ path inside stdout_events. That is not this
// ticket's doing and is not this ticket's to fix — every committed capture in this
// package already carries it (fifteen occurrences in #2060's `enable` arm, six in
// #1595's `revoke`), and stdout_events is a VERBATIM RECORDING whose value is that
// nothing rewrites it. The redactor covers the two paths THIS FILE puts on the
// argv, which are the two a reader would otherwise have to diff around.
//
// # Running it
//
//	go test -tags e2e_realclaude -race -count=1 -v \
//	  -run TestRealClaude_BypassApprovalArgv ./internal/e2e/realclaude/
//
// Five children, ten probe turns. The name prefix is deliberately none of
// TestRealClaude_SetPermissionMode, TestRealClaude_InBandModeSwitch or
// TestRealClaude_BypassReescalation — all three are documented reproduce filters for
// runs that budgeted their own children. The test PASSES on every recorded outcome
// and fails only where the instrument measured nothing. Read the count of `=== RUN`
// lines, never the exit code: a credential-less suite skips everything and exits 0,
// and a package that fails to build runs zero tests and exits 0 through any shell
// wrapper.
//
// An agent run happens in a worktree discarded when the run ends, so every capture
// this test writes is `git add`ed in the same commit as this file. #1688's and
// #2060's PRs are the pattern.

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/control"
	"github.com/pyrycode/pyrycode/internal/permbridge"
)

// --- the constants this run pins -------------------------------------------------

const (
	// The committed family these captures form. It shares no literal head with
	// permission_protocol_v, dropped_lines_v, set_permission_mode_v,
	// initialize_control_v, ask_user_question_v, permission_mode_switch_v or
	// bypass_reescalation_v, which is what keeps it out of every sweep in this
	// package; TestBypassApprovalArgv_FixtureNamesAvoidRegressionGlobs asserts that
	// rather than leaving it a convention to remember.
	bypassArgvFamilyPrefix = "bypass_approval_argv"

	// A fresh EMPTY directory under the test's pinned $HOME, deliberately not a git
	// repo: less project context for claude to load, so the turns are cheaper.
	bypassArgvWorkdirName = "bypass-approval-argv-work"

	// The --permission-prompt-tool reference. It is cmd/pyry's approveToolRef, which
	// is fmt.Sprintf("mcp__%s__%s", mcpServerName, approveToolName) over two
	// constants in package main that are not importable from here, so it is
	// transcribed — #1938's precedent and its caveat: renaming either constant there
	// does NOT reach this literal, and the symptom is a socket that is never
	// consulted rather than a build failure.
	bypassArgvPromptTool = "mcp__pyry_approve__approve"

	// The mode both the measurement arm and the prewrite arms request.
	bypassArgvDefaultMode = "default"
)

// bypassArgvDenyMessage is the fixed deny reason the stub returns for EVERY call.
// A package-level constant, never derived from the request: a message built from
// claude-supplied bytes would send them straight back out through a run log this
// pipeline salvages.
const bypassArgvDenyMessage = "#2061 probe: every gated call is denied"

const (
	// One approval exchange on the stub socket. The peer is `pyry mcp-approve` on
	// this machine as this user, so this is not an adversarial bound — it is what
	// keeps a decoder from parking on a socket whose peer died mid-frame and holding
	// the accept loop's turn.
	bypassArgvConnBudget = 30 * time.Second

	// A bound on RETAINED approval entries, not on the counts the verdicts read. A
	// claude retrying a tool in a loop must not be able to grow a committed artifact
	// without bound, and must not be able to make the verdict wrong either —
	// bypassArgvApprovalLog.seen stays uncapped and RequestsDropped records the
	// overflow, so a capped capture still states how many approvals there were.
	bypassArgvApprovalCap = 64

	// A tool name arrives from a subprocess and nothing bounds it. It is the ONE
	// claude-supplied string that reaches a log line and the artifact here;
	// reescalateModeQuoteCap's argument, at a cap wide enough for every tool name on
	// record.
	bypassArgvToolNameCap = 64

	// The init line's mcp_servers value is recorded verbatim into a log line, so it
	// gets the same treatment for the same reason, at a cap wide enough for the
	// single-server array this argv can produce.
	bypassArgvMCPServersCap = 512
)

// The arm names. Read-only: the deterministic tests range these and add their own
// hostile literals rather than appending here.
const (
	bypassArgvArmMeasure     = "bypass_then_default"
	bypassArgvArmControl     = "approval_only"
	bypassArgvArmPrewriteOne = "prewrite_1"
	bypassArgvArmPrewriteTwo = "prewrite_2"
	bypassArgvArmPrewriteTre = "prewrite_3"
)

// bypassArgvPrewriteArms is AC 3's "at least three consecutive spawns". They differ
// from each other in NAME ALONE, which is the point: a single spawn cannot tell a
// stable window from a coincidence.
var bypassArgvPrewriteArms = []string{
	bypassArgvArmPrewriteOne,
	bypassArgvArmPrewriteTwo,
	bypassArgvArmPrewriteTre,
}

// bypassArgvArmNames is the drive order. The Q2 pair runs FIRST so that a launch
// failure on the combined argv — the "Invalid MCP configuration" shape #1938
// recorded, or a claude that refuses --dangerously-skip-permissions beside
// --permission-mode default — is reported before three more children are spent on
// an argv that cannot launch.
var bypassArgvArmNames = append([]string{bypassArgvArmMeasure, bypassArgvArmControl},
	bypassArgvPrewriteArms...)

// --- the verdict vocabulary --------------------------------------------------------

// Q2's three outcomes. GONE is the one that closes #1686: the daemon's approval
// gate would be absent from every session, which is a far larger regression than
// the respawn the design removes.
const (
	bypassArgvGateIntact = "APPROVAL GATE INTACT"
	bypassArgvGateGone   = "APPROVAL GATE GONE"
)

// Q3's four outcomes, per spawn.
const (
	bypassArgvWindowOpen      = "RACE WINDOW OPEN"
	bypassArgvWindowClosed    = "RACE WINDOW CLOSED"
	bypassArgvWindowNoLanding = "DOWNGRADE HAS NO LANDING POINT"
	bypassArgvWindowUnread    = "WINDOW NOT MEASURED"
)

// bypassArgvLaunchFailed is the verbatim phrase reported for an arm whose child
// never emitted a system/init line. AC 1 asks the verdict to state whether the
// child launches at all, and #1938 recorded what a bad --mcp-config looks like:
// claude answers "Invalid MCP configuration" and exits 1, spending no tokens. That
// must never be read as a refusal.
const bypassArgvLaunchFailed = "CHILD DID NOT LAUNCH"

// --- the approval observation --------------------------------------------------------

// bypassArgvApprovalRequest is one approval that reached the stub socket. It
// carries the tool NAME and the arrival order and NOTHING ELSE — in particular no
// tool input, which is model-composed bytes that writeSetModeFixture runs no
// deny-scan over. #1938 needed a whole dropcapScanner to commit such a payload, and
// this measurement does not need the payload at all: its questions are answered by
// counts.
type bypassArgvApprovalRequest struct {
	Seq      int    `json:"seq"`
	ToolName string `json:"tool_name"`
}

// bypassArgvStageCount is the running state at one marked point of the drive
// sequence: how many approvals had reached the socket, and how many
// control_responses the rig had counted.
//
// The second field is what makes "was the downgrade acked BEFORE turn 1" a read
// rather than an inference. Deriving it from stdout order instead would rest on
// whether claude emits its system/init line at spawn or per turn — an assumption a
// measurement ticket must not build a verdict on.
type bypassArgvStageCount struct {
	Stage            string `json:"stage"`
	Approvals        int    `json:"approvals"`
	ControlResponses int    `json:"control_responses"`
}

// bypassArgvApproval is one arm's record of what reached the stub approval socket,
// sliced by the drive sequence's stages. No field carries omitempty: a ZERO
// approval count is the finding on an arm whose bridge never came back, and
// omitempty would spell that finding by ABSENCE — the exact trap
// modeSwitchAutoObservation's fields exist to avoid.
type bypassArgvApproval struct {
	Requests        []bypassArgvApprovalRequest `json:"requests"`
	RequestsSeen    int                         `json:"requests_seen"`
	RequestsDropped int                         `json:"requests_dropped"`
	DecodeErrors    int                         `json:"decode_errors"`
	NonApprove      int                         `json:"non_approve_requests"`
	Stages          []bypassArgvStageCount      `json:"stages"`
}

// bypassArgvApprovalLog accumulates one arm's approvals. Mutex-guarded because the
// accept-loop goroutine appends while the test goroutine marks and snapshots; that
// is a race under -race.
//
// One log per arm, swapped in through bypassArgvSocket before each child starts.
// A single shared log would let one child's approvals be attributed to the next.
type bypassArgvApprovalLog struct {
	mu           sync.Mutex
	seen         int // uncapped, and what every count the verdicts read comes from
	requests     []bypassArgvApprovalRequest
	dropped      int
	decodeErrors int
	nonApprove   int
	stages       []bypassArgvStageCount
}

// record notes one approval request. Called from the accept-loop goroutine.
func (l *bypassArgvApprovalLog) record(toolName string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seen++
	if len(l.requests) >= bypassArgvApprovalCap {
		l.dropped++
		return
	}
	l.requests = append(l.requests, bypassArgvApprovalRequest{
		Seq:      l.seen,
		ToolName: truncateString(toolName, bypassArgvToolNameCap),
	})
}

func (l *bypassArgvApprovalLog) noteDecodeError() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.decodeErrors++
}

func (l *bypassArgvApprovalLog) noteNonApprove() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.nonApprove++
}

// mark snapshots the running counts at one point of the drive sequence. It is
// setModeChildConfig.mark's shape, so it is handed to the rig directly.
func (l *bypassArgvApprovalLog) mark(stage string, controlResponses int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.stages = append(l.stages, bypassArgvStageCount{
		Stage: stage, Approvals: l.seen, ControlResponses: controlResponses,
	})
}

// observe returns the completed observation. It is setModeChildConfig.approval's
// shape, so it is handed to the rig directly and is called once, after the child
// has exited and its stdout reader has been joined.
//
// A last connection can in principle still be in flight when this runs — claude is
// dead, but its `pyry mcp-approve` grandchild's socket teardown is not synchronised
// with cmd.Wait(). The lock makes that safe rather than racy; what it would cost is
// a final entry, and every count the verdicts read is taken at a marked stage
// BEFORE this point, so no verdict depends on it.
func (l *bypassArgvApprovalLog) observe() *bypassArgvApproval {
	l.mu.Lock()
	defer l.mu.Unlock()
	return &bypassArgvApproval{
		Requests:        append([]bypassArgvApprovalRequest(nil), l.requests...),
		RequestsSeen:    l.seen,
		RequestsDropped: l.dropped,
		DecodeErrors:    l.decodeErrors,
		NonApprove:      l.nonApprove,
		Stages:          append([]bypassArgvStageCount(nil), l.stages...),
	}
}

// bypassArgvStageAt returns the counts recorded at the FIRST mark of stage, and
// whether it was marked at all. Each stage is marked at most once per child, so
// "first" and "only" coincide; taking the first keeps that true if a future
// widening ever marks one twice.
func bypassArgvStageAt(stages []bypassArgvStageCount, stage string) (bypassArgvStageCount, bool) {
	for _, s := range stages {
		if s.Stage == stage {
			return s, true
		}
	}
	return bypassArgvStageCount{}, false
}

// bypassArgvDuring returns the approvals recorded between two marked stages.
//
// An UNMARKED end reports 0 rather than falling back to the running total. An arm
// that never reached a stage has no read at it, and synthesising one from the total
// would attribute another turn's approvals to a turn that never ran — which on Q2
// is the difference between "the bridge was consulted after the downgrade" and "the
// bridge was consulted at some point".
func bypassArgvDuring(obs *bypassArgvApproval, from, to string) int {
	if obs == nil {
		return 0
	}
	start, okFrom := bypassArgvStageAt(obs.Stages, from)
	end, okTo := bypassArgvStageAt(obs.Stages, to)
	if !okFrom || !okTo || end.Approvals < start.Approvals {
		return 0
	}
	return end.Approvals - start.Approvals
}

// bypassArgvAckedBy reports whether at least one control_response had been counted
// by the time stage was marked. An unmarked stage reports false — an absent read is
// not an ack.
func bypassArgvAckedBy(obs *bypassArgvApproval, stage string) bool {
	if obs == nil {
		return false
	}
	at, ok := bypassArgvStageAt(obs.Stages, stage)
	return ok && at.ControlResponses >= 1
}

// --- the stub approve server -----------------------------------------------------

// bypassArgvSocket holds the log the accept loop is currently writing into. The
// five children run sequentially and each owns one log; the pointer is swapped
// between them under this lock.
//
// Its own mutex is never held while the log's is: attach and current each take and
// release this one, and the caller then takes the log's. There is no nesting, so
// there is no ordering to document and no inversion to construct.
type bypassArgvSocket struct {
	mu      sync.Mutex
	current *bypassArgvApprovalLog
	orphans int
}

func (s *bypassArgvSocket) attach(l *bypassArgvApprovalLog) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.current = l
}

// current returns the log to write into, or nil between arms. A nil return is
// counted rather than dropped silently: an approval arriving with no arm attached
// means a child outlived its own t.Run, which would make some other arm's counts
// wrong and must be visible in the run log.
func (s *bypassArgvSocket) log() *bypassArgvApprovalLog {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == nil {
		s.orphans++
	}
	return s.current
}

func (s *bypassArgvSocket) orphanCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.orphans
}

// bypassArgvServe answers ONE connection on the stub control socket: it decodes the
// control.Request `pyry mcp-approve` sent, records that an approval reached the
// bridge, and answers DENY unconditionally.
//
// NOTHING HERE CALLS t.Fatalf. From a non-test goroutine it does not fail the test
// it was meant to fail; a decode or encode failure is t.Logf plus a return, is
// counted on the arm's log, and the verdict switch on the test goroutine is what
// turns the resulting silence into a finding.
//
// The deadline is set on the conn rather than derived from the run's context: the
// peer is a local same-user process and the whole run is already bounded, so this is
// a liveness bound on one exchange, nothing more.
func bypassArgvServe(t *testing.T, conn net.Conn, sock *bypassArgvSocket) {
	t.Helper()
	defer func() { _ = conn.Close() }()

	if err := conn.SetDeadline(time.Now().Add(bypassArgvConnBudget)); err != nil {
		t.Logf("#2061: set approval conn deadline: %v (continuing)", err)
	}

	log := sock.log()

	var req control.Request
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		if log != nil {
			log.noteDecodeError()
		}
		t.Logf("#2061: decode an approval request: %v (continuing to accept)", err)
		return
	}
	if req.Approve == nil {
		// Answered rather than dropped, so `pyry mcp-approve` gets a response instead
		// of an EOF it would fail closed on for a different reason.
		if log != nil {
			log.noteNonApprove()
		}
		if err := json.NewEncoder(conn).Encode(control.Response{
			Error: "#2061 probe stub: request carried no approve payload",
		}); err != nil {
			t.Logf("#2061: answer a non-approval request: %v", err)
		}
		return
	}

	// THE RECORD IS THE MEASUREMENT. That an approval arrived here at all is what
	// Q2's verdict is taken on; the tool name is context for the reader and the
	// input is deliberately never touched.
	if log != nil {
		log.record(req.Approve.ToolName)
	} else {
		t.Logf("#2061: an approval arrived with no arm attached; some arm's counts may be wrong")
	}

	// permbridge.BehaviorDeny rather than the literal "deny", so a rename in the
	// bridge reaches this file. The message is the fixed constant.
	if err := json.NewEncoder(conn).Encode(control.Response{
		Approve: &control.ApproveResult{
			Behavior: permbridge.BehaviorDeny,
			Message:  bypassArgvDenyMessage,
		},
	}); err != nil {
		t.Logf("#2061: answer an approval request: %v", err)
	}
}

// --- the argv ----------------------------------------------------------------------

// bypassArgvApprovalArgs is permissionArgs(false, mcpConfigPath)'s set, transcribed
// because that function lives in package main. Three of the four are load-bearing
// beyond convention:
//
//   - --strict-mcp-config is non-negotiable. Without it a project or user .mcp.json
//     can register a second pyry_approve server that shadows this one and answers
//     ALLOW, which would make Q2's socket read report a bridge that was never
//     consulted — a false GATE GONE, or worse a false GATE INTACT.
//   - --permission-mode default is the only mode that consults the prompt tool.
//     ask_user_question_capture_test.go's argv already carries it and always has.
//   - There is no --allowed-tools at all: an allowlisted tool is never routed to the
//     prompt tool, so anything listed there could never reach the socket.
func bypassArgvApprovalArgs(mcpConfigPath string) []string {
	return []string{
		"--permission-prompt-tool", bypassArgvPromptTool,
		"--mcp-config", mcpConfigPath,
		"--strict-mcp-config",
		"--permission-mode", bypassArgvDefaultMode,
	}
}

// bypassArgvNames reports whether args carries flag. A plain scan: the slice is one
// argv's worth of strings and is read a handful of times.
func bypassArgvNames(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

// bypassArgvArm builds one arm inline, #2041's and #2060's precedent: setModeArms is
// #1595's live 2.1.220 measurement and an arm appended there would join that run AND
// mint into the set_permission_mode_v* family.
//
// Every arm carries production's four approval flags. An unrecognised name gets the
// CONTROL's shape — no bypass flag, no request — which is the safest wrong answer: a
// typo'd arm cannot silently spawn a bypass child.
// TestBypassArgvArms_CarryTheShapeTheVerdictsClassify pins the five real names.
func bypassArgvArm(name, mcpConfigPath string) setModeArm {
	arm := setModeArm{name: name, extraLaunchArgs: bypassArgvApprovalArgs(mcpConfigPath)}
	switch name {
	case bypassArgvArmMeasure:
		arm.launchYOLO = true
		arm.targetMode = bypassArgvDefaultMode
	case bypassArgvArmPrewriteOne, bypassArgvArmPrewriteTwo, bypassArgvArmPrewriteTre:
		arm.launchYOLO = true
		arm.targetMode = bypassArgvDefaultMode
		arm.requestBeforeFirstTurn = true
	}
	return arm
}

// --- the redactor ------------------------------------------------------------------

// The placeholders. They name what was removed and why, so a reader of a committed
// capture is not left wondering whether the flag carried a value at all.
const (
	bypassArgvRedactedConfig    = "<mcp-config: run-local temp path redacted>"
	bypassArgvRedactedConfigDir = "<mcp-config dir: run-local temp path redacted>"
	bypassArgvRedactedSocket    = "<control socket: run-local temp path redacted>"
	bypassArgvRedactedSocketDir = "<control socket dir: run-local temp path redacted>"
)

// bypassArgvRedactor returns setModeChildConfig.redactRunLocal for one run: it
// replaces the two run-local temp paths, and each one's parent directory, with fixed
// placeholders.
//
// It is a redaction of RUN-LOCAL NOISE, not of a credential. initControlScrubbed is
// the credential guard and runs first inside runSetModeChild, unconditionally.
//
// An EMPTY path contributes nothing, and that guard is not defensive tidiness:
// strings.ReplaceAll(s, "", x) interleaves x between every rune, so a redactor built
// from an empty path would rewrite every string handed to it into garbage.
//
// Each full path is substituted before its own directory, or the directory
// substitution would fire first and leave the basename dangling beside a
// placeholder.
func bypassArgvRedactor(mcpConfigPath, socketPath string) func(string) string {
	type sub struct{ from, to string }
	var subs []sub
	add := func(path, full, dir string) {
		if path == "" {
			return
		}
		subs = append(subs, sub{path, full})
		if d := filepath.Dir(path); d != "" && d != "." && d != "/" {
			subs = append(subs, sub{d, dir})
		}
	}
	add(mcpConfigPath, bypassArgvRedactedConfig, bypassArgvRedactedConfigDir)
	add(socketPath, bypassArgvRedactedSocket, bypassArgvRedactedSocketDir)

	return func(s string) string {
		for _, one := range subs {
			s = strings.ReplaceAll(s, one.from, one.to)
		}
		return s
	}
}

// --- the init-line read ---------------------------------------------------------------

// bypassArgvInitMCPServers returns the mcp_servers value of the FIRST system/init
// line in events, verbatim, and whether such a line was found at all.
//
// It returns the RAW value rather than decoding it into a shape this file guessed.
// Whether 2.1.239 spells mcp_servers as an array of objects, an array of strings or
// something else is exactly what this run is here to record, and a reader that
// decodes into a guessed type reports "absent" for a spelling it merely did not
// anticipate. found==true with an empty raw is therefore its own finding: the init
// line exists and carries no such key.
//
// A retained non-JSON line is a JSON *string* in the record and fails the decode; it
// is skipped rather than aborting the scan of the lines after it.
func bypassArgvInitMCPServers(events []json.RawMessage) (string, bool) {
	for _, ev := range events {
		var env struct {
			Type    string          `json:"type"`
			Subtype string          `json:"subtype"`
			Servers json.RawMessage `json:"mcp_servers"`
		}
		if err := json.Unmarshal(ev, &env); err != nil {
			continue
		}
		if env.Type != "system" || env.Subtype != "init" {
			continue
		}
		return truncateString(string(env.Servers), bypassArgvMCPServersCap), true
	}
	return "", false
}

// --- the verdicts -----------------------------------------------------------------

// bypassArgvGateVerdict classifies Q2 from TWO SOCKET COUNTS AND NOTHING ELSE: how
// many approval requests reached the stub during the arm's post-downgrade turn, and
// how many reached it during the control's turn at the same index.
//
// No probeOutcome is a parameter, and that is the AC-2 constraint expressed as a
// signature rather than as a comment. A bypass-launched child whose bridge never
// came back and a correctly bridged child whose approval was allowed read
// identically on every field setModeFieldMatches compares, so a behavioural rescue
// added here would report the gate intact in exactly the case where it is gone.
//
// The asymmetry between the two zero cases is deliberate. An arm that consulted the
// socket proves the bridge is there whatever the control did, so a control at zero
// does not unmeasure it. An arm at zero proves nothing on its own — only a control
// that DID consult the socket makes the arm's zero mean the bridge is gone rather
// than that the instrument never worked.
func bypassArgvGateVerdict(armApprovals, controlApprovals int) (string, string) {
	switch {
	case armApprovals > 0:
		return bypassArgvGateIntact, fmt.Sprintf(
			"%d approval request(s) reached the stub socket on the arm's post-downgrade turn, "+
				"so the daemon's approval bridge is consulted on a child launched with "+
				"--dangerously-skip-permissions beside the four approval flags", armApprovals)
	case controlApprovals > 0:
		return bypassArgvGateGone, fmt.Sprintf(
			"the arm's post-downgrade turn consulted the stub socket ZERO times while the "+
				"approval-flags-only control consulted it %d time(s) on the same turn. The bridge "+
				"did not come back after the in-band downgrade, so #1686's design removes the "+
				"daemon's approval gate from every session", controlApprovals)
	default:
		return setModeNoDiscrimination, "neither the arm nor the control consulted the stub " +
			"socket at all, so the approval read measured nothing on this argv and no verdict " +
			"about the bridge can be taken from it"
	}
}

// bypassArgvWindowOutcome classifies ONE prewrite spawn for Q3.
//
// acked is whether a control_response had been counted by the time the downgrade's
// wait returned, i.e. BEFORE turn 1 was written. toolUsed is whether turn 1 produced
// a tool_result at all. approvals is how many approval requests reached the stub
// during turn 1.
//
// The partition AC 3 requires is on the ACK, and it is checked first: an unacked
// spawn says nothing about the window whatever the turn did, because the design's
// "downgrade immediately" step had no landing point on that spawn.
//
// A turn that used no tool is neither ungated nor gated. Folding it into "ungated"
// would count a model that answered in prose as evidence of an open window, which is
// the cheapest possible way for this probe to report a finding it did not measure.
func bypassArgvWindowOutcome(acked, toolUsed bool, approvals int) (string, string) {
	switch {
	case !acked:
		return bypassArgvWindowNoLanding, "no control_response had arrived by the time turn 1 " +
			"was written, so the downgrade could not be written that early at all and #1686's " +
			"\"downgrade immediately\" step has no landing point on this spawn"
	case !toolUsed:
		return bypassArgvWindowUnread, "the downgrade was acked before turn 1, but turn 1 " +
			"produced no tool_result, so this spawn shows neither a gated nor an ungated turn"
	case approvals > 0:
		return bypassArgvWindowClosed, fmt.Sprintf(
			"the downgrade was acked before turn 1 and turn 1's tool use reached the stub socket "+
				"%d time(s), so the downgrade had landed by the time anything could execute",
			approvals)
	default:
		return bypassArgvWindowOpen, "the downgrade was acked before turn 1 and turn 1 still " +
			"executed a tool WITHOUT reaching the stub socket, so a child started in bypass can " +
			"execute inside the window #1686's design opens"
	}
}

// --- the fixture family --------------------------------------------------------------

// bypassArgvFixtureName mints the filename for one arm. #2060's minter shape: the
// prefix is a literal no input can reach, the version token goes through versionSlug
// and the arm token through modeSwitchNameToken, which preserves case and maps every
// path metacharacter to `_` so a future contributor typing `a/b` into the arm table
// cannot land a capture outside testdata/.
func bypassArgvFixtureName(versionToken, arm string) string {
	return fmt.Sprintf("%s_v%s_%s.json",
		bypassArgvFamilyPrefix, versionSlug(versionToken), modeSwitchNameToken(arm))
}

func bypassArgvFixturePath(t *testing.T, versionToken, arm string) string {
	t.Helper()
	return filepath.Join(packageDir(t), "testdata", bypassArgvFixtureName(versionToken, arm))
}

// --- the live probe ---------------------------------------------------------------------

// TestRealClaude_BypassApprovalArgv_Probe drives five live children on the argv
// #1686 would ship — production's four approval flags, with
// --dangerously-skip-permissions beside them on four of the five — and records, per
// arm, what reached a stub approval socket during each turn, the init line's
// permissionMode and mcp_servers, and the behavioural read of each turn.
//
// NOT t.Parallel(), and not for the obvious reason: WithWorktreeAuthenticated
// reaches t.Setenv, and a test that calls t.Setenv may not be parallel. The arms are
// sequential for a second reason — one socket serves all five, and the log it writes
// into is swapped between them.
//
// It PASSES on every recorded outcome. A bridge that never came back is a finding, a
// race window is a finding, a child that refuses to launch on the combined argv is a
// finding; only an instrument that measured nothing is a failure, which
// runSetModeChild raises for itself.
func TestRealClaude_BypassApprovalArgv_Probe(t *testing.T) {
	claudeBin := resolveClaudeBin(t)     // t.Skip when claude is not on PATH
	home := WithWorktreeAuthenticated(t) // t.Skip when there are no credentials
	pyryBin := ensurePyryBuilt(t)        // the binary the mcp-config's command names
	versionRaw, versionToken := captureClaudeVersion(t)
	t.Logf("#2061: claude version %q (token %q)", versionRaw, versionToken)

	workdir := filepath.Join(home, bypassArgvWorkdirName)
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("#2061: create workdir: %v", err)
	}

	// BOUND BEFORE THE CONFIG IS WRITTEN AND BEFORE ANY CHILD STARTS, so the first
	// approval cannot race a not-yet-listening socket. shortSocketPath is reused
	// rather than reinvented: macOS caps a Unix socket path near 104 bytes and a
	// path under the (long, authenticated) pinned HOME can overrun it.
	socketPath := shortSocketPath(t)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		// The path is test-owned and temporary, so naming it is safe and is the only
		// thing that makes an EADDRINUSE or a too-long path diagnosable.
		t.Fatalf("#2061: listen on the stub control socket %s: %v", socketPath, err)
	}

	sock := &bypassArgvSocket{}
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		// AN ACCEPT LOOP, NOT A SINGLE ACCEPT: control.Approve goes through
		// requestPatient, which opens a FRESH CONNECTION PER APPROVAL, and each child
		// gates several tools across two turns. The loop ends when ln.Close() makes
		// Accept return an error.
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			bypassArgvServe(t, conn, sock)
		}
	}()
	// Registered as a cleanup so a t.Fatalf anywhere below still closes the listener
	// and JOINS the goroutine before the test binary moves on — an un-joined server
	// goroutine outliving the test is what -race reports.
	t.Cleanup(func() {
		_ = ln.Close()
		<-serverDone
	})

	// The mcp-config document, transcribed from renderMCPServersConfig rather than
	// called: that function lives in package main and is not importable here. Same
	// two keys, same argv, and the pyry binary and socket are both absolute.
	//
	// It transcribes the pyry_approve entry ALONE, deliberately, where the daemon's
	// own document has carried a second pyry_files entry since #2169: this probe
	// asks what a bypass child's argv looks like, and a file-transfer server nothing
	// here calls would change no assertion below while adding a process to every
	// spawn.
	//
	// It goes in a t.TempDir() and NOT in the pinned HOME, which claude also reads
	// for its own configuration. Mode 0600: the file is not secret, but it is an
	// execution instruction claude obeys, and a world-writable one in a shared temp
	// directory is a local-privilege footgun in a suite that already writes there.
	cfgPath := filepath.Join(t.TempDir(), "mcp-approve.json")
	cfgDoc := fmt.Sprintf(`{"mcpServers":{"pyry_approve":{"command":%q,"args":["mcp-approve","-pyry-socket",%q]}}}`,
		pyryBin, socketPath)
	if err := os.WriteFile(cfgPath, []byte(cfgDoc), 0o600); err != nil {
		t.Fatalf("#2061: write the mcp-approve config %s: %v", cfgPath, err)
	}
	redact := bypassArgvRedactor(cfgPath, socketPath)

	records := make(map[string]*setModeFixtureRecord, len(bypassArgvArmNames))
	for _, name := range bypassArgvArmNames {
		arm := bypassArgvArm(name, cfgPath)
		log := &bypassArgvApprovalLog{}
		sock.attach(log)

		cfg := setModeChildConfig{
			model:          setModeModel,
			promptOne:      setModePromptOne,
			promptTwo:      setModePromptTwo,
			maxTurns:       setModeMaxTurns,
			fixturePath:    bypassArgvFixturePath,
			mark:           log.mark,
			approval:       log.observe,
			redactRunLocal: redact,
		}
		t.Run(name, func(t *testing.T) {
			records[name] = runSetModeChild(t, claudeBin, workdir, arm, versionRaw, versionToken, cfg)
			t.Logf("#2061[%s]: capture: %s", name, bypassArgvFixtureName(versionToken, name))
		})
	}
	// Detached before any verdict is computed, so a straggler cannot land in an arm's
	// log after that arm's observation was taken.
	sock.attach(nil)
	if n := sock.orphanCount(); n > 0 {
		t.Logf("#2061: %d approval(s) arrived with no arm attached; some arm's counts may be low", n)
	}

	var missing []string
	for _, name := range bypassArgvArmNames {
		if records[name] == nil {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Logf("#2061: verdicts UNAVAILABLE — arm(s) %v produced no record (a -run filter, or an "+
			"instrument failure above). Not computing a verdict from a missing arm.", missing)
		return
	}

	// AC 1's "does the child launch at all", answered before either verdict and for
	// every arm. #1938 recorded what a bad --mcp-config looks like: claude answers
	// "Invalid MCP configuration" and exits 1, spending no tokens. That must never be
	// read as a refusal, and it must not be read as an ungated turn either.
	launched := make(map[string]bool, len(bypassArgvArmNames))
	for _, name := range bypassArgvArmNames {
		r := records[name]
		servers, sawInit := bypassArgvInitMCPServers(r.StdoutEvents)
		launched[name] = sawInit
		if !sawInit {
			t.Logf("#2061[%s]: %s — no system/init line was emitted (exit=%d, deadline_tripped=%v, "+
				"%d stdout line(s)). Nothing can be said about the approval bridge on this arm.\n"+
				"  stderr: %s",
				name, bypassArgvLaunchFailed, r.ExitCode, r.ContextDeadlineTripped,
				len(r.StdoutEvents), r.StderrCapture)
			continue
		}
		t.Logf("#2061[%s]: launched — init.permissionMode %v, init.mcp_servers %s, exit=%d",
			name, r.InitPermissionModes, servers, r.ExitCode)
		if r.Approval != nil {
			t.Logf("#2061[%s]: approvals seen=%d dropped=%d decode_errors=%d non_approve=%d, stages %+v",
				name, r.Approval.RequestsSeen, r.Approval.RequestsDropped,
				r.Approval.DecodeErrors, r.Approval.NonApprove, r.Approval.Stages)
		}
	}

	bypassArgvReportQ2(t, records, launched)
	bypassArgvReportQ3(t, records, launched)
}

// bypassArgvReportQ2 takes and logs Q2's verdict on the socket read, then logs the
// behavioural comparison beside it as corroboration.
//
// The two reads are kept visibly apart, and the order is the point: the verdict is
// printed BEFORE the behavioural rows, so a reader cannot mistake the rows for what
// it was taken on. Where they disagree, the socket read is the one that stands —
// AC 2's whole argument is that the behavioural fields cannot separate a dead bridge
// from an allowed approval.
func bypassArgvReportQ2(t *testing.T, records map[string]*setModeFixtureRecord, launched map[string]bool) {
	t.Helper()

	arm, control := records[bypassArgvArmMeasure], records[bypassArgvArmControl]
	if !launched[bypassArgvArmMeasure] || !launched[bypassArgvArmControl] {
		t.Logf("#2061: Q2 VERDICT: %s — the arm or its control never launched on this argv, "+
			"which is itself the answer #1686 needs: the combined argv is not viable",
			bypassArgvLaunchFailed)
		return
	}

	// Turn 2 on both, the arm's post-downgrade read and the control's read at the
	// same index. A comparison across indices would fold in a turn-index confound,
	// which is the confound #1595's drive sequence is shaped to avoid.
	armT2 := bypassArgvDuring(arm.Approval, setModeStageControlOne, setModeStageTurnTwo)
	ctlT2 := bypassArgvDuring(control.Approval, setModeStageControlOne, setModeStageTurnTwo)
	t.Logf("#2061: Q2 socket read: arm turn 2 = %d approval(s), control turn 2 = %d approval(s)",
		armT2, ctlT2)

	verdict, why := bypassArgvGateVerdict(armT2, ctlT2)
	t.Logf("#2061: Q2 VERDICT: %s — %s", verdict, why)

	// The echo, recorded beside the verdict and entering nothing. #1595's argument
	// applies unchanged: an echoed success that still behaves like the bypass control
	// is a failed change.
	t.Logf("#2061: %s: requested %q, %d control_response(s), request_id matched=%v",
		bypassArgvArmMeasure, arm.RequestedMode, len(arm.ControlResponses),
		arm.ControlResponseRequestIDMatched)
	for i, resp := range arm.ControlResponses {
		t.Logf("#2061: %s: control_response[%d] verbatim: %s", bypassArgvArmMeasure, i, resp)
	}

	// CORROBORATION ONLY. The two references sit at DIFFERENT turn indices on
	// purpose — the arm's own turn 1 is the only in-family read of "this child, in
	// bypass, before the downgrade" — so these rows carry a confound the verdict
	// above is deliberately free of. They are here to make a disagreement between the
	// two reads legible, never to be read as a second verdict.
	got := setModeOutcomeAt(arm.ProbeOutcomes, 1)
	applied := setModeOutcomeAt(control.ProbeOutcomes, 1)
	failed := setModeOutcomeAt(arm.ProbeOutcomes, 0)
	t.Logf("#2061: Q2 behavioural corroboration (NOT the verdict): arm turn 2: %s", got)
	t.Logf("#2061:   reference %q = control turn 2: %s", bypassArgvArmControl, applied)
	t.Logf("#2061:   reference %q = the arm's OWN turn 1, in bypass: %s", bypassArgvArmMeasure, failed)
	if applied.equal(failed) {
		t.Logf("#2061:   %s at these indices, so the behavioural rows corroborate nothing here",
			setModeNoDiscrimination)
		return
	}
	for _, row := range setModeFieldMatches(got, applied, failed,
		bypassArgvArmControl+" turn 2", bypassArgvArmMeasure+" turn 1 (bypass)") {
		t.Logf("#2061:   field: %s", row)
	}
}

// bypassArgvReportQ3 classifies each prewrite spawn and tallies them.
//
// It reports the PER-SPAWN partition and the tally, never a majority vote. Three
// spawns disagreeing is a stronger finding than either uniform outcome — a
// nondeterministic window is worse for #1686 than a reliably open one, because a
// design cannot be tested against it — and a vote would erase exactly that.
func bypassArgvReportQ3(t *testing.T, records map[string]*setModeFixtureRecord, launched map[string]bool) {
	t.Helper()

	// The two behavioural references for "ungated in bypass" vs "gated through the
	// bridge", both read at turn 1 so they share Q3's index.
	bypassRef := setModeOutcomeAt(records[bypassArgvArmMeasure].ProbeOutcomes, 0)
	gatedRef := setModeOutcomeAt(records[bypassArgvArmControl].ProbeOutcomes, 0)
	refsSeparate := launched[bypassArgvArmMeasure] && launched[bypassArgvArmControl] &&
		!bypassRef.equal(gatedRef)
	t.Logf("#2061: Q3 references at turn 1: bypass-launched %s | approval-only %s (separate=%v)",
		bypassRef, gatedRef, refsSeparate)

	tally := make(map[string]int, 4)
	for _, name := range bypassArgvPrewriteArms {
		r := records[name]
		if !launched[name] {
			tally[bypassArgvLaunchFailed]++
			t.Logf("#2061[%s]: Q3 %s — no system/init line, so this spawn measured nothing",
				name, bypassArgvLaunchFailed)
			continue
		}

		// "Acked BEFORE turn 1" is read at the stage the rig marks once the
		// before-turn-1 request's wait has returned — not from the record's total,
		// which cannot say when.
		acked := bypassArgvAckedBy(r.Approval, setModeStagePreTurnControl)
		turnOne := setModeOutcomeAt(r.ProbeOutcomes, 0)
		approvals := bypassArgvDuring(r.Approval, setModeStagePreTurnControl, setModeStageTurnOne)

		outcome, why := bypassArgvWindowOutcome(acked, turnOne.ToolResultSeen, approvals)
		tally[outcome]++
		t.Logf("#2061[%s]: Q3 %s — %s\n  measured: acked_before_turn_1=%v turn 1 approvals=%d, %s",
			name, outcome, why, acked, approvals, turnOne)
		t.Logf("#2061[%s]: init.permissionMode %v, %d control_response(s), request_id matched=%v",
			name, r.InitPermissionModes, len(r.ControlResponses), r.ControlResponseRequestIDMatched)
		for i, resp := range r.ControlResponses {
			t.Logf("#2061[%s]: control_response[%d] verbatim: %s", name, i, resp)
		}

		// The behavioural half, recorded beside the socket read. Where the two
		// references collapse it says so rather than reporting a match against a
		// distinction that does not exist.
		if !refsSeparate {
			t.Logf("#2061[%s]: behavioural corroboration: %s at turn 1", name, setModeNoDiscrimination)
			continue
		}
		for _, row := range setModeFieldMatches(turnOne, bypassRef, gatedRef,
			"bypass-launched turn 1", "approval-only turn 1") {
			t.Logf("#2061[%s]:   field: %s", name, row)
		}
	}

	t.Logf("#2061: Q3 VERDICT over %d spawn(s): %v", len(bypassArgvPrewriteArms), tally)
	switch {
	case tally[bypassArgvWindowOpen] == len(bypassArgvPrewriteArms):
		t.Logf("#2061: Q3 every spawn observed turn 1 UNGATED after an acked downgrade — the " +
			"window #1686's design opens is real and reproducible")
	case tally[bypassArgvWindowNoLanding] == len(bypassArgvPrewriteArms):
		t.Logf("#2061: Q3 no spawn drew an ack before turn 1 — #1686's \"downgrade immediately\" " +
			"step has no landing point on this argv, which is a different answer from a window")
	case tally[bypassArgvWindowClosed] == len(bypassArgvPrewriteArms):
		t.Logf("#2061: Q3 every spawn had the downgrade landed before anything could execute")
	default:
		t.Logf("#2061: Q3 the spawns DISAGREE. A nondeterministic window is a stronger finding " +
			"than a uniform one: #1686 cannot be tested against it. The per-spawn rows above are " +
			"the record; no majority vote is taken.")
	}
}

// --- the deterministic half ----------------------------------------------------------

// TestBypassApprovalArgv_FixtureNamesAvoidRegressionGlobs is the offline naming
// half: no name bypassArgvFixtureName can mint joins a committed fixture family,
// every pattern making that claim can still match something, every minted name stays
// a plain component directly inside testdata/, and the five arms mint five distinct
// names.
//
// It reuses modeSwitchNamePattern, modeSwitchAnchor and
// setModeAdversarialVersionTokens rather than declaring a fifth copy of that table:
// a copy is a definition that drifts, and the shared token list's own doc says a file
// adding a token there widens every check at once.
//
// No subprocess and no credentials: it passes on a machine with no claude at all.
func TestBypassApprovalArgv_FixtureNamesAvoidRegressionGlobs(t *testing.T) {
	t.Parallel()

	tokens := append(append([]string(nil), setModeAdversarialVersionTokens...),
		"set_permission_mode", "dropped_lines", "permission_mode_switch",
		"initialize_control", "ask_user_question", "bypass_reescalation")

	// The other input dimension. The version token is slugged and the ARM token is
	// sanitised separately, so the arm is the half a future contributor can walk out
	// of testdata/ by typing a string into the arm table. These are this test's own
	// literals and must not be added to bypassArgvArmNames, which the live probe
	// ranges to build its children.
	arms := append(append([]string(nil), bypassArgvArmNames...),
		"a/b", "..", "../..", "/abs", "", "bypass_reescalation")

	// Every committed family in this package. A capture of this measurement joining
	// any of them is read by a test asserting findings about a different argv, or
	// overwrites committed evidence while every test stays green.
	patterns := []modeSwitchNamePattern{
		{
			glob:          fixtureGlob,
			underTestdata: true,
			controls:      []string{"permission_protocol_v0.0.0_acceptEdits.json"},
			hazard: "TestRealClaude_PermissionProtocol_RegressionFixtures sweeps that glob and " +
				"asserts no stdout_events entry is a control_request, which every arm here records",
		},
		{
			glob:     setModeFamilyGlob,
			controls: []string{"set_permission_mode_v0.0.0_revoke.json"},
			hazard: "that is #1595's committed record of the in-band revocation wire format, and a " +
				"live run here writing there overwrites it while every test stays green",
		},
		{
			glob:          dropcapFixtureGlob,
			underTestdata: true,
			controls:      []string{"dropped_lines_v0.0.0.json"},
			hazard:        "the dropped-line capture's fixture sweep would read this record as one of its own",
		},
		{
			glob:          initControlArmFixtureGlob,
			underTestdata: true,
			controls:      []string{"initialize_control_v0.0.0_before_first_turn.json"},
			hazard:        "#1688's initialize captures are swept per arm and asserted about a turn-free child",
		},
		{
			glob:          askQuestionFixtureGlob,
			underTestdata: true,
			controls:      []string{"ask_user_question_v0.0.0.json"},
			hazard:        "the ask_user_question reader would parse this record as one of its captures",
		},
		{
			// Built from #2041's own prefix constant rather than a literal, so the day
			// that family is renamed this row follows it instead of going vacuous.
			glob:          "testdata/" + modeSwitchFamilyPrefix + "_v*_*.json",
			underTestdata: true,
			controls:      []string{modeSwitchFixtureName("0.0.0", "acceptEdits")},
			hazard: "#2041's committed 2.1.239 mode-switch captures would be joined by a measurement " +
				"of a different argv, and a live run here could overwrite one",
		},
		{
			// #2060's family, the row its own names test could not carry. Built from
			// reescalateFamilyPrefix for the same reason as the row above.
			glob:          "testdata/" + reescalateFamilyPrefix + "_v*_*.json",
			underTestdata: true,
			controls:      []string{reescalateFixtureName("0.0.0", "reescalate")},
			hazard: "#2060's committed re-escalation captures are the evidence #1686's mechanism " +
				"finding rests on, and a live run here could overwrite one",
		},
	}

	wantDir := filepath.Join(packageDir(t), "testdata")

	t.Run("no minted name joins a committed family", func(t *testing.T) {
		t.Parallel()
		for _, token := range tokens {
			for _, arm := range arms {
				base := bypassArgvFixtureName(token, arm)
				for _, p := range patterns {
					subject := modeSwitchAnchor(p, base)
					matched, err := filepath.Match(p.glob, subject)
					if err != nil {
						t.Fatalf("filepath.Match(%q, %q): %v; a malformed pattern constant makes "+
							"every comparison in this file meaningless", p.glob, subject, err)
					}
					if matched {
						t.Errorf("token %q arm %q mints %q, which matches %q: %s",
							token, arm, base, p.glob, p.hazard)
					}
				}
			}
		}
	})

	// Without this the negative assertions above pass identically against a typo'd
	// pattern constant, a mis-anchored row, or a glob that matches nothing at all.
	t.Run("every pattern can still match its own family", func(t *testing.T) {
		t.Parallel()
		for _, p := range patterns {
			if len(p.controls) == 0 {
				t.Errorf("pattern %q carries no control, so its negative assertion proves nothing", p.glob)
				continue
			}
			for _, control := range p.controls {
				subject := modeSwitchAnchor(p, control)
				matched, err := filepath.Match(p.glob, subject)
				if err != nil {
					t.Fatalf("filepath.Match(%q, %q): %v", p.glob, subject, err)
				}
				if !matched {
					t.Errorf("control %q does not match %q; the row asserts nothing, either because "+
						"the pattern is wrong or because underTestdata=%v anchors it against the "+
						"wrong string", subject, p.glob, p.underTestdata)
				}
			}
		}
	})

	t.Run("every minted path stays directly inside testdata", func(t *testing.T) {
		t.Parallel()
		for _, token := range tokens {
			for _, arm := range arms {
				path := bypassArgvFixturePath(t, token, arm)
				if dir := filepath.Dir(path); dir != wantDir {
					t.Errorf("token %q arm %q: fixture path %q resolves outside %q",
						token, arm, path, wantDir)
				}
			}
		}
	})

	t.Run("the five arms mint five distinct names", func(t *testing.T) {
		t.Parallel()
		seen := make(map[string]string, len(bypassArgvArmNames))
		for _, arm := range bypassArgvArmNames {
			name := bypassArgvFixtureName("2.1.239", arm)
			if prev, dup := seen[name]; dup {
				t.Errorf("arms %q and %q both mint fixture name %q; one arm's evidence would "+
					"overwrite the other's", prev, arm, name)
			}
			seen[name] = arm
		}
		if len(seen) != len(bypassArgvArmNames) {
			t.Errorf("got %d distinct names for %d arms", len(seen), len(bypassArgvArmNames))
		}
	})
}

// TestBypassArgvGateVerdict_TakesQ2OnTheSocketRead pins AC 2's constraint as code.
// The verdict is a function of two socket counts and NOTHING else — no probeOutcome
// reaches it — so the failure mode AC 2 names (a dead bridge and a bridged-and-allowed
// approval reading identically on every behavioural field) cannot be reintroduced by
// adding a behavioural rescue without deleting a row here.
func TestBypassArgvGateVerdict_TakesQ2OnTheSocketRead(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		arm     int
		control int
		want    string
	}{
		{"the arm consulted the bridge after the downgrade", 1, 1, bypassArgvGateIntact},
		{"the arm consulted it more than once", 3, 1, bypassArgvGateIntact},
		{
			// The row AC 2 exists for: behaviourally indistinguishable from a
			// bridged-and-allowed turn, and the socket is what separates them.
			name: "the bridge never came back", arm: 0, control: 1, want: bypassArgvGateGone,
		},
		{"the bridge never came back, control consulted repeatedly", 0, 4, bypassArgvGateGone},
		{
			// Neither arm consulted the socket, so nothing was measured: reporting GONE
			// here would blame the arm for an instrument that never worked.
			name: "neither arm consulted the socket", arm: 0, control: 0, want: setModeNoDiscrimination,
		},
		{
			// A control that failed to consult the socket does not unmeasure an arm that
			// DID — the gate is demonstrably present on the arm whatever the control did.
			name: "the arm consulted it and the control did not", arm: 1, control: 0, want: bypassArgvGateIntact,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, reason := bypassArgvGateVerdict(tc.arm, tc.control)
			if got != tc.want {
				t.Errorf("bypassArgvGateVerdict(%d, %d) = %q, want %q (reason: %s)",
					tc.arm, tc.control, got, tc.want, reason)
			}
			if reason == "" {
				t.Error("reason is empty; the run has nothing to report for this outcome")
			}
		})
	}
}

// TestBypassArgvWindowOutcome_PartitionsQ3 pins AC 3's partition. The two outcomes
// the design treats differently are "acked and still ungated" (a real race window)
// and "no ack before turn 1" (the design's downgrade step has no landing point);
// collapsing them would report a window where there is none, or none where there is
// one.
func TestBypassArgvWindowOutcome_PartitionsQ3(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		acked     bool
		toolUsed  bool
		approvals int
		want      string
	}{
		{
			name:  "acked before turn 1 and the turn still ran ungated",
			acked: true, toolUsed: true, approvals: 0, want: bypassArgvWindowOpen,
		},
		{
			name:  "acked before turn 1 and the turn was gated through the bridge",
			acked: true, toolUsed: true, approvals: 1, want: bypassArgvWindowClosed,
		},
		{
			name:  "no ack drew before turn 1 at all",
			acked: false, toolUsed: true, approvals: 0, want: bypassArgvWindowNoLanding,
		},
		{
			// The partition is on the ACK, not on the tool: an unacked spawn says
			// nothing about the window whatever the turn did.
			name:  "no ack, and the turn was gated anyway",
			acked: false, toolUsed: true, approvals: 2, want: bypassArgvWindowNoLanding,
		},
		{
			// Neither ungated nor gated: claude reached for no tool, so this spawn
			// carries no read. Folding it into "ungated" would count a model that
			// answered in prose as evidence of an open window.
			name:  "acked, but the turn used no tool at all",
			acked: true, toolUsed: false, approvals: 0, want: bypassArgvWindowUnread,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, reason := bypassArgvWindowOutcome(tc.acked, tc.toolUsed, tc.approvals)
			if got != tc.want {
				t.Errorf("bypassArgvWindowOutcome(%v, %v, %d) = %q, want %q (reason: %s)",
					tc.acked, tc.toolUsed, tc.approvals, got, tc.want, reason)
			}
			if reason == "" {
				t.Error("reason is empty; the run has nothing to report for this spawn")
			}
		})
	}
}

// TestBypassArgvArms_CarryTheShapeTheVerdictsClassify pins the arm table against
// what the two verdicts assume of it. A disagreement is silent: an arm missing the
// bypass flag still runs, still writes a fixture and still gets classified — against
// a question it never asked.
func TestBypassArgvArms_CarryTheShapeTheVerdictsClassify(t *testing.T) {
	t.Parallel()

	const cfgPath = "/nonexistent/mcp-approve.json"

	measure := bypassArgvArm(bypassArgvArmMeasure, cfgPath)
	if !measure.launchYOLO {
		t.Error("the measurement arm must launch WITH --dangerously-skip-permissions; " +
			"that flag beside the approval flags is the whole subject of Q2")
	}
	if measure.targetMode != bypassArgvDefaultMode {
		t.Errorf("the measurement arm requests %q, want %q", measure.targetMode, bypassArgvDefaultMode)
	}
	if measure.requestBeforeFirstTurn {
		t.Error("the measurement arm's downgrade must follow turn 1; Q2's read is turn 2, and a " +
			"downgrade written before turn 1 is Q3's arm, not this one")
	}

	control := bypassArgvArm(bypassArgvArmControl, cfgPath)
	if control.launchYOLO {
		t.Error("the control must launch with the approval flags ALONE; a control carrying the " +
			"bypass flag is the arm, and the comparison would discriminate nothing")
	}
	if control.targetMode != "" || control.secondTargetMode != "" {
		t.Errorf("the control sends a control request (%q, %q); it would no longer be the posture "+
			"the measurement arm is classified against", control.targetMode, control.secondTargetMode)
	}

	for _, name := range bypassArgvPrewriteArms {
		arm := bypassArgvArm(name, cfgPath)
		if !arm.launchYOLO {
			t.Errorf("prewrite arm %q must launch in bypass; Q3 asks what can execute between "+
				"launch and the downgrade landing", name)
		}
		if !arm.requestBeforeFirstTurn {
			t.Errorf("prewrite arm %q does not write its downgrade before turn 1, so it measures "+
				"the same thing the Q2 arm does", name)
		}
		if arm.targetMode != bypassArgvDefaultMode {
			t.Errorf("prewrite arm %q requests %q, want %q", name, arm.targetMode, bypassArgvDefaultMode)
		}
	}

	// Every arm carries production's four flags — that is what makes this the argv
	// pyry's non-yolo sessions actually spawn with rather than a third invented one.
	want := strings.Join([]string{"--permission-prompt-tool", bypassArgvPromptTool,
		"--mcp-config", cfgPath, "--strict-mcp-config", "--permission-mode", "default"}, " ")
	for _, name := range bypassArgvArmNames {
		got := strings.Join(bypassArgvArm(name, cfgPath).extraLaunchArgs, " ")
		if got != want {
			t.Errorf("arm %q carries extra launch args %q, want %q", name, got, want)
		}
		// Restated as its own check because it is the one flag whose absence is
		// silent AND fatal to the measurement: without it a project or user .mcp.json
		// can register a second pyry_approve server that shadows the stub and answers
		// allow, so the socket read would report a bridge that was never consulted.
		if !bypassArgvNames(bypassArgvArm(name, cfgPath).extraLaunchArgs, "--strict-mcp-config") {
			t.Errorf("arm %q omits --strict-mcp-config", name)
		}
	}

	// The drive order the live probe ranges. The Q2 pair must come first, so a launch
	// failure on the combined argv is reported before three more children are spent.
	if bypassArgvArmNames[0] != bypassArgvArmMeasure || bypassArgvArmNames[1] != bypassArgvArmControl {
		t.Errorf("drive order is %v; the Q2 pair must run first", bypassArgvArmNames)
	}
	if len(bypassArgvArmNames) != 2+len(bypassArgvPrewriteArms) {
		t.Errorf("bypassArgvArmNames has %d entries for 2 + %d prewrite arms",
			len(bypassArgvArmNames), len(bypassArgvPrewriteArms))
	}
	if len(bypassArgvPrewriteArms) < 3 {
		t.Errorf("%d prewrite arms; AC 3 asks for at least three consecutive spawns",
			len(bypassArgvPrewriteArms))
	}
}

// TestBypassArgvRedactRunLocal_KeepsRunLocalPathsOutOfTheArtifact pins the redactor.
// /var/folders/ and /private/var/folders/ are two of dropcapFixedNeedles' five fixed
// deny classes, and a fresh random path per run would also make the captures
// undiffable — in a ticket whose subject IS the argv.
func TestBypassArgvRedactRunLocal_KeepsRunLocalPathsOutOfTheArtifact(t *testing.T) {
	t.Parallel()

	const (
		cfg  = "/var/folders/ab/cd1234/T/TestX/001/mcp-approve.json"
		sock = "/tmp/pyry-sock-987654/pyry.sock"
	)
	redact := bypassArgvRedactor(cfg, sock)

	for _, s := range []string{
		cfg, sock,
		"--mcp-config " + cfg,
		"connecting to " + sock + " failed",
		filepath.Dir(cfg),
		filepath.Dir(sock),
	} {
		got := redact(s)
		if strings.Contains(got, cfg) || strings.Contains(got, sock) {
			t.Errorf("redact(%q) = %q, which still carries a run-local path", s, got)
		}
		if strings.Contains(got, filepath.Dir(cfg)) || strings.Contains(got, filepath.Dir(sock)) {
			t.Errorf("redact(%q) = %q, which still carries a run-local directory", s, got)
		}
	}

	// A token carrying neither path is returned unchanged: the redactor must not
	// rewrite the argv it is there to make readable.
	for _, s := range []string{"--strict-mcp-config", "--permission-mode", "default", ""} {
		if got := redact(s); got != s {
			t.Errorf("redact(%q) = %q, want it unchanged", s, got)
		}
	}

	// An empty path must not turn the redactor into a function that rewrites every
	// string: strings.ReplaceAll(s, "", x) interleaves x between every rune.
	empty := bypassArgvRedactor("", "")
	if got := empty("--permission-mode"); got != "--permission-mode" {
		t.Errorf("a redactor built from empty paths rewrote %q into %q", "--permission-mode", got)
	}
}

// TestBypassArgvInitMCPServers_ReadsWhatTheInitLineActuallyCarries pins the
// mcp_servers read AC 1 asks the verdict to state. It records the value VERBATIM
// rather than decoding it into a shape this file guessed: whether 2.1.239 spells it
// as an array of objects is Open Question 2 in the plan, and a reader that decodes
// into a guessed type reports "absent" for a spelling it merely did not anticipate.
func TestBypassArgvInitMCPServers_ReadsWhatTheInitLineActuallyCarries(t *testing.T) {
	t.Parallel()

	raws := func(lines ...string) []json.RawMessage {
		out := make([]json.RawMessage, 0, len(lines))
		for _, l := range lines {
			out = append(out, json.RawMessage(l))
		}
		return out
	}

	tests := []struct {
		name      string
		events    []json.RawMessage
		wantFound bool
		wantRaw   string
	}{
		{
			name: "an init line carrying an array of server objects",
			events: raws(`{"type":"system","subtype":"init","permissionMode":"default",` +
				`"mcp_servers":[{"name":"pyry_approve","status":"connected"}]}`),
			wantFound: true,
			wantRaw:   `[{"name":"pyry_approve","status":"connected"}]`,
		},
		{
			name:      "an init line carrying an empty array",
			events:    raws(`{"type":"system","subtype":"init","mcp_servers":[]}`),
			wantFound: true,
			wantRaw:   `[]`,
		},
		{
			// The absence that matters: an init line WAS emitted and the key is not on
			// it. Reported as found-with-no-key, never as "no init line" — those are
			// two different findings and only one of them is a launch failure.
			name:      "an init line with no mcp_servers key at all",
			events:    raws(`{"type":"system","subtype":"init","permissionMode":"bypassPermissions"}`),
			wantFound: true,
			wantRaw:   "",
		},
		{
			name:      "no init line anywhere",
			events:    raws(`{"type":"assistant"}`, `{"type":"result","subtype":"success"}`),
			wantFound: false,
			wantRaw:   "",
		},
		{
			name: "the FIRST init line wins over a later one",
			events: raws(`{"type":"assistant"}`,
				`{"type":"system","subtype":"init","mcp_servers":["first"]}`,
				`{"type":"system","subtype":"init","mcp_servers":["second"]}`),
			wantFound: true,
			wantRaw:   `["first"]`,
		},
		{
			// A retained non-JSON line is a JSON *string* in the record; it must not
			// stop the scan of the lines after it.
			name: "a retained non-JSON line does not stop the scan",
			events: raws(`"claude: something unparseable"`,
				`{"type":"system","subtype":"init","mcp_servers":[{"name":"pyry_approve"}]}`),
			wantFound: true,
			wantRaw:   `[{"name":"pyry_approve"}]`,
		},
		{
			name:      "no events at all",
			events:    nil,
			wantFound: false,
			wantRaw:   "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			raw, found := bypassArgvInitMCPServers(tc.events)
			if found != tc.wantFound {
				t.Errorf("found = %v, want %v", found, tc.wantFound)
			}
			if raw != tc.wantRaw {
				t.Errorf("raw = %q, want %q", raw, tc.wantRaw)
			}
		})
	}
}

// TestBypassArgvApprovalLog_AttributesRequestsToTheTurnThatProducedThem pins the
// stage accounting both verdicts are computed from. A TOTAL cannot answer either
// question: Q2 asks about the post-downgrade turn specifically, and Q3 about turn 1.
func TestBypassArgvApprovalLog_AttributesRequestsToTheTurnThatProducedThem(t *testing.T) {
	t.Parallel()

	log := &bypassArgvApprovalLog{}
	log.mark(setModeStageStart, 0)
	log.mark(setModeStagePreTurnControl, 0)
	log.record("Bash")
	log.mark(setModeStageTurnOne, 0)
	log.mark(setModeStageControlOne, 1)
	log.record("Bash")
	log.record("Read")
	log.mark(setModeStageTurnTwo, 1)

	obs := log.observe()
	if got := bypassArgvDuring(obs, setModeStagePreTurnControl, setModeStageTurnOne); got != 1 {
		t.Errorf("turn 1 approvals = %d, want 1", got)
	}
	if got := bypassArgvDuring(obs, setModeStageControlOne, setModeStageTurnTwo); got != 2 {
		t.Errorf("turn 2 approvals = %d, want 2", got)
	}
	// A window whose ends were never marked reports zero rather than a running total:
	// an arm that never reached a stage has no read at it, and synthesising one would
	// attribute another turn's approvals to a turn that never ran.
	if got := bypassArgvDuring(obs, setModeStageControlTwo, setModeStageTurnThree); got != 0 {
		t.Errorf("an unmarked window reports %d approvals, want 0", got)
	}
	if len(obs.Requests) != 3 || obs.RequestsSeen != 3 {
		t.Errorf("recorded %d requests (seen %d), want 3", len(obs.Requests), obs.RequestsSeen)
	}

	// The ack read Q3's partition turns on. It is taken at the stage the rig marks
	// once the before-turn-1 request's wait has returned, so a control_response
	// counted later cannot back-date itself into an ack.
	if bypassArgvAckedBy(obs, setModeStagePreTurnControl) {
		t.Error("acked_before_turn_1 = true with zero control_responses at that stage")
	}
	if !bypassArgvAckedBy(obs, setModeStageControlOne) {
		t.Error("acked = false at a stage that recorded one control_response")
	}
	if bypassArgvAckedBy(obs, setModeStageTurnThree) {
		t.Error("an UNMARKED stage reported an ack; an absent read is not an ack")
	}
	if bypassArgvAckedBy(nil, setModeStagePreTurnControl) {
		t.Error("a nil observation reported an ack")
	}

	// The cap bounds retained ENTRIES, never the counts the verdicts read: a claude
	// retrying a tool in a loop must not be able to grow the artifact, and must not be
	// able to make the verdict wrong either.
	big := &bypassArgvApprovalLog{}
	big.mark(setModeStageStart, 0)
	for i := 0; i < bypassArgvApprovalCap+7; i++ {
		big.record("Bash")
	}
	big.mark(setModeStageTurnOne, 0)
	bigObs := big.observe()
	if len(bigObs.Requests) != bypassArgvApprovalCap {
		t.Errorf("retained %d requests, want the cap %d", len(bigObs.Requests), bypassArgvApprovalCap)
	}
	if bigObs.RequestsDropped != 7 {
		t.Errorf("dropped %d requests, want 7", bigObs.RequestsDropped)
	}
	if got := bypassArgvDuring(bigObs, setModeStageStart, setModeStageTurnOne); got != bypassArgvApprovalCap+7 {
		t.Errorf("turn 1 approvals = %d, want %d — the cap must bound the artifact, not the count",
			got, bypassArgvApprovalCap+7)
	}

	// A tool name arrives from a subprocess and nothing bounds it.
	long := &bypassArgvApprovalLog{}
	long.record(strings.Repeat("N", 4096))
	if n := len(long.observe().Requests[0].ToolName); n > bypassArgvToolNameCap {
		t.Errorf("retained a %d-byte tool name; the cap is %d", n, bypassArgvToolNameCap)
	}
}

//go:build e2e_realclaude

package realclaude

// #1938 — the live half of the AskUserQuestion capture: one real claude, spawned
// with the daemon's own permission flags, driven to call its clarifying-question
// tool, with the call's input recorded VERBATIM as the approve path receives it
// and committed under testdata/.
//
// Everything this run writes through is already merged and is called rather than
// rebuilt: #1943's askQuestionFixtureRecord, #1944's askQuestionFixtureName,
// #1941's writeAskQuestionFixture with its fail-closed deny-scan, and #1951's,
// #1952's and #1950's requireAskQuestionShape. This file produces the bytes and
// calls those four. It ships no record field, no namer, no writer and no shape
// logic.
//
// # Why a live run and not a fake
//
// All eight committed permission_protocol_* captures list AskUserQuestion in
// their `system`/`init` tools array, unbroken from claude 2.1.143 through
// 2.1.199, and not one committed capture holds a tool_use BLOCK for it: the
// name's every occurrence under testdata/ is inside a tools array. So the OFFER
// is measured and the PAYLOAD is not. A fake built to the tool's documented shape
// cannot contradict the documented shape, so the fake-daemon suite would go green
// against a field name claude never sends. Only a real child settles it.
//
// # Where it taps, and why any point on the approve path is equivalent
//
//	claude ──stdio──▶ pyry mcp-approve ──unix socket──▶ this test's stub server
//
// The daemon is absent by design. handleApprove would park the payload keyed by
// tool_use_id; the stub receives the identical control.Request{Verb:
// VerbMCPApprove, Approve: …} and answers it directly. control.ApprovePayload's
// Input field is json.RawMessage on both the marshal and the unmarshal side, so
// the bytes reaching the stub ARE the bytes claude emitted — which is what makes
// the tap point a free choice and makes startStreamModalResolutionHarness's
// phone/noise/relay stack unnecessary here.
//
// # What this run must not do
//
// It must not read the tool_use block off stdout to get a payload. AC 1 says "as
// the approve path receives it", and a stdout-sourced capture answers a different
// question than the daemon needs answered. It must not widen askQuestionInput or
// loosen a shape check to get green: a target accepting two spellings cannot
// redden on either, and reddening is how this capture reports a divergence.
//
// # Its argv is NOT permission_protocol_spike_test.go's
//
// That file is the right process skeleton and the wrong flags. It passes
// `--permission-prompt-tool stdio`, and `stdio` is not a served MCP tool: in the
// committed permission_protocol_v2.1.199.json the gated Bash call runs to
// completion, returns a real directory listing as its tool_result, and the run
// ends `result success`. Its own header says the test passes whether or not a
// permission event fires. Copied wholesale it would execute gated tools, which AC
// 4 forbids. Take the pipes, the single reader goroutine and the 1 MiB scanner
// buffer; replace the flags with cmd/pyry/mcp_config.go's permissionArgs set.
//
// # Running it
//
//	go test -tags e2e_realclaude -count=1 -v \
//	  -run '^TestRealClaude_AskUserQuestion_CapturesTheCall$' ./internal/e2e/realclaude/
//
// It costs one live claude turn and needs real credentials. A skip without them is
// the correct outcome and carries no signal; NO EMPTY OUTCOME REPORTS SUCCESS —
// every path out of the test body is a skip, one of three named t.Fatalf messages,
// or a written artifact. Read the count of tests that executed, never the exit
// code: this package is behind the e2e_realclaude tag, `make check` never compiles
// it, and the suite exits 0 both on a build failure and on a full credentials
// skip. `make preship` is the gate that proves the package builds.
//
// An agent run happens in a worktree discarded when the run ends, so the artifact
// this test writes must be `git add`ed in the same commit as this file. #1688's PR
// is the pattern. On 2026-08-25 #1763's live gate ran green, spent real tokens,
// and landed none of the three artifacts its criteria asked for.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/control"
	"github.com/pyrycode/pyrycode/internal/permbridge"
)

// --- the constants this run pins -------------------------------------------------

// askQuestionToolName is the tool whose call this run is here to record. It is
// compared against the ARRIVING payload's ToolName and against the init line's
// tools array; the record's own field is filled from the payload rather than from
// this constant, so a claude that spells the tool differently reddens through
// askQuestionShapeFindings' tool_name check instead of being papered over here.
const askQuestionToolName = "AskUserQuestion"

// askQuestionCapturePromptTool is the --permission-prompt-tool reference. It is
// cmd/pyry's approveToolRef, which is fmt.Sprintf("mcp__%s__%s", mcpServerName,
// approveToolName) over the two constants in cmd/pyry/mcp_approve.go. Those live
// in package main and are not importable from a test package, so the reference is
// transcribed; renaming either constant there does NOT reach this literal, and the
// symptom is the "not called" outcome below rather than a build failure.
const askQuestionCapturePromptTool = "mcp__pyry_approve__approve"

// askQuestionCaptureModel is deliberately not the spike's claude-haiku-4-5. That
// probe passes whether or not a permission event fires, so it optimises for cost;
// this run's failure mode is an unproduced artifact that blocks #1939, and
// tool-selection reliability is worth more than the token delta.
const askQuestionCaptureModel = "claude-sonnet-5"

// askQuestionCaptureDenyMessage is the fixed deny reason the stub returns for
// EVERY call, the wanted one included. It is a package-level constant and is never
// derived from the request: a message built from claude-supplied bytes would send
// them straight back out through a run log this pipeline salvages.
const askQuestionCaptureDenyMessage = "#1938 capture: every gated call is denied"

const (
	// askQuestionCaptureBudget bounds the whole spawn. A turn that asks a
	// clarifying question is short; the budget exists so a claude blocked on an
	// approval this test has already stopped answering cannot hang the suite.
	askQuestionCaptureBudget = 180 * time.Second
	// askQuestionCaptureConnBudget bounds ONE approval exchange on the stub
	// socket. The peer is `pyry mcp-approve` on this machine as this user, so this
	// is not an adversarial bound — it is what keeps a decoder from parking on a
	// socket whose peer died mid-frame and holding the accept loop's turn.
	askQuestionCaptureConnBudget = 30 * time.Second
	// askQuestionCaptureStderrCap caps claude's own stderr before it is printed.
	// stderr is claude's diagnostic channel plus `pyry mcp-approve`'s slog output;
	// it is unbounded and can carry paths, so it is printed only on the failure
	// branches and only capped.
	askQuestionCaptureStderrCap = 8 * 1024
)

// askQuestionCapturePrompt drives the call. Three properties are load-bearing and
// none of them is stylistic:
//
//   - IT NAMES THE TOOL. That is legitimate rather than a loosened check: this
//     ticket measures the tool's PAYLOAD SHAPE, not claude's spontaneous
//     propensity to reach for it.
//   - IT ASKS FOR A FORM THE EIGHT SHAPE CHECKS CAN READ: one question with a
//     header, at least two options, and a one-line description on each. The
//     multiSelect key is asked for by naming single-vs-multiple selection —
//     presence is what requireAskQuestionShape tests, so `false` is a pass.
//   - IT CARRIES NO PATH, NO FILENAME AND NO "in this repo". The deny-scan armed
//     below includes the FIXED class /var/folders/, and on macOS the worktree
//     lives under it, so a question quoting its working directory makes
//     scanAskQuestionFixture refuse, write nothing, and spend the spawn for
//     nothing. Keeping the subject abstract is a correctness constraint on the
//     prompt, not a matter of taste.
const askQuestionCapturePrompt = "Before writing anything, use the AskUserQuestion tool to ask me one " +
	"clarifying question: for an in-memory key-value cache, should it use a write-through or a " +
	"write-behind strategy? Give the question a short header, offer both strategies as options with a " +
	"one-line description each, and allow only a single choice. Ask the question and stop — do not write " +
	"code and do not use any other tool."

// --- the stub approve server -------------------------------------------------------

// askQuestionCaptureServe answers ONE connection on the stub control socket: it
// decodes the control.Request `pyry mcp-approve` sent, publishes the payload when
// it is the wanted call, and answers DENY unconditionally.
//
// DENY IS UNCONDITIONAL, INCLUDING FOR THE WANTED CALL. AC 4 asks that no gated
// tool execute and that the run leave nothing behind, and denying the clarifying
// question costs this capture nothing: the bytes are already in hand by the time
// the verdict is composed. Omitting --allowed-tools entirely (see the argv) means
// every tool claude reaches for arrives here, so this one branch is what makes
// "no gated tool executes" structural rather than a per-tool allowlist.
//
// THE SEND IS NON-BLOCKING INTO A BUFFERED CHANNEL. A second AskUserQuestion call,
// or a retry after the deny, must not park this goroutine after the test goroutine
// has moved on to the outcome switch.
//
// NOTHING HERE CALLS t.Fatalf. scanAskQuestionFixture, writeAskQuestionFixture and
// requireAskQuestionShape all fail fatally and all three doc comments name this
// test's goroutines as the place not to call them from; t.Fatalf from a non-test
// goroutine does not fail the test it was meant to fail. An accept, decode or
// encode failure is t.Logf plus a return, and the outcome switch on the test
// goroutine is what turns the resulting silence into a verdict.
//
// The deadline is set on the conn rather than derived from the run's context: the
// peer is a local same-user process and the whole run is already bounded, so this
// is a liveness bound on one exchange, nothing more.
func askQuestionCaptureServe(t *testing.T, conn net.Conn, wanted chan<- control.ApprovePayload) {
	t.Helper()
	defer func() { _ = conn.Close() }()

	if err := conn.SetDeadline(time.Now().Add(askQuestionCaptureConnBudget)); err != nil {
		t.Logf("#1938: set approval conn deadline: %v (continuing)", err)
	}

	var req control.Request
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		t.Logf("#1938: decode an approval request: %v (continuing to accept)", err)
		return
	}
	if req.Approve == nil {
		// Not an approval — answer rather than drop it, so `pyry mcp-approve` gets a
		// response instead of an EOF it would fail closed on for a different reason.
		if err := json.NewEncoder(conn).Encode(control.Response{
			Error: "#1938 capture stub: request carried no approve payload",
		}); err != nil {
			t.Logf("#1938: answer a non-approval request: %v", err)
		}
		return
	}

	if req.Approve.ToolName == askQuestionToolName {
		// A COPY of the payload, published by value: req is this goroutine's and the
		// test goroutine must not read through a pointer into it.
		select {
		case wanted <- *req.Approve:
		default:
		}
	}

	// permbridge.BehaviorDeny rather than the literal "deny", so a rename in the
	// bridge reaches this file. The message is the fixed constant.
	if err := json.NewEncoder(conn).Encode(control.Response{
		Approve: &control.ApproveResult{
			Behavior: permbridge.BehaviorDeny,
			Message:  askQuestionCaptureDenyMessage,
		},
	}); err != nil {
		t.Logf("#1938: answer an approval request: %v", err)
	}
}

// --- the stdout reader ------------------------------------------------------------

// askQuestionCaptureInit is the minimal envelope the reader decodes out of each
// stream-json line. It carries no message body: this run records the CALL, not the
// session, and there is no field on askQuestionFixtureRecord for stdout to land in
// even if it were accumulated.
type askQuestionCaptureInit struct {
	Type    string   `json:"type"`
	Subtype string   `json:"subtype"`
	Tools   []string `json:"tools"`
}

// askQuestionCaptureReader is the reader goroutine's recorded state. It is written
// by that goroutine and read by the test goroutine ONLY after the join on done —
// that join is the happens-before edge, which is why there is no mutex here.
type askQuestionCaptureReader struct {
	sawInit bool
	tools   []string
	scanErr error
}

// askQuestionCaptureRead consumes claude's stdout to EOF and records the first
// `system`/`init` line's tools array.
//
// That array is the sole input to the "not offered" diagnosis, and asserting it is
// what keeps a model that simply declined to ask from being misreported as a claude
// release that stopped offering the tool. The distinction is live rather than
// theoretical: every committed capture listing AskUserQuestion was spawned WITH
// --permission-prompt-tool, while the newest one, initialize_control_v2.1.239.json,
// was spawned without it and does not list the tool.
//
// It drains to EOF even after the init line is seen, because an unconsumed stdout
// pipe blocks the child.
func askQuestionCaptureRead(out *bufio.Scanner, rec *askQuestionCaptureReader) {
	// 1 MiB, the spike's cap: the default 64 KiB is too low for model output and
	// the bigger buffer is essentially free.
	out.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for out.Scan() {
		if rec.sawInit {
			continue
		}
		var line askQuestionCaptureInit
		if err := json.Unmarshal(out.Bytes(), &line); err != nil {
			continue
		}
		if line.Type == "system" && line.Subtype == "init" {
			rec.sawInit = true
			rec.tools = line.Tools
		}
	}
	rec.scanErr = out.Err()
}

// askQuestionCaptureOffers reports whether the init line's tools array names the
// tool. A plain scan rather than a map: the array is one init line's worth of
// strings and is read exactly once.
func askQuestionCaptureOffers(tools []string, name string) bool {
	for _, tool := range tools {
		if tool == name {
			return true
		}
	}
	return false
}

// --- the run ----------------------------------------------------------------------

// TestRealClaude_AskUserQuestion_CapturesTheCall spawns a real claude with the
// daemon's permission flags, drives it to call AskUserQuestion, records the call's
// input as the approve path receives it, and commits the record under testdata/.
//
// NOT t.Parallel(), and the reason is not obvious from the call: it goes through
// WithWorktreeAuthenticated, which reaches t.Setenv, and a test that calls
// t.Setenv may not be parallel.
//
// FOUR OUTCOMES AND NO FIFTH. Every path out of this body is a skip (no claude
// binary, or no credentials), one of three t.Fatalf messages naming DIFFERENT
// defects, or a written artifact. The success branch is the only one that falls
// through. The three failures are kept apart deliberately: a spawn that never
// initialised, a claude release that stopped offering the tool, and a model that
// was offered the tool and did not call it are three different defects, and a
// shared message would send the reader after the wrong one.
//
// WHICH FAILURE BRANCHES WERE EXERCISED, stated so none is credited with more than
// it measured. "No init line" was RUN on 2026-09-01, under a `go test -overlay`
// pointing --mcp-config at a nonexistent path: claude answered "Invalid MCP
// configuration", exited 1, and this branch reported exactly that, spending no
// tokens. "Not offered" is ARGUED from the committed
// initialize_control_v2.1.239.json, which was spawned WITHOUT
// --permission-prompt-tool and lists no AskUserQuestion — reaching it live would
// need an argv this test deliberately does not compose. "Not called" is
// UNEXERCISED: reaching it costs a full live spawn that produces nothing, which is
// the very outcome the branch exists to report.
func TestRealClaude_AskUserQuestion_CapturesTheCall(t *testing.T) {
	claudeBin := resolveClaudeBin(t)                    // skips naming PATH / PYRY_CLAUDE_BIN
	worktree := WithWorktreeAuthenticated(t)            // skips naming both credential variables
	pyryBin := ensurePyryBuilt(t)                       // the binary the mcp-config's command names
	versionRaw, versionToken := captureClaudeVersion(t) // exactly the record's two version fields

	// BOUND BEFORE THE CONFIG IS WRITTEN AND BEFORE claude STARTS, so the first
	// approval cannot race a not-yet-listening socket. shortSocketPath is reused
	// rather than reinvented: macOS caps a Unix socket path near 104 bytes and a
	// t.TempDir() path under /var/folders/…/Test…/001 can overrun it.
	socketPath := shortSocketPath(t)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		// The path is test-owned and temporary, so naming it is safe and is the
		// only thing that makes an EADDRINUSE or a too-long path diagnosable.
		t.Fatalf("#1938: listen on the stub control socket %s: %v", socketPath, err)
	}

	wanted := make(chan control.ApprovePayload, 1)
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		// AN ACCEPT LOOP, NOT A SINGLE ACCEPT: control.Approve goes through
		// requestPatient, which opens a FRESH CONNECTION PER APPROVAL, and claude may
		// gate several tools before it asks its question. The loop ends when
		// ln.Close() makes Accept return an error.
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			askQuestionCaptureServe(t, conn, wanted)
		}
	}()
	// Registered as a cleanup so that a t.Fatalf anywhere below still closes the
	// listener and JOINS the goroutine before the test binary moves on — an
	// un-joined server goroutine outliving the test is what -race reports.
	t.Cleanup(func() {
		_ = ln.Close()
		<-serverDone
	})

	// The mcp-config document, transcribed from renderMCPServersConfig rather than
	// called: that function lives in package main and is not importable here. Same
	// two keys, same argv, and the pyry binary and socket are both absolute.
	//
	// It goes in a t.TempDir() and NOT in the pinned HOME, which claude also reads
	// for its own configuration. Mode 0600: the file is not secret, but it is an
	// execution instruction claude obeys, and a world-writable one in a shared temp
	// directory is a local-privilege footgun in a suite that already writes there.
	cfgPath := filepath.Join(t.TempDir(), "mcp-approve.json")
	cfgDoc := fmt.Sprintf(`{"mcpServers":{"pyry_approve":{"command":%q,"args":["mcp-approve","-pyry-socket",%q]}}}`,
		pyryBin, socketPath)
	if err := os.WriteFile(cfgPath, []byte(cfgDoc), 0o600); err != nil {
		t.Fatalf("#1938: write the mcp-approve config %s: %v", cfgPath, err)
	}

	// permissionArgs(false, cfg)'s set, plus stream-json in and out. Three of these
	// are load-bearing beyond convention:
	//
	//   - --strict-mcp-config is non-negotiable. Without it a project or user
	//     .mcp.json can register a second pyry_approve server that shadows this
	//     one and answers allow, which would defeat AC 4 silently.
	//   - --permission-mode default is the only mode that consults the prompt tool.
	//   - THERE IS NO --allowed-tools AT ALL. An allowlisted tool is never routed
	//     to the prompt tool, so anything listed there could never park —
	//     AskUserQuestion in particular. Omitting the flag also gates every other
	//     tool claude reaches for, which is how "no gated tool executes" is
	//     satisfied by construction.
	//
	// And no --dangerously-skip-permissions: it disables the permission path
	// outright, which is exactly what this run needs.
	argv := []string{
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
		"--permission-prompt-tool", askQuestionCapturePromptTool,
		"--mcp-config", cfgPath,
		"--strict-mcp-config",
		"--permission-mode", "default",
		"--max-turns", "2",
		"--model", askQuestionCaptureModel,
	}

	// pyry's streamrunner envelope shape — the one known to be accepted by claude
	// on stream-json input.
	envelope, err := json.Marshal(map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": []map[string]any{{"type": "text", "text": askQuestionCapturePrompt}},
		},
	})
	if err != nil {
		t.Fatalf("#1938: marshal the stdin envelope: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), askQuestionCaptureBudget)
	defer cancel()

	cmd := exec.CommandContext(ctx, claudeBin, argv...)
	cmd.Dir = worktree
	// cmd.Env stays nil, so the child inherits this process's environment, which
	// WithWorktreeAuthenticated has already pinned (HOME to the worktree, plus the
	// credential variable). buildEnvWithRealHome is deliberately NOT used here: it
	// exists for the `go build` of pyry, and handing claude the operator's real
	// HOME would defeat the worktree isolation.
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("#1938: claude stdin pipe: %v", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("#1938: claude stdout pipe: %v", err)
	}
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		t.Fatalf("#1938: start claude: %v\nstderr:\n%s", err,
			truncateString(stderrBuf.String(), askQuestionCaptureStderrCap))
	}

	var reader askQuestionCaptureReader
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		askQuestionCaptureRead(bufio.NewScanner(stdoutPipe), &reader)
	}()

	if _, err := stdinPipe.Write(append(envelope, '\n')); err != nil {
		t.Logf("#1938: write the turn envelope: %v (continuing — the outcome switch is authoritative)", err)
	}
	if err := stdinPipe.Close(); err != nil {
		t.Logf("#1938: close claude stdin: %v (continuing)", err)
	}

	var (
		payload control.ApprovePayload
		gotCall bool
	)
	// THREE ARMS, AND THE STDOUT-EOF ONE IS NOT DECORATION. Measured 2026-09-01
	// against an overlay pointing --mcp-config at a nonexistent path: claude rejects
	// the config and exits in under a second, and with only the payload and deadline
	// arms this select then sat for the FULL askQuestionCaptureBudget before
	// reporting a spawn that had been dead the whole time. readerDone closes at
	// stdout EOF, which is that exit.
	select {
	case payload = <-wanted:
		gotCall = true
	case <-readerDone:
	case <-ctx.Done():
	}

	// AC 4's "stops once the wanted call is recorded". cmd.Wait() precedes the
	// reader join deliberately: Wait closes the stdout pipe, which is what unblocks
	// a reader parked on a descriptor some grandchild still holds. The init line
	// arrives long before either, so a truncated tail costs this run nothing.
	// Receiving from an already-closed readerDone is a no-op, so the join is correct
	// on all three arms.
	cancel()
	waitErr := cmd.Wait()
	<-readerDone

	// The race the EOF arm introduces, closed rather than argued away: claude cannot
	// exit before it reads the deny, and the payload is published BEFORE that deny is
	// encoded, so on a successful run both arms are ready at once and select picks
	// between them at random. A last non-blocking read is what keeps a recorded call
	// from being reported as a child that exited without one.
	if !gotCall {
		select {
		case payload = <-wanted:
			gotCall = true
		default:
		}
	}

	// A killed claude is EXPECTED on the success path, so waitErr is logged and
	// never fatal.
	exitCode := -1
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	if waitErr != nil {
		t.Logf("#1938: claude exited with %v (exit=%d); a kill is expected once the call is recorded",
			waitErr, exitCode)
	}
	if reader.scanErr != nil {
		t.Logf("#1938: stdout reader: %v", reader.scanErr)
	}

	switch {
	case gotCall:
		// Falls through to the fill below — the only branch that does.
	case !reader.sawInit:
		t.Fatalf("#1938: claude emitted no system/init line, so the spawn never initialised and "+
			"nothing can be said about whether %s was offered (exit=%d, deadline_tripped=%v). "+
			"This is a broken spawn, not a claude that declined to ask\nstderr:\n%s",
			askQuestionToolName, exitCode, ctx.Err() != nil,
			truncateString(stderrBuf.String(), askQuestionCaptureStderrCap))
	case !askQuestionCaptureOffers(reader.tools, askQuestionToolName):
		t.Fatalf("#1938: the system/init line of claude %q does NOT list %s among its tools %v, so "+
			"this spawn never offered the tool and no prompt could have elicited a call. Every "+
			"committed capture listing the tool was spawned with --permission-prompt-tool; this "+
			"spawn was too, so a claude release that stopped offering it is the finding to report",
			versionRaw, askQuestionToolName, reader.tools)
	default:
		t.Fatalf("#1938: claude %q OFFERED %s in its system/init tools array and no such call reached "+
			"the approve path within %s. The tool was available and the model did not call it: change "+
			"the prompt's directness, not the checks\nstderr:\n%s",
			versionRaw, askQuestionToolName, askQuestionCaptureBudget,
			truncateString(stderrBuf.String(), askQuestionCaptureStderrCap))
	}

	// --- fill, write, assert ------------------------------------------------------

	// ToolInput IS ASSIGNED, NEVER ROUND-TRIPPED. No generic decode, no
	// json.Compact, no re-marshal: encoding/json emits map keys in SORTED order, so
	// a decode-and-re-marshal silently rewrites the call's own key ordering and the
	// artifact stops being a recording. ApprovePayload.Input is already
	// json.RawMessage; assigning it is the whole job.
	//
	// ToolName COMES FROM THE PAYLOAD, never from askQuestionToolName. AC 1 requires
	// the shape check to read the capture's own field rather than the file name, so
	// a claude that spells the tool differently reddens through the tool_name check.
	//
	// ClaudeVersionSlug, NEVER ClaudeVersionRaw, is what the writer mints the
	// filename from: the raw field is a whole `claude --version` line.
	rec := &askQuestionFixtureRecord{
		ClaudeVersionRaw:  versionRaw,
		ClaudeVersionSlug: versionSlug(versionToken),
		ToolName:          payload.ToolName,
		ToolInput:         payload.Input,
	}

	artifactDir := filepath.Join(packageDir(t), "testdata")

	// newDropcapScanner, and deliberately NOT dropcapScanner{needles:
	// dropcapFixedNeedles()}. #1941's two callers use the fixed-only form because
	// their file bans the constructor — it reads os.Getenv and realHome, which would
	// make an offline table green or red depending on whose machine ran it. This run
	// has no such constraint and every reason to scan against the machine's actual
	// credentials and paths: it is the one run whose bytes a model wrote, and the
	// two credential classes the constructor arms are unarmed in the fixed-only
	// form.
	//
	// tempHome and workdir are the SAME DIRECTORY here, which is not a copy-paste
	// slip: WithWorktreeAuthenticated returns one directory serving as both the
	// pinned HOME and cmd.Dir.
	scanner := newDropcapScanner(worktree, artifactDir, worktree)

	// THE WRITE COMES FIRST AND THE SHAPE ASSERTION SECOND, and the order is
	// load-bearing: requireAskQuestionShape's own failure message tells the reader
	// to "read them from the committed artifact instead", which presupposes the
	// artifact exists. Asserting first would fail the run with the divergence
	// unreadable — the opposite of what a measurement ticket wants when the
	// measurement disagrees with the documented shape.
	//
	// writeAskQuestionFixture is the ONLY sanctioned route. A json.Marshal plus
	// os.WriteFile here would bypass the deny-scan and nothing would detect it:
	// finOfflineExecBans is per-file and this file execs, so it carries no entry.
	// Both calls are made from the test goroutine, which is what their t.Fatalf
	// requires.
	path := writeAskQuestionFixture(t, artifactDir, scanner, rec)
	requireAskQuestionShape(t, rec)

	// NEVER %v, %+v OR %#v THE RECORD: %+v prints tool_input, which is a live
	// child's bytes, into a run log this pipeline salvages. The path and the byte
	// count are the whole diagnostic.
	t.Logf("#1938: recorded a live %s call under claude %q: %s (%d bytes of tool_input)",
		payload.ToolName, versionRaw, path, len(rec.ToolInput))
}

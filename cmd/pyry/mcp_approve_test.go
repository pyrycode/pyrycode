package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/acp"
	"github.com/pyrycode/pyrycode/internal/control"
)

// --- fakes & helpers --------------------------------------------------------

// startApprovePeer stands up a Unix-socket accept loop that runs handle(conn)
// for each connection — a fake control-socket daemon. It returns the socket
// path. The listener and every handler goroutine are joined at test cleanup, so
// -race sees no goroutine outlive the test. Uses shortTempDir (auto_attach_test)
// to keep the socket path under the macOS sun_path limit.
func startApprovePeer(t *testing.T, handle func(net.Conn)) string {
	t.Helper()
	sock := filepath.Join(shortTempDir(t), "p.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return // listener closed at cleanup
			}
			wg.Add(1)
			go func(c net.Conn) {
				defer wg.Done()
				handle(c)
			}(conn)
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		wg.Wait()
	})
	return sock
}

// replyPeer decodes one control.Request and writes back resp. When gotReq is
// non-nil the decoded request is delivered to it (buffered cap-1) before the
// reply, so a test can assert the forwarded fields. Peer-side errors are ignored
// (matches startHasIDStub): a misbehaving peer surfaces as a client-side
// assertion failure, and calling t.Errorf from this goroutine could outlive the
// test.
func replyPeer(resp control.Response, gotReq chan<- control.Request) func(net.Conn) {
	return func(conn net.Conn) {
		defer func() { _ = conn.Close() }()
		var req control.Request
		if err := json.NewDecoder(conn).Decode(&req); err != nil {
			return
		}
		if gotReq != nil {
			gotReq <- req
		}
		_ = json.NewEncoder(conn).Encode(resp)
	}
}

// delayReplyPeer decodes one control.Request, waits d, then writes back resp —
// a daemon that takes its time reaching a verdict. Same error discipline as
// replyPeer: peer-side errors are ignored and t.Errorf is never called from this
// goroutine.
func delayReplyPeer(d time.Duration, resp control.Response) func(net.Conn) {
	return func(conn net.Conn) {
		defer func() { _ = conn.Close() }()
		var req control.Request
		if err := json.NewDecoder(conn).Decode(&req); err != nil {
			return
		}
		time.Sleep(d)
		_ = json.NewEncoder(conn).Encode(resp)
	}
}

// hangUpPeer decodes one control.Request and then closes without replying — the
// daemon exiting or closing the conn mid-approval. Decoding first is what makes
// it that case rather than "the dial lost": the request was accepted, and the
// connection ends with no verdict on it.
func hangUpPeer() func(net.Conn) {
	return func(conn net.Conn) {
		defer func() { _ = conn.Close() }()
		var req control.Request
		_ = json.NewDecoder(conn).Decode(&req)
	}
}

// holdPeer keeps the connection open and never replies — a daemon that is alive
// and holding the approval. It drains the conn until the client closes it. The
// client no longer carries a read deadline, so the ONLY thing that ends this
// handler is the client's ctx being cancelled (requestPatient's watcher wakes
// the read, and its deferred Close releases the drain). Every test using holdPeer
// must therefore cancel its ctx — an uncancelled one hangs the test and
// deadlocks startApprovePeer's wg.Wait cleanup.
func holdPeer() func(net.Conn) {
	return func(conn net.Conn) {
		defer func() { _ = conn.Close() }()
		_, _ = io.Copy(io.Discard, conn)
	}
}

// newApproveServer builds an approveServer pointed at sock with a discard
// logger. It delegates to the production constructor so the tests exercise the
// shape claude gets; that is safe for the parallel tests in this file because
// construction reads no environment (the client carries no per-call bound).
func newApproveServer(sock string) *approveServer {
	return newMCPApproveServer(sock, testLogger(io.Discard))
}

// callApprove invokes the tools/call handler directly with a well-formed
// tools/call params wrapping arguments, and returns the tool result. It asserts
// the handler never returns a JSON-RPC error (the fail-closed invariant: every
// terminus is a tool result, never an error frame).
func callApprove(t *testing.T, s *approveServer, arguments string) mcpToolResult {
	t.Helper()
	return invokeToolsCall(t, s, json.RawMessage(`{"name":"approve","arguments":`+arguments+`}`))
}

func invokeToolsCall(t *testing.T, s *approveServer, params json.RawMessage) mcpToolResult {
	t.Helper()
	return invokeToolsCallCtx(t, context.Background(), s, params)
}

// invokeToolsCallCtx is invokeToolsCall with the caller's ctx — the ctx toolsCall
// receives from Serve, carrying the signal cancellation in production.
func invokeToolsCallCtx(t *testing.T, ctx context.Context, s *approveServer, params json.RawMessage) mcpToolResult {
	t.Helper()
	res, err := s.toolsCall(ctx, params)
	if err != nil {
		t.Fatalf("toolsCall returned error, want nil (fail-closed invariant): %v", err)
	}
	tr, ok := res.(mcpToolResult)
	if !ok {
		t.Fatalf("toolsCall result type = %T, want mcpToolResult", res)
	}
	return tr
}

// assertVerdict checks the tool result carries exactly one text block whose body
// byte-equals wantText, and isError is false (a deny is a successful result).
func assertVerdict(t *testing.T, tr mcpToolResult, wantText string) {
	t.Helper()
	if tr.IsError {
		t.Error("isError = true, want false (a deny is a successful tool result)")
	}
	if len(tr.Content) != 1 {
		t.Fatalf("content len = %d, want 1", len(tr.Content))
	}
	if tr.Content[0].Type != "text" {
		t.Errorf("content[0].type = %q, want \"text\"", tr.Content[0].Type)
	}
	if tr.Content[0].Text != wantText {
		t.Errorf("verdict text mismatch:\n got %s\nwant %s", tr.Content[0].Text, wantText)
	}
}

// shortTempDir mirrors internal/control's helper: t.TempDir() lives under
// /var/folders/... on macOS which combined with long test names blows past the
// 104-byte sun_path limit. /tmp is short. Moved here from the auto-attach tests
// when #1348 deleted them.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "pyryapprove")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// testLogger builds a logger writing to w so tests can assert diagnostics land
// there and never on the stdout frame stream. Moved here from the ACP
// subcommand's tests when #1348 deleted them.
func testLogger(w io.Writer) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// jsonrpcReply is the decode-by-shape view of a single JSON-RPC response frame:
// id plus either a raw result or an error object. Result is raw so each test
// unmarshals it into the concrete result type or inspects it as bytes.
//
// It was called jsonrpcReply and lived in the ACP subcommand's tests until
// #1348 deleted that surface. Nothing about the shape was ACP-specific; it moved
// here with the one caller that outlived it.
type jsonrpcReply struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// driveMCP feeds one JSON-RPC frame through serveJSONRPCStdio with the approve server's
// handlers registered and runs to EOF, returning the single decoded reply and
// its raw line. Mirrors driveHandshake (acp_test.go).
func driveMCP(t *testing.T, s *approveServer, frame string) (jsonrpcReply, []byte) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var stdout, stderr bytes.Buffer
	if err := serveJSONRPCStdio(ctx, strings.NewReader(frame+"\n"), &stdout, testLogger(&stderr), s.register); err != nil {
		t.Fatalf("serveJSONRPCStdio: %v", err)
	}
	line := bytes.TrimRight(stdout.Bytes(), "\n")
	if len(line) == 0 {
		t.Fatalf("no response frame for %q", frame)
	}
	var r jsonrpcReply
	if err := json.Unmarshal(line, &r); err != nil {
		t.Fatalf("unmarshal reply %q: %v", line, err)
	}
	return r, line
}

// --- construction -----------------------------------------------------------

// daemonTimeoutMessage is the deny message permbridge's own approval timer
// produces (its reasonTimeout, unexported). The whole point of #1507 — kept here
// by different means — is that THIS message reaches claude rather than the
// client's generic "approval unavailable" whenever the daemon denies on its
// window.
const daemonTimeoutMessage = "approval request timed out"

// TestMCPApprove_DaemonMessageWinsAtEveryWindow pins AC-2. A daemon verdict that
// lands several multiples past its own configured window still passes through
// verbatim, because the client derives no bound from that window — at an
// overridden window as well as at the default. It is the behavioural kill for any
// mutant that reintroduces a client deadline from approvalTimeout(); the
// structural kill (no duration field, no margin constant, no duration source in
// the constructor) is what covers a bound too large to sit out in a test.
//
// The production constructor is the subject: newMCPApproveServer is what a live
// `pyry mcp-approve` builds.
//
// Content only, never latency — there is no upper bound to assert, so machine
// load cannot flake this. Serial by construction: t.Setenv forbids t.Parallel,
// the same constraint TestApprovalTimeout carries.
func TestMCPApprove_DaemonMessageWinsAtEveryWindow(t *testing.T) {
	// Far above every override row below, so each of those rows has the daemon
	// answering many multiples past its own window. Well under any plausible test
	// timeout, so the "content only" rule costs nothing in wall clock.
	const peerDelay = 250 * time.Millisecond

	cases := []struct {
		name  string
		env   string
		unset bool
	}{
		{name: "default window", unset: true},
		{name: "short override 20ms", env: "20ms"},
		{name: "short override 60ms", env: "60ms"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Carried forward from the retired client-deadline table: an override row
			// whose window equals mcpApprovalTimeout is vacuous, since approvalTimeout()
			// returns that value whether or not it reads the env. The unset row fails to
			// parse and is excluded by construction.
			if d, err := time.ParseDuration(tc.env); err == nil && d == mcpApprovalTimeout {
				t.Fatalf("override row env %q equals mcpApprovalTimeout (%v) — vacuous, it passes even if the env is ignored; pick a distinct window", tc.env, mcpApprovalTimeout)
			}

			// t.Setenv captures the prior value for restore-at-cleanup at call time,
			// so the Unsetenv below yields a genuine unset row (testing has no
			// t.Unsetenv) and an operator's real PYRY_APPROVAL_TIMEOUT still cannot
			// leak into it.
			t.Setenv(envApprovalTimeout, tc.env)
			if tc.unset {
				if err := os.Unsetenv(envApprovalTimeout); err != nil {
					t.Fatalf("Unsetenv: %v", err)
				}
			}

			sock := startApprovePeer(t, delayReplyPeer(peerDelay, control.Response{
				Approve: &control.ApproveResult{Behavior: "deny", Message: daemonTimeoutMessage},
			}))
			s := newMCPApproveServer(sock, testLogger(io.Discard))

			tr := callApprove(t, s, `{"tool_name":"Bash","input":{},"tool_use_id":"tu_window"}`)
			assertVerdict(t, tr, `{"behavior":"deny","message":"`+daemonTimeoutMessage+`"}`)
		})
	}
}

// --- handshake --------------------------------------------------------------

// TestMCPApprove_Initialize pins AC-1's handshake: initialize echoes the
// client's protocolVersion when sent, falls back to defaultMCPProtocolVersion
// when absent/empty, and always advertises the tools capability + the
// load-bearing serverInfo.name. A non-object params is a shape-gate reject.
func TestMCPApprove_Initialize(t *testing.T) {
	t.Parallel()
	s := newApproveServer("/nonexistent/p.sock")

	tests := []struct {
		name        string
		frame       string
		wantErr     bool
		wantVersion string
	}{
		{"echoes client version", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`, false, "2024-11-05"},
		{"echoes default-shaped version", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`, false, "2025-06-18"},
		{"fallback on absent params", `{"jsonrpc":"2.0","id":1,"method":"initialize"}`, false, defaultMCPProtocolVersion},
		{"fallback on empty object", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`, false, defaultMCPProtocolVersion},
		{"fallback on empty version", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":""}}`, false, defaultMCPProtocolVersion},
		{"non-object params rejected", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":[1,2,3]}`, true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			reply, line := driveMCP(t, s, tt.frame)
			if tt.wantErr {
				if reply.Error == nil {
					t.Fatalf("want error, got result %s", line)
				}
				if reply.Error.Code != acp.CodeInvalidParams {
					t.Fatalf("error code = %d, want CodeInvalidParams (%d)", reply.Error.Code, acp.CodeInvalidParams)
				}
				return
			}
			if reply.Error != nil {
				t.Fatalf("initialize returned error: %+v", *reply.Error)
			}
			var res mcpInitializeResult
			if err := json.Unmarshal(reply.Result, &res); err != nil {
				t.Fatalf("unmarshal result %s: %v", reply.Result, err)
			}
			if res.ProtocolVersion != tt.wantVersion {
				t.Errorf("protocolVersion = %q, want %q", res.ProtocolVersion, tt.wantVersion)
			}
			if res.ServerInfo.Name != mcpServerName {
				t.Errorf("serverInfo.name = %q, want %q", res.ServerInfo.Name, mcpServerName)
			}
			if !bytes.Contains(reply.Result, []byte(`"capabilities":{"tools":{}}`)) {
				t.Errorf("result %s missing capabilities.tools", reply.Result)
			}
		})
	}
}

// TestMCPApprove_ToolsList pins AC-1: tools/list advertises exactly one tool
// named "approve" (the load-bearing name that forms mcp__pyry_approve__approve)
// with an object input schema.
func TestMCPApprove_ToolsList(t *testing.T) {
	t.Parallel()
	s := newApproveServer("/nonexistent/p.sock")

	reply, _ := driveMCP(t, s, `{"jsonrpc":"2.0","id":7,"method":"tools/list"}`)
	if reply.Error != nil {
		t.Fatalf("tools/list returned error: %+v", *reply.Error)
	}
	var res mcpToolsListResult
	if err := json.Unmarshal(reply.Result, &res); err != nil {
		t.Fatalf("unmarshal result %s: %v", reply.Result, err)
	}
	if len(res.Tools) != 1 {
		t.Fatalf("tools len = %d, want 1", len(res.Tools))
	}
	if res.Tools[0].Name != approveToolName {
		t.Errorf("tool name = %q, want %q", res.Tools[0].Name, approveToolName)
	}
	if !bytes.Contains(res.Tools[0].InputSchema, []byte(`"tool_use_id"`)) {
		t.Errorf("inputSchema %s missing tool_use_id property", res.Tools[0].InputSchema)
	}
}

// --- forwarding round-trips -------------------------------------------------

// TestMCPApprove_AllowRoundTrip pins AC-2 (forwards tool_name/input/tool_use_id)
// and AC-4 (exact allow verdict JSON). The daemon's allow verdict — including
// its updatedInput — passes through byte-verbatim into the first text block.
func TestMCPApprove_AllowRoundTrip(t *testing.T) {
	t.Parallel()
	gotReq := make(chan control.Request, 1)
	sock := startApprovePeer(t, replyPeer(control.Response{
		Approve: &control.ApproveResult{
			Behavior:     "allow",
			UpdatedInput: json.RawMessage(`{"command":"ls -la"}`),
		},
	}, gotReq))
	s := newApproveServer(sock)

	tr := callApprove(t, s, `{"tool_name":"Bash","input":{"command":"ls"},"tool_use_id":"tu_alpha"}`)
	assertVerdict(t, tr, `{"behavior":"allow","updatedInput":{"command":"ls -la"}}`)

	// AC-2: the request the subcommand forwarded carries the mcp.approve verb and
	// the three fields verbatim (input byte-preserved, never parsed).
	req := <-gotReq
	if req.Verb != control.VerbMCPApprove {
		t.Errorf("forwarded verb = %q, want %q", req.Verb, control.VerbMCPApprove)
	}
	if req.Approve == nil {
		t.Fatal("forwarded request has nil Approve payload")
	}
	if req.Approve.ToolName != "Bash" || req.Approve.ToolUseID != "tu_alpha" {
		t.Errorf("forwarded (tool_name,tool_use_id) = (%q,%q), want (Bash,tu_alpha)",
			req.Approve.ToolName, req.Approve.ToolUseID)
	}
	if string(req.Approve.Input) != `{"command":"ls"}` {
		t.Errorf("forwarded input = %s, want %s", req.Approve.Input, `{"command":"ls"}`)
	}
}

// TestMCPApprove_DenyRoundTrip pins AC-4's deny golden shape: the daemon's deny
// verdict (behavior + message, no updatedInput) passes through byte-verbatim.
func TestMCPApprove_DenyRoundTrip(t *testing.T) {
	t.Parallel()
	sock := startApprovePeer(t, replyPeer(control.Response{
		Approve: &control.ApproveResult{Behavior: "deny", Message: "nope"},
	}, nil))
	s := newApproveServer(sock)

	tr := callApprove(t, s, `{"tool_name":"Bash","input":{"command":"rm -rf /"},"tool_use_id":"tu_beta"}`)
	assertVerdict(t, tr, `{"behavior":"deny","message":"nope"}`)
	if bytes.Contains([]byte(tr.Content[0].Text), []byte("updatedInput")) {
		t.Errorf("deny verdict %s must not carry updatedInput", tr.Content[0].Text)
	}
}

// --- fail-closed paths ------------------------------------------------------

// TestMCPApprove_SocketUnreachable_Deny pins AC-3's first half: with no daemon
// listening the tool result is a default-deny returned promptly, so claude's turn
// never hangs. There is no client-side per-call bound to credit this to — the
// dial carries its own (dialRetryBudget, ~1.5s, installed by dialWithRetry
// regardless of the ctx), which is what keeps this reachable under a patient read.
func TestMCPApprove_SocketUnreachable_Deny(t *testing.T) {
	t.Parallel()
	// A path with no listener → dial ENOENT → fast-fail deny.
	sock := filepath.Join(shortTempDir(t), "absent.sock")
	s := newApproveServer(sock)

	start := time.Now()
	tr := callApprove(t, s, `{"tool_name":"Bash","input":{},"tool_use_id":"tu_gamma"}`)
	elapsed := time.Since(start)

	assertVerdict(t, tr, `{"behavior":"deny","message":"approval unavailable"}`)
	if elapsed >= 3*time.Second {
		t.Errorf("unreachable-socket deny took %v, want fast-fail inside the dial's own budget", elapsed)
	}
}

// TestMCPApprove_ConnEndsWithoutVerdict_Deny pins AC-3's second half: a
// connection that ends with no verdict on it — the daemon exiting or closing
// mid-approval — denies AS SOON AS IT ENDS rather than after any window. The
// elapsed assertion is the criterion: what is being pinned here is the *when*.
func TestMCPApprove_ConnEndsWithoutVerdict_Deny(t *testing.T) {
	t.Parallel()
	sock := startApprovePeer(t, hangUpPeer())
	s := newApproveServer(sock)

	start := time.Now()
	tr := callApprove(t, s, `{"tool_name":"Bash","input":{},"tool_use_id":"tu_hangup"}`)
	elapsed := time.Since(start)

	assertVerdict(t, tr, `{"behavior":"deny","message":"approval unavailable"}`)
	if elapsed >= 3*time.Second {
		t.Errorf("conn-ended deny took %v, want it to land on the EOF rather than after a window", elapsed)
	}
}

// TestMCPApprove_SignalCancelMidApproval_Deny pins AC-4 at the surface claude
// sees: with the daemon still holding the approval, cancelling the Serve ctx (a
// SIGTERM in production) unblocks the forwarded call into the generic deny.
// It is the only test that catches a toolsCall which re-wraps the ctx in a way
// that drops the cancellation.
func TestMCPApprove_SignalCancelMidApproval_Deny(t *testing.T) {
	t.Parallel()
	sock := startApprovePeer(t, holdPeer())
	s := newApproveServer(sock)

	// No deadline — cancellation is the only bound, exactly as the Serve ctx
	// carries it. holdPeer's handler ends when this cancel closes the conn.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(100*time.Millisecond, cancel)

	start := time.Now()
	tr := invokeToolsCallCtx(t, ctx, s, json.RawMessage(
		`{"name":"approve","arguments":{"tool_name":"Bash","input":{},"tool_use_id":"tu_signal"}}`))
	elapsed := time.Since(start)

	assertVerdict(t, tr, `{"behavior":"deny","message":"approval unavailable"}`)
	if elapsed >= control.DialTimeout {
		t.Errorf("signal deny took %v, want the cancellation to unblock the read (not a fallback bound)", elapsed)
	}
}

// TestMCPApprove_DaemonError_Deny pins the fail-closed mapping of a daemon
// Response.Error (e.g. no approval registry configured / missing id) to a
// default-deny tool result.
func TestMCPApprove_DaemonError_Deny(t *testing.T) {
	t.Parallel()
	sock := startApprovePeer(t, replyPeer(control.Response{
		Error: "mcp.approve: no approval registry configured",
	}, nil))
	s := newApproveServer(sock)

	tr := callApprove(t, s, `{"tool_name":"Bash","input":{},"tool_use_id":"tu_delta"}`)
	assertVerdict(t, tr, `{"behavior":"deny","message":"approval unavailable"}`)
}

// TestMCPApprove_MalformedOrUnknown_Deny pins "malformed input must fail closed,
// never crash": a non-object params, an arguments value that is not the approve
// payload shape, and a tools/call for an unknown tool each yield a fixed-reason
// deny result and no panic.
func TestMCPApprove_MalformedOrUnknown_Deny(t *testing.T) {
	t.Parallel()
	s := newApproveServer("/nonexistent/p.sock") // never dialled on these paths

	tests := []struct {
		name     string
		params   string
		wantText string
	}{
		{"non-object params", `[1,2,3]`, `{"behavior":"deny","message":"malformed approval request"}`},
		{"arguments is array", `{"name":"approve","arguments":[1,2,3]}`, `{"behavior":"deny","message":"malformed approval request"}`},
		{"arguments absent", `{"name":"approve"}`, `{"behavior":"deny","message":"malformed approval request"}`},
		{"unknown tool", `{"name":"other","arguments":{"tool_name":"x","input":{},"tool_use_id":"y"}}`, `{"behavior":"deny","message":"unknown tool"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tr := invokeToolsCall(t, s, json.RawMessage(tt.params))
			assertVerdict(t, tr, tt.wantText)
		})
	}
}

// TestMCPApprove_MalformedInput_NoByteLeak pins the logging discipline on the
// fail-closed branch: the model-controlled arguments bytes (potentially
// secret-bearing) never enter a log field — only the fixed reason and behavior.
func TestMCPApprove_MalformedInput_NoByteLeak(t *testing.T) {
	t.Parallel()
	const secret = "SUPERSECRET_TOKEN_ac91"
	var stderr bytes.Buffer
	s := newMCPApproveServer("/nonexistent/p.sock", testLogger(&stderr))

	// Well-formed params, but arguments is an array carrying the secret — fails
	// the ApprovePayload unmarshal → deny, and must not log the offending bytes.
	tr := invokeToolsCall(t, s, json.RawMessage(`{"name":"approve","arguments":[{"secret":"`+secret+`"}]}`))
	assertVerdict(t, tr, `{"behavior":"deny","message":"malformed approval request"}`)
	if bytes.Contains(stderr.Bytes(), []byte(secret)) {
		t.Errorf("stderr leaked model-controlled bytes:\n%s", stderr.String())
	}
}

// TestControlApprove_PatientPastDialTimeout is AC-1's direct pin on the client
// helper: a verdict the daemon reaches later than the bound the call would
// otherwise carry still comes back as that verdict.
//
// Non-vacuous by construction — the peer's delay is expressed AS
// control.DialTimeout + 1s, which is exactly where the old undeadlined-ctx
// fallback truncated the read, so the assertion tracks that constant rather than
// a hand-picked number.
func TestControlApprove_PatientPastDialTimeout(t *testing.T) {
	t.Parallel()
	sock := startApprovePeer(t, delayReplyPeer(control.DialTimeout+1*time.Second, control.Response{
		Approve: &control.ApproveResult{Behavior: "allow"},
	}))

	res, err := control.Approve(context.Background(), sock, control.ApprovePayload{ToolUseID: "tu_patient"})
	if err != nil {
		t.Fatalf("Approve returned %v, want the daemon's late verdict", err)
	}
	if res == nil || res.Behavior != "allow" {
		t.Fatalf("verdict = %+v, want the daemon's allow", res)
	}
}

// TestControlApprove_CtxCancelUnblocks is AC-4's direct pin on the ctx watcher:
// against a daemon that holds the conn open and never replies, cancelling the ctx
// returns an error promptly. Without the watcher there is no mechanism at all to
// wake a Decode parked on the control socket.
func TestControlApprove_CtxCancelUnblocks(t *testing.T) {
	t.Parallel()
	sock := startApprovePeer(t, holdPeer())

	// No deadline: cancellation is the only thing that can end this call.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(100*time.Millisecond, cancel)

	start := time.Now()
	_, err := control.Approve(ctx, sock, control.ApprovePayload{ToolUseID: "tu_hold"})
	elapsed := time.Since(start)

	if err == nil {
		t.Error("want an error once the ctx is cancelled, got nil")
	}
	if elapsed >= control.DialTimeout {
		t.Errorf("Approve returned after %v, want the cancellation to wake the read well under %v", elapsed, control.DialTimeout)
	}
}

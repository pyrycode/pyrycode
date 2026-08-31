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

// holdPeer keeps the connection open and never replies, exercising the client
// read-deadline path (a wedged daemon). It drains the conn until the client
// closes it (control.Approve's request closes on return, at its read deadline),
// so the handler goroutine ends without a separate release signal — keeping the
// startApprovePeer wg.Wait cleanup deadlock-free.
func holdPeer() func(net.Conn) {
	return func(conn net.Conn) {
		defer func() { _ = conn.Close() }()
		_, _ = io.Copy(io.Discard, conn)
	}
}

// newApproveServer builds an approveServer pointed at sock with a discard
// logger and a generous per-call timeout (the fail-fast dial and canned peers
// resolve well inside it).
func newApproveServer(sock string) *approveServer {
	return &approveServer{socketPath: sock, timeout: 5 * time.Second, log: testLogger(io.Discard)}
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
	res, err := s.toolsCall(context.Background(), params)
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

// TestMCPApproveServer_ClientDeadline pins the per-call client read deadline
// newMCPApproveServer derives: approvalTimeout() + mcpApproveClientMargin, the
// same env-aware source the daemon hands the pending-approval registry. Raising
// PYRY_APPROVAL_TIMEOUT must raise both ends of the socket together instead of
// truncating the client at the constant's 10m30s (#1507). The first two rows are
// the production-unchanged pins: absent and unparseable both keep 10m30s.
//
// Those two rows are also the tree's only NUMERIC pin on the default window
// (#1909): TestApprovalTimeout's fallback rows compare against mcpApprovalTimeout
// symbolically, so they cannot see a value regression. Combined with the margin
// assertion below they force approvalTimeout() with the env absent to be exactly
// ten minutes.
//
// Serial by construction — t.Setenv forbids t.Parallel, the same constraint
// TestApprovalTimeout carries. It coexists with this file's parallel tests
// because those are released only after the package's serial phase.
func TestMCPApproveServer_ClientDeadline(t *testing.T) {
	cases := []struct {
		name  string
		env   string
		unset bool
		want  time.Duration
	}{
		{name: "unset falls back to the default window", unset: true, want: 10*time.Minute + 30*time.Second},
		{name: "unparseable falls back to the default window", env: "not-a-duration", want: 10*time.Minute + 30*time.Second},
		{name: "short override 2s", env: "2s", want: 32 * time.Second},
		// 12m, not the old 10m: an override row equal to the default proves nothing,
		// because approvalTimeout() returns that value whether or not it reads the
		// env. Above the default (so it also pins that the accessor does not clamp
		// down to it) and below streamTurnHoldTimeout, so the fixture does not read
		// as advice against the guide's fifteen-minute ceiling.
		{name: "generous override 12m", env: "12m", want: 12*time.Minute + 30*time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// The deterministic half of "no row can pass by reading the default": an
			// override row whose window equals mcpApprovalTimeout is vacuous, since
			// approvalTimeout() returns it on both the env and the fallback path. The
			// unset and unparseable rows fail to parse and are excluded by
			// construction, so no special-casing is needed.
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

			// Construction does no I/O — this socket is never dialled.
			s := newMCPApproveServer("/nonexistent/p.sock", testLogger(io.Discard))
			if s.timeout != tc.want {
				t.Errorf("client read deadline = %v, want %v", s.timeout, tc.want)
			}
			// The ordering invariant the margin exists for: the client deadline sits
			// exactly one margin PAST the daemon's window, so the daemon's informative
			// "approval request timed out" deny keeps winning the race against our
			// generic "approval unavailable". A negative delta here is the truncation
			// this ticket fixes, stated as a number.
			if got := s.timeout - approvalTimeout(); got != mcpApproveClientMargin {
				t.Errorf("client read deadline - approvalTimeout() = %v, want exactly %v (mcpApproveClientMargin)", got, mcpApproveClientMargin)
			}
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

// TestMCPApprove_SocketUnreachable_Deny pins AC-3: with no daemon listening the
// tool result is a default-deny returned well under the per-call timeout (the
// dial fails fast, ~dialRetryBudget), so claude's turn never hangs.
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
		t.Errorf("unreachable-socket deny took %v, want fast-fail well under the 5s timeout", elapsed)
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
	s := &approveServer{socketPath: "/nonexistent/p.sock", timeout: time.Second, log: testLogger(&stderr)}

	// Well-formed params, but arguments is an array carrying the secret — fails
	// the ApprovePayload unmarshal → deny, and must not log the offending bytes.
	tr := invokeToolsCall(t, s, json.RawMessage(`{"name":"approve","arguments":[{"secret":"`+secret+`"}]}`))
	assertVerdict(t, tr, `{"behavior":"deny","message":"malformed approval request"}`)
	if bytes.Contains(stderr.Bytes(), []byte(secret)) {
		t.Errorf("stderr leaked model-controlled bytes:\n%s", stderr.String())
	}
}

// TestControlApprove_UndeadlinedCtxReturns documents the deadline requirement
// control.Approve's doc-comment states: an undeadlined ctx falls back to
// DialTimeout for the read rather than blocking forever against a wedged daemon.
// Proves no-hang, not a specific latency.
func TestControlApprove_UndeadlinedCtxReturns(t *testing.T) {
	t.Parallel()
	sock := startApprovePeer(t, holdPeer())

	done := make(chan error, 1)
	go func() {
		_, err := control.Approve(context.Background(), sock, control.ApprovePayload{ToolUseID: "tu_hold"})
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Error("want a read-deadline error from the wedged peer, got nil")
		}
	case <-time.After(control.DialTimeout + 3*time.Second):
		t.Fatal("control.Approve blocked past the DialTimeout fallback — undeadlined ctx must not hang")
	}
}

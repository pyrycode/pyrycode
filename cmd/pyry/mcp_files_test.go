package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/pyrycode/pyrycode/internal/acp"
	"github.com/pyrycode/pyrycode/internal/control"
)

// --- fakes & helpers --------------------------------------------------------

// The control-socket peer fakes (startApprovePeer, replyPeer, hangUpPeer) and the
// shared MCP test scaffolding (shortTempDir, testLogger, jsonrpcReply,
// assertVerdict) live in mcp_approve_test.go. They are peer/transport shapes, not
// approve semantics, so this file reuses them rather than declaring second copies
// — the same reasoning by which mcp_files.go reuses that file's MCP frame types.
// Only startApprovePeer's name still says "approve"; renaming a helper with a
// live caller in another file is out of this ticket's scope.

// filesTestSessionID is the id a test server is constructed with — standing in
// for what the sibling ticket puts on the claude child's environment. Distinctive
// so the no-leak assertions can look for it by substring.
const filesTestSessionID = "sess-zzzidentity"

// filesTestPath and filesTestName are the model-supplied path and its leaf. Both
// are distinctive literals so TestMCPFiles_LogsCarryNoRequestBytes can assert
// their absence from stderr by substring rather than by exact match.
const (
	filesTestPath = "/zzzsecretdir/zzzsecretname.txt"
	filesTestName = "zzzsecretname.txt"
)

// newFilesServer builds a filesServer pointed at sock, bound to
// filesTestSessionID, with a discard logger. It delegates to the production
// constructor, which is safe for the parallel tests here precisely because that
// constructor reads no environment — the session id arrives as a parameter and
// runMCPFiles does the single os.Getenv.
func newFilesServer(sock string) *filesServer {
	return newMCPFilesServer(sock, filesTestSessionID, testLogger(io.Discard))
}

// callSendFile invokes the tools/call handler with a well-formed send_file frame
// wrapping arguments, and returns the tool result.
func callSendFile(t *testing.T, s *filesServer, arguments string) mcpToolResult {
	t.Helper()
	return invokeFilesCall(t, context.Background(), s, json.RawMessage(`{"name":"`+sendFileToolName+`","arguments":`+arguments+`}`))
}

// invokeFilesCall drives toolsCall directly with the caller's ctx — the ctx it
// receives from Serve, carrying the signal cancellation in production. It asserts
// the fail-closed invariant on every call: the handler returns a tool result and
// never a JSON-RPC error.
func invokeFilesCall(t *testing.T, ctx context.Context, s *filesServer, params json.RawMessage) mcpToolResult {
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

// driveFilesMCP feeds one JSON-RPC frame through serveJSONRPCStdio with this
// server's handlers registered and runs to EOF, returning the decoded reply. A
// near-twin of driveMCP (mcp_approve_test.go), which is typed to *approveServer;
// generalising that one would mean editing the permission bridge's tests, so this
// file carries its own.
func driveFilesMCP(t *testing.T, s *filesServer, frame string) (jsonrpcReply, []byte) {
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

// countingPeer wraps handle with a connection counter. It is what turns "the
// call was refused" into "the call was refused WITHOUT reaching the daemon" —
// the assertion that separates a genuine local guard from a forward that merely
// happened to fail.
func countingPeer(n *atomic.Int64, handle func(net.Conn)) func(net.Conn) {
	return func(conn net.Conn) {
		n.Add(1)
		if handle != nil {
			handle(conn)
			return
		}
		_ = conn.Close()
	}
}

// --- handshake --------------------------------------------------------------

// TestMCPFiles_Initialize pins AC-1's handshake: initialize echoes the client's
// protocolVersion when sent, falls back to defaultMCPProtocolVersion when
// absent/empty, and advertises the tools capability plus the load-bearing
// serverInfo.name (which forms mcp__pyry_files__send_file for the sibling
// ticket). A non-object params is a shape-gate reject.
func TestMCPFiles_Initialize(t *testing.T) {
	t.Parallel()
	s := newFilesServer("/nonexistent/p.sock")

	tests := []struct {
		name        string
		frame       string
		wantErr     bool
		wantVersion string
	}{
		{"echoes client version", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`, false, "2024-11-05"},
		{"fallback on absent params", `{"jsonrpc":"2.0","id":1,"method":"initialize"}`, false, defaultMCPProtocolVersion},
		{"fallback on empty object", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`, false, defaultMCPProtocolVersion},
		{"fallback on empty version", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":""}}`, false, defaultMCPProtocolVersion},
		{"non-object params rejected", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":[1,2,3]}`, true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			reply, line := driveFilesMCP(t, s, tt.frame)
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
			if res.ServerInfo.Name != mcpFilesServerName {
				t.Errorf("serverInfo.name = %q, want %q", res.ServerInfo.Name, mcpFilesServerName)
			}
			if res.ServerInfo.Name == mcpServerName {
				t.Error("serverInfo.name is the approve server's — this is its own server, not a second tool on pyry_approve")
			}
			if !bytes.Contains(reply.Result, []byte(`"capabilities":{"tools":{}}`)) {
				t.Errorf("result %s missing capabilities.tools", reply.Result)
			}
		})
	}
}

// TestMCPFiles_ToolsList pins AC-2: exactly one tool, named send_file, and the
// description plus schema that ARE the contract claude reads. Unlike the approve
// tool's advisory schema, nothing else tells the caller what to pass or under
// what constraint, so the confinement rule and the byte bound are asserted
// present — a description that drops them is a silently worse contract, not a
// cosmetic change.
func TestMCPFiles_ToolsList(t *testing.T) {
	t.Parallel()
	s := newFilesServer("/nonexistent/p.sock")

	reply, _ := driveFilesMCP(t, s, `{"jsonrpc":"2.0","id":7,"method":"tools/list"}`)
	if reply.Error != nil {
		t.Fatalf("tools/list returned error: %+v", *reply.Error)
	}
	var res mcpToolsListResult
	if err := json.Unmarshal(reply.Result, &res); err != nil {
		t.Fatalf("unmarshal result %s: %v", reply.Result, err)
	}
	if len(res.Tools) != 1 {
		t.Fatalf("tools len = %d, want exactly 1", len(res.Tools))
	}
	tool := res.Tools[0]
	if tool.Name != sendFileToolName {
		t.Errorf("tool name = %q, want %q", tool.Name, sendFileToolName)
	}
	if tool.Name == approveToolName {
		t.Error("tool name is the approve tool's — the two servers expose disjoint tools")
	}
	if !strings.Contains(tool.Description, "workspace") {
		t.Errorf("description does not state the workspace-confinement rule: %q", tool.Description)
	}
	// Derived from maxAttachFileBytes rather than spelled as a literal, so the
	// sentence cannot drift from the constant that enforces it.
	wantBound := fmt.Sprintf("%d MiB", maxAttachFileBytes>>20)
	if !strings.Contains(tool.Description, wantBound) {
		t.Errorf("description does not name the %s bound: %q", wantBound, tool.Description)
	}
	if !bytes.Contains(tool.InputSchema, []byte(`"path"`)) {
		t.Errorf("inputSchema %s missing the path property", tool.InputSchema)
	}
	if !bytes.Contains(tool.InputSchema, []byte(`"required":["path"]`)) {
		t.Errorf("inputSchema %s does not require path", tool.InputSchema)
	}
	// The schema must offer no way to name a session: the destination is the
	// environment's, never the caller's.
	if bytes.Contains(tool.InputSchema, []byte(`ession`)) {
		t.Errorf("inputSchema %s advertises a session field; the destination is never caller-chosen", tool.InputSchema)
	}
}

// --- forwarding round-trips -------------------------------------------------

// TestMCPFiles_HandOverRoundTrip pins AC-3's success half: the call reaches the
// daemon as VerbAttachFile carrying the server's session id and the path
// byte-verbatim, and the minted attachment id — the one value in this exchange
// documented safe to surface — comes back in the result.
func TestMCPFiles_HandOverRoundTrip(t *testing.T) {
	t.Parallel()
	const mintedID = "3f1a5c2e-0b7d-4e6a-9c81-2d4f6a8b0c13"

	gotReq := make(chan control.Request, 1)
	sock := startApprovePeer(t, replyPeer(control.Response{
		AttachFile: &control.AttachFileResult{AttachmentID: mintedID},
	}, gotReq))
	s := newFilesServer(sock)

	tr := callSendFile(t, s, `{"path":"`+filesTestPath+`"}`)
	assertVerdict(t, tr, fmt.Sprintf(sendFileHandedOver, mintedID))

	req := <-gotReq
	if req.Verb != control.VerbAttachFile {
		t.Errorf("verb = %q, want %q", req.Verb, control.VerbAttachFile)
	}
	if req.AttachFile == nil {
		t.Fatal("request carried no attachFile payload")
	}
	if req.AttachFile.SessionID != filesTestSessionID {
		t.Errorf("forwarded SessionID = %q, want the server's %q", req.AttachFile.SessionID, filesTestSessionID)
	}
	if req.AttachFile.Path != filesTestPath {
		t.Errorf("forwarded Path = %q, want it byte-verbatim: %q", req.AttachFile.Path, filesTestPath)
	}
}

// TestMCPFiles_SessionIDNeverFromToolInput pins AC-4's structural half. The
// arguments name a session of the caller's choosing, using the very JSON key
// control.AttachFilePayload tags SessionID with; the forwarded payload must still
// carry the server's.
//
// This is the row that reddens if sendFileArgs is ever replaced by
// control.AttachFilePayload as the unmarshal target. That mutant is not
// hypothetical: the payload type is right there, it has both fields, and reusing
// it looks like the tidier code — which is exactly why the guarantee is a type
// with no session field rather than an assignment that has to keep being correct.
func TestMCPFiles_SessionIDNeverFromToolInput(t *testing.T) {
	t.Parallel()
	const attacker = "sess-attacker-chosen"

	gotReq := make(chan control.Request, 1)
	sock := startApprovePeer(t, replyPeer(control.Response{
		AttachFile: &control.AttachFileResult{AttachmentID: "id"},
	}, gotReq))
	s := newFilesServer(sock)

	callSendFile(t, s, `{"path":"notes.md","sessionID":"`+attacker+`","session_id":"`+attacker+`"}`)

	req := <-gotReq
	if req.AttachFile == nil {
		t.Fatal("request carried no attachFile payload")
	}
	if req.AttachFile.SessionID == attacker {
		t.Fatalf("forwarded SessionID = %q — taken from the tool input, which lets a caller file into a conversation it has no part in", attacker)
	}
	if req.AttachFile.SessionID != filesTestSessionID {
		t.Errorf("forwarded SessionID = %q, want the environment-sourced %q", req.AttachFile.SessionID, filesTestSessionID)
	}
}

// TestMCPFiles_NoSessionIdentity_RefusesWithoutDialling pins AC-4's other half
// and the ticket's inert-on-landing state: with no session id on the environment
// the server still serves, and every call refuses. The connection count is the
// load-bearing assertion — it separates a local guard from a forward that merely
// fails, and an empty id forwarded onward is precisely what handleAttachFile and
// fileAttacher each refuse deliberately, both treating "" as a wildcard.
func TestMCPFiles_NoSessionIdentity_RefusesWithoutDialling(t *testing.T) {
	t.Parallel()
	var conns atomic.Int64
	sock := startApprovePeer(t, countingPeer(&conns, replyPeer(control.Response{
		AttachFile: &control.AttachFileResult{AttachmentID: "id"},
	}, nil)))

	s := newMCPFilesServer(sock, "", testLogger(io.Discard))
	tr := callSendFile(t, s, `{"path":"notes.md"}`)
	assertVerdict(t, tr, reasonSendFileNoSession)

	if n := conns.Load(); n != 0 {
		t.Errorf("peer accepted %d connections, want 0 — the guard must refuse before any dial, not forward an empty id", n)
	}
}

// TestMCPFiles_DaemonRefusalVerbatim pins AC-3's refusal half. The daemon's
// sentence reaches claude byte-exact, verb prefix included: not re-worded, not
// trimmed, not classified, nothing content-derived appended. That is what makes a
// refusal correctable — the confinement design's stated remedy is that claude
// reads "outside this conversation's workspace" and re-writes the file into the
// recorded workspace, which only works if the sentence survives this seam intact.
func TestMCPFiles_DaemonRefusalVerbatim(t *testing.T) {
	t.Parallel()
	// The shape handleAttachFile actually puts on the wire: its verb prefix plus
	// one of fileAttacher's static, content-free sentences.
	const refusal = "attachment.file: the path is outside this conversation's workspace"

	sock := startApprovePeer(t, replyPeer(control.Response{Error: refusal}, nil))
	s := newFilesServer(sock)

	tr := callSendFile(t, s, `{"path":"`+filesTestPath+`"}`)
	assertVerdict(t, tr, refusal)
}

// TestMCPFiles_TransportTermini pins AC-3's fail-closed rule for the paths where
// the daemon never answers. Each is a well-formed refusal result, never a
// JSON-RPC error and never a hang, so a claude turn cannot block on this tool.
func TestMCPFiles_TransportTermini(t *testing.T) {
	t.Parallel()

	t.Run("socket unreachable", func(t *testing.T) {
		t.Parallel()
		s := newFilesServer(shortTempDir(t) + "/absent.sock")
		tr := callSendFile(t, s, `{"path":"notes.md"}`)
		if tr.IsError {
			t.Error("isError = true, want false — a refusal is a successful tool result")
		}
		if len(tr.Content) != 1 || tr.Content[0].Text == "" {
			t.Fatalf("want one non-empty text block, got %+v", tr.Content)
		}
	})

	t.Run("conn ends with no reply", func(t *testing.T) {
		t.Parallel()
		sock := startApprovePeer(t, hangUpPeer())
		s := newFilesServer(sock)
		tr := callSendFile(t, s, `{"path":"notes.md"}`)
		if tr.IsError {
			t.Error("isError = true, want false")
		}
		if len(tr.Content) != 1 || tr.Content[0].Text == "" {
			t.Fatalf("want one non-empty text block, got %+v", tr.Content)
		}
	})

	t.Run("cancelled ctx", func(t *testing.T) {
		t.Parallel()
		sock := startApprovePeer(t, hangUpPeer())
		s := newFilesServer(sock)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		tr := invokeFilesCall(t, ctx, s, json.RawMessage(`{"name":"`+sendFileToolName+`","arguments":{"path":"notes.md"}}`))
		if tr.IsError {
			t.Error("isError = true, want false")
		}
		if len(tr.Content) != 1 || tr.Content[0].Text == "" {
			t.Fatalf("want one non-empty text block, got %+v", tr.Content)
		}
	})
}

// TestMCPFiles_MalformedAndMisdirected pins AC-2's fail-closed handler and AC-3's
// malformed-request rule. Every row is a refusal result and none reaches the
// daemon: the connection count is what makes the wrong-tool row a rejection
// rather than a forward that happened to be answered.
func TestMCPFiles_MalformedAndMisdirected(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		params string
		want   string
	}{
		{"params not JSON", `not-json`, reasonSendFileMalformed},
		{"params not an object", `[1,2,3]`, reasonSendFileMalformed},
		{"arguments not JSON", `{"name":"` + sendFileToolName + `","arguments":not-json}`, reasonSendFileMalformed},
		{"arguments not an object", `{"name":"` + sendFileToolName + `","arguments":["notes.md"]}`, reasonSendFileMalformed},
		{"unknown tool", `{"name":"read_file","arguments":{"path":"notes.md"}}`, reasonSendFileUnknownTool},
		{"the approve tool's name", `{"name":"` + approveToolName + `","arguments":{}}`, reasonSendFileUnknownTool},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var conns atomic.Int64
			sock := startApprovePeer(t, countingPeer(&conns, replyPeer(control.Response{
				AttachFile: &control.AttachFileResult{AttachmentID: "id"},
			}, nil)))
			s := newFilesServer(sock)

			tr := invokeFilesCall(t, context.Background(), s, json.RawMessage(tt.params))
			assertVerdict(t, tr, tt.want)
			if n := conns.Load(); n != 0 {
				t.Errorf("peer accepted %d connections, want 0 — a rejected call must not reach the daemon", n)
			}
		})
	}
}

// TestMCPFiles_LogsCarryNoRequestBytes pins AC-1's logging rule across every
// branch: no log line carries a host path, a filename, the session id, or the
// socket path. The session id earns its place in that list — it is the natural
// correlation key to reach for (mcp-approve's own decision log uses tool_use_id
// for exactly that), and it is the value that confers the authority to file into
// a conversation, on a stderr the forking claude captures.
//
// The two daemon-answering rows are the sharp ones: their RESULT text legitimately
// carries the daemon's sentence, and the assertion is that the log does not.
func TestMCPFiles_LogsCarryNoRequestBytes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// peer nil ⇒ no listener at all (the unreachable-socket branch).
		peer   func(net.Conn)
		params string
	}{
		{"success", replyPeer(control.Response{AttachFile: &control.AttachFileResult{AttachmentID: "id-1"}}, nil), `{"name":"` + sendFileToolName + `","arguments":{"path":"` + filesTestPath + `"}}`},
		{"daemon refusal", replyPeer(control.Response{Error: "attachment.file: the path is outside this conversation's workspace"}, nil), `{"name":"` + sendFileToolName + `","arguments":{"path":"` + filesTestPath + `"}}`},
		{"conn ends with no reply", hangUpPeer(), `{"name":"` + sendFileToolName + `","arguments":{"path":"` + filesTestPath + `"}}`},
		{"socket unreachable", nil, `{"name":"` + sendFileToolName + `","arguments":{"path":"` + filesTestPath + `"}}`},
		{"malformed params", nil, `not-json`},
		{"unknown tool", nil, `{"name":"` + filesTestName + `","arguments":{"path":"` + filesTestPath + `"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sock := shortTempDir(t) + "/absent.sock"
			if tt.peer != nil {
				sock = startApprovePeer(t, tt.peer)
			}
			var stderr bytes.Buffer
			s := newMCPFilesServer(sock, filesTestSessionID, testLogger(&stderr))

			invokeFilesCall(t, context.Background(), s, json.RawMessage(tt.params))

			got := stderr.String()
			for _, banned := range []struct{ what, value string }{
				{"the host path", filesTestPath},
				{"the filename", filesTestName},
				{"the session id", filesTestSessionID},
				{"the socket path", sock},
			} {
				if strings.Contains(got, banned.value) {
					t.Errorf("stderr carries %s (%q):\n%s", banned.what, banned.value, got)
				}
			}
		})
	}
}

// --- dispatch ---------------------------------------------------------------

// TestMCPFiles_DispatchArm pins AC-1's routing: runArgs has an mcp-files arm and
// it reaches runMCPFiles. Driving it with a positional is what makes the test
// possible at all — runMCPFiles rejects positionals before it touches os.Stdin,
// so the arm is proven without a TTY, a real fd, or a server that has to be shut
// down.
func TestMCPFiles_DispatchArm(t *testing.T) {
	t.Parallel()

	err := runArgs([]string{"pyry", "mcp-files", "bogus-positional"})
	if err == nil {
		t.Fatal("runArgs(mcp-files bogus-positional) = nil, want the positional rejection")
	}
	if !strings.Contains(err.Error(), "mcp-files") {
		t.Errorf("error = %q, want it to name the mcp-files subcommand", err)
	}
	if !strings.Contains(err.Error(), "bogus-positional") {
		t.Errorf("error = %q, want it to name the rejected positional", err)
	}
}

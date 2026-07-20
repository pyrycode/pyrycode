package control

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/permbridge"
)

// startServerWithApprovalRegistry mirrors startServerWithRekeyer but installs
// a REAL permbridge.Registry (a stdlib leaf — no fake needed) between
// NewServer and Serve. Tests drive the resolver by calling reg.Resolve
// directly, simulating #1080's modal-resolve consumer.
func startServerWithApprovalRegistry(t *testing.T, reg *permbridge.Registry, timeout time.Duration) (sock string, stop func()) {
	t.Helper()
	dir := shortTempDir(t)
	sock = filepath.Join(dir, "p.sock")

	srv := NewServer(sock, &fakeResolver{sess: &fakeSession{}}, nil, nil, nil, nil)
	srv.SetApprovalRegistry(reg, timeout)
	if err := srv.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()

	stop = func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Serve returned: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Errorf("Serve did not return after cancel")
		}
	}
	return sock, stop
}

// dialApprove dials the socket and sends one VerbMCPApprove request. The
// returned conn is closed on test cleanup; a generous deadline guards against
// a hung handler without interfering with the sub-second server timeouts the
// tests use. Callers read the response with readApproveResponse (or close the
// conn to simulate a disconnect).
func dialApprove(t *testing.T, sock string, payload *ApprovePayload) net.Conn {
	t.Helper()
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if err := json.NewEncoder(conn).Encode(Request{Verb: VerbMCPApprove, Approve: payload}); err != nil {
		t.Fatalf("encode request: %v", err)
	}
	return conn
}

func readApproveResponse(t *testing.T, conn net.Conn) Response {
	t.Helper()
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return resp
}

// waitForRegistered polls reg.Lookup until id is present — the handler has
// registered and is blocked on Await, so a resolver Resolve will land.
func waitForRegistered(t *testing.T, reg *permbridge.Registry, id string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := reg.Lookup(id); ok {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("approval id %q was never registered", id)
}

// waitForCleared polls reg.Lookup until id is absent, failing if it is still
// present after within. Used to prove eager cleanup happens well before the
// registry's own timeout would fire.
func waitForCleared(t *testing.T, reg *permbridge.Registry, id string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if _, ok := reg.Lookup(id); !ok {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("approval id %q still registered after %v (no eager cleanup)", id, within)
}

// rawEqual compares two json.RawMessage values ignoring insignificant
// whitespace (json.RawMessage round-trips through the wire byte-verbatim, but
// compacting makes the assertion robust to encoder spacing).
func rawEqual(got, want json.RawMessage) bool {
	var a, b bytes.Buffer
	if err := json.Compact(&a, got); err != nil {
		return false
	}
	if err := json.Compact(&b, want); err != nil {
		return false
	}
	return bytes.Equal(a.Bytes(), b.Bytes())
}

// TestServer_Approve_Allow exercises the allow path: the resolver (simulating
// #1080) resolves the pending entry with an Allow echoing the request input,
// and the verdict round-trips to the caller with UpdatedInput byte-verbatim.
func TestServer_Approve_Allow(t *testing.T) {
	t.Parallel()

	reg := permbridge.New()
	sock, stop := startServerWithApprovalRegistry(t, reg, 5*time.Second)
	defer stop()

	const id = "tuid-allow"
	input := json.RawMessage(`{"command":"ls -la","cwd":"/tmp"}`)
	conn := dialApprove(t, sock, &ApprovePayload{ToolName: "Bash", Input: input, ToolUseID: id})

	waitForRegistered(t, reg, id)
	if !reg.Resolve(id, permbridge.Allow(input)) {
		t.Fatal("Resolve returned false — entry was not live")
	}

	resp := readApproveResponse(t, conn)
	if resp.Error != "" {
		t.Fatalf("unexpected Response.Error = %q", resp.Error)
	}
	if resp.Approve == nil {
		t.Fatal("Response.Approve is nil, want allow verdict")
	}
	if resp.Approve.Behavior != permbridge.BehaviorAllow {
		t.Errorf("Behavior = %q, want %q", resp.Approve.Behavior, permbridge.BehaviorAllow)
	}
	if !rawEqual(resp.Approve.UpdatedInput, input) {
		t.Errorf("UpdatedInput = %s, want %s", resp.Approve.UpdatedInput, input)
	}
	if resp.Approve.Message != "" {
		t.Errorf("Message = %q, want empty on allow", resp.Approve.Message)
	}
}

// TestServer_Approve_DenyResolver exercises the resolver-deny path: the deny
// message round-trips and UpdatedInput stays empty.
func TestServer_Approve_DenyResolver(t *testing.T) {
	t.Parallel()

	reg := permbridge.New()
	sock, stop := startServerWithApprovalRegistry(t, reg, 5*time.Second)
	defer stop()

	const id = "tuid-deny"
	conn := dialApprove(t, sock, &ApprovePayload{
		ToolName:  "Bash",
		Input:     json.RawMessage(`{"command":"rm -rf /"}`),
		ToolUseID: id,
	})

	waitForRegistered(t, reg, id)
	if !reg.Resolve(id, permbridge.Deny("policy forbids this tool")) {
		t.Fatal("Resolve returned false — entry was not live")
	}

	resp := readApproveResponse(t, conn)
	if resp.Approve == nil {
		t.Fatal("Response.Approve is nil, want deny verdict")
	}
	if resp.Approve.Behavior != permbridge.BehaviorDeny {
		t.Errorf("Behavior = %q, want %q", resp.Approve.Behavior, permbridge.BehaviorDeny)
	}
	if resp.Approve.Message != "policy forbids this tool" {
		t.Errorf("Message = %q, want %q", resp.Approve.Message, "policy forbids this tool")
	}
	if len(resp.Approve.UpdatedInput) != 0 {
		t.Errorf("UpdatedInput = %s, want empty on deny", resp.Approve.UpdatedInput)
	}
}

// TestServer_Approve_DenyTimeout pins the "block for verdict" contract with no
// resolver: the registry's own timer denies after the (short) timeout with
// permbridge's fixed reason. This is the production state until #1080 wires a
// resolver.
func TestServer_Approve_DenyTimeout(t *testing.T) {
	t.Parallel()

	reg := permbridge.New()
	sock, stop := startServerWithApprovalRegistry(t, reg, 50*time.Millisecond)
	defer stop()

	conn := dialApprove(t, sock, &ApprovePayload{
		ToolName:  "Bash",
		Input:     json.RawMessage(`{"command":"echo hi"}`),
		ToolUseID: "tuid-timeout",
	})

	resp := readApproveResponse(t, conn)
	if resp.Approve == nil {
		t.Fatal("Response.Approve is nil, want timeout deny verdict")
	}
	if resp.Approve.Behavior != permbridge.BehaviorDeny {
		t.Errorf("Behavior = %q, want %q", resp.Approve.Behavior, permbridge.BehaviorDeny)
	}
	if resp.Approve.Message != "approval request timed out" {
		t.Errorf("Message = %q, want %q", resp.Approve.Message, "approval request timed out")
	}
	if len(resp.Approve.UpdatedInput) != 0 {
		t.Errorf("UpdatedInput = %s, want empty on deny", resp.Approve.UpdatedInput)
	}
}

// TestServer_Approve_DisconnectBeforeResolution proves the caller-disconnect
// path: with a long timeout and no resolver, closing the conn mid-wait must
// eagerly delete the pending entry (fail-closed deny) well before the timeout
// would fire — implicitly proving no hung goroutine parks the entry.
func TestServer_Approve_DisconnectBeforeResolution(t *testing.T) {
	t.Parallel()

	reg := permbridge.New()
	// Timeout deliberately far larger than the cleanup deadline below, so a
	// pass can only come from disconnect-driven eager cleanup, never the timer.
	sock, stop := startServerWithApprovalRegistry(t, reg, 30*time.Second)
	defer stop()

	const id = "tuid-disconnect"
	conn := dialApprove(t, sock, &ApprovePayload{
		ToolName:  "Bash",
		Input:     json.RawMessage(`{"command":"sleep 1"}`),
		ToolUseID: id,
	})

	waitForRegistered(t, reg, id)
	if err := conn.Close(); err != nil {
		t.Fatalf("close conn: %v", err)
	}
	waitForCleared(t, reg, id, 2*time.Second)
}

// TestServer_Approve_Unavailable covers the nil-registry degrade (v1 /
// foreground, and production until the daemon composition wires it): the
// verb replies with a wire error, not a panic. startServer never calls
// SetApprovalRegistry, so approvals stays nil.
func TestServer_Approve_Unavailable(t *testing.T) {
	t.Parallel()

	sock, stop := startServer(t, &fakeResolver{sess: &fakeSession{}})
	defer stop()

	conn := dialApprove(t, sock, &ApprovePayload{
		ToolName:  "Bash",
		Input:     json.RawMessage(`{"command":"echo hi"}`),
		ToolUseID: "tuid-unavail",
	})

	resp := readApproveResponse(t, conn)
	if !strings.Contains(resp.Error, "no approval registry configured") {
		t.Errorf("Response.Error = %q, want it to contain %q", resp.Error, "no approval registry configured")
	}
	if resp.Approve != nil {
		t.Errorf("Response.Approve = %+v, want nil on unavailable", resp.Approve)
	}
}

// TestServer_Approve_MissingToolUseID covers the empty-id guard. The guard
// must fire BEFORE Register, so the response is a wire error (not a deny
// verdict): resp.Approve == nil proves nothing was registered — had it fallen
// through to Register, permbridge would have fail-closed with a deny in
// resp.Approve instead.
func TestServer_Approve_MissingToolUseID(t *testing.T) {
	t.Parallel()

	reg := permbridge.New()
	sock, stop := startServerWithApprovalRegistry(t, reg, 5*time.Second)
	defer stop()

	conn := dialApprove(t, sock, &ApprovePayload{
		ToolName:  "Bash",
		Input:     json.RawMessage(`{"command":"echo hi"}`),
		ToolUseID: "",
	})

	resp := readApproveResponse(t, conn)
	if !strings.Contains(resp.Error, "missing tool_use_id") {
		t.Errorf("Response.Error = %q, want it to contain %q", resp.Error, "missing tool_use_id")
	}
	if resp.Approve != nil {
		t.Errorf("Response.Approve = %+v, want nil (guard fired before Register)", resp.Approve)
	}
}

// TestServer_Approve_ShutdownUnblocks guards the s.closedCh path: a daemon
// shutdown mid-wait must deny the pending request and let the handler return
// promptly (so handleWG.Wait does not stall for the approval timeout) without
// leaking the entry. Built inline so the test drives ctx cancellation and the
// Serve join directly.
func TestServer_Approve_ShutdownUnblocks(t *testing.T) {
	t.Parallel()

	reg := permbridge.New()
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "p.sock")

	srv := NewServer(sock, &fakeResolver{sess: &fakeSession{}}, nil, nil, nil, nil)
	// Long timeout: only the shutdown path can resolve this within the test.
	srv.SetApprovalRegistry(reg, 30*time.Second)
	if err := srv.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()

	const id = "tuid-shutdown"
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if err := json.NewEncoder(conn).Encode(Request{
		Verb:    VerbMCPApprove,
		Approve: &ApprovePayload{ToolName: "Bash", Input: json.RawMessage(`{"command":"echo hi"}`), ToolUseID: id},
	}); err != nil {
		t.Fatalf("encode request: %v", err)
	}

	waitForRegistered(t, reg, id)
	cancel() // shutdown mid-wait

	resp := readApproveResponse(t, conn)
	if resp.Approve == nil {
		t.Fatal("Response.Approve is nil, want shutdown deny verdict")
	}
	if resp.Approve.Behavior != permbridge.BehaviorDeny {
		t.Errorf("Behavior = %q, want %q", resp.Approve.Behavior, permbridge.BehaviorDeny)
	}
	if resp.Approve.Message != "daemon shutting down" {
		t.Errorf("Message = %q, want %q", resp.Approve.Message, "daemon shutting down")
	}

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve returned: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return after shutdown (handler stalled for approval timeout?)")
	}
	waitForCleared(t, reg, id, 2*time.Second)
}

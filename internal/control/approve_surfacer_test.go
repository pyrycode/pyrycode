package control

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/permbridge"
)

// startServerWithApprovalSurfacer mirrors startServerWithApprovalRegistry but also
// installs a #1080 approval surfacer, so a parked approve raises a client-facing
// modal and the returned retire runs on every terminal Await path.
func startServerWithApprovalSurfacer(t *testing.T, reg *permbridge.Registry, timeout time.Duration, surface func(permbridge.Request) func()) (sock string, stop func()) {
	t.Helper()
	dir := shortTempDir(t)
	sock = filepath.Join(dir, "p.sock")

	srv := NewServer(sock, &fakeResolver{sess: &fakeSession{}}, nil, nil, nil, nil)
	srv.SetApprovalRegistry(reg, timeout)
	srv.SetApprovalSurfacer(surface)
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

// TestServer_Approve_SurfacerInvokedAndRetired proves the #1080 control seam: a
// parked approve invokes the surfacer exactly once with the forwarded request, and
// the returned retire fires exactly once on the terminal Await return (driven here
// by a resolver Allow). The duplicate-Register early return never reaches this seam
// (it returns before Surface), and a nil surfacer leaves the pre-#1080 behaviour —
// covered by the existing timeout/allow/deny approve tests, which install none.
func TestServer_Approve_SurfacerInvokedAndRetired(t *testing.T) {
	t.Parallel()

	reg := permbridge.New()
	var surfaced, retired atomic.Int32
	reqCh := make(chan permbridge.Request, 1)
	surface := func(req permbridge.Request) func() {
		surfaced.Add(1)
		reqCh <- req
		return func() { retired.Add(1) }
	}
	// Timeout far larger than the test so a pass can only come from the resolver.
	sock, stop := startServerWithApprovalSurfacer(t, reg, 30*time.Second, surface)
	defer stop()

	const id = "tuid-surface"
	input := json.RawMessage(`{"command":"ls"}`)
	conn := dialApprove(t, sock, &ApprovePayload{ToolName: "Bash", Input: input, ToolUseID: id})

	// Once the handler has parked (registered), the surfacer has been invoked with
	// the forwarded request (the two are sequential in the handler).
	waitForRegistered(t, reg, id)
	select {
	case got := <-reqCh:
		if got.ToolUseID != id || got.ToolName != "Bash" {
			t.Errorf("surfaced req = %+v, want ToolUseID=%q ToolName=Bash", got, id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("surfacer was not invoked")
	}
	if n := surfaced.Load(); n != 1 {
		t.Errorf("surfaced = %d, want 1", n)
	}

	// Resolve to drive Await → handler returns → deferred retire fires.
	if !reg.Resolve(id, permbridge.Allow(input)) {
		t.Fatal("Resolve returned false — entry was not live")
	}
	resp := readApproveResponse(t, conn)
	if resp.Approve == nil || resp.Approve.Behavior != permbridge.BehaviorAllow {
		t.Fatalf("resp = %+v, want an allow verdict", resp)
	}

	// retire runs on the handler's return, after the response is encoded.
	deadline := time.Now().Add(2 * time.Second)
	for retired.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if n := retired.Load(); n != 1 {
		t.Errorf("retired = %d, want 1", n)
	}
}

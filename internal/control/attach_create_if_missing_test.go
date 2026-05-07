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

	"github.com/pyrycode/pyrycode/internal/sessions"
)

// TestAttach_CreateIfMissingOnWire pins the byte shape of the new
// CreateIfMissing field on AttachPayload. Companion to
// TestAttach_WireBackCompat_EmptySessionID — omitempty when false guarantees
// pre-1.3b clients (which don't know the field) and post-1.3b clients
// passing the default false produce byte-identical wire output during the
// rollover window. When set, the field marshals to plain JSON true.
func TestAttach_CreateIfMissingOnWire(t *testing.T) {
	t.Parallel()

	off, err := json.Marshal(AttachPayload{Cols: 80, Rows: 24, SessionID: "abc"})
	if err != nil {
		t.Fatalf("Marshal off: %v", err)
	}
	if bytes.Contains(off, []byte("createIfMissing")) {
		t.Errorf("CreateIfMissing=false leaked onto the wire: %s", off)
	}

	on, err := json.Marshal(AttachPayload{Cols: 80, Rows: 24, SessionID: "abc", CreateIfMissing: true})
	if err != nil {
		t.Fatalf("Marshal on: %v", err)
	}
	if !bytes.Contains(on, []byte(`"createIfMissing":true`)) {
		t.Errorf("CreateIfMissing=true did not marshal as expected: %s", on)
	}
}

// startServerWithResolverAndSessioner is a small wrapper combining a custom
// SessionResolver and a Sessioner. The existing helpers each take one or
// the other; the create-if-missing path needs both (Sessioner.GetOrCreate
// for dispatch, SessionResolver.Lookup for the post-create attach).
func startServerWithResolverAndSessioner(t *testing.T, r SessionResolver, s Sessioner) (sock string, stop func()) {
	t.Helper()
	dir := shortTempDir(t)
	sock = filepath.Join(dir, "p.sock")

	srv := NewServer(sock, r, nil, nil, nil, s)
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

// sendAttachWith sends an attach handshake with the given payload and
// returns the decoded ack. Mirrors sendAttach but lets the test specify
// CreateIfMissing.
func sendAttachWith(t *testing.T, sock string, payload AttachPayload) Response {
	t.Helper()
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if err := json.NewEncoder(conn).Encode(Request{
		Verb:   VerbAttach,
		Attach: &payload,
	}); err != nil {
		t.Fatalf("encode handshake: %v", err)
	}
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		t.Fatalf("decode ack: %v", err)
	}
	return resp
}

// TestServer_AttachCreateIfMissing_DispatchesToGetOrCreate asserts the
// handler routes through Sessioner.GetOrCreate (NOT ResolveID) when the
// flag is set, and that the rest of the attach flow runs unchanged from
// there: Lookup against the GetOrCreate-returned id, Activate, ack OK,
// byte stream forwards.
func TestServer_AttachCreateIfMissing_DispatchesToGetOrCreate(t *testing.T) {
	t.Parallel()

	const reqID sessions.SessionID = "11111111-2222-4333-8444-555555555555"
	const winnerID sessions.SessionID = "11111111-2222-4333-8444-555555555555"

	counter := &sessionCounter{}
	sess := &fakeSession{attachFn: counter.attach}

	resolver := &multiSessionResolver{
		sessions: map[sessions.SessionID]*fakeSession{
			winnerID: sess,
		},
		// resolveFn left at default — but we assert below that resolves
		// is empty, so any accidental call is caught.
	}

	sessioner := &fakeSessioner{returnID: winnerID}
	sock, stop := startServerWithResolverAndSessioner(t, resolver, sessioner)
	defer stop()

	resp := sendAttachWith(t, sock, AttachPayload{
		SessionID:       string(reqID),
		CreateIfMissing: true,
	})
	if resp.Error != "" {
		t.Fatalf("ack carried error: %q", resp.Error)
	}
	if !resp.OK {
		t.Fatalf("ack OK=false, want true")
	}

	calls := sessioner.recordedGetOrCreates()
	if len(calls) != 1 {
		t.Fatalf("GetOrCreate calls = %d, want 1", len(calls))
	}
	if calls[0].ID != reqID {
		t.Errorf("GetOrCreate id = %q, want %q", calls[0].ID, reqID)
	}
	if calls[0].Label != "" {
		t.Errorf("GetOrCreate label = %q, want \"\" (handler passes empty label)", calls[0].Label)
	}

	resolves, lookups := resolver.callTrace()
	if len(resolves) != 0 {
		t.Errorf("ResolveID calls = %v, want empty (createIfMissing path bypasses ResolveID)", resolves)
	}
	if len(lookups) != 1 || lookups[0] != winnerID {
		t.Errorf("Lookup calls = %v, want [%q]", lookups, winnerID)
	}
}

// TestServer_AttachCreateIfMissing_NoSessioner — when the daemon was built
// without a Sessioner (foreground mode plumbing), the createIfMissing path
// surfaces a clean error before any other work. Symmetric with the
// existing "sessions.new: no sessioner configured" guard.
func TestServer_AttachCreateIfMissing_NoSessioner(t *testing.T) {
	t.Parallel()

	resolver := &multiSessionResolver{
		sessions: map[sessions.SessionID]*fakeSession{},
	}
	sock, stop := startServerWithResolverAndSessioner(t, resolver, nil)
	defer stop()

	resp := sendAttachWith(t, sock, AttachPayload{
		SessionID:       "11111111-2222-4333-8444-555555555555",
		CreateIfMissing: true,
	})
	const want = "attach: no sessioner configured"
	if resp.Error != want {
		t.Errorf("Error = %q, want %q", resp.Error, want)
	}
	if resp.OK {
		t.Errorf("ack OK=true, want false")
	}

	resolves, lookups := resolver.callTrace()
	if len(resolves) != 0 {
		t.Errorf("ResolveID calls = %v, want empty", resolves)
	}
	if len(lookups) != 0 {
		t.Errorf("Lookup calls = %v, want empty (no work past the no-sessioner guard)", lookups)
	}
}

// TestServer_AttachCreateIfMissing_InvalidID — when GetOrCreate rejects
// the supplied id (empty / malformed / non-UUIDv4), the typed error
// surfaces verbatim under the "attach: " prefix. The handler does not
// fall through to Lookup.
func TestServer_AttachCreateIfMissing_InvalidID(t *testing.T) {
	t.Parallel()

	resolver := &multiSessionResolver{
		sessions: map[sessions.SessionID]*fakeSession{},
	}
	sessioner := &fakeSessioner{returnErr: sessions.ErrInvalidSessionID}
	sock, stop := startServerWithResolverAndSessioner(t, resolver, sessioner)
	defer stop()

	resp := sendAttachWith(t, sock, AttachPayload{
		SessionID:       "", // empty triggers the validator at the Pool boundary in real code
		CreateIfMissing: true,
	})
	if resp.OK {
		t.Errorf("ack OK=true, want false")
	}
	if !strings.Contains(resp.Error, "invalid session id") {
		t.Errorf("Error = %q, want substring %q", resp.Error, "invalid session id")
	}
	if !strings.HasPrefix(resp.Error, "attach: ") {
		t.Errorf("Error = %q, want \"attach: \" prefix", resp.Error)
	}

	calls := sessioner.recordedGetOrCreates()
	if len(calls) != 1 {
		t.Errorf("GetOrCreate calls = %d, want 1", len(calls))
	}
	_, lookups := resolver.callTrace()
	if len(lookups) != 0 {
		t.Errorf("Lookup calls = %v, want empty (Lookup must not run after GetOrCreate error)", lookups)
	}
}


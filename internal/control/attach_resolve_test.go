package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/sessions"
)

// TestAttach_WireBackCompat_EmptySessionID is a byte-equality regression
// guard for the omitempty tag on AttachPayload.SessionID. v0.5.x clients
// don't know the field; if a v0.7.x server (or a v0.7.x → v0.5.x server
// roundtrip via a captured payload) ever marshalled "sessionID":"" into
// the wire output, those clients would still parse it but the v0.5.x
// baseline test fixtures encoded elsewhere would diverge. Pin the
// v0.5.x byte form here.
func TestAttach_WireBackCompat_EmptySessionID(t *testing.T) {
	t.Parallel()

	got, err := json.Marshal(AttachPayload{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	want := []byte(`{"cols":80,"rows":24}`)
	if !bytes.Equal(got, want) {
		t.Errorf("Marshal AttachPayload{Cols:80,Rows:24} = %q, want %q (omitempty on SessionID is load-bearing)", got, want)
	}
}

// multiSessionResolver routes Lookup to one of several fakeSessions keyed
// by SessionID. ResolveID is driven by an explicit resolveFn so tests can
// stage UUID, prefix, ambiguous, and not-found behaviours independently.
type multiSessionResolver struct {
	mu        sync.Mutex
	sessions  map[sessions.SessionID]*fakeSession
	resolveFn func(arg string) (sessions.SessionID, error)
	resolves  []string
	lookups   []sessions.SessionID
}

func (r *multiSessionResolver) Lookup(id sessions.SessionID) (Session, error) {
	r.mu.Lock()
	r.lookups = append(r.lookups, id)
	s, ok := r.sessions[id]
	r.mu.Unlock()
	if !ok {
		return nil, sessions.ErrSessionNotFound
	}
	return s, nil
}

func (r *multiSessionResolver) ResolveID(arg string) (sessions.SessionID, error) {
	r.mu.Lock()
	r.resolves = append(r.resolves, arg)
	fn := r.resolveFn
	r.mu.Unlock()
	if fn == nil {
		return sessions.SessionID(arg), nil
	}
	return fn(arg)
}

func (r *multiSessionResolver) callTrace() (resolves []string, lookups []sessions.SessionID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	resolves = append(resolves, r.resolves...)
	lookups = append(lookups, r.lookups...)
	return resolves, lookups
}

// recordingAttachSession is a fakeSession that exposes an attachCalls
// counter for tests that assert "the bridge was never opened" on the
// error path. Embeds the existing fakeSession provider — the counter is
// incremented inside the provided attachFn so it observes only successful
// reach-attach calls (the existing TestServer_AttachOnForegroundSession
// shape).
type sessionCounter struct {
	mu          sync.Mutex
	attachCalls int
	attached    bool
	receivedIn  []byte
}

func (c *sessionCounter) attach(in io.Reader, out io.Writer) (<-chan struct{}, error) {
	c.mu.Lock()
	c.attachCalls++
	if c.attached {
		c.mu.Unlock()
		return nil, errors.New("already attached")
	}
	c.attached = true
	c.mu.Unlock()

	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		for {
			n, err := in.Read(buf)
			if n > 0 {
				c.mu.Lock()
				c.receivedIn = append(c.receivedIn, buf[:n]...)
				c.mu.Unlock()
			}
			if err != nil {
				break
			}
		}
		c.mu.Lock()
		c.attached = false
		c.mu.Unlock()
	}()
	return done, nil
}

func (c *sessionCounter) calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.attachCalls
}

// startServerForResolver wires up a Server backed by the given resolver
// and returns the socket path plus a stop function.
func startServerForResolver(t *testing.T, r SessionResolver) (sock string, stop func()) {
	t.Helper()
	dir := shortTempDir(t)
	sock = filepath.Join(dir, "p.sock")

	srv := NewServer(sock, r, nil, nil, nil, nil)
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

// sendAttach dials the socket, sends an attach handshake with the given
// SessionID, and returns the decoded ack response. The connection is
// closed after the response is read.
func sendAttach(t *testing.T, sock, sessionID string) Response {
	t.Helper()
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if err := json.NewEncoder(conn).Encode(Request{
		Verb:   VerbAttach,
		Attach: &AttachPayload{SessionID: sessionID},
	}); err != nil {
		t.Fatalf("encode handshake: %v", err)
	}
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		t.Fatalf("decode ack: %v", err)
	}
	return resp
}

// TestAttach_ResolvesByFullUUID verifies that a full UUID in
// AttachPayload.SessionID flows through ResolveID and lands on the
// matching session — not the bootstrap.
func TestAttach_ResolvesByFullUUID(t *testing.T) {
	t.Parallel()

	const bootstrapID sessions.SessionID = "00000000-0000-4000-8000-00000000aaaa"
	const mintedID sessions.SessionID = "11111111-2222-4333-8444-555555555555"

	bootstrapCounter := &sessionCounter{}
	mintedCounter := &sessionCounter{}
	bootstrapSess := &fakeSession{attachFn: bootstrapCounter.attach}
	mintedSess := &fakeSession{attachFn: mintedCounter.attach}

	resolver := &multiSessionResolver{
		sessions: map[sessions.SessionID]*fakeSession{
			bootstrapID: bootstrapSess,
			mintedID:    mintedSess,
		},
		resolveFn: func(arg string) (sessions.SessionID, error) {
			if arg == "" {
				return bootstrapID, nil
			}
			if sessions.SessionID(arg) == mintedID || sessions.SessionID(arg) == bootstrapID {
				return sessions.SessionID(arg), nil
			}
			return "", sessions.ErrSessionNotFound
		},
	}

	sock, stop := startServerForResolver(t, resolver)
	defer stop()

	resp := sendAttach(t, sock, string(mintedID))
	if resp.Error != "" {
		t.Fatalf("ack carried error: %q", resp.Error)
	}
	if !resp.OK {
		t.Fatalf("ack OK=false, want true")
	}

	resolves, lookups := resolver.callTrace()
	if len(resolves) != 1 || resolves[0] != string(mintedID) {
		t.Errorf("ResolveID calls = %v, want exactly [%q]", resolves, mintedID)
	}
	if len(lookups) != 1 || lookups[0] != mintedID {
		t.Errorf("Lookup calls = %v, want exactly [%q]", lookups, mintedID)
	}
	if mintedCounter.calls() != 1 {
		t.Errorf("minted session attachCalls = %d, want 1", mintedCounter.calls())
	}
	if bootstrapCounter.calls() != 0 {
		t.Errorf("bootstrap session attachCalls = %d, want 0 (full UUID must not fall through to bootstrap)", bootstrapCounter.calls())
	}
}

// TestAttach_ResolvesByUniquePrefix verifies that a unique prefix flows
// through ResolveID and lands on the matching session. The prefix shape
// is opaque to handleAttach — ResolveID owns the matching policy.
func TestAttach_ResolvesByUniquePrefix(t *testing.T) {
	t.Parallel()

	const bootstrapID sessions.SessionID = "00000000-0000-4000-8000-00000000aaaa"
	const mintedID sessions.SessionID = "11111111-2222-4333-8444-555555555555"
	const prefix = "11111111"

	bootstrapCounter := &sessionCounter{}
	mintedCounter := &sessionCounter{}
	bootstrapSess := &fakeSession{attachFn: bootstrapCounter.attach}
	mintedSess := &fakeSession{attachFn: mintedCounter.attach}

	resolver := &multiSessionResolver{
		sessions: map[sessions.SessionID]*fakeSession{
			bootstrapID: bootstrapSess,
			mintedID:    mintedSess,
		},
		resolveFn: func(arg string) (sessions.SessionID, error) {
			if arg == "" {
				return bootstrapID, nil
			}
			if arg == prefix {
				return mintedID, nil
			}
			return "", sessions.ErrSessionNotFound
		},
	}

	sock, stop := startServerForResolver(t, resolver)
	defer stop()

	resp := sendAttach(t, sock, prefix)
	if resp.Error != "" {
		t.Fatalf("ack carried error: %q", resp.Error)
	}
	if !resp.OK {
		t.Fatalf("ack OK=false, want true")
	}

	resolves, lookups := resolver.callTrace()
	if len(resolves) != 1 || resolves[0] != prefix {
		t.Errorf("ResolveID calls = %v, want exactly [%q]", resolves, prefix)
	}
	if len(lookups) != 1 || lookups[0] != mintedID {
		t.Errorf("Lookup calls = %v, want exactly [%q] (the resolved id, not the prefix)", lookups, mintedID)
	}
	if mintedCounter.calls() != 1 {
		t.Errorf("minted session attachCalls = %d, want 1", mintedCounter.calls())
	}
	if bootstrapCounter.calls() != 0 {
		t.Errorf("bootstrap session attachCalls = %d, want 0", bootstrapCounter.calls())
	}
}

// TestAttach_AmbiguousPrefix_ErrorBeforeBridge verifies that an ambiguous
// resolver result is encoded verbatim under the "attach: " prefix and that
// the bridge state is never touched (no Attach call lands on any session).
func TestAttach_AmbiguousPrefix_ErrorBeforeBridge(t *testing.T) {
	t.Parallel()

	const bootstrapID sessions.SessionID = "00000000-0000-4000-8000-00000000aaaa"

	counter := &sessionCounter{}
	bootstrapSess := &fakeSession{attachFn: counter.attach}

	ambiguous := fmt.Errorf("%w:\n%s\n%s",
		sessions.ErrAmbiguousSessionID,
		"aaaa1111-2222-4333-8444-555555555555 (alpha)",
		"aaaa2222-2222-4333-8444-555555555555 (beta)",
	)

	resolver := &multiSessionResolver{
		sessions: map[sessions.SessionID]*fakeSession{
			bootstrapID: bootstrapSess,
		},
		resolveFn: func(arg string) (sessions.SessionID, error) {
			if arg == "" {
				return bootstrapID, nil
			}
			return "", ambiguous
		},
	}

	sock, stop := startServerForResolver(t, resolver)
	defer stop()

	resp := sendAttach(t, sock, "aaaa")
	wantErr := "attach: " + ambiguous.Error()
	if resp.Error != wantErr {
		t.Errorf("Error = %q, want %q", resp.Error, wantErr)
	}
	if resp.OK {
		t.Errorf("ack OK=true, want false on resolver error")
	}

	resolves, lookups := resolver.callTrace()
	if len(resolves) != 1 || resolves[0] != "aaaa" {
		t.Errorf("ResolveID calls = %v, want exactly [%q]", resolves, "aaaa")
	}
	if len(lookups) != 0 {
		t.Errorf("Lookup calls = %v, want empty (Lookup must not run after ResolveID error)", lookups)
	}
	if counter.calls() != 0 {
		t.Errorf("session attachCalls = %d, want 0 (bridge must be untouched on resolver error)", counter.calls())
	}
}

// TestAttach_UnknownID_ErrorBeforeBridge verifies that ErrSessionNotFound
// from the resolver flows through verbatim under the "attach: " prefix
// and the bridge state is unchanged.
func TestAttach_UnknownID_ErrorBeforeBridge(t *testing.T) {
	t.Parallel()

	const bootstrapID sessions.SessionID = "00000000-0000-4000-8000-00000000aaaa"

	counter := &sessionCounter{}
	bootstrapSess := &fakeSession{attachFn: counter.attach}

	resolver := &multiSessionResolver{
		sessions: map[sessions.SessionID]*fakeSession{
			bootstrapID: bootstrapSess,
		},
		resolveFn: func(arg string) (sessions.SessionID, error) {
			if arg == "" {
				return bootstrapID, nil
			}
			return "", sessions.ErrSessionNotFound
		},
	}

	sock, stop := startServerForResolver(t, resolver)
	defer stop()

	resp := sendAttach(t, sock, "deadbeef")
	const wantErr = "attach: sessions: session not found"
	if resp.Error != wantErr {
		t.Errorf("Error = %q, want %q", resp.Error, wantErr)
	}
	if resp.OK {
		t.Errorf("ack OK=true, want false on resolver error")
	}

	resolves, lookups := resolver.callTrace()
	if len(resolves) != 1 || resolves[0] != "deadbeef" {
		t.Errorf("ResolveID calls = %v, want exactly [%q]", resolves, "deadbeef")
	}
	if len(lookups) != 0 {
		t.Errorf("Lookup calls = %v, want empty (Lookup must not run after ResolveID error)", lookups)
	}
	if counter.calls() != 0 {
		t.Errorf("session attachCalls = %d, want 0 (bridge must be untouched on resolver error)", counter.calls())
	}
}

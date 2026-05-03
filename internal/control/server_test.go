package control

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/supervisor"
)

// fakeSession satisfies control.Session for tests. Safe under concurrent use.
// Set attachFn to drive the Attach behaviour; nil means "no attach configured"
// (i.e. tests that exercise non-attach verbs).
type fakeSession struct {
	mu            sync.Mutex
	state         supervisor.State
	attachFn      func(in io.Reader, out io.Writer) (<-chan struct{}, error)
	activateCalls int
	activateErr   error
	resizeCalls   []resizeCall
	resizeErr     error
}

// resizeCall records one Session.Resize invocation. Rows-then-cols matches
// the seam's argument order (mirroring pty.Winsize).
type resizeCall struct{ Rows, Cols uint16 }

func (f *fakeSession) State() supervisor.State {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state
}

func (f *fakeSession) Attach(in io.Reader, out io.Writer) (<-chan struct{}, error) {
	f.mu.Lock()
	fn := f.attachFn
	f.mu.Unlock()
	if fn == nil {
		return nil, errors.New("fakeSession: no attach configured")
	}
	return fn(in, out)
}

func (f *fakeSession) Activate(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.activateCalls++
	return f.activateErr
}

func (f *fakeSession) Resize(rows, cols uint16) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resizeCalls = append(f.resizeCalls, resizeCall{Rows: rows, Cols: cols})
	return f.resizeErr
}

// recordedResizeCalls returns a copy of the recorded resize history under
// the lock — callers must not access fakeSession.resizeCalls directly.
func (f *fakeSession) recordedResizeCalls() []resizeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]resizeCall, len(f.resizeCalls))
	copy(out, f.resizeCalls)
	return out
}

// fakeResolver returns its single fakeSession for any id. Set lookupErr to
// drive the not-found / error path. Set resolveFn to drive ResolveID; the
// default returns the empty id with nil error, which makes existing tests
// (which only exercise Lookup) behave identically to the pre-1.1e-C world.
type fakeResolver struct {
	sess      *fakeSession
	lookupErr error
	resolveFn func(arg string) (sessions.SessionID, error)
}

func (r *fakeResolver) Lookup(id sessions.SessionID) (Session, error) {
	if r.lookupErr != nil {
		return nil, r.lookupErr
	}
	return r.sess, nil
}

func (r *fakeResolver) ResolveID(arg string) (sessions.SessionID, error) {
	if r.resolveFn != nil {
		return r.resolveFn(arg)
	}
	return sessions.SessionID(arg), nil
}

// recordingResolver wraps a delegate and notifies a record callback on each
// Lookup. Used by TestServer_Status_ResolvesDefaultSession to assert the
// handler passes the empty id today (the seam Phase 1.1 will swap to
// req.SessionID). resolveRecord, if non-nil, is notified on each ResolveID.
type recordingResolver struct {
	delegate      SessionResolver
	record        func(sessions.SessionID)
	resolveRecord func(string)
}

func (r *recordingResolver) Lookup(id sessions.SessionID) (Session, error) {
	r.record(id)
	return r.delegate.Lookup(id)
}

func (r *recordingResolver) ResolveID(arg string) (sessions.SessionID, error) {
	if r.resolveRecord != nil {
		r.resolveRecord(arg)
	}
	return r.delegate.ResolveID(arg)
}

// shortTempDir returns a short tempdir suitable for Unix socket paths.
// t.TempDir() lives under /var/folders/... on macOS, which combined with
// long test names blows past the 104-byte sun_path limit. /tmp is short.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "pyrysock")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// startServer wires up a Server on a tempdir socket and runs Serve in a
// goroutine. Returns a stop function that cancels the context, waits for
// Serve to return, and asserts no error.
func startServer(t *testing.T, resolver SessionResolver) (sock string, stop func()) {
	t.Helper()
	dir := shortTempDir(t)
	sock = filepath.Join(dir, "p.sock")

	srv := NewServer(sock, resolver, nil, nil, nil)
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

func TestServer_Status(t *testing.T) {
	t.Parallel()

	startedAt := time.Now().Add(-2 * time.Minute)
	resolver := &fakeResolver{sess: &fakeSession{state: supervisor.State{
		Phase:        supervisor.PhaseRunning,
		ChildPID:     12345,
		StartedAt:    startedAt,
		RestartCount: 3,
		LastUptime:   310 * time.Millisecond,
	}}}

	sock, stop := startServer(t, resolver)
	defer stop()

	resp, err := Status(context.Background(), sock)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if resp.Phase != "running" {
		t.Errorf("Phase = %q, want running", resp.Phase)
	}
	if resp.ChildPID != 12345 {
		t.Errorf("ChildPID = %d, want 12345", resp.ChildPID)
	}
	if resp.RestartCount != 3 {
		t.Errorf("RestartCount = %d, want 3", resp.RestartCount)
	}
	if resp.LastUptime != "310ms" {
		t.Errorf("LastUptime = %q, want 310ms", resp.LastUptime)
	}
	if resp.NextBackoff != "" {
		t.Errorf("NextBackoff = %q, want empty (running phase has no scheduled backoff)", resp.NextBackoff)
	}
	if resp.StartedAt == "" {
		t.Errorf("StartedAt is empty")
	}
}

func TestServer_StatusInBackoff(t *testing.T) {
	t.Parallel()

	resolver := &fakeResolver{sess: &fakeSession{state: supervisor.State{
		Phase:        supervisor.PhaseBackoff,
		ChildPID:     0,
		StartedAt:    time.Now(),
		RestartCount: 1,
		LastUptime:   270 * time.Millisecond,
		NextBackoff:  500 * time.Millisecond,
	}}}

	sock, stop := startServer(t, resolver)
	defer stop()

	resp, err := Status(context.Background(), sock)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if resp.Phase != "backoff" {
		t.Errorf("Phase = %q, want backoff", resp.Phase)
	}
	if resp.ChildPID != 0 {
		t.Errorf("ChildPID = %d, want 0", resp.ChildPID)
	}
	if resp.NextBackoff != "500ms" {
		t.Errorf("NextBackoff = %q, want 500ms", resp.NextBackoff)
	}
}

func TestServer_UnknownVerb(t *testing.T) {
	t.Parallel()

	sock, stop := startServer(t, &fakeResolver{sess: &fakeSession{}})
	defer stop()

	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if err := json.NewEncoder(conn).Encode(Request{Verb: "frobnicate"}); err != nil {
		t.Fatalf("encode: %v", err)
	}

	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error == "" {
		t.Fatalf("expected Error, got nil; resp=%+v", resp)
	}
	if !strings.Contains(resp.Error, "frobnicate") {
		t.Errorf("Error = %q, want to mention the bad verb", resp.Error)
	}
}

func TestServer_StaleSocketIsReplaced(t *testing.T) {
	t.Parallel()

	dir := shortTempDir(t)
	sock := filepath.Join(dir, "p.sock")

	// Pre-create a stale file at the socket path — simulates a prior pyry
	// crash that didn't clean up.
	if err := os.WriteFile(sock, []byte("stale"), 0o600); err != nil {
		t.Fatalf("seed stale: %v", err)
	}

	srv := NewServer(sock, &fakeResolver{sess: &fakeSession{}}, nil, nil, nil)
	if err := srv.Listen(); err != nil {
		t.Fatalf("Listen with stale file should succeed: %v", err)
	}
	defer func() { _ = srv.Close() }()

	// Confirm we can actually accept a connection on the new socket.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ctx) }()

	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial replaced socket: %v", err)
	}
	_ = conn.Close()
}

func TestServer_CloseRemovesSocket(t *testing.T) {
	t.Parallel()

	dir := shortTempDir(t)
	sock := filepath.Join(dir, "p.sock")

	srv := NewServer(sock, &fakeResolver{sess: &fakeSession{}}, nil, nil, nil)
	if err := srv.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("socket should exist after Listen: %v", err)
	}

	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(sock); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("socket should be removed after Close, stat err: %v", err)
	}

	// Idempotent — second Close should not error.
	if err := srv.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

// TestServer_ListenRefusesActiveInstance verifies #17: when a live pyry is
// already answering on the socket path, a second pyry's Listen must fail
// fast with ErrInstanceRunning instead of silently unlinking the path and
// hijacking it.
//
// The previous behaviour was the opposite — a second Listen would unlink
// the file and bind a fresh listener, leaving the first pyry's listener
// alive in the kernel but unreachable via the filesystem. Both pyrys
// would then race for ~/.claude/projects/<cwd>/sessions/ files.
func TestServer_ListenRefusesActiveInstance(t *testing.T) {
	t.Parallel()

	dir := shortTempDir(t)
	sock := filepath.Join(dir, "p.sock")

	// Stand up the first pyry. It must be Serving — a Listen-only server
	// has its FD bound but isn't accepting, and the dial-probe in the
	// second Listen would hang on the kernel-side accept queue rather
	// than completing the handshake. Serving makes the live-instance
	// probe symmetric with what a real client would see.
	srvA := NewServer(sock, &fakeResolver{sess: &fakeSession{}}, nil, nil, nil)
	if err := srvA.Listen(); err != nil {
		t.Fatalf("Listen A: %v", err)
	}
	defer func() { _ = srvA.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srvA.Serve(ctx) }()

	// Second pyry tries to take the same path.
	srvB := NewServer(sock, &fakeResolver{sess: &fakeSession{}}, nil, nil, nil)
	err := srvB.Listen()
	if !errors.Is(err, ErrInstanceRunning) {
		t.Fatalf("Listen B = %v, want ErrInstanceRunning", err)
	}

	// Original socket file must still belong to srvA — the rejected
	// Listen must not have unlinked it. Dialling the path should still
	// reach a live peer.
	conn, err := net.DialTimeout("unix", sock, dialProbeTimeout)
	if err != nil {
		t.Fatalf("first pyry's socket should still be reachable after refused second Listen: %v", err)
	}
	_ = conn.Close()
}

// TestServer_ListenFailsWhenParentDirIsAFile covers the os.MkdirAll error
// branch in Listen. A regular file at the parent path makes the dir
// creation fail, and Listen should report it as "create socket dir: ...".
func TestServer_ListenFailsWhenParentDirIsAFile(t *testing.T) {
	t.Parallel()

	dir := shortTempDir(t)
	parent := filepath.Join(dir, "blocking-file")
	if err := os.WriteFile(parent, []byte("not a dir"), 0o600); err != nil {
		t.Fatalf("seed parent file: %v", err)
	}
	sock := filepath.Join(parent, "p.sock") // parent isn't a directory

	srv := NewServer(sock, &fakeResolver{sess: &fakeSession{}}, nil, nil, nil)
	err := srv.Listen()
	if err == nil {
		t.Fatal("Listen should fail when parent is a regular file")
	}
	if !strings.Contains(err.Error(), "create socket dir") {
		t.Errorf("error = %q, want it to mention 'create socket dir'", err)
	}
}

// TestServer_ConcurrentClose hits the Close-from-two-goroutines path that
// can happen in practice (ctx-watcher + main's defer both fire). Mutex
// makes it safe; the test confirms with -race.
func TestServer_ConcurrentClose(t *testing.T) {
	t.Parallel()

	dir := shortTempDir(t)
	sock := filepath.Join(dir, "p.sock")

	srv := NewServer(sock, &fakeResolver{sess: &fakeSession{}}, nil, nil, nil)
	if err := srv.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}

	done := make(chan struct{}, 2)
	for i := 0; i < 2; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			_ = srv.Close()
		}()
	}
	<-done
	<-done

	// Both Close calls should have completed. Subsequent Close still ok.
	if err := srv.Close(); err != nil {
		t.Errorf("third Close: %v", err)
	}
}

func TestNewServer_PanicsOnNilSessions(t *testing.T) {
	t.Parallel()

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected NewServer(nil sessions) to panic, did not")
		}
		if msg, ok := r.(string); ok {
			if !strings.Contains(msg, "sessions") {
				t.Errorf("panic message %q should mention sessions", msg)
			}
		}
	}()

	// Note: we don't reach the lines below if the panic fires (which it
	// must), but they document the contract being tested.
	_ = NewServer("/tmp/p.sock", nil, nil, nil, nil)
	t.Fatal("NewServer returned without panicking")
}

func TestClient_DialFailsCleanly(t *testing.T) {
	t.Parallel()

	// Path that definitely doesn't exist.
	sock := filepath.Join(shortTempDir(t), "no-such-socket")
	_, err := Status(context.Background(), sock)
	if err == nil {
		t.Fatal("expected error dialing nonexistent socket")
	}
	if !strings.Contains(err.Error(), "dial") {
		t.Errorf("error should mention dial, got: %v", err)
	}
}

func TestServer_Stop(t *testing.T) {
	t.Parallel()

	dir := shortTempDir(t)
	sock := filepath.Join(dir, "p.sock")

	shutdownCalled := make(chan struct{}, 1)
	srv := NewServer(sock, &fakeResolver{sess: &fakeSession{}}, nil, func() {
		select {
		case shutdownCalled <- struct{}{}:
		default:
		}
	}, nil)
	if err := srv.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveDone := make(chan error, 1)
	go func() { serveDone <- srv.Serve(ctx) }()

	if err := Stop(context.Background(), sock); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	select {
	case <-shutdownCalled:
		// expected
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown callback was not invoked after stop request")
	}

	// The integration: shutdown() in the real wiring is the supervisor
	// context's cancel. main.go cancels its own ctx, the goroutine in
	// Serve sees ctx.Done() and closes the listener. Simulate that here
	// — Stop fired the callback, now we cancel and verify Serve returns.
	cancel()
	select {
	case err := <-serveDone:
		if err != nil {
			t.Errorf("Serve returned: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return after cancel")
	}
}

func TestServer_StopWithoutHandler(t *testing.T) {
	t.Parallel()

	// Server constructed without a shutdown handler should report a clean
	// error response rather than panicking on nil.
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "p.sock")

	srv := NewServer(sock, &fakeResolver{sess: &fakeSession{}}, nil, nil, nil)
	if err := srv.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ctx) }()

	err := Stop(context.Background(), sock)
	if err == nil {
		t.Fatal("expected error when shutdown handler is nil")
	}
	if !strings.Contains(err.Error(), "stop") {
		t.Errorf("error should mention stop, got: %v", err)
	}
}

// TestServer_Status_ResolvesDefaultSession verifies the resolver path: a
// VerbStatus request with no session field flows through Lookup("") to the
// default session. Phase 1.1 will populate req.SessionID; this test pins
// the empty-id-resolves-to-default behaviour as the seam.
func TestServer_Status_ResolvesDefaultSession(t *testing.T) {
	t.Parallel()

	sess := &fakeSession{state: supervisor.State{
		Phase:        supervisor.PhaseRunning,
		ChildPID:     999,
		StartedAt:    time.Now(),
		RestartCount: 7,
	}}
	var (
		mu          sync.Mutex
		lookupCalls []sessions.SessionID
	)
	resolver := &recordingResolver{
		delegate: &fakeResolver{sess: sess},
		record: func(id sessions.SessionID) {
			mu.Lock()
			defer mu.Unlock()
			lookupCalls = append(lookupCalls, id)
		},
	}

	sock, stop := startServer(t, resolver)
	defer stop()

	resp, err := Status(context.Background(), sock)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if resp.ChildPID != 999 || resp.RestartCount != 7 {
		t.Errorf("status payload did not come from the resolved session: %+v", resp)
	}

	mu.Lock()
	calls := append([]sessions.SessionID(nil), lookupCalls...)
	mu.Unlock()
	if len(calls) != 1 || calls[0] != "" {
		t.Errorf("expected exactly one Lookup call with empty id, got %v", calls)
	}
}

func TestClient_ServerHangup(t *testing.T) {
	t.Parallel()

	dir := shortTempDir(t)
	sock := filepath.Join(dir, "p.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	// Server that accepts and immediately closes — checks the client
	// reports a clean error rather than hanging.
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		_ = conn.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = Status(ctx, sock)
	if err == nil {
		t.Fatal("expected error from hung-up server")
	}
	// EOF is the expected shape — the client managed to write but the
	// server closed before responding.
	if !errors.Is(err, io.EOF) && !strings.Contains(err.Error(), "EOF") {
		t.Logf("(non-fatal) got error: %v", err)
	}
}

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

	"github.com/pyrycode/pyrycode/internal/supervisor"
)

// fakeState implements StateProvider for tests. Safe under concurrent use.
type fakeState struct {
	mu sync.Mutex
	st supervisor.State
}

func (f *fakeState) State() supervisor.State {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.st
}

func (f *fakeState) set(st supervisor.State) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.st = st
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
func startServer(t *testing.T, fs *fakeState) (sock string, stop func()) {
	t.Helper()
	dir := shortTempDir(t)
	sock = filepath.Join(dir, "p.sock")

	srv := NewServer(sock, fs, nil, nil, nil, nil)
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
	fs := &fakeState{st: supervisor.State{
		Phase:        supervisor.PhaseRunning,
		ChildPID:     12345,
		StartedAt:    startedAt,
		RestartCount: 3,
		LastUptime:   310 * time.Millisecond,
	}}

	sock, stop := startServer(t, fs)
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

	fs := &fakeState{st: supervisor.State{
		Phase:        supervisor.PhaseBackoff,
		ChildPID:     0,
		StartedAt:    time.Now(),
		RestartCount: 1,
		LastUptime:   270 * time.Millisecond,
		NextBackoff:  500 * time.Millisecond,
	}}

	sock, stop := startServer(t, fs)
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

	sock, stop := startServer(t, &fakeState{})
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

	srv := NewServer(sock, &fakeState{}, nil, nil, nil, nil)
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

	srv := NewServer(sock, &fakeState{}, nil, nil, nil, nil)
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
	srv := NewServer(sock, &fakeState{}, nil, nil, func() {
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

	srv := NewServer(sock, &fakeState{}, nil, nil, nil, nil)
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

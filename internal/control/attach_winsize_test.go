//go:build unix

package control

import (
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"reflect"
	"runtime"
	"syscall"
	"testing"
	"time"
)

// TestStartWinsizeWatcher_SIGWINCHEmitsResize pins the primary AC of #133:
// a SIGWINCH delivered while the watcher is alive triggers a VerbResize on
// the wire carrying the terminal size at signal time.
//
// Mirrors TestSendResize_RoundTrip's hand-rolled-listen pattern.
//
// Not t.Parallel(): syscall.Kill(SIGWINCH) is process-wide, so this test
// must not race with any other test that subscribes via signal.Notify
// (e.g. its peer in this file). No other test in internal/control
// subscribes to SIGWINCH today.
func TestStartWinsizeWatcher_SIGWINCHEmitsResize(t *testing.T) {

	dir := shortTempDir(t)
	sock := filepath.Join(dir, "p.sock")

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	gotReq := make(chan Request, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		var req Request
		if err := json.NewDecoder(conn).Decode(&req); err != nil {
			return
		}
		gotReq <- req
		_ = json.NewEncoder(conn).Encode(Response{OK: true})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	read := func() (int, int, bool) { return 120, 40, true }
	send := func(ctx context.Context, cols, rows int) error {
		return SendResize(ctx, sock, "abc", cols, rows)
	}
	stop := startWinsizeWatcher(ctx, read, send)
	defer stop()

	if err := syscall.Kill(syscall.Getpid(), syscall.SIGWINCH); err != nil {
		t.Fatalf("kill SIGWINCH: %v", err)
	}

	select {
	case req := <-gotReq:
		if req.Verb != VerbResize {
			t.Errorf("Verb = %q, want %q", req.Verb, VerbResize)
		}
		want := &ResizePayload{SessionID: "abc", Cols: 120, Rows: 40}
		if !reflect.DeepEqual(req.Resize, want) {
			t.Errorf("Resize = %+v, want %+v", req.Resize, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not receive a request")
	}
}

// TestStartWinsizeWatcher_StopIsSynchronousAndLeakFree exercises AC#3:
// repeated start/stop cycles must not leak goroutines or signal handlers.
//
// The structural guarantee is that stop blocks until the watcher's
// goroutine has exited; if that were violated, even modest leak rates
// (e.g. one goroutine per iteration) would surface as a clear delta in
// runtime.NumGoroutine() across 50 iterations.
//
// Not t.Parallel(): see SIGWINCHEmitsResize for the rationale.
func TestStartWinsizeWatcher_StopIsSynchronousAndLeakFree(t *testing.T) {

	read := func() (int, int, bool) { return 80, 24, true }
	send := func(ctx context.Context, cols, rows int) error { return nil }

	// Allow the runtime to settle: any stragglers from package init or
	// parallel-test scheduling stabilize before we sample the baseline.
	runtime.GC()
	time.Sleep(20 * time.Millisecond)
	before := runtime.NumGoroutine()

	const iterations = 50
	for i := 0; i < iterations; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		stop := startWinsizeWatcher(ctx, read, send)
		// Exercise the live path so we cover the SendResize branch
		// (best-effort no-op send) at least once per iteration.
		if err := syscall.Kill(syscall.Getpid(), syscall.SIGWINCH); err != nil {
			t.Fatalf("kill SIGWINCH: %v", err)
		}
		stop()
		cancel()
	}

	runtime.GC()
	time.Sleep(20 * time.Millisecond)
	after := runtime.NumGoroutine()

	// If stop were non-synchronous, watcher goroutines would accumulate
	// across iterations. Allow a small slack for unrelated parallel-test
	// noise but reject anything resembling per-iteration leakage.
	if after-before > 5 {
		t.Errorf("goroutine leak: before=%d after=%d (delta=%d) over %d iterations",
			before, after, after-before, iterations)
	}
}

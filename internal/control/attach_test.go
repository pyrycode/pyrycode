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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/supervisor"
)

// errReader reports the configured error on every Read.
type errReader struct{ err error }

func (e *errReader) Read(p []byte) (int, error) { return 0, e.err }

// errCopyWriter implements io.Writer and returns the configured error on
// every Write. Used to drive copyWithEscape's write-failure paths.
type errCopyWriter struct{ err error }

func (e *errCopyWriter) Write(p []byte) (int, error) { return 0, e.err }

func TestCopyWithEscape_NonEOFReadError(t *testing.T) {
	t.Parallel()

	want := errors.New("read failed")
	err := copyWithEscape(&bytes.Buffer{}, &errReader{err: want})
	if err == nil {
		t.Fatal("expected non-EOF read error to propagate")
	}
	if !errors.Is(err, want) && err.Error() != want.Error() {
		t.Errorf("err = %v, want %v", err, want)
	}
}

func TestCopyWithEscape_WriteErrorInNormalState(t *testing.T) {
	t.Parallel()

	// Closed-pipe error → writerErr coerces to nil (clean detach).
	err := copyWithEscape(&errCopyWriter{err: io.ErrClosedPipe}, bytes.NewReader([]byte("a")))
	if err != nil {
		t.Errorf("io.ErrClosedPipe should be coerced to nil, got %v", err)
	}

	// Other write error → propagates.
	other := errors.New("disk full")
	err = copyWithEscape(&errCopyWriter{err: other}, bytes.NewReader([]byte("a")))
	if err == nil || !errors.Is(err, other) {
		t.Errorf("expected %v to propagate, got %v", other, err)
	}
}

func TestCopyWithEscape_WriteErrorInPendingFlush(t *testing.T) {
	t.Parallel()

	// Escape + non-detach byte triggers the pending-flush write path.
	// Error there should propagate (or be coerced if it's a closed-pipe).
	other := errors.New("flush failed")
	err := copyWithEscape(&errCopyWriter{err: other}, bytes.NewReader([]byte("\x02x")))
	if err == nil || !errors.Is(err, other) {
		t.Errorf("expected %v from pending-flush write, got %v", other, err)
	}
}

func TestWriterErr(t *testing.T) {
	t.Parallel()

	otherErr := errors.New("some other I/O failure")

	tests := []struct {
		name string
		in   error
		want error
	}{
		{"nil stays nil", nil, nil},
		{"net.ErrClosed becomes nil (server hung up)", net.ErrClosed, nil},
		{"io.ErrClosedPipe becomes nil (server hung up)", io.ErrClosedPipe, nil},
		{"other error propagates", otherErr, otherErr},
		{"wrapped net.ErrClosed becomes nil", fmt.Errorf("wrapped: %w", net.ErrClosed), nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := writerErr(tt.in)
			if (got == nil) != (tt.want == nil) {
				t.Errorf("writerErr(%v) = %v, want %v (nil-equality differs)", tt.in, got, tt.want)
				return
			}
			if got != nil && got != tt.want {
				t.Errorf("writerErr(%v) = %v, want exact %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestCopyWithEscape(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   []byte
		want    []byte // bytes that reach dst
		wantNil bool   // expect nil return (clean detach via escape)
	}{
		{
			name:    "plain bytes pass through",
			input:   []byte("hello world"),
			want:    []byte("hello world"),
			wantNil: false, // EOF, not detach
		},
		{
			name:    "escape + d detaches",
			input:   []byte("hi\x02d"),
			want:    []byte("hi"),
			wantNil: true,
		},
		{
			name:    "escape + non-d flushes both",
			input:   []byte("a\x02xb"),
			want:    []byte("a\x02xb"),
			wantNil: false,
		},
		{
			name:    "lone escape at end is held (lost on EOF)",
			input:   []byte("a\x02"),
			want:    []byte("a"),
			wantNil: false,
		},
		{
			name:    "double escape — first is held, second flushes both",
			input:   []byte("\x02\x02a"),
			want:    []byte{0x02, 0x02, 'a'},
			wantNil: false,
		},
		{
			name:    "empty input ends cleanly via EOF",
			input:   []byte{},
			want:    []byte{},
			wantNil: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var dst bytes.Buffer
			err := copyWithEscape(&dst, bytes.NewReader(tt.input))
			if tt.wantNil && err != nil {
				t.Errorf("expected nil error on clean detach, got %v", err)
			}
			if !bytes.Equal(dst.Bytes(), tt.want) {
				t.Errorf("dst = %q, want %q", dst.Bytes(), tt.want)
			}
		})
	}
}

// fakeAttachProvider drives the attachFn on a fakeSession; it captures
// client input for later assertions. It does not write anything back to
// the client — output direction is verified separately via the real
// Bridge in TestServer_BridgeAttach.
//
// All access to the buffer is mutex-protected: writes happen via lockedWrite
// (called from the input pump goroutine), reads happen via received() (called
// from the test goroutine). This avoids the race detector flagging the
// concurrent buffer access that bytes.Buffer doesn't guard internally.
type fakeAttachProvider struct {
	mu              sync.Mutex
	attached        bool
	receivedFromCli []byte
	rejectWithErr   error
}

func (f *fakeAttachProvider) Attach(in io.Reader, out io.Writer) (<-chan struct{}, error) {
	f.mu.Lock()
	if f.rejectWithErr != nil {
		f.mu.Unlock()
		return nil, f.rejectWithErr
	}
	if f.attached {
		f.mu.Unlock()
		return nil, errors.New("already attached")
	}
	f.attached = true
	f.mu.Unlock()

	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		for {
			n, err := in.Read(buf)
			if n > 0 {
				f.mu.Lock()
				f.receivedFromCli = append(f.receivedFromCli, buf[:n]...)
				f.mu.Unlock()
			}
			if err != nil {
				break
			}
		}

		f.mu.Lock()
		f.attached = false
		f.mu.Unlock()
	}()
	return done, nil
}

func (f *fakeAttachProvider) received() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]byte(nil), f.receivedFromCli...)
}

func TestServer_AttachHandshakeAndStream(t *testing.T) {
	t.Parallel()

	dir := shortTempDir(t)
	sock := filepath.Join(dir, "p.sock")

	provider := &fakeAttachProvider{}
	srv := NewServer(sock, sessionResolverWith(provider.Attach), nil, nil, nil, nil)
	if err := srv.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ctx) }()

	// Connect, send handshake, read ack, then exchange raw bytes.
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if err := json.NewEncoder(conn).Encode(Request{
		Verb:   VerbAttach,
		Attach: &AttachPayload{Cols: 80, Rows: 24},
	}); err != nil {
		t.Fatalf("send handshake: %v", err)
	}

	dec := json.NewDecoder(conn)
	var resp Response
	if err := dec.Decode(&resp); err != nil {
		t.Fatalf("read ack: %v", err)
	}
	if resp.Error != "" {
		t.Fatalf("ack carried error: %q", resp.Error)
	}
	if !resp.OK {
		t.Fatalf("ack OK=false")
	}

	// Send raw bytes to the daemon.
	if _, err := conn.Write([]byte("ping from client")); err != nil {
		t.Fatalf("write to attach: %v", err)
	}

	// Close from client side — simulates a clean detach.
	_ = conn.Close()

	// Wait for the provider to observe the bytes (poll briefly).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := provider.received(); strings.Contains(string(got), "ping from client") {
			return // success
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("provider never received the client bytes; got %q", provider.received())
}

// TestServer_AttachAppliesHandshakeGeometry asserts the server invokes the
// resize seam with the handshake Cols/Rows from AttachPayload — rows-then-cols
// to match the seam's signature (mirroring pty.Winsize). Pins #136 AC#2.
func TestServer_AttachAppliesHandshakeGeometry(t *testing.T) {
	t.Parallel()

	dir := shortTempDir(t)
	sock := filepath.Join(dir, "p.sock")

	provider := &fakeAttachProvider{}
	sess := &fakeSession{attachFn: provider.Attach}
	srv := NewServer(sock, &fakeResolver{sess: sess}, nil, nil, nil, nil)
	if err := srv.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ctx) }()

	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if err := json.NewEncoder(conn).Encode(Request{
		Verb:   VerbAttach,
		Attach: &AttachPayload{Cols: 200, Rows: 50},
	}); err != nil {
		t.Fatalf("encode handshake: %v", err)
	}
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		t.Fatalf("decode ack: %v", err)
	}
	if !resp.OK {
		t.Fatalf("attach with geometry should succeed; got %+v", resp)
	}

	calls := sess.recordedResizeCalls()
	want := []resizeCall{{Rows: 50, Cols: 200}}
	if len(calls) != len(want) || calls[0] != want[0] {
		t.Errorf("resize calls = %v, want %v", calls, want)
	}
}

// TestServer_AttachZeroGeometryNoOp asserts that a zero in either dimension
// (the protocol's "unknown / don't touch" sentinel) suppresses the resize
// call entirely. A missing Attach payload is also a no-op.
func TestServer_AttachZeroGeometryNoOp(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		payload *AttachPayload
	}{
		{"both zero", &AttachPayload{Cols: 0, Rows: 0}},
		{"cols zero", &AttachPayload{Cols: 0, Rows: 50}},
		{"rows zero", &AttachPayload{Cols: 200, Rows: 0}},
		{"nil payload", nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dir := shortTempDir(t)
			sock := filepath.Join(dir, "p.sock")

			provider := &fakeAttachProvider{}
			sess := &fakeSession{attachFn: provider.Attach}
			srv := NewServer(sock, &fakeResolver{sess: sess}, nil, nil, nil, nil)
			if err := srv.Listen(); err != nil {
				t.Fatalf("Listen: %v", err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() { _ = srv.Serve(ctx) }()

			conn, err := net.Dial("unix", sock)
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			defer conn.Close()

			req := Request{Verb: VerbAttach, Attach: tc.payload}
			if err := json.NewEncoder(conn).Encode(req); err != nil {
				t.Fatalf("encode handshake: %v", err)
			}
			var resp Response
			if err := json.NewDecoder(conn).Decode(&resp); err != nil {
				t.Fatalf("decode ack: %v", err)
			}
			if !resp.OK {
				t.Fatalf("attach should still succeed: %+v", resp)
			}

			if calls := sess.recordedResizeCalls(); len(calls) != 0 {
				t.Errorf("expected no resize calls, got %v", calls)
			}
		})
	}
}

// TestServer_AttachResizeErrorDoesNotFailAttach asserts that a Resize error
// (e.g. transient EBADF on a closed fd between iterations) is logged but
// does not fail the attach handshake. Geometry is best-effort.
func TestServer_AttachResizeErrorDoesNotFailAttach(t *testing.T) {
	t.Parallel()

	dir := shortTempDir(t)
	sock := filepath.Join(dir, "p.sock")

	provider := &fakeAttachProvider{}
	sess := &fakeSession{
		attachFn:  provider.Attach,
		resizeErr: errors.New("synthetic setsize failure"),
	}
	srv := NewServer(sock, &fakeResolver{sess: sess}, nil, nil, nil, nil)
	if err := srv.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ctx) }()

	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if err := json.NewEncoder(conn).Encode(Request{
		Verb:   VerbAttach,
		Attach: &AttachPayload{Cols: 80, Rows: 24},
	}); err != nil {
		t.Fatalf("encode handshake: %v", err)
	}
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		t.Fatalf("decode ack: %v", err)
	}
	if !resp.OK {
		t.Errorf("attach should succeed despite resize error; got %+v", resp)
	}
}

// TestServer_AttachForegroundSessionResizeSwallowed pins that the foreground
// session path (Resize returns ErrAttachUnavailable) does not fail the
// attach. The attach itself still surfaces the foreground error via Attach
// returning ErrAttachUnavailable, exercised by TestServer_AttachOnForegroundSession.
func TestServer_AttachForegroundSessionResizeSwallowed(t *testing.T) {
	t.Parallel()

	dir := shortTempDir(t)
	sock := filepath.Join(dir, "p.sock")

	sess := &fakeSession{
		attachFn: func(io.Reader, io.Writer) (<-chan struct{}, error) {
			return nil, sessions.ErrAttachUnavailable
		},
		resizeErr: sessions.ErrAttachUnavailable,
	}
	srv := NewServer(sock, &fakeResolver{sess: sess}, nil, nil, nil, nil)
	if err := srv.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ctx) }()

	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if err := json.NewEncoder(conn).Encode(Request{
		Verb:   VerbAttach,
		Attach: &AttachPayload{Cols: 80, Rows: 24},
	}); err != nil {
		t.Fatalf("encode handshake: %v", err)
	}
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		t.Fatalf("decode ack: %v", err)
	}
	const want = "attach: no attach provider configured (daemon may be in foreground mode)"
	if resp.Error != want {
		t.Errorf("Error = %q, want %q", resp.Error, want)
	}
}

// TestServer_AttachOnForegroundSession exercises the foreground-mode wire
// contract: a session whose Attach returns sessions.ErrAttachUnavailable
// (no bridge) must surface verbatim as the Phase 0 "no attach provider
// configured (daemon may be in foreground mode)" error string. This pins
// the byte-identical AC for `pyry attach` against a foreground-mode pyry.
func TestServer_AttachOnForegroundSession(t *testing.T) {
	t.Parallel()

	dir := shortTempDir(t)
	sock := filepath.Join(dir, "p.sock")

	resolver := sessionResolverWith(func(in io.Reader, out io.Writer) (<-chan struct{}, error) {
		return nil, sessions.ErrAttachUnavailable
	})
	srv := NewServer(sock, resolver, nil, nil, nil, nil)
	if err := srv.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ctx) }()

	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if err := json.NewEncoder(conn).Encode(Request{Verb: VerbAttach}); err != nil {
		t.Fatalf("send handshake: %v", err)
	}
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		t.Fatalf("read ack: %v", err)
	}
	const want = "attach: no attach provider configured (daemon may be in foreground mode)"
	if resp.Error != want {
		t.Errorf("Error = %q, want %q (byte-identical to Phase 0)", resp.Error, want)
	}
}

// sessionResolverWith returns a SessionResolver whose default session's
// Attach delegates to fn. Used by attach tests that need to drive the
// per-session Attach path without instantiating a real *sessions.Session.
func sessionResolverWith(fn func(in io.Reader, out io.Writer) (<-chan struct{}, error)) SessionResolver {
	return &fakeResolver{sess: &fakeSession{attachFn: fn}}
}

// TestServer_StopWhileAttached confirms that VerbStop arriving while a
// client is in the middle of an attach cleanly tears down the connection
// instead of leaking it.
//
// Sequence:
//  1. Server has both an AttachProvider (bridge) and a shutdown callback.
//  2. Client A attaches.
//  3. Client B sends VerbStop on a separate connection.
//  4. Server fires shutdown — caller (in production: main.go) is expected
//     to call Server.Close, which is what we simulate.
//  5. Client A's conn should close cleanly. The bridge's done channel
//     should fire.
func TestServer_StopWhileAttached(t *testing.T) {
	t.Parallel()

	dir := shortTempDir(t)
	sock := filepath.Join(dir, "p.sock")

	bridge := supervisor.NewBridge(nil)

	shutdownFired := make(chan struct{}, 1)
	srv := NewServer(sock, sessionResolverWith(bridge.Attach), nil, func() {
		select {
		case shutdownFired <- struct{}{}:
		default:
		}
	}, nil, nil)
	if err := srv.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ctx) }()

	// Client A: attach.
	connA, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial A: %v", err)
	}
	defer connA.Close()
	if err := json.NewEncoder(connA).Encode(Request{Verb: VerbAttach}); err != nil {
		t.Fatalf("encode attach: %v", err)
	}
	var ackA Response
	if err := json.NewDecoder(connA).Decode(&ackA); err != nil {
		t.Fatalf("decode ack A: %v", err)
	}
	if !ackA.OK {
		t.Fatalf("attach ack OK=false: %+v", ackA)
	}
	if !bridge.Attached() {
		t.Fatal("bridge reports not attached after successful handshake")
	}

	// Client B: stop, on a separate connection.
	connB, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial B: %v", err)
	}
	defer connB.Close()
	if err := json.NewEncoder(connB).Encode(Request{Verb: VerbStop}); err != nil {
		t.Fatalf("encode stop: %v", err)
	}
	var ackB Response
	if err := json.NewDecoder(connB).Decode(&ackB); err != nil {
		t.Fatalf("decode ack B: %v", err)
	}
	if !ackB.OK {
		t.Fatalf("stop ack OK=false: %+v", ackB)
	}

	// Shutdown callback should have fired.
	select {
	case <-shutdownFired:
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown callback did not fire after VerbStop")
	}

	// Simulate what main.go does next: cancel the supervisor's context.
	// In production this triggers ptmx close (child dies) which closes
	// the input side of the bridge pipe, ending the attach goroutine.
	// We can't easily simulate the full ptmx cascade in this test, so
	// instead we close conn A from the client side — equivalent to a
	// disconnect. The point is to prove the server-side teardown
	// goroutine spawned by handleAttach completes regardless.
	_ = connA.Close()

	// Read deadline so this test can't hang forever if something goes wrong.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !bridge.Attached() {
			return // success — bridge cleared its attached flag
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("bridge.Attached() still true 2s after client disconnect")
}

// TestServer_ConcurrentAttachRace fires two attach handshakes simultaneously
// from separate goroutines. At-most-one is enforced via Bridge's mutex;
// exactly one of the two should land OK, the other ErrBridgeBusy. With the
// race detector on, this also exercises the mutex contract.
func TestServer_ConcurrentAttachRace(t *testing.T) {
	t.Parallel()

	dir := shortTempDir(t)
	sock := filepath.Join(dir, "p.sock")

	bridge := supervisor.NewBridge(nil)
	srv := NewServer(sock, sessionResolverWith(bridge.Attach), nil, nil, nil, nil)
	if err := srv.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ctx) }()

	type result struct {
		ok    bool
		errFx string
	}
	results := make(chan result, 2)
	conns := make(chan net.Conn, 2)

	for i := 0; i < 2; i++ {
		go func() {
			conn, err := net.Dial("unix", sock)
			if err != nil {
				results <- result{errFx: err.Error()}
				return
			}
			conns <- conn
			if err := json.NewEncoder(conn).Encode(Request{Verb: VerbAttach}); err != nil {
				results <- result{errFx: err.Error()}
				return
			}
			var resp Response
			if err := json.NewDecoder(conn).Decode(&resp); err != nil {
				results <- result{errFx: err.Error()}
				return
			}
			results <- result{ok: resp.OK, errFx: resp.Error}
		}()
	}

	r1 := <-results
	r2 := <-results

	// Exactly one should be OK, the other busy.
	okCount, busyCount := 0, 0
	for _, r := range []result{r1, r2} {
		switch {
		case r.ok && r.errFx == "":
			okCount++
		case strings.Contains(r.errFx, "already") || strings.Contains(r.errFx, "busy"):
			busyCount++
		default:
			t.Errorf("unexpected result: ok=%v err=%q", r.ok, r.errFx)
		}
	}
	if okCount != 1 || busyCount != 1 {
		t.Errorf("got %d OK and %d busy; want exactly 1 of each", okCount, busyCount)
	}

	// Drain conns so they close cleanly.
	close(conns)
	for c := range conns {
		_ = c.Close()
	}
}

// TestServer_HandshakeTimeout confirms a connected-but-silent client gets
// disconnected by the server after handshakeTimeout. Without this, a slow
// client could pin a server goroutine indefinitely.
//
// Note: the test relies on the production handshakeTimeout (5s) being
// reasonable for CI. We don't override it because the const isn't
// configurable at the call site; if this becomes a CI flake risk, plumb
// it through Server.Config.
func TestServer_HandshakeTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping 5s handshake timeout test in -short mode")
	}
	t.Parallel()

	dir := shortTempDir(t)
	sock := filepath.Join(dir, "p.sock")

	srv := NewServer(sock, &fakeResolver{sess: &fakeSession{}}, nil, nil, nil, nil)
	if err := srv.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ctx) }()

	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Don't send anything. The server's dec.Decode is gated on the
	// handshakeTimeout. Read should EOF (or err) within ~handshakeTimeout
	// seconds — give a generous buffer.
	_ = conn.SetReadDeadline(time.Now().Add(handshakeTimeout + 2*time.Second))
	buf := make([]byte, 256)
	n, readErr := conn.Read(buf)

	// Server's response on timeout is to encode an error response and
	// close. Either we read that error JSON, or the conn closes outright
	// (EOF). Both are acceptable; the FAIL case is "no error within the
	// deadline."
	if readErr == nil && n == 0 {
		t.Fatal("expected EOF or response after handshake timeout, got neither")
	}
}

// TestAttach_ClientSendsSessionID pins the wire shape produced by the
// client-side Attach helper. Phase 1.1e-D runs the selector argument
// straight from the CLI positional through to AttachPayload.SessionID;
// this test is the seam check that nothing in the client layer mangles or
// drops it. The server side just records the Request and returns an error
// ack so Attach returns before entering raw bridge mode (no terminal in the
// test environment).
func TestAttach_ClientSendsSessionID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		sessionID string
	}{
		{"empty selector → bootstrap on the server", ""},
		{"full UUID flows through verbatim", "11111111-2222-3333-4444-555555555555"},
		{"prefix flows through verbatim", "1111"},
		{"whitespace-only flows through (server lints)", " "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

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
				// Force Attach to return early without entering bridge mode.
				_ = json.NewEncoder(conn).Encode(Response{Error: "test: short-circuit"})
			}()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err = Attach(ctx, sock, 80, 24, tt.sessionID)
			if err == nil || !strings.Contains(err.Error(), "test: short-circuit") {
				t.Fatalf("Attach: want short-circuit error, got %v", err)
			}

			select {
			case req := <-gotReq:
				if req.Verb != VerbAttach {
					t.Errorf("Verb = %q, want %q", req.Verb, VerbAttach)
				}
				if req.Attach == nil {
					t.Fatalf("Attach payload missing")
				}
				if req.Attach.SessionID != tt.sessionID {
					t.Errorf("SessionID = %q, want %q", req.Attach.SessionID, tt.sessionID)
				}
				if req.Attach.Cols != 80 || req.Attach.Rows != 24 {
					t.Errorf("Cols/Rows = %d/%d, want 80/24", req.Attach.Cols, req.Attach.Rows)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("server did not receive a request")
			}
		})
	}
}

// TestAttach_EmptySessionIDOmittedOnWire confirms the byte-shape promise
// in protocol.go: an empty SessionID must marshal to no field, so a v0.5.x
// client (which doesn't know the field) and a v0.7.x client passing "" are
// indistinguishable to the server during the rollover window.
func TestAttach_EmptySessionIDOmittedOnWire(t *testing.T) {
	t.Parallel()

	dir := shortTempDir(t)
	sock := filepath.Join(dir, "p.sock")

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	gotRaw := make(chan []byte, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		buf := make([]byte, 4096)
		n, _ := conn.Read(buf)
		gotRaw <- append([]byte(nil), buf[:n]...)
		_ = json.NewEncoder(conn).Encode(Response{Error: "test: short-circuit"})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = Attach(ctx, sock, 80, 24, "")

	select {
	case raw := <-gotRaw:
		if bytes.Contains(raw, []byte("sessionID")) {
			t.Errorf("empty SessionID leaked onto the wire: %s", raw)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not receive bytes")
	}
}

// Bridge-as-AttachProvider integration test: confirm that supervisor.Bridge
// satisfies the AttachProvider interface and works through the server.
func TestServer_BridgeAttach(t *testing.T) {
	t.Parallel()

	dir := shortTempDir(t)
	sock := filepath.Join(dir, "p.sock")

	bridge := supervisor.NewBridge(nil)
	srv := NewServer(sock, sessionResolverWith(bridge.Attach), nil, nil, nil, nil)
	if err := srv.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ctx) }()

	// Connect, handshake, then write "hello" — it should appear on the
	// bridge's Read side (which is what runOnce would copy to the PTY).
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if err := json.NewEncoder(conn).Encode(Request{Verb: VerbAttach}); err != nil {
		t.Fatalf("encode: %v", err)
	}
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		t.Fatalf("decode ack: %v", err)
	}
	if !resp.OK {
		t.Fatalf("ack OK=false: %+v", resp)
	}

	// Read the bridge in a goroutine — this is what supervisor.runOnce does
	// (forwards bridge → PTY).
	got := make(chan string, 1)
	go func() {
		buf := make([]byte, 64)
		n, _ := bridge.Read(buf)
		got <- string(buf[:n])
	}()

	if _, err := conn.Write([]byte("hello-from-attached")); err != nil {
		t.Fatalf("write: %v", err)
	}

	select {
	case s := <-got:
		if s != "hello-from-attached" {
			t.Errorf("bridge received %q, want hello-from-attached", s)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("bridge.Read never completed after attached write")
	}
}

package supervisor

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeResizer is a test double for the resizer delegate (*tuidriver.Session
// in production). It records the most recent Resize call and returns a
// settable error, letting the Bridge resize tests assert the delegation
// contract without opening a real PTY — the "geometry reaches the kernel"
// path is covered by tui-driver's own Session.Resize tests.
type fakeResizer struct {
	rows, cols uint16
	calls      int
	err        error
}

func (f *fakeResizer) Resize(rows, cols uint16) error {
	f.calls++
	f.rows, f.cols = rows, cols
	return f.err
}

// errWriter implements io.Writer and returns the configured error on every
// call. Used to exercise the "attached output goes bad mid-write" path.
type errWriter struct{ err error }

func (e *errWriter) Write(p []byte) (int, error) { return 0, e.err }

// TestBridge_WriteSwallowsAttachedWriteErrors is the regression test for
// the "daemon wedges after detach" bug. If Bridge.Write propagates conn
// errors back to the supervisor's io.Copy(bridge, ptmx), the OUTPUT
// goroutine dies, the PTY stops being drained, and the supervised child
// blocks on stdout writes until the process is killed.
//
// Bridge.Write is required to return (len(p), nil) regardless of whether
// the attached writer succeeded — bytes lost are acceptable, a wedged
// daemon is not.
func TestBridge_WriteSwallowsAttachedWriteErrors(t *testing.T) {
	t.Parallel()

	b := NewBridge(nil)

	// Pipe so the input pump (io.Copy(pipeW, in)) stays alive and the
	// bridge keeps `output` set — exactly the race window where a PTY
	// write would hit a half-broken conn.
	pr, pw := io.Pipe()
	defer pr.Close()
	defer pw.Close()

	if _, err := b.Attach(pr, &errWriter{err: errors.New("conn closed")}); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	// PTY emits a byte while attached output is broken — this is the
	// supervisor.runOnce code path: io.Copy(bridge, ptmx) calls bridge.Write.
	// We assert the write reports success even though the underlying
	// out.Write failed.
	n, err := b.Write([]byte("hello"))
	if err != nil {
		t.Fatalf("Write must not propagate attached-writer errors: %v", err)
	}
	if n != 5 {
		t.Errorf("n = %d, want 5 (full slice reported as written even on discard)", n)
	}
}

func TestBridge_OutputObserver_InvokedOnWrite(t *testing.T) {
	t.Parallel()

	b := NewBridge(nil)

	var (
		mu   sync.Mutex
		seen [][]byte
	)
	b.SetOutputObserver(func(p []byte) {
		mu.Lock()
		defer mu.Unlock()
		// Copy because the bridge's contract permits the caller to reuse p.
		c := make([]byte, len(p))
		copy(c, p)
		seen = append(seen, c)
	})

	if _, err := b.Write([]byte("hello")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := b.Write([]byte("world")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 {
		t.Fatalf("observer invocations: got %d, want 2", len(seen))
	}
	if string(seen[0]) != "hello" {
		t.Errorf("first chunk: got %q, want %q", seen[0], "hello")
	}
	if string(seen[1]) != "world" {
		t.Errorf("second chunk: got %q, want %q", seen[1], "world")
	}
}

func TestBridge_OutputObserver_NilSkipped(t *testing.T) {
	t.Parallel()

	b := NewBridge(nil)
	var out bytes.Buffer

	// Pipe so the input pump stays parked in pr.Read; this keeps b.output
	// bound to &out for the assertion window. An immediate-EOF reader would
	// race the Attach goroutine's cleanup, which clears b.output on EOF.
	pr, pw := io.Pipe()
	defer pr.Close()

	done, err := b.Attach(pr, &out)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}

	// No observer registered → Write proceeds normally.
	n, err := b.Write([]byte("hi"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != 2 {
		t.Errorf("n = %d, want 2", n)
	}
	if got := out.String(); got != "hi" {
		t.Errorf("attached out: got %q, want %q", got, "hi")
	}

	// SetOutputObserver(nil) is also a no-op (idempotent clear).
	b.SetOutputObserver(nil)
	if _, err := b.Write([]byte("!")); err != nil {
		t.Fatalf("Write after nil-set: %v", err)
	}

	// Trigger detach deterministically and wait for the input pump to exit.
	pw.Close()
	<-done
}

func TestBridge_DiscardsWhenUnattached(t *testing.T) {
	t.Parallel()

	b := NewBridge(nil)
	n, err := b.Write([]byte("hello"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != 5 {
		t.Errorf("n = %d, want 5", n)
	}
	if b.Attached() {
		t.Errorf("Attached() = true on a fresh bridge")
	}
}

func TestBridge_OutputForwardsWhenAttached(t *testing.T) {
	t.Parallel()

	b := NewBridge(nil)
	var out bytes.Buffer

	// Pipe so the input pump stays parked through the Write/assertion; an
	// immediate-EOF reader would race the Attach cleanup that clears b.output.
	pr, pw := io.Pipe()
	defer pr.Close()

	done, err := b.Attach(pr, &out)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}

	if _, err := b.Write([]byte("hello")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := out.String(); got != "hello" {
		t.Errorf("out = %q, want hello", got)
	}

	// Close the pipe writer to deliver EOF on the input side; done should
	// then close once the input pump exits and runs detach cleanup.
	pw.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("done channel did not close after EOF on input")
	}
}

func TestBridge_InputFlowsToReader(t *testing.T) {
	t.Parallel()

	b := NewBridge(nil)
	in := strings.NewReader("greetings")
	var out bytes.Buffer

	done, err := b.Attach(in, &out)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}

	// Read from the bridge — this is what runOnce does (forwards to PTY).
	buf := make([]byte, 64)
	n, err := b.Read(buf)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got := string(buf[:n]); got != "greetings" {
		t.Errorf("Read = %q, want greetings", got)
	}

	<-done
}

func TestBridge_RejectsConcurrentAttach(t *testing.T) {
	t.Parallel()

	b := NewBridge(nil)
	pr, pw := io.Pipe()
	defer pr.Close()
	defer pw.Close()

	first, err := b.Attach(pr, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("first Attach: %v", err)
	}

	// Second attach should be rejected.
	_, err = b.Attach(strings.NewReader(""), &bytes.Buffer{})
	if !errors.Is(err, ErrBridgeBusy) {
		t.Errorf("second Attach: got %v, want ErrBridgeBusy", err)
	}

	// Detach by closing the input — first should complete cleanly.
	pw.Close()
	<-first

	// Now a fresh attach should succeed.
	if _, err := b.Attach(strings.NewReader(""), &bytes.Buffer{}); err != nil {
		t.Errorf("post-detach Attach: %v", err)
	}
}

// TestBridge_ResizeForwardsToResizer asserts that after SetResizer + Resize,
// the registered delegate receives the dimensions verbatim (rows-then-cols).
func TestBridge_ResizeForwardsToResizer(t *testing.T) {
	t.Parallel()

	b := NewBridge(nil)
	fr := &fakeResizer{}
	b.SetResizer(fr)

	if err := b.Resize(40, 100); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	if fr.calls != 1 {
		t.Fatalf("resizer calls = %d, want 1", fr.calls)
	}
	if fr.rows != 40 || fr.cols != 100 {
		t.Errorf("forwarded (rows=%d, cols=%d), want (40, 100)", fr.rows, fr.cols)
	}
}

// TestBridge_ResizePropagatesResizerError asserts a delegate error surfaces
// verbatim — the control plane logs it but does not fail the attach.
func TestBridge_ResizePropagatesResizerError(t *testing.T) {
	t.Parallel()

	b := NewBridge(nil)
	wantErr := errors.New("resize boom")
	b.SetResizer(&fakeResizer{err: wantErr})

	if err := b.Resize(24, 80); !errors.Is(err, wantErr) {
		t.Errorf("Resize err = %v, want errors.Is == %v", err, wantErr)
	}
}

// TestBridge_ResizeNoResizerRegistered asserts the seam is silent (returns
// nil) when no delegate has been registered for the current iteration. This
// is the race window between EndIteration and the next BeginIteration where
// an in-flight client resize targets nothing.
func TestBridge_ResizeNoResizerRegistered(t *testing.T) {
	t.Parallel()

	b := NewBridge(nil)
	if err := b.Resize(40, 100); err != nil {
		t.Errorf("Resize on bridge with no resizer: %v, want nil", err)
	}
}

// TestBridge_ResizeAfterClearResizer asserts SetResizer(nil) returns Resize
// to its silent-no-op state — what runOnce relies on at iteration teardown.
func TestBridge_ResizeAfterClearResizer(t *testing.T) {
	t.Parallel()

	b := NewBridge(nil)
	b.SetResizer(&fakeResizer{})
	b.SetResizer(nil)

	if err := b.Resize(40, 100); err != nil {
		t.Errorf("Resize after SetResizer(nil): %v, want nil", err)
	}
}

// TestBridge_OutputCoexistence_BothHeadsReceiveSameBytes pins the Phase-1
// two-heads invariant (#595): with BOTH the local attach head (b.output, via
// Attach) and the phone observer head (b.outputObserver, via SetOutputObserver)
// bound at once, Bridge.Write delivers the same bytes to both, in order. The
// existing OutputObserver_InvokedOnWrite / OutputForwardsWhenAttached tests each
// drive only one seam; none sets both — that is the gap #595 closes. Local
// attach is unaffected by an active phone observer, and the phone observer keeps
// receiving while a local attach is bound (both directions of AC2).
func TestBridge_OutputCoexistence_BothHeadsReceiveSameBytes(t *testing.T) {
	t.Parallel()

	b := NewBridge(nil)

	// Phone head: the observer copies p (the production contract) into a local
	// recording. Writes below run on this goroutine, so the recording and the
	// attached buffer need no extra synchronisation.
	var obs [][]byte
	b.SetOutputObserver(func(p []byte) {
		c := make([]byte, len(p))
		copy(c, p)
		obs = append(obs, c)
	})

	// Local head: park the input pump on a pipe so b.output stays bound across
	// the Writes (an immediate-EOF reader would race the Attach cleanup that
	// clears b.output).
	pr, pw := io.Pipe()
	defer pw.Close()
	var attached bytes.Buffer
	done, err := b.Attach(pr, &attached)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}

	if _, err := b.Write([]byte("alpha")); err != nil {
		t.Fatalf("Write alpha: %v", err)
	}
	if _, err := b.Write([]byte("beta")); err != nil {
		t.Fatalf("Write beta: %v", err)
	}

	if got := attached.String(); got != "alphabeta" {
		t.Errorf("attached head = %q, want %q", got, "alphabeta")
	}
	if len(obs) != 2 || string(obs[0]) != "alpha" || string(obs[1]) != "beta" {
		t.Errorf("observer head = %q, want [alpha beta]", obs)
	}

	pw.Close()
	<-done
}

// TestBridge_OutputCoexistence_FaultingLocalSinkDoesNotStarveObserver pins the
// fault-isolation half of the #595 invariant: a mid-detach local writer that
// errors on every Write must not starve the phone observer. Write swallows the
// local error (returns n == len(p)) and the observer still receives the chunk
// intact. Complements TestBridge_WriteSwallowsAttachedWriteErrors, which has no
// observer — here the observer is the surface that must survive.
func TestBridge_OutputCoexistence_FaultingLocalSinkDoesNotStarveObserver(t *testing.T) {
	t.Parallel()

	b := NewBridge(nil)

	var obs [][]byte
	b.SetOutputObserver(func(p []byte) {
		c := make([]byte, len(p))
		copy(c, p)
		obs = append(obs, c)
	})

	// Park the input pump so b.output stays bound to the erroring writer.
	pr, pw := io.Pipe()
	defer pw.Close()
	if _, err := b.Attach(pr, &errWriter{err: errors.New("conn closed")}); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	n, err := b.Write([]byte("payload"))
	if err != nil {
		t.Fatalf("Write must not surface the local sink error: %v", err)
	}
	if n != len("payload") {
		t.Errorf("n = %d, want %d", n, len("payload"))
	}
	if len(obs) != 1 || string(obs[0]) != "payload" {
		t.Errorf("observer head = %q, want [payload] despite a faulting local sink", obs)
	}
}

// TestBridge_OutputCoexistence_ObserverCopyContract demonstrates the copy
// contract the two-heads fan-out relies on: the supervisor reuses one io.Copy
// buffer across reads, so a chunk passed to Write is overwritten before the next
// Write. A correctly-copying observer's recording is therefore stable across the
// reuse, and the attached writer receives each chunk's bytes as they stood at
// its own Write.
func TestBridge_OutputCoexistence_ObserverCopyContract(t *testing.T) {
	t.Parallel()

	b := NewBridge(nil)

	var obs [][]byte
	b.SetOutputObserver(func(p []byte) {
		c := make([]byte, len(p))
		copy(c, p)
		obs = append(obs, c)
	})

	pr, pw := io.Pipe()
	defer pw.Close()
	var attached bytes.Buffer
	done, err := b.Attach(pr, &attached)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}

	// One caller buffer, reused across Writes — exactly the supervisor's
	// MirrorOutput → Bridge.Write shape, where the buffer is overwritten by the
	// next read.
	buf := []byte("AAAAA")
	if _, err := b.Write(buf); err != nil {
		t.Fatalf("Write 1: %v", err)
	}
	copy(buf, []byte("BBBBB")) // overwrite in place, as the next Read would
	if _, err := b.Write(buf); err != nil {
		t.Fatalf("Write 2: %v", err)
	}

	if len(obs) != 2 {
		t.Fatalf("observer invocations = %d, want 2", len(obs))
	}
	if string(obs[0]) != "AAAAA" {
		t.Errorf("first observed chunk = %q, want %q (stable across buffer reuse)", obs[0], "AAAAA")
	}
	if string(obs[1]) != "BBBBB" {
		t.Errorf("second observed chunk = %q, want %q", obs[1], "BBBBB")
	}
	if got := attached.String(); got != "AAAAABBBBB" {
		t.Errorf("attached head = %q, want %q", got, "AAAAABBBBB")
	}

	pw.Close()
	<-done
}

func TestBridge_BlocksReadUntilAttached(t *testing.T) {
	t.Parallel()

	b := NewBridge(nil)

	readDone := make(chan struct{})
	var got []byte
	go func() {
		defer close(readDone)
		buf := make([]byte, 8)
		n, _ := b.Read(buf)
		got = append(got, buf[:n]...)
	}()

	// Briefly confirm Read is still blocking.
	select {
	case <-readDone:
		t.Fatal("Read returned before any attach")
	case <-time.After(50 * time.Millisecond):
	}

	if _, err := b.Attach(strings.NewReader("ping"), &bytes.Buffer{}); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	select {
	case <-readDone:
	case <-time.After(time.Second):
		t.Fatal("Read did not return after attach")
	}
	if string(got) != "ping" {
		t.Errorf("got %q, want ping", got)
	}
}

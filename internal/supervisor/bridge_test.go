package supervisor

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

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

	done, err := b.Attach(strings.NewReader(""), &out)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}

	if _, err := b.Write([]byte("hello")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := out.String(); got != "hello" {
		t.Errorf("out = %q, want hello", got)
	}

	// `in` was an empty reader — io.Copy on it returns immediately, so the
	// done channel should fire promptly.
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

// TestBridge_ResizeAppliesToPTY opens a real PTY pair and verifies that
// after SetPTY + Resize, pty.Getsize reads back the dimensions we set.
// Skipped on hosts where pty.Open is unavailable (sandboxed CI etc.) —
// matches the e2e harness's pattern in internal/e2e/attach_pty.go.
func TestBridge_ResizeAppliesToPTY(t *testing.T) {
	t.Parallel()

	master, slave, err := pty.Open()
	if err != nil {
		t.Skipf("pty.Open unavailable: %v", err)
	}
	defer master.Close()
	defer slave.Close()

	b := NewBridge(nil)
	b.SetPTY(master)

	if err := b.Resize(40, 100); err != nil {
		t.Fatalf("Resize: %v", err)
	}

	rows, cols, err := pty.Getsize(master)
	if err != nil {
		t.Fatalf("pty.Getsize: %v", err)
	}
	if rows != 40 || cols != 100 {
		t.Errorf("size = (%d rows, %d cols), want (40, 100)", rows, cols)
	}
}

// TestBridge_ResizeNoPTYRegistered asserts the seam is silent (returns nil)
// when no PTY has been registered for the current iteration. This is the
// race window between EndIteration and the next BeginIteration where an
// in-flight client resize targets nothing.
func TestBridge_ResizeNoPTYRegistered(t *testing.T) {
	t.Parallel()

	b := NewBridge(nil)
	if err := b.Resize(40, 100); err != nil {
		t.Errorf("Resize on bridge with no PTY: %v, want nil", err)
	}
}

// TestBridge_ResizeAfterClearPTY asserts SetPTY(nil) returns Resize to its
// silent-no-op state — this is what runOnce relies on at iteration teardown.
func TestBridge_ResizeAfterClearPTY(t *testing.T) {
	t.Parallel()

	master, slave, err := pty.Open()
	if err != nil {
		t.Skipf("pty.Open unavailable: %v", err)
	}
	defer master.Close()
	defer slave.Close()

	b := NewBridge(nil)
	b.SetPTY(master)
	b.SetPTY(nil)

	if err := b.Resize(40, 100); err != nil {
		t.Errorf("Resize after SetPTY(nil): %v, want nil", err)
	}
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

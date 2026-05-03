//go:build e2e

package e2e

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"
)

// TestHelperProcess is not a real test. It is re-execed by pyry as the
// supervised "claude" when the e2e attach harness is configured:
//
//	GO_TEST_HELPER_PROCESS=1
//	GO_TEST_HELPER_MODE=echo
//
// Mode "echo": disable ECHO/ICANON on stdin (raw mode) so the kernel
// does not double-emit bytes through the supervisor's PTY line
// discipline. On startup the helper emits a deterministic marker line
// (`PYRY_E2E_STARTED pid=<pid>\n`) so restart-survival tests can observe
// each respawn distinctly. Then it line-buffers stdin → stdout. Two
// special lines are intercepted:
//
//   - `__EXIT__\n` — exit non-zero before echoing, so the supervisor
//     observes a child crash and respawns. Used by
//     TestE2E_Attach_SurvivesClaudeRestart.
//
//   - `__PID__\n` — re-emit the startup marker (with the helper's
//     current PID) without echoing the probe. The on-startup marker
//     races against attach-client connection — when no client is
//     attached yet, the bridge silently discards the bytes. The probe
//     gives tests a deterministic way to capture the PID after the
//     attach is wired. Not echoed back so it doesn't pollute round-trip
//     verifications.
//
// All other input round-trips through stdout at line granularity, which
// preserves the byte contract TestE2E_Attach_RoundTripsBytes relies on.
//
// On every SIGWINCH the helper emits a deterministic line of the form
// `winsize rows=N cols=M\n` (rows-first to match Bridge.Resize /
// Session.Resize field order). TestE2E_Attach_HandlesSIGWINCH uses this
// to observe live resize propagation. Other tests match their own
// payloads via bytes.Contains / regex with unique anchors, so the extra
// line is harmless to them.
//
// Process exit on stdin EOF (pyry SIGKILLs the child via the
// exec.CommandContext deadline, which closes the supervisor's PTY
// master and surfaces as EOF on the slave).
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_TEST_HELPER_PROCESS") != "1" {
		return
	}
	mode := os.Getenv("GO_TEST_HELPER_MODE")
	switch mode {
	case "echo":
		if term.IsTerminal(int(os.Stdin.Fd())) {
			if _, err := term.MakeRaw(int(os.Stdin.Fd())); err != nil {
				os.Exit(98)
			}
		}
		fmt.Fprintf(os.Stdout, "PYRY_E2E_STARTED pid=%d\n", os.Getpid())

		// SIGWINCH watcher. Emits a deterministic `winsize rows=N cols=M\n`
		// line per signal so TestE2E_Attach_HandlesSIGWINCH can observe
		// live resize propagation without races. Pattern mirrors
		// internal/supervisor/winsize.go: signal.Notify + buffered chan(1)
		// + goroutine + pty.GetsizeFull(os.Stdin) guarded by IsTerminal.
		// No teardown plumbing — the helper exits on stdin EOF / __EXIT__,
		// which terminates the goroutine implicitly.
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGWINCH)
		go func() {
			for range sigCh {
				if !term.IsTerminal(int(os.Stdin.Fd())) {
					continue
				}
				size, err := pty.GetsizeFull(os.Stdin)
				if err != nil {
					continue
				}
				fmt.Fprintf(os.Stdout, "winsize rows=%d cols=%d\n", size.Rows, size.Cols)
			}
		}()

		buf := make([]byte, 4096)
		var line []byte
		for {
			n, err := os.Stdin.Read(buf)
			for i := 0; i < n; i++ {
				b := buf[i]
				if b == '\n' {
					switch string(line) {
					case "__EXIT__":
						os.Exit(1)
					case "__PID__":
						fmt.Fprintf(os.Stdout, "PYRY_E2E_STARTED pid=%d\n", os.Getpid())
						line = line[:0]
						continue
					}
					line = append(line, b)
					if _, werr := os.Stdout.Write(line); werr != nil {
						os.Exit(97)
					}
					line = line[:0]
				} else {
					line = append(line, b)
				}
			}
			if err != nil {
				if len(line) > 0 {
					_, _ = os.Stdout.Write(line)
				}
				if err == io.EOF {
					os.Exit(0)
				}
				os.Exit(96)
			}
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown GO_TEST_HELPER_MODE: %q\n", mode)
		os.Exit(99)
	}
}

// TestE2E_Attach_RoundTripsBytes proves that a byte typed at a user's
// terminal travels: terminal → attach client → control socket → bridge
// → supervisor PTY → claude → and back, by writing a known payload into
// the attach PTY's master and asserting the same bytes are observed
// coming back through the master side within a generous deadline.
func TestE2E_Attach_RoundTripsBytes(t *testing.T) {
	a := StartAttach(t, "")

	payload := []byte("pyry-attach-roundtrip-" + tinyNonce() + "\n")

	if _, err := a.Master.Write(payload); err != nil {
		t.Fatalf("write master: %v", err)
	}

	if err := readUntilContains(a.Master, payload, 5*time.Second); err != nil {
		t.Fatalf("did not observe payload back: %v", err)
	}
}

// tinyNonce returns a short random hex string so concurrent test
// invocations don't accidentally match each other's payloads.
func tinyNonce() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// readUntilContains reads from r in a loop in a background goroutine
// until needle appears in the accumulated bytes or the overall deadline
// elapses. PTY master fds on darwin do not support SetReadDeadline
// (the runtime poller reports ErrNoDeadline), so the timeout is enforced
// by the caller rather than per-read.
//
// On timeout the reader goroutine is left running — the harness's
// teardown closes Master, which unblocks the Read with EOF.
func readUntilContains(r *os.File, needle []byte, total time.Duration) error {
	type readResult struct {
		buf []byte
		err error
	}
	ch := make(chan readResult, 1)
	var seen bytes.Buffer

	read := func() {
		buf := make([]byte, 4096)
		n, err := r.Read(buf)
		ch <- readResult{buf: buf[:n], err: err}
	}

	deadline := time.Now().Add(total)
	go read()
	for {
		select {
		case res := <-ch:
			if len(res.buf) > 0 {
				seen.Write(res.buf)
				if bytes.Contains(seen.Bytes(), needle) {
					return nil
				}
			}
			if res.err != nil {
				return fmt.Errorf("read: %w (seen %q)", res.err, seen.Bytes())
			}
			go read()
		case <-time.After(time.Until(deadline)):
			return fmt.Errorf("timeout after %s; seen %d bytes: %q", total, seen.Len(), seen.Bytes())
		}
	}
}

// TestE2E_Attach_HandlesSIGWINCH proves live SIGWINCH propagation through
// the full attach pipeline by resizing the harness's client-side master
// PTY and asserting the supervised child observes the new dimensions on
// stdout within a generous deadline.
//
// Path under test:
//
//	pty.Setsize(master) → kernel SIGWINCH on slave's process group (the
//	attach client) → startWinsizeWatcher → SendResize → server
//	handleResize → Session.Resize → Bridge.Resize → pty.Setsize on
//	supervisor's ptmx → kernel SIGWINCH on helper → helper emits
//	"winsize rows=42 cols=117\n".
//
// The PTY availability skip lives in StartAttach; this test does not
// re-probe.
func TestE2E_Attach_HandlesSIGWINCH(t *testing.T) {
	a := StartAttach(t, "")

	// Pick dimensions unlikely to match the slave's default initial size
	// so any handshake-driven initial resize emission cannot collide
	// with the marker we're matching for.
	target := &pty.Winsize{Rows: 42, Cols: 117}
	if err := pty.Setsize(a.Master, target); err != nil {
		t.Fatalf("Setsize master: %v", err)
	}

	needle := []byte("winsize rows=42 cols=117\n")
	if err := readUntilContains(a.Master, needle, 5*time.Second); err != nil {
		t.Fatalf("did not observe new winsize: %v", err)
	}
}

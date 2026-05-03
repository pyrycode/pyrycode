//go:build e2e

package e2e

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"testing"
	"time"

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
// discipline, then io.Copy stdin → stdout until stdin closes.
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
		_, _ = io.Copy(os.Stdout, os.Stdin)
		os.Exit(0)
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

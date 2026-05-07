//go:build e2e

package e2e

import (
	"bytes"
	"testing"
	"time"
)

// TestE2E_AttachStdio_BytesRoundTrip proves that a byte written into a
// programmatic parent's stdin-of-`pyry attach --stdio` travels: parent
// pipe → attach client → control socket → bridge → supervisor PTY →
// supervised helper → and back through the attach client's stdout into
// the parent's read pipe. The helper is the e2e test binary running
// TestHelperProcess in echo mode (line-buffered). The test writes one
// nonce-tagged line and asserts the same line appears on output within
// a generous deadline; pre-line banner bytes (the helper's startup
// "PYRY_E2E_STARTED pid=…\n") are accumulated and ignored, identical
// to the PTY harness's readUntilContains discipline.
//
// This test is the smallest meaningful exercise of the stdio-attach
// shape end-to-end. Follow-up tickets (1.3a-e2e-no-pty: assert no PTY
// fd open in the client; 1.3c-2-e2e-*: foreground auto-attach) reuse
// startStdioAttach for their own scenarios.
func TestE2E_AttachStdio_BytesRoundTrip(t *testing.T) {
	// Blocked on #167 — `pyry attach --stdio` is rejected by
	// parseClientFlags before parseAttachArgs runs, so the harness can't
	// drive the CLI through. Once #167 lands, remove this skip and the
	// existing harness body should make the test pass unchanged.
	t.Skip("blocked on #167 — pyry attach --stdio rejected by parseClientFlags")

	c := startStdioAttach(t, "stdio-roundtrip")

	// Trailing \n is required: the helper's echo mode is line-buffered
	// and only flushes a line when it sees \n.
	payload := []byte("pyry-stdio-roundtrip-" + tinyNonce() + "\n")

	if _, err := c.Write(payload); err != nil {
		t.Fatalf("write: %v", err)
	}

	seen, err := c.ReadUntil(payload, 5*time.Second)
	if err != nil {
		t.Fatalf("did not observe payload back: %v\nstderr:\n%s",
			err, c.Stderr.String())
	}

	// Defensive: the needle must appear *after* any banner. The
	// ReadUntil contract already guarantees needle ∈ seen on nil err,
	// so this is a smoke check that the buffer wasn't returned empty
	// by an unlikely API regression.
	if !bytes.Contains(seen, payload) {
		t.Fatalf("ReadUntil returned without payload in buffer: %q", seen)
	}
}

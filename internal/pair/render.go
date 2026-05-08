package pair

import (
	"fmt"
	"io"

	"github.com/mdp/qrterminal/v3"
)

// Render writes a paired-device-friendly representation of p to w:
//
//  1. A QR symbol of Encode(p), drawn with UTF-8 half-block characters
//     (the densest terminal form that scans reliably).
//  2. A blank line.
//  3. The output of Encode(p) on its own line.
//  4. A one-line instruction telling the user to either scan the QR
//     with the Pyrycode mobile app or paste the string above into the
//     app's pairing screen.
//
// Render returns the first error encountered while writing to w; on
// error, w may have received a prefix of the intended output but the
// error is propagated rather than swallowed. Render does not retry.
//
// Render does not log, persist, or otherwise duplicate the rendered
// bytes — its output contains the plaintext device-token and is a
// one-time display surface (see docs/protocol-mobile.md § "Token leak
// via phone"). Callers MUST NOT log the writer's destination, MUST NOT
// capture this output into any context that is persisted (CI logs,
// telemetry, error reports), and MUST treat the calling goroutine's
// stdout as the only intended sink.
func Render(p Payload, w io.Writer) error {
	encoded := Encode(p)
	tw := &errTrackingWriter{w: w}
	qrterminal.GenerateHalfBlock(encoded, qrterminal.M, tw)
	fmt.Fprintln(tw)
	fmt.Fprintln(tw, encoded)
	fmt.Fprintln(tw, "Scan the QR with the Pyrycode mobile app, or paste the string above into the app's pairing screen.")
	return tw.err
}

// errTrackingWriter wraps an io.Writer, captures the first non-nil
// write error, and short-circuits subsequent writes so that a broken
// underlying writer is not called repeatedly.
type errTrackingWriter struct {
	w   io.Writer
	err error
}

func (t *errTrackingWriter) Write(p []byte) (int, error) {
	if t.err != nil {
		return 0, t.err
	}
	n, err := t.w.Write(p)
	if err != nil {
		t.err = err
	}
	return n, err
}

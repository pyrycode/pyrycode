package pair

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"strings"

	"github.com/mdp/qrterminal/v3"
	"golang.org/x/crypto/blake2s"
)

// Render writes a paired-device-friendly representation of p to w:
//
//  1. A QR symbol of Encode(p), drawn with UTF-8 half-block characters
//     (the densest terminal form that scans reliably).
//  2. A blank line.
//  3. The output of Encode(p) on its own line.
//  4. The static-key fingerprint on its own line, formatted as
//     "Static-key fp: aa:bb:...  (verify this matches the fingerprint
//     shown on your phone)". The fingerprint is Fingerprint(pubkey)
//     applied to the base64-decoded p.ServerStaticPubkey; if that
//     decoding fails or yields a wrong-length value (only reachable
//     when the caller hand-builds a Payload that Decode would reject),
//     Render emits "Static-key fp: <invalid>" instead of panicking.
//  5. A one-line instruction telling the user to either scan the QR
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
	fmt.Fprintln(tw, fingerprintLine(p.ServerStaticPubkey))
	fmt.Fprintln(tw, "Scan the QR with the Pyrycode mobile app, or paste the string above into the app's pairing screen.")
	return tw.err
}

// fingerprintLine returns the rendered "Static-key fp: …" line.
// pubkeyB64 is the base64.StdEncoding-encoded raw 32-byte X25519
// public point. Decode-accepted payloads always carry a valid value
// here; hand-built Payload structs with malformed input yield the
// literal "<invalid>" placeholder so the output surface remains
// non-panicking for callers that bypass Decode.
func fingerprintLine(pubkeyB64 string) string {
	raw, err := base64.StdEncoding.DecodeString(pubkeyB64)
	if err != nil || len(raw) != 32 {
		return "Static-key fp: <invalid>  (verify this matches the fingerprint shown on your phone)"
	}
	var pub [32]byte
	copy(pub[:], raw)
	return "Static-key fp: " + Fingerprint(pub) + "  (verify this matches the fingerprint shown on your phone)"
}

// Fingerprint returns the verifying-string form of pubkey:
// BLAKE2s-256(pubkey) truncated to 8 bytes, formatted as colon-
// separated lowercase hex ("aa:bb:cc:dd:ee:ff:11:22"). Exactly 23
// characters (8*2 hex digits + 7 colons).
//
// The 8-byte / 64-bit width is load-bearing per
// docs/protocol-mobile.md § Security review. A 32-bit truncation is
// brute-forceable (~2^32 preimage search); do not narrow it.
func Fingerprint(pubkey [32]byte) string {
	sum := blake2s.Sum256(pubkey[:])
	hexed := hex.EncodeToString(sum[:8])
	var b strings.Builder
	b.Grow(23)
	for i := 0; i < 8; i++ {
		if i > 0 {
			b.WriteByte(':')
		}
		b.WriteString(hexed[i*2 : i*2+2])
	}
	return b.String()
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

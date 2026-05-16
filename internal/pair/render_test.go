package pair

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/identity"
)

const (
	instructionLine = "Scan the QR with the Pyrycode mobile app, or paste the string above into the app's pairing screen."
	// fingerprintZeros is the fingerprint emitted for a payload whose
	// ServerStaticPubkey decodes to 32 zero bytes — matches the
	// hardcoded vector in TestFingerprint_FixedVector.
	fingerprintZeros = "32:0b:5e:a9:9e:65:3b:c2"
	fingerprintHint  = "verify this matches the fingerprint shown on your phone"
)

// qrBlockGlyphs are the UTF-8 half-block code points qrterminal/v3
// emits when drawing a QR symbol via GenerateHalfBlock.
var qrBlockGlyphs = []string{"█", "▀", "▄"}

func containsAnyQRBlock(s string) bool {
	for _, g := range qrBlockGlyphs {
		if strings.Contains(s, g) {
			return true
		}
	}
	return false
}

func samplePayload() Payload {
	return Payload{
		Server:             identity.ServerID(testServerID),
		Relay:              testRelay,
		Token:              testToken,
		ServerStaticPubkey: testStaticPubkeyB64,
	}
}

func TestRender_Format_Happy(t *testing.T) {
	t.Parallel()
	p := samplePayload()
	var buf bytes.Buffer
	if err := Render(p, &buf); err != nil {
		t.Fatalf("Render returned error: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatal("Render wrote no bytes")
	}
	out := buf.String()
	if !containsAnyQRBlock(out) {
		t.Error("rendered output did not contain a QR block code point")
	}
	if !strings.Contains(out, Encode(p)) {
		t.Error("Encode(p) substring not found in rendered output")
	}
	if !strings.Contains(out, "Static-key fp: "+fingerprintZeros) {
		t.Errorf("fingerprint line missing or wrong value (want %q in output):\n%s", "Static-key fp: "+fingerprintZeros, out)
	}
	if !strings.Contains(out, fingerprintHint) {
		t.Errorf("fingerprint hint %q missing from output:\n%s", fingerprintHint, out)
	}
	if !strings.Contains(out, instructionLine) {
		t.Error("instruction line not found in rendered output")
	}
}

func TestRender_FieldOrder(t *testing.T) {
	t.Parallel()
	p := samplePayload()
	var buf bytes.Buffer
	if err := Render(p, &buf); err != nil {
		t.Fatalf("Render returned error: %v", err)
	}
	out := buf.String()
	encoded := Encode(p)
	idx := strings.Index(out, encoded)
	if idx < 0 {
		t.Fatal("Encode(p) substring not found in rendered output")
	}
	prefix := out[:idx]
	suffix := out[idx+len(encoded):]
	if !containsAnyQRBlock(prefix) {
		t.Error("QR block code point did not precede the encoded payload")
	}
	if !strings.Contains(suffix, instructionLine) {
		t.Error("instruction line did not follow the encoded payload")
	}
	// Fingerprint line must sit between the encoded payload and the
	// instruction line — the QR-then-payload-then-fingerprint-then-
	// instruction order is the contract from § UX implications.
	fpIdx := strings.Index(out, "Static-key fp: ")
	instrIdx := strings.Index(out, instructionLine)
	if fpIdx < 0 || instrIdx < 0 {
		t.Fatalf("fingerprint or instruction line missing: fpIdx=%d instrIdx=%d", fpIdx, instrIdx)
	}
	if !(idx < fpIdx && fpIdx < instrIdx) {
		t.Errorf("expected order encoded(%d) < fingerprint(%d) < instruction(%d)", idx, fpIdx, instrIdx)
	}
	// At least one blank line must separate the last QR row from the
	// encoded payload line: trim the encoded line back to the previous
	// newline, then look for "\n\n" (or "\n" + whitespace-only line)
	// in what remains.
	lineStart := strings.LastIndex(prefix, "\n")
	if lineStart < 0 {
		t.Fatal("no newline before encoded payload line")
	}
	beforeEncodedLine := prefix[:lineStart]
	if !strings.HasSuffix(beforeEncodedLine, "\n") {
		t.Error("expected blank line between QR symbol and encoded payload")
	}
}

// TestRender_InvalidPubkey_DoesNotPanic confirms the non-Decode-built
// fallback in fingerprintLine: a hand-built Payload with malformed
// ServerStaticPubkey must yield "Static-key fp: <invalid>" without
// panicking, since Render's contract says it accepts unvalidated
// Payload structs (Encode's doc-comment names this).
func TestRender_InvalidPubkey_DoesNotPanic(t *testing.T) {
	t.Parallel()
	p := samplePayload()
	p.ServerStaticPubkey = "!!!"
	var buf bytes.Buffer
	err := Render(p, &buf)
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}
	if !strings.Contains(buf.String(), "Static-key fp: <invalid>") {
		t.Errorf("expected Static-key fp: <invalid> in output:\n%s", buf.String())
	}
}

func TestRender_Deterministic(t *testing.T) {
	t.Parallel()
	p := samplePayload()
	var a, b bytes.Buffer
	if err := Render(p, &a); err != nil {
		t.Fatalf("Render(a) error: %v", err)
	}
	if err := Render(p, &b); err != nil {
		t.Fatalf("Render(b) error: %v", err)
	}
	if !bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Error("Render is not deterministic for the same Payload")
	}
}

// shortWriteWriter returns io.ErrShortWrite on every Write.
type shortWriteWriter struct {
	calls int
}

func (s *shortWriteWriter) Write(p []byte) (int, error) {
	s.calls++
	return 0, io.ErrShortWrite
}

func TestRender_WriterError(t *testing.T) {
	t.Parallel()
	p := samplePayload()
	w := &shortWriteWriter{}
	err := Render(p, w)
	if err == nil {
		t.Fatal("Render returned nil error for failing writer")
	}
	if !errors.Is(err, io.ErrShortWrite) {
		t.Errorf("expected error to wrap io.ErrShortWrite, got: %v", err)
	}
}

// panicAfterFirstErrorWriter returns io.ErrShortWrite on the first
// call and panics on every subsequent call. Render's
// errTrackingWriter must short-circuit so this writer is never called
// after the first error.
type panicAfterFirstErrorWriter struct {
	called bool
}

func (p *panicAfterFirstErrorWriter) Write(b []byte) (int, error) {
	if p.called {
		panic("writer called after first error")
	}
	p.called = true
	return 0, io.ErrShortWrite
}

func TestRender_DoesNotPanicOnBrokenWriter(t *testing.T) {
	t.Parallel()
	p := samplePayload()
	w := &panicAfterFirstErrorWriter{}
	err := Render(p, w)
	if err == nil {
		t.Fatal("Render returned nil error for failing writer")
	}
	if !errors.Is(err, io.ErrShortWrite) {
		t.Errorf("expected error to wrap io.ErrShortWrite, got: %v", err)
	}
}

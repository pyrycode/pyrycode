package protocol

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
	"time"
)

func canonical(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Compact(&buf, b); err != nil {
		t.Fatalf("compact: %v", err)
	}
	return buf.Bytes()
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return b
}

func TestEnvelope_RoundTrip_Full(t *testing.T) {
	raw := readFixture(t, "envelope_full.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if env.ID != 42 {
		t.Errorf("ID: got %d, want 42", env.ID)
	}
	if env.Type != TypeSendMessage {
		t.Errorf("Type: got %q, want %q", env.Type, TypeSendMessage)
	}
	wantTS, err := time.Parse(time.RFC3339Nano, "2026-05-08T10:33:14.012Z")
	if err != nil {
		t.Fatalf("parse expected ts: %v", err)
	}
	if !env.TS.Equal(wantTS) {
		t.Errorf("TS: got %v, want %v", env.TS, wantTS)
	}
	if env.InReplyTo == nil || *env.InReplyTo != 17 {
		t.Errorf("InReplyTo: got %v, want pointer to 17", env.InReplyTo)
	}
	if env.PayloadEncrypted {
		t.Errorf("PayloadEncrypted: got true, want false (absent in fixture)")
	}
	if len(env.Payload) == 0 {
		t.Errorf("Payload: empty, want non-empty")
	}

	out, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Equal(canonical(t, out), canonical(t, raw)) {
		t.Errorf("round-trip bytes differ:\n got: %s\nwant: %s", out, raw)
	}
}

func TestEnvelope_RoundTrip_Minimal(t *testing.T) {
	raw := readFixture(t, "envelope_minimal.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if env.ID != 1 || env.Type != TypeHello {
		t.Errorf("got id=%d type=%q, want 1/%q", env.ID, env.Type, TypeHello)
	}
	if env.InReplyTo != nil {
		t.Errorf("InReplyTo: got %v, want nil", env.InReplyTo)
	}
	if env.PayloadEncrypted {
		t.Errorf("PayloadEncrypted: got true, want false")
	}

	out, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Equal(canonical(t, out), canonical(t, raw)) {
		t.Errorf("round-trip bytes differ:\n got: %s\nwant: %s", out, raw)
	}
}

func TestRoutingEnvelope_RoundTrip(t *testing.T) {
	raw := readFixture(t, "routing_envelope.json")

	var re RoutingEnvelope
	if err := json.Unmarshal(raw, &re); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if re.ConnID != "c-7f3a" {
		t.Errorf("ConnID: got %q, want %q", re.ConnID, "c-7f3a")
	}

	// Frame must be byte-preserving across the splice (relay never parses
	// payloads). Decoding it as an Envelope must yield the same logical
	// content as the standalone envelope_full fixture.
	var inner Envelope
	if err := json.Unmarshal(re.Frame, &inner); err != nil {
		t.Fatalf("unmarshal inner frame: %v", err)
	}
	if inner.ID != 42 || inner.Type != TypeSendMessage {
		t.Errorf("inner: got id=%d type=%q, want 42/%q", inner.ID, inner.Type, TypeSendMessage)
	}

	out, err := json.Marshal(re)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Equal(canonical(t, out), canonical(t, raw)) {
		t.Errorf("round-trip bytes differ:\n got: %s\nwant: %s", out, raw)
	}
}

// TestRoutingEnvelope_TokenOmitempty pins that an unset Token does not
// land on the wire (preserves byte-identical encoding for non-auth
// paths) and that a non-empty Token round-trips faithfully.
func TestRoutingEnvelope_TokenOmitempty(t *testing.T) {
	re := RoutingEnvelope{ConnID: "c-1", Frame: json.RawMessage(`{}`)}
	out, err := json.Marshal(re)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if bytes.Contains(out, []byte(`"token"`)) {
		t.Errorf("empty Token should be omitted; got %s", out)
	}

	re.Token = "abc123"
	out, err = json.Marshal(re)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Contains(out, []byte(`"token":"abc123"`)) {
		t.Errorf("non-empty Token should encode; got %s", out)
	}
	var back RoutingEnvelope
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Token != "abc123" {
		t.Errorf("Token round-trip: got %q, want %q", back.Token, "abc123")
	}
}

// TestEnvelope_EventIDOmitempty pins that a nil EventID is absent from the
// wire (so every non-interactive / v1 construction site stays byte-identical)
// and that a non-nil EventID round-trips faithfully. AC-4: absent, not null/0.
func TestEnvelope_EventIDOmitempty(t *testing.T) {
	env := Envelope{ID: 1, Type: TypeHello, Payload: json.RawMessage(`{}`)}
	out, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if bytes.Contains(out, []byte(`"event_id"`)) {
		t.Errorf("nil EventID should be omitted; got %s", out)
	}

	id := uint64(7)
	env.EventID = &id
	out, err = json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Contains(out, []byte(`"event_id":7`)) {
		t.Errorf("non-nil EventID should encode; got %s", out)
	}
	var back Envelope
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.EventID == nil || *back.EventID != 7 {
		t.Errorf("EventID round-trip: got %v, want pointer to 7", back.EventID)
	}
}

// TestRoutingEnvelope_CloseCodeOmitempty pins that CloseCode=0 is
// omitted and that a non-zero CloseCode round-trips.
func TestRoutingEnvelope_CloseCodeOmitempty(t *testing.T) {
	re := RoutingEnvelope{ConnID: "c-1", Frame: json.RawMessage(`{}`)}
	out, err := json.Marshal(re)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if bytes.Contains(out, []byte(`"close_code"`)) {
		t.Errorf("CloseCode=0 should be omitted; got %s", out)
	}

	re.CloseCode = 4401
	out, err = json.Marshal(re)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Contains(out, []byte(`"close_code":4401`)) {
		t.Errorf("non-zero CloseCode should encode; got %s", out)
	}
	var back RoutingEnvelope
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.CloseCode != 4401 {
		t.Errorf("CloseCode round-trip: got %d, want %d", back.CloseCode, 4401)
	}
}

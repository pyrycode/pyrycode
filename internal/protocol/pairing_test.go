package protocol

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

// The fixtures' credential is SYNTHETIC and always has been: the token is the
// byte pattern 0f1e2d3c… repeated and the static pubkey is base64 of the bytes
// 0x40–0x5f. Nothing here was ever a live pairing, and nothing in this package
// mints one. The string is nonetheless a real pair.Encode output — it decodes
// through pair.Decode into the four documented fields — so a reader can check
// the shape the reply promises rather than take it on faith.

// TestMintPairingPayload_RoundTrip pins the REQUEST against its committed
// fixture: the envelope type, the absent in_reply_to, the id its reply answers,
// and the one payload field.
//
// It is also the only test anywhere that pins the wire STRING "mint_pairing".
// None of the four classifier registries can — v2OnlyTypes, TestIsKnownAppType's
// table, TestTypeConstants_V1V2Partition's slice and relay_guard_test.go's
// excludedTypes all key on the Go IDENTIFIER, so mutating the constant's value to
// "mint_pairings" moves consistently through every one of them and reddens none.
// Only a fixture read off disk carries the literal.
func TestMintPairingPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "mint_pairing.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeMintPairing {
		t.Errorf("Type: got %q, want %q", env.Type, TypeMintPairing)
	}
	if env.InReplyTo != nil {
		t.Errorf("InReplyTo: got pointer to %d, want nil — this frame is a request, not a reply", *env.InReplyTo)
	}
	// The value the reply correlates back to. Asserted here so the pair cannot
	// drift into two unrelated fixtures describing two different schemes.
	if env.ID != 204 {
		t.Errorf("ID: got %d, want 204 (the in_reply_to pairing_minted.json carries)", env.ID)
	}

	var payload MintPairingPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if want := "Kitchen iPad"; payload.DeviceName != want {
		t.Errorf("DeviceName: got %q, want %q", payload.DeviceName, want)
	}

	roundTripEnvelope(t, env, payload, raw)
}

// TestPairingMintedPayload_RoundTrip pins the REPLY against its committed
// fixture, and the non-nil in_reply_to is the structural half of the
// classification cmd/pyry/relay_guard_test.go's excludedTypes records: this frame
// is a reply, and correlation runs TO it rather than from it. Its sibling above
// asserts nil on the same tie, so the pair is a difference in shape rather than
// in values.
//
// The pairing string is compared in full rather than by prefix or by length. It
// is the credential the whole frame exists to carry, and a truncation, a
// re-encoding under the padded alphabet, or a wrapped copy would all survive a
// looser assertion while producing a string no client can paste.
func TestPairingMintedPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "pairing_minted.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypePairingMinted {
		t.Errorf("Type: got %q, want %q", env.Type, TypePairingMinted)
	}
	if env.InReplyTo == nil {
		t.Fatalf("InReplyTo: got nil, want pointer to 204 — this frame is a reply and correlation rides the envelope")
	}
	if got := *env.InReplyTo; got != 204 {
		t.Errorf("InReplyTo: got %d, want 204 (the id mint_pairing.json carries)", got)
	}

	var payload PairingMintedPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	want := "eyJzZXJ2ZXIiOiIzYjljMWQ3ZS00MmEwLTRmMTgtOWM2Yi01ZTBkOGExN2YzNDIiLCJyZWxheSI6IndzczovL3JlbGF5LnB5cnljb2RlLmRldiIsInRva2VuIjoiMGYxZTJkM2M0YjVhNjk3ODg3OTZhNWI0YzNkMmUxZjAwZjFlMmQzYzRiNWE2OTc4ODc5NmE1YjRjM2QyZTFmMCIsInNlcnZlcl9zdGF0aWNfcHVia2V5IjoiUUVGQ1EwUkZSa2RJU1VwTFRFMU9UMUJSVWxOVVZWWlhXRmxhVzF4ZFhsOD0ifQ"
	if payload.Pairing != want {
		t.Errorf("Pairing: got %q, want the committed encoded string verbatim", payload.Pairing)
	}

	roundTripEnvelope(t, env, payload, raw)
}

// TestMintPairingPayload_WireKeys pins the request payload's COMPLETE key set.
// It is the machine-checked form of the frame's central omission — NO field for
// the remote-permissions flag — rather than a property a reviewer has to notice.
//
// That omission is a security property, not tidiness. devices.Device's
// AllowRemotePermissions is documented as never set or carried over the wire, and
// it is the one gate on answering a permission, trust or destructive modal from a
// remote surface. A field here would let an already-paired client mint a device
// MORE privileged than an operator granted at the CLI, so it reddens by
// construction instead of being caught in review.
//
// Also pinned by absence: no request-id key (correlation rides the envelope's
// in_reply_to), and no nonce, token or server field — nothing a client sends is
// input to what the daemon mints.
//
// It is load-bearing beyond the round trip above for the reason that round trip
// cannot cover. An added field breaks the byte comparison there too, but only
// until somebody regenerates the fixture; regenerate it under the same mutant and
// the round trip goes green again while this stays red.
//
// Two-sided on purpose — every expected key present AND no unexpected key —
// because a one-sided containment check is exactly what lets an added field
// through, and that is the whole mutant class this test exists for.
func TestMintPairingPayload_WireKeys(t *testing.T) {
	b, err := json.Marshal(MintPairingPayload{DeviceName: "Kitchen iPad"})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal payload into key set: %v", err)
	}

	want := map[string]bool{"device_name": true}
	for k := range got {
		if !want[k] {
			t.Errorf("unexpected wire key %q: the payload's key set is fixed at %v, and this frame must carry no remote-permissions flag, no request id and no input to what the daemon mints", k, want)
		}
	}
	for k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("missing wire key %q, got: %s", k, b)
		}
	}
}

// TestPairingMintedPayload_WireKeys pins the reply payload's COMPLETE key set,
// and here the omission is the credential-handling decision itself: the encoded
// string travels ALONE, with no second copy of any field it already contains.
//
// A flat token, server, relay or server_static_pubkey key arriving here would put
// a bearer credential on the wire twice and invite a client to treat the loose
// copy as the less sensitive one. A device_name key would echo client-supplied
// text back out of the daemon. All five redden by construction.
//
// Same two-sided assertion and the same survives-regeneration argument as its
// sibling above.
func TestPairingMintedPayload_WireKeys(t *testing.T) {
	b, err := json.Marshal(PairingMintedPayload{Pairing: "ZXhhbXBsZQ"})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal payload into key set: %v", err)
	}

	want := map[string]bool{"pairing": true}
	for k := range got {
		if !want[k] {
			t.Errorf("unexpected wire key %q: the payload's key set is fixed at %v — the encoded string travels alone, with no second copy of a field it already contains and no echo of the request", k, want)
		}
	}
	for k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("missing wire key %q, got: %s", k, b)
		}
	}
}

// TestMintPairingPayload_ZeroValue_KeyPresent pins the no-omitempty decision,
// which the round trip above cannot: its fixture carries a name, so an omitempty
// added later for tidiness would leave every assertion there green.
//
// device_name has no omitempty for RequestSystemPromptPayload's stated reason —
// there is no presence contract. Absent and empty are the SAME case, "the client
// named no device", which the handler answers by falling back to the label
// `pyry pair` already generates. Keeping the key always on the wire is what lets
// the committed fixture pin the full shape.
func TestMintPairingPayload_ZeroValue_KeyPresent(t *testing.T) {
	b, err := json.Marshal(MintPairingPayload{})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if got, want := string(b), `{"device_name":""}`; got != want {
		t.Errorf("zero-value payload: got %s, want %s — device_name must carry no omitempty", got, want)
	}
}

// TestMintPairingPayload_DeviceNameBound walks both edges of MaxDeviceNameBytes,
// so an off-by-one in either direction reddens: exactly the bound decodes, one
// byte past it does not.
//
// The reject is fail-closed rather than a truncation. A truncating decoder would
// let the label a client displays differ from the one the daemon stored, on a
// frame whose whole subject is which device a credential belongs to.
//
// The error assertion is the second half and is not decoration. device_name is
// remote-authored and its rejection reaches a line-oriented log, so the message
// must name the category, the bound and the observed count and NEVER the bytes —
// internal/pair's ErrInvalidPayload rule, applied to the one string that crosses
// this boundary.
func TestMintPairingPayload_DeviceNameBound(t *testing.T) {
	marker := "SENTINELtext"
	atBound := marker + strings.Repeat("a", MaxDeviceNameBytes-len(marker))
	overBound := marker + strings.Repeat("a", MaxDeviceNameBytes+1-len(marker))

	t.Run("at-bound-accepted", func(t *testing.T) {
		b, err := json.Marshal(map[string]string{"device_name": atBound})
		if err != nil {
			t.Fatalf("marshal input: %v", err)
		}
		var p MintPairingPayload
		if err := json.Unmarshal(b, &p); err != nil {
			t.Fatalf("decode at exactly MaxDeviceNameBytes (%d): got %v, want nil", MaxDeviceNameBytes, err)
		}
		if p.DeviceName != atBound {
			t.Errorf("DeviceName: got %d bytes, want the %d-byte input verbatim", len(p.DeviceName), len(atBound))
		}
	})

	t.Run("over-bound-rejected", func(t *testing.T) {
		b, err := json.Marshal(map[string]string{"device_name": overBound})
		if err != nil {
			t.Fatalf("marshal input: %v", err)
		}
		var p MintPairingPayload
		err = json.Unmarshal(b, &p)
		if err == nil {
			t.Fatalf("decode at MaxDeviceNameBytes+1 (%d): got nil, want an error", MaxDeviceNameBytes+1)
		}
		msg := err.Error()
		if strings.Contains(msg, marker) {
			t.Errorf("error text carries the remote-authored bytes: %q", msg)
		}
		// It must still be diagnosable: the bound and the observed count.
		if !strings.Contains(msg, strconv.Itoa(MaxDeviceNameBytes)) {
			t.Errorf("error text names no bound, so a client cannot act on it: %q", msg)
		}
		if !strings.Contains(msg, strconv.Itoa(len(overBound))) {
			t.Errorf("error text names no observed byte count: %q", msg)
		}
	})
}

// TestMintPairingPayload_UnknownFieldIgnored pins the spec's forward-compatibility
// rule at § Application message types — implementations MUST tolerate unknown
// fields in payloads — against this payload's decode hook specifically.
//
// The hook is the reason this test exists rather than being obvious. Adding an
// UnmarshalJSON is exactly the edit that could reach for json.Decoder's
// DisallowUnknownFields while enforcing the bound, which would turn every future
// client that sends a field this daemon predates into a rejected frame. The
// declared field must survive the unknown one intact.
func TestMintPairingPayload_UnknownFieldIgnored(t *testing.T) {
	raw := []byte(`{"device_name":"Kitchen iPad","minted_by":"a-future-field","retries":3}`)

	var p MintPairingPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("decode with unknown fields: got %v, want nil", err)
	}
	if want := "Kitchen iPad"; p.DeviceName != want {
		t.Errorf("DeviceName: got %q, want %q — the declared field must survive an unknown neighbour", p.DeviceName, want)
	}
}

// TestPairingMintedPayload_UnknownFieldIgnored is the same rule on the reply.
// It has no decode hook, so this is the plain encoding/json behaviour — pinned
// anyway, because a hook added here later (to bound the string, say) would
// inherit the same trap its sibling records.
func TestPairingMintedPayload_UnknownFieldIgnored(t *testing.T) {
	raw := []byte(`{"pairing":"ZXhhbXBsZQ","expires_at":"2026-09-07T12:00:00Z"}`)

	var p PairingMintedPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("decode with unknown fields: got %v, want nil", err)
	}
	if want := "ZXhhbXBsZQ"; p.Pairing != want {
		t.Errorf("Pairing: got %q, want %q", p.Pairing, want)
	}
}

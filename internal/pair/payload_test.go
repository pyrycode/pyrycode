package pair

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/identity"
)

const (
	testServerID = "550e8400-e29b-41d4-a716-446655440000"
	testRelay    = "wss://relay.pyrycode.dev"
	testToken    = "deadbeefcafebabe1234567890abcdef0123456789abcdef0123456789abcdef"
	// testStaticPubkeyB64 is base64.StdEncoding of 32 zero bytes — a
	// fixed, structurally valid X25519 public point for tests that
	// only need a payload whose Decode-time length/encoding checks
	// pass. Tests that care about the fingerprint value use the
	// hardcoded vector in TestFingerprint_FixedVector instead.
	testStaticPubkeyB64 = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
)

// b64 base64url-encodes a string for building test inputs.
func b64(s string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(s))
}

func TestEncode_DecodeRoundTrip(t *testing.T) {
	t.Parallel()
	for i := 0; i < 100; i++ {
		want := Payload{
			Server:             identity.NewServerID(),
			Relay:              testRelay,
			Token:              testToken,
			ServerStaticPubkey: testStaticPubkeyB64,
		}
		s := Encode(want)
		got, err := Decode(s)
		if err != nil {
			t.Fatalf("iteration %d: Decode(Encode(%+v)): %v", i, want, err)
		}
		if got != want {
			t.Errorf("iteration %d: round-trip mismatch: got %+v, want %+v", i, got, want)
		}
	}
}

var base64URLPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func TestEncode_Format(t *testing.T) {
	t.Parallel()
	p := Payload{
		Server:             identity.ServerID(testServerID),
		Relay:              testRelay,
		Token:              testToken,
		ServerStaticPubkey: testStaticPubkeyB64,
	}
	s := Encode(p)
	if s == "" {
		t.Fatal("Encode produced empty string")
	}
	if !base64URLPattern.MatchString(s) {
		t.Errorf("Encode output %q contains chars outside the base64url alphabet", s)
	}
	if strings.ContainsAny(s, "=+/") {
		t.Errorf("Encode output %q contains padding or non-URL-safe chars", s)
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("encoded string is not valid base64url: %v", err)
	}
	var v map[string]any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decoded bytes are not a JSON object: %v", err)
	}
	for _, key := range []string{"server", "relay", "token", "server_static_pubkey"} {
		if _, ok := v[key]; !ok {
			t.Errorf("decoded JSON missing key %q; got %v", key, v)
		}
	}
}

func TestEncode_Stable(t *testing.T) {
	t.Parallel()
	p := Payload{
		Server:             identity.ServerID(testServerID),
		Relay:              testRelay,
		Token:              testToken,
		ServerStaticPubkey: testStaticPubkeyB64,
	}
	a := Encode(p)
	b := Encode(p)
	if a != b {
		t.Errorf("Encode not deterministic: a=%q b=%q", a, b)
	}
}

func TestDecode_Errors(t *testing.T) {
	t.Parallel()
	// Sentinels test that error messages do NOT echo input contents.
	const (
		sentinelToken = "SENSITIVETOKENMUSTNOTLEAK"
		sentinelRelay = "SENSITIVERELAYMUSTNOTLEAK"
	)
	validJSON := `{"server":"` + testServerID + `","relay":"x","token":"y","server_static_pubkey":"` + testStaticPubkeyB64 + `"}`
	pub31 := base64.StdEncoding.EncodeToString(make([]byte, 31))
	pub33 := base64.StdEncoding.EncodeToString(make([]byte, 33))

	tests := []struct {
		name string
		in   string
	}{
		{"empty input", ""},
		{"non base64 chars", "!!!"},
		{"padded base64 input", "YQ=="},
		{"valid base64 not json", b64("not json")},
		{"empty json object", b64("{}")},
		{"json array not object", b64("[1,2,3]")},
		{"missing server field", b64(`{"relay":"x","token":"y","server_static_pubkey":"` + testStaticPubkeyB64 + `"}`)},
		{"missing relay field", b64(`{"server":"` + testServerID + `","token":"y","server_static_pubkey":"` + testStaticPubkeyB64 + `"}`)},
		{"missing token field", b64(`{"server":"` + testServerID + `","relay":"x","server_static_pubkey":"` + testStaticPubkeyB64 + `"}`)},
		{"missing server_static_pubkey field", b64(`{"server":"` + testServerID + `","relay":"x","token":"y"}`)},
		{"empty server string", b64(`{"server":"","relay":"x","token":"y","server_static_pubkey":"` + testStaticPubkeyB64 + `"}`)},
		{"empty relay string", b64(`{"server":"` + testServerID + `","relay":"","token":"y","server_static_pubkey":"` + testStaticPubkeyB64 + `"}`)},
		{"empty token string", b64(`{"server":"` + testServerID + `","relay":"x","token":"","server_static_pubkey":"` + testStaticPubkeyB64 + `"}`)},
		{"empty server_static_pubkey string", b64(`{"server":"` + testServerID + `","relay":"x","token":"y","server_static_pubkey":""}`)},
		{"non base64 server_static_pubkey", b64(`{"server":"` + testServerID + `","relay":"x","token":"y","server_static_pubkey":"!!!"}`)},
		{"server_static_pubkey too short", b64(`{"server":"` + testServerID + `","relay":"x","token":"y","server_static_pubkey":"` + pub31 + `"}`)},
		{"server_static_pubkey too long", b64(`{"server":"` + testServerID + `","relay":"x","token":"y","server_static_pubkey":"` + pub33 + `"}`)},
		{"invalid server id", b64(`{"server":"not-a-uuid","relay":"x","token":"y","server_static_pubkey":"` + testStaticPubkeyB64 + `"}`)},
		{"uppercase server id", b64(`{"server":"550E8400-E29B-41D4-A716-446655440000","relay":"x","token":"y","server_static_pubkey":"` + testStaticPubkeyB64 + `"}`)},
		{"trailing json value", b64(validJSON + `{"x":1}`)},
		{"trailing garbage non json", b64(validJSON + `garbage`)},
		{"sensitive token in invalid payload", b64(`{"server":"not-a-uuid","relay":"` + sentinelRelay + `","token":"` + sentinelToken + `","server_static_pubkey":"` + testStaticPubkeyB64 + `"}`)},
		{"sensitive token after trailing data", b64(`{"server":"` + testServerID + `","relay":"` + sentinelRelay + `","token":"` + sentinelToken + `","server_static_pubkey":"` + testStaticPubkeyB64 + `"}{"x":1}`)},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := Decode(tt.in)
			if err == nil {
				t.Fatalf("Decode(%q) err = nil, want non-nil", tt.in)
			}
			if !errors.Is(err, ErrInvalidPayload) {
				t.Errorf("Decode(%q) err = %v, want wrap of ErrInvalidPayload", tt.in, err)
			}
			if got != (Payload{}) {
				t.Errorf("Decode(%q) = %+v, want zero Payload on error", tt.in, got)
			}
			msg := err.Error()
			if strings.Contains(msg, sentinelToken) {
				t.Errorf("Decode error leaked token sentinel: %q", msg)
			}
			if strings.Contains(msg, sentinelRelay) {
				t.Errorf("Decode error leaked relay sentinel: %q", msg)
			}
		})
	}
}

func TestDecode_AcceptsTrailingWhitespace(t *testing.T) {
	t.Parallel()
	// Trailing whitespace inside the JSON payload is normal and must
	// not be rejected as "trailing data": json.Decoder consumes it
	// before returning io.EOF.
	id := identity.NewServerID()
	raw := `{"server":"` + string(id) + `","relay":"x","token":"y","server_static_pubkey":"` + testStaticPubkeyB64 + `"}` + "\n  \t"
	got, err := Decode(b64(raw))
	if err != nil {
		t.Fatalf("Decode rejected trailing whitespace: %v", err)
	}
	want := Payload{Server: id, Relay: "x", Token: "y", ServerStaticPubkey: testStaticPubkeyB64}
	if got != want {
		t.Errorf("Decode mismatch: got %+v, want %+v", got, want)
	}
}

// TestFingerprint_FixedVector pins the BLAKE2s-256(pubkey)[:8] →
// colon-hex formatter on a known vector: pubkey = 32 zero bytes. The
// expected string is hardcoded (computed once out-of-band, not derived
// at test time from the same blake2s call we're verifying — that would
// be tautological). If this test ever flakes, the hash primitive or
// the truncation width has changed; both are spec-load-bearing.
func TestFingerprint_FixedVector(t *testing.T) {
	t.Parallel()
	var zero [32]byte
	const want = "32:0b:5e:a9:9e:65:3b:c2"
	got := Fingerprint(zero)
	if got != want {
		t.Errorf("Fingerprint(zero32) = %q, want %q", got, want)
	}
}

// TestFingerprint_LengthAndShape pins the output format independent
// of the hash value: exactly 8 lowercase-hex byte pairs separated by
// seven colons, 23 characters total.
func TestFingerprint_LengthAndShape(t *testing.T) {
	t.Parallel()
	var pub [32]byte
	for i := range pub {
		pub[i] = byte(i)
	}
	got := Fingerprint(pub)
	if len(got) != 23 {
		t.Errorf("Fingerprint length = %d, want 23 (output: %q)", len(got), got)
	}
	shape := regexp.MustCompile(`^[0-9a-f]{2}(:[0-9a-f]{2}){7}$`)
	if !shape.MatchString(got) {
		t.Errorf("Fingerprint shape mismatch: %q", got)
	}
}

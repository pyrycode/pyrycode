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
)

// b64 base64url-encodes a string for building test inputs.
func b64(s string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(s))
}

func TestEncode_DecodeRoundTrip(t *testing.T) {
	t.Parallel()
	for i := 0; i < 100; i++ {
		want := Payload{
			Server: identity.NewServerID(),
			Relay:  testRelay,
			Token:  testToken,
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
		Server: identity.ServerID(testServerID),
		Relay:  testRelay,
		Token:  testToken,
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
	for _, key := range []string{"server", "relay", "token"} {
		if _, ok := v[key]; !ok {
			t.Errorf("decoded JSON missing key %q; got %v", key, v)
		}
	}
}

func TestEncode_Stable(t *testing.T) {
	t.Parallel()
	p := Payload{
		Server: identity.ServerID(testServerID),
		Relay:  testRelay,
		Token:  testToken,
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
	validJSON := `{"server":"` + testServerID + `","relay":"x","token":"y"}`

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
		{"missing server field", b64(`{"relay":"x","token":"y"}`)},
		{"missing relay field", b64(`{"server":"` + testServerID + `","token":"y"}`)},
		{"missing token field", b64(`{"server":"` + testServerID + `","relay":"x"}`)},
		{"empty server string", b64(`{"server":"","relay":"x","token":"y"}`)},
		{"empty relay string", b64(`{"server":"` + testServerID + `","relay":"","token":"y"}`)},
		{"empty token string", b64(`{"server":"` + testServerID + `","relay":"x","token":""}`)},
		{"invalid server id", b64(`{"server":"not-a-uuid","relay":"x","token":"y"}`)},
		{"uppercase server id", b64(`{"server":"550E8400-E29B-41D4-A716-446655440000","relay":"x","token":"y"}`)},
		{"trailing json value", b64(validJSON + `{"x":1}`)},
		{"trailing garbage non json", b64(validJSON + `garbage`)},
		{"sensitive token in invalid payload", b64(`{"server":"not-a-uuid","relay":"` + sentinelRelay + `","token":"` + sentinelToken + `"}`)},
		{"sensitive token after trailing data", b64(`{"server":"` + testServerID + `","relay":"` + sentinelRelay + `","token":"` + sentinelToken + `"}{"x":1}`)},
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
	raw := `{"server":"` + string(id) + `","relay":"x","token":"y"}` + "\n  \t"
	got, err := Decode(b64(raw))
	if err != nil {
		t.Fatalf("Decode rejected trailing whitespace: %v", err)
	}
	want := Payload{Server: id, Relay: "x", Token: "y"}
	if got != want {
		t.Errorf("Decode mismatch: got %+v, want %+v", got, want)
	}
}

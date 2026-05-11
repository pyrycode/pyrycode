package devices

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Published SHA-256("abc") test vector. Pinned as a regression guard
// against an accidental swap to a different hash or encoding.
const sha256OfABC = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"

func TestHashToken_Deterministic(t *testing.T) {
	t.Parallel()

	const plain = "abc123-fixture-not-a-real-token"
	h1 := HashToken(plain)
	h2 := HashToken(plain)
	if h1 != h2 {
		t.Fatalf("HashToken not deterministic: %q vs %q", h1, h2)
	}
	if got, want := len(h1), 64; got != want {
		t.Errorf("HashToken length = %d, want %d", got, want)
	}
	for _, r := range h1 {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			t.Errorf("HashToken contains non-lowercase-hex rune %q", r)
			break
		}
	}
	if got := HashToken("abc"); got != sha256OfABC {
		t.Errorf("HashToken(%q) = %q, want %q", "abc", got, sha256OfABC)
	}
}

func TestVerifyToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		plain string
		hash  string
		want  bool
	}{
		{
			name:  "true path: matching token",
			plain: "abc",
			hash:  HashToken("abc"),
			want:  true,
		},
		{
			name:  "false path: non-matching token",
			plain: "abc",
			hash:  HashToken("xyz"),
			want:  false,
		},
		{
			name:  "false on empty hash",
			plain: "abc",
			hash:  "",
			want:  false,
		},
		{
			name:  "false on too-short hash",
			plain: "abc",
			hash:  "ba7816bf",
			want:  false,
		},
		{
			name:  "false on too-long hash",
			plain: "abc",
			hash:  HashToken("abc") + "00",
			want:  false,
		},
		{
			name:  "false on non-hex hash",
			plain: "abc",
			hash:  strings.Repeat("z", 64),
			want:  false,
		},
		{
			name:  "false on uppercase hex hash",
			plain: "abc",
			hash:  strings.ToUpper(HashToken("abc")),
			want:  false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := VerifyToken(tc.plain, tc.hash); got != tc.want {
				t.Errorf("VerifyToken(%q, %q) = %v, want %v", tc.plain, tc.hash, got, tc.want)
			}
		})
	}
}

func TestDevice_LegacyOmitsPushFields(t *testing.T) {
	t.Parallel()

	in := Device{
		TokenHash:  HashToken("abc"),
		Name:       "legacy-device",
		PairedAt:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		LastSeenAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if bytes.Contains(b, []byte(`"platform"`)) {
		t.Errorf("encoded form leaked empty platform key: %s", b)
	}
	if bytes.Contains(b, []byte(`"push_token"`)) {
		t.Errorf("encoded form leaked empty push_token key: %s", b)
	}
	var out Device
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if out != in {
		t.Errorf("round-trip mismatch: got %+v, want %+v", out, in)
	}
}

func TestDevice_PopulatedRoundTrip(t *testing.T) {
	t.Parallel()

	in := Device{
		TokenHash:  HashToken("xyz"),
		Name:       "pixel-8",
		PairedAt:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		LastSeenAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		Platform:   "apns",
		PushToken:  "f0r-test-fixture-not-a-real-token",
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var out Device
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if out != in {
		t.Errorf("round-trip mismatch: got %+v, want %+v", out, in)
	}
}

func TestDevice_DecodeLegacyDiskShape(t *testing.T) {
	t.Parallel()

	legacy := []byte(`{
      "token_hash": "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad",
      "name": "legacy",
      "paired_at": "2026-01-01T00:00:00Z",
      "last_seen_at": "2026-01-02T00:00:00Z"
    }`)
	var d Device
	if err := json.Unmarshal(legacy, &d); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if d.Platform != "" {
		t.Errorf("Platform = %q, want \"\"", d.Platform)
	}
	if d.PushToken != "" {
		t.Errorf("PushToken = %q, want \"\"", d.PushToken)
	}
}

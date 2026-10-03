package devices

import (
	"bytes"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func testKey(b byte) []byte { return bytes.Repeat([]byte{b}, 32) }

// TestRegistry_Validate_StaticKey covers the key half of Validate (#2734): an
// unbound record accepts any key, a bound one accepts only its own, and a
// mismatch returns the matched record (for the caller's log line) without
// stamping LastSeenAt.
func TestRegistry_Validate_StaticKey(t *testing.T) {
	t.Parallel()
	when := mustParseTime(t, "2020-01-01T00:00:00Z")
	bound := hex.EncodeToString(testKey(1))
	cases := []struct {
		name      string
		stored    string
		presented []byte
		want      ValidateResult
	}{
		{"unbound accepts any key", "", testKey(9), ValidateAccepted},
		{"unbound accepts empty key", "", nil, ValidateAccepted},
		{"bound accepts its own key", bound, testKey(1), ValidateAccepted},
		{"bound refuses another key", bound, testKey(2), ValidateKeyMismatch},
		{"bound refuses an empty key", bound, nil, ValidateKeyMismatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := &Registry{}
			r.Add(Device{TokenHash: HashToken("plain"), Name: "phone", LastSeenAt: when, StaticKey: tc.stored})
			got, res := r.Validate("plain", tc.presented)
			if res != tc.want {
				t.Fatalf("result = %v, want %v", res, tc.want)
			}
			stamped := r.List()[0].LastSeenAt.After(when)
			if stamped != (tc.want == ValidateAccepted) {
				t.Errorf("LastSeenAt stamped = %v, want %v", stamped, tc.want == ValidateAccepted)
			}
			if got.Name != "phone" {
				t.Errorf("returned Name = %q, want %q", got.Name, "phone")
			}
			if r.List()[0].StaticKey != tc.stored {
				t.Errorf("Validate changed StaticKey to %q", r.List()[0].StaticKey)
			}
		})
	}
}

func TestRegistry_BindStaticKey(t *testing.T) {
	t.Parallel()
	hash := HashToken("plain")
	bound := hex.EncodeToString(testKey(1))
	cases := []struct {
		name      string
		stored    string
		tokenHash string
		presented []byte
		want      BindResult
		wantKey   string
	}{
		{"unknown hash", "", HashToken("other"), testKey(1), BindUnknownDevice, ""},
		{"empty key never binds", "", hash, nil, BindUnknownDevice, ""},
		{"unbound binds", "", hash, testKey(1), BindNewlyBound, bound},
		{"same key matches", bound, hash, testKey(1), BindMatched, bound},
		{"other key mismatches", bound, hash, testKey(2), BindKeyMismatch, bound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := &Registry{}
			r.Add(Device{TokenHash: hash, Name: "phone", StaticKey: tc.stored})
			if got := r.BindStaticKey(tc.tokenHash, tc.presented); got != tc.want {
				t.Fatalf("BindStaticKey = %v, want %v", got, tc.want)
			}
			if got := r.List()[0].StaticKey; got != tc.wantKey {
				t.Errorf("StaticKey = %q, want %q", got, tc.wantKey)
			}
		})
	}
}

// TestRegistry_BindStaticKey_Race: connections racing to bind one unbound
// record with different keys produce exactly one winner, and the stored key is
// the winner's.
func TestRegistry_BindStaticKey_Race(t *testing.T) {
	t.Parallel()
	const n = 16
	hash := HashToken("plain")
	r := &Registry{}
	r.Add(Device{TokenHash: hash, Name: "phone"})
	results := make([]BindResult, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results[i] = r.BindStaticKey(hash, testKey(byte(i+1)))
		}()
	}
	close(start)
	wg.Wait()
	winners := 0
	for i, res := range results {
		switch res {
		case BindNewlyBound:
			winners++
			if want := hex.EncodeToString(testKey(byte(i + 1))); r.List()[0].StaticKey != want {
				t.Errorf("stored key = %q, want winner's %q", r.List()[0].StaticKey, want)
			}
		case BindKeyMismatch:
		default:
			t.Errorf("goroutine %d: result %v, want NewlyBound or KeyMismatch", i, res)
		}
	}
	if winners != 1 {
		t.Fatalf("winners = %d, want exactly 1", winners)
	}
}

// TestRegistry_StaticKey_RoundTrip: a bound record persists static_key, an
// unbound one writes no key at all, and a record from before the field loads
// unbound.
func TestRegistry_StaticKey_RoundTrip(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "devices.json")
	bound := hex.EncodeToString(testKey(1))
	r := &Registry{}
	r.Add(Device{TokenHash: HashToken("a"), Name: "a", StaticKey: bound})
	r.Add(Device{TokenHash: HashToken("b"), Name: "b"})
	if err := r.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if got := strings.Count(string(data), `"static_key"`); got != 1 {
		t.Errorf("static_key keys on disk = %d, want 1 (unbound omitted):\n%s", got, data)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, d := range loaded.List() {
		want := ""
		if d.Name == "a" {
			want = bound
		}
		if d.StaticKey != want {
			t.Errorf("%s: StaticKey = %q, want %q", d.Name, d.StaticKey, want)
		}
	}

	legacy := filepath.Join(t.TempDir(), "devices.json")
	if err := os.WriteFile(legacy, []byte(`{"devices":[{"token_hash":"`+HashToken("c")+`","name":"c"}]}`), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	old, err := Load(legacy)
	if err != nil {
		t.Fatalf("Load legacy: %v", err)
	}
	if _, res := old.Validate("c", testKey(7)); res != ValidateAccepted {
		t.Errorf("legacy record: Validate = %v, want ValidateAccepted", res)
	}
	if got := old.BindStaticKey(HashToken("c"), testKey(7)); got != BindNewlyBound {
		t.Errorf("legacy record: BindStaticKey = %v, want BindNewlyBound", got)
	}
}

// TestRegistry_StaticKey_RevokeReleases: revoke removes the record and its
// binding, and the re-paired record (a new token) binds whichever install
// redeems it.
func TestRegistry_StaticKey_RevokeReleases(t *testing.T) {
	t.Parallel()
	r := &Registry{}
	r.Add(Device{TokenHash: HashToken("old"), Name: "phone"})
	if got := r.BindStaticKey(HashToken("old"), testKey(1)); got != BindNewlyBound {
		t.Fatalf("first bind = %v, want BindNewlyBound", got)
	}
	if !r.Remove("phone") {
		t.Fatal("Remove = false")
	}
	if _, res := r.Validate("old", testKey(1)); res != ValidateUnknownToken {
		t.Errorf("revoked token: Validate = %v, want ValidateUnknownToken", res)
	}
	r.Add(Device{TokenHash: HashToken("new"), Name: "phone"})
	if _, res := r.Validate("new", testKey(2)); res != ValidateAccepted {
		t.Errorf("re-paired: Validate = %v, want ValidateAccepted", res)
	}
	if got := r.BindStaticKey(HashToken("new"), testKey(2)); got != BindNewlyBound {
		t.Errorf("re-paired: BindStaticKey = %v, want BindNewlyBound", got)
	}
}

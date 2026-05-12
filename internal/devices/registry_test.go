package devices

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func mustParseTime(t *testing.T, s string) time.Time {
	t.Helper()
	tt, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatalf("parse time %q: %v", s, err)
	}
	return tt
}

func TestRegistry_LoadMissingFile(t *testing.T) {
	t.Parallel()
	got, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("Load(missing): err = %v, want nil", err)
	}
	if got == nil {
		t.Fatal("Load(missing): got = nil, want empty *Registry")
	}
	if n := len(got.List()); n != 0 {
		t.Errorf("len(List) = %d, want 0", n)
	}
}

func TestRegistry_LoadEmptyFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "devices.json")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("write empty file: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load(empty): err = %v, want nil", err)
	}
	if got == nil {
		t.Fatal("Load(empty): got = nil, want empty *Registry")
	}
	if n := len(got.List()); n != 0 {
		t.Errorf("len(List) = %d, want 0", n)
	}
}

func TestRegistry_LoadMalformedJSON(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "devices.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write malformed: %v", err)
	}
	got, err := Load(path)
	if err == nil {
		t.Fatalf("Load(malformed) = %+v, want error", got)
	}
	if got != nil {
		t.Errorf("Load(malformed) returned non-nil registry: %+v", got)
	}
	if !strings.Contains(err.Error(), "registry: parse") {
		t.Errorf("err = %q, want it to contain %q", err, "registry: parse")
	}
}

func TestRegistry_AddSaveLoadRoundTrip(t *testing.T) {
	t.Parallel()
	when := mustParseTime(t, "2026-05-09T12:34:56.789Z")
	later := when.Add(time.Second)

	r := &Registry{}
	r.Add(Device{
		TokenHash:  HashToken("plain-1"),
		Name:       "Juhana's Pixel 8",
		PairedAt:   when,
		LastSeenAt: when,
	})
	r.Add(Device{
		TokenHash:  HashToken("plain-2"),
		Name:       "Phone 2",
		PairedAt:   later,
		LastSeenAt: later,
	})

	path := filepath.Join(t.TempDir(), "devices.json")
	if err := r.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	back, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := back.List()
	if len(got) != 2 {
		t.Fatalf("len(List) = %d, want 2", len(got))
	}

	// Sorted by PairedAt asc.
	want := []Device{
		{TokenHash: HashToken("plain-1"), Name: "Juhana's Pixel 8", PairedAt: when, LastSeenAt: when},
		{TokenHash: HashToken("plain-2"), Name: "Phone 2", PairedAt: later, LastSeenAt: later},
	}
	for i, w := range want {
		g := got[i]
		if g.TokenHash != w.TokenHash {
			t.Errorf("[%d] TokenHash = %q, want %q", i, g.TokenHash, w.TokenHash)
		}
		if g.Name != w.Name {
			t.Errorf("[%d] Name = %q, want %q", i, g.Name, w.Name)
		}
		if !g.PairedAt.Equal(w.PairedAt) {
			t.Errorf("[%d] PairedAt = %v, want %v", i, g.PairedAt, w.PairedAt)
		}
		if !g.LastSeenAt.Equal(w.LastSeenAt) {
			t.Errorf("[%d] LastSeenAt = %v, want %v", i, g.LastSeenAt, w.LastSeenAt)
		}
	}
}

func TestRegistry_RemovePresent(t *testing.T) {
	t.Parallel()
	r := &Registry{}
	r.Add(Device{Name: "alice", TokenHash: HashToken("a")})
	r.Add(Device{Name: "bob", TokenHash: HashToken("b")})

	if ok := r.Remove("alice"); !ok {
		t.Fatalf("Remove(alice) = false, want true")
	}
	got := r.List()
	if len(got) != 1 {
		t.Fatalf("len(List) = %d, want 1", len(got))
	}
	if got[0].Name != "bob" {
		t.Errorf("remaining Name = %q, want %q", got[0].Name, "bob")
	}
}

func TestRegistry_RemoveAbsent(t *testing.T) {
	t.Parallel()
	r := &Registry{}
	r.Add(Device{Name: "alice", TokenHash: HashToken("a")})

	if ok := r.Remove("ghost"); ok {
		t.Fatalf("Remove(ghost) = true, want false")
	}
	got := r.List()
	if len(got) != 1 || got[0].Name != "alice" {
		t.Errorf("List = %+v, want one entry named alice", got)
	}
}

func TestRegistry_FindByTokenHash(t *testing.T) {
	t.Parallel()
	aliceHash := HashToken("a")
	tests := []struct {
		name        string
		setup       func(*Registry)
		hash        string
		wantName    string
		wantOK      bool
	}{
		{
			name:     "hit",
			setup:    func(r *Registry) { r.Add(Device{Name: "alice", TokenHash: aliceHash}) },
			hash:     aliceHash,
			wantName: "alice",
			wantOK:   true,
		},
		{
			name:   "miss-empty",
			setup:  func(r *Registry) { r.Add(Device{Name: "alice", TokenHash: aliceHash}) },
			hash:   "",
			wantOK: false,
		},
		{
			name:   "miss-non-matching",
			setup:  func(r *Registry) { r.Add(Device{Name: "alice", TokenHash: aliceHash}) },
			hash:   HashToken("z"),
			wantOK: false,
		},
		{
			name:   "miss-empty-reg",
			setup:  func(r *Registry) {},
			hash:   aliceHash,
			wantOK: false,
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := &Registry{}
			tc.setup(r)
			got, ok := r.FindByTokenHash(tc.hash)
			if ok != tc.wantOK {
				t.Errorf("ok = %v, want %v", ok, tc.wantOK)
			}
			if tc.wantOK {
				if got.Name != tc.wantName {
					t.Errorf("Name = %q, want %q", got.Name, tc.wantName)
				}
			} else {
				if got != (Device{}) {
					t.Errorf("device = %+v, want zero Device", got)
				}
			}
		})
	}
}

func TestRegistry_UpdatePushRegistration(t *testing.T) {
	t.Parallel()
	aHash := HashToken("a")
	bHash := HashToken("b")

	tests := []struct {
		name          string
		tokenHash     string
		platform      string
		pushToken     string
		deviceName    string
		wantOK        bool
		wantAName     string
		wantAPlatform string
		wantAToken    string
		wantBName     string
		wantBPlatform string
		wantBToken    string
	}{
		{
			name:          "hit-updates-row-leaves-other-untouched",
			tokenHash:     aHash,
			platform:      "fcm",
			pushToken:     "fcm-token-xyz",
			deviceName:    "Alice's Pixel",
			wantOK:        true,
			wantAName:     "Alice's Pixel",
			wantAPlatform: "fcm",
			wantAToken:    "fcm-token-xyz",
			wantBName:     "bob",
			wantBPlatform: "",
			wantBToken:    "",
		},
		{
			name:          "miss-unknown-hash-leaves-everything",
			tokenHash:     HashToken("z"),
			platform:      "apns",
			pushToken:     "apns-token-abc",
			deviceName:    "ghost",
			wantOK:        false,
			wantAName:     "alice",
			wantAPlatform: "",
			wantAToken:    "",
			wantBName:     "bob",
			wantBPlatform: "",
			wantBToken:    "",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := &Registry{}
			r.Add(Device{Name: "alice", TokenHash: aHash})
			r.Add(Device{Name: "bob", TokenHash: bHash})

			ok := r.UpdatePushRegistration(tc.tokenHash, tc.platform, tc.pushToken, tc.deviceName)
			if ok != tc.wantOK {
				t.Errorf("ok = %v, want %v", ok, tc.wantOK)
			}
			got := r.List()
			byHash := map[string]Device{}
			for _, d := range got {
				byHash[d.TokenHash] = d
			}
			a := byHash[aHash]
			if a.Name != tc.wantAName || a.Platform != tc.wantAPlatform || a.PushToken != tc.wantAToken {
				t.Errorf("alice = %+v, want Name=%q Platform=%q PushToken=%q",
					a, tc.wantAName, tc.wantAPlatform, tc.wantAToken)
			}
			b := byHash[bHash]
			if b.Name != tc.wantBName || b.Platform != tc.wantBPlatform || b.PushToken != tc.wantBToken {
				t.Errorf("bob = %+v, want Name=%q Platform=%q PushToken=%q",
					b, tc.wantBName, tc.wantBPlatform, tc.wantBToken)
			}
		})
	}
}

func TestRegistry_SaveFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix permission semantics required")
	}
	t.Parallel()
	dir := t.TempDir()
	subdir := filepath.Join(dir, "pyry")
	path := filepath.Join(subdir, "devices.json")
	when := mustParseTime(t, "2026-05-09T12:34:56.789Z")

	r := &Registry{}
	r.Add(Device{
		TokenHash:  HashToken("plain-1"),
		Name:       "alice",
		PairedAt:   when,
		LastSeenAt: when,
	})
	if err := r.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	dirInfo, err := os.Stat(subdir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if mode := dirInfo.Mode().Perm(); mode != 0o700 {
		t.Errorf("dir mode = %o, want 0700", mode)
	}
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	if mode := fileInfo.Mode().Perm(); mode != 0o600 {
		t.Errorf("file mode = %o, want 0600", mode)
	}
}

func TestRegistry_SaveStableOrdering(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	t1 := mustParseTime(t, "2026-05-09T12:34:56.789Z")
	t2 := t1.Add(time.Second)
	t3 := t1.Add(2 * time.Second)

	mk := func(order []int) *Registry {
		devs := []Device{
			{Name: "alice", TokenHash: HashToken("a"), PairedAt: t1, LastSeenAt: t1},
			{Name: "bob", TokenHash: HashToken("b"), PairedAt: t2, LastSeenAt: t2},
			{Name: "carol", TokenHash: HashToken("c"), PairedAt: t3, LastSeenAt: t3},
		}
		r := &Registry{}
		for _, i := range order {
			r.Add(devs[i])
		}
		return r
	}

	pathA := filepath.Join(dir, "a.json")
	pathB := filepath.Join(dir, "b.json")
	if err := mk([]int{0, 1, 2}).Save(pathA); err != nil {
		t.Fatalf("Save A: %v", err)
	}
	if err := mk([]int{2, 0, 1}).Save(pathB); err != nil {
		t.Fatalf("Save B: %v", err)
	}
	a, err := os.ReadFile(pathA)
	if err != nil {
		t.Fatalf("read A: %v", err)
	}
	b, err := os.ReadFile(pathB)
	if err != nil {
		t.Fatalf("read B: %v", err)
	}
	if string(a) != string(b) {
		t.Errorf("byte content differs between same-content saves\nA = %s\nB = %s", a, b)
	}
}

func TestRegistry_SaveAtomicRenamePreservesOldFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix permission semantics required")
	}
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "devices.json")

	original := []byte(`{"devices":[{"token_hash":"deadbeef","name":"original","paired_at":"2026-01-01T00:00:00Z","last_seen_at":"2026-01-01T00:00:00Z"}]}`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatalf("write original: %v", err)
	}

	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	when := mustParseTime(t, "2026-05-09T12:34:56.789Z")
	r := &Registry{}
	r.Add(Device{TokenHash: HashToken("p"), Name: "new", PairedAt: when, LastSeenAt: when})
	if err := r.Save(path); err == nil {
		t.Fatal("Save: nil error, want failure")
	}

	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("restore chmod: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read original after failed save: %v", err)
	}
	if string(got) != string(original) {
		t.Errorf("original file mutated by failed save:\n got = %s\nwant = %s", got, original)
	}
}

func TestRegistry_ConcurrentReadWrite(t *testing.T) {
	t.Parallel()
	r := &Registry{}
	var wg sync.WaitGroup
	const n = 8
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r.Add(Device{
				Name:      fmt.Sprintf("d-%d", i),
				TokenHash: HashToken(fmt.Sprintf("p-%d", i)),
			})
			_ = r.List()
			_, _ = r.FindByTokenHash(HashToken("p-0"))
		}(i)
	}
	wg.Wait()
	if got := len(r.List()); got != n {
		t.Errorf("len(List) = %d, want %d", got, n)
	}
}

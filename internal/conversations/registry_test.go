package conversations

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func ptrTo[T any](v T) *T { return &v }

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
	path := filepath.Join(t.TempDir(), "conversations.json")
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
	path := filepath.Join(t.TempDir(), "conversations.json")
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

func TestRegistry_CreateSaveLoadRoundTrip(t *testing.T) {
	t.Parallel()
	when := mustParseTime(t, "2026-05-09T12:34:56.789Z")
	later := when.Add(time.Second)

	r := &Registry{}
	r.Create(Conversation{
		ID:               "11111111-2222-4333-8444-555555555555",
		Name:             strPtr("general"),
		Cwd:              "/home/user/project",
		CurrentSessionID: "sess-current",
		SessionHistory:   []string{"sess-old-1"},
		IsPromoted:       true,
		LastUsedAt:       later,
	})
	r.Create(Conversation{
		ID:         "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
		Cwd:        "/tmp/work",
		IsPromoted: false,
		LastUsedAt: when,
	})

	path := filepath.Join(t.TempDir(), "conversations.json")
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

	// Sorted by LastUsedAt asc — the unpromoted one (when) comes before the
	// promoted one (later).
	if got[0].ID != "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee" {
		t.Errorf("got[0].ID = %q, want unpromoted entry first (sorted by LastUsedAt asc)", got[0].ID)
	}
	if got[1].ID != "11111111-2222-4333-8444-555555555555" {
		t.Errorf("got[1].ID = %q, want promoted entry second", got[1].ID)
	}
	if !got[0].LastUsedAt.Equal(when) {
		t.Errorf("got[0].LastUsedAt = %v, want %v", got[0].LastUsedAt, when)
	}
	if !got[1].LastUsedAt.Equal(later) {
		t.Errorf("got[1].LastUsedAt = %v, want %v", got[1].LastUsedAt, later)
	}
	if got[1].Name == nil || *got[1].Name != "general" {
		t.Errorf("got[1].Name = %v, want pointer to %q", got[1].Name, "general")
	}
	if got[1].CurrentSessionID != "sess-current" {
		t.Errorf("got[1].CurrentSessionID = %q, want %q", got[1].CurrentSessionID, "sess-current")
	}
	if len(got[1].SessionHistory) != 1 || got[1].SessionHistory[0] != "sess-old-1" {
		t.Errorf("got[1].SessionHistory = %v, want [sess-old-1]", got[1].SessionHistory)
	}
}

func TestRegistry_Get(t *testing.T) {
	t.Parallel()
	const aliceID ConversationID = "11111111-2222-4333-8444-555555555555"

	tests := []struct {
		name    string
		setup   func(*Registry)
		id      ConversationID
		wantCwd string
		wantOK  bool
	}{
		{
			name:    "hit",
			setup:   func(r *Registry) { r.Create(Conversation{ID: aliceID, Cwd: "/a"}) },
			id:      aliceID,
			wantCwd: "/a",
			wantOK:  true,
		},
		{
			name:   "miss-empty",
			setup:  func(r *Registry) { r.Create(Conversation{ID: aliceID, Cwd: "/a"}) },
			id:     "",
			wantOK: false,
		},
		{
			name:   "miss-non-matching",
			setup:  func(r *Registry) { r.Create(Conversation{ID: aliceID, Cwd: "/a"}) },
			id:     "ffffffff-2222-4333-8444-555555555555",
			wantOK: false,
		},
		{
			name:   "miss-empty-reg",
			setup:  func(r *Registry) {},
			id:     aliceID,
			wantOK: false,
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := &Registry{}
			tc.setup(r)
			got, ok := r.Get(tc.id)
			if ok != tc.wantOK {
				t.Errorf("ok = %v, want %v", ok, tc.wantOK)
			}
			if tc.wantOK {
				if got.Cwd != tc.wantCwd {
					t.Errorf("Cwd = %q, want %q", got.Cwd, tc.wantCwd)
				}
			} else {
				if got.ID != "" || got.Cwd != "" {
					t.Errorf("conversation = %+v, want zero Conversation", got)
				}
			}
		})
	}
}

func TestRegistry_Delete(t *testing.T) {
	t.Parallel()
	const aID ConversationID = "11111111-2222-4333-8444-555555555555"
	const bID ConversationID = "22222222-2222-4333-8444-555555555555"
	const cID ConversationID = "33333333-2222-4333-8444-555555555555"
	const absentID ConversationID = "ffffffff-2222-4333-8444-555555555555"

	t.Run("hit", func(t *testing.T) {
		t.Parallel()
		r := &Registry{}
		r.Create(Conversation{ID: aID, Cwd: "/a"})
		if !r.Delete(aID) {
			t.Fatal("Delete(aID) = false, want true")
		}
		if n := len(r.List()); n != 0 {
			t.Errorf("len(List) after delete = %d, want 0", n)
		}
		if _, ok := r.Get(aID); ok {
			t.Errorf("Get(aID) ok = true after Delete, want false")
		}
	})

	t.Run("miss-empty-registry", func(t *testing.T) {
		t.Parallel()
		r := &Registry{}
		if r.Delete(aID) {
			t.Errorf("Delete on empty = true, want false")
		}
	})

	t.Run("miss-non-matching", func(t *testing.T) {
		t.Parallel()
		r := &Registry{}
		r.Create(Conversation{ID: aID, Cwd: "/a"})
		if r.Delete(absentID) {
			t.Errorf("Delete(absent) = true, want false")
		}
		got, ok := r.Get(aID)
		if !ok {
			t.Fatal("Get(aID) = !ok after non-matching Delete, want untouched")
		}
		if got.Cwd != "/a" {
			t.Errorf("Cwd = %q, want %q (untouched)", got.Cwd, "/a")
		}
	})

	t.Run("preserves-order", func(t *testing.T) {
		t.Parallel()
		r := &Registry{}
		r.Create(Conversation{ID: aID, Cwd: "/a"})
		r.Create(Conversation{ID: bID, Cwd: "/b"})
		r.Create(Conversation{ID: cID, Cwd: "/c"})
		if !r.Delete(bID) {
			t.Fatal("Delete(bID) = false, want true")
		}
		got := r.List()
		if len(got) != 2 {
			t.Fatalf("len(List) = %d, want 2", len(got))
		}
		if got[0].ID != aID {
			t.Errorf("got[0].ID = %q, want %q", got[0].ID, aID)
		}
		if got[1].ID != cID {
			t.Errorf("got[1].ID = %q, want %q", got[1].ID, cID)
		}
	})

	t.Run("snapshot-safety", func(t *testing.T) {
		t.Parallel()
		r := &Registry{}
		r.Create(Conversation{ID: aID, Cwd: "/a"})
		r.Create(Conversation{ID: bID, Cwd: "/b"})

		snap := r.List()
		if len(snap) != 2 {
			t.Fatalf("snapshot len = %d, want 2", len(snap))
		}
		if !r.Delete(snap[0].ID) {
			t.Fatal("Delete(snap[0].ID) = false, want true")
		}
		if len(snap) != 2 {
			t.Errorf("snapshot len after Delete = %d, want 2 (snapshot must be detached)", len(snap))
		}
		if snap[0].ID != aID || snap[1].ID != bID {
			t.Errorf("snapshot mutated: got [%q, %q], want [%q, %q]", snap[0].ID, snap[1].ID, aID, bID)
		}
	})

	t.Run("delete-twice-second-misses", func(t *testing.T) {
		t.Parallel()
		r := &Registry{}
		r.Create(Conversation{ID: aID, Cwd: "/a"})
		if !r.Delete(aID) {
			t.Fatal("first Delete = false, want true")
		}
		if r.Delete(aID) {
			t.Errorf("second Delete = true, want false")
		}
	})
}

func TestRegistry_SaveFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix permission semantics required")
	}
	t.Parallel()
	dir := t.TempDir()
	subdir := filepath.Join(dir, "pyry")
	path := filepath.Join(subdir, "conversations.json")
	when := mustParseTime(t, "2026-05-09T12:34:56.789Z")

	r := &Registry{}
	r.Create(Conversation{
		ID:         "11111111-2222-4333-8444-555555555555",
		Cwd:        "/x",
		LastUsedAt: when,
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
		convs := []Conversation{
			{ID: "11111111-2222-4333-8444-555555555555", Cwd: "/a", LastUsedAt: t1},
			{ID: "22222222-2222-4333-8444-555555555555", Cwd: "/b", LastUsedAt: t2},
			{ID: "33333333-2222-4333-8444-555555555555", Cwd: "/c", LastUsedAt: t3},
		}
		r := &Registry{}
		for _, i := range order {
			r.Create(convs[i])
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
	path := filepath.Join(dir, "conversations.json")

	original := []byte(`{"conversations":[{"id":"11111111-2222-4333-8444-555555555555","cwd":"/orig","is_promoted":false,"last_used_at":"2026-01-01T00:00:00Z"}]}`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatalf("write original: %v", err)
	}

	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	when := mustParseTime(t, "2026-05-09T12:34:56.789Z")
	r := &Registry{}
	r.Create(Conversation{
		ID:         "22222222-2222-4333-8444-555555555555",
		Cwd:        "/new",
		LastUsedAt: when,
	})
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
			r.Create(Conversation{
				ID:  ConversationID(fmt.Sprintf("%08d-2222-4333-8444-555555555555", i)),
				Cwd: fmt.Sprintf("/c-%d", i),
			})
			_ = r.List()
			_, _ = r.Get(ConversationID(fmt.Sprintf("%08d-2222-4333-8444-555555555555", 0)))
		}(i)
	}
	wg.Wait()
	if got := len(r.List()); got != n {
		t.Errorf("len(List) = %d, want %d", got, n)
	}
}

func TestRegistry_List_Filter(t *testing.T) {
	t.Parallel()

	mk := func() *Registry {
		r := &Registry{}
		r.Create(Conversation{ID: "11111111-2222-4333-8444-555555555555", Cwd: "/a", IsPromoted: false})
		r.Create(Conversation{ID: "22222222-2222-4333-8444-555555555555", Cwd: "/b", IsPromoted: true})
		r.Create(Conversation{ID: "33333333-2222-4333-8444-555555555555", Cwd: "/c", IsPromoted: false})
		r.Create(Conversation{ID: "44444444-2222-4333-8444-555555555555", Cwd: "/d", IsPromoted: true})
		return r
	}

	tests := []struct {
		name        string
		filter      []ListFilter
		wantPromote map[bool]int
	}{
		{
			name:        "no-filter",
			filter:      nil,
			wantPromote: map[bool]int{true: 2, false: 2},
		},
		{
			name:        "explicit-nil-pointer",
			filter:      []ListFilter{{IsPromoted: nil}},
			wantPromote: map[bool]int{true: 2, false: 2},
		},
		{
			name:        "promoted-true",
			filter:      []ListFilter{{IsPromoted: ptrTo(true)}},
			wantPromote: map[bool]int{true: 2, false: 0},
		},
		{
			name:        "promoted-false",
			filter:      []ListFilter{{IsPromoted: ptrTo(false)}},
			wantPromote: map[bool]int{true: 0, false: 2},
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := mk()
			got := r.List(tc.filter...)
			counts := map[bool]int{}
			for _, c := range got {
				counts[c.IsPromoted]++
			}
			for k, want := range tc.wantPromote {
				if counts[k] != want {
					t.Errorf("count[IsPromoted=%v] = %d, want %d (got = %+v)", k, counts[k], want, got)
				}
			}
		})
	}

	t.Run("returned-slice-is-copy", func(t *testing.T) {
		t.Parallel()
		r := mk()
		first := r.List()
		if len(first) == 0 {
			t.Fatal("List returned empty")
		}
		first[0].Cwd = "MUTATED"
		first = append(first, Conversation{ID: "99999999-2222-4333-8444-555555555555"})
		_ = first

		second := r.List()
		for _, c := range second {
			if c.Cwd == "MUTATED" {
				t.Error("mutation of returned slice element affected registry state")
			}
			if c.ID == "99999999-2222-4333-8444-555555555555" {
				t.Error("append to returned slice affected registry state")
			}
		}
	})
}

func TestRegistry_Update_Hit(t *testing.T) {
	t.Parallel()
	const id ConversationID = "11111111-2222-4333-8444-555555555555"
	when := mustParseTime(t, "2026-05-09T12:34:56.789Z")
	later := when.Add(time.Hour)

	r := &Registry{}
	r.Create(Conversation{ID: id, Cwd: "/x", IsPromoted: false, LastUsedAt: when})

	ok := r.Update(id, func(c *Conversation) {
		c.LastUsedAt = later
		c.IsPromoted = true
		c.Name = strPtr("renamed")
	})
	if !ok {
		t.Fatal("Update returned false, want true")
	}

	got, found := r.Get(id)
	if !found {
		t.Fatal("Get after Update: not found")
	}
	if !got.LastUsedAt.Equal(later) {
		t.Errorf("LastUsedAt = %v, want %v", got.LastUsedAt, later)
	}
	if !got.IsPromoted {
		t.Errorf("IsPromoted = false, want true")
	}
	if got.Name == nil || *got.Name != "renamed" {
		t.Errorf("Name = %v, want pointer to %q", got.Name, "renamed")
	}
}

func TestRegistry_Update_Miss(t *testing.T) {
	t.Parallel()
	const present ConversationID = "11111111-2222-4333-8444-555555555555"
	const absent ConversationID = "22222222-2222-4333-8444-555555555555"

	r := &Registry{}
	r.Create(Conversation{ID: present, Cwd: "/x"})

	called := false
	ok := r.Update(absent, func(c *Conversation) { called = true })
	if ok {
		t.Errorf("Update(absent) = true, want false")
	}
	if called {
		t.Errorf("fn was invoked on miss, want it untouched")
	}

	got, _ := r.Get(present)
	if got.Cwd != "/x" {
		t.Errorf("present entry mutated: Cwd = %q, want %q", got.Cwd, "/x")
	}
}

func TestRegistry_Promote(t *testing.T) {
	t.Parallel()
	const targetID ConversationID = "11111111-2222-4333-8444-555555555555"
	const otherID ConversationID = "22222222-2222-4333-8444-555555555555"
	const absentID ConversationID = "ffffffff-2222-4333-8444-555555555555"

	tests := []struct {
		name           string
		setup          func() *Registry
		id             ConversationID
		input          string
		wantErr        error
		wantPromoted   bool
		wantNamePtr    *string
		assertOther    func(t *testing.T, r *Registry)
	}{
		{
			name: "success",
			setup: func() *Registry {
				r := &Registry{}
				r.Create(Conversation{ID: targetID, Cwd: "/x", IsPromoted: false})
				return r
			},
			id:           targetID,
			input:        "general",
			wantErr:      nil,
			wantPromoted: true,
			wantNamePtr:  strPtr("general"),
		},
		{
			name: "unknown-id",
			setup: func() *Registry {
				r := &Registry{}
				r.Create(Conversation{ID: targetID, Cwd: "/x", IsPromoted: false})
				return r
			},
			id:           absentID,
			input:        "general",
			wantErr:      ErrConversationNotFound,
			wantPromoted: false,
			wantNamePtr:  nil,
		},
		{
			name: "already-promoted",
			setup: func() *Registry {
				r := &Registry{}
				r.Create(Conversation{ID: targetID, Cwd: "/x", IsPromoted: true, Name: strPtr("old")})
				return r
			},
			id:           targetID,
			input:        "new",
			wantErr:      ErrConversationAlreadyPromoted,
			wantPromoted: true,
			wantNamePtr:  strPtr("old"),
		},
		{
			name: "name-conflict-with-promoted",
			setup: func() *Registry {
				r := &Registry{}
				r.Create(Conversation{ID: otherID, Cwd: "/o", IsPromoted: true, Name: strPtr("dup")})
				r.Create(Conversation{ID: targetID, Cwd: "/x", IsPromoted: false})
				return r
			},
			id:           targetID,
			input:        "dup",
			wantErr:      ErrPromotionNameInUse,
			wantPromoted: false,
			wantNamePtr:  nil,
			assertOther: func(t *testing.T, r *Registry) {
				got, ok := r.Get(otherID)
				if !ok {
					t.Fatal("other conversation missing after refusal")
				}
				if !got.IsPromoted || got.Name == nil || *got.Name != "dup" {
					t.Errorf("other conversation mutated: IsPromoted=%v Name=%v", got.IsPromoted, got.Name)
				}
			},
		},
		{
			name: "name-conflict-with-unpromoted-OK",
			setup: func() *Registry {
				r := &Registry{}
				r.Create(Conversation{ID: otherID, Cwd: "/o", IsPromoted: false, Name: strPtr("dup")})
				r.Create(Conversation{ID: targetID, Cwd: "/x", IsPromoted: false})
				return r
			},
			id:           targetID,
			input:        "dup",
			wantErr:      nil,
			wantPromoted: true,
			wantNamePtr:  strPtr("dup"),
			assertOther: func(t *testing.T, r *Registry) {
				got, ok := r.Get(otherID)
				if !ok {
					t.Fatal("other conversation missing after success")
				}
				if got.IsPromoted {
					t.Errorf("other conversation IsPromoted = true, want false (left untouched)")
				}
				if got.Name == nil || *got.Name != "dup" {
					t.Errorf("other conversation Name = %v, want pointer to %q", got.Name, "dup")
				}
			},
		},
		{
			name: "empty-name",
			setup: func() *Registry {
				r := &Registry{}
				r.Create(Conversation{ID: targetID, Cwd: "/x", IsPromoted: false})
				return r
			},
			id:           targetID,
			input:        "",
			wantErr:      ErrPromotionNameEmpty,
			wantPromoted: false,
			wantNamePtr:  nil,
		},
		{
			name: "whitespace-name",
			setup: func() *Registry {
				r := &Registry{}
				r.Create(Conversation{ID: targetID, Cwd: "/x", IsPromoted: false})
				return r
			},
			id:           targetID,
			input:        "   \t\n",
			wantErr:      ErrPromotionNameEmpty,
			wantPromoted: false,
			wantNamePtr:  nil,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := tc.setup()
			err := r.Promote(tc.id, tc.input)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("Promote: err = %v, want nil", err)
				}
			} else {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("Promote: err = %v, want %v", err, tc.wantErr)
				}
			}

			// Inspect the target's post-state. If the target id is absent
			// from the registry, we expect the call to have refused with
			// ErrConversationNotFound and there is nothing further to check
			// on the target — but we still verify Get reports absence.
			got, ok := r.Get(targetID)
			switch {
			case tc.id == absentID:
				if !ok {
					t.Fatalf("target missing from registry after unknown-id call")
				}
				if got.IsPromoted {
					t.Errorf("target IsPromoted = true, want false (untouched after unknown-id)")
				}
				if got.Name != nil {
					t.Errorf("target Name = %v, want nil (untouched after unknown-id)", got.Name)
				}
			default:
				if !ok {
					t.Fatalf("target missing from registry")
				}
				if got.IsPromoted != tc.wantPromoted {
					t.Errorf("target IsPromoted = %v, want %v", got.IsPromoted, tc.wantPromoted)
				}
				switch {
				case tc.wantNamePtr == nil:
					if got.Name != nil {
						t.Errorf("target Name = %v, want nil", got.Name)
					}
				default:
					if got.Name == nil {
						t.Errorf("target Name = nil, want pointer to %q", *tc.wantNamePtr)
					} else if *got.Name != *tc.wantNamePtr {
						t.Errorf("target *Name = %q, want %q", *got.Name, *tc.wantNamePtr)
					}
				}
			}

			if tc.assertOther != nil {
				tc.assertOther(t, r)
			}
		})
	}
}

func TestRegistry_Promote_DoesNotPersist(t *testing.T) {
	t.Parallel()
	const id ConversationID = "11111111-2222-4333-8444-555555555555"
	when := mustParseTime(t, "2026-05-09T12:34:56.789Z")

	r := &Registry{}
	r.Create(Conversation{ID: id, Cwd: "/x", IsPromoted: false, LastUsedAt: when})

	path := filepath.Join(t.TempDir(), "conversations.json")
	if err := r.Save(path); err != nil {
		t.Fatalf("Save (pre-promote): %v", err)
	}
	if err := r.Promote(id, "general"); err != nil {
		t.Fatalf("Promote: %v", err)
	}

	back, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got, ok := back.Get(id)
	if !ok {
		t.Fatalf("Get after Load: not found")
	}
	if got.IsPromoted {
		t.Errorf("loaded IsPromoted = true, want false (Promote must not persist implicitly)")
	}
	if got.Name != nil {
		t.Errorf("loaded Name = %v, want nil", got.Name)
	}
}

func TestRegistry_Update_PointerStability(t *testing.T) {
	t.Parallel()
	const id ConversationID = "11111111-2222-4333-8444-555555555555"
	r := &Registry{}
	r.Create(Conversation{ID: id, Cwd: "/before"})

	r.Update(id, func(c *Conversation) {
		c.Cwd = "/after"
		c.SessionHistory = append(c.SessionHistory, "sess-1", "sess-2")
	})

	got, _ := r.Get(id)
	if got.Cwd != "/after" {
		t.Errorf("Cwd = %q, want %q", got.Cwd, "/after")
	}
	if len(got.SessionHistory) != 2 || got.SessionHistory[0] != "sess-1" || got.SessionHistory[1] != "sess-2" {
		t.Errorf("SessionHistory = %v, want [sess-1 sess-2]", got.SessionHistory)
	}
}

// TestRegistry_RebindSession_Hit covers AC#1: the owning conversation's
// CurrentSessionID becomes newID and oldID is appended to SessionHistory, while
// unrelated rows are left byte-identical.
func TestRegistry_RebindSession_Hit(t *testing.T) {
	t.Parallel()
	const (
		ownerID ConversationID = "11111111-2222-4333-8444-555555555555"
		otherID ConversationID = "22222222-2222-4333-8444-555555555555"
		oldSess                = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		newSess                = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	)
	when := mustParseTime(t, "2026-05-09T12:34:56.789Z")

	r := &Registry{}
	r.Create(Conversation{ID: ownerID, Cwd: "/owner", CurrentSessionID: oldSess, LastUsedAt: when})
	other := Conversation{ID: otherID, Cwd: "/other", CurrentSessionID: "cccccccc-cccc-4ccc-8ccc-cccccccccccc", LastUsedAt: when}
	r.Create(other)

	if ok := r.RebindSession(oldSess, newSess); !ok {
		t.Fatal("RebindSession returned false, want true")
	}

	got, found := r.Get(ownerID)
	if !found {
		t.Fatal("Get(owner) after rebind: not found")
	}
	if got.CurrentSessionID != newSess {
		t.Errorf("CurrentSessionID = %q, want %q", got.CurrentSessionID, newSess)
	}
	if len(got.SessionHistory) != 1 || got.SessionHistory[len(got.SessionHistory)-1] != oldSess {
		t.Errorf("SessionHistory = %v, want tail %q (len 1)", got.SessionHistory, oldSess)
	}

	// The unrelated row is untouched.
	gotOther, _ := r.Get(otherID)
	if !reflect.DeepEqual(gotOther, other) {
		t.Errorf("unrelated row mutated: got %+v, want %+v", gotOther, other)
	}
}

// TestRegistry_RebindSession_AppendOrder covers AC#1's oldest-first contract: a
// conversation that already carries history gets oldID appended at the tail
// (append-in-place, not prepend).
func TestRegistry_RebindSession_AppendOrder(t *testing.T) {
	t.Parallel()
	const (
		ownerID ConversationID = "11111111-2222-4333-8444-555555555555"
		s0                     = "00000000-0000-4000-8000-000000000000"
		oldSess                = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		newSess                = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	)
	r := &Registry{}
	r.Create(Conversation{ID: ownerID, Cwd: "/owner", CurrentSessionID: oldSess, SessionHistory: []string{s0}})

	if ok := r.RebindSession(oldSess, newSess); !ok {
		t.Fatal("RebindSession returned false, want true")
	}

	got, _ := r.Get(ownerID)
	want := []string{s0, oldSess}
	if !reflect.DeepEqual(got.SessionHistory, want) {
		t.Errorf("SessionHistory = %v, want %v (oldest-first append)", got.SessionHistory, want)
	}
}

// TestRegistry_RebindSession_Miss covers AC#4: a rotation of a session no
// conversation owns mutates nothing and returns false.
func TestRegistry_RebindSession_Miss(t *testing.T) {
	t.Parallel()
	const (
		ownerID ConversationID = "11111111-2222-4333-8444-555555555555"
		bound                  = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		unowned                = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
		newSess                = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	)
	r := &Registry{}
	orig := Conversation{ID: ownerID, Cwd: "/owner", CurrentSessionID: bound}
	r.Create(orig)

	if ok := r.RebindSession(unowned, newSess); ok {
		t.Errorf("RebindSession(unowned) = true, want false")
	}
	got, _ := r.Get(ownerID)
	if !reflect.DeepEqual(got, orig) {
		t.Errorf("row mutated on miss: got %+v, want %+v", got, orig)
	}
}

// TestRegistry_RebindSession_EmptyOldIDGuard covers the security layer-2
// data-integrity guard: an empty oldID must never match an unbound conversation
// (CurrentSessionID == "") and must return false without mutation.
func TestRegistry_RebindSession_EmptyOldIDGuard(t *testing.T) {
	t.Parallel()
	const (
		unboundID ConversationID = "11111111-2222-4333-8444-555555555555"
		newSess                  = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	)
	r := &Registry{}
	unbound := Conversation{ID: unboundID, Cwd: "/unbound"} // CurrentSessionID == ""
	r.Create(unbound)

	if ok := r.RebindSession("", newSess); ok {
		t.Fatal("RebindSession(\"\", new) = true, want false (must not match unbound row)")
	}
	got, _ := r.Get(unboundID)
	if !reflect.DeepEqual(got, unbound) {
		t.Errorf("unbound row mutated by empty-oldID call: got %+v, want %+v", got, unbound)
	}
}

// TestRegistry_RebindSession_DoesNotPersist mirrors
// TestRegistry_Promote_DoesNotPersist: RebindSession alone writes no file; the
// caller owns Save.
func TestRegistry_RebindSession_DoesNotPersist(t *testing.T) {
	t.Parallel()
	const (
		ownerID ConversationID = "11111111-2222-4333-8444-555555555555"
		oldSess                = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		newSess                = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	)
	when := mustParseTime(t, "2026-05-09T12:34:56.789Z")
	r := &Registry{}
	r.Create(Conversation{ID: ownerID, Cwd: "/owner", CurrentSessionID: oldSess, LastUsedAt: when})

	path := filepath.Join(t.TempDir(), "conversations.json")
	if err := r.Save(path); err != nil {
		t.Fatalf("Save (pre-rebind): %v", err)
	}
	if ok := r.RebindSession(oldSess, newSess); !ok {
		t.Fatal("RebindSession returned false, want true")
	}

	back, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got, ok := back.Get(ownerID)
	if !ok {
		t.Fatal("Get after Load: not found")
	}
	if got.CurrentSessionID != oldSess {
		t.Errorf("loaded CurrentSessionID = %q, want %q (RebindSession must not persist implicitly)", got.CurrentSessionID, oldSess)
	}
	if len(got.SessionHistory) != 0 {
		t.Errorf("loaded SessionHistory = %v, want empty (no implicit persist)", got.SessionHistory)
	}
}

// TestRegistry_RebindSession_FirstMatchOnly: two rows pathologically bound to
// the same oldID — only the first is rebound; the second is untouched.
func TestRegistry_RebindSession_FirstMatchOnly(t *testing.T) {
	t.Parallel()
	const (
		firstID  ConversationID = "11111111-2222-4333-8444-555555555555"
		secondID ConversationID = "22222222-2222-4333-8444-555555555555"
		dup                     = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		newSess                 = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	)
	r := &Registry{}
	r.Create(Conversation{ID: firstID, Cwd: "/first", CurrentSessionID: dup})
	r.Create(Conversation{ID: secondID, Cwd: "/second", CurrentSessionID: dup})

	if ok := r.RebindSession(dup, newSess); !ok {
		t.Fatal("RebindSession returned false, want true")
	}

	first, _ := r.Get(firstID)
	if first.CurrentSessionID != newSess {
		t.Errorf("first row CurrentSessionID = %q, want %q (rebound)", first.CurrentSessionID, newSess)
	}
	second, _ := r.Get(secondID)
	if second.CurrentSessionID != dup {
		t.Errorf("second row CurrentSessionID = %q, want %q (untouched)", second.CurrentSessionID, dup)
	}
	if len(second.SessionHistory) != 0 {
		t.Errorf("second row SessionHistory = %v, want empty (untouched)", second.SessionHistory)
	}
}

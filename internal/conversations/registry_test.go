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

// TestRegistry_SaveConcurrentNoLostUpdate is the #868 regression guard: many
// goroutines each mutate the registry and immediately Save concurrently, so the
// snapshot→encode→fsync→rename sequences overlap in time. Without saveMu an
// older snapshot's rename can land after a newer one's, leaving the on-disk file
// missing a record a prior completed Save already wrote; with saveMu the whole
// sequence serializes and rename order matches snapshot order, so the final file
// holds every record.
//
// Each goroutine Creates then *immediately* Saves (one mutation per save) rather
// than a mutate-all-then-save-all barrier: a barrier would make every Save
// snapshot the identical full state, so no Save could ever clobber another and
// the test would pass even on the unfixed code (vacuous). The outer loop drives
// per-iteration detection probability toward 1 for the pre-fix code; on the
// fixed code the membership assertion holds deterministically every iteration.
// Run under -race to cover the "no Save-vs-mutator deadlock, no new race" half
// of the AC.
func TestRegistry_SaveConcurrentNoLostUpdate(t *testing.T) {
	t.Parallel()
	when := mustParseTime(t, "2026-05-09T12:34:56.789Z")
	const iterations = 15
	const n = 24

	for iter := 0; iter < iterations; iter++ {
		path := filepath.Join(t.TempDir(), "conversations.json")
		r := &Registry{}

		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				r.Create(Conversation{
					ID:         ConversationID(fmt.Sprintf("%08d-2222-4333-8444-555555555555", i)),
					Cwd:        fmt.Sprintf("/c-%d", i),
					LastUsedAt: when.Add(time.Duration(i) * time.Second),
				})
				if err := r.Save(path); err != nil {
					t.Errorf("iter %d goroutine %d: Save: %v", iter, i, err)
				}
			}(i)
		}
		wg.Wait()

		// Every goroutine's Create happens-before its own Save, so the final
		// in-memory state is all n records; AC#1 says the on-disk file — written
		// by the last Save to rename — must match it (no lost update).
		back, err := Load(path)
		if err != nil {
			t.Fatalf("iter %d: Load: %v", iter, err)
		}
		got := back.List()
		if len(got) != n {
			t.Fatalf("iter %d: on-disk record count = %d, want %d (an older snapshot clobbered a newer one)", iter, len(got), n)
		}
		for i := 0; i < n; i++ {
			id := ConversationID(fmt.Sprintf("%08d-2222-4333-8444-555555555555", i))
			if _, ok := back.Get(id); !ok {
				t.Errorf("iter %d: on-disk file missing conversation %q (lost update)", iter, id)
			}
		}
	}
}

func TestRegistry_List_Filter(t *testing.T) {
	t.Parallel()

	// Four conversations spanning every (IsPromoted, IsArchived) combination so
	// a filter's expected result is a single unambiguous id. The two flags AND:
	// a set IsPromoted and a set IsArchived both have to match.
	const (
		idActiveDiscussion = ConversationID("11111111-2222-4333-8444-555555555555") // promoted=F archived=F
		idActiveChannel    = ConversationID("22222222-2222-4333-8444-555555555555") // promoted=T archived=F
		idArchDiscussion   = ConversationID("33333333-2222-4333-8444-555555555555") // promoted=F archived=T
		idArchChannel      = ConversationID("44444444-2222-4333-8444-555555555555") // promoted=T archived=T
	)
	mk := func() *Registry {
		r := &Registry{}
		r.Create(Conversation{ID: idActiveDiscussion, Cwd: "/a", IsPromoted: false, IsArchived: false})
		r.Create(Conversation{ID: idActiveChannel, Cwd: "/b", IsPromoted: true, IsArchived: false})
		r.Create(Conversation{ID: idArchDiscussion, Cwd: "/c", IsPromoted: false, IsArchived: true})
		r.Create(Conversation{ID: idArchChannel, Cwd: "/d", IsPromoted: true, IsArchived: true})
		return r
	}

	tests := []struct {
		name    string
		filter  []ListFilter
		wantIDs []ConversationID
	}{
		{
			name:    "no-filter",
			filter:  nil,
			wantIDs: []ConversationID{idActiveDiscussion, idActiveChannel, idArchDiscussion, idArchChannel},
		},
		{
			name:    "explicit-nil-pointers",
			filter:  []ListFilter{{IsPromoted: nil, IsArchived: nil}},
			wantIDs: []ConversationID{idActiveDiscussion, idActiveChannel, idArchDiscussion, idArchChannel},
		},
		{
			name:    "promoted-true",
			filter:  []ListFilter{{IsPromoted: ptrTo(true)}},
			wantIDs: []ConversationID{idActiveChannel, idArchChannel},
		},
		{
			name:    "promoted-false",
			filter:  []ListFilter{{IsPromoted: ptrTo(false)}},
			wantIDs: []ConversationID{idActiveDiscussion, idArchDiscussion},
		},
		{
			// AC3: unfiltered on archived returns both; here we narrow.
			name:    "archived-true",
			filter:  []ListFilter{{IsArchived: ptrTo(true)}},
			wantIDs: []ConversationID{idArchDiscussion, idArchChannel},
		},
		{
			name:    "archived-false",
			filter:  []ListFilter{{IsArchived: ptrTo(false)}},
			wantIDs: []ConversationID{idActiveDiscussion, idActiveChannel},
		},
		{
			// AND semantics: both fields set → intersection is one row.
			name:    "promoted-and-archived",
			filter:  []ListFilter{{IsPromoted: ptrTo(true), IsArchived: ptrTo(true)}},
			wantIDs: []ConversationID{idArchChannel},
		},
		{
			name:    "unpromoted-and-active",
			filter:  []ListFilter{{IsPromoted: ptrTo(false), IsArchived: ptrTo(false)}},
			wantIDs: []ConversationID{idActiveDiscussion},
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := mk()
			got := r.List(tc.filter...)
			gotIDs := map[ConversationID]bool{}
			for _, c := range got {
				gotIDs[c.ID] = true
			}
			if len(got) != len(tc.wantIDs) {
				t.Errorf("len(List) = %d, want %d (got = %+v)", len(got), len(tc.wantIDs), got)
			}
			for _, want := range tc.wantIDs {
				if !gotIDs[want] {
					t.Errorf("missing id %q (got = %+v)", want, got)
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
		name         string
		setup        func() *Registry
		id           ConversationID
		input        string
		wantErr      error
		wantPromoted bool
		wantNamePtr  *string
		assertOther  func(t *testing.T, r *Registry)
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
		ownerID = "11111111-2222-4333-8444-555555555555"
		s0      = "00000000-0000-4000-8000-000000000000"
		oldSess = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		newSess = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
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
		ownerID = "11111111-2222-4333-8444-555555555555"
		bound   = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		unowned = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
		newSess = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
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
		unboundID = "11111111-2222-4333-8444-555555555555"
		newSess   = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
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
		ownerID = "11111111-2222-4333-8444-555555555555"
		oldSess = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		newSess = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
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

func TestRegistry_SetArchived_HitSetsAndClears(t *testing.T) {
	t.Parallel()
	const id ConversationID = "11111111-2222-4333-8444-555555555555"
	when := mustParseTime(t, "2026-05-09T12:34:56.789Z")

	r := &Registry{}
	r.Create(Conversation{
		ID:               id,
		Name:             strPtr("general"),
		Cwd:              "/home/user/project",
		CurrentSessionID: "sess-current",
		SessionHistory:   []string{"sess-old"},
		IsPromoted:       true,
		LastUsedAt:       when,
	})

	// Archive → true, and every other field is left untouched (the
	// "flips exactly one field" guarantee).
	if ok := r.SetArchived(id, true); !ok {
		t.Fatal("SetArchived(true) = false, want true")
	}
	got, found := r.Get(id)
	if !found {
		t.Fatal("Get after SetArchived: not found")
	}
	if !got.IsArchived {
		t.Error("IsArchived = false, want true after SetArchived(true)")
	}
	if got.Name == nil || *got.Name != "general" {
		t.Errorf("Name = %v, want pointer to %q (untouched)", got.Name, "general")
	}
	if got.Cwd != "/home/user/project" {
		t.Errorf("Cwd = %q, want unchanged", got.Cwd)
	}
	if got.CurrentSessionID != "sess-current" {
		t.Errorf("CurrentSessionID = %q, want unchanged", got.CurrentSessionID)
	}
	if len(got.SessionHistory) != 1 || got.SessionHistory[0] != "sess-old" {
		t.Errorf("SessionHistory = %v, want [sess-old] (untouched)", got.SessionHistory)
	}
	if !got.IsPromoted {
		t.Error("IsPromoted = false, want true (untouched)")
	}
	if !got.LastUsedAt.Equal(when) {
		t.Errorf("LastUsedAt = %v, want %v (untouched)", got.LastUsedAt, when)
	}

	// Clear → false via the same method (symmetric toggle).
	if ok := r.SetArchived(id, false); !ok {
		t.Fatal("SetArchived(false) = false, want true")
	}
	got, _ = r.Get(id)
	if got.IsArchived {
		t.Error("IsArchived = true, want false after SetArchived(false)")
	}
}

func TestRegistry_SetArchived_Miss(t *testing.T) {
	t.Parallel()
	const present ConversationID = "11111111-2222-4333-8444-555555555555"
	const absent ConversationID = "22222222-2222-4333-8444-555555555555"

	r := &Registry{}
	r.Create(Conversation{ID: present, Cwd: "/x", IsArchived: false})

	if ok := r.SetArchived(absent, true); ok {
		t.Errorf("SetArchived(absent) = true, want false")
	}
	// Registry left unmodified on miss: the sentinel row is untouched.
	if n := len(r.List()); n != 1 {
		t.Errorf("len(List) = %d, want 1 (unchanged on miss)", n)
	}
	got, _ := r.Get(present)
	if got.IsArchived {
		t.Error("present row IsArchived = true, want false (miss must not mutate)")
	}
}

func TestRegistry_SetArchived_Idempotent(t *testing.T) {
	t.Parallel()
	const id ConversationID = "11111111-2222-4333-8444-555555555555"
	r := &Registry{}
	r.Create(Conversation{ID: id, Cwd: "/x"})

	if ok := r.SetArchived(id, true); !ok {
		t.Fatal("first SetArchived(true) = false, want true")
	}
	if ok := r.SetArchived(id, true); !ok {
		t.Fatal("second SetArchived(true) = false, want true")
	}
	got, _ := r.Get(id)
	if !got.IsArchived {
		t.Error("IsArchived = false, want true (stays archived across repeated calls)")
	}
}

// AC2: the archived flag survives a Save → Load round-trip in both states.
func TestRegistry_SetArchived_RoundTrip(t *testing.T) {
	t.Parallel()
	const archivedID ConversationID = "11111111-2222-4333-8444-555555555555"
	const activeID ConversationID = "22222222-2222-4333-8444-555555555555"
	when := mustParseTime(t, "2026-05-09T12:34:56.789Z")

	r := &Registry{}
	r.Create(Conversation{ID: archivedID, Cwd: "/a", IsArchived: true, LastUsedAt: when})
	r.Create(Conversation{ID: activeID, Cwd: "/b", IsArchived: false, LastUsedAt: when.Add(time.Second)})

	path := filepath.Join(t.TempDir(), "conversations.json")
	if err := r.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	back, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	gotArchived, ok := back.Get(archivedID)
	if !ok {
		t.Fatal("archived conversation missing after reload")
	}
	if !gotArchived.IsArchived {
		t.Error("archived conversation reloaded as active, want archived")
	}
	gotActive, ok := back.Get(activeID)
	if !ok {
		t.Fatal("active conversation missing after reload")
	}
	if gotActive.IsArchived {
		t.Error("active conversation reloaded as archived, want active")
	}
}

// AC1: a pre-existing on-disk row without an is_archived key decodes as active,
// with no migration step.
func TestRegistry_Load_AbsentArchivedKeyDecodesActive(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "conversations.json")
	// Hand-written registry: the row omits is_archived entirely, as a pre-#880
	// file would.
	raw := `{"conversations":[{"id":"11111111-2222-4333-8444-555555555555","cwd":"/legacy","is_promoted":false,"last_used_at":"2026-05-09T12:34:56.789Z"}]}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	r, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got, ok := r.Get("11111111-2222-4333-8444-555555555555")
	if !ok {
		t.Fatal("legacy row missing after Load")
	}
	if got.IsArchived {
		t.Error("absent is_archived key decoded as archived, want active (false)")
	}
}

// AC1 (byte-stability): an all-active registry serializes with no is_archived
// key (omitempty), so it is byte-identical to a pre-#880 file; Save→Load→Save is
// a fixed point.
func TestRegistry_Save_ActiveOmitsArchivedKey(t *testing.T) {
	t.Parallel()
	when := mustParseTime(t, "2026-05-09T12:34:56.789Z")
	r := &Registry{}
	r.Create(Conversation{ID: "11111111-2222-4333-8444-555555555555", Cwd: "/a", LastUsedAt: when})
	r.Create(Conversation{ID: "22222222-2222-4333-8444-555555555555", Cwd: "/b", LastUsedAt: when.Add(time.Second)})

	path := filepath.Join(t.TempDir(), "conversations.json")
	if err := r.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after save: %v", err)
	}
	if strings.Contains(string(first), "is_archived") {
		t.Errorf("all-active registry serialized an is_archived key:\n%s", first)
	}

	// Save → Load → Save is a fixed point (byte-stable reload discipline).
	back, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	path2 := filepath.Join(t.TempDir(), "conversations.json")
	if err := back.Save(path2); err != nil {
		t.Fatalf("re-Save: %v", err)
	}
	second, err := os.ReadFile(path2)
	if err != nil {
		t.Fatalf("read after re-save: %v", err)
	}
	if string(first) != string(second) {
		t.Errorf("Save→Load→Save not byte-identical:\n first = %s\nsecond = %s", first, second)
	}
}

// #2149 AC2/AC5: the setter stores a valid prompt verbatim, touches exactly one
// field, and does not alias the caller's pointer.
func TestRegistry_SetSystemPrompt_HitStoresVerbatim(t *testing.T) {
	t.Parallel()
	const id ConversationID = "11111111-2222-4333-8444-555555555555"
	when := mustParseTime(t, "2026-09-04T12:34:56.789Z")

	r := &Registry{}
	r.Create(Conversation{
		ID:               id,
		Name:             strPtr("general"),
		Cwd:              "/home/user/project",
		CurrentSessionID: "sess-current",
		SessionHistory:   []string{"sess-old"},
		IsPromoted:       true,
		IsArchived:       true,
		LastUsedAt:       when,
	})

	const prompt = "Answer in Finnish.\nBe terse — ä ö å 🐍"
	in := prompt
	if err := r.SetSystemPrompt(id, &in); err != nil {
		t.Fatalf("SetSystemPrompt: %v", err)
	}

	got, found := r.Get(id)
	if !found {
		t.Fatal("Get after SetSystemPrompt: not found")
	}
	if got.SystemPrompt == nil {
		t.Fatal("SystemPrompt = nil, want a stored value")
	}
	if *got.SystemPrompt != prompt {
		t.Errorf("SystemPrompt = %q, want %q (stored verbatim)", *got.SystemPrompt, prompt)
	}
	// The stored pointer is a fresh local, never the caller's variable.
	if got.SystemPrompt == &in {
		t.Error("SystemPrompt aliases the caller's pointer, want a fresh copy")
	}

	// Every other field is untouched (the single-field guarantee).
	if got.Name == nil || *got.Name != "general" {
		t.Errorf("Name = %v, want pointer to %q (untouched)", got.Name, "general")
	}
	if got.Cwd != "/home/user/project" {
		t.Errorf("Cwd = %q, want unchanged", got.Cwd)
	}
	if got.CurrentSessionID != "sess-current" {
		t.Errorf("CurrentSessionID = %q, want unchanged", got.CurrentSessionID)
	}
	if len(got.SessionHistory) != 1 || got.SessionHistory[0] != "sess-old" {
		t.Errorf("SessionHistory = %v, want [sess-old] (untouched)", got.SessionHistory)
	}
	if !got.IsPromoted {
		t.Error("IsPromoted = false, want true (untouched)")
	}
	if !got.IsArchived {
		t.Error("IsArchived = false, want true (untouched)")
	}
	if !got.LastUsedAt.Equal(when) {
		t.Errorf("LastUsedAt = %v, want %v (untouched)", got.LastUsedAt, when)
	}
}

// #2149 AC2/AC5: each refusal is a distinct static sentinel, leaves every record
// untouched, and carries no fragment of the rejected value. The two combined
// rows pin the documented refusal ordering: length is the O(1) gate and wins.
func TestRegistry_SetSystemPrompt_Refusals(t *testing.T) {
	t.Parallel()
	const present ConversationID = "11111111-2222-4333-8444-555555555555"
	const absent ConversationID = "22222222-2222-4333-8444-555555555555"
	const marker = "MARKER_K3M9P2X7"

	tooLong := marker + strings.Repeat("a", MaxSystemPromptBytes+1-len(marker))
	badUTF8 := marker + "\xff\xfe"
	tooLongAndBad := badUTF8 + strings.Repeat("a", MaxSystemPromptBytes+1-len(badUTF8))

	tests := []struct {
		name    string
		id      ConversationID
		prompt  string
		wantErr error
	}{
		{name: "too-long-by-one-byte", id: present, prompt: tooLong, wantErr: ErrSystemPromptTooLong},
		{name: "invalid-utf8", id: present, prompt: badUTF8, wantErr: ErrSystemPromptInvalidUTF8},
		{name: "unknown-id", id: absent, prompt: marker + " valid", wantErr: ErrConversationNotFound},
		{name: "too-long-and-invalid-utf8", id: present, prompt: tooLongAndBad, wantErr: ErrSystemPromptTooLong},
		{name: "unknown-id-and-too-long", id: absent, prompt: tooLong, wantErr: ErrSystemPromptTooLong},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			when := mustParseTime(t, "2026-09-04T12:34:56.789Z")
			r := &Registry{}
			r.Create(Conversation{
				ID:         present,
				Name:       strPtr("general"),
				Cwd:        "/home/user/project",
				IsPromoted: true,
				LastUsedAt: when,
			})

			p := tc.prompt
			err := r.SetSystemPrompt(tc.id, &p)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("SetSystemPrompt err = %v, want %v", err, tc.wantErr)
			}
			// AC5: the sentinel carries no fragment of the value.
			if strings.Contains(err.Error(), marker) {
				t.Errorf("error text leaks the rejected value: %q", err.Error())
			}

			// Every record is untouched.
			got, found := r.Get(present)
			if !found {
				t.Fatal("seeded row missing after a refusal")
			}
			if got.SystemPrompt != nil {
				t.Errorf("SystemPrompt = %v, want nil (refusal must not store)", got.SystemPrompt)
			}
			if got.Name == nil || *got.Name != "general" || got.Cwd != "/home/user/project" || !got.IsPromoted {
				t.Errorf("seeded row mutated by a refusal: %+v", got)
			}
			if !got.LastUsedAt.Equal(when) {
				t.Errorf("LastUsedAt = %v, want %v (untouched)", got.LastUsedAt, when)
			}
		})
	}
}

// #2149 AC2: the bound is inclusive and counted in bytes, not runes.
func TestRegistry_SetSystemPrompt_BoundaryIsBytes(t *testing.T) {
	t.Parallel()
	const id ConversationID = "11111111-2222-4333-8444-555555555555"

	// "ä" is two bytes: 4096 of them are exactly at the bound, 4097 are over it
	// while being far below the bound in runes.
	tests := []struct {
		name    string
		prompt  string
		wantErr error
	}{
		{name: "exactly-max-ascii", prompt: strings.Repeat("a", MaxSystemPromptBytes)},
		{name: "one-byte-over-ascii", prompt: strings.Repeat("a", MaxSystemPromptBytes+1), wantErr: ErrSystemPromptTooLong},
		{name: "exactly-max-two-byte-runes", prompt: strings.Repeat("ä", MaxSystemPromptBytes/2)},
		{name: "over-max-two-byte-runes", prompt: strings.Repeat("ä", MaxSystemPromptBytes/2+1), wantErr: ErrSystemPromptTooLong},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := &Registry{}
			r.Create(Conversation{ID: id, Cwd: "/x"})

			p := tc.prompt
			err := r.SetSystemPrompt(id, &p)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("SetSystemPrompt(%d bytes) = %v, want nil", len(p), err)
				}
				got, _ := r.Get(id)
				if got.SystemPrompt == nil || *got.SystemPrompt != tc.prompt {
					t.Errorf("stored value differs from the %d-byte input", len(p))
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("SetSystemPrompt(%d bytes) err = %v, want %v", len(p), err, tc.wantErr)
			}
			got, _ := r.Get(id)
			if got.SystemPrompt != nil {
				t.Error("SystemPrompt stored despite a refusal")
			}
		})
	}
}

// #2149 AC1/AC2: a nil argument returns the row to "none" without value
// validation, but still refuses an unknown id; a pointer to "" stores the
// explicitly-empty state.
func TestRegistry_SetSystemPrompt_ClearAndExplicitlyEmpty(t *testing.T) {
	t.Parallel()
	const id ConversationID = "11111111-2222-4333-8444-555555555555"
	const absent ConversationID = "22222222-2222-4333-8444-555555555555"

	r := &Registry{}
	r.Create(Conversation{ID: id, Cwd: "/x"})

	p := "be terse"
	if err := r.SetSystemPrompt(id, &p); err != nil {
		t.Fatalf("SetSystemPrompt(value): %v", err)
	}
	if err := r.SetSystemPrompt(id, nil); err != nil {
		t.Fatalf("SetSystemPrompt(nil): %v", err)
	}
	got, _ := r.Get(id)
	if got.SystemPrompt != nil {
		t.Errorf("SystemPrompt = %v, want nil after clearing", got.SystemPrompt)
	}

	empty := ""
	if err := r.SetSystemPrompt(id, &empty); err != nil {
		t.Fatalf("SetSystemPrompt(\"\"): %v", err)
	}
	got, _ = r.Get(id)
	if got.SystemPrompt == nil {
		t.Fatal("SystemPrompt = nil, want a non-nil pointer to \"\" (explicitly empty)")
	}
	if *got.SystemPrompt != "" {
		t.Errorf("SystemPrompt = %q, want \"\"", *got.SystemPrompt)
	}

	// Clearing an unknown id is still a refusal: the nil path skips value
	// validation, not the identity check.
	if err := r.SetSystemPrompt(absent, nil); !errors.Is(err, ErrConversationNotFound) {
		t.Errorf("SetSystemPrompt(absent, nil) err = %v, want ErrConversationNotFound", err)
	}
}

// #2149 AC3: every prompt state survives Save → Load unchanged, including one
// carrying newlines and non-ASCII text, and the three states stay
// distinguishable on disk.
func TestRegistry_SetSystemPrompt_RoundTrip(t *testing.T) {
	t.Parallel()
	const noneID ConversationID = "11111111-2222-4333-8444-555555555555"
	const emptyID ConversationID = "22222222-2222-4333-8444-555555555555"
	const setID ConversationID = "33333333-2222-4333-8444-555555555555"
	const prompt = "Vastaa suomeksi.\n\nOle ytimekäs — ä ö å 🐍\ttab"
	when := mustParseTime(t, "2026-09-04T12:34:56.789Z")

	r := &Registry{}
	r.Create(Conversation{ID: noneID, Cwd: "/a", LastUsedAt: when})
	r.Create(Conversation{ID: emptyID, Cwd: "/b", SystemPrompt: strPtr(""), LastUsedAt: when.Add(time.Second)})
	r.Create(Conversation{ID: setID, Cwd: "/c", LastUsedAt: when.Add(2 * time.Second)})
	p := prompt
	if err := r.SetSystemPrompt(setID, &p); err != nil {
		t.Fatalf("SetSystemPrompt: %v", err)
	}

	path := filepath.Join(t.TempDir(), "conversations.json")
	if err := r.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after save: %v", err)
	}
	// Exactly two of the three rows carry the key on disk.
	if n := strings.Count(string(raw), `"system_prompt"`); n != 2 {
		t.Errorf("system_prompt key count = %d, want 2 (none omits it):\n%s", n, raw)
	}
	if !strings.Contains(string(raw), `"system_prompt": ""`) {
		t.Errorf("explicitly-empty row did not serialize an empty system_prompt:\n%s", raw)
	}

	back, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	gotNone, ok := back.Get(noneID)
	if !ok {
		t.Fatal("none row missing after reload")
	}
	if gotNone.SystemPrompt != nil {
		t.Errorf("none row reloaded with %v, want nil", gotNone.SystemPrompt)
	}
	gotEmpty, ok := back.Get(emptyID)
	if !ok {
		t.Fatal("explicitly-empty row missing after reload")
	}
	if gotEmpty.SystemPrompt == nil || *gotEmpty.SystemPrompt != "" {
		t.Errorf("explicitly-empty row reloaded as %v, want a non-nil pointer to \"\"", gotEmpty.SystemPrompt)
	}
	gotSet, ok := back.Get(setID)
	if !ok {
		t.Fatal("prompt row missing after reload")
	}
	if gotSet.SystemPrompt == nil || *gotSet.SystemPrompt != prompt {
		t.Errorf("prompt row reloaded as %v, want %q unchanged", gotSet.SystemPrompt, prompt)
	}
}

// #2149 AC4: a registry whose rows all hold no prompt serializes with no
// system_prompt key, so it is byte-identical to its pre-#2149 form, and
// Save→Load→Save is a fixed point.
func TestRegistry_Save_NoPromptOmitsKey(t *testing.T) {
	t.Parallel()
	when := mustParseTime(t, "2026-09-04T12:34:56.789Z")
	r := &Registry{}
	r.Create(Conversation{ID: "11111111-2222-4333-8444-555555555555", Cwd: "/a", LastUsedAt: when})
	r.Create(Conversation{ID: "22222222-2222-4333-8444-555555555555", Cwd: "/b", LastUsedAt: when.Add(time.Second)})

	path := filepath.Join(t.TempDir(), "conversations.json")
	if err := r.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after save: %v", err)
	}
	if strings.Contains(string(first), "system_prompt") {
		t.Errorf("all-none registry serialized a system_prompt key:\n%s", first)
	}

	back, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	path2 := filepath.Join(t.TempDir(), "conversations.json")
	if err := back.Save(path2); err != nil {
		t.Fatalf("re-Save: %v", err)
	}
	second, err := os.ReadFile(path2)
	if err != nil {
		t.Fatalf("read after re-save: %v", err)
	}
	if string(first) != string(second) {
		t.Errorf("Save→Load→Save not byte-identical:\n first = %s\nsecond = %s", first, second)
	}
}

// #2149 AC4: a pre-existing on-disk row without a system_prompt key decodes as
// "no prompt", with no migration step.
func TestRegistry_Load_AbsentPromptKeyDecodesNone(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "conversations.json")
	raw := `{"conversations":[{"id":"11111111-2222-4333-8444-555555555555","cwd":"/legacy","is_promoted":false,"last_used_at":"2026-09-04T12:34:56.789Z"}]}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	r, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got, ok := r.Get("11111111-2222-4333-8444-555555555555")
	if !ok {
		t.Fatal("legacy row missing after Load")
	}
	if got.SystemPrompt != nil {
		t.Errorf("absent system_prompt key decoded as %v, want nil", got.SystemPrompt)
	}
}

// #2149: SetSystemPrompt does not persist implicitly — the Create / Update /
// Promote / SetArchived convention.
func TestRegistry_SetSystemPrompt_DoesNotPersist(t *testing.T) {
	t.Parallel()
	const id ConversationID = "11111111-2222-4333-8444-555555555555"
	r := &Registry{}
	r.Create(Conversation{ID: id, Cwd: "/x", LastUsedAt: mustParseTime(t, "2026-09-04T12:34:56.789Z")})

	path := filepath.Join(t.TempDir(), "conversations.json")
	if err := r.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	p := "be terse"
	if err := r.SetSystemPrompt(id, &p); err != nil {
		t.Fatalf("SetSystemPrompt: %v", err)
	}

	back, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got, ok := back.Get(id)
	if !ok {
		t.Fatal("row missing after reload")
	}
	if got.SystemPrompt != nil {
		t.Errorf("on-disk SystemPrompt = %v, want nil (SetSystemPrompt must not Save)", got.SystemPrompt)
	}
}

// #2149 AC5: a refused value reaches no file — not the pre-existing registry,
// not the one written after the refusal.
func TestRegistry_SetSystemPrompt_RefusedValueNeverReachesDisk(t *testing.T) {
	t.Parallel()
	const id ConversationID = "11111111-2222-4333-8444-555555555555"
	const marker = "MARKER_K3M9P2X7"
	r := &Registry{}
	r.Create(Conversation{ID: id, Cwd: "/x", LastUsedAt: mustParseTime(t, "2026-09-04T12:34:56.789Z")})

	dir := t.TempDir()
	before := filepath.Join(dir, "before.json")
	if err := r.Save(before); err != nil {
		t.Fatalf("Save before: %v", err)
	}

	p := marker + strings.Repeat("a", MaxSystemPromptBytes+1-len(marker))
	if err := r.SetSystemPrompt(id, &p); !errors.Is(err, ErrSystemPromptTooLong) {
		t.Fatalf("SetSystemPrompt err = %v, want ErrSystemPromptTooLong", err)
	}

	after := filepath.Join(dir, "after.json")
	if err := r.Save(after); err != nil {
		t.Fatalf("Save after: %v", err)
	}
	for _, path := range []string{before, after} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if strings.Contains(string(data), marker) {
			t.Errorf("%s contains a fragment of the refused value", filepath.Base(path))
		}
	}
}

// #2206 AC1: a stored label reads back through the read accessor; a label that
// was set and then cleared reads as absent. Keys compare as bytes, so paths that
// differ only in a trailing separator, a trailing space, or case are distinct
// workspaces.
func TestRegistry_WorkspaceLabel_SetReadClear(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		setup     func(*Registry)
		key       string
		wantLabel string
		wantOK    bool
	}{
		{
			name:  "unset key reads absent",
			setup: func(*Registry) {},
			key:   "/home/user/project",
		},
		{
			name:      "set then read returns the label",
			setup:     func(r *Registry) { r.SetWorkspaceLabel("/home/user/project", ptrTo("Tax filing")) },
			key:       "/home/user/project",
			wantLabel: "Tax filing",
			wantOK:    true,
		},
		{
			name: "a second set replaces rather than accumulates",
			setup: func(r *Registry) {
				r.SetWorkspaceLabel("/w", ptrTo("first"))
				r.SetWorkspaceLabel("/w", ptrTo("second"))
			},
			key:       "/w",
			wantLabel: "second",
			wantOK:    true,
		},
		{
			name: "clear removes the key, so the read is absent and not empty",
			setup: func(r *Registry) {
				r.SetWorkspaceLabel("/w", ptrTo("gone"))
				r.SetWorkspaceLabel("/w", nil)
			},
			key: "/w",
		},
		{
			name:  "clearing a key that was never set is a no-op",
			setup: func(r *Registry) { r.SetWorkspaceLabel("/never", nil) },
			key:   "/never",
		},
		{
			name:      "an explicitly empty label is present, distinguishable from cleared",
			setup:     func(r *Registry) { r.SetWorkspaceLabel("/w", ptrTo("")) },
			key:       "/w",
			wantLabel: "",
			wantOK:    true,
		},
		{
			name:  "byte-exact keys: a trailing separator is a different workspace",
			setup: func(r *Registry) { r.SetWorkspaceLabel("/a", ptrTo("plain")) },
			key:   "/a/",
		},
		{
			name:  "byte-exact keys: case is significant",
			setup: func(r *Registry) { r.SetWorkspaceLabel("/a", ptrTo("plain")) },
			key:   "/A",
		},
		{
			name:  "byte-exact keys: a trailing space is a different workspace",
			setup: func(r *Registry) { r.SetWorkspaceLabel("/a", ptrTo("plain")) },
			key:   "/a ",
		},
		{
			name: "clearing one key leaves its byte-neighbours alone",
			setup: func(r *Registry) {
				r.SetWorkspaceLabel("/a", ptrTo("kept"))
				r.SetWorkspaceLabel("/a/", ptrTo("cleared"))
				r.SetWorkspaceLabel("/a/", nil)
			},
			key:       "/a",
			wantLabel: "kept",
			wantOK:    true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := &Registry{}
			tt.setup(r)
			got, ok := r.WorkspaceLabel(tt.key)
			if ok != tt.wantOK {
				t.Fatalf("WorkspaceLabel(%q) ok = %v, want %v", tt.key, ok, tt.wantOK)
			}
			if got != tt.wantLabel {
				t.Errorf("WorkspaceLabel(%q) = %q, want %q", tt.key, got, tt.wantLabel)
			}
		})
	}
}

// #2206 AC2: a label write changes no other registry state — the conversation
// list is byte-identical across a set and a clear.
func TestRegistry_SetWorkspaceLabel_LeavesConversationsUntouched(t *testing.T) {
	t.Parallel()
	when := mustParseTime(t, "2026-09-07T12:34:56.789Z")
	r := &Registry{}
	r.Create(Conversation{
		ID:               "11111111-2222-4333-8444-555555555555",
		Name:             strPtr("general"),
		Cwd:              "/home/user/project",
		CurrentSessionID: "sess-current",
		SessionHistory:   []string{"sess-old"},
		IsPromoted:       true,
		IsArchived:       true,
		SystemPrompt:     strPtr("be terse"),
		LastUsedAt:       when,
	})
	before := r.List()

	r.SetWorkspaceLabel("/home/user/project", ptrTo("Tax filing"))
	r.SetWorkspaceLabel("/elsewhere", ptrTo("Other"))
	r.SetWorkspaceLabel("/elsewhere", nil)

	if after := r.List(); !reflect.DeepEqual(before, after) {
		t.Errorf("conversation list changed across label writes:\n before = %+v\n after  = %+v", before, after)
	}
}

// #2206 AC2: setting a label, saving, and loading into a fresh registry returns
// the same label, through the existing snapshot-encode-fsync-rename save path.
func TestRegistry_WorkspaceLabel_RoundTrip(t *testing.T) {
	t.Parallel()
	when := mustParseTime(t, "2026-09-07T12:34:56.789Z")
	r := &Registry{}
	r.Create(Conversation{ID: "11111111-2222-4333-8444-555555555555", Cwd: "/home/user/project", LastUsedAt: when})

	labels := map[string]string{
		"/home/user/project": "Tax filing",
		"/home/user/tmp2":    "Scratch — ä ö å 🐍\nsecond line",
		"/home/user/blank":   "",
	}
	for k, v := range labels {
		r.SetWorkspaceLabel(k, ptrTo(v))
	}

	path := filepath.Join(t.TempDir(), "conversations.json")
	if err := r.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	back, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for k, want := range labels {
		got, ok := back.WorkspaceLabel(k)
		if !ok {
			t.Errorf("WorkspaceLabel(%q) after reload: absent, want %q", k, want)
			continue
		}
		if got != want {
			t.Errorf("WorkspaceLabel(%q) after reload = %q, want %q", k, got, want)
		}
	}
	if got, ok := back.WorkspaceLabel("/never-set"); ok {
		t.Errorf("an unset key reads present after reload, as %q", got)
	}
	if got := back.List(); len(got) != 1 || got[0].Cwd != "/home/user/project" {
		t.Errorf("conversation list after reload = %+v, want the one seeded row", got)
	}
}

// #2206 AC2/AC3: encoding/json sorts map keys, so the label map needs no
// counterpart to the conversation slice's sort discipline — the same labels
// inserted in opposite orders produce byte-identical files.
func TestRegistry_Save_LabelOrderIsInsertionIndependent(t *testing.T) {
	t.Parallel()
	labels := map[string]string{"/z-last": "zed", "/a-first": "alpha", "/m-middle": "mid"}
	write := func(order []string) []byte {
		t.Helper()
		r := &Registry{}
		for _, k := range order {
			r.SetWorkspaceLabel(k, ptrTo(labels[k]))
		}
		path := filepath.Join(t.TempDir(), "conversations.json")
		if err := r.Save(path); err != nil {
			t.Fatalf("Save: %v", err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read after save: %v", err)
		}
		return data
	}
	forward := write([]string{"/a-first", "/m-middle", "/z-last"})
	reverse := write([]string{"/z-last", "/m-middle", "/a-first"})
	if string(forward) != string(reverse) {
		t.Errorf("label insertion order changed the file bytes:\nforward = %s\nreverse = %s", forward, reverse)
	}
}

// #2206: SetWorkspaceLabel does not persist implicitly — the Create / Update /
// Promote / Delete / RebindSession / SetArchived / SetSystemPrompt convention.
func TestRegistry_SetWorkspaceLabel_DoesNotPersist(t *testing.T) {
	t.Parallel()
	r := &Registry{}
	r.Create(Conversation{ID: "11111111-2222-4333-8444-555555555555", Cwd: "/w", LastUsedAt: mustParseTime(t, "2026-09-07T12:34:56.789Z")})

	path := filepath.Join(t.TempDir(), "conversations.json")
	if err := r.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	r.SetWorkspaceLabel("/w", ptrTo("Tax filing"))

	back, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, ok := back.WorkspaceLabel("/w"); ok {
		t.Errorf("on-disk label = %q, want absent (SetWorkspaceLabel must not Save)", got)
	}
}

// #2206 AC3: a registry with no label set saves with no workspace_labels key at
// all, so its bytes are identical to their pre-#2206 form and Save→Load→Save is a
// fixed point.
//
// Two label-free states are checked because they are reached differently, and
// only the second can catch a key emitted unconditionally as null:
// TestRegistry_Save_NoPromptOmitsKey and TestRegistry_Save_ActiveOmitsArchivedKey
// both build registries that never held a label, so a map allocated and then
// emptied is a state neither can reach — an unconditionally written key would
// still round-trip as a fixed point there while no longer matching a pre-ticket
// file.
func TestRegistry_Save_NoLabelsOmitsKey(t *testing.T) {
	t.Parallel()
	when := mustParseTime(t, "2026-09-07T12:34:56.789Z")
	seed := func() *Registry {
		r := &Registry{}
		r.Create(Conversation{ID: "11111111-2222-4333-8444-555555555555", Cwd: "/a", LastUsedAt: when})
		r.Create(Conversation{ID: "22222222-2222-4333-8444-555555555555", Cwd: "/b", LastUsedAt: when.Add(time.Second)})
		return r
	}
	// saveTwice asserts the omitted key and the Save→Load→Save fixed point, and
	// returns the first save's bytes for cross-state comparison.
	saveTwice := func(r *Registry) []byte {
		t.Helper()
		path := filepath.Join(t.TempDir(), "conversations.json")
		if err := r.Save(path); err != nil {
			t.Fatalf("Save: %v", err)
		}
		first, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read after save: %v", err)
		}
		if strings.Contains(string(first), "workspace_labels") {
			t.Errorf("label-free registry serialized a workspace_labels key:\n%s", first)
		}
		back, err := Load(path)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		path2 := filepath.Join(t.TempDir(), "conversations.json")
		if err := back.Save(path2); err != nil {
			t.Fatalf("re-Save: %v", err)
		}
		second, err := os.ReadFile(path2)
		if err != nil {
			t.Fatalf("read after re-save: %v", err)
		}
		if string(first) != string(second) {
			t.Errorf("Save→Load→Save not byte-identical:\n first = %s\nsecond = %s", first, second)
		}
		return first
	}

	neverSet := saveTwice(seed())

	cleared := seed()
	cleared.SetWorkspaceLabel("/a", ptrTo("gone"))
	cleared.SetWorkspaceLabel("/b", ptrTo("also gone"))
	cleared.SetWorkspaceLabel("/a", nil)
	cleared.SetWorkspaceLabel("/b", nil)

	if got := saveTwice(cleared); string(got) != string(neverSet) {
		t.Errorf("a set-then-cleared registry is not byte-identical to one that never held a label:\n cleared   = %s\n never-set = %s", got, neverSet)
	}
}

// #2206 AC3: a registry file written before this ticket — one carrying no
// workspace_labels key at all — loads with an empty label map and no error, with
// no migration step. The freshly loaded map must also be writable: assignment to
// a nil map panics, so the first set after such a Load is the lazy-allocation
// path.
func TestRegistry_Load_AbsentLabelKeyDecodesEmpty(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "conversations.json")
	raw := `{"conversations":[{"id":"11111111-2222-4333-8444-555555555555","cwd":"/legacy","is_promoted":false,"last_used_at":"2026-09-07T12:34:56.789Z"}]}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	r, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := r.Get("11111111-2222-4333-8444-555555555555"); !ok {
		t.Fatal("legacy row missing after Load")
	}
	if got, ok := r.WorkspaceLabel("/legacy"); ok {
		t.Errorf("absent workspace_labels key decoded as %q for the row's own cwd, want absent", got)
	}

	r.SetWorkspaceLabel("/legacy", ptrTo("Legacy"))
	if got, ok := r.WorkspaceLabel("/legacy"); !ok || got != "Legacy" {
		t.Errorf("first set on a freshly loaded legacy registry read back as (%q, %v), want (\"Legacy\", true)", got, ok)
	}
}

// #2206 AC4: reads and writes of the label map are serialised by the registry's
// existing mutex, Save's encode included.
//
// The encode is the load-bearing arm. Encoding the live map while a writer
// mutates it is a fatal "concurrent map iteration and map write" runtime throw
// that kills the process rather than a race the detector reports, so only a test
// that runs Save against concurrent label writers can reach it.
func TestRegistry_WorkspaceLabel_ConcurrentAccess(t *testing.T) {
	t.Parallel()
	when := mustParseTime(t, "2026-09-07T12:34:56.789Z")
	r := &Registry{}
	r.Create(Conversation{ID: "11111111-2222-4333-8444-555555555555", Cwd: "/w0", LastUsedAt: when})

	dir := t.TempDir()
	var wg sync.WaitGroup
	start := make(chan struct{})
	worker := func(n int, fn func(i int)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < n; i++ {
				fn(i)
			}
		}()
	}

	const mutations = 200
	worker(mutations, func(i int) {
		r.SetWorkspaceLabel(fmt.Sprintf("/w%d", i%8), ptrTo(fmt.Sprintf("label-%d", i)))
	})
	worker(mutations, func(i int) { r.SetWorkspaceLabel(fmt.Sprintf("/w%d", i%8), nil) })
	worker(mutations, func(i int) { r.WorkspaceLabel(fmt.Sprintf("/w%d", i%8)) })
	worker(mutations, func(i int) {
		r.Create(Conversation{ID: ConversationID(fmt.Sprintf("c%d", i)), Cwd: "/w0", LastUsedAt: when})
	})
	// Fewer iterations than the mutators: each Save fsyncs, and the arm only
	// needs to overlap the mutation window, not match its length.
	worker(40, func(i int) {
		if err := r.Save(filepath.Join(dir, fmt.Sprintf("conversations-%d.json", i%4))); err != nil {
			t.Errorf("Save: %v", err)
		}
	})

	close(start)
	wg.Wait()
}

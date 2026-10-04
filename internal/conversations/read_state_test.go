package conversations

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestRegistryReadUpTo(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "conversations.json")
	const id ConversationID = "11111111-1111-4111-8111-111111111111"
	raw := `{"conversations":[{"id":"11111111-1111-4111-8111-111111111111","cwd":"/legacy","is_promoted":true,"is_muted":true,"last_used_at":"2026-05-09T12:34:56.789Z"}]}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	before, ok := r.Get(id)
	if !ok || before.ReadUpTo != 0 {
		t.Fatalf("legacy = %+v", before)
	}
	for _, mark := range []uint64{0, 1<<63 + 9} {
		r.Update(id, func(c *Conversation) { c.ReadUpTo = mark })
		name := "renamed"
		r.Update(id, func(c *Conversation) { c.Name = &name })
		if err := r.Save(path); err != nil {
			t.Fatal(err)
		}
		r, err = Load(path)
		if err != nil {
			t.Fatal(err)
		}
		got, ok := r.Get(id)
		if !ok || got.ReadUpTo != mark || got.Cwd != before.Cwd || !got.IsMuted || !got.IsPromoted || !got.LastUsedAt.Equal(before.LastUsedAt) || got.Name == nil || *got.Name != name {
			t.Fatalf("reloaded = %+v, mark %d", got, mark)
		}
	}
}

func TestRegistryAdvanceReadUpTo(t *testing.T) {
	t.Parallel()
	const id ConversationID = "22222222-2222-4222-8222-222222222222"
	const maxMark = ^uint64(0)
	cases := []struct {
		name               string
		held, upTo, latest uint64
		want               uint64
		advanced           bool
	}{
		{"empty conversation stays at zero", 0, 0, 0, 0, false},
		{"over-large clamps on empty", 0, 9, 0, 0, false},
		{"advance", 3, 7, 10, 7, true},
		{"advance to latest", 3, 10, 10, 10, true},
		{"over-large clamps to latest", 3, 99, 10, 10, true},
		{"equal is no-op", 7, 7, 10, 7, false},
		{"lower is no-op", 7, 2, 10, 7, false},
		{"held past latest is kept", 12, 11, 10, 12, false},
		{"max range", 1, maxMark, maxMark, maxMark, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "conversations.json")
			r := &Registry{}
			r.Create(Conversation{ID: id, Cwd: "/w", ReadUpTo: tc.held, IsMuted: true})
			got, advanced, err := r.AdvanceReadUpTo(id, tc.upTo, tc.latest, path)
			if err != nil {
				t.Fatal(err)
			}
			if advanced != tc.advanced || got.ReadUpTo != tc.want || !got.IsMuted || got.Cwd != "/w" {
				t.Fatalf("got %+v advanced=%v, want mark %d advanced=%v", got, advanced, tc.want, tc.advanced)
			}
			_, statErr := os.Stat(path)
			if tc.advanced != (statErr == nil) {
				t.Fatalf("saved = %v, want %v", statErr == nil, tc.advanced)
			}
			if !tc.advanced {
				return
			}
			loaded, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if c, ok := loaded.Get(id); !ok || c.ReadUpTo != tc.want {
				t.Fatalf("reloaded = %+v", c)
			}
		})
	}
}

func TestRegistryAdvanceReadUpToMiss(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "conversations.json")
	r := &Registry{}
	r.Create(Conversation{ID: "a", Cwd: "/w"})
	for _, id := range []ConversationID{"", "b"} {
		if _, advanced, err := r.AdvanceReadUpTo(id, 1, 5, path); !errors.Is(err, ErrConversationNotFound) || advanced {
			t.Fatalf("%q: advanced=%v err=%v", id, advanced, err)
		}
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("miss saved the registry: %v", err)
	}
}

func TestRegistryAdvanceReadUpToSaveFailureReverts(t *testing.T) {
	t.Parallel()
	const id ConversationID = "c"
	r := &Registry{}
	r.Create(Conversation{ID: id, Cwd: "/w", ReadUpTo: 2})
	boom := errors.New("disk full")
	var saved []uint64
	fail := func(string, registryFile) error { return boom }
	keep := func(_ string, rf registryFile) error {
		saved = append(saved, rf.Conversations[0].ReadUpTo)
		return nil
	}
	if _, advanced, err := r.advanceReadUpTo(id, 5, 9, "p", fail); !errors.Is(err, boom) || advanced {
		t.Fatalf("advanced=%v err=%v", advanced, err)
	}
	if c, _ := r.Get(id); c.ReadUpTo != 2 {
		t.Fatalf("held after failed save = %d, want 2", c.ReadUpTo)
	}
	got, advanced, err := r.advanceReadUpTo(id, 5, 9, "p", keep)
	if err != nil || !advanced || got.ReadUpTo != 5 || len(saved) != 1 || saved[0] != 5 {
		t.Fatalf("retry = %+v advanced=%v err=%v saved=%v", got, advanced, err, saved)
	}
}

func TestRegistryAdvanceReadUpToConcurrent(t *testing.T) {
	t.Parallel()
	const id ConversationID = "33333333-3333-4333-8333-333333333333"
	path := filepath.Join(t.TempDir(), "conversations.json")
	r := &Registry{}
	r.Create(Conversation{ID: id, Cwd: "/w"})
	const n = 32
	var wg sync.WaitGroup
	for i := n; i >= 1; i-- {
		wg.Add(1)
		go func(mark uint64) {
			defer wg.Done()
			if _, _, err := r.AdvanceReadUpTo(id, mark, n, path); err != nil {
				t.Error(err)
			}
			// An unrelated Save racing the advances must not write an older mark.
			if err := r.Save(path); err != nil {
				t.Error(err)
			}
		}(uint64(i))
	}
	wg.Wait()
	if c, _ := r.Get(id); c.ReadUpTo != n {
		t.Fatalf("held = %d, want %d", c.ReadUpTo, n)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := loaded.Get(id); c.ReadUpTo != n {
		t.Fatalf("saved = %d, want %d", c.ReadUpTo, n)
	}
}

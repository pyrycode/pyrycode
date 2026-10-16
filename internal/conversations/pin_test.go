package conversations

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestRegistry_SetPinned_Transitions(t *testing.T) {
	t.Parallel()
	first := time.Date(2026, 10, 17, 12, 34, 56, 123456789, time.FixedZone("offset", 7200))
	later := first.Add(time.Hour)
	for _, archived := range []bool{false, true} {
		t.Run(map[bool]string{false: "active", true: "archived"}[archived], func(t *testing.T) {
			r := &Registry{}
			name, prompt := "channel", "instructions"
			seed := Conversation{
				ID: "target", Name: &name, Cwd: "/workspace", CurrentSessionID: "current",
				SessionHistory: []string{"previous"}, IsPromoted: true, IsArchived: archived,
				ArchivedAt: &first, IsMuted: true, ReadUpTo: 42, SystemPrompt: &prompt,
				LastContextUsage:    &ContextUsageReading{Model: "model", TotalTokens: 12, AsOf: first},
				PendingChannelPosts: []string{"pending"}, LastUsedAt: first,
			}
			r.Create(seed)
			r.Create(Conversation{ID: "other", IsPromoted: true})
			for _, step := range []struct {
				name   string
				pinned bool
				now    time.Time
				want   *time.Time
			}{
				{"pin", true, first, &first},
				{"repeat pin", true, later, &first},
				{"unpin", false, later, nil},
				{"repeat unpin", false, later, nil},
				{"repin", true, later, &later},
			} {
				t.Run(step.name, func(t *testing.T) {
					before, _ := r.Get(seed.ID)
					if err := r.SetPinned(seed.ID, step.pinned, step.now); err != nil {
						t.Fatal(err)
					}
					got, _ := r.Get(seed.ID)
					if step.want == nil {
						if got.PinnedAt != nil {
							t.Fatalf("PinnedAt = %v, want nil", got.PinnedAt)
						}
					} else if got.PinnedAt == nil || !got.PinnedAt.Equal(*step.want) || got.PinnedAt.Location() != time.UTC {
						t.Fatalf("PinnedAt = %v, want %v in UTC", got.PinnedAt, step.want)
					}
					// A previously read timestamp stays valid after clear or repin.
					if before.PinnedAt != nil && !before.PinnedAt.Equal(first) {
						t.Fatalf("previous snapshot changed: %v", before.PinnedAt)
					}
					got.PinnedAt = nil
					if !reflect.DeepEqual(got, seed) {
						t.Fatalf("pin changed unrelated metadata: got %+v, want %+v", got, seed)
					}
					other, _ := r.Get("other")
					if !reflect.DeepEqual(other, Conversation{ID: "other", IsPromoted: true}) {
						t.Fatalf("other row changed: %+v", other)
					}
				})
			}
		})
	}
}

func TestRegistry_SetPinned_Eligibility(t *testing.T) {
	t.Parallel()
	now := time.Now()
	for _, tc := range []struct {
		name     string
		id       ConversationID
		pinned   bool
		archived bool
		held     *time.Time
		wantErr  error
	}{
		{"unknown pin", "missing", true, false, nil, ErrConversationNotFound},
		{"unknown unpin", "missing", false, false, nil, ErrConversationNotFound},
		{"unpromoted pin", "discussion", true, false, nil, ErrConversationNotPromoted},
		{"unpromoted archived pin", "discussion", true, true, &now, ErrConversationNotPromoted},
		{"unpromoted clear", "discussion", false, false, &now, nil},
		{"unpromoted archived clear", "discussion", false, true, &now, nil},
		{"unpromoted already clear", "discussion", false, false, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &Registry{}
			r.Create(Conversation{ID: "discussion", IsArchived: tc.archived, PinnedAt: tc.held, LastUsedAt: now})
			r.Create(Conversation{ID: "channel", IsPromoted: true, PinnedAt: &now})
			before := r.List()
			err := r.SetPinned(tc.id, tc.pinned, now)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("SetPinned = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr != nil && errors.Is(err, ErrConversationNotFound) == errors.Is(err, ErrConversationNotPromoted) {
				t.Fatalf("refusals are not distinguishable: %v", err)
			}
			if tc.wantErr == nil {
				before[0].PinnedAt = nil
			}
			if !reflect.DeepEqual(r.List(), before) {
				t.Fatal("unexpected row mutation")
			}
		})
	}
}

func TestRegistry_Pinned_RoundTrip(t *testing.T) {
	t.Parallel()
	r, err := Load("testdata/conversations.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.List()) != 2 {
		t.Fatal("legacy fixture must contain two conversations")
	}
	for _, c := range r.List() {
		if c.PinnedAt != nil {
			t.Fatalf("legacy row %s loaded pinned", c.ID)
		}
	}
	id := r.List()[0].ID
	path := filepath.Join(t.TempDir(), "conversations.json")
	when := time.Date(2026, 10, 17, 12, 34, 56, 987654321, time.FixedZone("offset", -18000))
	for _, pinned := range []bool{false, true, false} {
		if err := r.SetPinned(id, pinned, when); err != nil {
			t.Fatal(err)
		}
		if err := r.Save(path); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte(`"pinned_at"`)) != pinned || bytes.Contains(data, []byte(`"pinned_at": null`)) {
			t.Fatalf("incorrect pin key presence: %s", data)
		}
		r, err = Load(path)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := r.Get(id)
		if pinned {
			if got.PinnedAt == nil || !got.PinnedAt.Equal(when) {
				t.Fatalf("reloaded PinnedAt = %v, want %v", got.PinnedAt, when)
			}
		} else if got.PinnedAt != nil {
			t.Fatalf("reloaded PinnedAt = %v, want nil", got.PinnedAt)
		}
		if err := r.SetPinned(id, !pinned, when); err != nil {
			t.Fatal(err)
		}
		unchanged, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(data, unchanged) {
			t.Fatalf("mutation persisted without Save: %v", err)
		}
	}
}

func TestRegistry_SetPinned_Concurrent(t *testing.T) {
	t.Parallel()
	r := &Registry{}
	r.Create(Conversation{ID: "channel", IsPromoted: true})
	now := time.Now()
	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				err := r.SetPinned("channel", i%2 == 0, now)
				if err != nil && !errors.Is(err, ErrConversationNotPromoted) {
					t.Errorf("SetPinned: %v", err)
				}
				r.Update("channel", func(c *Conversation) { c.IsPromoted = i%2 == 0 })
				r.mu.Lock()
				snapshot := r.snapshotLocked()
				r.mu.Unlock()
				if _, err := json.Marshal(snapshot); err != nil {
					t.Errorf("marshal snapshot: %v", err)
				}
				c, _ := r.Get("channel")
				if c.PinnedAt != nil && !c.PinnedAt.Equal(now) {
					t.Errorf("invalid pin time: %v", c.PinnedAt)
				}
			}
		}()
	}
	wg.Wait()
}

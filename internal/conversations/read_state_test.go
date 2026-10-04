package conversations

import (
	"os"
	"path/filepath"
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

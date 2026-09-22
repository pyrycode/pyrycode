package main

import (
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/eventring"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// TestDropRingOnConversationDelete proves #1502's production wiring: once
// dropRingOnConversationDelete joins a registry to the emitter's ring, both
// removal paths — a direct Registry.Delete (what delete_conversation calls) and
// an idle Sweep — free the removed conversation's ring entry, and every
// conversation still in the registry keeps its events.
func TestDropRingOnConversationDelete(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	longAgo := now.Add(-365 * 24 * time.Hour)
	const (
		deleted = "00000001-2222-4333-8444-555555555555"
		swept   = "00000002-2222-4333-8444-555555555555"
		kept    = "00000003-2222-4333-8444-555555555555"
	)

	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{ID: deleted, Cwd: "/deleted", LastUsedAt: now})
	reg.Create(conversations.Conversation{ID: swept, Cwd: "/swept", LastUsedAt: longAgo})
	reg.Create(conversations.Conversation{ID: kept, Cwd: "/kept", LastUsedAt: now})

	ring := eventring.New(eventring.MaxEventsPerConversation)
	for _, id := range []string{deleted, swept, kept} {
		ring.Append(id, protocol.TypeTurnState, nil, now)
	}
	keptNewest := ring.NewestID(kept)

	dropRingOnConversationDelete(reg, ring)

	if !reg.Delete(deleted) {
		t.Fatal("Delete(deleted) = false, want true")
	}
	if got := ring.NewestID(deleted); got != 0 {
		t.Errorf("after Delete: NewestID(deleted) = %d, want 0", got)
	}
	if got := ring.NewestID(swept); got == 0 {
		t.Error("Delete of one conversation dropped another's ring entry")
	}

	if n := conversations.Sweep(reg, now); n != 1 {
		t.Fatalf("Sweep = %d, want 1", n)
	}
	if got := ring.NewestID(swept); got != 0 {
		t.Errorf("after Sweep: NewestID(swept) = %d, want 0", got)
	}
	if got := ring.NewestID(kept); got != keptNewest {
		t.Errorf("NewestID(kept) = %d, want %d (a kept conversation keeps its events)", got, keptNewest)
	}
}

// TestDropRingOnConversationDelete_NilRegistry covers the posture where the
// relay leg has no conversations registry: wiring is a no-op, not a panic.
func TestDropRingOnConversationDelete_NilRegistry(t *testing.T) {
	t.Parallel()
	dropRingOnConversationDelete(nil, eventring.New(1))
}

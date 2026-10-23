package main

import (
	"slices"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
)

type testRemovalOwner struct {
	removed []conversations.ConversationID
}

func (o *testRemovalOwner) remove(id conversations.ConversationID) {
	o.removed = append(o.removed, id)
}

func TestConversationDeleteCleanup(t *testing.T) {
	t.Parallel()
	now := time.Now()
	const deleted = "00000001-2222-4333-8444-555555555555"
	const swept = "00000002-2222-4333-8444-555555555555"
	const kept = "00000003-2222-4333-8444-555555555555"
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{ID: deleted, LastUsedAt: now})
	reg.Create(conversations.Conversation{ID: swept, LastUsedAt: now.Add(-365 * 24 * time.Hour)})
	reg.Create(conversations.Conversation{ID: kept, LastUsedAt: now})
	a, b := &testRemovalOwner{}, &testRemovalOwner{}
	cleanupOnConversationDelete(reg, nil, a, nil, b)
	if !reg.Delete(deleted) {
		t.Fatal("direct deletion missed")
	}
	for _, owner := range []*testRemovalOwner{a, b} {
		if !slices.Equal(owner.removed, []conversations.ConversationID{deleted}) {
			t.Fatal("direct deletion did not notify only the removed owner", owner.removed)
		}
	}
	if reg.Delete(deleted) || conversations.Sweep(reg, now) != 1 {
		t.Fatal("repeated deletion or sweep changed")
	}
	for _, owner := range []*testRemovalOwner{a, b} {
		if !slices.Equal(owner.removed, []conversations.ConversationID{deleted, swept}) {
			t.Fatal("sweep did not compose every owner", owner.removed)
		}
	}
	if _, ok := reg.Get(kept); !ok {
		t.Fatal("another conversation was removed")
	}
}

func TestConversationDeleteCleanup_NilRegistry(t *testing.T) {
	t.Parallel()
	owner := &testRemovalOwner{}
	cleanupOnConversationDelete(nil, nil, owner)
	if len(owner.removed) != 0 {
		t.Fatal("nil registry notified an owner")
	}
}

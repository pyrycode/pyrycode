package main

import (
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
)

// TestCodexConversation_FromRealPool drives the relay's CodexConversation seam
// (#2644) over a real pool holding a live Claude bootstrap and a dormant Codex
// session. Only the conversation bound to the Codex session reads as Codex;
// every case the list reads as Claude (#2643) reads false.
func TestCodexConversation_FromRealPool(t *testing.T) {
	t.Parallel()
	pool, _ := newDormantWritePool(t, `"harness":"codex",`)

	reg := &conversations.Registry{}
	ts := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	reg.Create(conversations.Conversation{ID: "conv-claude", Cwd: "/w", CurrentSessionID: dormantWriteBootID, LastUsedAt: ts})
	reg.Create(conversations.Conversation{ID: "conv-codex", Cwd: "/w", CurrentSessionID: dormantWriteTargetID, LastUsedAt: ts})
	reg.Create(conversations.Conversation{ID: "conv-unbound", Cwd: "/w", LastUsedAt: ts})
	reg.Create(conversations.Conversation{ID: "conv-unheld", Cwd: "/w", CurrentSessionID: "not-in-pool", LastUsedAt: ts})

	isCodex := codexConversation(reg, sessionHarness(pool))
	if isCodex == nil {
		t.Fatal("codexConversation returned nil for a wired registry and harness")
	}
	for id, want := range map[string]bool{
		"conv-claude":  false,
		"conv-codex":   true,
		"conv-unbound": false,
		"conv-unheld":  false,
		"conv-unknown": false,
	} {
		if got := isCodex(id); got != want {
			t.Errorf("isCodex(%q) = %v, want %v", id, got, want)
		}
	}

	if codexConversation(nil, sessionHarness(pool)) != nil {
		t.Error("nil registry: want a nil seam")
	}
	if codexConversation(reg, nil) != nil {
		t.Error("nil harness: want a nil seam")
	}
}

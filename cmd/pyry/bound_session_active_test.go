package main

import (
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
)

// TestBoundSessionIDForActive exercises the #1081 stream-drain scoping resolver:
// it maps the ACTIVE conversation to its bound pool session id, and is fail-closed
// on every ambiguous state (no active conversation, unknown conversation, empty
// binding) so the drain drops rather than mis-attributes. The load-bearing case is
// empty CurrentSessionID: it must return ("", false), NOT fall through to a
// bootstrap default (the #678 isolation guard resolveBoundSession also enforces).
func TestBoundSessionIDForActive(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{ID: "conv-bound", CurrentSessionID: "sess-123", LastUsedAt: now})
	reg.Create(conversations.Conversation{ID: "conv-unbound", CurrentSessionID: "", LastUsedAt: now})

	t.Run("no active conversation is fail-closed", func(t *testing.T) {
		t.Parallel()
		active := &activeConversation{} // never routed → empty cursor
		id, ok := boundSessionIDForActive(active, reg)
		if ok || id != "" {
			t.Errorf("boundSessionIDForActive(no active) = (%q, %v), want (\"\", false)", id, ok)
		}
	})

	t.Run("unknown active conversation is fail-closed", func(t *testing.T) {
		t.Parallel()
		active := &activeConversation{}
		active.set("conv-does-not-exist")
		id, ok := boundSessionIDForActive(active, reg)
		if ok || id != "" {
			t.Errorf("boundSessionIDForActive(unknown) = (%q, %v), want (\"\", false)", id, ok)
		}
	})

	t.Run("empty CurrentSessionID is fail-closed, never a bootstrap default", func(t *testing.T) {
		t.Parallel()
		active := &activeConversation{}
		active.set("conv-unbound")
		id, ok := boundSessionIDForActive(active, reg)
		if ok {
			t.Errorf("boundSessionIDForActive(unbound) ok = true, want false")
		}
		if id != "" {
			t.Errorf("boundSessionIDForActive(unbound) id = %q, want \"\" — an unbound active conversation must not resolve to any session", id)
		}
	})

	t.Run("bound active conversation resolves to its session id", func(t *testing.T) {
		t.Parallel()
		active := &activeConversation{}
		active.set("conv-bound")
		id, ok := boundSessionIDForActive(active, reg)
		if !ok {
			t.Fatalf("boundSessionIDForActive(bound) ok = false, want true")
		}
		if id != "sess-123" {
			t.Errorf("boundSessionIDForActive(bound) id = %q, want %q", id, "sess-123")
		}
	})
}

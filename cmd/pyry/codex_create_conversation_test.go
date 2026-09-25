package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// TestSessionMinter_CodexConversation (#2647): the production create path mints
// a Codex session when asked for one. Its first message runs on the fake Codex
// with no model or effort of its own, and the session's agent survives a daemon
// restart as its dormant entry's harness.
func TestSessionMinter_CodexConversation(t *testing.T) {
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	codexHome := filepath.Join(dir, "codex-home")

	sink := newStreamTurnSink(0, nil)
	a := startCodexPool(t, regPath, codexHome, sink)
	sessionID, _, err := sessionMinter{a.pool}.Create(context.Background(), "conv-1", "", protocol.AgentCodex)
	if err != nil {
		t.Fatalf("Create(codex): %v", err)
	}
	id := sessions.SessionID(sessionID)

	// The conversation's first message: the drain activates the bound session,
	// which the factory builds as a Codex runner.
	if err := a.pool.Activate(a.ctx, id); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	sess, err := a.pool.Lookup(id)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	r, ok := sess.Runner().(*codexRunner)
	if !ok {
		t.Fatalf("runner is %T, want *codexRunner", sess.Runner())
	}
	if r.cfg.Model != "" || r.cfg.Effort != "" {
		t.Errorf("codex runner model %q effort %q, want neither (Codex's own defaults)", r.cfg.Model, r.cfg.Effort)
	}
	h := &codexHarnessT{r: r}
	h.bound(t, nil)
	if err := r.WriteUserTurn(context.Background(), "c-1", []byte("hello")); err != nil {
		t.Fatalf("WriteUserTurn: %v", err)
	}
	awaitEnvelope(t, sink, id, "text", isText)
	if end := awaitEnvelope(t, sink, id, "turn end", isTurnEnd).(turnevent.TurnEnd); end.Reason != turnevent.TurnEndReasonEndTurn {
		t.Fatalf("TurnEnd.Reason = %q, want end_turn", end.Reason)
	}

	// Restart: a second daemon lifetime over the same registry.
	a.stop()
	b := startCodexPool(t, regPath, codexHome, newStreamTurnSink(0, nil))
	if got, err := b.pool.HarnessFor(id); err != nil || got != harnessCodex {
		t.Errorf("after restart HarnessFor = %q, %v; want %q", got, err, harnessCodex)
	}
}

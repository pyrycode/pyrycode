package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// codexPoolT is one daemon lifetime for the thread-registry test: a real pool
// whose claude sessions get stubRunner and whose codex sessions get the real
// Codex runner against the fake app-server, all feeding sink.
type codexPoolT struct {
	pool *sessions.Pool
	ctx  context.Context
	stop func()
}

func startCodexPool(t *testing.T, regPath, codexHome string, sink *streamTurnSink) *codexPoolT {
	t.Helper()
	claude := func(sessions.RunnerConfig) (sessions.Runner, error) { return stubRunner{}, nil }
	pool, err := sessions.New(sessions.Config{
		Bootstrap: sessions.SessionConfig{
			ClaudeBin:      os.Args[0],
			WorkDir:        t.TempDir(),
			BackoffInitial: 10 * time.Millisecond,
		},
		RegistryPath:  regPath,
		RunnerFactory: harnessRunnerFactory(claude, newCodexRunnerFactory(codexHarness{bin: fakeCodexBin(t), home: codexHome, sink: sink})),
	})
	if err != nil {
		t.Fatalf("sessions.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- pool.Run(ctx) }()
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("pool.Run did not exit within 15s after cancel")
		}
	}
	t.Cleanup(stop)
	select {
	case <-pool.Ready():
	case <-time.After(10 * time.Second):
		t.Fatal("pool.Run did not become ready within 10s")
	}
	return &codexPoolT{pool: pool, ctx: ctx, stop: stop}
}

// revive materialises id from its dormant entry, starts its runner, and waits
// for a bound client.
func (p *codexPoolT) revive(t *testing.T, id sessions.SessionID) *codexHarnessT {
	t.Helper()
	sess, err := p.pool.Revive(id, "conv-1", "")
	if err != nil {
		t.Fatalf("Revive(%s): %v", id, err)
	}
	if err := p.pool.Activate(p.ctx, id); err != nil {
		t.Fatalf("Activate(%s): %v", id, err)
	}
	r, ok := sess.Runner().(*codexRunner)
	if !ok {
		t.Fatalf("runner is %T, want *codexRunner", sess.Runner())
	}
	return &codexHarnessT{r: r}
}

// threadOnDisk returns id's thread_id in the registry file, and whether the
// entry exists at all.
func threadOnDisk(t *testing.T, path string, id sessions.SessionID) (string, bool) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read registry: %v", err)
	}
	var file struct {
		Sessions []struct {
			ID       string `json:"id"`
			ThreadID string `json:"thread_id"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("parse registry: %v\n%s", err, raw)
	}
	for _, e := range file.Sessions {
		if e.ID == string(id) {
			return e.ThreadID, true
		}
	}
	return "", false
}

func awaitThreadOnDisk(t *testing.T, path string, id sessions.SessionID, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		got, ok := threadOnDisk(t, path, id)
		if ok && got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("registry thread for %s = %q (present %v), want %q", id, got, ok, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// awaitEnvelope returns the first event the fan-in carries for sessionID that
// match accepts.
func awaitEnvelope(t *testing.T, sink *streamTurnSink, sessionID sessions.SessionID, what string, match func(turnevent.Event) bool) turnevent.Event {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case env := <-sink.ch:
			if !env.exit && env.sessionID == string(sessionID) && match(env.ev) {
				return env.ev
			}
		case <-timeout:
			t.Fatalf("no %s event for %s", what, sessionID)
			return nil
		}
	}
}

func isText(ev turnevent.Event) bool {
	c, ok := ev.(turnevent.TextChunk)
	return ok && c.Text != ""
}

// TestCodexSession_ThreadSurvivesRebuild drives a Codex session through a real
// pool against the fake Codex (#2622): a turn, an interrupt, an app-server kill
// that resumes the same thread, a fresh restart onto a new thread under a new
// pool id, and a rebuild from the registry in a second pool that resumes the
// stored thread rather than starting one.
func TestCodexSession_ThreadSurvivesRebuild(t *testing.T) {
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	codexHome := filepath.Join(dir, "codex-home")
	id, err := sessions.NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	when := time.Now().UTC().Format(time.RFC3339Nano)
	seed := `{"version":1,"sessions":[` +
		`{"id":"550e8400-e29b-41d4-a716-446655440000","label":"","created_at":"` + when + `","last_active_at":"` + when + `","bootstrap":true},` +
		`{"id":"` + string(id) + `","label":"conv-1","created_at":"` + when + `","last_active_at":"` + when + `","harness":"codex"}]}`
	if err := os.WriteFile(regPath, []byte(seed), 0o600); err != nil {
		t.Fatalf("seed registry: %v", err)
	}

	// --- daemon lifetime A ---
	sinkA := newStreamTurnSink(0, nil)
	a := startCodexPool(t, regPath, codexHome, sinkA)
	h := a.revive(t, id)
	client1, t1 := h.bound(t, nil)
	if t1 == "" {
		t.Fatal("no thread started")
	}
	awaitThreadOnDisk(t, regPath, id, t1)

	if err := h.r.WriteUserTurn(context.Background(), "c-1", []byte("hello")); err != nil {
		t.Fatalf("WriteUserTurn: %v", err)
	}
	awaitEnvelope(t, sinkA, id, "text", isText)
	if end := awaitEnvelope(t, sinkA, id, "turn end", isTurnEnd).(turnevent.TurnEnd); end.Reason != turnevent.TurnEndReasonEndTurn {
		t.Fatalf("TurnEnd.Reason = %q, want end_turn", end.Reason)
	}

	if err := h.r.WriteUserTurn(context.Background(), "c-1", []byte("[fakecodex:hold]")); err != nil {
		t.Fatalf("WriteUserTurn(hold): %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		h.r.mu.Lock()
		running := h.r.turnID != ""
		h.r.mu.Unlock()
		if running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("held turn never started")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := h.r.Interrupt(); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	if end := awaitEnvelope(t, sinkA, id, "turn end", isTurnEnd).(turnevent.TurnEnd); end.Reason != turnevent.TurnEndReasonCancelled {
		t.Fatalf("TurnEnd.Reason = %q, want cancelled", end.Reason)
	}

	// Kill the app-server: the respawn resumes the same thread.
	if err := stopCodexClient(client1); err != nil {
		t.Fatalf("stop app-server: %v", err)
	}
	client2, tid := h.bound(t, client1)
	if tid != t1 {
		t.Fatalf("thread after kill = %q, want resumed %q", tid, t1)
	}

	// Fresh restart: a new pool id and a new thread, recorded on the new entry.
	newID, err := a.pool.RotateForNewSession(id)
	if err != nil {
		t.Fatalf("RotateForNewSession: %v", err)
	}
	h.r.BeginRotation()
	h.r.RestartFresh(string(newID))
	_, t2 := h.bound(t, client2)
	if t2 == "" || t2 == t1 {
		t.Fatalf("thread after fresh restart = %q, want a new one (old %q)", t2, t1)
	}
	awaitThreadOnDisk(t, regPath, newID, t2)
	if _, ok := threadOnDisk(t, regPath, id); ok {
		t.Fatalf("old id %s still in the registry after the rotation", id)
	}
	if err := h.r.WriteUserTurn(context.Background(), "c-1", []byte("fresh")); err != nil {
		t.Fatalf("WriteUserTurn(fresh): %v", err)
	}
	awaitEnvelope(t, sinkA, newID, "turn end", isTurnEnd)

	a.stop()

	// --- daemon lifetime B: rebuild from the registry ---
	if got, _ := threadOnDisk(t, regPath, newID); got != t2 {
		t.Fatalf("registry thread after shutdown = %q, want %q", got, t2)
	}
	sinkB := newStreamTurnSink(0, nil)
	b := startCodexPool(t, regPath, codexHome, sinkB)
	hb := b.revive(t, newID)
	if _, t3 := hb.bound(t, nil); t3 != t2 {
		t.Fatalf("rebuilt session's thread = %q, want the stored %q resumed", t3, t2)
	}
	if err := hb.r.WriteUserTurn(context.Background(), "c-1", []byte("after rebuild")); err != nil {
		t.Fatalf("WriteUserTurn(after rebuild): %v", err)
	}
	awaitEnvelope(t, sinkB, newID, "turn end", isTurnEnd)
	if got, _ := threadOnDisk(t, regPath, newID); got != t2 {
		t.Fatalf("registry thread after rebuild = %q, want %q", got, t2)
	}
}

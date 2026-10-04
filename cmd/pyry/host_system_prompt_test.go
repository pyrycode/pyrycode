package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay/handlers"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

// The existing fake runner supplies inert methods; Run exposes a stable child
// identity while holding the installed argv for prompt-file inspection.
type hostPromptRunner struct {
	stubRunner
	mu         sync.Mutex
	args       []string
	running    bool
	actuations int
}

func (r *hostPromptRunner) State() sessions.State {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.running {
		return sessions.State{Phase: sessions.PhaseRunning, ChildPID: 2768}
	}
	return sessions.State{}
}
func (r *hostPromptRunner) Run(ctx context.Context) error {
	r.mu.Lock()
	r.running = true
	r.mu.Unlock()
	<-ctx.Done()
	r.mu.Lock()
	r.running = false
	r.mu.Unlock()
	return ctx.Err()
}
func (r *hostPromptRunner) SetSpawnArgs(args []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.args = append([]string(nil), args...)
}
func (r *hostPromptRunner) Restart([]string)      { r.actuate() }
func (r *hostPromptRunner) RestartFresh(string)   { r.actuate() }
func (r *hostPromptRunner) Interrupt() error      { r.actuate(); return nil }
func (r *hostPromptRunner) BeginTeardown()        { r.actuate() }
func (r *hostPromptRunner) BeginRotation() func() { r.actuate(); return func() {} }
func (r *hostPromptRunner) actuate()              { r.mu.Lock(); r.actuations++; r.mu.Unlock() }
func (r *hostPromptRunner) promptPath(t *testing.T) string {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, arg := range r.args {
		if arg == "--append-system-prompt-file" && i+1 < len(r.args) {
			return r.args[i+1]
		}
	}
	t.Fatal("runner has no prompt file")
	return ""
}

func TestHostSystemPromptRelayUsesConversationPool(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	reg := &conversations.Registry{}
	for _, id := range []string{"active", "minted", "later"} {
		text := id + "-conversation-last"
		reg.Create(conversations.Conversation{ID: conversations.ConversationID(id), SystemPrompt: &text})
	}
	pool, err := sessions.New(sessions.Config{
		RegistryPath: filepath.Join(dir, "sessions.json"), ConversationsRegistry: reg,
		Bootstrap: sessions.SessionConfig{ClaudeBin: os.Args[0], WorkDir: dir},
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		RunnerFactory: func(cfg sessions.RunnerConfig) (sessions.Runner, error) {
			return &hostPromptRunner{args: append([]string(nil), cfg.ClaudeArgs...)}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- pool.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("pool did not stop")
		}
	})
	select {
	case <-pool.Ready():
	case <-time.After(3 * time.Second):
		t.Fatal("pool not ready")
	}
	activeID, err := pool.Create(ctx, "active")
	if err != nil {
		t.Fatal(err)
	}
	active, err := pool.Lookup(activeID)
	if err != nil {
		t.Fatal(err)
	}
	runner := active.Runner().(*hostPromptRunner)
	for deadline := time.Now().Add(2 * time.Second); runner.State().ChildPID == 0; {
		if time.Now().After(deadline) {
			t.Fatal("active runner did not start")
		}
		time.Sleep(time.Millisecond)
	}
	path := runner.promptPath(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	stamp := time.Unix(123456789, 0)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	mintedID, err := pool.Mint("minted", dir)
	if err != nil {
		t.Fatal(err)
	}
	w := relayWiring{hostSystemPrompt: pool}
	out := make(chan protocol.RoutingEnvelope, 2)
	conn := dispatch.NewTestConn("requester", out, nil)
	set := func(text string, wantType string) {
		t.Helper()
		payload, err := json.Marshal(protocol.SetHostSystemPromptPayload{SystemPrompt: &text})
		if err != nil {
			t.Fatal(err)
		}
		if err := handlers.SetHostSystemPrompt(w.hostSystemPrompt, logger)(ctx, conn, protocol.Envelope{ID: 2768, Payload: payload}); err != nil {
			t.Fatal(err)
		}
		if len(out) != 1 {
			t.Fatalf("reply count = %d", len(out))
		}
		var reply protocol.Envelope
		if err := json.Unmarshal((<-out).Frame, &reply); err != nil {
			t.Fatal(err)
		}
		if reply.Type != wantType || reply.InReplyTo == nil || *reply.InReplyTo != 2768 {
			t.Fatal("wrong acknowledgement")
		}
		if wantType == protocol.TypeError {
			var p protocol.ErrorPayload
			if err := json.Unmarshal(reply.Payload, &p); err != nil {
				t.Fatal(err)
			}
			if p.Code != protocol.CodeHostSystemPromptUnavailable || !p.Retryable {
				t.Fatal("storage error not retryable")
			}
		}
	}
	const changed = "HOST-CONSUMER-PRIVATE-2768"
	set(changed, protocol.TypeHostSystemPrompt)
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, before) {
		t.Fatal("active prompt changed")
	}
	if info, err := os.Stat(path); err != nil || !info.ModTime().Equal(stamp) {
		t.Fatal("active prompt rewritten")
	}
	if active.ID() != activeID || runner.State().ChildPID != 2768 {
		t.Fatal("active child identity changed")
	}
	for _, id := range []sessions.SessionID{mintedID, ""} {
		label := "minted"
		if id == "" {
			label = "later"
			id, err = pool.Mint("later", dir)
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := pool.Activate(ctx, id); err != nil {
			t.Fatal(err)
		}
		s, err := pool.Lookup(id)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(s.Runner().(*hostPromptRunner).promptPath(t))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), "\n"+changed+"\n") || !strings.HasSuffix(string(raw), label+"-conversation-last") {
			t.Fatal("next start used stale instructions or wrong order")
		}
	}
	// Make the setting directory unavailable without altering its stored bytes.
	stored, err := os.ReadFile(filepath.Join(dir, "daemon-instructions.json"))
	if err != nil {
		t.Fatal(err)
	}
	backup := dir + "-backup"
	if err := os.Rename(dir, backup); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(dir); _ = os.Rename(backup, dir) }()
	if err := os.WriteFile(dir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	set("HOST-FAILED-PRIVATE-2768", protocol.TypeError)
	if pool.DaemonInstructions() != changed {
		t.Fatal("failed durable write changed memory")
	}
	got, err := os.ReadFile(filepath.Join(backup, "daemon-instructions.json"))
	if err != nil || !bytes.Equal(got, stored) {
		t.Fatal("failed durable write changed store")
	}
	runner.mu.Lock()
	actuations := runner.actuations
	runner.mu.Unlock()
	if actuations != 0 {
		t.Fatal("handler actuated active runner")
	}
	if strings.Contains(logs.String(), changed) || strings.Contains(logs.String(), "HOST-FAILED-PRIVATE-2768") {
		t.Fatal("handler leaked instructions")
	}
}

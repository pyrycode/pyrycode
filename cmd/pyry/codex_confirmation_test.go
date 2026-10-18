package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/codexsup"
	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func TestCodexRunner_ConfirmedPermissionTracksAcceptedTurn(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "turns.jsonl")
	t.Setenv("FAKECODEX_TURN_LOG", logPath)
	h := newTestCodexRunner(t)
	h.r.SetSpawnPermissionMode("plan")
	h.run(t)
	h.bound(t, nil)
	reader, ok := any(h.r).(confirmedPermissionModeReader)
	if !ok {
		t.Fatal("Codex runner lacks the optional confirmed permission reader")
	}
	assert := func(want string, known bool) {
		t.Helper()
		if got, ok := reader.ConfirmedPermissionMode(); got != want || ok != known {
			t.Fatalf("confirmed = (%q,%v), want (%q,%v)", got, ok, want, known)
		}
	}
	assert("", false) // future plan intent is not an acknowledgement
	h.turn(t, "one")
	h.await(t, "turn end", isTurnEnd)
	assert("plan", true)
	if err := h.r.SetPermissionMode("default"); err != nil {
		t.Fatal(err)
	}
	assert("plan", true) // only future intent changed
	h.turn(t, "two")
	h.await(t, "turn end", isTurnEnd)
	assert("default", true)
	turns := readTurnLog(t, logPath)
	if len(turns) != 2 || turns[0]["sandboxPolicy"].(map[string]any)["type"] != "readOnly" || turns[1]["sandboxPolicy"].(map[string]any)["type"] != "workspaceWrite" {
		t.Fatal("posted policies did not match the confirmed plan/default turns")
	}
	old, _ := h.bound(t, nil)
	h.r.Restart(nil)
	h.bound(t, old)
	assert("", false)
	// A new child must establish its own acceptance, not inherit old state.
	h.turn(t, "three")
	h.await(t, "turn end", isTurnEnd)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if mode, known := reader.ConfirmedPermissionMode(); mode == "default" && known {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("replacement child never established its own confirmed posture")
}

// Drive the two independently ordered transport callbacks around a real bound
// child, without relying on scheduler timing to exercise the negative windows.
func TestCodexRunner_ConfirmationRequiresMatchingFactors(t *testing.T) {
	for _, order := range []string{"response-first", "notification-first"} {
		t.Run(order, func(t *testing.T) {
			h := newTestCodexRunner(t)
			h.run(t)
			client, thread := h.bound(t, nil)
			h.r.mu.Lock()
			gen, tr := h.r.childGeneration, h.r.confirmation.translator
			h.r.mu.Unlock()
			start := func(translator *codexsup.Translator, tid, turn string) {
				p, err := json.Marshal(map[string]any{"threadId": tid, "turn": map[string]string{"id": turn}})
				if err != nil {
					t.Fatal(err)
				}
				h.r.notify(translator, "turn/started", p)
			}
			assert := func(want string) {
				t.Helper()
				if got, known := h.r.ConfirmedPermissionMode(); got != want || known != (want != "") {
					t.Fatalf("confirmed = (%q,%v), want %q", got, known, want)
				}
			}
			epoch, ok := h.r.beginPermissionAttempt(client, gen, "plan")
			if !ok {
				t.Fatal("current child refused")
			}
			if order == "response-first" {
				h.r.finishPermissionAttempt(client, gen, epoch, "root-turn")
			} else {
				start(tr, thread, "root-turn")
			}
			assert("") // neither half alone confers authority
			// Same child, same turn ID, wrong thread must never confirm.
			start(tr, "other-thread", "root-turn")
			start(codexsup.NewTranslator(""), thread, "root-turn")
			assert("")
			if order == "response-first" {
				start(tr, thread, "root-turn")
			} else {
				h.r.finishPermissionAttempt(client, gen, epoch, "root-turn")
			}
			assert("plan")
			// A new workspace-write attempt hides the old plan before either ack.
			next, _ := h.r.beginPermissionAttempt(client, gen, "default")
			assert("")
			h.r.finishPermissionAttempt(client, gen, epoch, "root-turn")
			start(tr, thread, "root-turn")
			assert("") // old response cannot match this epoch
			h.r.finishPermissionAttempt(client, gen, next, "write-turn")
			assert("") // wrong turn ID cannot join
			start(tr, thread, "write-turn")
			assert("default")
			// A different root turn consumes authority; old duplicates cannot revive it.
			start(tr, thread, "another-root-turn")
			start(tr, thread, "write-turn")
			h.r.finishPermissionAttempt(client, gen, next, "write-turn")
			assert("")
			// Failure consumes its epoch; a late apparent success cannot revive it.
			failed, _ := h.r.beginPermissionAttempt(client, gen, "plan")
			start(tr, thread, "failed-turn")
			h.r.finishPermissionAttempt(client, gen, failed, "")
			h.r.finishPermissionAttempt(client, gen, failed, "failed-turn")
			assert("")
			for _, mode := range []string{"", "unknown-mode"} {
				unknown, _ := h.r.beginPermissionAttempt(client, gen, mode)
				start(tr, thread, "unknown-turn")
				h.r.finishPermissionAttempt(client, gen, unknown, "unknown-turn")
				assert("")
			}
		})
	}
}

func TestCodexRunner_ConfirmationInvalidatedByLifecycle(t *testing.T) {
	for _, barrier := range []string{"rotation-abort", "teardown", "restart", "fresh", "exit"} {
		t.Run(barrier, func(t *testing.T) {
			h := newTestCodexRunner(t)
			h.r.SetSpawnPermissionMode("plan")
			h.run(t)
			client, thread := h.bound(t, nil)
			h.turn(t, "establish")
			h.await(t, "turn end", isTurnEnd)
			if got, ok := h.r.ConfirmedPermissionMode(); got != "plan" || !ok {
				t.Fatal("plan not established")
			}
			h.r.mu.Lock()
			gen, tr, epoch := h.r.childGeneration, h.r.confirmation.translator, h.r.confirmation.epoch
			h.r.mu.Unlock()
			switch barrier {
			case "rotation-abort":
				h.r.BeginRotation()()
			case "teardown":
				h.r.BeginTeardown()
			case "restart":
				h.r.Restart(nil)
			case "fresh":
				h.r.RestartFresh("s-next")
			case "exit":
				if err := client.Stop(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			if got, ok := h.r.ConfirmedPermissionMode(); got != "" || ok {
				t.Fatalf("%s retained (%q,%v)", barrier, got, ok)
			}
			if barrier == "restart" || barrier == "fresh" || barrier == "exit" {
				h.bound(t, client)
			}
			p, _ := json.Marshal(map[string]any{"threadId": thread, "turn": map[string]string{"id": "stale-turn"}})
			h.r.notify(tr, "turn/started", p)
			h.r.finishPermissionAttempt(client, gen, epoch, "stale-turn")
			if got, ok := h.r.ConfirmedPermissionMode(); got != "" || ok {
				t.Fatalf("%s revived (%q,%v)", barrier, got, ok)
			}
		})
	}
}

// Use the actual runner factory and pool adapter, then the relay's run-config
// seam. Stored bypass intent must not leak through as wire permission authority.
func TestCodexRunner_ConfirmedPermissionBoundRunConfig(t *testing.T) {
	pool, err := sessions.New(sessions.Config{
		Bootstrap:     sessions.SessionConfig{ClaudeBin: os.Args[0], WorkDir: t.TempDir()},
		RegistryPath:  filepath.Join(t.TempDir(), "sessions.json"),
		RunnerFactory: newCodexRunnerFactory(codexHarness{bin: fakeCodexBin(t), home: t.TempDir(), sink: newStreamTurnSink(0, nil)}),
	})
	if err != nil {
		t.Fatal(err)
	}
	id := pool.Default().ID()
	h := &codexHarnessT{r: pool.Default().Runner().(*codexRunner), events: make(chan turnevent.Event, 256)}
	h.r.cfg.Sink = func(ev turnevent.Event) { h.events <- ev }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = pool.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	h.bound(t, nil)
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{ID: "bound", CurrentSessionID: string(id), LastUsedAt: time.Now()})
	reg.Create(conversations.Conversation{ID: "unbound", LastUsedAt: time.Now()})
	read := runConfigFor(func(conv string) (boundRunSettings, bool) {
		return resolveBoundRunSettings(reg, runSettingsPool{pool}, conv)
	}, nil)
	check := func(mode string, yolo bool) {
		t.Helper()
		cfg, ok := read("bound")
		if !ok || cfg.SessionID != string(id) || cfg.PermissionMode != mode || cfg.YOLO != yolo {
			t.Fatalf("run config = %+v,%v; want exact session mode %q yolo %v", cfg, ok, mode, yolo)
		}
	}
	plan, bypass := "plan", "bypassPermissions"
	if err := pool.UpdateSettings(id, sessions.SettingsUpdate{PermissionMode: &plan}); err != nil {
		t.Fatal(err)
	}
	check("", false)
	h.turn(t, "plan")
	h.await(t, "turn end", isTurnEnd)
	check("plan", false)
	if err := pool.UpdateSettings(id, sessions.SettingsUpdate{PermissionMode: &bypass}); err != nil {
		t.Fatal(err)
	}
	check("plan", false)
	h.turn(t, "bypass")
	h.await(t, "turn end", isTurnEnd)
	check("bypassPermissions", true)
	if cfg, ok := read("unbound"); ok || cfg.SessionID != "" || cfg.PermissionMode != "" || cfg.YOLO {
		t.Fatalf("unbound leaked config: %+v,%v", cfg, ok)
	}
	h.r.BeginTeardown()
	check("", false)
}

func TestCodexRunner_ConcurrentStartWaitCancellation(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "turns.jsonl")
	t.Setenv("FAKECODEX_TURN_LOG", logPath)
	h := newTestCodexRunner(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	h.r.cfg.Families = func() []turnevent.ModelOption {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		return nil
	}
	h.r.SetSpawnPermissionMode("plan")
	h.run(t)
	h.bound(t, nil)
	first := make(chan error, 1)
	go func() { first <- h.r.WriteUserTurn(context.Background(), "c-1", []byte("first")) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first snapshot did not reach model resolution")
	}
	if err := h.r.SetPermissionMode("default"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancelled := make(chan error, 1)
	go func() { cancelled <- h.r.WriteUserTurn(ctx, "c-1", []byte("cancelled")) }()
	cancel()
	select {
	case err := <-cancelled:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("waiting writer error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled writer waited for the blocked first call")
	}
	if calls.Load() != 1 {
		t.Fatal("cancelled waiter reached model resolution")
	}
	second := make(chan error, 1)
	go func() { second <- h.r.WriteUserTurn(context.Background(), "c-1", []byte("second")) }()
	// Keep the first snapshot parked while another live caller contends. Without
	// serialization it can post default first and the older plan would land last.
	select {
	case err := <-second:
		t.Fatalf("second start passed blocked first: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	once.Do(func() { close(release) })
	for _, result := range []chan error{first, second} {
		select {
		case err := <-result:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("serialized start did not finish")
		}
	}
	h.await(t, "first turn end", isTurnEnd)
	h.await(t, "second turn end", isTurnEnd)
	turns := readTurnLog(t, logPath)
	if len(turns) != 2 || turns[0]["sandboxPolicy"].(map[string]any)["type"] != "readOnly" || turns[1]["sandboxPolicy"].(map[string]any)["type"] != "workspaceWrite" {
		t.Fatal("concurrent RPC order or cancelled-call suppression failed")
	}
	// The deliberately overlapping fake turns may invalidate each other's
	// authority; the next isolated accepted turn must establish current default.
	h.turn(t, "settle")
	h.await(t, "settled turn end", isTurnEnd)
	if mode, ok := h.r.ConfirmedPermissionMode(); mode != "default" || !ok {
		t.Fatalf("settled confirmation = %q,%v", mode, ok)
	}
}

func TestCodexRunner_CancelledWaiterCannotPostAfterHeldRPC(t *testing.T) {
	gate := filepath.Join(t.TempDir(), "start")
	logPath := filepath.Join(t.TempDir(), "turns.jsonl")
	t.Setenv("FAKECODEX_TURN_START_GATE", gate)
	t.Setenv("FAKECODEX_TURN_LOG", logPath)
	h := newTestCodexRunner(t)
	h.r.SetSpawnPermissionMode("plan")
	h.run(t)
	t.Cleanup(func() { _ = os.WriteFile(gate+".release", nil, 0o600) })
	h.bound(t, nil)
	h.turn(t, "establish")
	h.await(t, "established turn end", isTurnEnd)
	if got, ok := h.r.ConfirmedPermissionMode(); got != "plan" || !ok {
		t.Fatal("plan was not established")
	}
	if err := h.r.SetPermissionMode("default"); err != nil {
		t.Fatal(err)
	}
	first := make(chan error, 1)
	go func() { first <- h.r.WriteUserTurn(context.Background(), "c-1", []byte("[fakecodex:start-held]")) }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(gate + ".arrived"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("RPC did not reach fake gate")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got, ok := h.r.ConfirmedPermissionMode(); got != "" || ok {
		t.Fatalf("held write RPC still reported old plan: %q,%v", got, ok)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	second := make(chan error, 1)
	go func() { second <- h.r.WriteUserTurn(ctx, "c-1", []byte("must-not-post")) }()
	select {
	case err := <-second:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("waiting writer error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("waiting writer did not honor deadline during held RPC")
	}
	if err := os.WriteFile(gate+".release", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-first:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("held RPC did not finish")
	}
	h.await(t, "released turn end", isTurnEnd)
	// A later successful RPC is a barrier: the cancelled waiter never posts even
	// after the held call releases the token.
	h.turn(t, "barrier")
	h.await(t, "barrier turn end", isTurnEnd)
	if turns := readTurnLog(t, logPath); len(turns) != 3 {
		t.Fatalf("turn/start count=%d, want establish/held/barrier only", len(turns))
	}
	if got, ok := h.r.ConfirmedPermissionMode(); got != "default" || !ok {
		t.Fatalf("released current mode = %q,%v", got, ok)
	}
}

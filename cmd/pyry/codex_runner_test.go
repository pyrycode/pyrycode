package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/codexsup"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/streamsup"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

var (
	fakeCodexOnce sync.Once
	fakeCodexPath string
	fakeCodexErr  error
)

// fakeCodexBin builds the fake app-server once per test binary, the way
// codexsup's TestMain does.
func fakeCodexBin(t *testing.T) string {
	t.Helper()
	fakeCodexOnce.Do(func() {
		dir, err := os.MkdirTemp("", "pyry-fakecodex-*")
		if err != nil {
			fakeCodexErr = err
			return
		}
		fakeCodexPath = filepath.Join(dir, "fakecodex")
		out, err := exec.Command("go", "build", "-o", fakeCodexPath,
			"github.com/pyrycode/pyrycode/internal/e2e/internal/fakecodex").CombinedOutput()
		if err != nil {
			fakeCodexErr = fmt.Errorf("go build fakecodex: %v\n%s", err, out)
		}
	})
	if fakeCodexErr != nil {
		t.Fatal(fakeCodexErr)
	}
	return fakeCodexPath
}

type codexHarnessT struct {
	r      *codexRunner
	events chan turnevent.Event
	exits  atomic.Int32
}

func newTestCodexRunner(t *testing.T) *codexHarnessT {
	t.Helper()
	h := &codexHarnessT{events: make(chan turnevent.Event, 256)}
	h.r = newCodexRunner(codexRunnerConfig{
		Binary:  fakeCodexBin(t),
		Home:    t.TempDir(),
		Dir:     t.TempDir(),
		Tag:     newStreamSessionTag("s-1"),
		Sink:    func(ev turnevent.Event) { h.events <- ev },
		OnExit:  func() { h.exits.Add(1) },
		Backoff: 10 * time.Millisecond,
	})
	return h
}

// run starts Run and returns a stop that cancels it and waits for it.
func (h *codexHarnessT) run(t *testing.T) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.r.Run(ctx) }()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Errorf("Run = %v, want context.Canceled", err)
				}
			case <-time.After(10 * time.Second):
				t.Error("Run did not return after cancel")
			}
		})
	}
	t.Cleanup(stop)
	return stop
}

// bound waits for a bound client other than not, and returns it with the
// thread it is on.
func (h *codexHarnessT) bound(t *testing.T, not *codexsup.Client) (*codexsup.Client, string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		h.r.mu.Lock()
		c, tid := h.r.client, h.r.threadID
		h.r.mu.Unlock()
		if c != nil && c != not {
			return c, tid
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no client bound")
	return nil, ""
}

// await returns the first event match accepts.
func (h *codexHarnessT) await(t *testing.T, what string, match func(turnevent.Event) bool) turnevent.Event {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case ev := <-h.events:
			if match(ev) {
				return ev
			}
		case <-timeout:
			t.Fatalf("no %s event", what)
			return nil
		}
	}
}

func isTurnEnd(ev turnevent.Event) bool {
	_, ok := ev.(turnevent.TurnEnd)
	return ok
}

func (h *codexHarnessT) turn(t *testing.T, text string) {
	t.Helper()
	if err := h.r.WriteUserTurn(context.Background(), "c-1", []byte(text)); err != nil {
		t.Fatalf("WriteUserTurn(%q) = %v", text, err)
	}
}

func TestCodexRunner_TurnReachesSink(t *testing.T) {
	h := newTestCodexRunner(t)
	h.run(t)
	h.bound(t, nil)
	h.turn(t, "hello")
	h.await(t, "text", func(ev turnevent.Event) bool {
		c, ok := ev.(turnevent.TextChunk)
		return ok && c.Text != ""
	})
	end := h.await(t, "turn end", isTurnEnd).(turnevent.TurnEnd)
	if end.Reason != turnevent.TurnEndReasonEndTurn {
		t.Fatalf("TurnEnd.Reason = %q, want end_turn", end.Reason)
	}
}

func TestCodexRunner_InterruptEndsRunningTurn(t *testing.T) {
	h := newTestCodexRunner(t)
	h.run(t)
	h.bound(t, nil)
	h.turn(t, "[fakecodex:hold]")
	deadline := time.Now().Add(10 * time.Second)
	for {
		h.r.mu.Lock()
		running := h.r.turnID != ""
		h.r.mu.Unlock()
		if running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("turn never started")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := h.r.Interrupt(); err != nil {
		t.Fatalf("Interrupt = %v", err)
	}
	end := h.await(t, "turn end", isTurnEnd).(turnevent.TurnEnd)
	if end.Reason != turnevent.TurnEndReasonCancelled {
		t.Fatalf("TurnEnd.Reason = %q, want cancelled", end.Reason)
	}
	if err := h.r.Interrupt(); err != nil {
		t.Fatalf("Interrupt with no running turn = %v, want nil", err)
	}
}

// TestCodexRunner_ApprovalDeclined: with no approvals path yet, the runner
// leaves codexsup's default decline in place, so the command never runs.
func TestCodexRunner_ApprovalDeclined(t *testing.T) {
	h := newTestCodexRunner(t)
	h.run(t)
	h.bound(t, nil)
	h.turn(t, "[fakecodex:approval]")
	ev := h.await(t, "tool update", func(ev turnevent.Event) bool {
		_, ok := ev.(turnevent.ToolUpdate)
		return ok
	}).(turnevent.ToolUpdate)
	if ev.ResultDetail != "declined" {
		t.Fatalf("ToolUpdate.ResultDetail = %q, want declined", ev.ResultDetail)
	}
	h.await(t, "turn end", isTurnEnd)
}

// TestCodexRunner_CrashResumesThread: an app-server exit the runner did not
// order reaches the exit lane, and the respawn resumes the same thread (the
// fake mints a new id only on thread/start).
func TestCodexRunner_CrashResumesThread(t *testing.T) {
	h := newTestCodexRunner(t)
	h.run(t)
	first, tid := h.bound(t, nil)
	if tid == "" {
		t.Fatal("no thread id held")
	}
	if err := stopCodexClient(first); err != nil {
		t.Fatalf("stop: %v", err)
	}
	_, tid2 := h.bound(t, first)
	if tid2 != tid {
		t.Fatalf("thread after crash = %q, want resumed %q", tid2, tid)
	}
	if h.exits.Load() < 1 {
		t.Fatal("exit lane not fired")
	}
	if n := h.r.State().RestartCount; n < 1 {
		t.Fatalf("RestartCount = %d, want >= 1", n)
	}
	h.turn(t, "after crash")
	h.await(t, "turn end", isTurnEnd)
}

// TestCodexRunner_EvictionResumesThread mirrors Session.runActive's eviction:
// BeginTeardown, then Run's ctx ends; turns are refused while evicted, and the
// next Run on the same runner resumes the same thread.
func TestCodexRunner_EvictionResumesThread(t *testing.T) {
	h := newTestCodexRunner(t)
	stop := h.run(t)
	first, tid := h.bound(t, nil)
	h.r.BeginTeardown()
	if err := h.r.WriteUserTurn(context.Background(), "c-1", []byte("x")); !errors.Is(err, streamsup.ErrNoLiveChild) {
		t.Fatalf("WriteUserTurn during teardown = %v, want ErrNoLiveChild", err)
	}
	stop()
	if err := h.r.WriteUserTurn(context.Background(), "c-1", []byte("x")); !errors.Is(err, streamsup.ErrNoLiveChild) {
		t.Fatalf("WriteUserTurn while evicted = %v, want ErrNoLiveChild", err)
	}
	if got := h.r.State().Phase; got != sessions.PhaseStopped {
		t.Fatalf("Phase = %q, want stopped", got)
	}
	h.run(t)
	_, tid2 := h.bound(t, first)
	if tid2 != tid {
		t.Fatalf("thread after eviction = %q, want resumed %q", tid2, tid)
	}
	h.turn(t, "after eviction")
	h.await(t, "turn end", isTurnEnd)
}

// TestCodexRunner_RestartFreshStartsNewThread: the rotation gate refuses turns
// until the fresh spawn binds, the tag moves to the new pool id, and the
// thread is a new one.
func TestCodexRunner_RestartFreshStartsNewThread(t *testing.T) {
	h := newTestCodexRunner(t)
	h.run(t)
	first, tid := h.bound(t, nil)
	h.r.BeginRotation()
	if err := h.r.WriteUserTurn(context.Background(), "c-1", []byte("x")); !errors.Is(err, streamsup.ErrNoLiveChild) {
		t.Fatalf("WriteUserTurn during rotation = %v, want ErrNoLiveChild", err)
	}
	h.r.RestartFresh("s-2")
	_, tid2 := h.bound(t, first)
	if tid2 == "" || tid2 == tid {
		t.Fatalf("thread after RestartFresh = %q, want a new one (old %q)", tid2, tid)
	}
	if got := h.r.cfg.Tag.ID(); got != "s-2" {
		t.Fatalf("tag = %q, want s-2", got)
	}
	h.turn(t, "fresh")
	h.await(t, "turn end", isTurnEnd)
}

// TestCodexRunner_RestartResumesAndInstallsSettings: Restart respawns on the
// same thread with the new model and effort, and the stored posture methods
// never loosen anything.
func TestCodexRunner_RestartResumesAndInstallsSettings(t *testing.T) {
	h := newTestCodexRunner(t)
	h.run(t)
	first, tid := h.bound(t, nil)
	h.r.Restart([]string{"--model", "gpt-6-luna", "--effort=low", "--permission-mode", "plan"})
	_, tid2 := h.bound(t, first)
	if tid2 != tid {
		t.Fatalf("thread after Restart = %q, want resumed %q", tid2, tid)
	}
	h.r.mu.Lock()
	model, effort := h.r.model, h.r.effort
	h.r.mu.Unlock()
	if model != "gpt-6-luna" || effort != "low" {
		t.Fatalf("model, effort = %q, %q; want gpt-6-luna, low", model, effort)
	}
	if err := h.r.SetPermissionMode(sessions.PermissionModeBypass); err == nil {
		t.Fatal("SetPermissionMode = nil, want an error: Codex has no live posture switch")
	}
	if err := h.r.SetModel("gpt-6-sol"); err != nil {
		t.Fatalf("SetModel = %v", err)
	}
	h.r.SetSpawnArgs([]string{"--effort", "medium"})
	h.r.mu.Lock()
	model, effort = h.r.model, h.r.effort
	h.r.mu.Unlock()
	if model != "" || effort != "medium" {
		t.Fatalf("after SetSpawnArgs model, effort = %q, %q; want \"\", medium", model, effort)
	}
	h.turn(t, "after restart")
	h.await(t, "turn end", isTurnEnd)
}

func TestCodexTurnSettings(t *testing.T) {
	tests := []struct {
		args          []string
		model, effort string
	}{
		{nil, "", ""},
		{[]string{"--model", "gpt-6-luna", "--effort", "low"}, "gpt-6-luna", "low"},
		{[]string{"--model=gpt-6-luna", "--effort=high"}, "gpt-6-luna", "high"},
		{[]string{"--dangerously-skip-permissions", "--permission-mode", "plan"}, "", ""},
		{[]string{"--model", "a", "--model", "b"}, "b", ""},
		{[]string{"--effort"}, "", ""},
	}
	for _, tc := range tests {
		model, effort := codexTurnSettings(tc.args)
		if model != tc.model || effort != tc.effort {
			t.Errorf("codexTurnSettings(%q) = %q, %q; want %q, %q", tc.args, model, effort, tc.model, tc.effort)
		}
	}
}

func TestPrepareCodexHome(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "codex-home")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	auth := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(auth, []byte(`{"token":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte("sandbox_mode = \"danger-full-access\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := prepareCodexHome(dir); err != nil {
		t.Fatalf("prepareCodexHome = %v", err)
	}
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("home mode = %v, %v; want 0700", fi.Mode().Perm(), err)
	}
	fi, err := os.Stat(cfgPath)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %v, %v; want 0600", fi.Mode().Perm(), err)
	}
	got, _ := os.ReadFile(cfgPath)
	for _, line := range []string{`approval_policy = "on-request"`, `sandbox_mode = "read-only"`, `approvals_reviewer = "user"`} {
		if !strings.Contains(string(got), line) {
			t.Errorf("config.toml lacks %s:\n%s", line, got)
		}
	}
	if strings.Contains(string(got), "danger-full-access") {
		t.Errorf("edited config survived:\n%s", got)
	}
	if b, _ := os.ReadFile(auth); string(b) != `{"token":"x"}` {
		t.Errorf("auth.json changed: %q", b)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Errorf("home holds %d entries, want auth.json and config.toml only", len(entries))
	}
}

// TestCodexRunnerFactory_DaemonHome: the factory prepares the daemon-owned
// home and the app-server process runs with CODEX_HOME set to it, never the
// operator's ~/.codex.
func TestCodexRunnerFactory_DaemonHome(t *testing.T) {
	if _, err := newCodexRunnerFactory(codexHarness{home: t.TempDir()})(sessions.RunnerConfig{SessionID: "s-1"}); err == nil {
		t.Fatal("factory without a sink = nil error")
	}
	tmp := t.TempDir()
	seen := filepath.Join(tmp, "codex-home-seen")
	wrapper := filepath.Join(tmp, "codex")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s' \"$CODEX_HOME\" > %q\nexec %q \"$@\"\n", seen, fakeCodexBin(t))
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", tmp)
	home := codexHomePath(filepath.Join(tmp, "instance"))
	factory := newCodexRunnerFactory(codexHarness{bin: wrapper, home: home, sink: newStreamTurnSink(0, nil)})
	runner, err := factory(sessions.RunnerConfig{SessionID: "s-1", Harness: harnessCodex, WorkDir: tmp,
		ClaudeArgs: []string{"--model", "gpt-6-luna", "--effort", "low"}})
	if err != nil {
		t.Fatalf("factory = %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "config.toml")); err != nil {
		t.Fatalf("config.toml not written: %v", err)
	}
	h := &codexHarnessT{r: runner.(*codexRunner)}
	if h.r.model != "gpt-6-luna" || h.r.effort != "low" {
		t.Fatalf("model, effort = %q, %q; want gpt-6-luna, low", h.r.model, h.r.effort)
	}
	h.run(t)
	h.bound(t, nil)
	got, err := os.ReadFile(seen)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != home {
		t.Fatalf("CODEX_HOME = %q, want %q", got, home)
	}
}

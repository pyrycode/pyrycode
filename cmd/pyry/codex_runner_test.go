package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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

// TestCodexRunner_ChildPIDWhileRunning: State reports the app-server's pid while
// a client is bound, and 0 before the first spawn and after Run returns (#2663).
func TestCodexRunner_ChildPIDWhileRunning(t *testing.T) {
	h := newTestCodexRunner(t)
	if pid := h.r.State().ChildPID; pid != 0 {
		t.Fatalf("ChildPID before Run = %d, want 0", pid)
	}
	stop := h.run(t)
	c, _ := h.bound(t, nil)
	if got, want := h.r.State().ChildPID, c.PID(); got != want || got == 0 {
		t.Fatalf("ChildPID while bound = %d, want the app-server's pid %d", got, want)
	}
	stop()
	if pid := h.r.State().ChildPID; pid != 0 {
		t.Fatalf("ChildPID after Run returned = %d, want 0", pid)
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

// TestCodexRunner_InterruptRefusedReturnsError: a refused turn/interrupt for
// a turn the runner still tracks is returned. Only a turn that completed
// before Codex handled the interrupt makes the refusal nil.
func TestCodexRunner_InterruptRefusedReturnsError(t *testing.T) {
	h := newTestCodexRunner(t)
	h.run(t)
	h.bound(t, nil)
	h.r.mu.Lock()
	h.r.turnID = "never-minted"
	h.r.mu.Unlock()
	if err := h.r.Interrupt(); err == nil {
		t.Fatal("Interrupt of a turn Codex does not know = nil, want the refusal")
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
// same thread with the new model and effort, and the posture setters store the
// mode for the next turn.
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
	if err := h.r.SetPermissionMode("plan"); err != nil {
		t.Fatalf("SetPermissionMode = %v, want nil: the posture applies per turn", err)
	}
	h.r.mu.Lock()
	mode := h.r.mode
	h.r.mu.Unlock()
	if mode != "plan" {
		t.Fatalf("mode after SetPermissionMode = %q, want plan", mode)
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
	for _, line := range []string{`approval_policy = "on-request"`, `sandbox_mode = "read-only"`, `approvals_reviewer = "user"`, `[features]`, `default_mode_request_user_input = true`} {
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

func TestCheckCodexVersion(t *testing.T) {
	for _, v := range []string{"0.156.1", "0.156.2", "0.157.0", "1.0.0", "0.156.1+build.7"} {
		if err := checkCodexVersion(v); err != nil {
			t.Errorf("checkCodexVersion(%q) = %v, want nil", v, err)
		}
	}
	for _, v := range []string{"0.155.0", "0.155.0-alpha.3", "0.156.1-alpha.1", "0.156.0", "", "garbage", "0.156", "0.156.x", "0.156.1.2"} {
		err := checkCodexVersion(v)
		if err == nil {
			t.Errorf("checkCodexVersion(%q) = nil, want a refusal", v)
			continue
		}
		if !strings.Contains(err.Error(), fmt.Sprintf("%q", v)) || !strings.Contains(err.Error(), codexMinVersion) {
			t.Errorf("checkCodexVersion(%q) = %q; want it to name %q and %s", v, err, v, codexMinVersion)
		}
	}
}

// TestCodexRunnerFactory_Refusals: a Codex below the pinned version, one whose
// version cannot be parsed, and a signed-out daemon home each refuse the
// session with no runner. The probe never opens a thread.
func TestCodexRunnerFactory_Refusals(t *testing.T) {
	for _, tc := range []struct {
		name, version string
		signedOut     bool
		want          func(home string) []string
	}{
		{"old version", "0.155.0-alpha.3", false, func(string) []string { return []string{`"0.155.0-alpha.3"`, "0.156.1"} }},
		{"unparseable version", "banana", false, func(string) []string { return []string{`"banana"`, "0.156.1"} }},
		{"signed out", "", true, func(home string) []string { return []string{"CODEX_HOME=" + home, "sign in"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.version != "" {
				t.Setenv("FAKECODEX_VERSION", tc.version)
			}
			if tc.signedOut {
				t.Setenv("FAKECODEX_SIGNED_OUT", "1")
			}
			home := filepath.Join(t.TempDir(), "codex-home")
			factory := newCodexRunnerFactory(codexHarness{bin: fakeCodexBin(t), home: home, sink: newStreamTurnSink(0, nil)})
			runner, err := factory(sessions.RunnerConfig{SessionID: "s-1", Harness: harnessCodex, WorkDir: t.TempDir()})
			if err == nil {
				t.Fatal("factory = nil error, want a refusal")
			}
			if runner != nil {
				t.Errorf("factory returned a runner alongside %v", err)
			}
			for _, s := range tc.want(home) {
				if !strings.Contains(err.Error(), s) {
					t.Errorf("error %q does not name %q", err, s)
				}
			}
			if strings.Contains(err.Error(), "@") {
				t.Errorf("error %q carries account details", err)
			}
		})
	}
}

// readTurnLog returns the turn/start params the fake recorded, one per turn.
func readTurnLog(t *testing.T, path string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var turns []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var p map[string]any
		if err := json.Unmarshal([]byte(line), &p); err != nil {
			t.Fatalf("turn log line %q: %v", line, err)
		}
		turns = append(turns, p)
	}
	return turns
}

// turnPosture renders a recorded turn's overrides for comparison.
func turnPosture(p map[string]any) string {
	b, _ := json.Marshal([]any{p["model"], p["effort"], p["approvalPolicy"], p["sandboxPolicy"], p["approvalsReviewer"]})
	return string(b)
}

// TestCodexRunner_LiveSettingsReachNextTurn (#2586): through the pool and the
// production factory, a live model, effort and posture change sends nothing to
// Codex and respawns nothing, and the next turn/start carries the new settings.
// The first turn carries the session's construction-time posture.
func TestCodexRunner_LiveSettingsReachNextTurn(t *testing.T) {
	turnLog := filepath.Join(t.TempDir(), "turns.jsonl")
	t.Setenv("FAKECODEX_TURN_LOG", turnLog)
	factory := newCodexRunnerFactory(codexHarness{
		bin: fakeCodexBin(t), home: filepath.Join(t.TempDir(), "codex-home"), sink: newStreamTurnSink(0, nil),
	})
	pool, err := sessions.New(sessions.Config{
		Bootstrap:     sessions.SessionConfig{ClaudeBin: os.Args[0], WorkDir: t.TempDir()},
		RegistryPath:  filepath.Join(t.TempDir(), "sessions.json"),
		RunnerFactory: factory,
	})
	if err != nil {
		t.Fatalf("sessions.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = pool.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	id := pool.Default().ID()
	h := &codexHarnessT{r: pool.Default().Runner().(*codexRunner)}
	client, _ := h.bound(t, nil)

	h.turn(t, "one")
	const granular = `{"granular":{"mcp_elicitations":true,"rules":true,"sandbox_approval":true}}`
	want := []string{`[null,null,` + granular + `,{"type":"workspaceWrite"},"user"]`}

	model, effort, plan := "gpt-6-luna", "low", "plan"
	if err := pool.UpdateSettings(id, sessions.SettingsUpdate{Model: &model, Effort: &effort, PermissionMode: &plan}); err != nil {
		t.Fatalf("UpdateSettings = %v", err)
	}
	if n := len(readTurnLog(t, turnLog)); n != 1 {
		t.Fatalf("turn/start count after the settings change = %d, want 1: the change sent a turn", n)
	}
	h.turn(t, "two")
	want = append(want, `["gpt-6-luna","low",`+granular+`,{"type":"readOnly"},"user"]`)

	yolo := true
	if err := pool.UpdateSettings(id, sessions.SettingsUpdate{YOLO: &yolo}); err != nil {
		t.Fatalf("UpdateSettings(yolo) = %v", err)
	}
	h.turn(t, "three")
	want = append(want, `["gpt-6-luna","low","never",{"type":"dangerFullAccess"},"user"]`)

	turns := readTurnLog(t, turnLog)
	if len(turns) != len(want) {
		t.Fatalf("turn/start count = %d, want %d", len(turns), len(want))
	}
	for i, p := range turns {
		if got := turnPosture(p); got != want[i] {
			t.Errorf("turn %d overrides = %s, want %s", i+1, got, want[i])
		}
	}
	h.r.mu.Lock()
	same := h.r.client == client
	h.r.mu.Unlock()
	if !same || h.r.State().RestartCount != 0 {
		t.Errorf("settings change respawned the app-server (same client %v, restarts %d)", same, h.r.State().RestartCount)
	}
}

// TestCodexRunner_ModelFamilyFollowsNewestVersion (#2628): through the
// production factory and the daemon's store, a session stored on a family
// sends and reports the family's newest held version on every turn, picks up
// a newer one between turns with no respawn, and keeps the family as its
// setting; a version no family names is sent and reported unchanged.
func TestCodexRunner_ModelFamilyFollowsNewestVersion(t *testing.T) {
	turnLog := filepath.Join(t.TempDir(), "turns.jsonl")
	t.Setenv("FAKECODEX_TURN_LOG", turnLog)
	store := newModelVocabularyStore(storePath(t))
	t.Cleanup(store.Close)
	factory := newCodexRunnerFactory(codexHarness{
		bin: fakeCodexBin(t), home: t.TempDir(), sink: newStreamTurnSink(0, nil), vocab: store,
	})
	runner, err := factory(sessions.RunnerConfig{
		SessionID: "s-1", Harness: harnessCodex, WorkDir: t.TempDir(), ClaudeArgs: []string{"--model", "luna"},
	})
	if err != nil {
		t.Fatalf("factory = %v", err)
	}
	h := &codexHarnessT{r: runner.(*codexRunner), events: make(chan turnevent.Event, 256)}
	h.r.cfg.Sink = func(ev turnevent.Event) { h.events <- ev }
	h.run(t)
	client, _ := h.bound(t, nil)
	// Let the spawn's own model/list read land, so it cannot overwrite the
	// entries this test retains below.
	deadline := time.Now().Add(10 * time.Second)
	for store.CodexModels() == nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	reported := func(text string) string {
		t.Helper()
		h.turn(t, "[fakecodex:usage] "+text)
		end := h.await(t, "turn end", isTurnEnd).(turnevent.TurnEnd)
		if len(end.ModelWindows) != 1 {
			t.Fatalf("turn %q ModelWindows = %#v, want one", text, end.ModelWindows)
		}
		return end.ModelWindows[0].ModelID
	}
	var want []string

	if got := reported("one"); got != "gpt-6-luna" {
		t.Errorf("turn one reported %q, want gpt-6-luna", got)
	}
	want = append(want, "gpt-6-luna")

	newer := slices.Clone(fakeCodexFamilies)
	for i := range newer {
		if newer[i].Value == "luna" {
			newer[i].ResolvedModel = "gpt-6.1-luna"
		}
	}
	store.RetainCodex(newer)
	if got := reported("two"); got != "gpt-6.1-luna" {
		t.Errorf("turn two reported %q, want gpt-6.1-luna", got)
	}
	want = append(want, "gpt-6.1-luna")
	h.r.mu.Lock()
	stored, same := h.r.model, h.r.client == client
	h.r.mu.Unlock()
	if stored != "luna" {
		t.Errorf("stored model = %q, want the family luna", stored)
	}
	if !same || h.r.State().RestartCount != 0 {
		t.Errorf("a newer version respawned the app-server (same client %v, restarts %d)", same, h.r.State().RestartCount)
	}

	if err := h.r.SetModel("gpt-5.6-sol"); err != nil {
		t.Fatalf("SetModel = %v", err)
	}
	if got := reported("three"); got != "gpt-5.6-sol" {
		t.Errorf("turn three reported %q, want gpt-5.6-sol", got)
	}
	want = append(want, "gpt-5.6-sol")

	turns := readTurnLog(t, turnLog)
	if len(turns) != len(want) {
		t.Fatalf("turn/start count = %d, want %d", len(turns), len(want))
	}
	for i, p := range turns {
		if p["model"] != want[i] {
			t.Errorf("turn %d model = %v, want %s", i+1, p["model"], want[i])
		}
	}
}

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

type taskStopRunner struct {
	stubRunner
	roster                 turnevent.BackgroundTaskRoster
	reported, accept, wait bool
	pid, calls, unrelated  int
	calledID               string
	deadline               time.Time
	entered                chan struct{}
}

func (r *taskStopRunner) State() sessions.State { return sessions.State{ChildPID: r.pid} }
func (r *taskStopRunner) BackgroundTaskRoster() (turnevent.BackgroundTaskRoster, bool) {
	return r.roster, r.reported
}
func (r *taskStopRunner) StopTask(ctx context.Context, id string) bool {
	r.calls++
	r.calledID = id
	r.deadline, _ = ctx.Deadline()
	if r.entered != nil {
		close(r.entered)
	}
	if r.wait {
		<-ctx.Done()
		return false
	}
	return r.accept
}
func (r *taskStopRunner) Interrupt() error { r.unrelated++; return nil }
func (r *taskStopRunner) Restart([]string) { r.unrelated++ }
func (r *taskStopRunner) WriteUserTurn(context.Context, string, []byte) error {
	r.unrelated++
	return nil
}

type taskStopUnavailableRunner struct{ stubRunner }

func (taskStopUnavailableRunner) State() sessions.State { return sessions.State{ChildPID: 42} }

func TestBoundBackgroundTaskStopper(t *testing.T) {
	const convID = "27960000-0000-4000-8000-000000000001"
	const otherID = "27960000-0000-4000-8000-000000000002"
	const unknownID = "27960000-0000-4000-8000-000000000003"
	for _, tc := range []struct {
		name, conv, binding, harness, task                            string
		reported, noMethod, noChild, truncated, empty, absent, refuse bool
		want                                                          relay.BackgroundTaskStopOutcome
	}{
		{name: "empty conversation", want: relay.BackgroundTaskStopCannotActOnConversation},
		{name: "noncanonical", conv: "hostile-conversation", want: relay.BackgroundTaskStopCannotActOnConversation},
		{name: "unknown", conv: unknownID, want: relay.BackgroundTaskStopCannotActOnConversation},
		{name: "unbound", conv: convID, binding: "empty", want: relay.BackgroundTaskStopCannotActOnConversation},
		{name: "session missing", conv: convID, binding: "missing", want: relay.BackgroundTaskStopCannotActOnConversation},
		{name: "codex", conv: convID, harness: "codex", reported: true, want: relay.BackgroundTaskStopRefused},
		{name: "no stop method", conv: convID, noMethod: true, want: relay.BackgroundTaskStopRefused},
		{name: "no live child", conv: convID, reported: true, noChild: true, want: relay.BackgroundTaskStopRefused},
		{name: "unreported", conv: convID, want: relay.BackgroundTaskStopRefused},
		{name: "empty roster", conv: convID, reported: true, empty: true, want: relay.BackgroundTaskStopRefused},
		{name: "absent task", conv: convID, reported: true, absent: true, want: relay.BackgroundTaskStopRefused},
		{name: "truncated", conv: convID, reported: true, truncated: true, want: relay.BackgroundTaskStopRefused},
		{name: "full length unmarked", conv: convID, reported: true, task: strings.Repeat("x", 256), want: relay.BackgroundTaskStopAccepted},
		{name: "accepted", conv: convID, reported: true, want: relay.BackgroundTaskStopAccepted},
		{name: "child refusal or error", conv: convID, reported: true, refuse: true, want: relay.BackgroundTaskStopRefused},
	} {
		t.Run(tc.name, func(t *testing.T) {
			task := tc.task
			if task == "" {
				task = "ZZ_HELD_TASK_ZZ"
			}
			target := &taskStopRunner{pid: 42, reported: tc.reported, accept: !tc.refuse, roster: turnevent.BackgroundTaskRoster{Tasks: []turnevent.BackgroundTask{{TaskID: task}}}}
			if tc.noChild {
				target.pid = 0
			}
			if tc.empty {
				target.roster.Tasks = nil
			}
			if tc.absent {
				target.roster.Tasks[0].TaskID = "ZZ_DIFFERENT_TASK_ZZ"
			}
			if tc.truncated {
				target.roster.Tasks[0].TruncatedFields = []string{"task_id"}
			}
			other := &taskStopRunner{pid: 43, reported: true, accept: true, roster: target.roster}
			made := 0
			pool, err := sessions.New(sessions.Config{Bootstrap: sessions.SessionConfig{ClaudeBin: os.Args[0]}, RegistryPath: filepath.Join(t.TempDir(), "sessions.json"), RunnerFactory: func(sessions.RunnerConfig) (sessions.Runner, error) {
				made++
				if made == 2 {
					if tc.noMethod {
						return taskStopUnavailableRunner{}, nil
					}
					return target, nil
				}
				return other, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			sid, err := pool.MintAs("target", "", tc.harness)
			if err != nil && !errors.Is(err, sessions.ErrPoolNotRunning) {
				t.Fatal(err)
			}
			binding := string(sid)
			if tc.binding == "empty" {
				binding = ""
			}
			if tc.binding == "missing" {
				binding = "missing-session"
			}
			reg := &conversations.Registry{}
			reg.Create(conversations.Conversation{ID: convID, CurrentSessionID: binding})
			reg.Create(conversations.Conversation{ID: otherID, CurrentSessionID: string(pool.BootstrapID())})
			got := boundBackgroundTaskStopper(reg, pool).StopBackgroundTask(context.Background(), tc.conv, task)
			if got != tc.want {
				t.Fatalf("outcome = %v, want %v", got, tc.want)
			}
			wantCalls := 0
			if tc.want == relay.BackgroundTaskStopAccepted || tc.refuse {
				wantCalls = 1
			}
			if target.calls != wantCalls || other.calls != 0 || target.unrelated != 0 || other.unrelated != 0 {
				t.Fatalf("calls target/other/unrelated = %d/%d/%d/%d", target.calls, other.calls, target.unrelated, other.unrelated)
			}
			if wantCalls == 1 && (target.calledID != task || target.deadline.IsZero() || target.deadline.After(time.Now().Add(30*time.Second))) {
				t.Fatal("wrong id or unbounded context")
			}
			if conv, _ := reg.Get(convID); conv.CurrentSessionID != binding {
				t.Fatal("binding changed")
			}
		})
	}
}

func TestBoundBackgroundTaskStopperCancellation(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(map[bool]string{false: "worker cancellation", true: "earlier deadline"}[timeout], func(t *testing.T) {
			r := &taskStopRunner{pid: 42, reported: true, wait: true, entered: make(chan struct{}), roster: turnevent.BackgroundTaskRoster{Tasks: []turnevent.BackgroundTask{{TaskID: "held"}}}}
			pool, err := sessions.New(sessions.Config{Bootstrap: sessions.SessionConfig{ClaudeBin: os.Args[0]}, RunnerFactory: func(sessions.RunnerConfig) (sessions.Runner, error) { return r, nil }})
			if err != nil {
				t.Fatal(err)
			}
			const id = "27960000-0000-4000-8000-000000000004"
			reg := &conversations.Registry{}
			reg.Create(conversations.Conversation{ID: id, CurrentSessionID: string(pool.BootstrapID())})
			ctx, cancel := context.WithCancel(context.Background())
			if timeout {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 40*time.Millisecond)
			}
			defer cancel()
			done := make(chan relay.BackgroundTaskStopOutcome, 1)
			go func() { done <- boundBackgroundTaskStopper(reg, pool).StopBackgroundTask(ctx, id, "held") }()
			select {
			case <-r.entered:
			case <-time.After(time.Second):
				t.Fatal("stop never called")
			}
			if !timeout {
				cancel()
			}
			select {
			case got := <-done:
				if got != relay.BackgroundTaskStopRefused {
					t.Fatal("cancel accepted")
				}
			case <-time.After(time.Second):
				t.Fatal("silent child wait unbounded")
			}
			if r.calls != 1 {
				t.Fatal("multiple stops")
			}
			if deadline, ok := ctx.Deadline(); ok && r.deadline.After(deadline) {
				t.Fatal("extended parent deadline")
			}
		})
	}
}

func TestStreamRunnerStopTaskWithoutChild(t *testing.T) {
	if (streamRunner{}).StopTask(context.Background(), "held") {
		t.Fatal("nil child accepted")
	}
}

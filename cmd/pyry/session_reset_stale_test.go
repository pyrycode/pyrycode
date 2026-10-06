package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/streamsup"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

type resetStateRunner struct {
	*resetRunner
	live bool
}

func (r resetStateRunner) State() sessions.State {
	if r.live {
		return sessions.State{ChildPID: 4242}
	}
	return sessions.State{}
}

func TestResetThenRotate_FailedHandoffIsStale(t *testing.T) {
	cases := []struct{ stage, event string }{
		{"resume", "activation_failed"}, {"readiness", "readiness_failed"},
		{"idle", "not_idle"}, {"delivery", "write_failed"},
		{"completion", "deadline"}, {"terminal", "terminal_failed"},
		{"admission", "reply_unusable"}, {"persistence", "note_write_failed"},
	}
	for _, live := range []bool{false, true} {
		for _, tc := range cases {
			if live && (tc.stage == "resume" || tc.stage == "readiness") {
				continue
			}
			t.Run(tc.stage+map[bool]string{true: "/live", false: "/dormant"}[live], func(t *testing.T) {
				if tc.stage == "persistence" && os.Geteuid() == 0 {
					t.Skip("requires directory permission enforcement")
				}
				var f *resetFixture
				tail := newResettingTail(t, func(_ *resettingTail) *resetFixture {
					f = newResetFixture(t, resetOptions{deadline: 35 * time.Millisecond, startBusy: tc.stage == "idle"})
					return f
				}, nil)
				root := t.TempDir()
				pool, err := sessions.New(sessions.Config{
					Bootstrap:    sessions.SessionConfig{ClaudeBin: os.Args[0]},
					RegistryPath: filepath.Join(root, "sessions.json"), Logger: quietLogger(),
					RunnerFactory: func(sessions.RunnerConfig) (sessions.Runner, error) { return stubRunner{}, nil },
				})
				if err != nil {
					t.Fatal(err)
				}
				runPoolReady(t, pool)
				oldID, err := pool.Mint(resetConvA, "")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := pool.WriteHandoffNote(resetConvA, resetPreviousNote); err != nil {
					t.Fatal(err)
				}
				f.reset.notes = pool
				resolve := f.reset.resolve
				f.reset.resolve = func(id string) (resetTarget, bool) {
					target, ok := resolve(id)
					target.runner = resetStateRunner{resetRunner: f.runner.(*resetRunner), live: live}
					target.activate = func(context.Context) error {
						if tc.stage == "resume" {
							return errors.New("SENSITIVE-ERROR /private/path")
						}
						return nil
					}
					target.write = func(context.Context, string, []byte) error {
						switch tc.stage {
						case "readiness":
							return streamsup.ErrNoLiveChild
						case "delivery":
							return errors.New("SENSITIVE-ERROR /private/path")
						case "completion":
							return nil
						case "terminal":
							f.answer(resetReplyText, false)
							f.capture.Sink(turnevent.TurnEnd{IsError: true})
						case "admission":
							f.answer("\xff", true)
						default:
							f.answer(resetReplyText, true)
						}
						return nil
					}
					return target, ok
				}
				if tc.stage == "persistence" {
					dir := filepath.Join(root, "handoff-notes")
					if err := os.Chmod(dir, 0o500); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
				}
				var successor sessions.SessionID
				tail.starter.rotate = func(id sessions.SessionID) (sessions.SessionID, error) {
					tail.rotateCalls++
					var err error
					successor, err = pool.RotateForNewSession(id)
					return successor, err
				}
				completed := false
				started := time.Now()
				tail.starter.resetThenRotate(func() {}, func(err error) {
					if err != nil {
						t.Error(err)
					}
					completed = true
				}, tail.runner, oldID, resetConvA, "", false)
				if !completed || tail.rotateCalls != 1 || tail.runner.restartCount() != 1 || time.Since(started) > time.Second {
					t.Fatal("failed wrap-up did not complete bounded rotation")
				}
				assertEdges(t, tail.bcast.recorded(),
					edge{active: true, phase: protocol.ResetPhaseWrappingUp, handoff: protocol.ResetHandoffPending},
					edge{active: true, phase: protocol.ResetPhaseRestarting, handoff: protocol.ResetHandoffSkipped},
					edge{active: false, phase: "", handoff: ""})
				raw, err := os.ReadFile(filepath.Join(root, "handoff-notes", resetConvA+".txt"))
				if err != nil || string(raw) != resetPreviousNote {
					t.Fatalf("older bytes changed: %q, %v", raw, err)
				}
				prompt, err := os.ReadFile(filepath.Join(root, "session-prompts", string(oldID)+".txt"))
				text := string(prompt)
				warning := "This handoff note is stale: the latest reset did not produce a fresh handoff, so it may omit recent work."
				fence, _ := sessions.FencedHandoffNote(resetPreviousNote)
				if err != nil || !strings.Contains(text, warning) || !strings.Contains(text, fence) || strings.Index(text, warning) > strings.Index(text, fence) || strings.Contains(text, resetReplyText) {
					t.Fatalf("successor lacks correctly framed stale context: %q, %v", text, err)
				}
				logs := f.logs.String()
				if !strings.Contains(logs, "level=WARN") || !strings.Contains(logs, "event=reset.wrapup."+tc.event) {
					t.Fatalf("missing failure classification: %s", logs)
				}
				for _, secret := range []string{resetPreviousNote, resetReplyText, "SENSITIVE-ERROR", "/private/path", root} {
					if strings.Contains(logs, secret) {
						t.Fatalf("failure log leaked %q", secret)
					}
				}
				if tc.stage == "terminal" {
					f.reset.resolve = func(string) (resetTarget, bool) {
						return resetTarget{runner: f.runner, write: func(context.Context, string, []byte) error {
							f.answer(resetReplyText, true)
							return nil
						}}, true
					}
					tail.starter.resetThenRotate(func() {}, nil, tail.runner, successor, resetConvA, "", false)
					raw, err := os.ReadFile(filepath.Join(root, "session-prompts", string(oldID)+".txt"))
					if err != nil || !strings.Contains(string(raw), resetReplyText) || strings.Contains(string(raw), warning) {
						t.Fatalf("successful wrap-up did not clear stale warning: %q, %v", raw, err)
					}
					if frames := tail.bcast.recorded(); frames[len(frames)-2].payload.Handoff != protocol.ResetHandoffWritten {
						t.Fatal("successful recovery did not report written")
					}
				}

			})
		}
	}
}

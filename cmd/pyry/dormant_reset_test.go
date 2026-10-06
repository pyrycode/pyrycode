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
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/streamsup"
)

func TestConversationReset_DormantReadiness(t *testing.T) {
	f := newResetFixture(t, resetOptions{previousNote: resetPreviousNote})
	resolve := f.reset.resolve
	attempts := 0
	f.reset.resolve = func(id string) (resetTarget, bool) {
		target, ok := resolve(id)
		write := target.write
		target.write = func(ctx context.Context, conv string, payload []byte) error {
			attempts++
			if attempts < 3 {
				return streamsup.ErrNoLiveChild
			}
			if err := write(ctx, conv, payload); err != nil {
				return err
			}
			f.answer(resetReplyText, true)
			return nil
		}
		return target, ok
	}
	if !f.reset.wrapUp(resetConvA) {
		t.Fatal("dormant wrap-up did not wait for stream readiness and store the reply")
	}
	if attempts != 3 {
		t.Fatalf("delivery attempts = %d, want 3", attempts)
	}
	if !strings.Contains(strings.Join(f.steps.seen(), ","), "write-turn") {
		t.Fatal("no wrap-up delivered")
	}
}

func TestActiveSessionStarter_RetainedDormantPoolWritesFreshNote(t *testing.T) {
	for _, named := range []bool{false, true} {
		t.Run(map[bool]string{false: "cursor", true: "named"}[named], func(t *testing.T) {
			f := newResetFixture(t, resetOptions{answerOnWrite: answerWith(resetReplyText)})
			root := t.TempDir()
			reg := &conversations.Registry{}
			runners := map[string]*switchHandoverRunner{}
			pool, err := sessions.New(sessions.Config{
				Bootstrap:    sessions.SessionConfig{ClaudeBin: os.Args[0]},
				RegistryPath: filepath.Join(root, "sessions.json"), Logger: f.reset.log,
				ConversationsRegistry: reg, ConversationsRegistryPath: filepath.Join(root, "conversations.json"),
				RunnerFactory: func(cfg sessions.RunnerConfig) (sessions.Runner, error) {
					r := &switchHandoverRunner{fixture: f, args: cfg.ClaudeArgs, spawned: make(chan string, 2)}
					runners[cfg.SessionID] = r
					return r, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx := runPoolReady(t, pool)
			oldID, err := pool.MintWith(resetConvA, "", "claude", sessions.SessionSettings{})
			if err != nil {
				t.Fatal(err)
			}
			reg.Create(conversations.Conversation{ID: resetConvA, CurrentSessionID: string(oldID)})
			if err := pool.Activate(ctx, oldID); err != nil {
				t.Fatal(err)
			}
			select {
			case <-runners[string(oldID)].spawned:
			case <-time.After(time.Second):
				t.Fatal("predecessor never ran")
			}
			sess, err := pool.Lookup(oldID)
			if err != nil {
				t.Fatal(err)
			}
			if err := sess.Evict(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.WriteHandoffNote(resetConvA, resetPreviousNote); err != nil {
				t.Fatal(err)
			}
			reset := newConversationReset(ctx, reg, pool, f.busy, nil, time.Second, f.reset.log)
			a := activeSessionStarter{
				currentConv: func() string { return resetConvA }, everRan: pool.EverActivated,
				resolveBound: func(id string) (sessions.Runner, sessions.SessionID, string, bool) {
					sess, old, cwd, ok := resolveBoundSession(reg, pool, id)
					if !ok {
						return nil, "", "", false
					}
					return sess.Runner(), old, cwd, true
				},
				rotate: pool.RotateForNewSession, reset: reset, log: f.reset.log,
			}
			outcome := newLateOutcome()
			id := ""
			if named {
				id = resetConvA
			}
			a.StartNewSessionLate(id, outcome.report)
			if err := outcome.await(t); err != nil {
				t.Fatal(err)
			}
			select {
			case <-runners[string(oldID)].spawned:
			case <-time.After(time.Second):
				t.Fatal("retained dormant predecessor was not activated")
			}
			note, err := pool.HandoffNote(resetConvA)
			if err != nil || note != resetReplyText {
				t.Fatalf("fresh note = %q, error = %v", note, err)
			}
			prompt, err := os.ReadFile(filepath.Join(root, "session-prompts", string(oldID)+".txt"))
			if err != nil || !strings.Contains(string(prompt), resetReplyText) || strings.Contains(string(prompt), resetPreviousNote) {
				t.Fatalf("successor prompt did not replace the older note: %v", err)
			}
		})
	}
}

func TestActiveSessionStarter_UsedDormantWrapsUp(t *testing.T) {
	for _, named := range []bool{false, true} {
		t.Run(map[bool]string{false: "cursor", true: "named"}[named], func(t *testing.T) {
			runner := newAsyncRunner(0)
			probe := &resetProbe{}
			a := probe.starter(runner, &safeLog{})
			a.everRan = func(sessions.SessionID) bool { return true }
			id := ""
			if named {
				id = starterConvB
			}
			outcome := newLateOutcome()
			a.StartNewSessionLate(id, outcome.report)
			if err := outcome.await(t); err != nil {
				t.Fatal(err)
			}
			runner.awaitRotation(t)
			if len(probe.seen()) != 1 {
				t.Fatal("used dormant session rotated without wrap-up")
			}
		})
	}
}

func TestConversationReset_ReadinessBoundAndHandover(t *testing.T) {
	for _, mode := range []string{"deadline", "cancelled", "handover", "write-error"} {
		t.Run(mode, func(t *testing.T) {
			f := newResetFixture(t, resetOptions{deadline: 40 * time.Millisecond})
			base, cancel := context.WithCancel(context.Background())
			defer cancel()
			f.reset.base = base
			if mode == "cancelled" {
				cancel()
			}
			resolve := f.reset.resolve
			attempts := 0
			f.reset.resolve = func(id string) (resetTarget, bool) {
				target, ok := resolve(id)
				target.write = func(context.Context, string, []byte) error {
					attempts++
					if mode == "write-error" {
						return errors.New("secret prompt /private/path")
					}
					return streamsup.ErrNoLiveChild
				}
				return target, ok
			}
			started := time.Now()
			if mode == "handover" {
				f.reset.wrapUpText(resetConvA)
			} else if f.reset.wrapUp(resetConvA) {
				t.Fatal("failed delivery stored a note")
			}
			if time.Since(started) > time.Second {
				t.Fatal("readiness did not respect deadline/cancellation")
			}
			if mode == "deadline" && attempts < 2 {
				t.Fatal("readiness refusal was not retried")
			}
			if (mode == "handover" || mode == "write-error") && attempts != 1 {
				t.Fatalf("non-retryable path attempted %d writes", attempts)
			}
			if strings.Contains(f.logs.String(), "secret prompt") || strings.Contains(f.logs.String(), "/private/path") {
				t.Fatal("content-bearing error leaked into logs")
			}
		})
	}
}

func TestConversationReset_ActivationSharesDeadline(t *testing.T) {
	for _, mode := range []string{"success", "activation-error", "activation-deadline", "reply-deadline", "handover"} {
		t.Run(mode, func(t *testing.T) {
			f := newResetFixture(t, resetOptions{deadline: 80 * time.Millisecond})
			resolve := f.reset.resolve
			var activated context.Context
			writes := 0
			f.reset.resolve = func(id string) (resetTarget, bool) {
				target, ok := resolve(id)
				target.activate = func(ctx context.Context) error {
					activated = ctx
					f.steps.record("activate-old")
					if mode == "activation-error" {
						return errors.New("secret prompt /private/path")
					}
					if mode == "activation-deadline" {
						<-ctx.Done()
						return ctx.Err()
					}
					return nil
				}
				target.write = func(ctx context.Context, _ string, _ []byte) error {
					writes++
					if mode != "handover" && ctx != activated {
						t.Error("activation and delivery received different deadline contexts")
					}
					if mode != "reply-deadline" {
						f.answer(resetReplyText, true)
					}
					return nil
				}
				return target, ok
			}
			if mode == "handover" {
				_, ended, _ := f.reset.wrapUpText(resetConvA)
				if !ended || activated != nil {
					t.Fatal("agent handover activated a dormant target or lost its reply")
				}
				return
			}
			wrote := f.reset.wrapUp(resetConvA)
			if activated == nil || wrote != (mode == "success") {
				t.Fatalf("activated=%v wrote=%v for %s", activated != nil, wrote, mode)
			}
			if strings.HasPrefix(mode, "activation-") && writes != 0 {
				t.Fatal("activation failure still wrote a wrap-up turn")
			}
			if mode == "reply-deadline" && !errors.Is(activated.Err(), context.DeadlineExceeded) {
				t.Fatal("reply did not consume the activation deadline")
			}
			logs := f.logs.String()
			if !strings.Contains(logs, "level=INFO") || !strings.Contains(logs, "reset.wrapup.resume") {
				t.Fatal("dormant resume attempt lacks an Info record")
			}
			if strings.Contains(logs, "secret prompt") || strings.Contains(logs, "/private/path") {
				t.Fatal("activation error leaked into logs")
			}
		})
	}
}

package main

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/streamsup"
)

func TestReplyFallbackTimingBudget(t *testing.T) {
	t.Parallel()
	const oldDeadline = 9800 * time.Millisecond
	for _, tc := range []struct {
		name, resultDelay, exitDelay string
		lateResult                   bool
	}{
		{"result before old deadline", "", "11s", false},
		{"result after old deadline", "11s", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			parent, cancel := context.WithCancel(context.Background())
			env := []string{"PYRY_REPLY_HELPER=1", "PYRY_REPLY_USER=private-user-sentinel",
				"PYRY_REPLY_ASSISTANT=private-assistant-sentinel", "PYRY_REPLY_OUTPUT=private-generated-sentinel",
				"PYRY_REPLY_RESULT_DELAY=" + tc.resultDelay, "PYRY_REPLY_EXIT_DELAY=" + tc.exitDelay}
			calls := 0
			a := testStartReplyFallback(t, parent, cancel, env, false, nil, func(f *replyFallback) {
				command := f.command
				f.command = func(ctx context.Context, binary string, args ...string) *exec.Cmd {
					calls++
					return command(ctx, binary, args...)
				}
			})
			a.awaitReady(t)
			a.awaitReturn(t, 30*time.Second)
			a.awaitWait(t)
			if a.err != nil || a.text != "private-generated-sentinel" || calls != 1 {
				t.Fatal("one successful delayed attempt did not return the valid reply")
			}
			record := a.record(t)
			for key, want := range map[string]any{
				"pid": float64(a.child.Process.Pid), "wait_completed": true, "wait_ok": true,
				"exit_code": float64(0), "fallback_deadline": false, "own_deadline_elapsed": false,
				"group_cancel_requested": false, "stdout_result_ok": true, "stdout_text_ok": true,
				"progress_event": "result",
			} {
				if record[key] != want {
					t.Fatalf("incorrect timing/completion predicate %s", key)
				}
			}
			elapsed, ok := record["attempt_ms"].(float64)
			age, ageOK := record["progress_age_ms"].(float64)
			if !ok || !ageOK || elapsed <= float64(oldDeadline.Milliseconds()) || elapsed >= 30000 {
				t.Fatal("completion did not cross the old deadline inside the total budget")
			}
			resultAt := elapsed - age
			if (resultAt > float64(oldDeadline.Milliseconds())) != tc.lateResult {
				t.Fatal("result receipt did not occur on the intended side of the old deadline")
			}
		})
	}
}

func TestReplyFallbackLookupBudget(t *testing.T) {
	t.Parallel()
	lookupDone := make(chan struct{})
	calls := 0
	f := replyFallback{
		account: func(ctx context.Context) (string, streamsup.AccountTokenFailure, error) {
			defer close(lookupDone)
			<-ctx.Done()
			return "", streamsup.AccountTokenReadFailure, ctx.Err()
		},
		command: func(context.Context, string, ...string) *exec.Cmd { calls++; return nil },
	}
	started := time.Now()
	text, err := f.run(context.Background(), "user", "assistant")
	elapsed := time.Since(started)
	<-lookupDone
	if text != "" || !errors.Is(err, errReplyFallback) || calls != 0 || elapsed < 29*time.Second || elapsed >= 30*time.Second {
		t.Fatal("credential lookup did not share the finite total budget without a launch")
	}
}

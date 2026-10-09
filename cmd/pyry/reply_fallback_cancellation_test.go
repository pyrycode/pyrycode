package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/streamsup"
)

// testReplyFallbackContext permits deadline expiry to follow child readiness.
// Its Done and Err agree, including when WithTimeout derives the attempt context.
type testReplyFallbackContext struct{ context.Context }

// Hide the wrapped cancelCtx so derived contexts propagate this context's Err.
func (c testReplyFallbackContext) Value(any) any { return nil }

func (c testReplyFallbackContext) Err() error {
	if c.Context.Err() != nil {
		return context.Cause(c.Context)
	}
	return nil
}

type testReplyFallbackAttempt struct {
	f         replyFallback
	child     *exec.Cmd
	logs      bytes.Buffer
	ready     chan error
	returned  chan struct{}
	waitStart chan struct{}
	waitDone  chan struct{}
	release   chan struct{}
	once      sync.Once
	text      string
	err       error
}

func testStartReplyFallback(t *testing.T, parent context.Context, cancel func(), env []string, held bool, grace func(time.Duration) <-chan time.Time) *testReplyFallbackAttempt {
	t.Helper()
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	a := &testReplyFallbackAttempt{ready: make(chan error, 1), returned: make(chan struct{}), waitStart: make(chan struct{}), waitDone: make(chan struct{}), release: make(chan struct{})}
	a.f = replyFallback{logger: slog.New(slog.NewJSONHandler(&a.logs, nil)),
		account: func(context.Context) (string, streamsup.AccountTokenFailure, error) { return "selected", "", nil },
		command: func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
			a.child = replyFallbackTestCommand(ctx, append(env, "PYRY_REPLY_READY_FD=3"))
			a.child.ExtraFiles = []*os.File{pw}
			return a.child
		},
		wait: func(cmd *exec.Cmd) error {
			defer close(a.waitDone)
			close(a.waitStart)
			_ = pw.Close() // The started child owns the remaining writer.
			<-a.release
			return cmd.Wait()
		},
		waitGrace: grace,
	}
	if !held {
		a.unhold()
	}
	go func() {
		_, err := io.ReadFull(pr, make([]byte, 1))
		_ = pr.Close()
		a.ready <- err
	}()
	t.Cleanup(func() {
		cancel()
		a.unhold()
		_ = pr.Close()
		_ = pw.Close()
		select {
		case <-a.waitStart:
			select {
			case <-a.waitDone:
			default:
				_ = syscall.Kill(-a.child.Process.Pid, syscall.SIGKILL) // Best effort on assertion failure.
			}
		default:
		}
		<-a.returned
		select {
		case <-a.waitStart:
			<-a.waitDone
		default: // Start failed, so no Wait worker exists.
		}
		<-a.ready
	})
	go func() {
		defer close(a.returned)
		defer pw.Close()
		a.text, a.err = a.f.run(parent, "private-user-sentinel", "private-assistant-sentinel")
	}()
	return a
}

func (a *testReplyFallbackAttempt) unhold() { a.once.Do(func() { close(a.release) }) }

func (a *testReplyFallbackAttempt) awaitReady(t *testing.T) {
	t.Helper()
	select {
	case err := <-a.ready:
		a.ready <- err // Cleanup also joins the reader, including on failure.
		if err != nil {
			t.Fatal("child readiness failed")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("child not ready")
	}
}

func (a *testReplyFallbackAttempt) awaitReturn(t *testing.T, bound time.Duration) {
	t.Helper()
	select {
	case <-a.returned:
	case <-time.After(bound):
		t.Fatal("fallback exceeded return bound")
	}
}

func (a *testReplyFallbackAttempt) awaitWait(t *testing.T) {
	t.Helper()
	select {
	case <-a.waitDone:
	case <-time.After(5 * time.Second):
		t.Fatal("child completion not received after gate release")
	}
}

func (a *testReplyFallbackAttempt) record(t *testing.T) map[string]any {
	t.Helper()
	var record map[string]any
	if json.Unmarshal(a.logs.Bytes(), &record) != nil || record["msg"] != "reply_fallback.lifecycle" || strings.Contains(a.logs.String(), "private-") || strings.Contains(a.logs.String(), "selected") {
		t.Fatal("missing, duplicate or sensitive daemon evidence")
	}
	return record
}

func testReplyFallbackUnknownCompletion(t *testing.T, record map[string]any) {
	t.Helper()
	for _, key := range []string{"exit_code", "exit_signal", "stdout_bytes", "stdout_cap_exceeded", "stdout_utf8_ok", "stdout_json_ok", "stdout_result_ok", "stdout_text_ok", "result_bytes", "wait_ok", "wait_delay"} {
		if record[key] != "unknown" {
			t.Fatalf("invented %s without Wait", key)
		}
	}
}

func TestReplyFallbackCancellationEvidence(t *testing.T) {
	t.Parallel()
	for _, deadline := range []bool{false, true} {
		name := "parent cancel"
		if deadline {
			name = "parent deadline"
		}
		for _, received := range []bool{true, false} {
			outcome := "withheld"
			if received {
				outcome = "received"
			}
			t.Run(name+"/"+outcome, func(t *testing.T) {
				t.Parallel()
				base, cancel := context.WithCancelCause(context.Background())
				parent := testReplyFallbackContext{base}
				graceEntered := make(chan time.Duration, 1)
				release := make(chan struct{})
				var once sync.Once
				grace := func(d time.Duration) <-chan time.Time {
					graceEntered <- d
					if received {
						once.Do(func() { close(release) })
						return make(chan time.Time) // Receipt wins independently of scheduler delay.
					}
					return time.After(d) // Withheld completion exercises the production grace.
				}
				env := []string{"PYRY_REPLY_HELPER=1", "PYRY_REPLY_USER=private-user-sentinel", "PYRY_REPLY_ASSISTANT=private-assistant-sentinel", "PYRY_REPLY_HANG=1"}
				a := testStartReplyFallback(t, parent, func() { cancel(context.Canceled) }, env, true, grace)
				workerDone := make(chan struct{})
				go func() { defer close(workerDone); <-release; a.unhold() }()
				t.Cleanup(func() { once.Do(func() { close(release) }); <-workerDone })
				a.awaitReady(t)
				cause := context.Canceled
				if deadline {
					cause = context.DeadlineExceeded
				}
				cancel(cause)
				a.awaitReturn(t, 2*time.Second)
				if a.text != "" || !errors.Is(a.err, errReplyFallback) {
					t.Fatal("cancellation execution contract changed")
				}
				select {
				case d := <-graceEntered:
					if d != 100*time.Millisecond {
						t.Fatalf("cancellation grace=%v", d)
					}
				default:
					t.Fatal("cancellation grace not entered")
				}
				record := a.record(t)
				for key, want := range map[string]bool{"parent_canceled": !deadline, "parent_deadline": deadline, "fallback_canceled": !deadline, "fallback_deadline": deadline, "own_deadline_elapsed": false, "group_cancel_requested": true, "cancel_snapshot_observed": true, "wait_completed": received, "exit_observed": received, "output_observed": received} {
					if record[key] != want {
						t.Fatalf("%s=%v want=%v", key, record[key], want)
					}
				}
				if received {
					if record["exit_code"] != float64(-1) || record["exit_signal"] != float64(syscall.SIGKILL) || record["wait_ok"] != false || record["wait_delay"] != false {
						t.Fatal("incorrect received exit")
					}
				} else {
					testReplyFallbackUnknownCompletion(t, record)
					select {
					case <-a.waitDone:
						t.Fatal("held Wait completed before release")
					default:
					}
				}
				a.unhold()
				a.awaitWait(t)
				status := a.child.ProcessState.Sys().(syscall.WaitStatus)
				if !status.Signaled() || status.Signal() != syscall.SIGKILL {
					t.Fatal("child did not terminate")
				}
			})
		}
	}
}

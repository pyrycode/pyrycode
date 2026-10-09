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
	f           replyFallback
	child       *exec.Cmd
	logs        bytes.Buffer
	ready       chan error
	returned    chan struct{}
	waitStart   chan struct{}
	waitDone    chan struct{}
	joiningWait chan struct{}
	release     chan struct{}
	once        sync.Once
	text        string
	err         error
}

func testStartReplyFallback(t *testing.T, parent context.Context, cancel func(), env []string, held bool, grace func(time.Duration) <-chan time.Time, configure ...func(*replyFallback)) *testReplyFallbackAttempt {
	t.Helper()
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	a := &testReplyFallbackAttempt{ready: make(chan error, 1), returned: make(chan struct{}), waitStart: make(chan struct{}), waitDone: make(chan struct{}), joiningWait: make(chan struct{}), release: make(chan struct{})}
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
	for _, configure := range configure {
		configure(&a.f)
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
		<-a.returned
		// Start sets Process before run returns, regardless of Wait scheduling.
		if a.child != nil && a.child.Process != nil {
			select {
			case <-a.waitDone:
			default:
				_ = syscall.Kill(-a.child.Process.Pid, syscall.SIGKILL) // Best effort on assertion failure.
			}
			close(a.joiningWait)
			<-a.waitStart
			<-a.waitDone
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

func TestReplyFallbackAttemptCleanupDelayedWait(t *testing.T) {
	t.Parallel()
	schedule, cleanupDone, controllerDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	joined := make(chan bool, 1)
	var a *testReplyFallbackAttempt
	t.Run("attempt", func(t *testing.T) {
		// This runs after the attempt's cleanup, including after a fatal assertion.
		t.Cleanup(func() { close(cleanupDone) })
		parent, cancel := context.WithCancel(context.Background())
		env := []string{"PYRY_REPLY_HELPER=1", "PYRY_REPLY_USER=private-user-sentinel", "PYRY_REPLY_ASSISTANT=private-assistant-sentinel", "PYRY_REPLY_HANG=1"}
		a = testStartReplyFallback(t, parent, cancel, env, true, nil, func(f *replyFallback) {
			wait := f.wait
			f.wait = func(cmd *exec.Cmd) error {
				<-schedule // Delay the worker before it announces its start.
				return wait(cmd)
			}
		})
		go func() {
			defer close(controllerDone)
			select {
			case <-a.joiningWait:
				joined <- true
			case <-cleanupDone:
				joined <- false
			}
			close(schedule) // Release the worker even when cleanup skipped its join.
		}()
		a.awaitReady(t)
		cancel()
		a.awaitReturn(t, 2*time.Second)
		select {
		case <-a.waitStart:
			t.Fatal("delayed worker started before cleanup")
		default:
		}
		if a.text != "" || !errors.Is(a.err, errReplyFallback) || a.record(t)["wait_completed"] != false {
			t.Fatal("delayed worker changed bounded cancellation evidence")
		}
		// Leave Wait held, as it would be when a snapshot assertion calls Fatal.
	})
	if a == nil {
		return // Pipe setup failed before any worker was launched.
	}
	if a.child != nil && a.child.Process != nil {
		select {
		case <-a.waitDone:
		default:
			t.Error("attempt cleanup returned with Wait still outstanding")
		}
	}
	<-controllerDone
	if !<-joined {
		t.Error("attempt cleanup returned before joining delayed Wait worker")
	}
	if a.child != nil && a.child.Process != nil {
		a.awaitWait(t) // Also reap the child when testing a broken cleanup.
		status := a.child.ProcessState.Sys().(syscall.WaitStatus)
		if !status.Signaled() || status.Signal() != syscall.SIGKILL {
			t.Fatal("delayed child did not terminate")
		}
	}
}

func TestReplyFallbackAttemptCleanupFailedStart(t *testing.T) {
	t.Parallel()
	missing := t.TempDir() + "/missing-helper"
	var a *testReplyFallbackAttempt
	t.Run("attempt", func(t *testing.T) {
		parent, cancel := context.WithCancel(context.Background())
		a = testStartReplyFallback(t, parent, cancel, nil, true, nil, func(f *replyFallback) {
			command := f.command
			f.command = func(ctx context.Context, binary string, args ...string) *exec.Cmd {
				child := command(ctx, binary, args...)
				child.Path = missing
				return child
			}
		})
		a.awaitReturn(t, 2*time.Second)
		if a.text != "" || !errors.Is(a.err, errReplyFallback) || a.child == nil || a.child.Process != nil || a.logs.Len() != 0 {
			t.Fatal("failed Start changed execution contract")
		}
	})
	if a == nil {
		return
	}
	for _, ch := range []chan struct{}{a.joiningWait, a.waitStart, a.waitDone} {
		select {
		case <-ch:
			t.Fatal("failed Start attempted to join a nonexistent Wait worker")
		default:
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

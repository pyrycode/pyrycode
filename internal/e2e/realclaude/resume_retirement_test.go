//go:build e2e_realclaude

package realclaude

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/claudetape"
)

func testResumeReceiptPath(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "controls")
	if err := claudetape.NewCheckpointDirectory(dir); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "bootstrap.receipt")
}

func TestResumePredecessorHelperProcess(t *testing.T) {
	if os.Getenv("PYRY_RESUME_PREDECESSOR_HELPER") == "1" {
		if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
			t.Fatal(err)
		}
	}
}

func TestResumePredecessorWaitsForRetirement(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.Command(os.Args[0], "-test.run=^TestResumePredecessorHelperProcess$")
	cmd.Env = append(os.Environ(), "PYRY_RESUME_PREDECESSOR_HELPER=1")
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		_ = input.Close()
		if !waited {
			if err := cmd.Wait(); err != nil {
				t.Error(err)
			}
		}
	}()
	path := testResumeReceiptPath(t)
	receipt := claudetape.LifecycleReceipt{Success: true, Frames: 1, OwnerPID: cmd.Process.Pid, BrokerPID: os.Getpid()}
	if err := claudetape.PublishLifecycleControl(path, receipt); err != nil {
		t.Fatal(err)
	}
	observed := make(chan error)
	advance := make(chan struct{})
	done := make(chan error, 1)
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		r, err := waitLifecycleResumePredecessor(path, cmd.Process.Pid, time.Now().Add(4*time.Second), func(pid int, signal syscall.Signal) error {
			if pid != cmd.Process.Pid || signal != 0 {
				return os.ErrInvalid
			}
			state := syscall.Kill(pid, signal)
			select {
			case observed <- state:
			case <-ctx.Done():
				return ctx.Err()
			}
			select {
			case <-advance:
				return state
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		if err == nil && r != receipt {
			err = errors.New("boundary changed the owned receipt")
		}
		done <- err
	}()
	defer func() { cancel(); <-joined }()
	checkObservation := func(want error) {
		t.Helper()
		select {
		case got := <-observed:
			if !errors.Is(got, want) {
				t.Fatalf("process observation = %v, want %v", got, want)
			}
		case err := <-done:
			t.Fatalf("snapshot boundary returned before owned retirement: %v", err)
		case <-ctx.Done():
			t.Fatal("snapshot boundary did not observe predecessor")
		}
	}
	advanceObservation := func() {
		t.Helper()
		select {
		case advance <- struct{}{}:
		case <-ctx.Done():
			t.Fatal("snapshot observation handshake timed out")
		}
	}
	checkObservation(nil)
	advanceObservation()
	// A second observation proves the first live reading stayed pending.
	checkObservation(nil)
	if err := input.Close(); err != nil {
		t.Fatal(err)
	}
	err = cmd.Wait()
	waited = true
	if err != nil {
		t.Fatal(err)
	}
	advanceObservation()
	checkObservation(syscall.ESRCH)
	advanceObservation()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("snapshot boundary did not complete after retirement")
	}
}

func TestResumePredecessorRejectsBoundary(t *testing.T) {
	t.Parallel()
	valid := claudetape.LifecycleReceipt{Success: true, Frames: 1, OwnerPID: os.Getpid(), BrokerPID: os.Getpid()}
	for _, tc := range []struct {
		name   string
		mutate func(*claudetape.LifecycleReceipt)
		state  error
	}{
		{"wrong-owner", func(r *claudetape.LifecycleReceipt) { r.OwnerPID++ }, syscall.ESRCH},
		{"failed", func(r *claudetape.LifecycleReceipt) { r.Success = false }, syscall.ESRCH},
		{"error", func(r *claudetape.LifecycleReceipt) { r.Error = "failed" }, syscall.ESRCH},
		{"empty", func(r *claudetape.LifecycleReceipt) { r.Frames = 0 }, syscall.ESRCH},
		{"unknown-process", nil, syscall.EPERM},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := valid
			if tc.mutate != nil {
				tc.mutate(&r)
			}
			path := testResumeReceiptPath(t)
			if err := claudetape.PublishLifecycleControl(path, r); err != nil {
				t.Fatal(err)
			}
			_, err := waitLifecycleResumePredecessor(path, valid.OwnerPID, time.Now().Add(time.Second), func(int, syscall.Signal) error {
				if tc.mutate != nil {
					t.Error("invalid receipt reached process observation")
				}
				return tc.state
			})
			if err == nil || errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("invalid boundary did not fail immediately: %v", err)
			}
		})
	}
}

func TestResumePredecessorDeadline(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"receipt-absent", "predecessor-present", "retirement-after-deadline"} {
		t.Run(name, func(t *testing.T) {
			path := testResumeReceiptPath(t)
			receiptPresent := name != "receipt-absent"
			if receiptPresent {
				r := claudetape.LifecycleReceipt{Success: true, Frames: 1, OwnerPID: os.Getpid()}
				if err := claudetape.PublishLifecycleControl(path, r); err != nil {
					t.Fatal(err)
				}
			}
			start := time.Now()
			deadline := start.Add(50 * time.Millisecond)
			_, err := waitLifecycleResumePredecessor(path, os.Getpid(), deadline, func(pid int, signal syscall.Signal) error {
				if !receiptPresent {
					t.Error("missing receipt reached process observation")
				}
				if name == "retirement-after-deadline" {
					timer := time.NewTimer(time.Until(deadline))
					defer timer.Stop()
					<-timer.C
					return syscall.ESRCH
				}
				return syscall.Kill(pid, signal)
			})
			if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
				t.Fatalf("boundary did not respect shared deadline: %v", err)
			}
		})
	}
}

package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/relay"
)

// TestFatalCause pins the exit classification that decides whether launchd
// restarts the daemon: a self-initiated fatal shutdown (relay persistent 4409)
// cancels the cause context WITH an error, so fatalCause returns it and
// runSupervisor exits non-zero; the operator paths (SIGTERM / `pyry stop`)
// carry the signal parent's cause or context.Canceled and stay down at exit 0.
func TestFatalCause(t *testing.T) {
	sentinel := errors.New("relay: server-id conflict (close 4409)")

	t.Run("no cause (context still live) returns nil", func(t *testing.T) {
		ctx, cancel := context.WithCancelCause(context.Background())
		defer cancel(nil)
		if got := fatalCause(ctx, context.Background()); got != nil {
			t.Fatalf("fatalCause on a live context = %v, want nil", got)
		}
	})

	t.Run("nil cause (operator stop) returns nil", func(t *testing.T) {
		ctx, cancel := context.WithCancelCause(context.Background())
		cancel(nil) // pyry stop: cancelCause(nil)
		if got := fatalCause(ctx, context.Background()); got != nil {
			t.Fatalf("fatalCause after cancel(nil) = %v, want nil", got)
		}
	})

	t.Run("explicit context.Canceled cause returns nil", func(t *testing.T) {
		ctx, cancel := context.WithCancelCause(context.Background())
		cancel(context.Canceled)
		if got := fatalCause(ctx, context.Background()); got != nil {
			t.Fatalf("fatalCause after cancel(context.Canceled) = %v, want nil", got)
		}
	})

	t.Run("ordinary parent cancel returns nil", func(t *testing.T) {
		parent, cancelParent := context.WithCancel(context.Background())
		ctx, cancel := context.WithCancelCause(parent)
		defer cancel(nil)
		cancelParent()
		if got := fatalCause(ctx, parent); got != nil {
			t.Fatalf("fatalCause after parent cancel = %v, want nil", got)
		}
	})

	t.Run("real error cause (fatal 4409) returns the cause", func(t *testing.T) {
		ctx, cancel := context.WithCancelCause(context.Background())
		cancel(sentinel) // relay self-initiated fatal shutdown
		got := fatalCause(ctx, context.Background())
		if !errors.Is(got, sentinel) {
			t.Fatalf("fatalCause after cancel(sentinel) = %v, want %v", got, sentinel)
		}
	})

	t.Run("wrapped error cause returns the wrapped chain", func(t *testing.T) {
		ctx, cancel := context.WithCancelCause(context.Background())
		wrapped := errors.New("relay: lifecycle: " + sentinel.Error())
		cancel(wrapped)
		if got := fatalCause(ctx, context.Background()); got == nil {
			t.Fatal("fatalCause after cancel(wrapped) = nil, want the wrapped error")
		}
	})
}

// Signals are process-wide, so isolate each NotifyContext case in a child.
func TestFatalCauseSignals(t *testing.T) {
	t.Parallel()
	for _, sig := range []string{"SIGTERM", "SIGINT"} {
		for _, fatalFirst := range []string{"0", "1"} {
			t.Run(sig+"/fatal-first="+fatalFirst, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFatalCauseSignalHelper$")
				cmd.Env = append(os.Environ(), "PYRY_TEST_STOP_SIGNAL="+sig, "PYRY_TEST_FATAL_FIRST="+fatalFirst)
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("signal helper: %v\n%s", err, out)
				}
			})
		}
	}
}

func TestFatalCauseSignalHelper(t *testing.T) {
	mode := os.Getenv("PYRY_TEST_STOP_SIGNAL")
	if mode == "" {
		return
	}
	sig := syscall.SIGTERM
	if mode == "SIGINT" {
		sig = syscall.SIGINT
	}
	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancelCause(sigCtx)
	defer cancel(nil)
	fatalFirst := os.Getenv("PYRY_TEST_FATAL_FIRST") == "1"
	if fatalFirst {
		cancel(relay.ErrServerIDConflict)
	}
	if err := syscall.Kill(os.Getpid(), sig); err != nil {
		t.Fatalf("send signal: %v", err)
	}
	select {
	case <-sigCtx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("signal parent did not cancel")
	}
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("daemon context did not cancel")
	}
	got := fatalCause(ctx, sigCtx)
	if fatalFirst {
		if !errors.Is(got, relay.ErrServerIDConflict) {
			t.Fatalf("fatal cause after later signal = %v, want server-ID conflict", got)
		}
	} else if got != nil {
		t.Fatalf("fatal cause after operator signal = %v, want nil", got)
	}
}

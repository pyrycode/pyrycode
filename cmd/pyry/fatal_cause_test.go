package main

import (
	"context"
	"errors"
	"testing"
)

// TestFatalCause pins the exit classification that decides whether launchd
// restarts the daemon: a self-initiated fatal shutdown (relay persistent 4409)
// cancels the cause context WITH an error, so fatalCause returns it and
// runSupervisor exits non-zero; the operator paths (SIGTERM / `pyry stop`)
// cancel with a nil cause and stay down at exit 0.
func TestFatalCause(t *testing.T) {
	sentinel := errors.New("relay: server-id conflict (close 4409)")

	t.Run("no cause (context still live) returns nil", func(t *testing.T) {
		ctx, cancel := context.WithCancelCause(context.Background())
		defer cancel(nil)
		if got := fatalCause(ctx); got != nil {
			t.Fatalf("fatalCause on a live context = %v, want nil", got)
		}
	})

	t.Run("nil cause (operator stop) returns nil", func(t *testing.T) {
		ctx, cancel := context.WithCancelCause(context.Background())
		cancel(nil) // pyry stop / SIGTERM: cancelCause(nil)
		if got := fatalCause(ctx); got != nil {
			t.Fatalf("fatalCause after cancel(nil) = %v, want nil", got)
		}
	})

	t.Run("explicit context.Canceled cause returns nil", func(t *testing.T) {
		ctx, cancel := context.WithCancelCause(context.Background())
		cancel(context.Canceled)
		if got := fatalCause(ctx); got != nil {
			t.Fatalf("fatalCause after cancel(context.Canceled) = %v, want nil", got)
		}
	})

	t.Run("signal-parent cancel returns nil", func(t *testing.T) {
		// SIGTERM cancels the signal parent, which propagates context.Canceled
		// as the child's cause — the operator-stop exit-0 path.
		parent, cancelParent := context.WithCancel(context.Background())
		ctx, cancel := context.WithCancelCause(parent)
		defer cancel(nil)
		cancelParent()
		if got := fatalCause(ctx); got != nil {
			t.Fatalf("fatalCause after parent cancel = %v, want nil", got)
		}
	})

	t.Run("real error cause (fatal 4409) returns the cause", func(t *testing.T) {
		ctx, cancel := context.WithCancelCause(context.Background())
		cancel(sentinel) // relay self-initiated fatal shutdown
		got := fatalCause(ctx)
		if !errors.Is(got, sentinel) {
			t.Fatalf("fatalCause after cancel(sentinel) = %v, want %v", got, sentinel)
		}
	})

	t.Run("wrapped error cause returns the wrapped chain", func(t *testing.T) {
		ctx, cancel := context.WithCancelCause(context.Background())
		wrapped := errors.New("relay: lifecycle: " + sentinel.Error())
		cancel(wrapped)
		if got := fatalCause(ctx); got == nil {
			t.Fatal("fatalCause after cancel(wrapped) = nil, want the wrapped error")
		}
	})
}

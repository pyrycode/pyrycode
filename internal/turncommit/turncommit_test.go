package turncommit

import (
	"context"
	"testing"
)

func TestFrom_NilWhenNoGate(t *testing.T) {
	t.Parallel()
	if g := From(context.Background()); g != nil {
		t.Fatalf("From(bare ctx) = %v, want nil", g)
	}
}

func TestWithNilGate_PassesCtxThroughUngated(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	if got := With(ctx, nil); got != ctx {
		t.Fatal("With(ctx, nil) returned a new ctx; want the same ctx (no gate wrapped)")
	}
	if g := From(With(ctx, nil)); g != nil {
		t.Fatalf("From(With(ctx, nil)) = %v, want nil", g)
	}
}

func TestWithThenFrom_RoundTripsAndInvokes(t *testing.T) {
	t.Parallel()
	called := 0
	gate := Gate(func() bool { called++; return true })

	got := From(With(context.Background(), gate))
	if got == nil {
		t.Fatal("From(With(ctx, gate)) = nil, want the gate")
	}
	if !got() {
		t.Fatal("gate() = false, want true")
	}
	if called != 1 {
		t.Fatalf("gate invoked %d times, want 1", called)
	}
}

func TestFrom_ReadsThroughDerivedCtx(t *testing.T) {
	t.Parallel()
	// A cancelable/timeout child of a gate-carrying ctx must still expose the gate,
	// because the seam derives sub-contexts (WithCancel/WithTimeout) before delivery.
	gate := Gate(func() bool { return false })
	parent := With(context.Background(), gate)
	child, cancel := context.WithCancel(parent)
	defer cancel()
	if g := From(child); g == nil || g() {
		t.Fatal("gate not visible through a derived ctx, or returned the wrong value")
	}
}

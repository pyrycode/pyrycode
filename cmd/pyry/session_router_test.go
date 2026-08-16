package main

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

// stubRunner is a Runner that never spawns anything. A factory is mandatory on
// sessions.Config since #1348 — it used to default to the terminal supervisor,
// which is why this test could previously get away with naming none.
type stubRunner struct{}

func (stubRunner) State() sessions.State { return sessions.State{} }
func (stubRunner) WriteUserTurn(ctx context.Context, conversationID string, payload []byte) error {
	return nil
}
func (stubRunner) WaitForPTY(ctx context.Context) error { return nil }
func (stubRunner) Run(ctx context.Context) error        { <-ctx.Done(); return ctx.Err() }
func (stubRunner) Restart(args []string)                {}

// newRouterTestPool builds a real *sessions.Pool. sessions.New constructs the
// bootstrap session entry without spawning claude, so Pool.Lookup works against
// the in-memory map.
func newRouterTestPool(t *testing.T) *sessions.Pool {
	t.Helper()
	pool, err := sessions.New(sessions.Config{
		Bootstrap:     sessions.SessionConfig{ClaudeBin: os.Args[0]},
		RunnerFactory: func(sessions.RunnerConfig) (sessions.Runner, error) { return stubRunner{}, nil },
	})
	if err != nil {
		t.Fatalf("sessions.New: %v", err)
	}
	return pool
}

// TestSessionRouter_Route exercises the cmd/pyry resolution adapter against a
// real *sessions.Pool + *conversations.Registry — the layer the handler-level
// stubs cannot reach. The load-bearing case is empty-CurrentSessionID:
// Pool.Lookup("") returns the bootstrap session, so without the empty-binding
// guard an unbound conversation would silently route a phone's turn into the
// shared bootstrap claude (the isolation break AC#4 forbids).
func TestSessionRouter_Route(t *testing.T) {
	t.Parallel()
	pool := newRouterTestPool(t)
	bootstrapID := pool.Default().ID()

	now := time.Now().UTC()
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{ID: "conv-bound", CurrentSessionID: string(bootstrapID), LastUsedAt: now})
	reg.Create(conversations.Conversation{ID: "conv-unbound", CurrentSessionID: "", LastUsedAt: now})
	reg.Create(conversations.Conversation{ID: "conv-dangling", CurrentSessionID: "session-not-in-pool", LastUsedAt: now})

	// Each subtest gets a fresh router with its own active-conversation holder so
	// the #687 cursor assertions are order-independent (a successful route in one
	// subtest must not bleed into another's "stays empty" check).
	newRouter := func() sessionRouter {
		return sessionRouter{pool: pool, convReg: reg, active: &activeConversation{}}
	}

	t.Run("bound resolves to the per-conversation session", func(t *testing.T) {
		r := newRouter()
		w, err := r.Route("conv-bound")
		if err != nil {
			t.Fatalf("Route: unexpected err %v", err)
		}
		b, ok := w.(boundSession)
		if !ok {
			t.Fatalf("Route returned %T, want boundSession", w)
		}
		if b.id != bootstrapID {
			t.Errorf("boundSession.id = %q, want %q", b.id, bootstrapID)
		}
		if b.sess != pool.Default() {
			t.Errorf("boundSession.sess = %p, want bootstrap %p", b.sess, pool.Default())
		}
		// #687 AC#1: a successful route stamps the active-conversation cursor.
		if got := r.active.CurrentConversation(); got != "conv-bound" {
			t.Errorf("active cursor = %q, want %q after a successful route", got, "conv-bound")
		}
	})

	t.Run("unknown conversation maps to ErrConversationNotFound", func(t *testing.T) {
		r := newRouter()
		w, err := r.Route("conv-does-not-exist")
		if !errors.Is(err, conversations.ErrConversationNotFound) {
			t.Errorf("err = %v, want ErrConversationNotFound", err)
		}
		if w != nil {
			t.Errorf("writer = %v, want nil on reject", w)
		}
		// #687 AC#4: a rejected route never moves the cursor.
		if got := r.active.CurrentConversation(); got != "" {
			t.Errorf("active cursor = %q, want empty after a rejected route", got)
		}
	})

	t.Run("empty CurrentSessionID rejects before Lookup, never returns bootstrap", func(t *testing.T) {
		// The hazard the guard defeats: a bare Pool.Lookup("") hands back the
		// bootstrap session, so a missing binding must be rejected before Lookup.
		boot, err := pool.Lookup("")
		if err != nil || boot != pool.Default() {
			t.Fatalf("precondition: Lookup(\"\") = (%v, %v), want bootstrap session", boot, err)
		}
		r := newRouter()
		w, err := r.Route("conv-unbound")
		if !errors.Is(err, errNoBoundSession) {
			t.Errorf("err = %v, want errNoBoundSession", err)
		}
		if w != nil {
			t.Errorf("writer = %v, want nil — an unbound conversation must NEVER route to the bootstrap", w)
		}
		// #687 AC#4: an unbound conversation never stamps the cursor.
		if got := r.active.CurrentConversation(); got != "" {
			t.Errorf("active cursor = %q, want empty after an unbound route", got)
		}
	})

	t.Run("bound id absent from pool flows ErrSessionNotFound through", func(t *testing.T) {
		r := newRouter()
		w, err := r.Route("conv-dangling")
		if !errors.Is(err, sessions.ErrSessionNotFound) {
			t.Errorf("err = %v, want ErrSessionNotFound", err)
		}
		if w != nil {
			t.Errorf("writer = %v, want nil on reject", w)
		}
		// #687 AC#4: a dangling binding never stamps the cursor.
		if got := r.active.CurrentConversation(); got != "" {
			t.Errorf("active cursor = %q, want empty after a dangling route", got)
		}
	})
}

// TestSessionRouter_ResolveDoesNotStamp guards the #721 side-effect split: the
// drain delivers via resolve (the stamp-free core), so a successful resolve must
// leave the active-conversation cursor untouched while a successful Route on the
// same binding still stamps it. Without this, a deferred drain would re-stamp
// the follow-active cursor at DRAIN time (drain order) rather than at phone-
// interaction time, breaking the #679/#687 invariant.
func TestSessionRouter_ResolveDoesNotStamp(t *testing.T) {
	t.Parallel()
	pool := newRouterTestPool(t)
	bootstrapID := pool.Default().ID()

	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{ID: "conv-bound", CurrentSessionID: string(bootstrapID), LastUsedAt: time.Now().UTC()})

	t.Run("resolve does not stamp the active cursor", func(t *testing.T) {
		r := sessionRouter{pool: pool, convReg: reg, active: &activeConversation{}}
		w, err := r.resolve("conv-bound")
		if err != nil {
			t.Fatalf("resolve: unexpected err %v", err)
		}
		if w == nil {
			t.Fatalf("resolve returned a nil writer for a bound conversation")
		}
		if got := r.active.CurrentConversation(); got != "" {
			t.Errorf("active cursor = %q, want empty — resolve must NOT stamp (drain-time re-resolve)", got)
		}
	})

	t.Run("Route stamps the active cursor", func(t *testing.T) {
		r := sessionRouter{pool: pool, convReg: reg, active: &activeConversation{}}
		if _, err := r.Route("conv-bound"); err != nil {
			t.Fatalf("Route: unexpected err %v", err)
		}
		if got := r.active.CurrentConversation(); got != "conv-bound" {
			t.Errorf("active cursor = %q, want conv-bound — Route must stamp", got)
		}
	})
}

package main

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

// newRouterTestPool builds a real *sessions.Pool. sessions.New constructs the
// bootstrap supervisor + session entry without spawning claude, so Pool.Lookup
// works against the in-memory map. os.Args[0] is a guaranteed absolute,
// executable path so supervisor.New's exec.LookPath succeeds without a real
// claude binary on PATH.
func newRouterTestPool(t *testing.T) *sessions.Pool {
	t.Helper()
	pool, err := sessions.New(sessions.Config{
		Bootstrap: sessions.SessionConfig{ClaudeBin: os.Args[0]},
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

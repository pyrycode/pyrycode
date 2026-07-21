package main

import (
	"errors"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

// restartFreshStub is a sessions.Runner whose fresh-start is RestartFresh(string)
// — *streamsup.Runner-shaped (the direct stream-json arm). It records the rotated
// id it is handed and the call count. It has NO StartNewSession, so a dispatch
// that took the /clear arm instead could not compile against it — the absence of
// that method is the structural "no /clear keystroke" proof.
type restartFreshStub struct {
	baseRunner
	calls int
	gotID string
}

func (r *restartFreshStub) RestartFresh(newID string) { r.calls++; r.gotID = newID }

// startNewSessionStub is a sessions.Runner whose fresh-start is
// StartNewSession() error — *supervisor.Supervisor-shaped (the PTY /clear arm).
type startNewSessionStub struct {
	baseRunner
	calls int
	err   error
}

func (r *startNewSessionStub) StartNewSession() error { r.calls++; return r.err }

// bothMethodsStub exposes RestartFresh AND StartNewSession, proving the type
// switch matches RestartFresh first (a future runner growing both prefers the
// direct stream path over /clear).
type bothMethodsStub struct {
	baseRunner
	freshCalls int
	freshID    string
	clearCalls int
}

func (r *bothMethodsStub) RestartFresh(newID string) { r.freshCalls++; r.freshID = newID }
func (r *bothMethodsStub) StartNewSession() error    { r.clearCalls++; return nil }

// rotateRecorder is a fake pool.RotateForNewSession: it records every oldID it is
// handed and returns a configurable (newID, err).
type rotateRecorder struct {
	calls []sessions.SessionID
	newID sessions.SessionID
	err   error
}

func (r *rotateRecorder) rotate(oldID sessions.SessionID) (sessions.SessionID, error) {
	r.calls = append(r.calls, oldID)
	return r.newID, r.err
}

// TestStartFreshRunner_Dispatch covers the RestartFresh-vs-StartNewSession type
// dispatch: the *streamsup.Runner (adapted as streamRunner) rotates the pool id
// then RestartFreshes — NO /clear; the PTY *supervisor.Supervisor takes the
// /clear arm and is NOT pre-rotated; an unknown runner is inert. Ordering is
// load-bearing: rotate runs before RestartFresh (skip-set primed before spawn).
func TestStartFreshRunner_Dispatch(t *testing.T) {
	t.Parallel()

	const oldID sessions.SessionID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	const newID sessions.SessionID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"

	t.Run("RestartFresh runner rotates then restarts fresh (no /clear)", func(t *testing.T) {
		rot := &rotateRecorder{newID: newID}
		r := &restartFreshStub{}
		if err := startFreshRunner(r, oldID, rot.rotate); err != nil {
			t.Fatalf("startFreshRunner: unexpected err %v", err)
		}
		if len(rot.calls) != 1 || rot.calls[0] != oldID {
			t.Errorf("rotate calls = %v, want [%q] (rotation targets the bound id)", rot.calls, oldID)
		}
		if r.calls != 1 {
			t.Errorf("RestartFresh called %d times, want 1", r.calls)
		}
		if r.gotID != string(newID) {
			t.Errorf("RestartFresh got id %q, want the rotated %q", r.gotID, newID)
		}
	})

	t.Run("runner with both methods prefers RestartFresh, never /clear", func(t *testing.T) {
		rot := &rotateRecorder{newID: newID}
		r := &bothMethodsStub{}
		if err := startFreshRunner(r, oldID, rot.rotate); err != nil {
			t.Fatalf("startFreshRunner: unexpected err %v", err)
		}
		if r.freshCalls != 1 || r.freshID != string(newID) {
			t.Errorf("RestartFresh calls=%d id=%q, want 1 / %q", r.freshCalls, r.freshID, newID)
		}
		if r.clearCalls != 0 {
			t.Errorf("StartNewSession called %d times, want 0 — RestartFresh must be matched first (no /clear)", r.clearCalls)
		}
	})

	t.Run("StartNewSession runner takes the /clear arm, rotate not called", func(t *testing.T) {
		rot := &rotateRecorder{newID: newID}
		r := &startNewSessionStub{}
		if err := startFreshRunner(r, oldID, rot.rotate); err != nil {
			t.Fatalf("startFreshRunner: unexpected err %v", err)
		}
		if r.calls != 1 {
			t.Errorf("StartNewSession called %d times, want 1", r.calls)
		}
		if len(rot.calls) != 0 {
			t.Errorf("rotate called %d times on the PTY arm, want 0 (only the watcher rotates a /clear)", len(rot.calls))
		}
	})

	t.Run("runner with neither method is inert, rotate not called", func(t *testing.T) {
		rot := &rotateRecorder{newID: newID}
		if err := startFreshRunner(&inertRunnerStub{}, oldID, rot.rotate); err != nil {
			t.Errorf("startFreshRunner(inert) = %v, want nil (no actuation beats wrong actuation)", err)
		}
		if len(rot.calls) != 0 {
			t.Errorf("rotate called on inert runner, want 0")
		}
	})

	t.Run("rotate error surfaces and RestartFresh is not called", func(t *testing.T) {
		want := errors.New("mint failed")
		rot := &rotateRecorder{err: want}
		r := &restartFreshStub{}
		if err := startFreshRunner(r, oldID, rot.rotate); !errors.Is(err, want) {
			t.Errorf("startFreshRunner err = %v, want %v", err, want)
		}
		if r.calls != 0 {
			t.Errorf("RestartFresh called %d times after a rotate error, want 0", r.calls)
		}
	})

	t.Run("StartNewSession error surfaces", func(t *testing.T) {
		want := errors.New("no live child")
		rot := &rotateRecorder{}
		if err := startFreshRunner(&startNewSessionStub{err: want}, oldID, rot.rotate); !errors.Is(err, want) {
			t.Errorf("startFreshRunner err = %v, want %v (best-effort: relay handler Warn-logs it)", err, want)
		}
	})
}

// TestActiveSessionStarter is the AC2 proof: an inbound new_session reaches the
// runner bound to the active conversation and NOT the bootstrap supervisor, and
// rotates the bound session's id. The bound fake records exactly one fresh-restart
// with the rotated id; a separate bootstrap fake wired nowhere records zero. The
// stream fake exposes no StartNewSession, so the dispatch provably never routes a
// /clear. It also covers AC4's inert states (no active conversation, unbound
// resolution) and best-effort error propagation.
func TestActiveSessionStarter(t *testing.T) {
	t.Parallel()

	t.Run("new_session reaches the bound runner (not bootstrap), rotates the bound id, no /clear", func(t *testing.T) {
		const boundID sessions.SessionID = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
		const freshID sessions.SessionID = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
		bound := &restartFreshStub{}
		bootstrap := &restartFreshStub{} // wired nowhere: must never be touched
		rot := &rotateRecorder{newID: freshID}
		ass := activeSessionStarter{
			currentConv: func() string { return "A" },
			resolveBound: func(convID string) (sessions.Runner, sessions.SessionID, bool) {
				if convID == "A" {
					return bound, boundID, true
				}
				return nil, "", false
			},
			rotate: rot.rotate,
		}
		if err := ass.StartNewSession(); err != nil {
			t.Fatalf("StartNewSession: unexpected err %v", err)
		}
		if bound.calls != 1 {
			t.Errorf("bound runner fresh-restarted %d times, want 1", bound.calls)
		}
		if bound.gotID != string(freshID) {
			t.Errorf("bound runner restarted with id %q, want the rotated %q", bound.gotID, freshID)
		}
		if bootstrap.calls != 0 {
			t.Errorf("bootstrap runner fresh-restarted %d times, want 0 — new_session must NOT reach the bootstrap", bootstrap.calls)
		}
		if len(rot.calls) != 1 || rot.calls[0] != boundID {
			t.Errorf("rotate calls = %v, want [%q] (rotation targets the bound session's id, not the bootstrap's)", rot.calls, boundID)
		}
	})

	t.Run("no active conversation is inert; resolveBound and rotate untouched (AC4)", func(t *testing.T) {
		resolved := false
		rot := &rotateRecorder{}
		ass := activeSessionStarter{
			currentConv:  func() string { return "" },
			resolveBound: func(string) (sessions.Runner, sessions.SessionID, bool) { resolved = true; return nil, "", false },
			rotate:       rot.rotate,
		}
		if err := ass.StartNewSession(); err != nil {
			t.Errorf("StartNewSession = %v, want nil", err)
		}
		if resolved {
			t.Errorf("resolveBound called with no active conversation, want short-circuit")
		}
		if len(rot.calls) != 0 {
			t.Errorf("rotate called with no active conversation, want 0 (no rotation)")
		}
	})

	t.Run("unbound/dangling resolution is inert; rotate untouched (AC4)", func(t *testing.T) {
		rot := &rotateRecorder{}
		ass := activeSessionStarter{
			currentConv:  func() string { return "A" },
			resolveBound: func(string) (sessions.Runner, sessions.SessionID, bool) { return nil, "", false },
			rotate:       rot.rotate,
		}
		if err := ass.StartNewSession(); err != nil {
			t.Errorf("StartNewSession = %v, want nil", err)
		}
		if len(rot.calls) != 0 {
			t.Errorf("rotate called on an unbound resolution, want 0")
		}
	})

	t.Run("a rotate error propagates (best-effort: relay handler Warn-logs it)", func(t *testing.T) {
		want := errors.New("session vanished")
		rot := &rotateRecorder{err: want}
		ass := activeSessionStarter{
			currentConv:  func() string { return "A" },
			resolveBound: func(string) (sessions.Runner, sessions.SessionID, bool) { return &restartFreshStub{}, "old", true },
			rotate:       rot.rotate,
		}
		if err := ass.StartNewSession(); !errors.Is(err, want) {
			t.Errorf("StartNewSession err = %v, want %v", err, want)
		}
	})
}

// TestResolveBoundSession exercises the active-conversation → bound-(session,id)
// resolution against a real *sessions.Pool + *conversations.Registry, mirroring
// TestResolveBoundRunner. The load-bearing case is empty-CurrentSessionID:
// Pool.Lookup("") returns the BOOTSTRAP session, so without the empty-binding
// guard an unbound conversation's new_session would rotate the shared bootstrap
// session (the #678 isolation break this ticket must not reintroduce).
func TestResolveBoundSession(t *testing.T) {
	t.Parallel()
	pool := newRouterTestPool(t)
	bootstrapID := pool.Default().ID()

	now := time.Now().UTC()
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{ID: "conv-bound", CurrentSessionID: string(bootstrapID), LastUsedAt: now})
	reg.Create(conversations.Conversation{ID: "conv-unbound", CurrentSessionID: "", LastUsedAt: now})
	reg.Create(conversations.Conversation{ID: "conv-dangling", CurrentSessionID: "session-not-in-pool", LastUsedAt: now})

	t.Run("unknown conversation is inert", func(t *testing.T) {
		sess, id, ok := resolveBoundSession(reg, pool, "conv-does-not-exist")
		if ok || sess != nil || id != "" {
			t.Errorf("resolveBoundSession(unknown) = (%v, %q, %v), want (nil, \"\", false)", sess, id, ok)
		}
	})

	t.Run("empty CurrentSessionID is inert, never the bootstrap session", func(t *testing.T) {
		// The hazard the guard defeats: a bare Pool.Lookup("") hands back the
		// bootstrap session, so a missing binding must be rejected before Lookup.
		boot, err := pool.Lookup("")
		if err != nil || boot != pool.Default() {
			t.Fatalf("precondition: Lookup(\"\") = (%v, %v), want bootstrap session", boot, err)
		}
		sess, id, ok := resolveBoundSession(reg, pool, "conv-unbound")
		if ok {
			t.Errorf("resolveBoundSession(unbound) ok = true, want false")
		}
		if sess != nil || id != "" {
			t.Errorf("resolveBoundSession(unbound) = (%v, %q), want (nil, \"\") — an unbound conversation must NEVER resolve to the bootstrap session", sess, id)
		}
	})

	t.Run("dangling binding is inert", func(t *testing.T) {
		sess, id, ok := resolveBoundSession(reg, pool, "conv-dangling")
		if ok || sess != nil || id != "" {
			t.Errorf("resolveBoundSession(dangling) = (%v, %q, %v), want (nil, \"\", false)", sess, id, ok)
		}
	})

	t.Run("bound conversation resolves to that session and its id", func(t *testing.T) {
		sess, id, ok := resolveBoundSession(reg, pool, "conv-bound")
		if !ok {
			t.Fatalf("resolveBoundSession(bound) ok = false, want true")
		}
		if sess != pool.Default() {
			t.Errorf("resolveBoundSession(bound) returned the wrong session; want the bound session")
		}
		if id != bootstrapID {
			t.Errorf("resolveBoundSession(bound) id = %q, want %q", id, bootstrapID)
		}
	})
}

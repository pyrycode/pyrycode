package main

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

// occurred is a fixed transition timestamp shared across the cases. It is UTC
// (as #659 stamps it) so the JSON round-trip in the fan-out tests is lossless
// modulo the monotonic-clock strip handled by time.Time.Equal.
var occurred = time.Date(2026, 6, 9, 10, 33, 14, 500000000, time.UTC)

// constResolver is a session→conversation resolver stub returning a fixed
// (convID, ok) for every lookup. The broadcast tests that expect delivery pass
// constResolver("conv-1", true); the drop/never-called cases vary it.
func constResolver(convID string, ok bool) func(string) (string, bool) {
	return func(string) (string, bool) { return convID, ok }
}

func decodeSessionTransition(t *testing.T, env protocol.Envelope) protocol.SessionTransitionPayload {
	t.Helper()
	var p protocol.SessionTransitionPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		t.Fatalf("decode session_transition payload: %v", err)
	}
	return p
}

func TestToWirePayload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		in       sessions.SessionTransition
		wantOK   bool
		wantPrev string
		wantNew  string
		wantReas string
	}{
		{
			name:     "clear maps prev+new verbatim",
			in:       sessions.SessionTransition{PreviousID: "sess-a", NewID: "sess-b", Reason: sessions.ReasonClear, OccurredAt: occurred},
			wantOK:   true,
			wantPrev: "sess-a",
			wantNew:  "sess-b",
			wantReas: "clear",
		},
		{
			name:     "eviction maps evicted id onto both fields",
			in:       sessions.SessionTransition{PreviousID: "sess-a", NewID: "", Reason: sessions.ReasonEviction, OccurredAt: occurred},
			wantOK:   true,
			wantPrev: "sess-a",
			wantNew:  "sess-a", // no successor; evicted id mirrored per mobile #336
			wantReas: "idle_evict",
		},
		{
			name:   "unknown reason drops",
			in:     sessions.SessionTransition{PreviousID: "sess-a", Reason: sessions.TransitionReason("frobnicate"), OccurredAt: occurred},
			wantOK: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := toWirePayload(tc.in)
			if ok != tc.wantOK {
				t.Fatalf("ok: got %v, want %v", ok, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if got.PreviousSessionID != tc.wantPrev {
				t.Errorf("previous_session_id: got %q, want %q", got.PreviousSessionID, tc.wantPrev)
			}
			if got.NewSessionID != tc.wantNew {
				t.Errorf("new_session_id: got %q, want %q", got.NewSessionID, tc.wantNew)
			}
			if got.Reason != tc.wantReas {
				t.Errorf("reason: got %q, want %q", got.Reason, tc.wantReas)
			}
			if got.WorkspaceCwd != nil {
				t.Errorf("workspace_cwd: got %v, want nil", *got.WorkspaceCwd)
			}
			if !got.OccurredAt.Equal(occurred) {
				t.Errorf("occurred_at: got %v, want %v", got.OccurredAt, occurred)
			}
		})
	}
}

// mixedSnapshot is one interactive ("i") and one non-interactive ("n") open
// conn — the AC#3 fixture: the event must reach "i" and never "n".
func mixedSnapshot() *fakeInteractiveBcast {
	return &fakeInteractiveBcast{
		snapshots: [][]relay.ActiveConn{{
			{ConnID: "i", Interactive: true},
			{ConnID: "n", Interactive: false},
		}},
	}
}

func TestSessionTransitionBroadcast_Clear(t *testing.T) {
	t.Parallel()
	bcast := mixedSnapshot()
	e := newSessionTransitionEmitterV2(bcast, constResolver("conv-1", true), discardLogger())

	e.broadcast(context.Background(), sessions.SessionTransition{
		PreviousID: "sess-a", NewID: "sess-b", Reason: sessions.ReasonClear, OccurredAt: occurred,
	})

	// AC#3: exactly one push, to the interactive conn; nothing to "n".
	if got := pushesFor(bcast.pushes, "n"); len(got) != 0 {
		t.Fatalf("non-interactive conn received %d pushes, want 0", len(got))
	}
	to := pushesFor(bcast.pushes, "i")
	if len(to) != 1 {
		t.Fatalf("interactive conn received %d pushes, want 1", len(to))
	}
	env := to[0].env
	if env.Type != protocol.TypeSessionTransition {
		t.Errorf("type: got %q, want %q", env.Type, protocol.TypeSessionTransition)
	}
	p := decodeSessionTransition(t, env)
	if p.Reason != "clear" {
		t.Errorf("reason: got %q, want %q", p.Reason, "clear")
	}
	if p.PreviousSessionID != "sess-a" || p.NewSessionID != "sess-b" {
		t.Errorf("ids: got prev=%q new=%q, want prev=sess-a new=sess-b", p.PreviousSessionID, p.NewSessionID)
	}
	// AC#1: the resolved conversation_id is stamped onto the envelope.
	if p.ConversationID != "conv-1" {
		t.Errorf("conversation_id: got %q, want %q", p.ConversationID, "conv-1")
	}
	if !p.OccurredAt.Equal(occurred) {
		t.Errorf("occurred_at: got %v, want %v", p.OccurredAt, occurred)
	}
	// workspace_cwd must render literal JSON null, not be absent.
	if !bytes.Contains(env.Payload, []byte(`"workspace_cwd":null`)) {
		t.Errorf("payload missing literal null workspace_cwd: %s", env.Payload)
	}
}

func TestSessionTransitionBroadcast_Eviction(t *testing.T) {
	t.Parallel()
	bcast := mixedSnapshot()
	e := newSessionTransitionEmitterV2(bcast, constResolver("conv-1", true), discardLogger())

	e.broadcast(context.Background(), sessions.SessionTransition{
		PreviousID: "sess-a", NewID: "", Reason: sessions.ReasonEviction, OccurredAt: occurred,
	})

	if got := pushesFor(bcast.pushes, "n"); len(got) != 0 {
		t.Fatalf("non-interactive conn received %d pushes, want 0", len(got))
	}
	to := pushesFor(bcast.pushes, "i")
	if len(to) != 1 {
		t.Fatalf("interactive conn received %d pushes, want 1", len(to))
	}
	p := decodeSessionTransition(t, to[0].env)
	if p.Reason != "idle_evict" {
		t.Errorf("reason: got %q, want %q", p.Reason, "idle_evict")
	}
	// Eviction has no successor; both id fields carry the evicted id (#336).
	if p.PreviousSessionID != "sess-a" || p.NewSessionID != "sess-a" {
		t.Errorf("ids: got prev=%q new=%q, want both sess-a", p.PreviousSessionID, p.NewSessionID)
	}
	// AC#1: idle_evict carries the resolved conversation_id too.
	if p.ConversationID != "conv-1" {
		t.Errorf("conversation_id: got %q, want %q", p.ConversationID, "conv-1")
	}
	if p.WorkspaceCwd != nil {
		t.Errorf("workspace_cwd: got %v, want nil", *p.WorkspaceCwd)
	}
}

// TestSessionTransitionBroadcast_NonInteractiveOnly is the capability gate in
// isolation (AC#3): a snapshot of only non-interactive conns yields zero pushes.
func TestSessionTransitionBroadcast_NonInteractiveOnly(t *testing.T) {
	t.Parallel()
	bcast := &fakeInteractiveBcast{
		snapshots: [][]relay.ActiveConn{{
			{ConnID: "n1", Interactive: false},
			{ConnID: "n2", Interactive: false},
		}},
	}
	e := newSessionTransitionEmitterV2(bcast, constResolver("conv-1", true), discardLogger())

	e.broadcast(context.Background(), sessions.SessionTransition{
		PreviousID: "sess-a", NewID: "sess-b", Reason: sessions.ReasonClear, OccurredAt: occurred,
	})

	if len(bcast.pushes) != 0 {
		t.Fatalf("got %d pushes to non-interactive conns, want 0 (types=%v)", len(bcast.pushes), pushTypes(bcast.pushes))
	}
}

// TestSessionTransitionBroadcast_UnknownReasonDrops proves no envelope reaches
// the wire for a reason outside the closed mapper set.
func TestSessionTransitionBroadcast_UnknownReasonDrops(t *testing.T) {
	t.Parallel()
	bcast := mixedSnapshot()
	e := newSessionTransitionEmitterV2(bcast, constResolver("conv-1", true), discardLogger())

	e.broadcast(context.Background(), sessions.SessionTransition{
		PreviousID: "sess-a", Reason: sessions.TransitionReason("frobnicate"), OccurredAt: occurred,
	})

	if len(bcast.pushes) != 0 {
		t.Fatalf("unknown reason emitted %d pushes, want 0", len(bcast.pushes))
	}
}

// TestSessionTransitionBroadcast_PushErrorContinues proves a failing conn does
// not abort the fan-out for the others (AC#2 robustness).
func TestSessionTransitionBroadcast_PushErrorContinues(t *testing.T) {
	t.Parallel()
	bcast := &fakeInteractiveBcast{
		snapshots: [][]relay.ActiveConn{{
			{ConnID: "i1", Interactive: true},
			{ConnID: "i2", Interactive: true},
		}},
		pushErr: map[string]error{"i1": context.DeadlineExceeded},
	}
	e := newSessionTransitionEmitterV2(bcast, constResolver("conv-1", true), discardLogger())

	e.broadcast(context.Background(), sessions.SessionTransition{
		PreviousID: "sess-a", NewID: "sess-b", Reason: sessions.ReasonClear, OccurredAt: occurred,
	})

	if got := pushesFor(bcast.pushes, "i1"); len(got) != 1 {
		t.Fatalf("first (failing) conn: got %d push attempts, want 1", len(got))
	}
	if got := pushesFor(bcast.pushes, "i2"); len(got) != 1 {
		t.Fatalf("second conn after a failing first: got %d pushes, want 1 (loop must continue)", len(got))
	}
}

// TestSessionTransitionEnqueue_NonBlockingDropOnFull guards #659's MUST-NOT-BLOCK
// observer contract: with no draining Run, sends past capacity drop instead of
// blocking the pool's lifecycle/watcher goroutine.
func TestSessionTransitionEnqueue_NonBlockingDropOnFull(t *testing.T) {
	t.Parallel()
	e := newSessionTransitionEmitterV2(&fakeInteractiveBcast{}, constResolver("", false), discardLogger())

	// Fill to capacity, then overflow. Each call must return (no block) — if any
	// blocked, the test would hang and the go test timeout would catch it.
	for i := 0; i < sessionTransitionQueueSize+2; i++ {
		e.Enqueue(sessions.SessionTransition{Reason: sessions.ReasonClear, OccurredAt: occurred})
	}

	if got := len(e.in); got != sessionTransitionQueueSize {
		t.Fatalf("queue depth: got %d, want %d (overflow must drop, not grow)", got, sessionTransitionQueueSize)
	}
}

// TestConversationForSession exercises the duplicated read scan: a session id
// resolves to its owning conversation via either the current binding or the
// append-only SessionHistory, with the empty-sid guard and single-owner
// invariant pinned.
func TestConversationForSession(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		convs  []conversations.Conversation
		sid    string
		wantID string
		wantOK bool
	}{
		{
			name:   "current-binding hit (clear post-rebind / idle_evict retained)",
			convs:  []conversations.Conversation{{ID: "conv-b", CurrentSessionID: "sess-b"}},
			sid:    "sess-b",
			wantID: "conv-b",
			wantOK: true,
		},
		{
			name:   "history hit (double-rotation race)",
			convs:  []conversations.Conversation{{ID: "conv-c", CurrentSessionID: "sess-c", SessionHistory: []string{"sess-a", "sess-b"}}},
			sid:    "sess-b",
			wantID: "conv-c",
			wantOK: true,
		},
		{
			name:   "miss (no conversation owns the id)",
			convs:  []conversations.Conversation{{ID: "conv-b", CurrentSessionID: "sess-b"}},
			sid:    "sess-z",
			wantOK: false,
		},
		{
			name:   "empty-sid guard must not match an unbound conversation",
			convs:  []conversations.Conversation{{ID: "conv-unbound", CurrentSessionID: ""}},
			sid:    "",
			wantOK: false,
		},
		{
			name: "single owner among many",
			convs: []conversations.Conversation{
				{ID: "conv-x", CurrentSessionID: "sess-x"},
				{ID: "conv-y", CurrentSessionID: "sess-y"},
			},
			sid:    "sess-y",
			wantID: "conv-y",
			wantOK: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reg := &conversations.Registry{}
			for _, c := range tc.convs {
				reg.Create(c)
			}
			gotID, gotOK := conversationForSession(reg, tc.sid)
			if gotOK != tc.wantOK {
				t.Fatalf("ok: got %v, want %v", gotOK, tc.wantOK)
			}
			if gotID != tc.wantID {
				t.Errorf("conversation id: got %q, want %q", gotID, tc.wantID)
			}
		})
	}
}

// TestSessionTransitionBroadcast_UnresolvableDrops proves AC#3: an unresolvable
// binding drops the whole event (zero pushes to any conn), and the emitter
// survives to fan out the next, resolvable transition.
func TestSessionTransitionBroadcast_UnresolvableDrops(t *testing.T) {
	t.Parallel()
	bcast := mixedSnapshot()
	// First lookup is unresolvable; the second resolves — proving Run survives
	// the drop (broadcast called twice on the same emitter).
	var nth int
	resolve := func(string) (string, bool) {
		nth++
		if nth == 1 {
			return "", false
		}
		return "conv-1", true
	}
	e := newSessionTransitionEmitterV2(bcast, resolve, discardLogger())

	e.broadcast(context.Background(), sessions.SessionTransition{
		PreviousID: "sess-a", NewID: "sess-b", Reason: sessions.ReasonClear, OccurredAt: occurred,
	})
	if len(bcast.pushes) != 0 {
		t.Fatalf("unresolvable transition emitted %d pushes, want 0 (whole-event drop)", len(bcast.pushes))
	}

	// Follow-up resolvable transition still delivers — the Run-survives shape.
	e.broadcast(context.Background(), sessions.SessionTransition{
		PreviousID: "sess-b", NewID: "sess-c", Reason: sessions.ReasonClear, OccurredAt: occurred,
	})
	if got := pushesFor(bcast.pushes, "i"); len(got) != 1 {
		t.Fatalf("follow-up resolvable transition: interactive conn got %d pushes, want 1", len(got))
	}
}

// TestSessionTransitionBroadcast_ResolvesByNewSessionID proves the resolver is
// called with payload.NewSessionID — for an eviction (NewID == "") that is the
// evicted id mirrored onto new_session_id, never the empty NewID.
func TestSessionTransitionBroadcast_ResolvesByNewSessionID(t *testing.T) {
	t.Parallel()
	bcast := mixedSnapshot()
	var gotSID string
	resolve := func(sid string) (string, bool) {
		gotSID = sid
		return "conv-1", true
	}
	e := newSessionTransitionEmitterV2(bcast, resolve, discardLogger())

	e.broadcast(context.Background(), sessions.SessionTransition{
		PreviousID: "sess-evicted", NewID: "", Reason: sessions.ReasonEviction, OccurredAt: occurred,
	})

	if gotSID != "sess-evicted" {
		t.Fatalf("resolver called with sid=%q, want the mirrored new_session_id %q", gotSID, "sess-evicted")
	}
}

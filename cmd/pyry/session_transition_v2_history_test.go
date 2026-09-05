package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

// AC-3: a session transition is appended ONCE per transition, carrying one
// timestamp shared with the fan-out — not one entry and not one timestamp per
// connected conn. Three interactive conns is what makes the difference visible:
// before the hoist, broadcast minted time.Now() inside the per-conn loop, so the
// three envelopes carried three distinct timestamps and no single log entry
// could match them all.
//
// This is also the only place a session boundary is retained at all: this
// producer deliberately skips the #647 replay ring.
func TestSessionTransitionEmitterV2_HistoryOncePerTransition(t *testing.T) {
	t.Parallel()
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{
		{ConnID: "i1", Interactive: true},
		{ConnID: "i2", Interactive: true},
		{ConnID: "i3", Interactive: true},
	}}}
	store := history.New(t.TempDir())
	e := newSessionTransitionEmitterV2(bcast, constResolver(testConvID, true), discardLogger())
	e.hist = store

	e.broadcast(context.Background(), sessions.SessionTransition{
		PreviousID: "sess-a", NewID: "sess-b", Reason: sessions.ReasonClear, OccurredAt: occurred,
	})

	if len(bcast.pushes) != 3 {
		t.Fatalf("fanned out %d envelopes, want one per interactive conn (3)", len(bcast.pushes))
	}
	entries := historyEntries(t, store, testConvID)
	if len(entries) != 1 {
		t.Fatalf("log holds %d entries for one transition, want exactly 1", len(entries))
	}
	got := entries[0]
	if got.Type != protocol.TypeSessionTransition {
		t.Fatalf("entry type = %q, want %q", got.Type, protocol.TypeSessionTransition)
	}
	for i, p := range bcast.pushes {
		if !got.TS.Equal(p.env.TS) {
			t.Fatalf("entry ts = %s but conn %d's envelope carries %s; the fan-out timestamp is not hoisted",
				got.TS, i, p.env.TS)
		}
		if !bytes.Equal(got.Payload, p.env.Payload) {
			t.Fatalf("entry payload differs from conn %d's envelope:\n log %s\nwire %s", i, got.Payload, p.env.Payload)
		}
	}
	if decoded := decodeSessionTransition(t, bcast.pushes[0].env); decoded.ConversationID != testConvID {
		t.Fatalf("stamped conversation_id = %q, want %q", decoded.ConversationID, testConvID)
	}
}

// A transition that never reaches the wire never reaches the log either: both
// existing drops return before the append, so the log records what was fanned
// out and never what was refused.
func TestSessionTransitionEmitterV2_DroppedTransitionsWriteNothing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		resolve func(string) (string, bool)
		trans   sessions.SessionTransition
	}{
		{
			name:    "unknown reason",
			resolve: constResolver(testConvID, true),
			trans: sessions.SessionTransition{
				PreviousID: "sess-a", Reason: sessions.TransitionReason("frobnicate"), OccurredAt: occurred,
			},
		},
		{
			name:    "unresolvable conversation",
			resolve: constResolver("", false),
			trans: sessions.SessionTransition{
				PreviousID: "sess-a", NewID: "sess-b", Reason: sessions.ReasonClear, OccurredAt: occurred,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "i1", Interactive: true}}}}
			store := history.New(t.TempDir())
			var buf bytes.Buffer
			e := newSessionTransitionEmitterV2(bcast, tt.resolve, bufLogger(&buf))
			e.hist = store

			e.broadcast(context.Background(), tt.trans)

			if len(bcast.pushes) != 0 {
				t.Fatalf("fanned out %d envelopes for a dropped transition; want 0", len(bcast.pushes))
			}
			if entries := historyEntries(t, store, testConvID); len(entries) != 0 {
				t.Fatalf("log holds %d entries for a dropped transition; want 0", len(entries))
			}
			// The drop's own Debug line is below the buffer logger's level; a Warn
			// here would mean an append was attempted and failed.
			if buf.Len() != 0 {
				t.Fatalf("dropped transition logged at Warn: %q", buf.String())
			}
		})
	}
}

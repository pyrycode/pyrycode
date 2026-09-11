package main

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

const (
	parentLaneA = "toolu_parent_a"
	parentLaneB = "toolu_parent_b"
)

func newParentLaneEmitter(t *testing.T, convID string) (*stubCursor, *fakeInteractiveBcast, *interactiveTurnEmitterV2) {
	t.Helper()
	cur := &stubCursor{}
	cur.set(convID)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())
	t.Cleanup(func() { e.flushTimer.Stop() })
	return cur, bcast, e
}

func TestInteractiveTurnEmitterV2_ParentLanesInterleaveInArrivalOrder(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, bcast, e := newParentLaneEmitter(t, testConvID)

	for _, ev := range []turnevent.Event{
		turnevent.TextChunk{MessageID: "shared", Text: "main-0a"},
		turnevent.TextChunk{MessageID: "shared", Text: "+main-0b"},
		turnevent.TextChunk{MessageID: "shared", ParentToolCallID: parentLaneA, Text: "child-a0"},
		turnevent.TextChunk{MessageID: "shared", ParentToolCallID: parentLaneA, Text: "+child-a0b"},
		turnevent.TextChunk{MessageID: "shared", Text: "main-1"},
		turnevent.TextChunk{MessageID: "a-second", ParentToolCallID: parentLaneA, Text: "child-a1"},
		turnevent.TextChunk{MessageID: "a-second", ParentToolCallID: parentLaneB, Text: "child-b0"},
		turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn},
	} {
		e.Handle(ctx, ev)
	}

	deltas := assistantDeltas(t, bcast.pushes)
	if len(deltas) != 5 {
		t.Fatalf("assistant deltas = %d, want 5: %+v", len(deltas), deltas)
	}
	wantText := []string{"main-0a+main-0b", "child-a0+child-a0b", "main-1", "child-a1", "child-b0"}
	wantParents := []string{"", parentLaneA, "", parentLaneA, parentLaneB}
	wantSeqs := []int{0, 0, 1, 1, 0}
	for i, d := range deltas {
		if d.Text != wantText[i] || d.ParentToolUseID != wantParents[i] || d.Seq != wantSeqs[i] {
			t.Errorf("delta %d = {text:%q parent:%q seq:%d}, want {text:%q parent:%q seq:%d}",
				i, d.Text, d.ParentToolUseID, d.Seq, wantText[i], wantParents[i], wantSeqs[i])
		}
	}

	mainID, childAID, childBID := deltas[0].TurnID, deltas[1].TurnID, deltas[4].TurnID
	if deltas[2].TurnID != mainID {
		t.Errorf("main lane changed turn id from %q to %q", mainID, deltas[2].TurnID)
	}
	if deltas[3].TurnID != childAID {
		t.Errorf("child A lane changed turn id from %q to %q", childAID, deltas[3].TurnID)
	}
	if mainID == childAID || mainID == childBID || childAID == childBID {
		t.Errorf("lane turn ids are not distinct: main=%q childA=%q childB=%q", mainID, childAID, childBID)
	}
}

func TestInteractiveTurnEmitterV2_ParentLaneTimerFlushKeepsIdentity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, bcast, e := newParentLaneEmitter(t, testConvID)

	first := strings.Repeat("x", maxDeltaTextBytes+1)
	e.Handle(ctx, turnevent.TextChunk{MessageID: "one", ParentToolCallID: parentLaneA, Text: first})
	e.flushDelta(ctx)
	e.Handle(ctx, turnevent.TextChunk{MessageID: "one", ParentToolCallID: parentLaneA, Text: "second"})
	e.flushDelta(ctx)

	deltas := assistantDeltas(t, bcast.pushes)
	if len(deltas) != 3 {
		t.Fatalf("assistant deltas = %d, want 3", len(deltas))
	}
	if got := deltas[0].Text + deltas[1].Text; got != first {
		t.Errorf("split child text rejoined to %d bytes, want %d", len(got), len(first))
	}
	for i, d := range deltas {
		if d.TurnID != deltas[0].TurnID || d.Seq != i || d.ParentToolUseID != parentLaneA {
			t.Errorf("delta %d = {seq:%d parent:%q}, want {seq:%d parent:%q}", i, d.Seq, d.ParentToolUseID, i, parentLaneA)
		}
	}
	if deltas[2].Text != "second" {
		t.Errorf("post-timer child text = %q, want second", deltas[2].Text)
	}
}

func TestInteractiveTurnEmitterV2_TurnEndResetsParentLanes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, bcast, e := newParentLaneEmitter(t, testConvID)

	e.Handle(ctx, turnevent.TextChunk{MessageID: "old", ParentToolCallID: parentLaneA, Text: "old-child"})
	e.Handle(ctx, turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn})
	e.Handle(ctx, turnevent.TextChunk{MessageID: "new-main", Text: "new-main"})
	e.Handle(ctx, turnevent.TextChunk{MessageID: "new-child", ParentToolCallID: parentLaneA, Text: "new-child"})
	e.Handle(ctx, turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn})

	deltas := assistantDeltas(t, bcast.pushes)
	if len(deltas) != 3 {
		t.Fatalf("assistant deltas = %d, want 3: %+v", len(deltas), deltas)
	}
	if got := []string{deltas[0].Text, deltas[1].Text, deltas[2].Text}; !slices.Equal(got, []string{"old-child", "new-main", "new-child"}) {
		t.Fatalf("delta text = %v, want old and new turn text once in arrival order", got)
	}
	if deltas[0].ParentToolUseID != parentLaneA || deltas[1].ParentToolUseID != "" || deltas[2].ParentToolUseID != parentLaneA {
		t.Errorf("parent ids leaked across turn reset: %q, %q, %q", deltas[0].ParentToolUseID, deltas[1].ParentToolUseID, deltas[2].ParentToolUseID)
	}
	for i, d := range deltas {
		if d.Seq != 0 {
			t.Errorf("delta %d seq = %d, want 0 for its fresh lane", i, d.Seq)
		}
	}
	if deltas[0].TurnID == deltas[2].TurnID {
		t.Errorf("child lane reused turn id %q across outer turns", deltas[0].TurnID)
	}
	if deltas[1].TurnID == deltas[0].TurnID || deltas[1].TurnID == deltas[2].TurnID {
		t.Errorf("new main lane turn id %q aliases a child lane", deltas[1].TurnID)
	}

	wantTypes := []string{
		protocol.TypeTurnState,
		protocol.TypeAssistantDelta,
		protocol.TypeTurnEnd,
		protocol.TypeTurnState,
		protocol.TypeTurnState,
		protocol.TypeAssistantDelta,
		protocol.TypeAssistantDelta,
		protocol.TypeTurnEnd,
		protocol.TypeTurnState,
	}
	if got := pushTypes(bcast.pushes); !slices.Equal(got, wantTypes) {
		t.Fatalf("turn-end envelope order:\n got %v\nwant %v", got, wantTypes)
	}
}

func TestInteractiveTurnEmitterV2_SwitchResetsParentLanes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cur, bcast, e := newParentLaneEmitter(t, switchConvA)

	e.Handle(ctx, turnevent.TextChunk{MessageID: "old", ParentToolCallID: parentLaneA, Text: "old-child"})
	cur.set(switchConvB)
	e.Handle(ctx, turnevent.TextChunk{MessageID: "new", ParentToolCallID: parentLaneA, Text: "new-child"})
	e.flushDelta(ctx)

	deltas := assistantDeltas(t, bcast.pushes)
	if len(deltas) != 2 {
		t.Fatalf("assistant deltas = %d, want 2", len(deltas))
	}
	if deltas[0].ConversationID != switchConvA || deltas[1].ConversationID != switchConvB {
		t.Errorf("delta conversations = %q then %q, want %q then %q",
			deltas[0].ConversationID, deltas[1].ConversationID, switchConvA, switchConvB)
	}
	if deltas[0].TurnID == deltas[1].TurnID {
		t.Errorf("child lane reused turn id %q across conversation switch", deltas[0].TurnID)
	}
	for i, d := range deltas {
		if d.Seq != 0 || d.ParentToolUseID != parentLaneA {
			t.Errorf("delta %d = {seq:%d parent:%q}, want fresh seq 0 and parent %q", i, d.Seq, d.ParentToolUseID, parentLaneA)
		}
	}

	wantTypes := []string{
		protocol.TypeTurnState,
		protocol.TypeAssistantDelta,
		protocol.TypeTurnState,
		protocol.TypeAssistantDelta,
	}
	if got := pushTypes(bcast.pushes); !slices.Equal(got, wantTypes) {
		t.Fatalf("switch envelope order:\n got %v\nwant %v", got, wantTypes)
	}
}

func TestInteractiveTurnEmitterV2_AttributedDeltaUsesRingAndHistory(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, bcast, e := newParentLaneEmitter(t, testConvID)
	store := history.New(t.TempDir())
	e.hist = store

	e.Handle(ctx, turnevent.TextChunk{MessageID: "child", ParentToolCallID: parentLaneA, Text: "retained child"})
	e.Handle(ctx, turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn})

	events, gap := e.ring.After(testConvID, 0)
	if gap {
		t.Fatal("ring reports a gap for a four-frame turn")
	}
	entries := historyEntries(t, store, testConvID)
	assertLogMatchesRing(t, entries, events)
	if len(bcast.pushes) != len(events) {
		t.Fatalf("wire pushes = %d, ring events = %d", len(bcast.pushes), len(events))
	}

	var ringDelta protocol.AssistantDeltaPayload
	found := false
	for _, ev := range events {
		if ev.Type != protocol.TypeAssistantDelta {
			continue
		}
		if err := json.Unmarshal(ev.Payload, &ringDelta); err != nil {
			t.Fatalf("decode retained assistant delta: %v", err)
		}
		found = true
	}
	if !found {
		t.Fatal("attributed assistant delta missing from ring and matching history")
	}
	if ringDelta.ParentToolUseID != parentLaneA || ringDelta.Text != "retained child" || ringDelta.Seq != 0 {
		t.Errorf("retained delta = {parent:%q text:%q seq:%d}, want attributed child at seq 0",
			ringDelta.ParentToolUseID, ringDelta.Text, ringDelta.Seq)
	}
}

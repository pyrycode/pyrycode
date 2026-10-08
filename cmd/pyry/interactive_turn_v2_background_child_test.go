package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func TestInteractiveTurnEmitterV2_BackgroundChildAcrossMainEnd(t *testing.T) {
	t.Parallel()
	for _, beforeEnd := range []bool{false, true} {
		t.Run(fmt.Sprintf("child-start-before-end=%v", beforeEnd), func(t *testing.T) {
			ctx := context.Background()
			_, bcast, e := newParentLaneEmitter(t, testConvID)
			e.hist = history.New(t.TempDir())
			e.phases = &turnPhaseSnapshot{}
			e.Handle(ctx, turnevent.ToolStart{ToolCallID: parentLaneA, Title: "Agent"})
			origin := e.turnID
			start := turnevent.ToolStart{ToolCallID: "child-call", ParentToolCallID: parentLaneA, Title: "Bash"}
			if beforeEnd {
				e.Handle(ctx, start)
			}
			e.Handle(ctx, turnevent.TextChunk{MessageID: "child", ParentToolCallID: parentLaneA, Text: "before"})
			e.Handle(ctx, turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn})
			if !beforeEnd {
				e.Handle(ctx, start)
			}
			for _, ev := range []turnevent.Event{
				turnevent.ToolProgress{ToolCallID: "child-call", ElapsedSeconds: -7},
				turnevent.ToolCallDenied{ToolCallID: "child-call", ToolName: "Bash", Message: "refused"},
				turnevent.ToolUpdate{ToolCallID: "child-call", ParentToolCallID: parentLaneA, Status: turnevent.ToolStatusFailed},
				turnevent.TextChunk{MessageID: "child", ParentToolCallID: parentLaneA, Text: "after"},
			} {
				e.Handle(ctx, ev)
			}
			e.flushAll(ctx)
			if e.inTurn || e.turnID != "" || len(e.phases.running()) != 0 {
				t.Fatal("background child opened a synthetic main turn or phase")
			}
			e.Handle(ctx, turnevent.ThoughtChunk{})
			laterMainID := e.turnID
			count := len(bcast.pushes)
			e.Handle(ctx, turnevent.ToolProgress{ToolCallID: "child-call", ElapsedSeconds: 9})
			e.Handle(ctx, turnevent.ToolStart{ToolCallID: "later-call", ParentToolCallID: parentLaneA, Title: "Read"})
			e.Handle(ctx, turnevent.ToolUpdate{ToolCallID: "later-call", ParentToolCallID: parentLaneA})
			e.Handle(ctx, turnevent.TextChunk{MessageID: "child", ParentToolCallID: parentLaneA, Text: "later"})
			e.flushAll(ctx)
			if e.turnID != laterMainID || e.currentState != "thinking" || len(pushesOfType(bcast.pushes[count:], protocol.TypeTurnState)) != 0 {
				t.Fatal("ongoing child work changed the later main turn")
			}
			deltas := assistantDeltas(t, bcast.pushes)
			if len(deltas) != 3 {
				t.Fatalf("child deltas = %+v, want three flushes", deltas)
			}
			for i, d := range deltas {
				if d.TurnID == "" || d.TurnID == origin || d.TurnID == laterMainID || d.TurnID != deltas[0].TurnID || d.Seq != i || d.ParentToolUseID != parentLaneA || d.Text != []string{"before", "after", "later"}[i] {
					t.Errorf("child delta %d = %+v", i, d)
				}
			}
			for _, p := range bcast.pushes {
				if !slices.Contains([]string{protocol.TypeToolUse, protocol.TypeToolResult, protocol.TypeToolProgress, protocol.TypeToolDenied}, p.env.Type) {
					continue
				}
				var fields map[string]any
				if err := json.Unmarshal(p.env.Payload, &fields); err != nil {
					t.Fatal(err)
				}
				if fields["turn_id"] != origin {
					t.Errorf("tool frame %s origin = %v, want %s", p.env.Type, fields["turn_id"], origin)
				}
				if fields["tool_use_id"] == "child-call" {
					if p.env.Type == protocol.TypeToolUse || p.env.Type == protocol.TypeToolResult {
						if fields["parent_tool_use_id"] != parentLaneA {
							t.Errorf("child parent changed: %v", fields)
						}
					} else if _, exists := fields["parent_tool_use_id"]; exists {
						t.Errorf("progress/denial gained a parent wire field: %v", fields)
					}
				}
			}
			progress := pushesOfType(bcast.pushes, protocol.TypeToolProgress)
			var got protocol.ToolProgressPayload
			if err := json.Unmarshal(progress[0].env.Payload, &got); err != nil {
				t.Fatal(err)
			}
			want := protocol.ToolProgressPayload{ConversationID: testConvID, TurnID: origin, ToolUseID: "child-call", ElapsedSeconds: -7}
			if got != want {
				t.Errorf("progress = %#v, want %#v", got, want)
			}
			denials := pushesOfType(bcast.pushes, protocol.TypeToolDenied)
			var denial protocol.ToolDeniedPayload
			if err := json.Unmarshal(denials[0].env.Payload, &denial); err != nil {
				t.Fatal(err)
			}
			wantDenial := protocol.ToolDeniedPayload{ConversationID: testConvID, TurnID: origin, ToolUseID: "child-call", ToolName: "Bash", Message: "refused"}
			if !reflect.DeepEqual(denial, wantDenial) {
				t.Errorf("denial = %#v, want %#v", denial, wantDenial)
			}
			wantOrder := []string{protocol.TypeTurnState, protocol.TypeToolUse}
			if beforeEnd {
				wantOrder = append(wantOrder, protocol.TypeToolUse)
			}
			wantOrder = append(wantOrder, protocol.TypeAssistantDelta, protocol.TypeTurnEnd, protocol.TypeTurnState)
			if !beforeEnd {
				wantOrder = append(wantOrder, protocol.TypeToolUse)
			}
			wantOrder = append(wantOrder, protocol.TypeToolProgress, protocol.TypeToolDenied, protocol.TypeToolResult,
				protocol.TypeAssistantDelta, protocol.TypeTurnState, protocol.TypeToolProgress, protocol.TypeToolUse,
				protocol.TypeToolResult, protocol.TypeAssistantDelta)
			if got := pushTypes(bcast.pushes); !slices.Equal(got, wantOrder) {
				t.Errorf("arrival order = %v, want %v", got, wantOrder)
			}
			if got := pushTypes(bcast.pushes[count:]); !slices.Equal(got, []string{protocol.TypeToolProgress, protocol.TypeToolUse, protocol.TypeToolResult, protocol.TypeAssistantDelta}) {
				t.Errorf("later child order = %v", got)
			}
			events, gap := e.ring.After(testConvID, 0)
			if gap || len(events) != len(bcast.pushes) {
				t.Fatal("live/ring frame counts differ or ring has gap")
			}
			assertLogMatchesRing(t, historyEntries(t, e.hist, testConvID), events)
			for i, ev := range events {
				if ev.Type != bcast.pushes[i].env.Type || !bytes.Equal(ev.Payload, bcast.pushes[i].env.Payload) {
					t.Errorf("live/ring payload or order differs at %d", i)
				}
			}
		})
	}
}

func TestInteractiveTurnEmitterV2_UnknownChildPreservesMainPhase(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"idle", "thinking", "responding"} {
		for _, resultFirst := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/result-first=%v", phase, resultFirst), func(t *testing.T) {
				ctx := context.Background()
				_, bcast, e := newParentLaneEmitter(t, testConvID)
				e.phases = &turnPhaseSnapshot{}
				if phase == "thinking" {
					e.Handle(ctx, turnevent.ThoughtChunk{})
				} else if phase == "responding" {
					e.Handle(ctx, turnevent.TextChunk{Text: "main"})
				}
				id, state, open, seq := e.turnID, e.currentState, e.inTurn, e.seq
				phases := e.phases.running()
				count := len(bcast.pushes)
				first := turnevent.Event(turnevent.ToolStart{ToolCallID: "call", ParentToolCallID: parentLaneA})
				if resultFirst {
					first = turnevent.ToolUpdate{ToolCallID: "call", ParentToolCallID: parentLaneA}
				}
				for _, ev := range []turnevent.Event{first,
					turnevent.ToolProgress{ToolCallID: "call"}, turnevent.ToolCallDenied{ToolCallID: "call"},
					turnevent.ToolUpdate{ToolCallID: "call", ParentToolCallID: parentLaneA},
					turnevent.TextChunk{ParentToolCallID: parentLaneA, Text: "child"},
				} {
					e.Handle(ctx, ev)
				}
				e.flushAll(ctx)
				if phase == "responding" {
					seq++ // the already buffered main delta was flushed in arrival order
				}
				if e.inTurn != open || e.turnID != id || e.currentState != state || e.seq != seq || !reflect.DeepEqual(e.phases.running(), phases) {
					t.Fatal("unknown child changed main lifecycle or phase reconcile")
				}
				var childID string
				for _, p := range bcast.pushes[count:] {
					if p.env.Type == protocol.TypeTurnState || p.env.Type == protocol.TypeTurnEnd {
						t.Fatalf("child emitted lifecycle frame %s", p.env.Type)
					}
					var f map[string]any
					if err := json.Unmarshal(p.env.Payload, &f); err != nil {
						t.Fatal(err)
					}
					if f["text"] == "main" {
						continue
					}
					turnID, _ := f["turn_id"].(string)
					if turnID == "" || turnID == id || (childID != "" && turnID != childID) {
						t.Errorf("inconsistent unknown-origin child identity: %v", f)
					}
					childID = turnID
				}
			})
		}
	}
}

func TestInteractiveTurnEmitterV2_ChildTeardownPerConversation(t *testing.T) {
	t.Parallel()
	for _, open := range []bool{false, true} {
		t.Run(fmt.Sprintf("main-open=%v", open), func(t *testing.T) {
			ctx := context.Background()
			_, bcast, e := newParentLaneEmitter(t, switchConvA)
			for _, conv := range []string{switchConvA, switchConvB} {
				e.HandleFor(ctx, conv, turnevent.ToolStart{ToolCallID: parentLaneA, Title: "Task"})
				if !open {
					e.HandleFor(ctx, conv, turnevent.TurnEnd{})
				}
				e.HandleFor(ctx, conv, turnevent.ToolStart{ToolCallID: "child", ParentToolCallID: parentLaneA})
				e.HandleFor(ctx, conv, turnevent.TextChunk{ParentToolCallID: parentLaneA, Text: conv})
			}
			other := e.turns[switchConvB]
			count := len(bcast.pushes)
			e.closeForConversation(ctx, switchConvA)
			if _, exists := e.turns[switchConvA]; exists {
				t.Fatal("teardown retained child attribution")
			}
			if e.turns[switchConvB] != other || other.deltaBuf.Len() == 0 {
				t.Fatal("teardown affected another conversation")
			}
			want := []string{protocol.TypeAssistantDelta}
			if open {
				want = append(want, protocol.TypeTurnState)
			}
			if got := pushTypes(bcast.pushes[count:]); !slices.Equal(got, want) {
				t.Errorf("teardown frames = %v, want %v", got, want)
			}
			e.HandleFor(ctx, switchConvB, turnevent.ToolProgress{ToolCallID: "child"})
			e.flushAll(ctx)
			if other.inTurn != open {
				t.Fatal("other conversation lost child attribution")
			}
			deltas := assistantDeltas(t, bcast.pushes)
			if len(deltas) != 2 || deltas[0].TurnID == deltas[1].TurnID {
				t.Fatalf("conversation child identities = %+v", deltas)
			}
			e.closeForConversation(ctx, switchConvB)
			if len(e.turns) != 0 || e.anyBuffered() {
				t.Fatal("teardown did not release all conversation state")
			}
		})
	}
}

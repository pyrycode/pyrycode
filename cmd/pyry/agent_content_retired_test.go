package main

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/thread"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func testRetiredAgentType(typ string) bool {
	return typ == protocol.TypeBackgroundTaskStarted || typ == protocol.TypeBackgroundTaskUpdated || typ == protocol.TypeBackgroundTaskRoster
}

func TestAgentContentRetired(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	h := history.New(dir)
	store := thread.NewStore(h)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		defer cancel()
		if err := store.Shutdown(); err != nil {
			t.Error(err)
		}
	})
	b := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "legacy", Interactive: true}, {ConnID: "thread", Interactive: true, Thread: true}, {ConnID: "other"}}}}
	e := newInteractiveTurnEmitterV2(nil, b, discardLogger())
	e.hist, e.runtimeFacts = h, true
	var live []protocol.Envelope
	p := &daemonThreadPublisher{store: store, active: func(context.Context, string) []relay.ActiveConn {
		return []relay.ActiveConn{{ConnID: "thread", Interactive: true, Thread: true}}
	}, push: func(_ context.Context, _ string, env protocol.Envelope) error { live = append(live, env); return nil }, log: discardLogger()}
	for _, conv := range []string{testConvID, testConvIDB} {
		id := conversations.ConversationID(conv)
		if err := store.Load(ctx, id); err != nil {
			t.Fatal(err)
		}
		testShadowWait(t, func() bool { return store.Status(id).State == thread.StateUsable })
		w := &shadowConversation{}
		p.publish(ctx, id, w)
		rows := map[uint64]protocol.ThreadItem{}
		publish := func() []thread.Item {
			t.Helper()
			e.flushAll(ctx)
			entries := historyEntries(t, h, conv)
			version := entries[len(entries)-1].ID
			testShadowWait(t, func() bool { return store.Status(id).Version == version })
			start := len(live)
			p.publish(ctx, id, w)
			for _, env := range live[start:] {
				testRetiredThreadUpdate(t, conv, rows, env)
			}
			caught, err := store.CatchUp(ctx, id, "", 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != len(caught.Items) {
				t.Fatal("live/catch-up row count differs")
			}
			for _, item := range caught.Items {
				testDaemonThreadJSONEqual(t, rows[item.ID], daemonThreadItem(item))
			}
			return caught.Items
		}
		for _, session := range []string{"source-a", "source-b", "source-c"} {
			source := history.SessionProvenance{Kind: "claude", SessionID: session}
			handle := func(ev turnevent.Event) { e.HandleFor(ctx, conv, ev, source) }
			for _, ev := range []turnevent.Event{
				turnevent.TextChunk{MessageID: "main", Text: "main"},
				turnevent.ToolStart{ToolCallID: "call", Title: "Agent"},
				turnevent.ToolUpdate{ToolCallID: "call", Status: turnevent.ToolStatusCompleted, Content: turnevent.TextContent{Text: "launch"}},
				turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn},
				turnevent.BackgroundTaskStarted{TaskID: "task", ToolCallID: "call"},
				turnevent.BackgroundTaskRoster{DroppedTasks: 1},
				turnevent.TextChunk{MessageID: "child", ParentToolCallID: "call", Text: "child", TextComplete: true},
				turnevent.BackgroundTaskProgress{TaskID: "task", ToolUses: 3},
			} {
				handle(ev)
			}
			if e.inTurn || e.turnID != "" || e.currentState != "idle" {
				t.Fatal("child/background traffic reopened main turn")
			}
			items := publish()
			var agent thread.Item
			for _, item := range items {
				if item.Session == session && item.Kind == "agent" {
					agent = item
				}
			}
			if agent.ID == 0 || !agent.Active || agent.Status != "running" || agent.EndedOrder != 0 || !bytes.Contains(agent.Content, []byte("launch")) {
				t.Fatalf("launch result/incomplete roster finished agent: %+v", agent)
			}
			if session == "source-a" {
				handle(turnevent.BackgroundTaskUpdated{TaskID: "task", Status: "completed", Summary: "actual outcome"})
			} else if session == "source-b" {
				handle(turnevent.BackgroundTaskRoster{})
			} else {
				e.closeAgentHistory(ctx, "idle_sleep", historyTS)
			}
			items = publish()
			agents, texts := 0, 0
			for _, item := range items {
				if item.Session != session {
					continue
				}
				if item.Agent != "claude" || item.Order == 0 {
					t.Fatal("lost source attribution/order")
				}
				if item.Kind == "agent" {
					agents++
					want := "finished"
					if session == "source-b" {
						want = "gone"
					} else if session == "source-c" {
						want = "ended_with_session"
						if !bytes.Contains(item.Content, []byte("idle_sleep")) {
							t.Fatal("lost saved ending cause")
						}
					}
					if item.ID != agent.ID || item.Active || item.Status != want || item.EndedOrder == 0 {
						t.Fatalf("lost terminal identity: %+v", item)
					}
				}
				if item.Kind == "assistant_message" {
					texts++
					var text protocol.AssistantDeltaPayload
					if err := json.Unmarshal(item.Content, &text); err != nil {
						t.Fatal(err)
					}
					if text.Text == "child" && (item.Parent != agent.ID || item.Active) {
						t.Fatal("lost child attribution/completion")
					}
				}
			}
			if agents != 1 || texts != 2 {
				t.Fatalf("canonical creations: agents=%d texts=%d", agents, texts)
			}
		}
		for _, entry := range historyEntries(t, h, conv) {
			if entry.Session == nil || entry.Session.Kind != "claude" || entry.TS.IsZero() || entry.Shown == nil {
				t.Fatal("source metadata lost")
			}
		}
		testDaemonThreadJSONEqual(t, historyEntries(t, h, conv), historyEntries(t, history.New(dir), conv))
		events, _ := e.ring.After(conv, 0)
		agentFacts := map[uint64]bool{}
		for _, entry := range historyEntries(t, h, conv) {
			agentFacts[entry.ID] = agentHistoryType(entry.Type)
		}
		for _, event := range events {
			if testRetiredAgentType(event.Type) || event.Type == protocol.TypeBanner && agentFacts[event.HistoryEntryID] {
				t.Fatal("retired agent replay: ", event.Type)
			}
		}
		for _, push := range b.pushes {
			var meta struct {
				ConversationID string `json:"conversation_id"`
			}
			if err := json.Unmarshal(push.env.Payload, &meta); err != nil {
				t.Fatal(err)
			}
			if meta.ConversationID == conv && push.env.Type == protocol.TypeBanner && push.env.HistoryEntryID != nil && agentFacts[*push.env.HistoryEntryID] {
				t.Fatal("agent receipt banner reached app")
			}
		}
	}
	for _, push := range b.pushes {
		if testRetiredAgentType(push.env.Type) {
			t.Fatal("retired agent live frame: ", push.env.Type)
		}
	}
	if len(pushesOfType(b.pushes, protocol.TypeBackgroundTaskProgress)) != 12 {
		t.Fatal("task progress delivery lost")
	}
}

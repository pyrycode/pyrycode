package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"testing"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func testGoneEntries(t *testing.T, store *history.Store, conv string) []history.Entry {
	t.Helper()
	var out []history.Entry
	for _, entry := range historyEntries(t, store, conv) {
		if entry.Type == "background_task_gone" {
			out = append(out, entry)
		}
	}
	return out
}

func TestAgentHistory_GoneRosters(t *testing.T) {
	t.Parallel()
	call := turnevent.ToolStart{Title: "Agent", ToolCallID: "call", ParentToolCallID: "parent"}
	link := turnevent.BackgroundTaskStarted{TaskID: "task", ToolCallID: "call"}
	empty := turnevent.BackgroundTaskRoster{}
	present := turnevent.BackgroundTaskRoster{Tasks: []turnevent.BackgroundTask{{TaskID: "task"}}}
	done := turnevent.BackgroundTaskUpdated{TaskID: "task", Status: "completed"}
	denied := turnevent.ToolCallDenied{ToolCallID: "call"}
	tests := []struct {
		name   string
		events []turnevent.Event
		want   int
	}{
		{"empty", []turnevent.Event{call, link, empty}, 1},
		{"roster-link", []turnevent.Event{call, turnevent.BackgroundTaskRoster{Tasks: []turnevent.BackgroundTask{{TaskID: "task", ToolCallID: "call"}}}, empty}, 1},
		{"truncated-roster-link", []turnevent.Event{call, turnevent.BackgroundTaskRoster{Tasks: []turnevent.BackgroundTask{{TaskID: "task", ToolCallID: "call", TruncatedFields: []string{"tool_call_id"}}}}, empty}, 0},
		{"denied-before-call", []turnevent.Event{denied, call, link, empty}, 0},
		{"reported-before-call", []turnevent.Event{done, call, link, empty}, 0},
		{"main-text-end", []turnevent.Event{turnevent.TextChunk{Text: "main"}, call, link, turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn}, empty}, 1},
		{"other-task", []turnevent.Event{call, link, turnevent.BackgroundTaskRoster{Tasks: []turnevent.BackgroundTask{{TaskID: "other"}}}}, 1},
		{"task-id-not-call-id", []turnevent.Event{call, link, present}, 0},
		{"call-id-does-not-preserve-task", []turnevent.Event{call, link, turnevent.BackgroundTaskRoster{Tasks: []turnevent.BackgroundTask{{TaskID: "call"}}}}, 1},
		{"no-refresh", []turnevent.Event{call, link}, 0},
		{"refresh-before-link", []turnevent.Event{call, empty, link}, 0},
		{"later-refresh", []turnevent.Event{call, empty, link, empty}, 1},
		{"link-before-call", []turnevent.Event{link, empty, call}, 0},
		{"link-before-call-later-refresh", []turnevent.Event{link, empty, call, empty}, 1},
		{"unobserved-call", []turnevent.Event{link, empty}, 0},
		{"ordinary-tool", []turnevent.Event{turnevent.ToolStart{Title: "Bash", ToolCallID: "call"}, link, empty}, 0},
		{"task-tool", []turnevent.Event{turnevent.ToolStart{Title: "Task", ToolCallID: "call"}, link, empty}, 1},
		{"launch-result-before-link", []turnevent.Event{call, turnevent.ToolUpdate{ToolCallID: "call", Status: turnevent.ToolStatusCompleted}, link, empty}, 1},
		{"launch-result-after-link", []turnevent.Event{call, link, turnevent.ToolUpdate{ToolCallID: "call", Status: turnevent.ToolStatusFailed}, empty}, 1},
		{"parent-from-result", []turnevent.Event{turnevent.ToolStart{Title: "Agent", ToolCallID: "call"}, turnevent.ToolUpdate{ToolCallID: "call", ParentToolCallID: "parent", Status: turnevent.ToolStatusCompleted}, link, empty}, 1},
		{"main-end", []turnevent.Event{call, link, turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn}, empty}, 1},
		{"ended-before-link", []turnevent.Event{call, done, link, empty}, 0},
		{"ended-after-link", []turnevent.Event{call, link, done, empty}, 0},
		{"unknown-reported-status", []turnevent.Event{call, turnevent.BackgroundTaskUpdated{TaskID: "task", Status: "future-status"}, link, empty}, 0},
		{"denied-before-link", []turnevent.Event{call, denied, link, empty}, 0},
		{"denied-after-link", []turnevent.Event{call, link, denied, empty}, 0},
		{"truncated-denial", []turnevent.Event{call, link, turnevent.ToolCallDenied{ToolCallID: "call", TruncatedFields: []string{"tool_call_id"}}, empty}, 1},
		{"repeated-late", []turnevent.Event{call, link, empty, empty, done, call, link, empty}, 1},
		{"repeated-task-different-call", []turnevent.Event{call, link, empty, turnevent.ToolStart{Title: "Agent", ToolCallID: "other-call"}, turnevent.BackgroundTaskStarted{TaskID: "task", ToolCallID: "other-call"}, empty}, 1},
		{"same-ended-call-new-task", []turnevent.Event{call, link, empty, turnevent.BackgroundTaskStarted{TaskID: "new-task", ToolCallID: "call"}, empty}, 1},
		{"status-empty", []turnevent.Event{call, link, turnevent.BackgroundTaskUpdated{TaskID: "task", Summary: "summary"}, empty}, 1},
		{"incomplete-count", []turnevent.Event{call, link, turnevent.BackgroundTaskRoster{DroppedTasks: 1}}, 0},
		{"incomplete-row", []turnevent.Event{call, link, turnevent.BackgroundTaskRoster{Tasks: []turnevent.BackgroundTask{{}}}}, 0},
		{"incomplete-row-id", []turnevent.Event{call, link, turnevent.BackgroundTaskRoster{Tasks: []turnevent.BackgroundTask{{TaskID: "other", TruncatedFields: []string{"task_id"}}}}}, 0},
		{"complete-after-incomplete", []turnevent.Event{call, link, turnevent.BackgroundTaskRoster{DroppedTasks: 1}, empty}, 1},
		{"description-cut", []turnevent.Event{call, turnevent.BackgroundTaskStarted{TaskID: "task", ToolCallID: "call", TruncatedFields: []string{"description"}}, turnevent.BackgroundTaskRoster{Tasks: []turnevent.BackgroundTask{{TaskID: "other", TruncatedFields: []string{"description"}}}}}, 1},
	}
	for _, field := range []string{"task_id", "tool_call_id"} {
		bad := link
		bad.TruncatedFields = []string{field}
		tests = append(tests, struct {
			name   string
			events []turnevent.Event
			want   int
		}{"cut-start-" + field, []turnevent.Event{call, bad, present, empty}, 0})
	}
	for _, bad := range []turnevent.BackgroundTaskStarted{{ToolCallID: "call"}, {TaskID: "task"}} {
		tests = append(tests, struct {
			name   string
			events []turnevent.Event
			want   int
		}{fmt.Sprintf("missing-start-%+v", bad), []turnevent.Event{call, bad, present, empty}, 0})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			store := history.New(dir)
			e := newInteractiveTurnEmitterV2(nil, historyOnlyBroadcaster{}, discardLogger())
			e.hist, e.runtimeFacts = store, true
			source := history.SessionProvenance{Kind: "claude", SessionID: "source"}
			hold := newSessionBackgroundTaskHold(func(ev turnevent.Event) { e.HandleFor(context.Background(), testConvID, ev, source) })
			for _, ev := range tt.events {
				hold.Sink(ev)
			}
			for _, reader := range []*history.Store{store, history.New(dir)} {
				entries := testGoneEntries(t, reader, testConvID)
				if len(entries) != tt.want {
					t.Fatalf("gone=%d want=%d", len(entries), tt.want)
				}
				for _, entry := range entries {
					var p agentHistoryFact
					if err := json.Unmarshal(entry.Payload, &p); err != nil {
						t.Fatal(err)
					}
					if p.TaskID != "task" || p.ToolCallID != "call" || p.Status != "gone" || p.ConversationID != testConvID || !conversations.ValidID(p.LifetimeID) || !p.OccurredAt.Equal(entry.TS) || entry.Session == nil || *entry.Session != source || entry.Shown == nil || !*entry.Shown {
						t.Fatalf("incomplete gone fact: %+v %+v", p, entry)
					}
					if tt.name != "task-tool" && p.ParentToolCallID != "parent" {
						t.Fatalf("lost parent: %+v", p)
					}
					for _, old := range historyEntries(t, reader, testConvID) {
						if old.Type == historyTaskLinked {
							var linked agentHistoryFact
							_ = json.Unmarshal(old.Payload, &linked)
							if linked.TaskID == p.TaskID && linked.LifetimeID != p.LifetimeID {
								t.Fatal("link lifetime changed")
							}
						}
					}
				}
			}
		})
	}
}

func TestAgentHistory_GoneIsolation(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := history.New(dir)
	e := newInteractiveTurnEmitterV2(nil, historyOnlyBroadcaster{}, discardLogger())
	e.hist, e.runtimeFacts = store, true
	feed := func(conv, session, kind string, inc uint64, ev turnevent.Event) {
		e.handleForSource(context.Background(), conv, ev, history.SessionProvenance{Kind: kind, SessionID: session}, inc)
	}
	for _, key := range []struct {
		conv, session, kind string
		inc                 uint64
	}{
		{testConvID, "a", "claude", 1}, {testConvID, "b", "claude", 1}, {testConvIDB, "a", "claude", 1}, {testConvID, "a", "claude", 2}, {testConvID, "c", "codex", 1},
	} {
		feed(key.conv, key.session, key.kind, key.inc, turnevent.ToolStart{Title: "Agent", ToolCallID: "call"})
		feed(key.conv, key.session, key.kind, key.inc, turnevent.BackgroundTaskStarted{TaskID: "task", ToolCallID: "call"})
	}
	feed(testConvID, "a", "claude", 1, turnevent.BackgroundTaskRoster{})
	if len(testGoneEntries(t, store, testConvID)) != 1 || len(testGoneEntries(t, store, testConvIDB)) != 0 {
		t.Fatal("refresh leaked across source/conversation/lifetime")
	}
	for _, key := range []struct {
		conv, session, kind string
		inc                 uint64
	}{
		{testConvID, "b", "claude", 1}, {testConvIDB, "a", "claude", 1}, {testConvID, "a", "claude", 2}, {testConvID, "c", "codex", 1},
	} {
		feed(key.conv, key.session, key.kind, key.inc, turnevent.BackgroundTaskRoster{})
	}
	e.closeRuntimeSource(context.Background(), testConvID, "a", "idle_sleep", historyTS, true, 0, 3)
	feed(testConvID, "a", "claude", 3, turnevent.ToolCallDenied{ToolCallID: "call"})
	feed(testConvID, "a", "claude", 3, turnevent.ToolStart{Title: "Agent", ToolCallID: "call"})
	feed(testConvID, "a", "claude", 3, turnevent.BackgroundTaskStarted{TaskID: "task", ToolCallID: "call"})
	feed(testConvID, "a", "claude", 3, turnevent.BackgroundTaskRoster{})
	e = newInteractiveTurnEmitterV2(nil, historyOnlyBroadcaster{}, discardLogger())
	e.hist, e.runtimeFacts = store, true
	feed(testConvID, "a", "claude", 1, turnevent.ToolStart{Title: "Agent", ToolCallID: "call"})
	feed(testConvID, "a", "claude", 1, turnevent.BackgroundTaskStarted{TaskID: "task", ToolCallID: "call"})
	feed(testConvID, "a", "claude", 1, turnevent.BackgroundTaskRoster{})
	for _, reader := range []*history.Store{store, history.New(dir)} {
		seen := map[string]bool{}
		for _, conv := range []string{testConvID, testConvIDB} {
			entries := testGoneEntries(t, reader, conv)
			want := 4
			if conv == testConvIDB {
				want = 1
			}
			if len(entries) != want {
				t.Fatalf("gone=%d want=%d", len(entries), want)
			}
			for _, entry := range entries {
				var p agentHistoryFact
				_ = json.Unmarshal(entry.Payload, &p)
				if seen[p.LifetimeID] {
					t.Fatal("reused durable lifetime")
				}
				seen[p.LifetimeID] = true
			}
		}
	}
}

func TestAgentHistory_GoneHousekeeping(t *testing.T) {
	t.Parallel()
	store := history.New(t.TempDir())
	e := newInteractiveTurnEmitterV2(nil, historyOnlyBroadcaster{}, discardLogger())
	e.hist, e.runtimeFacts = store, true
	hold := newSessionBackgroundTaskHold(func(ev turnevent.Event) {
		e.HandleFor(context.Background(), testConvID, ev, history.SessionProvenance{Kind: "claude", SessionID: "source"})
	})
	hold.Sink(turnevent.ToolStart{Title: "Agent", ToolCallID: "call"})
	hold.Sink(turnevent.BackgroundTaskStarted{TaskID: "task", ToolCallID: "call"})
	for i := 0; i < maxBackgroundTaskJoins+1; i++ {
		hold.Sink(turnevent.BackgroundTaskStarted{TaskID: fmt.Sprint(i), ToolCallID: fmt.Sprint(i)})
	}
	// Incomplete refresh prunes the hold's joins but supplies no absence evidence.
	hold.Sink(turnevent.BackgroundTaskRoster{DroppedTasks: 1})
	for i := 0; i < 3; i++ {
		_, _ = hold.BackgroundTaskRoster()
	}
	hold.childExited()
	_, _ = hold.BackgroundTaskRoster()
	if len(testGoneEntries(t, store, testConvID)) != 0 {
		t.Fatal("housekeeping inferred ending")
	}
	hold.Sink(turnevent.BackgroundTaskRoster{})
	if len(testGoneEntries(t, store, testConvID)) != 1 {
		t.Fatal("housekeeping discarded emitter eligibility")
	}
}

func TestAgentHistory_GoneChildLifetimeReset(t *testing.T) {
	t.Parallel()
	for _, reset := range []string{"child-exit", "conversation-close"} {
		for _, tt := range []struct {
			name     string
			ending   turnevent.Event
			relaunch bool
			want     int
		}{
			{"gone-reused-ids", turnevent.BackgroundTaskRoster{}, true, 2},
			{"reported-reused-ids", turnevent.BackgroundTaskUpdated{TaskID: "task", Status: "completed"}, true, 1},
			{"denied-reused-ids", turnevent.ToolCallDenied{ToolCallID: "call"}, true, 1},
			{"old-link-with-unrelated-denial", turnevent.ToolCallDenied{ToolCallID: "unrelated"}, false, 0},
		} {
			t.Run(reset+"/"+tt.name, func(t *testing.T) {
				dir := t.TempDir()
				store := history.New(dir)
				e := newInteractiveTurnEmitterV2(nil, historyOnlyBroadcaster{}, discardLogger())
				e.hist, e.runtimeFacts = store, true
				ctx := context.Background()
				source := history.SessionProvenance{Kind: "claude", SessionID: "source"}
				hold := newSessionBackgroundTaskHold(func(ev turnevent.Event) {
					e.handleForSource(ctx, testConvID, ev, source, 1)
				})
				hold.Sink(turnevent.ToolStart{Title: "Agent", ToolCallID: "call", ParentToolCallID: "old-parent"})
				hold.Sink(turnevent.BackgroundTaskStarted{TaskID: "task", ToolCallID: "call"})
				hold.Sink(tt.ending)
				oldLifetime := e.agentLifetime
				before := len(testGoneEntries(t, store, testConvID))
				hold.childExited()
				if reset == "child-exit" {
					e.closeRuntimeSource(ctx, testConvID, source.SessionID, "child_exit", historyTS, false, 0, 1)
				} else {
					e.closeForConversation(ctx, testConvID)
				}
				if len(testGoneEntries(t, store, testConvID)) != before {
					t.Fatal("reset alone inferred gone")
				}
				// Automatic respawns keep the runner's source and incarnation.
				if tt.relaunch {
					hold.Sink(turnevent.ToolStart{Title: "Agent", ToolCallID: "call"})
					hold.Sink(turnevent.BackgroundTaskStarted{TaskID: "task", ToolCallID: "call"})
				}
				hold.Sink(turnevent.BackgroundTaskRoster{})
				hold.Sink(turnevent.BackgroundTaskRoster{})
				for _, reader := range []*history.Store{store, history.New(dir)} {
					entries := testGoneEntries(t, reader, testConvID)
					if len(entries) != tt.want {
						t.Fatalf("gone=%d want=%d after %s", len(entries), tt.want, reset)
					}
					if !tt.relaunch {
						continue
					}
					var gone agentHistoryFact
					entry := entries[len(entries)-1]
					if err := json.Unmarshal(entry.Payload, &gone); err != nil {
						t.Fatal(err)
					}
					links := testAgentFacts(t, reader, testConvID)[historyTaskLinked]
					if gone.LifetimeID == oldLifetime || gone.LifetimeID != links[len(links)-1]["lifetime_id"] || gone.ParentToolCallID != "" || gone.TaskID != "task" || gone.ToolCallID != "call" || entry.Session == nil || *entry.Session != source {
						t.Fatalf("replacement inherited old child evidence: %+v", gone)
					}
				}
			})
		}
	}
}

func TestAgentHistory_GoneSealedLifetime(t *testing.T) {
	t.Parallel()
	for _, ended := range []bool{false, true} {
		t.Run(fmt.Sprintf("ended=%t", ended), func(t *testing.T) {
			dir := t.TempDir()
			store := history.New(dir)
			e := newInteractiveTurnEmitterV2(nil, historyOnlyBroadcaster{}, discardLogger())
			e.hist, e.runtimeFacts = store, true
			ctx := context.Background()
			source := history.SessionProvenance{Kind: "claude", SessionID: "source"}
			old := newSessionBackgroundTaskHold(func(ev turnevent.Event) { e.handleForSource(ctx, testConvID, ev, source, 1) })
			old.Sink(turnevent.ToolStart{Title: "Agent", ToolCallID: "call", ParentToolCallID: "old-parent"})
			old.Sink(turnevent.BackgroundTaskStarted{TaskID: "task", ToolCallID: "call"})
			if ended {
				old.Sink(turnevent.BackgroundTaskUpdated{TaskID: "task", Status: "completed"})
			}
			oldLifetime := e.agentLifetime
			e.closeRuntimeSource(ctx, testConvID, source.SessionID, "idle_sleep", historyTS, true, 0, 1)
			next := newSessionBackgroundTaskHold(func(ev turnevent.Event) { e.handleForSource(ctx, testConvID, ev, source, 2) })
			next.Sink(turnevent.ToolStart{Title: "Agent", ToolCallID: "call"})
			next.Sink(turnevent.BackgroundTaskStarted{TaskID: "task", ToolCallID: "call"})
			nextLifetime := e.agentLifetime
			old.Sink(turnevent.BackgroundTaskRoster{})
			old.Sink(turnevent.BackgroundTaskRoster{})
			next.Sink(turnevent.BackgroundTaskRoster{})
			for _, reader := range []*history.Store{store, history.New(dir)} {
				byLifetime := map[string]int{}
				for _, entry := range testGoneEntries(t, reader, testConvID) {
					var p agentHistoryFact
					if err := json.Unmarshal(entry.Payload, &p); err != nil {
						t.Fatal(err)
					}
					byLifetime[p.LifetimeID]++
					if p.LifetimeID == oldLifetime && p.ParentToolCallID != "old-parent" {
						t.Fatal("sealed predecessor lost call evidence")
					}
				}
				if oldLifetime == nextLifetime || byLifetime[oldLifetime] != 1-testBoolInt(ended) || byLifetime[nextLifetime] != 1 || len(byLifetime) != 2-testBoolInt(ended) {
					t.Fatalf("sealed evidence changed or leaked to successor: %v", byLifetime)
				}
			}
		})
	}
}

func TestAgentHistory_GoneReceiptBarriers(t *testing.T) {
	t.Parallel()
	valid, err := json.Marshal(agentHistoryFact{ConversationID: testConvID, LifetimeID: testConvIDB, ToolCallID: "call", TaskID: "task", Status: "gone", OccurredAt: historyTS})
	if err != nil {
		t.Fatal(err)
	}
	id := uint64(2)
	for _, tt := range []struct {
		name, typ string
		raw       json.RawMessage
		want      uint64
	}{
		{"valid", "background_task_gone", valid, 3},
		{"malformed", "background_task_gone", json.RawMessage(`{}`), 0},
		{"unrelated", "unrelated_unknown", valid, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			base := []protocol.HistoryEntry{{ID: 1, Type: protocol.TypeMessage, Payload: json.RawMessage(`{}`)}, {ID: 3, Type: protocol.TypeMessage, Payload: json.RawMessage(`{}`)}}
			if receipt, ok := legacyRuntimeReceipt(testConvID, tt.typ, tt.raw, &id, historyTS); ok {
				base = append(base, protocol.HistoryEntry{ID: id, Type: protocol.TypeBanner, Payload: receipt})
			}
			if got := testLegacyCheckpoint(base, 3, 0, false); got != tt.want {
				t.Fatalf("checkpoint=%d want=%d", got, tt.want)
			}
		})
	}
	for _, status := range []string{"", "completed", "unknown"} {
		var p agentHistoryFact
		_ = json.Unmarshal(valid, &p)
		p.Status = status
		raw, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := legacyRuntimeReceipt(testConvID, "background_task_gone", raw, &id, historyTS); ok {
			t.Fatalf("invalid gone status %q accepted", status)
		}
	}
	for _, check := range []struct {
		id *uint64
		ts bool
	}{{nil, true}, {new(uint64), true}, {&id, false}} {
		ts := historyTS
		if !check.ts {
			ts = (agentHistoryFact{}).OccurredAt
		}
		if _, ok := legacyRuntimeReceipt(testConvID, "background_task_gone", valid, check.id, ts); ok {
			t.Fatal("missing durable storage identity accepted")
		}
	}
}

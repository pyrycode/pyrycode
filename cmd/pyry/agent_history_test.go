package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/turnbridge"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func testAgentFacts(t *testing.T, store *history.Store, conv string) map[string][]map[string]string {
	t.Helper()
	facts := map[string][]map[string]string{}
	for _, entry := range historyEntries(t, store, conv) {
		if !strings.HasPrefix(entry.Type, "agent_call_") && !strings.HasPrefix(entry.Type, "background_task_o") && entry.Type != "background_task_linked" && entry.Type != "background_task_gone" {
			continue
		}
		var p map[string]string
		if err := json.Unmarshal(entry.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if p["conversation_id"] != conv || p["lifetime_id"] == "" || p["occurred_at"] == "" {
			t.Fatalf("incomplete fact: %s", entry.Payload)
		}
		shown := entry.Type == "agent_call_result" || entry.Type == "agent_call_denied" || entry.Type == "background_task_outcome" || entry.Type == "background_task_gone"
		if entry.Shown == nil || *entry.Shown != shown || entry.Session == nil || entry.Session.Kind != "claude" {
			t.Fatalf("metadata: %+v", entry)
		}
		facts[entry.Type] = append(facts[entry.Type], p)
	}
	return facts
}

func TestAgentHistory_ReportedOrdering(t *testing.T) {
	t.Parallel()
	source := history.SessionProvenance{Kind: "claude", SessionID: "source"}
	for _, order := range [][]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}} {
		for _, status := range []string{"completed", "failed", "stopped", "unfamiliar-terminal"} {
			t.Run(fmt.Sprint(order)+status, func(t *testing.T) {
				dir := t.TempDir()
				store := history.New(dir)
				e := newInteractiveTurnEmitterV2(nil, historyOnlyBroadcaster{}, discardLogger())
				e.hist, e.runtimeFacts = store, true
				ctx := context.Background()
				e.HandleFor(ctx, testConvID, turnevent.ToolStart{ToolCallID: "call", Title: "Agent", ParentToolCallID: "parent", RawInput: json.RawMessage(`{"secret":"input"}`)}, source)
				e.HandleFor(ctx, testConvID, turnevent.TextChunk{Text: "main"}, source)
				reports := []turnevent.Event{
					turnevent.ToolUpdate{ToolCallID: "call", ParentToolCallID: "parent", Status: turnevent.ToolStatusCompleted},
					turnevent.BackgroundTaskStarted{TaskID: "task", ToolCallID: "call", Description: "original description"},
					turnevent.BackgroundTaskUpdated{TaskID: "task", Status: status, Summary: "original outcome"},
				}
				for _, i := range order {
					e.HandleFor(ctx, testConvID, reports[i], source)
					if !e.inTurn || e.currentState != turnbridge.StateResponding {
						t.Fatal("report changed open main state")
					}
				}
				// Open and end main work independently of the child/background reports.
				e.HandleFor(ctx, testConvID, turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn}, source)
				for _, report := range []turnevent.Event{turnevent.BackgroundTaskUpdated{TaskID: "task", Patch: `{"is_backgrounded":true}`}, turnevent.BackgroundTaskProgress{TaskID: "task"}, turnevent.BackgroundTaskRoster{Tasks: []turnevent.BackgroundTask{{TaskID: "task", ToolCallID: "call"}}}} {
					e.HandleFor(ctx, testConvID, report, source)
					if e.inTurn || e.currentState != "" {
						t.Fatal("background report changed idle main state")
					}
				}
				for _, reader := range []*history.Store{store, history.New(dir)} {
					facts := testAgentFacts(t, reader, testConvID)
					for _, typ := range []string{"agent_call_observed", "agent_call_result", "background_task_outcome"} {
						if len(facts[typ]) != 1 {
							t.Fatalf("%s count: %+v", typ, facts)
						}
					}
					call, result, outcome := facts["agent_call_observed"][0], facts["agent_call_result"][0], facts["background_task_outcome"][0]
					if call["tool"] != "Agent" || call["parent_tool_call_id"] != "parent" || result["status"] != "completed" || outcome["status"] != status || outcome["task_id"] != "task" {
						t.Fatalf("reports lost: %+v", facts)
					}
					for _, rows := range facts {
						for _, row := range rows {
							if row["lifetime_id"] != call["lifetime_id"] {
								t.Fatal("turn end/order split lifetime")
							}
						}
					}
					if len(facts["background_task_linked"]) != 2 || facts["background_task_linked"][0]["tool_call_id"] != "call" {
						t.Fatalf("links: %+v", facts)
					}
					for _, report := range reports {
						typ, p, ok := turnbridge.MapEvent(report, turnbridge.TurnContext{ConversationID: testConvID})
						if !ok {
							t.Fatal("unmapped fixture")
						}
						var want map[string]any
						b, _ := json.Marshal(p)
						_ = json.Unmarshal(b, &want)
						found := false
						for _, entry := range historyEntries(t, reader, testConvID) {
							if entry.Type != typ {
								continue
							}
							var got map[string]any
							_ = json.Unmarshal(entry.Payload, &got)
							delete(got, "turn_id")
							delete(want, "turn_id")
							if reflect.DeepEqual(got, want) {
								found = true
							}
						}
						if !found {
							t.Fatalf("mapped report changed: %s %+v", typ, want)
						}
					}
				}
			})
		}
	}
	for _, status := range []turnevent.ToolStatus{turnevent.ToolStatusCompleted, turnevent.ToolStatusFailed} {
		for _, denied := range []bool{false, true} {
			t.Run(fmt.Sprint(status, denied), func(t *testing.T) {
				store := history.New(t.TempDir())
				e := newInteractiveTurnEmitterV2(nil, historyOnlyBroadcaster{}, discardLogger())
				e.hist, e.runtimeFacts = store, true
				e.HandleFor(context.Background(), testConvID, turnevent.ToolStart{Title: "Task", ToolCallID: "fg"}, source)
				e.HandleFor(context.Background(), testConvID, turnevent.ToolUpdate{ToolCallID: "fg"}, source)
				if denied {
					e.HandleFor(context.Background(), testConvID, turnevent.ToolCallDenied{ToolCallID: "fg", ToolName: "Task"}, source)
				}
				e.HandleFor(context.Background(), testConvID, turnevent.ToolUpdate{ToolCallID: "fg", Status: status}, source)
				facts := testAgentFacts(t, store, testConvID)
				if len(facts["agent_call_result"]) != 1 || facts["agent_call_result"][0]["status"] != string(status) || len(facts["background_task_linked"]) != 0 {
					t.Fatalf("foreground result: %+v", facts)
				}
				if len(facts["agent_call_denied"]) != testBoolInt(denied) {
					t.Fatalf("denial: %+v", facts)
				}
			})
		}
	}
}

func testBoolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func TestAgentHistory_IncompleteIdentities(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                      string
		ev                        turnevent.Event
		observed, linked, outcome int
		task                      string
	}{
		{"start", turnevent.BackgroundTaskStarted{TaskID: "task", ToolCallID: "call", TruncatedFields: []string{"description"}}, 1, 1, 0, "task"},
		{"update-first", turnevent.BackgroundTaskUpdated{TaskID: "task", Status: "failed"}, 1, 0, 1, "task"},
		{"progress-first", turnevent.BackgroundTaskProgress{TaskID: "task"}, 1, 0, 0, "task"},
		{"roster-first", turnevent.BackgroundTaskRoster{Tasks: []turnevent.BackgroundTask{{TaskID: "task", ToolCallID: "call"}}}, 1, 1, 0, "task"},
		{"cut-task", turnevent.BackgroundTaskStarted{TaskID: "task", ToolCallID: "call", TruncatedFields: []string{"task_id"}}, 0, 0, 0, ""},
		{"cut-call", turnevent.BackgroundTaskStarted{TaskID: "task", ToolCallID: "call", TruncatedFields: []string{"tool_call_id"}}, 1, 0, 0, "task"},
		{"missing-task", turnevent.BackgroundTaskStarted{ToolCallID: "call"}, 0, 0, 0, ""},
		{"missing-call", turnevent.BackgroundTaskStarted{TaskID: "task"}, 1, 0, 0, "task"},
		{"cut-update-task", turnevent.BackgroundTaskUpdated{TaskID: "task", Status: "completed", TruncatedFields: []string{"task_id"}}, 0, 0, 0, ""},
		{"cut-progress-task", turnevent.BackgroundTaskProgress{TaskID: "task", TruncatedFields: []string{"task_id"}}, 0, 0, 0, ""},
		{"cut-roster-task", turnevent.BackgroundTaskRoster{Tasks: []turnevent.BackgroundTask{{TaskID: "task", ToolCallID: "call", TruncatedFields: []string{"task_id"}}}}, 0, 0, 0, ""},
		{"cut-roster-call", turnevent.BackgroundTaskRoster{Tasks: []turnevent.BackgroundTask{{TaskID: "task", ToolCallID: "call", TruncatedFields: []string{"tool_call_id"}}}}, 1, 0, 0, "task"},
		{"empty-status", turnevent.BackgroundTaskUpdated{TaskID: "task", Summary: "summary", Patch: "patch"}, 1, 0, 0, "task"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			store := history.New(dir)
			e := newInteractiveTurnEmitterV2(nil, historyOnlyBroadcaster{}, discardLogger())
			e.hist, e.runtimeFacts = store, true
			e.HandleFor(context.Background(), testConvID, tt.ev, history.SessionProvenance{Kind: "claude", SessionID: "source"})
			if e.inTurn {
				t.Fatal("background opened turn")
			}
			for _, reader := range []*history.Store{store, history.New(dir)} {
				facts := testAgentFacts(t, reader, testConvID)
				if len(facts["background_task_observed"]) != tt.observed || len(facts["background_task_linked"]) != tt.linked || len(facts["background_task_outcome"]) != tt.outcome {
					t.Fatalf("unexpected facts: %+v", facts)
				}
				for _, row := range facts["background_task_observed"] {
					if row["task_id"] != tt.task || row["tool_call_id"] != "" {
						t.Fatalf("identity invented link: %+v", row)
					}
				}
			}
		})
	}
	for _, fields := range [][]string{{"tool_call_id"}, {"message"}} {
		t.Run(fmt.Sprint(fields), func(t *testing.T) {
			store := history.New(t.TempDir())
			e := newInteractiveTurnEmitterV2(nil, historyOnlyBroadcaster{}, discardLogger())
			e.hist, e.runtimeFacts = store, true
			source := history.SessionProvenance{Kind: "claude", SessionID: "source"}
			ctx := context.Background()
			e.HandleFor(ctx, testConvID, turnevent.ToolStart{Title: "Agent", ToolCallID: "call"}, source)
			e.HandleFor(ctx, testConvID, turnevent.ToolCallDenied{ToolCallID: "call", DroppedFields: fields}, source)
			want := 1
			if fields[0] == "tool_call_id" {
				want = 0
			}
			if got := len(testAgentFacts(t, store, testConvID)["agent_call_denied"]); got != want {
				t.Fatalf("denials=%d want=%d", got, want)
			}
		})
	}
}

func TestAgentHistory_Isolation(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := history.New(dir)
	ctx := context.Background()
	e := newInteractiveTurnEmitterV2(nil, historyOnlyBroadcaster{}, discardLogger())
	e.hist, e.runtimeFacts = store, true
	feed := func(conv, session string, inc uint64) {
		source := history.SessionProvenance{Kind: "claude", SessionID: session}
		e.handleForSource(ctx, conv, turnevent.ToolStart{Title: "Agent", ToolCallID: "call"}, source, inc)
		e.handleForSource(ctx, conv, turnevent.BackgroundTaskStarted{TaskID: "task", ToolCallID: "call"}, source, inc)
		e.handleForSource(ctx, conv, turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn}, source, inc)
		e.handleForSource(ctx, conv, turnevent.BackgroundTaskUpdated{TaskID: "task", Status: "completed"}, source, inc)
		if e.inTurn {
			t.Fatal("late task outcome reopened main turn")
		}
	}
	feed(testConvID, "a", 1)
	feed(testConvID, "b", 1)
	feed(testConvIDB, "a", 1)
	feed(testConvID, "a", 2)
	e.closeRuntimeSource(ctx, testConvID, "a", "child_exit", time.Now().UTC(), false, 0, 2)
	feed(testConvID, "a", 2)
	// Restarting the observer resets local incarnation numbering, never durable identity.
	e = newInteractiveTurnEmitterV2(nil, historyOnlyBroadcaster{}, discardLogger())
	e.hist, e.runtimeFacts = history.New(dir), true
	feed(testConvID, "a", 1)
	for _, kind := range []string{"codex", ""} {
		source := history.SessionProvenance{Kind: kind}
		e.HandleFor(ctx, testConvID, turnevent.ToolStart{Title: "Agent", ToolCallID: "excluded"}, source)
		e.HandleFor(ctx, testConvID, turnevent.BackgroundTaskStarted{TaskID: "excluded", ToolCallID: "excluded"}, source)
	}
	for _, reader := range []*history.Store{store, history.New(dir)} {
		seen := map[string]bool{}
		for _, conv := range []string{testConvID, testConvIDB} {
			facts := testAgentFacts(t, reader, conv)
			if len(facts["agent_call_observed"]) != map[string]int{testConvID: 5, testConvIDB: 1}[conv] {
				t.Fatalf("isolation: %+v", facts)
			}
			for i, row := range facts["agent_call_observed"] {
				life := row["lifetime_id"]
				if seen[life] {
					t.Fatal("reused durable lifetime")
				}
				seen[life] = true
				if facts["background_task_linked"][i]["lifetime_id"] != life || facts["background_task_outcome"][i]["lifetime_id"] != life {
					t.Fatal("link crossed lifetime")
				}
			}
		}
	}
}

func TestAgentHistory_StorageFailure(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"nil", "failed", "history-only"} {
		t.Run(mode, func(t *testing.T) {
			var logs bytes.Buffer
			logger := bufLogger(&logs)
			var store *history.Store
			if mode != "nil" {
				root := filepath.Join(t.TempDir(), "private-history-path-sentinel")
				if mode == "failed" {
					if err := os.WriteFile(root, []byte("x"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				store = history.New(root)
			}
			bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "phone", Interactive: true}}}}
			e := newInteractiveTurnEmitterV2(nil, bcast, logger)
			if mode == "history-only" {
				e.bcast = historyOnlyBroadcaster{}
			}
			e.hist, e.runtimeFacts = store, true
			source := history.SessionProvenance{Kind: "claude", SessionID: "source"}
			e.HandleFor(context.Background(), testConvID, turnevent.ToolStart{Title: "Agent", ToolCallID: "call-id-secret-sentinel"}, source)
			e.HandleFor(context.Background(), testConvID, turnevent.BackgroundTaskStarted{TaskID: "task-id-secret-sentinel", ToolCallID: "call-id-secret-sentinel", Description: "description-secret-sentinel"}, source)
			e.HandleFor(context.Background(), testConvID, turnevent.BackgroundTaskRoster{}, source)
			e.HandleFor(context.Background(), testConvID, turnevent.ToolStart{Title: "Agent", ToolCallID: "ending-secret-sentinel"}, source)
			e.closeAgentHistory(context.Background(), "idle_sleep", historyTS)
			if mode == "history-only" {
				if len(testGoneEntries(t, store, testConvID)) != 1 {
					t.Fatal("relay-disabled gone missing")
				}
				if len(testAgentFacts(t, store, testConvID)["background_task_linked"]) != 1 {
					t.Fatal("relay-disabled fact missing")
				}
			} else {
				if len(bcast.pushes) != 1 || bcast.pushes[0].env.Type != protocol.TypeTurnState {
					t.Fatal("storage failure restored raw task content or receipts")
				}
			}
			if mode == "failed" && logs.Len() == 0 {
				t.Fatal("missing failure log")
			}
			for _, secret := range []string{"private-history-path-sentinel", "task-id-secret-sentinel", "call-id-secret-sentinel", "description-secret-sentinel"} {
				if strings.Contains(logs.String(), secret) {
					t.Fatal("content-bearing failure log")
				}
			}
		})
	}
}

func TestAgentHistory_SealedReports(t *testing.T) {
	t.Parallel()
	for _, mainBeforeSeal := range []bool{false, true} {
		for _, tool := range []string{"Agent", "Task"} {
			for _, status := range []turnevent.ToolStatus{turnevent.ToolStatusCompleted, turnevent.ToolStatusFailed} {
				t.Run(fmt.Sprintf("first-after-seal/main=%t/%s/%s", mainBeforeSeal, tool, status), func(t *testing.T) {
					dir := t.TempDir()
					store := history.New(dir)
					e := newInteractiveTurnEmitterV2(nil, historyOnlyBroadcaster{}, discardLogger())
					e.hist, e.runtimeFacts = store, true
					ctx := context.Background()
					source := history.SessionProvenance{Kind: "claude", SessionID: "reused"}
					if mainBeforeSeal {
						e.handleForSource(ctx, testConvID, turnevent.ThoughtChunk{}, source, 1)
					}
					e.closeRuntimeSource(ctx, testConvID, "reused", "idle_sleep", historyTS, true, 0, 1)
					e.handleForSource(ctx, testConvID, turnevent.ToolStart{Title: tool, ToolCallID: "call"}, source, 2)
					e.handleForSource(ctx, testConvID, turnevent.TextChunk{Text: "successor"}, source, 2)
					successor := e.convTurnState
					turnID, lifetime, phaseOrder := e.turnID, e.agentLifetime, e.nextPhaseOrder
					for _, ev := range []turnevent.Event{
						turnevent.ToolStart{Title: tool, ToolCallID: "call", ParentToolCallID: "parent"},
						turnevent.ToolCallDenied{ToolCallID: "call", ToolName: tool},
						turnevent.ToolUpdate{ToolCallID: "call", ParentToolCallID: "parent", Status: status},
					} {
						e.handleForSource(ctx, testConvID, ev, source, 1)
						if e.inTurn || e.turnID != "" || e.currentState != "" {
							t.Fatal("sealed call reopened main work")
						}
						if !successor.inTurn || successor.turnID != turnID || successor.currentState != turnbridge.StateResponding || successor.agentLifetime != lifetime || e.nextPhaseOrder != phaseOrder {
							t.Fatal("sealed call changed successor state")
						}
					}
					for _, reader := range []*history.Store{store, history.New(dir)} {
						facts := testAgentFacts(t, reader, testConvID)
						if len(facts[historyAgentObserved]) != 2 || len(facts[historyAgentResult]) != 1 || len(facts[historyAgentDenied]) != 1 {
							t.Fatalf("sealed first-call reports: %+v", facts)
						}
						observed, result, denied := facts[historyAgentObserved][1], facts[historyAgentResult][0], facts[historyAgentDenied][0]
						if observed["tool"] != tool || observed["parent_tool_call_id"] != "parent" || result["status"] != string(status) || result["parent_tool_call_id"] != "parent" || denied["status"] != "denied" {
							t.Fatalf("sealed reports changed: %+v", facts)
						}
						for _, fact := range []map[string]string{result, denied} {
							if fact["tool_call_id"] != "call" || fact["lifetime_id"] != observed["lifetime_id"] || fact["lifetime_id"] == lifetime {
								t.Fatalf("sealed report joined successor: %+v", fact)
							}
						}
					}
				})
			}
		}
	}
	dir := t.TempDir()
	store := history.New(dir)
	e := newInteractiveTurnEmitterV2(nil, historyOnlyBroadcaster{}, discardLogger())
	e.hist, e.runtimeFacts = store, true
	ctx := context.Background()
	source := history.SessionProvenance{Kind: "claude", SessionID: "reused"}
	e.handleForSource(ctx, testConvID, turnevent.ToolStart{Title: "Agent", ToolCallID: "call", ParentToolCallID: "parent"}, source, 1)
	e.handleForSource(ctx, testConvID, turnevent.BackgroundTaskStarted{TaskID: "task", ToolCallID: "call"}, source, 1)
	e.closeRuntimeSource(ctx, testConvID, "reused", "idle_sleep", historyTS, true, 0, 1)
	e.handleForSource(ctx, testConvID, turnevent.ToolUpdate{ToolCallID: "call", ParentToolCallID: "parent", Status: turnevent.ToolStatusCompleted}, source, 1)
	e.handleForSource(ctx, testConvID, turnevent.BackgroundTaskUpdated{TaskID: "task", Status: "stopped"}, source, 1)
	e.handleForSource(ctx, testConvID, turnevent.BackgroundTaskStarted{TaskID: "task", ToolCallID: "call"}, source, 2)
	for _, reader := range []*history.Store{store, history.New(dir)} {
		facts := testAgentFacts(t, reader, testConvID)
		links, outcomes := facts["background_task_linked"], facts["background_task_outcome"]
		if len(links) != 2 || len(outcomes) != 1 || links[0]["lifetime_id"] != outcomes[0]["lifetime_id"] || links[0]["lifetime_id"] == links[1]["lifetime_id"] {
			t.Fatalf("sealed attribution: %+v", facts)
		}
		if len(facts["agent_call_result"]) != 1 || facts["agent_call_result"][0]["lifetime_id"] != links[0]["lifetime_id"] {
			t.Fatal("sealed launch result lost attribution")
		}
	}
	if e.inTurn {
		t.Fatal("late background report opened turn")
	}
}

func TestAgentHistory_FactValidation(t *testing.T) {
	t.Parallel()
	store := history.New(t.TempDir())
	e := newInteractiveTurnEmitterV2(nil, historyOnlyBroadcaster{}, discardLogger())
	e.hist, e.runtimeFacts = store, true
	ctx := context.Background()
	source := history.SessionProvenance{Kind: "claude", SessionID: "source"}
	for _, ev := range []turnevent.Event{turnevent.ToolStart{Title: "Agent", ToolCallID: "call"}, turnevent.ToolUpdate{ToolCallID: "call", Status: turnevent.ToolStatusFailed}, turnevent.ToolCallDenied{ToolCallID: "call"}, turnevent.BackgroundTaskStarted{TaskID: "task", ToolCallID: "call"}, turnevent.BackgroundTaskUpdated{TaskID: "task", Status: "unknown"}} {
		e.HandleFor(ctx, testConvID, ev, source)
	}
	e.HandleFor(ctx, testConvID, turnevent.ToolStart{Title: "Agent", ToolCallID: "gone-call"}, source)
	e.HandleFor(ctx, testConvID, turnevent.BackgroundTaskStarted{TaskID: "gone-task", ToolCallID: "gone-call"}, source)
	e.HandleFor(ctx, testConvID, turnevent.BackgroundTaskRoster{}, source)
	if len(testGoneEntries(t, store, testConvID)) != 1 {
		t.Fatal("gone fact missing from validation fixture")
	}
	fields := map[string][]string{"agent_call_observed": {"tool_call_id", "tool"}, "agent_call_result": {"tool_call_id", "status"}, "agent_call_denied": {"tool_call_id", "status"}, "background_task_observed": {"task_id"}, "background_task_linked": {"task_id", "tool_call_id"}, "background_task_outcome": {"task_id", "status"}, "background_task_gone": {"task_id", "tool_call_id", "status"}}
	for _, entry := range historyEntries(t, store, testConvID) {
		required, ok := fields[entry.Type]
		if !ok {
			continue
		}
		for _, field := range append([]string{"conversation_id", "lifetime_id", "occurred_at"}, required...) {
			var p map[string]any
			_ = json.Unmarshal(entry.Payload, &p)
			delete(p, field)
			raw, _ := json.Marshal(p)
			if validAgentHistoryFact(testConvID, entry.Type, raw) {
				t.Fatalf("missing %s in %s accepted", field, entry.Type)
			}
		}
	}
}

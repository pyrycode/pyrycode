package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func testSessionEndings(t *testing.T, store *history.Store) []history.Entry {
	t.Helper()
	var entries []history.Entry
	cursor := ""
	for {
		p, err := store.Page(conversations.ConversationID(testConvID), cursor, 128)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range p.Entries {
			if e.Type == "agent_ended_with_session" {
				entries = append(entries, e)
			}
		}
		if p.AtStart {
			break
		}
		cursor = p.Cursor
	}
	return entries
}

func TestAgentSessionEndings_Runtime(t *testing.T) {
	t.Parallel()
	call := turnevent.ToolStart{Title: "Agent", ToolCallID: "call", ParentToolCallID: "parent"}
	link := turnevent.BackgroundTaskStarted{TaskID: "task", ToolCallID: "call"}
	result := turnevent.ToolUpdate{ToolCallID: "call", Status: turnevent.ToolStatusCompleted}
	for _, tt := range []struct {
		name   string
		events []turnevent.Event
		want   int
	}{
		{"unfinished", []turnevent.Event{call}, 1},
		{"main-ended", []turnevent.Event{call, link, turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn}}, 1},
		{"late-link", []turnevent.Event{call, result, link}, 1},
		{"launch-result", []turnevent.Event{call, link, result}, 1},
		{"foreground", []turnevent.Event{call, result}, 0},
		{"failed", []turnevent.Event{call, turnevent.ToolUpdate{ToolCallID: "call", Status: turnevent.ToolStatusFailed}}, 0},
		{"denied", []turnevent.Event{call, link, turnevent.ToolCallDenied{ToolCallID: "call"}}, 0},
		{"terminal-before-link", []turnevent.Event{call, turnevent.BackgroundTaskUpdated{TaskID: "task", Status: "future-terminal"}, link}, 0},
		{"gone", []turnevent.Event{call, link, turnevent.BackgroundTaskRoster{}}, 0},
		{"patch-progress", []turnevent.Event{call, link, turnevent.BackgroundTaskUpdated{TaskID: "task", Summary: "private"}, turnevent.BackgroundTaskProgress{TaskID: "task"}}, 1},
		{"unlinked", []turnevent.Event{call, turnevent.BackgroundTaskStarted{TaskID: "task", ToolCallID: "call", TruncatedFields: []string{"tool_call_id"}}}, 2},
		{"task-only", []turnevent.Event{turnevent.BackgroundTaskProgress{TaskID: "task"}}, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			store := history.New(dir)
			e := newInteractiveTurnEmitterV2(nil, historyOnlyBroadcaster{}, discardLogger())
			e.hist, e.runtimeFacts = store, true
			source := history.SessionProvenance{Kind: "claude", SessionID: "source"}
			ctx := context.Background()
			for _, ev := range tt.events {
				e.handleForSource(ctx, testConvID, ev, source, 1)
			}
			lifetime := e.agentLifetime
			e.closeRuntimeSource(ctx, testConvID, "source", "idle_sleep", historyTS, true, 0, 1)
			e.closeRuntimeSource(ctx, testConvID, "source", "idle_sleep", historyTS, true, 0, 1)
			e.handleForSource(ctx, testConvID, call, source, 1)
			e.handleForSource(ctx, testConvID, link, source, 1)
			e.closeRuntimeSource(ctx, testConvID, "source", "idle_sleep", historyTS, true, 0, 1)
			for _, reader := range []*history.Store{store, history.New(dir)} {
				entries := testSessionEndings(t, reader)
				if len(entries) != tt.want {
					t.Fatalf("endings=%d want=%d", len(entries), tt.want)
				}
				for _, entry := range entries {
					var p map[string]any
					if err := json.Unmarshal(entry.Payload, &p); err != nil {
						t.Fatal(err)
					}
					if p["lifetime_id"] != lifetime || p["cause"] != "idle_sleep" || entry.Session == nil || *entry.Session != source || entry.Shown == nil || !*entry.Shown || !entry.TS.Equal(historyTS) {
						t.Fatalf("ending: %+v %s", entry, entry.Payload)
					}
					if !validAgentHistoryFact(testConvID, entry.Type, entry.Payload) {
						t.Fatal("invalid saved ending")
					}
				}
			}
		})
	}
}

func TestAgentSessionEndings_Isolation(t *testing.T) {
	t.Parallel()
	store := history.New(t.TempDir())
	e := newInteractiveTurnEmitterV2(nil, historyOnlyBroadcaster{}, discardLogger())
	e.hist, e.runtimeFacts = store, true
	ctx := context.Background()
	source := history.SessionProvenance{Kind: "claude", SessionID: "source"}
	call := turnevent.ToolStart{Title: "Agent", ToolCallID: "same"}
	e.handleForSource(ctx, testConvID, call, source, 1)
	e.closeRuntimeSource(ctx, testConvID, "source", "child_exit", historyTS, false, 1, 1)
	if len(testSessionEndings(t, store)) != 1 {
		t.Fatal("missing child exit")
	}
	e.handleForSource(ctx, testConvID, call, source, 1)
	e.runtimeEpoch = 1 // Mirrors the drain stamp on replacement output.
	e.closeRuntimeSource(ctx, testConvID, "source", "child_exit", historyTS, false, 1, 1)
	if len(testSessionEndings(t, store)) != 1 {
		t.Fatal("stale exit closed replacement")
	}
	e.handleForSource(ctx, testConvID, call, source, 2)
	e.handleForSource(ctx, testConvID, call, history.SessionProvenance{Kind: "claude", SessionID: "other"}, 1)
	e.handleForSource(ctx, testConvID, call, history.SessionProvenance{Kind: "codex", SessionID: "codex"}, 1)
	e.closeRuntimeSource(ctx, testConvID, "source", "operator_reset", historyTS, true, 0, 2)
	e.closeRuntimeSource(ctx, testConvID, "codex", "agent_switch", historyTS, true, 0, 1)
	if len(testSessionEndings(t, store)) != 2 {
		t.Fatal("source/incarnation isolation")
	}
	for _, entry := range historyEntries(t, store, testConvID) {
		if entry.Type == "session_divider" {
			t.Fatal("exit added divider")
		}
	}
}

func TestAgentSessionEndings_Startup(t *testing.T) {
	t.Parallel()
	for _, modern := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "modern"}[modern], func(t *testing.T) {
			started := time.Now()
			phase := func(name string) func() {
				t.Logf("startup phase=%s begin elapsed=%s", name, time.Since(started))
				at := time.Now()
				return func() { t.Logf("startup phase=%s duration=%s elapsed=%s", name, time.Since(at), time.Since(started)) }
			}
			constructed := phase("fixture")
			dir := t.TempDir()
			store := history.New(dir)
			reg := &conversations.Registry{}
			reg.Create(conversations.Conversation{ID: conversations.ConversationID(testConvID)})
			put := func(typ string, p any, source *history.SessionProvenance) {
				raw, err := json.Marshal(p)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = store.AppendWithMetadata(conversations.ConversationID(testConvID), typ, raw, historyTS, history.Metadata{Session: source}); err != nil {
					t.Fatal(err)
				}
			}
			var source *history.SessionProvenance
			var lifetime string
			if modern {
				source = &history.SessionProvenance{Kind: "claude", SessionID: "source"}
				e := newInteractiveTurnEmitterV2(nil, historyOnlyBroadcaster{}, discardLogger())
				e.hist, e.runtimeFacts = store, true
				for _, ev := range []turnevent.Event{turnevent.ToolStart{Title: "Agent", ToolCallID: "call", ParentToolCallID: "parent"}, turnevent.ToolUpdate{ToolCallID: "call", Status: turnevent.ToolStatusCompleted}, turnevent.BackgroundTaskStarted{TaskID: "task", ToolCallID: "call"}, turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn}} {
					e.HandleFor(context.Background(), testConvID, ev, *source)
				}
				lifetime = e.agentLifetime
				if lifetime == "" {
					t.Fatal("missing modern lifetime")
				}
			} else {
				put(protocol.TypeToolUse, map[string]string{"name": "Agent", "tool_use_id": "call", "parent_tool_use_id": "parent"}, nil)
				put(protocol.TypeToolResult, map[string]string{"tool_use_id": "call"}, nil)
				put(protocol.TypeBackgroundTaskStarted, map[string]string{"task_id": "task", "tool_call_id": "call"}, nil)
				put(protocol.TypeTurnEnd, map[string]string{"turn_id": "turn"}, nil)
			}
			put(protocol.TypeSessionTransition, map[string]string{}, nil)
			put(protocol.TypeToolUse, map[string]string{"name": "Task", "tool_use_id": "call"}, nil)
			put(protocol.TypeToolUse, map[string]any{"name": "Agent", "tool_use_id": "cut", "truncated_fields": []string{"tool_use_id"}}, nil)
			put(protocol.TypeBackgroundTaskStarted, map[string]any{"task_id": "bad", "tool_call_id": "call", "truncated_fields": []string{"task_id"}}, nil)
			// Force multiple raw pages and a real segment rollover without retaining prose in recovery.
			for i := 0; i < 140; i++ {
				put("padding", map[string]string{"padding": strings.Repeat("x", 8192)}, nil)
			}
			constructed()
			inspected := phase("raw-pages")
			files, err := filepath.Glob(filepath.Join(dir, "conversations", testConvID, "history", "segment-*.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			var bytes int64
			var sizes []int64
			for _, file := range files {
				info, err := os.Lstat(file)
				if err != nil {
					t.Fatal(err)
				}
				if !info.Mode().IsRegular() || info.Size() == 0 {
					t.Fatal("expected nonempty real segment")
				}
				bytes += info.Size()
				sizes = append(sizes, info.Size())
			}
			if len(sizes) < 2 || sizes[0] < history.MaxSegmentBytes {
				t.Fatalf("fixture did not roll over: segment sizes=%v", sizes)
			}
			reader := history.New(dir)
			pages, count, cursor := 0, 0, ""
			for {
				page, err := reader.Page(conversations.ConversationID(testConvID), cursor, 128)
				if err != nil {
					t.Fatal(err)
				}
				if len(page.Entries) > 0 {
					pages++
					count += len(page.Entries)
				}
				if page.AtStart {
					break
				}
				cursor = page.Cursor
			}
			if pages < 2 {
				t.Fatalf("fixture has only %d nonempty raw pages", pages)
			}
			t.Logf("startup fixture bytes=%d segment_sizes=%v raw_pages=%d entries=%d page_size=128", bytes, sizes, pages, count)
			inspected()
			now := time.Now().UTC()
			recovered := phase("recovery")
			reconcileStartupHistory(history.New(dir), reg, discardLogger(), now)
			recovered()
			read := phase("reopened-reads")
			entries := testSessionEndings(t, history.New(dir))
			if len(entries) != 2 {
				t.Fatalf("endings=%d want=2", len(entries))
			}
			seen := map[string]bool{}
			for _, entry := range entries {
				var p agentHistoryFact
				if err := json.Unmarshal(entry.Payload, &p); err != nil {
					t.Fatal(err)
				}
				if p.ConversationID != testConvID || p.Cause != "daemon_restart" || !p.OccurredAt.Equal(now) || !entry.TS.Equal(now) || entry.Shown == nil || !*entry.Shown || p.ToolCallID != "call" {
					t.Fatalf("bad ending %s", entry.Payload)
				}
				if seen[p.TaskID] {
					t.Fatalf("duplicate ending for task %q", p.TaskID)
				}
				seen[p.TaskID] = true
				switch p.TaskID {
				case "task":
					if p.ParentToolCallID != "parent" || !reflect.DeepEqual(entry.Session, source) || p.LifetimeID != lifetime || p.Tool != "Agent" {
						t.Fatalf("lost attribution: %s", entry.Payload)
					}
				case "":
					if p.ParentToolCallID != "" || entry.Session != nil || p.LifetimeID != "" || p.Tool != "Task" || p.CallObservedEntryID == 0 {
						t.Fatalf("bad reused call: %s", entry.Payload)
					}
				default:
					t.Fatalf("unexpected task ending: %s", entry.Payload)
				}
			}
			if !seen["task"] || !seen[""] || entries[0].ID == entries[1].ID {
				t.Fatal("missing distinct original task and reused call endings")
			}
			newest, pageErr := history.New(dir).Page(conversations.ConversationID(testConvID), "", 1)
			if pageErr != nil || len(newest.Entries) != 1 || newest.Entries[0].Type != historySessionDivider || max(entries[0].ID, entries[1].ID) >= newest.Entries[0].ID {
				t.Fatalf("restart source ending order: %+v %v", newest, pageErr)
			}
			read()
			repeated := phase("repeated-recovery-and-read")
			reconcileStartupHistory(history.New(dir), reg, discardLogger(), now.Add(time.Second))
			if !reflect.DeepEqual(testSessionEndings(t, history.New(dir)), entries) {
				t.Fatal("repeated recovery")
			}
			repeated()
		})
	}
}

func TestAgentSessionEndings_DrainOrdering(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := history.New(t.TempDir())
	sink := newStreamTurnSink(32, discardLogger())
	resolve := stubBusyResolve(map[string]string{"a": testConvID, "b": testConvID})
	busy := newTurnBusyTracker(resolve, discardLogger(), withExitEpoch(sink.exitEpoch))
	e := newInteractiveTurnEmitterV2(nil, historyOnlyBroadcaster{}, discardLogger())
	e.hist = store
	installRuntimeHistory(sink, e, busy)
	trans := newSessionTransitionEmitterV2(historyOnlyBroadcaster{}, resolve, discardLogger())
	trans.hist, trans.runtimeSink, trans.runtimeContext = store, sink, ctx
	a := sink.sinkForTag(func() string { return "a" }, "claude")
	b := sink.sinkForTag(func() string { return "b" }, "codex")
	a(turnevent.ToolStart{Title: "Agent", ToolCallID: "call"})
	a(turnevent.BackgroundTaskStarted{TaskID: "task", ToolCallID: "call"})
	a(turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn})
	trans.Enqueue(sessions.SessionTransition{ConversationID: testConvID, PreviousID: "a", NewID: "b", PreviousAgent: "claude", NextAgent: "codex", Cause: sessions.CauseAgentSwitch, OccurredAt: historyTS})
	b(turnevent.TextChunk{Text: "successor"})
	fence := make(chan struct{})
	sink.dispatchPlacement(ctx, func() { e.flushAll(ctx); close(fence) })
	cleanup := startStreamTurnDrainV2(ctx, sink, e, resolve, busy, discardLogger())
	defer func() { cancel(); cleanup() }()
	select {
	case <-fence:
	case <-time.After(5 * time.Second):
		t.Fatal("drain fence")
	}
	var ending, divider, successor uint64
	for _, entry := range historyEntries(t, store, testConvID) {
		switch entry.Type {
		case historyAgentSessionEnded:
			ending = entry.ID
		case historySessionDivider:
			divider = entry.ID
		case protocol.TypeAssistantDelta:
			successor = entry.ID
		}
	}
	if ending == 0 || !(ending < divider && divider < successor) {
		t.Fatalf("order %d %d %d", ending, divider, successor)
	}
	ring, _ := e.ring.After(testConvID, 0)
	for _, entry := range ring {
		if entry.Type == historyAgentSessionEnded {
			t.Fatal("raw ending replayed")
		}
	}
}

func TestAgentSessionEndings_FactValidation(t *testing.T) {
	at := historyTS
	for _, tt := range []struct {
		fact agentHistoryFact
		want bool
	}{
		{agentHistoryFact{ToolCallID: "call", CallObservedEntryID: 1}, true},
		{agentHistoryFact{TaskID: "task", TaskObservedEntryID: 2}, true},
		{agentHistoryFact{ToolCallID: "call"}, false},
		{agentHistoryFact{ToolCallID: "call", TaskObservedEntryID: 2}, false},
		{agentHistoryFact{CallObservedEntryID: 1}, false},
	} {
		tt.fact.ConversationID, tt.fact.Cause, tt.fact.OccurredAt = testConvID, "daemon_restart", at
		raw, _ := json.Marshal(tt.fact)
		if ok := validAgentHistoryFact(testConvID, historyAgentSessionEnded, raw); ok != tt.want {
			t.Fatalf("validation %s: %v", raw, ok)
		}
	}
}

func TestAgentSessionEndings_ReportedRecovery(t *testing.T) {
	t.Parallel()
	for _, terminal := range []bool{false, true} {
		t.Run(map[bool]string{false: "launch", true: "terminal"}[terminal], func(t *testing.T) {
			dir := t.TempDir()
			store := history.New(dir)
			reg := &conversations.Registry{}
			reg.Create(conversations.Conversation{ID: conversations.ConversationID(testConvID)})
			e := newInteractiveTurnEmitterV2(nil, historyOnlyBroadcaster{}, discardLogger())
			e.hist, e.runtimeFacts = store, true
			source := history.SessionProvenance{Kind: "claude", SessionID: "source"}
			events := []turnevent.Event{turnevent.ToolStart{Title: "Agent", ToolCallID: "call"}, turnevent.ToolUpdate{ToolCallID: "call", Status: turnevent.ToolStatusFailed}, turnevent.BackgroundTaskStarted{TaskID: "task", ToolCallID: "call"}}
			if terminal {
				events = append(events, turnevent.BackgroundTaskUpdated{TaskID: "task", Status: "completed"})
			}
			for _, ev := range events {
				e.HandleFor(context.Background(), testConvID, ev, source)
			}
			reconcileStartupHistory(history.New(dir), reg, discardLogger(), historyTS)
			for _, ev := range events {
				e.HandleFor(context.Background(), testConvID, ev, source)
			}
			reconcileStartupHistory(history.New(dir), reg, discardLogger(), historyTS)
			if got := len(testSessionEndings(t, history.New(dir))); got != 1-testBoolInt(terminal) {
				t.Fatalf("endings=%d", got)
			}
			for _, captured := range []history.SessionProvenance{source, {Kind: "claude", SessionID: "other"}} {
				e.handleForSource(context.Background(), testConvID, events[0], captured, 2)
				e.handleForSource(context.Background(), testConvID, events[2], captured, 2)
			}
			reconcileStartupHistory(history.New(dir), reg, discardLogger(), historyTS)
			if got := len(testSessionEndings(t, history.New(dir))); got != 3-testBoolInt(terminal) {
				t.Fatalf("reused source/lifetime endings=%d", got)
			}
		})
	}
}

func TestAgentSessionEndings_RosterReferenceIsolation(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name   string
		source *history.SessionProvenance
		linked bool
	}{
		{name: "legacy-unlinked"},
		{name: "legacy-linked", linked: true},
		{name: "attributed-unlinked", source: &history.SessionProvenance{Kind: "claude", SessionID: "source"}},
		{name: "attributed-linked", source: &history.SessionProvenance{Kind: "claude", SessionID: "source"}, linked: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			store := history.New(dir)
			convID := conversations.ConversationID(testConvID)
			reg := &conversations.Registry{}
			reg.Create(conversations.Conversation{ID: convID})
			put := func(typ string, payload any) uint64 {
				t.Helper()
				raw, err := json.Marshal(payload)
				if err != nil {
					t.Fatal(err)
				}
				shown := true
				id, err := store.AppendWithMetadata(convID, typ, raw, historyTS, history.Metadata{Session: tt.source, Shown: &shown})
				if err != nil {
					t.Fatal(err)
				}
				return id
			}
			observe := func() (uint64, uint64) {
				var rows []map[string]string
				var firstCall uint64
				for _, task := range []string{"first", "second"} {
					row := map[string]string{"task_id": task}
					if tt.linked {
						row["tool_call_id"] = "call-" + task
						id := put(protocol.TypeToolUse, map[string]string{"name": "Agent", "tool_use_id": row["tool_call_id"]})
						if task == "first" {
							firstCall = id
						}
					}
					rows = append(rows, row)
				}
				return put(protocol.TypeBackgroundTaskRoster, map[string]any{"tasks": rows}), firstCall
			}
			originalRoster, firstCall := observe()
			put(protocol.TypeSessionTransition, map[string]string{"cause": "operator_reset"})
			reusedRoster, _ := observe()
			// Only one ending survived recovery; its append scope already reuses both IDs.
			saved := agentHistoryFact{ConversationID: testConvID, TaskID: "first", TaskObservedEntryID: originalRoster, Cause: "daemon_restart", OccurredAt: historyTS}
			if tt.linked {
				saved.ToolCallID, saved.CallObservedEntryID = "call-first", firstCall
			}
			put(historyAgentSessionEnded, saved)
			for attempt := 0; attempt < 2; attempt++ {
				reconcileStartupHistory(history.New(dir), reg, discardLogger(), historyTS.Add(time.Duration(attempt+1)*time.Second))
				entries := testSessionEndings(t, history.New(dir))
				if len(entries) != 4 {
					t.Fatalf("reconciliation %d: endings=%d want=4", attempt+1, len(entries))
				}
				counts := map[uint64]map[string]int{originalRoster: {}, reusedRoster: {}}
				for _, entry := range entries {
					var p agentHistoryFact
					if err := json.Unmarshal(entry.Payload, &p); err != nil {
						t.Fatal(err)
					}
					if counts[p.TaskObservedEntryID] == nil || p.LifetimeID != "" || !reflect.DeepEqual(entry.Session, tt.source) {
						t.Fatalf("lost original attribution: %s", entry.Payload)
					}
					if tt.linked && (p.ToolCallID != "call-"+p.TaskID || p.CallObservedEntryID == 0) {
						t.Fatalf("lost linked call: %s", entry.Payload)
					}
					counts[p.TaskObservedEntryID][p.TaskID]++
				}
				for observation, tasks := range counts {
					if !reflect.DeepEqual(tasks, map[string]int{"first": 1, "second": 1}) {
						t.Fatalf("observation %d: endings=%v", observation, tasks)
					}
				}
			}
		})
	}
}

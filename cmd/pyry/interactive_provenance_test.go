package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func TestInteractiveProvenanceHelperProcess(t *testing.T) {
	if os.Getenv("PYRY_PROVENANCE_HELPER") != "1" {
		return
	}
	fmt.Println(assistantTextLine("reused", "factory text"))
	fmt.Println(resultLine)
	for {
		time.Sleep(time.Second)
	}
}

func TestInteractiveProvenanceFactories(t *testing.T) {
	for _, kind := range []string{"claude", "codex"} {
		t.Run(kind, func(t *testing.T) {
			sink := newStreamTurnSink(0, discardLogger())
			var runner sessions.Runner
			if kind == "claude" {
				t.Setenv("PYRY_PROVENANCE_HELPER", "1")
				bin := filepath.Join(t.TempDir(), "claude")
				script := fmt.Sprintf("#!/bin/sh\nexec %q -test.run=^TestInteractiveProvenanceHelperProcess$\n", os.Args[0])
				if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
				runner = runFactoryRunner(t, sink, bin, "old-routing")
			} else {
				factory := newCodexRunnerFactory(codexHarness{bin: fakeCodexBin(t), home: t.TempDir(), sink: sink})
				var err error
				runner, err = factory(sessions.RunnerConfig{SessionID: "old-routing", WorkDir: t.TempDir(), Logger: discardLogger()})
				if err != nil {
					t.Fatal(err)
				}
				h := &codexHarnessT{r: runner.(*codexRunner)}
				h.run(t)
				h.bound(t, nil)
				h.turn(t, "factory text")
			}
			// Retain the actual factory envelopes, then rotate before starting the drain.
			var captured []streamTurnEnvelope
			deadline := time.After(10 * time.Second)
		capture:
			for {
				select {
				case env := <-sink.ch:
					captured = append(captured, env)
					if _, ok := env.ev.(turnevent.TurnEnd); ok {
						break capture
					}
				case <-deadline:
					t.Fatal("factory emitted no turn end")
				}
			}
			runner.RestartFresh("successor-routing")
			for _, env := range captured {
				sink.ch <- env
			}
			resolver := &stubActiveSession{}
			resolver.bind("old-routing", testConvID)
			resolver.bind("successor-routing", testConvIDB)
			cur := &stubCursor{}
			cur.set(testConvIDB)
			bcast := newChanBcast("interactive")
			dir := t.TempDir()
			e := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())
			e.hist = history.New(dir)
			ctx, cancel := context.WithCancel(context.Background())
			join := startStreamTurnDrainV2(ctx, sink, e, resolver.get, nil, discardLogger())
			defer func() { cancel(); join() }()
			for {
				env := collectEnvs(t, bcast.pushed, 1)[0]
				if env.Type == protocol.TypeTurnEnd {
					break
				}
			}
			// Join before inspecting emitter-owned fields; idle is emitted after TurnEnd.
			cancel()
			join()
			expected := history.SessionProvenance{Kind: kind, SessionID: "old-routing"}
			for _, store := range []*history.Store{e.hist, history.New(dir)} {
				entries := historyEntries(t, store, testConvID)
				if len(entries) == 0 {
					t.Fatal("empty producing conversation history")
				}
				for _, entry := range entries {
					if entry.Session == nil || *entry.Session != expected {
						t.Fatalf("%s provenance = %v, want %v", entry.Type, entry.Session, expected)
					}
				}
			}
		})
	}
}

func assertProvenanceText(t *testing.T, entries []history.Entry, want map[string]history.SessionProvenance) {
	t.Helper()
	seen := make(map[string]history.SessionProvenance)
	for _, entry := range entries {
		if entry.Type != protocol.TypeAssistantDelta {
			continue
		}
		var delta protocol.AssistantDeltaPayload
		if err := json.Unmarshal(entry.Payload, &delta); err != nil {
			t.Fatal(err)
		}
		if entry.Session == nil {
			t.Fatalf("text %q has absent provenance", delta.Text)
		}
		if _, exists := seen[delta.Text]; exists {
			t.Fatalf("duplicate text %q", delta.Text)
		}
		seen[delta.Text] = *entry.Session
	}
	if !reflect.DeepEqual(seen, want) {
		t.Fatalf("text provenance = %v, want %v", seen, want)
	}
}

func TestInteractiveProvenanceRetainedState(t *testing.T) {
	for _, finish := range []string{"timer", "lifecycle", "turn_end"} {
		for _, successor := range []history.SessionProvenance{{Kind: "claude", SessionID: "new"}, {Kind: "codex", SessionID: "old"}} {
			t.Run(finish+"/"+successor.Kind, func(t *testing.T) {
				t.Parallel()
				ctx := context.Background()
				old := history.SessionProvenance{Kind: "claude", SessionID: "old"}
				other := history.SessionProvenance{Kind: "codex", SessionID: "other"}
				dir := t.TempDir()
				cur := &stubCursor{}
				cur.set(testConvIDB)
				e := newInteractiveTurnEmitterV2(cur, &fakeInteractiveBcast{}, discardLogger())
				e.hist = history.New(dir)
				t.Cleanup(func() { e.flushTimer.Stop() })
				text := func(conv, value, parent string, source history.SessionProvenance) {
					e.HandleFor(ctx, conv, turnevent.TextChunk{MessageID: "reused", ParentToolCallID: parent, Text: value}, source)
				}
				text(testConvID, "old-main", "", old)
				text(testConvID, "new-main", "", successor)
				text(testConvIDB, "other", "", other)
				// Both sources reuse the same child/message keys. Return to the old source
				// after the successor has buffered text to expose emitter-global attribution.
				text(testConvID, "old-child", "parent", old)
				text(testConvID, "new-child", "parent", successor)
				text(testConvID, "+tail", "parent", old)
				switch finish {
				case "timer":
					select {
					case <-e.flushC():
						e.flushAll(ctx)
					case <-time.After(2 * time.Second):
						t.Fatal("flush timer never fired")
					}
				case "lifecycle":
					e.closeForConversation(ctx, testConvID)
					if !e.anyBuffered() {
						t.Fatal("closing A also flushed B")
					}
				case "turn_end":
					e.HandleFor(ctx, testConvID, turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn}, old)
					e.HandleFor(ctx, testConvID, turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn}, successor)
				}
				e.closeForConversation(ctx, testConvID)
				e.closeForConversation(ctx, testConvIDB)
				if e.anyBuffered() || len(e.turns) != 0 {
					t.Fatal("lifecycle close retained source states")
				}
				want := map[string]history.SessionProvenance{"old-main": old, "new-main": successor, "old-child": old, "+tail": old, "new-child": successor}
				for _, store := range []*history.Store{e.hist, history.New(dir)} {
					entries := historyEntries(t, store, testConvID)
					assertProvenanceText(t, entries, want)
					var texts []string
					for _, entry := range entries {
						if entry.Type == protocol.TypeAssistantDelta {
							var delta protocol.AssistantDeltaPayload
							if err := json.Unmarshal(entry.Payload, &delta); err != nil {
								t.Fatal(err)
							}
							texts = append(texts, delta.Text)
						}
					}
					if !slices.Equal(texts, []string{"old-main", "new-main", "old-child", "new-child", "+tail"}) {
						t.Fatalf("text arrival order = %v", texts)
					}
					assertProvenanceText(t, historyEntries(t, store, testConvIDB), map[string]history.SessionProvenance{"other": other})
					idle := map[history.SessionProvenance]bool{}
					for _, entry := range entries {
						if entry.Session == nil || (*entry.Session != old && *entry.Session != successor) {
							t.Fatalf("%s source = %v", entry.Type, entry.Session)
						}
						if entry.Shown == nil || *entry.Shown != (entry.Type == protocol.TypeAssistantDelta) {
							t.Fatalf("%s visibility = %v", entry.Type, entry.Shown)
						}
						if entry.Type == protocol.TypeTurnState {
							var state protocol.TurnStatePayload
							if err := json.Unmarshal(entry.Payload, &state); err != nil {
								t.Fatal(err)
							}
							if state.State == "idle" {
								idle[*entry.Session] = true
							}
						}
					}
					if !idle[old] || !idle[successor] {
						t.Fatalf("idle sources = %v", idle)
					}
					latest, err := store.LatestDisplayableEntryID(testConvID)
					if err != nil {
						t.Fatal(err)
					}
					var lastText uint64
					for _, entry := range entries {
						if entry.Type == protocol.TypeAssistantDelta {
							lastText = entry.ID
						}
					}
					if latest != lastText {
						t.Fatalf("displayable watermark = %d, want %d", latest, lastText)
					}
				}
			})
		}
	}
}

func TestInteractiveProvenanceCompatibility(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	store := history.New(dir)
	legacyID, err := store.Append(testConvID, protocol.TypeAssistantDelta, json.RawMessage(`{"text":"legacy"}`), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	source := history.SessionProvenance{Kind: "codex", SessionID: "daemon-routing"}
	payload := json.RawMessage(`{"text":"captured"}`)
	id := appendConversationHistory(store, discardLogger(), "test", testConvID, protocol.TypeAssistantDelta, payload, time.Now(), source)
	if id == nil || *id != legacyID+1 {
		t.Fatalf("append identity = %v", id)
	}
	appendConversationHistory(store, discardLogger(), "test", testConvID, protocol.TypeAssistantDelta, payload, time.Now())
	entries := historyEntries(t, history.New(dir), testConvID)
	if entries[0].Session != nil || entries[2].Session != nil || entries[1].Session == nil || *entries[1].Session != source {
		t.Fatalf("legacy/captured/absent provenance = %v", entries)
	}
	pager := newHistoryPager(store, discardLogger())(testConvID, "", 128)
	for i, entry := range pager.Entries {
		if string(entry.Payload) != string(entries[len(entries)-1-i].Payload) {
			t.Fatal("legacy pager changed payload")
		}
	}
	for _, tc := range []struct {
		name, conv string
		store      *history.Store
	}{{"nil", testConvID, nil}, {"failed", "invalid", store}, {"stored", testConvID, store}} {
		t.Run(tc.name, func(t *testing.T) {
			b := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "yes", Interactive: true}, {ConnID: "no", Interactive: false}}}}
			e := newInteractiveTurnEmitterV2(&stubCursor{}, b, discardLogger())
			e.hist = tc.store
			e.HandleFor(ctx, tc.conv, turnevent.TextChunk{Text: "visible"}, source)
			e.HandleFor(ctx, tc.conv, turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn}, source)
			events, _ := e.ring.After(tc.conv, 0)
			if len(events) == 0 || len(b.pushes) != len(events) {
				t.Fatal("storage affected live/replay delivery")
			}
			for i, event := range events {
				env := b.pushes[i].env
				if b.pushes[i].connID != "yes" || string(env.Payload) != string(event.Payload) || !env.TS.Equal(event.TS) {
					t.Fatal("legacy recipient/payload changed")
				}
				if tc.name == "stored" && (env.HistoryEntryID == nil || event.HistoryEntryID == 0 || *env.HistoryEntryID != event.HistoryEntryID) {
					t.Fatal("durable identity lost")
				}
				if tc.name != "stored" && (env.HistoryEntryID != nil || event.HistoryEntryID != 0) {
					t.Fatal("failed append invented identity")
				}
			}
		})
	}
}

func TestInteractiveProvenanceDelayedDrain(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"claude", "codex"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			watcher := dropWatcher{kinds: make(chan string, 1)}
			logger := slog.New(watcher)
			sink := newStreamTurnSink(16, logger)
			tag := newStreamSessionTag("old")
			events := sink.sinkForTag(tag.ID, kind)
			events(turnevent.TextChunk{MessageID: "same", Text: "old"})
			tag.Rotate("new")
			events(turnevent.TextChunk{MessageID: "same", Text: "new"})
			sink.sinkForTag(func() string { return "old" }, kind)(turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn})
			events(turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn})
			sink.sinkForTag(func() string { return "unresolved" }, kind)(turnevent.TextChunk{Text: "must drop"})
			resolver := &stubActiveSession{}
			resolver.set("old")
			resolver.set("new")
			cur := &stubCursor{}
			cur.set(testConvIDB)
			b := newChanBcast("interactive")
			e := newInteractiveTurnEmitterV2(cur, b, logger)
			e.hist = history.New(t.TempDir())
			ctx, cancel := context.WithCancel(context.Background())
			join := startStreamTurnDrainV2(ctx, sink, e, resolver.get, nil, logger)
			defer func() { cancel(); join() }()
			select {
			case <-watcher.kinds:
			case <-time.After(2 * time.Second):
				t.Fatal("unresolved event was not dropped")
			}
			cancel()
			join()
			entries := historyEntries(t, e.hist, testConvID)
			assertProvenanceText(t, entries, map[string]history.SessionProvenance{"old": {Kind: kind, SessionID: "old"}, "new": {Kind: kind, SessionID: "new"}})
			replayed, _ := e.ring.After(testConvID, 0)
			assertLogMatchesRing(t, entries, replayed)
			if len(historyEntries(t, e.hist, testConvIDB)) != 0 {
				t.Fatal("active cursor received producer history")
			}
		})
	}
}

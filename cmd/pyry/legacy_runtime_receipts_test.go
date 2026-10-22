package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// testLegacyReceipts serializes the retained live receipt projection of source
// facts for identity assertions. It has no paging or wire-page API.
func testLegacyReceipts(t *testing.T, store *history.Store) ([]history.Entry, []byte) {
	t.Helper()
	var entries []history.Entry
	var wire []byte
	for _, e := range historyEntries(t, store, testConvID) {
		typ, payload := e.Type, e.Payload
		if !legacyHistoryType(typ) {
			var ok bool
			payload, ok = legacyRuntimeReceipt(testConvID, typ, payload, &e.ID, e.TS)
			if !ok {
				continue
			}
			typ = protocol.TypeBanner
		}
		encoded, err := json.Marshal(protocol.Envelope{Type: typ, Payload: payload, TS: e.TS, HistoryEntryID: &e.ID})
		if err != nil {
			t.Fatal(err)
		}
		var decoded protocol.Envelope
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, history.Entry{ID: *decoded.HistoryEntryID, Type: decoded.Type, Payload: decoded.Payload, TS: decoded.TS})
		wire = append(wire, encoded...)
	}
	slices.Reverse(entries)
	return entries, wire
}

func TestLegacyRuntimeReceipts_CompletedTurn(t *testing.T) {
	t.Parallel()
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			dir := t.TempDir()
			store := history.New(dir)
			bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "phone", Interactive: true}, {ConnID: "peer", Interactive: true}, {ConnID: "legacy"}}}}
			e := newInteractiveTurnEmitterV2(nil, bcast, discardLogger())
			e.hist, e.runtimeFacts = store, enabled
			ctx := context.Background()
			e.emit(ctx, testConvID, protocol.TypeMessage, protocol.MessagePayload{ConversationID: testConvID, MessageID: "user", Role: "user", Text: "synthetic"})
			source := history.SessionProvenance{Kind: "claude", SessionID: "synthetic-session"}
			e.HandleFor(ctx, testConvID, turnevent.TextChunk{Text: "synthetic reply"}, source)
			e.HandleFor(ctx, testConvID, turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn}, source)
			rawEntries := historyEntries(t, store, testConvID)
			entries, _ := testLegacyReceipts(t, store)
			entries = slices.DeleteFunc(entries, func(entry history.Entry) bool { return testRetiredInteractiveType(entry.Type) })
			versions := map[string][]history.Entry{"history": entries, "phone": nil, "peer": nil, "replay": nil}
			addEnvelope := func(name string, env protocol.Envelope) {
				encoded, err := json.Marshal(env)
				if err != nil {
					t.Fatal(err)
				}
				var decoded protocol.Envelope
				if err := json.Unmarshal(encoded, &decoded); err != nil || decoded.HistoryEntryID == nil {
					t.Fatalf("missing serialized identity: %s / %v", encoded, err)
				}
				versions[name] = append(versions[name], history.Entry{ID: *decoded.HistoryEntryID, Type: decoded.Type, Payload: decoded.Payload, TS: decoded.TS})
			}
			for _, push := range bcast.pushes {
				if push.connID == "legacy" {
					t.Fatal("noninteractive recipient received history")
				}
				addEnvelope(push.connID, push.env)
			}
			events, _ := e.ring.After(testConvID, 0)
			for _, event := range events {
				id := event.HistoryEntryID
				addEnvelope("replay", protocol.Envelope{Type: event.Type, Payload: event.Payload, TS: event.TS, HistoryEntryID: &id})
			}
			joined := slices.Clone(entries)
			for _, name := range []string{"phone", "peer", "replay"} {
				joined = append(joined, versions[name]...)
			}
			versions["overlap"] = append(joined, entries...)
			for name, received := range versions {
				if name != "overlap" && len(received) != len(entries) {
					t.Fatalf("%s delivered %d/%d entries", name, len(received), len(entries))
				}
				for _, expected := range entries {
					matched := false
					for _, actual := range received {
						if actual.ID == expected.ID {
							if actual.Type != expected.Type || !actual.TS.Equal(expected.TS) || !bytes.Equal(actual.Payload, expected.Payload) {
								t.Fatalf("%s changed durable identity/payload: %+v / %+v", name, actual, expected)
							}
							matched = true
						}
					}
					if !matched {
						t.Fatalf("%s missing durable ID %d", name, expected.ID)
					}
				}
			}
			for _, reader := range []*history.Store{store, history.New(dir)} {
				if got := historyEntries(t, reader, testConvID); !reflect.DeepEqual(got, rawEntries) {
					t.Fatal("raw entries or metadata changed")
				}
			}
		})
	}
}

func testRuntimeFact(t *testing.T, typ, convID string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(runtimeHistoryFact{ConversationID: convID, TurnID: "turn", ToolCallID: "tool", Cause: "operator_reset", PreviousSessionID: "old", NewSessionID: "new", OccurredAt: historyTS})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestLegacyRuntimeReceipts_LiveReplay(t *testing.T) {
	t.Parallel()
	store := history.New(t.TempDir())
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "phone", Interactive: true}, {ConnID: "peer", Interactive: true}, {ConnID: "legacy"}}}}
	e := newInteractiveTurnEmitterV2(nil, bcast, discardLogger())
	e.hist = store
	sink := newStreamTurnSink(8, discardLogger())
	installRuntimeHistory(sink, e, nil)
	ctx := context.Background()
	source := history.SessionProvenance{Kind: "claude", SessionID: "old"}
	e.HandleFor(ctx, testConvID, turnevent.TextChunk{Text: "reply"}, source)
	e.HandleFor(ctx, testConvID, turnevent.ToolStart{ToolCallID: "tool", Title: "Read"}, source)
	e.closeRuntimeSource(ctx, testConvID, "old", "operator_reset", historyTS, true, 0)
	tr := newSessionTransitionEmitterV2(bcast, nil, discardLogger())
	tr.hist = store
	tr.runtimeSink = sink
	tr.broadcast(ctx, sessions.SessionTransition{ConversationID: testConvID, PreviousID: "old", NewID: "new", Cause: sessions.CauseOperatorReset, OccurredAt: historyTS})
	page, _ := testLegacyReceipts(t, store)
	raw := historyEntries(t, store, testConvID)
	raw = slices.DeleteFunc(raw, func(entry history.Entry) bool { return testRetiredInteractiveType(entry.Type) })
	events, _ := e.ring.After(testConvID, 0)
	if len(events) != len(raw) || len(bcast.pushes) != 2*len(raw) {
		t.Fatalf("append/live/replay counts=%d/%d/%d", len(raw), len(bcast.pushes), len(events))
	}
	for i, original := range raw {
		var projected history.Entry
		for _, entry := range page {
			if entry.ID == original.ID {
				projected = entry
			}
		}
		if projected.ID == 0 {
			t.Fatalf("missing projected ID %d", original.ID)
		}
		event := events[i]
		if event.HistoryEntryID != original.ID || event.Type != projected.Type || !event.TS.Equal(projected.TS) || !bytes.Equal(event.Payload, projected.Payload) {
			t.Fatalf("replay differs: %+v / %+v", event, projected)
		}
		for _, push := range bcast.pushes {
			if push.env.HistoryEntryID != nil && *push.env.HistoryEntryID == original.ID {
				if push.connID == "legacy" || push.env.Type != projected.Type || !push.env.TS.Equal(projected.TS) || !bytes.Equal(push.env.Payload, projected.Payload) || push.env.EventID == nil {
					t.Fatal("live receipt differs from history/replay")
				}
			}
		}
		if legacyRuntimeFact(original.Type) && (original.Shown == nil || original.Session == nil && original.Type != historySessionDivider || bytes.Equal(original.Payload, projected.Payload)) {
			t.Fatal("raw fact/metadata lost")
		}
	}
	if other, _ := e.ring.After(testConvIDB, 0); len(other) != 0 {
		t.Fatal("receipt crossed conversation")
	}
}

func TestLegacyRuntimeReceipts_StorageFailure(t *testing.T) {
	t.Parallel()
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprint(failed), func(t *testing.T) {
			var store *history.Store
			if failed {
				root := filepath.Join(t.TempDir(), "occupied")
				if err := os.WriteFile(root, []byte("x"), 0600); err != nil {
					t.Fatal(err)
				}
				store = history.New(root)
			}
			bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "phone", Interactive: true}}}}
			e := newInteractiveTurnEmitterV2(nil, bcast, discardLogger())
			e.hist = store
			e.runtimeFacts = true
			e.HandleFor(context.Background(), testConvID, turnevent.TextChunk{Text: "reply"}, history.SessionProvenance{Kind: "claude", SessionID: "old"})
			e.HandleFor(context.Background(), testConvID, turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn}, history.SessionProvenance{Kind: "claude", SessionID: "old"})
			events, _ := e.ring.After(testConvID, 0)
			if len(events) != 2 || len(bcast.pushes) != 2 {
				t.Fatalf("eligible delivery suppressed %d/%d", len(events), len(bcast.pushes))
			}
			for _, push := range bcast.pushes {
				if push.env.Type == protocol.TypeBanner || push.env.HistoryEntryID != nil {
					t.Fatal("failed storage minted receipt")
				}
			}
		})
	}
}

func testLegacyReceiptEnvelope(t *testing.T, env protocol.Envelope, convID string) {
	t.Helper()
	var banner protocol.BannerPayload
	if env.Type != protocol.TypeBanner || env.HistoryEntryID == nil || *env.HistoryEntryID == 0 || env.TS.IsZero() || json.Unmarshal(env.Payload, &banner) != nil || banner != (protocol.BannerPayload{ConversationID: convID, Level: "info"}) {
		t.Fatalf("invalid legacy receipt: %+v", env)
	}
}

func TestLegacyRuntimeReceipts_StartupDivider(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store := history.New(root)
	reg, err := conversations.Load(filepath.Join(root, "conversations.json"))
	if err != nil {
		t.Fatal(err)
	}
	reg.Create(conversations.Conversation{ID: conversations.ConversationID(testConvID)})
	if _, err := store.Append(conversations.ConversationID(testConvID), protocol.TypeMessage, json.RawMessage(`{}`), historyTS); err != nil {
		t.Fatal(err)
	}
	reconcileStartupHistory(store, reg, discardLogger(), historyTS)
	page, _ := testLegacyReceipts(t, history.New(root))
	if len(page) != 2 || page[0].ID != 2 || page[0].Type != protocol.TypeBanner {
		t.Fatalf("startup divider receipt=%+v", page)
	}
}
func TestLegacyRuntimeReceipts_WireTargets(t *testing.T) {
	t.Parallel()
	// Explicit wire vocabulary keeps this oracle independent of legacyHistoryType.
	types := []string{
		"message", "assistant_delta", "tool_use", "tool_result", "tool_denied",
		"turn_end", "banner", "background_task_started", "background_task_updated",
		"compaction_boundary", "model_refusal_fallback", "model_refusal_no_fallback",
		"unrecognized_message", "session_transition", "turn_state", "stall", "api_retry",
		"compacting", "tool_progress", "thinking_progress", "background_task_roster",
		"background_task_progress", "rate_limited", "context_usage", "model_announced",
		"session_facts", "mcp_status", "model_list", "slash_command_list",
		"main_turn_opened", "main_tool_interrupted", "main_turn_interrupted", "session_divider",
		"future_content", "invalid_runtime_fact",
	}
	for _, typ := range types {
		t.Run(typ, func(t *testing.T) {
			for _, visibility := range []string{"absent", "false", "true"} {
				t.Run(visibility, func(t *testing.T) {
					dir := t.TempDir()
					store := history.New(dir)
					var shown *bool
					if visibility != "absent" {
						value := visibility == "true"
						shown = &value
					}
					entryType, payload := typ, json.RawMessage(`{}`)
					if legacyRuntimeFact(typ) {
						payload = testRuntimeFact(t, typ, testConvID)
					}
					if typ == "invalid_runtime_fact" {
						entryType = historyTurnOpened
					}
					metadata := history.Metadata{Shown: shown, Session: &history.SessionProvenance{Kind: "claude", SessionID: "stored-session"}}
					id := conversations.ConversationID(testConvID)
					if _, err := store.AppendWithMetadata(id, entryType, payload, historyTS, metadata); err != nil {
						t.Fatal(err)
					}
					unsupported := typ == "future_content" || typ == "invalid_runtime_fact"
					for _, reader := range []*history.Store{store, history.New(dir)} {
						entries, _ := testLegacyReceipts(t, reader)
						if unsupported {
							if len(entries) != 0 {
								t.Fatal("unsupported entry certified as receipt")
							}
						} else if len(entries) != 1 || entries[0].ID != 1 {
							t.Fatalf("eligible entry lost delivery/accounting: %+v", entries)
						}
						raw, err := reader.Page(id, "", 1)
						if err != nil || len(raw.Entries) != 1 || raw.Entries[0].Type != entryType || !bytes.Equal(raw.Entries[0].Payload, payload) || !raw.Entries[0].TS.Equal(historyTS) || !reflect.DeepEqual(raw.Entries[0].Shown, shown) || !reflect.DeepEqual(raw.Entries[0].Session, metadata.Session) {
							t.Fatalf("raw entry changed: %+v / %v", raw, err)
						}
						if got, err := reader.LatestEntryID(id); err != nil || got != 1 {
							t.Fatalf("raw cursor=%d,%v", got, err)
						}
					}
				})
			}
		})
	}
}

func TestLegacyRuntimeReceipts_CompletionTails(t *testing.T) {
	t.Parallel()
	for _, ending := range []struct {
		name  string
		event turnevent.TurnEnd
		shown bool
	}{
		{"normal", turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn}, false},
		{"failed", turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn, IsError: true, Outcome: "error_during_execution"}, true},
		{"stopped", turnevent.TurnEnd{Reason: turnevent.TurnEndReasonCancelled}, true},
	} {
		t.Run(ending.name, func(t *testing.T) {
			dir := t.TempDir()
			store := history.New(dir)
			e := newInteractiveTurnEmitterV2(nil, historyOnlyBroadcaster{}, discardLogger())
			e.hist, e.runtimeFacts = store, true
			ctx := context.Background()
			source := history.SessionProvenance{Kind: "claude", SessionID: "session"}
			e.emit(ctx, testConvID, protocol.TypeMessage, protocol.MessagePayload{ConversationID: testConvID, Role: "user", Text: "request"})
			e.HandleFor(ctx, testConvID, turnevent.TextChunk{Text: "reply"}, source)
			e.HandleFor(ctx, testConvID, ending.event, source)
			raw := historyEntries(t, store, testConvID)
			if len(raw) != 6 || raw[4].Type != protocol.TypeTurnEnd || raw[4].Shown == nil || *raw[4].Shown != ending.shown {
				t.Fatalf("completion raw visibility=%+v", raw)
			}
		})
	}
}

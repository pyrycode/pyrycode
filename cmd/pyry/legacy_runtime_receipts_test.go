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
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/relay/handlers"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// testLegacyCheckpoint mirrors mobile ThreadReadEvidence.checkpoint's receipt
// barriers. A presented content version is supplied explicitly; receiving pages
// alone cannot supply one. The known decoder arms are limited to this fixture.
func testLegacyCheckpoint(entries []protocol.HistoryEntry, presented, confirmed uint64, unidentified bool) uint64 {
	if unidentified || presented == 0 {
		return 0
	}
	facts := map[uint64]*bool{}
	for _, entry := range entries {
		if entry.ID == 0 {
			return 0
		}
		var p map[string]json.RawMessage
		if json.Unmarshal(entry.Payload, &p) != nil {
			facts[entry.ID] = nil
			continue
		}
		var nonvisual bool
		valid := true
		switch entry.Type {
		case protocol.TypeBanner:
			// BannerPayloadDto has five required fields, without defaults.
			for _, field := range []string{"conversation_id", "level", "text", "truncated", "stops_turn"} {
				if p[field] == nil || string(p[field]) == "null" {
					valid = false
				}
			}
			var banner protocol.BannerPayload
			valid = valid && json.Unmarshal(entry.Payload, &banner) == nil && banner.Level == "info"
			nonvisual = true
		case protocol.TypeTurnState, protocol.TypeTurnEnd:
			nonvisual = true
		case protocol.TypeMessage, protocol.TypeAssistantDelta:
		default:
			valid = false
		}
		if valid {
			facts[entry.ID] = &nonvisual
		} else {
			facts[entry.ID] = nil
		}
	}
	var ids []uint64
	for id := range facts {
		if id > confirmed {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	for i, id := range ids {
		if id <= presented && facts[id] == nil {
			return 0
		}
		if i > 0 && id <= presented && id-ids[i-1] > 1 {
			return 0
		}
	}
	candidate := presented
	for facts[candidate+1] != nil && *facts[candidate+1] {
		candidate++
	}
	if candidate <= confirmed {
		return 0
	}
	return candidate
}

func TestLegacyRuntimeReceipts_CompletedTurn(t *testing.T) {
	t.Parallel()
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			store := history.New(t.TempDir())
			raw := json.RawMessage(`{"conversation_id":"` + testConvID + `","message_id":"user","role":"user","text":"synthetic"}`)
			if appendConversationHistory(store, discardLogger(), "test", testConvID, protocol.TypeMessage, raw, historyTS) == nil {
				t.Fatal("append failed")
			}
			e := newInteractiveTurnEmitterV2(nil, historyOnlyBroadcaster{}, discardLogger())
			e.hist, e.runtimeFacts = store, enabled
			source := history.SessionProvenance{Kind: "claude", SessionID: "synthetic-session"}
			e.HandleFor(context.Background(), testConvID, turnevent.TextChunk{Text: "synthetic reply"}, source)
			e.HandleFor(context.Background(), testConvID, turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn}, source)
			page := newHistoryPager(store, discardLogger())(testConvID, "", 128)
			rawEntries := historyEntries(t, store, testConvID)
			if !page.AtStart || page.Cursor != "" {
				t.Fatal("expected terminal page")
			}
			latest, err := store.LatestDisplayableEntryID(conversations.ConversationID(testConvID))
			if err != nil {
				t.Fatal(err)
			}
			// The completed assistant version includes turn_end, then idle extends it.
			presented := uint64(len(rawEntries) - 1)
			if got := testLegacyCheckpoint(page.Entries, presented, 0, false); got != uint64(len(rawEntries)) || got < latest {
				t.Fatalf("checkpoint=%d, want %d reaching watermark %d; entries=%+v", got, len(rawEntries), latest, page.Entries)
			}
			if got := testLegacyCheckpoint(page.Entries, 0, 0, false); got != 0 {
				t.Fatal("fetched history granted sight")
			}
			serialized, err := json.Marshal(protocol.HistoryPagePayload{Entries: page.Entries, Cursor: page.Cursor, AtStart: page.AtStart})
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("mobile handoff enabled=%v watermark=%d checkpoint=%d page=%s", enabled, latest, len(rawEntries), serialized)
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

func TestLegacyRuntimeReceipts_BoundedPages(t *testing.T) {
	t.Parallel()
	for _, mixed := range []bool{false, true} {
		t.Run(fmt.Sprint(mixed), func(t *testing.T) {
			dir := t.TempDir()
			store := history.New(dir)
			id := conversations.ConversationID(testConvID)
			types := []string{historyTurnOpened, historyToolInterrupted, historyTurnInterrupted, historySessionDivider}
			for i := 0; i < 12; i++ {
				typ := types[i%4]
				raw := testRuntimeFact(t, typ, testConvID)
				if mixed && i%3 == 1 {
					typ = protocol.TypeMessage
					raw = json.RawMessage(`{"text":"content"}`)
				}
				shown := i%2 == 0
				visibility := &shown
				if i%3 == 0 {
					visibility = nil
				}
				if _, err := store.AppendWithMetadata(id, typ, raw, historyTS.Add(time.Duration(i)*time.Second), history.Metadata{Shown: visibility}); err != nil {
					t.Fatal(err)
				}
			}
			for _, reader := range []*history.Store{store, history.New(dir)} {
				if !mixed {
					if got, err := (legacyHistoryReader{reader}).LatestDisplayableEntryID(id); err != nil || got != 0 {
						t.Fatalf("all-runtime watermark=%d,%v", got, err)
					}
				}
				cursor := ""
				var delivered []protocol.HistoryEntry
				for step := 0; step < 8; step++ {
					raw, err := reader.Page(id, cursor, 3)
					if err != nil {
						t.Fatal(err)
					}
					page := newHistoryPager(reader, discardLogger())(testConvID, cursor, 3)
					if page.Cursor != raw.Cursor || page.AtStart != raw.AtStart || len(page.Entries) != len(raw.Entries) {
						t.Fatalf("bounded page mismatch: %+v / %+v", page, raw)
					}
					for i, entry := range page.Entries {
						original := raw.Entries[i]
						if entry.ID != original.ID || !entry.TS.Equal(original.TS) {
							t.Fatal("receipt identity changed")
						}
						if original.Type == protocol.TypeMessage {
							if !bytes.Equal(entry.Payload, original.Payload) {
								t.Fatal("content rewritten")
							}
							continue
						}
						var banner protocol.BannerPayload
						if json.Unmarshal(entry.Payload, &banner) != nil || entry.Type != protocol.TypeBanner || banner != (protocol.BannerPayload{ConversationID: testConvID, Level: "info"}) {
							t.Fatalf("receipt=%+v", entry)
						}
						var keys map[string]json.RawMessage
						_ = json.Unmarshal(entry.Payload, &keys)
						if len(keys) != 5 || keys["text"] == nil || keys["truncated"] == nil || keys["stops_turn"] == nil {
							t.Fatalf("required fields: %s", entry.Payload)
						}
					}
					// Re-requesting the same raw cursor produces exactly the same receipts.
					again := newHistoryPager(reader, discardLogger())(testConvID, cursor, 3)
					if !reflect.DeepEqual(page, again) {
						t.Fatal("repeated page changed")
					}
					delivered = append(delivered, page.Entries...)
					if page.AtStart {
						break
					}
					cursor = page.Cursor
				}
				if len(delivered) != 12 {
					t.Fatalf("walk delivered %d entries", len(delivered))
				}
				overlap := append(slices.Clone(delivered), delivered...)
				if testLegacyCheckpoint(overlap, 0, 0, false) != 0 {
					t.Fatal("receipt delivery granted presentation")
				}
			}
		})
	}
}

func TestLegacyRuntimeReceipts_Barriers(t *testing.T) {
	t.Parallel()
	base := []protocol.HistoryEntry{{ID: 3, Type: protocol.TypeMessage, Payload: json.RawMessage(`{}`)}, {ID: 1, Type: protocol.TypeMessage, Payload: json.RawMessage(`{}`)}}
	receipt := protocol.HistoryEntry{ID: 2, Type: protocol.TypeBanner, Payload: json.RawMessage(`{"conversation_id":"c","level":"info","text":"","truncated":false,"stops_turn":false}`)}
	malformed, unknown, missing := receipt, receipt, receipt
	malformed.Payload = json.RawMessage(`{"conversation_id":"c","level":"info"}`)
	unknown.Type, missing.ID = "unknown_fact", 0
	tests := []struct {
		name         string
		middle       *protocol.HistoryEntry
		unidentified bool
		want         uint64
	}{
		{"hole", nil, false, 0}, {"receipt", &receipt, false, 3}, {"unidentified", &receipt, true, 0},
		{"malformed", &malformed, false, 0}, {"unknown", &unknown, false, 0}, {"missing_identity", &missing, false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entries := slices.Clone(base)
			if tt.middle != nil {
				entries = append(entries, *tt.middle)
			}
			if got := testLegacyCheckpoint(entries, 3, 0, tt.unidentified); got != tt.want {
				t.Fatalf("checkpoint=%d want %d", got, tt.want)
			}
		})
	}
	store := history.New(t.TempDir())
	for _, typ := range []string{historyTurnOpened, historyToolInterrupted, historyTurnInterrupted, historySessionDivider, "unknown_fact"} {
		if _, err := store.Append(conversations.ConversationID(testConvID), typ, json.RawMessage(`{}`), historyTS); err != nil {
			t.Fatal(err)
		}
	}
	if page := newHistoryPager(store, discardLogger())(testConvID, "", 128); len(page.Entries) != 0 || !page.AtStart {
		t.Fatalf("invalid facts certified harmless: %+v", page)
	}
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
	page := newHistoryPager(store, discardLogger())(testConvID, "", 128)
	raw := historyEntries(t, store, testConvID)
	events, _ := e.ring.After(testConvID, 0)
	if len(events) != len(raw) || len(bcast.pushes) != 2*len(raw) {
		t.Fatalf("append/live/replay counts=%d/%d/%d", len(raw), len(bcast.pushes), len(events))
	}
	for _, original := range raw {
		var projected protocol.HistoryEntry
		for _, entry := range page.Entries {
			if entry.ID == original.ID {
				projected = entry
			}
		}
		if projected.ID == 0 {
			t.Fatalf("missing projected ID %d", original.ID)
		}
		event := events[original.ID-1]
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
	// Joining history with overlapping replay must preserve the checkpoint.
	joined := slices.Clone(page.Entries)
	for _, event := range events {
		joined = append(joined, protocol.HistoryEntry{ID: event.HistoryEntryID, Type: event.Type, Payload: event.Payload, TS: event.TS})
	}
	if got, want := testLegacyCheckpoint(joined, 3, 0, false), testLegacyCheckpoint(page.Entries, 3, 0, false); got != want {
		t.Fatalf("overlap changed checkpoint %d/%d", got, want)
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
			if len(events) != 4 || len(bcast.pushes) != 4 {
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

func TestLegacyRuntimeReceipts_Watermark(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := history.New(dir)
	id := conversations.ConversationID(testConvID)
	if _, err := store.Append(id, protocol.TypeMessage, json.RawMessage(`{}`), historyTS); err != nil {
		t.Fatal(err)
	}
	for _, typ := range []string{historyTurnOpened, historyToolInterrupted, historyTurnInterrupted, historySessionDivider} {
		shown := true
		if _, err := store.AppendWithMetadata(id, typ, testRuntimeFact(t, typ, testConvID), historyTS, history.Metadata{Shown: &shown}); err != nil {
			t.Fatal(err)
		}
	}
	for _, reader := range []*history.Store{store, history.New(dir)} {
		if got, err := (legacyHistoryReader{reader}).LatestDisplayableEntryID(id); err != nil || got != 1 {
			t.Fatalf("legacy watermark=%d,%v", got, err)
		}
		if got, err := reader.LatestDisplayableEntryID(id); err != nil || got != 5 {
			t.Fatalf("raw watermark=%d,%v", got, err)
		}
	}
	// Unknown content is not certified harmless by the legacy view.
	if _, err := store.Append(id, "future_content", json.RawMessage(`{}`), historyTS); err != nil {
		t.Fatal(err)
	}
	if got, err := (legacyHistoryReader{store}).LatestDisplayableEntryID(id); err != nil || got != 6 {
		t.Fatalf("unknown watermark=%d,%v", got, err)
	}
	if _, err := store.AppendWithMetadata(id, protocol.TypeMessage, json.RawMessage(`{}`), historyTS, history.Metadata{Shown: new(bool)}); err != nil {
		t.Fatal(err)
	}
	if got, err := (legacyHistoryReader{store}).LatestDisplayableEntryID(id); err != nil || got != 6 {
		t.Fatalf("hidden eligibility raised watermark=%d,%v", got, err)
	}
}

func TestLegacyRuntimeReceipts_ReadMarks(t *testing.T) {
	t.Parallel()
	for _, held := range []uint64{0, 9} {
		t.Run(fmt.Sprint(held), func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "conversations.json")
			reg, err := conversations.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			reg.Create(conversations.Conversation{ID: conversations.ConversationID(testConvID), ReadUpTo: held, IsPromoted: true})
			reg.Create(conversations.Conversation{ID: conversations.ConversationID(testConvIDB), ReadUpTo: 2, IsPromoted: true})
			if err := reg.Save(path); err != nil {
				t.Fatal(err)
			}
			store := history.New(root)
			id := conversations.ConversationID(testConvID)
			if _, err := store.Append(id, protocol.TypeMessage, json.RawMessage(`{}`), historyTS); err != nil {
				t.Fatal(err)
			}
			if appendConversationHistory(store, discardLogger(), "test", testConvID, historyTurnInterrupted, testRuntimeFact(t, historyTurnInterrupted, testConvID), historyTS) == nil {
				t.Fatal("append failed")
			}
			reader := legacyHistoryReader{store}
			invoke := func(h dispatch.Handler, typ string, payload json.RawMessage) protocol.Envelope {
				out := make(chan protocol.RoutingEnvelope, 2)
				conn := dispatch.NewTestConn("phone", out, nil)
				if err := h(context.Background(), conn, protocol.Envelope{ID: 10, Type: typ, Payload: payload}); err != nil {
					t.Fatal(err)
				}
				var env protocol.Envelope
				if err := json.Unmarshal((<-out).Frame, &env); err != nil {
					t.Fatal(err)
				}
				return env
			}
			list := invoke(handlers.ListConversationsWithAgents(reg, nil, reader), protocol.TypeListConversations, json.RawMessage(`{}`))
			var summary protocol.ConversationsPayload
			if err := json.Unmarshal(list.Payload, &summary); err != nil {
				t.Fatal(err)
			}
			if list.Type != protocol.TypeConversations || len(summary.Conversations) != 2 || summary.Conversations[0].LatestEntryID != 1 {
				t.Fatalf("list=%+v", list)
			}
			mark := handlers.MarkConversationRead(reg, reader, path, nil, discardLogger())
			for i := 0; i < 2; i++ {
				env := invoke(mark, protocol.TypeMarkConversationRead, json.RawMessage(`{"conversation_id":"`+testConvID+`","up_to":99}`))
				if env.Type != protocol.TypeConversationUpdated {
					t.Fatalf("mark=%+v", env)
				}
			}
			reopened, err := conversations.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			cv, _ := reopened.Get(id)
			if cv.ReadUpTo != max(held, 1) {
				t.Fatalf("held mark=%d", cv.ReadUpTo)
			}
			other, _ := reopened.Get(conversations.ConversationID(testConvIDB))
			if other.ReadUpTo != 2 {
				t.Fatal("mark crossed conversation")
			}
			host, err := conversations.Load(filepath.Join(t.TempDir(), "conversations.json"))
			if err != nil {
				t.Fatal(err)
			}
			host.Create(conversations.Conversation{ID: id})
			isolated, _ := host.Get(id)
			if isolated.ReadUpTo != 0 {
				t.Fatal("mark crossed host")
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
	page := newHistoryPager(history.New(root), discardLogger())(testConvID, "", 128)
	if len(page.Entries) != 2 || page.Entries[0].ID != 2 || page.Entries[0].Type != protocol.TypeBanner {
		t.Fatalf("startup divider receipt=%+v", page)
	}
	if got := testLegacyCheckpoint(page.Entries, 1, 0, false); got != 2 {
		t.Fatalf("startup checkpoint=%d", got)
	}
}

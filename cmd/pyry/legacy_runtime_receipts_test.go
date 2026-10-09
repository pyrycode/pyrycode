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
		case protocol.TypeTurnState, protocol.TypeContextUsage:
			nonvisual = true
		case protocol.TypeTurnEnd:
			var end protocol.TurnEndPayload
			valid = json.Unmarshal(entry.Payload, &end) == nil
			nonvisual = end.StopReason == "end_turn" && !end.IsError &&
				(end.Outcome == "" || end.Outcome == "success") &&
				(end.TerminalReason == "" || end.TerminalReason == "completed") && end.ErrorCategory == ""
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

// testLegacyLatest mirrors mobile contributesToUnreadWatermark on received wire
// entries. Keep this independent of the daemon's eligibility/visibility helpers.
func testLegacyLatest(entries []protocol.HistoryEntry) uint64 {
	var latest uint64
	for _, entry := range entries {
		switch entry.Type {
		case "turn_state", "stall", "api_retry", "compacting", "session_transition":
			continue
		}
		latest = max(latest, entry.ID)
	}
	return latest
}

func testLegacySerializedPage(t *testing.T, store *history.Store) ([]protocol.HistoryEntry, []byte) {
	t.Helper()
	page := newHistoryPager(store, discardLogger())(testConvID, "", 128)
	if !page.AtStart || page.Cursor != "" {
		t.Fatal("expected terminal page")
	}
	wire, err := json.Marshal(protocol.HistoryPagePayload{Entries: page.Entries, Cursor: page.Cursor, AtStart: page.AtStart})
	if err != nil {
		t.Fatal(err)
	}
	var decoded protocol.HistoryPagePayload
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded.Entries, wire
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
			wantLatest, wantCheckpoint, wantRaw := uint64(4), uint64(5), uint64(3)
			if enabled {
				wantLatest, wantCheckpoint, wantRaw = 5, 6, 4
			}
			rawEntries := historyEntries(t, store, testConvID)
			entries, wire := testLegacySerializedPage(t, store)
			versions := map[string][]protocol.HistoryEntry{"history": entries, "phone": nil, "peer": nil, "replay": nil}
			addEnvelope := func(name string, env protocol.Envelope) {
				encoded, err := json.Marshal(env)
				if err != nil {
					t.Fatal(err)
				}
				var decoded protocol.Envelope
				if err := json.Unmarshal(encoded, &decoded); err != nil || decoded.HistoryEntryID == nil {
					t.Fatalf("missing serialized identity: %s / %v", encoded, err)
				}
				versions[name] = append(versions[name], protocol.HistoryEntry{ID: *decoded.HistoryEntryID, Type: decoded.Type, Payload: decoded.Payload, TS: decoded.TS})
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
				if got := testLegacyLatest(received); got != wantLatest {
					t.Fatalf("%s latest=%d want %d", name, got, wantLatest)
				}
				if got := testLegacyCheckpoint(received, wantLatest, 0, false); got != wantCheckpoint {
					t.Fatalf("%s checkpoint=%d want %d", name, got, wantCheckpoint)
				}
				if testLegacyCheckpoint(received, 0, 0, false) != 0 {
					t.Fatalf("%s receipt delivery conferred presentation", name)
				}
			}
			for _, reader := range []*history.Store{store, history.New(dir)} {
				testLegacyListAndMark(t, reader, wantCheckpoint, wantLatest, 0)
				if got, err := reader.LatestDisplayableEntryID(conversations.ConversationID(testConvID)); err != nil || got != wantRaw {
					t.Fatalf("raw watermark=%d,%v want %d", got, err, wantRaw)
				}
				if got := historyEntries(t, reader, testConvID); !reflect.DeepEqual(got, rawEntries) {
					t.Fatal("raw entries or metadata changed")
				}
			}
			t.Logf("mobile handoff enabled=%v latest=%d checkpoint=%d clamp=%d page=%s", enabled, wantLatest, wantCheckpoint, wantLatest, wire)
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
					if got, err := (legacyHistoryReader{reader}).LatestDisplayableEntryID(id); err != nil || got != 12 {
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
		if got, err := (legacyHistoryReader{reader}).LatestDisplayableEntryID(id); err != nil || got != 5 {
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
	if got, err := (legacyHistoryReader{store}).LatestDisplayableEntryID(id); err != nil || got != 7 {
		t.Fatalf("hidden eligible target=%d,%v", got, err)
	}
	// More than one reader page of explicitly shown statuses must not hide
	// older wire-counted content or raise the legacy target.
	for i := 0; i < 130; i++ {
		shown := true
		if _, err := store.AppendWithMetadata(id, protocol.TypeTurnState, json.RawMessage(`{}`), historyTS, history.Metadata{Shown: &shown}); err != nil {
			t.Fatal(err)
		}
	}
	for _, reader := range []*history.Store{store, history.New(dir)} {
		if got, err := (legacyHistoryReader{reader}).LatestDisplayableEntryID(id); err != nil || got != 7 {
			t.Fatalf("multi-page legacy target=%d,%v", got, err)
		}
		if got, err := reader.LatestDisplayableEntryID(id); err != nil || got != 137 {
			t.Fatalf("raw visibility target=%d,%v", got, err)
		}
	}
}

func testLegacyListAndMark(t *testing.T, store *history.Store, checkpoint, latest, held uint64) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "conversations.json")
	reg, err := conversations.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	id := conversations.ConversationID(testConvID)
	reg.Create(conversations.Conversation{ID: id, ReadUpTo: held, IsPromoted: true})
	reg.Create(conversations.Conversation{ID: conversations.ConversationID(testConvIDB), ReadUpTo: 2, IsPromoted: true})
	if err := reg.Save(path); err != nil {
		t.Fatal(err)
	}
	reader := legacyHistoryReader{store}
	invoke := func(h dispatch.Handler, typ string, payload json.RawMessage, requestID uint64) protocol.Envelope {
		out := make(chan protocol.RoutingEnvelope, 2)
		conn := dispatch.NewTestConn("phone", out, nil)
		if err := h(context.Background(), conn, protocol.Envelope{ID: requestID, Type: typ, Payload: payload}); err != nil {
			t.Fatal(err)
		}
		var env protocol.Envelope
		select {
		case frame := <-out:
			if err := json.Unmarshal(frame.Frame, &env); err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatal("missing handler reply")
		}
		if env.InReplyTo == nil || *env.InReplyTo != requestID {
			t.Fatalf("uncorrelated reply: %+v", env)
		}
		return env
	}
	assertList := func(wantRead uint64) {
		list := invoke(handlers.ListConversationsWithAgents(reg, nil, reader), protocol.TypeListConversations, json.RawMessage(`{}`), 10)
		var summary protocol.ConversationsPayload
		if err := json.Unmarshal(list.Payload, &summary); err != nil {
			t.Fatal(err)
		}
		if list.Type != protocol.TypeConversations || len(summary.Conversations) != 2 || summary.Conversations[0].LatestEntryID != latest || summary.Conversations[0].ReadUpTo != wantRead {
			t.Fatalf("list=%+v want latest/read %d/%d", summary, latest, wantRead)
		}
	}
	assertList(held)
	mark := handlers.MarkConversationRead(reg, reader, path, nil, discardLogger())
	want := max(held, min(checkpoint, latest))
	for i, upTo := range []uint64{checkpoint, 0, checkpoint, checkpoint + 100} {
		env := invoke(mark, protocol.TypeMarkConversationRead, json.RawMessage(fmt.Sprintf(`{"conversation_id":"%s","up_to":%d}`, testConvID, upTo)), uint64(20+i))
		var updated protocol.ConversationUpdatedPayload
		if err := json.Unmarshal(env.Payload, &updated); err != nil {
			t.Fatal(err)
		}
		if env.Type != protocol.TypeConversationUpdated || updated.ID != testConvID || updated.ReadUpTo != want || latest > updated.ReadUpTo {
			t.Fatalf("mark=%+v want %d clearing latest %d", updated, want, latest)
		}
		reg, err = conversations.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		cv, _ := reg.Get(id)
		other, _ := reg.Get(conversations.ConversationID(testConvIDB))
		if cv.ReadUpTo != want || other.ReadUpTo != 2 {
			t.Fatalf("persisted mark/isolation=%d/%d", cv.ReadUpTo, other.ReadUpTo)
		}
		mark = handlers.MarkConversationRead(reg, reader, path, nil, discardLogger())
		assertList(want)
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
}

func TestLegacyRuntimeReceipts_ReadMarks(t *testing.T) {
	t.Parallel()
	for _, held := range []uint64{0, 9} {
		t.Run(fmt.Sprint(held), func(t *testing.T) {
			dir := t.TempDir()
			store := history.New(dir)
			if _, err := store.Append(conversations.ConversationID(testConvID), protocol.TypeMessage, json.RawMessage(`{}`), historyTS); err != nil {
				t.Fatal(err)
			}
			if appendConversationHistory(store, discardLogger(), "test", testConvID, historyTurnInterrupted, testRuntimeFact(t, historyTurnInterrupted, testConvID), historyTS) == nil {
				t.Fatal("append failed")
			}
			for _, reader := range []*history.Store{store, history.New(dir)} {
				testLegacyListAndMark(t, reader, 99, 2, held)
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
					excluded := slices.Contains([]string{"turn_state", "stall", "api_retry", "compacting", "session_transition"}, typ)
					unsupported := typ == "future_content" || typ == "invalid_runtime_fact"
					want := uint64(1)
					if excluded || unsupported && visibility == "false" {
						want = 0
					}
					for _, reader := range []*history.Store{store, history.New(dir)} {
						if got, err := (legacyHistoryReader{reader}).LatestDisplayableEntryID(id); err != nil || got != want {
							t.Fatalf("legacy target=%d,%v want %d", got, err, want)
						}
						entries, _ := testLegacySerializedPage(t, reader)
						if unsupported {
							if len(entries) != 0 {
								t.Fatal("unsupported entry certified as receipt")
							}
						} else if got := testLegacyLatest(entries); got != want {
							t.Fatalf("client latest=%d want %d", got, want)
						}
						raw, err := reader.Page(id, "", 1)
						if err != nil || len(raw.Entries) != 1 || raw.Entries[0].Type != entryType || !bytes.Equal(raw.Entries[0].Payload, payload) || !raw.Entries[0].TS.Equal(historyTS) || !reflect.DeepEqual(raw.Entries[0].Shown, shown) || !reflect.DeepEqual(raw.Entries[0].Session, metadata.Session) {
							t.Fatalf("raw entry changed: %+v / %v", raw, err)
						}
						if got, err := reader.LatestEntryID(id); err != nil || got != 1 {
							t.Fatalf("raw cursor=%d,%v", got, err)
						}
						testLegacyListAndMark(t, reader, 1, want, 0)
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
			entries, _ := testLegacySerializedPage(t, store)
			wantBeforeCompletion := uint64(6)
			if ending.shown {
				wantBeforeCompletion = 4
			}
			if got := testLegacyCheckpoint(entries, 4, 0, false); got != wantBeforeCompletion {
				t.Fatalf("unpresented terminal outcome checkpoint=%d want %d", got, wantBeforeCompletion)
			}
			for _, tail := range []struct {
				name               string
				append             func()
				latest, checkpoint uint64
			}{
				{"completion", func() {}, 5, 6},
				{"metadata", func() {
					e.HandleFor(ctx, testConvID, turnevent.ContextUsage{Model: "synthetic", TotalTokens: 1, MaxTokens: 100}, source)
				}, 7, 7},
				{"runtime", func() {
					e.emit(ctx, testConvID, historySessionDivider, runtimeHistoryFact{ConversationID: testConvID, Cause: "daemon_restart", OccurredAt: historyTS})
				}, 8, 8},
			} {
				t.Run(tail.name, func(t *testing.T) {
					tail.append()
					for _, reader := range []*history.Store{store, history.New(dir)} {
						entries, _ := testLegacySerializedPage(t, reader)
						if got := testLegacyLatest(entries); got != tail.latest {
							t.Fatalf("client latest=%d want %d", got, tail.latest)
						}
						// The completion version was explicitly presented. Metadata and
						// receipt tails extend accounting without granting new sight.
						if got := testLegacyCheckpoint(entries, 5, 0, false); got != tail.checkpoint {
							t.Fatalf("checkpoint=%d want %d", got, tail.checkpoint)
						}
						if testLegacyCheckpoint(entries, 0, 0, false) != 0 {
							t.Fatal("tail granted presentation")
						}
						testLegacyListAndMark(t, reader, tail.checkpoint, tail.latest, 0)
					}
				})
			}
		})
	}
}

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/msgqueue"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

func TestHistoryProjection_BoundedWalk(t *testing.T) {
	t.Parallel()
	for _, allFiltered := range []bool{false, true} {
		t.Run(fmt.Sprint(allFiltered), func(t *testing.T) {
			dir := t.TempDir()
			store := history.New(dir)
			id := conversations.ConversationID(testConvID)
			var want []history.Entry
			shown := []*bool{nil, new(bool), new(bool)}
			*shown[2] = true
			for i := 0; i < 15; i++ {
				typ := "synthetic_thread_fact"
				if !allFiltered && (i == 0 || i == 6 || i == 14) {
					typ = protocol.TypeAssistantDelta
				}
				raw := json.RawMessage(fmt.Sprintf(`{"text":"entry-%d"}`, i))
				ts := historyTS.Add(time.Duration(i) * time.Second)
				n, err := store.AppendWithMetadata(id, typ, raw, ts, history.Metadata{Shown: shown[i%3]})
				if err != nil {
					t.Fatal(err)
				}
				if typ == protocol.TypeAssistantDelta {
					want = append([]history.Entry{{ID: n, Type: typ, Payload: raw, TS: ts}}, want...)
				}
			}
			for _, reader := range []*history.Store{store, history.New(dir)} {
				pager := newHistoryPager(reader, discardLogger())
				cursor := ""
				var got []protocol.HistoryEntry
				emptyPages := 0
				for steps := 0; ; steps++ {
					if steps >= 15 {
						t.Fatal("walk did not terminate")
					}
					raw, err := reader.Page(id, cursor, 2)
					if err != nil {
						t.Fatal(err)
					}
					page := pager(testConvID, cursor, 2)
					if page.Outcome != relay.HistoryPageOK || page.Cursor != raw.Cursor || page.AtStart != raw.AtStart {
						t.Fatalf("raw/page cursor or outcome changed: %#v / %#v", raw, page)
					}
					expectedCount := 0
					for _, e := range raw.Entries {
						if e.Type == protocol.TypeAssistantDelta {
							expectedCount++
						}
					}
					if len(page.Entries) != expectedCount {
						t.Fatalf("filtered page contains %d entries, want %d from this raw page", len(page.Entries), expectedCount)
					}
					if len(page.Entries) == 0 {
						emptyPages++
					}
					got = append(got, page.Entries...)
					if page.AtStart {
						if page.Cursor != "" {
							t.Fatal("terminal cursor is nonempty")
						}
						break
					}
					if page.Cursor == "" || page.Cursor == cursor {
						t.Fatal("nonterminal empty page lost usable cursor")
					}
					cursor = page.Cursor
				}
				if emptyPages < 4 || len(got) != len(want) {
					t.Fatalf("walk got %d entries and %d empty pages", len(got), emptyPages)
				}
				for i, e := range got {
					w := want[i]
					if e.ID != w.ID || e.Type != w.Type || !e.TS.Equal(w.TS) || !bytes.Equal(e.Payload, w.Payload) {
						t.Fatalf("entry %d changed: %#v", i, e)
					}
				}
			}
		})
	}
}

func TestHistoryProjection_UnknownEmitIsDurableOnly(t *testing.T) {
	t.Parallel()
	for _, connected := range []bool{false, true} {
		t.Run(fmt.Sprint(connected), func(t *testing.T) {
			dir := t.TempDir()
			bcast := &fakeInteractiveBcast{}
			if connected {
				bcast.snapshots = [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}, {ConnID: "b", Interactive: true}, {ConnID: "legacy"}}}
			}
			e := newInteractiveTurnEmitterV2(&stubCursor{}, bcast, discardLogger())
			e.hist = history.New(dir)
			e.emit(context.Background(), testConvID, "synthetic_thread_fact", json.RawMessage(`{"detail":"private"}`))
			entries := historyEntries(t, history.New(dir), testConvID)
			if len(entries) != 1 || entries[0].Type != "synthetic_thread_fact" || string(entries[0].Payload) != `{"detail":"private"}` {
				t.Fatalf("raw durable facts = %#v", entries)
			}
			events, _ := e.ring.After(testConvID, 0)
			if len(events) != 0 || len(bcast.pushes) != 0 || bcast.callIdx != 0 {
				t.Fatalf("fact entered legacy stream/replay: %d events, %d pushes", len(events), len(bcast.pushes))
			}
			page := newHistoryPager(history.New(dir), discardLogger())(testConvID, "", 10)
			if len(page.Entries) != 0 || !page.AtStart {
				t.Fatalf("fact entered legacy history: %#v", page)
			}
		})
	}
}

func TestHistoryProjection_InteractiveMetadata(t *testing.T) {
	t.Parallel()
	tests := []struct {
		typ, raw string
		shown    bool
	}{
		{protocol.TypeAssistantDelta, `{"text":"reply"}`, true},
		{protocol.TypeToolUse, `{}`, true}, {protocol.TypeToolResult, `{}`, true}, {protocol.TypeToolDenied, `{}`, true},
		{protocol.TypeBackgroundTaskStarted, `{}`, true}, {protocol.TypeCompactionBoundary, `{}`, true},
		{protocol.TypeModelRefusalFallback, `{}`, true}, {protocol.TypeModelRefusalNoFallback, `{}`, true}, {protocol.TypeUnrecognizedMessage, `{}`, true},
		{protocol.TypeTurnEnd, `{"stop_reason":"end_turn"}`, false},
		{protocol.TypeTurnEnd, `{"stop_reason":"end_turn","outcome":"success","terminal_reason":"completed"}`, false},
		{protocol.TypeTurnEnd, `{"stop_reason":"cancelled"}`, true},
		{protocol.TypeTurnEnd, `{"stop_reason":"max_turns"}`, true},
		{protocol.TypeTurnEnd, `{"stop_reason":"unknown"}`, true},
		{protocol.TypeTurnEnd, `{"stop_reason":"end_turn","outcome":"error_max_turns"}`, true},
		{protocol.TypeTurnEnd, `{"stop_reason":"end_turn","outcome":"success","is_error":true}`, true},
		{protocol.TypeTurnEnd, `{"stop_reason":"end_turn","terminal_reason":"budget_exhausted"}`, true},
		{protocol.TypeTurnEnd, `{"stop_reason":"end_turn","error_category":"rate_limit"}`, true},
		{protocol.TypeBanner, `{"level":"info","stops_turn":false}`, false},
		{protocol.TypeBanner, `{"level":"info","stops_turn":true}`, true},
		{protocol.TypeBanner, `{"level":"warning"}`, true}, {protocol.TypeBanner, `{"level":"unknown"}`, true},
		{protocol.TypeBackgroundTaskUpdated, `{"patch":"changed"}`, false},
		{protocol.TypeBackgroundTaskUpdated, `{"status":"completed"}`, true},
		{protocol.TypeBackgroundTaskUpdated, `{"status":"unknown_terminal"}`, true},
		{protocol.TypeBackgroundTaskUpdated, `{"summary":"finished"}`, true},
	}
	for _, typ := range []string{protocol.TypeTurnState, protocol.TypeStall, protocol.TypeApiRetry, protocol.TypeCompacting, protocol.TypeToolProgress, protocol.TypeBackgroundTaskRoster, protocol.TypeBackgroundTaskProgress, protocol.TypeThinkingProgress, protocol.TypeRateLimited, protocol.TypeContextUsage, protocol.TypeModelAnnounced, protocol.TypeSessionFacts, protocol.TypeMCPStatus, protocol.TypeModelList, protocol.TypeSlashCommandList} {
		tests = append(tests, struct {
			typ, raw string
			shown    bool
		}{typ, `{}`, false})
	}
	for i, tt := range tests {
		t.Run(fmt.Sprintf("%s-%d", tt.typ, i), func(t *testing.T) {
			dir := t.TempDir()
			store := history.New(dir)
			// Earlier legacy entries keep their absent visibility and fallback unread classification.
			if _, err := store.Append(conversations.ConversationID(testConvID), protocol.TypeAssistantDelta, json.RawMessage(`{"text":"old"}`), historyTS); err != nil {
				t.Fatal(err)
			}
			bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}, {ConnID: "b", Interactive: true}, {ConnID: "legacy"}}}}
			e := newInteractiveTurnEmitterV2(&stubCursor{}, bcast, discardLogger())
			e.hist = store
			e.emit(context.Background(), testConvID, tt.typ, json.RawMessage(tt.raw))
			entries := historyEntries(t, store, testConvID)
			if len(entries) != 2 || entries[0].Shown != nil || entries[1].Shown == nil || *entries[1].Shown != tt.shown || entries[1].Session != nil {
				t.Fatalf("stored visibility = %#v", entries)
			}
			evs, _ := e.ring.After(testConvID, 0)
			assertLogMatchesRing(t, entries[1:], evs)
			if len(bcast.pushes) != 2 {
				t.Fatalf("recipient gates changed: %d pushes", len(bcast.pushes))
			}
			for _, p := range bcast.pushes {
				if !bytes.Equal(p.env.Payload, entries[1].Payload) || p.env.HistoryEntryID == nil || *p.env.HistoryEntryID != 2 {
					t.Fatalf("wire payload/identity changed: %#v", p.env)
				}
				var keys map[string]json.RawMessage
				if err := json.Unmarshal(p.env.Payload, &keys); err != nil {
					t.Fatal(err)
				}
				if keys["shown"] != nil || keys["session"] != nil {
					t.Fatal("metadata leaked into payload")
				}
			}
			page := newHistoryPager(store, discardLogger())(testConvID, "", 10)
			if len(page.Entries) != 2 || page.Entries[0].ID != 2 {
				t.Fatalf("hidden legacy event filtered: %#v", page)
			}
			want := uint64(1)
			if tt.shown {
				want = 2
			}
			for _, reader := range []*history.Store{store, history.New(dir)} {
				n, err := reader.LatestDisplayableEntryID(conversations.ConversationID(testConvID))
				if err != nil || n != want {
					t.Fatalf("unread watermark=%d,%v want %d", n, err, want)
				}
				if old := historyEntries(t, reader, testConvID)[0]; old.Shown != nil {
					t.Fatal("old entry reclassified")
				}
			}
		})
	}
}

func TestHistoryProjection_OtherProducerMetadata(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"clear", "idle_evict", "operator", "channel"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			store := history.New(dir)
			ctx := context.Background()
			switch kind {
			case "clear", "idle_evict":
				e := newSessionTransitionEmitterV2(&fakeInteractiveBcast{}, constResolver(testConvID, true), discardLogger())
				e.hist = store
				reason := sessions.ReasonClear
				if kind == "idle_evict" {
					reason = sessions.ReasonEviction
				}
				e.broadcast(ctx, sessions.SessionTransition{PreviousID: "old", NewID: "new", Reason: reason, OccurredAt: occurred})
			case "operator":
				newOperatorMessageHistory(store, nil, nil, discardLogger())(testConvID, msgqueue.QueuedMessage{Text: "operator"})
			case "channel":
				d := testDelivery(t, dir, store, nil)
				err := d.deliver(channelDeliveryPost{ConversationID: conversations.ConversationID(testConvID), TurnID: "post", Text: "channel", TS: historyTS})
				if err != nil {
					t.Fatal(err)
				}
			}
			for _, reader := range []*history.Store{store, history.New(dir)} {
				entries := historyEntries(t, reader, testConvID)
				wantLen := 1
				if kind == "channel" {
					wantLen = 2
				}
				if len(entries) != wantLen {
					t.Fatalf("entries=%#v", entries)
				}
				wantUnread := uint64(0)
				for _, e := range entries {
					shown := kind != "idle_evict" && e.Type != protocol.TypeTurnEnd
					if e.Shown == nil || *e.Shown != shown || e.Session != nil {
						t.Fatalf("%s metadata=%#v", kind, e)
					}
					if shown {
						wantUnread = e.ID
					}
				}
				n, err := reader.LatestDisplayableEntryID(conversations.ConversationID(testConvID))
				if err != nil || n != wantUnread {
					t.Fatalf("%s unread=%d,%v want %d", kind, n, err, wantUnread)
				}
			}
		})
	}
}

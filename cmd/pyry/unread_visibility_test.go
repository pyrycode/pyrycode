package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/relay/handlers"
	"github.com/pyrycode/pyrycode/internal/thread"
)

func TestUnreadHiddenTails(t *testing.T) {
	t.Parallel()
	for _, tail := range []string{"context_usage", "restarts", "future_hidden"} {
		for _, metadata := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/metadata=%t", tail, metadata), func(t *testing.T) {
				dir := t.TempDir()
				h := history.New(dir)
				put := func(typ, payload string) uint64 {
					t.Helper()
					m := history.Metadata{}
					if metadata {
						m = historyVisibilityMetadata(typ, json.RawMessage(payload))
					}
					id, err := h.AppendWithMetadata(testConvID, typ, json.RawMessage(payload), historyTS, m)
					if err != nil {
						t.Fatal(err)
					}
					return id
				}
				check := func(t *testing.T, latest uint64) {
					t.Helper()
					for _, reader := range []*history.Store{h, history.New(dir)} {
						for _, held := range []uint64{0, latest, 999} {
							testLegacyListAndMark(t, reader, latest, latest, held)
						}
					}
				}
				put("context_usage", `{}`)
				t.Run("hidden-only", func(t *testing.T) { check(t, 0) })
				first := put("message", `{"role":"user","text":"request"}`)
				latest := put("assistant_delta", `{"text":"reply"}`)
				put("turn_end", `{"stop_reason":"end_turn"}`)
				put("context_usage", `{}`)
				if tail == "restarts" {
					for i := 0; i < 130; i++ {
						put("session_divider", fmt.Sprintf(`{"conversation_id":%q,"cause":"daemon_restart","occurred_at":%q}`, testConvID, historyTS.Format(time.RFC3339Nano)))
					}
				}
				if tail == "future_hidden" {
					put("future_hidden", `{}`)
				}
				t.Run("hidden-tail", func(t *testing.T) { check(t, latest) })
				// Reading an older visible entry must leave the newer reply unread.
				for _, reader := range []*history.Store{h, history.New(dir)} {
					testLegacyListAndMark(t, reader, first, latest, 0)
				}
				read := latest
				latest = put("message", `{"role":"user","text":"next"}`)
				for _, reader := range []*history.Store{h, history.New(dir)} {
					testLegacyListAndMark(t, reader, read, latest, read)
				}
				check(t, latest)
			})
		}
	}
}

func TestUnreadRestartReconciliation(t *testing.T) {
	t.Parallel()
	for _, held := range []uint64{0, 1, 99} {
		t.Run(fmt.Sprint(held), func(t *testing.T) {
			dir := t.TempDir()
			h := history.New(dir)
			if _, err := h.Append(testConvID, "message", json.RawMessage(`{"text":"completed"}`), historyTS); err != nil {
				t.Fatal(err)
			}
			reg, err := conversations.Load(filepath.Join(dir, "conversations.json"))
			if err != nil {
				t.Fatal(err)
			}
			reg.Create(conversations.Conversation{ID: testConvID, ReadUpTo: held})
			for i := 0; i < 3; i++ {
				h = history.New(dir)
				reconcileStartupHistory(h, reg, discardLogger(), historyTS.Add(time.Duration(i)*time.Second))
				latest, err := (legacyHistoryReader{h}).LatestDisplayableEntryID(testConvID)
				cv, _ := reg.Get(testConvID)
				if err != nil || latest != 1 || cv.ReadUpTo != held || (latest > cv.ReadUpTo) != (held == 0) {
					t.Fatalf("restart %d: latest=%d read=%d err=%v", i, latest, cv.ReadUpTo, err)
				}
				testLegacyListAndMark(t, h, 1, 1, held)
			}
		})
	}
}

func TestUnreadThreadHiddenTails(t *testing.T) {
	cfg, h, store, shadow, _, _ := testThreadWatermark(t, false)
	reader := legacyHistoryReader{h}
	path := filepath.Join(t.TempDir(), "conversations.json")
	w, _ := testDaemonThreadQueryWire(t, shadow, func(c *relay.V2SessionConfig) {
		c.Handlers = make(map[string]dispatch.Handler)
		c.Handlers[protocol.TypeListConversations] = handlers.ListConversationsWithAgents(shadow.registry, nil, reader)
		c.Handlers[protocol.TypeMarkConversationRead] = handlers.MarkConversationRead(shadow.registry, c.ThreadLastShownVersion, path, nil, discardLogger())
	})
	w.open("phone", false, protocol.CapabilityThread)
	w.open("legacy", false)
	want := uint64(1)
	held := uint64(0)
	check := func(present bool) {
		t.Helper()
		legacy, err := (legacyHistoryReader{h}).LatestDisplayableEntryID(testConvID)
		shown, ok := cfg.ThreadLastShownVersion(testConvID)
		if err != nil || legacy != want || ok != present || present && shown != legacy {
			t.Fatalf("legacy=%d thread=%d/%t want present=%t err=%v", legacy, shown, ok, present, err)
		}
		if present {
			for _, held := range []uint64{0, want} {
				if (legacy > held) != (shown > held) {
					t.Fatal("routes disagree against shared mark")
				}
			}
		}
		for _, peer := range []string{"phone", "legacy"} {
			testDaemonThreadRequest(t, w, peer, 70, protocol.TypeListConversations, struct{}{})
			gotPeer, env := w.read()
			var rows protocol.ConversationsPayload
			if json.Unmarshal(env.Payload, &rows) != nil || gotPeer != peer || env.Type != protocol.TypeConversations || len(rows.Conversations) != 1 {
				t.Fatalf("list reply: %s %+v", gotPeer, env)
			}
			row := rows.Conversations[0]
			if row.LatestEntryID != want || (row.LastShownVersion != nil) != (peer == "phone" && present) {
				t.Fatalf("route %s: %+v", peer, row)
			}
			if row.LastShownVersion != nil && (*row.LastShownVersion != want || (*row.LastShownVersion > row.ReadUpTo) != (row.LatestEntryID > row.ReadUpTo)) {
				t.Fatalf("unread route mismatch: %+v", row)
			}
		}
		testDaemonThreadRequest(t, w, "phone", 71, protocol.TypeMarkConversationRead, map[string]any{"conversation_id": testConvID, "up_to": 999})
		peer, env := w.read()
		if peer != "phone" || env.InReplyTo == nil || *env.InReplyTo != 71 {
			t.Fatalf("uncorrelated mark reply: %s %+v", peer, env)
		}
		if !present {
			var refusal protocol.ErrorPayload
			if json.Unmarshal(env.Payload, &refusal) != nil || env.Type != protocol.TypeError || refusal.Code != protocol.CodeHistoryUnavailable || !refusal.Retryable {
				t.Fatalf("unavailable evidence reply: %+v", env)
			}
			persisted, err := conversations.Load(path)
			cv, _ := shadow.registry.Get(testConvID)
			durable, _ := persisted.Get(testConvID)
			if err != nil || cv.ReadUpTo != held || durable.ReadUpTo != held {
				t.Fatalf("unavailable evidence changed held mark: memory=%d disk=%d err=%v", cv.ReadUpTo, durable.ReadUpTo, err)
			}
			return
		}
		var updated struct {
			protocol.ConversationUpdatedPayload
			LastShownVersion *uint64 `json:"last_shown_version"`
		}
		if json.Unmarshal(env.Payload, &updated) != nil || env.Type != protocol.TypeConversationUpdated || updated.ReadUpTo != want || updated.LastShownVersion == nil || *updated.LastShownVersion != want {
			t.Fatalf("shared mark reply: %s %+v", peer, env)
		}
		held = want
	}
	check(true) // Eligible unloaded certificate.
	put := func(typ, payload string) {
		t.Helper()
		if appendConversationHistory(h, discardLogger(), "test", testConvID, typ, json.RawMessage(payload), historyTS) == nil {
			t.Fatal("append failed")
		}
	}
	put("main_turn_opened", fmt.Sprintf(`{"conversation_id":%q,"turn_id":"turn","occurred_at":%q}`, testConvID, historyTS.Format(time.RFC3339Nano)))
	put("assistant_delta", fmt.Sprintf(`{"conversation_id":%q,"turn_id":"turn","message_id":"reply","text":"reply"}`, testConvID))
	put("turn_end", fmt.Sprintf(`{"conversation_id":%q,"turn_id":"turn","stop_reason":"end_turn"}`, testConvID))
	want = 3
	if err := store.Load(t.Context(), testConvID); err != nil {
		t.Fatal(err)
	}
	testShadowWait(t, func() bool { s := store.Status(testConvID); return s.State == thread.StateUsable && s.Version == 4 })
	check(true)
	revised := false
	for _, item := range store.Snapshot(testConvID).Items {
		if item.Rev == 4 {
			revised = true
		}
	}
	if !revised {
		t.Fatal("completion did not exercise an item revision beyond shown content")
	}
	if err := store.Unload(testConvID); err != nil {
		t.Fatal(err)
	}
	check(true)
	for _, entry := range []struct{ typ, payload string }{
		{"context_usage", `{}`},
		{"turn_state", `{"state":"idle"}`},
		{"banner", `{"level":"info"}`},
		{"session_divider", `{"cause":"daemon_restart"}`},
		{"session_divider", `{"cause":"idle_sleep"}`},
		{"future_hidden", `{}`},
	} {
		put(entry.typ, entry.payload)
	}
	check(false) // Hidden progress invalidates the old certificate.
	if err := store.Load(t.Context(), testConvID); err != nil {
		t.Fatal(err)
	}
	testShadowWait(t, func() bool { s := store.Status(testConvID); return s.State == thread.StateUsable && s.Version == 10 })
	check(true) // Loaded metadata excludes hidden item/live-state revisions.
	if err := store.Unload(testConvID); err != nil {
		t.Fatal(err)
	}
	check(true) // Newly eligible certificate retains shown progress.
}

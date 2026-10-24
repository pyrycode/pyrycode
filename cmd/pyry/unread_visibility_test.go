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

func TestUnreadRestartReconciliation(t *testing.T) {
	t.Parallel()
	for _, held := range []uint64{0, 1, 99} {
		t.Run(fmt.Sprint(held), func(t *testing.T) {
			dir := t.TempDir()
			h := history.New(dir)
			if _, err := h.Append(testConvID, "message", json.RawMessage(`{"role":"user","text":"completed"}`), historyTS); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "conversations.json")
			reg, err := conversations.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			reg.Create(conversations.Conversation{ID: testConvID, ReadUpTo: held})
			if err := reg.Save(path); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 3; i++ {
				h = history.New(dir)
				reconcileStartupHistory(h, reg, discardLogger(), historyTS.Add(time.Duration(i)*time.Second))
				store := thread.NewStore(h)
				t.Cleanup(func() { _ = store.Shutdown() })
				if err := store.Load(t.Context(), testConvID); err != nil {
					t.Fatal(err)
				}
				testShadowWait(t, func() bool { return store.Status(testConvID).State == thread.StateUsable })
				latest := store.Status(testConvID).LastShownVersion
				reg, err = conversations.Load(path)
				if err != nil {
					t.Fatal(err)
				}
				cv, _ := reg.Get(testConvID)
				if latest != 1 || cv.ReadUpTo != held || (latest > cv.ReadUpTo) != (held == 0) {
					t.Fatalf("restart %d: latest=%d read=%d err=%v", i, latest, cv.ReadUpTo, err)
				}
				if err := store.Unload(testConvID); err != nil {
					t.Fatal(err)
				}
				if shown, ok := store.UnloadedLastShownVersion(testConvID); !ok || shown != 1 {
					t.Fatalf("restart certificate = %d/%t", shown, ok)
				}
			}
		})
	}
}

func TestUnreadThreadHiddenTails(t *testing.T) {
	cfg, h, store, shadow, _, _ := testThreadWatermark(t, false)
	path := filepath.Join(t.TempDir(), "conversations.json")
	w, _ := testDaemonThreadQueryWire(t, shadow, func(c *relay.V2SessionConfig) {
		c.Handlers = make(map[string]dispatch.Handler)
		c.Handlers[protocol.TypeListConversations] = handlers.ListConversationsWithAgents(shadow.registry, nil)
		c.Handlers[protocol.TypeMarkConversationRead] = handlers.MarkConversationRead(shadow.registry, c.ThreadLastShownVersion, path, nil, discardLogger())
	})
	w.open("phone", false, protocol.CapabilityThread)
	w.open("legacy", false)
	want := uint64(1)
	held := uint64(0)
	check := func(present bool) {
		t.Helper()
		shown, ok := cfg.ThreadLastShownVersion(testConvID)
		if ok != present || present && shown != want {
			t.Fatalf("shown=%d/%t want=%d/%t", shown, ok, want, present)
		}
		for _, peer := range []string{"phone", "legacy"} {
			testDaemonThreadRequest(t, w, peer, 70, protocol.TypeListConversations, struct{}{})
			gotPeer, env := w.read()
			var rows protocol.ConversationsPayload
			if json.Unmarshal(env.Payload, &rows) != nil || gotPeer != peer || env.Type != protocol.TypeConversations || len(rows.Conversations) != 1 {
				t.Fatalf("list reply: %s %+v", gotPeer, env)
			}
			row := rows.Conversations[0]
			if row.ReadUpTo != held || (row.LastShownVersion != nil) != present {
				t.Fatalf("route %s: %+v", peer, row)
			}
			if row.LastShownVersion != nil && (*row.LastShownVersion != want || (*row.LastShownVersion > row.ReadUpTo) != (want > held)) {
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

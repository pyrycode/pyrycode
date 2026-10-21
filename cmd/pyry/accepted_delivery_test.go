package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/msgqueue"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/thread"
)

func TestAcceptedDeliveryLiveRefusal(t *testing.T) {
	for _, separated := range []bool{false, true} {
		t.Run(map[bool]string{false: "coalesced", true: "separated_segment"}[separated], func(t *testing.T) {
			dir := t.TempDir()
			h := history.New(dir)
			sends := testSendHistory(h)
			m := msgqueue.QueuedMessage{ID: 7, MessageID: "app", DeviceID: "phone", Text: "accepted", TS: historyTS}
			sends.accepted(testConvID, m)
			var hold atomic.Bool
			entered, release := make(chan struct{}, 1), make(chan struct{})
			store := thread.NewStore(h, func(ctx context.Context, _ conversations.ConversationID) error {
				if hold.Load() {
					select {
					case entered <- struct{}{}:
					default:
					}
					select {
					case <-release:
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				return nil
			})
			t.Cleanup(func() {
				if err := store.Shutdown(); err != nil {
					t.Error(err)
				}
			})
			if err := store.Load(t.Context(), testConvID); err != nil {
				t.Fatal(err)
			}
			testShadowWait(t, func() bool { return store.Status(testConvID).Version == 1 })
			baseline, err := store.CatchUp(t.Context(), testConvID, "", 0, 20)
			if err != nil || len(baseline.Items) != 1 {
				t.Fatalf("accepted catch-up: %+v %v", baseline, err)
			}
			saved, _ := json.Marshal(baseline)
			var frames []protocol.Envelope
			peer, retired := "joined-phone", 0
			p := &daemonThreadPublisher{store: store, log: discardLogger(),
				active: func(context.Context, string) []relay.ActiveConn {
					return []relay.ActiveConn{{ConnID: peer, Interactive: true, Thread: true, View: relay.ThreadViewComplete}}
				},
				push: func(_ context.Context, target string, e protocol.Envelope) error {
					if target != peer {
						t.Error("wrong recipient")
					}
					frames = append(frames, e)
					return nil
				},
				retire: func(context.Context, string) error { retired++; return nil },
			}
			w := &shadowConversation{}
			p.publish(t.Context(), testConvID, w)
			if !separated {
				hold.Store(true)
			}
			padding := `{}`
			if separated {
				padding = `{"pad":"` + strings.Repeat("x", int(history.MaxSegmentBytes)-1024) + `"}`
			}
			if _, err := h.Append(testConvID, "padding", json.RawMessage(padding), historyTS); err != nil {
				t.Fatal(err)
			}
			if !separated {
				select {
				case <-entered:
				case <-time.After(10 * time.Second):
					t.Fatal("fold barrier")
				}
			}
			ref := sends.beforeDelivery(testConvID, m.ID)
			ref.mu.Lock()
			locked := true
			defer func() {
				if locked {
					ref.mu.Unlock()
				}
			}()
			m.Text = strings.Repeat("delivered ", 256)
			done := make(chan struct{})
			go func() {
				operatorMessageHistory(h, nil, nil, discardLogger(), sends, history.SessionProvenance{Kind: "codex", SessionID: "receiver"})(testConvID, m)
				close(done)
			}()
			testShadowWait(t, func() bool { id, _ := h.LatestEntryID(testConvID); return id == 3 })
			if separated {
				testShadowWait(t, func() bool { return store.Status(testConvID).Version == 3 })
				p.publish(t.Context(), testConvID, w)
				testAcceptedDeliveryReplay(t, dir, store.Snapshot(testConvID))
			}
			ref.mu.Unlock()
			locked = false
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("delivery did not finish")
			}
			refusal := json.RawMessage(`{"conversation_id":"` + testConvID + `","scope":"session","original_model":"haiku","fallback_model":"sonnet","banner":"Switch back"}`)
			if _, err := h.Append(testConvID, protocol.TypeModelRefusalFallback, refusal, historyTS); err != nil {
				t.Fatal(err)
			}
			if !separated {
				hold.Store(false)
				close(release)
			}
			testShadowWait(t, func() bool { return store.Status(testConvID).Version == 5 })
			o, err := store.Changes(t.Context(), testConvID, baseline.Epoch, baseline.Version)
			if err != nil {
				t.Fatal(err)
			}
			p.publish(t.Context(), testConvID, w)
			live, duplicate := 0, false
			for _, frame := range frames {
				if frame.Type == protocol.TypeThreadItemAdded {
					var added protocol.ThreadItemAddedPayload
					if err := json.Unmarshal(frame.Payload, &added); err != nil {
						t.Fatal(err)
					}
					if added.Item.ID == 3 {
						duplicate = true
					}
					if added.Item.Subtype == protocol.TypeModelRefusalFallback {
						live++
					}
				}
			}
			t.Logf("baseline_required=%v retired=%d live_refusals=%d duplicate_delivery=%v", o.BaselineRequired, retired, live, duplicate)
			if o.BaselineRequired || retired != 0 || live != 1 || duplicate {
				t.Fatal("accepted join lost the live refusal boundary")
			}
			current := store.Snapshot(testConvID)
			if len(current.Items) != 2 {
				t.Fatalf("public rows: %+v", current.Items)
			}
			item := current.Items[0]
			var content map[string]any
			_ = json.Unmarshal(item.Content, &content)
			if item.ID != 1 || item.Kind != "user_message" || item.Order != 3 || item.Rev != 4 || item.Status != "delivered" || item.Active || item.Session != "receiver" || item.Agent != "codex" || !item.Shown || content["device_id"] != "phone" || content["queued_msg_id"] != float64(7) {
				t.Fatalf("delivery ownership: %+v %v", item, content)
			}
			if after, _ := json.Marshal(baseline); string(after) != string(saved) {
				t.Fatal("retained catch-up mutated")
			}
			if separated {
				segments, _ := filepath.Glob(filepath.Join(dir, "conversations", testConvID, "history", "*.jsonl"))
				if len(segments) < 2 {
					t.Fatal("reader segment cut missing")
				}
			}
			testAcceptedDeliveryReplay(t, dir, current)
			peer = "replacement-phone"
			frames = nil
			p.publish(t.Context(), testConvID, w)
			if len(frames) != 0 {
				t.Fatal("historical refusal replayed live to replacement")
			}
		})
	}
}

func testAcceptedDeliveryReplay(t *testing.T, dir string, want thread.Snapshot) {
	t.Helper()
	// Reopen the durable log in an independent instance; two cache owners must
	// never share derived checkpoint files while the original worker is live.
	reopened := t.TempDir()
	logDir := filepath.Join("conversations", testConvID, "history")
	if err := os.MkdirAll(filepath.Join(reopened, logDir), 0700); err != nil {
		t.Fatal(err)
	}
	segments, err := filepath.Glob(filepath.Join(dir, logDir, "*.jsonl"))
	if err != nil || len(segments) == 0 {
		t.Fatalf("history segments: %v %v", segments, err)
	}
	for _, segment := range segments {
		raw, err := os.ReadFile(segment)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(reopened, logDir, filepath.Base(segment)), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	h := history.New(reopened)
	reader, err := h.Forward(testConvID, 0)
	if err != nil {
		t.Fatal(err)
	}
	f := thread.New(testConvID)
	if err := reader.Walk(t.Context(), want.Version, f.Feed); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.Items(), want.Items) {
		t.Fatal("inline replay differs from incremental source fold")
	}
	store := thread.NewStore(h)
	defer func() {
		if err := store.Shutdown(); err != nil {
			t.Error(err)
		}
	}()
	if err := store.Load(t.Context(), testConvID); err != nil {
		t.Fatal(err)
	}
	testShadowWait(t, func() bool { return store.Status(testConvID).Version == want.Version })
	if !reflect.DeepEqual(store.Snapshot(testConvID).Items, want.Items) {
		t.Fatal("reopened source fold differs")
	}
	catch, err := store.CatchUp(t.Context(), testConvID, "", 0, 20)
	if err != nil || catch.ResetRequired || len(catch.Items) != len(want.Items) {
		t.Fatalf("replacement catch-up: %+v %v", catch, err)
	}
	// Read the real segment bytes as well as the replayed rows.
	if _, err := os.Stat(filepath.Join(dir, "conversations", testConvID, "history")); err != nil {
		t.Fatal(err)
	}
}

func TestAcceptedDeliveryAppendFailures(t *testing.T) {
	for _, failed := range []string{"acceptance", "message", "outcome"} {
		t.Run(failed, func(t *testing.T) {
			h := history.New(t.TempDir())
			blocked := filepath.Join(t.TempDir(), "blocked")
			if err := os.WriteFile(blocked, []byte("file"), 0600); err != nil {
				t.Fatal(err)
			}
			bad := history.New(blocked)
			sends := testSendHistory(h)
			m := msgqueue.QueuedMessage{ID: 7, Text: "safe", TS: historyTS}
			if failed == "acceptance" {
				sends.store = bad
			}
			sends.accepted(testConvID, m)
			sends.store = h
			if failed == "outcome" {
				sends.store = bad
			}
			if failed == "message" {
				m.Text = strings.Repeat("x", int(history.MaxSegmentBytes)+1)
			}
			done := make(chan struct{})
			go func() { operatorMessageHistory(h, nil, nil, discardLogger(), sends)(testConvID, m); close(done) }()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("missing append companion stalled producer")
			}
			entries := historyEntries(t, h, testConvID)
			f := thread.New(testConvID)
			if err := f.Feed(entries); err != nil {
				t.Fatal(err)
			}
			items := f.Items()
			if len(items) != 1 {
				t.Fatalf("surviving facts: %+v", items)
			}
			var content map[string]json.RawMessage
			_ = json.Unmarshal(items[0].Content, &content)
			if content["outcome"] != nil {
				t.Fatal("failed append fabricated outcome")
			}
			switch failed {
			case "acceptance":
				if len(entries) != 1 || entries[0].Type != "message" || content["accepted_entry_id"] != nil {
					t.Fatal("failed acceptance fabricated reference")
				}
			case "message":
				if len(entries) != 1 || items[0].Status != "queued" || !items[0].Active {
					t.Fatal("failed message fabricated delivery")
				}
			case "outcome":
				if len(entries) != 2 || items[0].ID != 1 || items[0].Order != 2 || items[0].Status != "delivered" || items[0].Active {
					t.Fatal("committed delivery prefix lost identity")
				}
			}
		})
	}
}

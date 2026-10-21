package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/attachments"
	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/msgqueue"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/thread"
)

func TestDeliveredAttachmentWire(t *testing.T) {
	root := t.TempDir()
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{ID: testConvID})
	ids := []string{testPostID(t), testPostID(t)}
	data := [][]byte{[]byte("stored first\x00\xff"), []byte("stored second")}
	facts := []map[string]string{
		{"attachment_id": ids[0], "filename": "_.._.._private_report_.txt"},
		{"attachment_id": ids[1], "filename": "second.txt"},
	}
	var accepted protocol.ThreadItem
	var epoch string
	check := func(t *testing.T, item protocol.ThreadItem, status string, rev, order uint64) {
		t.Helper()
		testQueuedAttachmentContent(t, item, status, ids, facts)
		if item.ID != accepted.ID || item.Rev != rev || item.Order != order || item.Shown != accepted.Shown {
			t.Fatal("acceptance identity/placement changed", item)
		}
		var before, after map[string]json.RawMessage
		_ = json.Unmarshal(accepted.Content, &before)
		_ = json.Unmarshal(item.Content, &after)
		for _, key := range []string{"device_id", "message_id", "accepted_at", "client_sent_at", "queued_msg_id"} {
			if !reflect.DeepEqual(before[key], after[key]) {
				t.Fatalf("acceptance %s changed: %s -> %s", key, before[key], after[key])
			}
		}
		assertContentFree(t, string(item.Content), root, "forged.txt", "composed delivery prompt")
	}
	t.Run("delivery", func(t *testing.T) {
		var sends *queuedSendHistory
		var written []byte
		s, w, mgr, q, logs := testQueuedAttachmentWire(t, root, reg, nil, func(cfg *msgqueue.Config, h *queuedSendHistory) {
			sends = h
			cfg.OnTerminal = h.terminal
			cfg.SendNow = func(_ context.Context, _ string, _ uint64, payload []byte) error {
				written = append([]byte(nil), payload...)
				return nil
			}
		})
		for i, name := range []string{"../../private\nreport?.txt", "second.txt"} {
			testQueuedAttachmentUpload(t, w, uint64(i+1), ids[i], name, data[i])
		}
		store := s.store.(*thread.Store)
		testDaemonThreadAppend(t, s.history, "unknown", `{}`)
		baseline := testDaemonThreadLoad(t, store, 1)
		epoch = baseline.Epoch
		cursor := testDaemonThreadCursor(s)
		s.publisher.publish(t.Context(), testConvID, cursor)
		testCompactQuery(t, w, mgr, "phone", 10, protocol.TypeThreadCatchUp, protocol.ThreadCatchUpPayload{ConversationID: testConvID, Projection: protocol.ThreadProjectionCompact, Limit: 10})
		testDaemonWireRequest(t, w, 3, protocol.TypeSendMessage, protocol.SendMessagePayload{
			ConversationID: testConvID, Text: "safe accepted text", MessageID: "attached", AttachmentIDs: ids,
		})
		testDaemonWireReading(t, w, protocol.TypeAck, 3)
		testDaemonThreadLoad(t, store, 2)
		s.publisher.publish(t.Context(), testConvID, cursor)
		got, _, _ := testCompactQuery(t, w, mgr, "phone", 11, protocol.TypeThreadCatchUp, protocol.ThreadCatchUpPayload{ConversationID: testConvID, Epoch: epoch, Version: 1, Projection: protocol.ThreadProjectionCompact, Limit: 10})
		if len(got.Items) != 1 || len(got.Headers) != 0 {
			t.Fatal("acceptance is not a full root", got)
		}
		accepted = got.Items[0]
		check(t, accepted, "queued", 2, 0)
		queued := q.Snapshot(testConvID)
		if len(queued) != 1 || !q.SendNow(testConvID, queued[0].ID) || !bytes.Contains(written, []byte("safe accepted text")) {
			t.Fatal("fake child did not receive queued input")
		}
		testDaemonThreadLoad(t, store, 3)
		s.publisher.publish(t.Context(), testConvID, cursor)
		got, _, _ = testCompactQuery(t, w, mgr, "phone", 12, protocol.TypeThreadCatchUp, protocol.ThreadCatchUpPayload{ConversationID: testConvID, Epoch: epoch, Version: 2, Projection: protocol.ThreadProjectionCompact, Limit: 10})
		if len(got.Items) != 1 {
			t.Fatal("written acceptance missing", got)
		}
		check(t, got.Items[0], "written", 3, 0)
		raw, err := json.Marshal(map[string]any{"role": "user", "text": "safe accepted text", "attachment_ids": ids, "attachments": []map[string]string{{"attachment_id": ids[0], "filename": "forged.txt"}}})
		if err != nil {
			t.Fatal(err)
		}
		// Load the linked pair together: an independently published legacy message
		// requires a reset when later claimed, which is a different contract.
		func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			if err := store.Unload(testConvID); err != nil {
				t.Fatal(err)
			}
			deliveryID := testDaemonThreadAppend(t, s.history, "message", string(raw))
			sends.delivered(testConvID, queued[0].ID, sends.beforeDelivery(testConvID, queued[0].ID), &deliveryID, time.Now(), history.SessionProvenance{})
			testDaemonThreadLoad(t, store, 5)
		}()
		s.publisher.publish(t.Context(), testConvID, cursor)
		replaced := false
		for _, f := range testCompactRead(t, w, mgr, "phone") {
			if f.env.Type != protocol.TypeThreadCompactUpdate {
				continue
			}
			var p protocol.ThreadCompactUpdatePayload
			if json.Unmarshal(f.env.Payload, &p) != nil || p.Item == nil || p.Header != nil {
				t.Fatal("delivery replacement is not a full root", f.env)
			}
			check(t, *p.Item, "delivered", 5, 4)
			replaced = true
		}
		if !replaced {
			t.Fatal("compact delivery replacement missing")
		}
		assertContentFree(t, string(logs.BytesCopy()), root, "forged.txt", "second.txt")
	})
	// Retrieval follows current storage metadata; thread names retain acceptance.
	for i, id := range ids {
		path, err := attachments.ResolvePath(root, testConvID, id)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(path, filepath.Join(filepath.Dir(path), fmt.Sprintf("renamed-%d.txt", i))); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("reopened", func(t *testing.T) {
		s, w, mgr, _, _ := testQueuedAttachmentWire(t, root, reg, nil)
		baseline := testDaemonThreadLoad(t, s.store.(*thread.Store), 5)
		if baseline.Epoch != epoch {
			t.Fatal("clean reopening changed epoch")
		}
		s.publisher.publish(t.Context(), testConvID, testDaemonThreadCursor(s))
		for i, projection := range []string{"", protocol.ThreadProjectionCompact} {
			got, _, _ := testCompactQuery(t, w, mgr, "phone", uint64(20+i*2), protocol.TypeThreadCatchUp, protocol.ThreadCatchUpPayload{ConversationID: testConvID, Epoch: epoch, Projection: projection, Limit: 10})
			if len(got.Items) != 1 || len(got.Headers) != 0 {
				t.Fatal("reopened catch-up lost full root", got)
			}
			check(t, got.Items[0], "delivered", 5, 4)
			got, _, _ = testCompactQuery(t, w, mgr, "phone", uint64(21+i*2), protocol.TypeThreadPage, protocol.ThreadPagePayload{ConversationID: testConvID, Epoch: epoch, Projection: projection, BeforeOrder: 6, Limit: 10})
			if len(got.Items) != 1 || len(got.Headers) != 0 {
				t.Fatal("reopened page lost full root", got)
			}
			check(t, got.Items[0], "delivered", 5, 4)
		}
		for i, id := range ids {
			testDaemonWireRequest(t, w, uint64(30+i), protocol.TypeRequestAttachment, protocol.RequestAttachmentPayload{ConversationID: testConvID, AttachmentID: id})
			e := testDaemonWireReading(t, w, protocol.TypeAttachmentChunk, 0)
			var chunk protocol.AttachmentChunkPayload
			if json.Unmarshal(e.Payload, &chunk) != nil || !bytes.Equal(chunk.Data, data[i]) || chunk.AttachmentID != id || chunk.Filename != fmt.Sprintf("renamed-%d.txt", i) {
				t.Fatal("reopened attachment bytes differ", chunk)
			}
		}
	})
}

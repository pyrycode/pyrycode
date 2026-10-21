package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/attachments"
	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/msgqueue"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/relay/handlers"
	"github.com/pyrycode/pyrycode/internal/thread"
)

// The real queue remains unstarted so no delivery can replace the acceptance.
// Routing uses the same fake child boundary as the compact transport tests.
func testQueuedAttachmentWire(t *testing.T, root string, reg *conversations.Registry, beforeAccepted func()) (*threadShadow, *switchWire, *relay.V2SessionManager, *msgqueue.Queue, *lockedBuffer) {
	t.Helper()
	logs := &lockedBuffer{}
	logger := slog.New(slog.NewTextHandler(logs, nil))
	h := history.New(root)
	s := newThreadShadow(h, reg, thread.NewStore(h), logger)
	t.Cleanup(func() { _ = s.shutdown() })
	sends := testSendHistory(h)
	sends.logger = logger
	sends.attachmentName = storedQueuedAttachmentName(root, reg)
	q, err := msgqueue.New(msgqueue.Config{
		Logger: logger,
		Deliver: func(context.Context, string, []byte) error {
			t.Error("held delivery ran")
			return nil
		},
		OnAccepted: func(c string, m msgqueue.QueuedMessage) {
			if beforeAccepted != nil {
				beforeAccepted()
			}
			sends.accepted(c, m)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	resolve := func(c, id string) (string, bool) {
		if _, ok := reg.Get(conversations.ConversationID(c)); !ok {
			return "", false
		}
		path, err := attachments.ResolvePath(root, conversations.ConversationID(c), id)
		return path, err == nil
	}
	w, mgr := testDaemonThreadQueryWire(t, s, func(c *relay.V2SessionConfig) {
		c.Logger = logger
		c.AttachmentIntake = attachments.NewIntake(root)
		c.AttachmentResolve = resolve
		c.Handlers = map[string]dispatch.Handler{protocol.TypeSendMessage: handlers.SendMessage(testCompactSend{}, q, resolve, nil, "", nil, nil, logger)}
	})
	w.open("phone", false, protocol.CapabilityThread, protocol.CapabilityThreadExpansion, protocol.CapabilityThreadCompact)
	return s, w, mgr, q, logs
}

func testQueuedAttachmentUpload(t *testing.T, w *switchWire, request uint64, id, filename string, data []byte) {
	t.Helper()
	testDaemonWireRequest(t, w, request, protocol.TypeAttachmentChunk, protocol.AttachmentChunkPayload{
		ConversationID: testConvID, AttachmentID: id, Filename: filename,
		TotalChunks: 1, Size: int64(len(data)), SHA256: fmt.Sprintf("%x", sha256.Sum256(data)), Data: data,
	})
	testDaemonWireReading(t, w, protocol.TypeAttachmentStored, request)
}

func testQueuedAttachmentContent(t *testing.T, item protocol.ThreadItem, status string, ids []string, facts []map[string]string) {
	t.Helper()
	var content struct {
		Text  string              `json:"text"`
		IDs   []string            `json:"attachment_ids"`
		Facts []map[string]string `json:"attachments"`
	}
	if err := json.Unmarshal(item.Content, &content); err != nil {
		t.Fatal(err)
	}
	if item.Kind != "user_message" || item.Parent != 0 || item.Status != status || item.Active != (status == "queued") ||
		content.Text != "safe accepted text" || !reflect.DeepEqual(content.IDs, ids) || !reflect.DeepEqual(content.Facts, facts) {
		t.Fatalf("queued attachment content: kind=%s status=%s active=%t text=%q IDs=%v facts=%v; want IDs=%v facts=%v", item.Kind, item.Status, item.Active, content.Text, content.IDs, content.Facts, ids, facts)
	}
}

func TestQueuedAttachmentWire(t *testing.T) {
	root := t.TempDir()
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{ID: testConvID})
	ids := []string{"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}
	data := []byte("real stored attachment bytes\x00\xff")
	const hostile = "../../private\nreport?.txt"
	want := []map[string]string{
		{"attachment_id": ids[0], "filename": "_.._.._private_report_.txt"},
		{"attachment_id": ids[1], "filename": "second.txt"},
	}
	t.Run("acceptance", func(t *testing.T) {
		s, w, mgr, q, logs := testQueuedAttachmentWire(t, root, reg, nil)
		testQueuedAttachmentUpload(t, w, 1, ids[0], hostile, data)
		testQueuedAttachmentUpload(t, w, 2, ids[1], "second.txt", []byte("second"))
		store := s.store.(*thread.Store)
		testDaemonThreadAppend(t, s.history, "unknown", `{}`)
		baseline := testDaemonThreadLoad(t, store, 1)
		cursor := testDaemonThreadCursor(s)
		s.publisher.publish(t.Context(), testConvID, cursor)
		testCompactQuery(t, w, mgr, "phone", 10, protocol.TypeThreadCatchUp, protocol.ThreadCatchUpPayload{ConversationID: testConvID, Projection: protocol.ThreadProjectionCompact, Limit: 10})
		testDaemonWireRequest(t, w, 3, protocol.TypeSendMessage, map[string]any{
			"conversation_id": testConvID, "text": "safe accepted text", "message_id": "attached",
			"attachment_ids": []string{ids[0], ids[1], ids[0]},
			"attachments":    []map[string]string{{"attachment_id": ids[0], "filename": "forged.txt"}},
		})
		testDaemonWireReading(t, w, protocol.TypeAck, 3)
		if got := q.Snapshot(testConvID); len(got) != 1 {
			t.Fatalf("accepted queue: %+v", got)
		}
		testDaemonThreadLoad(t, store, 2)
		s.publisher.publish(t.Context(), testConvID, cursor)
		published := false
		for _, f := range testCompactRead(t, w, mgr, "phone") {
			if f.env.Type == protocol.TypeThreadCompactUpdate {
				var p protocol.ThreadCompactUpdatePayload
				if json.Unmarshal(f.env.Payload, &p) != nil || p.Item == nil || p.Header != nil {
					t.Fatal("queued root was not full", f.env)
				}
				testQueuedAttachmentContent(t, *p.Item, "queued", ids, want)
				published = true
			}
		}
		if !published {
			t.Fatalf("no initial queued publication: cursor=%+v peers=%+v logs=%s", cursor, mgr.ActiveThreadConns(t.Context(), testConvID), logs.BytesCopy())
		}
		got, _, _ := testCompactQuery(t, w, mgr, "phone", 4, protocol.TypeThreadCatchUp, protocol.ThreadCatchUpPayload{ConversationID: testConvID, Epoch: baseline.Epoch, Version: 1, Projection: protocol.ThreadProjectionCompact, Limit: 10})
		if len(got.Items) != 1 || len(got.Headers) != 0 {
			t.Fatal("compact acceptance missing full root", got)
		}
		testQueuedAttachmentContent(t, got.Items[0], "queued", ids, want)
		assertContentFree(t, string(logs.BytesCopy()), root, hostile, want[0]["filename"], "second.txt", "forged.txt")
	})

	// Change stored metadata after acceptance: fresh content must use durable facts.
	path, err := attachments.ResolvePath(root, testConvID, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, filepath.Join(filepath.Dir(path), "renamed.txt")); err != nil {
		t.Fatal(err)
	}
	t.Run("reopened", func(t *testing.T) {
		s, w, _, _, _ := testQueuedAttachmentWire(t, root, reg, nil)
		pending, err := readStartupSends(s.history, testConvID)
		if err != nil {
			t.Fatal(err)
		}
		closeStartupSends(s.history, discardLogger(), testConvID, pending, time.Now())
		baseline := testDaemonThreadLoad(t, s.store.(*thread.Store), 3)
		s.publisher.publish(t.Context(), testConvID, testDaemonThreadCursor(s))
		testDaemonThreadRequest(t, w, "phone", 5, protocol.TypeThreadCatchUp, protocol.ThreadCatchUpPayload{ConversationID: testConvID, Epoch: baseline.Epoch, Version: 2, Limit: 10})
		got, typ := testDaemonThreadReply(t, w, 5)
		if typ != protocol.TypeThreadItems {
			t.Fatal("reopened catch-up refused", typ)
		}
		if len(got.Items) != 1 {
			t.Fatal("reopened acceptance missing", got)
		}
		testQueuedAttachmentContent(t, got.Items[0], "lost", ids, want)
		testDaemonWireRequest(t, w, 6, protocol.TypeRequestAttachment, protocol.RequestAttachmentPayload{ConversationID: testConvID, AttachmentID: ids[0]})
		e := testDaemonWireReading(t, w, protocol.TypeAttachmentChunk, 0)
		var chunk protocol.AttachmentChunkPayload
		if json.Unmarshal(e.Payload, &chunk) != nil || !bytes.Equal(chunk.Data, data) || chunk.AttachmentID != ids[0] || chunk.Filename != "renamed.txt" {
			t.Fatal("retrieved bytes or stored name differ", chunk)
		}
	})
}

func TestQueuedAttachmentFacts(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	reg := &conversations.Registry{}
	other := conversations.ConversationID(testPostID(t))
	unregistered := conversations.ConversationID(testPostID(t))
	id := testPostID(t)
	for i, c := range []conversations.ConversationID{testConvID, other, unregistered} {
		if c != unregistered {
			reg.Create(conversations.Conversation{ID: c})
		}
		dir, err := attachments.EnsureDir(root, c, id)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := attachments.Store(dir, fmt.Sprintf("stored-%d.txt", i), []byte("bytes")); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name     string
		conv     conversations.ConversationID
		ids      []string
		lookup   bool
		filename string
	}{
		{"first conversation", testConvID, []string{id}, true, "stored-0.txt"},
		{"duplicate projected IDs", testConvID, []string{id, id}, true, "stored-0.txt"},
		{"same ID other conversation", other, []string{id}, true, "stored-1.txt"},
		{"unregistered stored conversation", unregistered, []string{id}, true, ""},
		{"legacy no resolver", testConvID, []string{id}, false, ""},
		{"no IDs", testConvID, nil, true, ""},
		{"unavailable metadata", testConvID, []string{testPostID(t)}, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := history.New(t.TempDir())
			var logs bytes.Buffer
			h := newQueuedSendHistory(store, bufLogger(&logs), nil)
			if tc.lookup {
				h.attachmentName = storedQueuedAttachmentName(root, reg)
			}
			h.accepted(string(tc.conv), msgqueue.QueuedMessage{ID: 1, Text: "safe accepted text", AttachmentIDs: tc.ids, TS: time.Now()})
			p, err := store.Page(tc.conv, "", 10)
			if err != nil || len(p.Entries) != 1 {
				t.Fatal("acceptance missing", p, err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(p.Entries[0].Payload, &fields); err != nil {
				t.Fatal(err)
			}
			var facts []map[string]string
			if raw, ok := fields["attachments"]; ok {
				if tc.filename == "" || json.Unmarshal(raw, &facts) != nil || !reflect.DeepEqual(facts, []map[string]string{{"attachment_id": id, "filename": tc.filename}}) {
					t.Fatal("unexpected stored facts", string(raw))
				}
			} else if tc.filename != "" {
				t.Fatal("stored facts missing")
			}
			assertContentFree(t, logs.String(), root, "stored-0.txt", "stored-1.txt", "stored-2.txt")
		})
	}
}

func TestQueuedAttachmentUnavailableAfterValidation(t *testing.T) {
	root := t.TempDir()
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{ID: testConvID})
	ids := []string{testPostID(t), testPostID(t)}
	s, w, mgr, _, logs := testQueuedAttachmentWire(t, root, reg, func() {
		path, err := attachments.ResolvePath(root, testConvID, ids[0])
		if err != nil {
			t.Error(err)
			return
		}
		if err := os.Remove(path); err != nil {
			t.Error(err)
		}
	})
	for i, id := range ids {
		testQueuedAttachmentUpload(t, w, uint64(i+1), id, "private.txt", []byte("stored"))
	}
	testDaemonWireRequest(t, w, 3, protocol.TypeSendMessage, protocol.SendMessagePayload{ConversationID: testConvID, Text: "safe accepted text", AttachmentIDs: ids})
	testDaemonWireReading(t, w, protocol.TypeAck, 3)
	testDaemonThreadLoad(t, s.store.(*thread.Store), 1)
	got, _, _ := testCompactQuery(t, w, mgr, "phone", 4, protocol.TypeThreadCatchUp, protocol.ThreadCatchUpPayload{ConversationID: testConvID, Projection: protocol.ThreadProjectionCompact, Limit: 10})
	if len(got.Items) != 1 {
		t.Fatal("validated acceptance lost", got)
	}
	testQueuedAttachmentContent(t, got.Items[0], "queued", ids, []map[string]string{{"attachment_id": ids[1], "filename": "private.txt"}})
	assertContentFree(t, string(logs.BytesCopy()), root, "private.txt")
}

func TestQueuedAttachmentRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		refs any
		code string
	}{
		{"missing", []string{testPostID(t)}, protocol.CodeAttachmentNotFound},
		{"invalid ID", []string{"../../private.txt"}, protocol.CodeAttachmentNotFound},
		{"empty ID", []string{""}, protocol.CodeAttachmentNotFound},
		{"object", map[string]string{"filename": "private.txt"}, protocol.CodeProtocolMalformed},
		{"numeric element", []int{1}, protocol.CodeProtocolMalformed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			reg := &conversations.Registry{}
			reg.Create(conversations.Conversation{ID: testConvID})
			s, w, _, q, logs := testQueuedAttachmentWire(t, root, reg, nil)
			valid := testPostID(t)
			testQueuedAttachmentUpload(t, w, 1, valid, "private.txt", []byte("valid bytes"))
			refs := tc.refs
			if ids, ok := refs.([]string); ok {
				refs = append([]string{valid}, ids...)
			}
			testDaemonWireRequest(t, w, 2, protocol.TypeSendMessage, map[string]any{"conversation_id": testConvID, "text": "safe accepted text", "attachment_ids": refs})
			e := testDaemonWireReading(t, w, protocol.TypeError, 2)
			var p protocol.ErrorPayload
			if json.Unmarshal(e.Payload, &p) != nil || p.Code != tc.code || len(q.Snapshot(testConvID)) != 0 {
				t.Fatal("send refusal", e)
			}
			assertContentFree(t, string(e.Payload), root, "private.txt")
			assertContentFree(t, string(logs.BytesCopy()), root, "private.txt")
			if latest, err := s.history.LatestEntryID(testConvID); err != nil || latest != 0 {
				t.Fatal("rejected send recorded", latest, err)
			}
		})
	}
}

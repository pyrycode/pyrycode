package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/msgqueue"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// The #2038 attachment shape end to end: what the operator typed versus the
// prompt the daemon composed for claude, which names an on-host path. opHostPath
// is the needle — it exists ONLY in the delivered payload, and finding it
// anywhere in a durable entry is the leak #2116 would then serve to every paired
// device.
const (
	opText     = "does this look right to you"
	opHostPath = "/Users/operator/.pyry/instances/default/attachments/4c1/diagram.png"
	opDelivery = "The user attached a file at " + opHostPath + "\n\n" + opText
	opMsgID    = "desktop-echo-42"
)

// opQueue wires a real msgqueue.Queue to the real producer against store, with a
// deliver the test controls. done closes once the producer has run, so a test
// reads the log without polling it. The wrapper only sequences — the value under
// test is still newOperatorMessageHistory's closure.
func opQueue(t *testing.T, store *history.Store, buf *bytes.Buffer, deliver msgqueue.DeliverFunc) (*msgqueue.Queue, chan struct{}) {
	t.Helper()
	producer := newOperatorMessageHistory(store, nil, bufLogger(buf))
	done := make(chan struct{})
	q, err := msgqueue.New(msgqueue.Config{
		Deliver:       deliver,
		RetryInterval: time.Millisecond,
		OnDelivered: func(convID string, msg msgqueue.QueuedMessage) {
			producer(convID, msg)
			close(done)
		},
	})
	if err != nil {
		t.Fatalf("msgqueue.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = q.Run(ctx) }()
	return q, done
}

// waitAppended blocks until the producer has run, so the log read that follows
// is not racing the drain goroutine.
func waitAppended(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the history append")
	}
}

// onlyEntry reads convID's log and fails unless it holds exactly one entry.
func onlyEntry(t *testing.T, store *history.Store, convID string) history.Entry {
	t.Helper()
	page, err := store.Page(conversations.ConversationID(convID), "", 10)
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	if len(page.Entries) != 1 {
		t.Fatalf("log holds %d entries, want exactly 1", len(page.Entries))
	}
	return page.Entries[0]
}

// AC 1, AC 2 and AC 5: a confirmed delivery appends one `message` envelope with
// role `user`, carrying the client's own message id and the text the operator
// typed — never the composed payload that reached claude's stdin.
func TestOperatorMessageHistory_AppendsUserMessageWithQueuedText(t *testing.T) {
	t.Parallel()
	store := history.New(t.TempDir())
	var buf bytes.Buffer
	q, done := opQueue(t, store, &buf, func(context.Context, string, []byte) error { return nil })

	q.EnqueueDelivery(testConvID, opMsgID, opText, opDelivery)
	waitAppended(t, done)

	entry := onlyEntry(t, store, testConvID)
	if entry.Type != protocol.TypeMessage {
		t.Errorf("entry type = %q, want %q", entry.Type, protocol.TypeMessage)
	}
	var payload protocol.MessagePayload
	if err := json.Unmarshal(entry.Payload, &payload); err != nil {
		t.Fatalf("decode entry payload: %v", err)
	}
	if payload.Role != "user" {
		t.Errorf("role = %q, want %q — a served page must need no second decode arm", payload.Role, "user")
	}
	if payload.ConversationID != testConvID {
		t.Errorf("conversation_id = %q, want %q", payload.ConversationID, testConvID)
	}
	if payload.MessageID != opMsgID {
		t.Errorf("message_id = %q, want the client's own id %q", payload.MessageID, opMsgID)
	}
	if payload.Text != opText {
		t.Errorf("text = %q, want the queued text %q", payload.Text, opText)
	}

	// The leak check runs over the RAW entry bytes, not the decoded Text, so a
	// host path landing in any OTHER field is caught too. The opText probe is the
	// non-vacuity control: it proves this search actually finds what is present,
	// so the opHostPath miss below means absence rather than a broken needle.
	raw := string(entry.Payload)
	if !strings.Contains(raw, opText) {
		t.Fatalf("entry %q does not contain the queued text; the leak check below would be vacuous", raw)
	}
	if strings.Contains(raw, opHostPath) {
		t.Errorf("entry leaked the on-host path %q: %s", opHostPath, raw)
	}
	// #2596 AC 2: a message that named no attachments omits the key entirely.
	// #2704 AC 4: so does a message with no sender and no tap time, which is what
	// keeps an older client's entry byte-identical to one stored before the keys.
	for _, key := range []string{`"attachment_ids"`, `"device_name"`, `"client_version"`, `"client_sent_at"`} {
		if strings.Contains(raw, key) {
			t.Errorf("entry for a message with no value carries a %s key: %s", key, raw)
		}
	}

	// An empty log buffer means no Warn fired, so the append succeeded — and it
	// is also the never-log check: neither the text nor the message id may reach
	// any log line, at any level this handler passes.
	if buf.Len() != 0 {
		t.Errorf("producer logged %q; want silence on the success path", buf.String())
	}
}

// #2596 AC 1 and AC 2: a message whose attachment ids resolved is stored as one
// user `message` entry listing those ids, once each in the order the handler
// passed them (it has already deduplicated on first occurrence), and the entry
// carries none of the delivery prompt — neither the on-host path nor the
// daemon-authored text around it.
func TestOperatorMessageHistory_StoresAttachmentIDs(t *testing.T) {
	t.Parallel()
	store := history.New(t.TempDir())
	var buf bytes.Buffer
	q, done := opQueue(t, store, &buf, func(context.Context, string, []byte) error { return nil })

	ids := []string{"att-b-0002", "att-a-0001"}
	q.EnqueueAttached(testConvID, opMsgID, opText, opDelivery, ids)
	waitAppended(t, done)

	entry := onlyEntry(t, store, testConvID)
	if entry.Type != protocol.TypeMessage {
		t.Errorf("entry type = %q, want %q", entry.Type, protocol.TypeMessage)
	}
	var payload protocol.MessagePayload
	if err := json.Unmarshal(entry.Payload, &payload); err != nil {
		t.Fatalf("decode entry payload: %v", err)
	}
	if payload.Role != "user" {
		t.Errorf("role = %q, want %q", payload.Role, "user")
	}
	if payload.Text != opText {
		t.Errorf("text = %q, want the queued text %q", payload.Text, opText)
	}
	if !slices.Equal(payload.AttachmentIDs, ids) {
		t.Errorf("attachment_ids = %q, want %q in the order passed", payload.AttachmentIDs, ids)
	}

	// The raw-bytes probe, as in the attachment-less test: the ids' presence is
	// the non-vacuity control for the two absences below.
	raw := string(entry.Payload)
	if !strings.Contains(raw, `"attachment_ids":["att-b-0002","att-a-0001"]`) {
		t.Fatalf("entry %q does not carry the ids under attachment_ids", raw)
	}
	if strings.Contains(raw, opHostPath) {
		t.Errorf("entry leaked the on-host path %q: %s", opHostPath, raw)
	}
	if strings.Contains(raw, "The user attached a file") {
		t.Errorf("entry carries the daemon-composed delivery prompt: %s", raw)
	}
	if buf.Len() != 0 {
		t.Errorf("producer logged %q; want silence on the success path", buf.String())
	}
}

// #2704 AC 2 and AC 3: the stored user entry names the device and app version
// that sent it and the client's tap time, the last re-formatted by the daemon
// as UTC RFC 3339 from the time the handler parsed. The tap time stays the
// client's — the entry is still stamped at confirmation, not by it.
func TestOperatorMessageHistory_StoresSender(t *testing.T) {
	t.Parallel()
	store := history.New(t.TempDir())
	var buf bytes.Buffer
	q, done := opQueue(t, store, &buf, func(context.Context, string, []byte) error { return nil })

	sentAt := time.Date(2020, 1, 2, 3, 4, 5, 120000000, time.FixedZone("EET", 2*60*60))
	q.EnqueueSent(testConvID, opMsgID, opText, opText, nil, "desktop", "pyrycode-desktop/0.9.1", sentAt)
	waitAppended(t, done)

	entry := onlyEntry(t, store, testConvID)
	var payload protocol.MessagePayload
	if err := json.Unmarshal(entry.Payload, &payload); err != nil {
		t.Fatalf("decode entry payload: %v", err)
	}
	if payload.DeviceName != "desktop" {
		t.Errorf("device_name = %q, want %q", payload.DeviceName, "desktop")
	}
	if payload.ClientVersion != "pyrycode-desktop/0.9.1" {
		t.Errorf("client_version = %q, want %q", payload.ClientVersion, "pyrycode-desktop/0.9.1")
	}
	if want := "2020-01-02T01:04:05.12Z"; payload.ClientSentAt != want {
		t.Errorf("client_sent_at = %q, want %q (UTC RFC 3339)", payload.ClientSentAt, want)
	}
	if entry.TS.Year() == 2020 {
		t.Errorf("entry stamped %v from the client's tap time; want the confirmation time", entry.TS)
	}
	if buf.Len() != 0 {
		t.Errorf("producer logged %q; want silence on the success path", buf.String())
	}
}

// The plan's timestamp decision: the entry is stamped when the write is
// CONFIRMED, not when the message was enqueued. A message can sit in the backlog
// for a long time, and an enqueue stamp would sort it behind entries carrying
// later times, so a served page would read out of order.
//
// This cannot flake on clock resolution: the delivery is held until the test
// releases it, so the confirmation is causally after the enqueue timestamp was
// read, with a channel round-trip, a mutex hand-off and a marshal in between.
func TestOperatorMessageHistory_StampsAtConfirmationNotEnqueue(t *testing.T) {
	t.Parallel()
	store := history.New(t.TempDir())
	var buf bytes.Buffer
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	q, done := opQueue(t, store, &buf, func(context.Context, string, []byte) error {
		entered <- struct{}{}
		<-release
		return nil
	})

	q.EnqueueDelivery(testConvID, opMsgID, opText, opDelivery)
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the delivery to start")
	}
	queued := q.Snapshot(testConvID)
	if len(queued) != 1 {
		t.Fatalf("backlog holds %d items, want the in-flight head", len(queued))
	}
	enqueuedAt := queued[0].TS
	close(release)
	waitAppended(t, done)

	if entry := onlyEntry(t, store, testConvID); !entry.TS.After(enqueuedAt) {
		t.Errorf("entry stamped %s, want strictly after the enqueue time %s", entry.TS, enqueuedAt)
	}
}

// #2730: a send-now message is recorded when claude's echo of the
// DELIVERY payload arrives, not at OnDelivered, and its content is still built
// from msg.Text. An ordinary message on the same producer commits at once.
func TestOperatorMessageHistory_SendNowCommitsAtTheEcho(t *testing.T) {
	t.Parallel()
	store := history.New(t.TempDir())
	var buf bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	place := newSendNowPlacement(ctx,
		func(sid string) (string, bool) { return testConvID, sid == "s1" },
		func(ctx context.Context, _ string) error { <-ctx.Done(); return ctx.Err() })
	producer := newOperatorMessageHistory(store, place, bufLogger(&buf))

	place.expect(testConvID, 4, []byte(opDelivery))
	producer(testConvID, msgqueue.QueuedMessage{ID: 4, MessageID: opMsgID, Text: opText, SentNow: true})
	if page, err := store.Page(conversations.ConversationID(testConvID), "", 10); err != nil || len(page.Entries) != 0 {
		t.Fatalf("before the echo: entries=%d err=%v, want nothing committed", len(page.Entries), err)
	}

	place.echo("s1", turnevent.UserEcho{TextSHA256: sha256.Sum256([]byte(opDelivery))})
	entry := onlyEntry(t, store, testConvID)
	var payload protocol.MessagePayload
	if err := json.Unmarshal(entry.Payload, &payload); err != nil || payload.Text != opText {
		t.Fatalf("entry text = %q (err %v), want the queued text %q", payload.Text, err, opText)
	}
	if strings.Contains(string(entry.Payload), opHostPath) {
		t.Errorf("entry leaked the on-host path: %s", entry.Payload)
	}

	var delivered map[string]any
	if err := json.Unmarshal(entry.Payload, &delivered); err != nil || delivered["sent_now"] != true {
		t.Fatalf("send-now source must identify its delivery kind: sent_now=%v err=%v", delivered["sent_now"], err)
	}
	if delivered["queued_msg_id"] != float64(4) {
		t.Fatalf("send-now queued_msg_id = %v, want integer 4", delivered["queued_msg_id"])
	}

	producer(testConvID, msgqueue.QueuedMessage{ID: 5, MessageID: opMsgID, Text: "plain"})
	var ordinary map[string]any
	if err := json.Unmarshal(historyEntries(t, store, testConvID)[1].Payload, &ordinary); err != nil {
		t.Fatal(err)
	}
	if ordinary["message_id"] != delivered["message_id"] || ordinary["queued_msg_id"] != float64(5) {
		t.Fatalf("same message_id must retain distinct queue IDs: send-now=%v ordinary=%v", delivered, ordinary)
	}
	page, err := store.Page(conversations.ConversationID(testConvID), "", 10)
	if err != nil || len(page.Entries) != 2 {
		t.Fatalf("stored entries = %d, want 2; err=%v", len(page.Entries), err)
	}
}

// Mobile 1642 must distinguish a delayed normal confirmation from peer Send now.
func TestOperatorMessageHistory_OrdinaryDeliveryDoesNotClaimSendNow(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	store := history.New(t.TempDir())
	producer := newOperatorMessageHistory(store, nil, bufLogger(&buf))
	producer(testConvID, msgqueue.QueuedMessage{MessageID: "ordinary", Text: "plain"})
	var payload map[string]any
	if err := json.Unmarshal(onlyEntry(t, store, testConvID).Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if _, present := payload["sent_now"]; present {
		t.Fatal("ordinary delivery must omit sent_now")
	}
	if _, present := payload["queued_msg_id"]; present {
		t.Fatal("delivery without a queue entry must omit queued_msg_id")
	}
}

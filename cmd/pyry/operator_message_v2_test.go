package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/eventring"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
)

// sampleOperatorMessage is one confirmed turn as newOperatorMessageHistory hands
// it over: the payload marshalled once and the stamp the log entry carries.
func sampleOperatorMessage(t *testing.T) operatorMessage {
	t.Helper()
	payload, err := json.Marshal(protocol.MessagePayload{
		ConversationID: testConvID,
		MessageID:      opMsgID,
		Role:           "user",
		Text:           opText,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return operatorMessage{convID: testConvID, payload: payload, ts: time.Now().UTC()}
}

// AC 1 and AC 3: one push per interactive conn, none to a non-interactive one,
// carrying the handed-over bytes and stamp verbatim and the ring id the event
// was appended under.
func TestOperatorMessageEmitterV2_Broadcast_InteractiveOnlyWithEventID(t *testing.T) {
	t.Parallel()
	ring := eventring.New(16)
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{
		{ConnID: "desk", Interactive: true},
		{ConnID: "v1-phone", Interactive: false},
	}}}
	e := newOperatorMessageEmitterV2(nil, discardLogger())
	m := sampleOperatorMessage(t)

	e.broadcast(context.Background(), bcast, ring, m)

	if len(bcast.pushes) != 1 {
		t.Fatalf("pushes = %d, want 1 (interactive conns only)", len(bcast.pushes))
	}
	got := bcast.pushes[0]
	if got.connID != "desk" {
		t.Errorf("pushed to %q, want the interactive conn", got.connID)
	}
	if got.env.Type != protocol.TypeMessage {
		t.Errorf("type = %q, want %q", got.env.Type, protocol.TypeMessage)
	}
	if !bytes.Equal(got.env.Payload, m.payload) {
		t.Errorf("payload = %s, want the handed-over bytes %s", got.env.Payload, m.payload)
	}
	if !got.env.TS.Equal(m.ts) {
		t.Errorf("ts = %v, want the log entry's stamp %v", got.env.TS, m.ts)
	}
	if got.env.EventID == nil {
		t.Fatal("envelope carries no event_id")
	}
	events, gap := ring.After(testConvID, 0)
	if gap || len(events) != 1 {
		t.Fatalf("ring holds %d events (gap %v), want 1", len(events), gap)
	}
	if events[0].ID != *got.env.EventID || events[0].Type != protocol.TypeMessage {
		t.Errorf("ring event = {%d %q}, want {%d %q}", events[0].ID, events[0].Type, *got.env.EventID, protocol.TypeMessage)
	}
	if !bytes.Equal(events[0].Payload, m.payload) {
		t.Errorf("ring payload = %s, want %s", events[0].Payload, m.payload)
	}
}

// AC 3: the ring append happens even when nobody is connected, so a phone that
// reconnects later can replay the message.
func TestOperatorMessageEmitterV2_Broadcast_AppendsToRingWithNoConns(t *testing.T) {
	t.Parallel()
	ring := eventring.New(16)
	e := newOperatorMessageEmitterV2(nil, discardLogger())

	e.broadcast(context.Background(), &fakeInteractiveBcast{}, ring, sampleOperatorMessage(t))

	if ring.NewestID(testConvID) == 0 {
		t.Error("no conn open: the message never reached the replay ring")
	}
}

// A wiring with no stream sink has no ring: the push still goes out, without an
// event_id.
func TestOperatorMessageEmitterV2_Broadcast_NilRingPushesWithoutEventID(t *testing.T) {
	t.Parallel()
	bcast := oneInteractiveConn("desk")
	e := newOperatorMessageEmitterV2(nil, discardLogger())

	e.broadcast(context.Background(), bcast, nil, sampleOperatorMessage(t))

	if len(bcast.pushes) != 1 {
		t.Fatalf("pushes = %d, want 1", len(bcast.pushes))
	}
	if bcast.pushes[0].env.EventID != nil {
		t.Errorf("event_id = %d, want none without a ring", *bcast.pushes[0].env.EventID)
	}
}

// AC 3: a conn reconnecting with an earlier last_event_id for the conversation
// gets the message in its replayed tail, ahead of the later events of the turn
// it started. replayMissed reads ring.After with no type filter, so the ring's
// answer is the replayed tail.
func TestOperatorMessageEmitterV2_ReplayedAheadOfLaterEvents(t *testing.T) {
	t.Parallel()
	ring := eventring.New(16)
	before := ring.Append(testConvID, protocol.TypeTurnEnd, json.RawMessage(`{}`), time.Now().UTC())
	e := newOperatorMessageEmitterV2(nil, discardLogger())
	e.broadcast(context.Background(), &fakeInteractiveBcast{}, ring, sampleOperatorMessage(t))
	ring.Append(testConvID, protocol.TypeTurnState, json.RawMessage(`{}`), time.Now().UTC())

	events, gap := ring.After(testConvID, before)
	if gap {
		t.Fatal("unexpected gap")
	}
	if len(events) != 2 || events[0].Type != protocol.TypeMessage || events[1].Type != protocol.TypeTurnState {
		types := make([]string, len(events))
		for i, ev := range events {
			types[i] = ev.Type
		}
		t.Errorf("replayed tail = %v, want [message turn_state]", types)
	}
}

// AC 3: a full channel drops the push with a content-free warning and never
// blocks the delivery that fired it.
func TestOperatorMessageNotify_DropOnFullDoesNotBlock(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	ch := make(chan operatorMessage, 1)
	notify := operatorMessageNotify(ch, logger)
	m := sampleOperatorMessage(t)

	notify(m)
	done := make(chan struct{})
	go func() {
		notify(m) // buffer full → must drop, not block
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("operatorMessageNotify blocked on a full channel")
	}

	logs := buf.String()
	if !strings.Contains(logs, "operator_message.queue_full") {
		t.Errorf("missing queue_full warn; logs = %q", logs)
	}
	if strings.Contains(logs, opText) || strings.Contains(logs, opMsgID) {
		t.Errorf("warn leaked message content: %q", logs)
	}
}

// End to end through the channel: a notification drains through Run, reaches
// the interactive conn, and cleanup joins Run on cancel.
func TestStartOperatorMessageStreamV2_DeliversAndJoins(t *testing.T) {
	t.Parallel()
	ch := make(chan operatorMessage, operatorMessageQueueSize)
	e := newOperatorMessageEmitterV2(ch, discardLogger())
	pushed := make(chan struct{}, 1)
	bcast := &notifyingBcast{inner: oneInteractiveConn("desk"), pushed: pushed}
	ring := eventring.New(16)

	ctx, cancel := context.WithCancel(context.Background())
	cleanup := startOperatorMessageStreamV2(ctx, e, bcast, ring)

	operatorMessageNotify(ch, discardLogger())(sampleOperatorMessage(t))
	select {
	case <-pushed:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not push the delivered message")
	}

	cancel()
	done := make(chan struct{})
	go func() {
		cleanup()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cleanup did not return after ctx cancel")
	}
	cleanup() // idempotent
	if ring.NewestID(testConvID) == 0 {
		t.Error("the streamed message never reached the ring")
	}
}

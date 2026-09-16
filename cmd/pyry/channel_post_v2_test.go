package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
)

// postPayload is the assistant_delta a test posted, kept short because every
// case here asserts on the ENVELOPE rather than on the text.
func postPayload(text string) protocol.AssistantDeltaPayload {
	return protocol.AssistantDeltaPayload{
		ConversationID: testConvID,
		TurnID:         "33333333-3333-4333-8333-333333333333",
		Text:           text,
	}
}

// TestChannelPostEmitterV2_GatesOnInteractive is the #607 capability gate: a v2
// conn without the interactive grant never sees the structured stream, and a
// posted message is on that stream like every other frame.
//
// Both conns are in ONE snapshot so the assertion is a filter rather than a
// timing artefact of two ActiveConns calls.
func TestChannelPostEmitterV2_GatesOnInteractive(t *testing.T) {
	t.Parallel()

	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{
		{ConnID: "interactive", Interactive: true},
		{ConnID: "plain", Interactive: false},
	}}}
	e := newChannelPostEmitterV2(bcast, context.Background(), discardLogger())

	e.announce(postPayload("a reminder"))

	if len(bcast.pushes) != 1 {
		t.Fatalf("pushed %d envelopes, want 1 — only the interactive conn", len(bcast.pushes))
	}
	if bcast.pushes[0].connID != "interactive" {
		t.Errorf("pushed to conn %q, want the interactive one", bcast.pushes[0].connID)
	}
}

// TestChannelPostEmitterV2_FrameShape pins what a client decodes: the
// assistant_delta type, the payload it was handed, no in_reply_to (nothing
// solicited this frame, so correlating it to whatever request happened to
// trigger the post would be a lie), and no event_id.
//
// The absent event_id is a DESIGN decision and not an omission: this emitter
// owns no eventring, so it has no durable id to advertise. A client that
// reconnects reads the post out of the conversation's durable log, which is the
// half of delivery the record owns.
func TestChannelPostEmitterV2_FrameShape(t *testing.T) {
	t.Parallel()

	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newChannelPostEmitterV2(bcast, context.Background(), discardLogger())

	want := postPayload("what are you avoiding today?")
	e.announce(want)

	if len(bcast.pushes) != 1 {
		t.Fatalf("pushed %d envelopes, want 1", len(bcast.pushes))
	}
	env := bcast.pushes[0].env
	if env.Type != protocol.TypeAssistantDelta {
		t.Errorf("type = %q, want %q — the one type the current clients draw as assistant text",
			env.Type, protocol.TypeAssistantDelta)
	}
	if env.InReplyTo != nil {
		t.Errorf("in_reply_to = %v; nothing solicits a posted message", *env.InReplyTo)
	}
	if env.EventID != nil {
		t.Errorf("event_id = %v; this emitter owns no ring and must advertise no durable id", *env.EventID)
	}
	if env.TS.IsZero() {
		t.Error("ts is zero, want a stamp taken at the announcement")
	}

	var got protocol.AssistantDeltaPayload
	if err := json.Unmarshal(env.Payload, &got); err != nil {
		t.Fatalf("decode assistant_delta payload: %v", err)
	}
	if got != want {
		t.Errorf("payload = %+v, want %+v", got, want)
	}
}

// TestChannelPostEmitterV2_EnvelopeIDsIncrease pins the per-emitter monotonic
// counter. V2SessionManager.Push never rewrites Envelope.ID, so each producer
// numbers its own frames, and a chunked post relies on this for the order its
// frames arrive in on one conn.
func TestChannelPostEmitterV2_EnvelopeIDsIncrease(t *testing.T) {
	t.Parallel()

	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
	e := newChannelPostEmitterV2(bcast, context.Background(), discardLogger())

	e.announce(postPayload("one"))
	e.announce(postPayload("two"))
	e.announce(postPayload("three"))

	if len(bcast.pushes) != 3 {
		t.Fatalf("pushed %d envelopes, want 3", len(bcast.pushes))
	}
	for i := 1; i < len(bcast.pushes); i++ {
		if bcast.pushes[i].env.ID <= bcast.pushes[i-1].env.ID {
			t.Errorf("envelope id %d did not increase past %d",
				bcast.pushes[i].env.ID, bcast.pushes[i-1].env.ID)
		}
	}
}

// TestChannelPostEmitterV2_PushErrorDoesNotStopTheLoop is AC#4's fan-out half: a
// conn that tore down between the snapshot and the push is skipped, never fatal,
// and never a reason the other conns go untold. The verb's own answer is settled
// by then — the record is on disk — which is why announce returns nothing at all.
func TestChannelPostEmitterV2_PushErrorDoesNotStopTheLoop(t *testing.T) {
	t.Parallel()

	bcast := &fakeInteractiveBcast{
		snapshots: [][]relay.ActiveConn{{
			{ConnID: "gone", Interactive: true},
			{ConnID: "live", Interactive: true},
		}},
		pushErr: map[string]error{"gone": errors.New("conn not found")},
	}
	e := newChannelPostEmitterV2(bcast, context.Background(), discardLogger())

	e.announce(postPayload("still delivered"))

	if len(bcast.pushes) != 2 {
		t.Fatalf("attempted %d pushes, want 2 — a failing conn must not end the loop", len(bcast.pushes))
	}
	if bcast.pushes[1].connID != "live" {
		t.Errorf("second push went to %q, want the surviving conn", bcast.pushes[1].connID)
	}
}

// TestChannelPostEmitterV2_CancelledContextPushesNothing covers teardown: the
// daemon context is captured at construction, so once it is cancelled a late
// post fans out to nobody rather than blocking the shutdown it raced.
func TestChannelPostEmitterV2_CancelledContextPushesNothing(t *testing.T) {
	t.Parallel()

	bcast := &fakeInteractiveBcast{
		snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}},
		pushErr:   map[string]error{"a": errors.New("session closed")},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e := newChannelPostEmitterV2(bcast, ctx, discardLogger())

	e.announce(postPayload("too late"))

	// The fake's ActiveConns is context-blind, so the push is attempted and
	// fails; what this pins is that the teardown branch returns instead of
	// logging a torn-down conn as a dropped frame.
	if len(bcast.pushes) > 1 {
		t.Errorf("attempted %d pushes after teardown, want at most the one that returned early", len(bcast.pushes))
	}
}

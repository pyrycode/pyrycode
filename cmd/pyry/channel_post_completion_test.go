package main

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/pyrycode/pyrycode/internal/control"
	"github.com/pyrycode/pyrycode/internal/relay"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func testPostCompletion(t *testing.T, raw json.RawMessage, conv, turn string) {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"conversation_id": conv, "turn_id": turn, "stop_reason": "end_turn", "producer": "channel_post"}
	if len(got) != len(want) {
		t.Fatalf("completion keys: %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("completion %s = %v, want %s", k, got[k], v)
		}
	}
}

func TestChannelDelivery_CompletionRequired(t *testing.T) {
	dir := t.TempDir()
	id := conversations.ConversationID(testPostID(t))
	h := &testPostHistory{Store: history.New(dir), fail: id, failAt: 2}
	d := testDelivery(t, dir, h, nil)
	turn := testAccept(t, d, id, "post")
	var live int
	d.announce = func(protocol.AssistantDeltaPayload) { live++ }
	d.drain()
	if live != 0 || len(d.posts) != 1 {
		t.Fatal("completion failure announced or consumed pending work")
	}
	testStartDelivery(t, d)
	testWaitDelivery(t, d, id)
	d.mu.Lock()
	defer d.mu.Unlock()
	page, err := h.Page(id, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 2 || page.Entries[0].Type != protocol.TypeTurnEnd || live != 1 {
		t.Fatalf("retry did not complete exactly once: entries=%v live=%d", page.Entries, live)
	}
	testPostCompletion(t, page.Entries[0].Payload, string(id), turn)
}

// testPostOrderBcast observes the production emit path at each live enqueue.
type testPostOrderBcast struct {
	*fakeInteractiveBcast
	t      *testing.T
	h      *history.Store
	e      *channelPostEmitterV2
	conv   string
	turn   string
	events int
}

func (b *testPostOrderBcast) Push(ctx context.Context, conn string, env protocol.Envelope) error {
	b.t.Helper()
	page, err := b.h.Page(conversations.ConversationID(b.conv), "", 128)
	if err != nil || len(page.Entries) != b.events || page.Entries[0].Type != protocol.TypeTurnEnd {
		b.t.Fatalf("live before complete durable history: %v %v", page, err)
	}
	retained, gap := b.e.ring.After(b.conv, 0)
	if gap || len(retained) == 0 || env.EventID == nil {
		b.t.Fatal("live before shared replay recording")
	}
	last := retained[len(retained)-1]
	if last.ID != *env.EventID || last.Type != env.Type || !bytes.Equal(last.Payload, env.Payload) {
		b.t.Fatal("live event differs from replay")
	}
	if len(b.e.waker.trig) != 0 {
		b.t.Fatal("wake before live fan-out finished")
	}
	if env.Type == protocol.TypeTurnEnd {
		testPostCompletion(b.t, env.Payload, b.conv, b.turn)
	}
	return b.fakeInteractiveBcast.Push(ctx, conn, env)
}

func TestChannelPost_RecordingReplayLiveWakeOrder(t *testing.T) {
	for _, connected := range []bool{false, true} {
		t.Run(map[bool]string{false: "disconnected", true: "connected"}[connected], func(t *testing.T) {
			dir := t.TempDir()
			id := conversations.ConversationID(testPostID(t))
			text := strings.Repeat("<", control.MaxChannelPostBytes)
			chunks := splitDeltaText(text, maxDeltaTextBytes)
			h := &testPostHistory{Store: history.New(dir), fail: id, failAt: len(chunks) + 1}
			d := testDelivery(t, dir, h, nil)
			turn := testAccept(t, d, id, text)
			b := &testPostOrderBcast{fakeInteractiveBcast: &fakeInteractiveBcast{}, t: t, h: h.Store, conv: string(id), turn: turn, events: len(chunks) + 1}
			if connected {
				b.snapshots = [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}, {ConnID: "b", Interactive: true}}}
			}
			e := newChannelPostEmitterV2(b, context.Background(), quietLogger())
			// Use the same ring as the interactive producer, rather than a post-owned one.
			e.ring = newInteractiveTurnEmitterV2(&activeConversation{}, b, quietLogger()).ring
			e.waker = newPushWaker(b, nil, nil, quietLogger())
			b.e = e
			d.announce, d.complete = e.announce, e.complete
			d.drain()
			if e.ring.NewestID(string(id)) != 0 || len(b.pushes) != 0 || len(e.waker.trig) != 0 {
				t.Fatal("failed completion entered replay/live/wake")
			}
			d.drain()
			if len(e.waker.trig) != 1 {
				t.Fatal("completed post did not trigger wake")
			}
			cause := e.waker.takeCause()
			if cause.conversationID != string(id) || cause.trigger != pushWakeTurnEnd {
				t.Fatal("wrong wake cause")
			}
			<-e.waker.trig
			events, gap := e.ring.After(string(id), 0)
			if gap || len(events) != len(chunks)+1 {
				t.Fatal("incomplete replay")
			}
			testPostCompletion(t, events[len(chunks)].Payload, string(id), turn)
			var joined strings.Builder
			for i, ev := range events[:len(chunks)] {
				var p protocol.AssistantDeltaPayload
				if err := json.Unmarshal(ev.Payload, &p); err != nil {
					t.Fatal(err)
				}
				if p.Seq != i || p.TurnID != turn {
					t.Fatal("replay order or identity changed")
				}
				joined.WriteString(p.Text)
				if connected && (*b.pushes[2*i].env.EventID != ev.ID || *b.pushes[2*i+1].env.EventID != ev.ID) {
					t.Fatal("connection-dependent ids")
				}
			}
			if joined.String() != text {
				t.Fatal("maximum post text changed")
			}
			d.drain()
			// Reading retained events, or cleanup-only reload, must not re-notify.
			e.ring.After(string(id), 0)
			if len(e.waker.trig) != 0 {
				t.Fatal("replay triggered wake")
			}
		})
	}
}

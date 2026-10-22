package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/control"
	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/thread"
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
	d.complete = func(channelPostTurnEndPayload) { live++ }
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
	testCanonicalPost(t, history.New(dir), id, turn, "post")
}

func testCanonicalPost(t *testing.T, h *history.Store, id conversations.ConversationID, turn, text string) {
	t.Helper()
	f := thread.New(string(id))
	r, err := h.Forward(id, 0)
	if err != nil {
		t.Fatal(err)
	}
	version, err := h.LatestEntryID(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Walk(t.Context(), version, f.Feed); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, item := range f.Items() {
		if item.Kind != "assistant_message" {
			continue
		}
		count++
		var content struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(item.Content, &content) != nil || content.Text != text || item.Turn != turn || item.Active || item.Status != "done" {
			t.Fatal("canonical post lost text, identity or completion")
		}
	}
	if count != 1 {
		t.Fatalf("canonical post count = %d", count)
	}
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
			b := &fakeInteractiveBcast{}
			if connected {
				b.snapshots = [][]relay.ActiveConn{{{ConnID: "legacy", Interactive: true}, {ConnID: "thread", Interactive: true, Thread: true}}}
			}
			e := newChannelPostEmitterV2(b, context.Background(), quietLogger())
			e.waker = newPushWaker(b, nil, nil, quietLogger())
			d.complete = func(p channelPostTurnEndPayload) {
				page, err := h.Page(id, "", 128)
				if err != nil || len(page.Entries) != len(chunks)+1 || page.Entries[0].Type != protocol.TypeTurnEnd {
					t.Fatal("wake before complete history")
				}
				testPostCompletion(t, page.Entries[0].Payload, string(id), turn)
				e.complete(p)
			}
			d.drain()
			if len(e.waker.trig) != 0 || len(d.posts) != 1 {
				t.Fatal("failed completion consumed work or woke")
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
			got := testDeltas(t, h.Store, id)
			var joined strings.Builder
			for i, p := range got {
				if p.Seq != i || p.TurnID != turn {
					t.Fatal("chunk order or identity changed")
				}
				joined.WriteString(p.Text)
			}
			if joined.String() != text {
				t.Fatal("maximum post changed")
			}
			d.drain()
			again := testDelivery(t, dir, history.New(dir), nil)
			again.complete = d.complete
			again.drain()
			testCanonicalPost(t, history.New(dir), id, turn, text)
			if len(b.pushes) != 0 || len(e.waker.trig) != 0 {
				t.Fatal("raw publication or repeated completion wake")
			}
		})
	}
}

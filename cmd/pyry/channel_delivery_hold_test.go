package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay/handlers"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func testBoundDelivery(t *testing.T) (*channelDelivery, *turnBusyTracker, *history.Store, *streamTurnSink) {
	t.Helper()
	dir := t.TempDir()
	h := history.New(dir)
	d := testDelivery(t, dir, h, nil)
	sink := newStreamTurnSink(0, discardLogger())
	busy := newTurnBusyTracker(stubBusyResolve(map[string]string{"sess-a": testConvID}), discardLogger(), withExitEpoch(sink.exitEpoch), withLifecycleClose(sink.requestLifecycleClose))
	d.bind(busy)
	return d, busy, h, sink
}

func testOpenPostTurn(t *testing.T, busy *turnBusyTracker) {
	t.Helper()
	_, finish, err := busy.beginDelivery(context.Background(), testConvID)
	if err != nil {
		t.Fatal(err)
	}
	finish()
}

func TestChannelDelivery_HoldDeadlineAndIndependentIdle(t *testing.T) {
	d, busy, h, _ := testBoundDelivery(t)
	testOpenPostTurn(t, busy)
	held := testAccept(t, d, testConvID, "posted-secret")
	idle := conversations.ConversationID(testPostID(t))
	testAccept(t, d, idle, "idle")
	var logs bytes.Buffer
	d.log = slog.New(slog.NewTextHandler(&logs, nil))
	d.now = func() time.Time { return d.posts[0].TS.Add(channelPostHoldDeadline + time.Second) }
	d.drain()
	d.drain()
	if len(testDeltas(t, h, testConvID)) != 0 || !busy.Busy(testConvID) || len(testDeltas(t, h, idle)) != 1 {
		t.Fatal("expiry released a live turn or idle conversation was blocked")
	}
	if strings.Count(logs.String(), "channel_post.hold_deadline") != 1 {
		t.Fatalf("diagnostic: %s", logs.String())
	}
	for _, secret := range []string{"posted-secret", "chunks", "seq"} {
		if strings.Contains(logs.String(), secret) {
			t.Fatal("diagnostic leaked content")
		}
	}
	unlock := busy.lockPostBoundary()
	busy.observe("sess-a", snEnd)
	busy.publishPostBoundary(testConvID, false)
	unlock()
	d.drain()
	if got := testDeltas(t, h, testConvID); len(got) != 1 || got[0].TurnID != held {
		t.Fatal("real completion did not release original post")
	}
}

type testPostBoundaryBcast struct {
	*chanBcast
	entered, release chan struct{}
}

func (b *testPostBoundaryBcast) Push(ctx context.Context, id string, env protocol.Envelope) error {
	if env.Type == protocol.TypeTurnEnd {
		close(b.entered)
		select {
		case <-b.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return b.chanBcast.Push(ctx, id, env)
}

func TestChannelDelivery_PublishedCompletionBeforePostAndSuccessor(t *testing.T) {
	d, busy, h, sink := testBoundDelivery(t)
	b := &testPostBoundaryBcast{newChanBcast("conn"), make(chan struct{}), make(chan struct{})}
	e := newInteractiveTurnEmitterV2(&stubCursor{}, b, discardLogger())
	e.hist = h
	ctx, cancel := context.WithCancel(context.Background())
	cleanup := startStreamTurnDrainV2(ctx, sink, e, busy.resolve, busy, discardLogger())
	defer func() { cancel(); cleanup() }()
	testOpenPostTurn(t, busy)
	sink.sinkFor("sess-a")(turnevent.ThinkingProgress{EstimatedTokens: 1})
	collectEnvs(t, b.pushed, 2)
	post := testAccept(t, d, testConvID, "post")
	sink.sinkFor("sess-a")(turnevent.TextChunk{MessageID: "reply", Text: "intact reply"})
	sink.sinkFor("sess-a")(snEnd)
	select {
	case <-b.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("completion did not reach publication gate")
	}
	if busy.Busy(testConvID) {
		t.Fatal("test did not expose internal idle before publication")
	}
	drained := make(chan struct{})
	go func() { d.drain(); close(drained) }()
	started := make(chan error, 1)
	go func() {
		undo, finish, err := busy.beginDelivery(ctx, testConvID)
		if err == nil {
			undo()
			finish()
		}
		started <- err
	}()
	select {
	case <-drained:
		t.Fatal("post bypassed unpublished completion")
	case <-time.After(30 * time.Millisecond):
	}
	if got := testDeltas(t, h, testConvID); len(got) != 1 || got[0].Text != "intact reply" {
		t.Fatal("held post reached history or reply changed")
	}
	close(b.release)
	select {
	case <-drained:
	case <-time.After(3 * time.Second):
		t.Fatal("post drain stuck")
	}
	select {
	case err := <-started:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("successor stuck")
	}
	page, err := h.Page(testConvID, "", 128)
	if err != nil {
		t.Fatal(err)
	}
	ended := false
	for i := len(page.Entries) - 1; i >= 0; i-- {
		if page.Entries[i].Type == protocol.TypeTurnEnd {
			ended = true
		}
		if i == 0 && (!ended || page.Entries[i].Type != protocol.TypeAssistantDelta) {
			t.Fatal("post did not follow real completion")
		}
	}
	var p protocol.AssistantDeltaPayload
	if err := json.Unmarshal(page.Entries[0].Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.TurnID != post {
		t.Fatal("wrong post identity")
	}
}

func TestChannelDelivery_ExitTeardownAndStaleExit(t *testing.T) {
	for _, reason := range []string{"exit", "teardown", "stale"} {
		t.Run(reason, func(t *testing.T) {
			d, busy, h, sink := testBoundDelivery(t)
			testOpenPostTurn(t, busy)
			b := newChanBcast("conn")
			e := newInteractiveTurnEmitterV2(&stubCursor{}, b, discardLogger())
			e.hist = h
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cleanup := startStreamTurnDrainV2(ctx, sink, e, busy.resolve, busy, discardLogger())
			defer func() { cancel(); cleanup() }()
			sink.sinkFor("sess-a")(turnevent.ThinkingProgress{EstimatedTokens: 1})
			collectEnvs(t, b.pushed, 2)
			testAccept(t, d, testConvID, "post")
			sink.sinkFor("sess-a")(turnevent.TextChunk{MessageID: "reply", Text: "buffered"})
			if reason == "stale" {
				sink.ch <- streamTurnEnvelope{sessionID: "sess-a", exit: true, exitEpoch: 0}
				sink.sinkFor("sess-a")(turnevent.ThinkingProgress{EstimatedTokens: 2})
				for collectEnvs(t, b.pushed, 1)[0].Type != protocol.TypeThinkingProgress {
				}
				d.drain()
				if !busy.Busy(testConvID) || len(testDeltas(t, h, testConvID)) != 1 {
					t.Fatal("stale exit released newer turn")
				}
			}
			if reason == "teardown" {
				busy.clearForSession("sess-a")
			} else {
				sink.exitFor("sess-a")()
			}
			// Text flush and idle state precede the consumer's release.
			var idle bool
			for !idle {
				env := collectEnvs(t, b.pushed, 1)[0]
				if env.Type == protocol.TypeTurnState {
					var p protocol.TurnStatePayload
					_ = json.Unmarshal(env.Payload, &p)
					idle = p.State == "idle"
				}
			}
			d.drain()
			got := testDeltas(t, h, testConvID)
			if len(got) != 2 || got[0].Text != "buffered" || got[1].Text != "post" {
				t.Fatalf("exit split/reordered output: %+v", got)
			}
		})
	}
}

func TestChannelDelivery_RetryAndReloadBlockCompetingStart(t *testing.T) {
	for _, reload := range []bool{false, true} {
		t.Run(map[bool]string{false: "partial retry", true: "held restart"}[reload], func(t *testing.T) {
			d, busy, h, _ := testBoundDelivery(t)
			if reload {
				testOpenPostTurn(t, busy)
			}
			text := strings.Repeat("x", maxDeltaTextBytes+1)
			post := testAccept(t, d, testConvID, text)
			if reload {
				d = testDelivery(t, d.path[:strings.LastIndex(d.path, "/")], h, nil)
				busy = newTurnBusyTracker(busy.resolve, discardLogger())
				d.bind(busy)
			}
			bad := &testPostHistory{Store: h, fail: testConvID, failAt: 2}
			d.hist = bad
			d.drain()
			testAccept(t, d, testConvID, "second")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			wrote := make(chan struct{}, 1)
			w := funcWriter{write: func(context.Context, string, []byte) error { wrote <- struct{}{}; return nil }}
			deliver := newInboundDeliver(func(string) (handlers.TurnWriter, error) { return w, nil }, busy, time.Second)
			done := make(chan error, 1)
			go func() { done <- deliver(ctx, testConvID, nil) }()
			select {
			case <-wrote:
				t.Fatal("successor split a partial post")
			case <-time.After(30 * time.Millisecond):
			}
			bad.mu.Lock()
			bad.failAt = 0
			bad.fail = ""
			bad.mu.Unlock()
			d.drain()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("successor did not start")
			}
			got := testDeltas(t, h, testConvID)
			if len(got) != 3 || got[0].TurnID != post || got[1].TurnID != post || got[0].Seq != 0 || got[1].Seq != 1 || got[0].Text+got[1].Text != text || got[2].Text != "second" {
				t.Fatal("retry/reload FIFO or identity changed")
			}
		})
	}
}

func TestChannelDelivery_InFlightSendNowHoldsAcrossClose(t *testing.T) {
	d, busy, h, _ := testBoundDelivery(t)
	testOpenPostTurn(t, busy)
	entered, release := make(chan struct{}), make(chan struct{})
	w := funcWriter{write: func(context.Context, string, []byte) error {
		close(entered)
		<-release
		return errors.New("failed write")
	}}
	send := newSendNowDeliver(func(string) (handlers.TurnWriter, error) { return w, nil }, func(string) bool { return true }, busy, nil)
	done := make(chan error, 1)
	go func() { done <- send(context.Background(), testConvID, 1, nil) }()
	<-entered
	testAccept(t, d, testConvID, "post")
	unlock := busy.lockPostBoundary()
	busy.observe("sess-a", snEnd)
	busy.publishPostBoundary(testConvID, false)
	unlock()
	busy.mu.Lock()
	gen := busy.carried[testConvID]
	busy.mu.Unlock()
	busy.releaseCarried(testConvID, gen) // force the existing grace past the blocked write
	d.drain()
	if len(testDeltas(t, h, testConvID)) != 0 {
		t.Fatal("post interleaved with in-flight send-now")
	}
	close(release)
	if err := <-done; err == nil {
		t.Fatal("write error lost")
	}
	d.drain()
	if len(testDeltas(t, h, testConvID)) != 1 {
		t.Fatal("finished send-now did not release")
	}
}

func TestChannelDelivery_NoRelayPublicationAndTeardown(t *testing.T) {
	d, busy, h, sink := testBoundDelivery(t)
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{ID: testConvID, CurrentSessionID: "sess-a"})
	trans := &captureObserverSink{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cleanup, _, _, _, _, _, err := startRelay(ctx, discardLogger(), relayWiring{streamSink: sink, busy: busy, hist: h, convReg: reg, active: &activeConversation{}, transitions: trans})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); cleanup() }()
	for _, teardown := range []bool{false, true} {
		testOpenPostTurn(t, busy)
		testAccept(t, d, testConvID, "post")
		sink.sinkFor("sess-a")(turnevent.TextChunk{MessageID: "reply", Text: "reply"})
		if teardown {
			trans.obs(sessions.SessionTransition{Reason: sessions.ReasonEviction, PreviousID: "sess-a", OccurredAt: time.Now()})
		} else {
			sink.sinkFor("sess-a")(snEnd)
		}
		deadline := time.Now().Add(3 * time.Second)
		for {
			d.drain()
			d.mu.Lock()
			pending := d.pendingFor(testConvID)
			d.mu.Unlock()
			if !pending {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("no-relay lifecycle did not release")
			}
			time.Sleep(time.Millisecond)
		}
	}
	if got := testDeltas(t, h, testConvID); len(got) != 4 || got[0].Text != "reply" || got[1].Text != "post" || got[2].Text != "reply" || got[3].Text != "post" {
		t.Fatalf("no-relay ordering: %+v", got)
	}
}

package main

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

type liveHeldBroadcaster struct {
	entered, release chan struct{}
	once             sync.Once
	fakeInteractiveBcast
}

func (b *liveHeldBroadcaster) ActiveConns(ctx context.Context) []relay.ActiveConn {
	b.once.Do(func() {
		close(b.entered)
		select {
		case <-b.release:
		case <-ctx.Done():
		}
	})
	return b.fakeInteractiveBcast.ActiveConns(ctx)
}
func TestDaemonLiveLegacyDelayAndIdentity(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := &liveHeldBroadcaster{entered: make(chan struct{}), release: make(chan struct{}), fakeInteractiveBcast: fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "phone", Interactive: true}}}}}
	o := newDaemonLiveState(func(id string) (string, bool) { return testConvID, id != "" })
	s := newStreamTurnSink(32, discardLogger())
	s.live = o
	e := newInteractiveTurnEmitterV2(&stubCursor{}, b, discardLogger())
	e.hist = history.New(t.TempDir())
	e.phases = &turnPhaseSnapshot{}
	stop := startStreamTurnDrainV2(ctx, s, e, o.resolve, nil, discardLogger())
	t.Cleanup(func() { cancel(); stop(); e.flushTimer.Stop() })
	send := s.sinkForTag(func() string { return "a" }, "claude")
	send(turnevent.ThinkingProgress{})
	select {
	case <-b.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("legacy gate not reached")
	}
	send(turnevent.ThinkingProgress{})
	send(turnevent.ToolStart{ToolCallID: "tool"})
	send(turnevent.ToolProgress{ToolCallID: "tool", ElapsedSeconds: 7})
	got := testLiveReadings(t, o, testConvID)[liveReadingKey{protocol.TypeToolProgress, "tool"}]
	var payload protocol.ToolProgressPayload
	if json.Unmarshal(got.Envelope.Payload, &payload) != nil || payload.TurnID == "" {
		t.Fatal("source turn identity absent")
	}
	// Readings reach retention while ActiveConns is blocked. They never call it.
	if testLiveReadings(t, o, testConvID)[liveReadingKey{protocol.TypeThinkingProgress, ""}].Revision != 2 {
		t.Fatal("retention waited for legacy")
	}
	page, err := e.hist.Page(conversations.ConversationID(testConvID), "", 20)
	if err != nil || len(page.Entries) != 1 {
		t.Fatalf("retention wrote history while legacy was held: %d, %v", len(page.Entries), err)
	}
	close(b.release)
	send(turnevent.TurnEnd{})
	waitLiveDrain(t, s)
	cancel()
	stop()
	page, err = e.hist.Page(conversations.ConversationID(testConvID), "", 20)
	if err != nil || len(page.Entries) != 8 {
		t.Fatalf("legacy history changed: %d, %v", len(page.Entries), err)
	}
	if e.turnID != "" {
		t.Fatal("drain did not finish queued turn")
	}
	for _, push := range b.pushes {
		if push.env.Type == protocol.TypeToolProgress {
			var legacy protocol.ToolProgressPayload
			_ = json.Unmarshal(push.env.Payload, &legacy)
			if legacy.TurnID != payload.TurnID {
				t.Fatal("legacy and retained turn IDs differ")
			}
		}
		if len(push.env.SessionID) != 0 {
			t.Fatal("live metadata changed legacy envelopes")
		}
	}
}
func TestDaemonLiveSamePhaseAndChildScope(t *testing.T) {
	o := testLiveOwner()
	s := newStreamTurnSink(32, discardLogger())
	s.live = o
	b := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "phone", Interactive: true}}}}
	e := newInteractiveTurnEmitterV2(&stubCursor{}, b, discardLogger())
	defer e.flushTimer.Stop()
	tag := newStreamSessionTag("a")
	send := s.sinkForSessionTag(tag, "claude")
	handle := func(ev turnevent.Event) {
		send(ev)
		env := <-s.ch
		e.liveCapture = env.live
		e.handleForSource(context.Background(), env.live.source.ConversationID, env.ev, env.source, env.incarnation)
		e.liveCapture = nil
	}
	handle(turnevent.TextChunk{Text: "first"})
	o.transition(sessions.SessionTransition{ConversationID: "conv", PreviousID: "a", NewID: "z", NextAgent: "claude", Cause: sessions.CauseOperatorReset})
	tag.Rotate("z")
	handle(turnevent.TextChunk{Text: "same phase"})
	count := 0
	for _, push := range b.pushes {
		if push.env.Type == protocol.TypeTurnState {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("legacy phase dedup changed: %d", count)
	}
	phase := testLiveReadings(t, o, "conv")[liveReadingKey{protocol.TypeTurnState, ""}]
	if string(phase.Envelope.SessionID) != `"z"` || phase.SessionGeneration != 2 {
		t.Fatal("same-phase successor not retained")
	}
	handle(turnevent.ToolStart{ToolCallID: "launcher", Title: "Agent"})
	handle(turnevent.ToolStart{ToolCallID: "child", ParentToolCallID: "launcher"})
	handle(turnevent.ToolProgress{ToolCallID: "child"})
	handle(turnevent.TurnEnd{})
	if _, ok := testLiveReadings(t, o, "conv")[liveReadingKey{protocol.TypeToolProgress, "child"}]; !ok {
		t.Fatal("main turn retired child progress")
	}
	handle(turnevent.ToolUpdate{ToolCallID: "child", ParentToolCallID: "launcher", Status: turnevent.ToolStatusCompleted})
	if _, ok := testLiveReadings(t, o, "conv")[liveReadingKey{protocol.TypeToolProgress, "child"}]; ok {
		t.Fatal("terminal child survived")
	}
}

func TestDaemonLiveTransitionMetadata(t *testing.T) {
	for _, tc := range []struct {
		name        string
		sid         sessions.SessionID
		agent, want string
	}{{"unknown", "new", "", ""}, {"unsupported", "new", "future", ""}, {"no session", "", "", "null"}, {"known", "new", "claude", `"new"`}} {
		t.Run(tc.name, func(t *testing.T) {
			o := testLiveOwner()
			o.transition(sessions.SessionTransition{ConversationID: "conv", PreviousID: "a", NewID: tc.sid, NextAgent: tc.agent})
			for _, r := range testLiveReadings(t, o, "conv") {
				if string(r.Envelope.SessionID) != tc.want {
					t.Fatalf("session metadata: %s", r.Envelope.SessionID)
				}
				raw, err := json.Marshal(r.Envelope)
				if err != nil || len(raw) > protocol.MaxThreadEnvelopeBytes {
					t.Fatal("clear exceeded bound")
				}
			}
		})
	}
}

func waitLiveDrain(t *testing.T, s *streamTurnSink) {
	t.Helper()
	s.offerMu.Lock()
	target := s.queued
	s.offerMu.Unlock()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for s.shadowProcessed.Load() < target {
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("drain did not process queued output")
		}
	}
}

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func TestDaemonLiveCaptureTransitionOrdering(t *testing.T) {
	o := testLiveOwner()
	s := newStreamTurnSink(8, discardLogger())
	s.live = o
	tag := newStreamSessionTag("a")
	send := s.sinkForSessionTag(tag, "claude")
	send(turnevent.ModelAnnounced{Model: "old"})
	old := <-s.ch
	ready, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	s.liveCaptureReady = func() { close(ready); <-release }
	go func() { defer close(done); send(turnevent.Stall{}) }()
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		<-done
	})
	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		t.Fatal("capture boundary not reached")
	}
	e := newSessionTransitionEmitterV2(historyOnlyBroadcaster{}, o.resolve, discardLogger())
	e.runtimeSink = s
	e.Enqueue(sessions.SessionTransition{ConversationID: "conv", PreviousID: "a", NewID: "a", NextAgent: "claude", Cause: sessions.CauseOperatorReset})
	close(release)
	<-done
	late := <-s.ch
	if late.live.source.SessionGeneration != old.live.source.SessionGeneration {
		t.Fatalf("predecessor captured generation %d, want %d", late.live.source.SessionGeneration, old.live.source.SessionGeneration)
	}
	if r := testLiveReadings(t, o, "conv")[liveReadingKey{protocol.TypeStall, ""}]; !r.Envelope.SessionStateCleared {
		t.Fatal("late predecessor replaced successor clear")
	}
}

func TestDaemonLiveReturningRoutingID(t *testing.T) {
	o := testLiveOwner()
	s := newStreamTurnSink(8, discardLogger())
	s.live = o
	tag := newStreamSessionTag("a")
	send := s.sinkForSessionTag(tag, "claude")
	send(turnevent.ModelAnnounced{Model: "first"})
	old := (<-s.ch).live.source
	for _, ids := range [][2]string{{"a", "z"}, {"z", "a"}} {
		s.queueBoundary(sessions.SessionTransition{ConversationID: "conv", PreviousID: sessions.SessionID(ids[0]), NewID: sessions.SessionID(ids[1]), NextAgent: "claude", Cause: sessions.CauseOperatorReset}, func() {}, nil)
		tag.Rotate(ids[1])
		send(turnevent.ModelAnnounced{Model: ids[1]})
		env := <-s.ch
		r := testLiveReadings(t, o, "conv")[liveReadingKey{protocol.TypeModelAnnounced, ""}]
		if r.Envelope.SessionStateCleared || r.SessionGeneration != env.live.source.SessionGeneration || r.SessionGeneration <= old.SessionGeneration {
			t.Fatalf("fresh %s bound to retired generation: %+v", ids[1], r)
		}
	}
	before := testLiveReadings(t, o, "conv")[liveReadingKey{protocol.TypeModelAnnounced, ""}]
	o.acceptEvent(old, turnevent.ModelAnnounced{Model: "late first"})
	after := testLiveReadings(t, o, "conv")[liveReadingKey{protocol.TypeModelAnnounced, ""}]
	if string(after.Envelope.Payload) != string(before.Envelope.Payload) || after.Revision != before.Revision {
		t.Fatal("delayed original A replaced returning A")
	}
}

func TestDaemonLiveNeutralMainTurnIdentity(t *testing.T) {
	for _, ev := range []turnevent.Event{turnevent.ToolProgress{ToolCallID: "tool"}, turnevent.ToolCallDenied{ToolCallID: "denied"}} {
		t.Run(fmt.Sprintf("%T", ev), func(t *testing.T) {
			o := testLiveOwner()
			s := newStreamTurnSink(8, discardLogger())
			s.live = o
			b := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "phone", Interactive: true}}}}
			e := newInteractiveTurnEmitterV2(&stubCursor{}, b, discardLogger())
			defer e.flushTimer.Stop()
			send := s.sinkForSessionTag(newStreamSessionTag("a"), "claude")
			for _, event := range []turnevent.Event{ev, turnevent.ToolProgress{ToolCallID: "next"}} {
				send(event)
				env := <-s.ch
				e.liveCapture = env.live
				e.handleForSource(context.Background(), "conv", event, env.source, env.incarnation)
				if env.live.mainTurnID == "" || env.live.mainTurnID != e.turnID {
					t.Fatalf("source and legacy identities disagree: %q / %q", env.live.mainTurnID, e.turnID)
				}
			}
			r := testLiveReadings(t, o, "conv")[liveReadingKey{protocol.TypeToolProgress, "next"}]
			var p protocol.ToolProgressPayload
			if json.Unmarshal(r.Envelope.Payload, &p) != nil || p.TurnID != e.turnID {
				t.Fatalf("retained progress identity: %s", r.Envelope.Payload)
			}
			for _, push := range b.pushes {
				if push.env.Type == protocol.TypeTurnState {
					t.Fatal("neutral event emitted phase")
				}
			}
			if turnMarkFor(ev) != turnMarkNone {
				t.Fatal("busy classification changed")
			}
		})
	}
}

func TestDaemonLiveCompletedActivationStorage(t *testing.T) {
	o := testLiveOwner()
	s := newStreamTurnSink(8, discardLogger())
	s.live = o
	tag := newStreamSessionTag("a")
	send := s.sinkForSessionTag(tag, "claude")
	var cursor *daemonLiveCursor
	var old daemonLiveSource
	for i := 0; i < 100; i++ {
		if i > 0 {
			s.beginRuntimeProducer(tag)
		}
		send(turnevent.ToolProgress{ToolCallID: fmt.Sprint(i)})
		env := <-s.ch
		if i == 0 {
			cursor, old = o.snapshot("conv"), env.live.source
		}
		if i%2 == 0 {
			s.exitForSessionTag(tag)()
			<-s.ch
		} else {
			s.runnerStopped("a")
			s.takeStopped(s.queued)
		}
	}
	o.acceptEvent(old, turnevent.ToolProgress{ToolCallID: "stale"})
	if len(o.sources) != 0 || len(o.turns) != 0 {
		t.Fatalf("completed activations retained: sources=%d turns=%d", len(o.sources), len(o.turns))
	}
	c := o.conversations["conv"]
	if len(c.revisions) > len(streamLiveFamilies)+1 {
		t.Fatalf("retired generations retained %d revisions", len(c.revisions))
	}
	if r, ok := cursor.Next(); !ok || r.SessionGeneration != old.SessionGeneration {
		t.Fatal("cleanup invalidated detached cursor")
	}
}

func TestDaemonLiveConversationRemoval(t *testing.T) {
	reg := &conversations.Registry{}
	id := conversations.ConversationID("conv")
	reg.Create(conversations.Conversation{ID: id, CurrentSessionID: "a"})
	o := newDaemonLiveState(func(sid string) (string, bool) { return conversationForSession(reg, sid) })
	s := newStreamTurnSink(8, discardLogger())
	s.live = o
	send := s.sinkForSessionTag(newStreamSessionTag("a"), "claude")
	send(turnevent.ToolProgress{ToolCallID: "tool"})
	old := (<-s.ch).live.source
	cursor := o.snapshot("conv")
	dropRingOnConversationDelete(reg, nil, nil, nil, s)
	reg.Delete(id)
	o.acceptEvent(old, turnevent.Stall{})
	s.queueBoundary(sessions.SessionTransition{ConversationID: "conv", PreviousID: "a", NewID: "a", NextAgent: "claude"}, func() {}, nil)
	send(turnevent.ModelAnnounced{Model: "late"})
	<-s.ch
	if len(o.conversations) != 0 || len(o.sources) != 0 || len(o.turns) != 0 {
		t.Fatal("removed conversation still owns state")
	}
	oldReading, ok := cursor.Next()
	if !ok {
		t.Fatal("removal invalidated cursor")
	}
	for i := 0; i < 100; i++ {
		reg.Create(conversations.Conversation{ID: id, CurrentSessionID: "a"})
		send(turnevent.ToolProgress{ToolCallID: fmt.Sprint(i)})
		fresh := <-s.ch
		if fresh.live.source.SessionGeneration <= old.SessionGeneration || o.retain(oldReading) {
			t.Fatal("deleted capture aliased recreated conversation")
		}
		reg.Delete(id)
		if len(o.conversations) != 0 || len(o.sources) != 0 || len(o.turns) != 0 {
			t.Fatal("repeated removals accumulated state")
		}
	}
}

func TestDaemonLiveNoRelayRemoval(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{ID: testConvID, CurrentSessionID: "a"})
	s := newStreamTurnSink(8, discardLogger())
	stop, _, _, _, _, _, err := startRelay(ctx, discardLogger(), relayWiring{streamSink: s, convReg: reg, active: &activeConversation{}, transitions: &stubTransitionSink{}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); stop() }()
	s.sinkForSessionTag(newStreamSessionTag("a"), "claude")(turnevent.ModelAnnounced{Model: "retained"})
	if len(testLiveReadings(t, s.live, testConvID)) != 1 {
		t.Fatal("no-relay owner not installed")
	}
	reg.Delete(testConvID)
	if len(s.live.conversations) != 0 || len(s.live.sources) != 0 || len(s.live.turns) != 0 {
		t.Fatal("no-relay composition did not release live state")
	}
}

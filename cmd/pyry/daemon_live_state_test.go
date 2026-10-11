package main

import (
	"encoding/json"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func testLiveOwner() *daemonLiveState {
	return newDaemonLiveState(func(id string) (string, bool) {
		if id == "b" {
			return "other", true
		}
		return "conv", id != ""
	})
}
func testLiveReadings(t *testing.T, o *daemonLiveState, conv string) map[liveReadingKey]daemonLiveReading {
	t.Helper()
	c := o.snapshot(conv)
	out := make(map[liveReadingKey]daemonLiveReading)
	for {
		r, ok := c.Next()
		if !ok {
			return out
		}
		out[liveReadingKey{r.Envelope.Type, r.ReadingID}] = r
	}
}
func TestDaemonLiveProducerCapture(t *testing.T) {
	o := testLiveOwner()
	s := newStreamTurnSink(1, nil)
	s.live = o
	tag := newStreamSessionTag("a")
	send := s.sinkForSessionTag(tag, "claude")
	send(turnevent.TextChunk{Text: "first"})
	first := <-s.ch
	send(turnevent.ApiRetry{Active: true, Current: 1})
	send(turnevent.ApiRetry{Active: false}) // fan-in is full; current retention must still advance
	got := testLiveReadings(t, o, "conv")[liveReadingKey{protocol.TypeApiRetry, ""}]
	var p protocol.ApiRetryPayload
	if err := json.Unmarshal(got.Envelope.Payload, &p); err != nil || p.Active {
		t.Fatalf("inactive retry lost: %s", got.Envelope.Payload)
	}
	if first.live == nil || first.live.source.SessionGeneration == 0 || first.live.source.ConversationID != "conv" {
		t.Fatal("source not captured before queue")
	}
	s.beginRuntimeProducer(tag)
	send(turnevent.TextChunk{Text: "successor"})
	next := testLiveReadings(t, o, "conv")[liveReadingKey{protocol.TypeTurnState, ""}]
	if next.SessionGeneration <= first.live.source.SessionGeneration {
		t.Fatal("reused routing ID did not advance generation")
	}
	o.acceptEvent(first.live.source, turnevent.Stall{})
	if r, ok := testLiveReadings(t, o, "conv")[liveReadingKey{protocol.TypeStall, ""}]; ok && !r.Envelope.SessionStateCleared {
		t.Fatal("retired source restored stall")
	}
}
func TestDaemonLiveTransitions(t *testing.T) {
	for _, cause := range []sessions.LifecycleCause{sessions.CauseOperatorReset, sessions.CauseClaudeClear, sessions.CauseRecovery, sessions.CauseAgentSwitch} {
		t.Run(string(cause), func(t *testing.T) {
			o := testLiveOwner()
			src := o.capture("a", 1, history.SessionProvenance{Kind: "claude", SessionID: "a"}, true)
			o.acceptEvent(src, turnevent.ModelAnnounced{Model: "old"})
			before := o.snapshot("conv")
			o.transition(sessions.SessionTransition{ConversationID: "conv", PreviousID: "a", NewID: "z", NextAgent: "codex", Cause: cause})
			got := testLiveReadings(t, o, "conv")
			if len(got) != len(daemonLiveFamilies) {
				t.Fatalf("clears: %d", len(got))
			}
			for k, r := range got {
				if !r.Envelope.SessionStateCleared || string(r.Envelope.Payload) != "{}" || string(r.Envelope.SessionID) != `"z"` || r.SessionGeneration <= src.SessionGeneration || r.Revision == 0 {
					t.Fatalf("bad clear %s: %+v", k.family, r)
				}
			}
			o.acceptEvent(src, turnevent.ModelAnnounced{Model: "late"})
			if !testLiveReadings(t, o, "conv")[liveReadingKey{protocol.TypeModelAnnounced, ""}].Envelope.SessionStateCleared {
				t.Fatal("old source restored reading")
			}
			old, ok := before.Next()
			if !ok || old.SessionGeneration != src.SessionGeneration {
				t.Fatal("cursor generation changed")
			}
		})
	}
}

func TestDaemonLiveProducerRecovery(t *testing.T) {
	o := testLiveOwner()
	s := newStreamTurnSink(32, discardLogger())
	s.live = o
	tag := newStreamSessionTag("a")
	send := s.sinkForSessionTag(tag, "claude")
	send(turnevent.ThinkingProgress{})
	first := <-s.ch
	send(turnevent.BackgroundTaskProgress{TaskID: "task"})
	<-s.ch
	s.exitForSessionTag(tag)()
	exit := <-s.ch
	if exit.live == nil || exit.live.source.SessionGeneration != first.live.source.SessionGeneration {
		t.Fatal("exit lost original generation")
	}
	if _, ok := testLiveReadings(t, o, "conv")[liveReadingKey{protocol.TypeBackgroundTaskProgress, "task"}]; ok {
		t.Fatal("task survived producer stop")
	}
	send(turnevent.ThinkingProgress{})
	successor := <-s.ch
	if successor.live.source.SessionGeneration <= first.live.source.SessionGeneration {
		t.Fatal("same-ID recovery did not advance generation")
	}
	o.acceptEvent(first.live.source, turnevent.Stall{})
	r, ok := testLiveReadings(t, o, "conv")[liveReadingKey{protocol.TypeStall, ""}]
	if ok && !r.Envelope.SessionStateCleared {
		t.Fatal("old recovery tail repopulated current generation")
	}
	for _, cause := range []sessions.LifecycleCause{sessions.CauseIdleSleep, sessions.CauseCapacityEviction} {
		s.queueBoundary(sessions.SessionTransition{ConversationID: "conv", PreviousID: "a", Cause: cause, Reason: sessions.ReasonEviction}, func() {}, nil)
		boundary := s.runtimePending[len(s.runtimePending)-1]
		clear, ok := boundary.live.Next()
		if !ok || !clear.Envelope.SessionStateCleared || string(clear.Envelope.SessionID) != "null" {
			t.Fatal("boundary clear not captured")
		}
		s.beginRuntimeProducer(tag)
		send(turnevent.ThinkingProgress{})
		next := <-s.ch
		if next.live.source.SessionGeneration <= successor.live.source.SessionGeneration {
			t.Fatal("reactivation reused generation")
		}
		successor = next
	}
}

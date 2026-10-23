package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func TestDaemonLiveExecutionOpeningClock(t *testing.T) {
	o := newDaemonLiveState(func(string) (string, bool) { return testConvID, true })
	s := newStreamTurnSink(8, discardLogger())
	s.live = o
	send := s.sinkForSessionTag(newStreamSessionTag("a"), "claude")
	e := newInteractiveTurnEmitterV2(&stubCursor{}, historyOnlyBroadcaster{}, discardLogger())
	defer e.flushTimer.Stop()
	e.hist, e.runtimeFacts = history.New(t.TempDir()), true
	var queued []streamTurnEnvelope
	var idles []protocol.Envelope
	for range 2 {
		send(turnevent.TextChunk{Text: "reply"})
		queued = append(queued, <-s.ch)
		send(turnevent.TurnEnd{})
		queued = append(queued, <-s.ch)
		idles = append(idles, testLiveReadings(t, o, testConvID)[liveReadingKey{protocol.TypeTurnState, ""}].Envelope)
	}
	// Both producers finish before any history is appended by the drain.
	for _, env := range queued {
		e.liveCapture = env.live
		e.handleForSource(context.Background(), testConvID, env.ev, env.source, env.incarnation)
	}
	page, err := e.hist.Page(conversations.ConversationID(testConvID), "", 100)
	if err != nil {
		t.Fatal(err)
	}
	openings := 0
	for _, entry := range page.Entries {
		if entry.Type != historyTurnOpened {
			continue
		}
		var p runtimeHistoryFact
		if json.Unmarshal(entry.Payload, &p) != nil || p.TurnID == "" || p.OccurredAt.IsZero() {
			t.Fatal("invalid source opening")
		}
		index := 1 - openings // History pages are newest first.
		if p.OccurredAt.After(idles[index].TS) || !p.OccurredAt.Equal(entry.TS) {
			t.Fatal("opening clock follows its captured terminal state")
		}
		if index == 1 && !p.OccurredAt.After(idles[0].TS) {
			t.Fatal("successor execution includes predecessor idle")
		}
		openings++
	}
	if openings != 2 {
		t.Fatalf("openings=%d, want 2", openings)
	}
}

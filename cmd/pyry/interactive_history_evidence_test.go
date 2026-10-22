package main

import (
	"bytes"
	"log/slog"
	"sort"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func testRecordedEmitter(t *testing.T, cursor cursorReader, bcast interactiveBroadcaster, log *slog.Logger) *interactiveTurnEmitterV2 {
	t.Helper()
	e := newInteractiveTurnEmitterV2(cursor, bcast, log)
	e.hist = history.New(t.TempDir())
	t.Cleanup(func() { e.flushTimer.Stop() })
	return e
}

// testEmitterFacts exposes recorded source payloads for buffering and lifecycle
// assertions. These envelopes are decoding containers, never delivery evidence.
func testEmitterFacts(t *testing.T, e *interactiveTurnEmitterV2, retained ...string) []recordedPush {
	t.Helper()
	convs := map[string]bool{}
	var entries []history.Entry
	retained = append(retained, testConvID)
	for _, state := range e.turns {
		retained = append(retained, state.conversationID)
	}
	for _, conv := range retained {
		if !convs[conv] {
			convs[conv] = true
			entries = append(entries, historyEntries(t, e.hist, conv)...)
		}
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].TS.Before(entries[j].TS) })
	var facts []recordedPush
	for _, entry := range entries {
		facts = append(facts, recordedPush{env: protocol.Envelope{Type: entry.Type, TS: entry.TS, Payload: entry.Payload}})
	}
	return facts
}

// testSourceCollector checks source order and the surviving live-state output
// together. Retired content is read from history, never synthesized as delivery.
func testSourceCollector(t *testing.T, store func() *history.Store, live <-chan protocol.Envelope) func(int) []protocol.Envelope {
	t.Helper()
	consumed := 0
	return func(n int) []protocol.Envelope {
		t.Helper()
		var entries []history.Entry
		testShadowWait(t, func() bool {
			entries = append(historyEntries(t, store(), testConvID), historyEntries(t, store(), testConvIDB)...)
			sort.SliceStable(entries, func(i, j int) bool { return entries[i].TS.Before(entries[j].TS) })
			return len(entries) >= consumed+n
		})
		var facts []protocol.Envelope
		for _, entry := range entries[consumed : consumed+n] {
			fact := protocol.Envelope{Type: entry.Type, TS: entry.TS, Payload: entry.Payload}
			facts = append(facts, fact)
			if testRetiredInteractiveType(entry.Type) {
				continue
			}
			select {
			case actual := <-live:
				if actual.Type != fact.Type || !bytes.Equal(actual.Payload, fact.Payload) || !actual.TS.Equal(fact.TS) {
					t.Fatalf("live state differs from source: %s / %s", actual.Type, fact.Type)
				}
			case <-time.After(3 * time.Second):
				t.Fatalf("missing retained live state %s", fact.Type)
			}
		}
		consumed += n
		return facts
	}
}

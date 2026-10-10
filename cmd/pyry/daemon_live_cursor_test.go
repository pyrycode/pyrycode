package main

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func TestDaemonLiveDetachedCursor(t *testing.T) {
	o := testLiveOwner()
	a := o.capture("a", 1, history.SessionProvenance{Kind: "claude", SessionID: "a"}, true)
	b := o.capture("b", 2, history.SessionProvenance{Kind: "codex", SessionID: "b"}, true)
	reply, event, hist := uint64(3), uint64(4), uint64(5)
	env := protocol.Envelope{Type: protocol.TypeModelAnnounced, Payload: json.RawMessage(`{"model":"one"}`), InReplyTo: &reply, EventID: &event, HistoryEntryID: &hist}
	first, ok := o.admit(a, env, "")
	if !ok {
		t.Fatal("admission refused")
	}
	cursor, otherCursor := o.snapshot("conv"), o.snapshot("conv")
	env.Payload[2] = 'X'
	reply = 30
	event = 40
	hist = 50
	first.Envelope.Payload[2] = 'Y'
	*first.Envelope.InReplyTo = 99
	o.acceptEvent(b, turnevent.Stall{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			o.acceptEvent(a, turnevent.ModelAnnounced{Model: "new"})
		}
	}()
	r, ok := cursor.Next()
	if !ok || *r.Envelope.InReplyTo != 3 || *r.Envelope.EventID != 4 || *r.Envelope.HistoryEntryID != 5 || string(r.Envelope.Payload) != `{"model":"one"}` {
		t.Fatalf("mutated cursor: %+v", r)
	}
	r.Envelope.Payload[2] = 'Z'
	r.Envelope.SessionID[1] = 'z'
	*r.Envelope.InReplyTo = 100
	second, _ := otherCursor.Next()
	if string(second.Envelope.SessionID) != `"a"` || *second.Envelope.InReplyTo != 3 || string(second.Envelope.Payload) != `{"model":"one"}` {
		t.Fatal("cursors alias")
	}
	if _, ok := cursor.Next(); ok {
		t.Fatal("cursor followed new readings")
	}
	wg.Wait()
	if o.retain(first) {
		t.Fatal("overtaken revision accepted")
	}
	got := testLiveReadings(t, o, "conv")[liveReadingKey{protocol.TypeModelAnnounced, ""}]
	if got.Revision != 101 || got.SessionGeneration != a.SessionGeneration {
		t.Fatalf("ordering = %+v", got)
	}
	if _, ok := testLiveReadings(t, o, "other")[liveReadingKey{protocol.TypeStall, ""}]; !ok {
		t.Fatal("other conversation lost")
	}
}

func TestDaemonLiveProgressRetirement(t *testing.T) {
	o := testLiveOwner()
	src := o.capture("a", 1, history.SessionProvenance{Kind: "claude", SessionID: "a"}, true)
	events := []turnevent.Event{
		turnevent.ThinkingProgress{}, turnevent.ToolStart{ToolCallID: "one"}, turnevent.ToolStart{ToolCallID: "two"},
		turnevent.ToolProgress{ToolCallID: "one", ElapsedSeconds: 1}, turnevent.ToolProgress{ToolCallID: "two", ElapsedSeconds: 2},
		turnevent.BackgroundTaskProgress{TaskID: "a"}, turnevent.BackgroundTaskProgress{TaskID: "b"},
	}
	for _, ev := range events {
		o.acceptEvent(src, ev)
	}
	got := testLiveReadings(t, o, "conv")
	if len(got) != 6 {
		t.Fatalf("independent identities lost: %d", len(got))
	}
	o.acceptEvent(src, turnevent.ToolUpdate{ToolCallID: "one", Status: turnevent.ToolStatusCompleted})
	o.acceptEvent(src, turnevent.BackgroundTaskUpdated{TaskID: "a", Patch: "{}"})
	got = testLiveReadings(t, o, "conv")
	if _, ok := got[liveReadingKey{protocol.TypeToolProgress, "one"}]; ok {
		t.Fatal("completed tool survived")
	}
	if _, ok := got[liveReadingKey{protocol.TypeBackgroundTaskProgress, "a"}]; !ok {
		t.Fatal("patch retired task")
	}
	o.acceptEvent(src, turnevent.ToolCallDenied{ToolCallID: "two"})
	o.acceptEvent(src, turnevent.ToolProgress{ToolCallID: "two"})
	if _, ok := testLiveReadings(t, o, "conv")[liveReadingKey{protocol.TypeToolProgress, "two"}]; ok {
		t.Fatal("late progress revived terminal tool")
	}
	o.acceptEvent(src, turnevent.ToolStart{ToolCallID: "two"})
	o.acceptEvent(src, turnevent.ToolProgress{ToolCallID: "two"})
	if _, ok := testLiveReadings(t, o, "conv")[liveReadingKey{protocol.TypeToolProgress, "two"}]; !ok {
		t.Fatal("new tool start did not release reused identity")
	}

	o.acceptEvent(src, turnevent.TurnEnd{})
	got = testLiveReadings(t, o, "conv")
	if _, ok := got[liveReadingKey{protocol.TypeThinkingProgress, ""}]; ok {
		t.Fatal("thinking survived turn")
	}
	if _, ok := got[liveReadingKey{protocol.TypeBackgroundTaskProgress, "b"}]; !ok {
		t.Fatal("background ended with main turn")
	}
	o.acceptEvent(src, turnevent.BackgroundTaskUpdated{TaskID: "a", Status: "completed"})
	o.acceptEvent(src, turnevent.BackgroundTaskProgress{TaskID: "a"})

	if _, ok := testLiveReadings(t, o, "conv")[liveReadingKey{protocol.TypeBackgroundTaskProgress, "a"}]; ok {
		t.Fatal("terminal task survived")
	}
	o.acceptEvent(src, turnevent.Stall{})
	o.acceptEvent(src, turnevent.TextChunk{Text: "new turn"})
	if _, ok := testLiveReadings(t, o, "conv")[liveReadingKey{protocol.TypeStall, ""}]; ok {
		t.Fatal("activity did not clear stall")
	}
}

func TestDaemonLiveAllFamilies(t *testing.T) {
	events := []turnevent.Event{turnevent.Stall{}, turnevent.ApiRetry{Active: false}, turnevent.Compacting{Active: false}, turnevent.ThinkingProgress{}, turnevent.ToolProgress{ToolCallID: "tool"}, turnevent.BackgroundTaskProgress{TaskID: "task"}, turnevent.RateLimited{}, turnevent.ContextUsage{}, turnevent.ModelAnnounced{}, turnevent.SessionFacts{}, turnevent.MCPStatus{}, turnevent.SlashCommandList{}, turnevent.ModelList{}}
	for i, ev := range events {
		t.Run(streamLiveFamilies[i+1], func(t *testing.T) {
			o := testLiveOwner()
			s := newStreamTurnSink(1, nil)
			s.live = o
			send := s.sinkForTag(func() string { return "a" }, "claude")
			send(ev)
			env := <-s.ch
			if env.live == nil {
				t.Fatal("source capture absent")
			}
			typ := streamLiveFamilies[i+1]
			id := ""
			if typ == protocol.TypeToolProgress {
				id = "tool"
			}
			if typ == protocol.TypeBackgroundTaskProgress {
				id = "task"
			}
			got, ok := testLiveReadings(t, o, "conv")[liveReadingKey{typ, id}]
			if !ok || got.Revision == 0 || got.SessionGeneration == 0 || string(got.Envelope.SessionID) != `"a"` {
				t.Fatalf("family not retained: %+v", got)
			}
		})
	}
}

func TestDaemonLiveMetadataAndBound(t *testing.T) {
	for _, p := range []history.SessionProvenance{{}, {Kind: "none"}, {Kind: "claude", SessionID: strings.Repeat("<", 256)}} {
		t.Run(p.Kind, func(t *testing.T) {
			o := testLiveOwner()
			src := o.capture("a", 1, p, true)
			env := protocol.Envelope{Type: protocol.TypeModelAnnounced, Payload: json.RawMessage(`{}`)}
			r, ok := o.admit(src, env, "")
			if !ok {
				t.Fatal("small envelope rejected")
			}
			want := liveSessionTag(p)
			if string(r.Envelope.SessionID) != string(want) {
				t.Fatal("provenance shape changed")
			}
			// Fill the serialized envelope exactly to its bound using worst-escaping text.
			env.TS = r.Envelope.TS
			r.Envelope.Payload = json.RawMessage(`{"value":""}`)
			overhead, _ := json.Marshal(r.Envelope)
			budget := protocol.MaxThreadEnvelopeBytes - len(overhead)
			env.Payload = json.RawMessage(`{"value":"` + strings.Repeat(`\u003c`, budget/6) + `"}`)
			r, ok = o.admit(src, env, "")
			if !ok {
				t.Fatal("bounded worst escaping rejected")
			}
			raw, _ := json.Marshal(r.Envelope)
			if len(raw) > protocol.MaxThreadEnvelopeBytes {
				t.Fatal("oversized fresh envelope")
			}
			env.Payload = json.RawMessage(`{"value":"` + strings.Repeat("x", protocol.MaxThreadEnvelopeBytes) + `"}`)
			if _, ok := o.admit(src, env, ""); ok {
				t.Fatal("oversized reading retained")
			}
		})
	}
}

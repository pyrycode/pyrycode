package main

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// waitShort bounds every reply wait in this file. Long enough that a scheduler
// hiccup does not redden the suite, short enough that a broken finish path fails
// in a second rather than parking the package's whole run.
const waitShort = 2 * time.Second

// TestWrapUpCapture_AccumulatesPastASaturatedSink is the placement pin, and the
// reason this type is a sink DECORATOR rather than a reader inside the drain.
//
// It is TestSessionResetFollower_ObservesPastASaturatedSink's shape and its
// argument, applied to assistant text: turnMarkFor answers turnMarkOpen for
// TextChunk, so a chunk is droppable class and sinkForTag refuses it at
// droppableCap under load. The len(sink.ch) assertion BEFORE the wait is what
// makes the green a conjunction — "refused at the fan-in, and captured anyway" —
// rather than a coincidence of the sink having quietly queued the chunks.
//
// What it rules out is every capture point on the far side of that channel,
// busy.observe included: a capture there writes a silently TRUNCATED note under
// exactly the load where a reset costs the most, and a truncated note is
// indistinguishable from a short one to everyone downstream.
func TestWrapUpCapture_AccumulatesPastASaturatedSink(t *testing.T) {
	t.Parallel()

	sink := newStreamTurnSink(1, discardLogger())
	tag := newStreamSessionTag(resetFollowSessionA)
	// Fill the one slot so every chunk below is refused at droppableCap.
	sink.sinkForTag(tag.ID)(turnevent.TextChunk{MessageID: "m-0", Text: "filler"})
	if got := len(sink.ch); got != 1 {
		t.Fatalf("fan-in holds %d envelopes after the filler, want 1 (the fixture is not saturated)", got)
	}

	c := newWrapUpCapture(sink.sinkForTag(tag.ID))
	reply, stop, ok := c.begin()
	if !ok {
		t.Fatalf("begin refused on a fresh capture")
	}
	defer stop()

	c.Sink(turnevent.TextChunk{MessageID: "m-1", Text: "handoff "})
	c.Sink(turnevent.TextChunk{MessageID: "m-1", Text: "note"})
	c.Sink(turnevent.TurnEnd{})

	if got := len(sink.ch); got != 1 {
		t.Errorf("fan-in holds %d envelopes, want 1 — the chunks must have been refused, not queued", got)
	}
	text, done := reply.wait(waitContext(t))
	if !done {
		t.Fatalf("wait reported no turn end after TurnEnd was sunk")
	}
	if text != "handoff note" {
		t.Errorf("captured %q, want %q", text, "handoff note")
	}
}

// TestWrapUpCapture_CapturesTextChunksOnly pins AC 1's enumeration: the text
// chunks, and not the thought chunks or the tool events. The negative rows are
// the ones that matter — a handoff note carrying the outgoing session's reasoning
// or a tool's output is not what the successor is owed, and both variants stream
// through this decorator on the same chain.
func TestWrapUpCapture_CapturesTextChunksOnly(t *testing.T) {
	t.Parallel()

	c := newWrapUpCapture(nil)
	reply, stop, ok := c.begin()
	if !ok {
		t.Fatalf("begin refused on a fresh capture")
	}
	defer stop()

	c.Sink(turnevent.ThoughtChunk{MessageID: "m-1", Text: "REASONING"})
	c.Sink(turnevent.TextChunk{MessageID: "m-1", Text: "kept"})
	c.Sink(turnevent.ToolStart{ToolCallID: "t-1", Title: "Read"})
	c.Sink(turnevent.ToolUpdate{ToolCallID: "t-1"})
	c.Sink(turnevent.Stall{})
	c.Sink(turnevent.Banner{})
	c.Sink(turnevent.TurnEnd{})

	text, done := reply.wait(waitContext(t))
	if !done {
		t.Fatalf("wait reported no turn end after TurnEnd was sunk")
	}
	if text != "kept" {
		t.Errorf("captured %q, want only the text chunk %q", text, "kept")
	}
}

// TestWrapUpCapture_ForwardsEveryVariantUnchanged pins the decorator contract each
// link in this chain states: it acts on some events and forwards ALL of them,
// unchanged, including the ones it acted on. Swallowing one here would change what
// the fan-in and the drain observe — a TurnEnd withheld would wedge the
// conversation busy forever, since turnMarkClose is the only thing that clears it.
func TestWrapUpCapture_ForwardsEveryVariantUnchanged(t *testing.T) {
	t.Parallel()

	var got []turnevent.Event
	c := newWrapUpCapture(func(ev turnevent.Event) { got = append(got, ev) })
	want := []turnevent.Event{
		turnevent.TextChunk{MessageID: "m-1", Text: "a"},
		turnevent.ThoughtChunk{MessageID: "m-1", Text: "b"},
		turnevent.ToolStart{ToolCallID: "t-1", Title: "Read"},
		turnevent.TurnEnd{},
	}

	// Once disarmed and once armed: the forwarding must not depend on the arm.
	for _, ev := range want {
		c.Sink(ev)
	}
	_, stop, ok := c.begin()
	if !ok {
		t.Fatalf("begin refused on a fresh capture")
	}
	stop()
	for _, ev := range want {
		c.Sink(ev)
	}

	if len(got) != 2*len(want) {
		t.Fatalf("forwarded %d events, want %d", len(got), 2*len(want))
	}
	for i, ev := range got {
		if !reflect.DeepEqual(ev, want[i%len(want)]) {
			t.Errorf("forwarded[%d] = %#v, want %#v", i, ev, want[i%len(want)])
		}
	}
}

// TestWrapUpCapture_OneArmAtATime pins the refusal AC 4 leans on from below. The
// reset's own in-progress guard is the conversation-level answer; this is the
// runner-level one, and it is what keeps two captures from splitting one turn's
// text between them.
func TestWrapUpCapture_OneArmAtATime(t *testing.T) {
	t.Parallel()

	c := newWrapUpCapture(nil)
	first, stopFirst, ok := c.begin()
	if !ok {
		t.Fatalf("begin refused on a fresh capture")
	}
	if _, _, ok := c.begin(); ok {
		t.Errorf("begin admitted a second arm while one was live")
	}

	// A disarm frees the capture for the next reset, whether the turn ended or not.
	stopFirst()
	second, stopSecond, ok := c.begin()
	if !ok {
		t.Fatalf("begin refused after the first arm was released")
	}
	defer stopSecond()
	if first == second {
		t.Errorf("begin handed back the same reply twice")
	}

	// stop is idempotent: the reset goroutine defers it and the turn-end path may
	// already have finished the reply.
	stopFirst()
	stopFirst()
}

// TestWrapUpCapture_NilCaptureRefuses keeps a hand-built streamRunner{} inert. The
// compile-time interface assertions in this package construct exactly such a
// value, and the production factory is the only thing that supplies the field.
func TestWrapUpCapture_NilCaptureRefuses(t *testing.T) {
	t.Parallel()

	var c *wrapUpCapture
	if _, _, ok := c.begin(); ok {
		t.Errorf("a nil capture admitted an arm")
	}
	// Sinking into a nil capture must not panic either: the field is nil on any
	// runner this package's tests hand-build.
	c.Sink(turnevent.TextChunk{MessageID: "m", Text: "x"})
}

// TestWrapUpCapture_StopsAtTheStoreBound pins the accumulation bound. A note is
// truncated by the store at MaxHandoffNoteBytes anyway, so buffering past it buys
// the successor nothing and lets a runaway child grow the daemon's heap by as much
// as it cares to write.
func TestWrapUpCapture_StopsAtTheStoreBound(t *testing.T) {
	t.Parallel()

	c := newWrapUpCapture(nil)
	reply, stop, ok := c.begin()
	if !ok {
		t.Fatalf("begin refused on a fresh capture")
	}
	defer stop()

	chunk := strings.Repeat("x", 4096)
	for written := 0; written < sessions.MaxHandoffNoteBytes+3*len(chunk); written += len(chunk) {
		c.Sink(turnevent.TextChunk{MessageID: "m-1", Text: chunk})
	}
	c.Sink(turnevent.TurnEnd{})

	text, done := reply.wait(waitContext(t))
	if !done {
		t.Fatalf("wait reported no turn end after TurnEnd was sunk")
	}
	if len(text) > sessions.MaxHandoffNoteBytes {
		t.Errorf("captured %d bytes, want at most the store bound %d", len(text), sessions.MaxHandoffNoteBytes)
	}
	if len(text) == 0 {
		t.Errorf("the bound dropped the whole reply; it must drop the tail, not the note")
	}
}

// TestWrapUpCapture_WaitLeavesOnContext is the deadline path: a child that never
// produces a turn end must not park the reset goroutine past its bound. The reply
// is left unfinished on purpose — the caller's answer is "no reply", and AC 2 says
// the previous note then stands.
func TestWrapUpCapture_WaitLeavesOnContext(t *testing.T) {
	t.Parallel()

	c := newWrapUpCapture(nil)
	reply, stop, ok := c.begin()
	if !ok {
		t.Fatalf("begin refused on a fresh capture")
	}
	defer stop()
	c.Sink(turnevent.TextChunk{MessageID: "m-1", Text: "partial"})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if text, done := reply.wait(ctx); done || text != "" {
		t.Errorf("wait = (%q, %v) on a cancelled context, want (\"\", false)", text, done)
	}
}

// TestWrapUpCapture_ConcurrentSinkAndWait is the -race arm. The writer is
// production's stdout forwarder goroutine running Sink; the reader is the reset
// goroutine waiting on the reply. They are different goroutines by construction —
// that is the whole reason the reset runs off the relay's dispatch goroutine — so
// the hand-off needs a barrier and this test is what says so out loud.
func TestWrapUpCapture_ConcurrentSinkAndWait(t *testing.T) {
	t.Parallel()

	c := newWrapUpCapture(nil)
	reply, stop, ok := c.begin()
	if !ok {
		t.Fatalf("begin refused on a fresh capture")
	}
	defer stop()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			c.Sink(turnevent.TextChunk{MessageID: "m-1", Text: "x"})
		}
		c.Sink(turnevent.TurnEnd{})
	}()

	text, done := reply.wait(waitContext(t))
	wg.Wait()
	if !done {
		t.Fatalf("wait reported no turn end")
	}
	if text != strings.Repeat("x", 500) {
		t.Errorf("captured %d bytes, want 500", len(text))
	}
}

// waitContext bounds a reply wait so a broken finish path fails the test instead
// of parking the package's run until the go-test timeout.
func waitContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), waitShort)
	t.Cleanup(cancel)
	return ctx
}

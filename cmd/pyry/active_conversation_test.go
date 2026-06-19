package main

import (
	"context"
	"sync"
	"testing"

	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// TestActiveConversation_ZeroValueEmpty: before any route the cursor is empty —
// the well-defined "no conversation routed yet" state the emitter drops on
// (#687 AC#4).
func TestActiveConversation_ZeroValueEmpty(t *testing.T) {
	t.Parallel()
	var a activeConversation
	if got := a.CurrentConversation(); got != "" {
		t.Fatalf("zero-value cursor = %q, want empty", got)
	}
}

// TestActiveConversation_SetOverwrites: set records the current conversation and
// a later set overwrites it (last-writer-wins — the intended attribution).
func TestActiveConversation_SetOverwrites(t *testing.T) {
	t.Parallel()
	var a activeConversation
	a.set("conv-x")
	if got := a.CurrentConversation(); got != "conv-x" {
		t.Fatalf("after set(conv-x): cursor = %q, want %q", got, "conv-x")
	}
	a.set("conv-y")
	if got := a.CurrentConversation(); got != "conv-y" {
		t.Fatalf("after set(conv-y): cursor = %q, want %q", got, "conv-y")
	}
}

// TestActiveConversation_ConcurrentSetGetNoRace (AC#3): the holder is written
// from the routing-path goroutine and read from the producer's single Run
// goroutine concurrently. Under `go test -race` this proves the cross-goroutine
// hand-off is race-free, and every observed value is one that was actually set
// (or the empty zero value) — never a torn read.
func TestActiveConversation_ConcurrentSetGetNoRace(t *testing.T) {
	t.Parallel()
	var a activeConversation
	values := []string{"conv-a", "conv-b", "conv-c"}
	allowed := map[string]bool{"": true}
	for _, v := range values {
		allowed[v] = true
	}

	const iters = 1000
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			a.set(values[i%len(values)])
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			if got := a.CurrentConversation(); !allowed[got] {
				t.Errorf("read an unexpected value %q (not set and not empty)", got)
				return
			}
		}
	}()
	wg.Wait()
}

// chanClosed reports whether ch is closed without blocking.
func chanClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// TestActiveConversation_WatchZeroValue (#679): before any route watch returns
// the empty id and a non-nil, open channel (the lazy-init keeps the zero-value
// literal valid); CurrentConversation stays "" (regression on #687).
func TestActiveConversation_WatchZeroValue(t *testing.T) {
	t.Parallel()
	var a activeConversation
	id, changed := a.watch()
	if id != "" {
		t.Fatalf("zero-value watch id = %q, want empty", id)
	}
	if changed == nil {
		t.Fatal("zero-value watch changed channel is nil; a switch could never fire")
	}
	if chanClosed(changed) {
		t.Fatal("zero-value watch channel is already closed; want open")
	}
	if got := a.CurrentConversation(); got != "" {
		t.Fatalf("zero-value cursor = %q, want empty", got)
	}
}

// TestActiveConversation_SameIDDoesNotFire (#679): consecutive messages to the
// SAME conversation must not re-subscribe — the tail stays open and catches each
// turn continuously, as the bootstrap stream did. A repeat set leaves the watched
// channel open.
func TestActiveConversation_SameIDDoesNotFire(t *testing.T) {
	t.Parallel()
	var a activeConversation
	a.set("conv-x")
	_, changed := a.watch()
	a.set("conv-x") // same id — no change
	if chanClosed(changed) {
		t.Fatal("set with the same id fired the switch signal; want no re-subscribe")
	}
}

// TestActiveConversation_DifferentIDFires (#679): a set to a DIFFERENT id closes
// the channel a prior watch captured (so the follow-active subscriber tears down
// and re-subscribes), and a fresh watch then yields the new id + a new open
// channel.
func TestActiveConversation_DifferentIDFires(t *testing.T) {
	t.Parallel()
	var a activeConversation
	a.set("conv-a")
	id, changed := a.watch()
	if id != "conv-a" {
		t.Fatalf("watch id = %q, want conv-a", id)
	}

	a.set("conv-b") // different id — fires
	if !chanClosed(changed) {
		t.Fatal("set with a different id did not close the captured channel; the switch would be missed")
	}

	id2, changed2 := a.watch()
	if id2 != "conv-b" {
		t.Fatalf("post-switch watch id = %q, want conv-b", id2)
	}
	if chanClosed(changed2) {
		t.Fatal("post-switch channel is already closed; want a fresh open channel")
	}
}

// TestActiveConversation_ConcurrentSetWatchNoRace (#679, -race): set on the
// routing goroutine races watch + CurrentConversation on the producer's Run
// goroutine. Proves the close+replace of the change signal is race-free and a
// watched channel is never closed twice.
func TestActiveConversation_ConcurrentSetWatchNoRace(t *testing.T) {
	t.Parallel()
	var a activeConversation
	values := []string{"conv-a", "conv-b", "conv-c"}

	const iters = 1000
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			a.set(values[i%len(values)])
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			_, changed := a.watch()
			// Touch the channel the way the switch watcher does — a non-blocking
			// select. The close+replace under mu must make this safe.
			_ = chanClosed(changed)
			_ = a.CurrentConversation()
		}
	}()
	wg.Wait()
}

// TestActiveConversation_EmitterStampsCursor (AC#2): the production holder
// satisfies cursorReader, so injecting it as the emitter's cursor makes the
// emitted envelope carry the stamped conversation_id — the injection swap this
// ticket relies on (interactive_turn_v2.go:131 reads through the interface).
// An unstamped holder drops every event, exactly as the empty bootstrap cursor
// did after #678.
func TestActiveConversation_EmitterStampsCursor(t *testing.T) {
	t.Parallel()

	t.Run("stamped holder emits with that conversation_id", func(t *testing.T) {
		t.Parallel()
		active := &activeConversation{}
		active.set(testConvID)
		bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
		e := newInteractiveTurnEmitterV2(active, bcast, discardLogger())

		e.Handle(context.Background(), turnevent.TextChunk{Text: "hi"})
		e.flushDelta(context.Background())

		deltas := assistantDeltas(t, bcast.pushes)
		if len(deltas) != 1 {
			t.Fatalf("want 1 assistant_delta, got %d", len(deltas))
		}
		if deltas[0].ConversationID != testConvID {
			t.Fatalf("delta conversation_id = %q, want %q (the stamped active conversation)", deltas[0].ConversationID, testConvID)
		}
	})

	t.Run("unstamped holder drops every event", func(t *testing.T) {
		t.Parallel()
		active := &activeConversation{} // never routed → empty cursor
		bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "a", Interactive: true}}}}
		e := newInteractiveTurnEmitterV2(active, bcast, discardLogger())

		e.Handle(context.Background(), turnevent.TextChunk{Text: "hi"})
		e.flushDelta(context.Background())

		if len(bcast.pushes) != 0 {
			t.Fatalf("unstamped holder pushed %d envelopes; want 0 (drops as before)", len(bcast.pushes))
		}
	})
}

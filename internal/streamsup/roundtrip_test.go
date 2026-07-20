package streamsup

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// TestParser_MultiTurnRoundTripZeroBleed is the headline integration test (AC1
// stdin-stays-open + AC3 zero cross-turn bleed). It composes the two seams the
// #1087 Runner exposes — WriteTurn onto Runner.Stdin(), and a Parser wired as
// Config.Stdout — against the stream_json fake child, and drives three turns
// with distinct per-turn markers over ONE held-open stdin handle.
//
// Driving turn-by-turn (WriteTurn, drain to TurnEnd, WriteTurn next) makes the
// attribution assertion race-free: turn N's events have all arrived (terminated
// by result→TurnEnd) before turn N+1's envelope is written.
func TestParser_MultiTurnRoundTripZeroBleed(t *testing.T) {
	t.Parallel()

	events := make(chan turnevent.Event, 128)
	parser := NewParser(func(ev turnevent.Event) { events <- ev }, discardLogger())

	stderr := &safeBuffer{}
	cfg := helperRunCfg(t, "stream_json", &safeBuffer{}, stderr)
	cfg.Stdout = parser // the parser is the terminal stdout sink, not a buffer

	spawned := make(chan struct{}, 1)
	cfg.onSpawn = func(int) {
		select {
		case spawned <- struct{}{}:
		default:
		}
	}

	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cancel, join := runInBackground(t, r)
	defer func() { cancel(); join() }()

	select {
	case <-spawned:
	case <-time.After(5 * time.Second):
		t.Fatal("child never spawned")
	}

	// Capture the held-open stdin ONCE and reuse it across every turn: no
	// re-open, no half-close, no per-turn stdin lifecycle (AC1).
	w := r.Stdin()
	if w == nil {
		t.Fatal("Stdin() is nil while the child is live")
	}

	markers := []string{"turn-alpha", "turn-bravo", "turn-charlie"}
	for i, marker := range markers {
		if err := WriteTurn(context.Background(), w, []byte(marker)); err != nil {
			t.Fatalf("turn %d WriteTurn: %v", i+1, err)
		}
		seg := drainToTurnEnd(t, events)
		assertSegment(t, i+1, marker, markers, seg)
	}
}

// drainToTurnEnd collects events off the channel up to and including the first
// TurnEnd, returning the whole segment.
func drainToTurnEnd(t *testing.T, events <-chan turnevent.Event) []turnevent.Event {
	t.Helper()
	var seg []turnevent.Event
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev := <-events:
			seg = append(seg, ev)
			if _, ok := ev.(turnevent.TurnEnd); ok {
				return seg
			}
		case <-timeout:
			t.Fatalf("timed out waiting for TurnEnd; collected %#v", seg)
			return seg
		}
	}
}

// assertSegment verifies one turn's event segment: exactly [TextChunk, TurnEnd]
// (the per-turn system/init emitted nothing and did not split the turn early),
// the text carries this turn's marker, and no sibling turn's marker (zero bleed).
func assertSegment(t *testing.T, turn int, marker string, allMarkers []string, seg []turnevent.Event) {
	t.Helper()
	if len(seg) != 2 {
		t.Fatalf("turn %d: got %d events, want 2 ([TextChunk, TurnEnd]); a per-turn init must not emit or split the turn: %#v", turn, len(seg), seg)
	}
	tc, ok := seg[0].(turnevent.TextChunk)
	if !ok {
		t.Fatalf("turn %d: first event = %#v, want TextChunk", turn, seg[0])
	}
	if _, ok := seg[1].(turnevent.TurnEnd); !ok {
		t.Fatalf("turn %d: last event = %#v, want TurnEnd", turn, seg[1])
	}
	if tc.Text != marker {
		t.Fatalf("turn %d: TextChunk.Text = %q, want this turn's marker %q", turn, tc.Text, marker)
	}
	for _, other := range allMarkers {
		if other == marker {
			continue
		}
		if strings.Contains(tc.Text, other) {
			t.Fatalf("turn %d: TextChunk carries sibling marker %q — cross-turn bleed: %q", turn, other, tc.Text)
		}
	}
}

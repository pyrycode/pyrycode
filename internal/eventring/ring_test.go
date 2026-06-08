package eventring

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// appendN appends n control turn_state events to convID and returns the ring.
func appendControl(t *testing.T, r *Ring, convID string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		r.Append(convID, protocol.TypeTurnState, nil, time.Unix(int64(i), 0))
	}
}

func eventIDs(evs []Event) []uint64 {
	out := make([]uint64, len(evs))
	for i, e := range evs {
		out[i] = e.ID
	}
	return out
}

func equalU64(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestNew_PanicsOnNonPositiveBound(t *testing.T) {
	t.Parallel()
	for _, n := range []int{0, -1} {
		func() {
			defer func() {
				if recover() == nil {
					t.Fatalf("New(%d) did not panic", n)
				}
			}()
			_ = New(n)
		}()
	}
}

// AC-1: ids strictly increase from 1 within a conversation; conversations have
// independent counters.
func TestAppend_IDsStrictlyIncreasePerConversation(t *testing.T) {
	t.Parallel()
	r := New(MaxEventsPerConversation)

	var gotA []uint64
	for i := 0; i < 3; i++ {
		gotA = append(gotA, r.Append("A", protocol.TypeTurnState, nil, time.Unix(int64(i), 0)))
	}
	if want := []uint64{1, 2, 3}; !equalU64(gotA, want) {
		t.Fatalf("conv A ids: got %v, want %v", gotA, want)
	}

	// A second conversation starts its own counter at 1.
	idB := r.Append("B", protocol.TypeTurnState, nil, time.Unix(0, 0))
	if idB != 1 {
		t.Fatalf("conv B first id: got %d, want 1 (independent counter)", idB)
	}
}

// AC-4: After(convID, afterID) returns retained events with id > afterID in
// ascending order; After(_, 0) returns all retained.
func TestAfter_ReplayReturnsEventsAfterID(t *testing.T) {
	t.Parallel()
	r := New(MaxEventsPerConversation)
	appendControl(t, r, "A", 5) // ids 1..5

	got, gap := r.After("A", 2)
	if gap {
		t.Fatal("After(A, 2): unexpected gap")
	}
	if want := []uint64{3, 4, 5}; !equalU64(eventIDs(got), want) {
		t.Fatalf("After(A, 2): got ids %v, want %v", eventIDs(got), want)
	}

	all, gap := r.After("A", 0)
	if gap {
		t.Fatal("After(A, 0): unexpected gap")
	}
	if want := []uint64{1, 2, 3, 4, 5}; !equalU64(eventIDs(all), want) {
		t.Fatalf("After(A, 0): got ids %v, want %v", eventIDs(all), want)
	}
}

// AC-5: a query at or past the latest id is caught up — distinguishable from a
// gap by gap==false with empty events.
func TestAfter_CaughtUp(t *testing.T) {
	t.Parallel()
	r := New(MaxEventsPerConversation)
	appendControl(t, r, "A", 5) // ids 1..5

	for _, afterID := range []uint64{5, 99} {
		got, gap := r.After("A", afterID)
		if gap {
			t.Fatalf("After(A, %d): want caught up (gap=false), got gap=true", afterID)
		}
		if len(got) != 0 {
			t.Fatalf("After(A, %d): want no events, got %v", afterID, eventIDs(got))
		}
	}
}

// AC-5: when the consumer's next-expected event has fallen off the back, After
// reports a gap (distinguishable from caught-up).
func TestAfter_GapWhenOldestFellOff(t *testing.T) {
	t.Parallel()
	r := New(3)
	appendControl(t, r, "A", 6) // cap 3 → retained ids 4,5,6; 1..3 evicted

	got, gap := r.After("A", 1) // next-expected (2) is below the oldest retained (4)
	if !gap {
		t.Fatalf("After(A, 1): want gap=true (missed events), got events %v gap=false", eventIDs(got))
	}
	if len(got) != 0 {
		t.Fatalf("After(A, 1) gap: want no events, got %v", eventIDs(got))
	}

	// Distinguishable from caught-up.
	_, caughtUp := r.After("A", 6)
	if caughtUp {
		t.Fatal("After(A, 6): want caught up (gap=false)")
	}
}

// AC-4: After never returns another conversation's events.
func TestAfter_ConversationIsolation(t *testing.T) {
	t.Parallel()
	r := New(MaxEventsPerConversation)
	r.Append("A", protocol.TypeTurnState, json.RawMessage(`"a"`), time.Unix(0, 0))
	r.Append("B", protocol.TypeTurnState, json.RawMessage(`"b"`), time.Unix(0, 0))

	a, _ := r.After("A", 0)
	if len(a) != 1 || string(a[0].Payload) != `"a"` {
		t.Fatalf("After(A, 0): got %d events, want 1 belonging to A", len(a))
	}
	b, _ := r.After("B", 0)
	if len(b) != 1 || string(b[0].Payload) != `"b"` {
		t.Fatalf("After(B, 0): got %d events, want 1 belonging to B", len(b))
	}
}

// AC-5: an unknown conversation distinguishes a fresh consumer from one
// referencing events the daemon never had.
func TestAfter_UnknownConversation(t *testing.T) {
	t.Parallel()
	r := New(MaxEventsPerConversation)

	got, gap := r.After("never-seen", 0)
	if gap || len(got) != 0 {
		t.Fatalf("After(never-seen, 0): want caught up (nil,false), got events=%v gap=%v", eventIDs(got), gap)
	}
	got, gap = r.After("never-seen", 5)
	if !gap || len(got) != 0 {
		t.Fatalf("After(never-seen, 5): want gap (nil,true), got events=%v gap=%v", eventIDs(got), gap)
	}
}

// AC-3: when the bound is reached, the oldest assistant_delta is evicted first;
// every control event is retained.
func TestAppend_EvictsDeltasFirst(t *testing.T) {
	t.Parallel()
	const cap = 6
	r := New(cap)

	// Fill with a mix so deltas exceed the headroom once we push past cap.
	// Sequence (10 appends, ids 1..10): control, delta, control, delta,
	// control, delta, control, delta, control, delta — 5 control, 5 delta.
	controlTypes := []string{
		protocol.TypeTurnState,
		protocol.TypeToolUse,
		protocol.TypeToolResult,
		protocol.TypeTurnEnd,
		protocol.TypeStall,
	}
	ci := 0
	for i := 0; i < 10; i++ {
		if i%2 == 0 {
			r.Append("A", controlTypes[ci], nil, time.Unix(int64(i), 0))
			ci++
		} else {
			r.Append("A", protocol.TypeAssistantDelta, nil, time.Unix(int64(i), 0))
		}
	}

	got, gap := r.After("A", 0)
	if gap {
		t.Fatalf("unexpected gap; events %v", eventIDs(got))
	}
	if len(got) != cap {
		t.Fatalf("retained %d events, want exactly cap=%d", len(got), cap)
	}

	// All five control events (ids 1,3,5,7,9) must be retained; the dropped
	// four must all be assistant_delta.
	retainedControl := 0
	for _, e := range got {
		if e.Type != protocol.TypeAssistantDelta {
			retainedControl++
		}
	}
	if retainedControl != len(controlTypes) {
		t.Fatalf("retained %d control events, want all %d (control retained over deltas)", retainedControl, len(controlTypes))
	}
}

// AC-2: under all-control pressure the hard bound still holds — the oldest
// control events are dropped, newest retained, ascending order preserved.
func TestAppend_AllControlHardBound(t *testing.T) {
	t.Parallel()
	const cap = 4
	r := New(cap)
	appendControl(t, r, "A", cap+3) // ids 1..7; only 4..7 fit

	// Querying from the eviction boundary returns exactly the retained tail,
	// ascending, with no fabricated gap.
	got, gap := r.After("A", 3)
	if gap {
		t.Fatalf("After(A, 3): unexpected gap; events %v", eventIDs(got))
	}
	if want := []uint64{4, 5, 6, 7}; !equalU64(eventIDs(got), want) {
		t.Fatalf("all-control eviction: got ids %v, want %v (oldest dropped, ascending)", eventIDs(got), want)
	}
	// A from-the-start query reports a gap — ids 1..3 were dropped to honour
	// the hard bound, so the count never exceeds cap.
	if _, gap := r.After("A", 0); !gap {
		t.Fatal("After(A, 0): want gap=true (ids 1..3 evicted under the hard bound)")
	}
}

// AC-5 (no fabricated gap): a delta evicted from the middle while an older
// event is retained is not a back-of-ring gap.
func TestAfter_MiddleDeltaEvictionNoGap(t *testing.T) {
	t.Parallel()
	const cap = 3
	r := New(cap)
	r.Append("A", protocol.TypeTurnState, nil, time.Unix(0, 0))      // id 1 (control, retained)
	r.Append("A", protocol.TypeAssistantDelta, nil, time.Unix(1, 0)) // id 2 (delta)
	r.Append("A", protocol.TypeTurnState, nil, time.Unix(2, 0))      // id 3 (control)
	r.Append("A", protocol.TypeTurnState, nil, time.Unix(3, 0))      // id 4 — evicts the oldest delta (id 2)

	// Retained {1,3,4}: the delta in the middle is gone, but id 1 is still here.
	all, gap := r.After("A", 0)
	if gap {
		t.Fatalf("After(A, 0): unexpected gap; events %v", eventIDs(all))
	}
	if want := []uint64{1, 3, 4}; !equalU64(eventIDs(all), want) {
		t.Fatalf("retained ids: got %v, want %v", eventIDs(all), want)
	}

	got, gap := r.After("A", 1)
	if gap {
		t.Fatal("After(A, 1): a missing middle delta must not fabricate a gap")
	}
	if want := []uint64{3, 4}; !equalU64(eventIDs(got), want) {
		t.Fatalf("After(A, 1): got ids %v, want %v", eventIDs(got), want)
	}
}

// New(1) is the degenerate cap-1 ring: every append after the first evicts the
// prior, and only the latest event is retained.
func TestAppend_CapOne(t *testing.T) {
	t.Parallel()
	r := New(1)
	appendControl(t, r, "A", 3) // ids 1..3; only id 3 retained

	got, gap := r.After("A", 0) // afterID 0 < latest 3, oldest retained 3 > 1 → gap
	if !gap {
		t.Fatalf("cap-1 After(A, 0): want gap (missed 1,2), got events %v", eventIDs(got))
	}
	got, gap = r.After("A", 2) // next-expected 3 == oldest retained → replay, no gap
	if gap {
		t.Fatalf("cap-1 After(A, 2): unexpected gap")
	}
	if want := []uint64{3}; !equalU64(eventIDs(got), want) {
		t.Fatalf("cap-1 After(A, 2): got ids %v, want %v", eventIDs(got), want)
	}
}

// -race: concurrent Append and After must not race or panic.
func TestRing_ConcurrentAppendAfter(t *testing.T) {
	t.Parallel()
	r := New(64)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			r.Append("A", protocol.TypeAssistantDelta, nil, time.Unix(int64(i), 0))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			_, _ = r.After("A", uint64(i))
		}
	}()
	wg.Wait()
}

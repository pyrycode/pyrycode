package eventring

import (
	"encoding/json"
	"math"
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

// #2022 AC-1: ids are unique daemon-wide. One counter serves the whole ring, so
// no conversation can ever be assigned an id another conversation already holds,
// and ids strictly increase in ring-wide append order however the conversations
// are interleaved. This inverts the pre-#2022 assertion that each conversation
// starts its own counter at 1 — that independence is exactly what let a
// per-connection replay watermark taken for one conversation mute another's live
// stream. Retention stays per-conversation; only the id space is shared.
func TestAppend_IDsUniqueAndIncreasingRingWide(t *testing.T) {
	t.Parallel()
	r := New(MaxEventsPerConversation)

	// Interleave rather than appending per conversation in blocks: a block order
	// is the one shape a per-conversation counter would also pass.
	order := []string{"A", "B", "A", "C", "B", "A", "C", "C", "B"}
	var all []uint64
	perConv := map[string][]uint64{}
	for i, convID := range order {
		id := r.Append(convID, protocol.TypeTurnState, nil, time.Unix(int64(i), 0))
		all = append(all, id)
		perConv[convID] = append(perConv[convID], id)
	}

	if want := []uint64{1, 2, 3, 4, 5, 6, 7, 8, 9}; !equalU64(all, want) {
		t.Fatalf("ring-wide append order ids: got %v, want %v (one counter, no restart per conversation)", all, want)
	}
	seen := map[uint64]string{}
	for i, id := range all {
		if id < 1 {
			t.Errorf("append %d (conv %s): id %d, want >= 1", i, order[i], id)
		}
		if prev, dup := seen[id]; dup {
			t.Errorf("id %d assigned to both conv %s and conv %s — ids must be unique across conversations", id, prev, order[i])
		}
		seen[id] = order[i]
	}
	// Each conversation's own ids remain strictly increasing (a subsequence of a
	// strictly increasing sequence), just no longer contiguous.
	for convID, ids := range perConv {
		for i := 1; i < len(ids); i++ {
			if ids[i] <= ids[i-1] {
				t.Errorf("conv %s ids %v: not strictly increasing at index %d", convID, ids, i)
			}
		}
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

// AC-5: a query at — exactly at, not beyond (#1494) — the latest id is caught
// up, distinguishable from a gap by gap==false with empty events.
func TestAfter_CaughtUp(t *testing.T) {
	t.Parallel()
	r := New(MaxEventsPerConversation)
	appendControl(t, r, "A", 5) // ids 1..5

	got, gap := r.After("A", 5)
	if gap {
		t.Fatalf("After(A, 5): want caught up (gap=false), got gap=true")
	}
	if len(got) != 0 {
		t.Fatalf("After(A, 5): want no events, got %v", eventIDs(got))
	}
}

// #1494: an afterID past the latest id assigned to this conversation names an
// event this daemon never issued to it — the post-daemon-restart shape, where
// the ring-wide counter restarts at 1 while the phone still holds a high cursor,
// and since #2022 also the rotation shape, where the cursor came from a
// conversation the daemon has left and sits above the new one's high-water
// mark. It is a gap (the
// consumer must resync), never caught-up: classifying it caught-up leaves the
// phone dedup'ing every live event against a cursor the daemon can never reach.
func TestAfter_GapBeyondIDSpace(t *testing.T) {
	t.Parallel()
	r := New(MaxEventsPerConversation)
	appendControl(t, r, "A", 5) // ids 1..5; latest is 5

	for _, afterID := range []uint64{6, 99, math.MaxUint64} {
		got, gap := r.After("A", afterID)
		if !gap {
			t.Errorf("After(A, %d): want gap=true (an id beyond the ring's id space), got gap=false", afterID)
		}
		if len(got) != 0 {
			t.Errorf("After(A, %d): want no events, got %v", afterID, eventIDs(got))
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

// #663: NewestID surfaces the newest retained id (nextID-1) so replayMissed can
// clamp the caught-up watermark to min(afterID, NewestID) — an unknown
// conversation is 0, advancing once per Append independent of retention.
func TestNewestID(t *testing.T) {
	t.Parallel()

	t.Run("unknown conversation is zero", func(t *testing.T) {
		t.Parallel()
		r := New(MaxEventsPerConversation)
		if got := r.NewestID("never-seen"); got != 0 {
			t.Fatalf("NewestID(never-seen) = %d, want 0", got)
		}
	})

	t.Run("tracks the last assigned id", func(t *testing.T) {
		t.Parallel()
		r := New(MaxEventsPerConversation)
		appendControl(t, r, "A", 5) // ids 1..5
		if got := r.NewestID("A"); got != 5 {
			t.Fatalf("NewestID(A) = %d, want 5", got)
		}
	})

	t.Run("advances past eviction (the clamp's load-bearing property)", func(t *testing.T) {
		t.Parallel()
		r := New(2)
		appendControl(t, r, "A", 5) // cap 2 → only ids 4,5 retained, counter at 5
		if got := r.NewestID("A"); got != 5 {
			t.Fatalf("NewestID(A) = %d, want 5 (counter advances independent of retention)", got)
		}
	})

	// #2022: each conversation still reports its OWN highest assigned id — that
	// per-conversation view is what the #663 clamp reads — but the ids now come
	// from the ring-wide counter, so B's do not restart at 1 behind A's.
	t.Run("each conversation reports its own highest assigned id", func(t *testing.T) {
		t.Parallel()
		r := New(MaxEventsPerConversation)
		appendControl(t, r, "A", 3) // ids 1..3
		appendControl(t, r, "B", 7) // ids 4..10
		if got := r.NewestID("A"); got != 3 {
			t.Errorf("NewestID(A) = %d, want 3", got)
		}
		if got := r.NewestID("B"); got != 10 {
			t.Errorf("NewestID(B) = %d, want 10 (ring-wide counter, not a per-conversation restart)", got)
		}
		// Appending to A again resumes from the ring-wide counter, above B's.
		if got := r.Append("A", protocol.TypeTurnState, nil, time.Unix(0, 0)); got != 11 {
			t.Errorf("next A id = %d, want 11", got)
		}
	})
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

// afterCase is one row of an After classification table: the cursor, the ids
// expected back, and the expected gap verdict.
type afterCase struct {
	name    string
	afterID uint64
	want    []uint64
	wantGap bool
}

func runAfterCases(t *testing.T, r *Ring, convID string, cases []afterCase) {
	t.Helper()
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, gap := r.After(convID, tc.afterID)
			if gap != tc.wantGap {
				t.Fatalf("After(%s, %d): gap = %v, want %v (events %v)", convID, tc.afterID, gap, tc.wantGap, eventIDs(got))
			}
			if !equalU64(eventIDs(got), tc.want) {
				t.Fatalf("After(%s, %d): ids %v, want %v", convID, tc.afterID, eventIDs(got), tc.want)
			}
		})
	}
}

// #2022 AC-3: with ids assigned ring-wide, a conversation's own ids are sparse
// and its earliest id is normally well above 1. After must classify the aged-out
// case against what was actually evicted from the back of the window, not infer
// it from contiguity: the pre-#2022 `events[0].ID > afterID+1` test reads a
// sparse-but-complete window as a gap, and a spurious gap is a spurious resync —
// a full client reload for a connection that needed none.
func TestAfter_SparseIDSpace(t *testing.T) {
	t.Parallel()

	t.Run("nothing evicted", func(t *testing.T) {
		t.Parallel()
		r := New(MaxEventsPerConversation)
		appendControl(t, r, "pad", 40) // ids 1..40 belong to another conversation
		for i := 0; i < 3; i++ {       // B takes 41, 43, 45; pad takes 42, 44, 46
			r.Append("B", protocol.TypeTurnState, nil, time.Unix(int64(i), 0))
			r.Append("pad", protocol.TypeTurnState, nil, time.Unix(int64(i), 0))
		}
		runAfterCases(t, r, "B", []afterCase{
			// The AC-3 case: B's whole history is retained, so a fresh cursor
			// replays all of it. Contiguity inference called this a gap.
			{"fresh cursor replays the whole retained history", 0, []uint64{41, 43, 45}, false},
			{"at this conversation's earliest id", 41, []uint64{43, 45}, false},
			{"at another conversation's id inside the window", 42, []uint64{43, 45}, false},
			{"one below the latest", 44, []uint64{45}, false},
			{"exactly at the latest is caught up", 45, nil, false},
			{"just past the latest is a gap", 46, nil, true},
			{"hostile max uint64 is a gap", math.MaxUint64, nil, true},
		})
	})

	t.Run("front eviction", func(t *testing.T) {
		t.Parallel()
		r := New(2)
		appendControl(t, r, "pad", 3)                                 // pad takes ids 1..3
		r.Append("B", protocol.TypeTurnState, nil, time.Unix(0, 0))   // 4
		r.Append("pad", protocol.TypeTurnState, nil, time.Unix(0, 0)) // 5
		r.Append("B", protocol.TypeTurnState, nil, time.Unix(1, 0))   // 6
		r.Append("pad", protocol.TypeTurnState, nil, time.Unix(1, 0)) // 7
		r.Append("B", protocol.TypeTurnState, nil, time.Unix(2, 0))   // 8 — B at cap: evicts id 4
		runAfterCases(t, r, "B", []afterCase{
			// The consumer holds id 4 itself; everything B emitted after it is
			// still retained, so its eviction costs this consumer nothing.
			{"at the evicted id is replayable", 4, []uint64{6, 8}, false},
			{"below the evicted id is a gap", 3, nil, true},
			{"fresh cursor is a gap", 0, nil, true},
			{"at another conversation's id above the eviction boundary", 5, []uint64{6, 8}, false},
			{"caught up at the latest", 8, nil, false},
		})
	})
}

// #2022: the eviction bookkeeping distinguishes a front removal (the window's
// back edge moved — the consumer may have missed something) from a middle delta
// removal (older events still retained — never a gap, per the ring's contract).
// This fixture does both in sequence, which is the one single-conversation case
// where classification differs from the pre-#2022 contiguity inference: once the
// front slides past a middle-evicted delta's hole, the old test could no longer
// tell the hole from a back-edge loss and reported a gap for a cursor that was
// still perfectly replayable.
func TestAfter_FrontEvictionAfterMiddleDeltaDrop(t *testing.T) {
	t.Parallel()
	r := New(3)
	r.Append("A", protocol.TypeTurnState, nil, time.Unix(0, 0))      // id 1 (control)
	r.Append("A", protocol.TypeAssistantDelta, nil, time.Unix(1, 0)) // id 2 (delta)
	r.Append("A", protocol.TypeTurnState, nil, time.Unix(2, 0))      // id 3 (control)
	r.Append("A", protocol.TypeTurnState, nil, time.Unix(3, 0))      // id 4 — evicts delta 2 from the MIDDLE
	r.Append("A", protocol.TypeTurnState, nil, time.Unix(4, 0))      // id 5 — no delta left: evicts id 1 from the FRONT

	runAfterCases(t, r, "A", []afterCase{
		// Only the middle-evicted delta 2 is missing above the cursor, and a
		// missing delta is not a gap.
		{"cursor at the front-evicted id replays the rest", 1, []uint64{3, 4, 5}, false},
		// Id 1 itself is gone, so this consumer genuinely missed a control event.
		{"cursor below the front-evicted id is a gap", 0, nil, true},
		{"caught up at the latest", 5, nil, false},
	})
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

// TestDrop pins #1502's removal contract: a dropped conversation reads exactly
// like an unknown one, every other conversation's retained events are
// untouched, and the ring-wide id counter is not reset — the next Append,
// including one for the dropped conversation, is issued an id above every id
// issued before (#2022's watermark safety depends on ids never repeating).
func TestDrop(t *testing.T) {
	r := New(8)
	appendControl(t, r, "gone", 3)
	appendControl(t, r, "kept", 2)
	appendControl(t, r, "gone", 1)

	keptBefore, _ := r.After("kept", 0)
	keptNewest := r.NewestID("kept")
	goneNewest := r.NewestID("gone")
	highest := max(keptNewest, goneNewest)

	r.Drop("gone")

	if got := r.NewestID("gone"); got != 0 {
		t.Errorf("NewestID(dropped) = %d, want 0", got)
	}
	if evs, gap := r.After("gone", 0); evs != nil || gap {
		t.Errorf("After(dropped, 0) = (%v, %v), want (nil, false)", eventIDs(evs), gap)
	}
	if evs, gap := r.After("gone", goneNewest); evs != nil || !gap {
		t.Errorf("After(dropped, %d) = (%v, %v), want (nil, true): a cursor naming a removed conversation is a gap", goneNewest, eventIDs(evs), gap)
	}

	keptAfter, gap := r.After("kept", 0)
	if gap || !equalU64(eventIDs(keptAfter), eventIDs(keptBefore)) {
		t.Errorf("After(kept, 0) = (%v, %v), want (%v, false)", eventIDs(keptAfter), gap, eventIDs(keptBefore))
	}
	if got := r.NewestID("kept"); got != keptNewest {
		t.Errorf("NewestID(kept) = %d, want %d", got, keptNewest)
	}

	// Unknown conversation: a no-op that leaves every retained event in place.
	r.Drop("never-seen")
	if evs, _ := r.After("kept", 0); !equalU64(eventIDs(evs), eventIDs(keptBefore)) {
		t.Errorf("Drop(unknown) changed kept: got %v, want %v", eventIDs(evs), eventIDs(keptBefore))
	}

	// The counter survives the drop, for the dropped conversation too.
	reID := r.Append("gone", protocol.TypeTurnState, nil, time.Unix(100, 0))
	if reID <= highest {
		t.Errorf("Append(dropped) after Drop = id %d, want > %d (ids must never repeat)", reID, highest)
	}
	if evs, gap := r.After("gone", 0); gap || !equalU64(eventIDs(evs), []uint64{reID}) {
		t.Errorf("After(re-appended, 0) = (%v, %v), want ([%d], false): no pre-drop event may survive", eventIDs(evs), gap, reID)
	}
	if id := r.Append("kept", protocol.TypeTurnState, nil, time.Unix(101, 0)); id <= reID {
		t.Errorf("Append(kept) = id %d, want > %d", id, reID)
	}
}

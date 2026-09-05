package main

import (
	"reflect"
	"sync"
	"testing"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// holdModelWindowsFixture builds a TurnEnd carrying a USABLE window report: one entry
// per id, each with a distinguishable window, plus a non-zero DroppedModelWindows so a
// retention that lost this variant's only truncation report is visible.
//
// It carries a Reason as well, and that is not decoration: Reason is turn-specific and
// must NOT be retained, so every fixture has to carry one for the tests below to be able
// to show that it does not come back out.
//
// The name is prefixed by its consumer rather than taking the obvious bare one. cmd/pyry
// is one package across many files, so a package-level fixture builder collides silently
// with a sibling's — the collision session_background_task_hold_test.go's own fixture doc
// records.
func holdModelWindowsFixture(ids ...string) turnevent.TurnEnd {
	windows := make([]turnevent.ModelWindow, 0, len(ids))
	for i, id := range ids {
		windows = append(windows, turnevent.ModelWindow{
			ModelID:      id,
			WindowTokens: 200000 * (i + 1),
		})
	}
	return turnevent.TurnEnd{
		Reason:              turnevent.TurnEndReasonEndTurn,
		ModelWindows:        windows,
		DroppedModelWindows: 3,
	}
}

// TestSessionModelWindowHold_RetainsAndForwards asserts both halves of Sink's contract in
// one test: the report is retained AND the event still reaches the downstream sink
// unchanged. Asserting the forward here rather than only by the absence of a swallow is
// what keeps a "retain and drop" implementation from passing.
//
// The ENTRIES are asserted in order and by value, not merely by count: AC 1 asks for the
// same ids with the same window values in the same order, and the producer's sort is the
// only thing that makes that order meaningful.
//
// Dropped is asserted alongside them because it is this variant's ONLY truncation report
// — there is no per-entry TruncatedFields on turnevent.ModelWindow — so a retention that
// kept the entries and lost the count would let a capped report be read as a whole one.
//
// The forwarded event is compared with reflect.DeepEqual rather than ==: turnevent.TurnEnd
// stopped being comparable at #2101, so == on the interface would panic at runtime rather
// than compare.
func TestSessionModelWindowHold_RetainsAndForwards(t *testing.T) {
	t.Parallel()

	var next recordingSink
	hold := newSessionModelWindowHold(next.sink)
	want := holdModelWindowsFixture("claude-haiku-4-5", "claude-sonnet-5")

	hold.Sink(want)

	got, ok := hold.ModelWindows()
	if !ok {
		t.Fatalf("ModelWindows() ok = false after one usable report, want true")
	}
	if !reflect.DeepEqual(got.Windows, want.ModelWindows) {
		t.Errorf("retained entries = %#v, want the reported %#v", got.Windows, want.ModelWindows)
	}
	if got.Dropped != want.DroppedModelWindows {
		t.Errorf("retained Dropped = %d, want the reported %d", got.Dropped, want.DroppedModelWindows)
	}

	seen := next.events()
	if len(seen) != 1 {
		t.Fatalf("downstream saw %d events, want 1", len(seen))
	}
	if !reflect.DeepEqual(seen[0], turnevent.Event(want)) {
		t.Errorf("forwarded event = %#v, want the event unchanged", seen[0])
	}
}

// TestSessionModelWindowHold_UnreportedIsItsOwnState pins the first half of AC 3. The
// assertion is on the bool alone, and here that is the only spelling available: #2101
// drops every unusable entry and reports nil for a map whose entries were ALL dropped, so
// there is no reported-but-empty state to tell apart from this one — the departure from
// sessionBackgroundTaskHold, where an empty roster is a positive statement claude really
// makes.
func TestSessionModelWindowHold_UnreportedIsItsOwnState(t *testing.T) {
	t.Parallel()

	hold := newSessionModelWindowHold(nil)
	if _, ok := hold.ModelWindows(); ok {
		t.Errorf("a fresh hold reports ok = true, want false — nothing usable has been reported")
	}
}

// TestSessionModelWindowHold_SilentTurnIsANoOp pins AC 2's second half and is the arm no
// sibling in this family has. #2101 collapses five "claude said nothing usable" shapes to
// a nil ModelWindows, so a result line without a usable modelUsage is claude saying
// NOTHING ABOUT WINDOWS, not claude saying the window changed. Erasing on silence would
// make a reader flicker between the true window and a fallback across turns.
//
// That is a deliberate departure from sessionBackgroundTaskHold.Sink's unconditional
// replace, which is correct THERE because an empty roster is a positive statement and an
// absent window map is not.
//
// Each row runs twice, and the pair is what makes the test complete. On a FRESH hold the
// silent turn must leave the unreported state, which an implementation storing whatever it
// is handed gets wrong; AFTER a usable report it must leave that report byte-for-byte
// intact, which is the arm an unconditional-replace mutant fails. Dropped is asserted in
// the second mode alongside the entries, so a partial store that keeps the entries and
// overwrites the count is caught too — the nil-windows-with-a-count row exists precisely
// to make that mutant reachable.
func TestSessionModelWindowHold_SilentTurnIsANoOp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		silent turnevent.TurnEnd
	}{
		{
			"claude sent no modelUsage at all",
			turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn},
		},
		{
			// The producer's "claude sent entries and none was usable" shape: nil
			// entries with a non-zero count. A count names no model and no window, so
			// there is nothing here for a consumer to join on and it is still silence.
			"claude sent entries and none survived",
			turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn, DroppedModelWindows: 7},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fresh := newSessionModelWindowHold(nil)
			fresh.Sink(tt.silent)
			if _, ok := fresh.ModelWindows(); ok {
				t.Errorf("a silent turn on a fresh hold reports ok = true, want false — "+
					"claude said nothing about windows (%#v)", tt.silent)
			}

			retained := newSessionModelWindowHold(nil)
			want := holdModelWindowsFixture("claude-sonnet-5")
			retained.Sink(want)
			retained.Sink(tt.silent)

			got, ok := retained.ModelWindows()
			if !ok {
				t.Fatalf("ModelWindows() ok = false after a usable report followed by a silent turn, " +
					"want true — silence must not erase what is retained")
			}
			if !reflect.DeepEqual(got.Windows, want.ModelWindows) {
				t.Errorf("retained entries = %#v after a silent turn, want the earlier report's %#v",
					got.Windows, want.ModelWindows)
			}
			if got.Dropped != want.DroppedModelWindows {
				t.Errorf("retained Dropped = %d after a silent turn, want the earlier report's %d — "+
					"the two kept fields are one report and must not be updated apart",
					got.Dropped, want.DroppedModelWindows)
			}
		})
	}
}

// TestSessionModelWindowHold_SecondUsableReportReplaces pins AC 2's first half: a later
// usable report supersedes the earlier one WHOLE, with no merge and no accumulation.
//
// The count assertion is what separates replacement from union-by-id, and the fixture is
// built so the two are distinguishable: the second report names one model the first did
// not and drops one the first had, so a merge would answer three entries where a replace
// answers two. An id present in BOTH reports is what makes the merge tempting to write and
// the count the only thing that catches it.
//
// Dropped is asserted too: it belongs to the report that won, not to whichever report last
// carried a non-zero one.
func TestSessionModelWindowHold_SecondUsableReportReplaces(t *testing.T) {
	t.Parallel()

	hold := newSessionModelWindowHold(nil)
	hold.Sink(holdModelWindowsFixture("claude-haiku-4-5", "claude-opus-5"))

	second := holdModelWindowsFixture("claude-haiku-4-5", "claude-sonnet-5")
	second.DroppedModelWindows = 0
	hold.Sink(second)

	got, ok := hold.ModelWindows()
	if !ok {
		t.Fatalf("ModelWindows() ok = false after two usable reports, want true")
	}
	if len(got.Windows) != 2 {
		t.Fatalf("retained report carries %d entries, want the second report's 2 — "+
			"a later report replaces, it does not merge by id (%#v)", len(got.Windows), got.Windows)
	}
	if !reflect.DeepEqual(got.Windows, second.ModelWindows) {
		t.Errorf("retained entries = %#v, want the second report's %#v", got.Windows, second.ModelWindows)
	}
	if got.Dropped != 0 {
		t.Errorf("retained Dropped = %d, want the second report's 0 — the count belongs to the "+
			"report that won, not to whichever report last carried a non-zero one", got.Dropped)
	}
}

// TestSessionModelWindowHold_OtherVariantsChangeNothing walks the variants that are not a
// TurnEnd: each is forwarded and none flips the retention into the reported state. This is
// the arm an implementation that stores whatever it is handed gets wrong.
//
// model_list, slash_command_list and background_task_roster are rows on purpose. They are
// the variants the OTHER THREE holds in this chain retain, so those rows are the
// cross-store pins: no decorator may store a sibling's variant. They are also why the
// forwarded-unchanged assertion is reflect.DeepEqual rather than != — every one of these
// carries a slice, so == on the interface would panic at runtime rather than compare.
func TestSessionModelWindowHold_OtherVariantsChangeNothing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ev   turnevent.Event
	}{
		{"text_chunk", turnevent.TextChunk{MessageID: "m-1", Text: "hello"}},
		{"model_list", modelListFixture("opus")},
		{"slash_command_list", holdSlashCommandListFixture("clear")},
		{"background_task_roster", holdBackgroundTaskRosterFixture("task-a")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var next recordingSink
			hold := newSessionModelWindowHold(next.sink)
			hold.Sink(tt.ev)

			if _, ok := hold.ModelWindows(); ok {
				t.Errorf("%T flipped the hold into the reported state, want it untouched", tt.ev)
			}
			seen := next.events()
			if len(seen) != 1 {
				t.Fatalf("downstream saw %d events, want 1", len(seen))
			}
			if !reflect.DeepEqual(seen[0], tt.ev) {
				t.Errorf("downstream saw %#v, want the event forwarded unchanged", seen[0])
			}
		})
	}
}

// TestSessionModelWindowHold_ReadReturnsDeepCopy pins AC 1's last clause: the reader may
// mutate what it is handed without changing what a later read reports.
//
// It is ONE level, where cloneBackgroundTaskRoster needs two: turnevent.ModelWindow has no
// slice fields, so the entries slice is the whole depth. Both things a reader can reach
// are mutated — an entry and the Dropped count — because a clone that copies the slice and
// then aliases nothing else still has to survive the struct-assignment half.
func TestSessionModelWindowHold_ReadReturnsDeepCopy(t *testing.T) {
	t.Parallel()

	hold := newSessionModelWindowHold(nil)
	hold.Sink(holdModelWindowsFixture("claude-sonnet-5"))

	first, ok := hold.ModelWindows()
	if !ok {
		t.Fatalf("ModelWindows() ok = false after one usable report, want true")
	}
	first.Windows[0] = turnevent.ModelWindow{ModelID: "mutated", WindowTokens: 1}
	first.Dropped = 99

	second, ok := hold.ModelWindows()
	if !ok {
		t.Fatalf("second ModelWindows() ok = false, want true")
	}
	if second.Windows[0].ModelID != "claude-sonnet-5" {
		t.Errorf("Windows[0].ModelID = %q after a reader mutated its copy, want %q",
			second.Windows[0].ModelID, "claude-sonnet-5")
	}
	if second.Windows[0].WindowTokens != 200000 {
		t.Errorf("Windows[0].WindowTokens = %d after a reader mutated its copy, want the reported %d",
			second.Windows[0].WindowTokens, 200000)
	}
	if second.Dropped != 3 {
		t.Errorf("Dropped = %d after a reader mutated its copy, want the reported 3", second.Dropped)
	}
}

// TestSessionModelWindowHold_RetainsPastASaturatedSink pins AC 4: the retention sits
// UPSTREAM of the fan-in send.
//
// IT NEEDS THE CLOSER ARM, NOT THE DROPPABLE ONE, and that is the whole reason this test
// cannot be copied from TestSessionBackgroundTaskHold_RetainsPastASaturatedSink. A roster
// is droppable class, so that twin only has to push the fan-in past droppableCap; a
// turnevent.TurnEnd is CLOSING class — turnMarkFor answers turnMarkClose for it, which
// TestTurnMarkFor_TotalOverEveryVariant pins — so it rides the streamTurnSinkCloseReserve
// band the droppable class may never take and is refused only once the channel is
// GENUINELY FULL. A one-slot sink is what makes those two conditions coincide.
//
// WHAT IT PINS, precisely, and not one step further: the retention does not depend on the
// fan-in ADMITTING the event, which is what rules out a retention point downstream of this
// channel. The len(ch) assertion before the report is what makes that a conjunction rather
// than a coincidence — it proves the fixture really is saturated, so a green here cannot be
// the sink having quietly queued the event after all. It does NOT pin statement order
// inside Sink: a refused send consumes nothing, so a mutant that forwards before it stores
// passes this test and every other one here.
//
// The Warn that sinkFor's closer arm writes on the refusal does not breach AC 5. It carries
// eventKind(ev) and the session id — the variant name and nothing claude authored — and the
// logger here discards regardless.
func TestSessionModelWindowHold_RetainsPastASaturatedSink(t *testing.T) {
	t.Parallel()

	sink := newStreamTurnSink(1, discardLogger())
	// Fill the one slot, so the closer below finds the channel genuinely full rather than
	// merely past a watermark.
	sink.sinkFor("sess")(turnevent.TextChunk{MessageID: "m-0", Text: "filler"})
	if got := len(sink.ch); got != 1 {
		t.Fatalf("fan-in holds %d envelopes after the filler, want 1 (the fixture is not saturated)", got)
	}

	hold := newSessionModelWindowHold(sink.sinkFor("sess"))
	hold.Sink(holdModelWindowsFixture("claude-sonnet-5"))

	if got := len(sink.ch); got != 1 {
		t.Errorf("fan-in holds %d envelopes, want 1 — the TurnEnd must have been refused, not queued", got)
	}
	got, ok := hold.ModelWindows()
	if !ok {
		t.Fatalf("ModelWindows() ok = false after a report the saturated sink dropped, want true")
	}
	if len(got.Windows) != 1 || got.Windows[0].ModelID != "claude-sonnet-5" {
		t.Errorf("retained entries = %#v, want the dropped event's entry", got.Windows)
	}
}

// TestSessionModelWindowHold_NilReceiverRead pins AC 3's second half: a runner whose
// retention was never minted answers the unreported state rather than panicking.
func TestSessionModelWindowHold_NilReceiverRead(t *testing.T) {
	t.Parallel()

	report, ok := (*sessionModelWindowHold)(nil).ModelWindows()
	if ok {
		t.Errorf("nil-receiver ModelWindows() ok = true, want false")
	}
	if report.Windows != nil || report.Dropped != 0 {
		t.Errorf("nil-receiver ModelWindows() = %#v, want the zero value", report)
	}
}

// TestSessionModelWindowHold_ConcurrentSinkAndRead is the -race arm and pins AC 1's "on any
// goroutine" clause. The writer is production's stdout-forwarder goroutine, the reader is
// #2107's on a relay leg. Those two are the pair the mutex exists for — NOT two overlapping
// writers, which os/exec's cmd.Wait join makes impossible across a respawn.
func TestSessionModelWindowHold_ConcurrentSinkAndRead(t *testing.T) {
	t.Parallel()

	hold := newSessionModelWindowHold(nil)
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			hold.Sink(holdModelWindowsFixture("claude-haiku-4-5", "claude-sonnet-5"))
			hold.Sink(turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn})
			hold.Sink(turnevent.TextChunk{MessageID: "m", Text: "x"})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			report, ok := hold.ModelWindows()
			if !ok {
				continue
			}
			// Read a field of every entry the clone reaches, so a reader racing a writer
			// through a shared backing array is a -race report rather than a silently
			// tolerated read.
			for _, w := range report.Windows {
				_ = w.ModelID
				_ = w.WindowTokens
			}
			_ = report.Dropped
		}
	}()

	wg.Wait()
	if _, ok := hold.ModelWindows(); !ok {
		t.Errorf("ModelWindows() ok = false after 500 usable reports, want true")
	}
}

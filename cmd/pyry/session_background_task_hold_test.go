package main

import (
	"reflect"
	"sync"
	"testing"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// holdBackgroundTaskRosterFixture builds a BackgroundTaskRoster with distinguishable
// contents, filling every field a clone has to reach — the per-entry TruncatedFields
// as well as the scalars — plus a non-zero DroppedTasks, so a clone that loses the
// roster's only truncation report is visible too.
//
// It never builds the EMPTY roster, and not for the twin's reason: an empty roster is
// a real value here (emitBackgroundTaskRoster emits one on purpose), so it needs a
// fixture — just not this one. `make([]T, 0, 0)` yields a non-nil empty slice, and
// BackgroundTaskRoster.Tasks is documented nil for an empty roster and never an empty
// non-nil slice, so the empty case is written as a literal at its call sites instead.
//
// The name is prefixed by its consumer rather than taking the obvious bare one.
// cmd/pyry is one package across many files, so a package-level fixture builder
// collides silently with a sibling's: #1849's emitter tests reaching for
// session_model_hold_test.go's modelListFixture hit a compile error that reads like a
// type error rather than what it is.
func holdBackgroundTaskRosterFixture(ids ...string) turnevent.BackgroundTaskRoster {
	tasks := make([]turnevent.BackgroundTask, 0, len(ids))
	for _, id := range ids {
		tasks = append(tasks, turnevent.BackgroundTask{
			TaskID:          id,
			TaskType:        "local_bash",
			Description:     "go test ./" + id,
			TruncatedFields: []string{"description"},
		})
	}
	return turnevent.BackgroundTaskRoster{Tasks: tasks, DroppedTasks: 3}
}

// TestSessionBackgroundTaskHold_RetainsAndForwards asserts both halves of Sink's
// contract in one test: the roster is retained AND the event still reaches the
// downstream sink unchanged. Asserting the forward here rather than only by the
// absence of a swallow is what keeps a "retain and drop" implementation from passing.
//
// DroppedTasks is asserted alongside the entries because it is this variant's ONLY
// truncation report — there is no top-level TruncatedFields naming "tasks" — so a
// retention that lost the count would let a capped roster be read as a whole one.
func TestSessionBackgroundTaskHold_RetainsAndForwards(t *testing.T) {
	t.Parallel()

	var next recordingSink
	hold := newSessionBackgroundTaskHold(next.sink)
	want := holdBackgroundTaskRosterFixture("task-a", "task-b")

	hold.Sink(want)

	got, ok := hold.BackgroundTaskRoster()
	if !ok {
		t.Fatalf("BackgroundTaskRoster() ok = false after one report, want true")
	}
	if len(got.Tasks) != 2 || got.Tasks[0].TaskID != "task-a" || got.Tasks[1].TaskID != "task-b" {
		t.Errorf("retained roster = %#v, want the reported two entries", got.Tasks)
	}
	if got.DroppedTasks != want.DroppedTasks {
		t.Errorf("retained DroppedTasks = %d, want the reported %d", got.DroppedTasks, want.DroppedTasks)
	}

	seen := next.events()
	if len(seen) != 1 {
		t.Fatalf("downstream saw %d events, want 1", len(seen))
	}
	fwd, isRoster := seen[0].(turnevent.BackgroundTaskRoster)
	if !isRoster {
		t.Fatalf("downstream saw %T, want turnevent.BackgroundTaskRoster forwarded unchanged", seen[0])
	}
	if !reflect.DeepEqual(fwd, want) {
		t.Errorf("forwarded roster = %#v, want the event unchanged", fwd)
	}
}

// TestSessionBackgroundTaskHold_UnreportedIsItsOwnState pins the first half of AC 2.
// The assertion is on the bool alone, and here that is not merely the tidier spelling
// the way it is on both siblings — it is the only correct one. See the empty-roster
// test below for the half that makes it load-bearing.
func TestSessionBackgroundTaskHold_UnreportedIsItsOwnState(t *testing.T) {
	t.Parallel()

	hold := newSessionBackgroundTaskHold(nil)
	if _, ok := hold.BackgroundTaskRoster(); ok {
		t.Errorf("a fresh hold reports ok = true, want false — nothing has been reported")
	}
}

// TestSessionBackgroundTaskHold_EmptyRosterReadsAsReported pins the rest of AC 2, and
// it is the test with no counterpart in either sibling.
//
// emitBackgroundTaskRoster emits an event for an absent or empty tasks array ON
// PURPOSE: an empty roster positively says nothing is alive, which is the signal
// #1240's symptom needs. So Tasks == nil is a REACHABLE REPORTED STATE here, where
// both siblings make it unreachable — turnevent.ModelList.Models is documented "never
// empty", and emitSlashCommandList returns early on a zero-length list (#1877). An
// implementation that spells "unreported" as len(Tasks) == 0 is merely redundant
// there and WRONG here, and this test is the only one in the file that catches it.
//
// The two states are asserted against each other in one test rather than in two, so
// what is pinned is the DISTINCTION rather than each answer separately.
func TestSessionBackgroundTaskHold_EmptyRosterReadsAsReported(t *testing.T) {
	t.Parallel()

	fresh := newSessionBackgroundTaskHold(nil)
	if _, ok := fresh.BackgroundTaskRoster(); ok {
		t.Fatalf("a hold with no report reads ok = true, want false")
	}

	reported := newSessionBackgroundTaskHold(nil)
	// A literal rather than the fixture: Tasks must be nil, which the fixture's
	// make(..., 0, 0) would not give.
	reported.Sink(turnevent.BackgroundTaskRoster{})

	got, ok := reported.BackgroundTaskRoster()
	if !ok {
		t.Fatalf("BackgroundTaskRoster() ok = false after an EMPTY roster was reported, want true — "+
			"an empty roster says nothing is alive, which is not the same as no roster at all (got %#v)", got)
	}
	if len(got.Tasks) != 0 {
		t.Errorf("retained roster carries %d entries, want the reported 0", len(got.Tasks))
	}
}

// TestSessionBackgroundTaskHold_SecondReportReplaces pins AC 1's "an earlier roster
// does not survive a later one". The count assertion is what separates replacement
// from concatenation — the two agree on the last entry.
//
// The non-empty -> EMPTY row is the one that matters most and is the reason this is a
// table rather than the twin's single case: it is the real transition "the last task
// finished", and it is the only shape a merge-instead-of-replace implementation cannot
// fake. A non-empty -> non-empty pair leaves the merged roster non-empty too, so only
// its length distinguishes the two; here the retained roster is empty or it is not.
//
// It also pins REPLACE, NEVER DIFF from the other side: the hold is handed a roster
// that dropped an entry and answers with the newer value, deriving nothing — no
// finish, no disappearance — from holding a previous roster and a newer one at the
// same instant, which turnevent.BackgroundTaskRoster's type doc refuses as a daemon
// inference.
func TestSessionBackgroundTaskHold_SecondReportReplaces(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		second  turnevent.BackgroundTaskRoster
		wantLen int
		wantID  string
	}{
		{"to a shorter roster", holdBackgroundTaskRosterFixture("task-c"), 1, "task-c"},
		{"to an empty roster", turnevent.BackgroundTaskRoster{}, 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			hold := newSessionBackgroundTaskHold(nil)
			hold.Sink(holdBackgroundTaskRosterFixture("task-a", "task-b"))
			hold.Sink(tt.second)

			got, ok := hold.BackgroundTaskRoster()
			if !ok {
				t.Fatalf("BackgroundTaskRoster() ok = false after two reports, want true")
			}
			if len(got.Tasks) != tt.wantLen {
				t.Fatalf("retained roster carries %d entries, want the second report's %d — "+
					"a later roster replaces, it does not merge", len(got.Tasks), tt.wantLen)
			}
			if tt.wantID != "" && got.Tasks[0].TaskID != tt.wantID {
				t.Errorf("retained entry = %q, want the second report's %q", got.Tasks[0].TaskID, tt.wantID)
			}
		})
	}
}

// TestSessionBackgroundTaskHold_OtherVariantsChangeNothing walks the variants that are
// not a BackgroundTaskRoster: each is forwarded and none flips the retention into the
// reported state. This is the arm an implementation that stores whatever it is handed
// gets wrong.
//
// model_list and slash_command_list are rows on purpose. They are the variants the
// OTHER TWO holds in this chain retain, so those rows are the cross-store pins: no
// decorator may store a sibling's variant. They are also why the forwarded-unchanged
// assertion is reflect.DeepEqual rather than != — every one of these carries a slice,
// so == on the interface would panic at runtime rather than compare.
//
// background_task_started is a row because it is the scalar peer of the retained
// variant from the same claude family, so it is the nearest miss a type switch could
// plausibly widen to by accident.
func TestSessionBackgroundTaskHold_OtherVariantsChangeNothing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ev   turnevent.Event
	}{
		{"text_chunk", turnevent.TextChunk{MessageID: "m-1", Text: "hello"}},
		{"turn_end", turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn}},
		{"background_task_started", turnevent.BackgroundTaskStarted{TaskID: "task-a", TaskType: "local_bash"}},
		{"model_list", modelListFixture("opus")},
		{"slash_command_list", holdSlashCommandListFixture("clear")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var next recordingSink
			hold := newSessionBackgroundTaskHold(next.sink)
			hold.Sink(tt.ev)

			if _, ok := hold.BackgroundTaskRoster(); ok {
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

// TestSessionBackgroundTaskHold_ReadReturnsDeepCopy pins AC 3. It mutates all THREE
// things a reader can reach — the Tasks slice itself, an entry's TruncatedFields
// element, and the entry — because the clone is two levels deep and one that stops at
// Tasks still passes a one-mutation test.
//
// Two levels, not the twin's three: BackgroundTask has exactly one []string field
// where turnevent.SlashCommand has two, so cloneSlashCommandList is the shape to
// follow and not the depth. DroppedTasks is an int and rides the struct assignment.
func TestSessionBackgroundTaskHold_ReadReturnsDeepCopy(t *testing.T) {
	t.Parallel()

	hold := newSessionBackgroundTaskHold(nil)
	hold.Sink(holdBackgroundTaskRosterFixture("task-a"))

	first, ok := hold.BackgroundTaskRoster()
	if !ok {
		t.Fatalf("BackgroundTaskRoster() ok = false after one report, want true")
	}
	first.Tasks[0].TruncatedFields[0] = "mutated-field"
	first.Tasks[0] = turnevent.BackgroundTask{TaskID: "mutated-entry"}
	first.DroppedTasks = 99

	second, ok := hold.BackgroundTaskRoster()
	if !ok {
		t.Fatalf("second BackgroundTaskRoster() ok = false, want true")
	}
	if second.Tasks[0].TaskID != "task-a" {
		t.Errorf("Tasks[0].TaskID = %q after a reader mutated its copy, want %q", second.Tasks[0].TaskID, "task-a")
	}
	if second.Tasks[0].TruncatedFields[0] != "description" {
		t.Errorf("TruncatedFields[0] = %q after a reader mutated its copy, want %q",
			second.Tasks[0].TruncatedFields[0], "description")
	}
	if second.DroppedTasks != 3 {
		t.Errorf("DroppedTasks = %d after a reader mutated its copy, want the reported 3", second.DroppedTasks)
	}
}

// TestSessionBackgroundTaskHold_PreservesNilSlices pins the convention every slice
// field on this variant documents: nil when there is nothing to report, never an empty
// non-nil slice. slices.Clone gives that for free and a hand-rolled make+copy clone
// does not, which is the whole reason to say so in a test — a reader that
// distinguishes the two would see a difference the retention invented.
//
// Both levels are checked, because they reach the convention by different routes:
// Tasks is nil for the empty roster the producer deliberately still emits, and an
// entry's TruncatedFields is nil whenever nothing about that entry was cut.
func TestSessionBackgroundTaskHold_PreservesNilSlices(t *testing.T) {
	t.Parallel()

	hold := newSessionBackgroundTaskHold(nil)
	hold.Sink(turnevent.BackgroundTaskRoster{
		Tasks: []turnevent.BackgroundTask{{TaskID: "task-a", TaskType: "local_bash"}},
	})

	got, ok := hold.BackgroundTaskRoster()
	if !ok {
		t.Fatalf("BackgroundTaskRoster() ok = false after one report, want true")
	}
	if got.Tasks[0].TruncatedFields != nil {
		t.Errorf("TruncatedFields = %#v after a nothing-was-cut entry, want nil", got.Tasks[0].TruncatedFields)
	}

	hold.Sink(turnevent.BackgroundTaskRoster{})
	empty, ok := hold.BackgroundTaskRoster()
	if !ok {
		t.Fatalf("BackgroundTaskRoster() ok = false after an empty roster, want true")
	}
	if empty.Tasks != nil {
		t.Errorf("Tasks = %#v after an empty roster, want nil", empty.Tasks)
	}
}

// TestSessionBackgroundTaskHold_RetainsPastASaturatedSink pins AC 1's second clause:
// the retention sits UPSTREAM of the fan-in send. A BackgroundTaskRoster is droppable
// class (turnMarkFor's default arm answers turnMarkNone for it, which
// TestTurnMarkFor_TotalOverEveryVariant pins), so a full channel refuses it.
//
// WHAT IT PINS, precisely, and not one step further: the retention does not depend on
// the fan-in ADMITTING the event, which is what rules out a retention point downstream
// of this channel. The len(ch) assertion before the report is what makes that a
// conjunction rather than a coincidence — it proves the fixture really is saturated,
// so a green here cannot be the sink having quietly queued the roster after all. It
// does NOT pin statement order inside Sink: a refused send consumes nothing, so a
// mutant that forwards before it stores passes this test and every other one here.
// Being a decorator upstream of the channel at all is the property; the order of two
// statements in an already-synchronous Sink is not.
//
// The stakes differ from both siblings even though the mechanism is identical. There,
// one initialize exchange per child produces exactly one event and no later one
// replaces it, so a refusal loses the value for the child's whole life. Here claude
// re-reports on every roster change, so a refusal costs the daemon only the window
// until the next change — which may be the whole run, since a roster that stops
// changing is exactly the busy-session case a client connecting mid-run needs answered.
func TestSessionBackgroundTaskHold_RetainsPastASaturatedSink(t *testing.T) {
	t.Parallel()

	sink := newStreamTurnSink(1, discardLogger())
	// Fill the one slot with a droppable event, so the roster below is refused at
	// droppableCap rather than admitted.
	sink.sinkFor("sess")(turnevent.TextChunk{MessageID: "m-0", Text: "filler"})
	if got := len(sink.ch); got != 1 {
		t.Fatalf("fan-in holds %d envelopes after the filler, want 1 (the fixture is not saturated)", got)
	}

	hold := newSessionBackgroundTaskHold(sink.sinkFor("sess"))
	hold.Sink(holdBackgroundTaskRosterFixture("task-a"))

	if got := len(sink.ch); got != 1 {
		t.Errorf("fan-in holds %d envelopes, want 1 — the roster must have been refused, not queued", got)
	}
	got, ok := hold.BackgroundTaskRoster()
	if !ok {
		t.Fatalf("BackgroundTaskRoster() ok = false after a report the saturated sink dropped, want true")
	}
	if len(got.Tasks) != 1 || got.Tasks[0].TaskID != "task-a" {
		t.Errorf("retained roster = %#v, want the dropped event's entry", got.Tasks)
	}
}

// TestSessionBackgroundTaskHold_NilReceiverRead pins AC 5's second half: a runner whose
// retention was never minted answers the unreported state rather than panicking.
func TestSessionBackgroundTaskHold_NilReceiverRead(t *testing.T) {
	t.Parallel()

	roster, ok := (*sessionBackgroundTaskHold)(nil).BackgroundTaskRoster()
	if ok {
		t.Errorf("nil-receiver BackgroundTaskRoster() ok = true, want false")
	}
	if roster.Tasks != nil || roster.DroppedTasks != 0 {
		t.Errorf("nil-receiver BackgroundTaskRoster() = %#v, want the zero value", roster)
	}
}

// TestSessionBackgroundTaskHold_ConcurrentSinkAndRead is the -race arm: the writer is
// production's stdout forwarder goroutine, the reader is #2079's resolver on a relay
// leg. Those two are the pair the mutex exists for — NOT two overlapping writers,
// which os/exec's cmd.Wait join makes impossible across a respawn.
func TestSessionBackgroundTaskHold_ConcurrentSinkAndRead(t *testing.T) {
	t.Parallel()

	hold := newSessionBackgroundTaskHold(nil)
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			hold.Sink(holdBackgroundTaskRosterFixture("task-a"))
			hold.Sink(turnevent.TextChunk{MessageID: "m", Text: "x"})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			roster, ok := hold.BackgroundTaskRoster()
			if !ok {
				continue
			}
			// Read a field of every slice the clone reaches, so a reader racing a
			// writer through a shared backing array is a -race report rather than a
			// silently tolerated read.
			for _, task := range roster.Tasks {
				_ = task.TaskID
				_ = len(task.TruncatedFields)
			}
		}
	}()

	wg.Wait()
	if _, ok := hold.BackgroundTaskRoster(); !ok {
		t.Errorf("BackgroundTaskRoster() ok = false after 500 reports, want true")
	}
}

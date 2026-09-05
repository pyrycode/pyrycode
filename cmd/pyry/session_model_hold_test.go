package main

import (
	"reflect"
	"sync"
	"testing"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// modelListFixture builds a ModelList with distinguishable contents. It never
// builds an EMPTY one: turnevent.ModelList.Models states the producer's gate does
// not emit on an empty array, so an empty-list report is a shape production cannot
// reach and a fixture for it would pin a state nothing can observe.
func modelListFixture(values ...string) turnevent.ModelList {
	models := make([]turnevent.ModelOption, 0, len(values))
	for _, v := range values {
		models = append(models, turnevent.ModelOption{
			ResolvedModel:   "resolved-" + v,
			Value:           v,
			DisplayName:     "Display " + v,
			EffortLevels:    []string{"low", "high"},
			TruncatedFields: []string{"display_name"},
		})
	}
	return turnevent.ModelList{Models: models}
}

// recordingSink collects every event handed to it, in order.
type recordingSink struct {
	mu   sync.Mutex
	seen []turnevent.Event
}

func (r *recordingSink) sink(ev turnevent.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, ev)
}

func (r *recordingSink) events() []turnevent.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]turnevent.Event(nil), r.seen...)
}

// TestSessionModelHold_RetainsAndForwards asserts both halves of Sink's contract
// in one test: the list is retained AND the event still reaches the downstream
// sink unchanged. Forwarding is AC 4 — the event path stays what it is today —
// and asserting it here rather than only by the absence of a swallow is what
// keeps a "retain and drop" implementation from passing.
func TestSessionModelHold_RetainsAndForwards(t *testing.T) {
	t.Parallel()

	var next recordingSink
	hold := newSessionModelHold(next.sink)
	want := modelListFixture("opus", "sonnet")

	hold.Sink(want)

	got, ok := hold.ModelList()
	if !ok {
		t.Fatalf("ModelList() ok = false after one report, want true")
	}
	if len(got.Models) != 2 || got.Models[0].Value != "opus" || got.Models[1].Value != "sonnet" {
		t.Errorf("retained list = %#v, want the reported two entries", got.Models)
	}

	seen := next.events()
	if len(seen) != 1 {
		t.Fatalf("downstream saw %d events, want 1", len(seen))
	}
	fwd, isList := seen[0].(turnevent.ModelList)
	if !isList {
		t.Fatalf("downstream saw %T, want turnevent.ModelList forwarded unchanged", seen[0])
	}
	if len(fwd.Models) != len(want.Models) || fwd.Models[0].Value != want.Models[0].Value {
		t.Errorf("forwarded list = %#v, want the event unchanged", fwd.Models)
	}
}

// TestSessionModelHold_UnreportedIsItsOwnState pins AC 3. The assertion is on the
// bool alone, never on len(Models) == 0: the producer never emits an empty list,
// so an empty-list assertion would pin a state production cannot reach.
func TestSessionModelHold_UnreportedIsItsOwnState(t *testing.T) {
	t.Parallel()

	hold := newSessionModelHold(nil)
	if _, ok := hold.ModelList(); ok {
		t.Errorf("a fresh hold reports ok = true, want false — nothing has been reported")
	}
}

// TestSessionModelHold_SecondReportReplaces pins AC 2: a second report replaces
// the retained value rather than accumulating beside it. The count assertion is
// what separates replacement from concatenation — the two agree on the last
// entry.
func TestSessionModelHold_SecondReportReplaces(t *testing.T) {
	t.Parallel()

	hold := newSessionModelHold(nil)
	hold.Sink(modelListFixture("opus", "sonnet"))
	hold.Sink(modelListFixture("haiku"))

	got, ok := hold.ModelList()
	if !ok {
		t.Fatalf("ModelList() ok = false after two reports, want true")
	}
	if len(got.Models) != 1 {
		t.Fatalf("retained list carries %d entries, want 1 — the second report replaces, it does not accumulate", len(got.Models))
	}
	if got.Models[0].Value != "haiku" {
		t.Errorf("retained entry = %q, want the second report's %q", got.Models[0].Value, "haiku")
	}
}

// TestSessionModelHold_OtherVariantsChangeNothing walks the variants that are not
// a ModelList: each is forwarded and none flips the retention into the reported
// state. This is the arm an implementation that stores whatever it is handed gets
// wrong.
func TestSessionModelHold_OtherVariantsChangeNothing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ev   turnevent.Event
	}{
		{"text_chunk", turnevent.TextChunk{MessageID: "m-1", Text: "hello"}},
		{"turn_end", turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn}},
		{"model_announced", turnevent.ModelAnnounced{Model: "claude-opus-5"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var next recordingSink
			hold := newSessionModelHold(next.sink)
			hold.Sink(tt.ev)

			if _, ok := hold.ModelList(); ok {
				t.Errorf("%T flipped the hold into the reported state, want it untouched", tt.ev)
			}
			seen := next.events()
			if len(seen) != 1 {
				t.Fatalf("downstream saw %d events, want 1", len(seen))
			}
			// DeepEqual rather than !=, and not as a matter of taste: these are
			// turnevent.Event INTERFACE values, so == dispatches to the dynamic type's
			// comparison and PANICS on any variant carrying a slice. ModelList has
			// carried one since #1812 and only escaped this row by not being in the
			// table; TurnEnd joined it in #2101 and turned the row into a panic that
			// killed the parallel subtests around it. A table of variants cannot use
			// == at all — the next variant to grow a slice would reintroduce it.
			if !reflect.DeepEqual(seen[0], tt.ev) {
				t.Errorf("downstream saw %#v, want the event forwarded unchanged", seen[0])
			}
		})
	}
}

// TestSessionModelHold_ReadReturnsDeepCopy mutates all THREE slices a reader can
// reach — the Models slice itself and both []string fields inside an entry —
// because a clone that misses one inner slice still passes a one-mutation test.
func TestSessionModelHold_ReadReturnsDeepCopy(t *testing.T) {
	t.Parallel()

	hold := newSessionModelHold(nil)
	hold.Sink(modelListFixture("opus"))

	first, ok := hold.ModelList()
	if !ok {
		t.Fatalf("ModelList() ok = false after one report, want true")
	}
	first.Models[0].EffortLevels[0] = "mutated-level"
	first.Models[0].TruncatedFields[0] = "mutated-field"
	first.Models[0] = turnevent.ModelOption{Value: "mutated-entry"}

	second, ok := hold.ModelList()
	if !ok {
		t.Fatalf("second ModelList() ok = false, want true")
	}
	if second.Models[0].Value != "opus" {
		t.Errorf("Models[0].Value = %q after a reader mutated its copy, want %q", second.Models[0].Value, "opus")
	}
	if second.Models[0].EffortLevels[0] != "low" {
		t.Errorf("EffortLevels[0] = %q after a reader mutated its copy, want %q", second.Models[0].EffortLevels[0], "low")
	}
	if second.Models[0].TruncatedFields[0] != "display_name" {
		t.Errorf("TruncatedFields[0] = %q after a reader mutated its copy, want %q", second.Models[0].TruncatedFields[0], "display_name")
	}
}

// TestSessionModelHold_RetainsPastASaturatedSink pins the ordering the ticket
// calls this design's central call: the retention sits UPSTREAM of the fan-in
// send. A ModelList is droppable class (turnMarkFor's default arm), so a full
// channel refuses it — and the list must survive that refusal, because one
// initialize exchange per child produces exactly one of these and no later event
// replaces it.
//
// WHAT IT PINS, precisely: the retention does not depend on the fan-in ADMITTING
// the event. The len(ch) assertion before the report is what makes that a
// conjunction rather than a coincidence — it proves the fixture really is
// saturated, so a green here cannot be the sink having quietly queued the list
// after all. A retention point downstream of this channel (in the drain) cannot
// satisfy it; no rearrangement WITHIN Sink can break it, which is the limit of
// what a same-file mutant can show.
func TestSessionModelHold_RetainsPastASaturatedSink(t *testing.T) {
	t.Parallel()

	sink := newStreamTurnSink(1, discardLogger())
	// Fill the one slot with a droppable event, so the ModelList below is refused
	// at droppableCap rather than admitted.
	sink.sinkFor("sess")(turnevent.TextChunk{MessageID: "m-0", Text: "filler"})
	if got := len(sink.ch); got != 1 {
		t.Fatalf("fan-in holds %d envelopes after the filler, want 1 (the fixture is not saturated)", got)
	}

	hold := newSessionModelHold(sink.sinkFor("sess"))
	hold.Sink(modelListFixture("opus"))

	if got := len(sink.ch); got != 1 {
		t.Errorf("fan-in holds %d envelopes, want 1 — the ModelList must have been refused, not queued", got)
	}
	got, ok := hold.ModelList()
	if !ok {
		t.Fatalf("ModelList() ok = false after a report the saturated sink dropped, want true")
	}
	if len(got.Models) != 1 || got.Models[0].Value != "opus" {
		t.Errorf("retained list = %#v, want the dropped event's entry", got.Models)
	}
}

// TestSessionModelHold_NilReceiverRead pins the nil-receiver no-op, mirroring
// clearForSession's precedent: a streamRunner whose hold was never minted answers
// the unreported state rather than panicking.
func TestSessionModelHold_NilReceiverRead(t *testing.T) {
	t.Parallel()

	list, ok := (*sessionModelHold)(nil).ModelList()
	if ok {
		t.Errorf("nil-receiver ModelList() ok = true, want false")
	}
	if list.Models != nil || list.DroppedModels != 0 {
		t.Errorf("nil-receiver ModelList() = %#v, want the zero value", list)
	}
}

// TestSessionModelHold_ConcurrentSinkAndRead is the -race arm: a writer on one
// goroutine (production's stdout forwarder, and a second one across a respawn)
// against a reader on another (#1837's publisher, on a relay leg).
func TestSessionModelHold_ConcurrentSinkAndRead(t *testing.T) {
	t.Parallel()

	hold := newSessionModelHold(nil)
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			hold.Sink(modelListFixture("opus"))
			hold.Sink(turnevent.TextChunk{MessageID: "m", Text: "x"})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			list, ok := hold.ModelList()
			if !ok {
				continue
			}
			// Read a field of every slice the clone reaches, so a reader racing a
			// writer through a shared backing array is a -race report rather than a
			// silently tolerated read.
			for _, m := range list.Models {
				_ = m.Value
				_ = len(m.EffortLevels)
				_ = len(m.TruncatedFields)
			}
		}
	}()

	wg.Wait()
	if _, ok := hold.ModelList(); !ok {
		t.Errorf("ModelList() ok = false after 500 reports, want true")
	}
}

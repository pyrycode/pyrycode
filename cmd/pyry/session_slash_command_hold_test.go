package main

import (
	"reflect"
	"sync"
	"testing"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// holdSlashCommandListFixture builds a SlashCommandList with distinguishable
// contents, filling every field a clone has to reach — both per-entry []string
// fields as well as the scalars — plus a non-zero DroppedCommands, so a clone that
// loses the count is visible too.
//
// It never builds an EMPTY one: streamsup's emitSlashCommandList returns early on a
// zero-length entry list (#1877), so an empty-list report is a shape production
// cannot reach and a fixture for it would pin a state nothing can observe.
//
// The name is prefixed by its consumer rather than taking the obvious bare one.
// cmd/pyry is one package across many files, so a package-level fixture builder
// collides silently with a sibling's: #1849's emitter tests reaching for
// session_model_hold_test.go's modelListFixture hit a compile error that reads like a
// type error rather than what it is. emitterSlashCommandListFixture already occupies
// the emitter's half of this name's space.
func holdSlashCommandListFixture(names ...string) turnevent.SlashCommandList {
	commands := make([]turnevent.SlashCommand, 0, len(names))
	for _, n := range names {
		commands = append(commands, turnevent.SlashCommand{
			Name:            n,
			ArgumentHint:    "[for-" + n + "]",
			Description:     "Description of " + n,
			Aliases:         []string{n + "-alias", n + "-alt"},
			TruncatedFields: []string{"description"},
		})
	}
	return turnevent.SlashCommandList{Commands: commands, DroppedCommands: 3}
}

// TestSessionSlashCommandHold_RetainsAndForwards asserts both halves of Sink's
// contract in one test: the list is retained AND the event still reaches the
// downstream sink unchanged. Asserting the forward here rather than only by the
// absence of a swallow is what keeps a "retain and drop" implementation from
// passing.
func TestSessionSlashCommandHold_RetainsAndForwards(t *testing.T) {
	t.Parallel()

	var next recordingSink
	hold := newSessionSlashCommandHold(next.sink)
	want := holdSlashCommandListFixture("clear", "compact")

	hold.Sink(want)

	got, ok := hold.SlashCommandList()
	if !ok {
		t.Fatalf("SlashCommandList() ok = false after one report, want true")
	}
	if len(got.Commands) != 2 || got.Commands[0].Name != "clear" || got.Commands[1].Name != "compact" {
		t.Errorf("retained list = %#v, want the reported two entries", got.Commands)
	}
	if got.DroppedCommands != want.DroppedCommands {
		t.Errorf("retained DroppedCommands = %d, want the reported %d", got.DroppedCommands, want.DroppedCommands)
	}

	seen := next.events()
	if len(seen) != 1 {
		t.Fatalf("downstream saw %d events, want 1", len(seen))
	}
	fwd, isList := seen[0].(turnevent.SlashCommandList)
	if !isList {
		t.Fatalf("downstream saw %T, want turnevent.SlashCommandList forwarded unchanged", seen[0])
	}
	if !reflect.DeepEqual(fwd, want) {
		t.Errorf("forwarded list = %#v, want the event unchanged", fwd)
	}
}

// TestSessionSlashCommandHold_UnreportedIsItsOwnState pins AC 3. The assertion is on
// the bool alone, never on len(Commands) == 0 — and the reason differs from the
// model list's, so it is stated rather than inherited: there it is the TYPE that
// documents Models never empty, here it is the PRODUCER that suppresses, so nothing
// with zero entries ever reaches the retention.
func TestSessionSlashCommandHold_UnreportedIsItsOwnState(t *testing.T) {
	t.Parallel()

	hold := newSessionSlashCommandHold(nil)
	if _, ok := hold.SlashCommandList(); ok {
		t.Errorf("a fresh hold reports ok = true, want false — nothing has been reported")
	}
}

// TestSessionSlashCommandHold_SecondReportReplaces pins AC 2: a respawn's inventory
// supersedes rather than accumulating beside the first. The count assertion is what
// separates replacement from concatenation — the two agree on the last entry.
func TestSessionSlashCommandHold_SecondReportReplaces(t *testing.T) {
	t.Parallel()

	hold := newSessionSlashCommandHold(nil)
	hold.Sink(holdSlashCommandListFixture("clear", "compact"))
	hold.Sink(holdSlashCommandListFixture("review"))

	got, ok := hold.SlashCommandList()
	if !ok {
		t.Fatalf("SlashCommandList() ok = false after two reports, want true")
	}
	if len(got.Commands) != 1 {
		t.Fatalf("retained list carries %d entries, want 1 — the second report replaces, it does not accumulate", len(got.Commands))
	}
	if got.Commands[0].Name != "review" {
		t.Errorf("retained entry = %q, want the second report's %q", got.Commands[0].Name, "review")
	}
}

// TestSessionSlashCommandHold_OtherVariantsChangeNothing walks the variants that are
// not a SlashCommandList: each is forwarded and none flips the retention into the
// reported state. This is the arm an implementation that stores whatever it is
// handed gets wrong.
//
// turnevent.ModelList is a row on purpose. It is the variant the OTHER hold in this
// chain retains, so this row is also the cross-store pin: neither decorator may
// store the other's variant. It is why the forwarded-unchanged assertion is
// reflect.DeepEqual rather than != — a ModelList carries a slice, so == on the
// interface would panic at runtime rather than compare.
func TestSessionSlashCommandHold_OtherVariantsChangeNothing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ev   turnevent.Event
	}{
		{"text_chunk", turnevent.TextChunk{MessageID: "m-1", Text: "hello"}},
		{"turn_end", turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn}},
		{"model_announced", turnevent.ModelAnnounced{Model: "claude-opus-5"}},
		{"model_list", modelListFixture("opus")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var next recordingSink
			hold := newSessionSlashCommandHold(next.sink)
			hold.Sink(tt.ev)

			if _, ok := hold.SlashCommandList(); ok {
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

// TestSessionSlashCommandHold_ReadReturnsDeepCopy mutates all FOUR things a reader
// can reach — the Commands slice itself and, inside an entry, both []string fields —
// because the clone is three levels deep here and one that stops at Commands, or
// that clones only one of the two inner slices, still passes a one-mutation test.
func TestSessionSlashCommandHold_ReadReturnsDeepCopy(t *testing.T) {
	t.Parallel()

	hold := newSessionSlashCommandHold(nil)
	hold.Sink(holdSlashCommandListFixture("clear"))

	first, ok := hold.SlashCommandList()
	if !ok {
		t.Fatalf("SlashCommandList() ok = false after one report, want true")
	}
	first.Commands[0].Aliases[0] = "mutated-alias"
	first.Commands[0].TruncatedFields[0] = "mutated-field"
	first.Commands[0] = turnevent.SlashCommand{Name: "mutated-entry"}

	second, ok := hold.SlashCommandList()
	if !ok {
		t.Fatalf("second SlashCommandList() ok = false, want true")
	}
	if second.Commands[0].Name != "clear" {
		t.Errorf("Commands[0].Name = %q after a reader mutated its copy, want %q", second.Commands[0].Name, "clear")
	}
	if second.Commands[0].Aliases[0] != "clear-alias" {
		t.Errorf("Aliases[0] = %q after a reader mutated its copy, want %q", second.Commands[0].Aliases[0], "clear-alias")
	}
	if second.Commands[0].TruncatedFields[0] != "description" {
		t.Errorf("TruncatedFields[0] = %q after a reader mutated its copy, want %q", second.Commands[0].TruncatedFields[0], "description")
	}
}

// TestSessionSlashCommandHold_PreservesNilSlices pins the convention every slice
// field on this variant documents: nil when there is nothing to report, never an
// empty non-nil slice. slices.Clone gives that for free and a hand-rolled make+copy
// clone does not, which is the whole reason to say so in a test — a reader that
// distinguishes the two (protocol.SlashCommand.MarshalJSON does) would see a
// difference the retention invented.
func TestSessionSlashCommandHold_PreservesNilSlices(t *testing.T) {
	t.Parallel()

	hold := newSessionSlashCommandHold(nil)
	hold.Sink(turnevent.SlashCommandList{
		Commands: []turnevent.SlashCommand{{Name: "clear"}},
	})

	got, ok := hold.SlashCommandList()
	if !ok {
		t.Fatalf("SlashCommandList() ok = false after one report, want true")
	}
	if got.Commands[0].Aliases != nil {
		t.Errorf("Aliases = %#v after a nil-aliases report, want nil", got.Commands[0].Aliases)
	}
	if got.Commands[0].TruncatedFields != nil {
		t.Errorf("TruncatedFields = %#v after a nil-report entry, want nil", got.Commands[0].TruncatedFields)
	}
}

// TestSessionSlashCommandHold_RetainsPastASaturatedSink pins the ordering the ticket
// calls this design's central call: the retention sits UPSTREAM of the fan-in send. A
// SlashCommandList is droppable class (turnMarkFor's default arm answers turnMarkNone,
// which TestTurnMarkFor_TotalOverEveryVariant pins), so a full channel refuses it — and
// the list must survive that refusal, because one initialize exchange per child
// produces exactly one of these and no later event replaces it.
//
// WHAT IT PINS, precisely, and not one step further: the retention does not depend on
// the fan-in ADMITTING the event, which is what rules out a retention point downstream
// of this channel. The len(ch) assertion before the report is what makes that a
// conjunction rather than a coincidence — it proves the fixture really is saturated, so
// a green here cannot be the sink having quietly queued the list after all. It does NOT
// pin statement order inside Sink: a refused send consumes nothing, so a mutant that
// forwards before it stores passes this test and every other one here. Being a
// decorator upstream of the channel at all is the property; the order of two statements
// in an already-synchronous Sink is not.
func TestSessionSlashCommandHold_RetainsPastASaturatedSink(t *testing.T) {
	t.Parallel()

	sink := newStreamTurnSink(1, discardLogger())
	// Fill the one slot with a droppable event, so the SlashCommandList below is
	// refused at droppableCap rather than admitted.
	sink.sinkFor("sess")(turnevent.TextChunk{MessageID: "m-0", Text: "filler"})
	if got := len(sink.ch); got != 1 {
		t.Fatalf("fan-in holds %d envelopes after the filler, want 1 (the fixture is not saturated)", got)
	}

	hold := newSessionSlashCommandHold(sink.sinkFor("sess"))
	hold.Sink(holdSlashCommandListFixture("clear"))

	if got := len(sink.ch); got != 1 {
		t.Errorf("fan-in holds %d envelopes, want 1 — the SlashCommandList must have been refused, not queued", got)
	}
	got, ok := hold.SlashCommandList()
	if !ok {
		t.Fatalf("SlashCommandList() ok = false after a report the saturated sink dropped, want true")
	}
	if len(got.Commands) != 1 || got.Commands[0].Name != "clear" {
		t.Errorf("retained list = %#v, want the dropped event's entry", got.Commands)
	}
}

// TestSessionSlashCommandHold_NilReceiverRead pins AC 4's second half: a streamRunner
// whose retention was never minted answers the unreported state rather than panicking.
func TestSessionSlashCommandHold_NilReceiverRead(t *testing.T) {
	t.Parallel()

	list, ok := (*sessionSlashCommandHold)(nil).SlashCommandList()
	if ok {
		t.Errorf("nil-receiver SlashCommandList() ok = true, want false")
	}
	if list.Commands != nil || list.DroppedCommands != 0 {
		t.Errorf("nil-receiver SlashCommandList() = %#v, want the zero value", list)
	}
}

// TestSessionSlashCommandHold_ConcurrentSinkAndRead is the -race arm: the writer is
// production's stdout forwarder goroutine, the reader is #2005's resolver on a relay
// leg. Those two are the pair the mutex exists for — NOT two overlapping writers,
// which os/exec's cmd.Wait join makes impossible across a respawn.
func TestSessionSlashCommandHold_ConcurrentSinkAndRead(t *testing.T) {
	t.Parallel()

	hold := newSessionSlashCommandHold(nil)
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			hold.Sink(holdSlashCommandListFixture("clear"))
			hold.Sink(turnevent.TextChunk{MessageID: "m", Text: "x"})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			list, ok := hold.SlashCommandList()
			if !ok {
				continue
			}
			// Read a field of every slice the clone reaches, so a reader racing a
			// writer through a shared backing array is a -race report rather than a
			// silently tolerated read.
			for _, c := range list.Commands {
				_ = c.Name
				_ = len(c.Aliases)
				_ = len(c.TruncatedFields)
			}
		}
	}()

	wg.Wait()
	if _, ok := hold.SlashCommandList(); !ok {
		t.Errorf("SlashCommandList() ok = false after 500 reports, want true")
	}
}

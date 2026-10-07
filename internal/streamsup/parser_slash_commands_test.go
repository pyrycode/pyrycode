package streamsup

import (
	"fmt"
	"log/slog"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// commandEntryFixture builds one entry of the `commands` array from the FIRST key the
// decode reads. The value is `any` so a row can put a number or a null where a string
// belongs, which is the undecodable rung's input and the null carve-out's.
//
// It still takes ONE parameter although the decode target declares THREE keys since
// #1957: commandEntryWithFixture is how a row adds the other two, for the reason stated
// there. Building the name key ALONE is also why no existing row gained an argument
// hint by accident when the third key landed — a row carries a hint only where it asks
// for one.
func commandEntryFixture(name any) map[string]any {
	return map[string]any{"name": name}
}

// commandEntryWithFixture returns a COPY of one `commands` entry carrying an extra
// claude key. commandEntryFixture's one-parameter signature is deliberately not
// widened — it has 37 calls across 32 lines of this file and none of them wants a
// second argument — which is modelEntryWithFixture's stated reason for
// modelEntryFixture, one array over, and this helper is that one's shape verbatim.
//
// The key is a PARAMETER rather than a description-specific signature, which is what
// #1957's argumentHint rows USE rather than minting a third helper, and what #1825's
// aliases rows use in turn. The
// value is `any` so a row can put a null or a non-string where a string belongs, which
// is the null carve-out's input and the undecodable rung's — and, since #1825, an
// []any where an array of strings belongs, which is what lets one helper build both a
// well-formed alias list and an element-level decode failure.
//
// Its own function rather than a call to modelEntryWithFixture, which is
// map-shaped identically: the two build entries of two different arrays with two
// different key sets, and a row naming the wrong array's builder is the thing a
// reader has to be able to see at the call site.
func commandEntryWithFixture(entry map[string]any, key string, value any) map[string]any {
	out := make(map[string]any, len(entry)+1)
	for k, v := range entry {
		out[k] = v
	}
	out[key] = value
	return out
}

// TestParser_InitializeControlResponseCommandsOnlyRungEmits is #1853's AC 2 second
// half, #1890's proof of the keyword that second half handed over, and — since #1891 —
// the liveness proof of the emit that rung now performs: a success carrying commands
// and no models lands on its OWN rung, logs controlResponseCommandsOnly and emits ONE
// turnevent.SlashCommandList, where a success carrying neither array logs the narrowed
// `ack` and emits nothing. The count still says what decoded on all three rows.
//
// It was ...AckReportsTheCommandCount until #1891. "Ack" stopped describing what the
// table covers when the rung it is organised around got its own keyword, and #1890
// deliberately left the name so it would be renamed ONCE, here, where the premise it
// was written on — that a commands-carrying payload with no models emits nothing —
// is the thing being overturned.
//
// The fixture is HAND-BUILT because the capture cannot supply it: all three
// responding arms carry both arrays, so no committed bytes exercise the
// commands-without-models case. The capture pins the other half
// (TestParser_InitializeControlResponseCountsTheCapturedCommands).
//
// THREE entries, not one, on the first row. A mutant reporting a bare present/absent
// bool, a literal 1, or len(models) all read right against a one-entry fixture and
// wrong against three.
//
// TWO COUNTS AGAINST ONE LITERAL, and they are a cross-check rather than a
// restatement: the record's `commands` is len(cr.Response.Response.Commands) taken
// before any construction runs, while len(list.Commands) comes out of
// emitSlashCommandList's own loop. They are computed in different places from
// different values, so they are asserted separately and must not be collapsed.
//
// THE NAMES VERBATIM on the first row, and `__remote-workflow` is why they are
// spelled out rather than counted: it is a committed capture's own name and the
// standing proof that NO NAME CHARSET MAY BE ASSUMED. Together with the type
// assertion — the single event must be a SlashCommandList and nothing else, so no
// ModelList rides along — this is what pins "same construction, same verbatim name
// (#1600)" at the new call site rather than at the models rung's.
//
// THE CAP IS NOT PINNED BY THAT, and the difference is worth a line rather than a
// claim: every name on the row is far under maxSlashCommandName, so a byte-exact
// comparison reddens a SECOND cap tighter than `__remote-workflow`'s 17 bytes and
// never the bound itself. Where this rung's cap IS pinned is the commands-only row of
// TestParser_SlashCommandFieldsAreCapped, which since #1885 rides this rung with one
// over-cap name and one that fits. The structure — ONE emitter reached from two rungs,
// so one loop and one maxSlashCommandName — is what makes that single row enough here,
// and that row is what MEASURES the structure rather than inheriting it.
//
// NOT a row here: any cap boundary. That whole matrix is
// TestParser_SlashCommandFieldsAreCapped's, its boundary rows are still measured on the
// models rung, and this rung's cap already has its own single row in that same matrix;
// duplicating a boundary row here would buy a second copy of a bound rather than a
// second proof of it.
//
// The `neither array` row is the PAIRED NEGATIVE for BOTH halves now. It is what makes
// the keyword a SPLIT rather than a rename — without it a mutant logging commands_only
// on every success goes green — and it is equally what stops a mutant that emits on
// every success from going green, since it is the only row on this table that reaches
// an emitting-adjacent rung and must not emit. It is also the reason wantReason is a
// row field rather than a literal in wantAttrs — the rows disagree on it.
//
// It is also the one rung where a swap between `commands` and any of the model trio
// shows: the trio is all-zero here and this count is not. That sentence is scoped to
// the rows that report a non-zero count, which after #1890 are the commands_only ones
// — on the narrowed ack row all four ints are 0 and a swap is invisible.
func TestParser_InitializeControlResponseCommandsOnlyRungEmits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		inner        map[string]any
		wantReason   string
		wantCommands int
		wantEvents   int
		wantNames    []string
		why          string
	}{
		{
			name: "commands and no models",
			inner: map[string]any{"commands": []any{
				commandEntryFixture("deep-research"),
				commandEntryFixture("design"),
				commandEntryFixture("__remote-workflow"),
			}},
			wantReason:   "commands_only",
			wantCommands: 3,
			wantEvents:   1,
			wantNames:    []string{"deep-research", "design", "__remote-workflow"},
			why:          "the rung's own discriminant is the commands array, so it reaches emitSlashCommandList and the inventory is emitted with no ModelList beside it (#1891)",
		},
		{
			// THE NULL CARVE-OUT: encoding/json decodes a null into a non-pointer Go
			// value as a NO-OP, so the entry lands with an empty Name and is COUNTED
			// rather than failing the line. The well-formed sibling is what makes the
			// count say 2 instead of agreeing with a decode that dropped the null entry.
			// Since #1891 it says the same about the EMITTED count: the null entry
			// becomes an entry with an empty Name, so both counts read 2.
			//
			// It lived in TestParser_InitializeControlResponseRejectBranches until #1890,
			// which is where a row that decodes and reports 2 never really belonged: it
			// is a commands-only success, so it is a third fixture for exactly this
			// outcome and it moved to sit beside its two.
			name: "an entry's name is null, beside a well-formed sibling",
			inner: map[string]any{"commands": []any{
				commandEntryFixture(nil), commandEntryFixture("deep-research")}},
			wantReason:   "commands_only",
			wantCommands: 2,
			wantEvents:   1,
			why:          "a null name is a no-op decode, so the entry is counted, emitted with an empty Name, and the line is a commands-only success like the row above",
		},
		{
			name:       "neither array",
			inner:      map[string]any{"mode": "default"},
			wantReason: "ack",
			why: "the paired negative for both halves: a success carrying neither array is what `ack` NARROWED to, and it is what stops a mutant that logs commands_only " +
				"— or emits — on every success",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := &logRecorder{}
			var events []turnevent.Event
			p := NewParser(func(ev turnevent.Event) { events = append(events, ev) }, slog.New(rec))
			if _, err := p.Write([]byte(initializeLineFixture(t, "success", tt.inner) + "\n")); err != nil {
				t.Fatalf("Write err = %v, want nil", err)
			}

			if len(events) != tt.wantEvents {
				t.Fatalf("event count: got %d, want %d (%s) — %#v", len(events), tt.wantEvents, tt.why, events)
			}
			if tt.wantEvents == 1 {
				// The type assertion is AC 1's "and no ModelList": a mutant that fell through
				// to the shared models tail emits one of those instead of — or beside — this,
				// and the count above plus this assertion catch it in either arrangement.
				list, ok := events[0].(turnevent.SlashCommandList)
				if !ok {
					t.Fatalf("event 0: got %T, want turnevent.SlashCommandList (%s)", events[0], tt.why)
				}
				if len(list.Commands) != tt.wantCommands {
					t.Errorf("emitted entries: got %d, want %d (%s) — the record's count is taken before "+
						"construction and this one comes out of the emitter's loop",
						len(list.Commands), tt.wantCommands, tt.why)
				}
				if tt.wantNames != nil {
					var names []string
					for _, entry := range list.Commands {
						names = append(names, entry.Name)
					}
					if !reflect.DeepEqual(names, tt.wantNames) {
						t.Errorf("emitted names: got %#v, want %#v (%s) — claude's names are carried "+
							"VERBATIM and in claude's order", names, tt.wantNames, tt.why)
					}
				}
			}
			consumes := rec.withMessage(controlResponseConsumeMsgFixture)
			if len(consumes) != 1 {
				t.Fatalf("records with message %q: got %d, want 1 (%s) — all records: %+v",
					controlResponseConsumeMsgFixture, len(consumes), tt.why, rec.all())
			}
			wantAttrs := map[string]string{
				"type":             "control_response",
				"reason":           tt.wantReason,
				"models":           "0",
				"dropped":          "0",
				"levels_dropped":   "0",
				"commands":         strconv.Itoa(tt.wantCommands),
				"commands_dropped": "0",
			}
			if !reflect.DeepEqual(consumes[0].attrs, wantAttrs) {
				t.Errorf("consume attrs: got %v, want exactly %v (%s)", consumes[0].attrs, wantAttrs, tt.why)
			}
		})
	}
}

// slashCommandNamePreview bounds one name or description for a failure message. The rows below
// compare 253-258 byte strings, and a bare %q of one of those prints two screens of
// `a`s — which turns a single red row into several reading passes for whoever has to
// act on it. Lengths are reported first and separately; this is only ever the second
// half of a message that already said how long the two sides were.
//
// The head is scrubbed with truncateField's own replacement so a preview cut inside a
// rune cannot itself print an invalid sequence.
func slashCommandNamePreview(s string) string {
	const head = 16
	if len(s) <= head {
		return fmt.Sprintf("%q", s)
	}
	return fmt.Sprintf("%q…(%d bytes)", strings.ToValidUTF8(s[:head], ""), len(s))
}

// TestParser_SlashCommandFieldsAreCapped is ALL FOUR per-field bounds' whole boundary
// matrices — FIVE dimensions, the fourth field having two — where each field is cut at
// its OWN cap at construction and the cut is REPORTED, by the DAEMON's snake_case name
// for the field. Three of the four names coincide with the wire name
// protocol.SlashCommand.TruncatedFields documents AND with claude's own key; the hint's
// (#1957) coincides with the wire name while DIFFERING from claude's argumentHint. It is
// named for the FIELDS since #1905, TestParser_ModelListFieldsAreCapped's shape one
// array over: it covered one bound when #1877 built it and covers five now.
//
// THE ALIAS ROWS (#1825) ARE THE FIRST HERE TO GRADE A LIST, and three of their claims
// have no analogue among the scalar rows: that a COUNT cut takes the TAIL so claude's
// order survives, that ONE report name covers TWO cut dimensions and appears at most
// once, and that absent, null and a published [] collapse onto nil. The last of those
// is why the runner compares this field with reflect.DeepEqual and never slices.Equal.
//
// THE HINT'S ROWS CARRY THE KEY/NAME SPLIT and no other rows can, this being the first
// field of the three where claude's key and the daemon's report name are different
// strings: a fixture writing argumentHint against an expectation reading argument_hint
// grades the JSON tag and the report name in one row, and getting either wrong is
// otherwise silent.
//
// The committed-capture pins are NOT here and are not missing either — they are
// TestParser_InitializeControlResponseCutsTheCapturedDescriptions and, since #1958,
// TestParser_InitializeControlResponseCarriesTheCapturedArgumentHints, which grade the
// same two caps against claude's own bytes rather than against constructed fixtures.
// The second of them is this matrix's own creditor: its empty expected report set is a
// measurement only because the hint LIVENESS row below proves the report can fire
// under "argument_hint" at all.
//
// #1877 shipped the first two rows as the bound's liveness proof, so it was never
// unproven for a merge window; #1878 turned them into a table and added the rest.
// #1904 repeated that sequencing one field over — liveness rows first — and #1905
// added the description's boundary rows to match the name's. What each row is FOR is
// in its own `why`, and the three exactly-at-the-cap rows carry the tickets' reason for
// existing: they are the only rows that redden on truncateField's <= becoming <.
//
// THREE such rows rather than one, and none is the sole red for that operator:
// truncateField is SHARED by all three fields, so a flip reddens the three at-cap rows
// together.
// What each of them IS the sole red for is an INLINED per-field cut — the emitter
// replacing that one field's bound call with its own length test — and each row's
// `why` states its own.
//
// MEASURED rather than reasoned, because the reasoning overshoots: a halved
// maxSlashCommandName reddens the name at-cap row, but it reddens the over-cap and
// mid-rune rows too. Their INPUTS do follow the constant down, but their expected
// OUTPUT lengths are written against slashCommandNameCapFixture as well, so the
// fixture literal catches the halving on either side. What is the at-cap row's alone
// is the BOUNDARY OPERATOR, which no input over the cap or well under it can see.
//
// The models array is carried by every row but ONE, because that is the rung this
// matrix was MEASURED on, and since #1891 it is no longer the only rung that could
// carry it: a line with no models lands on the commands_only rung, which emits an
// inventory of its own (TestParser_InitializeControlResponseCommandsOnlyRungEmits pins
// that). Moving the MATRIX there would still buy nothing, because the construction is
// IDENTICAL on both — one emitSlashCommandList, one loop, one maxSlashCommandName — so
// the boundary rows stay on the rung they were measured on and a second copy of them
// would be a second copy of a cap rather than a second proof of it.
//
// What that identity does NOT survive is a RELOCATION of the cut onto the models rung's
// call site: every row above stays green under it while the other rung copies uncut,
// workspace-authored names into a retained list. So exactly one row states its rung
// (#1885), and it is not a copy of the matrix — it is the measurement that the identity
// the paragraph above claims actually holds. A row is otherwise on the models rung by
// omission, which is what the zero value of commandsOnly means.
//
// Not a row here, deliberately: that an over-cap name is DROPPED, reordered,
// lowercased or trimmed. #1600's verbatim rule is carried by the exact equality on the
// entries that fit. The ENTRY-COUNT bound exists since #1826 and is deliberately not a
// row here either: it lives one level up, cut in emitModelList rather than in the
// construction loop this matrix measures, so a row would be asserting a different
// mechanism through the same input. TestParser_SlashCommandEntryCountIsBounded owns it,
// and the separation is what keeps a field-cap row from greening on a count bug.
func TestParser_SlashCommandFieldsAreCapped(t *testing.T) {
	t.Parallel()

	// Runes whose UTF-8 encodings are two and four bytes, so a cut landing inside
	// either leaves a partial rune for the scrub to delete.
	// TestParser_ModelListFieldsAreCapped carries the two-byte row one array over.
	const (
		twoByteRune  = "é"
		fourByteRune = "😀"
	)
	atCap := strings.Repeat("a", slashCommandNameCapFixture)
	// A different fill byte from the name's, so a row carrying both cut fields reddens
	// on the two values being swapped as well as on their report names being.
	descAtCap := strings.Repeat("d", slashCommandDescriptionCapFixture)
	// A third fill byte for the same reason, which is what makes the all-three-cut row
	// grade a swap of any TWO of the three values and not only of their report names.
	hintAtCap := strings.Repeat("h", slashCommandArgumentHintCapFixture)
	// A fourth, and the argument holds one more time. This one is a different LENGTH as
	// well as a different byte, its cap being 64 where the other three are 256, so a row
	// carrying it reddens on a bound call reading the wrong CONSTANT and not only on the
	// values or the report names being swapped.
	aliasAtCap := strings.Repeat("l", slashCommandAliasCapFixture)
	// atCountCap is a full-length alias list, distinct per element so a producer that
	// truncated from the HEAD, reversed, sorted or deduplicated is separated from one
	// that truncated from the tail. Built from the CAP fixture, so the count rows below
	// follow a halved constant to red rather than green.
	atCountCap := make([]any, 0, slashCommandAliasCountCapFixture)
	wantAtCountCap := make([]string, 0, slashCommandAliasCountCapFixture)
	for i := 0; i < slashCommandAliasCountCapFixture; i++ {
		a := fmt.Sprintf("alias-%d", i)
		atCountCap = append(atCountCap, a)
		wantAtCountCap = append(wantAtCountCap, a)
	}

	tests := []struct {
		name string
		// commandsOnly puts the row on the rung that carries NO models array, where the
		// SlashCommandList is the line's only event. The zero value is the models rung,
		// which is where the boundary rows below are measured and where they stay. ONE
		// bool rather than a wantEvents/wantIndex pair: the rung is the fact and the two
		// literals are its consequences, so a pair could state (2, 0) — a combination no
		// rung produces — and a row could disagree with itself about which rung it is on.
		commandsOnly bool
		// entries are claude's per-entry maps, in the order the line carries them.
		entries []any
		// want is the expected emitted entry, index for index.
		want []turnevent.SlashCommand
		why  string
	}{
		{
			name: "over the cap is cut and reported; a name that fits is not",
			entries: []any{
				commandEntryFixture(strings.Repeat("a", slashCommandNameCapFixture+1)),
				commandEntryFixture("deep-research"),
			},
			want: []turnevent.SlashCommand{
				{Name: atCap, TruncatedFields: []string{"name"}},
				{Name: "deep-research"},
			},
			why: "the second entry is what makes the first non-vacuous against a producer naming " +
				"\"name\" on every entry regardless of the cut, and it is simultaneously the " +
				"\"a cut on one entry does not appear on the entries AFTER it\" pin",
		},
		{
			name:         "on the commands-only rung, over the cap is cut and reported",
			commandsOnly: true,
			entries: []any{
				commandEntryFixture(strings.Repeat("a", slashCommandNameCapFixture+1)),
				commandEntryFixture("deep-research"),
			},
			want: []turnevent.SlashCommand{
				{Name: atCap, TruncatedFields: []string{"name"}},
				{Name: "deep-research"},
			},
			why: "the row above's entries and expectations VERBATIM, so the rung is the only variable " +
				"between the two and this row proves that and nothing else. It is the SOLE red — measured, " +
				"not reasoned — against RELOCATING the bound out of emitSlashCommandList onto the models " +
				"rung's call site (the emitter taking a nameLimit, the models rung passing " +
				"maxSlashCommandName and this one an unbounded limit), under which every row above stays " +
				"green while workspace-authored names ride uncut into a retained list. A DELETION of the " +
				"cut does NOT grade this row: it reddens the models rows too",
		},
		{
			name:    "exactly at the cap is NOT truncated",
			entries: []any{commandEntryFixture(atCap), commandEntryFixture("design")},
			want:    []turnevent.SlashCommand{{Name: atCap}, {Name: "design"}},
			why: "THE row this matrix exists for: the only NAME row that reddens on truncateField's <= " +
				"becoming <, since every other name row's input is either over the cap (cut under " +
				"either operator) or far under it (untouched under either). Not the sole red in the " +
				"table since #1905 — truncateField is SHARED, so the description's at-cap row " +
				"reddens on the same flip; what is this row's alone is an INLINED cut of the NAME " +
				"written with <, under which every description row stays green. A halved " +
				"maxSlashCommandName reddens this row too — and the over-cap and mid-rune rows with " +
				"it, their expected OUTPUT lengths being fixture literals — but the boundary " +
				"operator is this row's alone among the name rows",
		},
		{
			name: "a cut landing mid-rune deletes the partial rune (two-byte)",
			entries: []any{
				commandEntryFixture(strings.Repeat("a", slashCommandNameCapFixture-1) + twoByteRune + "z"),
			},
			want: []turnevent.SlashCommand{
				{Name: strings.Repeat("a", slashCommandNameCapFixture-1), TruncatedFields: []string{"name"}},
			},
			why: "one byte UNDER the cap: truncateField's scrub is a DELETION, not a replacement — and " +
				"the report still names the field, because the bool says only whether the cap cut",
		},
		{
			name: "a cut landing mid-rune deletes the partial rune (four-byte)",
			entries: []any{
				commandEntryFixture(strings.Repeat("a", slashCommandNameCapFixture-3) + fourByteRune + "z"),
			},
			want: []turnevent.SlashCommand{
				{Name: strings.Repeat("a", slashCommandNameCapFixture-3), TruncatedFields: []string{"name"}},
			},
			why: "THREE bytes under, which is what makes \"1-3 bytes under the cap\" a range rather than " +
				"a one-byte anecdote — and it is the row a scrub replacing the partial rune instead of " +
				"deleting it fails most visibly",
		},
		{
			name:    "an absent name is still an entry, spelled \"\" or null",
			entries: []any{commandEntryFixture(""), commandEntryFixture(nil), commandEntryFixture("design")},
			want:    []turnevent.SlashCommand{{Name: ""}, {Name: ""}, {Name: "design"}},
			why: "ONE row rather than two, because the claim is that the two absence spellings decode " +
				"ALIKE: \"\" and JSON null both arrive as \"\". The gate is on the ARRAY's length and never " +
				"on an entry's content — per-entry values are never validated beyond the cap, absence being " +
				"claude's to choose — so a producer skipping empty names emits fewer entries and fails the " +
				"count assertion, and the trailing real name is what pins their positions",
		},
		{
			name: "a description over the cap is cut and reported; one that fits is not",
			entries: []any{
				commandEntryWithFixture(commandEntryFixture("deep-research"), "description",
					strings.Repeat("d", slashCommandDescriptionCapFixture+1)),
				commandEntryWithFixture(commandEntryFixture("design"), "description", "plan a change"),
			},
			want: []turnevent.SlashCommand{
				{Name: "deep-research", Description: descAtCap, TruncatedFields: []string{"description"}},
				{Name: "design", Description: "plan a change"},
			},
			why: "the description's LIVENESS row: it is cut at maxSlashCommandDescription and the cut is " +
				"reported under \"description\", the field's own wire name. Both names are far under their " +
				"cap, so \"name\" appearing here at all would be the report naming a field nothing " +
				"happened to. The second entry is what makes the first non-vacuous against a producer " +
				"naming \"description\" on every entry regardless of the cut, and is simultaneously the " +
				"\"a cut on one entry does not appear on the entries AFTER it\" pin for the second bound " +
				"call — the first row's device, one field over",
		},
		{
			name: "both fields cut on ONE entry report in DECLARATION order",
			entries: []any{
				commandEntryWithFixture(
					commandEntryFixture(strings.Repeat("a", slashCommandNameCapFixture+1)),
					"description", strings.Repeat("d", slashCommandDescriptionCapFixture+1)),
				commandEntryWithFixture(commandEntryFixture("design"), "description", "plan a change"),
			},
			want: []turnevent.SlashCommand{
				{Name: atCap, Description: descAtCap, TruncatedFields: []string{"name", "description"}},
				{Name: "design", Description: "plan a change"},
			},
			why: "the ORDER row, and the only one that can be: a TruncatedFields of one name has no order " +
				"to exhibit, which is why the claim was unobservable until this field landed. It is the " +
				"SOLE red against the emitter's two bound calls being swapped — every other row cuts at " +
				"most one field and stays green under that swap — and the two fill bytes differ so a swap " +
				"of the two VALUES reddens here too, not only a swap of their report names. The uncut " +
				"second entry is the same non-vacuity pin the row above carries",
		},
		{
			name: "a null description is still an entry, spelled \"\"",
			entries: []any{
				commandEntryWithFixture(commandEntryFixture("deep-research"), "description", nil),
				commandEntryFixture("design"),
			},
			want: []turnevent.SlashCommand{{Name: "deep-research"}, {Name: "design"}},
			why: "the null carve-out one field over, and it is INHERITED rather than implemented: " +
				"encoding/json unmarshals a null into a non-pointer Go value as a no-op, so the entry is " +
				"ordinary and counted — no panic, no unrecognized_message, no failed line. The second " +
				"entry OMITS the key entirely, which is the absent spelling landing on the same \"\", and " +
				"the trailing position is what pins that neither one was skipped",
		},
		{
			name: "a description exactly at the cap is NOT truncated",
			entries: []any{
				commandEntryWithFixture(commandEntryFixture("deep-research"), "description", descAtCap),
				commandEntryWithFixture(commandEntryFixture("design"), "description", "plan a change"),
			},
			want: []turnevent.SlashCommand{
				{Name: "deep-research", Description: descAtCap},
				{Name: "design", Description: "plan a change"},
			},
			why: "the name at-cap row one field over, and it is NOT the sole red on truncateField's <= " +
				"becoming <: that helper is SHARED, so the flip reddens the name's at-cap row with " +
				"this one. What this row IS the sole red for is the emitter INLINING the " +
				"description's cut — replacing its bound call with a length test of its own, written " +
				"< or otherwise off by one at the boundary — under which every name row stays green " +
				"while a 256-byte workspace description rides in reported as truncated when nothing " +
				"was cut. Every other description row's input is over the cap (cut under either " +
				"operator) or far under it (untouched under either). The trailing short entry is the " +
				"name row's non-vacuity pin, one field over",
		},
		{
			name: "a description cut landing mid-rune deletes the partial rune (two-byte)",
			entries: []any{
				commandEntryWithFixture(commandEntryFixture("deep-research"), "description",
					strings.Repeat("d", slashCommandDescriptionCapFixture-1)+twoByteRune+"z"),
			},
			want: []turnevent.SlashCommand{
				{Name: "deep-research", Description: strings.Repeat("d", slashCommandDescriptionCapFixture-1),
					TruncatedFields: []string{"description"}},
			},
			why: "one byte UNDER the cap: truncateField's scrub is a DELETION, not a replacement — and " +
				"the report still names the field, because the bool says only whether the cap cut. " +
				"Sole red for the emitter INLINING the description's cut with a REPLACEMENT scrub or " +
				"with none; the shared-truncateField version of that mutant reddens the name's " +
				"mid-rune rows too, so this row is A red there and never the only one. CONSTRUCTED " +
				"rather than drawn from the capture because at this cap no captured description " +
				"reaches mid-rune at all — see maxSlashCommandDescription's doc, which owns that " +
				"measurement and is not restated here",
		},
		{
			name: "a description cut landing mid-rune deletes the partial rune (four-byte)",
			entries: []any{
				commandEntryWithFixture(commandEntryFixture("deep-research"), "description",
					strings.Repeat("d", slashCommandDescriptionCapFixture-3)+fourByteRune+"z"),
			},
			want: []turnevent.SlashCommand{
				{Name: "deep-research", Description: strings.Repeat("d", slashCommandDescriptionCapFixture-3),
					TruncatedFields: []string{"description"}},
			},
			why: "THREE bytes under, which is what makes \"1-3 bytes under the cap\" a range rather than " +
				"a one-byte anecdote for this field as well — and it is the row an inlined " +
				"description cut replacing the partial rune instead of deleting it fails most " +
				"visibly. Its CONSTRUCTED input has the row above's reason, and " +
				"maxSlashCommandDescription's doc has the measurement",
		},
		{
			name: "an argument hint over the cap is cut and reported; one that fits is not",
			entries: []any{
				commandEntryWithFixture(commandEntryFixture("deep-research"), "argumentHint",
					strings.Repeat("h", slashCommandArgumentHintCapFixture+1)),
				commandEntryWithFixture(commandEntryFixture("design"), "argumentHint", "<topic> [--deep]"),
			},
			want: []turnevent.SlashCommand{
				{Name: "deep-research", ArgumentHint: hintAtCap, TruncatedFields: []string{"argument_hint"}},
				{Name: "design", ArgumentHint: "<topic> [--deep]"},
			},
			why: "the hint's LIVENESS row, and the ONE place claude's camelCase KEY is graded against the " +
				"daemon's snake_case REPORT NAME at once: the fixture writes argumentHint and the " +
				"expectation reads argument_hint, so a JSON tag spelled argument_hint decodes \"\" on both " +
				"entries and a report name spelled argumentHint fails the first. Both names are far " +
				"under their cap, so \"name\" appearing here would be the report naming a field nothing " +
				"happened to. The second entry carries the bracket syntax a real hint is written in — 13 " +
				"of the capture's 18 non-empty hints do — and is simultaneously the non-vacuity pin " +
				"against a producer naming \"argument_hint\" on every entry and the \"a cut on one entry " +
				"does not appear on the entries AFTER it\" pin for the third bound call",
		},
		{
			name: "an argument hint exactly at the cap is NOT truncated",
			entries: []any{
				commandEntryWithFixture(commandEntryFixture("deep-research"), "argumentHint", hintAtCap),
				commandEntryWithFixture(commandEntryFixture("design"), "argumentHint", "<topic>"),
			},
			want: []turnevent.SlashCommand{
				{Name: "deep-research", ArgumentHint: hintAtCap},
				{Name: "design", ArgumentHint: "<topic>"},
			},
			why: "the other two at-cap rows one field over, and NOT the sole red on truncateField's <= " +
				"becoming <: that helper is SHARED by all three fields, so the flip reddens all three " +
				"at-cap rows together. What this row IS the sole red for is the emitter INLINING the " +
				"HINT's cut — replacing its bound call with a length test of its own, written < or " +
				"otherwise off by one at the boundary — under which every name and description row stays " +
				"green while a 256-byte workspace hint arrives reported as truncated when nothing was " +
				"cut. Every other hint row's input is over the cap (cut under either operator) or far " +
				"under it (untouched under either)",
		},
		{
			name: "an argument hint cut landing mid-rune deletes the partial rune (two-byte)",
			entries: []any{
				commandEntryWithFixture(commandEntryFixture("deep-research"), "argumentHint",
					strings.Repeat("h", slashCommandArgumentHintCapFixture-1)+twoByteRune+"z"),
			},
			want: []turnevent.SlashCommand{
				{Name: "deep-research", ArgumentHint: strings.Repeat("h", slashCommandArgumentHintCapFixture-1),
					TruncatedFields: []string{"argument_hint"}},
			},
			why: "one byte UNDER the cap: truncateField's scrub is a DELETION, not a replacement — and " +
				"the report still names the field, because the bool says only whether the cap cut. Sole " +
				"red WITH the row below and never alone, which is why the claim is scoped to the PAIR: " +
				"the emitter INLINING the hint's cut with a REPLACEMENT scrub reddens both of them and " +
				"— MEASURED, not reasoned — nothing else in this package. The shared-truncateField " +
				"version of that same mutant reddens the name's and the description's mid-rune rows " +
				"and four other tests' besides, so THERE this pair is A red and never the only one. " +
				"CONSTRUCTED rather than drawn from the capture " +
				"because at this cap no captured hint is cut at all — see maxSlashCommandArgumentHint's " +
				"doc, which owns that measurement and is not restated here",
		},
		{
			name: "an argument hint cut landing mid-rune deletes the partial rune (four-byte)",
			entries: []any{
				commandEntryWithFixture(commandEntryFixture("deep-research"), "argumentHint",
					strings.Repeat("h", slashCommandArgumentHintCapFixture-3)+fourByteRune+"z"),
			},
			want: []turnevent.SlashCommand{
				{Name: "deep-research", ArgumentHint: strings.Repeat("h", slashCommandArgumentHintCapFixture-3),
					TruncatedFields: []string{"argument_hint"}},
			},
			why: "THREE bytes under, which is what makes \"1-3 bytes under the cap\" a range rather than " +
				"a one-byte anecdote for this field as well. What it adds over the row above is the " +
				"RANGE and nothing else — the two redden together under every scrub mutant, which is " +
				"why the pair's sole-redness is claimed there once instead of split across both. Its " +
				"CONSTRUCTED input has the row above's reason, and maxSlashCommandArgumentHint's doc " +
				"has the measurement",
		},
		{
			name: "an absent argument hint is still an entry, spelled \"\", null or omitted",
			entries: []any{
				commandEntryWithFixture(commandEntryFixture("deep-research"), "argumentHint", ""),
				commandEntryWithFixture(commandEntryFixture("design"), "argumentHint", nil),
				commandEntryFixture("plan"),
			},
			want: []turnevent.SlashCommand{{Name: "deep-research"}, {Name: "design"}, {Name: "plan"}},
			why: "AN EMPTY HINT IS THE ORDINARY CASE for this field rather than an absence — 33 of the " +
				"capture's 51 entries carry \"\" and NONE omits the key — so what the first entry pins is " +
				"that it arrives as an entry whose hint is EMPTY: not dropped, not defaulted, and not " +
				"made indistinguishable from a missing key by a producer that skipped it. THREE " +
				"spellings in ONE row because the claim is that they decode ALIKE, the null carve-out's " +
				"reading and the absent key's being the empty string's. A producer skipping empty hints " +
				"emits fewer entries and fails the count assertion above; the trailing entries pin that " +
				"none of the three was skipped",
		},
		{
			name: "the hint and the description cut on ONE entry report in DECLARATION order",
			entries: []any{
				commandEntryWithFixture(
					commandEntryWithFixture(commandEntryFixture("deep-research"), "argumentHint",
						strings.Repeat("h", slashCommandArgumentHintCapFixture+1)),
					"description", strings.Repeat("d", slashCommandDescriptionCapFixture+1)),
				commandEntryFixture("design"),
			},
			want: []turnevent.SlashCommand{
				{Name: "deep-research", ArgumentHint: hintAtCap, Description: descAtCap,
					TruncatedFields: []string{"argument_hint", "description"}},
				{Name: "design"},
			},
			why: "the hint's position stated with NO name in the slice, which is what separates a report " +
				"built in CALL order from one built by a fixed sequence that happens to start at " +
				"\"name\": this entry's name is far under its cap, so \"name\" appearing at all would name " +
				"a field nothing happened to. It is A red and NOT the only one against the hint's bound " +
				"call being appended after the description's — the three-name row below reddens on that " +
				"too, and neither row can claim that mutant alone — and the two fill bytes differ so a " +
				"swap of the two VALUES reddens here as well as a swap of their report names",
		},
		{
			name: "an alias over the byte cap is cut and reported; one that fits is not",
			entries: []any{
				commandEntryWithFixture(commandEntryFixture("deep-research"), "aliases",
					[]any{strings.Repeat("l", slashCommandAliasCapFixture+1), "dr"}),
				commandEntryWithFixture(commandEntryFixture("design"), "aliases", []any{"plan"}),
			},
			want: []turnevent.SlashCommand{
				{Name: "deep-research", Aliases: []string{aliasAtCap, "dr"},
					TruncatedFields: []string{"aliases"}},
				{Name: "design", Aliases: []string{"plan"}},
			},
			why: "the alias field's BYTE-dimension LIVENESS row, and the report the whole empty expected " +
				"set of TestParser_InitializeControlResponseCarriesTheCapturedAliases is a measurement " +
				"against. The SECOND element is what makes the cut an ELEMENT cut rather than a list " +
				"one: it fits, it survives at full length, and it stays in claude's position — so a " +
				"producer that DROPPED the over-long element instead of cutting it fails on the list's " +
				"length. The second ENTRY is the non-vacuity pin against a producer naming \"aliases\" " +
				"on every entry and the \"a cut on one entry does not appear on the entries AFTER it\" " +
				"pin for the fourth bound call",
		},
		{
			name: "an alias exactly at the byte cap is NOT truncated",
			entries: []any{
				commandEntryWithFixture(commandEntryFixture("deep-research"), "aliases", []any{aliasAtCap}),
				commandEntryWithFixture(commandEntryFixture("design"), "aliases", []any{"plan"}),
			},
			want: []turnevent.SlashCommand{
				{Name: "deep-research", Aliases: []string{aliasAtCap}},
				{Name: "design", Aliases: []string{"plan"}},
			},
			why: "the other three at-cap rows one field over, and NOT the sole red on truncateField's <= " +
				"becoming <: that helper is SHARED by all four fields, so the flip reddens all four " +
				"at-cap rows together. What this row IS the sole red for is the emitter INLINING the " +
				"ALIAS element cut with its own length test written < or otherwise off by one, under " +
				"which every scalar row stays green while a 64-byte workspace alias arrives reported " +
				"as truncated when nothing was cut. It is NOT the row that catches a bound call " +
				"reading the wrong CONSTANT, which is worth saying because that is the plausible " +
				"guess: MEASURED, passing maxSlashCommandName here leaves this row green — its " +
				"64-byte input is untouched under a 256-byte cap, which is exactly what the row " +
				"expects — and reddens the over-cap, both mid-rune and both multi-field rows instead",
		},
		{
			name: "an alias cut landing mid-rune deletes the partial rune (two-byte)",
			entries: []any{
				commandEntryWithFixture(commandEntryFixture("deep-research"), "aliases",
					[]any{strings.Repeat("l", slashCommandAliasCapFixture-1) + twoByteRune + "z"}),
			},
			want: []turnevent.SlashCommand{
				{Name: "deep-research", Aliases: []string{strings.Repeat("l", slashCommandAliasCapFixture-1)},
					TruncatedFields: []string{"aliases"}},
			},
			why: "one byte UNDER the cap: truncateField's scrub is a DELETION, not a replacement — and " +
				"the report still names the field, because the bool says only whether the cap cut. " +
				"CONSTRUCTED rather than drawn from the capture, and by the widest margin of the four " +
				"fields: no captured alias carries non-ASCII at all and the longest is 9 bytes against " +
				"64, so mid-rune is not merely unreached here but unreachable twice over — see " +
				"maxSlashCommandAlias's doc, which owns that measurement",
		},
		{
			name: "an alias cut landing mid-rune deletes the partial rune (four-byte)",
			entries: []any{
				commandEntryWithFixture(commandEntryFixture("deep-research"), "aliases",
					[]any{strings.Repeat("l", slashCommandAliasCapFixture-3) + fourByteRune + "z"}),
			},
			want: []turnevent.SlashCommand{
				{Name: "deep-research", Aliases: []string{strings.Repeat("l", slashCommandAliasCapFixture-3)},
					TruncatedFields: []string{"aliases"}},
			},
			why: "THREE bytes under, which is what makes \"1-3 bytes under the cap\" a range rather than " +
				"a one-byte anecdote for this field as well. What it adds over the row above is the " +
				"RANGE and nothing else — the two redden together under every scrub mutant — and its " +
				"CONSTRUCTED input has the row above's reason",
		},
		{
			name: "more aliases than the count cap are cut FROM THE TAIL and reported",
			entries: []any{
				commandEntryWithFixture(commandEntryFixture("deep-research"), "aliases",
					append(append([]any{}, atCountCap...), "overflow-a", "overflow-b")),
				commandEntryWithFixture(commandEntryFixture("design"), "aliases", []any{"plan"}),
			},
			want: []turnevent.SlashCommand{
				{Name: "deep-research", Aliases: wantAtCountCap, TruncatedFields: []string{"aliases"}},
				{Name: "design", Aliases: []string{"plan"}},
			},
			why: "the alias field's COUNT-dimension LIVENESS row, the second dimension no other field " +
				"in this entry has. TWO elements over rather than one, so a bound cutting to cap-1 or " +
				"cap+1 is separated from one cutting to cap. The survivors are DISTINCT and in " +
				"claude's own order, which is what separates a tail cut from a head cut, a reversal, " +
				"a sort and a dedup — none of which any equal-length row could see, and a HEAD cut " +
				"is measured to redden here. It is also, MEASURED, the SOLE red for the two mutants " +
				"that make the count cut SILENT: the count bound not raising the report flag at all, " +
				"and the report moved inside the per-element loop where a count-only cut never " +
				"reaches it. Both leave every byte-dimension row green while a workspace's aliases " +
				"go missing from a menu with nothing saying so. The second entry is the non-vacuity " +
				"pin: it carries one alias and must not report",
		},
		{
			name: "exactly at the alias count cap is NOT reported",
			entries: []any{
				commandEntryWithFixture(commandEntryFixture("deep-research"), "aliases", atCountCap),
				commandEntryWithFixture(commandEntryFixture("design"), "aliases", []any{"plan"}),
			},
			want: []turnevent.SlashCommand{
				{Name: "deep-research", Aliases: wantAtCountCap},
				{Name: "design", Aliases: []string{"plan"}},
			},
			why: "the COUNT dimension's boundary row, and it is the sole red for the count bound's > " +
				"becoming >= — which no byte-dimension row can see, truncateField's own <= being a " +
				"different operator in a different statement. Under >= this entry reports \"aliases\" " +
				"on a list nothing happened to, the exact mistake boundEach's own doc names one array " +
				"over. It also reddens on a halved count cap, its input being built from the fixture",
		},
		{
			name: "both alias dimensions cut on ONE entry report \"aliases\" exactly ONCE",
			entries: []any{
				commandEntryWithFixture(commandEntryFixture("deep-research"), "aliases",
					append([]any{strings.Repeat("l", slashCommandAliasCapFixture+1)},
						append(append([]any{}, atCountCap[1:]...), "overflow-a")...)),
			},
			want: []turnevent.SlashCommand{
				{Name: "deep-research",
					Aliases:         append([]string{aliasAtCap}, wantAtCountCap[1:]...),
					TruncatedFields: []string{"aliases"}},
			},
			why: "ONE report name for TWO cut dimensions, which is the property that separates this " +
				"field from the three scalars: the list is over the count cap AND its first element " +
				"is over the byte cap, and \"aliases\" appears exactly once. Its exclusivity was " +
				"CLAIMED and then MEASURED, and the measurement corrected the claim rather than " +
				"confirming it — this row is the SOLE red for nothing tried. An unconditional " +
				"append inside the per-element loop reddens the byte-liveness and count rows too; " +
				"the more faithful once-per-CUT-ELEMENT append reddens the COUNT row ALONE and " +
				"leaves this one green, the count cut being the report it drops; a second report " +
				"raised inside the count block reddens this row and the count row together. So what " +
				"this row adds is COVERAGE of the coincidence, not a unique kill, and every one of " +
				"those mutants is caught by the exact-equality comparison rather than by a " +
				"membership check, which would pass all three",
		},
		{
			name: "absent, null and an empty array all arrive as nil and report nothing",
			entries: []any{
				commandEntryFixture("deep-research"),
				commandEntryWithFixture(commandEntryFixture("design"), "aliases", nil),
				commandEntryWithFixture(commandEntryFixture("plan"), "aliases", []any{}),
				commandEntryWithFixture(commandEntryFixture("loop"), "aliases", []any{"proactive"}),
			},
			want: []turnevent.SlashCommand{
				{Name: "deep-research"}, {Name: "design"}, {Name: "plan"},
				{Name: "loop", Aliases: []string{"proactive"}},
			},
			why: "THE COLLAPSE, and the three spellings are in ONE row because the claim is that they " +
				"read ALIKE: an absent key and a JSON null leave the decoded slice nil, a published " +
				"[] leaves it empty and non-nil, and the emitter normalises all three to nil " +
				"(turnevent.SlashCommand.Aliases). The runner's reflect.DeepEqual is what carries " +
				"this row — slices.Equal reports TRUE for (nil, []string{}) and would pass against " +
				"an emitter that kept the distinction, which is the whole trap the collapse decision " +
				"names. None of the three reports, so the zero-length arm returning BEFORE the count " +
				"bound is graded here too: an [] counted against the cap would still not report, but " +
				"one falling through to the element loop would allocate a non-nil empty slice. The " +
				"trailing real alias pins that no entry was skipped",
		},
		{
			name: "an entry carrying a key the daemon does not declare still decodes",
			entries: []any{
				commandEntryWithFixture(commandEntryFixture("deep-research"), "somethingClaudeAddsLater",
					"whatever"),
				commandEntryFixture("design"),
			},
			want: []turnevent.SlashCommand{{Name: "deep-research"}, {Name: "design"}},
			why: "the tolerance claim that used to be witnessed by the CAPTURE's `aliases` key, moved " +
				"here when #1825 declared that key and falsified the witness. encoding/json ignores " +
				"an unknown key rather than failing, so a key claude adds later costs the daemon a " +
				"gap and not a line — which is what makes commandEntryLine's all-or-nothing rule a " +
				"claim about DECLARED keys specifically. The capture can no longer prove it: its " +
				"fifty-one entries span exactly two key sets and all four keys are now declared",
		},
		{
			name: "all FOUR fields cut on ONE entry report in DECLARATION order",
			entries: []any{
				commandEntryWithFixture(
					commandEntryWithFixture(
						commandEntryWithFixture(
							commandEntryFixture(strings.Repeat("a", slashCommandNameCapFixture+1)),
							"argumentHint", strings.Repeat("h", slashCommandArgumentHintCapFixture+1)),
						"description", strings.Repeat("d", slashCommandDescriptionCapFixture+1)),
					"aliases", []any{strings.Repeat("l", slashCommandAliasCapFixture+1)}),
				commandEntryFixture("design"),
			},
			want: []turnevent.SlashCommand{
				{Name: atCap, ArgumentHint: hintAtCap, Description: descAtCap,
					Aliases:         []string{aliasAtCap},
					TruncatedFields: []string{"name", "argument_hint", "description", "aliases"}},
				{Name: "design"},
			},
			why: "the WHOLE declaration order in one slice, and the SOLE red — measured, not reasoned — " +
				"against the emitter's NAME and HINT bound calls being swapped: the hint+description " +
				"row cuts no name and the name+description row cuts no hint, so both stay green under " +
				"that swap while this one reports [\"argument_hint\", \"name\", ...]. Placing a bound " +
				"call wrongly is the one plausible wrong edit in each of these slices and every " +
				"placement COMPILES, which is why this matrix compares TruncatedFields by exact " +
				"equality and not by membership: every ordering mutant passes a contains check. It " +
				"grew a fourth name in #1825, where the alias call is APPENDED after the three rather " +
				"than inserted between them — the first of the three field slices for which the " +
				"correct placement is the end of the sequence. FOUR distinct fill bytes, so a swap of " +
				"any two VALUES reddens here too. The uncut second entry is the first row's " +
				"non-vacuity pin",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// The models key, the expected event count and the SlashCommandList's index all
			// derive from the row's ONE rung bool, so a row cannot ask for a rung and then
			// assert the other one's shape.
			inner := map[string]any{"commands": tt.entries}
			rung, wantEvents, listIndex := "commands-only", 1, 0
			if !tt.commandsOnly {
				inner["models"] = []map[string]any{modelEntryFixture("claude-sonnet-5", "sonnet", "Sonnet")}
				rung, wantEvents, listIndex = "models", 2, 1
			}
			events := collectEvents(initializeLineFixture(t, "success", inner))
			// The message NAMES THE RUNG, the rung being a row variable now: a count that
			// disagrees says which of emitModelList's two calls to emitSlashCommandList the
			// row asked for, and without it the reader goes to the wrong call site. It is
			// also the deterministic guard on the derivation above — a harness that dropped
			// the models key for every row would move the whole matrix onto the
			// commands-only rung, and the rows above fail here rather than silently
			// re-target their proof.
			if len(events) != wantEvents {
				t.Fatalf("event count on the %s rung: got %d, want %d — %#v",
					rung, len(events), wantEvents, events)
			}
			list, ok := events[listIndex].(turnevent.SlashCommandList)
			if !ok {
				t.Fatalf("event[%d] on the %s rung = %T, want turnevent.SlashCommandList",
					listIndex, rung, events[listIndex])
			}
			// Length FIRST, deliberately, and fatal: the cap cuts a NAME and never an
			// ENTRY, so a producer that dropped one must fail here rather than panic on the
			// index below.
			if len(list.Commands) != len(tt.want) {
				t.Fatalf("SlashCommandList carries %d entries, want %d (%s) — %#v",
					len(list.Commands), len(tt.want), tt.why, list.Commands)
			}
			for i, got := range list.Commands {
				want := tt.want[i]
				// Lengths before values, and a bounded preview when values print at all — see
				// slashCommandNamePreview. The length message alone is what identifies a cut
				// landing at the wrong byte; the equality below is what identifies a repair.
				if len(got.Name) != len(want.Name) {
					t.Errorf("entry %d name: got %d bytes, want %d (%s)",
						i, len(got.Name), len(want.Name), tt.why)
				} else if got.Name != want.Name {
					t.Errorf("entry %d name: got %s, want %s — same length, different bytes; claude's name "+
						"is carried VERBATIM and the cut is a BYTE cut and nothing else",
						i, slashCommandNamePreview(got.Name), slashCommandNamePreview(want.Name))
				}
				// Live on the two mid-rune rows and a no-op on the rest: a scrub that replaced
				// the partial rune would keep this green, which is why the byte length above is
				// the assertion that carries those rows.
				if !utf8.ValidString(got.Name) {
					t.Errorf("entry %d name is not valid UTF-8: %s", i, slashCommandNamePreview(got.Name))
				}
				// The name's assertions again for the third capped field, in the position the
				// field holds on the type so a reader checks one order rather than two. An EMPTY
				// expectation is live here rather than vacuous — it is the majority captured
				// shape — so a producer defaulting an absent hint to anything at all fails the
				// equality.
				if len(got.ArgumentHint) != len(want.ArgumentHint) {
					t.Errorf("entry %d argument hint: got %d bytes, want %d (%s)",
						i, len(got.ArgumentHint), len(want.ArgumentHint), tt.why)
				} else if got.ArgumentHint != want.ArgumentHint {
					t.Errorf("entry %d argument hint: got %s, want %s — same length, different bytes; "+
						"claude's hint is carried VERBATIM and the cut is a BYTE cut and nothing else",
						i, slashCommandNamePreview(got.ArgumentHint), slashCommandNamePreview(want.ArgumentHint))
				}
				// The name's UTF-8 check in its place beside them since #1958, when this field
				// got mid-rune rows for it to grade: live on those two rows and a no-op on the
				// rest. It is NOT what CARRIES them — the byte length above is, a scrub that
				// REPLACED the partial rune keeping this check green while the length moves — so
				// what it adds beside them is the narrower claim that no cut of this field leaves
				// an invalid encoding behind.
				if !utf8.ValidString(got.ArgumentHint) {
					t.Errorf("entry %d argument hint is not valid UTF-8: %s",
						i, slashCommandNamePreview(got.ArgumentHint))
				}
				// The name's assertions again for the second capped field, and for their reason:
				// the length is what identifies a cut landing at the wrong byte and the equality
				// is what identifies a repair.
				if len(got.Description) != len(want.Description) {
					t.Errorf("entry %d description: got %d bytes, want %d (%s)",
						i, len(got.Description), len(want.Description), tt.why)
				} else if got.Description != want.Description {
					t.Errorf("entry %d description: got %s, want %s — same length, different bytes; "+
						"claude's description is carried VERBATIM and the cut is a BYTE cut and nothing else",
						i, slashCommandNamePreview(got.Description), slashCommandNamePreview(want.Description))
				}
				// The name's UTF-8 check in its place beside them since #1905, when this field
				// got mid-rune rows for it to grade: live on those two rows and a no-op on the
				// rest, and a scrub that replaced the partial rune would keep it green — which
				// is why the byte length above is the assertion that carries them.
				if !utf8.ValidString(got.Description) {
					t.Errorf("entry %d description is not valid UTF-8: %s",
						i, slashCommandNamePreview(got.Description))
				}
				// reflect.DeepEqual and deliberately NOT slices.Equal, which is the one place
				// this field's assertions diverge from the three scalars' rather than repeating
				// them: slices.Equal reports TRUE for (nil, []string{}), so it cannot see the
				// collapse turnevent.SlashCommand.Aliases decides, and the absent/null/empty row
				// would pass against an emitter that kept the distinction. It carries the LENGTH,
				// the ORDER and the per-element bytes in one comparison, so the count rows'
				// tail-cut claim and the byte rows' verbatim claim both rest on it.
				if !reflect.DeepEqual(got.Aliases, want.Aliases) {
					t.Errorf("entry %d aliases: got %#v, want %#v (%s) — claude's aliases are carried "+
						"VERBATIM and in claude's order, each cut is a BYTE cut, a count cut is from the "+
						"TAIL, and nil is the one spelling of empty",
						i, got.Aliases, want.Aliases, tt.why)
				}
				// The scalars' UTF-8 check per ELEMENT, live on the two mid-rune rows and a no-op
				// on the rest. As there, it is not what CARRIES them — the equality above is, a
				// scrub that REPLACED the partial rune changing the bytes — so what it adds is the
				// narrower claim that no cut of this field leaves an invalid encoding behind.
				for j, alias := range got.Aliases {
					if !utf8.ValidString(alias) {
						t.Errorf("entry %d alias %d is not valid UTF-8: %s", i, j, slashCommandNamePreview(alias))
					}
				}
				// DeepEqual rather than a length or a contains check, for the models table's
				// reason: nil and []string{} disagree here and only one of them is the contract.
				if !reflect.DeepEqual(got.TruncatedFields, want.TruncatedFields) {
					t.Errorf("entry %d TruncatedFields: got %#v, want %#v (%s)",
						i, got.TruncatedFields, want.TruncatedFields, tt.why)
				}
			}
		})
	}
}

// TestParser_SlashCommandListIsSuppressed is the suppression table: the second gate
// on the MODEL-LIST rung is INDEPENDENT of the models one and decides on the
// `commands` array alone, and the CALL to emitSlashCommandList sits below
// logControlResponse, below the ModelList emit and below BOTH returns in the
// empty-models block — the narrowed ack's and the commands_only rung's. That COUNT is
// unchanged by #1891; what narrowed is which of the two returns belongs to a rung that
// emits nothing. The commands_only rung now has its own call to the emitter, so passing
// its return no longer means passing a silent rung, and what THIS call is unreachable
// from is rungs 1 through 3 — undecodable, nak, and the success carrying neither array.
//
// The first three rows are ONE behaviour rather than three: controlResponseLine's
// Commands is a plain slice precisely so an absent key, a JSON null and a published []
// are one reading, and emitSlashCommandList's gate is a len == 0 test. Stated precisely,
// because the decoded struct does not collapse them the way the gate does — an absent
// key and a null both leave the field nil while a published [] leaves an empty
// non-nil slice. What these rows pin is the GATE's reading of all three, not a claim
// that the shapes are indistinguishable before it.
//
// The last two rows look over-specified and are not. A suppression fixture proves an
// ORDERING only if the two placements disagree on it, which is why the nak row carries
// a non-empty `commands` (an absent or empty one reads zero events under a mutant that
// hoisted the emit above the subtype gate, and proves nothing) and why the undecodable
// row's `commands` array is itself WELL-FORMED with the decode failure in the models
// half (a row whose commands was the malformed value would leave a hoisted emit with
// nothing to emit). encoding/json records the type error and keeps decoding, and
// initializeLineFixture marshals a map — so `commands` decodes before `models` fails
// and a hoisted block would have a populated array available to it.
//
// THE COMMANDS-ONLY RUNG IS THIS TABLE'S SIXTH ROW AND IT LIVES ELSEWHERE:
// TestParser_InitializeControlResponseCommandsOnlyRungEmits owns it, and since #1891
// this table borrows NOTHING from it — that row EMITS now, so it can no longer supply
// a zero-events proof to stand in for a sixth row here. What makes the three
// empty-`commands` rows below complete rather than merely representative is
// STRUCTURAL: the commands_only rung's own discriminant is `commands != 0`, so it
// cannot reach the emitter with an empty array by construction, and the narrowed ack
// rung never calls the emitter at all. Rung 5's call — this table's — is therefore the
// only one that can reach the precondition with an empty slice, and these three rows
// are the whole of that reading.
func TestParser_SlashCommandListIsSuppressed(t *testing.T) {
	t.Parallel()

	models := []map[string]any{modelEntryFixture("claude-sonnet-5", "sonnet", "Sonnet")}

	tests := []struct {
		name       string
		subtype    string
		inner      map[string]any
		wantEvents int
		why        string
	}{
		{
			name:       "commands key absent",
			subtype:    "success",
			inner:      map[string]any{"models": models},
			wantEvents: 1,
			why:        "claude said nothing about commands: the field decodes nil and the gate suppresses",
		},
		{
			name:       "commands null",
			subtype:    "success",
			inner:      map[string]any{"models": models, "commands": nil},
			wantEvents: 1,
			why:        "a JSON null decodes nil exactly as an absent key does, and reaches the same gate",
		},
		{
			name:       "commands empty array",
			subtype:    "success",
			inner:      map[string]any{"models": models, "commands": []any{}},
			wantEvents: 1,
			why: "a published [] decodes to an EMPTY NON-NIL slice, unlike the two rows above — and the " +
				"gate reads len == 0, which is what collapses all three onto one answer. An empty emit " +
				"would assert \"claude offered nothing\" from evidence that cannot tell it apart from " +
				"\"claude said nothing about commands\"",
		},
		{
			name:    "nak rung, non-empty commands",
			subtype: "error",
			inner: map[string]any{"models": models, "commands": []any{
				commandEntryFixture("deep-research"),
			}},
			why: "BOTH arrays non-empty on a FAILURE reply: the row is the sole red against an emit " +
				"block hoisted above the subtype gate, which an absent or empty array could not catch",
		},
		{
			name:    "undecodable rung, valid commands",
			subtype: "success",
			inner: map[string]any{"models": 5, "commands": []any{
				commandEntryFixture("deep-research"),
			}},
			why: "the decode failure is in the MODELS half and the commands array is well-formed and " +
				"non-empty, so a block hoisted above the undecodable return has a populated array " +
				"available to it and reddens",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			events := collectEvents(initializeLineFixture(t, tt.subtype, tt.inner))
			if len(events) != tt.wantEvents {
				t.Errorf("event count: got %d, want %d (%s) — %#v",
					len(events), tt.wantEvents, tt.why, events)
			}
			// A TYPE SCAN over the whole slice rather than an index check, because that is
			// the claim: no SlashCommandList anywhere, whatever the count turns out to be.
			for i, ev := range events {
				if _, ok := ev.(turnevent.SlashCommandList); ok {
					t.Errorf("event[%d] is a turnevent.SlashCommandList; none may be emitted here (%s)", i, tt.why)
				}
			}
			// The models half is UNTOUCHED by the commands gate: on the model_list rung a
			// non-empty models array still produces its ModelList whatever `commands` holds.
			if tt.wantEvents == 1 && len(events) == 1 {
				if _, ok := events[0].(turnevent.ModelList); !ok {
					t.Errorf("event[0] = %T, want turnevent.ModelList — the models half is untouched (%s)",
						events[0], tt.why)
				}
			}
		})
	}
}

// commandEntriesFixture builds n `commands` entries, each identifiable by its index so
// tail-truncation is PINNED rather than assumed from a length. modelEntriesFixture's
// shape, one array over, and []any rather than []map[string]any because that is what
// initializeLineFixture's inner map already carries at every other commands call site.
//
// Every name is far under maxSlashCommandName, which is what lets a row built from this
// exercise the COUNT bound ALONE: a row that wants a field cap too says so by replacing
// a value, and then the two mechanisms are visibly two.
func commandEntriesFixture(n int) []any {
	out := make([]any, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, commandEntryFixture(fmt.Sprintf("command-%03d", i)))
	}
	return out
}

// TestParser_SlashCommandEntryCountIsBounded is #1826's central pin: how many entries
// the list carries is bounded AT CONSTRUCTION and the overflow is reported as a COUNT,
// so the true size stays recoverable as len(Commands) + DroppedCommands and a shortened
// menu is never read as a whole one.
//
// The capture proves neither half and cannot: claude sends fifty-one entries, well under
// the cap, which is exactly the headroom path
// TestParser_InitializeControlResponseCountsTheCapturedCommands pins.
// So these lines are SYNTHESIZED, which invents no field structure — the key is the
// capture's, only the count varies.
//
// EVERY ROW IS ON THE COMMANDS-ONLY RUNG, carrying no models array, and that is the
// choice rather than the default. The cut runs ONCE in emitModelList above both rungs
// that read the array, so a second copy of this matrix on the model-list rung would be a
// second copy of a cap rather than a second proof of it — TestParser_SlashCommandFieldsAreCapped
// makes the same argument for the field caps. What the rung buys instead is AC 3 for
// free: a row whose cap FIRED still reaches this rung, still emits exactly one list, and
// still logs commands_only, so the cap is shown not to move a classification on the one
// rung where a classification could move.
//
// Empty and absent arrays are deliberately NOT rows here. They return at the ack rung
// before the cut ever runs, and TestParser_InitializeControlResponseRejectBranches
// already covers them; a row here would assert the cap against an input it never sees.
func TestParser_SlashCommandEntryCountIsBounded(t *testing.T) {
	t.Parallel()

	t.Run("the entry count is bounded and the overflow is reported", func(t *testing.T) {
		t.Parallel()
		tests := []struct {
			name        string
			entries     []any
			wantLen     int
			wantDropped int
		}{
			{
				name:    "one over the cap drops one",
				entries: commandEntriesFixture(slashCommandListEntriesCapFixture + 1),
				wantLen: slashCommandListEntriesCapFixture, wantDropped: 1,
			},
			{
				// The <= boundary, matching truncateField's convention and both sibling
				// count bounds'. What it discriminates is a cap that fires one entry EARLY:
				// > and >= are indistinguishable here by construction, since at len == cap
				// the block computes a 0 drop and slices to identity either way — unlike
				// boundAliases', where >= would additionally raise the report flag.
				name:    "exactly at the cap carries every entry",
				entries: commandEntriesFixture(slashCommandListEntriesCapFixture),
				wantLen: slashCommandListEntriesCapFixture, wantDropped: 0,
			},
			{
				// The row that makes the report a COUNT rather than a flag: a flag cannot
				// tell 1 lost from 172, and the true size is only recoverable as
				// len(Commands) + DroppedCommands.
				name:    "a large array reports how many were lost",
				entries: commandEntriesFixture(300),
				wantLen: slashCommandListEntriesCapFixture, wantDropped: 300 - slashCommandListEntriesCapFixture,
			},
			{
				// Under the cap, so the bound is not proven only at its own boundary — and
				// at the capture's own fifty-one, which is the count this cap is derived to
				// clear. The hermetic capture test proves the same point against claude's
				// real bytes; this row proves it against the cut in isolation.
				name:    "the captured entry count is untouched",
				entries: commandEntriesFixture(51),
				wantLen: 51, wantDropped: 0,
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				line := initializeLineFixture(t, "success", map[string]any{"commands": tt.entries})
				events := collectEvents(line)
				// Exactly one: this is the commands-only rung, so no ModelList joins it. A
				// cap that fired changes neither the count nor the variant.
				if len(events) != 1 {
					t.Fatalf("event count: got %d, want 1 turnevent.SlashCommandList — %#v", len(events), events)
				}
				list, ok := events[0].(turnevent.SlashCommandList)
				if !ok {
					t.Fatalf("event[0] = %T, want turnevent.SlashCommandList", events[0])
				}

				if len(list.Commands) != tt.wantLen {
					t.Fatalf("len(Commands): got %d, want %d", len(list.Commands), tt.wantLen)
				}
				if list.DroppedCommands != tt.wantDropped {
					t.Errorf("DroppedCommands: got %d, want %d", list.DroppedCommands, tt.wantDropped)
				}
				// Truncation is from the TAIL, preserving claude's order: no ranking is
				// invented, because claude's ordering semantics are unobserved. Pinned per
				// entry rather than assumed from the count, which is what a head-truncating
				// entries[len(entries)-cap:] would otherwise pass.
				for i, got := range list.Commands {
					want := fmt.Sprintf("command-%03d", i)
					if got.Name != want {
						t.Errorf("Commands[%d].Name: got %q, want %q — the survivors are claude's first %d, in order",
							i, got.Name, want, tt.wantLen)
					}
				}
			})
		}
	})

	t.Run("the record names both numbers on the commands-only rung", func(t *testing.T) {
		t.Parallel()
		// THE RUNG WHERE A PARAMETER SWAP IS VISIBLE, which is logControlResponse's own
		// argument for putting these two last: here the model trio is all-zero and this
		// pair is not, where on the ack rung all six integers are 0 and a swap shows
		// nowhere. The two values DIFFER from each other (128 against 172), so a swap
		// BETWEEN them reddens too — equal values would make the pair self-symmetric and
		// the swap invisible again.
		rec := &logRecorder{}
		p := NewParser(func(turnevent.Event) {}, slog.New(rec))
		line := initializeLineFixture(t, "success", map[string]any{"commands": commandEntriesFixture(300)})
		if _, err := p.Write([]byte(line + "\n")); err != nil {
			t.Fatalf("Write err = %v, want nil", err)
		}

		consumes := rec.withMessage(controlResponseConsumeMsgFixture)
		if len(consumes) != 1 {
			t.Fatalf("records with message %q: got %d, want 1 — all records: %+v",
				controlResponseConsumeMsgFixture, len(consumes), rec.all())
		}
		// `commands` is the EMITTED count since #1826, mirroring `models`, where it was
		// the decoded one — and the reason keyword is UNCHANGED by a cap that fired,
		// which is AC 3 read off the record rather than off the event.
		wantAttrs := map[string]string{
			"type":             "control_response",
			"reason":           "commands_only",
			"models":           "0",
			"dropped":          "0",
			"levels_dropped":   "0",
			"commands":         strconv.Itoa(slashCommandListEntriesCapFixture),
			"commands_dropped": strconv.Itoa(300 - slashCommandListEntriesCapFixture),
		}
		if !reflect.DeepEqual(consumes[0].attrs, wantAttrs) {
			t.Errorf("consume attrs: got %v, want exactly %v", consumes[0].attrs, wantAttrs)
		}
	})

	t.Run("the record names both numbers on the model-list rung too", func(t *testing.T) {
		t.Parallel()
		// The SECOND rung that reads the array, and the one the cut is NOT written in.
		// Without this the pair could be computed on the commands-only branch alone and
		// every assertion above would stay green while a line carrying both arrays
		// reported a decoded count and a permanent zero.
		rec := &logRecorder{}
		p := NewParser(func(turnevent.Event) {}, slog.New(rec))
		line := initializeLineFixture(t, "success", map[string]any{
			"models":   modelEntriesFixture(2),
			"commands": commandEntriesFixture(300),
		})
		if _, err := p.Write([]byte(line + "\n")); err != nil {
			t.Fatalf("Write err = %v, want nil", err)
		}

		consumes := rec.withMessage(controlResponseConsumeMsgFixture)
		if len(consumes) != 1 {
			t.Fatalf("records with message %q: got %d, want 1 — all records: %+v",
				controlResponseConsumeMsgFixture, len(consumes), rec.all())
		}
		wantAttrs := map[string]string{
			"type":             "control_response",
			"reason":           "model_list",
			"models":           "2",
			"dropped":          "0",
			"levels_dropped":   "0",
			"commands":         strconv.Itoa(slashCommandListEntriesCapFixture),
			"commands_dropped": strconv.Itoa(300 - slashCommandListEntriesCapFixture),
		}
		if !reflect.DeepEqual(consumes[0].attrs, wantAttrs) {
			t.Errorf("consume attrs: got %v, want exactly %v", consumes[0].attrs, wantAttrs)
		}
	})
}

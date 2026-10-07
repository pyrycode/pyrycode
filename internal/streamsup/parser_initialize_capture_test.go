package streamsup

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// modelFieldCapFixtures record the three model-entry caps as LITERALS,
// deliberately not as maxModelResolved / maxModelValue / maxModelDisplayName. Same
// rule as taskStartedCapCheat: a fixture built from the constant it validates
// asserts nothing about the number — halve the constant and every row below would
// follow it green. These literals are what make such an edit go RED.
//
// Three literals for three constants even though all three are 256, mirroring the
// production split: they bound different fields for different reasons, and sharing
// one fixture would let a change to one silently retarget the others' proof.
//
// modelEffortLevelCapFixture is the same literal rule for a PER-ELEMENT length: it
// bounds one level string of an entry's list, not the list, so it is the number a
// row asserting a cut level's byte count has to be written against.
//
// modelListEntriesCapFixture pins a CARDINALITY rather than a byte length, and
// taskRosterEntriesCapFixture's reasoning carries over unchanged: a count fixture
// written as maxModelListEntries would follow the constant green if someone halved
// it, which is exactly the edit worth catching.
//
// modelEffortLevelCountCapFixture is that same rule for the per-ENTRY cardinality —
// how many levels one entry retains, not how many entries the list does. Written as
// maxModelEffortLevelCount it would follow the constant green if someone halved it,
// and halving that constant is the edit the exactly-at-the-cap row exists to redden.
//
// slashCommandNameCapFixture is the same literal rule one array over, for
// maxSlashCommandName (#1877). Its own fixture even though the number matches the
// three model ones, mirroring the production split: they bound different fields for
// different reasons, and sharing one fixture would let a change to one silently
// retarget the others' proof.
//
// slashCommandDescriptionCapFixture is that rule again for maxSlashCommandDescription
// (#1904), and the pair is where it bites hardest: the two caps sit on adjacent fields
// of the SAME entry and currently hold the same number, so one fixture for both would
// let a change to either budget follow the other's proof green.
//
// slashCommandArgumentHintCapFixture (#1957) makes that pair a TRIO and the argument
// stronger by one: three caps on three adjacent fields of one entry, all three holding
// 256 today, so a single shared fixture would let a change to any one of the three
// budgets follow the other two's proof green.
//
// slashCommandAliasCapFixture and slashCommandAliasCountCapFixture (#1825) are that
// rule for the entry's LIST-valued field, and they are TWO because the production
// constants are two: one bounds a single alias's bytes and one bounds how many aliases
// an entry retains. They are modelEffortLevelCapFixture and
// modelEffortLevelCountCapFixture one array over, and the swap hazard
// maxSlashCommandAliasCount's naming paragraph spends itself on lives here too — a row
// written against the wrong one of the two still compiles. Unlike the trio above these
// two agree with no sibling's number, which is the one thing that makes them easier to
// keep apart than the caps they sit beside.
//
// slashCommandListEntriesCapFixture (#1826) is the rule once more for the bound on the
// LIST rather than on an entry, and it is modelListEntriesCapFixture one array over. It
// is the one fixture in this block that no sibling could be confused with by VALUE — 128
// appears nowhere else here — but the swap hazard it does carry is with
// slashCommandAliasCountCapFixture, the entry's OWN cardinality bound: both are counts,
// both are ints, and a row written against the wrong one still compiles. The names say
// which list each bounds, List against Alias, and that is the whole of what keeps them
// apart.
const (
	modelResolvedCapFixture            = 256
	modelValueCapFixture               = 256
	modelDisplayNameCapFixture         = 256
	modelEffortLevelCapFixture         = 32
	modelEffortLevelCountCapFixture    = 8
	modelListEntriesCapFixture         = 10
	slashCommandNameCapFixture         = 256
	slashCommandArgumentHintCapFixture = 256
	slashCommandDescriptionCapFixture  = 256
	slashCommandAliasCapFixture        = 64
	slashCommandAliasCountCapFixture   = 8
	slashCommandListEntriesCapFixture  = 128
)

// capturedInitializeLine returns one arm's control_response line exactly as claude
// put it on the wire, compacted onto a single line for the parser's line-oriented
// input. Nothing is synthesized: the bytes are the capture's own
// control_responses[0], so the wrapper's subtype, its request_id and the whole
// fourteen-key initialize payload reach consumeLine as claude wrote them.
//
// Compaction is the only transformation, and it is a property of the FILE rather
// than of the wire: the capture is stored pretty-printed, and claude's own line
// carried no interior newline (a stream-json line cannot).
func capturedInitializeLine(t *testing.T, arm string) string {
	t.Helper()
	rec, payload := capturedInitialize(t, arm)
	if payload == nil {
		t.Fatalf("arm %q captured no initialize response, so there is no line to replay", arm)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, rec.ControlResponses[0]); err != nil {
		t.Fatalf("compacting the captured control_response line: %v", err)
	}
	return compact.String()
}

// capturedModelEntries reads one arm's models array as claude's own key/value maps.
//
// It decodes with LITERAL key strings rather than through modelOptionLine, and that
// is the point: an expectation read through the production decode target would
// follow a wrong json tag green, and one transcribed into the test as string
// literals would pin the transcription instead of the decode. These maps are the
// capture's own bytes, keyed by the names claude actually sends.
func capturedModelEntries(t *testing.T, arm string) []map[string]any {
	t.Helper()
	var decoded struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(capturedInitializePayload(t, arm), &decoded); err != nil {
		t.Fatalf("decoding models out of arm %q's initialize payload: %v", arm, err)
	}
	return decoded.Models
}

// capturedModelString pulls one claude-authored string off a captured entry,
// failing when the key is absent or not a string — an expectation that quietly
// became "" would make the comparison below hold for a decode that dropped the
// field.
func capturedModelString(t *testing.T, entry map[string]any, key string) string {
	t.Helper()
	value, ok := entry[key].(string)
	if !ok {
		t.Fatalf("captured entry has no string %q: %#v", key, entry)
	}
	return value
}

// capturedModelBool pulls one claude-authored bool off a captured entry, reporting
// separately whether the key was THERE AT ALL — the two four-key entries carry no
// capability key, and (false, false) is how that reads. Fatal when the key is
// present but not a bool: a capture that changed that is a capture this decode was
// never proven against, and an expectation that quietly became false would make the
// comparison below hold for a decode that dropped the field.
func capturedModelBool(t *testing.T, entry map[string]any, key string) (value, present bool) {
	t.Helper()
	raw, ok := entry[key]
	if !ok {
		return false, false
	}
	value, ok = raw.(bool)
	if !ok {
		t.Fatalf("captured entry's %q is %#v, not a bool: %#v", key, raw, entry)
	}
	return value, true
}

// capturedModelStrings pulls one claude-authored string LIST off a captured entry,
// reporting separately whether the key was THERE AT ALL — the two four-key entries
// carry no capability key, and (nil, false) is how that reads.
//
// Fatal when the key is present but not an array, or when any element is not a
// string: a capture that changed either is a capture this decode was never proven
// against, and an expectation that quietly became empty would make the comparison
// below hold for a decode that dropped the field.
func capturedModelStrings(t *testing.T, entry map[string]any, key string) (values []string, present bool) {
	t.Helper()
	raw, ok := entry[key]
	if !ok {
		return nil, false
	}
	items, ok := raw.([]any)
	if !ok {
		t.Fatalf("captured entry's %q is %#v, not an array: %#v", key, raw, entry)
	}
	values = make([]string, 0, len(items))
	for i, item := range items {
		s, ok := item.(string)
		if !ok {
			t.Fatalf("captured entry's %q[%d] is %#v, not a string: %#v", key, i, item, entry)
		}
		values = append(values, s)
	}
	return values, true
}

// TestParser_InitializeControlResponseDecodesTheCapturedModels is #1811's capture
// pin (AC 4). It replays each responding arm's REAL control_response line through
// the parser and checks the emitted ModelList against the capture's own bytes, so
// what turns it red is a claude-side shape change rather than a drifting fixture.
//
// It runs inside `make check`: the e2e_realclaude build tag governs that package's
// Go FILES, not its testdata, which is #1810's whole reason for existing.
//
// The key-count assertion is the coverage half. claude sends three distinct key
// sets — eight keys, nine for opus (its extra supportsFastMode, which this decode
// must IGNORE rather than trip on), and four for the two entries carrying no
// capability keys at all — and all three must produce a complete ModelOption. A
// four-key entry decoding to a dropped or empty row is the realistic failure, and
// counting the keys is what makes the row's provenance visible instead of assumed.
//
// It is also #1827's AC 1: the same capture proves the level LIST over an entry
// carrying the full five levels and one carrying no capability key at all, against
// claude's own bytes rather than a transcription.
func TestParser_InitializeControlResponseDecodesTheCapturedModels(t *testing.T) {
	t.Parallel()

	for _, arm := range initCaptureArms {
		if arm == initCaptureArmNoRequest {
			continue
		}
		t.Run(initCaptureArmLabel(arm), func(t *testing.T) {
			t.Parallel()

			want := capturedModelEntries(t, arm)
			// The capture's own shape, asserted before it is used as an expectation: six
			// entries whose key counts are exactly the three sets. A re-capture that
			// changed either fails HERE, naming the arm, instead of silently narrowing
			// what the comparison below proves.
			if len(want) != 6 {
				t.Fatalf("the capture carries %d model entries, want 6; a re-capture changed claude's "+
					"reply and this decode was proven against the six-entry shape", len(want))
			}
			keyCounts := make([]int, 0, len(want))
			for _, entry := range want {
				keyCounts = append(keyCounts, len(entry))
			}
			slices.Sort(keyCounts)
			if wantCounts := []int{4, 4, 8, 8, 8, 9}; !slices.Equal(keyCounts, wantCounts) {
				t.Fatalf("captured entries carry key counts %v, want %v (the three key sets: the eight-key "+
					"entries, the nine-key opus, and the two four-key entries)", keyCounts, wantCounts)
			}
			// The same coverage half for #1819's bool: the per-entry comparison proves the
			// TRUE reading only if some captured entry carries supportsAutoMode: true, and
			// the ABSENT reading only if some entry carries no capability key at all. sonnet
			// supplies the first today and haiku the second. Without this guard a re-capture
			// in which every entry carried the key would silently narrow what the loop
			// proves while the loop stayed green.
			var sawAutoModeTrue, sawAutoModeAbsent bool
			for _, entry := range want {
				switch value, present := capturedModelBool(t, entry, "supportsAutoMode"); {
				case present && value:
					sawAutoModeTrue = true
				case !present:
					sawAutoModeAbsent = true
				}
			}
			if !sawAutoModeTrue || !sawAutoModeAbsent {
				t.Fatalf("captured entries supply supportsAutoMode: true on some entry = %v and an entry "+
					"carrying no capability key = %v; both are needed for the comparison below to prove "+
					"both readings (sonnet and haiku are today's suppliers)", sawAutoModeTrue, sawAutoModeAbsent)
			}
			// A third of the same shape for #1827's level LIST: the comparison proves the
			// PUBLISHED reading only if some captured entry carries the full five levels,
			// and the ZERO-LENGTH one only if some entry carries no capability key at all.
			// sonnet supplies the first today and haiku the second. Five is asserted rather
			// than "non-empty" because the count is part of the shape this decode was
			// proven against: a re-capture in which claude published a sixth level, or in
			// which every entry carried the key, fails HERE rather than silently narrowing
			// what the loop proves while the loop stays green.
			var sawFiveEffortLevels, sawEffortLevelsAbsent bool
			for _, entry := range want {
				switch levels, present := capturedModelStrings(t, entry, "supportedEffortLevels"); {
				case present && len(levels) == 5:
					sawFiveEffortLevels = true
				case !present:
					sawEffortLevelsAbsent = true
				}
			}
			if !sawFiveEffortLevels || !sawEffortLevelsAbsent {
				t.Fatalf("captured entries supply the full five supportedEffortLevels on some entry = %v and "+
					"an entry carrying no capability key = %v; both are needed for the comparison below to "+
					"prove both shapes (sonnet and haiku are today's suppliers)",
					sawFiveEffortLevels, sawEffortLevelsAbsent)
			}

			// One row per family (modelfamily.Reduce). The capture publishes haiku AND
			// the pinned claude-haiku-4-5, so the pinned row is not decoded.
			// claude-fable-5[1m] stays, because no fable[1m] row is published and it is
			// its family's only row. Written out rather than derived through Reduce, so a
			// change to the rule reddens here instead of moving the expectation with it.
			want = slices.DeleteFunc(want, func(entry map[string]any) bool {
				return capturedModelString(t, entry, "value") == "claude-haiku-4-5"
			})
			if len(want) != 5 {
				t.Fatalf("after dropping the pinned claude-haiku-4-5 row the capture leaves %d entries, want 5", len(want))
			}

			events := collectEvents(capturedInitializeLine(t, arm))
			// TWO events, not one: every responding arm carries a commands array beside
			// its models one, so the rung emits the SlashCommandList too (#1877). The
			// ModelList stays at index 0 — the models array is the rung's own
			// discriminant, so its emit goes first. This test asserts the models half
			// only; TestParser_InitializeControlResponseCountsTheCapturedCommands asserts
			// the other.
			if len(events) != 2 {
				t.Fatalf("event count: got %d, want 2 (turnevent.ModelList then turnevent.SlashCommandList) — %#v",
					len(events), events)
			}
			list, ok := events[0].(turnevent.ModelList)
			if !ok {
				t.Fatalf("event[0] = %T, want turnevent.ModelList", events[0])
			}
			if len(list.Models) != len(want) {
				t.Fatalf("ModelList carries %d entries, want %d: one per family row, in claude's own order",
					len(list.Models), len(want))
			}
			// The zero-drop path, which is the only one the LIVE shape exercises: six
			// entries sit under maxModelListEntries. A re-capture that pushed claude's
			// list past the cap fails at the six-entry guard above first, so a non-zero
			// here can only mean the cap fired on a list that fits.
			if list.DroppedModels != 0 {
				t.Errorf("DroppedModels: got %d, want 0 — the captured list is under the entry cap",
					list.DroppedModels)
			}

			for i, got := range list.Models {
				// Byte-for-byte against the capture, in claude's own order: index i on both
				// sides is what pins the order as well as the values.
				if wantResolved := capturedModelString(t, want[i], "resolvedModel"); got.ResolvedModel != wantResolved {
					t.Errorf("entry %d ResolvedModel: got %q, want %q (verbatim, no repair)", i, got.ResolvedModel, wantResolved)
				}
				if wantValue := capturedModelString(t, want[i], "value"); got.Value != wantValue {
					t.Errorf("entry %d Value: got %q, want %q (verbatim, no repair)", i, got.Value, wantValue)
				}
				if wantDisplay := capturedModelString(t, want[i], "displayName"); got.DisplayName != wantDisplay {
					t.Errorf("entry %d DisplayName: got %q, want %q (verbatim, no repair)", i, got.DisplayName, wantDisplay)
				}
				// Derived from the capture's bytes, never transcribed, and SPLIT BY PRESENCE
				// because the two halves assert different things. Where claude published the
				// key the list must come back element for element in claude's own order —
				// which (low, medium, high, xhigh, max) is neither alphabetical nor sorted,
				// so this is the no-reordering pin as well as the verbatim one. Where claude
				// sent no key at all the expectation names the SETTLED spelling, nil, rather
				// than the zero length both readings once shared (#1828, argued at
				// turnevent.ModelOption.EffortLevels).
				//
				// Note this arm reads the same under EITHER branch of that decision: the
				// capture carries absent entries and full-five entries and never a published
				// [], so it never had the shape that discriminates the two readings. What
				// proves the collapse is TestParser_ModelListEffortLevelsReadClaudesKey's
				// published-empty row; this pin says only that the daemon's spelling holds
				// against claude's real bytes.
				if wantLevels, present := capturedModelStrings(t, want[i], "supportedEffortLevels"); present {
					if !slices.Equal(got.EffortLevels, wantLevels) {
						t.Errorf("entry %d EffortLevels: got %q, want %q (verbatim, in claude's own order)",
							i, got.EffortLevels, wantLevels)
					}
				} else if got.EffortLevels != nil {
					t.Errorf("entry %d carries no supportedEffortLevels key but decoded to %#v, want nil",
						i, got.EffortLevels)
				}
				// Derived from the capture's bytes, never transcribed: an entry carrying no
				// capability key at all reads false, which is the same reading a present
				// false would get — see turnevent.ModelOption.SupportsAutoMode for why that
				// collapse is deliberate.
				if wantAuto, _ := capturedModelBool(t, want[i], "supportsAutoMode"); got.SupportsAutoMode != wantAuto {
					t.Errorf("entry %d SupportsAutoMode: got %v, want %v (claude's key verbatim; absent reads false)",
						i, got.SupportsAutoMode, wantAuto)
				}
				// nil rather than an empty non-nil slice, and this arm proves the UNTRUNCATED
				// path: no captured field is anywhere near 256 bytes, so a report here would
				// mean the cap fired on a value that fits.
				if got.TruncatedFields != nil {
					t.Errorf("entry %d TruncatedFields: got %v, want nil — no captured field approaches a cap",
						i, got.TruncatedFields)
				}
			}
		})
	}
}

// capturedCommandEntries reads one arm's commands array as claude's own key/value
// maps, with LITERAL key strings rather than through commandEntryLine, for
// capturedModelEntries' reason: an expectation read through the production decode
// target would follow a wrong json tag green.
func capturedCommandEntries(t *testing.T, arm string) []map[string]any {
	t.Helper()
	var decoded struct {
		Commands []map[string]any `json:"commands"`
	}
	if err := json.Unmarshal(capturedInitializePayload(t, arm), &decoded); err != nil {
		t.Fatalf("decoding commands out of arm %q's initialize payload: %v", arm, err)
	}
	return decoded.Commands
}

// capturedCommandString pulls one claude-authored string off a captured command
// entry, failing when the key is absent or not a string — capturedModelString's rule,
// and here it is also how "every captured entry carries a string name" is asserted.
func capturedCommandString(t *testing.T, entry map[string]any, key string) string {
	t.Helper()
	value, ok := entry[key].(string)
	if !ok {
		t.Fatalf("captured command entry has no string %q: %#v", key, entry)
	}
	return value
}

// commandNameIsPlainSlug reports whether every byte of name is in [a-z0-9-] — the
// charset a reader would ASSUME a slash command's name has. It exists to fail the
// assumption rather than to enforce it: the capture carries `__remote-workflow`, and
// the guard below uses this to keep that proof visible after a re-capture.
func commandNameIsPlainSlug(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		switch c := name[i]; {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-':
		default:
			return false
		}
	}
	return true
}

// TestParser_InitializeControlResponseCountsTheCapturedCommands is #1853's capture
// pin (AC 4, first half). It replays each responding arm's REAL control_response line
// and checks the record's `commands` attribute against the capture's own array
// length, so what turns it red is a claude-side shape change rather than a drifting
// fixture. It runs inside `make check` for #1810's reason: the e2e_realclaude build
// tag governs that package's Go FILES, not its testdata.
//
// The capture's own shape is guarded FIRST, with failures naming the arm, before it
// is used as an expectation — TestParser_InitializeControlResponseDecodesTheCapturedModels'
// idiom. Each guard is a proof a re-capture could silently take away:
//
//   - fifty-one entries, every one carrying a STRING name, which is what the count
//     below is a count OF;
//   - EXACTLY TWO distinct key sets across the fifty-one, nine entries carrying
//     `aliases` and forty-two omitting it, which is the shape the decode rests on;
//   - some name outside [a-z0-9-], the committed proof that NO CHARSET may be
//     assumed (`__remote-workflow` supplies it today).
//
// THE SECOND GUARD USED TO SAY SOMETHING ELSE AND #1825 FALSIFIED IT, which is recorded
// rather than quietly edited. It asserted that SOME entry carried `aliases`, as the
// committed proof that an UNDECLARED fourth key decodes rather than failing the line.
// Declaring the field makes that claim false about this key, and the capture holds
// exactly two key sets — so there is no other key to re-point the witness at and the
// claim cannot be re-stated from these bytes at all. It is REPLACED by the capture's
// own two-key-set shape, which is what the new decode actually rests on and is a
// stronger proof a re-capture could take away; the TOLERANCE claim it carried moved to
// a CONSTRUCTED row of TestParser_SlashCommandFieldsAreCapped, where an entry carrying a
// key the daemon does not declare is still shown to decode. Moving a witness from the
// capture to a fixture is the honest edit here; deleting the claim would drop coverage
// this slice did not earn the right to drop.
//
// The guards read the capture only. Nothing here cross-checks the count against
// anything the RECORD supplies: this test is the pin, and a reader that enforced the
// invariant would satisfy it by construction.
//
// `models: 6` sits beside the command count deliberately — it is what kills an
// argument swap between the two ints at the logControlResponse call site.
//
// It is ALSO #1877's captured-line pin for the emit: the same line produces a
// turnevent.SlashCommandList behind the ModelList. `wantAttrs` is deliberately
// UNCHANGED — the record keeps its six attributes and their values, which is what the
// no-seventh-attribute decision buys.
//
// #1878 turned that entry COUNT into a per-name verbatim comparison over all
// fifty-one, index for index against the capture's own bytes, so claude's ORDER is
// pinned alongside the values and every entry's TruncatedFields is asserted nil. The
// ModelList's own per-entry comparison is NOT re-copied here — it stays in
// TestParser_InitializeControlResponseDecodesTheCapturedModels, which this test
// touches in no way; what this one adds beside it is the command inventory.
func TestParser_InitializeControlResponseCountsTheCapturedCommands(t *testing.T) {
	t.Parallel()

	// The one captured name outside [a-z0-9-], as a LITERAL. Written out rather than
	// derived from the capture on purpose: derived, it would follow a re-capture that
	// dropped it and assert nothing, which is the whole failure this pin exists to make
	// loud. commandNameIsPlainSlug's guard below carries the general class claim.
	const nameOutsideSlug = "__remote-workflow"

	for _, arm := range initCaptureArms {
		if arm == initCaptureArmNoRequest {
			continue
		}
		t.Run(initCaptureArmLabel(arm), func(t *testing.T) {
			t.Parallel()

			want := capturedCommandEntries(t, arm)
			if len(want) != 51 {
				t.Fatalf("the capture carries %d command entries, want 51; a re-capture changed "+
					"claude's reply and this count was proven against the fifty-one-entry shape", len(want))
			}
			var (
				sawNameOutsideSlug bool
				aliasCarriers      int
				keySets            = map[string]bool{}
			)
			for _, entry := range want {
				name := capturedCommandString(t, entry, "name")
				if _, ok := entry["aliases"]; ok {
					aliasCarriers++
				}
				keys := make([]string, 0, len(entry))
				for k := range entry {
					keys = append(keys, k)
				}
				slices.Sort(keys)
				keySets[strings.Join(keys, ",")] = true
				if !commandNameIsPlainSlug(name) {
					sawNameOutsideSlug = true
				}
			}
			// The counts as LITERALS, nameOutsideSlug's reason: derived on both sides these
			// guards would follow a re-capture anywhere and assert nothing.
			if aliasCarriers != 9 || len(want)-aliasCarriers != 42 {
				t.Fatalf("captured entries: %d carry `aliases` and %d omit it, want 9 and 42 — the decode "+
					"of the fourth key was proven against that split, and a re-capture that moved it "+
					"changed claude's own reply", aliasCarriers, len(want)-aliasCarriers)
			}
			if len(keySets) != 2 {
				t.Fatalf("captured entries span %d distinct key sets %v, want exactly 2 (the three scalar "+
					"keys, and those plus `aliases`) — a third set means claude added a key this decode "+
					"target does not declare, which is the documented gap protocol.SlashCommand's own "+
					"measurement exists to make visible", len(keySets), keySets)
			}
			if !sawNameOutsideSlug {
				t.Fatalf("captured entries supply no name outside [a-z0-9-]; it is needed for the count " +
					"below to prove what it claims — that no charset is assumed")
			}

			rec := &logRecorder{}
			var events []turnevent.Event
			p := NewParser(func(ev turnevent.Event) { events = append(events, ev) }, slog.New(rec))
			if _, err := p.Write([]byte(capturedInitializeLine(t, arm) + "\n")); err != nil {
				t.Fatalf("Write err = %v, want nil", err)
			}

			// The arm carries both arrays, so it lands on the model-list rung and emits
			// TWO events: the ModelList first — the rung's own discriminant — and the
			// command inventory behind it as a turnevent.SlashCommandList (#1877).
			if len(events) != 2 {
				t.Fatalf("event count: got %d, want 2 (turnevent.ModelList then turnevent.SlashCommandList) — %#v",
					len(events), events)
			}
			if _, ok := events[0].(turnevent.ModelList); !ok {
				t.Fatalf("event[0] = %T, want turnevent.ModelList", events[0])
			}
			list, ok := events[1].(turnevent.SlashCommandList)
			if !ok {
				t.Fatalf("event[1] = %T, want turnevent.SlashCommandList", events[1])
			}
			// THE CAP HAS HEADROOM OVER CLAUDE'S ORDINARY OUTPUT, and this guard is what
			// makes the equality below EVIDENCE of that rather than a coincidence. Since
			// #1826 an entry-count cap exists (maxSlashCommandListEntries), so "every
			// decoded entry is emitted" is now a claim about THIS capture being under it,
			// and the equality alone would hold just as well for a cap of 1 against a
			// one-entry capture. Asserting the strict inequality FIRST is what separates
			// the two readings, and it is the hermetic half of the proof that this cap
			// was derived to clear claude's real workspace rather than only shown to fire
			// on a synthesized one.
			if len(want) >= slashCommandListEntriesCapFixture {
				t.Fatalf("the capture carries %d entries, at or over the %d-entry cap: the assertions "+
					"below would then be proving the CAP rather than the decode, and "+
					"maxSlashCommandListEntries' derivation needs re-deriving against this capture",
					len(want), slashCommandListEntriesCapFixture)
			}
			// Against the capture's OWN array length, never a transcribed 51: the guard
			// above proves this capture under the cap, so every decoded entry is emitted
			// and a re-capture moves this expectation with the fixture.
			//
			// FATAL rather than reported (#1878): the loop below indexes both sides at i,
			// and indexing past the end would turn a producer bug into a panic.
			if len(list.Commands) != len(want) {
				t.Fatalf("SlashCommandList carries %d entries, want %d — one per array element, in claude's own order",
					len(list.Commands), len(want))
			}
			// The other half of "the cap did not fire": a producer that cut AND reported
			// the cut fails the length check above, but one that cut nothing and reported
			// a drop anyway would pass it. 0 is the whole of that claim.
			if list.DroppedCommands != 0 {
				t.Errorf("DroppedCommands: got %d, want 0 — the captured list is under the entry cap",
					list.DroppedCommands)
			}
			var sawNameOutsideSlugEmitted bool
			for i, got := range list.Commands {
				// The capture's own bytes on the right-hand side, never a transcription — and
				// index i on BOTH sides, which is what pins claude's ORDER as well as the
				// values, exactly as the models test's loop does.
				if wantName := capturedCommandString(t, want[i], "name"); got.Name != wantName {
					t.Errorf("entry %d name: got %q, want %q verbatim — no lowercasing, no trimming, "+
						"no charset filtering, no leading \"/\" added or removed", i, got.Name, wantName)
				}
				if got.Name == nameOutsideSlug {
					sawNameOutsideSlugEmitted = true
				}
				// The NAME's untruncated path: the capture's longest name is an order of
				// magnitude under maxSlashCommandName, so "name" appearing here could only mean
				// the cap fired on a value that fits. Mirrors the models test's own
				// untruncated-path assertion.
				//
				// It was `TruncatedFields != nil` until #1904 and could not stay so: the
				// capture's DESCRIPTIONS are not all under their cap, so the whole slice is no
				// longer empty on every entry and the old form would fail on a producer doing
				// exactly what it should. Narrowing it to the one name keeps precisely the claim
				// the message makes. How many captured descriptions report, and which, is the
				// description's committed-capture pin, and asserting it here would be that pin in
				// the wrong test — it is
				// TestParser_InitializeControlResponseCutsTheCapturedDescriptions (#1905), whose
				// subject is the ten entries a cap fired on where this test's is the whole
				// fifty-one-entry inventory. The nil-not-empty-slice contract is
				// unaffected: TestParser_SlashCommandFieldsAreCapped's DeepEqual carries it.
				if slices.Contains(got.TruncatedFields, "name") {
					t.Errorf("entry %d TruncatedFields: got %v, want no \"name\" — no captured name approaches the cap",
						i, got.TruncatedFields)
				}
			}
			if !sawNameOutsideSlugEmitted {
				t.Errorf("the emitted names carry no %q. It is pinned BY NAME rather than left to ride "+
					"anonymously inside the fifty-one comparisons above, because it is the committed proof "+
					"that a name outside [a-z0-9-] crosses the daemon verbatim — and a re-capture that "+
					"swapped it out would fail the comparisons with a message saying nothing about charsets. "+
					"The anonymous class guard (commandNameIsPlainSlug) stands beside this, not instead of it",
					nameOutsideSlug)
			}
			consumes := rec.withMessage(controlResponseConsumeMsgFixture)
			if len(consumes) != 1 {
				t.Fatalf("records with message %q: got %d, want 1 — all records: %+v",
					controlResponseConsumeMsgFixture, len(consumes), rec.all())
			}
			wantAttrs := map[string]string{
				"type":             "control_response",
				"reason":           "model_list",
				"models":           "5", // six captured, the pinned claude-haiku-4-5 reduced away beside haiku
				"dropped":          "0",
				"levels_dropped":   "0",
				"commands":         strconv.Itoa(len(want)),
				"commands_dropped": "0",
			}
			if !reflect.DeepEqual(consumes[0].attrs, wantAttrs) {
				t.Errorf("consume attrs: got %v, want exactly %v", consumes[0].attrs, wantAttrs)
			}
		})
	}
}

// TestParser_InitializeControlResponseCutsTheCapturedDescriptions is #1905's
// committed-capture pin for the DESCRIPTION cap, and it is the pin
// TestParser_InitializeControlResponseCountsTheCapturedCommands defers by name. It
// replays each responding arm's REAL control_response line and asserts what
// maxSlashCommandDescription does to claude's own descriptions: which entries report
// the cut, that the longest arrives cut to the cap VERBATIM, and that the one control
// character the capture contains crosses untouched.
//
// It runs inside `make check` for #1810's reason: the e2e_realclaude build tag governs
// that package's Go FILES, not its testdata. No live claude and no credentials are
// involved on any path here.
//
// A test of its own rather than three more assertions in the count pin, whose own
// comment calls asserting them there "that pin in the wrong test": that test's subject
// is the fifty-one-entry INVENTORY, name for name, and this one's is the ten entries a
// cap fired on.
//
// The capture's own shape is guarded FIRST, naming the arm, before it is used as an
// expectation — TestParser_InitializeControlResponseDecodesTheCapturedModels' idiom —
// and each guard is a proof a re-capture could silently take away:
//
//   - EXACTLY the ten over-cap names in claude's array ORDER, so a re-capture that
//     lengthened an eleventh description fails here rather than quietly turning the
//     emitted-set comparison below into a comparison against whatever arrived;
//   - every one of those ten cutting to exactly the cap, which IS the "at 256 no
//     captured entry is mid-rune reachable" measurement maxSlashCommandDescription's
//     doc records — asserted here rather than restated, and the precondition the
//     verbatim-prefix comparisons below rest on;
//   - claude-api's TWO 0x0a bytes straddling the cap.
//
// The newline claim is deliberately NARROW: 0x0a is the only sub-0x20 byte anywhere
// across the entries' string fields, so what the capture can prove is that THIS
// newline crosses verbatim at its own offset — never that "control characters
// survive", a breadth these bytes do not show. protocol.SlashCommand's doc carries
// that measurement and maxSlashCommandDescription's owns the cap's derivation; neither
// is restated here.
func TestParser_InitializeControlResponseCutsTheCapturedDescriptions(t *testing.T) {
	t.Parallel()

	// The over-cap names as a LITERAL, in claude's own array order, for
	// nameOutsideSlug's reason: derived on both sides, the comparison below would
	// follow a re-capture anywhere and assert nothing. Array order rather than
	// longest-first because ONE pass over the capture produces it, and it pins claude's
	// ordering as a side effect; the derivation beside it below builds the same order.
	wantCut := []string{
		"design", "dataviz", "artifact-capabilities", "update-config", "verify",
		"code-review", "doctor", "claude-api", "run", "run-skill-generator",
	}
	// The two entries pinned BY NAME below rather than left to ride anonymously inside
	// the set comparison, again for nameOutsideSlug's reason: one is the capture's
	// longest description and the other is the only one carrying a control character,
	// and a re-capture that swapped either out would fail the set comparison with a
	// message saying nothing about what was lost.
	const (
		longestName = "dataviz"
		newlineName = "claude-api"
	)

	for _, arm := range initCaptureArms {
		if arm == initCaptureArmNoRequest {
			continue
		}
		t.Run(initCaptureArmLabel(arm), func(t *testing.T) {
			t.Parallel()

			entries := capturedCommandEntries(t, arm)
			var (
				captureCut  []string
				longestDesc string
				newlineDesc string
			)
			for _, entry := range entries {
				name := capturedCommandString(t, entry, "name")
				description := capturedCommandString(t, entry, "description")
				switch name {
				case longestName:
					longestDesc = description
				case newlineName:
					newlineDesc = description
				}
				if len(description) <= slashCommandDescriptionCapFixture {
					continue
				}
				captureCut = append(captureCut, name)
				// truncateField's body written OUT — the byte cut, then the empty-replacement
				// scrub — rather than a call to it: this is a guard on the CAPTURE, so a
				// production mutant must not be able to move it. Landing on exactly the cap for
				// all ten is the mid-rune reachability measurement, and it is what lets the two
				// comparisons below compare against a plain byte prefix of claude's own value.
				cut := strings.ToValidUTF8(description[:slashCommandDescriptionCapFixture], "")
				if len(cut) != slashCommandDescriptionCapFixture {
					t.Fatalf("arm %q: captured %q cuts to %d bytes, want exactly %d — a re-capture put a "+
						"multi-byte rune across the cap, so the reachability this test and "+
						"maxSlashCommandDescription's doc both rest on has changed",
						arm, name, len(cut), slashCommandDescriptionCapFixture)
				}
			}
			if !slices.Equal(captureCut, wantCut) {
				t.Fatalf("arm %q: the captured descriptions over %d bytes are %q, want exactly %q in "+
					"claude's array order; a re-capture changed which entries the cap fires on and "+
					"the comparison below was proven against that set",
					arm, slashCommandDescriptionCapFixture, captureCut, wantCut)
			}
			// The offsets are DERIVED here and transcribed nowhere: the claim is that one
			// newline sits below the cap and one at or above it, which is precisely what makes
			// the emitted value carry exactly ONE of them.
			var newlines []int
			for i := 0; i < len(newlineDesc); i++ {
				if newlineDesc[i] == '\n' {
					newlines = append(newlines, i)
				}
			}
			if len(newlines) != 2 || newlines[0] >= slashCommandDescriptionCapFixture ||
				newlines[1] < slashCommandDescriptionCapFixture {
				t.Fatalf("arm %q: captured %q carries newlines at %v, want exactly two straddling the "+
					"%d-byte cap; a re-capture took away the one control-character proof this test "+
					"rests on", arm, newlineName, newlines, slashCommandDescriptionCapFixture)
			}

			events := collectEvents(capturedInitializeLine(t, arm))
			// TWO events: every responding arm carries a models array beside its commands one,
			// so the ModelList — the rung's own discriminant — goes first and the inventory
			// behind it.
			if len(events) != 2 {
				t.Fatalf("event count: got %d, want 2 (turnevent.ModelList then turnevent.SlashCommandList) — %#v",
					len(events), events)
			}
			list, ok := events[1].(turnevent.SlashCommandList)
			if !ok {
				t.Fatalf("event[1] = %T, want turnevent.SlashCommandList", events[1])
			}
			// FATAL, the count test's reason: the cap cuts a DESCRIPTION and never an ENTRY,
			// so a producer that dropped one must fail here rather than let the lookups below
			// report a missing entry as an empty description.
			if len(list.Commands) != len(entries) {
				t.Fatalf("SlashCommandList carries %d entries, want %d — one per array element, in "+
					"claude's own order", len(list.Commands), len(entries))
			}
			var (
				gotCut     []string
				longestGot turnevent.SlashCommand
				newlineGot turnevent.SlashCommand
			)
			for _, got := range list.Commands {
				if slices.Contains(got.TruncatedFields, "description") {
					gotCut = append(gotCut, got.Name)
				}
				switch got.Name {
				case longestName:
					longestGot = got
				case newlineName:
					newlineGot = got
				}
			}
			// EXACT equality over the whole set, never a per-entry Contains sweep: equality is
			// what makes "and NO OTHERS" structural — a cut that fired on an eleventh entry
			// reddens here without anything having to enumerate the forty-one that must not
			// report, and a cut deleted altogether reddens here as an empty set.
			if !slices.Equal(gotCut, wantCut) {
				t.Errorf("arm %q: the emitted entries reporting \"description\" are %q, want exactly %q — "+
					"the capture's own over-cap set, in claude's array order", arm, gotCut, wantCut)
			}
			// Over the cap by a WIDE margin, asserted against the capture's own length rather
			// than a transcribed 1145: this entry is the committed proof that a real
			// workspace value several times the cap arrives cut and self-reported, which the
			// bare "it is over the cap" the set comparison already carries does not say.
			if len(longestDesc) <= 4*slashCommandDescriptionCapFixture {
				t.Fatalf("arm %q: captured %q is %d bytes, want more than 4x the %d-byte cap — it is "+
					"this test's over-cap-by-a-wide-margin proof and a re-capture shortened it",
					arm, longestName, len(longestDesc), slashCommandDescriptionCapFixture)
			}
			if len(longestGot.Description) != slashCommandDescriptionCapFixture {
				t.Errorf("arm %q: emitted %q description: got %d bytes, want exactly %d (claude's own "+
					"value is %d)", arm, longestName, len(longestGot.Description),
					slashCommandDescriptionCapFixture, len(longestDesc))
			} else if longestGot.Description != longestDesc[:slashCommandDescriptionCapFixture] {
				t.Errorf("arm %q: emitted %q description: got %s, want the capture's own first %d bytes "+
					"%s — a VERBATIM prefix and not merely a value of the right length",
					arm, longestName, slashCommandNamePreview(longestGot.Description),
					slashCommandDescriptionCapFixture,
					slashCommandNamePreview(longestDesc[:slashCommandDescriptionCapFixture]))
			}
			// The SURVIVING newline and only it: the second sits above the cap and is cut
			// away, which is what "exactly one" states. #1600's verbatim rule holding on the
			// one control character the capture actually contains — not stripped, not
			// escaped, not normalised, and at claude's own offset inside a value that is
			// itself cut and self-reported.
			if got := strings.Count(newlineGot.Description, "\n"); got != 1 {
				t.Errorf("arm %q: emitted %q description carries %d newlines, want exactly 1 — claude's "+
					"first (byte %d) is under the cap and survives, its second (byte %d) is above "+
					"the cap and is cut away", arm, newlineName, got, newlines[0], newlines[1])
			} else if at := strings.IndexByte(newlineGot.Description, '\n'); at != newlines[0] {
				t.Errorf("arm %q: emitted %q carries its newline at byte %d, want %d — claude's own "+
					"offset, the value being carried verbatim up to the cut", arm, newlineName, at, newlines[0])
			}
			if newlineGot.Description != newlineDesc[:slashCommandDescriptionCapFixture] {
				t.Errorf("arm %q: emitted %q description: got %s, want the capture's own first %d bytes %s",
					arm, newlineName, slashCommandNamePreview(newlineGot.Description),
					slashCommandDescriptionCapFixture,
					slashCommandNamePreview(newlineDesc[:slashCommandDescriptionCapFixture]))
			}
		})
	}
}

// TestParser_InitializeControlResponseCarriesTheCapturedArgumentHints is #1958's
// committed-capture pin for the ARGUMENT HINT cap, and it is the INVERSE of its sibling
// one field over: TestParser_InitializeControlResponseCutsTheCapturedDescriptions grades
// a cut that fires on ten of the fifty-one entries, and this one grades a cap that fires
// on NONE. What it asserts is therefore SURVIVAL — claude's whole captured array
// crossing whole — rather than a cut landing in the right place.
//
// It runs inside `make check` for #1810's reason: the e2e_realclaude build tag governs
// that package's Go FILES, not its testdata. No live claude and no credentials are
// involved on any path here.
//
// A test of its own rather than more assertions inside
// TestParser_InitializeControlResponseCountsTheCapturedCommands, and the precedent
// points that way twice over. That test's subject is the fifty-one-entry INVENTORY, name
// for name; #1904 NARROWED its TruncatedFields assertion to the one name precisely
// because a whole-slice cap claim did not belong in it, and a second cap outcome
// asserted there would come back in through the door that narrowing closed. It would
// also have to be a per-entry Contains sweep to fit that loop, where what this pin needs
// is EXACT SET EQUALITY — the cut pin's idiom and not the inventory's.
//
// The capture's own shape is guarded FIRST, naming the arm, before it is used as an
// expectation, and each guard is a proof a re-capture could silently take away:
//
//   - fifty-one entries, every one carrying a STRING argumentHint. capturedCommandString
//     supplies the second half by failing when the key is absent or not a string, so
//     "present on all fifty-one and absent from none" needs no guard beside it;
//   - exactly thirty-three of them EMPTY, which keeps "an empty hint is the ORDINARY
//     case for this field" the capture's own fact rather than a transcription;
//   - NONE over the cap. This is what makes the empty expected report set below a
//     measurement of "nothing was cut" instead of a coincidence, and it is where a
//     changed CAPTURE is separated from a broken CUT: a re-capture that lengthened a
//     hint past the cap fails HERE saying exactly that, rather than reddening the
//     comparison below as though the emitter had regressed;
//   - auto-mode-setup pinned BY NAME, nameOutsideSlug's idiom: the capture's longest
//     hint at 121 bytes and the only one carrying a rune wider than one byte.
//
// THE EMPTY EXPECTED SET IS THIS PIN'S ONE VACUITY RISK, named here rather than left for
// a reader to find: slices.Equal against a nil slice also passes against a producer that
// reports nothing at all, for any field. What makes it a measurement is
// TestParser_SlashCommandFieldsAreCapped's hint LIVENESS row, which proves the report
// CAN fire under "argument_hint"; without that row standing beside it this assertion
// would be a tautology dressed as a pin.
//
// What the verbatim comparisons prove is deliberately NARROW, the sibling's newline
// discipline applied to a field whose values are not cut at all: across all fifty-one
// hints the capture carries NO byte below 0x20 and exactly one non-ASCII codepoint,
// U+2026, three times and all inside auto-mode-setup. So what these assertions show is
// that THOSE bytes cross untouched at claude's own offsets — never that "any byte
// survives", a breadth these values do not exhibit. Nothing here says a consumer may
// skip escaping: sanitization is the render boundary's, exactly as maxSlashCommandName's
// doc divides it.
//
// WHAT THIS PIN IS THE SOLE RED FOR, measured rather than reasoned: a producer that
// MANGLES a multi-byte rune in a hint that FITS — non-ASCII stripped, re-encoded or
// normalised below the cap. No row of TestParser_SlashCommandFieldsAreCapped can see
// that, because every hint row's surviving output is ASCII: the mid-rune rows'
// multi-byte rune is the one the cut DELETES, so no constructed row carries a wide rune
// through UNCUT and only claude's own auto-mode-setup does. What this pin is NOT alone
// for is a producer DEFAULTING an empty hint — that mutant reddens the matrix's
// empty/null/omitted row and most of the table with it, commandEntryFixture omitting the
// key on every entry that does not ask for one — so the equality below claims the
// defaulting case as coverage and never as its own.
func TestParser_InitializeControlResponseCarriesTheCapturedArgumentHints(t *testing.T) {
	t.Parallel()

	// The capture's counts as LITERALS, for nameOutsideSlug's reason: derived on both
	// sides, every guard below would follow a re-capture anywhere and assert nothing,
	// which is the whole failure these guards exist to make loud.
	const (
		wantEntries   = 51
		wantEmpty     = 33
		nonASCIIName  = "auto-mode-setup"
		nonASCIIBytes = 121
	)

	for _, arm := range initCaptureArms {
		if arm == initCaptureArmNoRequest {
			continue
		}
		t.Run(initCaptureArmLabel(arm), func(t *testing.T) {
			t.Parallel()

			entries := capturedCommandEntries(t, arm)
			if len(entries) != wantEntries {
				t.Fatalf("arm %q carries %d command entries, want %d; a re-capture changed claude's "+
					"reply and every count below was proven against the fifty-one-entry shape",
					arm, len(entries), wantEntries)
			}
			var (
				empty       int
				nonASCII    string
				sawNonASCII bool
			)
			for _, entry := range entries {
				name := capturedCommandString(t, entry, "name")
				hint := capturedCommandString(t, entry, "argumentHint")
				if hint == "" {
					empty++
				}
				// The whole premise of this pin, asserted per entry rather than over a maximum, so
				// the failure names the entry that moved. A hint over the cap would be CUT, and
				// the empty expected report set below is exactly the claim that none is.
				if len(hint) > slashCommandArgumentHintCapFixture {
					t.Fatalf("arm %q: captured %q carries a %d-byte hint %s, over the %d-byte cap — a "+
						"re-capture lengthened claude's own output past this cap, so what changed is "+
						"the CAPTURE and not the cut, and the empty expected report set below was "+
						"proven against a capture nothing was cut in",
						arm, name, len(hint), slashCommandNamePreview(hint), slashCommandArgumentHintCapFixture)
				}
				if name == nonASCIIName {
					nonASCII, sawNonASCII = hint, true
				}
			}
			if empty != wantEmpty {
				t.Fatalf("arm %q: %d of the captured hints are empty, want exactly %d — the majority-empty "+
					"shape is what the per-entry comparisons below are a proof ABOUT, an empty "+
					"expectation being the one that a defaulting producer fails", arm, empty, wantEmpty)
			}
			// The non-ASCII proof is a property of ONE captured value, so it is guarded by name:
			// a re-capture that shortened auto-mode-setup or swapped its ellipses for "..." would
			// otherwise leave the fifty-one comparisons below passing with nothing multi-byte in
			// them, and no message would say what was lost.
			if !sawNonASCII {
				t.Fatalf("arm %q carries no entry named %q; it is this pin's only source of a hint with a "+
					"rune wider than one byte", arm, nonASCIIName)
			}
			if len(nonASCII) != nonASCIIBytes || utf8.RuneCountInString(nonASCII) == len(nonASCII) {
				t.Fatalf("arm %q: captured %q hint is %d bytes over %d runes %s, want %d bytes over fewer "+
					"runes — a re-capture took away the one multi-byte value this pin rests on",
					arm, nonASCIIName, len(nonASCII), utf8.RuneCountInString(nonASCII),
					slashCommandNamePreview(nonASCII), nonASCIIBytes)
			}

			events := collectEvents(capturedInitializeLine(t, arm))
			// TWO events: every responding arm carries a models array beside its commands one,
			// so the ModelList — the rung's own discriminant — goes first and the inventory
			// behind it.
			if len(events) != 2 {
				t.Fatalf("event count: got %d, want 2 (turnevent.ModelList then turnevent.SlashCommandList) — %#v",
					len(events), events)
			}
			list, ok := events[1].(turnevent.SlashCommandList)
			if !ok {
				t.Fatalf("event[1] = %T, want turnevent.SlashCommandList", events[1])
			}
			// FATAL, the sibling's reason: the cap cuts a HINT and never an ENTRY, so a producer
			// that dropped one must fail here rather than let the index below run off the end.
			// It is also where a producer ELIDING the thirty-three empty-hint entries lands.
			if len(list.Commands) != len(entries) {
				t.Fatalf("SlashCommandList carries %d entries, want %d — one per array element, in "+
					"claude's own order", len(list.Commands), len(entries))
			}
			var (
				gotCut      []string
				nonASCIIGot turnevent.SlashCommand
			)
			for i, got := range list.Commands {
				// The capture's own bytes on the right-hand side and index i on BOTH, which is
				// what pins claude's ORDER alongside the values. EXACT equality, so this one
				// comparison carries all three of "all fifty-one arrive verbatim", "the empty ones
				// arrive as entries carrying an EMPTY hint" and "auto-mode-setup's 121 bytes
				// arrive whole": a defaulted, substituted, re-encoded or trimmed value differs
				// from claude's, and an empty expectation is the one a default cannot satisfy.
				// The RE-ENCODING half of that list is this test's alone; the rest it shares with
				// the matrix, as this test's doc records.
				if wantHint := capturedCommandString(t, entries[i], "argumentHint"); got.ArgumentHint != wantHint {
					t.Errorf("arm %q entry %d (%q) argument hint: got %s, want %s verbatim — no defaulting "+
						"of the empty ones, no substitution, no re-encoding of a multi-byte rune",
						arm, i, got.Name, slashCommandNamePreview(got.ArgumentHint),
						slashCommandNamePreview(wantHint))
				}
				if slices.Contains(got.TruncatedFields, "argument_hint") {
					gotCut = append(gotCut, got.Name)
				}
				if got.Name == nonASCIIName {
					nonASCIIGot = got
				}
			}
			// EXACT equality against a NIL set, never a per-entry Contains sweep, for the
			// sibling's reason: equality is what makes "and NO OTHERS" structural — a cut that
			// fired on any entry reddens here without anything having to enumerate the fifty that
			// must not report. See this test's doc for why an empty expectation is a measurement
			// here and not a tautology.
			var wantCut []string
			if !slices.Equal(gotCut, wantCut) {
				t.Errorf("arm %q: the emitted entries reporting \"argument_hint\" are %q, want NONE — no "+
					"captured hint reaches the %d-byte cap (the guard above holds that), so a report "+
					"here is the cap firing on a value that fits",
					arm, gotCut, slashCommandArgumentHintCapFixture)
			}
			// auto-mode-setup asserted again BY NAME. The equality above already covers this
			// entry, so what this adds is a failure MESSAGE that names it — nameOutsideSlug's
			// argument exactly: the one captured value carrying multi-byte runes should not fail
			// anonymously as "entry 7" in a message saying nothing about encodings.
			if nonASCIIGot.ArgumentHint != nonASCII {
				t.Errorf("arm %q: emitted %q argument hint: got %s, want the capture's own %d bytes %s — "+
					"the only captured hint with runes wider than one byte, carried WHOLE because "+
					"nothing cuts it at this cap",
					arm, nonASCIIName, slashCommandNamePreview(nonASCIIGot.ArgumentHint),
					len(nonASCII), slashCommandNamePreview(nonASCII))
			}
		})
	}
}

// TestParser_InitializeControlResponseCarriesTheCapturedAliases is #1825's
// committed-capture pin for the ALIAS field, and it is the third of the family:
// TestParser_InitializeControlResponseCutsTheCapturedDescriptions grades a cut that
// fires on ten of the fifty-one entries and
// TestParser_InitializeControlResponseCarriesTheCapturedArgumentHints one that fires on
// none. This one grades a cap that fires on none EITHER, in TWO dimensions, over a key
// that is present on only nine entries — so what it asserts is SURVIVAL, and the shape
// it survives as.
//
// It runs inside `make check` for #1810's reason: the e2e_realclaude build tag governs
// that package's Go FILES, not its testdata. No live claude and no credentials are
// involved on any path here.
//
// A test of its own for the argument-hint pin's two reasons, unchanged: the inventory
// test's subject is the fifty-one entries name for name and #1904 NARROWED its
// TruncatedFields assertion precisely so a whole-slice cap claim would not live there,
// and what this pin needs is EXACT SET EQUALITY rather than a per-entry Contains sweep.
//
// The capture's own shape is guarded FIRST, naming the arm, before it is used as an
// expectation, and each guard is a proof a re-capture could silently take away:
//
//   - fifty-one entries, NINE carrying `aliases` and forty-two omitting the key;
//   - ZERO carrying an EMPTY array, which is the measurement the collapse decision on
//     turnevent.SlashCommand.Aliases rests on and the reason it costs nothing observed;
//   - ELEVEN aliases in total, none over the byte cap and no entry over the count cap.
//     This is what makes the empty expected report set below a measurement of "nothing
//     was cut" instead of a coincidence, and it is where a changed CAPTURE is separated
//     from a broken CUT: a re-capture that lengthened an alias past the cap, or gave one
//     entry nine of them, fails HERE saying exactly that;
//   - `clear` and `usage` pinned BY NAME, nameOutsideSlug's idiom. They are the capture's
//     only TWO-alias entries, so they are the whole of its evidence that a list arrives
//     as a list and in claude's own order, and a re-capture that dropped one would leave
//     the fifty-one comparisons below passing with nothing multi-element in them.
//
// THE COLLAPSE CANNOT BE ASSERTED WITH slices.Equal AND THAT IS THIS PIN'S SHARPEST
// TRAP, named here rather than left for a reader to fall into: slices.Equal(nil,
// []string{}) reports TRUE, so a per-entry slices.Equal against a nil expectation passes
// unchanged against a producer that emits an empty non-nil slice for the forty-two
// omitters — and the collapse turnevent.SlashCommand.Aliases decides would be untested
// while looking covered. The explicit `!= nil` check below is what carries it, and it is
// the reason that check exists beside an equality that appears to subsume it.
//
// THE EMPTY EXPECTED REPORT SET IS THIS PIN'S OTHER VACUITY RISK, the argument-hint
// pin's unchanged: an exact equality against a nil set also passes against a producer
// that reports nothing at all, for any field. What makes it a measurement is
// TestParser_SlashCommandFieldsAreCapped's alias LIVENESS rows, which prove the report
// CAN fire under "aliases" on either dimension.
//
// What the verbatim comparisons prove is deliberately NARROW: across all eleven aliases
// the capture carries no byte below 0x20 and NO non-ASCII codepoint at all, the longest
// being 9 bytes. So what these assertions show is that THOSE bytes cross untouched —
// never that "any byte survives", a breadth these values do not exhibit, and never that
// an alias is ASCII by nature, which maxSlashCommandAlias's doc refuses as an argument.
func TestParser_InitializeControlResponseCarriesTheCapturedAliases(t *testing.T) {
	t.Parallel()

	// The capture's counts as LITERALS, for nameOutsideSlug's reason: derived on both
	// sides, every guard below would follow a re-capture anywhere and assert nothing.
	const (
		wantEntries  = 51
		wantCarriers = 9
		wantAliases  = 11
	)
	// The two-alias entries by name, with claude's OWN order inside each. Written out
	// rather than read from the capture for the same reason, and they are the only place
	// this pin can show that ORDER survives at all.
	twoAlias := map[string][]string{
		"clear": {"reset", "new"},
		"usage": {"cost", "stats"},
	}

	for _, arm := range initCaptureArms {
		if arm == initCaptureArmNoRequest {
			continue
		}
		t.Run(initCaptureArmLabel(arm), func(t *testing.T) {
			t.Parallel()

			entries := capturedCommandEntries(t, arm)
			if len(entries) != wantEntries {
				t.Fatalf("arm %q carries %d command entries, want %d; a re-capture changed claude's "+
					"reply and every count below was proven against the fifty-one-entry shape",
					arm, len(entries), wantEntries)
			}
			// want holds the capture's own aliases per entry, nil for an entry omitting the
			// key. Built here so the comparison loop reads the capture once and index i means
			// the same thing on both sides.
			want := make([][]string, len(entries))
			var carriers, total int
			for i, entry := range entries {
				name := capturedCommandString(t, entry, "name")
				raw, ok := entry["aliases"]
				if !ok {
					continue
				}
				carriers++
				list, ok := raw.([]any)
				if !ok {
					t.Fatalf("arm %q: captured %q carries an `aliases` that is %T, want an array — the "+
						"decode target declares []string and a re-capture that changed the shape breaks "+
						"the whole line rather than this entry", arm, name, raw)
				}
				// ZERO empty arrays is the collapse decision's own premise, so it is guarded
				// rather than assumed: an empty one here would mean the distinction the design
				// discards has started being observed.
				if len(list) == 0 {
					t.Fatalf("arm %q: captured %q carries an EMPTY `aliases` array — zero of the fifty-one "+
						"did when turnevent.SlashCommand.Aliases decided to collapse absent and empty "+
						"onto nil, and that decision's whole measurement was this count being zero", arm, name)
				}
				for _, v := range list {
					s, ok := v.(string)
					if !ok {
						t.Fatalf("arm %q: captured %q carries a non-string alias %T", arm, name, v)
					}
					// Per entry rather than over a maximum, so the failure names the entry that
					// moved. An alias over the cap would be CUT, and the empty expected report set
					// below is exactly the claim that none is.
					if len(s) > slashCommandAliasCapFixture {
						t.Fatalf("arm %q: captured %q carries a %d-byte alias %s, over the %d-byte cap — a "+
							"re-capture lengthened claude's own output past this cap, so what changed is "+
							"the CAPTURE and not the cut, and the empty expected report set below was "+
							"proven against a capture nothing was cut in",
							arm, name, len(s), slashCommandNamePreview(s), slashCommandAliasCapFixture)
					}
					want[i] = append(want[i], s)
					total++
				}
				if len(list) > slashCommandAliasCountCapFixture {
					t.Fatalf("arm %q: captured %q carries %d aliases, over the %d cap — the COUNT dimension's "+
						"half of the same separation: what changed is the capture and not the bound",
						arm, name, len(list), slashCommandAliasCountCapFixture)
				}
				if pinned, isPinned := twoAlias[name]; isPinned && !slices.Equal(want[i], pinned) {
					t.Fatalf("arm %q: captured %q aliases are %q, want %q in claude's own order — this is "+
						"one of the capture's only two multi-alias entries and the whole of its evidence "+
						"that a list arrives as a list", arm, name, want[i], pinned)
				}
			}
			if carriers != wantCarriers || total != wantAliases {
				t.Fatalf("arm %q: %d entries carry aliases and %d aliases in total, want %d and %d — the "+
					"nine-carrier, eleven-alias shape is what the comparisons below are a proof ABOUT",
					arm, carriers, total, wantCarriers, wantAliases)
			}

			events := collectEvents(capturedInitializeLine(t, arm))
			// TWO events: every responding arm carries a models array beside its commands one,
			// so the ModelList — the rung's own discriminant — goes first and the inventory
			// behind it.
			if len(events) != 2 {
				t.Fatalf("event count: got %d, want 2 (turnevent.ModelList then turnevent.SlashCommandList) — %#v",
					len(events), events)
			}
			list, ok := events[1].(turnevent.SlashCommandList)
			if !ok {
				t.Fatalf("event[1] = %T, want turnevent.SlashCommandList", events[1])
			}
			// FATAL, the siblings' reason: a cap cuts an ALIAS and never an ENTRY, so a producer
			// that dropped one must fail here rather than let the index below run off the end.
			if len(list.Commands) != len(entries) {
				t.Fatalf("SlashCommandList carries %d entries, want %d — one per array element, in "+
					"claude's own order", len(list.Commands), len(entries))
			}
			var (
				gotCut       []string
				sawOmitter   bool
				gotTwoAlias  = map[string][]string{}
				emittedTotal int
			)
			for i, got := range list.Commands {
				// The capture's own strings on the right-hand side and index i on BOTH, which is
				// what pins claude's ORDER across entries as well as inside a list.
				if !slices.Equal(got.Aliases, want[i]) {
					t.Errorf("arm %q entry %d (%q) aliases: got %q, want %q verbatim and in claude's own "+
						"order — no lowercasing, no trimming, no deduplication, no sorting",
						arm, i, got.Name, got.Aliases, want[i])
				}
				emittedTotal += len(got.Aliases)
				// THE COLLAPSE, and the one assertion slices.Equal above cannot make: it reports
				// TRUE for (nil, []string{}), so without this an emitter returning an empty
				// non-nil slice for the forty-two omitters passes every comparison in this test.
				if want[i] == nil {
					sawOmitter = true
					if got.Aliases != nil {
						t.Errorf("arm %q entry %d (%q) omits `aliases` in the capture but arrived with a "+
							"non-nil %#v — absent, null and a published [] are ONE reading spelled nil "+
							"(turnevent.SlashCommand.Aliases), and slices.Equal cannot see this",
							arm, i, got.Name, got.Aliases)
					}
				}
				if _, isPinned := twoAlias[got.Name]; isPinned {
					gotTwoAlias[got.Name] = got.Aliases
				}
				if slices.Contains(got.TruncatedFields, "aliases") {
					gotCut = append(gotCut, got.Name)
				}
			}
			if !sawOmitter {
				t.Fatalf("arm %q: no emitted entry corresponds to a captured entry omitting `aliases`, so "+
					"the collapse check above never ran; forty-two of the fifty-one should", arm)
			}
			if emittedTotal != wantAliases {
				t.Errorf("arm %q: the emitted entries carry %d aliases in total, want %d — a producer "+
					"dropping or duplicating one inside a list that still compares equal per entry is "+
					"what this total catches", arm, emittedTotal, wantAliases)
			}
			// The two multi-alias entries again BY NAME. The per-entry equality above already
			// covers them, so what this adds is a failure MESSAGE that names them —
			// nameOutsideSlug's argument exactly: the capture's only evidence that ORDER inside a
			// list survives should not fail anonymously as "entry 12".
			for name, pinned := range twoAlias {
				if !slices.Equal(gotTwoAlias[name], pinned) {
					t.Errorf("arm %q: emitted %q aliases: got %q, want the capture's own %q IN ORDER — one "+
						"of only two entries carrying more than one alias, and the reason a consumer "+
						"matching a menu entry against this list finds %q as well as the command name",
						arm, name, gotTwoAlias[name], pinned, pinned[0])
				}
			}
			// EXACT equality against a NIL set, never a per-entry Contains sweep, for the
			// siblings' reason: equality is what makes "and NO OTHERS" structural — a cut that
			// fired on any entry reddens here without anything having to enumerate the fifty that
			// must not report. See this test's doc for why an empty expectation is a measurement
			// here and not a tautology.
			var wantCut []string
			if !slices.Equal(gotCut, wantCut) {
				t.Errorf("arm %q: the emitted entries reporting \"aliases\" are %q, want NONE — no captured "+
					"alias reaches the %d-byte cap and no captured entry reaches the %d-alias cap (the "+
					"guards above hold both), so a report here is a cap firing on a value that fits",
					arm, gotCut, slashCommandAliasCapFixture, slashCommandAliasCountCapFixture)
			}
		})
	}
}

package streamsup

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// TestParser_InitializeControlResponseAbsentPayloadEmitsNothing is AC 3's absent
// case, and its fixture is COMMITTED rather than synthetic: initCaptureArmNoRequest
// is the arm of #1763's measurement that sent no initialize request, so its record
// reads back as an absent payload.
//
// The two halves are asserted separately because neither implies the other: the
// reader handing back nil is a fact about the capture, and a control_response
// carrying no inner response emitting nothing is a fact about the parser.
func TestParser_InitializeControlResponseAbsentPayloadEmitsNothing(t *testing.T) {
	t.Parallel()

	rec, payload := capturedInitialize(t, initCaptureArmNoRequest)
	if payload != nil {
		t.Fatalf("arm %q is the committed absent-payload fixture; the reader returned %d bytes",
			initCaptureArmNoRequest, len(payload))
	}
	if len(rec.ControlResponses) != 0 {
		t.Fatalf("arm %q carries %d control_responses, so it is no longer the absent fixture",
			initCaptureArmNoRequest, len(rec.ControlResponses))
	}

	if events := collectEvents(`{"type":"control_response","response":{"subtype":"success","request_id":"` +
		controlResponseRequestIDFixture + `"}}`); events != nil {
		t.Errorf("a control_response with no inner response emitted %#v, want nothing", events)
	}
}

// modelListLineFixture builds a control_response line around one models array. It
// invents NO field structure — the wrapper is the probe capture's and the entry
// keys are the initialize capture's — and exists only to vary what the capture
// cannot supply: a malformed shape, and values long enough to reach a cap.
//
// models is `any` rather than []map[string]string so a row can put a number or an
// object where the array belongs, which is exactly what the undecodable rung needs.
func modelListLineFixture(t *testing.T, subtype string, models any) string {
	t.Helper()
	return initializeLineFixture(t, subtype, map[string]any{"models": models})
}

// initializeLineFixture is modelListLineFixture with the INNER response opened up,
// so a row can build a payload the models-only wrapper cannot express — a `commands`
// array beside the models one, or one with no models key at all (#1853). The wrapper
// shape stays defined in ONE place: modelListLineFixture delegates here rather than
// marshalling a second copy of it, so a row built either way carries the same
// envelope.
//
// The inner values stay `any` for modelListLineFixture's reason: a row must be able
// to put a number, a string or an object where an array belongs, which is exactly
// what the undecodable rung needs.
func initializeLineFixture(t *testing.T, subtype string, inner map[string]any) string {
	t.Helper()
	line, err := json.Marshal(map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    subtype,
			"request_id": controlResponseRequestIDFixture,
			"response":   inner,
		},
	})
	if err != nil {
		t.Fatalf("marshalling the control_response fixture: %v", err)
	}
	return string(line)
}

// modelEntryFixture builds one entry of that array from the three mapped keys.
func modelEntryFixture(resolvedModel, value, displayName string) map[string]any {
	return map[string]any{
		"resolvedModel": resolvedModel,
		"value":         value,
		"displayName":   displayName,
	}
}

// modelEntryWithFixture returns a COPY of one entry carrying an extra claude key.
// modelEntryFixture's three-parameter signature is deliberately not widened — it
// has many call sites and none of them wants a fourth argument. The value is `any`
// so a row can put a non-bool where a bool belongs, which is the undecodable rung's
// input.
func modelEntryWithFixture(entry map[string]any, key string, value any) map[string]any {
	out := make(map[string]any, len(entry)+1)
	for k, v := range entry {
		out[k] = v
	}
	out[key] = value
	return out
}

// fixtureEntryRaw reports what entry i of a BUILT line actually carries under one
// literal claude key, read back out of that line's own bytes.
//
// It decodes with literal key strings rather than through modelOptionLine, for
// capturedModelEntries' reason: an expectation read through the production decode
// target would follow a wrong json tag green. Here it does a second job, and it does
// it for two tables — with a plain bool on the daemon's type an absent
// supportsAutoMode and a present false are indistinguishable BY DESIGN downstream,
// and an absent supportedEffortLevels, a published [] and a null are all
// zero-length, so a table that only inspected the decoded value would be satisfied
// by a fixture builder that quietly dropped the key, proving one shape twice or
// three times and calling it two or three.
//
// The key is a PARAMETER rather than one copy of this helper per field (#1827): the
// two callers differ only in which literal they read back.
func fixtureEntryRaw(t *testing.T, line string, i int, key string) (raw json.RawMessage, present bool) {
	t.Helper()
	var decoded struct {
		Response struct {
			Response struct {
				Models []map[string]json.RawMessage `json:"models"`
			} `json:"response"`
		} `json:"response"`
	}
	if err := json.Unmarshal([]byte(line), &decoded); err != nil {
		t.Fatalf("decoding the built line with literal keys: %v", err)
	}
	entries := decoded.Response.Response.Models
	if i >= len(entries) {
		t.Fatalf("the built line carries %d entries, want one at index %d", len(entries), i)
	}
	raw, present = entries[i][key]
	return raw, present
}

// TestParser_ModelListSupportsAutoModeReadsClaudesKey is #1819's AC 2: an absent
// key, a JSON null and an explicit false all read as false, and a present true
// reads as true. The choice is stated at turnevent.ModelOption.SupportsAutoMode;
// this is the test that makes it non-vacuous.
//
// The present-FALSE row is HAND-BUILT because claude never sends that shape — the
// committed capture carries four true entries and two carrying no capability key,
// and internal/e2e/internal/fakeclaude's canned list cans exactly those same two.
// So a design keeping absent and false apart could not be shown to keep them apart
// by any fixture in the tree, which is half of why the collapse is the answer.
//
// Non-vacuity comes from the wantWire assertion, which checks what the built line
// ACTUALLY carries before the decode is inspected: absent and present-false differ
// only on the wire under this design, so a builder that dropped the key would
// otherwise turn two rows into one.
//
// Do NOT go looking for a mutant that separates absent from present-false. None
// exists under a plain bool, and that is the design's content rather than a
// coverage gap. The true row is the one with sole redness under a json-tag typo or
// an always-false assignment; the other three pin the collapse.
func TestParser_ModelListSupportsAutoModeReadsClaudesKey(t *testing.T) {
	t.Parallel()

	base := modelEntryFixture("claude-sonnet-5", "sonnet", "Sonnet")
	tests := []struct {
		name  string
		entry map[string]any
		// wantWire is the raw JSON the built line must carry under the literal key,
		// "" meaning the entry must not carry the key at all.
		wantWire string
		want     bool
	}{
		{
			name:     "present and true",
			entry:    modelEntryWithFixture(base, "supportsAutoMode", true),
			wantWire: "true",
			want:     true,
		},
		{
			name:     "present and FALSE — hand-built, claude sends this nowhere",
			entry:    modelEntryWithFixture(base, "supportsAutoMode", false),
			wantWire: "false",
			want:     false,
		},
		{
			// The capture's four-key shape: the entry stops after the mapped strings.
			name:     "absent",
			entry:    base,
			wantWire: "",
			want:     false,
		},
		{
			// encoding/json documents unmarshalling a null into a non-pointer Go value as
			// a no-op producing no error, so this decodes cleanly rather than taking the
			// undecodable rung — a third spelling of the same reading.
			name:     "present and null",
			entry:    modelEntryWithFixture(base, "supportsAutoMode", nil),
			wantWire: "null",
			want:     false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			line := modelListLineFixture(t, "success", []map[string]any{tt.entry})

			raw, present := fixtureEntryRaw(t, line, 0, "supportsAutoMode")
			switch {
			case tt.wantWire == "" && present:
				t.Fatalf("the row declares an ABSENT key but the built line carries %s; the fixture is "+
					"proving a shape the row is not about", raw)
			case tt.wantWire != "" && !present:
				t.Fatalf("the row declares supportsAutoMode: %s but the built line carries no such key",
					tt.wantWire)
			case tt.wantWire != "" && string(raw) != tt.wantWire:
				t.Fatalf("the built line carries supportsAutoMode: %s, want %s", raw, tt.wantWire)
			}

			events := collectEvents(line)
			if len(events) != 1 {
				t.Fatalf("event count: got %d, want 1 turnevent.ModelList — %#v", len(events), events)
			}
			list, ok := events[0].(turnevent.ModelList)
			if !ok {
				t.Fatalf("event[0] = %T, want turnevent.ModelList", events[0])
			}
			if len(list.Models) != 1 {
				t.Fatalf("ModelList carries %d entries, want 1", len(list.Models))
			}
			if got := list.Models[0].SupportsAutoMode; got != tt.want {
				t.Errorf("SupportsAutoMode: got %v, want %v for a wire value of %q", got, tt.want, tt.wantWire)
			}
			// A bool is never cut, so it is never named here — asserted rather than left
			// to the capture pin, since this is the only test that feeds the key at all.
			if got := list.Models[0].TruncatedFields; got != nil {
				t.Errorf("TruncatedFields: got %v, want nil — a bool has no length to cut", got)
			}
		})
	}
}

// TestParser_ModelListEffortLevelsReadClaudesKey is #1827's decode pin, tightened by
// #1828: claude's list arrives verbatim and IN CLAUDE'S ORDER, and an absent key, a
// published empty array and a JSON null all decode to nil.
//
// The three zero-length rows assert == nil rather than len(...) == 0, because the
// reading is now settled and the SPELLING is what carries it: one reading of a
// zero-length effort menu, spelled nil, argued at
// turnevent.ModelOption.EffortLevels. A length-only assertion would leave the half
// this slice decided unpinned, and it is why those rows keep their own switch arm —
// slices.Equal(nil, []string{}) reports true, so folding them into the equality arm
// would make the assertion blind to the thing it exists to pin.
//
// A separating mutant now EXISTS, which inverts what this doc said while the
// question was open. Both mutants below were RUN against internal/streamsup rather
// than predicted, per this package's own lesson that a sole-redness claim is a
// measurable claim and is usually wrong until measured:
//
//   - Reverting the decision — boundEach's zero-length arm returning its input
//     unchanged — reddens `published empty` ALONE, package-wide. Nothing else in the
//     tree feeds a published [], so this row is the whole of the decision's coverage.
//   - Collapsing the other way — that arm returning []string{} — reddens all THREE
//     zero-length rows, not the two the shape suggests: `published empty` goes red
//     with `absent` and `present and null` because the assertion is == nil and the
//     mutant returns non-nil on every one of them. It also reddens all three arms of
//     TestParser_InitializeControlResponseDecodesTheCapturedModels, whose haiku
//     entries carry no key. So it is NOT sole-red anywhere, and this comment says so
//     rather than telling the tidier story the row names invite.
//
// The scrambled-order row is the only red under a producer that CANONICALISED into
// claude's published order, which is the reordering a five-level row cannot catch
// because it is already in that order. A producer that SORTED reddens both rows, so
// the scrambled one earns its place against the narrower mutant rather than the
// obvious one.
//
// The wantWire assertion is the ROW-DISTINCTNESS pin, not decoration, and it binds
// harder after the collapse than before: with all three zero-length rows now
// decoding to the identical nil, what the built line ACTUALLY carries is the only
// thing making them three rows rather than one shape proved three times. A fixture
// builder that quietly dropped the key would otherwise be invisible. Do not merge
// the rows on the grounds that they share an expectation — that merge is what this
// guard exists to prevent. Measured, not assumed: dropping the key from the
// published-empty row's fixture reddens that row on the wire assertion alone, while
// its decoded-value assertion passes, nil being what both shapes now produce.
func TestParser_ModelListEffortLevelsReadClaudesKey(t *testing.T) {
	t.Parallel()

	base := modelEntryFixture("claude-sonnet-5", "sonnet", "Sonnet")
	tests := []struct {
		name  string
		entry map[string]any
		// wantWire is the raw JSON the built line must carry under the literal key,
		// "" meaning the entry must not carry the key at all.
		wantWire string
		// want is nil on every row whose expectation is the SETTLED zero-length
		// spelling rather than a particular list, and those rows assert == nil — see
		// this test's doc.
		want []string
	}{
		{
			name: "claude's five levels, in claude's order",
			entry: modelEntryWithFixture(base, "supportedEffortLevels",
				[]any{"low", "medium", "high", "xhigh", "max"}),
			wantWire: `["low","medium","high","xhigh","max"]`,
			want:     []string{"low", "medium", "high", "xhigh", "max"},
		},
		{
			// Neither claude's published order nor a sorted one, which is what makes this
			// the row a producer that sorted or de-duplicated fails on alone.
			name:     "a scrambled order is carried, not sorted",
			entry:    modelEntryWithFixture(base, "supportedEffortLevels", []any{"max", "low", "high"}),
			wantWire: `["max","low","high"]`,
			want:     []string{"max", "low", "high"},
		},
		{
			// The capture's four-key shape: the entry stops after the mapped strings.
			name:     "absent",
			entry:    base,
			wantWire: "",
			want:     nil,
		},
		{
			// The one row the producer actually normalises: encoding/json lands a []
			// on an empty NON-NIL slice, and boundEach's zero-length arm turns it into
			// nil so all three zero-length wire shapes read alike.
			name:     "published empty",
			entry:    modelEntryWithFixture(base, "supportedEffortLevels", []any{}),
			wantWire: "[]",
			want:     nil,
		},
		{
			// encoding/json documents unmarshalling a null into a non-pointer Go value as
			// a no-op producing no error, so this decodes cleanly rather than taking the
			// undecodable rung — a third spelling of the same zero-length shape.
			name:     "present and null",
			entry:    modelEntryWithFixture(base, "supportedEffortLevels", nil),
			wantWire: "null",
			want:     nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			line := modelListLineFixture(t, "success", []map[string]any{tt.entry})

			raw, present := fixtureEntryRaw(t, line, 0, "supportedEffortLevels")
			switch {
			case tt.wantWire == "" && present:
				t.Fatalf("the row declares an ABSENT key but the built line carries %s; the fixture is "+
					"proving a shape the row is not about", raw)
			case tt.wantWire != "" && !present:
				t.Fatalf("the row declares supportedEffortLevels: %s but the built line carries no such key",
					tt.wantWire)
			case tt.wantWire != "" && string(raw) != tt.wantWire:
				t.Fatalf("the built line carries supportedEffortLevels: %s, want %s", raw, tt.wantWire)
			}

			events := collectEvents(line)
			if len(events) != 1 {
				t.Fatalf("event count: got %d, want 1 turnevent.ModelList — %#v", len(events), events)
			}
			list, ok := events[0].(turnevent.ModelList)
			if !ok {
				t.Fatalf("event[0] = %T, want turnevent.ModelList", events[0])
			}
			if len(list.Models) != 1 {
				t.Fatalf("ModelList carries %d entries, want 1", len(list.Models))
			}
			got := list.Models[0].EffortLevels
			switch {
			case tt.want == nil:
				// == nil rather than len(...) == 0, and its OWN arm rather than a
				// slices.Equal against a nil want: slices.Equal(nil, []string{}) reports
				// true, so the equality arm is blind to exactly the spelling this row pins.
				if got != nil {
					t.Errorf("EffortLevels: got %#v, want nil for a wire value of %q — a zero-length "+
						"menu is spelled nil, per turnevent.ModelOption.EffortLevels", got, tt.wantWire)
				}
			case !slices.Equal(got, tt.want):
				t.Errorf("EffortLevels: got %q, want %q (verbatim, in claude's own order)", got, tt.want)
			}
			// Nothing here is near the element cap, so nothing is named — the cut report
			// has its own table.
			if cut := list.Models[0].TruncatedFields; cut != nil {
				t.Errorf("TruncatedFields: got %v, want nil — no level here approaches its cap", cut)
			}
		})
	}
}

// modelEntriesFixture builds n entries, each identifiable by its resolvedModel so
// tail-truncation is PINNED rather than assumed. rosterEntriesFixture's shape.
func modelEntriesFixture(n int) []map[string]any {
	out := make([]map[string]any, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, modelEntryFixture(
			fmt.Sprintf("claude-model-%03d", i),
			fmt.Sprintf("alias-%03d", i),
			fmt.Sprintf("Model %03d", i),
		))
	}
	return out
}

// TestParser_InitializeControlResponseRejectBranches is AC 3: every malformed or
// non-initialize control_response is dropped with one debug record and produces
// NOTHING — never an event, and in particular never an Unrecognized, which would
// put a line the daemon does recognise in front of the live zero-unrecognized gate.
//
// The load-bearing row is "a NAK carrying a well-formed models array". Without it
// the subtype half of the gate is decorative: every other rejected row is already
// excluded by having no usable array, so that row alone fails when the subtype
// comparison is deleted.
//
// The wantAttrs comparison below is also what covers #1819's and #1827's "no log
// line carries decoded content": it is an EXACT map equality, so any attribute added
// to logControlResponse — supportsAutoMode and supportedEffortLevels included —
// turns every row here red. That half needs no assertion of its own, and this note
// exists so the coverage is visible rather than assumed. It matters most for the
// level list: an element-level type error quotes the offending array element, so an
// undecodable arm that logged its err would route claude's own level strings into
// the daemon log, which is the field #833's "effort values are NEVER logged" names.
func TestParser_InitializeControlResponseRejectBranches(t *testing.T) {
	t.Parallel()

	entry := modelEntryFixture("claude-sonnet-5", "sonnet", "Sonnet")
	tests := []struct {
		name string
		line string
		// No wantCommands field: since #1890 relocated the one row that decoded a
		// non-empty array, EVERY row here reports 0 — the rejected ones because the
		// array never decoded or the subtype refused it, the ack ones because their
		// `commands` is absent, null or empty. The attribute is hardcoded below.
		wantReason string
	}{
		{
			name:       "models is a number",
			line:       modelListLineFixture(t, "success", 5),
			wantReason: "undecodable",
		},
		{
			name:       "models is an object",
			line:       modelListLineFixture(t, "success", map[string]any{}),
			wantReason: "undecodable",
		},
		{
			name:       "an entry's value is not a string",
			line:       modelListLineFixture(t, "success", []map[string]any{{"value": 5}}),
			wantReason: "undecodable",
		},
		{
			// #1819: the bool fails the WHOLE-LINE decode exactly as a non-string value
			// does, by the same *json.UnmarshalTypeError, so no partial list is emitted.
			// "true" is the load-bearing shape here — a hand-written client or a future
			// claude sending the bool as a string is the realistic way this arrives.
			name:       "an entry's supportsAutoMode is a string",
			line:       modelListLineFixture(t, "success", []map[string]any{modelEntryWithFixture(entry, "supportsAutoMode", "true")}),
			wantReason: "undecodable",
		},
		{
			name:       "an entry's supportsAutoMode is a number",
			line:       modelListLineFixture(t, "success", []map[string]any{modelEntryWithFixture(entry, "supportsAutoMode", 1)}),
			wantReason: "undecodable",
		},
		{
			name:       "an entry's supportsAutoMode is an object",
			line:       modelListLineFixture(t, "success", []map[string]any{modelEntryWithFixture(entry, "supportsAutoMode", map[string]any{})}),
			wantReason: "undecodable",
		},
		{
			// #1827: a supportedEffortLevels that is a scalar where an array belongs is
			// the shape a hand-written client or a future claude is most likely to send.
			name:       "an entry's supportedEffortLevels is a string",
			line:       modelListLineFixture(t, "success", []map[string]any{modelEntryWithFixture(entry, "supportedEffortLevels", "low")}),
			wantReason: "undecodable",
		},
		{
			// The family's first ELEMENT-level mismatch: the WHOLE LINE fails, so no
			// partial list is emitted carrying only the elements that happened to decode.
			// A menu that silently lost an element would be published as claude's
			// complete one.
			name:       "an entry's supportedEffortLevels carries a non-string element",
			line:       modelListLineFixture(t, "success", []map[string]any{modelEntryWithFixture(entry, "supportedEffortLevels", []any{"low", 5})}),
			wantReason: "undecodable",
		},
		{
			name:       "the inner response is a string",
			line:       `{"type":"control_response","response":{"subtype":"success","response":"text"}}`,
			wantReason: "undecodable",
		},
		{
			name:       "the outer response is a string",
			line:       `{"type":"control_response","response":"text"}`,
			wantReason: "undecodable",
		},
		{
			name:       "THE SUBTYPE HALF: a NAK carrying a well-formed models array",
			line:       modelListLineFixture(t, "error", []map[string]any{entry}),
			wantReason: "nak",
		},
		{
			name:       "a subtype claude invents next year",
			line:       modelListLineFixture(t, "a_subtype_invented_next_year", []map[string]any{entry}),
			wantReason: "nak",
		},
		{
			name:       "models is null",
			line:       modelListLineFixture(t, "success", nil),
			wantReason: "ack",
		},
		{
			name:       "models is an empty array",
			line:       modelListLineFixture(t, "success", []map[string]any{}),
			wantReason: "ack",
		},
		{
			name:       "models is absent",
			line:       `{"type":"control_response","response":{"subtype":"success","response":{"mode":"default"}}}`,
			wantReason: "ack",
		},
		// #1853's rows. The array is DECLARED, so each of these shapes now fails the
		// whole-line decode where before it was an undeclared key and silently ignored.
		{
			name:       "commands is a number",
			line:       initializeLineFixture(t, "success", map[string]any{"commands": 5}),
			wantReason: "undecodable",
		},
		{
			name:       "commands is a string",
			line:       initializeLineFixture(t, "success", map[string]any{"commands": "deep-research"}),
			wantReason: "undecodable",
		},
		{
			name:       "commands is an object",
			line:       initializeLineFixture(t, "success", map[string]any{"commands": map[string]any{}}),
			wantReason: "undecodable",
		},
		{
			// The ELEMENT-level mismatch: the WHOLE LINE fails, so no partial inventory
			// survives carrying only the elements that happened to decode.
			name:       "a commands element is a number",
			line:       initializeLineFixture(t, "success", map[string]any{"commands": []any{5}}),
			wantReason: "undecodable",
		},
		{
			// The shape a future claude most plausibly sends: systemInitLine's line
			// already spells this same inventory as bare strings under slash_commands.
			name:       "a commands element is a bare string",
			line:       initializeLineFixture(t, "success", map[string]any{"commands": []any{"deep-research"}}),
			wantReason: "undecodable",
		},
		{
			name:       "an entry's name is a number",
			line:       initializeLineFixture(t, "success", map[string]any{"commands": []any{commandEntryFixture(5)}}),
			wantReason: "undecodable",
		},
		{
			// The row above's pair, and the pair is the point: the all-or-nothing rule is
			// commandEntryLine's rather than `name`'s, so the SECOND declared key fails the
			// WHOLE line the same way rather than being tolerated as one bad field. That is
			// also what separates this from a null description, which the cap table's own row
			// pins as an ordinary counted entry.
			name: "an entry's description is a number",
			line: initializeLineFixture(t, "success", map[string]any{"commands": []any{
				commandEntryWithFixture(commandEntryFixture("deep-research"), "description", 5),
			}}),
			wantReason: "undecodable",
		},
		{
			// The THIRD declared key, and the trio is what makes the rule the STRUCT's
			// rather than any one field's: a non-string argumentHint takes the same rung as
			// a non-string name, with no branch of its own written for it. What separates
			// this from a null argumentHint, which the cap table pins as an ordinary counted
			// entry, is the same thing that separates a number description from a null one.
			name: "an entry's argumentHint is a number",
			line: initializeLineFixture(t, "success", map[string]any{"commands": []any{
				commandEntryWithFixture(commandEntryFixture("deep-research"), "argumentHint", 5),
			}}),
			wantReason: "undecodable",
		},
		{
			// The FOURTH and last declared key at the OUTER level. It completes the rule as
			// the STRUCT's rather than any one field's — no branch is written for it — and
			// it is the row that records this key's own transition: `aliases` was the
			// capture's undeclared key until #1825 and was silently ignored on every shape,
			// including this one.
			name: "an entry's aliases is a string",
			line: initializeLineFixture(t, "success", map[string]any{"commands": []any{
				commandEntryWithFixture(commandEntryFixture("clear"), "aliases", "reset"),
			}}),
			wantReason: "undecodable",
		},
		{
			// The INNER level, and the only row in this table that has one: `aliases` is the
			// only declared key with an inside, so it is the only one where a value of the
			// right OUTER shape can still fail. The whole line goes, so no partial alias list
			// survives carrying only the elements that happened to decode — the element-level
			// rule of the `commands` array itself, one level further in. What separates this
			// from a NULL aliases, which the cap table pins as an ordinary counted entry, is
			// what separates a number description from a null one.
			name: "an element of an entry's aliases is a number",
			line: initializeLineFixture(t, "success", map[string]any{"commands": []any{
				commandEntryWithFixture(commandEntryFixture("clear"), "aliases", []any{"reset", 5}),
			}}),
			wantReason: "undecodable",
		},
		{
			name:       "commands is null",
			line:       initializeLineFixture(t, "success", map[string]any{"commands": nil}),
			wantReason: "ack",
		},
		{
			name:       "commands is an empty array",
			line:       initializeLineFixture(t, "success", map[string]any{"commands": []any{}}),
			wantReason: "ack",
		},
		{
			// THE SUBTYPE HALF, commands dimension, and the SOLE detector for the count
			// being taken BELOW the subtype comparison rather than above it: every other
			// nak row carries an absent or empty commands and reads 0 either way. A
			// response that reported FAILURE has no payload the daemon reads, so the
			// count is 0 however many entries the failed reply carried.
			name: "THE SUBTYPE HALF: a NAK carrying a well-formed commands array",
			line: initializeLineFixture(t, "error", map[string]any{
				"commands": []any{commandEntryFixture("deep-research"), commandEntryFixture("design")}}),
			wantReason: "nak",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := &logRecorder{}
			var events []turnevent.Event
			p := NewParser(func(ev turnevent.Event) { events = append(events, ev) }, slog.New(rec))
			if _, err := p.Write([]byte(tt.line + "\n")); err != nil {
				t.Fatalf("Write err = %v, want nil", err)
			}

			if len(events) != 0 {
				t.Errorf("event count: got %d, want 0 — %#v", len(events), events)
			}
			consumes := rec.withMessage(controlResponseConsumeMsgFixture)
			if len(consumes) != 1 {
				t.Fatalf("records with message %q: got %d, want 1 — all records: %+v",
					controlResponseConsumeMsgFixture, len(consumes), rec.all())
			}
			wantAttrs := map[string]string{
				"type":             "control_response",
				"reason":           tt.wantReason,
				"models":           "0",
				"dropped":          "0",
				"levels_dropped":   "0",
				"commands":         "0",
				"commands_dropped": "0",
			}
			if !reflect.DeepEqual(consumes[0].attrs, wantAttrs) {
				t.Errorf("consume attrs: got %v, want exactly %v", consumes[0].attrs, wantAttrs)
			}
		})
	}
}

// TestParser_ModelListFieldsAreCapped is AC 2: each of the three strings is bounded
// by its OWN named cap, and the report names the fields that were cut on the entry
// they were cut on.
//
// It is also #1827's AC 2 for the level LIST, where the report has to AGGREGATE: an
// entry whose levels were cut names "effort_levels" once however many of them were
// over-long, and an entry that lost nothing does not name it at all. The
// three-over-long row is that pin and the only row with sole redness against the
// realistic mutant, a per-element append inside boundEach's loop rather than after
// it; the one-level row stays green under that mutant, which is why it cannot carry
// the AC.
//
// The fixtures are synthesized because the capture cannot supply them: its longest
// values are 25, 18 and 21 bytes and its longest level is 6, so nothing in it comes
// within an order of magnitude of a cap.
func TestParser_ModelListFieldsAreCapped(t *testing.T) {
	t.Parallel()

	// A rune whose UTF-8 encoding is two bytes, so a cut landing inside it deletes a
	// partial rune. Five of the capture's six descriptions carry non-ASCII, which is
	// what makes this a live path rather than a corner case.
	const twoByteRune = "é"
	overCap := func(limit int) string { return strings.Repeat("a", limit+1) }

	tests := []struct {
		name string
		// entries are claude's per-entry maps, in the order the line carries them.
		entries []map[string]any
		// wantCut is the expected TruncatedFields per entry, index for index.
		wantCut [][]string
		// wantLen, when non-zero for an index/field, pins the cut value's byte length.
		check func(t *testing.T, models []turnevent.ModelOption)
	}{
		{
			name:    "resolved_model alone",
			entries: []map[string]any{modelEntryFixture(overCap(modelResolvedCapFixture), "sonnet", "Sonnet")},
			wantCut: [][]string{{"resolved_model"}},
		},
		{
			name:    "value alone",
			entries: []map[string]any{modelEntryFixture("claude-sonnet-5", overCap(modelValueCapFixture), "Sonnet")},
			wantCut: [][]string{{"value"}},
		},
		{
			name:    "display_name alone",
			entries: []map[string]any{modelEntryFixture("claude-sonnet-5", "sonnet", overCap(modelDisplayNameCapFixture))},
			wantCut: [][]string{{"display_name"}},
		},
		{
			name: "two fields on one entry, reported in DECLARATION order",
			entries: []map[string]any{modelEntryFixture(
				overCap(modelResolvedCapFixture), "sonnet", overCap(modelDisplayNameCapFixture))},
			// resolved_model before display_name whatever order the JSON carries them in:
			// the order is fixed by the producer's sequential bound calls, not by the line.
			wantCut: [][]string{{"resolved_model", "display_name"}},
		},
		{
			name:    "nothing cut reports nil, not an empty slice",
			entries: []map[string]any{modelEntryFixture("claude-sonnet-5", "sonnet", "Sonnet")},
			wantCut: [][]string{nil},
		},
		{
			name: "exactly at the cap is NOT truncated",
			entries: []map[string]any{modelEntryFixture(
				strings.Repeat("a", modelResolvedCapFixture),
				strings.Repeat("b", modelValueCapFixture),
				strings.Repeat("c", modelDisplayNameCapFixture))},
			wantCut: [][]string{nil},
			check: func(t *testing.T, models []turnevent.ModelOption) {
				if got := len(models[0].ResolvedModel); got != modelResolvedCapFixture {
					t.Errorf("a value of exactly the cap came back %d bytes, want %d — truncateField's boundary is <=",
						got, modelResolvedCapFixture)
				}
			},
		},
		{
			name: "a cut landing mid-rune deletes the partial rune",
			entries: []map[string]any{modelEntryFixture(
				strings.Repeat("a", modelResolvedCapFixture-1)+twoByteRune, "sonnet", "Sonnet")},
			wantCut: [][]string{{"resolved_model"}},
			check: func(t *testing.T, models []turnevent.ModelOption) {
				got := models[0].ResolvedModel
				// 255, not 256: the cut lands inside the two-byte rune and the partial rune
				// is DELETED rather than replaced, so a cut value can come out 1-3 bytes
				// under the limit — and the report still names the field.
				if len(got) != modelResolvedCapFixture-1 {
					t.Errorf("mid-rune cut came back %d bytes, want %d", len(got), modelResolvedCapFixture-1)
				}
				if !utf8.ValidString(got) {
					t.Errorf("mid-rune cut left invalid UTF-8: %q", got)
				}
			},
		},
		{
			name: "one over-long level names effort_levels",
			entries: []map[string]any{modelEntryWithFixture(
				modelEntryFixture("claude-sonnet-5", "sonnet", "Sonnet"),
				"supportedEffortLevels", []any{overCap(modelEffortLevelCapFixture)})},
			wantCut: [][]string{{"effort_levels"}},
			check: func(t *testing.T, models []turnevent.ModelOption) {
				// Length first so a decode that dropped the list FAILS here rather than
				// panicking on the index and taking the parallel siblings' reports with it.
				if len(models[0].EffortLevels) != 1 {
					t.Fatalf("EffortLevels came back %q, want the one level the entry carried",
						models[0].EffortLevels)
				}
				// The cap's VALUE, pinned on the ELEMENT, which is the only place it applies:
				// halve maxModelEffortLevel and this is what goes red.
				if got := len(models[0].EffortLevels[0]); got != modelEffortLevelCapFixture {
					t.Errorf("a cut level came back %d bytes, want %d", got, modelEffortLevelCapFixture)
				}
			},
		},
		{
			// THE aggregation pin. ONE name, not three: the report names FIELDS and a
			// list is one field however many of its elements were cut.
			name: "THREE over-long levels on one entry name effort_levels ONCE",
			entries: []map[string]any{modelEntryWithFixture(
				modelEntryFixture("claude-sonnet-5", "sonnet", "Sonnet"),
				"supportedEffortLevels", []any{
					overCap(modelEffortLevelCapFixture),
					overCap(modelEffortLevelCapFixture),
					overCap(modelEffortLevelCapFixture),
				})},
			wantCut: [][]string{{"effort_levels"}},
		},
		{
			name: "one over-long level among two that fit keeps all three, in order",
			entries: []map[string]any{modelEntryWithFixture(
				modelEntryFixture("claude-sonnet-5", "sonnet", "Sonnet"),
				"supportedEffortLevels", []any{"low", overCap(modelEffortLevelCapFixture), "max"})},
			wantCut: [][]string{{"effort_levels"}},
			check: func(t *testing.T, models []turnevent.ModelOption) {
				got := models[0].EffortLevels
				// The cap bounds the ELEMENT: cutting one level neither drops it nor
				// disturbs its neighbours, so the cardinality and claude's order survive.
				if len(got) != 3 {
					t.Fatalf("EffortLevels came back %d long, want 3 — an element cap must not change the count",
						len(got))
				}
				if got[0] != "low" || got[2] != "max" {
					t.Errorf("the levels that fit came back %q and %q, want \"low\" and \"max\" verbatim",
						got[0], got[2])
				}
			},
		},
		{
			// The only row in this table that reddens under an append made unconditional:
			// five levels, none near the cap, so the name must not appear at all. Rows
			// whose list is empty cannot carry that — boundEach returns before the append
			// on an empty input, so they stay green under the same mutant.
			name: "levels that all fit report nothing",
			entries: []map[string]any{modelEntryWithFixture(
				modelEntryFixture("claude-sonnet-5", "sonnet", "Sonnet"),
				"supportedEffortLevels", []any{"low", "medium", "high", "xhigh", "max"})},
			wantCut: [][]string{nil},
		},
		{
			// effort_levels LAST, whatever order the JSON carries the keys in: the order
			// is fixed by the producer's sequential calls, and this row is the sole red
			// against a boundEach call placed before the three bound calls.
			name: "a string cut AND a level cut, reported in DECLARATION order",
			entries: []map[string]any{modelEntryWithFixture(
				modelEntryFixture(overCap(modelResolvedCapFixture), "sonnet", "Sonnet"),
				"supportedEffortLevels", []any{overCap(modelEffortLevelCapFixture)})},
			wantCut: [][]string{{"resolved_model", "effort_levels"}},
		},
		{
			name: "a cut on one entry does not appear on the entries AFTER it",
			entries: []map[string]any{
				modelEntryFixture(overCap(modelResolvedCapFixture), "sonnet", "Sonnet"),
				modelEntryFixture("claude-opus-5", "opus", "Opus"),
				modelEntryFixture("claude-haiku-4-5", overCap(modelValueCapFixture), "Haiku"),
			},
			// Per-entry accumulation, not a shared slice — and the order is what makes
			// this row load-bearing. A `cut` hoisted out of the loop leaks FORWARD, never
			// backward: the entry that was cut is built before the next one appends, so a
			// clean entry placed AFTER a cut one is the only arrangement that catches it.
			// Entry 3 then also names entry 1's field, which is the second half.
			wantCut: [][]string{{"resolved_model"}, nil, {"value"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			events := collectEvents(modelListLineFixture(t, "success", tt.entries))
			if len(events) != 1 {
				t.Fatalf("event count: got %d, want 1 turnevent.ModelList — %#v", len(events), events)
			}
			list, ok := events[0].(turnevent.ModelList)
			if !ok {
				t.Fatalf("event[0] = %T, want turnevent.ModelList", events[0])
			}
			if len(list.Models) != len(tt.entries) {
				t.Fatalf("ModelList carries %d entries, want %d", len(list.Models), len(tt.entries))
			}
			for i, got := range list.Models {
				// DeepEqual rather than a length or a contains check: nil and []string{}
				// disagree here and only one of them is the contract.
				if !reflect.DeepEqual(got.TruncatedFields, tt.wantCut[i]) {
					t.Errorf("entry %d TruncatedFields: got %#v, want %#v", i, got.TruncatedFields, tt.wantCut[i])
				}
			}
			if tt.check != nil {
				tt.check(t, list.Models)
			}
		})
	}
}

// TestParser_ModelListReducesToOneRowPerFamilyBeforeTheCap replays the list
// pyrybox's daemon held on 2026-10-05 (Claude Code 2.1.289): twelve entries, of
// which model_list.json saved ten and the cap cut two. The pinned rows are reduced
// away BEFORE the cap counts, so the five families fit, nothing is reported
// dropped, and every surviving row is claude's own, in claude's order.
func TestParser_ModelListReducesToOneRowPerFamilyBeforeTheCap(t *testing.T) {
	t.Parallel()
	entries := []map[string]any{
		modelEntryFixture("claude-opus-5-5", "default", "Default (recommended)"),
		modelEntryFixture("claude-opus-5-5", "opus", "Opus 5.5"),
		modelEntryFixture("claude-fable-5-1", "fable", "Fable 5.1"),
		modelEntryFixture("claude-sonnet-5-5", "sonnet", "Sonnet 5.5"),
		modelEntryFixture("claude-haiku-4-5-20251001", "haiku", "Haiku 4.5"),
		modelEntryFixture("claude-sonnet-5", "claude-sonnet-5", "Sonnet 5"),
		modelEntryFixture("claude-opus-5", "claude-opus-5", "Opus 5"),
		modelEntryFixture("claude-fable-5", "claude-fable-5", "Fable 5"),
		modelEntryFixture("claude-opus-4-8", "claude-opus-4-8", "Opus 4.8"),
		modelEntryFixture("claude-opus-4-7", "claude-opus-4-7", "Opus 4.7"),
		// The two rows the cap cut were never saved, so any two pinned rows stand in.
		modelEntryFixture("claude-sonnet-4-6", "claude-sonnet-4-6", "Sonnet 4.6"),
		modelEntryFixture("claude-haiku-4-5", "claude-haiku-4-5", "Haiku 4.5"),
	}
	if len(entries) <= modelListEntriesCapFixture {
		t.Fatalf("the fixture carries %d entries; it must exceed the cap of %d to prove the reduction runs first",
			len(entries), modelListEntriesCapFixture)
	}
	events := collectEvents(modelListLineFixture(t, "success", entries))
	if len(events) != 1 {
		t.Fatalf("event count: got %d, want 1 turnevent.ModelList: %#v", len(events), events)
	}
	list, ok := events[0].(turnevent.ModelList)
	if !ok {
		t.Fatalf("event[0] = %T, want turnevent.ModelList", events[0])
	}
	var values, displays []string
	for _, m := range list.Models {
		values = append(values, m.Value)
		displays = append(displays, m.DisplayName)
	}
	if want := []string{"default", "opus", "fable", "sonnet", "haiku"}; !slices.Equal(values, want) {
		t.Errorf("values: got %q, want %q", values, want)
	}
	if want := []string{"Default (recommended)", "Opus 5.5", "Fable 5.1", "Sonnet 5.5", "Haiku 4.5"}; !slices.Equal(displays, want) {
		t.Errorf("display names: got %q, want %q", displays, want)
	}
	if list.DroppedModels != 0 {
		t.Errorf("DroppedModels: got %d, want 0; the cap must count families, not pinned versions", list.DroppedModels)
	}
}

// TestParser_ModelListEntryCountIsBounded is #1812's central pin: the number of
// entries is bounded AT CONSTRUCTION and the overflow is reported as a COUNT, so
// the list's true size stays recoverable as len(Models) + DroppedModels.
//
// The capture proves neither half and cannot: claude sends six entries, under the
// cap, which is exactly the zero-drop path the capture test pins. So these lines
// are SYNTHESIZED, which invents no field structure — the keys are the capture's,
// only the count varies.
//
// Empty and absent arrays are deliberately NOT rows here. They return at the ack
// rung before the cap ever runs, and TestParser_InitializeControlResponseRejectBranches
// already covers them; a row here would assert the cap against an input it never sees.
func TestParser_ModelListEntryCountIsBounded(t *testing.T) {
	t.Parallel()

	t.Run("the entry count is bounded and the overflow is reported", func(t *testing.T) {
		t.Parallel()
		tests := []struct {
			name        string
			entries     []map[string]any
			wantLen     int
			wantDropped int
		}{
			{
				name:    "one over the cap drops one",
				entries: modelEntriesFixture(modelListEntriesCapFixture + 1),
				wantLen: modelListEntriesCapFixture, wantDropped: 1,
			},
			{
				// The <= boundary, matching truncateField's convention and the roster's.
				// What it discriminates is a cap that fires one entry EARLY: > and >= are
				// indistinguishable here by construction, since at len == cap the block
				// computes a 0 drop and slices to identity either way.
				name:    "exactly at the cap carries every entry",
				entries: modelEntriesFixture(modelListEntriesCapFixture),
				wantLen: modelListEntriesCapFixture, wantDropped: 0,
			},
			{
				// The row that makes the report a COUNT rather than a flag: a flag cannot
				// tell 1 lost from 90, and the list's true size is only recoverable as
				// len(Models) + DroppedModels.
				name:    "a large array reports how many were lost",
				entries: modelEntriesFixture(100),
				wantLen: modelListEntriesCapFixture, wantDropped: 100 - modelListEntriesCapFixture,
			},
			{
				// Under the cap, so the bound is not proven only at its own boundary.
				name:    "one under the cap is untouched",
				entries: modelEntriesFixture(modelListEntriesCapFixture - 1),
				wantLen: modelListEntriesCapFixture - 1, wantDropped: 0,
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				events := collectEvents(modelListLineFixture(t, "success", tt.entries))
				if len(events) != 1 {
					t.Fatalf("event count: got %d, want 1 turnevent.ModelList — %#v", len(events), events)
				}
				list, ok := events[0].(turnevent.ModelList)
				if !ok {
					t.Fatalf("event[0] = %T, want turnevent.ModelList", events[0])
				}

				if len(list.Models) != tt.wantLen {
					t.Fatalf("len(Models): got %d, want %d", len(list.Models), tt.wantLen)
				}
				if list.DroppedModels != tt.wantDropped {
					t.Errorf("DroppedModels: got %d, want %d", list.DroppedModels, tt.wantDropped)
				}
				// Truncation is from the TAIL, preserving claude's order: no ranking is
				// invented, because claude's ordering semantics are unobserved. Pinned per
				// entry rather than assumed from the count, which is what a head-truncating
				// entries[len(entries)-cap:] would otherwise pass.
				for i, got := range list.Models {
					want := tt.entries[i]["resolvedModel"]
					if got.ResolvedModel != want {
						t.Errorf("Models[%d].ResolvedModel: got %q, want %q — the survivors are claude's first %d, in order",
							i, got.ResolvedModel, want, tt.wantLen)
					}
				}
			})
		}
	})

	t.Run("the record names both numbers", func(t *testing.T) {
		t.Parallel()
		// The ONLY place `dropped` is non-zero. Without it the attribute is decorative:
		// a producer hard-coding 0 would stay green in every other record assertion on
		// this path. The entry count does reach a client — turnbridge.MapEvent's arm
		// carries it (#1848) and it lands on the wire as dropped_models (#1849) — but
		// the record is the OPERATOR-facing signal that survives a cap firing with no
		// interactive conn present, which is logControlResponse's own argument for it.
		rec := &logRecorder{}
		p := NewParser(func(turnevent.Event) {}, slog.New(rec))
		line := modelListLineFixture(t, "success", modelEntriesFixture(100))
		if _, err := p.Write([]byte(line + "\n")); err != nil {
			t.Fatalf("Write err = %v, want nil", err)
		}

		consumes := rec.withMessage(controlResponseConsumeMsgFixture)
		if len(consumes) != 1 {
			t.Fatalf("records with message %q: got %d, want 1 — all records: %+v",
				controlResponseConsumeMsgFixture, len(consumes), rec.all())
		}
		// levels_dropped is 0 because no entry here carries a level list at all, so the
		// level bound never runs — its non-zero case is
		// TestParser_ModelListEffortLevelCountIsBounded's.
		wantAttrs := map[string]string{
			"type":             "control_response",
			"reason":           "model_list",
			"models":           strconv.Itoa(modelListEntriesCapFixture),
			"dropped":          strconv.Itoa(100 - modelListEntriesCapFixture),
			"levels_dropped":   "0",
			"commands":         "0",
			"commands_dropped": "0",
		}
		if !reflect.DeepEqual(consumes[0].attrs, wantAttrs) {
			t.Errorf("consume attrs: got %v, want exactly %v", consumes[0].attrs, wantAttrs)
		}
	})
}

// modelLevelsFixture builds n effort levels, each identifiable by its index so
// tail-truncation is PINNED rather than assumed from a length. modelEntriesFixture's
// shape, one dimension down.
//
// Every element is well under maxModelEffortLevel, which is what lets a row built from
// it exercise the COUNT bound ALONE: a row wanting the element cap too says so by
// replacing an element, and then the two mechanisms are visibly two.
func modelLevelsFixture(n int) []string {
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, fmt.Sprintf("level-%03d", i))
	}
	return out
}

// TestParser_ModelListEffortLevelCountIsBounded is the central pin for the per-entry
// LEVEL count: how many levels one entry retains is bounded AT CONSTRUCTION and the
// overflow is REPORTED, so a client is never handed three of claude's ten levels as a
// complete menu.
//
// The capture proves neither half and cannot: claude sends five levels, three under
// the cap, which is the zero-drop path TestParser_ModelListEffortLevelsReadClaudesKey
// and the captured-decode test already pin. So these lines are SYNTHESIZED, which
// invents no field structure — the keys are the capture's, only the level count varies.
//
// Empty, null and absent level lists are deliberately NOT rows here. They return at
// boundEach's zero-length arm BEFORE the count bound runs, and
// TestParser_ModelListEffortLevelsReadClaudesKey already covers all three; a row here
// would assert the bound against an input it never sees. Same exclusion
// TestParser_ModelListEntryCountIsBounded states for the ack rung, one dimension down.
func TestParser_ModelListEffortLevelCountIsBounded(t *testing.T) {
	t.Parallel()

	base := modelEntryFixture("claude-sonnet-5", "sonnet", "Sonnet")
	levelEntry := func(levels []string) map[string]any {
		return modelEntryWithFixture(base, "supportedEffortLevels", levels)
	}
	overCap := func(limit int) string { return strings.Repeat("a", limit+1) }

	t.Run("the level count is bounded and the overflow is reported", func(t *testing.T) {
		t.Parallel()
		tests := []struct {
			name string
			// entries are claude's per-entry maps, in the order the line carries them.
			entries []map[string]any
			// wantLevels is the expected EffortLevels per entry, index for index. By
			// VALUE rather than by length, which is what pins tail truncation.
			wantLevels [][]string
			// wantCut is the expected TruncatedFields per entry, index for index.
			wantCut [][]string
		}{
			{
				// The row proving the COUNT bound reports AT ALL: every surviving element
				// is far under maxModelEffortLevel, so the name can only have come from
				// the drop. Without it the report is exercised by the element cap alone.
				name:       "one over the cap drops one and names effort_levels",
				entries:    []map[string]any{levelEntry(modelLevelsFixture(modelEffortLevelCountCapFixture + 1))},
				wantLevels: [][]string{modelLevelsFixture(modelEffortLevelCountCapFixture)},
				wantCut:    [][]string{{"effort_levels"}},
			},
			{
				// The boundary row. It discriminates a bound firing one level EARLY, which
				// is why the want is the LITERAL fixture and never the production constant:
				// lower maxModelEffortLevelCount and this row goes red alongside the
				// over-the-cap rows rather than following the edit green.
				//
				// AND, unlike TestParser_ModelListEntryCountIsBounded's row of this shape,
				// it discriminates > from >= — measured, sole red among all six. The entry
				// cap's block computes only a count and a slice, so at len == cap both arms
				// are identity and the operator is an equivalent mutant. This block also
				// sets the REPORT flag, so >= would name "effort_levels" on a list of
				// exactly the cap where nothing was dropped and nothing was cut.
				// The inherited "expect the same" reading is wrong here, and the difference
				// is the flag rather than the arithmetic.
				name:       "exactly at the cap keeps every level and names nothing",
				entries:    []map[string]any{levelEntry(modelLevelsFixture(modelEffortLevelCountCapFixture))},
				wantLevels: [][]string{modelLevelsFixture(modelEffortLevelCountCapFixture)},
				wantCut:    [][]string{nil},
			},
			{
				// Under the cap, so the bound is not proven only at its own boundary.
				name:       "one under the cap is untouched",
				entries:    []map[string]any{levelEntry(modelLevelsFixture(modelEffortLevelCountCapFixture - 1))},
				wantLevels: [][]string{modelLevelsFixture(modelEffortLevelCountCapFixture - 1)},
				wantCut:    [][]string{nil},
			},
			{
				// The row a head-truncating values[len(values)-cap:] fails: the survivors
				// are claude's FIRST levels, pinned one by one in claude's own order.
				name:       "a large list keeps claude's FIRST levels, in order",
				entries:    []map[string]any{levelEntry(modelLevelsFixture(100))},
				wantLevels: [][]string{modelLevelsFixture(100)[:modelEffortLevelCountCapFixture]},
				wantCut:    [][]string{{"effort_levels"}},
			},
			{
				// The aggregation pin extended to TWO mechanisms: a dropped tail AND a cut
				// survivor still name the field exactly once, because the report names
				// FIELDS and a list is one field.
				name: "over the cap AND a surviving element over-long names effort_levels ONCE",
				entries: []map[string]any{levelEntry(append(
					[]string{overCap(modelEffortLevelCapFixture)},
					modelLevelsFixture(modelEffortLevelCountCapFixture)...))},
				wantLevels: [][]string{append(
					[]string{strings.Repeat("a", modelEffortLevelCapFixture)},
					modelLevelsFixture(modelEffortLevelCountCapFixture-1)...)},
				wantCut: [][]string{{"effort_levels"}},
			},
			{
				// Per-entry accumulation for the DROP path, the shape
				// TestParser_ModelListFieldsAreCapped pins for the CUT path. A counter or a
				// `cut` hoisted out of the per-entry scope leaks FORWARD, never backward,
				// so a clean entry placed AFTER an over-long one is the only arrangement
				// that catches it — and entry 3 carries a level list of its own so the leak
				// would have somewhere to land rather than being masked by an absent key.
				name: "a drop on one entry does not appear on the entries AFTER it",
				entries: []map[string]any{
					levelEntry(modelLevelsFixture(modelEffortLevelCountCapFixture + 1)),
					modelEntryFixture("claude-opus-5", "opus", "Opus"),
					levelEntry(modelLevelsFixture(modelEffortLevelCountCapFixture - 1)),
				},
				wantLevels: [][]string{
					modelLevelsFixture(modelEffortLevelCountCapFixture),
					nil,
					modelLevelsFixture(modelEffortLevelCountCapFixture - 1),
				},
				wantCut: [][]string{{"effort_levels"}, nil, nil},
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				events := collectEvents(modelListLineFixture(t, "success", tt.entries))
				if len(events) != 1 {
					t.Fatalf("event count: got %d, want 1 turnevent.ModelList — %#v", len(events), events)
				}
				list, ok := events[0].(turnevent.ModelList)
				if !ok {
					t.Fatalf("event[0] = %T, want turnevent.ModelList", events[0])
				}
				if len(list.Models) != len(tt.entries) {
					t.Fatalf("ModelList carries %d entries, want %d", len(list.Models), len(tt.entries))
				}
				// The level bound is per ENTRY, so it can never remove an entry: the
				// list-level count is the entry cap's business and is untouched here.
				if list.DroppedModels != 0 {
					t.Errorf("DroppedModels: got %d, want 0 — a LEVEL bound must not drop entries",
						list.DroppedModels)
				}
				for i, got := range list.Models {
					// DeepEqual rather than slices.Equal: slices.Equal(nil, []string{})
					// reports true, and the entry carrying no key at all must come back nil.
					if !reflect.DeepEqual(got.EffortLevels, tt.wantLevels[i]) {
						t.Errorf("entry %d EffortLevels: got %#v, want %#v — the survivors are claude's "+
							"first levels, in order", i, got.EffortLevels, tt.wantLevels[i])
					}
					if !reflect.DeepEqual(got.TruncatedFields, tt.wantCut[i]) {
						t.Errorf("entry %d TruncatedFields: got %#v, want %#v",
							i, got.TruncatedFields, tt.wantCut[i])
					}
				}
			})
		}
	})

	t.Run("the record names how many levels were dropped", func(t *testing.T) {
		t.Parallel()
		// The ONLY place `levels_dropped` is non-zero. Without it the attribute is
		// decorative: a producer hard-coding 0 would stay green in every other record
		// assertion on this path. What reaches the wire is only the per-entry
		// "effort_levels" NAME (#1848), which says the same thing whether one level was
		// cut or ninety were dropped, so this record stays the only observable of HOW
		// MANY the level bound cut — logControlResponse's own argument for it.
		//
		// THREE entries — over by one, clean, over by three — which is the arrangement
		// that pins the attribute as a TOTAL over the RETAINED entries and pins the
		// counter's SCOPE at the same time. A producer reporting only the last entry's
		// drop reads 3, one reporting the largest reads 3, one reporting how many
		// entries dropped anything reads 2, and one whose per-entry counter is hoisted
		// out of the loop reads 5 — the clean entry in the middle carrying the first
		// entry's drop forward. Only the correct total reads 4.
		//
		// The exact map equality is also what sweeps the new drop site for content: any
		// attribute added beside the counter — the classic `"level", level` beside it —
		// turns this red, which is TestParser_ModelListIsLoggedContentFree's guarantee
		// extended to the one rung that path cannot reach with a level list over the cap.
		rec := &logRecorder{}
		p := NewParser(func(turnevent.Event) {}, slog.New(rec))
		line := modelListLineFixture(t, "success", []map[string]any{
			levelEntry(modelLevelsFixture(modelEffortLevelCountCapFixture + 1)),
			levelEntry(modelLevelsFixture(modelEffortLevelCountCapFixture - 1)),
			levelEntry(modelLevelsFixture(modelEffortLevelCountCapFixture + 3)),
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
			"models":           "3",
			"dropped":          "0",
			"levels_dropped":   "4",
			"commands":         "0",
			"commands_dropped": "0",
		}
		if !reflect.DeepEqual(consumes[0].attrs, wantAttrs) {
			t.Errorf("consume attrs: got %v, want exactly %v", consumes[0].attrs, wantAttrs)
		}
	})
}

// TestParser_ModelListIsLoggedContentFree is AC 5: no record on this path carries
// decoded content, on ANY rung. The constraint is #833's posture,
// restated across internal/relay's v2session_settings.go and internal/sessions'
// pool.go as "model / effort / YOLO values are NEVER logged at any level".
//
// The sentinels are what stop the sweep passing vacuously: each is planted in a
// synthetic line and then searched for across EVERY record the parser produced, not
// only the expected one — the realistic way this rule breaks is someone appending
// "value", entry.Value to a drop site. The captured line is fed in beside them so
// the MODEL-LIST rung is swept with claude's real strings too — named rather than
// called "the emit rung", which since #1891 is two of them.
//
// #1878 extended it to COMMAND names on every rung. The captured line already supplied
// one, but every captured name is 24 bytes or shorter, so no line here drove a command
// name through truncateField while the sweep was watching: a Debug at the truncation
// site logging the full-length WORKSPACE-AUTHORED name reddened only the record COUNT,
// and a version appending to the existing record only the DeepEqual. Four lines and
// five command sentinels close that, each named for the rung its line lands on so a
// failure names the rung. The narrowed ack rung (#1890) carries none, and cannot: a
// line that decodes a command name is by definition not on it.
//
// The undecodable line carrying a commands array is the highest-value one:
// encoding/json QUOTES the offending input bytes into its error text, so an
// `"err", err` added to that arm would route workspace-authored command names into the
// daemon log through a channel no per-attribute check can see. emitModelList's
// undecodable arm argues that rule in prose; this is where it is a red test.
func TestParser_ModelListIsLoggedContentFree(t *testing.T) {
	t.Parallel()

	const (
		resolvedSentinel = "resolved-sentinel-181101"
		valueSentinel    = "value-sentinel-181102"
		displaySentinel  = "display-sentinel-181103"
		nakSentinel      = "nak-error-sentinel-181104"
		// One command sentinel per rung that can carry one, so a leak names the rung it
		// came from. The last was the ack rung's until #1890 split that rung; the line
		// carrying it lands on commands_only now, and both the name and the value follow
		// it — a sentinel whose text names the wrong rung would misdirect the one reader
		// it exists for.
		commandFitsSentinel         = "command-fits-sentinel-187801"
		commandCutPrefix            = "command-cut-sentinel-187802"
		nakCommandSentinel          = "nak-command-sentinel-187803"
		undecodableCommandSentinel  = "undecodable-command-sentinel-187804"
		commandsOnlyCommandSentinel = "commands-only-command-sentinel-189001"
	)
	// The over-cap name carries its distinctive part at the FRONT and is padded past
	// the cap with `a`s. The order is load-bearing: a sentinel sitting at the END would
	// be exactly what the cut removes, so the sweep would go silently vacuous on the one
	// path it was added to cover. It is commandCutPrefix — not this padded value — that
	// goes into `leaks` below, so a leak of the CUT value matches too.
	commandCutSentinel := commandCutPrefix +
		strings.Repeat("a", slashCommandNameCapFixture+1-len(commandCutPrefix))
	captured := capturedModelEntries(t, initCaptureArmBase)
	capturedValue := capturedModelString(t, captured[0], "value")
	capturedResolved := capturedModelString(t, captured[0], "resolvedModel")
	capturedDisplay := capturedModelString(t, captured[0], "displayName")
	// #1853's string family, swept for the same reason the three above are: the
	// commands array is DECODED now, so a command name is a claude-derived string that
	// could reach a record. The count is derived from the capture rather than
	// transcribed.
	capturedCommands := capturedCommandEntries(t, initCaptureArmBase)
	capturedCommand := capturedCommandString(t, capturedCommands[0], "name")

	rec := &logRecorder{}
	var events []turnevent.Event
	p := NewParser(func(ev turnevent.Event) { events = append(events, ev) }, slog.New(rec))
	lines := []string{
		// The model-list rung, three times: the sentinel-carrying synthetic line, then one
		// carrying COMMAND sentinels — one that fits and one over the cap — then the real
		// captured one with all fourteen payload keys intact.
		modelListLineFixture(t, "success", []map[string]any{
			modelEntryFixture(resolvedSentinel, valueSentinel, displaySentinel)}),
		// Plain model values here rather than the three model sentinels, so a failure
		// unambiguously names WHICH line leaked.
		initializeLineFixture(t, "success", map[string]any{
			"models": []map[string]any{modelEntryFixture("claude-sonnet-5", "sonnet", "Sonnet")},
			"commands": []any{
				commandEntryFixture(commandFitsSentinel),
				commandEntryFixture(commandCutSentinel),
			}}),
		capturedInitializeLine(t, initCaptureArmBase),
		// The rungs BELOW the model-list one, each paired: once as it stood, and once
		// carrying a `commands` array whose name is that rung's sentinel. The second of
		// each pair is what makes the sweep cover a rung that DECODES command names. The
		// nak and undecodable pairs stay on ONE rung each and emit nothing — the count is
		// taken below the success gate, so a decoded array does not move them. The last
		// pair does not stay on one rung: since #1890 its first line is the narrowed ack
		// and its second is the commands_only rung, so that pair covers two rungs and the
		// sweep gained a rung without gaining a line. Since #1891 those two rungs also
		// disagree about emitting — the ack line emits nothing, the commands_only line
		// emits its inventory — which is why "non-emitting" no longer names this block.
		// The sweep is indifferent to that: it searches RECORDS, and an emit produces
		// none.
		`{"type":"control_response","response":{"subtype":"error","error":"` + nakSentinel + `"}}`,
		initializeLineFixture(t, "error", map[string]any{
			"commands": []any{commandEntryFixture(nakCommandSentinel)}}),
		modelListLineFixture(t, "success", 5),
		initializeLineFixture(t, "success", map[string]any{
			"models":   5,
			"commands": []any{commandEntryFixture(undecodableCommandSentinel)}}),
		modelListLineFixture(t, "success", []map[string]any{}),
		initializeLineFixture(t, "success", map[string]any{
			"commands": []any{commandEntryFixture(commandsOnlyCommandSentinel)}}),
	}
	for _, line := range lines {
		if _, err := p.Write([]byte(line + "\n")); err != nil {
			t.Fatalf("Write err = %v, want nil", err)
		}
	}

	// SIX: the three model-carrying lines each emit a ModelList, and two of them carry
	// a non-empty commands array, so each adds a SlashCommandList behind its ModelList
	// (#1877). The sixth is the LAST line's, whose commands array rides the
	// commands_only rung — which emits one SlashCommandList of its own since #1891, and
	// no ModelList. Only the nak and undecodable command-carrying lines add nothing,
	// which is those two rungs' whole point and this count's own half of the suppression
	// pin. What the emits do NOT change is the sweep below.
	if len(events) != 6 {
		t.Fatalf("event count: got %d, want 6 (the three model-carrying lines, a command "+
			"inventory behind two of them, and the commands-only line's own) — %#v", len(events), events)
	}
	// One record per line, and every one of them this arm's: the count is the half
	// the per-record assertions cannot see.
	all := rec.all()
	if len(all) != len(lines) {
		t.Fatalf("records: got %d, want %d (one per control_response): %+v", len(all), len(lines), all)
	}

	// The ninth is commands_only rather than ack (#1890): its line carries a non-empty
	// `commands` and no models, which is now its own rung. The eighth is the narrowed
	// ack — an empty models array and no commands at all.
	wantReasons := []string{"model_list", "model_list", "model_list", "nak", "nak",
		"undecodable", "undecodable", "ack", "commands_only"}
	// The captured line logs 5, not its 6 entries: the record counts the rows
	// emitted, and the pinned claude-haiku-4-5 is reduced away beside haiku.
	wantCounts := []string{"1", "1", "5", "0", "0", "0", "0", "0", "0"}
	// The captured line's count comes from the capture's own bytes, so a re-capture
	// moves the expectation with the fixture rather than reddening a transcribed number.
	//
	// The two zeros worth re-deriving rather than guessing are the nak and undecodable
	// lines that DO carry a non-empty commands array: the count is taken BELOW the
	// success gate, so both read 0 even though the array decoded (undecodable) or would
	// have (nak). That is logControlResponse's documented placement and these two rows
	// are its proof on this path.
	wantCommandCounts := []string{"0", "2", strconv.Itoa(len(capturedCommands)),
		"0", "0", "0", "0", "0", "1"}
	for i, r := range all {
		if r.msg != controlResponseConsumeMsgFixture {
			t.Errorf("record %d message: got %q, want %q", i, r.msg, controlResponseConsumeMsgFixture)
			continue
		}
		// Every line here is under all THREE cardinality caps — the capture's entries
		// carry five levels each, three under maxModelEffortLevelCount, and its
		// fifty-one commands are well under maxSlashCommandListEntries — so `dropped`,
		// `levels_dropped` and `commands_dropped` are 0 on every row; their non-zero
		// cases are TestParser_ModelListEntryCountIsBounded's,
		// TestParser_ModelListEffortLevelCountIsBounded's and
		// TestParser_SlashCommandEntryCountIsBounded's. What this sweep adds is that all
		// five count attributes are swept for leaks like the other two.
		wantAttrs := map[string]string{
			"type":             "control_response",
			"reason":           wantReasons[i],
			"models":           wantCounts[i],
			"dropped":          "0",
			"levels_dropped":   "0",
			"commands":         wantCommandCounts[i],
			"commands_dropped": "0",
		}
		if !reflect.DeepEqual(r.attrs, wantAttrs) {
			t.Errorf("record %d attrs: got %v, want exactly %v", i, r.attrs, wantAttrs)
		}
	}

	// commandCutPrefix rather than the padded commandCutSentinel: the value a leak would
	// carry is the CUT one, 256 bytes of it, and only the prefix is in both.
	leaks := []string{resolvedSentinel, valueSentinel, displaySentinel, nakSentinel,
		capturedValue, capturedResolved, capturedDisplay, capturedCommand,
		commandFitsSentinel, commandCutPrefix, nakCommandSentinel,
		undecodableCommandSentinel, commandsOnlyCommandSentinel}
	for _, r := range all {
		for _, leak := range leaks {
			if strings.Contains(r.msg, leak) {
				t.Errorf("record message carries claude-derived content (%q): %q", leak, r.msg)
			}
			for k, v := range r.attrs {
				if strings.Contains(v, leak) {
					t.Errorf("record %q attr %q carries claude-derived content (%q); this path logs a "+
						"daemon-authored keyword and a count only", r.msg, k, leak)
				}
			}
		}
	}
}

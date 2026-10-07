package protocol

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// TestModelListPayload_NilModelsNormalises covers the case no fixture could —
// and here that is the ONLY path there is. Unmarshalling "models":[] always
// yields a non-nil empty slice, so the nil branch is reachable only by
// constructing the value directly, and this slice (#1704) ships no fixture at all
// (internal/protocol/testdata/ is #1705's). The producer (#1848) builds its outer
// slice by appending into a nil one, so an empty or absent claude models array
// hands this type a nil slice and, without normalisation, would ship
// "models":null to a phone.
//
// Both the value and pointer forms are checked because a pointer-receiver
// marshaller would silently miss the value path roundTripEnvelope takes. This is
// TestBackgroundTaskRosterPayload_NilTasksNormalises's shape.
func TestModelListPayload_NilModelsNormalises(t *testing.T) {
	p := ModelListPayload{ConversationID: "c1"}
	if p.Models != nil {
		t.Fatalf("precondition: Models must be nil, got %v", p.Models)
	}

	for _, tc := range []struct {
		name string
		in   any
	}{
		{"value", p},
		{"pointer", &p},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := json.Marshal(tc.in)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if bytes.Contains(out, []byte(`"models":null`)) {
				t.Errorf("nil Models marshalled to null: %s", out)
			}
			if !bytes.Contains(out, []byte(`"models":[]`)) {
				t.Errorf("nil Models did not normalise to []: %s", out)
			}
		})
	}

	// The normalisation must not mutate the receiver's copy back into the caller.
	if p.Models != nil {
		t.Errorf("MarshalJSON mutated the receiver: Models is now %v", p.Models)
	}
}

// TestModelOption_NilSliceEncodings pins the asymmetry between the entry's two
// list fields, which no fixture could pin either: decoding [] always yields a
// non-nil slice, and this slice ships no fixture at all. EffortLevels normalises
// nil to [], TruncatedFields does not and stays null.
//
// truncated_fields is pinned at all precisely BECAUSE it ships no fixture — the
// route BackgroundTaskRosterPayload's second roster entry takes is not open here.
// An unpinned null is one that a later omitempty, or a third normaliser copied
// from the field above it, silently turns into something else.
//
// The nested-in-payload subtest proves the entry marshaller fires through the
// payload's, and its trailing assertion is the only thing that would catch a
// payload marshaller normalising entries IN PLACE — that would reach through
// p.Models[i] into the caller's backing array.
func TestModelOption_NilSliceEncodings(t *testing.T) {
	o := ModelOption{Value: "sonnet"}
	if o.EffortLevels != nil || o.TruncatedFields != nil {
		t.Fatalf("precondition: both slices must be nil, got %v / %v", o.EffortLevels, o.TruncatedFields)
	}

	assertEncodings := func(t *testing.T, out []byte) {
		t.Helper()
		if bytes.Contains(out, []byte(`"effort_levels":null`)) {
			t.Errorf("nil EffortLevels marshalled to null: %s", out)
		}
		if !bytes.Contains(out, []byte(`"effort_levels":[]`)) {
			t.Errorf("nil EffortLevels did not normalise to []: %s", out)
		}
		if !bytes.Contains(out, []byte(`"truncated_fields":null`)) {
			t.Errorf("nil TruncatedFields must stay null, not normalise: %s", out)
		}
	}

	for _, tc := range []struct {
		name string
		in   any
	}{
		{"value", o},
		{"pointer", &o},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := json.Marshal(tc.in)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			assertEncodings(t, out)
		})
	}

	t.Run("nested in payload", func(t *testing.T) {
		p := ModelListPayload{ConversationID: "c1", Models: []ModelOption{o}}
		out, err := json.Marshal(p)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		assertEncodings(t, out)
		if p.Models[0].EffortLevels != nil {
			t.Errorf("payload marshalling mutated the caller's backing array: EffortLevels is now %v", p.Models[0].EffortLevels)
		}
	})

	// The entry's own normalisation must not write back through the receiver.
	if o.EffortLevels != nil {
		t.Errorf("MarshalJSON mutated the receiver: EffortLevels is now %v", o.EffortLevels)
	}
}

// #2651: agent and family are keys only a multi_agent client's list carries. An
// untagged row must marshal with NEITHER key, which is what keeps the frame an
// older client reads byte-identical to the one it read before the fields existed.
func TestModelOption_AgentAndFamilyKeys(t *testing.T) {
	untagged, err := json.Marshal(ModelOption{Value: "sonnet"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{`"agent"`, `"family"`} {
		if bytes.Contains(untagged, []byte(key)) {
			t.Errorf("an untagged row carries %s; an older client's frame would change: %s", key, untagged)
		}
	}

	tagged, err := json.Marshal(ModelOption{Value: "gpt-5", Agent: AgentCodex, Family: "gpt-5"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{`"agent":"codex"`, `"family":"gpt-5"`} {
		if !bytes.Contains(tagged, []byte(want)) {
			t.Errorf("a tagged row lacks %s: %s", want, tagged)
		}
	}
}

// TestModelListType_IsNotClaudesVocabulary pins the translation layer this frame
// exists to preserve, as its thinking_progress, rate_limited and model_announced
// siblings above do. The daemon is the ONE place a claude rename lands; naming
// the wire type after claude's own `initialize` subtype, or after its `models`
// array key, would undo that.
//
// The model_announced sibling's trap is this frame's too, and sharper here: a
// strings.Contains(TypeModelList, "model") check would be RED against the correct
// name — "model" is this frame's subject noun and the daemon's own word. The
// discriminating words are claude's subtype "init" and its array key "models",
// and "model_list" contains neither.
//
// The exact-equality pin is the half that fails a WRONG name rather than merely a
// claude-derived one: the negative checks alone leave every other wrong name
// green.
func TestModelListType_IsNotClaudesVocabulary(t *testing.T) {
	if TypeModelList == "initialize" {
		t.Errorf("wire type is claude's control_request subtype %q; it must be the daemon's own name", TypeModelList)
	}
	if strings.Contains(TypeModelList, "init") {
		t.Errorf("wire type %q is derived from claude's subtype (contains %q)", TypeModelList, "init")
	}
	if strings.Contains(TypeModelList, "models") {
		t.Errorf("wire type %q is derived from claude's array key (contains %q)", TypeModelList, "models")
	}
	// The exact pin, naming what the frame IS to a client rather than anything of
	// claude's.
	if TypeModelList != "model_list" {
		t.Errorf("wire type: got %q, want %q", TypeModelList, "model_list")
	}

	// The payload's own bytes, not the envelope's — the envelope carries its own
	// id/ts and would dilute the check. These are regression pins: non-discriminating
	// today by construction, their job is to go red the day someone wires claude's
	// camelCase entry keys back in or adds a field this shape deliberately drops.
	// "models" is NOT in the list and must not be: it is this payload's own wire
	// key, so checking for it would be red against the correct shape.
	//
	// Value is opus[1m] deliberately — the bracketed variant form, whose bytes
	// are the ones an encoding probe is most likely to mangle, and the shape
	// internal/relay's validModel was widened for at #1838.
	body, err := json.Marshal(ModelListPayload{
		ConversationID: "c1",
		Models: []ModelOption{{
			ResolvedModel:    "claude-opus-4-5-20251101",
			Value:            "opus[1m]",
			DisplayName:      "Opus (1M context)",
			EffortLevels:     []string{"low", "medium", "high", "xhigh", "max"},
			SupportsAutoMode: true,
		}},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	for _, key := range []string{
		"resolvedModel", "displayName", "supportsEffort",
		"supportedEffortLevels", "supportsAutoMode", "supportsFastMode",
		"description",
	} {
		if bytes.Contains(body, []byte(key)) {
			t.Errorf("payload carries claude's entry key or a deliberately-dropped field %q: %s", key, body)
		}
	}
}

// TestModelListPayload_RoundTrip pins the populated menu's bytes: the five rows
// claude 2.1.220 returned from a control_request with subtype initialize,
// measured 2026-08-21, in claude's own order.
//
// Haiku's row is the load-bearing one. claude's reply OMITS supportsAutoMode and
// supportedEffortLevels on that entry, and this wire states ONE position for
// absent and empty (ModelOption.MarshalJSON's rationale), so the row carries
// false and [] rather than an elided key. An empty effort list is a real shape a
// client meets on the first frame it ever decodes.
//
// Three values here are CHOSEN rather than captured, in
// TestModelAnnouncedPayload_RoundTrip's register — a fixture is something a client
// author copies as if it were observed, so the parts that were not observed have
// to say so:
//
//   - resolved_model. The 2026-08-21 measurement recorded displayName, value,
//     supportsAutoMode and supportedEffortLevels and no resolvedModel-shaped key,
//     so rows 1-4 carry rate_limited.json's "<unmeasured>" sentinel — whose angle
//     brackets double as the pin on Go's HTML escaping. Row 5 carries the
//     resolution of the `haiku` alias measured on the SAME claude version in the
//     committed capture internal/e2e/realclaude/testdata/dropped_lines_v2.1.220.json,
//     which is a turn announcement rather than an initialize reply. The initialize
//     reply's own per-entry keys have not been measured, and no shipped slice owns
//     doing it — the four sentinels still stand in the fixture, where Go's encoder
//     stores their angle brackets \u-escaped, so grep them as unmeasured rather
//     than as <unmeasured>.
//   - truncated_fields. Populated on row 3 and null on the other four —
//     background_task_roster.json's two-entry pattern. No measured value is
//     anywhere near a producer cap, so no real frame carries this row with this
//     report; the flag is chosen to discriminate a struct that drops or mis-wires
//     the field, and a cut `value` is the sharpest pairing available, being doubly
//     un-sendable.
//   - dropped_models. 2, non-zero so this fixture pins the value rather than the
//     zero encoding (background_task_roster.json's dropped_tasks: 3). The decode
//     counts it since #1812 (turnevent.ModelList.DroppedModels), and #1848 is where
//     the field and that counter meet — MapEvent's arm carries the count through
//     verbatim rather than recomputing it.
func TestModelListPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "model_list.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeModelList {
		t.Errorf("Type: got %q, want %q", env.Type, TypeModelList)
	}

	var payload ModelListPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ConversationID != "c1" {
		t.Errorf("ConversationID: got %q, want %q", payload.ConversationID, "c1")
	}
	if len(payload.Models) != 5 {
		t.Fatalf("Models: got %d entries, want 5", len(payload.Models))
	}

	const allLevels = "low,medium,high,xhigh,max"
	rows := []struct {
		resolvedModel    string
		value            string
		displayName      string
		effortLevels     string // joined, the idiom the roster test uses
		supportsAutoMode bool
		truncatedFields  []string // nil where the row reports no cut
	}{
		{"<unmeasured>", "default", "Default (recommended)", allLevels, true, nil},
		{"<unmeasured>", "opus[1m]", "Opus (1M context)", allLevels, true, nil},
		{"<unmeasured>", "claude-fable-5[1m]", "Fable", allLevels, true, []string{"value"}},
		{"<unmeasured>", "sonnet", "Sonnet", allLevels, true, nil},
		{"claude-haiku-4-5-20251001", "haiku", "Haiku", "", false, nil},
	}
	for i, want := range rows {
		got := payload.Models[i]
		t.Run(want.value, func(t *testing.T) {
			if got.ResolvedModel != want.resolvedModel {
				t.Errorf("ResolvedModel: got %q, want %q", got.ResolvedModel, want.resolvedModel)
			}
			if got.Value != want.value {
				t.Errorf("Value: got %q, want %q", got.Value, want.value)
			}
			if got.DisplayName != want.displayName {
				t.Errorf("DisplayName: got %q, want %q", got.DisplayName, want.displayName)
			}
			if joined := strings.Join(got.EffortLevels, ","); joined != want.effortLevels {
				t.Errorf("EffortLevels: got %q, want %q", joined, want.effortLevels)
			}
			if got.SupportsAutoMode != want.supportsAutoMode {
				t.Errorf("SupportsAutoMode: got %v, want %v", got.SupportsAutoMode, want.supportsAutoMode)
			}
			// Both forms of the per-entry truncation report are pinned across the
			// five rows: populated on one, null on the rest. truncated_fields is
			// deliberately NOT normalised the way effort_levels is — nil and []
			// say the identical thing here.
			if want.truncatedFields == nil && got.TruncatedFields != nil {
				t.Errorf("TruncatedFields: got %v, want nil", got.TruncatedFields)
			}
			if joined, wantJoined := strings.Join(got.TruncatedFields, ","), strings.Join(want.truncatedFields, ","); joined != wantJoined {
				t.Errorf("TruncatedFields: got %q, want %q", joined, wantJoined)
			}
		})
	}

	// The count dimension, decided at the menu level and distinct from any row's
	// text cut. #1848 fills this field from what the decode counted and #1849 puts
	// the frame on the wire, so len(models) + dropped_models IS the menu's true
	// size on a real frame today.
	if payload.DroppedModels != 2 {
		t.Errorf("DroppedModels: got %d, want 2", payload.DroppedModels)
	}

	roundTripEnvelope(t, env, payload, raw)
}

// TestModelListPayload_Empty_RoundTrip pins the frame carrying no models at all,
// on background_task_roster_empty.json's shape.
//
// The opening byte guard is that sibling's own device and is load-bearing rather
// than decoration: without it an omitempty on Models would elide the key from
// BOTH the fixture and the re-marshalled bytes, and the round trip alone would go
// on passing. An empty menu is a positive statement rather than an absence, which
// is why the key is present as [] — see ModelListPayload.MarshalJSON.
func TestModelListPayload_Empty_RoundTrip(t *testing.T) {
	raw := readFixture(t, "model_list_empty.json")

	if !bytes.Contains(canonical(t, raw), []byte(`"models":[]`)) {
		t.Errorf("fixture must carry the models key as an empty array, got: %s", raw)
	}

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeModelList {
		t.Errorf("Type: got %q, want %q", env.Type, TypeModelList)
	}

	var payload ModelListPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ConversationID != "c1" {
		t.Errorf("ConversationID: got %q, want %q", payload.ConversationID, "c1")
	}
	if len(payload.Models) != 0 {
		t.Errorf("Models: got %d entries, want 0", len(payload.Models))
	}
	if payload.DroppedModels != 0 {
		t.Errorf("DroppedModels: got %d, want 0", payload.DroppedModels)
	}

	roundTripEnvelope(t, env, payload, raw)
}

// TestModelListPayload_ZeroValue_RoundTrip pins the encoding of every field's zero
// value across BOTH types, which is what this stream's no-omitempty rule
// (docs/protocol-mobile.md § Interactive events) actually asserts.
//
// It is the fixture that carries most of the wire: five of the nine keys are
// reachable only here, and models carries exactly one entry precisely because an
// empty list cannot reach ModelOption's keys at all. The asymmetry inside that
// entry — effort_levels [] beside truncated_fields null — is the two marshallers'
// decided positions rather than a typo; ModelOption.MarshalJSON says why the two
// list fields differ.
//
// The six byte guards are what make "explicit zero, not elided" checkable at all,
// exactly as TestModelAnnouncedPayload_ZeroValue_RoundTrip's two are: an
// omitempty on any of those keys would elide it from both sides and the round
// trip would go on passing.
//
// The frame is one no producer will ever emit — a real menu names a conversation
// and its rows carry identifiers. It exists for the encoding, not the scenario.
func TestModelListPayload_ZeroValue_RoundTrip(t *testing.T) {
	raw := readFixture(t, "model_list_zero.json")

	// Two payload keys and four entry keys: the six whose zero value an omitempty
	// would elide. The three list keys are covered by the constructed-value tests
	// above, which is the only route that reaches a nil slice at all.
	for _, want := range []string{
		`"conversation_id":""`,
		`"dropped_models":0`,
		`"resolved_model":""`,
		`"value":""`,
		`"display_name":""`,
		`"supports_auto_mode":false`,
	} {
		if !bytes.Contains(canonical(t, raw), []byte(want)) {
			t.Errorf("fixture must carry %s explicitly at its zero value, got: %s", want, raw)
		}
	}

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeModelList {
		t.Errorf("Type: got %q, want %q", env.Type, TypeModelList)
	}

	var payload ModelListPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ConversationID != "" {
		t.Errorf("ConversationID: got %q, want %q", payload.ConversationID, "")
	}
	if payload.DroppedModels != 0 {
		t.Errorf("DroppedModels: got %d, want 0", payload.DroppedModels)
	}
	if len(payload.Models) != 1 {
		t.Fatalf("Models: got %d entries, want 1", len(payload.Models))
	}

	entry := payload.Models[0]
	if entry.ResolvedModel != "" {
		t.Errorf("ResolvedModel: got %q, want %q", entry.ResolvedModel, "")
	}
	if entry.Value != "" {
		t.Errorf("Value: got %q, want %q", entry.Value, "")
	}
	if entry.DisplayName != "" {
		t.Errorf("DisplayName: got %q, want %q", entry.DisplayName, "")
	}
	if entry.SupportsAutoMode {
		t.Errorf("SupportsAutoMode: got %v, want false", entry.SupportsAutoMode)
	}
	// Decoding [] yields a non-nil empty slice while null yields nil, so this pair
	// pins the fixture's asymmetry on the decode side too.
	if entry.EffortLevels == nil || len(entry.EffortLevels) != 0 {
		t.Errorf("EffortLevels: got %v, want an empty non-nil slice", entry.EffortLevels)
	}
	if entry.TruncatedFields != nil {
		t.Errorf("TruncatedFields: got %v, want nil", entry.TruncatedFields)
	}

	roundTripEnvelope(t, env, payload, raw)
}

// TestSlashCommandListPayload_NilCommandsNormalises covers the case no fixture
// could — and here that is the ONLY path there is. Unmarshalling "commands":[]
// always yields a non-nil empty slice, so the nil branch is reachable only by
// constructing the value directly: slash_command_list_empty.json carries the key
// as [] and decodes to an empty non-nil slice, never to nil. A producer (#1720)
// mapping an empty
// or absent claude commands array would hand this type a nil slice and, without
// normalisation, would ship "commands":null to a phone.
//
// Both the value and pointer forms are checked because a pointer-receiver
// marshaller would silently miss the value path roundTripEnvelope takes. This is
// TestModelListPayload_NilModelsNormalises's shape.
func TestSlashCommandListPayload_NilCommandsNormalises(t *testing.T) {
	p := SlashCommandListPayload{ConversationID: "c1"}
	if p.Commands != nil {
		t.Fatalf("precondition: Commands must be nil, got %v", p.Commands)
	}

	for _, tc := range []struct {
		name string
		in   any
	}{
		{"value", p},
		{"pointer", &p},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := json.Marshal(tc.in)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if bytes.Contains(out, []byte(`"commands":null`)) {
				t.Errorf("nil Commands marshalled to null: %s", out)
			}
			if !bytes.Contains(out, []byte(`"commands":[]`)) {
				t.Errorf("nil Commands did not normalise to []: %s", out)
			}
		})
	}

	// The normalisation must not mutate the receiver's copy back into the caller.
	if p.Commands != nil {
		t.Errorf("MarshalJSON mutated the receiver: Commands is now %v", p.Commands)
	}
}

// TestSlashCommand_NilSliceEncodings pins the asymmetry between the entry's two
// list fields at the one value no fixture can reach: decoding [] always yields a
// non-nil slice, so only a constructed value is nil here. Aliases normalises nil
// to [], TruncatedFields does not and stays null.
//
// The fixtures (#1718) now pin "truncated_fields":null in the wire bytes, and
// this test survives that because it reaches what they cannot: the value form,
// the pointer form, the nested-in-payload form, and the caller's-backing-array
// assertion below. An unpinned nil is one that a later omitempty, or a third
// normaliser copied from the field above it, silently turns into something else.
//
// The nested-in-payload subtest proves the entry marshaller fires through the
// payload's, and its trailing assertion is the only thing that would catch a
// payload marshaller normalising entries IN PLACE — that would reach through
// p.Commands[i] into the caller's backing array, and its JSON is byte-identical
// to the correct implementation's, so no byte check could tell the two apart.
//
// This is TestModelOption_NilSliceEncodings's shape, shared assertEncodings
// helper included, so the three assertions are stated once.
func TestSlashCommand_NilSliceEncodings(t *testing.T) {
	c := SlashCommand{Name: "clear"}
	if c.Aliases != nil || c.TruncatedFields != nil {
		t.Fatalf("precondition: both slices must be nil, got %v / %v", c.Aliases, c.TruncatedFields)
	}

	assertEncodings := func(t *testing.T, out []byte) {
		t.Helper()
		if bytes.Contains(out, []byte(`"aliases":null`)) {
			t.Errorf("nil Aliases marshalled to null: %s", out)
		}
		if !bytes.Contains(out, []byte(`"aliases":[]`)) {
			t.Errorf("nil Aliases did not normalise to []: %s", out)
		}
		if !bytes.Contains(out, []byte(`"truncated_fields":null`)) {
			t.Errorf("nil TruncatedFields must stay null, not normalise: %s", out)
		}
	}

	for _, tc := range []struct {
		name string
		in   any
	}{
		{"value", c},
		{"pointer", &c},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := json.Marshal(tc.in)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			assertEncodings(t, out)
		})
	}

	t.Run("nested in payload", func(t *testing.T) {
		p := SlashCommandListPayload{ConversationID: "c1", Commands: []SlashCommand{c}}
		out, err := json.Marshal(p)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		assertEncodings(t, out)
		if p.Commands[0].Aliases != nil {
			t.Errorf("payload marshalling mutated the caller's backing array: Aliases is now %v", p.Commands[0].Aliases)
		}
	})

	// The entry's own normalisation must not write back through the receiver.
	if c.Aliases != nil {
		t.Errorf("MarshalJSON mutated the receiver: Aliases is now %v", c.Aliases)
	}
}

// TestSlashCommandListType_IsNotClaudesVocabulary pins the translation layer
// this frame exists to preserve, as its rate_limited, model_announced and
// model_list siblings do. The daemon is the ONE place a claude rename lands;
// naming the wire type after claude's own vocabulary would undo that.
//
// claude has FOUR words on this path, not the two model_list had: the
// control_request subtype `initialize`, the control reply's array key
// `commands`, the system/init stdout line's `slash_commands`, and that same
// line's `terminal_slash_commands`. Only two of them appear in code below, and
// the reason is the containment lattice:
//
// The SINGULAR subject nouns cannot be checks at all. `command`,
// `slash_command` and `slash` are each a substring of the correct name, so a
// strings.Contains on any of the three would be RED against it — the trap
// TestModelAnnouncedType_IsNotClaudesSubtype records for `model` and
// TestModelListType_IsNotClaudesVocabulary for `models`, three words wide here
// instead of one. claude's keys differ from this frame's subject noun by a
// trailing s, so the discriminating checks are the PLURALS.
//
// One plural check covers all three. `commands` is a substring of
// `slash_commands`, which is a substring of `terminal_slash_commands`, so a name
// derived from either longer key necessarily contains the shorter one. Neither
// longer check could be sole-red for anything; adding them would be
// documentation rather than coverage, and this comment is the documentation.
//
// The `initialize` equality check is likewise subsumed — any name equal to it
// also contains `init` — and is kept anyway, as all three sibling pins keep
// theirs: it is the named statement of the one wrong name a reader would most
// plausibly reach for, so its redundancy is deliberate rather than an oversight.
//
// The exact-equality pin is the half that fails a WRONG name rather than merely
// a claude-derived one: the negative checks alone leave every other wrong name
// green. Naming is this ticket's (#1726) whole deliverable and nothing
// downstream supplies the string, so the pin is load-bearing.
//
// The payload-bytes half below arrived with the shape (#1727), which is what
// every sibling pin ends with. It checks that a populated payload's bytes carry
// none of claude's spellings this shape does NOT adopt, and it is
// non-discriminating today by construction: its job is to go red the day someone
// wires claude's own spellings, or the names-only twin, back in.
func TestSlashCommandListType_IsNotClaudesVocabulary(t *testing.T) {
	if TypeSlashCommandList == "initialize" {
		t.Errorf("wire type is claude's control_request subtype %q; it must be the daemon's own name", TypeSlashCommandList)
	}
	if strings.Contains(TypeSlashCommandList, "init") {
		t.Errorf("wire type %q is derived from claude's subtype (contains %q)", TypeSlashCommandList, "init")
	}
	if strings.Contains(TypeSlashCommandList, "commands") {
		t.Errorf("wire type %q is derived from claude's array key (contains %q)", TypeSlashCommandList, "commands")
	}
	// The exact pin, naming what the frame IS to a client rather than anything of
	// claude's.
	if TypeSlashCommandList != "slash_command_list" {
		t.Errorf("wire type: got %q, want %q", TypeSlashCommandList, "slash_command_list")
	}

	// The payload's own bytes, not the envelope's — the envelope carries its own
	// id/ts and would dilute the check.
	//
	// The check list is DERIVED, not copied, and two derivations are why it is so
	// short. First, this shape adopts all FOUR of claude's per-entry keys, so
	// "name", "description" and "aliases" would be red against the correct struct
	// and must not be checked; only argumentHint differs from what this wire
	// spells (argument_hint), so it is the one per-entry spelling a check can
	// name. Do NOT copy TestModelListType_IsNotClaudesVocabulary's list — it
	// contains "description", which this shape adopts, so carrying it over
	// verbatim would ship a check that fails against the correct implementation.
	// For the same reason there is no deliberately-dropped-field half here, unlike
	// that sibling's: this shape drops nothing.
	//
	// Second, "commands" is absent because it is this payload's own wire key — the
	// same trap the sibling records for "models". Of claude's two array keys on
	// this path, slash_commands is the load-bearing check: terminal_slash_commands
	// is subsumed by it in the byte-containment direction, since any bytes
	// containing the longer contain the shorter. The longer one is kept as the
	// named statement of claude's fourth word, the same deliberate redundancy the
	// `initialize` equality check above is.
	//
	// The row is clear's deliberately: an empty argument hint (the ordinary case,
	// 33 of the capture's 51 entries) and the aliases whose first member, reset,
	// is the desktop Actions menu's own entry.
	body, err := json.Marshal(SlashCommandListPayload{
		ConversationID: "c1",
		Commands: []SlashCommand{{
			Name:         "clear",
			ArgumentHint: "",
			Description:  "Clear conversation history and free up context",
			Aliases:      []string{"reset", "new"},
		}},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	for _, key := range []string{
		"argumentHint", "slash_commands", "terminal_slash_commands",
	} {
		if bytes.Contains(body, []byte(key)) {
			t.Errorf("payload carries claude's spelling %q: %s", key, body)
		}
	}
}

// TestSlashCommandListPayload_RoundTrip pins the populated menu's bytes: five
// rows drawn from the 51-entry array claude 2.1.239 returned from a
// control_request with subtype initialize (the committed capture
// internal/e2e/realclaude/testdata/initialize_control_v2.1.239.json,
// re-measured 2026-08-24), in claude's own capture order.
//
// The five are a SAMPLE, not the whole array and not evidence of any cap. They
// are chosen to cover both of the capture's key sets — 42 of 51 carry name,
// description and argumentHint, the other 9 add aliases, and there is no third
// — and the four properties a client otherwise gets wrong: clear carries the
// aliases whose reset is the desktop Actions menu's own entry (a name-only
// matcher greys out a command that works), config carries a single-element
// alias array beside clear's two, model's <model> hint is the pin on Go's HTML
// escaping, usage pairs aliases with an empty hint, and claude-api carries the
// only sub-0x20 byte on this path — 0x0a — beside raw non-ASCII em dashes that
// the same encoder passes through unescaped.
//
// Two values are CHOSEN rather than captured, in TestModelListPayload_RoundTrip's
// register — a fixture is something a client author copies as if it were
// observed, so the parts that were not observed have to say so:
//
//   - "aliases":[] on rows 1 and 4. claude never sends an empty alias array:
//     zero of the capture's 51 entries carry one and 42 omit the key. [] is the
//     position SlashCommand.MarshalJSON decided for both, so it is what the wire
//     states and what a fixture has to show.
//   - truncated_fields on row 1. Nothing cut that description; the fixture
//     carries it at its full measured length. The flag is chosen to discriminate
//     a struct that drops or mis-wires the field, and claude-api is the sharpest
//     pairing available — 1078 bytes against the capture's 69-byte median, in the
//     10-of-51 population a per-field bound cuts first. Populated on one row and
//     null on the other four is model_list.json's own two-form pattern.
//   - dropped_commands. 2, non-zero so this fixture pins the value rather than
//     the zero encoding. Something counts it since #1826 — streamsup's
//     maxSlashCommandListEntries bounds the entry count and
//     turnevent.SlashCommandList.DroppedCommands carries the cut, and since #2001
//     turnbridge.MapEvent's arm MAPS that onto this field verbatim. The 2 here is
//     nevertheless still a fixture's number rather than a producer's, because this
//     is a decode fixture and no live frame carries either count yet — #2003 is
//     what emits one.
//
// Row 1's description is asserted by measured property rather than as an exact
// string — it is the only row too long for the table, and byte length, rune
// length, the newline count and a prefix pin it more legibly than 1078 bytes of
// literal would.
//
// An omitempty on name, argument_hint or description is red here as well as on
// the zero-value fixture: canonical is json.Compact only, so the round trip
// re-encodes through the struct and the elided key diverges the bytes.
func TestSlashCommandListPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "slash_command_list.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeSlashCommandList {
		t.Errorf("Type: got %q, want %q", env.Type, TypeSlashCommandList)
	}

	var payload SlashCommandListPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ConversationID != "c1" {
		t.Errorf("ConversationID: got %q, want %q", payload.ConversationID, "c1")
	}
	if len(payload.Commands) != 5 {
		t.Fatalf("Commands: got %d entries, want 5", len(payload.Commands))
	}

	rows := []struct {
		name            string
		argumentHint    string
		description     string // empty where the row asserts by property below
		aliases         string // joined, the idiom the roster and model-list tests use
		truncatedFields []string
	}{
		{"claude-api", "", "", "", []string{"description"}},
		{"clear", "[name]", "Start a new session with empty context; previous session stays on disk (resumable with /resume)", "reset,new", nil},
		{"config", "key=value", "Set a setting by key", "settings", nil},
		{"model", "<model>", "Set the AI model for Claude Code", "", nil},
		{"usage", "", "Show session cost, plan usage, and what's contributing to your limits", "cost,stats", nil},
	}
	for i, want := range rows {
		got := payload.Commands[i]
		t.Run(want.name, func(t *testing.T) {
			if got.Name != want.name {
				t.Errorf("Name: got %q, want %q", got.Name, want.name)
			}
			if got.ArgumentHint != want.argumentHint {
				t.Errorf("ArgumentHint: got %q, want %q", got.ArgumentHint, want.argumentHint)
			}
			if want.description != "" && got.Description != want.description {
				t.Errorf("Description: got %q, want %q", got.Description, want.description)
			}
			// Absent and empty are the same [] position (SlashCommand.MarshalJSON's
			// COLLAPSE), so rows 1 and 4 decode to an empty slice and join to "".
			if joined := strings.Join(got.Aliases, ","); joined != want.aliases {
				t.Errorf("Aliases: got %q, want %q", joined, want.aliases)
			}
			// Both forms of the per-entry truncation report are pinned across the
			// five rows: populated on one, null on the rest. truncated_fields is
			// deliberately NOT normalised the way aliases is — nil and [] say the
			// identical thing here.
			if want.truncatedFields == nil && got.TruncatedFields != nil {
				t.Errorf("TruncatedFields: got %v, want nil", got.TruncatedFields)
			}
			if joined, wantJoined := strings.Join(got.TruncatedFields, ","), strings.Join(want.truncatedFields, ","); joined != wantJoined {
				t.Errorf("TruncatedFields: got %q, want %q", joined, wantJoined)
			}
		})
	}

	// claude-api's description by measured property. The newline count is the
	// load-bearing one: 0x0a is the ONLY sub-0x20 byte anywhere across the 51
	// entries' four string fields, so a type-ahead row assuming one line per
	// description meets its counterexample on this frame. The byte/rune split is
	// the em dashes, which Go's encoder passes through as raw UTF-8.
	t.Run("claude-api description", func(t *testing.T) {
		d := payload.Commands[0].Description
		if len(d) != 1078 {
			t.Errorf("byte length: got %d, want 1078", len(d))
		}
		if runes := len([]rune(d)); runes != 1068 {
			t.Errorf("rune length: got %d, want 1068", runes)
		}
		if n := strings.Count(d, "\n"); n != 2 {
			t.Errorf("newline count: got %d, want 2", n)
		}
		const prefix = "Reference for the Claude API / Anthropic SDK — model ids, pricing,"
		if !strings.HasPrefix(d, prefix) {
			t.Errorf("prefix: got %q, want it to start with %q", d, prefix)
		}
	})

	// The count dimension, decided at the menu level and distinct from any row's
	// text cut. A producer counts it since #1826 (streamsup's
	// maxSlashCommandListEntries) and turnbridge.MapEvent's arm now maps that count
	// onto this payload verbatim (#2001), but no frame of this type is produced at
	// all, so a client still cannot read len(commands) + dropped_commands as the
	// menu's true size off any live frame — #2003 is what makes that reading true.
	if payload.DroppedCommands != 2 {
		t.Errorf("DroppedCommands: got %d, want 2", payload.DroppedCommands)
	}

	roundTripEnvelope(t, env, payload, raw)
}

// TestSlashCommandListPayload_Empty_RoundTrip pins the frame carrying no
// commands at all, on TestModelListPayload_Empty_RoundTrip's shape.
//
// The opening byte guard is that sibling's own device and is load-bearing rather
// than decoration: without it an omitempty on Commands would elide the key from
// BOTH the fixture and the re-marshalled bytes, and the round trip alone would go
// on passing. An empty menu is a positive statement — claude offered nothing —
// rather than an absence, which is why the key is present as [] — see
// SlashCommandListPayload.MarshalJSON.
func TestSlashCommandListPayload_Empty_RoundTrip(t *testing.T) {
	raw := readFixture(t, "slash_command_list_empty.json")

	if !bytes.Contains(canonical(t, raw), []byte(`"commands":[]`)) {
		t.Errorf("fixture must carry the commands key as an empty array, got: %s", raw)
	}

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeSlashCommandList {
		t.Errorf("Type: got %q, want %q", env.Type, TypeSlashCommandList)
	}

	var payload SlashCommandListPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ConversationID != "c1" {
		t.Errorf("ConversationID: got %q, want %q", payload.ConversationID, "c1")
	}
	if len(payload.Commands) != 0 {
		t.Errorf("Commands: got %d entries, want 0", len(payload.Commands))
	}
	if payload.DroppedCommands != 0 {
		t.Errorf("DroppedCommands: got %d, want 0", payload.DroppedCommands)
	}

	roundTripEnvelope(t, env, payload, raw)
}

// TestSlashCommandListPayload_ZeroValue_RoundTrip pins the encoding of every
// field's zero value across BOTH types, which is what this stream's no-omitempty
// rule (docs/protocol-mobile.md § Interactive events) actually asserts.
//
// It is the fixture that carries most of the wire: five of the eight keys are
// reachable only here, and commands carries exactly one entry precisely because
// an empty list cannot reach SlashCommand's keys at all. The asymmetry inside
// that entry — aliases [] beside truncated_fields null — is the two marshallers'
// decided positions rather than a typo; SlashCommand.MarshalJSON says why the
// two list fields differ.
//
// The five byte guards are what make "explicit zero, not elided" checkable at
// all, exactly as TestModelListPayload_ZeroValue_RoundTrip's six are. Measured by
// running each of the eight keys as an omitempty mutant over a scratch overlay:
// conversation_id, dropped_commands, name, argument_hint and description reach no
// other assertion in the tree, so this fixture is the only thing that reddens
// them; commands, aliases and truncated_fields are red here too and additionally
// under the constructed-value tests above, the only route that reaches a nil
// slice at all.
//
// The frame is one no producer will ever emit — a real menu names a conversation
// and its rows carry command names. It exists for the encoding, not the scenario.
func TestSlashCommandListPayload_ZeroValue_RoundTrip(t *testing.T) {
	raw := readFixture(t, "slash_command_list_zero.json")

	// Two payload keys and three entry keys: the five whose zero value an
	// omitempty would elide and which no other test reaches.
	for _, want := range []string{
		`"conversation_id":""`,
		`"dropped_commands":0`,
		`"name":""`,
		`"argument_hint":""`,
		`"description":""`,
	} {
		if !bytes.Contains(canonical(t, raw), []byte(want)) {
			t.Errorf("fixture must carry %s explicitly at its zero value, got: %s", want, raw)
		}
	}

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeSlashCommandList {
		t.Errorf("Type: got %q, want %q", env.Type, TypeSlashCommandList)
	}

	var payload SlashCommandListPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ConversationID != "" {
		t.Errorf("ConversationID: got %q, want %q", payload.ConversationID, "")
	}
	if payload.DroppedCommands != 0 {
		t.Errorf("DroppedCommands: got %d, want 0", payload.DroppedCommands)
	}
	if len(payload.Commands) != 1 {
		t.Fatalf("Commands: got %d entries, want 1", len(payload.Commands))
	}

	entry := payload.Commands[0]
	if entry.Name != "" {
		t.Errorf("Name: got %q, want %q", entry.Name, "")
	}
	if entry.ArgumentHint != "" {
		t.Errorf("ArgumentHint: got %q, want %q", entry.ArgumentHint, "")
	}
	if entry.Description != "" {
		t.Errorf("Description: got %q, want %q", entry.Description, "")
	}
	// Decoding [] yields a non-nil empty slice while null yields nil, so this pair
	// pins the fixture's asymmetry on the decode side too.
	if entry.Aliases == nil || len(entry.Aliases) != 0 {
		t.Errorf("Aliases: got %v, want an empty non-nil slice", entry.Aliases)
	}
	if entry.TruncatedFields != nil {
		t.Errorf("TruncatedFields: got %v, want nil", entry.TruncatedFields)
	}

	roundTripEnvelope(t, env, payload, raw)
}

// TestRequestModelListPayload_RoundTrip pins the on-demand model-list REQUEST
// (#2125) against its committed fixture.
//
// It is the ONLY test in this package that pins the wire STRING
// "request_model_list". None of the three registries in compat_test.go can — all
// three key on the Go symbol, so renaming the constant's value moves through them
// consistently and reddens none (measured by mutant on #1895's sibling, recorded
// in the drift-detectors overview).
//
// THE CONVERSATION ID IS THE TIE, not an arbitrary string. It is "c1", exactly the
// conversation_id the committed model_list.json carries, so the two files describe
// ONE exchange — the ask, and the frame shape that answers it. The correlation
// itself is NOT pinned here and cannot be: model_list.json is the unsolicited form
// (live lane and connect-time reconcile), which carries no in_reply_to at all. The
// answer's envelope — same type, same payload source, in_reply_to set, event_id
// absent — is pinned against bytes the daemon actually emits, in
// internal/relay's handler test, which is stronger evidence than a hand-written
// file for a shape this package never constructs.
//
// InReplyTo is asserted NIL, the structural half of the classification
// cmd/pyry/relay_guard_test.go's inboundTypes records: this frame is a request,
// and the correlation runs FROM it rather than to it.
func TestRequestModelListPayload_RoundTrip(t *testing.T) {
	raw := readFixture(t, "request_model_list.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeRequestModelList {
		t.Errorf("Type: got %q, want %q", env.Type, TypeRequestModelList)
	}
	if env.InReplyTo != nil {
		t.Errorf("InReplyTo: got pointer to %d, want nil — this frame is a request, not a reply", *env.InReplyTo)
	}

	var payload RequestModelListPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if want := "c1"; payload.ConversationID != want {
		t.Errorf("ConversationID: got %q, want %q (model_list.json's conversation — the two fixtures describe one exchange)", payload.ConversationID, want)
	}

	roundTripEnvelope(t, env, payload, raw)
}

// TestRequestModelListPayload_WireKeys pins the payload's COMPLETE set of wire
// keys, so a later field cannot be added without this failing. It is the
// machine-checked form of the frame's central omission — NO REQUEST-ID KEY,
// because correlation rides the envelope's InReplyTo.
//
// It marshals a freshly populated struct rather than reading the fixture, and that
// is the whole point: adding an undeclared field reddens both this and the round
// trip, but regenerating the fixture under that change turns the round trip green
// again while this stays red. It is also what exercises the struct tags at all —
// the round trip above re-emits env.Payload's own json.RawMessage bytes, so an
// omitempty added here for tidiness is invisible to it.
func TestRequestModelListPayload_WireKeys(t *testing.T) {
	b, err := json.Marshal(RequestModelListPayload{ConversationID: "c1"})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal payload into key set: %v", err)
	}

	want := map[string]bool{"conversation_id": true}
	for k := range got {
		if !want[k] {
			t.Errorf("unexpected wire key %q: the payload's key set is fixed at %v — correlation rides the envelope's in_reply_to, so this frame carries no request-id key", k, want)
		}
	}
	for k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("missing wire key %q, got: %s — the key is always present (no omitempty), so absent and empty stay the same case", k, b)
		}
	}
}

// TestRequestModelListPayload_ZeroValue_KeyPresent pins the no-omitempty decision
// directly: a zero-valued payload still carries the key.
//
// Separate from the key-set test above because the two fail on different mutants.
// That one marshals a POPULATED struct, so an omitempty added to ConversationID
// leaves it green; only a zero value forces the tag to speak.
func TestRequestModelListPayload_ZeroValue_KeyPresent(t *testing.T) {
	b, err := json.Marshal(RequestModelListPayload{})
	if err != nil {
		t.Fatalf("marshal zero payload: %v", err)
	}
	if want := `{"conversation_id":""}`; string(b) != want {
		t.Errorf("zero payload: got %s, want %s — the key is unconditional, so a client that names no conversation still sends a well-formed frame", b, want)
	}
}

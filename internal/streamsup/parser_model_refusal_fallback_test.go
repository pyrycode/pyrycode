package streamsup

import (
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// EVERY LINE IN THIS FILE IS HAND-BUILT FROM DOCUMENTATION, AND NOT ONE BYTE OF IT
// IS AN OBSERVATION. That is the whole provenance of this arm rather than a
// division of labour: permission_denied had seven captured lines to keep its
// hermetic rows honest, and system/model_refusal_fallback has NONE. No capture
// exists and none can be taken, because a refusal cannot be provoked without a
// prompt this repo should not contain.
//
// The keys these rows use were read 2026-09-07 from the Claude Code headless docs
// and @anthropic-ai/claude-agent-sdk@0.3.263's sdk.d.ts, with the daemon on claude
// 2.1.259. So a row here may assert WHAT THE ARM DOES GIVEN A SHAPE, and may never
// be read as a claim about what claude actually sends — not about which keys arrive,
// not about which are always present, and above all not about a key's ABSENCE.
// #1419 had to repair two shipped fixtures whose hand-built shape had quietly become
// a claim about absence; the label above is what stops this file becoming a third.
//
// The rows treat every key as optional for the same reason. A type definition names
// keys the wire does not always send — `effort` is documented on system/init in that
// same sdk.d.ts and absent from all 58 committed init lines — so "absent" is the
// default expectation here, not the edge case.

// refusalFallbackLineFixture builds one system/model_refusal_fallback line carrying
// the given claude keys. The two envelope keys are set here so no row can misspell
// them and pass by falling into a different arm.
func refusalFallbackLineFixture(t *testing.T, fields map[string]any) string {
	t.Helper()
	line := map[string]any{"type": "system", "subtype": "model_refusal_fallback"}
	for k, v := range fields {
		line[k] = v
	}
	raw, err := json.Marshal(line)
	if err != nil {
		t.Fatalf("marshalling model_refusal_fallback line fixture: %v", err)
	}
	return string(raw)
}

// oneRefusalFallback feeds a line to a fresh parser and returns the single
// turnevent.ModelRefusalFallback it emitted, failing on any other count. "Exactly
// one" is asserted rather than "at least one" so a future arm that emitted a second
// event for the same line shows up here.
func oneRefusalFallback(t *testing.T, line string) turnevent.ModelRefusalFallback {
	t.Helper()
	events := collectEvents(line)
	if len(events) != 1 {
		t.Fatalf("collectEvents(%s) returned %d events, want exactly 1", line, len(events))
	}
	fallback, ok := events[0].(turnevent.ModelRefusalFallback)
	if !ok {
		t.Fatalf("event is %T, want turnevent.ModelRefusalFallback", events[0])
	}
	return fallback
}

// fullRefusalFallbackKeys is the documented eleven-key line: the six this event
// carries and the five it deliberately does not. Rows that need a complete line take
// a copy, so the DROPPED five travel with every such row rather than only with the
// leak test — a decode target that grew a sixth field would then redden wherever it
// was read, not only where someone remembered to look.
func fullRefusalFallbackKeys() map[string]any {
	return map[string]any{
		"trigger":                   "refusal",
		"direction":                 "retry",
		"scope":                     "session",
		"original_model":            "claude-opus-4-1-20250805",
		"fallback_model":            "claude-sonnet-4-5-20250929",
		"request_id":                "req_011CQ8xk2vN4pT9mB3wZ",
		"api_refusal_category":      "cyber",
		"api_refusal_explanation":   "the request asked for working exploit code",
		"content":                   "Switched to claude-sonnet-4-5 after a refusal.",
		"refused_user_message_uuid": "3f9c1d5a-7b21-4e88-9a0c-6d2e4f8b1a37",
		"retracted_message_uuids": []any{
			"8c2a4e10-5d33-4f77-b1e9-0a7c3b6d2f54",
		},
	}
}

// TestParser_RefusalFallbackCarriesClaudesSixFields is AC 1: the line is consumed by
// the subtype dispatch rather than dropped, and produces one event carrying all six
// values under the DAEMON's names.
//
// The line it feeds is the full documented eleven-key shape, not a six-key
// convenience, so the same row proves the other half at the same time: five of
// claude's keys reach no field, and they reach none STRUCTURALLY — none is declared
// on systemModelRefusalFallbackLine, so encoding/json discards them with no scrubbing
// step to maintain. A sweep asserting their values appear in no field of the event is
// what makes that checkable rather than merely intended.
func TestParser_RefusalFallbackCarriesClaudesSixFields(t *testing.T) {
	t.Parallel()

	keys := fullRefusalFallbackKeys()
	got := oneRefusalFallback(t, refusalFallbackLineFixture(t, keys))

	want := turnevent.ModelRefusalFallback{
		Scope:              "session",
		OriginalModel:      "claude-opus-4-1-20250805",
		FallbackModel:      "claude-sonnet-4-5-20250929",
		RefusalCategory:    "cyber",
		RefusalExplanation: "the request asked for working exploit code",
		Banner:             "Switched to claude-sonnet-4-5 after a refusal.",
	}
	if got.Scope != want.Scope || got.OriginalModel != want.OriginalModel ||
		got.FallbackModel != want.FallbackModel || got.RefusalCategory != want.RefusalCategory ||
		got.RefusalExplanation != want.RefusalExplanation || got.Banner != want.Banner {
		t.Errorf("carried fields = %+v, want %+v — each of claude's six keys reaches its "+
			"field verbatim, and api_refusal_category lands under the daemon's name", got, want)
	}
	if got.TruncatedFields != nil || got.DroppedFields != nil {
		t.Errorf("reports = %v/%v, want nil/nil — every value is well under its cap",
			got.TruncatedFields, got.DroppedFields)
	}

	// The five keys that are deliberately not fields. `direction` and `trigger` are
	// excluded from the sweep despite being on the line: they are constants restating
	// the subtype, so searching for "retry" or "refusal" inside claude's own prose
	// would fail on the prose rather than on a leak.
	for _, key := range []string{"request_id", "refused_user_message_uuid"} {
		leaked, ok := keys[key].(string)
		if !ok {
			t.Fatalf("fixture key %q is not a string; the sweep below cannot check it", key)
		}
		assertRefusalFallbackOmits(t, got, key, leaked)
	}
	for _, uuid := range keys["retracted_message_uuids"].([]any) {
		assertRefusalFallbackOmits(t, got, "retracted_message_uuids", uuid.(string))
	}
}

// assertRefusalFallbackOmits fails if any carried string of the event contains the
// given value. Used for the five keys the decode target does not declare — claude's
// message identity above all, which no daemon surface can join against and which a
// consumer must therefore never be handed.
func assertRefusalFallbackOmits(t *testing.T, got turnevent.ModelRefusalFallback, key, value string) {
	t.Helper()
	for _, field := range []string{got.Scope, got.OriginalModel, got.FallbackModel,
		got.RefusalCategory, got.RefusalExplanation, got.Banner} {
		if strings.Contains(field, value) {
			t.Errorf("field %q carries claude's %s; that key is not declared on the decode "+
				"target and may not reach a consumer", field, key)
		}
	}
}

// TestParser_RefusalFallbackBoundsEveryClaudeString drives an over-cap value through
// each of the six claude-derived strings and asserts what the bound did — AC 4. Each
// row states the field's OVERFLOW ANSWER as well as its cap, because the two answers
// are the substance of this arm and a row that only checked the length would pass on
// either one.
//
// The at-cap row is what stops the whole table passing on an off-by-one bound: it
// drives a value of exactly the cap through all six fields and requires BOTH report
// slices to come back nil, so a `>=` where the arm has `>` reddens here rather than
// silently emptying a valid model label in production.
func TestParser_RefusalFallbackBoundsEveryClaudeString(t *testing.T) {
	t.Parallel()

	overToken := strings.Repeat("t", maxTaskFieldID+1)
	overProse := strings.Repeat("p", maxDenialProse+1)
	atToken := strings.Repeat("t", maxTaskFieldID)
	atProse := strings.Repeat("p", maxDenialProse)

	tests := []struct {
		name   string
		fields map[string]any
		want   turnevent.ModelRefusalFallback
		why    string
	}{
		{
			name:   "scope over cap is dropped, not cut",
			fields: map[string]any{"scope": overToken, "fallback_model": "claude-sonnet-4-5"},
			want: turnevent.ModelRefusalFallback{
				FallbackModel: "claude-sonnet-4-5", DroppedFields: []string{"scope"},
			},
			why: "a consumer matches this token against the two documented values, so a cut " +
				"scope matches nothing while still looking like one",
		},
		{
			name:   "original_model over cap is dropped, not cut",
			fields: map[string]any{"original_model": overToken},
			want:   turnevent.ModelRefusalFallback{DroppedFields: []string{"original_model"}},
			why:    "a model label is matched, never read as prose",
		},
		{
			name:   "fallback_model over cap is dropped, not cut",
			fields: map[string]any{"fallback_model": overToken},
			want:   turnevent.ModelRefusalFallback{DroppedFields: []string{"fallback_model"}},
			why: "the label is JOINED against the ModelAnnounced the client already holds; a cut " +
				"one joins to nothing while looking real, which is worse than an absent one",
		},
		{
			name:   "api_refusal_category over cap is dropped under the daemon's name",
			fields: map[string]any{"api_refusal_category": overToken},
			want:   turnevent.ModelRefusalFallback{DroppedFields: []string{"refusal_category"}},
			why: "a token from claude's open classification set, matched rather than read — and " +
				"the report names the daemon's field, not claude's api_ prefixed key",
		},
		{
			name:   "api_refusal_explanation over cap is cut and reported",
			fields: map[string]any{"api_refusal_explanation": overProse},
			want: turnevent.ModelRefusalFallback{
				RefusalExplanation: atProse, TruncatedFields: []string{"refusal_explanation"},
			},
			why: "prose, where a cut sentence still reads as what it is",
		},
		{
			name:   "content over cap is cut and reported as banner",
			fields: map[string]any{"content": overProse},
			want: turnevent.ModelRefusalFallback{
				Banner: atProse, TruncatedFields: []string{"banner"},
			},
			why: "prose, as the explanation is; the report names the daemon's field rather than " +
				"claude's content key",
		},
		{
			name: "every field over cap at once, reported in declaration order",
			fields: map[string]any{
				"scope": overToken, "original_model": overToken, "fallback_model": overToken,
				"api_refusal_category": overToken, "api_refusal_explanation": overProse,
				"content": overProse,
			},
			want: turnevent.ModelRefusalFallback{
				RefusalExplanation: atProse,
				Banner:             atProse,
				TruncatedFields:    []string{"refusal_explanation", "banner"},
				DroppedFields: []string{"scope", "original_model", "fallback_model",
					"refusal_category"},
			},
			why: "both reports are ordered by the arm's own sequential calls, so a reader sees " +
				"the order rather than inferring it from Go's operand rule",
		},
		{
			name: "at cap trips neither bound",
			fields: map[string]any{
				"scope": atToken, "original_model": atToken, "fallback_model": atToken,
				"api_refusal_category": atToken, "api_refusal_explanation": atProse,
				"content": atProse,
			},
			want: turnevent.ModelRefusalFallback{
				Scope: atToken, OriginalModel: atToken, FallbackModel: atToken,
				RefusalCategory: atToken, RefusalExplanation: atProse, Banner: atProse,
			},
			why: "the boundary is <= on both answers; a value of exactly the cap is not over it",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := oneRefusalFallback(t, refusalFallbackLineFixture(t, tt.fields))
			if got.Scope != tt.want.Scope || got.OriginalModel != tt.want.OriginalModel ||
				got.FallbackModel != tt.want.FallbackModel ||
				got.RefusalCategory != tt.want.RefusalCategory ||
				got.RefusalExplanation != tt.want.RefusalExplanation || got.Banner != tt.want.Banner {
				t.Errorf("carried fields = %q/%q/%q/%q/%d bytes/%d bytes, "+
					"want %q/%q/%q/%q/%d bytes/%d bytes — %s",
					got.Scope, got.OriginalModel, got.FallbackModel, got.RefusalCategory,
					len(got.RefusalExplanation), len(got.Banner),
					tt.want.Scope, tt.want.OriginalModel, tt.want.FallbackModel,
					tt.want.RefusalCategory, len(tt.want.RefusalExplanation), len(tt.want.Banner), tt.why)
			}
			if strings.Join(got.TruncatedFields, ",") != strings.Join(tt.want.TruncatedFields, ",") {
				t.Errorf("TruncatedFields = %v, want %v — %s",
					got.TruncatedFields, tt.want.TruncatedFields, tt.why)
			}
			if strings.Join(got.DroppedFields, ",") != strings.Join(tt.want.DroppedFields, ",") {
				t.Errorf("DroppedFields = %v, want %v — %s",
					got.DroppedFields, tt.want.DroppedFields, tt.why)
			}
		})
	}
}

// TestParser_RefusalFallbackGatesOnNoField is AC 2, and it is the decision this arm's
// whole shape rests on rather than a tolerance check.
//
// NO FIELD GATES THE EMIT, unlike emitModelAnnounced (which emits nothing for a
// model-less init) and emitCompactionBoundary (which emits nothing without
// compact_metadata). Those two gate because the gated field IS the payload and a
// sibling event already reported the fact; here the SUBTYPE is the payload — nothing
// else on this surface explains why the model changed — so a field-less line is still
// news. A gate would also re-drop the line silently the first time claude renames a
// key, which is the exact defect this ticket exists to end.
//
// The gate question is unusually live here BECAUSE the field set is
// documentation-derived: a key nobody has observed is a key that may simply not
// arrive, so an arm that required one would be built on a guess.
func TestParser_RefusalFallbackGatesOnNoField(t *testing.T) {
	t.Parallel()

	// Each row omits one of the six and keeps the rest, so a gate on ANY single field
	// reddens exactly one row and names itself. The last row omits all six, which is
	// the case a per-field sweep cannot reach.
	present := []string{"scope", "original_model", "fallback_model",
		"api_refusal_category", "api_refusal_explanation", "content"}

	for _, omitted := range present {
		t.Run("without "+omitted, func(t *testing.T) {
			t.Parallel()
			fields := map[string]any{}
			for _, key := range present {
				if key == omitted {
					continue
				}
				fields[key] = "v"
			}
			got := oneRefusalFallback(t, refusalFallbackLineFixture(t, fields))
			if got.TruncatedFields != nil || got.DroppedFields != nil {
				t.Errorf("reports = %v/%v, want nil/nil — an ABSENT field was not bounded by the "+
					"daemon, and reporting it as bounded would be the daemon claiming credit for "+
					"claude's silence", got.TruncatedFields, got.DroppedFields)
			}
			if empty := refusalFallbackEmptyFields(got); len(empty) != 1 || empty[0] != daemonFieldName(omitted) {
				t.Errorf("empty fields = %v, want exactly [%s] — the omitted key lands empty and "+
					"nothing else does", empty, daemonFieldName(omitted))
			}
		})
	}

	t.Run("a line carrying none of the six still emits", func(t *testing.T) {
		t.Parallel()
		got := oneRefusalFallback(t, `{"type":"system","subtype":"model_refusal_fallback"}`)
		// Field by field rather than a struct comparison: the two report slices make
		// turnevent.ModelRefusalFallback incomparable, and nil-versus-empty is exactly
		// what this row must see.
		if len(refusalFallbackEmptyFields(got)) != 6 {
			t.Errorf("event = %+v, want every carried field empty — nothing was there to carry", got)
		}
		if got.TruncatedFields != nil || got.DroppedFields != nil {
			t.Errorf("reports = %v/%v, want nil/nil — an absent field is not a bounded one",
				got.TruncatedFields, got.DroppedFields)
		}
	})
}

// refusalFallbackEmptyFields names the carried fields that came back empty, under the
// daemon's names and in declaration order. The gate rows compare against it rather
// than against six separate conditions so a row says WHICH field went missing.
func refusalFallbackEmptyFields(got turnevent.ModelRefusalFallback) []string {
	var empty []string
	for _, f := range []struct {
		name  string
		value string
	}{
		{"scope", got.Scope},
		{"original_model", got.OriginalModel},
		{"fallback_model", got.FallbackModel},
		{"refusal_category", got.RefusalCategory},
		{"refusal_explanation", got.RefusalExplanation},
		{"banner", got.Banner},
	} {
		if f.value == "" {
			empty = append(empty, f.name)
		}
	}
	return empty
}

// daemonFieldName maps claude's key to the daemon's field name for the three that
// differ. The mapping is written out rather than derived, because the whole point of
// the rename is that no rule connects the two — the report names the field it
// describes, not the key it came from.
func daemonFieldName(claudeKey string) string {
	switch claudeKey {
	case "api_refusal_category":
		return "refusal_category"
	case "api_refusal_explanation":
		return "refusal_explanation"
	case "content":
		return "banner"
	default:
		return claudeKey
	}
}

// refusalFallbackUndecodableMsg is the Debug message the undecodable path emits, as a
// test-local literal rather than a reference to the production string —
// harnessNudgeDropMsg's rule. A test built from the value it validates asserts
// nothing; this literal is what makes an edit to the production message go RED.
const refusalFallbackUndecodableMsg = "streamsup: dropping undecodable system line"

// TestParser_RefusalFallbackConsumesUndecodable is AC 3, and the answer it pins is
// deliberate rather than an oversight.
//
// One wrong-typed value makes encoding/json fail the WHOLE decode target, and this
// arm's answer is a debug record and no event while still reporting the line
// consumed — emitPermissionDenied's undecodable path verbatim. PER-FIELD TYPE
// TOLERANCE WOULD BE A REAL DIVERGENCE FROM THE FAMILY AND IS NOT WANTED: the failure
// has not been observed, and a capture is what would justify building against it.
// Fail-closed means one absurd field costs the frame rather than producing a
// half-true one.
//
// THE RECORD IS WHAT MAKES "CONSUMED" CHECKABLE, and asserting zero events would not
// be. A system line the arm declined would fall through to the ignoredLineTypes
// branch's own silent drop and emit zero events too, so the two outcomes are
// indistinguishable by event count; they differ only in which Debug fires — this one
// naming the subtype, that one naming the type. Reaching emitUnrecognized is not
// among the possibilities either way, which is why the criterion is consumption
// rather than the absence of an unrecognized row: that absence holds before this
// ticket and after it.
//
// The record's attribute set is asserted CLOSED, and that is a security obligation
// rather than tidiness. Both prose fields are claude's writing about a refused
// request and can quote the user's own words back; the undecodable path is exactly
// where a drop site would be tempted to explain itself with the line's contents, so
// this is the assertion that keeps the arm's no-logging rule real.
func TestParser_RefusalFallbackConsumesUndecodable(t *testing.T) {
	t.Parallel()

	// One row per SHAPE of wrong type, not per field: the decode fails on the target as
	// a whole, so a numeric token and a composite prose field are the same failure
	// arriving through different keys.
	tests := []struct {
		name string
		line string
		why  string
	}{
		{
			name: "numeric scope",
			line: `{"type":"system","subtype":"model_refusal_fallback","scope":7}`,
			why:  "a scalar of the wrong type",
		},
		{
			name: "object-valued content",
			line: `{"type":"system","subtype":"model_refusal_fallback","content":{"text":"hi"}}`,
			why:  "a composite where a string is declared",
		},
		{
			name: "array-valued api_refusal_category with every other key well-formed",
			line: `{"type":"system","subtype":"model_refusal_fallback","scope":"session",` +
				`"original_model":"claude-opus-4-1","api_refusal_category":["cyber"]}`,
			why: "one bad key costs the whole frame; the two good ones do not rescue it, " +
				"which is what fail-closed means here",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := &logRecorder{}
			var got []turnevent.Event
			p := NewParser(func(ev turnevent.Event) { got = append(got, ev) }, slog.New(rec))
			if _, err := p.Write([]byte(tt.line + "\n")); err != nil {
				t.Fatalf("writing %s: %v", tt.line, err)
			}

			if len(got) != 0 {
				t.Fatalf("the line produced %d event(s), want 0 — %s", len(got), tt.why)
			}
			records := rec.withMessage(refusalFallbackUndecodableMsg)
			if len(records) != 1 {
				t.Fatalf("records with message %q = %d, want 1 — the line must be CONSUMED by this "+
					"arm, and this record is the only thing that distinguishes that from the "+
					"ignoredLineTypes branch's own silent drop (all records: %v)",
					refusalFallbackUndecodableMsg, len(records), rec.all())
			}
			attrs := records[0].attrs
			if got := attrs["subtype"]; got != "model_refusal_fallback" {
				t.Errorf("subtype attr = %q, want %q — the record names the subtype it dropped",
					got, "model_refusal_fallback")
			}
			// Closed, and over EVERY record the line produced rather than only this one: a
			// decoded value leaking into some other record on the same path would be the
			// same failure. The subtype is a message-name keyword this package owns, not a
			// byte of claude's line.
			for _, r := range rec.all() {
				for k := range r.attrs {
					if k != "subtype" {
						t.Errorf("record %q carries attribute %q: no field decoded from claude's "+
							"line may reach a log on this path", r.msg, k)
					}
				}
			}
		})
	}
}

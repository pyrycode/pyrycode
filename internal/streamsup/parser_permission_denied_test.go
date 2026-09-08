package streamsup

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// The lines in this file are HAND-BUILT, and that is a deliberate division of
// labour rather than a shortcut. claude's own bytes are replayed in
// permission_denial_capture_test.go, which asserts what the arm does with the
// seven denials on record; nothing here may assert on a shape this file invented.
// What these rows exercise is what no capture contains — an over-cap value, an
// absent field, a decision reason claude has never yet sent — so they are
// necessarily authored, and the capture is what keeps them honest about the shape
// they are authored in.

// denialLineFixture builds one system/permission_denied line carrying the given
// claude keys. The two envelope keys are set here so no row can misspell them and
// pass by falling into a different arm.
func denialLineFixture(t *testing.T, fields map[string]any) string {
	t.Helper()
	line := map[string]any{"type": "system", "subtype": "permission_denied"}
	for k, v := range fields {
		line[k] = v
	}
	raw, err := json.Marshal(line)
	if err != nil {
		t.Fatalf("marshalling denial line fixture: %v", err)
	}
	return string(raw)
}

// oneDenial feeds a line to a fresh parser and returns the single
// turnevent.ToolCallDenied it emitted, failing on any other count. "Exactly one"
// is asserted rather than "at least one" so a future arm that emitted a second
// event for the same line shows up here.
func oneDenial(t *testing.T, line string) turnevent.ToolCallDenied {
	t.Helper()
	events := collectEvents(line)
	if len(events) != 1 {
		t.Fatalf("collectEvents(%s) returned %d events, want exactly 1", line, len(events))
	}
	denial, ok := events[0].(turnevent.ToolCallDenied)
	if !ok {
		t.Fatalf("event is %T, want turnevent.ToolCallDenied", events[0])
	}
	return denial
}

// TestParser_PermissionDeniedBoundsEveryClaudeString drives an over-cap value
// through each of the five claude-derived strings and asserts what the bound did
// — AC 2. Each row states the field's OVERFLOW ANSWER as well as its cap, because
// the two answers are the substance of this arm and a row that only checked the
// length would pass on either one.
//
// The at-cap row is what stops the whole table passing on an off-by-one bound: it
// drives a value of exactly the cap through all five fields and requires BOTH
// report slices to come back nil, so a `>=` where the arm has `>` reddens here
// rather than silently emptying a valid tool name in production.
func TestParser_PermissionDeniedBoundsEveryClaudeString(t *testing.T) {
	t.Parallel()

	overID := strings.Repeat("i", maxTaskFieldID+1)
	overProse := strings.Repeat("p", maxDenialProse+1)

	tests := []struct {
		name   string
		fields map[string]any
		want   turnevent.ToolCallDenied
		why    string
	}{
		{
			name:   "tool_name over cap is dropped, not cut",
			fields: map[string]any{"tool_name": overID, "tool_use_id": "toolu_1"},
			want: turnevent.ToolCallDenied{
				ToolCallID: "toolu_1", DroppedFields: []string{"tool_name"},
			},
			why: "a consumer switches on this token against claude's tool set, so a cut name " +
				"would match nothing while still looking like a tool",
		},
		{
			name:   "tool_use_id over cap is dropped, not cut",
			fields: map[string]any{"tool_name": "Bash", "tool_use_id": overID},
			want: turnevent.ToolCallDenied{
				ToolName: "Bash", DroppedFields: []string{"tool_call_id"},
			},
			why: "the id is JOINED against a tool call the client already saw; a cut id joins to " +
				"nothing while looking like a real handle, which is strictly worse than an absent one",
		},
		{
			name:   "message over cap is cut and reported",
			fields: map[string]any{"message": overProse},
			want: turnevent.ToolCallDenied{
				Message: strings.Repeat("p", maxDenialProse), TruncatedFields: []string{"message"},
			},
			why: "prose, where a cut sentence still reads as what it is",
		},
		{
			name:   "decision_reason_type over cap is dropped, not cut",
			fields: map[string]any{"decision_reason_type": overID},
			want: turnevent.ToolCallDenied{
				DroppedFields: []string{"decision_reason_type"},
			},
			why: "a token from claude's documented set (classifier, asyncAgent, mode, rule), " +
				"matched rather than read",
		},
		{
			name:   "decision_reason over cap is cut and reported",
			fields: map[string]any{"decision_reason": overProse},
			want: turnevent.ToolCallDenied{
				DecisionReason:  strings.Repeat("p", maxDenialProse),
				TruncatedFields: []string{"decision_reason"},
			},
			why: "prose, as message is",
		},
		{
			name: "every field over cap at once, reported in declaration order",
			fields: map[string]any{
				"tool_name": overID, "tool_use_id": overID, "message": overProse,
				"decision_reason_type": overID, "decision_reason": overProse,
			},
			want: turnevent.ToolCallDenied{
				Message:         strings.Repeat("p", maxDenialProse),
				DecisionReason:  strings.Repeat("p", maxDenialProse),
				TruncatedFields: []string{"message", "decision_reason"},
				DroppedFields:   []string{"tool_name", "tool_call_id", "decision_reason_type"},
			},
			why: "both reports are ordered by the arm's own sequential calls, so a reader sees " +
				"the order rather than inferring it from Go's operand rule",
		},
		{
			name: "at cap trips neither bound",
			fields: map[string]any{
				"tool_name":            strings.Repeat("i", maxTaskFieldID),
				"tool_use_id":          strings.Repeat("i", maxTaskFieldID),
				"message":              strings.Repeat("p", maxDenialProse),
				"decision_reason_type": strings.Repeat("i", maxTaskFieldID),
				"decision_reason":      strings.Repeat("p", maxDenialProse),
			},
			want: turnevent.ToolCallDenied{
				ToolName:           strings.Repeat("i", maxTaskFieldID),
				ToolCallID:         strings.Repeat("i", maxTaskFieldID),
				Message:            strings.Repeat("p", maxDenialProse),
				DecisionReasonType: strings.Repeat("i", maxTaskFieldID),
				DecisionReason:     strings.Repeat("p", maxDenialProse),
			},
			why: "the boundary is <= on both answers; a value of exactly the cap is not over it",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := oneDenial(t, denialLineFixture(t, tt.fields))
			if got.ToolName != tt.want.ToolName || got.ToolCallID != tt.want.ToolCallID ||
				got.Message != tt.want.Message || got.DecisionReasonType != tt.want.DecisionReasonType ||
				got.DecisionReason != tt.want.DecisionReason {
				t.Errorf("carried fields = %q/%q/%d bytes/%q/%d bytes, want %q/%q/%d bytes/%q/%d bytes — %s",
					got.ToolName, got.ToolCallID, len(got.Message), got.DecisionReasonType,
					len(got.DecisionReason), tt.want.ToolName, tt.want.ToolCallID,
					len(tt.want.Message), tt.want.DecisionReasonType, len(tt.want.DecisionReason), tt.why)
			}
			if strings.Join(got.TruncatedFields, ",") != strings.Join(tt.want.TruncatedFields, ",") {
				t.Errorf("TruncatedFields = %v, want %v — %s", got.TruncatedFields, tt.want.TruncatedFields, tt.why)
			}
			if strings.Join(got.DroppedFields, ",") != strings.Join(tt.want.DroppedFields, ",") {
				t.Errorf("DroppedFields = %v, want %v — %s", got.DroppedFields, tt.want.DroppedFields, tt.why)
			}
		})
	}
}

// TestParser_PermissionDeniedSeparatesClaudesAbsenceFromTheDaemonsBound is AC 3's
// hermetic half and the reason the event carries two report slices at all.
//
// The two rows differ in ONE thing — whether claude sent the decision fields — and
// agree on everything else, so the test is a statement about the reports rather
// than about the values: a field claude omitted is named in NEITHER slice, which
// is what lets a later reader tell "claude said nothing" from "the daemon emptied
// it". Collapse the reports and these two rows become indistinguishable.
func TestParser_PermissionDeniedSeparatesClaudesAbsenceFromTheDaemonsBound(t *testing.T) {
	t.Parallel()

	t.Run("carried when claude sends them", func(t *testing.T) {
		t.Parallel()
		got := oneDenial(t, denialLineFixture(t, map[string]any{
			"tool_name": "Bash", "tool_use_id": "toolu_1",
			"decision_reason_type": "rule", "decision_reason": "a deny rule matched Bash(rm:*)",
		}))
		if got.DecisionReasonType != "rule" || got.DecisionReason != "a deny rule matched Bash(rm:*)" {
			t.Errorf("decision fields = %q/%q, want %q/%q — carried verbatim when claude sends them",
				got.DecisionReasonType, got.DecisionReason, "rule", "a deny rule matched Bash(rm:*)")
		}
		if got.TruncatedFields != nil || got.DroppedFields != nil {
			t.Errorf("reports = %v/%v, want nil/nil — an in-cap value is not a bounded one",
				got.TruncatedFields, got.DroppedFields)
		}
	})

	t.Run("empty and unreported when claude omits them", func(t *testing.T) {
		t.Parallel()
		got := oneDenial(t, denialLineFixture(t, map[string]any{
			"tool_name": "Bash", "tool_use_id": "toolu_1",
		}))
		if got.DecisionReasonType != "" || got.DecisionReason != "" {
			t.Errorf("decision fields = %q/%q, want empty — claude sent neither",
				got.DecisionReasonType, got.DecisionReason)
		}
		if got.TruncatedFields != nil || got.DroppedFields != nil {
			t.Errorf("reports = %v/%v, want nil/nil — an ABSENT field was not bounded by the daemon, "+
				"and reporting it as bounded would be the daemon claiming credit for claude's silence",
				got.TruncatedFields, got.DroppedFields)
		}
	})
}

// TestParser_PermissionDeniedGatesOnNothingAndDropsUndecodable pins the design
// decision this arm's whole shape rests on, and AC 4's dropcap row depends on the
// first half of it.
//
// NO FIELD GATES THE EMIT, unlike emitModelAnnounced (which emits nothing for a
// model-less init) and emitCompactionBoundary (which emits nothing without
// compact_metadata). Those two gate because the gated field IS the payload and a
// sibling event already reported the fact; here the SUBTYPE is the payload —
// nothing else on this surface distinguishes a denied call from one that ran and
// failed — so a field-less line is still news. A gate would also re-drop the line
// silently the first time claude renames a key, which is the exact defect #2232
// exists to end.
func TestParser_PermissionDeniedGatesOnNothingAndDropsUndecodable(t *testing.T) {
	t.Parallel()

	t.Run("a line with no fields at all still emits", func(t *testing.T) {
		t.Parallel()
		got := oneDenial(t, `{"type":"system","subtype":"permission_denied"}`)
		// Field by field rather than a struct comparison: the two report slices make
		// turnevent.ToolCallDenied incomparable, and nil-versus-empty is exactly what
		// this row must see.
		if got.ToolName != "" || got.ToolCallID != "" || got.Message != "" ||
			got.DecisionReasonType != "" || got.DecisionReason != "" {
			t.Errorf("event = %+v, want every carried field empty — nothing was there to carry", got)
		}
		if got.TruncatedFields != nil || got.DroppedFields != nil {
			t.Errorf("reports = %v/%v, want nil/nil — an absent field is not a bounded one",
				got.TruncatedFields, got.DroppedFields)
		}
	})

	t.Run("claude's own identities reach no field", func(t *testing.T) {
		t.Parallel()
		const sessionID = "6d1c0f7a-12a0-4c0a-9b2e-0d5a7c3f1e42"
		const uuid = "7d31f0bb-482d-45c0-bf8a-2ef2cd2ccfc0"
		got := oneDenial(t, denialLineFixture(t, map[string]any{
			"tool_name": "Bash", "tool_use_id": "toolu_1",
			"session_id": sessionID, "uuid": uuid,
		}))
		// Structural rather than careful: neither key is declared on
		// systemPermissionDeniedLine, so encoding/json discards both. The sweep over
		// the real captured values lives in permission_denial_capture_test.go.
		for _, field := range []string{got.ToolName, got.ToolCallID, got.Message,
			got.DecisionReasonType, got.DecisionReason} {
			if strings.Contains(field, sessionID) || strings.Contains(field, uuid) {
				t.Errorf("field %q carries claude's session_id or uuid; neither is the daemon's "+
					"identity and neither may reach a consumer", field)
			}
		}
	})

	t.Run("an undecodable line consumes and emits nothing", func(t *testing.T) {
		t.Parallel()
		// A numeric tool_name fails the whole decode, which is fail-closed: one absurd
		// field costs the frame rather than producing a half-true one. The line must
		// still be CONSUMED — reaching emitUnrecognized from a system line would break
		// the structural guarantee ignoredLineTypes provides.
		const line = `{"type":"system","subtype":"permission_denied","tool_name":7}`
		if events := collectEvents(line); len(events) != 0 {
			t.Fatalf("collectEvents(%s) returned %d events, want 0", line, len(events))
		}
	})
}

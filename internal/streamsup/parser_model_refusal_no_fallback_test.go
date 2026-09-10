package streamsup

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// EVERY LINE IN THIS FILE IS HAND-BUILT FROM DOCUMENTATION, NOT CAPTURED BY THIS
// REPOSITORY. The keys were read 2026-09-07 from the Claude Code headless and Agent
// SDK documentation and @anthropic-ai/claude-agent-sdk@0.3.263's sdk.d.ts, with the
// daemon on claude 2.1.259. These tests prove what the parser does GIVEN that shape;
// they do not claim which keys a real line carries. Every declared key is optional.

func refusalNoFallbackLineFixture(t *testing.T, fields map[string]any) string {
	t.Helper()
	line := map[string]any{"type": "system", "subtype": "model_refusal_no_fallback"}
	for key, value := range fields {
		line[key] = value
	}
	raw, err := json.Marshal(line)
	if err != nil {
		t.Fatalf("marshalling documentation-derived model_refusal_no_fallback fixture: %v", err)
	}
	return string(raw)
}

func oneRefusalNoFallback(t *testing.T, line string) turnevent.ModelRefusalNoFallback {
	t.Helper()
	events := collectEvents(line)
	if len(events) != 1 {
		t.Fatalf("collectEvents(%s) returned %d events, want exactly 1", line, len(events))
	}
	refusal, ok := events[0].(turnevent.ModelRefusalNoFallback)
	if !ok {
		t.Fatalf("event is %T, want turnevent.ModelRefusalNoFallback", events[0])
	}
	return refusal
}

func TestParser_RefusalNoFallbackCarriesDocumentedFields(t *testing.T) {
	t.Parallel()

	got := oneRefusalNoFallback(t, refusalNoFallbackLineFixture(t, map[string]any{
		"original_model":            "claude-opus-4-1-20250805",
		"api_refusal_category":      "cyber",
		"api_refusal_explanation":   "the request asked for working exploit code",
		"content":                   "Claude declined this request without retrying.",
		"request_id":                "request-secret-sentinel",
		"refused_user_message_uuid": "message-secret-sentinel",
	}))

	want := turnevent.ModelRefusalNoFallback{
		OriginalModel:      "claude-opus-4-1-20250805",
		RefusalCategory:    "cyber",
		RefusalExplanation: "the request asked for working exploit code",
		Banner:             "Claude declined this request without retrying.",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("event = %+v, want %+v", got, want)
	}
	for _, excluded := range []string{"request-secret-sentinel", "message-secret-sentinel"} {
		if strings.Contains(fmt.Sprintf("%+v", got), excluded) {
			t.Errorf("event contains excluded identifier %q; omitted keys must not enter the decode target", excluded)
		}
	}
}

func TestParser_RefusalVariantsRemainDistinct(t *testing.T) {
	t.Parallel()

	without := collectEvents(`{"type":"system","subtype":"model_refusal_no_fallback"}`)
	with := collectEvents(`{"type":"system","subtype":"model_refusal_fallback"}`)
	if len(without) != 1 || len(with) != 1 {
		t.Fatalf("event counts = %d/%d, want 1/1", len(without), len(with))
	}
	if _, ok := without[0].(turnevent.ModelRefusalNoFallback); !ok {
		t.Errorf("no-fallback line emitted %T, want ModelRefusalNoFallback", without[0])
	}
	if _, ok := with[0].(turnevent.ModelRefusalFallback); !ok {
		t.Errorf("fallback line emitted %T, want ModelRefusalFallback", with[0])
	}
}

func TestParser_RefusalNoFallbackMissingFieldsStillEmit(t *testing.T) {
	t.Parallel()

	all := map[string]string{
		"original_model":          "model-value",
		"api_refusal_category":    "category-value",
		"api_refusal_explanation": "explanation-value",
		"content":                 "banner-value",
	}
	for omitted := range all {
		omitted := omitted
		t.Run("without "+omitted, func(t *testing.T) {
			t.Parallel()
			fields := make(map[string]any, len(all)-1)
			for key, value := range all {
				if key != omitted {
					fields[key] = value
				}
			}
			got := oneRefusalNoFallback(t, refusalNoFallbackLineFixture(t, fields))
			if got.TruncatedFields != nil || got.DroppedFields != nil {
				t.Errorf("reports = %v/%v, want nil/nil; an absent field was not bounded",
					got.TruncatedFields, got.DroppedFields)
			}
			wantEmpty := daemonNoFallbackFieldName(omitted)
			if empty := refusalNoFallbackEmptyFields(got); !reflect.DeepEqual(empty, []string{wantEmpty}) {
				t.Errorf("empty fields = %v, want [%s]", empty, wantEmpty)
			}
		})
	}

	t.Run("all fields absent", func(t *testing.T) {
		t.Parallel()
		got := oneRefusalNoFallback(t, `{"type":"system","subtype":"model_refusal_no_fallback"}`)
		if empty := refusalNoFallbackEmptyFields(got); len(empty) != 4 {
			t.Errorf("empty fields = %v, want all four empty", empty)
		}
		if got.TruncatedFields != nil || got.DroppedFields != nil {
			t.Errorf("reports = %v/%v, want nil/nil", got.TruncatedFields, got.DroppedFields)
		}
	})
}

func daemonNoFallbackFieldName(claudeKey string) string {
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

func refusalNoFallbackEmptyFields(got turnevent.ModelRefusalNoFallback) []string {
	var empty []string
	for _, field := range []struct {
		name  string
		value string
	}{
		{"original_model", got.OriginalModel},
		{"refusal_category", got.RefusalCategory},
		{"refusal_explanation", got.RefusalExplanation},
		{"banner", got.Banner},
	} {
		if field.value == "" {
			empty = append(empty, field.name)
		}
	}
	return empty
}

func TestParser_RefusalNoFallbackBoundsEveryClaudeString(t *testing.T) {
	t.Parallel()

	overToken := strings.Repeat("t", maxTaskFieldID+1)
	overProse := strings.Repeat("p", maxDenialProse+1)
	atToken := strings.Repeat("t", maxTaskFieldID)
	atProse := strings.Repeat("p", maxDenialProse)

	tests := []struct {
		name   string
		fields map[string]any
		want   turnevent.ModelRefusalNoFallback
	}{
		{
			name:   "original model token drops",
			fields: map[string]any{"original_model": overToken},
			want:   turnevent.ModelRefusalNoFallback{DroppedFields: []string{"original_model"}},
		},
		{
			name:   "refusal category token drops under daemon name",
			fields: map[string]any{"api_refusal_category": overToken},
			want:   turnevent.ModelRefusalNoFallback{DroppedFields: []string{"refusal_category"}},
		},
		{
			name:   "refusal explanation prose cuts",
			fields: map[string]any{"api_refusal_explanation": overProse},
			want: turnevent.ModelRefusalNoFallback{
				RefusalExplanation: atProse, TruncatedFields: []string{"refusal_explanation"},
			},
		},
		{
			name:   "banner prose cuts under daemon name",
			fields: map[string]any{"content": overProse},
			want: turnevent.ModelRefusalNoFallback{
				Banner: atProse, TruncatedFields: []string{"banner"},
			},
		},
		{
			name: "all fields over cap preserve report order",
			fields: map[string]any{
				"original_model": overToken, "api_refusal_category": overToken,
				"api_refusal_explanation": overProse, "content": overProse,
			},
			want: turnevent.ModelRefusalNoFallback{
				RefusalExplanation: atProse,
				Banner:             atProse,
				TruncatedFields:    []string{"refusal_explanation", "banner"},
				DroppedFields:      []string{"original_model", "refusal_category"},
			},
		},
		{
			name: "all fields at cap remain intact and unreported",
			fields: map[string]any{
				"original_model": atToken, "api_refusal_category": atToken,
				"api_refusal_explanation": atProse, "content": atProse,
			},
			want: turnevent.ModelRefusalNoFallback{
				OriginalModel: atToken, RefusalCategory: atToken,
				RefusalExplanation: atProse, Banner: atProse,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := oneRefusalNoFallback(t, refusalNoFallbackLineFixture(t, tt.fields))
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("event = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParser_RefusalNoFallbackCutRemainsValidUTF8(t *testing.T) {
	t.Parallel()

	prefix := strings.Repeat("u", maxDenialProse-1)
	got := oneRefusalNoFallback(t, refusalNoFallbackLineFixture(t, map[string]any{
		"api_refusal_explanation": prefix + "é",
	}))
	if got.RefusalExplanation != prefix {
		t.Errorf("cut explanation = %q, want partial rune removed", got.RefusalExplanation)
	}
	if !reflect.DeepEqual(got.TruncatedFields, []string{"refusal_explanation"}) {
		t.Errorf("TruncatedFields = %v, want [refusal_explanation]", got.TruncatedFields)
	}
}

const refusalNoFallbackUndecodableMsg = "streamsup: dropping undecodable system line"

func TestParser_RefusalNoFallbackConsumesWrongTypes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		line string
	}{
		{"numeric model", `{"type":"system","subtype":"model_refusal_no_fallback","original_model":7}`},
		{"boolean explanation", `{"type":"system","subtype":"model_refusal_no_fallback","api_refusal_explanation":true}`},
		{"object prose", `{"type":"system","subtype":"model_refusal_no_fallback","content":{"text":"secret"}}`},
		{"array category with other valid fields", `{"type":"system","subtype":"model_refusal_no_fallback","original_model":"opus","api_refusal_category":["cyber"],"content":"secret"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			recorder := &logRecorder{}
			var events []turnevent.Event
			parser := NewParser(func(ev turnevent.Event) { events = append(events, ev) }, slog.New(recorder))
			if _, err := parser.Write([]byte(tt.line + "\n")); err != nil {
				t.Fatalf("writing malformed documentation-derived line: %v", err)
			}
			if len(events) != 0 {
				t.Fatalf("line produced %d events, want 0", len(events))
			}
			records := recorder.withMessage(refusalNoFallbackUndecodableMsg)
			if len(records) != 1 {
				t.Fatalf("matching debug records = %d, want 1; all records: %v", len(records), recorder.all())
			}
			if !reflect.DeepEqual(records[0].attrs, map[string]string{"subtype": "model_refusal_no_fallback"}) {
				t.Errorf("debug attrs = %v, want subtype only", records[0].attrs)
			}
		})
	}
}

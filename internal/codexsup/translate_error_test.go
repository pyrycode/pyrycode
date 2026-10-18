package codexsup

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func testErrorParams(t *testing.T, retry bool, message string, details, info any) json.RawMessage {
	t.Helper()
	p := map[string]any{
		"threadId": "th", "turnId": "t", "willRetry": retry,
		"error": map[string]any{"message": message, "additionalDetails": details, "codexErrorInfo": info},
	}
	s := loadSchema(t)
	sch, err := s.resolve("#/definitions/v2/ErrorNotification")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.validate(sch, wireValue(t, p)); err != nil {
		t.Fatalf("hand-built error does not match schema: %v", err)
	}
	return json.RawMessage(mustJSON(t, p))
}

func TestTranslateErrorFrames(t *testing.T) {
	t.Parallel()
	got := replay(t, "testdata/handbuilt/error_notifications.jsonl", "m")
	want := []turnevent.Event{
		turnevent.ApiRetry{Active: true},
		turnevent.ApiRetry{Active: false},
		turnevent.TextChunk{MessageID: "msg", Text: "resumed"},
		turnevent.Banner{Level: "error", Text: "connection failed\nretry exhausted"},
		turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn, IsError: true, ErrorCategory: "httpConnectionFailed"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %#v, want %#v", got, want)
	}
	if !slices.Contains(mappedMethods, "error") || slices.Contains(ignoredMethods, "error") || slices.Contains(unrecognizedMethods, "error") {
		t.Fatal("error must occupy only mappedMethods")
	}
}

func TestTranslateErrorRetryLifecycle(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, method, params string }{
		{"text", "item/agentMessage/delta", `{"itemId":"msg","delta":"resumed"}`},
		{"reasoning summary", "item/reasoning/summaryTextDelta", `{"itemId":"rs","delta":"thinking"}`},
		{"reasoning", "item/reasoning/textDelta", `{"itemId":"rs","delta":"thinking"}`},
		{"tool start", "item/started", `{"item":{"type":"commandExecution","id":"cmd","command":"pwd","cwd":"/capture/cwd","status":"inProgress"}}`},
		{"tool update", "item/completed", `{"item":{"type":"fileChange","id":"patch","status":"completed","changes":[]}}`},
		{"completed", "turn/completed", `{"turn":{"id":"t","status":"completed"}}`},
		{"interrupted", "turn/completed", `{"turn":{"id":"t","status":"interrupted"}}`},
		{"failed", "turn/completed", `{"turn":{"id":"t","status":"failed","error":{"message":"failed","codexErrorInfo":"other"}}}`},
		{"usage limit", "turn/completed", `{"turn":{"id":"t","status":"failed","error":{"message":"limit","codexErrorInfo":"usageLimitExceeded"}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := NewTranslator("m")
			retry := testErrorParams(t, true, "retrying", nil, "other")
			params := json.RawMessage(tc.params)
			baseline := NewTranslator("m").Translate(tc.method, params)
			for range 2 { // Reuse the turn ID across retry episodes.
				for range 2 {
					if got := tr.Translate("error", retry); !reflect.DeepEqual(got, []turnevent.Event{turnevent.ApiRetry{Active: true}}) {
						t.Fatalf("retry = %#v", got)
					}
				}
				for _, fr := range []frame{
					{Method: "warning", Params: json.RawMessage(`{"message":"notice"}`)},
					{Method: "turn/started", Params: json.RawMessage(`{}`)},
					{Method: "error", Params: testErrorParams(t, false, "failed", nil, "other")},
					{Method: "item/started", Params: json.RawMessage(`{"item":{"type":"contextCompaction"}}`)},
				} {
					if got := tr.Translate(fr.Method, fr.Params); len(only[turnevent.ApiRetry](got)) != 0 {
						t.Fatalf("%s cleared retry: %#v", fr.Method, got)
					}
				}
				want := append([]turnevent.Event{turnevent.ApiRetry{Active: false}}, baseline...)
				if got := tr.Translate(tc.method, params); !reflect.DeepEqual(got, want) {
					t.Fatalf("resumed = %#v, want %#v", got, want)
				}
				if got := tr.Translate(tc.method, params); !reflect.DeepEqual(got, baseline) {
					t.Fatalf("repeated content = %#v, want %#v", got, baseline)
				}
			}
			later := json.RawMessage(`{"threadId":"th","turnId":"later","itemId":"next","delta":"next turn"}`)
			if got := tr.Translate("item/agentMessage/delta", later); !reflect.DeepEqual(got, []turnevent.Event{turnevent.TextChunk{MessageID: "next", Text: "next turn"}}) {
				t.Fatalf("retry leaked into later turn: %#v", got)
			}
			if got := NewTranslator("m").Translate(tc.method, params); !reflect.DeepEqual(got, baseline) {
				t.Fatal("new translator inherited retry state")
			}
		})
	}
}

func TestTranslateErrorBannerBounds(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, message string
		details       any
		want          string
		truncated     bool
	}{
		{"null details", "failed", nil, "failed", false},
		{"empty details", "failed", "", "failed", false},
		{"empty message", "", nil, "", false},
		{"prose", "<b>failed</b>\u001b[31m", "provider details", "<b>failed</b>\u001b[31m\nprovider details", false},
		{"exact limit", strings.Repeat("x", maxBannerText), nil, strings.Repeat("x", maxBannerText), false},
		{"long message", strings.Repeat("x", maxBannerText+1), nil, strings.Repeat("x", maxBannerText), true},
		{"joined limit", "x", strings.Repeat("y", maxBannerText), "x\n" + strings.Repeat("y", maxBannerText-2), true},
		{"multibyte message", strings.Repeat("界", maxBannerText), nil, strings.Repeat("界", maxBannerText/3), true},
		{"multibyte details", strings.Repeat("x", maxBannerText-2), "界", strings.Repeat("x", maxBannerText-2) + "\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			params := testErrorParams(t, false, tc.message, tc.details, nil)
			got := NewTranslator("m").Translate("error", params)
			want := []turnevent.Event{turnevent.Banner{Level: "error", Text: tc.want, Truncated: tc.truncated}}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("incorrect banner: want %d bytes, truncated %v; got %T", len(tc.want), tc.truncated, got)
			}
			if !utf8.ValidString(got[0].(turnevent.Banner).Text) {
				t.Fatal("banner is not valid UTF-8")
			}
		})
	}
	// Both optional fields can be absent, rather than explicitly null.
	got := NewTranslator("").Translate("error", json.RawMessage(`{"threadId":"","turnId":"","willRetry":false,"error":{"message":"failed"}}`))
	if !reflect.DeepEqual(got, []turnevent.Event{turnevent.Banner{Level: "error", Text: "failed"}}) {
		t.Fatalf("absent optional fields = %#v", got)
	}
}

func TestTranslateErrorsPreserveCompletion(t *testing.T) {
	t.Parallel()
	for _, category := range []string{"usageLimitExceeded", "contextWindowExceeded", "other", "httpConnectionFailed"} {
		t.Run(category, func(t *testing.T) {
			tr, control := NewTranslator("m"), NewTranslator("m")
			for _, translator := range []*Translator{tr, control} {
				translator.Translate("model/rerouted", json.RawMessage(`{"turnId":"t","fromModel":"m","toModel":"other-model","reason":"test"}`))
				translator.Translate("thread/tokenUsage/updated", json.RawMessage(`{"turnId":"t","tokenUsage":{"total":{"inputTokens":10,"outputTokens":5},"last":{"inputTokens":10,"outputTokens":5},"modelContextWindow":1000}}`))
			}
			var info any = category
			if category == "httpConnectionFailed" {
				info = map[string]any{category: map[string]any{"httpStatusCode": 502}}
			}
			got := tr.Translate("error", testErrorParams(t, false, "failed", nil, info))
			var want []turnevent.Event
			if category != "usageLimitExceeded" && category != "contextWindowExceeded" {
				want = []turnevent.Event{turnevent.Banner{Level: "error", Text: "failed"}}
			}
			if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(tr, control) {
				t.Fatalf("error mapping changed turn state or wrong banner: %#v", got)
			}
			completed := json.RawMessage(mustJSON(t, map[string]any{"turn": map[string]any{
				"id": "t", "status": "failed", "error": map[string]any{"message": "failed", "codexErrorInfo": info},
			}}))
			if got, want := tr.Translate("turn/completed", completed), control.Translate("turn/completed", completed); !reflect.DeepEqual(got, want) {
				t.Fatalf("completion = %#v, want %#v", got, want)
			}
		})
	}
}

func TestTranslateMalformedErrors(t *testing.T) {
	t.Parallel()
	cases := []string{"", "{", "null", "[]", `["error"]`, `"error"`, "0", "true", "{}"}
	valid := testErrorParams(t, true, "retry", nil, nil)
	for _, field := range []string{"threadId", "turnId", "willRetry", "error", "message", "additionalDetails"} {
		for _, value := range []string{"missing", "null", "42", "[]", "{}", `"wrong"`, "false"} {
			if (field == "threadId" || field == "turnId" || field == "message" || field == "additionalDetails") && value == `"wrong"` || field == "willRetry" && value == "false" || field == "additionalDetails" && (value == "missing" || value == "null") {
				continue
			}
			var p map[string]json.RawMessage
			if err := json.Unmarshal(valid, &p); err != nil {
				t.Fatal(err)
			}
			target := p
			if field == "message" || field == "additionalDetails" {
				target = nil
				if err := json.Unmarshal(p["error"], &target); err != nil {
					t.Fatal(err)
				}
			}
			delete(target, field)
			if value != "missing" {
				target[field] = json.RawMessage(value)
			}
			if field == "message" || field == "additionalDetails" {
				p["error"] = json.RawMessage(mustJSON(t, target))
			}
			cases = append(cases, mustJSON(t, p))
		}
	}
	cases = append(cases, `{"error":42,"extra":"`+strings.Repeat("界", maxUnrecognizedRaw)+`"}`)
	for i, params := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			tr := NewTranslator("m")
			tr.Translate("error", valid)
			got := tr.Translate("error", json.RawMessage(params))
			raw := params
			if len(raw) > maxUnrecognizedRaw {
				raw = strings.ToValidUTF8(raw[:maxUnrecognizedRaw], "")
			}
			want := []turnevent.Event{turnevent.Unrecognized{Site: turnevent.UnrecognizedUndecodable, Raw: raw, Truncated: len(params) > maxUnrecognizedRaw}}
			if !reflect.DeepEqual(got, want) || !utf8.ValidString(raw) {
				t.Fatal("incorrect bounded undecodable fallback")
			}
			got = tr.Translate("item/agentMessage/delta", json.RawMessage(`{"itemId":"msg","delta":"ok"}`))
			if !reflect.DeepEqual(got, []turnevent.Event{turnevent.ApiRetry{Active: false}, turnevent.TextChunk{MessageID: "msg", Text: "ok"}}) {
				t.Fatalf("malformed error changed retry state: %#v", got)
			}
		})
	}
}

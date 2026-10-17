package codexsup

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func TestTranslateWarnings(t *testing.T) {
	t.Parallel()
	s := loadSchema(t)
	for _, tc := range []struct {
		name, method, params, want string
	}{
		{"warning absent", "warning", `{"message":"notice"}`, "notice"},
		{"warning null", "warning", `{"message":"notice","threadId":null}`, "notice"},
		{"warning targeted", "warning", `{"message":"notice","threadId":"other-thread"}`, "notice"},
		{"warning empty", "warning", `{"message":""}`, ""},
		{"config absent", "configWarning", `{"summary":"notice"}`, "notice"},
		{"config null", "configWarning", `{"summary":"notice","details":null,"path":null,"range":null}`, "notice"},
		{"config located", "configWarning", `{"summary":"notice","details":"guidance","path":"/untrusted/config","range":{"start":{"line":1,"column":1},"end":{"line":2,"column":3}}}`, "notice\nguidance"},
		{"config empty details", "configWarning", `{"summary":"notice","details":""}`, "notice"},
		{"config empty summary", "configWarning", `{"summary":"","details":"guidance"}`, "\nguidance"},
		{"config empty", "configWarning", `{"summary":""}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ref := "#/definitions/v2/WarningNotification"
			if tc.method == "configWarning" {
				ref = "#/definitions/v2/ConfigWarningNotification"
			}
			sch, err := s.resolve(ref)
			if err != nil {
				t.Fatal(err)
			}
			var v any
			if err := json.Unmarshal([]byte(tc.params), &v); err != nil {
				t.Fatal(err)
			}
			if err := s.validate(sch, v); err != nil {
				t.Fatalf("example does not match %s: %v", ref, err)
			}
			got := NewTranslator("m").Translate(tc.method, json.RawMessage(tc.params))
			want := []turnevent.Event{turnevent.Banner{Level: "warning", Text: tc.want}}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("got %#v, want %#v", got, want)
			}
		})
	}
}

func TestTranslateWarningBounds(t *testing.T) {
	t.Parallel()
	atLimit := strings.Repeat("x", maxBannerText)
	for _, method := range []string{"warning", "configWarning"} {
		key, schemaName := "message", "WarningNotification"
		if method == "configWarning" {
			key, schemaName = "summary", "ConfigWarningNotification"
		}
		s := loadSchema(t)
		sch, err := s.resolve("#/definitions/v2/" + schemaName)
		if err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			name, prose, details, want string
			truncated                  bool
		}{
			{"at limit", atLimit, "", atLimit, false},
			{"beyond limit", atLimit + "x", "", atLimit, true},
			{"multibyte fits", strings.Repeat("x", maxBannerText-3) + "界", "", strings.Repeat("x", maxBannerText-3) + "界", false},
			{"multibyte cut", strings.Repeat("x", maxBannerText-1) + "界", "", strings.Repeat("x", maxBannerText-1), true},
			{"combined at limit", "notice", strings.Repeat("x", maxBannerText-7), "notice\n" + strings.Repeat("x", maxBannerText-7), false},
			{"combined multibyte cut", "notice", strings.Repeat("x", maxBannerText-8) + "界", "notice\n" + strings.Repeat("x", maxBannerText-8), true},
		} {
			if method == "warning" && tc.details != "" {
				continue
			}
			t.Run(method+"/"+tc.name, func(t *testing.T) {
				params := map[string]string{key: tc.prose}
				if tc.details != "" {
					params["details"] = tc.details
				}
				if err := s.validate(sch, wireValue(t, params)); err != nil {
					t.Fatal(err)
				}
				got := NewTranslator("m").Translate(method, json.RawMessage(mustJSON(t, params)))
				want := []turnevent.Event{turnevent.Banner{Level: "warning", Text: tc.want, Truncated: tc.truncated}}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("banner mismatch: want %d text bytes, truncated %v; got %T", len(tc.want), tc.truncated, got)
				}
				b := got[0].(turnevent.Banner)
				if len(b.Text) > maxBannerText || !utf8.ValidString(b.Text) {
					t.Errorf("invalid bounded text: %d bytes, UTF-8 %v", len(b.Text), utf8.ValidString(b.Text))
				}
			})
		}
	}
}

func TestTranslateMalformedWarnings(t *testing.T) {
	t.Parallel()
	for _, method := range []string{"warning", "configWarning"} {
		key := "message"
		if method == "configWarning" {
			key = "summary"
		}
		cases := []string{"", "{", "null", "[]", `["notice"]`, `"notice"`, "0", "true", "{}"}
		for _, value := range []string{"null", "42", "true", "[]", "{}"} {
			cases = append(cases, `{"`+key+`":`+value+`}`)
			if method == "configWarning" && value != "null" {
				cases = append(cases, `{"summary":"notice","details":`+value+`}`)
			}
		}
		cases = append(cases, `{"`+key+`":42,"extra":"`+strings.Repeat("界", maxUnrecognizedRaw)+`"}`)
		for i, params := range cases {
			got := NewTranslator("m").Translate(method, json.RawMessage(params))
			u, ok := turnevent.Unrecognized{}, false
			if len(got) == 1 {
				u, ok = got[0].(turnevent.Unrecognized)
			}
			if !ok || u.Site != turnevent.UnrecognizedUndecodable || u.Kind != "" {
				t.Errorf("%s case %d: got %#v, want one undecodable", method, i, got)
				continue
			}
			wantRaw := params
			if len(wantRaw) > maxUnrecognizedRaw {
				wantRaw = strings.ToValidUTF8(wantRaw[:maxUnrecognizedRaw], "")
			}
			if u.Raw != wantRaw || u.Truncated != (len(params) > maxUnrecognizedRaw) || !utf8.ValidString(u.Raw) {
				t.Errorf("%s case %d: incorrect bounded fallback", method, i)
			}
		}
	}
}

func TestTranslateWarningsPreserveTurn(t *testing.T) {
	t.Parallel()
	tr, control := NewTranslator("m"), NewTranslator("m")
	for _, translator := range []*Translator{tr, control} {
		translator.Translate("model/rerouted", json.RawMessage(`{"turnId":"t","fromModel":"m","toModel":"other-model","reason":"test"}`))
		translator.Translate("thread/tokenUsage/updated", json.RawMessage(`{"turnId":"t","tokenUsage":{"total":{"inputTokens":10,"outputTokens":5},"last":{"inputTokens":10,"outputTokens":5},"modelContextWindow":1000}}`))
	}
	for range 2 {
		for method, params := range map[string]string{
			"warning":       `{"message":"notice","threadId":"other-thread"}`,
			"configWarning": `{"summary":"notice","path":"/untrusted/config","range":null}`,
		} {
			got := tr.Translate(method, json.RawMessage(params))
			if !reflect.DeepEqual(got, []turnevent.Event{turnevent.Banner{Level: "warning", Text: "notice"}}) {
				t.Errorf("%s: %#v", method, got)
			}
		}
	}
	if !reflect.DeepEqual(tr, control) {
		t.Fatal("warnings changed translator state")
	}
	completed := json.RawMessage(`{"turn":{"id":"t","status":"completed"}}`)
	if got, want := tr.Translate("turn/completed", completed), control.Translate("turn/completed", completed); !reflect.DeepEqual(got, want) {
		t.Errorf("completed turn = %#v, want %#v", got, want)
	}
}

func TestWarningMethodLanes(t *testing.T) {
	t.Parallel()
	for _, method := range []string{"warning", "configWarning"} {
		if !slices.Contains(mappedMethods, method) || slices.Contains(ignoredMethods, method) || slices.Contains(unrecognizedMethods, method) {
			t.Errorf("%s must occupy only mappedMethods", method)
		}
	}
	for _, method := range []string{"futureWarning", "guardianWarning"} {
		params := json.RawMessage(`{"message":"notice"}`)
		got := NewTranslator("m").Translate(method, params)
		want := []turnevent.Event{turnevent.Unrecognized{Site: turnevent.UnrecognizedCodexMethod, Kind: method, Raw: string(params)}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %#v, want %#v", method, got, want)
		}
	}
}

const testUntrustedWarning = "project is marked as untrusted in the effective configuration"

func testWarningParams(summary, details string) json.RawMessage {
	params, _ := json.Marshal(map[string]string{"summary": summary, "details": details})
	return params
}

func testWarningBanner(t *testing.T, tr *Translator, method string, params json.RawMessage, text string) {
	t.Helper()
	bounded, truncated := truncate(text, maxBannerText)
	want := []turnevent.Event{turnevent.Banner{Level: "warning", Text: bounded, Truncated: truncated}}
	if got := tr.Translate(method, params); !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: want one bounded warning banner; got %#v", method, got)
	}
}

func TestTranslateUntrustedWarningLifetime(t *testing.T) {
	t.Parallel()
	tr, control := NewTranslator("m"), NewTranslator("m")
	params := testWarningParams(testUntrustedWarning, "guidance")
	testWarningBanner(t, tr, "configWarning", params, testUntrustedWarning+"\nguidance")
	for range 2 {
		for _, translator := range []*Translator{tr, control} {
			translator.SetModel("next-model")
			translator.Translate("model/rerouted", json.RawMessage(`{"turnId":"t","toModel":"other-model"}`))
			translator.Translate("thread/tokenUsage/updated", json.RawMessage(`{"turnId":"t","tokenUsage":{"total":{"inputTokens":10,"outputTokens":5},"last":{"inputTokens":10,"outputTokens":5},"modelContextWindow":1000}}`))
		}
		if got := tr.Translate("configWarning", params); len(got) != 0 {
			t.Fatalf("repeat during turn = %#v, want no events", got)
		}
		completed := json.RawMessage(`{"turn":{"id":"t","status":"completed"}}`)
		if got, want := tr.Translate("turn/completed", completed), control.Translate("turn/completed", completed); !reflect.DeepEqual(got, want) {
			t.Fatalf("warning changed turn output: got %#v, want %#v", got, want)
		}
		if got := tr.Translate("configWarning", params); len(got) != 0 {
			t.Fatalf("repeat between turns = %#v, want no events", got)
		}
	}
	testWarningBanner(t, NewTranslator("m"), "configWarning", params, testUntrustedWarning+"\nguidance")
}

func TestTranslateUntrustedWarningIdentity(t *testing.T) {
	t.Parallel()
	for _, first := range []string{
		testUntrustedWarning,
		testUntrustedWarning + strings.Repeat("界", maxBannerText),
		strings.Repeat("界", maxBannerText) + testUntrustedWarning,
	} {
		tr := NewTranslator("m")
		params := testWarningParams(first, "guidance")
		testWarningBanner(t, tr, "configWarning", params, first+"\nguidance")
		for range 2 {
			for _, distinct := range []string{first + " changed", first + "界"} {
				testWarningBanner(t, tr, "configWarning", testWarningParams(distinct, "guidance"), distinct+"\nguidance")
			}
			testWarningBanner(t, tr, "configWarning", testWarningParams(first, "different guidance"), first+"\ndifferent guidance")
			// The identity is assembled prose, independent of its field split or metadata.
			repeat := testWarningParams(first+"\nguidance", "")
			if got := tr.Translate("configWarning", repeat); len(got) != 0 {
				t.Fatalf("distinct warning replaced the first identity: %#v", got)
			}
		}
	}
	tr := NewTranslator("m")
	testWarningBanner(t, tr, "configWarning", testWarningParams("notice", testUntrustedWarning), "notice\n"+testUntrustedWarning)
	if got := tr.Translate("configWarning", testWarningParams("notice", testUntrustedWarning)); len(got) != 0 {
		t.Fatalf("matching details repeat = %#v", got)
	}
}

func TestTranslateUntrustedWarningNonmatches(t *testing.T) {
	t.Parallel()
	tr := NewTranslator("m")
	for _, matched := range []bool{false, true} {
		if matched {
			testWarningBanner(t, tr, "configWarning", testWarningParams(testUntrustedWarning, ""), testUntrustedWarning)
		}
		for range 2 {
			for _, text := range []string{
				"unrelated warning",
				strings.Replace(testUntrustedWarning, "is marked", "Is marked", 1),
				strings.Replace(testUntrustedWarning, "untrusted", "un-trusted", 1),
				strings.TrimSuffix(testUntrustedWarning, "n"),
			} {
				testWarningBanner(t, tr, "configWarning", testWarningParams(text, ""), text)
			}
			// Matching metadata is inert and does not make unrelated prose a match.
			metadata, _ := json.Marshal(map[string]any{"summary": "notice", "path": testUntrustedWarning, "range": false, "threadId": []int{1}})
			testWarningBanner(t, tr, "configWarning", metadata, "notice")
			other, _ := json.Marshal(map[string]string{"message": testUntrustedWarning})
			testWarningBanner(t, tr, "warning", other, testUntrustedWarning)
			want := []turnevent.Event{unrecognized(turnevent.UnrecognizedCodexMethod, "futureWarning", other)}
			if got := tr.Translate("futureWarning", other); !reflect.DeepEqual(got, want) {
				t.Fatalf("unknown method handling = %#v, want %#v", got, want)
			}
		}
	}
	params := json.RawMessage(`{"summary":"` + testUntrustedWarning + `","path":"other","threadId":"other","range":42}`)
	if got := tr.Translate("configWarning", params); len(got) != 0 {
		t.Fatalf("nonmatch changed first identity: %#v", got)
	}
}

func TestTranslateUntrustedWarningMalformed(t *testing.T) {
	t.Parallel()
	for _, params := range []string{
		`{"summary":"` + testUntrustedWarning + `","details":42}`,
		`{"summary":"` + testUntrustedWarning + `","details":{}}`,
		`{"summary":"` + testUntrustedWarning + `","details":[]}`,
		`{"summary":"` + testUntrustedWarning + `","details":true}`,
		`{"summary":null,"details":"` + testUntrustedWarning + `"}`,
		`{"details":"` + testUntrustedWarning + `"}`,
		`{"summary":42,"details":"` + testUntrustedWarning + `"}`,
		`{"summary":"` + testUntrustedWarning + `"} trailing`,
		`{"summary":"` + testUntrustedWarning + strings.Repeat("界", maxUnrecognizedRaw) + `","details":42}`,
	} {
		tr := NewTranslator("m")
		for _, afterValid := range []bool{false, true} {
			if afterValid {
				testWarningBanner(t, tr, "configWarning", testWarningParams(testUntrustedWarning, ""), testUntrustedWarning)
			}
			before := *tr
			for range 2 {
				got := tr.Translate("configWarning", json.RawMessage(params))
				want := undecodable(json.RawMessage(params))
				if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(*tr, before) {
					t.Fatal("malformed target changed state or bounded undecodable handling")
				}
			}
		}
		if got := tr.Translate("configWarning", testWarningParams(testUntrustedWarning, "")); len(got) != 0 {
			t.Fatalf("malformed target replaced the valid identity: %#v", got)
		}
	}
}

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
		{"guardian targeted", "guardianWarning", `{"message":"notice\n界\u001b[31m","threadId":"other-thread"}`, "notice\n界\x1b[31m"},
		{"guardian empty", "guardianWarning", `{"message":"","threadId":"other-thread"}`, ""},
		{"deprecation absent", "deprecationNotice", `{"summary":"notice"}`, "notice"},
		{"deprecation null", "deprecationNotice", `{"summary":"notice","details":null}`, "notice"},
		{"deprecation details", "deprecationNotice", `{"summary":"notice","details":"guidance\n界"}`, "notice\nguidance\n界"},
		{"deprecation empty details", "deprecationNotice", `{"summary":"notice","details":""}`, "notice"},
		{"deprecation empty summary", "deprecationNotice", `{"summary":"","details":"guidance"}`, "\nguidance"},
		{"deprecation empty", "deprecationNotice", `{"summary":""}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ref := "#/definitions/v2/WarningNotification"
			switch tc.method {
			case "configWarning":
				ref = "#/definitions/v2/ConfigWarningNotification"
			case "guardianWarning":
				ref = "#/definitions/v2/GuardianWarningNotification"
			case "deprecationNotice":
				ref = "#/definitions/v2/DeprecationNoticeNotification"
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
	for _, method := range []string{"warning", "configWarning", "guardianWarning", "deprecationNotice"} {
		key, schemaName := "message", "WarningNotification"
		switch method {
		case "configWarning":
			key, schemaName = "summary", "ConfigWarningNotification"
		case "guardianWarning":
			schemaName = "GuardianWarningNotification"
		case "deprecationNotice":
			key, schemaName = "summary", "DeprecationNoticeNotification"
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
			if key == "message" && tc.details != "" {
				continue
			}
			t.Run(method+"/"+tc.name, func(t *testing.T) {
				params := map[string]string{key: tc.prose}
				if method == "guardianWarning" {
					params["threadId"] = "other-thread"
				}
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
	for _, method := range []string{"warning", "configWarning", "guardianWarning", "deprecationNotice"} {
		key := "message"
		if method == "configWarning" || method == "deprecationNotice" {
			key = "summary"
		}
		cases := []string{"", "{", "null", "[]", `["notice"]`, `"notice"`, "0", "true", "{}"}
		for _, value := range []string{"null", "42", "true", "[]", "{}"} {
			cases = append(cases, `{"`+key+`":`+value+`}`)
			if key == "summary" && value != "null" {
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
			"warning":           `{"message":"notice","threadId":"other-thread"}`,
			"configWarning":     `{"summary":"notice","path":"/untrusted/config","range":null}`,
			"guardianWarning":   `{"message":"notice","threadId":"other-thread","turnId":"t","stopsTurn":true}`,
			"deprecationNotice": `{"summary":"notice","threadId":"other-thread","turnId":"t","path":"/untrusted/config","stopsTurn":true}`,
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
	for _, method := range []string{"warning", "configWarning", "guardianWarning", "deprecationNotice"} {
		if !slices.Contains(mappedMethods, method) || slices.Contains(ignoredMethods, method) || slices.Contains(unrecognizedMethods, method) {
			t.Errorf("%s must occupy only mappedMethods", method)
		}
	}
	for _, method := range []string{"futureWarning"} {
		params := json.RawMessage(`{"message":"notice"}`)
		got := NewTranslator("m").Translate(method, params)
		want := []turnevent.Event{turnevent.Unrecognized{Site: turnevent.UnrecognizedCodexMethod, Kind: method, Raw: string(params)}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %#v, want %#v", method, got, want)
		}
	}
}

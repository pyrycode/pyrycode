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

func TestTranslateModerationMetadata(t *testing.T) {
	t.Parallel()
	for _, params := range []string{
		`{}`, `{"threadId":"th","turnId":"t"}`, `{"metadata":null}`,
		`{"metadata":{}}`, `{"metadata":[]}`,
		" \n{\"metadata\": \nnull\t} ", "{\"metadata\":{ \n\t }}", "{\"metadata\":[ \r\n\t ]}",
	} {
		t.Run(params, func(t *testing.T) {
			if got := NewTranslator("m").Translate("turn/moderationMetadata", json.RawMessage(params)); len(got) != 0 {
				t.Errorf("empty metadata produced %#v", got)
			}
		})
	}
	for _, metadata := range []string{`""`, `"text"`, `"{}"`, `"[]"`, `false`, `true`, `0`, `1e1000`, `[null]`, `{"value":null}`, `[{ }]`, `{"value":[]}`} {
		t.Run(metadata, func(t *testing.T) {
			params := " \n{ \"threadId\":\"th\", \"metadata\": " + metadata + ", \"turnId\":\"t\" }\t"
			got := NewTranslator("m").Translate("turn/moderationMetadata", json.RawMessage(params))
			want := []turnevent.Event{turnevent.Unrecognized{Site: turnevent.UnrecognizedCodexMethod, Kind: "turn/moderationMetadata", Raw: params}}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("got %#v, want %#v", got, want)
			}
		})
	}
}

func TestTranslateModerationMetadataUndecodable(t *testing.T) {
	t.Parallel()
	for _, params := range []string{"", " ", "{", "null", "[]", `[{}]`, `""`, "false", "0", `{"metadata":}`, `{"metadata":{]}`, `{"metadata":null} trailing`, `{"metadata":[]} {}`} {
		t.Run(params, func(t *testing.T) {
			got := NewTranslator("m").Translate("turn/moderationMetadata", json.RawMessage(params))
			want := []turnevent.Event{turnevent.Unrecognized{Site: turnevent.UnrecognizedUndecodable, Raw: params}}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("got %#v, want %#v", got, want)
			}
		})
	}
}

func TestTranslateModerationMetadataBounds(t *testing.T) {
	t.Parallel()
	const prefix = `{"metadata":"`
	for _, malformed := range []bool{false, true} {
		for _, size := range []int{maxUnrecognizedRaw - 1, maxUnrecognizedRaw, maxUnrecognizedRaw + 1} {
			params := prefix + strings.Repeat("x", size-len(prefix)-2) + `"}`
			site, kind := turnevent.UnrecognizedCodexMethod, "turn/moderationMetadata"
			if malformed {
				params = params[:len(params)-1] + "]"
				site, kind = turnevent.UnrecognizedUndecodable, ""
			}
			got := NewTranslator("m").Translate("turn/moderationMetadata", json.RawMessage(params))
			want := []turnevent.Event{turnevent.Unrecognized{Site: site, Kind: kind, Raw: params[:min(size, maxUnrecognizedRaw)], Truncated: size > maxUnrecognizedRaw}}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("size %d malformed %v: incorrect bounded fallback", size, malformed)
			}
		}
		wantRaw := prefix + strings.Repeat("x", maxUnrecognizedRaw-len(prefix)-1)
		params := wantRaw + "界\"}"
		site, kind := turnevent.UnrecognizedCodexMethod, "turn/moderationMetadata"
		if malformed {
			params += " trailing"
			site, kind = turnevent.UnrecognizedUndecodable, ""
		}
		got := NewTranslator("m").Translate("turn/moderationMetadata", json.RawMessage(params))
		want := []turnevent.Event{turnevent.Unrecognized{Site: site, Kind: kind, Raw: wantRaw, Truncated: true}}
		if !reflect.DeepEqual(got, want) || !utf8.ValidString(got[0].(turnevent.Unrecognized).Raw) {
			t.Errorf("malformed %v: incorrect UTF-8 truncation", malformed)
		}
	}
}

func TestModerationMetadataMethodLanes(t *testing.T) {
	t.Parallel()
	const method = "turn/moderationMetadata"
	if !slices.Contains(mappedMethods, method) || slices.Contains(ignoredMethods, method) || slices.Contains(unrecognizedMethods, method) {
		t.Error("moderation metadata must occupy only mappedMethods")
	}
	params := json.RawMessage(`{"metadata":null}`)
	got := NewTranslator("m").Translate("future/moderationMetadata", params)
	want := []turnevent.Event{turnevent.Unrecognized{Site: turnevent.UnrecognizedCodexMethod, Kind: "future/moderationMetadata", Raw: string(params)}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("unknown method: got %#v, want %#v", got, want)
	}
	frames := readFrames(t, "testdata/handbuilt/moderation_metadata.jsonl")
	if len(frames) != 1 || frames[0].Method != method {
		t.Fatalf("want one moderation metadata frame, got %#v", frames)
	}
	want = []turnevent.Event{turnevent.Unrecognized{Site: turnevent.UnrecognizedCodexMethod, Kind: method, Raw: string(frames[0].Params)}}
	if got := replay(t, "testdata/handbuilt/moderation_metadata.jsonl", "m"); !reflect.DeepEqual(got, want) {
		t.Errorf("nonempty fixture: got %#v, want %#v", got, want)
	}
}

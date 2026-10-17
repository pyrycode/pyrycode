package codexsup

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// These fixtures are schema-derived; they are not live MCP observations.
func TestTranslateMCPHandBuilt(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		fixture, id, input, text string
		status                   turnevent.ToolStatus
	}{
		{"mcp_completed", "mcp-1", `{"query":"hello"}`, "hello world", turnevent.ToolStatusCompleted},
		{"mcp_failed", "mcp-2", `[1,true]`, "permission denied", turnevent.ToolStatusFailed},
		{"mcp_empty", "mcp-3", `null`, "", turnevent.ToolStatusCompleted},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			frames := readFrames(t, filepath.Join("testdata", "handbuilt", tc.fixture+".jsonl"))
			tr := NewTranslator("")
			var content turnevent.ToolContent
			if tc.text != "" {
				content = turnevent.TextContent{Text: tc.text}
			}
			want := []turnevent.Event{
				turnevent.ToolStart{ToolCallID: tc.id, Title: "example/lookup", Kind: turnevent.ToolKindOther, RawInput: json.RawMessage(tc.input)},
				turnevent.ToolUpdate{ToolCallID: tc.id, Status: tc.status, Content: content},
			}
			for range 2 {
				var got []turnevent.Event
				for _, f := range frames {
					got = append(got, tr.Translate(f.Method, f.Params)...)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("events = %#v, want %#v", got, want)
				}
			}
		})
	}
}

func TestTranslateMCPArguments(t *testing.T) {
	t.Parallel()
	for _, input := range []string{
		`{ "command": "$(touch /tmp/untrusted)", "nested": [null,1] }`,
		`[1,"two",false,null]`, `"hello"`, `123456789012345678901234567890`, `1.25`, `true`, `false`, `null`,
		`{"large":"` + strings.Repeat("界", 17000) + `"}`,
	} {
		t.Run(input[:min(len(input), 30)], func(t *testing.T) {
			params := `{"item":{"type":"mcpToolCall","id":"m","server":"s","tool":"t","status":"inProgress","arguments":` + input + `}}`
			got := NewTranslator("").Translate("item/started", json.RawMessage(params))
			want := []turnevent.Event{turnevent.ToolStart{ToolCallID: "m", Title: "s/t", Kind: turnevent.ToolKindOther, RawInput: json.RawMessage(input)}}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("start differs for input of %d bytes", len(input))
			}
		})
	}
}

func TestTranslateMCPResults(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, extra, text string
	}{
		{"mixed", `,"result":{"content":[{"type":"text","text":"first"},null,42,"text",[],true,{"type":"image","data":"not-base64"},{"type":"resource","resource":{"uri":"file:///private/secret"}},{"type":"text","text":4},{"type":"text","text":null},{"type":"text"},{"type":4,"text":"skip"},{"text":"skip"},{"type":"text","text":"last"}],"structuredContent":{"text":"skip"},"_meta":"skip"}`, "firstlast"},
		{"error_precedence", `,"result":{"content":[{"type":"text","text":"hidden"}]},"error":{"message":"failure"}`, "failure"},
		{"empty_error_precedence", `,"result":{"content":[{"type":"text","text":"hidden"}]},"error":{"message":""}`, ""},
		{"null_error", `,"error":null,"result":{"content":[{"type":"text","text":"shown"}]}`, "shown"},
		{"absent", "", ""},
		{"null", `,"result":null`, ""},
		{"empty_array", `,"result":{"content":[]}`, ""},
		{"empty_text", `,"result":{"content":[{"type":"text","text":""}]}`, ""},
		{"non_text", `,"result":{"content":[{},null,"",{"type":"image","text":"skip"}]}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var content turnevent.ToolContent
			if tc.text != "" {
				content = turnevent.TextContent{Text: tc.text}
			}
			for _, status := range []string{"completed", "failed", "inProgress", "future"} {
				wantStatus := turnevent.ToolStatusFailed
				if status == "completed" {
					wantStatus = turnevent.ToolStatusCompleted
				}
				got := NewTranslator("").Translate("item/completed", testMCPParams(status, tc.extra))
				want := []turnevent.Event{turnevent.ToolUpdate{ToolCallID: "m", Status: wantStatus, Content: content}}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("status %s: got %#v, want %#v", status, got, want)
				}
			}
		})
	}
}

func testMCPParams(status, extra string) json.RawMessage {
	return json.RawMessage(`{"item":{"type":"mcpToolCall","id":"m","server":"s","tool":"t","arguments":null,"status":"` + status + `"` + extra + `}}`)
}

func TestTranslateMCPMalformed(t *testing.T) {
	t.Parallel()
	valid := string(testMCPParams("completed", ""))
	params := []string{`{`, `[]`, `{"item":{"type":"mcpToolCall","id":`}
	for _, field := range []string{"id", "server", "tool", "status", "arguments"} {
		var item map[string]json.RawMessage
		if err := json.Unmarshal(testMCPParams("completed", "")[8:len(valid)-1], &item); err != nil {
			t.Fatal(err)
		}
		delete(item, field)
		params = append(params, `{"item":`+mustJSON(t, item)+`}`)
		if field != "arguments" {
			for _, value := range []string{"null", "42", "[]", "{}", "true"} {
				item[field] = json.RawMessage(value)
				params = append(params, `{"item":`+mustJSON(t, item)+`}`)
			}
		}
	}
	for _, extra := range []string{
		`,"error":42`, `,"error":[]`, `,"error":{}`, `,"error":{"message":null}`, `,"error":{"message":42}`,
		`,"result":42`, `,"result":[]`, `,"result":{}`, `,"result":{"content":null}`, `,"result":{"content":{}}`, `,"result":{"content":"text"}`,
	} {
		params = append(params, string(testMCPParams("completed", extra)))
	}
	// Bounded fallback must also survive a cut inside a multibyte character.
	params = append(params, strings.TrimSuffix(valid, "}")+`,"padding":"`+strings.Repeat("界", 6000)+`","item":false}`)
	for _, method := range []string{"item/started", "item/completed"} {
		for i, p := range params {
			got := NewTranslator("").Translate(method, json.RawMessage(p))
			if len(got) != 1 {
				t.Fatalf("%s case %d: %#v", method, i, got)
			}
			u, ok := got[0].(turnevent.Unrecognized)
			if !ok || u.Site != turnevent.UnrecognizedUndecodable || len(u.Raw) > 16<<10 || !utf8.ValidString(u.Raw) || u.Truncated != (len(p) > 16<<10) {
				t.Errorf("%s case %d: invalid fallback %T", method, i, got[0])
			}
		}
	}
}

func TestTranslateMCPBounds(t *testing.T) {
	t.Parallel()
	for _, size := range []int{255, 256, 257, 16383, 16384, 16385} {
		// The final rune crosses each byte boundary when the cap is exceeded.
		value := strings.Repeat("x", size-3) + "界"
		params := strings.Replace(string(testMCPParams("inProgress", "")), `"server":"s","tool":"t"`, `"server":"`+value+`","tool":"`+value+`"`, 1)
		got := NewTranslator("").Translate("item/started", json.RawMessage(params))
		start, ok := got[0].(turnevent.ToolStart)
		wantTitle := value + "/" + value
		if len(wantTitle) > 256 {
			wantTitle = strings.ToValidUTF8(wantTitle[:256], "")
		}
		if len(got) != 1 || !ok || start.Title != wantTitle || !utf8.ValidString(start.Title) {
			t.Fatalf("title for size %d: %#v", size, got)
		}
		for _, extra := range []string{
			`,"error":{"message":` + mustJSON(t, value) + `}`,
			`,"result":{"content":[{"type":"text","text":` + mustJSON(t, value[:1]) + `},{"type":"text","text":` + mustJSON(t, value[1:]) + `}]}`,
		} {
			got = NewTranslator("").Translate("item/completed", testMCPParams("completed", extra))
			wantText := value
			if len(wantText) > 16384 {
				wantText = strings.ToValidUTF8(wantText[:16384], "")
			}
			want := []turnevent.Event{turnevent.ToolUpdate{ToolCallID: "m", Status: turnevent.ToolStatusCompleted, Content: turnevent.TextContent{Text: wantText}}}
			if !reflect.DeepEqual(got, want) || !utf8.ValidString(wantText) {
				t.Fatalf("result for size %d differs", size)
			}
		}
	}
}

func TestMCPTranslationLanes(t *testing.T) {
	t.Parallel()
	const method = "item/mcpToolCall/progress"
	if !slices.Contains(ignoredMethods, method) || slices.Contains(mappedMethods, method) || slices.Contains(unrecognizedMethods, method) {
		t.Fatal("progress must occupy only ignoredMethods")
	}
	if !slices.Contains(mappedItemTypes, "mcpToolCall") || slices.Contains(ignoredItemTypes, "mcpToolCall") || slices.Contains(unrecognizedItemTypes, "mcpToolCall") {
		t.Fatal("MCP calls must occupy only mappedItemTypes")
	}
	for _, p := range []string{`{"itemId":"m","message":"working","threadId":"th","turnId":"t"}`, `{}`} {
		if got := NewTranslator("").Translate(method, json.RawMessage(p)); got != nil {
			t.Errorf("progress emitted %#v", got)
		}
	}
}

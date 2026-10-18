package streamsup

import (
	"encoding/json"
	"testing"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func TestParserChildTextCompletion(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, line string
		complete   bool
	}{
		{"complete child", `{"type":"assistant","parent_tool_use_id":"a","message":{"id":"m","role":"assistant","content":[{"type":"text","text":"finished"}]}}`, true},
		{"main", `{"type":"assistant","message":{"id":"m","role":"assistant","content":[{"type":"text","text":"main"}]}}`, false},
		{"missing identity", `{"type":"assistant","parent_tool_use_id":"a","message":{"role":"assistant","content":[{"type":"text","text":"partial"}]}}`, false},
		{"wrong role", `{"type":"assistant","parent_tool_use_id":"a","message":{"id":"m","role":"user","content":[{"type":"text","text":"partial"}]}}`, false},
		{"blank", `{"type":"assistant","parent_tool_use_id":"a","message":{"id":"m","role":"assistant","content":[{"type":"text","text":" "}]}}`, false},
		{"null parent", `{"type":"assistant","parent_tool_use_id":null,"message":{"id":"m","role":"assistant","content":[{"type":"text","text":"partial"}]}}`, false},
		{"malformed block", `{"type":"assistant","parent_tool_use_id":"a","message":{"id":"m","role":"assistant","content":[{"type":"text","text":2}]}}`, false},
		{"unmatched ending", `{"type":"stream_event","parent_tool_use_id":"a","event":{"type":"message_stop"}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var completed bool
			p := NewParser(func(ev turnevent.Event) {
				if text, ok := ev.(turnevent.TextChunk); ok {
					raw, _ := json.Marshal(text)
					var fields map[string]any
					_ = json.Unmarshal(raw, &fields)
					completed = completed || fields["TextComplete"] == true
				}
			}, discardLogger())
			if _, err := p.Write([]byte(tc.line + "\n")); err != nil {
				t.Fatal(err)
			}
			if completed != tc.complete {
				t.Fatalf("completion=%v, want %v", completed, tc.complete)
			}
		})
	}
}

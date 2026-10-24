package protocol

import (
	"encoding/json"
	"testing"
)

func TestSummaryTaskUnknown(t *testing.T) {
	for _, kind := range []string{"agent", "tool_call"} {
		h := NewThreadSummary(ThreadItem{Kind: kind, Summary: "task guessed", Content: json.RawMessage(`{"task_id":"forged","task_link":{"tool_call_id":"c"}}`)})
		raw, err := json.Marshal(h)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		if _, ok := fields["task_id"]; ok {
			t.Fatal("content acquired association authority", string(raw))
		}
	}
}

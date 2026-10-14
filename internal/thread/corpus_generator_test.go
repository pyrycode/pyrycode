package thread

import (
	"fmt"
	"strings"
	"time"

	"github.com/pyrycode/pyrycode/internal/history"
)

const testCorpusIdentity = "fold-scenarios-v1/100-entries"

// testFoldCorpus repeats 100-entry scenarios. Count truncates the final scenario;
// identities depend only on scenario index, never count. Padding is bounded text
// deltas within each turn, so scaling adds turns rather than larger messages.
func testFoldCorpus(count int) []history.Entry {
	scenario := []struct{ typ, fields string }{
		{"message", `"role":"user","text":"request $"`},
		{"main_turn_opened", ``},
		{"agent_call_observed", `"tool_call_id":"$a","tool":"Agent"`},
		{"tool_use", `"tool_use_id":"$a","name":"Agent","input":{"prompt":"bounded"}`},
		{"tool_use", `"tool_use_id":"$b","name":"Task"`},
		{"assistant_delta", `"parent_tool_use_id":"$a","text":"child \"雪\"\n"`},
		{"tool_use", `"parent_tool_use_id":"$a","tool_use_id":"$nested","name":"Task"`},
		{"assistant_delta", `"parent_tool_use_id":"$nested","text":"nested"`},
		{"tool_use", `"parent_tool_use_id":"$nested","tool_use_id":"$read","name":"Read"`},
		{"assistant_delta", `"parent_tool_use_id":"$nested","text":"unfinished"`},
		{"tool_result", `"tool_use_id":"$a","is_error":false,"result_summary":"launch"`},
		{"background_task_started", `"tool_call_id":"$a","task_id":"$ta"`},
		{"background_task_observed", `"task_id":"$ta"`},
		{"tool_use", `"parent_tool_use_id":"$missing","tool_use_id":"$late","name":"Read"`},
		{"tool_result", `"parent_tool_use_id":"$b","tool_use_id":"$late","is_error":false`},
		{"background_task_linked", `"tool_call_id":"$a","task_id":"$ta"`},
		{"background_task_started", `"tool_call_id":"$b","task_id":"$tb"`},
		{"background_task_outcome", `"task_id":"$ta","status":"completed"`},
		{"background_task_updated", `"task_id":"$ta","status":"completed","summary":"companion"`},
		{"background_task_updated", `"task_id":"$tb","status":"completed"`},
		{"send_accepted", `"device_id":"phone","message_id":"$msg","text":"queued","accepted_at":"2026-01-01T00:00:00Z"`},
		{"message", `"role":"user","text":"delivered"`},
		{"send_delivered", `"accepted_entry_id":@accept,"delivery_entry_id":@delivery,"reason":"delivered"`},
		{"tool_use", `"tool_use_id":"$bash","name":"Bash","input":{"command":"echo bounded","run_in_background":"true"}`},
		{"tool_use", `"tool_use_id":"$local","name":"local_bash"`},
		{"tool_result", `"tool_use_id":"$bash","is_error":false`},
		{"background_task_observed", `"task_id":"$ts"`},
		{"background_task_linked", `"tool_call_id":"$bash","task_id":"$ts"`},
		{"background_task_started", `"tool_call_id":"$local","task_id":"$tl"`},
		{"assistant_delta", `"parent_tool_use_id":"$bash","text":"must stay private"`},
		{"assistant_delta", `"parent_tool_use_id":"$local","text":"also private"`},
		{"background_task_outcome", `"task_id":"$ts","status":"completed"`},
		{"background_task_updated", `"task_id":"$tl","status":"completed"`},
	}
	entries := make([]history.Entry, 0, count)
	for n := 0; len(entries) < count; n++ {
		prefix := fmt.Sprintf("s%08d-", n)
		for offset := 0; offset < 100 && len(entries) < count; offset++ {
			typ, fields := "assistant_delta", `"text":"<&>\\\"雪🙂\n","seq":0,"exact":9007199254740993`
			if offset < len(scenario) {
				typ, fields = scenario[offset].typ, scenario[offset].fields
			}
			if offset == 99 {
				typ, fields = "turn_end", `"stop_reason":"end_turn"`
			}
			fields = strings.NewReplacer("$", prefix, "@accept", fmt.Sprint(n*100+21), "@delivery", fmt.Sprint(n*100+22)).Replace(fields)
			base := fmt.Sprintf(`{"conversation_id":"a","turn_id":%q,"lifetime_id":%q,"occurred_at":"2026-01-01T00:00:00Z"`, prefix+"turn", fmt.Sprintf("11111111-1111-4111-8111-%012d", n+1))
			if fields != "" {
				base += "," + fields
			}
			entries = append(entries, history.Entry{ID: uint64(len(entries) + 1), Type: typ, Payload: []byte(base + "}"), TS: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Session: &history.SessionProvenance{Kind: "claude", SessionID: "corpus"}})
		}
	}
	return entries
}

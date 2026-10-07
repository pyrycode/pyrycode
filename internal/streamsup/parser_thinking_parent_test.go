package streamsup

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func TestParser_ThinkingParentAttribution(t *testing.T) {
	t.Parallel()
	sources := []struct {
		name, body string
		setup      []string
		progress   bool
	}{
		{name: "assistant", body: `"type":"assistant","message":{"id":"thinking","content":[{"type":"thinking","thinking":"private"}]}`},
		{name: "delta", body: `"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"private"}}`, setup: []string{
			`{"type":"stream_event","parent_tool_use_id":"setup-parent","event":{"type":"message_start","message":{"id":"thinking"}}}`,
			`{"type":"stream_event","parent_tool_use_id":"setup-parent","event":{"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}}`,
		}},
		{name: "progress", body: fmt.Sprintf(`"type":"system","subtype":"thinking_tokens","estimated_tokens":123,"estimated_tokens_delta":%d`, minThinkingTokensPerEvent), progress: true},
	}
	atCap := strings.Repeat("p", maxTaskFieldID)
	for _, source := range sources {
		for _, raw := range []string{`"agent"`, `""`, "", `null`, `42`, `true`, `{}`, `[]`, fmt.Sprintf("%q", atCap), fmt.Sprintf("%q", atCap+"p")} {
			t.Run(source.name+"/"+raw, func(t *testing.T) {
				var events []turnevent.Event
				p := NewParser(func(ev turnevent.Event) { events = append(events, ev) }, discardLogger())
				write := func(line string) {
					t.Helper()
					if _, err := p.Write([]byte(line + "\n")); err != nil {
						t.Fatal(err)
					}
				}
				for _, line := range source.setup {
					write(line)
				}
				// Always precede the test line with a valid parent; then return to a missing
				// parent on the same parser to detect attribution retained across lines.
				for i, parent := range []string{`"previous"`, raw, ""} {
					line := "{" + source.body
					if parent != "" {
						line += `,"parent_tool_use_id":` + parent
					}
					write(line + "}")
					if len(events) != i+1 {
						t.Fatalf("event count=%d, want %d", len(events), i+1)
					}
					wantParent := ""
					if err := json.Unmarshal([]byte(parent), &wantParent); err != nil || len(wantParent) > maxTaskFieldID {
						wantParent = ""
					}
					var want turnevent.Event
					if source.progress {
						want = turnevent.ThinkingProgress{ParentToolCallID: wantParent, EstimatedTokens: 123, EstimatedTokensDelta: minThinkingTokensPerEvent}
					} else {
						text := ""
						if source.name == "assistant" {
							text = "private"
						}
						want = turnevent.ThoughtChunk{MessageID: "thinking", Text: text, ParentToolCallID: wantParent}
					}
					if !reflect.DeepEqual(events[i], want) {
						t.Fatalf("event=%#v, want %#v", events[i], want)
					}
				}
			})
		}
	}
}

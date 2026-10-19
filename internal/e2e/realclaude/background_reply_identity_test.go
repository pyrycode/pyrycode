//go:build e2e_realclaude

package realclaude

import (
	"strings"
	"testing"
)

func TestBackgroundReplyIdentity(t *testing.T) {
	for _, name := range []string{"valid", "main first", "missing witness", "wrong operation", "wrong tool", "wrong child", "wrong kind", "foreground", "competing agent", "competing tool"} {
		t.Run(name, func(t *testing.T) {
			frames := []string{
				`{"type":"assistant","message":{"id":"launch","role":"assistant","content":[{"type":"tool_use","id":"agent","name":"Agent","input":{"run_in_background":true}}]}}`,
				`{"type":"assistant","parent_tool_use_id":"agent","message":{"id":"hold","role":"assistant","content":[{"type":"tool_use","id":"bash","name":"Bash","input":{"command":"anything"}}]}}`,
				`{"type":"result","subtype":"success"}`,
			}
			w := &backgroundReplyExecution{Operation: "operation", Tool: "bash"}
			switch name {
			case "main first":
				frames[1], frames[2] = frames[2], frames[1]
			case "missing witness":
				w = nil
			case "wrong operation":
				w.Operation = ""
			case "wrong tool":
				w.Tool = "other"
			case "wrong child":
				frames[1] = strings.ReplaceAll(frames[1], `"parent_tool_use_id":"agent"`, `"parent_tool_use_id":"other"`)
			case "wrong kind":
				frames[1] = strings.ReplaceAll(frames[1], `"Bash"`, `"Read"`)
			case "foreground":
				frames[0] = strings.ReplaceAll(frames[0], "true", "false")
			case "competing agent":
				frames = append(frames, strings.ReplaceAll(frames[0], `"agent"`, `"other"`))
			case "competing tool":
				frames = append(frames, frames[1])
			}
			l, m, _, s, _, p := backgroundReplyBoundary(backgroundReplyTestLines(frames), "token", w)
			want := name == "valid" || name == "main first"
			if got := l == 0 && m > l && s > l && p == "agent"; got != want {
				t.Fatalf("setup=%t want=%t", got, want)
			}
			if want {
				// Neither main success alone nor witness alone can authorize release.
				_, m, _, _, _, _ = backgroundReplyBoundary(backgroundReplyTestLines(frames), "token", nil)
				if m >= 0 {
					t.Fatal("main success bypassed missing witness")
				}
				frames = frames[:len(frames)-1]
				if name == "main first" {
					frames = frames[:1]
				}
				_, m, _, _, _, _ = backgroundReplyBoundary(backgroundReplyTestLines(frames), "token", w)
				if m >= 0 {
					t.Fatal("witness bypassed missing main success")
				}
			}
		})
	}
}

package claudetape

import (
	"encoding/json"
	"testing"
)

func TestBypassSourceEvidence(t *testing.T) {
	frame := func(raw string) Frame { return Frame{Direction: "stdout", Data: json.RawMessage(raw)} }
	init := frame(`{"type":"system","subtype":"init","tools":["AskUserQuestion"],"permissionMode":"bypassPermissions"}`)
	ask := frame(`{"type":"control_request","request_id":"one","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","tool_use_id":"use-one"}}`)
	second := frame(`{"type":"control_request","request_id":"two","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","tool_use_id":"use-two"}}`)
	result := frame(`{"type":"result"}`)
	for _, name := range []string{"valid", "missing-init", "wrong-mode", "missing-tools", "duplicate-request", "missing-tool-id", "ordinary-approval", "one-ask", "missing-result", "stdin-substitute"} {
		t.Run(name, func(t *testing.T) {
			rows := []Frame{init, ask, result, second, result, result}
			switch name {
			case "missing-init":
				rows = rows[1:]
			case "wrong-mode":
				rows[0] = frame(`{"type":"system","subtype":"init","tools":["AskUserQuestion"],"permissionMode":"default"}`)
			case "missing-tools":
				rows[0] = frame(`{"type":"system","subtype":"init","permissionMode":"bypassPermissions"}`)
			case "duplicate-request":
				rows[3] = ask
			case "missing-tool-id":
				rows[1] = frame(`{"type":"control_request","request_id":"one","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion"}}`)
			case "ordinary-approval":
				rows[1] = frame(`{"type":"control_request","request_id":"one","request":{"subtype":"can_use_tool","tool_name":"Bash","tool_use_id":"bash"}}`)
			case "one-ask":
				rows[3] = result
			case "missing-result":
				rows = rows[:5]
			case "stdin-substitute":
				rows[1].Direction = "stdin"
			}
			tape := Tape{Frames: rows}
			if err := tape.RequireBypassQuestions(); (err == nil) != (name == "valid") {
				t.Fatalf("source acceptance=%v", err)
			}
		})
	}
}

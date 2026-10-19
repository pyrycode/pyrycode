package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBackgroundCompletionCorrelation(t *testing.T) {
	const source = `{"execution":{"operation_id":"rig-id-operation","tool_use_id":"rig-id-bash"},"os":"linux","parent":"rig-id-agent","launch":0,"child_start":1,"release_index":3,"child_result":3,"replay":[
	"{\"type\":\"assistant\",\"message\":{\"id\":\"launch\",\"role\":\"assistant\",\"content\":[{\"type\":\"tool_use\",\"id\":\"rig-id-agent\",\"name\":\"Agent\",\"input\":{\"run_in_background\":true}}]}}",
	"{\"type\":\"assistant\",\"parent_tool_use_id\":\"rig-id-agent\",\"message\":{\"id\":\"hold\",\"role\":\"assistant\",\"content\":[{\"type\":\"tool_use\",\"id\":\"rig-id-bash\",\"name\":\"Bash\",\"input\":{\"command\":\"rig-synthetic-operation:rig-id-operation\"}}]}}",
	"{\"type\":\"result\",\"subtype\":\"success\"}",
	"{\"type\":\"user\",\"parent_tool_use_id\":\"rig-id-agent\",\"message\":{\"content\":[{\"type\":\"tool_result\",\"tool_use_id\":\"rig-id-bash\",\"content\":\"rig-release-observed\"}]}}"]}`
	for _, name := range []string{"valid", "missing", "operation", "tool", "child", "foreground", "ambiguous agent", "ambiguous tool", "ambiguous operation", "result", "failed", "source", "early release"} {
		t.Run(name, func(t *testing.T) {
			var r backgroundCompletionRecord
			if err := json.Unmarshal([]byte(source), &r); err != nil {
				t.Fatal(err)
			}
			switch name {
			case "missing":
				r.Execution = nil
			case "operation":
				r.Execution.Operation = "rig-id-other"
			case "tool":
				r.Execution.Tool = "rig-id-other"
			case "child":
				r.Replay[1] = strings.ReplaceAll(r.Replay[1], "rig-id-agent", "rig-id-other")
			case "foreground":
				r.Replay[0] = strings.ReplaceAll(r.Replay[0], "true", "false")
			case "ambiguous agent":
				r.Replay = append(r.Replay, r.Replay[0])
			case "ambiguous tool":
				r.Replay = append(r.Replay, r.Replay[1])
			case "ambiguous operation":
				r.Replay = append(r.Replay, strings.ReplaceAll(r.Replay[1], "rig-id-bash", "rig-id-other"))
			case "result":
				r.Replay[3] = strings.ReplaceAll(r.Replay[3], "rig-id-bash", "rig-id-other")
			case "failed":
				r.Replay[3] = strings.ReplaceAll(r.Replay[3], `"tool_use_id"`, `"is_error":true,"tool_use_id"`)
			case "source":
				r.ChildStart++
			case "early release":
				r.ReleaseIndex = r.ChildStart
			}
			if got := validateBackgroundCompletionCorrelation(&r) == nil; got != (name == "valid") {
				t.Fatalf("accepted=%t", got)
			}
		})
	}
}

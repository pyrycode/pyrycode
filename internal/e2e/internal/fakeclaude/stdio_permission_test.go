package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

const permissionRequestID = "permission-1"

func TestRunStreamJSON_StdioPermissionRequestPreservesConfiguredFields(t *testing.T) {
	config := `{"tool_name":"Bash","input":{"command":"ls","args":["-la"]},` +
		`"tool_use_id":"toolu-1","agent_id":"agent-1",` +
		`"permission_suggestions":[{"type":"addRules","rules":[{"toolName":"Bash"}]}],` +
		`"blocked_path":"/private/tmp/x","decision_reason":{"type":"rule","score":0},` +
		`"decision_reason_type":"rule","matched_ask_rule":{"toolName":"Bash"},` +
		`"classifier_approvable":false,"suppress_always_allow_rule":false,` +
		`"default_to_no":false,"title":"Run command","display_name":"Shell",` +
		`"description":"Run the requested command","requires_user_interaction":false}`
	input := userTurnLine("permission") + "\n" + permissionResponseLine(
		permissionRequestID, "deny", nil) + "\n"

	lines := runStdioPermission(t, config, input)
	if got, want := len(lines), 3; got != want {
		t.Fatalf("output lines = %d, want %d: %q", got, want, lines)
	}
	want := `{"request":{"agent_id":"agent-1","blocked_path":"/private/tmp/x",` +
		`"classifier_approvable":false,"decision_reason":{"type":"rule","score":0},` +
		`"decision_reason_type":"rule","default_to_no":false,` +
		`"description":"Run the requested command","display_name":"Shell",` +
		`"input":{"command":"ls","args":["-la"]},"matched_ask_rule":{"toolName":"Bash"},` +
		`"permission_suggestions":[{"type":"addRules","rules":[{"toolName":"Bash"}]}],` +
		`"requires_user_interaction":false,"subtype":"can_use_tool",` +
		`"suppress_always_allow_rule":false,"title":"Run command","tool_name":"Bash",` +
		`"tool_use_id":"toolu-1"},"request_id":"permission-1","type":"control_request"}`
	if lines[0] != want {
		t.Errorf("request line =\n%s\nwant\n%s", lines[0], want)
	}
	if !strings.HasSuffix(string(runStdioPermissionBytes(t, config, input)), "\n") {
		t.Fatal("output is not newline-terminated")
	}
}

func TestRunStreamJSON_StdioPermissionOmittedAndFalseStayDistinct(t *testing.T) {
	config := `{"tool_name":"Read","classifier_approvable":false}`
	lines := runStdioPermission(t, config, userTurnLine("permission")+"\n")
	if got, want := len(lines), 1; got != want {
		t.Fatalf("output lines = %d, want %d: %q", got, want, lines)
	}
	request := decodePermissionRequest(t, lines[0])
	if raw, ok := request["classifier_approvable"]; !ok || string(raw) != "false" {
		t.Errorf("classifier_approvable = %s, present %v; want present false", raw, ok)
	}
	for _, key := range []string{"default_to_no", "input", "requires_user_interaction", "tool_use_id"} {
		if _, ok := request[key]; ok {
			t.Errorf("request unexpectedly contains omitted key %q", key)
		}
	}
}

func TestRunStreamJSON_StdioPermissionDisabledConfigurationsPreserveOutput(t *testing.T) {
	input := userTurnLine("hello") + "\n"
	baseline := runStdioPermissionBytes(t, "", input)
	for _, tc := range []struct {
		name   string
		config string
	}{
		{"malformed", `{"tool_name":`},
		{"array", `[{"tool_name":"Bash"}]`},
		{"unknown field", `{"tool_name":"Bash","invented":true}`},
		{"caller subtype", `{"subtype":"interrupt","tool_name":"Bash"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := runStdioPermissionBytes(t, tc.config, input)
			if !bytes.Equal(got, baseline) {
				t.Errorf("disabled rider output =\n%s\nwant baseline\n%s", got, baseline)
			}
		})
	}
}

func TestRunStreamJSON_StdioPermissionWaitsForMatchingResponse(t *testing.T) {
	config := `{"tool_name":"Bash","input":{"command":"pwd"},"tool_use_id":"toolu-9"}`
	turn := userTurnLine("permission") + "\n"
	mismatch := permissionResponseLine("someone-elses-request", "allow", json.RawMessage(`{"command":"wrong"}`)) + "\n"

	lines := runStdioPermission(t, config, turn+mismatch)
	if got, want := len(lines), 1; got != want {
		t.Fatalf("before matching response, output lines = %d, want only the ask: %q", got, lines)
	}

	matching := permissionResponseLine(permissionRequestID, "allow", nil) + "\n"
	lines = runStdioPermission(t, config, turn+mismatch+matching)
	if got, want := len(lines), 4; got != want {
		t.Fatalf("after matching response, output lines = %d, want ask + allowed path: %q", got, lines)
	}
	assertPermittedTool(t, lines[1], "Bash", "toolu-9", `{"command":"pwd"}`)
	assertVerdictLine(t, lines[2], approveAllowNeedle)
}

func TestRunStreamJSON_StdioPermissionAsksOncePerTurn(t *testing.T) {
	config := `{"tool_name":"Read","input":{"file_path":"/tmp/example"},"tool_use_id":"toolu-read"}`
	input := userTurnLine("first") + "\n" +
		permissionResponseLine("permission-1", "deny", nil) + "\n" +
		userTurnLine("second") + "\n" +
		permissionResponseLine("permission-2", "deny", nil) + "\n"

	lines := runStdioPermission(t, config, input)
	if got, want := len(lines), 6; got != want {
		t.Fatalf("output lines = %d, want two ask/deny/result groups: %q", got, lines)
	}
	for i, lineIndex := range []int{0, 3} {
		var envelope struct {
			RequestID string `json:"request_id"`
		}
		if err := json.Unmarshal([]byte(lines[lineIndex]), &envelope); err != nil {
			t.Fatalf("decode request %d: %v", i+1, err)
		}
		wantID := []string{"permission-1", "permission-2"}[i]
		if envelope.RequestID != wantID {
			t.Errorf("request %d id = %q, want %q", i+1, envelope.RequestID, wantID)
		}
	}
}

func TestRunStreamJSON_StdioPermissionAllowUsesUpdatedInput(t *testing.T) {
	config := `{"tool_name":"AskUserQuestion","input":{"questions":[{"question":"Old?"}]},` +
		`"tool_use_id":"toolu-question"}`
	updated := json.RawMessage(`{"questions":[{"question":"New?","answers":["yes"]}]}`)
	input := userTurnLine("permission") + "\n" +
		permissionResponseLine(permissionRequestID, "allow", updated) + "\n"

	lines := runStdioPermission(t, config, input)
	if got, want := len(lines), 4; got != want {
		t.Fatalf("output lines = %d, want ask + allowed path: %q", got, lines)
	}
	assertPermittedTool(t, lines[1], "AskUserQuestion", "toolu-question", string(updated))
	assertVerdictLine(t, lines[2], approveAllowNeedle)
	assertSuccessResult(t, lines[3])
}

func TestRunStreamJSON_StdioPermissionDenyHasNoPermittedTool(t *testing.T) {
	config := `{"tool_name":"Bash","input":{"command":"rm nothing"},"tool_use_id":"toolu-deny"}`
	input := userTurnLine("permission") + "\n" +
		permissionResponseLine(permissionRequestID, "deny", nil) + "\n"

	lines := runStdioPermission(t, config, input)
	if got, want := len(lines), 3; got != want {
		t.Fatalf("output lines = %d, want ask + denied path: %q", got, lines)
	}
	assertVerdictLine(t, lines[1], approveDenyNeedle)
	assertSuccessResult(t, lines[2])
	for _, line := range lines[1:] {
		var envelope struct {
			Type    string `json:"type"`
			Message struct {
				Content []struct {
					Type string `json:"type"`
				} `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal([]byte(line), &envelope); err != nil {
			t.Fatalf("decode terminal line: %v", err)
		}
		for _, block := range envelope.Message.Content {
			if block.Type == "tool_use" {
				t.Fatalf("denied path reported a permitted tool: %s", line)
			}
		}
	}
}

func runStdioPermission(t *testing.T, config, input string) []string {
	t.Helper()
	raw := runStdioPermissionBytes(t, config, input)
	trimmed := strings.TrimSuffix(string(raw), "\n")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

func runStdioPermissionBytes(t *testing.T, config, input string) []byte {
	t.Helper()
	t.Setenv(envStreamCanUseTool, config)
	var out bytes.Buffer
	runStreamJSON(strings.NewReader(input), &out, false, false, "", false, 0, false, "", false)
	return out.Bytes()
}

func permissionResponseLine(requestID, behavior string, updatedInput json.RawMessage) string {
	response := map[string]any{"behavior": behavior}
	if updatedInput != nil {
		response["updatedInput"] = updatedInput
	}
	b, err := json.Marshal(map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": requestID,
			"response":   response,
		},
	})
	if err != nil {
		panic(err)
	}
	return string(b)
}

func decodePermissionRequest(t *testing.T, line string) map[string]json.RawMessage {
	t.Helper()
	var envelope struct {
		Type      string                     `json:"type"`
		RequestID string                     `json:"request_id"`
		Request   map[string]json.RawMessage `json:"request"`
	}
	if err := json.Unmarshal([]byte(line), &envelope); err != nil {
		t.Fatalf("decode permission request: %v", err)
	}
	if envelope.Type != "control_request" || envelope.RequestID == "" {
		t.Fatalf("request envelope = type %q id %q, want control_request with non-empty id",
			envelope.Type, envelope.RequestID)
	}
	if got := string(envelope.Request["subtype"]); got != `"can_use_tool"` {
		t.Fatalf("request subtype = %s, want can_use_tool", got)
	}
	return envelope.Request
}

func assertPermittedTool(t *testing.T, line, wantName, wantID, wantInput string) {
	t.Helper()
	var envelope outAssistantToolUse
	if err := json.Unmarshal([]byte(line), &envelope); err != nil {
		t.Fatalf("decode permitted tool line: %v", err)
	}
	if envelope.Type != "assistant" || len(envelope.Message.Content) != 1 {
		t.Fatalf("permitted tool envelope = %+v, want one assistant block", envelope)
	}
	block := envelope.Message.Content[0]
	if block.Type != "tool_use" || block.Name != wantName || block.ID != wantID {
		t.Errorf("permitted tool = type %q name %q id %q, want tool_use/%q/%q",
			block.Type, block.Name, block.ID, wantName, wantID)
	}
	if got := string(block.Input); got != wantInput {
		t.Errorf("permitted input = %s, want %s", got, wantInput)
	}
}

func assertVerdictLine(t *testing.T, line, want string) {
	t.Helper()
	var envelope outAssistant
	if err := json.Unmarshal([]byte(line), &envelope); err != nil {
		t.Fatalf("decode verdict line: %v", err)
	}
	if len(envelope.Message.Content) != 1 || envelope.Message.Content[0].Text != want {
		t.Errorf("verdict line = %s, want text %q", line, want)
	}
}

func assertSuccessResult(t *testing.T, line string) {
	t.Helper()
	var result outResult
	if err := json.Unmarshal([]byte(line), &result); err != nil {
		t.Fatalf("decode result line: %v", err)
	}
	if result.Type != "result" || result.Subtype != "success" {
		t.Errorf("result = %+v, want result/success", result)
	}
}

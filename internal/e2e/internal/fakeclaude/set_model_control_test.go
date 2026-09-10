package main

import (
	"fmt"
	"strings"
	"testing"
)

func setModelRequestLine(requestID, model string) string {
	return fmt.Sprintf(`{"type":"control_request","request_id":%q,`+
		`"request":{"subtype":"set_model","model":%q}}`, requestID, model)
}

func TestRunStreamJSON_SetModelAnswer(t *testing.T) {
	t.Parallel()

	for _, honorInterrupt := range []bool{false, true} {
		honorInterrupt := honorInterrupt
		t.Run(fmt.Sprintf("honor_interrupt_%t", honorInterrupt), func(t *testing.T) {
			t.Parallel()
			const requestID = "e2e-2280-set-model"
			const model = "sonnet"
			lines := answerSetPermissionMode(t, setModelRequestLine(requestID, model), honorInterrupt, false)
			if len(lines) != 1 {
				t.Fatalf("set_model answer line count = %d, want 1: %s", len(lines), strings.Join(lines, "\n"))
			}
			const want = `{"response":{"request_id":"e2e-2280-set-model","subtype":"success"},"type":"control_response"}`
			if lines[0] != want {
				t.Errorf("set_model answer = %s, want %s", lines[0], want)
			}
		})
	}
}

func TestRunStreamJSON_SetModelValuesCannotForgeALine(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		requestID string
		model     string
	}{
		{name: "request id", requestID: "id\n{\"type\":\"result\"}", model: "sonnet"},
		{name: "model", requestID: "id", model: "sonnet\n{\"type\":\"result\"}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			lines := answerSetPermissionMode(t, setModelRequestLine(tc.requestID, tc.model), false, false)
			if len(lines) != 1 {
				t.Fatalf("set_model answer line count = %d, want 1: %s", len(lines), strings.Join(lines, "\n"))
			}
			if !strings.Contains(lines[0], `"subtype":"success"`) {
				t.Errorf("set_model answer is not a success response: %s", lines[0])
			}
		})
	}
}

func TestRunStreamJSONApprove_AnswersSetModel(t *testing.T) {
	t.Parallel()

	var buf strings.Builder
	runStreamJSONApprove(strings.NewReader(setModelRequestLine("approve-id", "sonnet")+"\n"), &buf, "")
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("approve-path set_model answer line count = %d, want 1: %s", len(lines), strings.Join(lines, "\n"))
	}
	const want = `{"response":{"request_id":"approve-id","subtype":"success"},"type":"control_response"}`
	if lines[0] != want {
		t.Errorf("approve-path set_model answer = %s, want %s", lines[0], want)
	}
}

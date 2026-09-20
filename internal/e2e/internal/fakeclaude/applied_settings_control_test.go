package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func appliedSettingsRequestLine(requestID string) string {
	return fmt.Sprintf(`{"type":"control_request","request_id":%q,"request":{"subtype":"get_settings"}}`, requestID)
}

type appliedSettingsAck struct {
	Type     string `json:"type"`
	Response struct {
		Subtype   string `json:"subtype"`
		RequestID string `json:"request_id"`
		Response  struct {
			Applied struct {
				Model  string  `json:"model"`
				Effort *string `json:"effort"`
			} `json:"applied"`
		} `json:"response"`
	} `json:"response"`
}

func TestRunStreamJSON_AppliedSettingsAnswer(t *testing.T) {
	t.Parallel()
	const (
		wantModel  = "claude-fake-applied"
		wantEffort = "high"
	)

	for _, honorInterrupt := range []bool{false, true} {
		honorInterrupt := honorInterrupt
		t.Run(fmt.Sprintf("honor interrupt %v", honorInterrupt), func(t *testing.T) {
			t.Parallel()

			requestID := fmt.Sprintf("e2e-applied-settings-%v", honorInterrupt)
			var output bytes.Buffer
			runStreamJSON(strings.NewReader(appliedSettingsRequestLine(requestID)+"\n"), &output,
				honorInterrupt, false, "", false, 0, false, "", false)

			line := strings.TrimSpace(output.String())
			if line == "" {
				t.Fatal("get_settings went unanswered")
			}
			if strings.Contains(line, "\n") {
				t.Fatalf("get_settings produced more than one response line: %s", line)
			}

			var ack appliedSettingsAck
			if err := json.Unmarshal([]byte(line), &ack); err != nil {
				t.Fatalf("decode get_settings response: %v\n%s", err, line)
			}
			if ack.Type != "control_response" || ack.Response.Subtype != "success" {
				t.Errorf("response envelope = (%q, %q), want (control_response, success)",
					ack.Type, ack.Response.Subtype)
			}
			if ack.Response.RequestID != requestID {
				t.Errorf("response request_id = %q, want %q", ack.Response.RequestID, requestID)
			}
			if ack.Response.Response.Applied.Model != wantModel {
				t.Errorf("applied model = %q, want %q", ack.Response.Response.Applied.Model, wantModel)
			}
			if effort := ack.Response.Response.Applied.Effort; effort == nil || *effort != wantEffort {
				t.Errorf("applied effort = %v, want %q", effort, wantEffort)
			}
		})
	}
}

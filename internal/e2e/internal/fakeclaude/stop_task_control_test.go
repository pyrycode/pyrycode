package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestRunStreamJSONStopTask(t *testing.T) {
	for _, known := range []bool{false, true} {
		t.Run(fmt.Sprint(known), func(t *testing.T) {
			task := "unknown"
			if known {
				task = rosterCapturedTaskID
			}
			input := `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hold"}]}}` + "\n" +
				fmt.Sprintf(`{"type":"control_request","request_id":"stop-id","request":{"subtype":"stop_task","task_id":%q}}`, task) + "\n" +
				fmt.Sprintf(`{"type":"control_request","request_id":"stop-again","request":{"subtype":"stop_task","task_id":%q}}`, task) + "\n"
			var out bytes.Buffer
			runStreamJSONConfigured(strings.NewReader(input), &out, streamRunConfig{rosterTasks: 1, honorInterrupt: true})
			responses := 0
			stopped := 0
			removed := false
			for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
				var frame struct {
					Type, Subtype, TaskID, Status string
					Tasks                         []json.RawMessage
					Response                      struct {
						RequestID string `json:"request_id"`
						Subtype   string
						Response  map[string]any
					}
				}
				// Explicit snake-case task id is intentionally independent from the response's id.
				var raw map[string]json.RawMessage
				if err := json.Unmarshal([]byte(line), &frame); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal([]byte(line), &raw); err != nil {
					t.Fatal(err)
				}
				if frame.Type == "control_response" {
					responses++
					want := "error"
					if known && frame.Response.RequestID == "stop-id" {
						want = "success"
					}
					if frame.Response.Subtype != want {
						t.Fatalf("response = %s, want %s", line, want)
					}
					if want == "success" && len(frame.Response.Response) != 0 {
						t.Fatal("success payload not empty")
					}
				}
				if frame.Subtype == "task_notification" {
					var id string
					if err := json.Unmarshal(raw["task_id"], &id); err != nil {
						t.Fatal(err)
					}
					if id != task || frame.Status != "stopped" || responses != 1 {
						t.Fatal("wrong or premature completion")
					}
					stopped++
				}
				if frame.Subtype == "background_tasks_changed" && len(frame.Tasks) == 0 {
					removed = true
				}
			}
			if responses != 2 || stopped != map[bool]int{true: 1, false: 0}[known] || removed != known {
				t.Fatalf("responses/stopped/removed = %d/%d/%t", responses, stopped, removed)
			}
		})
	}
}

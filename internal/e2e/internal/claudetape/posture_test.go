package claudetape

import (
	"encoding/json"
	"testing"
)

func TestModeBeforeUserEvidence(t *testing.T) {
	f := func(dir, raw string) Frame { return Frame{Direction: dir, Data: json.RawMessage(raw)} }
	req := f("stdin", `{"type":"control_request","request_id":"mode","request":{"subtype":"set_permission_mode","mode":"default"}}`)
	ack := f("stdout", `{"type":"control_response","response":{"subtype":"success","request_id":"mode","response":{"mode":"default"}}}`)
	user := f("stdin", `{"type":"user"}`)
	for _, name := range []string{"valid", "missing-ack", "uncorrelated-ack", "nak", "wrong-mode", "late-ack", "new-mode-pending", "stale-mode-ack", "missing-user", "duplicate-request", "initialize-substitute"} {
		t.Run(name, func(t *testing.T) {
			rows := []Frame{req, ack, user}
			switch name {
			case "missing-ack":
				rows = []Frame{req, user}
			case "uncorrelated-ack":
				rows[1] = f("stdout", `{"type":"control_response","response":{"subtype":"success","request_id":"other","response":{"mode":"default"}}}`)
			case "nak":
				rows[1] = f("stdout", `{"type":"control_response","response":{"subtype":"error","request_id":"mode","response":{"mode":"default"}}}`)
			case "wrong-mode":
				rows[1] = f("stdout", `{"type":"control_response","response":{"subtype":"success","request_id":"mode","response":{"mode":"bypassPermissions"}}}`)
			case "late-ack":
				rows = []Frame{req, user, ack}
			case "new-mode-pending", "stale-mode-ack":
				next := f("stdin", `{"type":"control_request","request_id":"next","request":{"subtype":"set_permission_mode","mode":"bypassPermissions"}}`)
				rows = []Frame{req, ack, next, user}
				if name == "stale-mode-ack" {
					rows = []Frame{req, ack, next, ack, user}
				}
			case "missing-user":
				rows = rows[:2]
			case "duplicate-request":
				rows = []Frame{req, req, ack, user}
			case "initialize-substitute":
				rows[0] = f("stdin", `{"type":"control_request","request_id":"mode","request":{"subtype":"initialize"}}`)
			}
			tape := Tape{Frames: rows}
			if err := tape.RequireModeBeforeUser("default", 1); (err == nil) != (name == "valid") {
				t.Fatalf("native posture acceptance=%v", err)
			}
		})
	}
	tape := Tape{Frames: []Frame{user, req, ack, user}}
	if err := tape.RequireModeBeforeUser("default", 2); err != nil {
		t.Fatal("second turn checkpoint rejected")
	}
	if err := tape.RequireModeBeforeUser("", 1); err == nil {
		t.Fatal("empty posture accepted")
	}
	if err := tape.RequireModeBeforeUser("default", 0); err == nil {
		t.Fatal("zero user ordinal accepted")
	}
}

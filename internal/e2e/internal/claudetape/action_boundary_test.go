package claudetape

import (
	"encoding/json"
	"strings"
	"testing"
)

func checkpointFrame(direction, raw string) Frame {
	return Frame{Direction: direction, Data: json.RawMessage(raw)}
}
func sourceUserFrame(text string) Frame {
	raw, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []any{map[string]string{"type": "text", "text": text}}}})
	return Frame{Direction: "stdin", Data: raw}
}
func queueBoundaryRig() *Tape {
	c, _ := json.Marshal(ClientCheckpoint{Type: ClientCheckpointDirection, Sequence: 1, Action: "queue_admitted", ConversationID: "conv", MessageID: "ordinary-queued", QueuedMsgID: 7, Text: "queued"})
	return &Tape{Version: 2, Scenario: "ordinary-queue-placement", Frames: []Frame{
		sourceUserFrame("trigger"),
		checkpointFrame("stdout", `{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"tool_use","name":"Bash","id":"use"}}}`),
		{Direction: ClientCheckpointDirection, Data: c},
		checkpointFrame("stdout", `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"use"}]}}`),
		checkpointFrame("stdout", `{"type":"result"}`), sourceUserFrame("queued"), checkpointFrame("stdout", `{"type":"result"}`),
	}}
}
func TestQueueAdmissionBoundary(t *testing.T) {
	for _, name := range []string{"valid", "full assistant valid", "early action", "early tool", "early delivery", "late action", "missing action", "wrong result", "wrong conversation", "wrong queue id", "wrong text", "wrong message", "output input", "untyped input", "duplicate queued", "duplicate action", "extra terminal", "incomplete tail", "wrong trigger", "unknown action field"} {
		t.Run(name, func(t *testing.T) {
			tape := queueBoundaryRig()
			f := tape.Frames
			switch name {
			case "full assistant valid":
				f[1] = checkpointFrame("stdout", `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","id":"use"}]}}`)
			case "early action":
				f[1], f[2] = f[2], f[1]
			case "early tool":
				f[0], f[1] = f[1], f[0]
			case "early delivery":
				f[4], f[5] = f[5], f[4]
			case "late action":
				f[2], f[3] = f[3], f[2]
			case "missing action":
				f = append(f[:2], f[3:]...)
			case "wrong result":
				f[3].Data = []byte(strings.Replace(string(f[3].Data), `"tool_use_id":"use"`, `"tool_use_id":"foreign"`, 1))
			case "wrong conversation":
				f[2].Data = []byte(strings.Replace(string(f[2].Data), `"conversation_id":"conv"`, `"conversation_id":"other"`, 1))
			case "wrong queue id":
				f[2].Data = []byte(strings.Replace(string(f[2].Data), `"queued_msg_id":7`, `"queued_msg_id":0`, 1))
			case "wrong text":
				f[2].Data = []byte(strings.Replace(string(f[2].Data), `"text":"queued"`, `"text":"changed"`, 1))
			case "wrong message":
				f[2].Data = []byte(strings.Replace(string(f[2].Data), `"message_id":"ordinary-queued"`, `"message_id":"foreign"`, 1))
			case "output input":
				f[5].Direction = "stdout"
			case "untyped input":
				f[5].Data = []byte(strings.Replace(string(f[5].Data), `"role":"user"`, `"role":"assistant"`, 1))
			case "duplicate queued":
				f = append(f, sourceUserFrame("queued"))
			case "duplicate action":
				f = append(f, f[2])
			case "extra terminal":
				f = append(f, checkpointFrame("stdout", `{"type":"result"}`))
			case "incomplete tail":
				f = f[:len(f)-1]
			case "wrong trigger":
				f[0] = sourceUserFrame("other")
			case "unknown action field":
				f[2].Data = []byte(strings.Replace(string(f[2].Data), `"sequence":1`, `"sequence":1,"extra":false`, 1))
			}
			tape.Frames = f
			err := tape.RequireQueueAdmission("conv", "trigger", "queued")
			valid := name == "valid" || name == "full assistant valid"
			if (err == nil) != valid {
				t.Fatalf("queue boundary acceptance err=%v", err)
			}
		})
	}
}
func interruptBoundaryRig(thinking bool) *Tape {
	profile, phase := "interrupt-running", "responding"
	if thinking {
		profile, phase = "interrupt-thinking", "thinking"
	}
	c, _ := json.Marshal(ClientCheckpoint{Type: ClientCheckpointDirection, Sequence: 1, Action: "interrupt_phase", ConversationID: "conv", Phase: phase})
	f := []Frame{sourceUserFrame("trigger"), {Direction: ClientCheckpointDirection, Data: c}, checkpointFrame("stdin", `{"type":"control_request","request_id":"cancel","request":{"subtype":"interrupt"}}`), checkpointFrame("stdout", `{"type":"result","subtype":"error_during_execution"}`)}
	if !thinking {
		f = append(f, sourceUserFrame("health"), checkpointFrame("stdout", `{"type":"result","subtype":"success"}`))
	}
	return &Tape{Version: 2, Scenario: profile, Frames: f}
}
func TestInterruptBoundaryEvidence(t *testing.T) {
	for _, thinking := range []bool{false, true} {
		for _, name := range []string{"valid", "early interrupt", "early result", "wrong subtype", "output interrupt", "empty request id", "duplicate interrupt", "missing action", "foreign action", "wrong phase", "wrong trigger", "health contract"} {
			t.Run(name+map[bool]string{false: "-running", true: "-thinking"}[thinking], func(t *testing.T) {
				tape := interruptBoundaryRig(thinking)
				f := tape.Frames
				switch name {
				case "early interrupt":
					f[1], f[2] = f[2], f[1]
				case "early result":
					f[2], f[3] = f[3], f[2]
				case "wrong subtype":
					f[3].Data = []byte(`{"type":"result","subtype":"success"}`)
				case "output interrupt":
					f[2].Direction = "stdout"
				case "empty request id":
					f[2].Data = []byte(`{"type":"control_request","request_id":"","request":{"subtype":"interrupt"}}`)
				case "duplicate interrupt":
					f = append(f, f[2])
				case "missing action":
					f = append(f[:1], f[2:]...)
				case "foreign action":
					f[1].Data = []byte(strings.Replace(string(f[1].Data), `"conversation_id":"conv"`, `"conversation_id":"other"`, 1))
				case "wrong phase":
					f[1].Data = []byte(strings.Replace(string(f[1].Data), `"phase":`, `"unexpected_phase":`, 1))
				case "wrong trigger":
					f[0] = sourceUserFrame("other")
				case "health contract":
					if thinking {
						f = append(f, sourceUserFrame("health"))
					} else {
						f = f[:len(f)-1]
					}
				}
				tape.Frames = f
				err := tape.RequireInterruptBoundary("conv", "trigger", "health", thinking)
				if (err == nil) != (name == "valid") {
					t.Fatalf("interrupt boundary acceptance err=%v", err)
				}
			})
		}
	}
}

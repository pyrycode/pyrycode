package claudetape

import (
	"encoding/json"
	"strings"
	"testing"
)

func actionRig() (*Tape, []byte) {
	tape := rigTape()
	tape.Version = 2
	tape.Scenario = "ordinary-queue-placement"
	raw := []byte(`{"type":"client_checkpoint","sequence":1,"action":"queue_admitted","conversation_id":"old","message_id":"queued-message","queued_msg_id":9,"text":"queued"}`)
	tape.Frames = []Frame{tape.Frames[0], tape.Frames[1], {Direction: ClientCheckpointDirection, Data: append([]byte(nil), raw...)}, {Direction: "stdout", Data: json.RawMessage(`{"type":"result"}`)}}
	return tape, raw
}
func TestClientCheckpointCausalReplay(t *testing.T) {
	tape, action := actionRig()
	p, err := New(tape, tape.Args)
	if err != nil {
		t.Fatal(err)
	}
	out, err := p.Input([]byte(`{"type":"control_request","request_id":"fresh","request":{"subtype":"initialize"}}`))
	if err != nil || len(out) != 1 || len(p.Prefix()) != 0 || p.Complete() == nil {
		t.Fatal("action did not hold tail")
	}
	out, err = p.Action(action)
	if err != nil || len(out) != 1 || p.Complete() != nil {
		t.Fatal("matching action did not release exact tail")
	}
	if _, err = p.Action(action); err == nil || p.Complete() == nil {
		t.Fatal("extra action accepted after seal")
	}
}
func TestClientCheckpointRejections(t *testing.T) {
	for _, tc := range []struct{ name, old, new string }{
		{"conversation", "\"conversation_id\":\"old\"", "\"conversation_id\":\"fresh\""},
		{"message", "queued-message", "foreign"},
		{"queue id", "\"queued_msg_id\":9", "\"queued_msg_id\":10"},
		{"text", "\"text\":\"queued\"", "\"text\":\"changed\""},
		{"sequence", "\"sequence\":1", "\"sequence\":2"},
		{"unknown field", "\"sequence\":1", "\"extra\":false,\"sequence\":1"},
		{"missing field", "\"message_id\":\"queued-message\",", ""},
		{"duplicate field", "\"sequence\":1", "\"sequence\":2,\"sequence\":1"},
		{"null required", "\"text\":\"queued\"", "\"text\":null"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tape, raw := actionRig()
			p, err := New(tape, tape.Args)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = p.Input(tape.Frames[0].Data); err != nil {
				t.Fatal(err)
			}
			changed := []byte(strings.Replace(string(raw), tc.old, tc.new, 1))
			if _, err = p.Action(changed); err == nil || p.Complete() == nil {
				t.Fatal("changed action accepted")
			}
			if _, err = p.Action(raw); err == nil {
				t.Fatal("action rejection was not latched")
			}
		})
	}
}
func TestClientCheckpointDirectionAndVersions(t *testing.T) {
	tape, action := actionRig()
	p, err := New(tape, tape.Args)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Action(action); err == nil {
		t.Fatal("action accepted before preceding stdin")
	}
	p, err = New(tape, tape.Args)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Input(tape.Frames[0].Data); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Input(action); err == nil {
		t.Fatal("stdin substituted for client action")
	}
	for _, name := range []string{"v1", "unsupported profile", "no action", "second action", "wrong action", "wrong phase", "missing action tail"} {
		t.Run(name, func(t *testing.T) {
			tape, _ := actionRig()
			switch name {
			case "v1":
				tape.Version = 1
			case "unsupported profile":
				tape.Scenario = "reply"
			case "no action":
				tape.Frames = append(tape.Frames[:2], tape.Frames[3:]...)
			case "second action":
				raw := strings.Replace(string(action), `"sequence":1`, `"sequence":2`, 1)
				tape.Frames = append(tape.Frames, Frame{Direction: ClientCheckpointDirection, Data: []byte(raw)})
			case "wrong action":
				tape.Scenario = "interrupt-thinking"
			case "wrong phase":
				tape.Scenario = "interrupt-thinking"
				tape.Frames[2].Data = []byte(`{"type":"client_checkpoint","sequence":1,"action":"interrupt_phase","conversation_id":"conv","phase":"responding"}`)
			case "missing action tail":
				tape.Frames[2].Data = action[:len(action)-1]
			}
			if tape.Validate() == nil {
				t.Fatal("invalid checkpoint format accepted")
			}
		})
	}
	if _, err = DecodeClientCheckpoint(append(action, []byte(` {}`)...)); err == nil {
		t.Fatal("trailing action accepted")
	}
	if _, err = DecodeClientCheckpoint([]byte(strings.Repeat(" ", MaxClientCheckpointBytes+1))); err == nil {
		t.Fatal("oversized action accepted")
	}
}
func TestClientCheckpointContractIsExact(t *testing.T) {
	a, _ := actionRig()
	b, _ := actionRig()
	b.Frames[0].Data = []byte(`{"type":"control_request","request_id":"fresh","request":{"subtype":"initialize"}}`)
	b.Frames[1].Data = []byte(`{"type":"control_response","response":{"request_id":"fresh","subtype":"success"},"text":"old","large":9007199254740993}`)
	if SameContract(a, b) != nil {
		t.Fatal("native control binding rejected")
	}
	b.Frames[2].Data = []byte(strings.Replace(string(b.Frames[2].Data), `"conversation_id":"old"`, `"conversation_id":"fresh"`, 1))
	if SameContract(a, b) == nil {
		t.Fatal("native ID binding rewrote action identity")
	}
}

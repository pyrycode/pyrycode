package claudetape

import (
	"encoding/json"
	"testing"
)

func TestHookOrderEvidence(t *testing.T) {
	user := func(text string) Frame {
		raw, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []any{map[string]string{"type": "text", "text": text}}}})
		return Frame{Direction: "stdin", Data: raw}
	}
	refusal := Frame{Direction: "stdout", Data: json.RawMessage(`{"type":"system","subtype":"informational","content":"blocked by the pyry 2255 capture rig: this prompt carries the rig marker","prevent_continuation":true}`)}
	result := Frame{Direction: "stdout", Data: json.RawMessage(`{"type":"result","subtype":"success"}`)}
	for _, tc := range []struct {
		name   string
		change func([]Frame) []Frame
		valid  bool
	}{
		{"valid", nil, true},
		{"result shape unconstrained", func(f []Frame) []Frame {
			f[2].Data = json.RawMessage(`{"type":"result","subtype":"error_during_execution"}`)
			return f
		}, true},
		{"early live", func(f []Frame) []Frame { f[2], f[3] = f[3], f[2]; return f }, false},
		{"missing refusal", func(f []Frame) []Frame { return append(f[:1], f[2:]...) }, false},
		{"missing result", func(f []Frame) []Frame { return append(f[:2], f[3:]...) }, false},
		{"missing live result", func(f []Frame) []Frame { return f[:4] }, false},
		{"wrong block", func(f []Frame) []Frame { f[0] = user("different"); return f }, false},
		{"wrong live", func(f []Frame) []Frame { f[3] = user("different"); return f }, false},
		{"output user", func(f []Frame) []Frame { f[0].Direction = "stdout"; return f }, false},
		{"duplicate result", func(f []Frame) []Frame { return append(f, result) }, false},
		{"nonblocking info", func(f []Frame) []Frame {
			f[1].Data = json.RawMessage(`{"type":"system","subtype":"informational","content":"blocked by the pyry 2255 capture rig: this prompt carries the rig marker","prevent_continuation":false}`)
			return f
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := result
			r.Data = append([]byte(nil), result.Data...)
			frames := []Frame{user("block"), refusal, r, user("live"), result}
			if tc.change != nil {
				frames = tc.change(frames)
			}
			err := (&Tape{Frames: frames}).RequireHookOrder("block", "live")
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%t err=%v", tc.valid, err)
			}
		})
	}
}

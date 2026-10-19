package claudetape

import (
	"encoding/json"
	"testing"
)

func TestMidTurnInputCheckpoint(t *testing.T) {
	frame := func(direction, value string) Frame { return Frame{Direction: direction, Data: json.RawMessage(value)} }
	use := frame("stdout", `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","id":"bash"}]}}`)
	partial := frame("stdout", `{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"tool_use","name":"Bash","id":"bash"}}}`)
	input := frame("stdin", `{"type":"user","message":{"content":[{"type":"text","text":"queued"}]}}`)
	result := frame("stdout", `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"bash"}]}}`)
	foreign := frame("stdout", `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"foreign"}]}}`)
	wrong := frame("stdin", `{"type":"user","message":{"content":[{"type":"text","text":"different"}]}}`)
	output := input
	output.Direction = "stdout"
	for _, tt := range []struct {
		name   string
		frames []Frame
		valid  bool
	}{
		{"complete", []Frame{use, input, result}, true},
		{"partial-tool-start", []Frame{partial, input, use, result}, true},
		{"foreign-result-ignored", []Frame{use, foreign, input, result}, true},
		{"no-tool", []Frame{input, result}, false},
		{"no-input", []Frame{use, result}, false},
		{"wrong-input", []Frame{use, wrong, result}, false},
		{"stdout-is-not-input", []Frame{use, output, result}, false},
		{"input-before-tool", []Frame{input, use, result}, false},
		{"input-after-result", []Frame{use, result, input}, false},
		{"no-matching-result", []Frame{use, input, foreign}, false},
		{"duplicate-input", []Frame{use, input, input, result}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tape := Tape{Frames: tt.frames}
			if err := tape.RequireMidTurnInput("queued", "Bash"); (err == nil) != tt.valid {
				t.Fatalf("checkpoint acceptance = %v, want %v", err, tt.valid)
			}
		})
	}
}

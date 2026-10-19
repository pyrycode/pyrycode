package claudetape

import (
	"encoding/json"
	"errors"
)

// RequireMidTurnInput requires the exact user text on stdin after the first
// named tool starts and before its result. Without this causal input boundary,
// fast replay could finish the busy turn before the client queues its message.
// This checks pipe order, not whether the real tool executed or how long it ran.
func (t *Tape) RequireMidTurnInput(text, tool string) error {
	type block struct {
		Type, Name, ID, Text string
		ToolUseID            string `json:"tool_use_id"`
	}
	use, input, result, inputs := -1, -1, -1, 0
	var toolID string
	for i, f := range t.Frames {
		var v struct {
			Type    string
			Message struct{ Content []block }
			Event   struct {
				Type         string
				ContentBlock block `json:"content_block"`
			}
		}
		if json.Unmarshal(f.Data, &v) != nil {
			return errors.New("cannot inspect mid-turn checkpoint")
		}
		blocks := v.Message.Content
		if v.Type == "stream_event" && v.Event.Type == "content_block_start" {
			blocks = append(blocks, v.Event.ContentBlock)
		}
		for _, b := range blocks {
			if f.Direction == "stdout" && b.Type == "tool_use" && b.Name == tool && toolID == "" && b.ID != "" {
				use, toolID = i, b.ID
			}
			if f.Direction == "stdin" && v.Type == "user" && b.Type == "text" && b.Text == text {
				input = i
				inputs++
			}
			if f.Direction == "stdout" && b.Type == "tool_result" && toolID != "" && b.ToolUseID == toolID && result < 0 {
				result = i
			}
		}
	}
	if inputs != 1 || use < 0 || input <= use || result <= input {
		return errors.New("missing exact mid-turn stdin checkpoint before tool result")
	}
	return nil
}

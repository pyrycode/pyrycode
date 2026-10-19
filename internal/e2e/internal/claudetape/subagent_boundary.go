package claudetape

import (
	"encoding/json"
	"errors"
	"strings"
)

// RequireSubagentMainObservation requires genuine native main text on both
// sides of the actual first client main-delta observation. This barrier lets the
// real text coalescer flush once in replay without timers in the recorded stream.
func (t *Tape) RequireSubagentMainObservation(conv, prompt string) error {
	fail := errors.New("subagent lacks its native main text observation boundary")
	if t.Version != 2 || t.Scenario != "subagent-text" {
		return fail
	}
	actions, users, before, after := 0, 0, 0, 0
	for _, f := range t.Frames {
		if f.Direction == ClientCheckpointDirection {
			c, err := DecodeClientCheckpoint(f.Data)
			if err != nil || c.Action != "main_stream_observed" || c.ConversationID != conv || c.Sequence != 1 || users != 1 || before == 0 {
				return fail
			}
			actions++
			continue
		}
		if f.Direction == "stdin" {
			var v checkpointSource
			if json.Unmarshal(f.Data, &v) != nil {
				return fail
			}
			if v.Type == "user" {
				users++
				if users != 1 || !sourceUserText(v, prompt) {
					return fail
				}
			}
		}
		if f.Direction != "stdout" {
			continue
		}
		var v struct {
			Type   string
			Parent json.RawMessage `json:"parent_tool_use_id"`
			Event  struct {
				Type  string
				Delta struct{ Type, Text string }
			}
		}
		if json.Unmarshal(f.Data, &v) != nil {
			return fail
		}
		main := len(v.Parent) == 0 || string(v.Parent) == "null" || string(v.Parent) == `""`
		if users == 1 && main && v.Type == "stream_event" && v.Event.Type == "content_block_delta" && v.Event.Delta.Type == "text_delta" && strings.TrimSpace(v.Event.Delta.Text) != "" {
			if actions == 0 {
				before++
			} else {
				after++
			}
		}
	}
	if users != 1 || actions != 1 || before == 0 || after == 0 {
		return fail
	}
	return nil
}

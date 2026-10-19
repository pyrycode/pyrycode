package claudetape

import (
	"encoding/json"
	"errors"
)

type checkpointSource struct {
	Type      string
	RequestID string `json:"request_id"`
	Subtype   string
	Request   struct{ Subtype string }
	Message   struct {
		Role    string
		Content []checkpointBlock
	}
	Event struct {
		Type         string
		ContentBlock checkpointBlock `json:"content_block"`
	}
}
type checkpointBlock struct {
	Type, Name, ID, Text string
	ToolUseID            string `json:"tool_use_id"`
}

func sourceUserText(v checkpointSource, want string) bool {
	if v.Type != "user" || v.Message.Role != "user" {
		return false
	}
	for _, b := range v.Message.Content {
		if b.Type == "text" && b.Text == want {
			return true
		}
	}
	return false
}

// RequireQueueAdmission proves the actual client queue observation happened
// before the held tool result, while ordinary delivery reached stdin only after
// the first terminal result. A timestamp or output-only marker cannot replace it.
func (t *Tape) RequireQueueAdmission(conv, prompt, queued string) error {
	fail := func() error { return errors.New("ordinary queue lacks its exact causal action boundary") }
	if t.Version != 2 || t.Scenario != "ordinary-queue-placement" {
		return fail()
	}
	users, actions, terminals := 0, 0, 0
	first, use, action, result, terminal, delivery, completed := -1, -1, -1, -1, -1, -1, -1
	toolID := ""
	for i, f := range t.Frames {
		if f.Direction == ClientCheckpointDirection {
			c, err := DecodeClientCheckpoint(f.Data)
			if err != nil || c.Action != "queue_admitted" || c.Sequence != 1 || c.ConversationID != conv || c.MessageID != "ordinary-queued" || c.Text != queued || c.QueuedMsgID == 0 {
				return fail()
			}
			actions++
			action = i
			continue
		}
		var v checkpointSource
		if json.Unmarshal(f.Data, &v) != nil {
			return fail()
		}
		if f.Direction == "stdin" && v.Type == "user" {
			users++
			if users == 1 {
				if !sourceUserText(v, prompt) {
					return fail()
				}
				first = i
			}
			if users == 2 {
				if !sourceUserText(v, queued) {
					return fail()
				}
				delivery = i
			}
			if users > 2 {
				return fail()
			}
		}
		if f.Direction != "stdout" {
			continue
		}
		blocks := v.Message.Content
		if v.Type == "stream_event" && v.Event.Type == "content_block_start" {
			blocks = append(blocks, v.Event.ContentBlock)
		}
		for _, b := range blocks {
			if b.Type == "tool_use" && b.Name == "Bash" && toolID == "" && b.ID != "" {
				toolID = b.ID
				use = i
			}
			if b.Type == "tool_result" && b.ToolUseID == toolID && toolID != "" && result < 0 {
				result = i
			}
		}
		if v.Type == "result" {
			terminals++
			if terminals == 1 {
				terminal = i
			} else if terminals == 2 {
				completed = i
			} else {
				return fail()
			}
		}
	}
	if users != 2 || actions != 1 || terminals != 2 || first < 0 || use <= first || action <= use || result <= action || terminal <= result || delivery <= terminal || completed <= delivery {
		return fail()
	}
	return nil
}

// RequireInterruptBoundary binds an observed daemon phase to the exact native
// interrupt request before its cancelled result, and a subsequent health turn
// when required. Physical interruption and provider latency remain live proof.
func (t *Tape) RequireInterruptBoundary(conv, prompt, health string, thinking bool) error {
	fail := func() error { return errors.New("interrupt lacks its exact phase/input/cancelled boundary") }
	profile, phase := "interrupt-running", "responding"
	if thinking {
		profile, phase = "interrupt-thinking", "thinking"
	}
	if t.Version != 2 || t.Scenario != profile {
		return fail()
	}
	user, action, interrupt, cancelled, healthInput, healthResult := -1, -1, -1, -1, -1, -1
	users, actions, interrupts, results := 0, 0, 0, 0
	for i, f := range t.Frames {
		if f.Direction == ClientCheckpointDirection {
			c, err := DecodeClientCheckpoint(f.Data)
			if err != nil || c.Action != "interrupt_phase" || c.Phase != phase || c.ConversationID != conv || c.Sequence != 1 {
				return fail()
			}
			actions++
			action = i
			continue
		}
		var v checkpointSource
		if json.Unmarshal(f.Data, &v) != nil {
			return fail()
		}
		if f.Direction == "stdin" && v.Type == "user" {
			users++
			switch users {
			case 1:
				if !sourceUserText(v, prompt) {
					return fail()
				}
				user = i
			case 2:
				if thinking || !sourceUserText(v, health) {
					return fail()
				}
				healthInput = i
			default:
				return fail()
			}
		}
		if f.Direction == "stdin" && v.Type == "control_request" && v.Request.Subtype == "interrupt" {
			if v.RequestID == "" {
				return fail()
			}
			interrupts++
			interrupt = i
		}
		if f.Direction == "stdout" && v.Type == "result" {
			results++
			if results == 1 {
				if v.Subtype != "error_during_execution" {
					return fail()
				}
				cancelled = i
			} else if results == 2 && !thinking {
				healthResult = i
			} else {
				return fail()
			}
		}
	}
	if user < 0 || actions != 1 || interrupts != 1 || action <= user || interrupt <= action || cancelled <= interrupt {
		return fail()
	}
	if thinking {
		if users != 1 || results != 1 {
			return fail()
		}
	} else if users != 2 || results != 2 || healthInput <= cancelled || healthResult <= healthInput {
		return fail()
	}
	return nil
}

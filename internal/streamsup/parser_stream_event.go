package streamsup

import (
	"encoding/json"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// streamEventLine keeps the vendor event payload raw until consumeLine has selected
// the top-level stream_event arm. Nested control data can therefore never influence
// top-level segmentation, and an unsafe inner shape remains available to the existing
// capped Unrecognized path.
type streamEventLine struct {
	Event           json.RawMessage `json:"event"`
	ParentToolUseID json.RawMessage `json:"parent_tool_use_id"`
}

type streamEventInner struct {
	Type         string                   `json:"type"`
	Index        *int                     `json:"index"`
	Message      *streamEventMessage      `json:"message"`
	ContentBlock *streamEventContentBlock `json:"content_block"`
	Delta        json.RawMessage          `json:"delta"`
}

type streamEventMessage struct {
	ID string `json:"id"`
}

type streamEventContentBlock struct {
	Type string `json:"type"`
}

type streamEventDelta struct {
	Type string  `json:"type"`
	Text *string `json:"text"`
}

// emitStreamEvent consumes one stream_event family member. Only text_delta has a
// client-visible mapping; the other measured variants carry lifecycle, thinking,
// signature, or partial tool-input data whose settled forms already have owners.
// Unsafe inner shapes take one visible rejection path and never publish text.
func (p *Parser) emitStreamEvent(line []byte) {
	reject := func() {
		p.emitUnrecognized(turnevent.UnrecognizedLineType, "stream_event", line)
	}
	var outer streamEventLine
	if err := json.Unmarshal(line, &outer); err != nil {
		reject()
		return
	}
	var event streamEventInner
	if err := json.Unmarshal(outer.Event, &event); err != nil || event.Type == "" {
		reject()
		return
	}

	switch event.Type {
	case "message_start":
		if event.Message == nil || event.Message.ID == "" {
			reject()
			return
		}
		p.streamMessageID = event.Message.ID
		p.resetStreamEventBlock()
	case "content_block_start":
		if event.Index == nil || *event.Index < 0 || event.ContentBlock == nil || event.ContentBlock.Type == "" {
			reject()
			return
		}
		p.streamBlockIndex = *event.Index
		p.streamBlockType = event.ContentBlock.Type
		p.streamBlockOpen = true
		p.streamBlockTextDelta = false
	case "content_block_delta":
		var delta streamEventDelta
		if err := json.Unmarshal(event.Delta, &delta); err != nil || delta.Type == "" {
			reject()
			return
		}
		if event.Index == nil || *event.Index < 0 {
			// A recognized thinking delta stays out of the raw Unrecognized lane:
			// its body is model reasoning. It cannot open a turn without attribution,
			// but it must remain content-free when refused.
			if delta.Type == "thinking_delta" {
				return
			}
			reject()
			return
		}
		switch delta.Type {
		case "text_delta":
			if delta.Text == nil || p.streamMessageID == "" || !p.streamBlockOpen ||
				p.streamBlockIndex != *event.Index || p.streamBlockType != "text" {
				reject()
				return
			}
			p.streamBlockTextDelta = true
			p.emit(turnevent.TextChunk{MessageID: p.streamMessageID, Text: *delta.Text})
		case "thinking_delta":
			// streamEventDelta deliberately has no field for the thinking bytes. A
			// matching block therefore publishes lifecycle evidence without making
			// reasoning reachable from this event, a mapper, or a log.
			if p.streamMessageID == "" || !p.streamBlockOpen ||
				p.streamBlockIndex != *event.Index || p.streamBlockType != "thinking" {
				return
			}
			p.emit(turnevent.ThoughtChunk{
				MessageID:        p.streamMessageID,
				ParentToolCallID: parentToolUseID(outer.ParentToolUseID),
			})
		case "signature_delta", "input_json_delta":
			return
		default:
			reject()
		}
	case "content_block_stop":
		if event.Index == nil || *event.Index < 0 {
			reject()
			return
		}
		if p.streamBlockOpen && p.streamBlockIndex == *event.Index {
			p.resetStreamEventBlock()
		}
	case "message_delta", "message_stop":
		return
	default:
		reject()
	}
}

func (p *Parser) resetStreamEventBlock() {
	p.streamBlockIndex = 0
	p.streamBlockType = ""
	p.streamBlockOpen = false
	p.streamBlockTextDelta = false
}

func (p *Parser) resetStreamEventState() {
	p.streamMessageID = ""
	p.resetStreamEventBlock()
}

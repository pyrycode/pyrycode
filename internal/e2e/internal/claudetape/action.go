package claudetape

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
)

const ClientCheckpointDirection = "client_checkpoint"
const MaxClientCheckpointBytes = 8192

type ClientCheckpoint struct {
	Type           string `json:"type"`
	Sequence       uint64 `json:"sequence"`
	Action         string `json:"action"`
	ConversationID string `json:"conversation_id"`
	MessageID      string `json:"message_id,omitempty"`
	QueuedMsgID    uint64 `json:"queued_msg_id,omitempty"`
	Text           string `json:"text,omitempty"`
	Phase          string `json:"phase,omitempty"`
}

func RequiresClientCheckpoint(name string) bool {
	return name == "ordinary-queue-placement" || name == "interrupt-running" || name == "interrupt-thinking"
}

// DecodeClientCheckpoint accepts a closed, duplicate-free action object. These
// are actual client observations, never Claude stdin or control-ID bindings.
func DecodeClientCheckpoint(raw []byte) (ClientCheckpoint, error) {
	var c ClientCheckpoint
	fail := func() (ClientCheckpoint, error) {
		return ClientCheckpoint{}, errors.New("invalid typed client checkpoint")
	}
	if len(raw) > MaxClientCheckpointBytes {
		return fail()
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return fail()
	}
	keys := map[string]bool{}
	for d.More() {
		token, err = d.Token()
		if err != nil {
			return fail()
		}
		key, ok := token.(string)
		if !ok || keys[key] {
			return fail()
		}
		keys[key] = true
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return fail()
		}
	}
	if token, err = d.Token(); err != nil || token != json.Delim('}') {
		return fail()
	}
	if d.Decode(new(any)) != io.EOF {
		return fail()
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&c) != nil {
		return fail()
	}
	required := []string{"type", "sequence", "action", "conversation_id"}
	switch c.Action {
	case "queue_admitted":
		required = append(required, "message_id", "queued_msg_id", "text")
		if c.MessageID == "" || c.QueuedMsgID == 0 || c.Text == "" {
			return fail()
		}
	case "interrupt_phase":
		required = append(required, "phase")
		if c.Phase != "thinking" && c.Phase != "responding" {
			return fail()
		}
	default:
		return fail()
	}
	if c.Type != ClientCheckpointDirection || c.Sequence == 0 || c.Sequence > MaxFrames || c.ConversationID == "" || len(keys) != len(required) {
		return fail()
	}
	for _, key := range required {
		if !keys[key] {
			return fail()
		}
	}
	return c, nil
}

func (p *Player) Action(raw []byte) ([]json.RawMessage, error) {
	if p.err != nil {
		return nil, p.err
	}
	fail := func() ([]json.RawMessage, error) {
		p.err = errors.New("typed client action contract changed")
		return nil, p.err
	}
	if p.tape.Version != 2 || p.next >= len(p.tape.Frames) || p.tape.Frames[p.next].Direction != ClientCheckpointDirection {
		return fail()
	}
	want, err := DecodeClientCheckpoint(p.tape.Frames[p.next].Data)
	if err != nil {
		return fail()
	}
	got, err := DecodeClientCheckpoint(raw)
	if err != nil || !reflect.DeepEqual(want, got) {
		return fail()
	}
	p.next++
	out := p.Prefix()
	return out, p.err
}

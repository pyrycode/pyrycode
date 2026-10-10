package relay

import (
	"bytes"
	"encoding/json"
	"strconv"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

func threadItemUpdate(typ string) bool {
	switch typ {
	case protocol.TypeThreadItemAdded, protocol.TypeThreadItemChanged, protocol.TypeThreadTextAppend:
		return true
	}
	return false
}

// threadWithheld gates items independently of reply correlation and filters
// replaced unsolicited content. It runs before sealing, so drops spend no nonce.
func (m *V2SessionManager) threadWithheld(s *V2Session, env protocol.Envelope) bool {
	if threadItemUpdate(env.Type) {
		id := pushedConversationID(env)
		return !s.thread || !s.interactive || id == "" || (!s.multiAgent && m.cfg.CodexConversation != nil && m.cfg.CodexConversation(id))
	}
	if !s.thread {
		return env.SessionStateCleared
	}
	if liveStateFamily(env.Type) {
		if !s.interactive || !validLiveTag(env.SessionID) || env.EventID != nil {
			return true
		}
		id := pushedConversationID(env)
		if (id != "" && m.liveWithheld(s, id)) || ((len(env.SessionID) > 0 || env.SessionStateCleared) && id == "") {
			return true
		}
		if env.SessionStateCleared {
			var payload map[string]json.RawMessage
			if json.Unmarshal(env.Payload, &payload) != nil || payload == nil || len(payload) != 0 {
				return true
			}
		}
	}
	if env.InReplyTo != nil {
		return false
	}
	switch env.Type {
	case protocol.TypeAssistantDelta, protocol.TypeToolUse, protocol.TypeToolResult,
		protocol.TypeToolDenied, protocol.TypeTurnEnd, protocol.TypeSessionTransition,
		protocol.TypeMessage, protocol.TypeBackgroundTaskStarted, protocol.TypeBackgroundTaskUpdated,
		protocol.TypeBackgroundTaskRoster, protocol.TypeQueueState, protocol.TypeBanner,
		protocol.TypeCompactionBoundary, protocol.TypeModelRefusalFallback, protocol.TypeModelRefusalNoFallback, protocol.TypeUnrecognizedMessage:
		return true
	}
	return false
}

// replaceObjectField preserves the order and encoded values of unrelated fields.
// A nil replacement removes the field. Unchanged objects retain their bytes.
func replaceObjectField(raw json.RawMessage, field string, replacement json.RawMessage) json.RawMessage {
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return raw
	}
	var fields []json.RawMessage
	found := false
	changed := false
	for d.More() {
		start := int(d.InputOffset())
		key, err := d.Token()
		if err != nil {
			return raw
		}
		var value json.RawMessage
		if err := d.Decode(&value); err != nil {
			return raw
		}
		end := int(d.InputOffset())
		if key == field {
			found = true
			if bytes.Equal(value, replacement) {
				fields = append(fields, bytes.TrimLeft(raw[start:end], ", \n\r\t"))
				continue
			}
			changed = true
			if replacement == nil {
				continue
			}
			encoded, _ := json.Marshal(field)
			fields = append(fields, append(append(encoded, ':'), replacement...))
		} else {
			fields = append(fields, bytes.TrimLeft(raw[start:end], ", \n\r\t"))
		}
	}
	if found && !changed || !found && replacement == nil {
		return raw
	}
	if !found {
		encoded, _ := json.Marshal(field)
		fields = append(fields, append(append(encoded, ':'), replacement...))
	}
	out := []byte{'{'}
	for i, value := range fields {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, value...)
	}
	return append(out, '}')
}

func (m *V2SessionManager) threadSummary(s *V2Session, raw json.RawMessage) json.RawMessage {
	var row struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(raw, &row) != nil {
		return raw
	}
	var reading json.RawMessage
	if s.thread && m.cfg.ThreadLastShownVersion != nil {
		if version, ok := m.cfg.ThreadLastShownVersion(row.ID); ok {
			reading = json.RawMessage(strconv.FormatUint(version, 10))
		}
	}
	return replaceObjectField(raw, "last_shown_version", reading)
}

func (m *V2SessionManager) threadProjected(s *V2Session, env protocol.Envelope) protocol.Envelope {
	if !s.thread {
		env.SessionID = nil
		env.SessionStateCleared = false
	}
	switch env.Type {
	case protocol.TypeConversationUpdated:
		env.Payload = m.threadSummary(s, env.Payload)
	case protocol.TypeConversations:
		var p struct {
			Conversations []json.RawMessage `json:"conversations"`
		}
		if json.Unmarshal(env.Payload, &p) != nil || p.Conversations == nil {
			return env
		}
		changed := false
		for i, row := range p.Conversations {
			projected := m.threadSummary(s, row)
			changed = changed || !bytes.Equal(row, projected)
			p.Conversations[i] = projected
		}
		if changed {
			rows, err := json.Marshal(p.Conversations)
			if err == nil {
				env.Payload = replaceObjectField(env.Payload, "conversations", rows)
			}
		}
	}
	return env
}

// threadReply projects only changed fields in the original envelope, retaining
// handler bytes when no projection is needed. Item gating also covers continuations.
func (m *V2SessionManager) threadReply(s *V2Session, frame json.RawMessage) (json.RawMessage, bool) {
	var env protocol.Envelope
	if json.Unmarshal(frame, &env) != nil {
		return frame, false
	}
	if m.threadWithheld(s, env) {
		return nil, true
	}
	projected := m.threadProjected(s, env)
	if !s.thread {
		frame = replaceObjectField(frame, "session_id", nil)
		frame = replaceObjectField(frame, "session_state_cleared", nil)
	}
	if !bytes.Equal(projected.Payload, env.Payload) {
		frame = replaceObjectField(frame, "payload", projected.Payload)
	}
	if s.thread && liveStateFamily(env.Type) && len(frame) > protocol.MaxThreadEnvelopeBytes {
		return nil, true
	}
	return frame, false
}

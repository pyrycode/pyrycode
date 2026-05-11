package protocol

import "time"

// SendMessagePayload is the body of an Envelope whose Type == TypeSendMessage
// (docs/protocol-mobile.md § send_message). Phone → binary direction.
type SendMessagePayload struct {
	ConversationID string `json:"conversation_id"`
	MessageID      string `json:"message_id"`
	Text           string `json:"text"`
}

// MessagePayload is the body of an Envelope whose Type == TypeMessage
// (docs/protocol-mobile.md § message). Binary → phone direction; carries
// either a user-message echo (to other paired devices) or an assistant
// reply. Role is one of "user", "assistant", "system" per the spec's field
// table; the type stays string (not a named Role enum) because the binary
// already treats role-strings as string-typed elsewhere.
type MessagePayload struct {
	ConversationID string `json:"conversation_id"`
	MessageID      string `json:"message_id"`
	Role           string `json:"role"`
	Text           string `json:"text"`
}

// BackfillSincePayload is the body of an Envelope whose Type ==
// TypeBackfillSince (docs/protocol-mobile.md § backfill_since). Phone →
// binary direction. SinceTS is RFC3339Nano per the envelope timestamp rule;
// MaxMessages is the phone's advisory cap on returned-message count.
type BackfillSincePayload struct {
	SinceTS        time.Time `json:"since_ts"`
	ConversationID *string   `json:"conversation_id"` // *string + no omitempty: spec wire shows literal `null` (meaning "all conversations"); omitempty would drop the key.
	MaxMessages    int       `json:"max_messages"`
}

// MessageChunkPayload is the body of an Envelope whose Type ==
// TypeMessageChunk (docs/protocol-mobile.md § message_chunk). Binary →
// phone direction; streamed during a backfill response. Messages reuses
// MessagePayload directly — the spec says "same shape as message.payload,
// multiple."
type MessageChunkPayload struct {
	Messages []MessagePayload `json:"messages"`
}

// BackfillDonePayload is the body of an Envelope whose Type ==
// TypeBackfillDone (docs/protocol-mobile.md § backfill_done). Binary →
// phone direction; sent after the last message_chunk to mark completion.
type BackfillDonePayload struct {
	Delivered int `json:"delivered"`
}

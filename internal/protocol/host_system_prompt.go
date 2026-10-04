package protocol

// Daemon-wide host system prompt frames are pure wire data. Handler wiring,
// runtime validation and I/O are pending #2768. Both inbound verbs use
// authenticated paired-client map dispatch, with no conversation/session lookup
// and no interactive-capability gate.
//
// Successful reads and durable writes return exactly one unicast
// host_system_prompt correlated by Envelope.InReplyTo, never broadcast. Malformed
// payloads, missing/null/non-string writes and values above the inclusive
// conversations.MaxSystemPromptBytes bound (8192 bytes) use non-retryable
// CodeProtocolMalformed. Storage failure uses retryable
// CodeHostSystemPromptUnavailable. Error messages are static and contain no
// submitted text. These are consumer obligations, not validation by these DTOs.

// RequestHostSystemPromptPayload is the body of request_host_system_prompt,
// client → daemon. It encodes as {} and reads the connected daemon's setting.
type RequestHostSystemPromptPayload struct{}

// SetHostSystemPromptPayload is the body of set_host_system_prompt, client →
// daemon. SystemPrompt is required: missing/null decode to nil and are invalid;
// other non-string JSON values fail decoding. A nonnil pointer preserves any
// string verbatim, including whitespace and "". Only "" clears durably; reset
// uses an ordinary set of the default returned in HostSystemPromptPayload.
// No omitempty: the wire key is required even though the zero DTO is invalid.
type SetHostSystemPromptPayload struct {
	SystemPrompt *string `json:"system_prompt"`
}

// HostSystemPromptPayload is the body of host_system_prompt, daemon → client.
// It answers a read or durable write with the current value and shipped reset
// value. Both strings always serialize, even when current is "". Correlation is
// solely Envelope.InReplyTo: no conversation/session identifier or live-session
// verdict is carried. Changes apply at the next session start.
type HostSystemPromptPayload struct {
	SystemPrompt        string `json:"system_prompt"`
	DefaultSystemPrompt string `json:"default_system_prompt"`
}

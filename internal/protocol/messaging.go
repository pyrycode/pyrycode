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

// SessionTransitionPayload is the body of an Envelope whose Type ==
// TypeSessionTransition (docs/protocol-mobile.md § session_transition).
// Binary → phone direction; the wire form of a session boundary the phone
// renders as a ThreadItem.SessionBoundary marker (pyrycode-mobile#336).
//
// Reason is a plain string (not a named enum, matching MessagePayload.Role /
// TurnEndPayload.StopReason — internal/protocol is a stdlib-only leaf data
// package) over the closed wire set {clear, idle_evict, workspace_change}.
// OccurredAt is RFC3339Nano per the envelope timestamp rule.
//
// WorkspaceCwd is *string with no omitempty (mirroring
// BackfillSincePayload.ConversationID): it carries the new workspace dir for
// reason "workspace_change" and renders literal JSON null for "clear" /
// "idle_evict". This encodes the workspaceCwd-non-null-iff-workspace_change
// invariant directly on the wire — omitempty would drop the key and lose that
// distinction.
type SessionTransitionPayload struct {
	PreviousSessionID string    `json:"previous_session_id"`
	NewSessionID      string    `json:"new_session_id"`
	Reason            string    `json:"reason"`
	OccurredAt        time.Time `json:"occurred_at"`
	WorkspaceCwd      *string   `json:"workspace_cwd"` // *string + no omitempty: literal `null` for non-workspace_change reasons; omitempty would drop the key.
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

// Modal v2 wire payloads (epic #597 Phase 3, docs/protocol-mobile.md § Modal).
// These describe a modal the supervised claude surfaced (a permission prompt, a
// plan-approval, a tool-confirmation) over the encrypted mobile wire and the
// phone's answer to it. This is wire vocabulary only: pure structs and their
// (de)serialization. The minting of modal_id nonces, the dedup of answers by
// answer_token, the inbound-answer validation, and the fan-out gate all live in
// the producer (#703, with #706/#702 building ownership/gating), NOT here.
//
// No field carries omitempty: every field is always present on the wire so the
// testdata fixtures pin the full shape and boundary values like an empty
// default_option_id or option_id do not silently vanish.

// ModalOption is a single, ordered choice offered by a ModalShownPayload. ID is
// the stable identifier ModalAnswerPayload.OptionID and
// ModalShownPayload.DefaultOptionID reference; Label is the human-readable
// display text.
type ModalOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// ModalShownPayload is the body of an Envelope whose Type == TypeModalShown
// (docs/protocol-mobile.md § Modal). Binary → phone direction; surfaces a modal
// to the phone. It rides the "interactive" capability (#607) — viewing a modal
// is ungated; answering is gated separately, per-device (#702).
//
// ModalID is a one-time, opaque, unguessable nonce minted per surfaced modal
// (by #703). It is the sole correlation key: there is no conversation_id, so
// the daemon resolves ModalID against its own outstanding-modal state and never
// trusts a phone-asserted conversation. Options is ordered — the JSON-array
// order is the canonical display/selection order. DefaultOptionID MUST equal
// one of Options[].ID (documented invariant; the producer enforces it). Class
// is a plain string over a closed wire set (e.g. "permission"), not a named
// enum (leaf-data convention, matching MessagePayload.Role); the exhaustive
// class vocabulary is #703's to finalize.
type ModalShownPayload struct {
	ModalID         string        `json:"modal_id"`
	Class           string        `json:"class"`
	Title           string        `json:"title"`
	Prompt          string        `json:"prompt"`
	Options         []ModalOption `json:"options"`
	DefaultOptionID string        `json:"default_option_id"`
}

// ModalAnswerPayload is the body of an Envelope whose Type == TypeModalAnswer
// (docs/protocol-mobile.md § Modal). Phone → binary direction.
//
// This is an inbound v2 *control* envelope, structurally like
// RequestSnapshotPayload / TypeRekeyRequest: the v2 session manager intercepts
// it at dispatchAppFrame before dispatch.Route. There is NO dispatch.Route
// handler — the interception, validation (against the daemon's current
// outstanding ModalID, #703/#706), and dedup live in the producer.
//
// AnswerToken is a client-minted idempotency key (uniqueness and stability
// matter, secrecy does not): it lets the daemon collapse a replayed or
// reordered modal_answer to a no-op (#703). It is NOT the authorization —
// authorization is ModalID validity (#706) plus the per-device gate (#702).
type ModalAnswerPayload struct {
	ModalID     string `json:"modal_id"`
	OptionID    string `json:"option_id"`
	AnswerToken string `json:"answer_token"`
}

// ModalCancelPayload is the body of an Envelope whose Type == TypeModalCancel
// (docs/protocol-mobile.md § Modal). Phone → binary direction; cancels an
// outstanding modal from the phone.
//
// Like ModalAnswerPayload this is an inbound v2 control envelope intercepted at
// dispatchAppFrame before dispatch.Route — there is NO dispatch.Route handler.
type ModalCancelPayload struct {
	ModalID string `json:"modal_id"`
}

// ModalDismissedPayload is the body of an Envelope whose Type ==
// TypeModalDismissed (docs/protocol-mobile.md § Modal). Binary → phone
// direction; notifies the phone that a modal was resolved.
//
// Outcome is the selected ModalOption.ID when answered, or a producer-defined
// sentinel for cancel/timeout (plain string; the sentinel vocabulary is #703's,
// documented not enforced). Source is the closed set {remote, local, timeout}:
// remote = a phone modal_answer/modal_cancel, local = answered/cancelled at the
// desktop TTY, timeout = deny-on-timeout fired. Plain string, not a named enum.
type ModalDismissedPayload struct {
	ModalID string `json:"modal_id"`
	Outcome string `json:"outcome"`
	Source  string `json:"source"`
}

// Queue v2 wire payloads (epic #597 Phase 3, docs/protocol-mobile.md § Queue).
// These describe the queued-message backlog the daemon reports to the phone and
// the phone's request to cancel one entry. This is wire vocabulary only: pure
// structs and their (de)serialization. The emission on queue change, the
// resolution of conversation_id to an authorized conversation, and the
// msgqueue.Remove call all live in the producer (#722) / handler (#723), NOT
// here.
//
// No field carries omitempty: every field is always present on the wire so the
// testdata fixtures pin the full shape. Note that []QueuedItem(nil) marshals to
// JSON null while a non-nil empty slice marshals to []; the leaf type cannot
// force non-nil, so an empty backlog's [] vs null rendering is the producer's
// (#722) call (recommended: emit []) — both round-trip here.

// QueuedItem is one element of QueueStatePayload.Queued — the wire form of
// msgqueue.QueuedMessage (the producer #722 maps QueuedMessage.ID → QueuedMsgID).
// Named for its role in the array (the ModalOption precedent), not after the
// engine type, to keep it wire-scoped. Text is untrusted, phone-originated
// transit content (see the producer/handler #722/#723 for the never-log
// discipline).
type QueuedItem struct {
	QueuedMsgID uint64    `json:"queued_msg_id"`
	Text        string    `json:"text"`
	TS          time.Time `json:"ts"`
}

// QueueStatePayload is the body of an Envelope whose Type == TypeQueueState
// (docs/protocol-mobile.md § Queue). Binary → phone direction; the wire form of
// msgqueue.Snapshot(convID). Queued is ordered (FIFO/enqueue order, the
// options []ModalOption precedent). ConversationID is the daemon's own resolved
// id (#722), never attacker-derived.
type QueueStatePayload struct {
	ConversationID string       `json:"conversation_id"`
	Queued         []QueuedItem `json:"queued"`
}

// DequeueMessagePayload is the body of an Envelope whose Type ==
// TypeDequeueMessage (docs/protocol-mobile.md § Queue). Phone → binary
// direction.
//
// This is an inbound v2 *control* envelope, structurally like
// ModalAnswerPayload / RequestSnapshotPayload: the v2 session manager intercepts
// it at dispatchAppFrame before dispatch.Route. There is NO dispatch.Route
// handler — resolving ConversationID to an authorized conversation and applying
// msgqueue.Remove(convID, QueuedMsgID) is the handler's (#723) job. Unlike
// modal_answer this is ungated for any paired phone (ADR 025 § Security model).
// ConversationID is untrusted phone input.
type DequeueMessagePayload struct {
	ConversationID string `json:"conversation_id"`
	QueuedMsgID    uint64 `json:"queued_msg_id"`
}

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

// SessionTransitionPayload is the body of an Envelope whose Type ==
// TypeSessionTransition (docs/protocol-mobile.md § session_transition).
// Binary → phone direction; the wire form of a session boundary the phone
// renders as a ThreadItem.SessionBoundary marker (pyrycode-mobile#336).
//
// ConversationID is the routing key — a plain string with no omitempty,
// mirroring the sibling interactive payloads (SendMessagePayload, etc.) — so
// the phone folds the boundary marker into the correct conversation's thread.
// The producer binds it from the active conversation↔session mapping in #741;
// until that lands it emits "" (harmless: the mobile consumer is parked).
//
// Reason is a plain string (not a named enum, matching MessagePayload.Role /
// TurnEndPayload.StopReason — internal/protocol is a stdlib-only leaf data
// package) over the closed wire set {clear, idle_evict, workspace_change}.
// OccurredAt is RFC3339Nano per the envelope timestamp rule.
//
// WorkspaceCwd is *string with no omitempty: it carries the new workspace dir
// for reason "workspace_change" and renders literal JSON null for "clear" /
// "idle_evict". This encodes the workspaceCwd-non-null-iff-workspace_change
// invariant directly on the wire — omitempty would drop the key and lose that
// distinction.
type SessionTransitionPayload struct {
	ConversationID    string    `json:"conversation_id"` // routing key; plain string (no literal-null semantics), no omitempty — mirrors the sibling interactive payloads
	PreviousSessionID string    `json:"previous_session_id"`
	NewSessionID      string    `json:"new_session_id"`
	Reason            string    `json:"reason"`
	OccurredAt        time.Time `json:"occurred_at"`
	WorkspaceCwd      *string   `json:"workspace_cwd"` // *string + no omitempty: literal `null` for non-workspace_change reasons; omitempty would drop the key.
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

// SessionErrorPayload is the body of an Envelope whose Type ==
// TypeSessionError (docs/protocol-mobile.md § Error codes). Binary → phone
// direction; the unsolicited, conversation-scoped frame the daemon emits when
// its interactive message queue gives up delivering queued messages (the
// msgqueue OnGiveUp seam, #1000). Wire vocabulary only — the producer that
// emits it is sibling #1008; the never-log discipline for Message is #1008's
// concern.
//
// ConversationID is the routing key — a plain string with no omitempty,
// mirroring the sibling interactive payloads (QueueStatePayload,
// SessionTransitionPayload) — so the phone attaches the error to the correct
// session. It is the daemon's own resolved id (set by #1008 from the OnGiveUp
// conversation_id), never attacker-derived — same posture as
// QueueStatePayload.ConversationID.
//
// Code is the terminal wire code (CodeSessionBlocked); a plain string over a
// closed wire set, not a named enum (leaf-data convention, matching
// MessagePayload.Role / SessionTransitionPayload.Reason). It names a terminal
// give-up, distinct from the transient CodeServerBinaryBusy, so a client cannot
// read the frame as "retry shortly". The struct carries NO Retryable /
// RetryAfterS fields: their structural absence is what prevents a client
// reading the frame as transient. Message is the daemon-generated
// human-readable reason. No field carries omitempty — all three are always
// present so the golden fixture pins the full shape.
type SessionErrorPayload struct {
	ConversationID string `json:"conversation_id"` // routing key; plain string, no omitempty — mirrors the sibling interactive payloads
	Code           string `json:"code"`            // terminal wire code (CodeSessionBlocked); plain string, not a named enum (leaf-data convention)
	Message        string `json:"message"`         // daemon-generated human-readable reason
}

// Debug-bundle streaming v2 wire payloads (#812, docs/protocol-mobile.md
// § Debug bundle). These carry a byte-generic bundle stream over the encrypted
// mobile channel: one debug_bundle_chunk per cap-respecting slice, then one
// debug_bundle_done marking completion. Binary → phone direction; wire
// vocabulary only. The chunker, the streaming primitive, and the reassembly
// reference live in internal/relay/v2bundlestream.go; the request verb that
// drives a stream is sibling #813.

// DebugBundleChunkPayload is the body of an Envelope whose Type ==
// TypeDebugBundleChunk (docs/protocol-mobile.md § Debug bundle). Binary → phone
// direction; one ordered slice of a streamed bundle.
//
// Seq is 0-based, contiguous, and ascending across a stream — the receiver
// (ReassembleBundle / the phone) requires the next chunk's Seq to equal the
// count of chunks already seen, so a reorder, gap, or duplicate fails cleanly
// rather than corrupting output. Data is the raw bundle slice; []byte
// auto-encodes as standard base64 via encoding/json, which the phone decodes.
// The slice is content-bearing bundle bytes — never logged (AC#4).
type DebugBundleChunkPayload struct {
	Seq  int    `json:"seq"`
	Data []byte `json:"data"`
}

// DebugBundleDonePayload is the body of an Envelope whose Type ==
// TypeDebugBundleDone (docs/protocol-mobile.md § Debug bundle). Binary → phone
// direction; the completion marker sent after the last debug_bundle_chunk.
//
// Total is the exact number of chunks in the stream. The receiver uses it to
// detect a truncated stream: a done whose Total does not equal the count of
// chunks actually received is a count-mismatch error, never accepted as
// complete.
type DebugBundleDonePayload struct {
	Total int `json:"total"`
}

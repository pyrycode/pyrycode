package protocol

import (
	"encoding/json"
	"time"
)

// MaxAttachmentIDsPerMessage bounds how many attachment ids one send_message may
// name (SendMessagePayload.AttachmentIDs below, published by #2036 in
// docs/protocol-mobile.md § Attachments).
//
// It is the FIRST of this family's two bound idioms — a published NUMBER, a
// producer-side contract a client must obey to compose a conforming frame, the
// posture MaxAttachmentChunkBytes and MaxAttachmentIDBytes ship with. It is
// deliberately not the second: the per-upload byte bound and the concurrency
// bound behind attachment.too_large / attachment.too_many_uploads are
// receiver-configured and unpublished, learned by being rejected. A client
// composing a message needs this one BEFORE it sends, not after.
//
// UNCHECKED HERE, like every bound this package declares. internal/protocol is a
// stdlib-only leaf data package with no producer, no consumer and no validator;
// nothing in this package counts the list, and a reader must not mistake a
// declared bound for one this type enforces. It IS enforced, since #2038, by
// SendMessage in internal/relay/handlers — the frame's first counter — which
// refuses an over-bound list with CodeProtocolMalformed rather than truncating
// it. No wire code is named HERE for that refusal even so; ErrUnknownUpload's
// doc block is the precedent for declining to publish a mapping this package
// does not own.
//
// The bound counts ELEMENTS, NOT DISTINCT IDS. A list may repeat one id 32
// times, and that counting rule is load-bearing rather than incidental: #2038
// DEDUPLICATES a repeat on first occurrence, and it counts this bound against
// the raw elements BEFORE doing so, so 33 copies of one id is over bound.
//
// Why 32, as arithmetic rather than taste. Each canonical id is exactly 36 bytes
// and costs 39 inside a JSON array (two quotes and one separator), so 32 ids are
// 1248 B — under 2% of the 65519-byte application-envelope cap — and the bound
// never competes with Text for envelope budget. That is also why it is NOT
// derived from the cap: that derivation yields roughly 1680 ids and would leave
// no room for the message. The binding constraint is resource rather than bytes,
// since every id named becomes a directory component the receiver resolves, so
// 32 bounds that work at a number a receiver does inline without a queue while
// sitting far above what a person attaches to one message.
// TestSendMessagePayload_MaxAttachmentIDs_FitV2EnvelopeCap measures the real
// total rather than trusting this paragraph; if it ever fails, LOWER this
// constant.
//
// DO NOT OVERSELL IT. The envelope cap already limits an unbounded list to
// roughly 1680 elements, and a paired device may already send messages that
// spawn claude turns — orders of magnitude more expensive than 1680 directory
// resolutions. This bound is contract clarity and bounded work, not a new
// defence against anything the pairing boundary does not already permit.
const MaxAttachmentIDsPerMessage = 32

// SendMessagePayload is the body of an Envelope whose Type == TypeSendMessage
// (docs/protocol-mobile.md § send_message). Phone → binary direction.
//
// AttachmentIDs names the uploaded attachments this message carries (#2036), so
// the daemon can name them instead of inferring the set from upload order or
// arrival timing. Wire vocabulary only: nothing produces, consumes or validates
// it here. Element order is the client's own presentation order and is NOT a
// correlation key — ids identify attachments, positions identify nothing,
// QuestionAnswerEntry's rule. #2038 is the consumer, and it composes the prompt
// naming each attachment's on-host path in this order, deduplicated on first
// occurrence.
//
// EVERY ELEMENT IS AN UNVERIFIED CLAIM, and it BECOMES A DIRECTORY COMPONENT
// beneath the resolved conversation directory. Its canonical shape — the
// lowercase UUIDv4 form docs/protocol-mobile.md § Attachments publishes under
// "The attachment_id shape" — is validated BEFORE it reaches filepath.Join;
// conversations.ValidID is that check, the one attachments.EnsureDir already
// applies on the upload leg. An element reaching a path unvalidated is a
// traversal, and MaxAttachmentIDBytes does not help: 64 bytes accommodates
// "../../../../etc/passwd" several times over, so containment is a consequence
// of the SHAPE and never of any length ceiling. No named type marks the
// untrusted elements, matching AttachmentChunkPayload, which carries the same
// hazard on a plain string field with a doc block rather than a wrapper type.
//
// THE SHAPE CHECK IS LOAD-BEARING A SECOND TIME, against § Security model threat
// 1 (prompt injection). AttachmentStoredPayload is exempt from that threat
// because it carries no client-authored byte; every element here is
// client-authored and #2038 composes claude's input from it. A shape-validated
// id draws from [0-9a-f-] only and CANNOT carry injection text, while an
// unvalidated element reaches claude verbatim. The two hazards are independent
// and one check answers both — which is why it is not optional even for a
// consumer that never touches the filesystem.
//
// THE ELEMENTS ARE NOT A CAPABILITY. What keeps the field safe is CONFINEMENT: a
// named id resolves only under the message's OWN conversation, the one the
// authenticated v2 session is already on, decided daemon-side from session
// context and never client-asserted. That is what stops "name any id, get its
// bytes into your prompt", and it is a property of THIS field's resolution — not
// one borrowed from a sibling frame. This block used to anchor it to
// AttachmentChunkPayload's deliberately absent conversation_id; that frame carries
// one since #2142, so the anchor is gone while the property is untouched. Do NOT argue
// UUIDv4-therefore-unguessable anywhere on this field: the family's published
// stance is that the id is not secret, not unguessable and never the only thing
// between a caller and a file, so an entropy claim would quietly promote it to
// the capability it is documented not to be.
//
// LOGGABLE ONLY AFTER SHAPE VALIDATION. § Attachments permits logging an
// attachment id — but for a value that has been checked. Raw, an element is an
// arbitrary client-supplied string in a line-oriented log, the same
// log-injection shape the section forbids for filename and for the same reason.
//
// It carries the package's ONLY omitempty, and the departure is the point rather
// than an oversight. send_message is a v1-compatible inbound type, so a client
// predating this field sends three keys and always will: the empty-case wire
// form is the KEY ABSENT ENTIRELY, which is exactly what those clients already
// emit. The nil→[] MarshalJSON that QuestionShownPayload, BackgroundTaskRosterPayload,
// ModelListPayload and ToolUsePayload carry is a shape precedent and not a
// placement one — every one of them is an outbound, daemon-authored v2 frame.
// QuestionAnswerPayload is the near miss, genuinely inbound and still
// normalising, but it is v2-only and has carried its array since it was minted,
// so "always present" was true of every client that ever sent one. Here it would
// be false on arrival. Do not add a MarshalJSON by analogy: omitempty already
// elides nil and empty-non-nil alike, so the two directions are symmetric
// without one, and TestSendMessagePayload_ZeroValue_KeyAbsent pins it.
type SendMessagePayload struct {
	ConversationID string   `json:"conversation_id"`
	MessageID      string   `json:"message_id"`
	Text           string   `json:"text"`
	AttachmentIDs  []string `json:"attachment_ids,omitempty"` // optional (#2036); every element is an unverified claim that becomes a path component — validate its canonical shape before use
}

// UnmarshalJSON collapses the three empty wire forms — key absent, null, and []
// — to one in-memory value, so no consumer can branch on which of them arrived.
//
// Without it that guarantee is FALSE, and the gap is the most common Go slice
// trap: encoding/json decodes [] to an empty NON-NIL slice while an absent key
// and null both leave nil. len(x) == 0 agrees across all three, but x == nil
// does not — so a consumer can tell [] from the other two, and #2038 is the
// consumer that reads this field.
//
// This is NORMALISATION AT THE DECODE BOUNDARY, not validation: nothing is
// checked and nothing is rejected on content, so the package overview's "pure
// DTOs, no methods, no Validate()" posture is departed from once, deliberately,
// and only where a parser-differential ambiguity on an attacker-controlled
// inbound frame belongs.
//
// A DECODE FAILURE IS PROPAGATED, NEVER SWALLOWED into an empty list — an
// ill-typed attachment_ids must be a rejected frame rather than a silent "this
// message names no attachments" read off garbage. The one production decode
// site, SendMessage in internal/relay/handlers, answers protocol.malformed on
// it; encoding/json's error carries the field path and the offending value's
// KIND, never the remote-authored bytes, so the handler logging it satisfies the
// rule QuestionAnswerPayload's doc block publishes for its own decoder.
//
// The alias type is a defined type with no methods, which is what keeps
// json.Unmarshal from recursing back into this method — SlashCommandListPayload.MarshalJSON's
// reason, in the decode direction. The receiver is a pointer, as encoding/json
// requires for an Unmarshaler, so this writes only through the caller's own
// value; the decoded strings are freshly allocated and alias nothing in b.
func (p *SendMessagePayload) UnmarshalJSON(b []byte) error {
	type alias SendMessagePayload
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	if len(a.AttachmentIDs) == 0 {
		a.AttachmentIDs = nil
	}
	*p = SendMessagePayload(a)
	return nil
}

// MessagePayload is the body of an Envelope whose Type == TypeMessage
// (docs/protocol-mobile.md § message). Binary → phone direction; carries
// either a user-message echo (to other paired devices) or an assistant
// reply. Role is one of "user", "assistant", "system" per the spec's field
// table; the type stays string (not a named Role enum) because the binary
// already treats role-strings as string-typed elsewhere.
//
// AttachmentIDs is set only on the STORED user entry the history log holds
// (#2596): the ids a send_message named, as the handler resolved them —
// deduplicated on first occurrence, each past the resolver's canonical-shape
// check. Same key and element shape as SendMessagePayload.AttachmentIDs. It is
// omitted when empty, so an assistant emission and an attachment-less user entry
// marshal exactly as they did before the field existed, and an older decoder
// ignores the key. It carries ids only, never the on-host path the delivery
// prompt names.
type MessagePayload struct {
	ConversationID string   `json:"conversation_id"`
	MessageID      string   `json:"message_id"`
	Role           string   `json:"role"`
	Text           string   `json:"text"`
	AttachmentIDs  []string `json:"attachment_ids,omitempty"`
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
// ConversationID is an outbound routing/scoping key (#1065): the daemon asserts
// it from active.CurrentConversation() so interactive clients filter display by
// conversation — a modal carrying conversation A's tool title/input summary is
// scoped to conns following A and never rendered by a conn viewing B. It is
// daemon-derived, not attacker-derived, and joins the client-side scoping model
// TurnStatePayload/QueueStatePayload already use. Declared first (leading, no
// omitempty), mirroring those sibling payloads. The INBOUND anti-forgery model
// is unchanged: ModalAnswerPayload/ModalCancelPayload carry NO conversation_id,
// and an answer is authorized by resolving its ModalID server-side against the
// registry (plus the per-device gate #702) — a phone still cannot assert which
// conversation an answer targets. Adding this outbound key does not loosen that
// inbound guarantee.
//
// ModalID is a one-time, opaque, unguessable nonce minted per surfaced modal
// (by #703). It is the correlation key an inbound answer is resolved against:
// the daemon matches ModalID to its own outstanding-modal state and never trusts
// a phone-asserted conversation. Options is ordered — the JSON-array
// order is the canonical display/selection order. DefaultOptionID MUST equal
// one of Options[].ID (documented invariant; the producer enforces it). Class
// is a plain string over a closed wire set (e.g. "permission"), not a named
// enum (leaf-data convention, matching MessagePayload.Role); the exhaustive
// class vocabulary is #703's to finalize.
type ModalShownPayload struct {
	ConversationID  string             `json:"conversation_id"` // outbound routing/scoping key; daemon-asserted, client filters on it (#1065). Inbound answers carry no conversation_id — see doc.
	ModalID         string             `json:"modal_id"`
	Class           string             `json:"class"`
	Title           string             `json:"title"`
	Prompt          string             `json:"prompt"`
	Options         []ModalOption      `json:"options"`
	DefaultOptionID string             `json:"default_option_id"`
	AlwaysAllow     AlwaysAllowPayload `json:"always_allow"`
	// The remaining fields are optional Claude-authored display context. They
	// are never daemon-derived from tool input or used as permission authority.
	// Reason preserves an open JSON shape; ReasonType preserves an open string
	// vocabulary. DefaultToNo is a client-selection hint, not a timeout verdict.
	Reason      json.RawMessage `json:"reason,omitempty"`
	ReasonType  string          `json:"reason_type,omitempty"`
	BlockedPath string          `json:"blocked_path,omitempty"`
	Description string          `json:"description,omitempty"`
	DefaultToNo bool            `json:"default_to_no,omitempty"`
}

// AlwaysAllowPayload describes the bounded rules a permission modal can display
// for a possible "don't ask again" choice. Both fields are always present.
type AlwaysAllowPayload struct {
	Offered bool     `json:"offered"`
	Rules   []string `json:"rules"`
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
	AlwaysAllow bool   `json:"always_allow,omitempty"`
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
// engine type, to keep it wire-scoped.
//
// TWO fields are untrusted, client-originated transit content, not one: Text and
// MessageID (see the producer/handler #722/#723 for the never-log discipline).
// The two ids sit adjacent but their provenance is opposite, and nothing in the
// type says so — QueuedMsgID is the daemon's own per-conversation counter, while
// MessageID is a string the client chose. Do not read them as a matched pair.
//
// MessageID (#2092) is the message_id from the send_message that produced this
// item, relayed byte-for-byte: never trimmed, lower-cased or re-encoded, "" when
// the client sent none, and never minted by the daemon. It exists so a client can
// merge this row with the optimistic echo it drew when the operator hit send —
// in interactive mode no user-message event is streamed, so that echo is the
// client's only record of its own message. It addresses nothing: dequeue_message
// still resolves conversation_id + queued_msg_id, and no code path reads this to
// route, authorize, match or dedupe. queue_state fans out to EVERY interactive
// connection, so a client sees ids it never minted and must merge only against
// echoes it minted itself — uniqueness across devices is enforced nowhere.
type QueuedItem struct {
	QueuedMsgID uint64    `json:"queued_msg_id"`
	MessageID   string    `json:"message_id"`
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

// NewSessionPayload is the body of an Envelope whose Type == TypeNewSession
// (docs/protocol-mobile.md § New session). Phone → binary direction.
//
// This is an inbound v2 *control* envelope, structurally like
// DequeueMessagePayload: the v2 session manager intercepts it at dispatchAppFrame
// before dispatch.Route, and there is NO dispatch.Route handler — resolving
// ConversationID and rotating that conversation's session is the handler's (#2099)
// job. Unlike its siblings the whole payload is OPTIONAL: the frame carried none
// at all until #2099, so an un-upgraded client sends a bare envelope and MUST keep
// working.
//
// ConversationID is untrusted phone input and is a validated LOOKUP KEY, never
// authorization and never a path component — the rule docs/protocol-mobile.md
// already publishes for request_attachment and, since #2142, for attachment_chunk.
// Absent, empty, or a body that does not decode at all are ONE value and one
// meaning: rotate the conversation the daemon's own cursor points at, which is
// the pre-#2099 behaviour verbatim. omitempty keeps that the shape a client with
// nothing to name actually emits, so the wire has one canonical bare form.
type NewSessionPayload struct {
	ConversationID string `json:"conversation_id,omitempty"`
}

// InterruptPayload is the body of an Envelope whose Type == TypeInterrupt
// (docs/protocol-mobile.md § Interrupt (v2)). Phone → binary direction.
//
// This is an inbound v2 *control* envelope, structurally identical to
// NewSessionPayload: the v2 session manager intercepts it at dispatchAppFrame
// before dispatch.Route, and there is NO dispatch.Route handler — resolving
// ConversationID and stopping that conversation's running turn is the handler's
// (#2103) job. Like new_session before #2099 the whole payload is OPTIONAL: the
// frame carried none at all until #2103, so an un-upgraded client sends a bare
// envelope and MUST keep working.
//
// ConversationID is untrusted phone input and is a validated LOOKUP KEY, never
// authorization and never a path component — the rule docs/protocol-mobile.md
// already publishes for request_attachment, attachment_chunk and new_session.
// Absent, empty, or a body that does not decode at all are ONE value and one
// meaning: interrupt the conversation the daemon's own cursor points at, which is
// the pre-#2103 behaviour verbatim. omitempty keeps that the shape a client with
// nothing to name actually emits, so the wire has one canonical bare form.
type InterruptPayload struct {
	ConversationID string `json:"conversation_id,omitempty"`
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

package protocol

// Error-code constants — wire values for the "code" field of error
// payloads (docs/protocol-mobile.md § Error codes). The naming convention
// is Code<Category><Reason>, matching the dotted-string structure
// category.reason. Grouped by category in spec-table order.
const (
	// Protocol errors.
	CodeProtocolUnknownType = "protocol.unknown_type"
	CodeProtocolMalformed   = "protocol.malformed"
	CodeProtocolUnsupported = "protocol.unsupported"

	// Auth errors.
	CodeAuthInvalidToken = "auth.invalid_token"
	CodeAuthTokenRevoked = "auth.token_revoked"

	// Server errors.
	CodeServerBinaryOffline = "server.binary_offline"
	CodeServerBinaryBusy    = "server.binary_busy"

	// Conversation errors.
	CodeConversationNotFound        = "conversation.not_found"
	CodeConversationAlreadyPromoted = "conversation.already_promoted"

	// Message errors.
	CodeMessageTooLong = "message.too_long"

	// Relay errors.
	CodeRelayNoServer         = "relay.no_server"
	CodeRelayServerIDConflict = "relay.server_id_conflict"
)

// Envelope-type constants — wire values for Envelope.Type
// (docs/protocol-mobile.md § Message types). The set is closed in v1; new
// types require a v2 envelope per the protocol's versioning policy.
const (
	// Handshake and control.
	TypeHello    = "hello"
	TypeHelloAck = "hello_ack"
	TypeError    = "error"
	TypeAck      = "ack"

	// Messaging.
	TypeSendMessage = "send_message"
	TypeMessage     = "message"

	// Conversations.
	TypeListConversations   = "list_conversations"
	TypeConversations       = "conversations"
	TypeCreateConversation  = "create_conversation"
	TypeConversationCreated = "conversation_created"
	TypePromoteConversation = "promote_conversation"
	TypeConversationUpdated = "conversation_updated"

	// Backfill.
	TypeBackfillSince = "backfill_since"
	TypeMessageChunk  = "message_chunk"
	TypeBackfillDone  = "backfill_done"

	// Push.
	TypeRegisterPushToken = "register_push_token"
)

// Mobile Protocol v2 control-envelope types. These are NOT v1 application
// types; they MUST NOT appear in v1TypeSet (internal/protocol/envelope.go).
// The v2 session manager intercepts them at the dispatch boundary
// (internal/relay/v2session.go's dispatchAppFrame) before
// internal/dispatch.Route is called, so handler-table lookup never sees
// them.
const (
	// TypeRekeyRequest is the Mobile Protocol v2 control envelope either
	// side may emit to nudge the peer toward initiating a re-key
	// handshake (docs/protocol-mobile.md § Re-key). It is informational
	// from the binary's perspective: the binary is the IK responder per
	// ADR 024, so an inbound rekey_request takes no transport action.
	//
	// MUST NOT be added to v1TypeSet in internal/protocol/envelope.go: a
	// leak into that set would route the envelope to dispatch.Route's
	// handler chain, violating the v2 control / v1 application boundary
	// enforced by internal/relay's v2 session manager. The drift detector
	// in internal/protocol/compat_test.go partitions Type* constants
	// between v1TypeSet and v2OnlyTypes; this constant lives in the
	// latter.
	TypeRekeyRequest = "rekey_request"
)

// Mobile Protocol v2 interactive application-event types. These are
// additive, capability-gated events the binary pushes to a phone that has
// advertised the "interactive" capability (docs/protocol-mobile.md
// § Interactive events). Unlike TypeRekeyRequest (a v2 control envelope
// intercepted before dispatch.Route), these are outbound binary → phone
// application events that are never dispatched inbound — but for the
// v1/v2 partition's purpose they are equally "v2-only".
//
// MUST NOT be added to v1TypeSet in internal/protocol/envelope.go: an old
// phone receives the coarse v1 "message" fan-out, not these. The drift
// detector in internal/protocol/compat_test.go partitions Type* constants
// between v1TypeSet and v2OnlyTypes; these six live in the latter.
const (
	TypeTurnState      = "turn_state"
	TypeAssistantDelta = "assistant_delta"
	TypeToolUse        = "tool_use"
	TypeToolResult     = "tool_result"
	TypeTurnEnd        = "turn_end"
	TypeStall          = "stall"
)

// Mobile Protocol v2 screen-snapshot types. The always-available,
// parser-independent screen snapshot is the floor of ADR 025's
// safe-degradation strategy (docs/protocol-mobile.md § Screen snapshot): the
// phone asks for a one-shot text picture of the current screen, the binary
// renders it and pushes it back. The pair groups here so a reader greps
// "snapshot" and finds both adjacent with their rationale.
//
// MUST NOT be added to v1TypeSet in internal/protocol/envelope.go. Like
// TypeRekeyRequest, TypeRequestSnapshot is an inbound v2 control envelope the
// v2 session manager intercepts before dispatch.Route; a leak into v1TypeSet
// would route it to the handler chain. TypeScreenSnapshot is an outbound
// binary → phone event an old phone must never receive. The drift detector
// in internal/protocol/compat_test.go partitions Type* constants between
// v1TypeSet and v2OnlyTypes; these two live in the latter.
const (
	TypeRequestSnapshot = "request_snapshot" // phone → binary, inbound v2 control (intercepted pre-dispatch.Route)
	TypeScreenSnapshot  = "screen_snapshot"  // binary → phone, outbound v2 event (plain text only)
)

// Mobile Protocol v2 mid-turn-reconnect resync marker. When a reconnecting
// phone advertises a hello.last_event_id that has aged out of the bounded
// per-conversation event ring, the daemon emits this marker (instead of a
// partial, gap-ful replay) to tell the phone to do a full reload of the named
// conversation (#647; ADR 025 § Backpressure / replay). It carries only a
// conversation_id in an inline anonymous payload — no named payload struct,
// mirroring TypeRekeyRequest's payload-less control precedent.
//
// MUST NOT be added to v1TypeSet in internal/protocol/envelope.go: it is an
// outbound binary → phone v2 event an old phone must never receive. The drift
// detector in internal/protocol/compat_test.go partitions Type* constants
// between v1TypeSet and v2OnlyTypes; this constant lives in the latter.
const (
	TypeResync = "resync" // binary → phone, outbound v2 mid-turn-reconnect resync marker
)

// Mobile Protocol v2 session-boundary marker. When the daemon's session
// rotates (a /clear, an idle eviction, or a workspace change), it emits this
// outbound binary → phone event so the phone can construct a
// ThreadItem.SessionBoundary marker (pyrycode-mobile#336) instead of inferring
// boundaries from message fields that do not exist. The multi-field payload
// lives in SessionTransitionPayload (messaging.go): previous/new session id,
// the transition reason, when it occurred, and the workspace cwd.
//
// MUST NOT be added to v1TypeSet in internal/protocol/envelope.go: it is an
// outbound binary → phone v2 event an old phone must never receive. The drift
// detector in internal/protocol/compat_test.go partitions Type* constants
// between v1TypeSet and v2OnlyTypes; this constant lives in the latter.
//
// This ticket (#656) is wire vocabulary only — the producer that emits the
// marker on session transitions is sibling #657.
const (
	TypeSessionTransition = "session_transition" // binary → phone, outbound v2 session-boundary marker
)

// Mobile Protocol v2 modal vocabulary (epic #597 Phase 3,
// docs/protocol-mobile.md § Modal). When the supervised claude surfaces a modal
// (a permission prompt, a plan-approval, a tool-confirmation), the daemon
// describes it to the phone, the phone answers, and the daemon drives that
// answer back into claude. modal_shown rides the existing "interactive"
// capability (#607) negotiated in hello/hello_ack — viewing a modal is ungated;
// answering is gated separately, per-device, default OFF, in the security model
// (#702), which is NOT a wire capability.
//
// Two natures in one cluster. modal_shown / modal_dismissed are outbound
// binary → phone events an old phone must never receive. modal_answer /
// modal_cancel are inbound phone → binary *control* envelopes the v2 session
// manager intercepts at internal/relay/v2session.go's dispatchAppFrame before
// internal/dispatch.Route (like TypeRekeyRequest / TypeRequestSnapshot); there
// is NO dispatch.Route handler for them.
//
// MUST NOT be added to v1TypeSet in internal/protocol/envelope.go: a leak would
// either route an inbound control envelope to the handler chain or offer an
// outbound modal event to an old phone, violating the v1/v2 boundary. The drift
// detector in internal/protocol/compat_test.go partitions Type* constants
// between v1TypeSet and v2OnlyTypes; these four live in the latter.
//
// This ticket (#701) is wire vocabulary only — the producer that mints modal_id
// nonces, dedups answers by answer_token, validates inbound answers, and gates
// the fan-out is sibling #703 (with #706 building two-heads ownership and #702
// the per-device answer gate).
const (
	TypeModalShown     = "modal_shown"     // binary → phone, outbound v2 modal-surfaced event
	TypeModalAnswer    = "modal_answer"    // phone → binary, inbound v2 control (intercepted pre-dispatch.Route)
	TypeModalCancel    = "modal_cancel"    // phone → binary, inbound v2 control (intercepted pre-dispatch.Route)
	TypeModalDismissed = "modal_dismissed" // binary → phone, outbound v2 modal-resolution event
)

// Mobile Protocol v2 queued-backlog vocabulary (epic #597 Phase 3,
// docs/protocol-mobile.md § Queue). A phone that types while claude is busy has
// its turn buffered in internal/msgqueue; queue_state (daemon → phone) is the
// wire form of msgqueue.Snapshot(convID) so the phone can see the backlog, and
// dequeue_message (phone → daemon) drives msgqueue.Remove(convID, id) so the
// phone can cancel an entry it no longer wants.
//
// Two natures in one cluster. queue_state is an outbound binary → phone event
// an old phone must never receive. dequeue_message is an inbound phone → binary
// *control* envelope the v2 session manager intercepts at
// internal/relay/v2session.go's dispatchAppFrame before internal/dispatch.Route
// (like TypeModalAnswer / TypeRequestSnapshot); there is NO dispatch.Route
// handler for it.
//
// Trust contrast with the modal cluster: unlike modal_answer, dequeuing is
// ungated for any paired phone (ADR 025 § Security model) — viewing and
// dequeuing are an ordinary capability, with no nonce and no per-device gate.
// queued_msg_id is a plain per-conversation counter from internal/msgqueue, not
// a security primitive.
//
// MUST NOT be added to v1TypeSet in internal/protocol/envelope.go: a leak would
// either route the inbound control envelope to the handler chain or offer the
// outbound queue_state event to an old phone, violating the v1/v2 boundary. The
// drift detector in internal/protocol/compat_test.go partitions Type* constants
// between v1TypeSet and v2OnlyTypes; these two live in the latter.
//
// This ticket (#720) is wire vocabulary only — the producer that emits
// queue_state is sibling #722 and the handler that applies dequeue_message is
// sibling #723.
const (
	TypeQueueState     = "queue_state"     // binary → phone, outbound v2 queued-backlog snapshot
	TypeDequeueMessage = "dequeue_message" // phone → binary, inbound v2 control (intercepted pre-dispatch.Route)
)

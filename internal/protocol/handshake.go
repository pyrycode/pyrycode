package protocol

import "time"

// CapabilityInteractive is the wire vocabulary string a phone advertises in
// its hello.payload.capabilities to opt into the v2 interactive event
// stream, and that the daemon echoes in hello_ack.payload.capabilities when
// it supports it (docs/protocol-mobile.md § Capability negotiation). This
// is pure vocabulary — the trust decision (the daemon intersecting the
// phone's advertised set with its own supported set, never blindly
// mirroring the phone's claims) lives in the consumer, #608, not here.
const CapabilityInteractive = "interactive"

// CapabilityQuestion is the wire vocabulary string a client advertises in its
// hello.payload.capabilities to say it understands inbound question batches,
// and that the daemon echoes in hello_ack.payload.capabilities when it supports
// them (docs/protocol-mobile.md § Capability negotiation). Because the daemon
// echoes only the intersection with its own supported set, a client detects
// question support by advertising this string and reading it back: a daemon
// built before #2020 drops it, which is the stale-daemon signal a cross-repo
// test needs when a commit sha gives it no ordering.
//
// Detection only — this string grants no access. The interactive event stream,
// the TypeQuestionShown/TypeQuestionDismissed fan-out and the connect-time
// question reconcile all gate on CapabilityInteractive alone, so a client
// advertising only this one is negotiated as non-interactive and receives none
// of them.
// Like its neighbour this is pure vocabulary; the trust decision lives in the
// consumer, internal/relay, not here.
const CapabilityQuestion = "question"

// CapabilityModelList is the wire vocabulary string a client advertises in its
// hello.payload.capabilities to say it can ask for a conversation's model menu
// and render the answer, and that the daemon echoes in
// hello_ack.payload.capabilities when it supports them
// (docs/protocol-mobile.md § Capability negotiation). It detects the #2124 +
// #2125 pair as one unit: #2124's daemon-wide retained-vocabulary fallback and
// #2125's TypeRequestModelList verb. Neither is separately useful — the
// fallback with no verb gives a client no way to ask, and the verb with no
// fallback has nothing to answer for a conversation whose bound session has
// spawned no child — so one string covers both. A daemon built before them
// drops it in the intersection, which is the stale-daemon signal a cross-repo
// test needs when a commit sha gives it no ordering.
//
// Detection only — this string grants no access. The verb gates on
// CapabilityInteractive alone, exactly as it did before this string existed, so
// a client advertising only this one is negotiated as non-interactive and
// reaches none of it; and a client advertising interactive without this string
// is answered exactly as before. Gating on it would cut off pyrycode-mobile,
// which advertises interactive only (ADR 037 makes gating a per-feature
// decision, not a default).
// Like its neighbours this is pure vocabulary; the trust decision lives in the
// consumer, internal/relay, not here.
const CapabilityModelList = "model_list"

// CapabilityContextUsage is the wire vocabulary string a client advertises in its
// hello.payload.capabilities to say it can ask for a conversation's context
// breakdown on demand and render the answer, and that the daemon echoes in
// hello_ack.payload.capabilities when it supports the verb
// (docs/protocol-mobile.md § Capability negotiation). It detects #2431's
// TypeRequestContextUsage. A daemon built before it drops the string in the
// intersection, which is the stale-daemon signal a cross-repo test needs when a
// commit sha gives it no ordering.
//
// IT DETECTS THE ASK, NOT THE FRAME. A client learns from this string only that it
// may REQUEST a reading; it says nothing about receiving one. The automatic
// post-turn context_usage frame (#2371) has shipped on the interactive lane since
// before this string existed and keeps arriving for a client that never advertises
// it — so a client must already handle the frame regardless, and gating its renderer
// on this capability would blank the reading it was receiving before.
//
// Detection only — this string grants no access. The verb gates on
// CapabilityInteractive alone, exactly as CapabilityModelList's verb does, so a
// client advertising only this one is negotiated as non-interactive and reaches none
// of it; and a client advertising interactive WITHOUT this string is answered
// exactly as before. Gating on it would cut off pyrycode-mobile, which advertises
// interactive only (ADR 037 makes gating a per-feature decision, not a default).
// Like its neighbours this is pure vocabulary; the trust decision lives in the
// consumer, internal/relay, not here.
const CapabilityContextUsage = "context_usage"

// HelloServerPayload is the body of a "hello" envelope sent by the binary
// after WS upgrade (docs/protocol-mobile.md § Message types). Role is
// always "server".
type HelloServerPayload struct {
	Role             string   `json:"role"`
	ServerID         string   `json:"server_id"`
	BinaryVersion    string   `json:"binary_version"`
	ProtocolVersions []string `json:"protocol_versions"`
}

// HelloClientPayload is the body of a "hello" envelope sent by the phone
// after WS upgrade (docs/protocol-mobile.md § Message types). Role is
// always "client". LastSeenTS is optional and has NO CONSUMER — it is decoded
// here and read by nothing, in internal/relay or anywhere else, so it triggers
// no backfill and a hello carrying it behaves identically to one omitting it
// (#2090). Kept because it is still accepted
// wire vocabulary — a decoder that rejected it would be wrong — and because
// whether real history should exist is #2091's decision, not this type's. A
// reconnecting phone that wants the tail it missed sends LastEventID below.
//
// Token is the in-band carrier of the device-pairing token under v2
// (docs/protocol-mobile.md § Authentication, line 420). Empty under v1
// (carried as RoutingEnvelope.Token instead); the omitempty keeps v1
// round-trip byte-identical for existing fixtures. SECURITY: Token is
// plaintext credential material — MUST NOT be logged at any level.
//
// Capabilities is the phone's advertised feature set (e.g.
// [CapabilityInteractive]); omitempty so a phone advertising nothing
// round-trips byte-identically with the v1 hello shape (the key is absent,
// not null). The daemon intersecting this with its own supported set is
// #608's job — this field is the advertisement only, no enforcement here.
//
// LastEventID is the durable, daemon-wide-unique event id the phone last saw on
// the interactive structured stream (the event_id #649 stamps on Envelope).
// A reconnecting phone advertises it so the daemon can replay the missed tail
// from the in-memory event ring or signal a resync (#647). A pointer +
// omitempty so a phone advertising none round-trips byte-identically with
// today's hello (key absent, not null); ring ids are always >= 1 so a non-nil
// pointer never encodes 0. SECURITY: untrusted remote input — the consumer
// (internal/relay) range/shape-validates it and bounds replay by the ring;
// this wire-type layer does no enforcement.
type HelloClientPayload struct {
	Role             string     `json:"role"`
	DeviceName       string     `json:"device_name"`
	ClientVersion    string     `json:"client_version"`
	ProtocolVersions []string   `json:"protocol_versions"`
	LastSeenTS       *time.Time `json:"last_seen_ts,omitempty"`
	Token            string     `json:"token,omitempty"`
	Capabilities     []string   `json:"capabilities,omitempty"`
	LastEventID      *uint64    `json:"last_event_id,omitempty"`
}

// HelloAckPayload is the body of a "hello_ack" envelope sent in response
// to "hello" (docs/protocol-mobile.md § Message types). ConnID echoes the
// relay-assigned id back to the phone for diagnostics only.
//
// Capabilities is the daemon's supported feature set echoed back to the
// phone; omitempty so a daemon advertising nothing round-trips
// byte-identically with the v1 hello_ack shape. The daemon MUST echo only
// what it itself supports (the intersection with the phone's advertised
// set, never a blind mirror of the phone's claims) — that trust decision
// is #608's, not this wire-type layer's.
//
// WorkspaceRoot is the daemon host's absolute ~/pyry-workspace/ base for
// resolving relative workspace paths. It is omitted when the daemon cannot
// determine an absolute home directory or the peer is not authorized to
// receive host metadata. The producer reports the convention only; it does not
// create or inspect the path.
type HelloAckPayload struct {
	ProtocolVersion string   `json:"protocol_version"`
	ServerID        string   `json:"server_id"`
	ConnID          string   `json:"conn_id"`
	Capabilities    []string `json:"capabilities,omitempty"`
	WorkspaceRoot   string   `json:"workspace_root,omitempty"`
}

// ErrorPayload is the body of an "error" envelope (docs/protocol-mobile.md
// § Message types, § Error codes). RetryAfterS is optional and advisory;
// it is meaningful only when Retryable is true.
//
// ConversationID names the conversation an error is ABOUT, and it exists for the
// replies whose subject in_reply_to cannot identify (#2443). Most error replies
// answer a request that named its own subject, so the client already knows what
// the refusal is about and the field is omitted — new_session is the exception
// that minted it: a bare frame names no conversation and rotates the daemon's
// cursor one, so only the daemon knows which conversation the answer describes.
// Omitted (omitempty) by every other reply, which keeps their wire shape
// byte-identical to the pre-#2443 one.
//
// SECURITY: the value is DAEMON-AUTHORED and MUST NOT be an echo of a
// client-supplied id — the discipline V2SessionConfig's RunConfigFor and
// ModelListFor already state for their own reported ids. A producer sets it from
// the daemon's own registry or cursor, never from the request it is answering;
// echoing would make an error reply a mirror for arbitrary remote bytes.
//
// MinClientVersion is the three-part minimum (e.g. "1.4.0") the host holds for
// the requesting app, carried by CodeClientUpdateRequired alone (#2576) so the
// message can stay static. Omitted when unset, which keeps every other error
// reply byte-identical, and omitted on that code too when the hello's
// client_version could not be parsed, since the daemon then cannot tell which
// app's minimum applies. Daemon-authored from the configured minimum, never an
// echo of the client's client_version.
type ErrorPayload struct {
	Code             string `json:"code"`
	Message          string `json:"message"`
	Retryable        bool   `json:"retryable"`
	RetryAfterS      *int   `json:"retry_after_s,omitempty"`
	ConversationID   string `json:"conversation_id,omitempty"`
	MinClientVersion string `json:"min_client_version,omitempty"`
}

// AckPayload is the body of a generic "ack" envelope; empty by spec
// (docs/protocol-mobile.md § Message types).
type AckPayload struct{}

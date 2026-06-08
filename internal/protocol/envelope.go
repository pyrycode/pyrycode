// Package protocol declares the wire-format types for pyrycode's mobile
// WebSocket protocol v1. The package is pure data: no I/O, no socket
// handling, no context plumbing. Consumers (internal/relay-client,
// internal/dispatch, future cmd/pyry-relay) marshal/unmarshal these types
// against the wire and dispatch on Envelope.Type.
//
// The single source of truth for field names, optionality, and wire
// semantics is docs/protocol-mobile.md. When that document changes,
// this package changes; the test fixtures under testdata/ pin the
// round-trip shape.
package protocol

import (
	"encoding/json"
	"errors"
	"time"
)

// Envelope is the outer wire shape every application frame conforms to
// (docs/protocol-mobile.md § Message envelope). The Payload field carries
// the per-type body as a deferred-decode json.RawMessage; per-type structs
// land in a sibling ticket and slot in via a second-pass json.Unmarshal.
type Envelope struct {
	ID        uint64          `json:"id"`
	Type      string          `json:"type"`
	TS        time.Time       `json:"ts"`
	Payload   json.RawMessage `json:"payload"`
	InReplyTo *uint64         `json:"in_reply_to,omitempty"`

	// EventID is the durable, per-conversation event id (eventring) the
	// interactive structured stream stamps so a phone can advertise it as
	// last_event_id on reconnect. Distinct from ID (the per-conn envelope
	// counter that resets each reconnect). A pointer + omitempty so every
	// non-interactive / v1 construction site stays byte-identical: absent,
	// not 0. Ring ids are always >= 1, so a non-nil pointer never encodes 0.
	// Set only by the interactive emitter (#649); consumed by #647.
	EventID *uint64 `json:"event_id,omitempty"`

	PayloadEncrypted bool `json:"payload_encrypted,omitempty"`
}

// RoutingEnvelope wraps an Envelope with the relay-prepended conn_id used
// on the binary↔relay leg only (docs/protocol-mobile.md § Routing
// envelope). Phones never see it; the relay strips it before forwarding
// frames to phones and prepends it before forwarding frames to the binary.
//
// Frame is json.RawMessage so the relay can splice without parsing
// payloads — a structural property of the design (the relay holds zero
// per-user state).
//
// Token and CloseCode are direction-restricted relay-prepended fields,
// both omitempty so existing fixtures and wire bytes for non-auth /
// non-close paths are byte-identical to the pre-#308 shape.
type RoutingEnvelope struct {
	ConnID string          `json:"conn_id"`
	Frame  json.RawMessage `json:"frame"`

	// Token carries the phone's device-pairing token from the relay to
	// the binary on the FIRST phone→binary frame for a given ConnID
	// only. Empty on subsequent frames and on every binary→phone frame.
	// Populated by the relay from the phone's x-pyrycode-token HTTP
	// header at WS upgrade; never echoed back to the phone. Wire spec:
	// docs/protocol-mobile.md § Routing envelope.
	//
	// SECURITY: the binary's dispatcher and gate closure MUST NOT log
	// Token at any level. The token is plaintext credential material;
	// AuthenticateFirstFrame is the only consumer.
	Token string `json:"token,omitempty"`

	// CloseCode, when non-zero on a binary→relay routing envelope, asks
	// the relay to forward Frame (if non-empty) to the phone and then
	// close that phone's WS with this WS close code. Zero on every
	// phone→binary frame; the dispatcher ignores CloseCode on inbound
	// (phone→binary) frames. Wire spec: docs/protocol-mobile.md
	// § Routing envelope, § Error codes (close-code row 4401).
	CloseCode uint16 `json:"close_code,omitempty"`
}

// Sentinel errors returned by IsV1Compatible. Callers (the future WS
// dispatch layer) distinguish refusal cases via errors.Is and map each
// sentinel to its dotted-string wire code at the call site:
//
//	ErrUnknownType  -> CodeProtocolUnknownType
//	ErrUnsupported  -> CodeProtocolUnsupported
var (
	ErrUnknownType = errors.New("protocol: unknown envelope type")
	ErrUnsupported = errors.New("protocol: unsupported envelope feature")
)

// IsV1Compatible reports whether env is acceptable under wire-protocol v1.
// Returns nil when env.Type is in the v1 type set and env.PayloadEncrypted
// is false. Returns ErrUnsupported when env.PayloadEncrypted is true
// (reserved for v2; docs/protocol-mobile.md § Reserved for v2). Returns
// ErrUnknownType when env.Type is empty or not in the v1 set.
//
// Check order: PayloadEncrypted first, Type second. A frame failing both
// checks reports as ErrUnsupported — the stricter rejection wins.
//
// IsV1Compatible does not validate Payload contents, ID monotonicity, or
// TS skew; those are dispatcher concerns.
func IsV1Compatible(env Envelope) error {
	if env.PayloadEncrypted {
		return ErrUnsupported
	}
	if !v1TypeSet[env.Type] {
		return ErrUnknownType
	}
	return nil
}

// v1TypeSet is the closed enumeration of envelope types accepted by
// wire-protocol v1. Mobile Protocol v2 control types (e.g.
// TypeRekeyRequest) MUST NOT be added here: the v2 session manager
// (internal/relay/v2session.go) intercepts them at its dispatch boundary
// before internal/dispatch.Route consults this set. Adding a v2-control
// constant would silently route it to the handler chain — see
// internal/protocol/compat_test.go for the partition enforcement.
var v1TypeSet = map[string]bool{
	TypeHello:               true,
	TypeHelloAck:            true,
	TypeError:               true,
	TypeAck:                 true,
	TypeSendMessage:         true,
	TypeMessage:             true,
	TypeListConversations:   true,
	TypeConversations:       true,
	TypeCreateConversation:  true,
	TypeConversationCreated: true,
	TypePromoteConversation: true,
	TypeConversationUpdated: true,
	TypeBackfillSince:       true,
	TypeMessageChunk:        true,
	TypeBackfillDone:        true,
	TypeRegisterPushToken:   true,
}

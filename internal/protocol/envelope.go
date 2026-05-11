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
	ID               uint64          `json:"id"`
	Type             string          `json:"type"`
	TS               time.Time       `json:"ts"`
	Payload          json.RawMessage `json:"payload"`
	InReplyTo        *uint64         `json:"in_reply_to,omitempty"`
	PayloadEncrypted bool            `json:"payload_encrypted,omitempty"`
}

// RoutingEnvelope wraps an Envelope with the relay-prepended conn_id used
// on the binary↔relay leg only (docs/protocol-mobile.md § Routing
// envelope). Phones never see it; the relay strips it before forwarding
// frames to phones and prepends it before forwarding frames to the binary.
//
// Frame is json.RawMessage so the relay can splice without parsing
// payloads — a structural property of the design (the relay holds zero
// per-user state).
type RoutingEnvelope struct {
	ConnID string          `json:"conn_id"`
	Frame  json.RawMessage `json:"frame"`
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

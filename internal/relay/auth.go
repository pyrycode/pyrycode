package relay

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/coder/websocket"

	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// StatusUnauthorized is the WS close code the relay sends when the binary
// rejects a phone's device token (docs/protocol-mobile.md § Error codes,
// close-code row 4401). Typed locally so callers don't import the
// websocket package for the value; parallel to statusServerIDConflict in
// connection.go.
const StatusUnauthorized websocket.StatusCode = 4401

// MsgInvalidToken is the fixed user-facing message emitted in the
// auth.invalid_token error payload (docs/protocol-mobile.md § Error codes).
// Defined as an exported const so future integration tests can pin it
// without re-typing the spec sentence.
const MsgInvalidToken = "device token not recognised; re-pair via pyry pair on the binary"

// ErrMalformedHelloFrame is returned by AuthenticateFirstFrame when the
// inner envelope inside env.Frame cannot be JSON-decoded. The relay-conn
// caller maps this to its existing protocol.malformed handling; this
// package does not synthesize an error envelope for it (the malformed-
// frame response shape is owned by a sibling ticket).
var ErrMalformedHelloFrame = errors.New("relay: malformed hello frame")

// AuthOutcome is the structured result of AuthenticateFirstFrame. The
// relay-conn caller forwards Response back through the binary→relay leg
// and, when CloseConn is true, asks the relay to close the phone WS with
// StatusUnauthorized after the Response has been written.
//
// CloseConn carries the protocol-level intent (close-because-auth-failed)
// rather than the wire code itself; the close code is fixed at 4401 by
// spec and lives on the StatusUnauthorized constant.
type AuthOutcome struct {
	Response  protocol.RoutingEnvelope
	CloseConn bool
}

// AuthenticateFirstFrame is the per-connection token-validation predicate
// for the phone→relay→binary auth phase (docs/protocol-mobile.md §
// Authentication). It is invoked exactly once per phone conn — on the
// binary's receipt of the first frame for a given conn_id — and returns
// the routing envelope the binary should send back plus the close-or-keep
// signal the relay-conn layer will act on.
//
// The function is carrier-agnostic with respect to how token reached the
// binary: it never parses WS headers, never reads env.Frame's payload for
// a token field, and never inspects a hello payload. The relay-conn
// ticket that wires this handler into phone traffic picks one of
// (a) extended routing envelope, (b) synthesized connection_opened
// control frame, or (c) amended hello payload, and forwards the
// extracted token here unchanged.
//
// Two-state semantics: reg.Validate returns (Device, bool). A true result
// means the device row exists and was just bumped (the predicate already
// handled LastSeenAt under reg.mu — the handler does not call any further
// mutator). A false result means either never-paired or the row was
// removed via reg.Remove; both produce the same auth.invalid_token
// outcome per docs/protocol-mobile.md § Error codes line 535 ("Same UX as
// invalid_token"). The CodeAuthTokenRevoked constant is reserved for a
// future tombstone primitive and is NOT emitted by this handler.
//
// SECURITY: token is never logged, never wrapped into any returned error,
// and never echoed to the phone. The matched device's name IS logged on
// accept but is NOT logged on reject (preventing name-enumeration probes
// from binary logs).
func AuthenticateFirstFrame(
	env protocol.RoutingEnvelope,
	token string,
	reg *devices.Registry,
	serverID string,
	logger *slog.Logger,
) (AuthOutcome, error) {
	var inner protocol.Envelope
	if err := json.Unmarshal(env.Frame, &inner); err != nil {
		return AuthOutcome{}, ErrMalformedHelloFrame
	}
	helloID := inner.ID

	device, ok := reg.Validate(token)
	if ok {
		resp, err := buildResponse(env.ConnID, helloID, protocol.TypeHelloAck, protocol.HelloAckPayload{
			ProtocolVersion: "v1",
			ServerID:        serverID,
			ConnID:          env.ConnID,
		})
		if err != nil {
			return AuthOutcome{}, err
		}
		logger.Info("relay: auth accept",
			"event", "auth.accept",
			"conn_id", env.ConnID,
			"device_name", device.Name)
		return AuthOutcome{Response: resp, CloseConn: false}, nil
	}

	resp, err := buildResponse(env.ConnID, helloID, protocol.TypeError, protocol.ErrorPayload{
		Code:      protocol.CodeAuthInvalidToken,
		Message:   MsgInvalidToken,
		Retryable: false,
	})
	if err != nil {
		return AuthOutcome{}, err
	}
	logger.Warn("relay: auth reject",
		"event", "auth.reject",
		"conn_id", env.ConnID,
		"code", protocol.CodeAuthInvalidToken)
	return AuthOutcome{Response: resp, CloseConn: true}, nil
}

// buildResponse marshals payload, wraps it in an Envelope with the
// supplied type and in_reply_to, then wraps that envelope in a
// RoutingEnvelope addressed to connID. ID is fixed at 1: this is always
// the binary's first outbound frame on the phone's conn (subsequent
// envelopes on the same conn are allocated by the relay-conn layer from a
// per-conn counter starting at 2).
func buildResponse(connID string, helloID uint64, envType string, payload any) (protocol.RoutingEnvelope, error) {
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return protocol.RoutingEnvelope{}, fmt.Errorf("marshal %s payload: %w", envType, err)
	}
	envelope := protocol.Envelope{
		ID:        1,
		Type:      envType,
		TS:        time.Now().UTC(),
		Payload:   payloadJSON,
		InReplyTo: &helloID,
	}
	envJSON, err := json.Marshal(envelope)
	if err != nil {
		return protocol.RoutingEnvelope{}, fmt.Errorf("marshal %s envelope: %w", envType, err)
	}
	return protocol.RoutingEnvelope{
		ConnID: connID,
		Frame:  envJSON,
	}, nil
}

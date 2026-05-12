// Package handlers implements per-envelope-type processors for the
// binary's inbound phone-traffic dispatch. Each handler is a pure
// function: routing envelope in, routing envelope out (plus side effects
// on the devices/conversations registries it is passed). Handlers know
// payload semantics; the dispatcher (future internal/relay) owns conn
// state, per-conn id allocation, and conn lifecycle.
package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// msgUnauthorized is the user-facing message emitted in the
// auth.invalid_token error payload when a register_push_token frame
// arrives on a conn that has no authenticated Device. Should be
// unreachable in production once the dispatcher honours auth state; the
// handler emits a coherent envelope for the (dispatcher-bug) case.
const msgUnauthorized = "not authenticated; handshake required before register_push_token"

// msgBinaryBusy is the user-facing message emitted in the
// server.binary_busy error payload when registry persistence fails. The
// phone retries; on the retry, dedupe will succeed (in-memory is already
// updated) and no further write attempt occurs.
const msgBinaryBusy = "registry save in progress; retry"

// ErrMalformedFrame is returned by Handle when routing.Frame cannot be
// JSON-decoded as a protocol.Envelope, or when the inner payload cannot
// be decoded as RegisterPushTokenPayload. The dispatcher maps this to
// its existing protocol.malformed handling (a sibling ticket owns the
// response shape); this handler does not synthesize an error envelope
// for it.
var ErrMalformedFrame = errors.New("handlers: malformed register_push_token frame")

// Handle processes a register_push_token frame from the phone and
// returns the routing envelope to send back through the binary→relay
// leg. The frame's inner Envelope.Type is assumed to be
// TypeRegisterPushToken (the dispatcher already type-dispatched); Handle
// does not re-verify.
//
// `routing` is the inbound RoutingEnvelope wrapping the phone's frame.
// Handle decodes routing.Frame into a protocol.Envelope (for the
// in_reply_to echo) and decodes its payload into a
// RegisterPushTokenPayload.
//
// `device` is the authenticated phone's Device entry — the snapshot the
// relay-conn layer cached at first-frame auth (the value returned by
// AuthenticateFirstFrame). A nil pointer means the dispatcher routed an
// unauthenticated conn into this handler; Handle responds with an
// auth.invalid_token error envelope and does NOT touch the registry.
//
// `reg` and `registryPath` are passed through to UpdatePushRegistration
// and Save when the dedupe check fails (i.e. the triple is different).
// The handler never touches registryPath on the dedupe path.
//
// `nextID` is the envelope id the dispatcher allocated for this response
// from its per-conn counter (auth's hello_ack used id 1; this is id ≥ 2).
// The handler stamps it onto every envelope it builds (ack, error).
//
// SECURITY:
//   - The push token is opaque infrastructure data (FCM/APNs
//     registration id); not a secret on par with the device token. It is
//     logged at INFO level for traceability when a write happens, and at
//     DEBUG when dedupe skips the write. The phone-side device token
//     (used at auth) is NEVER read or logged by this handler.
//   - The Device snapshot's name IS logged on every path (write, dedupe,
//     unauth-reject). Unauth-reject is safe to name-log here because the
//     "no device" path is the only one without a name — there is nothing
//     to enumerate.
//
// Concurrency: the handler is stateless. reg's mutex serializes
// UpdatePushRegistration and Save (independently); two concurrent calls
// for the same TokenHash interleave at the mutex boundary, and the
// "last writer wins" memory state is whichever lock acquisition was
// later. Disk-level last-writer-wins is enforced by Save's atomic
// rename.
func Handle(
	routing protocol.RoutingEnvelope,
	device *devices.Device,
	reg *devices.Registry,
	registryPath string,
	nextID uint64,
	logger *slog.Logger,
) (protocol.RoutingEnvelope, error) {
	var inner protocol.Envelope
	if err := json.Unmarshal(routing.Frame, &inner); err != nil {
		return protocol.RoutingEnvelope{}, ErrMalformedFrame
	}
	requestID := inner.ID

	if device == nil {
		resp, err := wrap(routing.ConnID, requestID, nextID, protocol.TypeError, protocol.ErrorPayload{
			Code:      protocol.CodeAuthInvalidToken,
			Message:   msgUnauthorized,
			Retryable: false,
		})
		if err != nil {
			return protocol.RoutingEnvelope{}, err
		}
		logger.Warn("relay: register_push_token unauth",
			"event", "register_push_token.unauth",
			"conn_id", routing.ConnID,
			"code", protocol.CodeAuthInvalidToken)
		return resp, nil
	}

	var payload protocol.RegisterPushTokenPayload
	if err := json.Unmarshal(inner.Payload, &payload); err != nil {
		return protocol.RoutingEnvelope{}, ErrMalformedFrame
	}

	if payload.Platform == device.Platform &&
		payload.Token == device.PushToken &&
		payload.DeviceName == device.Name {
		resp, err := wrap(routing.ConnID, requestID, nextID, protocol.TypeAck, protocol.AckPayload{})
		if err != nil {
			return protocol.RoutingEnvelope{}, err
		}
		logger.Debug("relay: register_push_token dedupe",
			"event", "register_push_token.dedupe",
			"conn_id", routing.ConnID,
			"device_name", device.Name)
		return resp, nil
	}

	if ok := reg.UpdatePushRegistration(device.TokenHash, payload.Platform, payload.Token, payload.DeviceName); !ok {
		resp, err := wrap(routing.ConnID, requestID, nextID, protocol.TypeError, protocol.ErrorPayload{
			Code:      protocol.CodeAuthInvalidToken,
			Message:   msgUnauthorized,
			Retryable: false,
		})
		if err != nil {
			return protocol.RoutingEnvelope{}, err
		}
		logger.Warn("relay: register_push_token device gone mid-conn",
			"event", "register_push_token.gone_mid_conn",
			"conn_id", routing.ConnID,
			"device_name", device.Name)
		return resp, nil
	}

	if err := reg.Save(registryPath); err != nil {
		resp, wrapErr := wrap(routing.ConnID, requestID, nextID, protocol.TypeError, protocol.ErrorPayload{
			Code:      protocol.CodeServerBinaryBusy,
			Message:   msgBinaryBusy,
			Retryable: true,
		})
		if wrapErr != nil {
			return protocol.RoutingEnvelope{}, wrapErr
		}
		logger.Warn("relay: register_push_token save failed",
			"event", "register_push_token.save_failed",
			"conn_id", routing.ConnID,
			"device_name", device.Name,
			"err", err)
		return resp, nil
	}

	resp, err := wrap(routing.ConnID, requestID, nextID, protocol.TypeAck, protocol.AckPayload{})
	if err != nil {
		return protocol.RoutingEnvelope{}, err
	}
	logger.Info("relay: register_push_token write",
		"event", "register_push_token.write",
		"conn_id", routing.ConnID,
		"device_name", payload.DeviceName,
		"platform", payload.Platform)
	return resp, nil
}

// wrap marshals payload, wraps it in an Envelope with the supplied type,
// in_reply_to, and id, then wraps that envelope in a RoutingEnvelope
// addressed to connID. Mirrors the buildResponse helper in
// internal/relay/auth.go; reproduced here rather than imported to keep
// the handlers sub-package free of an internal/relay dependency.
func wrap(connID string, inReplyTo uint64, nextID uint64, envType string, payload any) (protocol.RoutingEnvelope, error) {
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return protocol.RoutingEnvelope{}, fmt.Errorf("marshal %s payload: %w", envType, err)
	}
	envelope := protocol.Envelope{
		ID:        nextID,
		Type:      envType,
		TS:        time.Now().UTC(),
		Payload:   payloadJSON,
		InReplyTo: &inReplyTo,
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

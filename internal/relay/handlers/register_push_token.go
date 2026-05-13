package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/dispatch"
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

// msgMalformed is the user-facing message emitted in the
// protocol.malformed error payload when RegisterPushTokenPayload cannot
// be JSON-decoded. The decode-error text is NOT echoed back (it could
// reflect attacker-controlled payload bytes); only this static string.
const msgMalformed = "malformed register_push_token payload"

// RegisterPushToken returns a dispatch.Handler that processes a
// register_push_token frame from the phone. reg is the devices registry;
// registryPath is the canonical on-disk path passed to Save; logger is
// the daemon's slog logger used for every branch's structured event.
//
// SECURITY:
//   - The push token (p.Token, dev.PushToken) is opaque infrastructure
//     data (FCM/APNs registration id); not a secret on par with the
//     device auth token. It is logged at INFO when a write happens (as
//     a side-effect of the write event) and never as a field value at
//     any level.
//   - The Device.Name is logged on every authenticated branch. The
//     unauth branch has no name to log; that is the only path without it.
//
// Concurrency: the handler is stateless beyond the closure capture.
// reg's mutex serialises UpdatePushRegistration and Save independently;
// two concurrent calls for the same TokenHash interleave at the mutex
// boundary with documented last-writer-wins semantics.
func RegisterPushToken(reg *devices.Registry, registryPath string, logger *slog.Logger) dispatch.Handler {
	return func(ctx context.Context, c *dispatch.Conn, env protocol.Envelope) error {
		dev := c.Auth()
		if dev == nil {
			logger.Warn("relay: register_push_token unauth",
				"event", "register_push_token.unauth",
				"conn_id", c.ConnID(),
				"code", protocol.CodeAuthInvalidToken)
			return replyError(ctx, c, env, protocol.CodeAuthInvalidToken, msgUnauthorized, false)
		}

		var p protocol.RegisterPushTokenPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			logger.Warn("relay: register_push_token malformed payload",
				"event", "register_push_token.malformed",
				"conn_id", c.ConnID(),
				"device_name", dev.Name,
				"err", err)
			return replyError(ctx, c, env, protocol.CodeProtocolMalformed, msgMalformed, false)
		}

		if p.Platform == dev.Platform && p.Token == dev.PushToken && p.DeviceName == dev.Name {
			logger.Debug("relay: register_push_token dedupe",
				"event", "register_push_token.dedupe",
				"conn_id", c.ConnID(),
				"device_name", dev.Name)
			return replyAck(ctx, c, env)
		}

		if ok := reg.UpdatePushRegistration(dev.TokenHash, p.Platform, p.Token, p.DeviceName); !ok {
			logger.Warn("relay: register_push_token device gone mid-conn",
				"event", "register_push_token.gone_mid_conn",
				"conn_id", c.ConnID(),
				"device_name", dev.Name)
			return replyError(ctx, c, env, protocol.CodeAuthInvalidToken, msgUnauthorized, false)
		}

		if err := reg.Save(registryPath); err != nil {
			logger.Warn("relay: register_push_token save failed",
				"event", "register_push_token.save_failed",
				"conn_id", c.ConnID(),
				"device_name", dev.Name,
				"err", err)
			return replyError(ctx, c, env, protocol.CodeServerBinaryBusy, msgBinaryBusy, true)
		}

		logger.Info("relay: register_push_token write",
			"event", "register_push_token.write",
			"conn_id", c.ConnID(),
			"device_name", p.DeviceName,
			"platform", p.Platform)
		return replyAck(ctx, c, env)
	}
}

func replyAck(ctx context.Context, c *dispatch.Conn, env protocol.Envelope) error {
	payload, err := json.Marshal(protocol.AckPayload{})
	if err != nil {
		return fmt.Errorf("marshal ack payload: %w", err)
	}
	return c.Reply(ctx, env, protocol.TypeAck, payload)
}

func replyError(ctx context.Context, c *dispatch.Conn, env protocol.Envelope, code, message string, retryable bool) error {
	payload, err := json.Marshal(protocol.ErrorPayload{
		Code:      code,
		Message:   message,
		Retryable: retryable,
	})
	if err != nil {
		return fmt.Errorf("marshal error payload: %w", err)
	}
	return c.Reply(ctx, env, protocol.TypeError, payload)
}

package handlers

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// ClaudeAccountSource reports the daemon's Claude account source as wire data.
// It cannot trigger a read: the reply is the outcome of the latest read the
// daemon made on its own.
type ClaudeAccountSource interface {
	ClaudeAccount() protocol.ClaudeAccountPayload
}

// RequestClaudeAccount answers request_claude_account with one claude_account
// to the requester. A failed source read is reported in-band by the payload's
// state, never as an error frame.
func RequestClaudeAccount(src ClaudeAccountSource, logger *slog.Logger) dispatch.Handler {
	return func(ctx context.Context, c *dispatch.Conn, env protocol.Envelope) error {
		var p protocol.RequestClaudeAccountPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			logger.Warn("relay: request_claude_account refused", "event", "request_claude_account.malformed", "conn_id", c.ConnID())
			return replyError(ctx, c, env, protocol.CodeProtocolMalformed, "malformed request_claude_account payload", false)
		}
		payload, err := json.Marshal(src.ClaudeAccount())
		if err != nil {
			return err
		}
		return c.Reply(ctx, env, protocol.TypeClaudeAccount, payload)
	}
}

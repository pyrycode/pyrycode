package handlers

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// ClaudeUsageSource reports the daemon's latest Claude usage allowance reading
// as wire data. It cannot trigger a model call: the reply is whatever claude's
// most recent rate_limit_event carried.
type ClaudeUsageSource interface {
	ClaudeUsage() protocol.ClaudeUsagePayload
}

// RequestClaudeUsage answers request_claude_usage with one claude_usage to the
// requester. A nil src answers state none, as a daemon with no reading does.
func RequestClaudeUsage(src ClaudeUsageSource, logger *slog.Logger) dispatch.Handler {
	return func(ctx context.Context, c *dispatch.Conn, env protocol.Envelope) error {
		var p protocol.RequestClaudeUsagePayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			logger.Warn("relay: request_claude_usage refused", "event", "request_claude_usage.malformed", "conn_id", c.ConnID())
			return replyError(ctx, c, env, protocol.CodeProtocolMalformed, "malformed request_claude_usage payload", false)
		}
		reading := protocol.ClaudeUsagePayload{State: protocol.ClaudeUsageStateNone}
		if src != nil {
			reading = src.ClaudeUsage()
		}
		payload, err := json.Marshal(reading)
		if err != nil {
			return err
		}
		return c.Reply(ctx, env, protocol.TypeClaudeUsage, payload)
	}
}

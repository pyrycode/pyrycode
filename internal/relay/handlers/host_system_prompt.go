package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

// HostSystemPromptStore exposes only the daemon-wide durable instructions door.
// It cannot look up, restart, rotate or interrupt a conversation session.
type HostSystemPromptStore interface {
	DaemonInstructions() string
	DefaultDaemonInstructions() string
	SetDaemonInstructions(string) error
}

// RequestHostSystemPrompt reads the paired daemon's current and reset values.
func RequestHostSystemPrompt(store HostSystemPromptStore, logger *slog.Logger) dispatch.Handler {
	return func(ctx context.Context, c *dispatch.Conn, env protocol.Envelope) error {
		var p protocol.RequestHostSystemPromptPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			logger.Warn("relay: request_host_system_prompt refused", "event", "request_host_system_prompt.malformed", "conn_id", c.ConnID())
			return replyError(ctx, c, env, protocol.CodeProtocolMalformed, "malformed request_host_system_prompt payload", false)
		}
		return replyHostSystemPrompt(ctx, c, env, store)
	}
}

// SetHostSystemPrompt acknowledges only after the pool has persisted the value.
func SetHostSystemPrompt(store HostSystemPromptStore, logger *slog.Logger) dispatch.Handler {
	return func(ctx context.Context, c *dispatch.Conn, env protocol.Envelope) error {
		var p protocol.SetHostSystemPromptPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil || p.SystemPrompt == nil {
			logger.Warn("relay: set_host_system_prompt refused", "event", "set_host_system_prompt.malformed", "conn_id", c.ConnID())
			return replyError(ctx, c, env, protocol.CodeProtocolMalformed, "malformed set_host_system_prompt payload", false)
		}
		if err := store.SetDaemonInstructions(*p.SystemPrompt); err != nil {
			code, message, event, retryable := protocol.CodeHostSystemPromptUnavailable, "host system prompt unavailable", "set_host_system_prompt.persist_failed", true
			switch {
			case errors.Is(err, sessions.ErrDaemonInstructionsTooLong):
				code, message, event, retryable = protocol.CodeProtocolMalformed, "system prompt exceeds the maximum length", "set_host_system_prompt.too_long", false
			case errors.Is(err, sessions.ErrDaemonInstructionsInvalidUTF8):
				code, message, event, retryable = protocol.CodeProtocolMalformed, "system prompt is not valid UTF-8", "set_host_system_prompt.invalid_utf8", false
			}
			// Parser and store errors can carry private text; only static metadata is logged.
			logger.Warn("relay: set_host_system_prompt refused", "event", event, "conn_id", c.ConnID())
			return replyError(ctx, c, env, code, message, retryable)
		}
		return replyHostSystemPrompt(ctx, c, env, store)
	}
}

func replyHostSystemPrompt(ctx context.Context, c *dispatch.Conn, env protocol.Envelope, store HostSystemPromptStore) error {
	payload, err := json.Marshal(protocol.HostSystemPromptPayload{
		SystemPrompt: store.DaemonInstructions(), DefaultSystemPrompt: store.DefaultDaemonInstructions(),
	})
	if err != nil {
		return err
	}
	return c.Reply(ctx, env, protocol.TypeHostSystemPrompt, payload)
}

package main

import (
	"context"
	"log/slog"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// channelPostTurnEndPayload reports a completed daemon-authored host post.
// Claude's result fields deliberately do not belong to this payload.
type channelPostTurnEndPayload struct {
	ConversationID string `json:"conversation_id"`
	TurnID         string `json:"turn_id"`
	StopReason     string `json:"stop_reason"`
	Producer       string `json:"producer"`
}

// channelPostEmitterV2 wakes absent devices after durable completion.
// Conversation content is delivered only by the canonical thread publisher.
type channelPostEmitterV2 struct {
	waker *pushWaker
}

func newChannelPostEmitterV2(_ interactiveBroadcaster, _ context.Context, _ *slog.Logger) *channelPostEmitterV2 {
	return &channelPostEmitterV2{}
}

// announce preserves the relay constructor seam without publishing content.
func (e *channelPostEmitterV2) announce(protocol.AssistantDeltaPayload) {}

// complete is called only for a newly recorded durable completion.
func (e *channelPostEmitterV2) complete(p channelPostTurnEndPayload) {
	e.waker.Trigger(p.ConversationID, pushWakeTurnEnd)
}

package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/pyrycode/pyrycode/internal/eventring"
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

// channelPostEmitterV2 publishes durably completed posts at the serialized
// delivery boundary. It shares the interactive emitter's bounded replay ring
// and waker without touching that emitter's single-writer lifecycle state.
// Content is carried only in history and encrypted client envelopes, never logs.
type channelPostEmitterV2 struct {
	bcast  interactiveBroadcaster // *relay.V2SessionManager (ActiveConns/Push)
	ctx    context.Context        // daemon ctx captured at construction, for broadcasts
	logger *slog.Logger
	ring   *eventring.Ring
	waker  *pushWaker

	// mu is a leaf lock guarding nextID and NOTHING else: held around the
	// counter bump alone, never across ActiveConns or a Push, so it can never
	// nest inside the manager's own pushMu and there is no lock order to reason
	// about. The sole channelDelivery consumer calls announce serially; the lock
	// retains the emitter's counter safety for direct callers as well.
	mu sync.Mutex
	// nextID is the per-emitter envelope-ID counter, the shape every v2 emitter
	// in this package uses — V2SessionManager.Push never rewrites Envelope.ID, so
	// each producer numbers its own frames.
	nextID uint64
}

// newChannelPostEmitterV2 builds the announcer over the relay's interactive
// fan-out surface. ctx is the daemon context: once it is cancelled ActiveConns
// answers empty and a racing push returns its error, so a late post fans out to
// nobody rather than blocking teardown.
func newChannelPostEmitterV2(bcast interactiveBroadcaster, ctx context.Context, logger *slog.Logger) *channelPostEmitterV2 {
	return &channelPostEmitterV2{bcast: bcast, ctx: ctx, logger: logger, ring: eventring.New(eventring.MaxEventsPerConversation)}
}

// announce receives precisely the delta recorded by channelDelivery.
func (e *channelPostEmitterV2) announce(p protocol.AssistantDeltaPayload) {
	e.emit(p.ConversationID, protocol.TypeAssistantDelta, p)
}

// complete fans out the post completion before asking the existing waker to
// reach absent devices. Reading the replay ring never calls this method.
func (e *channelPostEmitterV2) complete(p channelPostTurnEndPayload) {
	e.emit(p.ConversationID, protocol.TypeTurnEnd, p)
	e.waker.Trigger(p.ConversationID, pushWakeTurnEnd)
}

func (e *channelPostEmitterV2) emit(convID, typ string, p any) {
	payloadJSON, err := json.Marshal(p)
	if err != nil {
		// Both payload types are closed structs and cannot fail to marshal. Never echo the payload or
		// err.Error() — encoding/json can quote input bytes into its error, and one
		// of those fields is conversation content.
		e.logger.Warn("relay: channel-post drop; payload marshal",
			"event", "channel_post.marshal_err",
			"conversation_id", convID)
		return
	}

	ctx := e.ctx
	ts := time.Now().UTC()
	eventID := e.ring.Append(convID, typ, payloadJSON, ts)
	for _, c := range e.bcast.ActiveConns(ctx) {
		if !c.Interactive {
			continue // the #607 capability gate — v2 turn events ride interactive
		}
		e.mu.Lock()
		e.nextID++
		id := e.nextID
		e.mu.Unlock()
		env := protocol.Envelope{
			ID:      id,
			Type:    typ,
			EventID: &eventID,
			TS:      ts,
			Payload: payloadJSON,
		}
		if err := e.bcast.Push(ctx, c.ConnID, env); err != nil {
			if ctx.Err() != nil {
				return // teardown
			}
			// A conn that closed between the snapshot above and this push, which
			// Push answers with the ErrConnNotFound sentinel. Logged and skipped,
			// never fatal — and the client that missed it reads the post out of the
			// conversation's durable log or shared replay ring on reconnect.
			e.logger.Debug("relay: channel-post push dropped",
				"event", "channel_post.push_err",
				"conversation_id", convID,
				"conn_id", c.ConnID,
				"env_id", id,
				"err", err)
		}
	}
}

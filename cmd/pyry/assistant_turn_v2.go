package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/supervisor"
)

// v2Broadcaster is the minimal *relay.V2SessionManager surface the v2
// assistant-turn emitter needs: the capability-aware open-session snapshot
// (#626) and the per-conn sealed push (#571). *relay.V2SessionManager
// satisfies it (structurally identical to interactiveBroadcaster).
//
// The coarse `message` path now fans only to NON-interactive conns — the
// exact complement of #632's interactive-only structured filter. An
// interactive-granted conn gets the structured stream instead and never the
// coarse message, so the two paths are mutually exclusive per conn.
// Declared at the consumer (CODING-STYLE) so the emitter unit tests can
// drive it without spinning up a real manager.
type v2Broadcaster interface {
	ActiveConns(ctx context.Context) []relay.ActiveConn
	Push(ctx context.Context, connID string, env protocol.Envelope) error
}

// assistantTurnEmitterV2 is the v2 (Noise) analog of assistantTurnEmitter:
// it owns the buffered queue between Bridge.Write and the broadcast
// goroutine, copies PTY chunks on Enqueue, and drains them by reading the
// supervisor's conversation cursor and fanning a `message` envelope out to
// every currently-open NON-interactive v2 session via the manager's
// ActiveConns/Push funnel (interactive conns get the #632 structured stream
// instead). The application-layer minting (cursor read → message ID → payload
// marshal) is identical to the v1 emitter; only the per-conn delivery
// differs (a sealed Push per open conn, not dispatch.Conn.Send).
//
// SECURITY: chunk bytes (PTY output) are NEVER logged at any level — not in
// WARN, not in DEBUG, not via wrapped errors. Logs carry only chunk_len,
// conversation_id, message_id, and conn_id. The chunk reaches the phone via
// MessagePayload.Text verbatim, sealed under the session's send CipherState.
type assistantTurnEmitterV2 struct {
	sup    cursorReader
	bcast  v2Broadcaster
	logger *slog.Logger

	in chan []byte

	// nextID is the caller-side envelope-ID counter (#589 § Envelope-ID
	// policy). v2 has no per-session outbound counter, so the bridge mints
	// monotonic env.IDs here. Read/written only on the single Run goroutine
	// (broadcast is serial) — no atomic needed. MessageID (a fresh UUIDv4
	// per chunk) is the phone's dedup/ordering key; env.ID is an envelope
	// sequence hint, not load-bearing on v2, and may collide with the
	// dispatch reply path's env.ID on the same session.
	nextID uint64
}

// newAssistantTurnEmitterV2 constructs an emitter wired to sup and bcast.
// Run must be called once on a goroutine before Enqueue takes effect.
func newAssistantTurnEmitterV2(sup cursorReader, bcast v2Broadcaster, logger *slog.Logger) *assistantTurnEmitterV2 {
	return &assistantTurnEmitterV2{
		sup:    sup,
		bcast:  bcast,
		logger: logger,
		in:     make(chan []byte, assistantTurnQueueSize),
	}
}

// Enqueue is the Bridge output-observer call site. Byte-identical to the v1
// emitter: copies p (the supervisor's io.Copy buffer is reused on the next
// Read) and drops on a full queue with a WARN carrying only chunk_len.
func (e *assistantTurnEmitterV2) Enqueue(p []byte) {
	chunk := make([]byte, len(p))
	copy(chunk, p)
	select {
	case e.in <- chunk:
	default:
		e.logger.Warn("relay: assistant-turn queue full; dropping chunk",
			"event", "assistant_turn.queue_full",
			"chunk_len", len(p))
	}
}

// Run drains the queue until ctx is cancelled or in is closed. For each
// chunk: read the cursor; if empty, drop. Otherwise build a message
// envelope and Push it to every currently-open v2 conn.
func (e *assistantTurnEmitterV2) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case chunk, ok := <-e.in:
			if !ok {
				return
			}
			e.broadcast(ctx, chunk)
		}
	}
}

// broadcast handles one chunk: cursor read, payload marshal, then a fresh
// open-session snapshot and a sealed Push per conn. A per-conn Push error is
// logged at DEBUG and the loop continues — a dropped conn must not abort the
// turn for the others (AC#2). ctx-cancel mid-fan-out returns early.
func (e *assistantTurnEmitterV2) broadcast(ctx context.Context, chunk []byte) {
	convID := e.sup.CurrentConversation()
	if convID == "" {
		e.logger.Debug("relay: assistant-turn drop; no cursor",
			"event", "assistant_turn.no_cursor",
			"chunk_len", len(chunk))
		return
	}

	msgID, err := conversations.NewID()
	if err != nil {
		// crypto/rand failure — defensive; drop the chunk.
		e.logger.Warn("relay: assistant-turn drop; rand failure",
			"event", "assistant_turn.rand_err",
			"chunk_len", len(chunk),
			"conversation_id", convID,
			"err", err)
		return
	}
	payload := protocol.MessagePayload{
		ConversationID: convID,
		MessageID:      string(msgID),
		Role:           "assistant",
		Text:           string(chunk),
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		// MessagePayload is a closed struct of strings; marshal cannot fail
		// in practice. Defensive — never echo err.Error() (could quote
		// chunk bytes back if the input were invalid UTF-8).
		e.logger.Warn("relay: assistant-turn drop; payload marshal",
			"event", "assistant_turn.marshal_err",
			"chunk_len", len(chunk),
			"conversation_id", convID,
			"message_id", string(msgID))
		return
	}

	// Fresh snapshot per chunk: a phone that opens its session between
	// chunks is included next round; one that dropped is absent here, or
	// surfaces as a Push error below.
	for _, c := range e.bcast.ActiveConns(ctx) {
		if c.Interactive {
			continue // structured-stream conns get the #632 path, never the coarse message
		}
		e.nextID++
		env := protocol.Envelope{
			ID:      e.nextID,
			Type:    protocol.TypeMessage,
			TS:      time.Now().UTC(),
			Payload: payloadJSON,
		}
		if err := e.bcast.Push(ctx, c.ConnID, env); err != nil {
			if ctx.Err() != nil {
				return
			}
			e.logger.Debug("relay: assistant-turn push dropped",
				"event", "assistant_turn.push_err",
				"conn_id", c.ConnID,
				"conversation_id", convID,
				"message_id", string(msgID),
				"err", err)
		}
	}
}

// startAssistantTurnBridgeV2 wires an emitter to the bridge's output
// observer and the v2 manager's fan-out surface. Returns a cleanup that
// clears the observer and waits for Run to exit on ctx-cancel. Idempotent.
//
// Mirrors startAssistantTurnBridge (v1). The observer runs on the
// supervisor's PTY-drain goroutine; Enqueue is non-blocking (drop-on-full)
// so the drain never wedges. Cleanup does NOT close the input channel —
// Bridge.Write captures the observer under bridge.mu and releases the lock
// before invoking it, so a Write racing cleanup could call Enqueue after we
// cleared the observer; sending to a closed channel would panic. We rely on
// ctx cancellation to drain Run.
//
// PTY chunks (PTY → MessagePayload.Text → phone) are NEVER logged at any
// level. See assistantTurnEmitterV2 for the per-branch log surface.
//
// Foreground mode: when bridge == nil this function MUST NOT be called;
// startRelayV2 gates the call site.
func startAssistantTurnBridgeV2(
	ctx context.Context,
	sup cursorReader,
	bridge *supervisor.Bridge,
	bcast v2Broadcaster,
	logger *slog.Logger,
) func() {
	emitter := newAssistantTurnEmitterV2(sup, bcast, logger)
	bridge.SetOutputObserver(emitter.Enqueue)

	done := make(chan struct{})
	go func() {
		defer close(done)
		emitter.Run(ctx)
	}()

	var cleanedUp bool
	return func() {
		if cleanedUp {
			return
		}
		cleanedUp = true
		bridge.SetOutputObserver(nil)
		<-done
	}
}

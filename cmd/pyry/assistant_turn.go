package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/supervisor"
)

// assistantTurnQueueSize bounds the buffered chunk channel between the
// PTY-drain producer and the broadcast consumer. Sized for a small burst;
// drop-on-full keeps the producer non-blocking.
const assistantTurnQueueSize = 16

// cursorReader is the minimal supervisor surface the emitter needs:
// CurrentConversation() — the cursor stamped by send_message via
// Supervisor.WriteUserTurn (#312/#322). Declared as an interface so the
// emitter unit tests can drive it without a real *supervisor.Supervisor.
type cursorReader interface {
	CurrentConversation() string
}

// connBroadcaster is the minimal dispatcher surface the emitter needs.
// Exists so the per-chunk fan-out can be unit-tested without spinning up
// a full Dispatcher.
type connBroadcaster interface {
	ActiveConns() []*dispatch.Conn
}

// assistantTurnEmitter owns the buffered queue between Bridge.Write and
// the broadcast goroutine. PTY chunks are copied on Enqueue (the
// supervisor reuses the slice) and drained by Run, which reads the
// supervisor's conversation cursor and fans an envelope out to every
// currently-active phone conn.
//
// SECURITY: chunk bytes (PTY output) are NEVER logged at any level —
// not in WARN, not in DEBUG, not via wrapped errors. Logs carry only
// chunk_len, conversation_id, and message_id. The chunk reaches the
// phone via MessagePayload.Text verbatim; rendering / escape handling
// is the phone's responsibility.
type assistantTurnEmitter struct {
	sup    cursorReader
	disp   connBroadcaster
	logger *slog.Logger

	in chan []byte
}

// newAssistantTurnEmitter constructs an emitter wired to sup and disp.
// Run must be called once on a goroutine before Enqueue takes effect.
func newAssistantTurnEmitter(sup cursorReader, disp connBroadcaster, logger *slog.Logger) *assistantTurnEmitter {
	return &assistantTurnEmitter{
		sup:    sup,
		disp:   disp,
		logger: logger,
		in:     make(chan []byte, assistantTurnQueueSize),
	}
}

// Enqueue is the Bridge output-observer call site. Copies p before
// queueing — the supervisor's io.Copy buffer is reused on the next
// Read — and drops on a full queue with a WARN log.
func (e *assistantTurnEmitter) Enqueue(p []byte) {
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
// chunk: read the supervisor's cursor; if empty, drop. Otherwise build a
// message envelope and Send it to every currently-active conn.
func (e *assistantTurnEmitter) Run(ctx context.Context) {
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

// broadcast handles one chunk: cursor read, payload marshal, fan-out to
// every conn returned by ActiveConns. Per-conn Send errors are logged
// at DEBUG (transport disconnects are normal — same posture as the
// existing forwarder).
func (e *assistantTurnEmitter) broadcast(ctx context.Context, chunk []byte) {
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
		// MessagePayload is a closed struct of strings; marshal cannot
		// fail in practice. Defensive — never echo err.Error() (could
		// quote chunk bytes back if the input were invalid UTF-8).
		e.logger.Warn("relay: assistant-turn drop; payload marshal",
			"event", "assistant_turn.marshal_err",
			"chunk_len", len(chunk),
			"conversation_id", convID,
			"message_id", string(msgID))
		return
	}

	conns := e.disp.ActiveConns()
	for _, c := range conns {
		env := protocol.Envelope{
			ID:      c.NextID(),
			Type:    protocol.TypeMessage,
			TS:      time.Now().UTC(),
			Payload: payloadJSON,
		}
		if err := c.Send(ctx, env); err != nil {
			if ctx.Err() != nil {
				return
			}
			e.logger.Debug("relay: assistant-turn send dropped",
				"event", "assistant_turn.send_err",
				"conn_id", c.ConnID(),
				"conversation_id", convID,
				"message_id", string(msgID),
				"err", err)
		}
	}
}

// startAssistantTurnBridge wires an emitter to the bridge's output
// observer and the dispatcher's outbound surface. Returns a cleanup that
// clears the observer and waits for Run to exit on ctx-cancel. Idempotent.
//
// The observer runs on the supervisor's PTY-drain goroutine; Enqueue is
// non-blocking (drop-on-full) so the drain never wedges. Cleanup does NOT
// close the input channel — Bridge.Write captures the observer under
// bridge.mu and releases the lock before invoking it, so a Write racing
// with cleanup could call Enqueue after we cleared the observer; sending
// to a closed channel would panic. We rely on ctx cancellation to drain
// Run and let the buffered chunks be GC'd with the emitter.
//
// PTY chunks (PTY → MessagePayload.Text → phone) are NEVER logged at any
// level — neither in WARN nor DEBUG nor wrapped in errors. See
// assistantTurnEmitter for the per-branch log surface.
//
// Foreground mode: when bridge == nil this function MUST NOT be called;
// startRelay gates the call site.
func startAssistantTurnBridge(
	ctx context.Context,
	sup *supervisor.Supervisor,
	bridge *supervisor.Bridge,
	disp *dispatch.Dispatcher,
	logger *slog.Logger,
) func() {
	emitter := newAssistantTurnEmitter(sup, disp, logger)
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

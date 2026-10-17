package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// claudeUsageQueueSize bounds the readings waiting for the fan-out. Readings
// arrive once per model call and a fan-out is a snapshot plus non-blocking
// enqueues, so the buffer fills only if Run is wedged; a reading dropped then is
// superseded by the next model call's.
const claudeUsageQueueSize = 16

// claudeUsageEmitterV2 pushes each new Claude usage allowance reading to every
// open v2 connection as an uncorrelated claude_usage frame, the same payload
// request_claude_usage answers. A buffered channel decouples the store's
// onRecord hook, which runs on a Claude session's stdout forwarder and must not
// block, from the Run goroutine that takes the blocking ActiveConns snapshot and
// pushes.
//
// The push is live-only: it carries no event_id, joins no replay ring and names
// no conversation, so a reconnecting client is never replayed one and reads the
// current reading by asking. There is no interactive gate, as the request has
// none: the reading belongs to the account and every authenticated connection
// may read it. Clients that do not know the type drop it.
//
// No log line carries the payload.
type claudeUsageEmitterV2 struct {
	in     chan protocol.ClaudeUsagePayload
	logger *slog.Logger
	// nextID numbers this producer's envelopes. Push never rewrites Envelope.ID.
	// Read and written only on Run.
	nextID uint64
}

func newClaudeUsageEmitterV2(logger *slog.Logger) *claudeUsageEmitterV2 {
	return &claudeUsageEmitterV2{in: make(chan protocol.ClaudeUsagePayload, claudeUsageQueueSize), logger: logger}
}

// notify hands one reading to Run without blocking; a full buffer drops it.
// Safe for concurrent use.
func (e *claudeUsageEmitterV2) notify(p protocol.ClaudeUsagePayload) {
	select {
	case e.in <- p:
	default:
		e.logger.Debug("relay: claude-usage push queue full; dropping reading",
			"event", "claude_usage.queue_full")
	}
}

// Run pushes each handed-off reading until ctx is cancelled.
func (e *claudeUsageEmitterV2) Run(ctx context.Context, bcast interactiveBroadcaster) {
	for {
		select {
		case <-ctx.Done():
			return
		case p := <-e.in:
			e.broadcast(ctx, bcast, p)
		}
	}
}

// broadcast pushes one reading to every open connection. The envelope has no
// in_reply_to: nothing solicited it. A failed push is logged and skipped so the
// other connections still receive theirs.
func (e *claudeUsageEmitterV2) broadcast(ctx context.Context, bcast interactiveBroadcaster, p protocol.ClaudeUsagePayload) {
	payloadJSON, err := json.Marshal(p)
	if err != nil {
		// Unreachable: the payload is a string, a time and finite numbers.
		e.logger.Debug("relay: claude-usage drop; payload marshal",
			"event", "claude_usage.marshal_err")
		return
	}
	ts := time.Now().UTC()
	for _, c := range bcast.ActiveConns(ctx) {
		e.nextID++
		env := protocol.Envelope{
			ID:      e.nextID,
			Type:    protocol.TypeClaudeUsage,
			TS:      ts,
			Payload: payloadJSON,
		}
		if err := bcast.Push(ctx, c.ConnID, env); err != nil {
			if ctx.Err() != nil {
				return // teardown
			}
			e.logger.Debug("relay: claude-usage push dropped",
				"event", "claude_usage.push_err",
				"conn_id", c.ConnID,
				"env_id", e.nextID,
				"err", err)
		}
	}
}

// startClaudeUsageStreamV2 pushes every reading feed records from now on to
// bcast's connections. The returned cleanup detaches the feed and waits for the
// Run goroutine, which exits on ctx. A nil feed starts nothing.
func startClaudeUsageStreamV2(ctx context.Context, feed *claudeUsageStore, bcast interactiveBroadcaster, logger *slog.Logger) func() {
	if feed == nil {
		return func() {}
	}
	e := newClaudeUsageEmitterV2(logger)
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.Run(ctx, bcast)
	}()
	feed.setOnRecord(e.notify)

	var cleanedUp bool
	return func() {
		if cleanedUp {
			return
		}
		cleanedUp = true
		feed.setOnRecord(nil)
		<-done
	}
}

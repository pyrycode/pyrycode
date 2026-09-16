package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// channelPostEmitterV2 carries a message the HOST posted (#2498) to paired
// clients: once `pyry channel post` has recorded the content, one assistant_delta
// per chunk reaches every interactive client and draws there without a
// reconnect. Before this, the record existed only in the conversation's durable
// log and no connected client learned of it — which for a cron's daily question
// meant the operator heard hours late, on their next app restart.
//
// IT IS THE SECOND PRODUCER OF assistant_delta, and the first that is not a
// supervised claude turn. interactiveTurnEmitterV2 is the other one; that lane
// derives its turn lifecycle from claude's own stream, while this one reports a
// message the daemon was handed. Both produce the identical wire type on purpose:
// assistant_delta is what the current clients draw as assistant text, live and
// from a served history page alike, and no client change is in this slice's
// scope. That dual nature is recorded in docs/protocol-mobile.md's v2 type table,
// because nothing in the build goes red if it is not.
//
// THE FRAME IS assistant_delta AND NOTHING ELSE — no turn_state, no turn_end.
// A delta alone renders, and a fresh turn_id starts a fresh bubble, so neither
// is needed for the content to appear. Both would also be claims this verb
// cannot make: TurnEndPayload carries four claude-authored strings and four
// numbers that no claude reported here, and a channel is an ordinary bound
// conversation in which the operator can be mid-turn — so a synthetic end would
// close a turn actually in flight and a synthetic state would assert a lifecycle
// edge the post did not cause.
//
// The fan-out is conversationUpdateEmitterV2.announce's, copied deliberately
// rather than by resemblance — that is the frame with the same origin (a
// host-side control verb, broadcast from the control-server handler goroutine),
// so it is the one whose delivery semantics a client already reasons about. One
// shared timestamp per frame, the #607 interactive capability gate, a monotonic
// envelope id, and a push loop that tolerates a torn-down conn.
//
// LIVE-ONLY, like the conversation update and the attachment offer: no
// outstanding-post registry and no connect-time replay. The difference from both
// is that this frame HAS a durable half — channelPoster writes the same payload
// to the conversation's log before calling here — and that is what makes
// live-only acceptable for a type that is also the single droppable class in
// pushQueue.enqueue and convRing.evictOldest. A client that was disconnected, or
// whose delta was evicted under backpressure, reads the post out of history.
//
// NO EventID, which is the one field this emitter deliberately leaves unset
// where interactiveTurnEmitterV2.emit sets it. That id names a position in an
// eventring, this emitter owns no ring, and minting into the turn emitter's ring
// from here would thread that emitter across the composition root to buy a
// replay path the durable log already covers.
//
// SECURITY: no log line here carries the payload's Text, on any branch. It is
// conversation content and the durable log is the one place it may be written —
// channelPoster's own discipline, kept here because this is where the content
// reaches the wire. Seq is not logged either: across a chunked post it is a
// proxy for the message's length. The push-error line names the conversation id,
// the conn and the transport sentinel instead.
type channelPostEmitterV2 struct {
	bcast  interactiveBroadcaster // *relay.V2SessionManager (ActiveConns/Push)
	ctx    context.Context        // daemon ctx captured at construction, for broadcasts
	logger *slog.Logger

	// mu is a leaf lock guarding nextID and NOTHING else: held around the
	// counter bump alone, never across ActiveConns or a Push, so it can never
	// nest inside the manager's own pushMu and there is no lock order to reason
	// about. Load-bearing rather than copied, for conversationUpdateEmitterV2's
	// reason: control.Server.Serve accepts each conn onto its own goroutine, so
	// two crons can post concurrently.
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
	return &channelPostEmitterV2{bcast: bcast, ctx: ctx, logger: logger}
}

// announce fans ONE assistant_delta envelope to every interactive-capable conn.
// Runs synchronously on the control-server handler goroutine servicing
// channel.post — the shape channelCreator's announcement already runs in — and is
// bounded there: ActiveConns is a snapshot under the manager's own lock and Push
// enqueues without blocking, so a wedged phone cannot hold the verb open.
//
// It returns nothing, and that is the contract rather than an omission: a failed
// announcement must not turn a recorded post into a refusal. The content is in
// the conversation's durable log whether or not any client heard about it, so the
// verb still exits 0 — which is what a cron reads.
//
// ONE PAYLOAD PER CALL, and a post larger than maxDeltaTextBytes calls here once
// per chunk. The split lives in channelPoster because that is the one place that
// also writes the chunks to the log, and a split decided in two places is two
// places it can diverge. The per-chunk ActiveConns snapshot that follows is
// interactiveTurnEmitterV2.flushDelta's own behaviour, not a new exposure: a conn
// that joins mid-post is included from the next chunk on, and one that leaves
// surfaces as the Push error below.
//
// The envelope carries NO in_reply_to, which is what makes this a push rather
// than a reply: nothing solicited it, so there is no request envelope for it to
// name. Correlation is the payload's own conversation_id and turn_id.
//
// p is the payload as RECORDED, handed over by the poster after the durable write
// succeeded. Announcing anything reassembled here would be a second derivation of
// a value that must be one.
func (e *channelPostEmitterV2) announce(p protocol.AssistantDeltaPayload) {
	payloadJSON, err := json.Marshal(p)
	if err != nil {
		// Defensive: AssistantDeltaPayload is a closed struct of three strings and
		// an int and cannot fail to marshal in practice. Never echo the payload or
		// err.Error() — encoding/json can quote input bytes into its error, and one
		// of those fields is conversation content.
		e.logger.Warn("relay: channel-post drop; payload marshal",
			"event", "channel_post.marshal_err",
			"conversation_id", p.ConversationID)
		return
	}

	ctx := e.ctx
	ts := time.Now().UTC()
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
			Type:    protocol.TypeAssistantDelta,
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
			// conversation's durable log on its next connect, which is why this lane
			// needs no re-sync path of its own.
			e.logger.Debug("relay: channel-post push dropped",
				"event", "channel_post.push_err",
				"conversation_id", p.ConversationID,
				"conn_id", c.ConnID,
				"env_id", id,
				"err", err)
		}
	}
}

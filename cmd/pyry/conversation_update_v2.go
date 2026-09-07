package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// conversationUpdateEmitterV2 tells paired clients about a conversation the
// HOST created (#2156): once `pyry channel new` has written the row, one
// conversation_updated frame reaches every interactive client, which re-lists on
// it and draws the new channel. Before this, the row existed only in the
// registry and no connected client learned of it until its next
// list_conversations — and nothing prompted one.
//
// It is the FIRST UNSOLICITED PRODUCER of a frame that until now was only ever a
// reply. All four wire producers (promote / rename / archive / change_workspace)
// answer their requester through Conn.Reply, which was right while the requester
// was the only party that needed to know. A host-side create has no requester on
// the wire, so a push is the only route to anyone. That dual nature is recorded
// in the two places that classify the type — docs/protocol-mobile.md's v2 type
// table and relay_guard_test.go's excludedTypes — because nothing in the build
// goes red if it is not.
//
// The frame is conversation_updated and NEVER conversation_created. Desktop
// treats the latter as the reply to its own create and navigates into the
// thread, so minting one here would steal the screen of every paired client the
// moment an operator ran the CLI verb.
//
// The fan-out is attachmentOfferEmitterV2.announce's, copied deliberately rather
// than by resemblance — that is the frame with the same origin (a host-side
// control verb, broadcast from the control-server handler goroutine), so it is
// the one whose delivery semantics a client already reasons about. One shared
// timestamp per announcement, the #607 interactive capability gate, a monotonic
// envelope id, and a push loop that tolerates a torn-down conn.
//
// LIVE-ONLY, like the attachment offer and unlike modal_shown: there is no
// outstanding-announcement registry and no connect-time replay. A client that
// was disconnected when a channel was created is never pushed the row — and
// needs no replay, because it re-lists on connect and sees it there.
//
// SECURITY: no log line here carries the record's Cwd or Name, on any branch.
// Both are host filesystem strings — the operator's project directory and the
// label derived from it — and channelCreator's own channel_new.created line
// already limits itself to the two ids for that reason. The push-error line
// below names the conversation id, the conn and the transport sentinel instead.
type conversationUpdateEmitterV2 struct {
	bcast  interactiveBroadcaster // *relay.V2SessionManager (ActiveConns/Push)
	ctx    context.Context        // daemon ctx captured at construction, for broadcasts
	logger *slog.Logger

	// mu is a leaf lock guarding nextID and NOTHING else: held around the
	// counter bump alone, never across ActiveConns or a Push, so it can never
	// nest inside the manager's own pushMu and there is no lock order to reason
	// about. Load-bearing rather than copied, for attachmentOfferEmitterV2's
	// reason: control.Server.Serve accepts each conn onto its own goroutine, so
	// two concurrent channel.new calls announce concurrently.
	mu sync.Mutex
	// nextID is the per-emitter envelope-ID counter, the shape every v2 emitter
	// in this package uses — V2SessionManager.Push never rewrites Envelope.ID, so
	// each producer numbers its own frames.
	nextID uint64
}

// newConversationUpdateEmitterV2 builds the announcer over the relay's
// interactive fan-out surface. ctx is the daemon context: once it is cancelled
// ActiveConns answers empty and a racing push returns its error, so a late
// announcement fans out to nobody rather than blocking teardown.
func newConversationUpdateEmitterV2(bcast interactiveBroadcaster, ctx context.Context, logger *slog.Logger) *conversationUpdateEmitterV2 {
	return &conversationUpdateEmitterV2{bcast: bcast, ctx: ctx, logger: logger}
}

// announce fans one conversation_updated envelope to every interactive-capable
// conn. Runs synchronously on the control-server handler goroutine servicing
// channel.new — the shape fileAttacher's announcement already runs in — and is
// bounded there: ActiveConns is a snapshot under the manager's own lock and Push
// enqueues without blocking, so a wedged phone cannot hold the verb open.
//
// It returns nothing, and that is the contract rather than an omission: a failed
// announcement must not turn a successful create into a refusal. The row is in
// the registry and on disk whether or not any client heard about it, so the verb
// still answers with the created id.
//
// The envelope carries NO in_reply_to, which is what makes this a push rather
// than a fifth reply — nothing solicited it, so there is no request envelope for
// it to name. Correlation is the payload's own id.
//
// p is the record as READ BACK FROM THE REGISTRY, not as assembled from the
// request. That is a security property and not just an accuracy one: the caller
// of channel.new supplies a raw, unvalidated cwd, and what the row stores is
// resolveSpawnDir's confined, symlink-resolved output. Announcing anything but
// the stored row would put the unconfined string on the wire.
func (e *conversationUpdateEmitterV2) announce(p protocol.ConversationUpdatedPayload) {
	payloadJSON, err := json.Marshal(p)
	if err != nil {
		// Defensive: ConversationUpdatedPayload is a closed struct of two strings,
		// two bools, a *string and a time.Time, and cannot fail to marshal in
		// practice. Never echo the payload or err.Error() — encoding/json can quote
		// input bytes into its error, and two of those fields name a host location.
		e.logger.Warn("relay: conversation-update drop; payload marshal",
			"event", "conversation_update.marshal_err",
			"conversation_id", p.ID)
		return
	}

	ctx := e.ctx
	ts := time.Now().UTC()
	for _, c := range e.bcast.ActiveConns(ctx) {
		if !c.Interactive {
			continue // the #607 capability gate — v2 conversation events ride interactive
		}
		e.mu.Lock()
		e.nextID++
		id := e.nextID
		e.mu.Unlock()
		env := protocol.Envelope{
			ID:      id,
			Type:    protocol.TypeConversationUpdated,
			TS:      ts,
			Payload: payloadJSON,
		}
		if err := e.bcast.Push(ctx, c.ConnID, env); err != nil {
			if ctx.Err() != nil {
				return // teardown
			}
			// A conn that closed between the snapshot above and this push, which
			// Push answers with the ErrConnNotFound sentinel. Logged and skipped,
			// never fatal — and the client that missed it re-lists on its next
			// connect, so there is no re-sync path to mention.
			e.logger.Debug("relay: conversation-update push dropped",
				"event", "conversation_update.push_err",
				"conversation_id", p.ID,
				"conn_id", c.ConnID,
				"env_id", id,
				"err", err)
		}
	}
}

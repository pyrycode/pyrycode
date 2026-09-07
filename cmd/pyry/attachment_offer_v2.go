package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// attachmentOfferEmitterV2 announces a host-produced attachment to paired
// clients (#2166): once a file claude named has been stored under a
// conversation, one attachment_offered frame tells every interactive client the
// file exists, so it can draw it in the thread and fetch it with
// request_attachment. Before this, the minted id existed only in the control
// socket reply that went back to claude, and no client could ever learn it.
//
// It is the PRODUCER HALF of a frame #2082 published with nothing emitting it.
// No wire shape is decided here: the type constant, the payload and the relay
// guard's classification all landed with that ticket.
//
// The fan-out is streamApprovalBridge.broadcast's, copied deliberately rather
// than by resemblance — that is the frame with the same origin (an MCP tool call
// from claude, over the control socket, broadcast from the control-server
// handler goroutine), so it is the one whose delivery semantics a client already
// reasons about. One shared timestamp per announcement, the #607 interactive
// capability gate, a monotonic envelope id, and a push loop that tolerates a
// torn-down conn.
//
// LIVE-ONLY, and this is the one place it differs from modal_shown. There is no
// outstanding-offer registry and no connect-time replay: a client that was
// disconnected when a file landed is never told about it. modal_shown has
// modalbridge.Snapshot because an unanswered approval BLOCKS claude and must be
// re-raised; an offer blocks nothing, so the registry would buy a growing
// daemon-side store of claude-authored strings and nothing else. Recorded in
// docs/protocol-mobile.md § Attachments so a client author does not wait for a
// replay that never comes.
//
// SECURITY: no log line here carries the filename, on any branch. It is
// claude-authored, and docs/protocol-mobile.md § Attachments bans logging a
// filename for a privacy reason that sanitising does not lift — which is why the
// push-error line below names the two ids and the transport sentinel rather than
// "the offer". The ids are the pair fileAttacher already logs: one daemon-minted
// and one shape-checked by attachments.EnsureDir before this is ever reached.
type attachmentOfferEmitterV2 struct {
	bcast  interactiveBroadcaster // *relay.V2SessionManager (ActiveConns/Push)
	ctx    context.Context        // daemon ctx captured at construction, for broadcasts
	logger *slog.Logger

	// mu is a leaf lock guarding nextID and NOTHING else: held around the
	// counter bump alone, never across ActiveConns or a Push, so it can never
	// nest inside the manager's own pushMu and there is no lock order to reason
	// about. The sibling emitters (queueStateEmitterV2, sessionErrorEmitterV2)
	// carry an unguarded counter because they run only on the relay's single Run
	// goroutine; THIS one cannot, and the mutex is load-bearing rather than
	// copied: control.Server.Serve accepts each conn onto its own goroutine, so
	// two concurrent attachment.file calls announce concurrently.
	mu sync.Mutex
	// nextID is the per-emitter envelope-ID counter, the shape every v2 emitter
	// in this package uses — V2SessionManager.Push never rewrites Envelope.ID, so
	// each producer numbers its own frames.
	nextID uint64
}

// newAttachmentOfferEmitterV2 builds the announcer over the relay's interactive
// fan-out surface. ctx is the daemon context: once it is cancelled ActiveConns
// answers empty and a racing push returns its error, so a late announcement
// fans out to nobody rather than blocking teardown.
func newAttachmentOfferEmitterV2(bcast interactiveBroadcaster, ctx context.Context, logger *slog.Logger) *attachmentOfferEmitterV2 {
	return &attachmentOfferEmitterV2{bcast: bcast, ctx: ctx, logger: logger}
}

// announce fans one attachment_offered envelope to every interactive-capable
// conn. Runs synchronously on the control-server handler goroutine servicing
// attachment.file — the same shape streamApprovalBridge.broadcast runs in for
// mcp.approve — and is bounded there: ActiveConns is a snapshot under the
// manager's own lock and Push enqueues without blocking, so a wedged phone
// cannot hold the verb open.
//
// It returns nothing, and that is the contract rather than an omission: a failed
// announcement must not turn a successful store into a refusal. The bytes are on
// disk and the id is real whether or not any client heard about it, so the verb
// still answers with the minted id.
//
// filename is the name the file was actually STORED under — the leaf of the path
// attachments.Store returned — so it is the same string the retrieval leg
// publishes for this id. Passed through verbatim here: deriving it a second way
// is what would let the announced and the retrieved name drift apart.
func (e *attachmentOfferEmitterV2) announce(conversationID, attachmentID, filename string) {
	payloadJSON, err := json.Marshal(protocol.AttachmentOfferedPayload{
		ConversationID: conversationID,
		AttachmentID:   attachmentID,
		Filename:       filename,
	})
	if err != nil {
		// Defensive: AttachmentOfferedPayload is a closed struct of three strings
		// and cannot fail to marshal in practice. Never echo the payload or
		// err.Error() — encoding/json can quote input bytes into its error, and
		// one of those three strings is the filename.
		e.logger.Warn("relay: attachment-offer drop; payload marshal",
			"event", "attachment_offer.marshal_err")
		return
	}

	ctx := e.ctx
	ts := time.Now().UTC()
	for _, c := range e.bcast.ActiveConns(ctx) {
		if !c.Interactive {
			continue // the #607 capability gate — v2 attachment events ride interactive
		}
		e.mu.Lock()
		e.nextID++
		id := e.nextID
		e.mu.Unlock()
		env := protocol.Envelope{
			ID:      id,
			Type:    protocol.TypeAttachmentOffered,
			TS:      ts,
			Payload: payloadJSON,
		}
		if err := e.bcast.Push(ctx, c.ConnID, env); err != nil {
			if ctx.Err() != nil {
				return // teardown
			}
			// A conn that closed between the snapshot above and this push, which
			// Push answers with the ErrConnNotFound sentinel. Logged and skipped,
			// never fatal — and there is no re-sync path to mention, unlike
			// modal_shown's: the offer is live-only by design.
			e.logger.Debug("relay: attachment-offer push dropped",
				"event", "attachment_offer.push_err",
				"conversation_id", conversationID,
				"attachment_id", attachmentID,
				"conn_id", c.ConnID,
				"env_id", id,
				"err", err)
		}
	}
}

package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// workspaceUpdateEmitterV2 tells the OTHER paired clients about a workspace an
// operator just renamed (#2209): once handlers.RenameWorkspace has stored the
// label and answered its requester, one workspace_updated frame reaches every
// other interactive client, so the name chosen on one machine is the name shown
// on all of them. Before this, every other client kept the old label until it
// happened to re-list, and nothing prompted one.
//
// It is the SECOND PRODUCER of a frame introduced by #2207 as a reply, which is
// why this type is now classified twice over — as a reply correlated by
// in_reply_to, and as an unsolicited push. That dual nature is recorded in the
// two places that classify the type, docs/protocol-mobile.md's v2 type table and
// relay_guard_test.go's excludedTypes, because nothing in the build goes red if
// it is not.
//
// Copied from conversationUpdateEmitterV2 deliberately rather than by
// resemblance: one shared timestamp per announcement, the #607 interactive
// capability gate, a monotonic envelope id, and a push loop that tolerates a
// torn-down conn. The one structural difference is the exclusion below — that
// emitter announces a host-side create, which has no requester on the wire, so it
// fans to everyone.
//
// LIVE-ONLY, like the conversation announcement: there is no outstanding
// -announcement registry and no connect-time replay. A client that was
// disconnected during a rename is never pushed the record — and needs no replay,
// because every list_conversations row carries the stored label (#2208), so it
// sees the new name in its next list.
//
// SECURITY: no log line here carries the record's Path or Label, on any branch,
// and unlike the conversation announcement there is NO daemon-minted identifier
// to name in their place — a workspace has no row and no id of its own. The path
// is a filesystem location on the daemon's host; the label is opaque operator
// text that arrived from a network-paired party. Both are echoed to clients as
// stored and reach nothing else. The log lines below name the daemon-minted conn
// ids, the envelope id and the transport sentinel instead, which is the strict
// posture RenameWorkspace's own logging documents at length.
type workspaceUpdateEmitterV2 struct {
	bcast  interactiveBroadcaster // *relay.V2SessionManager (ActiveConns/Push)
	ctx    context.Context        // daemon ctx captured at construction, for broadcasts
	logger *slog.Logger

	// mu is a leaf lock guarding nextID and NOTHING else: held around the
	// counter bump alone, never across ActiveConns or a Push, so it can never
	// nest inside the manager's own pushMu and there is no lock order to reason
	// about. Load-bearing rather than copied: a rename dispatches on the
	// requesting conn's own appFrameWorker goroutine, so two paired clients
	// renaming at once announce concurrently through one emitter.
	mu sync.Mutex
	// nextID is the per-emitter envelope-ID counter, the shape every v2 emitter
	// in this package uses — V2SessionManager.Push never rewrites Envelope.ID, so
	// each producer numbers its own frames.
	nextID uint64
}

// newWorkspaceUpdateEmitterV2 builds the announcer over the relay's interactive
// fan-out surface. ctx is the daemon context: once it is cancelled ActiveConns
// answers empty and a racing push returns its error, so a late announcement fans
// out to nobody rather than blocking teardown.
func newWorkspaceUpdateEmitterV2(bcast interactiveBroadcaster, ctx context.Context, logger *slog.Logger) *workspaceUpdateEmitterV2 {
	return &workspaceUpdateEmitterV2{bcast: bcast, ctx: ctx, logger: logger}
}

// announce fans one workspace_updated envelope to every interactive-capable conn
// except the one named by excludeConnID. Runs synchronously on the requesting
// conn's appFrameWorker goroutine — rename_workspace is map-dispatched, so
// dispatchAppFrame falls through to enqueueAppFrame and this never lands on Run.
// That is what makes the ActiveConns call safe here despite its doc comment's
// wording: the snapshot request funnels ONTO Run, which is free to service it
// while this goroutine waits. It is bounded too: the snapshot is taken under the
// manager's own lock and Push enqueues without blocking, so a wedged phone cannot
// hold the verb open.
//
// It returns nothing, and that is the contract rather than an omission: nothing
// about the fan-out may fail the operation the operator asked for. The label is
// stored and persisted whether or not any other client heard about it, and the
// requester's correlated reply has already gone.
//
// excludeConnID is the REQUESTER'S conn id, compared against ActiveConn.ConnID —
// the same string space dispatch.Conn.ConnID() returns, so there is no mapping to
// get wrong. Exclusion is per CONN and not per device: a requester with a second
// client open is pushed on that second conn, which asked for nothing and holds no
// reply. An empty key therefore excludes nobody rather than matching a conn, no
// ActiveConn carrying an empty id.
//
// The envelope carries NO in_reply_to, which is what makes this a push rather
// than a second reply — nothing solicited it, so there is no request envelope for
// it to name. The requester's copy is the correlated one.
//
// p is the record as the handler PROJECTED IT FROM THE MATCHED REGISTRY ROW, not
// as assembled from the request. That is a security property and not an accuracy
// one: the path a client supplies is compared byte-for-byte against stored cwds
// and then discarded, so the only path this daemon publishes is one it stored
// itself.
func (e *workspaceUpdateEmitterV2) announce(p protocol.WorkspaceUpdatedPayload, excludeConnID string) {
	payloadJSON, err := json.Marshal(p)
	if err != nil {
		// Defensive: WorkspaceUpdatedPayload is a closed struct of a string and a
		// *string and cannot fail to marshal in practice. Never echo the payload or
		// err.Error() — encoding/json can quote input bytes into its error, and both
		// of these fields are values this daemon does not log.
		e.logger.Warn("relay: workspace-update drop; payload marshal",
			"event", "workspace_update.marshal_err",
			"conn_id", excludeConnID)
		return
	}

	ctx := e.ctx
	ts := time.Now().UTC()
	for _, c := range e.bcast.ActiveConns(ctx) {
		if c.ConnID == excludeConnID {
			continue // the requester already has its correlated reply
		}
		if !c.Interactive {
			continue // the #607 capability gate — v2 workspace events ride interactive
		}
		e.mu.Lock()
		e.nextID++
		id := e.nextID
		e.mu.Unlock()
		env := protocol.Envelope{
			ID:      id,
			Type:    protocol.TypeWorkspaceUpdated,
			TS:      ts,
			Payload: payloadJSON,
		}
		if err := e.bcast.Push(ctx, c.ConnID, env); err != nil {
			if ctx.Err() != nil {
				return // teardown
			}
			// A conn that closed between the snapshot above and this push, which
			// Push answers with the ErrConnNotFound sentinel. Logged and skipped,
			// never fatal — one client's failure must not affect another's delivery,
			// and the client that missed it reads the label off its next
			// list_conversations, so there is no re-sync path to mention.
			e.logger.Debug("relay: workspace-update push dropped",
				"event", "workspace_update.push_err",
				"conn_id", c.ConnID,
				"env_id", id,
				"err", err)
		}
	}
}

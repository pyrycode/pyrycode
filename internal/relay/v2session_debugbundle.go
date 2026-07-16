package relay

import (
	"context"
	"encoding/json"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// This file holds the request_debug_bundle handling — the request handler,
// its deterministic error-reply helper, its per-conn in-flight gate, and the
// shared error-message constant — carved out of v2session.go (#1025). Pure
// move: same package, no behaviour change. The V2Session / V2SessionManager
// structs, the Run loop, the TypeRequestDebugBundle dispatch case, and the
// DebugBundler seam field on V2SessionConfig stay in v2session.go; StreamBundle
// lives in v2bundlestream.go. This is the 5th #964 slice, after #1021
// (handshake), #1022 (rekey), #1023 (modal+queue), and #1024 (settings).

// msgDebugBundleUnavailable is the static message on every request_debug_bundle
// error reply. Deliberately generic: the wire reply NEVER echoes the assembly
// error text — which could quote a recording path or filename — nor any content
// byte, only this constant. The only failure this verb reports is "unavailable"
// (nil DebugBundler seam, or an assembly failure), so no per-cause message is
// needed.
const msgDebugBundleUnavailable = "debug bundle unavailable"

// handleDebugBundleRequest assembles the current session's debug bundle via the
// injected DebugBundler and streams it back to s as debug_bundle_chunk* +
// debug_bundle_done (#812), or sends a single deterministic error reply.
// Intercepted in dispatchAppFrame before dispatch.Route — like handleInterrupt /
// handleRequestSnapshot — and runs on the manager's single Run dispatch
// goroutine. Every branch either enqueues one bundle stream or sends exactly one
// error reply, then returns: it never panics, hangs, or silently drops the
// request.
//
// The request frame is bare (no payload): the bundle is daemon-global (the whole
// log ring plus the newest recording across all sessions, per #811), so there is
// no attacker-controlled field — no conversation_id, no path, no id — that flows
// into assembly or the wire, and no argument that could select another session's
// data.
//
// SECURITY (AC #4): the assembled archive bytes are the plaintext bundle
// (recording + logs) — the highest-value secret surface in the system. They are
// streamed ONLY over the AEAD-sealed push path (StreamBundle → Push → drainOnce
// seals every chunk before m.send); this method logs a content-free byte count
// on success and the failure EVENT on error — never the archive, a member, a log
// line, or a recording byte. Every error reply carries only the static
// msgDebugBundleUnavailable constant, never the assembly error text.
func (m *V2SessionManager) handleDebugBundleRequest(ctx context.Context, s *V2Session, env protocol.Envelope) {
	// A nil DebugBundler (optional seam / foreground / unwired) means the feature
	// is unavailable; report it deterministically rather than dropping.
	if m.cfg.DebugBundler == nil {
		m.debugBundleReplyError(ctx, s, env.ID)
		return
	}

	// #911: bound this conn to one in-flight bundle. If a prior bundle's chunks
	// are still queued (a slow/stalled transport has not drained them), a retry
	// must not stack a second bundle's never-droppable control frames onto the
	// queue — that is unbounded per-retry memory growth (~4/3 × archive per
	// stacked bundle). Reply with the same deterministic retryable "unavailable"
	// error as the branches below, so the phone retries later; once the prior
	// bundle drains the gate clears and the retry is served (AC #4). Placed BEFORE
	// DebugBundler() so the on-disk assembly is skipped, not merely the enqueue
	// (AC #1: "no second bundle is assembled or enqueued").
	if m.bundleInFlight(s.connID) {
		m.debugBundleReplyError(ctx, s, env.ID)
		return
	}

	archive, err := m.cfg.DebugBundler()
	if err != nil {
		// #811 read-failure honesty: a recording that exists but fails to read
		// surfaces as an error, not a false-absent. Log the failure EVENT only —
		// NEVER the wrapped err (it could quote a recording path/filename) — and
		// send a deterministic error reply.
		m.cfg.Logger.Warn("relay: v2 debug bundle assemble failed",
			"event", "v2.bundle.assemble_err",
			"conn_id", s.connID)
		m.debugBundleReplyError(ctx, s, env.ID)
		return
	}

	if err := m.StreamBundle(ctx, s.connID, archive); err != nil {
		// Unreachable in practice: s is V2StateOpen on the dispatch goroutine, so
		// its push queue exists. Logged at debug and dropped — the package's
		// outbound-drop posture; NEVER echo the archive.
		m.cfg.Logger.Debug("relay: v2 debug bundle stream dropped",
			"event", "v2.bundle.stream_err",
			"conn_id", s.connID,
			"err", err)
		return
	}

	// One content-free info log: conn_id + byte count only, never the archive or
	// any member (AC #4).
	m.cfg.Logger.Info("relay: v2 debug bundle served",
		"event", "v2.bundle.served",
		"conn_id", s.connID,
		"bytes", len(archive))
}

// debugBundleReplyError sends a single deterministic TypeError reply to s,
// correlated to inReplyTo, via the same m.forwardEnvelope seal-and-forward path
// handleRequestSnapshot uses. The code/message/retryable are FIXED
// (server.binary_offline + msgDebugBundleUnavailable + retryable), because the
// only failure this verb reports is "unavailable" — so no attacker-influenced or
// assembly-error text ever reaches the wire.
func (m *V2SessionManager) debugBundleReplyError(ctx context.Context, s *V2Session, inReplyTo uint64) {
	errPayload, err := json.Marshal(protocol.ErrorPayload{
		Code:      protocol.CodeServerBinaryOffline,
		Message:   msgDebugBundleUnavailable,
		Retryable: true,
	})
	if err != nil {
		// A closed struct of strings + bool; marshal cannot fail in practice.
		m.cfg.Logger.Warn("relay: v2 debug bundle error reply marshal failed",
			"event", "v2.bundle.err_marshal",
			"conn_id", s.connID)
		return
	}
	reply := protocol.Envelope{
		ID:        1, // non-load-bearing; the phone correlates on InReplyTo.
		Type:      protocol.TypeError,
		TS:        time.Now().UTC(),
		Payload:   errPayload,
		InReplyTo: &inReplyTo,
	}
	if err := m.forwardEnvelope(ctx, s.connID, reply); err != nil {
		m.cfg.Logger.Debug("relay: v2 debug bundle error reply push dropped",
			"event", "v2.bundle.err_push",
			"conn_id", s.connID,
			"err", err)
	}
}

// bundleInFlight reports whether connID's push queue still holds any
// debug_bundle_chunk or debug_bundle_done envelope from a prior bundle — i.e.
// a StreamBundle's frames enqueued by an earlier handleDebugBundleRequest have
// not all drained yet. It is the per-conn in-flight gate for #911: while it is
// true, a repeated request_debug_bundle on the same conn is rejected before any
// assembly or enqueue, so a client retry loop against a slow/stalled transport
// cannot stack a second bundle's never-droppable control frames (unbounded
// per-retry memory).
//
// Both types are scanned because StreamBundle enqueues all N chunks PLUS the
// trailing debug_bundle_done in one handler invocation and drainOnce pops one
// per Run pass: during the drain the queue holds a shrinking suffix that always
// includes the done marker until the very last pop, so scanning for either type
// covers the whole in-flight window and clears exactly when the done marker has
// also drained (AC #4).
//
// An unknown conn (!ok) has no queue and cannot be bundle-busy → false, the safe
// direction (a non-open conn's later StreamBundle/Push fails closed with
// ErrConnNotFound anyway).
//
// pushMu is taken ALONE for a pure O(len(items)) read of q.items and released
// before the caller's reply, preserving the leaf lock's "never held across an
// Encrypt, m.send, or any channel op … always taken alone" invariant. The scan
// runs on the Run dispatch goroutine, serialized against this conn's own
// drainOnce pops; the only concurrent mutator is an off-Run Push, which can only
// append frames — it can turn a false into a true (more conservative), never
// clear a true — so there is no TOCTOU that admits a second bundle.
func (m *V2SessionManager) bundleInFlight(connID string) bool {
	m.pushMu.Lock()
	defer m.pushMu.Unlock()
	q, ok := m.queues[connID]
	if !ok {
		return false
	}
	for i := range q.items {
		switch q.items[i].env.Type {
		case protocol.TypeDebugBundleChunk, protocol.TypeDebugBundleDone:
			return true
		}
	}
	return false
}

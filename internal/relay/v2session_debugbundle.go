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
//
// #1491 added the off-Run assembly to this file (assembleBundle,
// handleBundleReady, bundleResult); the same carve-out puts its two struct
// members over in v2session.go — V2Session.bundleAssembling and
// V2SessionManager.bundleReady, plus the Run arm that feeds handleBundleReady.

// msgDebugBundleUnavailable is the static message on every request_debug_bundle
// error reply. Deliberately generic: the wire reply NEVER echoes the assembly
// error text — which could quote a recording path or filename — nor any content
// byte, only this constant. The only failure this verb reports is "unavailable"
// (nil DebugBundler seam, or an assembly failure), so no per-cause message is
// needed.
const msgDebugBundleUnavailable = "debug bundle unavailable"

// bundleResult carries one finished assembly from its off-Run goroutine
// (assembleBundle) back to Run's m.bundleReady arm, where handleBundleReady
// streams it or replies with the deterministic error (#1491). The channel FIELD
// lives on V2SessionManager in v2session.go — the #1025 carve-out keeps the
// verb's machinery in this file but the struct definitions there.
type bundleResult struct {
	// s pins which session this result belongs to. Carried as a pointer but
	// NEVER dereferenced off Run — the same contract as wakeSignal.s. Only
	// handleBundleReady, on Run, reads its fields.
	s *V2Session

	// inReplyTo is the accepted request's envelope id, so the failure branch
	// correlates its reply exactly as the on-Run reject branches do.
	inReplyTo uint64

	// archive is the assembled bundle: plaintext recording + logs, the
	// highest-value secret surface in the system. It travels only into
	// StreamBundle (→ the AEAD-sealed push path) and is never logged.
	archive []byte

	// err selects the reply branch ONLY. It MUST NEVER be logged or sent — an
	// assembly error can quote a recording path or filename, and every reply
	// this verb emits carries the static msgDebugBundleUnavailable instead.
	// handleBundleReady logs the failure EVENT alone; the deterministic
	// backstop is TestV2Session_DebugBundle_ErrorReplies, which greps the
	// captured log buffer for the error text.
	err error
}

// handleDebugBundleRequest accepts a request_debug_bundle, hands the assembly to
// an off-Run goroutine, and lets handleBundleReady stream the result back as
// debug_bundle_chunk* + debug_bundle_done (#812) — or sends a single
// deterministic error reply here and now. Intercepted in dispatchAppFrame before
// dispatch.Route — like handleInterrupt / handleRequestSnapshot — and runs on the
// manager's single Run dispatch goroutine. Every branch either accepts exactly
// one assembly or sends exactly one error reply, then returns: it never panics,
// hangs, or silently drops the request.
//
// The DebugBundler seam is the one control-verb step that is NOT fast: in
// production it reads the newest recording in full and gzips the archive in
// memory, both uncapped. Running it inline parked Run inside a single select arm
// for that whole duration, stalling every other conn's frames, the push drain and
// the timers (#1491). Only the seam call moves; StreamBundle, the error reply and
// both log lines still run on Run, in handleBundleReady, so no s.send.Encrypt
// ever leaves the single-owner goroutine.
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

	// #911: bound this conn to one in-flight bundle. If a prior bundle is still
	// assembling, or its chunks are still queued (a slow/stalled transport has not
	// drained them), a retry must not stack a second bundle's never-droppable
	// control frames onto the queue — that is unbounded per-retry memory growth
	// (~4/3 × archive per stacked bundle) — nor start a second concurrent
	// full-size assembly. Reply with the same deterministic retryable
	// "unavailable" error as the branches below, so the phone retries later; once
	// the prior bundle drains the gate clears and the retry is served (AC #4).
	// Placed BEFORE the hand-off so the on-disk assembly is skipped, not merely
	// the enqueue (AC #1: "no second bundle is assembled or enqueued").
	//
	// The two halves compose GAPLESSLY because they overlap rather than abut
	// (#1491). s.bundleAssembling covers [accept → StreamBundle's enqueue
	// complete]; bundleInFlight reads true from the first Push onward. Both the
	// set below and the clear in handleBundleReady are on Run, and that clear runs
	// AFTER StreamBundle returns within the same Run pass, so there is no instant
	// at which both halves read false while a bundle is live — and no Run arm can
	// interleave between them to observe one.
	if s.bundleAssembling || m.bundleInFlight(s.connID) {
		m.debugBundleReplyError(ctx, s, env.ID)
		return
	}

	// Accepted. Mark the conn assembling (the accept-side gate half), then run the
	// seam off Run. assembleBundle touches nothing Run owns; its result comes back
	// through m.bundleReady, where handleBundleReady does every remaining step.
	s.bundleAssembling = true
	go m.assembleBundle(ctx, s, env.ID)
}

// assembleBundle runs the DebugBundler seam OFF the Run goroutine and funnels the
// result back to Run via m.bundleReady (#1491). One goroutine per ACCEPTED
// request, and at most one per conn at a time — that is exactly what the
// s.bundleAssembling marker enforces, so concurrent full-size archives in memory
// are bounded by a guard rather than by hope.
//
// It reads exactly two things, neither Run-owned: m.cfg.DebugBundler (immutable
// after NewV2SessionManager, like the m.cfg.Logger that Push already reads
// off-Run) and s.done (created on Run at open, closed once by closeWith, never
// reassigned — the same field appFrameWorker reads off-Run). It dereferences NO
// Run-owned session field: not s.send, s.recv, s.state, s.bundleAssembling, nor
// m.sessions. It performs no cryptographic operation, so no s.send.Encrypt can
// run concurrently with Run's and reuse a nonce.
//
// The hand-off BLOCKS with ctx/s.done escapes rather than dropping like the
// drainCh idiom: a dropped result would leave s.bundleAssembling set forever and
// lock the conn out of its own debug bundle permanently. This is armIdleTimer's
// reasoning, not Push's — the goroutine exists only to carry one result, so
// parking it costs nothing that matters, and both escapes fire on teardown.
func (m *V2SessionManager) assembleBundle(ctx context.Context, s *V2Session, inReplyTo uint64) {
	archive, err := m.cfg.DebugBundler()
	select {
	case m.bundleReady <- bundleResult{s: s, inReplyTo: inReplyTo, archive: archive, err: err}:
	case <-s.done:
		// This conn tore down mid-assembly. The marker dies with the session and
		// a reconnecting conn_id gets a fresh V2Session, so dropping here cannot
		// lock anyone out.
	case <-ctx.Done():
		// Run is exiting; the manager is dead and the marker is irrelevant.
	}
}

// handleBundleReady completes one accepted request_debug_bundle on the Run
// goroutine, from the result its off-Run assembleBundle produced (#1491). Reached
// only from Run's m.bundleReady arm. Every step here — StreamBundle, the error
// reply, both log lines — is the code that used to run inline in
// handleDebugBundleRequest, running in the same place it always did: on Run,
// where s.send is single-owned.
//
// The marker clear is DEFERRED so every exit path takes it, including the failure
// and stale-session returns. A branch-local clear is one edit away from the
// permanent per-conn lockout AC #2 forbids; the defer makes that structural.
// Clearing after StreamBundle returns (not before) is what makes the gate's two
// halves overlap — see the gate comment in handleDebugBundleRequest.
//
// SECURITY: res.err never reaches a log or the wire. The failure branch logs the
// EVENT only, and its reply carries the static msgDebugBundleUnavailable, exactly
// like the on-Run reject branches (AC #3).
func (m *V2SessionManager) handleBundleReady(ctx context.Context, res bundleResult) {
	defer func() { res.s.bundleAssembling = false }()

	if res.s.state != V2StateOpen {
		// closeWith ran between the accept and this result. Drop it: sealing under
		// a dead session would burn a send-nonce for a frame no live peer awaits.
		// Mirrors handleWake's state guard and forwardAppReply's V2StateOpen gate.
		// A reconnected same-conn_id session is a DIFFERENT pointer whose marker is
		// false, so consulting m.sessions here would buy nothing.
		m.cfg.Logger.Debug("relay: v2 debug bundle dropped; session not open",
			"event", "v2.bundle.stale",
			"conn_id", res.s.connID)
		return
	}

	if res.err != nil {
		// #811 read-failure honesty: a recording that exists but fails to read
		// surfaces as an error, not a false-absent. Log the failure EVENT only —
		// NEVER res.err (it could quote a recording path/filename) — and send a
		// deterministic error reply.
		m.cfg.Logger.Warn("relay: v2 debug bundle assemble failed",
			"event", "v2.bundle.assemble_err",
			"conn_id", res.s.connID)
		m.debugBundleReplyError(ctx, res.s, res.inReplyTo)
		return
	}

	if err := m.StreamBundle(ctx, res.s.connID, res.archive); err != nil {
		// Unreachable in practice: the V2StateOpen check above means the push queue
		// exists. Logged at debug and dropped — the package's outbound-drop
		// posture; NEVER echo the archive.
		m.cfg.Logger.Debug("relay: v2 debug bundle stream dropped",
			"event", "v2.bundle.stream_err",
			"conn_id", res.s.connID,
			"err", err)
		return
	}

	// One content-free info log: conn_id + byte count only, never the archive or
	// any member (AC #4).
	m.cfg.Logger.Info("relay: v2 debug bundle served",
		"event", "v2.bundle.served",
		"conn_id", res.s.connID,
		"bytes", len(res.archive))
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
	if m.dropInlineReplyIfDown(s, "v2.bundle.err_dropped_transport_down") {
		return
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
// a StreamBundle's frames enqueued by an earlier handleBundleReady have not all
// drained yet. It is the queue-derived half of the per-conn in-flight gate for
// #911: while it is true, a repeated request_debug_bundle on the same conn is
// rejected before any assembly or enqueue, so a client retry loop against a
// slow/stalled transport cannot stack a second bundle's never-droppable control
// frames (unbounded per-retry memory). The accept-side half — covering the
// assembly window, before which nothing is queued to scan — is
// V2Session.bundleAssembling (#1491).
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

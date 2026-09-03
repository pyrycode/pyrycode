package relay

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// This file holds the inbound attachment-RETRIEVAL interception (#2054): the
// handler behind dispatchAppFrame's TypeRequestAttachment case and the two wire
// answers it can give. It is the join between three landed pieces that had no
// caller between them — protocol.TypeRequestAttachment (#2052, vocabulary only),
// (*V2SessionManager).StreamAttachment (#2053, unwired), and
// attachments.ResolvePath behind the AttachmentResolve seam (#2037).
//
// UNLIKE ITS UPLOAD SIBLING IT IMPORTS NOTHING FROM internal/attachments.
// v2session_attachment.go has to, because it maps that package's sentinels to
// wire codes through errors.Is. Here there is nothing to map: the resolver seam is
// comma-ok by design, so the one sentinel behind it never crosses into this
// package, and the two codes below are chosen from a bool and one error identity.
//
// WHERE IT RUNS. On the conn's appFrameWorker, off the Run goroutine, reached from
// appFrameWorker's appFrameAttachmentRequest arm — the second frame type to need
// that route after the upload leg, and for the same reason: answering one request
// reads a stored file of up to the per-upload byte bound, hashes it and
// base64-marshals one envelope per chunk. The two answer paths leave by DIFFERENT
// routes and both are correct: the chunks go out through StreamAttachment's Push,
// documented safe from any goroutine because it touches only the manager's push
// queues, while a reject goes through forwardToRun so Run seals it under s.send.
// A reject emitted directly from this goroutine would be a concurrent Encrypt on
// the single-owner send CipherState — a nonce reuse.
//
// SERIALISATION IS THE CONCURRENCY BOUND. The worker is strictly FIFO and there is
// one per conn, so a conn has at most one retrieval in flight; a second request
// waits behind the first rather than doubling the memory in play. That is not an
// invariant this file maintains — it is the worker's shape — but it IS the reason
// no per-verb concurrency limit is minted here, which § Attachments leaves
// receiver-configured and unpublished.

// Static reply messages, one per wire code this path can answer with. Both are
// fixed constants and NEITHER is derived from an error value, an id or a path:
// attachments.ResolvePath's refusals format the raw client-supplied id and the
// daemon's own directory layout into their messages, and os.ReadFile's wrap a
// host path whose leaf is a sanitised client filename.
const (
	msgAttachmentNotFound      = "attachment not found"
	msgAttachmentStreamAborted = "attachment stream aborted"
)

// The two answers, spelled once, reusing the upload leg's attachmentReject so a
// code cannot be paired with the wrong retryable flag — the field a client
// actually branches on. Both values are read off docs/protocol-mobile.md § Error
// codes rather than chosen here: not_found is published "no", stream_aborted
// "yes, after a backoff".
var (
	rejectAttachmentNotFound = attachmentReject{protocol.CodeAttachmentNotFound, msgAttachmentNotFound, false}
	rejectStreamAborted      = attachmentReject{protocol.CodeAttachmentStreamAborted, msgAttachmentStreamAborted, true}
)

// handleRequestAttachment answers one inbound request_attachment with the stored
// file's bytes or with one coded reject. Intercepted in dispatchAppFrame before
// dispatch.Route, exactly like handleAttachmentChunk, and routed to this conn's
// appFrameWorker rather than handled inline on Run (see the file header).
//
// It takes the plaintext rather than the already-probed Envelope because the
// worker receives bytes: the frame is decoded here for the second and last time.
//
// ORDER IS THE DESIGN, and the ordering — not merely the presence of the checks —
// is what carries the security property:
//
//  1. A nil AttachmentResolve makes the frame INERT — but still CONSUMED, so it no
//     longer draws dispatch.Route's unknown-type reply. Mirrors the nil
//     AttachmentIntake guard and buys the same property: an unwired daemon
//     performs zero parsing of remote-authored bytes.
//  2. A PAYLOAD DECODE FAILURE IS REJECTED, NOT TOLERATED. handleRequestSnapshot
//     tolerates one because its zero value fails the next gate anyway;
//     protocol.RequestAttachmentPayload's own block states the opposite obligation
//     for this frame — "a decode failure is a rejected frame, never an
//     empty-but-successful request". NOTHING about the failure is echoed or
//     logged: encoding/json quotes offending input into its error string and those
//     bytes are remote-authored.
//  3. THE MEMBERSHIP GATE FIRES BEFORE EITHER ID BECOMES A PATH COMPONENT. This is
//     the whole of attachments.ResolvePath's stated precondition — the conversation
//     must be one the CALLER validated, because "this function cannot check that,
//     and a caller that gets it wrong defeats every check below" — and this handler
//     is that caller. handleRequestSnapshot is the in-package precedent for the
//     ordering; its CODE is deliberately not copied. It answers
//     conversation.not_found, and a distinguishable answer here would rebuild
//     exactly the path-existence oracle CodeAttachmentNotFound's merge exists to
//     prevent. A nil seam refuses everything, which is the only fail-safe reading.
//     KnownConversation rather than a session router: a router additionally refuses
//     a known conversation with NO BOUND SESSION, which is precisely the reopened
//     conversation this verb exists for.
//  4. An empty attachment id is refused before the seam. Not a second copy of
//     ResolvePath's canonical-shape check — the non-canonical-but-non-empty id
//     stays that function's to refuse, and forking the shape rule would be the
//     second, weaker copy this package keeps declining. It is the ONE case the
//     published contract calls out as silent: filepath.Join(dir, "", "") is dir, so
//     an unchecked empty component addresses the conversation directory root rather
//     than erroring.
//  5. Anything the resolver will not answer is not_found, and the collapse is
//     structural: one sentinel behind that bool covers the unknown id, the
//     non-canonical id and the id resolving outside the conversation's own
//     directory alike, so this site CANNOT branch on what it must not distinguish.
//
// SIX CAUSES, ONE CODE, DELIBERATELY. Steps 2 through 5 plus a pre-emission read
// failure all yield attachment.not_found with the same static message and the same
// retryable flag, so an unknown conversation and an unknown attachment are byte
// for byte identical on the wire. § Attachments extends the merge to the whole
// request: "A request that yields no bytes at all is attachment.not_found — one
// code for every such outcome."
//
// THE SUCCESS PATH REPLIES NOTHING OF ITS OWN. The chunks ARE the answer, each
// correlated to this request through in_reply_to, and there is no completion frame
// — TotalChunks rides every chunk instead, so a receiver knows the expected count
// from the first frame it sees.
//
// SECURITY — the never-log rule. protocol.AttachmentChunkPayload's SECURITY block
// and docs/protocol-mobile.md § Attachments fix the loggable set, and
// attachments.ResolvePath's doc block adds the resolved path to the banned side
// because its leaf is a sanitised client filename. So: conn id, the attachment id
// once it is non-empty, and the reject code may be logged; the file's bytes, the
// filename, the digest and the host path never. Past the resolver the logged id is
// canonical, because ResolvePath answered true for it; on the resolver-MISS arm it
// can be an arbitrary client string, since the comma-ok seam collapses a failed
// shape check into the same false as a missing file. Logging it there is deliberate
// and matches handleAttachmentChunk, whose header names the bound that makes it
// safe: slog's TextHandler escapes control bytes, so an escape-bearing id cannot
// forge log structure. The requested CONVERSATION id is not logged on ANY arm —
// this handler validates its membership, not its shape, and § Attachments makes
// shape validation the precondition for logging a client-supplied string. The error out of StreamAttachment is never logged on any
// arm, only classified: #2053 rebuilt it around the stripped cause precisely so a
// caller could classify without holding the path, and not logging it is the other
// half of that bargain.
func (m *V2SessionManager) handleRequestAttachment(ctx context.Context, s *V2Session, plaintext []byte) {
	if m.cfg.AttachmentResolve == nil {
		m.cfg.Logger.Debug("relay: v2 request_attachment inert; no resolver wired",
			"event", "v2.attachment.request.inert",
			"conn_id", s.connID)
		return
	}

	var env protocol.Envelope
	if err := json.Unmarshal(plaintext, &env); err != nil {
		// Unreachable: dispatchAppFrame decoded these same bytes to match the
		// type before enqueuing them. Reply nothing — there is no envelope id
		// left to correlate a reply to. NEVER echo err.
		m.cfg.Logger.Warn("relay: v2 request_attachment envelope did not decode",
			"event", "v2.attachment.request.envelope_err",
			"conn_id", s.connID)
		return
	}

	var req protocol.RequestAttachmentPayload
	if err := json.Unmarshal(env.Payload, &req); err != nil {
		// Rejected, not tolerated (step 2). NEVER echo err or any payload byte,
		// and no id either: the decode is what failed, so a partially-populated
		// id would attribute a refusal to an attachment nobody asked for.
		m.rejectAttachmentRequest(ctx, s, env.ID, rejectAttachmentNotFound, "payload did not decode", "")
		return
	}

	if m.cfg.KnownConversation == nil || !m.cfg.KnownConversation(req.ConversationID) {
		// Step 3. The requested id is NOT logged — it is a client-supplied string
		// this handler never shape-validates, and the record must not become the
		// log-injection shape § Attachments forbids.
		m.rejectAttachmentRequest(ctx, s, env.ID, rejectAttachmentNotFound, "conversation is not one this daemon hosts", "")
		return
	}

	if req.AttachmentID == "" {
		// Step 4.
		m.rejectAttachmentRequest(ctx, s, env.ID, rejectAttachmentNotFound, "attachment id is absent or empty", "")
		return
	}

	path, ok := m.cfg.AttachmentResolve(req.ConversationID, req.AttachmentID)
	if !ok {
		// Step 5. The attachment id is logged here even though this is the one arm
		// where it may NOT be canonical: ResolvePath's shape check is one of the
		// causes the comma-ok seam collapses into false, so "shape invalid" and
		// "no such attachment" arrive identically and an arbitrary client string
		// can reach the record. Logging it anyway matches handleAttachmentChunk,
		// and the bound is the one that handler's header names — slog's
		// TextHandler escapes control bytes, so an escape-bearing id cannot forge
		// log structure — plus one record per request rather than per chunk. This
		// is NOT the § Attachments shape-validated-first rule; that rule is why the
		// conversation id above stays unlogged on every arm.
		m.rejectAttachmentRequest(ctx, s, env.ID, rejectAttachmentNotFound, "no attachment resolves under that conversation", req.AttachmentID)
		return
	}

	if err := m.StreamAttachment(ctx, s.connID, req.AttachmentID, path, env.ID); err != nil {
		rej, reason := rejectAttachmentNotFound, "stored file could not be read"
		if attachmentStreamAborted(err) {
			rej, reason = rejectStreamAborted, "stream abandoned"
		}
		m.rejectAttachmentRequest(ctx, s, env.ID, rej, reason, req.AttachmentID)
		return
	}

	m.cfg.Logger.Info("relay: v2 attachment served",
		"event", "v2.attachment.request.served",
		"conn_id", s.connID,
		"attachment_id", req.AttachmentID,
		"in_reply_to", env.ID)
}

// attachmentStreamAborted reports whether err out of StreamAttachment means
// EMISSION HAD ALREADY BEGUN, which is the only thing that separates the abort
// code from the reject one. StreamAttachment returns a bare error and no count of
// what it enqueued, so the error's IDENTITY is the whole signal available.
//
// IT NAMES THE ABORT SET AND DEFAULTS TO not_found, and the direction is the
// decision. StreamAttachment has three error origins: a failed read and a failed
// envelope marshal, both raised BEFORE any envelope is pushed, and a failed Push,
// which can only happen once emission has begun. Push's own contract closes ITS
// set — ErrConnNotFound when the conn's queue is gone, ctx.Err() only when ctx was
// already cancelled at entry, and a queue drop or overflow is explicitly NOT an
// error — so the abort set is small and enumerable while the pre-emission set is
// open (any errno from a read). Defaulting the open set to not_found classifies a
// future pre-emission failure correctly with no edit here; defaulting the other
// way would claim bytes went out when none did.
//
// THE OBLIGATION THAT COMES WITH THAT: any future failure mode that can occur
// AFTER the first Push must be added to this function, or it will be answered
// not_found while a client holds a partial transfer.
//
// A Push failure at index 0 is answered stream_aborted where not_found would also
// have been defensible. That is the correct trade rather than an accepted
// imprecision: stream_aborted is the retryable code and a torn-down conn is a
// retryable condition, while not_found is terminal and would tell a client its
// file is gone when it is not.
func attachmentStreamAborted(err error) bool {
	return errors.Is(err, ErrConnNotFound) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded)
}

// rejectAttachmentRequest records one refusal and answers it, so the log line and
// the wire frame cannot drift apart into two edits. reason is a DAEMON-AUTHORED
// constant chosen by the call site — it is what makes the six merged causes
// separable in the daemon's own logs while staying identical on the wire, which is
// the whole shape of the merge: indistinguishable to a client, diagnosable to an
// operator. attachmentID is logged only where the caller has established it is
// NON-EMPTY — not necessarily canonical, since the resolver-miss arm cannot tell a
// failed shape check from a missing file; "" means the caller had no id it was
// allowed to name, and the field is then omitted rather than logged empty.
func (m *V2SessionManager) rejectAttachmentRequest(ctx context.Context, s *V2Session, inReplyTo uint64, rej attachmentReject, reason, attachmentID string) {
	attrs := []any{
		"event", "v2.attachment.request.refused",
		"conn_id", s.connID,
		"code", rej.code,
		"reason", reason,
	}
	if attachmentID != "" {
		attrs = append(attrs, "attachment_id", attachmentID)
	}
	m.cfg.Logger.Warn("relay: v2 request_attachment refused", attrs...)
	m.attachmentReplyError(ctx, s, inReplyTo, rej)
}

package relay

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/pyrycode/pyrycode/internal/attachments"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// This file holds the inbound attachment-upload interception (#1897): the handler
// behind dispatchAppFrame's TypeAttachmentChunk case, the sentinel-to-wire-code
// map, and the two reply emitters. It is the join between the wire and
// attachments.Intake (#1896), which had no caller outside its own package.
//
// IT IMPORTS internal/attachments, and it is the one seam handler in this package
// that imports its implementing package. That is deliberate and settled rather
// than a slip: #1751's spec § Error handling fixes internal/* packages returning
// Go sentinels and the relay dispatch site mapping them to dotted wire codes at
// the call site via errors.Is, and errors.Is needs the sentinel values. The seam
// interface itself (AttachmentIntake, in v2session_seams.go) still names only
// internal/protocol, so the "imports neither X nor Y" property its neighbours
// carry holds where it can.
//
// WHERE IT RUNS. On the conn's appFrameWorker, off the Run goroutine, reached
// from appFrameWorker's job.attachment arm — not inline in dispatchAppFrame like
// every other control arm. A completing chunk hashes and writes up to the
// per-upload byte bound, which is precisely the work #1491 had to move off Run for
// handleDebugBundleRequest. Being on that worker is also what discharges
// Receive's single-feeder precondition, since exactly one is spawned per session
// and it routes strictly FIFO.

// Static reply messages, one per wire code this path can answer with. Every one
// is a fixed constant and NONE is derived from an error value: EnsureDir wraps
// host paths and the daemon's own conversation id into its refusals, and Store's
// rename leg wraps an *os.LinkError whose Error() prints the sanitised filename.
// docs/protocol-mobile.md § Error codes mandates the static message for
// attachment.storage_failed by name; the others follow it rather than each
// inventing a disclosure posture.
const (
	msgAttachmentInvalidChunk    = "attachment chunk rejected"
	msgAttachmentIntegrityFailed = "attachment integrity check failed"
	msgAttachmentTooLarge        = "attachment exceeds the per-upload byte bound"
	msgAttachmentTooManyUploads  = "too many uploads in flight"
	msgAttachmentStorageFailed   = "attachment could not be stored"
)

// attachmentReject is the wire answer for one refusal: the dotted code, its
// static message and the retryability docs/protocol-mobile.md § Error codes
// publishes for that code. Grouping the three keeps a caller from pairing a code
// with the wrong retryable flag, which is the field a client actually branches on.
type attachmentReject struct {
	code      string
	message   string
	retryable bool
}

// The five answers, spelled once. Values rather than a map so each is addressable
// by name from the tests that pin the two codes this ticket chose.
var (
	rejectInvalidChunk    = attachmentReject{protocol.CodeAttachmentInvalidChunk, msgAttachmentInvalidChunk, false}
	rejectIntegrityFailed = attachmentReject{protocol.CodeAttachmentIntegrityFailed, msgAttachmentIntegrityFailed, false}
	rejectTooLarge        = attachmentReject{protocol.CodeAttachmentTooLarge, msgAttachmentTooLarge, false}
	rejectTooManyUploads  = attachmentReject{protocol.CodeAttachmentTooManyUploads, msgAttachmentTooManyUploads, true}
	rejectStorageFailed   = attachmentReject{protocol.CodeAttachmentStorageFailed, msgAttachmentStorageFailed, true}
)

// attachmentRejectFor maps one refusal out of AttachmentIntake.Receive to the wire
// code that answers it. The map is deliberately MANY-TO-ONE and written one arm
// per CODE rather than one per sentinel: twelve sentinels are reachable and the
// published vocabulary has five members that fit them, so a per-sentinel switch
// would be five duplicated bodies.
//
// Ten of the twelve were already committed by landed doc blocks — #1751's spec
// § Error handling for the framing and integrity groups, admission.go's own docs
// for the two resource bounds, and storage.go's ErrWriteFailed doc for all three
// of that file's. #1897 chose the remaining two, and BOTH FOLD INTO THE PUBLISHED
// VOCABULARY rather than minting a code: #1751 declared that vocabulary in one
// place exactly so the implementations answering from it would not each invent a
// name, and a new code would need a codes.go constant plus a published § Error
// codes row — a protocol-publication slice, not this one.
//
// ErrUnknownUpload → attachment.invalid_chunk. The sentinel is narrower than it
// reads: it is the pair's entry vanishing between Receive's Lookup and its
// Deliver — reaped for idleness, or released by this conn's teardown — and NOT a
// client resuming against a restarted daemon, which #1896's pinned admission fork
// ADMITS into a fresh transfer whatever index the chunk carries. So the case to
// choose against is "your transfer expired". invalid_chunk already carries the
// class "this chunk cannot be placed against a transfer", and this is the sub-case
// where no transfer state is left to place it against. Its published
// retryable:false is the CORRECT advice here rather than merely tolerable: a naive
// resend of this one chunk would be admitted as a fresh transfer that can never
// complete and idles out, so "rebuild the transfer" is exactly right. One clause
// of that published row does not transfer — "resending the same frames reproduces
// it" is false for an expired transfer, where resending ALL frames succeeds — and
// that clause is the row's rationale, not the contract it states.
//
// ErrNoConversation → attachment.storage_failed, where every clause of the
// published row is true of it: a VERIFIED attachment (Deliver returned assembled,
// digest-checked bytes) COULD NOT BE WRITTEN TO THE HOST (there is no destination
// to file it under), the message is STATIC so the daemon's routing state stays off
// the wire, and RETRYABLE AFTER A BACKOFF is right — it clears the moment the
// daemon routes to a conversation, and hot-looping the re-upload of an
// already-transmitted file is what that row forbids. Sharing the code with
// ErrInvalidID does not collapse what that sentinel's doc block argued for: its
// objection is to routing an unrouted daemon through EnsureDir("") and telling a
// client its OWN id was malformed, and attachment.storage_failed says neither
// "your id is malformed" nor "the daemon is unrouted" — it says the host could not
// file it, back off.
//
// AN ERROR MATCHING NO ARM IS STILL ANSWERED, never dropped: storage_failed is the
// honest generic for a daemon-side condition of unknown cause, and it is retryable
// and static. Reachable only through a future sentinel this switch has not learned
// about, which is precisely when a silent drop would be worst.
func attachmentRejectFor(err error) attachmentReject {
	switch {
	case errors.Is(err, attachments.ErrInvalidDeclaration),
		errors.Is(err, attachments.ErrTotalChunksMismatch),
		errors.Is(err, attachments.ErrIndexOutOfRange),
		errors.Is(err, attachments.ErrDuplicateIndex),
		errors.Is(err, attachments.ErrUnknownUpload):
		return rejectInvalidChunk
	case errors.Is(err, attachments.ErrSizeMismatch),
		errors.Is(err, attachments.ErrDigestMismatch):
		return rejectIntegrityFailed
	case errors.Is(err, attachments.ErrUploadTooLarge):
		return rejectTooLarge
	case errors.Is(err, attachments.ErrTooManyUploads):
		return rejectTooManyUploads
	default:
		// ErrInvalidID, ErrNotContained, ErrWriteFailed, ErrNoConversation, and
		// anything this switch has not learned about.
		return rejectStorageFailed
	}
}

// handleAttachmentChunk drives one inbound attachment_chunk through the
// AttachmentIntake seam and answers it. Intercepted in dispatchAppFrame before
// dispatch.Route, exactly like handleQuestionAnswer, but routed to this conn's
// appFrameWorker rather than handled inline on Run (see the file header).
//
// It takes the plaintext rather than the already-probed Envelope because the
// worker receives bytes: the frame is decoded here for the second and last time,
// which is the same two-decode cost dispatchAppFrame's own doc accepts for every
// v1 application frame (its probe, then dispatch.Route's).
//
// Order is load-bearing:
//
//  1. A nil AttachmentIntake makes the frame INERT — but still CONSUMED, so it no
//     longer draws dispatch.Route's unknown-type reply. Mirrors
//     handleQuestionAnswer's nil guard and buys the same property: an unwired
//     daemon performs zero parsing of remote-authored bytes.
//  2. A PAYLOAD DECODE FAILURE IS REJECTED, NOT TOLERATED. The modal handlers'
//     `_ = json.Unmarshal(...)` idiom is unsafe here for the reason #1984 states:
//     encoding/json populates the fields it read BEFORE the one that failed, and
//     AttachmentChunkPayload nests data, size and sha256 behind the ids — so
//     tolerating a failure could hand Receive a populated attachment_id with
//     truncated bytes and a live declaration. The refusal is invalid_chunk: a frame
//     whose framing claims cannot be read is a frame whose framing claims are
//     unusable. NOTHING about the failure is echoed or logged — encoding/json
//     quotes offending input into its error string and those bytes are
//     remote-authored.
//  3. Receive's THREE-WAY answer, in the order that makes the middle one
//     unmissable: refuse on err != nil, answer attachment_stored on stored, and
//     otherwise reply NOTHING. That last branch is what most chunks of a healthy
//     upload take. attachments.ErrIncomplete never arrives here — Receive is the
//     one place that interprets it — so a handler reading err != nil the usual way
//     would answer one reject frame per healthy chunk.
//
// THE ATTACHMENT ID IN THE SUCCESS REPLY IS THE ONE Receive RETURNED, not the one
// this handler decoded. They are equal today by construction, and reading it back
// off the seam is what keeps "the attachment that was stored" sourced from the
// thing that stored it.
//
// SECURITY — the never-log rule. protocol.AttachmentChunkPayload's SECURITY block
// is the declaring contract ("Log the attachment id, the index and the total;
// never the bytes, and never a raw filename") and docs/protocol-mobile.md
// § Attachments adds the declared digest, so the loggable set here is the union's
// complement: conn id, attachment id, index and total may be logged; bytes,
// filename, digest and host path never. The error out of Receive is NEVER logged
// on any arm — not just never replied — because EnsureDir's and Store's refusals
// wrap host paths and the daemon's own conversation id, and a log line carrying a
// host path is banned as squarely as a wire message carrying one. The mapped code
// goes into the record instead. Several doc blocks inside internal/attachments
// assert a stricter four-string ban that includes the attachment id; the declaring
// type does not state it and attachment_stored echoes the id to the wire, so those
// blocks are wrong about the id and right about the rest. Correcting them is out
// of this slice's scope.
//
// The accepted-but-incomplete path logs at DEBUG rather than Info, and that is a
// volume decision with a security edge: attachment_id is client-chosen and its
// shape is checked only at EnsureDir, on the completing chunk, so a hostile paired
// device can put an arbitrary run of bytes there and have it logged on EVERY
// chunk. Keeping the per-chunk record at Debug leaves only terminal outcomes — one
// per transfer — at Info. slog's TextHandler escapes control bytes, so an
// escape-bearing id cannot forge log structure.
func (m *V2SessionManager) handleAttachmentChunk(ctx context.Context, s *V2Session, plaintext []byte) {
	if m.cfg.AttachmentIntake == nil {
		m.cfg.Logger.Debug("relay: v2 attachment_chunk inert; no intake wired",
			"event", "v2.attachment.chunk.inert",
			"conn_id", s.connID)
		return
	}

	var env protocol.Envelope
	if err := json.Unmarshal(plaintext, &env); err != nil {
		// Unreachable: dispatchAppFrame decoded these same bytes to match the
		// type before enqueuing them. Reply nothing — there is no envelope id
		// left to correlate a reply to. NEVER echo err.
		m.cfg.Logger.Warn("relay: v2 attachment_chunk envelope did not decode",
			"event", "v2.attachment.chunk.envelope_err",
			"conn_id", s.connID)
		return
	}

	var chunk protocol.AttachmentChunkPayload
	if err := json.Unmarshal(env.Payload, &chunk); err != nil {
		// Rejected, not tolerated (see step 2). NEVER echo err or any payload
		// byte — not to the phone, not into this record, and no attachment id
		// either: the decode is what failed, so a partially-populated id would
		// attribute refused bytes to a transfer.
		m.cfg.Logger.Warn("relay: v2 attachment_chunk rejected; payload did not decode",
			"event", "v2.attachment.chunk.decode_err",
			"conn_id", s.connID,
			"code", rejectInvalidChunk.code)
		m.attachmentReplyError(ctx, s, env.ID, rejectInvalidChunk)
		return
	}

	attachmentID, stored, err := m.cfg.AttachmentIntake.Receive(s.connID, chunk)
	if err != nil {
		rej := attachmentRejectFor(err)
		m.cfg.Logger.Warn("relay: v2 attachment_chunk refused",
			"event", "v2.attachment.chunk.refused",
			"conn_id", s.connID,
			"attachment_id", chunk.AttachmentID,
			"index", chunk.Index,
			"total_chunks", chunk.TotalChunks,
			"code", rej.code)
		m.attachmentReplyError(ctx, s, env.ID, rej)
		return
	}
	if !stored {
		// Accepted; the transfer wants more. NO REPLY AT ALL — this is what most
		// chunks of a healthy upload get, and answering here is the failure mode
		// the seam's doc block warns about.
		m.cfg.Logger.Debug("relay: v2 attachment_chunk accepted",
			"event", "v2.attachment.chunk.accepted",
			"conn_id", s.connID,
			"attachment_id", chunk.AttachmentID,
			"index", chunk.Index,
			"total_chunks", chunk.TotalChunks)
		return
	}

	payload, err := json.Marshal(protocol.AttachmentStoredPayload{AttachmentID: attachmentID})
	if err != nil {
		// A closed struct of one string; marshal cannot fail in practice.
		// Defensive — drop rather than answer a stored upload with a refusal it
		// would re-upload against.
		m.cfg.Logger.Warn("relay: v2 attachment_stored marshal failed",
			"event", "v2.attachment.stored.marshal_err",
			"conn_id", s.connID,
			"attachment_id", attachmentID)
		return
	}
	m.cfg.Logger.Info("relay: v2 attachment stored",
		"event", "v2.attachment.stored",
		"conn_id", s.connID,
		"attachment_id", attachmentID,
		"total_chunks", chunk.TotalChunks)
	m.attachmentReply(ctx, s, env.ID, protocol.TypeAttachmentStored, payload)
}

// attachmentReplyError answers one refusal with a single TypeError envelope
// correlated to inReplyTo. rej supplies a STATIC message and the retryability
// docs/protocol-mobile.md § Error codes publishes for that code; no value derived
// from an error ever reaches this path.
//
// Its own helper rather than a call into settingsReplyError or
// debugBundleReplyError, matching the package's stated posture that each
// reply-owing handler owns its error helper — and it must be its own anyway,
// because those two emit through m.forwardEnvelope, which seals on Run and cannot
// be reached from the worker goroutine.
func (m *V2SessionManager) attachmentReplyError(ctx context.Context, s *V2Session, inReplyTo uint64, rej attachmentReject) {
	payload, err := json.Marshal(protocol.ErrorPayload{
		Code:      rej.code,
		Message:   rej.message,
		Retryable: rej.retryable,
	})
	if err != nil {
		// A closed struct of strings + bool; marshal cannot fail in practice.
		m.cfg.Logger.Warn("relay: v2 attachment error reply marshal failed",
			"event", "v2.attachment.err_marshal",
			"conn_id", s.connID,
			"code", rej.code)
		return
	}
	m.attachmentReply(ctx, s, inReplyTo, protocol.TypeError, payload)
}

// attachmentReply hands one reply to Run for sealing, correlated to inReplyTo.
//
// It goes through forwardToRun rather than m.forwardEnvelope because it runs on
// the appFrameWorker: s.send is the single-owner Noise send CipherState and a
// concurrent Encrypt would reuse a nonce. forwardToRun parks the reply on
// m.appReply, where Run's forwardAppReply seals it — the same route every v1
// handler reply already takes, so this path adds no second sealing site.
//
// A false return means the session or the manager is tearing down; the reply is
// abandoned exactly as routeAppFrame abandons its drain, and forwardAppReply's own
// V2StateOpen and transportDown gates may still drop it after that.
func (m *V2SessionManager) attachmentReply(ctx context.Context, s *V2Session, inReplyTo uint64, typ string, payload json.RawMessage) {
	reply := protocol.Envelope{
		ID:        1, // non-load-bearing; the phone correlates on InReplyTo.
		Type:      typ,
		TS:        time.Now().UTC(),
		Payload:   payload,
		InReplyTo: &inReplyTo,
	}
	frame, err := json.Marshal(reply)
	if err != nil {
		// payload is already valid JSON and the rest is a closed struct; marshal
		// cannot fail in practice.
		m.cfg.Logger.Warn("relay: v2 attachment reply marshal failed",
			"event", "v2.attachment.reply_marshal",
			"conn_id", s.connID,
			"reply_type", typ)
		return
	}
	if !m.forwardToRun(ctx, s, protocol.RoutingEnvelope{ConnID: s.connID, Frame: frame}) {
		m.cfg.Logger.Debug("relay: v2 attachment reply dropped; session tearing down",
			"event", "v2.attachment.reply_dropped",
			"conn_id", s.connID,
			"reply_type", typ)
	}
}

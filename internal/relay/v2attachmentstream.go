package relay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/pyrycode/pyrycode/internal/attachments"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// This file holds the OUTBOUND half of the retrieval leg (#2053): the pure
// chunker, the stream entry point, and the receiver-contract oracle. It ships
// UNWIRED — nothing in the daemon calls StreamAttachment when it lands, exactly
// as #812 shipped StreamBundle ahead of the request verb that drove it (#813).
// The handler that joins a request to this stream is #2054.
//
// WHAT IT COPIES FROM v2bundlestream.go AND WHAT IT DOES NOT. The transport
// mechanics are copied: envelopes go out through the manager's asynchronous
// Push path rather than the small per-handler reply channel, so a payload of any
// size cannot overrun that buffer. The chunking arithmetic is NOT copied, and
// three differences are load-bearing rather than incidental:
//
//   - A ZERO-BYTE FILE IS ONE CHUNK, NOT ZERO. bundleEnvelopes yields zero
//     chunks for an empty blob, which is right for a stream terminated by a
//     completion marker and wrong for one that is not.
//   - THERE IS NO COMPLETION FRAME, and none is coming. TotalChunks rides every
//     chunk, so a receiver knows the expected count from the first frame it sees
//     and spots a truncated stream earlier than a terminal marker would allow.
//     DebugBundleDonePayload exists only because a bundle chunk carries a bare
//     Seq and the count is unknowable until the end.
//   - THE STRIDE IS EXACT. Every chunk but the last carries exactly
//     protocol.MaxAttachmentChunkBytes raw bytes.
//
// The stride is not stylistic. attachments.CheckDeclaration enforces
// TotalChunks == max(1, ceil(Size / bound)) as an EQUALITY, so a stream chunked
// at any other size is one this project's own receiver refuses. The bound is
// read from protocol.MaxAttachmentChunkBytes at every use and never copied as a
// local 45000, for CheckDeclaration's own stated reason: one place it can drift.

// attachmentEnvelopes splits blob into max(1, ceil(len(blob)/bound))
// TypeAttachmentChunk envelopes with 0-based ascending Index, each carrying all
// eight published fields and each correlated to inReplyTo. Pure — no manager and
// no filesystem — which is what lets the published shape rules be tested without
// standing up a session.
//
// THREE OF THE EIGHT FIELDS ARE DERIVED FROM THE BYTES, and that is a finding
// about storage rather than a preference. Intake.Receive calls Store(dir,
// chunk.Filename, data), which persists the bytes under the SANITISED filename
// and nothing else: the upload's declared mime_type is discarded outright, and
// its declared size and sha256 are checked at admission against the assembled
// bytes and then dropped. The frame requires all eight fields on every chunk, so
// the daemon derives what it did not store — size and digest from the bytes, and
// the media type from the bytes too, so a file whose name and client-declared
// type disagree with its content is described by its content. filename is the
// caller's, which is the leaf of the path ResolvePath answered and therefore
// SanitizeFilename's rendering rather than the client's own bytes.
//
// http.DetectContentType is the stdlib WHATWG mimesniff implementation. It reads
// at most the first 512 bytes and is TOTAL — application/octet-stream is the
// fallback and an empty input answers text/plain; charset=utf-8, which is a
// consequence of the algorithm rather than a choice and is pinned by a test.
// mime.TypeByExtension is deliberately NOT used: the name is exactly the thing
// that must not decide the answer.
//
// ITS OUTPUT IS DRAWN FROM A CLOSED SET OF DAEMON-AUTHORED CONSTANTS — the fixed
// signature table plus those two defaults — so an attacker choosing the file's
// bytes selects WHICH of those strings is emitted and cannot inject text into
// it. That is the concrete difference from the inbound declared value, an
// arbitrary 255-byte client string, and it is why the derived field carries
// neither the log-injection hazard nor the envelope-budget hazard the declared
// one does. It does NOT make the value trustworthy to act on: it is still
// computed from bytes an attacker chose, so a sniffed text/html is exactly as
// dangerous to render as a declared one, and docs/protocol-mobile.md
// § Attachments keeps its MUST NOT dispatch rule unchanged.
//
// DATA IS NEVER NIL. encoding/json renders a nil []byte as null and an empty
// non-nil one as "", and the published field is a base64 STRING that is always
// present. Slicing a nil blob yields a nil slice, so the zero-byte chunk's Data
// is normalised explicitly; without it a conforming zero-byte transfer would
// carry "data":null.
//
// EventID is left nil on every envelope, so forwardEnvelope's reconnect-replay
// dedup guard is inert for these frames, matching bundleEnvelopes. ID is
// non-load-bearing (set to Index for debuggability only); a receiver correlates
// on InReplyTo and keys the transfer on AttachmentID, never on ID. Content bytes
// never touch the logs — the raw slice lives only in the sealed payload.
func attachmentEnvelopes(attachmentID, filename string, blob []byte, inReplyTo uint64) ([]protocol.Envelope, error) {
	const bound = protocol.MaxAttachmentChunkBytes

	// Division-then-remainder rather than (len + bound - 1) / bound, the form
	// CheckDeclaration bans outright for wrapping near math.MaxInt64. Overflow
	// is not reachable here — the upload leg bounds one stored attachment at 16
	// MiB — so this is the two sides of one contract reading the same way rather
	// than a defence against an input this function can receive.
	n := len(blob) / bound
	if len(blob)%bound != 0 {
		n++
	}
	// The published max(1, …), reachable only at len(blob) == 0. It is the whole
	// definition of the zero-byte file: one chunk carrying zero bytes, against a
	// bare ceiling's 0 and the documented total_chunks >= 1.
	n = max(n, 1)

	digest := sha256.Sum256(blob)
	// The whole transfer's metadata, repeated identically on every chunk. Index
	// and Data are the only fields that vary.
	meta := protocol.AttachmentChunkPayload{
		AttachmentID: attachmentID,
		TotalChunks:  n,
		Filename:     filename,
		MimeType:     http.DetectContentType(blob),
		Size:         int64(len(blob)),
		SHA256:       hex.EncodeToString(digest[:]),
	}

	now := time.Now().UTC()
	envs := make([]protocol.Envelope, 0, n)
	for index := 0; index < n; index++ {
		start := index * bound
		chunk := meta
		chunk.Index = index
		chunk.Data = blob[start:min(start+bound, len(blob))]
		if chunk.Data == nil {
			chunk.Data = []byte{}
		}
		payload, err := json.Marshal(chunk)
		if err != nil {
			// Defensive: a closed struct of strings, ints and a byte slice does
			// not fail to marshal in practice — invalid UTF-8 in filename is
			// replaced rather than refused, and there is no value encoding/json
			// rejects. NO CONTENT BYTES IN THE ERROR, counts only.
			return nil, fmt.Errorf("marshal attachment chunk %d of %d: %w", index, n, err)
		}
		// One variable per iteration: every frame owns its own pointer, so no
		// frame can be re-correlated by a mutation through another's.
		reply := inReplyTo
		envs = append(envs, protocol.Envelope{
			ID:        uint64(index),
			Type:      protocol.TypeAttachmentChunk,
			TS:        now,
			Payload:   payload,
			InReplyTo: &reply,
		})
	}
	return envs, nil
}

// StreamAttachment moves one stored attachment to the addressed, open,
// authenticated v2 conn as a conforming stream of attachment_chunk frames, each
// correlated to the request that asked for it. It reads the whole file, builds
// the envelopes with attachmentEnvelopes, then enqueues each in order via Push —
// the manager's own asynchronous send path (Push → drainOnce → forwardEnvelope),
// the same path every unsolicited daemon → client frame uses. It deliberately
// does NOT use the per-frame handler-reply channel (handlerOutboundBuf = 8, a
// small fixed buffer sized for the 1-few-reply request/response case), so a file
// of any size cannot overrun it.
//
// PRECONDITION, AND IT CARRIES THE WHOLE SECURITY PROPERTY: path must be one
// attachments.ResolvePath answered for the conversation the authenticated
// session is already on, and attachmentID must be the id those bytes are stored
// under. This function resolves nothing and validates neither — the same
// trusts-its-caller posture Store documents for the dir EnsureDir returned, and
// for the same reason: re-deriving here would fork a check that has to happen
// before a path is built at all, and the reject vocabulary that answers a failed
// check (attachment.not_found and attachment.stream_aborted alike) belongs to
// the handler that can correlate a reject to a request. That handler is #2054.
//
// TWO CONSEQUENCES THE CALLER HAS TO HONOUR, stated because nothing here would
// notice either. A path this function did not get from ResolvePath carries NO
// CONTAINMENT GUARANTEE: every traversal defence is that function's full-path
// equality check. And a path naming a NON-REGULAR FILE misbehaves rather than
// erroring — os.ReadFile on a FIFO blocks indefinitely, wedging whichever
// goroutine the caller runs this on. ResolvePath answers only entries whose
// Type().IsRegular() holds and only a path equal to the one its two ids build,
// so both are unreachable through the sanctioned caller; a guard here would be a
// second, weaker copy of a check that already exists.
//
// A whole-file read rather than a streaming one: the upload leg bounds one
// stored attachment at 16 MiB (maxUploadBytes in internal/attachments), so the
// read is comfortably bounded and a streaming read would buy nothing but a
// second failure mode mid-stream.
//
// Safe to call from any goroutine, including a future #2054 handler on the Run
// goroutine or on a conn's appFrameWorker: Push never blocks and never touches
// s.send, so this sidesteps both the frame-cap wall and the handler-buffer wall,
// and no concurrent Encrypt on the single-owner send state is reachable however
// many callers stream at once. Attachment frames are control-class (not
// TypeAssistantDelta), so the pushQueue drop policy never evicts them — all
// chunks are delivered, in order.
//
// Returns on successful ENQUEUE, not delivery (delivery is async on Run; a
// per-frame seal/forward failure is logged at debug by drainOnce). That is
// precisely why attachment.stream_aborted is #2054's to emit and not this
// slice's: a failure after enqueue is invisible here. Returns the first Push
// error and stops on it, since Push answers ErrConnNotFound for a conn that is
// not open and there is no point continuing.
//
// SECURITY — the never-log rule, and it binds the RETURNED ERROR as well as the
// log call. protocol.AttachmentChunkPayload's SECURITY block and
// docs/protocol-mobile.md § Attachments fix the loggable set: conn id,
// attachment id, index and total may be logged; the streamed bytes, the
// filename, the digest and the host path never. ResolvePath's doc block adds
// that the path it returns is NOT fully daemon-authored — its leaf is a
// sanitised client filename — so it is no more loggable than the raw name is.
// os.ReadFile returns an *fs.PathError whose Error() prints that path, so a
// caller logging this function's error verbatim would defeat the rule through
// it; the error below is rebuilt around the STRIPPED cause instead, which keeps
// errors.Is(err, fs.ErrNotExist) working for #2054 while making the leak
// unavailable. Logs one content-free debug line on success — counts only.
func (m *V2SessionManager) StreamAttachment(ctx context.Context, connID, attachmentID, path string, inReplyTo uint64) error {
	blob, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("stream attachment %s: read stored file: %w", attachmentID, strippedPathError(err))
	}
	// filepath.Base, not the whole path: the leaf is the display name the
	// retrieval leg publishes, and the directory components are the daemon's own
	// layout, which no frame may disclose.
	return m.streamAttachmentBytes(ctx, connID, attachmentID, filepath.Base(path), blob, inReplyTo)
}

// streamAttachmentBytes is StreamAttachment without the read: it chunks bytes
// the caller already holds and enqueues them in order via Push. Split out for
// the live workspace read (#2598), whose bytes come from readChecked — reopening
// them by path, as StreamAttachment does, would discard that function's
// descriptor identity check. Same error set as StreamAttachment past its read,
// so attachmentStreamAborted classifies both callers alike.
func (m *V2SessionManager) streamAttachmentBytes(ctx context.Context, connID, attachmentID, filename string, blob []byte, inReplyTo uint64) error {
	envs, err := attachmentEnvelopes(attachmentID, filename, blob, inReplyTo)
	if err != nil {
		return err
	}
	for _, env := range envs {
		if err := m.Push(ctx, connID, env); err != nil {
			return err
		}
	}
	m.cfg.Logger.Debug("relay: v2 attachment stream enqueued",
		"event", "v2.attachment.stream",
		"conn_id", connID,
		"attachment_id", attachmentID,
		"in_reply_to", inReplyTo,
		"chunks", len(envs),
		"bytes", len(blob))
	return nil
}

// strippedPathError answers the underlying cause of a filesystem error with the
// path removed. *fs.PathError's Error() prints its Op and Path, and the path an
// attachment lives at ends in a client filename that must not be logged, so the
// wrapped cause is the only part safe to carry outward. The bare errno still
// satisfies errors.Is against fs.ErrNotExist and friends, so a caller loses
// nothing it can branch on.
func strippedPathError(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) && pe.Err != nil {
		return pe.Err
	}
	return err
}

// ReassembleAttachment is the receiver-contract reference and test oracle: it
// walks frames in arrival order, reconstructs one transfer's file, or fails
// cleanly. It is pure and exported — run in production only phone-side (out of
// repo); the daemon has no inbound caller. It is what makes "reassembling them
// in index order recovers the file byte for byte" testable at all, since that is
// a statement about a reassembler, and it is ReassembleBundle's counterpart
// without that function's contract.
//
// IT SELECTS ONE TRANSFER BY BOTH CORRELATORS, which is the division
// § attachment_stored records: in_reply_to says WHICH FRAME THIS ANSWERS and the
// payload id says WHICH TRANSFER IT BELONGS TO. A frame answering another
// request is skipped, exactly as ReassembleBundle skips a non-bundle frame, so
// concurrent retrievals over one connection separate cleanly. A frame answering
// THIS request while naming another transfer is REJECTED rather than skipped:
// that is the daemon answering the right ask with the wrong bytes, and it is the
// failure the payload id catches and in_reply_to cannot — which is what makes
// the two non-redundant rather than belt-and-braces.
//
// THE PUBLISHED RECEIVER RULES, applied to the selected set:
//
//   - NEVER ALLOCATE FROM A CLAIM. TotalChunks and Size are integers chosen by
//     whoever sent the frame, so attachments.CheckDeclaration cross-checks them
//     through the published per-chunk bound on the FIRST selected chunk, before
//     anything is sized. Reusing the project's own enforcement point rather than
//     re-deriving the equality keeps this a reference to the real contract.
//   - A receiver ADDRESSES BY INDEX and never appends, so chunks may arrive in
//     ANY ORDER. That is deliberately weaker than debug_bundle_chunk's Seq,
//     which demands strict succession — the neighbouring rule is the obvious
//     thing to copy and it is the wrong one here. A duplicate index, an index
//     outside [0, TotalChunks), or a declaration disagreeing with the transfer's
//     earlier chunks is refused.
//   - Completion is every index in [0, TotalChunks) having arrived exactly once.
//     Only then is the assembled length compared against Size and
//     sha256(assembled) against SHA256, as lowercase hex for EXACT EQUALITY.
//
// On ANY failure it returns (nil, err) — never partial or corrupted bytes. No
// error it builds names the filename or the digest, both of which ride the
// frames it is reading.
func ReassembleAttachment(frames []protocol.Envelope, attachmentID string, inReplyTo uint64) ([]byte, error) {
	var (
		parts   [][]byte
		arrived []bool
		total   int
		size    int64
		digest  string
		seen    int
		started bool
	)
	for _, f := range frames {
		if f.Type != protocol.TypeAttachmentChunk || f.InReplyTo == nil || *f.InReplyTo != inReplyTo {
			// Another transfer's frame, or an interleaved control frame the
			// transport put in the way; the real phone filters the same way.
			continue
		}
		var p protocol.AttachmentChunkPayload
		if err := json.Unmarshal(f.Payload, &p); err != nil {
			return nil, fmt.Errorf("reassemble attachment: decode chunk: %w", err)
		}
		if p.AttachmentID != attachmentID {
			return nil, fmt.Errorf("reassemble attachment: a frame answering request %d names another transfer", inReplyTo)
		}

		if !started {
			// Before anything is sized. The equality this checks is the same one
			// the sender's stride is built to satisfy.
			if err := attachments.CheckDeclaration(p.TotalChunks, p.Size); err != nil {
				return nil, fmt.Errorf("reassemble attachment: %w", err)
			}
			total, size, digest = p.TotalChunks, p.Size, p.SHA256
			parts = make([][]byte, total)
			arrived = make([]bool, total)
			started = true
		} else if p.TotalChunks != total || p.Size != size || p.SHA256 != digest {
			return nil, fmt.Errorf("reassemble attachment: chunk %d declares a different transfer", p.Index)
		}

		if p.Index < 0 || p.Index >= total {
			return nil, fmt.Errorf("reassemble attachment: index %d outside [0, %d)", p.Index, total)
		}
		// arrived rather than a nil check on parts: a zero-byte chunk's decoded
		// Data is an empty slice, and whether that is nil is an encoding/json
		// implementation detail no correctness argument should rest on.
		if arrived[p.Index] {
			return nil, fmt.Errorf("reassemble attachment: duplicate index %d", p.Index)
		}
		parts[p.Index] = p.Data
		arrived[p.Index] = true
		seen++
	}

	if !started {
		return nil, fmt.Errorf("reassemble attachment: no chunk answering request %d", inReplyTo)
	}
	if seen != total {
		return nil, fmt.Errorf("reassemble attachment: incomplete, %d of %d chunks", seen, total)
	}

	// Sized from what actually arrived, never from the declared Size: the
	// declaration has been cross-checked but the bytes are the only thing that
	// has been counted.
	n := 0
	for _, part := range parts {
		n += len(part)
	}
	out := make([]byte, 0, n)
	for _, part := range parts {
		out = append(out, part...)
	}
	if int64(len(out)) != size {
		return nil, fmt.Errorf("reassemble attachment: assembled %d bytes against a declared size of %d", len(out), size)
	}
	sum := sha256.Sum256(out)
	// Exact equality on lowercase hex. A prefix or case-insensitive comparison
	// would be a hole; the computed digest is not named in the error, because
	// § Attachments bans logging a digest and a caller may log this.
	if hex.EncodeToString(sum[:]) != digest {
		return nil, fmt.Errorf("reassemble attachment: assembled bytes do not match the declared digest")
	}
	return out, nil
}

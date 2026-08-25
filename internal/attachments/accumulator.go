// Package attachments holds one inbound attachment upload's chunks in memory
// while its attachment_chunk frames arrive, and refuses a stream whose framing
// contradicts what the transfer declared.
//
// The receiver's rules are published in docs/protocol-mobile.md § Attachments,
// "Reassembly & integrity (the receiver's rules)": each chunk's decoded data is
// stored AT ITS INDEX, so chunks may arrive in ANY ORDER, and the transfer is
// complete once every index in [0, total_chunks) has arrived exactly once. That
// is deliberately weaker than the bundle stream's strict succession rule in
// relay.ReassembleBundle, which is the neighbouring rule a reader would
// otherwise copy and which the published contract warns against by name. What
// this package does copy from ReassembleBundle is its all-or-nothing posture:
// it yields the complete bytes or it fails cleanly, and never partial or
// corrupted output.
//
// In-memory only. Nothing here touches the filesystem, reads a socket, or emits
// a wire code, and the package makes zero log calls. The three framing refusals
// are Go sentinels so the daemon's logs and this package's tests can tell them
// apart; mapping all three to the single attachment.invalid_chunk wire code is
// the dispatch site's job (#1744), which is also the only place that knows the
// attachment id and conn id worth logging.
//
// The receiver's resource bounds — how many uploads may be in flight, how many
// bytes one may accumulate, and the first-chunk total_chunks/size cross-check
// that refuses before allocating — are #1767's, and admission runs before an
// Accumulator exists. What this slice bounds on its own is the KEY SPACE: the
// range check means no accumulator ever holds more entries than the count it
// was constructed with, and nothing here is ever sized from a claim.
package attachments

import (
	"errors"
	"fmt"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// Sentinel errors returned by Add and Assemble. Callers distinguish them with
// errors.Is, never by comparing error strings: every refusal is returned
// wrapped with the offending numbers.
//
// The first three are framing refusals and all three map to the one wire code
// protocol.CodeAttachmentInvalidChunk — at #1744's dispatch site, not here. The
// many-to-one mapping is deliberate: distinguishability is wanted in-process,
// for these tests and for the daemon's logs, not on the wire.
var (
	// ErrTotalChunksMismatch reports a chunk whose declared total disagrees
	// with the count the transfer was admitted under. Discards the transfer.
	ErrTotalChunksMismatch = errors.New("attachments: chunk total_chunks disagrees with the transfer's declared count")

	// ErrIndexOutOfRange reports a chunk index outside [0, total_chunks),
	// negative or too large alike. Discards the transfer.
	ErrIndexOutOfRange = errors.New("attachments: chunk index outside [0, total_chunks)")

	// ErrDuplicateIndex reports a chunk index that has already arrived, whether
	// or not it repeats the bytes already held. Discards the transfer.
	ErrDuplicateIndex = errors.New("attachments: chunk index already received")

	// ErrIncomplete reports that at least one index has not arrived yet. It is
	// NOT a refusal: it does not discard the transfer, and a later Add can turn
	// it into bytes. It is the one sentinel here a caller must not map to a
	// client-visible reject code.
	ErrIncomplete = errors.New("attachments: transfer incomplete")
)

// Accumulator holds one attachment transfer's chunks, addressed by index, until
// every index has arrived. Feed it one chunk at a time with Add; ask Assemble
// whether the transfer is complete and, if it is, for its bytes.
//
// NOT SAFE FOR CONCURRENT USE, and it deliberately carries no mutex. One
// Accumulator is owned by one session and fed serially: the frames that drive
// it arrive on relay's appFrameWorker, exactly one goroutine per session with
// strict FIFO ordering and no two handlers for one conn running concurrently.
// That is a CALLER OBLIGATION rather than a happy accident. Synchronising the
// registry of in-flight uploads belongs to the slices that build it (#1767,
// #1744) and to the release path (#1742), never to this type.
type Accumulator struct {
	// totalChunks is the transfer's declared chunk count. Latched at
	// construction from the admission decision rather than learned from the
	// stream, so even the transfer's very first chunk can be refused for
	// disagreeing with it.
	totalChunks int

	// size is the declared byte length of the whole file. Latched here because
	// it comes from the admission decision too; unread by this slice, read by
	// the sibling that checks the assembled bytes against it (#1770).
	size int64

	// sha256 is the declared lowercase-hex digest of the whole file. Latched
	// for the same reason as size, and read by the same sibling (#1770).
	sha256 string

	// chunks holds each arrived chunk's bytes at its index. PRESENCE IS THE
	// KEY, never the bytes: a zero-byte attachment is one chunk whose Data is
	// legitimately nil, so the duplicate check uses the two-value map lookup
	// and completion counts entries — neither ever tests a value for nil.
	chunks map[int][]byte

	// rejected latches the first framing refusal, wrapped with the offending
	// numbers. Once set, the transfer is discarded: every later Add and every
	// Assemble answers with this identical error value and never with bytes.
	rejected error
}

// NewAccumulator latches the transfer's three declared facts and returns an
// empty accumulator. It cannot fail: refusing an implausible declaration is the
// admission layer's job (#1767), which runs before this type exists.
//
// The chunk map is created WITHOUT a capacity hint. On the inbound leg
// totalChunks is an attacker-chosen integer — see the NEVER ALLOCATE FROM A
// CLAIM block on protocol.AttachmentChunkPayload — and
// make(map[int][]byte, totalChunks) on a claimed 2^31-1 pre-allocates the
// bucket array from a single ~60 KB frame, which is that attack wearing a shape
// the block does not literally spell out. Storage grows with what actually
// arrives, which is what makes this slice safe standing alone, before #1767.
func NewAccumulator(totalChunks int, size int64, sha256 string) *Accumulator {
	return &Accumulator{
		totalChunks: totalChunks,
		size:        size,
		sha256:      sha256,
		chunks:      make(map[int][]byte),
	}
}

// Add stores one chunk's bytes at its index, or refuses the chunk and discards
// the transfer. Once refused the transfer stays refused: every later Add and
// every later Assemble answers with the identical error value, so the offending
// numbers survive into whatever the dispatch site logs.
//
// The checks run in a fixed order, and the order is part of the contract — a
// frame carrying two faults yields one deterministic sentinel:
//
//  1. the transfer is already refused → the latched error, verbatim.
//  2. the chunk's total disagrees with the declared count →
//     ErrTotalChunksMismatch. First, because a disagreeing count invalidates
//     the frame of reference the range check below uses. This is stronger than
//     the published "disagreeing with the stream's earlier chunks": the
//     comparison is against the latched declaration, so the first chunk of a
//     transfer can be refused for it.
//  3. the index falls outside [0, declared count) → ErrIndexOutOfRange.
//  4. the index has already arrived → ErrDuplicateIndex. There is no
//     idempotent-retry carve-out: completion is every index arriving EXACTLY
//     ONCE, so a re-send is refused even when it repeats the bytes already
//     held. Accepting it would also let a client probe which indices the
//     receiver holds by observing which re-sends are accepted.
//
// Add reads exactly three of the payload's eight fields — Index, TotalChunks
// and Data — and the omissions are deliberate rather than oversights.
// AttachmentID is not checked because the caller looks this accumulator up BY
// it, so a foreign chunk cannot reach here and a check would invent a fourth
// framing sentinel no rule covers. Size and SHA256 are not checked because both
// are latched from the admitted transfer, so a later chunk restating either one
// differently changes nothing that is checked — that is the security property,
// and it is why only a total disagreement is a framing refusal. Filename and
// MimeType are never read at all.
//
// PRECONDITION: chunk.Data is retained WITHOUT being copied, so the caller must
// not mutate it, nor decode into a reused buffer, after Add returns. That holds
// for today's caller because encoding/json allocates a fresh slice for each
// base64 field. A defensive copy is deliberately not made: it would double peak
// memory for every upload against the byte bound #1767 introduces.
func (a *Accumulator) Add(chunk protocol.AttachmentChunkPayload) error {
	if a.rejected != nil {
		return a.rejected
	}
	if chunk.TotalChunks != a.totalChunks {
		return a.reject(fmt.Errorf("attachments: chunk declares total_chunks %d, transfer declared %d: %w",
			chunk.TotalChunks, a.totalChunks, ErrTotalChunksMismatch))
	}
	if chunk.Index < 0 || chunk.Index >= a.totalChunks {
		return a.reject(fmt.Errorf("attachments: chunk index %d outside [0, %d): %w",
			chunk.Index, a.totalChunks, ErrIndexOutOfRange))
	}
	if _, dup := a.chunks[chunk.Index]; dup {
		return a.reject(fmt.Errorf("attachments: chunk index %d of %d already received: %w",
			chunk.Index, a.totalChunks, ErrDuplicateIndex))
	}
	a.chunks[chunk.Index] = chunk.Data
	return nil
}

// reject latches err as the transfer's refusal, releases the bytes already held
// and returns err, so a caller reads as `return a.reject(...)`.
//
// Dropping the map at the moment of refusal is what makes "discarded rather
// than continued" true of the MEMORY and not only of the answers: without it a
// hostile client could park held bytes behind a poisoned accumulator until
// #1742's reaper runs.
//
// The wrapped message carries the index and the counts only. Data is a user's
// private file bytes and Filename is both private in itself and a
// log-injection shape in a line-oriented log, so neither ever enters an error
// string — the rules protocol.AttachmentChunkPayload's doc block states, here
// honoured by its first consumer.
func (a *Accumulator) reject(err error) error {
	a.rejected = err
	a.chunks = nil
	return err
}

// Assemble returns the transfer's bytes once every index in
// [0, declared count) has arrived exactly once. It neither consumes nor mutates
// the accumulator, so an incomplete transfer can be completed by a later Add
// and assembled then.
//
// A refused transfer never yields bytes, however complete it looks: the latched
// refusal is returned instead.
//
// ErrIncomplete IS NOT A REFUSAL. It neither latches nor discards, and a later
// chunk can turn it into bytes. That single difference is why this type is
// stateful where relay.ReassembleBundle is one pure call taking every frame at
// once, whose "incomplete" no later frame can revisit.
//
// A declared count below 1 is never complete. Without that clause the
// exactly-once test is vacuously true for a declared count of zero, and such a
// transfer would assemble to empty bytes — a success from a declaration the
// published contract forbids (total_chunks >= 1). It is not a fourth sentinel:
// no index is admissible for such a transfer, so permanently incomplete is the
// honest answer, and refusing the declaration itself is #1767's decision to
// make once, at admission.
//
// The returned slice is freshly allocated on every call and sized from the SUM
// OF THE ARRIVED CHUNK LENGTHS, never from the declared size — sizing from a
// claim is the same attack the constructor's hint-free map avoids. The
// single-chunk case is deliberately not fast-pathed to the stored slice either:
// a caller that mutates what it gets back must not be able to corrupt the
// accumulator or a second caller's copy.
//
// The assembled bytes are returned UNCHECKED against the transfer's declared
// size and sha256; that comparison is #1770's, and nothing consumes these bytes
// until #1744 wires the dispatch site.
func (a *Accumulator) Assemble() ([]byte, error) {
	if a.rejected != nil {
		return nil, a.rejected
	}
	if a.totalChunks < 1 || len(a.chunks) != a.totalChunks {
		return nil, ErrIncomplete
	}
	n := 0
	for _, data := range a.chunks {
		n += len(data)
	}
	out := make([]byte, 0, n)
	for i := 0; i < a.totalChunks; i++ {
		out = append(out, a.chunks[i]...)
	}
	return out, nil
}

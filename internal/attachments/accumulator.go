// Package attachments holds one inbound attachment upload's chunks in memory
// while its attachment_chunk frames arrive, refuses a stream whose framing
// contradicts what the transfer declared, and yields the assembled bytes only
// once they match the size and the digest the transfer declared.
//
// The receiver's rules are published in docs/protocol-mobile.md § Attachments,
// "Reassembly & integrity (the receiver's rules)": each chunk's decoded data is
// stored AT ITS INDEX, so chunks may arrive in ANY ORDER, the transfer is
// complete once every index in [0, total_chunks) has arrived exactly once, and
// only then is the assembled length compared against the declared size and its
// lowercase-hex sha256 against the declared digest. That is deliberately weaker
// than the bundle stream's strict succession rule in
// relay.ReassembleBundle, which is the neighbouring rule a reader would
// otherwise copy and which the published contract warns against by name. What
// this package does copy from ReassembleBundle is its all-or-nothing posture:
// it yields the complete bytes or it fails cleanly, and never partial or
// corrupted output.
//
// In-memory for accumulation and admission: nothing on those paths touches the
// filesystem, and EnsureDir — which resolves and creates the directory an
// attachment is filed under — is the sole function here that does. Nothing
// reads a socket or emits a wire code, and the package makes zero log calls,
// EnsureDir included. Every refusal is a Go
// sentinel so the daemon's logs and this package's tests can tell them apart;
// mapping the three framing refusals to the single attachment.invalid_chunk
// wire code and the two integrity refusals to attachment.integrity_failed is
// the dispatch site's job (#1744), which is also the only place that knows the
// attachment id and conn id worth logging.
//
// The receiver's resource bounds are owned three separate ways, and admission
// runs before an Accumulator exists: how many uploads may be in flight is
// maxInFlightUploads, enforced by Registry's admission gate; how many bytes one
// may accumulate is maxUploadBytes, enforced twice
// in this package — the declared size at CheckDeclaredSize and the accumulated
// bytes at step 5 of Add — and the first-chunk total_chunks/size cross-check
// that refuses before allocating is CheckDeclaration, also in this package. What
// this package bounds on its own is the KEY SPACE: the range check means no
// accumulator ever holds more entries than the count it was constructed with,
// and nothing here is ever sized from a claim. With both admission checks run
// that claim is now absolute rather than merely relative — an admitted transfer
// declares at most 373 chunks, since maxUploadBytes at
// protocol.MaxAttachmentChunkBytes needs no more.
package attachments

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// Sentinel errors returned by Add and Assemble. Callers distinguish them with
// errors.Is, never by comparing error strings: every refusal is returned
// wrapped with the offending numbers.
//
// The first three are framing refusals and all three map to the one wire code
// protocol.CodeAttachmentInvalidChunk; the next two are integrity refusals and
// both map to protocol.CodeAttachmentIntegrityFailed. ErrIncomplete maps to
// nothing. Every one of those mappings happens at #1744's dispatch site, not
// here — this package still does not import codes.go. The many-to-one shape is
// deliberate in both families: distinguishability is wanted in-process, for
// these tests and for the daemon's logs, not on the wire.
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

	// ErrSizeMismatch reports assembled bytes whose length differs from the
	// transfer's declared size. Discards the transfer.
	ErrSizeMismatch = errors.New("attachments: assembled length differs from the declared size")

	// ErrDigestMismatch reports assembled bytes whose lowercase-hex sha256
	// differs from the transfer's declared digest. Discards the transfer.
	ErrDigestMismatch = errors.New("attachments: assembled sha256 differs from the declared digest")

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
// registry of in-flight uploads belongs to Registry, which holds its own mutex
// across each whole operation, the release path included — never to this type.
type Accumulator struct {
	// totalChunks is the transfer's declared chunk count. Latched at
	// construction from the admission decision rather than learned from the
	// stream, so even the transfer's very first chunk can be refused for
	// disagreeing with it.
	totalChunks int

	// size is the declared byte length of the whole file. Latched here because
	// it comes from the admission decision too, and read by Assemble as the
	// right operand of one comparison against the assembled length — never as
	// the size of anything allocated.
	size int64

	// sha256 is the declared lowercase-hex digest of the whole file. Latched
	// for the same reason as size, and read by Assemble as one string operand
	// of an exact-equality comparison. It is an attacker-supplied claim on the
	// inbound leg, so it is compared and never inspected, trusted or logged.
	sha256 string

	// chunks holds each arrived chunk's bytes at its index. PRESENCE IS THE
	// KEY, never the bytes: a zero-byte attachment is one chunk whose Data is
	// legitimately nil, so the duplicate check uses the two-value map lookup
	// and completion counts entries — neither ever tests a value for nil.
	chunks map[int][]byte

	// received is Σ len(data) of the chunks CURRENTLY HELD, maintained by Add
	// as it stores them. INVARIANT: 0 <= received <= maxUploadBytes, which is
	// what lets step 5's comparison be written as a subtraction that cannot
	// wrap.
	//
	// Never reset, a refusal included: reject drops the map and latches, and
	// every path out of a refused transfer short-circuits on that latch before
	// reaching this field, so zeroing it would be a write nothing can observe.
	//
	// It is NOT substitutable into Assemble, which looks like the one place a
	// running total would save work and is the one place it must not be used:
	// sizing or comparing from a counter maintained here would reintroduce
	// exactly the proxy "the bytes verified are the bytes returned" forbids, and
	// a later regression in the assembly loop would become invisible to a green
	// check.
	received int64

	// rejected latches the first refusal, framing or integrity, wrapped with
	// the offending numbers. Once set, the transfer is discarded: every later
	// Add and every Assemble answers with this identical error value and never
	// with bytes.
	rejected error
}

// NewAccumulator latches the transfer's three declared facts and returns an
// empty accumulator. It cannot fail: refusing an implausible declaration is
// CheckDeclaration's job, which runs before this type exists.
//
// Admission runs IN FRONT OF this constructor and does not replace its own
// safety. This function stays exported and constructible without passing
// through CheckDeclaration — the error return is the only signal, and no
// validated type carries the decision here — so nothing below may assume the
// declaration was ever checked.
//
// The chunk map is created WITHOUT a capacity hint. On the inbound leg
// totalChunks is an attacker-chosen integer — see the NEVER ALLOCATE FROM A
// CLAIM block on protocol.AttachmentChunkPayload — and
// make(map[int][]byte, totalChunks) on a claimed 2^31-1 pre-allocates the
// bucket array from a single ~60 KB frame, which is that attack wearing a shape
// the block does not literally spell out. Storage grows with what actually
// arrives, which is what makes the hint-free map safe standing alone —
// independently of whether the caller ran CheckDeclaration first.
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
//  5. the chunk's bytes would take the held total past maxUploadBytes →
//     ErrUploadTooLarge. This rung sits on the DATA PATH and so cannot be
//     skipped the way the two admission functions can, which is what bounds
//     retained memory even for a caller that ran neither.
//
// Three properties of step 5 are contract rather than preference:
//
//   - LAST, NOT FIRST. received counts only bytes this accumulator actually
//     retains, so the check belongs with the store rather than ahead of the
//     checks that decide whether a store happens at all. Its one observable
//     consequence is which sentinel a frame carrying two faults gets — every
//     unstored chunk rejects the transfer, so a counter that moved for one
//     could never be read afterwards — and placing it last leaves every
//     existing two-fault frame answering the sentinel it answers today.
//   - SUBTRACTION, NOT A SUM. received + len > bound forms a sum that could in
//     principle wrap; len > bound - received cannot, because the invariant on
//     received keeps the right operand within [0, maxUploadBytes]. The addend
//     here is a materialised slice length rather than a claimed number, so the
//     wrap is not reachable in practice — the safe form is written anyway, and
//     it is the INVARIANT rather than the unreachability doing the work, so a
//     later reader neither simplifies it back nor concludes that
//     CheckDeclaration's overflow discipline was cargo cult.
//   - `>` AND NOT `>=`. A transfer landing exactly on the bound is admitted:
//     the bound is a ceiling on what may be held, not on what may be
//     approached.
//
// Add reads exactly three of the payload's eight fields — Index, TotalChunks
// and Data — and the omissions are deliberate rather than oversights.
// AttachmentID is not checked because the caller looks this accumulator up BY
// it, so a foreign chunk cannot reach here and a check would invent a fourth
// framing sentinel no rule covers. Size and SHA256 are not checked HERE, because
// Assemble compares the LATCHED declaration against the assembled bytes rather
// than anything a chunk restates, so a later chunk carrying a different size or
// digest changes nothing that is checked — that is the security property, and it
// is why only a total disagreement is a framing refusal. Filename and MimeType
// are never read at all.
//
// PRECONDITION: chunk.Data is retained WITHOUT being copied, so the caller must
// not mutate it, nor decode into a reused buffer, after Add returns. That holds
// for today's caller because encoding/json allocates a fresh slice for each
// base64 field. A defensive copy is deliberately not made: it would double peak
// memory for every upload against maxUploadBytes, whose doc carries that
// arithmetic.
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
	if int64(len(chunk.Data)) > maxUploadBytes-a.received {
		return a.reject(fmt.Errorf("attachments: chunk %d carries %d bytes, transfer already holds %d, per-upload bound is %d: %w",
			chunk.Index, len(chunk.Data), a.received, maxUploadBytes, ErrUploadTooLarge))
	}
	a.chunks[chunk.Index] = chunk.Data
	a.received += int64(len(chunk.Data))
	return nil
}

// reject latches err as the transfer's refusal, releases the bytes already held
// and returns err, so a caller reads as `return a.reject(...)`. Both Add and
// Assemble call it: a framing fault and an integrity mismatch discard the
// transfer identically, and only ErrIncomplete does not.
//
// Dropping the map at the moment of refusal is what makes "discarded rather
// than continued" true of the MEMORY and not only of the answers: without it a
// hostile client could park held bytes behind a poisoned accumulator until the
// registry's idle reap took the entry a window later. Through Deliver no entry
// outlives a refusal, but Admit hands the accumulator back off-lock and a caller
// that feeds it directly and then stops parks exactly that. For an integrity
// refusal there is no reaper to fall back on at all — a complete-but-corrupt
// transfer is not a PARTIAL upload, Deliver releases its entry on the mismatch
// so no entry survives for a reap to find, and this line is the only thing that
// frees it.
//
// The wrapped message carries the index and the counts only, plus — for an
// integrity refusal — the digest this receiver computed and the CHARACTER COUNT
// of the declared claim. Data is a user's private file bytes, Filename is both
// private in itself and a log-injection shape in a line-oriented log, and the
// declared sha256 is an attacker-chosen string of that same class, so none of
// the three ever enters an error string — the rules
// protocol.AttachmentChunkPayload's doc block states, here honoured by its
// first consumer. The computed digest is safe to carry where the claim is not:
// it is daemon-authored, fixed in shape, one-way with respect to the content,
// and not a capability, because retrieval names a conversation and an
// attachment and never a hash.
func (a *Accumulator) reject(err error) error {
	a.rejected = err
	a.chunks = nil
	return err
}

// Assemble returns the transfer's bytes once every index in
// [0, declared count) has arrived exactly once AND the assembled bytes match
// both facts the transfer declared. It neither consumes nor mutates the
// accumulator, so an incomplete transfer can be completed by a later Add and
// assembled then, and a caller may assemble a complete one repeatedly.
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
// honest answer, and CheckDeclaration refuses such a declaration once, at
// admission. That does NOT make this clause newly dead: admission runs in front
// of this type rather than gating it, and NewAccumulator stays constructible
// without passing through CheckDeclaration, so this clause is still the only
// thing standing between such a caller and a vacuous success.
//
// The returned slice is freshly allocated on every call and sized from the SUM
// OF THE ARRIVED CHUNK LENGTHS, never from the declared size — sizing from a
// claim is the same attack the constructor's hint-free map avoids. The
// single-chunk case is deliberately not fast-pathed to the stored slice either:
// a caller that mutates what it gets back must not be able to corrupt the
// accumulator or a second caller's copy, and the fresh copy is also what keeps
// the two checks below from being a check-then-use over aliased memory — THE
// BYTES VERIFIED ARE THE BYTES RETURNED.
//
// Only once the transfer is complete are those bytes compared against what the
// transfer declared, per docs/protocol-mobile.md § Attachments, "Reassembly &
// integrity (the receiver's rules)": the assembled length against the declared
// size, then its lowercase-hex sha256 against the declared digest. Either
// mismatch discards the transfer exactly as a framing fault does, and no bytes
// are yielded — the receiver never emits partial or corrupted output. Both
// comparisons read the assembled slice itself rather than a proxy computed from
// the chunk map, so a later regression in the loop above cannot slip past a
// green check.
//
// THE LENGTH IS CHECKED FIRST, and the order is part of the contract rather
// than a preference. A digest mismatch is observable on its own — same length,
// different bytes — while every wrong-length stream also fails the digest, so
// no fixture can reach the digest check by being the wrong length alone.
// Checking the length first is what makes ErrSizeMismatch the answer a
// wrong-length stream gets at all; swapping the two is what the wrong-length
// row of TestAccumulator_IntegrityFaults_RejectAndDiscard exists to catch.
//
// The digest comparison is EXACT STRING EQUALITY, and softening it is the one
// change this method must not accept. internal/update's VerifySHA256 is the
// repo's nearest "bytes against a declared hex digest" comparison and folds
// case, which is defensible where it lives and a hole here: the published
// contract calls a case-insensitive or prefix comparison one, in the same
// sentence that mandates lowercase hex and answers the availability worry by
// obliging clients to send that form. There is likewise no well-formedness
// carve-out — an uppercase, truncated, non-hex or EMPTY claim simply loses the
// comparison — because the claim is attacker-supplied, so skipping the check
// for some claim value would hand the sender an opt-out from integrity checking
// while leaving the reject path looking live.
//
// A SUCCESSFUL RETURN IS NOT A SAFETY VERDICT. The same party supplied the
// bytes and the digest, so a match proves the transfer was not corrupted and
// proves nothing about whether the content is safe — integrity, not
// authenticity, as protocol.AttachmentChunkPayload's doc block states. The
// consumers of these bytes are the readers of that sentence: #1772's filename
// sanitiser, #1773's storage and #1746's retrieval.
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
	if int64(len(out)) != a.size {
		return nil, a.reject(fmt.Errorf("attachments: assembled %d bytes, transfer declared %d: %w",
			len(out), a.size, ErrSizeMismatch))
	}
	sum := sha256.Sum256(out)
	if actual := hex.EncodeToString(sum[:]); actual != a.sha256 {
		return nil, a.reject(fmt.Errorf("attachments: assembled sha256 %s, declared digest is %d characters: %w",
			actual, len(a.sha256), ErrDigestMismatch))
	}
	return out, nil
}

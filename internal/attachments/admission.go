package attachments

import (
	"errors"
	"fmt"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// maxUploadBytes is the hard ceiling on the RETAINED bytes of ONE attachment
// upload — Σ len(chunk.Data) over the chunks an Accumulator holds, which is
// exactly what it holds, because Add retains each chunk's slice without copying
// it. Two rungs enforce it and both answer ErrUploadTooLarge: CheckDeclaredSize
// refuses a declared size above it on the first chunk, before any bytes are
// held, and Add refuses the chunk that would take the held total past it.
//
// Derivation. The inbound population is phone-authored attachments —
// screenshots, photos, short documents, log excerpts. A 12 MP HEIC photo is
// single-digit MB and a 4K PNG screenshot is under 10 MB, so 16 MiB clears the
// largest of those with headroom while refusing what is plainly a file transfer
// wearing an attachment's clothes. 16777216 raw bytes is
// max(1, ceil(16777216 / 45000)) = 373 chunks at
// protocol.MaxAttachmentChunkBytes, the last of them 37216 bytes. It creates no
// new frame-size pressure: 45000 raw base64-encodes to exactly 60000 bytes,
// under the 65519-byte application-envelope cap, and that is a property of the
// chunk constant rather than of this one.
//
// WORST-CASE RESIDENT attachment bytes is this bound MULTIPLIED BY the
// concurrent-upload bound #1778 sets — 64 MiB at a concurrency of 4, 128 MiB at
// 8. The budget #1778 inherits rather than re-derives is to hold that PRODUCT at
// or under roughly 64 MiB, so four concurrent uploads at this number; wanting
// more concurrency moves one of the two numbers.
//
// Retained is not peak. Assemble allocates a fresh slice of the assembled
// length, so one upload peaks at 2× its retained bytes for the duration of that
// call, and the worst case above gains one more bound while an assembly is in
// flight. Add's refusal to copy each chunk is what keeps retained at 1× rather
// than 2×, which is the arithmetic its no-copy PRECONDITION cites.
//
// The crossing chunk's bytes are the one slack that is not slack: they already
// exist in the caller's decoded frame when Add measures them, so the transient
// overshoot is one frame — bounded by the 65519-byte envelope cap, never by this
// constant — and is never retained.
//
// It is NOT derived from relay's pushQueueByteCeiling, the repo's other
// retained-bytes ceiling, and copying that number would be a citation standing
// in for a derivation. That one is per-session push-queue bytes, set at ~2× a
// structural maximum (256 × 65519). This bound has no structural maximum to
// double: it is a policy pick from the file population, multiplied by a
// concurrency bound rather than by a queue cap. Same doc discipline, different
// derivation shape.
//
// Unexported because it is receiver policy that docs/protocol-mobile.md
// § Attachments deliberately leaves unpublished — the bounds are
// "receiver-configured and unpublished — a client learns them by being
// rejected". Untyped because its readers want different types: int64 in the
// comparisons, int in a test's make. A later ticket that publishes the number so
// a client can pre-flight a large file can export it then.
const maxUploadBytes = 16 << 20 // 16 MiB

// ErrInvalidDeclaration reports a transfer declaration this receiver refuses
// before an Accumulator exists. Every refusal CheckDeclaration returns wraps it
// with the offending numbers, and callers distinguish it with errors.Is rather
// than by comparing error strings.
//
// It is deliberately NOT one of the sentinels in accumulator.go's var block,
// whose own opening sentence scopes it to Add and Assemble: this one is raised
// by neither, and refuses the declaration a transfer would be admitted under
// rather than a chunk within an admitted one. ErrTotalChunksMismatch is the
// nearby sentinel a reader might reach for and is a different fault — it means
// a chunk disagrees with the count the transfer WAS admitted under, so it
// presupposes the admission this error withholds.
//
// One sentinel covers every refusal CheckDeclaration returns. ErrUploadTooLarge,
// the other sentinel in this file, is a deliberate second one rather than a
// widening of this: this one is committed to the attachment.invalid_chunk wire
// code, whose published repair is to RE-CHUNK, and that one to
// attachment.too_large, whose repair is to SHRINK THE FILE. Mapping either to
// its code is the dispatch site's job (#1744); this file emits no wire code and
// imports no codes.
var ErrInvalidDeclaration = errors.New("attachments: transfer declaration is not admissible")

// ErrUploadTooLarge reports an upload that exceeds this receiver's per-upload
// byte bound, maxUploadBytes, at either rung: a declared size above the bound
// (CheckDeclaredSize) or an arriving chunk that would take the held total past
// it (Add). Both wrap it with the offending numbers, and callers distinguish it
// with errors.Is rather than by comparing error strings.
//
// It is deliberately NOT folded into ErrInvalidDeclaration, its neighbour above.
// Both wire codes are non-retryable, so collapsing them would not hot-loop a
// client — it would send it to RE-CHUNK an oversized file, which fails
// identically, forever, rather than to shrink it.
//
// Nor is it a general resource-limit sentinel #1778 could share for its
// concurrency refusal, and that is why the name is specific to the byte bound.
// Retryability itself inverts across the two: internal/protocol's
// CodeAttachmentTooManyUploads is marked transient, because it clears when other
// uploads finish, while this one never clears for that file. Collapsing them
// would tell a client either to re-upload an oversized file in a loop or to
// abandon a bound that clears in seconds.
//
// It is not in accumulator.go's var block either, whose own opening sentence
// scopes it to Add and Assemble: this one is returned by Add AND by an admission
// function that runs before an Accumulator exists.
var ErrUploadTooLarge = errors.New("attachments: upload exceeds the per-upload byte bound")

// CheckDeclaration answers whether a first chunk's declared total_chunks and
// size can both be true. A nil error admits the transfer, and the same two
// numbers then go to NewAccumulator; every refusal wraps ErrInvalidDeclaration.
//
// It takes the two declared numbers as scalars rather than a
// protocol.AttachmentChunkPayload, mirroring NewAccumulator, and the narrow
// signature is load-bearing twice over. It says these two fields are the whole
// subject — a reader must not conclude the payload's other six are checked here
// — and it structurally excludes Filename, SHA256, AttachmentID and Data from
// ever reaching an error string, which is the strongest available form of the
// never-log rule protocol.AttachmentChunkPayload's SECURITY block states.
//
// PURE and stateless: no filesystem, no socket, no logger, no lock and no
// goroutine, so it is safe to call concurrently from any goroutine, exactly like
// SanitizeFilename and unlike Accumulator, which its own doc records as fed
// serially by one session's appFrameWorker.
//
// WHAT IS CHECKED IS THE TWO NUMBERS, NOT THE STRIDE. A client whose chunking
// differs from the published one but whose count happens to conform — 45001
// bytes cut at 32 KiB is 2 chunks either way — is admitted here and assembles
// normally, because Assemble compares the assembled length and digest against
// the declaration and nothing downstream needs the stride. A client whose
// chunking makes the count differ is refused, and that is not a regression: the
// published contract already obliges every chunk but the last to carry exactly
// protocol.MaxAttachmentChunkBytes raw bytes.
//
// WHAT IS NOT CHECKED IS MAGNITUDE. A declaration that is arithmetically
// conforming but enormous — size math.MaxInt64 with its matching count — is
// ADMITTED here, and the per-upload byte bound that refuses it is
// CheckDeclaredSize, a SIBLING in this file rather than a check folded in.
// Folding it in would refuse that pair, and TestCheckDeclaration's row on it is
// the only one that reads this ceiling's exact value rather than merely
// asserting it is not 1 — so folding would destroy the sole pin on the
// arithmetic whose wrapping form silently admits the largest declaration the
// wire can carry. Nothing this function admits is allocated from in the
// meantime: NewAccumulator's map takes no capacity hint and Assemble sizes from
// the sum of the arrived chunk lengths.
//
// Admission also runs IN FRONT OF the Accumulator rather than gating it. This
// returns an error rather than a validated type, so NewAccumulator stays
// constructible without passing through here, which is why Assemble's
// totalChunks < 1 clause is still load-bearing.
func CheckDeclaration(totalChunks int, size int64) error {
	// First, and it is not subsumed by the cross-check below: measured,
	// max(1, ceil(-1 / bound)) is 1 under either natural ceiling form, and so
	// is max(1, ceil(math.MinInt64 / bound)), so the pair size -1 /
	// total_chunks 1 passes the cross-check unaided. Running it first is also
	// what lets the arithmetic below reason about a non-negative operand.
	//
	// Which of the two refusals a doubly-bad declaration gets is therefore
	// observable only in the wrapped message, never in the sentinel, so this
	// order is deliberately not something a test can pin.
	if size < 0 {
		return fmt.Errorf("attachments: declared size %d is negative: %w", size, ErrInvalidDeclaration)
	}

	// The ceiling is division-then-remainder, and the (size + bound - 1) / bound
	// form is BANNED here rather than merely not preferred. Its numerator wraps
	// for every size within bound-1 of math.MaxInt64: at math.MaxInt64 it
	// evaluates to -204963823041216, which the clamp below launders back into 1,
	// so that form ADMITS the largest declaration the wire can carry paired with
	// a count of 1 — silently, and precisely at the value the never-allocate-
	// from-a-claim rule exists for. The correct answer there is
	// 204963823041218. Go's constant-overflow compile error is no defence,
	// since size arrives as a variable. This form cannot overflow: size / bound
	// is at most math.MaxInt64 / bound, and one increment from there cannot
	// reach math.MaxInt64.
	//
	// The bound is read from protocol.MaxAttachmentChunkBytes, never copied as a
	// local 45000, so there is only one place it can drift. The division is
	// raw-over-raw, as that constant's own doc requires: Size is raw file bytes
	// and the bound is raw chunk bytes, neither is base64.
	want := size / protocol.MaxAttachmentChunkBytes
	if size%protocol.MaxAttachmentChunkBytes != 0 {
		want++
	}

	// The published max(1, …), reachable only at size == 0. It is what resolves
	// the zero-byte file, whose bare ceiling is 0 against the documented
	// total_chunks >= 1, and it also subsumes the lower bound: want is now
	// always >= 1, so the equality already refuses a count of 0 and every
	// negative count. A separate lower-bound check would be dead code.
	want = max(want, 1)

	// totalChunks is WIDENED to int64 rather than want being narrowed to int.
	// int(want) would re-introduce, in the comparison itself, exactly the wrap
	// the ceiling above was computed to avoid — on a platform where int is 32
	// bits, a want beyond that range narrows to some other number and a hostile
	// count can be made to match it.
	if int64(totalChunks) != want {
		return fmt.Errorf("attachments: declared total_chunks %d, but size %d at %d raw bytes per chunk needs %d: %w",
			totalChunks, size, protocol.MaxAttachmentChunkBytes, want, ErrInvalidDeclaration)
	}
	return nil
}

// CheckDeclaredSize answers whether a first chunk's declared size is within this
// receiver's per-upload byte bound. A nil error admits the transfer; the one
// refusal wraps ErrUploadTooLarge with the size and the bound. It is the cheap
// rung of that bound — it refuses the honest oversize file on frame one, before
// any of its bytes are held — and Add's step 5 is the rung that makes the number
// a ceiling rather than a ceiling plus a per-chunk fudge factor.
//
// It checks MAGNITUDE AND NOTHING ELSE, which is why a NEGATIVE size is admitted
// here rather than refused. CheckDeclaration already refuses one with its own
// sentinel, and a second owner for that fault would make which sentinel a caller
// sees depend on the order it happened to run the two checks.
//
// PAIRING OBLIGATION: the caller runs BOTH checks on the first chunk, before
// constructing an Accumulator, and that is load-bearing for more than tidiness.
// CheckDeclaration bounds total_chunks from above, this bounds size from above,
// and only TOGETHER do they bound the accumulator's key space: with both run, an
// admitted transfer declares at most 373 chunks, so its map can hold at most 373
// entries. Run alone, this one admits a declaration of 2^31-1 ZERO-BYTE chunks,
// which no byte bound can refuse, because Σ len(Data) stays 0. Neither function
// can enforce the other's presence — admission runs IN FRONT OF the Accumulator
// rather than gating it, as CheckDeclaration's own doc records — so the
// obligation lands on the dispatch site (#1744).
//
// PURE and stateless: no filesystem, no socket, no logger, no lock and no
// goroutine, so it is safe to call concurrently from any goroutine, exactly like
// CheckDeclaration and SanitizeFilename.
func CheckDeclaredSize(size int64) error {
	// > and not >=: a transfer declaring exactly the bound is admitted, because
	// the bound is a ceiling on what may be held rather than on what may be
	// approached. There is no arithmetic here at all, only this comparison
	// against a constant, which is why a negative size cannot be laundered the
	// way a wrapping ceiling form launders one in CheckDeclaration.
	if size > maxUploadBytes {
		return fmt.Errorf("attachments: declared size %d exceeds the per-upload bound of %d bytes: %w",
			size, maxUploadBytes, ErrUploadTooLarge)
	}
	return nil
}

package attachments

import (
	"errors"
	"fmt"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

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
// One sentinel covers every refusal below. Mapping it to the
// attachment.invalid_chunk wire code, whose published row leads with "framing
// claims are inconsistent or out of range", is the dispatch site's job (#1744);
// this file emits no wire code and imports no codes.
var ErrInvalidDeclaration = errors.New("attachments: transfer declaration is not admissible")

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
// ADMITTED here, and the per-upload byte bound that refuses it is #1777's.
// Nothing this function admits is allocated from in the meantime:
// NewAccumulator's map takes no capacity hint and Assemble sizes from the sum of
// the arrived chunk lengths.
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

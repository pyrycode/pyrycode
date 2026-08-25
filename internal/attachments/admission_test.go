package attachments

import (
	"errors"
	"fmt"
	"math"
	"testing"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// TestCheckDeclaration drives both directions of the admission decision from one
// table: a declaration whose two numbers cannot both be true is refused with
// ErrInvalidDeclaration, and one matching the published
// total_chunks = max(1, ceil(size / protocol.MaxAttachmentChunkBytes)) form is
// admitted.
//
// Subtest names are built from the two numbers, which is the whole input: there
// is no client-supplied string in this function's signature, so the NUL-in-a-
// subtest-name hazard TestSanitizeFilename works around cannot arise here.
//
// The refusal rows never assert WHICH check refused them, only the sentinel,
// because one sentinel covers both refusals. The order the two checks run in is
// therefore not observable and no row here claims to pin it.
func TestCheckDeclaration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		size        int64
		totalChunks int
		admit       bool
	}{
		// A negative size is refused by a check of its own, and that check
		// cannot be folded into the cross-check: max(1, ceil(-1 / 45000)) is 1,
		// so the pair below passes the cross-check unaided. The second row is
		// the same at the extreme, and is what states the guard as size < 0
		// rather than size == -1: math.MinInt64's ceiling clamps to 1 too.
		{size: -1, totalChunks: 1, admit: false},
		{size: math.MinInt64, totalChunks: 1, admit: false},

		// A count the form does not produce: zero, negative, and one either
		// side of an honest count. No separate lower-bound check answers the
		// first two — the required count is always at least 1, so equality
		// already refuses every count below it.
		{size: 0, totalChunks: 0, admit: false},
		{size: protocol.MaxAttachmentChunkBytes, totalChunks: -1, admit: false},
		{size: protocol.MaxAttachmentChunkBytes + 1, totalChunks: 1, admit: false},
		{size: protocol.MaxAttachmentChunkBytes, totalChunks: 2, admit: false},

		// The client that chunks at its own 32 KiB buffer: 100000 bytes cut
		// that way declares 4 chunks where the published form says 3. Refused,
		// and its honest-count twin below is admitted, so the refusal is about
		// the count and not about the size.
		{size: 100000, totalChunks: 4, admit: false},

		// The largest declaration the wire can carry, paired with the count a
		// wrapping ceiling produces. (size + bound - 1) / bound wraps here to
		// -204963823041216, which max(1, …) launders back into 1 — so that form
		// ADMITS this pair. The division-then-remainder ceiling refuses it.
		{size: math.MaxInt64, totalChunks: 1, admit: false},

		// The published form, admitted. The zero-byte file is the case a bare
		// ceil gets wrong: ceil(0 / bound) is 0, and the max(1, …) is what makes
		// one empty chunk the right answer.
		{size: 0, totalChunks: 1, admit: true},
		{size: protocol.MaxAttachmentChunkBytes, totalChunks: 1, admit: true},
		{size: protocol.MaxAttachmentChunkBytes + 1, totalChunks: 2, admit: true},
		{size: 100000, totalChunks: 3, admit: true},

		// Arithmetically conforming but enormous is ADMITTED here: bounding the
		// magnitude of one upload is the per-upload byte bound's job (#1777),
		// and adding an absolute cap here would refuse this row. It is also the
		// only row that reads the ceiling's exact value rather than merely
		// asserting it is not 1, so it is what pins the arithmetic itself.
		//
		// The count needs a 64-bit int. If this ever has to build where int is
		// 32 bits, this row is the one that fails to compile — loudly, which is
		// the right failure for a receiver whose declared counts are int.
		{size: math.MaxInt64, totalChunks: 204963823041218, admit: true},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("size=%d,total_chunks=%d", tt.size, tt.totalChunks), func(t *testing.T) {
			t.Parallel()

			err := CheckDeclaration(tt.totalChunks, tt.size)
			if tt.admit {
				if err != nil {
					t.Fatalf("CheckDeclaration(%d, %d) = %v, want nil", tt.totalChunks, tt.size, err)
				}
				return
			}
			if !errors.Is(err, ErrInvalidDeclaration) {
				t.Fatalf("CheckDeclaration(%d, %d) = %v, want ErrInvalidDeclaration", tt.totalChunks, tt.size, err)
			}
		})
	}
}

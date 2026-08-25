package attachments

import (
	"bytes"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// testFixture is one attachment's bytes, split by testParts into three chunks
// whose LAST IS SHORTER than the other two (10 + 10 + 4). The uneven tail is
// deliberate: a reassembly keyed to a uniform stride, and one that appends in
// arrival order instead of addressing by index, both pass on a uniform fixture
// and fail on this one.
//
// The fixture is tens of bytes rather than anything near
// protocol.MaxAttachmentChunkBytes because this package enforces no byte cap —
// a bound-sized fixture would only be slow and would imply an enforcement that
// lives in #1767.
var (
	testFixture = []byte("0123456789abcdefghijklmn")
	testParts   = [][]byte{testFixture[0:10], testFixture[10:20], testFixture[20:24]}
)

// testTotal is the declared chunk count matching testParts.
const testTotal = 3

// The two declared digests the fixtures carry: testFixture's, and the empty
// input's. Both are WRITTEN OUT rather than computed by a test helper. A helper
// would reach for the same crypto/sha256 and encoding/hex calls Assemble does,
// so a mutant that changed the algorithm or the encoding would move the
// expectation along with the code and stay green. These two literals instead
// trace to a value verifiable outside this package:
//
//	printf '0123456789abcdefghijklmn' | shasum -a 256
//	printf '' | shasum -a 256
const (
	testFixtureDigest = "d5ea2aa9223ac1fa43ccec70b30962690fcfc6686857f99a0c4b2963cf8bee4b"
	testEmptyDigest   = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

// testChunk builds a chunk carrying only the three fields Add reads. Leaving
// AttachmentID, Filename, MimeType, Size and SHA256 at their zero values is
// itself the statement that Add reads none of them.
func testChunk(index, totalChunks int, data []byte) protocol.AttachmentChunkPayload {
	return protocol.AttachmentChunkPayload{Index: index, TotalChunks: totalChunks, Data: data}
}

// newTestAccumulator builds an accumulator for testFixture, declaring the size
// and the digest the fixture actually has, so a successful Assemble is a
// successful integrity check too.
func newTestAccumulator() *Accumulator {
	return NewAccumulator(testTotal, int64(len(testFixture)), testFixtureDigest)
}

func TestAccumulator_RoundTrip_AnyArrivalOrder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		order []int
	}{
		{"ascending", []int{0, 1, 2}},
		{"reverse", []int{2, 1, 0}},
		// A written-out permutation rather than a seeded shuffle, so a failure
		// reproduces from the source alone.
		{"shuffled", []int{1, 2, 0}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			a := newTestAccumulator()
			for _, i := range tt.order {
				if err := a.Add(testChunk(i, testTotal, testParts[i])); err != nil {
					t.Fatalf("Add(index %d) = %v, want nil", i, err)
				}
			}
			got, err := a.Assemble()
			if err != nil {
				t.Fatalf("Assemble() error = %v, want nil", err)
			}
			if !bytes.Equal(got, testFixture) {
				t.Errorf("Assemble() = %q, want %q", got, testFixture)
			}
		})
	}
}

// TestAccumulator_FramingFaults_RejectAndDiscard drives both halves of the
// second criterion from one table: each framing fault answers with its own
// sentinel, and the refused transfer is then discarded rather than continued.
// Every row carries EXACTLY ONE fault, so no row depends on Add's documented
// check order (TestAccumulator_TotalChunksMismatchOutranksIndexOutOfRange pins
// that separately).
func TestAccumulator_FramingFaults_RejectAndDiscard(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		prime []int                           // valid chunks fed before the fault
		fault protocol.AttachmentChunkPayload // the one faulty chunk
		rest  []int                           // valid chunks still unfed, so the transfer would otherwise complete
		want  error
	}{
		{
			name:  "duplicate index, different bytes",
			prime: []int{0, 1},
			fault: testChunk(0, testTotal, []byte("DIFFERENT!")),
			rest:  []int{2},
			want:  ErrDuplicateIndex,
		},
		{
			// There is no idempotent-retry carve-out: completion is every index
			// arriving exactly once, so repeating the bytes already held is
			// still a duplicate.
			name:  "duplicate index, identical bytes",
			prime: []int{0, 1},
			fault: testChunk(0, testTotal, testParts[0]),
			rest:  []int{2},
			want:  ErrDuplicateIndex,
		},
		{
			name:  "index negative",
			prime: []int{0},
			fault: testChunk(-1, testTotal, testParts[1]),
			rest:  []int{1, 2},
			want:  ErrIndexOutOfRange,
		},
		{
			name:  "index equal to total_chunks",
			prime: []int{0},
			fault: testChunk(testTotal, testTotal, testParts[1]),
			rest:  []int{1, 2},
			want:  ErrIndexOutOfRange,
		},
		{
			// Index 1 is admissible under both the declared count and the
			// chunk's own, so the count disagreement is the row's only fault.
			name:  "total_chunks greater than declared",
			prime: []int{0},
			fault: testChunk(1, testTotal+1, testParts[1]),
			rest:  []int{1, 2},
			want:  ErrTotalChunksMismatch,
		},
		{
			name:  "total_chunks less than declared",
			prime: []int{0},
			fault: testChunk(1, testTotal-1, testParts[1]),
			rest:  []int{1, 2},
			want:  ErrTotalChunksMismatch,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			a := newTestAccumulator()
			for _, i := range tt.prime {
				if err := a.Add(testChunk(i, testTotal, testParts[i])); err != nil {
					t.Fatalf("priming Add(index %d) = %v, want nil", i, err)
				}
			}
			err := a.Add(tt.fault)
			if !errors.Is(err, tt.want) {
				t.Fatalf("Add(faulty chunk) = %v, want %v", err, tt.want)
			}

			// Discarded rather than continued: every later chunk answers with
			// the identical latched error, even a perfectly valid one.
			for _, i := range tt.rest {
				later := a.Add(testChunk(i, testTotal, testParts[i]))
				if later != err {
					t.Errorf("Add(valid index %d) after refusal = %v, want the latched %v", i, later, err)
				}
			}

			// Every index has now been offered, so a latch checked only in Add
			// would leave this Assemble looking complete.
			got, aerr := a.Assemble()
			if aerr != err {
				t.Errorf("Assemble() after refusal error = %v, want the latched %v", aerr, err)
			}
			if got != nil {
				t.Errorf("Assemble() after refusal = %q, want no bytes", got)
			}
		})
	}
}

// TestAccumulator_IntegrityFaults_RejectAndDiscard drives every integrity
// refusal from one table, in the shape TestAccumulator_FramingFaults_RejectAndDiscard
// established: each row's stream is FRAMED PERFECTLY and completes, so the
// contradiction with one of the transfer's two declarations is the row's only
// fault, and the shared body then asserts the sentinel, that no bytes came
// back, that the held bytes were released, and that the refusal latched.
func TestAccumulator_IntegrityFaults_RejectAndDiscard(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		size   int64  // declared byte length of the whole file
		sha256 string // declared lowercase-hex digest of the whole file
		parts  [][]byte
		want   error
	}{
		{
			// A short last chunk. Nothing can be the wrong length AND match
			// the declared digest, so this row is also what catches the two
			// checks being swapped: under a swap it answers ErrDigestMismatch.
			name:   "assembled length short of the declared size",
			size:   int64(len(testFixture)),
			sha256: testFixtureDigest,
			parts:  [][]byte{testParts[0], testParts[1], testFixture[20:23]},
			want:   ErrSizeMismatch,
		},
		{
			// The declared size is an attacker-chosen integer, and this is the
			// one check that reads it. An absurd claim must lose a comparison,
			// never size an allocation.
			name:   "declared size absurd",
			size:   math.MaxInt64,
			sha256: testFixtureDigest,
			parts:  testParts,
			want:   ErrSizeMismatch,
		},
		{
			// The flipped byte is in the LAST chunk, so a digest taken over
			// chunks[0] — or over anything short of the whole assembly — still
			// fails this row.
			name:   "declared length, different bytes",
			size:   int64(len(testFixture)),
			sha256: testFixtureDigest,
			parts:  [][]byte{testParts[0], testParts[1], []byte("klmo")},
			want:   ErrDigestMismatch,
		},
		{
			// The CORRECT digest, uppercased: the comparison is exact, never
			// the strings.EqualFold that update.VerifySHA256 uses.
			name:   "correct digest in uppercase hex",
			size:   int64(len(testFixture)),
			sha256: strings.ToUpper(testFixtureDigest),
			parts:  testParts,
			want:   ErrDigestMismatch,
		},
		{
			name:   "truncated claim",
			size:   int64(len(testFixture)),
			sha256: testFixtureDigest[:32],
			parts:  testParts,
			want:   ErrDigestMismatch,
		},
		{
			// Right length, wrong alphabet. A shape check standing in for the
			// equality comparison would pass a well-formed claim; only this
			// row and its neighbours say the claim is compared, not inspected.
			name:   "claim of the declared length that is not hex",
			size:   int64(len(testFixture)),
			sha256: strings.Repeat("z", 64),
			parts:  testParts,
			want:   ErrDigestMismatch,
		},
		{
			// The value #1769's fixtures carried. An empty claim is the one a
			// well-formedness carve-out is most tempting for, and a carve-out
			// on it is an integrity opt-out the sender chooses.
			name:   "empty claim",
			size:   int64(len(testFixture)),
			sha256: "",
			parts:  testParts,
			want:   ErrDigestMismatch,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			a := NewAccumulator(testTotal, tt.size, tt.sha256)
			for i, data := range tt.parts {
				if err := a.Add(testChunk(i, testTotal, data)); err != nil {
					t.Fatalf("Add(index %d) = %v, want nil", i, err)
				}
			}
			got, err := a.Assemble()
			if !errors.Is(err, tt.want) {
				t.Fatalf("Assemble() error = %v, want %v", err, tt.want)
			}
			if got != nil {
				t.Errorf("Assemble() = %q, want no bytes", got)
			}
			if a.chunks != nil {
				t.Errorf("chunks after the refusal holds %d entries, want the map dropped", len(a.chunks))
			}

			// The transfer is complete, so the only chunk a client can still
			// send is a duplicate: without the latch this answers
			// ErrDuplicateIndex, a framing sentinel #1744 maps to a different
			// wire code, and one corrupt transfer emits two.
			if later := a.Add(testChunk(0, testTotal, tt.parts[0])); later != err {
				t.Errorf("Add(already-arrived index 0) after the refusal = %v, want the latched %v", later, err)
			}
			got, aerr := a.Assemble()
			if aerr != err {
				t.Errorf("Assemble() after the refusal error = %v, want the latched %v", aerr, err)
			}
			if got != nil {
				t.Errorf("Assemble() after the refusal = %q, want no bytes", got)
			}
		})
	}
}

// TestAccumulator_SingleChunk_ReturnsAFreshCopy assembles a one-chunk transfer
// carrying non-empty bytes, mutates what it got back, and assembles again. A
// fast path yielding the stored chunks[0] passes the first assertion and fails
// the second, which is what would make the two integrity checks a check-then-use
// over aliased memory: the bytes verified would stop being the bytes the caller
// holds.
func TestAccumulator_SingleChunk_ReturnsAFreshCopy(t *testing.T) {
	t.Parallel()

	// The chunk carries its OWN copy of the fixture: a fast-path regression
	// would otherwise scribble on testFixture itself and redden every other
	// test in this file instead of this one.
	data := append([]byte(nil), testFixture...)
	a := NewAccumulator(1, int64(len(testFixture)), testFixtureDigest)
	if err := a.Add(testChunk(0, 1, data)); err != nil {
		t.Fatalf("Add(the only chunk) = %v, want nil", err)
	}
	got, err := a.Assemble()
	if err != nil {
		t.Fatalf("Assemble() error = %v, want nil", err)
	}
	if !bytes.Equal(got, testFixture) {
		t.Fatalf("Assemble() = %q, want %q", got, testFixture)
	}
	for i := range got {
		got[i] = 'X'
	}
	again, err := a.Assemble()
	if err != nil {
		t.Fatalf("Assemble() after mutating the first result error = %v, want nil", err)
	}
	if !bytes.Equal(again, testFixture) {
		t.Errorf("Assemble() after mutating the first result = %q, want %q", again, testFixture)
	}
}

// TestAccumulator_ZeroDeclaredCount_IsIncomplete pins Assemble's
// declared-count-below-one guard: without it the exactly-once test is vacuously
// true for a declared count of zero and such a transfer assembles to empty
// bytes, a success from a declaration the published contract forbids. The
// declared size is 0 and the declared digest is the empty input's, so neither
// integrity check can redden this for another reason — it fails precisely when
// the guard is gone.
func TestAccumulator_ZeroDeclaredCount_IsIncomplete(t *testing.T) {
	t.Parallel()

	a := NewAccumulator(0, 0, testEmptyDigest)
	got, err := a.Assemble()
	if !errors.Is(err, ErrIncomplete) {
		t.Fatalf("Assemble() on a declared count of zero error = %v, want %v", err, ErrIncomplete)
	}
	if got != nil {
		t.Errorf("Assemble() on a declared count of zero = %q, want no bytes", got)
	}
}

// TestAccumulator_TotalChunksMismatchOutranksIndexOutOfRange pins Add's
// documented check order for a frame carrying two faults at once: a disagreeing
// count invalidates the frame of reference the range check would use, so the
// count is checked first.
func TestAccumulator_TotalChunksMismatchOutranksIndexOutOfRange(t *testing.T) {
	t.Parallel()

	a := newTestAccumulator()
	err := a.Add(testChunk(-1, testTotal+1, testParts[0]))
	if !errors.Is(err, ErrTotalChunksMismatch) {
		t.Fatalf("Add(two faults) = %v, want %v", err, ErrTotalChunksMismatch)
	}
	if errors.Is(err, ErrIndexOutOfRange) {
		t.Errorf("Add(two faults) = %v, want it not to also report %v", err, ErrIndexOutOfRange)
	}
}

// TestAccumulator_DuplicateZeroByteChunk pins the duplicate check on the map
// KEY rather than on the stored bytes. A check written as chunks[i] != nil
// passes every row of TestAccumulator_FramingFaults_RejectAndDiscard and
// silently accepts this re-send, because a zero-byte chunk stores a nil value
// under a key that exists.
func TestAccumulator_DuplicateZeroByteChunk(t *testing.T) {
	t.Parallel()

	// The empty input's digest, even though this transfer never assembles
	// successfully: a "" claim is precisely the value Assemble must refuse, so
	// leaving one in a fixture would leave a dead transfer behind for the next
	// test that copies it.
	a := NewAccumulator(1, 0, testEmptyDigest)
	if err := a.Add(testChunk(0, 1, nil)); err != nil {
		t.Fatalf("Add(index 0) = %v, want nil", err)
	}
	err := a.Add(testChunk(0, 1, nil))
	if !errors.Is(err, ErrDuplicateIndex) {
		t.Fatalf("Add(re-sent index 0) = %v, want %v", err, ErrDuplicateIndex)
	}
}

// TestAccumulator_Incomplete_IsResumable pins the difference from
// relay.ReassembleBundle, whose "incomplete" is terminal: here a later chunk
// turns the same accumulator into a complete one.
func TestAccumulator_Incomplete_IsResumable(t *testing.T) {
	t.Parallel()

	a := newTestAccumulator()
	for _, i := range []int{0, 2} {
		if err := a.Add(testChunk(i, testTotal, testParts[i])); err != nil {
			t.Fatalf("Add(index %d) = %v, want nil", i, err)
		}
	}
	got, err := a.Assemble()
	if !errors.Is(err, ErrIncomplete) {
		t.Fatalf("Assemble() with a gap error = %v, want %v", err, ErrIncomplete)
	}
	if got != nil {
		t.Errorf("Assemble() with a gap = %q, want no bytes", got)
	}

	if err := a.Add(testChunk(1, testTotal, testParts[1])); err != nil {
		t.Fatalf("Add(index 1) filling the last gap = %v, want nil", err)
	}

	// Twice, on the same accumulator: ErrIncomplete neither latched like a
	// refusal nor consumed the state, and Assemble does not consume it either.
	for attempt := 1; attempt <= 2; attempt++ {
		got, err := a.Assemble()
		if err != nil {
			t.Fatalf("Assemble() attempt %d error = %v, want nil", attempt, err)
		}
		if !bytes.Equal(got, testFixture) {
			t.Errorf("Assemble() attempt %d = %q, want %q", attempt, got, testFixture)
		}
	}
}

// TestAccumulator_ZeroByteAttachment covers the sender arithmetic's
// total_chunks = max(1, ceil(size / 45000)): an empty file is one chunk
// carrying zero bytes, not zero chunks. Both spellings of "no bytes" reach the
// accumulator in practice — nil from a payload built directly, an empty
// non-nil slice from encoding/json decoding "data": "".
//
// Declaring size 0 and the empty input's digest makes this the integrity
// checks' zero-byte row as well: a guard that rejected an empty assembly would
// redden it. It pins nothing about WHAT the digest is computed over, because
// the empty digest is the same whether it is taken over the assembled slice or
// over any subset of it; TestAccumulator_IntegrityFaults_RejectAndDiscard's
// non-empty rows are what pin that.
func TestAccumulator_ZeroByteAttachment(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		data []byte
	}{
		{"nil data", nil},
		{"empty non-nil data", []byte{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			a := NewAccumulator(1, 0, testEmptyDigest)
			if err := a.Add(testChunk(0, 1, tt.data)); err != nil {
				t.Fatalf("Add(the only chunk) = %v, want nil", err)
			}
			got, err := a.Assemble()
			if err != nil {
				t.Fatalf("Assemble() error = %v, want nil", err)
			}
			if len(got) != 0 {
				t.Errorf("Assemble() = %q, want zero bytes", got)
			}
		})
	}
}

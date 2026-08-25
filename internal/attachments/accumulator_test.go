package attachments

import (
	"bytes"
	"errors"
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

// testChunk builds a chunk carrying only the three fields Add reads. Leaving
// AttachmentID, Filename, MimeType, Size and SHA256 at their zero values is
// itself the statement that Add reads none of them.
func testChunk(index, totalChunks int, data []byte) protocol.AttachmentChunkPayload {
	return protocol.AttachmentChunkPayload{Index: index, TotalChunks: totalChunks, Data: data}
}

// newTestAccumulator builds an accumulator for testFixture.
func newTestAccumulator() *Accumulator {
	return NewAccumulator(testTotal, int64(len(testFixture)), "")
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

	a := NewAccumulator(1, 0, "")
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

			a := NewAccumulator(1, 0, "")
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

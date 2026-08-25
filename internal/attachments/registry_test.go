package attachments

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// The two conns and the one attachment_id every test here keys on. The same id
// on two conns is the whole point of uploadKey, so it is shared deliberately.
const (
	testConnA        = "conn-a"
	testConnB        = "conn-b"
	testAttachmentID = "att-1"
)

// testBoundTotal is the count CheckDeclaration requires for a declared size of
// maxUploadBytes at protocol.MaxAttachmentChunkBytes, which makes
// (testBoundTotal, maxUploadBytes) the one admissible multi-chunk declaration
// this package already has both bytes and a written-out digest for.
const testBoundTotal = 373

// boundChunk returns chunk i of testBoundFixture. It SLICES the fixture rather
// than copying it, so like every other reader of that package-level buffer it
// must not mutate what it hands back.
func boundChunk(i int) protocol.AttachmentChunkPayload {
	start := i * protocol.MaxAttachmentChunkBytes
	end := min(start+protocol.MaxAttachmentChunkBytes, len(testBoundFixture))
	return testChunk(i, testBoundTotal, testBoundFixture[start:end])
}

// The concurrency tests below coordinate with channels rather than sync
// primitives: after this slice sync is imported by registry.go and by no other
// file in this package, which is what makes that an absence check with a live
// control. A start channel closed once releases the fan-out, and a result
// channel BUFFERED TO N is the join — unbuffered, an assertion failing
// mid-drain would park every goroutine not yet drained for the life of the test
// binary.

func TestRegistry_InsertThenLookup_ReturnsTheSameAccumulator(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	acc := newTestAccumulator()

	upload, inserted := r.insert(testConnA, testAttachmentID, acc)
	if !inserted {
		t.Fatalf("insert into an empty registry reported a repeat, want fresh")
	}
	if upload != acc {
		t.Errorf("insert returned a different accumulator than it was handed")
	}

	got, ok := r.Lookup(testConnA, testAttachmentID)
	if !ok {
		t.Fatalf("Lookup(%q, %q) reported absent, want present", testConnA, testAttachmentID)
	}
	// Pointer identity, not field equality: a fresh accumulator built from the
	// same declaration compares equal field by field and holds none of the
	// chunks the transfer in flight has accumulated.
	if got != acc {
		t.Errorf("Lookup returned a different accumulator than the one inserted")
	}
	if n := r.count(); n != 1 {
		t.Errorf("count() = %d, want 1", n)
	}
}

func TestRegistry_SameAttachmentIDOnTwoConns_AreSeparateEntries(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	accA, accB := newTestAccumulator(), newTestAccumulator()

	if _, inserted := r.insert(testConnA, testAttachmentID, accA); !inserted {
		t.Fatalf("insert on %q reported a repeat, want fresh", testConnA)
	}
	if _, inserted := r.insert(testConnB, testAttachmentID, accB); !inserted {
		t.Fatalf("insert of the same attachment_id on %q reported a repeat, want fresh", testConnB)
	}
	if n := r.count(); n != 2 {
		t.Fatalf("count() = %d, want 2", n)
	}

	gotA, okA := r.Lookup(testConnA, testAttachmentID)
	gotB, okB := r.Lookup(testConnB, testAttachmentID)
	if !okA || !okB {
		t.Fatalf("Lookup answered (%v, %v), want both present", okA, okB)
	}
	if gotA != accA || gotB != accB {
		t.Errorf("each conn's lookup must answer its own accumulator")
	}

	// The sharpest available statement that neither conn can feed the other's
	// transfer: were the two entries one pointer, the second Add of index 0
	// would answer ErrDuplicateIndex.
	if err := accA.Add(testChunk(0, testTotal, testParts[0])); err != nil {
		t.Fatalf("feeding %q's transfer chunk 0: %v", testConnA, err)
	}
	if err := accB.Add(testChunk(0, testTotal, testParts[0])); err != nil {
		t.Errorf("feeding %q's transfer chunk 0 after %q held the same index: %v", testConnB, testConnA, err)
	}
}

func TestRegistry_SecondInsertUnderAHeldPair_KeepsTheIncumbent(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	incumbent := newTestAccumulator()
	if _, inserted := r.insert(testConnA, testAttachmentID, incumbent); !inserted {
		t.Fatalf("insert into an empty registry reported a repeat, want fresh")
	}
	if err := incumbent.Add(testChunk(0, testTotal, testParts[0])); err != nil {
		t.Fatalf("feeding the transfer in flight chunk 0: %v", err)
	}

	// A second first-chunk for the live pair, carrying its own accumulator.
	upload, inserted := r.insert(testConnA, testAttachmentID, newTestAccumulator())
	if inserted {
		t.Errorf("insert under a held pair reported a fresh insert, want a repeat")
	}
	if upload != incumbent {
		t.Errorf("insert under a held pair returned something other than the incumbent")
	}
	if n := r.count(); n != 1 {
		t.Errorf("count() = %d, want 1", n)
	}
	if got, ok := r.Lookup(testConnA, testAttachmentID); !ok || got != incumbent {
		t.Errorf("Lookup after a repeat insert answered (%v, %v), want the incumbent", got, ok)
	}

	// The chunk the transfer already held survived the repeat.
	for i := 1; i < testTotal; i++ {
		if err := incumbent.Add(testChunk(i, testTotal, testParts[i])); err != nil {
			t.Fatalf("feeding the transfer in flight chunk %d: %v", i, err)
		}
	}
	assembled, err := incumbent.Assemble()
	if err != nil {
		t.Fatalf("Assemble() after a repeat insert: %v", err)
	}
	if !bytes.Equal(assembled, testFixture) {
		t.Errorf("Assemble() = %q, want %q", assembled, testFixture)
	}
}

func TestRegistry_Release_RemovesExactlyItsEntry(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	accA, accB := newTestAccumulator(), newTestAccumulator()
	r.insert(testConnA, testAttachmentID, accA)
	r.insert(testConnB, testAttachmentID, accB)

	r.Release(testConnA, testAttachmentID)

	if _, ok := r.Lookup(testConnA, testAttachmentID); ok {
		t.Errorf("Lookup after Release reported the released upload present")
	}
	if n := r.count(); n != 1 {
		t.Errorf("count() after Release = %d, want 1", n)
	}
	if got, ok := r.Lookup(testConnB, testAttachmentID); !ok || got != accB {
		t.Errorf("releasing %q's upload disturbed %q's entry under the same attachment_id", testConnA, testConnB)
	}

	// Releasing a pair the registry does not hold is a no-op.
	r.Release(testConnA, testAttachmentID)
	if n := r.count(); n != 1 {
		t.Errorf("count() after releasing an absent pair = %d, want 1", n)
	}
}

func TestRegistry_ConcurrentMixedOperations_AreSafe(t *testing.T) {
	t.Parallel()

	const (
		workers = 32
		readers = 4
	)
	r := NewRegistry()
	start := make(chan struct{})
	done := make(chan struct{}, workers+readers+1)

	// Each worker drives its own distinct pair from insert to release.
	for i := 0; i < workers; i++ {
		attachmentID := fmt.Sprintf("att-%d", i)
		go func() {
			<-start
			if _, inserted := r.insert(testConnA, attachmentID, newTestAccumulator()); !inserted {
				t.Errorf("insert of the distinct pair %q reported a repeat", attachmentID)
			}
			if _, ok := r.Lookup(testConnA, attachmentID); !ok {
				t.Errorf("Lookup of the pair %q this goroutine just inserted reported absent", attachmentID)
			}
			r.Release(testConnA, attachmentID)
			done <- struct{}{}
		}()
	}

	// One shared pair, inserted and released alongside readers hitting it. The
	// readers assert safety, never ordering: either answer is correct.
	go func() {
		<-start
		r.insert(testConnB, testAttachmentID, newTestAccumulator())
		r.Release(testConnB, testAttachmentID)
		done <- struct{}{}
	}()
	for i := 0; i < readers; i++ {
		go func() {
			<-start
			for j := 0; j < 64; j++ {
				r.Lookup(testConnB, testAttachmentID)
				r.count()
			}
			done <- struct{}{}
		}()
	}

	close(start)
	for i := 0; i < workers+readers+1; i++ {
		<-done
	}

	if n := r.count(); n != 0 {
		t.Errorf("count() after every upload was released = %d, want 0", n)
	}
}

// TestRegistry_ConcurrentSamePairInsert_TellsExactlyOneItIsFresh is AC 3's pin
// and the only test here that has one. It is the sole red for a registry that
// looks the pair up under one acquisition of mu and stores under another: that
// shape is race-free, satisfies every other test in this file, and still tells
// two goroutines they both made a fresh insert. Its redness is probabilistic
// rather than certain, so measure it with -count=5 and read the fresh-insert
// count as the failing assertion.
func TestRegistry_ConcurrentSamePairInsert_TellsExactlyOneItIsFresh(t *testing.T) {
	t.Parallel()

	const goroutines = 32
	type outcome struct {
		own      *Accumulator
		got      *Accumulator
		inserted bool
	}
	r := NewRegistry()
	start := make(chan struct{})
	outcomes := make(chan outcome, goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			own := newTestAccumulator()
			<-start
			got, inserted := r.insert(testConnA, testAttachmentID, own)
			outcomes <- outcome{own: own, got: got, inserted: inserted}
		}()
	}
	close(start)

	all := make([]outcome, 0, goroutines)
	fresh := 0
	var winner *Accumulator
	for i := 0; i < goroutines; i++ {
		got := <-outcomes
		all = append(all, got)
		if got.inserted {
			fresh++
			winner = got.own
			if got.got != got.own {
				t.Errorf("the goroutine told its insert was fresh got back an accumulator it did not hand in")
			}
		}
	}

	if fresh != 1 {
		t.Fatalf("goroutines told their insert was fresh = %d, want exactly 1", fresh)
	}
	for _, got := range all {
		if got.got != winner {
			t.Errorf("a goroutine was handed an accumulator other than the one insert kept")
		}
	}
	if n := r.count(); n != 1 {
		t.Errorf("count() = %d, want 1", n)
	}
}

// TestRegistry_AdmitRefusedDeclaration_StoresNothing carries ONE FAULT PER ROW
// rather than one doubly-bad declaration, which is what makes each check's
// removal a sole red: a declaration both checks refuse reddens under either
// removal and therefore proves neither. It would also pin a cross-check order
// that nothing has decided, since the two answer different sentinels.
func TestRegistry_AdmitRefusedDeclaration_StoresNothing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		totalChunks int
		size        int64
		want        error
	}{
		{
			// Its own arithmetically-correct count, so CheckDeclaration admits
			// this one and only the byte bound refuses it.
			name:        "size one byte above the per-upload bound",
			totalChunks: testBoundTotal,
			size:        maxUploadBytes + 1,
			want:        ErrUploadTooLarge,
		},
		{
			// Well within the byte bound, so CheckDeclaredSize admits this one
			// and only the arithmetic refuses it: 100000 bytes is 3 chunks.
			name:        "count disagreeing with the declared size",
			totalChunks: 4,
			size:        100000,
			want:        ErrInvalidDeclaration,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := NewRegistry()
			upload, err := r.Admit(testConnA, testAttachmentID, tt.totalChunks, tt.size, testFixtureDigest)
			if !errors.Is(err, tt.want) {
				t.Fatalf("Admit of a declaration with %s: error = %v, want one wrapping %v", tt.name, err, tt.want)
			}
			if upload != nil {
				t.Errorf("Admit returned a non-nil accumulator for a refused declaration")
			}
			if n := r.count(); n != 0 {
				t.Errorf("count() after a refusal = %d, want 0", n)
			}
			if _, ok := r.Lookup(testConnA, testAttachmentID); ok {
				t.Errorf("Lookup after a refusal reported the pair present")
			}

			// The two client-supplied strings this entry point necessarily
			// holds must not reach the message, and the declared size must:
			// that last one is the control, since an entry point swallowing the
			// wrapped message would satisfy both prohibitions vacuously.
			msg := err.Error()
			if strings.Contains(msg, testAttachmentID) {
				t.Errorf("refusal message %q carries the attachment_id", msg)
			}
			if strings.Contains(msg, testFixtureDigest) {
				t.Errorf("refusal message %q carries the declared digest", msg)
			}
			if size := strconv.FormatInt(tt.size, 10); !strings.Contains(msg, size) {
				t.Errorf("refusal message %q does not carry the declared size %s", msg, size)
			}
		})
	}
}

func TestRegistry_AdmitAfterARefusal_AdmitsTheSamePair(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	if _, err := r.Admit(testConnA, testAttachmentID, testBoundTotal, maxUploadBytes+1, testFixtureDigest); err == nil {
		t.Fatalf("Admit of an oversized declaration returned a nil error, want a refusal")
	}

	// A refusal that stored nothing can blacklist nothing.
	upload, err := r.Admit(testConnA, testAttachmentID, testBoundTotal, maxUploadBytes, testBoundFixtureDigest)
	if err != nil {
		t.Fatalf("Admit of an admissible declaration under a previously refused pair: %v", err)
	}
	if upload == nil {
		t.Fatalf("Admit answered a nil accumulator with a nil error")
	}
	if n := r.count(); n != 1 {
		t.Errorf("count() = %d, want 1", n)
	}
	if got, ok := r.Lookup(testConnA, testAttachmentID); !ok || got != upload {
		t.Errorf("Lookup after the re-declaration answered (%p, %v), want the admitted accumulator", got, ok)
	}
}

func TestRegistry_Admit_HoldsAnAccumulatorLatchedWithTheDeclaration(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	upload, err := r.Admit(testConnA, testAttachmentID, testBoundTotal, maxUploadBytes, testBoundFixtureDigest)
	if err != nil {
		t.Fatalf("Admit of an admissible declaration: %v", err)
	}
	// Pointer identity, not field equality: an accumulator built from the same
	// declaration compares equal field by field and holds no chunks.
	if got, ok := r.Lookup(testConnA, testAttachmentID); !ok || got != upload {
		t.Fatalf("Lookup answered (%p, %v), want the accumulator Admit returned", got, ok)
	}
	if n := r.count(); n != 1 {
		t.Errorf("count() = %d, want 1", n)
	}

	// The end-to-end statement that the declared numbers were latched rather
	// than zeros: a zero-latched accumulator refuses the first chunk, and one
	// that dropped the size or the digest gets as far as Assemble.
	for i := 0; i < testBoundTotal; i++ {
		if err := upload.Add(boundChunk(i)); err != nil {
			t.Fatalf("feeding the admitted transfer chunk %d: %v", i, err)
		}
	}
	assembled, err := upload.Assemble()
	if err != nil {
		t.Fatalf("Assemble() of the admitted transfer: %v", err)
	}
	if !bytes.Equal(assembled, testBoundFixture) {
		t.Errorf("Assemble() returned %d bytes that differ from the fixture, want its %d bytes", len(assembled), len(testBoundFixture))
	}
}

// TestRegistry_AdmitUnderAHeldPair_KeepsTheIncumbent re-pins through the
// exported path what TestRegistry_SecondInsertUnderAHeldPair_KeepsTheIncumbent
// pins on insert directly: that test stays green against an entry point that
// releases the pair before inserting, or that installs a freshly built
// accumulator by some other route. It asserts NOTHING about the second call's
// error, because whether this entry point answers the incumbent or refuses the
// repeat is not pinned, and the incumbent must survive either way.
func TestRegistry_AdmitUnderAHeldPair_KeepsTheIncumbent(t *testing.T) {
	t.Parallel()

	const held = 2

	r := NewRegistry()
	incumbent, err := r.Admit(testConnA, testAttachmentID, testBoundTotal, maxUploadBytes, testBoundFixtureDigest)
	if err != nil {
		t.Fatalf("Admit of an admissible declaration: %v", err)
	}
	for i := 0; i < held; i++ {
		if err := incumbent.Add(boundChunk(i)); err != nil {
			t.Fatalf("feeding the transfer in flight chunk %d: %v", i, err)
		}
	}

	// A second first-chunk for the live pair, carrying the same declaration.
	repeat, _ := r.Admit(testConnA, testAttachmentID, testBoundTotal, maxUploadBytes, testBoundFixtureDigest)
	if repeat != nil && repeat != incumbent {
		t.Errorf("Admit under a held pair answered an accumulator other than the incumbent")
	}
	if got, ok := r.Lookup(testConnA, testAttachmentID); !ok || got != incumbent {
		t.Errorf("Lookup after a repeat admission answered (%p, %v), want the incumbent", got, ok)
	}
	if n := r.count(); n != 1 {
		t.Errorf("count() = %d, want 1", n)
	}

	// The chunks the transfer already held survived the repeat.
	for i := held; i < testBoundTotal; i++ {
		if err := incumbent.Add(boundChunk(i)); err != nil {
			t.Fatalf("feeding the transfer in flight chunk %d: %v", i, err)
		}
	}
	assembled, err := incumbent.Assemble()
	if err != nil {
		t.Fatalf("Assemble() after a repeat admission: %v", err)
	}
	if !bytes.Equal(assembled, testBoundFixture) {
		t.Errorf("Assemble() returned %d bytes that differ from the fixture, want its %d bytes", len(assembled), len(testBoundFixture))
	}
}

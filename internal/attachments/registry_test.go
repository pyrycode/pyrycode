package attachments

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

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

// withAttachmentID returns the chunk with its AttachmentID set, leaving the
// caller's copy untouched — the parameter is a value, so the shared fixtures
// testChunk and boundChunk build are not disturbed by it. It is the ONE place
// an attachment_id enters a chunk in this file, which is what lets testChunk go
// on leaving that field zero: that absence is testChunk's own statement that Add
// reads none of those fields, and Deliver is the only surface here that reads
// this one.
func withAttachmentID(chunk protocol.AttachmentChunkPayload, attachmentID string) protocol.AttachmentChunkPayload {
	chunk.AttachmentID = attachmentID
	return chunk
}

// packageSentinels names every sentinel this package exports — accumulator.go's
// var block, admission.go's three and storage.go's three. AC 5 asks that the
// unknown-pair refusal be distinct from every existing sentinel, which is a
// claim over a SET and is asserted over that set rather than over a sample of
// it: three hand-picked near misses would leave the quantifier unproved while
// looking like coverage. A sentinel added later and not listed here weakens the
// claim silently, which is the one maintenance cost this shape carries.
var packageSentinels = []error{
	ErrTotalChunksMismatch,
	ErrIndexOutOfRange,
	ErrDuplicateIndex,
	ErrSizeMismatch,
	ErrDigestMismatch,
	ErrIncomplete,
	ErrInvalidDeclaration,
	ErrUploadTooLarge,
	ErrTooManyUploads,
	ErrInvalidID,
	ErrNotContained,
	ErrWriteFailed,
}

// The concurrency tests below coordinate with channels rather than sync
// primitives: sync is imported by registry.go and, in this file, by fakeClock
// alone, so no test's own coordination reaches for a mutex and Accumulator's
// mutex-free contract still reads as an absence with a live control beside it.
// A start channel closed once releases the fan-out, and a result
// channel BUFFERED TO N is the join — unbuffered, an assertion failing
// mid-drain would park every goroutine not yet drained for the life of the test
// binary.
//
// The r.insert calls below discard insert's error with _, and the reason is
// stated once here rather than at each of the seven sites: except in
// TestRegistry_ConcurrentMixedOperations_AreSafe, whose own comments cover it,
// every one of those registries provably holds fewer than maxInFlightUploads
// entries at the call, so the capacity refusal is unreachable by construction.
// The tests that mean to observe that refusal go through Admit.

func TestRegistry_InsertThenLookup_ReturnsTheSameAccumulator(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	acc := newTestAccumulator()

	upload, inserted, _ := r.insert(testConnA, testAttachmentID, acc)
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

	if _, inserted, _ := r.insert(testConnA, testAttachmentID, accA); !inserted {
		t.Fatalf("insert on %q reported a repeat, want fresh", testConnA)
	}
	if _, inserted, _ := r.insert(testConnB, testAttachmentID, accB); !inserted {
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
	if _, inserted, _ := r.insert(testConnA, testAttachmentID, incumbent); !inserted {
		t.Fatalf("insert into an empty registry reported a repeat, want fresh")
	}
	if err := incumbent.Add(testChunk(0, testTotal, testParts[0])); err != nil {
		t.Fatalf("feeding the transfer in flight chunk 0: %v", err)
	}

	// A second first-chunk for the live pair, carrying its own accumulator.
	upload, inserted, _ := r.insert(testConnA, testAttachmentID, newTestAccumulator())
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
	done := make(chan struct{}, workers+readers+2)

	// Each worker drives its own distinct pair from insert to release. workers
	// exceeds maxInFlightUploads by design, so a distinct pair has TWO legal
	// answers here — inserted, or refused by the capacity gate — and which one
	// it gets depends on how the fan-out interleaves. Both are safety; the
	// subject of this test is that mixed concurrent operations are safe, and the
	// cap is now part of that subject rather than an obstacle to it. What stays
	// illegal is the third answer: a DISTINCT pair reported as a repeat.
	for i := 0; i < workers; i++ {
		attachmentID := fmt.Sprintf("att-%d", i)
		go func() {
			<-start
			_, inserted, err := r.insert(testConnA, attachmentID, newTestAccumulator())
			switch {
			case err != nil && !errors.Is(err, ErrTooManyUploads):
				t.Errorf("insert of the distinct pair %q: error = %v, want nil or one wrapping %v", attachmentID, err, ErrTooManyUploads)
			case err == nil && !inserted:
				t.Errorf("insert of the distinct pair %q reported a repeat", attachmentID)
			}
			// Only an insert that happened has an entry to find; a refused one
			// is absent by definition. Release is unconditional either way —
			// releasing a pair the registry does not hold is a tested no-op.
			if inserted {
				if _, ok := r.Lookup(testConnA, attachmentID); !ok {
					t.Errorf("Lookup of the pair %q this goroutine just inserted reported absent", attachmentID)
				}
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

	// The conn-wide sweep runs against every operation above, which is what buys
	// -race coverage of ReleaseConn concurrent with insert, Release, Lookup and
	// count. It targets testConnB and MUST NOT target testConnA: the workers
	// assert that a pair they just inserted under that conn is present, and a
	// concurrent sweep of it would delete the entry between the insert and the
	// look-up, failing that assertion for a reason with nothing to do with what is
	// under test. testConnB's own readers assert safety and never ordering, and
	// the closing count() holds either way.
	go func() {
		<-start
		for j := 0; j < 64; j++ {
			r.ReleaseConn(testConnB)
		}
		done <- struct{}{}
	}()

	close(start)
	for i := 0; i < workers+readers+2; i++ {
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
			got, inserted, _ := r.insert(testConnA, testAttachmentID, own)
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
// removal a sole red: a declaration both checks refuse stays GREEN under either
// removal, because the check that survives refuses it anyway, and therefore
// proves neither. It would also pin a cross-check order that nothing has
// decided, since the two answer different sentinels.
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
// error, and does not need to: this test's repeat carries the SAME, ADMISSIBLE
// declaration, the case Admit's doc commits to answering with the incumbent, and
// the incumbent must survive whatever error comes back. The repeat whose
// declaration is refused answers that sentinel instead of the incumbent, and
// TestRegistry_AdmitRefusedRepeatUnderAHeldPair_KeepsTheIncumbent is what pins
// it.
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

// TestRegistry_AdmitRefusedRepeatUnderAHeldPair_KeepsTheIncumbent pins the one
// behaviour a careless restructure flips: BOTH declaration checks run on a
// repeat, ahead of the look-up that would find the incumbent, so a repeat
// carrying an invalid declaration answers that declaration's sentinel rather
// than being handed the live transfer. Every other test in this file stays green
// against an Admit that looks the incumbent up first and returns early — the
// sibling refusal rows admit on a fresh registry, so the look-up misses and the
// checks run either way, and TestRegistry_AdmitUnderAHeldPair_KeepsTheIncumbent
// repeats the SAME, admissible declaration, so it reaches the incumbent by
// either route.
//
// ONE FAULT PER ROW, for the reason
// TestRegistry_AdmitRefusedDeclaration_StoresNothing states, and the two
// declarations are that test's two verbatim: the only thing that differs here is
// that the pair is already held.
func TestRegistry_AdmitRefusedRepeatUnderAHeldPair_KeepsTheIncumbent(t *testing.T) {
	t.Parallel()

	const held = 2

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
			incumbent, err := r.Admit(testConnA, testAttachmentID, testBoundTotal, maxUploadBytes, testBoundFixtureDigest)
			if err != nil {
				t.Fatalf("Admit of an admissible declaration: %v", err)
			}
			for i := 0; i < held; i++ {
				if err := incumbent.Add(boundChunk(i)); err != nil {
					t.Fatalf("feeding the transfer in flight chunk %d: %v", i, err)
				}
			}

			// A second first-chunk for the live pair, differing from the
			// incumbent's declaration only in the two declared numbers.
			repeat, err := r.Admit(testConnA, testAttachmentID, tt.totalChunks, tt.size, testBoundFixtureDigest)
			if !errors.Is(err, tt.want) {
				t.Fatalf("Admit of a repeat declaring %s: error = %v, want one wrapping %v", tt.name, err, tt.want)
			}
			if repeat != nil {
				t.Errorf("Admit returned a non-nil accumulator for a refused repeat")
			}

			// The refusal disturbed nothing. Pointer identity, not field
			// equality: an accumulator built from the same declaration compares
			// equal field by field and holds none of the chunks in flight.
			if got, ok := r.Lookup(testConnA, testAttachmentID); !ok || got != incumbent {
				t.Errorf("Lookup after a refused repeat answered (%p, %v), want the incumbent", got, ok)
			}
			if n := r.count(); n != 1 {
				t.Errorf("count() = %d, want 1", n)
			}

			// The chunks the transfer already held survived the refusal, and it
			// still assembles.
			for i := held; i < testBoundTotal; i++ {
				if err := incumbent.Add(boundChunk(i)); err != nil {
					t.Fatalf("feeding the transfer in flight chunk %d: %v", i, err)
				}
			}
			assembled, err := incumbent.Assemble()
			if err != nil {
				t.Fatalf("Assemble() after a refused repeat: %v", err)
			}
			if !bytes.Equal(assembled, testBoundFixture) {
				t.Errorf("Assemble() returned %d bytes that differ from the fixture, want its %d bytes", len(assembled), len(testBoundFixture))
			}
		})
	}
}

// fillRegistry admits n uploads under ids distinct from testAttachmentID, and
// through Admit rather than insert so that what it fills is exactly the set of
// live admitted uploads maxInFlightUploads counts.
//
// Each filler declares (total_chunks 1, size 0), the cheapest CONFORMING pair,
// since max(1, ceil(0 / 45000)) is 1. That is not incidental: the capacity gate
// is the LATER of three gates, so a filler whose declaration did not conform
// would be refused by a declaration check and the registry would never reach the
// bound at all — every row below would then go green having proved nothing.
func fillRegistry(t *testing.T, r *Registry, n int) {
	t.Helper()

	for i := 0; i < n; i++ {
		attachmentID := fmt.Sprintf("filler-%d", i)
		if _, err := r.Admit(testConnA, attachmentID, 1, 0, testFixtureDigest); err != nil {
			t.Fatalf("filling the registry: Admit of %q: %v", attachmentID, err)
		}
	}
	if got := r.count(); got != n {
		t.Fatalf("after filling, count() = %d, want %d", got, n)
	}
}

func TestRegistry_AdmitAtTheBound_RefusesANewPair(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	fillRegistry(t, r, maxInFlightUploads)

	upload, err := r.Admit(testConnA, testAttachmentID, testBoundTotal, maxUploadBytes, testBoundFixtureDigest)
	if !errors.Is(err, ErrTooManyUploads) {
		t.Fatalf("Admit of a new pair at the bound: error = %v, want one wrapping %v", err, ErrTooManyUploads)
	}
	// A THIRD sentinel rather than either neighbour reused, and that is an
	// assertion rather than a naming convention: this bound clears by itself as
	// other uploads finish, and both neighbours are permanent for what they
	// refused, so a client that cannot tell them apart backs off from a fault
	// that will never clear or hot-loops on one that would have.
	if errors.Is(err, ErrUploadTooLarge) {
		t.Errorf("the capacity refusal also wraps %v, want a sentinel distinct from it", ErrUploadTooLarge)
	}
	if errors.Is(err, ErrInvalidDeclaration) {
		t.Errorf("the capacity refusal also wraps %v, want a sentinel distinct from it", ErrInvalidDeclaration)
	}
	if upload != nil {
		t.Errorf("Admit returned a non-nil accumulator for a pair refused by the bound")
	}

	// The refusal took no slot and left no entry: the registry holds exactly
	// what it held before the chunk arrived.
	if n := r.count(); n != maxInFlightUploads {
		t.Errorf("count() after a capacity refusal = %d, want %d", n, maxInFlightUploads)
	}
	if _, ok := r.Lookup(testConnA, testAttachmentID); ok {
		t.Errorf("Lookup after a capacity refusal reported the refused pair present")
	}

	// The same two prohibitions TestRegistry_AdmitRefusedDeclaration_StoresNothing
	// asserts, and the same shape of control: the bound must be in the message,
	// since a refusal that swallowed it would satisfy both prohibitions by
	// carrying nothing at all.
	msg := err.Error()
	if strings.Contains(msg, testAttachmentID) {
		t.Errorf("refusal message %q carries the attachment_id", msg)
	}
	if strings.Contains(msg, testBoundFixtureDigest) {
		t.Errorf("refusal message %q carries the declared digest", msg)
	}
	if bound := strconv.Itoa(maxInFlightUploads); !strings.Contains(msg, bound) {
		t.Errorf("refusal message %q does not carry the bound %s", msg, bound)
	}

	// Nothing is blacklisted and nothing was consumed: an immediate retry of the
	// same id is refused the same way while the bound is still met.
	if _, err := r.Admit(testConnA, testAttachmentID, testBoundTotal, maxUploadBytes, testBoundFixtureDigest); !errors.Is(err, ErrTooManyUploads) {
		t.Errorf("an immediate retry of the refused id: error = %v, want one wrapping %v", err, ErrTooManyUploads)
	}
	if n := r.count(); n != maxInFlightUploads {
		t.Errorf("count() after the retry = %d, want %d", n, maxInFlightUploads)
	}
}

// TestRegistry_AdmitAtTheBound_AnswersTheDeclarationSentinelFirst pins the one
// gate ordering this package makes a contract: the bound is the LAST of the
// three, behind both declaration checks. At capacity, a first chunk whose
// declaration is also invalid is told the fault that will NEVER clear ahead of
// the one that clears by itself. It is the sole red for a gate placed in front
// of the checks, which satisfies every other row in this file.
//
// ONE FAULT PER ROW, for the reason
// TestRegistry_AdmitRefusedDeclaration_StoresNothing states, and the two
// declarations are that test's two verbatim: the only thing that differs here is
// that the registry is full.
func TestRegistry_AdmitAtTheBound_AnswersTheDeclarationSentinelFirst(t *testing.T) {
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
			fillRegistry(t, r, maxInFlightUploads)

			upload, err := r.Admit(testConnA, testAttachmentID, tt.totalChunks, tt.size, testFixtureDigest)
			if !errors.Is(err, tt.want) {
				t.Fatalf("Admit at the bound of a declaration with %s: error = %v, want one wrapping %v", tt.name, err, tt.want)
			}
			if errors.Is(err, ErrTooManyUploads) {
				t.Errorf("Admit at the bound answered %v for a declaration the checks refuse, want the declaration sentinel", ErrTooManyUploads)
			}
			if upload != nil {
				t.Errorf("Admit returned a non-nil accumulator for a refused declaration")
			}
			if n := r.count(); n != maxInFlightUploads {
				t.Errorf("count() after a declaration refusal at the bound = %d, want %d", n, maxInFlightUploads)
			}
		})
	}
}

// TestRegistry_AdmitAtTheBound_ReleaseReturnsTheSlot makes two statements about
// slot accounting, both about what does NOT consume a slot and what returns one.
func TestRegistry_AdmitAtTheBound_ReleaseReturnsTheSlot(t *testing.T) {
	t.Parallel()

	const nextAttachmentID = "att-next"

	r := NewRegistry()
	fillRegistry(t, r, maxInFlightUploads-1)

	// (a) One short of the bound, a DECLARATION refusal does not consume the
	// last free slot — it never took one — so the same id, re-declared
	// correctly, is admitted into that slot immediately afterwards.
	if _, err := r.Admit(testConnA, testAttachmentID, testBoundTotal, maxUploadBytes+1, testBoundFixtureDigest); !errors.Is(err, ErrUploadTooLarge) {
		t.Fatalf("Admit of an oversized declaration below the bound: error = %v, want one wrapping %v", err, ErrUploadTooLarge)
	}
	if n := r.count(); n != maxInFlightUploads-1 {
		t.Fatalf("count() after a declaration refusal = %d, want %d", n, maxInFlightUploads-1)
	}
	if _, err := r.Admit(testConnA, testAttachmentID, testBoundTotal, maxUploadBytes, testBoundFixtureDigest); err != nil {
		t.Fatalf("Admit of an admissible re-declaration into the last free slot: %v", err)
	}
	if n := r.count(); n != maxInFlightUploads {
		t.Fatalf("count() after the re-declaration = %d, want %d", n, maxInFlightUploads)
	}

	// (b) At the bound a new pair is refused; releasing an admitted upload
	// returns its slot and the same new pair is then admitted.
	if _, err := r.Admit(testConnA, nextAttachmentID, 1, 0, testFixtureDigest); !errors.Is(err, ErrTooManyUploads) {
		t.Fatalf("Admit of a new pair at the bound: error = %v, want one wrapping %v", err, ErrTooManyUploads)
	}
	r.Release(testConnA, testAttachmentID)
	if n := r.count(); n != maxInFlightUploads-1 {
		t.Fatalf("count() after Release = %d, want %d", n, maxInFlightUploads-1)
	}
	if _, err := r.Admit(testConnA, nextAttachmentID, 1, 0, testFixtureDigest); err != nil {
		t.Errorf("Admit of the refused pair after a Release freed its slot: %v", err)
	}
	if n := r.count(); n != maxInFlightUploads {
		t.Errorf("count() after the slot was refilled = %d, want %d", n, maxInFlightUploads)
	}
}

// TestRegistry_ConcurrentAdmitAtTheBound_AdmitsExactlyTheFreeSlots is the
// indivisibility pin: count-and-admit is ONE critical section, so with one free
// slot exactly one of many concurrent first chunks for DISTINCT new pairs is
// admitted, however they interleave.
//
// Its control is a registry that reads the count under one acquisition and
// admits under another — Admit calling count() before taking mu, deciding on
// that stale number inside it, and insertLocked losing its gate. That shape is
// race-free, passes -race, and satisfies every other row in this file. MEASURED
// against it: 200 rounds × 8 goroutines is red in 5 of 5 iterations of
// -count=5 -race, against 5 of 5 green on the unmodified tree.
//
// ROUNDS ARE THE LEVER, NOT GOROUTINE COUNT.
// TestRegistry_ConcurrentSamePairInsert_TellsExactlyOneItIsFresh is the pattern
// for the fan-out mechanics and NOT for its structure: one wide round measured
// 0 reds in 5 against a split-lock control on an earlier ticket here, because a
// single fan-out gives the interleaving one chance to happen.
func TestRegistry_ConcurrentAdmitAtTheBound_AdmitsExactlyTheFreeSlots(t *testing.T) {
	t.Parallel()

	const (
		rounds     = 200
		goroutines = 8
	)

	for round := 0; round < rounds; round++ {
		r := NewRegistry()
		fillRegistry(t, r, maxInFlightUploads-1)

		start := make(chan struct{})
		errs := make(chan error, goroutines)
		for i := 0; i < goroutines; i++ {
			attachmentID := fmt.Sprintf("racer-%d", i)
			go func() {
				<-start
				_, err := r.Admit(testConnA, attachmentID, 1, 0, testFixtureDigest)
				errs <- err
			}()
		}
		close(start)

		admitted := 0
		for i := 0; i < goroutines; i++ {
			switch err := <-errs; {
			case err == nil:
				admitted++
			case !errors.Is(err, ErrTooManyUploads):
				t.Fatalf("round %d: Admit of a distinct new pair: error = %v, want nil or one wrapping %v", round, err, ErrTooManyUploads)
			}
		}
		if admitted != 1 {
			t.Fatalf("round %d: goroutines admitted into 1 free slot = %d, want exactly 1", round, admitted)
		}
		if n := r.count(); n != maxInFlightUploads {
			t.Fatalf("round %d: count() = %d, want %d", round, n, maxInFlightUploads)
		}
	}
}

// TestRegistry_DeliverAtTheBound_ChargesAHeldPairOnlyOnce is AC 1, and both its
// clauses run against a registry sitting EXACTLY at maxInFlightUploads with the
// subject pair among the held entries — which is the whole point, since a bound
// that a held pair is charged against a second time is a bound that locks a live
// transfer out of its own accumulator.
//
// Clause (b) is also the coverage gap #1796 shipped with and code review
// measured: an overlay mutant hoisting the capacity check ahead of the incumbent
// look-up in insertLocked passed green across the whole package, because no
// fixture drove a held pair while the registry was full. A gate guaranteed by the
// code's shape is not covered until a test tries to violate the shape, and this
// is that test. The repeat's declaration is the incumbent's VERBATIM and must
// stay admissible: a refused repeat answers its own declaration sentinel, which
// TestRegistry_AdmitRefusedRepeatUnderAHeldPair_KeepsTheIncumbent pins and which
// would prove nothing about the gate order.
func TestRegistry_DeliverAtTheBound_ChargesAHeldPairOnlyOnce(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	fillRegistry(t, r, maxInFlightUploads-1)

	// The bound declaration, so the subject's transfer cannot finish in one
	// chunk and the pair is still held when clause (b) runs.
	incumbent, err := r.Admit(testConnA, testAttachmentID, testBoundTotal, maxUploadBytes, testBoundFixtureDigest)
	if err != nil {
		t.Fatalf("Admit of the subject pair into the last free slot: %v", err)
	}
	if n := r.count(); n != maxInFlightUploads {
		t.Fatalf("count() with the subject admitted = %d, want %d", n, maxInFlightUploads)
	}

	// (a) A chunk for the held pair reaches that pair's accumulator rather than
	// meeting the bound: the transfer is 373 chunks, so one chunk answers the
	// resumable sentinel and not the concurrency one.
	out, err := r.Deliver(testConnA, withAttachmentID(boundChunk(0), testAttachmentID))
	if !errors.Is(err, ErrIncomplete) {
		t.Fatalf("Deliver of a chunk for a held pair at the bound: error = %v, want one wrapping %v", err, ErrIncomplete)
	}
	if errors.Is(err, ErrTooManyUploads) {
		t.Errorf("Deliver of a chunk for a HELD pair answered %v, want the held pair's own transfer", ErrTooManyUploads)
	}
	if out != nil {
		t.Errorf("Deliver of an incomplete transfer returned %d bytes, want none", len(out))
	}
	if n := r.count(); n != maxInFlightUploads {
		t.Errorf("count() after delivering to a held pair = %d, want %d", n, maxInFlightUploads)
	}

	// (b) A repeat declaration for the held pair answers the incumbent rather
	// than the bound, and takes no second slot.
	repeat, err := r.Admit(testConnA, testAttachmentID, testBoundTotal, maxUploadBytes, testBoundFixtureDigest)
	if err != nil {
		t.Fatalf("Admit of a repeat declaration for a held pair at the bound: %v", err)
	}
	if repeat != incumbent {
		t.Errorf("Admit of a repeat for a held pair at the bound answered an accumulator other than the incumbent")
	}
	if n := r.count(); n != maxInFlightUploads {
		t.Errorf("count() after the repeat = %d, want %d", n, maxInFlightUploads)
	}

	// The chunk clause (a) delivered went into the incumbent and survived the
	// repeat: a second delivery of index 0 is a duplicate rather than a fresh
	// chunk. It also ends the transfer, which the closing count assertion reads.
	if _, err := r.Deliver(testConnA, withAttachmentID(boundChunk(0), testAttachmentID)); !errors.Is(err, ErrDuplicateIndex) {
		t.Fatalf("Deliver of index 0 a second time: error = %v, want one wrapping %v", err, ErrDuplicateIndex)
	}
	if n := r.count(); n != maxInFlightUploads-1 {
		t.Errorf("count() after the refusal ended the subject's transfer = %d, want %d", n, maxInFlightUploads-1)
	}
}

// TestRegistry_Deliver_EndOfAnUploadReturnsTheSlot is AC 2 and AC 3 in one
// table: every way an upload ENDS here, and the identical slot accounting after
// each. Every row runs with the registry at the bound, so "the slot came back"
// is asserted the way a client observes it — a fresh attachment_id admitted
// immediately afterwards, which is refused outright when nothing was released.
//
// The six latching sentinels are one table rather than six tests because the
// assertion after the refusal is the same for all of them: reject latches and
// drops the bytes wherever it is called from, and this surface's job is only to
// stop holding the entry. The ErrUploadTooLarge row is the Add form of that
// sentinel; the CheckDeclaredSize form is refused inside Admit before an
// Accumulator exists and never reaches here.
func TestRegistry_Deliver_EndOfAnUploadReturnsTheSlot(t *testing.T) {
	t.Parallel()

	const freshAttachmentID = "att-fresh"

	tests := []struct {
		name        string
		totalChunks int
		size        int64
		sha256      string
		chunks      []protocol.AttachmentChunkPayload
		want        error  // nil for the completing row
		wantBytes   []byte // non-nil only for the completing row
	}{
		{
			name:        "completes",
			totalChunks: 1,
			size:        int64(len(testFixture)),
			sha256:      testFixtureDigest,
			chunks:      []protocol.AttachmentChunkPayload{testChunk(0, 1, testFixture)},
			wantBytes:   testFixture,
		},
		{
			// The chunk's own total disagrees with the count the transfer was
			// admitted under, which Add refuses ahead of the range check.
			name:        "total_chunks disagrees",
			totalChunks: 1,
			size:        int64(len(testFixture)),
			sha256:      testFixtureDigest,
			chunks:      []protocol.AttachmentChunkPayload{testChunk(0, 2, testFixture)},
			want:        ErrTotalChunksMismatch,
		},
		{
			// The chunk carries the DECLARED total, so the total check ahead of
			// the range check passes and the range check is what refuses it.
			name:        "index out of range",
			totalChunks: 1,
			size:        int64(len(testFixture)),
			sha256:      testFixtureDigest,
			chunks:      []protocol.AttachmentChunkPayload{testChunk(1, 1, testFixture)},
			want:        ErrIndexOutOfRange,
		},
		{
			// The bound declaration, and it cannot be shrunk: a duplicate index
			// is only reachable on a transfer a first chunk does not complete.
			name:        "duplicate index",
			totalChunks: testBoundTotal,
			size:        maxUploadBytes,
			sha256:      testBoundFixtureDigest,
			chunks:      []protocol.AttachmentChunkPayload{boundChunk(0), boundChunk(0)},
			want:        ErrDuplicateIndex,
		},
		{
			// The bound declaration for the same reason: the byte bound is only
			// crossable by a transfer declared large enough to be admitted at
			// all. The first chunk carries the whole fixture, which Add ACCEPTS
			// — its rung is > and not >= — and the one-byte second chunk is what
			// crosses.
			name:        "chunk crosses the byte bound",
			totalChunks: testBoundTotal,
			size:        maxUploadBytes,
			sha256:      testBoundFixtureDigest,
			chunks: []protocol.AttachmentChunkPayload{
				testChunk(0, testBoundTotal, testBoundFixture),
				testChunk(1, testBoundTotal, []byte("z")),
			},
			want: ErrUploadTooLarge,
		},
		{
			// A size testFixture does not have, and one CheckDeclaration still
			// admits at a count of 1, so the refusal comes from Assemble's
			// length comparison rather than from admission.
			name:        "assembled length wrong",
			totalChunks: 1,
			size:        int64(len(testFixture)) + 1,
			sha256:      testFixtureDigest,
			chunks:      []protocol.AttachmentChunkPayload{testChunk(0, 1, testFixture)},
			want:        ErrSizeMismatch,
		},
		{
			// The declared LENGTH is right, so the length comparison passes and
			// the digest comparison is what refuses it.
			name:        "digest wrong",
			totalChunks: 1,
			size:        int64(len(testFixture)),
			sha256:      testEmptyDigest,
			chunks:      []protocol.AttachmentChunkPayload{testChunk(0, 1, testFixture)},
			want:        ErrDigestMismatch,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := NewRegistry()
			fillRegistry(t, r, maxInFlightUploads-1)
			if _, err := r.Admit(testConnA, testAttachmentID, tt.totalChunks, tt.size, tt.sha256); err != nil {
				t.Fatalf("Admit of the subject pair into the last free slot: %v", err)
			}
			if n := r.count(); n != maxInFlightUploads {
				t.Fatalf("count() with the subject admitted = %d, want %d", n, maxInFlightUploads)
			}

			// Every chunk but the last must be ACCEPTED — nil or the resumable
			// sentinel — so the row's verdict is the last delivery's alone.
			for i, chunk := range tt.chunks[:len(tt.chunks)-1] {
				_, err := r.Deliver(testConnA, withAttachmentID(chunk, testAttachmentID))
				if err != nil && !errors.Is(err, ErrIncomplete) {
					t.Fatalf("Deliver of chunk %d, which the row expects to be accepted: %v", i, err)
				}
			}

			out, err := r.Deliver(testConnA, withAttachmentID(tt.chunks[len(tt.chunks)-1], testAttachmentID))
			switch {
			case tt.want == nil && err != nil:
				t.Fatalf("Deliver of the completing chunk: %v", err)
			case tt.want != nil && !errors.Is(err, tt.want):
				t.Fatalf("Deliver of the ending chunk: error = %v, want one wrapping %v", err, tt.want)
			}
			if tt.wantBytes == nil {
				if out != nil {
					t.Errorf("Deliver of a refused transfer returned %d bytes, want none", len(out))
				}
			} else if !bytes.Equal(out, tt.wantBytes) {
				t.Errorf("Deliver of the completing chunk returned %q, want %q", out, tt.wantBytes)
			}

			// The slot came back, asserted three ways: the count, the pair's
			// absence, and the client-observable one — a FRESH attachment_id
			// admitted into the freed slot, using the filler declaration.
			if n := r.count(); n != maxInFlightUploads-1 {
				t.Fatalf("count() after the upload ended = %d, want %d", n, maxInFlightUploads-1)
			}
			if _, ok := r.Lookup(testConnA, testAttachmentID); ok {
				t.Errorf("Lookup after the upload ended reported the pair present")
			}
			if _, err := r.Admit(testConnA, freshAttachmentID, 1, 0, testFixtureDigest); err != nil {
				t.Fatalf("Admit of a fresh attachment_id into the freed slot: %v", err)
			}
			if n := r.count(); n != maxInFlightUploads {
				t.Errorf("count() after the freed slot was refilled = %d, want %d", n, maxInFlightUploads)
			}
		})
	}
}

// TestRegistry_DeliverIncomplete_KeepsTheEntry is AC 4, and it is the suite's
// only end-to-end statement through Deliver — which is why it earns the 16 MiB
// fixture the ends-an-upload table's completing row deliberately does not spend.
// The completing round trip is what proves all 373 chunks reached ONE
// accumulator rather than 373 fresh ones, and the pointer identity is what
// proves the registry did not swap it mid-transfer.
func TestRegistry_DeliverIncomplete_KeepsTheEntry(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	admitted, err := r.Admit(testConnA, testAttachmentID, testBoundTotal, maxUploadBytes, testBoundFixtureDigest)
	if err != nil {
		t.Fatalf("Admit of the bound declaration: %v", err)
	}

	if _, err := r.Deliver(testConnA, withAttachmentID(boundChunk(0), testAttachmentID)); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("Deliver of chunk 0 of %d: error = %v, want one wrapping %v", testBoundTotal, err, ErrIncomplete)
	}
	if n := r.count(); n != 1 {
		t.Fatalf("count() after an incomplete delivery = %d, want 1", n)
	}
	// Pointer identity, not field equality: an accumulator built from the same
	// declaration compares equal field by field and holds no chunks.
	if got, ok := r.Lookup(testConnA, testAttachmentID); !ok || got != admitted {
		t.Fatalf("Lookup after an incomplete delivery answered (%p, %v), want the accumulator Admit returned", got, ok)
	}

	// A later chunk still reaches the same accumulator.
	if _, err := r.Deliver(testConnA, withAttachmentID(boundChunk(1), testAttachmentID)); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("Deliver of chunk 1 of %d: error = %v, want one wrapping %v", testBoundTotal, err, ErrIncomplete)
	}
	if n := r.count(); n != 1 {
		t.Fatalf("count() after a second incomplete delivery = %d, want 1", n)
	}

	var out []byte
	for i := 2; i < testBoundTotal; i++ {
		got, err := r.Deliver(testConnA, withAttachmentID(boundChunk(i), testAttachmentID))
		if i < testBoundTotal-1 {
			if !errors.Is(err, ErrIncomplete) {
				t.Fatalf("Deliver of chunk %d of %d: error = %v, want one wrapping %v", i, testBoundTotal, err, ErrIncomplete)
			}
			continue
		}
		if err != nil {
			t.Fatalf("Deliver of the completing chunk %d of %d: %v", i, testBoundTotal, err)
		}
		out = got
	}
	if !bytes.Equal(out, testBoundFixture) {
		t.Errorf("Deliver of the completing chunk returned %d bytes that differ from the fixture, want its %d bytes", len(out), len(testBoundFixture))
	}
	if n := r.count(); n != 0 {
		t.Errorf("count() after the transfer completed = %d, want 0", n)
	}
}

// TestRegistry_DeliverUnheldPair_RefusesAndCreatesNothing is AC 5. The
// cross-conn row is the only place this method exercises the conn half of the
// key, and it is what makes the presence answer safe to give at all: a caller
// can only ever probe transfers on its own conn.
func TestRegistry_DeliverUnheldPair_RefusesAndCreatesNothing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		connID       string
		attachmentID string
	}{
		{
			name:         "attachment_id never admitted on this conn",
			connID:       testConnA,
			attachmentID: "att-never-admitted",
		},
		{
			// Held, but by the other conn.
			name:         "held attachment_id delivered on another conn",
			connID:       testConnB,
			attachmentID: testAttachmentID,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := NewRegistry()
			if _, err := r.Admit(testConnA, testAttachmentID, 1, int64(len(testFixture)), testFixtureDigest); err != nil {
				t.Fatalf("Admit of the subject pair: %v", err)
			}

			out, err := r.Deliver(tt.connID, withAttachmentID(testChunk(0, 1, testFixture), tt.attachmentID))
			if !errors.Is(err, ErrUnknownUpload) {
				t.Fatalf("Deliver for a pair the registry does not hold: error = %v, want one wrapping %v", err, ErrUnknownUpload)
			}
			if out != nil {
				t.Errorf("Deliver for an unheld pair returned %d bytes, want none", len(out))
			}

			// The refusal is distinct from every sentinel this package already
			// publishes — the claim is over the SET, so it is asserted over the
			// set rather than over a sample of it.
			for _, sentinel := range packageSentinels {
				if errors.Is(err, sentinel) {
					t.Errorf("the unknown-pair refusal also wraps %v, want a sentinel distinct from every existing one", sentinel)
				}
			}

			// Nothing was created: the delivered pair is still absent and the
			// registry holds exactly the one entry it held before.
			if _, ok := r.Lookup(tt.connID, tt.attachmentID); ok {
				t.Errorf("Lookup after an unknown-pair refusal reported the delivered pair present")
			}
			if n := r.count(); n != 1 {
				t.Errorf("count() after an unknown-pair refusal = %d, want 1", n)
			}
		})
	}
}

// fakeClock is a controllable time source for the registry's `now` seam,
// mirroring streamsup's watchdog_test double. It is the first clock double in
// this package.
//
// It locks even though every test below is single-goroutine: the concurrency
// tests above are one edit away from wanting a controllable clock, and a
// lock-free double would be a -race landmine for whoever makes that edit. That
// lock is also the third of the constraints newRegistryWithClock states, since
// advance runs on the test's goroutine while a stamping method reads it under
// Registry.mu, which does not cover this type.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// testClockStart is the instant every fake clock in this file starts at. Its
// exact value carries no meaning; what matters is that it is not the zero
// time.Time, so a stamp that was never written is distinguishable from one that
// was.
var testClockStart = time.Unix(1_000, 0)

// Time comparisons below use Equal and never == or reflect.DeepEqual, per
// docs/PROJECT-MEMORY.md § "time.Time round-trip discipline": a wall-clock
// reading carries a monotonic component that == compares and Equal does not.

// TestRegistry_AdmitStampsTheClockReading is AC 1. Its second half is the half
// that matters: a registry that stored the clock FUNCTION and read it on demand
// would satisfy the first assertion and fail this one, and it is the difference
// between a recorded instant an idle window can measure and a value that is
// always "now".
func TestRegistry_AdmitStampsTheClockReading(t *testing.T) {
	t.Parallel()

	clk := &fakeClock{t: testClockStart}
	r := newRegistryWithClock(clk.now)

	if _, err := r.Admit(testConnA, testAttachmentID, testBoundTotal, maxUploadBytes, testBoundFixtureDigest); err != nil {
		t.Fatalf("Admit of the bound declaration: %v", err)
	}

	stamped, ok := r.lastChunkAt(testConnA, testAttachmentID)
	if !ok {
		t.Fatalf("lastChunkAt(%q, %q) reported absent after Admit, want present", testConnA, testAttachmentID)
	}
	if !stamped.Equal(testClockStart) {
		t.Errorf("lastChunkAt after Admit = %v, want the clock's reading %v", stamped, testClockStart)
	}

	// The stamp is a RECORDED INSTANT, not a live read of the seam.
	clk.advance(time.Hour)
	after, ok := r.lastChunkAt(testConnA, testAttachmentID)
	if !ok {
		t.Fatalf("lastChunkAt reported absent after the clock advanced with nothing delivered, want present")
	}
	if !after.Equal(stamped) {
		t.Errorf("lastChunkAt after advancing the clock = %v, want the admission reading %v", after, stamped)
	}
}

// TestRegistry_DeliverMovesTheStampForwardForThatPairOnly is AC 2. The two pairs
// share one attachment_id across two conns — uploadKey's own shape — so the
// unchanged-pair assertion is a statement about the KEY and not merely about two
// unrelated entries. ErrIncomplete is the outcome deliberately chosen: it is the
// one answer that keeps the entry, so the stamp is still there to read.
func TestRegistry_DeliverMovesTheStampForwardForThatPairOnly(t *testing.T) {
	t.Parallel()

	clk := &fakeClock{t: testClockStart}
	r := newRegistryWithClock(clk.now)

	for _, connID := range []string{testConnA, testConnB} {
		if _, err := r.Admit(connID, testAttachmentID, testBoundTotal, maxUploadBytes, testBoundFixtureDigest); err != nil {
			t.Fatalf("Admit of the bound declaration on %q: %v", connID, err)
		}
	}
	beforeA, okA := r.lastChunkAt(testConnA, testAttachmentID)
	beforeB, okB := r.lastChunkAt(testConnB, testAttachmentID)
	if !okA || !okB {
		t.Fatalf("lastChunkAt after admitting both pairs: %q present = %v, %q present = %v, want both present", testConnA, okA, testConnB, okB)
	}

	clk.advance(time.Minute)
	if _, err := r.Deliver(testConnA, withAttachmentID(boundChunk(0), testAttachmentID)); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("Deliver of chunk 0 of %d: error = %v, want one wrapping %v", testBoundTotal, err, ErrIncomplete)
	}

	gotA, ok := r.lastChunkAt(testConnA, testAttachmentID)
	if !ok {
		t.Fatalf("lastChunkAt on %q after an incomplete delivery reported absent, want present", testConnA)
	}
	want := testClockStart.Add(time.Minute)
	if !gotA.Equal(want) {
		t.Errorf("lastChunkAt on %q after a delivered chunk = %v, want the clock's reading at that moment %v", testConnA, gotA, want)
	}
	if !gotA.After(beforeA) {
		t.Errorf("lastChunkAt on %q did not move forward: %v, was %v", testConnA, gotA, beforeA)
	}

	gotB, ok := r.lastChunkAt(testConnB, testAttachmentID)
	if !ok {
		t.Fatalf("lastChunkAt on %q reported absent after a delivery on %q, want present", testConnB, testConnA)
	}
	if !gotB.Equal(beforeB) {
		t.Errorf("delivering a chunk on %q moved %q's stamp to %v, want it left at %v", testConnA, testConnB, gotB, beforeB)
	}
}

// TestRegistry_LookupAndRepeatAdmitLeaveTheStampWhereItIs is AC 3: only a
// DELIVERED CHUNK moves the time. Both clauses are what stops a client holding a
// slot open indefinitely by re-sending a first chunk it never follows, so both
// advance the clock first — a clause that failed to advance would pass against a
// stamping look-up too.
func TestRegistry_LookupAndRepeatAdmitLeaveTheStampWhereItIs(t *testing.T) {
	t.Parallel()

	clk := &fakeClock{t: testClockStart}
	r := newRegistryWithClock(clk.now)

	incumbent, err := r.Admit(testConnA, testAttachmentID, testBoundTotal, maxUploadBytes, testBoundFixtureDigest)
	if err != nil {
		t.Fatalf("Admit of the bound declaration: %v", err)
	}

	// (a) a pure read.
	clk.advance(time.Minute)
	if got, ok := r.Lookup(testConnA, testAttachmentID); !ok || got != incumbent {
		t.Fatalf("Lookup answered (%p, %v), want the accumulator Admit returned", got, ok)
	}
	stamp, ok := r.lastChunkAt(testConnA, testAttachmentID)
	if !ok {
		t.Fatalf("lastChunkAt after a Lookup reported absent, want present")
	}
	if !stamp.Equal(testClockStart) {
		t.Errorf("Lookup moved lastChunkAt to %v, want it left at the admission reading %v", stamp, testClockStart)
	}

	// (b) a repeat admission under the held pair, which still answers the
	// incumbent exactly as TestRegistry_AdmitUnderAHeldPair_KeepsTheIncumbent
	// asserts today.
	clk.advance(time.Minute)
	repeat, err := r.Admit(testConnA, testAttachmentID, testBoundTotal, maxUploadBytes, testBoundFixtureDigest)
	if err != nil {
		t.Fatalf("Admit under a held pair: %v", err)
	}
	if repeat != incumbent {
		t.Errorf("Admit under a held pair answered an accumulator other than the incumbent")
	}
	stamp, ok = r.lastChunkAt(testConnA, testAttachmentID)
	if !ok {
		t.Fatalf("lastChunkAt after a repeat admission reported absent, want present")
	}
	if !stamp.Equal(testClockStart) {
		t.Errorf("a repeat Admit under a held pair moved lastChunkAt to %v, want it left at the admission reading %v", stamp, testClockStart)
	}
}

// TestRegistry_NewRegistryReadsTheWallClock is AC 4, bracketed rather than slept
// through. The bracket is falsified by a zero stamp, by a seam left nil and
// never read, and by any fixed instant substituted for a nil clock — which is
// every way the delegation could go wrong. All three readings carry monotonic
// components, so the comparison is exact rather than clock-resolution dependent.
func TestRegistry_NewRegistryReadsTheWallClock(t *testing.T) {
	t.Parallel()

	before := time.Now()
	r := NewRegistry()
	if _, err := r.Admit(testConnA, testAttachmentID, testBoundTotal, maxUploadBytes, testBoundFixtureDigest); err != nil {
		t.Fatalf("Admit of the bound declaration: %v", err)
	}
	after := time.Now()

	stamp, ok := r.lastChunkAt(testConnA, testAttachmentID)
	if !ok {
		t.Fatalf("lastChunkAt(%q, %q) reported absent after Admit, want present", testConnA, testAttachmentID)
	}
	if stamp.Before(before) || stamp.After(after) {
		t.Errorf("lastChunkAt from a registry built by NewRegistry = %v, want a wall-clock reading inside [%v, %v]", stamp, before, after)
	}
}

// TestRegistry_IdleUpload_NextChunkIsRefusedAsUnknown is AC 1, and its first arm
// is the boundary: at EXACTLY uploadIdleTimeout the upload is still in flight,
// because the window bounds how long silence may LAST rather than how long it
// may be approached — the polarity CheckDeclaredSize already argues for its own
// >. That arm alone reddens under >= in place of >, and the second arm reddens
// under a reap placed after lookupAndStamp's map read, which would find the
// expired entry, stamp it forward and resume exactly what the window exists to
// end.
func TestRegistry_IdleUpload_NextChunkIsRefusedAsUnknown(t *testing.T) {
	t.Parallel()

	clk := &fakeClock{t: testClockStart}
	r := newRegistryWithClock(clk.now)

	if _, err := r.Admit(testConnA, testAttachmentID, testBoundTotal, maxUploadBytes, testBoundFixtureDigest); err != nil {
		t.Fatalf("Admit of the bound declaration: %v", err)
	}

	clk.advance(uploadIdleTimeout)
	if _, err := r.Deliver(testConnA, withAttachmentID(boundChunk(0), testAttachmentID)); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("Deliver at exactly uploadIdleTimeout since the last chunk: error = %v, want one wrapping %v", err, ErrIncomplete)
	}

	// Past the window with nothing delivered in between. The chunk is a
	// perfectly good one for the transfer that was admitted, so nothing but the
	// reap can be refusing it.
	clk.advance(uploadIdleTimeout + time.Nanosecond)
	out, err := r.Deliver(testConnA, withAttachmentID(boundChunk(1), testAttachmentID))
	if !errors.Is(err, ErrUnknownUpload) {
		t.Fatalf("Deliver after more than uploadIdleTimeout of silence: error = %v, want %v", err, ErrUnknownUpload)
	}
	if out != nil {
		t.Errorf("Deliver for a reaped upload returned %d bytes, want none", len(out))
	}
	if _, ok := r.Lookup(testConnA, testAttachmentID); ok {
		t.Errorf("Lookup still reports the reaped pair as held")
	}
}

// TestRegistry_ExpiredUpload_GivesTheSlotBack is AC 2, and it is the test that
// reddens if the reap sits in Admit rather than ahead of insertLocked's capacity
// gate: the gate reads len(r.uploads) inside that body, so a reap anywhere else
// under the same acquisition is a reap the gate cannot see.
//
// count() is read AFTER the Admit that drove the reap and never before. That
// accessor deliberately does not reap, so a test that advanced the clock and
// then read it would be measuring a change nothing has caused.
func TestRegistry_ExpiredUpload_GivesTheSlotBack(t *testing.T) {
	t.Parallel()

	clk := &fakeClock{t: testClockStart}
	r := newRegistryWithClock(clk.now)
	fillRegistry(t, r, maxInFlightUploads)

	if _, err := r.Admit(testConnA, testAttachmentID, testBoundTotal, maxUploadBytes, testBoundFixtureDigest); !errors.Is(err, ErrTooManyUploads) {
		t.Fatalf("Admit of a new pair at the bound: error = %v, want one wrapping %v", err, ErrTooManyUploads)
	}

	clk.advance(uploadIdleTimeout + time.Nanosecond)

	upload, err := r.Admit(testConnA, testAttachmentID, testBoundTotal, maxUploadBytes, testBoundFixtureDigest)
	if err != nil {
		t.Fatalf("Admit of a new pair once every held upload had been idle past the window: %v", err)
	}
	if upload == nil {
		t.Fatal("Admit answered a nil accumulator with a nil error")
	}
	if got := r.count(); got != 1 {
		t.Errorf("count() after the reap and the admission = %d, want 1 — the newcomer alone", got)
	}
	if got, ok := r.Lookup(testConnA, testAttachmentID); !ok || got != upload {
		t.Errorf("Lookup answered (%p, %v), want the accumulator the post-reap Admit returned", got, ok)
	}
}

// TestRegistry_Window_RunsFromTheLastChunkNotFromAdmission is AC 3. Both
// deliveries land inside the window measured from the PREVIOUS chunk while the
// second one is well past it measured from admission, so it reddens both under a
// reap that compares against an admission-time field and under a registry whose
// deliveries never moved the stamp.
func TestRegistry_Window_RunsFromTheLastChunkNotFromAdmission(t *testing.T) {
	t.Parallel()

	clk := &fakeClock{t: testClockStart}
	r := newRegistryWithClock(clk.now)

	if _, err := r.Admit(testConnA, testAttachmentID, testBoundTotal, maxUploadBytes, testBoundFixtureDigest); err != nil {
		t.Fatalf("Admit of the bound declaration: %v", err)
	}

	clk.advance(uploadIdleTimeout - time.Minute)
	if _, err := r.Deliver(testConnA, withAttachmentID(boundChunk(0), testAttachmentID)); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("Deliver inside the window: error = %v, want one wrapping %v", err, ErrIncomplete)
	}

	clk.advance(uploadIdleTimeout - time.Minute)
	// The fixture's own arithmetic, asserted rather than trusted: a window
	// shorter than two minutes would leave both arms inside it and the test
	// would go green having proved nothing.
	if elapsed := clk.now().Sub(testClockStart); elapsed <= uploadIdleTimeout {
		t.Fatalf("elapsed since admission = %v, want more than uploadIdleTimeout (%v)", elapsed, uploadIdleTimeout)
	}
	if _, err := r.Deliver(testConnA, withAttachmentID(boundChunk(1), testAttachmentID)); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("Deliver still inside the window measured from the last chunk: error = %v, want one wrapping %v", err, ErrIncomplete)
	}
}

// TestRegistry_Reap_LeavesAFedUploadOfAnotherPairInFlight is AC 4. The two pairs
// share one attachment_id across two conns — uploadKey's own shape — so the
// survivor claim is about the KEY and not merely about two unrelated entries. It
// reddens under a reap that clears the map and under one that deletes on the
// wrong side of the comparison.
func TestRegistry_Reap_LeavesAFedUploadOfAnotherPairInFlight(t *testing.T) {
	t.Parallel()

	clk := &fakeClock{t: testClockStart}
	r := newRegistryWithClock(clk.now)

	for _, connID := range []string{testConnA, testConnB} {
		if _, err := r.Admit(connID, testAttachmentID, testBoundTotal, maxUploadBytes, testBoundFixtureDigest); err != nil {
			t.Fatalf("Admit of the bound declaration on %q: %v", connID, err)
		}
	}

	// A is fed twice, each time inside the window since its own last chunk; B
	// is never fed, so by the second delivery it has been silent for nearly two
	// windows and that delivery's own reap takes it.
	for i := 0; i < 2; i++ {
		clk.advance(uploadIdleTimeout - time.Minute)
		if _, err := r.Deliver(testConnA, withAttachmentID(boundChunk(i), testAttachmentID)); !errors.Is(err, ErrIncomplete) {
			t.Fatalf("Deliver of chunk %d on %q: error = %v, want one wrapping %v", i, testConnA, err, ErrIncomplete)
		}
	}

	stamp, ok := r.lastChunkAt(testConnA, testAttachmentID)
	if !ok {
		t.Fatalf("lastChunkAt on %q reported absent after the reap took %q's silent upload, want present", testConnA, testConnB)
	}
	if want := testClockStart.Add(2 * (uploadIdleTimeout - time.Minute)); !stamp.Equal(want) {
		t.Errorf("lastChunkAt on %q = %v, want its last delivered chunk's reading %v", testConnA, stamp, want)
	}
	if _, ok := r.Lookup(testConnB, testAttachmentID); ok {
		t.Errorf("%q's upload was silent past the window and is still held", testConnB)
	}
	if got := r.count(); got != 1 {
		t.Errorf("count() after the reap = %d, want 1 — %q's upload alone", got, testConnA)
	}
}

// The four tests below admit their pairs directly rather than through
// fillRegistry, which fills testConnA alone and names its ids itself. Each of
// them either needs a SECOND conn or needs to look its own ids up afterwards, and
// copying that helper's id scheme to the call site would be a citation of an
// unexported naming detail rather than a fixture. The declaration is
// fillRegistry's own — (total_chunks 1, size 0), the cheapest CONFORMING pair —
// so the capacity gate, which is the LATER of three gates, is the only one any of
// them can reach.

// TestRegistry_ReleaseConn_RemovesEveryUploadTheConnHolds is AC 1. THREE entries
// and not two: on a two-entry map a body that removes the first and the last is
// indistinguishable from one that removes them all, so two would go green under a
// sweep that stops early.
func TestRegistry_ReleaseConn_RemovesEveryUploadTheConnHolds(t *testing.T) {
	t.Parallel()

	held := []string{testAttachmentID, "att-2", "att-3"}

	r := NewRegistry()
	for _, attachmentID := range held {
		if _, err := r.Admit(testConnA, attachmentID, 1, 0, testFixtureDigest); err != nil {
			t.Fatalf("Admit of %q on %q: %v", attachmentID, testConnA, err)
		}
	}

	r.ReleaseConn(testConnA)

	if n := r.count(); n != 0 {
		t.Errorf("count() after releasing the conn holding all %d = %d, want 0", len(held), n)
	}
	for _, attachmentID := range held {
		if _, ok := r.Lookup(testConnA, attachmentID); ok {
			t.Errorf("Lookup(%q, %q) after ReleaseConn reported the upload present", testConnA, attachmentID)
		}
	}
}

// TestRegistry_ReleaseConn_LeavesAnotherConnsUploadsInFlight is AC 2. Both conns
// hold the SAME attachment_id plus one of their own, so the survivor claim is
// about uploadKey's own shape and not merely about two unrelated entries — that
// id is documented as guessable, so two conns mid-transfer on it at once is
// routine.
//
// The pointer-identity assertion is the load-bearing one, the
// TestRegistry_SameAttachmentIDOnTwoConns_AreSeparateEntries idiom: presence
// alone would still hold if the sweep had removed B's entry and left A's, since
// SOMETHING sits at the key either way.
func TestRegistry_ReleaseConn_LeavesAnotherConnsUploadsInFlight(t *testing.T) {
	t.Parallel()

	const otherAttachmentID = "att-2"
	both := []string{testAttachmentID, otherAttachmentID}

	r := NewRegistry()
	for _, connID := range []string{testConnA, testConnB} {
		for _, attachmentID := range both {
			if _, err := r.Admit(connID, attachmentID, 1, 0, testFixtureDigest); err != nil {
				t.Fatalf("Admit of %q on %q: %v", attachmentID, connID, err)
			}
		}
	}
	survivor, ok := r.Lookup(testConnB, testAttachmentID)
	if !ok {
		t.Fatalf("Lookup(%q, %q) reported absent before the release, want present", testConnB, testAttachmentID)
	}

	r.ReleaseConn(testConnA)

	for _, attachmentID := range both {
		if _, ok := r.Lookup(testConnA, attachmentID); ok {
			t.Errorf("Lookup(%q, %q) after releasing that conn reported the upload present", testConnA, attachmentID)
		}
		if _, ok := r.Lookup(testConnB, attachmentID); !ok {
			t.Errorf("releasing %q removed %q's upload of %q", testConnA, testConnB, attachmentID)
		}
	}
	if n := r.count(); n != len(both) {
		t.Errorf("count() after releasing one of two conns = %d, want %d", n, len(both))
	}
	if got, _ := r.Lookup(testConnB, testAttachmentID); got != survivor {
		t.Errorf("Lookup(%q, %q) answered an accumulator other than the one that conn was admitted with", testConnB, testAttachmentID)
	}
}

// TestRegistry_ReleaseConnHoldingNothing_ChangesNothingAndDoesNotReap is AC 3,
// and it carries the no-reap decision in the same body because a reap is exactly
// what "changes nothing" would be violated by. Both held entries are idle PAST
// uploadIdleTimeout when the release runs, and nothing has reaped them: count and
// Lookup do not reap either, which is what makes them honest observers here.
//
// It is the sole red for a ReleaseConn that runs reapExpiredLocked at the head of
// its body. Every other test of this method holds its clock still, so this is the
// only one that can see the difference.
func TestRegistry_ReleaseConnHoldingNothing_ChangesNothingAndDoesNotReap(t *testing.T) {
	t.Parallel()

	held := []string{testAttachmentID, "att-2"}

	clk := &fakeClock{t: testClockStart}
	r := newRegistryWithClock(clk.now)
	for _, attachmentID := range held {
		if _, err := r.Admit(testConnA, attachmentID, 1, 0, testFixtureDigest); err != nil {
			t.Fatalf("Admit of %q on %q: %v", attachmentID, testConnA, err)
		}
	}

	clk.advance(uploadIdleTimeout + time.Second)
	r.ReleaseConn(testConnB)

	if n := r.count(); n != len(held) {
		t.Errorf("count() after releasing a conn holding nothing = %d, want %d", n, len(held))
	}
	for _, attachmentID := range held {
		if _, ok := r.Lookup(testConnA, attachmentID); !ok {
			t.Errorf("Lookup(%q, %q) after releasing %q reported the upload absent", testConnA, attachmentID, testConnB)
		}
	}
}

// TestRegistry_ReleaseConn_ReturnsEverySlotItHeld is AC 4 —
// TestRegistry_AdmitAtTheBound_ReleaseReturnsTheSlot widened from a pair to a
// conn — and its two halves are the AC's two halves. That the slots free is the
// first; that THE RELEASE is what freed them is the second, and the fixture's
// clock is NEVER ADVANCED, so no reap can fire anywhere in this test and nothing
// else is left that could have.
//
// "That many new pairs" is an EXACT count, so the refusal after the second
// admission is as load-bearing as the two successes: without it a sweep that
// emptied the whole map would satisfy every other assertion here.
func TestRegistry_ReleaseConn_ReturnsEverySlotItHeld(t *testing.T) {
	t.Parallel()

	const perConn = maxInFlightUploads / 2
	// The fixture's own arithmetic, asserted rather than trusted: at an odd bound
	// the two conns would not fill it and every assertion below would be measuring
	// a registry that never reached capacity.
	if perConn*2 != maxInFlightUploads {
		t.Fatalf("this fixture splits maxInFlightUploads (%d) across two conns and needs it even", maxInFlightUploads)
	}

	clk := &fakeClock{t: testClockStart}
	r := newRegistryWithClock(clk.now)
	ids := make([]string, perConn)
	for i := range ids {
		ids[i] = fmt.Sprintf("att-%d", i)
	}
	for _, connID := range []string{testConnA, testConnB} {
		for _, attachmentID := range ids {
			if _, err := r.Admit(connID, attachmentID, 1, 0, testFixtureDigest); err != nil {
				t.Fatalf("Admit of %q on %q: %v", attachmentID, connID, err)
			}
		}
	}
	if _, err := r.Admit(testConnA, "att-next", 1, 0, testFixtureDigest); !errors.Is(err, ErrTooManyUploads) {
		t.Fatalf("Admit of a new pair at the bound: error = %v, want one wrapping %v", err, ErrTooManyUploads)
	}

	r.ReleaseConn(testConnA)

	if n := r.count(); n != perConn {
		t.Fatalf("count() after releasing one of the two conns at the bound = %d, want %d", n, perConn)
	}
	for i := 0; i < perConn; i++ {
		attachmentID := fmt.Sprintf("fresh-%d", i)
		if _, err := r.Admit(testConnA, attachmentID, 1, 0, testFixtureDigest); err != nil {
			t.Fatalf("Admit of %q into a slot the release freed: %v", attachmentID, err)
		}
	}
	if _, err := r.Admit(testConnA, "fresh-past-the-bound", 1, 0, testFixtureDigest); !errors.Is(err, ErrTooManyUploads) {
		t.Errorf("Admit of one pair more than the release freed: error = %v, want one wrapping %v", err, ErrTooManyUploads)
	}

	// The other conn's transfers were never the ones being reclaimed.
	for _, attachmentID := range ids {
		if _, ok := r.Lookup(testConnB, attachmentID); !ok {
			t.Errorf("Lookup(%q, %q) after %q's slots were refilled reported the upload absent", testConnB, attachmentID, testConnA)
		}
	}
}

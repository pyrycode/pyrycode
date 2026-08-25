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
	done := make(chan struct{}, workers+readers+1)

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

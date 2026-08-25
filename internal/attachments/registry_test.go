package attachments

import (
	"bytes"
	"fmt"
	"testing"
)

// The two conns and the one attachment_id every test here keys on. The same id
// on two conns is the whole point of uploadKey, so it is shared deliberately.
const (
	testConnA        = "conn-a"
	testConnB        = "conn-b"
	testAttachmentID = "att-1"
)

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

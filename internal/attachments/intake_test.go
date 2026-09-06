package attachments

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// testFilename is the client-supplied name every chunk in this file carries. It
// is chosen so SanitizeFilename returns it UNCHANGED — no separator, no leading
// dot — which is what lets one needle catch a leak of either the raw or the
// sanitised form in assertNoBannedStrings. A name the sanitiser rewrote would
// need two needles and would still miss whichever form the leak used.
const testFilename = "holiday-in-tromso.png"

// The attachment ids these tests key on. aid1 and aid2 come from storage_test.go
// and are canonical under conversations.ValidID, which is required of any id
// that reaches EnsureDir. The four below are deliberately NOT canonical: they
// key transfers that never complete, and using non-canonical ones there states
// that admission does not check the shape — the check #1897 owns and this slice
// deliberately does not add.
const (
	heldA = "held-a"
	heldB = "held-b"
	heldC = "held-c"
	heldD = "held-d"
)

// newTestIntake returns an Intake over a fresh instance directory alongside that
// directory's EvalSymlinks-resolved form, which is what every want path here is
// built from — resolvedInstanceDir's own doc records why the raw t.TempDir()
// value cannot be.
//
// It takes no conversation: since #2143 the destination is a Receive argument, so
// there is nothing about it to fix at construction.
func newTestIntake(t *testing.T) (*Intake, string) {
	t.Helper()

	instanceDir, root := resolvedInstanceDir(t)
	return NewIntake(instanceDir), root
}

// uploadChunk builds one chunk of a transfer of testFixture: the whole
// declaration every chunk repeats, plus the client strings the never-leak
// assertion needs. Unlike testChunk it fills AttachmentID, Size and SHA256,
// because Admit reads all three off the chunk on this path.
func uploadChunk(attachmentID string, index, totalChunks int, data []byte) protocol.AttachmentChunkPayload {
	return protocol.AttachmentChunkPayload{
		AttachmentID: attachmentID,
		Index:        index,
		TotalChunks:  totalChunks,
		Filename:     testFilename,
		MimeType:     "application/octet-stream",
		Size:         int64(len(testFixture)),
		SHA256:       testFixtureDigest,
		Data:         data,
	}
}

// completeChunk is the single-chunk transfer of testFixture: one Receive of it
// admits, delivers, assembles and stores. It is the only fixture here that
// reaches the filesystem, so its attachment id must be canonical.
func completeChunk(attachmentID string) protocol.AttachmentChunkPayload {
	return uploadChunk(attachmentID, 0, 1, testFixture)
}

// boundUploadChunk is chunk i of the multi-chunk bound transfer, carrying its
// declaration. Multi-chunk is only reachable through the bound fixture:
// CheckDeclaration requires total_chunks == max(1, ceil(size / 45000)), so a
// two-chunk declaration of a 24-byte file is refused at admission and cannot be
// used to test routing.
func boundUploadChunk(attachmentID string, i int) protocol.AttachmentChunkPayload {
	chunk := boundChunk(i)
	chunk.AttachmentID = attachmentID
	chunk.Filename = testFilename
	chunk.MimeType = "application/octet-stream"
	chunk.Size = maxUploadBytes
	chunk.SHA256 = testBoundFixtureDigest
	return chunk
}

// assertAccepted asserts the three-way answer's ACCEPTED shape: no refusal, and
// no stored attachment. It is the assertion AC 2 turns on — a build that handed
// Deliver's ErrIncomplete back as an error fails it without any test having to
// name that sentinel.
func assertAccepted(t *testing.T, id string, stored bool, err error) {
	t.Helper()

	if err != nil {
		t.Fatalf("Receive() error = %v, want nil: an accepted chunk is not a refusal", err)
	}
	if stored || id != "" {
		t.Errorf("Receive() = (%q, %t), want (\"\", false)", id, stored)
	}
}

// assertNoBannedStrings asserts a refusal carries none of the three strings
// protocol.AttachmentChunkPayload's SECURITY block bans from a message. It
// FATALS on an empty needle first: a substring assertion against "" is true of
// every string, so a fixture that forgot to set one of the three would report
// coverage it does not have.
func assertNoBannedStrings(t *testing.T, err error, chunk protocol.AttachmentChunkPayload) {
	t.Helper()

	banned := []struct{ what, needle string }{
		{"filename", chunk.Filename},
		{"declared digest", chunk.SHA256},
		{"chunk data", string(chunk.Data)},
	}
	msg := err.Error()
	for _, b := range banned {
		if b.needle == "" {
			t.Fatalf("the %s needle is empty, which makes this assertion vacuous", b.what)
		}
		if strings.Contains(msg, b.needle) {
			t.Errorf("refusal names the %s (%d bytes): %q", b.what, len(b.needle), msg)
		}
	}
}

// TestIntake_FirstChunkOfAnyIndex_GoesThroughAdmission is AC 1's first half. The
// opening chunk carries index 5, which docs/protocol-mobile.md § Attachments
// permits — chunks may arrive in ANY order, deliberately weaker than
// debug_bundle_chunk's strict seq — so a fork gated on index == 0 refuses a
// conforming client. The registry holding exactly one entry after BOTH chunks is
// the other half: the second one routed to the transfer already in flight rather
// than opening a second under the live pair.
func TestIntake_FirstChunkOfAnyIndex_GoesThroughAdmission(t *testing.T) {
	t.Parallel()

	intake, root := newTestIntake(t)

	id, stored, err := intake.Receive(testConnA, string(convA), boundUploadChunk(aid1, 5))
	assertAccepted(t, id, stored, err)
	if got := intake.reg.count(); got != 1 {
		t.Fatalf("registry holds %d entries after the first chunk, want 1", got)
	}

	id, stored, err = intake.Receive(testConnA, string(convA), boundUploadChunk(aid1, 0))
	assertAccepted(t, id, stored, err)
	if got := intake.reg.count(); got != 1 {
		t.Errorf("registry holds %d entries after the second chunk, want 1: the pair was admitted twice", got)
	}

	// Nothing is filed before a transfer completes, so the instance directory is
	// still as resolvedInstanceDir left it.
	assertEmptyDir(t, root)
}

// TestIntake_CompletingChunk_StoresUnderTheConversationTheCallerNamed is #2143's
// core claim. ONE intake takes the IDENTICAL chunk twice, naming a different
// conversation each time, and each upload lands in that conversation's own
// directory.
//
// One intake rather than two is what makes it discriminating. The shape it
// replaces built a second intake per row around a second construction-time
// resolver, which could not tell a per-transfer destination from a per-daemon
// one; this one reddens against a build that captured the first conversation it
// saw, or that read a destination from anywhere but the argument.
func TestIntake_CompletingChunk_StoresUnderTheConversationTheCallerNamed(t *testing.T) {
	t.Parallel()

	intake, root := newTestIntake(t)

	for _, conv := range []conversations.ConversationID{convA, convB} {
		id, stored, err := intake.Receive(testConnA, string(conv), completeChunk(aid1))
		if err != nil {
			t.Fatalf("Receive(%q) error = %v, want nil", conv, err)
		}
		if !stored || id != aid1 {
			t.Errorf("Receive(%q) = (%q, %t), want (%q, true)", conv, id, stored, aid1)
		}

		dir := wantDir(root, conv, aid1)
		assertDirEntries(t, dir, testFilename)
		got, err := os.ReadFile(filepath.Join(dir, testFilename))
		if err != nil {
			t.Fatalf("read stored attachment under %q: %v", conv, err)
		}
		if !bytes.Equal(got, testFixture) {
			t.Errorf("stored bytes under %q = %q, want %q", conv, got, testFixture)
		}

		// The completing chunk gives the slot back, so a stored upload holds no
		// capacity — and the next iteration admits a fresh transfer under the
		// same pair rather than reaching a latched accumulator.
		if n := intake.reg.count(); n != 0 {
			t.Fatalf("registry holds %d entries after completing under %q, want 0", n, conv)
		}
	}
}

// TestIntake_Refusals_ComeBackAsTheirOwnSentinel is AC 1's second half and AC 5
// at once: every layer this seam sequences answers ITS OWN sentinel, reachable
// by errors.Is through a path that wraps nothing, and no refusal names the
// filename, the declared digest or the bytes. Rolling the never-leak assertion
// into every row rather than testing it once is what makes it a claim over the
// whole reject surface instead of over a sample of it.
func TestIntake_Refusals_ComeBackAsTheirOwnSentinel(t *testing.T) {
	t.Parallel()

	// A valid-shaped digest that is not testFixture's, so the length comparison
	// passes and the digest comparison is what refuses.
	wrongDigest := testBoundFixtureDigest

	inadmissible := completeChunk(aid1)
	inadmissible.TotalChunks = 2 // max(1, ceil(24 / 45000)) is 1

	oversized := completeChunk(aid1)
	oversized.Size = maxUploadBytes + 1
	oversized.TotalChunks = testBoundTotal // still the honest count for that size

	integrity := completeChunk(aid1)
	integrity.SHA256 = wrongDigest

	tests := []struct {
		name string
		// before is Received first and every chunk in it must be ACCEPTED, so a
		// row that accidentally refuses early fails loudly rather than passing
		// for the wrong reason.
		before []protocol.AttachmentChunkPayload
		chunk  protocol.AttachmentChunkPayload
		// conversation is the destination the caller names, spelled on EVERY row
		// rather than defaulted, because two rows below turn on it being a
		// hostile value and a zero value would then read as an omission.
		conversation string
		want         error
	}{
		{
			name:         "declaration is inadmissible",
			chunk:        inadmissible,
			conversation: string(convA),
			want:         ErrInvalidDeclaration,
		},
		{
			name:         "declared size is over the per-upload bound",
			chunk:        oversized,
			conversation: string(convA),
			want:         ErrUploadTooLarge,
		},
		{
			name: "too many uploads in flight",
			before: []protocol.AttachmentChunkPayload{
				boundUploadChunk(heldA, 0),
				boundUploadChunk(heldB, 0),
				boundUploadChunk(heldC, 0),
				boundUploadChunk(heldD, 0),
			},
			chunk:        completeChunk(aid1),
			conversation: string(convA),
			want:         ErrTooManyUploads,
		},
		{
			name:         "framing refusal on a later chunk",
			before:       []protocol.AttachmentChunkPayload{boundUploadChunk(aid1, 0)},
			chunk:        boundUploadChunk(aid1, 0),
			conversation: string(convA),
			want:         ErrDuplicateIndex,
		},
		{
			name:         "integrity refusal on the completing chunk",
			chunk:        integrity,
			conversation: string(convA),
			want:         ErrDigestMismatch,
		},
		{
			// Nothing checks the id's shape at admission, so a non-canonical one
			// is accepted, transmitted whole, and refused only when the
			// completing chunk reaches EnsureDir. #1897 owns the earlier check.
			name:         "attachment id is not of canonical shape",
			chunk:        completeChunk(heldA),
			conversation: string(convA),
			want:         ErrInvalidID,
		},
		{
			// #2143's backstop, at the layer where the value became
			// client-authored. The caller is contracted to have run
			// KnownConversation, and EnsureDir's conversations.ValidID check is
			// what stands behind a caller that did not: the empty id is refused
			// before the filesystem is touched.
			name:         "conversation id is empty",
			chunk:        completeChunk(aid1),
			conversation: "",
			want:         ErrInvalidID,
		},
		{
			// The same backstop against the shape that would matter: a
			// traversal-bearing conversation id never reaches a path join,
			// because EnsureDir validates BOTH components before it resolves
			// anything. A build that relaxed that check to a non-empty test
			// reddens here.
			name:         "conversation id is traversal-shaped",
			chunk:        completeChunk(aid1),
			conversation: "../../../../etc",
			want:         ErrInvalidID,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			intake, root := newTestIntake(t)
			for i, chunk := range tt.before {
				id, stored, err := intake.Receive(testConnA, string(convA), chunk)
				if err != nil {
					t.Fatalf("setup chunk %d: Receive() error = %v, want nil", i, err)
				}
				if stored || id != "" {
					t.Fatalf("setup chunk %d: Receive() = (%q, %t), want an accepted chunk", i, id, stored)
				}
			}

			id, stored, err := intake.Receive(testConnA, tt.conversation, tt.chunk)
			if !errors.Is(err, tt.want) {
				t.Fatalf("Receive() error = %v, want errors.Is(err, %v)", err, tt.want)
			}
			if stored || id != "" {
				t.Errorf("Receive() = (%q, %t) on a refusal, want (\"\", false)", id, stored)
			}
			assertNoBannedStrings(t, err, tt.chunk)
			assertEmptyDir(t, root)
		})
	}
}

// TestIntake_CompletedTransfer_IsFullyReleased pins the sequencing the package
// overview requires: Deliver is called with the SAME chunk just passed to Admit,
// looking the pair back up, and Admit's returned accumulator is never fed. A
// build that fed it instead bypasses every release Deliver owns, so the entry
// survives its own completion and the repeat below reaches a latched
// accumulator that answers ErrDuplicateIndex for index 0.
func TestIntake_CompletedTransfer_IsFullyReleased(t *testing.T) {
	t.Parallel()

	intake, root := newTestIntake(t)

	if _, stored, err := intake.Receive(testConnA, string(convA), completeChunk(aid1)); err != nil || !stored {
		t.Fatalf("first Receive() = (_, %t, %v), want (_, true, nil)", stored, err)
	}
	if n := intake.reg.count(); n != 0 {
		t.Fatalf("registry holds %d entries after a completed upload, want 0", n)
	}

	// The same pair, re-uploaded the way a phone that lost its reply would: a
	// fresh transfer, admitted again, stored again.
	id, stored, err := intake.Receive(testConnA, string(convA), completeChunk(aid1))
	if err != nil {
		t.Fatalf("re-upload: Receive() error = %v, want nil", err)
	}
	if !stored || id != aid1 {
		t.Errorf("re-upload: Receive() = (%q, %t), want (%q, true)", id, stored, aid1)
	}
	assertDirEntries(t, wantDir(root, convA, aid1), testFilename)
}

// TestIntake_ReleaseConn_ReturnsTheCapacityTheConnHeld is AC 4. A dropped phone
// holds no capacity: the fifth pair is refused while the conn holds four, and
// admitted once the conn is gone.
func TestIntake_ReleaseConn_ReturnsTheCapacityTheConnHeld(t *testing.T) {
	t.Parallel()

	intake, _ := newTestIntake(t)

	for _, id := range []string{heldA, heldB, heldC, heldD} {
		if _, _, err := intake.Receive(testConnA, string(convA), boundUploadChunk(id, 0)); err != nil {
			t.Fatalf("Receive(%q) error = %v, want nil", id, err)
		}
	}
	if _, _, err := intake.Receive(testConnA, string(convA), completeChunk(aid1)); !errors.Is(err, ErrTooManyUploads) {
		t.Fatalf("Receive() at the bound error = %v, want errors.Is(err, ErrTooManyUploads)", err)
	}

	intake.ReleaseConn(testConnA)
	if n := intake.reg.count(); n != 0 {
		t.Fatalf("registry holds %d entries after ReleaseConn, want 0", n)
	}
	if _, _, err := intake.Receive(testConnA, string(convA), boundUploadChunk(aid2, 0)); err != nil {
		t.Errorf("Receive() after ReleaseConn error = %v, want nil: the capacity did not come back", err)
	}
}

// TestIntake_ReleaseConn_LeavesAnotherConnsUploadsInFlight is the other half of
// AC 4: the release is keyed by conn, so a phone dropping does not disturb
// another one's transfers. Without it, ReleaseConn clearing the whole registry
// would pass the test above.
func TestIntake_ReleaseConn_LeavesAnotherConnsUploadsInFlight(t *testing.T) {
	t.Parallel()

	intake, _ := newTestIntake(t)

	if _, _, err := intake.Receive(testConnA, string(convA), boundUploadChunk(heldA, 0)); err != nil {
		t.Fatalf("Receive(conn A) error = %v, want nil", err)
	}
	if _, _, err := intake.Receive(testConnB, string(convA), boundUploadChunk(heldB, 0)); err != nil {
		t.Fatalf("Receive(conn B) error = %v, want nil", err)
	}

	intake.ReleaseConn(testConnA)
	if n := intake.reg.count(); n != 1 {
		t.Fatalf("registry holds %d entries after releasing conn A, want 1", n)
	}
	// Conn B's transfer is still in flight, which only the routing half can
	// answer: a second chunk at index 0 reaches the live accumulator.
	if _, _, err := intake.Receive(testConnB, string(convA), boundUploadChunk(heldB, 0)); !errors.Is(err, ErrDuplicateIndex) {
		t.Errorf("conn B second chunk error = %v, want errors.Is(err, ErrDuplicateIndex)", err)
	}
}

// TestIntake_ReceiveMismatchedConversation_RefusesAndStoresNothing is #2146 at
// the composite entry point, driven exactly as the wire drives it: the caller
// names the destination per chunk — handleAttachmentChunk gates it on every
// frame — so a phone that switches conversations mid-upload reaches Receive with
// a DIFFERENT argument on a later chunk of a transfer already in flight.
//
// It is the seam-level half of AC 2's "stores no bytes". The refusal must arrive
// before EnsureDir and Store rather than merely instead of them, so the
// assertion is over the instance directory as a whole: NEITHER conversation has
// a directory, not just the one the switch named. A refusal that ran after
// EnsureDir would leave conversation B's directory behind holding no file, which
// reads as success to anything that lists directories.
//
// The sentinel travels out VERBATIM — Receive wraps, annotates and reinterprets
// nothing and interprets only ErrIncomplete — so errors.Is reaches it here
// exactly as it does at the registry.
func TestIntake_ReceiveMismatchedConversation_RefusesAndStoresNothing(t *testing.T) {
	t.Parallel()

	intake, root := newTestIntake(t)

	// The admitting chunk fixes the destination on the transfer.
	if _, stored, err := intake.Receive(testConnA, string(convA), boundUploadChunk(aid1, 0)); err != nil || stored {
		t.Fatalf("Receive of the admitting chunk = (_, %t, %v), want (_, false, nil)", stored, err)
	}

	id, stored, err := intake.Receive(testConnA, string(convB), boundUploadChunk(aid1, 1))
	if !errors.Is(err, ErrConversationMismatch) {
		t.Fatalf("Receive of a later chunk under %q: error = %v, want errors.Is(err, %v)", convB, err, ErrConversationMismatch)
	}
	if id != "" || stored {
		t.Errorf("Receive() = (%q, %t) on a refusal, want (\"\", false)", id, stored)
	}

	for _, conv := range []conversations.ConversationID{convA, convB} {
		if _, err := os.Stat(filepath.Join(root, string(conv))); !os.IsNotExist(err) {
			t.Errorf("os.Stat of the %q directory after a mismatch refusal: err = %v, want it never created", conv, err)
		}
	}

	// The transfer is still in flight — nothing dropped — so the switch cost the
	// client nothing but the one refused chunk.
	if n := intake.reg.count(); n != 1 {
		t.Errorf("registry holds %d entries after a mismatch refusal, want the incumbent's 1", n)
	}
}

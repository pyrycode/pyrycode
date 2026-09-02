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

// fixedConversation is the resolver a daemon sitting on one conversation would
// supply. #1897 builds the production one from activeConversation.
func fixedConversation(id conversations.ConversationID) func() (conversations.ConversationID, bool) {
	return func() (conversations.ConversationID, bool) { return id, true }
}

// newTestIntake returns an Intake over a fresh instance directory alongside that
// directory's EvalSymlinks-resolved form, which is what every want path here is
// built from — resolvedInstanceDir's own doc records why the raw t.TempDir()
// value cannot be.
func newTestIntake(t *testing.T, conversation func() (conversations.ConversationID, bool)) (*Intake, string) {
	t.Helper()

	instanceDir, root := resolvedInstanceDir(t)
	return NewIntake(instanceDir, conversation), root
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

	intake, root := newTestIntake(t, fixedConversation(convA))

	id, stored, err := intake.Receive(testConnA, boundUploadChunk(aid1, 5))
	assertAccepted(t, id, stored, err)
	if got := intake.reg.count(); got != 1 {
		t.Fatalf("registry holds %d entries after the first chunk, want 1", got)
	}

	id, stored, err = intake.Receive(testConnA, boundUploadChunk(aid1, 0))
	assertAccepted(t, id, stored, err)
	if got := intake.reg.count(); got != 1 {
		t.Errorf("registry holds %d entries after the second chunk, want 1: the pair was admitted twice", got)
	}

	// Nothing is filed before a transfer completes, so the instance directory is
	// still as resolvedInstanceDir left it.
	assertEmptyDir(t, root)
}

// TestIntake_CompletingChunk_StoresUnderTheResolvedConversation is AC 3. The two
// rows drive the IDENTICAL chunk through two intakes whose resolvers answer
// different conversations: the destination follows the daemon's resolver, and
// there is no field on the frame it could have followed instead. One row alone
// would pass under a build that hardcoded a conversation.
func TestIntake_CompletingChunk_StoresUnderTheResolvedConversation(t *testing.T) {
	t.Parallel()

	for _, conv := range []conversations.ConversationID{convA, convB} {
		t.Run(string(conv), func(t *testing.T) {
			t.Parallel()

			intake, root := newTestIntake(t, fixedConversation(conv))

			id, stored, err := intake.Receive(testConnA, completeChunk(aid1))
			if err != nil {
				t.Fatalf("Receive() error = %v, want nil", err)
			}
			if !stored || id != aid1 {
				t.Errorf("Receive() = (%q, %t), want (%q, true)", id, stored, aid1)
			}

			dir := wantDir(root, conv, aid1)
			assertDirEntries(t, dir, testFilename)
			got, err := os.ReadFile(filepath.Join(dir, testFilename))
			if err != nil {
				t.Fatalf("read stored attachment: %v", err)
			}
			if !bytes.Equal(got, testFixture) {
				t.Errorf("stored bytes = %q, want %q", got, testFixture)
			}

			// The completing chunk gives the slot back, so a stored upload holds
			// no capacity.
			if n := intake.reg.count(); n != 0 {
				t.Errorf("registry holds %d entries after a completed upload, want 0", n)
			}
		})
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
		want   error
	}{
		{
			name:  "declaration is inadmissible",
			chunk: inadmissible,
			want:  ErrInvalidDeclaration,
		},
		{
			name:  "declared size is over the per-upload bound",
			chunk: oversized,
			want:  ErrUploadTooLarge,
		},
		{
			name: "too many uploads in flight",
			before: []protocol.AttachmentChunkPayload{
				boundUploadChunk(heldA, 0),
				boundUploadChunk(heldB, 0),
				boundUploadChunk(heldC, 0),
				boundUploadChunk(heldD, 0),
			},
			chunk: completeChunk(aid1),
			want:  ErrTooManyUploads,
		},
		{
			name:   "framing refusal on a later chunk",
			before: []protocol.AttachmentChunkPayload{boundUploadChunk(aid1, 0)},
			chunk:  boundUploadChunk(aid1, 0),
			want:   ErrDuplicateIndex,
		},
		{
			name:  "integrity refusal on the completing chunk",
			chunk: integrity,
			want:  ErrDigestMismatch,
		},
		{
			// Nothing checks the id's shape at admission, so a non-canonical one
			// is accepted, transmitted whole, and refused only when the
			// completing chunk reaches EnsureDir. #1897 owns the earlier check.
			name:  "attachment id is not of canonical shape",
			chunk: completeChunk(heldA),
			want:  ErrInvalidID,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			intake, root := newTestIntake(t, fixedConversation(convA))
			for i, chunk := range tt.before {
				id, stored, err := intake.Receive(testConnA, chunk)
				if err != nil {
					t.Fatalf("setup chunk %d: Receive() error = %v, want nil", i, err)
				}
				if stored || id != "" {
					t.Fatalf("setup chunk %d: Receive() = (%q, %t), want an accepted chunk", i, id, stored)
				}
			}

			id, stored, err := intake.Receive(testConnA, tt.chunk)
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

	intake, root := newTestIntake(t, fixedConversation(convA))

	if _, stored, err := intake.Receive(testConnA, completeChunk(aid1)); err != nil || !stored {
		t.Fatalf("first Receive() = (_, %t, %v), want (_, true, nil)", stored, err)
	}
	if n := intake.reg.count(); n != 0 {
		t.Fatalf("registry holds %d entries after a completed upload, want 0", n)
	}

	// The same pair, re-uploaded the way a phone that lost its reply would: a
	// fresh transfer, admitted again, stored again.
	id, stored, err := intake.Receive(testConnA, completeChunk(aid1))
	if err != nil {
		t.Fatalf("re-upload: Receive() error = %v, want nil", err)
	}
	if !stored || id != aid1 {
		t.Errorf("re-upload: Receive() = (%q, %t), want (%q, true)", id, stored, aid1)
	}
	assertDirEntries(t, wantDir(root, convA, aid1), testFilename)
}

// TestIntake_NoConversation_RefusesTheCompletingChunk is AC 3's second half. All
// three ways the daemon can be on no conversation answer ErrNoConversation and
// NOT ErrInvalidID: handing "" to EnsureDir would report a daemon that has
// routed nowhere as a malformed identifier, which is the mutant the second
// assertion exists to catch.
func TestIntake_NoConversation_RefusesTheCompletingChunk(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		conversation func() (conversations.ConversationID, bool)
	}{
		{"resolver answers false", func() (conversations.ConversationID, bool) { return convA, false }},
		{"resolver answers the empty id", func() (conversations.ConversationID, bool) { return "", true }},
		{"no resolver was wired", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			intake, root := newTestIntake(t, tt.conversation)

			chunk := completeChunk(aid1)
			id, stored, err := intake.Receive(testConnA, chunk)
			if !errors.Is(err, ErrNoConversation) {
				t.Fatalf("Receive() error = %v, want errors.Is(err, ErrNoConversation)", err)
			}
			if errors.Is(err, ErrInvalidID) {
				t.Errorf("Receive() error = %v, want no ErrInvalidID: an unrouted daemon is not a malformed id", err)
			}
			if stored || id != "" {
				t.Errorf("Receive() = (%q, %t), want (\"\", false)", id, stored)
			}
			assertNoBannedStrings(t, err, chunk)

			// Refused rather than filed anywhere, and the slot came back with
			// the transfer that ended.
			assertEmptyDir(t, root)
			if n := intake.reg.count(); n != 0 {
				t.Errorf("registry holds %d entries, want 0", n)
			}
		})
	}
}

// TestIntake_ErrNoConversationIsDistinct asserts the new sentinel is
// distinguishable from every sentinel this package already publishes — a claim
// over the SET, asserted over packageSentinels rather than over a hand-picked
// near miss, the shape TestRegistry_DeliverUnheldPair_RefusesAndCreatesNothing
// established.
func TestIntake_ErrNoConversationIsDistinct(t *testing.T) {
	t.Parallel()

	for _, other := range packageSentinels {
		if other == ErrNoConversation {
			continue
		}
		if errors.Is(ErrNoConversation, other) || errors.Is(other, ErrNoConversation) {
			t.Errorf("ErrNoConversation and %v are not distinguishable", other)
		}
	}
}

// TestIntake_ReleaseConn_ReturnsTheCapacityTheConnHeld is AC 4. A dropped phone
// holds no capacity: the fifth pair is refused while the conn holds four, and
// admitted once the conn is gone.
func TestIntake_ReleaseConn_ReturnsTheCapacityTheConnHeld(t *testing.T) {
	t.Parallel()

	intake, _ := newTestIntake(t, fixedConversation(convA))

	for _, id := range []string{heldA, heldB, heldC, heldD} {
		if _, _, err := intake.Receive(testConnA, boundUploadChunk(id, 0)); err != nil {
			t.Fatalf("Receive(%q) error = %v, want nil", id, err)
		}
	}
	if _, _, err := intake.Receive(testConnA, completeChunk(aid1)); !errors.Is(err, ErrTooManyUploads) {
		t.Fatalf("Receive() at the bound error = %v, want errors.Is(err, ErrTooManyUploads)", err)
	}

	intake.ReleaseConn(testConnA)
	if n := intake.reg.count(); n != 0 {
		t.Fatalf("registry holds %d entries after ReleaseConn, want 0", n)
	}
	if _, _, err := intake.Receive(testConnA, boundUploadChunk(aid2, 0)); err != nil {
		t.Errorf("Receive() after ReleaseConn error = %v, want nil: the capacity did not come back", err)
	}
}

// TestIntake_ReleaseConn_LeavesAnotherConnsUploadsInFlight is the other half of
// AC 4: the release is keyed by conn, so a phone dropping does not disturb
// another one's transfers. Without it, ReleaseConn clearing the whole registry
// would pass the test above.
func TestIntake_ReleaseConn_LeavesAnotherConnsUploadsInFlight(t *testing.T) {
	t.Parallel()

	intake, _ := newTestIntake(t, fixedConversation(convA))

	if _, _, err := intake.Receive(testConnA, boundUploadChunk(heldA, 0)); err != nil {
		t.Fatalf("Receive(conn A) error = %v, want nil", err)
	}
	if _, _, err := intake.Receive(testConnB, boundUploadChunk(heldB, 0)); err != nil {
		t.Fatalf("Receive(conn B) error = %v, want nil", err)
	}

	intake.ReleaseConn(testConnA)
	if n := intake.reg.count(); n != 1 {
		t.Fatalf("registry holds %d entries after releasing conn A, want 1", n)
	}
	// Conn B's transfer is still in flight, which only the routing half can
	// answer: a second chunk at index 0 reaches the live accumulator.
	if _, _, err := intake.Receive(testConnB, boundUploadChunk(heldB, 0)); !errors.Is(err, ErrDuplicateIndex) {
		t.Errorf("conn B second chunk error = %v, want errors.Is(err, ErrDuplicateIndex)", err)
	}
}

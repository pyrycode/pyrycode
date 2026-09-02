package attachments

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// Canonical ids for both parameters. Every one satisfies conversations.ValidID,
// which is what makes the containment rows non-vacuous: once both ids must be
// canonical, no id can spell a traversal, so the last three tests here have to
// reach the containment check through on-disk symlinks rather than through a
// hostile string.
const (
	convA = "11111111-1111-4111-8111-111111111111"
	convB = "22222222-2222-4222-8222-222222222222"
	aid1  = "33333333-3333-4333-9333-333333333333"
	aid2  = "44444444-4444-4444-a444-444444444444"
)

// resolvedInstanceDir creates an instance directory and returns it alongside
// its EvalSymlinks-resolved form. Every want in this file is built from the
// SECOND return value.
//
// Not from the raw t.TempDir(): on macOS that is /var/folders/…, a symlink to
// /private/var/folders/…, so a want joined onto the raw string fails for a
// reason that has nothing to do with the code under test. And not from
// EnsureDir's own return value either — a want derived from the return passes
// under every mutant, including one that returns an unresolved path, which is
// the single thing the success assertion exists to catch.
func resolvedInstanceDir(t *testing.T) (instanceDir, root string) {
	t.Helper()

	instanceDir = filepath.Join(t.TempDir(), "instance")
	if err := os.MkdirAll(instanceDir, 0o700); err != nil {
		t.Fatalf("create instance dir: %v", err)
	}
	root, err := filepath.EvalSymlinks(instanceDir)
	if err != nil {
		t.Fatalf("resolve instance dir: %v", err)
	}
	return instanceDir, root
}

// wantDir composes the destination EnsureDir must answer, textually beneath the
// resolved root — the same construction the implementation performs, which is
// the point: the assertion is on the FULL path, so a build that drops the
// per-attachment component or anchors somewhere else fails it.
func wantDir(root string, conv conversations.ConversationID, attachmentID string) string {
	return filepath.Join(root, "conversations", string(conv), "attachments", attachmentID)
}

// assertEmptyDir asserts nothing was created beneath a fixture directory. The
// containment tests' "nothing is created" claim is about what lands under the
// pre-existing symlink, which necessarily predates the call.
func assertEmptyDir(t *testing.T, dir string) {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %q: %v", dir, err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("%q gained %v, want nothing created beneath it", dir, names)
	}
}

// assertDirEntries asserts that dir holds exactly the named entries and
// nothing else. Sibling to assertEmptyDir, and what turns "no temporary file
// was left behind" into an assertion: the .attachment-*.tmp shape Store uses is
// unreachable as a stored attachment's name, because SanitizeFilename never
// returns a component beginning with '.'.
func assertDirEntries(t *testing.T, dir string, want ...string) {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %q: %v", dir, err)
	}
	got := make([]string, 0, len(entries))
	for _, e := range entries {
		got = append(got, e.Name())
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("%q holds %v, want exactly %v", dir, got, want)
	}
}

// TestStore_WritesSanitisedComponent is AC 1. Every expected component is a
// string LITERAL rather than a call to SanitizeFilename: computing it would
// make the row pass under a build that dropped the sanitiser, since both sides
// would then move together.
func TestStore_WritesSanitisedComponent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		filename string
		want     string
		data     []byte
	}{
		// The ordinary case, which the sanitiser leaves untouched — and which
		// is therefore VACUOUS for a build that joined the raw client name.
		// The next row is what makes AC 1 non-vacuous.
		{"unchanged by the sanitiser", "report.pdf", "report.pdf", []byte("pdf bytes")},
		// Required by AC 1: separators become '_' and the leading '.' gains an
		// '_' prefix, so a build that joined the raw name writes two
		// directories up and this row's read-back fails.
		{"traversal in the client name", "../../etc/passwd", "_.._.._etc_passwd", []byte("not passwd")},
		// Nothing survives the allowlist, so the fallback answers. Pins that
		// Store does not special-case an "empty-looking" name.
		{"nothing survives the allowlist", "///", "attachment", []byte("still stored")},
		// An empty attachment is a legitimate file, not a refusal.
		{"zero-length attachment", "empty.bin", "empty.bin", []byte{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			instanceDir, root := resolvedInstanceDir(t)
			dir, err := EnsureDir(instanceDir, convA, aid1)
			if err != nil {
				t.Fatalf("EnsureDir() error = %v, want nil", err)
			}

			got, err := Store(dir, tt.filename, tt.data)
			if err != nil {
				t.Fatalf("Store() error = %v, want nil", err)
			}
			// Built from the RESOLVED root, never from t.TempDir() (a symlink
			// on macOS) and never from Store's own return value (which passes
			// under every mutant, including one writing the raw client name).
			want := filepath.Join(wantDir(root, convA, aid1), tt.want)
			if got != want {
				t.Errorf("Store() = %q, want %q", got, want)
			}

			body, err := os.ReadFile(want)
			if err != nil {
				t.Fatalf("read stored file: %v", err)
			}
			if !bytes.Equal(body, tt.data) {
				t.Errorf("stored bytes = %q, want %q", body, tt.data)
			}

			info, err := os.Stat(want)
			if err != nil {
				t.Fatalf("stat stored file: %v", err)
			}
			if perm := info.Mode().Perm(); perm != 0o600 {
				t.Errorf("Store() created %q with mode %#o, want %#o", want, perm, 0o600)
			}
			assertDirEntries(t, dir, tt.want)
		})
	}
}

// TestStore_SameFilenameTwoAttachments is AC 2, end to end through EnsureDir:
// the per-attachment-id directory component is the entire reason two identical
// client filenames can coexist in one conversation. The bytes DIFFER per
// attachment, which is what makes an overwrite visible — identical bytes would
// pass under a build where one clobbered the other.
func TestStore_SameFilenameTwoAttachments(t *testing.T) {
	t.Parallel()

	instanceDir, root := resolvedInstanceDir(t)

	firstDir, err := EnsureDir(instanceDir, convA, aid1)
	if err != nil {
		t.Fatalf("EnsureDir(aid1) error = %v, want nil", err)
	}
	secondDir, err := EnsureDir(instanceDir, convA, aid2)
	if err != nil {
		t.Fatalf("EnsureDir(aid2) error = %v, want nil", err)
	}

	firstBytes := []byte("first attachment")
	secondBytes := []byte("second attachment, different bytes")

	first, err := Store(firstDir, "report.pdf", firstBytes)
	if err != nil {
		t.Fatalf("Store(aid1) error = %v, want nil", err)
	}
	second, err := Store(secondDir, "report.pdf", secondBytes)
	if err != nil {
		t.Fatalf("Store(aid2) error = %v, want nil", err)
	}

	if first == second {
		t.Fatalf("Store() answered %q for both attachments, want two paths", first)
	}
	if want := filepath.Join(wantDir(root, convA, aid1), "report.pdf"); first != want {
		t.Errorf("Store(aid1) = %q, want %q", first, want)
	}
	if want := filepath.Join(wantDir(root, convA, aid2), "report.pdf"); second != want {
		t.Errorf("Store(aid2) = %q, want %q", second, want)
	}

	for _, tc := range []struct {
		path string
		want []byte
	}{{first, firstBytes}, {second, secondBytes}} {
		body, err := os.ReadFile(tc.path)
		if err != nil {
			t.Fatalf("read %q: %v", tc.path, err)
		}
		if !bytes.Equal(body, tc.want) {
			t.Errorf("%q holds %q, want %q", tc.path, body, tc.want)
		}
	}
}

// TestStore_Idempotent pins the no-O_EXCL decision: a phone that drops
// mid-upload and reconnects re-sends the same attachment id, and an exclusive
// create would turn that ordinary reconnect into a storage failure at #1744.
func TestStore_Idempotent(t *testing.T) {
	t.Parallel()

	instanceDir, root := resolvedInstanceDir(t)
	dir, err := EnsureDir(instanceDir, convA, aid1)
	if err != nil {
		t.Fatalf("EnsureDir() error = %v, want nil", err)
	}
	data := []byte("re-delivered upload")

	first, err := Store(dir, "report.pdf", data)
	if err != nil {
		t.Fatalf("first Store() error = %v, want nil", err)
	}
	second, err := Store(dir, "report.pdf", data)
	if err != nil {
		t.Fatalf("second Store() error = %v, want nil", err)
	}

	want := filepath.Join(wantDir(root, convA, aid1), "report.pdf")
	if first != want || second != want {
		t.Errorf("Store() = %q then %q, want %q both times", first, second, want)
	}
	body, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("read stored file: %v", err)
	}
	if !bytes.Equal(body, data) {
		t.Errorf("stored bytes = %q, want %q", body, data)
	}
	assertDirEntries(t, dir, "report.pdf")
}

// storeRenameFailure returns the directory Store was handed and the error it
// answered when the rename destination is already a DIRECTORY. Root-safe by
// construction, which a chmod 0o500 fixture is not: renaming a file onto an
// existing directory fails on both Linux and macOS regardless of privilege.
func storeRenameFailure(t *testing.T) (dir string, err error) {
	t.Helper()

	instanceDir, _ := resolvedInstanceDir(t)
	dir, err = EnsureDir(instanceDir, convA, aid1)
	if err != nil {
		t.Fatalf("EnsureDir() error = %v, want nil", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "report.pdf"), 0o700); err != nil {
		t.Fatalf("create blocking directory: %v", err)
	}
	got, err := Store(dir, "report.pdf", []byte("bytes that cannot land"))
	if got != "" {
		t.Errorf("Store() = %q, want no path on failure", got)
	}
	return dir, err
}

// TestStore_RenameFails is AC 4's first fixture, and the only place the
// no-leftover-temp property is pinned: the deferred os.Remove is what makes it
// hold on every failure path.
func TestStore_RenameFails(t *testing.T) {
	t.Parallel()

	dir, err := storeRenameFailure(t)
	if !errors.Is(err, ErrWriteFailed) {
		t.Errorf("Store() error = %v, want ErrWriteFailed", err)
	}
	assertDirEntries(t, dir, "report.pdf")
}

// TestStore_CreateTempFails is AC 4's second fixture, also root-safe. It
// asserts nothing about the directory's contents: the directory is gone, so
// reading it would fail for a reason unrelated to the code under test.
func TestStore_CreateTempFails(t *testing.T) {
	t.Parallel()

	instanceDir, _ := resolvedInstanceDir(t)
	dir, err := EnsureDir(instanceDir, convA, aid1)
	if err != nil {
		t.Fatalf("EnsureDir() error = %v, want nil", err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("remove attachment dir: %v", err)
	}

	got, err := Store(dir, "report.pdf", []byte("nowhere to go"))
	if !errors.Is(err, ErrWriteFailed) {
		t.Errorf("Store() error = %v, want ErrWriteFailed", err)
	}
	if got != "" {
		t.Errorf("Store() = %q, want no path on failure", got)
	}
}

// TestStore_SentinelIsDistinct is AC 4's errors.Is clause: ErrWriteFailed must
// be distinguishable from every sentinel already exported by this package, so
// #1744 can map it on its own.
func TestStore_SentinelIsDistinct(t *testing.T) {
	t.Parallel()

	_, err := storeRenameFailure(t)
	if !errors.Is(err, ErrWriteFailed) {
		t.Fatalf("Store() error = %v, want ErrWriteFailed", err)
	}

	others := []struct {
		name string
		err  error
	}{
		{"ErrTotalChunksMismatch", ErrTotalChunksMismatch},
		{"ErrIndexOutOfRange", ErrIndexOutOfRange},
		{"ErrDuplicateIndex", ErrDuplicateIndex},
		{"ErrSizeMismatch", ErrSizeMismatch},
		{"ErrDigestMismatch", ErrDigestMismatch},
		{"ErrIncomplete", ErrIncomplete},
		{"ErrInvalidDeclaration", ErrInvalidDeclaration},
		{"ErrUploadTooLarge", ErrUploadTooLarge},
		{"ErrInvalidID", ErrInvalidID},
		{"ErrNotContained", ErrNotContained},
	}
	for _, other := range others {
		if errors.Is(err, other.err) {
			t.Errorf("Store() error = %v, want it distinguishable from %s", err, other.name)
		}
	}
}

func TestEnsureDir_CreatesResolvedDirectory(t *testing.T) {
	t.Parallel()

	instanceDir, root := resolvedInstanceDir(t)

	got, err := EnsureDir(instanceDir, convA, aid1)
	if err != nil {
		t.Fatalf("EnsureDir() error = %v, want nil", err)
	}
	if want := wantDir(root, convA, aid1); got != want {
		t.Errorf("EnsureDir() = %q, want %q", got, want)
	}

	info, err := os.Stat(got)
	if err != nil {
		t.Fatalf("stat returned path: %v", err)
	}
	if !info.IsDir() {
		t.Errorf("EnsureDir() returned %q, want a directory", got)
	}
	// One MkdirAll creates every missing level with the same mode, so the leaf
	// covers the chain.
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("EnsureDir() created %q with mode %#o, want %#o", got, perm, 0o700)
	}
}

// TestEnsureDir_DistinctAttachmentIDs is the row that pins the per-attachment
// component: without it both ids in one conversation answer one directory, and
// #1782's two same-named files would collide.
func TestEnsureDir_DistinctAttachmentIDs(t *testing.T) {
	t.Parallel()

	instanceDir, root := resolvedInstanceDir(t)

	first, err := EnsureDir(instanceDir, convA, aid1)
	if err != nil {
		t.Fatalf("EnsureDir(aid1) error = %v, want nil", err)
	}
	second, err := EnsureDir(instanceDir, convA, aid2)
	if err != nil {
		t.Fatalf("EnsureDir(aid2) error = %v, want nil", err)
	}

	if first == second {
		t.Errorf("EnsureDir() answered %q for both attachment ids, want two directories", first)
	}
	if want := wantDir(root, convA, aid1); first != want {
		t.Errorf("EnsureDir(aid1) = %q, want %q", first, want)
	}
	if want := wantDir(root, convA, aid2); second != want {
		t.Errorf("EnsureDir(aid2) = %q, want %q", second, want)
	}
	for _, dir := range []string{first, second} {
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("stat %q: %v", dir, err)
		}
	}
}

// TestEnsureDir_CreatesAbsentInstanceDir pins the choice that an absent
// instance directory is created rather than refused: every other registry under
// that directory creates it lazily on first write, so attachment storage must
// not depend on whether some other subsystem happened to persist first.
func TestEnsureDir_CreatesAbsentInstanceDir(t *testing.T) {
	t.Parallel()

	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve parent: %v", err)
	}
	instanceDir := filepath.Join(parent, "instance")

	got, err := EnsureDir(instanceDir, convA, aid1)
	if err != nil {
		t.Fatalf("EnsureDir() error = %v, want nil", err)
	}
	if want := wantDir(instanceDir, convA, aid1); got != want {
		t.Errorf("EnsureDir() = %q, want %q", got, want)
	}
}

// TestEnsureDir_Idempotent pins the re-delivered upload: an exclusive create
// would turn it into a storage failure at #1744.
func TestEnsureDir_Idempotent(t *testing.T) {
	t.Parallel()

	instanceDir, root := resolvedInstanceDir(t)

	first, err := EnsureDir(instanceDir, convA, aid1)
	if err != nil {
		t.Fatalf("first EnsureDir() error = %v, want nil", err)
	}
	second, err := EnsureDir(instanceDir, convA, aid1)
	if err != nil {
		t.Fatalf("second EnsureDir() error = %v, want nil", err)
	}
	if want := wantDir(root, convA, aid1); first != want || second != want {
		t.Errorf("EnsureDir() = %q then %q, want %q both times", first, second, want)
	}
}

func TestEnsureDir_InvalidID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		conversation conversations.ConversationID
		attachment   string
	}{
		{"empty conversation id", "", aid1},
		{"conversation id spelling a traversal", "../../etc", aid1},
		{"conversation id one character short", conversations.ConversationID(convA[:35]), aid1},
		{"conversation id in uppercase hex", "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA", aid1},
		{"empty attachment id", convA, ""},
		{"attachment id spelling a traversal", convA, "../../etc/passwd"},
		// Long but not canonical: it fits protocol.MaxAttachmentIDBytes, which
		// is documented as explicitly NOT the containment mechanism. The shape
		// check is.
		{"attachment id at the byte ceiling", convA, strings.Repeat("a", protocol.MaxAttachmentIDBytes)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Never created by the fixture, so os.Stat reporting IsNotExist
			// afterwards is the "the instance directory is left exactly as it
			// was" assertion in its strongest form — and the sole red for a
			// build that resolves the anchor before validating.
			instanceDir := filepath.Join(t.TempDir(), "instance")

			got, err := EnsureDir(instanceDir, tt.conversation, tt.attachment)
			if !errors.Is(err, ErrInvalidID) {
				t.Errorf("EnsureDir() error = %v, want ErrInvalidID", err)
			}
			if errors.Is(err, ErrNotContained) {
				t.Errorf("EnsureDir() error = %v, want an id refusal rather than a containment refusal", err)
			}
			if got != "" {
				t.Errorf("EnsureDir() = %q, want no path on refusal", got)
			}
			if _, err := os.Stat(instanceDir); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("stat %q = %v, want the instance directory left uncreated", instanceDir, err)
			}
		})
	}
}

// TestEnsureDir_EscapingConversationSymlink is the criterion an anchor on the
// resolved CONVERSATION directory would make vacuous: that link resolves first,
// and everything beneath it is then trivially "contained" in it.
func TestEnsureDir_EscapingConversationSymlink(t *testing.T) {
	t.Parallel()

	instanceDir, _ := resolvedInstanceDir(t)
	// A second t.TempDir(), so the target EXISTS: EvalSymlinks on a dangling
	// link answers an OS error, and the test would then assert the wrong
	// sentinel for a right-looking reason.
	outside := t.TempDir()

	if err := os.MkdirAll(filepath.Join(instanceDir, "conversations"), 0o700); err != nil {
		t.Fatalf("create conversations dir: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(instanceDir, "conversations", convA)); err != nil {
		t.Fatalf("symlink conversation out of the instance dir: %v", err)
	}

	got, err := EnsureDir(instanceDir, convA, aid1)
	if !errors.Is(err, ErrNotContained) {
		t.Errorf("EnsureDir() error = %v, want ErrNotContained", err)
	}
	if errors.Is(err, ErrInvalidID) {
		t.Errorf("EnsureDir() error = %v, want a containment refusal rather than an id refusal", err)
	}
	if got != "" {
		t.Errorf("EnsureDir() = %q, want no path on refusal", got)
	}
	assertEmptyDir(t, outside)
}

// TestEnsureDir_SiblingConversationSymlink is the criterion an instance-dir
// anchor with a mere "is it under the root" containment test would make vacuous:
// the sibling stays inside the instance directory and passes. Equality against
// the textually expected destination refuses it.
func TestEnsureDir_SiblingConversationSymlink(t *testing.T) {
	t.Parallel()

	instanceDir, _ := resolvedInstanceDir(t)
	sibling := filepath.Join(instanceDir, "conversations", string(convB))
	if err := os.MkdirAll(sibling, 0o700); err != nil {
		t.Fatalf("create sibling conversation dir: %v", err)
	}
	if err := os.Symlink(sibling, filepath.Join(instanceDir, "conversations", convA)); err != nil {
		t.Fatalf("symlink conversation at its sibling: %v", err)
	}

	got, err := EnsureDir(instanceDir, convA, aid1)
	if !errors.Is(err, ErrNotContained) {
		t.Errorf("EnsureDir() error = %v, want ErrNotContained", err)
	}
	if errors.Is(err, ErrInvalidID) {
		t.Errorf("EnsureDir() error = %v, want a containment refusal rather than an id refusal", err)
	}
	if got != "" {
		t.Errorf("EnsureDir() = %q, want no path on refusal", got)
	}
	assertEmptyDir(t, sibling)
}

// TestEnsureDir_AttachmentDirIsSymlink proves the check is on the FULL path
// rather than on the conversation component alone: here every ancestor is a
// real directory inside the instance dir and only the leaf redirects.
func TestEnsureDir_AttachmentDirIsSymlink(t *testing.T) {
	t.Parallel()

	instanceDir, _ := resolvedInstanceDir(t)
	outside := t.TempDir()

	attachments := filepath.Join(instanceDir, "conversations", string(convA), "attachments")
	if err := os.MkdirAll(attachments, 0o700); err != nil {
		t.Fatalf("create attachments dir: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(attachments, aid1)); err != nil {
		t.Fatalf("symlink attachment dir out of the instance dir: %v", err)
	}

	got, err := EnsureDir(instanceDir, convA, aid1)
	if !errors.Is(err, ErrNotContained) {
		t.Errorf("EnsureDir() error = %v, want ErrNotContained", err)
	}
	if got != "" {
		t.Errorf("EnsureDir() = %q, want no path on refusal", got)
	}
	assertEmptyDir(t, outside)
}

// TestEnsureDir_DanglingConversationSymlink pins the one honest non-sentinel
// row: a conversation directory that exists as a symlink to nothing is broken
// host state, not a containment breach, so it answers a wrapped OS error rather
// than either sentinel and #1744 maps it to attachment.storage_failed with
// everything else it does not recognise.
func TestEnsureDir_DanglingConversationSymlink(t *testing.T) {
	t.Parallel()

	instanceDir, _ := resolvedInstanceDir(t)
	if err := os.MkdirAll(filepath.Join(instanceDir, "conversations"), 0o700); err != nil {
		t.Fatalf("create conversations dir: %v", err)
	}
	target := filepath.Join(t.TempDir(), "gone")
	if err := os.Symlink(target, filepath.Join(instanceDir, "conversations", convA)); err != nil {
		t.Fatalf("symlink conversation at a missing target: %v", err)
	}

	got, err := EnsureDir(instanceDir, convA, aid1)
	if err == nil {
		t.Fatalf("EnsureDir() = %q, want an error", got)
	}
	if errors.Is(err, ErrInvalidID) || errors.Is(err, ErrNotContained) {
		t.Errorf("EnsureDir() error = %v, want a wrapped OS error rather than a sentinel", err)
	}
	if got != "" {
		t.Errorf("EnsureDir() = %q, want no path on failure", got)
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stat %q = %v, want the dangling target left uncreated", target, err)
	}
}

// wantPath composes the FILE path ResolvePath must answer, textually beneath
// the resolved root. Sibling to wantDir, and built the same way and for the
// same reason: the assertion is on the full path, so a build that anchored
// somewhere else or dropped a component fails it.
func wantPath(root string, conv conversations.ConversationID, attachmentID, name string) string {
	return filepath.Join(wantDir(root, conv, attachmentID), name)
}

// assertNoPath is every ResolvePath refusal assertion in one place: no path,
// ErrNotFound, and NEITHER of the two sentinels a reader would expect this
// function to reuse from EnsureDir. That second half is not stylistic — #1897
// maps ErrInvalidID and ErrNotContained to CodeAttachmentStorageFailed, and the
// retrieval leg answers attachment.not_found, so a refusal wrapping either
// would be mapped to the wrong wire code by a dispatch site sharing any part of
// that table.
func assertNoPath(t *testing.T, got string, err error) {
	t.Helper()

	if !errors.Is(err, ErrNotFound) {
		t.Errorf("ResolvePath() error = %v, want ErrNotFound", err)
	}
	for _, sentinel := range []error{ErrInvalidID, ErrNotContained} {
		if errors.Is(err, sentinel) {
			t.Errorf("ResolvePath() error = %v also wraps %v, want the retrieval leg's own sentinel alone", err, sentinel)
		}
	}
	if got != "" {
		t.Errorf("ResolvePath() = %q, want no path on refusal", got)
	}
}

// plantFile writes a file directly into dir, creating dir if absent. Used for
// the fixtures Store cannot produce: a leftover temp file, and a file parked
// under a symlink target outside the instance directory.
func plantFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()

	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create %q: %v", dir, err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write %q: %v", path, err)
	}
	return path
}

// TestResolvePath_RoundTrip is AC 1: the path answered is the path Store wrote,
// and reading it yields the bytes that were stored.
func TestResolvePath_RoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		filename string
		stored   string
	}{
		// Vacuous on its own: here the client name and the stored component
		// are the same string, so a build that joined the CLIENT name onto the
		// directory instead of answering the entry it found passes this row.
		{"unchanged by the sanitiser", "report.pdf", "report.pdf"},
		// The row that makes AC 1 non-vacuous. The two strings differ, so the
		// joined-client-name build answers a path that does not exist and the
		// read-back fails.
		{"rewritten by the sanitiser", "../../etc/passwd", "_.._.._etc_passwd"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			instanceDir, root := resolvedInstanceDir(t)
			dir, err := EnsureDir(instanceDir, convA, aid1)
			if err != nil {
				t.Fatalf("EnsureDir() error = %v, want nil", err)
			}
			data := []byte("bytes for " + tt.name)
			if _, err := Store(dir, tt.filename, data); err != nil {
				t.Fatalf("Store() error = %v, want nil", err)
			}

			got, err := ResolvePath(instanceDir, convA, aid1)
			if err != nil {
				t.Fatalf("ResolvePath() error = %v, want nil", err)
			}
			if want := wantPath(root, convA, aid1, tt.stored); got != want {
				t.Fatalf("ResolvePath() = %q, want %q", got, want)
			}
			body, err := os.ReadFile(got)
			if err != nil {
				t.Fatalf("read the resolved path: %v", err)
			}
			if !bytes.Equal(body, data) {
				t.Errorf("the resolved path holds %q, want %q", body, data)
			}
		})
	}
}

// TestResolvePath_CreatesNothing is AC 2. Each row names the paths that must
// still be absent afterwards, which is what a build reinstating EnsureDir's
// MkdirAll of the anchor — or walking to the longest existing ancestor and
// creating the rest — fails.
func TestResolvePath_CreatesNothing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(t *testing.T) (instanceDir string, absent []string)
	}{
		{
			"instance directory does not exist",
			func(t *testing.T) (string, []string) {
				instanceDir := filepath.Join(t.TempDir(), "instance")
				return instanceDir, []string{instanceDir}
			},
		},
		{
			"instance directory exists, nothing beneath it",
			func(t *testing.T) (string, []string) {
				instanceDir, _ := resolvedInstanceDir(t)
				return instanceDir, []string{filepath.Join(instanceDir, "conversations")}
			},
		},
		{
			"conversation exists, attachment directory does not",
			func(t *testing.T) (string, []string) {
				instanceDir, _ := resolvedInstanceDir(t)
				attachments := filepath.Join(instanceDir, "conversations", string(convA), "attachments")
				if err := os.MkdirAll(attachments, 0o700); err != nil {
					t.Fatalf("create attachments dir: %v", err)
				}
				return instanceDir, []string{filepath.Join(attachments, aid1)}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			instanceDir, absent := tt.setup(t)

			got, err := ResolvePath(instanceDir, convA, aid1)
			assertNoPath(t, got, err)

			for _, path := range absent {
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("stat %q = %v, want it left uncreated", path, err)
				}
			}
		})
	}
}

// TestResolvePath_InvalidID is AC 3's shape refusal, over the same rows
// EnsureDir's own table carries. Every row stores the CANONICAL pair first and
// asserts it still resolves, so a row's refusal is attributable to the shape
// check rather than to a fixture that holds nothing to find.
func TestResolvePath_InvalidID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		conversation conversations.ConversationID
		attachment   string
	}{
		{"empty conversation id", "", aid1},
		{"conversation id spelling a traversal", "../../etc", aid1},
		{"conversation id one character short", conversations.ConversationID(convA[:35]), aid1},
		{"conversation id in uppercase hex", "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA", aid1},
		{"empty attachment id", convA, ""},
		{"attachment id spelling a traversal", convA, "../../etc/passwd"},
		// Fits protocol.MaxAttachmentIDBytes, which is documented as explicitly
		// NOT the containment mechanism. The shape check is.
		{"attachment id at the byte ceiling", convA, strings.Repeat("a", protocol.MaxAttachmentIDBytes)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			instanceDir, _ := resolvedInstanceDir(t)
			dir, err := EnsureDir(instanceDir, convA, aid1)
			if err != nil {
				t.Fatalf("EnsureDir() error = %v, want nil", err)
			}
			if _, err := Store(dir, "report.pdf", []byte("the canonical pair's bytes")); err != nil {
				t.Fatalf("Store() error = %v, want nil", err)
			}

			got, err := ResolvePath(instanceDir, tt.conversation, tt.attachment)
			assertNoPath(t, got, err)

			if _, err := ResolvePath(instanceDir, convA, aid1); err != nil {
				t.Errorf("the canonical pair no longer resolves (%v), so this row's refusal proves nothing", err)
			}
		})
	}
}

// TestResolvePath_UnknownPair is AC 3's unknown-id refusal, in both directions
// that matter: an id nobody stored, and an id stored under a DIFFERENT
// conversation. The second is the confinement property from the caller's side —
// naming another conversation must not reach its file.
func TestResolvePath_UnknownPair(t *testing.T) {
	t.Parallel()

	instanceDir, _ := resolvedInstanceDir(t)
	dir, err := EnsureDir(instanceDir, convA, aid1)
	if err != nil {
		t.Fatalf("EnsureDir() error = %v, want nil", err)
	}
	if _, err := Store(dir, "report.pdf", []byte("stored under convA/aid1")); err != nil {
		t.Fatalf("Store() error = %v, want nil", err)
	}

	t.Run("attachment id nobody stored", func(t *testing.T) {
		got, err := ResolvePath(instanceDir, convA, aid2)
		assertNoPath(t, got, err)
	})
	t.Run("attachment id stored under another conversation", func(t *testing.T) {
		got, err := ResolvePath(instanceDir, convB, aid1)
		assertNoPath(t, got, err)
	})
}

// TestResolvePath_EscapingConversationSymlink is the criterion an anchor on the
// resolved CONVERSATION directory would make vacuous. The target is POPULATED
// with a file the build under test could answer, so the refusal is a real
// refusal rather than an empty directory answering nothing either way.
func TestResolvePath_EscapingConversationSymlink(t *testing.T) {
	t.Parallel()

	instanceDir, _ := resolvedInstanceDir(t)
	// A second t.TempDir(), so the target EXISTS: EvalSymlinks on a dangling
	// link answers an OS error and the row would refuse for the wrong reason.
	outside := t.TempDir()
	plantFile(t, filepath.Join(outside, "attachments", aid1), "secret.pdf", []byte("another conversation's bytes"))

	if err := os.MkdirAll(filepath.Join(instanceDir, "conversations"), 0o700); err != nil {
		t.Fatalf("create conversations dir: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(instanceDir, "conversations", convA)); err != nil {
		t.Fatalf("symlink conversation out of the instance dir: %v", err)
	}

	got, err := ResolvePath(instanceDir, convA, aid1)
	assertNoPath(t, got, err)
	assertDirEntries(t, filepath.Join(outside, "attachments", aid1), "secret.pdf")
}

// TestResolvePath_SiblingConversationSymlink is the criterion an instance-dir
// anchor with a mere "is it under the root" containment test would make
// vacuous: the sibling stays inside the instance directory and passes any such
// test. Equality against the textually expected destination refuses it. The
// sibling's own attachment is stored through the real path, so a
// containment-only build answers convB's bytes for a convA request.
func TestResolvePath_SiblingConversationSymlink(t *testing.T) {
	t.Parallel()

	instanceDir, _ := resolvedInstanceDir(t)
	siblingDir, err := EnsureDir(instanceDir, convB, aid1)
	if err != nil {
		t.Fatalf("EnsureDir(convB) error = %v, want nil", err)
	}
	if _, err := Store(siblingDir, "report.pdf", []byte("convB's bytes")); err != nil {
		t.Fatalf("Store(convB) error = %v, want nil", err)
	}

	sibling := filepath.Join(instanceDir, "conversations", string(convB))
	if err := os.Symlink(sibling, filepath.Join(instanceDir, "conversations", convA)); err != nil {
		t.Fatalf("symlink conversation at its sibling: %v", err)
	}

	got, err := ResolvePath(instanceDir, convA, aid1)
	assertNoPath(t, got, err)
	assertDirEntries(t, siblingDir, "report.pdf")
}

// TestResolvePath_AttachmentDirIsSymlink proves the check is on the FULL path
// rather than on the conversation component alone: every ancestor here is a
// real directory inside the instance dir and only the leaf redirects.
func TestResolvePath_AttachmentDirIsSymlink(t *testing.T) {
	t.Parallel()

	instanceDir, _ := resolvedInstanceDir(t)
	outside := t.TempDir()
	plantFile(t, outside, "secret.pdf", []byte("bytes outside the instance dir"))

	attachments := filepath.Join(instanceDir, "conversations", string(convA), "attachments")
	if err := os.MkdirAll(attachments, 0o700); err != nil {
		t.Fatalf("create attachments dir: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(attachments, aid1)); err != nil {
		t.Fatalf("symlink attachment dir out of the instance dir: %v", err)
	}

	got, err := ResolvePath(instanceDir, convA, aid1)
	assertNoPath(t, got, err)
	assertDirEntries(t, outside, "secret.pdf")
}

// TestResolvePath_LeftoverTempFile is AC 4. The leftover is reachable because
// Store creates its temp file INSIDE the destination directory — that is what
// makes the rename intra-filesystem and therefore atomic — and its
// defer os.Remove does not survive a kill.
//
// The row below is non-vacuous only because '.' sorts ahead of every character
// SanitizeFilename can emit, so the leftover is what a build selecting the
// first entry in name order answers.
func TestResolvePath_LeftoverTempFile(t *testing.T) {
	t.Parallel()

	t.Run("beside a stored file", func(t *testing.T) {
		t.Parallel()

		instanceDir, root := resolvedInstanceDir(t)
		dir, err := EnsureDir(instanceDir, convA, aid1)
		if err != nil {
			t.Fatalf("EnsureDir() error = %v, want nil", err)
		}
		data := []byte("the stored attachment")
		if _, err := Store(dir, "report.pdf", data); err != nil {
			t.Fatalf("Store() error = %v, want nil", err)
		}
		plantFile(t, dir, ".attachment-1234567.tmp", []byte("a killed upload's leftover"))
		assertDirEntries(t, dir, ".attachment-1234567.tmp", "report.pdf")

		got, err := ResolvePath(instanceDir, convA, aid1)
		if err != nil {
			t.Fatalf("ResolvePath() error = %v, want nil", err)
		}
		if want := wantPath(root, convA, aid1, "report.pdf"); got != want {
			t.Errorf("ResolvePath() = %q, want %q", got, want)
		}
	})

	t.Run("alone in the directory", func(t *testing.T) {
		t.Parallel()

		instanceDir, _ := resolvedInstanceDir(t)
		dir, err := EnsureDir(instanceDir, convA, aid1)
		if err != nil {
			t.Fatalf("EnsureDir() error = %v, want nil", err)
		}
		plantFile(t, dir, ".attachment-7654321.tmp", []byte("a killed upload's leftover"))

		got, err := ResolvePath(instanceDir, convA, aid1)
		assertNoPath(t, got, err)
	})
}

// TestResolvePath_TwoFilesOneAttachmentDirectory is AC 5: Intake.Receive calls
// Store(dir, chunk.Filename, data), so one attachment id uploaded twice under
// two client filenames leaves two files in one directory. The published
// contract makes that last-writer-wins and explicitly not a privilege boundary,
// so what is required here is a deterministic answer rather than a refusal.
//
// The fixture stores the lexicographically SMALLER name FIRST, which is what
// separates the shipped rule from the two neighbouring ones: a newest-wins
// selection and a last-entry-in-name-order selection both answer zebra.pdf.
func TestResolvePath_TwoFilesOneAttachmentDirectory(t *testing.T) {
	t.Parallel()

	instanceDir, root := resolvedInstanceDir(t)
	dir, err := EnsureDir(instanceDir, convA, aid1)
	if err != nil {
		t.Fatalf("EnsureDir() error = %v, want nil", err)
	}
	alpha := []byte("uploaded first, under alpha.pdf")
	if _, err := Store(dir, "alpha.pdf", alpha); err != nil {
		t.Fatalf("Store(alpha.pdf) error = %v, want nil", err)
	}
	if _, err := Store(dir, "zebra.pdf", []byte("uploaded second, under zebra.pdf")); err != nil {
		t.Fatalf("Store(zebra.pdf) error = %v, want nil", err)
	}
	assertDirEntries(t, dir, "alpha.pdf", "zebra.pdf")

	want := wantPath(root, convA, aid1, "alpha.pdf")
	for call := range 3 {
		got, err := ResolvePath(instanceDir, convA, aid1)
		if err != nil {
			t.Fatalf("ResolvePath() call %d error = %v, want nil", call, err)
		}
		if got != want {
			t.Fatalf("ResolvePath() call %d = %q, want %q on every call", call, got, want)
		}
	}
	body, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("read the resolved path: %v", err)
	}
	if !bytes.Equal(body, alpha) {
		t.Errorf("the resolved path holds %q, want %q", body, alpha)
	}
}

// TestResolvePath_NonRegularEntry is the leaf half of the containment property,
// which the directory-level symlink rows above cannot reach: an entry that is
// not a plain file is skipped rather than followed or answered. Without it the
// DIRECTORY is confined and the FILE it answers is not, and #1746 streams
// whatever the link points at.
func TestResolvePath_NonRegularEntry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		plant func(t *testing.T, dir string)
	}{
		{
			"a symlink pointing outside the instance directory",
			func(t *testing.T, dir string) {
				target := plantFile(t, t.TempDir(), "secret.pdf", []byte("bytes outside the instance dir"))
				if err := os.Symlink(target, filepath.Join(dir, "linked.pdf")); err != nil {
					t.Fatalf("symlink into the attachment dir: %v", err)
				}
			},
		},
		{
			"a subdirectory",
			func(t *testing.T, dir string) {
				if err := os.MkdirAll(filepath.Join(dir, "nested.pdf"), 0o700); err != nil {
					t.Fatalf("create a subdirectory: %v", err)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			instanceDir, _ := resolvedInstanceDir(t)
			dir, err := EnsureDir(instanceDir, convA, aid1)
			if err != nil {
				t.Fatalf("EnsureDir() error = %v, want nil", err)
			}
			tt.plant(t, dir)

			got, err := ResolvePath(instanceDir, convA, aid1)
			assertNoPath(t, got, err)
		})
	}
}

// TestResolvePath_CaseFoldedID is what makes AC 3's shape refusal load-bearing
// rather than incidental. Measured, not assumed: with every other row in
// TestResolvePath_InvalidID the non-canonical id also names a directory that
// does not exist, so an overlay mutant deleting BOTH conversations.ValidID
// calls passes the whole package green — those rows refuse for absence, and the
// shape check they are named after is never the reason.
//
// A case-folded id is the one shape that resolves anyway. EnsureDir's doc block
// states why: ValidID's lowercase-only alphabet is what keeps the id-to-
// directory mapping injective on a case-insensitive filesystem, which APFS is
// by default, and filepath.EvalSymlinks deliberately does not case-canonicalise
// (that is the property separating it from agentrun.ResolveWorkdir). So on
// macOS the uppercased pair resolves to the very directory the lowercase pair
// stored into, and only the shape check refuses it. On a case-sensitive
// filesystem the row still passes, by absence rather than by shape — correct
// either way, just not the mutant's sole red there.
func TestResolvePath_CaseFoldedID(t *testing.T) {
	t.Parallel()

	// Local to this test because the package-level ids are all digits and
	// dashes, which fold to themselves.
	const (
		convHex = "abcdefab-cdef-4abc-8def-abcdefabcdef"
		aidHex  = "bcdefabc-defa-4bcd-9efa-bcdefabcdefa"
	)

	tests := []struct {
		name         string
		conversation conversations.ConversationID
		attachment   string
	}{
		{"conversation id case-folded to uppercase", conversations.ConversationID(strings.ToUpper(convHex)), aidHex},
		{"attachment id case-folded to uppercase", convHex, strings.ToUpper(aidHex)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			instanceDir, _ := resolvedInstanceDir(t)
			dir, err := EnsureDir(instanceDir, convHex, aidHex)
			if err != nil {
				t.Fatalf("EnsureDir() error = %v, want nil", err)
			}
			if _, err := Store(dir, "report.pdf", []byte("the lowercase pair's bytes")); err != nil {
				t.Fatalf("Store() error = %v, want nil", err)
			}

			got, err := ResolvePath(instanceDir, tt.conversation, tt.attachment)
			assertNoPath(t, got, err)

			if _, err := ResolvePath(instanceDir, convHex, aidHex); err != nil {
				t.Errorf("the canonical pair no longer resolves (%v), so this row's refusal proves nothing", err)
			}
		})
	}
}

package attachments

import (
	"errors"
	"os"
	"path/filepath"
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

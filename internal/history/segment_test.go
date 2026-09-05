package history

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/conversations"
)

// The header literal is written by hand so it is a stable prefix and a stable
// length — cursor offsets depend on both. This is the test that keeps the
// literal and the struct it must decode as in agreement.
func TestSegmentHeaderLiteralMatchesItsStruct(t *testing.T) {
	t.Parallel()
	if !strings.HasSuffix(segmentHeaderLine, "\n") {
		t.Fatal("the header literal must terminate its line")
	}
	var h segmentHeader
	if err := json.Unmarshal([]byte(strings.TrimSuffix(segmentHeaderLine, "\n")), &h); err != nil {
		t.Fatalf("header literal does not decode: %v", err)
	}
	if h.Format != segmentFormat || h.Version != segmentVersion {
		t.Fatalf("header literal decodes to %+v, want format %q version %d", h, segmentFormat, segmentVersion)
	}
}

func TestSegmentNamesSortNumerically(t *testing.T) {
	t.Parallel()
	// Lexicographic order must equal numeric order, which is what lets the
	// walk take os.ReadDir's sorted answer without re-sorting.
	if !(segmentName(2) < segmentName(10) && segmentName(10) < segmentName(1<<63)) {
		t.Fatalf("segment names do not sort numerically: %q %q %q", segmentName(2), segmentName(10), segmentName(1<<63))
	}
	for _, n := range []uint64{1, 10, 1 << 63} {
		got, ok := parseSegmentName(segmentName(n))
		if !ok || got != n {
			t.Fatalf("parseSegmentName(%q) = (%d,%v), want (%d,true)", segmentName(n), got, ok, n)
		}
	}
	for _, bad := range []string{"segment-1.jsonl", "segment-0000000000000000000x.jsonl", "attachments", ".hidden", "segment-00000000000000000001.tmp"} {
		if _, ok := parseSegmentName(bad); ok {
			t.Errorf("parseSegmentName(%q) accepted a name that is not a segment", bad)
		}
	}
}

// AC 3's neighbour: a segment file grown out of band past the ceiling a
// well-formed one can reach must not pin an arbitrary allocation.
func TestOversizedSegmentIsRefused(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := newStore(root, testSegmentBytes)
	appendN(t, s, convA, 0, 3)

	dir := historyDir(root, convA)
	names, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read history dir: %v", err)
	}
	path := filepath.Join(dir, names[0].Name())
	pad := make([]byte, segmentCeiling(testSegmentBytes)+1)
	for i := range pad {
		pad[i] = 'x'
	}
	if err := os.WriteFile(path, append([]byte(segmentHeaderLine), pad...), 0o600); err != nil {
		t.Fatalf("pad segment: %v", err)
	}

	fresh := newStore(root, testSegmentBytes)
	if _, err := fresh.Page(convA, "", 5); !errors.Is(err, ErrCorruptSegment) {
		t.Fatalf("Page over an oversized segment: err = %v, want ErrCorruptSegment", err)
	}
}

// A symlink parked in the history directory is stepped over rather than
// followed: containment of the directory is not containment of what it holds.
func TestSymlinkedSegmentIsSkipped(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := newStore(root, testSegmentBytes)
	want := appendN(t, s, convA, 0, 3)

	outside := filepath.Join(t.TempDir(), "elsewhere.jsonl")
	if err := os.WriteFile(outside, []byte(segmentHeaderLine+`{"id":9999,"type":"leak","payload":{},"ts":"2026-09-05T12:00:00Z"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write the outside file: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(historyDir(root, convA), segmentName(9))); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	fresh := newStore(root, testSegmentBytes)
	got := walkAll(t, fresh, convA, 5)
	assertReversed(t, got, want)
	for _, e := range got {
		if e.ID == 9999 {
			t.Fatal("the walk followed a symlinked segment")
		}
	}
}

// The containment refusal that a filepath.Rel-style "is it under the root" test
// would pass: a history directory symlinked at a SIBLING conversation.
func TestHistoryDirectorySymlinkedAtASiblingIsRefused(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := newStore(root, testSegmentBytes)
	appendN(t, s, convB, 0, 2)

	// Point conversation A's history at conversation B's — inside the instance
	// directory the whole way, so only full-path equality catches it.
	aDir := filepath.Join(root, "conversations", string(convA))
	if err := os.MkdirAll(aDir, 0o700); err != nil {
		t.Fatalf("create conversation A: %v", err)
	}
	if err := os.Symlink(historyDir(root, convB), filepath.Join(aDir, "history")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	fresh := newStore(root, testSegmentBytes)
	if _, err := fresh.Page(convA, "", 5); !errors.Is(err, ErrNotContained) {
		t.Fatalf("Page over a sibling-symlinked history dir: err = %v, want ErrNotContained", err)
	}
	if _, err := fresh.Append(convA, "t", json.RawMessage(`{}`), testTS); !errors.Is(err, ErrNotContained) {
		t.Fatalf("Append over a sibling-symlinked history dir: err = %v, want ErrNotContained", err)
	}
}

// New is the production constructor; nothing but the segment bound differs from
// the one every other test builds, and that difference must not be silent.
func TestNewUsesTheNamedSegmentBound(t *testing.T) {
	t.Parallel()
	s := New(t.TempDir())
	if s.maxSegmentBytes != MaxSegmentBytes {
		t.Fatalf("New built a store bounded at %d, want MaxSegmentBytes (%d)", s.maxSegmentBytes, MaxSegmentBytes)
	}
	if _, err := s.Append(conversations.ConversationID(convA), "assistant_delta", json.RawMessage(`{"n":1}`), testTS); err != nil {
		t.Fatalf("Append through New: %v", err)
	}
}

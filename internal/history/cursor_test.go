package history

import (
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
)

// AC 6: a cursor this package did not mint is refused rather than followed.
// Every case must return an explicit error, no entries, and touch no segment
// outside the conversation being read.
func TestForgedCursorsAreRefused(t *testing.T) {
	t.Parallel()

	// A real cursor, and the position it names, to build near-misses from.
	root := t.TempDir()
	minted := newStore(root, testSegmentBytes)
	appendN(t, minted, convA, 0, 12)
	first, err := minted.Page(convA, "", 3)
	if err != nil {
		t.Fatalf("mint a real cursor: %v", err)
	}
	realSeg, realOff := decodeForTest(t, first.Cursor)

	tests := []struct {
		name   string
		cursor string
	}{
		{"not base64", "!!!not base64!!!"},
		// A real cursor's bytes in the wrong base64 alphabet: padded standard
		// encoding, which the unpadded url-safe decoder refuses. The empty
		// string is deliberately absent — it is the documented "start at the
		// newest entry", not a forgery.
		{"padded standard base64", base64.StdEncoding.EncodeToString([]byte("1." + string(convA) + ".1.41"))},
		{"too few fields", enc("1." + string(convA) + ".1")},
		{"too many fields", enc("1." + string(convA) + ".1.0.9")},
		{"unknown version tag", enc("2." + string(convA) + fmt.Sprintf(".%d.%d", realSeg, realOff))},
		{"non-canonical conversation id", enc("1.not-a-uuid" + fmt.Sprintf(".%d.%d", realSeg, realOff))},
		{"another conversation", enc("1." + string(convB) + fmt.Sprintf(".%d.%d", realSeg, realOff))},
		{"non-numeric segment", enc("1." + string(convA) + fmt.Sprintf(".seven.%d", realOff))},
		{"negative segment", enc("1." + string(convA) + fmt.Sprintf(".-1.%d", realOff))},
		{"non-numeric offset", enc("1." + string(convA) + fmt.Sprintf(".%d.seven", realSeg))},
		{"negative offset", enc("1." + string(convA) + fmt.Sprintf(".%d.-8", realSeg))},
		{"segment that does not exist", enc("1." + string(convA) + fmt.Sprintf(".9999.%d", realOff))},
		{"segment zero", enc("1." + string(convA) + fmt.Sprintf(".0.%d", realOff))},
		{"offset one byte past a boundary", enc("1." + string(convA) + fmt.Sprintf(".%d.%d", realSeg, realOff+1))},
		{"offset inside the header", enc("1." + string(convA) + fmt.Sprintf(".%d.4", realSeg))},
		{"offset past the end of the segment", enc("1." + string(convA) + fmt.Sprintf(".%d.%d", realSeg, math.MaxInt64))},
		{"maximum uint64 offset", enc("1." + string(convA) + fmt.Sprintf(".%d.%d", realSeg, uint64(math.MaxUint64)))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := newStore(root, testSegmentBytes)
			p, err := s.Page(convA, tt.cursor, 5)
			if !errors.Is(err, ErrInvalidCursor) {
				t.Fatalf("Page: err = %v, want ErrInvalidCursor", err)
			}
			if len(p.Entries) != 0 || p.AtStart || p.Cursor != "" {
				t.Fatalf("a refused cursor answered %d entries, AtStart=%v, cursor=%q", len(p.Entries), p.AtStart, p.Cursor)
			}
			// The cursor is attacker-chosen text; a refusal that echoed it
			// would put those bytes into whatever the consumer logs.
			if strings.Contains(err.Error(), tt.cursor) && tt.cursor != "" {
				t.Fatalf("refusal echoes the cursor: %v", err)
			}
		})
	}
}

// A cursor minted while reading one conversation must not read another's log —
// the strongest observable form of "causes no read outside that conversation's
// own log directory".
func TestCursorFromAnotherConversationReadsNothing(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := newStore(root, testSegmentBytes)
	appendN(t, s, convA, 0, 12)
	appendN(t, s, convB, 500, 12)

	pageA, err := s.Page(convA, "", 3)
	if err != nil {
		t.Fatalf("page conversation A: %v", err)
	}

	fresh := newStore(root, testSegmentBytes)
	fresh.resetReadStats()
	p, err := fresh.Page(convB, pageA.Cursor, 5)
	if !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("A's cursor against B: err = %v, want ErrInvalidCursor", err)
	}
	if len(p.Entries) != 0 {
		t.Fatalf("A's cursor against B returned %d entries", len(p.Entries))
	}
	if _, opens := fresh.readStats(); opens != 0 {
		t.Fatalf("a refused cursor opened %d segments, want 0", opens)
	}
}

// A cursor minted by this package, naming this conversation, and still not
// followable: nothing was ever written for the conversation, so no position in
// it exists. An absent log answers a cursor-less page as empty-and-at-the-start,
// which must not become a way to have an arbitrary cursor accepted.
func TestCursorForAConversationWithNoLogIsRefused(t *testing.T) {
	t.Parallel()
	s := newStore(t.TempDir(), testSegmentBytes)

	p, err := s.Page(convA, mintCursor(convA, 1, int64(len(segmentHeaderLine))), 5)
	if !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("Page with a cursor into an absent log: err = %v, want ErrInvalidCursor", err)
	}
	if len(p.Entries) != 0 || p.AtStart || p.Cursor != "" {
		t.Fatalf("a refused cursor answered %d entries, AtStart=%v, cursor=%q", len(p.Entries), p.AtStart, p.Cursor)
	}
}

// The mint/parse pair is the only place the cursor's shape is known; a cursor
// that survives a round trip must name the position it was minted for.
func TestCursorRoundTrip(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		seg uint64
		off int64
	}{{1, 41}, {7, 0}, {math.MaxUint64, math.MaxInt64}} {
		c := mintCursor(convA, tc.seg, tc.off)
		got, err := parseCursor(c, convA)
		if err != nil {
			t.Fatalf("parseCursor(mintCursor(%d,%d)): %v", tc.seg, tc.off, err)
		}
		if got.segment != tc.seg || got.offset != tc.off {
			t.Fatalf("round trip gave (%d,%d), want (%d,%d)", got.segment, got.offset, tc.seg, tc.off)
		}
		if strings.ContainsAny(c, ".+/=") {
			t.Fatalf("cursor %q is not opaque base64url", c)
		}
	}
}

func enc(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

func decodeForTest(t *testing.T, cursor string) (uint64, int64) {
	t.Helper()
	pos, err := parseCursor(cursor, convA)
	if err != nil {
		t.Fatalf("parseCursor(%q): %v", cursor, err)
	}
	return pos.segment, pos.offset
}

package history

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
)

const (
	convA = conversations.ConversationID("11111111-1111-4111-8111-111111111111")
	convB = conversations.ConversationID("22222222-2222-4222-8222-222222222222")
)

// testSegmentBytes is small enough that a handful of entries fill a segment, so
// AC 1's boundary crossing and AC 4's many-segment log need tens of entries
// rather than thousands. It is the whole reason newStore exists beside New.
const testSegmentBytes = 400

var testTS = time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)

// appendN appends n entries whose payloads carry their index, and returns them
// in append order (oldest first). Every entry encodes to the same length for
// indices of equal width, which is what makes AC 4's segment layout repeatable.
func appendN(t *testing.T, s *Store, convID conversations.ConversationID, from, n int) []Entry {
	t.Helper()
	out := make([]Entry, 0, n)
	for i := from; i < from+n; i++ {
		// Quoted: JSON numbers may not carry leading zeros, and the fixed width
		// is what keeps entry lines comparable in size.
		payload := json.RawMessage(fmt.Sprintf(`{"n":"%06d"}`, i))
		id, err := s.Append(convID, "assistant_delta", payload, testTS)
		if err != nil {
			t.Fatalf("Append(%d): %v", i, err)
		}
		out = append(out, Entry{ID: id, Type: "assistant_delta", Payload: payload, TS: testTS})
	}
	return out
}

// walkAll pages backwards from the newest entry to the start of the log and
// returns everything it saw, newest-first, failing on a walk that does not
// terminate on AtStart.
func walkAll(t *testing.T, s *Store, convID conversations.ConversationID, limit int) []Entry {
	t.Helper()
	var got []Entry
	cursor := ""
	for i := 0; ; i++ {
		if i > 1000 {
			t.Fatal("walk did not reach the start of the log in 1000 pages")
		}
		p, err := s.Page(convID, cursor, limit)
		if err != nil {
			t.Fatalf("Page(page %d): %v", i, err)
		}
		if len(p.Entries) > limit {
			t.Fatalf("page %d returned %d entries, over the requested limit %d", i, len(p.Entries), limit)
		}
		got = append(got, p.Entries...)
		if p.AtStart {
			if p.Cursor != "" {
				t.Fatalf("page %d reported AtStart with a non-empty cursor", i)
			}
			return got
		}
		if p.Cursor == "" {
			t.Fatalf("page %d is not AtStart but returned no cursor", i)
		}
		cursor = p.Cursor
	}
}

// assertReversed checks that got is want reversed — the "newest-first, no entry
// repeated and none skipped" property in one comparison.
func assertReversed(t *testing.T, got, want []Entry) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("walk returned %d entries, want %d", len(got), len(want))
	}
	for i, g := range got {
		w := want[len(want)-1-i]
		if g.ID != w.ID {
			t.Fatalf("entry %d: id %d, want %d", i, g.ID, w.ID)
		}
		if g.Type != w.Type {
			t.Fatalf("entry %d: type %q, want %q", i, g.Type, w.Type)
		}
		if string(g.Payload) != string(w.Payload) {
			t.Fatalf("entry %d: payload %q, want %q", i, g.Payload, w.Payload)
		}
		if !g.TS.Equal(w.TS) {
			t.Fatalf("entry %d: ts %v, want %v", i, g.TS, w.TS)
		}
	}
}

func historyDir(root string, convID conversations.ConversationID) string {
	return filepath.Join(root, "conversations", string(convID), "history")
}

// --- AC 1: newest-first paging, backwards to the start of the log ------------

func TestPageWalksBackwardsAcrossSegments(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := newStore(root, testSegmentBytes)

	want := appendN(t, s, convA, 0, 25)

	// The fixture must actually cross segment boundaries, or the walk proves
	// nothing about the property this test is named for.
	segs, err := os.ReadDir(historyDir(root, convA))
	if err != nil {
		t.Fatalf("read history dir: %v", err)
	}
	if len(segs) < 2 {
		t.Fatalf("fixture spans %d segment(s); the test needs at least 2", len(segs))
	}

	assertReversed(t, walkAll(t, s, convA, 3), want)
}

func TestPageOnAConversationWithNoLogIsEmptyAndAtStart(t *testing.T) {
	t.Parallel()
	s := newStore(t.TempDir(), testSegmentBytes)

	p, err := s.Page(convA, "", 10)
	if err != nil {
		t.Fatalf("Page on an absent log: %v", err)
	}
	if len(p.Entries) != 0 || !p.AtStart || p.Cursor != "" {
		t.Fatalf("got %d entries, AtStart=%v, cursor=%q; want empty and at the start", len(p.Entries), p.AtStart, p.Cursor)
	}
}

func TestPageFillingExactlyAtTheStartIsNotYetAtStart(t *testing.T) {
	t.Parallel()
	s := newStore(t.TempDir(), testSegmentBytes)
	appendN(t, s, convA, 0, 6)

	// Two pages of three consume the log exactly. A full page may always have
	// more behind it, so the second must NOT claim AtStart; the third is the
	// terminal, empty answer.
	p1, err := s.Page(convA, "", 3)
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	p2, err := s.Page(convA, p1.Cursor, 3)
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	if len(p2.Entries) != 3 || p2.AtStart {
		t.Fatalf("page 2: %d entries, AtStart=%v; want a full page that is not yet at the start", len(p2.Entries), p2.AtStart)
	}
	p3, err := s.Page(convA, p2.Cursor, 3)
	if err != nil {
		t.Fatalf("page 3: %v", err)
	}
	if len(p3.Entries) != 0 || !p3.AtStart {
		t.Fatalf("page 3: %d entries, AtStart=%v; want the terminal empty page", len(p3.Entries), p3.AtStart)
	}
}

func TestPageIsolatesConversations(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := newStore(root, testSegmentBytes)
	wantA := appendN(t, s, convA, 0, 9)
	appendN(t, s, convB, 100, 9)

	assertReversed(t, walkAll(t, s, convA, 4), wantA)
}

// --- AC 2: the reader answers from disk, ids outlive the process ------------

func TestReopenedStoreReadsFromDiskAndContinuesTheIDSpace(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	first := newStore(root, testSegmentBytes)
	want := appendN(t, first, convA, 0, 20)

	// A store that has never seen a single Append must still answer.
	second := newStore(root, testSegmentBytes)
	assertReversed(t, walkAll(t, second, convA, 5), want)

	var maxOnDisk uint64
	for _, e := range want {
		if e.ID > maxOnDisk {
			maxOnDisk = e.ID
		}
	}
	id, err := second.Append(convA, "turn_end", json.RawMessage(`{"after":"reopen"}`), testTS)
	if err != nil {
		t.Fatalf("Append after reopen: %v", err)
	}
	if id <= maxOnDisk {
		t.Fatalf("id after reopen is %d, want greater than every id on disk (%d)", id, maxOnDisk)
	}

	third := newStore(root, testSegmentBytes)
	p, err := third.Page(convA, "", 1)
	if err != nil {
		t.Fatalf("Page from a third store: %v", err)
	}
	if len(p.Entries) != 1 || p.Entries[0].ID != id {
		t.Fatalf("newest entry after reopen is %+v, want the entry with id %d", p.Entries, id)
	}
}

// --- AC 3: every segment carries a schema version --------------------------

func TestUnrecognisedSegmentVersionDecodesNothing(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		header string
	}{
		{"future version", `{"format":"pyrycode.history","version":2}`},
		{"foreign format", `{"format":"claude.transcript","version":1}`},
		{"not json at all", `this is not a header`},
		{"empty first line", ``},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			s := newStore(root, testSegmentBytes)
			appendN(t, s, convA, 0, 3)

			// Rewrite the one segment with a header this build cannot know,
			// keeping a well-formed entry line behind it so the assertion is
			// about the version arm and not about the body.
			dir := historyDir(root, convA)
			names, err := os.ReadDir(dir)
			if err != nil {
				t.Fatalf("read history dir: %v", err)
			}
			body := tt.header + "\n" + `{"id":1,"type":"assistant_delta","payload":{"n":0},"ts":"2026-09-05T12:00:00Z"}` + "\n"
			if err := os.WriteFile(filepath.Join(dir, names[0].Name()), []byte(body), 0o600); err != nil {
				t.Fatalf("rewrite segment: %v", err)
			}

			fresh := newStore(root, testSegmentBytes)
			p, err := fresh.Page(convA, "", 10)
			if !errors.Is(err, ErrUnknownVersion) {
				t.Fatalf("Page: err = %v, want ErrUnknownVersion", err)
			}
			if len(p.Entries) != 0 {
				t.Fatalf("Page decoded %d entries from an unrecognised segment, want 0", len(p.Entries))
			}
		})
	}
}

func TestCorruptBodyInARecognisedSegmentIsDistinctFromAnUnknownVersion(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := newStore(root, testSegmentBytes)
	appendN(t, s, convA, 0, 3)

	dir := historyDir(root, convA)
	names, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read history dir: %v", err)
	}
	body := segmentHeaderLine + "not an entry\n"
	if err := os.WriteFile(filepath.Join(dir, names[0].Name()), []byte(body), 0o600); err != nil {
		t.Fatalf("rewrite segment: %v", err)
	}

	fresh := newStore(root, testSegmentBytes)
	p, err := fresh.Page(convA, "", 10)
	if !errors.Is(err, ErrCorruptSegment) {
		t.Fatalf("Page: err = %v, want ErrCorruptSegment", err)
	}
	if errors.Is(err, ErrUnknownVersion) {
		t.Fatal("a corrupt body must not be reported as an unknown version")
	}
	if len(p.Entries) != 0 {
		t.Fatalf("Page decoded %d entries from a corrupt segment, want 0", len(p.Entries))
	}
}

// --- AC 4: serving the newest page is bounded by the page, not the log ------

func TestNewestPageWorkIsBoundedByThePageNotTheLog(t *testing.T) {
	t.Parallel()

	// perSegment is read back off disk rather than assumed, so the fixture
	// stays a whole number of segments if the bound or the entry shape changes.
	probeRoot := t.TempDir()
	probe := newStore(probeRoot, testSegmentBytes)
	appendUniform(t, probe, convA, 40)
	perSegment := entriesInFirstSegment(t, probe, probeRoot, convA)
	if perSegment < 2 {
		t.Fatalf("fixture puts %d entries in a segment; the test needs at least 2", perSegment)
	}

	small := newStore(t.TempDir(), testSegmentBytes)
	appendUniform(t, small, convA, perSegment*5)
	bytesSmall, opensSmall := measureNewestPage(t, small, convA, perSegment-1)

	big := newStore(t.TempDir(), testSegmentBytes)
	appendUniform(t, big, convA, perSegment*500) // 100x the segment count
	bytesBig, opensBig := measureNewestPage(t, big, convA, perSegment-1)

	if bytesSmall != bytesBig {
		t.Fatalf("bytes read to serve the newest page: %d at 5 segments, %d at 500; want unchanged", bytesSmall, bytesBig)
	}
	if opensSmall != opensBig {
		t.Fatalf("segments opened to serve the newest page: %d at 5 segments, %d at 500; want unchanged", opensSmall, opensBig)
	}
	if opensSmall != 1 {
		t.Fatalf("a page smaller than one segment opened %d segments, want 1", opensSmall)
	}
}

// appendUniform appends n entries whose encoded lines are all the SAME length,
// by padding each payload to compensate for the width of the id about to be
// minted. Without that, ids crossing from 3 to 4 digits change how many entries
// fit in a segment, and "the bytes read are unchanged" stops being a statement
// about the read path at all.
func appendUniform(t *testing.T, s *Store, convID conversations.ConversationID, n int) {
	t.Helper()
	const payloadWidth = 24 // must exceed the widest id the fixture mints
	for i := 1; i <= n; i++ {
		digits := len(strconv.Itoa(i))
		if digits >= payloadWidth {
			t.Fatalf("id %d is too wide for a %d-byte payload budget", i, payloadWidth)
		}
		payload := json.RawMessage(`{"p":"` + strings.Repeat("x", payloadWidth-digits) + `"}`)
		id, err := s.Append(convID, "assistant_delta", payload, testTS)
		if err != nil {
			t.Fatalf("Append(%d): %v", i, err)
		}
		if id != uint64(i) {
			t.Fatalf("Append(%d) minted id %d; the padding assumes ids run 1..n", i, id)
		}
	}
}

// entriesInFirstSegment reports how many entries the appender packs into a
// segment that has rolled, read back off disk rather than computed.
func entriesInFirstSegment(t *testing.T, s *Store, root string, convID conversations.ConversationID) int {
	t.Helper()
	dir := historyDir(root, convID)
	names, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read history dir: %v", err)
	}
	if len(names) < 2 {
		t.Fatalf("no rolled segment to measure (%d present)", len(names))
	}
	entries, _, err := s.readSegment(filepath.Join(dir, names[0].Name()))
	if err != nil {
		t.Fatalf("read segment: %v", err)
	}
	return len(entries)
}

func measureNewestPage(t *testing.T, s *Store, convID conversations.ConversationID, limit int) (int64, int) {
	t.Helper()
	s.resetReadStats()
	p, err := s.Page(convID, "", limit)
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	if len(p.Entries) != limit {
		t.Fatalf("newest page returned %d entries, want %d", len(p.Entries), limit)
	}
	return s.readStats()
}

// --- AC 5: appends during a backward walk change nothing the walk returns ---

func TestAppendsDuringAWalkDoNotDisturbIt(t *testing.T) {
	t.Parallel()
	s := newStore(t.TempDir(), testSegmentBytes)
	before := appendN(t, s, convA, 0, 14)

	var got []Entry
	cursor := ""
	interleaved := 100
	for {
		p, err := s.Page(convA, cursor, 3)
		if err != nil {
			t.Fatalf("Page: %v", err)
		}
		got = append(got, p.Entries...)
		if p.AtStart {
			break
		}
		cursor = p.Cursor
		// An append lands between every pair of pages.
		appendN(t, s, convA, interleaved, 2)
		interleaved += 2
	}

	// The walk saw exactly the log as it stood when it started: nothing
	// appended during it appears, and nothing that existed was reordered.
	assertReversed(t, got, before)
}

func TestPageServedBeforeAnAppendNeverContainsIt(t *testing.T) {
	t.Parallel()
	s := newStore(t.TempDir(), testSegmentBytes)
	appendN(t, s, convA, 0, 4)

	p, err := s.Page(convA, "", 10)
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	id, err := s.Append(convA, "turn_end", json.RawMessage(`{"late":true}`), testTS)
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	for _, e := range p.Entries {
		if e.ID == id {
			t.Fatalf("a page served before the append contains entry %d", id)
		}
	}
}

func TestConcurrentAppendsAndWalks(t *testing.T) {
	t.Parallel()
	s := newStore(t.TempDir(), testSegmentBytes)
	appendN(t, s, convA, 0, 10)

	var wg sync.WaitGroup
	ids := make(chan uint64, 60)
	for w := 0; w < 3; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				id, err := s.Append(convA, "assistant_delta", json.RawMessage(fmt.Sprintf(`{"w":%d,"i":%d}`, w, i)), testTS)
				if err != nil {
					t.Errorf("concurrent Append: %v", err)
					return
				}
				ids <- id
			}
		}(w)
	}
	for r := 0; r < 2; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Deliberately not walkAll: a helper that calls t.Fatal cannot run
			// off the test's own goroutine, so this walk reports with t.Error
			// and returns.
			for i := 0; i < 10; i++ {
				var got []Entry
				cursor := ""
				for {
					p, err := s.Page(convA, cursor, 4)
					if err != nil {
						t.Errorf("concurrent Page: %v", err)
						return
					}
					got = append(got, p.Entries...)
					if p.AtStart {
						break
					}
					cursor = p.Cursor
				}
				// Whatever the walk caught, it must be strictly descending in
				// id: appends may extend the log under it, never reorder it.
				for k := 1; k < len(got); k++ {
					if got[k-1].ID <= got[k].ID {
						t.Errorf("walk is not newest-first at %d: %d then %d", k, got[k-1].ID, got[k].ID)
						return
					}
				}
			}
		}()
	}
	wg.Wait()
	close(ids)

	seen := make(map[uint64]bool)
	for id := range ids {
		if seen[id] {
			t.Fatalf("id %d was minted twice", id)
		}
		seen[id] = true
	}
	if len(seen) != 60 {
		t.Fatalf("minted %d distinct ids, want 60", len(seen))
	}
}

// --- argument validation ---------------------------------------------------

func TestInvalidConversationIDIsRefused(t *testing.T) {
	t.Parallel()
	s := newStore(t.TempDir(), testSegmentBytes)
	for _, bad := range []conversations.ConversationID{"", "..", "../../etc", "not-a-uuid", "11111111-1111-1111-8111-111111111111"} {
		if _, err := s.Append(bad, "t", json.RawMessage(`{}`), testTS); !errors.Is(err, ErrInvalidID) {
			t.Errorf("Append(%q): err = %v, want ErrInvalidID", bad, err)
		}
		if _, err := s.Page(bad, "", 1); !errors.Is(err, ErrInvalidID) {
			t.Errorf("Page(%q): err = %v, want ErrInvalidID", bad, err)
		}
	}
}

func TestUnstorablePayloadIsRefused(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := newStore(root, testSegmentBytes)

	if _, err := s.Append(convA, "t", json.RawMessage(`{"unterminated":`), testTS); !errors.Is(err, ErrInvalidPayload) {
		t.Errorf("Append with invalid JSON: err = %v, want ErrInvalidPayload", err)
	}
	big := json.RawMessage(`{"big":"` + string(make([]byte, testSegmentBytes)) + `"}`)
	for i := range big {
		if big[i] == 0 {
			big[i] = 'x'
		}
	}
	if _, err := s.Append(convA, "t", big, testTS); !errors.Is(err, ErrInvalidPayload) {
		t.Errorf("Append over the segment bound: err = %v, want ErrInvalidPayload", err)
	}
	// Neither refusal may leave anything on disk.
	if _, err := os.Stat(historyDir(root, convA)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a refused Append created %q", historyDir(root, convA))
	}
}

func TestPageSizeBounds(t *testing.T) {
	t.Parallel()
	s := newStore(t.TempDir(), testSegmentBytes)
	appendN(t, s, convA, 0, 3)

	for _, bad := range []int{0, -1} {
		if _, err := s.Page(convA, "", bad); !errors.Is(err, ErrInvalidPageSize) {
			t.Errorf("Page(limit=%d): err = %v, want ErrInvalidPageSize", bad, err)
		}
	}
	// An over-large ask is clamped, not refused: a page is "up to limit".
	p, err := s.Page(convA, "", MaxPageEntries+1_000_000)
	if err != nil {
		t.Fatalf("Page with an over-large limit: %v", err)
	}
	if len(p.Entries) != 3 {
		t.Fatalf("clamped page returned %d entries, want the whole 3-entry log", len(p.Entries))
	}
}

func TestPayloadRoundTripsWithoutHTMLEscaping(t *testing.T) {
	t.Parallel()
	s := newStore(t.TempDir(), testSegmentBytes)
	payload := json.RawMessage(`{"text":"a <b> & c"}`)
	if _, err := s.Append(convA, "assistant_delta", payload, testTS); err != nil {
		t.Fatalf("Append: %v", err)
	}
	p, err := s.Page(convA, "", 1)
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	if got := string(p.Entries[0].Payload); got != string(payload) {
		t.Fatalf("payload round-tripped as %q, want %q", got, payload)
	}
}

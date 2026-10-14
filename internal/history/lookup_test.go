package history

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
)

func testLookup(t *testing.T, s *Store, conv conversations.ConversationID, ids ...uint64) []Entry {
	t.Helper()
	got, err := s.Lookup(conv, ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(ids) {
		t.Fatalf("lookup length = %d, want %d", len(got), len(ids))
	}
	for i, id := range ids {
		if got[i].ID != id {
			t.Fatalf("lookup[%d] ID = %d, want %d", i, got[i].ID, id)
		}
	}
	return got
}

func testLookupMissing(t *testing.T, s *Store, ids, missing []uint64) {
	t.Helper()
	got, err := s.Lookup(convA, ids)
	var absent *MissingEntriesError
	if got != nil || !errors.As(err, &absent) || !reflect.DeepEqual(absent.IDs, missing) {
		t.Fatalf("lookup = %#v, %v; want missing %v and no result", got, err, missing)
	}
}

func testLookupWrite(t *testing.T, root string, conv conversations.ConversationID, num uint64, body string) string {
	t.Helper()
	dir := historyDir(root, conv)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, segmentName(num))
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func testLookupLine(id uint64) string {
	return fmt.Sprintf("{\"id\":%d,\"type\":\"opaque\",\"payload\":{\"text\":\"<&>\"},\"ts\":\"2026-09-05T12:00:00Z\"}\n", id)
}

func TestLookupRoundTrip(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := newStore(root, testSegmentBytes)
	want := appendN(t, s, convA, 0, 9)
	shown := false
	metadata := Metadata{Session: &SessionProvenance{Kind: "codex", SessionID: "opaque-session"}, Shown: &shown}
	id, err := s.AppendWithMetadata(convA, "future", []byte(`{"x":"<&>","shown":null}`), testTS, metadata)
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, Entry{ID: id, Type: "future", Payload: []byte(`{"x":"<&>","shown":null}`), TS: testTS, Session: metadata.Session, Shown: &shown})
	appendN(t, s, convA, 10, 4) // seal the metadata segment before testing aliasing
	segs, err := listSegments(historyDir(root, convA))
	if err != nil || len(segs) < 2 {
		t.Fatalf("roll fixture: %v, %v", segs, err)
	}
	for _, reader := range []*Store{s, newStore(root, testSegmentBytes)} {
		ids := []uint64{1, 4, 9, id}
		got := testLookup(t, reader, convA, ids...)
		for i, id := range ids {
			if !reflect.DeepEqual(got[i], want[id-1]) {
				t.Fatalf("entry = %#v, want %#v", got[i], want[id-1])
			}
		}
		got[0].Payload[0] = '!'
		got[3].Payload[0], got[3].Session.Kind, *got[3].Shown = '!', "changed", true
		again := testLookup(t, reader, convA, ids...)
		if !reflect.DeepEqual(again[0], want[0]) || !reflect.DeepEqual(again[3], want[id-1]) {
			t.Fatal("returned data aliases retained entries")
		}
	}
}

func TestLookupInput(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := newStore(root, testSegmentBytes)
	for _, ids := range [][]uint64{{2, 1}, {1, 1}, {0, 0}} {
		got, err := s.Lookup(convA, ids)
		if got != nil || !errors.Is(err, ErrInvalidEntryIDs) {
			t.Fatalf("unordered lookup = %v, %v", got, err)
		}
	}
	if got, err := s.Lookup("../bad", nil); got != nil || !errors.Is(err, ErrInvalidID) {
		t.Fatalf("invalid conv = %v, %v", got, err)
	}
	testLookup(t, s, convA)
	testLookupMissing(t, s, []uint64{0, 1, 999}, []uint64{0, 1, 999})
	if _, err := os.Stat(historyDir(root, convA)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lookup created directory: %v", err)
	}
	for _, body := range []string{"", `{"format":`, segmentHeaderLine, segmentHeaderLine + `{"id":1`} {
		testLookupWrite(t, root, convA, 1, body)
		testLookupMissing(t, s, []uint64{0, 1}, []uint64{0, 1})
	}
	testLookupWrite(t, root, convA, 1, segmentHeaderLine)
	appendN(t, s, convA, 0, 2)
	testLookupMissing(t, s, []uint64{0, 1, 3, 999}, []uint64{0, 3, 999})
}

func TestLookupSelectiveReads(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	var total int64
	for num := uint64(1); num <= 5; num++ {
		body := segmentHeaderLine
		for offset := uint64(0); offset < 6; offset++ {
			body += testLookupLine(num*10 + offset)
		}
		testLookupWrite(t, root, convA, num, body)
		total += int64(len(body))
	}
	s := newStore(root, 1024)
	testLookup(t, s, convA, 31)
	bytes, opens := s.readStats()
	if opens != 6 || bytes <= 0 || bytes >= total {
		t.Fatalf("discovery read = %d bytes, %d opens; total %d", bytes, opens, total)
	}
	s.resetReadStats()
	testLookup(t, s, convA, 31)
	if bytes, opens := s.readStats(); bytes != 0 || opens != 0 {
		t.Fatalf("cache hit read = %d, %d", bytes, opens)
	}
	// Complete corruption after the first entry must not affect another range.
	body := segmentHeaderLine + testLookupLine(10) + "broken complete entry\n"
	testLookupWrite(t, root, convA, 1, body)
	s.resetReadStats()
	testLookup(t, s, convA, 41)
	if _, opens := s.readStats(); opens != 2 {
		t.Fatalf("resized-prefix + selected segment opens = %d", opens)
	}
	testLookupWrite(t, root, convA, 6, segmentHeaderLine+testLookupLine(60))
	s.resetReadStats()
	testLookup(t, s, convA, 60)
	if _, opens := s.readStats(); opens != 2 {
		t.Fatalf("new-prefix + selected segment opens = %d", opens)
	}
	if got, err := s.Lookup(convA, []uint64{10}); got != nil || !errors.Is(err, ErrCorruptSegment) {
		t.Fatalf("selected corruption = %v, %v", got, err)
	}
}

func TestLookupTornTail(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	testLookupWrite(t, root, convA, 1, segmentHeaderLine+testLookupLine(1)+testLookupLine(2)+`{"id":3`)
	s := newStore(root, testSegmentBytes)
	testLookup(t, s, convA, 1, 2)
	testLookupMissing(t, s, []uint64{1, 3, 100}, []uint64{3, 100})
	s = newStore(root, testSegmentBytes)
	if id, err := s.Append(convA, "opaque", []byte(`null`), testTS); id != 3 || err != nil {
		t.Fatalf("append past tear = %d, %v", id, err)
	}
	testLookup(t, s, convA, 1, 2, 3)
}

func TestLookupFreshness(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	testLookupWrite(t, root, convA, 1, segmentHeaderLine)
	s := newStore(root, testSegmentBytes)
	testLookupMissing(t, s, []uint64{1}, []uint64{1})
	appendN(t, s, convA, 0, 1)
	testLookup(t, s, convA, 1)
	appendN(t, s, convA, 1, 1)
	testLookup(t, s, convA, 1, 2)
	appendN(t, s, convA, 2, 6)
	s.resetReadStats()
	testLookup(t, s, convA, 8)
	if _, opens := s.readStats(); opens != 1 {
		t.Fatalf("append/roll bounds caused %d opens, want one selected read", opens)
	}
	// An independently written header-only segment can gain a range later.
	testLookupWrite(t, root, convA, 100, segmentHeaderLine)
	testLookupMissing(t, s, []uint64{9}, []uint64{9})
	appendN(t, newStore(root, testSegmentBytes), convA, 8, 1)
	testLookup(t, s, convA, 9)
}

func TestLookupMembership(t *testing.T) {
	t.Parallel()
	for _, warm := range []bool{false, true} {
		for _, action := range []string{"remove", "leaf-outside", "leaf-sibling", "dir-outside", "dir-sibling"} {
			t.Run(fmt.Sprintf("warm=%v/%s", warm, action), func(t *testing.T) {
				root := t.TempDir()
				path := testLookupWrite(t, root, convA, 1, segmentHeaderLine+testLookupLine(1))
				testLookupWrite(t, root, convA, 2, segmentHeaderLine+testLookupLine(2))
				sibling := testLookupWrite(t, root, convB, 1, segmentHeaderLine+testLookupLine(1))
				outside := testLookupWrite(t, t.TempDir(), convA, 1, segmentHeaderLine+testLookupLine(1))
				s := newStore(root, testSegmentBytes)
				if warm {
					testLookup(t, s, convA, 1)
				}
				if action == "remove" || action == "leaf-outside" || action == "leaf-sibling" {
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
					if action != "remove" {
						target := outside
						if action == "leaf-sibling" {
							target = sibling
						}
						if err := os.Symlink(target, path); err != nil {
							t.Fatal(err)
						}
					}
					testLookupMissing(t, s, []uint64{0, 1, 2, 100}, []uint64{0, 1, 100})
				} else {
					dir := filepath.Dir(path)
					if err := os.Rename(dir, dir+"-saved"); err != nil {
						t.Fatal(err)
					}
					target := filepath.Dir(outside)
					if action == "dir-sibling" {
						target = filepath.Dir(sibling)
					}
					if err := os.Symlink(target, dir); err != nil {
						t.Fatal(err)
					}
					for _, ids := range [][]uint64{nil, {1}} {
						if got, err := s.Lookup(convA, ids); got != nil || !errors.Is(err, ErrNotContained) {
							t.Fatalf("redirect lookup = %v, %v", got, err)
						}
					}
				}
			})
		}
	}
}

func TestLookupCache(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, conv := range []conversations.ConversationID{convA, convB} {
		for num := uint64(1); num <= 3; num++ {
			body := segmentHeaderLine + fmt.Sprintf("{\"id\":%d,\"type\":\"%s\",\"payload\":null,\"ts\":\"2026-09-05T12:00:00Z\"}\n", num, conv)
			testLookupWrite(t, root, conv, num, body)
		}
	}
	s := newStore(root, testSegmentBytes)
	if s.lookupCache.limit != 8*1024*1024 {
		t.Fatalf("default cache limit = %d", s.lookupCache.limit)
	}
	s.lookupCache.limit = 400 // two one-entry segments fit, three cannot
	testLookup(t, s, convA, 1)
	testLookup(t, s, convA, 2)
	s.resetReadStats()
	testLookup(t, s, convA, 1) // promote A1 before adding B1
	if _, opens := s.readStats(); opens != 0 {
		t.Fatalf("promotion read %d segments", opens)
	}
	if got := testLookup(t, s, convB, 1); got[0].Type != string(convB) {
		t.Fatal("cache crossed conversations")
	}
	s.resetReadStats()
	testLookup(t, s, convA, 1)
	if _, opens := s.readStats(); opens != 0 {
		t.Fatal("LRU evicted most recently used segment")
	}
	s.resetReadStats()
	testLookup(t, s, convA, 2)
	if _, opens := s.readStats(); opens != 1 {
		t.Fatalf("evicted lookup opens = %d", opens)
	}
	if s.lookupCache.bytes > s.lookupCache.limit {
		t.Fatal("cache exceeded byte limit")
	}
	testLookupWrite(t, root, convA, 1, segmentHeaderLine+`{"id":1,"type":"resized","payload":null,"ts":"2026-09-05T12:00:00Z"}`+"\n")
	s.resetReadStats()
	if got := testLookup(t, s, convA, 1); got[0].Type != "resized" {
		t.Fatal("resized segment reused cached data")
	}
	if _, opens := s.readStats(); opens != 2 {
		t.Fatalf("resized prefix + segment opens = %d", opens)
	}
	for _, num := range []uint64{2, 3} {
		if err := os.Remove(filepath.Join(historyDir(root, convA), segmentName(num))); err != nil {
			t.Fatal(err)
		}
	}
	s.resetReadStats()
	testLookup(t, s, convA, 1)
	if _, opens := s.readStats(); opens != 1 {
		t.Fatalf("cached segment becoming newest read %d segments", opens)
	}
	s = newStore(root, testSegmentBytes)
	s.lookupCache.limit = 1
	testLookup(t, s, convB, 1)
	s.resetReadStats()
	testLookup(t, s, convB, 1)
	if _, opens := s.readStats(); opens != 1 || s.lookupCache.bytes != 0 {
		t.Fatalf("oversized segment retained: opens=%d bytes=%d", opens, s.lookupCache.bytes)
	}
}

func TestLookupCacheAccounting(t *testing.T) {
	t.Parallel()
	entries := make([]segEntry, 1, 10)
	entries[0].entry = Entry{Payload: make([]byte, 1, 300), Type: "opaque"}
	base := lookupDecodedBytes(entries)
	if base <= lookupDecodedBytes(append([]segEntry(nil), entries...)) {
		t.Fatal("accounting omits unused backing capacity")
	}
	entries[0].entry.Payload = entries[0].entry.Payload[:1:1]
	if lookupDecodedBytes(entries) != base-299 {
		t.Fatal("accounting omits payload allocation capacity")
	}
	entries[0].entry.Payload = make([]byte, 1, 300)
	entries[0].entry.Session = &SessionProvenance{Kind: "codex", SessionID: "opaque-session"}
	shown := false
	entries[0].entry.Shown = &shown
	withMetadata := lookupDecodedBytes(entries)
	if withMetadata <= base+int64(len("codexopaque-session")) {
		t.Fatal("accounting omits pointed-to metadata")
	}
	entries[0].entry.TS = time.Date(2026, 9, 5, 12, 0, 0, 0, time.FixedZone("", 90*60))
	if lookupDecodedBytes(entries) != withMetadata+256 {
		t.Fatal("accounting omits timestamp location")
	}
	c := segmentCache{limit: base}
	c.put(segmentCacheKey{convID: convA, segment: 1}, entries)
	if c.bytes != 0 {
		t.Fatal("referenced allocations bypassed cache limit")
	}
}

func TestLookupRejectsStoredData(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, body string
		want       error
	}{
		{"version", "{\"format\":\"pyrycode.history\",\"version\":99}\n" + testLookupLine(1), ErrUnknownVersion},
		{"corruption", segmentHeaderLine + testLookupLine(1) + "broken\n", ErrCorruptSegment},
		{"duplicate", segmentHeaderLine + testLookupLine(1) + testLookupLine(1), ErrCorruptSegment},
		{"zero", segmentHeaderLine + testLookupLine(0), ErrCorruptSegment},
		{"ceiling", segmentHeaderLine + testLookupLine(1) + string(make([]byte, 900)), ErrCorruptSegment},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			testLookupWrite(t, root, convA, 1, tc.body)
			if got, err := newStore(root, testSegmentBytes).Lookup(convA, []uint64{1}); got != nil || !errors.Is(err, tc.want) {
				t.Fatalf("lookup = %v, %v; want %v", got, err, tc.want)
			}
		})
	}
}

func TestLookupConcurrent(t *testing.T) {
	t.Parallel()
	s := newStore(t.TempDir(), testSegmentBytes)
	appendN(t, s, convA, 0, 1)
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				got, err := s.Lookup(convA, []uint64{1})
				if err != nil || len(got) != 1 || got[0].ID != 1 {
					t.Errorf("concurrent lookup = %v, %v", got, err)
					return
				}
				got[0].Payload[0] = '!'
			}
		}()
	}
	for range 20 {
		if _, err := s.Append(convA, "opaque", []byte(`null`), testTS); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	testLookup(t, s, convA, 1, 21)
}

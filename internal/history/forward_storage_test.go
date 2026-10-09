package history

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestForwardEmptyAndStorageErrors(t *testing.T) {
	t.Parallel()
	line, _ := encodeEntry(Entry{ID: 1, Type: "unknown", Payload: json.RawMessage(`{}`), TS: testTS})
	for _, tt := range []struct {
		name, data string
		want       error
		count      int
	}{
		{"empty", "", nil, 0},
		{"header", segmentHeaderLine, nil, 0},
		{"torn header", `{"format":`, nil, 0},
		{"torn entry", segmentHeaderLine + string(line) + `{"id":2`, nil, 1},
		{"corrupt", segmentHeaderLine + "bad\n", ErrCorruptSegment, 0},
		{"version", `{"format":"pyrycode.history","version":2}` + "\n", ErrUnknownVersion, 0},
		{"oversize", strings.Repeat("x", int(segmentCeiling(testSegmentBytes)+1)), ErrCorruptSegment, 0},
		{"duplicate", segmentHeaderLine + string(line) + string(line), ErrCorruptSegment, 0},
		{"zero ID", segmentHeaderLine + `{"id":0,"payload":{}}` + "\n", ErrCorruptSegment, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := newStore(t.TempDir(), testSegmentBytes)
			dir := historyDir(s.instanceDir, convA)
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, segmentName(1)), []byte(tt.data), 0o600); err != nil {
				t.Fatal(err)
			}
			r := testForwardReader(t, s, 0)
			n := 0
			err := r.Walk(context.Background(), 10, func(es []Entry) error { n += len(es); return nil })
			if !errors.Is(err, tt.want) || n != tt.count {
				t.Fatalf("count=%d error=%v, want %d %v", n, err, tt.count, tt.want)
			}
		})
	}
	s := newStore(t.TempDir(), testSegmentBytes)
	r := testForwardReader(t, s, 0)
	for _, h := range []uint64{0, 10} {
		if err := r.Walk(context.Background(), h, func([]Entry) error { t.Fatal("missing log delivered entries"); return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Forward("../invalid", 0); !errors.Is(err, ErrInvalidID) {
		t.Fatal(err)
	}
	if _, err := os.Stat(historyDir(s.instanceDir, convA)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("read created directory")
	}
	dir := historyDir(s.instanceDir, convA)
	if err := os.MkdirAll(filepath.Join(dir, segmentName(1)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, segmentName(1)), filepath.Join(dir, segmentName(2))); err != nil {
		t.Fatal(err)
	}
	if err := r.Walk(context.Background(), 10, func([]Entry) error { t.Fatal("nonregular segment read"); return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestForwardRechecksReads(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"missing", "containment", "leaf", "order"} {
		t.Run(mode, func(t *testing.T) {
			s := newStore(t.TempDir(), testSegmentBytes)
			appendN(t, s, convA, 0, 12)
			dir := historyDir(s.instanceDir, convA)
			segs, err := listSegments(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(segs) < 3 {
				t.Fatal("fixture must span segments")
			}
			r := testForwardReader(t, s, 0)
			changed := false
			err = r.Walk(context.Background(), 12, func([]Entry) error {
				if changed {
					return nil
				}
				changed = true
				path := filepath.Join(dir, segs[1].name)
				switch mode {
				case "missing":
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
				case "containment":
					appendN(t, s, convB, 0, 1)
					if err := os.Rename(dir, dir+"-saved"); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(historyDir(s.instanceDir, convB), dir); err != nil {
						t.Fatal(err)
					}
				case "leaf":
					if err := os.Rename(path, path+"-saved"); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(path+"-saved", path); err != nil {
						t.Fatal(err)
					}
				case "order":
					line, _ := encodeEntry(Entry{ID: 1, Payload: json.RawMessage(`{}`), TS: testTS})
					if err := os.WriteFile(path, append([]byte(segmentHeaderLine), line...), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				return nil
			})
			want := ErrCorruptSegment
			if mode == "missing" {
				want = fs.ErrNotExist
			}
			if mode == "containment" {
				want = ErrNotContained
			}
			if !errors.Is(err, want) {
				t.Fatalf("%s: %v, want %v", mode, err, want)
			}
		})
	}
}

func TestForwardBoundedChunksAndPosition(t *testing.T) {
	t.Parallel()
	s := New(t.TempDir())
	appendN(t, s, convA, 0, MaxPageEntries+3)
	r := testForwardReader(t, s, 0)
	var chunks []int
	err := r.Walk(context.Background(), uint64(MaxPageEntries+3), func(es []Entry) error { chunks = append(chunks, len(es)); return nil })
	if err != nil || !reflect.DeepEqual(chunks, []int{MaxPageEntries, 3}) {
		t.Fatalf("chunks=%v, error=%v", chunks, err)
	}
	s = newStore(t.TempDir(), testSegmentBytes)
	appendN(t, s, convA, 0, 20)
	r = testForwardReader(t, s, 0)
	if err := r.Walk(context.Background(), 20, func([]Entry) error { return nil }); err != nil {
		t.Fatal(err)
	}
	segs, err := listSegments(historyDir(s.instanceDir, convA))
	if err != nil {
		t.Fatal(err)
	}
	// Damage completed segments, including the sealed final segment.
	if !r.sealed {
		t.Fatal("fixture must end in a sealed segment")
	}
	for _, seg := range segs {
		if err := os.WriteFile(filepath.Join(historyDir(s.instanceDir, convA), seg.name), []byte("bad\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	appendN(t, s, convA, 20, 4)
	s.mu.Lock()
	before := s.segmentsOpened
	s.mu.Unlock()
	var ids []uint64
	if err := r.Walk(context.Background(), 24, func(es []Entry) error {
		for _, e := range es {
			ids = append(ids, e.ID)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids, []uint64{21, 22, 23, 24}) {
		t.Fatal(ids)
	}
	s.mu.Lock()
	opened := s.segmentsOpened - before
	bytes := s.readBytes
	s.mu.Unlock()
	if opened > 3 || bytes > int64(s.segmentsOpened)*(segmentCeiling(testSegmentBytes)+1) {
		t.Fatal("unbounded segment work")
	}
	dir := historyDir(s.instanceDir, convA)
	if err := os.Rename(dir, dir+"-saved"); err != nil {
		t.Fatal(err)
	}
	if err := r.Walk(context.Background(), 25, func([]Entry) error { return nil }); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
}

func testForwardStoredIDs(t *testing.T, s *Store, segment uint64, sealed bool, ids ...uint64) {
	t.Helper()
	dir := historyDir(s.instanceDir, convA)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data := []byte(segmentHeaderLine)
	for _, id := range ids {
		line, err := encodeEntry(Entry{ID: id, Type: "unknown", Payload: json.RawMessage(`{}`), TS: testTS})
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, line...)
	}
	if sealed {
		data = append(data, `{"id":`...) // A torn tail seals the segment.
	}
	if err := os.WriteFile(filepath.Join(dir, segmentName(segment)), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestForwardOrderingAcrossCalls(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		id   uint64
	}{
		{"duplicate", 3},
		{"decreasing", 2},
	} {
		for _, mode := range []string{"walk", "replay-to-tail", "tail-catch-up"} {
			t.Run(tt.name+"/"+mode, func(t *testing.T) {
				s := newStore(t.TempDir(), testSegmentBytes)
				testForwardStoredIDs(t, s, 1, true, 3)
				r := testForwardReader(t, s, 0)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				var ids []uint64
				consume := func(es []Entry) error {
					for _, e := range es {
						ids = append(ids, e.ID)
					}
					if mode == "tail-catch-up" && es[len(es)-1].ID == 3 {
						if id, err := s.Append(convA, "unknown", json.RawMessage(`{}`), testTS); err != nil || id != 4 {
							t.Fatalf("append ID=%d: %v", id, err)
						}
						// Corrupt the committed segment before the notified catch-up.
						testForwardStoredIDs(t, s, 2, false, tt.id, 4)
					}
					if es[len(es)-1].ID > 3 {
						cancel()
					}
					return nil
				}
				if mode != "tail-catch-up" {
					testForwardStoredIDs(t, s, 2, false, tt.id, 4)
					if err := r.Walk(ctx, 3, consume); err != nil {
						t.Fatal(err)
					}
				}
				var err error
				if mode == "walk" {
					err = r.Walk(ctx, 4, consume)
				} else {
					err = r.Tail(ctx, consume)
				}
				if !errors.Is(err, ErrCorruptSegment) || !reflect.DeepEqual(ids, []uint64{3}) || r.LastEntryID() != 3 {
					t.Fatalf("IDs=%v last=%d error=%v, want [3], 3, corruption", ids, r.LastEntryID(), err)
				}
				s.mu.Lock()
				defer s.mu.Unlock()
				if len(s.followers) != 0 {
					t.Fatal("failed tail retained a registration")
				}
			})
		}
	}
}

func TestForwardOrderingResumeAndRetry(t *testing.T) {
	t.Parallel()
	s := newStore(t.TempDir(), testSegmentBytes)
	testForwardStoredIDs(t, s, 1, true, 1)
	testForwardStoredIDs(t, s, 2, false, 3, 5)
	r := testForwardReader(t, s, 2) // The resume ID need not exist in storage.
	stop := errors.New("consumer stopped")
	if err := r.Walk(context.Background(), 3, func([]Entry) error { return stop }); !errors.Is(err, stop) || r.LastEntryID() != 2 {
		t.Fatalf("failed callback: last=%d error=%v", r.LastEntryID(), err)
	}
	var ids []uint64
	for _, h := range []uint64{3, 5} {
		if err := r.Walk(context.Background(), h, func(es []Entry) error {
			for _, e := range es {
				ids = append(ids, e.ID)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(ids, []uint64{3, 5}) || r.LastEntryID() != 5 {
		t.Fatalf("IDs=%v last=%d, want [3 5], 5", ids, r.LastEntryID())
	}
}

func TestForwardCommitNotifications(t *testing.T) {
	t.Parallel()
	s := newStore(t.TempDir(), testSegmentBytes)
	wake, stop := s.follow(convA)
	defer stop()
	if id, err := s.Append(convA, "bad", json.RawMessage(`bad`), testTS); id != 0 || err == nil {
		t.Fatal(id, err)
	}
	if id, err := s.AppendWithMetadata(convA, "bad", json.RawMessage(`{}`), testTS, Metadata{Session: &SessionProvenance{Kind: "bad"}}); id != 0 || err == nil {
		t.Fatal(id, err)
	}
	select {
	case <-wake:
		t.Fatal("failed append published success")
	default:
	}
	appendN(t, s, convB, 0, 1)
	select {
	case <-wake:
		t.Fatal("foreign append notified")
	default:
	}
	appendN(t, s, convA, 0, 20)
	testForwardWait(t, wake)
	select {
	case <-wake:
		t.Fatal("notifications did not coalesce")
	default:
	}
	c := s.convs[convA]
	seg := c.seg
	if c.segBytes >= s.maxSegmentBytes {
		seg++
	}
	path := filepath.Join(historyDir(s.instanceDir, convA), segmentName(seg))
	if seg == c.seg {
		if err := os.Rename(path, path+"-saved"); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if id, err := s.Append(convA, "write failure", json.RawMessage(`{}`), testTS); id != 0 || err == nil {
		t.Fatal(id, err)
	}
	select {
	case <-wake:
		t.Fatal("failed storage write published success")
	default:
	}
}

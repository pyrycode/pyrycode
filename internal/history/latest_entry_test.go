package history

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLatestEntryIDRecovery(t *testing.T) {
	t.Parallel()
	for _, tail := range []struct{ name, data string }{
		{"normal", ""}, {"empty", ""}, {"header", segmentHeaderLine}, {"incomplete", `{"version":`}, {"stacked", ""},
	} {
		t.Run(tail.name, func(t *testing.T) {
			root := t.TempDir()
			s := newStore(root, testSegmentBytes)
			if id, err := s.LatestEntryID(convA); err != nil || id != 0 {
				t.Fatalf("missing = %d, %v", id, err)
			}
			entries := appendN(t, s, convA, 0, 40)
			want := entries[len(entries)-1].ID
			if id, err := s.LatestEntryID(convA); err != nil || id != want {
				t.Fatalf("after append = %d, %v", id, err)
			}
			if _, opened := s.readStats(); opened != 0 {
				t.Fatalf("initialized append cursor opened %d segments", opened)
			}
			segs, err := listSegments(historyDir(root, convA))
			if err != nil {
				t.Fatal(err)
			}
			if tail.name == "stacked" {
				for k, data := range []string{"", segmentHeaderLine, `{"version":`} {
					if err := os.WriteFile(filepath.Join(historyDir(root, convA), segmentName(segs[len(segs)-1].num+uint64(k)+1)), []byte(data), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			} else if tail.name != "normal" {
				if err := os.WriteFile(filepath.Join(historyDir(root, convA), segmentName(segs[len(segs)-1].num+1)), []byte(tail.data), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			reader := newStore(root, testSegmentBytes)
			if id, err := reader.LatestEntryID(convA); err != nil || id != want {
				t.Fatalf("recovered = %d, %v; want %d", id, err, want)
			}
			_, opened := reader.readStats()
			wantOpened := 1
			if tail.name != "normal" {
				wantOpened = 2
			}
			if tail.name == "stacked" {
				wantOpened = 4
			}
			if opened != wantOpened {
				t.Fatalf("opened %d segments, want %d", opened, wantOpened)
			}
			reader.resetReadStats()
			for range 3 {
				if id, err := reader.LatestEntryID(convA); err != nil || id != want {
					t.Fatalf("cached = %d, %v", id, err)
				}
			}
			if _, opened := reader.readStats(); opened != 0 {
				t.Fatalf("cached reads opened %d segments", opened)
			}
			next := appendN(t, reader, convA, 40, 1)[0].ID
			if next != want+1 {
				t.Fatalf("append = %d, want %d", next, want+1)
			}
			if id, err := reader.LatestEntryID(convA); err != nil || id != next {
				t.Fatalf("after append = %d, %v", id, err)
			}
		})
	}
}

func TestLatestEntryIDEmptyAndTorn(t *testing.T) {
	t.Parallel()
	for _, data := range []string{"", segmentHeaderLine, `{"version":`} {
		root := t.TempDir()
		dir := historyDir(root, convA)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, segmentName(1)), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		s := newStore(root, testSegmentBytes)
		if id, err := s.LatestEntryID(convA); err != nil || id != 0 {
			t.Fatalf("empty = %d, %v", id, err)
		}
		if id := appendN(t, s, convA, 0, 1)[0].ID; id != 1 {
			t.Fatalf("first append = %d", id)
		}
	}
	root := t.TempDir()
	s := newStore(root, testSegmentBytes)
	appendN(t, s, convA, 0, 1)
	f, err := os.OpenFile(filepath.Join(historyDir(root, convA), segmentName(1)), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"id":2`); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	s = newStore(root, testSegmentBytes)
	if id, err := s.LatestEntryID(convA); err != nil || id != 1 {
		t.Fatalf("torn = %d, %v", id, err)
	}
	if id := appendN(t, s, convA, 1, 1)[0].ID; id != 2 {
		t.Fatalf("append past torn = %d", id)
	}
}

func TestLatestEntryIDChecksEveryCall(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := newStore(root, testSegmentBytes)
	appendN(t, s, convA, 0, 1)
	if _, err := s.LatestEntryID("../bad"); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("invalid id: %v", err)
	}
	dir := historyDir(root, convA)
	if err := os.Rename(dir, dir+"-saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), dir); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LatestEntryID(convA); !errors.Is(err, ErrNotContained) {
		t.Fatalf("cached containment: %v", err)
	}
}

func TestLatestEntryIDAfterFailedAppend(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := newStore(root, testSegmentBytes)
	appendN(t, s, convA, 0, 1)
	// Force a roll, then make the next fresh segment unavailable to the writer.
	s.mu.Lock()
	next := s.convs[convA].seg + 1
	s.convs[convA].segBytes = s.maxSegmentBytes
	s.mu.Unlock()
	blocked := filepath.Join(historyDir(root, convA), segmentName(next))
	if err := os.Symlink(filepath.Join(t.TempDir(), "outside"), blocked); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(convA, "turn_end", []byte(`{}`), testTS); err == nil {
		t.Fatal("append unexpectedly succeeded")
	}
	if id, err := s.LatestEntryID(convA); err != nil || id != 1 {
		t.Fatalf("after failed append = %d, %v", id, err)
	}
	if err := os.Remove(blocked); err != nil {
		t.Fatal(err)
	}
	if id := appendN(t, s, convA, 1, 1)[0].ID; id != 2 {
		t.Fatalf("next append = %d, want 2", id)
	}
}

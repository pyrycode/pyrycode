package history

import (
	"errors"
	"os"
	"sync"
	"testing"
)

func TestCachedLatestEntryID(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := newStore(root, testSegmentBytes)
	check := func(store *Store, want uint64, known bool) {
		t.Helper()
		store.resetReadStats()
		got, ok, err := store.CachedLatestEntryID(convA)
		if err != nil || got != want || ok != known {
			t.Fatalf("cached latest = %d, %t, %v; want %d, %t", got, ok, err, want, known)
		}
		if bytes, opens := store.readStats(); bytes != 0 || opens != 0 {
			t.Fatalf("metadata read segments: %d bytes, %d opens", bytes, opens)
		}
	}
	check(s, 0, false)
	if _, err := s.EnsureLogDir(convA); err != nil {
		t.Fatal(err)
	}
	check(s, 0, false)
	if _, err := s.LatestEntryID(convA); err != nil {
		t.Fatal(err)
	}
	check(s, 0, true)
	entries := appendN(t, s, convA, 0, 40)
	check(s, entries[len(entries)-1].ID, true)
	cold := newStore(root, testSegmentBytes)
	check(cold, 0, false)
	if cold.convs[convA] != nil {
		t.Fatal("cold lookup initialized cursor")
	}
	if _, err := cold.LatestEntryID(convA); err != nil {
		t.Fatal(err)
	}
	check(cold, entries[len(entries)-1].ID, true)
	if _, _, err := s.CachedLatestEntryID("../bad"); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("invalid id: %v", err)
	}
	var unavailable *Store
	if _, ok, err := unavailable.CachedLatestEntryID(convA); ok || err == nil {
		t.Fatalf("nil history = %t, %v", ok, err)
	}
	dir := historyDir(root, convA)
	if err := os.Rename(dir, dir+"-saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), dir); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.CachedLatestEntryID(convA); ok || !errors.Is(err, ErrNotContained) {
		t.Fatalf("redirected warm cursor = %t, %v", ok, err)
	}
}

func TestCachedLatestEntryIDConcurrent(t *testing.T) {
	t.Parallel()
	s := newStore(t.TempDir(), testSegmentBytes)
	appendN(t, s, convA, 0, 1)
	var readers sync.WaitGroup
	for range 4 {
		readers.Go(func() {
			var previous uint64
			for range 100 {
				got, known, err := s.CachedLatestEntryID(convA)
				if err != nil || !known || got < previous || got > 21 {
					t.Errorf("concurrent cursor = %d, %t, %v", got, known, err)
					return
				}
				previous = got
			}
		})
	}
	appendN(t, s, convA, 1, 20)
	readers.Wait()
}

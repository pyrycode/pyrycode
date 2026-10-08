package history

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLatestDisplayableEntryIDTypes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		typ     string
		exclude bool
	}{
		{"turn_state", true}, {"stall", true}, {"api_retry", true},
		{"compacting", true}, {"session_transition", true},
		{"assistant_delta", false}, {"turn_end", false}, {"tool_start", false},
		{"user_message", false}, {"unknown_future_type", false}, {"", false},
		{"TURN_STATE", false},
	} {
		t.Run(tc.typ, func(t *testing.T) {
			s := newStore(t.TempDir(), testSegmentBytes)
			first := appendN(t, s, convA, 0, 1)[0].ID
			last, err := s.Append(convA, tc.typ, []byte(`null`), testTS)
			if err != nil {
				t.Fatal(err)
			}
			want := last
			if tc.exclude {
				want = first
			}
			for _, reader := range []*Store{s, newStore(s.instanceDir, testSegmentBytes)} {
				if got, err := reader.LatestDisplayableEntryID(convA); err != nil || got != want {
					t.Fatalf("displayable = %d, %v; want %d", got, err, want)
				}
			}
			if got, err := s.LatestEntryID(convA); err != nil || got != last {
				t.Fatalf("raw = %d, %v; want %d", got, err, last)
			}
		})
	}
}

func TestLatestDisplayableEntryIDRecovery(t *testing.T) {
	t.Parallel()
	for _, content := range []bool{false, true} {
		for _, tail := range []struct{ name, data string }{
			{"normal", ""}, {"empty", ""}, {"header", segmentHeaderLine},
			{"incomplete header", `{"version":`},
			{"torn entry", segmentHeaderLine + `{"id":999`},
		} {
			t.Run(tail.name+map[bool]string{false: " status-only", true: " content"}[content], func(t *testing.T) {
				root := t.TempDir()
				s := newStore(root, testSegmentBytes)
				var want, raw uint64
				if content {
					want = appendN(t, s, convA, 0, 1)[0].ID
				}
				statuses := []string{"turn_state", "stall", "api_retry", "compacting", "session_transition"}
				for i := range 25 {
					var err error
					raw, err = s.Append(convA, statuses[i%len(statuses)], []byte(`[]`), testTS)
					if err != nil {
						t.Fatal(err)
					}
				}
				segs, err := listSegments(historyDir(root, convA))
				if err != nil || len(segs) < 3 {
					t.Fatalf("segments = %d, %v", len(segs), err)
				}
				if tail.name != "normal" {
					if err := os.WriteFile(filepath.Join(historyDir(root, convA), segmentName(segs[len(segs)-1].num+1)), []byte(tail.data), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				// Check existing logs through a fresh store, independent of Append's cache.
				s = newStore(root, testSegmentBytes)
				if got, err := s.LatestDisplayableEntryID(convA); err != nil || got != want {
					t.Fatalf("recovered = %d, %v; want %d", got, err, want)
				}
				if got, err := s.LatestEntryID(convA); err != nil || got != raw {
					t.Fatalf("recovered raw = %d, %v; want %d", got, err, raw)
				}
				if _, opened := s.readStats(); opened < len(segs) {
					t.Fatalf("recovery opened %d segments; expected traversal of %d", opened, len(segs))
				}
				s.resetReadStats()
				for range 3 {
					if got, err := s.LatestDisplayableEntryID(convA); err != nil || got != want {
						t.Fatalf("cached = %d, %v; want %d", got, err, want)
					}
				}
				var errStatus error
				raw, errStatus = s.Append(convA, "turn_state", []byte(`{}`), testTS)
				if errStatus != nil {
					t.Fatal(errStatus)
				}
				if got, err := s.LatestDisplayableEntryID(convA); err != nil || got != want {
					t.Fatalf("cached after status = %d, %v; want %d", got, err, want)
				}
				if _, opened := s.readStats(); opened != 0 {
					t.Fatalf("cached lookup opened %d segments", opened)
				}
				if got, err := s.LatestEntryID(convA); err != nil || got != raw {
					t.Fatalf("raw = %d, %v; want %d", got, err, raw)
				}
				page := walkAll(t, s, convA, 7)
				if uint64(len(page)) != raw || page[0].ID != raw || page[0].Type != "turn_state" {
					t.Fatalf("history lost status entries: %+v", page)
				}
				next := appendN(t, s, convA, 1, 1)[0].ID
				if next != raw+1 {
					t.Fatalf("next ID = %d; want %d", next, raw+1)
				}
				if got, err := s.LatestDisplayableEntryID(convA); err != nil || got != next {
					t.Fatalf("new content = %d, %v; want %d", got, err, next)
				}
				if _, err := s.Append(convA, "turn_state", []byte(`{}`), testTS); err != nil {
					t.Fatal(err)
				}
				if got, err := s.LatestDisplayableEntryID(convA); err != nil || got != next {
					t.Fatalf("status after content = %d, %v; want %d", got, err, next)
				}
			})
		}
	}
}

func TestLatestDisplayableEntryIDEmptyAndChecks(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := newStore(root, testSegmentBytes)
	if got, err := s.LatestDisplayableEntryID(convA); err != nil || got != 0 {
		t.Fatalf("missing = %d, %v", got, err)
	}
	if _, err := os.Stat(historyDir(root, convA)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing lookup created directory: %v", err)
	}
	if err := os.MkdirAll(historyDir(root, convA), 0o700); err != nil {
		t.Fatal(err)
	}
	if got, err := s.LatestDisplayableEntryID(convA); err != nil || got != 0 {
		t.Fatalf("empty = %d, %v", got, err)
	}
	appendN(t, s, convA, 0, 1)
	if got, err := s.LatestDisplayableEntryID(convB); err != nil || got != 0 {
		t.Fatalf("other conversation = %d, %v", got, err)
	}
	if _, err := s.LatestDisplayableEntryID("../bad"); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("invalid ID: %v", err)
	}
	if _, err := (*Store)(nil).LatestDisplayableEntryID(convA); err == nil {
		t.Fatal("nil store succeeded")
	}
	dir := historyDir(root, convA)
	if err := os.Rename(dir, dir+"-saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), dir); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LatestDisplayableEntryID(convA); !errors.Is(err, ErrNotContained) {
		t.Fatalf("warm containment: %v", err)
	}
}

func TestLatestDisplayableEntryIDAfterFailedAppend(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := newStore(root, testSegmentBytes)
	appendN(t, s, convA, 0, 1)
	if got, err := s.LatestDisplayableEntryID(convA); err != nil || got != 1 {
		t.Fatalf("initial = %d, %v", got, err)
	}
	s.mu.Lock()
	next := s.convs[convA].seg + 1
	s.convs[convA].segBytes = s.maxSegmentBytes
	s.mu.Unlock()
	blocked := filepath.Join(historyDir(root, convA), segmentName(next))
	if err := os.Symlink(filepath.Join(t.TempDir(), "outside"), blocked); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(convA, "unknown_future_type", []byte(`{}`), testTS); err == nil {
		t.Fatal("append unexpectedly succeeded")
	}
	s.resetReadStats()
	if got, err := s.LatestDisplayableEntryID(convA); err != nil || got != 1 {
		t.Fatalf("after failure = %d, %v", got, err)
	}
	if _, opened := s.readStats(); opened == 0 {
		t.Fatal("failed append did not trigger recovery")
	}
	if err := os.Remove(blocked); err != nil {
		t.Fatal(err)
	}
	if id := appendN(t, s, convA, 1, 1)[0].ID; id != 2 {
		t.Fatalf("next append = %d", id)
	}
}

func TestLatestDisplayableEntryIDRecoveryError(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := newStore(root, testSegmentBytes)
	appendN(t, s, convA, 0, 1)
	var raw uint64
	for range 20 {
		var err error
		raw, err = s.Append(convA, "turn_state", []byte(`{}`), testTS)
		if err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(historyDir(root, convA), segmentName(1))
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("garbage\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s = newStore(root, testSegmentBytes)
	if got, err := s.LatestEntryID(convA); err != nil || got != raw {
		t.Fatalf("raw query reached older corrupt segment: %d, %v", got, err)
	}
	for range 2 {
		if _, err := s.LatestDisplayableEntryID(convA); !errors.Is(err, ErrUnknownVersion) {
			t.Fatalf("filtered recovery error: %v", err)
		}
	}
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := s.LatestDisplayableEntryID(convA); err != nil || got != 1 {
		t.Fatalf("retry after repair = %d, %v", got, err)
	}
}

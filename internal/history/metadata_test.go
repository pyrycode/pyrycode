package history

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestMetadataRoundTrip(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dir := historyDir(root, convA)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Literal version-1 bytes, independent of the new encoder.
	legacy := []byte(segmentHeaderLine + `{"id":1,"type":"legacy","payload":{"shown":false,"session":"opaque<&>"},"ts":"2026-09-05T12:00:00Z"}` + "\n")
	legacyPath := filepath.Join(dir, segmentName(1))
	if err := os.WriteFile(legacyPath, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, segmentName(2)), []byte(segmentHeaderLine), 0o600); err != nil {
		t.Fatal(err)
	}
	s := newStore(root, testSegmentBytes)
	want := []Entry{{ID: 1, Type: "legacy", Payload: json.RawMessage(`{"shown":false,"session":"opaque<&>"}`), TS: testTS}}
	shown, hidden := true, false
	for _, session := range []*SessionProvenance{nil, {Kind: "none"}, {Kind: "claude", SessionID: "claude-session"}, {Kind: "codex", SessionID: "codex-session"}} {
		for _, visibility := range []*bool{nil, &shown, &hidden} {
			payload := json.RawMessage(`{"session":"payload-session","shown":"opaque<&>"}`)
			id, err := s.AppendWithMetadata(convA, "fact", payload, testTS, Metadata{Session: session, Shown: visibility})
			if err != nil || id != uint64(len(want)+1) {
				t.Fatalf("append = %d, %v", id, err)
			}
			want = append(want, Entry{ID: id, Type: "fact", Payload: payload, TS: testTS, Session: session, Shown: visibility})
		}
	}
	id, err := s.Append(convA, "legacy-api", []byte(`null`), testTS)
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, Entry{ID: id, Type: "legacy-api", Payload: json.RawMessage(`null`), TS: testTS})
	for _, reader := range []*Store{s, newStore(root, testSegmentBytes)} {
		got := walkAll(t, reader, convA, 2)
		if len(got) != len(want) {
			t.Fatalf("page count = %d, want %d", len(got), len(want))
		}
		for i, entry := range got {
			if expected := want[len(want)-1-i]; !reflect.DeepEqual(entry, expected) {
				t.Fatalf("entry = %#v, want %#v", entry, expected)
			}
		}
	}
	segs, err := listSegments(dir)
	if err != nil || len(segs) < 3 {
		t.Fatalf("fixture did not cross segments: %d, %v", len(segs), err)
	}
	for _, seg := range segs {
		data, err := os.ReadFile(filepath.Join(dir, seg.name))
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n"))[1:] {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(line, &fields); err != nil {
				t.Fatal(err)
			}
			var id uint64
			if err := json.Unmarshal(fields["id"], &id); err != nil {
				t.Fatal(err)
			}
			expected := want[id-1]
			for key, absent := range map[string]bool{"session": expected.Session == nil, "shown": expected.Shown == nil} {
				value, present := fields[key]
				if present == absent || (present && string(value) == "null") {
					t.Fatalf("id %d: %s = %s, present %v, absent %v", id, key, value, present, absent)
				}
			}
			if expected.Session != nil && expected.Session.Kind == "none" && string(fields["session"]) != `{"kind":"none"}` {
				t.Fatalf("explicit no session = %s", fields["session"])
			}
			if expected.Session != nil && expected.Session.Kind != "none" {
				wantSession := fmt.Sprintf(`{"kind":%q,"session_id":%q}`, expected.Session.Kind, expected.Session.SessionID)
				if string(fields["session"]) != wantSession {
					t.Fatalf("known session = %s, want %s", fields["session"], wantSession)
				}
			}
		}
	}
	if got, err := os.ReadFile(legacyPath); err != nil || !bytes.Equal(got, legacy) {
		t.Fatalf("legacy bytes changed: %v", err)
	}
}

func TestMetadataVisibility(t *testing.T) {
	t.Parallel()
	shown, hidden := true, false
	for _, typ := range []string{"turn_state", "stall", "api_retry", "compacting", "session_transition", "assistant_delta", "unknown", ""} {
		for _, visibility := range []*bool{nil, &shown, &hidden} {
			t.Run(fmt.Sprintf("%s/%v", typ, visibility), func(t *testing.T) {
				root := t.TempDir()
				s := newStore(root, testSegmentBytes)
				appendN(t, s, convA, 0, 1)
				if got, err := s.LatestDisplayableEntryID(convA); err != nil || got != 1 {
					t.Fatalf("initial watermark = %d, %v", got, err)
				}
				id, err := s.AppendWithMetadata(convA, typ, []byte(`{"shown":true}`), testTS, Metadata{Shown: visibility})
				if err != nil {
					t.Fatal(err)
				}
				counts := typ == "assistant_delta" || typ == "unknown" || typ == ""
				if visibility != nil {
					counts = *visibility
				}
				want := uint64(1)
				if counts {
					want = id
				}
				for _, reader := range []*Store{s, newStore(root, testSegmentBytes)} {
					if got, err := reader.LatestDisplayableEntryID(convA); err != nil || got != want {
						t.Fatalf("displayable = %d, %v; want %d", got, err, want)
					}
					if got, err := reader.LatestEntryID(convA); err != nil || got != id {
						t.Fatalf("raw = %d, %v", got, err)
					}
					if entries := walkAll(t, reader, convA, 1); len(entries) != 2 || entries[0].ID != id {
						t.Fatalf("paging omitted entry: %#v", entries)
					}
				}
			})
		}
	}
}

func TestMetadataVisibilityRecoveryAcrossSegments(t *testing.T) {
	t.Parallel()
	for _, visible := range []bool{false, true} {
		t.Run(fmt.Sprint(visible), func(t *testing.T) {
			root := t.TempDir()
			s := newStore(root, testSegmentBytes)
			var want uint64
			if visible {
				appendN(t, s, convA, 0, 1)
				shown := true
				var err error
				want, err = s.AppendWithMetadata(convA, "session_transition", []byte(`null`), testTS, Metadata{Shown: &shown})
				if err != nil {
					t.Fatal(err)
				}
			}
			hidden := false
			var last uint64
			for i := range 25 {
				var err error
				if i%2 == 0 {
					last, err = s.AppendWithMetadata(convA, "future_fact", []byte(`{}`), testTS, Metadata{Shown: &hidden})
				} else {
					last, err = s.Append(convA, "stall", []byte(`{}`), testTS)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			for _, reader := range []*Store{s, newStore(root, testSegmentBytes)} {
				if got, err := reader.LatestDisplayableEntryID(convA); err != nil || got != want {
					t.Fatalf("recovery = %d, %v; want %d", got, err, want)
				}
				reader.resetReadStats()
				if got, err := reader.LatestDisplayableEntryID(convA); err != nil || got != want {
					t.Fatalf("cache = %d, %v", got, err)
				}
				if _, opened := reader.readStats(); opened != 0 {
					t.Fatalf("warm lookup opened %d segments", opened)
				}
				if got, err := reader.LatestEntryID(convA); err != nil || got != last {
					t.Fatalf("raw = %d, %v", got, err)
				}
				if entries := walkAll(t, reader, convA, 3); uint64(len(entries)) != last {
					t.Fatalf("entries = %d, want %d", len(entries), last)
				}
			}
			if segs, err := listSegments(historyDir(root, convA)); err != nil || len(segs) < 3 {
				t.Fatalf("fixture segments = %d, %v", len(segs), err)
			}
		})
	}
}

func testHistorySnapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := make(map[string]string)
	for _, file := range files {
		data, err := os.ReadFile(filepath.Join(dir, file.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out[file.Name()] = string(data)
	}
	return out
}

func TestAppendIDExhaustion(t *testing.T) {
	t.Parallel()
	for _, metadata := range []bool{false, true} {
		t.Run(fmt.Sprint(metadata), func(t *testing.T) {
			root := t.TempDir()
			dir := historyDir(root, convA)
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			seed := fmt.Sprintf("%s{\"id\":%d,\"type\":\"reply\",\"payload\":null,\"ts\":\"2026-09-05T12:00:00Z\"}\n", segmentHeaderLine, MaxEntryID-1)
			if err := os.WriteFile(filepath.Join(dir, segmentName(1)), []byte(seed), 0o600); err != nil {
				t.Fatal(err)
			}
			s := newStore(root, testSegmentBytes)
			appendEntry := func(store *Store, withMetadata bool) (uint64, error) {
				if withMetadata {
					hidden := false
					return store.AppendWithMetadata(convA, "reply", []byte(`null`), testTS, Metadata{Shown: &hidden})
				}
				return store.Append(convA, "turn_state", []byte(`null`), testTS)
			}
			if id, err := appendEntry(s, metadata); err != nil || id != MaxEntryID {
				t.Fatalf("last append = %d, %v; want %d", id, err, MaxEntryID)
			}
			before := testHistorySnapshot(t, dir)
			for _, reader := range []*Store{s, newStore(root, testSegmentBytes)} {
				if got, err := reader.LatestDisplayableEntryID(convA); err != nil || got != MaxEntryID-1 {
					t.Fatalf("initial displayable = %d, %v", got, err)
				}
				for range 2 {
					for _, path := range []bool{false, true} {
						if id, err := appendEntry(reader, path); id != 0 || !errors.Is(err, ErrIDExhausted) {
							t.Fatalf("exhausted append = %d, %v", id, err)
						}
					}
				}
				if got, err := reader.LatestEntryID(convA); err != nil || got != MaxEntryID {
					t.Fatalf("raw = %d, %v", got, err)
				}
				if got, err := reader.LatestDisplayableEntryID(convA); err != nil || got != MaxEntryID-1 {
					t.Fatalf("displayable = %d, %v", got, err)
				}
				if got := walkAll(t, reader, convA, 1); len(got) != 2 || got[0].ID != MaxEntryID {
					t.Fatalf("boundary page = %#v", got)
				}
				if after := testHistorySnapshot(t, dir); !reflect.DeepEqual(after, before) {
					t.Fatal("exhausted append modified segments")
				}
			}
			if id, err := s.Append(convB, "reply", []byte(`null`), testTS); err != nil || id != 1 {
				t.Fatalf("independent conversation = %d, %v", id, err)
			}
		})
	}
}

func TestRecoveredOversizedIDRefusesAppend(t *testing.T) {
	t.Parallel()
	for _, last := range []uint64{MaxEntryID + 1, MaxEntryID + 2, math.MaxUint64} {
		t.Run(fmt.Sprint(last), func(t *testing.T) {
			root := t.TempDir()
			dir := historyDir(root, convA)
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			seed := fmt.Sprintf("%s{\"id\":%d,\"type\":\"reply\",\"payload\":null,\"ts\":\"2026-09-05T12:00:00Z\"}\n", segmentHeaderLine, last)
			if err := os.WriteFile(filepath.Join(dir, segmentName(1)), []byte(seed), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, segmentName(2)), []byte(`{"version":`), 0o600); err != nil {
				t.Fatal(err)
			}
			before := testHistorySnapshot(t, dir)
			for range 2 {
				s := newStore(root, testSegmentBytes)
				if id, err := s.Append(convA, "reply", []byte(`null`), testTS); id != 0 || !errors.Is(err, ErrIDExhausted) {
					t.Fatalf("legacy append = %d, %v", id, err)
				}
				if id, err := s.AppendWithMetadata(convA, "reply", []byte(`null`), testTS, Metadata{}); id != 0 || !errors.Is(err, ErrIDExhausted) {
					t.Fatalf("metadata append = %d, %v", id, err)
				}
				if got, err := s.LatestEntryID(convA); err != nil || got != last {
					t.Fatalf("raw = %d, %v; want %d", got, err, last)
				}
				if got, err := s.LatestDisplayableEntryID(convA); err != nil || got != last {
					t.Fatalf("displayable = %d, %v; want %d", got, err, last)
				}
				if entries := walkAll(t, s, convA, 1); len(entries) != 1 || entries[0].ID != last {
					t.Fatalf("recovered page = %#v", entries)
				}
				if after := testHistorySnapshot(t, dir); !reflect.DeepEqual(after, before) {
					t.Fatal("recovered exhausted log changed")
				}
			}
		})
	}
}

func TestAppendMetadataValidation(t *testing.T) {
	t.Parallel()
	for _, session := range []SessionProvenance{{}, {Kind: "other", SessionID: "secret-session"}, {Kind: "claude"}, {Kind: "codex"}, {Kind: "none", SessionID: "secret-session"}} {
		t.Run(fmt.Sprintf("%s/%s", session.Kind, session.SessionID), func(t *testing.T) {
			root := t.TempDir()
			s := New(root)
			id, err := s.AppendWithMetadata(convA, "secret-type", []byte(`"secret-payload"`), testTS, Metadata{Session: &session})
			if id != 0 || !errors.Is(err, ErrInvalidMetadata) {
				t.Fatalf("invalid metadata = %d, %v", id, err)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatalf("error disclosed metadata: %v", err)
			}
			if _, err := os.Stat(historyDir(root, convA)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid metadata created directory: %v", err)
			}
		})
	}
}

func TestConcurrentMixedAppendPaths(t *testing.T) {
	t.Parallel()
	s := New(t.TempDir())
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var err error
			if i%2 == 0 {
				_, err = s.Append(convA, "legacy", []byte(`null`), testTS)
			} else {
				_, err = s.AppendWithMetadata(convA, "fact", []byte(`null`), testTS, Metadata{Session: &SessionProvenance{Kind: "none"}})
			}
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	got := walkAll(t, s, convA, 4)
	if len(got) != 20 {
		t.Fatalf("entries = %d", len(got))
	}
	for i, entry := range got {
		if entry.ID != uint64(20-i) {
			t.Fatalf("id = %d at position %d", entry.ID, i)
		}
	}
}

func TestAppendMetadataRecoveryAndContainment(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := newStore(root, testSegmentBytes)
	shown, hidden := true, false
	if id, err := s.AppendWithMetadata(convA, "stall", []byte(`null`), testTS, Metadata{Shown: &shown}); err != nil || id != 1 {
		t.Fatalf("initial append = %d, %v", id, err)
	}
	dir := historyDir(root, convA)
	path := filepath.Join(dir, segmentName(1))
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(original, []byte(`{"id":2,"shown":true`)...), 0o600); err != nil {
		t.Fatal(err)
	}
	s = newStore(root, testSegmentBytes)
	if id, err := s.AppendWithMetadata(convA, "reply", []byte(`null`), testTS, Metadata{Session: &SessionProvenance{Kind: "none"}, Shown: &hidden}); err != nil || id != 2 {
		t.Fatalf("append after torn tail = %d, %v", id, err)
	}
	if segs, err := listSegments(dir); err != nil || len(segs) != 2 {
		t.Fatalf("did not roll past torn tail: %d, %v", len(segs), err)
	}
	if got := walkAll(t, s, convA, 1); len(got) != 2 || got[0].ID != 2 || got[1].ID != 1 {
		t.Fatalf("recovered page = %#v", got)
	}
	if got, err := s.LatestDisplayableEntryID(convA); err != nil || got != 1 {
		t.Fatalf("recovered visibility = %d, %v", got, err)
	}
	if err := os.Rename(dir, dir+"-saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), dir); err != nil {
		t.Fatal(err)
	}
	if id, err := s.AppendWithMetadata(convA, "reply", []byte(`null`), testTS, Metadata{}); id != 0 || !errors.Is(err, ErrNotContained) {
		t.Fatalf("warm containment = %d, %v", id, err)
	}
}

func TestAppendMetadataSizeBound(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := newStore(root, testSegmentBytes)
	metadata := Metadata{Session: &SessionProvenance{Kind: "claude", SessionID: strings.Repeat("x", testSegmentBytes)}}
	if id, err := s.AppendWithMetadata(convA, "reply", []byte(`null`), testTS, metadata); id != 0 || !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("oversized metadata = %d, %v", id, err)
	}
	if files := testHistorySnapshot(t, historyDir(root, convA)); len(files) != 0 {
		t.Fatalf("size refusal wrote files: %v", files)
	}
	if id, err := s.Append(convA, "reply", []byte(`null`), testTS); err != nil || id != 1 {
		t.Fatalf("size refusal consumed id: %d, %v", id, err)
	}
}

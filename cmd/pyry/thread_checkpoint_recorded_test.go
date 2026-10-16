package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/thread"
)

// The source exposes only the already-emitted prefix. Lookup supplies immutable
// content references; it never feeds entries into either fold.
type testRecordedCheckpointSource struct {
	entries []history.Entry
	lookups int
}

func (s *testRecordedCheckpointSource) Lookup(ids []uint64) ([]history.Entry, error) {
	s.lookups++
	entries := make([]history.Entry, len(ids))
	for i, id := range ids {
		if id == 0 || id > uint64(len(s.entries)) || s.entries[id-1].ID != id {
			return nil, fmt.Errorf("recorded checkpoint: unavailable entry %d", id)
		}
		entries[i] = s.entries[id-1]
	}
	return entries, nil
}

type testRecordedCheckpoint struct {
	items   []thread.Item
	state   thread.CheckpointState
	records []thread.CheckpointItem
}

func testRecordedCheckpointSnapshot(t *testing.T, f *thread.Fold) testRecordedCheckpoint {
	t.Helper()
	items, err := f.Render()
	if err != nil {
		t.Fatal(err)
	}
	state, records, err := f.ExportCheckpoint()
	if err != nil {
		t.Fatal(err)
	}
	return testRecordedCheckpoint{items, state, records}
}

func testRecordedCheckpointCompare(t *testing.T, f *thread.Fold, want testRecordedCheckpoint, cut, boundary int) {
	t.Helper()
	got := testRecordedCheckpointSnapshot(t, f)
	// DeepEqual compares RawMessage bytes directly, including whitespace/escaping;
	// marshaling rendered items here would conceal byte differences in Content.
	if !reflect.DeepEqual(got.items, want.items) {
		t.Fatalf("cut %d boundary %d: rendered items/Content differ", cut, boundary)
	}
	if !reflect.DeepEqual(got.records, want.records) {
		t.Fatalf("cut %d boundary %d: complete internal item metadata/recipes differ", cut, boundary)
	}
	if !reflect.DeepEqual(got.state, want.state) {
		t.Fatalf("cut %d boundary %d: exported continuation state differs", cut, boundary)
	}
}

func testRecordedCheckpointResume(t *testing.T, entries []history.Entry) {
	t.Helper()
	if len(entries) == 0 {
		t.Fatal("recording emitted no history")
	}
	// One uninterrupted production reference fold establishes every expected
	// boundary, including hidden facts and multiple entries emitted by one frame.
	source := &testRecordedCheckpointSource{}
	reference := thread.NewReference(testConvID, source)
	want := []testRecordedCheckpoint{testRecordedCheckpointSnapshot(t, reference)}
	for i := range entries {
		source.entries = entries[:i+1]
		if err := reference.Feed(entries[i : i+1]); err != nil {
			t.Fatal(err)
		}
		want = append(want, testRecordedCheckpointSnapshot(t, reference))
	}
	comparisons, cuts := 0, 0
	for cut := 0; cut <= len(entries); cut++ {
		t.Run(fmt.Sprintf("cut_%d", cut), func(t *testing.T) {
			// Each export part crosses JSON separately, as the shipped contract
			// requires. Neither part contains legacy inline history payloads.
			stateJSON, err := json.Marshal(want[cut].state)
			if err != nil {
				t.Fatal(err)
			}
			recordsJSON, err := json.Marshal(want[cut].records)
			if err != nil {
				t.Fatal(err)
			}
			var state thread.CheckpointState
			var records []thread.CheckpointItem
			if err := json.Unmarshal(stateJSON, &state); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(recordsJSON, &records); err != nil {
				t.Fatal(err)
			}
			restoredSource := &testRecordedCheckpointSource{entries: entries[:cut]}
			restored, err := thread.ImportCheckpoint(testConvID, restoredSource, state, records)
			if err != nil {
				t.Fatal(err)
			}
			if restoredSource.lookups != 0 {
				t.Fatal("import read history instead of restoring continuation state")
			}
			testRecordedCheckpointCompare(t, restored, want[cut], cut, cut)
			comparisons++
			for i := cut; i < len(entries); i++ {
				restoredSource.entries = entries[:i+1]
				if err := restored.Feed(entries[i : i+1]); err != nil {
					t.Fatal(err)
				}
				testRecordedCheckpointCompare(t, restored, want[i+1], cut, i+1)
				comparisons++
			}
			cuts++
		})
	}
	n := len(entries)
	if cuts != n+1 || comparisons != (n+1)*(n+2)/2 {
		t.Fatalf("incomplete proof: %d cuts, %d comparisons for %d entries", cuts, comparisons, n)
	}
	t.Logf("%d entries: %d cuts and %d immediate/suffix comparisons", n, cuts, comparisons)
}

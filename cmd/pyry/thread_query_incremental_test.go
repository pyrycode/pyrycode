package main

import (
	"context"
	"reflect"
	"slices"
	"testing"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/thread"
)

// The recordings are derived by TestThreadRecordedBaselines' existing helpers.
// Tail publications are compared with independently replayed indexes per entry.
func testRecordedQueryIndexes(t *testing.T, entries []history.Entry) {
	t.Helper()
	ctx := context.Background()
	id := conversations.ConversationID(testConvID)
	h, replay := history.New(t.TempDir()), history.New(t.TempDir())
	s := thread.NewStore(h)
	t.Cleanup(func() { _ = s.Shutdown() })
	if err := s.Load(ctx, id); err != nil {
		t.Fatal(err)
	}
	wait := func(store *thread.Store, version uint64) thread.Snapshot {
		t.Helper()
		var snap thread.Snapshot
		testShadowWait(t, func() bool {
			snap = store.Snapshot(id)
			return snap.State == thread.StateUsable && snap.Version == version
		})
		return snap
	}
	wait(s, 0)
	for _, e := range entries {
		for _, target := range []*history.Store{h, replay} {
			if _, err := target.AppendWithMetadata(id, e.Type, e.Payload, e.TS, history.Metadata{Session: e.Session, Shown: e.Shown}); err != nil {
				t.Fatal(err)
			}
		}
		snap := wait(s, e.ID)
		fresh := thread.NewStore(replay)
		if err := fresh.Load(ctx, id); err != nil {
			t.Fatal(err)
		}
		freshSnap := wait(fresh, e.ID)
		for _, after := range []uint64{0, 1, e.ID / 2, e.ID, e.ID + 1} {
			for mode := range 3 {
				query := func(store *thread.Store, epoch string) (thread.QueryResult, error) {
					if mode == 0 {
						return store.CatchUp(ctx, id, epoch, after, 3)
					}
					if mode == 1 {
						return store.HistoryPage(ctx, id, after, 3)
					}
					return store.NewestWindow(ctx, id, 3)
				}
				got, err := query(s, snap.Epoch)
				want, wantErr := query(fresh, freshSnap.Epoch)
				if got.Epoch != snap.Epoch || want.Epoch != freshSnap.Epoch {
					t.Fatal("query epoch differs from captured publication")
				}
				// Independent Store lifetimes generate independent epoch tokens.
				want.Epoch = got.Epoch
				less := func(a, b thread.Item) int {
					if a.ID < b.ID {
						return -1
					}
					if a.ID > b.ID {
						return 1
					}
					return 0
				}
				slices.SortFunc(got.Items, less)
				slices.SortFunc(want.Items, less)
				if err != wantErr || !reflect.DeepEqual(got, want) {
					t.Fatalf("entry %d mode %d after %d: query/replay differ: %#v / %#v (%v / %v)", e.ID, mode, after, got, want, err, wantErr)
				}
			}
		}
		if err := fresh.Shutdown(); err != nil {
			t.Fatal(err)
		}
	}
}

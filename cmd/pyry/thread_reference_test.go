package main

import (
	"context"
	"reflect"
	"testing"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/thread"
)

// Replay each recorded partition through production replay followed by tailing.
func testRecordedReferencePartition(t *testing.T, entries []history.Entry, split int) {
	t.Helper()
	h := history.New(t.TempDir())
	id := conversations.ConversationID(testConvID)
	appendEntries := func(entries []history.Entry) {
		for _, e := range entries {
			if _, err := h.AppendWithMetadata(id, e.Type, e.Payload, e.TS, history.Metadata{Session: e.Session, Shown: e.Shown}); err != nil {
				t.Fatal(err)
			}
		}
	}
	appendEntries(entries[:split])
	s := thread.NewStore(h)
	defer s.Shutdown()
	if err := s.Load(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	check := func(entries []history.Entry) {
		t.Helper()
		legacy := thread.New(testConvID)
		if err := legacy.Feed(entries); err != nil {
			t.Fatal(err)
		}
		testShadowWait(t, func() bool {
			return s.Status(id).State == thread.StateUsable && s.Status(id).Version == uint64(len(entries))
		})
		snap := s.Snapshot(id)
		if !reflect.DeepEqual(snap.Items, legacy.Items()) || snap.LastShownVersion != legacy.Observe().LastShownVersion {
			t.Fatalf("production recorded partition %d differs", split)
		}
	}
	check(entries[:split])
	appendEntries(entries[split:])
	check(entries)
}

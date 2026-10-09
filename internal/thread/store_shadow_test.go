package thread

import (
	"context"
	"testing"
)

func TestStorePrivateActiveWork(t *testing.T) {
	t.Parallel()
	s, h := testThreadStore(t)
	entries := testStoreAppend(t, h, testStoreA, testChild(1, "tool_use", "unresolved-parent", `,"tool_use_id":"child","name":"Read"`))
	if err := s.Load(context.Background(), testStoreA); err != nil {
		t.Fatal(err)
	}
	snapshot := testStoreWait(t, s, testStoreA, StateUsable, 1)
	if len(snapshot.Items) != 0 || !snapshot.Active {
		t.Fatalf("private activity lost: %#v", snapshot)
	}
	entries = append(entries, testStoreAppend(t, h, testStoreA, testChild(2, "turn_end", "unresolved-parent", `,"stop_reason":"end_turn"`))...)
	snapshot = testStoreWait(t, s, testStoreA, StateUsable, 2)
	if snapshot.Active {
		t.Fatal("ended private work remains active")
	}
	testStoreEqual(t, snapshot, testStoreA, entries)
}

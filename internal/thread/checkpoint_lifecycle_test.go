package thread

import (
	"reflect"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
)

func testRestoreFold(t *testing.T, f *Fold) *Fold {
	t.Helper()
	d, rows, err := f.ExportCheckpoint()
	if err != nil {
		t.Fatal(err)
	}
	r, err := ImportCheckpoint("a", f.source, testCheckpointJSON(t, d), testCheckpointJSON(t, rows))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestCheckpointLifecycleAssertions(t *testing.T) {
	entries := testEndingEntries()
	f, _ := testReferenceFold(entries)
	for i, e := range entries {
		testFeed(t, f, e)
		f = testRestoreFold(t, f)
		if i < 3 {
			continue
		}
		var child Item
		for _, item := range f.Items() {
			if item.ID == 3 {
				child = item
			}
		}
		if child.ID != 3 || child.Rev != uint64(i+1) || f.touches[3] != uint64(i+1) {
			t.Fatal("child revision/touch", i, child, f.touches)
		}
		if child.Active != (i == 5) {
			t.Fatal("provisional closure/reopening lost", i, child)
		}
		if i >= 4 && messageText(child) != "unfinished late" {
			t.Fatal("inactive late text lost")
		}
	}
	entries = []history.Entry{testDurable(1, "agent_call_observed", `,"tool_call_id":"c","tool":"Agent"`), testAgentCall(2), testDurable(3, "background_task_observed", `,"task_id":"task"`), testDurable(4, "background_task_linked", `,"task_id":"task","tool_call_id":"c"`), testDurable(5, "agent_ended_with_session", `,"task_id":"task","tool_call_id":"c","cause":"capacity_eviction"`), testTask(6, "background_task_updated", `,"status":"completed"`)}
	f, _ = testReferenceFold(entries)
	testFeed(t, f, entries[:5]...)
	f = testRestoreFold(t, f)
	before := testAgent(t, f, "ended_with_session", false, 5)
	testFeed(t, f, entries[5])
	after := testAgent(t, f, "ended_with_session", false, 5)
	if after.Rev != 5 || f.touches[2] != 5 || !reflect.DeepEqual(before, after) {
		t.Fatal("late conflicting report revised genuine ending")
	}

	// Reuse the same durable IDs in a fresh producer lifetime.
	second := testCheckpointNextLifetime(entries)
	all := append(append([]history.Entry(nil), entries...), second...)
	testCheckpointResume(t, all, []int{0, 5, 6, 7, 8, 9, 10, 11, 12})
}

func testCheckpointNextLifetime(entries []history.Entry) []history.Entry {
	out := append([]history.Entry(nil), entries...)
	for i := range out {
		out[i].ID += uint64(len(entries))
		out[i].Payload = []byte(strings.ReplaceAll(string(out[i].Payload), "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"))
	}
	return out
}

func TestCheckpointSnapshotIsolation(t *testing.T) {
	entries := testEndingEntries()
	f, _ := testReferenceFold(entries)
	testFeed(t, f, entries[:4]...)
	d, rows, _ := f.ExportCheckpoint()
	r, err := ImportCheckpoint("a", f.source, d, rows)
	if err != nil {
		t.Fatal(err)
	}
	before, beforeRows, _ := r.ExportCheckpoint()
	for _, c := range d.Children {
		if c.Snapshot != nil {
			c.Snapshot.Base.Metadata.Status = "changed"
			if len(c.Snapshot.Base.Recipe.Text) > 0 {
				c.Snapshot.Base.Recipe.Text[0] = 999
			}
			for key, v := range c.Snapshot.Previous.Recipe.Fields {
				v.Entry = 999
				c.Snapshot.Previous.Recipe.Fields[key] = v
			}
		}
	}
	after, afterRows, _ := r.ExportCheckpoint()
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(beforeRows, afterRows) {
		t.Fatal("snapshot aliases document")
	}
	testFeed(t, f, entries[4:]...)
	after, afterRows, _ = r.ExportCheckpoint()
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(beforeRows, afterRows) {
		t.Fatal("snapshot aliases original fold")
	}
	testFeed(t, r, entries[4:]...)
	if !reflect.DeepEqual(f.Items(), r.Items()) {
		t.Fatal("independent snapshot continuation differs")
	}
}

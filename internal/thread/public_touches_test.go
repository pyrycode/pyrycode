package thread

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
)

// This full-walk oracle intentionally has no candidate cache or mutation hooks.
type testPublicTouches struct {
	touches           map[uint64]uint64
	published         map[uint64]bool
	shown, suppressed uint64
}

func (r *testPublicTouches) check(t *testing.T, f *Fold) {
	t.Helper()
	if r.touches == nil {
		r.touches, r.published = make(map[uint64]uint64), make(map[uint64]bool)
	}
	visible := make(map[uint64]bool)
	for index, item := range f.items {
		claimed, exists := f.messages[item.ID]
		if (f.children[index] != nil && (item.Parent == 0 || !visible[item.Parent])) || (exists && claimed < 0) {
			if r.touches[item.ID] != 0 {
				delete(r.touches, item.ID)
				r.suppressed = f.version
			}
			continue
		}
		visible[item.ID] = true
		if r.touches[item.ID] == 0 || item.Rev == f.version {
			r.touches[item.ID] = f.version
		}
		if item.Shown && (!r.published[item.ID] || index == f.appendedText) {
			r.shown = f.version
		}
		r.published[item.ID] = true
	}
	if !reflect.DeepEqual(visible, f.publicVisible) || len(f.affected) != 0 || len(f.affectedSet) != 0 {
		t.Fatalf("entry %d: retained public membership diverged or candidates leaked across entries", f.version)
	}
	if !reflect.DeepEqual(r.touches, f.touches) || !reflect.DeepEqual(r.published, f.published) || r.shown != f.lastShown || r.suppressed != f.suppressed {
		t.Fatalf("entry %d: touches got %v want %v; published got %v want %v; shown got %d want %d; suppressed got %d want %d", f.version, f.touches, r.touches, f.published, r.published, f.lastShown, r.shown, f.suppressed, r.suppressed)
	}
}

func TestPublicTouchesReference(t *testing.T) {
	t.Parallel()
	for _, entries := range [][]history.Entry{testFoldCorpus(1000), {
		testMain(1, "tool_use", `,"tool_use_id":"root","name":"Agent"`),
		testChild(2, "tool_use", "missing", `,"tool_use_id":"nested","name":"Task"`),
		testChild(3, "tool_use", "nested", `,"tool_use_id":"deep","name":"Agent"`),
		testChild(4, "assistant_delta", "deep", `,"text":" retained\n雪 "`),
		testChild(5, "tool_result", "root", `,"tool_use_id":"nested","is_error":false`),
		testChild(6, "tool_result", "missing", `,"tool_use_id":"nested","is_error":false`),
		testChild(7, "tool_result", "root", `,"tool_use_id":"nested","is_error":false`),
		testChild(8, "assistant_delta", "deep", `,"text":" suffix "`),
		testChild(9, "assistant_delta", "deep", `,"text":""`),
		testAcceptance(10, "phone"),
		testMessage(11),
		testSendOutcome(12, "send_delivered", `,"accepted_entry_id":10,"delivery_entry_id":11`, "delivered"),
		testAcceptance(13, "phone"),
		testSendOutcome(14, "send_dropped", `,"accepted_entry_id":13`, "removed"),
		testAcceptance(15, "phone"),
		testSendOutcome(16, "send_lost", `,"accepted_entry_id":15`, "daemon_restart"),
	}} {
		f := New("a")
		var reference testPublicTouches
		for _, e := range entries {
			testFeed(t, f, e)
			reference.check(t, f)
		}
	}
}

func TestPublicTouchAffectedWork(t *testing.T) {
	t.Parallel()
	var baseline []int
	for _, size := range []int{4, 16} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			f := New("a")
			var entries []history.Entry
			add := func(e history.Entry) history.Entry {
				e.ID = uint64(len(entries) + 1)
				entries = append(entries, e)
				return e
			}
			for i := 0; i < size; i++ {
				parent := fmt.Sprintf("other-%d", i)
				add(testMain(1, "tool_use", `,"tool_use_id":"`+parent+`","name":"Agent"`))
				add(testChild(1, "tool_use", parent, `,"tool_use_id":"`+parent+`-nested","name":"Task"`))
				add(testChild(1, "assistant_delta", parent+"-nested", `,"text":"settled child"`))
				add(testMain(1, "tool_result", `,"tool_use_id":"`+parent+`","is_error":false`))
				add(testMessage(1))
			}
			add(testMain(1, "tool_use", `,"tool_use_id":"root","name":"Agent"`))
			root := entries[len(entries)-1].ID
			add(testChild(1, "tool_use", "missing", `,"tool_use_id":"nested","name":"Task"`))
			add(testChild(1, "tool_use", "nested", `,"tool_use_id":"deep","name":"Agent"`))
			add(testChild(1, "assistant_delta", "deep", `,"text":"waiting descendant"`))
			add(testMain(1, "background_task_started", `,"task_id":"nested-task","tool_call_id":"nested"`))
			add(testMain(1, "assistant_delta", `,"text":"main"`))
			testFeed(t, f, entries...)
			fixed := f.Items()[:size*4]
			var reference testPublicTouches
			// Seed the oracle from the settled setup; measurements start below.
			reference.touches = make(map[uint64]uint64)
			reference.published = make(map[uint64]bool)
			for id, v := range f.touches {
				reference.touches[id] = v
			}
			for id, v := range f.published {
				reference.published[id] = v
			}
			reference.shown, reference.suppressed = f.lastShown, f.suppressed
			var counts []int
			feed := func(e history.Entry) {
				f.publicCounts = &publicWorkCounts{}
				f.childCounts = &childWorkCounts{}
				testFeed(t, f, add(e))
				c := f.childCounts
				counts = append(counts, f.publicCounts.work+c.registered+c.restored+c.baseCopies+c.expanded+c.edges+c.resolved)
				reference.check(t, f)
			}
			feed(testMain(1, "assistant_delta", `,"text":" append"`))
			feed(testChild(1, "tool_result", "root", `,"tool_use_id":"nested","is_error":false`))
			if f.touches[root+3] != f.version || f.items[len(f.items)-2].Rev == f.version {
				t.Fatal("late public descendant with older revision not exercised")
			}
			shown := f.lastShown
			feed(testChild(1, "tool_result", "missing", `,"tool_use_id":"nested","is_error":false`))
			if f.touches[root+1] != 0 || f.touches[root+3] != 0 || f.suppressed != f.version || f.lastShown != shown {
				t.Fatal("ancestor withdrawal did not suppress descendants while retaining watermark")
			}
			feed(testChild(1, "tool_result", "root", `,"tool_use_id":"nested","is_error":false`))
			if f.lastShown != shown {
				t.Fatal("reappearance raised watermark")
			}
			feed(testAcceptance(1, "phone"))
			accepted := f.version
			feed(testMessage(1))
			delivery := f.version
			feed(testSendOutcome(1, "send_delivered", fmt.Sprintf(`,"accepted_entry_id":%d,"delivery_entry_id":%d`, accepted, delivery), "delivered"))
			if f.touches[delivery] != 0 || f.suppressed != f.version || f.lastShown != delivery {
				t.Fatal("delivery suppression/watermark mismatch")
			}
			if !reflect.DeepEqual(fixed, f.Items()[:len(fixed)]) {
				t.Fatal("unrelated rows changed")
			}
			for i, count := range counts {
				if count == 0 {
					t.Fatalf("entry %d did no measured work", i)
				}
				if size == 16 && count >= 2*baseline[i] {
					t.Fatalf("entry %d visibility work grew >=2x with 4x unrelated items/trees: %d -> %d", i, baseline[i], count)
				}
			}
			if size == 4 {
				baseline = counts
			}
			t.Logf("visibility and discovery work: %v", counts)
			testMainReplay(t, entries)
		})
	}
}

func TestPublicTouchHistoricalPeakWork(t *testing.T) {
	t.Parallel()
	var baseline []int
	for _, size := range []int{4096, 16384} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			f := New("a")
			testFeed(t, f, testMain(1, "tool_use", `,"tool_use_id":"old-root","name":"Agent"`))
			for i := 0; i < size; i++ {
				testFeed(t, f, testChild(f.version+1, "tool_use", "old-root", fmt.Sprintf(`,"tool_use_id":"old-read-%d","name":"Read"`, i)))
			}
			f.publicCounts = &publicWorkCounts{}
			testFeed(t, f, testMain(f.version+1, "tool_result", `,"tool_use_id":"old-root","is_error":false`))
			if f.publicCounts.work < size || f.items[size].Active || f.items[size].Rev != f.version {
				t.Fatal("setup did not repair the large unrelated child tree")
			}
			// A map's yielded keys omit retained table scans during range/clear.
			// Dense candidate storage makes its traversal length countable.
			storage := reflect.ValueOf(f.affected)
			if storage.Kind() != reflect.Slice {
				t.Fatal("candidate traversal retains sparse map storage after the historical peak")
			}
			if storage.Len() != 0 || storage.Cap() < size {
				t.Fatal("setup did not retain a historical candidate-storage peak")
			}
			f.publicCounts = nil
			testFeed(t, f, testMain(f.version+1, "assistant_delta", `,"text":"fixed"`))
			index := len(f.items) - 1
			fixed := f.Items()[:index]
			var counts []int
			for i := 0; i < 4; i++ {
				f.publicCounts = &publicWorkCounts{}
				testFeed(t, f, testMain(f.version+1, "assistant_delta", `,"text":" append"`))
				counts = append(counts, f.publicCounts.work)
				if f.lastShown != f.version || f.touches[f.items[index].ID] != f.version || len(f.affected) != 0 {
					t.Fatal("fixed append lost its public touch or leaked candidates")
				}
			}
			// Isolate duplicate registration and post-join collection/cleanup.
			f.publicCounts = &publicWorkCounts{}
			f.version++
			f.items[index].Rev = f.version
			for i := 0; i < 3; i++ {
				f.markAffected(index)
			}
			if len(f.affected) != 1 {
				t.Fatal("repeated mutation hooks did not deduplicate the fixed item")
			}
			f.recordShown()
			counts = append(counts, f.publicCounts.work)
			if f.lastShown != f.version || f.touches[f.items[index].ID] != f.version || len(f.affected) != 0 || len(f.affectedSet) != 0 {
				t.Fatal("post-join processing lost the touch or retained candidates")
			}
			if !reflect.DeepEqual(fixed, f.Items()[:index]) {
				t.Fatal("fixed entry changed the unrelated repaired tree")
			}
			for i, count := range counts {
				if count == 0 || (baseline != nil && count >= 2*baseline[i]) {
					t.Fatalf("entry %d visibility work grew >=2x with a 4x historical peak: %v -> %v", i, baseline, counts)
				}
			}
			baseline = counts
			t.Logf("visibility work after %d repaired children: %v", size, counts)
		})
	}
}

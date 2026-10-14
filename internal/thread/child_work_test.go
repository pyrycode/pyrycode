package thread

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
)

func TestChildAffectedWork(t *testing.T) {
	t.Parallel()
	var baseline []childWorkCounts
	for _, size := range []int{4, 16} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			f := New("a")
			var entries []history.Entry
			add := func(e history.Entry) { e.ID = uint64(len(entries) + 1); entries = append(entries, e) }
			for i := 0; i < size; i++ {
				parent := fmt.Sprintf("other-%d", i)
				nested, shell := parent+"-nested", parent+"-shell"
				add(testMain(1, "tool_use", `,"tool_use_id":"`+parent+`","name":"Agent"`))
				add(testChild(1, "tool_use", parent, `,"tool_use_id":"`+nested+`","name":"Agent"`))
				add(testChild(1, "tool_use", nested, `,"tool_use_id":"`+shell+`","name":"Bash"`))
				add(testMain(1, "background_task_started", `,"task_id":"`+shell+`","tool_call_id":"`+shell+`"`))
				add(testMain(1, "tool_result", `,"tool_use_id":"`+parent+`","is_error":false`))
				add(testEntry(1, "message", `{"role":"user","text":"old history"}`))
			}
			root := uint64(len(entries) + 1)
			add(testMain(1, "tool_use", `,"tool_use_id":"root","name":"Agent"`))
			add(testChild(1, "tool_use", "root", `,"tool_use_id":"nested","name":"Agent"`))
			add(testChild(1, "assistant_delta", "nested", `,"text":"unfinished"`))
			add(testChild(1, "tool_use", "nested", `,"tool_use_id":"done","name":"Read"`))
			add(testChild(1, "tool_result", "nested", `,"tool_use_id":"done","is_error":false`))
			add(testChild(1, "tool_use", "nested", `,"tool_use_id":"pending","name":"Read"`))
			add(testChild(1, "assistant_delta", "nested", `,"text":"still running"`))
			testFeed(t, f, entries...)
			fixed := append([]Item(nil), f.Items()[:size*4]...)
			var counts []childWorkCounts
			feed := func(e history.Entry) {
				f.childCounts = &childWorkCounts{}
				add(e)
				testFeed(t, f, entries[len(entries)-1])
				counts = append(counts, *f.childCounts)
			}
			feed(testEntry(1, "banner", `{"text":"unrelated","level":"info"}`))
			feed(testMain(1, "assistant_delta", `,"text":"main only"`))
			feed(testMain(1, "tool_use", `,"tool_use_id":"main-read","name":"Read"`))
			feed(testMain(1, "tool_result", `,"tool_use_id":"main-read","is_error":false`))
			feed(testMain(1, "tool_use", `,"tool_use_id":"main-agent","name":"Agent"`))
			feed(testMain(1, "tool_result", `,"tool_use_id":"main-agent","is_error":false`))
			for _, count := range counts {
				if count != (childWorkCounts{}) {
					t.Fatalf("unrelated append did child work: %+v", count)
				}
			}
			feed(testMain(1, "tool_result", `,"tool_use_id":"root","is_error":false`))
			testChildItem(t, f, root+1, root, "agent", "running", false)
			testChildItem(t, f, root+5, root+1, "tool_call", "interrupted", false)
			testChildItem(t, f, root+6, root+1, "assistant_message", "interrupted", false)
			feed(testMain(1, "background_task_started", `,"task_id":"task","tool_call_id":"root"`))
			testChildItem(t, f, root+1, root, "agent", "running", true)
			testChildItem(t, f, root+5, root+1, "tool_call", "running", true)
			testChildItem(t, f, root+6, root+1, "assistant_message", "running", true)
			testChildItem(t, f, root+3, root+1, "tool_call", "done", false)
			feed(testMain(1, "background_task_updated", `,"task_id":"task","status":"completed","summary":"final"`))
			feed(testMain(1, "background_task_updated", `,"task_id":"task","summary":"enriched"`))
			ending := testChildItem(t, f, root+5, root+1, "tool_call", "interrupted", false)
			ancestor := testChildItem(t, f, root, 0, "agent", "finished", false)
			var content any
			if err := json.Unmarshal(ancestor.Content, &content); err != nil {
				t.Fatal(err)
			}
			testContent(t, ending, "parent_ending", content)
			feed(testChild(1, "assistant_delta", "nested", `,"text":"new child"`))
			feed(testChild(1, "tool_use", "nested", `,"tool_use_id":"new-read","name":"Read"`))
			if counts[len(counts)-1].registered == 0 {
				t.Fatal("registration bound was not exercised")
			}
			if !reflect.DeepEqual(fixed, f.Items()[:len(fixed)]) {
				t.Fatal("independent trees changed")
			}
			for _, count := range counts[6:] {
				if count.resolved == 0 || count.resolved > 8 || count.expanded != count.resolved || count.restored > 8 || count.baseCopies > 8 || count.registered > 4 || count.edges > 12 {
					t.Fatalf("unbounded or absent affected work: %+v", count)
				}
			}
			if baseline == nil {
				baseline = counts
			} else if !reflect.DeepEqual(baseline, counts) {
				t.Fatalf("work grew with unrelated history: small=%+v large=%+v", baseline, counts)
			}
			t.Logf("inspected child work: %+v", counts)
			full := testMainReplay(t, entries)
			if !reflect.DeepEqual(full.Items(), f.Items()) {
				t.Fatal("single-entry feeds differ")
			}
		})
	}
}

func TestChildTargetedRepair(t *testing.T) {
	t.Parallel()
	entries := []history.Entry{
		testMain(1, "tool_use", `,"tool_use_id":"a","name":"Agent"`),
		testMain(2, "tool_use", `,"tool_use_id":"b","name":"Agent"`),
		testMain(3, "tool_use", `,"tool_use_id":"nested","name":"Agent"`),
		testChild(4, "assistant_delta", "nested", `,"text":"descendant"`),
		testMain(5, "tool_result", `,"tool_use_id":"a","is_error":false`),
		testMain(6, "tool_result", `,"tool_use_id":"nested","is_error":false,"parent_tool_use_id":"a"`),
		testMain(7, "background_task_started", `,"task_id":"task","tool_call_id":"nested"`),
		testMain(8, "tool_result", `,"tool_use_id":"nested","is_error":false,"parent_tool_use_id":"b"`),
	}
	f := New("a")
	for i, e := range entries {
		testFeed(t, f, e)
		if i == 5 {
			item := testChildItem(t, f, 3, 1, "agent", "finished", false)
			if item.EndedOrder != 6 {
				t.Fatal("late parenting lost intrinsic final", item)
			}
			testChildItem(t, f, 4, 3, "assistant_message", "interrupted", false)
		}
	}
	testChildItem(t, f, 3, 2, "agent", "running", true)
	testChildItem(t, f, 4, 3, "assistant_message", "running", true)
	// The old edge must be removed even though its ending can still be enriched.
	f.childCounts = &childWorkCounts{}
	entries = append(entries,
		testMain(9, "background_task_started", `,"task_id":"old","tool_call_id":"a"`),
		testMain(10, "tool_result", `,"tool_use_id":"b","is_error":false`),
	)
	testFeed(t, f, entries[8])
	if *f.childCounts != (childWorkCounts{}) {
		t.Fatalf("old parent still visits moved descendants: %+v", f.childCounts)
	}
	testFeed(t, f, entries[9])
	testChildItem(t, f, 3, 2, "agent", "running", false)
	testChildItem(t, f, 4, 3, "assistant_message", "interrupted", false)
	full := testMainReplay(t, entries)
	if !reflect.DeepEqual(full.Items(), f.Items()) {
		t.Fatal("repair replay differs")
	}
}

func TestChildRejectedCandidatesCount(t *testing.T) {
	t.Parallel()
	f := New("a")
	testFeed(t, f,
		testChild(1, "tool_use", "self", `,"tool_use_id":"self","name":"Agent"`),
		testChild(2, "assistant_delta", "self", `,"text":"unresolved descendant"`),
	)
	f.childCounts = &childWorkCounts{}
	testFeed(t, f, testMain(3, "tool_result", `,"tool_use_id":"self","is_error":false`))
	if len(f.Items()) != 0 || f.childCounts.resolved != 2 || f.childCounts.expanded != 2 || f.childCounts.edges < 4 {
		t.Fatalf("rejected self edge or omitted descendant was not counted: %+v items=%+v", f.childCounts, f.Items())
	}
}

func TestChildRejectsShellParent(t *testing.T) {
	t.Parallel()
	t.Skip("blocked on #3345: ordinary shells are accepted as child parents")
	f := New("a")
	testFeed(t, f,
		testMain(1, "tool_use", `,"tool_use_id":"shell","name":"Bash"`),
		testChild(2, "assistant_delta", "shell", `,"text":"should remain unresolved"`),
	)
	if len(f.Items()) != 1 {
		t.Fatalf("ordinary shell accepted as parent: %+v", f.Items())
	}
}

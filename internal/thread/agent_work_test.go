package thread

import (
	"fmt"
	"reflect"
	"slices"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
)

func testAgentUnrelated(t *testing.T, f *Fold, size int) []Item {
	t.Helper()
	for i := 0; i < size; i++ {
		id, name := uint64(i*7+10), fmt.Sprintf("unrelated-%02d", i)
		testFeed(t, f,
			testMain(id, "tool_use", `,"tool_use_id":"`+name+`","name":"Agent"`),
			testMain(id+1, "background_task_started", `,"task_id":"`+name+`","tool_call_id":"`+name+`"`),
			testMain(id+2, "background_task_updated", `,"task_id":"`+name+`","status":"completed"`),
			testMain(id+3, "tool_result", `,"tool_use_id":"`+name+`","is_error":false`),
			testMain(id+4, "tool_result", `,"tool_use_id":"`+name+`-pending","is_error":false`),
			testMain(id+5, "background_task_started", `,"task_id":"`+name+`-pending","tool_call_id":"`+name+`-pending"`),
			testMain(id+6, "background_task_updated", `,"task_id":"`+name+`-pending","status":"completed"`),
		)
	}
	return f.Items()
}

func testAgentTaskIndex(t *testing.T, f *Fold) {
	t.Helper()
	for _, g := range f.agentGroups {
		seen := make(map[string]bool)
		for call, ids := range g.taskIDs {
			if !slices.IsSorted(ids) {
				t.Fatal("task order is not lexical", ids)
			}
			for _, id := range ids {
				if seen[id] || g.tasks[id] == nil || g.tasks[id].call != call {
					t.Fatal("stale or duplicate task ownership", call, id)
				}
				seen[id] = true
			}
		}
		for id, task := range g.tasks {
			if task.call != "" && !seen[id] {
				t.Fatal("missing task ownership", id)
			}
		}
	}
}

func TestAgentAffectedWork(t *testing.T) {
	t.Parallel()
	var baseline []int
	for _, size := range []int{4, 16} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			f := New("a")
			fixed := testAgentUnrelated(t, f, size)
			testFeed(t, f,
				testMain(1000, "tool_use", `,"tool_use_id":"root","name":"Agent"`),
				testMain(1001, "tool_use", `,"tool_use_id":"next","name":"Task"`),
				testMain(1002, "background_task_started", `,"task_id":"task","tool_call_id":"root"`),
			)
			entries := []history.Entry{
				testMain(2000, "tool_result", `,"tool_use_id":"root","is_error":false`),
				testMain(2001, "background_task_updated", `,"task_id":"task","status":"stopping"`),
				testMain(2002, "background_task_started", `,"task_id":"task","tool_call_id":"next"`),
				testEntry(2003, "background_task_roster", `{"tasks":[{"task_id":"task","tool_call_id":"root"},{"task_id":"z","tool_call_id":"root"},{"task_id":"bad","tool_call_id":"next","truncated_fields":["task_id"]}]}`),
			}
			var counts []int
			for i, e := range entries {
				f.agentCounts = &agentWorkCounts{}
				testFeed(t, f, e)
				testAgentTaskIndex(t, f)
				counts = append(counts, f.agentCounts.work)
				if f.agentCounts.work == 0 || f.agentCounts.promotions != 0 {
					t.Fatalf("missing work or unexpected promotion: %+v", f.agentCounts)
				}
				if !reflect.DeepEqual(fixed, f.Items()[:len(fixed)]) {
					t.Fatal("unrelated calls changed")
				}
				switch i {
				case 0:
					testMainItem(t, f, 1000, 2000, "agent", "running", true)
				case 1:
					testMainItem(t, f, 1000, 2001, "agent", "stopping", true)
				case 2:
					testMainItem(t, f, 1000, 2002, "agent", "finished", false)
					testMainItem(t, f, 1001, 2002, "agent", "stopping", true)
				case 3:
					testMainItem(t, f, 1000, 2003, "agent", "stopping", true)
					testMainItem(t, f, 1001, 2003, "agent", "running", true)
				}
			}
			if baseline == nil {
				baseline = counts
			} else {
				for i, count := range counts {
					if count >= 2*baseline[i] {
						t.Fatalf("entry %d work grew >=2x: small=%v large=%v", i, baseline, counts)
					}
				}
			}
			t.Logf("result/report/reassociation/roster candidates: %v", counts)
		})
	}
}

func TestAgentOwnershipOrder(t *testing.T) {
	t.Parallel()
	entries := []history.Entry{
		testMain(1, "tool_use", `,"tool_use_id":"root","name":"Agent"`),
		testMain(2, "tool_use", `,"tool_use_id":"next","name":"Agent"`),
		testEntry(3, "background_task_roster", `{"tasks":[{"task_id":"z","tool_call_id":"root"},{"task_id":"a","tool_call_id":"root"}]}`),
		testMain(4, "background_task_started", `,"task_id":"z","tool_call_id":"next"`),
		testEntry(5, "background_task_roster", `{"tasks":[{"task_id":"z","tool_call_id":"root"},{"task_id":"z","tool_call_id":"next"},{"task_id":"z","tool_call_id":"root"}]}`),
	}
	f := New("a")
	for i, e := range entries {
		testFeed(t, f, e)
		testAgentTaskIndex(t, f)
		if i >= 2 {
			want := "z"
			if i == 3 {
				want = "a"
			}
			testContent(t, f.Items()[0], "task_id", want)
		}
	}
	full := testMainReplay(t, entries)
	if !reflect.DeepEqual(full.Items(), f.Items()) {
		t.Fatal("single-entry ownership replay differs")
	}
}

func TestAgentPromotionWork(t *testing.T) {
	t.Parallel()
	var baseline int
	for _, size := range []int{4, 16} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			f := New("a")
			fixed := testAgentUnrelated(t, f, size)
			testFeed(t, f,
				testMain(1000, "tool_use", `,"tool_use_id":"shell","name":"Bash"`),
				testMain(1001, "background_task_started", `,"task_id":"shell-task","tool_call_id":"shell"`),
				testMain(1002, "background_task_started", `,"task_id":"a-shell-task","tool_call_id":"shell"`),
			)
			f.agentCounts = &agentWorkCounts{}
			testFeed(t, f, testDurable(2000, "background_task_outcome", `,"task_id":"shell-task","status":"completed"`))
			testAgentTaskIndex(t, f)
			if f.agentCounts.promotions != 1 || f.agentCounts.work < size {
				t.Fatalf("first migration was not counted: %+v", f.agentCounts)
			}
			f.agentCounts = &agentWorkCounts{}
			testFeed(t, f, testMain(2001, "background_task_updated", `,"task_id":"shell-task","status":"completed","summary":"mapped companion"`))
			testAgentTaskIndex(t, f)
			if f.agentCounts.promotions != 0 {
				t.Fatal("ordinary report repeated lifetime migration")
			}
			if baseline == 0 {
				baseline = f.agentCounts.work
			} else if f.agentCounts.work >= 2*baseline {
				t.Fatalf("post-promotion work grew: small=%d large=%d", baseline, f.agentCounts.work)
			}
			shell := testMainItem(t, f, 1000, 2001, "tool_call", "finished", false)
			if shell.EndedOrder != 2000 {
				t.Fatal("companion changed first ending", shell)
			}
			testContent(t, shell, "task_id", "shell-task")
			if !reflect.DeepEqual(fixed, f.Items()[:len(fixed)]) {
				t.Fatal("legacy agents were promoted")
			}
			t.Logf("post-promotion candidates: %d", f.agentCounts.work)
		})
	}
}

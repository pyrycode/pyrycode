package thread

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
)

func TestCheckpointEqualCompanionProvenance(t *testing.T) {
	for _, tool := range []string{"Agent", "Bash"} {
		for _, mappedFirst := range []bool{false, true} {
			for _, kind := range []string{"call", "denial", "task", "link"} {
				t.Run(fmt.Sprint(tool, mappedFirst, kind), func(t *testing.T) {
					link := testEntry(3, "ignored", `{}`)
					durable := testDurable(4, "agent_call_result", `,"turn_id":"t","tool_call_id":"c","tool_use_id":"c","is_error":false,"status":"completed","summary":"same <&>"`)
					mappedType, field := "tool_result", "result"
					switch kind {
					case "denial":
						durable = testDurable(4, "agent_call_denied", `,"turn_id":"t","tool_call_id":"c","tool_use_id":"c","status":"denied","summary":"same <&>"`)
						mappedType, field = "tool_denied", "denial"
					case "task":
						link = testMain(3, "background_task_started", `,"task_id":"task","tool_call_id":"c"`)
						durable = testDurable(4, "background_task_outcome", `,"task_id":"task","status":"completed","summary":"same <&>"`)
						mappedType, field = "background_task_updated", "task_report"
					case "link":
						durable = testDurable(4, "background_task_linked", `,"task_id":"task","tool_call_id":"c","summary":"same <&>"`)
						mappedType, field = "background_task_started", "task_link"
					}
					mapped := durable
					mapped.ID, mapped.Type = 5, mappedType
					first, second, mappedID := durable, mapped, uint64(5)
					if mappedFirst {
						first, second, mappedID = mapped, durable, 4
						first.ID, second.ID = 4, 5
					}
					entries := []history.Entry{testDurable(1, "agent_call_observed", `,"tool_call_id":"c","tool":"`+tool+`"`), testMain(2, "tool_use", `,"tool_use_id":"c","name":"`+tool+`"`), link, first, second}
					testCheckpointResume(t, entries, []int{0, 1, 2, 3, 4, 5})
					f, source := testReferenceFold(entries)
					testFeed(t, f, entries[:4]...)
					d, rows, _ := f.ExportCheckpoint()
					r, err := ImportCheckpoint("a", source, testCheckpointJSON(t, d), testCheckpointJSON(t, rows))
					if err != nil {
						t.Fatal(err)
					}
					before := r.Items()
					touch := r.touches[2]
					rev := r.items[0].Rev
					testFeed(t, r, second)
					if r.recipes[2].fields[field].entry != mappedID {
						t.Fatal("equal content lost mapped provenance")
					}
					if tool == "Bash" && !mappedFirst && (kind == "call" || kind == "denial") {
						// A durable-only shell result becomes foreground eligible only
						// when its mapped companion supplies the matching turn.
						if r.items[0].Rev != 5 || r.touches[2] != 5 {
							t.Fatal("foreground eligibility did not revise/touch shell")
						}
					} else if !reflect.DeepEqual(before, r.Items()) || r.touches[2] != touch || r.items[0].Rev != rev {
						t.Fatal("equal companion changed rendered item/revision/touch")
					}
					if (kind == "task" || (kind == "call" && tool == "Agent")) && r.Items()[0].EndedOrder != 4 {
						t.Fatal("terminal order changed")
					}
				})
			}
		}
	}
}

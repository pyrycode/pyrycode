package thread

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
)

func TestReferenceFinalReportAccounting(t *testing.T) {
	for _, turns := range []int{16, 64} {
		for _, padding := range []int{16, 4096} {
			var entries []history.Entry
			for turn := 0; turn < turns; turn++ {
				part := []history.Entry{
					testMain(1, "tool_use", `,"tool_use_id":"main","name":"Read"`),
					testMain(2, "tool_result", `,"tool_use_id":"main","is_error":false`),
					testMain(3, "tool_use", `,"tool_use_id":"agent","name":"Agent"`),
					testChild(4, "tool_use", "agent", `,"tool_use_id":"child","name":"Read"`),
					testChild(5, "tool_result", "agent", `,"tool_use_id":"child","is_error":false`),
					testMain(6, "background_task_started", `,"task_id":"task","tool_call_id":"agent"`),
					testMain(7, "background_task_updated", `,"task_id":"task","status":"completed"`),
					testMain(8, "turn_end", `,"stop_reason":"end_turn"`),
				}
				for _, e := range part {
					e.ID = uint64(len(entries) + 1)
					e.Session = testSource("claude", fmt.Sprintf("s%04d", turn))
					e.Payload = append(e.Payload[:len(e.Payload)-1], []byte(`,"padding":"`+strings.Repeat("x", padding)+`"}`)...)
					entries = append(entries, e)
				}
			}
			f, source := testReferenceFold(entries)
			for i := 0; i < len(entries); i += 8 {
				source.chunk = entries[i : i+8]
				testFeed(t, f, source.chunk...)
				source.remember(source.chunk)
			}
			source.chunk = nil
			if len(f.Items()) != 4*turns {
				t.Fatal("fixed-shape workload changed")
			}
			for _, item := range f.Items() {
				if item.Active {
					t.Fatal("workload contains unfinished items")
				}
			}
			bytes := testFinalReportBytes(f)
			if bytes[0] != turns*testFinalReportBytesForOne() {
				t.Fatal("final marker cost changed with history or payload size", bytes[0])
			}
			t.Logf("turns=%d entries=%d padding=%d logical bytes/turn: final-markers=%.0f metadata-recipes=%.0f active-unresolved=%.0f other-lifecycle=%.0f; source-cache=%d total; scalar sizes + string bytes; container/header/capacity overhead excluded, pointer targets counted once, strings counted per field (no backing-sharing deduction); fixed-width entry IDs, fixed-length source IDs", turns, len(entries), padding, float64(bytes[0])/float64(turns), float64(bytes[1])/float64(turns), float64(bytes[2])/float64(turns), float64(bytes[3])/float64(turns), source.bytes)
		}
	}
}

func testFinalReportBytesForOne() int {
	r := &agentReport{status: "completed", field: "task_report", mapped: true}
	h := &agentReports{markers: [4]*agentReport{r}}
	return testFinalReportBytes(h)[0]
}

// Logical accounting follows every retained field, including intrinsic/previous
// child state. It excludes the authoritative fixture source and Store snapshots.
func testFinalReportBytes(root any) [4]int {
	var totals [4]int
	seen := make(map[uintptr]bool)
	var walk func(reflect.Value, int)
	walk = func(v reflect.Value, category int) {
		if !v.IsValid() {
			return
		}
		if v.Type() == reflect.TypeFor[Item]() || v.Type() == reflect.TypeFor[contentRecipe]() {
			category = 1
		}
		if (v.Type() == reflect.TypeFor[agentCall]() && v.FieldByName("item").Int() < 0) || (v.Type() == reflect.TypeFor[agentTask]() && v.FieldByName("call").String() == "") {
			category = 2
		}
		if v.Type() == reflect.TypeFor[agentReports]() {
			walk(v.FieldByName("markers"), 0)
			walk(v.FieldByName("pending"), 2)
			return
		}
		switch v.Kind() {
		case reflect.Pointer:
			if !v.IsNil() && !seen[v.Pointer()] {
				seen[v.Pointer()] = true
				walk(v.Elem(), category)
			}
		case reflect.Interface:
			if !v.IsNil() {
				walk(v.Elem(), category)
			}
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				if v.Type() == reflect.TypeFor[Fold]() && v.Type().Field(i).Name == "source" {
					continue
				}
				walk(v.Field(i), category)
			}
		case reflect.Map:
			iter := v.MapRange()
			for iter.Next() {
				walk(iter.Key(), category)
				walk(iter.Value(), category)
			}
		case reflect.Slice, reflect.Array:
			for i := 0; i < v.Len(); i++ {
				walk(v.Index(i), category)
			}
		case reflect.String:
			totals[category] += v.Len()
		default:
			totals[category] += int(v.Type().Size())
		}
	}
	walk(reflect.ValueOf(root), 3)
	return totals
}

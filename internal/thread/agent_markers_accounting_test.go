package thread

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
)

func TestReferenceFinalReportAccounting(t *testing.T) {
	var mainPerTurn, childPerTurn int
	t.Logf("host struct sizes: mutable mainCall=%d marker=%d; map slots hold pointers so sparse finished maps do not inline the larger records", reflect.TypeFor[mainCall]().Size(), reflect.TypeFor[mainCallMarker]().Size())
	t.Logf("host child sizes: parent marker=%d reversible snapshot=%d; final markers retain no snapshot allocation", reflect.TypeFor[childState]().Size(), reflect.TypeFor[childSnapshot]().Size())
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
			for _, st := range f.turns {
				if st.calls != nil || len(st.finished) != 2 {
					t.Fatal("finished workload retained mutable calls or unexpected marker count")
				}
			}
			bytes := testFinalReportBytes(f)
			// Traverse all private indexes, so relocating snapshots cannot bypass
			// the marker-only guard. This workload has no reversible children.
			testChildMarkerShape(t, f)
			if childPerTurn == 0 {
				childPerTurn = bytes[5] / turns
			}
			if childPerTurn == 0 || bytes[5] != turns*childPerTurn || bytes[6] != 0 {
				t.Fatal("child marker cost grew or reversible snapshots survived", bytes)
			}
			if bytes[0] != turns*testFinalReportBytesForOne() {
				t.Fatal("final marker cost changed with history or payload size", bytes[0])
			}
			if mainPerTurn == 0 {
				mainPerTurn = bytes[4] / turns
			}
			if mainPerTurn == 0 || bytes[4] != turns*mainPerTurn {
				t.Fatal("main marker cost changed with history or payload size", bytes[4])
			}
			t.Logf("turns=%d entries=%d padding=%d logical bytes/turn: agent-markers=%.0f main-markers=%.0f child-parent-markers=%.0f reversible-child-snapshots=%.0f metadata-recipes=%.0f active-unresolved=%.0f other-lifecycle=%.0f; source-cache=%d total; scalar sizes + string bytes; container/header/capacity overhead excluded, pointer targets counted once, strings counted per field (no backing-sharing deduction); fixed-width entry IDs, fixed-length source IDs", turns, len(entries), padding, float64(bytes[0])/float64(turns), float64(bytes[4])/float64(turns), float64(bytes[5])/float64(turns), float64(bytes[6])/float64(turns), float64(bytes[1])/float64(turns), float64(bytes[2])/float64(turns), float64(bytes[3])/float64(turns), source.bytes)
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
func testFinalReportBytes(root any) [7]int {
	var totals [7]int
	seen := make(map[uintptr]bool)
	var walk func(reflect.Value, int)
	walk = func(v reflect.Value, category int) {
		if !v.IsValid() {
			return
		}
		if v.Type() == reflect.TypeFor[mainCallMarker]() {
			category = 4
		}
		if category == 4 && v.Type() == reflect.TypeFor[mainTurn]() && v.FieldByName("end").IsNil() {
			category = 2
		}
		if v.Type() == reflect.TypeFor[childSnapshot]() {
			category = 6
		}
		if category != 6 && (v.Type() == reflect.TypeFor[Item]() || v.Type() == reflect.TypeFor[contentRecipe]()) {
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
				fieldCategory := category
				if v.Type() == reflect.TypeFor[Fold]() {
					switch v.Type().Field(i).Name {
					case "source":
						continue
					case "turns", "openings", "openTexts":
						fieldCategory = 4
					case "childReports":
						fieldCategory = 2
					case "children", "childEdges", "childAgents", "childEndings":
						fieldCategory = 5
					}
				}
				if category == 4 && v.Type() == reflect.TypeFor[mainTurn]() && v.Type().Field(i).Name == "calls" {
					fieldCategory = 2 // unresolved calls and shared shell exceptions
				}
				walk(v.Field(i), fieldCategory)
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

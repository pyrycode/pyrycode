package thread

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
)

func TestMainTextClosureTracking(t *testing.T) {
	t.Parallel()
	for _, explicit := range []bool{false, true} {
		for _, source := range []*history.SessionProvenance{nil, testSource("claude", "s"), testSource("codex", "s")} {
			t.Run(fmt.Sprintf("%s/explicit=%t", sourceName(source), explicit), func(t *testing.T) {
				opening := testMain(1, "assistant_delta", `,"text":"old"`)
				if explicit {
					opening = testMain(1, "main_turn_opened", `,"occurred_at":"2026-01-01T00:00:00Z"`)
				}
				entries := []history.Entry{
					opening,
					testMain(2, "assistant_delta", `,"text":" first"`),
					testMain(3, "tool_use", `,"tool_use_id":"c","name":"Read"`),
					testMain(4, "assistant_delta", `,"text":"old generation"`),
					testMain(5, "main_turn_opened", `,"occurred_at":"2026-01-01T00:00:00Z"`),
					testMain(6, "assistant_delta", `,"text":"new generation"`),
					testMain(7, "turn_end", `,"stop_reason":"end_turn"`),
					testMain(8, "assistant_delta", `,"text":"inactive late"`),
					testDivider(9, "agent_switch", "s", "n", "claude", "codex"),
					testMain(10, "assistant_delta", `,"text":"current scope"`),
					testChild(11, "assistant_delta", "missing", `,"text":"child"`),
					testAcceptance(12, "phone"),
					testMain(13, "tool_result", `,"tool_use_id":"pending","is_error":false`),
					testMessage(14),
					testMain(15, "main_turn_interrupted", `,"turn_opened_entry_id":1,"cause":"daemon_restart","occurred_at":"2026-01-01T00:00:00Z"`),
					testMain(16, "assistant_delta", `,"text":"after delivery"`),
					testMessage(17),
					testMessage(18),
				}
				for i := range entries {
					entries[i].Session = source
				}
				// Hidden delivery still splits text, but closing it cannot raise the watermark.
				entries[13].Shown = new(bool)
				f := New("a")
				testFeed(t, f, entries[:13]...)
				shown := f.lastShown
				testFeed(t, f, entries[13])
				if len(f.openTexts) != 0 {
					t.Fatal("delivered message retained main coalescing runs")
				}
				for _, st := range f.openings {
					if st.text >= 0 || st.textSlot != 0 {
						t.Fatal("retained opening still has an open main run")
					}
				}
				for _, st := range f.childTurns {
					if st.text < 0 || st.textSlot != 0 {
						t.Fatal("main closure split or registered child text")
					}
				}
				if f.lastShown != shown {
					t.Fatal("closure alone raised LastShownVersion")
				}
				testMainItem(t, f, 4, 14, "assistant_message", "done", false)
				testMainItem(t, f, 6, 7, "assistant_message", "done", false)
				lateRev := uint64(8)
				if source != nil {
					lateRev = 10
				}
				late := testMainItem(t, f, 8, lateRev, "assistant_message", "done", false)
				if source != nil {
					testContent(t, late, "text", "inactive latecurrent scope")
				} else {
					testMainItem(t, f, 10, 14, "assistant_message", "done", false)
				}
				testFeed(t, f, entries[14:]...)
				testMainItem(t, f, 3, 15, "tool_call", "interrupted", false)
				testMainItem(t, f, 4, 14, "assistant_message", "done", false)
				if st := f.openings[1]; st == nil || st.end == nil || st.text != -1 {
					t.Fatal("closed original opening unavailable for recovery")
				}
				rev := uint64(16)
				if source == nil {
					rev = 17
				}
				item := testMainItem(t, f, 16, rev, "assistant_message", "done", false)
				testContent(t, item, "text", "after delivery")
				replay := testMainReplay(t, entries)
				if !reflect.DeepEqual(replay.Items(), f.Items()) || replay.lastShown != f.lastShown {
					t.Fatal("single-entry tail differs from replay")
				}
			})
		}
	}
}

func TestMainTextClosureWork(t *testing.T) {
	t.Parallel()
	for _, peak := range []bool{false, true} {
		t.Run(fmt.Sprintf("historical-peak=%t", peak), func(t *testing.T) {
			var baseline []int
			for _, size := range []int{1024, 4096} {
				f := New("a")
				feed := func(e history.Entry) {
					e.ID = f.version + 1
					testFeed(t, f, e)
				}
				for i := 0; i < size; i++ {
					feed(testMain(1, "main_turn_opened", `,"occurred_at":"2026-01-01T00:00:00Z"`))
					feed(testMain(1, "assistant_delta", `,"text":"fixed"`))
					if !peak {
						feed(testMain(1, "turn_end", `,"stop_reason":"end_turn"`))
					}
				}
				feed(testMessage(1))
				if len(f.openTexts) != 0 || (peak && cap(f.openTexts) < size) {
					t.Fatal("setup failed to drain tracking or retain historical peak capacity")
				}
				fixed := f.Items()
				var counts []int
				measure := func(e history.Entry) {
					f.closureCounts = &closureWorkCounts{}
					f.publicCounts = &publicWorkCounts{}
					feed(e)
					counts = append(counts, f.closureCounts.work+f.publicCounts.work)
				}
				for cycle := 0; cycle < 3; cycle++ {
					first := f.version + 1
					for run := 0; run < 3; run++ {
						measure(testMain(1, "main_turn_opened", `,"occurred_at":"2026-01-01T00:00:00Z"`))
						measure(testMain(1, "assistant_delta", `,"text":"fixed"`))
					}
					measure(testMain(1, "assistant_delta", `,"text":" append"`))
					measure(testMain(1, "tool_use", `,"tool_use_id":"c","name":"Read"`))
					measure(testMain(1, "assistant_delta", `,"text":"after tool"`))
					if len(f.openTexts) != 3 {
						t.Fatal("append duplicated tracking or scoped tool closure lost another run")
					}
					// Close a nonfinal slot; the moved run must remain discoverable.
					measure(testMain(1, "main_turn_interrupted", fmt.Sprintf(`,"turn_opened_entry_id":%d,"cause":"daemon_restart","occurred_at":"2026-01-01T00:00:00Z"`, first)))
					for slot, st := range f.openTexts {
						if st.textSlot != slot+1 || st.text < 0 {
							t.Fatal("scoped removal left invalid dense membership")
						}
					}
					measure(testMessage(1))
					measure(testMessage(1))
					if len(f.openTexts) != 0 || f.closureCounts.work != 1 {
						t.Fatal("empty closure retained candidates or inspected history")
					}
				}
				if !reflect.DeepEqual(fixed, f.Items()[:len(fixed)]) {
					t.Fatal("fixed workload changed retained closed items")
				}
				for i, count := range counts {
					if baseline != nil && count >= 2*baseline[i] && count != 0 {
						t.Fatalf("entry %d closure work grew >=2x with 4x retained history: %v -> %v", i, baseline, counts)
					}
				}
				baseline = counts
				t.Logf("size=%d measured ingestion work: %v", size, counts)
			}
		})
	}
}

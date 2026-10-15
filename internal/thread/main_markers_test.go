package thread

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
)

func testMainMarkerReplay(t *testing.T, entries []history.Entry, check func(*Fold, int)) {
	t.Helper()
	for _, size := range []int{1, 5, len(entries)} {
		f, source := testReferenceFold(entries)
		legacy := New("a")
		for i := 0; i < len(entries); i += size {
			source.chunk = entries[i:min(i+size, len(entries))]
			got, err := f.FeedChanges(source.chunk)
			want, legacyErr := legacy.FeedChanges(source.chunk)
			if err != nil || legacyErr != nil || !reflect.DeepEqual(got, want) || !reflect.DeepEqual(f.Items(), legacy.Items()) || !reflect.DeepEqual(f.touches, legacy.touches) || !reflect.DeepEqual(f.published, legacy.published) || !reflect.DeepEqual(f.publicVisible, legacy.publicVisible) || f.suppressed != legacy.suppressed || f.unprovenThrough != legacy.unprovenThrough {
				t.Fatalf("chunk=%d at=%d differs: %v / %v\ngot=%+v\nwant=%+v", size, i, err, legacyErr, got, want)
			}
			if size == 1 {
				check(f, i+1)
			}
			testNoLifecyclePayload(t, f)
		}
	}
}

func TestReferenceMainMarkers(t *testing.T) {
	marker := reflect.TypeFor[mainCallMarker]()
	if marker.NumField() != 4 || marker.Field(0).Type.Kind() != reflect.Int || marker.Field(1).Type.Kind() != reflect.Uint64 || marker.Field(2).Name != "lifetime" || marker.Field(2).Type.Kind() != reflect.String || marker.Field(3).Type != reflect.TypeFor[*mainOutcome]() {
		t.Fatal("finished call marker retains more than scoped identity, item and outcome reference")
	}
	for _, source := range []*history.SessionProvenance{nil, testSource("claude", "s"), testSource("codex", "s")} {
		entries := []history.Entry{
			testMain(1, "main_turn_opened", `,"occurred_at":"2026-01-01T00:00:00Z"`),
			testMain(2, "tool_use", `,"tool_use_id":"c","name":"Read"`),
			testMain(3, "turn_end", `,"stop_reason":"end_turn"`),
			testMain(4, "tool_result", `,"tool_use_id":"c","is_error":false`),
			testMain(5, "tool_use", `,"tool_use_id":"late","name":"Read"`),
			testMain(6, "tool_result", `,"tool_use_id":"late","is_error":true`),
			testMain(7, "assistant_delta", `,"text":"late"`),
			testMain(8, "assistant_delta", `,"text":" text"`),
			testDivider(9, "operator_reset", "s", "n", "claude", "claude"),
			testMain(10, "main_turn_opened", `,"occurred_at":"2026-01-01T00:00:00Z"`),
			testMain(11, "tool_use", `,"tool_use_id":"c","name":"Read"`),
			testMain(12, "main_tool_interrupted", `,"tool_call_id":"c","turn_opened_entry_id":1,"cause":"daemon_restart","occurred_at":"2026-01-01T00:00:00Z"`),
			testMessage(13),
			testMain(14, "main_turn_interrupted", `,"turn_opened_entry_id":1,"cause":"daemon_restart","occurred_at":"2026-01-01T00:00:00Z"`),
			testMain(15, "turn_end", `,"stop_reason":"end_turn"`),
			testMain(16, "assistant_delta", `,"text":"tail"`),
			testMain(17, "tool_use", `,"tool_use_id":"c","name":"Read"`),
			testMain(18, "assistant_delta", `,"text":"after"`),
			testMessage(19),
		}
		for i := range entries {
			entries[i].Session = source
		}
		for i := 0; i < 64; i++ {
			for _, e := range []history.Entry{testMain(1, "tool_result", `,"tool_use_id":"c","is_error":true`), testMain(1, "turn_end", `,"stop_reason":"conflict"`)} {
				e.ID, e.Session = uint64(len(entries)+1), source
				entries = append(entries, e)
			}
		}
		t.Run(sourceName(source), func(t *testing.T) {
			var bounded int
			testMainMarkerReplay(t, entries, func(f *Fold, n int) {
				if n >= 3 && f.openings[1].calls != nil {
					t.Fatal("finished turn retains mutable call collection")
				}
				if n >= 4 {
					testMainItem(t, f, 2, 3, "tool_call", "interrupted", false)
				}
				if n >= 6 {
					testMainItem(t, f, 5, 5, "tool_call", "interrupted", false)
				}
				if n >= 8 {
					rev := uint64(8)
					item := testMainItem(t, f, 7, rev, "assistant_message", "done", false)
					testContent(t, item, "text", "late text")
				}
				if n == 12 {
					testMainItem(t, f, 11, 11, "tool_call", "running", true)
				}
				if n >= 18 {
					testContent(t, testMainItem(t, f, 16, 16, "assistant_message", "done", false), "text", "tail")
					testContent(t, testMainItem(t, f, 18, 18, "assistant_message", "done", false), "text", "after")
				}
				if n >= 15 {
					testMainItem(t, f, 11, 15, "tool_call", "interrupted", false)
					for _, st := range f.openings {
						if st.calls != nil {
							t.Fatal("redundant results restored call graph")
						}
					}
					if len(f.openings[1].finished) != 2 || len(f.openings[10].finished) != 1 {
						t.Fatal("marker count is not bounded per original identity")
					}
					// Only continuation facts are counted; every redundant ending also
					// creates a public item, intentionally outside this bound.
					var bytes int
					for _, size := range testFinalReportBytes(f.openings) {
						bytes += size
					}
					if n == 15 {
						bounded = bytes
					} else if bytes != bounded {
						t.Fatal("marker retention grew", bounded, bytes)
					}
				}
			})
		})
	}
}

func TestReferenceMainMarkerChild(t *testing.T) {
	entries := []history.Entry{
		testMain(1, "tool_use", `,"tool_use_id":"agent","name":"Agent"`),
		testMain(2, "tool_use", `,"tool_use_id":"c","name":"Read"`),
		testMain(3, "tool_result", `,"tool_use_id":"c","is_error":false`),
		testMain(4, "turn_end", `,"stop_reason":"end_turn"`),
		testChild(5, "tool_use", "agent", `,"tool_use_id":"c","name":"Read"`),
		testChild(6, "tool_result", "agent", `,"tool_use_id":"c","is_error":true`),
		testMain(7, "background_task_started", `,"task_id":"task","tool_call_id":"agent"`),
		testMain(8, "background_task_updated", `,"task_id":"task","status":"completed"`),
	}
	testMainMarkerReplay(t, entries, func(f *Fold, n int) {
		if n == 4 && f.turns[mainKey{turn: "t"}].calls != nil {
			t.Fatal("ordinary call graph retained")
		}
		if n >= 5 {
			for _, item := range f.Items() {
				if item.ID == 2 && (item.Parent != 1 || item.Status != "done" || item.Active) {
					t.Fatal("late parent lost intrinsic outcome", item)
				}
			}
		}
	})
}

func TestReferenceMainMarkerPending(t *testing.T) {
	t.Run("uncreated child claim", func(t *testing.T) {
		entries := []history.Entry{
			testMain(1, "tool_result", `,"tool_use_id":"c","is_error":false`),
			testMain(2, "turn_end", `,"stop_reason":"end_turn"`),
			testChild(3, "tool_use", "missing", `,"tool_use_id":"c","name":"Read"`),
		}
		testMainMarkerReplay(t, entries, func(f *Fold, n int) {
			if n == 3 && (f.openings[1].calls != nil || len(f.childCalls) != 1) {
				t.Fatal("child claim left the finished turn's mutable collection allocated")
			}
		})
	})
	entries := []history.Entry{
		testMain(1, "tool_result", `,"tool_use_id":"c","is_error":false,"result_detail":"original"`),
		testMain(2, "turn_end", `,"stop_reason":"end_turn"`),
		testMain(3, "tool_use", `,"tool_use_id":"c","name":"Read"`),
		testMain(4, "tool_use", `,"tool_use_id":"agent","name":"Agent"`),
		testDurable(5, "agent_call_observed", `,"tool_call_id":"root","tool":"Agent"`),
		testChild(6, "tool_use", "root", `,"tool_use_id":"agent","name":"Agent"`),
		testChild(7, "tool_use", "root", `,"tool_use_id":"c","name":"Read"`),
		testMain(8, "main_turn_opened", `,"occurred_at":"2026-01-01T00:00:00Z"`),
		testMain(9, "turn_end", `,"stop_reason":"end_turn"`),
	}
	testMainMarkerReplay(t, entries, func(f *Fold, n int) {
		st := f.openings[1]
		if n <= 2 && (st.calls["c"] == nil || st.calls["c"].terminal.id != 1 || len(st.finished) != 0) {
			t.Fatal("uncreated outcome was discarded")
		}
		if n >= 3 {
			item := testMainItem(t, f, 3, 3, "tool_call", "done", false)
			testContent(t, item, "result", map[string]any{"turn_id": "t", "tool_use_id": "c", "is_error": false, "result_detail": "original"})
		}
		if n == 5 && st.finished["agent"].lifetime == "" {
			t.Fatal("launcher marker missed first-lifetime promotion")
		}
		if n >= 6 {
			if _, retained := st.finished["agent"]; retained {
				t.Fatal("child claim retained launcher marker")
			}
			if _, retained := st.finished["c"]; !retained {
				t.Fatal("new lifetime claimed old ordinary item")
			}
		}
		if n == 9 && (f.openings[8].calls != nil || f.openings[8].finished != nil) {
			t.Fatal("empty finished turn retained lifecycle collections")
		}
	})
}

func TestReferenceMainMarkerShell(t *testing.T) {
	var entries []history.Entry
	for generation := 0; generation < 5; generation++ {
		part := []history.Entry{
			testMain(1, "main_turn_opened", `,"occurred_at":"2026-01-01T00:00:00Z"`),
			testMain(2, "tool_use", `,"tool_use_id":"c","name":"Bash"`),
			testMain(3, "turn_end", `,"stop_reason":"end_turn"`),
			testDurable(4, "agent_call_observed", `,"tool_call_id":"root","tool":"Agent"`),
			testMain(5, "tool_result", `,"tool_use_id":"c","is_error":false`),
			testMain(6, "background_task_started", `,"task_id":"task","tool_call_id":"c"`),
			testMain(7, "background_task_updated", `,"task_id":"task","status":"completed"`),
		}
		if generation == 2 {
			// An established lifetime cannot adopt evidence created before the
			// next lifetime is announced. Announce it before the late shell.
			part[1], part[3] = part[3], part[1]
		}
		if generation >= 3 {
			part[3] = testEntry(4, "unsupported", `{}`) // Keep both legacy scopes without a saved lifetime.
		}
		for _, e := range part {
			e.ID = uint64(len(entries) + 1)
			if generation < 3 {
				e.Session = testSource("claude", fmt.Sprintf("s%d", generation%2))
			}
			if generation == 2 {
				e.Payload = []byte(strings.ReplaceAll(string(e.Payload), "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"))
			}
			entries = append(entries, e)
		}
		if generation == 3 {
			entries = append(entries, testDivider(uint64(len(entries)+1), "operator_reset", "s", "n", "claude", "claude"))
		}
	}
	testMainMarkerReplay(t, entries, func(f *Fold, n int) {
		base := 0
		for generation := 0; generation < 5; generation++ {
			step := n - base
			creation := 2
			if generation == 2 {
				creation = 4
			}
			if step >= 3 && step <= 7 {
				if step >= creation {
					call := f.openings[uint64(base+1)].calls["c"]
					if call == nil || call.shell == nil || call.shell.ordinary != call {
						t.Fatal("main closure discarded shared shell owner")
					}
				}
				for _, item := range f.Items() {
					if item.ID != uint64(base+creation) {
						continue
					}
					if item.Active != (step == 6) {
						t.Fatal("shell closure/link boundary lost", n, item)
					}
					if step == 7 && (item.Status != "finished" || item.EndedOrder != uint64(base+7)) {
						t.Fatal("background completion lost", item)
					}
				}
			}
			base += 7
			if generation == 3 {
				base++
			}
		}
	})
}

package thread

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
)

func TestReferenceFinalReportMarkers(t *testing.T) {
	for _, typ := range []reflect.Type{reflect.TypeFor[agentCall](), reflect.TypeFor[agentTask]()} {
		field, _ := typ.FieldByName("reports")
		if field.Type.Kind() == reflect.Slice {
			t.Errorf("%s retains the old report slice", typ)
		}
	}
	for _, tool := range []string{"Agent", "Bash"} {
		entries := []history.Entry{
			testDurable(1, "agent_call_observed", `,"tool_call_id":"c","tool":"Agent"`),
			testMain(2, "tool_use", fmt.Sprintf(`,"tool_use_id":"c","name":%q`, tool)),
			testMain(3, "tool_result", `,"tool_use_id":"c","is_error":false`),
			testMain(4, "background_task_started", `,"task_id":"task","tool_call_id":"c"`),
			testDurable(5, "agent_ended_with_session", `,"tool_call_id":"c","cause":"child_exit"`),
			testDurable(6, "background_task_outcome", `,"task_id":"task","status":"completed"`),
			testMain(7, "background_task_updated", `,"task_id":"task","status":"completed","summary":"mapped"`),
			testMain(8, "tool_use", `,"tool_use_id":"other","name":"Agent"`),
			testMain(9, "background_task_started", `,"task_id":"task","tool_call_id":"other"`),
		}
		for i := 0; i < 64; i++ {
			entries = append(entries,
				testMain(uint64(len(entries)+1), "background_task_updated", fmt.Sprintf(`,"task_id":"task","status":"failed","summary":"conflict%d"`, i)),
				testMain(uint64(len(entries)+2), "background_task_updated", fmt.Sprintf(`,"task_id":"task","summary":"update%d"`, i)),
				testDurable(uint64(len(entries)+3), "agent_ended_with_session", `,"tool_call_id":"c","cause":"daemon_restart"`),
				testMain(uint64(len(entries)+4), "tool_denied", `,"tool_use_id":"c","reason":"late"`))
		}
		for _, chunk := range []int{1, 7, len(entries)} {
			t.Run(fmt.Sprint(tool, chunk), func(t *testing.T) {
				f, source := testReferenceFold(entries)
				legacy := New("a")
				for start := 0; start < len(entries); start += chunk {
					source.chunk = entries[start:min(start+chunk, len(entries))]
					got, err := f.FeedChanges(source.chunk)
					want, legacyErr := legacy.FeedChanges(source.chunk)
					if err != nil || legacyErr != nil || !reflect.DeepEqual(got, want) || !reflect.DeepEqual(f.Items(), legacy.Items()) || !reflect.DeepEqual(f.touches, legacy.touches) || !reflect.DeepEqual(f.published, legacy.published) || f.suppressed != legacy.suppressed || f.unprovenThrough != legacy.unprovenThrough {
						t.Fatalf("chunk at %d differs: %v / %v\ngot %+v\nwant %+v", start, err, legacyErr, got, want)
					}
					if chunk == 1 {
						items := f.Items()
						if start == 2 && items[0].Active || start == 3 && !items[0].Active || start == 4 && items[0].Active {
							t.Fatal("provisional/link/genuine-end boundary missing")
						}
						if start >= 8 && (items[1].Active || items[1].EndedOrder != 8) {
							t.Fatal("task reassociation lost first terminal")
						}
						for _, g := range f.agentGroups {
							if c := g.calls["c"]; c != nil {
								if start == 2 && len(c.reports.pending) != 1 {
									t.Fatal("provisional result prematurely compacted")
								}
								if start >= 4 && (c.reports.markers[0] == nil || c.reports.markers[0].id != 5 || len(c.reports.entries()) > 4) {
									t.Fatal("genuine call final did not become bounded markers")
								}
							}
						}
					}
					testNoLifecyclePayload(t, f)
					for _, g := range f.agentGroups {
						for _, task := range g.tasks {
							if start+chunk >= 6 && (cap(task.reports.pending) != 0 || len(task.reports.entries()) > 4) {
								t.Fatal("final task report retention grew")
							}
						}
					}
				}
				if !strings.Contains(string(f.Items()[1].Content), "update63") {
					t.Fatal("late enrichment lost")
				}
			})
		}
	}
}

func TestReferenceFinalReportIdentity(t *testing.T) {
	var entries []history.Entry
	// Reuse call/task IDs with different recorded sources, lifetimes and legacy
	// scopes. Every predecessor has already reached a genuine terminal.
	for i := 0; i < 5; i++ {
		part := []history.Entry{
			testMain(1, "tool_use", `,"tool_use_id":"c","name":"Agent"`),
			testMain(2, "background_task_started", `,"task_id":"task","tool_call_id":"c"`),
			testMain(3, "background_task_updated", `,"task_id":"task","status":"completed"`),
		}
		if i < 3 {
			part = append([]history.Entry{testDurable(1, "agent_call_observed", `,"tool_call_id":"c","tool":"Agent"`)}, part...)
		}
		for _, e := range part {
			e.ID = uint64(len(entries) + 1)
			if i < 3 {
				e.Session = testSource("claude", "s1")
				if i == 1 {
					e.Session = testSource("claude", "s2")
				}
				if i == 2 {
					e.Payload = []byte(strings.ReplaceAll(string(e.Payload), "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"))
				}
			}
			entries = append(entries, e)
		}
		if i == 3 {
			entries = append(entries, testDivider(uint64(len(entries)+1), "operator_reset", "s", "n", "claude", "claude"))
		}
	}
	for _, size := range []int{1, 5, len(entries)} {
		f, source := testReferenceFold(entries)
		legacy := New("a")
		for i := 0; i < len(entries); i += size {
			source.chunk = entries[i:min(i+size, len(entries))]
			got, err := f.FeedChanges(source.chunk)
			want, legacyErr := legacy.FeedChanges(source.chunk)
			if err != nil || legacyErr != nil || !reflect.DeepEqual(got, want) || !reflect.DeepEqual(f.touches, legacy.touches) {
				t.Fatal("reused scoped identity changed output", i)
			}
		}
		if len(f.agentGroups) != 5 {
			t.Fatal("fixture did not create five isolated groups", len(f.agentGroups))
		}
	}
}

func TestReferenceFinalReportsBeforeShellCreation(t *testing.T) {
	entries := []history.Entry{
		testDurable(1, "agent_call_observed", `,"tool_call_id":"root","tool":"Agent"`),
		testEntry(2, "tool_denied", `{"turn_id":"wrong","tool_use_id":"c"}`),
		testMain(3, "tool_result", `,"tool_use_id":"c","is_error":false`),
		testDurable(4, "agent_ended_with_session", `,"tool_call_id":"c","cause":"child_exit"`),
		testMain(5, "tool_use", `,"tool_use_id":"c","name":"Bash"`),
		testMain(6, "background_task_started", `,"task_id":"task","tool_call_id":"c"`),
		testMain(7, "background_task_updated", `,"task_id":"task","status":"completed"`),
	}
	for _, size := range []int{1, 3, len(entries)} {
		f, source := testReferenceFold(entries)
		legacy := New("a")
		for i := 0; i < len(entries); i += size {
			source.chunk = entries[i:min(i+size, len(entries))]
			got, err := f.FeedChanges(source.chunk)
			want, legacyErr := legacy.FeedChanges(source.chunk)
			if err != nil || legacyErr != nil || !reflect.DeepEqual(got, want) || !reflect.DeepEqual(f.Items(), legacy.Items()) {
				t.Fatal("pre-creation report eligibility changed", i, err, legacyErr)
			}
		}
		if item := f.Items()[0]; item.EndedOrder != 5 || item.Status != "ended_with_session" {
			t.Fatal("linked shell lost independent ending", item)
		}
	}
}

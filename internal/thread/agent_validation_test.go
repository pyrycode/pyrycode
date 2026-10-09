package thread

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
)

func testAgentNeutralFact(t *testing.T, initial []history.Entry, bad history.Entry) {
	t.Helper()
	f, want := New("a"), New("a")
	testFeed(t, f, initial...)
	testFeed(t, want, initial...)
	testFeed(t, f, bad)
	want.version = bad.ID
	if !reflect.DeepEqual(f, want) {
		t.Fatal("unusable identity changed items, pending joins or attribution")
	}
}

func TestAgentIdentityMarkers(t *testing.T) {
	t.Parallel()
	for _, marker := range []string{"truncated_fields", "dropped_fields"} {
		for _, field := range []string{"turn_id", "tool_use_id", "parent_tool_use_id", "parent_tool_call_id"} {
			t.Run(marker+"/"+field, func(t *testing.T) {
				initial := []history.Entry{testMain(1, "assistant_delta", `,"text":"one"`), testDurable(2, "agent_call_observed", `,"tool_call_id":"c","tool":"Agent"`)}
				bad := testMain(3, "tool_use", `,"tool_use_id":"c","name":"Agent","`+marker+`":["`+field+`"]`)
				testAgentNeutralFact(t, initial, bad)
				entries := append(initial, bad, testAgentCall(4), testMain(5, "assistant_delta", `,"text":"two"`))
				f := testMainReplay(t, entries)
				item := testAgent(t, f, "running", true, 0)
				if item.ID != 4 || item.Rev != 4 {
					t.Fatal("unusable launch claimed creation", item)
				}
				testContent(t, item, "input", map[string]any{"prompt": "original"})
				testMainItem(t, f, 1, 4, "assistant_message", "done", false)
				testMainItem(t, f, 5, 5, "assistant_message", "running", true)
			})
		}
	}
}

func TestAgentReportIdentityMarkers(t *testing.T) {
	t.Parallel()
	for _, marker := range []string{"truncated_fields", "dropped_fields"} {
		for _, field := range []string{"turn_id", "tool_use_id", "parent_tool_use_id", "parent_tool_call_id"} {
			for _, typ := range []string{"tool_result", "tool_denied"} {
				for _, early := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%s/%s/early=%t", marker, field, typ, early), func(t *testing.T) {
						initial := []history.Entry{testMain(1, "assistant_delta", `,"text":"open"`), testDurable(2, "agent_call_observed", `,"tool_call_id":"c","tool":"Agent"`)}
						if !early {
							initial = append(initial, testAgentCall(3))
						}
						bad := testMain(4, typ, `,"tool_use_id":"c","is_error":false,"`+marker+`":["`+field+`"]`)
						testAgentNeutralFact(t, initial, bad)
						entries := append(initial, bad)
						if early {
							entries = append(entries, testAgentCall(5))
						}
						f := testMainReplay(t, entries)
						testAgent(t, f, "running", true, 0)
						entries = append(entries, testMain(6, "tool_result", `,"tool_use_id":"c","is_error":false`))
						testAgent(t, testMainReplay(t, entries), "finished", false, 6)
					})
				}
			}
		}
	}
}

func TestAgentRosterIdentityMarkers(t *testing.T) {
	t.Parallel()
	for _, marker := range []string{"truncated_fields", "dropped_fields"} {
		for _, field := range []string{"tool_call_id", "task_id"} {
			t.Run(marker+"/"+field, func(t *testing.T) {
				entries := []history.Entry{testAgentCall(1), testEntry(2, "background_task_roster", `{"tasks":[{"task_id":"task","tool_call_id":"c","`+marker+`":["`+field+`"]}]}`), testTask(3, "background_task_updated", `,"status":"completed","summary":"early"`)}
				f := testMainReplay(t, entries)
				testAgent(t, f, "running", true, 0)
				if field == "tool_call_id" {
					g := f.agentGroups[agentKey{}]
					if g == nil || g.tasks["task"] == nil || g.tasks["task"].observed != 2 {
						t.Fatal("lost independently usable roster task")
					}
				} else {
					testAgentNeutralFact(t, entries[:1], entries[1])
				}
				entries = append(entries, testTask(4, "background_task_started", `,"tool_call_id":"c"`))
				item := testAgent(t, testMainReplay(t, entries), "finished", false, 3)
				testContent(t, item, "task_report", map[string]any{"task_id": "task", "status": "completed", "summary": "early"})
			})
		}
	}
}

func TestAgentDurableParentText(t *testing.T) {
	t.Parallel()
	for _, source := range []*history.SessionProvenance{nil, testSource("claude", "s")} {
		for _, tc := range []struct {
			name, parent string
			isolated     bool
		}{
			{"child", `,"parent_tool_call_id":"outer"`, false},
			{"main", "", false},
			{"other_source", `,"parent_tool_call_id":"outer"`, true},
		} {
			t.Run(sourceName(source)+"/"+tc.name, func(t *testing.T) {
				entries := []history.Entry{testMain(1, "assistant_delta", `,"text":"one"`), testDurable(2, "agent_call_observed", `,"tool_call_id":"c","tool":"Agent"`+tc.parent), testAgentCall(3), testMain(4, "assistant_delta", `,"text":"two"`)}
				for i := range entries {
					entries[i].Session = source
				}
				if tc.isolated {
					entries[1].Session = testSource("claude", "other")
				}
				f := testMainReplay(t, entries)
				item := testRetainedAgent(t, f, "running", true, 0)
				if tc.parent != "" && !tc.isolated {
					if len(f.Items()) != 1 {
						t.Fatal("child launch split main text", f.Items())
					}
					testContent(t, item, "parent_tool_call_id", "outer")
					testContent(t, testMainItem(t, f, 1, 4, "assistant_message", "running", true), "text", "onetwo")
				} else {
					if len(f.Items()) != 3 {
						t.Fatal("main launch did not split main text", f.Items())
					}
					testMainItem(t, f, 1, 3, "assistant_message", "done", false)
					testMainItem(t, f, 4, 4, "assistant_message", "running", true)
				}
			})
		}
	}
}

func TestAgentTaskExtraIdentity(t *testing.T) {
	t.Parallel()
	for _, typ := range []string{"background_task_progress", "background_task_updated", "background_task_roster"} {
		t.Run(typ, func(t *testing.T) {
			initial := []history.Entry{testAgentCall(1), testTask(2, "background_task_started", `,"tool_call_id":"c"`)}
			plain := testTask(3, typ, `,"description":"progress","summary":"update"`)
			extra := testTask(3, typ, `,"description":"progress","summary":"update","tool_use_id":"c","tool_call_id":"c","parent_tool_call_id":"invented"`)
			if typ == "background_task_roster" {
				plain = testEntry(3, typ, `{"tasks":[]}`)
				extra = testEntry(3, typ, `{"tasks":[],"tool_use_id":"c","tool_call_id":"c","parent_tool_call_id":"invented"}`)
			}
			f := testMainReplay(t, append(initial, extra))
			want := testMainReplay(t, append(initial, plain))
			// Updates retain extra fields as raw data; they cannot enrich call identity.
			for key, g := range f.agentGroups {
				if !reflect.DeepEqual(g.calls, want.agentGroups[key].calls) {
					t.Fatal("unsupported fields changed call evidence")
				}
			}
			item := testAgent(t, f, "running", true, 0)
			testContent(t, item, "parent_tool_call_id", nil)
			if item.Rev != testAgent(t, want, "running", true, 0).Rev {
				t.Fatal("unsupported identity fields advanced revision")
			}
		})
	}
}

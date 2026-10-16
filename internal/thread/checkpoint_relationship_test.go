package thread

import (
	"reflect"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
)

func TestCheckpointResumeShellPromotion(t *testing.T) {
	t.Run("first-scope", func(t *testing.T) {
		entries := []history.Entry{
			testMain(1, "tool_use", `,"tool_use_id":"a","name":"Agent"`),
			testChild(2, "tool_use", "a", `,"tool_use_id":"c","name":"Bash"`),
			testDurable(3, "agent_call_observed", `,"tool_call_id":"d","tool":"Agent"`),
			testChild(4, "tool_result", "a", `,"tool_use_id":"c","is_error":false`),
			testChild(5, "assistant_delta", "a", `,"text":"continued"`),
		}
		testCheckpointResume(t, entries, []int{0, 1, 2, 3, 4, 5})
	})
	for _, source := range []*history.SessionProvenance{nil, testSource("claude", "s")} {
		t.Run(sourceName(source), func(t *testing.T) {
			entries := []history.Entry{
				testDivider(1, "idle_sleep", "s", "s", "claude", "claude"),
				testMain(2, "tool_use", `,"tool_use_id":"a","name":"Agent"`),
				testChild(3, "tool_use", "a", `,"tool_use_id":"c","name":"Bash"`),
				testDurable(4, "agent_call_observed", `,"tool_call_id":"d","tool":"Agent"`),
				testDurable(5, "background_task_linked", `,"task_id":"task","tool_call_id":"c"`),
				testChild(6, "tool_result", "a", `,"tool_use_id":"c","is_error":false`),
				testTask(7, "background_task_updated", `,"status":"completed"`),
				testChild(8, "assistant_delta", "a", `,"text":"continued"`),
			}
			for i := range entries {
				entries[i].Session = source
			}
			cuts := make([]int, len(entries)+1)
			for i := range cuts {
				cuts[i] = i
			}
			testCheckpointResume(t, entries, cuts)

			f, lookup := testReferenceFold(entries)
			testFeed(t, f, entries[:4]...)
			d, rows, err := f.ExportCheckpoint()
			if err != nil {
				t.Fatal(err)
			}
			r, err := ImportCheckpoint("a", lookup, testCheckpointJSON(t, d), testCheckpointJSON(t, rows))
			if err != nil {
				t.Fatal(err)
			}
			got, gotRows, err := r.ExportCheckpoint()
			if err != nil || !reflect.DeepEqual(d, got) || !reflect.DeepEqual(rows, gotRows) {
				t.Fatal("promotion scopes changed during import", err)
			}
			if len(r.childCalls) != 1 {
				t.Fatal("missing child shell")
			}
			for key, child := range r.childCalls {
				call := child.call
				if key.group.scope != 1 || key.group.lifetime != "" || call.group.scope != 0 || call.group.lifetime == "" || r.agentGroups[call.group].calls[key.call] != call.shell || call.shell.ordinary != call {
					t.Fatal("promoted owner lost its original child route")
				}
			}
			for _, field := range []string{"source", "lifetime", "membership"} {
				t.Run("invalid-"+field, func(t *testing.T) {
					state := testCheckpointJSON(t, d)
					call := &state.MainCalls[state.ChildCalls[0].Ref-1]
					switch field {
					case "source":
						call.Group.Source.SessionID = "other"
					case "lifetime":
						call.Group.Lifetime = ""
					case "membership":
						for i := range state.Groups {
							delete(state.Groups[i].Calls, "c")
						}
					}
					if got, err := ImportCheckpoint("a", lookup, state, rows); got != nil || err == nil {
						t.Fatal("accepted inconsistent promoted child owner")
					}
				})
			}
		})
	}
}

func TestCheckpointTurnTextOwnership(t *testing.T) {
	cases := []struct {
		name    string
		entries []history.Entry
	}{
		{"turns", []history.Entry{
			testEntry(1, "assistant_delta", `{"turn_id":"first","text":"FIRST"}`),
			testEntry(2, "assistant_delta", `{"turn_id":"second","text":"SECOND"}`),
			testEntry(3, "assistant_delta", `{"turn_id":"first","text":"TAIL"}`),
		}},
		{"generations", []history.Entry{
			testMain(1, "assistant_delta", `,"text":"FIRST"`),
			testMain(2, "main_turn_opened", `,"occurred_at":"2026-01-01T00:00:00Z"`),
			testMain(3, "assistant_delta", `,"text":"SECOND"`),
			testMain(4, "assistant_delta", `,"text":"TAIL"`),
		}},
		{"children", []history.Entry{
			testChild(1, "assistant_delta", "first", `,"text":"FIRST"`),
			testChild(2, "assistant_delta", "second", `,"text":"SECOND"`),
			testChild(3, "assistant_delta", "first", `,"text":"TAIL"`),
		}},
		{"main-child", []history.Entry{
			testMain(1, "assistant_delta", `,"text":"FIRST"`),
			testChild(2, "assistant_delta", "parent", `,"text":"SECOND"`),
			testMain(3, "assistant_delta", `,"text":"TAIL"`),
		}},
		{"sources", []history.Entry{
			testMain(1, "assistant_delta", `,"text":"FIRST"`),
			testMain(2, "assistant_delta", `,"text":"SECOND"`),
			testMain(3, "assistant_delta", `,"text":"TAIL"`),
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "sources" {
				tc.entries[0].Session = testSource("claude", "first")
				tc.entries[1].Session = testSource("claude", "second")
				tc.entries[2].Session = testSource("claude", "first")
			}
			cuts := make([]int, len(tc.entries)+1)
			for i := range cuts {
				cuts[i] = i
			}
			testCheckpointResume(t, tc.entries, cuts)
			f, source := testReferenceFold(tc.entries)
			testFeed(t, f, tc.entries[:len(tc.entries)-1]...)
			d, rows, err := f.ExportCheckpoint()
			if err != nil {
				t.Fatal(err)
			}
			if len(d.Turns) != 2 || d.Turns[0].Text < 0 || d.Turns[1].Text < 0 {
				t.Fatal("fixture needs two open text rows")
			}
			for _, swap := range []bool{false, true} {
				state := testCheckpointJSON(t, d)
				if swap {
					state.Turns[0].Text, state.Turns[1].Text = state.Turns[1].Text, state.Turns[0].Text
				} else {
					state.Turns[0].Text = state.Turns[1].Text
				}
				if got, err := ImportCheckpoint("a", source, state, testCheckpointJSON(t, rows)); got != nil || err == nil {
					t.Fatalf("accepted another owner's in-range assistant row (swap=%v)", swap)
				}
			}
		})
	}
}

func TestCheckpointFinishedMarkerValidation(t *testing.T) {
	entries := []history.Entry{
		testMain(1, "assistant_delta", `,"text":"open"`),
		testMain(2, "tool_use", `,"tool_use_id":"c","name":"Read"`),
		testMain(3, "tool_result", `,"tool_use_id":"c","is_error":false`),
	}
	f, source := testReferenceFold(entries)
	testFeed(t, f, entries...)
	d, rows, err := f.ExportCheckpoint()
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Turns) != 1 || len(d.Turns[0].Finished) != 1 {
		t.Fatal("fixture needs a compacted finished call")
	}
	for _, mode := range []string{"minus-one", "assistant-row", "nil-terminal"} {
		t.Run(mode, func(t *testing.T) {
			state := testCheckpointJSON(t, d)
			m := state.Turns[0].Finished["c"]
			switch mode {
			case "minus-one":
				m.Item = -1
			case "assistant-row":
				m.Item = 0
			case "nil-terminal":
				m.Terminal = 0
			}
			state.Turns[0].Finished["c"] = m
			if got, err := ImportCheckpoint("a", source, state, testCheckpointJSON(t, rows)); got != nil || err == nil {
				t.Fatal("accepted invalid finished marker")
			}
		})
	}
	// Both ordinary rows and the itemless launcher sentinel support later claims.
	for _, name := range []string{"Read", "Agent"} {
		t.Run("valid-"+name, func(t *testing.T) {
			entries := []history.Entry{
				testMain(1, "tool_use", `,"tool_use_id":"c","name":"`+name+`"`),
				testMain(2, "tool_result", `,"tool_use_id":"c","is_error":false`),
				testChild(3, "tool_use", "parent", `,"tool_use_id":"c","name":"`+name+`"`),
			}
			testCheckpointResume(t, entries, []int{0, 1, 2, 3})
		})
	}
}

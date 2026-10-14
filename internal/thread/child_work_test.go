package thread

import (
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
)

func TestChildRejectsShellParent(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"Bash", "local_bash"} {
		for _, source := range []*history.SessionProvenance{nil, testSource("claude", "s")} {
			t.Run(name+"/"+sourceName(source), func(t *testing.T) {
				entries := []history.Entry{
					testMain(1, "tool_use", `,"tool_use_id":"shell","name":"`+name+`"`),
					testChild(2, "assistant_delta", "shell", `,"text":"should remain unresolved"`),
					testChild(3, "tool_use", "shell", `,"tool_use_id":"read","name":"Read"`),
					testChild(4, "tool_result", "shell", `,"tool_use_id":"read","is_error":false`),
					testChild(5, "tool_use", "shell", `,"tool_use_id":"nested","name":"Agent"`),
					testChild(6, "assistant_delta", "nested", `,"text":"unresolved descendant"`),
					testChild(7, "tool_use", "nested", `,"tool_use_id":"deep-read","name":"Read"`),
					testChild(8, "tool_result", "nested", `,"tool_use_id":"deep-read","is_error":false`),
					testChild(9, "tool_use", "nested", `,"tool_use_id":"deep-task","name":"Task"`),
					testChild(10, "assistant_delta", "deep-task", `,"text":"deep unresolved descendant"`),
					testChild(11, "tool_result", "nested", `,"tool_use_id":"deep-task","is_error":false`),
					testChild(12, "tool_result", "shell", `,"tool_use_id":"nested","is_error":false`),
					testMain(13, "tool_result", `,"tool_use_id":"shell","is_error":false`),
				}
				f := New("a")
				for i := range entries {
					entries[i].Session = source
					testFeed(t, f, entries[i])
					items := f.Items()
					if len(items) != 1 || items[0].ID != 1 || items[0].Kind != "tool_call" || items[0].Parent != 0 {
						t.Fatalf("ordinary shell accepted as parent after entry %d: %#v", entries[i].ID, items)
					}
				}
				testMainItem(t, f, 1, 13, "tool_call", "done", false)
				testMainReplay(t, entries)
			})
		}
	}
}

func TestChildAgentParents(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"Agent", "Task"} {
		t.Run(name, func(t *testing.T) {
			entries := []history.Entry{
				testMain(1, "tool_use", `,"tool_use_id":"parent","name":"`+name+`"`),
				testChild(2, "assistant_delta", "parent", `,"text":"owned child"`),
				testChild(3, "tool_use", "parent", `,"tool_use_id":"read","name":"Read"`),
				testMain(4, "tool_result", `,"tool_use_id":"parent","is_error":false`),
			}
			f := New("a")
			testFeed(t, f, entries[:3]...)
			testChildItem(t, f, 2, 1, "assistant_message", "done", false)
			testChildItem(t, f, 3, 1, "tool_call", "running", true)
			testFeed(t, f, entries[3:]...)
			testMainItem(t, f, 1, 4, "agent", "finished", false)
			testChildItem(t, f, 3, 1, "tool_call", "interrupted", false)
			if len(f.Items()) != 3 {
				t.Fatal("agent children omitted", f.Items())
			}
			testMainReplay(t, entries)
		})
	}
}

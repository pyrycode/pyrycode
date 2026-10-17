package thread

import (
	"reflect"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
)

func childCompletionEntries() []history.Entry {
	return []history.Entry{
		testMain(1, "tool_use", `,"tool_use_id":"a","name":"Agent"`),
		testMain(2, "turn_end", `,"stop_reason":"end_turn"`),
		testChild(3, "assistant_delta", "a", `,"text":"final reply","text_complete":true`),
		testMain(4, "tool_result", `,"tool_use_id":"a","is_error":false`),
	}
}

func TestChildTextCompletionBoundary(t *testing.T) {
	t.Parallel()
	entries := childCompletionEntries()
	f := testMainReplay(t, entries)
	item := testChildItem(t, f, 3, 1, "assistant_message", "done", false)
	if item.Rev != 3 || item.Order != 3 {
		t.Fatal("parent ending changed intrinsic completion", item)
	}
	testContent(t, item, "text", "final reply")
	testContent(t, item, "parent_ending", nil)
}

func TestChildTextCompletionRepair(t *testing.T) {
	t.Parallel()
	entries := childCompletionEntries()
	entries = append(entries,
		testMain(5, "background_task_started", `,"task_id":"task","tool_call_id":"a"`),
		testMain(6, "background_task_updated", `,"task_id":"task","status":"completed"`))
	f := testMainReplay(t, entries)
	testChildItem(t, f, 3, 1, "assistant_message", "done", false)
	testChildItem(t, f, 1, 0, "agent", "finished", false)
}

func TestChildTextCompletionAfterEnding(t *testing.T) {
	t.Parallel()
	entries := childCompletionEntries()
	entries[2] = testChild(3, "assistant_delta", "a", `,"text":"partial "`)
	entries = append(entries, testChild(5, "assistant_delta", "a", `,"text":"finished","text_complete":true`))
	f := testMainReplay(t, entries)
	item := testChildItem(t, f, 3, 1, "assistant_message", "done", false)
	if item.Rev != 5 {
		t.Fatal("completion did not advance terminal revision")
	}
	testContent(t, item, "text", "partial finished")
	testContent(t, item, "parent_ending", nil)
}

func TestChildTextCompletionNegative(t *testing.T) {
	t.Parallel()
	for _, extra := range []string{"", `,"text_complete":false`, `,"text_complete":null`, `,"text_complete":"true"`} {
		t.Run(extra, func(t *testing.T) {
			entries := childCompletionEntries()
			entries[2] = testChild(3, "assistant_delta", "a", `,"text":"partial"`+extra)
			f := testMainReplay(t, entries)
			if extra == `,"text_complete":"true"` || extra == `,"text_complete":null` {
				if len(f.Items()) != 2 {
					t.Fatal("malformed completion created prose", f.Items())
				}
				return
			}
			testChildItem(t, f, 3, 1, "assistant_message", "interrupted", false)
		})
	}
	entries := childCompletionEntries()
	entries[2] = testChild(3, "assistant_delta", "absent", `,"text":"other","text_complete":true`)
	entries = append(entries, testChild(5, "assistant_delta", "a", `,"text":"unfinished"`))
	f := testMainReplay(t, entries)
	testChildItem(t, f, 5, 1, "assistant_message", "interrupted", false)
	if len(f.Items()) != 3 {
		t.Fatal("absent parent created a public child", f.Items())
	}
	siblings := []history.Entry{
		testMain(1, "tool_use", `,"tool_use_id":"a","name":"Agent"`),
		testMain(2, "tool_use", `,"tool_use_id":"b","name":"Agent"`),
		testChild(3, "assistant_delta", "a", `,"text":"partial"`),
		testChild(4, "assistant_delta", "b", `,"text":"finished","text_complete":true`),
		testMain(5, "tool_result", `,"tool_use_id":"a","is_error":false`),
	}
	f = testMainReplay(t, siblings)
	testChildItem(t, f, 3, 1, "assistant_message", "interrupted", false)
	testChildItem(t, f, 4, 2, "assistant_message", "done", false)
}

func TestChildTextCompletionCheckpoint(t *testing.T) {
	t.Parallel()
	testCheckpointResume(t, childCompletionEntries(), []int{0, 1, 2, 3, 4})
	for _, cut := range []int{2, 3, 4} {
		w, f, entries := testCheckpointWriter(t, childCompletionEntries())
		testFeed(t, f, entries[:cut]...)
		if _, err := testCommitCheckpoint(t, w, f, nil, true); err != nil {
			t.Fatal(err)
		}
		reopened, _, err := w.s.readCheckpoint(w.id, w.source)
		if err != nil {
			t.Fatal(err)
		}
		testFeed(t, f, entries[cut:]...)
		testFeed(t, reopened, entries[cut:]...)
		want, err := f.Render()
		if err != nil {
			t.Fatal(err)
		}
		got, err := reopened.Render()
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatal("persisted checkpoint diverged", err)
		}
		if got[len(got)-1].Status != "done" {
			t.Fatal("reopened reply lost completion")
		}
	}
}

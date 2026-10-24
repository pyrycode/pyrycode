package thread

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
)

func testSummaryTask(t *testing.T, h ChildSummary, want string) {
	t.Helper()
	raw, err := json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	var got *string
	if err := json.Unmarshal(fields["TaskID"], &got); err != nil || got == nil || *got != want {
		t.Fatalf("missing/wrong authoritative association: want %q, header=%s, error=%v", want, raw, err)
	}
}

func testSummaryTaskFixture(t *testing.T, entries []history.Entry) (*Store, *Fold, *recentSource, func(history.Entry)) {
	t.Helper()
	f, source := testReferenceFold(entries)
	f.captureSet = make(map[int]struct{})
	s, c := NewStore(nil), &publicationCapture{}
	s.lookup = func(_ context.Context, _ conversations.ConversationID, ids []uint64) ([]history.Entry, error) {
		return source.Lookup(ids)
	}
	w := &conversationWorker{changed: make(chan struct{})}
	s.workers[testStoreA] = w
	feed := func(e history.Entry) {
		t.Helper()
		testFeed(t, f, e)
		slots, changes, err := c.capture(f)
		if err != nil {
			t.Fatal(err)
		}
		changes.Epoch = "epoch"
		s.publish(context.Background(), testStoreA, w, slots, changes, f, c)
	}
	return s, f, source, feed
}

func TestSummaryTaskLifecycle(t *testing.T) {
	entries := []history.Entry{
		testAgentCall(1),
		testMain(2, "tool_use", `,"tool_use_id":"other","name":"Agent"`),
		testTask(3, "background_task_started", `,"tool_call_id":"c"`),
		testEntry(4, "background_task_started", `{"task_id":"z task 🐈","tool_call_id":"c"}`),
		testEntry(5, "background_task_started", `{"task_id":"z task 🐈","tool_call_id":"other"}`),
		testTask(6, "background_task_updated", `,"status":"completed"`),
		testTask(7, "background_task_updated", `,"status":"completed"`),
	}
	s, f, source, feed := testSummaryTaskFixture(t, entries)
	var oldStore *Store
	var oldPage CompactQueryResult
	for n, e := range entries {
		feed(e)
		lookup := source.lookup
		source.lookup = func([]uint64) ([]history.Entry, error) { t.Fatal("summary read closed history"); return nil, nil }
		page, err := s.CompactHistoryPage(context.Background(), testStoreA, e.ID+1, 10)
		if err != nil || len(page.Headers) != min(n+1, 2) {
			t.Fatal(page, err)
		}
		want := ""
		if e.ID >= 3 {
			want = "task"
		}
		if e.ID == 4 {
			want = "z task 🐈"
		}
		testSummaryTask(t, page.Headers[0], want)
		if e.ID >= 5 {
			testSummaryTask(t, page.Headers[1], "z task 🐈")
		}
		if e.ID > 2 && e.ID < 7 {
			for _, read := range []func() (CompactQueryResult, error){
				func() (CompactQueryResult, error) {
					return s.CompactCatchUp(context.Background(), testStoreA, "epoch", e.ID-1, 1)
				},
				func() (CompactQueryResult, error) {
					return s.CompactChanges(context.Background(), testStoreA, "epoch", e.ID-1)
				},
			} {
				changed, err := read()
				if err != nil || changed.ResetRequired || len(changed.Headers) == 0 {
					t.Fatal("association change lost", changed, err)
				}
				for _, h := range changed.Headers {
					if !reflect.DeepEqual(h, page.Headers[h.ID-1]) {
						t.Fatal("interval association differs", h)
					}
				}
			}
			if page.Headers[0].SubtreeVersion == nil || *page.Headers[0].SubtreeVersion != e.ID {
				t.Fatal("stale association", page.Headers[0])
			}
		}
		if e.ID == 3 {
			snap, index := s.captureQuery(testStoreA)
			oldStore, oldPage = NewStore(nil), page
			oldStore.workers[testStoreA] = &conversationWorker{snapshot: snap, query: index}
		}
		if oldStore != nil {
			again, err := oldStore.CompactHistoryPage(context.Background(), testStoreA, 4, 10)
			if err != nil || !reflect.DeepEqual(again, oldPage) {
				t.Fatal("captured association changed", again, err)
			}
		}
		source.lookup = lookup
		full, err := f.Render()
		if err != nil {
			t.Fatal(err)
		}
		for i, h := range page.Headers {
			var content struct {
				Task string `json:"task_id"`
			}
			if err := json.Unmarshal(full[i].Content, &content); err != nil {
				t.Fatal(err)
			}
			testSummaryTask(t, h, content.Task)
			if h.Rev != full[i].Rev || h.Status != full[i].Status || h.Active != full[i].Active || h.EndedOrder != full[i].EndedOrder {
				t.Fatal("canonical metadata differs", h, full[i])
			}
		}
	}
}

func TestSummaryTaskScope(t *testing.T) {
	for name, bad := range map[string]history.Entry{
		"absent":               testTask(2, "background_task_started", ""),
		"truncated-call":       testTask(2, "background_task_started", `,"tool_call_id":"c","truncated_fields":["tool_call_id"]`),
		"dropped-task":         testTask(2, "background_task_started", `,"tool_call_id":"c","dropped_fields":["task_id"]`),
		"foreign-conversation": testTask(2, "background_task_started", `,"tool_call_id":"c","conversation_id":"foreign"`),
		"foreign-lifetime":     testDurable(2, "background_task_linked", `,"tool_call_id":"c","task_id":"task"`),
		"foreign-session":      testTask(2, "background_task_started", `,"tool_call_id":"c"`),
	} {
		t.Run(name, func(t *testing.T) {
			launch := testAgentCall(1)
			if name == "foreign-session" {
				launch.Session, bad.Session = testSource("claude", "one"), testSource("claude", "two")
			}
			entries := []history.Entry{launch, bad}
			s, _, _, feed := testSummaryTaskFixture(t, entries)
			for _, e := range entries {
				feed(e)
			}
			page, err := s.CompactNewestWindow(context.Background(), testStoreA, 10)
			if err != nil || len(page.Headers) != 1 {
				t.Fatal(page, err)
			}
			testSummaryTask(t, page.Headers[0], "")
		})
	}
	for _, boundary := range []string{"operator_reset", "idle_sleep", "lifetime"} {
		t.Run(boundary, func(t *testing.T) {
			entries := []history.Entry{testAgentCall(1), testTask(2, "background_task_started", `,"tool_call_id":"c"`), testDivider(3, boundary, "s", "n", "claude", "claude"), testAgentCall(4), testTask(5, "background_task_started", `,"tool_call_id":"c"`)}
			if boundary == "lifetime" {
				entries[2] = testDurable(3, "agent_call_observed", `,"tool_call_id":"c","tool":"Agent"`)
				entries[2].Payload = []byte(strings.ReplaceAll(string(entries[2].Payload), "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"))
			}
			s, _, _, feed := testSummaryTaskFixture(t, entries)
			for _, e := range entries {
				feed(e)
				if e.ID < 4 {
					continue
				}
				page, err := s.CompactNewestWindow(context.Background(), testStoreA, 10)
				if err != nil || len(page.Headers) != 2 {
					t.Fatal(page, err)
				}
				testSummaryTask(t, page.Headers[0], "task")
				want := ""
				if e.ID == 5 {
					want = "task"
				}
				testSummaryTask(t, page.Headers[1], want)
			}
		})
	}
}

func TestSummaryTaskResume(t *testing.T) {
	entries := []history.Entry{
		testMain(1, "tool_use", `,"tool_use_id":"parent","name":"Agent"`),
		testEntry(2, "background_task_started", `{"task_id":"root-task","tool_call_id":"parent"}`),
		testChild(3, "tool_use", "parent", `,"tool_use_id":"child","name":"Agent"`),
		testEntry(4, "background_task_started", `{"task_id":"child-task","tool_call_id":"child"}`),
		testEntry(5, "background_task_updated", `{"task_id":"child-task","status":"completed"}`),
	}
	for _, cut := range []int{0, 3, len(entries)} {
		t.Run(string(rune('0'+cut)), func(t *testing.T) {
			s, _, _, _ := testStoreResumeSeed(t, entries, cut, true)
			lookup := s.lookup
			var closedReads atomic.Bool
			s.lookup = func(ctx context.Context, id conversations.ConversationID, ids []uint64) ([]history.Entry, error) {
				if closedReads.Load() {
					t.Error("summary read closed history")
					return nil, ErrUnavailable
				}
				return lookup(ctx, id, ids)
			}
			snap := testCheckpointLoad(t, s, testStoreA, uint64(len(entries)))
			for range 2 {
				closedReads.Store(true)
				root, err := s.CompactNewestWindow(context.Background(), testStoreA, 10)
				if err != nil || len(root.Headers) != 1 {
					t.Fatal(root, err)
				}
				testSummaryTask(t, root.Headers[0], "root-task")
				children, err := s.ChildPage(context.Background(), testStoreA, snap.Epoch, 1, root.Headers[0].Rev, 10, nil)
				if err != nil || len(children.Items) != 1 {
					t.Fatal(children, err)
				}
				testSummaryTask(t, children.Items[0], "child-task")
				if children.Items[0].Status != "finished" || children.Items[0].Active || children.Items[0].EndedOrder != 5 {
					t.Fatal("lost lifecycle", children.Items[0])
				}
				closedReads.Store(false)
				if err := s.Unload(testStoreA); err != nil {
					t.Fatal(err)
				}
				snap = testCheckpointLoad(t, s, testStoreA, uint64(len(entries)))
			}
		})
	}
}

func TestSummaryTaskFacts(t *testing.T) {
	i := Item{ID: 1, Rev: 1, Kind: "agent", Content: json.RawMessage(`{"task_id":"forged"}`)}
	for _, r := range []contentRecipe{
		{},
		{creating: 2},
		{creating: 1, agent: true, fields: map[string]recipeField{"task_id": {value: json.RawMessage(`null`)}}},
		{creating: 1, agent: true, fields: map[string]recipeField{"task_id": {value: json.RawMessage(`5`)}}},
	} {
		h := prepareChildHeader(i)
		h.taskAssociation(i, r)
		var got ChildSummary
		h.apply(&got)
		if got.TaskID != nil {
			t.Fatal("unknown evidence acquired authority", got)
		}
	}
	h := prepareChildHeader(i)
	h.taskAssociation(i, contentRecipe{creating: 1})
	var first, second ChildSummary
	h.apply(&first)
	*first.TaskID = "mutated"
	h.apply(&second)
	if second.TaskID == nil || *second.TaskID != "" {
		t.Fatal("result aliases prepared header")
	}
	// A full row supplied without fold metadata cannot establish authority.
	s := testChildStore([]Item{{ID: 1, Rev: 1}, {ID: 2, Rev: 2, Parent: 1, Kind: "agent", Content: i.Content}})
	p, err := s.ChildPage(context.Background(), testStoreA, "epoch", 1, 1, 1, nil)
	if err != nil || len(p.Items) != 1 || p.Items[0].TaskID != nil {
		t.Fatal(p, err)
	}
}

func TestSummaryTaskShell(t *testing.T) {
	entries := []history.Entry{
		testMain(1, "tool_use", `,"tool_use_id":"c","name":"Bash","input":{"run_in_background":true},"task_id":"forged"`),
		testMain(2, "tool_result", `,"tool_use_id":"c","is_error":false,"result":"task forged"`),
		testTask(3, "background_task_started", `,"tool_call_id":"c"`),
	}
	s, _, _, feed := testSummaryTaskFixture(t, entries)
	for _, e := range entries {
		feed(e)
		p, err := s.CompactNewestWindow(context.Background(), testStoreA, 10)
		if err != nil || len(p.Headers) != 1 || p.Headers[0].Kind != "tool_call" {
			t.Fatal(p, err)
		}
		want := ""
		if e.ID == 3 {
			want = "task"
		}
		testSummaryTask(t, p.Headers[0], want)
	}
}

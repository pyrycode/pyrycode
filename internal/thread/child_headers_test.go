package thread

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
)

func TestChildHeaderFacts(t *testing.T) {
	for _, tc := range []struct {
		status, text, want string
		known              bool
	}{
		{"running", "\t\x00\n", "Tool call", false},
		{"failed", "broken", "broken", true},
		{"denied", "no", "no", true},
		{"done", "ok", "ok", true},
		{"completed", "ok", "ok", true},
		{"interrupted", "unknown", "unknown", false},
		{"done", strings.Repeat("界", 201), strings.Repeat("界", 199) + "…", true},
		{"done", "a\x00b\n c", "a b c", true},
		{"done", "\xffok", "�ok", true},
		{"done", strings.Repeat(" ", 300) + "suffix", "suffix", true},
	} {
		t.Run(tc.status+tc.want[:min(len(tc.want), 8)], func(t *testing.T) {
			s := testChildStore([]Item{{ID: 1, Rev: 1}, {ID: 2, Rev: 7, Parent: 1, Kind: "tool_call", Status: tc.status, Summary: tc.text, NoChild: true}})
			p, err := s.ChildPage(context.Background(), testStoreA, "epoch", 1, 1, 1, nil)
			if err != nil {
				t.Fatal(err)
			}
			r := p.Items[0]
			if r.Headline != tc.want || utf8.RuneCountInString(r.Headline) > 200 || (r.ErrorSummary != nil) != tc.known || r.Rev != 7 || !r.NoChild {
				t.Fatalf("header: %#v", r)
			}
			if tc.known && ((*r.ErrorSummary != "") != (tc.status == "failed" || tc.status == "denied")) {
				t.Fatal("wrong error presence", *r.ErrorSummary)
			}
			if r.PublicChildCount == nil || *r.PublicChildCount != 0 || r.HasPublicChildren == nil || *r.HasPublicChildren || r.SubtreeVersion == nil {
				t.Fatal("missing known empty facts", r)
			}
			*r.PublicChildCount = 99
			*r.HasPublicChildren = true
			*r.SubtreeVersion = 99
			if r.ErrorSummary != nil {
				*r.ErrorSummary = "mutated"
			}
			again, _ := s.ChildPage(context.Background(), testStoreA, "epoch", 1, 1, 1, nil)
			if *again.Items[0].PublicChildCount != 0 || *again.Items[0].HasPublicChildren || *again.Items[0].SubtreeVersion == 99 || again.Items[0].ErrorSummary != nil && *again.Items[0].ErrorSummary == "mutated" {
				t.Fatal("result aliases publication")
			}
		})
	}
}

func TestChildHeaderFreshness(t *testing.T) {
	f, c, s, w := testPublicationFixture(8)
	parents := []uint64{0, 1, 2, 3, 1, 5, 1, 0}
	publish := func(affected ...int) {
		f.version++
		for _, i := range affected {
			f.markAffected(i)
			f.captureAffected = append(f.captureAffected, i)
			f.touches[f.items[i].ID] = f.version
		}
		items, changes, err := c.capture(f)
		if err != nil {
			t.Fatal(err)
		}
		changes.Epoch = "epoch"
		s.publish(context.Background(), testStoreA, w, items, changes, f, c)
	}
	for i, parent := range parents {
		f.items[i].Parent = parent
		f.items[i].Kind = "agent"
		f.items[i].Status = "running"
		f.items[i].Shown, f.items[i].Active = true, true
	}
	publish(0, 1, 2, 3, 4, 5, 6, 7)
	read := func(parent uint64) ChildPageResult {
		p, err := s.ChildPage(context.Background(), testStoreA, "epoch", parent, f.items[parent-1].Rev, 10, nil)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	before := read(1)
	snap, old := s.captureQuery(testStoreA)
	oldStore := NewStore(nil)
	oldStore.workers[testStoreA] = &conversationWorker{snapshot: snap, query: old}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 50 {
			page, err := oldStore.ChildPage(context.Background(), testStoreA, "epoch", 1, before.ParentRev, 10, nil)
			if err != nil || !reflect.DeepEqual(page, before) {
				t.Error("old public header capture changed", err)
				return
			}
		}
	}()
	defer func() { <-done }()
	header := func(id uint64) ChildSummary {
		for _, row := range read(parents[id-1]).Items {
			if row.ID == id {
				return row
			}
		}
		t.Fatalf("missing header %d", id)
		return ChildSummary{}
	}
	for _, tc := range []struct {
		name     string
		change   func()
		affected []int
		stale    []uint64
	}{
		{"nested content", func() { f.items[3].Summary = "changed" }, []int{3}, []uint64{2, 3}},
		{"hidden still public", func() { f.items[3].Shown = false }, []int{3}, []uint64{2, 3}},
		{"complete", func() { f.items[3].Status = "failed"; f.items[3].Active = false }, []int{3}, []uint64{2, 3}},
		{"reparent", func() { f.items[3].Parent = 5 }, []int{3}, []uint64{2, 3, 5}},
		{"withdraw", func() { f.publicVisible[4] = false; f.messages[4] = -1 }, []int{3}, []uint64{5}},
		{"private change", func() { f.items[3].Summary = "private" }, []int{3}, nil},
		{"late appearance", func() { f.publicVisible[4] = true; delete(f.messages, 4) }, []int{3}, []uint64{5}},
		{"new nested child", func() {
			f.items = append(f.items, Item{ID: 9, Rev: 9, Parent: 3, Kind: "tool_call", Status: "running"})
			f.publicVisible[9] = true
		}, []int{8}, []uint64{2, 3}},
		{"no item progress", func() {}, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previous := make(map[uint64]uint64)
			for _, id := range []uint64{2, 3, 5, 6, 7} {
				previous[id] = *header(id).SubtreeVersion
			}
			tc.change()
			publish(tc.affected...)
			for id, version := range previous {
				row := header(id)
				wantChange := false
				for _, changed := range tc.stale {
					wantChange = wantChange || id == changed
				}
				if (*row.SubtreeVersion != version) != wantChange {
					t.Fatalf("freshness %d: %d -> %d", id, version, *row.SubtreeVersion)
				}
				if row.ErrorSummary != nil {
					t.Fatal("descendant failure attributed to ancestor", row)
				}
			}
			for _, parent := range f.Items() {
				testChildOracle(t, s, testStoreA, f.Items(), parent, 2)
			}
			if read(1).Items[0].Rev != before.Items[0].Rev {
				t.Fatal("ancestor revision mutated")
			}
			if page, err := oldStore.ChildPage(context.Background(), testStoreA, "epoch", 1, 1, 10, nil); err != nil || !reflect.DeepEqual(page, before) {
				t.Fatal("old capture changed")
			}
		})
	}
}

func TestChildHeaderReferenceOutcomes(t *testing.T) {
	s, h := testThreadStore(t)
	entries := []struct{ typ, body string }{
		{"tool_use", `,"tool_use_id":"parent","name":"Agent"`},
		{"tool_use", `,"tool_use_id":"a","name":"Read"`},
		{"tool_result", `,"tool_use_id":"a","is_error":true,"result_detail":"failure"`},
		{"tool_use", `,"tool_use_id":"b","name":"Read"`},
		{"tool_result", `,"tool_use_id":"b","is_error":false`},
		{"tool_use", `,"tool_use_id":"c","name":"Read"`},
		{"tool_denied", `,"tool_use_id":"c","message":"no"`},
	}
	for i, e := range entries {
		entry := testChild(0, e.typ, "parent", e.body)
		if i == 0 {
			entry = testMain(0, e.typ, e.body)
		}
		testStoreAppend(t, h, testStoreA, entry)
	}
	if err := s.Load(context.Background(), testStoreA); err != nil {
		t.Fatal(err)
	}
	snap := testStoreWait(t, s, testStoreA, StateUsable, 7)
	s.lookup = func(context.Context, conversations.ConversationID, []uint64) ([]history.Entry, error) {
		t.Error("header read hydrated history")
		return nil, nil
	}
	p, err := s.ChildPage(context.Background(), testStoreA, snap.Epoch, 1, snap.Items[0].Rev, 10, nil)
	if err != nil || len(p.Items) != 3 {
		t.Fatal(p, err)
	}
	for i, r := range p.Items {
		if r.ErrorSummary == nil || (*r.ErrorSummary != "") != (i != 1) {
			t.Fatal("reference outcome", r)
		}
	}
}

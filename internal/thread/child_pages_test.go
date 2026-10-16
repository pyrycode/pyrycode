package thread

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
)

func testChildStore(items []Item) *Store {
	s := NewStore(nil)
	s.workers[testStoreA] = &conversationWorker{snapshot: Snapshot{State: StateUsable, Epoch: "epoch", Version: 100}, query: newQueryIndex(items, nil)}
	return s
}

func testChildOracle(t *testing.T, s *Store, id conversations.ConversationID, items []Item, parent Item, limit int) {
	t.Helper()
	var want []uint64
	children := slices.DeleteFunc(slices.Clone(items), func(i Item) bool { return i.Parent != parent.ID })
	slices.SortFunc(children, func(a, b Item) int {
		if a.Order < b.Order {
			return -1
		}
		if a.Order > b.Order {
			return 1
		}
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	for _, i := range children {
		want = append(want, i.ID)
	}
	var got []uint64
	var cursor *ChildCursor
	for pages := 0; ; pages++ {
		if pages > len(items)+1 {
			t.Fatal("pagination did not terminate")
		}
		snap := s.Status(id)
		page, err := s.ChildPage(context.Background(), id, snap.Epoch, parent.ID, parent.Rev, limit, cursor)
		if err != nil || page.State != StateUsable || page.Version != snap.Version || page.More == page.Complete || len(page.Items) > min(limit, 256) {
			t.Fatalf("page: %#v %v", page, err)
		}
		for _, row := range page.Items {
			got = append(got, row.ID)
		}
		if page.Complete {
			break
		}
		next, err := page.CursorAfter(len(page.Items))
		if err != nil {
			t.Fatal(err)
		}
		cursor = &next
	}
	if !slices.Equal(got, want) {
		t.Fatalf("children of %d: %v want %v", parent.ID, got, want)
	}
}

func TestChildPageSelection(t *testing.T) {
	items := []Item{{ID: 1, Rev: 9, Kind: "user_message"}, {ID: 2, Rev: 2, Parent: 1, Order: 0, Summary: "first"}, {ID: 3, Rev: 3, Parent: 1, Order: 0}, {ID: 4, Rev: 4, Parent: 1, Order: 2, Shown: false, Status: "dropped"}, {ID: 5, Rev: 5, Parent: 1, Order: 2}, {ID: 6, Rev: 6, Parent: 1, Order: 2}, {ID: 7, Rev: 7, Parent: 1, Order: 3}, {ID: 8, Rev: 8, Parent: 2}, {ID: 9, Rev: 9, Active: true}}
	s := testChildStore(items)
	s.workers[testStoreB] = &conversationWorker{snapshot: Snapshot{State: StateUsable, Epoch: "other", Version: 11}, query: newQueryIndex([]Item{{ID: 1, Rev: 9}, {ID: 2, Rev: 1, Parent: 1, Order: 8}}, nil)}
	testChildOracle(t, s, testStoreA, items, items[0], 2)
	testChildOracle(t, s, testStoreB, []Item{{ID: 2, Rev: 1, Parent: 1, Order: 8}}, items[0], 1)
	page, err := s.ChildPage(context.Background(), testStoreA, "epoch", 1, 9, 4, nil)
	if err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= len(page.Items); n++ {
		c, e := page.CursorAfter(n)
		if e != nil {
			t.Fatal(e)
		}
		rest, e := s.ChildPage(context.Background(), testStoreA, "epoch", 1, 9, 256, &c)
		if e != nil || len(rest.Items) != 6-n || rest.Items[0].ID != uint64(n+2) {
			t.Fatal(n, rest, e)
		}
	}
	for _, n := range []int{-1, 0, 5} {
		if _, e := page.CursorAfter(n); !errors.Is(e, ErrInvalidChildCursor) {
			t.Fatal(n, e)
		}
	}
	empty, e := s.ChildPage(context.Background(), testStoreA, "epoch", 9, 9, 1, nil)
	if e != nil || !empty.Complete || empty.More || len(empty.Items) != 0 {
		t.Fatal(empty, e)
	}
	many := []Item{{ID: 1, Rev: 1}}
	for i := 2; i < 600; i++ {
		many = append(many, Item{ID: uint64(i), Rev: 1, Parent: 1, Order: 0})
	}
	s = testChildStore(many)
	for _, limit := range []int{1, 256, 257, int(^uint(0) >> 1)} {
		p, e := s.ChildPage(context.Background(), testStoreA, "epoch", 1, 1, limit, nil)
		if e != nil || len(p.Items) != min(limit, 256) || !p.More {
			t.Fatal(len(p.Items), e)
		}
	}
}

func TestChildPageSummaries(t *testing.T) {
	for _, text := range []string{"", " \x00\t", "hello\n world", strings.Repeat("界", 1000), strings.Repeat("x", 255) + "界", "\xff\xfeok", strings.Repeat(" ", 256) + "invisible suffix"} {
		item := Item{ID: 2, Order: 0, Rev: 8, EndedOrder: 7, Kind: "tool_use", Subtype: "Read", Session: "s", Agent: "codex", NoChild: true, Turn: "turn", Parent: 1, Status: "done", Active: true, Shown: false, Summary: text, Content: []byte(`{}`)}
		s := testChildStore([]Item{{ID: 1, Rev: 1}, item})
		s.lookup = func(context.Context, conversations.ConversationID, []uint64) ([]history.Entry, error) {
			t.Fatal("summary hydrated body")
			return nil, nil
		}
		page, e := s.ChildPage(context.Background(), testStoreA, "epoch", 1, 1, 1, nil)
		if e != nil {
			t.Fatal(e)
		}
		row := page.Items[0]
		if row.ID != item.ID || row.Order != item.Order || row.Rev != item.Rev || row.EndedOrder != item.EndedOrder || row.Kind != item.Kind || row.Subtype != item.Subtype || row.Session != item.Session || row.Agent != item.Agent || row.NoChild != item.NoChild || row.Turn != item.Turn || row.Parent != item.Parent || row.Status != item.Status || row.Active != item.Active || row.Shown != item.Shown || !row.DetailOmitted || !utf8.ValidString(row.Headline) || len(row.Headline) > 256 || row.Headline == "" {
			t.Fatalf("bad summary: %#v", row)
		}
		if strings.Contains(row.Headline, "invisible") {
			t.Fatal("scanned past prefix")
		}
		if text == "hello\n world" && row.Headline != "hello world" {
			t.Fatal(row.Headline)
		}
		page.Items[0].Status = "changed"
		again, _ := s.ChildPage(context.Background(), testStoreA, "epoch", 1, 1, 1, nil)
		if again.Items[0].Status != "done" {
			t.Fatal("result aliases capture")
		}
	}
}

func TestChildPageRefusals(t *testing.T) {
	items := []Item{{ID: 1, Rev: 9}, {ID: 2, Rev: 2, Parent: 1}, {ID: 3, Rev: 3, Parent: 1}}
	s := testChildStore(items)
	p, _ := s.ChildPage(context.Background(), testStoreA, "epoch", 1, 9, 1, nil)
	cursor, _ := p.CursorAfter(1)
	for _, tc := range []struct {
		name        string
		id          conversations.ConversationID
		epoch       string
		parent, rev uint64
		limit       int
		cursor      *ChildCursor
		want        error
	}{
		{"id", "../../bad", "epoch", 1, 9, 1, nil, history.ErrInvalidID},
		{"epoch", testStoreA, "", 1, 9, 1, nil, ErrInvalidChildScope},
		{"parent", testStoreA, "epoch", 0, 9, 1, nil, ErrInvalidChildScope},
		{"revision", testStoreA, "epoch", 1, 0, 1, nil, ErrInvalidChildScope},
		{"limit", testStoreA, "epoch", 1, 9, 0, nil, ErrInvalidLimit},
		{"negative", testStoreA, "epoch", 1, 9, -1, nil, ErrInvalidLimit},
		{"stale epoch", testStoreA, "other", 1, 9, 1, nil, ErrStaleChildPage},
		{"stale revision", testStoreA, "epoch", 1, 8, 1, nil, ErrStaleChildPage},
		{"missing private", testStoreA, "epoch", 10, 1, 1, nil, ErrChildParentNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, e := s.ChildPage(context.Background(), tc.id, tc.epoch, tc.parent, tc.rev, tc.limit, tc.cursor)
			if !errors.Is(e, tc.want) || !reflect.DeepEqual(got, ChildPageResult{}) {
				t.Fatal(got, e)
			}
		})
	}
	for _, mutate := range []func(*ChildCursor){func(c *ChildCursor) { c.ConversationID = testStoreB }, func(c *ChildCursor) { c.Parent = 2 }, func(c *ChildCursor) { c.ParentRev++ }, func(c *ChildCursor) { c.Epoch = "wrong" }, func(c *ChildCursor) { c.Version-- }, func(c *ChildCursor) { c.ID = 0 }, func(c *ChildCursor) { c.ID = 1 }, func(c *ChildCursor) { c.Order++ }} {
		bad := cursor
		mutate(&bad)
		got, e := s.ChildPage(context.Background(), testStoreA, "epoch", 1, 9, 1, &bad)
		if e == nil || !reflect.DeepEqual(got, ChildPageResult{}) {
			t.Fatal(got, e)
		}
	}
	s.workers[testStoreA].snapshot.Version++
	if got, e := s.ChildPage(context.Background(), testStoreA, "epoch", 1, 9, 1, &cursor); !errors.Is(e, ErrStaleChildPage) || got.Complete || len(got.Items) != 0 {
		t.Fatal(got, e)
	}
	for _, state := range []SnapshotState{StateNotLoaded, StateRebuilding, StateUnavailable} {
		s.workers[testStoreA].snapshot = Snapshot{State: state}
		if state == StateUnavailable {
			s.workers[testStoreA].snapshot.Err = ErrUnavailable
		}
		got, e := s.ChildPage(context.Background(), testStoreA, "epoch", 1, 9, 1, nil)
		if e != nil || !reflect.DeepEqual(got, ChildPageResult{State: state, Err: s.workers[testStoreA].snapshot.Err}) {
			t.Fatal(got, e)
		}
	}
}

func TestChildPageIncrementalOracle(t *testing.T) {
	f := New("a")
	f.captureSet = make(map[int]struct{})
	c, s, w := &publicationCapture{}, NewStore(nil), &conversationWorker{changed: make(chan struct{})}
	s.workers[testStoreA] = w
	entries := []history.Entry{
		testAcceptance(1, "phone"), testMessage(2), testMessage(3),
		testSendOutcome(4, "send_delivered", `,"accepted_entry_id":1,"delivery_entry_id":2`, "delivered"),
		testMain(5, "tool_use", `,"tool_use_id":"parent","name":"Agent"`),
		testChild(6, "tool_use", "missing", `,"tool_use_id":"nested","name":"Task"`),
		testChild(7, "assistant_delta", "nested", `,"text":"deep"`),
		testChild(8, "tool_result", "parent", `,"tool_use_id":"nested","is_error":false`),
	}
	publish := func() {
		items, changes, e := c.capture(f)
		if e != nil {
			t.Fatal(e)
		}
		changes.Epoch = "epoch"
		s.publish(context.Background(), testStoreA, w, items, changes, f, c.affected)
		for _, parent := range f.Items() {
			testChildOracle(t, s, testStoreA, f.Items(), parent, 1)
		}
	}
	for _, e := range entries {
		testFeed(t, f, e)
		publish()
		if e.ID == 7 {
			if p, err := s.ChildPage(context.Background(), testStoreA, "epoch", 6, 6, 1, nil); !errors.Is(err, ErrChildParentNotFound) || p.Complete {
				t.Fatal(p, err)
			}
		}
	}
	// Exercise publication maintenance independently of fold event syntax.
	f, c, s, w = testPublicationFixture(30)
	for step := range 30 {
		i := 2 + step%(len(f.items)-2)
		f.version++
		f.items[i].Parent = uint64(1 + step%2)
		f.items[i].Order = uint64(step % 3)
		f.items[i].Rev = f.version
		f.touches[f.items[i].ID] = f.version
		f.captureAffected = append(f.captureAffected, i)
		f.publicVisible[f.items[i].ID] = step%4 != 0
		if step%4 == 0 {
			f.messages[f.items[i].ID] = -1
		} else {
			delete(f.messages, f.items[i].ID)
		}
		publish()
	}
}

func TestChildPageCaptureLifecycle(t *testing.T) {
	s, h := testThreadStore(t)
	lookup := s.lookup
	var lookups atomic.Uint64
	s.lookup = func(ctx context.Context, id conversations.ConversationID, ids []uint64) ([]history.Entry, error) {
		lookups.Add(1)
		return lookup(ctx, id, ids)
	}
	testStoreAppend(t, h, testStoreA, testMain(0, "tool_use", `,"tool_use_id":"parent","name":"Agent"`))
	testStoreAppend(t, h, testStoreA, testChild(0, "assistant_delta", "parent", `,"text":"captured"`))
	if e := s.Load(context.Background(), testStoreA); e != nil {
		t.Fatal(e)
	}
	snap := testStoreWait(t, s, testStoreA, StateUsable, 2)
	before := lookups.Load()
	want, e := s.ChildPage(context.Background(), testStoreA, snap.Epoch, 1, 1, 10, nil)
	if e != nil || len(want.Items) != 1 || lookups.Load() != before {
		t.Fatal(want, e)
	}
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once, cleanup sync.Once
	defer cleanup.Do(func() { close(release) })
	s.mu.Lock()
	s.workers[testStoreA].query.work = func(bool) { once.Do(func() { close(entered); <-release }) }
	s.mu.Unlock()
	var got ChildPageResult
	var err error
	go func() {
		defer close(done)
		got, err = s.ChildPage(context.Background(), testStoreA, snap.Epoch, 1, 1, 10, nil)
	}()
	testStoreSignal(t, entered)
	testStoreAppend(t, h, testStoreA, testChild(0, "assistant_delta", "parent", `,"text":"new"`))
	testStoreWait(t, s, testStoreA, StateUsable, 3)
	if e := s.Unload(testStoreA); e != nil {
		t.Fatal(e)
	}
	if p, e := s.ChildPage(context.Background(), testStoreA, snap.Epoch, 1, 1, 1, nil); e != nil || p.State != StateNotLoaded || p.Complete {
		t.Fatal(p, e)
	}
	if e := s.Load(context.Background(), testStoreA); e != nil {
		t.Fatal(e)
	}
	testStoreWait(t, s, testStoreA, StateUsable, 3)
	cleanup.Do(func() { close(release) })
	testStoreSignal(t, done)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal(got, want, err)
	}
	cursor, _ := got.CursorAfter(1)
	if p, e := s.ChildPage(context.Background(), testStoreA, snap.Epoch, 1, 1, 1, &cursor); e == nil || p.Complete {
		t.Fatal(p, e)
	}
}

func TestChildPageCancellation(t *testing.T) {
	items := []Item{{ID: 1, Rev: 1}}
	for i := 2; i <= 16; i++ {
		items = append(items, Item{ID: uint64(i), Parent: 1, Rev: 1})
	}
	s := testChildStore(items)
	q := s.workers[testStoreA].query
	visits := 0
	q.work = func(bool) { visits++ }
	page, _ := s.ChildPage(context.Background(), testStoreA, "epoch", 1, 1, 16, nil)
	lastVisit := visits
	// Cancel at every metadata visit, including after selected rows accumulated.
	for at := 1; at <= lastVisit; at++ {
		ctx, cancel := context.WithCancel(context.Background())
		visits = 0
		q.work = func(bool) {
			visits++
			if visits == at {
				cancel()
			}
		}
		got, e := s.ChildPage(ctx, testStoreA, "epoch", 1, 1, 16, nil)
		cancel()
		if !errors.Is(e, context.Canceled) || !reflect.DeepEqual(got, ChildPageResult{}) {
			t.Fatal(at, got, e)
		}
	}
	q.work = nil
	if p, e := s.ChildPage(context.Background(), testStoreA, "epoch", 1, 1, 16, nil); e != nil || !reflect.DeepEqual(p, page) {
		t.Fatal(p, e)
	}
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	var cleanup sync.Once
	defer cleanup.Do(func() { close(release) })
	q.work = func(bool) {
		if first.CompareAndSwap(false, true) {
			close(entered)
			<-release
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var got ChildPageResult
	var err error
	go func() { defer close(done); got, err = s.ChildPage(ctx, testStoreA, "epoch", 1, 1, 16, nil) }()
	testStoreSignal(t, entered)
	cancel()
	if p, e := s.ChildPage(context.Background(), testStoreA, "epoch", 1, 1, 16, nil); e != nil || !reflect.DeepEqual(p, page) {
		t.Fatal(p, e)
	}
	cleanup.Do(func() { close(release) })
	testStoreSignal(t, done)
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, ChildPageResult{}) {
		t.Fatal(got, err)
	}
}

func TestChildPageBoundedWork(t *testing.T) {
	for _, n := range []int{1024, 16384} {
		items := []Item{{ID: 1, Rev: 1}, {ID: 2, Rev: 2, Parent: 1}}
		body := strings.Repeat("x", n*64)
		items[1].Summary, items[1].Content = body, []byte(body)
		for i := 3; i < n; i++ {
			parent := uint64(0)
			if i%2 == 0 {
				parent = 2
			}
			items = append(items, Item{ID: uint64(i), Rev: 1, Order: uint64(i % 5), Parent: parent, Summary: body, Content: []byte(`{}`)})
		}
		s := testChildStore(items)
		var reads, lookups atomic.Int64
		s.workers[testStoreA].query.work = func(p bool) {
			if !p {
				reads.Add(1)
			}
		}
		s.lookup = func(context.Context, conversations.ConversationID, []uint64) ([]history.Entry, error) {
			lookups.Add(1)
			return nil, nil
		}
		p, e := s.ChildPage(context.Background(), testStoreA, "epoch", 1, 1, 3, nil)
		if e != nil || !p.Complete || len(p.Items) != 1 || reads.Load() > 100 || lookups.Load() != 0 {
			t.Fatal(n, p, e, reads.Load(), lookups.Load())
		}
		t.Logf("N=%d visits=%d history lookups=%d", n, reads.Load(), lookups.Load())
	}
}

func TestChildPagePreparationWork(t *testing.T) {
	var counts []int
	for _, n := range []int{1024, 16384} {
		f, c, s, w := testPublicationFixture(n)
		count := 0
		s.queryWork = func(preparing bool) {
			if preparing {
				count++
			}
		}
		f.version++
		i := len(f.items) - 1
		f.items[i].Parent, f.items[i].Order, f.items[i].Rev = 1, 0, f.version
		f.touches[f.items[i].ID] = f.version
		f.captureAffected = append(f.captureAffected, i)
		items, changes, e := c.capture(f)
		if e != nil {
			t.Fatal(e)
		}
		s.publish(context.Background(), testStoreA, w, items, changes, f, c.affected)
		counts = append(counts, count)
	}
	if counts[1] > 600 || counts[1] >= 2*counts[0] {
		t.Fatal("membership preparation scales with inventory", counts)
	}
	t.Logf("incremental metadata visits: %v", counts)
}

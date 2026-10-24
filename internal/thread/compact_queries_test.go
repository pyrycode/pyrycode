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

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
)

func testCompactOracle(t *testing.T, s *Store, facts []Item, upper uint64, limit int, newest bool) CompactQueryResult {
	t.Helper()
	p, err := s.compactQuery(context.Background(), testStoreA, upper, limit, newest)
	if err != nil || p.State != StateUsable || p.ConversationID != testStoreA || p.Projection != "compact-v1" || p.Version != s.Status(testStoreA).Version || p.Epoch != s.Status(testStoreA).Epoch {
		t.Fatal(p, err)
	}
	if newest {
		upper = p.Version + 1
	}
	roots := slices.DeleteFunc(slices.Clone(facts), func(i Item) bool { return i.Parent != 0 || i.Order == 0 || i.Order >= upper })
	slices.SortFunc(roots, func(a, b Item) int {
		if a.Order < b.Order {
			return -1
		}
		if a.Order > b.Order {
			return 1
		}
		return 0
	})
	lower := uint64(0)
	older := false
	if len(roots) > 0 {
		lower = roots[max(0, len(roots)-min(limit, MaxQueryItems))].Order
		older = roots[0].Order < lower
	}
	if p.LowerOrder != lower || p.Continuation != lower || p.UpperOrder != upper || p.OlderExists != older {
		t.Fatal("bounds", p)
	}
	want := make(map[uint64]Item)
	byID := make(map[uint64]Item)
	for _, i := range facts {
		byID[i.ID] = i
		if i.Parent == 0 && i.Order != 0 && i.Order >= lower && i.Order < upper {
			want[i.ID] = i
		}
	}
	if newest {
		for _, i := range facts {
			if !i.Active {
				continue
			}
			for i.Parent != 0 {
				var ok bool
				i, ok = byID[i.Parent]
				if !ok {
					break
				}
			}
			if i.ID != 0 && i.Parent == 0 {
				want[i.ID] = i
			}
		}
	}
	seen := make(map[uint64]bool)
	for _, i := range p.Items {
		w, ok := want[i.ID]
		if !ok || w.Kind == "tool_call" || w.Kind == "agent" || !reflect.DeepEqual(i, w) || seen[i.ID] {
			t.Fatal("message", i, w)
		}
		seen[i.ID] = true
	}
	for _, h := range p.Headers {
		i, ok := want[h.ID]
		metadata := Item{ID: h.ID, Order: h.Order, Rev: h.Rev, EndedOrder: h.EndedOrder, Kind: h.Kind, Subtype: h.Subtype, Session: h.Session, Agent: h.Agent, NoChild: h.NoChild, Turn: h.Turn, Parent: h.Parent, Status: h.Status, Active: h.Active, Shown: h.Shown, Summary: i.Summary, Content: i.Content}
		count := uint64(0)
		for _, child := range facts {
			if child.Parent == h.ID {
				count++
			}
		}
		if !ok || !reflect.DeepEqual(metadata, i) || (i.Kind != "tool_call" && i.Kind != "agent") || seen[h.ID] || h.Parent != 0 || !h.DetailOmitted || h.PublicChildCount == nil || *h.PublicChildCount != count || h.HasPublicChildren == nil || *h.HasPublicChildren != (count > 0) || h.SubtreeVersion == nil {
			t.Fatal("header", h, i)
		}
		seen[h.ID] = true
	}
	if len(seen) != len(want) {
		t.Fatal("membership", seen, want)
	}
	return p
}

func TestCompactSelection(t *testing.T) {
	items := []Item{{ID: 1, Rev: 1, Kind: "agent", Summary: "root", Status: "done"}, {ID: 2, Rev: 2, Parent: 1, Kind: "tool_call"}, {ID: 3, Rev: 3, Parent: 2, Active: true}, {ID: 4, Rev: 4, Order: 4, Kind: "tool_call", Summary: strings.Repeat("界", 300)}, {ID: 5, Rev: 5, Order: 7, Kind: "user_message", Status: "dropped", Content: []byte(`{"text":"kept"}`)}, {ID: 6, Rev: 6, Order: 7, Kind: "tool_call"}, {ID: 7, Rev: 7, Order: 90, Kind: "assistant_message"}}
	for i := 8; i < 500; i++ {
		items = append(items, Item{ID: uint64(i), Rev: uint64(i), Order: 8, Parent: 2, Active: true})
	}
	items[0].Session, items[0].Agent, items[0].Turn, items[0].Subtype, items[0].EndedOrder, items[0].NoChild = "session", "codex", "turn", "Task", 19, true
	items[6].Active = true
	s := testChildStore(items)
	for _, newest := range []bool{false, true} {
		for _, upper := range []uint64{0, 1, 7, 8, 101} {
			testCompactOracle(t, s, items, upper, 2, newest)
		}
	}
	p := testCompactOracle(t, s, items, 8, 1, false)
	if len(p.Items)+len(p.Headers) != 2 {
		t.Fatal("lower ties omitted", p)
	}
	var ids []uint64
	for upper := uint64(101); ; {
		p = testCompactOracle(t, s, items, upper, 1, false)
		for _, i := range p.Items {
			ids = append(ids, i.ID)
		}
		for _, h := range p.Headers {
			ids = append(ids, h.ID)
		}
		if !p.OlderExists {
			break
		}
		upper = p.Continuation
	}
	slices.Sort(ids)
	if !slices.Equal(ids, []uint64{4, 5, 6, 7}) {
		t.Fatal(ids)
	}
	p = testCompactOracle(t, s, items, 7, 1, false)
	if p.Headers[0].Headline != strings.Repeat("界", 199)+"…" || p.Headers[0].ErrorSummary != nil || *p.Headers[0].PublicChildCount != 0 || *p.Headers[0].HasPublicChildren {
		t.Fatal("bounded/unknown/known empty header facts", p)
	}
	p, _ = s.CompactNewestWindow(context.Background(), testStoreA, 1)
	if len(p.Headers) != 1 || p.Headers[0].Active || p.Headers[0].Headline != "root" || p.Headers[0].ErrorSummary == nil || *p.Headers[0].ErrorSummary != "" {
		t.Fatal(p)
	}
	*p.Headers[0].PublicChildCount = 999
	*p.Headers[0].HasPublicChildren = false
	*p.Headers[0].SubtreeVersion = 999
	*p.Headers[0].ErrorSummary = "mutated"
	testCompactOracle(t, s, items, 0, 1, true)
	unordered := testChildStore(items[:3])
	zero, _ := unordered.CompactNewestWindow(context.Background(), testStoreA, 1)
	if len(zero.Headers) != 1 || zero.LowerOrder != 0 || zero.Continuation != 0 || zero.UpperOrder != 101 || zero.OlderExists {
		t.Fatal("empty ordered window with extras", zero)
	}
	for i := range 300 {
		items = append(items, Item{ID: uint64(1000 + i), Order: uint64(1000 + i), Rev: 1})
	}
	s = testChildStore(items)
	p, _ = s.CompactHistoryPage(context.Background(), testStoreA, 2000, 10000)
	if len(p.Items)+len(p.Headers) != MaxQueryItems {
		t.Fatal("clamp", p)
	}
}

func TestCompactIncremental(t *testing.T) {
	f, c, s, w := testPublicationFixture(16)
	publish := func(indices ...int) {
		f.version++
		for _, i := range indices {
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
		testCompactOracle(t, s, f.Items(), 0, 1, true)
		testCompactOracle(t, s, f.Items(), 20, 3, false)
	}
	f.items[0].Kind, f.items[1].Kind = "agent", "tool_call"
	f.items[2].Parent, f.items[3].Parent = 1, 3
	publish(0, 1, 2, 3)
	snap, q := s.captureQuery(testStoreA)
	old := NewStore(nil)
	old.workers[testStoreA] = &conversationWorker{snapshot: snap, query: q}
	want, _ := old.CompactNewestWindow(context.Background(), testStoreA, MaxQueryItems)
	assertFresh := func(first, second uint64) {
		p, err := s.CompactHistoryPage(context.Background(), testStoreA, f.version+1, MaxQueryItems)
		if err != nil || len(p.Headers) != 2 || *p.Headers[0].SubtreeVersion != first || *p.Headers[1].SubtreeVersion != second {
			t.Fatal("root freshness", p, first, second, err)
		}
	}
	assertFresh(17, 17)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 30 {
			got, e := old.CompactNewestWindow(context.Background(), testStoreA, MaxQueryItems)
			if e != nil || !reflect.DeepEqual(got, want) {
				t.Error("capture changed", e)
			}
			_ = old.Snapshot(testStoreA)
		}
	}()
	defer func() { <-done }()
	for _, active := range []bool{true, false, true} {
		f.items[3].Active = active
		publish(3)
		assertFresh(f.version, 17)
	}
	f.items[2].Parent = 2
	publish(2)
	assertFresh(21, 21)
	f.publicVisible[4] = false
	f.messages[4] = -1
	publish(3)
	assertFresh(21, 22)
	f.publicVisible[4] = true
	delete(f.messages, 4)
	publish(3)
	assertFresh(21, 23)
	f.items[2].Parent = 0
	publish(2)
	assertFresh(21, 24)
	if f.items[0].Rev != 1 || f.items[1].Rev != 2 {
		t.Fatal("source revision changed")
	}
}

func TestCompactPublicMembership(t *testing.T) {
	f := New("a")
	f.captureSet = make(map[int]struct{})
	c, s, w := &publicationCapture{}, NewStore(nil), &conversationWorker{changed: make(chan struct{})}
	s.workers[testStoreA] = w
	for _, e := range []history.Entry{
		testAcceptance(1, "phone"), testMessage(2), testMessage(3),
		testSendOutcome(4, "send_delivered", `,"accepted_entry_id":1,"delivery_entry_id":2`, "delivered"),
		testMain(5, "tool_use", `,"tool_use_id":"parent","name":"Agent"`),
		testChild(6, "tool_use", "missing", `,"tool_use_id":"nested","name":"Task"`),
		testChild(7, "assistant_delta", "nested", `,"text":"deep"`),
		testChild(8, "tool_result", "parent", `,"tool_use_id":"nested","is_error":false`),
	} {
		testFeed(t, f, e)
		items, changes, err := c.capture(f)
		if err != nil {
			t.Fatal(err)
		}
		changes.Epoch = "epoch"
		s.publish(context.Background(), testStoreA, w, items, changes, f, c)
		testCompactOracle(t, s, f.Items(), 0, 1, true)
		testCompactOracle(t, s, f.Items(), 9, 2, false)
	}
}

func TestCompactPreparationWork(t *testing.T) {
	var counts []int
	for _, n := range []int{128, 8192} {
		f, c, s, w := testPublicationFixture(n)
		for i := 2; i < n; i++ {
			f.items[i].Parent, f.items[i].Active = 1, true
			f.captureAffected = append(f.captureAffected, i)
		}
		publish := func() {
			f.version++
			items, changes, err := c.capture(f)
			if err != nil {
				t.Fatal(err)
			}
			s.publish(context.Background(), testStoreA, w, items, changes, f, c)
		}
		publish()
		semantic, copies := 0, 0
		s.queryWork = func(preparing bool) {
			if preparing {
				semantic++
			}
		}
		s.queryCopies = func(n int) { copies += n }
		c.copied = s.queryCopies
		f.items[n-1].Parent, f.items[n-1].Active = 2, false
		f.captureAffected = append(f.captureAffected, n-1)
		publish()
		counts = append(counts, semantic)
		if copies < n || semantic > 1000 {
			t.Fatal("preparation/copy accounting", n, semantic, copies)
		}
		t.Logf("inventory=%d semantic=%d copied=%d", n, semantic, copies)
	}
	if counts[1] >= 3*counts[0] {
		t.Fatal("unrelated inventory rescan", counts)
	}
}

func TestCompactBoundedWork(t *testing.T) {
	for _, n := range []int{128, 8192} {
		items := []Item{{ID: 1, Rev: 1, Kind: "agent", Summary: strings.Repeat("x", n*64), Content: []byte(`{"secret":"body"}`)}, {ID: 2, Rev: 2, Order: 2, Kind: "user_message", Content: []byte(`{"text":"message"}`)}, {ID: 3, Rev: 3, Parent: 1, Kind: "tool_call"}}
		for i := 4; i < n; i++ {
			items = append(items, Item{ID: uint64(i), Rev: 1, Parent: 3, Active: true})
		}
		s := testChildStore(items)
		q := s.workers[testStoreA].query
		row, _ := q.lookup(2, true)
		row.recipe.creating = 2
		q.membership(row, true)
		q.membership(row, false)
		reads, prep, lookups := 0, 0, 0
		q.work = func(p bool) {
			if p {
				prep++
			} else {
				reads++
			}
		}
		s.lookup = func(_ context.Context, _ conversations.ConversationID, ids []uint64) ([]history.Entry, error) {
			lookups++
			if !slices.Equal(ids, []uint64{2}) {
				t.Fatal("unexpected reference", ids)
			}
			return []history.Entry{testMessage(2)}, nil
		}
		p, e := s.CompactNewestWindow(context.Background(), testStoreA, 1)
		if e != nil || len(p.Items) != 1 || len(p.Headers) != 1 || reads > 30 || prep != 0 || lookups != 1 {
			t.Fatal(n, p, e, reads, prep)
		}
		t.Logf("descendants=%d request-visits=%d preparation=%d", n-4, reads, prep)
	}
}

func TestCompactLifecycle(t *testing.T) {
	s := testChildStore([]Item{{ID: 1, Rev: 1, Order: 1, Kind: "tool_call"}, {ID: 2, Rev: 2, Order: 2}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if p, e := s.CompactNewestWindow(ctx, "bad/id", 0); !errors.Is(e, context.Canceled) || !reflect.DeepEqual(p, CompactQueryResult{}) {
		t.Fatal(p, e)
	}
	if _, e := s.CompactNewestWindow(context.Background(), "bad/id", 1); !errors.Is(e, history.ErrInvalidID) {
		t.Fatal(e)
	}
	for _, limit := range []int{-1, 0} {
		if _, e := s.CompactNewestWindow(context.Background(), testStoreA, limit); !errors.Is(e, ErrInvalidLimit) {
			t.Fatal(e)
		}
	}
	q := s.workers[testStoreA].query
	visits := 0
	q.work = func(bool) { visits++ }
	want, _ := s.CompactNewestWindow(context.Background(), testStoreA, 2)
	for at, total := 1, visits; at <= total; at++ {
		ctx, cancel := context.WithCancel(context.Background())
		visits = 0
		q.work = func(bool) {
			visits++
			if visits == at {
				cancel()
			}
		}
		p, e := s.CompactNewestWindow(ctx, testStoreA, 2)
		cancel()
		if !errors.Is(e, context.Canceled) || !reflect.DeepEqual(p, CompactQueryResult{}) {
			t.Fatal(at, p, e)
		}
	}
	q.work = nil
	if p, e := s.CompactNewestWindow(context.Background(), testStoreA, 2); e != nil || !reflect.DeepEqual(p, want) {
		t.Fatal(p, e)
	}
	for _, state := range []SnapshotState{StateRebuilding, StateUnavailable, StateNotLoaded, StateUsable} {
		s.workers[testStoreA].snapshot = Snapshot{State: state}
		s.workers[testStoreA].query = nil
		p, e := s.CompactNewestWindow(context.Background(), testStoreA, 2)
		if state == StateUsable {
			state = StateUnavailable
		}
		if e != nil || p.State != state || p.ConversationID != "" || p.Epoch != "" || p.UpperOrder != 0 || len(p.Headers)+len(p.Items) != 0 {
			t.Fatal(p, e)
		}
	}
}

func TestCompactCapture(t *testing.T) {
	testRecordCompactTrace(t)
	for _, outcome := range []string{"success", "failure", "cancel", "current-failure", "current-cancel"} {
		t.Run(outcome, func(t *testing.T) {
			s, h := testThreadStore(t)
			trace := testTraceCompactStore(t, s)
			testStoreAppend(t, h, testStoreA, testMain(0, "tool_use", `,"tool_use_id":"root","name":"Agent"`))
			testStoreAppend(t, h, testStoreA, testMessage(0))
			if e := s.Load(context.Background(), testStoreA); e != nil {
				t.Fatal(e)
			}
			snap := trace.wait(t, s, testStoreA, 2)
			testStoreAppend(t, h, testStoreB, testMessage(0))
			if e := s.Load(context.Background(), testStoreB); e != nil {
				t.Fatal(e)
			}
			unrelated := trace.wait(t, s, testStoreB, 1)
			want := testCompactOracle(t, s, snap.Items, 0, 2, true)
			lookup := s.lookup
			entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			defer releaseOnce.Do(func() { close(release) })
			var first atomic.Bool
			type requestKey struct{}
			s.lookup = func(ctx context.Context, id conversations.ConversationID, ids []uint64) ([]history.Entry, error) {
				if ctx.Value(requestKey{}) != nil && first.CompareAndSwap(false, true) {
					defer trace.begin("held-query")()
					trace.mark("query held")
					close(entered)
					<-release
					trace.mark("query released")
					if strings.HasSuffix(outcome, "failure") {
						return nil, errors.New("private source path")
					}
				}
				return lookup(ctx, id, ids)
			}
			ctx, cancel := context.WithCancel(context.WithValue(context.Background(), requestKey{}, true))
			defer cancel()
			var p CompactQueryResult
			var err error
			go func() { defer close(done); p, err = s.CompactNewestWindow(ctx, testStoreA, 2) }()
			defer func() {
				releaseOnce.Do(func() { close(release) })
				cancel()
				<-done
			}()
			testStoreSignal(t, entered)
			if got := s.Snapshot(testStoreA); !reflect.DeepEqual(got, snap) {
				t.Fatal("concurrent complete read", got)
			}
			testStoreAppend(t, h, testStoreA, testMessage(0))
			trace.wait(t, s, testStoreA, 3)
			if !strings.HasPrefix(outcome, "current-") {
				finished := trace.begin("unload")
				e := s.Unload(testStoreA)
				finished()
				if e != nil {
					t.Fatal(e)
				}
				finished = trace.begin("reload-admission")
				e = s.Load(context.Background(), testStoreA)
				finished()
				if e != nil {
					t.Fatal(e)
				}
			}
			replacement := trace.wait(t, s, testStoreA, 3)
			if strings.HasSuffix(outcome, "cancel") {
				cancel()
			}
			releaseOnce.Do(func() { close(release) })
			testStoreSignal(t, done)
			switch outcome {
			case "success":
				if err != nil || !reflect.DeepEqual(p, want) {
					t.Fatal(p, want, err)
				}
				p.Items[0].Content[0] = 'x'
				*p.Headers[0].PublicChildCount = 999
			case "failure", "current-failure":
				if err != nil || !reflect.DeepEqual(p, CompactQueryResult{State: StateUnavailable, Err: ErrUnavailable}) {
					t.Fatal(p, err)
				}
			case "cancel", "current-cancel":
				if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(p, CompactQueryResult{}) {
					t.Fatal(p, err)
				}
			}
			if outcome == "current-failure" {
				replacement = Snapshot{State: StateUnavailable, Err: ErrUnavailable}
			}
			if got := s.Snapshot(testStoreA); !reflect.DeepEqual(got, replacement) {
				t.Fatal("replacement disturbed", got)
			}
			if got := s.Snapshot(testStoreB); !reflect.DeepEqual(got, unrelated) {
				t.Fatal("unrelated reader disturbed", got)
			}
		})
	}
}

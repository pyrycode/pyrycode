package thread

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
)

func testQueryItems(t *testing.T, got, want []Item) {
	t.Helper()
	sort.Slice(got, func(i, j int) bool { return got[i].ID < got[j].ID })
	want = append([]Item(nil), want...)
	sort.Slice(want, func(i, j int) bool { return want[i].ID < want[j].ID })
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("items: got %#v, want %#v", got, want)
	}
}

func TestQuerySelection(t *testing.T) {
	items := []Item{
		{ID: 1, Order: 0, Content: []byte(`{"parent":true}`)},
		{ID: 2, Order: 2, Parent: 1},
		{ID: 3, Order: 10, Parent: 2, Active: true, Shown: false, Status: "dropped"},
		{ID: 4, Order: 10, Parent: 2},
		{ID: 5, Order: 0, Active: true},
		{ID: 6, Order: 20},
	}
	snap := Snapshot{State: StateUsable, Items: items, Version: 21, Epoch: "epoch"}
	s := NewStore(nil)
	s.workers[testStoreA] = &conversationWorker{snapshot: snap, query: newQueryIndex(items, nil)}
	for _, tc := range []struct {
		name   string
		upper  uint64
		limit  int
		newest bool
		ids    []int
		lower  uint64
		older  bool
	}{
		{"ties and closure", 11, 1, false, []int{0, 1, 2, 3}, 10, true},
		{"exclusive", 10, 1, false, []int{0, 1}, 2, false},
		{"zero", 0, 1, false, nil, 0, false},
		{"oldest", 2, 1, false, nil, 0, false},
		{"newest active extras", 22, 1, true, []int{0, 1, 2, 4, 5}, 20, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got QueryResult
			var err error
			if tc.newest {
				got, err = s.NewestWindow(context.Background(), testStoreA, tc.limit)
			} else {
				got, err = s.HistoryPage(context.Background(), testStoreA, tc.upper, tc.limit)
			}
			if err != nil || got.State != StateUsable || got.Epoch != "epoch" || got.Version != 21 || got.UpperOrder != tc.upper || got.LowerOrder != tc.lower || got.Continuation != tc.lower || got.OlderExists != tc.older {
				t.Fatal(got, err)
			}
			var want []Item
			for _, i := range tc.ids {
				want = append(want, items[i])
			}
			testQueryItems(t, got.Items, want)
		})
	}
	s.workers[testStoreA].snapshot.Items = items[:1]
	s.workers[testStoreA].query = newQueryIndex(items[:1], nil)
	s.workers[testStoreA].snapshot.Items[0].Active = true
	s.workers[testStoreA].query = newQueryIndex(items[:1], nil)
	got, err := s.NewestWindow(context.Background(), testStoreA, 1)
	if err != nil || got.LowerOrder != 0 || got.Continuation != 0 || got.OlderExists || len(got.Items) != 1 {
		t.Fatal(got, err)
	}
	got.Items[0].Content[0] = 'X'
	again, _ := s.NewestWindow(context.Background(), testStoreA, 1)
	if again.Items[0].Content[0] != '{' {
		t.Fatal("content aliases publication")
	}
}

func TestQueryLimits(t *testing.T) {
	s, _ := testThreadStore(t)
	items := make([]Item, MaxQueryItems+5)
	for i := range items {
		items[i] = Item{ID: uint64(i + 1), Order: uint64(i + 1)}
	}
	s.workers[testStoreA] = &conversationWorker{snapshot: Snapshot{State: StateUsable, Version: uint64(len(items))}, query: newQueryIndex(items, nil), cancel: func() {}, done: make(chan struct{}), changed: make(chan struct{})}
	close(s.workers[testStoreA].done)
	for _, limit := range []int{-1, 0, 1, MaxQueryItems, MaxQueryItems + 1, int(^uint(0) >> 1)} {
		for _, newest := range []bool{false, true} {
			var got QueryResult
			var err error
			if newest {
				got, err = s.NewestWindow(context.Background(), testStoreA, limit)
			} else {
				got, err = s.HistoryPage(context.Background(), testStoreA, uint64(len(items)+1), limit)
			}
			if limit <= 0 {
				if !errors.Is(err, ErrInvalidLimit) || got.Items != nil {
					t.Fatal(got, err)
				}
				continue
			}
			if err != nil || len(got.Items) != min(limit, MaxQueryItems) {
				t.Fatal(len(got.Items), err)
			}
		}
	}
	if _, err := s.HistoryPage(context.Background(), "../../private", 1, 1); !errors.Is(err, history.ErrInvalidID) {
		t.Fatal(err)
	}
	if _, err := s.NewestWindow(context.Background(), "../../private", 1); !errors.Is(err, history.ErrInvalidID) {
		t.Fatal(err)
	}
}

func TestStoreQueriesUpdates(t *testing.T) {
	s, h := testThreadStore(t)
	if err := s.Load(context.Background(), testStoreA); err != nil {
		t.Fatal(err)
	}
	testStoreWait(t, s, testStoreA, StateUsable, 0)
	entries := []history.Entry{
		testAcceptance(1, "phone"),
		testMain(2, "tool_use", `,"tool_use_id":"old","name":"Read"`),
		testMessage(3),
		testSendOutcome(4, "send_delivered", `,"accepted_entry_id":1,"delivery_entry_id":3`, "delivered"),
		testMain(5, "tool_result", `,"tool_use_id":"old","is_error":false`),
		testMain(6, "tool_use", `,"tool_use_id":"parent","name":"Agent"`),
		testChild(7, "tool_use", "missing", `,"tool_use_id":"nested","name":"Task"`),
		testChild(8, "assistant_delta", "nested", `,"text":"deep"`),
		testChild(9, "tool_result", "parent", `,"tool_use_id":"nested","is_error":false`),
		testChild(10, "tool_use", "nested", `,"tool_use_id":"read","name":"Read"`),
		testChild(11, "turn_end", "nested", `,"stop_reason":"end_turn"`),
	}
	var committed []history.Entry
	for i, e := range entries {
		committed = append(committed, testStoreAppend(t, h, testStoreA, e)...)
		snap := testStoreWait(t, s, testStoreA, StateUsable, uint64(i+1))
		page, err := s.HistoryPage(context.Background(), testStoreA, snap.Version+1, MaxQueryItems)
		if err != nil || page.Epoch != snap.Epoch || page.Version != snap.Version {
			t.Fatal(page, err)
		}
		var ordered []Item
		for _, item := range snap.Items {
			if item.Order > 0 {
				ordered = append(ordered, item)
			}
		}
		testQueryItems(t, page.Items, ordered)
		newest, err := s.NewestWindow(context.Background(), testStoreA, 1)
		if err != nil {
			t.Fatal(err)
		}
		want, lower, older := testQueryReference(snap.Items, snap.Version+1, 1, true)
		testQueryItems(t, newest.Items, want)
		if newest.LowerOrder != lower || newest.OlderExists != older {
			t.Fatal(newest)
		}
		if i == 3 && (len(page.Items) != 2 || page.Items[0].ID != 1 || page.Items[0].Order != 3) {
			t.Fatal("delivery placement/suppression", page)
		}
		if i == 7 && len(snap.Items) != 3 {
			t.Fatal("unresolved child leaked")
		}
		if i == 8 && len(snap.Items) != 5 {
			t.Fatal("child did not resolve")
		}
	}
	// Select only the deep child: both ancestors lie outside the ordered window.
	got, err := s.HistoryPage(context.Background(), testStoreA, 9, 1)
	if err != nil || got.LowerOrder != 8 || len(got.Items) != 3 {
		t.Fatal(got, err)
	}
	snap := s.Snapshot(testStoreA)
	testQueryItems(t, got.Items, []Item{snap.Items[2], snap.Items[3], snap.Items[4]})
}

func testQueryReference(items []Item, upper uint64, limit int, active bool) ([]Item, uint64, bool) {
	var ordered []Item
	byID := make(map[uint64]Item)
	for _, item := range items {
		byID[item.ID] = item
		if item.Order > 0 && item.Order < upper {
			ordered = append(ordered, item)
		}
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Order < ordered[j].Order })
	var lower uint64
	older := false
	if len(ordered) > 0 {
		start := max(0, len(ordered)-min(limit, MaxQueryItems))
		lower = ordered[start].Order
		older = start > 0
	}
	selected := make(map[uint64]Item)
	for _, item := range items {
		if (lower > 0 && item.Order >= lower && item.Order < upper) || (active && item.Active) {
			selected[item.ID] = item
		}
	}
	for _, item := range items {
		if _, ok := selected[item.ID]; !ok {
			continue
		}
		for p := item.Parent; p != 0; {
			parent, ok := byID[p]
			if !ok {
				break
			}
			selected[p] = parent
			p = parent.Parent
		}
	}
	var result []Item
	for _, item := range selected {
		result = append(result, item)
	}
	return result, lower, older
}

func TestStoreQueriesBounded(t *testing.T) {
	root := t.TempDir()
	h := history.New(root)
	for i := 1; i <= 36000; i++ {
		e := testEntry(0, "unknown", `{}`)
		if i%6 == 0 {
			e = testMessage(0)
		}
		switch i {
		case 1:
			e = testMain(0, "tool_use", `,"tool_use_id":"parent","name":"Agent"`)
		case 2:
			e = testChild(0, "tool_use", "parent", `,"tool_use_id":"nested","name":"Task"`)
		case 3:
			e = testChild(0, "assistant_delta", "nested", `,"text":"old active child"`)
		case 4:
			e = testAcceptance(0, "phone")
		case 6:
			e.Shown = new(bool)
		}
		testStoreAppend(t, h, testStoreA, e)
	}
	var requests, preparation atomic.Uint64
	var epoch string
	for _, phase := range []string{"initial", "clean reopen", "unload reload"} {
		t.Run(phase, func(t *testing.T) {
			h = history.New(root)
			s := NewStore(h)
			s.queryWork = func(preparing bool) {
				if preparing {
					preparation.Add(1)
				} else {
					requests.Add(1)
				}
			}
			base := s.forward
			reached := make(chan struct{}, 1)
			s.forward = func(id conversations.ConversationID) (storeReader, error) {
				r, err := base(id)
				return testStoreReader{storeReader: r, tail: func(ctx context.Context, _ func([]history.Entry) error) error {
					reached <- struct{}{}
					<-ctx.Done()
					return ctx.Err()
				}}, err
			}
			defer s.Shutdown()
			if err := s.Load(context.Background(), testStoreA); err != nil {
				t.Fatal(err)
			}
			select {
			case <-reached:
			case <-time.After(120 * time.Second):
				t.Fatal("replay stalled")
			}
			if phase == "unload reload" {
				if err := s.Unload(testStoreA); err != nil {
					t.Fatal(err)
				}
				if err := s.Load(context.Background(), testStoreA); err != nil {
					t.Fatal(err)
				}
				select {
				case <-reached:
				case <-time.After(120 * time.Second):
					t.Fatal("reload stalled")
				}
			}
			snap := s.Snapshot(testStoreA)
			if epoch != "" && snap.Epoch != epoch {
				t.Fatal("clean recovery changed epoch")
			}
			epoch = snap.Epoch
			// The only reader is paused at Tail and no writer runs during measurement.
			reads := func() [2]int64 {
				v := reflect.ValueOf(h).Elem()
				return [2]int64{v.FieldByName("readBytes").Int(), v.FieldByName("segmentsOpened").Int()}
			}
			readBefore := reads()
			prepBefore := preparation.Load()
			if readBefore[0] == 0 || readBefore[1] == 0 || prepBefore < 6000 {
				t.Fatal("preparation was not measured")
			}
			for _, upper := range []uint64{1001, 18001, 36001} {
				for _, newest := range []bool{false, true} {
					before := requests.Load()
					var got QueryResult
					var err error
					if newest {
						got, err = s.NewestWindow(context.Background(), testStoreA, 20)
					} else {
						got, err = s.HistoryPage(context.Background(), testStoreA, upper, 20)
					}
					if err != nil {
						t.Fatal(err)
					}
					bound := upper
					if newest {
						bound = 36001
					}
					want, lower, older := testQueryReference(snap.Items, bound, 20, newest)
					testQueryItems(t, got.Items, want)
					if got.LowerOrder != lower || got.Continuation != lower || got.OlderExists != older || got.UpperOrder != bound || got.Version != 36000 || got.Epoch != snap.Epoch {
						t.Fatal(got)
					}
					if visits := requests.Load() - before; visits > 80 {
						t.Fatalf("linear request: %d visits", visits)
					}
					allocs := testing.AllocsPerRun(2, func() {
						if newest {
							_, err = s.NewestWindow(context.Background(), testStoreA, 20)
						} else {
							_, err = s.HistoryPage(context.Background(), testStoreA, upper, 20)
						}
					})
					if err != nil || allocs > 80 {
						t.Fatalf("full clone: %v allocations, %v", allocs, err)
					}
				}
			}
			if reads() != readBefore || preparation.Load() != prepBefore {
				t.Fatal("request read history or rebuilt index")
			}
			t.Logf("preparation visits=%d read bytes/segments=%v; bounded request visits=%d", prepBefore, readBefore, requests.Load())
			if err := s.Shutdown(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestStoreQueriesLifecycle(t *testing.T) {
	s, h := testThreadStore(t)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, newest := range []bool{false, true} {
		var err error
		if newest {
			_, err = s.NewestWindow(cancelled, testStoreA, 1)
		} else {
			_, err = s.HistoryPage(cancelled, testStoreA, 1, 1)
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
	check := func(state SnapshotState) {
		t.Helper()
		for _, newest := range []bool{false, true} {
			var got QueryResult
			var err error
			if newest {
				got, err = s.NewestWindow(context.Background(), testStoreA, 1)
			} else {
				got, err = s.HistoryPage(context.Background(), testStoreA, 9, 1)
			}
			want := QueryResult{State: state}
			if state == StateUnavailable {
				want.Err = ErrUnavailable
			}
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatal(got, err)
			}
		}
	}
	check(StateNotLoaded)
	started, release := make(chan struct{}, 2), make(chan struct{})
	base := s.forward
	s.forward = func(id conversations.ConversationID) (storeReader, error) {
		r, err := base(id)
		return testStoreReader{storeReader: r, walk: func(ctx context.Context, h uint64, feed func([]history.Entry) error) error {
			select {
			case started <- struct{}{}:
			default:
			}
			select {
			case <-release:
				return r.Walk(ctx, h, feed)
			case <-ctx.Done():
				return ctx.Err()
			}
		}}, err
	}
	workerCtx, stop := context.WithCancel(context.Background())
	defer stop()
	testStoreAppend(t, h, testStoreA, testMessage(0))
	if err := s.Load(workerCtx, testStoreA); err != nil {
		t.Fatal(err)
	}
	testStoreSignal(t, started)
	check(StateRebuilding)
	close(release)
	testStoreWait(t, s, testStoreA, StateUsable, 1)
	stop()
	testStoreWait(t, s, testStoreA, StateUnavailable, 0)
	check(StateUnavailable)
	if err := s.Retry(context.Background(), testStoreA); err != nil {
		t.Fatal(err)
	}
	testStoreWait(t, s, testStoreA, StateUsable, 1)
	// Cancel during actual index access, after publication acquisition.
	ctx, stopQuery := context.WithCancel(context.Background())
	defer stopQuery()
	s.mu.Lock()
	s.workers[testStoreA].query.work = func(bool) { stopQuery() }
	s.mu.Unlock()
	if got, err := s.NewestWindow(ctx, testStoreA, 1); !errors.Is(err, context.Canceled) || got.Items != nil {
		t.Fatal(got, err)
	}
	s.mu.Lock()
	s.workers[testStoreA].query.work = nil
	s.mu.Unlock()
	testStoreAppend(t, h, testStoreA, testMessage(0))
	testStoreWait(t, s, testStoreA, StateUsable, 2)
	testStoreAppend(t, h, testStoreB, testMessage(0))
	if err := s.Load(context.Background(), testStoreB); err != nil {
		t.Fatal(err)
	}
	testStoreWait(t, s, testStoreB, StateUsable, 1)
	if err := s.Unload(testStoreA); err != nil {
		t.Fatal(err)
	}
	check(StateNotLoaded)
	if err := s.Shutdown(); err != nil {
		t.Fatal(err)
	}
	check(StateNotLoaded)
}

func TestStoreQueriesRetirementIsolation(t *testing.T) {
	s, h := testThreadStore(t)
	testStoreAppend(t, h, testStoreA, testMessage(0))
	if err := s.Load(context.Background(), testStoreA); err != nil {
		t.Fatal(err)
	}
	snap := testStoreWait(t, s, testStoreA, StateUsable, 1)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var gate, cleanup sync.Once
	defer cleanup.Do(func() { close(release) })
	s.mu.Lock()
	s.workers[testStoreA].query.work = func(bool) {
		gate.Do(func() { close(entered); <-release })
	}
	s.mu.Unlock()
	var got QueryResult
	var err error
	go func() {
		defer close(done)
		got, err = s.NewestWindow(context.Background(), testStoreA, 1)
	}()
	testStoreSignal(t, entered)
	// A paused consumer must not hold publication or history-writer locks.
	testStoreAppend(t, h, testStoreA, testMessage(0))
	testStoreWait(t, s, testStoreA, StateUsable, 2)
	testStoreAppend(t, h, testStoreB, testMessage(0))
	if err := s.Load(context.Background(), testStoreB); err != nil {
		t.Fatal(err)
	}
	testStoreWait(t, s, testStoreB, StateUsable, 1)
	// Hold retirement at the final checkpoint, before Unload joins its worker.
	retiring, finish := make(chan struct{}), make(chan struct{})
	var finishOnce sync.Once
	replace := s.replace
	s.replace = func(old, new string) error { close(retiring); <-finish; return replace(old, new) }
	defer finishOnce.Do(func() { close(finish) })
	unloaded := make(chan struct{})
	var unloadErr error
	go func() { defer close(unloaded); unloadErr = s.Unload(testStoreA) }()
	testStoreSignal(t, retiring)
	if result, e := s.HistoryPage(context.Background(), testStoreA, 2, 1); e != nil || result.State != StateNotLoaded || result.Items != nil {
		t.Fatal(result, e)
	}
	cleanup.Do(func() { close(release) })
	testStoreSignal(t, done)
	if err != nil || got.Version != snap.Version || got.Epoch != snap.Epoch || got.UpperOrder != 2 {
		t.Fatal(got, err)
	}
	testQueryItems(t, got.Items, snap.Items)
	finishOnce.Do(func() { close(finish) })
	testStoreSignal(t, unloaded)
	s.replace = replace
	if unloadErr != nil {
		t.Fatal(unloadErr)
	}
}

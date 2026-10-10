package thread

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
)

func TestCatchUpPublications(t *testing.T) {
	s, h := testThreadStore(t)
	testStoreAppend(t, h, testStoreA, testAcceptance(0, "phone"))
	if err := s.Load(context.Background(), testStoreA); err != nil {
		t.Fatal(err)
	}
	testStoreWait(t, s, testStoreA, StateUsable, 1)
	saved, err := s.CatchUp(context.Background(), testStoreA, "", 0, 1)
	if err != nil || saved.Version != 1 {
		t.Fatal(saved, err)
	}
	// Each append is published separately, exceeding live-range retention.
	for i := 2; i <= 80; i++ {
		e := testEntry(0, "unknown", `{}`)
		if i%2 == 0 {
			e = testMessage(0)
		}
		testStoreAppend(t, h, testStoreA, e)
		testStoreWait(t, s, testStoreA, StateUsable, uint64(i))
	}
	live, err := s.Changes(context.Background(), testStoreA, saved.Epoch, 1)
	if err != nil || !live.BaselineRequired {
		t.Fatal(live, err)
	}
	got, err := s.CatchUp(context.Background(), testStoreA, saved.Epoch, 1, 1)
	if err != nil || got.ResetRequired || got.FromVersion != 1 || got.Version != 80 || len(got.Items) != 41 {
		t.Fatal(got, err)
	}
	testQueryItems(t, got.Items, s.Snapshot(testStoreA).Items)
	// H reads do not wait, but still include old active unordered work.
	got, err = s.CatchUp(context.Background(), testStoreA, saved.Epoch, 80, 0)
	if err != nil || got.ResetRequired || len(got.Items) != 1 || got.Items[0].ID != 1 {
		t.Fatal(got, err)
	}
	// Non-boundary saved versions are legal, including an entry producing no item.
	got, err = s.CatchUp(context.Background(), testStoreA, saved.Epoch, 77, -1)
	if err != nil || got.ResetRequired || len(got.Items) != 3 {
		t.Fatal(got, err)
	}
}

func TestCatchUpEvidence(t *testing.T) {
	for _, chunked := range []bool{false, true} {
		t.Run(map[bool]string{false: "whole", true: "single entries"}[chunked], func(t *testing.T) {
			s, h := testThreadStore(t)
			entries := []history.Entry{
				testMain(1, "tool_use", `,"tool_use_id":"parent","name":"Agent"`),
				testChild(2, "tool_use", "missing", `,"tool_use_id":"nested","name":"Task"`),
				testChild(3, "assistant_delta", "nested", `,"text":"late child"`),
				testChild(4, "turn_end", "nested", `,"stop_reason":"end_turn"`),
				testEntry(5, "unknown", `{}`),
				testChild(6, "tool_result", "parent", `,"tool_use_id":"nested","is_error":false`),
				testAcceptance(7, "phone"),
				testSendOutcome(8, "send_dropped", `,"accepted_entry_id":7`, "removed"),
			}
			if chunked {
				if err := s.Load(context.Background(), testStoreA); err != nil {
					t.Fatal(err)
				}
				testStoreWait(t, s, testStoreA, StateUsable, 0)
			}
			for i, e := range entries {
				testStoreAppend(t, h, testStoreA, e)
				if chunked {
					testStoreWait(t, s, testStoreA, StateUsable, uint64(i+1))
				}
			}
			if !chunked {
				if err := s.Load(context.Background(), testStoreA); err != nil {
					t.Fatal(err)
				}
			}
			snap := testStoreWait(t, s, testStoreA, StateUsable, 8)
			got, err := s.CatchUp(context.Background(), testStoreA, snap.Epoch, 5, 1)
			if err != nil || got.ResetRequired {
				t.Fatal(got, err)
			}
			testQueryItems(t, got.Items, snap.Items)
			var late bool
			for _, item := range got.Items {
				if item.ID == 3 {
					late = true
					if item.Rev >= 5 {
						t.Fatalf("fixture must expose an old-revision first appearance: %#v", item)
					}
				}
			}
			if !late {
				t.Fatal("late child omitted")
			}
			got.Items[0].Content[0] = 'X'
			again, _ := s.CatchUp(context.Background(), testStoreA, snap.Epoch, 5, 1)
			for _, item := range again.Items {
				if len(item.Content) > 0 && item.Content[0] == 'X' {
					t.Fatal("aliased content")
				}
			}
			// Suppress a public delivery row: hidden dropped rows above remain public.
			testStoreAppend(t, h, testStoreA, testAcceptance(9, "phone"), testMessage(10))
			snap = testStoreWait(t, s, testStoreA, StateUsable, 10)
			testStoreAppend(t, h, testStoreA, testSendOutcome(11, "send_delivered", `,"accepted_entry_id":9,"delivery_entry_id":10`, "delivered"))
			testStoreWait(t, s, testStoreA, StateUsable, 11)
			got, err = s.CatchUp(context.Background(), testStoreA, snap.Epoch, 10, 1)
			if err != nil || !got.ResetRequired || got.Items != nil || got.FromVersion != 0 {
				t.Fatal(got, err)
			}
			got, err = s.CatchUp(context.Background(), testStoreA, snap.Epoch, 11, 1)
			if err != nil || got.ResetRequired {
				t.Fatal(got, err)
			}
			if err := s.Shutdown(); err != nil {
				t.Fatal(err)
			}
			reopened := NewStore(h)
			defer reopened.Shutdown()
			if err := reopened.Load(context.Background(), testStoreA); err != nil {
				t.Fatal(err)
			}
			testStoreWait(t, reopened, testStoreA, StateUsable, 11)
			got, err = reopened.CatchUp(context.Background(), testStoreA, snap.Epoch, 10, 1)
			if err != nil || !got.ResetRequired || got.Epoch != snap.Epoch || got.Items != nil {
				t.Fatal("suppression evidence lost on clean restart", got, err)
			}
		})
	}
}

func TestCatchUpContracts(t *testing.T) {
	s, h := testThreadStore(t)
	for i := 0; i < MaxCatchUpGap+2; i++ {
		testStoreAppend(t, h, testStoreA, testMessage(0))
	}
	if err := s.Load(context.Background(), testStoreA); err != nil {
		t.Fatal(err)
	}
	snap := testStoreWait(t, s, testStoreA, StateUsable, MaxCatchUpGap+2)
	for _, tc := range []struct {
		name, epoch string
		after       uint64
		reset       bool
	}{
		{"exact", snap.Epoch, 2, false}, {"above", snap.Epoch, 1, true},
		{"future", snap.Epoch, snap.Version + 1, true}, {"empty", "", 2, true},
		{"mismatch", "wrong", 2, true}, {"zero mismatch", "wrong", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.CatchUp(context.Background(), testStoreA, tc.epoch, tc.after, 1)
			if err != nil || got.ResetRequired != tc.reset || got.Epoch != snap.Epoch || got.Version != snap.Version {
				t.Fatal(got, err)
			}
			if tc.reset {
				if got.Items != nil || got.FromVersion != 0 || got.UpperOrder != 0 {
					t.Fatal("partial reset", got)
				}
			} else if len(got.Items) != MaxCatchUpGap {
				t.Fatal(len(got.Items))
			}
		})
	}
	for _, epoch := range []string{"", snap.Epoch} {
		for _, limit := range []int{-1, 0, 1, MaxQueryItems + 20} {
			got, err := s.CatchUp(context.Background(), testStoreA, epoch, 0, limit)
			want, werr := s.NewestWindow(context.Background(), testStoreA, limit)
			if !errors.Is(err, werr) || !reflect.DeepEqual(got, want) {
				t.Fatal(got, want, err, werr)
			}
		}
	}
	if _, err := s.CatchUp(context.Background(), "../../private", snap.Epoch, 1, 1); !errors.Is(err, history.ErrInvalidID) {
		t.Fatal(err)
	}
	// An index without reconstructed evidence must never certify a range.
	s.mu.Lock()
	s.workers[testStoreA].query = newQueryIndex(snap.Items, nil)
	s.mu.Unlock()
	got, err := s.CatchUp(context.Background(), testStoreA, snap.Epoch, snap.Version, 1)
	if err != nil || !got.ResetRequired || got.Items != nil {
		t.Fatal(got, err)
	}
}

func testCatchUpReference(items []Item, after uint64) []Item {
	byID := make(map[uint64]Item)
	selected := make(map[uint64]bool)
	var result []Item
	add := func(item Item) {
		if !selected[item.ID] {
			selected[item.ID] = true
			result = append(result, item)
		}
	}
	for _, item := range items {
		byID[item.ID] = item
		if item.Rev > after || item.Active || (item.ID == 35981 && after < 35991) {
			add(item)
		}
	}
	for i := 0; i < len(result); i++ {
		if parent, ok := byID[result[i].Parent]; ok {
			add(parent)
		}
	}
	return result
}

func TestCatchUpBoundedRecovery(t *testing.T) {
	root := t.TempDir()
	h := history.New(root)
	appendRange := func(start, end int) {
		for i := start; i <= end; i++ {
			e := testEntry(0, "unknown", `{}`)
			if i%6 == 0 {
				e = testMessage(0)
			}
			switch i {
			case 1, 2:
				e = testAcceptance(0, "phone")
			case 35001:
				e = testMain(0, "tool_use", `,"tool_use_id":"parent","name":"Agent"`)
			case 35002:
				e = testChild(0, "tool_use", "parent", `,"tool_use_id":"nested","name":"Task"`)
			case 35003:
				e = testChild(0, "assistant_delta", "nested", `,"text":"old active child"`)
			case 35004:
				e = testChild(0, "tool_use", "nested", `,"tool_use_id":"read","name":"Read"`)
			case 35005:
				e = testChild(0, "tool_use", "nested", `,"tool_use_id":"write","name":"Write"`)
			case 35980:
				e = testChild(0, "tool_use", "missing", `,"tool_use_id":"late","name":"Task"`)
			case 35981:
				e = testChild(0, "assistant_delta", "late", `,"text":"late complete child"`)
			case 35982:
				e = testChild(0, "turn_end", "late", `,"stop_reason":"end_turn"`)
			case 35991:
				e = testChild(0, "tool_result", "parent", `,"tool_use_id":"late","is_error":false`)
			case 35992, 35993:
				e = testMain(0, "assistant_delta", `,"text":"repeated"`)
			case 35995:
				e = testSendOutcome(0, "send_dropped", `,"accepted_entry_id":1`, "removed")
			}
			testStoreAppend(t, h, testStoreA, e)
		}
	}
	appendRange(1, 35900)
	first := NewStore(h)
	if err := first.Load(context.Background(), testStoreA); err != nil {
		t.Fatal(err)
	}
	// Use the reader's completed tail transition instead of cloning snapshots while
	// cold replay runs; first catch-up returns the saved durable version.
	deadline := time.Now().Add(120 * time.Second)
	var saved QueryResult
	for time.Now().Before(deadline) {
		saved, _ = first.CatchUp(context.Background(), testStoreA, "", 0, 1)
		if saved.State == StateUsable {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if saved.Version != 35900 {
		t.Fatal(saved)
	}
	if err := first.Shutdown(); err != nil {
		t.Fatal(err)
	}
	appendRange(35901, 36000)
	for _, phase := range []string{"cold recovery", "clean restart", "unload reload"} {
		t.Run(phase, func(t *testing.T) {
			h = history.New(root)
			s := NewStore(h)
			defer s.Shutdown()
			var visits, preparation atomic.Uint64
			s.queryWork = func(preparing bool) {
				if preparing {
					preparation.Add(1)
				} else {
					visits.Add(1)
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
			load := func() {
				t.Helper()
				started := time.Now()
				if err := s.Load(context.Background(), testStoreA); err != nil {
					t.Fatal(err)
				}
				select {
				case <-reached:
				case <-time.After(120 * time.Second):
					t.Fatal("recovery stalled")
				}
				t.Logf("recovery/publication: %v, preparation visits: %d", time.Since(started), preparation.Load())
			}
			load()
			if phase == "unload reload" {
				if err := s.Unload(testStoreA); err != nil {
					t.Fatal(err)
				}
				load()
			}
			snap := s.Snapshot(testStoreA)
			if snap.Epoch != saved.Epoch || snap.Version != 36000 {
				t.Fatal("saved epoch/version not recovered", snap.Epoch, snap.Version)
			}
			if len(s.workers[testStoreA].ranges) != 0 {
				t.Fatal("recovery retained live ranges")
			}
			reads := func() [2]int64 {
				v := reflect.ValueOf(h).Elem()
				return [2]int64{v.FieldByName("readBytes").Int(), v.FieldByName("segmentsOpened").Int()}
			}
			readBefore, prepBefore := reads(), preparation.Load()
			if readBefore[0] == 0 || readBefore[1] == 0 || prepBefore < 6000 {
				t.Fatal("cold work not counted")
			}
			for _, after := range []uint64{saved.Version, 35990, 35999, 36000, 36000 - MaxCatchUpGap} {
				before := visits.Load()
				got, err := s.CatchUp(context.Background(), testStoreA, saved.Epoch, after, 1)
				if err != nil || got.ResetRequired || got.FromVersion != after || got.Version != 36000 || got.Epoch != saved.Epoch {
					t.Fatal(got, err)
				}
				want := testCatchUpReference(snap.Items, after)
				testQueryItems(t, got.Items, want)
				if work := visits.Load() - before; work > uint64(3*len(want)+100) {
					t.Fatalf("conversation scan: %d visits for %d items", work, len(want))
				}
				allocs := testing.AllocsPerRun(2, func() { _, err = s.CatchUp(context.Background(), testStoreA, saved.Epoch, after, 1) })
				if err != nil || allocs > float64(3*len(want)+100) {
					t.Fatalf("full snapshot clone: %v allocations, %v", allocs, err)
				}
			}
			if reads() != readBefore || preparation.Load() != prepBefore {
				t.Fatal("query read history or prepared an index")
			}
			if err := s.Shutdown(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCatchUpIsolation(t *testing.T) {
	for _, cancelQuery := range []bool{false, true} {
		t.Run(map[bool]string{false: "captured retirement", true: "cancellation"}[cancelQuery], func(t *testing.T) {
			s, h := testThreadStore(t)
			reached, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			var releaseOnce sync.Once
			var armed atomic.Bool
			s.queryWork = func(preparing bool) {
				if !preparing && armed.Load() {
					once.Do(func() { close(reached); <-release })
				}
			}
			defer releaseOnce.Do(func() { close(release) })
			testStoreAppend(t, h, testStoreA, testAcceptance(0, "phone"), testMessage(0))
			if err := s.Load(context.Background(), testStoreA); err != nil {
				t.Fatal(err)
			}
			snap := testStoreWait(t, s, testStoreA, StateUsable, 2)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			armed.Store(true)
			done := make(chan struct{})
			var got QueryResult
			var queryErr error
			go func() { defer close(done); got, queryErr = s.CatchUp(ctx, testStoreA, snap.Epoch, 1, 1) }()
			testStoreSignal(t, reached)
			if cancelQuery {
				cancel()
			}
			// Captured selection owns no global lock or worker. Both writers/publications
			// and another conversation continue while its real binary seek is paused.
			testStoreAppend(t, h, testStoreA, testMessage(0))
			testStoreWait(t, s, testStoreA, StateUsable, 3)
			testStoreAppend(t, h, testStoreB, testMessage(0))
			if err := s.Load(context.Background(), testStoreB); err != nil {
				t.Fatal(err)
			}
			testStoreWait(t, s, testStoreB, StateUsable, 1)
			if err := s.Unload(testStoreA); err != nil {
				t.Fatal(err)
			}
			unloaded, err := s.CatchUp(context.Background(), testStoreA, snap.Epoch, 1, 1)
			if err != nil || unloaded.State != StateNotLoaded {
				t.Fatal(unloaded, err)
			}
			if err := s.Load(context.Background(), testStoreA); err != nil {
				t.Fatal(err)
			}
			reloaded := testStoreWait(t, s, testStoreA, StateUsable, 3)
			if reloaded.Epoch != snap.Epoch {
				t.Fatal("reload changed epoch")
			}
			if err := s.Shutdown(); err != nil {
				t.Fatal(err)
			}
			releaseOnce.Do(func() { close(release) })
			testStoreSignal(t, done)
			if cancelQuery {
				if !errors.Is(queryErr, context.Canceled) || !reflect.DeepEqual(got, QueryResult{}) {
					t.Fatal(got, queryErr)
				}
			} else {
				if queryErr != nil || got.ResetRequired || got.Version != snap.Version || got.Epoch != snap.Epoch {
					t.Fatal(got, queryErr)
				}
				testQueryItems(t, got.Items, snap.Items)
			}
		})
	}
}

func TestCatchUpReadiness(t *testing.T) {
	s, h := testThreadStore(t)
	reached, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	s.beforeFold = func(ctx context.Context, _ conversations.ConversationID) error {
		once.Do(func() { close(reached) })
		select {
		case <-release:
			return errors.New("fixture recovery failure")
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	testStoreAppend(t, h, testStoreA, testMessage(0))
	check := func(state SnapshotState) {
		t.Helper()
		got, err := s.CatchUp(context.Background(), testStoreA, "", 0, 1)
		want := QueryResult{State: state}
		if state == StateUnavailable {
			want.Err = ErrUnavailable
		}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatal(got, err)
		}
	}
	check(StateNotLoaded)
	if err := s.Load(context.Background(), testStoreA); err != nil {
		t.Fatal(err)
	}
	testStoreSignal(t, reached)
	check(StateRebuilding)
	close(release)
	testStoreWait(t, s, testStoreA, StateUnavailable, 0)
	check(StateUnavailable)
	s.beforeFold = nil
	if err := s.Retry(context.Background(), testStoreA); err != nil {
		t.Fatal(err)
	}
	testStoreWait(t, s, testStoreA, StateUsable, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := s.CatchUp(ctx, testStoreA, "", 0, 1)
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, QueryResult{}) {
		t.Fatal(got, err)
	}
	if err := s.Shutdown(); err != nil {
		t.Fatal(err)
	}
	check(StateNotLoaded)
}

func TestCatchUpMissingHistory(t *testing.T) {
	f := New("a")
	testFeed(t, f, testMessage(1), testEntry(3, "unknown", `{}`), testMessage(4))
	s := NewStore(nil)
	snap := Snapshot{State: StateUsable, Items: f.Items(), Epoch: "epoch", Version: 4}
	q := newQueryIndex(snap.Items, nil)
	q.prepareCatchUp(f)
	s.workers[testStoreA] = &conversationWorker{snapshot: snap, query: q}
	for _, tc := range []struct {
		after uint64
		reset bool
	}{{1, true}, {2, false}, {3, false}, {4, false}} {
		got, err := s.CatchUp(context.Background(), testStoreA, "epoch", tc.after, 1)
		if err != nil || got.ResetRequired != tc.reset {
			t.Fatal(tc, got, err)
		}
		if tc.reset && (got.Items != nil || got.FromVersion != 0) {
			t.Fatal("partial range", got)
		}
	}
}

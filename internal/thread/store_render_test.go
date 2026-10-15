package thread

import (
	"context"
	"errors"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
)

func TestStoreReferencePublication(t *testing.T) {
	entries := testFoldCorpus(6738)
	f, source := testReferenceFold(entries)
	f.captureSet = make(map[int]struct{})
	source.chunk = entries
	testFeed(t, f, entries...)
	source.remember(entries)
	var c publicationCapture
	slots, changes, err := c.capture(f)
	if err != nil {
		t.Fatal(err)
	}
	q := newPublicationIndex(slots, nil)
	q.prepareCatchUp(f)
	source.chunk = nil
	changes = Observation{} // Initial additions are transient, not a retained ring.
	runtime.GC()
	var beforeContent, beforeSummary, afterContent, afterSummary, deferred, recipes, parentEndings int
	output, err := f.Render()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range output {
		r := f.recipes[item.ID]
		if r.creating == 0 {
			deferred += len(item.Content)
			continue
		}
		beforeContent += len(item.Content)
		if item.Kind == "assistant_message" || item.Kind == "user_message" {
			beforeSummary += len(item.Summary)
		}
		recipes += int(unsafe.Sizeof(r)) + len(r.text)*8
		for key, field := range r.fields {
			recipes += len(key) + int(unsafe.Sizeof(field)) + len(field.value)
			if key == "parent_ending" {
				parentEndings += len(field.value)
			}
		}
		row, _ := q.lookup(item.ID, false)
		afterContent += len(row.item.Content)
		if item.Kind == "assistant_message" || item.Kind == "user_message" {
			afterSummary += len(row.item.Summary)
		}
	}
	if beforeContent == 0 || beforeSummary == 0 || afterContent != 0 || afterSummary != 0 || len(changes.Items) != 0 {
		t.Fatalf("converted publication/query retention Content=%d -> %d Summary=%d -> %d", beforeContent, afterContent, beforeSummary, afterSummary)
	}
	private := 0
	for _, raw := range f.legacyOutcomes {
		private += len(raw)
	}
	if private != 0 || parentEndings != 0 {
		t.Fatalf("copied private outcomes=%d parent endings=%d", private, parentEndings)
	}
	t.Logf("entries=%d GC complete; publication/query converted Content=%d -> %d Summary=%d -> %d; logical public recipes=%d (shared values, excludes container overhead); recent cache=%d; deferred public inline=%d private outcomes=%d recipe parent-ending=%d; history cache=0 (fixture history input excluded); change ring=0 (initial publication)", len(entries), beforeContent, afterContent, beforeSummary, afterSummary, recipes, source.bytes, deferred, private, parentEndings)
	runtime.KeepAlive(c)
	runtime.KeepAlive(q)
}

func TestStoreSelectedLookup(t *testing.T) {
	s, h := testThreadStore(t)
	for range 300 {
		testStoreAppend(t, h, testStoreA, testMessage(0))
	}
	base := s.lookup
	var armed atomic.Bool
	s.lookup = func(ctx context.Context, id conversations.ConversationID, ids []uint64) ([]history.Entry, error) {
		if armed.Load() && !reflect.DeepEqual(ids, []uint64{2}) {
			t.Errorf("page resolved unrelated references: %v", ids)
		}
		return base(ctx, id, ids)
	}
	if err := s.Load(context.Background(), testStoreA); err != nil {
		t.Fatal(err)
	}
	testStoreWait(t, s, testStoreA, StateUsable, 300)
	armed.Store(true)
	got, err := s.HistoryPage(context.Background(), testStoreA, 3, 1)
	if err != nil || len(got.Items) != 1 || got.Items[0].ID != 2 || len(got.Items[0].Content) == 0 {
		t.Fatal(got, err)
	}
	got.Items[0].Content[0] = '!'
	again, err := s.HistoryPage(context.Background(), testStoreA, 3, 1)
	if err != nil || again.Items[0].Content[0] == '!' {
		t.Fatal("result aliases another output", err)
	}
}

type outputRequestKey struct{}

func TestStoreCapturedRecipes(t *testing.T) {
	s, h := testThreadStore(t)
	testStoreAppend(t, h, testStoreA, testMain(0, "assistant_delta", `,"text":"a"`))
	base := s.lookup
	reached, release := make(chan struct{}), make(chan struct{})
	s.lookup = func(ctx context.Context, id conversations.ConversationID, ids []uint64) ([]history.Entry, error) {
		if ctx.Value(outputRequestKey{}) != nil {
			close(reached)
			<-release
		}
		return base(ctx, id, ids)
	}
	if err := s.Load(context.Background(), testStoreA); err != nil {
		t.Fatal(err)
	}
	snap := testStoreWait(t, s, testStoreA, StateUsable, 1)
	result := make(chan QueryResult, 1)
	go func() {
		got, err := s.NewestWindow(context.WithValue(context.Background(), outputRequestKey{}, true), testStoreA, 1)
		if err != nil {
			t.Error(err)
		}
		result <- got
	}()
	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("query did not resolve its captured recipe")
	}
	testStoreAppend(t, h, testStoreA, testMain(0, "assistant_delta", `,"text":"b"`), testMain(0, "assistant_delta", `,"text":"c"`))
	testStoreWait(t, s, testStoreA, StateUsable, 3)
	close(release)
	got := <-result
	if got.Version != snap.Version || got.Epoch != snap.Epoch || len(got.Items) != 1 || got.Items[0].Rev != 1 || messageText(got.Items[0]) != "a" {
		t.Fatal("captured output changed after feed", got)
	}
	current := s.Snapshot(testStoreA)
	if messageText(current.Items[0]) != "abc" {
		t.Fatal(current)
	}
	changes, err := s.Changes(context.Background(), testStoreA, snap.Epoch, 1)
	var delta string
	for _, c := range changes.Changes {
		delta += c.TextAppend
	}
	if err != nil || delta != "bc" {
		t.Fatal("live output lost incoming suffixes", changes, err)
	}
}

func TestStoreOutputFailure(t *testing.T) {
	for _, mode := range []string{"snapshot", "page", "newest", "catch-up"} {
		for _, missing := range []bool{false, true} {
			t.Run(mode+map[bool]string{false: "/unreadable", true: "/missing"}[missing], func(t *testing.T) {
				s, h := testThreadStore(t)
				testStoreAppend(t, h, testStoreA, testMessage(0), testMessage(0))
				var armed atomic.Bool
				base := s.lookup
				s.lookup = func(ctx context.Context, id conversations.ConversationID, ids []uint64) ([]history.Entry, error) {
					if armed.Load() {
						if missing {
							entries, err := base(ctx, id, ids)
							return entries[:len(entries)-1], err
						}
						return nil, errors.New("private payload /secret/path")
					}
					return base(ctx, id, ids)
				}
				if err := s.Load(context.Background(), testStoreA); err != nil {
					t.Fatal(err)
				}
				snap := testStoreWait(t, s, testStoreA, StateUsable, 2)
				armed.Store(true)
				var got QueryResult
				var err error
				switch mode {
				case "snapshot":
					failed := s.Snapshot(testStoreA)
					got = QueryResult{State: failed.State, Items: failed.Items, Err: failed.Err, Version: failed.Version}
				case "page":
					got, err = s.HistoryPage(context.Background(), testStoreA, 3, 2)
				case "newest":
					got, err = s.NewestWindow(context.Background(), testStoreA, 2)
				case "catch-up":
					got, err = s.CatchUp(context.Background(), testStoreA, snap.Epoch, 1, 2)
				}
				if err != nil || got.State != StateUnavailable || got.Err != ErrUnavailable || got.Items != nil || got.Version != 0 {
					t.Fatal("partial or revealing failure", got, err)
				}
				s.mu.Lock()
				w := s.workers[testStoreA]
				cleared := w.query == nil && w.items == nil && w.ranges == nil
				s.mu.Unlock()
				if !cleared {
					t.Fatal("failed worker retained usable state")
				}
				<-w.done
				armed.Store(false)
				if err := s.Retry(context.Background(), testStoreA); err != nil {
					t.Fatal(err)
				}
				testStoreWait(t, s, testStoreA, StateUsable, 2)
			})
		}
	}
}

func TestStoreOutputCancellation(t *testing.T) {
	s, h := testThreadStore(t)
	testStoreAppend(t, h, testStoreA, testMessage(0))
	base := s.lookup
	reached := make(chan struct{})
	s.lookup = func(ctx context.Context, id conversations.ConversationID, ids []uint64) ([]history.Entry, error) {
		if ctx.Value(outputRequestKey{}) != nil {
			close(reached)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return base(ctx, id, ids)
	}
	if err := s.Load(context.Background(), testStoreA); err != nil {
		t.Fatal(err)
	}
	testStoreWait(t, s, testStoreA, StateUsable, 1)
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), outputRequestKey{}, true))
	defer cancel()
	result := make(chan error, 1)
	go func() {
		got, err := s.NewestWindow(ctx, testStoreA, 1)
		if got.Items != nil {
			t.Error("cancellation returned partial content")
		}
		result <- err
	}()
	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("query did not read history")
	}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if s.Snapshot(testStoreA).State != StateUsable {
		t.Fatal("request cancellation withdrew worker")
	}
}

func TestStoreOutputCancellationWhileWithdrawing(t *testing.T) {
	for _, mode := range []string{"page", "newest", "catch-up"} {
		t.Run(mode, func(t *testing.T) {
			s, h := testThreadStore(t)
			testStoreAppend(t, h, testStoreA, testMessage(0))
			lookup := s.lookup
			reached, release := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			defer releaseOnce.Do(func() { close(release) })
			s.lookup = func(ctx context.Context, id conversations.ConversationID, ids []uint64) ([]history.Entry, error) {
				if ctx.Value(outputRequestKey{}) != nil {
					close(reached)
					<-release
					return nil, errors.New("private source failure")
				}
				return lookup(ctx, id, ids)
			}
			if err := s.Load(context.Background(), testStoreA); err != nil {
				t.Fatal(err)
			}
			testStoreWait(t, s, testStoreA, StateUsable, 1)
			testStoreAppend(t, h, testStoreA, testMessage(0))
			snap := testStoreWait(t, s, testStoreA, StateUsable, 2)
			ctx, cancel := context.WithCancel(context.WithValue(context.Background(), outputRequestKey{}, true))
			defer cancel()
			type outcome struct {
				result QueryResult
				err    error
			}
			done := make(chan outcome, 1)
			go func() {
				var got outcome
				switch mode {
				case "page":
					got.result, got.err = s.HistoryPage(ctx, testStoreA, 3, 1)
				case "newest":
					got.result, got.err = s.NewestWindow(ctx, testStoreA, 1)
				case "catch-up":
					got.result, got.err = s.CatchUp(ctx, testStoreA, snap.Epoch, 1, 1)
				}
				done <- got
			}()
			testStoreSignal(t, reached)
			func() {
				s.mu.Lock()
				defer s.mu.Unlock()
				releaseOnce.Do(func() { close(release) })
				// Observe withdrawal blocked on the mutex before cancelling, so the
				// earlier post-lookup context check has already returned nil.
				stack := make([]byte, 1<<20)
				deadline := time.Now().Add(5 * time.Second)
				for !strings.Contains(string(stack[:runtime.Stack(stack, true)]), "(*Store).withdraw(") {
					if time.Now().After(deadline) {
						t.Fatal("request did not reach contended withdrawal")
					}
					time.Sleep(time.Millisecond)
				}
				cancel()
			}()
			select {
			case got := <-done:
				if !errors.Is(got.err, context.Canceled) || !reflect.DeepEqual(got.result, QueryResult{}) {
					t.Fatal("cancelled request returned usable output", got)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("cancelled withdrawal did not finish")
			}
			if got := s.Status(testStoreA); got.State != StateUsable || got.Version != snap.Version || got.Epoch != snap.Epoch {
				t.Fatal("cancelled request withdrew publication", got)
			}
			if got, err := s.NewestWindow(context.Background(), testStoreA, 1); err != nil || got.State != StateUsable || len(got.Items) != 1 {
				t.Fatal("cancelled request cleared query state", got, err)
			}
			if got, err := s.Changes(context.Background(), testStoreA, snap.Epoch, 1); err != nil || got.BaselineRequired || len(got.Changes) != 1 {
				t.Fatal("cancelled request cleared retained changes", got, err)
			}
			testStoreAppend(t, h, testStoreA, testMessage(0))
			testStoreWait(t, s, testStoreA, StateUsable, 3)
		})
	}
}

func TestStoreOldRenderFailure(t *testing.T) {
	s, h := testThreadStore(t)
	testStoreAppend(t, h, testStoreA, testMessage(0))
	base := s.lookup
	var broken atomic.Bool
	reached, release := make(chan struct{}), make(chan struct{})
	s.lookup = func(ctx context.Context, id conversations.ConversationID, ids []uint64) ([]history.Entry, error) {
		if ctx.Value(outputRequestKey{}) != nil {
			close(reached)
			<-release
			return nil, errors.New("old worker private path")
		}
		if broken.Load() {
			return nil, errors.New("unreadable")
		}
		return base(ctx, id, ids)
	}
	if err := s.Load(context.Background(), testStoreA); err != nil {
		t.Fatal(err)
	}
	testStoreWait(t, s, testStoreA, StateUsable, 1)
	done := make(chan QueryResult, 1)
	go func() {
		got, _ := s.NewestWindow(context.WithValue(context.Background(), outputRequestKey{}, true), testStoreA, 1)
		done <- got
	}()
	testStoreSignal(t, reached)
	broken.Store(true)
	if s.Snapshot(testStoreA).State != StateUnavailable {
		t.Fatal("second output did not withdraw worker")
	}
	s.mu.Lock()
	old := s.workers[testStoreA]
	s.mu.Unlock()
	<-old.done
	broken.Store(false)
	if err := s.Retry(context.Background(), testStoreA); err != nil {
		t.Fatal(err)
	}
	testStoreWait(t, s, testStoreA, StateUsable, 1)
	close(release)
	if got := <-done; got.State != StateUnavailable || got.Items != nil || got.Err != ErrUnavailable {
		t.Fatal(got)
	}
	if s.Snapshot(testStoreA).State != StateUsable {
		t.Fatal("old captured failure withdrew Retry replacement")
	}
}

func TestStoreSelectedRecipeClosure(t *testing.T) {
	s, h := testThreadStore(t)
	entries := testStoreAppend(t, h, testStoreA,
		testMain(0, "tool_use", `,"tool_use_id":"parent","name":"Agent"`),
		testChild(0, "assistant_delta", "parent", `,"text":"a"`),
		testChild(0, "assistant_delta", "parent", `,"text":"b"`),
		testChild(0, "tool_use", "parent", `,"tool_use_id":"r","name":"Read"`),
		testChild(0, "tool_result", "parent", `,"tool_use_id":"r","is_error":false`),
		testMain(0, "tool_result", `,"tool_use_id":"parent","is_error":false`))
	var armed atomic.Bool
	base := s.lookup
	s.lookup = func(ctx context.Context, id conversations.ConversationID, ids []uint64) ([]history.Entry, error) {
		if armed.Load() && !reflect.DeepEqual(ids, []uint64{1, 4, 5, 6}) {
			t.Errorf("selected tool resolved unrelated references: %v", ids)
		}
		return base(ctx, id, ids)
	}
	if err := s.Load(context.Background(), testStoreA); err != nil {
		t.Fatal(err)
	}
	testStoreWait(t, s, testStoreA, StateUsable, 6)
	armed.Store(true)
	got, err := s.HistoryPage(context.Background(), testStoreA, 5, 1)
	f := New(string(testStoreA))
	testFeed(t, f, entries...)
	want, _, _ := testQueryReference(f.Items(), 5, 1, false)
	if err != nil || len(got.Items) != 2 {
		t.Fatal(got, err)
	}
	testQueryItems(t, got.Items, want)
}

func TestStoreRenderRetryJoinsProducer(t *testing.T) {
	s, h := testThreadStore(t)
	testStoreAppend(t, h, testStoreA, testMessage(0))
	finish, reached := make(chan struct{}), make(chan struct{}, 1)
	var finishOnce sync.Once
	defer finishOnce.Do(func() { close(finish) })
	forward, lookup := s.forward, s.lookup
	var broken atomic.Bool
	s.forward = func(id conversations.ConversationID) (storeReader, error) {
		r, err := forward(id)
		return testStoreReader{storeReader: r, tail: func(ctx context.Context, _ func([]history.Entry) error) error {
			reached <- struct{}{}
			<-ctx.Done()
			<-finish
			return ctx.Err()
		}}, err
	}
	s.lookup = func(ctx context.Context, id conversations.ConversationID, ids []uint64) ([]history.Entry, error) {
		if broken.Load() {
			return nil, ErrUnavailable
		}
		return lookup(ctx, id, ids)
	}
	if err := s.Load(context.Background(), testStoreA); err != nil {
		t.Fatal(err)
	}
	testStoreSignal(t, reached)
	broken.Store(true)
	if s.Snapshot(testStoreA).State != StateUnavailable {
		t.Fatal("render failure did not withdraw worker")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := s.Retry(ctx, testStoreA); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("Retry replaced a producer before joining it", err)
	}
	retired := make(chan error, 1)
	go func() { retired <- s.Unload(testStoreA) }()
	testStoreWait(t, s, testStoreA, StateNotLoaded, 0)
	finishOnce.Do(func() { close(finish) })
	if err := <-retired; err != ErrUnavailable {
		t.Fatal("render failure was certified as clean retirement", err)
	}
}

func TestStoreLiveReferenceSources(t *testing.T) {
	s, h := testThreadStore(t)
	testStoreAppend(t, h, testStoreA, testMain(0, "assistant_delta", `,"text":"old"`))
	lookup := s.lookup
	var misses atomic.Int64
	s.lookup = func(ctx context.Context, id conversations.ConversationID, ids []uint64) ([]history.Entry, error) {
		if ctx.Value(outputRequestKey{}) != nil {
			misses.Add(int64(len(ids)))
			if !reflect.DeepEqual(ids, []uint64{1, 2}) {
				t.Errorf("live source reread feed/cache hits: %v", ids)
			}
		}
		return lookup(ctx, id, ids)
	}
	if err := s.Load(context.WithValue(context.Background(), outputRequestKey{}, true), testStoreA); err != nil {
		t.Fatal(err)
	}
	first := testStoreWait(t, s, testStoreA, StateUsable, 1)
	testStoreAppend(t, h, testStoreA, testMain(0, "assistant_delta", `,"text":" tail"`))
	testStoreWait(t, s, testStoreA, StateUsable, 2)
	for range 257 {
		testStoreAppend(t, h, testStoreA, testEntry(0, "ignored", `{}`))
	}
	testStoreWait(t, s, testStoreA, StateUsable, 259)
	if misses.Load() != 0 {
		t.Fatal("live feed/recent hits caused history lookup", misses.Load())
	}
	testStoreAppend(t, h, testStoreA, testMain(0, "assistant_delta", `,"text":" new"`))
	testStoreWait(t, s, testStoreA, StateUsable, 260)
	if misses.Load() != 4 { // before/after comparisons each resolve the two evicted text entries
		t.Fatal("live output did not resolve exactly its evicted references", misses.Load())
	}
	got, err := s.Changes(context.Background(), testStoreA, first.Epoch, 259)
	if err != nil || len(got.Changes) != 1 || got.Changes[0].PreviousRev != 2 || got.Changes[0].TextAppend != " new" {
		t.Fatal(got, err)
	}
}

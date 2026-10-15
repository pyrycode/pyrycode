package thread

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
)

// testRetryContext pauses the first join after the store releases its mutex.
// Every Done call returns the same cancellation channel.
type testRetryContext struct {
	context.Context
	entered, waiting chan struct{}
	resume           <-chan struct{}
	calls            atomic.Int32
}

func (c *testRetryContext) Done() <-chan struct{} {
	switch c.calls.Add(1) {
	case 1:
		close(c.entered)
		<-c.resume
	case 2:
		close(c.waiting)
	}
	return c.Context.Done()
}

func TestStoreConcurrentRetryWithdrawal(t *testing.T) {
	s, h := testThreadStore(t)
	testStoreAppend(t, h, testStoreA, testMessage(0))
	first, second, resume := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var firstOnce, secondOnce, resumeOnce sync.Once
	reached := make(chan struct{}, 3)
	forward, lookup := s.forward, s.lookup
	var producers atomic.Int32
	var broken atomic.Bool
	s.forward = func(id conversations.ConversationID) (storeReader, error) {
		r, err := forward(id)
		finish := second
		if producers.Add(1) == 1 {
			finish = first
		}
		return testStoreReader{storeReader: r, tail: func(ctx context.Context, _ func([]history.Entry) error) error {
			reached <- struct{}{}
			<-ctx.Done()
			<-finish
			return ctx.Err()
		}}, err
	}
	s.lookup = func(ctx context.Context, id conversations.ConversationID, ids []uint64) ([]history.Entry, error) {
		if broken.Load() {
			return nil, errors.New("private source failure")
		}
		return lookup(ctx, id, ids)
	}
	parent, cancel := context.WithCancel(context.Background())
	ctx := &testRetryContext{Context: parent, entered: make(chan struct{}), waiting: make(chan struct{}), resume: resume}
	result, done := make(chan error, 1), make(chan struct{})
	started := false
	t.Cleanup(func() {
		firstOnce.Do(func() { close(first) })
		secondOnce.Do(func() { close(second) })
		resumeOnce.Do(func() { close(resume) })
		cancel()
		if started {
			testStoreSignal(t, done)
		}
	})
	withdraw := func() {
		t.Helper()
		broken.Store(true)
		if got := s.Snapshot(testStoreA); got.State != StateUnavailable || got.Err != ErrUnavailable {
			t.Fatal("render failure did not withdraw worker", got)
		}
		broken.Store(false)
	}
	if err := s.Load(context.Background(), testStoreA); err != nil {
		t.Fatal(err)
	}
	testStoreSignal(t, reached)
	withdraw()
	started = true
	go func() {
		defer close(done)
		result <- s.Retry(ctx, testStoreA)
	}()
	testStoreSignal(t, ctx.entered)
	// Retry B replaces W0 while Retry A is paused in its first join.
	firstOnce.Do(func() { close(first) })
	if err := s.Retry(context.Background(), testStoreA); err != nil {
		t.Fatal(err)
	}
	testStoreSignal(t, reached)
	s.mu.Lock()
	replacement := s.workers[testStoreA]
	s.mu.Unlock()
	withdraw()
	resumeOnce.Do(func() { close(resume) })
	// A must join the withdrawn W1, even though it already joined W0.
	testStoreSignal(t, ctx.waiting)
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("Retry replaced an unjoined current producer", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Retry did not honor cancellation while joining replacement")
	}
	s.mu.Lock()
	current := s.workers[testStoreA]
	s.mu.Unlock()
	if current != replacement || producers.Load() != 2 {
		t.Fatal("Retry lost ownership of the still-exiting replacement")
	}
	select {
	case <-replacement.done:
		t.Fatal("test did not hold replacement producer open")
	default:
	}
	secondOnce.Do(func() { close(second) })
	if err := s.Retry(context.Background(), testStoreA); err != nil {
		t.Fatal(err)
	}
	testStoreSignal(t, reached)
	testStoreWait(t, s, testStoreA, StateUsable, 1)
}

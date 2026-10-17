package thread

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
)

type testCheckpointClock struct {
	mu     sync.Mutex
	writes map[conversations.ConversationID][]cacheRecord
	fail   bool
}

func testCheckpointStore(t *testing.T) (*Store, *history.Store, *testCheckpointClock) {
	t.Helper()
	s, h := testThreadStore(t)
	c := &testCheckpointClock{writes: make(map[conversations.ConversationID][]cacheRecord)}
	s.replace = func(from, to string) error {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.fail {
			return errors.New("private payload /host/path")
		}
		data, err := os.ReadFile(from)
		if err != nil {
			return err
		}
		var record cacheRecord
		if err := json.Unmarshal(data, &record); err != nil {
			return err
		}
		if err := os.Rename(from, to); err != nil {
			return err
		}
		if filepath.Base(to) != cacheName {
			return nil
		}
		id := conversations.ConversationID(filepath.Base(filepath.Dir(filepath.Dir(to))))
		c.writes[id] = append(c.writes[id], record)
		return nil
	}
	return s, h, c
}

func (c *testCheckpointClock) count(t *testing.T, id conversations.ConversationID, want int) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if got := len(c.writes[id]); got != want {
		t.Fatalf("successful certificate replacements = %d, want %d", got, want)
	}
}

func testCheckpointLoad(t *testing.T, s *Store, id conversations.ConversationID, version uint64) Snapshot {
	t.Helper()
	if err := s.Load(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	return testStoreWait(t, s, id, StateUsable, version)
}

func TestStoreCheckpointLifecycle(t *testing.T) {
	t.Parallel()
	s, h, c := testCheckpointStore(t)
	first := testCheckpointLoad(t, s, testStoreA, 0)
	if err := s.Unload(testStoreA); err != nil {
		t.Fatal(err)
	}
	c.count(t, testStoreA, 2)
	// Shutdown preserves the unloaded certificate and does not fold new history.
	testStoreAppend(t, h, testStoreA, testMessage(1))
	if err := s.Shutdown(); err != nil {
		t.Fatal(err)
	}
	c.count(t, testStoreA, 2)
	c.mu.Lock()
	last := c.writes[testStoreA][1]
	c.mu.Unlock()
	if !last.Clean || last.Version != 0 {
		t.Fatal("unloaded progress changed", last)
	}
	next := NewStore(h)
	t.Cleanup(func() { _ = next.Shutdown() })
	if got := testCheckpointLoad(t, next, testStoreA, 1); got.Epoch != first.Epoch {
		t.Fatal("clean reopen rotated epoch")
	}
}

func TestStoreCheckpointFailures(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"initial", "unload", "shutdown"} {
		t.Run(phase, func(t *testing.T) {
			s, _, c := testCheckpointStore(t)
			if phase != "initial" {
				testCheckpointLoad(t, s, testStoreA, 0)
			}
			c.mu.Lock()
			c.fail = true
			c.mu.Unlock()
			if phase == "initial" {
				if err := s.Load(context.Background(), testStoreA); err != nil {
					t.Fatal(err)
				}
				snap := testStoreWait(t, s, testStoreA, StateUnavailable, 0)
				if snap.Err != ErrUnavailable || snap.Items != nil || snap.Epoch != "" {
					t.Fatal("unsafe error", snap)
				}
			}
			if phase != "shutdown" {
				if err := s.Unload(testStoreA); err != ErrPersistence {
					t.Fatal("persistence error lost", err)
				}
				if err := s.Unload(testStoreA); err != ErrPersistence {
					t.Fatal("repeated error lost", err)
				}
			}
			if err := s.Shutdown(); err != ErrPersistence {
				t.Fatal("failed retirement certified", err)
			}
		})
	}
}

func TestStoreCheckpointUncleanContinuation(t *testing.T) {
	t.Parallel()
	fixture := []history.Entry{
		testMain(1, "assistant_delta", `,"text":"unfinished "`),
		testChild(2, "tool_use", "parent", `,"tool_use_id":"nested","name":"Read"`),
		testTask(3, "background_task_updated", `,"status":"completed","summary":"early"`),
		testTask(4, "background_task_started", `,"tool_call_id":"parent"`),
		testMain(5, "tool_use", `,"tool_use_id":"parent","name":"Agent"`),
		testMain(6, "assistant_delta", `,"text":"continued"`),
		testChild(7, "tool_result", "parent", `,"tool_use_id":"nested","is_error":false`),
	}
	for _, cut := range []int{1, 2, 3, 4} {
		t.Run(string(rune('A'+cut)), func(t *testing.T) {
			s, h, c := testCheckpointStore(t)
			entries := testStoreAppend(t, h, testStoreA, fixture[:cut]...)
			ctx, cancel := context.WithCancel(context.Background())
			if err := s.Load(ctx, testStoreA); err != nil {
				t.Fatal(err)
			}
			first := testStoreWait(t, s, testStoreA, StateUsable, uint64(cut))
			entries = append(entries, testStoreAppend(t, h, testStoreA, fixture[cut:5]...)...)
			testStoreWait(t, s, testStoreA, StateUsable, 5)
			c.count(t, testStoreA, 1)
			cancel()
			testStoreWait(t, s, testStoreA, StateUnavailable, 0)
			if err := s.Unload(testStoreA); err != ErrUnavailable {
				t.Fatal(err)
			}
			next := NewStore(h)
			t.Cleanup(func() { _ = next.Shutdown() })
			got := testCheckpointLoad(t, next, testStoreA, 5)
			if got.Epoch == first.Epoch {
				t.Fatal("unclean reopen retained epoch")
			}
			check := func(snap Snapshot) {
				t.Helper()
				f := New(string(testStoreA))
				testFeed(t, f, entries...)
				if !reflect.DeepEqual(snap.Items, f.Items()) || snap.Version != f.Version() || snap.LastShownVersion != f.lastShown {
					t.Fatal("recovery differs from full replay", snap, f.Observe())
				}
			}
			check(got)
			for _, e := range fixture[5:] {
				entries = append(entries, testStoreAppend(t, h, testStoreA, e)...)
				check(testStoreWait(t, next, testStoreA, StateUsable, uint64(len(entries))))
			}
		})
	}
}

func TestStoreCheckpointInitialReadiness(t *testing.T) {
	t.Parallel()
	s, h, c := testCheckpointStore(t)
	testStoreAppend(t, h, testStoreA, testMessage(0))
	reached, release := make(chan struct{}), make(chan struct{})
	var once, checkpointOnce sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	replace := s.replace
	s.replace = func(from, to string) error {
		if filepath.Base(to) == cacheName {
			checkpointOnce.Do(func() { close(reached); <-release })
		}
		return replace(from, to)
	}
	if err := s.Load(context.Background(), testStoreA); err != nil {
		t.Fatal(err)
	}
	testStoreSignal(t, reached)
	if got := s.Snapshot(testStoreA); got.State != StateRebuilding || got.Version != 0 || got.Items != nil {
		t.Fatal("initial checkpoint published prematurely", got)
	}
	once.Do(func() { close(release) })
	testStoreWait(t, s, testStoreA, StateUsable, 1)
	c.count(t, testStoreA, 1)
	// Even unchanged progress requires a final clean replacement.
	if err := s.Unload(testStoreA); err != nil {
		t.Fatal(err)
	}
	c.count(t, testStoreA, 2)
	s.replace = replace
}

func TestStoreCheckpointCancelledFold(t *testing.T) {
	t.Parallel()
	s, h, c := testCheckpointStore(t)
	reached := make(chan struct{})
	calls := 0
	s.beforeFold = func(ctx context.Context, _ conversations.ConversationID) error {
		calls++
		if calls == 3 {
			close(reached)
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}
	testStoreAppend(t, h, testStoreA, testMessage(0))
	testCheckpointLoad(t, s, testStoreA, 1)
	completed := testStoreAppend(t, h, testStoreA, testMessage(0))
	testStoreWait(t, s, testStoreA, StateUsable, 2)
	testStoreAppend(t, h, testStoreA, testMessage(0))
	testStoreSignal(t, reached)
	if err := s.Unload(testStoreA); err != nil {
		t.Fatal("cancelled tail lost completed progress", err)
	}
	c.count(t, testStoreA, 2)
	c.mu.Lock()
	last := c.writes[testStoreA][1]
	c.mu.Unlock()
	if last.Version != 2 || last.LastShownVersion != 2 || last.Checksum != entryChecksum(completed[0]) {
		t.Fatal("retirement checkpoint included incomplete fold", last)
	}
}

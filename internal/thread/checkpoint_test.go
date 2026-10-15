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
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
)

type testCheckpointClock struct {
	mu      sync.Mutex
	now     time.Time
	timers  map[chan time.Time]time.Time
	writes  map[conversations.ConversationID][]cacheRecord
	times   map[conversations.ConversationID][]time.Time
	changed chan struct{}
	fail    bool
}

func testCheckpointStore(t *testing.T) (*Store, *history.Store, *testCheckpointClock) {
	t.Helper()
	s, h := testThreadStore(t)
	c := &testCheckpointClock{now: time.Unix(1000, 0), timers: make(map[chan time.Time]time.Time), writes: make(map[conversations.ConversationID][]cacheRecord), times: make(map[conversations.ConversationID][]time.Time), changed: make(chan struct{})}
	s.now = func() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
	s.newTimer = func(d time.Duration) (<-chan time.Time, func()) {
		c.mu.Lock()
		defer c.mu.Unlock()
		ch := make(chan time.Time, 1)
		c.timers[ch] = c.now.Add(d)
		c.notify()
		return ch, func() { c.mu.Lock(); defer c.mu.Unlock(); delete(c.timers, ch); c.notify() }
	}
	s.replace = func(from, to string) error {
		c.mu.Lock()
		defer c.mu.Unlock()
		if filepath.Base(to) != cacheName {
			return os.Rename(from, to)
		}
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
		id := conversations.ConversationID(filepath.Base(filepath.Dir(filepath.Dir(to))))
		c.writes[id] = append(c.writes[id], record)
		c.times[id] = append(c.times[id], c.now)
		c.notify()
		return nil
	}
	return s, h, c
}
func (c *testCheckpointClock) notify() { close(c.changed); c.changed = make(chan struct{}) }
func (c *testCheckpointClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	for ch, deadline := range c.timers {
		if !c.now.Before(deadline) {
			ch <- c.now
			delete(c.timers, ch)
		}
	}
	c.notify()
}
func (c *testCheckpointClock) count(t *testing.T, id conversations.ConversationID, want int) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if got := len(c.writes[id]); got != want {
		t.Fatalf("successful cache replacements = %d, want %d", got, want)
	}
}
func (c *testCheckpointClock) wait(t *testing.T, id conversations.ConversationID, want int) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		c.mu.Lock()
		count, ch := len(c.writes[id]), c.changed
		c.mu.Unlock()
		if count >= want {
			c.count(t, id, want)
			return
		}
		select {
		case <-ch:
		case <-deadline.C:
			t.Fatalf("cache replacements did not reach %d", want)
		}
	}
}
func testCheckpointLoad(t *testing.T, s *Store, id conversations.ConversationID, version uint64) Snapshot {
	t.Helper()
	if err := s.Load(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	return testStoreWait(t, s, id, StateUsable, version)
}

func TestStoreCheckpointSchedule(t *testing.T) {
	t.Parallel()
	s, h, c := testCheckpointStore(t)
	testCheckpointLoad(t, s, testStoreA, 0)
	c.count(t, testStoreA, 1)
	for i := 1; i <= 5; i++ {
		c.advance(5 * time.Second)
		testStoreAppend(t, h, testStoreA, testMessage(0))
		snap := testStoreWait(t, s, testStoreA, StateUsable, uint64(i))
		if snap.LastShownVersion != uint64(i) {
			t.Fatal("tail did not publish shown progress")
		}
		c.count(t, testStoreA, 1)
	}
	c.advance(4 * time.Second)
	c.count(t, testStoreA, 1)
	c.advance(time.Second)
	c.wait(t, testStoreA, 2) // no further append to trigger flush
	// The next publication proves the timer write has completed on the worker.
	testStoreAppend(t, h, testStoreA, testEntry(0, "unsupported", `{}`))
	got := testStoreWait(t, s, testStoreA, StateUsable, 6)
	if got.LastShownVersion != 5 {
		t.Fatal("unsupported entry changed shown version")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	changes, err := s.Changes(ctx, testStoreA, got.Epoch, 5)
	if err != nil || changes.Version != 6 || len(changes.Changes) != 0 {
		t.Fatal("no-item progress did not notify", changes, err)
	}
	c.advance(29 * time.Second)
	c.count(t, testStoreA, 2)
	c.advance(time.Second)
	c.wait(t, testStoreA, 3)
	// Unchanged state has no pending periodic timer after its successful write.
	if err := s.Unload(testStoreA); err != nil {
		t.Fatal(err)
	}
	c.count(t, testStoreA, 3)
	testCheckpointLoad(t, s, testStoreA, 6)
	c.count(t, testStoreA, 4)
	c.advance(2 * time.Minute)
	c.mu.Lock()
	timers := len(c.timers)
	c.mu.Unlock()
	if timers != 0 {
		t.Fatal("unchanged progress armed a periodic timer")
	}
	c.count(t, testStoreA, 4)
	testStoreAppend(t, h, testStoreA, testEntry(0, "unsupported", `{}`))
	testStoreWait(t, s, testStoreA, StateUsable, 7)
	c.count(t, testStoreA, 5) // dirtied after an already-expired deadline
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := 1; i < 3; i++ {
		if c.times[testStoreA][i].Sub(c.times[testStoreA][i-1]) < 30*time.Second {
			t.Fatal("scheduled replacements too close")
		}
	}
}

func TestStoreCheckpointIsolation(t *testing.T) {
	t.Parallel()
	s, h, c := testCheckpointStore(t)
	testCheckpointLoad(t, s, testStoreA, 0)
	c.advance(10 * time.Second)
	testCheckpointLoad(t, s, testStoreB, 0)
	for _, id := range []conversations.ConversationID{testStoreA, testStoreB} {
		testStoreAppend(t, h, id, testMessage(0))
		testStoreWait(t, s, id, StateUsable, 1)
	}
	c.advance(20 * time.Second)
	c.wait(t, testStoreA, 2)
	c.count(t, testStoreB, 1)
	c.advance(10 * time.Second)
	c.wait(t, testStoreB, 2)
	for _, id := range []conversations.ConversationID{testStoreA, testStoreB} {
		if err := s.Unload(id); err != nil {
			t.Fatal(err)
		}
		c.count(t, id, 2)
	}
}

func TestStoreCheckpointLifecycle(t *testing.T) {
	t.Parallel()
	s, h, c := testCheckpointStore(t)
	testStoreAppend(t, h, testStoreA, testMain(1, "tool_use", `,"tool_use_id":"read","name":"Read"`))
	first := testCheckpointLoad(t, s, testStoreA, 1)
	if !first.Active {
		t.Fatal("fixture not active")
	}
	c.advance(time.Second)
	testStoreAppend(t, h, testStoreA, testMain(2, "tool_result", `,"tool_use_id":"read","is_error":false`))
	if testStoreWait(t, s, testStoreA, StateUsable, 2).Active {
		t.Fatal("fixture not idle")
	}
	c.count(t, testStoreA, 2)
	testStoreAppend(t, h, testStoreA, testMessage(0))
	testStoreWait(t, s, testStoreA, StateUsable, 3)
	c.count(t, testStoreA, 2)
	c.advance(29 * time.Second)
	c.count(t, testStoreA, 2)
	c.advance(time.Second)
	c.wait(t, testStoreA, 3)
	testStoreAppend(t, h, testStoreA, testMessage(0))
	testStoreWait(t, s, testStoreA, StateUsable, 4)
	if err := s.Unload(testStoreA); err != nil {
		t.Fatal(err)
	}
	c.count(t, testStoreA, 4)
	if got := testCheckpointLoad(t, s, testStoreA, 4); got.Epoch != first.Epoch {
		t.Fatal("reload rotated epoch")
	}
	testStoreAppend(t, h, testStoreA, testMessage(0))
	testStoreWait(t, s, testStoreA, StateUsable, 5)
	if err := s.Shutdown(); err != nil {
		t.Fatal(err)
	}
	c.count(t, testStoreA, 7) // recovery, dirty retirement and final validation
	next := NewStore(h)
	t.Cleanup(func() { _ = next.Shutdown() })
	if got := testCheckpointLoad(t, next, testStoreA, 5); got.Epoch != first.Epoch {
		t.Fatal("clean reopen rotated epoch")
	}
}

func TestStoreCheckpointFailures(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"initial", "scheduled", "idle", "unload", "shutdown"} {
		t.Run(phase, func(t *testing.T) {
			s, h, c := testCheckpointStore(t)
			testStoreAppend(t, h, testStoreA, testMain(1, "tool_use", `,"tool_use_id":"read","name":"Read"`))
			if phase != "initial" {
				testCheckpointLoad(t, s, testStoreA, 1)
			}
			c.mu.Lock()
			c.fail = true
			c.mu.Unlock()
			switch phase {
			case "initial":
				if err := s.Load(context.Background(), testStoreA); err != nil {
					t.Fatal(err)
				}
			case "idle":
				testStoreAppend(t, h, testStoreA, testMain(2, "tool_result", `,"tool_use_id":"read","is_error":false`))
			default:
				testStoreAppend(t, h, testStoreA, testEntry(0, "unsupported", `{}`))
				testStoreWait(t, s, testStoreA, StateUsable, 2)
				if phase == "scheduled" {
					c.advance(30 * time.Second)
				}
			}
			if phase == "initial" || phase == "scheduled" || phase == "idle" {
				snap := testStoreWait(t, s, testStoreA, StateUnavailable, 0)
				if snap.Err != ErrUnavailable || snap.Items != nil || snap.Epoch != "" {
					t.Fatal("unsafe error", snap)
				}
			}
			if phase != "shutdown" {
				if err := s.Unload(testStoreA); err != ErrPersistence {
					t.Fatal("persistence error lost", err)
				}
			}
			if err := s.Shutdown(); err != ErrPersistence {
				t.Fatal("failed run certified", err)
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
	// No dirty progress remains, so unload performs no additional replacement.
	if err := s.Unload(testStoreA); err != nil {
		t.Fatal(err)
	}
	// Final validation writes again; it must not re-enter the one-shot gate.
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

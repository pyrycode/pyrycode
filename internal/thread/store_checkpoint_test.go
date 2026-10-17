package thread

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
)

type testWorkerTimer struct {
	ticks                 chan time.Time
	ready, reset, stopped chan struct{}
}

func testWorkerClock(s *Store) *testWorkerTimer {
	c := &testWorkerTimer{make(chan time.Time), make(chan struct{}), make(chan struct{}, 1), make(chan struct{})}
	s.newTimer = func() checkpointTimer {
		close(c.ready)
		return checkpointTimer{c.ticks, func() { c.reset <- struct{}{} }, func() { close(c.stopped) }}
	}
	return c
}

func (c *testWorkerTimer) tick(t *testing.T) {
	t.Helper()
	testStoreSignal(t, c.ready)
	select {
	case c.ticks <- time.Time{}:
	case <-time.After(5 * time.Second):
		t.Fatal("timer not consumed")
	}
	testStoreSignal(t, c.reset)
}

func testCheckpointProgress(t *testing.T, s *Store, version uint64) {
	t.Helper()
	for {
		s.mu.Lock()
		w := s.workers[testStoreA]
		snapshot, changed := w.snapshot, w.changed
		s.mu.Unlock()
		if snapshot.State == StateUsable && snapshot.Version == version {
			return
		}
		if snapshot.State == StateUnavailable {
			t.Fatal("worker unavailable")
		}
		testStoreSignal(t, changed)
	}
}

func TestStoreCheckpointTimer(t *testing.T) {
	t.Parallel()
	s, h := testThreadStore(t)
	c := testWorkerClock(s)
	var replacements atomic.Int32
	s.replace = func(from, to string) error {
		if filepath.Base(to) == checkpointStateName {
			replacements.Add(1)
		}
		return os.Rename(from, to)
	}
	if err := s.Load(context.Background(), testStoreA); err != nil {
		t.Fatal(err)
	}
	testStoreSignal(t, c.ready)
	c.tick(t) // unchanged empty progress
	if replacements.Load() != 0 {
		t.Fatal("unchanged empty checkpoint")
	}
	for version := uint64(1); version <= 4; version++ {
		testStoreAppend(t, h, testStoreA, testMessage(0))
		testCheckpointProgress(t, s, version)
		if replacements.Load() != int32(version-1) {
			t.Fatal("publication checkpointed before deadline")
		}
		c.tick(t) // includes silence after the append; publications never rearm
		if replacements.Load() != int32(version) {
			t.Fatal("changed deadline skipped")
		}
	}
	c.tick(t)
	if replacements.Load() != 4 {
		t.Fatal("unchanged progress checkpointed")
	}
	testStoreAppend(t, h, testStoreA, testEntry(0, "unsupported", `{}`))
	testCheckpointProgress(t, s, 5)
	c.tick(t)
	if replacements.Load() != 5 {
		t.Fatal("version-only change skipped")
	}
	if err := s.Unload(testStoreA); err != nil {
		t.Fatal(err)
	}
	testStoreSignal(t, c.stopped)
}

func TestStoreCheckpointPending(t *testing.T) {
	t.Parallel()
	companion := []history.Entry{testDurable(1, "agent_call_observed", `,"tool_call_id":"c","tool":"Agent"`), testMain(2, "tool_use", `,"tool_use_id":"c","name":"Agent"`), testDurable(3, "agent_call_result", `,"turn_id":"t","tool_call_id":"c","is_error":false,"status":"completed","summary":"same"`), testMain(4, "tool_result", `,"tool_call_id":"c","is_error":false,"status":"completed","summary":"same"`), testMessage(5)}
	private := []history.Entry{testChild(1, "assistant_delta", "missing", `,"text":"hidden "`), testChild(2, "assistant_delta", "missing", `,"text":"continued"`), testMessage(3), testMain(4, "tool_use", `,"tool_use_id":"missing","name":"Agent"`)}
	sends := append(testReferenceSendEntries(), testMessage(0))
	for family, input := range [][]history.Entry{testEndingEntries(), sends, companion, private} {
		t.Run(string(rune('A'+family)), func(t *testing.T) {
			s, h := testThreadStore(t)
			c := testWorkerClock(s)
			var fail atomic.Bool
			s.checkpointStep = func(stage string, action func() error) error {
				if stage == "state-directory" && fail.Load() {
					return errors.New("private /host/path payload")
				}
				return action()
			}
			var logs bytes.Buffer
			s.log = slog.New(slog.NewTextHandler(&logs, nil))
			entries := testStoreAppend(t, h, testStoreA, input[:1]...)
			if err := s.Load(context.Background(), testStoreA); err != nil {
				t.Fatal(err)
			}
			testCheckpointProgress(t, s, uint64(len(entries)))
			c.tick(t)
			_, source := testReferenceFold(entries)
			_, first, err := s.readCheckpoint(testStoreA, source)
			if err != nil {
				t.Fatal(err)
			}
			prefix, err := os.ReadFile(filepath.Join(mustCheckpointDir(t, s), first.Generation))
			if err != nil {
				t.Fatal(err)
			}
			fail.Store(true)
			for i, e := range input[1 : len(input)-1] {
				entries = append(entries, testStoreAppend(t, h, testStoreA, e)...)
				testCheckpointProgress(t, s, uint64(len(entries)))
				if i == 0 {
					c.tick(t)
				}
			}
			if strings.Count(logs.String(), "thread checkpoint failed") != 1 || strings.Contains(logs.String(), "/host/path") {
				t.Fatal("unsafe or duplicate failure diagnostic", logs.String())
			}
			fail.Store(false)
			c.tick(t)
			baseline, source := testReferenceFold(entries)
			baseline.conversationID = string(testStoreA)
			testFeed(t, baseline, entries...)
			r, saved, err := s.readCheckpoint(testStoreA, source)
			if err != nil || saved.Certificate.Clean {
				t.Fatal("periodic state", err)
			}
			p, q := testResumeProof{}, testResumeProof{}
			if p.update(t, baseline, true) != q.update(t, r, true) {
				t.Fatal("pending rows lost across publications/failure")
			}
			raw, err := os.ReadFile(filepath.Join(mustCheckpointDir(t, s), saved.Generation))
			if err != nil || !bytes.HasPrefix(raw, prefix) {
				t.Fatal("generation prefix changed", err)
			}
			suffix := testStoreAppend(t, h, testStoreA, input[len(input)-1])
			for _, f := range []*Fold{baseline, r} {
				f.source = newRecentSource(func(ids []uint64) ([]history.Entry, error) { return h.Lookup(testStoreA, ids) })
				testFeed(t, f, suffix...)
			}
			if p.update(t, baseline, false) != q.update(t, r, false) {
				t.Fatal("resumed suffix differs")
			}
		})
	}
}

func TestStoreCheckpointDirty(t *testing.T) {
	t.Parallel()
	for _, leaf := range []string{cacheName, checkpointStateName} {
		for _, stage := range []string{"replace", "directory"} {
			t.Run(leaf+"/"+stage, func(t *testing.T) {
				s, h := testThreadStore(t)
				testCheckpointLoad(t, s, testStoreA, 0)
				if err := s.Unload(testStoreA); err != nil {
					t.Fatal(err)
				}
				next := NewStore(h)
				t.Cleanup(func() { _ = next.Shutdown() })
				reached, release := make(chan struct{}), make(chan struct{})
				var once sync.Once
				t.Cleanup(func() { once.Do(func() { close(release) }) })
				block := func() error { close(reached); <-release; return errors.New("private /host/path") }
				lastLeaf := ""
				next.replace = func(from, to string) error {
					lastLeaf = filepath.Base(to)
					if lastLeaf == leaf && stage == "replace" {
						return block()
					}
					return os.Rename(from, to)
				}
				next.syncDir = func(dir string) error {
					if lastLeaf == leaf && stage == "directory" {
						lastLeaf = ""
						return block()
					}
					return syncCacheDir(dir)
				}
				if err := next.Load(context.Background(), testStoreA); err != nil {
					t.Fatal(err)
				}
				testStoreSignal(t, reached)
				if next.Status(testStoreA).State != StateRebuilding {
					t.Fatal("published before durable dirty marking")
				}
				once.Do(func() { close(release) })
				testStoreWait(t, next, testStoreA, StateUnavailable, 0)
				if err := next.Unload(testStoreA); err != ErrPersistence {
					t.Fatal("dirty failure lost", err)
				}
			})
		}
	}
}

func TestStoreCheckpointFinalFailure(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"history-sync", "state-directory", "certificate"} {
		t.Run(stage, func(t *testing.T) {
			s, _ := testThreadStore(t)
			var fail atomic.Bool
			s.checkpointStep = func(name string, action func() error) error {
				if name == stage && fail.Load() {
					return errors.New("injected")
				}
				return action()
			}
			s.replace = func(from, to string) error {
				if stage == "certificate" && fail.Load() && filepath.Base(to) == cacheName {
					return errors.New("injected")
				}
				return os.Rename(from, to)
			}
			testCheckpointLoad(t, s, testStoreA, 0)
			fail.Store(true)
			if err := s.Unload(testStoreA); err != ErrPersistence {
				t.Fatal("failed final checkpoint certified", err)
			}
			var c cacheRecord
			if err := s.readCacheFile(testStoreA, cacheName, &c); err == nil && c.Clean {
				t.Fatal("scalar clean claim survived")
			}
			var saved checkpointFileState
			if err := s.readCacheFile(testStoreA, checkpointStateName, &saved); err == nil && saved.Certificate.Clean {
				t.Fatal("index clean claim survived")
			}
			if err := s.Unload(testStoreA); err != ErrPersistence {
				t.Fatal("repeat lost failure", err)
			}
		})
	}
}

func TestStoreCheckpointFinalCancellation(t *testing.T) {
	t.Parallel()
	s, _ := testThreadStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.checkpointStep = func(stage string, action func() error) error {
		if stage == "state-directory" {
			cancel()
		}
		return action()
	}
	if err := s.Load(ctx, testStoreA); err != nil {
		t.Fatal(err)
	}
	testCheckpointProgress(t, s, 0)
	if err := s.Unload(testStoreA); err == nil {
		t.Fatal("parent cancellation during final transaction certified clean")
	}
	var c cacheRecord
	if err := s.readCacheFile(testStoreA, cacheName, &c); err == nil && c.Clean {
		t.Fatal("scalar remained clean after cancellation")
	}
	var saved checkpointFileState
	if err := s.readCacheFile(testStoreA, checkpointStateName, &saved); err == nil && saved.Certificate.Clean {
		t.Fatal("index remained clean after cancellation")
	}
}

func TestStoreCheckpointBytes(t *testing.T) {
	t.Parallel()
	var writes []int64
	var states []int
	for _, count := range []int{2, 200} {
		entries := make([]history.Entry, count)
		for i := range entries {
			entries[i] = testMessage(uint64(i + 1))
		}
		w, f, entries := testCheckpointWriter(t, entries)
		testFeed(t, f, entries...)
		c := &workerCheckpoint{writer: *w, fold: f, pending: make(map[uint64]bool)}
		snapshot := Snapshot{Version: f.Version(), LastShownVersion: f.lastShown, Epoch: strings.Repeat("c", 32)}
		if err := c.commit(snapshot, entryChecksum(entries[len(entries)-1]), false); err != nil {
			t.Fatal(err)
		}
		before := c.writer.ack.ValidLength
		r := f.recipes[1]
		r.fields = map[string]recipeField{"task_id": {value: []byte(`"recipe-only"`)}}
		f.recipes[1] = r
		if err := c.commit(snapshot, entryChecksum(entries[len(entries)-1]), false); err != nil {
			t.Fatal(err)
		}
		writes = append(writes, c.writer.ack.ValidLength-before)
		raw, err := os.ReadFile(filepath.Join(mustCheckpointDir(t, w.s), checkpointStateName))
		if err != nil {
			t.Fatal(err)
		}
		states = append(states, len(raw))
	}
	if writes[0] == 0 || writes[0] != writes[1] || states[0] >= states[1] {
		t.Fatal("incremental index/full-state accounting", writes, states)
	}
	t.Logf("index append bytes small/large=%v; complete state rewrite bytes=%v", writes, states)
}

func TestStoreCheckpointTailJoin(t *testing.T) {
	t.Parallel()
	s, h := testThreadStore(t)
	testStoreAppend(t, h, testStoreA, testMessage(1))
	c := testWorkerClock(s)
	entered, exited := make(chan struct{}), make(chan struct{})
	checkpointing, release := make(chan struct{}), make(chan struct{})
	var blocked atomic.Bool
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	s.checkpointStep = func(stage string, action func() error) error {
		if stage == "history-sync" && blocked.CompareAndSwap(false, true) {
			close(checkpointing)
			<-release
		}
		return action()
	}
	base := s.forward
	s.forward = func(id conversations.ConversationID) (storeReader, error) {
		r, err := base(id)
		return testStoreReader{storeReader: r, tail: func(ctx context.Context, feed func([]history.Entry) error) error {
			defer close(exited)
			close(entered)
			for {
				if err := feed(nil); err != nil {
					return err
				}
			}
		}}, err
	}
	if err := s.Load(context.Background(), testStoreA); err != nil {
		t.Fatal(err)
	}
	testStoreSignal(t, entered)
	ticked := make(chan struct{})
	go func() { c.tick(t); close(ticked) }()
	testStoreSignal(t, checkpointing) // busy forwarding cannot postpone the deadline
	unloaded := make(chan error, 1)
	go func() { unloaded <- s.Unload(testStoreA) }()
	testStoreSignal(t, exited)
	select {
	case <-unloaded:
		t.Fatal("retirement did not join checkpoint work")
	default:
	}
	once.Do(func() { close(release) })
	testStoreSignal(t, ticked)
	select {
	case err := <-unloaded:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("retirement did not finish")
	}
	testStoreSignal(t, c.stopped)
}

func mustCheckpointDir(t *testing.T, s *Store) string {
	t.Helper()
	dir, err := s.history.LogDir(testStoreA)
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestStoreCheckpointFinal(t *testing.T) {
	t.Parallel()
	for _, empty := range []bool{false, true} {
		t.Run(map[bool]string{false: "changed", true: "empty"}[empty], func(t *testing.T) {
			s, h := testThreadStore(t)
			var entries []history.Entry
			if !empty {
				entries = testStoreAppend(t, h, testStoreA, testMessage(1))
			}
			first := testCheckpointLoad(t, s, testStoreA, uint64(len(entries)))
			if err := s.Unload(testStoreA); err != nil {
				t.Fatal(err)
			}
			_, source := testReferenceFold(entries)
			r, saved, err := s.readCheckpoint(testStoreA, source)
			if err != nil || !saved.Certificate.Clean || r.Version() != first.Version {
				t.Fatal("joined retirement did not persist clean fold", err)
			}
			generation := saved.Generation
			if err := s.Load(context.Background(), testStoreA); err != nil {
				t.Fatal(err)
			}
			testStoreWait(t, s, testStoreA, StateUsable, first.Version)
			_, dirty, err := s.readCheckpoint(testStoreA, source)
			if err != nil || dirty.Certificate.Clean || dirty.Fold.Version != saved.Fold.Version {
				t.Fatal("load did not preserve and dirty saved boundary", err)
			}
			if err := s.Unload(testStoreA); err != nil {
				t.Fatal(err)
			}
			_, saved, err = s.readCheckpoint(testStoreA, source)
			if err != nil || !saved.Certificate.Clean || saved.Generation == generation {
				t.Fatal("full-refold worker reused old generation", err)
			}
		})
	}
}

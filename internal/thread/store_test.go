package thread

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
)

const testStoreA conversations.ConversationID = "11111111-1111-4111-8111-111111111111"
const testStoreB conversations.ConversationID = "22222222-2222-4222-8222-222222222222"

type testStoreReader struct {
	storeReader
	walk func(context.Context, uint64, func([]history.Entry) error) error
	tail func(context.Context, func([]history.Entry) error) error
}

func (r testStoreReader) Walk(ctx context.Context, h uint64, feed func([]history.Entry) error) error {
	if r.walk != nil {
		return r.walk(ctx, h, feed)
	}
	return r.storeReader.Walk(ctx, h, feed)
}
func (r testStoreReader) Tail(ctx context.Context, feed func([]history.Entry) error) error {
	if r.tail != nil {
		return r.tail(ctx, feed)
	}
	return r.storeReader.Tail(ctx, feed)
}
func testThreadStore(t *testing.T) (*Store, *history.Store) {
	t.Helper()
	h := history.New(t.TempDir())
	s := NewStore(h)
	t.Cleanup(func() { _ = s.Shutdown() })
	return s, h
}
func testStoreAppend(t *testing.T, h *history.Store, id conversations.ConversationID, entries ...history.Entry) []history.Entry {
	t.Helper()
	for i := range entries {
		e := &entries[i]
		e.Payload = []byte(strings.ReplaceAll(string(e.Payload), `"conversation_id":"a"`, `"conversation_id":"`+string(id)+`"`))
		n, err := h.AppendWithMetadata(id, e.Type, e.Payload, e.TS, history.Metadata{Session: e.Session, Shown: e.Shown})
		if err != nil {
			t.Fatal(err)
		}
		e.ID = n
	}
	return entries
}
func testStoreWait(t *testing.T, s *Store, id conversations.ConversationID, state SnapshotState, version uint64) Snapshot {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		snap := s.Snapshot(id)
		if snap.State == state && snap.Version == version {
			return snap
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("snapshot did not reach %v/%d: %#v", state, version, s.Snapshot(id))
	return Snapshot{}
}
func testStoreSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not reach barrier")
	}
}
func testStoreEqual(t *testing.T, snap Snapshot, id conversations.ConversationID, entries []history.Entry) {
	t.Helper()
	f := New(string(id))
	testFeed(t, f, entries...)
	if snap.Version != f.Version() || !reflect.DeepEqual(snap.Items, f.Items()) {
		t.Fatalf("snapshot differs from full replay: %#v, want %#v", snap, f.Items())
	}
}

func TestStoreReplayTailIsolation(t *testing.T) {
	t.Parallel()
	s, h := testThreadStore(t)
	var entries []history.Entry
	// Raw entries cross the reader's chunk bound; public items stay bounded so
	// per-entry folding work does not turn the isolation proof into a load test.
	for i := 0; i < history.MaxPageEntries+2; i++ {
		e := testEntry(0, "unsupported", `{}`)
		if i == 0 || i >= history.MaxPageEntries-1 {
			e = testMessage(0)
		}
		entries = append(entries, e)
	}
	entries = testStoreAppend(t, h, testStoreA, entries...)
	dir, err := h.LogDir(testStoreA)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	replay, release, handoff, tail := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce, tailOnce sync.Once
	var operationDone <-chan struct{}
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		tailOnce.Do(func() { close(tail) })
		cancel()
		if operationDone != nil {
			<-operationDone
		}
	})
	var mu sync.Mutex
	var seen []uint64
	deliveryChanged := make(chan struct{})
	phase := "opening replay reader"
	var foldTime time.Duration
	var replayDone, checkpointDone time.Time
	chunks := 0
	await := func(name string, signal <-chan struct{}) {
		t.Helper()
		s.mu.Lock()
		worker := s.workers[testStoreA]
		s.mu.Unlock()
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		reason := "deadline exceeded"
		select {
		case <-signal:
			return
		case <-worker.done:
			reason = "worker exited"
		case <-timer.C:
		}
		mu.Lock()
		lastPhase, consumed, count := phase, len(seen), chunks
		mu.Unlock()
		s.mu.Lock()
		state, version := worker.snapshot.State, worker.snapshot.Version
		s.mu.Unlock()
		t.Fatalf("%s: %s; phase=%s consumed=%d chunks=%d state=%v version=%d", name, reason, lastPhase, consumed, count, state, version)
	}
	replace := s.replace
	s.replace = func(src, dst string) error {
		err := replace(src, dst)
		if err == nil && filepath.Dir(dst) == dir && filepath.Base(dst) == cacheName {
			mu.Lock()
			checkpointDone = time.Now()
			phase = "preparing publication"
			mu.Unlock()
		}
		return err
	}
	base := s.forward
	s.forward = func(id conversations.ConversationID) (storeReader, error) {
		r, err := base(id)
		if err != nil || id != testStoreA {
			return r, err
		}
		first := true
		record := func(feed func([]history.Entry) error) func([]history.Entry) error {
			return func(chunk []history.Entry) error {
				if len(chunk) > history.MaxPageEntries {
					t.Error("oversized chunk")
				}
				mu.Lock()
				phase = "folding chunk"
				mu.Unlock()
				started := time.Now()
				err := feed(chunk)
				mu.Lock()
				foldTime += time.Since(started)
				if err == nil {
					chunks++
					for _, e := range chunk {
						seen = append(seen, e.ID)
					}
					close(deliveryChanged)
					deliveryChanged = make(chan struct{})
				}
				mu.Unlock()
				if err == nil && first {
					first = false
					mu.Lock()
					phase = "paused partial replay"
					mu.Unlock()
					close(replay)
					select {
					case <-release:
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				return err
			}
		}
		return testStoreReader{storeReader: r, walk: func(ctx context.Context, h uint64, feed func([]history.Entry) error) error {
			err := r.Walk(ctx, h, record(feed))
			mu.Lock()
			replayDone = time.Now()
			phase = "checkpointing replay"
			if err != nil {
				phase = "replay walk failed"
			}
			mu.Unlock()
			return err
		}, tail: func(ctx context.Context, feed func([]history.Entry) error) error {
			mu.Lock()
			phase = "paused tail handoff"
			mu.Unlock()
			close(handoff)
			select {
			case <-tail:
			case <-ctx.Done():
				return ctx.Err()
			}
			return r.Tail(ctx, record(feed))
		}}, nil
	}
	if err := s.Load(ctx, testStoreA); err != nil {
		t.Fatal(err)
	}
	await("initial replay barrier", replay)
	s.mu.Lock()
	worker := s.workers[testStoreA]
	s.mu.Unlock()
	if err := s.Load(ctx, testStoreA); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	duplicate := s.workers[testStoreA]
	s.mu.Unlock()
	if duplicate != worker {
		t.Fatal("duplicate load replaced worker")
	}
	partial := s.Snapshot(testStoreA)
	if partial.State != StateRebuilding || partial.Version != 0 || len(partial.Items) != 0 {
		t.Fatal("partial replay exposed a publication")
	}
	type appendResult struct {
		entry history.Entry
		err   error
	}
	result := make(chan appendResult, 1)
	done := make(chan struct{})
	operationDone = done
	go func() {
		defer close(done)
		e := testMessage(0)
		id, err := h.Append(testStoreA, e.Type, e.Payload, e.TS)
		e.ID = id
		if err == nil {
			_, err = h.Append(testStoreB, e.Type, e.Payload, e.TS)
		}
		if err == nil {
			err = s.Load(ctx, testStoreB)
		}
		result <- appendResult{e, err}
	}()
	await("appends and B load while replay paused", done)
	appended := <-result
	if appended.err != nil {
		t.Fatal(appended.err)
	}
	entries = append(entries, appended.entry)
	t.Log("waiting for B publication while A replay is paused")
	testStoreWait(t, s, testStoreB, StateUsable, 1)
	mu.Lock()
	foldBeforeRelease := foldTime
	mu.Unlock()
	started := time.Now()
	releaseOnce.Do(func() { close(release) })
	await("replay to tail handoff", handoff)
	mu.Lock()
	t.Logf("replay to handoff: total=%s fold=%s walk=%s checkpoint=%s publication=%s", time.Since(started), foldTime-foldBeforeRelease, replayDone.Sub(started), checkpointDone.Sub(replayDone), time.Since(checkpointDone))
	replayChunks := chunks
	mu.Unlock()
	if replayChunks < 2 {
		t.Fatal("replay did not cross the chunk boundary")
	}
	testStoreEqual(t, s.Snapshot(testStoreA), testStoreA, entries[:len(entries)-1])
	entries = append(entries, testStoreAppend(t, h, testStoreA, testMessage(0))...)
	tailOnce.Do(func() { close(tail) })
	t.Log("waiting for replay/handoff commits to catch up")
	testStoreEqual(t, testStoreWait(t, s, testStoreA, StateUsable, uint64(len(entries))), testStoreA, entries)
	entries = append(entries, testStoreAppend(t, h, testStoreA, testMessage(0))...)
	t.Log("waiting for the later tail commit")
	snap := testStoreWait(t, s, testStoreA, StateUsable, uint64(len(entries)))
	testStoreEqual(t, snap, testStoreA, entries)
	snap.Items[0].Content[0] = 'X'
	snap.Items[0].Summary = "mutated"
	testStoreEqual(t, s.Snapshot(testStoreA), testStoreA, entries)
	// Tail publishes inside feed; its successful delivery bookkeeping follows.
	mu.Lock()
	consumed, delivery := len(seen), deliveryChanged
	mu.Unlock()
	if consumed < len(entries) {
		await("final tail delivery bookkeeping", delivery)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != len(entries) {
		t.Fatalf("consumed %d, want %d", len(seen), len(entries))
	}
	for i, n := range seen {
		if n != uint64(i+1) {
			t.Fatal("duplicate or reordered delivery", seen)
		}
	}
}

func TestStoreContinuationReopen(t *testing.T) {
	t.Parallel()
	fixture := []history.Entry{
		testMain(1, "tool_use", `,"tool_use_id":"a","name":"Agent"`),
		testChild(2, "tool_use", "absent", `,"tool_use_id":"c","name":"Read"`),
		testMain(3, "tool_result", `,"tool_use_id":"d","is_error":false`),
		testChild(4, "tool_result", "a", `,"tool_use_id":"c","is_error":false`),
		testChild(5, "tool_use", "a", `,"tool_use_id":"d","name":"Read"`),
		testChild(6, "assistant_delta", "a", `,"text":"child"`),
		testDivider(7, "claude_clear", "old", "new", "claude", ""),
		testTransition(8, "clear", "old", "new"),
	}
	for i := range fixture {
		fixture[i].Session = testSource("claude", "source")
		fixture[i].Shown = new(bool)
	}
	for cut := 0; cut <= len(fixture); cut++ {
		t.Run(string(rune('A'+cut)), func(t *testing.T) {
			s, h := testThreadStore(t)
			entries := testStoreAppend(t, h, testStoreA, fixture[:cut]...)
			if err := s.Load(context.Background(), testStoreA); err != nil {
				t.Fatal(err)
			}
			testStoreEqual(t, testStoreWait(t, s, testStoreA, StateUsable, uint64(cut)), testStoreA, entries)
			s.Unload(testStoreA)
			if s.Snapshot(testStoreA).State != StateNotLoaded {
				t.Fatal("unload retained view")
			}
			if err := s.Load(context.Background(), testStoreA); err != nil {
				t.Fatal(err)
			}
			testStoreWait(t, s, testStoreA, StateUsable, uint64(cut))
			entries = append(entries, testStoreAppend(t, h, testStoreA, fixture[cut:]...)...)
			testStoreEqual(t, testStoreWait(t, s, testStoreA, StateUsable, uint64(len(entries))), testStoreA, entries)
			full := New(string(testStoreA))
			testFeed(t, full, entries...)
			testChildItem(t, full, 2, 1, "tool_call", "done", false)
			testChildItem(t, full, 5, 1, "tool_call", "done", false)
			other := testStoreAppend(t, h, testStoreB, testChild(1, "tool_use", "a", `,"tool_use_id":"d","name":"Read"`))
			if err := s.Load(context.Background(), testStoreB); err != nil {
				t.Fatal(err)
			}
			testStoreEqual(t, testStoreWait(t, s, testStoreB, StateUsable, 1), testStoreB, other)
			entries = append(entries, testStoreAppend(t, h, testStoreA, testChild(9, "assistant_delta", "a", `,"text":" tail"`))...)
			testStoreEqual(t, testStoreWait(t, s, testStoreA, StateUsable, 9), testStoreA, entries)
		})
	}
}

func TestStoreFailureRetry(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"replay", "tail"} {
		for _, failure := range []string{"read", "fold", "short"} {
			t.Run(phase+"/"+failure, func(t *testing.T) {
				s, h := testThreadStore(t)
				entries := testStoreAppend(t, h, testStoreA, testMessage(1), testMessage(2))
				base := s.forward
				armed := true
				s.forward = func(id conversations.ConversationID) (storeReader, error) {
					r, err := base(id)
					if err != nil {
						return nil, err
					}
					inject := armed
					armed = false
					fail := func(ctx context.Context, bound uint64, feed func([]history.Entry) error) error {
						if failure == "short" {
							return nil
						}
						return r.Walk(ctx, bound, func(chunk []history.Entry) error {
							if failure == "read" {
								if err := feed(chunk[:1]); err != nil {
									return err
								}
								return errors.New("private-payload /private/host/path")
							}
							bad := append([]history.Entry(nil), chunk...)
							bad = append(bad, bad[0])
							return feed(bad)
						})
					}
					wrap := testStoreReader{storeReader: r}
					if inject && phase == "replay" {
						wrap.walk = fail
					}
					if inject && phase == "tail" {
						wrap.tail = func(ctx context.Context, feed func([]history.Entry) error) error {
							entries = append(entries, testStoreAppend(t, h, testStoreA, testMessage(0))...)
							return fail(ctx, ^uint64(0), feed)
						}
					}
					return wrap, nil
				}
				if err := s.Load(context.Background(), testStoreA); err != nil {
					t.Fatal(err)
				}
				testStoreWait(t, s, testStoreA, StateUnavailable, 0)
				snap := s.Snapshot(testStoreA)
				if !errors.Is(snap.Err, ErrUnavailable) || snap.Items != nil || strings.Contains(snap.Err.Error(), "private") {
					t.Fatal("unsafe failure", snap)
				}
				if err := s.Load(context.Background(), testStoreA); err != nil {
					t.Fatal(err)
				}
				if s.Snapshot(testStoreA).State != StateUnavailable {
					t.Fatal("load implicitly retried")
				}
				if err := s.Retry(context.Background(), testStoreA); err != nil {
					t.Fatal(err)
				}
				testStoreEqual(t, testStoreWait(t, s, testStoreA, StateUsable, uint64(len(entries))), testStoreA, entries)
				if !errors.Is(s.Retry(context.Background(), testStoreA), ErrNotUnavailable) {
					t.Fatal("retry replaced usable worker")
				}
			})
		}
	}
}

func TestStoreReadSecurity(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"corrupt", "sibling"} {
		t.Run(failure, func(t *testing.T) {
			s, h := testThreadStore(t)
			testStoreAppend(t, h, testStoreA, testMessage(1))
			testStoreAppend(t, h, testStoreB, testMessage(1))
			dir, err := h.LogDir(testStoreA)
			if err != nil {
				t.Fatal(err)
			}
			files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
			if err != nil || len(files) != 1 {
				t.Fatal(files, err)
			}
			path := files[0]
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if failure == "corrupt" {
				if err := os.WriteFile(path, []byte("private-content /host/path\n"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				other, err := h.LogDir(testStoreB)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(dir, dir+"-saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(other, dir); err != nil {
					t.Fatal(err)
				}
			}
			damaged, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Load(context.Background(), testStoreA); err != nil {
				t.Fatal(err)
			}
			snap := testStoreWait(t, s, testStoreA, StateUnavailable, 0)
			if snap.Err != ErrUnavailable {
				t.Fatal("raw error exposed", snap.Err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(after, damaged) {
				t.Fatal("history modified", err)
			}
			if failure == "corrupt" {
				if err := os.WriteFile(path, original, 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Remove(dir); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(dir+"-saved", dir); err != nil {
					t.Fatal(err)
				}
			}
			base := s.forward
			s.forward = func(id conversations.ConversationID) (storeReader, error) {
				r, err := base(id)
				return testStoreReader{storeReader: r, tail: func(ctx context.Context, _ func([]history.Entry) error) error { <-ctx.Done(); return ctx.Err() }}, err
			}
			if err := s.Retry(context.Background(), testStoreA); err != nil {
				t.Fatal(err)
			}
			testStoreWait(t, s, testStoreA, StateUsable, 1)
			if err := os.Rename(dir, dir+"-saved"); err != nil {
				t.Fatal(err)
			}
			if s.Snapshot(testStoreA).State != StateUsable {
				t.Fatal("snapshot performed history I/O")
			}
			if err := os.Rename(dir+"-saved", dir); err != nil {
				t.Fatal(err)
			}
			after, err = os.ReadFile(path)
			if err != nil || !bytes.Equal(after, original) {
				t.Fatal("replay changed history", err)
			}

		})
	}
	s, _ := testThreadStore(t)
	if !errors.Is(s.Load(context.Background(), "../../secret"), history.ErrInvalidID) || !errors.Is(s.Retry(context.Background(), "../../secret"), history.ErrInvalidID) {
		t.Fatal("invalid ID accepted")
	}
	if !errors.Is(s.Retry(context.Background(), testStoreA), ErrNotLoaded) {
		t.Fatal("retry loaded absent conversation")
	}
}

func TestStoreLifecycle(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"replay", "tail"} {
		for _, action := range []string{"unload", "shutdown", "context"} {
			t.Run(phase+"/"+action, func(t *testing.T) {
				s, h := testThreadStore(t)
				entries := testStoreAppend(t, h, testStoreA, testMessage(1))
				reached, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
				var once sync.Once
				t.Cleanup(func() { once.Do(func() { close(release) }) })
				base := s.forward
				s.forward = func(id conversations.ConversationID) (storeReader, error) {
					r, err := base(id)
					if err != nil || id != testStoreA {
						return r, err
					}
					gate := func(ctx context.Context, feed func([]history.Entry) error) func([]history.Entry) error {
						return func(chunk []history.Entry) error {
							close(reached)
							<-ctx.Done()
							close(cancelled)
							<-release
							return feed(chunk)
						}
					}
					wrap := testStoreReader{storeReader: r}
					if phase == "replay" {
						wrap.walk = func(ctx context.Context, bound uint64, feed func([]history.Entry) error) error {
							return r.Walk(ctx, bound, gate(ctx, feed))
						}
					} else {
						wrap.tail = func(ctx context.Context, feed func([]history.Entry) error) error { return r.Tail(ctx, gate(ctx, feed)) }
					}
					return wrap, nil
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if err := s.Load(ctx, testStoreA); err != nil {
					t.Fatal(err)
				}
				if phase == "tail" {
					testStoreWait(t, s, testStoreA, StateUsable, 1)
					entries = append(entries, testStoreAppend(t, h, testStoreA, testMessage(2))...)
				}
				testStoreSignal(t, reached)
				finished := make(chan struct{})
				go func() {
					defer close(finished)
					switch action {
					case "unload":
						s.Unload(testStoreA)
					case "shutdown":
						s.Shutdown()
					case "context":
						cancel()
					}
				}()
				testStoreSignal(t, cancelled)
				if action != "context" {
					select {
					case <-finished:
						t.Fatal("teardown returned before join")
					default:
					}
					if s.Snapshot(testStoreA).State != StateNotLoaded {
						t.Fatal("retiring view retained")
					}
				}
				if action == "unload" {
					if !errors.Is(s.Load(context.Background(), testStoreA), ErrUnloading) {
						t.Fatal("competing replacement")
					}
					if err := s.Load(context.Background(), testStoreB); err != nil {
						t.Fatal(err)
					}
					testStoreWait(t, s, testStoreB, StateUsable, 0)
				}
				once.Do(func() { close(release) })
				testStoreSignal(t, finished)
				if action == "context" {
					testStoreWait(t, s, testStoreA, StateUnavailable, 0)
					s.Unload(testStoreA)
				}
				if s.Snapshot(testStoreA).State != StateNotLoaded {
					t.Fatal("late publication")
				}
				s.forward = base
				if action == "shutdown" {
					if !errors.Is(s.Load(context.Background(), testStoreA), ErrClosed) || !errors.Is(s.Retry(context.Background(), testStoreA), ErrClosed) {
						t.Fatal("work after shutdown")
					}
				} else {
					s.Unload(testStoreA)
					entries = append(entries, testStoreAppend(t, h, testStoreA, testMessage(0))...)
					if err := s.Load(context.Background(), testStoreA); err != nil {
						t.Fatal(err)
					}
					testStoreEqual(t, testStoreWait(t, s, testStoreA, StateUsable, uint64(len(entries))), testStoreA, entries)
					var wg sync.WaitGroup
					for i := 0; i < 3; i++ {
						wg.Add(1)
						go func() { defer wg.Done(); s.Shutdown() }()
					}
					wg.Wait()
					if s.Snapshot(testStoreA).State != StateNotLoaded || s.Snapshot(testStoreB).State != StateNotLoaded {
						t.Fatal("shutdown retained state")
					}
				}
			})
		}
	}
}

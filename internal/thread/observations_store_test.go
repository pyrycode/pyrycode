package thread

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
)

func TestStoreObservationRetainedSuffixMemory(t *testing.T) {
	const helperEnv = "PYRYCODE_TEST_RETAINED_SUFFIX_MEMORY"
	if os.Getenv(helperEnv) != "1" {
		t.Parallel()
		// Isolate heap accounting from concurrent tests and their workers.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestStoreObservationRetainedSuffixMemory$", "-test.v")
		cmd.Env = append(os.Environ(), helperEnv+"=1")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("suffix memory helper: %v\n%s", err, output)
		}
		t.Logf("%s", output)
		return
	}

	runtime.GC()
	var start runtime.MemStats
	runtime.ReadMemStats(&start)
	w := &conversationWorker{}
	content, err := json.Marshal(map[string]string{"text": strings.Repeat("a", maxChangeBytes)})
	if err != nil {
		t.Fatal(err)
	}
	before := Snapshot{State: StateUsable, Version: 1, Items: []Item{{ID: 1, Rev: 1, Kind: "assistant_message", Content: content}}}
	for i := uint64(2); i <= maxChangeBatches+1; i++ {
		raw, err := json.Marshal(map[string]string{"text": strings.Repeat("a", maxChangeBytes) + strings.Repeat("b", int(i-1))})
		if err != nil {
			t.Fatal(err)
		}
		after := Snapshot{State: StateUsable, Version: i, Items: []Item{{ID: 1, Rev: i, Kind: "assistant_message", Content: raw}}}
		observation := difference(before, after)
		if observation.BaselineRequired || len(observation.Changes) != 1 || observation.Changes[0].TextAppend != "b" {
			t.Fatal("expected one applicable single-byte suffix")
		}
		encoded, err := json.Marshal(observation.Changes)
		if err != nil {
			t.Fatal(err)
		}
		w.retain(changeBatch{observation: observation, bytes: len(encoded)})
		before = after
	}
	runtime.GC()
	var end runtime.MemStats
	runtime.ReadMemStats(&end)
	growth := int64(end.HeapAlloc) - int64(start.HeapAlloc)
	t.Logf("retained batches=%d accounted bytes=%d live heap growth=%d", len(w.ranges), w.rangeBytes, growth)
	if len(w.ranges) != maxChangeBatches || w.rangeBytes > maxChangeBytes {
		t.Fatal("expected all tiny suffix batches within the retention cap")
	}
	runtime.KeepAlive(w)
	// Allow transient JSON buffers, but never one full message per suffix.
	if growth > 8*maxChangeBytes {
		t.Fatal("tiny suffixes retain full messages beyond the memory bound")
	}
}

func TestStoreObservationRetention(t *testing.T) {
	s, h := testThreadStore(t)
	if err := s.Load(context.Background(), testStoreA); err != nil {
		t.Fatal(err)
	}
	base := testStoreWait(t, s, testStoreA, StateUsable, 0)
	for i := uint64(1); i <= maxChangeBatches+1; i++ {
		testStoreAppend(t, h, testStoreA, testMessage(0))
		testStoreWait(t, s, testStoreA, StateUsable, i)
	}
	for _, tc := range []struct {
		epoch string
		after uint64
	}{{base.Epoch, 0}, {"wrong", maxChangeBatches + 1}, {base.Epoch, 999}} {
		o, err := s.Changes(context.Background(), testStoreA, tc.epoch, tc.after)
		if err != nil || !o.BaselineRequired || len(o.Changes) != 0 || o.Items != nil {
			t.Fatal(o, err)
		}
	}
	testStoreAppend(t, h, testStoreA, testEntry(0, "message", `{"role":"user","text":"`+strings.Repeat("<", maxChangeBytes/5)+`"}`))
	testStoreWait(t, s, testStoreA, StateUsable, maxChangeBatches+2)
	o, err := s.Changes(context.Background(), testStoreA, base.Epoch, maxChangeBatches+1)
	if err != nil || !o.BaselineRequired {
		t.Fatal("oversize retained", o, err)
	}
	s.mu.Lock()
	w := s.workers[testStoreA]
	n, bytes := len(w.ranges), w.rangeBytes
	s.mu.Unlock()
	if n > maxChangeBatches || bytes > maxChangeBytes {
		t.Fatal("unbounded", n, bytes)
	}
}

func TestStoreObservationRecovery(t *testing.T) {
	s, h := testThreadStore(t)
	entries := testStoreAppend(t, h, testStoreA, testAcceptance(1, "phone"), testSendOutcome(2, "send_dropped", `,"accepted_entry_id":1`, "removed"))
	if err := s.Load(context.Background(), testStoreA); err != nil {
		t.Fatal(err)
	}
	first := testStoreWait(t, s, testStoreA, StateUsable, 2)
	if first.LastShownVersion != 1 {
		t.Fatal(first)
	} // final shown set is empty, Rev is 2
	if err := s.Unload(testStoreA); err != nil {
		t.Fatal(err)
	}
	testStoreAppend(t, h, testStoreA, testEntry(0, "unknown", `{}`))
	if err := s.Load(context.Background(), testStoreA); err != nil {
		t.Fatal(err)
	}
	reload := testStoreWait(t, s, testStoreA, StateUsable, 3)
	if reload.Epoch != first.Epoch || reload.LastShownVersion != 1 || !sameItems(first.Items, reload.Items) {
		t.Fatal(reload)
	}
	o, err := s.Changes(context.Background(), testStoreA, first.Epoch, 2)
	if err != nil || !o.BaselineRequired {
		t.Fatal("reload claimed continuity", o, err)
	}
	if err := s.Shutdown(); err != nil {
		t.Fatal(err)
	}
	reopened := NewStore(h)
	defer reopened.Shutdown()
	if err := reopened.Load(context.Background(), testStoreA); err != nil {
		t.Fatal(err)
	}
	recovered := testStoreWait(t, reopened, testStoreA, StateUsable, 3)
	f := New(string(testStoreA))
	testFeed(t, f, entries...)
	if recovered.Epoch != first.Epoch || recovered.LastShownVersion != f.Observe().LastShownVersion {
		t.Fatal(recovered)
	}
	testStoreAppend(t, h, testStoreA, testMessage(0))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	continuation, err := reopened.Changes(ctx, testStoreA, recovered.Epoch, 3)
	if err != nil || continuation.BaselineRequired || !sameItems(testApplyObservation(t, recovered.Items, continuation), reopened.Snapshot(testStoreA).Items) {
		t.Fatal(continuation, err)
	}
}

func TestStoreObservationLifecycle(t *testing.T) {
	for _, action := range []string{"cancel", "unload", "shutdown", "failure"} {
		t.Run(action, func(t *testing.T) {
			s, h := testThreadStore(t)
			ctx, stop := context.WithCancel(context.Background())
			defer stop()
			if err := s.Load(ctx, testStoreA); err != nil {
				t.Fatal(err)
			}
			base := testStoreWait(t, s, testStoreA, StateUsable, 0)
			waiting, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				o, err := s.Changes(waiting, testStoreA, base.Epoch, 0)
				if action != "cancel" && (o.State == StateUsable || o.LastShownVersion != 0 || o.Version != 0) {
					err = errors.New("usable lifecycle result")
				}
				done <- err
			}()
			switch action {
			case "cancel":
				cancel()
			case "unload":
				if err := s.Unload(testStoreA); err != nil {
					t.Fatal(err)
				}
			case "shutdown":
				if err := s.Shutdown(); err != nil {
					t.Fatal(err)
				}
			case "failure":
				stop()
				testStoreWait(t, s, testStoreA, StateUnavailable, 0)
			}
			select {
			case err := <-done:
				if (action == "cancel" && !errors.Is(err, context.Canceled)) || (action != "cancel" && err != nil) {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("waiter leaked")
			}
			if action != "shutdown" {
				testStoreAppend(t, h, testStoreB, testMessage(0))
				if err := s.Load(context.Background(), testStoreB); err != nil {
					t.Fatal(err)
				}
				testStoreWait(t, s, testStoreB, StateUsable, 1)
			}
		})
	}
	t.Run("rebuilding", func(t *testing.T) {
		started := make(chan struct{})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		h := history.New(t.TempDir())
		testStoreAppend(t, h, testStoreA, testMessage(0))
		s := NewStore(h, func(ctx context.Context, _ conversations.ConversationID) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		})
		defer s.Shutdown()
		if err := s.Load(ctx, testStoreA); err != nil {
			t.Fatal(err)
		}
		testStoreSignal(t, started)
		o := s.Observe(testStoreA)
		if o.State != StateRebuilding || o.Items != nil || o.LastShownVersion != 0 || o.Version != 0 || o.Epoch != "" {
			t.Fatal(o)
		}
		cancel()
	})
}

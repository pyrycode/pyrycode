package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/thread"
)

// Chunk progress renews the inactivity bound but cannot certify publication.
// ctx independently bounds the entire wait, including a continually progressing replay.
func testWaitThreadMetadata(ctx context.Context, store interface {
	Status(conversations.ConversationID) thread.Snapshot
}, progress <-chan struct{}, idle time.Duration) error {
	timer := time.NewTimer(idle)
	defer timer.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		status := store.Status(testConvID)
		if status.State == thread.StateUnavailable {
			return thread.ErrUnavailable
		}
		if status.State == thread.StateUsable && status.Version == 1024 && status.LastShownVersion == 1024 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return fmt.Errorf("no replay progress for %s: %w", idle, context.DeadlineExceeded)
		case <-progress:
			timer.Reset(idle)
		case <-ticker.C:
		}
	}
}

type testThreadMetadataStatusFunc func(conversations.ConversationID) thread.Snapshot

func (f testThreadMetadataStatusFunc) Status(id conversations.ConversationID) thread.Snapshot {
	return f(id)
}

func TestThreadMetadataReadinessWait(t *testing.T) {
	t.Run("progress requires complete publication", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var mu sync.Mutex
			status := thread.Snapshot{State: thread.StateRebuilding, Version: 1024, LastShownVersion: 1024}
			store := testThreadMetadataStatusFunc(func(conversations.ConversationID) thread.Snapshot {
				mu.Lock()
				defer mu.Unlock()
				return status
			})
			progress := make(chan struct{}, 1)
			done := make(chan struct{})
			go func() {
				defer close(done)
				for chunk := 1; chunk <= 6; chunk++ {
					time.Sleep(5 * time.Second)
					mu.Lock()
					if chunk == 5 {
						status.State = thread.StateUsable
						status.LastShownVersion = 1023
					}
					if chunk == 6 {
						status.LastShownVersion = 1024
					}
					mu.Unlock()
					select {
					case progress <- struct{}{}:
					default:
					}
				}
			}()
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			start := time.Now()
			err := testWaitThreadMetadata(ctx, store, progress, 20*time.Second)
			elapsed := time.Since(start)
			<-done
			if err != nil || elapsed < 30*time.Second || elapsed >= 31*time.Second {
				t.Fatalf("readiness returned before complete publication or after progress expired: elapsed=%s err=%v", elapsed, err)
			}
		})
	})
	t.Run("inactivity expires before caller deadline", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			store := testThreadMetadataStatusFunc(func(conversations.ConversationID) thread.Snapshot {
				return thread.Snapshot{State: thread.StateRebuilding}
			})
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			start := time.Now()
			err := testWaitThreadMetadata(ctx, store, nil, 20*time.Second)
			if !errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil || time.Since(start) != 20*time.Second {
				t.Fatalf("inactivity wait: elapsed=%s err=%v caller=%v", time.Since(start), err, ctx.Err())
			}
		})
	})
	t.Run("progress cannot extend caller deadline", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			store := testThreadMetadataStatusFunc(func(conversations.ConversationID) thread.Snapshot {
				return thread.Snapshot{State: thread.StateRebuilding}
			})
			ctx, cancel := context.WithTimeout(t.Context(), 12*time.Second)
			defer cancel()
			progress := make(chan struct{}, 1)
			done := make(chan struct{})
			go func() {
				defer close(done)
				ticker := time.NewTicker(5 * time.Second)
				defer ticker.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						progress <- struct{}{}
					}
				}
			}()
			start := time.Now()
			err := testWaitThreadMetadata(ctx, store, progress, 20*time.Second)
			<-done
			if !errors.Is(err, context.DeadlineExceeded) || ctx.Err() == nil || time.Since(start) != 12*time.Second {
				t.Fatalf("caller deadline: elapsed=%s err=%v caller=%v", time.Since(start), err, ctx.Err())
			}
		})
	})
}

func TestDaemonThreadMetadataReadiness(t *testing.T) {
	if os.Getenv("PYRY_SLOW_TESTS") != "1" {
		t.Skip("full delayed replay readiness proof: run make test-slow")
	}
	var chunks atomic.Int32
	start := time.Now()
	testThreadMetadataFixture(t, func(ctx context.Context, _ conversations.ConversationID) error {
		chunks.Add(1)
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		select {
		case <-timer.C:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	if chunks.Load() < 5 || time.Since(start) <= 20*time.Second {
		t.Fatalf("controlled delay did not cross the inactivity interval: chunks=%d elapsed=%s", chunks.Load(), time.Since(start))
	}
}

func TestDaemonThreadMetadataReadinessStalled(t *testing.T) {
	h := history.New(t.TempDir())
	if _, err := h.Append(testConvID, "message", []byte(`{"role":"user","text":"held"}`), time.Time{}); err != nil {
		t.Fatal(err)
	}
	entered, exited := make(chan struct{}), make(chan struct{})
	store := thread.NewStore(h, func(ctx context.Context, _ conversations.ConversationID) error {
		close(entered)
		defer close(exited)
		<-ctx.Done()
		return ctx.Err()
	})
	workerCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		defer cancel()
		if err := store.Shutdown(); err != nil && !errors.Is(err, thread.ErrPersistence) {
			t.Error(err)
		}
		select {
		case <-exited:
		default:
			t.Error("shutdown returned before the stalled worker exited")
		}
	})
	if err := store.Load(workerCtx, testConvID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("replay never reached the controlled barrier")
	}
	ctx, stop := context.WithTimeout(t.Context(), 10*time.Second)
	defer stop()
	start := time.Now()
	err := testWaitThreadMetadata(ctx, store, nil, time.Second)
	if !errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
		t.Fatalf("inactivity wait: err=%v outer context=%v", err, ctx.Err())
	}
	status := store.Status(testConvID)
	if status.State != thread.StateRebuilding || status.Version != 0 || status.LastShownVersion != 0 {
		t.Fatalf("stalled replay published progress: %+v", status)
	}
	t.Logf("stalled readiness failed after %s; worker joined by cleanup", time.Since(start))
}

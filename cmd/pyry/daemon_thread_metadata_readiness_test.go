package main

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/thread"
)

// Chunk progress renews the inactivity bound but cannot certify publication.
// ctx independently bounds the entire wait, including a continually progressing replay.
func testWaitThreadMetadata(ctx context.Context, store *thread.Store, progress <-chan struct{}, idle time.Duration) error {
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

func TestDaemonThreadMetadataReadiness(t *testing.T) {
	var chunks atomic.Int32
	start := time.Now()
	testThreadMetadataFixture(t, func(ctx context.Context, _ conversations.ConversationID) error {
		chunks.Add(1)
		timer := time.NewTimer(3 * time.Second)
		defer timer.Stop()
		select {
		case <-timer.C:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	if chunks.Load() < 4 || time.Since(start) <= 10*time.Second {
		t.Fatalf("controlled delay did not cross the old total bound: chunks=%d elapsed=%s", chunks.Load(), time.Since(start))
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

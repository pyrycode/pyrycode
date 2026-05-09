package sessions

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
)

// withConvSweepInterval temporarily overrides convSweepInterval for the
// duration of t. Restored via t.Cleanup.
func withConvSweepInterval(t *testing.T, d time.Duration) {
	t.Helper()
	prev := convSweepInterval
	convSweepInterval = d
	t.Cleanup(func() { convSweepInterval = prev })
}

// seedConvRegistry writes a conversations.json file with `archivable`
// archive-eligible entries (LastUsedAt 60 days in the past) and `fresh`
// non-archivable entries (LastUsedAt set to time.Now()). Returns the loaded
// *conversations.Registry and the on-disk path.
func seedConvRegistry(t *testing.T, archivable, fresh int) (*conversations.Registry, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "conversations.json")
	reg := &conversations.Registry{}
	now := time.Now().UTC()
	for i := 0; i < archivable; i++ {
		reg.Create(conversations.Conversation{
			ID:         conversations.ConversationID(fmt.Sprintf("%08d-arch-4333-8444-555555555555", i)),
			Cwd:        fmt.Sprintf("/seed-arch-%d", i),
			LastUsedAt: now.Add(-60 * 24 * time.Hour),
		})
	}
	for i := 0; i < fresh; i++ {
		reg.Create(conversations.Conversation{
			ID:         conversations.ConversationID(fmt.Sprintf("%08d-fres-4333-8444-555555555555", i)),
			Cwd:        fmt.Sprintf("/seed-fresh-%d", i),
			LastUsedAt: now,
		})
	}
	if err := reg.Save(path); err != nil {
		t.Fatalf("seed Save: %v", err)
	}
	loaded, err := conversations.Load(path)
	if err != nil {
		t.Fatalf("seed Load: %v", err)
	}
	return loaded, path
}

// TestPool_Run_RegistersSweepLoop_HappyPath proves that wiring
// ConversationsRegistry into sessions.Config causes Pool.Run to spawn the
// sweep goroutine, that the goroutine actually invokes Sweep+Save, and that
// the on-disk file ends with only the non-archivable survivors.
func TestPool_Run_RegistersSweepLoop_HappyPath(t *testing.T) {
	withConvSweepInterval(t, 5*time.Millisecond)

	reg, path := seedConvRegistry(t, 2, 1)

	pool := helperPoolWithSleepArgs(t)
	pool.convReg = reg
	pool.convRegistryPath = path

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- pool.Run(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatalf("sweep did not complete within 2s")
		}
		reg2, err := conversations.Load(path)
		if err != nil {
			t.Fatalf("Load while polling: %v", err)
		}
		if len(reg2.List()) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Pool.Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Pool.Run did not return within 5s of cancel")
	}

	final, err := conversations.Load(path)
	if err != nil {
		t.Fatalf("final Load: %v", err)
	}
	survivors := final.List()
	if len(survivors) != 1 {
		t.Fatalf("len(survivors) = %d, want 1", len(survivors))
	}
	now := time.Now()
	if conversations.ShouldArchive(survivors[0], now) {
		t.Errorf("survivor %q is archive-eligible: %+v", survivors[0].ID, survivors[0])
	}
}

// TestPool_Run_NoSweepLoopWhenRegistryNil pins the negative gate: when
// ConversationsRegistry is nil, no sweep goroutine runs, and no Save happens
// at any path the loop might have used.
func TestPool_Run_NoSweepLoopWhenRegistryNil(t *testing.T) {
	withConvSweepInterval(t, 1*time.Millisecond)

	pool := helperPoolWithSleepArgs(t)
	if pool.convReg != nil {
		t.Fatalf("helperPoolWithSleepArgs unexpectedly set convReg")
	}

	path := filepath.Join(t.TempDir(), "shouldnotexist.json")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- pool.Run(ctx) }()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Pool.Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Pool.Run did not return within 5s of cancel")
	}

	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("expected %s to be absent (no sweep should have run); stat err = %v", path, err)
	}
}

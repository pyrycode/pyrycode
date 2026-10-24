package thread

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
)

func TestStoreConditionalUnload(t *testing.T) {
	h := history.New(t.TempDir())
	var hold atomic.Bool
	entered, release := make(chan struct{}, 1), make(chan struct{})
	s := NewStore(h, func(ctx context.Context, _ conversations.ConversationID) error {
		if hold.Load() {
			select {
			case entered <- struct{}{}:
			default:
			}
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	})
	t.Cleanup(func() {
		if err := s.Shutdown(); err != nil {
			t.Error(err)
		}
	})
	testStoreAppend(t, h, testStoreA, testMessage(0))
	if err := s.Load(t.Context(), testStoreA); err != nil {
		t.Fatal(err)
	}
	base := testStoreWait(t, s, testStoreA, StateUsable, 1)
	declined := func(epoch string, version uint64) {
		t.Helper()
		if retired, err := s.UnloadIfCurrent(testStoreA, epoch, version); retired || err != nil {
			t.Fatalf("unexpected retirement: %v %v", retired, err)
		}
		if got := s.Status(testStoreA); got.State != StateUsable || got.Epoch != base.Epoch {
			t.Fatal("declined retirement withdrew usable source")
		}
	}
	declined("other-epoch", 1)
	declined(base.Epoch, 0)
	declined(base.Epoch, 2)
	hold.Store(true)
	testStoreAppend(t, h, testStoreA, testEntry(0, "unsupported", `{}`))
	testStoreSignal(t, entered)
	declined(base.Epoch, 1) // snapshot equality cannot authorize an in-flight fold
	hold.Store(false)
	close(release)
	testStoreWait(t, s, testStoreA, StateUsable, 2)
	declined(base.Epoch, 1)
	if retired, err := s.UnloadIfCurrent(testStoreA, base.Epoch, 2); !retired || err != nil {
		t.Fatalf("matching idle retirement: %v %v", retired, err)
	}
	if got := s.Status(testStoreA); got.State != StateNotLoaded {
		t.Fatal("not unloaded")
	}
	if retired, err := s.UnloadIfCurrent(testStoreA, base.Epoch, 2); retired || err != nil {
		t.Fatal("absent worker retired twice")
	}
	testStoreAppend(t, h, testStoreA, testMain(0, "assistant_delta", `,"text":"active"`))
	if err := s.Load(t.Context(), testStoreA); err != nil {
		t.Fatal(err)
	}
	active := testStoreWait(t, s, testStoreA, StateUsable, 3)
	if active.Epoch != base.Epoch || !active.Active {
		t.Fatal("clean reload lost epoch or activity")
	}
	declined(base.Epoch, 3)
	if err := s.Unload(testStoreA); err != nil {
		t.Fatal(err)
	}
}

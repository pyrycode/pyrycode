package sessions

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

// raceRunner is a Runner double that holds the eviction-teardown window open so a
// test can inject a delivery at exactly the vulnerable moment. Run announces every
// spawn on `spawns` and, on the FIRST teardown (its ctx cancelled by
// runActive.cancelSup), closes `teardown` and then blocks on `release` — pinning
// the session mid-eviction until the test lets go. Later spawns (the respawn under
// test) return promptly on ctx cancel, so cleanup is clean.
//
// It never allocates a PTY, so it is safe on CI runners with no terminal — the
// stream-json runner and the bootstrap supervisor both drive the same narrow
// Runner seam this stands in for.
type raceRunner struct {
	spawns   chan struct{} // Run sends once per invocation (buffered)
	teardown chan struct{} // closed by the first Run when its ctx cancels
	release  chan struct{} // test closes to let the first Run return
	runs     atomic.Int32  // invocation counter; distinguishes the first teardown
}

func (r *raceRunner) State() State { return State{} }

func (r *raceRunner) WriteUserTurn(ctx context.Context, conversationID string, payload []byte) error {
	return nil
}

// WaitForPTY returns immediately: a re-activated raceRunner is "bound" the instant
// it respawns. The racing Activate reaches this only after its activeCh wait
// resolves (post-respawn on the fix; immediately on the closed stale channel on
// main), so nil here keeps Activate from blocking on a PTY the double never has.
func (r *raceRunner) WaitForPTY(ctx context.Context) error { return nil }

// Run announces the spawn, then parks on ctx. The first invocation (the child
// being evicted) holds the teardown window open until the test closes release;
// every later invocation just returns on cancel.
func (r *raceRunner) Run(ctx context.Context) error {
	first := r.runs.Add(1) == 1
	r.spawns <- struct{}{}
	<-ctx.Done()
	if first {
		close(r.teardown)
		<-r.release
	}
	return ctx.Err()
}

func (r *raceRunner) Restart(args []string) {}

func (r *raceRunner) SetSpawnArgs(args []string) {}

// TestSession_IdleEviction_ActivateRacingTeardownRespawns is the hermetic,
// fake-tier sibling to internal/e2e's TestE2E_IdleEviction_RespawnsOnSendMessage
// (#396): it pins the drain-respawn-after-eviction contract on the same lifecycle
// seam the stream-json runner drives, deterministically and without a live claude.
//
// #1186: an idle eviction tears the child down in runActive, but on main the
// session did not leave stateActive until the outer Run loop's
// transitionTo(stateEvicted) ran AFTER runActive returned. A send_message
// delivery that arrived during that teardown window (Activate → WaitForPTY) saw a
// still-active session, no-oped against the dying child, and was acked but
// silently dropped — no respawn, no reply. The live #1177 real-claude gate caught
// exactly this because it sends immediately on the idle_eviction WARN, landing in
// the race window; the settled-state fake tests (#396/#680) wait for "evicted"
// first, so they structurally cannot.
//
// The fix (two-phase beginEvict/endEvict) commits stateEvicted BEFORE teardown, so
// a racing Activate observes a non-active session, blocks on the fresh activeCh,
// and drives a real respawn. This test injects that racing Activate into the held
// teardown window and asserts the observable outcome — a second spawn — so it
// validates ANY correct fix, not the internals. It FAILS on main (no respawn) and
// PASSES with the fix, clean under -race.
func TestSession_IdleEviction_ActivateRacingTeardownRespawns(t *testing.T) {
	t.Parallel()

	rr := &raceRunner{
		spawns:   make(chan struct{}, 4),
		teardown: make(chan struct{}),
		release:  make(chan struct{}),
	}
	sess := &Session{
		id:          "evict-race-test",
		sup:         rr,
		log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		idleTimeout: 50 * time.Millisecond,
		lcState:     stateActive,
		activeCh:    closedChan(),
		evictedCh:   make(chan struct{}),
		activateCh:  make(chan struct{}, 1),
		evictCh:     make(chan struct{}, 1),
		removedCh:   make(chan struct{}),
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = sess.Run(ctx) }()

	// Spawn 1: the initial child. Then wait for the idle timer to fire and
	// runActive to enter its teardown window (raceRunner parks on release).
	waitSpawn(t, rr.spawns, "initial spawn")
	select {
	case <-rr.teardown:
	case <-time.After(2 * time.Second):
		t.Fatal("idle eviction never entered teardown")
	}

	// Inject the racing delivery while the teardown window is held open. On the
	// fix, lcState is already stateEvicted here, so Activate signals activateCh and
	// blocks on the fresh activeCh, driving a respawn; on main lcState is still
	// stateActive, so Activate no-ops against the dying child. `entered` proves the
	// goroutine is scheduled; the short settle lets it pass Activate's lcState
	// check. Both happen while release is unclosed — lcState CANNOT flip underneath
	// us (the flip needs the first Run to return, which needs release), so this
	// settle only covers goroutine scheduling, not a state transition. That is why
	// it is not #1181-style timing fragility.
	activateCtx, cancelActivate := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancelActivate)
	entered := make(chan struct{})
	go func() {
		close(entered)
		_ = sess.Activate(activateCtx)
	}()
	<-entered
	time.Sleep(50 * time.Millisecond)

	// Release the teardown: the child is reaped, the loop reaches runEvicted, and a
	// correct fix drives the queued Activate into a respawn.
	close(rr.release)

	// Spawn 2 is the respawn. Its absence is the #1186 silent no-reply.
	select {
	case <-rr.spawns:
	case <-time.After(3 * time.Second):
		t.Fatal("no respawn after a send raced idle-eviction teardown (#1186): the delivery was acked but silently dropped")
	}
}

// waitSpawn receives one spawn announcement or fails with context.
func waitSpawn(t *testing.T, spawns <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-spawns:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

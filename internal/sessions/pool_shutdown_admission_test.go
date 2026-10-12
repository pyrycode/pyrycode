package sessions

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

type shutdownAdmissionRunner struct {
	fakeRunner
	started  chan struct{}
	stopping chan struct{}
	release  chan struct{}
}

func (r *shutdownAdmissionRunner) Run(ctx context.Context) error {
	close(r.started)
	<-ctx.Done()
	close(r.stopping)
	<-r.release
	return ctx.Err()
}

// Hold the bootstrap in teardown so the assertion exercises shutdown itself,
// not the already-covered state after Pool.Run has returned.
func TestPool_Supervise_RejectsDuringShutdown(t *testing.T) {
	t.Parallel()
	runner := &shutdownAdmissionRunner{started: make(chan struct{}), stopping: make(chan struct{}), release: make(chan struct{})}
	pool, err := helperPoolFakeRunner(t, filepath.Join(t.TempDir(), "registry.json"), func(RunnerConfig) (Runner, error) { return runner, nil })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- pool.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		close(runner.release)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("pool did not join its sessions")
		}
	})
	select {
	case <-runner.started:
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap did not start")
	}
	cancel()
	select {
	case <-runner.stopping:
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap did not begin teardown")
	}
	if err := pool.supervise(helperDummySession(t, pool)); !errors.Is(err, ErrPoolNotRunning) {
		t.Fatalf("supervise during shutdown = %v, want ErrPoolNotRunning", err)
	}
	select {
	case <-done:
		t.Fatal("pool returned before its bootstrap finished")
	default:
	}
}

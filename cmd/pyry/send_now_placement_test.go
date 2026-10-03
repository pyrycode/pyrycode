package main

import (
	"context"
	"crypto/sha256"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// placementRig is a sendNowPlacement whose idle the test releases by hand, with
// one session ("s1") bound to testConvID and a commit counter per message id.
type placementRig struct {
	p    *sendNowPlacement
	idle chan struct{}

	mu      sync.Mutex
	commits []uint64
	fired   chan uint64
}

func newPlacementRig(t *testing.T) *placementRig {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	r := &placementRig{idle: make(chan struct{}), fired: make(chan uint64, 8)}
	r.p = newSendNowPlacement(ctx,
		func(sid string) (string, bool) { return testConvID, sid == "s1" },
		func(ctx context.Context, _ string) error {
			select {
			case <-r.idle:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	return r
}

func (r *placementRig) commitFor(id uint64) func() {
	return func() {
		r.mu.Lock()
		r.commits = append(r.commits, id)
		r.mu.Unlock()
		r.fired <- id
	}
}

func (r *placementRig) committed() []uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]uint64(nil), r.commits...)
}

func echoOf(text string) turnevent.UserEcho {
	return turnevent.UserEcho{TextSHA256: sha256.Sum256([]byte(text))}
}

// waitCommit receives one commit or fails.
func (r *placementRig) waitCommit(t *testing.T, want uint64) {
	t.Helper()
	select {
	case got := <-r.fired:
		if got != want {
			t.Fatalf("committed %d, want %d", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("no commit for %d", want)
	}
}

func TestSendNowPlacement_CommitsOnceAtTheEcho(t *testing.T) {
	t.Parallel()
	r := newPlacementRig(t)
	r.p.expect(testConvID, 1, []byte("payload one"))
	r.p.attach(testConvID, 1, r.commitFor(1))
	if got := r.committed(); len(got) != 0 {
		t.Fatalf("committed %v before the echo, want nothing", got)
	}
	r.p.echo("s1", echoOf("an opener nobody registered"))
	if got := r.committed(); len(got) != 0 {
		t.Fatalf("an unmatched echo committed %v", got)
	}
	r.p.echo("s1", echoOf("payload one"))
	r.waitCommit(t, 1)
	// The idle the waiter is parked on, and a second identical echo, find the
	// entry gone.
	close(r.idle)
	r.p.echo("s1", echoOf("payload one"))
	time.Sleep(20 * time.Millisecond)
	if got := r.committed(); len(got) != 1 {
		t.Fatalf("committed %v, want exactly one", got)
	}
}

func TestSendNowPlacement_EchoBeforeAttachCommitsAtAttach(t *testing.T) {
	t.Parallel()
	r := newPlacementRig(t)
	r.p.expect(testConvID, 1, []byte("fast"))
	r.p.echo("s1", echoOf("fast"))
	r.p.attach(testConvID, 1, r.commitFor(1))
	if got := r.committed(); len(got) != 1 || got[0] != 1 {
		t.Fatalf("committed %v, want [1] synchronously at attach", got)
	}
}

func TestSendNowPlacement_IdleWithoutEchoCommitsOnce(t *testing.T) {
	t.Parallel()
	r := newPlacementRig(t)
	r.p.expect(testConvID, 1, []byte("never echoed"))
	r.p.attach(testConvID, 1, r.commitFor(1))
	close(r.idle)
	r.waitCommit(t, 1)
	r.p.echo("s1", echoOf("never echoed"))
	time.Sleep(20 * time.Millisecond)
	if got := r.committed(); len(got) != 1 {
		t.Fatalf("a late echo committed again: %v", got)
	}
}

func TestSendNowPlacement_IdenticalPayloadsCommitInWriteOrder(t *testing.T) {
	t.Parallel()
	r := newPlacementRig(t)
	r.p.expect(testConvID, 1, []byte("same"))
	r.p.expect(testConvID, 2, []byte("same"))
	r.p.attach(testConvID, 1, r.commitFor(1))
	r.p.attach(testConvID, 2, r.commitFor(2))
	r.p.echo("s1", echoOf("same"))
	r.waitCommit(t, 1)
	r.p.echo("s1", echoOf("same"))
	r.waitCommit(t, 2)
}

func TestSendNowPlacement_CancelledWriteNeverCommits(t *testing.T) {
	t.Parallel()
	r := newPlacementRig(t)
	cancel := r.p.expect(testConvID, 1, []byte("failed write"))
	cancel()
	r.p.echo("s1", echoOf("failed write"))
	r.p.mu.Lock()
	n := len(r.p.pending)
	r.p.mu.Unlock()
	if n != 0 || len(r.committed()) != 0 {
		t.Fatalf("pending=%d committed=%v after a cancelled write, want none", n, r.committed())
	}
}

func TestSendNowPlacement_UnknownSessionIsIgnored(t *testing.T) {
	t.Parallel()
	r := newPlacementRig(t)
	r.p.expect(testConvID, 1, []byte("x"))
	r.p.attach(testConvID, 1, r.commitFor(1))
	r.p.echo("other-session", echoOf("x"))
	if got := r.committed(); len(got) != 0 {
		t.Fatalf("an echo from an unbound session committed %v", got)
	}
	close(r.idle)
	r.waitCommit(t, 1)
}

func TestSendNowPlacement_NilCommitsAtAttach(t *testing.T) {
	t.Parallel()
	var p *sendNowPlacement
	p.expect(testConvID, 1, []byte("x"))()
	n := 0
	p.attach(testConvID, 1, func() { n++ })
	p.echo("s1", echoOf("x"))
	if n != 1 {
		t.Fatalf("nil placement committed %d times, want 1", n)
	}
}

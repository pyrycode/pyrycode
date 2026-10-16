package relay

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// waitForOpenAdmission takes a Run-owned snapshot after response authentication.
// Run cannot answer until the handshake handler has also registered the queue.
func waitForOpenAdmission(ctx context.Context, mgr *V2SessionManager, connID string) error {
	conns := mgr.ActiveConns(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, conn := range conns {
		if conn.ConnID == connID {
			return nil
		}
	}
	return fmt.Errorf("connection %q not admitted", connID)
}

func TestWaitForOpenAdmission_Context(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		cancel bool
		want   error
	}{
		{name: "deadline", want: context.DeadlineExceeded},
		{name: "cancelled", cancel: true, want: context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				// With no Run receiver, the snapshot must exit via its context.
				mgr := &V2SessionManager{snapshot: make(chan snapshotReq)}
				ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
				defer cancel()
				if tc.cancel {
					time.AfterFunc(time.Second, cancel)
				}
				start := time.Now()
				if err := waitForOpenAdmission(ctx, mgr, v2TestConnID); !errors.Is(err, tc.want) {
					t.Fatalf("admission wait: %v, want %v", err, tc.want)
				}
				want := 2 * time.Second
				if tc.cancel {
					want = time.Second
				}
				if elapsed := time.Since(start); elapsed != want {
					t.Fatalf("admission wait elapsed %v, want %v", elapsed, want)
				}
			})
		})
	}
}

func TestDriveToOpen_WaitsForAdmission(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		respPriv, respPub := genV2Keypair(t)
		initPriv, _ := genV2Keypair(t)
		rec := &v2Recorder{}
		published, release := make(chan struct{}), make(chan struct{})
		unhold := sync.OnceFunc(func() { close(release) })
		var first sync.Once
		outbound := func(env protocol.RoutingEnvelope) error {
			if err := rec.outbound(env); err != nil {
				return err
			}
			first.Do(func() {
				close(published)
				<-release
			})
			return nil
		}
		frames := make(chan protocol.RoutingEnvelope, 2)
		opened := make(chan *openSession, 1)
		fixtureDone := make(chan struct{})
		var sess *openSession
		defer func() {
			unhold()
			<-fixtureDone
			if sess == nil {
				select {
				case sess = <-opened:
				default:
				}
			}
			if sess != nil {
				sess.stop()
			}
		}()
		go func() {
			defer close(fixtureDone)
			opened <- driveToOpen(t, V2SessionConfig{
				Frames: frames, Outbound: outbound, StaticPriv: respPriv,
				Devices:  v2PairedRegistry(t, v2TestToken),
				ServerID: v2TestServerID, Logger: silentLogger(),
			}, frames, rec, respPub, initPriv)
		}()
		select {
		case <-published:
		case <-fixtureDone:
			t.Fatal("fixture exited before publishing response")
		case <-time.After(5 * time.Second):
			t.Fatal("response publication timed out")
		}
		// Virtual time runs the recorder's polling timer. Wait then establishes
		// durable blocking, independent of the host scheduler or elapsed real time.
		time.Sleep(10 * time.Millisecond)
		synctest.Wait()
		select {
		case sess = <-opened:
			// Run is held inside its first outbound: no teardown or session
			// mutation can occur until release, so this state read is ordered.
			s := sess.mgr.sessions[v2TestConnID]
			sess.mgr.pushMu.Lock()
			_, queued := sess.mgr.queues[v2TestConnID]
			sess.mgr.pushMu.Unlock()
			if s == nil {
				t.Fatal("fixture returned while response held, but session absent")
			}
			err := sess.mgr.Push(t.Context(), v2TestConnID, buildMessageEnvelope(t, 1, "early"))
			t.Fatalf("fixture returned before admission: session present, state=%v, queue=%v, first push=%v", s.state, queued, err)
		default:
		}
		unhold()
		select {
		case sess = <-opened:
		case <-fixtureDone:
			// A successful send precedes fixtureDone; prefer that result if ready.
			select {
			case sess = <-opened:
			default:
				t.Fatal("fixture exited without an admitted session")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("fixture did not finish after admission release")
		}
		for _, id := range []uint64{11, 12} {
			if err := sess.mgr.Push(t.Context(), v2TestConnID, buildMessageEnvelope(t, id, "admitted")); err != nil {
				t.Fatalf("admitted push %d: %v", id, err)
			}
		}
		envs := waitForEnvelopes(t, rec, 3)
		for i, id := range []uint64{11, 12} {
			if got := decryptAppFrame(t, envs[i+1], sess.initRecv); got.ID != id {
				t.Fatalf("push order: got %d, want %d", got.ID, id)
			}
		}
	})
}

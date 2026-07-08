package relay

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// --- #831 inbound new_session → /clear routing fixtures ---

// fakeSessionStarter is a relay-side test double for SessionStarter: it counts
// StartNewSession calls and returns an injectable error. The mutex guards the
// cross-goroutine access (the Run goroutine writes via StartNewSession, the test
// goroutine reads via startCount), mirroring fakeInterrupter.
type fakeSessionStarter struct {
	mu         sync.Mutex
	startCalls int
	err        error
}

func (f *fakeSessionStarter) StartNewSession() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startCalls++
	return f.err
}

func (f *fakeSessionStarter) startCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.startCalls
}

// TestV2Session_NewSession_RoutesClearByCapability drives an inbound `new_session`
// frame through the real Frames/Run loop and asserts the manager routes exactly
// one /clear for an interactive conn and zero for a non-interactive one — the
// inbound capability gate (AC #3, AC #4). After the new_session frame, a second
// conn is opened as a barrier: because Frames is a single FIFO channel drained by
// the one Run goroutine, the new_session (enqueued first) is fully handled before
// the barrier conn opens, so startCount is final regardless of the gate's verdict.
func TestV2Session_NewSession_RoutesClearByCapability(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		caps      []string
		wantStart int
	}{
		{"interactive routes one /clear", []string{protocol.CapabilityInteractive}, 1},
		{"non-interactive is inert", nil, 0},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			respPriv, respPub := genV2Keypair(t)
			fake := &fakeSessionStarter{}
			frames := make(chan protocol.RoutingEnvelope, 8)
			rec := &v2Recorder{}
			mgr, stop := startManager(t, V2SessionConfig{
				Frames:         frames,
				Outbound:       rec.outbound,
				StaticPriv:     respPriv,
				Devices:        v2PairedRegistry(t, v2TestToken),
				ServerID:       v2TestServerID,
				Logger:         silentLogger(),
				SessionStarter: fake,
			})
			t.Cleanup(stop)

			send, _ := openModalConn(t, mgr, frames, rec, respPub, "c-int", tc.caps)
			frames <- sealAppFrameConn(t, send, "c-int", protocol.Envelope{
				Type: protocol.TypeNewSession,
				TS:   time.Now().UTC(),
			})

			// Barrier: the new_session is enqueued before this conn's noise_init, so
			// once the barrier conn is open the new_session has been handled.
			openModalConn(t, mgr, frames, rec, respPub, "c-barrier", []string{protocol.CapabilityInteractive})

			if got := fake.startCount(); got != tc.wantStart {
				t.Errorf("startCalls = %d, want %d", got, tc.wantStart)
			}
		})
	}
}

// TestV2Session_NewSession_NilStarterInert proves a nil SessionStarter (foreground
// / pre-wire) makes an interactive new_session inert: no /clear, no panic, the
// manager keeps serving. If handleNewSession mishandled the nil seam, the Run
// goroutine would be dead and the barrier conn would never open (waitConnOpen
// would fail).
func TestV2Session_NewSession_NilStarterInert(t *testing.T) {
	t.Parallel()

	respPriv, respPub := genV2Keypair(t)
	frames := make(chan protocol.RoutingEnvelope, 8)
	rec := &v2Recorder{}
	mgr, stop := startManager(t, V2SessionConfig{
		Frames:     frames,
		Outbound:   rec.outbound,
		StaticPriv: respPriv,
		Devices:    v2PairedRegistry(t, v2TestToken),
		ServerID:   v2TestServerID,
		Logger:     silentLogger(),
		// SessionStarter intentionally nil.
	})
	t.Cleanup(stop)

	send, _ := openModalConn(t, mgr, frames, rec, respPub, "c-int", []string{protocol.CapabilityInteractive})
	frames <- sealAppFrameConn(t, send, "c-int", protocol.Envelope{
		Type: protocol.TypeNewSession,
		TS:   time.Now().UTC(),
	})

	// Barrier: opening succeeds only if Run processed the new_session without
	// crashing or hanging on the nil seam.
	openModalConn(t, mgr, frames, rec, respPub, "c-barrier", []string{protocol.CapabilityInteractive})
}

// TestV2Session_NewSession_StartErrorTolerated proves a StartNewSession error (no
// live session / mid-teardown) is best-effort: the keystroke still counts as one
// attempted call, and the manager neither crashes nor closes the conn (the
// barrier conn opens afterward).
func TestV2Session_NewSession_StartErrorTolerated(t *testing.T) {
	t.Parallel()

	respPriv, respPub := genV2Keypair(t)
	fake := &fakeSessionStarter{err: errors.New("no live session")}
	frames := make(chan protocol.RoutingEnvelope, 8)
	rec := &v2Recorder{}
	mgr, stop := startManager(t, V2SessionConfig{
		Frames:         frames,
		Outbound:       rec.outbound,
		StaticPriv:     respPriv,
		Devices:        v2PairedRegistry(t, v2TestToken),
		ServerID:       v2TestServerID,
		Logger:         silentLogger(),
		SessionStarter: fake,
	})
	t.Cleanup(stop)

	send, _ := openModalConn(t, mgr, frames, rec, respPub, "c-int", []string{protocol.CapabilityInteractive})
	frames <- sealAppFrameConn(t, send, "c-int", protocol.Envelope{
		Type: protocol.TypeNewSession,
		TS:   time.Now().UTC(),
	})

	openModalConn(t, mgr, frames, rec, respPub, "c-barrier", []string{protocol.CapabilityInteractive})

	if got := fake.startCount(); got != 1 {
		t.Errorf("startCalls = %d, want 1 (attempted despite error)", got)
	}
}

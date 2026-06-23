package relay

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// --- #707 inbound interrupt → Esc routing fixtures ---

// fakeInterrupter is a relay-side test double for Interrupter: it counts SendEsc
// calls and returns an injectable error. The mutex guards the cross-goroutine
// access (the Run goroutine writes via SendEsc, the test goroutine reads via
// escCount), mirroring fakeModalResolver.
type fakeInterrupter struct {
	mu       sync.Mutex
	escCalls int
	err      error
}

func (f *fakeInterrupter) SendEsc() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.escCalls++
	return f.err
}

func (f *fakeInterrupter) escCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.escCalls
}

// TestV2Session_Interrupt_RoutesEscByCapability drives an inbound `interrupt`
// frame through the real Frames/Run loop and asserts the manager routes exactly
// one Esc for an interactive conn and zero for a non-interactive one — the new
// inbound capability gate (AC #2, AC #4). After the interrupt frame, a second
// conn is opened as a barrier: because Frames is a single FIFO channel drained by
// the one Run goroutine, the interrupt (enqueued first) is fully handled before
// the barrier conn opens, so escCount is final regardless of the gate's verdict.
func TestV2Session_Interrupt_RoutesEscByCapability(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		caps    []string
		wantEsc int
	}{
		{"interactive routes one Esc", []string{protocol.CapabilityInteractive}, 1},
		{"non-interactive is inert", nil, 0},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			respPriv, respPub := genV2Keypair(t)
			fake := &fakeInterrupter{}
			frames := make(chan protocol.RoutingEnvelope, 8)
			rec := &v2Recorder{}
			mgr, stop := startManager(t, V2SessionConfig{
				Frames:      frames,
				Outbound:    rec.outbound,
				StaticPriv:  respPriv,
				Devices:     v2PairedRegistry(t, v2TestToken),
				ServerID:    v2TestServerID,
				Logger:      silentLogger(),
				Interrupter: fake,
			})
			t.Cleanup(stop)

			send, _ := openModalConn(t, mgr, frames, rec, respPub, "c-int", tc.caps)
			frames <- sealAppFrameConn(t, send, "c-int", protocol.Envelope{
				Type: protocol.TypeInterrupt,
				TS:   time.Now().UTC(),
			})

			// Barrier: the interrupt is enqueued before this conn's noise_init, so
			// once the barrier conn is open the interrupt has been handled.
			openModalConn(t, mgr, frames, rec, respPub, "c-barrier", []string{protocol.CapabilityInteractive})

			if got := fake.escCount(); got != tc.wantEsc {
				t.Errorf("escCalls = %d, want %d", got, tc.wantEsc)
			}
		})
	}
}

// TestV2Session_Interrupt_NilInterrupterInert proves a nil Interrupter (foreground
// / pre-wire) makes an interactive interrupt inert: no Esc, no panic, the manager
// keeps serving. If handleInterrupt mishandled the nil seam, the Run goroutine
// would be dead and the barrier conn would never open (waitConnOpen would fail).
func TestV2Session_Interrupt_NilInterrupterInert(t *testing.T) {
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
		// Interrupter intentionally nil.
	})
	t.Cleanup(stop)

	send, _ := openModalConn(t, mgr, frames, rec, respPub, "c-int", []string{protocol.CapabilityInteractive})
	frames <- sealAppFrameConn(t, send, "c-int", protocol.Envelope{
		Type: protocol.TypeInterrupt,
		TS:   time.Now().UTC(),
	})

	// Barrier: opening succeeds only if Run processed the interrupt without
	// crashing or hanging on the nil seam.
	openModalConn(t, mgr, frames, rec, respPub, "c-barrier", []string{protocol.CapabilityInteractive})
}

// TestV2Session_Interrupt_SendEscErrorTolerated proves a SendEsc error (no live
// session / mid-teardown) is best-effort: the keystroke still counts as one
// attempted call, and the manager neither crashes nor closes the conn (the
// barrier conn opens afterward).
func TestV2Session_Interrupt_SendEscErrorTolerated(t *testing.T) {
	t.Parallel()

	respPriv, respPub := genV2Keypair(t)
	fake := &fakeInterrupter{err: errors.New("no live session")}
	frames := make(chan protocol.RoutingEnvelope, 8)
	rec := &v2Recorder{}
	mgr, stop := startManager(t, V2SessionConfig{
		Frames:      frames,
		Outbound:    rec.outbound,
		StaticPriv:  respPriv,
		Devices:     v2PairedRegistry(t, v2TestToken),
		ServerID:    v2TestServerID,
		Logger:      silentLogger(),
		Interrupter: fake,
	})
	t.Cleanup(stop)

	send, _ := openModalConn(t, mgr, frames, rec, respPub, "c-int", []string{protocol.CapabilityInteractive})
	frames <- sealAppFrameConn(t, send, "c-int", protocol.Envelope{
		Type: protocol.TypeInterrupt,
		TS:   time.Now().UTC(),
	})

	openModalConn(t, mgr, frames, rec, respPub, "c-barrier", []string{protocol.CapabilityInteractive})

	if got := fake.escCount(); got != 1 {
		t.Errorf("escCalls = %d, want 1 (attempted despite error)", got)
	}
}

package relay

import (
	"errors"
	"strings"
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
//
// Since #1192 it also asserts the non-interactive arm records which arm it took:
// the barrier is what makes the interactive case's NEGATIVE assertion sound —
// the interrupt is provably handled before the buffer is read, so an absent
// record is a real absence and not a race.
func TestV2Session_Interrupt_RoutesEscByCapability(t *testing.T) {
	t.Parallel()

	// nonInteractiveEvent is asserted by name, never by the v2.interrupt. prefix:
	// #1193 adds a success-path record to the same family, and a prefix-wide
	// absence assertion here would go red the moment it lands.
	const nonInteractiveEvent = "event=v2.interrupt.non_interactive"

	cases := []struct {
		name       string
		caps       []string
		wantEsc    int
		wantRecord bool
	}{
		{"interactive routes one Esc", []string{protocol.CapabilityInteractive}, 1, false},
		{"non-interactive is inert", nil, 0, true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			respPriv, respPub := genV2Keypair(t)
			fake := &fakeInterrupter{}
			frames := make(chan protocol.RoutingEnvelope, 8)
			rec := &v2Recorder{}
			logger, logBuf := bufferLogger()
			mgr, stop := startManager(t, V2SessionConfig{
				Frames:      frames,
				Outbound:    rec.outbound,
				StaticPriv:  respPriv,
				Devices:     v2PairedRegistry(t, v2TestToken),
				ServerID:    v2TestServerID,
				Logger:      logger,
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

			if !tc.wantRecord {
				// Non-vacuity guard: an emission placed ABOVE the !s.interactive
				// check would still satisfy the positive case below.
				if strings.Contains(logBuf.String(), nonInteractiveEvent) {
					t.Errorf("interactive conn emitted %s; that record belongs to the non-interactive arm only\nlog:\n%s",
						nonInteractiveEvent, logBuf.String())
				}
				return
			}
			waitForLogContains(t, logBuf, nonInteractiveEvent)
			line := findLogLine(t, logBuf, nonInteractiveEvent)
			if !strings.Contains(line, "conn_id=c-int") {
				t.Errorf("record does not identify the conn: %s", line)
			}
			// AC3 log hygiene, asserted on THIS line: the arm emits with the whole
			// *V2Session in scope, so neither the identity-bearing peer static key
			// (v2session.go SECURITY comment) nor the matched device snapshot may
			// appear. Other records in this buffer (the handshake) legitimately
			// mention the device, hence the per-line assertion.
			for _, forbidden := range []string{"peer", "static", "key", "device", v2TestDevName} {
				if strings.Contains(strings.ToLower(line), forbidden) {
					t.Errorf("record leaks %q — identifiers and enumerated outcomes only: %s", forbidden, line)
				}
			}
		})
	}
}

// findLogLine returns the one log line containing substr. slog's TextHandler
// emits a single line per record, so this is what lets a hygiene assertion apply
// to one record rather than to the whole buffer.
func findLogLine(t *testing.T, buf *syncLogBuffer, substr string) string {
	t.Helper()
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.Contains(line, substr) {
			return line
		}
	}
	t.Fatalf("no log line containing %q; got:\n%s", substr, buf.String())
	return ""
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

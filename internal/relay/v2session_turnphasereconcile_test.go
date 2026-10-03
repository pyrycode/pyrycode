package relay

import (
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// --- #2712 connect-time turn-phase reconcile (the seventh Mode B instance) ---
//
// The marshal arm and the non-ctx push arm are unreachable by fixture for the
// reasons v2session_rosterreconcile_test.go's header gives; the ordering against
// replayMissed is argued in the plan and pinned on the producer side by
// TestTurnPhaseSnapshot_PublishedBeforeFanOut in cmd/pyry.

// reconciledTurnStates decrypts every noise_msg addressed to connID and returns
// each turn_state keyed by conversation_id. The reconcile is the only outbound
// app traffic in these tests, so any other Type is a bug, as is a non-nil
// EventID (AC3: the frame is never part of the replay ring) or a repeated
// conversation_id. Decrypt each conn's frames exactly once per test.
func reconciledTurnStates(t *testing.T, rec *v2Recorder, connID string, recv *noise.CipherState) map[string]protocol.TurnStatePayload {
	t.Helper()
	out := make(map[string]protocol.TurnStatePayload)
	for _, env := range noiseMsgsForConn(t, rec, connID) {
		inner := decryptAppFrame(t, env, recv)
		if inner.Type != protocol.TypeTurnState {
			t.Fatalf("conn %q: noise_msg Type = %q, want %q", connID, inner.Type, protocol.TypeTurnState)
		}
		if inner.EventID != nil {
			t.Errorf("conn %q: reconciled turn_state carries event_id %d, want none", connID, *inner.EventID)
		}
		var p protocol.TurnStatePayload
		if err := json.Unmarshal(inner.Payload, &p); err != nil {
			t.Fatalf("conn %q: decode turn_state payload: %v", connID, err)
		}
		if _, dup := out[p.ConversationID]; dup {
			t.Errorf("conn %q: conversation_id %q re-sent more than once", connID, p.ConversationID)
		}
		out[p.ConversationID] = p
	}
	return out
}

// TestV2Session_TurnPhaseReconcile_Delivery sends exactly the seam's payloads to
// a freshly interactive-open conn, unchanged and without an event_id (AC1, AC3).
func TestV2Session_TurnPhaseReconcile_Delivery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		payloads []protocol.TurnStatePayload
	}{
		{"thinking", []protocol.TurnStatePayload{{ConversationID: "conv-phase-1", State: "thinking"}}},
		{"responding", []protocol.TurnStatePayload{{ConversationID: "conv-phase-1", State: "responding"}}},
		{"two conversations", []protocol.TurnStatePayload{
			{ConversationID: "conv-phase-1", State: "thinking"},
			{ConversationID: "conv-phase-2", State: "responding"},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			const connA = "c-v2-A"
			respPriv, respPub := genV2Keypair(t)
			reg := v2PairedRegistry(t, v2TestToken)
			frames := make(chan protocol.RoutingEnvelope, 8)
			rec := &v2Recorder{}
			mgr, stop := startManager(t, V2SessionConfig{
				Frames:            frames,
				Outbound:          rec.outbound,
				StaticPriv:        respPriv,
				Devices:           reg,
				ServerID:          v2TestServerID,
				Logger:            silentLogger(),
				RunningTurnPhases: func() []protocol.TurnStatePayload { return tt.payloads },
			})
			t.Cleanup(stop)

			_, aRecv := openModalConn(t, mgr, frames, rec, respPub, connA, []string{protocol.CapabilityInteractive})
			waitForEnvelopes(t, rec, 1+len(tt.payloads))

			got := reconciledTurnStates(t, rec, connA, aRecv)
			if len(got) != len(tt.payloads) {
				t.Fatalf("conn %q: got %d turn_state, want %d", connA, len(got), len(tt.payloads))
			}
			for _, want := range tt.payloads {
				if got[want.ConversationID] != want {
					t.Errorf("conn %q: conv %q = %+v, want %+v", connA, want.ConversationID, got[want.ConversationID], want)
				}
			}
		})
	}
}

// TestV2Session_TurnPhaseReconcile_UnicastOnlyOpeningConn: with A already open,
// opening B sends the phase to B only. It also pins the seam as a pure read —
// called once per open, the same set both times.
func TestV2Session_TurnPhaseReconcile_UnicastOnlyOpeningConn(t *testing.T) {
	t.Parallel()

	const (
		connA  = "c-v2-A"
		connB  = "c-v2-B"
		convID = "conv-phase-unicast"
	)
	respPriv, respPub := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)
	frames := make(chan protocol.RoutingEnvelope, 8)
	rec := &v2Recorder{}
	mgr, stop := startManager(t, V2SessionConfig{
		Frames:     frames,
		Outbound:   rec.outbound,
		StaticPriv: respPriv,
		Devices:    reg,
		ServerID:   v2TestServerID,
		Logger:     silentLogger(),
		RunningTurnPhases: func() []protocol.TurnStatePayload {
			return []protocol.TurnStatePayload{{ConversationID: convID, State: "thinking"}}
		},
	})
	t.Cleanup(stop)

	_, aRecv := openModalConn(t, mgr, frames, rec, respPub, connA, []string{protocol.CapabilityInteractive})
	_, bRecv := openModalConn(t, mgr, frames, rec, respPub, connB, []string{protocol.CapabilityInteractive})

	// resp(A) + phase(A) + resp(B) + phase(B); a fan-out on B's open would add a
	// second phase to A, caught by the count and the per-conn dup check.
	waitForEnvelopes(t, rec, 4)

	if got := reconciledTurnStates(t, rec, connA, aRecv); len(got) != 1 {
		t.Errorf("conn %q (opened first): got %d turn_state, want exactly 1", connA, len(got))
	}
	if got := reconciledTurnStates(t, rec, connB, bRecv); len(got) != 1 {
		t.Errorf("conn %q (opened second): got %d turn_state, want 1", connB, len(got))
	}
}

// TestV2Session_TurnPhaseReconcile_NoFrame: no frame, no log record and no error
// for an unwired seam, for no running turn (AC2's idle arm), and for a conn that
// did not negotiate the interactive capability (AC1's second sentence).
func TestV2Session_TurnPhaseReconcile_NoFrame(t *testing.T) {
	t.Parallel()

	const (
		connA      = "c-v2-A"
		connCtl    = "c-v2-CTL"
		convID     = "conv-phase-noframe"
		eventScope = "v2.turnstate."
	)
	running := func() []protocol.TurnStatePayload {
		return []protocol.TurnStatePayload{{ConversationID: convID, State: "responding"}}
	}

	tests := []struct {
		name string
		seam func() []protocol.TurnStatePayload
		caps []string
		// controlConn opens an interactive conn after A so the capability row's
		// zero is measured against a seam that does produce a frame.
		controlConn bool
	}{
		{name: "nil seam", seam: nil, caps: []string{protocol.CapabilityInteractive}},
		{name: "no turn running", seam: func() []protocol.TurnStatePayload { return nil }, caps: []string{protocol.CapabilityInteractive}},
		{name: "capability not negotiated", seam: running, caps: nil, controlConn: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			logBuf := &lockedBuffer{}
			logger := slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

			respPriv, respPub := genV2Keypair(t)
			reg := v2PairedRegistry(t, v2TestToken)
			frames := make(chan protocol.RoutingEnvelope, 8)
			rec := &v2Recorder{}
			mgr, stop := startManager(t, V2SessionConfig{
				Frames:            frames,
				Outbound:          rec.outbound,
				StaticPriv:        respPriv,
				Devices:           reg,
				ServerID:          v2TestServerID,
				Logger:            logger,
				RunningTurnPhases: tt.seam,
			})
			t.Cleanup(stop)

			openModalConn(t, mgr, frames, rec, respPub, connA, tt.caps)

			wantEnvelopes := 1 // resp(A)
			var ctlRecv *noise.CipherState
			if tt.controlConn {
				_, ctlRecv = openModalConn(t, mgr, frames, rec, respPub, connCtl, []string{protocol.CapabilityInteractive})
				wantEnvelopes = 3 // + resp(CTL) + phase(CTL)
			}
			waitForEnvelopes(t, rec, wantEnvelopes)

			if tt.controlConn {
				if got := reconciledTurnStates(t, rec, connCtl, ctlRecv); len(got) != 1 {
					t.Fatalf("interactive control conn %q: got %d turn_state, want 1", connCtl, len(got))
				}
			}
			if msgs := noiseMsgsForConn(t, rec, connA); len(msgs) != 0 {
				t.Errorf("conn %q: got %d noise_msg, want 0", connA, len(msgs))
			}
			logs := logBuf.String()
			if !strings.Contains(logs, "handshake") {
				t.Fatalf("log capture appears inert (no handshake line); logs = %q", logs)
			}
			if strings.Contains(logs, eventScope) {
				t.Errorf("conn %q: reconcile wrote a log record on a no-frame path; logs = %q", connA, logs)
			}
			waitConnOpen(t, mgr, connA)
		})
	}
}

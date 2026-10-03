package relay

import (
	"context"
	"encoding/json"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// This file holds the connect-time turn-phase reconcile (#2712) — the seventh
// Mode B instance after reconcileModals (#877), reconcileQueues (#878),
// reconcileModelLists (#1863), reconcileQuestions (#1979),
// reconcileSlashCommandLists (#2006) and reconcileBackgroundTaskRosters (#2078).
// Its own file for the reason the twins give for theirs: the test files are
// one-per-reconcile.

// reconcileTurnPhases unicasts the current phase of every running turn to a
// freshly interactive-open conn (#2712), so a client that left mid-turn shows
// thinking or responding straight away instead of idle until the turn ends. It is
// structurally the roster twin with the payload type substituted; the
// RunningTurnPhases seam doc states why the frame needs a reconcile at all and
// why this one runs after replayMissed.
//
// Run-goroutine only (called from handleNoiseInit's success tail), so
// s.interactive / s.connID are read lock-free under the package's single-owner
// invariant. It reaches no cmd/pyry emitter state directly: the seam is the
// producer's synchronised snapshot, and reading it changes nothing, so the
// emitter's transition de-duplication is untouched and its next live transition
// reaches every conn exactly as it would have without this send.
//
// It enqueues rather than seals, as the six twins do: Push only buffers, leaving
// drainOnce to consult Connected before sealing, so no Noise send-nonce is burned
// for a frame that cannot reach the phone (#874). The drain goes through
// forwardEnvelope, so withheldFromConn still withholds a Codex conversation's
// phase from a conn without multi_agent, exactly as it withholds the live copy.
//
// EventID left nil is load-bearing: it keeps the frame out of the turn-event
// replay ring and the durable history (this path appends to neither), and makes
// forwardEnvelope's last_event_id dedup inert for it. A re-sent turn_state is
// harmless — the client matches on conversation_id and replaces that
// conversation's phase in place.
//
// SECURITY: the payload carries a server-minted conversation_id and a phase from
// turnbridge's closed vocabulary; no claude-authored text. The logging posture is
// still the twins' — content-free discriminants only, err never echoed on the
// marshal arm, nothing logged on success — because a connect-time reconcile fires
// on every handshake.
func (m *V2SessionManager) reconcileTurnPhases(ctx context.Context, s *V2Session) {
	// Capability gate + the unwired/foreground opt-out, as in the six twins.
	if !s.interactive || m.cfg.RunningTurnPhases == nil {
		return
	}
	running := m.cfg.RunningTurnPhases()
	if len(running) == 0 {
		return // no turn running ⇒ nothing sent; the client reads idle.
	}
	ts := time.Now().UTC()
	for _, p := range running {
		payload, err := json.Marshal(p)
		if err != nil {
			// TurnStatePayload is two strings, so marshal cannot fail. Defensive
			// only: never echo err or the payload. Skip this one, keep the rest.
			m.cfg.Logger.Warn("relay: v2 turn_state reconcile marshal failed",
				"event", "v2.turnstate.reconcile.marshal_err",
				"conn_id", s.connID,
				"conversation_id", p.ConversationID)
			continue
		}
		env := protocol.Envelope{
			ID:      1, // non-load-bearing; the client correlates on conversation_id.
			Type:    protocol.TypeTurnState,
			TS:      ts,
			Payload: payload,
			// EventID left nil: control state, never part of the replay ring.
		}
		if err := m.Push(ctx, s.connID, env); err != nil {
			// Push returns ctx.Err() or ErrConnNotFound only; the latter is
			// unreachable here (the queue was created earlier in this same
			// handleNoiseInit), hence Debug.
			m.cfg.Logger.Debug("relay: v2 turn_state reconcile push dropped",
				"event", "v2.turnstate.reconcile.push_err",
				"conn_id", s.connID,
				"err", err)
			if ctx.Err() != nil {
				return
			}
		}
	}
}

//go:build e2e_realclaude

package realclaude

// TestRealClaude_SendQueuedNowPlacement is the live gate for #2730: a message the
// operator queued behind a running turn and then pushed in with send_queued_now
// must appear on the wire where claude actually read it. Claude reads stdin
// input written mid-turn after the tool call that was running, so the operator's
// own `message` push has to sit after that call's tool_result and before the
// assistant text that follows it, not at the moment the daemon wrote stdin.
//
// It composes the running-turn seam (startStreamRunningTurnHarness, whose daemon
// runs with --dangerously-skip-permissions so no modal blocks the Bash call) with
// sealSendMessage, sealEnvelope and nextAttachReadEnvelope as the single in-order
// reader. The Bash command is the silent foreground loop runningTurnPrompt uses,
// not a bare `sleep`, because claude backgrounds a bare sleep and the turn would
// end before the send-now lands.
//
// Every envelope is recorded in arrival order in one log across all phases, and
// the assertions read positions in that log. No assertion pins claude's wording
// beyond the marker it was asked to include.

import (
	"strconv"
	"testing"
	"time"

	scenariohelpers "github.com/pyrycode/pyrycode/internal/e2e/internal/scenario"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func TestRealClaude_SendQueuedNowPlacement(t *testing.T) {
	runParallel(t)
	h, convID := startStreamRunningTurnHarness(t)
	sequence := uint64(1)
	wire := scenariohelpers.Wire{
		Send: func(kind string, payload any) uint64 {
			sequence++
			sealEnvelope(t, h.phone, h.initSend, protocol.Envelope{ID: sequence, Type: kind, TS: time.Now().UTC(), Payload: mustJSON(t, payload)})
			return sequence
		},
		Next: func(deadline time.Time) protocol.Envelope {
			env, _ := nextAttachReadEnvelope(t, h, deadline)
			return env
		},
	}
	scenariohelpers.SendNowPlacement(t, wire, convID, scenariohelpers.SendNowPrompt(strconv.FormatInt(time.Now().UnixNano(), 10)), perTurnReplyBudget, scenariohelpers.SendNowSettle)
}

// sendQueuedNow seals a send_queued_now control envelope for convID's queued
// entry id. The daemon sends no reply; the queue_state and the stream show the
// outcome. It mirrors the fake-claude suite's dequeue_message send.
func sendQueuedNow(t *testing.T, h *perConvHarness, convID string, id uint64) {
	t.Helper()
	sealEnvelope(t, h.phone, h.initSend, protocol.Envelope{
		ID:      4,
		Type:    protocol.TypeSendQueuedNow,
		TS:      time.Now().UTC(),
		Payload: mustJSON(t, protocol.SendQueuedNowPayload{ConversationID: convID, QueuedMsgID: id}),
	})
}

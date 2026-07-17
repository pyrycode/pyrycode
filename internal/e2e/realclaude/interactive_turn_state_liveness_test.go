//go:build e2e_realclaude

package realclaude

// The #1062 deliverable: a real-claude PER-CONVERSATION turn_state liveness gate.
// Where the #997 per-conversation reply-stream gate
// (interactive_per_conversation_liveness_test.go) proves assistant_delta streams,
// this proves the daemon ALSO fans turn_state (thinking/responding) while the turn
// runs — the coarse lifecycle signal the remote gates its interrupt affordance and
// queued-send window on, and the absence of which produces a false "the turn seems
// to have stalled…" warning after a healthy reply.
//
// Scope — a LIVENESS gate, not the deterministic #1062 oracle. #1062's root cause
// (a shared long-lived emitter carrying an orphaned turn's currentState across a
// follow-active switch, de-duping the new conversation's opening responding away)
// is a timing race: it fires only when the prior conversation's turn was torn down
// mid-turn without a delivered TurnEnd at the instant the cursor moved. This fast,
// co-located harness rarely reproduces that precondition — real claude replies in
// seconds and the switch lands cleanly — so the DETERMINISTIC RED->GREEN proof
// lives in the hermetic emitter unit test (cmd/pyry/interactive_turn_v2_switch_test.go,
// TestInteractiveTurnV2_Switch*), which forces the open-prior-turn carryover directly.
// This gate's value is standing real-claude coverage that turn_state genuinely fans
// on the per-conversation path (it does NOT, at all, on the pre-#1062 code for a
// switched conversation), per the real-claude-e2e-in-a-pre-ship-gate rule.
//
// Placement alone wires this into `make e2e-realclaude` / `make preship` via the
// e2e_realclaude build tag and the package glob — no Makefile change, mirroring
// #854/#997/#1031. It reuses the #997 per-conversation harness in this package
// verbatim (startPerConversationHarness, createConversationViaPhone,
// sealSendMessage, drainForReply) and adds one drain helper, drainForTurnState,
// the turn_state analogue of drainForAssistantReply.

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// TestInteractivePerConversationTurnStateLiveness creates a per-conversation
// conversation over the wire, sends one message, and asserts the daemon fans a
// thinking/responding turn_state for THAT conversation while the turn runs — then
// (AC2) confirms the reply path did not regress by draining an assistant_delta for
// the same conversation. A timeout with no turn_state is the liveness RED this
// rung captures.
func TestInteractivePerConversationTurnStateLiveness(t *testing.T) {
	h := startPerConversationHarness(t)
	nonce := time.Now().UnixNano()

	convID := createConversationViaPhone(t, h.phone, h.initSend, h.initRecv, 2, nil)
	sealSendMessage(t, h.phone, h.initSend, 3, convID, "m-1",
		fmt.Sprintf("Reply with a single short word. run=%d", nonce))

	// AC1/AC5: a turn_state (thinking|responding) fans for this conversation while
	// the turn runs.
	drainForTurnState(t, h.phone, h.initRecv, convID, perTurnReplyBudget)

	// AC2: the reply-stream path is unaffected — a non-empty assistant_delta for the
	// same conversation still arrives. Same in-order drain discipline; the receive
	// nonce is already advanced past the turn_state above.
	drainForAssistantReply(t, h.phone, h.initRecv, convID, 1, perTurnReplyBudget)
}

// drainForTurnState reads binary→phone noise_msg frames in receive order — the
// receive nonce is sequential, so every frame MUST be decrypted in order to keep
// the CipherState in sync — and returns once it observes a turn_state envelope for
// convID whose state is thinking or responding (the running-turn signal the remote
// gates its interrupt affordance on). Interleaved acks / assistant_delta / tool_use
// envelopes are decrypted and skipped; a non-noise_msg control frame is skipped
// WITHOUT decrypting so it does not advance the nonce. The turn_state analogue of
// drainForAssistantReply; a timeout with no such state is the liveness RED.
func drainForTurnState(t *testing.T, phone *fakephone.Client, cs *noise.CipherState, convID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatalf("no thinking/responding turn_state for %q within %s — the per-conversation turn stream never fanned turn_state (#1062)",
				convID, timeout)
		}
		raw, err := phone.ReceiveBytes(remaining)
		if err != nil {
			if errors.Is(err, fakephone.ErrReceiveTimeout) {
				t.Fatalf("no thinking/responding turn_state for %q within %s — the per-conversation turn stream never fanned turn_state (#1062)",
					convID, timeout)
			}
			t.Fatalf("phone receive (awaiting turn_state): %v", err)
		}
		var inner protocol.InnerFrameV2
		if err := json.Unmarshal(raw, &inner); err != nil {
			t.Fatalf("decode inner frame: %v", err)
		}
		if inner.Type != protocol.TypeNoiseMsg {
			continue // non-noise_msg control frame does not advance the receive nonce
		}
		cipher, err := base64.StdEncoding.DecodeString(inner.Data)
		if err != nil {
			t.Fatalf("decode inner data: %v", err)
		}
		plain, err := cs.Decrypt(cipher)
		if err != nil {
			t.Fatalf("phone decrypt (receive-nonce desync?): %v", err)
		}
		var env protocol.Envelope
		if err := json.Unmarshal(plain, &env); err != nil {
			t.Fatalf("decode envelope: %v", err)
		}
		if env.Type != protocol.TypeTurnState {
			continue // ack, assistant_delta, tool_use, turn_end, … — keep draining in order
		}
		var p protocol.TurnStatePayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			t.Fatalf("decode turn_state payload: %v", err)
		}
		if p.ConversationID == convID && (p.State == "thinking" || p.State == "responding") {
			t.Logf("turn_state %q for %q", p.State, convID)
			return
		}
	}
}

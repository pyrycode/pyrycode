//go:build e2e_realclaude

package realclaude

// TestInteractiveStreamInterruptStopsRunningTurn is the #1176 deliverable (part of
// #1083, T9): the real-claude counterpart of the fakeclaude interrupt proof
// (TestRelayV2_StreamInterruptStopsRunningTurn, #1136). It stands up the interactive
// daemon under the production stream-json interactive runner
// (interactive_runner: "stream-json") against a live claude, puts a GENUINELY-running
// live turn in flight, sends a phone-originated interrupt, and asserts the turn stops
// as cancelled — not run-to-completion — with the session still usable afterwards.
//
// Why this gate exists: the production interrupt path is shipped and unit-tested
// independently — the streamsup.Runner.Interrupt() primitive (#1120), per-conversation
// routing (activeInterrupter → resolveBoundRunner, #1121), and the parser's
// error_during_execution → cancelled classification (#1120, `maxTaskRosterEntries`) — but
// whether an interrupt envelope actually stops a REAL claude turn mid-stream, with the
// parser classifying real claude's interrupt-terminated result as cancelled, was
// unproven live. This is the recurring fake-green/real-red risk (#949) applied to the
// interrupt path: the fakeclaude analog scripts a held turn via an env-toggled child;
// real claude cannot be scripted, so this composes the #1172 running-turn trigger (a
// bounded silent foreground Bash loop that holds turn_state{responding} for ~40s) with
// the shipped interrupt primitive.
//
// Composition — reuses the #1172 seam verbatim (startStreamRunningTurnHarness +
// driveRunningTurn + drainForResponding, whose header flags them as THE reusable seam
// for this ticket) plus drainForCompletedTurn (#1153) for the health turn; adds ONE new
// drain helper (drainForCancelledTurnEnd) and one test fn. No production code changes.
//
// Wire sequence (single phone reader; h.initRecv threads continuously across all three
// drains, so each resumes exactly where the prior left off and any leftover ack /
// turn_state{idle} from the cancelled turn is decrypted-and-skipped, harmless):
//
//	send_message(convID, reqID 2, 40s-loop prompt)
//	  → real claude issues a Bash tool call → turn_state{responding}    ── drainForResponding (AC1)
//	  → [Bash loop runs silently ~40s, no new turn_state]
//	interrupt(reqID 3, no payload) → active cursor = convID (stamped by the send above) →
//	  resolveBoundRunner(convID) → the bootstrap runner running the live turn →
//	  streamsup.Runner.Interrupt() → control_request → real claude cancels the Bash tool →
//	  result{subtype: error_during_execution} → parser: TurnEnd{cancelled} →
//	  turn_end{StopReason:"cancelled", convID}                          ── drainForCancelledTurnEnd (AC2/AC3)
//	  → turn_state{idle}  (drained-and-skipped by the next drain, before its M1)
//	send_message(convID, reqID 4, "single short word")
//	  → responding → assistant_delta(non-empty) → turn_end{end_turn} → turn_state{idle}
//	                                                                    ── drainForCompletedTurn (AC4)
//
// Routing scope (why bootstrap-bound, not a minted target): the fakeclaude analog MINTS
// a separate conversation so its interrupt-target differs from bootstrap, making its M2
// a live cross-conversation isolation proof. This spec deliberately does NOT mint — it
// composes the #1172 harness, whose driving conversation (runningTurnConvID) is bound to
// the bootstrap pool id, so the running turn executes on the bootstrap runner and the
// payload-less interrupt routes there via the active cursor. AC2 requires the interrupt
// to reach the RUNNING TURN'S bound runner — which it does — not to prove isolation.
// Cross-conversation routing isolation is unit-owned deterministically by #1121
// (interrupt_routing_test.go) — the belt; this e2e is the different-fabric suspenders
// confirming the wired path runs live against real claude. A documented scope choice.
//
// Vacuous-pass guard (mirrors the fakeclaude analog
// `TestRelayV2_StreamInterruptStopsRunningTurn`): real claude WILL end the turn
// naturally at ~40s if the interrupt no-ops, so
// "the turn just ended" is not enough. drainForCancelledTurnEnd asserts the FIRST
// turn_end for convID carries StopReason == "cancelled" (a spontaneous natural end
// reports "end_turn"); the responding-drained → interrupt-sent → turn_end-drained
// sequencing enforces the interrupt-before / stop-after ordering. NO content/echo
// assertion anywhere — real claude's output is non-deterministic; assertions are on
// turn_state/turn_end transitions and the cancelled stop reason only, exactly as
// #1172/#1153/#1136 established.
//
// Like #854/#997/#1030/#1153/#1154/#1172 this is a standing real-claude liveness gate in
// preship, not a deterministic RED/GREEN oracle; placement under the e2e_realclaude build
// tag wires it into `make e2e-realclaude` via the package glob (no Makefile change).
// Absent a Claude login the harness SKIPs cleanly — the ticket carries needs-real-claude
// and is operator-gated (AC5).

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/scenario"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// TestInteractiveStreamInterruptStopsRunningTurn drives one real claude turn into
// responding via the #1172 bounded-Bash-loop trigger, interrupts it mid-stream, and
// proves the turn stops as cancelled while the session stays usable for the next turn.
func TestInteractiveStreamInterruptStopsRunningTurn(t *testing.T) {
	runParallel(t)
	h, convID := startStreamRunningTurnHarness(t)
	scenario.InterruptTurn(t, checkpointHarnessWire(t, h), scenario.InterruptConfig{ConversationID: convID, Prompt: runningTurnPrompt(int((runningTurnHold + runningTurnLoopSlack).Seconds()), time.Now().UnixNano()), HealthPrompt: fmt.Sprintf("Reply with a single short word. run=%d", time.Now().UnixNano()), Budget: perTurnReplyBudget})
}

// TestInteractiveStreamInterruptStopsInitialThinkingTurn waits for the server's
// thinking phase before any assistant text or tool frame, then sends an interrupt
// naming that conversation and requires the first terminal event to be cancelled.
func TestInteractiveStreamInterruptStopsInitialThinkingTurn(t *testing.T) {
	runParallel(t)
	h, convID := startStreamRunningTurnHarness(t)
	scenario.InterruptTurn(t, checkpointHarnessWire(t, h), scenario.InterruptConfig{ConversationID: convID, Prompt: scenario.InitialThinkingPrompt(fmt.Sprint(time.Now().UnixNano())), InitialThinking: true, Budget: perTurnReplyBudget})
}

func checkpointHarnessWire(t *testing.T, h *perConvHarness) scenario.Wire {
	sequence := uint64(1)
	return scenario.ObserveSource(t, h.home, scenario.Wire{Next: scenario.PollingReceive(t, h.phone, h.initRecv), Send: func(kind string, payload any) uint64 {
		sequence++
		var raw json.RawMessage
		if payload != nil {
			raw = mustJSON(t, payload)
		}
		sealEnvelope(t, h.phone, h.initSend, protocol.Envelope{ID: sequence, Type: kind, TS: time.Now().UTC(), Payload: raw})
		return sequence
	}})
}

// driveInitialThinkingTurn asks for a deliberately multi-step, tool-free proof.
// Unlike the running-turn trigger, it does not ask Claude to race from thinking
// into a tool call; the only acceptable next observable after the interrupt is
// the cancelled terminal event.
func driveInitialThinkingTurn(t *testing.T, h *perConvHarness, reqID uint64, convID string) {
	t.Helper()
	nonce := time.Now().UnixNano()
	sealSendMessage(t, h.phone, h.initSend, reqID, convID, fmt.Sprintf("m-%d", reqID),
		fmt.Sprintf("Do not use tools. Carefully prove or disprove this claim, checking the base case, induction step, and at least two possible counterexamples before writing any final answer: every positive integer can be written as a sum of distinct Fibonacci numbers with no consecutive Fibonacci numbers used. Take time to reason through all cases. run=%d", nonce))
}

// drainForInitialThinking returns only when thinking is published for convID. A
// progress, response, tool, or terminal frame for that conversation first makes
// the proof vacuous and therefore fails immediately.
func drainForInitialThinking(t *testing.T, phone *fakephone.Client, cs *noise.CipherState, convID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatalf("no initial turn_state{thinking} for %q within %s", convID, timeout)
		}
		raw, err := phone.ReceiveBytes(remaining)
		if err != nil {
			if errors.Is(err, fakephone.ErrReceiveTimeout) {
				continue
			}
			t.Fatalf("phone receive (awaiting initial thinking): %v", err)
		}
		var inner protocol.InnerFrameV2
		if err := json.Unmarshal(raw, &inner); err != nil {
			t.Fatalf("decode inner frame: %v", err)
		}
		if inner.Type != protocol.TypeNoiseMsg {
			continue
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
		if env.Type == protocol.TypeTurnState {
			var state protocol.TurnStatePayload
			if err := json.Unmarshal(env.Payload, &state); err != nil {
				t.Fatalf("decode turn_state: %v", err)
			}
			if state.ConversationID != convID {
				continue
			}
			if state.State == "thinking" {
				t.Logf("initial turn_state{thinking} for %q observed", convID)
				return
			}
			t.Fatalf("turn_state{%s} for %q arrived before initial thinking", state.State, convID)
		}
		switch env.Type {
		case protocol.TypeThinkingProgress, protocol.TypeAssistantDelta, protocol.TypeToolUse, protocol.TypeTurnEnd:
			var addressed struct {
				ConversationID string `json:"conversation_id"`
			}
			if err := json.Unmarshal(env.Payload, &addressed); err != nil {
				t.Fatalf("decode %s conversation: %v", env.Type, err)
			}
			if addressed.ConversationID == convID {
				t.Fatalf("%s for %q arrived before initial turn_state{thinking}", env.Type, convID)
			}
		}
	}
}

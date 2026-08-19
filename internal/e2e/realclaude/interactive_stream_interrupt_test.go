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
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// TestInteractiveStreamInterruptStopsRunningTurn drives one real claude turn into
// responding via the #1172 bounded-Bash-loop trigger, interrupts it mid-stream, and
// proves the turn stops as cancelled while the session stays usable for the next turn.
func TestInteractiveStreamInterruptStopsRunningTurn(t *testing.T) {
	h, convID := startStreamRunningTurnHarness(t)

	// AC1: put a genuinely-running live turn in flight. driveRunningTurn sends id 2
	// (first post-handshake message) with the 40s foreground-Bash-loop prompt;
	// drainForResponding blocks until turn_state{responding} for convID — and by its
	// in-order-drain construction NO terminal turn_state{idle} has been observed yet,
	// so the turn is genuinely mid-stream. Generous budget: cold claude spawn + model
	// load, not fakeclaude milliseconds. (Return value t0 is unused — the interrupt is
	// fired immediately, deep inside the 40s loop, so no timing window is measured.)
	driveRunningTurn(t, h, 2, convID, runningTurnHold)
	drainForResponding(t, h.phone, h.initRecv, convID, perTurnReplyBudget)

	// AC2: fire the payload-less interrupt IMMEDIATELY (no intervening wait). A
	// payload-less interrupt routes to the ACTIVE conversation — stamped convID by the
	// send_message above → activeInterrupter.SendEsc() → resolveBoundRunner(convID) →
	// the bootstrap runner running the live turn → streamsup.Runner.Interrupt() →
	// control_request to the real claude child.
	sealEnvelope(t, h.phone, h.initSend, protocol.Envelope{
		ID:   3,
		Type: protocol.TypeInterrupt,
		TS:   time.Now().UTC(),
	})

	// AC3: the running turn terminates as cancelled, not run-to-completion. Fails loud
	// on the first non-cancelled turn_end (the spontaneous-end guard).
	drainForCancelledTurnEnd(t, h.phone, h.initRecv, convID, perTurnReplyBudget)

	// AC4: the session stays usable — a subsequent trivial turn on the same conversation
	// runs to a terminal turn_state{idle}. NOT driveRunningTurn (that would launch
	// another 40s loop); a single-short-word prompt with a per-run nonce, matching the
	// #1153 liveness shape. drainForCompletedTurn decrypts-and-skips the leftover
	// turn_state{idle} from the cancelled turn before its own M1.
	sealSendMessage(t, h.phone, h.initSend, 4, convID, "m-4",
		fmt.Sprintf("Reply with a single short word. run=%d", time.Now().UnixNano()))
	drainForCompletedTurn(t, h.phone, h.initRecv, convID, perTurnReplyBudget)
}

// drainForCancelledTurnEnd reads binary→phone noise_msg frames in receive order — the
// receive nonce is sequential, so every noise_msg MUST be decrypted in order to keep the
// CipherState in sync — and returns once it observes the FIRST turn_end for convID,
// asserting its StopReason is "cancelled". It skips non-noise_msg inner frames WITHOUT
// decrypting (they do not advance the nonce) and decrypts-and-skips every other envelope
// type (ack, turn_state, assistant_delta, …) in order — with ONE exception since #1500:
// an unrecognized_message is fatal, see the arm below. Same in-order decrypt discipline
// as drainForResponding, retargeted from turn_state{responding} to turn_end.
//
// The StopReason check is the vacuous-pass guard (mirrors the fakeclaude analog
// `TestRelayV2_StreamInterruptStopsRunningTurn`): the FIRST terminal event of the running
// turn must be cancelled, not a spontaneous natural end. A running-turn loop that
// completes naturally at ~40s reports "end_turn"; only the interrupt's
// error_during_execution path reports "cancelled". A non-cancelled first turn_end →
// t.Fatalf immediately (the interrupt did not cancel — the turn ran to completion). On
// the deadline → t.Fatalf naming the likely cause. Reusable-shaped, but private to this
// gate. Fail-loud only (this is a liveness gate, no recovery).
func drainForCancelledTurnEnd(t *testing.T, phone *fakephone.Client, cs *noise.CipherState, convID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatalf("no turn_end for %q within %s — the interrupt did not stop the running turn "+
				"(it never routed to the running turn's bound runner, or real claude never emitted "+
				"error_during_execution in response to the interrupt control_request)", convID, timeout)
		}
		raw, err := phone.ReceiveBytes(remaining)
		if err != nil {
			if errors.Is(err, fakephone.ErrReceiveTimeout) {
				continue // deadline reached — re-loop into the t.Fatalf above
			}
			t.Fatalf("phone receive (awaiting turn_end): %v", err)
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
		if env.Type == protocol.TypeUnrecognizedMessage {
			// THE STANDING ALARM ON THE INTERRUPT WINDOW (#1500), in the shape
			// drainForCompletedTurn's own has. Until this arm existed the window had
			// none: the `continue` below decrypts-and-skips every non-turn_end
			// envelope, and the zero-unrecognized alarm lives in drainForCompletedTurn,
			// which this spec only reaches on the HEALTH TURN afterwards — by which
			// point an interrupt-window frame is long gone. So this is the only place a
			// REAL claude's interrupt ack is observed, and it was red before #1500.
			//
			// No conversation filter, matching both existing alarms: a parser gap is a
			// property of the daemon, not of one conversation.
			//
			// Going red has TWO readings, and MessageType is the discriminator:
			//
			//  1. THE DAEMON BUG. An ack the daemon SOLICITED is reaching the phone.
			//     Interrupt here is a stdin control_request, and claude answers ~40ms
			//     later with a control_response on the same stdout the parser reads;
			//     streamsup.consumeLine's control_response arm consumes it content-free.
			//     A type of "control_response" means that arm regressed or never landed,
			//     and every interrupt is now firing a false parser-gap alarm at a phone.
			//
			//  2. A CLAUDE RELEASE EMITTING A GENUINELY NEW TYPE MID-INTERRUPT. Not a
			//     bug — this is the alarm working, and it is what the frame is FOR. Read
			//     the payload and decide whether the type deserves a mapping or a place
			//     on streamsup.ignoredLineTypes. Do NOT answer it by widening this drain
			//     back to a bare continue; that only restores the blind spot.
			//
			// What a GREEN run here does not prove: cmd/pyry's turnMarkFor classifies
			// Unrecognized as turnMarkNone, i.e. droppable at the fan-in, so green means
			// no ack frame ARRIVED, which under capacity pressure is weaker than no ack
			// frame was produced. Fine for a fail-loud sentinel; it is why the hermetic
			// tier pins the ack on the emitted line rather than on a downstream absence.
			var p protocol.UnrecognizedMessagePayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("decode unrecognized_message payload: %v", err)
			}
			t.Fatalf("an unrecognized_message reached the phone while draining the interrupt window: "+
				"site=%q type=%q truncated=%v\nraw: %s\n\n"+
				"Either the daemon is surfacing the interrupt ack it solicited for itself (type "+
				"control_response — streamsup.consumeLine's arm regressed), or claude started emitting a "+
				"genuinely new message type mid-interrupt, which is this frame working and wants a mapping "+
				"decision, not a wider drain. The type above is what tells those apart.",
				p.Site, p.MessageType, p.Truncated, p.Raw)
		}
		if env.Type != protocol.TypeTurnEnd {
			continue // ack, turn_state, assistant_delta, tool_use, … — keep draining in order
		}
		var p protocol.TurnEndPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			t.Fatalf("decode turn_end payload: %v", err)
		}
		if p.ConversationID != convID {
			continue // a turn_end for another conversation — keep draining
		}
		// The FIRST turn_end for the running conversation must be cancelled.
		if p.StopReason != "cancelled" {
			t.Fatalf("turn_end StopReason = %q for %q, want %q — the running turn ran to completion, the interrupt "+
				"did NOT cancel it (a spontaneous natural end reports \"end_turn\"; \"cancelled\" confirms real claude "+
				"took the error_during_execution path in response to the interrupt control_request)",
				p.StopReason, convID, "cancelled")
		}
		t.Logf("turn_end{StopReason:cancelled} for %q observed — the interrupt stopped the running turn (AC2/AC3)", convID)
		return
	}
}

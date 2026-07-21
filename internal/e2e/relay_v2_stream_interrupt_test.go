//go:build e2e

package e2e

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// TestRelayV2_StreamInterruptStopsRunningTurn is the LIVE, end-to-end proof that a
// phone-originated interrupt is honoured PER CONVERSATION under
// interactive_runner:"stream-json" (#1136). #1141 proved one send_message drains on
// the stream path; this proves the next behaviour on that path — an interrupt ends
// the running turn — with a fakeclaude that is genuinely mid-turn when the interrupt
// arrives.
//
// The production interrupt path is shipped and unit-tested: the streamsup primitive
// ((*streamsup.Runner).Interrupt() → a control_request line, #1120), the
// per-conversation routing (activeInterrupter → resolveBoundRunner → the active
// conversation's bound runner, #1121), and the parser's interrupt classification
// (error_during_execution → cancelled, #1120). This spec is the first time all of
// them run TOGETHER, live, over the stream toggle:
//
//	phone create_conversation → daemon mints session-B (stream RunnerFactory) →
//	  fakeclaude-B (interrupt mode)
//	phone send_message(convB,"…go") → sessionRouter.Route [stamps active = convB] →
//	  boundSession.WriteUserTurn → streamsup WriteTurn → {"type":"user",…} →
//	  fakeclaude-B echoes {"type":"assistant",…} (NO result — the turn stays in flight)
//	  → daemon Parser → TextChunk → drain gate (session-B == active ✓) → emitter →
//	  turn_state{responding} → assistant_delta{"…go"}                          ── M1 (AC1)
//	phone interrupt (no payload) → handleInterrupt → activeInterrupter.SendEsc() →
//	  active=convB → resolveBoundRunner(convB) → session-B.Runner() →
//	  streamRunner.Interrupt() → WriteInterrupt → {"type":"control_request",…} →
//	  fakeclaude-B stdin → fakeclaude-B: {"type":"result","subtype":"error_during_execution"}
//	  → daemon Parser → TurnEnd{Cancelled} → drain gate (session-B == active ✓) →
//	  emitter → turn_end{StopReason:"cancelled"} → turn_state{idle}            ── M2 (AC2/AC3)
//
// THE TARGET IS A *MINTED* CONVERSATION (its own, non-bootstrap runner). This is what
// makes the e2e exercise #1121's routing. Had the target been bootstrap-bound (as in
// #1141's send test), resolveBoundRunner would return the bootstrap runner —
// indistinguishable from the pre-#1121 bug (interrupt → bootstrap). Minting forces the
// interrupt to reach session-B's OWN child. The idle bootstrap session is the other
// conversation running concurrently under the same daemon, and it is provably not the
// target: under the pre-#1121 bug the interrupt would hit the (non-active) bootstrap →
// no observable turn_end → M2 times out. Correct routing → turn_end{cancelled, convB}.
// So M2 IS the live isolation proof (AC3). Runner-level cross-conversation isolation is
// unit-owned by #1121 (interrupt_routing_test.go), deterministically — the belt; the
// e2e is a different fabric confirming the wired target.
//
// VACUOUS-PASS GUARD (the stream analogue of the PTY sibling's, relay_v2_interrupt_test.go).
// The fake emits NO result on the user turn, so the ONLY source of a turn_end is its
// response to the interrupt control_request (structural causality: a turn_end ⟺ the
// interrupt was received and honoured). Asserting StopReason == "cancelled" (a
// spontaneous end would be "end_turn") is a second, independent guard. M1-before /
// M2-after ordering is the third. The stream path CAN assert StopReason — the PTY
// sibling cannot (tui-driver's EventKindJsonlEndOfTurn can't distinguish an
// interrupt-stop from a clean end); this is the distinguishing rigour of the stream path.
func TestRelayV2_StreamInterruptStopsRunningTurn(t *testing.T) {
	const (
		initialUUID    = "11111111-1111-4111-8111-111111111111" // bootstrap
		knownUserText  = "e2e-1136-user:go\n"
		echoNeedle     = "e2e-1136-user:go"
		createReqID    = uint64(1136)
		sendReqID      = uint64(1137)
		interruptReqID = uint64(1138)
	)

	home := shortHome(t)

	// Pair one interactive device.
	rA := RunBareIn(t, home, "pair", "-pyry-name=test", "--name=phone-a")
	if rA.ExitCode != 0 {
		t.Fatalf("pyry pair phone-a exit=%d\nstdout:\n%s\nstderr:\n%s", rA.ExitCode, rA.Stdout, rA.Stderr)
	}
	payloadA := decodePairPayload(t, rA.Stdout)
	pubKey, err := base64.StdEncoding.DecodeString(payloadA.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	// No seedBoundConversation: the interrupt target is MINTED over the wire (below),
	// so its binding is created by create_conversation, not seeded. Only the one extra
	// env (PYRY_FAKE_CLAUDE_STREAM_INTERRUPT) distinguishes this from #1141's send test —
	// the daemon runs under interactive_runner:"stream-json", and both the bootstrap and
	// the minted child inherit the interrupt mode via the daemon's process env.
	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })

	h := StartStreamInteractiveWithRelay(t, home, initialUUID, fr.URL()+"/v2/server",
		"PYRY_FAKE_CLAUDE_STREAM_INTERRUPT=1")
	t.Cleanup(func() { h.Stop(t) })

	serverID := readPersistedServerID(t, home)
	waitBinaryHello(t, fr, serverID)

	dialCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	phoneA, err := fakephone.Dial(dialCtx, fr.URL(), serverID, payloadA.Token, "phone-a")
	if err != nil {
		t.Fatalf("phone A dial: %v", err)
	}
	t.Cleanup(func() { _ = phoneA.Close() })
	// Interactive — the capability both the structured stream AND handleInterrupt require.
	sendA, recvA := driveHandshakeToOpenDaemonInteractive(t, phoneA, pubKey, payloadA.Token)

	sealSend := func(env protocol.Envelope) {
		t.Helper()
		raw, err := json.Marshal(env)
		if err != nil {
			t.Fatalf("marshal envelope: %v", err)
		}
		ciphertext, err := sendA.Encrypt(raw)
		if err != nil {
			t.Fatalf("seal envelope: %v", err)
		}
		sendNoiseMsg(t, phoneA, ciphertext)
	}

	// nextEnv decrypts the next binary→phone application envelope, skipping non-noise_msg
	// inner frames in capture order so the receive nonce stays in sequence. One recvA is
	// used for the whole test (the single reader, same as the send / new_session specs).
	// ok=false on deadline.
	nextEnv := func(deadline time.Time) (protocol.Envelope, bool) {
		t.Helper()
		for {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return protocol.Envelope{}, false
			}
			raw, err := phoneA.ReceiveBytes(remaining)
			if err != nil {
				if errors.Is(err, fakephone.ErrReceiveTimeout) {
					return protocol.Envelope{}, false
				}
				t.Fatalf("phone A receive: %v", err)
			}
			var inner protocol.InnerFrameV2
			if err := json.Unmarshal(raw, &inner); err != nil {
				t.Fatalf("phone A decode inner frame: %v", err)
			}
			if inner.Type != protocol.TypeNoiseMsg {
				continue
			}
			return decryptInnerEnvelope(t, inner, recvA), true
		}
	}

	// --- Mint the target conversation over the wire. All-null create_conversation
	// (server defaults). The interactive session may interleave broadcasts, so drain
	// until conversation_created rather than assuming the next frame is it. The minted
	// session gets its OWN stream runner + its OWN stream fakeclaude child (interrupt
	// mode inherited via the daemon env).
	sealSend(protocol.Envelope{
		ID:      createReqID,
		Type:    protocol.TypeCreateConversation,
		TS:      time.Now().UTC(),
		Payload: mustJSON(t, protocol.CreateConversationPayload{}),
	})
	var convB string
	createDeadline := time.Now().Add(15 * time.Second)
	for convB == "" {
		env, ok := nextEnv(createDeadline)
		if !ok {
			t.Fatal("did not receive conversation_created before deadline (the minted stream-json session never came up)")
		}
		if env.Type == protocol.TypeError {
			t.Fatalf("unexpected error envelope while awaiting conversation_created: %s", string(env.Payload))
		}
		if env.Type != protocol.TypeConversationCreated {
			continue
		}
		var p protocol.ConversationCreatedPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			t.Fatalf("decode conversation_created payload: %v", err)
		}
		if p.ID == "" {
			t.Fatal("conversation_created carried an empty id")
		}
		convB = p.ID
	}
	t.Logf("minted stream-json conversation %s (its own runner + child)", convB)

	// --- Drive one send_message to convB and AWAIT its sealed ack. The ack proves the
	// turn was accepted and the active cursor stamped to convB (so the payload-less
	// interrupt routes to it) — it does NOT prove delivery (async; M1 is the real
	// "turn started" signal).
	sealSend(protocol.Envelope{
		ID:   sendReqID,
		Type: protocol.TypeSendMessage,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.SendMessagePayload{
			ConversationID: convB,
			MessageID:      "u-1",
			Text:           knownUserText,
		}),
	})
	ackDeadline := time.Now().Add(15 * time.Second)
	gotAck := false
	for !gotAck {
		env, ok := nextEnv(ackDeadline)
		if !ok {
			t.Fatal("did not receive the send_message ack for the minted conversation before deadline")
		}
		if env.Type == protocol.TypeError {
			t.Fatalf("unexpected error envelope while awaiting the send_message ack: %s", string(env.Payload))
		}
		if env.Type == protocol.TypeAck && env.InReplyTo != nil && *env.InReplyTo == sendReqID {
			gotAck = true
		}
	}

	// --- M1 (AC1): the turn is in flight. Drain until an assistant_delta for convB whose
	// Text carries the echoed prompt. The echo is the non-vacuity guard — a full
	// phone→daemon→fake→daemon→phone round-trip, not merely "some text". A leading
	// turn_state{responding} precedes it; ignore it until the delta is seen. Observing
	// the delta proves the turn is RUNNING when we send the interrupt. The ~20s deadline
	// absorbs mint + spawn + first-turn latency (WriteUserTurn returns the retryable
	// ErrNoLiveChild between the minted child's spawn and stdin-ready; the inbound queue
	// retries, so the turn lands once the child is live).
	sawDelta := false
	m1Deadline := time.Now().Add(20 * time.Second)
	for !sawDelta {
		env, ok := nextEnv(m1Deadline)
		if !ok {
			t.Fatal("M1: never observed an assistant_delta for the minted conversation; the turn never started in flight " +
				"(delivery never reached the minted child, or the parser / drain gate / emitter dropped it)")
		}
		if env.Type != protocol.TypeAssistantDelta {
			continue
		}
		var d protocol.AssistantDeltaPayload
		if err := json.Unmarshal(env.Payload, &d); err != nil {
			t.Fatalf("phone A decode assistant_delta payload: %v", err)
		}
		if d.ConversationID != convB {
			t.Errorf("assistant_delta ConversationID: got %q, want %q", d.ConversationID, convB)
		}
		if !strings.Contains(d.Text, echoNeedle) {
			t.Fatalf("M1: assistant_delta did not carry the echoed prompt; got Text=%q, want it to contain %q "+
				"(the round-trip proof — fakeclaude echoes the sent prompt back through the daemon)", d.Text, echoNeedle)
		}
		sawDelta = true
	}
	t.Logf("M1: observed assistant_delta echoing the prompt — the turn is in flight when we interrupt (AC1)")

	// --- Send the interrupt (only after M1, so the minted child is live). A payload-less
	// interrupt frame routes to the ACTIVE conversation (convB) → resolveBoundRunner →
	// session-B's own stream runner → WriteInterrupt → control_request to fakeclaude-B.
	sealSend(protocol.Envelope{
		ID:   interruptReqID,
		Type: protocol.TypeInterrupt,
		TS:   time.Now().UTC(),
	})

	// --- M2 (AC2 + AC3): the interrupted turn end. Drain until a turn_end for convB with
	// StopReason == "cancelled". Because the fake emitted no result on the user turn, the
	// ONLY source of a turn_end is its response to the interrupt control_request
	// (structural causality — the PTY sibling's guard), and "cancelled" (not "end_turn")
	// confirms it took the error_during_execution path, not a spontaneous end. That the
	// turn_end arrives for convB at all proves the interrupt reached the minted
	// conversation's own bound runner (not the idle bootstrap) — the live routing-target
	// confirmation (AC3).
	sawInterruptedEnd := false
	m2Deadline := time.Now().Add(15 * time.Second)
	for !sawInterruptedEnd {
		env, ok := nextEnv(m2Deadline)
		if !ok {
			t.Fatal("M2: never observed a turn_end after the interrupt; the interrupt did not stop the turn " +
				"(it never routed to the minted conversation's bound runner, or the fake never honoured the control_request). " +
				"The fake emits NO result on the user turn, so the ONLY source of a turn_end is its interrupt response")
		}
		if env.Type != protocol.TypeTurnEnd {
			continue
		}
		var te protocol.TurnEndPayload
		if err := json.Unmarshal(env.Payload, &te); err != nil {
			t.Fatalf("phone A decode turn_end payload: %v", err)
		}
		if te.ConversationID != convB {
			t.Errorf("turn_end ConversationID: got %q, want %q", te.ConversationID, convB)
		}
		if te.StopReason != "cancelled" {
			t.Fatalf("M2: turn_end StopReason = %q, want %q (a spontaneous end would be \"end_turn\"; \"cancelled\" "+
				"confirms the fake took the error_during_execution path in response to the interrupt control_request)",
				te.StopReason, "cancelled")
		}
		sawInterruptedEnd = true
	}
	t.Logf("M2: observed turn_end{StopReason:cancelled} for the minted conversation — the interrupt stopped the turn (AC2/AC3)")
}

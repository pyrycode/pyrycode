//go:build e2e

package e2e

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
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
	//
	// It also carries #1500's zero-unrecognized_message assertion, and it sits HERE
	// rather than in one drain loop because that is one site covering all four windows:
	// the bogus rider is off in this test, so every window must be clean, and the union
	// strictly contains the one AC3(a) requires (up to and including the cancelled
	// turn_end). The zero is NOT vacuous: fakeclaude writes the control_response ack
	// BEFORE the interrupted result, on the same writer, so the turn_end this test
	// already blocks on cannot arrive unless the ack has already been through the
	// parser. Its arrival is pinned separately, on the emitted bytes, by the fake's own
	// TestRunStreamJSON_InterruptAckRider — a zero-assertion cannot prove its own input
	// arrived.
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
			env := decryptInnerEnvelope(t, inner, recvA)
			if env.Type == protocol.TypeUnrecognizedMessage {
				var p protocol.UnrecognizedMessagePayload
				if err := json.Unmarshal(env.Payload, &p); err != nil {
					t.Fatalf("phone A decode unrecognized_message payload: %v", err)
				}
				t.Fatalf("an unrecognized_message reached the phone during the interrupt: site=%q type=%q truncated=%v\n"+
					"raw: %s\n\n"+
					"The interrupt ack is the expected culprit (#1500): the daemon writes an interrupt "+
					"control_request to the child's stdin and the child answers on the SAME stdout the parser "+
					"reads, so a missing control_response arm in streamsup.consumeLine turns every interrupt into "+
					"this frame — the one frame whose whole value is meaning \"claude started emitting something "+
					"NEW\". Read the type above: if it is control_response the arm regressed; anything else means "+
					"the fake grew a shape the parser has no mapping for.",
					p.Site, p.MessageType, p.Truncated, p.Raw)
			}
			return env, true
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

// TestRelayV2_StreamInterruptNamedConversationStopsThatOne is #2103's fake-daemon
// proof: with the daemon's current-conversation cursor parked on A, an interrupt
// naming B stops B's turn and leaves A's running. Its bare-path sibling above stays
// exactly as it was — that is the shape an un-upgraded client sends.
//
// THE SEND ORDER IS FORCED, not chosen. A conversation is mid-turn only after a
// send, and the cursor is stamped only by a successful sessionRouter.Route, so the
// conversation that must own the cursor has to be sent to LAST. It cannot be
// re-stamped afterwards either: a second send to a conversation whose turn is in
// flight blocks on streamTurnHoldTimeout, which is fifteen minutes. Hence B, then A.
//
//	M1  mint B         — create_conversation; since #2085 this binds B's session
//	                     without spawning its child.
//	M2  send to B      — B's child comes up, B's turn goes in flight, cursor = B.
//	M3  send to A      — A's turn goes in flight and the cursor moves to A. This is
//	                     the step that makes the test able to fail: with the cursor
//	                     on A, a daemon that ignored the named id stops A.
//	M4  interrupt B    — the frame under test.
//
// AC-4 ASKS FOR B's turn_end ON THE WIRE AND THAT FRAME CANNOT EXIST IN THIS STATE,
// which is a fact about the daemon rather than about this test. startStreamTurnDrainV2
// gates every turn event on the CURSOR conversation's bound session and drops the
// rest; there is one shared interactiveTurnEmitterV2 over one global cursor, and
// emitter.Handle sits below that gate. So with the cursor on A, B's TurnEnd is
// dropped before it is ever shaped into an envelope — it has no conversation_id and
// no StopReason to assert on, because it never becomes one. Making background
// conversations' turn events reach the phone is a different ticket; this one must
// not smuggle it in.
//
// The substitute is a trio, and each member covers the others' blind spot:
//
//	(a) v2.interrupt.dispatched carrying B's conversation id. Proves the frame
//	    arrived, resolved B and dispatched to B's bound runner. Under the pre-#2103
//	    daemon the identical record carries A's id, so the ID is the discriminator,
//	    not the record's existence.
//	(b) stream_turn.not_active with kind=turn_end and B's session id. This IS AC-4's
//	    turn_end for B, observed at the one point the architecture lets it be seen.
//	    It is not a proxy for the actuation: fakeclaude's interrupt mode emits no
//	    result on a user turn, so the only possible source of a TurnEnd for B is its
//	    response to the interrupt control_request — the same structural causality
//	    the bare-path sibling above relies on.
//	(c) no turn_end for A on the wire. A is the ACTIVE session, so a turn_end for A
//	    would drain to the phone; under the defect the interrupt stops A and
//	    turn_end{convA,cancelled} arrives. This is the live, on-the-wire form of
//	    "A's turn is still in flight".
//
// (c) runs LAST because it is destructive: it ends on a receive that reaches its
// deadline, and a timed-out fakephone receive takes the conn down with it (see
// waitForLog's doc). Ordering it before the log assertions would report the
// resulting death as an unrelated failure.
func TestRelayV2_StreamInterruptNamedConversationStopsThatOne(t *testing.T) {
	const (
		initialUUID = "11111111-1111-4111-8111-111111111111" // bootstrap, bound to conversation A
		convA       = "22222222-2222-4222-8222-222222222222"
		textToB     = "e2e-2103-user:b\n"
		needleToB   = "e2e-2103-user:b"
		textToA     = "e2e-2103-user:a\n"
		needleToA   = "e2e-2103-user:a"

		createBReqID   = uint64(2103)
		sendToBReqID   = uint64(2104)
		sendToAReqID   = uint64(2105)
		interruptReqID = uint64(2106)
	)

	testStart := time.Now()
	elapsed := func() string { return time.Since(testStart).Round(time.Millisecond).String() }

	home := shortHome(t)

	rA := RunBareIn(t, home, "pair", "-pyry-name=test", "--name=phone-a")
	if rA.ExitCode != 0 {
		t.Fatalf("pyry pair phone-a exit=%d\nstdout:\n%s\nstderr:\n%s", rA.ExitCode, rA.Stdout, rA.Stderr)
	}
	payloadA := decodePairPayload(t, rA.Stdout)
	pubKey, err := base64.StdEncoding.DecodeString(payloadA.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	// Bind conversation A to the bootstrap session before the daemon starts: the
	// daemon loads conversations.json once at startup. Unlike the bare-path sibling
	// this test needs A to be addressable by name, because A is the conversation the
	// cursor must end up on.
	seedBoundConversation(t, home, convA, initialUUID)

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })

	regPath := filepath.Join(home, ".pyry", "test", "sessions.json")

	// PYRY_FAKE_CLAUDE_STREAM_INTERRUPT puts BOTH children in interrupt mode (it
	// reaches them through the daemon's process env), which is what lets two turns
	// be in flight at once: the fake answers a user turn with an assistant chunk and
	// no result, so a turn ends only when an interrupt arrives.
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

	// One reader for the whole test so the receive CipherState nonce stays in
	// sequence; ok=false on deadline.
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

	// sendAndAwaitEcho drives one send_message and drains to the assistant_delta
	// echoing it. The echo — not the ack — is what proves that conversation's child
	// is live and serving, which is the precondition a vacuous pass would lack: an
	// interrupt against a conversation with no running turn is inert by
	// construction (streamsup.WriteInterrupt refuses a nil writer), so a test that
	// interrupted a childless B would assert nothing.
	sendAndAwaitEcho := func(milestone string, reqID uint64, convID, msgID, text, needle string) {
		t.Helper()
		sealSend(protocol.Envelope{
			ID:   reqID,
			Type: protocol.TypeSendMessage,
			TS:   time.Now().UTC(),
			Payload: mustJSON(t, protocol.SendMessagePayload{
				ConversationID: convID,
				MessageID:      msgID,
				Text:           text,
			}),
		})
		deadline := time.Now().Add(30 * time.Second)
		for {
			env, ok := nextEnv(deadline)
			if !ok {
				t.Fatalf("%s: never observed an assistant_delta echoing %q for conversation %s; that "+
					"conversation's turn never went in flight, so the interrupt under test would be "+
					"inert and this test vacuous", milestone, needle, convID)
			}
			if env.Type == protocol.TypeError {
				t.Fatalf("%s: unexpected error envelope: %s", milestone, string(env.Payload))
			}
			if env.Type != protocol.TypeAssistantDelta {
				continue
			}
			var d protocol.AssistantDeltaPayload
			if err := json.Unmarshal(env.Payload, &d); err != nil {
				t.Fatalf("%s: decode assistant_delta payload: %v", milestone, err)
			}
			if d.ConversationID == convID && strings.Contains(d.Text, needle) {
				return
			}
		}
	}

	// --- M1: mint conversation B over the wire. All-null create_conversation
	// (server defaults). Drain until conversation_created rather than assuming the
	// next frame is it — the interactive session interleaves broadcasts.
	sealSend(protocol.Envelope{
		ID:      createBReqID,
		Type:    protocol.TypeCreateConversation,
		TS:      time.Now().UTC(),
		Payload: mustJSON(t, protocol.CreateConversationPayload{}),
	})
	var convB string
	createDeadline := time.Now().Add(15 * time.Second)
	for convB == "" {
		env, ok := nextEnv(createDeadline)
		if !ok {
			t.Fatal("M1: did not receive conversation_created before deadline")
		}
		if env.Type == protocol.TypeError {
			t.Fatalf("M1: unexpected error envelope while awaiting conversation_created: %s", string(env.Payload))
		}
		if env.Type != protocol.TypeConversationCreated {
			continue
		}
		var p protocol.ConversationCreatedPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			t.Fatalf("M1: decode conversation_created payload: %v", err)
		}
		if p.ID == "" {
			t.Fatal("M1: conversation_created carried an empty id")
		}
		convB = p.ID
	}
	if convB == convA {
		t.Fatalf("M1: the minted conversation id equals A's (%s); the test needs two distinct "+
			"conversations or its whole premise collapses", convA)
	}
	t.Logf("[t=%s] M1: minted conversation B = %s", elapsed(), convB)

	// --- M2: B's turn goes in flight (and the cursor lands on B for now).
	sendAndAwaitEcho("M2", sendToBReqID, convB, "u-b1", textToB, needleToB)
	t.Logf("[t=%s] M2: conversation B's turn is in flight", elapsed())

	// --- M3: A's turn goes in flight AND the cursor moves to A. Both turns are now
	// open, and the daemon's cursor points at the one the frame will NOT name.
	sendAndAwaitEcho("M3", sendToAReqID, convA, "u-a1", textToA, needleToA)
	t.Logf("[t=%s] M3: conversation A's turn is in flight and the cursor is on A — "+
		"the named interrupt now has something to get wrong", elapsed())

	// B's session id, for assertion (b). The pool labels a minted session with the
	// conversation id that asked for it, so the label is the only handle a test has
	// on a non-bootstrap session's id.
	entryB, ok := readEntryByLabel(regPath, convB)
	if !ok || entryB.ID == "" {
		t.Fatalf("conversation B (%s) has no session entry; assertion (b) has nothing to match on\nfile:\n%s",
			convB, mustReadFile(t, regPath))
	}

	// --- M4: the frame under test — an interrupt NAMING B while the cursor is on A.
	sealSend(protocol.Envelope{
		ID:      interruptReqID,
		Type:    protocol.TypeInterrupt,
		TS:      time.Now().UTC(),
		Payload: mustJSON(t, protocol.InterruptPayload{ConversationID: convB}),
	})

	// (a) The daemon resolved the NAMED conversation, not the cursor's. Under the
	// pre-#2103 daemon this same record carries convA, so the id is what discriminates.
	waitForLogLineAll(t, h.Stderr, []string{"v2.interrupt.dispatched", convB}, 15*time.Second)
	t.Logf("[t=%s] M4(a): the interrupt dispatched to conversation B's own bound runner", elapsed())

	// (b) B's turn ended. The drain observes the TurnEnd and then drops it because
	// B is not the cursor's conversation, which is the only place this frame is
	// visible in this state — see the header. The fake emits no result on a user
	// turn, so the interrupt is its only possible cause.
	waitForLogLineAll(t, h.Stderr,
		[]string{"stream_turn.not_active", "turn_end", entryB.ID}, 15*time.Second)
	t.Logf("[t=%s] M4(b): conversation B's turn ended (session %s)", elapsed(), entryB.ID)

	// (c) A's turn is still in flight. Destructive and therefore last: this loop
	// ends on a receive that reaches its deadline, which takes the phone conn down.
	settle := time.Now().Add(3 * time.Second)
	for {
		env, ok := nextEnv(settle)
		if !ok {
			break
		}
		if env.Type != protocol.TypeTurnEnd {
			continue
		}
		var te protocol.TurnEndPayload
		if err := json.Unmarshal(env.Payload, &te); err != nil {
			t.Fatalf("M4(c): decode turn_end payload: %v", err)
		}
		if te.ConversationID == convA {
			t.Fatalf("M4(c) (AC-1): conversation A's turn ended (StopReason %q) after an interrupt naming "+
				"B (%s). A is the CURSOR's conversation and its turn must still be in flight: this is "+
				"the #2103 defect.", te.StopReason, convB)
		}
	}
	t.Logf("[t=%s] M4(c): no turn_end for conversation A — the cursor's turn is still in flight", elapsed())
}

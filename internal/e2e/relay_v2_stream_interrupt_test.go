//go:build e2e

package e2e

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/scenario"
	"github.com/pyrycode/pyrycode/internal/history"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/paireddevice"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// TestRelayV2_StreamInterruptStopsRunningTurn requires a fresh source echo,
// a cancelled source end from that producing turn and actual terminal live idle.
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
	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	relayURL := fr.URL() + "/v2/server"

	// Pair one interactive device.
	payloadA, err := paireddevice.Setup(paireddevice.Config{
		Home:                   home,
		InstanceName:           "test",
		Relay:                  relayURL,
		DeviceName:             "phone-a",
		AllowRemotePermissions: false,
	})
	if err != nil {
		t.Fatalf("setup paired device: %v", err)
	}
	pubKey, err := base64.StdEncoding.DecodeString(payloadA.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	// No seedBoundConversation: the interrupt target is MINTED over the wire (below),
	// so its binding is created by create_conversation, not seeded. Only the one extra
	// env (PYRY_FAKE_CLAUDE_STREAM_INTERRUPT) distinguishes this from #1141's send test —
	// the daemon runs under interactive_runner:"stream-json", and both the bootstrap and
	// the minted child inherit the interrupt mode via the daemon's process env.
	h := StartStreamInteractiveWithRelay(t, home, initialUUID, relayURL,
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

	source := scenario.NewSourceObserver(t, home)
	sealSend := func(env protocol.Envelope) {
		source.Before(env)
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
	// Keep M1's echo through the ack wait on the same single reader.
	var convB string
	sawDelta := false
	sawInterruptedEnd := false
	source.OnEntry = func(entry history.Entry) {
		var p struct {
			ConversationID string `json:"conversation_id"`
			Text           string `json:"text"`
			Parent         string `json:"parent_tool_use_id"`
			Reason         string `json:"stop_reason"`
		}
		if json.Unmarshal(entry.Payload, &p) != nil {
			t.Fatal("invalid interrupt source")
		}
		if p.ConversationID != convB || p.Parent != "" {
			return
		}
		if entry.Type == protocol.TypeAssistantDelta && strings.Contains(p.Text, echoNeedle) {
			sawDelta = true
		}
		if entry.Type == protocol.TypeTurnEnd {
			if p.Reason != "cancelled" {
				t.Fatal("interrupt source did not end cancelled")
			}
			sawInterruptedEnd = true
		}
	}

	poll := scenario.PollingReceive(t, phoneA, recvA)
	nextEnv := func(deadline time.Time) (protocol.Envelope, bool) {
		env, ok := source.Next(poll, deadline)
		if env.Type == protocol.TypeUnrecognizedMessage {
			t.Fatal("interrupt exposed an unsupported provider message")
		}
		return env, ok
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

	// --- M1 (AC1): the turn is in flight. Use the recorded assistant_delta for convB whose
	// Text carries the echoed prompt. The echo is the non-vacuity guard — a full
	// phone→daemon→fake→daemon→phone round-trip, not merely "some text". A leading
	// turn_state{responding} precedes it; ignore it until the delta is seen. Observing
	// the delta proves the turn is RUNNING when we send the interrupt. The ~20s deadline
	// absorbs mint + spawn + first-turn latency (WriteUserTurn returns the retryable
	// ErrNoLiveChild between the minted child's spawn and stdin-ready; the inbound queue
	// retries, so the turn lands once the child is live).
	m1Deadline := time.Now().Add(20 * time.Second)
	for !sawDelta {
		_, ok := nextEnv(m1Deadline)
		if !ok {
			t.Fatal("M1: never observed an assistant_delta for the minted conversation; the turn never started in flight " +
				"(delivery never reached the minted child, or the parser / drain gate / emitter dropped it)")
		}
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
	m2Deadline := time.Now().Add(15 * time.Second)
	for !sawInterruptedEnd {
		if _, ok := nextEnv(m2Deadline); !ok {
			t.Fatal("no matching cancelled source and terminal live idle")
		}
	}
	t.Logf("M2: observed turn_end{StopReason:cancelled} for the minted conversation — the interrupt stopped the turn (AC2/AC3)")
}

// TestRelayV2_StreamInterruptNamedConversationStopsThatOne keeps both turns
// running, interrupts B while the cursor is on A, and verifies A stays open.
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
	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	relayURL := fr.URL() + "/v2/server"

	payloadA, err := paireddevice.Setup(paireddevice.Config{
		Home:                   home,
		InstanceName:           "test",
		Relay:                  relayURL,
		DeviceName:             "phone-a",
		AllowRemotePermissions: false,
	})
	if err != nil {
		t.Fatalf("setup paired device: %v", err)
	}
	pubKey, err := base64.StdEncoding.DecodeString(payloadA.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	// Bind conversation A to the bootstrap session before the daemon starts: the
	// daemon loads conversations.json once at startup. Unlike the bare-path sibling
	// this test needs A to be addressable by name, because A is the conversation the
	// cursor must end up on.
	seedBoundConversation(t, home, convA, initialUUID)

	regPath := filepath.Join(home, ".pyry", "test", "sessions.json")

	// PYRY_FAKE_CLAUDE_STREAM_INTERRUPT puts BOTH children in interrupt mode (it
	// reaches them through the daemon's process env), which is what lets two turns
	// be in flight at once: the fake answers a user turn with an assistant chunk and
	// no result, so a turn ends only when an interrupt arrives.
	h := StartStreamInteractiveWithRelay(t, home, initialUUID, relayURL,
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

	source := scenario.NewSourceObserver(t, home)
	sealSend := func(env protocol.Envelope) {
		source.Before(env)
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
	echoes := map[string]string{}
	ends := map[string]string{}
	sessions := map[string]string{}
	source.OnEntry = func(entry history.Entry) {
		var p struct {
			ConversationID string `json:"conversation_id"`
			Text           string `json:"text"`
			Parent         string `json:"parent_tool_use_id"`
			Reason         string `json:"stop_reason"`
		}
		if json.Unmarshal(entry.Payload, &p) != nil {
			t.Fatal("invalid named interrupt source")
		}
		if p.Parent != "" {
			return
		}
		sessions[p.ConversationID] = entry.Session.SessionID
		if entry.Type == protocol.TypeAssistantDelta {
			echoes[p.ConversationID] += p.Text
		}
		if entry.Type == protocol.TypeTurnEnd {
			ends[p.ConversationID] = p.Reason
		}
	}

	poll := scenario.PollingReceive(t, phoneA, recvA)
	nextEnv := func(deadline time.Time) (protocol.Envelope, bool) {
		env, ok := source.Next(poll, deadline)
		if env.Type == protocol.TypeUnrecognizedMessage {
			t.Fatal("interrupt exposed an unsupported provider message")
		}
		return env, ok
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
		acked := false
		for !acked || !strings.Contains(echoes[convID], needle) {
			env, ok := nextEnv(deadline)
			if !ok {
				t.Fatalf("%s: no correlated ack and fresh source echo for %s", milestone, convID)
			}
			if env.Type == protocol.TypeError {
				t.Fatal("send rejected")
			}
			if env.Type == protocol.TypeAck && env.InReplyTo != nil && *env.InReplyTo == reqID {
				acked = true
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

	deadline := time.Now().Add(15 * time.Second)
	for ends[convB] == "" {
		if _, ok := nextEnv(deadline); !ok {
			t.Fatal("named B lacks matching source cancellation and live idle")
		}
		if ends[convA] != "" {
			t.Fatal("named B interrupt stopped cursor A")
		}
	}
	if ends[convB] != "cancelled" || sessions[convB] != entryB.ID || sessions[convA] != initialUUID {
		t.Fatal("named interrupt lost reason or producing session")
	}
	settle := time.Now().Add(3 * time.Second)
	for time.Now().Before(settle) {
		if _, ok := nextEnv(settle); !ok {
			break
		}
		if ends[convA] != "" {
			t.Fatal("cursor A ended after named B interrupt")
		}
	}
}

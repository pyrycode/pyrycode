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
	"github.com/pyrycode/pyrycode/internal/e2e/internal/paireddevice"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// TestRelayV2_StreamSendMessageDrainsTurn is the first LIVE, end-to-end proof of
// the stream-json interactive runner. Every leg of the wire has been proven in
// isolation — the config toggle (selectInteractiveRunner, #1081), the stream
// runner factory (#1109/#1098), the turn drain (startStreamTurnDrainV2 →
// interactiveTurnEmitterV2, #1098), and the stream-json fakeclaude mode (#1140) —
// but this is the first time all of them run TOGETHER, live, against a real spawned
// fakeclaude.
//
// One interactive phone handshakes to a daemon started under
// interactive_runner:"stream-json" (StartStreamInteractiveWithRelay writes the
// production config toggle), drives ONE send_message, and observes the turn drain:
//
//	phone send_message(knownConvID, "…ping") → sessionRouter.Route (stamps the
//	  active-conversation cursor) → boundSession.WriteUserTurn → streamsup WriteTurn →
//	  marshalTurnEnvelope → {"type":"user",…"text":"…ping"} → fakeclaude stdin (stream
//	  mode) → one assistant text line (echoing "…ping") + one result line → daemon
//	  streamsup.Parser → TextChunk + TurnEnd → drain gate (sink tag == activeSession ✓)
//	  → interactiveTurnEmitterV2 → turn_state{responding} → assistant_delta{"…ping"}
//	  → turn_end → turn_state{idle} → sealed push → phone.
//
// Two ordered milestones ARE the acceptance criteria:
//   - M1 assistant_delta carrying the ECHOED prompt (ConversationID == knownConvID).
//     The echo is the non-vacuity guard: it proves the full
//     phone→daemon→fakeclaude→daemon→phone round-trip, not merely "some text".
//   - M2 terminal turn_state{idle} (ConversationID == knownConvID), observed AFTER
//     the delta — the turn closed.
//
// WHY THE IDS MUST LINE UP (the one non-obvious invariant). The drain gate forwards
// an event to the emitter only when the event's tag equals activeSession(). The
// sink tag is the runner's construction-time cfg.SessionID = the bootstrap pool id,
// pinned to initialUUID by seedBootstrapRegistry; activeSession() resolves the
// active conversation → its bound session id = knownConvID's binding = initialUUID
// via seedBoundConversation. So seedBootstrapRegistry(initialUUID) +
// seedBoundConversation(knownConvID, initialUUID) is what makes the gate pass. A
// mismatched UUID drops every event at the gate and hangs the drain — surfacing as
// an M1 timeout with a clear diagnostic, never a silent pass.
//
// The ack precedes delivery: streamRunner.WriteUserTurn returns the retryable
// ErrNoLiveChild while the child is between spawn and stdin-ready, and the send
// handler acks on accept-into-backlog. So the test must NOT treat the ack as the
// turn — it drains M1/M2 as the real completion signal, and the ~20s drain deadline
// absorbs the spawn + first-turn latency.
func TestRelayV2_StreamSendMessageDrainsTurn(t *testing.T) {
	const (
		initialUUID   = "11111111-1111-4111-8111-111111111111"
		knownConvID   = "22222222-2222-4222-8222-222222222222"
		knownUserText = "e2e-1141-user:ping\n"
		echoNeedle    = "e2e-1141-user:ping"
		sendReqID     = uint64(1141)
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

	// Bind knownConvID to the bootstrap session (initialUUID). Two effects:
	// sessionRouter.Route resolves knownConvID → the bootstrap session and stamps the
	// active-conversation cursor (#678 rejects an empty current_session_id before any
	// pool Lookup), and the drain gate's activeSession() resolves to initialUUID —
	// equal to the sink tag. The daemon loads conversations.json once at startup, so
	// the row must exist BEFORE it starts.
	seedBoundConversation(t, home, knownConvID, initialUUID)

	h := StartStreamInteractiveWithRelay(t, home, initialUUID, relayURL)
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
	// Interactive — the capability the structured stream requires.
	sendA, recvA := driveHandshakeToOpenDaemonInteractive(t, phoneA, pubKey, payloadA.Token)

	// Drive one send_message and await its sealed ack. The ack proves the turn was
	// accepted and the active cursor stamped — it does NOT prove delivery (async; the
	// real completion signal is the drain below).
	reqEnv, err := json.Marshal(protocol.Envelope{
		ID:   sendReqID,
		Type: protocol.TypeSendMessage,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.SendMessagePayload{
			ConversationID: knownConvID,
			MessageID:      "u-1",
			Text:           knownUserText,
		}),
	})
	if err != nil {
		t.Fatalf("marshal send_message envelope: %v", err)
	}
	sendCipher, err := sendA.Encrypt(reqEnv)
	if err != nil {
		t.Fatalf("seal send_message envelope: %v", err)
	}
	sendNoiseMsg(t, phoneA, sendCipher)

	var ackEnv protocol.Envelope
	ackDeadline := time.Now().Add(15 * time.Second)
	for {
		remaining := time.Until(ackDeadline)
		if remaining <= 0 {
			t.Fatal("interactive phone A never received the send_message ack; the turn was never accepted")
		}
		env := decryptInnerEnvelope(t, readInnerFrame(t, phoneA, remaining), recvA)
		if env.Type == protocol.TypeAck {
			ackEnv = env
			break
		}
	}
	if ackEnv.InReplyTo == nil || *ackEnv.InReplyTo != sendReqID {
		t.Errorf("ack InReplyTo = %v, want pointer to %d", ackEnv.InReplyTo, sendReqID)
	}

	// Drain the turn: two ordered milestones (mirrors the interrupt test's shape). A
	// leading turn_state{responding} arrives BEFORE the delta; ignore turn_states
	// until sawDelta is set. Read the phone conn serially on the test goroutine (the
	// single reader, same as the interrupt / new_session specs).
	sawDelta := false
	drainDeadline := time.Now().Add(20 * time.Second)
	for {
		remaining := time.Until(drainDeadline)
		if remaining <= 0 {
			if !sawDelta {
				t.Fatal("M1: interactive phone A never observed an assistant_delta; the turn never drained " +
					"end-to-end (delivery never reached the child, or the parser / drain gate / emitter dropped " +
					"it — most likely a UUID mismatch between seedBootstrapRegistry and seedBoundConversation)")
			}
			t.Fatal("M2: interactive phone A observed the assistant_delta but never a terminal " +
				"turn_state{idle}; the turn opened but never closed")
		}
		raw, err := phoneA.ReceiveBytes(remaining)
		if err != nil {
			if errors.Is(err, fakephone.ErrReceiveTimeout) {
				continue // deadline reached — re-loop into the milestone-specific t.Fatal above
			}
			t.Fatalf("phone A receive (drain): %v", err)
		}
		var inner protocol.InnerFrameV2
		if err := json.Unmarshal(raw, &inner); err != nil {
			t.Fatalf("phone A decode inner frame (drain): %v", err)
		}
		if inner.Type != protocol.TypeNoiseMsg {
			continue
		}
		env := decryptInnerEnvelope(t, inner, recvA)

		switch env.Type {
		case protocol.TypeAssistantDelta:
			if sawDelta {
				continue
			}
			var d protocol.AssistantDeltaPayload
			if err := json.Unmarshal(env.Payload, &d); err != nil {
				t.Fatalf("phone A decode assistant_delta payload: %v", err)
			}
			if d.ConversationID != knownConvID {
				t.Errorf("assistant_delta ConversationID: got %q, want %q", d.ConversationID, knownConvID)
			}
			if !strings.Contains(d.Text, echoNeedle) {
				t.Fatalf("M1: assistant_delta did not carry the echoed prompt; got Text=%q, want it to contain %q "+
					"(the round-trip proof — fakeclaude echoes the sent prompt back through the daemon)", d.Text, echoNeedle)
			}
			sawDelta = true
			t.Logf("M1: interactive phone A observed assistant_delta carrying the echoed prompt")
		case protocol.TypeTurnState:
			if !sawDelta {
				continue // the leading responding state precedes the delta
			}
			var st protocol.TurnStatePayload
			if err := json.Unmarshal(env.Payload, &st); err != nil {
				t.Fatalf("phone A decode turn_state payload: %v", err)
			}
			if st.State != "idle" {
				continue
			}
			if st.ConversationID != knownConvID {
				t.Errorf("turn_state ConversationID: got %q, want %q", st.ConversationID, knownConvID)
			}
			t.Logf("M2: interactive phone A observed terminal turn_state{idle} — the turn closed")
			return
		}
	}
}

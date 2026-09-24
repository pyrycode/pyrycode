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
// gate drop logs stream_turn.not_active, which the M1 failure checks for before it
// names a mismatch. (#2610: re-seeding either side with a different UUID was
// observed to still drain, so a mismatch is no longer assumed to be the cause.)
//
// The ack is not delivery: streamRunner.WriteUserTurn returns the retryable
// ErrNoLiveChild while the child is between spawn and stdin-ready, and the send
// handler acks on accept-into-backlog. So the test must NOT treat the ack as the
// turn — it drains M1/M2 as the real completion signal, and the drain wait absorbs
// the spawn + first-turn latency.
//
// streamWait bounds the whole send_message → ack + turn exchange. Until #2610 the
// test waited 15 s for the ack, then 20 s for the drain, in two loops; under the
// full `make e2e` run the drain once burned its full 20 s. See the single loop
// below for why that was most likely a discarded frame rather than slowness. A
// turn that is never delivered still fails, at streamWait.
func TestRelayV2_StreamSendMessageDrainsTurn(t *testing.T) {
	const (
		initialUUID   = "11111111-1111-4111-8111-111111111111"
		knownConvID   = "22222222-2222-4222-8222-222222222222"
		knownUserText = "e2e-1141-user:ping\n"
		echoNeedle    = "e2e-1141-user:ping"
		sendReqID     = uint64(1141)
		streamWait    = 60 * time.Second
	)

	// Milestone lines and timeout failures carry the elapsed time since here, so a
	// failure shows where the time went rather than one total (#1273's pattern).
	testStart := time.Now()
	elapsed := func() string { return time.Since(testStart).Round(time.Millisecond).String() }

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

	// Await the ack and drain the turn in ONE loop, recording each milestone the
	// moment it arrives in whatever order. The ack is NOT ordered before the turn's
	// frames: the handler enqueues before it acks, and the delta trails the ack by
	// only a few ms alone. A separate ack-first loop that skipped non-ack frames
	// (this test's shape until #2610; #2612's trap) would discard an early delta and
	// leave the drain waiting out its deadline for a frame already consumed.
	// A leading turn_state{responding} precedes the delta; ignore turn_states until
	// sawDelta is set. Read the phone conn serially on the test goroutine (the single
	// reader, same as the interrupt / new_session specs).
	gotAck, sawDelta, sawIdle := false, false, false
	var preAck []string // envelope types seen before the ack, for the failure record
	deadline := time.Now().Add(streamWait)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			if !gotAck {
				t.Fatalf("[t=%s] ack: interactive phone A waited %s after send_message for its ack and never "+
					"received it; the turn was never accepted (frames seen meanwhile: %v)", elapsed(), streamWait, preAck)
			}
			if !sawDelta {
				t.Fatalf("[t=%s] M1: interactive phone A waited %s after send_message for an assistant_delta and "+
					"never observed one; the turn never drained end-to-end. %s\ndaemon stderr tail:\n%s",
					elapsed(), streamWait, drainGateDropDiagnosis(h), stderrTail(h, 4000))
			}
			t.Fatalf("[t=%s] M2: interactive phone A observed the assistant_delta but waited %s after "+
				"send_message without a terminal turn_state{idle}; the turn opened but never closed", elapsed(), streamWait)
		}
		raw, err := phoneA.ReceiveBytes(remaining)
		if err != nil {
			if errors.Is(err, fakephone.ErrReceiveTimeout) {
				continue // deadline reached — re-loop into the milestone-specific t.Fatal above
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
		if !gotAck && env.Type != protocol.TypeAck {
			preAck = append(preAck, string(env.Type))
		}

		switch env.Type {
		case protocol.TypeAck:
			if gotAck {
				continue
			}
			if env.InReplyTo == nil || *env.InReplyTo != sendReqID {
				t.Errorf("ack InReplyTo = %v, want pointer to %d", env.InReplyTo, sendReqID)
			}
			gotAck = true
			t.Logf("[t=%s] ack: interactive phone A received the send_message ack", elapsed())
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
			t.Logf("[t=%s] M1: interactive phone A observed assistant_delta carrying the echoed prompt", elapsed())
		case protocol.TypeTurnState:
			if !sawDelta || sawIdle {
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
			sawIdle = true
			t.Logf("[t=%s] M2: interactive phone A observed terminal turn_state{idle} — the turn closed", elapsed())
		}
		if gotAck && sawIdle {
			return
		}
	}
}

// drainGateDropDiagnosis reads the daemon's captured stderr for the drain gate's
// stream_turn.not_active record — the gate logs it (at Debug; the stream harness
// runs -pyry-verbose) for every event whose producing session is not the active
// conversation's. Its presence is the evidence of a seed UUID mismatch between
// seedBootstrapRegistry and seedBoundConversation; its absence rules that out, so
// the M1 failure names the mismatch only when this check found one.
func drainGateDropDiagnosis(h *Harness) string {
	for _, line := range strings.Split(h.Stderr.String(), "\n") {
		if strings.Contains(line, "stream_turn.not_active") {
			return "The drain gate DROPPED events as not the active session — most likely a UUID mismatch " +
				"between seedBootstrapRegistry and seedBoundConversation. First drop record: " + line
		}
	}
	return "The drain gate logged no stream_turn.not_active drop, so this is not a seed UUID mismatch: " +
		"delivery never reached the child, or the parser / emitter never produced the delta in time."
}

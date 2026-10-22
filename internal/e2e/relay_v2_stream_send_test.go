//go:build e2e

package e2e

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/scenario"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/paireddevice"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// TestRelayV2_StreamSendMessageDrainsTurn drives an encrypted send through the
// spawned daemon and fakeclaude. It requires a correlated acknowledgement,
// attributed source text carrying the exact prompt, matching source completion
// and terminal live idle. Retired content must never arrive on the wire.
// The bootstrap registry and conversation binding identify the same producer.
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
	source := scenario.SourceSince(t, home, knownConvID)
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
	sawEnd := false
	var sourceWatermark uint64
	var sourceTurn string
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
				t.Fatalf("[t=%s] M1: interactive phone A waited %s after send_message for retained assistant text and "+
					"never observed one; the turn never drained end-to-end. %s\ndaemon stderr tail:\n%s",
					elapsed(), streamWait, drainGateDropDiagnosis(h), stderrTail(h, 4000))
			}
			t.Fatalf("[t=%s] M2: interactive phone A observed source text but waited %s after "+
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
		for _, entry := range source(knownConvID) {
			if entry.ID <= sourceWatermark {
				continue
			}
			sourceWatermark = entry.ID
			if entry.Type != protocol.TypeAssistantDelta && entry.Type != protocol.TypeTurnEnd {
				continue
			}
			if entry.Session == nil || entry.Session.SessionID != initialUUID {
				t.Fatal("source lost producing session")
			}
			if entry.Type == protocol.TypeAssistantDelta {
				var d protocol.AssistantDeltaPayload
				if json.Unmarshal(entry.Payload, &d) != nil {
					t.Fatal("invalid assistant source")
				}
				if d.ConversationID != knownConvID || d.ParentToolUseID != "" || d.TurnID == "" {
					t.Fatal("source lost conversation or main-turn attribution")
				}
				if !strings.Contains(d.Text, echoNeedle) {
					t.Fatal("source did not carry echoed prompt")
				}
				if sourceTurn != "" && sourceTurn != d.TurnID {
					t.Fatal("reply crossed source turns")
				}
				sourceTurn, sawDelta = d.TurnID, true
			} else {
				var end struct {
					protocol.TurnEndPayload
					ParentToolUseID string `json:"parent_tool_use_id"`
				}
				if json.Unmarshal(entry.Payload, &end) != nil {
					t.Fatal("invalid terminal source")
				}
				sawEnd = end.ConversationID == knownConvID && end.ParentToolUseID == "" && end.TurnID == sourceTurn
			}
		}
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
		case protocol.TypeAssistantDelta, protocol.TypeToolUse, protocol.TypeToolResult, protocol.TypeToolDenied, protocol.TypeTurnEnd:
			t.Fatal("retired raw content delivered on wire")
		case protocol.TypeTurnState:
			if !sawDelta || !sawEnd || sawIdle {
				continue // the leading responding state precedes source completion
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

// drainGateDropDiagnosis reads the daemon's captured stderr for the drain's
// stream_turn.no_conversation record — the drain logs it (at Debug; the stream
// harness runs -pyry-verbose) for every event whose producing session resolves to
// no conversation (#2739). Its presence is the evidence of a seed UUID mismatch between
// seedBootstrapRegistry and seedBoundConversation; its absence rules that out, so
// the M1 failure names the mismatch only when this check found one.
func drainGateDropDiagnosis(h *Harness) string {
	for _, line := range strings.Split(h.Stderr.String(), "\n") {
		if strings.Contains(line, "stream_turn.no_conversation") {
			return "The drain DROPPED events whose session resolved to no conversation — most likely a UUID mismatch " +
				"between seedBootstrapRegistry and seedBoundConversation. First drop record: " + line
		}
	}
	return "The drain logged no stream_turn.no_conversation drop, so this is not a seed UUID mismatch: " +
		"delivery never reached the child, or the parser / emitter never produced the delta in time."
}

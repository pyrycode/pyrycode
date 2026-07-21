//go:build e2e

package e2e

// Note: msg1Marker / msg2Marker / msg3Marker below are test-only ASCII markers.
// Do NOT paste real secrets into them — they round-trip through the assistant_delta
// text the test decodes and echoes in failure messages.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// TestRelayV2_StreamQueueDrainsInOrder is the stream-json analog of the PTY sibling
// TestRelayV2_QueueDrainsInOrder_AfterBusyTurn (relay_v2_queue_drain_test.go): it
// proves LIVE — over one daemon started under interactive_runner:"stream-json", plus
// a real spawned fakeclaude — that multiple sends submitted while a turn is in flight
// drain to the client in submission order. It extends the first live stream send
// (#1141's TestRelayV2_StreamSendMessageDrainsTurn) from one send to three ordered
// sends behind a startup hold.
//
// WHY A STARTUP HOLD, NOT A msgqueue BACKLOG. On the stream path streamsup.WriteTurn
// (envelope.go) writes the user-turn envelope to the child's held-open stdin and
// returns nil on WRITE — no WaitReady, no commit confirm (contrast the PTY path's
// supervisor.WriteUserTurn). So newInboundDeliver's WriteUserTurn returns on write,
// the msgqueue's serial per-conversation drain empties as fast as it can write bytes
// into the pipe, and a queue_state depth of N is NOT reliably observable here (unlike
// the PTY sibling, whose idle-trigger parks the drain). Submission order is instead
// preserved by a chain of FIFO/serial stages: FIFO drain → FIFO held-open stdin pipe
// → serial claude reader → FIFO stdout → serial parser → serial emitter. To make that
// chain observable, the fakeclaude is held (PYRY_FAKE_CLAUDE_STREAM_HOLD) BEFORE it
// consumes stdin: the child "comes up busy", the daemon buffers all three turns into
// the stdin pipe during the hold, and dropping the trigger lets the child drain them
// FIFO — one clean responding→delta→idle turn cycle per turn, in order. This is the
// faithful stream analog of the PTY sibling's come-up-busy → accumulate → release →
// drain, and it matches real claude (a child slow to start its read loop leaves later
// turns queued in its stdin pipe).
//
// VACUITY GUARD (the headline discipline, per the PTY sibling). The positive gates
// the ordering assertion. (1) All three sends must be ACKED while the child is held
// (in flight) before the trigger drops — a missing ack is the harness-produced-no-
// queue failure mode (enqueue rejected — check the binding, #678), fatal, since the
// ordering assertion would be vacuous without a real backlog. (2) Exactly three
// assistant_deltas must then be observed; fewer is fatal naming the count (the drain
// never completed end-to-end). Only over three real deltas is the submission-order
// assertion non-vacuous.
//
// WHY DELTA ORDER SATISFIES "observes their turn_state transitions in that order".
// Each assistant_delta is emitted strictly inside its own responding→…→turn_end→idle
// cycle (interactive_turn_v2.go; unit-proven in TestStreamTurnDrainV2_FullSingleTurn);
// a fresh chunk after idle opens a NEW turn. So three deltas carrying msg1/msg2/msg3
// in order ⟺ three turn cycles in submission order. The delta marker is the
// unambiguous per-turn oracle — a bare turn_state{responding/idle} carries no text and
// cannot be attributed to a send on its own.
//
// WHY THE IDS MUST LINE UP (same invariant as #1141). The drain gate forwards an event
// to the emitter only when the event's sink tag == activeSession(). The sink tag is
// the runner's construction-time cfg.SessionID = the bootstrap pool id, pinned to
// initialUUID by seedBootstrapRegistry; activeSession() resolves the active
// conversation → knownConvID's binding = initialUUID via seedBoundConversation. A
// mismatched UUID drops every event at the gate and hangs the drain.
func TestRelayV2_StreamQueueDrainsInOrder(t *testing.T) {
	const (
		initialUUID = "11111111-1111-4111-8111-111111111111"
		knownConvID = "33333333-3333-4333-8333-333333333333"
		msg1Marker  = "e2e-1138-msg1"
		msg2Marker  = "e2e-1138-msg2"
		msg3Marker  = "e2e-1138-msg3"
		reqID1      = uint64(11381)
		reqID2      = uint64(11382)
		reqID3      = uint64(11383)
	)

	home := shortHome(t)

	// Pair one interactive device.
	r := RunBareIn(t, home, "pair", "-pyry-name=test", "--name=phone-a")
	if r.ExitCode != 0 {
		t.Fatalf("pyry pair exit=%d\nstdout:\n%s\nstderr:\n%s", r.ExitCode, r.Stdout, r.Stderr)
	}
	payload := decodePairPayload(t, r.Stdout)
	pubKey, err := base64.StdEncoding.DecodeString(payload.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	// Bind knownConvID → the bootstrap session (initialUUID). sessionRouter.Route
	// resolves knownConvID past the #678 empty-CSID guard, and the drain gate's
	// activeSession() == the sink tag initialUUID. Loaded once at startup, so the row
	// must exist BEFORE the daemon starts.
	seedBoundConversation(t, home, knownConvID, initialUUID)

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })

	// The child holds at startup until holdPath appears (not created yet). All three
	// sends buffer in its stdin pipe during the hold; dropping the trigger releases
	// the FIFO drain. No SESSIONS_DIR / STDIN_LOG env — stream mode opens no
	// transcript and this test's oracle is the assistant_delta stream, not a stdin log.
	holdPath := filepath.Join(home, "stream-hold.trig")
	h := StartStreamInteractiveWithRelay(t, home, initialUUID, fr.URL()+"/v2/server",
		"PYRY_FAKE_CLAUDE_STREAM_HOLD="+holdPath)
	t.Cleanup(func() { h.Stop(t) })

	serverID := readPersistedServerID(t, home)
	waitBinaryHello(t, fr, serverID)

	dialCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	phone, err := fakephone.Dial(dialCtx, fr.URL(), serverID, payload.Token, "phone-a")
	if err != nil {
		t.Fatalf("phone dial: %v", err)
	}
	t.Cleanup(func() { _ = phone.Close() })
	// Interactive — the capability the structured stream requires.
	sendCS, recvCS := driveHandshakeToOpenDaemonInteractive(t, phone, pubKey, payload.Token)

	sealSend := func(env protocol.Envelope) {
		t.Helper()
		raw, err := json.Marshal(env)
		if err != nil {
			t.Fatalf("marshal envelope: %v", err)
		}
		ciphertext, err := sendCS.Encrypt(raw)
		if err != nil {
			t.Fatalf("seal envelope: %v", err)
		}
		sendNoiseMsg(t, phone, ciphertext)
	}

	// nextEnv decrypts the next binary→phone application envelope, skipping
	// non-noise_msg inner frames. Every noise_msg is decrypted in capture order so the
	// receive nonce stays in sequence. ok=false on deadline.
	nextEnv := func(deadline time.Time) (protocol.Envelope, bool) {
		t.Helper()
		for {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return protocol.Envelope{}, false
			}
			raw, err := phone.ReceiveBytes(remaining)
			if err != nil {
				if errors.Is(err, fakephone.ErrReceiveTimeout) {
					return protocol.Envelope{}, false
				}
				t.Fatalf("phone receive: %v", err)
			}
			var inner protocol.InnerFrameV2
			if err := json.Unmarshal(raw, &inner); err != nil {
				t.Fatalf("decode inner frame: %v", err)
			}
			if inner.Type != protocol.TypeNoiseMsg {
				continue
			}
			return decryptInnerEnvelope(t, inner, recvCS), true
		}
	}

	// 1. Submit three sends to the SAME conversation, back-to-back, while the child is
	//    held. Same conversation ⟹ one serial per-conversation drain orders them.
	sends := []struct {
		reqID uint64
		msgID string
		text  string
	}{
		{reqID1, "u-1", msg1Marker},
		{reqID2, "u-2", msg2Marker},
		{reqID3, "u-3", msg3Marker},
	}
	for _, s := range sends {
		sealSend(protocol.Envelope{
			ID:      s.reqID,
			Type:    protocol.TypeSendMessage,
			TS:      time.Now().UTC(),
			Payload: mustJSON(t, protocol.SendMessagePayload{ConversationID: knownConvID, MessageID: s.msgID, Text: s.text}),
		})
	}

	// 2. [Vacuity positive — gate before release] Collect all three acks before
	//    touching the trigger. The child is still held, so no assistant_delta /
	//    turn_state frames arrive yet; only acks (and any queue_state) do. A missing
	//    ack is the harness-produced-no-queue mode — fatal, the ordering assertion
	//    below would be vacuous.
	acked := map[uint64]bool{}
	ackDeadline := time.Now().Add(15 * time.Second)
	for len(acked) < len(sends) {
		env, ok := nextEnv(ackDeadline)
		if !ok {
			t.Fatalf("observed only %d/%d send acks before the release deadline; enqueue was rejected "+
				"(check the binding — #678). The queue never populated, so the ordering assertion would be vacuous.",
				len(acked), len(sends))
		}
		if env.Type == protocol.TypeError {
			t.Fatalf("unexpected error envelope while enqueueing sends: %s", string(env.Payload))
		}
		if env.Type != protocol.TypeAck || env.InReplyTo == nil {
			continue
		}
		acked[*env.InReplyTo] = true
	}

	// 3. Release: the child begins consuming stdin and drains the three buffered turns
	//    in FIFO order.
	if err := os.WriteFile(holdPath, []byte("go\n"), 0o600); err != nil {
		t.Fatalf("write stream hold trigger: %v", err)
	}

	// 4. [Ordering assertion → terminal close] Drain the phone stream; collect
	//    assistant_delta texts in arrival order (ignore turn_state / queue_state /
	//    stall). Each delta is emitted inside its own turn cycle, so arrival order IS
	//    submission order. After the third delta, the next turn_state{idle} is the last
	//    turn closing.
	var deltas []string
	drainDeadline := time.Now().Add(20 * time.Second)
	for {
		env, ok := nextEnv(drainDeadline)
		if !ok {
			if len(deltas) < len(sends) {
				t.Fatalf("observed only %d/%d assistant_deltas before the drain deadline; the queued sends never "+
					"drained end-to-end (delivery never reached the child, a UUID mismatch dropped events at the "+
					"drain gate, or the hold never released). deltas so far: %q", len(deltas), len(sends), deltas)
			}
			t.Fatalf("observed all %d assistant_deltas but never a terminal turn_state{idle}; the last turn opened "+
				"but never closed", len(sends))
		}
		switch env.Type {
		case protocol.TypeAssistantDelta:
			var d protocol.AssistantDeltaPayload
			if err := json.Unmarshal(env.Payload, &d); err != nil {
				t.Fatalf("decode assistant_delta payload: %v", err)
			}
			if d.ConversationID != knownConvID {
				t.Errorf("assistant_delta ConversationID = %q, want %q", d.ConversationID, knownConvID)
			}
			deltas = append(deltas, d.Text)
			if len(deltas) == len(sends) {
				// The ordering assertion: the three deltas carry msg1, msg2, msg3 in
				// submission order. Out-of-order is the real failure this test guards
				// (a reordering drain / pipe / emitter).
				for i, want := range []string{msg1Marker, msg2Marker, msg3Marker} {
					if !strings.Contains(deltas[i], want) {
						t.Fatalf("assistant_deltas drained out of submission order: delta[%d]=%q does not contain %q "+
							"(all deltas, in arrival order: %q)", i, deltas[i], want, deltas)
					}
				}
				t.Logf("observed 3 assistant_deltas in submission order: %q", deltas)
			}
		case protocol.TypeTurnState:
			if len(deltas) < len(sends) {
				continue // interleaved responding/idle states precede the final close
			}
			var st protocol.TurnStatePayload
			if err := json.Unmarshal(env.Payload, &st); err != nil {
				t.Fatalf("decode turn_state payload: %v", err)
			}
			if st.State != "idle" {
				continue
			}
			if st.ConversationID != knownConvID {
				t.Errorf("terminal turn_state ConversationID = %q, want %q", st.ConversationID, knownConvID)
			}
			t.Logf("observed terminal turn_state{idle} — the last queued turn closed")
			return
		}
	}
}

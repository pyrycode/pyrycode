//go:build e2e

package e2e

// Note: msg1Marker … msg4Marker below are test-only ASCII markers. Do NOT paste
// real secrets into them — they round-trip through the assistant_delta text and the
// queue_state entries the test decodes and echoes in failure messages.

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

// TestRelayV2_StreamMidTurnHoldDropAndDrainInOrder is the stream-json analog of the
// PTY sibling TestRelayV2_QueueDrainsInOrder_AfterBusyTurn
// (relay_v2_queue_drain_test.go): it proves LIVE — over one daemon started under
// interactive_runner:"stream-json", plus a real spawned fakeclaude — that sends
// submitted while a turn is in flight are HELD in the queue as a client-visible
// backlog, that a held one can still be dropped before it runs, and that the rest
// drain to the client in submission order.
//
// WHAT CHANGED, AND WHY THIS FILE'S OLD RATIONALE IS GONE (#1199). Until this
// ticket, the paragraph here recorded that "a queue_state depth of N is NOT reliably
// observable" on the stream path, because streamsup.WriteTurn (envelope.go) writes
// the user-turn envelope to the child's held-open stdin and returns nil on WRITE —
// no WaitReady, no commit confirm (contrast the PTY path's
// supervisor.WriteUserTurn). The msgqueue drain therefore emptied as fast as it
// could write bytes into the pipe, and the queued-backlog UI and the
// drop-before-drain control were both regressed on the runner the Mac daemon runs in
// production. That record is now FALSE and is rewritten rather than left beside a
// contradicting test: newInboundDeliver holds each delivery on the turn-busy tracker
// (waitIdleForDelivery) and marks the conversation (openForDelivery) before the
// write, so the backlog holds for the whole running turn and the head stays
// droppable throughout it.
//
// WHY THE STARTUP HOLD IS STILL THE FIXTURE. fakeclaude is held
// (PYRY_FAKE_CLAUDE_STREAM_HOLD) BEFORE it consumes stdin, so the child "comes up
// busy". That is what makes the FIRST send's turn a long one with no live claude
// needed — msg1 is written into the stdin pipe and marks the conversation mid-turn,
// and msg2..msg4 then park in the backlog by program order on the drain goroutine,
// not by luck. Dropping the trigger lets the child consume msg1, and from there each
// turn's result line closes the turn and releases exactly one held send. Note the
// consequence for timing: after this ticket the three turns run SEQUENTIALLY (each
// released by the previous turn's TurnEnd) instead of being buffered together in the
// stdin pipe. With fakeclaude that is milliseconds, well inside the existing drain
// deadline.
//
// VACUITY GUARD (the headline discipline, per the PTY sibling). The positive gates
// every assertion. (1) All four sends must be ACKED while the child is held — a
// missing ack is the harness-produced-no-queue failure mode (enqueue rejected —
// check the binding, #678), fatal, since everything below would be vacuous without a
// real backlog. (2) A single queue_state must carry msg2, msg3 AND msg4
// simultaneously; that frame is the mid-turn hold itself, and before this ticket it
// did not reliably exist, since each was written into the pipe microseconds after
// the one before. (3) Exactly three assistant_deltas must then be observed; fewer is
// fatal naming the count (the drain never completed end-to-end), and a fourth — or
// msg4's marker in any of them — is the sharpest failure available: the dropped
// message reached claude.
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
func TestRelayV2_StreamMidTurnHoldDropAndDrainInOrder(t *testing.T) {
	const (
		initialUUID  = "11111111-1111-4111-8111-111111111111"
		knownConvID  = "33333333-3333-4333-8333-333333333333"
		msg1Marker   = "e2e-1138-msg1"
		msg2Marker   = "e2e-1138-msg2"
		msg3Marker   = "e2e-1138-msg3"
		msg4Marker   = "e2e-1199-msg4" // the one dropped before it drains
		reqID1       = uint64(11381)
		reqID2       = uint64(11382)
		reqID3       = uint64(11383)
		reqID4       = uint64(11384)
		dequeueReqID = uint64(11991)
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

	// The child holds at startup until holdPath appears (not created yet), so the
	// first send's turn is a long one and the rest are held in the daemon's backlog;
	// dropping the trigger lets the child start consuming. No SESSIONS_DIR /
	// STDIN_LOG env — stream mode opens no transcript and this test's oracles are the
	// queue_state and assistant_delta streams, not a stdin log.
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

	// 1. Submit four sends to the SAME conversation, back-to-back, while the child is
	//    held. Same conversation ⟹ one serial per-conversation drain orders them, and
	//    is what makes the hold a per-conversation guarantee rather than a race.
	sends := []struct {
		reqID uint64
		msgID string
		text  string
	}{
		{reqID1, "u-1", msg1Marker},
		{reqID2, "u-2", msg2Marker},
		{reqID3, "u-3", msg3Marker},
		{reqID4, "u-4", msg4Marker},
	}
	for _, s := range sends {
		sealSend(protocol.Envelope{
			ID:      s.reqID,
			Type:    protocol.TypeSendMessage,
			TS:      time.Now().UTC(),
			Payload: mustJSON(t, protocol.SendMessagePayload{ConversationID: knownConvID, MessageID: s.msgID, Text: s.text}),
		})
	}

	// 2. [Vacuity positive + AC1 — both gated before release] Collect all four acks
	//    AND the queue_state that carries msg2, msg3 and msg4 together, in ONE loop:
	//    the acks and that frame interleave freely, and a separate ack-first loop
	//    would consume the very frame the hold assertion needs. The child is still
	//    held, so no assistant_delta / turn_state frames arrive yet.
	//
	//    msg1 is the running turn (written into the pipe, conversation marked
	//    mid-turn); the other three can only still be in the backlog because their
	//    deliveries are parked behind it. That is the regression this ticket closes,
	//    and observing it here is what makes every later assertion non-vacuous.
	acked := map[uint64]bool{}
	var msg4QueuedID uint64
	holdDeadline := time.Now().Add(20 * time.Second)
	for len(acked) < len(sends) || msg4QueuedID == 0 {
		env, ok := nextEnv(holdDeadline)
		if !ok {
			if len(acked) < len(sends) {
				t.Fatalf("observed only %d/%d send acks before the release deadline; enqueue was rejected "+
					"(check the binding — #678). The queue never populated, so every assertion below would be vacuous.",
					len(acked), len(sends))
			}
			t.Fatal("all sends acked, but never a queue_state carrying msg2+msg3+msg4 together; the mid-turn hold " +
				"did not engage — the backlog drained on write, so there was never a queued row to render or drop (#1199)")
		}
		if env.Type == protocol.TypeError {
			t.Fatalf("unexpected error envelope while enqueueing sends: %s", string(env.Payload))
		}
		switch env.Type {
		case protocol.TypeAck:
			if env.InReplyTo != nil {
				acked[*env.InReplyTo] = true
			}
		case protocol.TypeQueueState:
			var qs protocol.QueueStatePayload
			if err := json.Unmarshal(env.Payload, &qs); err != nil {
				t.Fatalf("decode queue_state payload: %v", err)
			}
			if qs.ConversationID != knownConvID {
				t.Errorf("queue_state ConversationID = %q, want %q", qs.ConversationID, knownConvID)
				continue
			}
			held := queuedIDsByText(qs)
			if held[msg2Marker] == 0 || held[msg3Marker] == 0 || held[msg4Marker] == 0 {
				continue // an earlier, partial snapshot — the backlog is still filling
			}
			msg4QueuedID = held[msg4Marker]
		}
	}
	t.Logf("observed the mid-turn backlog holding msg2, msg3 and msg4 while msg1's turn runs")

	// 3. [AC2] Drop the LAST held message before it drains. It is not the head, but
	//    the head (msg2) is itself only waiting — the whole point of the hold is that
	//    a queued row stays droppable for the length of the running turn.
	sealSend(protocol.Envelope{
		ID:      dequeueReqID,
		Type:    protocol.TypeDequeueMessage,
		TS:      time.Now().UTC(),
		Payload: mustJSON(t, protocol.DequeueMessagePayload{ConversationID: knownConvID, QueuedMsgID: msg4QueuedID}),
	})

	dropDeadline := time.Now().Add(15 * time.Second)
	for {
		env, ok := nextEnv(dropDeadline)
		if !ok {
			t.Fatal("did not observe a post-dequeue queue_state losing msg4 before the deadline")
		}
		if env.Type == protocol.TypeError {
			t.Fatalf("dequeue_message produced an error envelope: %s", string(env.Payload))
		}
		if env.Type != protocol.TypeQueueState {
			continue
		}
		var qs protocol.QueueStatePayload
		if err := json.Unmarshal(env.Payload, &qs); err != nil {
			t.Fatalf("decode queue_state payload: %v", err)
		}
		if qs.ConversationID != knownConvID {
			continue
		}
		held := queuedIDsByText(qs)
		if held[msg4Marker] != 0 {
			continue // a lingering pre-dequeue snapshot
		}
		if held[msg2Marker] == 0 || held[msg3Marker] == 0 {
			t.Fatalf("the post-dequeue backlog lost more than msg4: %v", qs.Queued)
		}
		break
	}

	// 4. Release: the child begins consuming stdin. From here each turn's result line
	//    closes the turn and releases exactly one held send, so the remaining turns
	//    run sequentially rather than from a pre-filled pipe.
	if err := os.WriteFile(holdPath, []byte("go\n"), 0o600); err != nil {
		t.Fatalf("write stream hold trigger: %v", err)
	}

	// 5. [Ordering + drop terminal] Drain the phone stream; collect assistant_delta
	//    texts in arrival order (ignore turn_state / queue_state / stall). Each delta
	//    is emitted inside its own turn cycle, so arrival order IS submission order.
	//    After the third delta, the next turn_state{idle} is the last turn closing.
	//    msg4 must appear in NO delta and there must be no fourth: that is the
	//    sharpest statement of "the dropped message never reached claude".
	wantDeltas := []string{msg1Marker, msg2Marker, msg3Marker}
	var deltas []string
	drainDeadline := time.Now().Add(20 * time.Second)
	for {
		env, ok := nextEnv(drainDeadline)
		if !ok {
			if len(deltas) < len(wantDeltas) {
				t.Fatalf("observed only %d/%d assistant_deltas before the drain deadline; the queued sends never "+
					"drained end-to-end (delivery never reached the child, a UUID mismatch dropped events at the "+
					"drain gate, or the hold never released). deltas so far: %q", len(deltas), len(wantDeltas), deltas)
			}
			t.Fatalf("observed all %d assistant_deltas but never a terminal turn_state{idle}; the last turn opened "+
				"but never closed", len(wantDeltas))
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
			if strings.Contains(d.Text, msg4Marker) {
				t.Fatalf("an assistant_delta carries the DROPPED message's marker: %q — dequeue_message did not "+
					"stop it reaching claude", d.Text)
			}
			if len(deltas) == len(wantDeltas) {
				t.Fatalf("a fourth assistant_delta arrived (%q) after the three expected turns; the dropped "+
					"message reached claude", d.Text)
			}
			deltas = append(deltas, d.Text)
			if len(deltas) == len(wantDeltas) {
				// The ordering assertion: the three surviving deltas carry msg1, msg2,
				// msg3 in submission order. Out-of-order is the real failure this test
				// guards (a reordering drain / pipe / emitter).
				for i, want := range wantDeltas {
					if !strings.Contains(deltas[i], want) {
						t.Fatalf("assistant_deltas drained out of submission order: delta[%d]=%q does not contain %q "+
							"(all deltas, in arrival order: %q)", i, deltas[i], want, deltas)
					}
				}
				t.Logf("observed 3 assistant_deltas in submission order, with the dropped msg4 absent: %q", deltas)
			}
		case protocol.TypeTurnState:
			if len(deltas) < len(wantDeltas) {
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

// queuedIDsByText indexes a queue_state's backlog by message text, so the test can
// ask "is msg4 still queued, and under which queued_msg_id?" without depending on
// the backlog's length or on any entry's position — both of which shift as the
// drain advances. A zero id means absent; the engine assigns ids from 1
// (msgqueue.convQueue.nextID), so zero is unambiguous.
func queuedIDsByText(qs protocol.QueueStatePayload) map[string]uint64 {
	out := make(map[string]uint64, len(qs.Queued))
	for _, item := range qs.Queued {
		out[item.Text] = item.QueuedMsgID
	}
	return out
}

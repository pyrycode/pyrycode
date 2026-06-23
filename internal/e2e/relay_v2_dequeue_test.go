//go:build e2e

package e2e

// Note: msg1Text / msg2Text below are test-only markers. Do NOT paste real
// secrets into them — they round-trip through the queue_state payload the test
// decodes and echoes in failure messages.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// TestRelayV2_DequeueMessage_RemovesQueuedBeforeDrain is the #723 capstone (the
// original #705 AC-5): with a fake interactive phone and no live claude, it drives
// enqueue → queue_state push → dequeue_message → updated queue_state reflecting
// the removal.
//
// "No live claude": the supervised child is the default /bin/sleep 99999, which
// never commits a turn, so the inbound backlog persists — the FIRST enqueued
// message is the perpetually in-flight (draining) head and is un-removable, while
// later messages stay queued and removable. The test therefore enqueues TWO
// messages and dequeues the NON-head (msg2), asserting the surviving backlog is
// [msg1] in order (AC-1, AC-4, AC-5). The convergence flows through the automatic
// msgqueue OnChange → #722 producer path that Remove fires; the handler never
// re-emits queue_state itself.
func TestRelayV2_DequeueMessage_RemovesQueuedBeforeDrain(t *testing.T) {
	const (
		knownConvID  = "66666666-6666-4666-8666-666666666666"
		initialUUID  = "44444444-4444-4444-8444-444444444444"
		msg1Text     = "e2e-723-msg1\n"
		msg2Text     = "e2e-723-msg2\n"
		reqID1       = uint64(21)
		reqID2       = uint64(22)
		dequeueReqID = uint64(23)
	)

	home := shortHome(t)

	r := RunBareIn(t, home, "pair", "-pyry-name=test", "--name=phone-a")
	if r.ExitCode != 0 {
		t.Fatalf("pyry pair exit=%d\nstdout:\n%s\nstderr:\n%s", r.ExitCode, r.Stdout, r.Stderr)
	}
	payload := decodePairPayload(t, r.Stdout)
	pubKey, err := base64.StdEncoding.DecodeString(payload.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	// Bind knownConvID to the bootstrap session (== initialUUID after
	// reconciliation) so send_message's router.Route resolves and the frame
	// enqueues instead of rejecting pre-enqueue (#678).
	seedBoundConversation(t, home, knownConvID, initialUUID)

	// Pre-create <initialUUID>.jsonl in the daemon's COMPUTED sessions dir so
	// reconcileBootstrapOnNew rotates the bootstrap session id to initialUUID.
	// resolveClaudeSessionsDir has no env override — it always computes
	// <HOME>/.claude/projects/encode(workdir) — so alignment is by construction
	// (HOME=home, -pyry-workdir=home), the rotation-test pattern.
	sessionsDir := filepath.Join(home, ".claude", "projects", encodeWorkdir(home))
	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		t.Fatalf("mkdir sessions dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionsDir, initialUUID+".jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("pre-create initial jsonl: %v", err)
	}

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })

	// No live claude: the default supervised child is /bin/sleep 99999, which
	// never commits a turn, so the inbound backlog persists deterministically.
	h := StartInWithEnv(t, home,
		[]string{"PYRY_ALLOW_INSECURE_RELAY=1", "PYRY_MOBILE_V2=1"},
		"-pyry-relay="+fr.URL()+"/v2/server",
	)
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
	// non-noise_msg inner frames. Every noise_msg is decrypted in capture order so
	// the receive nonce stays in sequence. Returns ok=false on deadline (fakephone
	// closes the WS on a timed-out Receive, so callers use a single deadline with
	// back-to-back reads).
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

	// 1. Enqueue two messages for the bound conversation.
	sealSend(protocol.Envelope{
		ID:      reqID1,
		Type:    protocol.TypeSendMessage,
		TS:      time.Now().UTC(),
		Payload: mustJSON(t, protocol.SendMessagePayload{ConversationID: knownConvID, MessageID: "u-1", Text: msg1Text}),
	})
	sealSend(protocol.Envelope{
		ID:      reqID2,
		Type:    protocol.TypeSendMessage,
		TS:      time.Now().UTC(),
		Payload: mustJSON(t, protocol.SendMessagePayload{ConversationID: knownConvID, MessageID: "u-2", Text: msg2Text}),
	})

	// 2. Drain until a queue_state shows BOTH messages in FIFO order. msg1 never
	//    drains under sleep-claude, so [msg1,msg2] is the stable steady state;
	//    earlier single-item snapshots and the two acks are skipped.
	var bothQueued protocol.QueueStatePayload
	deadline := time.Now().Add(20 * time.Second)
	for {
		env, ok := nextEnv(deadline)
		if !ok {
			t.Fatal("did not observe a queue_state with both messages before deadline (enqueue may have rejected — check the binding)")
		}
		if env.Type == protocol.TypeError {
			t.Fatalf("unexpected error envelope while enqueueing: %s", string(env.Payload))
		}
		if env.Type != protocol.TypeQueueState {
			continue
		}
		var qs protocol.QueueStatePayload
		if err := json.Unmarshal(env.Payload, &qs); err != nil {
			t.Fatalf("decode queue_state payload: %v", err)
		}
		if len(qs.Queued) == 2 {
			bothQueued = qs
			break
		}
	}

	if bothQueued.ConversationID != knownConvID {
		t.Errorf("queue_state ConversationID = %q, want %q", bothQueued.ConversationID, knownConvID)
	}
	if got := bothQueued.Queued[0].Text; got != msg1Text {
		t.Errorf("queued[0].Text = %q, want %q (FIFO head)", got, msg1Text)
	}
	if got := bothQueued.Queued[1].Text; got != msg2Text {
		t.Errorf("queued[1].Text = %q, want %q (FIFO tail)", got, msg2Text)
	}
	msg2ID := bothQueued.Queued[1].QueuedMsgID

	// 3. Dequeue the NON-head message (msg2). The head (msg1) is the in-flight
	//    draining message and would be an un-removable no-op.
	sealSend(protocol.Envelope{
		ID:      dequeueReqID,
		Type:    protocol.TypeDequeueMessage,
		TS:      time.Now().UTC(),
		Payload: mustJSON(t, protocol.DequeueMessagePayload{ConversationID: knownConvID, QueuedMsgID: msg2ID}),
	})

	// 4. Observe an updated queue_state reflecting the removal: [msg1] only, order
	//    preserved. A dequeue_message of a valid request never produces an error
	//    reply (AC-2), so an error envelope here is a failure.
	deadline2 := time.Now().Add(10 * time.Second)
	for {
		env, ok := nextEnv(deadline2)
		if !ok {
			t.Fatal("did not observe the post-dequeue queue_state before deadline")
		}
		if env.Type == protocol.TypeError {
			t.Fatalf("dequeue_message produced an error envelope (AC-2 forbids it): %s", string(env.Payload))
		}
		if env.Type != protocol.TypeQueueState {
			continue
		}
		var qs protocol.QueueStatePayload
		if err := json.Unmarshal(env.Payload, &qs); err != nil {
			t.Fatalf("decode queue_state payload: %v", err)
		}
		if len(qs.Queued) != 1 {
			continue // skip lingering pre-dequeue [msg1,msg2] snapshots
		}
		if qs.Queued[0].QueuedMsgID == msg2ID {
			t.Errorf("post-dequeue backlog still contains msg2 (id=%d)", msg2ID)
		}
		if got := qs.Queued[0].Text; got != msg1Text {
			t.Errorf("post-dequeue queued[0].Text = %q, want %q (msg2 removed, order preserved)", got, msg1Text)
		}
		break
	}
}

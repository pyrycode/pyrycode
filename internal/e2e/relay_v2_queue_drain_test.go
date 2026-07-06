//go:build e2e

package e2e

// Note: msg1Marker / msg2Marker / msg3Marker below are test-only ASCII markers.
// Do NOT paste real secrets into them — they round-trip through the queue_state
// payload the test decodes and echoes in failure messages, and through the
// fakeclaude stdin log (the drain-order oracle) which failures also print.

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

// TestRelayV2_QueueDrainsInOrder_AfterBusyTurn is epic #597's Phase 3 live exit
// gate for the inbound queued-backlog feature (split from #708, mirrors the
// Phase 2 capstone #642 and the #723 deterministic dequeue test). It confirms
// LIVE — over one daemon + a real (fake) claude that goes busy → free — what
// #705/#721/#722/#723 proved deterministically upstream: phone-typed messages
// arriving while claude is busy queue (not interleave), drain to claude in send
// order once the turn frees, and a queued message dropped from the phone via
// dequeue_message never reaches claude while the surviving order holds.
//
// The child is a busy-until-idle-trigger fakeclaude
// (PYRY_FAKE_CLAUDE_IDLE_TRIGGER): it comes up "busy" (no idle glyph, so the
// supervisor's WaitReady blocks), the drain parks on the head m1, and the
// backlog accumulates. Dropping the trigger flips it idle once, and the
// survivors drain back-to-back. This is what #723's /bin/sleep child could not
// do (never idle → never drains): #723 proves enqueue + dequeue-of-a-non-head;
// #792 proves the drain.
//
// Vacuous-pass guard (the headline requirement): the two POSITIVES gate the
// NEGATIVE. (1) the queue must be observed populated to all three in FIFO before
// the drop, with a t.Fatal on the harness-produced-no-queue mode; (2) the
// survivors (m1, m3) must be observed REACHING claude in order (stdin log,
// Index(m1) < Index(m3)) BEFORE the "m2 absent" negative is evaluated, again a
// t.Fatal on absence. A "dropped message absent" over a queue that never
// populated or never drained cannot pass vacuously.
//
// AC4 (flight recorder) is scoped out of this CI test: the recorder lives in
// internal/agentrun/ptyrunner and is unreachable from the supervisor-hosted
// mobile-relay path (verified: no PYRY_RECORD_DIR/RecordTo wiring on that path;
// same disposition as sibling #791 AC5). It is deferred to the operator live
// run. This test asserts AC1-AC3.
func TestRelayV2_QueueDrainsInOrder_AfterBusyTurn(t *testing.T) {
	const (
		knownConvID  = "77777777-7777-4777-8777-777777777777"
		initialUUID  = "44444444-4444-4444-8444-444444444444"
		msg1Marker   = "e2e-792-msg1"
		msg2Marker   = "e2e-792-msg2"
		msg3Marker   = "e2e-792-msg3"
		reqID1       = uint64(31)
		reqID2       = uint64(32)
		reqID3       = uint64(33)
		dequeueReqID = uint64(34)
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
	// reconciliation) so send_message's router.Route resolves and the frames
	// enqueue instead of rejecting pre-enqueue (#678).
	seedBoundConversation(t, home, knownConvID, initialUUID)

	// Pre-create <initialUUID>.jsonl in the daemon's COMPUTED sessions dir so
	// reconcileBootstrapOnNew rotates the bootstrap session id to initialUUID.
	// Alignment is by construction (HOME=home, -pyry-workdir=home), the
	// rotation-test pattern. StartRotationWithRelay also MkdirAll's this dir; we
	// do it first so the pre-created jsonl exists BEFORE the daemon starts.
	sessionsDir := filepath.Join(home, ".claude", "projects", encodeWorkdir(home))
	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		t.Fatalf("mkdir sessions dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionsDir, initialUUID+".jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("pre-create initial jsonl: %v", err)
	}

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })

	// Distinct test-owned paths under home. neverRotate is never created (this
	// test does not exercise claude's /clear rotation); idleTrig is the busy→free
	// signal the test drops to release claude; stdinLog is the drain-order oracle
	// (StartRotationWithRelay sets PYRY_FAKE_CLAUDE_STDIN_LOG to it).
	neverRotate := filepath.Join(home, "never-rotate.trig")
	idleTrig := filepath.Join(home, "idle.trig")
	stdinLog := filepath.Join(home, "stdin.log")

	// Busy fakeclaude: PYRY_FAKE_CLAUDE_IDLE_TRIGGER makes it come up busy (no
	// idle glyph → WaitReady blocks) until idleTrig appears; PYRY_MOBILE_V2=1
	// selects the v2 relay path. StartRotationWithRelay supplies the sessions-dir
	// / initial-uuid / stdin-log / relay wiring and PYRY_ALLOW_INSECURE_RELAY.
	// It self-registers the daemon teardown (sync.Once), so no explicit Stop.
	StartRotationWithRelay(t, home, sessionsDir, initialUUID, neverRotate, stdinLog,
		fr.URL()+"/v2/server",
		"PYRY_MOBILE_V2=1",
		"PYRY_FAKE_CLAUDE_IDLE_TRIGGER="+idleTrig,
	)

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

	// 1. Enqueue three messages while claude is busy. Each acks immediately (#721
	//    enqueue-and-ack); the drain picks m1 → WriteUserTurn(m1) → WaitReady
	//    blocks (busy). m1 is the in-flight (draining) head; m2, m3 stay queued at
	//    idx 1, 2.
	sealSend(protocol.Envelope{
		ID:      reqID1,
		Type:    protocol.TypeSendMessage,
		TS:      time.Now().UTC(),
		Payload: mustJSON(t, protocol.SendMessagePayload{ConversationID: knownConvID, MessageID: "u-1", Text: msg1Marker}),
	})
	sealSend(protocol.Envelope{
		ID:      reqID2,
		Type:    protocol.TypeSendMessage,
		TS:      time.Now().UTC(),
		Payload: mustJSON(t, protocol.SendMessagePayload{ConversationID: knownConvID, MessageID: "u-2", Text: msg2Marker}),
	})
	sealSend(protocol.Envelope{
		ID:      reqID3,
		Type:    protocol.TypeSendMessage,
		TS:      time.Now().UTC(),
		Payload: mustJSON(t, protocol.SendMessagePayload{ConversationID: knownConvID, MessageID: "u-3", Text: msg3Marker}),
	})

	// 2. [Vacuous-pass positive #1] Drain queue_state until all three are queued
	//    in FIFO order. Earlier single/double snapshots and the three acks are
	//    skipped. A miss before the deadline is the harness-produced-no-queue
	//    failure mode — fatal, since the m2-absent negative would be vacuous
	//    without a real backlog.
	var allQueued protocol.QueueStatePayload
	deadline := time.Now().Add(20 * time.Second)
	for {
		env, ok := nextEnv(deadline)
		if !ok {
			t.Fatal("did not observe a queue_state with all three messages before deadline (harness produced no queue — enqueue may have rejected; check the binding)")
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
		if len(qs.Queued) == 3 {
			allQueued = qs
			break
		}
	}
	if allQueued.ConversationID != knownConvID {
		t.Errorf("queue_state ConversationID = %q, want %q", allQueued.ConversationID, knownConvID)
	}
	if got := allQueued.Queued[0].Text; got != msg1Marker {
		t.Errorf("queued[0].Text = %q, want %q (FIFO head)", got, msg1Marker)
	}
	if got := allQueued.Queued[1].Text; got != msg2Marker {
		t.Errorf("queued[1].Text = %q, want %q", got, msg2Marker)
	}
	if got := allQueued.Queued[2].Text; got != msg3Marker {
		t.Errorf("queued[2].Text = %q, want %q (FIFO tail)", got, msg3Marker)
	}
	msg2ID := allQueued.Queued[1].QueuedMsgID

	// AC1: nothing delivered to claude during the busy window — the stdin log is
	// still empty. m1 is parked in WaitReady before DeliverPrompt (delivery is
	// gated on claude reaching idle), so no PTY write happens until the trigger
	// drops. A non-existent log file is equivalent to empty.
	if data, err := os.ReadFile(stdinLog); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read stdin log during busy window: %v", err)
		}
	} else if len(data) != 0 {
		t.Fatalf("stdin log is non-empty during the busy window — a message was delivered mid-turn (AC1 violated): %q", data)
	}

	// 3. Drop the middle message (m2, idx 1 — removable; the head m1 is the
	//    in-flight draining message and would be an un-removable no-op).
	sealSend(protocol.Envelope{
		ID:      dequeueReqID,
		Type:    protocol.TypeDequeueMessage,
		TS:      time.Now().UTC(),
		Payload: mustJSON(t, protocol.DequeueMessagePayload{ConversationID: knownConvID, QueuedMsgID: msg2ID}),
	})

	// 4. Observe queue_state converge to [m1, m3] (len 2, m2 absent, order
	//    preserved). A dequeue_message of a valid request never produces an error
	//    reply (#723 AC-2), so an error envelope here is a failure.
	deadline2 := time.Now().Add(10 * time.Second)
	for {
		env, ok := nextEnv(deadline2)
		if !ok {
			t.Fatal("did not observe the post-dequeue queue_state ([m1,m3]) before deadline")
		}
		if env.Type == protocol.TypeError {
			t.Fatalf("dequeue_message produced an error envelope (a valid dequeue must not): %s", string(env.Payload))
		}
		if env.Type != protocol.TypeQueueState {
			continue
		}
		var qs protocol.QueueStatePayload
		if err := json.Unmarshal(env.Payload, &qs); err != nil {
			t.Fatalf("decode queue_state payload: %v", err)
		}
		if len(qs.Queued) != 2 {
			continue // skip the lingering pre-dequeue [m1,m2,m3] snapshot
		}
		if qs.Queued[0].QueuedMsgID == msg2ID || qs.Queued[1].QueuedMsgID == msg2ID {
			t.Errorf("post-dequeue backlog still contains dropped msg2 (id=%d)", msg2ID)
		}
		if g0, g1 := qs.Queued[0].Text, qs.Queued[1].Text; g0 != msg1Marker || g1 != msg3Marker {
			t.Errorf("post-dequeue backlog = [%q, %q], want [%q, %q] (m2 removed, order preserved)", g0, g1, msg1Marker, msg3Marker)
		}
		break
	}

	// 5. Free claude: dropping idleTrig makes fakeclaude emit the idle glyph once
	//    → WaitReady returns → m1 delivers + commits (transcript growth) → the
	//    drain advances → m3 delivers + commits. Drain queue_state until empty.
	if err := os.WriteFile(idleTrig, []byte("go\n"), 0o600); err != nil {
		t.Fatalf("write idle trigger: %v", err)
	}
	deadline3 := time.Now().Add(20 * time.Second)
	for {
		env, ok := nextEnv(deadline3)
		if !ok {
			t.Fatal("did not observe the drained (empty) queue_state before deadline (claude never idled / commit never confirmed)")
		}
		if env.Type == protocol.TypeError {
			t.Fatalf("unexpected error envelope during drain: %s", string(env.Payload))
		}
		if env.Type != protocol.TypeQueueState {
			continue
		}
		var qs protocol.QueueStatePayload
		if err := json.Unmarshal(env.Payload, &qs); err != nil {
			t.Fatalf("decode queue_state payload: %v", err)
		}
		if len(qs.Queued) == 0 {
			break
		}
	}

	// 6. [Vacuous-pass positive #2 → then the negative] The empty queue_state is a
	//    happens-after fence: an item leaves the backlog only on a confirmed
	//    commit, and a commit happens only after DeliverPrompt wrote the prompt to
	//    the PTY (fsynced into the stdin log). So both surviving prompts are on
	//    disk now — assert the drain reached claude IN ORDER first (this gates the
	//    negative), then assert the drop held.
	logBytes, err := os.ReadFile(stdinLog)
	if err != nil {
		t.Fatalf("read stdin log after drain: %v", err)
	}
	log := string(logBytes)

	i1 := strings.Index(log, msg1Marker)
	i3 := strings.Index(log, msg3Marker)
	if i1 < 0 || i3 < 0 {
		t.Fatalf("survivors did not both reach claude (drain did not run): Index(%q)=%d Index(%q)=%d — the msg2-absent negative would be vacuous.\nstdin log: %q",
			msg1Marker, i1, msg3Marker, i3, log)
	}
	if i1 >= i3 {
		t.Fatalf("survivors reached claude out of send order: Index(%q)=%d not before Index(%q)=%d (AC2).\nstdin log: %q",
			msg1Marker, i1, msg3Marker, i3, log)
	}

	// AC3: the dropped m2 never reached claude.
	if strings.Contains(log, msg2Marker) {
		t.Errorf("dropped msg2 reached claude (AC3 violated): stdin log contains %q.\nstdin log: %q", msg2Marker, log)
	}
}

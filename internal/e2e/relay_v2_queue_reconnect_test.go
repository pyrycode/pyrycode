//go:build e2e

package e2e

// This file is the cross-layer proof for the queue half of the reconnect-reconcile
// contract (ADR 025 → "Backpressure / replay"), split from #829 (its modal twin is
// #903). It exercises the producer wired by #878 (connect-time queue reconcile) end
// to end over one spawned daemon + Noise transport + relay routing + a busy
// fakeclaude: a non-empty message backlog that was populated while a client was
// disconnected is re-sent as a single queue_state snapshot, reflecting current
// backlog truth (same queued_msg_id set), exactly once when a fake client
// (re)connects — and reconnecting AGAIN re-sends the same snapshot idempotently, with
// no duplication and no corruption. Losing or duplicating a queued message on
// reconnect is a no-data-loss regression, so the exactly-once negatives are gated
// behind the proven positives (#792's "positives gate the negative" doctrine).
//
// Reconnect is composed, not a fakephone helper: ps.phone.Close() then a fresh
// fakephone.Dial + re-handshake with the SAME token — a brand-new conn_id + fresh
// Noise CipherStates. That is the only reconnect path fakephone/fakerelay can model
// (fresh conn_id per client upgrade, no 30s grace), which is exactly #878's
// brand-new-conn mechanism (reconcileQueues fires on handleNoiseInit's success tail).
// AC1 names two reconnect modes — within the relay's 30s grace (same session) and
// after it (fresh attach) — but over fakerelay they COLLAPSE to the single
// fresh-attach path: fakerelay has no 30s grace and mints a fresh conn_id per client
// upgrade, so Close + fresh Dial + re-handshake is inherently a brand-new conn. The
// literal within-grace same-session mode is #874/#875's surviving-conn hold/flush
// (the daemon–relay link stays up throughout this test; only the client WS blips),
// unreachable by this harness and owned by those tickets. We write the one
// fresh-attach scenario and document the collapse here rather than fabricate a
// cosmetic "within grace" subtest over a graceless harness.
//
// Isolation that makes the whole test non-vacuous: phone #2 / #3 connect strictly
// AFTER the last backlog change (the enqueues on phone #1's watch). The #722
// on-change producer emits queue_state only on queue-content change and broadcasts to
// conns open at change time; a brand-new conn joining with no subsequent change gets
// nothing from it — so reconcileQueues (#878) is the SOLE possible queue_state
// delivery path to phone #2 / #3, and a green assertion proves reconcile, not an
// accidental re-broadcast. queue_state also carries EventID == nil (a control event,
// never in the #647/#777 turn-event replay ring) and the hello advertises no replay
// cursor, so replay cannot re-deliver it either.
//
// The backlog is held stable by a busy fakeclaude (PYRY_FAKE_CLAUDE_IDLE_TRIGGER
// pointing at a file we NEVER create): claude comes up busy (WaitReady blocks) and
// stays busy for the whole test, so nothing drains to the PTY and the backlog is
// frozen across every disconnect/reconnect. Markers are test-only ASCII (they
// round-trip through the queue_state payload the test echoes in failure messages). Do
// NOT paste secrets.

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

// reconnectQueueHarness holds the reconnect ingredients: a running daemon + relay
// with a paired phone-a token, plus stdinLog (the never-drained fence — a busy claude
// never delivers to the PTY, so it stays empty). It deliberately does NOT dial a
// phone: the test owns connect / disconnect / reconnect via openInteractiveQueuePhone
// so phone #1, #2 and #3 are all minted with the SAME token. Named queue-specific (the
// deliberate-duplication house pattern of bringUpTwoHeadModalHarness) so this file
// stands alone with no shared-harness edit and no symbol collision with the modal twin.
type reconnectQueueHarness struct {
	fr       *fakerelay.Server
	serverID string
	pubKey   []byte
	token    string // the paired phone-a token; reused verbatim on reconnect
	stdinLog string // never-drained fence: empty for the whole busy run
}

// queuePhoneSession is one interactive conn bound to the daemon over a fresh Noise
// session, plus its sealed-send + single-deadline decrypt-drain closures. A reconnect
// mints a new queuePhoneSession over a brand-new conn_id + fresh CipherStates.
type queuePhoneSession struct {
	phone    *fakephone.Client
	sealSend func(env protocol.Envelope)
	nextEnv  func(deadline time.Time) (protocol.Envelope, bool)
}

// bringUpReconnectQueueHarness spawns the daemon + busy fakeclaude and pairs phone-a,
// mirroring relay_v2_queue_drain_test.go's bootstrap up to (and including)
// waitBinaryHello but stopping short of the dial — the frozen harness stays untouched.
// Pairs WITHOUT --allow-remote-permissions (there is no answer path here, only
// send_message enqueue + queue_state observation), seeds knownConvID so send_message
// routes and frames enqueue, aligns the sessions dir + pre-creates <initialUUID>.jsonl,
// and starts a busy fakeclaude via PYRY_FAKE_CLAUDE_IDLE_TRIGGER pointing at a trigger
// file this test never creates (so claude stays busy and the backlog is frozen).
func bringUpReconnectQueueHarness(t *testing.T) *reconnectQueueHarness {
	t.Helper()
	const initialUUID = "44444444-4444-4444-8444-444444444444"

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

	// Bind knownConvID to the bootstrap session (== initialUUID after reconciliation)
	// so send_message's router.Route resolves and the frames enqueue instead of
	// rejecting pre-enqueue (#678).
	seedBoundConversation(t, home, knownQueueConvID, initialUUID)

	// Pre-create <initialUUID>.jsonl in the daemon's COMPUTED sessions dir so
	// reconcileBootstrapOnNew rotates the bootstrap session id to initialUUID.
	// Alignment is by construction (HOME=home, -pyry-workdir=home), the rotation-test
	// pattern; do it BEFORE the daemon starts.
	sessionsDir := claudeSessionsDir(home)
	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		t.Fatalf("mkdir sessions dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionsDir, initialUUID+".jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("pre-create initial jsonl: %v", err)
	}

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })

	// neverRotate is never created (no /clear rotation here); idleTrig is the busy→free
	// signal we DELIBERATELY never drop, so claude stays busy and the backlog is frozen
	// for the whole test; stdinLog is the never-drained fence (stays empty on the busy
	// path). StartRotationWithRelay sets PYRY_FAKE_CLAUDE_STDIN_LOG to stdinLog.
	neverRotate := filepath.Join(home, "never-rotate.trig")
	idleTrig := filepath.Join(home, "idle.trig")
	stdinLog := filepath.Join(home, "stdin.log")

	// Busy fakeclaude: PYRY_FAKE_CLAUDE_IDLE_TRIGGER makes it come up busy (no idle
	// glyph → WaitReady blocks) until idleTrig appears — and it never does.
	// PYRY_MOBILE_V2=1 selects the v2 relay path. StartRotationWithRelay supplies the
	// sessions-dir / initial-uuid / stdin-log / relay wiring + PYRY_ALLOW_INSECURE_RELAY
	// and self-registers the daemon teardown (sync.Once), so no explicit Stop.
	StartRotationWithRelay(t, home, sessionsDir, initialUUID, neverRotate, stdinLog,
		fr.URL()+"/v2/server",
		"PYRY_MOBILE_V2=1",
		"PYRY_FAKE_CLAUDE_IDLE_TRIGGER="+idleTrig,
	)

	serverID := readPersistedServerID(t, home)
	waitBinaryHello(t, fr, serverID)

	return &reconnectQueueHarness{
		fr:       fr,
		serverID: serverID,
		pubKey:   pubKey,
		token:    payload.Token,
		stdinLog: stdinLog,
	}
}

// openInteractiveQueuePhone dials a fresh phone, drives the interactive Noise
// handshake with h.token, and returns the bound session. Reconnect is:
//
//	old.phone.Close(); ps := openInteractiveQueuePhone(t, h, "phone-a")
//
// with the SAME token — a brand-new conn_id + fresh CipherStates, which is exactly the
// fresh-attach path #878's reconcileQueues fires on. The sealSend/nextEnv bodies are
// copied verbatim from relay_v2_queue_drain_test.go (frozen and correct): every
// noise_msg is decrypted in capture order so the receive nonce stays in sequence, and
// every wait is a single long deadline with back-to-back reads — never a short poll (a
// timed-out fakephone Receive closes the WS and makes the conn unusable).
func openInteractiveQueuePhone(t *testing.T, h *reconnectQueueHarness, deviceName string) *queuePhoneSession {
	t.Helper()

	dialCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	phone, err := fakephone.Dial(dialCtx, h.fr.URL(), h.serverID, h.token, deviceName)
	if err != nil {
		t.Fatalf("phone dial: %v", err)
	}
	t.Cleanup(func() { _ = phone.Close() })

	sendCS, recvCS := driveHandshakeToOpenDaemonInteractive(t, phone, h.pubKey, h.token)

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

	return &queuePhoneSession{phone: phone, sealSend: sealSend, nextEnv: nextEnv}
}

// assertQueueStateFor drains ps under one 20 s deadline until a queue_state for
// knownQueueConvID whose backlog matches wantTexts (FIFO order) arrives, skipping
// intermediate partial snapshots (the #722 on-change producer emits single/partial
// snapshots as each enqueue lands) and any acks. It t.Fatals on the deadline (the
// harness-produced-no-queue / reconcile-delivered-nothing vacuous-pass guard) and on
// an intervening TypeError, and returns the decoded payload so the caller can
// capture/compare the true queued_msg_ids (which the daemon mints — the test learns
// them here, it does not guess). want is the FIFO markers only, not ids: at the
// populate fence the ids are unknown, so the id-equality check lives in the caller,
// comparing against the captured originals.
func assertQueueStateFor(t *testing.T, ps *queuePhoneSession, wantTexts []string) protocol.QueueStatePayload {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		env, ok := ps.nextEnv(deadline)
		if !ok {
			t.Fatal("did not observe a queue_state with the expected backlog before deadline (phone #1: enqueue may have rejected — check the binding; phone #2/#3: reconcileQueues delivered nothing on the fresh interactive handshake)")
		}
		if env.Type == protocol.TypeError {
			t.Fatalf("unexpected error envelope while awaiting queue_state: %s", string(env.Payload))
		}
		if env.Type != protocol.TypeQueueState {
			continue
		}
		var qs protocol.QueueStatePayload
		if err := json.Unmarshal(env.Payload, &qs); err != nil {
			t.Fatalf("decode queue_state payload: %v", err)
		}
		if len(qs.Queued) != len(wantTexts) {
			continue // skip a partial (single/double) snapshot from the on-change producer
		}
		if qs.ConversationID != knownQueueConvID {
			t.Fatalf("queue_state ConversationID = %q, want %q", qs.ConversationID, knownQueueConvID)
		}
		for i, want := range wantTexts {
			if got := qs.Queued[i].Text; got != want {
				t.Fatalf("queue_state Queued[%d].Text = %q, want %q (FIFO order preserved)", i, got, want)
			}
		}
		return qs
	}
}

// assertNoSecondQueueState drains ps for `within` and t.Fatals on a second queue_state
// or any TypeError; a clean timeout is the pass (exactly-once). ps is discarded right
// after each call, so the timeout-closes-the-WS side effect is harmless.
func assertNoSecondQueueState(t *testing.T, ps *queuePhoneSession, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		env, ok := ps.nextEnv(deadline)
		if !ok {
			return // drained — no second queue_state (the expected exactly-once outcome)
		}
		if env.Type == protocol.TypeQueueState {
			t.Fatalf("a SECOND queue_state arrived after the reconciled snapshot (exactly-once violated — reconcile must unicast once, and no #722 on-change broadcast can reach a conn that joined after the last backlog change): %s", string(env.Payload))
		}
		if env.Type == protocol.TypeError {
			t.Fatalf("unexpected error envelope during the no-second-queue_state drain: %s", string(env.Payload))
		}
	}
}

// Shared fixtures for the queue reconnect test. knownQueueConvID binds the backlog to
// the bootstrap conversation; the markers are test-only ASCII (see the file header).
const (
	knownQueueConvID = "77777777-7777-4777-8777-777777777777"
	queueMsg1Marker  = "e2e-904-msg1"
	queueMsg2Marker  = "e2e-904-msg2"
	queueReqID1      = uint64(31)
	queueReqID2      = uint64(32)
)

// TestRelayV2_QueueReconcileOnReconnect proves the queue reconnect-reconcile contract
// end to end: a non-empty backlog populated while a client was disconnected is re-sent
// as a queue_state snapshot exactly once, reflecting current backlog truth (same
// queued_msg_id set), when a fake client reconnects (AC1) — and reconnecting again
// re-sends the SAME snapshot idempotently, no duplication or corruption (AC2). AC3 is
// the whole file (daemon-layer e2e over the fake client, //go:build e2e).
//
// Assertion order is load-bearing: each step is a hard precondition of the next, so no
// downstream exactly-once / current-truth check can pass vacuously.
func TestRelayV2_QueueReconcileOnReconnect(t *testing.T) {
	h := bringUpReconnectQueueHarness(t)
	want := []string{queueMsg1Marker, queueMsg2Marker}

	// 1. Populate the backlog (phone #1). Send two send_message envelopes while claude
	//    is busy: each acks immediately (#721 enqueue-and-ack); the drain picks the head
	//    m1 → WaitReady blocks (busy), so m1 is the in-flight head and m2 stays queued —
	//    both remain in the backlog for the whole busy run.
	ps1 := openInteractiveQueuePhone(t, h, "phone-a")
	ps1.sealSend(protocol.Envelope{
		ID:      queueReqID1,
		Type:    protocol.TypeSendMessage,
		TS:      time.Now().UTC(),
		Payload: mustJSON(t, protocol.SendMessagePayload{ConversationID: knownQueueConvID, MessageID: "u-1", Text: queueMsg1Marker}),
	})
	ps1.sealSend(protocol.Envelope{
		ID:      queueReqID2,
		Type:    protocol.TypeSendMessage,
		TS:      time.Now().UTC(),
		Payload: mustJSON(t, protocol.SendMessagePayload{ConversationID: knownQueueConvID, MessageID: "u-2", Text: queueMsg2Marker}),
	})

	// 2. Fence + capture the true ids. This proves the backlog genuinely exists (a
	//    reconcile over an empty/absent backlog could not pass here) and is the source
	//    of the true queued_msg_ids the reconcile must reproduce — the daemon mints
	//    them; the test learns them now, it does not guess.
	original := assertQueueStateFor(t, ps1, want)

	// 3. Disconnect. The backlog is now present while NO client is connected: a phone
	//    disconnect resolves nothing for the queue, and claude is still busy, so the
	//    backlog is unchanged.
	if err := ps1.phone.Close(); err != nil {
		t.Fatalf("close phone #1: %v", err)
	}

	// 4. Reconnect (fresh attach — the mode under test). Same token → brand-new conn_id
	//    + fresh Noise session → handleNoiseInit success tail → reconcileQueues. ps2
	//    connects strictly AFTER the last backlog change, so no #722 on-change broadcast
	//    can have reached it: reconcile is its SOLE possible delivery path.
	ps2 := openInteractiveQueuePhone(t, h, "phone-a")

	// 5. Reconciled exactly once, current truth (AC1). The reconciled snapshot must
	//    carry the full backlog with the SAME queued_msg_id set (not a re-mint, not a
	//    stale/empty view), then a bounded "no second queue_state" drain proves
	//    exactly-once.
	reconciled := assertQueueStateFor(t, ps2, want)
	for i := range original.Queued {
		if reconciled.Queued[i].QueuedMsgID != original.Queued[i].QueuedMsgID {
			t.Fatalf("reconciled queue_state Queued[%d].QueuedMsgID = %d, want %d (a re-mint or a stale view, not a re-send of current backlog truth — AC1)",
				i, reconciled.Queued[i].QueuedMsgID, original.Queued[i].QueuedMsgID)
		}
	}
	assertNoSecondQueueState(t, ps2, 3*time.Second)

	// 6. Idempotent re-send (AC2). Reconnect a SECOND time; the reconciled snapshot must
	//    equal the original element-for-element by queued_msg_id + text + order —
	//    reconnecting again re-sends the SAME snapshot, no duplication, no corruption
	//    (match-and-replace by stable id applied idempotently). Same bounded
	//    exactly-once drain.
	if err := ps2.phone.Close(); err != nil {
		t.Fatalf("close phone #2: %v", err)
	}
	ps3 := openInteractiveQueuePhone(t, h, "phone-a")
	again := assertQueueStateFor(t, ps3, want)
	for i := range original.Queued {
		if again.Queued[i].QueuedMsgID != original.Queued[i].QueuedMsgID || again.Queued[i].Text != original.Queued[i].Text {
			t.Fatalf("second reconnect's queue_state Queued[%d] = {id=%d text=%q}, want {id=%d text=%q} (reconnecting again must re-send the SAME snapshot — AC2 idempotency)",
				i, again.Queued[i].QueuedMsgID, again.Queued[i].Text, original.Queued[i].QueuedMsgID, original.Queued[i].Text)
		}
	}
	assertNoSecondQueueState(t, ps3, 3*time.Second)

	// 7. Never-drained fence. Claude never idled (WaitReady blocked the whole run), so
	//    nothing was delivered to the PTY: the stdin log is empty/absent. This confirms
	//    the reconciled backlog is genuinely "missed while disconnected", not one that
	//    partially drained. A non-existent log file is equivalent to empty.
	if data, err := os.ReadFile(h.stdinLog); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read stdin log after the run: %v", err)
		}
	} else if len(data) != 0 {
		t.Fatalf("stdin log is non-empty — a queued message was delivered to claude despite the busy hold (the backlog was not genuinely held across the reconnect): %q", data)
	}
}

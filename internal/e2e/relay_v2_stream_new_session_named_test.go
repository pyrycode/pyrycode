//go:build e2e

package e2e

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
	"github.com/pyrycode/pyrycode/internal/e2e/internal/paireddevice"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// TestRelayV2_StreamNewSessionNamedConversationRotatesThatOne is #2099's
// fake-daemon proof: with the daemon's current-conversation cursor parked on A, a
// new_session naming B rotates B and leaves A's child alone.
//
// THE SEQUENCE IS THE TEST, and each step exists to defeat a way this could pass
// while broken:
//
//	M1   send to A          — A's child is live and the cursor is stamped to A.
//	M2   create B           — since #2085 create_conversation binds B's session but
//	                          does NOT spawn its child.
//	M2.5 new_session for B  — AC-4's fourth row: the frame under test, sent while B
//	                          has no live child. Must be inert.
//	M3   send to B          — brings B's child up. Without it B would be bound with
//	                          no live child, the named rotation would be inert, and
//	                          every assertion below would pass vacuously.
//	M4   send to A again    — moves the cursor BACK to A. Without it the cursor is
//	                          already on B and a daemon that ignored the named id
//	                          would rotate B anyway, which is the defect passing.
//	M5   new_session for B  — the frame under test, now that B can be acted on.
//
// M2.5 and M5 send the SAME frame naming the SAME conversation, with M3 as the only
// variable between them, and each one's verdict is what makes the other's mean
// something: M5's rotation cannot be an unconditional one, because the identical
// frame changed nothing at M2.5, and M2.5's refusal cannot be a dead wire, because
// the identical frame rotates at M5.
//
// The two assertions are a matched pair and neither alone would do. B's
// session_transition carrying B's conversation id proves the NAMED conversation
// rotated (AC-1's actuation, AC-2's fan-out — it reaches this phone, which is also
// the one that sent the frame, and the emitter resolves the id from the new session
// rather than from any cursor). A's registry id still reading initialUUID proves the
// cursor's conversation was NOT touched, which is the whole defect: before #2099
// this frame rotated A.
func TestRelayV2_StreamNewSessionNamedConversationRotatesThatOne(t *testing.T) {
	const (
		initialUUID = "11111111-1111-4111-8111-111111111111" // bootstrap, bound to conversation A
		convA       = "22222222-2222-4222-8222-222222222222"
		textToA     = "e2e-2099-user:a\n"
		needleToA   = "e2e-2099-user:a"
		textToB     = "e2e-2099-user:b\n"
		needleToB   = "e2e-2099-user:b"
		textBackToA = "e2e-2099-user:a-again\n"

		sendToAReqID     = uint64(2099)
		createBReqID     = uint64(2100)
		sendToBReqID     = uint64(2101)
		sendBackToAReqID = uint64(2102)
		inertReqID       = uint64(2103)
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
	// daemon loads conversations.json once at startup. A's session id is therefore
	// the registry's bootstrap entry, which is what the final assertion reads.
	seedBoundConversation(t, home, convA, initialUUID)

	regPath := filepath.Join(home, ".pyry", "test", "sessions.json")

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
	// echoing it. The echo — not the ack — is what proves the conversation's child is
	// live and serving, which is exactly the precondition a vacuous pass would lack.
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
					"conversation's child never served the turn, so the rotation under test would be "+
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

	// --- M1: A's child is live and the cursor is stamped to A.
	sendAndAwaitEcho("M1", sendToAReqID, convA, "u-a1", textToA, needleToA)
	t.Logf("[t=%s] M1: conversation A is live and the cursor is stamped to A", elapsed())

	// --- M2: mint conversation B over the wire. All-null create_conversation
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
			t.Fatal("M2: did not receive conversation_created before deadline")
		}
		if env.Type == protocol.TypeError {
			t.Fatalf("M2: unexpected error envelope while awaiting conversation_created: %s", string(env.Payload))
		}
		if env.Type != protocol.TypeConversationCreated {
			continue
		}
		var p protocol.ConversationCreatedPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			t.Fatalf("M2: decode conversation_created payload: %v", err)
		}
		if p.ID == "" {
			t.Fatal("M2: conversation_created carried an empty id")
		}
		convB = p.ID
	}
	if convB == convA {
		t.Fatalf("M2: the minted conversation id equals A's (%s); the test needs two distinct "+
			"conversations or its whole premise collapses", convA)
	}
	t.Logf("[t=%s] M2: minted conversation B = %s", elapsed(), convB)

	// --- M2.5 (AC-4, fourth row): the SAME frame M5 sends, sent while B has no live
	// child, must be inert. This is the exact state #2085 created and the one the
	// first cut of #2099 got wrong: create_conversation binds and persists B's
	// session but defers the spawn to B's first message, so B resolves through the
	// registry perfectly well and only the child is missing. Rotating it anyway
	// rekeys the pool, rewrites sessions.json, rebinds the conversation and tells
	// every interactive client to render a session delimiter for a chat that has
	// never had a turn — while RestartFresh spawns nothing, so there is no fresh
	// session to show for any of it.
	//
	// The verdict is read as a PAIR, because either half alone is worthless. The
	// registry proves the frame did nothing; the daemon's own record proves it
	// ARRIVED, resolved B and chose the inert arm, without which an unmoved registry
	// would be equally explained by a frame that never got there.
	//
	// They run in that order so the failure names the defect rather than its shadow:
	// a daemon missing this guard rotates within milliseconds of the send, so the
	// registry check fires first and reports the rotation itself. Ordered the other
	// way, the same run fails on a missing log line and leaves the reader to work out
	// what happened instead.
	//
	// The record, not a wire receive, is the arrival proof for a structural reason:
	// new_session is fire-and-forget, so a refusal has no reply to wait for, and a
	// receive that timed out would take the phone conn down with it (waitForLog's own
	// doc records that constraint). The daemon runs under -pyry-verbose, so its DEBUG
	// records reach h.Stderr.
	preB, ok := readEntryByLabel(regPath, convB)
	if !ok {
		t.Fatalf("M2.5: conversation B (%s) has no session entry after create_conversation; the "+
			"inertness under test would be about an unbound id instead of a childless one\nfile:\n%s",
			convB, mustReadFile(t, regPath))
	}
	sendNewSessionFrameFor(t, phoneA, sendA, inertReqID, convB)

	// A rotation persists sessions.json under Pool.mu BEFORE it notifies anyone, so an
	// unmoved id is a direct observable rather than a race against the broadcast.
	// Polled rather than sampled once: a wrong rotation would be racing this read.
	// Conversation A is covered by M4's pre-rotation baseline below, which runs after
	// this and would catch a frame that acted on the cursor instead.
	assertBStill := func(stage string) {
		t.Helper()
		postB, ok := readEntryByLabel(regPath, convB)
		if !ok || postB.ID != preB.ID {
			t.Fatalf("M2.5 (AC-4, %s): conversation B's persisted session id moved %q → %q while B had "+
				"no live child. A named id the daemon cannot act on must be inert: no rotation, no "+
				"respawn.\nfile:\n%s", stage, preB.ID, postB.ID, mustReadFile(t, regPath))
		}
	}
	settleB := time.Now().Add(time.Second)
	for time.Now().Before(settleB) {
		assertBStill("settling")
		time.Sleep(50 * time.Millisecond)
	}
	waitForLogLineAll(t, h.Stderr, []string{"v2.new_session.no_live_child", convB}, 15*time.Second)
	assertBStill("after the refusal was recorded")
	t.Logf("[t=%s] M2.5: the new_session naming childless B was refused; its session is still %s",
		elapsed(), preB.ID)

	// --- M3: bring B's child up. Since #2085 create_conversation binds B's session
	// but defers the child to B's first message, so without this B would be bound with
	// no live child and the named rotation would be correctly inert.
	sendAndAwaitEcho("M3", sendToBReqID, convB, "u-b1", textToB, needleToB)
	t.Logf("[t=%s] M3: conversation B's child is live (the cursor is now on B)", elapsed())

	// --- M4: move the cursor BACK to A. Only a successful route stamps it, so this
	// send is the only way to put it back. This is the step that makes the test able
	// to fail: with the cursor on A, a daemon that ignored the named id rotates A.
	sendAndAwaitEcho("M4", sendBackToAReqID, convA, "u-a2", textBackToA, "e2e-2099-user:a-again")
	t.Logf("[t=%s] M4: the cursor is back on A — the named rotation now has something to get wrong", elapsed())

	// Baseline for the untouched-A assertion, read AFTER every send so a rotation
	// caused by anything earlier would already have shown up here.
	preA, ok := readBootstrapIfPresent(regPath)
	if !ok || preA.ID != initialUUID {
		t.Fatalf("M4: A's bootstrap session id is %q, want %q before the rotation — the untouched-A "+
			"assertion below would be meaningless\nfile:\n%s", preA.ID, initialUUID, mustReadFile(t, regPath))
	}

	// --- M5: new_session NAMING B, sent ONCE and then waited out on a single
	// deadline. The bare-path sibling re-sends on a cadence because its frame can
	// drop silently on a detached session, but that shape is unusable here: a
	// nextEnv that reaches its deadline closes the phone conn (see waitForLog's doc),
	// so a cadence that ever went quiet would kill the wire and then report the
	// death as a send failure. This session is demonstrably attached — four
	// milestones have round-tripped on it and M2.5 watched this very frame type
	// reach the handler — so one send is enough and the deadline is the only clock.
	const m5ReqID = uint64(3000)
	sendNewSessionFrameFor(t, phoneA, sendA, m5ReqID, convB)

	var (
		sawTransition bool
		newBSessionID string
		m5Deadline    = time.Now().Add(20 * time.Second)
	)
	for !sawTransition {
		env, ok := nextEnv(m5Deadline)
		if !ok {
			t.Fatalf("M5 (AC-1/AC-2): never observed a session_transition for conversation B (%s) after a "+
				"new_session naming it. Either the named id never reached the starter, or it was refused "+
				"as unknown/unbound and the frame went inert.\nsessions.json:\n%s",
				convB, mustReadFile(t, regPath))
		}
		if env.Type != protocol.TypeSessionTransition {
			continue
		}
		var st protocol.SessionTransitionPayload
		if err := json.Unmarshal(env.Payload, &st); err != nil {
			t.Fatalf("M5: decode session_transition payload: %v", err)
		}
		// AC-2: the transition names B, not the cursor's A. A transition for A here
		// IS the defect, so it fails loudly rather than being skipped as noise.
		if st.ConversationID == convA {
			t.Fatalf("M5 (AC-1): the rotation landed on conversation A (%s), the CURSOR's conversation, "+
				"after a new_session naming B (%s). This is the #2099 defect: %s → %s",
				convA, convB, st.PreviousSessionID, st.NewSessionID)
		}
		if st.ConversationID != convB {
			continue
		}
		if st.Reason != "clear" {
			t.Errorf("M5: session_transition Reason: got %q, want %q", st.Reason, "clear")
		}
		if st.NewSessionID == "" || st.NewSessionID == st.PreviousSessionID {
			t.Errorf("M5: session_transition did not carry a fresh id: previous=%q new=%q",
				st.PreviousSessionID, st.NewSessionID)
		}
		newBSessionID = st.NewSessionID
		sawTransition = true
	}
	t.Logf("[t=%s] M5: conversation B rotated to %s and the transition named B", elapsed(), newBSessionID)

	// --- M6 (AC-1's other half): A's child was NOT touched. A is bound to the
	// bootstrap session, so its registry entry is the direct observable — an id still
	// reading initialUUID means no rotation ran against A. Polled briefly rather than
	// read once: a wrong rotation would be racing this read, and a single sample taken
	// a millisecond early would report the defect as absent.
	settle := time.Now().Add(2 * time.Second)
	for time.Now().Before(settle) {
		e, ok := readBootstrapIfPresent(regPath)
		if !ok {
			continue
		}
		if e.ID != initialUUID {
			t.Fatalf("M6 (AC-1): conversation A's session rotated %s → %s while the new_session named B (%s). "+
				"The cursor's conversation must be untouched.", initialUUID, e.ID, convB)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Logf("[t=%s] M6: conversation A's session is still %s — the cursor's conversation was untouched", elapsed(), initialUUID)
}

// sendNewSessionFrameFor is sendNewSessionFrame carrying a conversation_id (#2099):
// the frame names the conversation to restart instead of leaving the daemon to pick
// its cursor's. It lives beside its one caller rather than in the shared-helpers
// file, whose grouping is historical (helpers orphaned by #1348's deletions) rather
// than thematic. Its bare twin stays there and stays exercised as itself: that is
// the shape an un-upgraded client sends.
func sendNewSessionFrameFor(t *testing.T, phone *fakephone.Client, cs *noise.CipherState, reqID uint64, conversationID string) {
	t.Helper()
	env, err := json.Marshal(protocol.Envelope{
		ID:   reqID,
		Type: protocol.TypeNewSession,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.NewSessionPayload{
			ConversationID: conversationID,
		}),
	})
	if err != nil {
		t.Fatalf("marshal new_session envelope: %v", err)
	}
	cipher, err := cs.Encrypt(env)
	if err != nil {
		t.Fatalf("seal new_session envelope: %v", err)
	}
	sendNoiseMsg(t, phone, cipher)
}

// readEntryByLabel returns the sessions.json entry a conversation's session is
// persisted under. The pool labels a minted session with the conversation id that
// asked for it, so the label is the only handle a test has on a non-bootstrap
// session's id. Returns ok == false for a missing, unparseable or label-less file,
// which callers treat as a failure rather than a retry: by the time this is called
// the conversation has already been created and acknowledged over the wire.
func readEntryByLabel(regPath, label string) (registryEntry, bool) {
	data, err := os.ReadFile(regPath)
	if err != nil {
		return registryEntry{}, false
	}
	var reg registryFile
	if err := json.Unmarshal(data, &reg); err != nil {
		return registryEntry{}, false
	}
	for _, e := range reg.Sessions {
		if e.Label == label {
			return e, true
		}
	}
	return registryEntry{}, false
}

// minTime returns the earlier of two deadlines. The M2.5 and M5 loops wait on
// whichever of the resend cadence and the overall deadline comes first, so a quiet
// wire still re-sends rather than blocking to the end of the window.
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

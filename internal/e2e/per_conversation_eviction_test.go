//go:build e2e

package e2e

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/paireddevice"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// Phase 2.0 of EPIC #672: idle eviction is load-bearing — daemon RAM scales
// with *active* discussions, not the total ever created. #677 mints+binds a
// dedicated claude session at create_conversation time; #678 routes
// send_message through the bound session's Pool.Activate (the cap-enforcing
// spawn entry). #680 adds no production code: it proves that a *phone-created*
// discussion's bound session is a full citizen of the idle-evict / active-cap
// machinery the bootstrap already participates in.
//
// Since #2085 the bind is still eager but the SPAWN is not: create_conversation
// registers the session evicted and the child comes up on the conversation's
// first message. Both tests therefore drive a turn per discussion through
// activateViaTurn before asserting anything about leaving the active state —
// see that helper for why the precondition is the difference between these
// tests proving something and passing vacuously.
//
// The two tests below pin the four acceptance criteria at the binary boundary
// using the v1 fakephone/fakerelay harness, an INTERACTIVE phone, and a
// stream-json fakeclaude child under the production interactive_runner toggle
// (#1512 — the daemon's only interactive runner is stream-json, so a TUI child
// was one-sided fiction):
//
//	Test A (idle):  AC#1 per-discussion idle eviction, AC#2 reactivate-on-send,
//	                AC#4 no cross-bleed when one discussion churns.
//	Test B (cap):   AC#3 cap evicts the LRU active peer (incl. a cross-discussion
//	                victim), AC#4 only the deliberate victim transitions.
//
// What this tier asserts is lifecycle/routing scoping (which session's registry
// entry transitions, which reactivates) PLUS wire-observable turn delivery: a
// reactivated discussion's turn comes back as an assistant_delta scoped to that
// conversation. Transcript CONTENT recall ("the prior conversation is intact")
// stays realclaude's domain — stream-mode fakeclaude opens no <uuid>.jsonl at
// all (it short-circuits above its sessions-dir binding), so there is no
// transcript here to read. TestInteractiveStreamResumeAfterEviction covers that
// half.

// TestE2E_PerConversation_IdleEvictsAndReactivates exercises a per-discussion
// session through its full active→evicted→active arc:
//
//	AC#1 — two phone-created discussions bind two distinct dedicated sessions;
//	       each idle-evicts (lifecycle_state=="evicted", claude exited) once its
//	       idle window elapses with no attach.
//	AC#2 — a send_message to one evicted discussion reactivates it (respawn
//	       claude against its OWN uuid, never a peer's) and DELIVERS the turn: the
//	       phone observes the reply, not merely the write-side ack. Which id FLAG
//	       that respawn carries is decided per spawn by streamsup's by-id
//	       transcript probe since #1631 (useCreateForm), so it is --session-id here
//	       — stream-mode fakeclaude establishes no transcript to resume. The id is
//	       the assertion; the flag is not.
//	AC#4 — reactivating one discussion's session leaves the other discussion's
//	       evicted session untouched: churn in one does not disturb another.
func TestE2E_PerConversation_IdleEvictsAndReactivates(t *testing.T) {
	const (
		initialUUID        = "66666666-6666-4666-8666-666666666666"
		reqID       uint64 = 4
		wakeText           = "e2e-1512-wake:reactivate\n"
		wakeNeedle         = "e2e-1512-wake:reactivate"
	)

	home := shortHome(t)
	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	relayURL := fr.URL() + "/v2/server"

	pairPayload, err := paireddevice.Setup(paireddevice.Config{
		Home:                   home,
		InstanceName:           "test",
		Relay:                  relayURL,
		DeviceName:             "phone-a",
		AllowRemotePermissions: false,
	})
	if err != nil {
		t.Fatalf("setup paired device: %v", err)
	}
	pubKey, err := base64.StdEncoding.DecodeString(pairPayload.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	// idle=8s, uncapped: the only transitions are idle-driven, so a previously
	// active per-conversation session evicts ~8s after its last activation.
	//
	// 8s, not 2s, and the number is load-bearing. Session.runActive arms the idle
	// timer ONCE on entering active and resets it only while attached > 0 — turn
	// activity does not touch it. So a reactivated session has exactly idleTimeout
	// from Activate to re-eviction, whatever the turn is doing. The stream path's
	// delivery chain spends most of that: streamRunner.WriteUserTurn returns the
	// retryable ErrNoLiveChild while the respawned child is between spawn and
	// stdin-ready, and the msgqueue drain retries every
	// msgqueue.defaultRetryInterval (1s). Worst case ≈ spawn + 3 retries + echo /
	// parse / drain / seal ≈ 4s. At 2s the child is SIGKILLed before the turn
	// lands and the drain below hangs to its deadline — a flake, not a red.
	//
	// CONSTRAINT for future edits: this window must stay ≥ 3 ×
	// msgqueue.defaultRetryInterval plus spawn. If either number moves, so does
	// this one.
	h := startPerConvHarness(t, home, initialUUID, relayURL, "-pyry-idle-timeout=8s")

	regPath := filepath.Join(home, ".pyry", "test", "sessions.json")
	convPath := filepath.Join(home, ".pyry", "test", "conversations.json")

	phone, initSend, initRecv := dialHelloPhone(t, home, fr, pubKey, pairPayload.Token)

	// AC#4 (binding distinctness): two discussions, two distinct dedicated
	// sessions — neither the bootstrap, neither shared.
	convA := createConversationViaPhone(t, phone, initSend, initRecv, 2)
	boundA := boundSessionID(t, convPath, convA)
	convB := createConversationViaPhone(t, phone, initSend, initRecv, 3)
	boundB := boundSessionID(t, convPath, convB)
	if boundA == boundB {
		t.Fatalf("convA and convB share bound session %s — not distinct dedicated sessions", boundA)
	}

	// Each discussion's claude comes up on its FIRST MESSAGE since #2085, so
	// drive one per conversation and confirm each reaches "active" before
	// asserting anything about leaving that state. Without this the evicted-wait
	// below would be satisfied instantly by a session born evicted, and a green
	// run would stop proving "claude exited, RAM freed". The text is deliberately
	// not the wake marker: the AC#2 assertion further down must be satisfiable
	// only by the reactivated turn.
	activateViaTurn(t, phone, initSend, initRecv, regPath, convA, boundA, 100, "m-prime-a", "e2e-2085-prime:a\n")
	activateViaTurn(t, phone, initSend, initRecv, regPath, convB, boundB, 101, "m-prime-b", "e2e-2085-prime:b\n")

	// AC#1: each per-discussion session idle-evicts. lifecycle_state=="evicted"
	// is written only after the supervisor stops the child, so it faithfully
	// witnesses "claude process exited, RAM freed" — and, thanks to the
	// activations above, it witnesses a genuine active→evicted transition. The
	// wait must exceed the 8s window above.
	waitForSessionState(t, regPath, boundA, "evicted", 15*time.Second)
	waitForSessionState(t, regPath, boundB, "evicted", 15*time.Second)

	// AC#2: a send_message to evicted convA reactivates its bound session and
	// delivers the turn. Two PRECONDITIONS first — the sealed ack and the
	// evicted→active registry flip — neither of which is the proof: the send
	// handler acks on accept-into-backlog, well before the respawned child is
	// stdin-ready. The proof is M1/M2 below.
	send := protocol.Envelope{
		ID:   reqID,
		Type: protocol.TypeSendMessage,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.SendMessagePayload{
			ConversationID: convA,
			MessageID:      "m-1",
			Text:           wakeText,
		}),
	}
	sendRaw, err := json.Marshal(send)
	if err != nil {
		t.Fatalf("marshal send_message envelope: %v", err)
	}
	ct, err := initSend.Encrypt(sendRaw)
	if err != nil {
		t.Fatalf("seal send_message envelope: %v", err)
	}
	sendNoiseMsg(t, phone, ct)
	drainForReply(t, phone, initRecv, protocol.TypeAck, reqID, 15*time.Second)
	waitForSessionState(t, regPath, boundA, "active", 3*time.Second)

	// Drain the reactivated turn: two ordered milestones, mirroring
	// TestRelayV2_StreamSendMessageDrainsTurn. A leading turn_state{responding}
	// arrives BEFORE the delta; ignore turn_states until sawDelta is set. The
	// phone conn is read serially on the test goroutine (the single reader — two
	// readers would race the Noise receive nonce).
	sawDelta := false
	drainDeadline := time.Now().Add(20 * time.Second)
	for {
		remaining := time.Until(drainDeadline)
		if remaining <= 0 {
			if !sawDelta {
				t.Fatalf("M1: phone never observed an assistant_delta for the reactivated conversation %s. "+
					"Either delivery never reached the respawned child (stale sink binding on the new runner, "+
					"or a drain-gate tag mismatch) or the session re-evicted before the turn landed. The "+
					"stream_turn.not_active Debug record discriminates them — daemon stderr tail:\n%s",
					convA, stderrTail(h, 4000))
			}
			t.Fatal("M2: phone observed the assistant_delta but never a terminal " +
				"turn_state{idle}; the turn opened but never closed")
		}
		raw, err := phone.ReceiveBytes(remaining)
		if err != nil {
			if errors.Is(err, fakephone.ErrReceiveTimeout) {
				continue // deadline reached — re-loop into the milestone-specific t.Fatal above
			}
			t.Fatalf("phone receive (drain): %v", err)
		}
		var inner protocol.InnerFrameV2
		if err := json.Unmarshal(raw, &inner); err != nil {
			t.Fatalf("phone decode inner frame (drain): %v", err)
		}
		// Skip on the INNER type only, never on a decrypted envelope: the receive
		// CipherState must open sealed frames in arrival order, so dropping a
		// noise_msg undecrypted desynchronises the nonce and every later open fails.
		if inner.Type != protocol.TypeNoiseMsg {
			continue
		}
		env := decryptInnerEnvelope(t, inner, initRecv)

		switch env.Type {
		case protocol.TypeAssistantDelta:
			if sawDelta {
				continue
			}
			var d protocol.AssistantDeltaPayload
			if err := json.Unmarshal(env.Payload, &d); err != nil {
				t.Fatalf("phone decode assistant_delta payload: %v", err)
			}
			// BOTH halves are required, and a wrong-conversation delta is a hard
			// fail rather than a loop-continue. Marker alone would be satisfied by
			// convA's text delivered on convB — which IS the cross-bleed defect
			// AC#4 exists to catch. Conversation alone would be satisfied by any
			// delta on convA, including a stale pre-eviction one.
			if d.ConversationID != convA {
				t.Fatalf("M1: assistant_delta scoped to the WRONG conversation: got %q, want %q "+
					"(cross-discussion bleed — convA's turn surfaced on another discussion)",
					d.ConversationID, convA)
			}
			if !strings.Contains(d.Text, wakeNeedle) {
				t.Fatalf("M1: assistant_delta did not carry this turn's marker; got Text=%q, want it to "+
					"contain %q (stream-mode fakeclaude echoes the sent prompt, so the marker is the "+
					"round-trip proof that THIS turn reached the respawned child)", d.Text, wakeNeedle)
			}
			sawDelta = true
			t.Logf("M1: phone observed assistant_delta on the reactivated conversation carrying this turn's marker")
		case protocol.TypeTurnState:
			if !sawDelta {
				continue // the leading responding state precedes the delta
			}
			var st protocol.TurnStatePayload
			if err := json.Unmarshal(env.Payload, &st); err != nil {
				t.Fatalf("phone decode turn_state payload: %v", err)
			}
			if st.State != "idle" {
				continue
			}
			if st.ConversationID != convA {
				t.Errorf("turn_state ConversationID: got %q, want %q", st.ConversationID, convA)
			}
			t.Logf("M2: phone observed terminal turn_state{idle} — the reactivated turn closed")

			// AC#4 (no cross-bleed): convA's reactivation did NOT touch convB. With
			// no cap there is no LRU eviction, and an evicted session has no reason
			// to wake without its own send/attach. Checked AFTER M2 so the claim is
			// "convB stayed evicted across a COMPLETED turn in convA", not merely
			// across an ack; the 8s window leaves ample room before convA re-arms.
			assertEvicted(t, regPath, boundB)
			return
		}
	}
}

// TestE2E_PerConversation_CapEvictsCrossDiscussion drives the active cap with
// one create_conversation plus one turn per discussion — the turn is what
// activates, since #2085 moved the spawn off the create and onto the first
// message — and asserts LRU victim selection:
//
//	AC#3 — activating one more session than the cap evicts the LRU active peer
//	       rather than exceeding the cap; the active count is never > cap at any
//	       settled checkpoint. The second eviction targets a *per-conversation*
//	       session (the security-sensitive cross-conversation eviction the PO
//	       flagged): remote activity in discussion C evicts discussion A.
//	AC#4 — only the deliberate LRU victim transitions; the bystander stays
//	       active and each discussion keeps its own distinct bound session.
//
// cap=2, no idle timeout: the only transitions are cap-driven, so the victim
// sequence is deterministic (and no idle timer is armed, so the 8s window the
// idle test needs is irrelevant here).
func TestE2E_PerConversation_CapEvictsCrossDiscussion(t *testing.T) {
	const initialUUID = "77777777-7777-4777-8777-777777777777"

	home := shortHome(t)
	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	relayURL := fr.URL() + "/v2/server"

	pairPayload, err := paireddevice.Setup(paireddevice.Config{
		Home:                   home,
		InstanceName:           "test",
		Relay:                  relayURL,
		DeviceName:             "phone-a",
		AllowRemotePermissions: false,
	})
	if err != nil {
		t.Fatalf("setup paired device: %v", err)
	}
	pubKey, err := base64.StdEncoding.DecodeString(pairPayload.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	startPerConvHarness(t, home, initialUUID, relayURL, "-pyry-active-cap=2")

	regPath := filepath.Join(home, ".pyry", "test", "sessions.json")
	convPath := filepath.Join(home, ".pyry", "test", "conversations.json")

	bootstrapID := waitForBootstrap(t, regPath, 5*time.Second)

	phone, initSend, initRecv := dialHelloPhone(t, home, fr, pubKey, pairPayload.Token)

	// Create A and drive its first turn — active = {bootstrap, A} = 2, exactly
	// at cap, no evict. The create alone activates nothing since #2085; each
	// activateViaTurn returns only once the session it woke reads "active", so
	// the three activations below are ordered and the LRU sequence stays
	// deterministic.
	convA := createConversationViaPhone(t, phone, initSend, initRecv, 2)
	boundA := boundSessionID(t, convPath, convA)
	activateViaTurn(t, phone, initSend, initRecv, regPath, convA, boundA, 100, "m-cap-a", "e2e-2085-cap:a\n")
	// 50ms gap so lastActiveAt timestamps are distinguishable for pickLRUVictim.
	time.Sleep(50 * time.Millisecond)

	// Create B and drive its first turn — activating B = 3 > cap → cap-evicts
	// LRU peer = bootstrap.
	convB := createConversationViaPhone(t, phone, initSend, initRecv, 3)
	boundB := boundSessionID(t, convPath, convB)
	activateViaTurn(t, phone, initSend, initRecv, regPath, convB, boundB, 101, "m-cap-b", "e2e-2085-cap:b\n")
	time.Sleep(50 * time.Millisecond)

	// AC#3: bootstrap is the LRU victim; A and B stay active; count back to 2.
	waitForSessionState(t, regPath, bootstrapID, "evicted", 3*time.Second)
	assertActive(t, regPath, boundA)
	assertActive(t, regPath, boundB)

	// Create C and drive its first turn — activating C = 3 > cap → cap-evicts
	// LRU peer = boundA, a per-conversation session: discussion C's activity
	// evicts discussion A.
	convC := createConversationViaPhone(t, phone, initSend, initRecv, 4)
	boundC := boundSessionID(t, convPath, convC)
	activateViaTurn(t, phone, initSend, initRecv, regPath, convC, boundC, 102, "m-cap-c", "e2e-2085-cap:c\n")

	// AC#3: boundA is the LRU victim; B and C stay active; count never > 2.
	waitForSessionState(t, regPath, boundA, "evicted", 3*time.Second)
	assertActive(t, regPath, boundB)
	assertActive(t, regPath, boundC)

	// AC#4: only the deliberate LRU victim transitioned. The bystander boundB
	// stayed active across C's creation, and each discussion's bound session is
	// its own distinct UUID — no two discussions (or the bootstrap) collide.
	assertDistinctIDs(t, map[string]string{
		"bootstrap": bootstrapID,
		"boundA":    boundA,
		"boundB":    boundB,
		"boundC":    boundC,
	})
	// Bindings are unchanged from capture — eviction does not rebind a session.
	if got := boundSessionID(t, convPath, convA); got != boundA {
		t.Errorf("convA current_session_id changed: got %s, want %s", got, boundA)
	}
	if got := boundSessionID(t, convPath, convB); got != boundB {
		t.Errorf("convB current_session_id changed: got %s, want %s", got, boundB)
	}
	if got := boundSessionID(t, convPath, convC); got != boundC {
		t.Errorf("convC current_session_id changed: got %s, want %s", got, boundC)
	}
}

// startPerConvHarness spawns pyry with a stream-json fakeclaude child and relay
// wiring, threading arbitrary -pyry-* FLAGS so one caller can pass
// -pyry-idle-timeout and another -pyry-active-cap. That is the whole reason it
// exists: the shared StartStreamInteractiveWithRelay takes extra ENV only, and
// teaching it flags is a signature change across every call site.
//
// The daemon is opted into the stream runner explicitly via
// writeStreamInteractiveConfig — the same production config toggle the shared
// harness writes — rather than leaning on selectInteractiveRunner's empty-string
// default. An implicit default is a test that stops asserting its own path the
// day the default moves.
//
// Child env is exactly the stream set. Stream-mode fakeclaude short-circuits
// above its mustEnv calls, so it binds no sessions dir and opens no transcript:
// SESSIONS_DIR / INITIAL_UUID / TRIGGER / STDIN_LOG are not merely unnecessary,
// they are dead. Harness.ClaudeSessionsDir is left unset for the same reason.
// -pyry-verbose raises the stderr handler to slog.LevelDebug and nothing else,
// which is what makes the drain gate's stream_turn.not_active drop record
// visible — the single highest-value line when a delivery assertion times out.
//
// initialUUID is a DAEMON-side seed (seedBootstrapRegistry pins the bootstrap
// pool id the cap test's waitForBootstrap reads), unrelated to the child env of
// the same name that stream mode drops.
func startPerConvHarness(t *testing.T, home, initialUUID, relayURL string, extraFlags ...string) *Harness {
	t.Helper()
	fakeBin := ensureFakeClaudeBuilt(t)
	writeStreamInteractiveConfig(t, home)
	seedBootstrapRegistry(t, home, initialUUID)

	flags := append([]string{
		"-pyry-workdir=" + home,
		"-pyry-relay=" + relayURL,
		"-pyry-verbose",
	}, extraFlags...)

	socket, cmd, stdout, stderr, doneCh := spawnWith(t, home, spawnOpts{
		claudeBin:  fakeBin,
		claudeArgs: []string{},
		extraFlags: flags,
		extraEnv: []string{
			"PYRY_ALLOW_INSECURE_RELAY=1",
			"PYRY_MOBILE_V2=1",
			"PYRY_FAKE_CLAUDE_STREAM_JSON=1",
		},
	})

	h := &Harness{
		SocketPath: socket,
		HomeDir:    home,
		PID:        cmd.Process.Pid,
		Stdout:     stdout,
		Stderr:     stderr,
		cmd:        cmd,
		doneCh:     doneCh,
	}
	t.Cleanup(func() { h.teardown(t) })

	if err := h.waitForReady(); err != nil {
		t.Fatalf("e2e: %v", err)
	}
	return h
}

// stderrTail returns the last n bytes of the daemon's captured stderr, for
// attaching to a delivery-assertion failure. Content-free by construction at
// the records that matter: the drain gate's drop line carries a discriminant
// and a session id, never turn text.
func stderrTail(h *Harness, n int) string {
	s := h.Stderr.String()
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

// dialHelloPhone dials a fakephone through fr and completes the v2 Noise_IK
// handshake, requesting the INTERACTIVE capability — the grant the structured
// stream requires, and the grant that authorises the daemon to push unsolicited
// frames onto this conn. That is why every read downstream of here is a drain
// loop and not a single-frame read. Returns the ready client plus the initiator
// CipherStates (initSend seals phone→daemon, initRecv opens daemon→phone). The
// daemon must already be running (its binary leg registered with the relay) and
// paired (pairToken + pubKey from a prior `pyry pair`). Close is registered for
// cleanup.
func dialHelloPhone(t *testing.T, home string, fr *fakerelay.Server, pubKey []byte, pairToken string) (*fakephone.Client, *noise.CipherState, *noise.CipherState) {
	t.Helper()
	serverID := readPersistedServerID(t, home)

	waitCtx, cancelWait := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelWait()
	if !fr.WaitBinary(waitCtx, serverID) {
		t.Fatal("binary connection not registered within 5s")
	}

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelDial()
	phone, err := fakephone.Dial(dialCtx, fr.URL(), serverID, pairToken, "phone-a")
	if err != nil {
		t.Fatalf("phone dial: %v", err)
	}
	t.Cleanup(func() { _ = phone.Close() })

	initSend, initRecv := driveHandshakeToOpenDaemonInteractive(t, phone, pubKey, pairToken)
	return phone, initSend, initRecv
}

// activateViaTurn drives one message on convID and drains the wire until that
// turn has both opened (an assistant_delta on convID) and closed (a terminal
// turn_state{idle} on convID), then waits for boundID to read "active" in the
// session registry.
//
// It exists because since #2085 create_conversation does NOT spawn claude — the
// child comes up on the conversation's first message. Both tests in this file
// assert something about a session LEAVING the active state, and an evicted
// session is what a freshly created one already is: without an activation first,
// the cap test never reaches its cap and the idle test's evicted-wait is
// satisfied instantly by a session that was never active. So this is not
// scaffolding, it is the precondition that keeps those assertions non-vacuous.
//
// Draining the whole turn (not just the ack) is load-bearing for the idle test:
// its AC#2 drain hard-fails on an assistant_delta for convA that lacks the wake
// marker, so this turn's own deltas must be off the wire before the reactivation
// phase begins. Every sealed frame is decrypted in arrival order for the reason
// drainForReply documents — the receive CipherState is a lockstep counter.
func activateViaTurn(t *testing.T, phone *fakephone.Client, initSend, initRecv *noise.CipherState,
	regPath, convID, boundID string, reqID uint64, messageID, text string) {
	t.Helper()
	send := protocol.Envelope{
		ID:   reqID,
		Type: protocol.TypeSendMessage,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.SendMessagePayload{
			ConversationID: convID,
			MessageID:      messageID,
			Text:           text,
		}),
	}
	raw, err := json.Marshal(send)
	if err != nil {
		t.Fatalf("marshal priming send_message (conv=%s): %v", convID, err)
	}
	ct, err := initSend.Encrypt(raw)
	if err != nil {
		t.Fatalf("seal priming send_message (conv=%s): %v", convID, err)
	}
	sendNoiseMsg(t, phone, ct)

	sawDelta := false
	deadline := time.Now().Add(30 * time.Second)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			if !sawDelta {
				t.Fatalf("priming turn on %s never produced an assistant_delta — the first message did not "+
					"bring the conversation's child up", convID)
			}
			t.Fatalf("priming turn on %s opened but never closed with turn_state{idle}", convID)
		}
		frame, err := phone.ReceiveBytes(remaining)
		if err != nil {
			if errors.Is(err, fakephone.ErrReceiveTimeout) {
				continue // deadline reached — re-loop into the t.Fatalf above
			}
			t.Fatalf("phone receive (priming %s): %v", convID, err)
		}
		var inner protocol.InnerFrameV2
		if err := json.Unmarshal(frame, &inner); err != nil {
			t.Fatalf("decode inner frame (priming %s): %v", convID, err)
		}
		if inner.Type != protocol.TypeNoiseMsg {
			continue
		}
		env := decryptInnerEnvelope(t, inner, initRecv)
		switch env.Type {
		case protocol.TypeAssistantDelta:
			var d protocol.AssistantDeltaPayload
			if err := json.Unmarshal(env.Payload, &d); err != nil {
				t.Fatalf("decode assistant_delta (priming %s): %v", convID, err)
			}
			if d.ConversationID == convID {
				sawDelta = true
			}
		case protocol.TypeTurnState:
			if !sawDelta {
				continue // the leading responding state precedes the delta
			}
			var st protocol.TurnStatePayload
			if err := json.Unmarshal(env.Payload, &st); err != nil {
				t.Fatalf("decode turn_state (priming %s): %v", convID, err)
			}
			if st.State != "idle" || st.ConversationID != convID {
				continue
			}
			// The registry write trails the in-memory transition, so poll rather
			// than assert: the turn closing proves the child ran, this proves the
			// state the eviction assertions are about to watch leave.
			waitForSessionState(t, regPath, boundID, "active", 5*time.Second)
			return
		}
	}
}

// createConversationViaPhone sends an all-null create_conversation (server
// defaults) with envelope id reqID, drains to the matching conversation_created
// reply, and returns the server-minted conversation id. Returns only after the
// daemon has minted + bound + eagerly persisted the dedicated session (the reply
// is sent after the handler's reg.Save). Since #2085 the mint spawns no child,
// so the reply arrives without waiting on a PTY; the 15s budget is now slack for
// a loaded host rather than a spawn allowance, and activateViaTurn is what
// brings the conversation's claude up.
func createConversationViaPhone(t *testing.T, phone *fakephone.Client, initSend, initRecv *noise.CipherState, reqID uint64) string {
	t.Helper()
	req := protocol.Envelope{
		ID:      reqID,
		Type:    protocol.TypeCreateConversation,
		TS:      time.Now().UTC(),
		Payload: mustJSON(t, protocol.CreateConversationPayload{}), // all fields null
	}
	reqRaw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal create_conversation (id=%d): %v", reqID, err)
	}
	ct, err := initSend.Encrypt(reqRaw)
	if err != nil {
		t.Fatalf("seal create_conversation (id=%d): %v", reqID, err)
	}
	sendNoiseMsg(t, phone, ct)
	env := drainForReply(t, phone, initRecv, protocol.TypeConversationCreated, reqID, 15*time.Second)
	var p protocol.ConversationCreatedPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		t.Fatalf("unmarshal conversation_created payload: %v", err)
	}
	if p.ID == "" {
		t.Fatalf("conversation_created payload has empty id")
	}
	return p.ID
}

// drainForReply reads sealed frames in arrival order until one decrypts to an
// envelope of type want whose InReplyTo is reqID, and returns it. Every other
// envelope type is ignored: an interactive conn may carry unsolicited structured
// pushes, so a request's reply is not guaranteed to be the next frame on the
// wire.
//
// Non-noise_msg inner frames are skipped WITHOUT decrypting (they do not advance
// the receive nonce); every noise_msg is decrypted, in order, because the receive
// CipherState is a lockstep counter — skipping one sealed frame desynchronises it
// and every later open fails. Same discipline as
// TestRelayV2_StreamSendMessageDrainsTurn's drain.
//
// On deadline it fails naming the envelope types it did see, so a wrong-reply
// failure is distinguishable from a nothing-arrived one.
func drainForReply(t *testing.T, phone *fakephone.Client, recv *noise.CipherState, want string, reqID uint64, timeout time.Duration) protocol.Envelope {
	t.Helper()
	var seen []string
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatalf("no %s with in_reply_to=%d within %s; envelope types observed: %v",
				want, reqID, timeout, seen)
		}
		raw, err := phone.ReceiveBytes(remaining)
		if err != nil {
			if errors.Is(err, fakephone.ErrReceiveTimeout) {
				continue // deadline reached — re-loop into the t.Fatalf above
			}
			t.Fatalf("phone receive (awaiting %s id=%d): %v", want, reqID, err)
		}
		var inner protocol.InnerFrameV2
		if err := json.Unmarshal(raw, &inner); err != nil {
			t.Fatalf("decode inner frame (awaiting %s id=%d): %v", want, reqID, err)
		}
		if inner.Type != protocol.TypeNoiseMsg {
			continue
		}
		env := decryptInnerEnvelope(t, inner, recv)
		seen = append(seen, env.Type)
		if env.Type != want {
			continue
		}
		if env.InReplyTo == nil || *env.InReplyTo != reqID {
			t.Fatalf("%s InReplyTo: got %v, want pointer to %d", want, env.InReplyTo, reqID)
		}
		return env
	}
}

// boundSessionID reads conversations.json and returns the current_session_id
// bound to convID, failing if absent or empty. The create_conversation reply is
// sent after the handler's eager reg.Save (atomic rename), so the row is on disk
// by the time the phone observes the reply; the short poll absorbs any
// cross-process visibility lag.
func boundSessionID(t *testing.T, convPath, convID string) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if reg, err := conversations.Load(convPath); err == nil {
			if conv, ok := reg.Get(conversations.ConversationID(convID)); ok && conv.CurrentSessionID != "" {
				return conv.CurrentSessionID
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("conversation %s never had a non-empty current_session_id within 2s\nfile:\n%s",
				convID, mustReadFile(t, convPath))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// assertEvicted checks regPath right now for id and fails if its
// lifecycle_state is not "evicted". The evicted-side counterpart of
// assertActive (cap_test.go): a one-shot "X must be evicted at this exact
// moment" bystander checkpoint, distinct from waitForSessionState's polling.
func assertEvicted(t *testing.T, regPath, id string) {
	t.Helper()
	reg := readRegistry(t, regPath)
	for _, e := range reg.Sessions {
		if e.ID == id {
			if e.LifecycleState != "evicted" {
				t.Fatalf("expected session %s evicted, but lifecycle_state=%q", id, e.LifecycleState)
			}
			return
		}
	}
	t.Fatalf("session %s not present in registry\nfile:\n%s", id, mustReadFile(t, regPath))
}

// assertDistinctIDs fails if any two named ids collide or any is empty. Pins the
// AC#4 invariant that each discussion (and the bootstrap) owns a distinct
// dedicated session UUID. The name map gives a readable collision message.
func assertDistinctIDs(t *testing.T, ids map[string]string) {
	t.Helper()
	seen := make(map[string]string, len(ids))
	for name, id := range ids {
		if id == "" {
			t.Fatalf("session id for %q is empty", name)
		}
		if prev, ok := seen[id]; ok {
			t.Fatalf("session id collision: %q and %q both bind %s", prev, name, id)
		}
		seen[id] = name
	}
}

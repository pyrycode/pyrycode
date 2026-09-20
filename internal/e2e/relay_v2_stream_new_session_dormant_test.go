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

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/paireddevice"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// TestRelayV2_StreamNewSessionAfterDaemonRestartResetsDormantConversation is
// #2521's fake-daemon proof, and it is the reported sequence rather than a
// convenient approximation of it: a channel that has been used, a restart, and
// then Reset as the FIRST thing that happens to that channel.
//
// THE RESTART IS THE TEST. sessions.New materialises only the bootstrap (#1487),
// so after it B's session is a persisted entry with no *Session behind it —
// Pool.Lookup misses, resolveBoundSession refuses, and before this ticket the
// frame landed on the same inert arm an unknown conversation gets. The message
// route never had that gap (sessionRouter.resolve re-materialises on
// ErrSessionNotFound), which is exactly why "send one message first, then Reset"
// was the workaround the report described. A test that sent to B after the
// restart would revive B on the way in and prove nothing at all.
//
// The milestones:
//
//	M1  send to A          — the wire works and the cursor is stamped to A.
//	M2  create B           — bound, persisted, no child (#2085).
//	M3  send to B          — B's session is ACTIVATED, which is the durable fact
//	                         Pool.EverActivated reads back after the restart. Without
//	                         it B is a never-used conversation and must stay inert.
//	--- daemon restart ---
//	M4  new_session for B  — the frame under test, sent before any message.
//	M5  send to B          — proves the first post-reset turn spawns under the FRESH
//	                         id, so the retired transcript is not resumed.
//
// Conversation A is the isolation control throughout: it is bound to the
// bootstrap session, so its registry entry still reading initialUUID at the end
// means no rotation ran against anything but B.
func TestRelayV2_StreamNewSessionAfterDaemonRestartResetsDormantConversation(t *testing.T) {
	const (
		initialUUID = "11111111-1111-4111-8111-111111111111" // bootstrap, bound to conversation A
		convA       = "22222222-2222-4222-8222-222222222222"
		textToA     = "e2e-2521-user:a\n"
		needleToA   = "e2e-2521-user:a"
		textToB     = "e2e-2521-user:b\n"
		needleToB   = "e2e-2521-user:b"
		textAfter   = "e2e-2521-user:b-after-reset\n"
		needleAfter = "e2e-2521-user:b-after-reset"
	)

	testStart := time.Now()
	elapsed := func() string { return time.Since(testStart).Round(time.Millisecond).String() }

	home := shortHome(t)
	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	relayURL := fr.URL() + "/v2/server"

	payload, err := paireddevice.Setup(paireddevice.Config{
		Home:                   home,
		InstanceName:           "test",
		Relay:                  relayURL,
		DeviceName:             "phone-a",
		AllowRemotePermissions: false,
	})
	if err != nil {
		t.Fatalf("setup paired device: %v", err)
	}
	pubKey, err := base64.StdEncoding.DecodeString(payload.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	// Bind A to the bootstrap session before the daemon starts: the daemon loads
	// conversations.json once at startup.
	seedBoundConversation(t, home, convA, initialUUID)
	regPath := filepath.Join(home, ".pyry", "test", "sessions.json")

	// --- Daemon #1.
	h1 := StartStreamInteractiveWithRelay(t, home, initialUUID, relayURL)
	t.Cleanup(func() { h1.Stop(t) })
	phone1, send1, recv1 := dialPairedPhone(t, fr, home, payload.Token, pubKey, "phone-a")

	seal1, next1 := noiseWire(t, phone1, send1, recv1)
	sendAndAwaitEcho(t, seal1, next1, "M1", 2521, convA, "u-a1", textToA, needleToA)
	t.Logf("[t=%s] M1: conversation A is live", elapsed())

	convB := createConversationOverWire(t, seal1, next1, 2522)
	if convB == convA {
		t.Fatalf("M2: the minted conversation id equals A's (%s)", convA)
	}
	t.Logf("[t=%s] M2: minted conversation B = %s", elapsed(), convB)

	sendAndAwaitEcho(t, seal1, next1, "M3", 2523, convB, "u-b1", textToB, needleToB)
	preB, ok := readEntryByLabel(regPath, convB)
	if !ok {
		t.Fatalf("M3: conversation B (%s) has no session entry after its first message\nfile:\n%s",
			convB, mustReadFile(t, regPath))
	}
	t.Logf("[t=%s] M3: conversation B has run; its session is %s", elapsed(), preB.ID)

	// --- The restart. Stop drops the phone conn with the daemon, so everything
	// below is built fresh over the same $HOME and the same relay.
	_ = phone1.Close()
	h1.Stop(t)
	t.Logf("[t=%s] daemon #1 stopped; conversation B's session is now a persisted entry "+
		"the next pool will not materialise", elapsed())

	h2 := RestartStreamInteractiveWithRelay(t, home, initialUUID, relayURL)
	t.Cleanup(func() { h2.Stop(t) })
	phone2, send2, recv2 := dialPairedPhone(t, fr, home, payload.Token, pubKey, "phone-a2")
	seal2, next2 := noiseWire(t, phone2, send2, recv2)
	t.Logf("[t=%s] daemon #2 is up and the phone is attached", elapsed())

	// --- M4 (AC-1/AC-2): Reset B as the FIRST thing that happens to B on this
	// daemon. No message has woken it; its session is dormant.
	sendNewSessionFrameFor(t, phone2, send2, 2524, convB)

	newB := awaitConversationTransition(t, next2, transitionWant{
		milestone:  "M4 (AC-1/AC-2)",
		convID:     convB,
		forbidConv: convA,
		previous:   preB.ID,
		because: "a new_session naming it, sent before any message on a freshly restarted daemon. Its " +
			"session was dormant — persisted but not materialised — which is exactly the state Reset " +
			"must no longer refuse",
		diag: func() string { return mustReadFile(t, regPath) },
	})
	t.Logf("[t=%s] M4: dormant conversation B rotated %s → %s", elapsed(), preB.ID, newB)

	// AC-2: the fresh binding is PERSISTED, not merely broadcast. Polled because
	// the rotation's save and the observer fan-out are ordered but not instant
	// relative to this reader.
	persisted := false
	for deadline := time.Now().Add(5 * time.Second); !persisted && time.Now().Before(deadline); {
		if e, ok := readEntryByLabel(regPath, convB); ok && e.ID == newB {
			persisted = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !persisted {
		t.Fatalf("M4 (AC-2): conversation B's persisted session id is not the fresh %q; a reset whose "+
			"binding does not survive would be undone by the next restart\nfile:\n%s",
			newB, mustReadFile(t, regPath))
	}

	// --- M5 (AC-1): the first message after the reset must spawn under the FRESH
	// id. The runner logs its own argv, so the spawn record is the direct
	// observable — and it is the only one that can tell "rotated" from "rotated and
	// then resumed the retired transcript anyway", which is the failure mode AC-1
	// names and which every registry assertion above would pass through.
	sendAndAwaitEcho(t, seal2, next2, "M5", 2525, convB, "u-b2", textAfter, needleAfter)
	waitForLogLineAll(t, h2.Stderr, []string{"spawning claude", newB}, 20*time.Second)
	// Matched on the ID FLAGS rather than on the retired id anywhere in the line.
	// The per-session --settings and --append-system-prompt-file paths are named
	// for the id the session was BUILT under and a rotation does not re-key them,
	// so a bare substring search reports every correct spawn as a violation. What
	// AC-1 forbids is the id reaching claude as an identity — and --resume is the
	// specific shape that would reopen the retired transcript.
	for _, line := range strings.Split(h2.Stderr.String(), "\n") {
		if !strings.Contains(line, "spawning claude") {
			continue
		}
		for _, banned := range []string{"--session-id " + preB.ID, "--resume " + preB.ID} {
			if strings.Contains(line, banned) {
				t.Fatalf("M5 (AC-1): a post-reset spawn carried %q — the reset assigned a fresh identity "+
					"and the retired transcript must not be reopened:\n%s", banned, line)
			}
		}
	}
	t.Logf("[t=%s] M5: the first message after the reset spawned under %s", elapsed(), newB)

	// --- Isolation (AC-3): A was never touched. A is bound to the bootstrap
	// session, so its entry still reading initialUUID means no rotation reached any
	// conversation but B.
	e, ok := readBootstrapIfPresent(regPath)
	if !ok || e.ID != initialUUID {
		t.Fatalf("AC-3: conversation A's session is %q, want %q — resetting B must change no other "+
			"conversation\nfile:\n%s", e.ID, initialUUID, mustReadFile(t, regPath))
	}
	t.Logf("[t=%s] isolation: conversation A's session is still %s", elapsed(), initialUUID)
}

// TestRelayV2_StreamClearAfterDaemonRestartResetsDormantConversation is the
// SIBLING ROUTE to the spec above, and it is the defect the first pass of #2521
// shipped: #2456 requires that the verb a client types and the control it presses
// give the same thing, and after a daemon restart only the control worked.
//
// THE ROUTE IS THE TEST. handleSendMessage validates the binding by calling Route
// BEFORE the /clear intercept fires, and Route re-materialises a dormant session
// through Pool.Revive without activating it. So by the time the intercept raises
// the named new_session, the conversation is no longer dormant at all — it is in
// the pool with no child, which is the OTHER state this ticket had to release. The
// reset then asks "has this conversation ever run?" of a session whose evidence
// its own caller has just moved, and until materialise carried the retired entry's
// timestamps the answer was a fresh created_at == last_active_at: never-used,
// inert, nothing visible to the operator.
//
// THE SPEC ABOVE CANNOT CATCH THIS, which is why this one exists rather than an
// extra assertion there. It sends the named frame as the first thing the restarted
// daemon sees, so nothing revives in between and the discriminator is read
// straight off the dormant entry — the arm that was always correct.
//
// The milestones:
//
//	M1  send to A          — the wire works and the bootstrap child is serving.
//	M2  create B           — bound, persisted, no child (#2085).
//	M3  send to B          — B's session is ACTIVATED, the durable fact the reset
//	                         reads back after the restart.
//	--- daemon restart ---
//	M4  "/clear" to B      — the frame under test, typed before any other message.
//
// Conversation A is the isolation control: bound to the bootstrap session, so its
// entry still reading initialUUID at the end means no rotation reached anything
// but B.
func TestRelayV2_StreamClearAfterDaemonRestartResetsDormantConversation(t *testing.T) {
	const (
		initialUUID = "11111111-1111-4111-8111-111111111111" // bootstrap, bound to conversation A
		convA       = "33333333-3333-4333-8333-333333333333"
		textToA     = "e2e-2521-clear:a\n"
		needleToA   = "e2e-2521-clear:a"
		textToB     = "e2e-2521-clear:b\n"
		needleToB   = "e2e-2521-clear:b"
	)

	testStart := time.Now()
	elapsed := func() string { return time.Since(testStart).Round(time.Millisecond).String() }

	home := shortHome(t)
	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	relayURL := fr.URL() + "/v2/server"

	payload, err := paireddevice.Setup(paireddevice.Config{
		Home:                   home,
		InstanceName:           "test",
		Relay:                  relayURL,
		DeviceName:             "phone-a",
		AllowRemotePermissions: false,
	})
	if err != nil {
		t.Fatalf("setup paired device: %v", err)
	}
	pubKey, err := base64.StdEncoding.DecodeString(payload.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	seedBoundConversation(t, home, convA, initialUUID)
	regPath := filepath.Join(home, ".pyry", "test", "sessions.json")

	// --- Daemon #1.
	h1 := StartStreamInteractiveWithRelay(t, home, initialUUID, relayURL)
	t.Cleanup(func() { h1.Stop(t) })
	phone1, send1, recv1 := dialPairedPhone(t, fr, home, payload.Token, pubKey, "phone-a")

	seal1, next1 := noiseWire(t, phone1, send1, recv1)
	sendAndAwaitEcho(t, seal1, next1, "M1", 2531, convA, "u-a1", textToA, needleToA)
	t.Logf("[t=%s] M1: conversation A is live", elapsed())

	convB := createConversationOverWire(t, seal1, next1, 2532)
	if convB == convA {
		t.Fatalf("M2: the minted conversation id equals A's (%s)", convA)
	}
	t.Logf("[t=%s] M2: minted conversation B = %s", elapsed(), convB)

	sendAndAwaitEcho(t, seal1, next1, "M3", 2533, convB, "u-b1", textToB, needleToB)
	preB, ok := readEntryByLabel(regPath, convB)
	if !ok {
		t.Fatalf("M3: conversation B (%s) has no session entry after its first message\nfile:\n%s",
			convB, mustReadFile(t, regPath))
	}
	t.Logf("[t=%s] M3: conversation B has run; its session is %s", elapsed(), preB.ID)

	// --- The restart.
	_ = phone1.Close()
	h1.Stop(t)
	h2 := RestartStreamInteractiveWithRelay(t, home, initialUUID, relayURL)
	t.Cleanup(func() { h2.Stop(t) })
	phone2, send2, recv2 := dialPairedPhone(t, fr, home, payload.Token, pubKey, "phone-a2")
	seal2, next2 := noiseWire(t, phone2, send2, recv2)
	t.Logf("[t=%s] daemon #2 is up; conversation B's session is a persisted entry it has not materialised", elapsed())

	// --- M4: type /clear into B as the FIRST thing that happens to B on this
	// daemon. No echo is awaited and none is owed: the intercept answers an ack and
	// runs the reset instead of delivering the text, so the session_transition IS
	// the observable.
	seal2(protocol.Envelope{
		ID:   2534,
		Type: protocol.TypeSendMessage,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.SendMessagePayload{
			ConversationID: convB,
			MessageID:      "u-b-clear",
			Text:           "/clear",
		}),
	})

	newB := awaitConversationTransition(t, next2, transitionWant{
		milestone:  "M4",
		convID:     convB,
		forbidConv: convA,
		previous:   preB.ID,
		because: "a typed /clear, sent before any other message on a freshly restarted daemon. Route " +
			"revives its dormant session on the way to the intercept, so the reset that follows sees a " +
			"session with no child — and must still read it as previously used",
		diag: func() string { return mustReadFile(t, regPath) },
	})
	t.Logf("[t=%s] M4: /clear rotated dormant conversation B %s → %s", elapsed(), preB.ID, newB)

	// The fresh binding is PERSISTED, not merely broadcast — polled for the same
	// reason the sibling spec polls: the rotation's save and the observer fan-out
	// are ordered but not instant relative to this reader.
	persisted := false
	for deadline := time.Now().Add(5 * time.Second); !persisted && time.Now().Before(deadline); {
		if e, ok := readEntryByLabel(regPath, convB); ok && e.ID == newB {
			persisted = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !persisted {
		t.Fatalf("M4: conversation B's persisted session id is not the fresh %q; a reset whose binding "+
			"does not survive would be undone by the next restart\nfile:\n%s",
			newB, mustReadFile(t, regPath))
	}

	// --- Isolation: A was never touched.
	e, ok := readBootstrapIfPresent(regPath)
	if !ok || e.ID != initialUUID {
		t.Fatalf("isolation: conversation A's session is %q, want %q — a /clear in B must change no "+
			"other conversation\nfile:\n%s", e.ID, initialUUID, mustReadFile(t, regPath))
	}
	t.Logf("[t=%s] isolation: conversation A's session is still %s", elapsed(), initialUUID)
}

// transitionWant is awaitConversationTransition's expectation set.
//
// A STRUCT RATHER THAN A POSITIONAL LIST because five of its fields are strings,
// and adjacent same-typed parameters transpose silently — the hazard
// boundRunSettings' own doc records, and the one this file's registrySeed type
// answers on the harness side. A transposed convID and forbidConv here would
// invert an isolation assertion into a vacuous one.
type transitionWant struct {
	milestone  string
	convID     string        // the conversation whose rotation is under test
	forbidConv string        // a rotation reaching this one is an isolation failure
	previous   string        // the session id the conversation was bound to before
	because    string        // why this milestone expects a transition, for the timeout message
	diag       func() string // read lazily: only the failure paths pay for it
}

// awaitConversationTransition drains to the conversation-scoped session_transition
// for want.convID and answers the fresh id it carries.
//
// Shared by this file's two specs because they differ only in WHICH frame raises
// the rotation — the named new_session a client's Reset control sends, or the
// /clear it types — and #2456 makes "the same thing happens" the contract. Two
// hand-written assertion blocks would be free to drift apart in exactly the place
// the contract says they must not.
//
// The forbidConv check runs BEFORE the convID filter, so an isolation failure is
// reported as one rather than timing out as a missing transition.
func awaitConversationTransition(t *testing.T, next func(time.Time) (protocol.Envelope, bool),
	want transitionWant) string {
	t.Helper()
	deadline := time.Now().Add(25 * time.Second)
	for {
		env, ok := next(deadline)
		if !ok {
			t.Fatalf("%s: never observed a session_transition for conversation %s after %s.\nsessions.json:\n%s",
				want.milestone, want.convID, want.because, want.diag())
		}
		if env.Type != protocol.TypeSessionTransition {
			continue
		}
		var st protocol.SessionTransitionPayload
		if err := json.Unmarshal(env.Payload, &st); err != nil {
			t.Fatalf("%s: decode session_transition payload: %v", want.milestone, err)
		}
		if st.ConversationID == want.forbidConv {
			t.Fatalf("%s: the rotation landed on conversation %s after a reset naming %s: %s → %s",
				want.milestone, want.forbidConv, want.convID, st.PreviousSessionID, st.NewSessionID)
		}
		if st.ConversationID != want.convID {
			continue
		}
		if st.Reason != "clear" {
			t.Errorf("%s: session_transition Reason: got %q, want %q", want.milestone, st.Reason, "clear")
		}
		if st.PreviousSessionID != want.previous {
			t.Errorf("%s: session_transition PreviousSessionID = %q, want the id the conversation was "+
				"bound to before the restart (%q)", want.milestone, st.PreviousSessionID, want.previous)
		}
		if st.NewSessionID == "" || st.NewSessionID == st.PreviousSessionID {
			t.Fatalf("%s: session_transition did not carry a fresh id: previous=%q new=%q",
				want.milestone, st.PreviousSessionID, st.NewSessionID)
		}
		return st.NewSessionID
	}
}

// dialPairedPhone dials the fake relay as an already-paired device and drives the
// handshake, answering the phone and its two cipher states. It is a free function
// rather than inline because this spec does it TWICE — once per daemon — which is
// the whole shape of a restart test.
func dialPairedPhone(t *testing.T, fr *fakerelay.Server, home, token string, pubKey []byte, name string) (
	*fakephone.Client, *noise.CipherState, *noise.CipherState) {
	t.Helper()
	serverID := readPersistedServerID(t, home)
	waitBinaryHello(t, fr, serverID)

	dialCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	phone, err := fakephone.Dial(dialCtx, fr.URL(), serverID, token, name)
	if err != nil {
		t.Fatalf("%s dial: %v", name, err)
	}
	t.Cleanup(func() { _ = phone.Close() })
	send, recv := driveHandshakeToOpenDaemonInteractive(t, phone, pubKey, token)
	return phone, send, recv
}

// noiseWire pairs one connection's sealed sender with its single reader. ONE
// reader per connection is load-bearing rather than tidy: the receive CipherState
// carries a nonce sequence, so a second reader on the same conn would decrypt out
// of order and fail in a way that reads as a protocol bug.
func noiseWire(t *testing.T, phone *fakephone.Client, send, recv *noise.CipherState) (
	seal func(protocol.Envelope), next func(time.Time) (protocol.Envelope, bool)) {
	t.Helper()

	seal = func(env protocol.Envelope) {
		t.Helper()
		raw, err := json.Marshal(env)
		if err != nil {
			t.Fatalf("marshal envelope: %v", err)
		}
		ciphertext, err := send.Encrypt(raw)
		if err != nil {
			t.Fatalf("seal envelope: %v", err)
		}
		sendNoiseMsg(t, phone, ciphertext)
	}

	next = func(deadline time.Time) (protocol.Envelope, bool) {
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
				t.Fatalf("phone decode inner frame: %v", err)
			}
			if inner.Type != protocol.TypeNoiseMsg {
				continue
			}
			return decryptInnerEnvelope(t, inner, recv), true
		}
	}
	return seal, next
}

// sendAndAwaitEcho drives one send_message and drains to the assistant_delta
// echoing it. The ECHO, not the ack, is the proof the conversation's child is
// live and serving — an ack would leave a vacuous pass available to a daemon that
// accepted the turn and never spawned anything.
func sendAndAwaitEcho(t *testing.T, seal func(protocol.Envelope), next func(time.Time) (protocol.Envelope, bool),
	milestone string, reqID uint64, convID, msgID, text, needle string) {
	t.Helper()
	seal(protocol.Envelope{
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
		env, ok := next(deadline)
		if !ok {
			t.Fatalf("%s: never observed an assistant_delta echoing %q for conversation %s",
				milestone, needle, convID)
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

// createConversationOverWire mints a conversation with server defaults and
// answers its id. It drains to conversation_created rather than assuming the next
// frame is it — an interactive session interleaves broadcasts.
func createConversationOverWire(t *testing.T, seal func(protocol.Envelope),
	next func(time.Time) (protocol.Envelope, bool), reqID uint64) string {
	t.Helper()
	seal(protocol.Envelope{
		ID:      reqID,
		Type:    protocol.TypeCreateConversation,
		TS:      time.Now().UTC(),
		Payload: mustJSON(t, protocol.CreateConversationPayload{}),
	})
	deadline := time.Now().Add(15 * time.Second)
	for {
		env, ok := next(deadline)
		if !ok {
			t.Fatal("did not receive conversation_created before deadline")
		}
		if env.Type == protocol.TypeError {
			t.Fatalf("unexpected error envelope while awaiting conversation_created: %s", string(env.Payload))
		}
		if env.Type != protocol.TypeConversationCreated {
			continue
		}
		var p protocol.ConversationCreatedPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			t.Fatalf("decode conversation_created payload: %v", err)
		}
		if p.ID == "" {
			t.Fatal("conversation_created carried an empty id")
		}
		return p.ID
	}
}

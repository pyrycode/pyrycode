//go:build e2e

package e2e

import (
	"bytes"
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

// TestRelayV2_StreamNewSessionRotatesAndRestartsFresh is the LIVE, end-to-end proof
// that `new_session` rotates the session id and restarts the bound runner FRESH —
// with NO typed `/clear` — under interactive_runner:"stream-json" (#1137). #1141
// proved one send_message drains on the stream path; #1136 proved an interrupt stops
// a running turn; this proves the NEXT behaviour on that path.
//
// On the stream path a new session is a fresh SPAWN with a new id, not a `/clear`
// keystroke (that is the terminal/PTY-path mechanism). The production pieces are
// shipped and unit-tested: Pool.RotateForNewSession (#1125, mints a fresh id,
// re-keys the pool, rebinds the conversation, persists, fires a ReasonClear
// transition), (*streamsup.Runner).RestartFresh (#1124, rotates the runner id and
// re-spawns fresh), and startFreshRunner's type-switch dispatch wiring them together
// (#1125). This spec is the first time all of them run TOGETHER, live, over the
// stream toggle against the fakeclaude stream-json harness:
//
//	phone send_message #1(knownConvID,"…one") → Route [stamps active = knownConvID] →
//	  boundSession.WriteUserTurn → stream fakeclaude echo + result → daemon Parser →
//	  drain gate (sink tag == active == initialUUID ✓) → assistant_delta{"…one"}   ── M1
//	phone new_session → handleNewSession → activeSessionStarter → resolveBound(knownConvID)
//	  → the bootstrap stream runner → startFreshRunner's RestartFresh arm:
//	  RotateForNewSession(initialUUID) mints newID + saveLocked + rebind + ReasonClear →
//	  RestartFresh(newID) re-spawns claude --session-id newID (NO /clear keystroke)
//	    registry id: initialUUID → newID                                            ── M2
//	    session_transition{clear, initialUUID → newID, knownConvID} broadcast        ── M3
//	phone send_message #2(knownConvID→newID,"…two") → ack → the FRESH child's stdin   ── M4
//	the stdin log never carries "/clear"                                             ── M5
//
// THE STREAM TWIN OF TestRelayV2_NewSessionRotatesOnDisk (the PTY new_session e2e).
// The PTY test asserts the fsnotify watcher follows claude's self-rotated
// <uuid>.jsonl into the registry after a typed /clear; the stream test asserts the
// DAEMON-minted rotation lands directly — no watcher, no /clear.
//
// WHY M2's ON-DISK ROTATION IS A STRONG PROOF OF THE WHOLE MECHANISM (not just a
// pool write). startFreshRunner's stream arm is a SINGLE path — newID, _ :=
// rotate(oldID); v.RestartFresh(string(newID)) — so a persisted rotation
// initialUUID → newID (from RotateForNewSession's saveLocked) implies
// RestartFresh(newID) was invoked immediately after with the same id. Registry
// rotated ⟹ the bound runner restarted fresh under newID.
//
// THE POST-ROTATION DRAIN DIVERGENCE (why M4 is proven at the fake boundary, not the
// phone). The stream turn-drain gate forwards an event only when the producing
// runner's sink tag equals the active conversation's bound session id. The sink tag
// is fixed at runner CONSTRUCTION (cfg.SessionID = initialUUID) and RestartFresh
// re-spawns the child IN PLACE — it never rebuilds the runner/Parser/sink — so the
// runner's events stay tagged initialUUID forever, while the conversation rebinds to
// newID. So a turn issued AFTER the rotation has its delta dropped at the gate and
// never reaches the phone. This is a latent gap in the opt-in, still-under-construction
// stream path (a SEPARATE concern from new_session routing, which this ticket proves
// works; flagged for a production follow-up), and is why M4 asserts the fresh child's
// STDIN, not a phone-side delta — asserting a delta would HANG.
func TestRelayV2_StreamNewSessionRotatesAndRestartsFresh(t *testing.T) {
	const (
		initialUUID   = "11111111-1111-4111-8111-111111111111" // bootstrap
		knownConvID   = "22222222-2222-4222-8222-222222222222"
		userTextOne   = "e2e-1137-user:one\n"
		echoNeedleOne = "e2e-1137-user:one"
		userTextTwo   = "e2e-1137-user:two\n"
		echoNeedleTwo = "e2e-1137-user:two"
		sendReqIDOne  = uint64(1137)
		sendReqIDTwo  = uint64(1237)
	)

	// Every milestone line below carries the wall-clock elapsed since this point,
	// so a failing run's output shows WHERE the time went instead of leaving a
	// single total to be reconstructed by hand (#1273: the 20.21 s failure was
	// read as a slow machine when it was really a full 20.00 s deadline burn on a
	// 0.21 s prefix). Captured before shortHome/pairing/daemon startup so the
	// stamp covers that prefix too — it is exactly the quantity that arithmetic
	// turns on. go test prints buffered t.Logf output when a test FAILS, not only
	// under -v, so M1–M3's stamps ride along with any later failure for free.
	testStart := time.Now()
	elapsed := func() string { return time.Since(testStart).Round(time.Millisecond).String() }

	home := shortHome(t)

	// Pair one interactive device.
	rA := RunBareIn(t, home, "pair", "-pyry-name=test", "--name=phone-a")
	if rA.ExitCode != 0 {
		t.Fatalf("pyry pair phone-a exit=%d\nstdout:\n%s\nstderr:\n%s", rA.ExitCode, rA.Stdout, rA.Stderr)
	}
	payloadA := decodePairPayload(t, rA.Stdout)
	pubKey, err := base64.StdEncoding.DecodeString(payloadA.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	// Bind knownConvID to the bootstrap session (initialUUID). Load-bearing twice:
	// it stamps the id alignment that makes M1's pre-rotation drain pass (the runner's
	// construction-time sink tag == activeSession() == initialUUID), and — via the M1
	// send_message that stamps the active-conversation cursor — it lets new_session
	// resolve to the bound runner (the #1125 routing prerequisite). The daemon loads
	// conversations.json once at startup, so the row must exist BEFORE it starts.
	seedBoundConversation(t, home, knownConvID, initialUUID)

	// The stdin tee target. StartStreamInteractiveWithRelay flows this env to the
	// daemon's process env, inherited by BOTH the bootstrap child and the fresh
	// post-rotation child (append-mode, so both accumulate into one log — see the
	// fakeclaude stream-mode tee).
	stdinLog := filepath.Join(t.TempDir(), "fakeclaude-stdin.log")

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })

	regPath := filepath.Join(home, ".pyry", "test", "sessions.json")

	h := StartStreamInteractiveWithRelay(t, home, initialUUID, fr.URL()+"/v2/server",
		"PYRY_FAKE_CLAUDE_STDIN_LOG="+stdinLog)
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
	// Interactive — the capability handleNewSession AND the structured stream require.
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

	// nextEnv decrypts the next binary→phone application envelope, skipping non-noise_msg
	// inner frames in capture order so the receive nonce stays in sequence. One recvA is
	// used for the whole test (the single reader, same as the send / interrupt specs).
	// ok=false on deadline.
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

	// --- M1 (AC-1 precondition): pre-rotation baseline. Drive send_message #1 to the
	// bootstrap-bound conversation, await its sealed ack (the cursor is now stamped to
	// knownConvID), then drain until an assistant_delta whose Text carries the echoed
	// prompt. This is #1141's send test verbatim: it proves the child is live and the
	// PRE-rotation turn drains to the phone (the gate passes because the sink tag ==
	// active == initialUUID). The echo is the non-vacuity guard — a full round-trip.
	sealSend(protocol.Envelope{
		ID:   sendReqIDOne,
		Type: protocol.TypeSendMessage,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.SendMessagePayload{
			ConversationID: knownConvID,
			MessageID:      "u-1",
			Text:           userTextOne,
		}),
	})
	ackDeadline := time.Now().Add(15 * time.Second)
	gotAck := false
	for !gotAck {
		env, ok := nextEnv(ackDeadline)
		if !ok {
			t.Fatal("M1: never received the send_message #1 ack; the pre-rotation turn was never accepted " +
				"(so the active cursor was never stamped and new_session could not resolve the bound runner)")
		}
		if env.Type == protocol.TypeError {
			t.Fatalf("M1: unexpected error envelope while awaiting the send_message #1 ack: %s", string(env.Payload))
		}
		if env.Type == protocol.TypeAck && env.InReplyTo != nil && *env.InReplyTo == sendReqIDOne {
			gotAck = true
		}
	}

	sawDelta := false
	m1Deadline := time.Now().Add(20 * time.Second)
	for !sawDelta {
		env, ok := nextEnv(m1Deadline)
		if !ok {
			t.Fatal("M1: interactive phone A never observed an assistant_delta for the pre-rotation turn; it " +
				"never drained end-to-end (delivery never reached the child, or the parser / drain gate / emitter " +
				"dropped it — most likely a UUID mismatch between seedBootstrapRegistry and seedBoundConversation)")
		}
		if env.Type != protocol.TypeAssistantDelta {
			continue
		}
		var d protocol.AssistantDeltaPayload
		if err := json.Unmarshal(env.Payload, &d); err != nil {
			t.Fatalf("phone A decode assistant_delta payload: %v", err)
		}
		if d.ConversationID != knownConvID {
			t.Errorf("assistant_delta ConversationID: got %q, want %q", d.ConversationID, knownConvID)
		}
		if !strings.Contains(d.Text, echoNeedleOne) {
			t.Fatalf("M1: assistant_delta did not carry the echoed prompt; got Text=%q, want it to contain %q "+
				"(the round-trip proof — fakeclaude echoes the sent prompt back through the daemon)", d.Text, echoNeedleOne)
		}
		sawDelta = true
	}
	t.Logf("[t=%s] M1: observed assistant_delta echoing the pre-rotation prompt — the child is live and the turn drained", elapsed())

	// Baseline (non-vacuity for M2): the daemon reconciled the bootstrap id to
	// initialUUID before any rotation, so the "id changed" assertion below is
	// non-vacuous.
	pre := waitForBootstrapID(t, regPath, initialUUID, 5*time.Second)

	// --- new_session actuation (AC-1) + M2 (AC-2). Re-send new_session every ~250 ms
	// until the registry id rotates away from initialUUID or a ~10 s deadline elapses.
	// new_session is fire-and-forget and drops silently on a detached session, so
	// re-send exactly as the PTY sibling (relay_v2_new_session_test.go) does; after M1
	// the child is live, so the first frame should actuate and the loop is defensive
	// against timing. `post` captures the FIRST id change, so its owning transition
	// (M3) is the rotation FROM initialUUID even in the rare double-actuation case.
	// Fresh envelope ids per send keep the send CipherState nonce advancing.
	var reqID uint64 = 1
	var post registryEntry
	rotated := false
	deadline := time.Now().Add(10 * time.Second)
	for !rotated && time.Now().Before(deadline) {
		sendNewSessionFrame(t, phoneA, sendA, reqID)
		reqID++
		poll := time.Now().Add(250 * time.Millisecond)
		for time.Now().Before(poll) {
			if e, ok := readBootstrapIfPresent(regPath); ok && e.ID != "" && e.ID != initialUUID {
				post = e
				rotated = true
				break
			}
			time.Sleep(25 * time.Millisecond)
		}
	}
	if !rotated {
		t.Fatalf("M2 (AC-1/AC-2): registry bootstrap id never rotated away from %q after new_session within 10s; "+
			"the stream fresh-restart did not actuate (the frame dropped on a detached session, the "+
			"streamRunner.RestartFresh forwarder is missing — silent-inert — or the active cursor was never "+
			"stamped)\nfile:\n%s", initialUUID, mustReadFile(t, regPath))
	}

	// M2: assert the rotation on disk — the exact shape the PTY test uses. Registry
	// rotated ⟹ RestartFresh(post.ID) ran (startFreshRunner's stream arm is a single
	// rotate-then-RestartFresh path): "session id rotates" + "restarts fresh".
	if !uuidStemPattern.MatchString(post.ID) {
		t.Errorf("post-rotation id %q does not match UUIDv4 stem pattern", post.ID)
	}
	if !post.LastActiveAt.After(pre.LastActiveAt) {
		t.Errorf("last_active_at did not advance: pre=%s post=%s",
			pre.LastActiveAt.Format(time.RFC3339Nano),
			post.LastActiveAt.Format(time.RFC3339Nano))
	}
	t.Logf("[t=%s] M2: registry rotated %s → %s — the id rotated and the bound runner restarted fresh", elapsed(), initialUUID, post.ID)

	// --- M3 (AC-2 "the client observes …"): drain phone envelopes until the
	// session_transition broadcast for the rotation. notifyTransition(ReasonClear)
	// fans this to every interactive conn — the phone-side, different-fabric observable
	// of the fresh-session break, carrying the daemon-minted new id. Match on post.ID
	// (skipping any trailing turn #1 frames or a second rotation's transition), then
	// assert the full shape. A generous deadline tolerates interleaving.
	sawTransition := false
	m3Deadline := time.Now().Add(15 * time.Second)
	for !sawTransition {
		env, ok := nextEnv(m3Deadline)
		if !ok {
			t.Fatalf("M3: never observed a session_transition{new_session_id:%s} after the rotation; the "+
				"transition emitter did not broadcast the clear, or the resolver could not map the new id → the "+
				"conversation (M2 green + M3 red ⟹ emitter/resolver, not rotation)", post.ID)
		}
		if env.Type != protocol.TypeSessionTransition {
			continue
		}
		var st protocol.SessionTransitionPayload
		if err := json.Unmarshal(env.Payload, &st); err != nil {
			t.Fatalf("phone A decode session_transition payload: %v", err)
		}
		if st.NewSessionID != post.ID {
			continue // a trailing/second-rotation transition — keep looking for post.ID's
		}
		if st.Reason != "clear" {
			t.Errorf("session_transition Reason: got %q, want %q", st.Reason, "clear")
		}
		if st.PreviousSessionID != initialUUID {
			t.Errorf("session_transition PreviousSessionID: got %q, want %q", st.PreviousSessionID, initialUUID)
		}
		if st.ConversationID != knownConvID {
			t.Errorf("session_transition ConversationID: got %q, want %q", st.ConversationID, knownConvID)
		}
		sawTransition = true
	}
	t.Logf("[t=%s] M3: observed session_transition{clear, %s → %s} for the conversation — the client saw the rotation", elapsed(), initialUUID, post.ID)

	// --- M4 (AC-2 "…serving a subsequent turn"): the post-rotation session serves a
	// subsequent turn. send_message #2 to knownConvID (now rebound to post.ID) → await
	// its ack: Route resolves knownConvID → CurrentSessionID == post.ID → Pool.Lookup
	// HIT (the pool was re-keyed), proving the rotated session accepts the turn. Then
	// poll the stdin log until it carries the …two bytes — the FRESH child received and
	// is serving the subsequent turn.
	//
	// We deliberately do NOT drain a phone-side assistant_delta for turn #2: on the
	// opt-in stream path its delta is dropped at the drain gate (the runner's sink keeps
	// its construction-time tag while the conversation rebinds — see the header). The
	// stdin-log delivery is the achievable "serving" observable.
	sealSend(protocol.Envelope{
		ID:   sendReqIDTwo,
		Type: protocol.TypeSendMessage,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.SendMessagePayload{
			ConversationID: knownConvID,
			MessageID:      "u-2",
			Text:           userTextTwo,
		}),
	})
	gotAck2 := false
	ack2Deadline := time.Now().Add(15 * time.Second)
	for !gotAck2 {
		env, ok := nextEnv(ack2Deadline)
		if !ok {
			t.Fatal("M4: never received the send_message #2 ack; the rotated session did not accept the " +
				"subsequent turn (Route failed to resolve knownConvID → the re-keyed pool id)")
		}
		if env.Type == protocol.TypeError {
			t.Fatalf("M4: unexpected error envelope while awaiting the send_message #2 ack: %s", string(env.Payload))
		}
		if env.Type == protocol.TypeAck && env.InReplyTo != nil && *env.InReplyTo == sendReqIDTwo {
			gotAck2 = true
		}
	}

	// The stdin log's size the moment ack #2 landed, paired with its size at the
	// poll deadline below. Growth between the two falsifies "no bytes arrived at
	// all": the log is opened O_APPEND with a per-write Sync and BOTH the bootstrap
	// and the fresh post-rotation child accumulate into this one file, so growth
	// means some child was alive and receiving — separating that from "bytes
	// arrived, but not turn #2's". Both probes use the SAME instrument
	// (os.ReadFile + len) as the poll loop and as the log=%q the failure prints:
	// the file is appended to concurrently, and mixing an os.Stat size into one end
	// would make the delta unattributable. The elapsed stamp is load-bearing, not
	// decoration — there is no milestone line between M3 and an M4 failure, so the
	// up-to-15 s ack #2 wait is otherwise invisible, and whether ack #2 landed at
	// t=0.2 s or t=14 s is the slowness-vs-stall discriminator.
	ackTwoAt := elapsed()
	ackTwoBytes, ackTwoErr := os.ReadFile(stdinLog)

	// Poll the stdin log until the fresh child has received turn #2. The daemon's
	// inbound queue retries WriteUserTurn until the fresh child is stdin-ready, so a
	// generous deadline absorbs the fresh spawn.
	//
	// This deadline stays where it is, deliberately (#1273). The msgqueue drain
	// BLOCKS inside the delivery seam waiting for claude to go idle (#704) with no
	// periodic retry, so a missed wake-up is an unbounded stall and 20 s, 60 s and
	// 300 s are the same red — raising it cannot fix the failure it would hide. Nor
	// is the test wrapped in a re-run: auto-retrying before reporting red converts a
	// possible permanent-stall defect into an invisible one, the false-green shape
	// that already shipped an unverified change once (#1168 / PR #1169, where a SKIP
	// exited 0 and read as a pass). The diagnostics below make the next occurrence
	// decidable instead; that is the whole scope here.
	//
	// The read error is KEPT rather than discarded: without it len(nil) == 0 renders
	// an unreadable file as "the file was empty" — a broken instrument reporting
	// itself as a measurement, which is exactly the ambiguity this change closes.
	var logBytes []byte
	var logErr error
	turnTwoDeadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(turnTwoDeadline) {
		logBytes, logErr = os.ReadFile(stdinLog)
		if bytes.Contains(logBytes, []byte(echoNeedleTwo)) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !bytes.Contains(logBytes, []byte(echoNeedleTwo)) {
		// Re-read the registry's bootstrap id ONE-SHOT and report it beside the
		// post.ID captured at M2. A mismatch means a SECOND rotation re-keyed the
		// binding out from under turn #2's in-flight write — the M2 loop re-sends
		// new_session every ~250 ms and this file's own comments already anticipate
		// double-actuation — a reading the old message could not tell from a stall.
		// readBootstrapIfPresent reads the same file and the same field M2 did, so a
		// mismatch has exactly one reading; comparing against conversations.json's
		// CurrentSessionID would confound "second rotation" with "cross-file skew".
		// It is also the only admissible reader here: nothing on this failure path may
		// call a t.Fatal*-ing helper (readBootstrap, waitForBootstrapID*, mustReadFile,
		// boundSessionID), or its fatal fires first and this message never prints.
		//
		// ok=false conflates missing / unparseable / no bootstrap row, so it renders as
		// a sentinel — never as "", which would read as "the rotation lost the id" and
		// fabricate a finding out of a broken instrument.
		regNowID := "<no bootstrap entry: registry missing, unparseable, or bootstrap row absent>"
		if e, ok := readBootstrapIfPresent(regPath); ok {
			regNowID = e.ID
		}
		t.Fatalf("M4: the fresh child never received the subsequent turn; the stdin log has no %q bytes "+
			"(delivery never reached it, or the rotated session did not re-spawn). ack #2 received=%t\n"+
			"stdin log: %d bytes at ack #2 (t=%s, read err: %v) → %d bytes at expiry (read err: %v) "+
			"— no growth ⟹ no bytes arrived at all; growth ⟹ bytes arrived, but not turn #2's\n"+
			"registry bootstrap id: %q at M2 (post.ID) → %q re-read now — a mismatch ⟹ a second rotation "+
			"re-keyed the binding under turn #2\nlog=%q",
			echoNeedleTwo, gotAck2,
			len(ackTwoBytes), ackTwoAt, ackTwoErr, len(logBytes), logErr,
			post.ID, regNowID, logBytes)
	}
	t.Logf("[t=%s] M4: the fresh post-rotation child received the subsequent turn (stdin log carries %q)", elapsed(), echoNeedleTwo)

	// --- M5 (AC-3): no "/clear" on the stream path. On the stream path new_session is
	// a process re-spawn (RestartFresh), never a keystroke, so no child ever receives
	// "/clear". Read the full stdin log and assert it (a) captured a user turn — the
	// non-vacuity guard proving the tee is actually wired — and (b) never contains
	// "/clear".
	logBytes, _ = os.ReadFile(stdinLog)
	if !bytes.Contains(logBytes, []byte(echoNeedleOne)) && !bytes.Contains(logBytes, []byte(`"type":"user"`)) {
		t.Fatalf("M5 non-vacuity: the stdin log captured no user turn (neither %q nor `\"type\":\"user\"`); the "+
			"tee is not wired or PYRY_FAKE_CLAUDE_STDIN_LOG never reached the child, so the no-/clear assertion "+
			"below would pass vacuously\nlog=%q", echoNeedleOne, logBytes)
	}
	if bytes.Contains(logBytes, []byte("/clear")) {
		t.Fatalf("M5 (AC-3): the stdin log contains \"/clear\"; the stream new_session mis-routed to the PTY "+
			"/clear path (a startFreshRunner type-switch regression) — the stream path must re-spawn, never type "+
			"/clear\nlog=%q", logBytes)
	}
	t.Logf("[t=%s] M5: the stdin log captured user turns and never a /clear — the stream new_session re-spawned, no keystroke (AC-3)", elapsed())
}

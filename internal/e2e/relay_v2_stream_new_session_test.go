//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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
//	phone send_message #2(knownConvID→newID,"…two") → ack → the FRESH child's OWN
//	  per-child stdin log (<stem>.<post.ID>), never the outgoing child's            ── M4
//	  → drain gate (sink tag == active == post.ID ✓) → assistant_delta{"…two"}      ── M6
//	no child's stdin log ever carries "/clear"                                      ── M5
//
// THIS IS NOW THE ONLY new_session e2e IN THIS PACKAGE. Its PTY twin
// (TestRelayV2_NewSessionRotatesOnDisk) went with the terminal-driving interactive
// path in #1348, and the mechanism that twin asserted — claude self-rotated on a
// typed /clear and an fsnotify watcher followed the new <uuid>.jsonl into the
// registry — was retired in #2137. What survives, and what this test asserts, is the
// DAEMON-minted rotation landing directly: no /clear keystroke anywhere.
//
// WHY M2's ON-DISK ROTATION IS A STRONG PROOF OF THE WHOLE MECHANISM (not just a
// pool write). startFreshRunner's stream arm is a SINGLE path — newID, _ :=
// rotate(oldID); v.RestartFresh(string(newID)) — so a persisted rotation
// initialUUID → newID (from RotateForNewSession's saveLocked) implies
// RestartFresh(newID) was invoked immediately after with the same id. Registry
// rotated ⟹ the bound runner restarted fresh under newID.
//
// THE POST-ROTATION DRAIN CONVERGENCE (why M4 and M6 are BOTH here, and why neither
// subsumes the other). The stream turn-drain gate forwards an event only when the
// producing runner's sink tag equals the active conversation's bound session id.
// RestartFresh re-spawns the child IN PLACE — it never rebuilds the
// runner/Parser/sink — so until #1133 that tag stayed frozen at cfg.SessionID
// (= initialUUID) while the conversation rebound to newID, and every post-rotation
// delta was dropped at the gate. That was this file's stated divergence, and M4
// asserted the fresh child's STDIN precisely because a delta assertion would then
// have hung.
//
// #1133 makes the tag LIVE: newStreamRunnerFactory binds one streamSessionTag to
// both fan-in lanes and RestartFresh moves it through
// streamsup.Config.OnSessionRotate, so after the rotation the runner tags its events
// post.ID, the gate's UNCHANGED exact-match comparison admits them, and M6 asserts
// the delta the old divergence made unassertable. M6 is this file's regression proof
// for that ticket: revert the tag rotation and M6 is the milestone that times out.
//
// M4 STAYS, and not as a leftover. The two milestones answer different questions and
// each is blind to the other's: M4 names WHICH CHILD served the turn — a turn
// delivered to the outgoing child leaves the post-rotation child's per-child log
// needle-free — while M6 proves the resulting event reaches the CLIENT. A turn
// mis-routed to the outgoing child could still produce a phone-side delta once the
// tag rotates, so M6 alone would not catch the #1330-class defect M4 exists for; and
// a correctly-routed turn whose events the gate still drops passes M4 while the
// stream is dark, which is the defect M6 exists for.
//
// WHY THAT STDIN EVIDENCE IS PER-CHILD (#1331). The tee's env value is a path STEM and
// each child appends to <stem>.<its own session id>, so M4 reads ONLY the log of the
// child the daemon spawned with --session-id post.ID. Under the retired one-shared-file
// contract a needle proved that SOME child received the turn and never which, so a turn
// delivered to the OUTGOING child passed M4 — and the run then surfaced downstream as an
// AC-1 failure whose two named readings were both wrong, a broken-instrument report about
// a working instrument. M5 spans EVERY per-child log for the same reason in reverse: a
// "/clear" typed at the outgoing child must still be caught, and turn #1's needle (the
// outgoing child's bytes) is what discharges M5's non-vacuity.
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

	// The stdin tee target — a path STEM, not a file. StartStreamInteractiveWithRelay
	// flows this env to the daemon's process env, inherited identically by BOTH the
	// bootstrap child and the fresh post-rotation child; each of them appends its own
	// stdin to <stem>.<its own session id> (fakeclaude's stream tee, #1331). That
	// per-child split is what lets M4 name the child that received turn #2: one shared
	// file could only ever say SOME child did, so a turn delivered to the outgoing child
	// read as green. No ".log" suffix — the id takes the place the extension used to
	// hold, and the bare stem's absence stays meaningful (only the PTY tee writes it).
	stdinLogStem := filepath.Join(t.TempDir(), "fakeclaude-stdin")

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })

	regPath := filepath.Join(home, ".pyry", "test", "sessions.json")

	h := StartStreamInteractiveWithRelay(t, home, initialUUID, fr.URL()+"/v2/server",
		"PYRY_FAKE_CLAUDE_STDIN_LOG="+stdinLogStem)
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
	// The phone-side delta for turn #2 is M6, below. It is drained AFTER this
	// milestone rather than instead of it: this one names which child served the
	// turn, M6 proves the resulting event reaches the client, and the header explains
	// why neither implies the other.
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

	// The POST-ROTATION child's stdin log's size the moment ack #2 landed, paired
	// with its size at the poll deadline below. Growth between the two falsifies "no
	// bytes arrived at all" FOR THAT CHILD: the file is opened O_APPEND with a
	// per-write Sync and exactly ONE child ever writes it (#1331), so growth means
	// the post-rotation child was alive and receiving — separating that from "bytes
	// arrived, but not turn #2's". Which child the bytes went to is the other
	// dimension entirely, and the per-child inventory in the failure record carries
	// it.
	//
	// A read error here is INFORMATIVE, not just noise to be kept: the child opens
	// its log before it reads its first stdin byte, so an ENOENT at ack #2 means the
	// fresh child had not started yet at that moment — a reading no byte count can
	// express, and one the shared-file instrument could never produce.
	//
	// Both probes use the SAME instrument (os.ReadFile + len) as the poll loop and as
	// the log=%q the failure prints, on the SAME per-child path: the file is appended
	// to concurrently, and mixing an os.Stat size into one end would make the delta
	// unattributable. The elapsed stamp is load-bearing, not decoration — there is no
	// milestone line between M3 and an M4 failure, so the up-to-15 s ack #2 wait is
	// otherwise invisible, and whether ack #2 landed at t=0.2 s or t=14 s is the
	// slowness-vs-stall discriminator.
	ackTwoAt := elapsed()
	ackTwoBytes, ackTwoErr := os.ReadFile(childStdinLog(stdinLogStem, post.ID))

	// The daemon log's length at that same moment — the failure record's window
	// boundary (#1296). A byte OFFSET, not a timestamp: safeBuffer is append-only
	// (its whole method set is Write/String/Bytes — no Reset, no Read, and nothing
	// in the harness truncates it), so byte N means the same byte for the life of
	// the test and snapshot[ackTwoLogLen:] is exactly "what the daemon logged
	// after ack #2" — no clock, no parsing, no coupling to the log's format. Same
	// instrument at both ends (h.Stderr.Bytes() + len) for the reason the
	// paragraph above gives about the stdin log; measuring by copying the buffer
	// costs a few KB and is the right trade for instrument consistency.
	//
	// The boundary is approximate, not exact: os/exec's stderr-copy goroutine is
	// asynchronous, so a line written microseconds before this point may land just
	// after it. Against a 20 s window at msgqueue's 1 s retry cadence that cannot
	// change any reading below, but the record should not pretend otherwise.
	ackTwoLogLen := len(h.Stderr.Bytes())

	// Poll the POST-ROTATION child's OWN stdin log until it has received turn #2. The
	// daemon's inbound queue retries WriteUserTurn until the fresh child is
	// stdin-ready, so a generous deadline absorbs the fresh spawn.
	//
	// Reading only <stem>.<post.ID> is the assertion (#1331): a turn delivered solely
	// to the outgoing child (initialUUID) leaves this file needle-free and fails M4,
	// where the retired shared-file read passed it. The needle can only appear here if
	// a child that the daemon pinned to post.ID wrote it, so a drift between this
	// derivation and fakeclaude's fails LOUD rather than silently green.
	//
	// This deadline stays where it is, deliberately (#1273) — though NOT for the
	// reason this comment used to give, which was wrong on both clauses (#1296).
	// The drain is not retry-less: on the error arm it re-attempts the same head
	// every defaultRetryInterval (1 s) and abandons it after defaultGiveUpAfter
	// (2 m). And the two parked waits are BOUNDED, not unbounded —
	// inboundActivateTimeout (30 s) and streamTurnHoldTimeout (15 min). What they
	// are is SILENT at the daemon's Info level, for the whole of this 20 s window,
	// which sits below both. Raising the deadline still cannot fix the failure, and
	// the reason is the OUTCOME, not the silence: past 15 min a wedged hold gives up
	// and surfaces a typed session_error, and everything shorter just spends longer
	// inside the same stall — 20 s, 60 s and 300 s are all red. A longer deadline
	// buys only more evidence, and attaching the daemon's log to the failure below
	// buys that evidence without paying the wall-clock. Nor is the test wrapped in a
	// re-run: auto-retrying before reporting red converts a possible permanent-stall
	// defect into an invisible one, the false-green shape that already shipped an
	// unverified change once (#1168 / PR #1169, where a SKIP exited 0 and read as a
	// pass).
	//
	// The read error is KEPT rather than discarded: without it len(nil) == 0 renders
	// an unreadable file as "the file was empty" — a broken instrument reporting
	// itself as a measurement, which is exactly the ambiguity this change closes.
	var logBytes []byte
	var logErr error
	gotTurnTwo := false
	turnTwoDeadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(turnTwoDeadline) {
		gotTurnTwo, logBytes, logErr = childReceivedTurn(stdinLogStem, post.ID, []byte(echoNeedleTwo))
		if gotTurnTwo {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !gotTurnTwo {
		// Re-read the registry's bootstrap id ONE-SHOT and report it beside the
		// post.ID captured at M2. A mismatch means a SECOND rotation re-keyed the
		// binding out from under turn #2's in-flight write — the M2 loop re-sends
		// new_session every ~250 ms and this file's own comments already anticipate
		// double-actuation — a reading the old message could not tell from a stall.
		// readBootstrapIfPresent reads the same file and the same field M2 did, so a
		// mismatch has exactly one reading; comparing against conversations.json's
		// CurrentSessionID would confound "second rotation" with "cross-file skew".
		// It is also the only admissible reader here: nothing on this failure path may
		// call a t.Fatal*-ing helper (waitForBootstrapID, mustReadFile,
		// boundSessionID), or its fatal fires first and this message never prints.
		//
		// ok=false conflates missing / unparseable / no bootstrap row, so it renders as
		// a sentinel — never as "", which would read as "the rotation lost the id" and
		// fabricate a finding out of a broken instrument. The `e.ID != ""` clause is the
		// same guard M2's own read carries: readBootstrapIfPresent does
		// not require a non-empty id, and an empty one would print as a MANUFACTURED
		// second-rotation proof — flagged in docs/knowledge/codebase/1273.md as a
		// fold-in for the next ticket touching this file, which is this one.
		regNowID := "<no bootstrap entry: registry missing, unparseable, bootstrap row absent, or its id empty>"
		if e, ok := readBootstrapIfPresent(regPath); ok && e.ID != "" {
			regNowID = e.ID
		}
		// The attribution dimension (#1331), and the reason it is rendered ahead of
		// every delivery reading below: those readings all presume the turn was
		// heading for the right child. childStdinLogs and childStdinInventory are
		// fatal-free for the same reason readBootstrapIfPresent is.
		inventoryLogs, inventoryErr := childStdinLogs(stdinLogStem)
		t.Fatalf("M4: the POST-ROTATION child — the one the daemon spawned with --session-id %s — never received "+
			"the subsequent turn; that child's OWN stdin log has no %q bytes (delivery never reached it, it "+
			"reached the OUTGOING child instead, or the rotated session did not re-spawn). ack #2 received=%t\n"+
			"post-rotation child's stdin log: %d bytes at ack #2 (t=%s, read err: %v) → %d bytes at expiry "+
			"(read err: %v) — no growth ⟹ no bytes reached THAT child at all; growth ⟹ bytes reached it, but not "+
			"turn #2's\n%s\n"+
			"read that inventory FIRST: bytes under the outgoing id and none under the post-rotation id ⟹ the turn "+
			"was delivered to the child the rotation replaced — a delivery-ordering defect (#1330's class, which "+
			"this per-child attribution exists to make visible), NOT a stall, and none of the delivery readings "+
			"below apply to it\n"+
			"registry bootstrap id: %q at M2 (post.ID) → %q re-read now — a mismatch ⟹ a second rotation "+
			"re-keyed the binding under turn #2\n%s\n"+
			"how to read that count pair (the harness starts pyry WITH -pyry-verbose, so the daemon logs at "+
			"Debug and BOTH of msgqueue's drain arms are visible here — #1318): retry Warns > 0 ⟹ the "+
			"ERROR-RETURN arm — DeliverFunc returned a non-nil error the Pending classifier did NOT match, so "+
			"the head stayed queued and was re-attempted every defaultRetryInterval (1s), ~1 Warn per second "+
			"being the signature, and the fresh child never became writable; no give-up is possible inside "+
			"this window (defaultGiveUpAfter is 2m). pending/holds > 0 ⟹ the HOLD arm — delivery declined "+
			"with a Pending-classified error, the give-up streak reset, and the SAME head retried at that "+
			"same 1s cadence. Both > 0 ⟹ the drain moved between the two arms inside the window.\n"+
			"BUT the hold arm is structurally UNREACHABLE on this harness, so pending/holds: 0 is the "+
			"expected reading rather than an empirical one, and a NON-zero count is a finding about the "+
			"delivery path (something changed under this test), not about the stall: cmd/pyry wires exactly "+
			"one Pending classifier, errors.Is(err, supervisor.ErrTrustModalPending) (main.go:932), and that "+
			"sentinel's only production producer is the PTY delivery gate — whereas this harness runs "+
			"interactive_runner:\"stream-json\", whose WriteUserTurn returns only ErrNoLiveChild, "+
			"turncommit.ErrDropped or nil (internal/streamsup/runner.go:283). The count is kept as a CONTRACT "+
			"check on that chain, which is worth knowing on sight.\n"+
			"BOTH counts 0 narrows the field to exactly two states — and NO log level makes either visible, "+
			"because they emit nothing at any level: parked in Activate (bounded by inboundActivateTimeout, "+
			"30s), which newInboundDeliver does not log around, and parked in the mid-turn hold "+
			"(streamTurnHoldTimeout, 15m), whose whole waitIdleForDelivery → WaitIdle chain is silent. That "+
			"pair is NOT decided by this record; raising the level further would not decide it either.\nlog=%q",
			post.ID, echoNeedleTwo, gotAck2,
			len(ackTwoBytes), ackTwoAt, ackTwoErr, len(logBytes), logErr,
			childStdinInventory(inventoryLogs, inventoryErr, initialUUID, post.ID),
			post.ID, regNowID,
			daemonLogWindow(h.Stderr.Bytes(), ackTwoLogLen, daemonLogBudget),
			logBytes)
	}
	t.Logf("[t=%s] M4: the post-rotation child (--session-id %s) received the subsequent turn — its OWN stdin log carries %q, so the turn was served by the FRESH child and not by the outgoing one", elapsed(), post.ID, echoNeedleTwo)

	// --- M6 (#1133): the post-rotation turn's delta REACHES THE PHONE. The runner's
	// sink tag rotated with the runner, so the drain gate now sees tag == active ==
	// post.ID and forwards instead of dropping. This is the milestone that was
	// impossible to write before that ticket and is its regression proof: revert the
	// tag rotation and this loop times out while every other milestone stays green.
	//
	// The needle is turn #2's own text, not merely "some delta arrived". Turn #1's
	// delta already reached this phone at M1 and its envelope may still be ahead of
	// this loop in the queue, so a needle-free assertion would be discharged by the
	// PRE-rotation turn and pass against the very defect it exists to catch.
	//
	// Ordered after M4 deliberately: M4's poll exits on the fresh child's stdin
	// WRITE, which precedes its echo, so this loop starts inside the window where
	// the child has the turn and has not yet answered — the deadline covers the echo,
	// the parse, the fan-in and the emitter's ~250ms coalescing timer.
	sawDeltaTwo := false
	m6Deadline := time.Now().Add(20 * time.Second)
	for !sawDeltaTwo {
		env, ok := nextEnv(m6Deadline)
		if !ok {
			t.Fatalf("M6 (#1133): the phone never observed an assistant_delta carrying %q for the POST-ROTATION turn "+
				"within 20s of M4. M4 is green, so the fresh child (--session-id %s) provably received that turn — "+
				"which rules out delivery and routing and leaves the drain: the runner's turn-event sink tag did not "+
				"rotate with the runner, so every event it produces is still tagged with the pre-rotation id %q while "+
				"the conversation is bound to %q, and the active-session gate drops all of them. That is the exact "+
				"pre-#1133 divergence this file's header used to document; check that newStreamRunnerFactory still "+
				"binds both lanes through streamSessionTag and that RestartFresh still fires "+
				"streamsup.Config.OnSessionRotate.\n%s",
				echoNeedleTwo, post.ID, initialUUID, post.ID,
				daemonLogWindow(h.Stderr.Bytes(), ackTwoLogLen, daemonLogBudget))
		}
		if env.Type != protocol.TypeAssistantDelta {
			continue
		}
		var d protocol.AssistantDeltaPayload
		if err := json.Unmarshal(env.Payload, &d); err != nil {
			t.Fatalf("M6: phone A decode assistant_delta payload: %v", err)
		}
		if !strings.Contains(d.Text, echoNeedleTwo) {
			// Turn #1's delta, or a coalesced fragment of turn #2 that does not yet
			// carry the needle. Neither is a failure; keep draining.
			continue
		}
		if d.ConversationID != knownConvID {
			t.Errorf("M6: post-rotation assistant_delta ConversationID: got %q, want %q — the event was forwarded "+
				"but stamped for the wrong conversation", d.ConversationID, knownConvID)
		}
		sawDeltaTwo = true
	}
	t.Logf("[t=%s] M6: the post-rotation turn's assistant_delta reached the phone — the sink tag rotated with the runner and the drain gate forwarded instead of dropping", elapsed())

	// --- AC-1 instrument guard (#1318). Prove the daemon really is logging at Debug,
	// so M4's pending/holds count above is a MEASUREMENT and not a suppression.
	// StartStreamInteractiveWithRelay passes -pyry-verbose, which is the only thing
	// that makes msgqueue's pending/hold line (Debug, deliberately) renderable in
	// that window at all; if the flip silently stops working the count degrades to a
	// constant 0 and the record is back to being unable to separate the two readings
	// — the exact ambiguity the instrument exists to close.
	//
	// NOT a new milestone: it asserts nothing about new_session, only about the
	// instrument M4 hands the next recurrence.
	//
	// WHY A GUARD THAT ONLY RUNS ON GREEN IS THE RIGHT SHAPE. The instrument matters
	// on a RED M4, and this never runs there — the t.Fatalf above exits first. That
	// is the point, not an oversight: it is a REGRESSION guard, not a diagnostic.
	// Every green run re-proves the flip, so the rare red run's record can be trusted
	// when it finally arrives.
	//
	// WHY A DEBUG RECORD IS DETERMINISTIC HERE, AND WHY THAT ARGUMENT MOVED (#1133).
	// It used to rest on the post-rotation drop: the sink tag stayed frozen at
	// initialUUID while the conversation rebound to post.ID, so every turn-#2 event was
	// dropped at the drain's active-session gate with a Debug "relay: stream-turn drop;
	// not active session" and a green M4 owed at least one such record. #1133 rotates
	// the tag, those events are now FORWARDED — that is M6 — and that record is no
	// longer owed. The guard is unchanged and still sound on a strictly EARLIER anchor,
	// upstream of everything it is placed after: mapStreamsupConfig sets
	// RequestInitializeOnSpawn on every stream runner, so each spawn asks its child for
	// an initialize report and the reply logs a Debug "streamsup: consuming solicited
	// control_response". That is per SPAWN, so the bootstrap child alone discharges it
	// before the first send_message is sent — measured 2026-09-06 on this test: the
	// capture's first level=DEBUG record is that line, present by M1.
	//
	// The assertion stays the deliberately WEAK "some Debug record exists anywhere in
	// the capture", and after this ticket that weakness is load-bearing rather than
	// merely convenient: the level flip is the thing under test, the anchor above is
	// one of several Debug producers rather than the only one, and pinning a specific
	// message would couple this guard to a wording that has now changed once.
	//
	// The poll is bounded rather than one-shot because the capture side is
	// asynchronous — os/exec's stderr copier runs on its own goroutine, so a record
	// written before this point can land just after it (the same approximation
	// ackTwoLogLen's boundary documents). 5s at 50ms sits well inside the test's
	// existing budget.
	sawDebug := false
	debugDeadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(debugDeadline) {
		if bytes.Contains(h.Stderr.Bytes(), []byte(daemonDebugLevel)) {
			sawDebug = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !sawDebug {
		t.Fatalf("AC-1: the daemon's captured stderr carries no %q record within 5s of M4, so this harness is "+
			"NOT logging at Debug and M4's pending/holds count is a suppression rather than a measurement. "+
			"ONE reading remains, and #1133 is why it is only one: the handler is not at Debug — -pyry-verbose no "+
			"longer reaches it (internal/e2e/harness.go's extraFlags, or cmd/pyry/main.go's flag → slog.LevelDebug) "+
			"or something later raised the level. The two readings this message used to offer were both about the "+
			"post-rotation drop that USED to be this guard's anchor, and neither survives: that drop is gone by "+
			"design (M6 asserts the events are forwarded now), and the anchor moved to the daemon's startup Debug "+
			"records, which are emitted long before the rotation and are independent of it. What is NOT a reading, "+
			"and must not be read in: anything about turn #2's delivery or echo. M4 and M6 are both green above — "+
			"the fresh child received the turn AND its delta reached the phone — so no question about that turn is "+
			"open by the time this guard runs\nstderr=%q", daemonDebugLevel, h.Stderr.Bytes())
	}
	t.Logf("[t=%s] AC-1: the daemon's capture carries a %q record — -pyry-verbose reached the handler, so M4's pending/holds count is a measurement", elapsed(), daemonDebugLevel)

	// --- M5 (AC-3): no "/clear" on the stream path. On the stream path new_session is
	// a process re-spawn (RestartFresh), never a keystroke, so NO child ever receives
	// "/clear". Read EVERY per-child log — not just the post-rotation child's — and
	// assert (a) some child captured a user turn, the non-vacuity guard proving the tee
	// is actually wired, and (b) no child's bytes contain "/clear".
	//
	// Spanning every child is load-bearing in BOTH directions (#1331), which is why the
	// per-child split narrows M4 without narrowing M5. Non-vacuity is discharged by turn
	// #1's needle, and turn #1 went to the OUTGOING child — reading only the
	// post-rotation child's log would make the guard depend on turn #2, the very thing
	// M4 may have just found missing. And a "/clear" typed at the outgoing child is
	// exactly the mis-route M5 exists to catch, so a post-rotation-only scan would let
	// the regression through.
	logs, logsErr := childStdinLogs(stdinLogStem)
	if logsErr != nil {
		// A broken instrument must not read as a pass: the scans below span only what
		// was read, so an unread log is a hole in both of them.
		t.Errorf("M5: could not read every per-child stdin log under %s.*: %v", stdinLogStem, logsErr)
	}
	if len(childrenWith(logs, echoNeedleOne)) == 0 && len(childrenWith(logs, `"type":"user"`)) == 0 {
		t.Fatalf("M5 non-vacuity: no per-child stdin log captured a user turn (neither %q nor `\"type\":\"user\"` "+
			"in any of them); the tee is not wired, PYRY_FAKE_CLAUDE_STDIN_LOG never reached the children, or this "+
			"test and fakeclaude disagree on the per-child path shape — so the no-/clear assertion below would "+
			"pass vacuously\n%s", echoNeedleOne, childStdinInventory(logs, logsErr, initialUUID, post.ID))
	}
	if clearIDs := childrenWith(logs, "/clear"); len(clearIDs) > 0 {
		t.Fatalf("M5 (AC-3): the stdin log of child(ren) %q contains \"/clear\"; the stream new_session mis-routed "+
			"to the PTY /clear path (a startFreshRunner type-switch regression) — the stream path must re-spawn, "+
			"never type /clear\n%s\nlogs=%q", clearIDs,
			childStdinInventory(logs, logsErr, initialUUID, post.ID), logs)
	}
	t.Logf("[t=%s] M5: %d per-child stdin log(s), at least one carrying a user turn and NONE of them a /clear — the stream new_session re-spawned, no keystroke (AC-3)", elapsed(), len(logs))
}

// daemonLogBudget bounds the daemon-log excerpt M4's failure message attaches.
//
// The sizing arithmetic this doc used to carry — ~20 retry Warns (one per msgqueue
// defaultRetryInterval across the 20 s window) at ~250 B each ≈ 5 KB, so the
// modelled failure renders COMPLETE — is RETIRED rather than merely stale. It was
// computed against an INFO-level window, and the harness now starts pyry with
// -pyry-verbose (#1318), so the window is Debug-level and its size is no longer
// modelled at all. Completeness is not claimed; leaving the old numbers standing
// would let a future reader take a retired model for a current one.
//
// The honest fallback is the structure already in place: the head+tail elision arm
// keeps both ends, and the header reports the window's TOTAL size rather than the
// kept size, so an over-budget window announces itself instead of truncating
// silently.
//
// A tuning knob, not a contract — and now the operative sentence. The number stays
// where it is deliberately: no over-budget window has been observed, and sizing a
// bound against an imagined chatty run defends a failure that has not happened. If
// a real recurrence renders elided, the header's counts still answer the question
// the excerpt only corroborates, so the constant can move without touching the shape.
const daemonLogBudget = 8 << 10

// msgqueueRetryWarn is the drain's delivery-failure retry line, verbatim from
// internal/msgqueue/queue.go (the q.log.Warn at the end of the drain loop — Warn,
// unlike the pending/hold branch just above it, which logs at Debug). Duplicated
// here rather than exported from msgqueue on purpose: exporting a log message
// turns operator-facing prose into an API. The rendering prints the literal it
// counted, so a drift surfaces as "retry Warns: 0" beside an excerpt that visibly
// contains the lines — a discrepancy the reader sees in one look, which is the
// cheaper guard.
const msgqueueRetryWarn = "msgqueue: delivery failed, will retry"

// msgqueueHoldDebug is the drain's legitimate-HOLD retry line, verbatim from
// internal/msgqueue/queue.go (the q.log.Debug in the pending branch, immediately
// above the Warn msgqueueRetryWarn quotes). Debug rather than Warn is deliberate
// there — "a long legitimate wait must not spam the operator log" — which is
// precisely why the fix for #1318 raises the HARNESS's level (-pyry-verbose, in
// StartStreamInteractiveWithRelay) and leaves that production call site alone.
//
// Duplicated rather than exported from msgqueue for the reason msgqueueRetryWarn's
// doc gives just above, and guarded the same way: a count of 0 printed beside an
// excerpt that visibly contains the lines is a one-look discrepancy.
//
// The name encodes the LEVEL, as msgqueueRetryWarn's does, because the level is
// exactly what makes the two counts' readings differ — this one is a measurement
// only while the daemon logs at Debug, and reads 0 under a suppression otherwise.
const msgqueueHoldDebug = "msgqueue: delivery held (awaiting external decision), will retry"

// daemonDebugLevel is slog's TextHandler rendering of a Debug record's level field
// ("time=… level=DEBUG msg=…"), verified against a live handler rather than
// assumed. The harness starts pyry with -pyry-verbose, so its absence from the
// WHOLE capture — not merely from M4's window — means the level flip is broken and
// the pending/hold count has silently degraded to a suppression.
const daemonDebugLevel = "level=DEBUG"

// daemonLogWindow renders the daemon-log bytes written after the byte offset
// `since` — the window between M4's ack #2 and its poll expiry (#1296). Pure
// over its arguments: no *testing.T, no I/O, no clock, so it cannot fire a fatal
// while t.Fatalf's arguments are being built (the rule the M4 branch states, and
// the reason readBootstrapIfPresent is the only registry reader admissible there).
//
// It NEVER returns "". A failure to measure and a measured absence render as
// distinct sentinels, because an empty rendering would read as the FINDING "the
// daemon logged nothing during the window" when it may be the absence of data —
// the same posture as the regNowID sentinel above: the instrument reports itself.
func daemonLogWindow(snapshot []byte, since, budget int) string {
	if since < 0 || since > len(snapshot) {
		// Unreachable while safeBuffer stays append-only; it exists so that
		// assumption is checkable rather than implicit, and so a broken boundary
		// can never render as a measurement.
		return fmt.Sprintf("<unmeasurable: the ack #2 offset is %d into a %d-byte daemon-log snapshot; "+
			"the window boundary cannot be applied>", since, len(snapshot))
	}
	window := snapshot[since:]
	if len(window) == 0 {
		return "<empty: the daemon logged nothing between ack #2 and expiry>"
	}
	// The counts are the primary datum and the excerpt is corroboration: as a PAIR
	// they answer "which of msgqueue's drain arms was this, if either?" as numbers,
	// and numbers survive elision where a hand-count of the excerpt does not. BOTH
	// are scoped to the window, never to the whole buffer — pre-ack-#2 lines are not
	// evidence about the dead window.
	//
	// The pending/hold count is a measurement only because the harness raises the
	// daemon to Debug (#1318); at Info that arm's line is suppressed, and the count
	// would read 0 whether or not the drain was looping in it. The AC-1 guard after
	// M4 keeps that precondition honest on every green run.
	header := fmt.Sprintf("daemon log, ack #2 → expiry: %d bytes, retry Warns: %d (matching %q), pending/holds: %d (matching %q)",
		len(window),
		bytes.Count(window, []byte(msgqueueRetryWarn)), msgqueueRetryWarn,
		bytes.Count(window, []byte(msgqueueHoldDebug)), msgqueueHoldDebug)
	if len(window) <= budget {
		// Says nothing about eliding: a short window that LOOKS truncated is the
		// symmetric half of the bound's honesty obligation.
		return header + "\n" + string(window)
	}
	// Head AND tail. The realistic over-budget case is an unexpectedly chatty run,
	// where onset (did the retries begin at all?) and end (what was the daemon's
	// last word) are both load-bearing, and head-only elision buries one of them.
	// The cut is by byte, not newline-aligned — the header says "bytes" and means
	// it. The header states the window's TOTAL size, not the kept size: a bound
	// that reports only what survived it is exactly the silent truncation this
	// record must not do.
	keep := budget / 2
	if keep < 0 {
		keep = 0
	}
	elided := len(window) - 2*keep
	return fmt.Sprintf("%s\n  (bounded at %d bytes: showing the first %d and the last %d, %d elided from "+
		"the middle)\n%s… [%d bytes elided from the middle] …%s",
		header, budget, keep, keep, elided, window[:keep], elided, window[len(window)-keep:])
}

// --- Per-child stdin evidence (#1331) ---------------------------------------
//
// fakeclaude's stream tee treats PYRY_FAKE_CLAUDE_STDIN_LOG as a path STEM and writes
// each child's stdin to <stem>.<that child's session id>, so the evidence M4 and M5
// read is attributed to the child that received it. Every helper below is FATAL-FREE
// — no *testing.T, no t.Fatal* — because M4's failure record calls them while building
// its message, and a fatal fired there means the message never prints (the rule the M4
// branch states, and the reason readBootstrapIfPresent is its only registry reader).

// childStdinLog is the path the child pinned to sessionID tees its stdin to.
// Deliberately DUPLICATES fakeclaude's streamStdinLogPath derivation rather than
// sharing it: fakeclaude is package main and unimportable — the same posture
// msgqueueRetryWarn takes toward msgqueue's log literal. A drift between the two fails
// LOUD rather than silently green: the needle can only ever appear in a file some child
// actually wrote, so a reader looking at the wrong path finds nothing and M4 goes red.
func childStdinLog(stem, sessionID string) string {
	return stem + "." + sessionID
}

// childReceivedTurn reports whether the child pinned to sessionID received needle,
// reading ONLY that child's log — which is what makes "a turn delivered to the outgoing
// child" fail M4 instead of passing it. The bytes and the read error both come back so
// a caller can render an absent or unreadable file as itself: without the error,
// len(nil) == 0 renders "the file does not exist" as "the file was empty", a broken
// instrument reporting itself as a measurement.
func childReceivedTurn(stem, sessionID string, needle []byte) (bool, []byte, error) {
	b, err := os.ReadFile(childStdinLog(stem, sessionID))
	return bytes.Contains(b, needle), b, err
}

// childStdinLogs returns every per-child stdin log under stem, keyed by the session id
// its name carries. os.ReadDir plus a filepath.Base(stem)+"." prefix filter, NOT
// filepath.Glob: a stem containing a glob metacharacter would make Glob return
// ErrBadPattern, and that renders as "no children" — an instrument failure reported as
// a measurement. The trailing dot in the prefix is load-bearing too: it skips the bare
// <stem> file, which only the PTY tee ever writes.
//
// A per-file read error is errors.Join'd into err while every other file still lands in
// the map, so partial evidence survives with the gap stated rather than being discarded
// wholesale.
func childStdinLogs(stem string) (map[string][]byte, error) {
	dir, prefix := filepath.Dir(stem), filepath.Base(stem)+"."
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read per-child stdin log dir %s: %w", dir, err)
	}
	logs := make(map[string][]byte)
	var readErrs []error
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			readErrs = append(readErrs, err)
			continue
		}
		logs[strings.TrimPrefix(e.Name(), prefix)] = b
	}
	return logs, errors.Join(readErrs...)
}

// childrenWith returns the session ids whose stdin log contains needle, sorted so a
// failure message is stable. It serves BOTH of M5's checks — the "/clear" scan, which
// must NAME the child that received one, and the non-vacuity guard, which holds when
// some child's bytes carry a user turn — so both span every child by construction. An
// empty inventory therefore yields an empty list, i.e. FAILS non-vacuity rather than
// passing it vacuously.
func childrenWith(logs map[string][]byte, needle string) []string {
	var ids []string
	for id, b := range logs {
		if bytes.Contains(b, []byte(needle)) {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids
}

// childStdinInventory renders every per-child log's size beside the id that wrote it,
// marking the two ids the assertion is about. It is the attribution half of M4's failure
// record, and its decisive reading is the wrong-child one: bytes under the OUTGOING id
// and none under the post-rotation id ⟹ the turn was delivered to the child the rotation
// replaced. An id that is neither is named as such — a second rotation (M2 re-sends
// new_session every ~250 ms, and this file already anticipates double actuation) or an
// unattributed child.
//
// Pure over its arguments, and it NEVER returns "": a failure to enumerate and an
// enumerated absence render as distinct sentinels — the same posture daemonLogWindow
// takes — because a blank rendering would read as the finding "no child ever wrote".
func childStdinInventory(logs map[string][]byte, err error, outgoingID, postID string) string {
	if len(logs) == 0 {
		if err != nil {
			return fmt.Sprintf("per-child stdin logs: <unenumerable: %v>", err)
		}
		return "per-child stdin logs: <none: not one child wrote a single stdin byte, so the tee never wired " +
			"(or this test and fakeclaude disagree on the per-child path shape)>"
	}
	ids := make([]string, 0, len(logs))
	for id := range logs {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	var b strings.Builder
	fmt.Fprintf(&b, "per-child stdin logs (%d):", len(ids))
	for _, id := range ids {
		mark := "neither the outgoing nor the post-rotation id — a second rotation, or an unattributed child"
		switch id {
		case postID:
			mark = "POST-ROTATION (the child M4 asserts)"
		case outgoingID:
			mark = "outgoing (the child the rotation replaced)"
		}
		fmt.Fprintf(&b, "\n  %s = %d bytes ← %s", id, len(logs[id]), mark)
	}
	if err != nil {
		fmt.Fprintf(&b, "\n  (incomplete — some logs were unreadable: %v)", err)
	}
	return b.String()
}

// TestDaemonLogWindow pins daemonLogWindow's four arms and BOTH of its header
// counts. The renderer is pure over (snapshot, since, budget), so every arm is
// provable offline here — including the two readings with no live repro in the M4
// fixture: the genuinely-empty window (plausible but not producible on demand), and
// a non-zero pending/hold count, whose arm is structurally unreachable on that
// harness (#1318). Both are CONTRACT checks, not manufactured failing scenarios.
//
// Assertions are substrings and computed numbers rather than a whole-output golden:
// a golden would turn every wording tweak in a diagnostic into a test edit.
func TestDaemonLogWindow(t *testing.T) {
	const (
		before   = "pyrycode starting\n"                   // pre-window: must never be rendered
		midNeed  = "MIDDLE-NEEDLE"                         // must not survive elision
		headNeed = "HEAD-NEEDLE-0123456789012345678901234" // 37 B, the kept head
		tailNeed = "TAIL-NEEDLE-9876543210987654321098765" // 37 B, inside the kept tail
	)
	chatty := headNeed + strings.Repeat("m", 40) + midNeed + strings.Repeat("m", 40) + tailNeed

	tests := []struct {
		name     string
		snapshot string
		since    int
		budget   int
		want     []string
		notWant  []string
	}{
		{
			name:     "empty window renders the sentinel, not a blank",
			snapshot: before + msgqueueRetryWarn + "\n",
			since:    len(before + msgqueueRetryWarn + "\n"),
			budget:   daemonLogBudget,
			want:     []string{"<empty:", "logged nothing"},
			// A blank rendering would read as the finding, and pre-window bytes must
			// not leak in past the boundary. NEITHER count may appear on a sentinel
			// arm: a header is a claim to have measured the window, and these arms
			// did not.
			notWant: []string{"pyrycode starting", "retry Warns", "pending/holds"},
		},
		{
			name:     "offset past the end is unmeasurable, naming both numbers",
			snapshot: "abc",
			since:    4,
			budget:   daemonLogBudget,
			want:     []string{"<unmeasurable:", "offset is 4", "3-byte"},
			notWant:  []string{"retry Warns", "pending/holds"},
		},
		{
			name:     "negative offset takes the same arm",
			snapshot: "abc",
			since:    -1,
			budget:   daemonLogBudget,
			want:     []string{"<unmeasurable:", "offset is -1"},
			notWant:  []string{"retry Warns", "pending/holds"},
		},
		{
			name:     "window under budget renders verbatim and says nothing about eliding",
			snapshot: before + "spawning claude\n" + msgqueueRetryWarn + " err=boom\n",
			since:    len(before),
			budget:   daemonLogBudget,
			want: []string{
				"spawning claude\n" + msgqueueRetryWarn + " err=boom\n",
				fmt.Sprintf("%d bytes", len("spawning claude\n"+msgqueueRetryWarn+" err=boom\n")),
				"retry Warns: 1",
				// The ZERO half of the pending/hold count's non-vacuity (#1318): this
				// snapshot carries a retry Warn and no hold line, so one input pins
				// both counts, in opposite directions.
				"pending/holds: 0",
			},
			notWant: []string{"elided", "bounded at", "pyrycode starting"},
		},
		{
			name:     "over-budget window keeps both ends and states the exact elision",
			snapshot: chatty,
			since:    0,
			budget:   74, // keep = 37 each end
			want: []string{
				headNeed,
				tailNeed,
				fmt.Sprintf("%d bytes,", len(chatty)),    // the TOTAL, not the kept count
				fmt.Sprintf("%d elided", len(chatty)-74), // header restatement
				fmt.Sprintf("[%d bytes elided", len(chatty)-74), // the in-excerpt marker
				"showing the first 37 and the last 37",
			},
			notWant: []string{midNeed},
		},
		{
			name:     "the Warn count is scoped to the window, not the whole buffer",
			snapshot: msgqueueRetryWarn + "\n" + msgqueueRetryWarn + "\n" + msgqueueRetryWarn + "\n",
			since:    len(msgqueueRetryWarn + "\n"), // one copy sits BEFORE the boundary
			budget:   daemonLogBudget,
			want:     []string{"retry Warns: 2"},
		},
		{
			name:     "a non-empty window with no retries reads zero, not empty",
			snapshot: "spawning claude\nclaude exited\n",
			since:    0,
			budget:   daemonLogBudget,
			want:     []string{"retry Warns: 0", "pending/holds: 0", "spawning claude", "claude exited"},
			notWant:  []string{"<empty:", "<unmeasurable:"},
		},
		{
			// The NON-ZERO half of the pending/hold count's non-vacuity (#1318). No
			// live repro is needed or possible: the hold arm is structurally
			// unreachable on the M4 harness (cmd/pyry wires ErrTrustModalPending as
			// its only Pending classifier and the stream runner never produces it),
			// so this is the CONTRACT check that the count would report the arm if
			// the delivery path ever did reach it — the same posture the empty-window
			// arm above takes.
			name:     "a window carrying the hold marker reports a non-zero count",
			snapshot: msgqueueHoldDebug + " conversation_id=c1\n" + msgqueueHoldDebug + " conversation_id=c1\n",
			since:    0,
			budget:   daemonLogBudget,
			want:     []string{"pending/holds: 2", "retry Warns: 0"},
			notWant:  []string{"<empty:", "<unmeasurable:"},
		},
		{
			// A separate property from the count itself, so deliberately not folded
			// into the case above: pre-ack-#2 lines are not evidence about the dead
			// window. Mirrors the retry-Warn scoping case.
			name:     "the pending/hold count is scoped to the window, not the whole buffer",
			snapshot: msgqueueHoldDebug + "\n" + msgqueueHoldDebug + "\n" + msgqueueHoldDebug + "\n",
			since:    len(msgqueueHoldDebug + "\n"), // one copy sits BEFORE the boundary
			budget:   daemonLogBudget,
			want:     []string{"pending/holds: 2"},
		},
		{
			// The two counts are independent, not one matching the other's substring:
			// both literals end "…, will retry", so a sloppy marker would make the
			// pair move together and the header would stop discriminating the arms.
			name:     "a window with both markers reports both counts",
			snapshot: msgqueueRetryWarn + " err=boom\n" + msgqueueHoldDebug + " queued_msg_id=u-2\n",
			since:    0,
			budget:   daemonLogBudget,
			want:     []string{"retry Warns: 1", "pending/holds: 1"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := daemonLogWindow([]byte(tc.snapshot), tc.since, tc.budget)
			if got == "" {
				t.Fatal("daemonLogWindow returned \"\": a failure to measure must never render as an " +
					"absence of output — every arm owes a sentinel or a header")
			}
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("rendering does not contain %q\ngot:\n%s", w, got)
				}
			}
			for _, w := range tc.notWant {
				if strings.Contains(got, w) {
					t.Errorf("rendering unexpectedly contains %q\ngot:\n%s", w, got)
				}
			}
		})
	}
}

// TestChildStdinLogAttribution is #1331's falsifiability proof for M4's attributed
// assertion and for M5's spanning ones. Every fixture path is written with a
// HAND-WRITTEN literal shape — filepath.Join(dir, "fakeclaude-stdin."+id) — and never
// by calling childStdinLog: a fixture built through the helper would agree with
// whatever derivation the helper happened to have and would prove nothing.
//
// WHAT THIS PROVES. The attribution property: evidence in which only the OUTGOING child
// received the turn cannot satisfy the post-rotation child's assertion. The first M4 row
// below is exactly that evidence — green under the retired shared-file instrument, red
// under this one.
//
// WHAT IT DOES NOT PROVE. That fakeclaude and this file agree on the path shape. Nothing
// offline can: fakeclaude is a separate `package main` binary, unimportable from here,
// which is why childStdinLog duplicates its derivation. That agreement is proven live, by
// a green M4 together with M5's non-vacuity, and a disagreement is loud (M4 red), never
// silently green. Do not read these rows as covering it.
//
// WHY A FIXTURE AND NOT A LIVE RE-RUN. The production race that delivered a turn to the
// wrong child is CLOSED (#1330, dac5779), so the wrong-child evidence is no longer
// producible on demand even in principle. A fixture is the only available proof, not a
// convenience.
func TestChildStdinLogAttribution(t *testing.T) {
	const (
		outgoingID = "11111111-1111-4111-8111-111111111111" // the bootstrap child
		postID     = "33333333-3333-4333-8333-333333333333" // the post-rotation child
		thirdID    = "44444444-4444-4444-8444-444444444444" // a second rotation's child
		turnTwo    = "e2e-1137-user:two"                    // M4's needle
		turnOne    = "e2e-1137-user:one"                    // M5's non-vacuity needle
	)

	// fixture lays the named per-child files down in a fresh dir and returns the stem.
	// The path shape is written out literally here, on purpose (see the doc comment).
	fixture := func(t *testing.T, files map[string]string, extra map[string]string) string {
		t.Helper()
		dir := t.TempDir()
		for id, body := range files {
			if err := os.WriteFile(filepath.Join(dir, "fakeclaude-stdin."+id), []byte(body), 0o600); err != nil {
				t.Fatalf("write per-child fixture for %s: %v", id, err)
			}
		}
		for name, body := range extra {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
				t.Fatalf("write extra fixture %s: %v", name, err)
			}
		}
		return filepath.Join(dir, "fakeclaude-stdin")
	}

	t.Run("M4 predicate reads only the post-rotation child", func(t *testing.T) {
		tests := []struct {
			name    string
			files   map[string]string
			want    bool
			wantErr bool
		}{
			{
				// THE AC-3 ROW. The outgoing child got turn #2 and the post-rotation
				// child got nothing — the shape the #1330 race produced. The retired
				// shared-file read found the needle and went green here; the attributed
				// read must not. The non-nil error is half the point: an absent file
				// must render as absent, never as an empty one.
				name:    "only the OUTGOING child received the turn: M4 must go red",
				files:   map[string]string{outgoingID: turnOne + "\n" + turnTwo + "\n"},
				want:    false,
				wantErr: true,
			},
			{
				// The control (#1295/#1330): proves the fixture shape is reachable at
				// all, so the row above is red because of attribution and not because
				// the predicate can never see a needle.
				name: "the post-rotation child received it: M4 goes green",
				files: map[string]string{
					outgoingID: turnOne + "\n",
					postID:     turnTwo + "\n",
				},
				want: true,
			},
			{
				name: "a second delivery to the outgoing child does not un-prove it",
				files: map[string]string{
					outgoingID: turnOne + "\n" + turnTwo + "\n",
					postID:     turnTwo + "\n",
				},
				want: true,
			},
			{
				// An empty file and an absent one are different readings: this child
				// opened its log (so it started) and received nothing.
				name:    "the post-rotation child's log exists but is empty: red, and NO error",
				files:   map[string]string{outgoingID: turnTwo + "\n", postID: ""},
				want:    false,
				wantErr: false,
			},
			{
				name:    "no per-child log at all: red, with the error kept",
				files:   nil,
				want:    false,
				wantErr: true,
			},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				stem := fixture(t, tc.files, nil)
				got, b, err := childReceivedTurn(stem, postID, []byte(turnTwo))
				if got != tc.want {
					t.Errorf("childReceivedTurn(post=%s, %q) = %v, want %v (bytes read: %q)",
						postID, turnTwo, got, tc.want, b)
				}
				if (err != nil) != tc.wantErr {
					t.Errorf("childReceivedTurn read error = %v, want error present: %v", err, tc.wantErr)
				}
			})
		}
	})

	t.Run("M5 predicates span every child", func(t *testing.T) {
		tests := []struct {
			name string
			// files/extra build the fixture; extra names are literal, unprefixed.
			files      map[string]string
			extra      map[string]string
			wantIDs    []string // childStdinLogs' keys
			wantClear  []string // childrenWith(logs, "/clear")
			wantVacuum bool     // no child carried a user turn ⟹ non-vacuity FAILS
		}{
			{
				// AC-5's explicit case: the mis-route is caught even when it lands on
				// the child M4 no longer reads.
				name: "/clear at the OUTGOING child is reported, naming it",
				files: map[string]string{
					outgoingID: turnOne + "\n/clear\n",
					postID:     turnTwo + "\n",
				},
				wantIDs:   []string{outgoingID, postID},
				wantClear: []string{outgoingID},
			},
			{
				name: "/clear at the post-rotation child is reported, naming it",
				files: map[string]string{
					outgoingID: turnOne + "\n",
					postID:     turnTwo + "\n/clear\n",
				},
				wantIDs:   []string{outgoingID, postID},
				wantClear: []string{postID},
			},
			{
				name: "no /clear anywhere: nothing reported",
				files: map[string]string{
					outgoingID: turnOne + "\n",
					postID:     turnTwo + "\n",
				},
				wantIDs: []string{outgoingID, postID},
			},
			{
				// Non-vacuity is discharged by turn #1's bytes, which are the OUTGOING
				// child's — the reason M5 must keep spanning every child even though
				// M4 narrowed to one.
				name:    "only the outgoing child carries a user turn: non-vacuity still holds",
				files:   map[string]string{outgoingID: turnOne + "\n"},
				wantIDs: []string{outgoingID},
			},
			{
				name:       "no per-child logs at all: non-vacuity FAILS",
				files:      nil,
				wantVacuum: true,
			},
			{
				// A third id is collected too, so the inventory can name a second
				// rotation instead of hiding it.
				name: "a third child is collected, not filtered out",
				files: map[string]string{
					outgoingID: turnOne + "\n",
					postID:     turnTwo + "\n",
					thirdID:    `{"type":"user"}` + "\n",
				},
				wantIDs: []string{outgoingID, postID, thirdID},
			},
			{
				// The prefix filter: the bare stem (only the PTY tee writes it) and an
				// unrelated neighbour are not per-child evidence.
				name:    "files without the <stem>. prefix are not collected",
				files:   map[string]string{outgoingID: turnOne + "\n"},
				extra:   map[string]string{"fakeclaude-stdin": "/clear\n", "unrelated.log": "/clear\n"},
				wantIDs: []string{outgoingID},
			},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				stem := fixture(t, tc.files, tc.extra)
				logs, err := childStdinLogs(stem)
				if err != nil {
					t.Fatalf("childStdinLogs: %v", err)
				}
				gotIDs := make([]string, 0, len(logs))
				for id := range logs {
					gotIDs = append(gotIDs, id)
				}
				slices.Sort(gotIDs)
				wantIDs := slices.Sorted(slices.Values(tc.wantIDs))
				if !slices.Equal(gotIDs, wantIDs) {
					t.Errorf("childStdinLogs ids = %q, want %q", gotIDs, wantIDs)
				}
				if got := childrenWith(logs, "/clear"); !slices.Equal(got, tc.wantClear) {
					t.Errorf("childrenWith(/clear) = %q, want %q", got, tc.wantClear)
				}
				// The milestone's own non-vacuity expression, verbatim.
				vacuous := len(childrenWith(logs, turnOne)) == 0 && len(childrenWith(logs, `"type":"user"`)) == 0
				if vacuous != tc.wantVacuum {
					t.Errorf("non-vacuity guard vacuous = %v, want %v", vacuous, tc.wantVacuum)
				}
			})
		}
	})

	t.Run("the inventory never renders as a blank", func(t *testing.T) {
		logs := map[string][]byte{
			outgoingID: []byte(turnOne + "\n"),
			postID:     nil,
			thirdID:    []byte(turnTwo),
		}
		tests := []struct {
			name    string
			logs    map[string][]byte
			err     error
			want    []string
			notWant []string
		}{
			{
				name: "an enumerated inventory marks both ids and names the stranger",
				logs: logs,
				want: []string{
					// Computed, not transcribed: a byte count spelled by hand is a
					// second source of truth that drifts on the next fixture edit.
					fmt.Sprintf("%s = %d bytes ← outgoing", outgoingID, len(turnOne+"\n")),
					postID + " = 0 bytes ← POST-ROTATION",
					fmt.Sprintf("%s = %d bytes ← neither", thirdID, len(turnTwo)),
					"per-child stdin logs (3):",
				},
				notWant: []string{"<none:", "<unenumerable:"},
			},
			{
				// An enumerated absence: the tee never wired. Distinct from the arm
				// below, and it must not claim to have failed to look.
				name:    "an empty inventory is a sentinel, not a blank",
				logs:    nil,
				want:    []string{"<none:", "the tee never wired"},
				notWant: []string{"<unenumerable:", "bytes ←"},
			},
			{
				// A failure to look. It must not read as "no child wrote".
				name:    "an unenumerable inventory names the error, not an absence",
				logs:    nil,
				err:     errors.New("readdir: boom"),
				want:    []string{"<unenumerable:", "readdir: boom"},
				notWant: []string{"<none:"},
			},
			{
				name: "a partial read states the gap beside the counts it did get",
				logs: map[string][]byte{outgoingID: []byte(turnOne)},
				err:  errors.New("open …post: permission denied"),
				want: []string{
					fmt.Sprintf("%s = %d bytes ← outgoing", outgoingID, len(turnOne)),
					"incomplete", "permission denied",
				},
				notWant: []string{"<none:", "<unenumerable:"},
			},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				got := childStdinInventory(tc.logs, tc.err, outgoingID, postID)
				if got == "" {
					t.Fatal("childStdinInventory returned \"\": a failure to enumerate must never render as an " +
						"absence of output — every arm owes a sentinel or an inventory")
				}
				for _, w := range tc.want {
					if !strings.Contains(got, w) {
						t.Errorf("rendering does not contain %q\ngot:\n%s", w, got)
					}
				}
				for _, w := range tc.notWant {
					if strings.Contains(got, w) {
						t.Errorf("rendering unexpectedly contains %q\ngot:\n%s", w, got)
					}
				}
			})
		}
	})
}

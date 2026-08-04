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

	// Poll the stdin log until the fresh child has received turn #2. The daemon's
	// inbound queue retries WriteUserTurn until the fresh child is stdin-ready, so a
	// generous deadline absorbs the fresh spawn.
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
		// fabricate a finding out of a broken instrument. The `e.ID != ""` clause is the
		// same guard M2's read carries 170 lines up (:267): readBootstrapIfPresent does
		// not require a non-empty id, and an empty one would print as a MANUFACTURED
		// second-rotation proof — flagged in docs/knowledge/codebase/1273.md as a
		// fold-in for the next ticket touching this file, which is this one.
		regNowID := "<no bootstrap entry: registry missing, unparseable, bootstrap row absent, or its id empty>"
		if e, ok := readBootstrapIfPresent(regPath); ok && e.ID != "" {
			regNowID = e.ID
		}
		t.Fatalf("M4: the fresh child never received the subsequent turn; the stdin log has no %q bytes "+
			"(delivery never reached it, or the rotated session did not re-spawn). ack #2 received=%t\n"+
			"stdin log: %d bytes at ack #2 (t=%s, read err: %v) → %d bytes at expiry (read err: %v) "+
			"— no growth ⟹ no bytes arrived at all; growth ⟹ bytes arrived, but not turn #2's\n"+
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
			echoNeedleTwo, gotAck2,
			len(ackTwoBytes), ackTwoAt, ackTwoErr, len(logBytes), logErr,
			post.ID, regNowID,
			daemonLogWindow(h.Stderr.Bytes(), ackTwoLogLen, daemonLogBudget),
			logBytes)
	}
	t.Logf("[t=%s] M4: the fresh post-rotation child received the subsequent turn (stdin log carries %q)", elapsed(), echoNeedleTwo)

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
	// WHY A DEBUG RECORD IS DETERMINISTIC HERE. After M2's rotation the runner's sink
	// tag stays initialUUID while the conversation rebinds to post.ID, so every event
	// the fresh child produces for turn #2 is dropped at the drain's active-session
	// gate with a Debug "relay: stream-turn drop; not active session"
	// (cmd/pyry/stream_turn_drain.go) — the very divergence this file's header
	// documents at :62-72, and the reason M4 asserts stdin rather than a phone-side
	// delta. So on any run where M4 goes green, turn #2's echo owes at least one such
	// record. The assertion is deliberately the WEAKER "some Debug record exists
	// anywhere in the capture": the level flip is the thing under test, and pinning a
	// specific message would couple this guard to that message's wording.
	//
	// The poll is bounded rather than one-shot because the drop is asynchronous
	// (child stdout → parser → sink → drain goroutine) while M4's poll exits on the
	// stdin log, which the child writes to BEFORE it emits its echo. 5s at 50ms sits
	// well inside the test's existing budget.
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
			"Two readings, and they are different defects: either -pyry-verbose no longer reaches the handler "+
			"level (internal/e2e/harness.go's extraFlags, or cmd/pyry/main.go's flag → slog.LevelDebug), or "+
			"the drain stopped dropping post-rotation events at the active-session gate — which would mean "+
			"this file's header divergence analysis (:62-72) is stale and M4 could assert a phone-side delta "+
			"after all\nstderr=%q", daemonDebugLevel, h.Stderr.Bytes())
	}
	t.Logf("[t=%s] AC-1: the daemon's capture carries a %q record — -pyry-verbose reached the handler, so M4's pending/holds count is a measurement", elapsed(), daemonDebugLevel)

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

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
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// perConvMidTurnMarker is unique to this test. It is what the mid-turn kicker
// puts on the wire as assistant text, so a stray copy anywhere else would be
// this test's own line and nobody else's.
const perConvMidTurnMarker = "e2e-1191:mid-turn"

// perConvMidTurnLine is the claude-format JSONL line the kicker re-drops into the
// MINTED child's own trigger path to make the turn genuinely run: the mapper turns
// assistant + non-empty text into a TextChunk, which the emitter reports as
// turn_state{responding} (internal/turnbridge/mapper.go).
//
// It carries NO stop_reason and is NOT an interruption marker, and both absences
// are load-bearing, not formatting: they are two of the four line kinds the
// causality re-derivation on the test function below enumerates. The whole test
// rests on the bare ESC being the ONLY thing that can produce an end-of-turn line
// in the minted transcript; a stop_reason here would satisfy IsEndTurn and marker
// prose here would satisfy isInterruptMarker — either way a second source, and the
// interrupt's causality would be unprovable. fakeclaude's other write on this path,
// appendTurnGrowth, writes "{}\n" — inert to the mapper on both readings — so a
// delivered turn cannot fabricate a turn_end either.
const perConvMidTurnLine = `{"type":"assistant","message":{"id":"m-1191","content":[{"type":"text","text":"` +
	perConvMidTurnMarker + `"}]}}` + "\n"

// interruptMarkerNeedle is the fragment of the interruption marker fakeclaude's
// bare-ESC handler appends (interruptMarkerLine,
// internal/e2e/internal/fakeclaude/main.go). The fake is `package main` under an
// internal/ dir, so the const cannot be imported; this needles its shape instead.
//
// It is deliberately the EXACT string the mapper keys on
// (interruptMarkerSentinel), which is what
// keeps needle-drift and mapper-drift from diverging: if the fake's line ever
// stops carrying it, the mapper stops matching, no turn_end arrives, and the run
// dies loudly at the AC1 fatal IN THE SAME RUN. So the Phase-5 absence check
// below cannot go silently vacuous — the only condition that would empty it is
// itself a hard red. That is strictly stronger than the old
// `"stop_reason":"end_turn"` needle, which sat in JSON KEY position and would
// have missed on a whitespace change while IsEndTurn still held.
const interruptMarkerNeedle = `[Request interrupted by user`

// TestRelayV2_PerConversationInterruptStopsRunningTurn is the #1191 oracle: a
// phone-originated interrupt must stop the running turn of a MINTED
// per-conversation PTY session — a conversation created over the wire, for which
// the daemon spawned its own claude — not the bootstrap one.
//
// Every green fake-tier interrupt test on the PTY tier drives the BOOTSTRAP
// session (TestRelayV2_InterruptStopsRunningTurn, the #794 capstone). The minted
// path — the code #1121 introduced (activeInterrupter → resolveBoundRunner → the
// bound session's runner), and the path the live 2026-07-24 PTY failure ran on —
// had no fake-tier interrupt coverage at all. It could not have any before #1195:
// a minted PTY session emitted no turn lifecycle, because fakeclaude wrote
// <sharedDir>/<INITIAL_UUID>.jsonl while the daemon tailed
// <sharedDir>/<mintedID>.jsonl. This test rides #1195's two knobs.
//
// WHY MINTED IS THE POINT (the stream sibling's argument, relay_v2_stream_interrupt_test.go:47-56).
// Had the target been bootstrap-bound, a correct route and the pre-#1121 bug
// (Interrupter: w.sup — every interrupt to the bootstrap supervisor) would be
// indistinguishable. Minting forces the interrupt to reach the minted child.
//
// THREE ORACLES, each covering the one before it:
//
//   - The wire turn_end{ConversationID: convID} (Phase 4) proves the daemon
//     reported a turn ending for that conversation. It does NOT prove which
//     transcript the event came out of: ConversationID is stamped from the ACTIVE
//     CURSOR, not from the file, so alone it would also pass while the daemon
//     tailed the wrong one.
//   - The on-disk transcript pair (Phase 5) closes that. ESC_ENDS_TURN is set on
//     the DAEMON, so BOTH children carry an ESC→marker handler — which turns the
//     bootstrap transcript from a blind spot into a mis-route DETECTOR. A
//     mis-routed ESC would append the interruption marker to
//     <sharedDir>/<initialUUID>.jsonl and the negative assertion catches it. This
//     is the answer to "the stdin log is not a discriminator": the TRANSCRIPTS
//     are, because they are per-child where the shared log is not.
//   - The bare-ESC count over the shared stdin log is the AC4 oracle. Useless for
//     attribution (one file, every child appends — `StartRotationWithRelay`) but sound for
//     global ABSENCE: zero means no child received an ESC, whoever they are.
//
// STRUCTURAL CAUSALITY is what ties them together, and it is the attribution shape
// this tier requires: in this test the bare ESC is the ONLY thing that can produce
// an end-of-turn line in the minted transcript. So a turn_end for the minted
// conversation ⟺ the minted child's ESC handler fired.
//
// THAT INVARIANT IS RE-DERIVED HERE, NOT INHERITED. It used to hold for a reason
// that no longer exists: EventKindJsonlEndOfTurn was the mapper's ONLY turn_end
// source. #1243 added a second (the interruption marker), which is exactly the
// hazard S-2 of docs/specs/architecture/1191-minted-perconv-interrupt-oracle.md
// (:590-609) flagged for whoever extended this file. The set of turn_end sources is
// closed and countable, so the re-derivation is checkable rather than rhetorical.
// turnevent.TurnEnd{ is constructed at three sites in the repo; two are reachable
// from a PTY session:
//
//   - internal/turnbridge/`mapEvent` (EventKindJsonlEndOfTurn) is now UNREACHABLE
//     in this test. tuidriver.IsEndTurn requires all three of assistant,
//     stop_reason=="end_turn", non-empty text; and the minted transcript's line set
//     is closed: "{}" from the pre-created file, "{}" per turn from appendTurnGrowth,
//     the kicker's perConvMidTurnLine (assistant text, NO stop_reason — see there),
//     and the ESC handler's marker (type:"user"). None satisfies IsEndTurn.
//   - internal/turnbridge/mapper.go:95 (the interruption marker) is reachable ONLY
//     from the ESC handler's line. It needs type:"user", no top-level
//     "permissionMode" key, and text prefixed interruptMarkerNeedle. Of the four
//     line kinds above only the marker is a user entry at all — the fake never
//     writes the delivered prompt into the transcript.
//   - internal/streamsup/`benignRateLimitStatus` is the stream-json path, which a PTY session
//     never enters.
//
// So the invariant holds in the same shape by the MIRROR of its old argument: the
// design has exactly one reachable turn_end source and it is the ESC handler's own
// write. That is also why the staged marker goes down the ESC handler's own path and
// NOT the mid-turn kicker's trigger path — staging it through the kicker would give
// the minted transcript a second end-of-turn source and silently falsify all of the
// above, leaving this test passing while proving less.
//
// AC4 ("an interrupt for a conversation with no resolvable bound session stays
// inert and never actuates the bootstrap claude") is proved as a PAIR:
//
//   - Arm level, already shipped and deterministic against a real *sessions.Pool:
//     TestResolveBoundRunner/"empty CurrentSessionID is inert, never the bootstrap
//     runner" and TestActiveInterrupter/"unbound/dangling resolution is inert
//     (AC3)" (cmd/pyry/interrupt_routing_test.go). Nothing here would strengthen
//     those; they are cited, not duplicated.
//   - Wiring level, NEW here: Phase 0 sends an interrupt on a production-wired
//     daemon while nothing is active, and Phase 4 proves ZERO bytes reached any
//     child. Under the pre-#1121 wiring that count would be 1. Phase 0 exercises
//     the no_active_conv guard rather than no_bound_runner because that is the
//     unresolvable state reachable OVER THE WIRE — the cursor is stamped only on
//     sessionRouter.Route's success path (cmd/pyry/main.go:1248), so a conversation
//     cannot become active without a resolvable binding. Both guards return
//     (nil, false) into the same inert path; the property under test — inert, and
//     never the bootstrap — is identical.
//
// The Phase-0 negative is not vacuous: the same oracle demonstrably reports a real
// ESC in the same run (Phase 5 asserts exactly 1), so no settle timer is needed
// anywhere and none is used.
func TestRelayV2_PerConversationInterruptStopsRunningTurn(t *testing.T) {
	const (
		initialUUID        = "88888888-8888-4888-8888-888888888888" // bootstrap
		inertInterruptReqI = uint64(40)
		createReqID        = uint64(41)
		sendReqID          = uint64(42)
		interruptReqID     = uint64(43)
	)

	home := shortHome(t)

	// Pair one interactive device — the capability both the turn-lifecycle
	// broadcasts and handleInterrupt require.
	r := RunBareIn(t, home, "pair", "-pyry-name=test", "--name=phone-a")
	if r.ExitCode != 0 {
		t.Fatalf("pyry pair exit=%d\nstdout:\n%s\nstderr:\n%s", r.ExitCode, r.Stdout, r.Stderr)
	}
	payload := decodePairPayload(t, r.Stdout)
	pubKey, err := base64.StdEncoding.DecodeString(payload.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	// Align the sessions dir to the daemon's COMPUTED path and pre-create
	// <initialUUID>.jsonl BEFORE the daemon starts so the bootstrap session id
	// reconciles cleanly. An all-null create_conversation resolves its spawn dir to
	// "" and the pool spawns in tpl.WorkDir — the bootstrap workdir — so
	// perConversationSessionsDir maps the minted session's convDir back to this same
	// shared dir. Only the FILENAME diverges, which the stem knob reconciles.
	sessionsDir := claudeSessionsDir(home)
	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		t.Fatalf("mkdir sessions dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionsDir, initialUUID+".jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("pre-create initial jsonl: %v", err)
	}

	// The per-child JSONL trigger dir must exist before the daemon (and therefore
	// its children) start; the fake only ever renames out of it.
	trigDir := filepath.Join(home, "jsonl-trig")
	if err := os.MkdirAll(trigDir, 0o700); err != nil {
		t.Fatalf("mkdir jsonl trigger dir: %v", err)
	}

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })

	neverRotate := filepath.Join(home, "never-rotate.trig")
	stdinLog := filepath.Join(home, "stdin.log")

	// All three fake knobs ride on the DAEMON, so both children inherit them.
	//
	//   - SESSION_ID_FROM_ARGV: the minted child takes its stem from its own spawn
	//     argv, so it writes the file the daemon tails and appendTurnEnd lands in
	//     the tailed range. The bootstrap child's argv carries initialUUID
	//     (seedBootstrapRegistry pins it), so its stem is byte-identical either way.
	//   - JSONL_TRIGGER_DIR: the mid-turn drop is keyed on the child's OWN stem, so
	//     <trigDir>/<mintedID>.jsonl.trig is claimable by the minted child alone.
	//     The shared PYRY_FAKE_CLAUDE_JSONL_TRIGGER would have been a coin flip
	//     between the two children.
	//   - ESC_ENDS_TURN: deliberately daemon-wide, NOT minted-only. Both children
	//     get an ESC→end_turn handler, which is what makes the bootstrap transcript
	//     a mis-route detector in Phase 5 rather than a blind spot.
	//
	// TUI mode and Esc-ends-turn coexist by design — they touch different bytes:
	// TUI emits the startup glyph + spinner, the ESC detector scans stdin for the
	// bare interrupt ESC (fakeclaude/main.go:120-124).
	StartRotationWithRelay(t, home, sessionsDir, initialUUID, neverRotate, stdinLog,
		fr.URL()+"/v2/server",
		"PYRY_MOBILE_V2=1",
		"PYRY_FAKE_CLAUDE_TUI=1",
		"PYRY_FAKE_CLAUDE_SESSION_ID_FROM_ARGV=1",
		"PYRY_FAKE_CLAUDE_JSONL_TRIGGER_DIR="+trigDir,
		"PYRY_FAKE_CLAUDE_ESC_ENDS_TURN=1",
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

	// nextEnv decrypts the next binary->phone application envelope, skipping non-noise_msg
	// inner frames, in capture order so the receive nonce stays in sequence. ONE recvCS
	// drains the whole test (create reply, send ack, turn_state, turn_end) — never read
	// frames on two paths. ok=false on deadline.
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

	// --- Phase 0 (AC4, wiring level). Interrupt BEFORE any other app frame, so
	// activeConversation.CurrentConversation() is still "" (it is stamped only on
	// sessionRouter.Route's success path) and activeInterrupter.SendEsc takes the
	// no_active_conv arm: inert, and above all NOT the bootstrap supervisor.
	//
	// Nothing is asserted here. The count is read in Phase 4, after the long
	// NATURAL settle that Phases 1-3 provide — that is deliberate: reading it now
	// would need a settle timer, and a timer-backed negative smuggles in a timing
	// assumption the deferred read does not.
	sealSend(protocol.Envelope{
		ID:   inertInterruptReqI,
		Type: protocol.TypeInterrupt,
		TS:   time.Now().UTC(),
	})

	// --- Phase 1: mint. All-null create_conversation (server defaults), so the
	// spawn dir resolves to the bootstrap workdir and the minted transcript lands in
	// the shared sessions dir. The interactive session may interleave broadcasts, so
	// drain until the conversation_created reply.
	sealSend(protocol.Envelope{
		ID:      createReqID,
		Type:    protocol.TypeCreateConversation,
		TS:      time.Now().UTC(),
		Payload: mustJSON(t, protocol.CreateConversationPayload{}),
	})
	var convID string
	createDeadline := time.Now().Add(15 * time.Second)
	for convID == "" {
		env, ok := nextEnv(createDeadline)
		if !ok {
			t.Fatal("Phase 1: did not receive conversation_created before deadline (the minted per-conversation session never came up)")
		}
		if env.Type == protocol.TypeError {
			t.Fatalf("Phase 1: unexpected error envelope while awaiting conversation_created: %s", string(env.Payload))
		}
		if env.Type != protocol.TypeConversationCreated {
			continue
		}
		var p protocol.ConversationCreatedPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			t.Fatalf("Phase 1: decode conversation_created payload: %v", err)
		}
		if p.ID == "" {
			t.Fatal("Phase 1: conversation_created carried an empty id")
		}
		convID = p.ID
	}

	// The wire reply carries the conversation id, not the session id; both the
	// injection path and the transcript assertions are keyed on the SESSION stem, so
	// read the binding off disk.
	convPath := filepath.Join(home, ".pyry", "test", "conversations.json")
	mintedID := boundSessionID(t, convPath, convID)
	t.Logf("Phase 1: minted per-conversation session %s for conversation %s", mintedID, convID)

	// --- Phase 2: move the cursor. The send_message stamps active = convID, which
	// is what resolveBoundRunner will resolve at interrupt time, and re-keys the
	// turn-stream subscription onto the minted session. Await the sealed ack: it
	// confirms WriteUserTurn ran, so the stamp has landed.
	//
	// The prompt is short and plain-ASCII (no ESC), so its bracketed paste is
	// 0x1b 0x5b … and contributes NO bare ESC to the stdin log. That is what keeps
	// the Phase-4 and Phase-5 counts meaningful.
	sealSend(protocol.Envelope{
		ID:   sendReqID,
		Type: protocol.TypeSendMessage,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.SendMessagePayload{
			ConversationID: convID,
			MessageID:      "u-1",
			Text:           "e2e-1191:go\n",
		}),
	})
	ackDeadline := time.Now().Add(15 * time.Second)
	gotAck := false
	for !gotAck {
		env, ok := nextEnv(ackDeadline)
		if !ok {
			t.Fatal("Phase 2: did not receive the send_message ack for the minted conversation before deadline")
		}
		if env.Type == protocol.TypeError {
			t.Fatalf("Phase 2: unexpected error envelope while awaiting the send_message ack: %s", string(env.Payload))
		}
		if env.Type == protocol.TypeAck && env.InReplyTo != nil && *env.InReplyTo == sendReqID {
			gotAck = true
		}
	}

	// --- Phase 3 (AC2 pre-guard): the turn must be genuinely RUNNING before the
	// interrupt, or "an interrupt stopped it" means nothing.
	//
	// Since #1244 the kicker is MORE than a vacuous-pass guard — it is a
	// PRECONDITION for the turn_end below to reach the wire at all. The emitter drops
	// a TurnEnd when no turn is open (cmd/pyry/interactive_turn_v2.go:208-213, debug
	// event interactive_turn.turn_end_no_turn, nothing on the wire). The fake's old
	// canned line was self-sufficient — an assistant end_turn entry produced BOTH a
	// TextChunk (which opened the turn via startTurnIfNeeded) and a TurnEnd. The
	// interruption marker produces ONE event and cannot open its own turn, so the
	// kicker's TextChunk is what opens it. The turn then stays open until the
	// interrupt: endTurn() has exactly two call sites (:159, the follow-active
	// conversation switch — impossible here, one conversation, cursor stamped once;
	// and :217, the TurnEnd arm itself), and nothing closes a turn on inactivity.
	//
	// Re-drop rather than write once (#929): the producer waits one
	// subscribeRetryDelay before subscribing and then tails from EOF, so a
	// single-shot append lands BELOW the tailed range and is never emitted. A real
	// running turn streams assistant output continuously until interrupted; the
	// kicker mirrors that, so whenever the subscription settles it catches a
	// responding line. Re-dropping is safe: the only content dropped is an
	// assistant-TEXT line with no stop_reason, so it can never fabricate a turn_end.
	mintedTrig := filepath.Join(trigDir, mintedID+".jsonl.trig")
	stopKick := make(chan struct{})
	var stopKickOnce sync.Once
	stopKicking := func() { stopKickOnce.Do(func() { close(stopKick) }) }
	t.Cleanup(stopKicking)
	go func() {
		for {
			_ = os.WriteFile(mintedTrig, []byte(perConvMidTurnLine), 0o600)
			select {
			case <-stopKick:
				return
			case <-time.After(250 * time.Millisecond):
			}
		}
	}()

	sawMidTurn := false
	midTurnDeadline := time.Now().Add(20 * time.Second)
	for !sawMidTurn {
		env, ok := nextEnv(midTurnDeadline)
		if !ok {
			t.Fatalf("Phase 3/vacuous-pass guard: never observed a non-idle turn_state for the minted conversation %s "+
				"before the interrupt; without a running turn, \"an interrupt stopped it\" would pass vacuously. "+
				"The kicker drops %s; the minted child must claim it and grow %s",
				convID, mintedTrig, filepath.Join(sessionsDir, mintedID+".jsonl"))
		}
		if env.Type == protocol.TypeError {
			t.Fatalf("Phase 3: unexpected error envelope while awaiting a non-idle turn_state: %s", string(env.Payload))
		}
		if env.Type != protocol.TypeTurnState {
			continue
		}
		var st protocol.TurnStatePayload
		if err := json.Unmarshal(env.Payload, &st); err != nil {
			t.Fatalf("Phase 3: decode turn_state payload: %v", err)
		}
		if st.ConversationID == convID && st.State != "idle" {
			sawMidTurn = true
		}
	}
	stopKicking()
	t.Logf("Phase 3: the minted conversation %s has a running turn", convID)

	// --- Phase 4 (AC1) + the AC4 baseline. Read Phase 0's verdict first: an
	// interrupt sent while nothing was active must have actuated NO child. The
	// oracle is the shared stdin log every fakeclaude child appends to — it cannot
	// say WHICH child received an ESC, but a count of zero says none did.
	logBytes, err := os.ReadFile(stdinLog)
	if err != nil {
		t.Fatalf("Phase 4: read fakeclaude stdin log %s: %v", stdinLog, err)
	}
	if before := countBareESC(logBytes); before != 0 {
		t.Fatalf("AC4: %d bare ESC(s) in fakeclaude's stdin log BEFORE this test's interrupt; "+
			"the Phase-0 interrupt (sent with no active conversation) must be inert and actuate no child at all. "+
			"Do not weaken this assertion — localize the ESC first (log=%q)", before, logBytes)
	}

	// The interrupt under test. dispatchAppFrame intercepts it → handleInterrupt
	// (interactive ✓) → activeInterrupter.SendEsc → currentConv() == convID →
	// resolveBoundRunner → the minted session's *supervisor.Supervisor → armSendEsc
	// → a lone 0x1b on the MINTED child's PTY.
	sealSend(protocol.Envelope{
		ID:   interruptReqID,
		Type: protocol.TypeInterrupt,
		TS:   time.Now().UTC(),
	})

	var end protocol.TurnEndPayload
	gotEnd := false
	turnEndDeadline := time.Now().Add(15 * time.Second)
	for !gotEnd {
		env, ok := nextEnv(turnEndDeadline)
		if !ok {
			t.Fatalf("AC1: never received a turn_end for the minted conversation %s after the interrupt. "+
				"fakeclaude's bare-ESC handler is the ONLY source of an end-of-turn line in this test "+
				"(see the causality re-derivation on this test's doc comment), but a timeout no longer "+
				"has a single reading — DISCRIMINATE on the minted transcript %s: if it contains %q the "+
				"ESC arrived and the failure is downstream of the fake (the marker was held out by the "+
				"authorship gate — check for a stray top-level permissionMode key — or the TurnEnd was "+
				"dropped outside a turn; grep the daemon log for interactive_turn.turn_end_no_turn). If "+
				"it does not, the ESC never reached the minted child %s and the interrupt did not stop "+
				"the turn. Phase 5 would answer this but sits after this fatal",
				convID, filepath.Join(sessionsDir, mintedID+".jsonl"), interruptMarkerNeedle, mintedID)
		}
		if env.Type == protocol.TypeError {
			t.Fatalf("AC1: unexpected error envelope while awaiting turn_end: %s", string(env.Payload))
		}
		if env.Type != protocol.TypeTurnEnd {
			continue
		}
		if err := json.Unmarshal(env.Payload, &end); err != nil {
			t.Fatalf("AC1: decode turn_end payload: %v", err)
		}
		gotEnd = true
	}
	if end.ConversationID != convID {
		t.Fatalf("AC1: turn_end ConversationID = %q, want the minted conversation %q "+
			"(the interrupt must stop the turn of the conversation it was routed for)",
			end.ConversationID, convID)
	}
	// AC2: the turn_end must REPORT the stop as an interrupt, not just report it.
	// Asserted against the literal wire string, not turnevent.TurnEndReasonCancelled:
	// protocol.TurnEndPayload.StopReason is a plain string by decision
	// (docs/knowledge/features/protocol-package.md:740) and this tier asserts wire
	// bytes. This is the first thing to run claude's interruption marker through the
	// REAL tailer, producer and emitter — #1243's unit table covers the
	// discriminator's truth table, never this span.
	if end.StopReason != "cancelled" {
		t.Fatalf("AC2: turn_end StopReason = %q, want %q. %q here means the marker reached the wire "+
			"path but internal/turnbridge/mapper.go:95 did not classify it as an interruption — a live "+
			"regression of #1243, not a staging fault (a staging fault produces NO turn_end at all and "+
			"fails at the AC1 fatal above)",
			end.StopReason, "cancelled", "end_turn")
	}
	t.Logf("AC1+AC2: the interrupt stopped the minted conversation's turn and reported it cancelled "+
		"(turn_id=%s, conversation=%s, stop_reason=%s)", end.TurnID, convID, end.StopReason)

	// --- Phase 5 (AC2 on disk + AC4 teeth). turn_end.ConversationID is stamped from
	// the ACTIVE CURSOR, not from the transcript the event came out of, so Phase 4
	// alone proves only what the emitter believed. The on-disk pair proves the
	// TARGETING, over bytes rather than the wire — a different fabric.
	mintedPath := filepath.Join(sessionsDir, mintedID+".jsonl")
	bootstrapPath := filepath.Join(sessionsDir, initialUUID+".jsonl")
	if mintedPath == bootstrapPath {
		t.Fatalf("Phase 5: minted and bootstrap transcript paths are identical (%s); the assertion pair is meaningless", mintedPath)
	}
	mintedBody, err := os.ReadFile(mintedPath)
	if err != nil {
		t.Fatalf("Phase 5: read minted transcript %s: %v (the minted child must write the file the daemon tails)", mintedPath, err)
	}
	if !strings.Contains(string(mintedBody), interruptMarkerNeedle) {
		t.Errorf("Phase 5: minted transcript %s does not contain %q; the turn_end above did not come from the "+
			"minted child's own ESC handler", mintedPath, interruptMarkerNeedle)
	}
	// The mis-route detector. ESC_ENDS_TURN is set daemon-wide, so the bootstrap
	// child has the same handler: had the interrupt actuated it (the pre-#1121
	// Interrupter: w.sup wiring, the #678 isolation break), the interruption marker
	// would be HERE.
	//
	// This absence check is non-vacuous BY THE POSITIVE ABOVE, in this same run: the
	// same needle over the same handler's output is found in the minted file. See
	// interruptMarkerNeedle for why the needle cannot silently stop appearing.
	bootstrapBody, err := os.ReadFile(bootstrapPath)
	if err != nil {
		t.Fatalf("Phase 5: read bootstrap transcript %s: %v", bootstrapPath, err)
	}
	if strings.Contains(string(bootstrapBody), interruptMarkerNeedle) {
		t.Errorf("Phase 5: bootstrap transcript %s contains %q; the interrupt actuated the SHARED BOOTSTRAP claude "+
			"instead of the minted per-conversation child (#678/#1121 mis-route)", bootstrapPath, interruptMarkerNeedle)
	}

	// AC4's teeth, and what makes the Phase-4 zero non-vacuous: the same oracle must
	// now report exactly ONE bare ESC — the one this test sent. The turn_end
	// happens-after the ESC was read, so it is already on disk; the short bounded
	// poll only closes the residual cross-process fsync-visibility window (the #794
	// sibling's pattern, relay_v2_interrupt_test.go:331-339).
	after := 0
	escDeadline := time.Now().Add(2 * time.Second)
	for {
		logBytes, _ = os.ReadFile(stdinLog)
		after = countBareESC(logBytes)
		if after >= 1 || time.Now().After(escDeadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if after != 1 {
		t.Errorf("AC4: fakeclaude's stdin log holds %d bare ESC(s), want exactly 1 (this test's Phase-4 interrupt). "+
			"0 means the interrupt never reached a child; more than 1 means an interrupt actuated a child it should "+
			"not have. log=%q", after, logBytes)
	}
	t.Logf("AC4: exactly one bare ESC reached a child across the whole run; the inert Phase-0 interrupt actuated none")
}

// countBareESC returns how many bare ESCs — 0x1b bytes not immediately followed by
// 0x5b ('[') — appear in b. Every bracketed-paste marker tui-driver writes is
// 0x1b 0x5b and a delivered prompt's content is raw-ESC-free (#749), so each bare
// 0x1b in fakeclaude's stdin log is one supervisor.SendEsc actuation. The counting
// form of hasBareESC.
//
// hasBareESC is deliberately left alone rather than re-expressed over this
// function: it lives in #794's shipped live capstone, and editing that file to
// save ~10 lines would add merge surface against every in-flight branch and put an
// exit-gate test at risk for a cosmetic win. The duplication is a decision.
func countBareESC(b []byte) int {
	n := 0
	for i, c := range b {
		if c != 0x1b {
			continue
		}
		if i == len(b)-1 || b[i+1] != 0x5b {
			n++
		}
	}
	return n
}

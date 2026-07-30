//go:build e2e

package e2e

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// TestRelayV2_InterruptStopsRunningTurn is the LIVE interrupt capstone for #794
// (EPIC #597 Phase 3 live exit gate; mirrors the Phase 2 capstone #642). It is the
// live confirmation that a phone-originated `interrupt` reaches the supervised
// claude as an Esc and CAUSES the running turn to stop. One interactive phone
// handshakes to ONE daemon over a real Noise session, drives a turn (stamping the
// supervisor cursor), observes the turn go mid-turn, sends a sealed `interrupt`
// frame, and observes the turn_end the interrupt causes.
//
// The interrupt production path is shipped and was deterministically proved
// upstream (#707 wire type + Esc routing, #726 SendEsc). This test does not
// re-prove correctness — it confirms the wired path live over a real (fake) claude
// that is genuinely mid-turn when the interrupt arrives:
//
//	phone interrupt frame → Noise decrypt → interactive gate → handleInterrupt →
//	supervisor.SendEsc() → lone 0x1b → fakeclaude → end_turn JSONL line →
//	structured-turn producer → turnbridge mapper → emitter → turn_end → phone.
//
// VACUOUS-PASS GUARD (the headline requirement the PO hoisted to AC1). "An
// interrupt stopped the turn" passes vacuously if the turn had already ended, or
// if the turn_end came from anywhere but the Esc. Two mechanisms defeat that:
//
//   - Structural causality: fakeclaude's bare-ESC handler (envEscEndsTurn) is the
//     ONLY source of an end-of-turn JSONL line in this test — the test never drops
//     an end-of-turn fixture via the JSONL trigger. So a turn_end ⟺ the Esc was
//     received and processed. (The busy→idle-by-file shape #792 reuses would NOT
//     have this property; here the Esc byte itself drives the flip.)
//   - Two ordered t.Fatal guards: a non-idle turn_state MUST be observed BEFORE
//     the interrupt is sent (the turn was running — AC1), and a turn_end MUST be
//     observed AFTER it (the turn stopped because of the Esc — AC3).
//
// A separate direct oracle (AC2) reads fakeclaude's own stdin log and asserts a
// bare ESC is present — belt-and-suspenders with different fabric (fakeclaude's
// byte record vs. the daemon's wire report).
//
// StopReason fidelity: the turn_end carries StopReason=="cancelled". #1243 taught
// the mapper to recognise claude's interruption marker (turnbridge/mapper.go:95)
// and #1244 restaged fakeclaude's ESC handler to write that marker rather than a
// canned assistant end_turn line, so the tui-driver limitation this paragraph used
// to record — EventKindJsonlEndOfTurn cannot distinguish an interrupt-stop from a
// normal end — no longer decides the reason on this path.
//
// This test still does NOT assert StopReason, now by choice rather than by
// impossibility: it proves CAUSALITY (the turn_end exists only because the Esc was
// received) on the BOOTSTRAP session, and the reason assertion belongs one tier up
// on the minted path, where TestRelayV2_PerConversationInterruptStopsRunningTurn
// (relay_v2_perconv_interrupt_test.go) owns it. Duplicating it here would add a
// second maintenance site for #1243's mapping and nothing else.
//
// AC4 (flight-recorder audit) is NOT asserted here: the flight recorder
// (internal/agentrun/ptyrunner, PYRY_RECORD_DIR) is reached only from the
// agent-run/dispatcher path, never the mobile-relay path where claude is hosted
// by internal/supervisor. No single run is both a mobile interrupt and recorded.
// Deferred to the operator live run (mobile-live-e2e-runbook), same as #792/#791.
func TestRelayV2_InterruptStopsRunningTurn(t *testing.T) {
	const (
		knownConvID    = "77777777-7777-4777-8777-777777777777"
		knownUserText  = "e2e-794-user:go\n"
		initialUUID    = "66666666-6666-4666-8666-666666666666"
		sendReqID      = uint64(94)
		interruptReqID = uint64(95)
	)

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

	// Bind knownConvID to the bootstrap session so sessionRouter.Route resolves
	// (#678) and WriteUserTurn's ValidateConversation gate passes → the supervisor
	// cursor stamp lands, which the structured emitter needs to address envelopes.
	seedBoundConversation(t, home, knownConvID, initialUUID)

	// Align the sessions dir to the daemon's COMPUTED path so the structured
	// producer tails exactly what fakeclaude writes, and pre-create
	// <initialUUID>.jsonl BEFORE the daemon starts so the first resolve succeeds at
	// a tiny offset and every appended line lands in the tailed range (the #642
	// cold-start recipe).
	sessionsDir := claudeSessionsDir(home)
	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		t.Fatalf("mkdir sessions dir: %v", err)
	}
	initialJSONL := filepath.Join(sessionsDir, initialUUID+".jsonl")
	if err := os.WriteFile(initialJSONL, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("pre-create initial jsonl: %v", err)
	}

	tmp := t.TempDir()
	rotateTrigger := filepath.Join(tmp, "rotate.trigger.never-created")
	stdinLog := filepath.Join(tmp, "fakeclaude-stdin.log")
	jsonlTrigger := filepath.Join(tmp, "structured.jsonl.trigger")

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })

	// TUI mode ON so WaitReady confirms fast and the send_message ack is prompt
	// (#603). Esc-ends-turn mode ON so the interrupt's bare ESC drives the turn's
	// end. The two coexist (they touch different bytes): TUI emits the startup
	// glyph + spinner; the ESC detector scans stdin for the bare interrupt ESC.
	h := StartRotationWithRelay(t, home, sessionsDir, initialUUID, rotateTrigger,
		stdinLog, fr.URL()+"/v2/server",
		"PYRY_MOBILE_V2=1",
		"PYRY_FAKE_CLAUDE_TUI=1",
		"PYRY_FAKE_CLAUDE_JSONL_TRIGGER="+jsonlTrigger,
		"PYRY_FAKE_CLAUDE_ESC_ENDS_TURN=1",
	)
	t.Cleanup(func() { h.Stop(t) })

	serverID := readPersistedServerID(t, home)
	waitBinaryHello(t, fr, serverID)

	// Phone A: interactive — the capability both the structured stream AND
	// handleInterrupt require. driveHandshakeToOpenDaemonInteractive pins the grant.
	dialCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	phoneA, err := fakephone.Dial(dialCtx, fr.URL(), serverID, payloadA.Token, "phone-a")
	if err != nil {
		t.Fatalf("phone A dial: %v", err)
	}
	t.Cleanup(func() { _ = phoneA.Close() })
	sendA, recvA := driveHandshakeToOpenDaemonInteractive(t, phoneA, pubKey, payloadA.Token)

	// --- Start the turn. Send one plain-ASCII send_message and AWAIT its sealed
	// ack. The ack confirms WriteUserTurn ran → the cursor is stamped (knownConvID)
	// → the structured emitter addresses envelopes to it. The prompt is short and
	// plain-ASCII (no ESC) so its bracketed paste is atomic and contributes no bare
	// ESC to the stdin log — only the later interrupt does.
	reqEnv, err := json.Marshal(protocol.Envelope{
		ID:   sendReqID,
		Type: protocol.TypeSendMessage,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.SendMessagePayload{
			ConversationID: knownConvID,
			MessageID:      "u-1",
			Text:           knownUserText,
		}),
	})
	if err != nil {
		t.Fatalf("marshal send_message envelope: %v", err)
	}
	sendCipher, err := sendA.Encrypt(reqEnv)
	if err != nil {
		t.Fatalf("seal send_message envelope: %v", err)
	}
	sendNoiseMsg(t, phoneA, sendCipher)

	var ackEnv protocol.Envelope
	ackDeadline := time.Now().Add(15 * time.Second)
	for {
		remaining := time.Until(ackDeadline)
		if remaining <= 0 {
			t.Fatal("interactive phone A did not receive the sealed send_message ack before deadline")
		}
		env := decryptInnerEnvelope(t, readInnerFrame(t, phoneA, remaining), recvA)
		if env.Type == protocol.TypeAck {
			ackEnv = env
			break
		}
	}
	if ackEnv.InReplyTo == nil || *ackEnv.InReplyTo != sendReqID {
		t.Errorf("ack InReplyTo = %v, want pointer to %d", ackEnv.InReplyTo, sendReqID)
	}

	// --- AC1: establish + observe the mid-turn window. Kick the turn with a SINGLE
	// assistant-text line (NOT an end-of-turn line): fakeclaude appends it →
	// producer → TextChunk → emitter emits turn_state{State:"responding"}. Draining
	// to a non-idle turn_state proves the turn is running / had NOT already ended
	// (vacuous-pass guard #1).
	//
	// The line is RE-DROPPED on a ticker rather than written once. Since #854 the
	// structured-turn producer resolves the bootstrap transcript from the PID that
	// has it open (resolveOwnBootstrapJSONL), so its subscription only settles once
	// fakeclaude's child fd is probed — ~500 ms after startup (one subscribeRetryDelay
	// after the first "no jsonl open yet"), and it tails from EOF. A single line
	// dropped right after the send ack lands BELOW that EOF and is never tailed, so
	// no turn_state ever reaches the phone (the failure this ticket fixes). A real
	// running turn streams assistant output continuously until interrupted; the
	// re-drop mirrors that, so whenever the subscription settles it catches a
	// responding line. The kicker is stopped the instant AC1 observes the turn —
	// strictly before the interrupt — so fakeclaude's bare-ESC handler stays the ONLY
	// source of an end-of-turn line (vacuous-pass guard #1 intact). The line is
	// assistant-text, never an end_turn, so re-dropping can never fabricate a
	// turn_end (guard #2 intact).
	midTurnLine := `{"type":"assistant","message":{"id":"m1","content":[{"type":"text","text":"Working on it."}]}}` + "\n"
	stopKick := make(chan struct{})
	var stopKickOnce sync.Once
	stopKicking := func() { stopKickOnce.Do(func() { close(stopKick) }) }
	t.Cleanup(stopKicking)
	go func() {
		for {
			_ = os.WriteFile(jsonlTrigger, []byte(midTurnLine), 0o600)
			select {
			case <-stopKick:
				return
			case <-time.After(250 * time.Millisecond):
			}
		}
	}()

	sawMidTurn := false
	ac1Deadline := time.Now().Add(20 * time.Second)
	for !sawMidTurn {
		remaining := time.Until(ac1Deadline)
		if remaining <= 0 {
			t.Fatal("AC1/vacuous-pass guard #1: interactive phone A never observed a non-idle turn_state before the interrupt; " +
				"the harness never started a turn, so an \"interrupt stopped it\" would pass vacuously")
		}
		raw, err := phoneA.ReceiveBytes(remaining)
		if err != nil {
			if errors.Is(err, fakephone.ErrReceiveTimeout) {
				t.Fatal("AC1/vacuous-pass guard #1: no non-idle turn_state before the deadline (receive timed out); the turn never started")
			}
			t.Fatalf("phone A receive (AC1): %v", err)
		}
		var inner protocol.InnerFrameV2
		if err := json.Unmarshal(raw, &inner); err != nil {
			t.Fatalf("phone A decode inner frame (AC1): %v", err)
		}
		if inner.Type != protocol.TypeNoiseMsg {
			continue
		}
		env := decryptInnerEnvelope(t, inner, recvA)
		if env.Type != protocol.TypeTurnState {
			continue
		}
		var st protocol.TurnStatePayload
		if err := json.Unmarshal(env.Payload, &st); err != nil {
			t.Fatalf("phone A decode turn_state payload (AC1): %v", err)
		}
		if st.ConversationID != knownConvID {
			t.Errorf("turn_state ConversationID: got %q, want %q", st.ConversationID, knownConvID)
		}
		if st.State != "idle" {
			sawMidTurn = true
		}
	}
	stopKicking()
	t.Logf("AC1: interactive phone A observed a non-idle turn_state — the turn is running")

	// --- Interrupt (AC2 setup). Seal an `interrupt` frame (no payload) with phone
	// A's send CipherState. The daemon's dispatchAppFrame intercepts it before
	// dispatch.Route → handleInterrupt (interactive ✓) → SendEsc() → a lone 0x1b to
	// fakeclaude's stdin.
	interruptEnv, err := json.Marshal(protocol.Envelope{
		ID:   interruptReqID,
		Type: protocol.TypeInterrupt,
		TS:   time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("marshal interrupt envelope: %v", err)
	}
	intCipher, err := sendA.Encrypt(interruptEnv)
	if err != nil {
		t.Fatalf("seal interrupt envelope: %v", err)
	}
	sendNoiseMsg(t, phoneA, intCipher)

	// --- AC3: observe the turn stop CAUSED by the interrupt. fakeclaude's bare-ESC
	// detector fires → appendTurnEnd → producer → TurnEnd → emitter → turn_end.
	// Since the test never dropped an end-of-turn line, this turn_end's ONLY source
	// is the bare-ESC handler — observing it proves the Esc was received and stopped
	// the turn (vacuous-pass guard #2). StopReason is NOT asserted (see the doc
	// comment's fidelity note).
	sawTurnEnd := false
	ac3Deadline := time.Now().Add(10 * time.Second)
	for !sawTurnEnd {
		remaining := time.Until(ac3Deadline)
		if remaining <= 0 {
			t.Fatal("AC3/vacuous-pass guard #2: interactive phone A never received a turn_end after the interrupt; " +
				"the interrupt did not stop the turn (the Esc never reached claude or the detector did not fire). " +
				"The ONLY source of a turn_end in this test is fakeclaude's bare-ESC handler")
		}
		raw, err := phoneA.ReceiveBytes(remaining)
		if err != nil {
			if errors.Is(err, fakephone.ErrReceiveTimeout) {
				t.Fatal("AC3/vacuous-pass guard #2: no turn_end after the interrupt (receive timed out); the interrupt did not stop the turn")
			}
			t.Fatalf("phone A receive (AC3): %v", err)
		}
		var inner protocol.InnerFrameV2
		if err := json.Unmarshal(raw, &inner); err != nil {
			t.Fatalf("phone A decode inner frame (AC3): %v", err)
		}
		if inner.Type != protocol.TypeNoiseMsg {
			continue
		}
		env := decryptInnerEnvelope(t, inner, recvA)
		if env.Type != protocol.TypeTurnEnd {
			continue
		}
		var te protocol.TurnEndPayload
		if err := json.Unmarshal(env.Payload, &te); err != nil {
			t.Fatalf("phone A decode turn_end payload (AC3): %v", err)
		}
		if te.ConversationID != knownConvID {
			t.Errorf("turn_end ConversationID: got %q, want %q", te.ConversationID, knownConvID)
		}
		sawTurnEnd = true
	}
	t.Logf("AC3: interactive phone A received turn_end — the daemon reports the turn stopped by the interrupt")

	// --- AC2: direct keystroke oracle. fakeclaude's stdin log must contain a bare
	// ESC — a 0x1b not immediately followed by 0x5b ('['). Every bracketed-paste
	// marker is 0x1b 0x5b, so a bare 0x1b is unambiguously the interrupt. This is
	// fakeclaude's own byte record of what it received; the turn_end (AC3) is the
	// daemon's independent wire report — two oracles, different sources. The AC3
	// turn_end happens-after the Esc was read and the end-of-turn line committed, so
	// the bare ESC is on disk by now; a short bounded poll closes the residual
	// cross-process fsync-visibility window (mirrors TestFakeClaude_StdinLog).
	var logBytes []byte
	escDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(escDeadline) {
		logBytes, _ = os.ReadFile(stdinLog)
		if hasBareESC(logBytes) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !hasBareESC(logBytes) {
		t.Fatalf("AC2: fakeclaude's stdin log has no bare ESC (0x1b not followed by 0x5b); the interrupt frame never routed to SendEsc "+
			"(e.g. a capability-gate regression). log=%q", logBytes)
	}
	t.Logf("AC2: fakeclaude's stdin log contains the bare interrupt ESC")
}

// hasBareESC reports whether b contains a bare ESC — a 0x1b that is not
// immediately followed by 0x5b ('['). Every bracketed-paste marker tui-driver
// writes is 0x1b 0x5b, so a bare 0x1b in fakeclaude's stdin log is unambiguously
// the interrupt keystroke (supervisor.SendEsc's lone 0x1b). Mirrors fakeclaude's
// containsBareESC detector, applied over the whole on-disk log.
func hasBareESC(b []byte) bool {
	for i, c := range b {
		if c != 0x1b {
			continue
		}
		if i == len(b)-1 || b[i+1] != 0x5b {
			return true
		}
	}
	return false
}

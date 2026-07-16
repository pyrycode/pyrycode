//go:build e2e

package e2e

// This file is the LIVE untrusted-cwd trust-flow capstone for #993 (split from
// #988), certifying end-to-end — over one spawned daemon + a real (fake) claude
// that raises the startup trust-folder dialog — the no-auto-trust consent gate the
// upstream tickets proved deterministically: a conversation created in a cwd the
// daemon host has NOT trusted forwards claude's startup trust-folder dialog to an
// interactive phone as modal_shown{trust} (#708 producer); the queued (untrusted)
// turn is HELD, never typed into the consent gate, while the modal is up (#1013
// ErrTrustModalPending); answering to ACCEPT (proceed → Supervisor.AcceptTrust)
// clears trust and runs the held turn (a reply is produced); answering to DENY
// (exit → Supervisor.SendEsc) yields the typed, terminal session_error
// {session.blocked, "folder not trusted"} (#1014) and genuinely blocks the turn —
// no reply is produced, and the deny does NOT enter a retry loop.
//
// It RIDES the shipped seams and introduces no production behaviour: the only
// non-test code is the fakeclaude trust-dialog simulation (PYRY_FAKE_CLAUDE_TRUST_
// TRIGGER), used solely by this test. The real-claude PTY path is blocked by a
// claude/tui-driver version mismatch, so a fakeclaude-simulated startup trust
// dialog is the CI-faithful runnable path (same disposition as #791/#792 for the
// permission modal). The fixture shape is de-risked in milliseconds by the untagged
// internal/fakeclaude/trust_detect_test.go before it ever runs here.
//
// Scope: deny-by-ANSWER (optExit) is the required fast path (deterministic,
// seconds). Deny-ON-TIMEOUT (waiting out the real ~2-minute modalDenyTimeout)
// exercises ResolveTimeout's identical emitFolderNotTrusted; it is OUT OF SCOPE
// here to keep CI fast — the trust-class timeout emit is already unit-covered by
// #1014's modal_resolve_v2_test.go, and TestRelayV2_RemotePermissionDeniedOnTimeout
// already carries one ~2-min e2e for the timeout timer.
//
// All markers here are test-only ASCII: the queued turn is "e2e-993-turn"; the
// accept oracle is "1\r" (AcceptTrust's trust-accept keystroke) and the deny oracle
// is a bare ESC (0x1b). Do NOT paste secrets — the stdin log is echoed in failure
// messages. The file stays substrate-clean: a bare 0x1b is fine; a 0x1b followed by
// '[' is the banned CSI form and never appears here.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// trustModalHarness is the shared bring-up for both #993 tests: one daemon hosting
// a trust-trigger fakeclaude (untrusted cwd), plus one GATED interactive phone with
// sealed-send + single-deadline decrypt-drain helpers, and a bound conversation so
// the queued turn resolves. Each test raises exactly one trust dialog and answers
// it once.
type trustModalHarness struct {
	phone     *fakephone.Client
	sealSend  func(env protocol.Envelope)
	nextEnv   func(deadline time.Time) (protocol.Envelope, bool)
	stdinLog  string
	trustTrig string
	convID    string
}

// bringUpTrustModalHarness spawns the daemon + fakeclaude (trust-trigger mode) and
// opens the gated interactive phone's Noise session. It is bringUpModalHarness
// (relay_v2_modal_answer_test.go) with two deltas: (a) it forwards
// PYRY_FAKE_CLAUDE_TRUST_TRIGGER instead of the permission trigger, and (b) it
// seeds a bound conversation BEFORE the daemon starts (seedBoundConversation) so
// the queued send_message resolves to the bootstrap session — the untrusted-cwd
// conversation of AC-1. The interactive pairing carries --allow-remote-permissions
// (the #702 device gate ResolveAnswer requires for any modal answer, trust
// included).
func bringUpTrustModalHarness(t *testing.T) *trustModalHarness {
	t.Helper()
	const (
		initialUUID = "44444444-4444-4444-8444-444444444444"
		knownConvID = "77777777-7777-4777-8777-777777777777"
	)

	home := shortHome(t)

	// Pair the answering phone WITH --allow-remote-permissions: ResolveAnswer gates
	// EVERY modal answer (trust included) on s.device.MayAnswerRemotePermission()
	// (== AllowRemotePermissions), which pairing defaults OFF. Without this flag the
	// trust answer denies at the gate before ever routing AcceptTrust / SendEsc.
	r := RunBareIn(t, home, "pair", "-pyry-name=test", "--name=phone-a", "--allow-remote-permissions")
	if r.ExitCode != 0 {
		t.Fatalf("pyry pair exit=%d\nstdout:\n%s\nstderr:\n%s", r.ExitCode, r.Stdout, r.Stderr)
	}
	payload := decodePairPayload(t, r.Stdout)
	pubKey, err := base64.StdEncoding.DecodeString(payload.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	// Bind knownConvID to the bootstrap session (== initialUUID after reconciliation)
	// so send_message's router.Route resolves and the queued turn enqueues instead of
	// rejecting pre-enqueue (#678). Must exist BEFORE the daemon starts (in-memory
	// registry, no reload).
	seedBoundConversation(t, home, knownConvID, initialUUID)

	// Align the sessions dir to the daemon's COMPUTED path and pre-create
	// <initialUUID>.jsonl BEFORE the daemon starts so the bootstrap session id
	// reconciles to initialUUID (the #792 / rotation-test pattern; HOME=home,
	// -pyry-workdir=home).
	sessionsDir := claudeSessionsDir(home)
	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		t.Fatalf("mkdir sessions dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionsDir, initialUUID+".jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("pre-create initial jsonl: %v", err)
	}

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })

	// Test-owned paths under home. neverRotate is never created (no /clear rotation);
	// trustTrig is the trust-dialog-raise signal; stdinLog is the keystroke oracle
	// (StartRotationWithRelay sets PYRY_FAKE_CLAUDE_STDIN_LOG).
	neverRotate := filepath.Join(home, "never-rotate.trig")
	trustTrig := filepath.Join(home, "trust.trig")
	stdinLog := filepath.Join(home, "stdin.log")

	// Trust-trigger fakeclaude: PYRY_FAKE_CLAUDE_TRUST_TRIGGER makes it come up idle
	// (baseline glyph) and raise the startup trust-folder dialog on the trigger's
	// first appearance, then clear it only on the accept keystroke; PYRY_MOBILE_V2=1
	// selects the v2 relay path. The extraEnv seam forwards the trust trigger with no
	// harness.go change (the #642 seam).
	StartRotationWithRelay(t, home, sessionsDir, initialUUID, neverRotate, stdinLog,
		fr.URL()+"/v2/server",
		"PYRY_MOBILE_V2=1",
		"PYRY_FAKE_CLAUDE_TRUST_TRIGGER="+trustTrig,
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

	// Interactive handshake: grants the `interactive` capability the modal_shown /
	// session_error broadcasts ride. The device gate rode the pairing.
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

	// nextEnv decrypts the next binary->phone application envelope, skipping
	// non-noise_msg inner frames. Every noise_msg is decrypted in capture order so
	// the receive nonce stays in sequence (603.md/634.md). Callers pass a single
	// long deadline with back-to-back reads — never a short poll on a phone conn
	// (fakephone closes the WS on a timed-out Receive). ok=false on deadline.
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

	return &trustModalHarness{
		phone:     phone,
		sealSend:  sealSend,
		nextEnv:   nextEnv,
		stdinLog:  stdinLog,
		trustTrig: trustTrig,
		convID:    knownConvID,
	}
}

// awaitTrustModalShown raises one trust dialog (drops trustTrig) and drains the
// phone's inbound envelopes under one long deadline until a modal_shown{trust}
// arrives, returning its decoded payload. It asserts the class and the fixed trust
// option IDs [proceed, exit] read OFF THE WIRE (AC-2, non-vacuous). t.Fatal if none
// before ~20 s (the harness-produced-no-modal vacuous-pass guard, shared by both
// tests).
func awaitTrustModalShown(t *testing.T, h *trustModalHarness) protocol.ModalShownPayload {
	t.Helper()
	if err := os.WriteFile(h.trustTrig, []byte("go\n"), 0o600); err != nil {
		t.Fatalf("write trust trigger: %v", err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		env, ok := h.nextEnv(deadline)
		if !ok {
			t.Fatal("did not observe a modal_shown before deadline (harness produced no trust modal — the fixture may not classify as trust-folder, or the modal stream is not wired)")
		}
		if env.Type == protocol.TypeError {
			t.Fatalf("unexpected error envelope while awaiting modal_shown: %s", string(env.Payload))
		}
		if env.Type != protocol.TypeModalShown {
			continue
		}
		var shown protocol.ModalShownPayload
		if err := json.Unmarshal(env.Payload, &shown); err != nil {
			t.Fatalf("decode modal_shown payload: %v", err)
		}
		if shown.ModalID == "" {
			t.Fatal("modal_shown carried an empty modal_id")
		}
		if shown.Class != "trust" {
			t.Fatalf("modal_shown Class = %q, want %q (the untrusted-cwd dialog must forward as the trust class)", shown.Class, "trust")
		}
		gotIDs := make([]string, len(shown.Options))
		for i, o := range shown.Options {
			gotIDs[i] = o.ID
		}
		if wantIDs := []string{"proceed", "exit"}; !slices.Equal(gotIDs, wantIDs) {
			t.Fatalf("modal_shown option IDs = %v, want %v (the fixed trust option set)", gotIDs, wantIDs)
		}
		return shown
	}
}

// sendQueuedTurn sends the queued (untrusted) turn and drains queue_state until the
// marker is observed queued (len 1), proving it really enqueued and is held. Then
// the HELD-FENCE: the stdin log must NOT yet contain the marker — the turn was held
// before DeliverPrompt (#1013), not merely not-yet-delivered. Returns after the
// fence so callers answer the modal against a genuinely-held turn.
func sendQueuedTurn(t *testing.T, h *trustModalHarness, reqID uint64, marker string) {
	t.Helper()
	h.sealSend(protocol.Envelope{
		ID:      reqID,
		Type:    protocol.TypeSendMessage,
		TS:      time.Now().UTC(),
		Payload: mustJSON(t, protocol.SendMessagePayload{ConversationID: h.convID, MessageID: "u-993", Text: marker}),
	})

	deadline := time.Now().Add(20 * time.Second)
	for {
		env, ok := h.nextEnv(deadline)
		if !ok {
			t.Fatal("did not observe a queue_state with the queued turn before deadline (enqueue may have rejected; check the binding)")
		}
		if env.Type == protocol.TypeError {
			t.Fatalf("unexpected error envelope while enqueueing the turn: %s", string(env.Payload))
		}
		if env.Type != protocol.TypeQueueState {
			continue
		}
		var qs protocol.QueueStatePayload
		if err := json.Unmarshal(env.Payload, &qs); err != nil {
			t.Fatalf("decode queue_state payload: %v", err)
		}
		if len(qs.Queued) == 1 && qs.Queued[0].Text == marker {
			break
		}
	}

	// Held-fence: the turn is queued (above), but readyForDelivery returned
	// ErrTrustModalPending BEFORE DeliverPrompt, so nothing was written to the PTY —
	// the marker is absent from the stdin log while the modal is up. A non-existent
	// log file is equivalent to empty. This certifies the hold is real (not that
	// delivery merely hasn't happened yet).
	if data, err := os.ReadFile(h.stdinLog); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read stdin log during held window: %v", err)
		}
	} else if bytes.Contains(data, []byte(marker)) {
		t.Fatalf("stdin log contains the queued turn %q while the trust modal is up — the untrusted turn reached claude's consent gate (held-fence violated, #1013)\nstdin log: %q", marker, data)
	}
}

// answerModal seals a modal_answer for modalID with optionID (proceed = accept,
// exit = deny). AnswerToken is a client idempotency key, not authorization.
func answerModal(t *testing.T, h *trustModalHarness, reqID uint64, modalID, optionID string) {
	t.Helper()
	h.sealSend(protocol.Envelope{
		ID:   reqID,
		Type: protocol.TypeModalAnswer,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.ModalAnswerPayload{
			ModalID:     modalID,
			OptionID:    optionID,
			AnswerToken: "e2e-993-trust-token",
		}),
	})
}

// TestRelayV2_UntrustedCwdTrustAccepted is the live accept scenario (AC-1, AC-2,
// AC-3): a conversation in an untrusted cwd forwards the trust dialog as
// modal_shown{trust} (surfaced + read off the wire), the queued turn is held, and
// answering to ACCEPT clears trust so the held turn runs (a reply is produced).
func TestRelayV2_UntrustedCwdTrustAccepted(t *testing.T) {
	const (
		turnMarker = "e2e-993-turn"
		reqTurn    = uint64(61)
		reqAnswer  = uint64(62)
	)
	h := bringUpTrustModalHarness(t)

	// 1. [Vacuous-pass positive #1 — surface] (AC-1/AC-2). Await modal_shown; assert
	//    the trust class and the fixed [proceed, exit] option IDs off the wire.
	shown := awaitTrustModalShown(t, h)

	// 2. [Vacuous-pass positive #2 — the turn is queued and HELD] (AC-1). Enqueue the
	//    untrusted turn and prove it is held (held-fence: marker absent from stdin).
	sendQueuedTurn(t, h, reqTurn, turnMarker)

	// 3. Answer to ACCEPT (proceed): ResolveAnswer routes AcceptTrust -> "1\r", which
	//    fakeclaude sees (not a bare ESC) -> clears the trust dialog -> the held turn
	//    delivers + commits on the next drain retry.
	answerModal(t, h, reqAnswer, shown.ModalID, "proceed")

	// 4. [AC-3 — the turn ran] Drain queue_state until empty. An item leaves the
	//    backlog only on a confirmed commit, and a commit happens only after
	//    DeliverPrompt wrote the prompt to the PTY (fsynced into the stdin log). So the
	//    delivered turn is on disk when the queue is empty.
	deadline := time.Now().Add(30 * time.Second)
	for {
		env, ok := h.nextEnv(deadline)
		if !ok {
			t.Fatal("did not observe the drained (empty) queue_state after accepting trust before deadline (trust never cleared / the held turn never delivered — AC-3)")
		}
		if env.Type == protocol.TypeError {
			t.Fatalf("unexpected error envelope while draining after accept: %s", string(env.Payload))
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

	// 5. The empty queue_state is a happens-after fence: both the accept keystroke and
	//    the delivered turn are on disk now. Assert the accept keystroke "1\r" routed
	//    (proves trust was granted by AcceptTrust, not auto-trusted) AND the turn
	//    marker reached claude (proves the held turn ran once trust cleared).
	logBytes, err := os.ReadFile(h.stdinLog)
	if err != nil {
		t.Fatalf("read stdin log after drain: %v", err)
	}
	if !bytes.Contains(logBytes, []byte("1\r")) {
		t.Fatalf("stdin log does not contain the trust-accept keystroke %q — trust was not granted via AcceptTrust\nstdin log: %q", "1\r", logBytes)
	}
	if !bytes.Contains(logBytes, []byte(turnMarker)) {
		t.Fatalf("stdin log does not contain the queued turn %q after accepting trust — the held turn never ran (AC-3)\nstdin log: %q", turnMarker, logBytes)
	}
}

// TestRelayV2_UntrustedCwdTrustDenied is the live deny-by-answer scenario (AC-1,
// AC-2, AC-4): same forward + held turn, but answering to DENY (exit) yields the
// typed, terminal session_error{session.blocked, "folder not trusted"} and
// genuinely blocks the turn — no reply is produced, and the deny does not enter a
// retry loop. The non-vacuity discipline: the turn is really queued and held first,
// a positive gates the negative (the deny ESC must be observed before asserting the
// turn's absence), and the code must be the TERMINAL session.blocked, not the
// transient server.binary_busy.
func TestRelayV2_UntrustedCwdTrustDenied(t *testing.T) {
	const (
		turnMarker = "e2e-993-turn"
		reqTurn    = uint64(71)
		reqAnswer  = uint64(72)
	)
	h := bringUpTrustModalHarness(t)

	// 1-2. Same forward + held turn as the accept test.
	shown := awaitTrustModalShown(t, h)
	sendQueuedTurn(t, h, reqTurn, turnMarker)

	// 3. Answer to DENY (exit): ResolveAnswer routes SendEsc -> bare ESC (fakeclaude
	//    leaves the dialog up, so the turn stays held) AND emitFolderNotTrusted ->
	//    session_error (the deterministic refusal signal at the resolver, #1014).
	answerModal(t, h, reqAnswer, shown.ModalID, "exit")

	// 4. [AC-4 — typed error] Read a session_error off the wire. The hard gate is the
	//    TERMINAL code (session.blocked, NOT the transient server.binary_busy — a
	//    regression surfacing a retry hint would fail here) + the compile-time reason.
	var serr protocol.SessionErrorPayload
	deadline := time.Now().Add(20 * time.Second)
	for {
		env, ok := h.nextEnv(deadline)
		if !ok {
			t.Fatal("did not observe a session_error after denying trust before deadline (the typed refusal was not emitted — AC-4)")
		}
		if env.Type == protocol.TypeError {
			t.Fatalf("unexpected error envelope while awaiting session_error: %s", string(env.Payload))
		}
		if env.Type != protocol.TypeSessionError {
			continue
		}
		if err := json.Unmarshal(env.Payload, &serr); err != nil {
			t.Fatalf("decode session_error payload: %v", err)
		}
		break
	}
	if serr.Code != protocol.CodeSessionBlocked {
		t.Fatalf("session_error Code = %q, want %q (the TERMINAL give-up code, not the transient %q)", serr.Code, protocol.CodeSessionBlocked, protocol.CodeServerBinaryBusy)
	}
	if serr.Message != "folder not trusted" {
		t.Fatalf("session_error Message = %q, want %q", serr.Message, "folder not trusted")
	}
	// ConversationID is a bonus (activeConversation.CurrentConversation, a relay-side
	// cursor): assert it only when populated, never hinging AC-4 on it.
	if serr.ConversationID != "" && serr.ConversationID != h.convID {
		t.Errorf("session_error ConversationID = %q, want %q (or empty)", serr.ConversationID, h.convID)
	}

	// 5. [AC-4 non-vacuity — positive gates the negative] The deny ESC must reach the
	//    stdin log (the deny genuinely routed), so "no delivery after deny" is not
	//    vacuously true because nothing happened. Poll the log until the bare ESC
	//    (0x1b) appears.
	escDeadline := time.Now().Add(10 * time.Second)
	for {
		data, err := os.ReadFile(h.stdinLog)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read stdin log while awaiting the deny ESC: %v", err)
		}
		if bytes.Contains(data, []byte{0x1b}) {
			break
		}
		if time.Now().After(escDeadline) {
			t.Fatalf("stdin log never gained the bare ESC deny keystroke (0x1b) — the deny did not route, so the no-delivery assertion would be vacuous\nstdin log: %q", data)
		}
		time.Sleep(100 * time.Millisecond)
	}

	// 6. [AC-4 negative — the denied turn is NEVER delivered, no retry loop] Give the
	//    daemon a bounded window (several 1 s msgqueue retry cycles) to (wrongly)
	//    deliver the held turn or drain the queue. During it, an empty queue_state
	//    would mean the denied turn drained — a hard failure. After it, the turn
	//    marker must still be absent from the stdin log: the deny genuinely blocked it.
	watch := time.Now().Add(5 * time.Second)
	for {
		env, ok := h.nextEnv(watch)
		if !ok {
			break // window elapsed with no drain — the expected AC-4 outcome
		}
		if env.Type != protocol.TypeQueueState {
			continue
		}
		var qs protocol.QueueStatePayload
		if err := json.Unmarshal(env.Payload, &qs); err != nil {
			t.Fatalf("decode queue_state payload: %v", err)
		}
		if len(qs.Queued) == 0 {
			t.Fatal("queue drained to empty after denying trust — the denied turn ran (AC-4 violated: deny must block the turn)")
		}
	}
	logBytes, err := os.ReadFile(h.stdinLog)
	if err != nil {
		t.Fatalf("read stdin log after the deny window: %v", err)
	}
	if bytes.Contains(logBytes, []byte(turnMarker)) {
		t.Fatalf("stdin log contains the queued turn %q after denying trust — a denied turn was delivered to claude (AC-4 violated)\nstdin log: %q", turnMarker, logBytes)
	}
}

//go:build e2e

package e2e

// This file is the LIVE remote-permission capstone for epic #597 Phase 3 (#791,
// split from #708, mirrors #642 / #792). It confirms end-to-end — over one
// spawned daemon + a real (fake) claude that raises a permission prompt — the
// remote-permission control loop the upstream tickets proved deterministically:
// a raised permission is surfaced to an interactive phone as modal_shown; a
// GATED phone (paired --allow-remote-permissions) answers with modal_answer,
// which the daemon validates (nonce + per-device gate) and routes into claude as
// the CHOSEN option's keystroke; an identical replay is a no-op (nonce
// anti-replay); an unanswered prompt is safe-denied (ESC) after the real 2-minute
// deny-on-timeout window. AC5 (flight recorder) is out of scope for the fakeclaude
// variant (same disposition as #792) and lands on the operator live run.
//
// All markers here are test-only ASCII: the chosen-keystroke oracle is "2\r" and
// the deny oracle is a bare ESC (0x1b). fakeclaude runs its stdin (the PTY slave)
// in raw mode in modal mode — as the real claude TUI does — so keystrokes reach
// the child verbatim and unbuffered: the supervisor's "2\r" answer lands as "2\r"
// (raw mode preserves CR) and the bare ESC lands as a lone 0x1b (no line
// terminator needed). The "2" digit is the load-bearing part (the CHOSEN
// allow_always option, not the always-"1" default). Do NOT paste secrets — the
// stdin log is echoed in failure messages. The file stays substrate-clean: a bare
// 0x1b is fine; a 0x1b followed by '[' is the banned CSI form and never appears
// here.

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
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// modalHarness is the shared bring-up for both #791 tests: one daemon hosting a
// modal-trigger fakeclaude, plus one GATED interactive phone with sealed-send +
// single-deadline decrypt-drain helpers. Each test raises exactly one modal via
// modalTrig and ends.
type modalHarness struct {
	phone     *fakephone.Client
	sealSend  func(env protocol.Envelope)
	nextEnv   func(deadline time.Time) (protocol.Envelope, bool)
	stdinLog  string
	modalTrig string
}

// bringUpModalHarness spawns the daemon + fakeclaude (modal-trigger mode) and
// opens the gated interactive phone's Noise session, mirroring #792's recipe:
// align the sessions dir, pre-create <initialUUID>.jsonl (the bootstrap id is
// pinned to it deterministically via seedBootstrapRegistry + --session-id, #839),
// wire the relay, pair the answering phone WITH
// --allow-remote-permissions (the #702 device gate), dial, and complete the
// interactive handshake. No bound conversation is seeded: the modal is raised on
// the bootstrap session and the modal_shown/modal_dismissed broadcast is
// operator-global (fans to every interactive conn regardless of conversation), so
// the interactive capability alone suffices to receive it.
func bringUpModalHarness(t *testing.T) *modalHarness {
	t.Helper()
	const initialUUID = "44444444-4444-4444-8444-444444444444"

	home := shortHome(t)

	// Pair the answering phone WITH --allow-remote-permissions: ResolveAnswer
	// gates on s.device.MayAnswerRemotePermission() (== AllowRemotePermissions),
	// which pairing defaults OFF. Without this flag the answer denies at the gate.
	r := RunBareIn(t, home, "pair", "-pyry-name=test", "--name=phone-a", "--allow-remote-permissions")
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
	// reconciles to initialUUID and the modal stream's Session resolves cleanly at
	// startup (the #792 / rotation-test pattern; HOME=home, -pyry-workdir=home).
	sessionsDir := claudeSessionsDir(home)
	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		t.Fatalf("mkdir sessions dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionsDir, initialUUID+".jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("pre-create initial jsonl: %v", err)
	}

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })

	// Test-owned paths under home. neverRotate is never created (no /clear
	// rotation here); modalTrig is the modal-raise signal; stdinLog is the
	// keystroke oracle (StartRotationWithRelay sets PYRY_FAKE_CLAUDE_STDIN_LOG).
	neverRotate := filepath.Join(home, "never-rotate.trig")
	modalTrig := filepath.Join(home, "modal.trig")
	stdinLog := filepath.Join(home, "stdin.log")

	// Modal-trigger fakeclaude: PYRY_FAKE_CLAUDE_MODAL_TRIGGER makes it come up
	// idle (baseline glyph) and raise a permission modal on the trigger's first
	// appearance; PYRY_MOBILE_V2=1 selects the v2 relay path. The extraEnv seam
	// forwards the modal trigger with no harness.go change (the #642 seam).
	StartRotationWithRelay(t, home, sessionsDir, initialUUID, neverRotate, stdinLog,
		fr.URL()+"/v2/server",
		"PYRY_MOBILE_V2=1",
		"PYRY_FAKE_CLAUDE_MODAL_TRIGGER="+modalTrig,
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

	// Interactive handshake: grants the `interactive` capability the modal
	// broadcast rides. The grant reused verbatim; the device gate rode the pairing.
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

	return &modalHarness{
		phone:     phone,
		sealSend:  sealSend,
		nextEnv:   nextEnv,
		stdinLog:  stdinLog,
		modalTrig: modalTrig,
	}
}

// awaitModalShown raises one modal (drops modalTrig) and drains the phone's
// inbound envelopes under one long deadline until a modal_shown arrives, returning
// its decoded payload. t.Fatal if none before ~20 s (the harness-produced-no-modal
// vacuous-pass guard, shared by both tests).
func awaitModalShown(t *testing.T, h *modalHarness) protocol.ModalShownPayload {
	t.Helper()
	if err := os.WriteFile(h.modalTrig, []byte("go\n"), 0o600); err != nil {
		t.Fatalf("write modal trigger: %v", err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		env, ok := h.nextEnv(deadline)
		if !ok {
			t.Fatal("did not observe a modal_shown before deadline (harness produced no modal — the fixture may not classify as permission, or the #798 modal stream is not wired)")
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
		return shown
	}
}

// TestRelayV2_RemotePermissionAnswered is the live answered-permission scenario:
// AC1 (surface with options), AC2 (gated answer routes the chosen keystroke), AC3
// (nonce anti-replay). The two POSITIVES gate the NEGATIVE: the modal must be
// observed surfaced, then the chosen keystroke "2\r" must be observed in the stdin
// log, BEFORE the replay negative is evaluated — each with a dedicated t.Fatal
// naming its failure mode, so a "no second dismissal" over a modal that never
// surfaced or was never answered cannot pass vacuously.
func TestRelayV2_RemotePermissionAnswered(t *testing.T) {
	h := bringUpModalHarness(t)

	// 1. [Vacuous-pass positive #1 — surface] (AC1). Await modal_shown; assert the
	//    permission class and the FIXED four option IDs (allow_once..reject_always),
	//    capture the modal_id nonce.
	shown := awaitModalShown(t, h)
	if shown.Class != "permission" {
		t.Fatalf("modal_shown Class = %q, want %q", shown.Class, "permission")
	}
	wantIDs := []string{
		string(turnevent.PermissionOptionKindAllowOnce),
		string(turnevent.PermissionOptionKindAllowAlways),
		string(turnevent.PermissionOptionKindRejectOnce),
		string(turnevent.PermissionOptionKindRejectAlways),
	}
	gotIDs := make([]string, len(shown.Options))
	for i, o := range shown.Options {
		gotIDs[i] = o.ID
	}
	if !slices.Equal(gotIDs, wantIDs) {
		t.Fatalf("modal_shown option IDs = %v, want %v (the fixed screen-independent permission set)", gotIDs, wantIDs)
	}
	modalID := shown.ModalID

	// 2. [Vacuous-pass positive #2 — answer routes the chosen keystroke] (AC2).
	//    Answer with allow_always DELIBERATELY: it is option index 2, so the routed
	//    keystroke is "2", distinct from the always-"1" default — proving the
	//    keystroke tracks the CHOSEN option, not a coincidental default.
	h.sealSend(protocol.Envelope{
		ID:   41,
		Type: protocol.TypeModalAnswer,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.ModalAnswerPayload{
			ModalID:     modalID,
			OptionID:    string(turnevent.PermissionOptionKindAllowAlways),
			AnswerToken: "e2e-791-answer-token",
		}),
	})

	dismissDeadline := time.Now().Add(20 * time.Second)
	for {
		env, ok := h.nextEnv(dismissDeadline)
		if !ok {
			t.Fatal("did not observe modal_dismissed after answering before deadline")
		}
		if env.Type == protocol.TypeError {
			t.Fatalf("unexpected error envelope while awaiting modal_dismissed: %s", string(env.Payload))
		}
		if env.Type != protocol.TypeModalDismissed {
			continue
		}
		var dis protocol.ModalDismissedPayload
		if err := json.Unmarshal(env.Payload, &dis); err != nil {
			t.Fatalf("decode modal_dismissed payload: %v", err)
		}
		if dis.ModalID != modalID {
			t.Fatalf("modal_dismissed ModalID = %q, want %q", dis.ModalID, modalID)
		}
		if dis.Outcome != string(turnevent.PermissionOptionKindAllowAlways) {
			t.Errorf("modal_dismissed Outcome = %q, want %q", dis.Outcome, "allow_always")
		}
		if dis.Source != "remote" {
			t.Errorf("modal_dismissed Source = %q, want %q", dis.Source, "remote")
		}
		break
	}

	// The modal_dismissed is a happens-after fence: the keystroke was routed before
	// it broadcast, and fakeclaude fsyncs the stdin log per write, so the answer
	// keystroke is on disk now. Require exactly one "2\r" (the chosen allow_always
	// digit + CR commit) and NO "1\r" (the always-"1" default) — proving
	// option->keystroke fidelity. t.Fatal BEFORE the replay negative (AC2; the
	// replay negative would be vacuous if the answer never routed).
	logBytes, err := os.ReadFile(h.stdinLog)
	if err != nil {
		t.Fatalf("read stdin log after answer: %v", err)
	}
	if n := bytes.Count(logBytes, []byte("2\r")); n != 1 {
		t.Fatalf("stdin log contains %d occurrences of the chosen keystroke %q after the answer, want exactly 1 (answer must route the allow_always option's keystroke — AC2; without it the replay negative is vacuous)\nstdin log: %q",
			n, "2\r", logBytes)
	}
	if bytes.Contains(logBytes, []byte("1\r")) {
		t.Fatalf("stdin log contains the default keystroke %q — the answer routed the default, not the chosen allow_always option (AC2 option->keystroke fidelity)\nstdin log: %q",
			"1\r", logBytes)
	}

	// 3. [Negative — nonce anti-replay] (AC3). Re-send the IDENTICAL modal_answer
	//    (same modal_id + option + token). It misses at Lookup (already consumed by
	//    the first answer) → no keystroke, no dismissal. Assert no second
	//    modal_dismissed within a short deadline, and still exactly one "2\r".
	h.sealSend(protocol.Envelope{
		ID:   42,
		Type: protocol.TypeModalAnswer,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.ModalAnswerPayload{
			ModalID:     modalID,
			OptionID:    string(turnevent.PermissionOptionKindAllowAlways),
			AnswerToken: "e2e-791-answer-token",
		}),
	})

	replayDeadline := time.Now().Add(3 * time.Second)
	for {
		env, ok := h.nextEnv(replayDeadline)
		if !ok {
			break // drained — no second dismissal (the expected AC3 outcome)
		}
		if env.Type == protocol.TypeModalDismissed {
			t.Fatalf("replay produced a second modal_dismissed (nonce anti-replay violated — AC3): %s", string(env.Payload))
		}
		if env.Type == protocol.TypeError {
			t.Fatalf("replay produced an error envelope: %s", string(env.Payload))
		}
	}

	logBytes2, err := os.ReadFile(h.stdinLog)
	if err != nil {
		t.Fatalf("read stdin log after replay: %v", err)
	}
	if n := bytes.Count(logBytes2, []byte("2\r")); n != 1 {
		t.Fatalf("stdin log contains %d occurrences of %q after replay, want exactly 1 (the replay must not route a second keystroke — AC3)\nstdin log: %q",
			n, "2\r", logBytes2)
	}
}

// TestRelayV2_RemotePermissionDeniedOnTimeout is the live deny-on-timeout scenario
// (AC4). It raises one modal, does NOT answer, and waits just over the real
// 2-minute modalDenyTimeout (the e2e drives the pyry binary as a subprocess, so
// the package var cannot be shrunk from the test — see spec § AC4) for a
// modal_dismissed{denied_timeout, timeout}. It then asserts the stdin log gained
// the bare ESC deny keystroke and no answer digit, and that a LATE modal_answer is
// a no-op (the modal was already consumed by the timeout Resolve). Run -count=1:
// its only latency is the deterministic timer, not a race.
func TestRelayV2_RemotePermissionDeniedOnTimeout(t *testing.T) {
	h := bringUpModalHarness(t)

	// 1. [Vacuous-pass positive] Raise one modal, capture the modal_id. Do NOT answer.
	shown := awaitModalShown(t, h)
	modalID := shown.ModalID

	// 2. [Deny-on-timeout] (AC4). Wait a little over the real 2-minute window for
	//    the fail-closed dismissal. Single long deadline, back-to-back decrypts.
	var dis protocol.ModalDismissedPayload
	timeoutDeadline := time.Now().Add(2*time.Minute + 20*time.Second)
	for {
		env, ok := h.nextEnv(timeoutDeadline)
		if !ok {
			t.Fatal("did not observe modal_dismissed{denied_timeout} within the deny-on-timeout window (~2m); the timer may be mis-wired or the modal was never armed")
		}
		if env.Type == protocol.TypeError {
			t.Fatalf("unexpected error envelope while awaiting the timeout dismissal: %s", string(env.Payload))
		}
		if env.Type != protocol.TypeModalDismissed {
			continue
		}
		if err := json.Unmarshal(env.Payload, &dis); err != nil {
			t.Fatalf("decode modal_dismissed payload: %v", err)
		}
		break
	}
	if dis.ModalID != modalID {
		t.Fatalf("modal_dismissed ModalID = %q, want %q", dis.ModalID, modalID)
	}
	if dis.Outcome != "denied_timeout" {
		t.Errorf("modal_dismissed Outcome = %q, want %q", dis.Outcome, "denied_timeout")
	}
	if dis.Source != "timeout" {
		t.Errorf("modal_dismissed Source = %q, want %q", dis.Source, "timeout")
	}

	// The dismissal is a happens-after fence: the ESC deny keystroke was routed
	// before it broadcast. The log must contain the bare ESC (0x1b) and NO answer
	// digit (no "<n>\r"), proving the fail-closed deny reached claude and no answer
	// routed.
	logBytes, err := os.ReadFile(h.stdinLog)
	if err != nil {
		t.Fatalf("read stdin log after timeout: %v", err)
	}
	if !bytes.Contains(logBytes, []byte{0x1b}) {
		t.Fatalf("stdin log does not contain the bare ESC deny keystroke (0x1b) after the deny-on-timeout\nstdin log: %q", logBytes)
	}
	assertNoAnswerDigit(t, logBytes, "after deny-on-timeout")

	// 3. [Late answer is a no-op] (AC4). Send a modal_answer AFTER the timeout
	//    dismissal: the modal was already consumed by the timeout Resolve, so it
	//    misses at Lookup → no dismissal, no keystroke.
	h.sealSend(protocol.Envelope{
		ID:   51,
		Type: protocol.TypeModalAnswer,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.ModalAnswerPayload{
			ModalID:     modalID,
			OptionID:    string(turnevent.PermissionOptionKindAllowOnce),
			AnswerToken: "e2e-791-late-answer-token",
		}),
	})

	lateDeadline := time.Now().Add(3 * time.Second)
	for {
		env, ok := h.nextEnv(lateDeadline)
		if !ok {
			break // drained — the late answer produced nothing (the expected no-op)
		}
		if env.Type == protocol.TypeModalDismissed {
			t.Fatalf("late modal_answer produced a modal_dismissed (the modal was already consumed by the timeout — AC4 no-op violated): %s", string(env.Payload))
		}
		if env.Type == protocol.TypeError {
			t.Fatalf("late modal_answer produced an error envelope: %s", string(env.Payload))
		}
	}
	logBytes2, err := os.ReadFile(h.stdinLog)
	if err != nil {
		t.Fatalf("read stdin log after late answer: %v", err)
	}
	assertNoAnswerDigit(t, logBytes2, "after the late no-op answer")
}

// assertNoAnswerDigit fails if the stdin log contains any permission answer
// keystroke "<n>\r" for n in 1..4 (the four permission options map to those
// 1-based digits; fakeclaude's raw-mode stdin preserves the CR commit). Used by
// Test B to prove the deny path and the late no-op answer routed no answer
// keystroke — only the bare ESC (0x1b) deny.
func assertNoAnswerDigit(t *testing.T, log []byte, when string) {
	t.Helper()
	for d := byte('1'); d <= '4'; d++ {
		if bytes.Contains(log, []byte{d, '\r'}) {
			t.Fatalf("stdin log contains an answer digit %q %s (no answer should have routed)\nstdin log: %q", string([]byte{d, '\r'}), when, log)
		}
	}
}

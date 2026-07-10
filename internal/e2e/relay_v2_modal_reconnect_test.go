//go:build e2e

package e2e

// This file is the cross-layer proof for the modal half of the reconnect-reconcile
// contract (ADR 025 → "Backpressure / replay"), split from #829. It exercises the
// producer wired by #877 (connect-time modal reconcile) end-to-end over one spawned
// daemon + Noise transport + relay routing + a fakeclaude-raised permission modal:
// a modal_shown raised while no client was connected is re-delivered exactly once,
// with the SAME modal_id, when a fake client (re)connects, and the re-delivered
// prompt is answerable exactly once — a second answer to the re-sent prompt is
// genuinely driven and observed rejected (the one-time nonce was consumed by the
// first answer). Because the answer semantics are a security property (a nonce that
// must never be spent twice, a deny-on-timeout window that must not be reopened by
// the re-send), a vacuous assertion here would falsely certify that reconnect cannot
// cause a double-answer — so the double-answer negative is gated behind the proven
// positives (#791's "positives gate the negative" doctrine).
//
// Reconnect is composed, not a fakephone helper: ps1.phone.Close() then a fresh
// fakephone.Dial + re-handshake with the SAME token — a brand-new conn_id + fresh
// Noise CipherStates. That is the only reconnect path fakephone/fakerelay can model
// (fresh conn_id per /v1/client upgrade, no 30s grace), which is exactly #877's
// brand-new-conn mechanism; the literal within-grace same-session mode is #874/#875's
// surviving-conn hold/flush, unreachable by this harness and owned by those tickets.
//
// Isolation that makes the whole test non-vacuous: phone #2 connects strictly AFTER
// the modal is Recorded, so broadcastInteractive (raise-time only, to conns open then)
// never targeted it — reconcile is ps2's sole possible delivery path. Phone #1's live
// modal_shown is the fence that the modal is Recorded and the source of the true
// original modal_id (the daemon logs no modal_id on the happy path).
//
// All markers here are test-only ASCII: the chosen-keystroke oracle is "2\r" (the
// allow_always option's digit, distinct from the always-"1" default), fsynced to the
// stdin log by fakeclaude before the clear that triggers the dismissal broadcast. Do
// NOT paste secrets — the stdin log is echoed in failure messages. The file stays
// substrate-clean: only the answer digit and CR, no CSI form.

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

// reconnectModalHarness holds the reconnect ingredients: a running daemon+relay with
// a paired --allow-remote-permissions phone-a token, plus the fakeclaude keystroke
// oracle (stdinLog) and the one-shot modal-raise signal (modalTrig). It deliberately
// does NOT dial a phone — the test owns connect / disconnect / reconnect via
// openInteractivePhone so both phone #1 and the reconnected phone #2 are minted here
// with the SAME token.
type reconnectModalHarness struct {
	fr        *fakerelay.Server
	serverID  string
	pubKey    []byte
	token     string // the paired phone-a token; reused verbatim on reconnect
	stdinLog  string // fakeclaude keystroke oracle
	modalTrig string // drop this file to raise the one modal
}

// phoneSession is one interactive conn bound to the daemon over a fresh Noise
// session, plus its sealed-send + single-deadline decrypt-drain closures. A reconnect
// mints a new phoneSession over a brand-new conn_id + fresh CipherStates.
type phoneSession struct {
	phone    *fakephone.Client
	sealSend func(env protocol.Envelope)
	nextEnv  func(deadline time.Time) (protocol.Envelope, bool)
}

// bringUpReconnectModalHarness spawns the daemon + modal-trigger fakeclaude and pairs
// the answering phone, mirroring bringUpModalHarness up to (and including)
// waitBinaryHello but stopping short of the dial — the frozen #791 harness stays
// untouched (the deliberate-duplication house pattern of bringUpTwoHeadModalHarness).
// It pairs phone-a WITH --allow-remote-permissions (the #702 device gate; without it
// the reconnected answer denies at the gate and the AC3 negatives go vacuous), aligns
// the sessions dir + pre-creates <initialUUID>.jsonl, and starts the fakeclaude with
// PYRY_MOBILE_V2=1 + the modal trigger.
func bringUpReconnectModalHarness(t *testing.T) *reconnectModalHarness {
	t.Helper()
	const initialUUID = "44444444-4444-4444-8444-444444444444"

	home := shortHome(t)

	// Pair WITH --allow-remote-permissions: the reconnected phone's answer must reach
	// ResolveAnswer's nonce check (and consume it), not deny at the per-device gate —
	// otherwise the double-answer negative would pass for the wrong reason.
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
	// <initialUUID>.jsonl BEFORE the daemon starts so the bootstrap session reconciles
	// to initialUUID and the modal stream's Session resolves cleanly (the #791/#792
	// rotation-test pattern; HOME=home, -pyry-workdir=home).
	sessionsDir := filepath.Join(home, ".claude", "projects", encodeWorkdir(home))
	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		t.Fatalf("mkdir sessions dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionsDir, initialUUID+".jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("pre-create initial jsonl: %v", err)
	}

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })

	neverRotate := filepath.Join(home, "never-rotate.trig")
	modalTrig := filepath.Join(home, "modal.trig")
	stdinLog := filepath.Join(home, "stdin.log")

	// Modal-trigger fakeclaude: comes up idle and raises a permission modal on the
	// trigger's first appearance; PYRY_MOBILE_V2=1 selects the v2 relay path. The
	// extraEnv seam forwards the modal trigger with no harness.go change (the #642 seam).
	StartRotationWithRelay(t, home, sessionsDir, initialUUID, neverRotate, stdinLog,
		fr.URL()+"/v2/server",
		"PYRY_MOBILE_V2=1",
		"PYRY_FAKE_CLAUDE_MODAL_TRIGGER="+modalTrig,
	)

	serverID := readPersistedServerID(t, home)
	waitBinaryHello(t, fr, serverID)

	return &reconnectModalHarness{
		fr:        fr,
		serverID:  serverID,
		pubKey:    pubKey,
		token:     payload.Token,
		stdinLog:  stdinLog,
		modalTrig: modalTrig,
	}
}

// openInteractivePhone dials a fresh phone, drives the interactive Noise handshake
// with h.token, and returns the bound session. Reconnect is:
//
//	old.phone.Close(); ps := openInteractivePhone(t, h, "phone-a")
//
// with the SAME token — a brand-new conn_id + fresh CipherStates, which is exactly the
// fresh-attach path #877's reconcileModals fires on. The sealSend/nextEnv bodies are
// copied verbatim from bringUpModalHarness (frozen and correct): every noise_msg is
// decrypted in capture order so the receive nonce stays in sequence, and every wait is
// a single long deadline with back-to-back reads — never a short poll (a timed-out
// fakephone Receive closes the WS and makes the conn unusable).
func openInteractivePhone(t *testing.T, h *reconnectModalHarness, deviceName string) *phoneSession {
	t.Helper()

	dialCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	phone, err := fakephone.Dial(dialCtx, h.fr.URL(), h.serverID, h.token, deviceName)
	if err != nil {
		t.Fatalf("phone dial: %v", err)
	}
	t.Cleanup(func() { _ = phone.Close() })

	sendCS, recvCS := driveHandshakeToOpenDaemonInteractive(t, phone, h.pubKey, h.token)

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

	return &phoneSession{phone: phone, sealSend: sealSend, nextEnv: nextEnv}
}

// assertModalShownFor drains ps under one 20 s deadline until a modal_shown arrives,
// then asserts it is the outstanding permission modal: non-empty modal_id (equal to
// wantID when wantID != ""), Class == "permission", and the fixed four permission
// option IDs. It returns the decoded payload. t.Fatal on an intervening TypeError or
// on the deadline (the harness-produced-no-modal / reconcile-delivered-nothing
// vacuous-pass guard). Used to capture the original id (phone #1's live raise) and to
// assert the re-delivered id (phone #2's reconcile).
func assertModalShownFor(t *testing.T, ps *phoneSession, wantID string) protocol.ModalShownPayload {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		env, ok := ps.nextEnv(deadline)
		if !ok {
			t.Fatal("did not observe a modal_shown before deadline (phone #1: the fixture may not classify as permission; phone #2: reconcileModals delivered nothing on the fresh interactive handshake)")
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
		if wantID != "" && shown.ModalID != wantID {
			t.Fatalf("re-delivered modal_shown ModalID = %q, want %q (a re-mint, not a re-send — the reconciled prompt must carry the ORIGINAL nonce, AC2)", shown.ModalID, wantID)
		}
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
		return shown
	}
}

// TestRelayV2_ModalReconcileOnReconnect proves the modal reconnect-reconcile contract
// end-to-end: a permission modal raised while a client was disconnected is re-delivered
// exactly once, with the same modal_id, on reconnect (AC1/AC2), and is answerable
// exactly once — a second answer to the re-sent prompt is rejected (AC3). AC4 is the
// whole file (daemon-layer e2e over the fake client, //go:build e2e).
//
// Assertion order is load-bearing: each step is a hard precondition of the next, so the
// double-answer security negative in step 6 can never pass vacuously.
func TestRelayV2_ModalReconcileOnReconnect(t *testing.T) {
	h := bringUpReconnectModalHarness(t)

	// 1. Establish a prior session (phone #1) and raise the modal. ps1's live
	//    modal_shown (via raise-time broadcastInteractive) is the fence that the modal
	//    is Recorded + armed + outstanding, and the source of the TRUE original
	//    modal_id X (the daemon logs no modal_id on the happy path).
	ps1 := openInteractivePhone(t, h, "phone-a")
	if err := os.WriteFile(h.modalTrig, []byte("go\n"), 0o600); err != nil {
		t.Fatalf("write modal trigger: %v", err)
	}
	original := assertModalShownFor(t, ps1, "")
	modalID := original.ModalID

	// 2. Disconnect — no client connected. The modal stays outstanding: a phone
	//    disconnect resolves nothing (only a local/remote answer, a remote cancel, or
	//    the deny-timeout resolve it). The reconnect below is sub-second, so we are
	//    well inside the 2-minute deny window and Snapshot still holds X.
	if err := ps1.phone.Close(); err != nil {
		t.Fatalf("close phone #1: %v", err)
	}

	// 3. Reconnect (fresh attach — the mode under test). Same token → same device
	//    identity → brand-new conn_id + fresh Noise session → handleNoiseInit success
	//    tail → reconcileModals. ps2 connects strictly AFTER the raise, so no raise-time
	//    broadcast can have reached it: reconcile is its sole possible delivery path.
	ps2 := openInteractivePhone(t, h, "phone-a")

	// 4. Re-delivered with the same nonce (AC1 + AC2). The reconciled modal_shown must
	//    carry modal_id == X with the full permission payload. Exactly-once is proven
	//    without a mid-stream timed-out drain (a timed-out fakephone Receive closes the
	//    WS and would kill the conn steps 5-6 still need): reconcile enqueues its whole
	//    batch before this first read on an ordered transport, so any duplicate delivery
	//    (a double-send, or an impossible raise-time broadcast to ps2) would already sit
	//    in the queue behind this one and surface in the step-5 / step-6 drains below —
	//    each of which t.Fatals on a second TypeModalShown.
	assertModalShownFor(t, ps2, modalID)

	// 5. Answerable exactly once — the answer routes (AC3, the positive that gates the
	//    negative). Answer allow_always DELIBERATELY: its keystroke "2" is distinct from
	//    the always-"1" default, so the log oracle proves the CHOSEN option routed.
	ps2.sealSend(protocol.Envelope{
		ID:   41,
		Type: protocol.TypeModalAnswer,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.ModalAnswerPayload{
			ModalID:     modalID,
			OptionID:    string(turnevent.PermissionOptionKindAllowAlways),
			AnswerToken: "e2e-903-answer-token",
		}),
	})

	dismissDeadline := time.Now().Add(20 * time.Second)
	for {
		env, ok := ps2.nextEnv(dismissDeadline)
		if !ok {
			t.Fatal("did not observe modal_dismissed after answering the re-delivered prompt before deadline (the reconciled prompt was not answerable — the re-send did not preserve the nonce, or the answer did not route)")
		}
		if env.Type == protocol.TypeError {
			t.Fatalf("unexpected error envelope while awaiting modal_dismissed: %s", string(env.Payload))
		}
		if env.Type == protocol.TypeModalShown {
			t.Fatalf("a SECOND modal_shown arrived after the re-delivery (exactly-once violated — reconcile must unicast once and no raise-time broadcast can reach ps2): %s", string(env.Payload))
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

	// modal_dismissed is a happens-after fence: fakeclaude fsyncs the answer keystroke
	// before the clear that triggers the broadcast, so it is on disk now. Require
	// exactly one "2\r" (the chosen allow_always digit + CR) and NO "1\r" (the default)
	// — proving the reconnected conn's answer actually reached claude. Snapshot the log
	// for the step-6 unchanged check. t.Fatal HERE, before the double-answer negative,
	// so that negative can never pass vacuously.
	logAfterAnswer, err := os.ReadFile(h.stdinLog)
	if err != nil {
		t.Fatalf("read stdin log after the answer: %v", err)
	}
	if n := bytes.Count(logAfterAnswer, []byte("2\r")); n != 1 {
		t.Fatalf("stdin log contains %d occurrences of the chosen keystroke %q after the answer, want exactly 1 (the reconciled prompt must be answerable and route the allow_always keystroke — AC3; without it the double-answer negative is vacuous)\nstdin log: %q",
			n, "2\r", logAfterAnswer)
	}
	if bytes.Contains(logAfterAnswer, []byte("1\r")) {
		t.Fatalf("stdin log contains the default keystroke %q — the answer routed the default, not the chosen allow_always option (AC3 option->keystroke fidelity)\nstdin log: %q",
			"1\r", logAfterAnswer)
	}

	// 6. Double-answer rejected (AC3, the security negative). Send a SECOND
	//    modal_answer for X (the replay/reorder threat the ADR-025 nonce guards). The
	//    modal_id was consumed by the first answer's Registry.Resolve, so it misses at
	//    Lookup → no keystroke, no second dismissal; the re-send opened no second answer
	//    window. Assert no second modal_dismissed, no TypeError, and the stdin log
	//    byte-for-byte unchanged.
	ps2.sealSend(protocol.Envelope{
		ID:   42,
		Type: protocol.TypeModalAnswer,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.ModalAnswerPayload{
			ModalID:     modalID,
			OptionID:    string(turnevent.PermissionOptionKindAllowAlways),
			AnswerToken: "e2e-903-answer-token",
		}),
	})

	replayDeadline := time.Now().Add(3 * time.Second)
	for {
		env, ok := ps2.nextEnv(replayDeadline)
		if !ok {
			break // drained — no second dismissal (the expected AC3 outcome)
		}
		if env.Type == protocol.TypeModalDismissed {
			t.Fatalf("the second answer to the re-sent prompt produced a second modal_dismissed (reconnect re-opened answerability — the one-time nonce was spent twice, AC3): %s", string(env.Payload))
		}
		if env.Type == protocol.TypeModalShown {
			t.Fatalf("a SECOND modal_shown arrived (exactly-once violated — reconcile must re-deliver the prompt only once): %s", string(env.Payload))
		}
		if env.Type == protocol.TypeError {
			t.Fatalf("the second answer produced an error envelope: %s", string(env.Payload))
		}
	}

	logAfterReplay, err := os.ReadFile(h.stdinLog)
	if err != nil {
		t.Fatalf("read stdin log after the second answer: %v", err)
	}
	if !bytes.Equal(logAfterReplay, logAfterAnswer) {
		t.Fatalf("stdin log changed after the second answer (the re-sent prompt routed a keystroke twice — AC3 answerable-exactly-once violated)\nbefore: %q\nafter:  %q", logAfterAnswer, logAfterReplay)
	}
}

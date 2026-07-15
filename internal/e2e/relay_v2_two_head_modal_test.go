//go:build e2e

package e2e

// This file is the LIVE two-head first-answer-wins capstone for epic #597 Phase 3
// (#793, split from #708, mirrors #642 / #791 / #792). It confirms end-to-end — over
// one spawned daemon hosting a real (fake) claude that raises a permission prompt,
// with BOTH a paired phone and a local `pyry attach` head observing it — the
// two-head modal-ownership control loop the upstream tickets proved deterministically
// (#706 first-answer-wins, local attach dismisses; #798 live-wired the producer):
// the local TTY answers first, claude's modal vanishes, tui-driver fires
// EventKindPtyModalHidden, the daemon's #706 local arm Resolve()s the shared registry
// and broadcasts modal_dismissed{source: local}, the phone's modal_shown clears, and
// the phone's subsequent (late) modal_answer misses the consumed nonce and is a
// no-op. It CONFIRMS live; it does not re-prove — the correctness oracle stays
// upstream (#706). Test/harness only: the two-heads production code already shipped.
//
// AC4 (flight recorder) is deferred to the operator live-stack run (same disposition
// as #791/#792); what this capstone asserts live for AC4's intent is the local arm's
// internal/audit record (source=local, outcome=dismissed_local) landing on the
// daemon's slog — a stronger, security-relevant confirmation for this ticket.
//
// All markers here are test-only ASCII: the winning-keystroke oracle is the single
// TTY digit "1" (allow_once) and the observed anchor is the "Do you want to proceed?"
// prompt (not a guarded substrate token — see fakeclaude's modalScreen doc). No CSI
// form (a 0x1b followed by '[') and no TUI glyph appears here, so the file stays
// substrate-clean. Do NOT paste secrets — the stdin log is echoed in failure messages.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// modalAnchor is the permission-prompt phrase fakeclaude's modalScreen carries. The
// local attach head's own screen must render it (step 2) so the loser-no-op below is
// not vacuous — a "late answer ignored" over a prompt the local head never saw would
// pass without meaning anything.
const modalAnchor = "Do you want to proceed?"

// localAnswerKey is the byte the operator types at the local attach TTY to answer the
// permission modal (option 1, allow_once). fakeclaude clears the modal on the first
// post-modal stdin byte regardless of value, so the exact digit is load-bearing only
// as the "winning keystroke delivered exactly once" stdin-log oracle.
const localAnswerKey = "1"

// twoHeadModalHarness is the bring-up for the #793 capstone: the #791 relay daemon +
// gated interactive phone (built WITH clear-on-answer so the local answer clears the
// modal), PLUS a local `pyry attach` head bound to the same daemon's bootstrap session
// BEFORE the modal is raised. It embeds *modalHarness so the frozen freestanding
// helpers (awaitModalShown, sealSend/nextEnv) apply to the phone side unchanged.
type twoHeadModalHarness struct {
	*modalHarness          // phone side: phone, sealSend, nextEnv, stdinLog, modalTrig
	attachMaster  *os.File // the local pyry attach head's PTY master (test writes/reads)
	h             *Harness // the relay daemon: SocketPath (attach target) + Stderr (audit oracle)
}

// bringUpTwoHeadModalHarness spawns the relay daemon + gated interactive phone exactly
// as #791's bringUpModalHarness does, but (a) adds PYRY_FAKE_CLAUDE_MODAL_CLEAR_ON_ANSWER
// so the local answer keystroke clears the modal (the #706 local arm needs a real
// EventKindPtyModalHidden), (b) spawns the local `pyry attach` head and (c) returns the
// *Harness for SocketPath/Stderr. It reuses the frozen freestanding #791 helpers
// verbatim and does NOT mutate bringUpModalHarness/modalHarness (frozen for #791); the
// daemon+phone setup is duplicated here deliberately rather than refactored, to keep
// #791 byte-frozen.
func bringUpTwoHeadModalHarness(t *testing.T) *twoHeadModalHarness {
	t.Helper()
	const initialUUID = "44444444-4444-4444-8444-444444444444"

	home := shortHome(t)

	// Pair the answering phone WITH --allow-remote-permissions (the #702 device gate):
	// its late answer in step 6 must reach ResolveAnswer's Lookup (and miss on the
	// consumed nonce), not deny at the gate — otherwise the loser-no-op would be
	// vacuous for the wrong reason.
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
	sessionsDir := claudeSessionsDir(home)
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

	// Modal-trigger fakeclaude WITH clear-on-answer: raises a permission modal on the
	// trigger's first appearance (as #791) and, additionally, clears it on the first
	// post-modal stdin byte (the local answer keystroke) so tui-driver fires
	// EventKindPtyModalHidden and the daemon's #706 local arm broadcasts
	// modal_dismissed{local}. The extraEnv seam forwards both with no harness.go change.
	h := StartRotationWithRelay(t, home, sessionsDir, initialUUID, neverRotate, stdinLog,
		fr.URL()+"/v2/server",
		"PYRY_MOBILE_V2=1",
		"PYRY_FAKE_CLAUDE_MODAL_TRIGGER="+modalTrig,
		"PYRY_FAKE_CLAUDE_MODAL_CLEAR_ON_ANSWER=1",
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
	// inner frames, in capture order so the receive nonce stays in sequence (603.md/634.md).
	// Callers pass a single long deadline with back-to-back reads. ok=false on deadline.
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

	modal := &modalHarness{
		phone:     phone,
		sealSend:  sealSend,
		nextEnv:   nextEnv,
		stdinLog:  stdinLog,
		modalTrig: modalTrig,
	}

	// Spawn the local attach head and confirm its output head is bound BEFORE any
	// modal is raised — the Bridge does not replay scrollback, so an attach bound
	// after the modal write would miss it (the #706 two-heads coexistence model).
	master := spawnLocalAttachHead(t, h)

	return &twoHeadModalHarness{modalHarness: modal, attachMaster: master, h: h}
}

// spawnLocalAttachHead spawns `pyry attach` against the relay daemon's own control
// socket (empty session id = the bootstrap session the modal is raised on) with a
// test-owned pty on its stdio, and returns the pty Master. The daemon is in bridge
// mode (stdin=/dev/null), so this local `output` head coexists with the phone observer
// — the ADR-025 two-heads model. Input is raw passthrough (Session.AttachInput, not
// DeliverPrompt), so a byte written to Master reaches fakeclaude's stdin verbatim and
// is logged; output is fanned by the Bridge, so the modal renders on Master. It skips
// (t.Skipf) when pty.Open is unavailable (sandboxed CI), and fences on the daemon's
// "control: client attached" log so the caller may raise the modal knowing the output
// head is bound.
func spawnLocalAttachHead(t *testing.T, h *Harness) *os.File {
	t.Helper()

	master, slave, err := pty.Open()
	if err != nil {
		t.Skipf("e2e: pty.Open unavailable: %v", err)
	}

	bin := ensurePyryBuilt(t)
	attachCmd := exec.Command(bin, "attach", "-pyry-socket="+h.SocketPath)
	attachCmd.Stdin = slave
	attachCmd.Stdout = slave
	attachCmd.Stderr = slave
	// Make the slave the controlling terminal so the attach client's IsTerminal(stdin)
	// is true and it enters raw mode (keystrokes pass through unmodified). Mirrors
	// StartAttach.
	attachCmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	attachCmd.Env = childEnv(h.HomeDir)

	if err := attachCmd.Start(); err != nil {
		t.Fatalf("e2e: pyry attach start: %v", err)
	}
	attachDone := make(chan struct{})
	go func() {
		_ = attachCmd.Wait()
		close(attachDone)
	}()
	t.Cleanup(func() {
		_ = master.Close()
		_ = slave.Close()
		killSpawned(t, attachCmd, attachDone)
	})

	// Surface a handshake death early instead of waiting out a later read deadline.
	select {
	case <-attachDone:
		exit := -1
		if attachCmd.ProcessState != nil {
			exit = attachCmd.ProcessState.ExitCode()
		}
		t.Fatalf("e2e: pyry attach exited before it bound (exit=%d)\ndaemon stderr:\n%s",
			exit, h.Stderr.String())
	case <-time.After(500 * time.Millisecond):
	}

	// The daemon logs "control: client attached" strictly AFTER Bridge.Attach sets the
	// output writer (control/server.go), so this is a deterministic "output head bound"
	// fence — once seen, a later PTY write (the modal screen) fans to this attach.
	awaitStderrContains(t, h, "control: client attached", 5*time.Second)

	return master
}

// TestRelayV2_TwoHeadFirstAnswerWins is the live two-head first-answer-wins scenario:
// AC1 (both a phone and a local attach observe the same permission prompt), AC2
// (the local answer wins; its keystroke routes exactly once and claude proceeds), AC3
// (the phone's modal clears and its late answer is a rejected no-op), and AC4's live
// audit-trail evidence (the local arm's dismissed_local record on the daemon slog).
//
// Assertion order is load-bearing: the two observe-positives (phone modal_shown +
// attach anchor) and the winner dismissal are hard preconditions of the loser-no-op
// negative — each with a dedicated t.Fatal naming its failure mode — so a "late answer
// ignored" over a prompt a surface never saw, or over a modal never resolved locally,
// cannot pass vacuously.
func TestRelayV2_TwoHeadFirstAnswerWins(t *testing.T) {
	th := bringUpTwoHeadModalHarness(t)

	// 1. [Vacuous-pass positive #1 — phone observes] (AC1 phone side). Raise the modal
	//    and drain the phone until modal_shown; assert the permission class and the
	//    fixed four option IDs; capture the modal_id nonce.
	shown := awaitModalShown(t, th.modalHarness)
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

	// 2. [Vacuous-pass positive #2 — local attach observes] (AC1 attach side). The same
	//    permission prompt must render on the local attach head's own screen. Without
	//    this the loser-no-op below is vacuous ("dismissed a prompt the head never
	//    saw"). The attach bound before the modal was raised, so the Bridge fanned
	//    modalScreen to it.
	if err := readUntilContains(th.attachMaster, []byte(modalAnchor), 20*time.Second); err != nil {
		t.Fatalf("local attach head never rendered the permission prompt anchor %q (the attach bound after the modal, or the Bridge did not fan output to the local head): %v", modalAnchor, err)
	}

	// Keep the attach output side drained for the rest of the test so claude's PTY
	// output (the clear screen, then quiet) never backs up against an unread master and
	// stalls the supervisor's PTY-drain. Exits on teardown's master.Close().
	go func() {
		buf := make([]byte, 4096)
		for {
			if _, err := th.attachMaster.Read(buf); err != nil {
				return
			}
		}
	}()

	// 3. [Winner — local answers first] (AC2). Type the answer keystroke at the local
	//    TTY. fakeclaude clears the modal on the first post-modal stdin byte →
	//    EventKindPtyModalHidden → the #706 local arm Resolve()s and broadcasts. Await
	//    modal_dismissed on the phone; assert it is the LOCAL dismissal of THIS modal —
	//    the phone learning its modal_shown is gone (AC3 "the phone modal clears").
	if _, err := th.attachMaster.Write([]byte(localAnswerKey)); err != nil {
		t.Fatalf("write local answer keystroke to attach master: %v", err)
	}

	dismissDeadline := time.Now().Add(20 * time.Second)
	for {
		env, ok := th.nextEnv(dismissDeadline)
		if !ok {
			t.Fatal("did not observe modal_dismissed after the local answer before deadline (the local answer did not clear the modal, or the #706 local arm did not resolve/broadcast)")
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
		if dis.Source != "local" {
			t.Errorf("modal_dismissed Source = %q, want %q (the resolution came from the local attach TTY)", dis.Source, "local")
		}
		if dis.Outcome != "dismissed_local" {
			t.Errorf("modal_dismissed Outcome = %q, want %q", dis.Outcome, "dismissed_local")
		}
		break
	}

	// 4. [Winning keystroke delivered exactly once] (AC2). modal_dismissed is a
	//    happens-after fence (fakeclaude fsyncs the stdin write before the clear that
	//    triggers the broadcast), so the answer keystroke is on disk now. Require
	//    exactly one occurrence of the local keystroke — the winner routed its answer
	//    once. Snapshot the log for the step-6 unchanged check.
	logAfterWin, err := os.ReadFile(th.stdinLog)
	if err != nil {
		t.Fatalf("read stdin log after the local answer: %v", err)
	}
	if n := bytes.Count(logAfterWin, []byte(localAnswerKey)); n != 1 {
		t.Fatalf("stdin log contains %d occurrences of the local answer keystroke %q, want exactly 1 (the winning keystroke must be delivered exactly once — AC2)\nstdin log: %q",
			n, localAnswerKey, logAfterWin)
	}

	// 5. [Live audit-trail evidence] (AC4, in lieu of the deferred flight recorder). The
	//    #706 local arm audits the resolution BEFORE it broadcasts, so the
	//    dismissed_local audit line is already on the daemon's slog. dismissed_local is a
	//    unique, format-agnostic marker: its presence proves the local resolution was
	//    recorded to the audit trail live.
	awaitStderrContains(t, th.h, "dismissed_local", 5*time.Second)

	// 6. [Loser — phone's late answer is a no-op] (AC3). Send a modal_answer from the
	//    phone AFTER the local dismissal. The modal_id was already consumed by the local
	//    Resolve, so it misses at Lookup → no keystroke routed, no second dismissal.
	//    Assert no further modal_dismissed within a short deadline AND the stdin log is
	//    byte-for-byte unchanged (the loser routed nothing).
	th.sealSend(protocol.Envelope{
		ID:   61,
		Type: protocol.TypeModalAnswer,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.ModalAnswerPayload{
			ModalID:     modalID,
			OptionID:    string(turnevent.PermissionOptionKindAllowOnce),
			AnswerToken: "e2e-793-late-answer-token",
		}),
	})

	lateDeadline := time.Now().Add(3 * time.Second)
	for {
		env, ok := th.nextEnv(lateDeadline)
		if !ok {
			break // drained — the late answer produced nothing (the expected no-op)
		}
		if env.Type == protocol.TypeModalDismissed {
			t.Fatalf("the phone's late modal_answer produced a second modal_dismissed (first-answer-wins violated — the modal was already resolved locally, AC3): %s", string(env.Payload))
		}
		if env.Type == protocol.TypeError {
			t.Fatalf("the phone's late modal_answer produced an error envelope: %s", string(env.Payload))
		}
	}

	logAfterLate, err := os.ReadFile(th.stdinLog)
	if err != nil {
		t.Fatalf("read stdin log after the late answer: %v", err)
	}
	if !bytes.Equal(logAfterLate, logAfterWin) {
		t.Fatalf("stdin log changed after the phone's late answer (the loser routed a keystroke — AC3 no-op violated)\nbefore: %q\nafter:  %q", logAfterWin, logAfterLate)
	}
}

// awaitStderrContains polls the daemon's captured stderr until it contains substr or
// the deadline elapses, t.Fatal on timeout. Used as a happens-after fence on the
// daemon's structured logs — the attach-bound confirmation and the dismissed_local
// audit line — since the os/exec pipe copy into h.Stderr is asynchronous.
func awaitStderrContains(t *testing.T, h *Harness, substr string, total time.Duration) {
	t.Helper()
	deadline := time.Now().Add(total)
	for time.Now().Before(deadline) {
		if strings.Contains(h.Stderr.String(), substr) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("daemon stderr did not contain %q within %s\nstderr:\n%s", substr, total, h.Stderr.String())
}

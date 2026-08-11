//go:build e2e_realclaude

package realclaude

// TestInteractiveModalResolution is the #1030 deliverable (split from #963): a
// real-claude liveness gate for remotely ANSWERING and CANCELLING a permission
// prompt that real `claude --model haiku` actually raises. Before this, no
// modal-resolution verb had ever executed against real claude — the interactive
// daemon path had only bootstrap (#854), per-conversation (#997), and
// conversation-lifecycle (#1028) real-claude gates. The remote-approval flow
// (answer/cancel a permission modal from a phone) is the entire point of driving a
// session remotely, so per the 2026-07-08 "real-claude e2e in a pre-ship gate,
// always" policy it needs a real-tier gate even though the fake tier already owns
// its deterministic shape and security properties (modal_answer #791, modal_cancel
// #1003, trust-class #993).
//
// The fake tier drives a fakeclaude that simulates a modal via a test env-var
// trigger. Here the trigger is real: claude, spawned WITHOUT
// --dangerously-skip-permissions, chooses to call the gated Bash tool under
// default permission mode, blocks on a real TUI permission modal, tui-driver
// classifies it as a permission modal, and the daemon's #798 surfacer broadcasts
// modal_shown to interactive conns with ZERO production change (relay.go wires
// startInteractiveModalStreamV2 unconditionally under bridge != nil &&
// claudeSessionsDir != "", which the daemon satisfies; runSupervisor trust-marks
// -pyry-workdir at startup, so the startup trust modal never fires — only the
// per-tool permission modal does).
//
// One daemon, one bound conversation, two sequential modal cycles over one
// encrypted channel (the #1028 shape — request/reply strictly alternate, so the
// Noise receive nonce stays in lockstep; every drain decrypts every noise_msg in
// arrival order to preserve that invariant):
//
//   - Phase A / answer (AC #1): raise a real permission modal → assert
//     modal_shown{permission} BEFORE resolving → modal_answer{allow_once} → the
//     session observably proceeds (a subsequent non-empty assistant_delta). claude's
//     words are never asserted (substrate-guard safe).
//   - Phase B / cancel (AC #2): raise a second modal (distinct nonce) → assert
//     modal_shown{permission} → modal_cancel → observe the
//     modal_dismissed{cancelled,remote} broadcast. modal_cancel is
//     fire-and-broadcast: there is NO reply correlated to the cancel, so the proof
//     is the broadcast dismissal, never a correlated cancel-reply.
//
// One daemon + one session halves the expensive cold-claude spawn versus two
// subtests, and the shared trigger scaffold (raiseRealPermissionModal) means both
// branches cost only a second modal cycle to cover.
//
// It reuses the #854/#997/#1028 harness in this package (bootstrapDaemon plumbing,
// driveHandshakeInteractive, sealSendMessage, sealEnvelope, drainForAssistantReply,
// and the pair/seed/relay helpers) and adds a non-skip-permissions spawn variant
// (spawnPermissionDaemon), a Type-only broadcast drain (drainForControlEvent — the
// modal_shown/modal_dismissed broadcasts are EventID==nil, uncorrelated), and the
// shared permission-trigger scaffold. The --dangerously-skip-permissions flag the
// three shipped gates hardcode (`spawnBootstrapDaemon`) is the
// one line spawnPermissionDaemon drops — that flag suppresses permission modals
// entirely, so this gate needs a daemon without it.
//
// Non-vacuity is load-bearing: each phase asserts modal_shown{permission} BEFORE
// it resolves, so neither the answer's continuation nor the cancel's dismissal can
// pass over a modal that never surfaced. Like #854/#997/#1028 this is a standing
// real-claude liveness gate in preship, not a deterministic RED/GREEN oracle — the
// fake tier owns the deterministic shape and keystroke-fidelity checks. Placement
// under the e2e_realclaude build tag wires it into `make e2e-realclaude` (and thus
// `make preship`) with no Makefile change; startModalResolutionHarness skips
// cleanly when claude/creds are absent.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// Seeded state for this gate. Distinct from the #854 (liveBootstrapUUID /
// liveConvID) and #997 (livePerConvBootstrapUUID) fixtures so the four files never
// share a fixed identifier in the same package. Only the bootstrap POOL id is
// seeded (loaded once at startup); real claude still mints its own on-disk uuid.
const (
	liveModalBootstrapUUID = "99999999-9999-4999-8999-999999999999"
	liveModalConvID        = "66666666-6666-4666-8666-666666666666"
)

// modalSurfaceBudget is the budget for a real permission modal to surface after a
// Bash-trigger send: cold claude (spawn + model load) reaching the gated tool call
// and raising the TUI permission modal, which tui-driver classifies and the #798
// surfacer broadcasts as modal_shown. Generous like the cold first-turn budget —
// Phase B's send may also queue behind Phase A's turn tail before it delivers.
const modalSurfaceBudget = 120 * time.Second

// modalDismissBudget is the budget for the modal_dismissed{cancelled,remote}
// broadcast after modal_cancel. The modal is already up (asserted just prior), so
// ResolveCancel → SendEsc → broadcastModalDismissed is fast.
const modalDismissBudget = 30 * time.Second

func TestInteractiveModalResolution(t *testing.T) {
	skipUnlessPTYGate(t)
	h, convID := startModalResolutionHarness(t)
	// A per-run nonce keeps each phase's trigger command distinct (defeats any
	// accidental caching) without asserting on its echo. Phase B uses nonce+1 so
	// its prompt differs from Phase A's.
	nonce := time.Now().UnixNano()

	// --- Phase A: answer (AC #1) -------------------------------------------------
	// Raise a real permission modal (non-vacuity gate: modal_shown{permission} is
	// asserted inside the helper BEFORE the answer), answer allow_once, and prove
	// the session proceeded via a subsequent non-empty assistant_delta. Answering
	// requires the phone be paired --allow-remote-permissions (the device gate);
	// startModalResolutionHarness pairs with it.
	modalID := raiseRealPermissionModal(t, h, 2, convID, bashEchoTrigger(nonce))
	sealEnvelope(t, h.phone, h.initSend, protocol.Envelope{
		ID:   3,
		Type: protocol.TypeModalAnswer,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.ModalAnswerPayload{
			ModalID:  modalID,
			OptionID: string(turnevent.PermissionOptionKindAllowOnce),
			// A client-minted idempotency key, NOT authorization — an arbitrary
			// constant is fine (authorization is ModalID validity + the device gate).
			AnswerToken: "e2e-1030-answer-token",
		}),
	})
	drainForAssistantReply(t, h.phone, h.initRecv, convID, 1, perTurnReplyBudget)

	// --- Phase B: cancel (AC #2) -------------------------------------------------
	// Raise a second permission modal (distinct nonce), cancel it, and observe the
	// modal_dismissed{cancelled,remote} broadcast — the state/broadcast-shaped
	// observable the ticket mandates. modal_cancel is fire-and-broadcast: no reply
	// is correlated to the request, so the request ID is cosmetic and the drain
	// matches on the broadcast Type, never on InReplyTo.
	modalID2 := raiseRealPermissionModal(t, h, 4, convID, bashEchoTrigger(nonce+1))
	sealEnvelope(t, h.phone, h.initSend, protocol.Envelope{
		ID:      5,
		Type:    protocol.TypeModalCancel,
		TS:      time.Now().UTC(),
		Payload: mustJSON(t, protocol.ModalCancelPayload{ModalID: modalID2}),
	})
	dismissed := drainForControlEvent(t, h.phone, h.initRecv, protocol.TypeModalDismissed, modalDismissBudget)
	var dis protocol.ModalDismissedPayload
	if err := json.Unmarshal(dismissed.Payload, &dis); err != nil {
		t.Fatalf("decode modal_dismissed payload: %v", err)
	}
	if dis.ModalID != modalID2 {
		t.Errorf("modal_dismissed ModalID = %q, want %q", dis.ModalID, modalID2)
	}
	if dis.Outcome != "cancelled" {
		t.Errorf("modal_dismissed Outcome = %q, want %q", dis.Outcome, "cancelled")
	}
	if dis.Source != "remote" {
		t.Errorf("modal_dismissed Source = %q, want %q", dis.Source, "remote")
	}
}

// bashEchoTrigger is the PTY-runner trigger: an echo Bash command. On the PTY
// runner a real terminal prompts for every tool use, so even a trivial echo
// surfaces a permission dialog tui-driver detects. It does NOT gate on the
// stream-json runner (claude auto-approves a bare echo), so stream specs use
// writeFileTrigger instead. The <nonce> keeps each phase's command distinct.
func bashEchoTrigger(nonce int64) string {
	return fmt.Sprintf("Use the Bash tool to run the command: echo pyrycode-%d. After it completes, reply with a single short word.", nonce)
}

// writeFileTrigger is the stream-runner trigger: a Write of a fresh file. The
// stream-json runner consults the --permission-prompt-tool only for actions that
// genuinely need approval, and a bare echo is auto-approved — so the old echo
// trigger never raised a modal on the stream path and
// TestInteractiveStreamModalResolution timed out (#1170). A Write of a new file is
// gated there. It is NOT used on the PTY runner: a Write "create file?" dialog
// does not surface on the PTY live buffer the way echo does, so PTY specs keep
// bashEchoTrigger. The <nonce> keeps each file distinct.
func writeFileTrigger(nonce int64) string {
	return fmt.Sprintf("Use the Write tool to create a file named pyrycode-%d.txt containing the single word hello. After it completes, reply with a single short word.", nonce)
}

// raiseRealPermissionModal sends triggerPrompt (forcing real claude to call a
// permission-gated tool in default permission mode — no --dangerously-skip-permissions),
// drains the wire to the resulting modal_shown broadcast, asserts it is a
// permission modal with a non-empty modal_id, and returns that modal_id. This is
// the shared trigger scaffold both phases call; the caller supplies the trigger
// (bashEchoTrigger for PTY specs, writeFileTrigger for stream specs) because the
// two runners gate different actions. Every trigger ends with "reply with a single
// short word", which guarantees a non-empty continuation assistant_delta once the
// answer routes (Phase A's liveness signal) — claude's actual word is never asserted.
//
// The Class == "permission" assertion AFTER the drain is the non-vacuity gate:
// neither the caller's answer nor its cancel can pass over a modal that never
// surfaced (the drain deadlines) or a non-permission modal (this fails).
func raiseRealPermissionModal(t *testing.T, h *perConvHarness, reqID uint64, convID string, triggerPrompt string) string {
	t.Helper()
	sealSendMessage(t, h.phone, h.initSend, reqID, convID, fmt.Sprintf("m-%d", reqID), triggerPrompt)
	env := drainForControlEvent(t, h.phone, h.initRecv, protocol.TypeModalShown, modalSurfaceBudget)
	var shown protocol.ModalShownPayload
	if err := json.Unmarshal(env.Payload, &shown); err != nil {
		t.Fatalf("decode modal_shown payload: %v", err)
	}
	if shown.Class != "permission" {
		t.Fatalf("modal_shown Class = %q, want %q — real claude raised a non-permission modal (the trigger surface changed, or a trust/onboarding modal slipped through)", shown.Class, "permission")
	}
	if shown.ModalID == "" {
		t.Fatal("modal_shown carried an empty modal_id")
	}
	return shown.ModalID
}

// --- harness ----------------------------------------------------------------

// startModalResolutionHarness stands up the real interactive stack for the modal
// gate: it mirrors startPerConversationHarness (#997) with two deltas — (1) it
// pairs the phone WITH --allow-remote-permissions (the answer device gate), and
// (2) it spawns the daemon via spawnPermissionDaemon (WITHOUT
// --dangerously-skip-permissions) so real claude actually raises the permission
// modal. It seeds the bootstrap POOL id AND a conversation bound to it, so the
// Bash trigger drives the bootstrap session directly; the modal_shown /
// modal_dismissed broadcasts are operator-global (they fan to every interactive
// conn regardless of conversation), so the interactive capability alone receives
// them. Returns the harness and the seeded bound conversation id. Skips cleanly
// when claude / creds are absent.
func startModalResolutionHarness(t *testing.T) (*perConvHarness, string) {
	t.Helper()
	// No t.Parallel: WithWorktreeAuthenticated calls t.Setenv.
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skipf("realclaude: claude not on PATH: %v", err)
	}
	home := WithWorktreeAuthenticated(t) // skips cleanly when no creds
	claudeBin, err := exec.LookPath("claude")
	if err != nil {
		t.Fatalf("realclaude: resolve claude: %v", err)
	}

	// Isolated workdir under the authenticated HOME: guarantees the fresh-daemon
	// state (empty claude sessions dir) and is the cwd the bound conversation and
	// the trust-mark both resolve to.
	workdir := filepath.Join(home, "work")
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("realclaude: mkdir workdir: %v", err)
	}

	// Pair WITH --allow-remote-permissions: ResolveAnswer gates on the device's
	// MayAnswerRemotePermission() (== AllowRemotePermissions), which pairing
	// defaults OFF. Without this flag Phase A's answer denies at the gate, no
	// continuation streams, and drainForAssistantReply deadlines.
	exit, stdout, stderr := runPyry(t, "pair", "-pyry-name=test", "--name=phone-a", "--allow-remote-permissions")
	if exit != 0 {
		t.Fatalf("pyry pair exit=%d\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
	}
	payload := decodePairPayload(t, stdout)
	pubKey, err := base64.StdEncoding.DecodeString(payload.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	// Seed the bootstrap pool id (loaded once at startup) AND a conversation bound
	// to it, so the trigger's send_message routes to the bootstrap supervisor
	// instead of rejecting an empty binding (#678).
	seedBootstrapRegistry(t, home, liveModalBootstrapUUID)
	seedBoundConversation(t, home, liveModalConvID, liveModalBootstrapUUID, workdir)

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })

	d := spawnPermissionDaemon(t, home, workdir, claudeBin, fr.URL()+"/v2/server")
	t.Cleanup(func() { d.stop(t) })

	serverID := readPersistedServerID(t, home)
	waitBinaryHello(t, fr, serverID)

	dialCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	phone, err := fakephone.Dial(dialCtx, fr.URL(), serverID, payload.Token, "phone-a")
	if err != nil {
		t.Fatalf("phone dial: %v", err)
	}
	t.Cleanup(func() { _ = phone.Close() })

	initSend, initRecv := driveHandshakeInteractive(t, phone, pubKey, payload.Token)
	return &perConvHarness{phone: phone, initSend: initSend, initRecv: initRecv, home: home, workdir: workdir}, liveModalConvID
}

// spawnPermissionDaemon forks real pyry exactly like spawnBootstrapDaemon EXCEPT
// it drops the trailing --dangerously-skip-permissions flag, so real claude runs
// in default permission mode and the first gated Bash call raises a real
// permission modal. It reuses bootstrapDaemon / waitForReady / stop /
// shortSocketPath / ensurePyryBuilt / lockedBuffer unchanged. Blocks until the
// control socket is dialable.
func spawnPermissionDaemon(t *testing.T, home, workdir, claudeBin, relayURL string) *bootstrapDaemon {
	t.Helper()
	bin := ensurePyryBuilt(t) // builds with real HOME (warm cache); runs with isolated HOME
	socket := shortSocketPath(t)
	stderr := &lockedBuffer{}

	args := []string{
		"-pyry-socket=" + socket,
		"-pyry-name=test",
		"-pyry-claude=" + claudeBin,
		"-pyry-idle-timeout=0",
		"-pyry-workdir=" + workdir,
		"-pyry-relay=" + relayURL,
		"--",
		"--model", "haiku",
		// NOTE: NO --dangerously-skip-permissions — that flag suppresses the
		// permission modals this gate exists to exercise. This is the single line
		// that differs from spawnBootstrapDaemon.
	}
	cmd := exec.Command(bin, args...)
	// os.Environ() already carries the isolated HOME and the credential
	// (WithWorktreeAuthenticated t.Setenv's both). Add the relay switches.
	cmd.Env = append(os.Environ(), "PYRY_ALLOW_INSECURE_RELAY=1", "PYRY_MOBILE_V2=1")
	cmd.Stderr = io.MultiWriter(os.Stderr, stderr) // DEBUG tee

	if err := cmd.Start(); err != nil {
		t.Fatalf("realclaude: pyry start: %v", err)
	}
	doneCh := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(doneCh)
	}()

	d := &bootstrapDaemon{socketPath: socket, cmd: cmd, doneCh: doneCh, stderr: stderr}
	if err := d.waitForReady(10 * time.Second); err != nil {
		t.Fatalf("realclaude: daemon not ready: %v\nstderr:\n%s", err, stderr.String())
	}
	return d
}

// drainForControlEvent reads binary→phone frames in receive order — every
// noise_msg MUST be decrypted in order to keep the receive nonce in sync — and
// returns the first envelope whose Type == wantType. Unlike drainForReply it does
// NOT correlate on InReplyTo: modal_shown and modal_dismissed are EventID==nil
// broadcasts (modal_cancel is fire-and-broadcast, with no request to reply to). A
// TypeError envelope at any point is a hard fail (e.g. a session_error); a
// non-noise_msg control frame (e.g. rekey) is skipped WITHOUT decrypting so it
// does not advance the nonce. Interleaved acks / turn_state / assistant_delta /
// tool_use / turn_end are decrypted and skipped in order.
func drainForControlEvent(t *testing.T, phone *fakephone.Client, cs *noise.CipherState, wantType string, timeout time.Duration) protocol.Envelope {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatalf("no %s broadcast within %s — the trigger did not raise a modal, tui-driver did not classify it, or the daemon did not surface/dismiss it", wantType, timeout)
		}
		raw, err := phone.ReceiveBytes(remaining)
		if err != nil {
			if errors.Is(err, fakephone.ErrReceiveTimeout) {
				t.Fatalf("no %s broadcast within %s — the trigger did not raise a modal, tui-driver did not classify it, or the daemon did not surface/dismiss it", wantType, timeout)
			}
			t.Fatalf("phone receive (awaiting %s): %v", wantType, err)
		}
		var inner protocol.InnerFrameV2
		if err := json.Unmarshal(raw, &inner); err != nil {
			t.Fatalf("decode inner frame: %v", err)
		}
		if inner.Type != protocol.TypeNoiseMsg {
			continue // non-noise_msg control frame does not advance the receive nonce
		}
		cipher, err := base64.StdEncoding.DecodeString(inner.Data)
		if err != nil {
			t.Fatalf("decode inner data: %v", err)
		}
		plain, err := cs.Decrypt(cipher)
		if err != nil {
			t.Fatalf("phone decrypt (receive-nonce desync?): %v", err)
		}
		var env protocol.Envelope
		if err := json.Unmarshal(plain, &env); err != nil {
			t.Fatalf("decode envelope: %v", err)
		}
		if env.Type == protocol.TypeError {
			t.Fatalf("unexpected error envelope while awaiting %s: %s", wantType, string(env.Payload))
		}
		if env.Type == wantType {
			return env
		}
		// ack / turn_state / assistant_delta / tool_use / turn_end / an earlier
		// modal event — keep draining in order.
	}
}

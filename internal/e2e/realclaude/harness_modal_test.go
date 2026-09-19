//go:build e2e_realclaude

package realclaude

// Shared harness for the real-claude interactive suite: raising and observing a
// real permission modal.
//
// Transcribed from interactive_modal_resolution_test.go (#1030), which #1348
// deleted on 2026-08-16 along with the terminal-path test it served. The test is
// gone for good; these helpers stay because surviving stream-path tests call
// them. See harness_daemon_test.go for the full account of why this file exists.

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
	"github.com/pyrycode/pyrycode/internal/e2e/internal/paireddevice"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
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

// writeFileTrigger is the stream-runner trigger: a Write of a fresh file. The
// stream-json runner consults the --permission-prompt-tool only for actions that
// genuinely need approval, and a bare echo is auto-approved — so the old echo
// trigger never raised a modal on the stream path and
// TestInteractiveStreamModalResolution timed out (#1170). A Write of a new file is
// gated there.
//
// It used to have a sibling, bashEchoTrigger, which raised the modal on the
// deleted terminal runner instead. #1348 removed that runner on 2026-08-16 and
// this is now the only trigger, so callers no longer choose between them.
func writeFileTrigger(nonce int64) string {
	return fmt.Sprintf("Use the Write tool to create a file named pyrycode-%d.txt containing the single word hello. After it completes, reply with a single short word.", nonce)
}

// raiseRealPermissionModal sends triggerPrompt (forcing real claude to call a
// permission-gated tool in default permission mode — no --dangerously-skip-permissions),
// drains the wire to the resulting modal_shown broadcast, asserts it is a
// permission modal with a non-empty modal_id, and returns that modal_id. This is
// the shared trigger scaffold both phases call; the caller supplies the trigger,
// which is writeFileTrigger now that the terminal runner and its echo trigger are
// gone. Every trigger ends with "reply with a single
// short word", which guarantees a non-empty continuation assistant_delta once the
// answer routes (Phase A's liveness signal) — claude's actual word is never asserted.
//
// The Class == "permission" assertion AFTER the drain is the non-vacuity gate:
// neither the caller's answer nor its cancel can pass over a modal that never
// surfaced (the drain deadlines) or a non-permission modal (this fails).
func raiseRealPermissionModal(t *testing.T, h *perConvHarness, reqID uint64, convID string, triggerPrompt string) string {
	t.Helper()
	return raiseRealPermissionModalPayload(t, h, reqID, convID, triggerPrompt).ModalID
}

func raiseRealPermissionModalPayload(t *testing.T, h *perConvHarness, reqID uint64, convID string, triggerPrompt string) protocol.ModalShownPayload {
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
	return shown
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

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	relayURL := fr.URL() + "/v2/server"

	// Seed WITH remote permissions: ResolveAnswer gates on the device's
	// MayAnswerRemotePermission() (== AllowRemotePermissions), which pairing
	// defaults OFF. Without this privilege Phase A's answer denies at the gate, no
	// continuation streams, and drainForAssistantReply deadlines.
	payload, err := paireddevice.Setup(paireddevice.Config{
		Home:                   home,
		InstanceName:           "test",
		Relay:                  relayURL,
		DeviceName:             "phone-a",
		AllowRemotePermissions: true,
	})
	if err != nil {
		t.Fatalf("paireddevice.Setup: %v", err)
	}
	pubKey, err := base64.StdEncoding.DecodeString(payload.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	// Seed the bootstrap pool id (loaded once at startup) AND a conversation bound
	// to it, so the trigger's send_message routes to the bootstrap supervisor
	// instead of rejecting an empty binding (#678).
	seedBootstrapRegistry(t, home, liveModalBootstrapUUID)
	seedBoundConversation(t, home, liveModalConvID, liveModalBootstrapUUID, workdir)

	d := spawnPermissionDaemon(t, home, workdir, claudeBin, relayURL, permissionDaemonModel, false)
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

// permissionDaemonModel is the model every permission-modal gate in this package
// has always run under, hoisted out of spawnPermissionDaemon's argv when #1987
// made the model a parameter. Passing it preserves those gates byte for byte.
const permissionDaemonModel = "haiku"

// spawnPermissionDaemon forks real pyry exactly like spawnBootstrapDaemon. Its
// default call shape drops the trailing --dangerously-skip-permissions flag, so
// real claude runs in default permission mode and the first gated Bash call raises
// a real permission modal. The operatorBypass arm appends the two fixed literals
// needed to launch in bypass while retaining the stdio prompt surface after an
// in-band downgrade. It reuses bootstrapDaemon / waitForReady / stop /
// shortSocketPath / ensurePyryBuilt / lockedBuffer unchanged. Blocks until the
// control socket is dialable.
//
// model is the value handed to claude's --model. IT MUST BE A COMPILE-TIME
// CONSTANT at every call site: it lands directly in the child's argv, so a
// runtime-derived string would put arbitrary text there. Today's callers pass
// permissionDaemonModel or askQuestionCaptureModel, and which gate runs under
// which is argued at the caller, not here.
func spawnPermissionDaemon(t *testing.T, home, workdir, claudeBin, relayURL, model string, operatorBypass bool) *bootstrapDaemon {
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
		"--model", model,
		// Default callers append nothing, so there is no
		// --dangerously-skip-permissions. That is the shape the standing modal gates
		// exercise and the single line that differs from spawnBootstrapDaemon.
	}
	if operatorBypass {
		args = append(args,
			"--dangerously-skip-permissions",
			"--permission-prompt-tool", "stdio",
		)
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

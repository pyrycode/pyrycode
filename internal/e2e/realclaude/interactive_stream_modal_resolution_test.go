//go:build e2e_realclaude

package realclaude

// TestInteractiveStreamModalResolution is the #1154 deliverable (split from
// #1083): the stream-json variant of the #1030 real-claude permission
// round-trip. It drives a live `claude --model haiku` to a genuine tool
// permission modal under the production stream-json interactive runner
// (interactive_runner: "stream-json"), answers it allow_once from a paired
// phone, and asserts the turn observably resumes to completion. This is the
// desktop#483 scenario on the real stream stack: on the PTY path the scenario
// is red today; the stream path is expected to surface modal_shown correctly,
// and this gate proves it against real claude.
//
// It is a SIBLING of the PTY-path TestInteractiveModalResolution (#1030), not a
// replacement — #1030 runs the same answer round-trip under the DEFAULT PTY
// runner (it does not write the stream-json toggle). The two live side by side:
// desktop#483 is exactly PTY-red vs. stream-green. Scope here is answer-only
// (approve); the cancel phase (#1030 Phase B) is out of scope.
//
// Three rungs precede this. Fake tier, stream path (#1139): modal_shown /
// modal_answer / verdict, timeout denies fail-closed, against a spawned
// fakeclaude under interactive_runner:"stream-json". Real tier, PTY path (#1030):
// answer + cancel against live claude under the default PTY runner. This is the
// third — the stream-json variant of the #1030 real test, per the
// always-a-real-claude-gate policy (2026-07-08): the operator must never be the
// first real-stack execution, and the stream-json runner is the Mac-daemon
// cutover target, so the remote permission round-trip needs a real-claude gate
// on the stream stack before the binary is flipped.
//
// It composes two shipped real-claude tests with ZERO production change and no
// edit to any existing file: the #1030 harness/trigger scaffold
// (startModalResolutionHarness's shape, spawnPermissionDaemon,
// raiseRealPermissionModal, the Phase A answer envelope) and the #1153
// stream-json seams (writeStreamInteractiveConfig, drainForCompletedTurn). The
// single line that distinguishes this file from #1030's PTY-path harness is the
// writeStreamInteractiveConfig(t, home) call before the daemon spawns — #1153
// kept that config-writer a standalone helper (not a spawn wrapper) for exactly
// this rider, so it composes with spawnPermissionDaemon rather than
// spawnBootstrapDaemon.
//
// Non-vacuity is load-bearing on both ends: raiseRealPermissionModal asserts
// modal_shown{permission} with a non-empty modal_id BEFORE the answer is sealed
// (a modal that never surfaced deadlines the drain — never a silent pass), and
// drainForCompletedTurn requires a non-empty continuation assistant_delta (M1)
// FOLLOWED BY the terminal turn_state{idle} (M2), so "answered but the turn
// never resumed" fails at M1 and "resumed but never closed" fails at M2 —
// neither greens vacuously. Like #854/#997/#1028/#1030 this is a standing
// real-claude liveness gate in preship, not a deterministic RED/GREEN oracle;
// the fake tier (#1139) owns the deterministic shape. Placement under the
// e2e_realclaude build tag wires it into `make e2e-realclaude` (and thus
// `make preship`) with no Makefile change; startStreamModalResolutionHarness
// skips cleanly when claude / creds are absent.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/paireddevice"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// Seeded state for this gate. Distinct NAMES and LITERALS from the #1153
// (streamBootstrapUUID / streamConvID) and #1030 (liveModalBootstrapUUID /
// liveModalConvID) fixtures so no file in the package redeclares a name. The two
// real-claude modal tests never share on-disk state (each gets its own
// authenticated tempdir HOME), but the distinct literals keep any cross-test
// confusion impossible. Only the bootstrap POOL id is seeded (loaded once at
// startup); real claude still mints its own on-disk uuid.
const (
	streamModalBootstrapUUID = "77777777-7777-4777-8777-777777777777"
	streamModalConvID        = "55555555-5555-4555-8555-555555555555"
)

// TestInteractiveStreamModalResolution is #1030 Phase A on the stream stack:
// raise a real permission modal, answer allow_once, and prove the turn resumes
// to completion. Answer-only — the cancel phase (#1030 Phase B) is out of scope.
func TestInteractiveStreamModalResolution(t *testing.T) {
	h, convID := startStreamModalResolutionHarness(t, permissionDaemonModel)
	driveInteractiveStreamModalResolution(t, h, convID)
}

// Preserve the existing stdio allow/resume proof independently of ask context.
func TestInteractiveStreamStdioModalAllow(t *testing.T) {
	h, convID := startStdioModalResolutionHarness(t, permissionDaemonModel)
	driveInteractiveStreamModalResolution(t, h, convID)
}

func TestInteractiveStreamStdioAlwaysAllowIsSessionScoped(t *testing.T) {
	const (
		witness = "pyrycode-always-allow-witness.txt"
		command = "touch " + witness
		prompt  = "Use the Bash tool to run exactly `" + command + "`. Do not use another tool. After it completes, reply with one short word."
	)

	observations, configure, claudeVersion := startPermissionObserver(t)
	h, convID, _ := startObservedPermissionHarness(t, permissionDaemonModel, true, configure)
	shown := raiseRealPermissionModalPayload(t, h, 2, convID, prompt)
	if !shown.AlwaysAllow.Offered || len(shown.AlwaysAllow.Rules) == 0 {
		source := nextPermissionObservation(t, observations, "control_request")
		if source.RequestID == "" || source.Request.ToolUseID == "" || source.Request.ToolName != "Bash" || source.Request.Input.Command != command {
			t.Fatal("permission-offer diagnostic source did not match the correlated test-owned request")
		}
		validation, outcome := writePermissionOfferDiagnostic(t, permissionOfferDiagnosticPath, h.home, h.workdir, claudeVersion(), source, shown.AlwaysAllow)
		t.Fatalf("first permission modal did not offer always-allow rules: validation=%s outcome=%s diagnostic=%s", validation, outcome, permissionOfferDiagnosticPath)
	}
	sealEnvelope(t, h.phone, h.initSend, protocol.Envelope{
		ID:   3,
		Type: protocol.TypeModalAnswer,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.ModalAnswerPayload{
			ModalID:     shown.ModalID,
			OptionID:    string(turnevent.PermissionOptionKindAllowOnce),
			AnswerToken: "e2e-2365-session-grant",
			AlwaysAllow: true,
		}),
	})
	drainForCompletedTurn(t, h.phone, h.initRecv, convID, perTurnReplyBudget)
	witnessPath := filepath.Join(h.workdir, witness)
	if _, err := os.Stat(witnessPath); err != nil {
		t.Fatalf("first session-granted command did not create its witness: %v", err)
	}
	stale := time.Unix(1, 0)
	if err := os.Chtimes(witnessPath, stale, stale); err != nil {
		t.Fatalf("reset session-grant witness timestamp: %v", err)
	}

	sealSendMessage(t, h.phone, h.initSend, 4, convID, "m-4", prompt)
	drainForCompletedTurnWithoutModal(t, h.phone, h.initRecv, convID, perTurnReplyBudget)
	info, err := os.Stat(witnessPath)
	if err != nil {
		t.Fatalf("stat session-grant witness after second command: %v", err)
	}
	if !info.ModTime().After(stale) {
		t.Fatalf("second session-granted command did not update its witness: modtime=%s", info.ModTime())
	}

	fresh, freshConvID := startStdioModalResolutionHarness(t, permissionDaemonModel)
	freshShown := raiseRealPermissionModalPayload(t, fresh, 2, freshConvID, prompt)
	if freshShown.ModalID == "" {
		t.Fatal("fresh session permission modal carried an empty modal_id")
	}
	if _, err := os.Stat(filepath.Join(fresh.workdir, witness)); !os.IsNotExist(err) {
		t.Fatalf("fresh-session command executed before its new permission modal (stat error: %v)", err)
	}
}

// cancelledApprovalTurnEndBudget is the budget for a stdio permission's turn to
// close after the operator cancels its modal. Its SIZE is the assertion (#2416):
// the daemon's approval window is mcpApprovalTimeout, ten minutes unless
// PYRY_APPROVAL_TIMEOUT overrides it, and before the cancel path resolved the
// parked completer the turn closed only when that window elapsed. So this must
// stay an order of magnitude under it — a budget anywhere near ten minutes would
// green on the parked behaviour this gate exists to catch. It is generous for what
// it actually measures: the session is warm (the modal already surfaced), so all
// that remains is claude reading the deny and writing one short reply.
const cancelledApprovalTurnEndBudget = 60 * time.Second

// TestInteractiveStreamStdioCancelDeniesParkedPermission is #2416's live gate: a
// modal_cancel on a stream-json (stdio) permission must resolve claude's parked
// completer to deny at once, not leave it blocked for the approval window.
//
// It is the sibling of TestInteractiveStreamStdioModalAllow — same harness, same
// trigger shape, the cancel verb instead of the answer — and it goes red on the
// pre-#2416 daemon by deadline at the turn_end drain.
//
// Non-vacuity rests on three legs, and each fails a different way. The modal is
// asserted up BEFORE the cancel is sealed (raiseRealPermissionModalPayload's own
// gate), so a cancel can never pass over a modal that never surfaced. The
// dismissal broadcast is checked field by field, which is AC-1's "audit and
// broadcast unchanged" half — the deny must be ADDED to the cancel path, not
// substituted for what it already did. And the witness file must not exist:
// turn_end alone cannot tell a denied tool call from an executed one, and a
// permission path that ended the turn by ALLOWING the Write would satisfy every
// other assertion here.
func TestInteractiveStreamStdioCancelDeniesParkedPermission(t *testing.T) {
	const (
		witness = "pyrycode-cancel-denied-witness.txt"
		// "do not retry" keeps the turn from raising a second permission modal
		// after the deny, which would leave this drain waiting on a prompt nobody
		// answers — a deadline that would read as the bug under test.
		prompt = "Use the Write tool to create a file named " + witness + " containing the single word hello. " +
			"If the tool call is denied, do not retry it, do not use any other tool, and reply with one short word."
	)

	h, convID := startStdioModalResolutionHarness(t, permissionDaemonModel)
	shown := raiseRealPermissionModalPayload(t, h, 2, convID, prompt)

	sealEnvelope(t, h.phone, h.initSend, protocol.Envelope{
		ID:      3,
		Type:    protocol.TypeModalCancel,
		TS:      time.Now().UTC(),
		Payload: mustJSON(t, protocol.ModalCancelPayload{ModalID: shown.ModalID}),
	})

	env := drainForControlEvent(t, h.phone, h.initRecv, protocol.TypeModalDismissed, modalDismissBudget)
	var dismissed protocol.ModalDismissedPayload
	if err := json.Unmarshal(env.Payload, &dismissed); err != nil {
		t.Fatalf("decode modal_dismissed payload: %v", err)
	}
	if dismissed.ModalID != shown.ModalID || dismissed.Outcome != "cancelled" || dismissed.Source != "remote" {
		t.Fatalf("modal_dismissed = %+v, want {%s cancelled remote} — the cancel path's broadcast must be unchanged", dismissed, shown.ModalID)
	}

	drainForControlEvent(t, h.phone, h.initRecv, protocol.TypeTurnEnd, cancelledApprovalTurnEndBudget)

	if _, err := os.Stat(filepath.Join(h.workdir, witness)); !os.IsNotExist(err) {
		t.Fatalf("the cancelled permission's Write executed anyway (stat error: %v) — the cancel resolved the parked approval to allow, not deny", err)
	}
}

func driveInteractiveStreamModalResolution(t *testing.T, h *perConvHarness, convID string) {
	t.Helper()
	// A per-run nonce keeps the trigger command distinct (defeats accidental
	// caching) without asserting on its echo.
	nonce := time.Now().UnixNano()

	// Raise a real permission modal. The non-vacuity gate lives inside the helper:
	// it drains to modal_shown and asserts Class == "permission" + non-empty
	// modal_id BEFORE returning (AC #1 + AC #2), so a modal that never surfaced
	// deadlines the drain and fails rather than passing silently. Answering
	// requires the phone be paired --allow-remote-permissions (the device gate);
	// startStreamModalResolutionHarness pairs with it.
	shown := raiseRealPermissionModalPayload(t, h, 2, convID, writeFileTrigger(nonce))
	sealEnvelope(t, h.phone, h.initSend, protocol.Envelope{
		ID:   3,
		Type: protocol.TypeModalAnswer,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.ModalAnswerPayload{
			ModalID:  shown.ModalID,
			OptionID: string(turnevent.PermissionOptionKindAllowOnce),
			// A client-minted idempotency key, NOT authorization — an arbitrary
			// constant is fine (authorization is ModalID validity + the device gate).
			AnswerToken: "e2e-1154-answer-token",
		}),
	})

	// AC #3: the turn makes real progress after the answer — a non-empty
	// continuation assistant_delta (M1) FOLLOWED BY the terminal turn_state{idle}
	// (M2). "Modal answered but the turn never resumed" fails at M1; "delta but
	// never idle" fails at M2. drainForCompletedTurn (#1153) is strictly stronger
	// than #1030 Phase A's drainForAssistantReply (M1 only) — the tool executing
	// after the authorized allow is what produces the continuation delta.
	drainForCompletedTurn(t, h.phone, h.initRecv, convID, perTurnReplyBudget)
}

// --- harness ----------------------------------------------------------------

// startStreamModalResolutionHarness stands up the real interactive stack for the
// stream-json modal gate. It mirrors #1030's startModalResolutionHarness with
// exactly ONE inserted line — writeStreamInteractiveConfig(t, home) before the
// daemon spawns (the stream-vs-PTY differentiator, the entire reason this file
// exists) — and fresh constants. Everything else is identical: it pairs the
// phone WITH --allow-remote-permissions (the answer device gate — without it
// ResolveAnswer denies and the turn never resumes), seeds the bootstrap POOL id
// AND a conversation bound to it, and spawns via spawnPermissionDaemon (WITHOUT
// --dangerously-skip-permissions) so real claude actually raises the permission
// modal. The modal_shown / modal_dismissed broadcasts are operator-global (they
// fan to every interactive conn regardless of conversation), so the interactive
// capability alone receives them. Returns the harness and the seeded bound
// conversation id. Skips cleanly when claude / creds are absent.
// model is passed through to spawnPermissionDaemon's --model and MUST be a
// compile-time constant, for that helper's reason. It became a parameter in
// #1987, whose question gate needs the one model under which a live
// AskUserQuestion call has actually been measured; the two modal gates pass
// permissionDaemonModel and are unchanged by it.
func startStreamModalResolutionHarness(t *testing.T, model string) (*perConvHarness, string) {
	return startPermissionModalResolutionHarness(t, model, false)
}

func startStdioModalResolutionHarness(t *testing.T, model string) (*perConvHarness, string) {
	return startPermissionModalResolutionHarness(t, model, true)
}

func startPermissionModalResolutionHarness(t *testing.T, model string, stdioPermissionPrompt bool) (*perConvHarness, string) {
	h, convID, _ := startObservedPermissionHarness(t, model, stdioPermissionPrompt, nil)
	return h, convID
}

func startObservedPermissionHarness(t *testing.T, model string, stdioPermissionPrompt bool, configure func(string) string) (*perConvHarness, string, func()) {
	return startObservedPermissionHarnessWithOperatorBypass(t, model, stdioPermissionPrompt, configure, false)
}

func startObservedPermissionHarnessWithOperatorBypass(t *testing.T, model string, stdioPermissionPrompt bool, configure func(string) string, operatorBypass bool) (*perConvHarness, string, func()) {
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

	// Flip the production toggle to the stream-json interactive runner BEFORE the
	// daemon spawns — resolveConfigPath reads <home>/.pyry/config.json once at
	// startup. This one line is what distinguishes this gate from the PTY-path
	// #1030 harness; it is the seam this test exists to prove end-to-end.
	if stdioPermissionPrompt {
		writeStdioPermissionPromptConfig(t, home)
	} else {
		writeStreamInteractiveConfig(t, home)
	}

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	relayURL := fr.URL() + "/v2/server"

	// Seed WITH remote permissions: ResolveAnswer gates on the device's
	// MayAnswerRemotePermission() (== AllowRemotePermissions), which pairing
	// defaults OFF. Without this privilege the answer denies at the gate, no
	// continuation streams, and drainForCompletedTurn deadlines at M1.
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
	seedBootstrapRegistry(t, home, streamModalBootstrapUUID)
	seedBoundConversation(t, home, streamModalConvID, streamModalBootstrapUUID, workdir)

	if configure != nil {
		claudeBin = configure(claudeBin)
	}
	d := spawnPermissionDaemon(t, home, workdir, claudeBin, relayURL, model, operatorBypass)
	t.Cleanup(func() { d.stop(t) })

	serverID := readPersistedServerID(t, home)
	waitBinaryHello(t, fr, serverID)

	// The daemon travels on the harness since #2446, whose arm needs its control
	// socket to name the live child's pid and its stderr to say why a respawn
	// died. Every existing case ignores the field.
	h := &perConvHarness{home: home, workdir: workdir, daemon: d}
	connect := func() {
		dialCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		phone, err := fakephone.Dial(dialCtx, fr.URL(), serverID, payload.Token, "phone-a")
		if err != nil {
			t.Fatalf("phone dial: %v", err)
		}
		t.Cleanup(func() { _ = phone.Close() })
		h.phone = phone
		h.initSend, h.initRecv = driveHandshakeInteractive(t, phone, pubKey, payload.Token)
	}
	connect()
	return h, streamModalConvID, func() {
		_ = h.phone.Close()
		connect()
	}
}

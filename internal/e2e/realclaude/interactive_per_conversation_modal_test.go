//go:build e2e_realclaude

package realclaude

// TestInteractivePerConversationModalShown is the #1066 AC4 real-claude gate: a
// MINTED per-conversation session (created over the wire, so the daemon mints a
// dedicated claude session), started WITHOUT --dangerously-skip-permissions, must
// fan modal_shown for a permission-gated tool, scoped to its own conversation.
//
// Modal detection is proven for a BOOTSTRAP-bound conversation by #1030
// (TestInteractiveModalResolution). This gate pins the MINTED-session path — the one
// the desktop real-claude permission gate reported broken (no modal_shown, no reply).
// It composes #1030's no-skip-permissions daemon + real-permission trigger with
// #997's create-over-the-wire minting: spawnPermissionDaemon (no skip flag,
// --model haiku), createConversationViaPhone (mint a dedicated session), then the
// #1030 Bash trigger raising a real TUI permission modal on THAT minted session.
//
// This is the faithful reproduction the fake tier cannot produce (fakeclaude never
// writes a minted session's per-Cwd transcript). RED on current main iff the
// minted-session modal path is broken (#1069 readiness gate / #1070 transcript-coupled
// modal stream); GREEN once fixed. Substrate-clean: asserts only wire fields (class,
// modal_id, conversation_id), never claude's rendered words.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// livePerConvModalBootstrapUUID is the seeded bootstrap POOL id for this gate.
// Distinct from the sibling realclaude fixtures so no fixed id is shared in-package.
const livePerConvModalBootstrapUUID = "55555555-5555-4555-8555-555555555555"

func TestInteractivePerConversationModalShown(t *testing.T) {
	h := startPerConvPermissionHarness(t)
	nonce := time.Now().UnixNano()

	// Mint a dedicated per-conversation claude session over the wire (null cwd ⇒ the
	// daemon workdir). This is the path #1030 never exercises (it binds the bootstrap).
	convID := createConversationViaPhone(t, h.phone, h.initSend, h.initRecv, 2, nil)

	// Route a turn that forces real claude to call the gated Bash tool → a real TUI
	// permission modal on the MINTED session (same trigger shape as #1030's
	// raiseRealPermissionModal).
	sealSendMessage(t, h.phone, h.initSend, 3, convID, "m-1",
		fmt.Sprintf("Use the Bash tool to run the command: echo pyrycode-%d. After it completes, reply with a single short word.", nonce))

	// The #1066 assertion: the minted session's permission modal must fan modal_shown,
	// scoped to its own conversation. drainForControlEvent deadlines (RED) if no modal
	// surfaces — exactly the reported failure.
	env := drainForControlEvent(t, h.phone, h.initRecv, protocol.TypeModalShown, modalSurfaceBudget)
	var shown protocol.ModalShownPayload
	if err := json.Unmarshal(env.Payload, &shown); err != nil {
		t.Fatalf("decode modal_shown payload: %v", err)
	}
	if shown.Class != "permission" {
		t.Fatalf("modal_shown Class = %q, want %q — the minted session raised a non-permission modal (trust/onboarding slipped through, or the trigger surface changed)", shown.Class, "permission")
	}
	if shown.ModalID == "" {
		t.Fatal("modal_shown carried an empty modal_id")
	}
	if shown.ConversationID != convID {
		t.Fatalf("modal_shown ConversationID = %q, want the minted conversation %q "+
			"(a minted per-conversation session's permission modal must fan scoped to its own conversation)",
			shown.ConversationID, convID)
	}
	t.Logf("minted per-conversation session fanned modal_shown (modal_id=%s) scoped to conversation %s", shown.ModalID, convID)
}

// startPerConvPermissionHarness mirrors startPerConversationHarness (#997 — mint over
// the wire, no seeded bound conversation) but spawns the daemon via
// spawnPermissionDaemon (#1030 — WITHOUT --dangerously-skip-permissions) so real
// claude actually raises the permission modal. Viewing a modal is ungated (#607), so
// a plain interactive pairing suffices; no --allow-remote-permissions is needed (this
// gate only observes modal_shown, it does not answer). Skips cleanly when claude /
// creds are absent.
func startPerConvPermissionHarness(t *testing.T) *perConvHarness {
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

	workdir := filepath.Join(home, "work")
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("realclaude: mkdir workdir: %v", err)
	}

	exit, stdout, stderr := runPyry(t, "pair", "-pyry-name=test", "--name=phone-a")
	if exit != 0 {
		t.Fatalf("pyry pair exit=%d\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
	}
	payload := decodePairPayload(t, stdout)
	pubKey, err := base64.StdEncoding.DecodeString(payload.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	// Seed ONLY the bootstrap pool id; each conversation is created over the wire.
	seedBootstrapRegistry(t, home, livePerConvModalBootstrapUUID)

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
	return &perConvHarness{phone: phone, initSend: initSend, initRecv: initRecv, home: home, workdir: workdir}
}

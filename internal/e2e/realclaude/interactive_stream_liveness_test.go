//go:build e2e_realclaude

package realclaude

// TestInteractiveStreamLiveness drives one real Claude turn through the daemon's
// stream-json interactive runner and encrypted relay path. It requires nonempty
// attributed text followed by terminal idle, without constraining model wording
// or chunk counts. The seeded session and conversation binding must agree so the
// stream drain can attribute output to the driving conversation.

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/paireddevice"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/scenario"
	"github.com/pyrycode/pyrycode/internal/noise"
)

// Fixed identifiers for the seeded state, distinct from #854's liveBootstrapUUID /
// liveConvID (same package — the two files must not redeclare). streamBootstrapUUID
// is the bootstrap session's POOL id (pinned via seedBootstrapRegistry); streamConvID
// is the driving conversation, bound to it via seedBoundConversation. The two tests
// never share on-disk state (each gets its own authenticated tempdir HOME), but the
// distinct literals keep any cross-test confusion impossible.
const (
	streamBootstrapUUID = "88888888-8888-4888-8888-888888888888"
	streamConvID        = "66666666-6666-4666-8666-666666666666"
)

func TestInteractiveStreamLiveness(t *testing.T) {
	runParallel(t)
	// Parallel only through runParallel; see liveHome.
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skipf("realclaude: claude not on PATH: %v", err)
	}
	home := liveHome(t) // skips cleanly when no creds
	claudeBin, err := exec.LookPath("claude")
	if err != nil {
		t.Fatalf("realclaude: resolve claude: %v", err)
	}

	// Isolated workdir under the authenticated HOME. Load-bearing twice: it
	// guarantees the fresh-daemon state (empty claude sessions dir) AND sidesteps
	// the shared-session-folder collision (#828) by construction.
	workdir := filepath.Join(home, "work")
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("realclaude: mkdir workdir: %v", err)
	}

	// Flip the production toggle to the stream-json interactive runner BEFORE the
	// daemon spawns — resolveConfigPath reads <home>/.pyry/config.json once at
	// startup. This is the seam this test exists to prove end-to-end.
	writeStreamInteractiveConfig(t, home)

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	relayURL := fr.URL() + "/v2/server"

	payload, err := paireddevice.Setup(paireddevice.Config{
		Home:                   home,
		InstanceName:           "test",
		Relay:                  relayURL,
		DeviceName:             "phone-a",
		AllowRemotePermissions: false,
	})
	if err != nil {
		t.Fatalf("paireddevice.Setup: %v", err)
	}
	pubKey, err := base64.StdEncoding.DecodeString(payload.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	// Seed the deterministic bootstrap id + the driving conversation binding
	// BEFORE the daemon starts (the registry is loaded once at startup, no reload).
	seedBootstrapRegistry(t, home, streamBootstrapUUID)
	seedBoundConversation(t, home, streamConvID, streamBootstrapUUID, workdir)

	d := spawnBootstrapDaemon(t, home, workdir, claudeBin, relayURL)
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

	// A cold live session must deliver nonempty text and close within the budget.
	// Chunk counts and generated wording are independent of basic liveness.
	nonce := time.Now().UnixNano()
	sealSendMessage(t, phone, initSend, 2, streamConvID, "m-1",
		fmt.Sprintf("Without using tools, briefly describe reliable process supervision. run=%d", nonce))
	drainForCompletedTurn(t, phone, initRecv, streamConvID, 120*time.Second)
}

// --- stream-json toggle + turn drain ----------------------------------------

// writeStreamInteractiveConfig opts the daemon into the stream-json interactive
// runner via the production config toggle. resolveConfigPath reads
// <home>/.pyry/config.json once at startup (per-user, NOT per-instance), so this
// must land BEFORE the daemon spawns. A raw JSON literal (no internal/config
// import) keeps the realclaude package import-lean, mirroring seedBootstrapRegistry;
// a partial config leaves every other field at its default. Transcribed from the
// fake harness StartStreamInteractiveWithRelay (internal/e2e/harness.go). This is
// the reusable seam the permission-flow rider #1154 composes with
// spawnPermissionDaemon.
func writeStreamInteractiveConfig(t *testing.T, home string) {
	t.Helper()
	pyryDir := filepath.Join(home, ".pyry")
	if err := os.MkdirAll(pyryDir, 0o700); err != nil {
		t.Fatalf("realclaude: mkdir .pyry: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pyryDir, "config.json"),
		[]byte(`{"interactive_runner":"stream-json"}`), 0o600); err != nil {
		t.Fatalf("realclaude: write config.json: %v", err)
	}
}

// writeStdioPermissionPromptConfig selects the stream runner and the opt-in
// stdio permission transport in the one file the daemon reads at startup.
func writeStdioPermissionPromptConfig(t *testing.T, home string) {
	t.Helper()
	pyryDir := filepath.Join(home, ".pyry")
	if err := os.MkdirAll(pyryDir, 0o700); err != nil {
		t.Fatalf("realclaude: mkdir .pyry: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pyryDir, "config.json"),
		[]byte(`{"interactive_runner":"stream-json","stdio_permission_prompt":true}`), 0o600); err != nil {
		t.Fatalf("realclaude: write config.json: %v", err)
	}
}

// warnRateLimitStatus is the measured allowed-but-notable provider status.
const warnRateLimitStatus = scenario.WarnRateLimitStatus

// drainForCompletedTurn decrypts every noise_msg in receive order to preserve the
// receive nonce. It returns the driving conversation's concatenated reply after
// a non-whitespace delta (M1) followed by matching terminal idle (M2). Idle before
// M1 cannot close this turn. Unknown messages and unrecognized rate-limit statuses
// are global alarms, including frames attributed to other conversations.
func drainForCompletedTurn(t *testing.T, phone completedTurnReceiver, cs *noise.CipherState, convID string, timeout time.Duration) string {
	t.Helper()
	return drainForCompletedTurnOptions(t, phone, cs, convID, timeout, false)
}

func drainForCompletedTurnWithoutModal(t *testing.T, phone *fakephone.Client, cs *noise.CipherState, convID string, timeout time.Duration) string {
	t.Helper()
	return drainForCompletedTurnOptions(t, phone, cs, convID, timeout, true)
}

type completedTurnReceiver interface {
	ReceiveBytes(time.Duration) ([]byte, error)
}

func drainForCompletedTurnOptions(t *testing.T, phone completedTurnReceiver, cs *noise.CipherState, convID string, timeout time.Duration, rejectModal bool) string {
	t.Helper()
	return scenario.CompletedTurn(t, scenario.Receive(t, phone, cs), convID, timeout, rejectModal)
}

//go:build e2e_realclaude

package realclaude

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/agentrun"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/paireddevice"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// TestInteractiveSessionErrorRecovery uses production crash detection, backoff
// and the two-minute queue give-up window. Its daemon build intentionally ignores
// PYRY_E2E_BIN: an ordinary prebuilt daemon cannot activate this test bridge.
func TestInteractiveSessionErrorRecovery(t *testing.T) {
	claudeBin, err := exec.LookPath("claude")
	if err != nil {
		t.Skipf("claude not available: %v", err)
	}
	WithWorktreeAuthenticated(t) // dispatcher supplies authentication; never fetch it here
	bin := filepath.Join(t.TempDir(), "pyry")
	buildCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	build := exec.CommandContext(buildCtx, "go", "build", "-tags", "e2e_realclaude", "-o", bin, "github.com/pyrycode/pyrycode/cmd/pyry")
	build.Env = buildEnvWithRealHome()
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build control-enabled daemon: %v\n%s", err, out)
	}
	for _, drop := range []bool{false, true} {
		name := "retained"
		if drop {
			name = "dropped"
		}
		t.Run(name, func(t *testing.T) { testSessionErrorRecovery(t, bin, claudeBin, drop) })
	}
}

func testSessionErrorRecovery(t *testing.T, bin, claudeBin string, drop bool) {
	t.Helper()
	home := WithWorktreeAuthenticated(t)
	workdir := filepath.Join(home, "work")
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatal(err)
	}
	resolved, err := agentrun.ResolveWorkdir(workdir)
	if err != nil {
		t.Fatal(err)
	}
	workdir = resolved
	writeStreamInteractiveConfig(t, home)
	file := filepath.Join(home, "executable-selection")
	gate := filepath.Join(home, "exit-gate")
	failing := filepath.Join(home, "failing-claude")
	// Gate only the first exit until handshake/enqueue finish. All following
	// launches find the gate already open and exercise repeated fast child exits.
	script := "#!/bin/sh\nprintf 'FAILING_CHILD\\n' >&2\nwhile [ ! -e \"$PYRY_E2E_EXIT_GATE\" ]; do sleep 0.02; done\nexit 1\n"
	if err := os.WriteFile(failing, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	replaceSessionErrorSelection(t, file, failing)
	t.Setenv("PYRY_E2E_CLAUDE_BIN_FILE", file)
	t.Setenv("PYRY_E2E_EXIT_GATE", gate)
	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	relayURL := fr.URL() + "/v2/server"
	payload, err := paireddevice.Setup(paireddevice.Config{Home: home, InstanceName: "test", Relay: relayURL, DeviceName: "phone-a"})
	if err != nil {
		t.Fatal(err)
	}
	pubKey, err := base64.StdEncoding.DecodeString(payload.ServerStaticPubkey)
	if err != nil {
		t.Fatal(err)
	}
	seedBootstrapRegistry(t, home, liveBootstrapUUID)
	seedBoundConversation(t, home, liveConvID, liveBootstrapUUID, workdir)
	d := spawnBootstrapDaemonBinary(t, bin, home, workdir, claudeBin, relayURL)
	t.Cleanup(func() { d.stop(t) })
	serverID := readPersistedServerID(t, home)
	waitBinaryHello(t, fr, serverID)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	phone, err := fakephone.Dial(ctx, fr.URL(), serverID, payload.Token, "phone-a")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = phone.Close() })
	send, recv := driveHandshakeInteractive(t, phone, pubKey, payload.Token)
	heldText := "Without using tools, reply briefly about retained session recovery. marker=held-2859"
	sealSendMessage(t, phone, send, 2, liveConvID, "held-2859", heldText)
	state := &sessionErrorObservation{}
	deadline := time.Now().Add(20 * time.Second)
	for !state.queuedHeld {
		state.observe(t, readSessionErrorEnvelope(t, phone, recv, deadline))
	}
	if err := os.WriteFile(gate, []byte("exit"), 0o600); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(30 * time.Second)
	for !state.crashing {
		state.observe(t, readSessionErrorEnvelope(t, phone, recv, deadline))
	}
	if !state.queuedHeld || state.empty || state.delivered != "" || state.blocked {
		t.Fatalf("child_crashing did not retain undelivered head: %+v", state)
	}
	if strings.Count(d.stderr.String(), "FAILING_CHILD") < 3 {
		t.Fatal("crash notice without repeated executable launches")
	}
	expectedID := "held-2859"
	if drop {
		// Bounded by the actual production two-minute give-up, with room for its
		// final activation attempt; never shorten that window for this proof.
		deadline = time.Now().Add(3 * time.Minute)
		for !state.blocked || !state.empty {
			state.observe(t, readSessionErrorEnvelope(t, phone, recv, deadline))
		}
		if state.delivered != "" {
			t.Fatal("dropped head was delivered")
		}
		expectedID = "fresh-2859"
	}
	assertSessionErrorIdentity(t, d, home)
	replaceSessionErrorSelection(t, file, claudeBin) // no restart/kill/control request
	if drop {
		sealSendMessage(t, phone, send, 3, liveConvID, expectedID,
			"Without using tools, reply briefly about fresh session recovery. marker=fresh-2859")
	}
	deadline = time.Now().Add(2 * time.Minute)
	for !state.completed || state.delivered != expectedID || !state.empty {
		state.observe(t, readSessionErrorEnvelope(t, phone, recv, deadline))
		if state.delivered != "" && state.delivered != expectedID {
			t.Fatalf("unexpected/replayed delivery: %q", state.delivered)
		}
		if !drop && state.blocked {
			t.Fatal("retained head reached give-up before recovery")
		}
	}
	assertSessionErrorIdentity(t, d, home)
	// A completed real-Claude transcript is an independent witness of the exact
	// prompt received. Do not depend on the model's choice of reply wording.
	f, path, err := resolveAndOpenJSONL(workdir, liveBootstrapUUID)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	marker := "held-2859"
	if drop {
		marker = "fresh-2859"
	}
	if !strings.Contains(string(data), "marker="+marker) {
		t.Fatal("recovered Claude never received the expected prompt")
	}
	if drop && strings.Contains(string(data), "marker=held-2859") {
		t.Fatal("dropped prompt replayed into recovered Claude")
	}
	t.Logf("same daemon/session recovered automatically; completed=%t, delivered=%s, backlog empty=%t", state.completed, state.delivered, state.empty)
}

func replaceSessionErrorSelection(t *testing.T, file, bin string) {
	t.Helper()
	if err := os.WriteFile(file+".tmp", []byte(bin+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(file+".tmp", file); err != nil {
		t.Fatal(err)
	}
}

func assertSessionErrorIdentity(t *testing.T, d *bootstrapDaemon, home string) {
	t.Helper()
	select {
	case <-d.doneCh:
		t.Fatal("daemon exited during release")
	default:
	}
	data, err := os.ReadFile(filepath.Join(home, ".pyry", "test", "conversations.json"))
	if err != nil {
		t.Fatal(err)
	}
	var registry struct {
		Conversations []struct {
			ID      string `json:"id"`
			Session string `json:"current_session_id"`
		}
	}
	if err := json.Unmarshal(data, &registry); err != nil {
		t.Fatal(err)
	}
	for _, conv := range registry.Conversations {
		if conv.ID == liveConvID && conv.Session == liveBootstrapUUID {
			return
		}
	}
	t.Fatal("conversation bound session changed through recovery")
}

func readSessionErrorEnvelope(t *testing.T, phone *fakephone.Client, recv *noise.CipherState, deadline time.Time) protocol.Envelope {
	t.Helper()
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatal("timed out observing session-error recovery state")
		}
		inner := readInnerFrame(t, phone, remaining)
		if inner.Type != protocol.TypeNoiseMsg {
			continue
		}
		data, err := base64.StdEncoding.DecodeString(inner.Data)
		if err != nil {
			t.Fatal(err)
		}
		plain, err := recv.Decrypt(data)
		if err != nil {
			t.Fatal(err)
		}
		var env protocol.Envelope
		if err := json.Unmarshal(plain, &env); err != nil {
			t.Fatal(err)
		}
		return env
	}
}

type sessionErrorObservation struct {
	crashing, blocked, queuedHeld, empty, delta, completed bool
	delivered                                              string
}

func (s *sessionErrorObservation) observe(t *testing.T, env protocol.Envelope) {
	t.Helper()
	switch env.Type {
	case protocol.TypeSessionError:
		var p protocol.SessionErrorPayload
		decodeSessionErrorPayload(t, env, &p)
		if p.ConversationID != liveConvID {
			t.Fatalf("wrong session-error scope: %q", p.ConversationID)
		}
		switch p.Code {
		case protocol.CodeSessionChildCrashing:
			s.crashing = true
		case protocol.CodeSessionBlocked:
			s.blocked = true
		default:
			t.Fatalf("unexpected session-error code: %s", p.Code)
		}
	case protocol.TypeQueueState:
		var p protocol.QueueStatePayload
		decodeSessionErrorPayload(t, env, &p)
		if p.ConversationID != liveConvID {
			return
		}
		s.empty = len(p.Queued) == 0
		s.queuedHeld = len(p.Queued) == 1 && p.Queued[0].MessageID == "held-2859"
	case protocol.TypeMessage:
		var p protocol.MessagePayload
		decodeSessionErrorPayload(t, env, &p)
		if p.ConversationID == liveConvID && p.Role == "user" {
			if s.delivered != "" {
				t.Fatal("multiple user deliveries during one recovery")
			}
			s.delivered = p.MessageID
		}
	case protocol.TypeAssistantDelta:
		var p protocol.AssistantDeltaPayload
		decodeSessionErrorPayload(t, env, &p)
		if p.ConversationID == liveConvID && strings.TrimSpace(p.Text) != "" {
			s.delta = true
		}
	case protocol.TypeTurnState:
		var p protocol.TurnStatePayload
		decodeSessionErrorPayload(t, env, &p)
		if p.ConversationID == liveConvID && p.State == "idle" && s.delta {
			s.completed = true
		}
	}
}

func decodeSessionErrorPayload(t *testing.T, env protocol.Envelope, p any) {
	t.Helper()
	if err := json.Unmarshal(env.Payload, p); err != nil {
		t.Fatal(fmt.Errorf("decode %s: %w", env.Type, err))
	}
}

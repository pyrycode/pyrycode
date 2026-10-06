//go:build e2e_realclaude

package realclaude

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/paireddevice"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// TestInteractiveStreamDormantResetAfterRestart requires authenticated evidence
// in the ordinary live suite. The distinctive fact is sent only before shutdown;
// reset is the target's first action after restart. A persisted older note cannot
// supply the fact, so both the stored note and successor argv's prompt must be fresh.
func TestInteractiveStreamDormantResetAfterRestart(t *testing.T) {
	runParallel(t)
	claudeBin, err := exec.LookPath("claude")
	if err != nil {
		t.Skipf("realclaude: claude not on PATH: %v", err)
	}
	home := liveHome(t) // The only credential skip; evidence below is mandatory.
	workdir := filepath.Join(home, "work")
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeStreamInteractiveConfig(t, home)
	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	relayURL := fr.URL() + "/v2/server"
	payload, err := paireddevice.Setup(paireddevice.Config{
		Home: home, InstanceName: "test", Relay: relayURL, DeviceName: "phone-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	pubKey, err := base64.StdEncoding.DecodeString(payload.ServerStaticPubkey)
	if err != nil {
		t.Fatal(err)
	}
	seedBootstrapRegistry(t, home, clearBootstrapUUID)
	seedBoundConversation(t, home, clearConvID, clearSessUUID, workdir)
	dial := func() (*fakephone.Client, *noise.CipherState, *noise.CipherState) {
		t.Helper()
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
		return phone, send, recv
	}

	d1 := spawnBootstrapDaemon(t, home, workdir, claudeBin, relayURL)
	t.Cleanup(func() { d1.stop(t) })
	phone1, send1, recv1 := dial()
	fact := fmt.Sprintf("DORMANT%X", time.Now().UnixNano())
	sealSendMessage(t, phone1, send1, 29051, clearConvID, "plant",
		"Remember this unfinished project's build tag: "+fact+". Do not write it to any file or use tools. Reply only ok.")
	drainForCompletedTurn(t, phone1, recv1, clearConvID, perTurnReplyBudget)
	assertPerSessionPrompt(t, d1, clearSessUUID, idSettleTimeout)
	_ = phone1.Close()
	d1.stop(t)

	// Seed after shutdown, without altering the predecessor's transcript or either
	// registry. A restart constructor that reseeded sessions would erase the proof.
	const older = "An older session had no build tag. This note predates the unfinished project."
	notePath := filepath.Join(home, ".pyry", "test", "handoff-notes", clearConvID+".txt")
	if err := os.MkdirAll(filepath.Dir(notePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(notePath, []byte(older), 0o600); err != nil {
		t.Fatal(err)
	}
	d2 := spawnBootstrapDaemon(t, home, workdir, claudeBin, relayURL)
	t.Cleanup(func() { d2.stop(t) })
	phone2, send2, recv2 := dial()
	w := newResetWindow()
	sealEnvelope(t, phone2, send2, protocol.Envelope{
		ID: 29052, Type: protocol.TypeNewSession, TS: time.Now().UTC(),
		Payload: mustJSON(t, protocol.NewSessionPayload{ConversationID: clearConvID}),
	})
	w.awaitReset(t, phone2, recv2, 29052, clearResetWindowBudget)
	assertResetEdge(t, "wrapping up", w.edges[0], protocol.ResettingPayload{
		ConversationID: clearConvID, Active: true,
		Phase: protocol.ResetPhaseWrappingUp, Handoff: protocol.ResetHandoffPending,
	})
	assertResetEdge(t, "restarting with a fresh note", w.edges[1], protocol.ResettingPayload{
		ConversationID: clearConvID, Active: true,
		Phase: protocol.ResetPhaseRestarting, Handoff: protocol.ResetHandoffWritten,
	})
	assertResetEdge(t, "inactive", w.edges[2], protocol.ResettingPayload{ConversationID: clearConvID})
	if len(w.edges) != 3 || len(w.transitions) != 1 {
		t.Fatalf("reset produced %d edges and %d transitions", len(w.edges), len(w.transitions))
	}
	tr := w.transitions[0]
	if tr.edgesBefore < 2 || tr.payload.ConversationID != clearConvID ||
		tr.payload.PreviousSessionID != clearSessUUID || tr.payload.NewSessionID == clearSessUUID ||
		!uuidStemPattern.MatchString(tr.payload.NewSessionID) || tr.payload.Reason != "clear" {
		t.Fatalf("invalid dormant reset transition: %+v", tr)
	}
	if !strings.Contains(d2.stderr.String(), "--resume "+clearSessUUID) {
		t.Fatal("restart reset did not resume the predecessor's established identity")
	}
	note, err := os.ReadFile(notePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(note) == older || !strings.Contains(strings.ToUpper(string(note)), fact) {
		t.Fatalf("newly stored note lacks predecessor-only fact %q: %q", fact, note)
	}
	promptPath := assertPerSessionPrompt(t, d2, tr.payload.NewSessionID, idSettleTimeout)
	prompt, err := os.ReadFile(promptPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(prompt), string(note)) || !strings.Contains(strings.ToUpper(string(prompt)), fact) {
		t.Fatal("the successor's actual appended system prompt lacks the newly stored note and fact")
	}
	t.Logf("restart-first reset resumed %s, wrote a fresh note (%d bytes), and composed it into successor %s",
		clearSessUUID, len(note), tr.payload.NewSessionID)
}

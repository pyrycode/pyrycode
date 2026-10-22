//go:build e2e_realclaude

package realclaude

// Three sequential turns on one held-open child prove contextual continuity.
// A per-run token is planted, separated from recall by a filler turn, and read
// back from recorded source text after each live running-to-idle transition.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/paireddevice"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// Fixed identifiers for the seeded state, distinct in NAME from every same-package
// const (compile-safety) and in LITERAL from feature/1172's 33333333/44444444
// (hygiene — a UUID-literal collision is not a compile error). The 4/8
// version/variant nibbles keep the UUIDv4 shape of the existing fixtures.
// streamMultiTurnBootstrapUUID is the bootstrap session's POOL id (pinned via
// seedBootstrapRegistry); streamMultiTurnConvID is the driving conversation,
// bound to it via seedBoundConversation.
const (
	streamMultiTurnBootstrapUUID = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	streamMultiTurnConvID        = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
)

func TestInteractiveStreamMultiTurnContinuity(t *testing.T) {
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
	// startup. This is the seam this test exercises end-to-end.
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
	seedBootstrapRegistry(t, home, streamMultiTurnBootstrapUUID)
	seedBoundConversation(t, home, streamMultiTurnConvID, streamMultiTurnBootstrapUUID, workdir)

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

	// A per-run nonce defeats cross-run caching from producing a false green; the
	// token is a contiguous uppercase alphanumeric identifier (%X → uppercase hex,
	// no internal separators for claude to reformat).
	nonce := time.Now().UnixNano()
	token := fmt.Sprintf("PYRY%X", nonce)

	// Three sequential turns on the ONE held-open session (initSend/initRecv,
	// streamMultiTurnConvID). The filler turn is load-bearing — see the file
	// header. Each turn fully drains to turn_state{idle} before the next send.
	turns := []struct {
		id     uint64
		msgID  string
		prompt string
	}{
		{2, "m-1", fmt.Sprintf("Remember this exact identifier for the rest of our conversation: %s. Reply with just the word ok.", token)},
		{3, "m-2", fmt.Sprintf("Reply with a single short word. run=%d", nonce)},
		{4, "m-3", "What was the exact identifier I asked you to remember earlier? Reply with only that identifier and nothing else."},
	}

	// After the loop, recallText holds the recall turn's (the last turn's) reply.
	// Only that turn's text drives the continuity assertion.
	var recallText string
	for _, turn := range turns {
		sealSendMessage(t, phone, initSend, turn.id, streamMultiTurnConvID, turn.msgID, turn.prompt)
		recallText = drainForCompletedSourceText(t, phone, initRecv, home, streamMultiTurnConvID, perTurnReplyBudget)
	}

	// Continuity by content: the recall reply must echo the planted token. A
	// respawned/memory-less child cannot produce it, so this cannot pass
	// vacuously. ToUpper absorbs any hex case-folding by claude; Contains
	// (substring, not equality) absorbs surrounding words.
	if !strings.Contains(strings.ToUpper(recallText), token) {
		t.Fatalf("continuity broken: recall turn's reply %q does not contain the planted token %q — "+
			"the held-open child lost context across turns (an unexpected respawn, or dropped conversation memory)",
			recallText, token)
	}
}

// --- capturing turn drain ---------------------------------------------------

// drainForCompletedSourceText waits for the named live turn to run and return
// idle, then reads that completed turn's text from retained source history.
func drainForCompletedSourceText(t *testing.T, phone *fakephone.Client, cs *noise.CipherState, home, convID string, timeout time.Duration) string {
	t.Helper()
	sawRunning := false
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatalf("conversation %q did not run and close within %s", convID, timeout)
		}
		raw, err := phone.ReceiveBytes(remaining)
		if err != nil {
			if errors.Is(err, fakephone.ErrReceiveTimeout) {
				continue // deadline reached — re-loop into the milestone-specific t.Fatalf above
			}
			t.Fatalf("phone receive (drain): %v", err)
		}
		var inner protocol.InnerFrameV2
		if err := json.Unmarshal(raw, &inner); err != nil {
			t.Fatalf("decode inner frame (drain): %v", err)
		}
		if inner.Type != protocol.TypeNoiseMsg {
			// A non-noise_msg control frame (e.g. rekey) does not advance the
			// receive nonce — skip without decrypting.
			continue
		}
		cipher, err := base64.StdEncoding.DecodeString(inner.Data)
		if err != nil {
			t.Fatalf("decode inner data (drain): %v", err)
		}
		plain, err := cs.Decrypt(cipher)
		if err != nil {
			t.Fatalf("phone decrypt (receive-nonce desync?): %v", err)
		}
		var env protocol.Envelope
		if err := json.Unmarshal(plain, &env); err != nil {
			t.Fatalf("decode envelope (drain): %v", err)
		}
		if env.Type != protocol.TypeTurnState {
			continue
		}
		var phase protocol.TurnStatePayload
		if err := json.Unmarshal(env.Payload, &phase); err != nil {
			t.Fatal(err)
		}
		if phase.ConversationID != convID {
			continue
		}
		if phase.State != "idle" {
			sawRunning = true
			continue
		}
		if !sawRunning {
			continue
		}
		return completedSourceText(t, home, convID)
	}
}

func completedSourceText(t *testing.T, home, convID string) string {
	t.Helper()
	page, err := history.New(filepath.Join(home, ".pyry", "test")).Page(conversations.ConversationID(convID), "", 128)
	if err != nil {
		t.Fatal(err)
	}
	if !page.AtStart {
		t.Fatal("completed reply source exceeds the fixture window")
	}
	var ending protocol.TurnEndPayload
	for _, entry := range page.Entries {
		if entry.Type == protocol.TypeTurnEnd {
			if err := json.Unmarshal(entry.Payload, &ending); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	if ending.TurnID == "" {
		t.Fatal("idle has no recorded completion")
	}
	var text strings.Builder
	for i := len(page.Entries) - 1; i >= 0; i-- {
		entry := page.Entries[i]
		if entry.Type != protocol.TypeAssistantDelta {
			continue
		}
		var delta protocol.AssistantDeltaPayload
		if err := json.Unmarshal(entry.Payload, &delta); err != nil {
			t.Fatal(err)
		}
		if delta.TurnID == ending.TurnID && delta.ParentToolUseID == "" {
			text.WriteString(delta.Text)
		}
	}
	if strings.TrimSpace(text.String()) == "" {
		t.Fatal("completed turn recorded no nonempty reply")
	}
	return text.String()
}

func TestCompletedSourceText(t *testing.T) {
	const conv = conversations.ConversationID(streamMultiTurnConvID)
	home := t.TempDir()
	h := history.New(filepath.Join(home, ".pyry", "test"))
	for _, fact := range []struct {
		typ     string
		payload any
	}{
		{protocol.TypeAssistantDelta, protocol.AssistantDeltaPayload{TurnID: "old", Text: "old reply"}},
		{protocol.TypeTurnEnd, protocol.TurnEndPayload{TurnID: "old"}},
		{protocol.TypeAssistantDelta, protocol.AssistantDeltaPayload{TurnID: "new", Text: "one"}},
		{protocol.TypeAssistantDelta, protocol.AssistantDeltaPayload{TurnID: "new", ParentToolUseID: "child", Text: "child reply"}},
		{protocol.TypeAssistantDelta, protocol.AssistantDeltaPayload{TurnID: "new", Text: "two"}},
		{protocol.TypeTurnEnd, protocol.TurnEndPayload{TurnID: "new"}},
	} {
		raw, err := json.Marshal(fact.payload)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.Append(conv, fact.typ, raw, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if got := completedSourceText(t, home, string(conv)); got != "onetwo" {
		t.Fatalf("completed source text = %q", got)
	}
}

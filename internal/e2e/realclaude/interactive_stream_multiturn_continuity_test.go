//go:build e2e_realclaude

package realclaude

// Three sequential turns on one held-open child prove contextual continuity.
// A per-run token is planted, separated from recall by a filler turn, and read
// back from recorded source text after each live running-to-idle transition.

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

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/paireddevice"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/scenario"
	"github.com/pyrycode/pyrycode/internal/history"
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
	next := scenario.PollingReceive(t, phone, initRecv)
	for _, turn := range turns {
		source := scenario.SourceSince(t, home, streamMultiTurnConvID)
		sealSendMessage(t, phone, initSend, turn.id, streamMultiTurnConvID, turn.msgID, turn.prompt)
		recallText = drainForCompletedSourceText(t, next, source, home, streamMultiTurnConvID, perTurnReplyBudget)
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

// drainForCompletedSourceText requires actual owned delivery and retained source
// completion before independently reading the completed turn's recorded text.
func drainForCompletedSourceText(t *testing.T, next scenario.Next, source scenario.SourceFacts, home, convID string, timeout time.Duration) string {
	t.Helper()
	owned := scenario.CompletedTurn(t, next, convID, timeout, false, source)
	recorded := completedSourceText(t, home, convID)
	if recorded != owned {
		t.Fatal("recorded completion differs from the command-owned source reply")
	}
	return recorded
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
	var endingSource *history.SessionProvenance
	var endingID uint64
	for _, entry := range page.Entries {
		if entry.Type == protocol.TypeTurnEnd {
			var completion struct {
				protocol.TurnEndPayload
				Parent string `json:"parent_tool_use_id"`
			}
			if err := json.Unmarshal(entry.Payload, &completion); err != nil {
				t.Fatal(err)
			}
			if completion.Parent != "" {
				continue
			}
			ending, endingSource, endingID = completion.TurnEndPayload, entry.Session, entry.ID
			break
		}
	}
	if ending.TurnID == "" {
		t.Fatal("idle has no recorded completion")
	}
	var text strings.Builder
	for i := len(page.Entries) - 1; i >= 0; i-- {
		entry := page.Entries[i]
		if entry.Type != protocol.TypeAssistantDelta || entry.ID >= endingID {
			continue
		}
		sameSource := entry.Session == nil && endingSource == nil
		if entry.Session != nil && endingSource != nil {
			sameSource = *entry.Session == *endingSource
		}
		if !sameSource {
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

func TestCompletedSourceTextIdentity(t *testing.T) {
	for _, scenario := range []string{"session reuse", "agent kind reuse", "later child completion", "text after completion"} {
		t.Run(scenario, func(t *testing.T) {
			const conv = conversations.ConversationID(streamMultiTurnConvID)
			home := t.TempDir()
			h := history.New(filepath.Join(home, ".pyry", "test"))
			main := history.SessionProvenance{Kind: "claude", SessionID: "producing-session"}
			appendFact := func(typ string, payload any, source *history.SessionProvenance) {
				t.Helper()
				raw, err := json.Marshal(payload)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := h.AppendWithMetadata(conv, typ, raw, time.Now(), history.Metadata{Session: source}); err != nil {
					t.Fatal(err)
				}
			}
			foreign := main
			foreign.SessionID = "previous-session"
			if scenario == "agent kind reuse" {
				foreign = main
				foreign.Kind = "codex"
			}
			appendFact(protocol.TypeAssistantDelta, protocol.AssistantDeltaPayload{TurnID: "reused", Text: "foreign"}, &foreign)
			appendFact(protocol.TypeTurnEnd, protocol.TurnEndPayload{TurnID: "reused"}, &foreign)
			appendFact(protocol.TypeAssistantDelta, protocol.AssistantDeltaPayload{TurnID: "reused", Text: "one"}, &main)
			appendFact(protocol.TypeAssistantDelta, protocol.AssistantDeltaPayload{TurnID: "reused", ParentToolUseID: "child", Text: "child"}, &main)
			appendFact(protocol.TypeAssistantDelta, protocol.AssistantDeltaPayload{TurnID: "reused", Text: "two"}, &main)
			appendFact(protocol.TypeTurnEnd, protocol.TurnEndPayload{TurnID: "reused"}, &main)
			if scenario == "later child completion" {
				appendFact(protocol.TypeAssistantDelta, protocol.AssistantDeltaPayload{TurnID: "child-turn", ParentToolUseID: "child", Text: "later child"}, &main)
				appendFact(protocol.TypeTurnEnd, map[string]string{"turn_id": "child-turn", "parent_tool_use_id": "child"}, &main)
			}
			if scenario == "text after completion" {
				appendFact(protocol.TypeAssistantDelta, protocol.AssistantDeltaPayload{TurnID: "reused", Text: "unfinished"}, &main)
			}
			if got := completedSourceText(t, home, string(conv)); got != "onetwo" {
				t.Fatalf("completed source text = %q, want onetwo", got)
			}
		})
	}
}

func TestCompletedSourceTextWaitsForOwnedHistory(t *testing.T) {
	const conv = conversations.ConversationID(streamMultiTurnConvID)
	home := t.TempDir()
	store := history.New(filepath.Join(home, ".pyry", "test"))
	appendTurn := func(session, turn, text string, opened time.Time) {
		t.Helper()
		for i, fact := range []struct {
			kind  string
			value any
		}{
			{"main_turn_opened", map[string]any{"conversation_id": conv, "turn_id": turn, "occurred_at": opened}},
			{protocol.TypeAssistantDelta, protocol.AssistantDeltaPayload{ConversationID: string(conv), TurnID: turn, Text: text}},
			{protocol.TypeTurnEnd, protocol.TurnEndPayload{ConversationID: string(conv), TurnID: turn, StopReason: "end_turn"}},
		} {
			raw, err := json.Marshal(fact.value)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.AppendWithMetadata(conv, fact.kind, raw, opened.Add(time.Duration(i)*time.Second), history.Metadata{Session: &history.SessionProvenance{Kind: "claude", SessionID: session}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	appendTurn("previous", "old", "old reply", time.Now().UTC().Add(-time.Minute))
	source := scenario.SourceSince(t, home, string(conv))
	opened := time.Now().UTC()
	reads := 0
	next := func(time.Time) protocol.Envelope {
		reads++
		switch reads {
		case 1:
			return protocol.Envelope{Type: protocol.TypeTurnState, SessionID: json.RawMessage(`"current"`), ConversationID: json.RawMessage(`"` + string(conv) + `"`), TS: opened.Add(time.Second), Payload: mustJSON(t, protocol.TurnStatePayload{ConversationID: string(conv), State: "idle"})}
		case 2:
			appendTurn("current", "fresh", "fresh reply", opened)
		default:
			t.Fatal("owned idle and later history did not complete")
		}
		return protocol.Envelope{}
	}
	if got := drainForCompletedSourceText(t, next, source, home, string(conv), time.Second); got != "fresh reply" || reads != 2 {
		t.Fatalf("text=%q reads=%d, want the fresh reply after delayed history", got, reads)
	}
}

//go:build e2e

package e2e

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/paireddevice"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// #2739: a conversation's turn tail must reach its own history and clients while
// another conversation is active. The daemon used to drop every event of a
// non-active conversation at the stream drain, so A's tool result, reply and turn
// end vanished the moment a message to B moved the cursor, and the client showed
// A's tool call running forever.
//
// THE FEED. Both children replay the same two fragments (the replay knobs reach
// every child through the daemon's env): the first user turn writes bgConvFirst,
// a lone tool_use with no result, so the turn stays open; the release file then
// lets bgConvSecond out — tool result, reply, result line. The first child to see
// the release file deletes it, so the test re-creates it until both turns end.
const (
	bgConvBootstrapUUID = "27390000-0000-4000-8000-000000000001"
	bgConvA             = "27390000-0000-4000-8000-000000000002"
	bgConvNeedle        = "e2e-2739:reply-after-tool"
	bgConvToolUseID     = "toolu_2739"
)

var (
	bgConvFirst = `{"type":"assistant","message":{"id":"m-tool-2739","role":"assistant","content":[` +
		`{"type":"tool_use","id":"` + bgConvToolUseID + `","name":"Bash","input":{"command":"true"}}]}}` + "\n"
	bgConvSecond = strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + bgConvToolUseID +
			`","content":"ok","is_error":false}]}}`,
		`{"type":"assistant","message":{"id":"m-reply-2739","role":"assistant","content":[{"type":"text","text":"` +
			bgConvNeedle + `"}]}}`,
		`{"type":"result","subtype":"success","session_id":"fake-stream"}`,
	}, "\n") + "\n"
)

// TestRelayV2_StreamBackgroundConversationTailReachesHistory: send to A, wait for
// A's tool_use, send to B (the cursor moves to B), release A's tail, and assert
// A's tool result, reply and turn end reach the phone stamped with A's id and
// land in A's durable history.
func TestRelayV2_StreamBackgroundConversationTailReachesHistory(t *testing.T) {
	dir := t.TempDir()
	firstPath := filepath.Join(dir, "first.jsonl")
	secondPath := filepath.Join(dir, "second.jsonl")
	releasePath := filepath.Join(dir, "release")
	for path, body := range map[string]string{firstPath: bgConvFirst, secondPath: bgConvSecond} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write replay fragment: %v", err)
		}
	}

	home := shortHome(t)
	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	relayURL := fr.URL() + "/v2/server"

	payload, err := paireddevice.Setup(paireddevice.Config{
		Home: home, InstanceName: "test", Relay: relayURL, DeviceName: "phone-a",
	})
	if err != nil {
		t.Fatalf("setup paired device: %v", err)
	}
	pubKey, err := base64.StdEncoding.DecodeString(payload.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}
	seedBoundConversation(t, home, bgConvA, bgConvBootstrapUUID)

	h := StartStreamInteractiveWithRelay(t, home, bgConvBootstrapUUID, relayURL,
		"PYRY_FAKE_CLAUDE_STREAM_REPLAY_FIRST="+firstPath,
		"PYRY_FAKE_CLAUDE_STREAM_REPLAY_SECOND="+secondPath,
		"PYRY_FAKE_CLAUDE_STREAM_REPLAY_RELEASE="+releasePath)
	t.Cleanup(func() { h.Stop(t) })

	serverID := readPersistedServerID(t, home)
	waitBinaryHello(t, fr, serverID)
	dialCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	phone, err := fakephone.Dial(dialCtx, fr.URL(), serverID, payload.Token, "phone-a")
	if err != nil {
		t.Fatalf("phone dial: %v", err)
	}
	t.Cleanup(func() { _ = phone.Close() })
	send, recv := driveHandshakeToOpenDaemonInteractive(t, phone, pubKey, payload.Token)
	sealSend, nextEnv := sealedConnDriver(t, phone, "phone", send, recv)

	// Mint B over the wire.
	sealSend(protocol.Envelope{ID: 27390, Type: protocol.TypeCreateConversation, TS: time.Now().UTC(),
		Payload: mustJSON(t, protocol.CreateConversationPayload{})})
	var convB string
	deadline := time.Now().Add(15 * time.Second)
	for convB == "" {
		env, ok := nextEnv(deadline)
		if !ok {
			t.Fatal("conversation_created never arrived")
		}
		if env.Type == protocol.TypeError {
			t.Fatalf("unexpected error envelope: %s", env.Payload)
		}
		if env.Type == protocol.TypeConversationCreated {
			var p protocol.ConversationCreatedPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("decode conversation_created: %v", err)
			}
			convB = p.ID
		}
	}

	sendAndAwaitToolUse := func(reqID uint64, convID, msgID string) {
		t.Helper()
		sealSend(protocol.Envelope{ID: reqID, Type: protocol.TypeSendMessage, TS: time.Now().UTC(),
			Payload: mustJSON(t, protocol.SendMessagePayload{ConversationID: convID, MessageID: msgID, Text: "go\n"})})
		deadline := time.Now().Add(30 * time.Second)
		for {
			env, ok := nextEnv(deadline)
			if !ok {
				t.Fatalf("no tool_use for conversation %s; its turn never went in flight", convID)
			}
			if env.Type == protocol.TypeError {
				t.Fatalf("unexpected error envelope: %s", env.Payload)
			}
			if env.Type != protocol.TypeToolUse {
				continue
			}
			var p protocol.ToolUsePayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("decode tool_use: %v", err)
			}
			if p.ConversationID == convID {
				return
			}
		}
	}
	sendAndAwaitToolUse(27391, bgConvA, "u-a")
	// The cursor moves to B here, with A's turn still open.
	sendAndAwaitToolUse(27392, convB, "u-b")

	// Release both tails. Each child deletes the file when it consumes it, so it is
	// re-created whenever it is gone until both turns have ended.
	type seen struct{ toolResult, reply, turnEnd bool }
	got := map[string]*seen{bgConvA: {}, convB: {}}
	deadline = time.Now().Add(30 * time.Second)
	for !got[bgConvA].turnEnd || !got[convB].turnEnd {
		if _, err := os.Stat(releasePath); os.IsNotExist(err) {
			if err := os.WriteFile(releasePath, nil, 0o600); err != nil {
				t.Fatalf("write release signal: %v", err)
			}
		}
		env, ok := nextEnv(time.Now().Add(200 * time.Millisecond))
		if !ok {
			if time.Now().After(deadline) {
				t.Fatalf("tails did not complete: A=%+v B=%+v", *got[bgConvA], *got[convB])
			}
			continue
		}
		var p struct {
			ConversationID string `json:"conversation_id"`
			Text           string `json:"text"`
		}
		switch env.Type {
		case protocol.TypeToolResult, protocol.TypeAssistantDelta, protocol.TypeTurnEnd:
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("decode %s: %v", env.Type, err)
			}
		default:
			continue
		}
		s, ok := got[p.ConversationID]
		if !ok {
			t.Fatalf("%s stamped with unknown conversation %q", env.Type, p.ConversationID)
		}
		switch env.Type {
		case protocol.TypeToolResult:
			s.toolResult = true
		case protocol.TypeAssistantDelta:
			s.reply = s.reply || strings.Contains(p.Text, bgConvNeedle)
		case protocol.TypeTurnEnd:
			s.turnEnd = true
		}
	}
	if a := got[bgConvA]; !a.toolResult || !a.reply {
		t.Errorf("conversation A's frames: %+v, want tool_result, reply and turn_end stamped with A", *a)
	}

	// A's durable history holds the tool result and the turn end.
	var histToolResult, histTurnEnd bool
	histDeadline := time.Now().Add(5 * time.Second)
	for !(histToolResult && histTurnEnd) && time.Now().Before(histDeadline) {
		for _, e := range waitForHistoryEntries(t, home, bgConvA) {
			histToolResult = histToolResult || e.Type == protocol.TypeToolResult
			histTurnEnd = histTurnEnd || e.Type == protocol.TypeTurnEnd
		}
		if !(histToolResult && histTurnEnd) {
			time.Sleep(50 * time.Millisecond)
		}
	}
	if !histToolResult || !histTurnEnd {
		t.Errorf("conversation A's history: tool_result=%v turn_end=%v, want both", histToolResult, histTurnEnd)
	}
}

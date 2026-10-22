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
	"github.com/pyrycode/pyrycode/internal/e2e/internal/scenario"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// Attached clients retain controls while posts are served by canonical catch-up.
func TestChannelPost_E2E_ReachesAttachedClient(t *testing.T) {
	const (
		initialUUID = "11111111-1111-4111-8111-111111111111"
		channelName = "reminders"
		text        = "What is the one thing you are avoiding today?"
		// Generous by design — a hang-catcher, not a timing assumption. The post
		// travels the control socket, the durable log and the relay leg.
		frameDeadline = 15 * time.Second
	)

	home := shortHome(t)

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

	h := StartStreamInteractiveWithRelay(t, home, initialUUID, relayURL)
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
	send, recv := driveHandshakeToOpenDaemonInteractive(t, phone, pubKey, payload.Token, protocol.CapabilityThread)
	seal, next := sealedConnDriver(t, phone, "attached", send, recv)

	// ── The post, with the phone already attached ──
	p := runVerb(t, h.SocketPath, home, "channel", "post", "--name", channelName, "--text", text)
	if p.ExitCode != 0 {
		t.Fatalf("pyry channel post exit=%d\nstdout:\n%s\nstderr:\n%s", p.ExitCode, p.Stdout, p.Stderr)
	}

	row := waitForConversationNamed(t, home, channelName)

	waitForHistoryType(t, home, row.ID, protocol.TypeTurnEnd, 5*time.Second)
	entries := readHistoryEntries(home, row.ID)
	if len(entries) != 2 {
		t.Fatal("post source is not unique")
	}
	var delta protocol.AssistantDeltaPayload
	if json.Unmarshal(entries[0].Payload, &delta) != nil || delta.Text != text || delta.ConversationID != row.ID || delta.TurnID == "" {
		t.Fatal("post source changed")
	}
	assertHostPostEnd(t, entries[1].Payload, row.ID, delta.TurnID)
	seal(protocol.Envelope{ID: 3737, Type: protocol.TypeThreadCatchUp, TS: time.Now().UTC(), Payload: mustJSON(t, protocol.ThreadCatchUpPayload{ConversationID: row.ID, Limit: 100})})
	deadline := time.Now().Add(frameDeadline)
	found := false
	for {
		env, ok := next(deadline)
		if !ok || env.Type == protocol.TypeError {
			t.Fatal("post catch-up unavailable")
		}
		if env.Type == protocol.TypeBanner || env.Type == protocol.TypeAssistantDelta || env.Type == protocol.TypeTurnEnd {
			t.Fatal("raw post publication")
		}
		if env.Type != protocol.TypeThreadItems || env.InReplyTo == nil || *env.InReplyTo != 3737 {
			continue
		}
		var p protocol.ThreadItemsPayload
		if json.Unmarshal(env.Payload, &p) != nil || p.ConversationID != row.ID {
			t.Fatal("invalid post catch-up")
		}
		for _, item := range p.Items {
			var content struct {
				Text string `json:"text"`
			}
			if item.Kind == "assistant_message" && json.Unmarshal(item.Content, &content) == nil && content.Text == text {
				if found || item.Active {
					t.Fatal("duplicate or incomplete canonical post")
				}
				found = true
			}
		}
		if p.Final {
			break
		}
	}
	if !found {
		t.Fatal("canonical post missing")
	}
}

func TestChannelPost_E2E_HeldUntilRealCompletion(t *testing.T) {
	const convID = "28110000-0000-4000-8000-000000000002"
	const post = "scheduled-secret-post"
	dir := t.TempDir()
	first, second, release := filepath.Join(dir, "first"), filepath.Join(dir, "second"), filepath.Join(dir, "release")
	for path, text := range map[string]string{
		first:  `{"type":"assistant","message":{"id":"head","role":"assistant","content":[{"type":"text","text":"reply-head"}]}}` + "\n" + bgConvFirst,
		second: bgConvSecond,
	} {
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	home := shortHome(t)
	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	relayURL := fr.URL() + "/v2/server"
	payload, err := paireddevice.Setup(paireddevice.Config{Home: home, InstanceName: "test", Relay: relayURL, DeviceName: "phone-a"})
	if err != nil {
		t.Fatal(err)
	}
	pub, err := base64.StdEncoding.DecodeString(payload.ServerStaticPubkey)
	if err != nil {
		t.Fatal(err)
	}
	seedPromotedBoundConversation(t, home, convID, "held", bgConvBootstrapUUID)
	h := StartStreamInteractiveWithRelay(t, home, bgConvBootstrapUUID, relayURL,
		"PYRY_FAKE_CLAUDE_STREAM_REPLAY_FIRST="+first, "PYRY_FAKE_CLAUDE_STREAM_REPLAY_SECOND="+second,
		"PYRY_FAKE_CLAUDE_STREAM_REPLAY_RELEASE="+release)
	t.Cleanup(func() { h.Stop(t) })
	serverID := readPersistedServerID(t, home)
	waitBinaryHello(t, fr, serverID)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	phone, err := fakephone.Dial(ctx, fr.URL(), serverID, payload.Token, "phone-a")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = phone.Close() })
	send, recv := driveHandshakeToOpenDaemonInteractive(t, phone, pub, payload.Token)
	seal, wireNext := sealedConnDriver(t, phone, "phone", send, recv)
	source := scenario.SourceSince(t, home, convID)
	var reply strings.Builder
	var claudeTurn string
	var terminal history.Entry
	var mark uint64
	var toolSeen, running bool
	var idle time.Time
	next := func(deadline time.Time) (protocol.Envelope, bool) {
		poll := time.Now().Add(20 * time.Millisecond)
		if deadline.Before(poll) {
			poll = deadline
		}
		env, _ := wireNext(poll)
		for _, e := range source(convID) {
			if e.ID <= mark {
				continue
			}
			mark = e.ID
			var p struct {
				Conversation string `json:"conversation_id"`
				Turn         string `json:"turn_id"`
				Parent       string `json:"parent_tool_use_id"`
				Text         string `json:"text"`
			}
			if json.Unmarshal(e.Payload, &p) != nil {
				t.Fatal("invalid held-turn source")
			}
			if p.Conversation != convID || p.Parent != "" || p.Turn == "" {
				continue
			}
			if claudeTurn == "" && e.Type == protocol.TypeAssistantDelta {
				claudeTurn = p.Turn
			}
			if p.Turn != claudeTurn {
				continue
			}
			if e.Session == nil || e.Session.SessionID != bgConvBootstrapUUID || e.Session.Kind != "claude" {
				t.Fatal("held-turn source lost producing session")
			}
			switch e.Type {
			case protocol.TypeAssistantDelta:
				reply.WriteString(p.Text)
			case protocol.TypeToolUse:
				toolSeen = true
			case protocol.TypeTurnEnd:
				terminal = e
			}
		}
		if env.Type == protocol.TypeTurnState {
			var p protocol.TurnStatePayload
			if json.Unmarshal(env.Payload, &p) != nil {
				t.Fatal("invalid held-turn live state")
			}
			if p.ConversationID == convID {
				if p.State == "idle" && terminal.ID != 0 && !env.TS.Before(terminal.TS) && idle.IsZero() {
					idle = env.TS
				} else if p.State != "idle" {
					running = true
				}
			}
		}
		switch env.Type {
		case protocol.TypeToolUse, protocol.TypeToolResult, protocol.TypeToolDenied:
			t.Fatal("interactive tool content reached app transport")
		case protocol.TypeAssistantDelta, protocol.TypeTurnEnd, protocol.TypeBanner:
			t.Fatal("retired content reached app transport")
		}
		return env, env.Type != "" || time.Now().Before(deadline)
	}
	seal(protocol.Envelope{ID: 28110, Type: protocol.TypeSendMessage, TS: time.Now().UTC(),
		Payload: mustJSON(t, protocol.SendMessagePayload{ConversationID: convID, MessageID: "held-user", Text: "go"})})
	deadline := time.Now().Add(15 * time.Second)
	for !toolSeen || !running {
		env, ok := next(deadline)
		if !ok {
			t.Fatal("turn never opened")
		}
		if env.Type == protocol.TypeAssistantDelta || env.Type == protocol.TypeToolUse || env.Type == protocol.TypeTurnEnd {
			t.Fatal("interactive setup published retired content")
		}
	}
	p := runVerb(t, h.SocketPath, home, "channel", "post", "--name", "held", "--text", post)
	if p.ExitCode != 0 {
		t.Fatalf("acceptance failed: %s", p.Stderr)
	}
	// The file gate proves the turn cannot finish during this observation window.
	time.Sleep(350 * time.Millisecond)
	seal(protocol.Envelope{ID: 28111, Type: protocol.TypeListConversations, TS: time.Now().UTC(), Payload: json.RawMessage(`{}`)})
	deadline = time.Now().Add(15 * time.Second)
	for {
		env, ok := next(deadline)
		if !ok {
			t.Fatal("held-output barrier absent")
		}
		if env.Type == protocol.TypeAssistantDelta || env.Type == protocol.TypeTurnEnd {
			t.Fatalf("output crossed the held boundary: %s", env.Type)
		}
		if env.Type == protocol.TypeConversations && env.InReplyTo != nil && *env.InReplyTo == 28111 {
			break
		}
	}
	for _, e := range readHistoryEntries(home, convID) {
		if strings.Contains(string(e.Payload), post) || strings.Contains(string(e.Payload), "channel_post") {
			t.Fatal("held post entered served history")
		}
	}
	seal(protocol.Envelope{ID: 28113, Type: protocol.TypeSendMessage, TS: time.Now().UTC(),
		Payload: mustJSON(t, protocol.SendMessagePayload{ConversationID: convID, MessageID: "successor-user", Text: "next"})})
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	ended := false
	var posted protocol.AssistantDeltaPayload
	deadline = time.Now().Add(15 * time.Second)
	for posted.Text == "" {
		_, ok := next(deadline)
		if !ok {
			t.Fatal("released post absent")
		}
		ended = terminal.ID != 0
		for _, entry := range readHistoryEntries(home, convID) {
			if entry.Type != protocol.TypeAssistantDelta {
				continue
			}
			var p protocol.AssistantDeltaPayload
			if json.Unmarshal(entry.Payload, &p) != nil {
				t.Fatal("invalid post source")
			}
			if p.Text != post {
				continue
			}
			if !ended || p.TurnID == claudeTurn || p.Seq != 0 {
				t.Fatal("post crossed completion")
			}
			posted = p
		}
	}
	if reply.String() != "reply-head"+bgConvNeedle {
		t.Fatalf("reply split or changed: %q", reply.String())
	}
	postEnded := false
	deadline = time.Now().Add(15 * time.Second)
	var successor history.Entry
	for !postEnded || successor.ID == 0 || idle.Before(terminal.TS) {
		env, ok := next(deadline)
		if !ok {
			t.Fatal("post completion or successor absent")
		}
		_ = env
		for _, entry := range readHistoryEntries(home, convID) {
			if entry.Type != protocol.TypeTurnEnd {
				continue
			}
			var end channelPostEndIdentity
			if json.Unmarshal(entry.Payload, &end) == nil && end.TurnID == posted.TurnID {
				assertHostPostEnd(t, entry.Payload, convID, posted.TurnID)
				postEnded = true
			}
		}
		for _, e := range source(convID) {
			var p protocol.AssistantDeltaPayload
			if e.Type == protocol.TypeAssistantDelta && json.Unmarshal(e.Payload, &p) == nil && p.TurnID != posted.TurnID && p.TurnID != claudeTurn && p.ParentToolUseID == "" {
				if e.Session == nil || e.Session.SessionID != bgConvBootstrapUUID || p.ConversationID != convID || p.Text == "" {
					t.Fatal("successor lost producing identity/text")
				}
				successor = e
			}
		}
	}
	page := readHistoryEntries(home, convID)
	reply.Reset()
	ended = false
	var postCompletions int
	var postEndID uint64
	for _, e := range page {
		if e.Type == protocol.TypeTurnEnd {
			var p map[string]any
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				t.Fatal(err)
			}
			if p["turn_id"] == posted.TurnID {
				assertHostPostEnd(t, e.Payload, convID, posted.TurnID)
				postCompletions++
				postEndID = e.ID
			}
			ended = true
		}
		if e.Type != protocol.TypeAssistantDelta {
			continue
		}
		var p protocol.AssistantDeltaPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if p.TurnID == claudeTurn {
			reply.WriteString(p.Text)
		}
		if p.TurnID == posted.TurnID && (!ended || p != posted) {
			t.Fatal("history/live post order differs")
		}
	}
	if postCompletions != 1 {
		t.Fatal("served post completion not unique")
	}
	if terminal.ID == 0 || postEndID <= terminal.ID || successor.ID <= postEndID {
		t.Fatal("source order did not preserve reply, post completion and successor")
	}
	if reply.String() != "reply-head"+bgConvNeedle {
		t.Fatal("served history split or changed the reply")
	}
}

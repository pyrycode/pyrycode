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

// TestChannelPost_E2E_ReachesAttachedClient closes the loop #2496 was split for
// (#2498): a cron posts into a channel on the host, and an ALREADY-CONNECTED
// client is told, without a reconnect.
//
// Every piece under it already exists and is covered on its own — the verb and
// its durable record (#2497), the host-side fan-out shape (#2156), the frame type
// (#607/#609) and the interactive grant (#626). What has never run together is
// the join: until this ticket the recorded post reached a connected client as
// nothing at all, and reached a reconnecting one as a `message` entry whose role
// the current clients deliberately draw as null.
//
// THE PHONE IS ATTACHED AND INTERACTIVE BEFORE THE POST RUNS. The frame is
// live-only — no outstanding-post registry and no connect-time replay — so a
// phone that handshakes afterwards is never pushed it, and this ordering is
// load-bearing rather than incidental. The durable half is what serves that
// phone, and it is asserted here too.
//
// THE CHANNEL IS CREATED BY THE POST ITSELF, through the label-misses arm, which
// is the shape a cron's first run actually takes. That also puts a
// conversation_updated on the same wire immediately before the assistant_delta,
// so the read loop below has to classify rather than take the next frame — which
// is the honest condition, not a simplification of it.
//
// Fake-daemon tier (fakeclaude, fakerelay, fakephone; no credentials, no
// network), so `make check` covers the delivery path.
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
	_, recv := driveHandshakeToOpenDaemonInteractive(t, phone, pubKey, payload.Token)

	// ── The post, with the phone already attached ──
	p := runVerb(t, h.SocketPath, home, "channel", "post", "--name", channelName, "--text", text)
	if p.ExitCode != 0 {
		t.Fatalf("pyry channel post exit=%d\nstdout:\n%s\nstderr:\n%s", p.ExitCode, p.Stdout, p.Stderr)
	}

	row := waitForConversationNamed(t, home, channelName)

	// ── The live frame ──
	var delta protocol.AssistantDeltaPayload
	deadline := time.Now().Add(frameDeadline)
	for delta.Text == "" {
		env, ok := nextAttachmentEnvelope(t, phone, recv, deadline)
		if !ok {
			t.Fatal("no assistant_delta arrived after a successful post; a message recorded while a " +
				"client is attached must reach it without a reconnect")
		}
		if env.Type != protocol.TypeAssistantDelta {
			continue // the created channel's conversation_updated, or the bootstrap session's pushes
		}
		// A post carries no in_reply_to: nothing solicits it. Asserting the absence
		// here is what stops a future producer from quietly correlating it to
		// whatever request happened to be in flight.
		if env.InReplyTo != nil {
			t.Errorf("assistant_delta carried in_reply_to = %v; nothing solicits a posted message", *env.InReplyTo)
		}
		if err := json.Unmarshal(env.Payload, &delta); err != nil {
			t.Fatalf("decode assistant_delta payload: %v", err)
		}
	}

	if delta.Text != text {
		t.Errorf("pushed text = %q, want the posted %q", delta.Text, text)
	}
	if delta.ConversationID != row.ID {
		t.Errorf("pushed conversation_id = %q, want the channel the post created %q", delta.ConversationID, row.ID)
	}
	if delta.TurnID == "" {
		t.Error("pushed turn_id is empty; a client coalesces on it and every post would join one bubble")
	}

	// ── The durable half, in the shape that was pushed ──
	//
	// The two legs are produced independently — the push travels the relay and
	// this reads the file — so the claim that they carry ONE payload is a check
	// rather than a tautology. It is also AC#3: exactly one entry, so a client
	// paging history after the fact draws the post once.
	entries := waitForHistoryEntries(t, home, row.ID)
	if len(entries) != 1 {
		t.Fatalf("history holds %d entries, want exactly 1 — one post is one record", len(entries))
	}
	if entries[0].Type != protocol.TypeAssistantDelta {
		t.Errorf("entry type = %q, want %q — the record must be the shape that was pushed",
			entries[0].Type, protocol.TypeAssistantDelta)
	}
	var recorded protocol.AssistantDeltaPayload
	if err := json.Unmarshal(entries[0].Payload, &recorded); err != nil {
		t.Fatalf("decode recorded payload: %v", err)
	}
	if recorded != delta {
		t.Errorf("recorded %+v but pushed %+v; a client that reconnects must read back what a "+
			"connected one was shown", recorded, delta)
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
	seal, next := sealedConnDriver(t, phone, "phone", send, recv)
	seal(protocol.Envelope{ID: 28110, Type: protocol.TypeSendMessage, TS: time.Now().UTC(),
		Payload: mustJSON(t, protocol.SendMessagePayload{ConversationID: convID, MessageID: "held-user", Text: "go"})})
	var reply strings.Builder
	var claudeTurn string
	for {
		env, ok := next(time.Now().Add(15 * time.Second))
		if !ok {
			t.Fatal("turn never opened")
		}
		if env.Type == protocol.TypeAssistantDelta {
			var p protocol.AssistantDeltaPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatal(err)
			}
			reply.WriteString(p.Text)
			claudeTurn = p.TurnID
		}
		if env.Type == protocol.TypeToolUse {
			break
		}
	}
	p := runVerb(t, h.SocketPath, home, "channel", "post", "--name", "held", "--text", post)
	if p.ExitCode != 0 {
		t.Fatalf("acceptance failed: %s", p.Stderr)
	}
	requestHistory := func(req uint64, held bool) protocol.HistoryPagePayload {
		t.Helper()
		seal(protocol.Envelope{ID: req, Type: protocol.TypeRequestHistory, TS: time.Now().UTC(),
			Payload: mustJSON(t, protocol.RequestHistoryPayload{ConversationID: convID})})
		for {
			env, ok := next(time.Now().Add(15 * time.Second))
			if !ok {
				t.Fatal("history page absent")
			}
			if held && (env.Type == protocol.TypeAssistantDelta || env.Type == protocol.TypeTurnEnd) {
				t.Fatalf("output crossed the held boundary: %s", env.Type)
			}
			if env.Type == protocol.TypeHistoryPage {
				var page protocol.HistoryPagePayload
				if err := json.Unmarshal(env.Payload, &page); err != nil {
					t.Fatal(err)
				}
				return page
			}
		}
	}
	// The file gate proves the turn cannot finish during this observation window.
	time.Sleep(350 * time.Millisecond)
	for _, e := range requestHistory(28111, true).Entries {
		if strings.Contains(string(e.Payload), post) {
			t.Fatal("held post entered served history")
		}
	}
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	ended := false
	var posted protocol.AssistantDeltaPayload
	for posted.Text == "" {
		env, ok := next(time.Now().Add(15 * time.Second))
		if !ok {
			t.Fatal("released post absent")
		}
		if env.Type == protocol.TypeTurnEnd {
			ended = true
		}
		if env.Type != protocol.TypeAssistantDelta {
			continue
		}
		var p protocol.AssistantDeltaPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if p.TurnID == claudeTurn {
			reply.WriteString(p.Text)
			continue
		}
		if !ended || p.Text != post || p.Seq != 0 {
			t.Fatalf("post crossed completion: %+v", p)
		}
		posted = p
	}
	if reply.String() != "reply-head"+bgConvNeedle {
		t.Fatalf("reply split or changed: %q", reply.String())
	}
	page := requestHistory(28112, false)
	reply.Reset()
	ended = false
	for i := len(page.Entries) - 1; i >= 0; i-- {
		e := page.Entries[i]
		if e.Type == protocol.TypeTurnEnd {
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
	if reply.String() != "reply-head"+bgConvNeedle {
		t.Fatal("served history split or changed the reply")
	}
}

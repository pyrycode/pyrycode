//go:build e2e

package e2e

import (
	"context"
	"encoding/base64"
	"encoding/json"
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

//go:build e2e

package e2e

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"github.com/pyrycode/pyrycode/internal/devices"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/paireddevice"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/scenario"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func assertHostPostEnd(t *testing.T, raw json.RawMessage, conv, turn string) {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"conversation_id": conv, "turn_id": turn, "stop_reason": "end_turn", "producer": "channel_post"}
	if len(got) != 4 {
		t.Fatal("unexpected host completion keys")
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("incorrect host completion %s", k)
		}
	}
}

func TestChannelPost_E2E_DisconnectedWakeAndHistory(t *testing.T) {
	const convID = "28090000-0000-4000-8000-000000000002"
	const initial = "28090000-0000-4000-8000-000000000001"
	const pushToken = "test-fcm-2809"
	const name = "scheduled"
	// Escaping forces several bounded source records and exercises source order.
	text := strings.Repeat("<雪", 5001)
	home := shortHome(t)
	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	relayURL := fr.URL() + "/v2/server"
	pair, err := paireddevice.Setup(paireddevice.Config{Home: home, InstanceName: "test", Relay: relayURL, DeviceName: "phone-a"})
	if err != nil {
		t.Fatal(err)
	}
	pub, err := base64.StdEncoding.DecodeString(pair.ServerStaticPubkey)
	if err != nil {
		t.Fatal(err)
	}
	registryPath := filepath.Join(home, ".pyry", "test", "devices.json")
	registry, err := devices.Load(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	if !registry.UpdatePushRegistration(devices.HashToken(pair.Token), "fcm", pushToken, "phone-a") {
		t.Fatal("paired device absent")
	}
	if err := registry.Save(registryPath); err != nil {
		t.Fatal(err)
	}

	seedPromotedBoundConversation(t, home, convID, name, initial)
	h := StartStreamInteractiveWithRelay(t, home, initial, relayURL)
	t.Cleanup(func() { h.Stop(t) })
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("daemon diagnostics:\n%s", h.Stderr.String())
		}
	})
	serverID := readPersistedServerID(t, home)
	waitBinaryHello(t, fr, serverID)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	phone, err := fakephone.Dial(ctx, fr.URL(), serverID, pair.Token, "phone-a")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = phone.Close() })
	send, recv := driveHandshakeToOpenDaemonInteractive(t, phone, pub, pair.Token)
	source := scenario.NewSourceObserver(t, home)
	seal, next := sealedConnDriver(t, phone, "phone", send, recv, source)
	// Route a real user message and retain its producing-session source evidence.
	seal(protocol.Envelope{ID: 28090, Type: protocol.TypeSendMessage, TS: time.Now().UTC(), Payload: mustJSON(t, protocol.SendMessagePayload{ConversationID: convID, MessageID: "route", Text: "hello"})})
	deadline := time.Now().Add(10 * time.Second)
	for !source.Ended(convID) {
		_, ok := next(deadline)
		if !ok {
			t.Fatal("routed turn never completed")
		}
	}
	terminal, ok := source.Terminal(convID)
	if !ok || terminal.Session == nil || terminal.Session.SessionID != initial || source.Text(convID) == "" {
		t.Fatal("routed turn lost producing-session content/completion")
	}
	// Observe the routed turn's connected-device suppression before dropping
	// the phone, so its earlier trigger cannot masquerade as the post's wake.
	for !strings.Contains(h.Stderr.String(), "reason=device_connected") {
		select {
		case <-ctx.Done():
			t.Fatal("connected turn did not suppress wake")
		case <-time.After(10 * time.Millisecond):
		}
	}

	if err := phone.Close(); err != nil {
		t.Fatal(err)
	}
	// The relay forwards the peer-close notice before the delivery consumer
	// triggers the wake. A wire wake is the positive proof of absent eligibility.
	for !strings.Contains(h.Stderr.String(), "v2.peer_close.teardown") {
		select {
		case <-ctx.Done():
			t.Fatal("daemon did not consume peer-close notice")
		case <-time.After(10 * time.Millisecond):
		}
	}
	post := runVerb(t, h.SocketPath, home, "channel", "post", "--name", name, "--text", text)
	if post.ExitCode != 0 {
		t.Fatalf("post failed: %s", post.Stderr)
	}
	rawWake, ok := fr.NextPushWake(ctx)
	if !ok {
		t.Fatal("no actual outbound wake request")
	}
	var wake map[string]json.RawMessage
	if err := json.Unmarshal(rawWake, &wake); err != nil {
		t.Fatal(err)
	}
	var body map[string]string
	if len(wake) != 1 || json.Unmarshal(wake["push_wake"], &body) != nil || len(body) != 3 || body["platform"] != "fcm" || body["token"] != pushToken || body["request_id"] == "" {
		t.Fatal("wake carried incorrect or content-bearing fields")
	}
	phone2, err := fakephone.Dial(ctx, fr.URL(), serverID, pair.Token, "phone-a")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = phone2.Close() })
	send2, recv2 := driveHandshakeToOpenDaemonInteractive(t, phone2, pub, pair.Token)
	seal2, next2 := sealedConnDriver(t, phone2, "returning phone", send2, recv2)
	seal2(protocol.Envelope{ID: 3738, Type: protocol.TypeListConversations, TS: time.Now().UTC(), Payload: json.RawMessage(`{}`)})
	for {
		env, ok := next2(time.Now().Add(10 * time.Second))
		if !ok {
			t.Fatal("reconnect barrier missing")
		}
		if env.Type == protocol.TypeAssistantDelta || env.Type == protocol.TypeTurnEnd || env.Type == protocol.TypeBanner {
			t.Fatal("post replayed raw content")
		}
		if env.Type == protocol.TypeConversations && env.InReplyTo != nil && *env.InReplyTo == 3738 {
			break
		}
	}
	var turn string
	var joined strings.Builder
	chunks, completions := 0, 0
	for _, entry := range readHistoryEntries(home, convID) {
		if entry.Type == protocol.TypeAssistantDelta {
			var p protocol.AssistantDeltaPayload
			if json.Unmarshal(entry.Payload, &p) != nil {
				t.Fatal("invalid post source")
			}
			if !strings.Contains(text, p.Text) || p.TurnID == "" {
				continue
			}
			if turn == "" {
				turn = p.TurnID
			}
			if p.TurnID != turn || p.Seq != chunks {
				t.Fatal("post source order changed")
			}
			chunks++
			joined.WriteString(p.Text)
		}
		if entry.Type == protocol.TypeTurnEnd {
			var p channelPostEndIdentity
			if json.Unmarshal(entry.Payload, &p) == nil && p.TurnID == turn && turn != "" {
				assertHostPostEnd(t, entry.Payload, convID, turn)
				completions++
			}
		}
	}
	if chunks < 2 || completions != 1 || joined.String() != text {
		t.Fatal("post source incomplete or duplicated")
	}
	quiet, quietCancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer quietCancel()
	if _, ok := fr.NextPushWake(quiet); ok {
		t.Fatal("reconnect woke a device again")
	}
}

type channelPostEndIdentity struct {
	TurnID string `json:"turn_id"`
}

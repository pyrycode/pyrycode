//go:build e2e

package e2e

import (
	"bytes"
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

func TestChannelPost_E2E_DisconnectedWakeReplayAndHistory(t *testing.T) {
	const convID = "28090000-0000-4000-8000-000000000002"
	const initial = "28090000-0000-4000-8000-000000000001"
	const pushToken = "test-fcm-2809"
	const name = "scheduled"
	// Escaping forces several bounded envelopes and exercises ordered replay.
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
	seal, next := sealedConnDriver(t, phone, "phone", send, recv)
	// Route a real user message: replay uses the daemon's cursor, not a client
	// conversation selection or the new channel's name.
	seal(protocol.Envelope{ID: 28090, Type: protocol.TypeSendMessage, TS: time.Now().UTC(), Payload: mustJSON(t, protocol.SendMessagePayload{ConversationID: convID, MessageID: "route", Text: "hello"})})
	var cursor uint64
	for cursor == 0 {
		env, ok := next(time.Now().Add(10 * time.Second))
		if !ok {
			t.Fatal("routed turn never completed")
		}
		if env.Type == protocol.TypeTurnEnd && env.EventID != nil {
			cursor = *env.EventID
		}
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
	if len(wake) != 1 || json.Unmarshal(wake["push_wake"], &body) != nil || len(body) != 2 || body["platform"] != "fcm" || body["token"] != pushToken {
		t.Fatal("wake carried incorrect or content-bearing fields")
	}
	phone2, err := fakephone.Dial(ctx, fr.URL(), serverID, pair.Token, "phone-a")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = phone2.Close() })
	send2, recv2 := driveHandshakeToOpenDaemonInteractiveResuming(t, phone2, pub, pair.Token, &cursor)
	seal2, next2 := sealedConnDriver(t, phone2, "returning phone", send2, recv2)
	var deltas []protocol.Envelope
	var completion protocol.Envelope
	var turn string
	var joined strings.Builder
	lastID := cursor
	for completion.Type == "" {
		env, ok := next2(time.Now().Add(10 * time.Second))
		if !ok {
			t.Fatal("completed post not replayed")
		}
		if env.Type != protocol.TypeAssistantDelta && env.Type != protocol.TypeTurnEnd {
			continue
		}
		if env.EventID == nil || *env.EventID <= lastID {
			t.Fatal("replay event ids absent or unordered")
		}
		lastID = *env.EventID
		if env.Type == protocol.TypeTurnEnd {
			assertHostPostEnd(t, env.Payload, convID, turn)
			completion = env
			break
		}
		var p protocol.AssistantDeltaPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if turn == "" {
			turn = p.TurnID
		}
		if p.ConversationID != convID || p.TurnID != turn || p.Seq != len(deltas) {
			t.Fatal("replayed post split or reordered")
		}
		joined.WriteString(p.Text)
		deltas = append(deltas, env)
	}
	if len(deltas) < 2 || joined.String() != text {
		t.Fatal("chunked replay changed text")
	}
	seal2(protocol.Envelope{ID: 28092, Type: protocol.TypeRequestHistory, TS: time.Now().UTC(), Payload: mustJSON(t, protocol.RequestHistoryPayload{ConversationID: convID})})
	for {
		env, ok := next2(time.Now().Add(10 * time.Second))
		if !ok {
			t.Fatal("served history absent")
		}
		if env.Type != protocol.TypeHistoryPage {
			continue
		}
		var page protocol.HistoryPagePayload
		if err := json.Unmarshal(env.Payload, &page); err != nil {
			t.Fatal(err)
		}
		var recorded []protocol.HistoryEntry
		for i := len(page.Entries) - 1; i >= 0; i-- {
			var identity struct {
				TurnID string `json:"turn_id"`
			}
			if err := json.Unmarshal(page.Entries[i].Payload, &identity); err != nil {
				t.Fatal(err)
			}
			if identity.TurnID == turn {
				recorded = append(recorded, page.Entries[i])
			}
		}
		if len(recorded) != len(deltas)+1 {
			t.Fatal("served history duplicates or omits post events")
		}
		for i, delta := range deltas {
			if recorded[i].Type != delta.Type || !bytes.Equal(recorded[i].Payload, delta.Payload) {
				t.Fatal("history and replay differ")
			}
		}
		assertHostPostEnd(t, recorded[len(deltas)].Payload, convID, turn)
		break
	}
	quiet, quietCancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer quietCancel()
	if _, ok := fr.NextPushWake(quiet); ok {
		t.Fatal("replay woke a device again")
	}
}

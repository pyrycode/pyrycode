//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
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

// TestRelayV2_LiveHistoryEntryID joins a producer's direct push to its source
// history entry over a real Noise session. Seed only prior traffic, then drive
// the operator producer through send_message instead of seeding the event tested.
func TestRelayV2_LiveHistoryEntryID(t *testing.T) {
	const (
		initialUUID = "11111111-1111-4111-8111-111111111111"
		convID      = "77777777-7777-4777-8777-777777777777"
		messageID   = "live-history-2861"
		text        = "match this live operator turn to durable history"
	)
	home := shortHome(t)
	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	relayURL := fr.URL() + "/v2/server"
	pair, err := paireddevice.Setup(paireddevice.Config{
		Home: home, InstanceName: "test", Relay: relayURL, DeviceName: "phone-a",
	})
	if err != nil {
		t.Fatalf("setup paired device: %v", err)
	}
	pubKey, err := base64.StdEncoding.DecodeString(pair.ServerStaticPubkey)
	if err != nil {
		t.Fatal(err)
	}
	seedBoundConversation(t, home, convID, initialUUID)
	store := history.New(filepath.Join(home, ".pyry", "test"))
	// History survives starts; ring/envelope counters start afresh. A large offset
	// makes either ID substitution fail, including startup frames on this session.
	for range 100 {
		if _, err := store.Append(conversations.ConversationID(convID), protocol.TypeMessage, json.RawMessage(`{}`), time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	h := StartStreamInteractiveWithRelay(t, home, initialUUID, relayURL)
	t.Cleanup(func() { h.Stop(t) })
	serverID := readPersistedServerID(t, home)
	waitBinaryHello(t, fr, serverID)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	phone, err := fakephone.Dial(ctx, fr.URL(), serverID, pair.Token, "phone-a")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = phone.Close() })
	send, recv := driveHandshakeToOpenDaemonInteractive(t, phone, pubKey, pair.Token)
	sendSealedEnvelope(t, phone, send, protocol.Envelope{
		ID: 28610, Type: protocol.TypeSendMessage, TS: time.Now().UTC(),
		Payload: mustJSON(t, protocol.SendMessagePayload{ConversationID: convID, MessageID: messageID, Text: text}),
	})
	var live protocol.Envelope
	deadline := time.Now().Add(15 * time.Second)
	for {
		env, ok := nextAttachmentEnvelope(t, phone, recv, deadline)
		if !ok {
			t.Fatal("direct live operator message did not arrive")
		}
		if env.Type != protocol.TypeMessage {
			continue
		}
		var msg protocol.MessagePayload
		if err := json.Unmarshal(env.Payload, &msg); err != nil {
			t.Fatal(err)
		}
		if msg.ConversationID == convID && msg.MessageID == messageID && msg.Role == "user" && msg.Text == text {
			live = env
			break
		}
	}
	if live.HistoryEntryID == nil || *live.HistoryEntryID <= 100 {
		t.Fatalf("history_entry_id=%v, want the new durable entry after the seeds", live.HistoryEntryID)
	}
	if live.EventID == nil || *live.HistoryEntryID == *live.EventID || *live.HistoryEntryID == live.ID {
		t.Fatalf("fixture must distinguish history, ring and envelope ids: %+v", live)
	}
	matches := 0
	for _, entry := range readHistoryEntries(home, convID) {
		if entry.Type == live.Type && bytes.Equal(entry.Payload, live.Payload) && entry.TS.Equal(live.TS) {
			matches++
			if entry.ID != *live.HistoryEntryID {
				t.Fatalf("served entry id=%d, live history_entry_id=%d", entry.ID, *live.HistoryEntryID)
			}
		}
	}
	if matches != 1 {
		t.Fatalf("found %d served entries matching live type, payload and timestamp, want one", matches)
	}
}

func sendSealedEnvelope(t *testing.T, phone *fakephone.Client, send *noise.CipherState, env protocol.Envelope) {
	t.Helper()
	envBytes, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal %s envelope %d: %v", env.Type, env.ID, err)
	}
	ciphertext, err := send.Encrypt(envBytes)
	if err != nil {
		t.Fatalf("seal %s envelope %d: %v", env.Type, env.ID, err)
	}
	sendNoiseMsg(t, phone, ciphertext)
}

//go:build e2e

package e2e

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
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

// TestRelayV2_StreamMidTurnHoldDropAndDrainInOrder keeps three canonical queued
// items held behind a running turn, drops one before release and proves FIFO
// delivery of the surviving messages through the real daemon and fake child.
func TestRelayV2_StreamMidTurnHoldDropAndDrainInOrder(t *testing.T) {
	const (
		initialUUID  = "11111111-1111-4111-8111-111111111111"
		knownConvID  = "33333333-3333-4333-8333-333333333333"
		msg1Marker   = "e2e-1138-msg1"
		msg2Marker   = "e2e-1138-msg2"
		msg3Marker   = "e2e-1138-msg3"
		msg4Marker   = "e2e-1199-msg4" // the one dropped before it drains
		reqID1       = uint64(11381)
		reqID4       = uint64(11384)
		dequeueReqID = uint64(11991)
	)

	home := shortHome(t)
	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	relayURL := fr.URL() + "/v2/server"

	// Pair one interactive device.
	payload, err := paireddevice.Setup(paireddevice.Config{
		Home:                   home,
		InstanceName:           "test",
		Relay:                  relayURL,
		DeviceName:             "phone-a",
		AllowRemotePermissions: false,
	})
	if err != nil {
		t.Fatalf("setup paired device: %v", err)
	}
	pubKey, err := base64.StdEncoding.DecodeString(payload.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	// Bind knownConvID → the bootstrap session (initialUUID). sessionRouter.Route
	// resolves knownConvID past the #678 empty-CSID guard, and the drain gate's
	// activeSession() == the sink tag initialUUID. Loaded once at startup, so the row
	// must exist BEFORE the daemon starts.
	seedBoundConversation(t, home, knownConvID, initialUUID)

	// The child holds at startup until holdPath appears (not created yet), so the
	// first send's turn is a long one and the rest are held in the daemon's backlog;
	// dropping the trigger lets the child start consuming. No SESSIONS_DIR /
	// STDIN_LOG env — stream mode opens no transcript and this test's oracles are the
	// canonical thread stream, not a stdin log.
	holdPath := filepath.Join(home, "stream-hold.trig")
	h := StartStreamInteractiveWithRelay(t, home, initialUUID, relayURL,
		"PYRY_FAKE_CLAUDE_STREAM_HOLD="+holdPath)
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
	// Interactive — the capability the structured stream requires.
	sendCS, recvCS := driveHandshakeToOpenDaemonInteractive(t, phone, pubKey, payload.Token, protocol.CapabilityThread)

	sealSend := func(env protocol.Envelope) {
		t.Helper()
		raw, err := json.Marshal(env)
		if err != nil {
			t.Fatalf("marshal envelope: %v", err)
		}
		ciphertext, err := sendCS.Encrypt(raw)
		if err != nil {
			t.Fatalf("seal envelope: %v", err)
		}
		sendNoiseMsg(t, phone, ciphertext)
	}

	// nextEnv decrypts the next binary→phone application envelope, skipping
	// non-noise_msg inner frames. Every noise_msg is decrypted in capture order so the
	// receive nonce stays in sequence. ok=false on deadline.
	nextEnv := func(deadline time.Time) (protocol.Envelope, bool) {
		t.Helper()
		for {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return protocol.Envelope{}, false
			}
			raw, err := phone.ReceiveBytes(remaining)
			if err != nil {
				if errors.Is(err, fakephone.ErrReceiveTimeout) {
					return protocol.Envelope{}, false
				}
				t.Fatalf("phone receive: %v", err)
			}
			var inner protocol.InnerFrameV2
			if err := json.Unmarshal(raw, &inner); err != nil {
				t.Fatalf("decode inner frame: %v", err)
			}
			if inner.Type != protocol.TypeNoiseMsg {
				continue
			}
			return decryptInnerEnvelope(t, inner, recvCS), true
		}
	}

	request := uint64(50000)
	next := scenario.QueuePlacementFrames(t, scenario.Wire{
		Send: func(kind string, payload any) uint64 {
			request++
			sealSend(protocol.Envelope{ID: request, Type: kind, TS: time.Now().UTC(), Payload: mustJSON(t, payload)})
			return request
		},
		Next: func(deadline time.Time) protocol.Envelope { env, _ := nextEnv(deadline); return env },
	}, knownConvID)
	for i, text := range []string{msg1Marker, msg2Marker, msg3Marker, msg4Marker} {
		sealSend(protocol.Envelope{ID: reqID1 + uint64(i), Type: protocol.TypeSendMessage, TS: time.Now().UTC(),
			Payload: mustJSON(t, protocol.SendMessagePayload{ConversationID: knownConvID, MessageID: text, Text: text})})
	}
	acked := map[uint64]bool{}
	held := map[string]scenario.QueuePlacementFrame{}
	deadline := time.Now().Add(20 * time.Second)
	for len(acked) < 4 || len(held) < 3 {
		f, ok := next(deadline)
		if !ok {
			t.Fatalf("held backlog incomplete: %d acks, %d queued items", len(acked), len(held))
		}
		if f.Kind == "ack" && f.RequestID >= reqID1 && f.RequestID <= reqID4 {
			acked[f.RequestID] = true
		}
		if f.Kind == "delta" || f.Kind == "turn_end" {
			t.Fatal("held turn completed before release")
		}
		if (f.Text == msg2Marker || f.Text == msg3Marker || f.Text == msg4Marker) && f.Status != "queued" {
			t.Fatal("backlog item left the queue before the joint hold checkpoint")
		}
		if f.Kind == "queued_item" && (f.Text == msg2Marker || f.Text == msg3Marker || f.Text == msg4Marker) {
			held[f.Text] = f
		}
	}
	drop := held[msg4Marker]
	sealSend(protocol.Envelope{ID: dequeueReqID, Type: protocol.TypeDequeueMessage, TS: time.Now().UTC(),
		Payload: mustJSON(t, protocol.DequeueMessagePayload{ConversationID: knownConvID, QueuedMsgID: drop.QueuedID, AcceptedItemID: json.RawMessage(fmt.Sprint(drop.ItemID))})})
	deadline = time.Now().Add(15 * time.Second)
	for {
		f, ok := next(deadline)
		if !ok {
			t.Fatal("dropped thread item not observed before release")
		}
		if f.Kind == "delta" || f.Kind == "turn_end" {
			t.Fatal("turn completed before drop checkpoint")
		}
		if f.ItemID == held[msg2Marker].ItemID || f.ItemID == held[msg3Marker].ItemID {
			if f.Status != "queued" {
				t.Fatal("dequeue changed a surviving held item")
			}
		}
		if f.ItemID == drop.ItemID && f.Status == "dropped" {
			if f.Active || f.Shown || f.Order != 0 {
				t.Fatal("dropped item remained active, shown or ordered")
			}
			break
		}
	}
	if err := os.WriteFile(holdPath, []byte("go\\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	want := []string{msg1Marker, msg2Marker, msg3Marker}
	var deltas []string
	ends := 0
	deadline = time.Now().Add(20 * time.Second)
	for len(deltas) < 3 || ends < 3 {
		f, ok := next(deadline)
		if !ok {
			t.Fatalf("drain incomplete: %d deltas, %d ends", len(deltas), ends)
		}
		if f.Kind == "turn_end" {
			ends++
		}
		if f.Kind != "delta" {
			continue
		}
		if len(deltas) >= len(want) || strings.Contains(f.Text, msg4Marker) || !strings.Contains(f.Text, want[len(deltas)]) {
			t.Fatalf("FIFO/drop violated: %q after %v", f.Text, deltas)
		}
		deltas = append(deltas, f.Text)
	}
	settle := time.Now().Add(250 * time.Millisecond)
	for {
		f, ok := next(settle)
		if !ok {
			break
		}
		if f.Kind == "delta" || f.Kind == "turn_end" {
			t.Fatal("late duplicate or dropped turn reached the app")
		}
	}
}

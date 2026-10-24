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
	"github.com/pyrycode/pyrycode/internal/e2e/internal/scenario"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func TestRelayV2_AgentContentRetiredReconnect(t *testing.T) {
	const sid = "37350000-0000-4000-8000-000000000010"
	const conv = "37350000-0000-4000-8000-000000000011"
	home := shortHome(t)
	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	relayURL := fr.URL() + "/v2/server"
	paired, err := paireddevice.Setup(paireddevice.Config{Home: home, InstanceName: "test", Relay: relayURL, DeviceName: "phone"})
	if err != nil {
		t.Fatal(err)
	}
	pub, err := base64.StdEncoding.DecodeString(paired.ServerStaticPubkey)
	if err != nil {
		t.Fatal(err)
	}
	seedBoundConversation(t, home, conv, sid)
	h := StartStreamInteractiveWithRelay(t, home, sid, relayURL, "PYRY_FAKE_CLAUDE_STREAM_ROSTER=1", "PYRY_FAKE_CLAUDE_STREAM_INTERRUPT=1")
	t.Cleanup(func() { h.Stop(t) })
	serverID := readPersistedServerID(t, home)
	waitBinaryHello(t, fr, serverID)
	source := scenario.NewSourceObserver(t, home)
	retained := false
	source.OnEntry = func(entry history.Entry) {
		if entry.Type != protocol.TypeBackgroundTaskRoster {
			return
		}
		var p protocol.BackgroundTaskRosterPayload
		if err := json.Unmarshal(entry.Payload, &p); err != nil {
			t.Fatal(err)
		}
		retained = p.ConversationID == conv && len(p.Tasks) != 0
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	first, err := fakephone.Dial(ctx, fr.URL(), serverID, paired.Token, "producer")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	send, recv := driveHandshakeToOpenDaemonInteractive(t, first, pub, paired.Token)
	seal, next := sealedConnDriver(t, first, "producer", send, recv, source)
	seal(protocol.Envelope{ID: 1, Type: protocol.TypeSendMessage, TS: time.Now().UTC(), Payload: mustJSON(t, protocol.SendMessagePayload{ConversationID: conv, MessageID: "hold", Text: "hold\n"})})
	deadline := time.Now().Add(15 * time.Second)
	for !retained {
		env, ok := next(deadline)
		if !ok || env.Type == protocol.TypeError {
			t.Fatal("task roster source not retained")
		}
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	for i, caps := range [][]string{{protocol.CapabilityInteractive}, {protocol.CapabilityInteractive, protocol.CapabilityThread}} {
		// Peer teardown can cancel accepted ciphertext and retire the binary
		// socket. Cross that transport lifetime explicitly before reconnecting.
		if !fr.ReconnectBinary(ctx, serverID) {
			t.Fatalf("binary reconnect did not complete: %v\n%s", ctx.Err(), h.Stderr.String())
		}
		phone, err := fakephone.Dial(ctx, fr.URL(), serverID, paired.Token, "reconnect")
		if err != nil {
			t.Fatalf("reconnect dial: %v\n%s", err, h.Stderr.String())
		}
		t.Cleanup(func() { _ = phone.Close() })
		send, recv := driveHandshakeToOpenDaemonInteractive(t, phone, pub, paired.Token, caps...)
		seal, next := sealedConnDriver(t, phone, "reconnect", send, recv)
		request := uint64(10 + i)
		seal(protocol.Envelope{ID: request, Type: protocol.TypeListConversations, TS: time.Now().UTC(), Payload: json.RawMessage(`{}`)})
		deadline := time.Now().Add(10 * time.Second)
		for {
			env, ok := next(deadline)
			if !ok || env.Type == protocol.TypeError {
				t.Fatal("ordered reconnect barrier failed")
			}
			if env.Type == protocol.TypeBackgroundTaskStarted || env.Type == protocol.TypeBackgroundTaskUpdated || env.Type == protocol.TypeBackgroundTaskRoster || env.Type == protocol.TypeBanner {
				t.Fatal("retired content on reconnect: ", env.Type)
			}
			if env.Type == protocol.TypeConversations && env.InReplyTo != nil && *env.InReplyTo == request {
				break
			}
		}
		if i == 1 {
			request = 20
			seal(protocol.Envelope{ID: request, Type: protocol.TypeThreadCatchUp, TS: time.Now().UTC(), Payload: mustJSON(t, protocol.ThreadCatchUpPayload{ConversationID: conv, Limit: 100})})
			deadline = time.Now().Add(10 * time.Second)
			for {
				env, ok := next(deadline)
				if !ok || env.Type == protocol.TypeError {
					var p protocol.ErrorPayload
					_ = json.Unmarshal(env.Payload, &p)
					t.Fatalf("explicit catch-up failed: type=%q code=%q\n%s", env.Type, p.Code, h.Stderr.String())
				}
				if env.Type != protocol.TypeThreadItems || env.InReplyTo == nil || *env.InReplyTo != request {
					continue
				}
				var p protocol.ThreadItemsPayload
				if err := json.Unmarshal(env.Payload, &p); err != nil {
					t.Fatal(err)
				}
				if p.ConversationID != conv {
					t.Fatal("catch-up crossed conversation")
				}
				if p.Final {
					break
				}
			}
		}
		if err := phone.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

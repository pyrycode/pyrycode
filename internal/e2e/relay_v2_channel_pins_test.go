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
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func TestRelayV2_ChannelPins(t *testing.T) {
	testRelayV2ChannelPins(t, false)
}

func TestRelayV2_ChannelPinsReplyOrdering(t *testing.T) {
	testRelayV2ChannelPins(t, true)
}

func testRelayV2ChannelPins(t *testing.T, requireReplyFirst bool) {
	t.Helper()
	const convID = "77777777-7777-4777-8777-777777777777"
	const bootstrap = "11111111-1111-4111-8111-111111111111"
	home := shortHome(t)
	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	relayURL := fr.URL() + "/v2/server"
	var tokens [2]string
	var pub []byte
	for i, name := range []string{"phone-a", "phone-b"} {
		pair, err := paireddevice.Setup(paireddevice.Config{Home: home, InstanceName: "test", Relay: relayURL, DeviceName: name})
		if err != nil {
			t.Fatal(err)
		}
		tokens[i] = pair.Token
		pub, err = base64.StdEncoding.DecodeString(pair.ServerStaticPubkey)
		if err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(home, ".pyry", "test", "conversations.json")
	reg, err := conversations.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	reg.Create(conversations.Conversation{ID: convID, Cwd: home, IsPromoted: true, LastUsedAt: time.Now().UTC()})
	if err := reg.Save(path); err != nil {
		t.Fatal(err)
	}
	var stamp *time.Time
	var firstStamp time.Time
	var requestID uint64 = 3538
	for cycle := range 3 {
		var h *Harness
		if cycle == 0 {
			h = StartStreamInteractiveWithRelay(t, home, bootstrap, relayURL)
		} else {
			h = RestartStreamInteractiveWithRelay(t, home, bootstrap, relayURL)
		}
		serverID := readPersistedServerID(t, home)
		waitBinaryHello(t, fr, serverID)
		var phones [2]*fakephone.Client
		var send [2]func(protocol.Envelope)
		var next [2]func(time.Time) (protocol.Envelope, bool)
		for i, name := range []string{"phone-a", "phone-b"} {
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			phone, err := fakephone.Dial(ctx, fr.URL(), serverID, tokens[i], name)
			cancel()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = phone.Close() })
			phones[i] = phone
			s, r := driveHandshakeToOpenDaemonInteractive(t, phone, pub, tokens[i])
			send[i], next[i] = sealedConnDriver(t, phone, name, s, r)
		}
		var earlyPushes [2][]protocol.Envelope
		await := func(peer int, reply bool, id uint64, typ string) protocol.Envelope {
			t.Helper()
			if !reply && len(earlyPushes[peer]) > 0 {
				e := earlyPushes[peer][0]
				earlyPushes[peer] = earlyPushes[peer][1:]
				return e
			}
			deadline := time.Now().Add(10 * time.Second)
			for {
				e, ok := next[peer](deadline)
				if !ok {
					t.Fatalf("cycle %d peer %d: missing %s reply=%v", cycle, peer, typ, reply)
				}
				if e.InReplyTo != nil && *e.InReplyTo == id {
					if !reply || e.Type != typ {
						t.Fatalf("unexpected correlated response: %+v", e)
					}
					return e
				}
				if e.Type == protocol.TypeConversationUpdated {
					var p protocol.ConversationUpdatedPayload
					if err := json.Unmarshal(e.Payload, &p); err != nil {
						t.Fatal(err)
					}
					if p.ID == convID {
						if e.InReplyTo != nil || reply && requireReplyFirst {
							t.Fatal("pin push preceded correlated reply or carried correlation")
						}
						if reply {
							earlyPushes[peer] = append(earlyPushes[peer], e)
							continue
						}
						return e
					}
				}
			}
		}
		// A list from each fresh authenticated connection proves startup loaded
		// the previous daemon's state, independently of the mutation replies.
		for i := range phones {
			requestID++
			send[i](protocol.Envelope{ID: requestID, Type: protocol.TypeListConversations, TS: time.Now().UTC(), Payload: json.RawMessage(`{}`)})
			e := await(i, true, requestID, protocol.TypeConversations)
			var p protocol.ConversationsPayload
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, row := range p.Conversations {
				if row.ID == convID {
					found = true
					assertChannelPin(t, row.PinnedAt, stamp)
				}
			}
			if !found {
				t.Fatal("restarted list lost channel")
			}
		}
		pinned := cycle != 1
		for repeat := range 2 {
			requestID++
			// Literal public keys keep the request independent of the DTO tags.
			payload := json.RawMessage(`{"conversation_id":"` + convID + `","pinned":false}`)
			if pinned {
				payload = json.RawMessage(`{"conversation_id":"` + convID + `","pinned":true}`)
			}
			send[0](protocol.Envelope{ID: requestID, Type: protocol.TypeSetConversationPinned, TS: time.Now().UTC(), Payload: payload})
			reply := await(0, true, requestID, protocol.TypeConversationUpdated)
			var updated protocol.ConversationUpdatedPayload
			if err := json.Unmarshal(reply.Payload, &updated); err != nil {
				t.Fatal(err)
			}
			if updated.ID != convID {
				t.Fatal("reply addressed wrong conversation")
			}
			if pinned && repeat == 0 {
				if updated.PinnedAt == nil || updated.PinnedAt.IsZero() {
					t.Fatal("pin reply missing timestamp")
				}
				stamp = updated.PinnedAt
				if cycle == 0 {
					firstStamp = *stamp
				} else if !stamp.After(firstStamp) {
					t.Fatal("re-pin did not replace cleared timestamp")
				}
			} else if !pinned {
				stamp = nil
			}
			assertChannelPin(t, updated.PinnedAt, stamp)
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(reply.Payload, &fields); err != nil {
				t.Fatal(err)
			}
			if !pinned && string(fields["pinned_at"]) != "null" {
				t.Fatalf("unpinned reply field = %s, want null", fields["pinned_at"])
			}
			for i := range phones {
				push := await(i, false, requestID, protocol.TypeConversationUpdated)
				if !bytes.Equal(push.Payload, reply.Payload) {
					t.Fatalf("peer %d push differs from reply: %s / %s", i, push.Payload, reply.Payload)
				}
			}
			loaded, err := conversations.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			row, ok := loaded.Get(convID)
			if !ok {
				t.Fatal("reload lost channel")
			}
			assertChannelPin(t, row.PinnedAt, stamp)
		}
		for _, phone := range phones {
			_ = phone.Close()
		}
		h.Stop(t)
	}
}

func assertChannelPin(t *testing.T, got, want *time.Time) {
	t.Helper()
	if (got == nil) != (want == nil) || got != nil && !got.Equal(*want) {
		t.Fatalf("pin = %v, want %v", got, want)
	}
}

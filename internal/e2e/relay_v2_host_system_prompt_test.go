//go:build e2e

package e2e

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/paireddevice"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func TestRelayV2_HostSystemPrompt(t *testing.T) {
	const initialUUID = "11111111-1111-4111-8111-111111111111"
	const custom = "HOST-PRIVATE-2768-SENTINEL \t规则\r\n"
	home := shortHome(t)
	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	relayURL := fr.URL() + "/v2/server"
	tokens := make([]string, 2)
	var pubKey []byte
	for i, name := range []string{"requester", "bystander"} {
		p, err := paireddevice.Setup(paireddevice.Config{Home: home, InstanceName: "test", Relay: relayURL, DeviceName: name})
		if err != nil {
			t.Fatal(err)
		}
		tokens[i] = p.Token
		pubKey, err = base64.StdEncoding.DecodeString(p.ServerStaticPubkey)
		if err != nil {
			t.Fatal(err)
		}
	}
	var defaultText string
	var reqID uint64 = 2768
	for stage, want := range []string{"", custom, ""} {
		var h *Harness
		if stage == 0 {
			h = StartStreamInteractiveWithRelay(t, home, initialUUID, relayURL)
		} else {
			h = RestartStreamInteractiveWithRelay(t, home, initialUUID, relayURL)
		}
		t.Cleanup(func() { h.Stop(t) })
		serverID := readPersistedServerID(t, home)
		waitBinaryHello(t, fr, serverID)
		seals := make([]func(protocol.Envelope), 2)
		nexts := make([]func(time.Time) (protocol.Envelope, bool), 2)
		phones := make([]*fakephone.Client, 2)
		for i, name := range []string{"requester", "bystander"} {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			phone, err := fakephone.Dial(ctx, fr.URL(), serverID, tokens[i], name)
			cancel()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = phone.Close() })
			phones[i] = phone
			// This handshake advertises no interactive capability. No conversation is created.
			send, recv := driveHandshakeToOpenDaemon(t, phone, pubKey, tokens[i])
			seals[i], nexts[i] = noiseWire(t, phone, send, recv)
		}
		request := func(client int, verb string, payload any) protocol.HostSystemPromptPayload {
			t.Helper()
			reqID++
			seals[client](protocol.Envelope{ID: reqID, Type: verb, TS: time.Now().UTC(), Payload: mustJSON(t, payload)})
			env, ok := nexts[client](time.Now().Add(3 * time.Second))
			if !ok || env.Type != protocol.TypeHostSystemPrompt || env.InReplyTo == nil || *env.InReplyTo != reqID {
				t.Fatalf("uncorrelated host reply: %+v", env)
			}
			var p protocol.HostSystemPromptPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatal(err)
			}
			var keys map[string]json.RawMessage
			if err := json.Unmarshal(env.Payload, &keys); err != nil {
				t.Fatal(err)
			}
			if len(keys) != 2 || keys["system_prompt"] == nil || keys["default_system_prompt"] == nil {
				t.Fatal("missing reply key")
			}
			return p
		}
		read := request(0, protocol.TypeRequestHostSystemPrompt, protocol.RequestHostSystemPromptPayload{})
		if stage == 0 {
			defaultText = read.DefaultSystemPrompt
			if defaultText == "" {
				t.Fatal("fresh default empty")
			}
			want = defaultText
		}
		if read.SystemPrompt != want || read.DefaultSystemPrompt != defaultText {
			t.Fatal("restart read differs")
		}
		// A read on the other connection is a barrier: any broadcast would arrive
		// first and fail its request-ID assertion. The same barrier follows the write.
		if p := request(1, protocol.TypeRequestHostSystemPrompt, protocol.RequestHostSystemPromptPayload{}); p != read {
			t.Fatal("clients see different settings")
		}
		text := custom
		if stage == 1 {
			text = ""
		}
		if stage == 2 {
			text = defaultText
		}
		written := request(0, protocol.TypeSetHostSystemPrompt, protocol.SetHostSystemPromptPayload{SystemPrompt: &text})
		if written.SystemPrompt != text || written.DefaultSystemPrompt != defaultText {
			t.Fatal("write reply differs")
		}
		if p := request(1, protocol.TypeRequestHostSystemPrompt, protocol.RequestHostSystemPromptPayload{}); p != written {
			t.Fatal("write not visible to next read")
		}
		// Also catch duplicate success replies queued on the requester.
		if p := request(0, protocol.TypeRequestHostSystemPrompt, protocol.RequestHostSystemPromptPayload{}); p != written {
			t.Fatal("requester read differs")
		}
		for _, phone := range phones {
			_ = phone.Close()
		}
		h.Stop(t)
		if strings.Contains(h.Stderr.String()+h.Stdout.String(), custom) || strings.Contains(h.Stderr.String(), "HOST-PRIVATE-2768-SENTINEL") {
			t.Fatal("daemon log exposed instructions")
		}
	}
}

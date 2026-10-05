//go:build e2e

package e2e

import (
	"bytes"
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

// claude's prompt_suggestion line, after a successful result, becomes a broadcast
// reply_suggestion; an accepted send_message clears it with an explicit null; a client
// connecting later receives the clear, never the old text. fakeclaude's replay rider
// supplies the first turn verbatim (no placeholder substitution); the second turn gets
// the canned echo, which carries no suggestion.
const (
	rsBootstrapUUID = "28310000-0000-4000-8000-000000000001"
	rsConvID        = "28310000-0000-4000-8000-000000000002"
	// A second conversation bound to a session no child runs: no suggestion may name it.
	rsOtherConvID    = "28310000-0000-4000-8000-000000000003"
	rsOtherSessionID = "28310000-0000-4000-8000-000000000004"
	rsNeedle         = "e2e-reply-suggestion:replayed-reply"
	rsSuggestion     = "e2e-reply-suggestion:run the tests next"
	rsReplayEnv      = "PYRY_FAKE_CLAUDE_STREAM_REPLAY_FIRST"
)

func rsFragment() string {
	return strings.Join([]string{
		`{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"` + rsNeedle + `"}]}}`,
		`{"type":"result","subtype":"success","is_error":false,"session_id":"fake-stream"}`,
		`{"type":"prompt_suggestion","suggestion":"` + rsSuggestion + `","uuid":"6a1c3f0e-2b4d-4e8f-9a7b-1c2d3e4f5a6b","session_id":"fake-stream"}`,
	}, "\n") + "\n"
}

func TestRelayV2_StreamReplySuggestionSetClearReconnect(t *testing.T) {
	fragmentPath := filepath.Join(t.TempDir(), "reply-suggestion.jsonl")
	if err := os.WriteFile(fragmentPath, []byte(rsFragment()), 0o600); err != nil {
		t.Fatalf("write replay fragment: %v", err)
	}

	home := shortHome(t)
	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	relayURL := fr.URL() + "/v2/server"

	// All devices paired before the daemon starts: it loads its registries once.
	names := []string{"phone-a", "phone-b", "phone-c"}
	tokens := make([]string, 0, len(names))
	var staticPub string
	for _, name := range names {
		p, err := paireddevice.Setup(paireddevice.Config{
			Home: home, InstanceName: "test", Relay: relayURL, DeviceName: name,
		})
		if err != nil {
			t.Fatalf("setup paired device %s: %v", name, err)
		}
		tokens = append(tokens, p.Token)
		staticPub = p.ServerStaticPubkey
	}
	pubKey, err := base64.StdEncoding.DecodeString(staticPub)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	// The driven conversation takes the bootstrap session; the other is bound elsewhere.
	instDir := filepath.Join(home, ".pyry", "test")
	convJSON := []byte(`{"conversations":[` +
		`{"id":"` + rsConvID + `","cwd":"` + home + `","current_session_id":"` + rsBootstrapUUID +
		`","is_promoted":false,"last_used_at":"2026-01-01T00:00:00Z"},` +
		`{"id":"` + rsOtherConvID + `","cwd":"` + home + `","current_session_id":"` + rsOtherSessionID +
		`","is_promoted":false,"last_used_at":"2026-01-01T00:00:00Z"}]}`)
	if err := os.MkdirAll(instDir, 0o700); err != nil {
		t.Fatalf("mkdir instance dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(instDir, "conversations.json"), convJSON, 0o600); err != nil {
		t.Fatalf("seed conversations.json: %v", err)
	}

	h := StartStreamInteractiveWithRelay(t, home, rsBootstrapUUID, relayURL, rsReplayEnv+"="+fragmentPath)
	t.Cleanup(func() { h.Stop(t) })
	serverID := readPersistedServerID(t, home)
	waitBinaryHello(t, fr, serverID)

	type nextFn = func(time.Time) (protocol.Envelope, bool)
	dial := func(name, token string) (func(protocol.Envelope), nextFn) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		phone, err := fakephone.Dial(ctx, fr.URL(), serverID, token, name)
		if err != nil {
			t.Fatalf("%s dial: %v", name, err)
		}
		t.Cleanup(func() { _ = phone.Close() })
		send, recv := driveHandshakeToOpenDaemonInteractive(t, phone, pubKey, token)
		return sealedConnDriver(t, phone, name, send, recv)
	}

	// await drains one phone until a reply_suggestion for rsConvID in the wanted state
	// (set or null) and, when needTurnEnd, a turn_end. Any suggestion naming another
	// conversation is an error; forbidOld fails on any frame carrying the old text.
	await := func(name string, next nextFn, wantSet, needTurnEnd, forbidOld bool) (protocol.ReplySuggestionPayload, json.RawMessage) {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		sawTurnEnd := !needTurnEnd
		var got protocol.ReplySuggestionPayload
		var raw json.RawMessage
		for raw == nil || !sawTurnEnd {
			env, ok := next(deadline)
			if !ok {
				t.Fatalf("%s: deadline (wantSet=%v): suggestion=%v turn_end=%v", name, wantSet, raw != nil, sawTurnEnd)
			}
			if forbidOld && bytes.Contains(env.Payload, []byte(rsSuggestion)) {
				t.Fatalf("%s: %s frame carries the cleared suggestion text", name, env.Type)
			}
			switch env.Type {
			case protocol.TypeError:
				t.Fatalf("%s: unexpected error envelope: %s", name, string(env.Payload))
			case protocol.TypeTurnEnd:
				sawTurnEnd = true
			case protocol.TypeReplySuggestion:
				var p protocol.ReplySuggestionPayload
				if err := json.Unmarshal(env.Payload, &p); err != nil {
					t.Fatalf("%s: decode reply_suggestion: %v", name, err)
				}
				if p.ConversationID != rsConvID {
					t.Errorf("%s: reply_suggestion names conversation %q, want only %q", name, p.ConversationID, rsConvID)
					continue
				}
				if env.EventID != nil {
					t.Errorf("%s: reply_suggestion carries event_id %d, want none", name, *env.EventID)
				}
				if (p.SuggestedReply != nil) != wantSet {
					t.Logf("%s: skipping reply_suggestion rev=%d set=%v", name, p.Revision, p.SuggestedReply != nil)
					continue
				}
				got, raw = p, append(json.RawMessage(nil), env.Payload...)
			}
		}
		return got, raw
	}

	sendA, nextA := dial("phone-a", tokens[0])
	_, nextB := dial("phone-b", tokens[1])
	send := func(id uint64, msgID, text string) {
		sendA(protocol.Envelope{ID: id, Type: protocol.TypeSendMessage, TS: time.Now().UTC(),
			Payload: mustJSON(t, protocol.SendMessagePayload{ConversationID: rsConvID, MessageID: msgID, Text: text})})
	}

	// 1. The suggestion reaches both interactive phones after the first turn.
	send(28311, "m-reply-suggestion-1", "e2e-reply-suggestion:first\n")
	setA, _ := await("phone-a", nextA, true, true, false)
	setB, _ := await("phone-b", nextB, true, false, false)
	for _, s := range []struct {
		name string
		p    protocol.ReplySuggestionPayload
	}{{"phone-a", setA}, {"phone-b", setB}} {
		if *s.p.SuggestedReply != rsSuggestion || s.p.Revision == 0 || s.p.SessionID == "" {
			t.Fatalf("%s set: got reply=%q rev=%d session=%q, want reply=%q rev>0 session non-empty",
				s.name, *s.p.SuggestedReply, s.p.Revision, s.p.SessionID, rsSuggestion)
		}
	}
	if setA.Revision != setB.Revision {
		t.Errorf("set revision differs across phones: a=%d b=%d", setA.Revision, setB.Revision)
	}

	// 2. An accepted send_message clears it on both phones with the next revision.
	send(28312, "m-reply-suggestion-2", "e2e-reply-suggestion:second\n")
	clearA, rawA := await("phone-a", nextA, false, false, false)
	clearB, rawB := await("phone-b", nextB, false, false, false)
	for _, c := range []struct {
		name string
		p    protocol.ReplySuggestionPayload
		raw  json.RawMessage
	}{{"phone-a", clearA, rawA}, {"phone-b", clearB, rawB}} {
		var keyed map[string]json.RawMessage
		if err := json.Unmarshal(c.raw, &keyed); err != nil {
			t.Fatalf("%s: decode clear as object: %v", c.name, err)
		}
		if v, ok := keyed["suggested_reply"]; !ok || string(v) != "null" {
			t.Errorf("%s clear: suggested_reply = %q (present=%v), want literal null", c.name, v, ok)
		}
		if c.p.Revision <= setA.Revision {
			t.Errorf("%s clear: revision %d, want > set revision %d", c.name, c.p.Revision, setA.Revision)
		}
	}

	// 3. A phone connecting after the clear gets the null on connect, never the text.
	_, nextC := dial("phone-c", tokens[2])
	recC, rawC := await("phone-c", nextC, false, false, true)
	if !bytes.Contains(rawC, []byte(`"suggested_reply":null`)) {
		t.Errorf("phone-c reconcile: payload %s lacks an explicit null suggested_reply", rawC)
	}
	if recC.Revision != clearA.Revision {
		t.Errorf("phone-c reconcile: revision %d, want the clear's %d", recC.Revision, clearA.Revision)
	}
	// Short settle: nothing further on C may name the other conversation or the old text.
	settle := time.Now().Add(2 * time.Second)
	for {
		env, ok := nextC(settle)
		if !ok {
			break
		}
		if bytes.Contains(env.Payload, []byte(rsSuggestion)) {
			t.Fatalf("phone-c settle: %s frame carries the cleared suggestion text", env.Type)
		}
		if env.Type == protocol.TypeReplySuggestion && bytes.Contains(env.Payload, []byte(rsOtherConvID)) {
			t.Fatalf("phone-c settle: reply_suggestion names the other conversation: %s", env.Payload)
		}
	}
}

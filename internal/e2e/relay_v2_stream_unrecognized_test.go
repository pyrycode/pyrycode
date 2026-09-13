//go:build e2e

package e2e

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/paireddevice"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// TestRelayV2_StreamUnrecognizedMessageReachesPhone is the live, end-to-end proof
// that claude output the stream parser has no mapping for reaches a client instead
// of vanishing.
//
// Before this feature the parser dropped such output into a Debug log. The
// production daemon runs at info level, so the drop left NO trace anywhere and no
// client was told. That is fine for the message types we deliberately ignore. It
// is not fine for a type we have never seen: a future claude version that moved
// something meaningful into a new message type would show nothing, everywhere,
// with nothing saying why.
//
// The fake emits two shapes per turn ahead of its normal reply, one for each of
// the two drop sites a live claude could plausibly grow:
//
//	{"type":"fake_future_event",…}                     → an unknown TOP-LEVEL type
//	{"type":"assistant","message":{"content":[         → an unknown assistant BLOCK
//	   {"type":"fake_future_block",…}]}}
//
// and the daemon must turn each into one unrecognized_message frame carrying the
// offending type and its raw JSON.
//
// THE NEGATIVE HALF IS THE POINT. The same run asserts the fake's NORMAL reply
// still produces its assistant_delta and turn_end, and that the ordinary traffic
// raises no unrecognized frames of its own. A feature that surfaces unknown output
// is only worth having if known output stays silent; the tier that proves that
// against real claude is the realclaude suite's zero-unrecognized assertion, and
// this is its hermetic sibling.
func TestRelayV2_StreamUnrecognizedMessageReachesPhone(t *testing.T) {
	const (
		initialUUID = "11111111-1111-4111-8111-111111111111"
		knownConvID = "33333333-3333-4333-8333-333333333333"
		userText    = "e2e-unrecognized:hello\n"
		echoNeedle  = "e2e-unrecognized:hello"
		sendReqID   = uint64(2001)

		// The invented type names and needles fakeclaude's bogus rider writes
		// (internal/e2e/internal/fakeclaude/main.go). Duplicated as literals
		// because the fake is a separate main package.
		bogusLineType    = "fake_future_event"
		bogusLineNeedle  = "bogus-line-needle"
		bogusBlockType   = "fake_future_block"
		bogusBlockNeedle = "bogus-block-needle"
	)

	home := shortHome(t)

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	relayURL := fr.URL() + "/v2/server"
	payloadA, err := paireddevice.Setup(paireddevice.Config{
		Home:         home,
		InstanceName: "test",
		Relay:        relayURL,
		DeviceName:   "phone-a",
	})
	if err != nil {
		t.Fatalf("setup phone-a: %v", err)
	}
	pubKey, err := base64.StdEncoding.DecodeString(payloadA.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	// Bind the conversation to the bootstrap session so the drain gate passes; a
	// mismatched UUID drops every event at the gate and hangs the test.
	seedBoundConversation(t, home, knownConvID, initialUUID)

	// The bogus rider is the ONLY env that distinguishes this from the plain send
	// spec. Unset, fakeclaude is byte-identical to its prior behaviour.
	h := StartStreamInteractiveWithRelay(t, home, initialUUID, relayURL,
		"PYRY_FAKE_CLAUDE_STREAM_BOGUS=1")
	t.Cleanup(func() { h.Stop(t) })

	serverID := readPersistedServerID(t, home)
	waitBinaryHello(t, fr, serverID)

	dialCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	phoneA, err := fakephone.Dial(dialCtx, fr.URL(), serverID, payloadA.Token, "phone-a")
	if err != nil {
		t.Fatalf("phone A dial: %v", err)
	}
	t.Cleanup(func() { _ = phoneA.Close() })
	// Interactive — the capability the unrecognized_message frame is gated on.
	sendA, recvA := driveHandshakeToOpenDaemonInteractive(t, phoneA, pubKey, payloadA.Token)

	sealSend := func(env protocol.Envelope) {
		t.Helper()
		raw, err := json.Marshal(env)
		if err != nil {
			t.Fatalf("marshal envelope: %v", err)
		}
		ciphertext, err := sendA.Encrypt(raw)
		if err != nil {
			t.Fatalf("seal envelope: %v", err)
		}
		sendNoiseMsg(t, phoneA, ciphertext)
	}

	nextEnv := func(deadline time.Time) (protocol.Envelope, bool) {
		t.Helper()
		for {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return protocol.Envelope{}, false
			}
			raw, err := phoneA.ReceiveBytes(remaining)
			if err != nil {
				if errors.Is(err, fakephone.ErrReceiveTimeout) {
					return protocol.Envelope{}, false
				}
				t.Fatalf("phone A receive: %v", err)
			}
			var inner protocol.InnerFrameV2
			if err := json.Unmarshal(raw, &inner); err != nil {
				t.Fatalf("phone A decode inner frame: %v", err)
			}
			if inner.Type != protocol.TypeNoiseMsg {
				continue
			}
			return decryptInnerEnvelope(t, inner, recvA), true
		}
	}

	sealSend(protocol.Envelope{
		ID:   sendReqID,
		Type: protocol.TypeSendMessage,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.SendMessagePayload{
			ConversationID: knownConvID,
			MessageID:      "m-unrecognized-1",
			Text:           userText,
		}),
	})

	// Collect until the turn ends. turn_end is the terminator rather than a frame
	// count, so an extra interleaved broadcast cannot make this flaky. The rider
	// writes its two bogus lines BEFORE the reply, so by the time turn_end lands
	// both have necessarily been through the parser — no ordering race to tune.
	var (
		unrecognized []protocol.UnrecognizedMessagePayload
		sawEcho      bool
		sawTurnEnd   bool
	)
	deadline := time.Now().Add(30 * time.Second)
	for !sawTurnEnd {
		env, ok := nextEnv(deadline)
		if !ok {
			t.Fatalf("did not observe turn_end before deadline; unrecognized so far=%d echo=%v",
				len(unrecognized), sawEcho)
		}
		switch env.Type {
		case protocol.TypeError:
			t.Fatalf("unexpected error envelope: %s", string(env.Payload))
		case protocol.TypeUnrecognizedMessage:
			var p protocol.UnrecognizedMessagePayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("decode unrecognized_message payload: %v", err)
			}
			unrecognized = append(unrecognized, p)
		case protocol.TypeAssistantDelta:
			var p protocol.AssistantDeltaPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("decode assistant_delta payload: %v", err)
			}
			if strings.Contains(p.Text, echoNeedle) {
				sawEcho = true
			}
		case protocol.TypeTurnEnd:
			sawTurnEnd = true
		}
	}

	// --- The positive half: both drop sites surfaced.
	if len(unrecognized) != 2 {
		t.Fatalf("unrecognized_message frames: got %d, want 2 (one per drop site)\n%+v",
			len(unrecognized), unrecognized)
	}

	byType := map[string]protocol.UnrecognizedMessagePayload{}
	for _, p := range unrecognized {
		if p.ConversationID != knownConvID {
			t.Errorf("unrecognized conversation_id: got %q, want %q", p.ConversationID, knownConvID)
		}
		if p.Truncated {
			t.Errorf("unrecognized truncated: got true for a small payload (%d bytes)", len(p.Raw))
		}
		byType[p.MessageType] = p
	}

	line, ok := byType[bogusLineType]
	if !ok {
		t.Fatalf("no unrecognized_message for the unknown top-level type %q; got %v",
			bogusLineType, byType)
	}
	if line.Site != "line_type" {
		t.Errorf("unknown top-level type site: got %q, want %q", line.Site, "line_type")
	}
	// The RAW JSON must carry the parts the parser's own struct never declares —
	// that is the whole reason content blocks are held as raw bytes rather than
	// decoded into the mapping struct, which would have discarded exactly this.
	if !strings.Contains(line.Raw, bogusLineNeedle) {
		t.Errorf("unknown top-level raw lost the payload: got %q, want it to contain %q",
			line.Raw, bogusLineNeedle)
	}

	block, ok := byType[bogusBlockType]
	if !ok {
		t.Fatalf("no unrecognized_message for the unknown assistant block %q; got %v",
			bogusBlockType, byType)
	}
	if block.Site != "assistant_block" {
		t.Errorf("unknown assistant block site: got %q, want %q", block.Site, "assistant_block")
	}
	if !strings.Contains(block.Raw, bogusBlockNeedle) {
		t.Errorf("unknown assistant block raw lost the payload: got %q, want it to contain %q",
			block.Raw, bogusBlockNeedle)
	}

	// --- The negative half: the normal reply is untouched. The diagnostic must not
	// cost the turn it rode in on, and must not open or close a turn of its own.
	if !sawEcho {
		t.Error("the normal assistant reply never arrived; the diagnostic swallowed the turn")
	}
}

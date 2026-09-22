//go:build e2e

package e2e

import (
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

// claude's system/thinking_tokens lines become thinking_progress, but not one for one:
// streamsup's emitThinkingProgress accumulates estimated_tokens_delta and emits only on
// the line whose delta reaches the remaining distance to minThinkingTokensPerEvent, with
// that line's OWN values. Every seam — the parser, turnbridge.MapEvent, cmd/pyry's
// interactive v2 emitter — has unit coverage; before #1534 nothing drove one of these
// lines through a real daemon to a connected client, and the realclaude drains skip the
// frame, so make preship did not close the gap either.
//
// TWO FEEDS ON ONE PATH, and the pair is the proof. The silent feed stops one token short
// of the bound; the crossing feed is the same lines plus one more, reaching it exactly.
// A zero alone proves nothing (a wrong env name, a malformed line, a drop before the
// parser all leave it green); the crossing run through the same helper kills those.
// Together they pin the bound from both sides: a lowered constant reds the silence, a
// raised one or a `>=` turned `>` reds the crossing.
//
// THE FEED IS fakeclaude's REPLAY RIDER (#2503), as in the background-task spec: the
// first user turn's canned reply is replaced by the fragment. Every fed line precedes the
// result line and the parser consumes stdout in order, so turn_end reaching the client
// means every fed line has been parsed: no sleep, no poll.
const (
	// Distinct from every sibling spec's ids: they share the package.
	thinkingProgressBootstrapUUID = "15340000-0000-4000-8000-000000000001"
	thinkingProgressConvID        = "15340000-0000-4000-8000-000000000002"
	thinkingProgressUserText      = "e2e-thinking-progress:hello\n"
	thinkingProgressSendReqID     = uint64(1534)

	// The replayed reply, deliberately not the user text: an echo would prove the canned
	// path ran, the opposite of what the milestone has to show.
	thinkingProgressNeedle = "e2e-thinking-progress:replayed-reply"

	thinkingProgressReplayEnv = "PYRY_FAKE_CLAUDE_STREAM_REPLAY_FIRST"

	// The rate bound, as a test-side literal on purpose: importing the parser's constant
	// would move this test with it, and a change to the bound is what it exists to catch.
	thinkingProgressBound = 64

	// Capture payloads, byte for byte: internal/e2e/realclaude/testdata/
	// dropped_lines_v2.1.220.json, dropped_lines[] indices 31, 32 and 36. 31 and 32 are
	// consecutive in the capture (deltas 1 and 62, 63 together); 36 opens a later turn
	// with delta 1, which brings the crossing feed to exactly 64.
	thinkingTokensLine31 = `{"type":"system","subtype":"thinking_tokens","estimated_tokens":64,"estimated_tokens_delta":1,"uuid":"9f1a9f05-691c-471d-9804-0b509a3b5056","session_id":"$SESSION_ID"}`
	thinkingTokensLine32 = `{"type":"system","subtype":"thinking_tokens","estimated_tokens":126,"estimated_tokens_delta":62,"uuid":"5afa65fa-b589-4d47-8c0c-e232a85e47ff","session_id":"$SESSION_ID"}`
	thinkingTokensLine36 = `{"type":"system","subtype":"thinking_tokens","estimated_tokens":1,"estimated_tokens_delta":1,"uuid":"5aea1d00-d6da-4c9c-b880-d80985b88537","session_id":"$SESSION_ID"}`
)

// thinkingProgressObservation is what one driven turn put on the wire: every
// thinking_progress payload as raw bytes, a count of the unrecognized lane, and the two
// milestones, returned as values so each test asserts them first and fatally.
type thinkingProgressObservation struct {
	progress     []json.RawMessage
	unrecognized int
	sawNeedle    bool
	sawTurnEnd   bool
}

// thinkingProgressFragment is the whole replayed stdout of the first turn: the given
// thinking_tokens lines, then a reply in the shape of fakeclaude's writeStreamResponse.
func thinkingProgressFragment(thinkingLines []string) string {
	lines := append([]string(nil), thinkingLines...)
	lines = append(lines,
		`{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"`+thinkingProgressNeedle+`"}]}}`,
		`{"type":"result","subtype":"success","session_id":"fake-stream"}`,
	)
	return strings.Join(lines, "\n") + "\n"
}

// thinkingDeltaSum decodes the fed lines themselves and sums their deltas, so a feed that
// drifts off the edge of the bound fails as that rather than as a wrong frame count.
func thinkingDeltaSum(t *testing.T, thinkingLines []string) int {
	t.Helper()
	sum := 0
	for i, line := range thinkingLines {
		var l struct {
			Type                 string `json:"type"`
			Subtype              string `json:"subtype"`
			EstimatedTokensDelta int    `json:"estimated_tokens_delta"`
		}
		if err := json.Unmarshal([]byte(line), &l); err != nil {
			t.Fatalf("decode fed line %d: %v", i, err)
		}
		if l.Type != "system" || l.Subtype != "thinking_tokens" {
			t.Fatalf("fed line %d is %s/%s, want system/thinking_tokens", i, l.Type, l.Subtype)
		}
		sum += l.EstimatedTokensDelta
	}
	return sum
}

// driveThinkingProgressTurn spawns a stream-interactive daemon whose fakeclaude replays
// thinkingProgressFragment(thinkingLines) for the first user turn, drives that turn from
// a connected interactive v2 client, and returns what the turn put on the wire.
//
// It asserts no acceptance criterion. It t.Fatalf's only on setup, transport and decode
// faults and on an error envelope; on the drain deadline it logs the counts and returns
// with sawTurnEnd false, leaving the diagnosis to the caller's milestone assertions.
func driveThinkingProgressTurn(t *testing.T, thinkingLines []string) thinkingProgressObservation {
	t.Helper()

	// Written before the daemon starts: fakeclaude reads the file once at startup.
	fragmentPath := filepath.Join(t.TempDir(), "thinking-progress.jsonl")
	if err := os.WriteFile(fragmentPath, []byte(thinkingProgressFragment(thinkingLines)), 0o600); err != nil {
		t.Fatalf("write replay fragment: %v", err)
	}

	home := shortHome(t)
	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	relayURL := fr.URL() + "/v2/server"

	payloadA, err := paireddevice.Setup(paireddevice.Config{
		Home:                   home,
		InstanceName:           "test",
		Relay:                  relayURL,
		DeviceName:             "phone-a",
		AllowRemotePermissions: false,
	})
	if err != nil {
		t.Fatalf("setup paired device: %v", err)
	}
	pubKey, err := base64.StdEncoding.DecodeString(payloadA.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	// The UUID MUST equal the one handed to StartStreamInteractiveWithRelay: a mismatch
	// drops every event at the drain gate and presents as an unexplained timeout.
	seedBoundConversation(t, home, thinkingProgressConvID, thinkingProgressBootstrapUUID)

	h := StartStreamInteractiveWithRelay(t, home, thinkingProgressBootstrapUUID, relayURL,
		thinkingProgressReplayEnv+"="+fragmentPath)
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
	// Interactive, and load-bearing: a non-interactive handshake yields zero frames, which
	// the silent test would read as a pass. The crossing test is what catches that.
	sendA, recvA := driveHandshakeToOpenDaemonInteractive(t, phoneA, pubKey, payloadA.Token)
	sealSend, nextEnv := sealedConnDriver(t, phoneA, "phone A", sendA, recvA)

	sealSend(protocol.Envelope{
		ID:   thinkingProgressSendReqID,
		Type: protocol.TypeSendMessage,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.SendMessagePayload{
			ConversationID: thinkingProgressConvID,
			MessageID:      "m-thinking-progress-1",
			Text:           thinkingProgressUserText,
		}),
	})

	var obs thinkingProgressObservation
	deadline := time.Now().Add(30 * time.Second)
	for !obs.sawTurnEnd {
		env, ok := nextEnv(deadline)
		if !ok {
			t.Logf("drain deadline reached: thinking_progress=%d unrecognized=%d needle=%v turn_end=%v",
				len(obs.progress), obs.unrecognized, obs.sawNeedle, obs.sawTurnEnd)
			break
		}
		switch env.Type {
		case protocol.TypeError:
			t.Fatalf("unexpected error envelope: %s", string(env.Payload))
		case protocol.TypeThinkingProgress:
			obs.progress = append(obs.progress, append(json.RawMessage(nil), env.Payload...))
		case protocol.TypeUnrecognizedMessage:
			obs.unrecognized++
		case protocol.TypeAssistantDelta:
			var p protocol.AssistantDeltaPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("decode assistant_delta payload: %v", err)
			}
			if strings.Contains(p.Text, thinkingProgressNeedle) {
				obs.sawNeedle = true
			}
		case protocol.TypeTurnEnd:
			obs.sawTurnEnd = true
		}
	}
	return obs
}

// assertThinkingProgressMilestones is the shared first-and-fatal gate: without the
// replayed reply and turn_end, no count read off the run means anything.
func assertThinkingProgressMilestones(t *testing.T, obs thinkingProgressObservation) {
	t.Helper()
	if !obs.sawNeedle {
		t.Fatalf("the replayed reply never arrived (no assistant_delta carrying %q) — the run did not complete, "+
			"so its counts prove nothing; thinking_progress=%d unrecognized=%d turn_end=%v. "+
			"An echo of the user text instead means the replay env never reached fakeclaude",
			thinkingProgressNeedle, len(obs.progress), obs.unrecognized, obs.sawTurnEnd)
	}
	if !obs.sawTurnEnd {
		t.Fatalf("the replayed reply arrived but the turn never closed (no turn_end) — the run did not complete, "+
			"so its counts prove nothing; thinking_progress=%d unrecognized=%d",
			len(obs.progress), obs.unrecognized)
	}
	if obs.unrecognized != 0 {
		t.Errorf("unrecognized_message frames: got %d, want 0 — a fed line reached the fallback lane "+
			"rather than the thinking_tokens arm", obs.unrecognized)
	}
}

// TestRelayV2_StreamThinkingProgressBelowBoundIsSilent feeds thinking_tokens lines whose
// deltas sum to one short of the bound and proves no thinking_progress reaches the phone.
func TestRelayV2_StreamThinkingProgressBelowBoundIsSilent(t *testing.T) {
	feed := []string{thinkingTokensLine31, thinkingTokensLine32}
	if got := thinkingDeltaSum(t, feed); got != thinkingProgressBound-1 {
		t.Fatalf("silent feed's delta sum: got %d, want %d (one short of the bound)", got, thinkingProgressBound-1)
	}

	obs := driveThinkingProgressTurn(t, feed)
	assertThinkingProgressMilestones(t, obs)

	if len(obs.progress) != 0 {
		t.Errorf("thinking_progress frames: got %d, want 0 — the fed deltas sum to %d, under the %d-token bound, "+
			"so the parser's bound has dropped or its accumulator is bypassed",
			len(obs.progress), thinkingProgressBound-1, thinkingProgressBound)
	}
}

// TestRelayV2_StreamThinkingProgressCrossingDeliversOneFrame feeds the silent feed plus
// one line that brings the sum to exactly the bound and proves exactly one
// thinking_progress reaches the phone, carrying the crossing line's own values.
func TestRelayV2_StreamThinkingProgressCrossingDeliversOneFrame(t *testing.T) {
	feed := []string{thinkingTokensLine31, thinkingTokensLine32, thinkingTokensLine36}
	if got := thinkingDeltaSum(t, feed); got != thinkingProgressBound {
		t.Fatalf("crossing feed's delta sum: got %d, want %d (exactly the bound)", got, thinkingProgressBound)
	}

	obs := driveThinkingProgressTurn(t, feed)
	assertThinkingProgressMilestones(t, obs)

	if len(obs.progress) != 1 {
		t.Fatalf("thinking_progress frames: got %d, want 1 — the fed deltas reach the %d-token bound exactly "+
			"on the last line", len(obs.progress), thinkingProgressBound)
	}
	// Decoding into the struct cannot see an extra key: a frame that started carrying the
	// fed line's session_id or uuid would pass the value check below.
	var keyed map[string]json.RawMessage
	if err := json.Unmarshal(obs.progress[0], &keyed); err != nil {
		t.Fatalf("decode thinking_progress payload as an object: %v", err)
	}
	assertExactKeys(t, "thinking_progress payload", keyed,
		[]string{"conversation_id", "estimated_tokens", "estimated_tokens_delta"})

	var got protocol.ThinkingProgressPayload
	if err := json.Unmarshal(obs.progress[0], &got); err != nil {
		t.Fatalf("decode thinking_progress payload: %v", err)
	}
	// Line 36's own values, both different from the accumulated 64: a frame carrying 64
	// would mean the parser reported its accumulator instead of the line.
	want := protocol.ThinkingProgressPayload{
		ConversationID:       thinkingProgressConvID,
		EstimatedTokens:      1,
		EstimatedTokensDelta: 1,
	}
	if got != want {
		t.Errorf("thinking_progress:\n got  %+v\n want %+v", got, want)
	}
}

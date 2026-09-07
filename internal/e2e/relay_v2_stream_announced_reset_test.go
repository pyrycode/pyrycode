//go:build e2e

package e2e

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// TestRelayV2_StreamAnnouncedResetFollowsClaude drives #2135 end to end against a
// REAL daemon on the stream-json interactive runner: claude announces on its own
// output stream that it reset the conversation and mounted a fresh transcript, and
// the daemon must follow it.
//
// It carries AC 1, AC 2's end-to-end half and AC 4. AC 3 and AC 5 are pinned at the
// unit level, where an equal-id announcement and a saturated fan-in can be created
// deterministically; neither is reachable from a phone.
//
// The three assertions, and what each one's absence would look like:
//
//   - ONE session_transition with reason clear, naming the pre-reset id and the
//     announced one. Before this ticket nothing emitted it and no head drew a
//     break, which is the visible half of the bug (AC 1). "Exactly one" is the
//     load-bearing word: the rotation watcher observes the same rotation through
//     the new transcript's creation, and the second of the two re-keys must find
//     the old id already gone and stay silent.
//   - the second turn's turn_end still reaches the phone (AC 2). This is the half
//     that fails LOUDER than the original bug if it regresses: re-keying the
//     registry without rotating the runner's stream session tag leaves every later
//     event tagged with the retired id, the drain's active-session gate drops all
//     of them, and the conversation goes dark until the daemon restarts.
//   - the run-configuration reply names the ANNOUNCED id and reports the ANNOUNCED
//     id's transcript (AC 4). The transcript is planted under the announced id
//     BEFORE the turn and the pre-reset session has none, so used_tokens moves from
//     0 to the planted figure. A reply still resolving the pre-reset id reads 0 —
//     which is exactly the frozen gauge, and is why the discriminating pair is a
//     planted figure against a fresh zero rather than two defaults.
func TestRelayV2_StreamAnnouncedResetFollowsClaude(t *testing.T) {
	const (
		initialUUID = "11111111-1111-4111-8111-111111111111"
		// The id claude announces. Conspicuous digits, and the same stem #2134's
		// parser fixtures use, so a tree-wide sweep finds the announcement's two
		// ends together.
		announcedUUID = "0f1e2d3c-4b5a-4978-8796-a5b4c3d2e1f0"

		createReqID = uint64(2135)
		firstReqID  = uint64(2136)
		sendReqID   = uint64(2137)
		secondReqID = uint64(2138)
		send2ReqID  = uint64(2139)

		// The planted transcript's four counters, summing to 12345 — a figure no
		// default window or fresh-session report can produce, so reading it back
		// cannot be a coincidence.
		wantUsed = 12345
	)

	home := shortHome(t)

	rA := RunBareIn(t, home, "pair", "-pyry-name=test", "--name=phone-a")
	if rA.ExitCode != 0 {
		t.Fatalf("pyry pair phone-a exit=%d\nstdout:\n%s\nstderr:\n%s", rA.ExitCode, rA.Stdout, rA.Stderr)
	}
	payloadA := decodePairPayload(t, rA.Stdout)
	pubKey, err := base64.StdEncoding.DecodeString(payloadA.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })

	// The rider is what makes the announcement exist. Without it the fake child
	// never writes a conversation_reset line, nothing reaches the follower, and this
	// spec would assert the pre-reset state while looking green.
	h := StartStreamInteractiveWithRelay(t, home, initialUUID, fr.URL()+"/v2/server",
		"PYRY_FAKE_CLAUDE_STREAM_RESET_TO="+announcedUUID)
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
	sendA, recvA := driveHandshakeToOpenDaemonInteractive(t, phoneA, pubKey, payloadA.Token)

	sealSend := func(env protocol.Envelope) {
		t.Helper()
		raw, err := json.Marshal(env)
		if err != nil {
			t.Fatalf("marshal %s envelope (id=%d): %v", env.Type, env.ID, err)
		}
		cipher, err := sendA.Encrypt(raw)
		if err != nil {
			t.Fatalf("seal %s envelope (id=%d): %v", env.Type, env.ID, err)
		}
		sendNoiseMsg(t, phoneA, cipher)
	}
	nextEnv := func(deadline time.Time) (protocol.Envelope, bool) {
		t.Helper()
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return protocol.Envelope{}, false
		}
		return decryptInnerEnvelope(t, readInnerFrame(t, phoneA, remaining), recvA), true
	}
	awaitReply := func(want uint64) protocol.Envelope {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for {
			env, ok := nextEnv(deadline)
			if !ok {
				t.Fatalf("phone A never received a reply to request id=%d", want)
			}
			if env.InReplyTo != nil && *env.InReplyTo == want {
				return env
			}
		}
	}
	readSettings := func(reqID uint64, convID string) protocol.SessionSettingsPayload {
		t.Helper()
		sealSend(protocol.Envelope{
			ID:      reqID,
			Type:    protocol.TypeRequestSessionSettings,
			TS:      time.Now().UTC(),
			Payload: mustJSON(t, protocol.RequestSessionSettingsPayload{ConversationID: convID}),
		})
		reply := awaitReply(reqID)
		if reply.Type != protocol.TypeSessionSettings {
			t.Fatalf("reply Type = %q, want %q (payload %s)", reply.Type, protocol.TypeSessionSettings, string(reply.Payload))
		}
		var got protocol.SessionSettingsPayload
		if err := json.Unmarshal(reply.Payload, &got); err != nil {
			t.Fatalf("decode session_settings payload: %v", err)
		}
		return got
	}
	// driveTurn sends one user turn and drains to its turn_end, counting the
	// session_transition frames that interleave. Counting them HERE rather than in a
	// separate drain is what makes "exactly one" assertable: the frames arrive
	// unsolicited, interleaved with the turn's own, and a later read would have
	// already discarded them.
	driveTurn := func(reqID uint64, convID, msgID, text string) []protocol.SessionTransitionPayload {
		t.Helper()
		sealSend(protocol.Envelope{
			ID:   reqID,
			Type: protocol.TypeSendMessage,
			TS:   time.Now().UTC(),
			Payload: mustJSON(t, protocol.SendMessagePayload{
				ConversationID: convID,
				MessageID:      msgID,
				Text:           text,
			}),
		})
		var transitions []protocol.SessionTransitionPayload
		deadline := time.Now().Add(30 * time.Second)
		for {
			env, ok := nextEnv(deadline)
			if !ok {
				t.Fatalf("the turn never closed (no turn_end for conversation %s after %q)", convID, text)
			}
			if env.Type == protocol.TypeError {
				t.Fatalf("unexpected error envelope during the turn: %s", string(env.Payload))
			}
			if env.Type == protocol.TypeSessionTransition {
				var p protocol.SessionTransitionPayload
				if err := json.Unmarshal(env.Payload, &p); err != nil {
					t.Fatalf("decode session_transition payload: %v", err)
				}
				transitions = append(transitions, p)
			}
			if env.Type == protocol.TypeTurnEnd {
				return transitions
			}
		}
	}

	convID := createConversationViaPhone(t, phoneA, sendA, recvA, createReqID)

	// The pre-reset id is learned from the daemon rather than assumed: it is the id
	// the reply must STOP naming, and asserting a movement needs both ends.
	before := readSettings(firstReqID, convID)
	if before.SessionID == "" {
		t.Fatal("session_id is empty on the seeded conversation — there is no pre-reset id to move away from")
	}
	if before.SessionID == announcedUUID {
		t.Fatalf("the seeded conversation is already bound to the announced id %q — "+
			"the assertion below could not tell a followed reset from no reset at all", announcedUUID)
	}
	if before.UsedTokens != 0 {
		t.Fatalf("used_tokens = %d before the reset, want 0 — the minted session has no transcript, "+
			"and a non-zero here would make the post-reset figure unattributable", before.UsedTokens)
	}

	// Plant the ANNOUNCED id's transcript, before the turn that announces it. The
	// daemon's sessions dir is re-derived from its $HOME and workdir (both the
	// harness home) rather than read off the harness, which leaves ClaudeSessionsDir
	// unset in stream mode because stream-mode fakeclaude opens no transcript of its
	// own — the same absence #2107's spec works around, and the reason a planted file
	// is the only way to give the two ids DIFFERENT figures.
	sessionsDir := claudeSessionsDir(home)
	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		t.Fatalf("mkdir claude sessions dir %s: %v", sessionsDir, err)
	}
	transcript := `{"type":"user","message":{"role":"user","content":"hi"}}` + "\n" +
		`{"type":"assistant","message":{"model":"claude-sonnet-5","role":"assistant","stop_reason":"end_turn",` +
		`"content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":45,"cache_creation_input_tokens":300,` +
		`"cache_read_input_tokens":12000,"output_tokens":0}}}` + "\n"
	if err := os.WriteFile(filepath.Join(sessionsDir, announcedUUID+".jsonl"), []byte(transcript), 0o600); err != nil {
		t.Fatalf("write announced transcript: %v", err)
	}

	// Turn one: the rider writes the announcement BEFORE the reply, so this
	// turn_end implies the announcement has already been through the parser, the
	// follower and the pool re-key. No sleep, no poll.
	transitions := driveTurn(sendReqID, convID, "m-2135-1", "e2e-2135 announce a reset")

	if len(transitions) != 1 {
		t.Fatalf("client saw %d session_transition frames, want exactly 1: %+v — "+
			"0 means nothing followed the announcement and no head draws a break; "+
			"more than 1 means the rotation watcher fired a second delimiter for the same reset",
			len(transitions), transitions)
	}
	// The wire reason is a plain string on the payload, so the literal is the
	// contract — cmd/pyry maps internal/sessions' ReasonClear onto it.
	if got := transitions[0].Reason; got != "clear" {
		t.Errorf("session_transition reason = %q, want %q", got, "clear")
	}
	if transitions[0].PreviousSessionID != before.SessionID || transitions[0].NewSessionID != announcedUUID {
		t.Errorf("session_transition = {%q → %q}, want {%q → %q}",
			transitions[0].PreviousSessionID, transitions[0].NewSessionID, before.SessionID, announcedUUID)
	}

	// AC 4. The reply resolves the conversation's CURRENT bound session, so both
	// halves move together or neither does.
	after := readSettings(secondReqID, convID)
	if after.SessionID != announcedUUID {
		t.Errorf("session_id = %q after the announced reset, want the announced %q — "+
			"a client writing its model / effort choice back would address the retired session",
			after.SessionID, announcedUUID)
	}
	if after.UsedTokens != wantUsed {
		t.Errorf("used_tokens = %d after the announced reset, want %d — the ANNOUNCED id's transcript. "+
			"0 means the gauge is still resolving the pre-reset id, which stopped growing the moment claude reset "+
			"and never grows again", after.UsedTokens, wantUsed)
	}

	// AC 2, and the assertion whose failure is worse than the bug this ticket fixes:
	// a second turn from the STILL-RUNNING child must still reach the phone. The
	// child was never replaced, so every event it produces now carries the rotated
	// tag or none of them cross the drain's active-session gate.
	if got := driveTurn(send2ReqID, convID, "m-2135-2", "e2e-2135 after the reset"); len(got) != 0 {
		t.Errorf("client saw %d further session_transition frames on the second turn, want 0: %+v — "+
			"the reset is announced once and re-announcing the same id must change nothing", len(got), got)
	}
}

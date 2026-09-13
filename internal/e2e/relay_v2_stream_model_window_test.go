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
	"github.com/pyrycode/pyrycode/internal/e2e/internal/paireddevice"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// TestRelayV2_StreamSessionSettingsReportsTheObservedWindow is #2107's end-to-end
// proof, against a REAL daemon running the stream-json interactive runner: the
// context-window gauge reads a TRUE FRACTION on a 1M-context session instead of
// sitting clamped, because the window reported is the one claude observed for the
// model that produced the used count.
//
// The two halves it joins arrive on DIFFERENT CHANNELS, and only a full-stack run
// exercises both at once:
//
//   - the used count comes off the transcript FILE, which the test plants at the
//     path the daemon resolves by session id;
//   - the window comes off claude's STDOUT, where the fake child's modelUsage
//     rider reports it at each turn end and the daemon retains it per session.
//
// Before #2107 this session read window_tokens 0 — #2100's "no trustworthy
// reading", correct but useless, because 223075 used against a believed 200000 is
// 111%. The assertion is that it now reads 1000000 against the same 223075.
//
// THE FIXTURE IS DISCRIMINATING, which is why one assertion suffices. The rider
// reports TWO models at two sizes (200000 and 1000000) and the transcript names
// the 1M one, so a daemon that answered "the first entry", "the only entry", "the
// largest that fits" or "the session's configured model" would not produce 1000000
// here. The unit tables in internal/contextwindow separate those readings row by
// row; this spec proves the whole path is wired.
//
// screen_snapshot is deliberately NOT asserted here even though it reads the same
// figures. In stream mode the daemon routes its typed-nil supervisor to a nil
// Snapshotter on purpose, so handleRequestSnapshot short-circuits to
// server.binary_offline and there is no reply to assert against — see
// TestRelayV2_StreamRequestSessionSettings' own doc. That half is proven at its
// seam by cmd/pyry's TestBootstrapSnapshotUsage_ReportsTheObservedWindow, and the
// two seams are constructed side by side in startRelayV2 from one resolver.
func TestRelayV2_StreamSessionSettingsReportsTheObservedWindow(t *testing.T) {
	const (
		initialUUID = "11111111-1111-4111-8111-111111111111"
		createReqID = uint64(2170)
		firstReqID  = uint64(2171)
		sendReqID   = uint64(2172)
		secondReqID = uint64(2173)

		// The model the planted transcript names — the BASE of the id the
		// fake-claude rider reports 1000000 under, which since #2118 the rider
		// keys with a trailing variant group (riderModelWindowSonnetKey,
		// "claude-sonnet-5[1m]") while its canonicalModel keeps this base.
		//
		// THE TWO HALVES ARE NOW TWO STRINGS, and that is the point. They used to
		// come from one constant on the rider's side, so this spec passed while
		// the production join could not fire on any real 1M session: the
		// transcript records a bare id and claude's result line keys the same
		// model with the group. Writing both halves from one string reproduced
		// the "never checked against each other" defect inside the harness meant
		// to catch it. Do not re-align them.
		transcriptModel = "claude-sonnet-5"

		// The four counters of the planted usage block, summing to 223075 — the
		// figure measured live on 2026-09-04 on a real 1M-context session, and
		// the one that reads 111% against the old hardcoded 200000.
		wantUsed   = 223075
		wantWindow = 1_000_000
	)

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

	// The rider is what makes the daemon's per-session retention hold anything at
	// all: without it the fake child's result lines carry no modelUsage, nothing
	// is ever reported, and this spec would assert the default window while
	// looking green.
	h := StartStreamInteractiveWithRelay(t, home, initialUUID, relayURL,
		"PYRY_FAKE_CLAUDE_STREAM_MODEL_WINDOWS=1")
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
	// Correlate on in_reply_to rather than on type, so a TypeError refusal is
	// reported as the regression it is instead of being drained past into a
	// timeout. Unsolicited frames (turn_state and friends) interleave freely.
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

	convID := createConversationViaPhone(t, phoneA, sendA, recvA, createReqID)

	// The session id is learned from the daemon rather than assumed, because it is
	// what names the transcript the daemon will resolve: snapshotUsageFor stats
	// <sessionsDir>/<id>.jsonl, so planting the file anywhere else would leave the
	// used count at zero and the spec asserting nothing.
	sessionID := readSettings(firstReqID, convID).SessionID
	if sessionID == "" {
		t.Fatal("session_id is empty on the seeded conversation — there is no id to plant a transcript under")
	}

	// Plant the transcript. The daemon's sessions dir is re-derived from its $HOME
	// and workdir (both the harness home) rather than read off the harness, which
	// leaves ClaudeSessionsDir unset in stream mode because stream-mode fakeclaude
	// opens no transcript of its own — that absence is exactly why this spec has to
	// write one.
	sessionsDir := claudeSessionsDir(home)
	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		t.Fatalf("mkdir claude sessions dir %s: %v", sessionsDir, err)
	}
	transcript := `{"type":"user","message":{"role":"user","content":"hi"}}` + "\n" +
		`{"type":"assistant","message":{"model":"` + transcriptModel + `","role":"assistant","stop_reason":"end_turn",` +
		`"content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":2,"cache_creation_input_tokens":950,` +
		`"cache_read_input_tokens":221118,"output_tokens":1005}}}` + "\n"
	transcriptPath := filepath.Join(sessionsDir, sessionID+".jsonl")
	if err := os.WriteFile(transcriptPath, []byte(transcript), 0o600); err != nil {
		t.Fatalf("write transcript %s: %v", transcriptPath, err)
	}

	// Drive one turn and wait for turn_end. This is what makes the WINDOW exist:
	// the rider writes modelUsage onto the result line, so without a completed turn
	// nothing is ever reported and the retention stays empty. It is also the
	// SYNCHRONISATION — the daemon's hold is filled by the parser on the same line
	// that produces this frame, upstream of the send, so turn_end reaching the
	// phone implies the report is already retained. No sleep, no poll.
	sealSend(protocol.Envelope{
		ID:   sendReqID,
		Type: protocol.TypeSendMessage,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.SendMessagePayload{
			ConversationID: convID,
			MessageID:      "m-2107-1",
			Text:           "e2e-2107 drive one turn",
		}),
	})
	turnDeadline := time.Now().Add(30 * time.Second)
	sawTurnEnd := false
	for !sawTurnEnd {
		env, ok := nextEnv(turnDeadline)
		if !ok {
			break
		}
		if env.Type == protocol.TypeError {
			t.Fatalf("unexpected error envelope during the turn: %s", string(env.Payload))
		}
		if env.Type == protocol.TypeTurnEnd {
			sawTurnEnd = true
		}
	}
	if !sawTurnEnd {
		t.Fatalf("the turn never closed (no turn_end for conversation %s) — the window is reported at turn end, "+
			"so nothing downstream of this point would be testing the join", convID)
	}

	got := readSettings(secondReqID, convID)
	if got.UsedTokens != wantUsed {
		t.Errorf("used_tokens = %d, want %d — the planted transcript's latest usage-bearing entry",
			got.UsedTokens, wantUsed)
	}
	if got.WindowTokens != wantWindow {
		t.Errorf("window_tokens = %d, want %d — the window claude reported for %q, the model that produced the used "+
			"count. 0 means the join never fired and #2100 collapsed the default; %d means the observed window was "+
			"ignored for the fallback; anything else means a DIFFERENT model's window was reported",
			got.WindowTokens, wantWindow, transcriptModel, defaultWindowE2E)
	}
}

// defaultWindowE2E mirrors internal/contextwindow's unexported defaultWindowTokens
// — the window every session was reported at before #2107, and the value a failed
// join still falls back to. Named so the failure message above can tell "the join
// never fired" apart from "a different model's window was reported".
const defaultWindowE2E = 200_000

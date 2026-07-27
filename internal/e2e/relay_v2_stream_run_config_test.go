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
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// TestRelayV2_StreamRequestSessionSettings drives request_session_settings
// against a REAL daemon running the stream-json interactive runner — the runner
// in production since the 2026-07-24 cutover — and asserts it answers with the
// run configuration.
//
// This is the end-to-end proof for desktop#491 and #1214, and both halves of it
// fail on the parent commit.
//
// Before, a client read the run configuration off screen_snapshot's side-load
// (#848, #857). That reply is gated on a live terminal screen, and stream mode
// has none: cmd/pyry routes the typed-nil supervisor to a nil Snapshotter on
// purpose (#1077, #1101), so handleRequestSnapshot short-circuits to
// server.binary_offline. The settings, which have nothing to do with a terminal,
// were refused along with it — leaving the desktop run-configuration sheet with
// no values, no session id to address a change to, and no context figure.
//
// Two assertions carry the whole fix:
//
//   - session_id is non-empty and IS the bootstrap id. Without it the sheet's
//     controls stay inert forever, because the client's only other source is the
//     unsolicited session_transition marker, which fires on a clear or an idle
//     eviction and never on session creation.
//   - window_tokens is non-zero. In stream mode the usage seam used to be nil by
//     construction, so every consumer reported a zero window, which the desktop
//     renders as "context usage unavailable" (#1214). Zero used against a
//     200k window is correct here: stream-mode fakeclaude writes no transcript,
//     so this is the genuine fresh-session report, and it proves the reader
//     resolved and degraded rather than never having been built.
//
// No conversation is seeded and no turn is driven: the request frame is bare and
// the reply is daemon-wide, so the verb answers on a freshly started daemon. That
// is itself part of the contract — the sheet must work on a conversation the user
// has never sent a message in, which is exactly the case that was broken.
func TestRelayV2_StreamRequestSessionSettings(t *testing.T) {
	const (
		initialUUID = "11111111-1111-4111-8111-111111111111"
		reqID       = uint64(1491)
	)

	home := shortHome(t)

	// Pair one interactive device.
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

	h := StartStreamInteractiveWithRelay(t, home, initialUUID, fr.URL()+"/v2/server")
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
	// Interactive — the capability the read verb gates on, mirroring the write verb.
	sendA, recvA := driveHandshakeToOpenDaemonInteractive(t, phoneA, pubKey, payloadA.Token)

	// A BARE frame: no payload at all. The reply is daemon-wide, so there is no
	// field a client could use to select another session's data.
	reqEnv, err := json.Marshal(protocol.Envelope{
		ID:   reqID,
		Type: protocol.TypeRequestSessionSettings,
		TS:   time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("marshal request_session_settings envelope: %v", err)
	}
	cipher, err := sendA.Encrypt(reqEnv)
	if err != nil {
		t.Fatalf("seal request_session_settings envelope: %v", err)
	}
	sendNoiseMsg(t, phoneA, cipher)

	// Await the correlated reply. Unsolicited frames (turn_state and friends) can
	// interleave, so read until the in_reply_to matches.
	var reply protocol.Envelope
	deadline := time.Now().Add(15 * time.Second)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatal("phone A never received a reply to request_session_settings; the verb is unanswered on the stream runner")
		}
		env := decryptInnerEnvelope(t, readInnerFrame(t, phoneA, remaining), recvA)
		if env.InReplyTo != nil && *env.InReplyTo == reqID {
			reply = env
			break
		}
	}

	if reply.Type == protocol.TypeError {
		var errPayload protocol.ErrorPayload
		_ = json.Unmarshal(reply.Payload, &errPayload)
		t.Fatalf("request_session_settings answered TypeError{code=%q} on the stream runner — "+
			"the run configuration must NOT be gated on a terminal screen (desktop#491)", errPayload.Code)
	}
	if reply.Type != protocol.TypeSessionSettings {
		t.Fatalf("reply Type = %q, want %q", reply.Type, protocol.TypeSessionSettings)
	}

	var got protocol.SessionSettingsPayload
	if err := json.Unmarshal(reply.Payload, &got); err != nil {
		t.Fatalf("decode session_settings payload: %v", err)
	}

	if got.SessionID == "" {
		t.Error("session_id is empty — a client cannot address a set_session_settings, so the run-config controls stay inert (desktop#491)")
	}
	if got.SessionID != initialUUID {
		t.Errorf("session_id = %q, want the bootstrap id %q — a client must write to the session whose values it just read",
			got.SessionID, initialUUID)
	}
	if got.WindowTokens == 0 {
		t.Error("window_tokens = 0 — the usage reader was never built on the stream runner, " +
			"which the desktop renders as \"context usage unavailable\" (#1214)")
	}
	if got.UsedTokens != 0 {
		t.Errorf("used_tokens = %d, want 0 — stream-mode fakeclaude writes no transcript, so this is a fresh session", got.UsedTokens)
	}
}

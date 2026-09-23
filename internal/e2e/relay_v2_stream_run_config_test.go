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
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// TestRelayV2_StreamRequestSessionSettings drives request_session_settings
// against a REAL daemon running the stream-json interactive runner — the runner
// in production since the 2026-07-24 cutover — and asserts it answers with the
// named conversation's run configuration.
//
// This is the end-to-end proof for desktop#491 and #1214, against the real
// production wiring rather than a seam double.
//
// Before, a client read the run configuration off screen_snapshot's side-load
// (#848, #857). That reply is gated on a live terminal screen, and stream mode
// has none: cmd/pyry wires no Snapshotter (#1077, #1101, #1348), so
// handleRequestSnapshot short-circuits to
// server.binary_offline. The settings, which have nothing to do with a terminal,
// were refused along with it — leaving the desktop run-configuration sheet with
// no values, no session id to address a change to, and no context figure.
//
// Three assertions carry it:
//
//   - session_id is non-empty and is NOT the bootstrap id. Non-empty because
//     without it the sheet's controls stay inert forever, the client's only other
//     source being the unsolicited session_transition marker, which fires on a
//     clear or an idle eviction and never on session creation. Not the bootstrap
//     because the reply must describe the conversation the client NAMED (#1610) —
//     a seeded conversation's bound session is a session minted for it. Both
//     halves are needed: non-empty alone would pass on a bootstrap leak, which is
//     precisely the defect, since a client writes its model / effort /
//     bypass-permissions choice straight back to whatever id it is handed.
//   - window_tokens is non-zero and used_tokens is zero. In stream mode the usage
//     seam used to be nil by construction, so every consumer reported a zero
//     window, which the desktop renders as "context usage unavailable" (#1214).
//     The minted session has no transcript, so the by-id reader collapses to the
//     fresh-session report — a default non-zero window against zero used — which
//     proves it resolved and degraded rather than never having been built.
//   - a BARE frame on the same open conn is answered with the all-zero payload.
//     That is the fail-closed direction against the real RunConfigFor producer,
//     which is the wiring where "unresolvable addresses nothing" actually matters.
//
// The conversation is seeded rather than assumed, and no turn is driven:
// create_conversation mints, binds and eagerly persists a dedicated session
// before it replies, so the conversation is addressable from the instant the
// client learns its id. That IS the desktop#491 case — a sheet opened on a
// conversation the user has never sent a message in — and it is why the seed
// costs one request rather than a turn.
func TestRelayV2_StreamRequestSessionSettings(t *testing.T) {
	const (
		initialUUID = "11111111-1111-4111-8111-111111111111"
		createReqID = uint64(1490)
		reqID       = uint64(1491)
		bareReqID   = uint64(1492)
	)

	home := shortHome(t)
	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	relayURL := fr.URL() + "/v2/server"

	// Set up one interactive device.
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

	h := StartStreamInteractiveWithRelay(t, home, initialUUID, relayURL)
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

	// Await the correlated reply of ANY type. Unsolicited frames (turn_state and
	// friends) interleave, so read until the in_reply_to matches — and correlate on
	// that rather than on the type, or a TypeError refusal would be drained past
	// and reported as a timeout instead of as the regression it is.
	awaitReply := func(want uint64) protocol.Envelope {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				t.Fatalf("phone A never received a reply to request_session_settings (id=%d); the verb is unanswered on the stream runner", want)
			}
			env := decryptInnerEnvelope(t, readInnerFrame(t, phoneA, remaining), recvA)
			if env.InReplyTo != nil && *env.InReplyTo == want {
				return env
			}
		}
	}

	// Seed the conversation the sheet is for. createConversationViaPhone drains
	// past interleaved broadcasts to the correlated conversation_created and
	// returns only after the daemon has minted, bound and eagerly persisted a
	// dedicated session — so the conversation is addressable with no turn driven.
	convID := createConversationViaPhone(t, phoneA, sendA, recvA, createReqID)

	// The frame NAMES that conversation. Since #1610 that name selects which
	// session the reply describes, not merely whether it is populated.
	sealSend(protocol.Envelope{
		ID:      reqID,
		Type:    protocol.TypeRequestSessionSettings,
		TS:      time.Now().UTC(),
		Payload: mustJSON(t, protocol.RequestSessionSettingsPayload{ConversationID: convID}),
	})
	reply := awaitReply(reqID)

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
	if got.SessionID == initialUUID {
		t.Errorf("session_id = %q, which is the BOOTSTRAP id — the reply must describe the session bound to the conversation the client named, "+
			"or the client writes its model / effort / bypass-permissions choice into a shared background session (#1610)", got.SessionID)
	}
	if got.WindowTokens == 0 {
		t.Error("window_tokens = 0 — the usage reader was never built on the stream runner, " +
			"which the desktop renders as \"context usage unavailable\" (#1214)")
	}
	if got.UsedTokens != 0 {
		t.Errorf("used_tokens = %d, want 0 — the minted session has no transcript, so this is a fresh session", got.UsedTokens)
	}

	// The fail-closed direction, against the real producer: a bare frame names no
	// conversation, so it addresses nothing and gets the all-zero payload — never
	// an error frame, and never the bootstrap session it used to be answered with.
	sealSend(protocol.Envelope{
		ID:   bareReqID,
		Type: protocol.TypeRequestSessionSettings,
		TS:   time.Now().UTC(),
	})
	bareReply := awaitReply(bareReqID)
	if bareReply.Type != protocol.TypeSessionSettings {
		t.Fatalf("bare request_session_settings reply Type = %q, want %q — an unaddressable request degrades to the zero payload, never an error frame",
			bareReply.Type, protocol.TypeSessionSettings)
	}
	var bare protocol.SessionSettingsPayload
	if err := json.Unmarshal(bareReply.Payload, &bare); err != nil {
		t.Fatalf("decode bare session_settings payload: %v", err)
	}
	if bare != (protocol.SessionSettingsPayload{}) {
		t.Errorf("bare request_session_settings payload = %+v, want the zero payload — a frame naming no conversation must address nothing, never the bootstrap session (#678 AC#4)", bare)
	}
}

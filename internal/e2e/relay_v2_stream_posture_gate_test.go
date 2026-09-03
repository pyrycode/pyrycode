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
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// #2064 AC 3 — a user turn does not reach a child whose permission posture claude has
// not confirmed, proven against a fake daemon whose child WITHHOLDS the ack.
//
// # Why this test is the negative arm and where its positive control lives
//
// The rider (PYRY_FAKE_CLAUDE_STREAM_WITHHOLD_MODE_ACK, #2067) makes the child read
// the set_permission_mode control request and answer nothing, so the gate this daemon
// armed at spawn can never open. That is DETERMINISTIC rather than merely slow: no
// deadline can be long enough for an ack that is never written.
//
// Its positive control is THE REST OF THIS PACKAGE. Every stream session in the
// fake-daemon suite stores the default posture, which the runner's allow-list admits,
// so every one of them now arms this gate and drives turns to completion anyway. A
// gate that never opened — a runner and a parser holding different gates, a release
// that never fires, a correlation that matches nothing — reddens the whole suite
// wholesale. There is nothing left for a same-file "and now it works" arm to add, and
// standing a second full relay harness up for one would cost a harness for no
// additional red.
//
// # The two assertions, and why neither is vacuous alone
//
// A bare "no assistant_delta arrived" would pass for any reason a turn might not
// land — a mis-seeded binding, a dropped frame, a daemon that never came up — which
// is the failure mode that makes an absence-only test worthless. So the absence is
// paired with a POSITIVE: the daemon's own refusal record must appear in its captured
// stderr, which fires only if the gate was actually armed and actually refused. Read
// together they say the gate held the turn, not that something else lost it.
//
// The send is ACKED before either assertion, which is the third guard: an unacked
// send means the enqueue was rejected (check the binding, #678) and everything below
// would be measuring a turn that was never accepted.
//
// # Why the ids must line up
//
// Same invariant as the sibling stream tests: the drain gate forwards an event to the
// emitter only when the event's sink tag == activeSession(). The sink tag is the
// runner's construction-time cfg.SessionID = the bootstrap pool id pinned by
// seedBootstrapRegistry to initialUUID; activeSession() resolves the active
// conversation → knownConvID's binding = initialUUID via seedBoundConversation.
func TestRelayV2_StreamTurnHeldUntilPostureAck(t *testing.T) {
	const (
		initialUUID = "11111111-1111-4111-8111-111111111111"
		knownConvID = "33333333-3333-4333-8333-333333333333"
		msgMarker   = "e2e-2064-held-turn"
		sendReqID   = uint64(20641)
		// The daemon's refusal record. Substring, not the whole line: slog renders
		// attributes after the message and the session id is not predictable here.
		refusalRecord = "turn refused; permission posture not yet confirmed"
	)

	home := shortHome(t)

	r := RunBareIn(t, home, "pair", "-pyry-name=test", "--name=phone-a")
	if r.ExitCode != 0 {
		t.Fatalf("pyry pair exit=%d\nstdout:\n%s\nstderr:\n%s", r.ExitCode, r.Stdout, r.Stderr)
	}
	payload := decodePairPayload(t, r.Stdout)
	pubKey, err := base64.StdEncoding.DecodeString(payload.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	seedBoundConversation(t, home, knownConvID, initialUUID)

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })

	// The child comes up, reads the daemon's set_permission_mode request, and answers
	// nothing. Everything else about it is an ordinary fakeclaude.
	h := StartStreamInteractiveWithRelay(t, home, initialUUID, fr.URL()+"/v2/server",
		"PYRY_FAKE_CLAUDE_STREAM_WITHHOLD_MODE_ACK=1")
	t.Cleanup(func() { h.Stop(t) })

	serverID := readPersistedServerID(t, home)
	waitBinaryHello(t, fr, serverID)

	dialCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	phone, err := fakephone.Dial(dialCtx, fr.URL(), serverID, payload.Token, "phone-a")
	if err != nil {
		t.Fatalf("phone dial: %v", err)
	}
	t.Cleanup(func() { _ = phone.Close() })
	sendCS, recvCS := driveHandshakeToOpenDaemonInteractive(t, phone, pubKey, payload.Token)

	env := protocol.Envelope{
		ID:      sendReqID,
		Type:    protocol.TypeSendMessage,
		TS:      time.Now().UTC(),
		Payload: mustJSON(t, protocol.SendMessagePayload{ConversationID: knownConvID, MessageID: "u-1", Text: msgMarker}),
	}
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	ciphertext, err := sendCS.Encrypt(raw)
	if err != nil {
		t.Fatalf("seal envelope: %v", err)
	}
	sendNoiseMsg(t, phone, ciphertext)

	// Drain frames until the deadline, collecting the ack (the vacuity positive) and
	// watching for any assistant_delta (the failure). Every noise_msg is decrypted in
	// capture order so the receive nonce stays in sequence.
	deadline := time.Now().Add(6 * time.Second)
	acked := false
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		rawFrame, err := phone.ReceiveBytes(remaining)
		if err != nil {
			if errors.Is(err, fakephone.ErrReceiveTimeout) {
				break
			}
			t.Fatalf("phone receive: %v", err)
		}
		var inner protocol.InnerFrameV2
		if err := json.Unmarshal(rawFrame, &inner); err != nil {
			t.Fatalf("decode inner frame: %v", err)
		}
		if inner.Type != protocol.TypeNoiseMsg {
			continue
		}
		got := decryptInnerEnvelope(t, inner, recvCS)
		switch got.Type {
		case protocol.TypeAck:
			// Correlated by InReplyTo, not by the ack's own ID — the daemon mints a
			// fresh envelope id for the reply and carries the request's in the field.
			if got.InReplyTo != nil && *got.InReplyTo == sendReqID {
				acked = true
			}
		case protocol.TypeAssistantDelta:
			t.Fatalf("an assistant_delta arrived while the posture was unconfirmed: %s", string(got.Payload))
		}
	}

	if !acked {
		t.Fatalf("the send was never acked, so no turn was ever accepted and the assertions below would be vacuous\ndaemon stderr:\n%s", h.Stderr.String())
	}
	if !strings.Contains(h.Stderr.String(), refusalRecord) {
		t.Fatalf("the daemon never recorded a posture refusal, so the turn was withheld by something other than this gate\ndaemon stderr:\n%s", h.Stderr.String())
	}
}

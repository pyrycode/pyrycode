//go:build e2e

package e2e

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// TestTwoPhoneCoarse_NonInteractiveOnly is the coarse-half proof for #634: the
// re-targeted #589 coarse emitter fans its finished-turn `message` envelope
// only to NON-interactive v2 conns. Two phones handshake to one daemon over
// real Noise sessions — phone A advertises (and is granted) the `interactive`
// capability, phone B advertises nothing. A turn is driven from B (whose ack we
// never await — it would error ~30s later as server.binary_offline; the only
// thing we need is the synchronous cursor stamp in WriteUserTurn,
// supervisor.go:206-208), then a fakeclaude stdout chunk is fanned out. We
// assert the NON-interactive phone B receives the coarse `message` and the
// `interactive` phone A receives none — the two complementary filters
// (`!c.Interactive` coarse vs `c.Interactive` structured) are mutually
// exclusive per conn.
//
// The `interactive` phone *receiving the structured stream* is the capstone
// follow-up #642 — not exercisable under fakeclaude (it writes only `{}\n` to
// its session JSONL while the structured producer tails the real claude
// transcript), so it is deliberately NOT asserted here.
func TestTwoPhoneCoarse_NonInteractiveOnly(t *testing.T) {
	const (
		knownConvID        = "55555555-5555-4555-8555-555555555555"
		knownUserText      = "e2e-634-user:hi\n"
		knownAssistantText = "e2e-634-assistant:coarse-only"
	)

	home := shortHome(t)

	// Pair two devices against the same instance: distinct bearer tokens, the
	// same per-instance server static pubkey (keys.LoadOrCreate is keyed by
	// instance name, so the second pair loads the first's static key).
	rA := RunBareIn(t, home, "pair", "-pyry-name=test", "--name=phone-a")
	if rA.ExitCode != 0 {
		t.Fatalf("pyry pair phone-a exit=%d\nstdout:\n%s\nstderr:\n%s", rA.ExitCode, rA.Stdout, rA.Stderr)
	}
	payloadA := decodePairPayload(t, rA.Stdout)
	rB := RunBareIn(t, home, "pair", "-pyry-name=test", "--name=phone-b")
	if rB.ExitCode != 0 {
		t.Fatalf("pyry pair phone-b exit=%d\nstdout:\n%s\nstderr:\n%s", rB.ExitCode, rB.Stdout, rB.Stderr)
	}
	payloadB := decodePairPayload(t, rB.Stdout)

	if payloadA.ServerStaticPubkey != payloadB.ServerStaticPubkey {
		t.Fatalf("expected a shared per-instance server static pubkey across pairings")
	}
	pubKey, err := base64.StdEncoding.DecodeString(payloadA.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	tmp := t.TempDir()
	// Align the sessions dir to the daemon's COMPUTED path (resolveClaudeSessionsDir
	// has no env override — always <HOME>/.claude/projects/encode(workdir), with
	// HOME=home and -pyry-workdir=home) so reconcileBootstrapOnNew rotates the
	// bootstrap session id to initialUUID — the id the binding below points at.
	// Unlike its structured sibling this test awaits no ack and asserts nothing
	// about JSONL content, so moving the sessions dir off tmp is inert to its
	// assertions; the only effect is making reconciliation set the bootstrap id.
	sessionsDir := filepath.Join(home, ".claude", "projects", encodeWorkdir(home))
	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		t.Fatalf("mkdir sessions dir: %v", err)
	}
	initialUUID := "44444444-4444-4444-8444-444444444444"
	// Pre-create <initialUUID>.jsonl BEFORE the daemon starts so reconciliation
	// finds it and rotates the bootstrap session id to initialUUID.
	initialJSONL := filepath.Join(sessionsDir, initialUUID+".jsonl")
	if err := os.WriteFile(initialJSONL, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("pre-create initial jsonl: %v", err)
	}
	// Bind knownConvID to the bootstrap session (== initialUUID after
	// reconciliation) so sessionRouter.Route resolves under #678's contract;
	// the ValidateConversation gate in WriteUserTurn then passes and the
	// supervisor cursor stamp lands.
	seedBoundConversation(t, home, knownConvID, initialUUID)
	rotateTrigger := filepath.Join(tmp, "rotate.trigger.never-created")
	stdinLog := filepath.Join(tmp, "fakeclaude-stdin.log")
	asstTrigger := filepath.Join(tmp, "assistant.trigger")

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })

	h := StartRotationWithRelay(t, home, sessionsDir, initialUUID, rotateTrigger,
		stdinLog, fr.URL()+"/v2/server",
		"PYRY_MOBILE_V2=1",
		"PYRY_FAKE_CLAUDE_ASSISTANT_TRIGGER="+asstTrigger,
	)
	t.Cleanup(func() { h.Stop(t) })

	serverID := readPersistedServerID(t, home)
	waitBinaryHello(t, fr, serverID)

	// Phone A: advertises interactive → granted → s.interactive=true → the
	// coarse emitter skips it.
	dialCtxA, cancelA := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelA()
	phoneA, err := fakephone.Dial(dialCtxA, fr.URL(), serverID, payloadA.Token, "phone-a")
	if err != nil {
		t.Fatalf("phone A dial: %v", err)
	}
	t.Cleanup(func() { _ = phoneA.Close() })
	_, recvA := driveHandshakeToOpenDaemonInteractive(t, phoneA, pubKey, payloadA.Token)

	// Phone B: no capabilities → s.interactive=false → the coarse emitter's
	// target.
	dialCtxB, cancelB := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelB()
	phoneB, err := fakephone.Dial(dialCtxB, fr.URL(), serverID, payloadB.Token, "phone-b")
	if err != nil {
		t.Fatalf("phone B dial: %v", err)
	}
	t.Cleanup(func() { _ = phoneB.Close() })
	sendB, recvB := driveHandshakeToOpenDaemon(t, phoneB, pubKey, payloadB.Token)

	// Drive a turn from phone B WITHOUT awaiting the ack: seal a send_message
	// and send it. We never read its reply — under fakeclaude WriteUserTurn's
	// WaitReady gate never reaches idle, so the ack errors ~30s later as
	// server.binary_offline (irrelevant). The synchronous cursor stamp at the
	// top of WriteUserTurn is all the coarse path needs.
	const sendReqID uint64 = 21
	reqEnv, err := json.Marshal(protocol.Envelope{
		ID:   sendReqID,
		Type: protocol.TypeSendMessage,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.SendMessagePayload{
			ConversationID: knownConvID,
			MessageID:      "u-1",
			Text:           knownUserText,
		}),
	})
	if err != nil {
		t.Fatalf("marshal send_message envelope: %v", err)
	}
	ciphertext, err := sendB.Encrypt(reqEnv)
	if err != nil {
		t.Fatalf("seal send_message envelope: %v", err)
	}
	sendNoiseMsg(t, phoneB, ciphertext)

	// Re-arm the assistant trigger from a background goroutine to defeat the
	// cursor-stamp-vs-emit race: if a fakeclaude emit raced ahead of the
	// synchronous cursor stamp it dropped (no cursor), so we keep re-writing the
	// trigger (fakeclaude re-emits on each, 50ms poll, repeat-firing) until the
	// reader signals it has the marker. Re-arming is filesystem-only and never
	// touches the phone connection — crucial, because fakephone closes the WS on
	// a timed-out Receive, so the read side must use a single long deadline and
	// read frames back-to-back (mirroring the #603 template) rather than a
	// short-timeout poll.
	stopArming := make(chan struct{})
	armingDone := make(chan struct{})
	go func() {
		defer close(armingDone)
		for {
			select {
			case <-stopArming:
				return
			default:
			}
			_ = os.WriteFile(asstTrigger, []byte(knownAssistantText), 0o600)
			time.Sleep(100 * time.Millisecond)
		}
	}()
	defer func() { close(stopArming); <-armingDone }()

	// Loop-until-marker on phone B with a single deadline, decrypting every
	// binary→phone frame in capture order (sequential receive nonce). Tolerate
	// non-`message` frames (e.g. the send_message's eventual binary_offline
	// error reply).
	//
	// TIMING (load-bearing — not a hang): the coarse message lands ~30s in, not
	// instantly. The send_message handler runs synchronously on the v2 manager's
	// single Run goroutine and blocks there inside WriteUserTurn's WaitReady gate
	// until sendMessageDeliverTimeout (30s) — fakeclaude renders no idle TUI, so
	// WaitReady never confirms. The cursor is stamped synchronously *before* that
	// gate (supervisor.go:206-208), so the coarse emit (driven independently off
	// the PTY chunk) has a valid cursor; but its ActiveConns/Push rendezvous on
	// the SAME Run goroutine, so the fan-out is serviced only once the 30s
	// WaitReady times out and Run returns to its select. The 40s deadline clears
	// that deterministic 30s floor with margin.
	startWait := time.Now()
	var matched protocol.Envelope
	var matchedPayload protocol.MessagePayload
	found := false
	deadline := time.Now().Add(40 * time.Second)
	for !found {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatalf("phone B did not receive the coarse message marker within %s", time.Since(startWait))
		}
		inner := readInnerFrame(t, phoneB, remaining)
		if inner.Type != protocol.TypeNoiseMsg {
			continue
		}
		env := decryptInnerEnvelope(t, inner, recvB)
		if env.Type != protocol.TypeMessage {
			continue
		}
		var p protocol.MessagePayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			t.Fatalf("phone B decode message payload: %v", err)
		}
		if !strings.Contains(p.Text, knownAssistantText) {
			continue
		}
		matched = env
		matchedPayload = p
		found = true
	}
	t.Logf("phone B received coarse message after %s (gated on the 30s WaitReady timeout)", time.Since(startWait))

	if matched.InReplyTo != nil {
		t.Errorf("matched.InReplyTo: got %v, want nil (server-initiated)", matched.InReplyTo)
	}
	if matchedPayload.ConversationID != knownConvID {
		t.Errorf("ConversationID: got %q, want %q", matchedPayload.ConversationID, knownConvID)
	}
	if matchedPayload.Role != "assistant" {
		t.Errorf("Role: got %q, want %q", matchedPayload.Role, "assistant")
	}
	if !conversations.ValidID(matchedPayload.MessageID) {
		t.Errorf("MessageID %q is not a valid UUIDv4", matchedPayload.MessageID)
	}

	// Phone A (interactive) must receive NO coarse `message`. The same
	// broadcast pass that pushed to B structurally skipped A. Drain A's inbound
	// for a bounded window; any TypeMessage is a double-delivery failure. Under
	// fakeclaude the structured producer never fires, so A receives nothing
	// turn-related at all.
	negDeadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(negDeadline) {
		raw, err := phoneA.ReceiveBytes(time.Until(negDeadline))
		if err != nil {
			if errors.Is(err, fakephone.ErrReceiveTimeout) {
				break // clean: nothing queued for A
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
		env := decryptInnerEnvelope(t, inner, recvA)
		if env.Type == protocol.TypeMessage {
			t.Fatalf("interactive phone A received a coarse message envelope (double-delivery): payload=%s", string(env.Payload))
		}
	}
}

// buildHelloEarlyInteractive mirrors buildHelloEarly (relay_v2_handshake_test.go)
// but advertises the interactive capability so the daemon grants it. Defined
// locally because relay_v2_handshake_test.go is off-limits (in-flight
// feature/449 overlap).
func buildHelloEarlyInteractive(t *testing.T, token string) []byte {
	t.Helper()
	payload, err := json.Marshal(protocol.HelloClientPayload{
		Role:             "client",
		DeviceName:       "v2-e2e-phone",
		ClientVersion:    "0.0.1-test",
		ProtocolVersions: []string{"v2"},
		Token:            token,
		Capabilities:     []string{protocol.CapabilityInteractive},
	})
	if err != nil {
		t.Fatalf("marshal interactive hello payload: %v", err)
	}
	envBytes, err := json.Marshal(protocol.Envelope{
		ID:      1,
		Type:    protocol.TypeHello,
		TS:      time.Now().UTC(),
		Payload: payload,
	})
	if err != nil {
		t.Fatalf("marshal interactive hello envelope: %v", err)
	}
	return envBytes
}

// driveHandshakeToOpenDaemonInteractive is driveHandshakeToOpenDaemon
// (relay_v2_daemon_test.go) with a capability-advertising hello. It also
// asserts the hello_ack early data echoes the interactive grant, pinning the
// test precondition that the coarse emitter must skip this conn.
func driveHandshakeToOpenDaemonInteractive(t *testing.T, phone *fakephone.Client, pubKey []byte, token string) (*noise.CipherState, *noise.CipherState) {
	t.Helper()
	initPriv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("phone keygen: %v", err)
	}
	initiator, err := noise.NewInitiator(initPriv.Bytes(), pubKey)
	if err != nil {
		t.Fatalf("NewInitiator: %v", err)
	}
	initMsg, err := initiator.WriteInit(buildHelloEarlyInteractive(t, token))
	if err != nil {
		t.Fatalf("WriteInit: %v", err)
	}
	sendNoiseInit(t, phone, initMsg)

	inner := readInnerFrame(t, phone, 3*time.Second)
	if inner.Type != protocol.TypeNoiseResp {
		t.Fatalf("handshake: got inner type %q, want %q", inner.Type, protocol.TypeNoiseResp)
	}
	respRaw, err := base64.StdEncoding.DecodeString(inner.Data)
	if err != nil {
		t.Fatalf("decode noise_resp data: %v", err)
	}
	earlyAck, initSend, initRecv, err := initiator.ReadResp(respRaw)
	if err != nil {
		t.Fatalf("initiator.ReadResp: %v", err)
	}

	var ackEnv protocol.Envelope
	if err := json.Unmarshal(earlyAck, &ackEnv); err != nil {
		t.Fatalf("decode hello_ack envelope: %v", err)
	}
	if ackEnv.Type != protocol.TypeHelloAck {
		t.Fatalf("early-data type = %q, want %q", ackEnv.Type, protocol.TypeHelloAck)
	}
	var ack protocol.HelloAckPayload
	if err := json.Unmarshal(ackEnv.Payload, &ack); err != nil {
		t.Fatalf("decode hello_ack payload: %v", err)
	}
	if !slices.Contains(ack.Capabilities, protocol.CapabilityInteractive) {
		t.Fatalf("daemon did not grant interactive (hello_ack capabilities=%v); test precondition unmet", ack.Capabilities)
	}
	return initSend, initRecv
}

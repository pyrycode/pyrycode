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

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// TestTwoPhoneStructured_InteractiveReceivesStream is the live structured-receive
// capstone for #642 (EPIC #596, ADR 025 § Phase 2). It is the LIVE confirmation
// that the capability-gated structured stream reaches an interactive phone while
// a non-interactive phone is kept out at the gate. Two phones handshake to ONE
// daemon over real Noise sessions; phone A is granted `interactive`, phone B is
// not. A drives a turn (stamping the supervisor cursor) and then real
// claude-format turn events are fed into the daemon's live structured-turn
// producer via fakeclaude. The test asserts:
//
//   - A (interactive) RECEIVES and decrypts the structured envelopes
//     (turn_state / assistant_delta / tool_use / turn_end) (AC1, AC2 A-side).
//   - B (non-interactive) receives NOTHING turn-related: neither a structured
//     envelope (the #632 capability gate skips it) nor a coarse `message` (the v2
//     coarse fan-out was removed in #699). A non-interactive conn gets no
//     assistant output at all — the delivery boundary is preserved (AC2/AC3
//     B-side).
//
// The structured events flow through the REAL production stack (tui-driver
// JSONL parse → turnbridge mapper → #632 capability-gated emitter → Noise seal
// → phone decrypt); only the claude *process* is the fakeclaude stand-in, as it
// is for every other e2e in this suite (option (b) of the ticket's harness
// choice).
//
// VACUOUS-PASS GUARD (security-relevant, the headline AC): B's "never receives
// a structured envelope" negative is meaningless unless the structured path is
// LIVE and OBSERVED on A in the same run. So A's assertion runs FIRST and
// `t.Fatal`s if A's structured set is empty/incomplete — that signals the
// harness produced no structured events, which would make B's negative
// vacuously true. AC4 (application output NEVER logged) is inherited from #589:
// the production emitters log only content-free discriminants and this test adds
// no logging, so it holds by construction.
func TestTwoPhoneStructured_InteractiveReceivesStream(t *testing.T) {
	const (
		knownConvID   = "55555555-5555-4555-8555-555555555555"
		knownUserText = "e2e-642-user:hi\n"
		initialUUID   = "44444444-4444-4444-8444-444444444444"
		sendReqID     = uint64(21)
	)

	// structuredTypes is the set of capability-gated structured envelope types.
	// A must receive (a superset of) the first four; B must receive NONE of
	// these.
	structuredTypes := map[string]bool{
		protocol.TypeTurnState:      true,
		protocol.TypeAssistantDelta: true,
		protocol.TypeToolUse:        true,
		protocol.TypeToolResult:     true,
		protocol.TypeTurnEnd:        true,
		protocol.TypeStall:          true,
	}
	// required is the minimal set A must observe to prove the structured path is
	// live (the vacuous-pass guard's positive half).
	required := map[string]bool{
		protocol.TypeTurnState:      false,
		protocol.TypeAssistantDelta: false,
		protocol.TypeToolUse:        false,
		protocol.TypeTurnEnd:        false,
	}
	allRequiredSeen := func() bool {
		for _, ok := range required {
			if !ok {
				return false
			}
		}
		return true
	}
	missingRequired := func() []string {
		var out []string
		for typ, ok := range required {
			if !ok {
				out = append(out, typ)
			}
		}
		slices.Sort(out)
		return out
	}

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

	// Bind knownConvID to the bootstrap session (== initialUUID after
	// reconciliation) so sessionRouter.Route resolves under #678's contract;
	// the ValidateConversation gate in WriteUserTurn then passes and the
	// supervisor cursor stamp lands.
	seedBoundConversation(t, home, knownConvID, initialUUID)

	// Align the sessions dir to the daemon's COMPUTED path so the structured
	// producer (resolveLatestSessionJSONL over claudeSessionsDir) tails exactly
	// what fakeclaude writes. resolveClaudeSessionsDir has no env override — it
	// always computes <HOME>/.claude/projects/encode(workdir) — so alignment is
	// by construction (HOME=home, -pyry-workdir=home), the rotation-test pattern.
	sessionsDir := filepath.Join(home, ".claude", "projects", encodeWorkdir(home))
	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		t.Fatalf("mkdir sessions dir: %v", err)
	}

	// Pre-create <initialUUID>.jsonl BEFORE the daemon starts so the producer's
	// first resolve succeeds immediately at a tiny offset (rotation_test.go
	// pattern). Without it the first resolve finds an empty dir, Warn-logs "no
	// session jsonl found", and retries ~500 ms later — a retry that can land
	// AFTER the post-ack fixture append and capture an EOF offset past the
	// fixture, so the producer would tail from beyond every event (the prior
	// run's cold-start race). With the file present at startup the offset is
	// captured seconds before the append, so every appended line lands inside
	// the tailed range.
	initialJSONL := filepath.Join(sessionsDir, initialUUID+".jsonl")
	if err := os.WriteFile(initialJSONL, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("pre-create initial jsonl: %v", err)
	}

	tmp := t.TempDir()
	rotateTrigger := filepath.Join(tmp, "rotate.trigger.never-created")
	stdinLog := filepath.Join(tmp, "fakeclaude-stdin.log")
	jsonlTrigger := filepath.Join(tmp, "structured.jsonl.trigger")

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })

	// TUI mode ON so WaitReady confirms fast and the send_message ack is prompt
	// (#603) — no 30 s WaitReady floor. The JSONL trigger feeds structured turn
	// events into the live producer on demand.
	h := StartRotationWithRelay(t, home, sessionsDir, initialUUID, rotateTrigger,
		stdinLog, fr.URL()+"/v2/server",
		"PYRY_MOBILE_V2=1",
		"PYRY_FAKE_CLAUDE_TUI=1",
		"PYRY_FAKE_CLAUDE_JSONL_TRIGGER="+jsonlTrigger,
	)
	t.Cleanup(func() { h.Stop(t) })

	serverID := readPersistedServerID(t, home)
	waitBinaryHello(t, fr, serverID)

	// Phone A: advertises interactive → granted → receives the structured stream
	// (and, being interactive, is skipped by the coarse emitter).
	dialCtxA, cancelA := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelA()
	phoneA, err := fakephone.Dial(dialCtxA, fr.URL(), serverID, payloadA.Token, "phone-a")
	if err != nil {
		t.Fatalf("phone A dial: %v", err)
	}
	t.Cleanup(func() { _ = phoneA.Close() })
	sendA, recvA := driveHandshakeToOpenDaemonInteractive(t, phoneA, pubKey, payloadA.Token)

	// Phone B: advertises nothing → not granted → the coarse emitter's target.
	// Pin the non-grant so B's "no structured" negative is airtight at the gate.
	dialCtxB, cancelB := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelB()
	phoneB, err := fakephone.Dial(dialCtxB, fr.URL(), serverID, payloadB.Token, "phone-b")
	if err != nil {
		t.Fatalf("phone B dial: %v", err)
	}
	t.Cleanup(func() { _ = phoneB.Close() })
	_, recvB := driveHandshakeNonInteractivePinned(t, phoneB, pubKey, payloadB.Token)

	// Drive the turn from A and AWAIT its sealed ack. The ack confirms
	// WriteUserTurn ran → the cursor is stamped (knownConvID) → the structured
	// emitter's Handle will see a non-empty cursor when the fixture tails. The
	// ack is the deterministic fence that the stamp precedes the trigger drop.
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
	ciphertext, err := sendA.Encrypt(reqEnv)
	if err != nil {
		t.Fatalf("seal send_message envelope: %v", err)
	}
	sendNoiseMsg(t, phoneA, ciphertext)

	// Decrypt-drain A to the sealed ack. No `message` is ever pushed to A (the v2
	// coarse fan-out was removed in #699), so A's pre-fixture stream is the ack
	// alone. The TypeMessage check below stays as a regression tripwire. Every
	// binary→phone frame is decrypted in capture order (sequential receive nonce).
	// TUI mode makes the ack prompt; the deadline allows for first-run fakeclaude
	// build + startup slack.
	var ackEnv protocol.Envelope
	ackDeadline := time.Now().Add(15 * time.Second)
	for {
		remaining := time.Until(ackDeadline)
		if remaining <= 0 {
			t.Fatal("interactive phone A did not receive the sealed send_message ack before deadline")
		}
		env := decryptInnerEnvelope(t, readInnerFrame(t, phoneA, remaining), recvA)
		if env.Type == protocol.TypeMessage {
			t.Fatalf("phone A received a `message` envelope before the ack; the v2 coarse fan-out was removed in #699 and no v2 path should mint one: payload=%s", string(env.Payload))
		}
		if env.Type == protocol.TypeAck {
			ackEnv = env
			break
		}
	}
	if ackEnv.InReplyTo == nil || *ackEnv.InReplyTo != sendReqID {
		t.Errorf("ack InReplyTo = %v, want pointer to %d", ackEnv.InReplyTo, sendReqID)
	}

	// After the ack, drop the JSONL trigger: its contents are a short sequence
	// of claude-format JSONL lines that exercise every structured envelope type.
	// fakeclaude appends them verbatim to its live session JSONL; the producer
	// (subscribed at startup, offset just past the pre-created {}) tails them →
	// turnbridge mapper → #632 emitter → sealed structured envelopes → A.
	//
	// Mapping (turnbridge/mapper_test.go is the oracle):
	//   1. assistant text          → turn_state(responding) + buffered delta
	//   2. assistant tool_use Read  → flush assistant_delta + tool_use
	//   3. user tool_result         → tool_result
	//   4. assistant text +end_turn → flush assistant_delta + turn_end + idle
	fixtureLines := []string{
		`{"type":"assistant","message":{"id":"m1","content":[{"type":"text","text":"Checking the file."}]}}`,
		`{"type":"assistant","message":{"id":"m2","content":[{"type":"tool_use","id":"tool-1","name":"Read","input":{"file_path":"/tmp/x"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tool-1","is_error":false,"content":"file body"}]}}`,
		`{"type":"assistant","message":{"id":"m3","stop_reason":"end_turn","content":[{"type":"text","text":"All done."}]}}`,
	}
	fixture := strings.Join(fixtureLines, "\n") + "\n"
	if err := os.WriteFile(jsonlTrigger, []byte(fixture), 0o600); err != nil {
		t.Fatalf("write structured jsonl trigger: %v", err)
	}

	// --- AC1 + AC2/AC3 (A side) + vacuous-pass guard. Single decrypt-drain on A
	// under one deadline (fakephone closes the WS on a timed-out Receive, so a
	// single long deadline with back-to-back reads, never a short poll). Collect
	// structured types; break once all four required types are seen; fail loud
	// on any `message` (the v2 coarse path is gone post-#699 — a regression
	// tripwire); assert every structured payload addresses the cursor'd
	// conversation.
	failVacuous := func(seen int) {
		t.Fatalf("interactive phone A did not receive the full structured stream within the deadline "+
			"(observed %d structured envelope(s); missing required types %v). The structured path must be "+
			"LIVE and observed on A in this same run for B's \"no structured\" negative to mean anything "+
			"(vacuous-pass guard); an empty/partial set here means the harness produced no structured events.",
			seen, missingRequired())
	}
	seenStructured := 0
	aDeadline := time.Now().Add(20 * time.Second)
	for !allRequiredSeen() {
		remaining := time.Until(aDeadline)
		if remaining <= 0 {
			failVacuous(seenStructured)
		}
		raw, err := phoneA.ReceiveBytes(remaining)
		if err != nil {
			if errors.Is(err, fakephone.ErrReceiveTimeout) {
				failVacuous(seenStructured)
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
			t.Fatalf("phone A received a `message` envelope; the v2 coarse fan-out was removed in #699 and no v2 path should mint one: payload=%s", string(env.Payload))
		}
		if !structuredTypes[env.Type] {
			continue
		}
		seenStructured++
		var hdr struct {
			ConversationID string `json:"conversation_id"`
		}
		if err := json.Unmarshal(env.Payload, &hdr); err != nil {
			t.Fatalf("phone A decode structured %s payload: %v", env.Type, err)
		}
		if hdr.ConversationID != knownConvID {
			t.Errorf("structured %s ConversationID: got %q, want %q", env.Type, hdr.ConversationID, knownConvID)
		}
		if _, ok := required[env.Type]; ok {
			required[env.Type] = true
		}
	}
	t.Logf("interactive phone A received the full structured stream (%d envelopes incl. turn_state/assistant_delta/tool_use/turn_end)", seenStructured)

	// --- AC2/AC3 (B side). The NON-interactive phone receives NOTHING
	// turn-related: the #632 capability gate keeps it off the structured stream,
	// and the v2 coarse `message` fan-out was removed in #699, so no coarse
	// envelope is minted for it either. All of B's frames are already queued by
	// now (any structured leak would have been pushed during A's now-complete
	// structured drain), so a bounded drain that surfaces no turn-related
	// envelope confirms the delivery boundary. fakephone closes the WS on a
	// timed-out Receive, so use a single deadline and read back-to-back: queued
	// frames return at wire speed and only the terminal empty read consumes the
	// deadline.
	bDeadline := time.Now().Add(5 * time.Second)
	for {
		remaining := time.Until(bDeadline)
		if remaining <= 0 {
			break
		}
		raw, err := phoneB.ReceiveBytes(remaining)
		if err != nil {
			if errors.Is(err, fakephone.ErrReceiveTimeout) {
				break // B's stream is drained — no further frames
			}
			t.Fatalf("phone B receive: %v", err)
		}
		var inner protocol.InnerFrameV2
		if err := json.Unmarshal(raw, &inner); err != nil {
			t.Fatalf("phone B decode inner frame: %v", err)
		}
		if inner.Type != protocol.TypeNoiseMsg {
			continue
		}
		env := decryptInnerEnvelope(t, inner, recvB)
		if structuredTypes[env.Type] {
			t.Fatalf("non-interactive phone B received a structured envelope %q (capability-gate leak): payload=%s", env.Type, string(env.Payload))
		}
		if env.Type == protocol.TypeMessage {
			t.Fatalf("non-interactive phone B received a coarse `message` envelope; the v2 coarse fan-out was removed in #699 and no v2 path should mint one: payload=%s", string(env.Payload))
		}
	}
}

// driveHandshakeNonInteractivePinned mirrors driveHandshakeToOpenDaemon
// (relay_v2_daemon_test.go) but additionally asserts the hello_ack early data
// does NOT echo the interactive grant — the complement of
// driveHandshakeToOpenDaemonInteractive's pin (below). This makes phone B's
// "never receives a structured envelope" negative airtight at the capability-gate
// boundary: a mis-granted B would otherwise only fail downstream (when it
// received a structured envelope), so pinning the non-grant closes the gap
// between "advertised nothing" and "was not granted interactive". Defined locally
// (not in relay_v2_handshake_test.go) alongside the other v2 two-phone helpers.
func driveHandshakeNonInteractivePinned(t *testing.T, phone *fakephone.Client, pubKey []byte, token string) (*noise.CipherState, *noise.CipherState) {
	t.Helper()
	initPriv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("phone keygen: %v", err)
	}
	initiator, err := noise.NewInitiator(initPriv.Bytes(), pubKey)
	if err != nil {
		t.Fatalf("NewInitiator: %v", err)
	}
	initMsg, err := initiator.WriteInit(buildHelloEarly(t, token))
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
	if slices.Contains(ack.Capabilities, protocol.CapabilityInteractive) {
		t.Fatalf("daemon granted interactive to a phone that advertised none (hello_ack capabilities=%v); B's no-structured negative would be testing the wrong gate state", ack.Capabilities)
	}
	return initSend, initRecv
}

// buildHelloEarlyInteractive mirrors buildHelloEarly (relay_v2_handshake_test.go)
// but advertises the interactive capability so the daemon grants it. Defined here
// (not in relay_v2_handshake_test.go) alongside the other v2 two-phone helpers.
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
// (relay_v2_daemon_test.go) with a capability-advertising hello. It also asserts
// the hello_ack early data echoes the interactive grant, pinning the precondition
// that this conn is on the structured-stream path (the grant phone A relies on).
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

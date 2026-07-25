//go:build e2e

package e2e

// This file is the #1195 RED->GREEN oracle for the PTY-tier test substrate: a MINTED
// per-conversation interactive session (a conversation created over the wire, so the
// daemon mints a dedicated claude session for it) must produce conversation-scoped
// turn lifecycle for ITS OWN session — at minimum one turn_end carrying that
// conversation's id.
//
// Before this ticket that was observable on ZERO PTY-tier tests, structurally. A
// turn_end exists on exactly one code path — tuidriver.EventKindJsonlEndOfTurn
// (internal/turnbridge/mapper.go), which is TRANSCRIPT-derived — and for a minted
// conversation the daemon tails <convDir>/<mintedSessionID>.jsonl
// (resolveBoundSessionJSONL). But fakeclaude parsed no argv: it bound its stem to
// PYRY_FAKE_CLAUDE_INITIAL_UUID, a process-wide value every child inherits
// identically, so the minted child wrote <sharedDir>/<INITIAL_UUID>.jsonl. The two
// could never agree, the subscription never opened, and the conversation emitted no
// turn events. The fix is entirely in the fake — two default-off knobs
// (PYRY_FAKE_CLAUDE_SESSION_ID_FROM_ARGV, PYRY_FAKE_CLAUDE_JSONL_TRIGGER_DIR). The
// daemon-side resolver is untouched: the fake moves to meet the daemon, never the
// reverse.
//
// The turn end is driven by a file drop, NOT by an interrupt: #1191 (blocked on this
// ticket) owns the minted-interrupt oracle, and proving the substrate with an
// interrupt here would leave that test a duplicate.
//
// Substrate-clean: asserts only wire fields (conversation_id, stop_reason) and a
// test-authored marker string, never claude's rendered words.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// mintedTurnEndMarker is unique to this test. It is what makes the on-disk
// targeting assertion sharp: the marker may appear ONLY in the minted session's
// transcript, never in the shared bootstrap one.
const mintedTurnEndMarker = "e2e-1195:minted-turn-end"

// mintedEndTurnLine is the claude-format JSONL line the test injects into the
// minted child's transcript — an assistant message with stop_reason "end_turn",
// the shape the turnbridge mapper turns into EventKindJsonlEndOfTurn -> turn_end
// (the fixture shape at relay_two_phone_structured_test.go). Test-authored, so it
// is inert substrate, and deliberately NOT the fake's own interruptEndTurnLine —
// this test must not depend on the interrupt route.
const mintedEndTurnLine = `{"type":"assistant","message":{"id":"m-1195","stop_reason":"end_turn","content":[{"type":"text","text":"` +
	mintedTurnEndMarker + `"}]}}` + "\n"

func TestRelayV2_PerConversationTurnEnd(t *testing.T) {
	const (
		initialUUID = "77777777-7777-4777-8777-777777777777"
		createReqID = uint64(30)
		sendReqID   = uint64(31)
	)

	home := shortHome(t)

	// Pair one interactive device. This test only OBSERVES turn lifecycle, so no
	// permission-granting capability is needed.
	r := RunBareIn(t, home, "pair", "-pyry-name=test", "--name=phone-a")
	if r.ExitCode != 0 {
		t.Fatalf("pyry pair exit=%d\nstdout:\n%s\nstderr:\n%s", r.ExitCode, r.Stdout, r.Stderr)
	}
	payload := decodePairPayload(t, r.Stdout)
	pubKey, err := base64.StdEncoding.DecodeString(payload.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	// Align the sessions dir to the daemon's COMPUTED path and pre-create
	// <initialUUID>.jsonl BEFORE the daemon starts so the bootstrap session id
	// reconciles cleanly (the #791/#792 rotation-test pattern). An all-null
	// create_conversation resolves its spawn dir to "" and the pool spawns in
	// tpl.WorkDir — the bootstrap workdir — so perConversationSessionsDir maps the
	// minted session's convDir back to this same shared dir. Only the FILENAME
	// diverges, which is exactly what the stem knob reconciles.
	sessionsDir := claudeSessionsDir(home)
	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		t.Fatalf("mkdir sessions dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionsDir, initialUUID+".jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("pre-create initial jsonl: %v", err)
	}

	// The per-child JSONL trigger dir must exist before the daemon (and therefore
	// its children) start; the fake only ever renames out of it.
	trigDir := filepath.Join(home, "jsonl-trig")
	if err := os.MkdirAll(trigDir, 0o700); err != nil {
		t.Fatalf("mkdir jsonl trigger dir: %v", err)
	}

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })

	neverRotate := filepath.Join(home, "never-rotate.trig")
	stdinLog := filepath.Join(home, "stdin.log")

	// Both new knobs are set on the DAEMON, so both children inherit them — and the
	// bootstrap child is nevertheless unaffected. seedBootstrapRegistry (called by
	// StartRotationWithRelay) pins the bootstrap pool id to initialUUID, so its
	// spawn argv carries "--session-id <initialUUID>" / "--resume <initialUUID>" and
	// the stem knob resolves to the same value PYRY_FAKE_CLAUDE_INITIAL_UUID would
	// have given: byte-identical by construction. Its per-child trigger path is
	// <trigDir>/<initialUUID>.jsonl.trig, which this test never creates, so the
	// injection knob is inert for it too.
	StartRotationWithRelay(t, home, sessionsDir, initialUUID, neverRotate, stdinLog,
		fr.URL()+"/v2/server",
		"PYRY_MOBILE_V2=1",
		"PYRY_FAKE_CLAUDE_TUI=1",
		"PYRY_FAKE_CLAUDE_SESSION_ID_FROM_ARGV=1",
		"PYRY_FAKE_CLAUDE_JSONL_TRIGGER_DIR="+trigDir,
	)

	serverID := readPersistedServerID(t, home)
	waitBinaryHello(t, fr, serverID)

	dialCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	phone, err := fakephone.Dial(dialCtx, fr.URL(), serverID, payload.Token, "phone-a")
	if err != nil {
		t.Fatalf("phone dial: %v", err)
	}
	t.Cleanup(func() { _ = phone.Close() })

	// Interactive handshake grants the `interactive` capability the turn-lifecycle
	// broadcasts ride. Keep the raw CipherStates so we can both create/send AND drain.
	sendCS, recvCS := driveHandshakeToOpenDaemonInteractive(t, phone, pubKey, payload.Token)

	sealSend := func(env protocol.Envelope) {
		t.Helper()
		raw, err := json.Marshal(env)
		if err != nil {
			t.Fatalf("marshal envelope: %v", err)
		}
		ciphertext, err := sendCS.Encrypt(raw)
		if err != nil {
			t.Fatalf("seal envelope: %v", err)
		}
		sendNoiseMsg(t, phone, ciphertext)
	}

	// nextEnv decrypts the next binary->phone application envelope, skipping non-noise_msg
	// inner frames, in capture order so the receive nonce stays in sequence. One recvCS
	// is used for the whole test (create reply, send ack, turn_end). ok=false on deadline.
	nextEnv := func(deadline time.Time) (protocol.Envelope, bool) {
		t.Helper()
		for {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return protocol.Envelope{}, false
			}
			raw, err := phone.ReceiveBytes(remaining)
			if err != nil {
				if errors.Is(err, fakephone.ErrReceiveTimeout) {
					return protocol.Envelope{}, false
				}
				t.Fatalf("phone receive: %v", err)
			}
			var inner protocol.InnerFrameV2
			if err := json.Unmarshal(raw, &inner); err != nil {
				t.Fatalf("decode inner frame: %v", err)
			}
			if inner.Type != protocol.TypeNoiseMsg {
				continue
			}
			return decryptInnerEnvelope(t, inner, recvCS), true
		}
	}

	// --- Mint a per-conversation session over the wire. All-null create_conversation
	// (server defaults), so the spawn dir resolves to the bootstrap workdir and the
	// minted transcript lands in the shared sessions dir. The interactive session may
	// interleave broadcasts, so drain until the conversation_created reply.
	sealSend(protocol.Envelope{
		ID:      createReqID,
		Type:    protocol.TypeCreateConversation,
		TS:      time.Now().UTC(),
		Payload: mustJSON(t, protocol.CreateConversationPayload{}),
	})
	var convID string
	createDeadline := time.Now().Add(15 * time.Second)
	for convID == "" {
		env, ok := nextEnv(createDeadline)
		if !ok {
			t.Fatal("did not receive conversation_created before deadline (the minted per-conversation session never came up)")
		}
		if env.Type == protocol.TypeError {
			t.Fatalf("unexpected error envelope while awaiting conversation_created: %s", string(env.Payload))
		}
		if env.Type != protocol.TypeConversationCreated {
			continue
		}
		var p protocol.ConversationCreatedPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			t.Fatalf("decode conversation_created payload: %v", err)
		}
		if p.ID == "" {
			t.Fatal("conversation_created carried an empty id")
		}
		convID = p.ID
	}

	// The wire reply carries the conversation id, not the session id; the injection
	// path is keyed on the SESSION stem, so read the binding off disk (#680's helper).
	convPath := filepath.Join(home, ".pyry", "test", "conversations.json")
	mintedID := boundSessionID(t, convPath, convID)
	t.Logf("minted per-conversation session %s for conversation %s", mintedID, convID)

	// --- Route a turn to the minted conversation so the active cursor advances onto
	// it: resolveTarget re-keys the turn-stream subscription on
	// active.CurrentConversation, so nothing for this conversation can arrive before
	// the cursor moves. Await the sealed ack.
	sealSend(protocol.Envelope{
		ID:   sendReqID,
		Type: protocol.TypeSendMessage,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.SendMessagePayload{
			ConversationID: convID,
			MessageID:      "u-1",
			Text:           "e2e-1195:go\n",
		}),
	})
	ackDeadline := time.Now().Add(15 * time.Second)
	gotAck := false
	for !gotAck {
		env, ok := nextEnv(ackDeadline)
		if !ok {
			t.Fatal("did not receive the send_message ack for the minted conversation before deadline")
		}
		if env.Type == protocol.TypeError {
			t.Fatalf("unexpected error envelope while awaiting the send_message ack: %s", string(env.Payload))
		}
		if env.Type == protocol.TypeAck && env.InReplyTo != nil && *env.InReplyTo == sendReqID {
			gotAck = true
		}
	}

	// --- Drive the turn end from a file drop into the MINTED child's own trigger
	// path. The re-drop kicker is mandatory, not defensive polish (#929): the
	// producer waits one subscribeRetryDelay before subscribing and then tails from
	// EOF, so a single-shot append lands BELOW the tailed range and is never emitted.
	// Re-dropping is safe here in a way #930's blind re-drop is not — the only
	// content ever dropped is this one end-of-turn line, so re-firing can at worst
	// produce a second turn_end, and the drain accepts the first match.
	mintedTrig := filepath.Join(trigDir, mintedID+".jsonl.trig")
	stopKick := make(chan struct{})
	var stopKickOnce sync.Once
	stopKicking := func() { stopKickOnce.Do(func() { close(stopKick) }) }
	t.Cleanup(stopKicking)
	go func() {
		for {
			_ = os.WriteFile(mintedTrig, []byte(mintedEndTurnLine), 0o600)
			select {
			case <-stopKick:
				return
			case <-time.After(250 * time.Millisecond):
			}
		}
	}()

	// --- AC1: a turn_end scoped to the minted conversation, where the PTY tier
	// produced none before this ticket.
	var end protocol.TurnEndPayload
	turnDeadline := time.Now().Add(15 * time.Second)
	gotEnd := false
	for !gotEnd {
		env, ok := nextEnv(turnDeadline)
		if !ok {
			t.Fatalf("interactive phone A never observed a turn_end for the minted conversation %s within the deadline. "+
				"The minted child's transcript stem and the daemon's tail target must agree: the daemon tails "+
				"<sharedDir>/%s.jsonl (resolveBoundSessionJSONL), so the child must write that file "+
				"(PYRY_FAKE_CLAUDE_SESSION_ID_FROM_ARGV) and claim %s (PYRY_FAKE_CLAUDE_JSONL_TRIGGER_DIR)",
				convID, mintedID, mintedTrig)
		}
		if env.Type == protocol.TypeError {
			t.Fatalf("unexpected error envelope while awaiting turn_end: %s", string(env.Payload))
		}
		if env.Type != protocol.TypeTurnEnd {
			continue
		}
		if err := json.Unmarshal(env.Payload, &end); err != nil {
			t.Fatalf("decode turn_end payload: %v", err)
		}
		gotEnd = true
	}
	stopKicking()

	if end.ConversationID != convID {
		t.Fatalf("turn_end ConversationID = %q, want the minted conversation %q "+
			"(a minted per-conversation session's turn lifecycle must fan scoped to its own conversation)",
			end.ConversationID, convID)
	}
	if end.StopReason != "end_turn" {
		t.Errorf("turn_end StopReason = %q, want %q", end.StopReason, "end_turn")
	}
	t.Logf("minted per-conversation session fanned turn_end (turn_id=%s) scoped to conversation %s", end.TurnID, convID)

	// --- AC2 (non-vacuity), asserted on disk. turn_end.ConversationID is stamped
	// from the ACTIVE CURSOR, not from the transcript the event came out of, so the
	// wire assertion above proves only what the emitter believed. The on-disk pair
	// proves the TARGETING: the injected marker must be in the minted session's own
	// transcript and must NOT be in the shared bootstrap one. If the stem knob
	// failed and the minted child wrote <initialUUID>.jsonl, a turn-end produced by
	// the bootstrap child could never satisfy this.
	mintedPath := filepath.Join(sessionsDir, mintedID+".jsonl")
	mintedBody, err := os.ReadFile(mintedPath)
	if err != nil {
		t.Fatalf("read minted transcript %s: %v (the minted child must write the file the daemon tails)", mintedPath, err)
	}
	if !strings.Contains(string(mintedBody), mintedTurnEndMarker) {
		t.Errorf("minted transcript %s does not contain the injected end-of-turn marker %q; "+
			"the turn_end above did not come from the minted session's own transcript", mintedPath, mintedTurnEndMarker)
	}
	bootstrapPath := filepath.Join(sessionsDir, initialUUID+".jsonl")
	bootstrapBody, err := os.ReadFile(bootstrapPath)
	if err != nil {
		t.Fatalf("read bootstrap transcript %s: %v", bootstrapPath, err)
	}
	if strings.Contains(string(bootstrapBody), mintedTurnEndMarker) {
		t.Errorf("bootstrap transcript %s contains the injected end-of-turn marker %q; "+
			"the minted child wrote into the SHARED bootstrap transcript instead of its own, "+
			"so the turn_end proves nothing about per-conversation targeting", bootstrapPath, mintedTurnEndMarker)
	}
	if mintedPath == bootstrapPath {
		t.Fatalf("minted and bootstrap transcript paths are identical (%s); the non-vacuity assertion is meaningless", mintedPath)
	}
}

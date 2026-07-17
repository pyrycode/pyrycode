//go:build e2e

package e2e

// This file is the #1066 reproduction + regression oracle: a MINTED per-conversation
// interactive session (a conversation created over the wire, so the daemon mints a
// dedicated claude session for it), started WITHOUT --dangerously-skip-permissions,
// must fan modal_shown for a permission-gated tool, scoped to its own conversation
// (#1065 conversation_id). Modal detection is proven for the BOOTSTRAP session by #791
// (relay_v2_modal_answer_test.go) and #1030 (real claude); this pins the MINTED-session
// path, which the #791/#793 harnesses never exercise (they raise the modal on the
// bootstrap session, convID=="").
//
// SKIP-GATED (2026-07-17). Hand-building #1066 reproduced this RED, but it is a
// two-layer daemon wedge that the fake tier cannot drive GREEN faithfully, and both
// layers are now their own tickets that BLOCK #1066:
//
//   - #1069 — the delivery readiness gate rejects the permission modal as an
//     "unexpected modal at startup: permission" and msgqueue retries the turn forever
//     (WriteUserTurn -> Session.WaitReady -> UnexpectedModalError).
//   - #1070 — the modal stream's Session.Events() subscription is gated on the
//     per-conversation transcript via WaitForSessionJSONL (turnbridge/producer.go), but
//     a modal is PTY-screen-derived and a permission-blocked minted claude writes no
//     transcript, so the subscription never opens and modal_shown never fans. Likely a
//     tui-driver screen-only Events change.
//
// The fake claude also cannot produce a minted session's per-Cwd transcript (it derives
// one shared stem from PYRY_FAKE_CLAUDE_INITIAL_UUID, not --session-id; see
// per_conversation_eviction_test.go), so this test cannot be driven GREEN hermetically
// today, and real-claude PTY-modal detection is toolchain-blocked (claude 2.x vs
// tui-driver 1.10.0). Un-skip once #1069 + #1070 land AND the toolchain lock is resolved:
// with #1070's decoupling the modal no longer needs the (absent) transcript, so this
// becomes the RED->GREEN oracle. Substrate-clean: asserts only wire fields (class,
// modal_id, conversation_id), never claude's rendered words.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func TestRelayV2_PerConversationModalShown(t *testing.T) {
	t.Skip("#1066: minted per-conversation modal fan-out is a two-layer daemon wedge; " +
		"blocked on #1069 (readiness gate rejects the permission modal) and #1070 (modal stream " +
		"coupled to the per-conversation transcript). Not faithfully driveable GREEN on the fake tier " +
		"(fakeclaude cannot produce a minted per-Cwd transcript), and real-claude is toolchain-blocked " +
		"(claude 2.x vs tui-driver 1.10.0). Un-skip once #1069+#1070 land and the toolchain lock clears.")

	const (
		initialUUID = "44444444-4444-4444-8444-444444444444"
		createReqID = uint64(20)
		sendReqID   = uint64(21)
	)

	home := shortHome(t)

	// Pair one interactive device. Viewing a modal is ungated (#607), so no
	// --allow-remote-permissions is needed — this test only OBSERVES modal_shown.
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
	// reconciles cleanly (the #791/#792 rotation-test pattern). Per-conversation
	// sessions take the nil-resolver path and never read this file.
	sessionsDir := claudeSessionsDir(home)
	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		t.Fatalf("mkdir sessions dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionsDir, initialUUID+".jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("pre-create initial jsonl: %v", err)
	}

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })

	neverRotate := filepath.Join(home, "never-rotate.trig")
	modalTrig := filepath.Join(home, "modal.trig")
	stdinLog := filepath.Join(home, "stdin.log")

	// Modal-trigger fakeclaude (v2 relay, TUI mode). Per-conversation minting is an
	// always-on daemon feature: create_conversation spawns a fresh fakeclaude for the
	// minted session, and it inherits PYRY_FAKE_CLAUDE_MODAL_TRIGGER, so it raises a
	// permission modal on the trigger's first appearance exactly like the bootstrap one.
	StartRotationWithRelay(t, home, sessionsDir, initialUUID, neverRotate, stdinLog,
		fr.URL()+"/v2/server",
		"PYRY_MOBILE_V2=1",
		"PYRY_FAKE_CLAUDE_TUI=1",
		"PYRY_FAKE_CLAUDE_MODAL_TRIGGER="+modalTrig,
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

	// Interactive handshake grants the `interactive` capability the modal broadcast
	// rides. Keep the raw CipherStates so we can both create/send AND drain broadcasts.
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
	// is used for the whole test (create reply, send ack, modal_shown). ok=false on deadline.
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
	// (server defaults). The interactive session may interleave broadcasts, so drain
	// until the conversation_created reply rather than assuming the next frame is it.
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
	t.Logf("minted per-conversation session for conversation %s", convID)

	// --- Route a turn to the minted conversation so the active cursor advances and the
	// modal stream follows onto the minted session (mirrors the interrupt capstone's
	// cursor-stamp step). Await the sealed ack.
	sealSend(protocol.Envelope{
		ID:   sendReqID,
		Type: protocol.TypeSendMessage,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.SendMessagePayload{
			ConversationID: convID,
			MessageID:      "u-1",
			Text:           "e2e-1066:go\n",
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

	// --- Raise the permission modal on the (now-followed) minted session and drain for
	// modal_shown via the frozen #791 awaitModalShown, then assert the #1065 scoping.
	h := &modalHarness{
		phone:     phone,
		sealSend:  sealSend,
		nextEnv:   nextEnv,
		stdinLog:  stdinLog,
		modalTrig: modalTrig,
	}
	shown := awaitModalShown(t, h)

	if shown.Class != "permission" {
		t.Fatalf("modal_shown Class = %q, want %q", shown.Class, "permission")
	}
	// The security-relevant assertion (#1065 scoping): a minted-session modal must be
	// stamped for its OWN conversation — not "" (bootstrap) and not another conv.
	if shown.ConversationID != convID {
		t.Fatalf("modal_shown ConversationID = %q, want the minted conversation %q "+
			"(a minted per-conversation session's permission modal must fan scoped to its own conversation)",
			shown.ConversationID, convID)
	}
	t.Logf("minted per-conversation session fanned modal_shown (modal_id=%s) scoped to conversation %s", shown.ModalID, convID)
}

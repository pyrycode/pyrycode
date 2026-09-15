//go:build e2e

package e2e

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
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

// TestRelayV2_NewSessionRefusedWorkspaceRepliesToRequester is #2443's fake-daemon
// proof: a paired phone drives a new_session against a real spawned daemon whose
// conversation records a workspace that has since been re-pointed OUTSIDE $HOME,
// and receives a coded error frame correlated to the frame it sent — while the
// rotation itself still completes.
//
// THE TWO ASSERTIONS ARE A MATCHED PAIR and neither alone would do. The error
// frame is AC-1: the operator is told the fresh session did not start where they
// recorded it, instead of being left to believe a rotation moved a conversation
// that is still running in the old directory. The session_transition is AC-2: the
// rotation still happened, so this is a caveat on a success and not a failure
// dressed as one. A daemon that answered the frame by refusing to rotate would
// pass the first and fail the second.
//
// THE ESCAPE IS INSTALLED AFTER THE CHILD IS LIVE, which is the ticket's own
// scenario ("deleted or moved outside $HOME SINCE I set it") and also what keeps
// the test honest: seeding an escaping cwd from the start would leave the daemon's
// startup and the send path free to trip over it first, and the refusal under test
// would be one of several candidate explanations for a failure. The escape is a
// symlink rather than a deletion or a file because those two do NOT refuse —
// confineWorkdirToHomeCreating re-creates a missing directory and accepts an
// existing regular file inside $HOME. A symlink is the one mutation that makes the
// recorded path resolve outside the boundary, which is the condition
// `confineWorkdirToHomeCreating`'s first containment check rejects.
//
// No live claude is involved: the fake-claude harness serves the turn that brings
// the child up, which is all the rotation needs.
func TestRelayV2_NewSessionRefusedWorkspaceRepliesToRequester(t *testing.T) {
	const (
		initialUUID   = "11111111-1111-4111-8111-111111111111" // bootstrap, bound to the conversation
		convID        = "55555555-5555-4555-8555-555555555555"
		textToConv    = "e2e-2443-user:hello\n"
		needle        = "e2e-2443-user:hello"
		sendReqID     = uint64(2443)
		rotateReqID   = uint64(2444)
		workspaceName = "recorded-workspace"
	)

	home := shortHome(t)
	// The workspace the conversation records: a real directory under $HOME while
	// the child comes up, re-pointed outside $HOME before the rotation.
	recordedWS := filepath.Join(home, workspaceName)
	if err := os.MkdirAll(recordedWS, 0o700); err != nil {
		t.Fatalf("mkdir recorded workspace: %v", err)
	}
	escaped := t.TempDir() // a sibling temp dir, outside the daemon's p-301-* $HOME

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	relayURL := fr.URL() + "/v2/server"

	payload, err := paireddevice.Setup(paireddevice.Config{
		Home:                   home,
		InstanceName:           "test",
		Relay:                  relayURL,
		DeviceName:             "phone-a",
		AllowRemotePermissions: false,
	})
	if err != nil {
		t.Fatalf("setup paired device: %v", err)
	}
	pubKey, err := base64.StdEncoding.DecodeString(payload.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	// Seed the conversation bound to the bootstrap session with the workspace
	// recorded — the shape change_workspace leaves behind. Written inline rather
	// than through seedBoundConversation, whose cwd is fixed at $HOME and which is
	// shared with the e2e_install build.
	convPath := filepath.Join(home, ".pyry", "test", "conversations.json")
	if err := os.MkdirAll(filepath.Dir(convPath), 0o700); err != nil {
		t.Fatalf("mkdir registry dir: %v", err)
	}
	convJSON := []byte(`{"conversations":[{"id":"` + convID +
		`","cwd":"` + recordedWS +
		`","current_session_id":"` + initialUUID +
		`","is_promoted":false,"last_used_at":"2026-01-01T00:00:00Z"}]}`)
	if err := os.WriteFile(convPath, convJSON, 0o600); err != nil {
		t.Fatalf("seed conversations.json: %v", err)
	}

	h := StartStreamInteractiveWithRelay(t, home, initialUUID, relayURL)
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
	send, recv := driveHandshakeToOpenDaemonInteractive(t, phone, pubKey, payload.Token)

	sealSend := func(env protocol.Envelope) {
		t.Helper()
		raw, err := json.Marshal(env)
		if err != nil {
			t.Fatalf("marshal envelope: %v", err)
		}
		ciphertext, err := send.Encrypt(raw)
		if err != nil {
			t.Fatalf("seal envelope: %v", err)
		}
		sendNoiseMsg(t, phone, ciphertext)
	}

	// One reader for the whole test so the receive CipherState nonce stays in
	// sequence; ok=false on deadline.
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
				t.Fatalf("phone decode inner frame: %v", err)
			}
			if inner.Type != protocol.TypeNoiseMsg {
				continue
			}
			return decryptInnerEnvelope(t, inner, recv), true
		}
	}

	// --- M1: bring the conversation's child up. Without a live child the named
	// rotation is correctly inert and every assertion below would pass vacuously.
	sealSend(protocol.Envelope{
		ID:   sendReqID,
		Type: protocol.TypeSendMessage,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.SendMessagePayload{
			ConversationID: convID,
			MessageID:      "u-1",
			Text:           textToConv,
		}),
	})
	echoDeadline := time.Now().Add(30 * time.Second)
	for {
		env, ok := nextEnv(echoDeadline)
		if !ok {
			t.Fatalf("M1: never observed an assistant_delta echoing %q; the conversation's child never "+
				"served a turn, so the rotation under test would be inert", needle)
		}
		if env.Type == protocol.TypeError {
			t.Fatalf("M1: unexpected error envelope: %s", string(env.Payload))
		}
		if env.Type != protocol.TypeAssistantDelta {
			continue
		}
		var d protocol.AssistantDeltaPayload
		if err := json.Unmarshal(env.Payload, &d); err != nil {
			t.Fatalf("M1: decode assistant_delta payload: %v", err)
		}
		if d.ConversationID == convID && strings.Contains(d.Text, needle) {
			break
		}
	}

	// --- M2: the operator's workspace moves outside $HOME. The recorded string is
	// unchanged; what it resolves to is not, which is exactly the state #1475's
	// re-confinement at the spawn site exists to catch.
	if err := os.Remove(recordedWS); err != nil {
		t.Fatalf("remove recorded workspace: %v", err)
	}
	if err := os.Symlink(escaped, recordedWS); err != nil {
		t.Fatalf("re-point recorded workspace outside $HOME: %v", err)
	}

	// --- M3: the frame under test.
	sendNewSessionFrameFor(t, phone, send, rotateReqID, convID)

	var (
		sawReply      bool
		sawTransition bool
		deadline      = time.Now().Add(30 * time.Second)
	)
	for !sawReply || !sawTransition {
		env, ok := nextEnv(deadline)
		if !ok {
			t.Fatalf("after a new_session whose recorded workspace resolves outside $HOME: "+
				"error reply seen = %v (AC-1), session_transition seen = %v (AC-2); wanted both",
				sawReply, sawTransition)
		}
		switch env.Type {
		case protocol.TypeError:
			var p protocol.ErrorPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("decode error payload: %v", err)
			}
			if p.Code != protocol.CodeNewSessionWorkspaceRefused {
				t.Fatalf("unexpected error code %q (message %q); want %q",
					p.Code, p.Message, protocol.CodeNewSessionWorkspaceRefused)
			}
			if env.InReplyTo == nil || *env.InReplyTo != rotateReqID {
				t.Errorf("InReplyTo = %v, want a pointer to %d — the refusal must be correlated to the "+
					"new_session that caused it", env.InReplyTo, rotateReqID)
			}
			if p.ConversationID != convID {
				t.Errorf("reply ConversationID = %q, want %q", p.ConversationID, convID)
			}
			if p.Retryable {
				t.Errorf("reply Retryable = true, want false")
			}
			// AC-4's containment property, checked over the ACTUAL DECRYPTED BYTES
			// rather than over the decoded message: the confinement error names both
			// the offending path and the $HOME boundary, and no fragment of either
			// may reach the wire through any field.
			for _, secret := range []string{recordedWS, escaped, home, workspaceName} {
				if strings.Contains(string(env.Payload), secret) {
					t.Errorf("the reply leaked a filesystem path fragment %q: %s", secret, string(env.Payload))
				}
			}
			sawReply = true
		case protocol.TypeSessionTransition:
			var st protocol.SessionTransitionPayload
			if err := json.Unmarshal(env.Payload, &st); err != nil {
				t.Fatalf("decode session_transition payload: %v", err)
			}
			if st.ConversationID != convID {
				continue
			}
			if st.NewSessionID == "" || st.NewSessionID == st.PreviousSessionID {
				t.Errorf("session_transition did not carry a fresh id: previous=%q new=%q",
					st.PreviousSessionID, st.NewSessionID)
			}
			sawTransition = true
		}
	}

	// The daemon's own record of the refusal must name the conversation and no
	// path either — the log is the other channel the confinement error could reach.
	waitForLogLineAll(t, h.Stderr, []string{"v2.new_session.spawn_dir_rejected", convID}, 15*time.Second)
}

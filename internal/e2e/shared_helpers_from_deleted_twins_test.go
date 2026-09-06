//go:build e2e

package e2e

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// Helpers that outlived their homes. Each of these was defined inside a
// terminal-path e2e spec that #1348 deleted, and each is still called by a
// surviving stream-path spec. They moved here rather than being duplicated into
// each caller, and this file is deliberately named for what it is so a later
// reader knows the grouping is historical rather than thematic.

// sendNewSessionFrame moved from relay_v2_new_session_test.go.
// sendNewSessionFrame seals a payload-less new_session control envelope with cs
// (the phone's send CipherState) and writes it to phone. new_session is
// fire-and-forget — no reply — so callers observe its effect on disk (the
// registry rotation) or its absence, never a returned envelope. The request id is
// cosmetic (there is no ack); callers bump it per re-send to keep envelope ids
// fresh across a bounded re-send loop.
func sendNewSessionFrame(t *testing.T, phone *fakephone.Client, cs *noise.CipherState, reqID uint64) {
	t.Helper()
	env, err := json.Marshal(protocol.Envelope{
		ID:   reqID,
		Type: protocol.TypeNewSession,
		TS:   time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("marshal new_session envelope: %v", err)
	}
	cipher, err := cs.Encrypt(env)
	if err != nil {
		t.Fatalf("seal new_session envelope: %v", err)
	}
	sendNoiseMsg(t, phone, cipher)
}

// sendNewSessionFrameFor is sendNewSessionFrame carrying a conversation_id (#2099):
// the frame names the conversation to restart instead of leaving the daemon to pick
// its cursor's. Kept beside its bare twin rather than folded into it — the bare form
// is the shape an un-upgraded client sends, and it stays exercised as itself.
func sendNewSessionFrameFor(t *testing.T, phone *fakephone.Client, cs *noise.CipherState, reqID uint64, conversationID string) {
	t.Helper()
	env, err := json.Marshal(protocol.Envelope{
		ID:   reqID,
		Type: protocol.TypeNewSession,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.NewSessionPayload{
			ConversationID: conversationID,
		}),
	})
	if err != nil {
		t.Fatalf("marshal new_session envelope: %v", err)
	}
	cipher, err := cs.Encrypt(env)
	if err != nil {
		t.Fatalf("seal new_session envelope: %v", err)
	}
	sendNoiseMsg(t, phone, cipher)
}

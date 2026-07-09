package handlers

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

const (
	renameConvConnID    = "c-rename-conv"
	renameConvRequestID = uint64(11)
	// renameConvFirstID is the id the handler's first reply must carry: on a
	// fresh conn NextID starts at 1 (the rename_conversation path runs the
	// dispatcher's normal reply machinery, with no gate hello_ack pre-advance).
	renameConvFirstID = uint64(1)
	// renameConvTargetID is the seeded conversation's stable id — the exact
	// registry key a valid rename must match.
	renameConvTargetID = "conv-rename-target"
	renameConvOldName  = "old-title"
	renameConvNewName  = "new-title"
	renameConvCwd      = "/work/rename-target"
)

// renameConvSeedTime is the seeded row's LastUsedAt — a fixed instant so the
// "rename does not bump LastUsedAt" assertion compares against a known value.
var renameConvSeedTime = time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

// newRenameConvConn returns a fresh *dispatch.Conn (NextID NOT pre-advanced, so
// the first reply lands at id=1) plus a recv helper that reads one outbound
// envelope. nil auth is fine: the rename_conversation handler does not consult
// c.Auth().
func newRenameConvConn(t *testing.T) (*dispatch.Conn, func() protocol.RoutingEnvelope) {
	t.Helper()
	out := make(chan protocol.RoutingEnvelope, 4)
	c := dispatch.NewTestConn(renameConvConnID, out, nil)
	recv := func() protocol.RoutingEnvelope {
		t.Helper()
		select {
		case env := <-out:
			return env
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for outbound envelope")
			return protocol.RoutingEnvelope{}
		}
	}
	return c, recv
}

// newRenameConvReg returns a registry backed by a temp-dir path (so the eager
// Save writes to a throwaway file) seeded with a single conversation whose id is
// renameConvTargetID and whose name is renameConvOldName.
func newRenameConvReg(t *testing.T) (*conversations.Registry, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "conversations.json")
	reg, err := conversations.Load(path)
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	old := renameConvOldName
	reg.Create(conversations.Conversation{
		ID:         conversations.ConversationID(renameConvTargetID),
		Name:       &old,
		Cwd:        renameConvCwd,
		IsPromoted: true,
		LastUsedAt: renameConvSeedTime,
	})
	return reg, path
}

func renameConvRequest(t *testing.T, p protocol.RenameConversationPayload) protocol.Envelope {
	t.Helper()
	payloadJSON, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return protocol.Envelope{
		ID:      renameConvRequestID,
		Type:    protocol.TypeRenameConversation,
		TS:      time.Now().UTC(),
		Payload: payloadJSON,
	}
}

func assertRenameConvEnvelopeShape(t *testing.T, resp protocol.RoutingEnvelope, wantType string) protocol.Envelope {
	t.Helper()
	if resp.ConnID != renameConvConnID {
		t.Errorf("Response.ConnID = %q, want %q", resp.ConnID, renameConvConnID)
	}
	var env protocol.Envelope
	if err := json.Unmarshal(resp.Frame, &env); err != nil {
		t.Fatalf("unmarshal response envelope: %v", err)
	}
	if env.Type != wantType {
		t.Errorf("Type = %q, want %q", env.Type, wantType)
	}
	if env.ID != renameConvFirstID {
		t.Errorf("ID = %d, want %d (first reply on a fresh conn)", env.ID, renameConvFirstID)
	}
	if env.InReplyTo == nil || *env.InReplyTo != renameConvRequestID {
		t.Errorf("InReplyTo = %v, want pointer to %d", env.InReplyTo, renameConvRequestID)
	}
	return env
}

// TestRenameConversation_Success_UpdatesReplyAndRow covers AC #2 + #3: a valid
// rename replies conversation_updated with the new title (other fields
// unchanged, LastUsedAt NOT bumped), the stored row's name is updated, and a
// subsequent List surfaces the new title.
func TestRenameConversation_Success_UpdatesReplyAndRow(t *testing.T) {
	t.Parallel()
	reg, regPath := newRenameConvReg(t)
	c, recv := newRenameConvConn(t)
	req := renameConvRequest(t, protocol.RenameConversationPayload{
		ConversationID: renameConvTargetID,
		Name:           renameConvNewName,
	})

	h := RenameConversation(reg, regPath, testLogger(t))
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}

	env := assertRenameConvEnvelopeShape(t, recv(), protocol.TypeConversationUpdated)
	var payload protocol.ConversationUpdatedPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal conversation_updated payload: %v", err)
	}
	if payload.ID != renameConvTargetID {
		t.Errorf("reply ID = %q, want %q", payload.ID, renameConvTargetID)
	}
	if payload.Name == nil || *payload.Name != renameConvNewName {
		t.Errorf("reply Name = %v, want pointer to %q", payload.Name, renameConvNewName)
	}
	if payload.Cwd != renameConvCwd {
		t.Errorf("reply Cwd = %q, want %q (unchanged)", payload.Cwd, renameConvCwd)
	}
	if !payload.IsPromoted {
		t.Errorf("reply IsPromoted = false, want true (unchanged)")
	}
	if !payload.LastUsedAt.Equal(renameConvSeedTime) {
		t.Errorf("reply LastUsedAt = %v, want %v (rename must not bump)", payload.LastUsedAt, renameConvSeedTime)
	}

	// The stored row now carries the new name; LastUsedAt is unchanged.
	stored, ok := reg.Get(conversations.ConversationID(renameConvTargetID))
	if !ok {
		t.Fatalf("registry missing seeded row after rename")
	}
	if stored.Name == nil || *stored.Name != renameConvNewName {
		t.Errorf("stored Name = %v, want pointer to %q", stored.Name, renameConvNewName)
	}
	if !stored.LastUsedAt.Equal(renameConvSeedTime) {
		t.Errorf("stored LastUsedAt = %v, want %v (rename must not bump)", stored.LastUsedAt, renameConvSeedTime)
	}

	// Surfacing (AC #3): List returns the row with its new title.
	list := reg.List()
	if len(list) != 1 {
		t.Fatalf("registry has %d rows, want 1", len(list))
	}
	if list[0].Name == nil || *list[0].Name != renameConvNewName {
		t.Errorf("listed Name = %v, want pointer to %q", list[0].Name, renameConvNewName)
	}
}

// TestRenameConversation_EagerPersist_SurvivesReload covers AC #2 durability: a
// successful rename Saves eagerly, so a fresh Load from disk returns the new
// title (proving it survives a daemon-process restart).
func TestRenameConversation_EagerPersist_SurvivesReload(t *testing.T) {
	t.Parallel()
	reg, regPath := newRenameConvReg(t)
	c, recv := newRenameConvConn(t)
	req := renameConvRequest(t, protocol.RenameConversationPayload{
		ConversationID: renameConvTargetID,
		Name:           renameConvNewName,
	})

	h := RenameConversation(reg, regPath, testLogger(t))
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}
	assertRenameConvEnvelopeShape(t, recv(), protocol.TypeConversationUpdated)

	reloaded, err := conversations.Load(regPath)
	if err != nil {
		t.Fatalf("reload registry from disk: %v", err)
	}
	got, ok := reloaded.Get(conversations.ConversationID(renameConvTargetID))
	if !ok {
		t.Fatalf("renamed conversation not found after reload from disk")
	}
	if got.Name == nil || *got.Name != renameConvNewName {
		t.Errorf("reloaded Name = %v, want pointer to %q (rename must persist)", got.Name, renameConvNewName)
	}
}

// TestRenameConversation_NotFound_LeavesRegistryUnmodified covers AC #4: an
// unknown conversation_id yields a conversation.not_found error reply and leaves
// the seeded row byte-unchanged.
func TestRenameConversation_NotFound_LeavesRegistryUnmodified(t *testing.T) {
	t.Parallel()
	reg, regPath := newRenameConvReg(t)
	c, recv := newRenameConvConn(t)
	req := renameConvRequest(t, protocol.RenameConversationPayload{
		ConversationID: "no-such-conversation",
		Name:           renameConvNewName,
	})

	h := RenameConversation(reg, regPath, testLogger(t))
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}

	env := assertRenameConvEnvelopeShape(t, recv(), protocol.TypeError)
	var payload protocol.ErrorPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal error payload: %v", err)
	}
	if payload.Code != protocol.CodeConversationNotFound {
		t.Errorf("Code = %q, want %q", payload.Code, protocol.CodeConversationNotFound)
	}
	if payload.Retryable {
		t.Errorf("Retryable = true, want false")
	}
	if payload.Message != msgRenameConversationNotFound {
		t.Errorf("Message = %q, want static %q", payload.Message, msgRenameConversationNotFound)
	}

	// The seeded row is untouched — the miss does not mutate the registry.
	stored, ok := reg.Get(conversations.ConversationID(renameConvTargetID))
	if !ok {
		t.Fatalf("registry missing seeded row after not-found rename")
	}
	if stored.Name == nil || *stored.Name != renameConvOldName {
		t.Errorf("stored Name = %v, want unchanged pointer to %q", stored.Name, renameConvOldName)
	}
}

// TestRenameConversation_EmptyName_LeavesStoredNameUnchanged covers AC #5: an
// empty or whitespace-only title is rejected with a non-retryable
// protocol.malformed reply and leaves the stored name unchanged.
func TestRenameConversation_EmptyName_LeavesStoredNameUnchanged(t *testing.T) {
	t.Parallel()
	for _, title := range []string{"", "   ", "\t\n "} {
		title := title
		t.Run("title="+title, func(t *testing.T) {
			t.Parallel()
			reg, regPath := newRenameConvReg(t)
			c, recv := newRenameConvConn(t)
			req := renameConvRequest(t, protocol.RenameConversationPayload{
				ConversationID: renameConvTargetID,
				Name:           title,
			})

			h := RenameConversation(reg, regPath, testLogger(t))
			if err := h(context.Background(), c, req); err != nil {
				t.Fatalf("handler: %v", err)
			}

			env := assertRenameConvEnvelopeShape(t, recv(), protocol.TypeError)
			var payload protocol.ErrorPayload
			if err := json.Unmarshal(env.Payload, &payload); err != nil {
				t.Fatalf("unmarshal error payload: %v", err)
			}
			if payload.Code != protocol.CodeProtocolMalformed {
				t.Errorf("Code = %q, want %q", payload.Code, protocol.CodeProtocolMalformed)
			}
			if payload.Retryable {
				t.Errorf("Retryable = true, want false")
			}
			if payload.Message != msgRenameConversationEmptyName {
				t.Errorf("Message = %q, want static %q", payload.Message, msgRenameConversationEmptyName)
			}

			// The stored name is untouched — the guard runs before Update.
			stored, ok := reg.Get(conversations.ConversationID(renameConvTargetID))
			if !ok {
				t.Fatalf("registry missing seeded row")
			}
			if stored.Name == nil || *stored.Name != renameConvOldName {
				t.Errorf("stored Name = %v, want unchanged pointer to %q", stored.Name, renameConvOldName)
			}
		})
	}
}

// TestRenameConversation_Malformed_EmitsProtocolMalformed covers AC #5's
// malformed sibling: a non-decodable payload yields a protocol.malformed error
// envelope carrying the static message (decode-error text must not be echoed)
// and leaves the seeded row untouched.
func TestRenameConversation_Malformed_EmitsProtocolMalformed(t *testing.T) {
	t.Parallel()
	reg, regPath := newRenameConvReg(t)
	c, recv := newRenameConvConn(t)
	req := protocol.Envelope{
		ID:      renameConvRequestID,
		Type:    protocol.TypeRenameConversation,
		TS:      time.Now().UTC(),
		Payload: []byte("{"),
	}

	h := RenameConversation(reg, regPath, testLogger(t))
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}

	env := assertRenameConvEnvelopeShape(t, recv(), protocol.TypeError)
	var payload protocol.ErrorPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal error payload: %v", err)
	}
	if payload.Code != protocol.CodeProtocolMalformed {
		t.Errorf("Code = %q, want %q", payload.Code, protocol.CodeProtocolMalformed)
	}
	if payload.Retryable {
		t.Errorf("Retryable = true, want false")
	}
	if payload.Message != msgRenameConversationMalformed {
		t.Errorf("Message = %q, want static %q (decode-error text must not be echoed)", payload.Message, msgRenameConversationMalformed)
	}

	stored, ok := reg.Get(conversations.ConversationID(renameConvTargetID))
	if !ok {
		t.Fatalf("registry missing seeded row after malformed frame")
	}
	if stored.Name == nil || *stored.Name != renameConvOldName {
		t.Errorf("stored Name = %v, want unchanged pointer to %q", stored.Name, renameConvOldName)
	}
}

// TestRenameConversation_NoEcho_RejectMessageIsStatic covers the no-echo
// discipline: an injected-looking id/title never appears in the reject reply
// message on the not-found path — only the fixed static string is sent.
func TestRenameConversation_NoEcho_RejectMessageIsStatic(t *testing.T) {
	t.Parallel()
	reg, regPath := newRenameConvReg(t)
	c, recv := newRenameConvConn(t)
	injected := "../../etc/passwd\x00<script>"
	req := renameConvRequest(t, protocol.RenameConversationPayload{
		ConversationID: injected,
		Name:           injected,
	})

	h := RenameConversation(reg, regPath, testLogger(t))
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}

	env := assertRenameConvEnvelopeShape(t, recv(), protocol.TypeError)
	var payload protocol.ErrorPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal error payload: %v", err)
	}
	if payload.Message != msgRenameConversationNotFound {
		t.Errorf("Message = %q, want static %q (no payload bytes echoed)", payload.Message, msgRenameConversationNotFound)
	}
}

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
	promoteConvConnID    = "c-promote-conv"
	promoteConvRequestID = uint64(13)
	// promoteConvFirstID is the id the handler's first reply must carry: on a
	// fresh conn NextID starts at 1 (the promote_conversation path runs the
	// dispatcher's normal reply machinery, with no gate hello_ack pre-advance).
	promoteConvFirstID = uint64(1)
	// promoteConvTargetID is the seeded scratch conversation's stable id — the
	// exact registry key a valid promote must match.
	promoteConvTargetID = "conv-promote-target"
	promoteConvName     = "my-channel"
	// promoteConvSeedCwd is the scratch row's stored cwd. The promoted channel
	// inherits it (Option B): the reply and the stored value must be this, never
	// the payload's cwd.
	promoteConvSeedCwd = "/work/promote-target"
	// promoteConvPayloadCwd is a DIFFERENT cwd the client sends on the wire. The
	// handler must discard it — a regression to Option A (consuming payload cwd)
	// fails the "Cwd ignored" assertion.
	promoteConvPayloadCwd = "/tmp/attacker-supplied"
)

// promoteConvSeedTime is the seeded row's LastUsedAt — a fixed instant so the
// "promote does not bump LastUsedAt" assertion compares against a known value.
var promoteConvSeedTime = time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)

// newPromoteConvConn returns a fresh *dispatch.Conn (NextID NOT pre-advanced, so
// the first reply lands at id=1) plus a recv helper that reads one outbound
// envelope. nil auth is fine: the promote_conversation handler does not consult
// c.Auth().
func newPromoteConvConn(t *testing.T) (*dispatch.Conn, func() protocol.RoutingEnvelope) {
	t.Helper()
	out := make(chan protocol.RoutingEnvelope, 4)
	c := dispatch.NewTestConn(promoteConvConnID, out, nil)
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

// newPromoteConvReg returns a registry backed by a temp-dir path (so the eager
// Save writes to a throwaway file) seeded with a single unpromoted scratch
// conversation (id promoteConvTargetID, no name, cwd promoteConvSeedCwd).
func newPromoteConvReg(t *testing.T) (*conversations.Registry, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "conversations.json")
	reg, err := conversations.Load(path)
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	reg.Create(conversations.Conversation{
		ID:         conversations.ConversationID(promoteConvTargetID),
		Cwd:        promoteConvSeedCwd,
		IsPromoted: false,
		LastUsedAt: promoteConvSeedTime,
	})
	return reg, path
}

func promoteConvRequest(t *testing.T, p protocol.PromoteConversationPayload) protocol.Envelope {
	t.Helper()
	payloadJSON, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return protocol.Envelope{
		ID:      promoteConvRequestID,
		Type:    protocol.TypePromoteConversation,
		TS:      time.Now().UTC(),
		Payload: payloadJSON,
	}
}

func assertPromoteConvEnvelopeShape(t *testing.T, resp protocol.RoutingEnvelope, wantType string) protocol.Envelope {
	t.Helper()
	if resp.ConnID != promoteConvConnID {
		t.Errorf("Response.ConnID = %q, want %q", resp.ConnID, promoteConvConnID)
	}
	var env protocol.Envelope
	if err := json.Unmarshal(resp.Frame, &env); err != nil {
		t.Fatalf("unmarshal response envelope: %v", err)
	}
	if env.Type != wantType {
		t.Errorf("Type = %q, want %q", env.Type, wantType)
	}
	if env.ID != promoteConvFirstID {
		t.Errorf("ID = %d, want %d (first reply on a fresh conn)", env.ID, promoteConvFirstID)
	}
	if env.InReplyTo == nil || *env.InReplyTo != promoteConvRequestID {
		t.Errorf("InReplyTo = %v, want pointer to %d", env.InReplyTo, promoteConvRequestID)
	}
	return env
}

func assertPromoteConvError(t *testing.T, env protocol.Envelope, wantCode, wantMessage string) {
	t.Helper()
	var payload protocol.ErrorPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal error payload: %v", err)
	}
	if payload.Code != wantCode {
		t.Errorf("Code = %q, want %q", payload.Code, wantCode)
	}
	if payload.Retryable {
		t.Errorf("Retryable = true, want false")
	}
	if payload.Message != wantMessage {
		t.Errorf("Message = %q, want static %q", payload.Message, wantMessage)
	}
}

// TestPromoteConversation_Success_InheritsCwdUpdatesRow covers AC #2: a valid
// promote replies conversation_updated with IsPromoted:true, the requested name,
// and the seeded (inherited) cwd — proving Option B (the channel keeps the
// scratch conversation's existing workspace). The stored row is now promoted;
// LastUsedAt is not bumped (promotion is a metadata flip).
func TestPromoteConversation_Success_InheritsCwdUpdatesRow(t *testing.T) {
	t.Parallel()
	reg, regPath := newPromoteConvReg(t)
	c, recv := newPromoteConvConn(t)
	req := promoteConvRequest(t, protocol.PromoteConversationPayload{
		ConversationID: promoteConvTargetID,
		Name:           promoteConvName,
		Cwd:            promoteConvSeedCwd,
	})

	h := PromoteConversation(reg, regPath, testLogger(t))
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}

	env := assertPromoteConvEnvelopeShape(t, recv(), protocol.TypeConversationUpdated)
	var payload protocol.ConversationUpdatedPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal conversation_updated payload: %v", err)
	}
	if payload.ID != promoteConvTargetID {
		t.Errorf("reply ID = %q, want %q", payload.ID, promoteConvTargetID)
	}
	if !payload.IsPromoted {
		t.Errorf("reply IsPromoted = false, want true")
	}
	if payload.Name == nil || *payload.Name != promoteConvName {
		t.Errorf("reply Name = %v, want pointer to %q", payload.Name, promoteConvName)
	}
	if payload.Cwd != promoteConvSeedCwd {
		t.Errorf("reply Cwd = %q, want %q (inherited scratch cwd, Option B)", payload.Cwd, promoteConvSeedCwd)
	}
	if !payload.LastUsedAt.Equal(promoteConvSeedTime) {
		t.Errorf("reply LastUsedAt = %v, want %v (promote must not bump)", payload.LastUsedAt, promoteConvSeedTime)
	}

	// The stored row is now a promoted channel with the requested name.
	stored, ok := reg.Get(conversations.ConversationID(promoteConvTargetID))
	if !ok {
		t.Fatalf("registry missing seeded row after promote")
	}
	if !stored.IsPromoted {
		t.Errorf("stored IsPromoted = false, want true")
	}
	if stored.Name == nil || *stored.Name != promoteConvName {
		t.Errorf("stored Name = %v, want pointer to %q", stored.Name, promoteConvName)
	}
	if stored.Cwd != promoteConvSeedCwd {
		t.Errorf("stored Cwd = %q, want %q (unchanged)", stored.Cwd, promoteConvSeedCwd)
	}
}

// TestPromoteConversation_PayloadCwd_IsIgnored locks Option B: a promote sending
// a payload cwd DIFFERENT from the seeded row's cwd must reply and store the
// SEEDED cwd, never the payload value. A regression to Option A (consuming the
// untrusted payload path as the workspace) fails here.
func TestPromoteConversation_PayloadCwd_IsIgnored(t *testing.T) {
	t.Parallel()
	reg, regPath := newPromoteConvReg(t)
	c, recv := newPromoteConvConn(t)
	req := promoteConvRequest(t, protocol.PromoteConversationPayload{
		ConversationID: promoteConvTargetID,
		Name:           promoteConvName,
		Cwd:            promoteConvPayloadCwd, // != seeded cwd
	})

	h := PromoteConversation(reg, regPath, testLogger(t))
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}

	env := assertPromoteConvEnvelopeShape(t, recv(), protocol.TypeConversationUpdated)
	var payload protocol.ConversationUpdatedPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal conversation_updated payload: %v", err)
	}
	if payload.Cwd != promoteConvSeedCwd {
		t.Errorf("reply Cwd = %q, want seeded %q (payload cwd must be ignored)", payload.Cwd, promoteConvSeedCwd)
	}

	stored, ok := reg.Get(conversations.ConversationID(promoteConvTargetID))
	if !ok {
		t.Fatalf("registry missing seeded row after promote")
	}
	if stored.Cwd != promoteConvSeedCwd {
		t.Errorf("stored Cwd = %q, want seeded %q (payload cwd must not be stored)", stored.Cwd, promoteConvSeedCwd)
	}
}

// TestPromoteConversation_EagerPersist_SurvivesReload covers AC #2 durability: a
// successful promote Saves eagerly, so a fresh Load from disk returns the
// promoted state (proving it survives a daemon-process restart).
func TestPromoteConversation_EagerPersist_SurvivesReload(t *testing.T) {
	t.Parallel()
	reg, regPath := newPromoteConvReg(t)
	c, recv := newPromoteConvConn(t)
	req := promoteConvRequest(t, protocol.PromoteConversationPayload{
		ConversationID: promoteConvTargetID,
		Name:           promoteConvName,
		Cwd:            promoteConvSeedCwd,
	})

	h := PromoteConversation(reg, regPath, testLogger(t))
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}
	assertPromoteConvEnvelopeShape(t, recv(), protocol.TypeConversationUpdated)

	reloaded, err := conversations.Load(regPath)
	if err != nil {
		t.Fatalf("reload registry from disk: %v", err)
	}
	got, ok := reloaded.Get(conversations.ConversationID(promoteConvTargetID))
	if !ok {
		t.Fatalf("promoted conversation not found after reload from disk")
	}
	if !got.IsPromoted {
		t.Errorf("reloaded IsPromoted = false, want true (promote must persist)")
	}
	if got.Name == nil || *got.Name != promoteConvName {
		t.Errorf("reloaded Name = %v, want pointer to %q (promote must persist)", got.Name, promoteConvName)
	}
}

// TestPromoteConversation_NotFound_LeavesRegistryUnmodified covers AC #3: an
// unknown conversation_id yields a conversation.not_found error reply and leaves
// the seeded row unpromoted.
func TestPromoteConversation_NotFound_LeavesRegistryUnmodified(t *testing.T) {
	t.Parallel()
	reg, regPath := newPromoteConvReg(t)
	c, recv := newPromoteConvConn(t)
	req := promoteConvRequest(t, protocol.PromoteConversationPayload{
		ConversationID: "no-such-conversation",
		Name:           promoteConvName,
		Cwd:            promoteConvSeedCwd,
	})

	h := PromoteConversation(reg, regPath, testLogger(t))
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}

	env := assertPromoteConvEnvelopeShape(t, recv(), protocol.TypeError)
	assertPromoteConvError(t, env, protocol.CodeConversationNotFound, msgPromoteConversationNotFound)

	stored, ok := reg.Get(conversations.ConversationID(promoteConvTargetID))
	if !ok {
		t.Fatalf("registry missing seeded row after not-found promote")
	}
	if stored.IsPromoted {
		t.Errorf("stored IsPromoted = true, want false (miss must not mutate)")
	}
}

// TestPromoteConversation_AlreadyPromoted covers AC #3: promoting an
// already-promoted row yields a conversation.already_promoted error reply.
func TestPromoteConversation_AlreadyPromoted(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "conversations.json")
	reg, err := conversations.Load(path)
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	existing := "already-a-channel"
	reg.Create(conversations.Conversation{
		ID:         conversations.ConversationID(promoteConvTargetID),
		Name:       &existing,
		Cwd:        promoteConvSeedCwd,
		IsPromoted: true,
		LastUsedAt: promoteConvSeedTime,
	})
	c, recv := newPromoteConvConn(t)
	req := promoteConvRequest(t, protocol.PromoteConversationPayload{
		ConversationID: promoteConvTargetID,
		Name:           promoteConvName,
		Cwd:            promoteConvSeedCwd,
	})

	h := PromoteConversation(reg, path, testLogger(t))
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}

	env := assertPromoteConvEnvelopeShape(t, recv(), protocol.TypeError)
	assertPromoteConvError(t, env, protocol.CodeConversationAlreadyPromoted, msgPromoteConversationAlreadyPromoted)

	// The existing name is untouched — the refusal does not overwrite it.
	stored, ok := reg.Get(conversations.ConversationID(promoteConvTargetID))
	if !ok {
		t.Fatalf("registry missing seeded row")
	}
	if stored.Name == nil || *stored.Name != existing {
		t.Errorf("stored Name = %v, want unchanged pointer to %q", stored.Name, existing)
	}
}

// TestPromoteConversation_EmptyName_MalformedNoMutation covers AC #3's
// input-validation arm: an empty or whitespace-only name is rejected with a
// non-retryable protocol.malformed reply and leaves the row unpromoted.
// Registry.Promote owns the empty-name refusal; the handler only maps it.
func TestPromoteConversation_EmptyName_MalformedNoMutation(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"", "   ", "\t\n "} {
		name := name
		t.Run("name="+name, func(t *testing.T) {
			t.Parallel()
			reg, regPath := newPromoteConvReg(t)
			c, recv := newPromoteConvConn(t)
			req := promoteConvRequest(t, protocol.PromoteConversationPayload{
				ConversationID: promoteConvTargetID,
				Name:           name,
				Cwd:            promoteConvSeedCwd,
			})

			h := PromoteConversation(reg, regPath, testLogger(t))
			if err := h(context.Background(), c, req); err != nil {
				t.Fatalf("handler: %v", err)
			}

			env := assertPromoteConvEnvelopeShape(t, recv(), protocol.TypeError)
			assertPromoteConvError(t, env, protocol.CodeProtocolMalformed, msgPromoteConversationEmptyName)

			stored, ok := reg.Get(conversations.ConversationID(promoteConvTargetID))
			if !ok {
				t.Fatalf("registry missing seeded row")
			}
			if stored.IsPromoted {
				t.Errorf("stored IsPromoted = true, want false (empty name must not mutate)")
			}
		})
	}
}

// TestPromoteConversation_NameInUse covers AC #3: promoting a scratch row to a
// name already carried by another promoted row is rejected protocol.malformed
// and leaves the scratch row unpromoted.
func TestPromoteConversation_NameInUse(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "conversations.json")
	reg, err := conversations.Load(path)
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	taken := promoteConvName
	reg.Create(conversations.Conversation{
		ID:         conversations.ConversationID("other-channel"),
		Name:       &taken,
		Cwd:        "/work/other",
		IsPromoted: true,
		LastUsedAt: promoteConvSeedTime,
	})
	reg.Create(conversations.Conversation{
		ID:         conversations.ConversationID(promoteConvTargetID),
		Cwd:        promoteConvSeedCwd,
		IsPromoted: false,
		LastUsedAt: promoteConvSeedTime,
	})
	c, recv := newPromoteConvConn(t)
	req := promoteConvRequest(t, protocol.PromoteConversationPayload{
		ConversationID: promoteConvTargetID,
		Name:           promoteConvName, // already used by other-channel
		Cwd:            promoteConvSeedCwd,
	})

	h := PromoteConversation(reg, path, testLogger(t))
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}

	env := assertPromoteConvEnvelopeShape(t, recv(), protocol.TypeError)
	assertPromoteConvError(t, env, protocol.CodeProtocolMalformed, msgPromoteConversationNameInUse)

	stored, ok := reg.Get(conversations.ConversationID(promoteConvTargetID))
	if !ok {
		t.Fatalf("registry missing scratch row")
	}
	if stored.IsPromoted {
		t.Errorf("scratch IsPromoted = true, want false (name-in-use must not mutate)")
	}
}

// TestPromoteConversation_Malformed_DoesNotLeakPayloadBytes covers AC #3's
// malformed sibling: a non-decodable payload yields a protocol.malformed error
// carrying the static message (decode-error text must not be echoed) and leaves
// the seeded row untouched.
func TestPromoteConversation_Malformed_DoesNotLeakPayloadBytes(t *testing.T) {
	t.Parallel()
	reg, regPath := newPromoteConvReg(t)
	c, recv := newPromoteConvConn(t)
	req := protocol.Envelope{
		ID:      promoteConvRequestID,
		Type:    protocol.TypePromoteConversation,
		TS:      time.Now().UTC(),
		Payload: []byte("{"),
	}

	h := PromoteConversation(reg, regPath, testLogger(t))
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}

	env := assertPromoteConvEnvelopeShape(t, recv(), protocol.TypeError)
	assertPromoteConvError(t, env, protocol.CodeProtocolMalformed, msgPromoteConversationMalformed)

	stored, ok := reg.Get(conversations.ConversationID(promoteConvTargetID))
	if !ok {
		t.Fatalf("registry missing seeded row after malformed frame")
	}
	if stored.IsPromoted {
		t.Errorf("stored IsPromoted = true, want false (malformed must not mutate)")
	}
}

// TestPromoteConversation_NoEcho_RejectMessageIsStatic covers the no-echo
// discipline: an injected-looking id/name never appears in the reject reply
// message on the not-found path — only the fixed static string is sent.
func TestPromoteConversation_NoEcho_RejectMessageIsStatic(t *testing.T) {
	t.Parallel()
	reg, regPath := newPromoteConvReg(t)
	c, recv := newPromoteConvConn(t)
	injected := "../../etc/passwd\x00<script>"
	req := promoteConvRequest(t, protocol.PromoteConversationPayload{
		ConversationID: injected,
		Name:           injected,
		Cwd:            injected,
	})

	h := PromoteConversation(reg, regPath, testLogger(t))
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}

	env := assertPromoteConvEnvelopeShape(t, recv(), protocol.TypeError)
	var payload protocol.ErrorPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal error payload: %v", err)
	}
	if payload.Message != msgPromoteConversationNotFound {
		t.Errorf("Message = %q, want static %q (no payload bytes echoed)", payload.Message, msgPromoteConversationNotFound)
	}
}

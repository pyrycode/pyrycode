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
	createConvConnID    = "c-create-conv"
	createConvRequestID = uint64(7)
	// createConvFirstID is the id the handler's first reply must carry: on a
	// fresh conn NextID starts at 1 (the create_conversation path runs the
	// dispatcher's normal reply machinery, with no gate hello_ack pre-advance).
	createConvFirstID = uint64(1)
	createConvDefault = "/srv/pyry/work"
)

// newCreateConvConn returns a fresh *dispatch.Conn (NextID NOT pre-advanced, so
// the first reply lands at id=1) plus a recv helper that reads one outbound
// envelope. nil auth is fine: the create_conversation handler does not consult
// c.Auth().
func newCreateConvConn(t *testing.T) (*dispatch.Conn, func() protocol.RoutingEnvelope) {
	t.Helper()
	out := make(chan protocol.RoutingEnvelope, 4)
	c := dispatch.NewTestConn(createConvConnID, out, nil)
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

// newCreateConvReg returns an empty registry backed by a temp-dir path so the
// eager Save writes to a throwaway file.
func newCreateConvReg(t *testing.T) (*conversations.Registry, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "conversations.json")
	reg, err := conversations.Load(path)
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	return reg, path
}

func createConvRequest(t *testing.T, p protocol.CreateConversationPayload) protocol.Envelope {
	t.Helper()
	payloadJSON, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return protocol.Envelope{
		ID:      createConvRequestID,
		Type:    protocol.TypeCreateConversation,
		TS:      time.Now().UTC(),
		Payload: payloadJSON,
	}
}

func assertCreateConvEnvelopeShape(t *testing.T, resp protocol.RoutingEnvelope, wantType string) protocol.Envelope {
	t.Helper()
	if resp.ConnID != createConvConnID {
		t.Errorf("Response.ConnID = %q, want %q", resp.ConnID, createConvConnID)
	}
	var env protocol.Envelope
	if err := json.Unmarshal(resp.Frame, &env); err != nil {
		t.Fatalf("unmarshal response envelope: %v", err)
	}
	if env.Type != wantType {
		t.Errorf("Type = %q, want %q", env.Type, wantType)
	}
	if env.ID != createConvFirstID {
		t.Errorf("ID = %d, want %d (first reply on a fresh conn)", env.ID, createConvFirstID)
	}
	if env.InReplyTo == nil || *env.InReplyTo != createConvRequestID {
		t.Errorf("InReplyTo = %v, want pointer to %d", env.InReplyTo, createConvRequestID)
	}
	return env
}

// TestCreateConversation_AllNull_CreatesRowAndReplies covers the common
// "scratch discussion" path (mirrors testdata/create_conversation.json): a
// frame with every field null mints a row carrying the server defaults and
// replies with a conversation_created envelope correlated to the request.
func TestCreateConversation_AllNull_CreatesRowAndReplies(t *testing.T) {
	t.Parallel()
	reg, regPath := newCreateConvReg(t)
	c, recv := newCreateConvConn(t)
	req := createConvRequest(t, protocol.CreateConversationPayload{}) // all fields nil

	h := CreateConversation(reg, regPath, createConvDefault, testLogger(t))
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}

	env := assertCreateConvEnvelopeShape(t, recv(), protocol.TypeConversationCreated)
	var payload protocol.ConversationCreatedPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal conversation_created payload: %v", err)
	}
	if !conversations.ValidID(payload.ID) {
		t.Errorf("reply ID = %q, want a canonical UUIDv4", payload.ID)
	}
	if payload.Cwd != createConvDefault {
		t.Errorf("Cwd = %q, want %q (default applied for null cwd)", payload.Cwd, createConvDefault)
	}
	if payload.IsPromoted {
		t.Errorf("IsPromoted = true, want false (default for null)")
	}
	if payload.Name != nil {
		t.Errorf("Name = %v, want nil (null passthrough)", payload.Name)
	}
	if payload.LastUsedAt.IsZero() {
		t.Errorf("LastUsedAt is zero, want a stamped time")
	}

	// The registry now holds exactly the created row, matching the reply.
	stored, ok := reg.Get(conversations.ConversationID(payload.ID))
	if !ok {
		t.Fatalf("registry has no row for created id %q", payload.ID)
	}
	if stored.Cwd != createConvDefault || stored.IsPromoted || stored.Name != nil {
		t.Errorf("stored row mismatch: cwd=%q promoted=%v name=%v", stored.Cwd, stored.IsPromoted, stored.Name)
	}
	if !stored.LastUsedAt.Equal(payload.LastUsedAt) {
		t.Errorf("stored LastUsedAt = %v, want %v", stored.LastUsedAt, payload.LastUsedAt)
	}
	if got := reg.List(); len(got) != 1 {
		t.Errorf("registry has %d rows, want 1", len(got))
	}
}

// TestCreateConversation_ExplicitFields_RecordedVerbatim asserts a fully
// populated payload is stored and echoed verbatim — in particular the explicit
// cwd is NOT overridden by defaultCwd.
func TestCreateConversation_ExplicitFields_RecordedVerbatim(t *testing.T) {
	t.Parallel()
	reg, regPath := newCreateConvReg(t)
	c, recv := newCreateConvConn(t)

	promoted := true
	name := "proj"
	cwd := "/work/proj"
	req := createConvRequest(t, protocol.CreateConversationPayload{
		IsPromoted: &promoted,
		Name:       &name,
		Cwd:        &cwd,
	})

	h := CreateConversation(reg, regPath, createConvDefault, testLogger(t))
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}

	env := assertCreateConvEnvelopeShape(t, recv(), protocol.TypeConversationCreated)
	var payload protocol.ConversationCreatedPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.Cwd != cwd {
		t.Errorf("Cwd = %q, want %q (explicit cwd, not defaultCwd)", payload.Cwd, cwd)
	}
	if !payload.IsPromoted {
		t.Errorf("IsPromoted = false, want true")
	}
	if payload.Name == nil || *payload.Name != name {
		t.Errorf("Name = %v, want pointer to %q", payload.Name, name)
	}

	stored, ok := reg.Get(conversations.ConversationID(payload.ID))
	if !ok {
		t.Fatalf("registry missing created row")
	}
	if stored.Cwd != cwd || !stored.IsPromoted || stored.Name == nil || *stored.Name != name {
		t.Errorf("stored row mismatch: %+v", stored)
	}
}

// TestCreateConversation_EagerPersist_SurvivesReload asserts the create path
// Saves eagerly (not lazily like the sweep loop): the row is readable after a
// fresh Load from disk, proving it survives a daemon-process restart.
func TestCreateConversation_EagerPersist_SurvivesReload(t *testing.T) {
	t.Parallel()
	reg, regPath := newCreateConvReg(t)
	c, recv := newCreateConvConn(t)
	req := createConvRequest(t, protocol.CreateConversationPayload{})

	h := CreateConversation(reg, regPath, createConvDefault, testLogger(t))
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}
	env := assertCreateConvEnvelopeShape(t, recv(), protocol.TypeConversationCreated)
	var payload protocol.ConversationCreatedPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}

	reloaded, err := conversations.Load(regPath)
	if err != nil {
		t.Fatalf("reload registry from disk: %v", err)
	}
	if _, ok := reloaded.Get(conversations.ConversationID(payload.ID)); !ok {
		t.Errorf("created conversation %q not found after reload from disk", payload.ID)
	}
}

// TestCreateConversation_Malformed_EmitsProtocolMalformed asserts a
// non-decodable payload yields a protocol.malformed error envelope carrying the
// static message (decode-error text must not be echoed) and leaves the registry
// untouched.
func TestCreateConversation_Malformed_EmitsProtocolMalformed(t *testing.T) {
	t.Parallel()
	reg, regPath := newCreateConvReg(t)
	c, recv := newCreateConvConn(t)
	req := protocol.Envelope{
		ID:      createConvRequestID,
		Type:    protocol.TypeCreateConversation,
		TS:      time.Now().UTC(),
		Payload: []byte("{"),
	}

	h := CreateConversation(reg, regPath, createConvDefault, testLogger(t))
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}

	env := assertCreateConvEnvelopeShape(t, recv(), protocol.TypeError)
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
	if payload.Message != msgCreateConversationMalformed {
		t.Errorf("Message = %q, want static %q (decode-error text must not be echoed)", payload.Message, msgCreateConversationMalformed)
	}
	if got := reg.List(); len(got) != 0 {
		t.Errorf("registry has %d rows after malformed frame, want 0", len(got))
	}
}

// TestCreateConversation_CreatedID_ValidatesForSendMessage proves AC#3's
// daemon-side contract at the unit level: a freshly created row is immediately
// resolvable via Registry.Get — the same predicate the pool's
// ValidateConversation closure uses to admit a send_message — so a phone can
// send on the new id with no claude session spawned at create time.
func TestCreateConversation_CreatedID_ValidatesForSendMessage(t *testing.T) {
	t.Parallel()
	reg, regPath := newCreateConvReg(t)
	c, recv := newCreateConvConn(t)
	req := createConvRequest(t, protocol.CreateConversationPayload{})

	h := CreateConversation(reg, regPath, createConvDefault, testLogger(t))
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}
	env := assertCreateConvEnvelopeShape(t, recv(), protocol.TypeConversationCreated)
	var payload protocol.ConversationCreatedPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}

	if _, ok := reg.Get(conversations.ConversationID(payload.ID)); !ok {
		t.Errorf("created id %q does not validate via Registry.Get; a follow-up send_message would be rejected", payload.ID)
	}
}

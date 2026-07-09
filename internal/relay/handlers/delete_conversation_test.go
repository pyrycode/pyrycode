package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

const (
	deleteConvConnID    = "c-delete-conv"
	deleteConvRequestID = uint64(21)
	// deleteConvFirstID is the id the handler's first reply must carry: on a
	// fresh conn NextID starts at 1 (the delete_conversation path runs the
	// dispatcher's normal reply machinery, with no gate hello_ack pre-advance).
	deleteConvFirstID = uint64(1)
	// deleteConvTargetID is the seeded conversation's stable id — the exact
	// registry key a valid delete must match.
	deleteConvTargetID = "conv-delete-target"
	deleteConvName     = "target-title"
	deleteConvCwd      = "/work/delete-target"
)

// deleteConvSeedTime is the seeded row's LastUsedAt — a fixed instant so a
// reload comparison uses a known value.
var deleteConvSeedTime = time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

// newDeleteConvConn returns a fresh *dispatch.Conn (NextID NOT pre-advanced, so
// the first reply lands at id=1) plus a recv helper that reads one outbound
// envelope. nil auth is fine: the delete_conversation handler does not consult
// c.Auth().
func newDeleteConvConn(t *testing.T) (*dispatch.Conn, func() protocol.RoutingEnvelope) {
	t.Helper()
	out := make(chan protocol.RoutingEnvelope, 4)
	c := dispatch.NewTestConn(deleteConvConnID, out, nil)
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

// newDeleteConvReg returns a registry backed by a temp-dir path (so the eager
// Save writes to a throwaway file) seeded with a single conversation whose id is
// deleteConvTargetID.
func newDeleteConvReg(t *testing.T) (*conversations.Registry, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "conversations.json")
	reg, err := conversations.Load(path)
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	name := deleteConvName
	reg.Create(conversations.Conversation{
		ID:         conversations.ConversationID(deleteConvTargetID),
		Name:       &name,
		Cwd:        deleteConvCwd,
		IsPromoted: true,
		LastUsedAt: deleteConvSeedTime,
	})
	return reg, path
}

func deleteConvRequest(t *testing.T, p protocol.DeleteConversationPayload) protocol.Envelope {
	t.Helper()
	payloadJSON, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return protocol.Envelope{
		ID:      deleteConvRequestID,
		Type:    protocol.TypeDeleteConversation,
		TS:      time.Now().UTC(),
		Payload: payloadJSON,
	}
}

func assertDeleteConvEnvelopeShape(t *testing.T, resp protocol.RoutingEnvelope, wantType string) protocol.Envelope {
	t.Helper()
	if resp.ConnID != deleteConvConnID {
		t.Errorf("Response.ConnID = %q, want %q", resp.ConnID, deleteConvConnID)
	}
	var env protocol.Envelope
	if err := json.Unmarshal(resp.Frame, &env); err != nil {
		t.Fatalf("unmarshal response envelope: %v", err)
	}
	if env.Type != wantType {
		t.Errorf("Type = %q, want %q", env.Type, wantType)
	}
	if env.ID != deleteConvFirstID {
		t.Errorf("ID = %d, want %d (first reply on a fresh conn)", env.ID, deleteConvFirstID)
	}
	if env.InReplyTo == nil || *env.InReplyTo != deleteConvRequestID {
		t.Errorf("InReplyTo = %v, want pointer to %d", env.InReplyTo, deleteConvRequestID)
	}
	return env
}

// TestDeleteConversation_Success_RemovesRowRepliesAndPersists covers AC #1 + #2:
// a valid delete replies conversation_deleted (in_reply_to correlated, payload
// id == the deleted id), the stored row is gone in-memory, and a fresh Load from
// disk no longer contains it (proving the eager Save persisted the removal so it
// survives a daemon restart).
func TestDeleteConversation_Success_RemovesRowRepliesAndPersists(t *testing.T) {
	t.Parallel()
	reg, regPath := newDeleteConvReg(t)
	c, recv := newDeleteConvConn(t)
	req := deleteConvRequest(t, protocol.DeleteConversationPayload{ConversationID: deleteConvTargetID})

	h := DeleteConversation(reg, regPath, testLogger(t))
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}

	env := assertDeleteConvEnvelopeShape(t, recv(), protocol.TypeConversationDeleted)
	var payload protocol.ConversationDeletedPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal conversation_deleted payload: %v", err)
	}
	if payload.ID != deleteConvTargetID {
		t.Errorf("reply ID = %q, want %q", payload.ID, deleteConvTargetID)
	}

	// The row is gone in-memory.
	if _, ok := reg.Get(conversations.ConversationID(deleteConvTargetID)); ok {
		t.Errorf("registry still contains deleted row")
	}

	// AC #1 restart-survival: a fresh Load from the same path omits the row.
	reloaded, err := conversations.Load(regPath)
	if err != nil {
		t.Fatalf("reload registry from disk: %v", err)
	}
	if _, ok := reloaded.Get(conversations.ConversationID(deleteConvTargetID)); ok {
		t.Errorf("deleted conversation reappeared after reload from disk (eager Save did not persist)")
	}
}

// TestDeleteConversation_DeletedRowGoneFromList covers AC #3: after a successful
// delete, a list_conversations call against the same registry omits the deleted
// id (and still surfaces a surviving sibling). list_conversations reads the live
// registry, so a hard-deleted row disappears with no consumer-side change.
func TestDeleteConversation_DeletedRowGoneFromList(t *testing.T) {
	t.Parallel()
	reg, regPath := newDeleteConvReg(t)
	// Seed a second, unrelated conversation that must survive the delete.
	survivorName := "survivor"
	const survivorID = "conv-delete-survivor"
	reg.Create(conversations.Conversation{
		ID:         conversations.ConversationID(survivorID),
		Name:       &survivorName,
		Cwd:        "/work/survivor",
		IsPromoted: true,
		LastUsedAt: deleteConvSeedTime.Add(time.Minute),
	})

	c, recv := newDeleteConvConn(t)
	del := DeleteConversation(reg, regPath, testLogger(t))
	if err := del(context.Background(), c, deleteConvRequest(t, protocol.DeleteConversationPayload{ConversationID: deleteConvTargetID})); err != nil {
		t.Fatalf("delete handler: %v", err)
	}
	assertDeleteConvEnvelopeShape(t, recv(), protocol.TypeConversationDeleted)

	// A list_conversations request now omits the deleted id.
	listReq := protocol.Envelope{
		ID:      deleteConvRequestID + 1,
		Type:    protocol.TypeListConversations,
		TS:      time.Now().UTC(),
		Payload: mustMarshal(t, protocol.ListConversationsPayload{}),
	}
	list := ListConversations(reg)
	if err := list(context.Background(), c, listReq); err != nil {
		t.Fatalf("list handler: %v", err)
	}
	var listEnv protocol.Envelope
	if err := json.Unmarshal(recv().Frame, &listEnv); err != nil {
		t.Fatalf("unmarshal list response envelope: %v", err)
	}
	if listEnv.Type != protocol.TypeConversations {
		t.Fatalf("list reply Type = %q, want %q", listEnv.Type, protocol.TypeConversations)
	}
	var listPayload protocol.ConversationsPayload
	if err := json.Unmarshal(listEnv.Payload, &listPayload); err != nil {
		t.Fatalf("unmarshal conversations payload: %v", err)
	}
	if len(listPayload.Conversations) != 1 {
		t.Fatalf("list returned %d rows, want 1 (only the survivor)", len(listPayload.Conversations))
	}
	if listPayload.Conversations[0].ID != survivorID {
		t.Errorf("list surfaced %q, want survivor %q", listPayload.Conversations[0].ID, survivorID)
	}
	for _, conv := range listPayload.Conversations {
		if conv.ID == deleteConvTargetID {
			t.Errorf("deleted id %q still present in list_conversations", deleteConvTargetID)
		}
	}
}

// TestDeleteConversation_NotFound_LeavesRegistryUnmodified covers AC #4: an
// unknown conversation_id yields a conversation.not_found error reply and leaves
// the seeded row intact.
func TestDeleteConversation_NotFound_LeavesRegistryUnmodified(t *testing.T) {
	t.Parallel()
	reg, regPath := newDeleteConvReg(t)
	c, recv := newDeleteConvConn(t)
	req := deleteConvRequest(t, protocol.DeleteConversationPayload{ConversationID: "no-such-conversation"})

	h := DeleteConversation(reg, regPath, testLogger(t))
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}

	env := assertDeleteConvEnvelopeShape(t, recv(), protocol.TypeError)
	payload := assertErrorPayload(t, env, protocol.CodeConversationNotFound, msgDeleteConversationNotFound)
	if payload.Retryable {
		t.Errorf("Retryable = true, want false")
	}

	// The seeded row is untouched — the miss does not mutate the registry.
	if _, ok := reg.Get(conversations.ConversationID(deleteConvTargetID)); !ok {
		t.Errorf("seeded row removed by a not-found delete")
	}
}

// TestDeleteConversation_IdempotentOnMiss covers AC #4's idempotent-on-miss
// clause: deleting a seeded id succeeds, and re-issuing the same delete returns
// the identical conversation.not_found with no further state change.
func TestDeleteConversation_IdempotentOnMiss(t *testing.T) {
	t.Parallel()
	reg, regPath := newDeleteConvReg(t)
	c, recv := newDeleteConvConn(t)
	h := DeleteConversation(reg, regPath, testLogger(t))

	// First delete succeeds.
	if err := h(context.Background(), c, deleteConvRequest(t, protocol.DeleteConversationPayload{ConversationID: deleteConvTargetID})); err != nil {
		t.Fatalf("first delete handler: %v", err)
	}
	assertDeleteConvEnvelopeShape(t, recv(), protocol.TypeConversationDeleted)

	// Re-issuing the same id now misses: conversation.not_found, no state change.
	if err := h(context.Background(), c, deleteConvRequest(t, protocol.DeleteConversationPayload{ConversationID: deleteConvTargetID})); err != nil {
		t.Fatalf("second delete handler: %v", err)
	}
	env := recvErrorEnvelope(t, recv())
	assertErrorPayload(t, env, protocol.CodeConversationNotFound, msgDeleteConversationNotFound)
	if _, ok := reg.Get(conversations.ConversationID(deleteConvTargetID)); ok {
		t.Errorf("registry contains a row after two deletes of the same id")
	}
}

// TestDeleteConversation_Malformed_DoesNotLeakPayloadBytes covers AC #5: a
// non-decodable payload yields a protocol.malformed error carrying the static
// message (no payload bytes on the wire), leaves the registry untouched, and —
// critically for the security divergence from the rename template — the
// malformed log record carries NEITHER an "err" field (a json decode error can
// embed offending input bytes) NOR a "conversation_id" field (a decode failure
// leaves the struct only partially populated, so it may hold raw attacker bytes).
func TestDeleteConversation_Malformed_DoesNotLeakPayloadBytes(t *testing.T) {
	t.Parallel()
	const marker = "INJECTED_MARKER_9f3"
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	reg, regPath := newDeleteConvReg(t)
	c, recv := newDeleteConvConn(t)
	// Malformed: truncated object whose raw bytes carry the marker.
	req := protocol.Envelope{
		ID:      deleteConvRequestID,
		Type:    protocol.TypeDeleteConversation,
		TS:      time.Now().UTC(),
		Payload: []byte(`{"conversation_id":"` + marker + `"`),
	}

	h := DeleteConversation(reg, regPath, logger)
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}

	env := assertDeleteConvEnvelopeShape(t, recv(), protocol.TypeError)
	payload := assertErrorPayload(t, env, protocol.CodeProtocolMalformed, msgDeleteConversationMalformed)
	if payload.Retryable {
		t.Errorf("Retryable = true, want false")
	}
	if strings.Contains(payload.Message, marker) {
		t.Errorf("wire message %q echoed payload bytes", payload.Message)
	}

	// The registry is untouched by a malformed frame.
	if _, ok := reg.Get(conversations.ConversationID(deleteConvTargetID)); !ok {
		t.Errorf("seeded row removed by a malformed delete")
	}

	// The malformed log record must not leak payload bytes: no marker anywhere,
	// and specifically neither an "err" nor a "conversation_id" field.
	logged := buf.String()
	if strings.Contains(logged, marker) {
		t.Errorf("malformed log leaked payload bytes: %s", logged)
	}
	rec := findLogRecord(t, logged, "delete_conversation.malformed")
	if _, ok := rec["err"]; ok {
		t.Errorf("malformed log record carries an \"err\" field (json decode error can embed payload bytes): %v", rec)
	}
	if _, ok := rec["conversation_id"]; ok {
		t.Errorf("malformed log record carries a \"conversation_id\" field (may hold raw attacker bytes): %v", rec)
	}
	if _, ok := rec["conn_id"]; !ok {
		t.Errorf("malformed log record missing conn_id: %v", rec)
	}
}

// TestDeleteConversation_NoEcho_NotFoundMessageIsStatic covers AC #5's no-echo
// discipline on the not-found path: an injected-looking id never appears in the
// reject reply message — only the fixed static string is sent.
func TestDeleteConversation_NoEcho_NotFoundMessageIsStatic(t *testing.T) {
	t.Parallel()
	reg, regPath := newDeleteConvReg(t)
	c, recv := newDeleteConvConn(t)
	injected := "../../etc/passwd\x00<script>"
	req := deleteConvRequest(t, protocol.DeleteConversationPayload{ConversationID: injected})

	h := DeleteConversation(reg, regPath, testLogger(t))
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}

	env := assertDeleteConvEnvelopeShape(t, recv(), protocol.TypeError)
	payload := assertErrorPayload(t, env, protocol.CodeConversationNotFound, msgDeleteConversationNotFound)
	if strings.Contains(payload.Message, injected) {
		t.Errorf("reply message %q echoed the supplied id", payload.Message)
	}
}

// mustMarshal is a small test helper for building a payload from a typed value.
func mustMarshal(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// recvErrorEnvelope decodes an outbound routing frame into an Envelope, asserting
// only that it decodes. Used where the reply id/in_reply_to are not the point.
func recvErrorEnvelope(t *testing.T, resp protocol.RoutingEnvelope) protocol.Envelope {
	t.Helper()
	var env protocol.Envelope
	if err := json.Unmarshal(resp.Frame, &env); err != nil {
		t.Fatalf("unmarshal response envelope: %v", err)
	}
	return env
}

// assertErrorPayload unmarshals an error envelope and asserts its code and static
// message, returning the decoded payload for any further per-test checks.
func assertErrorPayload(t *testing.T, env protocol.Envelope, wantCode, wantMsg string) protocol.ErrorPayload {
	t.Helper()
	var payload protocol.ErrorPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal error payload: %v", err)
	}
	if payload.Code != wantCode {
		t.Errorf("Code = %q, want %q", payload.Code, wantCode)
	}
	if payload.Message != wantMsg {
		t.Errorf("Message = %q, want static %q", payload.Message, wantMsg)
	}
	return payload
}

// findLogRecord scans newline-delimited JSON slog output for the first record
// whose "event" field equals want, and returns it decoded. Fails the test if no
// such record is present.
func findLogRecord(t *testing.T, logged, want string) map[string]any {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(logged), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("unmarshal log line %q: %v", line, err)
		}
		if rec["event"] == want {
			return rec
		}
	}
	t.Fatalf("no log record with event %q in:\n%s", want, logged)
	return nil
}

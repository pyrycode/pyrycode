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
	archiveConvConnID    = "c-archive-conv"
	archiveConvRequestID = uint64(31)
	// archiveConvFirstID is the id the handler's first reply must carry: on a
	// fresh conn NextID starts at 1 (the archive path runs the dispatcher's
	// normal reply machinery, with no gate hello_ack pre-advance).
	archiveConvFirstID = uint64(1)
	// archiveConvTargetID is the seeded conversation's stable id — the exact
	// registry key a valid archive/unarchive must match.
	archiveConvTargetID = "conv-archive-target"
	archiveConvName     = "target-title"
	archiveConvCwd      = "/work/archive-target"
)

// archiveConvSeedTime is the seeded row's LastUsedAt — a fixed instant so the
// "archive does not touch other fields" assertion compares against a known value.
var archiveConvSeedTime = time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

// newArchiveConvConn returns a fresh *dispatch.Conn (NextID NOT pre-advanced, so
// the first reply lands at id=1) plus a recv helper that reads one outbound
// envelope. nil auth is fine: the archive handler does not consult c.Auth().
func newArchiveConvConn(t *testing.T) (*dispatch.Conn, func() protocol.RoutingEnvelope) {
	t.Helper()
	out := make(chan protocol.RoutingEnvelope, 4)
	c := dispatch.NewTestConn(archiveConvConnID, out, nil)
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

// newArchiveConvReg returns a registry backed by a temp-dir path (so the eager
// Save writes to a throwaway file) seeded with a single conversation whose id is
// archiveConvTargetID and whose IsArchived flag is the supplied value.
func newArchiveConvReg(t *testing.T, archived bool) (*conversations.Registry, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "conversations.json")
	reg, err := conversations.Load(path)
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	name := archiveConvName
	reg.Create(conversations.Conversation{
		ID:         conversations.ConversationID(archiveConvTargetID),
		Name:       &name,
		Cwd:        archiveConvCwd,
		IsPromoted: true,
		IsArchived: archived,
		LastUsedAt: archiveConvSeedTime,
	})
	return reg, path
}

func archiveConvRequest(t *testing.T, typ string, p protocol.ArchiveConversationPayload) protocol.Envelope {
	t.Helper()
	payloadJSON, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return protocol.Envelope{
		ID:      archiveConvRequestID,
		Type:    typ,
		TS:      time.Now().UTC(),
		Payload: payloadJSON,
	}
}

func assertArchiveConvEnvelopeShape(t *testing.T, resp protocol.RoutingEnvelope, wantType string) protocol.Envelope {
	t.Helper()
	if resp.ConnID != archiveConvConnID {
		t.Errorf("Response.ConnID = %q, want %q", resp.ConnID, archiveConvConnID)
	}
	var env protocol.Envelope
	if err := json.Unmarshal(resp.Frame, &env); err != nil {
		t.Fatalf("unmarshal response envelope: %v", err)
	}
	if env.Type != wantType {
		t.Errorf("Type = %q, want %q", env.Type, wantType)
	}
	if env.ID != archiveConvFirstID {
		t.Errorf("ID = %d, want %d (first reply on a fresh conn)", env.ID, archiveConvFirstID)
	}
	if env.InReplyTo == nil || *env.InReplyTo != archiveConvRequestID {
		t.Errorf("InReplyTo = %v, want pointer to %d", env.InReplyTo, archiveConvRequestID)
	}
	return env
}

// TestArchiveConversation_Success_UpdatesReplyRowAndPersists covers AC #1: a
// valid archive replies conversation_updated with is_archived == true, leaves
// every other field of the reply unchanged from the seed, flips the stored row's
// IsArchived, and a fresh Load from disk shows the row archived (proving the
// eager Save persisted the change so it survives a daemon restart).
func TestArchiveConversation_Success_UpdatesReplyRowAndPersists(t *testing.T) {
	t.Parallel()
	reg, regPath := newArchiveConvReg(t, false)
	c, recv := newArchiveConvConn(t)
	req := archiveConvRequest(t, protocol.TypeArchiveConversation,
		protocol.ArchiveConversationPayload{ConversationID: archiveConvTargetID})

	h := ArchiveConversation(reg, regPath, testLogger(t), true)
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}

	env := assertArchiveConvEnvelopeShape(t, recv(), protocol.TypeConversationUpdated)
	var payload protocol.ConversationUpdatedPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal conversation_updated payload: %v", err)
	}
	if !payload.IsArchived {
		t.Errorf("reply IsArchived = false, want true")
	}
	if payload.ID != archiveConvTargetID {
		t.Errorf("reply ID = %q, want %q", payload.ID, archiveConvTargetID)
	}
	if payload.Name == nil || *payload.Name != archiveConvName {
		t.Errorf("reply Name = %v, want pointer to %q (unchanged)", payload.Name, archiveConvName)
	}
	if payload.Cwd != archiveConvCwd {
		t.Errorf("reply Cwd = %q, want %q (unchanged)", payload.Cwd, archiveConvCwd)
	}
	if !payload.IsPromoted {
		t.Errorf("reply IsPromoted = false, want true (unchanged)")
	}
	if !payload.LastUsedAt.Equal(archiveConvSeedTime) {
		t.Errorf("reply LastUsedAt = %v, want %v (unchanged)", payload.LastUsedAt, archiveConvSeedTime)
	}

	// The stored row now carries the archived flag.
	stored, ok := reg.Get(conversations.ConversationID(archiveConvTargetID))
	if !ok {
		t.Fatalf("registry missing seeded row after archive")
	}
	if !stored.IsArchived {
		t.Errorf("stored IsArchived = false, want true")
	}

	// AC #1 restart-survival: a fresh Load from disk shows the row archived.
	reloaded, err := conversations.Load(regPath)
	if err != nil {
		t.Fatalf("reload registry from disk: %v", err)
	}
	got, ok := reloaded.Get(conversations.ConversationID(archiveConvTargetID))
	if !ok {
		t.Fatalf("archived conversation not found after reload from disk")
	}
	if !got.IsArchived {
		t.Errorf("reloaded IsArchived = false, want true (archive must persist)")
	}
}

// TestUnarchiveConversation_Success_RestoresRowAndPersists covers AC #2: a valid
// unarchive of a seeded-archived row replies conversation_updated with
// is_archived == false, clears the stored flag, and a fresh Load shows the row
// active.
func TestUnarchiveConversation_Success_RestoresRowAndPersists(t *testing.T) {
	t.Parallel()
	reg, regPath := newArchiveConvReg(t, true)
	c, recv := newArchiveConvConn(t)
	req := archiveConvRequest(t, protocol.TypeUnarchiveConversation,
		protocol.ArchiveConversationPayload{ConversationID: archiveConvTargetID})

	h := ArchiveConversation(reg, regPath, testLogger(t), false)
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}

	env := assertArchiveConvEnvelopeShape(t, recv(), protocol.TypeConversationUpdated)
	var payload protocol.ConversationUpdatedPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal conversation_updated payload: %v", err)
	}
	if payload.IsArchived {
		t.Errorf("reply IsArchived = true, want false (restored to active)")
	}

	stored, ok := reg.Get(conversations.ConversationID(archiveConvTargetID))
	if !ok {
		t.Fatalf("registry missing seeded row after unarchive")
	}
	if stored.IsArchived {
		t.Errorf("stored IsArchived = true, want false")
	}

	reloaded, err := conversations.Load(regPath)
	if err != nil {
		t.Fatalf("reload registry from disk: %v", err)
	}
	got, ok := reloaded.Get(conversations.ConversationID(archiveConvTargetID))
	if !ok {
		t.Fatalf("conversation not found after reload from disk")
	}
	if got.IsArchived {
		t.Errorf("reloaded IsArchived = true, want false (unarchive must persist)")
	}
}

// TestArchiveConversation_Idempotent covers the idempotency clauses of AC #1 and
// AC #2: toggling a row that is already in the target state is not an error,
// still persists, and still replies conversation_updated reflecting the
// (unchanged) state.
func TestArchiveConversation_Idempotent(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		typ          string
		archived     bool
		seedArchived bool
		wantArchived bool
	}{
		{"archive-already-archived", protocol.TypeArchiveConversation, true, true, true},
		{"unarchive-already-active", protocol.TypeUnarchiveConversation, false, false, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reg, regPath := newArchiveConvReg(t, tc.seedArchived)
			c, recv := newArchiveConvConn(t)
			req := archiveConvRequest(t, tc.typ,
				protocol.ArchiveConversationPayload{ConversationID: archiveConvTargetID})

			h := ArchiveConversation(reg, regPath, testLogger(t), tc.archived)
			if err := h(context.Background(), c, req); err != nil {
				t.Fatalf("handler: %v", err)
			}

			env := assertArchiveConvEnvelopeShape(t, recv(), protocol.TypeConversationUpdated)
			var payload protocol.ConversationUpdatedPayload
			if err := json.Unmarshal(env.Payload, &payload); err != nil {
				t.Fatalf("unmarshal conversation_updated payload: %v", err)
			}
			if payload.IsArchived != tc.wantArchived {
				t.Errorf("reply IsArchived = %v, want %v (unchanged idempotent state)", payload.IsArchived, tc.wantArchived)
			}

			// Persisted: a reload shows the same (unchanged) state.
			reloaded, err := conversations.Load(regPath)
			if err != nil {
				t.Fatalf("reload registry from disk: %v", err)
			}
			got, ok := reloaded.Get(conversations.ConversationID(archiveConvTargetID))
			if !ok {
				t.Fatalf("conversation not found after reload from disk")
			}
			if got.IsArchived != tc.wantArchived {
				t.Errorf("reloaded IsArchived = %v, want %v", got.IsArchived, tc.wantArchived)
			}
		})
	}
}

// TestArchiveConversation_NotFound_LeavesRegistryUnmodified covers AC #3 for both
// verbs: an unknown conversation_id yields a conversation.not_found error reply
// whose static message echoes no payload bytes, and the seeded row is untouched.
func TestArchiveConversation_NotFound_LeavesRegistryUnmodified(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		typ      string
		archived bool
	}{
		{"archive", protocol.TypeArchiveConversation, true},
		{"unarchive", protocol.TypeUnarchiveConversation, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reg, regPath := newArchiveConvReg(t, false)
			c, recv := newArchiveConvConn(t)
			injected := "../../etc/passwd\x00<script>"
			req := archiveConvRequest(t, tc.typ,
				protocol.ArchiveConversationPayload{ConversationID: injected})

			h := ArchiveConversation(reg, regPath, testLogger(t), tc.archived)
			if err := h(context.Background(), c, req); err != nil {
				t.Fatalf("handler: %v", err)
			}

			env := assertArchiveConvEnvelopeShape(t, recv(), protocol.TypeError)
			payload := assertErrorPayload(t, env, protocol.CodeConversationNotFound, msgArchiveConversationNotFound)
			if payload.Retryable {
				t.Errorf("Retryable = true, want false")
			}
			if strings.Contains(payload.Message, injected) {
				t.Errorf("reply message %q echoed the supplied id", payload.Message)
			}

			// The seeded row is untouched — the miss does not mutate the registry.
			stored, ok := reg.Get(conversations.ConversationID(archiveConvTargetID))
			if !ok {
				t.Fatalf("registry missing seeded row after not-found %s", tc.name)
			}
			if stored.IsArchived {
				t.Errorf("seeded row IsArchived = true, want false (a not-found %s must not mutate)", tc.name)
			}
		})
	}
}

// TestArchiveConversation_Malformed_DoesNotLeakPayloadBytes covers AC #4 for both
// verbs: a non-decodable payload yields a protocol.malformed error carrying a
// static message (no payload bytes on the wire), leaves the registry untouched,
// and — the security divergence from the rename template — the malformed log
// record carries NEITHER an "err" field (a json decode error can embed offending
// input bytes) NOR a "conversation_id" field (a decode failure leaves the struct
// only partially populated, so it may hold raw attacker bytes).
func TestArchiveConversation_Malformed_DoesNotLeakPayloadBytes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		typ      string
		archived bool
	}{
		{"archive", protocol.TypeArchiveConversation, true},
		{"unarchive", protocol.TypeUnarchiveConversation, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			const marker = "INJECTED_MARKER_9f3"
			var buf bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&buf, nil))

			reg, regPath := newArchiveConvReg(t, false)
			c, recv := newArchiveConvConn(t)
			// Malformed: truncated object whose raw bytes carry the marker.
			req := protocol.Envelope{
				ID:      archiveConvRequestID,
				Type:    tc.typ,
				TS:      time.Now().UTC(),
				Payload: []byte(`{"conversation_id":"` + marker + `"`),
			}

			h := ArchiveConversation(reg, regPath, logger, tc.archived)
			if err := h(context.Background(), c, req); err != nil {
				t.Fatalf("handler: %v", err)
			}

			env := assertArchiveConvEnvelopeShape(t, recv(), protocol.TypeError)
			payload := assertErrorPayload(t, env, protocol.CodeProtocolMalformed, "malformed "+tc.typ+" payload")
			if payload.Retryable {
				t.Errorf("Retryable = true, want false")
			}
			if strings.Contains(payload.Message, marker) {
				t.Errorf("wire message %q echoed payload bytes", payload.Message)
			}

			// The registry is untouched by a malformed frame.
			stored, ok := reg.Get(conversations.ConversationID(archiveConvTargetID))
			if !ok {
				t.Fatalf("registry missing seeded row after malformed %s", tc.name)
			}
			if stored.IsArchived {
				t.Errorf("seeded row mutated by a malformed %s", tc.name)
			}

			// The malformed log record must not leak payload bytes: no marker
			// anywhere, and specifically neither an "err" nor a "conversation_id"
			// field.
			logged := buf.String()
			if strings.Contains(logged, marker) {
				t.Errorf("malformed log leaked payload bytes: %s", logged)
			}
			rec := findLogRecord(t, logged, tc.typ+".malformed")
			if _, ok := rec["err"]; ok {
				t.Errorf("malformed log record carries an \"err\" field (json decode error can embed payload bytes): %v", rec)
			}
			if _, ok := rec["conversation_id"]; ok {
				t.Errorf("malformed log record carries a \"conversation_id\" field (may hold raw attacker bytes): %v", rec)
			}
			if _, ok := rec["conn_id"]; !ok {
				t.Errorf("malformed log record missing conn_id: %v", rec)
			}
		})
	}
}

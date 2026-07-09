package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	changeWSConnID    = "c-change-ws"
	changeWSRequestID = uint64(31)
	// changeWSFirstID is the id the handler's first reply must carry: on a fresh
	// conn NextID starts at 1 (change_workspace runs the dispatcher's normal
	// reply machinery, with no gate hello_ack pre-advance).
	changeWSFirstID = uint64(1)
	// changeWSTargetID is the seeded conversation's stable id — the exact
	// registry key a valid change_workspace must match.
	changeWSTargetID = "conv-change-ws-target"
	changeWSName     = "target-title"
	// changeWSOldCwd is the seeded row's recorded workspace before the change.
	changeWSOldCwd = "/work/old-workspace"
	// changeWSNewCwd is the realpath the accept-resolver returns — deliberately
	// different from changeWSOldCwd so the change is observable, and different
	// from any request path so tests prove the STORED value is the resolver's
	// realpath (validate == store), not the raw request bytes.
	changeWSNewCwd = "/home/user/projects/new-workspace"
	// changeWSRequestPath is the raw path a client sends; the accept-resolver
	// maps it to changeWSNewCwd.
	changeWSRequestPath = "~/projects/new-workspace"
)

// changeWSSeedTime is the seeded row's LastUsedAt — a fixed instant so tests can
// assert it is NOT bumped by a metadata edit.
var changeWSSeedTime = time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

// acceptResolver is a hermetic WorkspaceResolver stand-in that confines every
// input to a fixed realpath — the "the path was accepted" fake. No real
// filesystem is touched.
func acceptResolver(string) (string, error) { return changeWSNewCwd, nil }

// rejectResolver is a hermetic WorkspaceResolver stand-in that rejects every
// input, wrapping ErrWorkspaceRejected in an error whose text ECHOES the
// requested path (exactly as the real confineWorkdirToHome names the offending
// path). Tests use it to prove the handler never logs or echoes that error.
func rejectResolver(requested string) (string, error) {
	return "", fmt.Errorf("%w: %q resolves outside the home directory", ErrWorkspaceRejected, requested)
}

// newChangeWSConn returns a fresh *dispatch.Conn (NextID NOT pre-advanced, so
// the first reply lands at id=1) plus a recv helper that reads one outbound
// envelope. nil auth is fine: the change_workspace handler does not consult
// c.Auth().
func newChangeWSConn(t *testing.T) (*dispatch.Conn, func() protocol.RoutingEnvelope) {
	t.Helper()
	out := make(chan protocol.RoutingEnvelope, 4)
	c := dispatch.NewTestConn(changeWSConnID, out, nil)
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

// newChangeWSReg returns a registry backed by a temp-dir path (so the eager Save
// writes to a throwaway file) seeded with a single conversation whose id is
// changeWSTargetID and whose recorded workspace is changeWSOldCwd.
func newChangeWSReg(t *testing.T) (*conversations.Registry, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "conversations.json")
	reg, err := conversations.Load(path)
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	name := changeWSName
	reg.Create(conversations.Conversation{
		ID:         conversations.ConversationID(changeWSTargetID),
		Name:       &name,
		Cwd:        changeWSOldCwd,
		IsPromoted: true,
		LastUsedAt: changeWSSeedTime,
	})
	return reg, path
}

func changeWSRequest(t *testing.T, p protocol.ChangeWorkspacePayload) protocol.Envelope {
	t.Helper()
	payloadJSON, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return protocol.Envelope{
		ID:      changeWSRequestID,
		Type:    protocol.TypeChangeWorkspace,
		TS:      time.Now().UTC(),
		Payload: payloadJSON,
	}
}

func assertChangeWSEnvelopeShape(t *testing.T, resp protocol.RoutingEnvelope, wantType string) protocol.Envelope {
	t.Helper()
	if resp.ConnID != changeWSConnID {
		t.Errorf("Response.ConnID = %q, want %q", resp.ConnID, changeWSConnID)
	}
	var env protocol.Envelope
	if err := json.Unmarshal(resp.Frame, &env); err != nil {
		t.Fatalf("unmarshal response envelope: %v", err)
	}
	if env.Type != wantType {
		t.Errorf("Type = %q, want %q", env.Type, wantType)
	}
	if env.ID != changeWSFirstID {
		t.Errorf("ID = %d, want %d (first reply on a fresh conn)", env.ID, changeWSFirstID)
	}
	if env.InReplyTo == nil || *env.InReplyTo != changeWSRequestID {
		t.Errorf("InReplyTo = %v, want pointer to %d", env.InReplyTo, changeWSRequestID)
	}
	return env
}

// TestChangeWorkspace_Success_UpdatesRepliesPersists covers AC #1 + #2: a valid
// change_workspace sets the recorded Cwd to the resolver's realpath, replies
// conversation_updated (in_reply_to correlated, payload cwd == the realpath),
// updates the in-memory row, does NOT bump LastUsedAt (a metadata edit), and a
// fresh Load from disk shows the new cwd (proving the eager Save persisted it so
// it survives a daemon restart).
func TestChangeWorkspace_Success_UpdatesRepliesPersists(t *testing.T) {
	t.Parallel()
	reg, regPath := newChangeWSReg(t)
	c, recv := newChangeWSConn(t)
	req := changeWSRequest(t, protocol.ChangeWorkspacePayload{
		ConversationID: changeWSTargetID,
		Cwd:            changeWSRequestPath,
	})

	h := ChangeWorkspace(reg, acceptResolver, regPath, testLogger(t))
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}

	env := assertChangeWSEnvelopeShape(t, recv(), protocol.TypeConversationUpdated)
	var payload protocol.ConversationUpdatedPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal conversation_updated payload: %v", err)
	}
	if payload.ID != changeWSTargetID {
		t.Errorf("reply ID = %q, want %q", payload.ID, changeWSTargetID)
	}
	if payload.Cwd != changeWSNewCwd {
		t.Errorf("reply Cwd = %q, want the resolved realpath %q", payload.Cwd, changeWSNewCwd)
	}
	if !payload.LastUsedAt.Equal(changeWSSeedTime) {
		t.Errorf("reply LastUsedAt = %v, want unchanged %v (metadata edit)", payload.LastUsedAt, changeWSSeedTime)
	}

	// The in-memory row carries the resolved realpath, and LastUsedAt is unchanged.
	cv, ok := reg.Get(conversations.ConversationID(changeWSTargetID))
	if !ok {
		t.Fatalf("seeded row vanished")
	}
	if cv.Cwd != changeWSNewCwd {
		t.Errorf("in-memory Cwd = %q, want %q", cv.Cwd, changeWSNewCwd)
	}
	if !cv.LastUsedAt.Equal(changeWSSeedTime) {
		t.Errorf("in-memory LastUsedAt = %v, want unchanged %v", cv.LastUsedAt, changeWSSeedTime)
	}

	// AC #1 restart-survival: a fresh Load from the same path shows the new cwd.
	reloaded, err := conversations.Load(regPath)
	if err != nil {
		t.Fatalf("reload registry from disk: %v", err)
	}
	rc, ok := reloaded.Get(conversations.ConversationID(changeWSTargetID))
	if !ok {
		t.Fatalf("row absent after reload from disk")
	}
	if rc.Cwd != changeWSNewCwd {
		t.Errorf("reloaded Cwd = %q, want %q (eager Save did not persist)", rc.Cwd, changeWSNewCwd)
	}
}

// TestChangeWorkspace_ListReflectsNewWorkspace covers AC #2: after a successful
// change, a list_conversations call against the same registry surfaces the new
// workspace for the row — with no list-handler change (it reads the live
// registry).
func TestChangeWorkspace_ListReflectsNewWorkspace(t *testing.T) {
	t.Parallel()
	reg, regPath := newChangeWSReg(t)
	c, recv := newChangeWSConn(t)

	chg := ChangeWorkspace(reg, acceptResolver, regPath, testLogger(t))
	if err := chg(context.Background(), c, changeWSRequest(t, protocol.ChangeWorkspacePayload{
		ConversationID: changeWSTargetID,
		Cwd:            changeWSRequestPath,
	})); err != nil {
		t.Fatalf("change handler: %v", err)
	}
	assertChangeWSEnvelopeShape(t, recv(), protocol.TypeConversationUpdated)

	listReq := protocol.Envelope{
		ID:      changeWSRequestID + 1,
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
	var listPayload protocol.ConversationsPayload
	if err := json.Unmarshal(listEnv.Payload, &listPayload); err != nil {
		t.Fatalf("unmarshal conversations payload: %v", err)
	}
	var found bool
	for _, conv := range listPayload.Conversations {
		if conv.ID == changeWSTargetID {
			found = true
			if conv.Cwd != changeWSNewCwd {
				t.Errorf("list surfaced Cwd = %q for target, want new workspace %q", conv.Cwd, changeWSNewCwd)
			}
		}
	}
	if !found {
		t.Errorf("target %q absent from list_conversations", changeWSTargetID)
	}
}

// TestChangeWorkspace_Rejected_LeavesStateUnchangedNoLeak covers AC #3 + AC #5
// (the security-critical test): a target the resolver rejects yields a
// non-retryable protocol.malformed with the static message (no marker), leaves
// the recorded workspace unchanged, and — critically — the change_workspace.
// rejected log record carries NO "err" field and NO path/marker anywhere, only
// conn_id + conversation_id.
func TestChangeWorkspace_Rejected_LeavesStateUnchangedNoLeak(t *testing.T) {
	t.Parallel()
	const marker = "INJECTED_PATH_MARKER_7c1/../../etc"
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	reg, regPath := newChangeWSReg(t)
	c, recv := newChangeWSConn(t)
	req := changeWSRequest(t, protocol.ChangeWorkspacePayload{
		ConversationID: changeWSTargetID,
		Cwd:            marker,
	})

	h := ChangeWorkspace(reg, rejectResolver, regPath, logger)
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}

	env := assertChangeWSEnvelopeShape(t, recv(), protocol.TypeError)
	payload := assertErrorPayload(t, env, protocol.CodeProtocolMalformed, msgChangeWorkspaceRejected)
	if payload.Retryable {
		t.Errorf("Retryable = true, want false")
	}
	if strings.Contains(payload.Message, marker) {
		t.Errorf("wire message %q echoed the supplied path", payload.Message)
	}

	// The recorded workspace is untouched by a rejected target.
	cv, ok := reg.Get(conversations.ConversationID(changeWSTargetID))
	if !ok {
		t.Fatalf("seeded row vanished")
	}
	if cv.Cwd != changeWSOldCwd {
		t.Errorf("Cwd = %q after reject, want unchanged %q", cv.Cwd, changeWSOldCwd)
	}

	// The rejected log record must not leak path bytes: no marker anywhere, and
	// specifically no "err" field (the confine err names the offending path);
	// conn_id + conversation_id are the only two fields.
	logged := buf.String()
	if strings.Contains(logged, marker) {
		t.Errorf("rejected log leaked the supplied path: %s", logged)
	}
	rec := findLogRecord(t, logged, "change_workspace.rejected")
	if _, ok := rec["err"]; ok {
		t.Errorf("rejected log record carries an \"err\" field (the confine err names the path): %v", rec)
	}
	if _, ok := rec["conn_id"]; !ok {
		t.Errorf("rejected log record missing conn_id: %v", rec)
	}
	if _, ok := rec["conversation_id"]; !ok {
		t.Errorf("rejected log record missing conversation_id: %v", rec)
	}
}

// TestChangeWorkspace_NotFound_LeavesRegistryUnmodified covers AC #4: an unknown
// conversation_id (confine passes, then Update misses) yields a non-retryable
// conversation.not_found and leaves the seeded row intact.
func TestChangeWorkspace_NotFound_LeavesRegistryUnmodified(t *testing.T) {
	t.Parallel()
	reg, regPath := newChangeWSReg(t)
	c, recv := newChangeWSConn(t)
	req := changeWSRequest(t, protocol.ChangeWorkspacePayload{
		ConversationID: "no-such-conversation",
		Cwd:            changeWSRequestPath,
	})

	h := ChangeWorkspace(reg, acceptResolver, regPath, testLogger(t))
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}

	env := assertChangeWSEnvelopeShape(t, recv(), protocol.TypeError)
	payload := assertErrorPayload(t, env, protocol.CodeConversationNotFound, msgChangeWorkspaceNotFound)
	if payload.Retryable {
		t.Errorf("Retryable = true, want false")
	}

	// The seeded row is untouched — the miss mutates nothing.
	cv, ok := reg.Get(conversations.ConversationID(changeWSTargetID))
	if !ok {
		t.Fatalf("seeded row removed by a not-found change")
	}
	if cv.Cwd != changeWSOldCwd {
		t.Errorf("Cwd = %q after not-found, want unchanged %q", cv.Cwd, changeWSOldCwd)
	}
}

// TestChangeWorkspace_Malformed_DoesNotLeakPayloadBytes covers AC #4 + AC #5: a
// non-decodable payload yields a non-retryable protocol.malformed carrying the
// static message (no payload bytes on the wire), leaves the registry untouched,
// and — mirroring the delete_conversation divergence — the malformed log record
// carries NEITHER an "err" field (a json decode error can embed offending input
// bytes) NOR a "conversation_id" field (a decode failure leaves the struct only
// partially populated, so it may hold raw attacker bytes).
func TestChangeWorkspace_Malformed_DoesNotLeakPayloadBytes(t *testing.T) {
	t.Parallel()
	const marker = "INJECTED_MARKER_a4d"
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	reg, regPath := newChangeWSReg(t)
	c, recv := newChangeWSConn(t)
	// Malformed: truncated object whose raw bytes carry the marker.
	req := protocol.Envelope{
		ID:      changeWSRequestID,
		Type:    protocol.TypeChangeWorkspace,
		TS:      time.Now().UTC(),
		Payload: []byte(`{"conversation_id":"` + marker + `"`),
	}

	h := ChangeWorkspace(reg, acceptResolver, regPath, logger)
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}

	env := assertChangeWSEnvelopeShape(t, recv(), protocol.TypeError)
	payload := assertErrorPayload(t, env, protocol.CodeProtocolMalformed, msgChangeWorkspaceMalformed)
	if payload.Retryable {
		t.Errorf("Retryable = true, want false")
	}
	if strings.Contains(payload.Message, marker) {
		t.Errorf("wire message %q echoed payload bytes", payload.Message)
	}

	// The registry is untouched by a malformed frame.
	cv, ok := reg.Get(conversations.ConversationID(changeWSTargetID))
	if !ok {
		t.Fatalf("seeded row removed by a malformed change")
	}
	if cv.Cwd != changeWSOldCwd {
		t.Errorf("Cwd = %q after malformed, want unchanged %q", cv.Cwd, changeWSOldCwd)
	}

	logged := buf.String()
	if strings.Contains(logged, marker) {
		t.Errorf("malformed log leaked payload bytes: %s", logged)
	}
	rec := findLogRecord(t, logged, "change_workspace.malformed")
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

// TestChangeWorkspace_EmptyCwd_MalformedNoMutation covers AC #4: an empty target
// path is rejected as protocol.malformed (msgChangeWorkspaceEmpty), non-retryable,
// BEFORE the resolver runs — so an empty path never silently resolves to the
// daemon's process cwd. The seeded row is untouched and the resolver is never
// invoked.
func TestChangeWorkspace_EmptyCwd_MalformedNoMutation(t *testing.T) {
	t.Parallel()
	reg, regPath := newChangeWSReg(t)
	c, recv := newChangeWSConn(t)

	resolverCalled := false
	guardResolver := func(string) (string, error) {
		resolverCalled = true
		return changeWSNewCwd, nil
	}

	// A whitespace-only cwd must trip the same guard as "".
	req := changeWSRequest(t, protocol.ChangeWorkspacePayload{
		ConversationID: changeWSTargetID,
		Cwd:            "   ",
	})

	h := ChangeWorkspace(reg, guardResolver, regPath, testLogger(t))
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}

	env := assertChangeWSEnvelopeShape(t, recv(), protocol.TypeError)
	payload := assertErrorPayload(t, env, protocol.CodeProtocolMalformed, msgChangeWorkspaceEmpty)
	if payload.Retryable {
		t.Errorf("Retryable = true, want false")
	}
	if resolverCalled {
		t.Errorf("resolver invoked on an empty cwd, want the empty guard to short-circuit before confine")
	}

	cv, ok := reg.Get(conversations.ConversationID(changeWSTargetID))
	if !ok {
		t.Fatalf("seeded row vanished")
	}
	if cv.Cwd != changeWSOldCwd {
		t.Errorf("Cwd = %q after empty-cwd reject, want unchanged %q", cv.Cwd, changeWSOldCwd)
	}
}

// TestChangeWorkspace_NoEcho_RejectMessagesAreStatic covers AC #5's no-echo
// discipline across the reject branches: an injected-looking id/path never
// appears in the not_found reply message — only the fixed static string is sent.
func TestChangeWorkspace_NoEcho_RejectMessagesAreStatic(t *testing.T) {
	t.Parallel()
	reg, regPath := newChangeWSReg(t)
	c, recv := newChangeWSConn(t)
	injected := "../../etc/passwd\x00<script>"
	req := changeWSRequest(t, protocol.ChangeWorkspacePayload{
		ConversationID: injected,
		Cwd:            injected,
	})

	h := ChangeWorkspace(reg, acceptResolver, regPath, testLogger(t))
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}

	env := assertChangeWSEnvelopeShape(t, recv(), protocol.TypeError)
	payload := assertErrorPayload(t, env, protocol.CodeConversationNotFound, msgChangeWorkspaceNotFound)
	if strings.Contains(payload.Message, injected) {
		t.Errorf("reply message %q echoed the supplied bytes", payload.Message)
	}
}

package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

const (
	cwfConnID    = "c-create-ws-folder"
	cwfRequestID = uint64(41)
	// cwfFirstID is the id the handler's first reply must carry: on a fresh conn
	// NextID starts at 1 (create_workspace_folder runs the dispatcher's normal
	// reply machinery, with no gate hello_ack pre-advance).
	cwfFirstID = uint64(1)
	// cwfCreatedPath is the realpath the accept-resolver returns — deliberately
	// distinct from any request field so a test proves the reply carries the
	// resolver's realpath, not raw request bytes.
	cwfCreatedPath = "/home/user/projects/new-app"
	cwfParent      = "~/projects"
	cwfName        = "new-app"
)

// recordingFolderResolver is a hermetic WorkspaceFolderResolver stand-in: it
// records the arguments and call count so tests can prove the handler forwards the
// exact (parent, name) and that the pre-resolve guards short-circuit before it
// runs. No real filesystem is touched. ret is returned on success; when err is
// non-nil it is returned instead (the reject fake).
type recordingFolderResolver struct {
	gotParent string
	gotName   string
	calls     int
	ret       string
	err       error
}

func (r *recordingFolderResolver) resolve(parent, name string) (string, error) {
	r.calls++
	r.gotParent = parent
	r.gotName = name
	if r.err != nil {
		return "", r.err
	}
	return r.ret, nil
}

// newCWFConn returns a fresh *dispatch.Conn (NextID NOT pre-advanced, so the first
// reply lands at id=1) plus a recv helper that reads one outbound envelope. nil
// auth is fine: the create_workspace_folder handler does not consult c.Auth().
func newCWFConn(t *testing.T) (*dispatch.Conn, func() protocol.RoutingEnvelope) {
	t.Helper()
	out := make(chan protocol.RoutingEnvelope, 4)
	c := dispatch.NewTestConn(cwfConnID, out, nil)
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

func cwfRequest(t *testing.T, p protocol.CreateWorkspaceFolderPayload) protocol.Envelope {
	t.Helper()
	return protocol.Envelope{
		ID:      cwfRequestID,
		Type:    protocol.TypeCreateWorkspaceFolder,
		TS:      time.Now().UTC(),
		Payload: mustMarshal(t, p),
	}
}

func assertCWFEnvelopeShape(t *testing.T, resp protocol.RoutingEnvelope, wantType string) protocol.Envelope {
	t.Helper()
	if resp.ConnID != cwfConnID {
		t.Errorf("Response.ConnID = %q, want %q", resp.ConnID, cwfConnID)
	}
	var env protocol.Envelope
	if err := json.Unmarshal(resp.Frame, &env); err != nil {
		t.Fatalf("unmarshal response envelope: %v", err)
	}
	if env.Type != wantType {
		t.Errorf("Type = %q, want %q", env.Type, wantType)
	}
	if env.ID != cwfFirstID {
		t.Errorf("ID = %d, want %d (first reply on a fresh conn)", env.ID, cwfFirstID)
	}
	if env.InReplyTo == nil || *env.InReplyTo != cwfRequestID {
		t.Errorf("InReplyTo = %v, want pointer to %d", env.InReplyTo, cwfRequestID)
	}
	return env
}

// TestCreateWorkspaceFolder_Success_RepliesCreatedPath covers AC #1 + #4: a valid
// request replies workspace_folder_created (in_reply_to correlated) whose path is
// the resolver's returned realpath, and the resolver was invoked exactly once with
// the exact parent + name from the payload.
func TestCreateWorkspaceFolder_Success_RepliesCreatedPath(t *testing.T) {
	t.Parallel()
	c, recv := newCWFConn(t)
	rec := &recordingFolderResolver{ret: cwfCreatedPath}

	h := CreateWorkspaceFolder(rec.resolve, testLogger(t))
	if err := h(context.Background(), c, cwfRequest(t, protocol.CreateWorkspaceFolderPayload{
		Parent: cwfParent,
		Name:   cwfName,
	})); err != nil {
		t.Fatalf("handler: %v", err)
	}

	env := assertCWFEnvelopeShape(t, recv(), protocol.TypeWorkspaceFolderCreated)
	var payload protocol.WorkspaceFolderCreatedPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal workspace_folder_created payload: %v", err)
	}
	if payload.Path != cwfCreatedPath {
		t.Errorf("reply Path = %q, want the resolver's realpath %q", payload.Path, cwfCreatedPath)
	}
	if rec.calls != 1 {
		t.Fatalf("resolver called %d times, want exactly 1", rec.calls)
	}
	if rec.gotParent != cwfParent || rec.gotName != cwfName {
		t.Errorf("resolver got (%q, %q), want (%q, %q)", rec.gotParent, rec.gotName, cwfParent, cwfName)
	}
}

// TestCreateWorkspaceFolder_Rejected_MalformedNoLeak covers AC #2 + AC #7 (the
// security-critical test): a resolver rejection yields a non-retryable
// protocol.malformed with the static message (no marker), and the rejected log
// record carries NO "err", NO parent/name/path/marker anywhere — only conn_id.
func TestCreateWorkspaceFolder_Rejected_MalformedNoLeak(t *testing.T) {
	t.Parallel()
	const marker = "INJECTED_PATH_MARKER_9f2/../../etc"
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	c, recv := newCWFConn(t)
	// reject-fake whose error text ECHOES the marker, exactly as the real
	// confineWorkdirToHomeCreating names the offending path.
	rec := &recordingFolderResolver{
		err: fmt.Errorf("%w: %q resolves outside the home directory", ErrWorkspaceFolderRejected, marker),
	}

	h := CreateWorkspaceFolder(rec.resolve, logger)
	if err := h(context.Background(), c, cwfRequest(t, protocol.CreateWorkspaceFolderPayload{
		Parent: marker,
		Name:   cwfName,
	})); err != nil {
		t.Fatalf("handler: %v", err)
	}

	env := assertCWFEnvelopeShape(t, recv(), protocol.TypeError)
	payload := assertErrorPayload(t, env, protocol.CodeProtocolMalformed, msgCreateWorkspaceFolderRejected)
	if payload.Retryable {
		t.Errorf("Retryable = true, want false")
	}
	if strings.Contains(payload.Message, marker) {
		t.Errorf("wire message %q echoed the supplied path", payload.Message)
	}

	logged := buf.String()
	if strings.Contains(logged, marker) {
		t.Errorf("rejected log leaked the supplied path: %s", logged)
	}
	logRec := findLogRecord(t, logged, "create_workspace_folder.rejected")
	if _, ok := logRec["err"]; ok {
		t.Errorf("rejected log record carries an \"err\" field (the confine err names the path): %v", logRec)
	}
	// conn_id is the ONLY safe structured field — both parent and name are
	// attacker-controlled path components.
	if _, ok := logRec["conn_id"]; !ok {
		t.Errorf("rejected log record missing conn_id: %v", logRec)
	}
	if _, ok := logRec["parent"]; ok {
		t.Errorf("rejected log record carries a \"parent\" field (attacker path): %v", logRec)
	}
	if _, ok := logRec["name"]; ok {
		t.Errorf("rejected log record carries a \"name\" field (attacker path): %v", logRec)
	}
}

// TestCreateWorkspaceFolder_EmptyParent_MalformedResolverNotCalled covers AC #7: an
// empty/whitespace parent is rejected as protocol.malformed BEFORE the resolver
// runs — so an empty parent never silently resolves under the daemon's process cwd.
func TestCreateWorkspaceFolder_EmptyParent_MalformedResolverNotCalled(t *testing.T) {
	t.Parallel()
	for _, parent := range []string{"", "   "} {
		t.Run(fmt.Sprintf("parent=%q", parent), func(t *testing.T) {
			c, recv := newCWFConn(t)
			rec := &recordingFolderResolver{ret: cwfCreatedPath}

			h := CreateWorkspaceFolder(rec.resolve, testLogger(t))
			if err := h(context.Background(), c, cwfRequest(t, protocol.CreateWorkspaceFolderPayload{
				Parent: parent,
				Name:   cwfName,
			})); err != nil {
				t.Fatalf("handler: %v", err)
			}

			env := assertCWFEnvelopeShape(t, recv(), protocol.TypeError)
			payload := assertErrorPayload(t, env, protocol.CodeProtocolMalformed, msgCreateWorkspaceFolderEmptyParent)
			if payload.Retryable {
				t.Errorf("Retryable = true, want false")
			}
			if rec.calls != 0 {
				t.Errorf("resolver called %d times on empty parent, want 0 (guard precedes resolve)", rec.calls)
			}
		})
	}
}

// TestCreateWorkspaceFolder_BadName_MalformedResolverNotCalled covers AC #3: a name
// that is not a single clean path element (separator, "..", absolute, empty) is
// rejected as protocol.malformed BEFORE the resolver runs, so the folder can never
// land outside the parent.
func TestCreateWorkspaceFolder_BadName_MalformedResolverNotCalled(t *testing.T) {
	t.Parallel()
	names := []string{"a/b", "../x", "x/..", "/abs", "..", "", "  ", "sub/dir", "a/../b"}
	for _, name := range names {
		t.Run(fmt.Sprintf("name=%q", name), func(t *testing.T) {
			c, recv := newCWFConn(t)
			rec := &recordingFolderResolver{ret: cwfCreatedPath}

			h := CreateWorkspaceFolder(rec.resolve, testLogger(t))
			if err := h(context.Background(), c, cwfRequest(t, protocol.CreateWorkspaceFolderPayload{
				Parent: cwfParent,
				Name:   name,
			})); err != nil {
				t.Fatalf("handler: %v", err)
			}

			env := assertCWFEnvelopeShape(t, recv(), protocol.TypeError)
			payload := assertErrorPayload(t, env, protocol.CodeProtocolMalformed, msgCreateWorkspaceFolderBadName)
			if payload.Retryable {
				t.Errorf("Retryable = true, want false")
			}
			if rec.calls != 0 {
				t.Errorf("resolver called %d times on bad name %q, want 0", rec.calls, name)
			}
		})
	}
}

// TestCreateWorkspaceFolder_Malformed_DoesNotLeakPayloadBytes covers AC #7: a
// non-decodable payload yields a non-retryable protocol.malformed carrying the
// static message (no payload bytes on the wire), never invokes the resolver, and
// the malformed log record carries NO "err" field (a json decode error can embed
// offending input bytes) — only conn_id.
func TestCreateWorkspaceFolder_Malformed_DoesNotLeakPayloadBytes(t *testing.T) {
	t.Parallel()
	const marker = "INJECTED_MARKER_c7e"
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	c, recv := newCWFConn(t)
	rec := &recordingFolderResolver{ret: cwfCreatedPath}
	// Malformed: truncated object whose raw bytes carry the marker.
	req := protocol.Envelope{
		ID:      cwfRequestID,
		Type:    protocol.TypeCreateWorkspaceFolder,
		TS:      time.Now().UTC(),
		Payload: []byte(`{"parent":"` + marker + `"`),
	}

	h := CreateWorkspaceFolder(rec.resolve, logger)
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}

	env := assertCWFEnvelopeShape(t, recv(), protocol.TypeError)
	payload := assertErrorPayload(t, env, protocol.CodeProtocolMalformed, msgCreateWorkspaceFolderMalformed)
	if payload.Retryable {
		t.Errorf("Retryable = true, want false")
	}
	if strings.Contains(payload.Message, marker) {
		t.Errorf("wire message %q echoed payload bytes", payload.Message)
	}
	if rec.calls != 0 {
		t.Errorf("resolver called on a malformed payload, want 0")
	}

	logged := buf.String()
	if strings.Contains(logged, marker) {
		t.Errorf("malformed log leaked payload bytes: %s", logged)
	}
	logRec := findLogRecord(t, logged, "create_workspace_folder.malformed")
	if _, ok := logRec["err"]; ok {
		t.Errorf("malformed log record carries an \"err\" field (json decode error can embed payload bytes): %v", logRec)
	}
	if _, ok := logRec["conn_id"]; !ok {
		t.Errorf("malformed log record missing conn_id: %v", logRec)
	}
}

// TestCreateWorkspaceFolder_NoEcho_RejectMessageIsStatic covers AC #7's no-echo
// discipline: an injected-looking name never appears in the reject reply Message —
// only the fixed static string is sent.
func TestCreateWorkspaceFolder_NoEcho_RejectMessageIsStatic(t *testing.T) {
	t.Parallel()
	c, recv := newCWFConn(t)
	injected := "../../etc/passwd\x00<script>"
	rec := &recordingFolderResolver{ret: cwfCreatedPath}

	h := CreateWorkspaceFolder(rec.resolve, testLogger(t))
	if err := h(context.Background(), c, cwfRequest(t, protocol.CreateWorkspaceFolderPayload{
		Parent: cwfParent,
		Name:   injected,
	})); err != nil {
		t.Fatalf("handler: %v", err)
	}

	env := assertCWFEnvelopeShape(t, recv(), protocol.TypeError)
	payload := assertErrorPayload(t, env, protocol.CodeProtocolMalformed, msgCreateWorkspaceFolderBadName)
	if strings.Contains(payload.Message, injected) {
		t.Errorf("reply message %q echoed the supplied name", payload.Message)
	}
	if rec.calls != 0 {
		t.Errorf("resolver called on a bad name, want 0")
	}
}

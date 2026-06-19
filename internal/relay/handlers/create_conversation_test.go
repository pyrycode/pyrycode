package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// stubSessionCreator records each Create(ctx, label, spawnDir) call and returns
// a configurable id + error. With err == nil and id == "" it returns a fresh
// distinct id per call ("sess-1", "sess-2", …) so per-conversation distinctness
// is observable without per-test wiring; a fixed id pins the binding assertion.
// The stub IS the cmd-layer seam: it records the raw spawnDir the handler
// forwards (no validation happens handler-side). Mirrors stubTurnWriter in
// send_message_test.go.
type stubSessionCreator struct {
	err       error  // when non-nil, every Create returns ("", err)
	id        string // when non-empty, every Create returns this fixed id
	calls     int
	labels    []string
	spawnDirs []string // the spawnDir arg recorded per call
}

func (s *stubSessionCreator) Create(ctx context.Context, label, spawnDir string) (string, error) {
	s.calls++
	s.labels = append(s.labels, label)
	s.spawnDirs = append(s.spawnDirs, spawnDir)
	if s.err != nil {
		return "", s.err
	}
	if s.id != "" {
		return s.id, nil
	}
	return fmt.Sprintf("sess-%d", s.calls), nil
}

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

	h := CreateConversation(reg, &stubSessionCreator{}, regPath, createConvDefault, testLogger(t))
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

	h := CreateConversation(reg, &stubSessionCreator{}, regPath, createConvDefault, testLogger(t))
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
// fresh Load from disk, proving it survives a daemon-process restart. It also
// covers AC#3 — the CurrentSessionID binding given at create time round-trips
// through the registry's atomic Save/Load unchanged.
func TestCreateConversation_EagerPersist_SurvivesReload(t *testing.T) {
	t.Parallel()
	reg, regPath := newCreateConvReg(t)
	c, recv := newCreateConvConn(t)
	req := createConvRequest(t, protocol.CreateConversationPayload{})

	h := CreateConversation(reg, &stubSessionCreator{}, regPath, createConvDefault, testLogger(t))
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}
	env := assertCreateConvEnvelopeShape(t, recv(), protocol.TypeConversationCreated)
	var payload protocol.ConversationCreatedPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}

	// Capture the binding recorded in-memory at create time so the reload
	// assertion compares against the value actually given, not a literal.
	created, ok := reg.Get(conversations.ConversationID(payload.ID))
	if !ok {
		t.Fatalf("registry missing created row %q", payload.ID)
	}
	if created.CurrentSessionID == "" {
		t.Fatalf("created row has empty CurrentSessionID; expected a bound session")
	}

	reloaded, err := conversations.Load(regPath)
	if err != nil {
		t.Fatalf("reload registry from disk: %v", err)
	}
	got, ok := reloaded.Get(conversations.ConversationID(payload.ID))
	if !ok {
		t.Fatalf("created conversation %q not found after reload from disk", payload.ID)
	}
	if got.CurrentSessionID != created.CurrentSessionID {
		t.Errorf("reloaded CurrentSessionID = %q, want %q (binding must round-trip)", got.CurrentSessionID, created.CurrentSessionID)
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

	creator := &stubSessionCreator{}
	h := CreateConversation(reg, creator, regPath, createConvDefault, testLogger(t))
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
	// A malformed frame must short-circuit before the mint — no session spawned.
	if creator.calls != 0 {
		t.Errorf("creator.Create called %d times on a malformed frame, want 0", creator.calls)
	}
}

// TestCreateConversation_BindsDedicatedSession covers AC#1: a create frame mints
// exactly one session and records its id on the conversation row, and the mint
// label is the server-minted conversation id echoed in the reply (a stable
// session↔conversation breadcrumb). Replaces the pre-eager-binding
// "no claude session spawned at create time" test, whose premise inverts here.
func TestCreateConversation_BindsDedicatedSession(t *testing.T) {
	t.Parallel()
	reg, regPath := newCreateConvReg(t)
	c, recv := newCreateConvConn(t)
	req := createConvRequest(t, protocol.CreateConversationPayload{})

	creator := &stubSessionCreator{id: "sess-bound"}
	h := CreateConversation(reg, creator, regPath, createConvDefault, testLogger(t))
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}
	env := assertCreateConvEnvelopeShape(t, recv(), protocol.TypeConversationCreated)
	var payload protocol.ConversationCreatedPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}

	// Exactly one mint, labelled with the conversation id echoed in the reply.
	if creator.calls != 1 {
		t.Fatalf("creator.Create called %d times, want exactly 1", creator.calls)
	}
	if creator.labels[0] != payload.ID {
		t.Errorf("mint label = %q, want the conversation id %q", creator.labels[0], payload.ID)
	}

	// The stored row points at the minted session.
	stored, ok := reg.Get(conversations.ConversationID(payload.ID))
	if !ok {
		t.Fatalf("registry has no row for created id %q", payload.ID)
	}
	if stored.CurrentSessionID != "sess-bound" {
		t.Errorf("stored CurrentSessionID = %q, want %q (the minted session id)", stored.CurrentSessionID, "sess-bound")
	}
}

// TestCreateConversation_DistinctSessionPerConversation covers AC#2: two create
// frames yield two rows bound to two different, non-empty session ids.
func TestCreateConversation_DistinctSessionPerConversation(t *testing.T) {
	t.Parallel()
	reg, regPath := newCreateConvReg(t)
	creator := &stubSessionCreator{} // default: a fresh distinct id per call
	h := CreateConversation(reg, creator, regPath, createConvDefault, testLogger(t))

	ids := make([]string, 0, 2)
	for i := 0; i < 2; i++ {
		c, recv := newCreateConvConn(t)
		req := createConvRequest(t, protocol.CreateConversationPayload{})
		if err := h(context.Background(), c, req); err != nil {
			t.Fatalf("handler call %d: %v", i, err)
		}
		env := assertCreateConvEnvelopeShape(t, recv(), protocol.TypeConversationCreated)
		var payload protocol.ConversationCreatedPayload
		if err := json.Unmarshal(env.Payload, &payload); err != nil {
			t.Fatalf("unmarshal payload %d: %v", i, err)
		}
		stored, ok := reg.Get(conversations.ConversationID(payload.ID))
		if !ok {
			t.Fatalf("registry missing row for created id %q", payload.ID)
		}
		if stored.CurrentSessionID == "" {
			t.Fatalf("row %d has empty CurrentSessionID, want a bound session", i)
		}
		ids = append(ids, stored.CurrentSessionID)
	}
	if ids[0] == ids[1] {
		t.Errorf("both conversations bound the same session id %q, want distinct sessions", ids[0])
	}
}

// TestCreateConversation_SetCwd_ThreadsRawSpawnDir covers AC#1: a set Cwd is
// forwarded verbatim as the mint's spawnDir (the cmd-layer seam, here the stub,
// validates it — the handler does no path handling). The row + reply still record
// the raw requested Cwd, and the mint label remains the server-minted id, never
// the cwd. Replaces the pre-#685 CwdNeverReachesMint test, whose premise inverts.
func TestCreateConversation_SetCwd_ThreadsRawSpawnDir(t *testing.T) {
	t.Parallel()
	reg, regPath := newCreateConvReg(t)
	c, recv := newCreateConvConn(t)

	cwd := "/work/phone-chosen/dir"
	req := createConvRequest(t, protocol.CreateConversationPayload{Cwd: &cwd})

	creator := &stubSessionCreator{}
	h := CreateConversation(reg, creator, regPath, createConvDefault, testLogger(t))
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}
	env := assertCreateConvEnvelopeShape(t, recv(), protocol.TypeConversationCreated)
	var payload protocol.ConversationCreatedPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}

	if creator.calls != 1 {
		t.Fatalf("creator.Create called %d times, want exactly 1", creator.calls)
	}
	// The set Cwd reaches the spawn path verbatim as spawnDir.
	if creator.spawnDirs[0] != cwd {
		t.Errorf("mint spawnDir = %q, want the requested cwd %q", creator.spawnDirs[0], cwd)
	}
	// The label remains the server-minted conversation id, never the cwd.
	if creator.labels[0] != payload.ID || !conversations.ValidID(creator.labels[0]) {
		t.Errorf("mint label = %q, want the conversation id %q (a canonical UUIDv4)", creator.labels[0], payload.ID)
	}
	// The row + reply record the raw requested Cwd.
	if payload.Cwd != cwd {
		t.Errorf("reply Cwd = %q, want %q", payload.Cwd, cwd)
	}
	stored, _ := reg.Get(conversations.ConversationID(payload.ID))
	if stored.Cwd != cwd {
		t.Errorf("stored Cwd = %q, want %q", stored.Cwd, cwd)
	}
}

// TestCreateConversation_NullCwd_EmptySpawnDir covers AC#4: a null Cwd yields an
// empty spawnDir (→ the shared trusted workdir downstream, byte-identical to
// today) while the recorded row still carries defaultCwd. This pins the
// "where to spawn" (empty) vs "what to record" (defaultCwd) separation.
func TestCreateConversation_NullCwd_EmptySpawnDir(t *testing.T) {
	t.Parallel()
	reg, regPath := newCreateConvReg(t)
	c, recv := newCreateConvConn(t)
	req := createConvRequest(t, protocol.CreateConversationPayload{}) // Cwd nil

	creator := &stubSessionCreator{}
	h := CreateConversation(reg, creator, regPath, createConvDefault, testLogger(t))
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}
	env := assertCreateConvEnvelopeShape(t, recv(), protocol.TypeConversationCreated)
	var payload protocol.ConversationCreatedPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}

	if creator.calls != 1 {
		t.Fatalf("creator.Create called %d times, want exactly 1", creator.calls)
	}
	if creator.spawnDirs[0] != "" {
		t.Errorf("mint spawnDir = %q, want \"\" (null Cwd → shared workdir)", creator.spawnDirs[0])
	}
	if payload.Cwd != createConvDefault {
		t.Errorf("reply Cwd = %q, want %q (defaultCwd recorded for null Cwd)", payload.Cwd, createConvDefault)
	}
	stored, _ := reg.Get(conversations.ConversationID(payload.ID))
	if stored.Cwd != createConvDefault {
		t.Errorf("stored Cwd = %q, want %q", stored.Cwd, createConvDefault)
	}
}

// TestCreateConversation_SpawnDirRejected_NonRetryableMalformed covers AC#2: when
// the cmd-layer seam rejects the requested Cwd (wraps ErrSpawnDirRejected), the
// handler replies a NON-retryable protocol.malformed with the static message
// (no path echoed) and records no row — no half-bound conversation.
func TestCreateConversation_SpawnDirRejected_NonRetryableMalformed(t *testing.T) {
	t.Parallel()
	reg, regPath := newCreateConvReg(t)
	c, recv := newCreateConvConn(t)
	cwd := "/etc/escape"
	req := createConvRequest(t, protocol.CreateConversationPayload{Cwd: &cwd})

	creator := &stubSessionCreator{err: fmt.Errorf("confine workdir: %w", ErrSpawnDirRejected)}
	h := CreateConversation(reg, creator, regPath, createConvDefault, testLogger(t))
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
		t.Errorf("Retryable = true, want false (a $HOME escape is deterministic)")
	}
	if payload.Message != msgCreateConversationCwdRejected {
		t.Errorf("Message = %q, want static %q", payload.Message, msgCreateConversationCwdRejected)
	}
	if got := reg.List(); len(got) != 0 {
		t.Errorf("registry has %d rows after a rejected Cwd, want 0 (no half-bound row)", len(got))
	}
}

// TestCreateConversation_MintFailure_NoRowAndBinaryOffline covers the mint-error
// path: when the session mint fails, the handler replies a retryable
// server.binary_offline and creates no conversation row (no half-bound orphan).
func TestCreateConversation_MintFailure_NoRowAndBinaryOffline(t *testing.T) {
	t.Parallel()
	reg, regPath := newCreateConvReg(t)
	c, recv := newCreateConvConn(t)
	req := createConvRequest(t, protocol.CreateConversationPayload{})

	creator := &stubSessionCreator{err: fmt.Errorf("pool not running")}
	h := CreateConversation(reg, creator, regPath, createConvDefault, testLogger(t))
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}

	env := assertCreateConvEnvelopeShape(t, recv(), protocol.TypeError)
	var payload protocol.ErrorPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal error payload: %v", err)
	}
	if payload.Code != protocol.CodeServerBinaryOffline {
		t.Errorf("Code = %q, want %q", payload.Code, protocol.CodeServerBinaryOffline)
	}
	if !payload.Retryable {
		t.Errorf("Retryable = false, want true (the phone retries onto a fresh conversation)")
	}
	if payload.Message != msgCreateConversationMintFailed {
		t.Errorf("Message = %q, want %q", payload.Message, msgCreateConversationMintFailed)
	}
	if got := reg.List(); len(got) != 0 {
		t.Errorf("registry has %d rows after a mint failure, want 0 (no half-bound orphan)", len(got))
	}
}

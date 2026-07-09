package handlers

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

const recentWSConnID = "conn-recent-ws"

func makeRecentWorkspacesFrame(t *testing.T, id uint64) protocol.RoutingEnvelope {
	t.Helper()
	env := protocol.Envelope{
		ID:      id,
		Type:    protocol.TypeRecentWorkspaces,
		TS:      time.Now().UTC(),
		Payload: json.RawMessage("{}"),
	}
	frame, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	return protocol.RoutingEnvelope{ConnID: recentWSConnID, Frame: frame}
}

func decodeRecentWorkspacesResponse(t *testing.T, out protocol.RoutingEnvelope) (protocol.Envelope, protocol.RecentWorkspacesListPayload) {
	t.Helper()
	var inner protocol.Envelope
	if err := json.Unmarshal(out.Frame, &inner); err != nil {
		t.Fatalf("decode inner envelope: %v", err)
	}
	if inner.Type != protocol.TypeRecentWorkspacesList {
		t.Fatalf("inner.Type: got %q, want %q", inner.Type, protocol.TypeRecentWorkspacesList)
	}
	var payload protocol.RecentWorkspacesListPayload
	if err := json.Unmarshal(inner.Payload, &payload); err != nil {
		t.Fatalf("decode recent_workspaces_list payload: %v", err)
	}
	return inner, payload
}

// runRecentWorkspaces registers the handler on a fresh dispatcher, sends one
// recent_workspaces frame with the given request id, and returns the decoded
// reply. It centralizes the dispatcher plumbing shared by every scenario below.
func runRecentWorkspaces(t *testing.T, reg *conversations.Registry, reqID uint64) (protocol.Envelope, protocol.RecentWorkspacesListPayload) {
	t.Helper()
	in := make(chan protocol.RoutingEnvelope, 1)
	d := dispatch.New(dispatch.Config{Frames: in, Logger: testLogger(t)})
	d.Register(protocol.TypeRecentWorkspaces, RecentWorkspaces(reg))
	stop := runListConvDispatcher(t, d)
	defer stop()

	in <- makeRecentWorkspacesFrame(t, reqID)
	out := recvOutbound(t, d)
	return decodeRecentWorkspacesResponse(t, out)
}

func TestRecentWorkspaces_EmptyRegistry(t *testing.T) {
	t.Parallel()
	reg := &conversations.Registry{}
	inner, payload := runRecentWorkspaces(t, reg, 11)

	if inner.InReplyTo == nil || *inner.InReplyTo != 11 {
		t.Errorf("InReplyTo: got %v, want pointer to 11", inner.InReplyTo)
	}
	if inner.ID != 1 {
		t.Errorf("inner.ID: got %d, want 1", inner.ID)
	}
	if inner.TS.IsZero() {
		t.Error("inner.TS: got zero time, want non-zero")
	}
	if payload.Workspaces == nil {
		t.Fatal("Workspaces: got nil slice, want empty non-nil slice")
	}
	if len(payload.Workspaces) != 0 {
		t.Errorf("len(Workspaces): got %d, want 0", len(payload.Workspaces))
	}
	// Empty list serializes as "[]", not "null".
	wantPayloadBytes := []byte(`{"workspaces":[]}`)
	if string(inner.Payload) != string(wantPayloadBytes) {
		t.Errorf("payload bytes: got %s, want %s", inner.Payload, wantPayloadBytes)
	}
}

func TestRecentWorkspaces_SingleConversation(t *testing.T) {
	t.Parallel()
	reg := &conversations.Registry{}
	ts := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	reg.Create(conversations.Conversation{ID: "conv-1", Cwd: "/work/proj", LastUsedAt: ts})

	inner, payload := runRecentWorkspaces(t, reg, 99)

	if inner.InReplyTo == nil || *inner.InReplyTo != 99 {
		t.Errorf("InReplyTo: got %v, want pointer to 99", inner.InReplyTo)
	}
	if len(payload.Workspaces) != 1 {
		t.Fatalf("len(Workspaces): got %d, want 1", len(payload.Workspaces))
	}
	got := payload.Workspaces[0]
	if got.Path != "/work/proj" {
		t.Errorf("Path: got %q, want %q", got.Path, "/work/proj")
	}
	if !got.LastUsedAt.Equal(ts) {
		t.Errorf("LastUsedAt: got %v, want %v", got.LastUsedAt, ts)
	}
}

// Two conversations sharing a Cwd collapse to one entry carrying the MAX
// LastUsedAt of the two — the folder's recency is its most-recent use.
func TestRecentWorkspaces_DedupesByCwdKeepingMaxLastUsedAt(t *testing.T) {
	t.Parallel()
	reg := &conversations.Registry{}
	tOld := time.Date(2026, 5, 10, 9, 0, 0, 0, time.UTC)
	tNew := time.Date(2026, 5, 12, 9, 0, 0, 0, time.UTC)
	reg.Create(conversations.Conversation{ID: "conv-old", Cwd: "/shared", LastUsedAt: tOld})
	reg.Create(conversations.Conversation{ID: "conv-new", Cwd: "/shared", LastUsedAt: tNew})

	_, payload := runRecentWorkspaces(t, reg, 3)

	if len(payload.Workspaces) != 1 {
		t.Fatalf("len(Workspaces): got %d, want 1 (deduped by Cwd)", len(payload.Workspaces))
	}
	if !payload.Workspaces[0].LastUsedAt.Equal(tNew) {
		t.Errorf("LastUsedAt: got %v, want %v (max of the two)", payload.Workspaces[0].LastUsedAt, tNew)
	}
}

// Distinct workspaces are ordered most-recent-first by their max LastUsedAt.
func TestRecentWorkspaces_OrderedMostRecentFirst(t *testing.T) {
	t.Parallel()
	reg := &conversations.Registry{}
	tEarly := time.Date(2026, 5, 10, 10, 0, 0, 0, time.UTC)
	tMid := time.Date(2026, 5, 11, 10, 0, 0, 0, time.UTC)
	tLate := time.Date(2026, 5, 12, 10, 0, 0, 0, time.UTC)

	// Seed in an order opposite to the expected sort.
	reg.Create(conversations.Conversation{ID: "conv-early", Cwd: "/early", LastUsedAt: tEarly})
	reg.Create(conversations.Conversation{ID: "conv-late", Cwd: "/late", LastUsedAt: tLate})
	reg.Create(conversations.Conversation{ID: "conv-mid", Cwd: "/mid", LastUsedAt: tMid})

	_, payload := runRecentWorkspaces(t, reg, 5)

	gotPaths := make([]string, 0, len(payload.Workspaces))
	for _, w := range payload.Workspaces {
		gotPaths = append(gotPaths, w.Path)
	}
	wantPaths := []string{"/late", "/mid", "/early"}
	if len(gotPaths) != len(wantPaths) {
		t.Fatalf("paths: got %v, want %v", gotPaths, wantPaths)
	}
	for i := range gotPaths {
		if gotPaths[i] != wantPaths[i] {
			t.Fatalf("paths: got %v, want %v", gotPaths, wantPaths)
		}
	}
}

// A tie on LastUsedAt across two workspaces is broken deterministically by Path
// ascending, so the reply is stable regardless of map iteration order.
func TestRecentWorkspaces_TieBreaksByPathAscending(t *testing.T) {
	t.Parallel()
	reg := &conversations.Registry{}
	ts := time.Date(2026, 5, 11, 10, 0, 0, 0, time.UTC)
	reg.Create(conversations.Conversation{ID: "conv-b", Cwd: "/b", LastUsedAt: ts})
	reg.Create(conversations.Conversation{ID: "conv-a", Cwd: "/a", LastUsedAt: ts})
	reg.Create(conversations.Conversation{ID: "conv-c", Cwd: "/c", LastUsedAt: ts})

	_, payload := runRecentWorkspaces(t, reg, 6)

	gotPaths := make([]string, 0, len(payload.Workspaces))
	for _, w := range payload.Workspaces {
		gotPaths = append(gotPaths, w.Path)
	}
	wantPaths := []string{"/a", "/b", "/c"}
	if len(gotPaths) != len(wantPaths) {
		t.Fatalf("paths: got %v, want %v", gotPaths, wantPaths)
	}
	for i := range gotPaths {
		if gotPaths[i] != wantPaths[i] {
			t.Fatalf("paths: got %v, want %v", gotPaths, wantPaths)
		}
	}
}

// A workspace whose only conversation is archived still appears (#888 design
// decision (a): recency is derived from all conversations, no IsArchived
// filter). A future reversal must edit this test deliberately.
func TestRecentWorkspaces_IncludesArchivedOnlyWorkspace(t *testing.T) {
	t.Parallel()
	reg := &conversations.Registry{}
	tActive := time.Date(2026, 5, 11, 10, 0, 0, 0, time.UTC)
	tArchived := time.Date(2026, 5, 10, 10, 0, 0, 0, time.UTC)
	reg.Create(conversations.Conversation{ID: "conv-active", Cwd: "/active", IsArchived: false, LastUsedAt: tActive})
	reg.Create(conversations.Conversation{ID: "conv-archived", Cwd: "/archived", IsArchived: true, LastUsedAt: tArchived})

	_, payload := runRecentWorkspaces(t, reg, 7)

	byPath := map[string]protocol.RecentWorkspace{}
	for _, w := range payload.Workspaces {
		byPath[w.Path] = w
	}
	if len(byPath) != 2 {
		t.Fatalf("len(Workspaces): got %d, want 2 (archived-only workspace still appears)", len(byPath))
	}
	got, ok := byPath["/archived"]
	if !ok {
		t.Fatalf("/archived: missing, want present")
	}
	if !got.LastUsedAt.Equal(tArchived) {
		t.Errorf("/archived LastUsedAt: got %v, want %v", got.LastUsedAt, tArchived)
	}
}

// A conversation with an empty Cwd contributes no entry — an empty string is
// not a workspace path.
func TestRecentWorkspaces_SkipsEmptyCwd(t *testing.T) {
	t.Parallel()
	reg := &conversations.Registry{}
	ts := time.Date(2026, 5, 11, 10, 0, 0, 0, time.UTC)
	reg.Create(conversations.Conversation{ID: "conv-empty", Cwd: "", LastUsedAt: ts})
	reg.Create(conversations.Conversation{ID: "conv-real", Cwd: "/real", LastUsedAt: ts})

	_, payload := runRecentWorkspaces(t, reg, 8)

	if len(payload.Workspaces) != 1 {
		t.Fatalf("len(Workspaces): got %d, want 1 (empty Cwd skipped)", len(payload.Workspaces))
	}
	if payload.Workspaces[0].Path != "/real" {
		t.Errorf("Path: got %q, want %q", payload.Workspaces[0].Path, "/real")
	}
}
